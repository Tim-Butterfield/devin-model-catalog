// Package identity turns published model names into variant identity.
//
// It classifies suffixes rather than stripping them. A trailing word is removed
// from a model's base name only when it is recognised as one of the modelled
// dimensions (reasoning effort, serving variant, context variant); anything
// else stays part of the name, because an unrecognised suffix is
// identity-bearing until proven otherwise. "Gemini 3 Flash" and "Gemini 3 Pro"
// are different models, and so are "Claude Opus 4.6" and
// "Claude Opus 4.6 1M".
//
// Classification is label-driven only. Devin's model_uid values are not
// reliably parseable (some are opaque, and serving markers are inconsistent),
// so they are used as stable identifiers, never as a source of dimensions.
package identity

import (
	"strings"
)

// Effort is a reasoning-effort level. Devin sells effort levels as separate
// models at separate prices, so it is part of identity.
type Effort string

const (
	EffortUnspecified Effort = "" // the source did not say
	EffortNone        Effort = "none"
	EffortMinimal     Effort = "minimal"
	EffortLow         Effort = "low"
	EffortMedium      Effort = "medium"
	EffortHigh        Effort = "high"
	EffortXHigh       Effort = "xhigh"
	EffortMax         Effort = "max"
)

// Efforts lists every specified effort level, lowest first.
var Efforts = []Effort{EffortNone, EffortMinimal, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}

// ParseEffort reads a published effort word. It reports false for anything
// that is not a recognised level, so callers never guess.
func ParseEffort(s string) (Effort, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "no", "off":
		return EffortNone, true
	case "minimal":
		return EffortMinimal, true
	case "low":
		return EffortLow, true
	case "medium", "med":
		return EffortMedium, true
	case "high":
		return EffortHigh, true
	case "xhigh", "x-high", "extra high":
		return EffortXHigh, true
	case "max":
		return EffortMax, true
	}
	return EffortUnspecified, false
}

// Serving distinguishes serving tiers such as Devin's "Fast", which are
// separate variants at their own published rates.
type Serving string

const (
	ServingUnspecified Serving = ""
	ServingFast        Serving = "fast"
)

// Variant is the identity parsed out of a published name.
type Variant struct {
	// BaseName is the name with recognised dimension suffixes removed.
	BaseName string `json:"base_name"`

	Effort  Effort  `json:"effort,omitempty"`
	Serving Serving `json:"serving_variant,omitempty"`

	// ContextVariant is a context-bearing marker such as "1M". It denotes a
	// genuinely different model and is never collapsed into the base.
	ContextVariant string `json:"context_variant,omitempty"`

	// Classified lists the suffixes recognised and removed, for diagnostics.
	Classified []string `json:"classified,omitempty"`
}

// BaseKey is the matching key of the base model: the grain most benchmark
// sources publish at. Effort and serving variant are excluded because they are
// separate dimensions on an observation; the context variant is included
// because it is a different model.
func (v Variant) BaseKey() string {
	key := Normalise(v.BaseName)
	if v.ContextVariant != "" {
		key += "|ctx:" + strings.ToLower(v.ContextVariant)
	}
	return key
}

// CanonicalKey includes every dimension. Two names sharing a canonical key are
// the same variant.
func (v Variant) CanonicalKey() string {
	parts := []string{v.BaseKey()}
	if v.Effort != EffortUnspecified {
		parts = append(parts, "effort:"+string(v.Effort))
	}
	if v.Serving != ServingUnspecified {
		parts = append(parts, "serving:"+string(v.Serving))
	}
	return strings.Join(parts, "|")
}

