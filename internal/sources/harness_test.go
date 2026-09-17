package sources

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// Collectors declare KnownHarnessContexts, and every curated harness name maps
// to one of its entries; a context missing from the list would be emitted
// undeclared.
func TestEveryCuratedHarnessContextIsDeclared(t *testing.T) {
	declared := KnownHarnessContexts()
	for name, ctx := range harnesses {
		if !slices.Contains(declared, ctx) {
			t.Errorf("harness %q maps to %s, which KnownHarnessContexts does not declare", name, ctx.Code)
		}
	}
	codes := map[string]bool{}
	for _, ctx := range declared {
		if codes[ctx.Code] {
			t.Errorf("context %s is declared twice", ctx.Code)
		}
		codes[ctx.Code] = true
	}
}

// The curated map is keyed by fold, so a key that is not its own fold would
// simply never match anything.
func TestCuratedHarnessKeysAreFolds(t *testing.T) {
	for key := range harnesses {
		if fold := FoldContextLabel(key); fold != key {
			t.Errorf("harness key %q is not in folded form (%q), so no published name can reach it", key, fold)
		}
	}
}

// Derived codes are namespaced, but a curated code must still be unreachable
// by derivation for a *different* harness. Requiring every curated code to be
// one of the map's own keys keeps that true however the map grows: a name that
// folds to a curated code resolves to that curated entry, never to a duplicate.
func TestEveryCuratedCodeIsReachableAsAKey(t *testing.T) {
	for _, ctx := range harnesses {
		if _, ok := harnesses[ctx.Code]; !ok {
			t.Errorf("curated code %s is not itself a key, so the name it stands for cannot resolve to it", ctx.Code)
		}
	}
}

func TestResolveHarnessFoldsSpellingButNotProducts(t *testing.T) {
	sameAs := func(a, b string) {
		t.Helper()
		x, _, err := ResolveHarness(a)
		if err != nil {
			t.Fatalf("%q: %v", a, err)
		}
		y, _, err := ResolveHarness(b)
		if err != nil {
			t.Fatalf("%q: %v", b, err)
		}
		if x.Code != y.Code {
			t.Errorf("%q and %q differ only in spelling but resolved to %s and %s", a, b, x.Code, y.Code)
		}
	}
	differ := func(a, b string) {
		t.Helper()
		x, _, _ := ResolveHarness(a)
		y, _, _ := ResolveHarness(b)
		if x.Code == y.Code {
			t.Errorf("%q and %q are different labels but both resolved to %s", a, b, x.Code)
		}
	}

	// Case and separators are spelling.
	sameAs("Claude Code", "claude-code")
	sameAs("claude_code", "CLAUDE CODE")
	sameAs("Mini-SWE-Agent", "mini-swe-agent")
	sameAs(" Codex CLI ", "codex-cli")
	sameAs("Grok CLI", "grok-cli")
	// Curated product judgements survive.
	sameAs("Codex", "Codex CLI")
	sameAs("Terminus", "Terminus 2")
	sameAs("Grok Shell", "Grok Build")
	// A separator where the other label has none is not spelling: nothing in
	// the source says these are the same product.
	differ("ForgeCode", "Forge Code")
	differ("Terminus", "Terminus-KIRA")
	differ("MAYA", "MAYA-V2")
	// An unknown harness never borrows a curated code.
	differ("Droid", "Claude Code")
}

func TestResolveHarnessDerivesUnknownNames(t *testing.T) {
	ctx, curated, err := ResolveHarness("Letta Code")
	if err != nil {
		t.Fatal(err)
	}
	if curated {
		t.Error("Letta Code is not curated")
	}
	if ctx.Code != "harness_letta_code" {
		t.Errorf("code = %q", ctx.Code)
	}
	// The upstream spelling is preserved exactly, only whitespace-tidied.
	if ctx.Name != "Letta Code" {
		t.Errorf("name = %q, want the source's own spelling", ctx.Name)
	}
	// Classified conservatively: another harness, not Devin and not direct.
	if ctx.Kind != ContextExternalHarness {
		t.Errorf("kind = %q", ctx.Kind)
	}

	if ctx, curated, err := ResolveHarness("  Claude   Code  "); err != nil || !curated || ctx.Code != "claude_code" {
		t.Errorf("curated lookup ignores surrounding and repeated whitespace: %+v %v %v", ctx, curated, err)
	}
}

