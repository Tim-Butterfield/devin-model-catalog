// Package app is the application service shared by the CLI and the MCP
// server. Both are thin adapters over these methods; neither contains
// catalog, refresh or query logic of its own.
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/buildinfo"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/config"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/devin"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/metrics"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/query"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/retrieval"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/retrieval/browser"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources/bfcl"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources/epoch"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources/manual"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/store"
)

// Kind classifies failures for exit codes and tool errors.
type Kind int

const (
	// KindFailure is an unexpected failure.
	KindFailure Kind = 1
	// KindUsage is an invalid request.
	KindUsage Kind = 2
	// KindNotReady is missing or invalid configuration or selection.
	KindNotReady Kind = 3
	// KindSource is a source refresh failure.
	KindSource Kind = 4
)

// Error is a classified failure.
type Error struct {
	Kind Kind
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func fail(kind Kind, format string, args ...any) error {
	return &Error{Kind: kind, Err: fmt.Errorf(format, args...)}
}

// KindOf classifies any error.
func KindOf(err error) Kind {
	var appErr *Error
	var notReady *query.NotReadyError
	var invalid *query.InvalidError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &appErr):
		return appErr.Kind
	case errors.As(err, &notReady):
		return KindNotReady
	case errors.As(err, &invalid):
		return KindUsage
	}
	return KindFailure
}

// Options configures a Service.
type Options struct {
	DBPath     string
	Config     config.Resolved
	HTTP       retrieval.Fetcher
	Browser    *browser.Manager
	Collectors []sources.Collector
	DevinURL   string
	Now        func() time.Time
}

// Service is the application.
type Service struct {
	db         *store.DB
	cfg        config.Resolved
	http       retrieval.Fetcher
	browser    *browser.Manager
	collectors []sources.Collector
	devinURL   string
	now        func() time.Time
}

// BuiltinCollectors returns the evidence collectors shipped in this release.
func BuiltinCollectors() []sources.Collector {
	return []sources.Collector{epoch.New(), bfcl.New()}
}