// effortTwoWord maps trailing two-word effort phrases.
//
// Bare "Thinking" is deliberately absent: Devin publishes both
// "Claude Opus 4.5" and "Claude Opus 4.5 Thinking" as distinct variants, and
// nothing states which effort bare "Thinking" denotes. Guessing would either
// collide two identities or invent a level, so the word stays in the name.
var effortTwoWord = map[string]Effort{
	"no thinking":       EffortNone,
	"none thinking":     EffortNone,
	"minimal thinking":  EffortMinimal,
	"low thinking":      EffortLow,
	"medium thinking":   EffortMedium,
	"high thinking":     EffortHigh,
	"xhigh thinking":    EffortXHigh,
	"x-high thinking":   EffortXHigh,
	"max thinking":      EffortMax,
	"no reasoning":      EffortNone,
	"none reasoning":    EffortNone,
	"minimal reasoning": EffortMinimal,
	"low reasoning":     EffortLow,
	"medium reasoning":  EffortMedium,
	"high reasoning":    EffortHigh,
	"xhigh reasoning":   EffortXHigh,
	"x-high reasoning":  EffortXHigh,
	"max reasoning":     EffortMax,
}

// effortThreeWord maps trailing three-word effort phrases. It is checked
// before the two-word map so "Extra High Reasoning" is not read as
// "High Reasoning" with "Extra" left in the name.
var effortThreeWord = map[string]Effort{
	"extra high reasoning": EffortXHigh,
	"extra high thinking":  EffortXHigh,
}

// effortOneWord maps a trailing bare effort word ("Claude Fable 5 Max").
var effortOneWord = map[string]Effort{
	"none":    EffortNone,
	"minimal": EffortMinimal,
	"low":     EffortLow,
	"medium":  EffortMedium,
	"high":    EffortHigh,
	"xhigh":   EffortXHigh,
	"x-high":  EffortXHigh,
	"max":     EffortMax,
}

// servingOneWord maps a trailing serving-variant word. "Flash", "Turbo",
// "Pro", "Mini" and "Lightning" are NOT here: they are part of model names.
var servingOneWord = map[string]Serving{
	"fast": ServingFast,
}

// contextOneWord maps a trailing context-bearing marker.
var contextOneWord = map[string]string{
	"1m": "1M",
}

// Classify parses a published name into variant identity.
//
// Suffixes are matched only at the end of the name, which keeps
// "Grok Code Fast 1" and "Fast Arena" intact. A one-word name is never reduced
// to nothing.
func Classify(label string) Variant {
	// Parentheses are spelling, not identity: Devin's legacy table writes
	// "Claude Opus 4.6 (Thinking)" for the model its token tables call
	// "Claude Opus 4.6 Thinking".
	label = strings.NewReplacer("(", " ", ")", " ").Replace(label)
	v := Variant{BaseName: strings.Join(strings.Fields(label), " ")}

	for {
		tokens := strings.Fields(v.BaseName)
		if len(tokens) < 2 {
			break
		}

		if v.Effort == EffortUnspecified && len(tokens) >= 4 {
			phrase := strings.ToLower(strings.Join(tokens[len(tokens)-3:], " "))
			if effort, ok := effortThreeWord[phrase]; ok {
				v.Effort = effort
				v.BaseName = strings.Join(tokens[:len(tokens)-3], " ")
				v.Classified = append(v.Classified, "effort:"+phrase)
				continue
			}
		}

		if v.Effort == EffortUnspecified && len(tokens) >= 3 {
			phrase := strings.ToLower(tokens[len(tokens)-2] + " " + tokens[len(tokens)-1])
			if effort, ok := effortTwoWord[phrase]; ok {
				v.Effort = effort
				v.BaseName = strings.Join(tokens[:len(tokens)-2], " ")
				v.Classified = append(v.Classified, "effort:"+phrase)
				continue
			}
		}

		last := strings.ToLower(tokens[len(tokens)-1])

		if v.ContextVariant == "" {
			if marker, ok := contextOneWord[last]; ok {
				v.ContextVariant = marker
				v.BaseName = strings.Join(tokens[:len(tokens)-1], " ")
				v.Classified = append(v.Classified, "context:"+last)
				continue
			}
		}

		if v.Serving == ServingUnspecified {
			if serving, ok := servingOneWord[last]; ok {
				v.Serving = serving
				v.BaseName = strings.Join(tokens[:len(tokens)-1], " ")
				v.Classified = append(v.Classified, "serving:"+last)
				continue
			}
		}

		if v.Effort == EffortUnspecified {
			if effort, ok := effortOneWord[last]; ok {
				v.Effort = effort
				v.BaseName = strings.Join(tokens[:len(tokens)-1], " ")
				v.Classified = append(v.Classified, "effort:"+last)
				continue
			}
		}

		break
	}

	return v
}

