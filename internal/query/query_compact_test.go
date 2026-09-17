package query

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/store"
)

// broadSnapshot adds n synthetic high-effort models to ambiguitySnapshot so a
// broad query returns many results in every state: missing, resolved from one
// harness, resolved effort-inexact, Devin-measured with an alternative, and
// ambiguous across peer contexts.
func broadSnapshot(n int) *store.Snapshot {
	snap := ambiguitySnapshot()
	id := int64(1000)
	add := func(o store.ObservationRow) {
		id++
		o.ID, o.Source = id, "test"
		snap.Observations = append(snap.Observations, o)
	}
	for i := range n {
		key, uid := fmt.Sprintf("model %02d", i), fmt.Sprintf("model-%02d-high", i)
		snap.Models = append(snap.Models, store.CatalogModel{UID: uid, Label: fmt.Sprintf("Model %02d High", i), Provider: "P", BaseName: key, BaseKey: key, Effort: "high"})
		v := float64(40 + (i*7)%50)
		score := func(ctx, effort string, value float64) {
			add(store.ObservationRow{Metric: "score", BaseKey: key, Effort: effort, EvaluationContext: ctx, Number: num(value), SourceModelName: key})
		}
		switch i % 4 {
		case 1:
			score("harness", "high", v)
		case 2:
			score("harness", "high", v)
			score("harness2", "high", v+3)
		case 3:
			score("devin", "high", v)
			score("harness", "high", v+5)
		default:
			if i%8 == 4 {
				score("harness", "", v)
			}
		}
		if i%5 != 0 {
			add(store.ObservationRow{Metric: "price", CatalogUID: uid, EvaluationContext: "published", Number: num(float64(5 + i%9))})
		}
		if i%2 == 0 {
			add(store.ObservationRow{Metric: "flag", CatalogUID: uid, EvaluationContext: "published", Number: num(float64(i % 4 / 2))})
		}
	}
	return snap
}

func project(t *testing.T, e *Engine, req Request, detail string) *Response {
	t.Helper()
	req.Detail = detail
	resp, err := e.Query(req)
	if err != nil {
		t.Fatalf("detail %q: %v", detail, err)
	}
	return resp
}

// broadRequest is a candidate-selection query: one criterion that is also an
// order term, and several order terms.
var broadRequest = Request{
	Criteria: []Criterion{{Metric: "score", Op: "gte", Value: 45.0, Missing: MissingAllow, Ambiguous: AmbiguousAllow}},
	OrderBy:  []OrderTerm{{Metric: "score"}, {Metric: "price"}, {Metric: "flag", Direction: "desc"}},
	Limit:    25,
}

// projectionRequests cover the shapes a projection could get wrong: ambiguity
// and missing policies, context restrictions and preferences, missing values
// sorted first, a criterion repeated as an order term (with and without the
// same evidence policy), and one metric ordered under two policies.
func projectionRequests() []Request {
	return []Request{
		broadRequest,
		{Criteria: []Criterion{{Metric: "score", Op: "gte", Value: 60.0, Missing: MissingAllow}}, OrderBy: []OrderTerm{{Metric: "price", Missing: "first"}}, Limit: 50},
		{Criteria: []Criterion{{Metric: "score", Op: "gte", Value: 50.0, Missing: MissingReject, Ambiguous: AmbiguousAllow}, {Metric: "price", Op: "lte", Value: 10.0, Missing: MissingAllow}},
			OrderBy: []OrderTerm{{Metric: "score"}, {Metric: "score", Evidence: EvidencePolicy{PreferContexts: []string{"harness2"}}}, {Metric: "price"}}, Limit: 50},
		{Criteria: []Criterion{{Metric: "score", Op: "gte", Value: 50.0, Missing: MissingReject, Evidence: EvidencePolicy{PreferContexts: []string{"harness"}}}},
			OrderBy: []OrderTerm{{Metric: "score"}}, Limit: 50},
		{OrderBy: []OrderTerm{{Metric: "score", Evidence: EvidencePolicy{Contexts: []string{"harness"}}}}, Filter: Filter{UIDs: []string{"delta", "theta", "beta", "alpha-high-fast"}}},
	}
}

