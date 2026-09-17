package app_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/app"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/query"
)

// TestDescribeSummaryIsMateriallySmaller uses the real metric and source
// definitions with fixture data, so it measures the projection rather than
// today's live coverage.
func TestDescribeSummaryIsMateriallySmaller(t *testing.T) {
	h := newHarness(t)
	h.datasetsRefresh()
	h.use(1)
	h.refresh(app.RefreshOptions{})

	summary, err := h.svc.Describe(h.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	full, err := h.svc.Describe(h.ctx, query.DetailFull)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := json.Marshal(summary)
	f, _ := json.Marshal(full)

	for _, key := range []string{`"access_basis"`, `"attribution"`, `"license"`, `"missing_possible"`, `"data_points"`, `"defined_by"`, `"homepage"`} {
		if strings.Contains(string(s), key) {
			t.Errorf("summary contains %s", key)
		}
		if !strings.Contains(string(f), key) {
			t.Errorf("full lost %s", key)
		}
	}
	if !strings.Contains(string(s), `"devin_legacy_credits"`) || !slices.Contains(summary.MetricsWithoutValues, "devin_legacy_credits") {
		t.Fatalf("a metric with no values in the dataset must still be named: %v", summary.MetricsWithoutValues)
	}
	if ratio := float64(len(s)) / float64(len(f)); ratio > 0.75 {
		t.Fatalf("describe summary %d bytes vs full %d bytes (ratio %.2f); expected a material reduction", len(s), len(f), ratio)
	}

	// The zero-rate rule is stated once per projection, not repeated in every
	// price description.
	if strings.Count(string(f), "missing, not zero") != 1 || strings.Count(string(s), "missing, not zero") != 1 {
		t.Fatalf("zero-rate rule should appear exactly once per projection")
	}

	if _, err := h.svc.Describe(h.ctx, "verbose"); app.KindOf(err) != app.KindUsage {
		t.Fatalf("invalid detail: %v", err)
	}
}

// TestDescribeSummaryStaysWithinItsBudget is a regression guard on the size of
// the default response, which a host has to be able to hand an agent whole.
//
// The number is a budget with room in it, not the current size: the summary
// should be free to gain a metric without a test change. Nothing is dropped to
// meet it — the summary lists every metric and every context in use whatever
// the total comes to — so if this fails, the fix is to stop repeating
// something, never to start omitting something.
//
// The fixture catalog has a dozen evaluation contexts where the live one has
// dozens, so this bounds the whole response while
// TestDescribeSummaryDoesNotPayTwicePerEvaluationContext bounds the part that
// actually grows.
func TestDescribeSummaryStaysWithinItsBudget(t *testing.T) {
	h := newHarness(t)
	h.datasetsRefresh()
	h.use(1)
	h.refresh(app.RefreshOptions{})

	summary, err := h.svc.Describe(h.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	// 8212 bytes on this fixture before the evaluation contexts stopped being
	// listed twice, 7734 after. One round budget with room for a metric or two.
	const budget = 8192
	if len(s) > budget {
		t.Errorf("the default summary is %d bytes, above its %d-byte budget; find what it now says twice", len(s), budget)
	}

	// The ceiling has to have been earned: every metric and every context in
	// use is still there, so the bytes went to de-duplication and not to a
	// shorter list.
	full, err := h.svc.Describe(h.ctx, query.DetailFull)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Metrics)+len(summary.MetricsWithoutValues) != len(full.Metrics) {
		t.Fatalf("summary describes %d+%d metrics, full describes %d",
			len(summary.Metrics), len(summary.MetricsWithoutValues), len(full.Metrics))
	}
	used := map[string]bool{}
	for _, m := range summary.Metrics {
		for _, c := range m.EvaluationContexts {
			used[c] = true
		}
	}
	grouped := map[string]bool{}
	for _, codes := range summary.EvaluationContextsByKind {
		for _, c := range codes {
			grouped[c] = true
		}
	}
	for c := range used {
		if !grouped[c] {
			t.Errorf("context %s is used by a metric but is not listed with its kind", c)
		}
	}
	for c := range grouped {
		if !used[c] {
			t.Errorf("context %s is listed but no listed metric uses it", c)
		}
	}
}