// Normalise lowercases and collapses punctuation and whitespace, for use as a
// matching key. It removes no words. Version dots are kept: GPT-5.2 must not
// become GPT-52.
func Normalise(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	lastWasSpace := true
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.':
			b.WriteRune(r)
			lastWasSpace = false
		default:
			if !lastWasSpace {
				b.WriteRune(' ')
				lastWasSpace = true
			}
		}
	}

	return strings.TrimSpace(b.String())
}

// NormaliseIdentifier converts a vendor-style model identifier towards the
// spelling Devin publishes, so both describe the same identity:
//
//   - a trailing release date ("-2026-03-05" or "-20251101") is publication
//     metadata, not identity, and is removed;
//   - a hyphen between a digit and a short (one- or two-digit) number is a
//     version separator: "claude-opus-4-6" means "claude-opus-4.6".
//
// Other hyphens are left alone. "Grok-4-0709" keeps its build number and
// "Qwen3-235B" keeps its size, because neither is a short version component.
func NormaliseIdentifier(s string) string {
	s = StripTrailingDate(strings.TrimSpace(s))

	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range runes {
		if r != '-' || i == 0 || !isDigit(runes[i-1]) {
			b.WriteRune(r)
			continue
		}
		// Measure the digit run after the hyphen.
		j := i + 1
		for j < len(runes) && isDigit(runes[j]) {
			j++
		}
		run := j - (i + 1)
		shortVersion := run >= 1 && run <= 2 && (j == len(runes) || !isAlnum(runes[j]))
		if shortVersion {
			b.WriteRune('.')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// StripTrailingDate removes a trailing "-YYYY-MM-DD" or "-YYYYMMDD" stamp.
// Only a well-formed date in the 19xx/20xx range is removed; a trailing number
// that is not a date ("Grok Code Fast 1", "GLM-4") is identity and stays.
func StripTrailingDate(s string) string {
	if n := len(s); n > 11 {
		tail := s[n-11:]
		if tail[0] == '-' && tail[5] == '-' && tail[8] == '-' &&
			allDigits(tail[1:5]) && allDigits(tail[6:8]) && allDigits(tail[9:11]) &&
			(tail[1:3] == "19" || tail[1:3] == "20") {
			return strings.TrimRight(s[:n-11], "-_ ")
		}
	}
	if n := len(s); n > 9 {
		tail := s[n-9:]
		if tail[0] == '-' && allDigits(tail[1:]) && (tail[1:3] == "19" || tail[1:3] == "20") {
			return strings.TrimRight(s[:n-9], "-_ ")
		}
	}
	return s
}

// TokenSet splits a key into discriminating tokens for similarity scoring.
func TokenSet(key string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range strings.Fields(Normalise(strings.ReplaceAll(key, "|", " "))) {
		if len(tok) < 2 {
			continue
		}
		out[tok] = true
	}
	return out
}

// Jaccard is intersection over union of two token sets. It is used only to
// suggest candidates for human review, never to assert identity.
func Jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	intersection := 0
	for tok := range a {
		if b[tok] {
			intersection++
		}
	}
	return float64(intersection) / float64(len(a)+len(b)-intersection)
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func isAlnum(r rune) bool {
	return isDigit(r) || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func allDigits(s string) bool {
	for _, r := range s {
		if !isDigit(r) {
			return false
		}
	}
	return s != ""
}
