package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/devin"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/identity"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/metrics"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources"
)

// ---- Reference data -------------------------------------------------------

// UpsertSource records a source's description and access basis.
func UpsertSource(ctx context.Context, q Querier, info sources.Info) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO sources (code, name, kind, homepage, url, retrieval, access_basis, license, attribution)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (code) DO UPDATE SET
			name = excluded.name, kind = excluded.kind, homepage = excluded.homepage, url = excluded.url,
			retrieval = excluded.retrieval, access_basis = excluded.access_basis,
			license = excluded.license, attribution = excluded.attribution`,
		info.Code, info.Name, info.Kind, info.Homepage, info.URL, info.Retrieval, info.AccessBasis, info.License, info.Attribution)
	if err != nil {
		return fmt.Errorf("recording source %s: %w", info.Code, err)
	}
	return nil
}

// UpsertMetric records a metric definition. A metric may only be redefined by
// the source that introduced it.
func UpsertMetric(ctx context.Context, q Querier, d metrics.Definition) error {
	if err := d.Validate(); err != nil {
		return err
	}
	var owner, kind string
	err := q.QueryRowContext(ctx, `SELECT defined_by, value_kind FROM metrics WHERE key = ?`, d.Key).Scan(&owner, &kind)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	case owner != d.DefinedBy:
		return fmt.Errorf("metric %s is already defined by source %s and cannot be redefined by %s", d.Key, owner, d.DefinedBy)
	}
	_, err = q.ExecContext(ctx, `
		INSERT INTO metrics (key, display_name, description, value_kind, unit, direction, scope, defined_by, missing_possible, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET
			display_name = excluded.display_name, description = excluded.description, value_kind = excluded.value_kind,
			unit = excluded.unit, direction = excluded.direction, scope = excluded.scope,
			missing_possible = excluded.missing_possible, status = excluded.status`,
		d.Key, d.DisplayName, d.Description, d.ValueKind, d.Unit, d.Direction, d.Scope, d.DefinedBy, boolInt(d.MissingPossible), d.Status)
	if err != nil {
		return fmt.Errorf("recording metric %s: %w", d.Key, err)
	}
	return nil
}

// UpsertContext records an evaluation context.
func UpsertContext(ctx context.Context, q Querier, c sources.EvaluationContext) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO evaluation_contexts (code, name, kind) VALUES (?, ?, ?)
		ON CONFLICT (code) DO UPDATE SET name = excluded.name, kind = excluded.kind`, c.Code, c.Name, c.Kind)
	if err != nil {
		return fmt.Errorf("recording evaluation context %s: %w", c.Code, err)
	}
	return nil
}

// ContextKind returns a stored context's kind, or "" when it is unknown.
func ContextKind(ctx context.Context, q Querier, code string) (sources.ContextKind, error) {
	var kind string
	err := q.QueryRowContext(ctx, `SELECT kind FROM evaluation_contexts WHERE code = ?`, code).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return sources.ContextKind(kind), err
}

// ---- Dataset inventory and selection -------------------------------------

// DatasetRow is an inventory entry with its current selection number.
type DatasetRow struct {
	ID int `json:"id"`
	devin.Dataset
}

// InventoryState describes the last successful inventory refresh.
type InventoryState struct {
	RefreshedAt string   `json:"refreshed_at"`
	SourceURL   string   `json:"source_url"`
	SHA256      string   `json:"content_sha256"`
	Count       int      `json:"dataset_count"`
	Warnings    []string `json:"warnings,omitempty"`
}

