package query

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/store"
)

func describeBoth(t *testing.T, snap *store.Snapshot) (*Description, *Description) {
	t.Helper()
	e := NewEngine(snap)
	summary, err := e.Describe("")
	if err != nil {
		t.Fatal(err)
	}
	full, err := e.Describe(DetailFull)
	if err != nil {
		t.Fatal(err)
	}
	return summary, full
}

func TestDescribeSummaryIsDefaultAndPreservesMetricSemantics(t *testing.T) {
	summary, full := describeBoth(t, ambiguitySnapshot())
	if summary.Detail != DetailSummary || full.Detail != DetailFull {
		t.Fatalf("detail: %q %q", summary.Detail, full.Detail)
	}

	// Same metric keys overall; metrics without values are listed by key in summary.
	var summaryKeys []string
	for _, m := range summary.Metrics {
		summaryKeys = append(summaryKeys, m.Key)
	}
	summaryKeys = append(summaryKeys, summary.MetricsWithoutValues...)
	var fullKeys []string
	for _, m := range full.Metrics {
		fullKeys = append(fullKeys, m.Key)
	}
	slices.Sort(summaryKeys)
	slices.Sort(fullKeys)
	if !slices.Equal(summaryKeys, fullKeys) {
		t.Fatalf("keys differ: %v vs %v", summaryKeys, fullKeys)
	}
	if !slices.Equal(summary.MetricsWithoutValues, []string{"tokens"}) {
		t.Fatalf("metrics without values: %v", summary.MetricsWithoutValues)
	}

	byKey := map[string]MetricDescription{}
	for _, m := range full.Metrics {
		byKey[m.Key] = m
	}
	for _, s := range summary.Metrics {
		f := byKey[s.Key]
		if s.DisplayName != f.DisplayName || s.Description != f.Description || s.ValueKind != f.ValueKind || s.Unit != f.Unit ||
			s.Direction != f.Direction || s.Scope != f.Scope || s.CatalogModelsWithValue != f.CatalogModelsWithValue ||
			!slices.Equal(s.Sources, f.Sources) || !slices.Equal(s.EvaluationContexts, f.EvaluationContexts) {
			t.Fatalf("metric %s differs:\nsummary %+v\nfull    %+v", s.Key, s, f)
		}
		if s.DefinedBy != "" || s.MissingPossible != nil || s.DataPoints != nil || s.Status != "" {
			t.Fatalf("summary carries full-only fields: %+v", s)
		}
		if f.DefinedBy == "" || f.MissingPossible == nil || f.DataPoints == nil || f.Status == "" {
			t.Fatalf("full lost fields: %+v", f)
		}
	}
	if score := byKey["score"]; score.CatalogModelsWithValue == 0 || !slices.Contains(score.EvaluationContexts, "harness2") {
		t.Fatalf("score coverage/contexts: %+v", score)
	}

	// Summary groups the codes the listed metrics use under their kind; full
	// lists every context the catalog knows, with names.
	if len(summary.EvaluationContexts) != 0 {
		t.Fatalf("summary must not also carry the code/kind list: %+v", summary.EvaluationContexts)
	}
	if len(full.EvaluationContextsByKind) != 0 {
		t.Fatalf("full must not also carry the grouping: %+v", full.EvaluationContextsByKind)
	}
	var summaryContexts []string
	kindOf := map[string]string{}
	for kind, codes := range summary.EvaluationContextsByKind {
		if kind == "" || len(codes) == 0 {
			t.Fatalf("summary context group %q: %v", kind, codes)
		}
		if !slices.IsSorted(codes) {
			t.Fatalf("codes under %s are not ordered: %v", kind, codes)
		}
		for _, c := range codes {
			if kindOf[c] != "" {
				t.Fatalf("context %s is listed under two kinds", c)
			}
			kindOf[c] = kind
			summaryContexts = append(summaryContexts, c)
		}
	}
	// Every code a metric names is discoverable, with its kind, from the group.
	for _, m := range summary.Metrics {
		for _, c := range m.EvaluationContexts {
			if kindOf[c] == "" {
				t.Fatalf("metric %s context %s missing from summary contexts %v", m.Key, c, summaryContexts)
			}
		}
	}
	// The kinds a group uses are the kinds the request vocabulary accepts, and
	// full agrees with the summary about every code's kind.
	fullKind := map[string]string{}
	for _, c := range full.EvaluationContexts {
		fullKind[c.Code] = string(c.Kind)
	}
	for code, kind := range kindOf {
		if !slices.Contains(summary.Query.ContextKinds, kind) {
			t.Fatalf("context %s has kind %q, which is not in the request vocabulary %v", code, kind, summary.Query.ContextKinds)
		}
		if fullKind[code] != kind {
			t.Fatalf("context %s is %q in summary and %q in full", code, kind, fullKind[code])
		}
	}
	if len(full.EvaluationContexts) < len(summaryContexts) || full.EvaluationContexts[0].Name == "" {
		t.Fatalf("full contexts: %+v", full.EvaluationContexts)
	}

	// Query vocabulary is complete in summary.
	q := summary.Query
	if !slices.Equal(q.Operators, Operators) || !slices.Equal(q.MissingPolicies, []string{MissingReject, MissingAllow}) ||
		!slices.Equal(q.AmbiguousPolicies, []string{AmbiguousReject, AmbiguousAllow}) || !slices.Equal(q.DetailValues, []string{DetailCompact, DetailSummary, DetailFull}) ||
		len(q.ContextKinds) != 4 || len(q.FilterFields) != 6 {
		t.Fatalf("summary query help: %+v", q)
	}
	if len(full.Query.Notes) == 0 || full.Query.ModelIdentifiers == "" {
		t.Fatalf("full query help: %+v", full.Query)
	}
}

