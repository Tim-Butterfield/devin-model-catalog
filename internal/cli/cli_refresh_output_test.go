package cli_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/app"
)

// The normal refresh output is meant to be read in an ordinary terminal, so it
// is held to a width rather than to an exact layout. Tabwriter padding is free
// to change; the promise is that a refresh of these sources fits.
const consoleWidth = 80

// refreshedCLI runs the full workflow and returns the normal and JSON forms of
// the same refresh.
func refreshedCLI(t *testing.T) (*runner, string, string) {
	t.Helper()
	r := newRunner(t)
	r.must("datasets", "refresh")
	r.must("use-dataset", "1")
	text := r.must("refresh")
	// A second refresh is idempotent, so the JSON describes the same counts.
	jsonOut := r.must("refresh", "--json")
	return r, text, jsonOut
}

// TestRefreshNormalOutputHasNoDataPointColumn is the presentation contract: the
// per-source count of individual metric values is a `--json` concern. Neither
// its old name nor its new one may reappear as a column, because the column was
// the thing that made the table too wide and invited "1221 models".
func TestRefreshNormalOutputHasNoDataPointColumn(t *testing.T) {
	_, text, _ := refreshedCLI(t)

	header := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "SOURCE") {
			header = line
			break
		}
	}
	if header == "" {
		t.Fatalf("no source table in:\n%s", text)
	}
	for _, banned := range []string{"OBSERVATIONS", "DATA POINTS", "DATA_POINTS"} {
		if strings.Contains(text, banned) {
			t.Errorf("normal refresh output must not carry a %s column:\n%s", banned, text)
		}
	}
	if got := strings.Fields(header); !slices.Equal(got,
		[]string{"SOURCE", "STATUS", "PUBLISHED", "ACCEPTED", "EXCLUDED", "REJECTED", "UNMATCHED"}) {
		t.Errorf("refresh columns = %v", got)
	}
}

// TestRefreshNormalOutputFitsEightyColumns covers every line the fixtures
// produce: the dataset heading, the table, and the follow-up lines.
func TestRefreshNormalOutputFitsEightyColumns(t *testing.T) {
	_, text, _ := refreshedCLI(t)
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if len(line) > consoleWidth {
			t.Errorf("%d columns (limit %d): %q", len(line), consoleWidth, line)
		}
	}
}

// TestRefreshTableFitsAtLiveCounts states the width as a design property
// rather than a fixture accident. The fixtures publish two-digit counts; the
// live sources publish four-digit ones. Tabwriter gives each column
// max(heading, widest cell) plus two spaces of padding, with no padding after
// the last column, so widening every count to its live size and every source
// code and status to their longest real values must still fit.
func TestRefreshTableFitsAtLiveCounts(t *testing.T) {
	columns := [][]string{
		{"SOURCE", "manual_context"}, // the longest code a manual import is likely to use
		{"STATUS", "success"},        // the longer of the two statuses
		{"PUBLISHED", "9999"},        // every count an order of magnitude above live
		{"ACCEPTED", "9999"},
		{"EXCLUDED", "9999"},
		{"REJECTED", "9999"},
		{"UNMATCHED", "9999"},
	}
	width := 0
	for i, col := range columns {
		cell := 0
		for _, v := range col {
			if len(v) > cell {
				cell = len(v)
			}
		}
		width += cell
		if i < len(columns)-1 {
			width += 2 // tabwriter padding, dropped after the final column
		}
	}
	if width > consoleWidth {
		t.Errorf("the refresh table needs %d columns at live sizes (limit %d)", width, consoleWidth)
	}
}

// TestRefreshDatasetHeadingNamesModels keeps the heading in the unit a user
// thinks in. "232 models" cannot be misread as rows or as values.
func TestRefreshDatasetHeadingNamesModels(t *testing.T) {
	_, text, jsonOut := refreshedCLI(t)

	var report app.RefreshReport
	if err := json.Unmarshal([]byte(jsonOut), &report); err != nil {
		t.Fatal(err)
	}
	if report.CatalogModels == nil {
		t.Fatal("a successful refresh must report catalog_models")
	}
	first, _, _ := strings.Cut(text, "\n")
	if !strings.HasPrefix(first, "Dataset: ") || !strings.Contains(first, "models,") {
		t.Fatalf("dataset heading = %q", first)
	}
}

