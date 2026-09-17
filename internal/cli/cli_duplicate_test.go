package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/app"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/query"
)

// Enterprise fixture facts these tests rely on: BFCL publishes the base model
// of claude-opus-5-medium in two modes, 77.47 (bfcl_fc) and 70 (bfcl_prompt).
// Both are direct evaluations (proxy) measured without the medium effort
// level (inexact), and without a context choice they are ambiguous peers.

func refreshedRunner(t *testing.T) (*runner, *app.Service) {
	t.Helper()
	r := newRunner(t)
	r.must("datasets", "refresh")
	r.must("use-dataset", "2")
	r.must("refresh")
	svc, err := app.Open(context.Background(), app.Options{DBPath: r.vars["DEVMODELS_DB"], HTTP: r.fetch})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	return r, svc
}

// renderQuery runs a JSON request through the CLI. It checks that CLI JSON
// equals the service result in every projection and that the text table is
// the same whichever projection produced it, and returns the table.
func renderQuery(t *testing.T, r *runner, svc *app.Service, body string) (string, string) {
	t.Helper()
	path := writeTemp(t, body)
	var table, legend string
	for _, detail := range []string{"", query.DetailCompact, query.DetailSummary, query.DetailFull} {
		args := []string{"query", "--request", path}
		if detail != "" {
			args = append(args, "--detail", detail)
		}
		var req query.Request
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		req.Detail = detail
		direct, err := svc.Query(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		directJSON, _ := json.Marshal(direct)
		var a, b any
		json.Unmarshal([]byte(r.must(append(args, "--json")...)), &a)
		json.Unmarshal(directJSON, &b)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("CLI JSON and service disagree for detail %q", detail)
		}

		// The table is the block after the two summary lines, and the column
		// legend is the block after that. Metric keys are too wide to head a
		// column, so the table numbers its value columns and the legend says
		// which term each number is; both have to be stable across projections.
		parts := strings.SplitN(r.must(args...), "\n\n", 4)
		if len(parts) < 3 {
			t.Fatalf("no table and legend for detail %q", detail)
		}
		if table == "" {
			table, legend = parts[1], parts[2]
		} else if parts[1] != table || parts[2] != legend {
			t.Fatalf("text table differs for detail %q:\n%s\n%s\nwant:\n%s\n%s", detail, parts[1], parts[2], table, legend)
		}
	}
	return table, legend
}

// columnLabels returns the metric term each numbered column stands for, in
// column order, from the legend printed under the table.
func columnLabels(t *testing.T, legend string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(legend, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[") {
			// A continuation of the previous, wrapped label.
			if len(out) > 0 && line != "" {
				out[len(out)-1] += " " + line
			}
			continue
		}
		if i := strings.Index(line, "] "); i > 0 {
			out = append(out, line[i+2:])
		}
	}
	if len(out) == 0 {
		t.Fatalf("no column legend in:\n%s", legend)
	}
	return out
}

func header(table string) string {
	return strings.SplitN(table, "\n", 2)[0]
}

func row(t *testing.T, table, uid string) string {
	t.Helper()
	for _, line := range strings.Split(table, "\n") {
		if strings.Contains(line, " "+uid+" ") {
			return line
		}
	}
	t.Fatalf("no row for %s in:\n%s", uid, table)
	return ""
}

// inOrder checks that each want appears in line after the previous one.
func inOrder(t *testing.T, line string, want ...string) {
	t.Helper()
	rest := line
	for _, w := range want {
		i := strings.Index(rest, w)
		if i < 0 {
			t.Fatalf("%q missing or out of order in %q (want %q)", w, line, want)
		}
		rest = rest[i+len(w):]
	}
}

func TestDuplicateMetricOrderTermsRenderAsDistinctColumns(t *testing.T) {
	r, svc := refreshedRunner(t)
	table, legend := renderQuery(t, r, svc, `{"order_by":[
		{"metric":"bfcl_overall_accuracy","evidence":{"contexts":["bfcl_fc"]}},
		{"metric":"bfcl_overall_accuracy","evidence":{"contexts":["bfcl_prompt"]}},
		{"metric":"bfcl_overall_accuracy"}],"limit":50}`)

	labels := columnLabels(t, legend)
	want := []string{
		"BFCL_OVERALL_ACCURACY[contexts=bfcl_fc]",
		"BFCL_OVERALL_ACCURACY[contexts=bfcl_prompt]",
		"BFCL_OVERALL_ACCURACY[default]",
	}
	if len(labels) != 3 {
		t.Fatalf("want three BFCL columns, got %d: %q", len(labels), labels)
	}
	for i, w := range want {
		if labels[i] != w {
			t.Fatalf("column %d is %q, want %q (all: %q)", i+1, labels[i], w, labels)
		}
	}
	// Each labelled column is a column in the table, in the same order.
	inOrder(t, header(table), "RANK", "MODEL", "UID", "[1]", "[2]", "[3]")
	// Flagged, flagged and ambiguous: three distinct values, none overwritten.
	inOrder(t, row(t, table, "claude-opus-5-medium"), "claude-opus-5-medium", "77.47*~", "70*~", "70..77.47?")

	missing := false
	for _, line := range strings.Split(table, "\n") {
		if strings.Count(line, "—") == 3 {
			missing = true
		}
	}
	if !missing {
		t.Fatalf("a model without BFCL evidence must show missing in every BFCL column:\n%s", table)
	}
}

