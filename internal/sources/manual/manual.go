// Package manual parses user-supplied observation files.
//
// Manual import exists for evidence that cannot be collected automatically on
// a defensible basis — for example, context windows the user transcribes from
// vendor documentation. Imported data is stored and queried exactly like
// collected data, under its own source code, and carries the access basis the
// user declares.
package manual

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/identity"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/metrics"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources"
)

// CodePattern is the required shape of a manual source code, which keeps
// imports from colliding with built-in sources.
var CodePattern = regexp.MustCompile(`^manual_[a-z0-9_]{1,40}$`)

// FileSource declares the imported source.
type FileSource struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Homepage    string `json:"homepage,omitempty"`
	URL         string `json:"url,omitempty"`
	AccessBasis string `json:"access_basis"`
	License     string `json:"license,omitempty"`
	Attribution string `json:"attribution,omitempty"`
}

// FileMetric declares a new metric. Scope is always evidence.
type FileMetric struct {
	Key         string            `json:"key"`
	DisplayName string            `json:"display_name"`
	Description string            `json:"description"`
	ValueKind   metrics.ValueKind `json:"value_kind"`
	Unit        string            `json:"unit,omitempty"`
	Direction   metrics.Direction `json:"direction"`
}

// FileObservation is one imported value.
type FileObservation struct {
	Metric            string `json:"metric"`
	Model             string `json:"model"`
	Effort            string `json:"effort,omitempty"`
	ServingVariant    string `json:"serving_variant,omitempty"`
	EvaluationContext string `json:"evaluation_context"`
	Value             any    `json:"value"`
	Note              string `json:"note,omitempty"`
}

// File is the import document.
type File struct {
	Source             FileSource                  `json:"source"`
	Metrics            []FileMetric                `json:"metrics,omitempty"`
	EvaluationContexts []sources.EvaluationContext `json:"evaluation_contexts,omitempty"`
	Observations       []FileObservation           `json:"observations"`
}

// Import is a validated import, ready to store once metric kinds are checked
// against the database.
type Import struct {
	Info     sources.Info
	Metrics  []metrics.Definition
	Contexts []sources.EvaluationContext
	Rows     []FileObservation
}

// Parse decodes and structurally validates an import document.
func Parse(raw []byte) (*Import, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("import file is not a valid import document: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("import file is not a valid import document: it must contain exactly one JSON object")
	}
	if !CodePattern.MatchString(f.Source.Code) {
		return nil, fmt.Errorf("source.code %q must match %s", f.Source.Code, CodePattern)
	}
	if strings.TrimSpace(f.Source.Name) == "" {
		return nil, fmt.Errorf("source.name is required")
	}
	if strings.TrimSpace(f.Source.AccessBasis) == "" {
		return nil, fmt.Errorf("source.access_basis is required: state where the values come from and why you may use them")
	}
	if len(f.Observations) == 0 {
		return nil, fmt.Errorf("the import has no observations")
	}

	imp := &Import{
		Info: sources.Info{
			Code: f.Source.Code, Name: f.Source.Name, Kind: "manual", Homepage: f.Source.Homepage, URL: f.Source.URL,
			Retrieval: "manual_import", AccessBasis: f.Source.AccessBasis, License: f.Source.License, Attribution: f.Source.Attribution,
		},
		Rows: f.Observations,
	}
	for _, m := range f.Metrics {
		def := metrics.Definition{
			Key: m.Key, DisplayName: m.DisplayName, Description: m.Description, ValueKind: m.ValueKind, Unit: m.Unit,
			Direction: m.Direction, Scope: metrics.ScopeEvidence, DefinedBy: f.Source.Code, MissingPossible: true, Status: metrics.StatusCurrent,
		}
		if err := def.Validate(); err != nil {
			return nil, err
		}
		imp.Metrics = append(imp.Metrics, def)
	}
	for _, c := range f.EvaluationContexts {
		switch c.Kind {
		case sources.ContextDevin, sources.ContextExternalHarness, sources.ContextDirect:
		default:
			return nil, fmt.Errorf("evaluation context %s: kind must be devin, external_harness or direct", c.Code)
		}
		if !metrics.ValidKey(c.Code) || c.Name == "" {
			return nil, fmt.Errorf("evaluation context %q needs a lowercase code and a name", c.Code)
		}
		imp.Contexts = append(imp.Contexts, c)
	}
	return imp, nil
}

