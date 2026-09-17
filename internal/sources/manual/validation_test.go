package manual_test

import (
	"strings"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/metrics"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources/manual"
)

// An import is the one place a user's own document becomes stored evidence, so
// every way of getting it wrong must be refused with a message that says which
// part is wrong. A partial import would silently drop what the user asked to
// record, which is why nothing here is a warning.
func TestParseRejectsMalformedDocuments(t *testing.T) {
	cases := []struct {
		name     string
		document string
		want     string
	}{
		{
			"a source code outside the manual namespace",
			`{"source": {"code": "epoch", "name": "n", "access_basis": "b"},
			  "observations": [{"metric": "m", "model": "A", "evaluation_context": "c", "value": 1}]}`,
			"source.code",
		},
		{
			"a source code with characters the pattern excludes",
			`{"source": {"code": "manual_Context Windows", "name": "n", "access_basis": "b"},
			  "observations": [{"metric": "m", "model": "A", "evaluation_context": "c", "value": 1}]}`,
			"source.code",
		},
		{
			"no source name",
			`{"source": {"code": "manual_x", "name": "   ", "access_basis": "b"},
			  "observations": [{"metric": "m", "model": "A", "evaluation_context": "c", "value": 1}]}`,
			"source.name is required",
		},
		{
			"no access basis",
			`{"source": {"code": "manual_x", "name": "n", "access_basis": ""},
			  "observations": [{"metric": "m", "model": "A", "evaluation_context": "c", "value": 1}]}`,
			"source.access_basis is required",
		},
		{
			"no observations",
			`{"source": {"code": "manual_x", "name": "n", "access_basis": "b"}, "observations": []}`,
			"no observations",
		},
		{
			"a misspelled field, rather than a silently ignored one",
			`{"source": {"code": "manual_x", "name": "n", "access_basis": "b", "licence": "MIT"},
			  "observations": [{"metric": "m", "model": "A", "evaluation_context": "c", "value": 1}]}`,
			"not a valid import document",
		},
		{
			"a metric definition that is not internally consistent",
			`{"source": {"code": "manual_x", "name": "n", "access_basis": "b"},
			  "metrics": [{"key": "Context Window", "display_name": "d", "description": "e",
			               "value_kind": "integer", "direction": "higher_is_better"}],
			  "observations": [{"metric": "m", "model": "A", "evaluation_context": "c", "value": 1}]}`,
			"metric key",
		},
		{
			"a text metric claiming a direction",
			`{"source": {"code": "manual_x", "name": "n", "access_basis": "b"},
			  "metrics": [{"key": "note", "display_name": "d", "description": "e",
			               "value_kind": "text", "direction": "higher_is_better"}],
			  "observations": [{"metric": "note", "model": "A", "evaluation_context": "c", "value": "x"}]}`,
			"cannot have direction",
		},
		{
			"an evaluation context kind this project does not define",
			`{"source": {"code": "manual_x", "name": "n", "access_basis": "b"},
			  "evaluation_contexts": [{"code": "c", "name": "C", "kind": "vibes"}],
			  "observations": [{"metric": "m", "model": "A", "evaluation_context": "c", "value": 1}]}`,
			"kind must be devin, external_harness or direct",
		},
		{
			// published is a kind the project owns for the catalog's own
			// prices. An import declaring it would let user-supplied values
			// present themselves as non-proxy evidence.
			"an evaluation context claiming the published kind",
			`{"source": {"code": "manual_x", "name": "n", "access_basis": "b"},
			  "evaluation_contexts": [{"code": "c", "name": "C", "kind": "published"}],
			  "observations": [{"metric": "m", "model": "A", "evaluation_context": "c", "value": 1}]}`,
			"kind must be devin, external_harness or direct",
		},
		{
			"an evaluation context with no name",
			`{"source": {"code": "manual_x", "name": "n", "access_basis": "b"},
			  "evaluation_contexts": [{"code": "c", "name": "", "kind": "direct"}],
			  "observations": [{"metric": "m", "model": "A", "evaluation_context": "c", "value": 1}]}`,
			"lowercase code and a name",
		},
		{
			"not JSON at all",
			`this is not a document`,
			"not a valid import document",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := manual.Parse([]byte(tc.document))
			if err == nil {
				t.Fatalf("must be rejected, but parsed")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error must say %q, got %v", tc.want, err)
			}
		})
	}
}

