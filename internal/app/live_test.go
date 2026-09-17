//go:build live

package app_test

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/app"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/query"
)

// TestLiveRefresh collects from the real sources and refreshes every supported
// dataset in turn. Run with:
//
//	go test ./internal/app -run TestLiveRefresh -tags=live -v
func TestLiveRefresh(t *testing.T) {
	ctx := context.Background()
	svc, err := app.Open(ctx, app.Options{DBPath: filepath.Join(t.TempDir(), "live.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	list, err := svc.RefreshDatasets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var supported []int
	for _, d := range list.Datasets {
		t.Logf("dataset %d: %s [%s] supported=%v rows=%d %s", d.ID, d.DisplayName, d.SourceKey, d.Supported, d.ModelRowCount, d.UnsupportedReason)
		if d.Supported {
			supported = append(supported, d.ID)
		}
	}
	for _, w := range list.Warnings {
		t.Logf("warning: %s", w)
	}
	if len(supported) == 0 {
		t.Fatal("the live page offers no supported dataset")
	}

	for i, id := range supported {
		if _, err := svc.UseDataset(ctx, id); err != nil {
			t.Fatal(err)
		}
		opts := app.RefreshOptions{}
		if i > 0 {
			opts.Sources = []string{"devin"} // evidence sources were refreshed with the first dataset
		}
		report, err := svc.Refresh(ctx, opts)
		t.Logf("--- dataset %d: %s", id, report.Dataset.DisplayName)
		for _, s := range report.Sources {
			unmatched := "-"
			if s.Unmatched != nil {
				unmatched = strconv.Itoa(*s.Unmatched)
			}
			t.Logf("%-8s %-8s published=%d accepted=%d excluded=%d rejected=%d data_points=%d unmatched=%s %s",
				s.Source, s.Status, s.Counts.Published, s.Counts.Accepted, s.Counts.Excluded, s.Counts.Rejected, s.Counts.DataPoints, unmatched, s.Error)
			if s.Counts.Published != s.Counts.Accepted+s.Counts.Excluded+s.Counts.Rejected {
				t.Errorf("%s does not account for every published row", s.Source)
			}
			for _, w := range s.Warnings {
				t.Logf("  warning: %s", w)
			}
		}
		// Every reason a live row was set aside, so a sharp rise is visible
		// and attributable rather than just a larger number.
		rejected, err := svc.Rejected(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rejected {
			t.Logf("rejected %-10s %-28s %s: %s", r.Source, r.SourceRowRef, r.SourceModelName, r.Reason)
		}
		if err != nil {
			t.Fatal(err)
		}

		d, err := svc.Describe(ctx, "full")
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range d.Metrics {
			if m.CatalogModelsWithValue > 0 {
				t.Logf("metric %-50s models_with_value=%d data_points=%d", m.Key, m.CatalogModelsWithValue, *m.DataPoints)
			}
		}
		// The contexts the live sources actually named this run. Their number
		// tracks the field, so a sudden change is worth reading.
		t.Logf("evaluation contexts: %d", len(d.EvaluationContexts))
		for _, c := range d.EvaluationContexts {
			t.Logf("  %-32s %-18s %s", c.Code, c.Kind, c.Name)
		}

		resp, err := svc.Query(ctx, query.Request{
			Criteria: []query.Criterion{{Metric: "swe_bench_verified", Op: "present"}},
			OrderBy:  []query.OrderTerm{{Metric: "swe_bench_verified"}},
			Limit:    5,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range resp.Results {
			t.Logf("#%d %s (%s) swe_bench_verified=%v state=%s", r.Rank, r.Model.Label, r.Model.UID, r.Order[0].Value, r.Order[0].State)
		}
		for _, a := range resp.Ambiguities {
			t.Logf("ambiguity: %s models=%d contexts=%v", a.Metric, a.Models, a.Contexts)
		}
	}
}