// Observations converts the rows using the resolved metric definitions and
// known contexts. Any invalid row fails the whole import: a partial manual
// import would silently drop what the user asked to record.
func (imp *Import) Observations(defs map[string]metrics.Definition, contexts map[string]bool) (sources.Result, error) {
	res := sources.Result{SourceRowCount: len(imp.Rows)}
	for i, row := range imp.Rows {
		where := fmt.Sprintf("observations[%d]", i)
		def, ok := defs[row.Metric]
		if !ok {
			return sources.Result{}, fmt.Errorf("%s: unknown metric %q (define it under metrics)", where, row.Metric)
		}
		if def.Scope != metrics.ScopeEvidence {
			return sources.Result{}, fmt.Errorf("%s: %s is published by the Devin dataset and cannot be imported", where, row.Metric)
		}
		if strings.TrimSpace(row.Model) == "" {
			return sources.Result{}, fmt.Errorf("%s: model is required", where)
		}
		if !contexts[row.EvaluationContext] {
			return sources.Result{}, fmt.Errorf("%s: unknown evaluation_context %q (define it under evaluation_contexts)", where, row.EvaluationContext)
		}
		value, err := convert(def, row.Value)
		if err != nil {
			return sources.Result{}, fmt.Errorf("%s: %w", where, err)
		}

		variant := identity.Classify(identity.NormaliseIdentifier(row.Model))
		effort := variant.Effort
		if row.Effort != "" {
			parsed, ok := identity.ParseEffort(row.Effort)
			if !ok {
				return sources.Result{}, fmt.Errorf("%s: effort %q is not a known level", where, row.Effort)
			}
			effort = parsed
		}
		serving := variant.Serving
		switch strings.ToLower(row.ServingVariant) {
		case "":
		case "fast":
			serving = identity.ServingFast
		case "standard":
			serving = identity.ServingUnspecified
		default:
			return sources.Result{}, fmt.Errorf("%s: serving_variant must be fast or standard", where)
		}

		var details map[string]string
		if row.Note != "" {
			details = map[string]string{"note": row.Note}
		}
		res.AcceptedRows++
		res.Observations = append(res.Observations, sources.Observation{
			Metric: row.Metric, SourceModelName: row.Model, SourceRowRef: where,
			BaseKey: variant.BaseKey(), Effort: effort, Serving: serving, ContextVariant: variant.ContextVariant,
			EvaluationContext: row.EvaluationContext, Value: value, Details: details,
		})
	}
	return res, res.AccountingError(imp.Info.Code)
}

func convert(def metrics.Definition, v any) (sources.Value, error) {
	switch def.ValueKind {
	case metrics.KindNumber, metrics.KindInteger:
		f, ok := v.(float64)
		if !ok {
			return sources.Value{}, fmt.Errorf("%s needs a numeric value, got %v", def.Key, v)
		}
		// encoding/json rejects the NaN and Infinity literals, and a decimal
		// too large for a float64 as well, so this guard is belt and braces —
		// but a stored non-finite value would compare false against every
		// criterion and sort unpredictably, which is worth refusing outright
		// rather than relying on the decoder.
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return sources.Value{}, fmt.Errorf("%s needs a finite number, got %v", def.Key, f)
		}
		if def.ValueKind == metrics.KindInteger {
			// Range-check before converting: converting an out-of-range float
			// to int64 is implementation-defined in Go, so the whole-number
			// comparison alone would rest on how the target happens to behave.
			// 2^63 is exactly representable as a float64, so [-2^63, 2^63) is
			// exactly the int64 range and these bounds lose nothing.
			const twoTo63 = 1 << 63
			if f < -twoTo63 || f >= twoTo63 || f != float64(int64(f)) {
				return sources.Value{}, fmt.Errorf("%s needs a whole number, got %v", def.Key, f)
			}
		}
		return sources.NumberValue(f), nil
	case metrics.KindBoolean:
		b, ok := v.(bool)
		if !ok {
			return sources.Value{}, fmt.Errorf("%s needs true or false, got %v", def.Key, v)
		}
		return sources.BoolValue(b), nil
	case metrics.KindText:
		s, ok := v.(string)
		if !ok {
			return sources.Value{}, fmt.Errorf("%s needs a string value, got %v", def.Key, v)
		}
		return sources.TextValue(s), nil
	}
	return sources.Value{}, fmt.Errorf("%s has unsupported value kind %s", def.Key, def.ValueKind)
}
