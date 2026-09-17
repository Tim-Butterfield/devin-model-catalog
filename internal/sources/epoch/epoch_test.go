package epoch_test

import (
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/identity"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources/epoch"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/testfixtures"
)

func find(res sources.Result, metric, name, ctx string) *sources.Observation {
	for i, o := range res.Observations {
		if o.Metric == metric && o.SourceModelName == name && o.EvaluationContext == ctx {
			return &res.Observations[i]
		}
	}
	return nil
}

func TestParseAccountsForEveryRow(t *testing.T) {
	res, err := epoch.Parse(testfixtures.DefaultEpochArchive())
	if err != nil {
		t.Fatal(err)
	}
	if res.SourceRowCount != 16 || res.AcceptedRows != 13 || len(res.Rejected) != 3 {
		t.Fatalf("rows %d accepted %d rejected %d", res.SourceRowCount, res.AcceptedRows, len(res.Rejected))
	}
	// Only rows that carry nothing usable are rejected, and each says why.
	for _, r := range res.Rejected {
		if r.Reason == "" || r.SourceRowRef == "" {
			t.Errorf("a rejection needs an exact reason and a row reference: %+v", r)
		}
		if strings.Contains(r.Reason, "not recognised") || strings.Contains(r.Reason, "harness") && !strings.Contains(r.Reason, "no harness") {
			t.Errorf("a row must never be rejected for naming an unfamiliar harness: %+v", r)
		}
	}
}

// A harness this build does not curate is evidence in its own right: it gets a
// distinct context, keeps its published name, and is never folded into a
// neighbouring harness.
func TestUnknownHarnessBecomesItsOwnContext(t *testing.T) {
	res, err := epoch.Parse(testfixtures.DefaultEpochArchive())
	if err != nil {
		t.Fatal(err)
	}
	if o := find(res, "deepswe", "kimi-k3_high", "harness_somenewagent"); o == nil {
		t.Fatal("an unfamiliar harness must still produce an observation, in its own context")
	}
	if o := find(res, "terminal_bench", "claude-opus-5_unknown", "harness_droid"); o == nil {
		t.Fatal("Droid must produce an observation in context harness_droid")
	}
	// The same model under Claude Code keeps its own separate value.
	if o := find(res, "terminal_bench", "claude-opus-5_unknown", "claude_code"); o == nil {
		t.Fatal("the curated harness observation must be unaffected")
	}

	byCode := map[string]sources.EvaluationContext{}
	for _, c := range res.Contexts {
		byCode[c.Code] = c
	}
	droid, ok := byCode["harness_droid"]
	if !ok {
		t.Fatal("the result must report the context it derived, so the refresh can record it")
	}
	if droid.Name != "Droid" || droid.Kind != sources.ContextExternalHarness {
		t.Errorf("derived context = %+v, want the published name and external_harness", droid)
	}
	// Every context an observation cites is reported, so nothing is emitted
	// against a context the refresh has never heard of.
	for _, o := range res.Observations {
		if _, ok := byCode[o.EvaluationContext]; !ok {
			t.Errorf("observation cites context %q that the result does not report", o.EvaluationContext)
		}
	}
}

// A row whose versioned identifier is blank but whose Name is not is real
// evidence; only a row with no identity at all is rejected.
func TestNameColumnIsTheFallbackIdentity(t *testing.T) {
	res, err := epoch.Parse(testfixtures.DefaultEpochArchive())
	if err != nil {
		t.Fatal(err)
	}
	o := find(res, "deepswe", "Zephyr 2", "mini_swe_agent")
	if o == nil {
		t.Fatal("a blank 'Model version' with a Name must be read from the Name")
	}
	if o.BaseKey != "zephyr 2" || o.Effort != identity.EffortMedium {
		t.Errorf("fallback identity = %+v", o)
	}
	var refs []string
	for _, r := range res.Rejected {
		refs = append(refs, r.SourceRowRef)
	}
	if !slices.Contains(refs, "terminalbench_external.csv:5") {
		t.Fatalf("a row with neither an identifier nor a name must be rejected: %v", refs)
	}
}