// TestRefreshNormalOutputDropsTheAccountingParagraph keeps the happy path
// quiet. The row-accounting invariant is asserted in tests and stated in the
// architecture notes; it is not console prose on every refresh.
func TestRefreshNormalOutputDropsTheAccountingParagraph(t *testing.T) {
	_, text, _ := refreshedCLI(t)
	if strings.Contains(text, "PUBLISHED = ACCEPTED") {
		t.Errorf("the accounting paragraph should not be printed:\n%s", text)
	}
	// The fixtures do produce rejected rows and unmatched evidence, so the
	// conditional follow-ups must appear, name the count, and point somewhere.
	if !strings.Contains(text, "`devmodels rejected") {
		t.Errorf("rejected rows exist but nothing points at them:\n%s", text)
	}
	if !strings.Contains(text, "data points describe models this dataset does not offer") ||
		!strings.Contains(text, "`devmodels unmatched`") {
		t.Errorf("unmatched evidence is not explained:\n%s", text)
	}
}

// TestRefreshJSONUsesDataPoints is the machine-readable contract: this count
// has one name across every surface, not two.
func TestRefreshJSONUsesDataPoints(t *testing.T) {
	_, _, jsonOut := refreshedCLI(t)
	if strings.Contains(jsonOut, "observation_count") {
		t.Errorf("refresh --json still emits observation_count:\n%s", jsonOut)
	}
	if strings.Contains(jsonOut, "unmatched_observations") {
		t.Errorf("refresh --json still emits unmatched_observations:\n%s", jsonOut)
	}
	if !strings.Contains(jsonOut, `"data_points"`) || !strings.Contains(jsonOut, `"unmatched_data_points"`) {
		t.Errorf("refresh --json lost the renamed counts:\n%s", jsonOut)
	}

	var report app.RefreshReport
	if err := json.Unmarshal([]byte(jsonOut), &report); err != nil {
		t.Fatalf("refresh --json must stay valid JSON: %v", err)
	}
	if len(report.Sources) == 0 || !report.Succeeded {
		t.Fatalf("report: %+v", report)
	}
	for _, s := range report.Sources {
		if s.Counts.DataPoints < s.Counts.Accepted {
			t.Errorf("%s: %d data points for %d accepted rows", s.Source, s.Counts.DataPoints, s.Counts.Accepted)
		}
		if s.Counts.Published != s.Counts.Accepted+s.Counts.Excluded+s.Counts.Rejected {
			t.Errorf("%s: published rows are not accounted for", s.Source)
		}
	}
}

// TestStatusAndMetricsUseDataPoints covers the other two surfaces that expose
// the same count, in both their human and machine forms.
func TestStatusAndMetricsUseDataPoints(t *testing.T) {
	r, _, _ := refreshedCLI(t)

	for _, args := range [][]string{{"status", "--json"}, {"metrics", "--detail", "full", "--json"}} {
		out := r.must(args...)
		for _, old := range []string{"observation_count", "unmatched_observations", "rejected_count"} {
			if strings.Contains(out, old) {
				t.Errorf("%v still emits %s:\n%s", args, old, out)
			}
		}
		if !strings.Contains(out, `"data_points"`) {
			t.Errorf("%v does not emit data_points:\n%s", args, out)
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Errorf("%v is not valid JSON: %v", args, err)
		}
	}
	if out := r.must("status", "--json"); !strings.Contains(out, `"rejected_rows"`) ||
		!strings.Contains(out, `"unmatched_data_points"`) {
		t.Errorf("status --json lost a renamed field:\n%s", out)
	}

	status := r.must("status")
	if strings.Contains(status, "OBSERVATIONS") || !strings.Contains(status, "DATA POINTS") {
		t.Errorf("status table heading:\n%s", status)
	}
	if !strings.Contains(status, "Data points about models this dataset does not offer") {
		t.Errorf("status unmatched line:\n%s", status)
	}
	// `metrics` describes each metric vertically rather than in a wide table,
	// so the count is named in prose. What matters is that the name is the one
	// every surface uses for it.
	metrics := r.must("metrics", "--detail", "full")
	if strings.Contains(strings.ToLower(metrics), "observations") {
		t.Errorf("metrics must not call stored values observations:\n%s", metrics)
	}
	if !strings.Contains(metrics, "data points") {
		t.Errorf("metrics must name its stored-value count data points:\n%s", metrics)
	}
	// `unmatched` prints one row per model name, metric and context, so its row
	// count is not a data-point count and must not be presented as one.
	unmatched := r.must("unmatched")
	// Human output is wrapped to a fixed width, so a sentence may be split
	// across lines. What matters here is the wording, not where it breaks.
	flat := strings.Join(strings.Fields(unmatched), " ")
	if strings.Contains(flat, "observations describe models") {
		t.Errorf("unmatched summary line:\n%s", unmatched)
	}
	if !strings.Contains(flat, "rows above, covering") ||
		!strings.Contains(flat, "data points that describe models this dataset does not offer") {
		t.Errorf("unmatched must name its rows and its data points separately:\n%s", unmatched)
	}
}
