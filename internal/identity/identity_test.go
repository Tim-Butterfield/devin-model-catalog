package identity

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		label, base string
		effort      Effort
		serving     Serving
		ctx         string
	}{
		{"GPT-5.2 High Thinking Fast", "GPT-5.2", EffortHigh, ServingFast, ""},
		{"Claude Opus 4.8 High Fast", "Claude Opus 4.8", EffortHigh, ServingFast, ""},
		{"Claude Opus 4.6 Thinking 1M", "Claude Opus 4.6 Thinking", "", "", "1M"},
		{"GLM-5.2 No Thinking 1M", "GLM-5.2", EffortNone, "", "1M"},
		{"Gemini 3 Flash High", "Gemini 3 Flash", EffortHigh, "", ""},
		{"GPT-5.3-Codex X-High", "GPT-5.3-Codex", EffortXHigh, "", ""},
		{"Inkling None", "Inkling", EffortNone, "", ""},
		{"o3 High Reasoning", "o3", EffortHigh, "", ""},
		{"SWE-1.7 Lightning Max", "SWE-1.7 Lightning", EffortMax, "", ""},
		// Identity-bearing words stay.
		{"Grok Code Fast 1", "Grok Code Fast 1", "", "", ""},
		{"Fast Arena", "Fast Arena", "", "", ""},
		{"SWE-check", "SWE-check", "", "", ""},
		{"Max", "Max", "", "", ""},
		{"Claude Opus 4.5 Thinking", "Claude Opus 4.5 Thinking", "", "", ""},
		// Legacy-table spellings.
		{"Claude Opus 4.6 (Thinking)", "Claude Opus 4.6 Thinking", "", "", ""},
		{"Claude Fable 5.1 (Low Thinking)", "Claude Fable 5.1", EffortLow, "", ""},
		{"Claude Opus 5 Fast (High Thinking)", "Claude Opus 5", EffortHigh, ServingFast, ""},
		{"GPT-5.6 Sol (Extra High Reasoning) Fast", "GPT-5.6 Sol", EffortXHigh, ServingFast, ""},
		{"GPT-5.3-Codex (Extra High Reasoning Fast)", "GPT-5.3-Codex", EffortXHigh, ServingFast, ""},
		{"GPT-5.2 (No Reasoning Fast)", "GPT-5.2", EffortNone, ServingFast, ""},
		{"GLM-5.2 1M (No Thinking)", "GLM-5.2", EffortNone, "", "1M"},
		{"gpt-oss 120B (Medium)", "gpt-oss 120B", EffortMedium, "", ""},
		{"o3 (high reasoning)", "o3", EffortHigh, "", ""},
		{"Kimi K3 (Max Reasoning)", "Kimi K3", EffortMax, "", ""},
	}
	for _, c := range cases {
		v := Classify(c.label)
		if v.BaseName != c.base || v.Effort != c.effort || v.Serving != c.serving || v.ContextVariant != c.ctx {
			t.Errorf("Classify(%q) = base %q effort %q serving %q ctx %q; want %q %q %q %q",
				c.label, v.BaseName, v.Effort, v.Serving, v.ContextVariant, c.base, c.effort, c.serving, c.ctx)
		}
	}
}

func TestContextVariantIsIdentityBearing(t *testing.T) {
	if Classify("Claude Sonnet 4.6").BaseKey() == Classify("Claude Sonnet 4.6 1M").BaseKey() {
		t.Fatal("a 1M context variant must not share a base key with its standard sibling")
	}
	if Classify("Gemini 3 Flash").BaseKey() == Classify("Gemini 3 Pro").BaseKey() {
		t.Fatal("Flash and Pro are distinct models")
	}
	a, b := Classify("Claude Opus 5 High"), Classify("Claude Opus 5 High Fast")
	if a.BaseKey() != b.BaseKey() || a.CanonicalKey() == b.CanonicalKey() {
		t.Fatalf("serving variants share a base but not a canonical key: %q %q", a.CanonicalKey(), b.CanonicalKey())
	}
}

func TestLegacyAndTokenSpellingsShareIdentity(t *testing.T) {
	pairs := [][2]string{
		{"Claude Opus 4.6 (Thinking)", "Claude Opus 4.6 Thinking"},
		{"Claude Opus 4.8 Fast (Medium Thinking)", "Claude Opus 4.8 Medium Fast"},
		{"GPT-5.6 Sol (Extra High Reasoning) Fast", "GPT-5.6 Sol XHigh Thinking Fast"},
		{"GLM-5.2 1M (No Thinking)", "GLM-5.2 No Thinking 1M"},
	}
	for _, p := range pairs {
		if a, b := Classify(p[0]).CanonicalKey(), Classify(p[1]).CanonicalKey(); a != b {
			t.Errorf("%q -> %q but %q -> %q", p[0], a, p[1], b)
		}
	}
}

func TestNormaliseIdentifier(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-6":          "claude-opus-4.6",
		"claude-fable-5-1":         "claude-fable-5.1",
		"gpt-5.4-2026-03-05":       "gpt-5.4",
		"Claude-Opus-4-5-20251101": "Claude-Opus-4.5",
		"Grok-4-0709":              "Grok-4-0709",
		"Qwen3-235B-A22B":          "Qwen3-235B-A22B",
		"GLM-4":                    "GLM-4",
		"gemini-2-5-flash":         "gemini-2.5-flash",
		"Grok Code Fast 1":         "Grok Code Fast 1",
	}
	for in, want := range cases {
		if got := NormaliseIdentifier(in); got != want {
			t.Errorf("NormaliseIdentifier(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExternalSpellingsMeetDevinLabels(t *testing.T) {
	devin := Classify("Claude Opus 4.6").BaseKey()
	for _, external := range []string{"claude-opus-4-6", "Claude-Opus-4-6-20260101", "claude opus 4.6"} {
		if got := Classify(NormaliseIdentifier(external)).BaseKey(); got != devin {
			t.Errorf("%q normalises to %q, want %q", external, got, devin)
		}
	}
}

func TestParseEffortRejectsUnknown(t *testing.T) {
	if _, ok := ParseEffort("0.99"); ok {
		t.Fatal("a number is not an effort level")
	}
	if e, ok := ParseEffort(" X-High "); !ok || e != EffortXHigh {
		t.Fatalf("X-High parsed as %q %v", e, ok)
	}
}

func TestJaccardSuggestsButDoesNotAssert(t *testing.T) {
	s := Jaccard(TokenSet("gpt 5.2 turbo edition"), TokenSet("gpt 5.2"))
	if s <= 0 || s >= 1 {
		t.Fatalf("partial overlap should score strictly between 0 and 1, got %v", s)
	}
}
