package devin

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/identity"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/metrics"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources"
)

// The Legacy enterprise (credits) dataset is rendered by the ModelsTable
// component. Its rows are not JSON: they are a JavaScript array literal
// (`const allModels = [{ name: "…", icon: windsurfIcon, credits: "9", … }]`)
// inside the component, in the same Markdown document. It is read here with a
// strict parser for exactly that literal subset — strings, numbers, booleans,
// null and bare identifiers — so no JavaScript is executed and no browser is
// needed. Anything outside that subset makes the dataset unsupported rather
// than partially read.

// legacyComponent renders the legacy credits table.
const legacyComponent = "ModelsTable"

// MetricLegacyCredits is the credit cost published in the legacy table.
const MetricLegacyCredits = "devin_legacy_credits"

func legacyMetricDefinition() metrics.Definition {
	return metrics.Definition{
		Key:         MetricLegacyCredits,
		DisplayName: "Devin legacy credits",
		Description: "Credit cost Devin publishes for this model in its Legacy enterprise (credits) table, which applies to credit-based enterprise contracts. " +
			"The table does not state what one credit buys or the metering basis, so compare values only within that dataset; it is not a token price and is not converted to one. " +
			"A variable price (shown as *) is not recorded.",
		ValueKind: metrics.KindNumber, Unit: "credits", Direction: metrics.LowerIsBetter,
		Scope: metrics.ScopeCatalog, DefinedBy: SourceCode, MissingPossible: true, Status: metrics.StatusCurrent,
	}
}

// jsKind classifies a literal value.
type jsKind string

const (
	jsString     jsKind = "string"
	jsNumber     jsKind = "number"
	jsBool       jsKind = "bool"
	jsNull       jsKind = "null"
	jsIdentifier jsKind = "identifier"
)

type jsValue struct {
	Kind jsKind
	Text string
}

type jsField struct {
	Key   string
	Value jsValue
}

// LegacyEntry is one row of the legacy model table, preserved in source order.
type LegacyEntry struct {
	Fields []jsField
}

func (e LegacyEntry) get(key string) (jsValue, bool) {
	for _, f := range e.Fields {
		if f.Key == key {
			return f.Value, true
		}
	}
	return jsValue{}, false
}

// rawJSON renders the row for provenance. Bare identifiers (icon references)
// are recorded as {"js_identifier": name} so they are not mistaken for strings.
func (e LegacyEntry) rawJSON() string {
	var sb strings.Builder
	sb.WriteByte('{')
	for i, f := range e.Fields {
		if i > 0 {
			sb.WriteByte(',')
		}
		k, _ := json.Marshal(f.Key)
		sb.Write(k)
		sb.WriteByte(':')
		switch f.Value.Kind {
		case jsString:
			v, _ := json.Marshal(f.Value.Text)
			sb.Write(v)
		case jsNumber, jsBool, jsNull:
			sb.WriteString(f.Value.Text)
		case jsIdentifier:
			v, _ := json.Marshal(map[string]string{"js_identifier": f.Value.Text})
			sb.Write(v)
		}
	}
	sb.WriteByte('}')
	return sb.String()
}

// extractLegacyModels reads the allModels literal of the ModelsTable
// component. It returns (nil, nil) when the page defines no such component.
func extractLegacyModels(md string) ([]LegacyEntry, error) {
	start := strings.Index(md, "export const "+legacyComponent)
	if start == -1 {
		return nil, nil
	}
	body := md[start:]
	if next := strings.Index(body[1:], "\nexport const "); next != -1 {
		body = body[:next+1]
	}
	rel := strings.Index(body, "const allModels")
	if rel == -1 {
		return nil, fmt.Errorf("the %s component no longer defines allModels", legacyComponent)
	}
	open := strings.Index(body[rel:], "[")
	if open == -1 {
		return nil, fmt.Errorf("allModels has no array")
	}
	open += rel
	end, err := matchBracket(body, open, '[', ']')
	if err != nil {
		return nil, fmt.Errorf("allModels array is unterminated: %w", err)
	}
	// The literal must be the whole initializer; `[…].map(f)` is computed data,
	// including when a formatter puts the call on the next line.
	if after := strings.TrimLeft(body[end+1:], " \t\r\n"); after != "" && strings.IndexByte(".[(?+-*/%&|<>=!,", after[0]) >= 0 {
		return nil, fmt.Errorf("allModels is not a plain array literal (followed by %q)", after[:min(len(after), 12)])
	}
	return parseJSObjectArray(body[open : end+1])
}