// Open opens the database and registers the built-in reference data.
func Open(ctx context.Context, opts Options) (*Service, error) {
	if opts.DBPath == "" {
		opts.DBPath = opts.Config.DBPath
	}
	if opts.DBPath == "" {
		return nil, fail(KindNotReady, "no database path is configured")
	}
	db, err := store.Open(ctx, opts.DBPath)
	if err != nil {
		return nil, err
	}
	s := &Service{db: db, cfg: opts.Config, http: opts.HTTP, browser: opts.Browser, collectors: opts.Collectors, devinURL: opts.DevinURL, now: opts.Now}
	if s.http == nil {
		s.http = retrieval.NewHTTP(buildinfo.UserAgent())
	}
	if s.collectors == nil {
		s.collectors = BuiltinCollectors()
	}
	if s.devinURL == "" {
		s.devinURL = devin.ModelsURL
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	if s.browser == nil && opts.Config.BrowserDir != "" {
		s.browser = browser.NewManager(opts.Config.BrowserDir)
	}
	if err := s.registerReference(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Service) Close() error { return s.db.Close() }

// DB exposes the store for tests.
func (s *Service) DB() *store.DB { return s.db }

func (s *Service) registerReference(ctx context.Context) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.UpsertSource(ctx, tx, devin.Info()); err != nil {
			return err
		}
		if err := store.UpsertContext(ctx, tx, devin.PublishedContextDef()); err != nil {
			return err
		}
		for _, d := range devin.MetricDefinitions() {
			if err := store.UpsertMetric(ctx, tx, d); err != nil {
				return err
			}
		}
		for _, c := range s.collectors {
			if err := store.UpsertSource(ctx, tx, c.Info()); err != nil {
				return err
			}
			for _, ec := range c.Contexts() {
				if err := store.UpsertContext(ctx, tx, ec); err != nil {
					return err
				}
			}
			for _, d := range c.Metrics() {
				if err := store.UpsertMetric(ctx, tx, d); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ---- Datasets ---------------------------------------------------------------

// DatasetList is the stored inventory with the selection marked.
type DatasetList struct {
	Inventory           *store.InventoryState `json:"inventory"`
	Datasets            []store.DatasetRow    `json:"datasets"`
	Selected            *store.Selection      `json:"selected,omitempty"`
	SelectedDatasetID   int                   `json:"selected_dataset_id,omitempty"`
	SelectedInInventory bool                  `json:"selected_in_inventory"`
	Warnings            []string              `json:"warnings,omitempty"`
}

// Datasets lists the stored inventory.
func (s *Service) Datasets(ctx context.Context) (*DatasetList, error) {
	var rows []store.DatasetRow
	var inv *store.InventoryState
	var sel *store.Selection
	err := s.read(ctx, func(q store.Querier) error {
		var err error
		if rows, inv, err = store.ListDatasets(ctx, q); err != nil {
			return err
		}
		sel, err = store.GetSelection(ctx, q)
		return err
	})
	if err != nil {
		return nil, err
	}
	list := &DatasetList{Inventory: inv, Datasets: rows, Selected: sel}
	if list.Datasets == nil {
		list.Datasets = []store.DatasetRow{}
	}
	if inv != nil {
		list.Warnings = inv.Warnings
	}
	if sel != nil {
		for _, r := range rows {
			if r.SourceKey == sel.SourceKey {
				list.SelectedDatasetID, list.SelectedInInventory = r.ID, true
			}
		}
		if inv != nil && !list.SelectedInInventory {
			list.Warnings = append(list.Warnings, fmt.Sprintf("the selected dataset %q is not in the current inventory; select a current dataset", sel.DisplayName))
		}
	}
	return list, nil
}

// RefreshDatasets replaces the inventory from the live page.
func (s *Service) RefreshDatasets(ctx context.Context) (*DatasetList, error) {
	page, err := s.fetchPage(ctx)
	if err != nil {
		return nil, err
	}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := store.ReplaceDatasetInventory(ctx, tx, page.Datasets, s.devinURL, page.SHA256, page.Warnings, s.now())
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Datasets(ctx)
}

func (s *Service) fetchPage(ctx context.Context) (*devin.Page, error) {
	body, err := s.http.Get(ctx, s.devinURL)
	if err != nil {
		return nil, &Error{Kind: KindSource, Err: fmt.Errorf("reading the Devin models page: %w", err)}
	}
	page, err := devin.ParsePage(string(body))
	if err != nil {
		return nil, &Error{Kind: KindSource, Err: fmt.Errorf("parsing the Devin models page: %w", err)}
	}
	return page, nil
}

// Selection is the result of selecting a dataset.
type Selection struct {
	Selected *store.Selection `json:"selected"`
	Changed  bool             `json:"changed"`
	Message  string           `json:"message"`
}

// UseDataset selects a dataset by its current inventory number.
func (s *Service) UseDataset(ctx context.Context, id int) (*Selection, error) {
	rows, inv, err := store.ListDatasets(ctx, s.db.SQL())
	if err != nil {
		return nil, err
	}
	if inv == nil {
		return nil, fail(KindNotReady, "no dataset inventory yet: run `devmodels datasets refresh` first")
	}
	idx := slices.IndexFunc(rows, func(r store.DatasetRow) bool { return r.ID == id })
	if idx == -1 {
		return nil, fail(KindUsage, "no dataset has id %d; current ids are 1..%d (run `devmodels datasets`)", id, len(rows))
	}
	row := rows[idx]
	if !row.Supported {
		return nil, fail(KindUsage, "dataset %d (%s) cannot be selected: %s", id, row.DisplayName, row.UnsupportedReason)
	}

	var changed bool
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		changed, err = store.SelectDataset(ctx, tx, row, s.now())
		return err
	})
	if err != nil {
		return nil, err
	}
	sel, err := store.GetSelection(ctx, s.db.SQL())
	if err != nil {
		return nil, err
	}
	msg := fmt.Sprintf("%s is already selected", row.DisplayName)
	if changed {
		msg = fmt.Sprintf("selected %s; the previous catalog was cleared. Run `devmodels refresh` to load its models", row.DisplayName)
	} else if sel.CatalogState != store.CatalogCurrent {
		msg += "; run `devmodels refresh` to load its models"
	}
	return &Selection{Selected: sel, Changed: changed, Message: msg}, nil
}

// ---- Refresh ----------------------------------------------------------------

// RefreshOptions selects what to refresh.
type RefreshOptions struct {
	// Sources limits the refresh to these source codes; empty means all.
	Sources []string
	// AllowShrink accepts a catalog less than half the size of the current one.
	AllowShrink bool
}

// SourceReport is one source's refresh outcome.
type SourceReport struct {
	Source   string          `json:"source"`
	Status   string          `json:"status"`
	RunID    int64           `json:"run_id,omitempty"`
	Error    string          `json:"error,omitempty"`
	Counts   store.RunCounts `json:"counts"`
	Warnings []string        `json:"warnings,omitempty"`

	// Unmatched is how many of this source's stored data points describe a
	// model identity no model in the selected Devin catalog has. It is valid
	// evidence about other models, not a failure, and is reported so a large
	// number is visibly ordinary rather than alarming. It is nil for the Devin
	// catalog, whose data points are bound to catalog rows by construction,
	// and when the catalog is not current enough to compare against.
	Unmatched *int `json:"unmatched_data_points,omitempty"`
}

// RefreshReport is the outcome of a refresh.
type RefreshReport struct {
	Dataset *store.Selection `json:"dataset,omitempty"`
	// CatalogModels is how many models the selected dataset's catalog holds
	// after the refresh, so the heading can say what the dataset contains in
	// the unit users think in. It is nil when the refresh did not put that
	// count within reach, which is the only case the heading omits it.
	CatalogModels *int           `json:"catalog_models,omitempty"`
	Sources       []SourceReport `json:"sources"`
	Succeeded     bool           `json:"succeeded"`
}

// SourceCodes lists every refreshable source code.
func (s *Service) SourceCodes() []string {
	codes := []string{devin.SourceCode}
	for _, c := range s.collectors {
		codes = append(codes, c.Info().Code)
	}
	return codes
}

// Refresh verifies the selected dataset, replaces its catalog, and refreshes
// every evidence source. Each source is atomic; a failed source keeps its
// previous data.
func (s *Service) Refresh(ctx context.Context, opts RefreshOptions) (*RefreshReport, error) {
	known := s.SourceCodes()
	for _, code := range opts.Sources {
		if !slices.Contains(known, code) {
			return nil, fail(KindUsage, "unknown source %q; refreshable sources: %s", code, strings.Join(known, ", "))
		}
	}
	wants := func(code string) bool { return len(opts.Sources) == 0 || slices.Contains(opts.Sources, code) }

	report := &RefreshReport{Sources: []SourceReport{}}
	var firstErr error

	if wants(devin.SourceCode) {
		sel, err := store.GetSelection(ctx, s.db.SQL())
		if err != nil {
			return nil, err
		}
		if sel == nil {
			return nil, fail(KindNotReady, "no Devin dataset is selected: run `devmodels datasets refresh`, then `devmodels use-dataset <id>`")
		}
		rep, err := s.refreshCatalog(ctx, sel, opts.AllowShrink)
		report.Sources = append(report.Sources, rep)
		if rep.Status == "success" {
			// The catalog refresh accepted exactly the rows that became the
			// stored models, so its accepted count is the model count without
			// a further read. A failed run's count describes a catalog that
			// was never stored, so it is not used.
			models := rep.Counts.Accepted
			report.CatalogModels = &models
		}
		if err != nil {
			if KindOf(err) == KindNotReady {
				// The selection no longer matches the live source: stop, so
				// nothing proceeds as though the licensing context were valid.
				report.Dataset, _ = store.GetSelection(ctx, s.db.SQL())
				return report, err
			}
			firstErr = err
		}
	}

	for _, c := range s.collectors {
		if !wants(c.Info().Code) {
			continue
		}
		rep, err := s.refreshCollector(ctx, c)
		report.Sources = append(report.Sources, rep)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	report.Dataset, _ = store.GetSelection(ctx, s.db.SQL())
	s.annotateUnmatched(ctx, report)
	report.Succeeded = firstErr == nil
	if firstErr != nil {
		return report, &Error{Kind: KindSource, Err: fmt.Errorf("one or more sources failed to refresh; their previous data is unchanged: %w", firstErr)}
	}
	return report, nil
}

// annotateUnmatched fills in each evidence source's unmatched-data-point
// count, so the refresh output distinguishes evidence about models this
// dataset does not offer from evidence that went wrong. It is best-effort
// reporting over data already committed: a failure here leaves the counts
// absent rather than failing a refresh that succeeded.
func (s *Service) annotateUnmatched(ctx context.Context, report *RefreshReport) {
	if !slices.ContainsFunc(report.Sources, func(r SourceReport) bool { return r.Source != devin.SourceCode }) {
		// Only the catalog was refreshed, so there is nothing to annotate and
		// no reason to load a snapshot.
		return
	}
	engine, err := s.engine(ctx)
	if err != nil || engine.Ready() != nil {
		return
	}
	bySource := map[string]int{}
	for _, o := range engine.UnmatchedEvidence() {
		bySource[o.Source]++
	}
	for i := range report.Sources {
		if report.Sources[i].Source == devin.SourceCode {
			continue
		}
		count := bySource[report.Sources[i].Source]
		report.Sources[i].Unmatched = &count
	}
}

func (s *Service) finishFailed(ctx context.Context, rep *SourceReport, err error) {
	rep.Status, rep.Error = "failed", err.Error()
	// The failure is often the cancellation itself (Ctrl-C), so record it
	// without that cancellation; otherwise the run would stay "running".
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if ferr := store.FinishRun(ctx, s.db.SQL(), rep.RunID, "failed", err.Error(), rep.Counts, rep.Warnings, s.now()); ferr != nil {
		rep.Error += fmt.Sprintf(" (and recording the failure failed: %v)", ferr)
	}
}

func (s *Service) refreshCatalog(ctx context.Context, sel *store.Selection, allowShrink bool) (SourceReport, error) {
	rep := SourceReport{Source: devin.SourceCode}
	runID, err := store.StartRun(ctx, s.db.SQL(), devin.SourceCode, s.now())
	if err != nil {
		return rep, err
	}
	rep.RunID = runID

	page, err := s.fetchPage(ctx)
	if err != nil {
		s.finishFailed(ctx, &rep, err)
		return rep, err
	}

	ds, ok := page.Find(sel.SourceKey)
	if !ok || !ds.Supported {
		var available []string
		for _, d := range page.Datasets {
			state := "supported"
			if !d.Supported {
				state = "unsupported"
			}
			available = append(available, fmt.Sprintf("%d. %s (%s)", d.Position, d.DisplayName, state))
		}
		err := &Error{Kind: KindNotReady, Err: fmt.Errorf(
			"the selected dataset %q [%s] no longer matches a supported dataset on the live Devin page, so the current catalog was left unchanged. "+
				"Currently published datasets: %s. Run `devmodels datasets refresh`, then `devmodels use-dataset <id>`",
			sel.DisplayName, sel.SourceKey, strings.Join(available, "; "))}
		s.finishFailed(ctx, &rep, err)
		return rep, err
	}

	cat, err := devin.BuildCatalog(page, ds)
	if err != nil {
		err = &Error{Kind: KindSource, Err: err}
		s.finishFailed(ctx, &rep, err)
		return rep, err
	}
	rep.Counts = store.RunCounts{
		Published:  cat.PublishedRows,
		Accepted:   len(cat.Models),
		Excluded:   cat.Count(devin.DispositionExcluded),
		Rejected:   cat.Count(devin.DispositionRejected),
		DataPoints: len(cat.Observations),
	}
	rep.Warnings = cat.Warnings
	if ds.DisplayName != sel.DisplayName {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("the selected dataset is now titled %q (was %q); its structural identity is unchanged", ds.DisplayName, sel.DisplayName))
	}
	if drift := s.inventoryDrift(ctx, page); drift != "" {
		rep.Warnings = append(rep.Warnings, drift)
	}

	if sel.CatalogState == store.CatalogCurrent && !allowShrink {
		current, err := store.ListCatalogModels(ctx, s.db.SQL())
		if err != nil {
			s.finishFailed(ctx, &rep, err)
			return rep, err
		}
		if len(current) > 0 && len(cat.Models)*2 < len(current) {
			err := &Error{Kind: KindSource, Err: fmt.Errorf(
				"the refreshed catalog has %d models against %d currently; a drop of more than half is treated as an incomplete source. "+
					"If the change is genuine, rerun with --allow-shrink", len(cat.Models), len(current))}
			s.finishFailed(ctx, &rep, err)
			return rep, err
		}
	}

	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.ReplaceCatalog(ctx, tx, cat, runID, s.now()); err != nil {
			return err
		}
		return store.FinishRun(ctx, tx, runID, "success", "", rep.Counts, rep.Warnings, s.now())
	})
	if err != nil {
		s.finishFailed(ctx, &rep, err)
		return rep, err
	}
	rep.Status = "success"
	return rep, nil
}

