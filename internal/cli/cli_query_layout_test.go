package cli_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/cli"
)

// A query prints its results one of two ways, and there is one rule for
// choosing: the table for as long as its columns still fit the design width,
// and a stacked block per model once they do not.
//
// These tests hold both ends of that rule. A table that gave way early would
// waste the width; a table that gave way late would overflow it, which is what
// used to happen — seven ordering terms produced 86-column rows. So the switch
// is checked in both directions, and every layout the suite produces is
// measured, because a fallback that overflows is no fallback at all.

// tableColumnLimit is how many value columns the table can still hold. The
// rank column takes 6, and the label, uid and value columns cannot usefully go
// below 12, 12 and 8: 6 + 12 + 12 + 8n <= 80 holds up to n = 6.
//
// It is worked out here rather than read from the implementation, so that a
// floor moved by accident fails this suite instead of moving it too.
const tableColumnLimit = 6

// queryTerms are added to a request one at a time. Each names a different
// metric, so each one adds a display column. Every direction is explicit,
// because a metric with no better direction requires one.
var queryTerms = []string{
	"swe_bench_verified:desc",
	"bfcl_overall_accuracy:desc",
	"deepswe:desc",
	"terminal_bench:desc",
	"frontiercode:desc",
	"devin_input_price_usd_per_mtok:asc",
	"devin_output_price_usd_per_mtok:asc",
	"devin_cache_read_price_usd_per_mtok:asc",
	"devin_cache_write_price_usd_per_mtok:asc",
	"devin_long_context_input_price_usd_per_mtok:asc",
	"devin_long_context_output_price_usd_per_mtok:asc",
	"devin_long_context_threshold_tokens:desc",
}

func queryWithTerms(n int) []string {
	args := []string{"query", "--limit", "5"}
	for _, term := range queryTerms[:n] {
		args = append(args, "--order", term)
	}
	return args
}

const (
	stackedUID   = "    uid: "
	stackedValue = "    ["
)

// tabulated reports which layout the output used. The table heads its rows;
// the stacked form has no header, only blocks.
func tabulated(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "RANK") {
			return true
		}
	}
	return false
}

// legendEntries returns the numbered term labels printed under the results.
// They are the only lines that start a bracket in the first column: a stacked
// value line is indented under its model.
func legendEntries(out string) []string {
	var entries []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "[") {
			continue
		}
		if i := strings.Index(line, "] "); i > 0 {
			entries = append(entries, line[i+2:])
		}
	}
	return entries
}

// tableLines returns the header and rows of the table, and nothing else: the
// block runs from the header to the blank line after the last row.
func tableLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "RANK") {
			lines = append(lines, line)
			continue
		}
		if len(lines) == 0 {
			continue
		}
		if strings.TrimSpace(line) == "" {
			break
		}
		lines = append(lines, line)
	}
	return lines
}

type stackedBlock struct {
	rank   int
	label  string
	uid    string
	values []string
}

// stackedBlocks reads the stacked layout back into the facts it was printed
// from: a rank and a label, the uid beneath them, then one numbered value per
// term. A block is recognised by its uid line, so nothing else in the output
// that happens to begin with a number can be mistaken for one.
func stackedBlocks(out string) []stackedBlock {
	lines := strings.Split(out, "\n")
	var blocks []stackedBlock
	for i := 0; i+1 < len(lines); i++ {
		if !strings.HasPrefix(lines[i+1], stackedUID) {
			continue
		}
		head := strings.Fields(lines[i])
		if len(head) == 0 {
			continue
		}
		rank, err := strconv.Atoi(head[0])
		if err != nil {
			continue
		}
		b := stackedBlock{
			rank:  rank,
			label: strings.TrimSpace(strings.TrimPrefix(lines[i], head[0])),
			uid:   strings.TrimPrefix(lines[i+1], stackedUID),
		}
		for j := i + 2; j < len(lines) && strings.HasPrefix(lines[j], stackedValue); j++ {
			if k := strings.Index(lines[j], "] "); k > 0 {
				b.values = append(b.values, lines[j][k+2:])
			}
		}
		blocks = append(blocks, b)
	}
	return blocks
}

// queryJSON is the part of the machine-readable response these tests check the
// printed layout against.
type queryJSON struct {
	Results []struct {
		Rank  int `json:"rank"`
		Model struct {
			UID   string `json:"uid"`
			Label string `json:"label"`
		} `json:"model"`
		Order []struct {
			State string `json:"state"`
			Value any    `json:"value"`
		} `json:"order"`
	} `json:"results"`
}

func decodeQuery(t *testing.T, body string) queryJSON {
	t.Helper()
	var q queryJSON
	if err := json.Unmarshal([]byte(body), &q); err != nil {
		t.Fatalf("query --json is not readable: %v", err)
	}
	return q
}

