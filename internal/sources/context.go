package sources

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// FoldContextLabel is the canonical form of a source-provided context label.
//
// Two labels denote the same context exactly when their folds are equal:
// letters are lowercased and every run of other characters becomes one "_".
// So "Grok CLI", "grok-cli" and "grok_cli" are one context, while "ForgeCode"
// and "Forge Code" stay two — one has a separator where the other has none,
// and nothing in the source says they are the same product.
//
// This is the whole equivalence rule. There is no edit distance, no substring
// matching and no token overlap: a fold either matches exactly or it does not.
// Guessing that two labels mean the same harness would invent evidence, and a
// score's harness changes the score.
//
// The fold is empty when the label carries no letters or digits at all, which
// callers must treat as no usable label rather than as a context named "".
func FoldContextLabel(label string) string {
	var b strings.Builder
	b.Grow(len(label))
	pendingSeparator := false
	for _, r := range label {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if pendingSeparator && b.Len() > 0 {
				b.WriteByte('_')
			}
			pendingSeparator = false
			b.WriteRune(unicode.ToLower(r))
		default:
			pendingSeparator = true
		}
	}
	return b.String()
}

// DisplayLabel is a source label with its whitespace tidied and nothing else
// changed, so the value a caller sees is the value the source published.
func DisplayLabel(label string) string { return strings.Join(strings.Fields(label), " ") }

// MaxContextLabel bounds how long a source-provided label may be before this
// version refuses to derive a context from it.
//
// A context code is a stable public identifier: callers filter on it, it is a
// primary key, and it is repeated in every response that names the context. A
// label long enough to make an unwieldy code is more likely to be a parsing
// mistake or a mangled cell than a harness name, and truncating it would risk
// giving two different harnesses the same code. The bound is far above any
// real name, so it rejects only the implausible.
const MaxContextLabel = 128

// DeriveContext builds a context for a label this version has no entry for.
//
// The code is prefix + the label's fold, which keeps derived codes in their own
// namespace so one can never collide with a curated code that means something
// else. The name is the source's own spelling. The kind is the caller's, never
// the source's: kinds decide whether a value is a proxy, so they stay a closed
// set this project owns.
func DeriveContext(prefix, label string, kind ContextKind) (EvaluationContext, error) {
	display := DisplayLabel(label)
	fold := FoldContextLabel(display)
	switch {
	case fold == "":
		return EvaluationContext{}, fmt.Errorf("%q contains no letters or digits, so it names no evaluation context", truncateLabel(label))
	case len(fold) > MaxContextLabel:
		return EvaluationContext{}, fmt.Errorf("%q is %d characters once normalized, beyond the %d this version will turn into an evaluation context code",
			truncateLabel(display), len(fold), MaxContextLabel)
	case !ValidContextKind(kind):
		return EvaluationContext{}, fmt.Errorf("evaluation context kind %q is not one this version defines", kind)
	}
	return EvaluationContext{Code: prefix + fold, Name: display, Kind: kind}, nil
}

// truncateLabel keeps an over-long label out of an error message, which a
// caller may store or print. It cuts on a rune boundary: the reason is written
// to the database and returned as JSON, so half a character would be a defect
// in output the user reads.
func truncateLabel(s string) string {
	if len(s) <= 60 {
		return s
	}
	cut := 60
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