func TestCompactIsTheDefaultAndMateriallySmaller(t *testing.T) {
	e := NewEngine(broadSnapshot(40))
	compact := project(t, e, broadRequest, "")
	summary := project(t, e, broadRequest, DetailSummary)
	if compact.Detail != DetailCompact || len(compact.Criteria) != 1 || len(compact.OrderBy) != 3 || compact.Returned < 20 {
		t.Fatalf("compact default: detail %q criteria %+v order_by %+v returned %d", compact.Detail, compact.Criteria, compact.OrderBy, compact.Returned)
	}
	results, _ := json.Marshal(compact.Results)
	for _, key := range []string{`"metric"`, `"evidence"`, `"source"`, `"selected"`, `"peers"`, `"peer_contexts"`, `"selection_basis"`, `"missing":true`, `"ambiguous":true`, `"op"`, `"direction"`} {
		if strings.Contains(string(results), key) {
			t.Errorf("compact results contain %s", key)
		}
	}
	c, _ := json.Marshal(compact)
	s, _ := json.Marshal(summary)
	// Structural, not byte-exact: compact names each term once instead of once
	// per value and reduces evidence objects to a context and flags.
	ratio := float64(len(c)) / float64(len(s))
	t.Logf("compact %d bytes, summary %d bytes, ratio %.2f", len(c), len(s), ratio)
	if ratio > 0.65 {
		t.Fatalf("compact is %d bytes, summary is %d bytes (ratio %.2f); expected a material reduction", len(c), len(s), ratio)
	}
}

// sameValue checks that a compact value reports exactly the summary's facts.
func sameValue(t *testing.T, where string, c, s Resolution) {
	t.Helper()
	if c.State != s.State || !reflect.DeepEqual(c.Value, s.Value) || c.AmbiguityReason != s.AmbiguityReason ||
		!reflect.DeepEqual(c.ValueRange, s.ValueRange) || c.AlternativeCount != s.AlternativeCount {
		t.Fatalf("%s: compact %+v, summary %+v", where, c, s)
	}
	if c.Missing || c.Ambiguous || c.Evidence != nil || c.Selected != nil || c.Peers != nil || c.PeerContexts != nil {
		t.Fatalf("%s: compact carries summary or full fields: %+v", where, c)
	}
	var flags []string
	context := ""
	if ev := s.Evidence; ev != nil {
		context = ev.Context
		if ev.Proxy {
			flags = append(flags, FlagProxy)
		}
		if !ev.EffortExact {
			flags = append(flags, FlagEffortInexact)
		}
		if !ev.ServingExact {
			flags = append(flags, FlagServingInexact)
		}
		if ev.Alias {
			flags = append(flags, FlagAlias)
		}
	}
	if c.Context != context || !slices.Equal(c.Flags, flags) {
		t.Fatalf("%s: context %q flags %v, want %q %v", where, c.Context, c.Flags, context, flags)
	}
	if len(c.PeerValues) != len(s.PeerValues) {
		t.Fatalf("%s: peer values %+v, want %+v", where, c.PeerValues, s.PeerValues)
	}
	for j, p := range c.PeerValues {
		if p.Value != s.PeerValues[j].Value || p.Context != s.PeerValues[j].Context || p.Source != "" {
			t.Fatalf("%s: peer %d %+v, want %+v", where, j, p, s.PeerValues[j])
		}
	}
}