// TestQueryKeepsTheTableWhileItsColumnsFit walks the whole range, from a
// request with no metric terms to one with more than any table could hold.
func TestQueryKeepsTheTableWhileItsColumnsFit(t *testing.T) {
	r, _, _ := refreshedCLI(t)
	for n := 0; n <= len(queryTerms); n++ {
		args := queryWithTerms(n)
		what := "devmodels query with " + strconv.Itoa(n) + " metric term(s)"
		_, out, errOut := r.run(args...)
		assertFits(t, what, out+errOut)

		if got, want := tabulated(out), n <= tableColumnLimit; got != want {
			t.Errorf("%s: tabulated = %v, want %v\n%s", what, got, want, out)
		}
		if got := legendEntries(out); len(got) != n {
			t.Errorf("%s: the legend names %d terms, want %d: %v", what, len(got), n, got)
		}
		if n > tableColumnLimit && len(stackedBlocks(out)) == 0 {
			t.Errorf("%s: no stacked results in\n%s", what, out)
		}
	}
}

// TestTheTableGivesWayOnlyWhenItHasTo is the other direction: at the last
// width it can hold, the table is still using essentially the whole of it.
func TestTheTableGivesWayOnlyWhenItHasTo(t *testing.T) {
	r, _, _ := refreshedCLI(t)
	_, out, _ := r.run(queryWithTerms(tableColumnLimit)...)
	if !tabulated(out) {
		t.Fatalf("%d metric terms should still be a table:\n%s", tableColumnLimit, out)
	}
	widest := 0
	for _, line := range tableLines(out) {
		if w := columns(line); w > widest {
			widest = w
		}
	}
	if widest < cli.Width-2 {
		t.Errorf("the table at %d metric terms is only %d columns wide, so it is giving way "+
			"earlier than the width requires:\n%s", tableColumnLimit, widest, out)
	}
}

// TestStackedResultsCarryEveryTermsValue checks the stacked layout against the
// machine-readable response for the same request: same models in the same
// order, and term k printed as [k] with the value the response gives it.
func TestStackedResultsCarryEveryTermsValue(t *testing.T) {
	r, _, _ := refreshedCLI(t)
	args := queryWithTerms(tableColumnLimit + 1)
	_, out, errOut := r.run(args...)
	assertFits(t, "devmodels query, stacked", out+errOut)
	if tabulated(out) {
		t.Fatalf("%d metric terms should no longer be a table:\n%s", tableColumnLimit+1, out)
	}

	want := decodeQuery(t, r.must(append(args, "--json")...))
	blocks := stackedBlocks(out)
	if len(blocks) != len(want.Results) {
		t.Fatalf("printed %d models, the response has %d:\n%s", len(blocks), len(want.Results), out)
	}
	for i, b := range blocks {
		res := want.Results[i]
		if b.rank != res.Rank || b.uid != res.Model.UID || b.label != res.Model.Label {
			t.Errorf("block %d is %d %q %q, the response has %d %q %q",
				i+1, b.rank, b.label, b.uid, res.Rank, res.Model.Label, res.Model.UID)
		}
		if len(b.values) != len(res.Order) {
			t.Fatalf("block %d prints %d values for %d terms", i+1, len(b.values), len(res.Order))
		}
		for k, got := range b.values {
			term := res.Order[k]
			switch term.State {
			case "missing":
				if got != "—" {
					t.Errorf("%s [%d] is %q for a missing value", b.uid, k+1, got)
				}
			case "ambiguous":
				if !strings.HasSuffix(got, "?") {
					t.Errorf("%s [%d] is %q for an ambiguous value", b.uid, k+1, got)
				}
			default:
				if number, ok := term.Value.(float64); ok {
					if prefix := strconv.FormatFloat(number, 'f', -1, 64); !strings.HasPrefix(got, prefix) {
						t.Errorf("%s [%d] is %q, the response has %s", b.uid, k+1, got, prefix)
					}
				}
			}
		}
	}
}

// TestStackedResultsKeepLabelsAndUIDsWhole is why the layout exists. In the
// table these were cut to twelve columns each; here every one is printed
// entire, which is what makes the uid worth copying.
func TestStackedResultsKeepLabelsAndUIDsWhole(t *testing.T) {
	r, _, _ := refreshedCLI(t)
	args := queryWithTerms(tableColumnLimit + 1)
	_, out, _ := r.run(args...)
	want := decodeQuery(t, r.must(append(args, "--json")...))

	widest := 0
	for _, res := range want.Results {
		if !strings.Contains(out, res.Model.UID) {
			t.Errorf("uid %q is not printed whole:\n%s", res.Model.UID, out)
		}
		if !strings.Contains(out, res.Model.Label) {
			t.Errorf("label %q is not printed whole:\n%s", res.Model.Label, out)
		}
		if w := columns(res.Model.UID); w > widest {
			widest = w
		}
	}
	// A fixture whose uids all fit a table column would make the check above
	// pass without testing anything.
	if widest <= 12 {
		t.Errorf("the widest uid in the fixtures is %d columns, which the table "+
			"would not have truncated; this case no longer tests a long uid", widest)
	}
	if strings.Contains(out, "…") {
		t.Errorf("the stacked layout truncated something it had room for:\n%s", out)
	}
}