func TestResolveHarnessRejectsNamelessValues(t *testing.T) {
	for _, in := range []string{"", "   ", "\t\n", "---", "()"} {
		if ctx, _, err := ResolveHarness(in); err == nil {
			t.Errorf("%q names no harness, but resolved to %+v", in, ctx)
		}
	}
}

func TestFoldContextLabel(t *testing.T) {
	cases := map[string]string{
		"Claude Code":       "claude_code",
		"claude-code":       "claude_code",
		"  Codex CLI  ":     "codex_cli",
		"Prompt + Thinking": "prompt_thinking",
		"Terminus 2":        "terminus_2",
		"spoox-o-m":         "spoox_o_m",
		"CodeBrain-1.5":     "codebrain_1_5",
		"ForgeCode":         "forgecode",
		"":                  "",
		"---":               "",
		"日本語":               "日本語",
	}
	for in, want := range cases {
		if got := FoldContextLabel(in); got != want {
			t.Errorf("FoldContextLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeriveContextRejectsUnknownKinds(t *testing.T) {
	if _, err := DeriveContext("harness_", "Droid", ContextKind("something_new")); err == nil {
		t.Fatal("context kinds are a closed set this project owns; a collector may not invent one")
	}
}

// A context code is a public identifier repeated in every response that names
// the context, so a label long enough to make an unwieldy one is refused
// rather than truncated: truncation could give two harnesses the same code.
func TestDeriveContextRejectsAnImplausiblyLongLabel(t *testing.T) {
	long := strings.Repeat("a", MaxContextLabel+1)
	ec, err := DeriveContext("harness_", long, ContextExternalHarness)
	if err == nil {
		t.Fatalf("a %d-character label must be refused, got %+v", len(long), ec)
	}
	// The error must not itself carry the whole label back to the caller.
	if len(err.Error()) > 200 {
		t.Errorf("the error repeats the over-long label: %v", err)
	}
	if _, err := DeriveContext("harness_", strings.Repeat("a", MaxContextLabel), ContextExternalHarness); err != nil {
		t.Errorf("a label exactly at the bound is still a harness: %v", err)
	}

	if _, _, err := ResolveHarness(long); err == nil {
		t.Fatal("a collector resolving such a label must see the failure, so it can reject the row with a reason")
	}
}

// The rejection reason is stored and returned as JSON, so shortening the label
// must not cut a character in half. "a" followed by two-byte runes puts a
// continuation byte exactly at the cut, which a byte-wise slice would split.
func TestARejectedLabelIsShortenedOnACharacterBoundary(t *testing.T) {
	label := "a" + strings.Repeat("é", 100)
	_, err := DeriveContext("harness_", label, ContextExternalHarness)
	if err == nil {
		t.Fatalf("a %d-byte label must be refused", len(label))
	}
	if !utf8.ValidString(err.Error()) {
		t.Errorf("the error is not valid UTF-8: %q", err.Error())
	}
}

func TestResultAddContextIsIdempotentAndKeepsFirstSpelling(t *testing.T) {
	var r Result
	r.AddContext(EvaluationContext{Code: "harness_droid", Name: "Droid", Kind: ContextExternalHarness})
	r.AddContext(EvaluationContext{Code: "harness_droid", Name: "DROID", Kind: ContextExternalHarness})
	if len(r.Contexts) != 1 || r.Contexts[0].Name != "Droid" {
		t.Fatalf("contexts = %+v", r.Contexts)
	}
}