func TestCompactProjectionDoesNotChangeSemantics(t *testing.T) {
	e := NewEngine(broadSnapshot(40))
	for n, req := range projectionRequests() {
		c, s, f := project(t, e, req, DetailCompact), project(t, e, req, DetailSummary), project(t, e, req, DetailFull)
		if !slices.Equal(uids(c), uids(s)) || !slices.Equal(uids(c), uids(f)) {
			t.Fatalf("request %d: order differs:\ncompact %v\nsummary %v\nfull    %v", n, uids(c), uids(s), uids(f))
		}
		for _, other := range []*Response{s, f} {
			if c.CatalogModels != other.CatalogModels || c.MatchedFilter != other.MatchedFilter || c.Eligible != other.Eligible ||
				c.ExcludedModels != other.ExcludedModels || c.Returned != other.Returned ||
				!reflect.DeepEqual(c.Exclusions, other.Exclusions) || !reflect.DeepEqual(c.Ambiguities, other.Ambiguities) {
				t.Fatalf("request %d: counts, exclusions or ambiguities differ between compact and %s", n, other.Detail)
			}
		}
		for k, term := range c.OrderBy {
			if term.Metric != req.OrderBy[k].Metric || term.Direction == "" || term.Missing == "" {
				t.Fatalf("request %d: order_by[%d] = %+v", n, k, term)
			}
		}
		for i, term := range c.Criteria {
			if term.Metric != req.Criteria[i].Metric || (term.Op != "present" && term.Missing == "") || term.Ambiguous == "" {
				t.Fatalf("request %d: criteria[%d] = %+v", n, i, term)
			}
			if k := term.ValueInOrderBy; k != nil && (c.OrderBy[*k].Metric != term.Metric || !SameEvidencePolicy(c.OrderBy[*k].Evidence, term.Evidence)) {
				t.Fatalf("request %d: criteria[%d] points at a different order term %+v", n, i, c.OrderBy[*k])
			}
		}
		for r := range c.Results {
			cr, sr, fr := c.Results[r], s.Results[r], f.Results[r]
			for k := range cr.Order {
				sameValue(t, fmt.Sprintf("request %d result %d order[%d]", n, r, k), cr.Order[k].Resolution, sr.Order[k].Resolution)
				if fr.Order[k].State != sr.Order[k].State {
					t.Fatalf("request %d: full and summary states differ", n)
				}
			}
			for i := range cr.Criteria {
				where := fmt.Sprintf("request %d result %d criteria[%d]", n, r, i)
				if cr.Criteria[i].Outcome != sr.Criteria[i].Outcome || cr.Criteria[i].Outcome != fr.Criteria[i].Outcome {
					t.Fatalf("%s: outcome %q, summary %q, full %q", where, cr.Criteria[i].Outcome, sr.Criteria[i].Outcome, fr.Criteria[i].Outcome)
				}
				if k := c.Criteria[i].ValueInOrderBy; k != nil {
					if !reflect.DeepEqual(cr.Criteria[i].Resolution, Resolution{}) {
						t.Fatalf("%s: a criterion reported in order_by carries only its outcome: %+v", where, cr.Criteria[i])
					}
					if !reflect.DeepEqual(sr.Criteria[i].Resolution, sr.Order[*k].Resolution) {
						t.Fatalf("%s: value_in_order_by must only be set when the values are identical", where)
					}
					continue
				}
				sameValue(t, where, cr.Criteria[i].Resolution, sr.Criteria[i].Resolution)
			}
		}
	}
}

func TestCompactValuesStayExplicit(t *testing.T) {
	snap := ambiguitySnapshot()
	snap.Aliases = []store.Alias{{SourceCode: "test", SourceNameNormalized: "zeta 1", BaseKey: "beta"}}
	e := NewEngine(snap)
	resp := project(t, e, Request{OrderBy: []OrderTerm{{Metric: "score"}, {Metric: "price"}}}, "")
	by := map[string]Result{}
	for _, r := range resp.Results {
		by[r.Model.UID] = r
	}
	entry := func(uid string, k int) (Resolution, string) {
		v := by[uid].Order[k]
		raw, _ := json.Marshal(v)
		return v.Resolution, string(raw)
	}

	if v, _ := entry("gamma-max", 0); v.State != StateResolved || v.Value != 70.0 || v.Context != "devin" || v.Flags != nil || v.AlternativeCount != 2 {
		t.Fatalf("Devin-measured exact value has no flags: %+v", v)
	}
	if v, raw := entry("alpha-high", 0); v.State != StateResolved || v.Value != 0.0 || !strings.Contains(raw, `"value":0`) || !slices.Equal(v.Flags, []string{FlagProxy}) {
		t.Fatalf("a real zero stays a value: %s", raw)
	}
	if v, _ := entry("alpha-high-fast", 0); !slices.Equal(v.Flags, []string{FlagProxy, FlagServingInexact}) {
		t.Fatalf("borrowed serving variant: %+v", v)
	}
	if v, _ := entry("beta", 0); !slices.Equal(v.Flags, []string{FlagProxy, FlagAlias}) || v.Value != 50.0 {
		t.Fatalf("alias flag: %+v", v)
	}
	if _, raw := entry("gamma-max", 1); raw != `{"state":"missing"}` {
		t.Fatalf("missing must be explicit and carry no value: %s", raw)
	}
	delta, raw := entry("delta", 0)
	if delta.State != StateAmbiguous || delta.Value != nil || strings.Contains(raw, `"ambiguity_reason":"peer_contexts","value"`) ||
		delta.AmbiguityReason != AmbiguityPeerContexts || delta.ValueRange == nil || delta.ValueRange.Min != 55 || delta.ValueRange.Max != 71 ||
		!reflect.DeepEqual(delta.PeerValues, []PeerValue{{Value: 55.0, Context: "harness"}, {Value: 71.0, Context: "harness2"}}) ||
		!slices.Equal(delta.Flags, []string{FlagProxy}) || delta.Context != "" {
		t.Fatalf("ambiguous values list every peer and no representative value: %s", raw)
	}
	if theta, raw := entry("theta", 0); theta.AmbiguityReason != AmbiguityConflictingValues ||
		!reflect.DeepEqual(theta.PeerValues, []PeerValue{{Value: 40.0, Context: "harness"}, {Value: 44.0, Context: "harness"}}) {
		t.Fatalf("conflicting values stay distinguishable: %s", raw)
	}
}