func (s *Service) inventoryDrift(ctx context.Context, page *devin.Page) string {
	rows, inv, err := store.ListDatasets(ctx, s.db.SQL())
	if err != nil || inv == nil {
		return ""
	}
	same := len(rows) == len(page.Datasets)
	for i := 0; same && i < len(rows); i++ {
		same = rows[i].SourceKey == page.Datasets[i].SourceKey && rows[i].DisplayName == page.Datasets[i].DisplayName
	}
	if same {
		return ""
	}
	return "the live dataset list differs from the stored inventory; run `devmodels datasets refresh` to update the dataset ids"
}

func (s *Service) refreshCollector(ctx context.Context, c sources.Collector) (SourceReport, error) {
	info := c.Info()
	rep := SourceReport{Source: info.Code}
	runID, err := store.StartRun(ctx, s.db.SQL(), info.Code, s.now())
	if err != nil {
		return rep, err
	}
	rep.RunID = runID

	env := sources.Env{HTTP: s.http}
	if s.browser != nil {
		env.Browser = s.browser
	}
	res, err := c.Collect(ctx, env)
	if err == nil {
		err = res.AccountingError(info.Code)
	}
	if err == nil {
		err = checkEmitted(c, res)
	}
	rep.Counts = countsOf(res)
	rep.Warnings = res.Warnings
	if err != nil {
		s.finishFailed(ctx, &rep, err)
		return rep, err
	}

	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.ReplaceEvidence(ctx, tx, info.Code, runID, res, s.now()); err != nil {
			return err
		}
		return store.FinishRun(ctx, tx, runID, "success", "", rep.Counts, rep.Warnings, s.now())
	})
	if err != nil {
		s.finishFailed(ctx, &rep, err)
		return rep, err
	}
	rep.Status = "success"
	return rep, nil
}

