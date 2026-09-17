package sources

import "fmt"

// derivedHarnessPrefix namespaces the code of a harness this build has no
// entry for, so a derived code can never be mistaken for, or collide with, a
// curated one or with a source's own context code such as epoch_ai_eval.
const derivedHarnessPrefix = "harness_"

// harnesses maps a published harness name, in its FoldContextLabel form, to a
// curated evaluation context.
//
// Because the keys are folds, spelling variants need no entry: "Claude Code",
// "claude-code" and "claude_code" all fold to claude_code. What is listed here
// is therefore only what the fold cannot know:
//
//   - the short codes this project has always published, which callers filter
//     on and aliases refer to, and which must not change;
//   - the few judgements that two genuinely different names are one harness —
//     "Codex" and "Codex CLI", "Terminus" and "Terminus 2", "Grok Shell" and
//     its later name "Grok Build". Those are product facts, not spelling, and
//     no mechanical rule could derive them.
//
// A harness absent from this map is not rejected: ResolveHarness derives a
// distinct context for it, so a new upstream harness needs no release. What it
// never gets is somebody else's code.
var harnesses = map[string]EvaluationContext{
	"devin":          {Code: "devin", Name: "Devin", Kind: ContextDevin},
	"claude_code":    {Code: "claude_code", Name: "Claude Code", Kind: ContextExternalHarness},
	"codex":          {Code: "codex", Name: "Codex CLI", Kind: ContextExternalHarness},
	"codex_cli":      {Code: "codex", Name: "Codex CLI", Kind: ContextExternalHarness},
	"cursor_cli":     {Code: "cursor_cli", Name: "Cursor CLI", Kind: ContextExternalHarness},
	"mini_swe_agent": {Code: "mini_swe_agent", Name: "mini-SWE-agent", Kind: ContextExternalHarness},
	"grok_build":     {Code: "grok_build", Name: "Grok Build", Kind: ContextExternalHarness},
	"grok_shell":     {Code: "grok_build", Name: "Grok Build", Kind: ContextExternalHarness},
	"opencode":       {Code: "opencode", Name: "OpenCode", Kind: ContextExternalHarness},
	"gemini_cli":     {Code: "gemini_cli", Name: "Gemini CLI", Kind: ContextExternalHarness},
	"openhands":      {Code: "openhands", Name: "OpenHands", Kind: ContextExternalHarness},
	"terminus":       {Code: "terminus", Name: "Terminus", Kind: ContextExternalHarness},
	"terminus_2":     {Code: "terminus", Name: "Terminus", Kind: ContextExternalHarness},
}

// ResolveHarness resolves a published harness name to an evaluation context,
// reporting whether the name was curated rather than derived.
//
// It fails only on a name that identifies nothing: an empty or
// punctuation-only string means the source did not say which harness produced
// the score, so the score cannot be attributed to one.
func ResolveHarness(published string) (ec EvaluationContext, curated bool, err error) {
	display := DisplayLabel(published)
	if fold := FoldContextLabel(display); fold != "" {
		if ec, ok := harnesses[fold]; ok {
			return ec, true, nil
		}
	}
	derived, err := DeriveContext(derivedHarnessPrefix, display, ContextExternalHarness)
	if err != nil {
		return EvaluationContext{}, false, fmt.Errorf("no harness is named, so the context that produced the value is unknown: %w", err)
	}
	return derived, false, nil
}

// KnownHarnessContexts lists each curated harness context once, in a stable
// order. Collectors declare these up front; contexts derived during a
// collection come back in Result.Contexts instead.
func KnownHarnessContexts() []EvaluationContext {
	seen := map[string]bool{}
	var out []EvaluationContext
	for _, key := range []string{"devin", "claude_code", "codex", "cursor_cli", "mini_swe_agent", "grok_build", "opencode", "gemini_cli", "openhands", "terminus"} {
		ctx := harnesses[key]
		if !seen[ctx.Code] {
			seen[ctx.Code] = true
			out = append(out, ctx)
		}
	}
	return out
}