func TestCompactListsTermsOnceAndReportsSharedValuesOnce(t *testing.T) {
	e := NewEngine(ambiguitySnapshot())
	resp := project(t, e, Request{
		Criteria: []Criterion{
			{Metric: "score", Op: "GTE", Value: 50.0, Missing: "Allow", Ambiguous: AmbiguousAllow},
			{Metric: "score", Op: "gte", Value: 50.0, Missing: MissingAllow, Evidence: EvidencePolicy{PreferContexts: []string{"harness2"}}},
			{Metric: "price", Op: "present"},
		},
		OrderBy: []OrderTerm{{Metric: "price"}, {Metric: "score"}},
	}, "")
	crit := resp.Criteria
	if len(crit) != 3 || crit[0].ValueInOrderBy == nil || *crit[0].ValueInOrderBy != 1 || crit[0].Op != "gte" || crit[0].Missing != MissingAllow ||
		crit[1].ValueInOrderBy != nil || crit[2].ValueInOrderBy == nil || *crit[2].ValueInOrderBy != 0 || crit[2].Missing != MissingReject {
		t.Fatalf("normalized criteria: %+v", crit)
	}
	if o := resp.OrderBy; o[0].Direction != "asc" || o[1].Direction != "desc" || o[1].Missing != "last" {
		t.Fatalf("normalized order_by: %+v", o)
	}
	if resp.Eligible == 0 {
		t.Fatal("expected eligible models")
	}
	for _, r := range resp.Results {
		for _, i := range []int{0, 2} {
			if raw, _ := json.Marshal(r.Criteria[i]); !strings.HasPrefix(string(raw), `{"outcome":"`) || strings.Count(string(raw), `":`) != 1 {
				t.Fatalf("criterion %d reported in order_by must carry only its outcome: %s", i, raw)
			}
		}
		if r.Criteria[1].State == "" {
			t.Fatalf("a criterion with its own evidence policy carries its own value: %+v", r.Criteria[1])
		}
	}
	raw, _ := json.Marshal(resp)
	if !strings.Contains(string(raw), `"value_in_order_by":0`) || !strings.Contains(string(raw), `"value_in_order_by":1`) {
		t.Fatalf("value_in_order_by must be visible in the response: %s", raw)
	}
}

func TestCompactRejectsInvalidCombinations(t *testing.T) {
	e := NewEngine(ambiguitySnapshot())
	var inv *InvalidError
	if _, err := e.Query(Request{Detail: DetailCompact, IncludeAlternatives: true}); !errors.As(err, &inv) {
		t.Fatalf("compact with alternatives: %v", err)
	}
	if _, err := e.Query(Request{Detail: "tiny"}); !errors.As(err, &inv) || !strings.Contains(err.Error(), "compact, summary or full") {
		t.Fatalf("unknown detail: %v", err)
	}
}