// checkEmitted rejects observations of metrics or contexts the collector
// neither declared up front nor reported as used by this run, which would
// otherwise surface as foreign-key failures. A context derived from a source
// label counts as declared once the result names it, which is what lets a new
// upstream harness be ingested without a release.
func checkEmitted(c sources.Collector, res sources.Result) error {
	defs := map[string]metrics.Definition{}
	for _, d := range c.Metrics() {
		defs[d.Key] = d
	}
	ctxs := map[string]bool{}
	for _, ec := range c.Contexts() {
		ctxs[ec.Code] = true
	}
	for _, ec := range res.Contexts {
		ctxs[ec.Code] = true
	}
	for _, o := range res.Observations {
		if _, ok := defs[o.Metric]; !ok {
			return fmt.Errorf("collector %s emitted undeclared metric %q", c.Info().Code, o.Metric)
		}
		if !ctxs[o.EvaluationContext] {
			return fmt.Errorf("collector %s emitted evaluation context %q without declaring it or reporting it in the result", c.Info().Code, o.EvaluationContext)
		}
	}
	return nil
}

func countsOf(res sources.Result) store.RunCounts {
	return store.RunCounts{
		Published: res.SourceRowCount, Accepted: res.AcceptedRows, Rejected: len(res.Rejected),
		DataPoints: len(res.Observations),
	}
}

