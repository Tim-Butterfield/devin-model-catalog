package cli_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/cli"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/query"
)

func decodeResponse(t *testing.T, out string) query.Response {
	t.Helper()
	var resp query.Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decoding query output: %v\n%s", err, out)
	}
	return resp
}

// A benchmark context restriction must not make published prices missing.
func TestContextFlagsApplyOnlyToBenchmarkMetrics(t *testing.T) {
	r, _ := refreshedRunner(t)
	resp := decodeResponse(t, r.must("query", "--context", "epoch_ai_eval",
		"--where", "swe_bench_verified>=77", "--where", "devin_output_price_usd_per_mtok<=30",
		"--order", "devin_output_price_usd_per_mtok", "--json"))
	if resp.Eligible != 1 || resp.Results[0].Model.UID != "claude-opus-5-medium" {
		t.Fatalf("the price criterion must still see prices: eligible %d exclusions %+v", resp.Eligible, resp.Exclusions)
	}
	if !slices.Equal(resp.Criteria[0].Evidence.Contexts, []string{"epoch_ai_eval"}) || resp.Criteria[1].Evidence.Contexts != nil || resp.OrderBy[0].Evidence.Contexts != nil {
		t.Fatalf("contexts applied to the wrong terms: %+v %+v", resp.Criteria, resp.OrderBy)
	}
}

func TestContextFlagsDoNotWidenARequestsOwnContexts(t *testing.T) {
	r, _ := refreshedRunner(t)
	path := writeTemp(t, `{"criteria":[{"metric":"bfcl_overall_accuracy","op":"gte","value":60,"missing":"reject","evidence":{"contexts":["bfcl_prompt"]}}],
		"order_by":[{"metric":"bfcl_overall_accuracy"}]}`)
	resp := decodeResponse(t, r.must("query", "--request", path, "--context", "bfcl_fc", "--json"))
	if !slices.Equal(resp.Criteria[0].Evidence.Contexts, []string{"bfcl_prompt"}) || !slices.Equal(resp.OrderBy[0].Evidence.Contexts, []string{"bfcl_fc"}) {
		t.Fatalf("a term's own contexts must be kept and the flag applied only elsewhere: %+v %+v", resp.Criteria, resp.OrderBy)
	}
}

func TestMalformedRequestFilesAreUsageErrors(t *testing.T) {
	r, _ := refreshedRunner(t)
	for name, args := range map[string][]string{
		"trailing data": {"query", "--request", writeTemp(t, `{"limit":5} {"limit":6}`)},
		"missing file":  {"query", "--request", writeTemp(t, "{}") + ".absent"},
	} {
		if code, _, _ := r.run(args...); code != cli.ExitUsage {
			t.Errorf("%s: exit %d, want %d", name, code, cli.ExitUsage)
		}
	}
}
