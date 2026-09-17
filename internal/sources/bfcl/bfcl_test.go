package bfcl_test

import (
	"strings"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources/bfcl"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/testfixtures"
)

func TestParse(t *testing.T) {
	res, err := bfcl.Parse([]byte(testfixtures.BFCLCSV))
	if err != nil {
		t.Fatal(err)
	}
	// Only the unreadable accuracy is a rejection; an unfamiliar mode and an
	// unstated mode are both evidence.
	if res.SourceRowCount != 6 || res.AcceptedRows != 5 || len(res.Rejected) != 1 {
		t.Fatalf("rows %d accepted %d rejected %d", res.SourceRowCount, res.AcceptedRows, len(res.Rejected))
	}

	contexts := map[string]string{}
	for _, o := range res.Observations {
		contexts[o.SourceModelName] = o.EvaluationContext + " " + o.BaseKey
	}
	want := map[string]string{
		"Claude-Opus-5 (FC)":          "bfcl_fc claude opus 5",
		"Claude-Opus-5 (Prompt)":      "bfcl_prompt claude opus 5",
		"GPT-5.6-Sol-2026-07-09 (FC)": "bfcl_fc gpt 5.6 sol",
		// A mode this build does not curate keeps its own context and never
		// borrows bfcl_fc or bfcl_prompt.
		"Kimi-K3 (Weird Mode)": "bfcl_mode_weird_mode kimi k3",
		// A name with no mode qualifier says only that the mode is unstated.
		"BitAgent-8B": bfcl.UnstatedModeContext + " bitagent 8b",
	}
	for name, w := range want {
		if contexts[name] != w {
			t.Errorf("%s = %q, want %q", name, contexts[name], w)
		}
	}

	byCode := map[string]sources.EvaluationContext{}
	for _, c := range res.Contexts {
		byCode[c.Code] = c
	}
	weird, ok := byCode["bfcl_mode_weird_mode"]
	if !ok {
		t.Fatal("the result must report the context it derived")
	}
	if weird.Name != "BFCL Weird Mode" || weird.Kind != sources.ContextDirect {
		t.Errorf("derived mode context = %+v", weird)
	}
	for _, o := range res.Observations {
		if _, ok := byCode[o.EvaluationContext]; !ok {
			t.Errorf("observation cites context %q that the result does not report", o.EvaluationContext)
		}
	}
}

// Mode qualifiers that differ only in case, spacing or separators are one
// context; anything else stays its own.
func TestModeQualifiersFoldOnSpelling(t *testing.T) {
	res, err := bfcl.Parse([]byte("Rank,Overall Acc,Model\n" +
		"1,50%,A (Prompt + Thinking)\n2,51%,B (prompt+thinking)\n3,52%,C (PROMPT THINKING)\n4,53%,D (Prompt Thinking Extra)\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, o := range res.Observations {
		got[o.SourceModelName] = o.EvaluationContext
	}
	for _, name := range []string{"A (Prompt + Thinking)", "B (prompt+thinking)", "C (PROMPT THINKING)"} {
		if got[name] != "bfcl_prompt_thinking" {
			t.Errorf("%s = %q, want bfcl_prompt_thinking", name, got[name])
		}
	}
	if got["D (Prompt Thinking Extra)"] != "bfcl_mode_prompt_thinking_extra" {
		t.Errorf("an extra word is a different mode: %q", got["D (Prompt Thinking Extra)"])
	}
}

func TestNonNumericAccuracyIsRejected(t *testing.T) {
	res, err := bfcl.Parse([]byte("Rank,Overall Acc,Model\n1,NaN%,X (FC)\n2,Inf,Y (FC)\n3,50%,Z (FC)\n"))
	if err != nil {
		t.Fatal(err)
	}
	if res.AcceptedRows != 1 || len(res.Rejected) != 2 || res.Observations[0].SourceModelName != "Z (FC)" {
		t.Fatalf("NaN and Inf are not percentages: accepted %d rejected %+v", res.AcceptedRows, res.Rejected)
	}
}

func TestReshapedHeaderFails(t *testing.T) {
	_, err := bfcl.Parse([]byte("Rank,Accuracy Score,Name\n1,50%,X (FC)\n"))
	if err == nil {
		t.Fatal("columns must be located by name, never by position")
	}
	for _, want := range []string{"format appears to have changed", "devmodels update"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("a format change must say %q; got %v", want, err)
		}
	}
	if _, err := bfcl.Parse([]byte("Rank,Overall Acc,Model\n")); err == nil {
		t.Fatal("a file with no rows must fail")
	}
}