// ---- Status -------------------------------------------------------------

// Status is the operational state.
type Status struct {
	Version       string          `json:"version"`
	Paths         config.Resolved `json:"configuration"`
	SchemaVersion int             `json:"schema_version"`

	Usable   bool     `json:"usable"`
	Problems []string `json:"problems"`

	Inventory     *store.InventoryState `json:"dataset_inventory"`
	Selection     *store.Selection      `json:"selected_dataset"`
	CatalogModels int                   `json:"catalog_models"`
	CatalogRows   map[string]int        `json:"catalog_row_dispositions"`

	Sources  []store.SourceStatus `json:"sources"`
	Evidence EvidenceCounts       `json:"evidence"`
	Aliases  int                  `json:"aliases"`

	BrowserRuntime *browser.Status `json:"browser_runtime,omitempty"`
}

// EvidenceCounts summarises the two distinct ways a published row can end up
// contributing nothing to an answer. They are not the same thing, and only one
// of them suggests a problem.
type EvidenceCounts struct {
	// Rejected counts published rows this version could not represent faithfully,
	// each with an exact reason. A persistently large number is worth looking
	// at with `devmodels rejected`.
	Rejected int `json:"rejected_rows"`
	// Unmatched counts stored data points whose model identity matches no
	// model in the selected Devin catalog. Leaderboards cover a far wider
	// field than any one Devin dataset, so this is normally large and normal;
	// `devmodels unmatched` lists it with suggested identities.
	Unmatched int `json:"unmatched_data_points"`
}

