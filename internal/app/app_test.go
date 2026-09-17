package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/app"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/devin"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/query"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/retrieval"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources/bfcl"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources/epoch"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/testfixtures"
)

type harness struct {
	t     *testing.T
	ctx   context.Context
	fetch *retrieval.Static
	svc   *app.Service
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	fetch := &retrieval.Static{Bodies: map[string][]byte{
		devin.ModelsURL:  []byte(testfixtures.DevinPage()),
		epoch.ArchiveURL: testfixtures.DefaultEpochArchive(),
		bfcl.DataURL:     []byte(testfixtures.BFCLCSV),
	}, Errors: map[string]error{}}
	clock := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	svc, err := app.Open(context.Background(), app.Options{
		DBPath: filepath.Join(t.TempDir(), "devmodels.db"),
		HTTP:   fetch,
		Now:    func() time.Time { clock = clock.Add(time.Second); return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	return &harness{t: t, ctx: context.Background(), fetch: fetch, svc: svc}
}

func (h *harness) page(md string) { h.fetch.Bodies[devin.ModelsURL] = []byte(md) }

func (h *harness) datasetsRefresh() *app.DatasetList {
	h.t.Helper()
	list, err := h.svc.RefreshDatasets(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	return list
}

func (h *harness) use(id int) {
	h.t.Helper()
	if _, err := h.svc.UseDataset(h.ctx, id); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) refresh(opts app.RefreshOptions) *app.RefreshReport {
	h.t.Helper()
	rep, err := h.svc.Refresh(h.ctx, opts)
	if err != nil {
		h.t.Fatalf("refresh: %v", err)
	}
	return rep
}

func (h *harness) catalogUIDs() []string {
	h.t.Helper()
	resp, err := h.svc.Query(h.ctx, query.Request{Limit: 500})
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, r := range resp.Results {
		out = append(out, r.Model.UID)
	}
	return out
}

func (h *harness) count(sqlText string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.svc.DB().SQL().QueryRowContext(h.ctx, sqlText, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func withoutEnterprise() string {
	return testfixtures.RenderDevinPage(testfixtures.DefaultRecords(), []testfixtures.Tab{testfixtures.SelfServeTab, testfixtures.LegacyTab})
}

func TestDatasetIDsAreRegeneratedFromOne(t *testing.T) {
	h := newHarness(t)
	for _, step := range []struct {
		page string
		want []string
	}{
		{testfixtures.DevinPage(), []string{"Self-serve", "Enterprise (ACUs)", "Legacy enterprise (credits)"}},
		{withoutEnterprise(), []string{"Self-serve", "Legacy enterprise (credits)"}},
		{testfixtures.DevinPage(), []string{"Self-serve", "Enterprise (ACUs)", "Legacy enterprise (credits)"}},
	} {
		h.page(step.page)
		list := h.datasetsRefresh()
		if len(list.Datasets) != len(step.want) {
			t.Fatalf("got %d datasets", len(list.Datasets))
		}
		for i, d := range list.Datasets {
			if d.ID != i+1 || d.DisplayName != step.want[i] {
				t.Fatalf("dataset %d = %d %q", i, d.ID, d.DisplayName)
			}
		}
	}
}

func TestUnsupportedDatasetCannotBeSelected(t *testing.T) {
	h := newHarness(t)
	acus := testfixtures.Tab{Title: "Legacy (ACUs)", Component: `<ModelsTable unit="acus" />`}
	h.page(testfixtures.RenderDevinPage(testfixtures.DefaultRecords(), append(testfixtures.DefaultTabs(), acus)))
	h.datasetsRefresh()
	_, err := h.svc.UseDataset(h.ctx, 4)
	if app.KindOf(err) != app.KindUsage || !strings.Contains(err.Error(), "acus") {
		t.Fatalf("unsupported dataset selection: %v", err)
	}
	if _, err := h.svc.UseDataset(h.ctx, 9); app.KindOf(err) != app.KindUsage {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestLegacyDatasetLifecycle(t *testing.T) {
	h := newHarness(t)
	list := h.datasetsRefresh()
	if d := list.Datasets[2]; !d.Supported || d.SourceKey != "ModelsTable" || d.ModelRowCount != 7 {
		t.Fatalf("legacy inventory row: %+v", d)
	}
	h.use(1)
	h.refresh(app.RefreshOptions{Sources: []string{devin.SourceCode}})

	h.use(3)
	var nr *query.NotReadyError
	if _, err := h.svc.Query(h.ctx, query.Request{}); !errors.As(err, &nr) {
		t.Fatalf("switching to legacy must block queries until refreshed: %v", err)
	}
	rep := h.refresh(app.RefreshOptions{})
	if rep.Sources[0].Counts.Published != 7 || rep.Sources[0].Counts.Accepted != 7 {
		t.Fatalf("legacy refresh counts: %+v", rep.Sources[0].Counts)
	}
	uids := h.catalogUIDs()
	if len(uids) != 7 || has(uids, "claude-sonnet-4-6") || !has(uids, "legacy:claude-opus-5-medium-thinking") {
		t.Fatalf("legacy catalog: %v", uids)
	}
	if n := h.count(`SELECT COUNT(*) FROM observations WHERE catalog_uid <> '' AND metric_key LIKE '%price%'`); n != 0 {
		t.Fatalf("legacy models must carry no token prices, found %d", n)
	}

	resp, err := h.svc.Query(h.ctx, query.Request{
		Criteria: []query.Criterion{{Metric: devin.MetricLegacyCredits, Op: "lte", Value: 100.0, Missing: query.MissingReject}},
		OrderBy:  []query.OrderTerm{{Metric: devin.MetricLegacyCredits}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range resp.Results {
		got = append(got, r.Model.UID)
	}
	if strings.Join(got, ",") != "legacy:glm-5.2-1m-no-thinking,legacy:swe-2-high,legacy:claude-fable-5.1-low-thinking,legacy:claude-opus-5-medium-thinking" {
		t.Fatalf("credits query: %v", got)
	}

	// Evidence matches legacy spellings through the shared identity layer.
	d, err := h.svc.ModelDetails(h.ctx, "Claude Opus 5 (Medium Thinking)", query.DetailFull)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range d.Metrics {
		if m.Metric == "swe_bench_verified" && m.Selected != nil && m.Selected.EffortExact && m.Value == 78.1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("legacy model should receive effort-exact SWE-bench evidence: %+v", d.Metrics)
	}

	// Idempotent like any other dataset.
	before := h.count(`SELECT COUNT(*) FROM observations`)
	h.refresh(app.RefreshOptions{})
	if after := h.count(`SELECT COUNT(*) FROM observations`); after != before {
		t.Fatalf("repeat legacy refresh changed observations: %d -> %d", before, after)
	}
}

func TestReorderingCannotChangeTheSelectedDataset(t *testing.T) {
	h := newHarness(t)
	h.datasetsRefresh()
	h.use(2) // Enterprise
	h.refresh(app.RefreshOptions{})
	if got := h.catalogUIDs(); len(got) != 5 || !has(got, "kimi-k3-high") {
		t.Fatalf("enterprise catalog: %v", got)
	}

	renamed := testfixtures.EnterpriseTab
	renamed.Title = "Enterprise (ACU billing)"
	h.page(testfixtures.RenderDevinPage(testfixtures.DefaultRecords(), []testfixtures.Tab{testfixtures.SelfServeTab, testfixtures.LegacyTab, renamed}))

	rep := h.refresh(app.RefreshOptions{Sources: []string{devin.SourceCode}})
	got := h.catalogUIDs()
	if len(got) != 5 || has(got, "claude-sonnet-4-6") {
		t.Fatalf("after reorder the catalog must still be the enterprise dataset: %v", got)
	}
	if rep.Dataset.DisplayName != "Enterprise (ACU billing)" {
		t.Fatalf("display name should follow the source: %q", rep.Dataset.DisplayName)
	}
	warnings := strings.Join(rep.Sources[0].Warnings, "\n")
	if !strings.Contains(warnings, "now titled") || !strings.Contains(warnings, "datasets refresh") {
		t.Fatalf("rename and inventory drift should be reported: %q", warnings)
	}

	// Id 2 now means Legacy; the persisted selection is unaffected.
	list := h.datasetsRefresh()
	if list.SelectedDatasetID != 3 {
		t.Fatalf("selection should be found at its new id 3, got %d", list.SelectedDatasetID)
	}
}

func TestSelectedDatasetDisappearingFailsWithoutTouchingCatalog(t *testing.T) {
	h := newHarness(t)
	h.datasetsRefresh()
	h.use(2)
	h.refresh(app.RefreshOptions{})
	before := h.catalogUIDs()
	observations := h.count(`SELECT COUNT(*) FROM observations`)

	h.page(withoutEnterprise())
	rep, err := h.svc.Refresh(h.ctx, app.RefreshOptions{})
	if app.KindOf(err) != app.KindNotReady {
		t.Fatalf("want a selection error, got %v", err)
	}
	for _, want := range []string{"Enterprise (ACUs)", "Self-serve", "datasets refresh"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
	if len(rep.Sources) != 1 || rep.Sources[0].Status != "failed" {
		t.Fatalf("the refresh must stop at the dataset check: %+v", rep.Sources)
	}
	if after := h.catalogUIDs(); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("catalog changed: %v -> %v", before, after)
	}
	if h.count(`SELECT COUNT(*) FROM observations`) != observations {
		t.Fatal("observations changed")
	}

	// A similarly named dataset is not adopted: rename Self-serve to look like Enterprise.
	lookalike := testfixtures.SelfServeTab
	lookalike.Title = "Enterprise (ACUs)"
	h.page(testfixtures.RenderDevinPage(testfixtures.DefaultRecords(), []testfixtures.Tab{lookalike}))
	if _, err := h.svc.Refresh(h.ctx, app.RefreshOptions{Sources: []string{devin.SourceCode}}); app.KindOf(err) != app.KindNotReady {
		t.Fatalf("a same-titled but structurally different dataset must not be adopted: %v", err)
	}

	st, err := h.svc.Status(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(st.Problems, "\n"), "devin failed") {
		t.Fatalf("status should report the failed dataset check: %v", st.Problems)
	}
}

func TestFailedOrIncompleteDevinRefreshPreservesCatalog(t *testing.T) {
	h := newHarness(t)
	h.datasetsRefresh()
	h.use(1)
	h.refresh(app.RefreshOptions{})
	before := h.catalogUIDs()
	prices := h.count(`SELECT COUNT(*) FROM observations WHERE catalog_uid <> ''`)

	full := testfixtures.DevinPage()
	for _, tc := range []struct {
		name   string
		mutate func()
	}{
		{"network", func() { h.fetch.Errors[devin.ModelsURL] = errors.New("connection refused") }},
		{"truncated", func() { h.page(full[:len(full)*2/3]) }},
		{"reshaped", func() { h.page(strings.ReplaceAll(full, "modelCostData", "priceRows")) }},
	} {
		// Each case starts from the working page with no injected failure, so
		// it exercises only its own fault.
		name := tc.name
		h.fetch.Errors = map[string]error{}
		h.page(full)
		tc.mutate()
		rep, err := h.svc.Refresh(h.ctx, app.RefreshOptions{})
		if app.KindOf(err) != app.KindSource {
			t.Fatalf("%s: want a source failure, got %v", name, err)
		}
		if got := h.catalogUIDs(); strings.Join(got, ",") != strings.Join(before, ",") {
			t.Fatalf("%s: catalog changed", name)
		}
		if h.count(`SELECT COUNT(*) FROM observations WHERE catalog_uid <> ''`) != prices {
			t.Fatalf("%s: catalog observations changed", name)
		}
		// Independent sources still refresh.
		for _, s := range rep.Sources[1:] {
			if s.Status != "success" {
				t.Fatalf("%s: %s should still refresh: %+v", name, s.Source, s)
			}
		}
	}
}

func TestSwitchingDatasetsNeverReturnsPreviousModels(t *testing.T) {
	h := newHarness(t)
	h.datasetsRefresh()
	h.use(1)
	h.refresh(app.RefreshOptions{})
	if !has(h.catalogUIDs(), "claude-sonnet-4-6") {
		t.Fatal("self-serve model missing")
	}

	h.use(2)
	var nr *query.NotReadyError
	if _, err := h.svc.Query(h.ctx, query.Request{}); !errors.As(err, &nr) {
		t.Fatalf("queries must fail between selection and refresh, got %v", err)
	}
	if _, err := h.svc.ModelDetails(h.ctx, "claude-sonnet-4-6", ""); !errors.As(err, &nr) {
		t.Fatalf("details must fail between selection and refresh, got %v", err)
	}
	if n := h.count(`SELECT COUNT(*) FROM catalog_models`) + h.count(`SELECT COUNT(*) FROM observations WHERE catalog_uid <> ''`); n != 0 {
		t.Fatalf("previous dataset rows remain: %d", n)
	}

	h.refresh(app.RefreshOptions{Sources: []string{devin.SourceCode}})
	got := h.catalogUIDs()
	if has(got, "claude-sonnet-4-6") || len(got) != 5 {
		t.Fatalf("enterprise catalog contains self-serve models: %v", got)
	}

	// Re-selecting the current dataset keeps its catalog.
	sel, err := h.svc.UseDataset(h.ctx, 2)
	if err != nil || sel.Changed {
		t.Fatalf("re-select: %+v %v", sel, err)
	}
	if len(h.catalogUIDs()) != 5 {
		t.Fatal("re-selecting the same dataset must not clear it")
	}
}

func TestRepeatedRefreshIsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.datasetsRefresh()
	h.use(1)

	snapshot := func() map[string]int {
		return map[string]int{
			"models":       h.count(`SELECT COUNT(*) FROM catalog_models`),
			"dispositions": h.count(`SELECT COUNT(*) FROM catalog_row_dispositions`),
			"observations": h.count(`SELECT COUNT(*) FROM observations`),
			"rejected":     h.count(`SELECT COUNT(*) FROM rejected_rows`),
			"contexts":     h.count(`SELECT COUNT(*) FROM evaluation_contexts`),
			"epoch":        h.count(`SELECT COUNT(*) FROM observations WHERE source_code = 'epoch'`),
		}
	}
	rep := h.refresh(app.RefreshOptions{})
	first := snapshot()
	details, err := h.svc.ModelDetails(h.ctx, "claude-opus-5-medium", query.DetailFull)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		h.refresh(app.RefreshOptions{})
	}
	second := snapshot()
	for k, v := range first {
		if second[k] != v {
			t.Errorf("%s: %d after first refresh, %d after third", k, v, second[k])
		}
	}
	again, err := h.svc.ModelDetails(h.ctx, "claude-opus-5-medium", query.DetailFull)
	if err != nil {
		t.Fatal(err)
	}
	for i := range details.Metrics {
		if details.Metrics[i].AlternativeCount != again.Metrics[i].AlternativeCount {
			t.Errorf("%s alternatives grew: %d -> %d", details.Metrics[i].Metric, details.Metrics[i].AlternativeCount, again.Metrics[i].AlternativeCount)
		}
	}
	// Every published row ends in exactly one state, and every accepted row of
	// an evidence source produced exactly one observation. Asserting the
	// identity rather than a fixed number keeps the test about the invariant
	// instead of about the fixture's current row count.
	var epochReport app.SourceReport
	for _, s := range rep.Sources {
		if s.Counts.Published != s.Counts.Accepted+s.Counts.Excluded+s.Counts.Rejected {
			t.Errorf("%s: %d published but %d accepted + %d excluded + %d rejected", s.Source,
				s.Counts.Published, s.Counts.Accepted, s.Counts.Excluded, s.Counts.Rejected)
		}
		if s.Source == epoch.SourceCode {
			epochReport = s
		}
	}
	if first["epoch"] == 0 || first["epoch"] != epochReport.Counts.Accepted {
		t.Errorf("epoch stored %d observations for %d accepted rows", first["epoch"], epochReport.Counts.Accepted)
	}
}

// TestDataPointsCountValuesNotRowsOrModels pins what the public `data_points`
// count means, and why it carries that name: it is individual metric values, so
// it is neither the row count nor the model count. One Devin catalog row publishes several prices, a
// recommendation state and any long-context rates, so the catalog's data points
// are a multiple of its models. Today an accepted benchmark row happens to
// produce exactly one, which is asserted per evidence source rather than as a
// general rule, because nothing guarantees it for a future source.
func TestDataPointsCountValuesNotRowsOrModels(t *testing.T) {
	h := newHarness(t)
	h.datasetsRefresh()
	h.use(1)
	rep := h.refresh(app.RefreshOptions{})

	if rep.CatalogModels == nil {
		t.Fatal("a successful catalog refresh must report its model count")
	}
	for _, s := range rep.Sources {
		stored := h.count(`SELECT COUNT(*) FROM observations WHERE source_code = ?`, s.Source)
		if s.Counts.DataPoints != stored {
			t.Errorf("%s: reported %d data points but stored %d values", s.Source, s.Counts.DataPoints, stored)
		}
		switch s.Source {
		case devin.SourceCode:
			if s.Counts.Accepted != *rep.CatalogModels {
				t.Errorf("the catalog's accepted rows (%d) are its models (%d)", s.Counts.Accepted, *rep.CatalogModels)
			}
			if s.Counts.DataPoints <= s.Counts.Accepted {
				t.Errorf("a catalog row publishes several values, so %d data points should exceed %d rows",
					s.Counts.DataPoints, s.Counts.Accepted)
			}
		default:
			if s.Counts.DataPoints != s.Counts.Accepted {
				t.Errorf("%s: %d data points for %d accepted rows", s.Source, s.Counts.DataPoints, s.Counts.Accepted)
			}
		}
	}

	// The status surface counts the same values, and unmatched is counted in
	// data points too rather than in models or rows.
	st, err := h.svc.Status(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, s := range st.Sources {
		total += s.DataPoints
	}
	if all := h.count(`SELECT COUNT(*) FROM observations`); total != all {
		t.Errorf("status reports %d data points over %d stored values", total, all)
	}
	perSource := 0
	for _, s := range rep.Sources {
		if s.Unmatched != nil {
			perSource += *s.Unmatched
		}
	}
	if st.Evidence.Unmatched != perSource {
		t.Errorf("status unmatched %d against %d summed over the refresh report", st.Evidence.Unmatched, perSource)
	}
}

func TestFailedCollectorKeepsPreviousEvidence(t *testing.T) {
	h := newHarness(t)
	h.datasetsRefresh()
	h.use(1)
	h.refresh(app.RefreshOptions{})
	before := h.count(`SELECT COUNT(*) FROM observations WHERE source_code = 'epoch'`)

	broken := testfixtures.DefaultEpochFiles()
	delete(broken, "terminalbench_external.csv")
	h.fetch.Bodies[epoch.ArchiveURL] = testfixtures.EpochArchive(broken)
	rep, err := h.svc.Refresh(h.ctx, app.RefreshOptions{})
	if app.KindOf(err) != app.KindSource {
		t.Fatalf("want source failure, got %v", err)
	}
	statuses := map[string]string{}
	for _, s := range rep.Sources {
		statuses[s.Source] = s.Status
	}
	if statuses["devin"] != "success" || statuses["epoch"] != "failed" || statuses["bfcl"] != "success" {
		t.Fatalf("statuses: %v", statuses)
	}
	if after := h.count(`SELECT COUNT(*) FROM observations WHERE source_code = 'epoch'`); after != before {
		t.Fatalf("epoch evidence changed on failure: %d -> %d", before, after)
	}
	st, _ := h.svc.Status(h.ctx)
	if !st.Usable || !strings.Contains(strings.Join(st.Problems, "\n"), "epoch failed") {
		t.Fatalf("status: usable %v problems %v", st.Usable, st.Problems)
	}
}

func TestShrinkGuard(t *testing.T) {
	h := newHarness(t)
	h.datasetsRefresh()
	h.use(1)
	h.refresh(app.RefreshOptions{Sources: []string{devin.SourceCode}})

	few := testfixtures.Filter(testfixtures.DefaultRecords(), func(r testfixtures.Record) bool {
		return r.Tier == testfixtures.Enterprise || r.ModelUID == "adaptive" || r.ModelUID == "swe-check"
	})
	h.page(testfixtures.RenderDevinPage(few, testfixtures.DefaultTabs()))
	if _, err := h.svc.Refresh(h.ctx, app.RefreshOptions{Sources: []string{devin.SourceCode}}); app.KindOf(err) != app.KindSource {
		t.Fatalf("a sharp shrink must fail by default: %v", err)
	}
	if len(h.catalogUIDs()) != 9 {
		t.Fatal("catalog changed despite the guard")
	}
	h.refresh(app.RefreshOptions{Sources: []string{devin.SourceCode}, AllowShrink: true})
	if got := h.catalogUIDs(); len(got) != 2 {
		t.Fatalf("with --allow-shrink: %v", got)
	}
}

func TestEvidenceQueryEndToEnd(t *testing.T) {
	h := newHarness(t)
	h.datasetsRefresh()
	h.use(2)
	h.refresh(app.RefreshOptions{})

	compact, err := h.svc.Query(h.ctx, query.Request{
		Criteria: []query.Criterion{{Metric: "swe_bench_verified", Op: "gte", Value: 77.0, Missing: query.MissingReject}},
		OrderBy:  []query.OrderTerm{{Metric: "devin_output_price_usd_per_mtok"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cc := compact.Results[0].Criteria[0]; compact.Detail != query.DetailCompact || cc.State != query.StateResolved || cc.Value != 78.1 ||
		cc.Context != "epoch_ai_eval" || strings.Join(cc.Flags, ",") != query.FlagProxy || cc.Evidence != nil || cc.AlternativeCount != 1 {
		t.Fatalf("compact evidence: %+v", cc)
	}
	resp, err := h.svc.Query(h.ctx, query.Request{
		Criteria: []query.Criterion{{Metric: "swe_bench_verified", Op: "gte", Value: 77.0, Missing: query.MissingReject}},
		OrderBy:  []query.OrderTerm{{Metric: "devin_output_price_usd_per_mtok"}},
		Detail:   query.DetailSummary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Eligible != 1 || resp.Results[0].Model.UID != "claude-opus-5-medium" {
		t.Fatalf("results: %+v", resp.Results)
	}
	c := resp.Results[0].Criteria[0]
	if c.State != query.StateResolved || c.Value != 78.1 || c.Evidence == nil || !c.Evidence.EffortExact || !c.Evidence.Proxy ||
		c.AlternativeCount != 1 || c.Evidence.Source != "epoch" || c.Evidence.Context != "epoch_ai_eval" || c.Selected != nil {
		t.Fatalf("summary evidence: %+v %+v", c, c.Evidence)
	}
	fullResp, err := h.svc.Query(h.ctx, query.Request{
		Criteria: []query.Criterion{{Metric: "swe_bench_verified", Op: "gte", Value: 77.0, Missing: query.MissingReject}},
		Detail:   query.DetailFull,
	})
	if err != nil || fullResp.Results[0].Criteria[0].Selected == nil || fullResp.Results[0].Criteria[0].Selected.SourceRowRef == "" {
		t.Fatalf("full detail must keep the observation object: %+v %v", fullResp, err)
	}
	if len(resp.Exclusions) != 1 || resp.Exclusions[0].Reason != "missing" || resp.Exclusions[0].Models != 4 {
		t.Fatalf("exclusions: %+v", resp.Exclusions)
	}

	d, err := h.svc.ModelDetails(h.ctx, "Claude Opus 5 Medium", query.DetailFull)
	if err != nil {
		t.Fatal(err)
	}
	metrics := map[string]query.MetricEvidence{}
	for _, m := range d.Metrics {
		metrics[m.Metric] = m
	}
	// BFCL publishes the base model in two modes (FC 77.47, Prompt 70.0): peer
	// contexts that disagree, so no value is chosen.
	bfclRes := metrics["bfcl_overall_accuracy"]
	if !bfclRes.Ambiguous || bfclRes.Selected != nil || bfclRes.Value != nil ||
		strings.Join(bfclRes.PeerContexts, ",") != "bfcl_fc,bfcl_prompt" || bfclRes.Peers[0].EffortExact {
		t.Fatalf("BFCL FC and Prompt results for a base model should be an effort-inexact ambiguity: %+v", bfclRes)
	}
	if bfclRes.ValueRange == nil || bfclRes.ValueRange.Min != 70 || bfclRes.ValueRange.Max != 77.47 {
		t.Fatalf("BFCL value range: %+v", bfclRes.ValueRange)
	}
	preferred, err := h.svc.Query(h.ctx, query.Request{
		Criteria: []query.Criterion{{Metric: "bfcl_overall_accuracy", Op: "gte", Value: 75.0, Missing: query.MissingReject,
			Evidence: query.EvidencePolicy{PreferContexts: []string{"bfcl_fc"}}}},
	})
	if err != nil || preferred.Eligible != 1 || preferred.Results[0].Criteria[0].Value != 77.47 {
		t.Fatalf("an explicit context preference resolves BFCL: %+v %v", preferred, err)
	}
	// Claude Code (66) and Droid (70) are both Terminal-Bench evidence for this
	// model, and Droid is a harness this build does not curate. It is ingested
	// as its own context rather than discarded, so the two now disagree as
	// equally ranked peers: the answer is ambiguous, not the higher number and
	// not the curated one.
	tb := metrics["terminal_bench"]
	if !tb.Ambiguous || tb.Value != nil || tb.Selected != nil {
		t.Fatalf("an uncurated harness is evidence, so terminal_bench must be ambiguous: %+v", tb)
	}
	if strings.Join(tb.PeerContexts, ",") != "claude_code,harness_droid" {
		t.Fatalf("terminal_bench peers: %v", tb.PeerContexts)
	}
	if tb.ValueRange == nil || tb.ValueRange.Min != 66 || tb.ValueRange.Max != 70 {
		t.Fatalf("terminal_bench range: %+v", tb.ValueRange)
	}
	// Naming the curated context resolves it; the catalog never picks for you.
	restricted, err := h.svc.Query(h.ctx, query.Request{
		Filter: query.Filter{UIDs: []string{"claude-opus-5-medium"}},
		Criteria: []query.Criterion{{Metric: "terminal_bench", Op: "present",
			Evidence: query.EvidencePolicy{Contexts: []string{"claude_code"}}}},
	})
	if err != nil || restricted.Eligible != 1 || restricted.Results[0].Criteria[0].Value != 66.0 {
		t.Fatalf("restricting to the curated context resolves terminal_bench: %+v %v", restricted, err)
	}
	if metrics["devin_output_price_usd_per_mtok"].Value != 25.0 {
		t.Fatalf("price: %+v", metrics["devin_output_price_usd_per_mtok"])
	}
	if !has(d.MissingMetrics, "deepswe") {
		t.Fatalf("deepswe has only a high-effort row, so it is missing for the medium variant: %v", d.MissingMetrics)
	}
}

func TestManualImportAddsAMetricWithoutSchemaChanges(t *testing.T) {
	h := newHarness(t)
	h.datasetsRefresh()
	h.use(2)
	h.refresh(app.RefreshOptions{Sources: []string{devin.SourceCode}})
	tables := h.count(`SELECT COUNT(*) FROM sqlite_master`)

	doc := `{
	  "source": {"code": "manual_context", "name": "Context windows from vendor docs", "access_basis": "transcribed by the user from vendor documentation"},
	  "metrics": [{"key": "context_window_tokens", "display_name": "Context window", "description": "Maximum context length.", "value_kind": "integer", "unit": "tokens", "direction": "higher_is_better"}],
	  "evaluation_contexts": [{"code": "vendor_docs", "name": "Vendor documentation", "kind": "direct"}],
	  "observations": [
	    {"metric": "context_window_tokens", "model": "Claude Opus 5", "evaluation_context": "vendor_docs", "value": 200000},
	    {"metric": "context_window_tokens", "model": "GPT-5.6 Sol", "evaluation_context": "vendor_docs", "value": 272000},
	    {"metric": "context_window_tokens", "model": "Kimi K3", "evaluation_context": "vendor_docs", "value": 0}
	  ]
	}`
	for i := 0; i < 2; i++ {
		if _, err := h.svc.Import(h.ctx, []byte(doc)); err != nil {
			t.Fatal(err)
		}
	}
	if h.count(`SELECT COUNT(*) FROM sqlite_master`) != tables {
		t.Fatal("importing a metric must not change the schema")
	}
	if n := h.count(`SELECT COUNT(*) FROM observations WHERE source_code = 'manual_context'`); n != 3 {
		t.Fatalf("re-import must replace, not accumulate: %d", n)
	}

	resp, err := h.svc.Query(h.ctx, query.Request{
		Criteria: []query.Criterion{{Metric: "context_window_tokens", Op: "gte", Value: 200000.0, Missing: query.MissingReject}},
		OrderBy:  []query.OrderTerm{{Metric: "context_window_tokens"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range resp.Results {
		got = append(got, r.Model.UID)
	}
	if strings.Join(got, ",") != "gpt-5-6-sol-medium,claude-opus-5-medium" {
		t.Fatalf("got %v", got)
	}

	// A bad document changes nothing.
	bad := strings.Replace(doc, `"value": 272000`, `"value": "lots"`, 1)
	if _, err := h.svc.Import(h.ctx, []byte(bad)); app.KindOf(err) != app.KindUsage {
		t.Fatalf("bad import: %v", err)
	}
	if n := h.count(`SELECT COUNT(*) FROM observations WHERE source_code = 'manual_context'`); n != 3 {
		t.Fatalf("a failed import must be atomic: %d", n)
	}
	if _, err := h.svc.Import(h.ctx, []byte(strings.Replace(doc, "manual_context", "epoch", 1))); err == nil {
		t.Fatal("a manual import must not take a built-in source code")
	}

	if err := h.svc.RemoveImport(h.ctx, "manual_context"); err != nil {
		t.Fatal(err)
	}
	if h.count(`SELECT COUNT(*) FROM metrics WHERE key = 'context_window_tokens'`) != 0 {
		t.Fatal("removing the import removes the metric it defined")
	}
}

func TestRejectedRowsUnmatchedEvidenceAndAliases(t *testing.T) {
	h := newHarness(t)
	h.datasetsRefresh()
	h.use(2)
	h.refresh(app.RefreshOptions{})

	rejected, err := h.svc.Rejected(h.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	// Nothing is rejected for naming an unfamiliar harness or mode any more:
	// every rejection names something the row could not supply at all.
	for _, r := range rejected {
		if r.Reason == "" {
			t.Errorf("a rejection needs an exact reason: %+v", r)
		}
		for _, name := range []string{"SomeNewAgent", "Droid", "Weird Mode"} {
			if strings.Contains(r.Reason, name) {
				t.Errorf("an unfamiliar evaluation context is not a rejection: %+v", r)
			}
		}
	}
	// Those rows are now evidence, under contexts derived from their labels.
	for _, code := range []string{"harness_somenewagent", "harness_droid", "bfcl_mode_weird_mode", "bfcl_mode_unstated"} {
		if h.count(`SELECT COUNT(*) FROM evaluation_contexts WHERE code = '`+code+`'`) != 1 {
			t.Errorf("context %s was not recorded", code)
		}
		if h.count(`SELECT COUNT(*) FROM observations WHERE evaluation_context = '`+code+`'`) == 0 {
			t.Errorf("context %s carries no observation", code)
		}
	}

	unmatched, err := h.svc.Unmatched(h.ctx, "epoch")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, u := range unmatched {
		names = append(names, u.SourceModelName)
	}
	if !has(names, "some-other-model") {
		t.Fatalf("unmatched: %v", names)
	}

	if _, err := h.svc.AddAlias(h.ctx, "epoch", "some-other-model", "Kimi K3 High", "test mapping"); err != nil {
		t.Fatal(err)
	}
	d, err := h.svc.ModelDetails(h.ctx, "kimi-k3-high", query.DetailFull)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range d.Metrics {
		if m.Metric == "swe_bench_verified" && m.Selected.Match == "alias" && m.Value == 40.0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("alias not applied: %+v", d.Metrics)
	}
	// Aliases survive refreshes.
	h.refresh(app.RefreshOptions{})
	if aliases, _ := h.svc.Aliases(h.ctx); len(aliases) != 1 {
		t.Fatal("alias lost on refresh")
	}
	if _, err := h.svc.AddAlias(h.ctx, "devin", "x", "y", ""); app.KindOf(err) != app.KindUsage {
		t.Fatal("aliases do not apply to the catalog source")
	}
}

// TestUnmatchedEntriesCountTheDataPointsTheyStandFor pins the one thing the
// unmatched list must not do: call its own length a data-point count. Two
// stored values that differ only by reasoning effort share a source, model
// name, metric and context, so they are one row — and that row has to say it
// stands for two, because `devmodels status` counts them as two.
func TestUnmatchedEntriesCountTheDataPointsTheyStandFor(t *testing.T) {
	h := newHarness(t)
	h.datasetsRefresh()
	h.use(2)
	h.refresh(app.RefreshOptions{Sources: []string{devin.SourceCode}})

	// One model the dataset does not offer, measured at two efforts, plus a
	// second name so the list is not a single row.
	doc := `{
	  "source": {"code": "manual_unmatched", "name": "Hand-entered benchmark notes", "access_basis": "transcribed by the user"},
	  "metrics": [{"key": "notebench", "display_name": "NoteBench", "description": "Hand-entered score.", "value_kind": "number", "unit": "percent", "direction": "higher_is_better"}],
	  "evaluation_contexts": [{"code": "vendor_docs", "name": "Vendor documentation", "kind": "direct"}],
	  "observations": [
	    {"metric": "notebench", "model": "Nonexistent Model X", "effort": "High", "evaluation_context": "vendor_docs", "value": 71},
	    {"metric": "notebench", "model": "Nonexistent Model X", "effort": "Medium", "evaluation_context": "vendor_docs", "value": 64},
	    {"metric": "notebench", "model": "Nonexistent Model Y", "evaluation_context": "vendor_docs", "value": 55}
	  ]
	}`
	if _, err := h.svc.Import(h.ctx, []byte(doc)); err != nil {
		t.Fatal(err)
	}

	entries, err := h.svc.Unmatched(h.ctx, "manual_unmatched")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("the two efforts of one model share a row: %+v", entries)
	}
	byName := map[string]app.UnmatchedEntry{}
	points := 0
	for _, e := range entries {
		byName[e.SourceModelName] = e
		points += e.DataPoints
	}
	if got := byName["Nonexistent Model X"].DataPoints; got != 2 {
		t.Errorf("the collapsed row stands for 2 data points, not %d", got)
	}
	if got := byName["Nonexistent Model Y"].DataPoints; got != 1 {
		t.Errorf("an uncollapsed row stands for 1 data point, not %d", got)
	}

	// The whole point: the entry count is not the data-point count, and the
	// data-point count is the one `status` reports.
	if points == len(entries) {
		t.Fatalf("this fixture must collapse something, or it tests nothing")
	}
	st, err := h.svc.Status(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	all, err := h.svc.Unmatched(h.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, e := range all {
		total += e.DataPoints
	}
	if total != st.Evidence.Unmatched {
		t.Errorf("unmatched rows cover %d data points; status reports unmatched_data_points=%d", total, st.Evidence.Unmatched)
	}
}

func TestStatusOnAFreshDatabase(t *testing.T) {
	h := newHarness(t)
	st, err := h.svc.Status(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Usable || !strings.Contains(strings.Join(st.Problems, "\n"), "datasets refresh") {
		t.Fatalf("fresh status: %+v", st)
	}
	if _, err := h.svc.Refresh(h.ctx, app.RefreshOptions{}); app.KindOf(err) != app.KindNotReady {
		t.Fatalf("refresh without a selection: %v", err)
	}
	if _, err := h.svc.Refresh(h.ctx, app.RefreshOptions{Sources: []string{"nope"}}); app.KindOf(err) != app.KindUsage {
		t.Fatalf("unknown source: %v", err)
	}
	// Evidence sources do not need a selected dataset.
	if _, err := h.svc.Refresh(h.ctx, app.RefreshOptions{Sources: []string{"epoch", "bfcl"}}); err != nil {
		t.Fatalf("evidence-only refresh: %v", err)
	}
}