// ReplaceDatasetInventory replaces the whole inventory, numbering datasets
// 1..N in page order.
func ReplaceDatasetInventory(ctx context.Context, q Querier, datasets []devin.Dataset, url, sha string, warnings []string, now time.Time) ([]DatasetRow, error) {
	if _, err := q.ExecContext(ctx, `DELETE FROM devin_datasets`); err != nil {
		return nil, err
	}
	rows := make([]DatasetRow, 0, len(datasets))
	for i, d := range datasets {
		row := DatasetRow{ID: i + 1, Dataset: d}
		attrs, _ := json.Marshal(d.Attributes)
		if _, err := q.ExecContext(ctx, `
			INSERT INTO devin_datasets (id, source_key, display_name, component, attributes_json, position, supported, unsupported_reason, model_row_count)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.ID, d.SourceKey, d.DisplayName, d.Component, string(attrs), d.Position, boolInt(d.Supported), d.UnsupportedReason, d.ModelRowCount); err != nil {
			return nil, fmt.Errorf("recording dataset %q: %w", d.DisplayName, err)
		}
		rows = append(rows, row)
	}
	warn, _ := json.Marshal(nonNil(warnings))
	if _, err := q.ExecContext(ctx, `
		INSERT INTO dataset_inventory (singleton, refreshed_at, source_url, content_sha256, dataset_count, warnings_json)
		VALUES (1, ?, ?, ?, ?, ?)
		ON CONFLICT (singleton) DO UPDATE SET refreshed_at = excluded.refreshed_at, source_url = excluded.source_url,
			content_sha256 = excluded.content_sha256, dataset_count = excluded.dataset_count, warnings_json = excluded.warnings_json`,
		timestamp(now), url, sha, len(datasets), string(warn)); err != nil {
		return nil, err
	}
	return rows, nil
}

// ListDatasets returns the current inventory and its state (nil if never refreshed).
func ListDatasets(ctx context.Context, q Querier) ([]DatasetRow, *InventoryState, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, source_key, display_name, component, attributes_json, position, supported, unsupported_reason, model_row_count
		FROM devin_datasets ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	var out []DatasetRow
	for rows.Next() {
		var r DatasetRow
		var attrs string
		var supported int
		if err := rows.Scan(&r.ID, &r.SourceKey, &r.DisplayName, &r.Component, &attrs, &r.Position, &supported, &r.UnsupportedReason, &r.ModelRowCount); err != nil {
			rows.Close()
			return nil, nil, err
		}
		r.Supported = supported == 1
		_ = json.Unmarshal([]byte(attrs), &r.Attributes)
		out = append(out, r)
	}
	// The rows must be released before the next statement on the single
	// connection, and an iteration error must not pass as a short inventory.
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, nil, err
	}

	var st InventoryState
	var warn string
	err = q.QueryRowContext(ctx, `SELECT refreshed_at, source_url, content_sha256, dataset_count, warnings_json FROM dataset_inventory WHERE singleton = 1`).
		Scan(&st.RefreshedAt, &st.SourceURL, &st.SHA256, &st.Count, &warn)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	_ = json.Unmarshal([]byte(warn), &st.Warnings)
	return out, &st, nil
}

// Catalog states.
const (
	CatalogNeedsRefresh = "needs_refresh"
	CatalogCurrent      = "current"
)

// Selection is the persisted selected dataset.
type Selection struct {
	SourceKey          string            `json:"source_key"`
	DisplayName        string            `json:"display_name"`
	Component          string            `json:"component"`
	Attributes         map[string]string `json:"attributes"`
	SelectedAt         string            `json:"selected_at"`
	CatalogState       string            `json:"catalog_state"`
	CatalogRefreshedAt string            `json:"catalog_refreshed_at,omitempty"`
	CatalogRunID       int64             `json:"catalog_run_id,omitempty"`
}

// GetSelection returns the selection, or nil when none is made.
func GetSelection(ctx context.Context, q Querier) (*Selection, error) {
	var s Selection
	var attrs string
	var refreshed sql.NullString
	var runID sql.NullInt64
	err := q.QueryRowContext(ctx, `
		SELECT source_key, display_name, component, attributes_json, selected_at, catalog_state, catalog_refreshed_at, catalog_run_id
		FROM selected_dataset WHERE singleton = 1`).
		Scan(&s.SourceKey, &s.DisplayName, &s.Component, &attrs, &s.SelectedAt, &s.CatalogState, &refreshed, &runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(attrs), &s.Attributes)
	s.CatalogRefreshedAt = refreshed.String
	s.CatalogRunID = runID.Int64
	return &s, nil
}

// SelectDataset records a new selection. Selecting a different dataset clears
// the previous dataset's catalog in the same transaction, so no query can
// return models from the prior licensing context. Re-selecting the current
// dataset keeps its catalog.
func SelectDataset(ctx context.Context, q Querier, ds DatasetRow, now time.Time) (changed bool, err error) {
	prev, err := GetSelection(ctx, q)
	if err != nil {
		return false, err
	}
	if prev != nil && prev.SourceKey == ds.SourceKey {
		return false, nil
	}
	if err := clearCatalog(ctx, q); err != nil {
		return false, err
	}
	attrs, _ := json.Marshal(ds.Attributes)
	_, err = q.ExecContext(ctx, `
		INSERT INTO selected_dataset (singleton, source_key, display_name, component, attributes_json, selected_at, catalog_state, catalog_refreshed_at, catalog_run_id)
		VALUES (1, ?, ?, ?, ?, ?, ?, NULL, NULL)
		ON CONFLICT (singleton) DO UPDATE SET source_key = excluded.source_key, display_name = excluded.display_name,
			component = excluded.component, attributes_json = excluded.attributes_json, selected_at = excluded.selected_at,
			catalog_state = excluded.catalog_state, catalog_refreshed_at = NULL, catalog_run_id = NULL`,
		ds.SourceKey, ds.DisplayName, ds.Component, string(attrs), timestamp(now), CatalogNeedsRefresh)
	return true, err
}

func clearCatalog(ctx context.Context, q Querier) error {
	for _, stmt := range []string{
		`DELETE FROM catalog_models`,
		`DELETE FROM catalog_row_dispositions`,
		`DELETE FROM observations WHERE catalog_uid <> ''`,
	} {
		if _, err := q.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("clearing the previous dataset's catalog: %w", err)
		}
	}
	return nil
}

// ReplaceCatalog atomically replaces the selected dataset's catalog. Call it
// only inside a transaction, and only with a complete, verified catalog.
func ReplaceCatalog(ctx context.Context, q Querier, cat *devin.Catalog, runID int64, now time.Time) error {
	if err := clearCatalog(ctx, q); err != nil {
		return err
	}
	for i, m := range cat.Models {
		if _, err := q.ExecContext(ctx, `
			INSERT INTO catalog_models (uid, row_order, label, provider, base_name, base_key, effort, serving_variant, context_variant, canonical_key, raw_json, run_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.UID, i, m.Label, m.Provider, m.Variant.BaseName, m.Variant.BaseKey(), m.Variant.Effort, m.Variant.Serving,
			m.Variant.ContextVariant, m.Variant.CanonicalKey(), m.RawJSON, runID); err != nil {
			return fmt.Errorf("recording catalog model %s: %w", m.UID, err)
		}
	}
	for i, d := range cat.Dispositions {
		if _, err := q.ExecContext(ctx, `
			INSERT INTO catalog_row_dispositions (row_ref, row_order, model_uid, label, disposition, reason, run_id)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, d.RowRef, i, d.UID, d.Label, d.Disposition, d.Reason, runID); err != nil {
			return fmt.Errorf("recording disposition of %s: %w", d.RowRef, err)
		}
	}
	if err := insertObservations(ctx, q, devin.SourceCode, runID, cat.Observations, now); err != nil {
		return err
	}
	attrs, _ := json.Marshal(cat.Dataset.Attributes)
	res, err := q.ExecContext(ctx, `
		UPDATE selected_dataset SET catalog_state = ?, catalog_refreshed_at = ?, catalog_run_id = ?, display_name = ?, attributes_json = ?
		WHERE singleton = 1 AND source_key = ?`,
		CatalogCurrent, timestamp(now), runID, cat.Dataset.DisplayName, string(attrs), cat.Dataset.SourceKey)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("the selected dataset changed while the catalog was being refreshed")
	}
	return nil
}

// ---- Source runs ----------------------------------------------------------

// RunCounts summarises one run. Published always equals Accepted + Excluded +
// Rejected: every row the source published ends in exactly one of those three
// states.
type RunCounts struct {
	// Published is how many rows the source published for this run.
	Published int `json:"published_rows"`
	// Accepted is how many of them became catalog models or observations.
	Accepted int `json:"accepted_rows"`
	// Excluded is how many the source itself hides from its own display.
	// Only the Devin catalog has such rows; it is 0 for evidence sources.
	Excluded int `json:"excluded_rows"`
	// Rejected is how many could not be represented faithfully, each with an
	// exact recorded reason.
	Rejected int `json:"rejected_rows"`
	// DataPoints is how many individual metric values the accepted rows
	// produced. One accepted row often produces several: a Devin catalog row
	// publishes input, output and cache prices, its recommendation state and
	// any long-context rates, so this count far exceeds Accepted. A benchmark
	// row commonly produces one, but nothing guarantees that.
	DataPoints int `json:"data_points"`
}

// Run is a recorded source run.
type Run struct {
	ID         int64     `json:"id"`
	Source     string    `json:"source"`
	StartedAt  string    `json:"started_at"`
	FinishedAt string    `json:"finished_at,omitempty"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	Counts     RunCounts `json:"counts"`
	Warnings   []string  `json:"warnings,omitempty"`
}

// StartRun records the start of a source run.
func StartRun(ctx context.Context, q Querier, source string, now time.Time) (int64, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO source_runs (source_code, started_at, status) VALUES (?, ?, 'running')`, source, timestamp(now))
	if err != nil {
		return 0, fmt.Errorf("recording the start of a %s run: %w", source, err)
	}
	return res.LastInsertId()
}

// FinishRun records a run's outcome and prunes old runs of the source.
func FinishRun(ctx context.Context, q Querier, runID int64, status, errMsg string, counts RunCounts, warnings []string, now time.Time) error {
	warn, _ := json.Marshal(nonNil(warnings))
	_, err := q.ExecContext(ctx, `
		UPDATE source_runs SET finished_at = ?, status = ?, error = ?, published_rows = ?, accepted_rows = ?,
			excluded_rows = ?, rejected_rows = ?, observation_count = ?, warnings_json = ?
		WHERE id = ?`,
		timestamp(now), status, errMsg, counts.Published, counts.Accepted, counts.Excluded, counts.Rejected,
		counts.DataPoints, string(warn), runID)
	if err != nil {
		return err
	}
	// Keep a short operational tail, never a history warehouse.
	_, err = q.ExecContext(ctx, `
		DELETE FROM source_runs WHERE source_code = (SELECT source_code FROM source_runs WHERE id = ?)
		AND id NOT IN (SELECT id FROM source_runs WHERE source_code = (SELECT source_code FROM source_runs WHERE id = ?) ORDER BY id DESC LIMIT 10)
		AND status <> 'running'`, runID, runID)
	return err
}

// ReplaceEvidence atomically replaces everything a source last published.
// Call it only inside a transaction, and only with a complete result.
func ReplaceEvidence(ctx context.Context, q Querier, source string, runID int64, res sources.Result, now time.Time) error {
	// Contexts first: an observation's evaluation_context is a foreign key, so
	// a context this run derived from a source label must exist before the
	// observations that cite it.
	for _, ec := range res.Contexts {
		if err := upsertRunContext(ctx, q, source, ec); err != nil {
			return err
		}
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM observations WHERE source_code = ?`, source); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM rejected_rows WHERE source_code = ?`, source); err != nil {
		return err
	}
	if err := insertObservations(ctx, q, source, runID, res.Observations, now); err != nil {
		return err
	}
	for _, r := range res.Rejected {
		details, _ := json.Marshal(nonNilMap(r.Details))
		if _, err := q.ExecContext(ctx, `
			INSERT INTO rejected_rows (source_code, run_id, source_model_name, source_row_ref, reason, details_json)
			VALUES (?, ?, ?, ?, ?, ?)`,
			source, runID, r.SourceModelName, r.SourceRowRef, r.Reason, string(details)); err != nil {
			return fmt.Errorf("recording rejected row %s: %w", r.SourceRowRef, err)
		}
	}
	return nil
}

// upsertRunContext records a context a collection used. A context's kind
// decides whether its values are proxies, so reclassifying one would silently
// change what every stored observation citing it means: that fails the refresh
// instead, leaving the previous evidence in place.
func upsertRunContext(ctx context.Context, q Querier, source string, ec sources.EvaluationContext) error {
	if !sources.ValidContextKind(ec.Kind) {
		return fmt.Errorf("source %s produced evaluation context %s with kind %q, which is not one this version defines", source, ec.Code, ec.Kind)
	}
	existing, err := ContextKind(ctx, q, ec.Code)
	if err != nil {
		return err
	}
	if existing != "" && existing != ec.Kind {
		return fmt.Errorf("source %s reports evaluation context %s as kind %s, but it is already recorded as kind %s; "+
			"a context cannot change kind, because that would change whether every value measured in it is a proxy", source, ec.Code, ec.Kind, existing)
	}
	return UpsertContext(ctx, q, ec)
}

func insertObservations(ctx context.Context, q Querier, source string, runID int64, obs []sources.Observation, now time.Time) error {
	for _, o := range obs {
		if !o.Value.Set() {
			return fmt.Errorf("observation %s/%s from %s has no single value", o.Metric, o.SourceRowRef, source)
		}
		var num sql.NullFloat64
		var text sql.NullString
		switch {
		case o.Value.Number != nil:
			num = sql.NullFloat64{Float64: *o.Value.Number, Valid: true}
		case o.Value.Bool != nil:
			num = sql.NullFloat64{Float64: float64(boolInt(*o.Value.Bool)), Valid: true}
		case o.Value.Text != nil:
			text = sql.NullString{String: *o.Value.Text, Valid: true}
		}
		details, _ := json.Marshal(nonNilMap(o.Details))
		if _, err := q.ExecContext(ctx, `
			INSERT INTO observations (source_code, run_id, metric_key, catalog_uid, source_model_name, source_row_ref, base_key, effort,
				serving_variant, context_variant, evaluation_context, value_number, value_text, details_json, observed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			source, runID, o.Metric, o.CatalogUID, o.SourceModelName, o.SourceRowRef, o.BaseKey, o.Effort, o.Serving,
			o.ContextVariant, o.EvaluationContext, num, text, string(details), timestamp(now)); err != nil {
			return fmt.Errorf("recording observation %s of %s (%s): %w", o.Metric, o.SourceModelName, o.SourceRowRef, err)
		}
	}
	return nil
}

// ---- Snapshot for querying ------------------------------------------------

// CatalogModel is a stored catalog model.
type CatalogModel struct {
	UID            string `json:"uid"`
	Label          string `json:"label"`
	Provider       string `json:"provider"`
	BaseName       string `json:"base_name"`
	BaseKey        string `json:"base_key"`
	Effort         string `json:"effort,omitempty"`
	ServingVariant string `json:"serving_variant,omitempty"`
	ContextVariant string `json:"context_variant,omitempty"`
	CanonicalKey   string `json:"canonical_key"`
	RawJSON        string `json:"-"`
}

// ObservationRow is a stored observation.
type ObservationRow struct {
	ID                int64
	Source            string
	RunID             int64
	Metric            string
	CatalogUID        string
	SourceModelName   string
	SourceRowRef      string
	BaseKey           string
	Effort            string
	Serving           string
	ContextVariant    string
	EvaluationContext string
	Number            *float64
	Text              *string
	Details           map[string]string
	ObservedAt        string
}

// Alias is a confirmed identity decision.
type Alias struct {
	SourceCode           string `json:"source"`
	SourceName           string `json:"source_name"`
	SourceNameNormalized string `json:"source_name_normalized"`
	TargetLabel          string `json:"target_label"`
	BaseKey              string `json:"base_key"`
	Effort               string `json:"effort,omitempty"`
	Note                 string `json:"note,omitempty"`
	CreatedAt            string `json:"created_at"`
}

// Snapshot is everything the query layer reads, loaded at once.
type Snapshot struct {
	Selection    *Selection
	Models       []CatalogModel
	MetricOrder  []string
	Metrics      map[string]metrics.Definition
	Sources      map[string]sources.Info
	Contexts     map[string]sources.EvaluationContext
	Observations []ObservationRow
	Aliases      []Alias
}

// LoadSnapshot reads the current state.
func LoadSnapshot(ctx context.Context, q Querier) (*Snapshot, error) {
	snap := &Snapshot{
		Metrics:  map[string]metrics.Definition{},
		Sources:  map[string]sources.Info{},
		Contexts: map[string]sources.EvaluationContext{},
	}
	var err error
	if snap.Selection, err = GetSelection(ctx, q); err != nil {
		return nil, err
	}
	if snap.Models, err = ListCatalogModels(ctx, q); err != nil {
		return nil, err
	}
	defs, err := ListMetrics(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, d := range defs {
		snap.MetricOrder = append(snap.MetricOrder, d.Key)
		snap.Metrics[d.Key] = d
	}
	infos, err := ListSources(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, s := range infos {
		snap.Sources[s.Code] = s
	}
	ctxs, err := ListContexts(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, c := range ctxs {
		snap.Contexts[c.Code] = c
	}
	if snap.Observations, err = listObservations(ctx, q); err != nil {
		return nil, err
	}
	if snap.Aliases, err = ListAliases(ctx, q); err != nil {
		return nil, err
	}
	return snap, nil
}

// ListCatalogModels returns the current catalog in page order.
func ListCatalogModels(ctx context.Context, q Querier) ([]CatalogModel, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT uid, label, provider, base_name, base_key, effort, serving_variant, context_variant, canonical_key, raw_json
		FROM catalog_models ORDER BY row_order`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CatalogModel
	for rows.Next() {
		var m CatalogModel
		if err := rows.Scan(&m.UID, &m.Label, &m.Provider, &m.BaseName, &m.BaseKey, &m.Effort, &m.ServingVariant, &m.ContextVariant, &m.CanonicalKey, &m.RawJSON); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListMetrics returns every metric definition, ordered by source then key.
func ListMetrics(ctx context.Context, q Querier) ([]metrics.Definition, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT key, display_name, description, value_kind, unit, direction, scope, defined_by, missing_possible, status
		FROM metrics ORDER BY scope, defined_by, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metrics.Definition
	for rows.Next() {
		var d metrics.Definition
		var missing int
		if err := rows.Scan(&d.Key, &d.DisplayName, &d.Description, &d.ValueKind, &d.Unit, &d.Direction, &d.Scope, &d.DefinedBy, &missing, &d.Status); err != nil {
			return nil, err
		}
		d.MissingPossible = missing == 1
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListSources returns every known source.
func ListSources(ctx context.Context, q Querier) ([]sources.Info, error) {
	rows, err := q.QueryContext(ctx, `SELECT code, name, kind, homepage, url, retrieval, access_basis, license, attribution FROM sources ORDER BY kind, code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sources.Info
	for rows.Next() {
		var s sources.Info
		if err := rows.Scan(&s.Code, &s.Name, &s.Kind, &s.Homepage, &s.URL, &s.Retrieval, &s.AccessBasis, &s.License, &s.Attribution); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListContexts returns every known evaluation context.
func ListContexts(ctx context.Context, q Querier) ([]sources.EvaluationContext, error) {
	rows, err := q.QueryContext(ctx, `SELECT code, name, kind FROM evaluation_contexts ORDER BY kind, code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sources.EvaluationContext
	for rows.Next() {
		var c sources.EvaluationContext
		if err := rows.Scan(&c.Code, &c.Name, &c.Kind); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func listObservations(ctx context.Context, q Querier) ([]ObservationRow, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, source_code, run_id, metric_key, catalog_uid, source_model_name, source_row_ref, base_key, effort, serving_variant,
			context_variant, evaluation_context, value_number, value_text, details_json, observed_at
		FROM observations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObservationRow
	for rows.Next() {
		var o ObservationRow
		var num sql.NullFloat64
		var text sql.NullString
		var details string
		if err := rows.Scan(&o.ID, &o.Source, &o.RunID, &o.Metric, &o.CatalogUID, &o.SourceModelName, &o.SourceRowRef, &o.BaseKey,
			&o.Effort, &o.Serving, &o.ContextVariant, &o.EvaluationContext, &num, &text, &details, &o.ObservedAt); err != nil {
			return nil, err
		}
		if num.Valid {
			o.Number = &num.Float64
		}
		if text.Valid {
			o.Text = &text.String
		}
		_ = json.Unmarshal([]byte(details), &o.Details)
		out = append(out, o)
	}
	return out, rows.Err()
}

// ---- Status and diagnostics ----------------------------------------------

// SourceStatus is one source's operational state.
type SourceStatus struct {
	Info        sources.Info `json:"source"`
	LastAttempt *Run         `json:"last_attempt,omitempty"`
	LastSuccess *Run         `json:"last_success,omitempty"`
	// DataPoints is how many individual metric values this source currently
	// contributes, not how many models or rows it covers.
	DataPoints int `json:"data_points"`
	// RejectedRows is how many of its published rows could not be represented
	// faithfully, each with a recorded reason. It counts rows, which is why it
	// does not say "count" beside a field that counts values.
	RejectedRows int `json:"rejected_rows"`
}

// SourceStatuses reports every source's latest runs and current counts.
func SourceStatuses(ctx context.Context, q Querier) ([]SourceStatus, error) {
	infos, err := ListSources(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]SourceStatus, 0, len(infos))
	for _, info := range infos {
		st := SourceStatus{Info: info}
		if st.LastAttempt, err = latestRun(ctx, q, info.Code, false); err != nil {
			return nil, err
		}
		if st.LastSuccess, err = latestRun(ctx, q, info.Code, true); err != nil {
			return nil, err
		}
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM observations WHERE source_code = ?`, info.Code).Scan(&st.DataPoints); err != nil {
			return nil, err
		}
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM rejected_rows WHERE source_code = ?`, info.Code).Scan(&st.RejectedRows); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

func latestRun(ctx context.Context, q Querier, source string, successOnly bool) (*Run, error) {
	query := `SELECT id, source_code, started_at, COALESCE(finished_at, ''), status, error, published_rows, accepted_rows,
		excluded_rows, rejected_rows, observation_count, warnings_json FROM source_runs WHERE source_code = ?`
	if successOnly {
		query += ` AND status = 'success'`
	}
	query += ` ORDER BY id DESC LIMIT 1`
	var r Run
	var warn string
	err := q.QueryRowContext(ctx, query, source).Scan(&r.ID, &r.Source, &r.StartedAt, &r.FinishedAt, &r.Status, &r.Error,
		&r.Counts.Published, &r.Counts.Accepted, &r.Counts.Excluded, &r.Counts.Rejected, &r.Counts.DataPoints, &warn)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(warn), &r.Warnings)
	return &r, nil
}

// RejectedRow is a stored rejection: a published row this version could not
// represent faithfully, with the exact reason.
type RejectedRow struct {
	ID              int64             `json:"id"`
	Source          string            `json:"source"`
	SourceModelName string            `json:"source_model_name"`
	SourceRowRef    string            `json:"source_row_ref"`
	Reason          string            `json:"reason"`
	Details         map[string]string `json:"details,omitempty"`
}

// ListRejected returns rejected rows, optionally limited to one source.
func ListRejected(ctx context.Context, q Querier, source string) ([]RejectedRow, error) {
	query := `SELECT id, source_code, source_model_name, source_row_ref, reason, details_json FROM rejected_rows`
	var args []any
	if source != "" {
		query += ` WHERE source_code = ?`
		args = append(args, source)
	}
	query += ` ORDER BY source_code, id`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RejectedRow
	for rows.Next() {
		var r RejectedRow
		var details string
		if err := rows.Scan(&r.ID, &r.Source, &r.SourceModelName, &r.SourceRowRef, &r.Reason, &details); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(details), &r.Details)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListDispositions returns what became of every row of the selected dataset.
func ListDispositions(ctx context.Context, q Querier) ([]devin.RowDisposition, error) {
	rows, err := q.QueryContext(ctx, `SELECT row_ref, model_uid, label, disposition, reason FROM catalog_row_dispositions ORDER BY row_order`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []devin.RowDisposition
	for rows.Next() {
		var d devin.RowDisposition
		if err := rows.Scan(&d.RowRef, &d.UID, &d.Label, &d.Disposition, &d.Reason); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---- Aliases --------------------------------------------------------------

// PutAlias records a confirmed identity decision.
func PutAlias(ctx context.Context, q Querier, source, sourceName, targetLabel, note string, now time.Time) (Alias, error) {
	target := identity.Classify(targetLabel)
	a := Alias{
		SourceCode: source, SourceName: sourceName, SourceNameNormalized: identity.Normalise(sourceName),
		TargetLabel: targetLabel, BaseKey: target.BaseKey(), Effort: string(target.Effort), Note: note, CreatedAt: timestamp(now),
	}
	if a.SourceNameNormalized == "" || a.BaseKey == "" {
		return Alias{}, fmt.Errorf("an alias needs a non-empty source name and target")
	}
	_, err := q.ExecContext(ctx, `
		INSERT INTO model_aliases (source_code, source_name_normalized, source_name, target_label, base_key, effort, note, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source_code, source_name_normalized) DO UPDATE SET source_name = excluded.source_name, target_label = excluded.target_label,
			base_key = excluded.base_key, effort = excluded.effort, note = excluded.note, created_at = excluded.created_at`,
		a.SourceCode, a.SourceNameNormalized, a.SourceName, a.TargetLabel, a.BaseKey, a.Effort, a.Note, a.CreatedAt)
	return a, err
}

// DeleteAlias removes an alias, reporting whether one existed.
func DeleteAlias(ctx context.Context, q Querier, source, sourceName string) (bool, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM model_aliases WHERE source_code = ? AND source_name_normalized = ?`, source, identity.Normalise(sourceName))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListAliases returns every alias.
func ListAliases(ctx context.Context, q Querier) ([]Alias, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT source_code, source_name, source_name_normalized, target_label, base_key, effort, note, created_at
		FROM model_aliases ORDER BY source_code, source_name_normalized`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alias
	for rows.Next() {
		var a Alias
		if err := rows.Scan(&a.SourceCode, &a.SourceName, &a.SourceNameNormalized, &a.TargetLabel, &a.BaseKey, &a.Effort, &a.Note, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- Manual sources -------------------------------------------------------

// SourceKind returns a source's kind, or "" when it is unknown.
func SourceKind(ctx context.Context, q Querier, code string) (string, error) {
	var kind string
	err := q.QueryRowContext(ctx, `SELECT kind FROM sources WHERE code = ?`, code).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return kind, err
}

// RemoveManualSource deletes a manually imported source, its observations,
// its runs, and the metrics it defined that no other source still reports.
func RemoveManualSource(ctx context.Context, q Querier, code string) error {
	kind, err := SourceKind(ctx, q, code)
	if err != nil {
		return err
	}
	if kind != "manual" {
		return fmt.Errorf("%q is not a manually imported source", code)
	}
	var others int
	if err := q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM observations o JOIN metrics m ON m.key = o.metric_key
		WHERE m.defined_by = ? AND o.source_code <> ?`, code, code).Scan(&others); err != nil {
		return err
	}
	if others > 0 {
		return fmt.Errorf("metrics defined by %s still carry %d observations from other sources; remove those sources first", code, others)
	}
	for _, stmt := range []string{
		`DELETE FROM observations WHERE source_code = ?`,
		`DELETE FROM rejected_rows WHERE source_code = ?`,
		`DELETE FROM source_runs WHERE source_code = ?`,
		`DELETE FROM model_aliases WHERE source_code = ?`,
		`DELETE FROM metrics WHERE defined_by = ?`,
		`DELETE FROM sources WHERE code = ?`,
	} {
		if _, err := q.ExecContext(ctx, stmt, code); err != nil {
			return err
		}
	}
	return nil
}

// ---- helpers --------------------------------------------------------------

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