type jsParser struct {
	s string
	i int
}

func (p *jsParser) fail(format string, args ...any) error {
	return fmt.Errorf("allModels literal at offset %d: %s", p.i, fmt.Sprintf(format, args...))
}

func (p *jsParser) ws() {
	for p.i < len(p.s) && strings.IndexByte(" \t\r\n", p.s[p.i]) >= 0 {
		p.i++
	}
}

func (p *jsParser) peek() byte {
	if p.i >= len(p.s) {
		return 0
	}
	return p.s[p.i]
}

func (p *jsParser) expect(c byte) error {
	p.ws()
	if p.peek() != c {
		return p.fail("expected %q, found %q", c, p.peek())
	}
	p.i++
	return nil
}

func parseJSObjectArray(s string) ([]LegacyEntry, error) {
	p := &jsParser{s: s}
	if err := p.expect('['); err != nil {
		return nil, err
	}
	var out []LegacyEntry
	for {
		p.ws()
		if p.peek() == ']' {
			p.i++
			break
		}
		entry, err := p.object()
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
		p.ws()
		switch p.peek() {
		case ',':
			p.i++
		case ']':
			p.i++
			p.ws()
			if p.i != len(p.s) {
				return nil, p.fail("unexpected content after the array")
			}
			return out, nil
		default:
			return nil, p.fail("expected ',' or ']', found %q", p.peek())
		}
	}
	return out, nil
}

func (p *jsParser) object() (LegacyEntry, error) {
	var e LegacyEntry
	if err := p.expect('{'); err != nil {
		return e, err
	}
	seen := map[string]bool{}
	for {
		p.ws()
		if p.peek() == '}' {
			p.i++
			return e, nil
		}
		key, err := p.key()
		if err != nil {
			return e, err
		}
		if seen[key] {
			return e, p.fail("duplicate key %q", key)
		}
		seen[key] = true
		if err := p.expect(':'); err != nil {
			return e, err
		}
		p.ws()
		value, err := p.value()
		if err != nil {
			return e, err
		}
		e.Fields = append(e.Fields, jsField{Key: key, Value: value})
		p.ws()
		switch p.peek() {
		case ',':
			p.i++
		case '}':
			p.i++
			return e, nil
		default:
			return e, p.fail("expected ',' or '}', found %q", p.peek())
		}
	}
}

func (p *jsParser) key() (string, error) {
	switch c := p.peek(); {
	case c == '"' || c == '\'':
		return p.quoted()
	case isIdentStart(c):
		return p.identifier(), nil
	}
	return "", p.fail("expected a property name, found %q", p.peek())
}

func (p *jsParser) value() (jsValue, error) {
	c := p.peek()
	switch {
	case c == '"' || c == '\'':
		s, err := p.quoted()
		return jsValue{Kind: jsString, Text: s}, err
	case c == '-' || (c >= '0' && c <= '9'):
		start := p.i
		for p.i < len(p.s) && strings.IndexByte("0123456789.eE+-", p.s[p.i]) >= 0 {
			p.i++
		}
		text := p.s[start:p.i]
		if _, err := strconv.ParseFloat(text, 64); err != nil {
			return jsValue{}, p.fail("malformed number %q", text)
		}
		return jsValue{Kind: jsNumber, Text: text}, nil
	case isIdentStart(c):
		id := p.identifier()
		switch id {
		case "true", "false":
			return jsValue{Kind: jsBool, Text: id}, nil
		case "null":
			return jsValue{Kind: jsNull, Text: id}, nil
		}
		p.ws()
		if next := p.peek(); next == '(' || next == '.' || next == '[' {
			return jsValue{}, p.fail("expression %q%c is not a literal value", id, next)
		}
		return jsValue{Kind: jsIdentifier, Text: id}, nil
	}
	return jsValue{}, p.fail("unsupported value starting with %q", c)
}

func (p *jsParser) quoted() (string, error) {
	quote := p.s[p.i]
	p.i++
	var sb strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == quote:
			p.i++
			return sb.String(), nil
		case c == '\\':
			if p.i+1 >= len(p.s) {
				return "", p.fail("unterminated escape")
			}
			next := p.s[p.i+1]
			switch next {
			case '"', '\'', '\\', '/':
				sb.WriteByte(next)
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'u':
				if p.i+6 > len(p.s) {
					return "", p.fail("short unicode escape")
				}
				r, err := strconv.ParseUint(p.s[p.i+2:p.i+6], 16, 32)
				if err != nil {
					return "", p.fail("bad unicode escape")
				}
				sb.WriteRune(rune(r))
				p.i += 4
			default:
				return "", p.fail("unsupported escape \\%c", next)
			}
			p.i += 2
		case c == '\n':
			return "", p.fail("newline inside a string")
		default:
			sb.WriteByte(c)
			p.i++
		}
	}
	return "", p.fail("unterminated string")
}