func TestObservationsCarryEffortHarnessAndScale(t *testing.T) {
	res, err := epoch.Parse(testfixtures.DefaultEpochArchive())
	if err != nil {
		t.Fatal(err)
	}
	o := find(res, "swe_bench_verified", "claude-opus-5_medium", epoch.OwnEvaluationContext)
	if o == nil || o.Effort != identity.EffortMedium || o.BaseKey != "claude opus 5" || math.Abs(*o.Value.Number-78.1) > 1e-9 {
		t.Fatalf("swe-bench observation = %+v", o)
	}
	if o := find(res, "swe_bench_verified", "claude-opus-5", epoch.OwnEvaluationContext); o == nil || o.Effort != identity.EffortUnspecified {
		t.Fatalf("an identifier with no suffix has no effort: %+v", o)
	}
	// The explicit effort column wins over the identifier suffix.
	if o := find(res, "frontiercode", "claude-opus-5_max", "claude_code"); o == nil || o.Effort != identity.EffortMedium {
		t.Fatalf("frontiercode observation = %+v", o)
	}
	if o := find(res, "frontiercode", "swe-1.7", "devin"); o == nil {
		t.Fatal("the devin harness must map to the devin context")
	}
	// Harness names are matched after trimming and case-folding.
	if o := find(res, "terminal_bench", "gpt-5.6-sol_unknown", "codex"); o == nil || o.Effort != identity.EffortUnspecified || o.BaseKey != "gpt 5.6 sol" {
		t.Fatalf("terminal-bench observation = %+v", o)
	}
}

func TestStructuralChangesFailTheSource(t *testing.T) {
	missingFile := testfixtures.DefaultEpochFiles()
	delete(missingFile, "deepswe_external.csv")
	if _, err := epoch.Parse(testfixtures.EpochArchive(missingFile)); err == nil || !strings.Contains(err.Error(), "deepswe") {
		t.Fatalf("a missing file must fail the source, got %v", err)
	}

	missingColumn := testfixtures.DefaultEpochFiles()
	missingColumn["frontiercode_external.csv"] = "Model version,Score,Harness\nx,0.5,codex\n"
	_, err := epoch.Parse(testfixtures.EpochArchive(missingColumn))
	if err == nil {
		t.Fatal("a missing score column must fail the source")
	}
	// A shape this collector cannot read is reported as what it is — the
	// upstream format having moved — rather than as a pile of suspect rows.
	for _, want := range []string{"format appears to have changed", "last successful refresh is unchanged", "devmodels update"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("a format change must say %q; got %v", want, err)
		}
	}

	empty := testfixtures.DefaultEpochFiles()
	empty["swe_bench_verified.csv"] = "Model version,mean_score\n"
	if _, err := epoch.Parse(testfixtures.EpochArchive(empty)); err == nil {
		t.Fatal("an empty file must fail the source")
	}

	if _, err := epoch.Parse([]byte("not a zip")); err == nil {
		t.Fatal("a corrupt archive must fail")
	}
}

func TestOutOfRangeScoreIsNotRescaled(t *testing.T) {
	files := testfixtures.DefaultEpochFiles()
	files["swe_bench_verified.csv"] = "Model version,mean_score\nmodel-a,78.1\nmodel-b,0.5\n"
	res, err := epoch.Parse(testfixtures.EpochArchive(files))
	if err != nil {
		t.Fatal(err)
	}
	if find(res, "swe_bench_verified", "model-a", epoch.OwnEvaluationContext) != nil {
		t.Fatal("a score outside 0-1 must not be guessed into a percentage")
	}
	if find(res, "swe_bench_verified", "model-b", epoch.OwnEvaluationContext) == nil {
		t.Fatal("a valid fraction must be accepted")
	}
}