func TestDescribeSummaryIsEnoughToBuildAValidQuery(t *testing.T) {
	e := NewEngine(ambiguitySnapshot())
	d, err := e.Describe("")
	if err != nil {
		t.Fatal(err)
	}
	var metric, context string
	for _, m := range d.Metrics {
		if m.Scope == "evidence" && len(m.EvaluationContexts) > 0 {
			metric, context = m.Key, m.EvaluationContexts[0]
			break
		}
	}
	resp, err := e.Query(Request{
		Criteria: []Criterion{{Metric: metric, Op: d.Query.Operators[0], Value: 0.0, Missing: d.Query.MissingPolicies[1], Ambiguous: d.Query.AmbiguousPolicies[1],
			Evidence: EvidencePolicy{PreferContexts: []string{context}, ContextKinds: []string{d.Query.ContextKinds[2]}}}},
		OrderBy: []OrderTerm{{Metric: metric}},
		Detail:  d.Query.DetailValues[0],
	})
	if err != nil || resp.Eligible == 0 {
		t.Fatalf("query built from describe summary: %+v %v", resp, err)
	}
}

// TestDescribeSummaryAnswersEveryQuestionItIsFor states the default summary's
// job as a list of questions rather than a layout, so the encoding can change
// again without the promise changing: an agent that has read agents_md must be
// able to pick a metric and build its first query from this response alone.
func TestDescribeSummaryAnswersEveryQuestionItIsFor(t *testing.T) {
	summary, full := describeBoth(t, ambiguitySnapshot())

	// 1. Every metric is discoverable, including one with no value anywhere.
	named := map[string]bool{}
	for _, m := range summary.Metrics {
		named[m.Key] = true
	}
	for _, k := range summary.MetricsWithoutValues {
		named[k] = true
	}
	for _, m := range full.Metrics {
		if !named[m.Key] {
			t.Errorf("metric %s is in full but not discoverable from the summary", m.Key)
		}
	}
	if !named["tokens"] {
		t.Error("a metric with no values in this dataset must still be named")
	}

	// 2. Each listed metric carries what choosing between metrics needs.
	for _, m := range summary.Metrics {
		switch {
		case m.Key == "" || m.DisplayName == "" || m.Description == "":
			t.Errorf("metric %s: key, display name and description are all required: %+v", m.Key, m)
		case m.ValueKind == "" || m.Direction == "" || m.Scope == "":
			t.Errorf("metric %s: value kind, direction and scope are all required: %+v", m.Key, m)
		case m.Unit != metricByKey(full, m.Key).Unit:
			t.Errorf("metric %s: unit differs between projections", m.Key)
		}
		// Enough source and context information to build an evidence policy.
		if len(m.Sources) == 0 || len(m.EvaluationContexts) == 0 {
			t.Errorf("metric %s: no source or context to build an evidence policy from: %+v", m.Key, m)
		}
	}

	// 3. A metric measured in several contexts keeps them all, distinctly, so
	//    a task that justifies restricting or preferring one can name it.
	score := metricByKey(summary, "score")
	if !slices.Equal(score.EvaluationContexts, []string{"devin", "harness", "harness2"}) {
		t.Fatalf("score must keep every context it was measured in: %v", score.EvaluationContexts)
	}
	// Those contexts span kinds, and each is placed under its own kind.
	for _, want := range [][2]string{{"devin", "devin"}, {"harness", "external_harness"}, {"harness2", "external_harness"}} {
		if !slices.Contains(summary.EvaluationContextsByKind[want[1]], want[0]) {
			t.Errorf("context %s is not listed under kind %s: %v", want[0], want[1], summary.EvaluationContextsByKind)
		}
	}

	// 4. Summary and full describe the same metrics with the same associations.
	for _, s := range summary.Metrics {
		f := metricByKey(full, s.Key)
		if f.Key == "" {
			t.Errorf("metric %s is in the summary but not in full", s.Key)
			continue
		}
		if !slices.Equal(s.Sources, f.Sources) || !slices.Equal(s.EvaluationContexts, f.EvaluationContexts) ||
			s.Scope != f.Scope || s.Direction != f.Direction || s.CatalogModelsWithValue != f.CatalogModelsWithValue {
			t.Errorf("metric %s: summary and full disagree\nsummary %+v\nfull    %+v", s.Key, s, f)
		}
	}

	// 5. Full still carries the provenance, lifecycle and data-point counts.
	for _, f := range full.Metrics {
		if f.DefinedBy == "" || f.MissingPossible == nil || f.Status == "" || f.DataPoints == nil {
			t.Errorf("full lost a provenance or lifecycle field: %+v", f)
		}
	}
	if len(full.Sources) == 0 || len(full.EvaluationContexts) == 0 {
		t.Error("full must still list sources and contexts in long form")
	}
}