// Status reports whether the data is usable and why not.
func (s *Service) Status(ctx context.Context) (*Status, error) {
	st := &Status{Version: buildinfo.String(), Paths: s.cfg, Problems: []string{}, CatalogRows: map[string]int{}}
	st.Paths.DBPath = s.db.Path()
	var snap *store.Snapshot
	var disps []devin.RowDisposition
	err := s.read(ctx, func(q store.Querier) error {
		var err error
		if _, st.Inventory, err = store.ListDatasets(ctx, q); err != nil {
			return err
		}
		if snap, err = store.LoadSnapshot(ctx, q); err != nil {
			return err
		}
		if disps, err = store.ListDispositions(ctx, q); err != nil {
			return err
		}
		st.Sources, err = store.SourceStatuses(ctx, q)
		return err
	})
	if err != nil {
		return nil, err
	}
	if st.SchemaVersion, err = s.db.SchemaVersion(ctx); err != nil {
		return nil, err
	}
	st.Selection = snap.Selection
	st.CatalogModels = len(snap.Models)
	st.Aliases = len(snap.Aliases)
	for _, d := range disps {
		st.CatalogRows[d.Disposition]++
	}
	for _, src := range st.Sources {
		st.Evidence.Rejected += src.RejectedRows
		if a := src.LastAttempt; a != nil && a.Status == "failed" {
			msg := fmt.Sprintf("source %s failed its last refresh (%s): %s", src.Info.Code, a.StartedAt, a.Error)
			if src.LastSuccess != nil {
				msg += fmt.Sprintf("; data from the successful refresh at %s is still in use", src.LastSuccess.FinishedAt)
			}
			st.Problems = append(st.Problems, msg)
		}
		if src.Info.Kind == "builtin" && src.Info.Code != devin.SourceCode && src.LastSuccess == nil {
			st.Problems = append(st.Problems, fmt.Sprintf("source %s has never been refreshed, so its metrics have no observations", src.Info.Code))
		}
	}
	engine := query.NewEngine(snap)
	st.Evidence.Unmatched = len(engine.UnmatchedEvidence())

	switch {
	case st.Inventory == nil:
		st.Problems = append(st.Problems, "no dataset inventory: run `devmodels datasets refresh`")
	case st.Selection == nil:
		st.Problems = append(st.Problems, "no dataset selected: run `devmodels use-dataset <id>`")
	}
	if err := engine.Ready(); err != nil {
		if st.Selection != nil {
			st.Problems = append(st.Problems, err.Error())
		}
	} else {
		st.Usable = true
	}
	if s.browser != nil {
		bs := s.browser.Status()
		st.BrowserRuntime = &bs
	}
	return st, nil
}

// ---- Queries ----------------------------------------------------------------

// read runs fn in one transaction, so every statement sees the same database
// state: a refresh another process commits between two statements must not
// mix two catalogs into one answer.
func (s *Service) read(ctx context.Context, fn func(q store.Querier) error) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error { return fn(tx) })
}

func (s *Service) engine(ctx context.Context) (*query.Engine, error) {
	var snap *store.Snapshot
	err := s.read(ctx, func(q store.Querier) error {
		var err error
		snap, err = store.LoadSnapshot(ctx, q)
		return err
	})
	if err != nil {
		return nil, err
	}
	return query.NewEngine(snap), nil
}

// Describe reports the available metrics, sources and contexts in the
// requested projection: "summary" (the default when empty) or "full".
func (s *Service) Describe(ctx context.Context, detail string) (*query.Description, error) {
	e, err := s.engine(ctx)
	if err != nil {
		return nil, err
	}
	return e.Describe(detail)
}

// Query evaluates a structured query against the current catalog.
func (s *Service) Query(ctx context.Context, req query.Request) (*query.Response, error) {
	e, err := s.engine(ctx)
	if err != nil {
		return nil, err
	}
	return e.Query(req)
}

// ModelDetails reports what is known about one catalog model, in the
// requested projection: "compact" (the default when empty) or "full".
func (s *Service) ModelDetails(ctx context.Context, ref, detail string) (*query.Details, error) {
	var snap *store.Snapshot
	var rejected []store.RejectedRow
	err := s.read(ctx, func(q store.Querier) error {
		var err error
		if snap, err = store.LoadSnapshot(ctx, q); err != nil {
			return err
		}
		rejected, err = store.ListRejected(ctx, q, "")
		return err
	})
	if err != nil {
		return nil, err
	}
	return query.NewEngine(snap).Details(ref, detail, rejected)
}

// RejectedEntry is a rejected row with review suggestions.
type RejectedEntry struct {
	store.RejectedRow
	Suggestions []query.Suggestion `json:"suggestions,omitempty"`
}

// UnmatchedEntry is one source, model name, metric and evaluation context for
// which stored evidence matches no current catalog model. Several stored data
// points can share all four — the same benchmark run at two reasoning efforts,
// say — so an entry can stand for more than one, and DataPoints says how many.
// Effort is taken from the first of them.
type UnmatchedEntry struct {
	Source            string `json:"source"`
	SourceModelName   string `json:"source_model_name"`
	Metric            string `json:"metric"`
	EvaluationContext string `json:"evaluation_context"`
	Effort            string `json:"effort,omitempty"`
	BaseKey           string `json:"base_key"`
	// DataPoints counts the individual stored metric values this entry stands
	// for. It is at least 1, and the entry count is not a data-point count.
	DataPoints  int                `json:"data_points"`
	Suggestions []query.Suggestion `json:"suggestions,omitempty"`
}

