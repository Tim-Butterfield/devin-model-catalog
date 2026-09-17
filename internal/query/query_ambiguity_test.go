package query

import (
	"errors"
	"slices"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/store"
)

// ambiguitySnapshot extends the base snapshot with peer observations:
//
//	delta    score 55 (harness) and 71 (harness2): peer contexts disagree
//	epsilon  score 60 (harness) and 60 (harness2): peer contexts agree
//	theta    score 40 and 44, both harness:        one context disagrees with itself
func ambiguitySnapshot() *store.Snapshot {
	snap := snapshot()
	snap.Contexts["harness2"] = sources.EvaluationContext{Code: "harness2", Name: "Harness Two", Kind: sources.ContextExternalHarness}
	snap.Models = append(snap.Models,
		store.CatalogModel{UID: "delta", Label: "Delta", Provider: "D", BaseName: "Delta", BaseKey: "delta"},
		store.CatalogModel{UID: "epsilon", Label: "Epsilon", Provider: "E", BaseName: "Epsilon", BaseKey: "epsilon"},
		store.CatalogModel{UID: "theta", Label: "Theta", Provider: "T", BaseName: "Theta", BaseKey: "theta"},
	)
	snap.Observations = append(snap.Observations,
		store.ObservationRow{ID: 20, Source: "test", Metric: "score", BaseKey: "delta", EvaluationContext: "harness", Number: num(55), SourceModelName: "delta"},
		store.ObservationRow{ID: 21, Source: "test", Metric: "score", BaseKey: "delta", EvaluationContext: "harness2", Number: num(71), SourceModelName: "delta"},
		store.ObservationRow{ID: 22, Source: "test", Metric: "score", BaseKey: "epsilon", EvaluationContext: "harness", Number: num(60), SourceModelName: "epsilon"},
		store.ObservationRow{ID: 23, Source: "test", Metric: "score", BaseKey: "epsilon", EvaluationContext: "harness2", Number: num(60), SourceModelName: "epsilon"},
		store.ObservationRow{ID: 24, Source: "test", Metric: "score", BaseKey: "theta", EvaluationContext: "harness", Number: num(40), SourceModelName: "theta"},
		store.ObservationRow{ID: 25, Source: "test", Metric: "score", BaseKey: "theta", EvaluationContext: "harness", Number: num(44), SourceModelName: "theta"},
	)
	return snap
}

func modelByUID(t *testing.T, e *Engine, uid string) store.CatalogModel {
	t.Helper()
	for _, m := range e.snap.Models {
		if m.UID == uid {
			return m
		}
	}
	t.Fatalf("no model %s", uid)
	return store.CatalogModel{}
}

func TestPeerContextsAreNotResolvedToTheFavourableValue(t *testing.T) {
	e := NewEngine(ambiguitySnapshot())
	delta := modelByUID(t, e, "delta")

	res := e.Resolve(delta, "score", EvidencePolicy{}, false)
	if res.Value == 71.0 || (res.Selected != nil && res.Selected.Value == 71.0) {
		t.Fatal("71 must not be selected merely because higher is better")
	}
	if !res.Ambiguous || res.Missing || res.Value != nil || res.Selected != nil {
		t.Fatalf("peer contexts that disagree must be ambiguous with no value: %+v", res)
	}
	if res.AmbiguityReason != AmbiguityPeerContexts || !slices.Equal(res.PeerContexts, []string{"harness", "harness2"}) || len(res.Peers) != 2 {
		t.Fatalf("ambiguity detail: %+v", res)
	}
	if res.ValueRange == nil || res.ValueRange.Min != 55 || res.ValueRange.Max != 71 {
		t.Fatalf("value range: %+v", res.ValueRange)
	}

	// An explicit context restriction resolves it.
	if r := e.Resolve(delta, "score", EvidencePolicy{Contexts: []string{"harness"}}, false); r.Ambiguous || r.Value != 55.0 {
		t.Fatalf("restricted to harness: %+v", r)
	}
	// So does an explicit, ordered preference, in either direction.
	if r := e.Resolve(delta, "score", EvidencePolicy{PreferContexts: []string{"harness"}}, false); r.Ambiguous || r.Value != 55.0 || r.AlternativeCount != 1 {
		t.Fatalf("prefer harness: %+v", r)
	}
	if r := e.Resolve(delta, "score", EvidencePolicy{PreferContexts: []string{"harness2", "harness"}}, false); r.Ambiguous || r.Value != 71.0 {
		t.Fatalf("prefer harness2: %+v", r)
	}
}

func TestAgreeingAndSelfConflictingPeers(t *testing.T) {
	e := NewEngine(ambiguitySnapshot())
	agree := e.Resolve(modelByUID(t, e, "epsilon"), "score", EvidencePolicy{}, true)
	if agree.Ambiguous || agree.Value != 60.0 || agree.Selected == nil || agree.AlternativeCount != 1 || len(agree.Alternatives) != 1 {
		t.Fatalf("agreeing peers should resolve: %+v", agree)
	}
	conflict := e.Resolve(modelByUID(t, e, "theta"), "score", EvidencePolicy{}, false)
	if !conflict.Ambiguous || conflict.AmbiguityReason != AmbiguityConflictingValues {
		t.Fatalf("one context publishing two values is ambiguous too: %+v", conflict)
	}
}