// TestStackedResultsKeepTheEvidenceMarkers checks that a value's own marks
// survive the change of layout, along with the key that explains them.
func TestStackedResultsKeepTheEvidenceMarkers(t *testing.T) {
	r, _, _ := refreshedCLI(t)
	_, out, _ := r.run(queryWithTerms(tableColumnLimit + 1)...)
	var values []string
	for _, b := range stackedBlocks(out) {
		values = append(values, b.values...)
	}
	printed := strings.Join(values, " ")
	for mark, meaning := range map[string]string{
		"*": "proxy",
		"~": "inexact variant",
		"?": "ambiguous",
		"—": "missing",
	} {
		if !strings.Contains(printed, mark) {
			t.Errorf("no %s (%s) value in the stacked output; the fixtures used to "+
				"produce one, so either the layout or the fixtures changed:\n%s", mark, meaning, out)
		}
		if !strings.Contains(out, mark+" "+meaning) {
			t.Errorf("the key does not explain %s (%s):\n%s", mark, meaning, out)
		}
	}
}

// TestOneMetricUnderTwoPoliciesGetsTwoColumnsInBothLayouts covers the case
// where the number of display columns is not the number of metrics: the same
// metric read under two evidence policies resolves twice and is shown twice.
func TestOneMetricUnderTwoPoliciesGetsTwoColumnsInBothLayouts(t *testing.T) {
	r, _, _ := refreshedCLI(t)
	const twoPolicies = `{"metric":"bfcl_overall_accuracy","direction":"desc","evidence":{"contexts":["bfcl_fc"]}},
	                      {"metric":"bfcl_overall_accuracy","direction":"desc","evidence":{"contexts":["bfcl_prompt"]}}`

	for _, tc := range []struct {
		name      string
		body      string
		tabulated bool
		terms     int
	}{
		{
			name:      "two columns, still a table",
			body:      `{"limit":5,"order_by":[` + twoPolicies + `]}`,
			tabulated: true,
			terms:     2,
		},
		{
			name: "the same two inside a request the table cannot hold",
			body: `{"limit":5,"order_by":[` + twoPolicies + `,
				{"metric":"swe_bench_verified","direction":"desc"},
				{"metric":"deepswe","direction":"desc"},
				{"metric":"terminal_bench","direction":"desc"},
				{"metric":"frontiercode","direction":"desc"},
				{"metric":"devin_input_price_usd_per_mtok","direction":"asc"}]}`,
			tabulated: false,
			terms:     7,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, out, errOut := r.run("query", "--request", writeTemp(t, tc.body))
			assertFits(t, "devmodels query, "+tc.name, out+errOut)
			if got := tabulated(out); got != tc.tabulated {
				t.Errorf("tabulated = %v, want %v\n%s", got, tc.tabulated, out)
			}
			legend := legendEntries(out)
			if len(legend) != tc.terms {
				t.Fatalf("the legend names %d terms, want %d: %v", len(legend), tc.terms, legend)
			}
			// The two columns are the same metric, so each has to say which
			// policy it read, or the reader cannot tell them apart.
			if legend[0] == legend[1] {
				t.Errorf("both policies are labelled %q", legend[0])
			}
			for i, code := range []string{"bfcl_fc", "bfcl_prompt"} {
				if !strings.Contains(legend[i], code) {
					t.Errorf("legend[%d] = %q, which does not name %s", i, legend[i], code)
				}
			}
		})
	}
}

// TestCriteriaAndOrderingShareTheNumberingInBothLayouts checks the other way a
// request reaches many columns: criteria are numbered first, then the ordering
// terms, in the order they were given.
func TestCriteriaAndOrderingShareTheNumberingInBothLayouts(t *testing.T) {
	r, _, _ := refreshedCLI(t)
	args := []string{
		"query", "--limit", "5",
		"--where-or-missing", "swe_bench_verified>=1",
		"--where-or-missing", "bfcl_overall_accuracy>=1",
		"--where-or-missing", "deepswe>=1",
		"--where-or-missing", "terminal_bench>=1",
		"--order", "frontiercode:desc",
		"--order", "devin_input_price_usd_per_mtok:asc",
		"--order", "devin_output_price_usd_per_mtok:asc",
	}
	_, out, errOut := r.run(args...)
	assertFits(t, "devmodels query, criteria and ordering", out+errOut)
	if tabulated(out) {
		t.Fatalf("seven terms should no longer be a table:\n%s", out)
	}
	want := []string{
		"SWE_BENCH_VERIFIED", "BFCL_OVERALL_ACCURACY", "DEEPSWE", "TERMINAL_BENCH",
		"FRONTIERCODE", "DEVIN_INPUT_PRICE_USD_PER_MTOK", "DEVIN_OUTPUT_PRICE_USD_PER_MTOK",
	}
	got := legendEntries(out)
	if len(got) != len(want) {
		t.Fatalf("the legend names %d terms, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("legend[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, b := range stackedBlocks(out) {
		if len(b.values) != len(want) {
			t.Errorf("%s prints %d values for %d terms", b.uid, len(b.values), len(want))
		}
	}
}