// Rejected lists published rows this version could not represent faithfully, each
// with its reason and any catalog identities its name resembles.
func (s *Service) Rejected(ctx context.Context, source string) ([]RejectedEntry, error) {
	var rows []store.RejectedRow
	var snap *store.Snapshot
	err := s.read(ctx, func(q store.Querier) error {
		var err error
		if rows, err = store.ListRejected(ctx, q, source); err != nil {
			return err
		}
		snap, err = store.LoadSnapshot(ctx, q)
		return err
	})
	if err != nil {
		return nil, err
	}
	e := query.NewEngine(snap)
	out := make([]RejectedEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, RejectedEntry{RejectedRow: r, Suggestions: e.Suggest(r.SourceModelName, 3)})
	}
	return out, nil
}

// Unmatched lists stored evidence that matches no current catalog model, with
// suggestions for aliases. Nothing here is applied automatically.
//
// One entry per source, model name, metric and evaluation context: repeating a
// name once per stored value would bury the identities a reader is here to
// review. Each entry counts the values it stands for, so the list can be
// summarised in data points without calling its own length one.
func (s *Service) Unmatched(ctx context.Context, source string) ([]UnmatchedEntry, error) {
	e, err := s.engine(ctx)
	if err != nil {
		return nil, err
	}
	at := map[string]int{}
	out := []UnmatchedEntry{}
	for _, o := range e.UnmatchedEvidence() {
		if source != "" && o.Source != source {
			continue
		}
		key := o.Source + "|" + o.SourceModelName + "|" + o.Metric + "|" + o.EvaluationContext
		if i, ok := at[key]; ok {
			out[i].DataPoints++
			continue
		}
		at[key] = len(out)
		out = append(out, UnmatchedEntry{
			Source: o.Source, SourceModelName: o.SourceModelName, Metric: o.Metric, EvaluationContext: o.EvaluationContext,
			Effort: o.Effort, BaseKey: o.BaseKey, DataPoints: 1, Suggestions: e.Suggest(o.SourceModelName, 3),
		})
	}
	return out, nil
}

// ---- Aliases ----------------------------------------------------------------

// AddAlias records that a source's published model name denotes a catalog
// identity. The target is written as a Devin-style label; an effort word in it
// ("Claude Opus 5 High") fixes the effort too.
func (s *Service) AddAlias(ctx context.Context, source, sourceName, targetLabel, note string) (*store.Alias, error) {
	kind, err := store.SourceKind(ctx, s.db.SQL(), source)
	if err != nil {
		return nil, err
	}
	if kind == "" || source == devin.SourceCode {
		return nil, fail(KindUsage, "aliases apply to evidence sources; %q is not one", source)
	}
	var alias store.Alias
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		alias, err = store.PutAlias(ctx, tx, source, sourceName, targetLabel, note, s.now())
		return err
	})
	if err != nil {
		return nil, &Error{Kind: KindUsage, Err: err}
	}
	return &alias, nil
}

// RemoveAlias deletes an alias.
func (s *Service) RemoveAlias(ctx context.Context, source, sourceName string) error {
	var found bool
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		found, err = store.DeleteAlias(ctx, tx, source, sourceName)
		return err
	})
	if err != nil {
		return err
	}
	if !found {
		return fail(KindUsage, "no alias for %q from %s", sourceName, source)
	}
	return nil
}

// Aliases lists aliases.
func (s *Service) Aliases(ctx context.Context) ([]store.Alias, error) {
	out, err := store.ListAliases(ctx, s.db.SQL())
	if out == nil {
		out = []store.Alias{}
	}
	return out, err
}

// ---- Manual import --------------------------------------------------------

