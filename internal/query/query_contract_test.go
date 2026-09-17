package query

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/identity"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/store"
)

func TestNonFiniteCriterionValuesAreRejected(t *testing.T) {
	e := NewEngine(snapshot())
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		var inv *InvalidError
		_, err := e.Query(Request{Criteria: []Criterion{{Metric: "score", Op: "gte", Value: v, Missing: MissingReject}}})
		if !errors.As(err, &inv) || !strings.Contains(err.Error(), "finite") {
			t.Errorf("value %v: want a finite-number error, got %v", v, err)
		}
	}
}

// The alias match is not part of the evidence tier, so ambiguous peers can mix
// identity and alias matches; the alias flag must hold for all of them.
func TestAliasFlagOnAmbiguousValuesNeedsEveryPeer(t *testing.T) {
	snap := ambiguitySnapshot()
	snap.Models = append(snap.Models, store.CatalogModel{UID: "psi", Label: "Psi", Provider: "P", BaseName: "Psi", BaseKey: "psi"})
	snap.Observations = append(snap.Observations,
		// delta gains an aliased peer beside its two identity-matched peers.
		store.ObservationRow{ID: 40, Source: "test", Metric: "score", BaseKey: "omega", EvaluationContext: "harness2", Number: num(99), SourceModelName: "Omega"},
		// psi has only aliased peers, which disagree.
		store.ObservationRow{ID: 41, Source: "test", Metric: "score", BaseKey: "psi a", EvaluationContext: "harness", Number: num(30), SourceModelName: "Psi A"},
		store.ObservationRow{ID: 42, Source: "test", Metric: "score", BaseKey: "psi b", EvaluationContext: "harness2", Number: num(35), SourceModelName: "Psi B"},
	)
	snap.Aliases = []store.Alias{
		{SourceCode: "test", SourceNameNormalized: identity.Normalise("Omega"), BaseKey: "delta"},
		{SourceCode: "test", SourceNameNormalized: identity.Normalise("Psi A"), BaseKey: "psi"},
		{SourceCode: "test", SourceNameNormalized: identity.Normalise("Psi B"), BaseKey: "psi"},
	}
	e := NewEngine(snap)
	req := Request{OrderBy: []OrderTerm{{Metric: "score"}}, Filter: Filter{UIDs: []string{"delta", "psi"}}}

	for _, detail := range []string{DetailCompact, DetailSummary} {
		resp := project(t, e, req, detail)
		for _, r := range resp.Results {
			v := r.Order[0]
			if v.State != StateAmbiguous {
				t.Fatalf("%s %s: want ambiguous, got %+v", detail, r.Model.UID, v)
			}
			wantAlias := r.Model.UID == "psi"
			gotAlias := slices.Contains(v.Flags, FlagAlias) || (v.Evidence != nil && v.Evidence.Alias)
			if gotAlias != wantAlias {
				t.Errorf("%s %s: alias flag %v, want %v", detail, r.Model.UID, gotAlias, wantAlias)
			}
		}
	}
}

func TestDetailsProxyCaveatNeedsBenchmarkEvidence(t *testing.T) {
	const proxyCaveat = "every benchmark value is a proxy"
	e := NewEngine(snapshot())
	beta, err := e.Details("beta", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(beta.Caveats, "\n"), proxyCaveat) {
		t.Fatalf("a model with no benchmark evidence must not be called proxy-only: %v", beta.Caveats)
	}
	alpha, err := e.Details("alpha-high", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(alpha.Caveats, "\n"), proxyCaveat) {
		t.Fatalf("a model whose only benchmark value is a proxy needs the caveat: %v", alpha.Caveats)
	}
}

func TestFilterValuesWithAFixedVocabularyAreValidated(t *testing.T) {
	e := NewEngine(snapshot())
	var inv *InvalidError
	for _, f := range []Filter{{Efforts: []string{"x-high"}}, {Efforts: []string{"hihg"}}, {ServingVariants: []string{"fsat"}}} {
		if _, err := e.Query(Request{Filter: f}); !errors.As(err, &inv) || !strings.Contains(err.Error(), "use ") {
			t.Errorf("%+v: want an error listing valid values, got %v", f, err)
		}
	}
	// Valid values, in any case, still filter exactly as before.
	resp, err := e.Query(Request{Filter: Filter{Efforts: []string{"HIGH", "unspecified"}, ServingVariants: []string{"Standard"}}})
	if err != nil || !slices.Equal(uids(resp), []string{"alpha-high", "beta"}) {
		t.Fatalf("valid filter: %v %v", uids(resp), err)
	}
}

func TestDuplicateLabelNamesTheCandidates(t *testing.T) {
	snap := snapshot()
	snap.Models = append(snap.Models, store.CatalogModel{UID: "alpha-high-2", Label: "Alpha High", Provider: "A", BaseName: "Alpha", BaseKey: "alpha", Effort: "high"})
	e := NewEngine(snap)
	_, err := e.FindModel("alpha high")
	var inv *InvalidError
	if !errors.As(err, &inv) || !strings.Contains(err.Error(), "alpha-high, alpha-high-2") || strings.Contains(err.Error(), "not found") {
		t.Fatalf("an ambiguous label must list the matching uids: %v", err)
	}
	if m, err := e.FindModel("alpha-high-2"); err != nil || m.UID != "alpha-high-2" {
		t.Fatalf("a uid still resolves: %+v %v", m, err)
	}
}
