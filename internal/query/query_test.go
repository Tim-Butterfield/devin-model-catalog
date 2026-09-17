package query

import (
	"errors"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/metrics"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/store"
)

func num(v float64) *float64 { return &v }

func def(key string, kind metrics.ValueKind, dir metrics.Direction, scope metrics.Scope) metrics.Definition {
	return metrics.Definition{Key: key, DisplayName: key, Description: key, ValueKind: kind, Direction: dir, Scope: scope,
		DefinedBy: "test", MissingPossible: true, Status: metrics.StatusCurrent}
}

// snapshot builds a small catalog:
//
//	alpha-high       Alpha High        score 0 (exact), price 10
//	alpha-high-fast  Alpha High Fast   score only via standard sibling, price 20
//	beta             Beta              no score, price 5
//	gamma-max        Gamma Max         score 80 external + 70 devin, no price
func snapshot() *store.Snapshot {
	models := []store.CatalogModel{
		{UID: "alpha-high", Label: "Alpha High", Provider: "A", BaseName: "Alpha", BaseKey: "alpha", Effort: "high"},
		{UID: "alpha-high-fast", Label: "Alpha High Fast", Provider: "A", BaseName: "Alpha", BaseKey: "alpha", Effort: "high", ServingVariant: "fast"},
		{UID: "beta", Label: "Beta", Provider: "B", BaseName: "Beta", BaseKey: "beta"},
		{UID: "gamma-max", Label: "Gamma Max", Provider: "G", BaseName: "Gamma", BaseKey: "gamma", Effort: "max"},
	}
	defs := []metrics.Definition{
		def("score", metrics.KindNumber, metrics.HigherIsBetter, metrics.ScopeEvidence),
		def("price", metrics.KindNumber, metrics.LowerIsBetter, metrics.ScopeCatalog),
		def("flag", metrics.KindBoolean, metrics.Neutral, metrics.ScopeCatalog),
		def("tokens", metrics.KindInteger, metrics.Neutral, metrics.ScopeEvidence),
	}
	snap := &store.Snapshot{
		Selection: &store.Selection{SourceKey: "k", DisplayName: "Test", CatalogState: store.CatalogCurrent},
		Models:    models,
		Metrics:   map[string]metrics.Definition{},
		Sources:   map[string]sources.Info{"test": {Code: "test"}},
		Contexts: map[string]sources.EvaluationContext{
			"harness":   {Code: "harness", Name: "Harness", Kind: sources.ContextExternalHarness},
			"devin":     {Code: "devin", Name: "Devin", Kind: sources.ContextDevin},
			"published": {Code: "published", Name: "Published", Kind: sources.ContextPublished},
		},
		Observations: []store.ObservationRow{
			{ID: 1, Source: "test", Metric: "score", BaseKey: "alpha", Effort: "high", EvaluationContext: "harness", Number: num(0), SourceModelName: "alpha_high"},
			{ID: 2, Source: "test", Metric: "score", BaseKey: "gamma", Effort: "max", EvaluationContext: "harness", Number: num(80), SourceModelName: "gamma_max"},
			{ID: 3, Source: "test", Metric: "score", BaseKey: "gamma", Effort: "max", EvaluationContext: "devin", Number: num(70), SourceModelName: "gamma_max"},
			{ID: 4, Source: "test", Metric: "score", BaseKey: "gamma", Effort: "", EvaluationContext: "harness", Number: num(90), SourceModelName: "gamma"},
			{ID: 5, Source: "test", Metric: "score", BaseKey: "zeta", EvaluationContext: "harness", Number: num(50), SourceModelName: "Zeta 1"},
			{ID: 10, Source: "test", Metric: "price", CatalogUID: "alpha-high", EvaluationContext: "published", Number: num(10)},
			{ID: 11, Source: "test", Metric: "price", CatalogUID: "alpha-high-fast", EvaluationContext: "published", Number: num(20)},
			{ID: 12, Source: "test", Metric: "price", CatalogUID: "beta", EvaluationContext: "published", Number: num(5)},
			{ID: 13, Source: "test", Metric: "flag", CatalogUID: "beta", EvaluationContext: "published", Number: num(1)},
		},
	}
	for _, d := range defs {
		snap.MetricOrder = append(snap.MetricOrder, d.Key)
		snap.Metrics[d.Key] = d
	}
	return snap
}