// Import records a manual import document, replacing any previous import of
// the same source code. The whole import is one transaction.
func (s *Service) Import(ctx context.Context, raw []byte) (*SourceReport, error) {
	imp, err := manual.Parse(raw)
	if err != nil {
		return nil, &Error{Kind: KindUsage, Err: err}
	}
	rep := &SourceReport{Source: imp.Info.Code}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		kind, err := store.SourceKind(ctx, tx, imp.Info.Code)
		if err != nil {
			return err
		}
		if kind == "builtin" {
			return fail(KindUsage, "%s is a built-in source and cannot be imported", imp.Info.Code)
		}
		if err := store.UpsertSource(ctx, tx, imp.Info); err != nil {
			return err
		}
		existing, err := store.ListContexts(ctx, tx)
		if err != nil {
			return err
		}
		for _, c := range imp.Contexts {
			if i := slices.IndexFunc(existing, func(e sources.EvaluationContext) bool { return e.Code == c.Code }); i != -1 {
				// A context's kind decides whether its values are proxies, so an
				// import may reuse a context but never redefine one.
				if existing[i].Kind != c.Kind {
					return fail(KindUsage, "evaluation context %s already exists with kind %s; an import cannot change it, so use another code", c.Code, existing[i].Kind)
				}
				continue
			}
			if err := store.UpsertContext(ctx, tx, c); err != nil {
				return err
			}
		}
		for _, d := range imp.Metrics {
			if err := store.UpsertMetric(ctx, tx, d); err != nil {
				return &Error{Kind: KindUsage, Err: err}
			}
		}
		defList, err := store.ListMetrics(ctx, tx)
		if err != nil {
			return err
		}
		defs := map[string]metrics.Definition{}
		for _, d := range defList {
			defs[d.Key] = d
		}
		ctxList, err := store.ListContexts(ctx, tx)
		if err != nil {
			return err
		}
		ctxs := map[string]bool{}
		for _, c := range ctxList {
			ctxs[c.Code] = true
		}
		res, err := imp.Observations(defs, ctxs)
		if err != nil {
			return &Error{Kind: KindUsage, Err: err}
		}
		runID, err := store.StartRun(ctx, tx, imp.Info.Code, s.now())
		if err != nil {
			return err
		}
		rep.RunID, rep.Counts = runID, countsOf(res)
		if err := store.ReplaceEvidence(ctx, tx, imp.Info.Code, runID, res, s.now()); err != nil {
			return err
		}
		return store.FinishRun(ctx, tx, runID, "success", "", rep.Counts, nil, s.now())
	})
	if err != nil {
		return nil, err
	}
	rep.Status = "success"
	return rep, nil
}

// RemoveImport deletes a manual source and what it defined.
func (s *Service) RemoveImport(ctx context.Context, code string) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.RemoveManualSource(ctx, tx, code); err != nil {
			return &Error{Kind: KindUsage, Err: err}
		}
		return nil
	})
}

// ---- Browser runtime ------------------------------------------------------

// BrowserStatus reports the browser runtime.
func (s *Service) BrowserStatus() (*browser.Status, error) {
	if s.browser == nil {
		return nil, fail(KindNotReady, "no browser runtime directory is configured")
	}
	st := s.browser.Status()
	return &st, nil
}

// BrowserCheckResult reports a real end-to-end run of the browser runtime
// against a built-in page; no site is contacted.
type BrowserCheckResult struct {
	Runtime    *browser.Status `json:"runtime"`
	Navigated  bool            `json:"navigated"`
	Interacted bool            `json:"clicked_and_waited"`
	Evaluated  bool            `json:"evaluated"`
	UserAgent  string          `json:"user_agent,omitempty"`
	DurationMS int64           `json:"duration_ms"`
}

// BrowserCheck provisions the runtime if needed and drives a self-test page:
// navigate, click, wait for a delayed state change, read text, and evaluate
// JavaScript.
func (s *Service) BrowserCheck(ctx context.Context) (*BrowserCheckResult, error) {
	if s.browser == nil {
		return nil, fail(KindNotReady, "no browser runtime directory is configured")
	}
	began := time.Now()
	res := &BrowserCheckResult{}
	err := s.browser.WithPage(ctx, func(p retrieval.Page) error {
		if err := p.Navigate(browser.SelfTestURL); err != nil {
			return err
		}
		res.Navigated = true
		if err := p.Click("#go"); err != nil {
			return err
		}
		if err := p.WaitFor(`document.body.dataset.state === "done"`); err != nil {
			return err
		}
		text, err := p.Text("#out")
		if err != nil {
			return err
		}
		if strings.TrimSpace(text) != "clicked" {
			return fmt.Errorf("the self-test page did not update after the click (text %q)", text)
		}
		res.Interacted = true
		var out struct {
			Sum int    `json:"sum"`
			UA  string `json:"ua"`
		}
		if err := p.Evaluate(`({sum: 20 + 22, ua: navigator.userAgent})`, &out); err != nil {
			return err
		}
		if out.Sum != 42 {
			return fmt.Errorf("script evaluation returned %d, want 42", out.Sum)
		}
		res.Evaluated, res.UserAgent = true, out.UA
		return nil
	})
	st := s.browser.Status()
	res.Runtime = &st
	res.DurationMS = time.Since(began).Milliseconds()
	if err != nil {
		return res, &Error{Kind: KindSource, Err: fmt.Errorf("browser runtime check failed: %w", err)}
	}
	return res, nil
}

// BrowserInstall provisions the browser runtime ahead of first use.
func (s *Service) BrowserInstall(ctx context.Context) (*browser.Status, error) {
	if s.browser == nil {
		return nil, fail(KindNotReady, "no browser runtime directory is configured")
	}
	st, err := s.browser.Ensure(ctx)
	if err != nil {
		return &st, &Error{Kind: KindSource, Err: err}
	}
	return &st, nil
}