func TestParseAcceptsACompleteDocument(t *testing.T) {
	imp, err := manual.Parse([]byte(`{
	  "source": {"code": "manual_context_windows", "name": "Vendor docs",
	             "access_basis": "Transcribed from each vendor's public documentation",
	             "homepage": "https://example.invalid/", "license": "none", "attribution": "vendors"},
	  "metrics": [{"key": "context_window_tokens", "display_name": "Context window",
	               "description": "Documented maximum input context.", "value_kind": "integer",
	               "unit": "tokens", "direction": "higher_is_better"}],
	  "evaluation_contexts": [{"code": "vendor_documentation", "name": "Vendor documentation", "kind": "direct"}],
	  "observations": [{"metric": "context_window_tokens", "model": "Claude Opus 5",
	                    "evaluation_context": "vendor_documentation", "value": 200000}]
	}`))
	if err != nil {
		t.Fatalf("a complete document: %v", err)
	}
	if imp.Info.Kind != "manual" || imp.Info.Retrieval != "manual_import" {
		t.Errorf("an import must declare itself manual, got kind %q retrieval %q", imp.Info.Kind, imp.Info.Retrieval)
	}
	if len(imp.Metrics) != 1 || imp.Metrics[0].Scope != metrics.ScopeEvidence {
		t.Errorf("an imported metric is always evidence-scope, got %+v", imp.Metrics)
	}
	if imp.Metrics[0].DefinedBy != "manual_context_windows" {
		t.Errorf("an imported metric is defined by its own source, got %q", imp.Metrics[0].DefinedBy)
	}
}

// contextWindow is an evidence metric an import may legitimately define.
func contextWindow(kind metrics.ValueKind) map[string]metrics.Definition {
	return map[string]metrics.Definition{"context_window_tokens": {
		Key: "context_window_tokens", DisplayName: "Context window", Description: "d",
		ValueKind: kind, Direction: metrics.HigherIsBetter, Scope: metrics.ScopeEvidence,
		DefinedBy: "manual_x", MissingPossible: true, Status: metrics.StatusCurrent,
	}}
}

func parse(t *testing.T, observations string) *manual.Import {
	t.Helper()
	imp, err := manual.Parse([]byte(`{"source": {"code": "manual_x", "name": "n", "access_basis": "b"},
	  "observations": [` + observations + `]}`))
	if err != nil {
		t.Fatalf("building the import: %v", err)
	}
	return imp
}

func TestObservationsRejectRowsItCannotRepresent(t *testing.T) {
	known := map[string]bool{"vendor_documentation": true}

	cases := []struct {
		name        string
		observation string
		kind        metrics.ValueKind
		want        string
	}{
		{
			"a metric no definition covers",
			`{"metric": "invented", "model": "A", "evaluation_context": "vendor_documentation", "value": 1}`,
			metrics.KindInteger, `unknown metric "invented"`,
		},
		{
			"an evaluation context no declaration covers",
			`{"metric": "context_window_tokens", "model": "A", "evaluation_context": "nowhere", "value": 1}`,
			metrics.KindInteger, `unknown evaluation_context "nowhere"`,
		},
		{
			"no model",
			`{"metric": "context_window_tokens", "model": "  ", "evaluation_context": "vendor_documentation", "value": 1}`,
			metrics.KindInteger, "model is required",
		},
		{
			"a fractional value for a whole-number metric",
			`{"metric": "context_window_tokens", "model": "A", "evaluation_context": "vendor_documentation", "value": 1.5}`,
			metrics.KindInteger, "whole number",
		},
		{
			// 1e300 is finite but far outside int64. Converting it would be
			// implementation-defined, so the range is checked first.
			"a whole number too large for an integer metric",
			`{"metric": "context_window_tokens", "model": "A", "evaluation_context": "vendor_documentation", "value": 1e300}`,
			metrics.KindInteger, "whole number",
		},
		{
			"a string where a number belongs",
			`{"metric": "context_window_tokens", "model": "A", "evaluation_context": "vendor_documentation", "value": "200000"}`,
			metrics.KindInteger, "numeric value",
		},
		{
			"a number where a boolean belongs",
			`{"metric": "context_window_tokens", "model": "A", "evaluation_context": "vendor_documentation", "value": 1}`,
			metrics.KindBoolean, "true or false",
		},
		{
			"a number where text belongs",
			`{"metric": "context_window_tokens", "model": "A", "evaluation_context": "vendor_documentation", "value": 1}`,
			metrics.KindText, "string value",
		},
		{
			"an effort level this version does not know",
			`{"metric": "context_window_tokens", "model": "A", "effort": "turbo", "evaluation_context": "vendor_documentation", "value": 1}`,
			metrics.KindInteger, `effort "turbo" is not a known level`,
		},
		{
			"a serving variant that is neither fast nor standard",
			`{"metric": "context_window_tokens", "model": "A", "serving_variant": "slow", "evaluation_context": "vendor_documentation", "value": 1}`,
			metrics.KindInteger, "serving_variant must be fast or standard",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			imp := parse(t, tc.observation)
			res, err := imp.Observations(contextWindow(tc.kind), known)
			if err == nil {
				t.Fatalf("must be rejected, but produced %d observations", len(res.Observations))
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error must say %q, got %v", tc.want, err)
			}
			if !strings.Contains(err.Error(), "observations[0]") {
				t.Errorf("error must locate the row, got %v", err)
			}
			if len(res.Observations) != 0 {
				t.Errorf("a rejected import must yield nothing, got %d observations", len(res.Observations))
			}
		})
	}
}