func uids(r *Response) []string {
	var out []string
	for _, x := range r.Results {
		out = append(out, x.Model.UID)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMissingIsNotZero(t *testing.T) {
	e := NewEngine(snapshot())

	resp, err := e.Query(Request{Criteria: []Criterion{{Metric: "score", Op: "gte", Value: 0.0, Missing: MissingReject}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range uids(resp) {
		if u == "beta" {
			t.Fatal("a model with no score must not satisfy score >= 0")
		}
	}
	if !contains(uids(resp), "alpha-high") {
		t.Fatal("a real zero satisfies score >= 0")
	}

	resp, err = e.Query(Request{Criteria: []Criterion{{Metric: "score", Op: "lt", Value: 1.0, Missing: MissingAllow}}})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(uids(resp), "beta") || !contains(uids(resp), "alpha-high") {
		t.Fatalf("allow keeps missing and zero both, got %v", uids(resp))
	}
	for _, r := range resp.Results {
		if r.Model.UID == "beta" && (r.Criteria[0].Outcome != "missing_allowed" || r.Criteria[0].State != StateMissing || r.Criteria[0].Value != nil) {
			t.Fatalf("missing must be reported as missing, not a value: %+v", r.Criteria[0])
		}
		if r.Model.UID == "alpha-high" && r.Criteria[0].Value != 0.0 {
			t.Fatalf("zero must be reported as zero: %+v", r.Criteria[0])
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestMissingPolicyIsRequired(t *testing.T) {
	_, err := NewEngine(snapshot()).Query(Request{Criteria: []Criterion{{Metric: "score", Op: "gte", Value: 1.0}}})
	var inv *InvalidError
	if !errors.As(err, &inv) {
		t.Fatalf("want InvalidError, got %v", err)
	}
}

func TestOrderingDoesNotExclude(t *testing.T) {
	resp, err := NewEngine(snapshot()).Query(Request{OrderBy: []OrderTerm{{Metric: "score"}}})
	if err != nil {
		t.Fatal(err)
	}
	// gamma-max 70 (devin-measured preferred), alpha-high 0, then missing values last by label.
	want := []string{"gamma-max", "alpha-high", "alpha-high-fast", "beta"}
	if !equal(uids(resp), want) {
		t.Fatalf("order = %v, want %v", uids(resp), want)
	}
	if resp.Eligible != 4 {
		t.Fatalf("ordering excluded models: eligible %d", resp.Eligible)
	}

	resp, _ = NewEngine(snapshot()).Query(Request{OrderBy: []OrderTerm{{Metric: "price", Missing: "first"}}})
	if uids(resp)[0] != "gamma-max" {
		t.Fatalf("missing first: %v", uids(resp))
	}
}

func TestEligibilityThenOrdering(t *testing.T) {
	resp, err := NewEngine(snapshot()).Query(Request{
		Criteria: []Criterion{{Metric: "price", Op: "lte", Value: 15.0, Missing: MissingReject}},
		OrderBy:  []OrderTerm{{Metric: "price", Direction: "desc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !equal(uids(resp), []string{"alpha-high", "beta"}) {
		t.Fatalf("got %v", uids(resp))
	}
	if resp.ExcludedModels != 2 || len(resp.Exclusions) != 2 {
		t.Fatalf("exclusions: %+v", resp.Exclusions)
	}
}

func TestSelectionPrefersExactThenDevinAndCountsAlternatives(t *testing.T) {
	e := NewEngine(snapshot())
	gamma := e.snap.Models[3]
	res := e.Resolve(gamma, "score", EvidencePolicy{}, true)
	if res.Value != 70.0 || res.Selected.EvaluationContext != "devin" || res.Selected.Proxy {
		t.Fatalf("devin-measured exact value must win: %+v", res.Selected)
	}
	if res.AlternativeCount != 2 || len(res.Alternatives) != 2 {
		t.Fatalf("alternatives: %d %d", res.AlternativeCount, len(res.Alternatives))
	}
	// The effort-inexact 90 must rank after both exact values.
	if res.Alternatives[1].Value != 90.0 || res.Alternatives[1].EffortExact {
		t.Fatalf("inexact alternative ordering: %+v", res.Alternatives)
	}

	res = e.Resolve(gamma, "score", EvidencePolicy{Contexts: []string{"harness"}, ExactEffort: true}, false)
	if res.Value != 80.0 || res.AlternativeCount != 0 {
		t.Fatalf("policy-constrained resolution: %+v", res)
	}
}

func TestServingVariantBorrowingIsFlagged(t *testing.T) {
	e := NewEngine(snapshot())
	fast := e.snap.Models[1]
	res := e.Resolve(fast, "score", EvidencePolicy{}, false)
	if res.Missing || res.Selected.ServingExact || len(res.Selected.Caveats) == 0 {
		t.Fatalf("fast variant should borrow the standard score with a caveat: %+v", res)
	}
	if !e.Resolve(fast, "score", EvidencePolicy{ExactServing: true}, false).Missing {
		t.Fatal("exact_serving must reject borrowed evidence")
	}
}

func TestNotReady(t *testing.T) {
	snap := snapshot()
	snap.Selection = nil
	var nr *NotReadyError
	if _, err := NewEngine(snap).Query(Request{}); !errors.As(err, &nr) {
		t.Fatalf("no selection: %v", err)
	}
	snap = snapshot()
	snap.Selection.CatalogState = store.CatalogNeedsRefresh
	if _, err := NewEngine(snap).Details("beta", "", nil); !errors.As(err, &nr) {
		t.Fatalf("needs refresh: %v", err)
	}
}

func TestValidation(t *testing.T) {
	e := NewEngine(snapshot())
	bad := []Request{
		{Criteria: []Criterion{{Metric: "nope", Op: "gte", Value: 1.0, Missing: "reject"}}},
		{Criteria: []Criterion{{Metric: "score", Op: "between", Value: 1.0, Missing: "reject"}}},
		{Criteria: []Criterion{{Metric: "score", Op: "gte", Value: "high", Missing: "reject"}}},
		{Criteria: []Criterion{{Metric: "flag", Op: "gte", Value: true, Missing: "reject"}}},
		{Criteria: []Criterion{{Metric: "score", Op: "present", Missing: "allow"}}},
		{Criteria: []Criterion{{Metric: "score", Op: "gte", Value: 1.0, Missing: "reject", Evidence: EvidencePolicy{Contexts: []string{"unknown"}}}}},
		{OrderBy: []OrderTerm{{Metric: "tokens"}}},
		{Limit: 1000},
	}
	for i, req := range bad {
		var inv *InvalidError
		if _, err := e.Query(req); !errors.As(err, &inv) {
			t.Errorf("request %d: want InvalidError, got %v", i, err)
		}
	}
}

func TestBooleanAndPresent(t *testing.T) {
	e := NewEngine(snapshot())
	resp, err := e.Query(Request{Criteria: []Criterion{{Metric: "flag", Op: "eq", Value: true, Missing: MissingReject}}})
	if err != nil || !equal(uids(resp), []string{"beta"}) {
		t.Fatalf("%v %v", uids(resp), err)
	}
	resp, err = e.Query(Request{Criteria: []Criterion{{Metric: "price", Op: "present"}}})
	if err != nil || resp.Eligible != 3 {
		t.Fatalf("present: %v %v", uids(resp), err)
	}
}

func TestAliasMapsUnmatchedEvidence(t *testing.T) {
	snap := snapshot()
	if n := len(NewEngine(snap).UnmatchedEvidence()); n != 1 {
		t.Fatalf("unmatched before alias: %d", n)
	}
	snap.Aliases = []store.Alias{{SourceCode: "test", SourceNameNormalized: "zeta 1", BaseKey: "beta"}}
	e := NewEngine(snap)
	res := e.Resolve(snap.Models[2], "score", EvidencePolicy{}, false)
	if res.Missing || res.Selected.Match != "alias" || res.Value != 50.0 {
		t.Fatalf("alias resolution: %+v", res)
	}
	if n := len(e.UnmatchedEvidence()); n != 0 {
		t.Fatalf("unmatched after alias: %d", n)
	}
}

func TestDescribeReportsCoverage(t *testing.T) {
	d, err := NewEngine(snapshot()).Describe(DetailFull)
	if err != nil || !d.CatalogReady || len(d.Metrics) != 4 {
		t.Fatalf("%+v %v", d, err)
	}
	for _, m := range d.Metrics {
		if m.Key == "score" && m.CatalogModelsWithValue != 3 {
			t.Errorf("score coverage = %d", m.CatalogModelsWithValue)
		}
		if m.Key == "tokens" && (m.DataPoints == nil || *m.DataPoints != 0) {
			t.Errorf("tokens data points = %v", m.DataPoints)
		}
	}
}