// withHarnessContexts mirrors the live catalog's shape, where one benchmark is
// republished from dozens of third-party agent harnesses and each gets its own
// evaluation context. That long tail is what makes the default summary grow.
func withHarnessContexts(n int) *store.Snapshot {
	snap := snapshot()
	for i := range n {
		code := fmt.Sprintf("harness_%02d_coding_agent", i)
		snap.Contexts[code] = sources.EvaluationContext{Code: code, Name: "Harness " + code, Kind: sources.ContextExternalHarness}
		snap.Observations = append(snap.Observations, store.ObservationRow{
			ID: int64(1000 + i), Source: "test", Metric: "score", BaseKey: "beta",
			EvaluationContext: code, Number: num(float64(50 + i)), SourceModelName: "beta",
		})
	}
	return snap
}

// TestDescribeSummaryDoesNotPayTwicePerEvaluationContext is the byte guard that
// has teeth, because it measures the thing that grows without bound rather than
// a fixture total.
//
// A context in use costs its code inside the metric that uses it, and its code
// once more in the grouped list that gives it a kind. It must not also cost a
// {"code": ..., "kind": ...} object of its own: that restates the code to add a
// word already stated for the whole group.
func TestDescribeSummaryDoesNotPayTwicePerEvaluationContext(t *testing.T) {
	size := func(n int) int {
		d, err := NewEngine(withHarnessContexts(n)).Describe("")
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		return len(b)
	}
	const added = 40
	base, wide := size(0), size(added)
	perContext := float64(wide-base) / added

	// The codes here are 23 characters, so two quoted copies with separators
	// are about 52 bytes; a code/kind object as well would be about 60 more.
	const budget = 60
	if perContext > budget {
		t.Errorf("each evaluation context adds %.0f bytes to the default summary (budget %d): it should cost its code in the metric that uses it plus its code in the grouped list, and nothing else",
			perContext, budget)
	}

	// And the response still names every one of them.
	d, err := NewEngine(withHarnessContexts(added)).Describe("")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(d.EvaluationContextsByKind[string(sources.ContextExternalHarness)]); got != added+1 {
		t.Fatalf("grouped external harnesses = %d, want %d including the fixture's own", got, added+1)
	}
	// The fixture already measures score in "harness" and "devin".
	if got := len(metricByKey(d, "score").EvaluationContexts); got != added+2 {
		t.Fatalf("score names %d contexts, want %d", got, added+2)
	}
}

func metricByKey(d *Description, key string) MetricDescription {
	for _, m := range d.Metrics {
		if m.Key == key {
			return m
		}
	}
	return MetricDescription{}
}

func TestDescribeInvalidDetailAndNotReady(t *testing.T) {
	var inv *InvalidError
	if _, err := NewEngine(ambiguitySnapshot()).Describe("verbose"); !errors.As(err, &inv) {
		t.Fatalf("invalid detail: %v", err)
	}
	snap := ambiguitySnapshot()
	snap.Selection = nil
	d, err := NewEngine(snap).Describe("")
	if err != nil {
		t.Fatal(err)
	}
	if d.CatalogReady || len(d.MetricsWithoutValues) != 0 || len(d.Metrics) != len(snap.MetricOrder) {
		t.Fatalf("without a ready catalog, summary must list every metric: %+v", d)
	}
}