func (p *jsParser) identifier() string {
	start := p.i
	for p.i < len(p.s) && (isIdentStart(p.s[p.i]) || (p.s[p.i] >= '0' && p.s[p.i] <= '9')) {
		p.i++
	}
	return p.s[start:p.i]
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// LegacyUID derives a stable identifier for a legacy row, which publishes no
// model_uid of its own: "legacy:" plus the lowercased name with punctuation
// runs replaced by hyphens. A renamed row therefore becomes a different model.
func LegacyUID(name string) string {
	var sb strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' {
			if dash && sb.Len() > 0 {
				sb.WriteByte('-')
			}
			sb.WriteRune(r)
			dash = false
			continue
		}
		dash = true
	}
	return "legacy:" + sb.String()
}

func buildLegacyCatalog(p *Page, ds Dataset) (*Catalog, error) {
	cat := &Catalog{Dataset: ds, Warnings: append([]string(nil), p.Warnings...)}
	var cands []candidate
	for i, e := range p.Legacy {
		cat.PublishedRows++
		ref := fmt.Sprintf("ModelsTable.allModels[%d]", i)
		nameValue, ok := e.get("name")
		name := strings.TrimSpace(nameValue.Text)
		if !ok || nameValue.Kind != jsString || name == "" {
			cat.Dispositions = append(cat.Dispositions, RowDisposition{RowRef: ref, Disposition: DispositionRejected,
				Reason: "row has no name, so no identity can be read from it"})
			continue
		}
		provider := ""
		if v, ok := e.get("provider"); ok && v.Kind == jsString {
			provider = strings.ToUpper(v.Text)
		}
		entry := e
		cands = append(cands, candidate{
			ref: ref, uid: LegacyUID(name), label: name, provider: provider,
			variant: identity.Classify(name), raw: e.rawJSON(),
			observations: func(v identity.Variant) []sources.Observation {
				return legacyObservations(cat, ref, entry, name, v)
			},
		})
	}
	cat.settle(cands)
	if err := cat.verify(); err != nil {
		return nil, err
	}
	return cat, nil
}

func legacyObservations(cat *Catalog, ref string, e LegacyEntry, name string, v identity.Variant) []sources.Observation {
	base := sources.Observation{
		SourceModelName: name, SourceRowRef: ref, CatalogUID: LegacyUID(name),
		BaseKey: v.BaseKey(), Effort: v.Effort, Serving: v.Serving, ContextVariant: v.ContextVariant,
		EvaluationContext: PublishedContext,
	}
	var out []sources.Observation

	if credits, ok := e.get("credits"); ok {
		text := strings.TrimSpace(credits.Text)
		switch {
		case credits.Kind != jsString && credits.Kind != jsNumber:
			cat.Warnings = append(cat.Warnings, fmt.Sprintf("%s (%s) publishes credits as a %s; no credits value recorded", ref, name, credits.Kind))
		case text == "*":
			// The table's footnote: variable per-request pricing.
		default:
			f, err := strconv.ParseFloat(text, 64)
			// ParseFloat accepts "NaN" and "Inf"; neither is a published credit cost.
			if err != nil || !(f >= 0) || math.IsInf(f, 0) {
				cat.Warnings = append(cat.Warnings, fmt.Sprintf("%s (%s) publishes credits %q, which is not a number; no credits value recorded", ref, name, text))
				break
			}
			o := base
			o.Metric, o.Value = MetricLegacyCredits, sources.NumberValue(f)
			out = append(out, o)
		}
	}

	// The table's Recommended tab lists rows whose recommended property is
	// truthy, so an absent property publishes "not recommended".
	recommended := false
	if r, ok := e.get("recommended"); ok {
		if r.Kind != jsBool {
			cat.Warnings = append(cat.Warnings, fmt.Sprintf("%s (%s) publishes recommended as a %s; not recorded", ref, name, r.Kind))
			return out
		}
		recommended = r.Text == "true"
	}
	o := base
	o.Metric, o.Value = MetricRecommended, sources.BoolValue(recommended)
	return append(out, o)
}