// A catalog-scope metric is published by the Devin dataset for a specific
// model. Letting an import supply one would let a user overwrite a published
// price with a value of their own.
func TestObservationsRefuseCatalogScopeMetrics(t *testing.T) {
	defs := map[string]metrics.Definition{"devin_output_price_usd_per_mtok": {
		Key: "devin_output_price_usd_per_mtok", DisplayName: "Output price", Description: "d",
		ValueKind: metrics.KindNumber, Direction: metrics.LowerIsBetter, Scope: metrics.ScopeCatalog,
		DefinedBy: "devin", MissingPossible: true, Status: metrics.StatusCurrent,
	}}
	imp := parse(t, `{"metric": "devin_output_price_usd_per_mtok", "model": "A",
	                  "evaluation_context": "vendor_documentation", "value": 1}`)

	_, err := imp.Observations(defs, map[string]bool{"vendor_documentation": true})
	if err == nil || !strings.Contains(err.Error(), "cannot be imported") {
		t.Fatalf("a catalog-scope metric must be refused, got %v", err)
	}
}

// One bad row fails the whole file: a partial import would silently drop part
// of what the user asked to record.
func TestObservationsAreAllOrNothing(t *testing.T) {
	imp := parse(t, `{"metric": "context_window_tokens", "model": "A", "evaluation_context": "vendor_documentation", "value": 1},
	                 {"metric": "context_window_tokens", "model": "B", "evaluation_context": "vendor_documentation", "value": 1.5},
	                 {"metric": "context_window_tokens", "model": "C", "evaluation_context": "vendor_documentation", "value": 3}`)

	res, err := imp.Observations(contextWindow(metrics.KindInteger), map[string]bool{"vendor_documentation": true})
	if err == nil {
		t.Fatal("the second row is invalid, so the import must fail")
	}
	if !strings.Contains(err.Error(), "observations[1]") {
		t.Errorf("the error must name the offending row, got %v", err)
	}
	if len(res.Observations) != 0 || res.AcceptedRows != 0 {
		t.Errorf("a failed import must yield no partial result, got %+v", res)
	}
}

// The name carries identity; an explicit field overrides what the name implies.
func TestObservationsReadIdentityFromNameAndOverrides(t *testing.T) {
	imp := parse(t, `{"metric": "context_window_tokens", "model": "Claude Opus 5 High Fast",
	                  "evaluation_context": "vendor_documentation", "value": 1},
	                 {"metric": "context_window_tokens", "model": "Claude Opus 5 High Fast",
	                  "serving_variant": "standard", "evaluation_context": "vendor_documentation", "value": 2,
	                  "note": "standard serving"}`)

	res, err := imp.Observations(contextWindow(metrics.KindInteger), map[string]bool{"vendor_documentation": true})
	if err != nil {
		t.Fatalf("a valid import: %v", err)
	}
	if res.AcceptedRows != 2 || len(res.Observations) != 2 {
		t.Fatalf("both rows are valid, got %d accepted", res.AcceptedRows)
	}
	if err := res.AccountingError("manual_x"); err != nil {
		t.Errorf("every row must be accounted for: %v", err)
	}
	if res.Observations[0].Effort != res.Observations[1].Effort {
		t.Errorf("both rows name the same effort, got %v and %v", res.Observations[0].Effort, res.Observations[1].Effort)
	}
	if res.Observations[0].Serving == res.Observations[1].Serving {
		t.Errorf("serving_variant must override what the name implies, both are %v", res.Observations[0].Serving)
	}
	if res.Observations[1].Details["note"] != "standard serving" {
		t.Errorf("a note must be carried, got %v", res.Observations[1].Details)
	}
	if res.Observations[0].SourceModelName != "Claude Opus 5 High Fast" {
		t.Errorf("the published name must be kept verbatim, got %q", res.Observations[0].SourceModelName)
	}
}

// A stored NaN would compare false against every criterion and sort
// unpredictably, so no import may produce one. JSON is the first line of
// defence: the non-finite literals are not JSON at all, and a decimal too large
// for a float64 is refused by the decoder rather than rounded to infinity.
// convert refuses a non-finite value as well, for a caller that does not arrive
// through this decoder.
func TestNonFiniteValuesCannotBeImported(t *testing.T) {
	for _, literal := range []string{"NaN", "Infinity", "-Infinity", "1e400", "-1e400"} {
		document := `{"source": {"code": "manual_x", "name": "n", "access_basis": "b"},
		  "observations": [{"metric": "m", "model": "A", "evaluation_context": "c", "value": ` + literal + `}]}`
		if _, err := manual.Parse([]byte(document)); err == nil {
			t.Errorf("%s must not decode as a value", literal)
		}
	}
}