func TestCriterionSharingAnOrderValueRendersOneColumn(t *testing.T) {
	r, svc := refreshedRunner(t)
	body := `{"criteria":[{"metric":"bfcl_overall_accuracy","op":"gte","value":60,"missing":"allow","evidence":{"prefer_contexts":["bfcl_fc"]}}],
		"order_by":[{"metric":"bfcl_overall_accuracy","evidence":{"prefer_contexts":["bfcl_fc"]}}],"limit":50}`
	table, legend := renderQuery(t, r, svc, body)

	var resp query.Response
	json.Unmarshal([]byte(r.must("query", "--request", writeTemp(t, body), "--json")), &resp)
	if len(resp.Criteria) != 1 || resp.Criteria[0].ValueInOrderBy == nil || *resp.Criteria[0].ValueInOrderBy != 0 {
		t.Fatalf("expected the compact criterion to be reported through order_by[0]: %+v", resp.Criteria)
	}
	labels := columnLabels(t, legend)
	if len(labels) != 1 || labels[0] != "BFCL_OVERALL_ACCURACY" {
		t.Fatalf("identical terms share one plainly labelled column, got %q", labels)
	}
	inOrder(t, row(t, table, "claude-opus-5-medium"), "claude-opus-5-medium", "77.47*~")
}

func TestSameMetricCriterionWithItsOwnPolicyKeepsItsValue(t *testing.T) {
	r, svc := refreshedRunner(t)
	body := `{"criteria":[{"metric":"bfcl_overall_accuracy","op":"gte","value":60,"missing":"reject","evidence":{"contexts":["bfcl_prompt"]}}],
		"order_by":[{"metric":"bfcl_overall_accuracy","evidence":{"contexts":["bfcl_fc"]}}]}`
	table, legend := renderQuery(t, r, svc, body)

	var resp query.Response
	json.Unmarshal([]byte(r.must("query", "--request", writeTemp(t, body), "--json")), &resp)
	if len(resp.Criteria) != 1 || resp.Criteria[0].ValueInOrderBy != nil {
		t.Fatalf("a criterion with a different policy must carry its own value: %+v", resp.Criteria)
	}
	labels := columnLabels(t, legend)
	if len(labels) != 2 ||
		labels[0] != "BFCL_OVERALL_ACCURACY[contexts=bfcl_prompt]" ||
		labels[1] != "BFCL_OVERALL_ACCURACY[contexts=bfcl_fc]" {
		t.Fatalf("each policy keeps its own labelled column, got %q", labels)
	}
	inOrder(t, row(t, table, "claude-opus-5-medium"), "claude-opus-5-medium", "70*~", "77.47*~")
}

func TestSingleOccurrenceColumnsAreUnchanged(t *testing.T) {
	r, svc := refreshedRunner(t)
	table, legend := renderQuery(t, r, svc, `{"criteria":[{"metric":"swe_bench_verified","op":"gte","value":77,"missing":"reject"}],
		"order_by":[{"metric":"devin_output_price_usd_per_mtok"},{"metric":"swe_bench_verified"}]}`)
	labels := columnLabels(t, legend)
	// A metric appearing once is labelled by its name alone: no policy
	// qualifier is needed to tell it from anything.
	for _, l := range labels {
		if strings.Contains(l, "[") {
			t.Fatalf("single-occurrence metrics keep their plain labels, got %q", labels)
		}
	}
	if len(labels) != 2 || labels[0] != "SWE_BENCH_VERIFIED" || labels[1] != "DEVIN_OUTPUT_PRICE_USD_PER_MTOK" {
		t.Fatalf("columns in order, got %q", labels)
	}
	inOrder(t, header(table), "RANK", "MODEL", "UID", "[1]", "[2]")
	inOrder(t, row(t, table, "claude-opus-5-medium"), "claude-opus-5-medium", "78.1*", "25")
}

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