func TestAmbiguitySummaryReportsReasons(t *testing.T) {
	e := NewEngine(ambiguitySnapshot())
	resp, err := e.Query(Request{OrderBy: []OrderTerm{{Metric: "score"}}, Filter: Filter{UIDs: []string{"delta", "theta"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Ambiguities) != 1 || resp.Ambiguities[0].Models != 2 ||
		!slices.Equal(resp.Ambiguities[0].Reasons, []string{AmbiguityConflictingValues, AmbiguityPeerContexts}) {
		t.Fatalf("summary: %+v", resp.Ambiguities)
	}
}

func TestStrongerEvidenceStillWinsOverPeers(t *testing.T) {
	// gamma has a Devin-measured exact 70, an external exact 80 and an
	// effort-inexact 90: evidence strength decides, not ambiguity or value.
	e := NewEngine(ambiguitySnapshot())
	res := e.Resolve(modelByUID(t, e, "gamma-max"), "score", EvidencePolicy{}, false)
	if res.Ambiguous || res.Value != 70.0 {
		t.Fatalf("gamma: %+v", res)
	}
}

func TestAmbiguousCriteria(t *testing.T) {
	e := NewEngine(ambiguitySnapshot())
	only := func(c Criterion) *Response {
		t.Helper()
		resp, err := e.Query(Request{Criteria: []Criterion{c}, Filter: Filter{UIDs: []string{"delta"}}})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	if r := only(Criterion{Metric: "score", Op: "gte", Value: 50.0, Missing: MissingReject}); r.Eligible != 1 || r.Results[0].Criteria[0].Outcome != "met_by_all_peers" {
		t.Fatalf("every peer satisfies gte 50: %+v", r)
	}
	if r := only(Criterion{Metric: "score", Op: "gte", Value: 80.0, Missing: MissingReject}); r.Eligible != 0 || r.Exclusions[0].Reason != "not_met" {
		t.Fatalf("no peer satisfies gte 80: %+v", r)
	}
	r := only(Criterion{Metric: "score", Op: "gte", Value: 60.0, Missing: MissingReject})
	if r.Eligible != 0 || len(r.Exclusions) != 1 || r.Exclusions[0].Reason != "ambiguous" {
		t.Fatalf("peers disagree about gte 60, default reject: %+v", r)
	}
	if len(r.Ambiguities) != 1 || r.Ambiguities[0].Metric != "score" || r.Ambiguities[0].Models != 1 || !slices.Equal(r.Ambiguities[0].Contexts, []string{"harness", "harness2"}) {
		t.Fatalf("ambiguity summary: %+v", r.Ambiguities)
	}
	if r := only(Criterion{Metric: "score", Op: "gte", Value: 60.0, Missing: MissingReject, Ambiguous: AmbiguousAllow}); r.Eligible != 1 || r.Results[0].Criteria[0].Outcome != "ambiguous_allowed" {
		t.Fatalf("ambiguous allow: %+v", r)
	}
	if r := only(Criterion{Metric: "score", Op: "gte", Value: 60.0, Missing: MissingReject, Evidence: EvidencePolicy{PreferContexts: []string{"harness2"}}}); r.Eligible != 1 || r.Results[0].Criteria[0].Outcome != "met" {
		t.Fatalf("an explicit preference resolves the criterion: %+v", r)
	}

	var inv *InvalidError
	if _, err := e.Query(Request{Criteria: []Criterion{{Metric: "score", Op: "gte", Value: 1.0, Missing: MissingReject, Ambiguous: "maybe"}}}); !errors.As(err, &inv) {
		t.Fatalf("invalid ambiguous policy: %v", err)
	}
	if _, err := e.Query(Request{OrderBy: []OrderTerm{{Metric: "score", Evidence: EvidencePolicy{PreferContexts: []string{"nope"}}}}}); !errors.As(err, &inv) {
		t.Fatalf("unknown preferred context: %v", err)
	}
}

func TestAmbiguousValuesSortAfterResolvedAndBeforeMissing(t *testing.T) {
	e := NewEngine(ambiguitySnapshot())
	resp, err := e.Query(Request{
		OrderBy: []OrderTerm{{Metric: "score"}},
		Filter:  Filter{UIDs: []string{"beta", "delta", "alpha-high", "gamma-max"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := uids(resp); !slices.Equal(got, []string{"gamma-max", "alpha-high", "delta", "beta"}) {
		t.Fatalf("order = %v", got)
	}
	if resp.Eligible != 4 || len(resp.Ambiguities) != 1 {
		t.Fatalf("ordering must exclude nothing and report the ambiguity: %+v", resp)
	}

	resp, _ = e.Query(Request{
		OrderBy: []OrderTerm{{Metric: "score", Missing: "first"}},
		Filter:  Filter{UIDs: []string{"beta", "delta", "alpha-high", "gamma-max"}},
	})
	if got := uids(resp); !slices.Equal(got, []string{"beta", "gamma-max", "alpha-high", "delta"}) {
		t.Fatalf("missing first = %v", got)
	}
}
