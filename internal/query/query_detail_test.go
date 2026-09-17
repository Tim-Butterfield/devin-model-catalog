package query

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/store"
)

func TestSummaryProjection(t *testing.T) {
	e := NewEngine(ambiguitySnapshot())
	resp, err := e.Query(Request{
		Criteria: []Criterion{{Metric: "price", Op: "lte", Value: 25.0, Missing: MissingAllow}},
		OrderBy:  []OrderTerm{{Metric: "score"}},
		Filter:   Filter{UIDs: []string{"gamma-max", "alpha-high-fast", "delta", "theta", "beta"}},
		Detail:   DetailSummary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Detail != DetailSummary || resp.Criteria != nil || resp.OrderBy != nil {
		t.Fatalf("detail = %q; summary lists no terms: %+v %+v", resp.Detail, resp.Criteria, resp.OrderBy)
	}
	byUID := map[string]Result{}
	for _, r := range resp.Results {
		byUID[r.Model.UID] = r
		for _, v := range append(slices.Clone(r.Order), OrderValue{Resolution: r.Criteria[0].Resolution}) {
			if v.Selected != nil || v.Peers != nil || v.Alternatives != nil || v.SelectionBasis != "" {
				t.Fatalf("summary must not carry full evidence: %+v", v)
			}
		}
		if r.Criteria[0].Op != "" || r.Criteria[0].Target != nil || r.Order[0].Direction != "" {
			t.Fatalf("summary must not echo the caller's own terms: %+v", r)
		}
	}

	gamma := byUID["gamma-max"].Order[0]
	if gamma.State != StateResolved || gamma.Value != 70.0 || gamma.Evidence == nil ||
		gamma.Evidence.Source != "test" || gamma.Evidence.Context != "devin" || gamma.Evidence.Proxy || !gamma.Evidence.EffortExact || gamma.AlternativeCount != 2 {
		t.Fatalf("resolved summary: %+v %+v", gamma, gamma.Evidence)
	}
	fast := byUID["alpha-high-fast"].Order[0]
	if fast.Evidence == nil || fast.Evidence.ServingExact || !fast.Evidence.Proxy {
		t.Fatalf("exactness and proxy flags must survive: %+v", fast.Evidence)
	}
	if beta := byUID["beta"].Order[0]; beta.State != StateMissing || beta.Missing || beta.Value != nil || beta.Evidence != nil {
		t.Fatalf("missing summary: %+v", beta)
	}
	delta := byUID["delta"].Order[0]
	if delta.State != StateAmbiguous || delta.Value != nil || delta.AmbiguityReason != AmbiguityPeerContexts ||
		!slices.Equal(delta.PeerContexts, []string{"harness", "harness2"}) || len(delta.PeerValues) != 2 ||
		delta.ValueRange == nil || delta.Evidence == nil || delta.Evidence.Source != "" || !delta.Evidence.Proxy {
		t.Fatalf("ambiguous summary: %+v", delta)
	}
	if theta := byUID["theta"].Order[0]; theta.AmbiguityReason != AmbiguityConflictingValues || len(theta.PeerValues) != 2 {
		t.Fatalf("conflicting_values must stay distinguishable in summary: %+v", theta)
	}
	if c := byUID["beta"].Criteria[0]; c.Outcome != "met" || c.State != StateResolved || c.Value != 5.0 {
		t.Fatalf("criterion summary: %+v", c)
	}
}

func TestFullProjectionAndAlternatives(t *testing.T) {
	e := NewEngine(ambiguitySnapshot())
	base := Request{OrderBy: []OrderTerm{{Metric: "score"}}, Filter: Filter{UIDs: []string{"gamma-max", "delta"}}}

	full := base
	full.Detail = DetailFull
	resp, err := e.Query(full)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Detail != DetailFull || resp.Results[0].Order[0].Selected == nil || resp.Results[0].Order[0].SelectionBasis == "" ||
		resp.Results[0].Order[0].Direction != "desc" || len(resp.Results[1].Order[0].Peers) != 2 || resp.Results[0].Order[0].Evidence != nil {
		t.Fatalf("full projection: %+v", resp.Results)
	}

	alts := base
	alts.IncludeAlternatives = true
	resp, err = e.Query(alts)
	if err != nil || resp.Detail != DetailFull || len(resp.Results[0].Order[0].Alternatives) != 2 {
		t.Fatalf("include_alternatives implies full: %+v %v", resp, err)
	}

	var inv *InvalidError
	for _, detail := range []string{DetailSummary, DetailCompact} {
		bad := base
		bad.Detail, bad.IncludeAlternatives = detail, true
		if _, err := e.Query(bad); !errors.As(err, &inv) {
			t.Fatalf("%s with alternatives must be rejected: %v", detail, err)
		}
	}
	bad := base
	bad.Detail = "verbose"
	if _, err := e.Query(bad); !errors.As(err, &inv) {
		t.Fatalf("unknown detail: %v", err)
	}
}

func TestProjectionDoesNotChangeSemantics(t *testing.T) {
	e := NewEngine(ambiguitySnapshot())
	for _, crit := range []Criterion{
		{Metric: "score", Op: "gte", Value: 60.0, Missing: MissingReject},
		{Metric: "score", Op: "gte", Value: 60.0, Missing: MissingReject, Ambiguous: AmbiguousAllow},
		{Metric: "score", Op: "gte", Value: 60.0, Missing: MissingReject, Evidence: EvidencePolicy{PreferContexts: []string{"harness2"}}},
	} {
		req := Request{Criteria: []Criterion{crit}, OrderBy: []OrderTerm{{Metric: "score"}}, Detail: DetailSummary}
		summary, err := e.Query(req)
		if err != nil {
			t.Fatal(err)
		}
		req.Detail = DetailFull
		full, err := e.Query(req)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(uids(summary), uids(full)) || summary.Eligible != full.Eligible ||
			len(summary.Exclusions) != len(full.Exclusions) || len(summary.Ambiguities) != len(full.Ambiguities) {
			t.Fatalf("projection changed results for %+v:\nsummary %v %+v\nfull    %v %+v", crit, uids(summary), summary.Exclusions, uids(full), full.Exclusions)
		}
		for i := range summary.Results {
			s, f := summary.Results[i].Order[0], full.Results[i].Order[0]
			if s.State != f.State || s.Value != f.Value || s.AmbiguityReason != f.AmbiguityReason || s.AlternativeCount != f.AlternativeCount {
				t.Fatalf("resolution differs: %+v vs %+v", s, f)
			}
			if c := summary.Results[i].Criteria[0]; c.Outcome != full.Results[i].Criteria[0].Outcome {
				t.Fatalf("outcome differs: %s vs %s", c.Outcome, full.Results[i].Criteria[0].Outcome)
			}
		}
	}
}

// get_model_details has its own two projections. The default must carry
// everything needed to make and explain a recommendation, and must never
// disagree with full about what the evidence says.
func TestModelDetailProjections(t *testing.T) {
	e := NewEngine(ambiguitySnapshot())
	rejected := []store.RejectedRow{{
		Source: "test", SourceModelName: "delta", SourceRowRef: "f.csv:2", Reason: "score \"n/a\" is not a number",
	}}

	compact, err := e.Details("delta", "", rejected)
	if err != nil {
		t.Fatal(err)
	}
	if compact.Detail != DetailCompact {
		t.Fatalf("the default projection is compact, got %q", compact.Detail)
	}
	full, err := e.Details("delta", DetailFull, rejected)
	if err != nil {
		t.Fatal(err)
	}

	// Identity, dataset and per-metric semantics are identical.
	if compact.Model != full.Model || compact.Dataset != full.Dataset || compact.CanonicalKey != full.CanonicalKey ||
		!slices.Equal(compact.MissingMetrics, full.MissingMetrics) || len(compact.Metrics) != len(full.Metrics) {
		t.Fatalf("compact must describe the same model and metrics as full:\n%+v\n%+v", compact, full)
	}
	for i := range compact.Metrics {
		c, f := compact.Metrics[i], full.Metrics[i]
		if c.Metric != f.Metric || c.DisplayName != f.DisplayName || c.Unit != f.Unit || c.Direction != f.Direction || c.Scope != f.Scope {
			t.Fatalf("metric header differs: %+v vs %+v", c, f)
		}
		if c.State != f.State || c.Value != f.Value || c.AmbiguityReason != f.AmbiguityReason || c.AlternativeCount != f.AlternativeCount {
			t.Fatalf("resolution differs for %s: %+v vs %+v", c.Metric, c, f)
		}
		if (c.ValueRange == nil) != (f.ValueRange == nil) {
			t.Fatalf("value range differs for %s", c.Metric)
		}
	}

	// What compact keeps: enough provenance to explain the value.
	var score MetricEvidence
	for _, m := range compact.Metrics {
		if m.Metric == "score" {
			score = m
		}
	}
	if score.State != StateAmbiguous || score.ValueRange == nil || len(score.PeerValues) != 2 {
		t.Fatalf("an ambiguous value must keep its range and peer values in compact: %+v", score)
	}
	for _, p := range score.PeerValues {
		if p.Context == "" || p.Source == "" {
			t.Fatalf("a compact peer value still names its source and context: %+v", p)
		}
	}
	if score.Evidence == nil || !score.Evidence.Proxy {
		t.Fatalf("compact must keep the flags that mark weaker evidence: %+v", score.Evidence)
	}
	// Ambiguity is a material caveat, so it survives the projection.
	if !strings.Contains(strings.Join(compact.Caveats, "\n"), "equally ranked observations that disagree") {
		t.Fatalf("material caveats must survive: %v", compact.Caveats)
	}

	// What compact drops: the diagnostic material, and nothing else.
	if compact.PublishedRow != nil || compact.Rejected != nil {
		t.Fatalf("compact must not carry the published row or rejected-row hints: %+v", compact)
	}
	if full.PublishedRow == nil || len(full.Rejected) != 1 {
		t.Fatalf("full must carry them: %+v", full)
	}
	for _, m := range compact.Metrics {
		if m.Selected != nil || m.Peers != nil || m.Alternatives != nil || m.SelectionBasis != "" {
			t.Fatalf("compact must not carry observation objects: %+v", m)
		}
	}

	if _, err := e.Details("delta", "summary", nil); err == nil {
		t.Fatal("get_model_details has two projections; an unknown one must be rejected")
	}
}

func TestModelDetailCompactIsSubstantiallySmaller(t *testing.T) {
	e := NewEngine(ambiguitySnapshot())
	rejected := []store.RejectedRow{{Source: "test", SourceModelName: "delta", SourceRowRef: "f.csv:2", Reason: "score is not a number"}}
	compact, err := e.Details("delta", DetailCompact, rejected)
	if err != nil {
		t.Fatal(err)
	}
	full, err := e.Details("delta", DetailFull, rejected)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := json.Marshal(compact)
	f, _ := json.Marshal(full)
	for _, key := range []string{`"selected"`, `"peers"`, `"observation_id"`, `"observed_at"`, `"selection_basis"`, `"published_row"`, `"possibly_related_rejected_rows"`} {
		if strings.Contains(string(c), key) {
			t.Errorf("compact detail payload contains %s", key)
		}
	}
	if ratio := float64(len(c)) / float64(len(f)); ratio > 0.6 {
		t.Fatalf("compact is %d bytes, full is %d bytes (ratio %.2f); expected a substantial reduction", len(c), len(f), ratio)
	}
}

func TestSummaryPayloadIsSubstantiallySmaller(t *testing.T) {
	e := NewEngine(ambiguitySnapshot())
	req := Request{
		Criteria: []Criterion{{Metric: "score", Op: "present", Ambiguous: AmbiguousAllow}},
		OrderBy:  []OrderTerm{{Metric: "score"}, {Metric: "price"}, {Metric: "flag", Direction: "desc"}},
		Limit:    25,
		Detail:   DetailSummary,
	}
	summary, err := e.Query(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Detail = DetailFull
	full, err := e.Query(req)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := json.Marshal(summary)
	f, _ := json.Marshal(full)
	for _, key := range []string{`"selected"`, `"peers"`, `"caveats"`, `"source_row_ref"`, `"observed_at"`, `"selection_basis"`, `"alternatives"`, `"observation_id"`, `"missing":true`, `"ambiguous":true`} {
		if strings.Contains(string(s), key) {
			t.Errorf("summary payload contains %s", key)
		}
	}
	// Structural, not byte-exact: the summary drops every per-value
	// observation object, so it must be well under half the full size.
	if ratio := float64(len(s)) / float64(len(f)); ratio > 0.5 {
		t.Fatalf("summary is %d bytes, full is %d bytes (ratio %.2f); expected a substantial reduction", len(s), len(f), ratio)
	}
}
