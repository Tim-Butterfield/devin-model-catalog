// Package sources defines the contract every metric collector satisfies.
//
// Collectors are explicit: each supported source is implemented in code with
// its own retrieval, parsing, identity mapping and access basis. What they
// produce is generic — normalized observations of generically defined metrics —
// so storage and querying never depend on which collector, or which retrieval
// mechanism, produced a value.
//
// The accounting invariant: every row a source publishes ends as at least one
// accepted observation or exactly one recorded rejection. Nothing disappears
// silently, and a row is rejected only when this version cannot represent it
// faithfully — never merely because one of its labels is new.
package sources

import (
	"context"
	"fmt"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/identity"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/metrics"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/retrieval"
)

// Info describes a source and the basis on which it is collected.
type Info struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // "builtin" or "manual"
	Homepage string `json:"homepage,omitempty"`
	URL      string `json:"url,omitempty"`

	// Retrieval names the mechanism: http_markdown, http_csv_zip, http_csv,
	// browser_dom, manual_import, ...
	Retrieval string `json:"retrieval"`

	// AccessBasis is the documented rationale for collecting this source the
	// way this collector does. A source without a defensible basis is not
	// implemented.
	AccessBasis string `json:"access_basis"`
	License     string `json:"license,omitempty"`
	Attribution string `json:"attribution,omitempty"`
}

// ContextKind classifies where an observation's value was measured.
//
// The kinds are a closed set owned by this project, not by any source: a kind
// decides whether a value is a proxy, so a collector chooses among these and
// never invents one from source text.
type ContextKind string

const (
	// ContextDevin is evidence measured with Devin itself as the harness.
	ContextDevin ContextKind = "devin"
	// ContextPublished is a fact published by the catalog owner (a price).
	ContextPublished ContextKind = "published"
	// ContextExternalHarness is a score produced under another agent harness.
	ContextExternalHarness ContextKind = "external_harness"
	// ContextDirect is a direct model evaluation with no agent harness.
	ContextDirect ContextKind = "direct"
)

// ValidContextKind reports whether kind is one this project defines.
func ValidContextKind(kind ContextKind) bool {
	switch kind {
	case ContextDevin, ContextPublished, ContextExternalHarness, ContextDirect:
		return true
	}
	return false
}

// EvaluationContext is a context an observation may carry. Its Code is a
// stable identifier callers filter on; its Name is the source's own spelling.
type EvaluationContext struct {
	Code string      `json:"code"`
	Name string      `json:"name"`
	Kind ContextKind `json:"kind"`
}

// Value is a single observed value. Exactly one field is set.
type Value struct {
	Number *float64 `json:"number,omitempty"`
	Bool   *bool    `json:"bool,omitempty"`
	Text   *string  `json:"text,omitempty"`
}

// NumberValue builds a numeric Value.
func NumberValue(f float64) Value { return Value{Number: &f} }

// BoolValue builds a boolean Value.
func BoolValue(b bool) Value { return Value{Bool: &b} }

// TextValue builds a text Value.
func TextValue(s string) Value { return Value{Text: &s} }

// Set reports whether exactly one field is populated.
func (v Value) Set() bool {
	n := 0
	for _, ok := range []bool{v.Number != nil, v.Bool != nil, v.Text != nil} {
		if ok {
			n++
		}
	}
	return n == 1
}

// Any returns the populated value as a plain Go value.
func (v Value) Any() any {
	switch {
	case v.Number != nil:
		return *v.Number
	case v.Bool != nil:
		return *v.Bool
	case v.Text != nil:
		return *v.Text
	}
	return nil
}

// Observation is one normalized value a source published.
type Observation struct {
	Metric string

	// SourceModelName is the raw published model name, verbatim.
	SourceModelName string
	// SourceRowRef locates the published row ("deepswe_external.csv:12").
	SourceRowRef string

	// CatalogUID binds a catalog-scope observation to a Devin model_uid.
	// Evidence-scope observations leave it empty and are matched by identity.
	CatalogUID string

	BaseKey        string
	Effort         identity.Effort
	Serving        identity.Serving
	ContextVariant string

	EvaluationContext string
	Value             Value
	Details           map[string]string
}

// RejectedRow is a published row this version could not represent faithfully,
// with the exact reason. It is not a holding pen for data whose labels are
// merely unfamiliar: a row lands here only when something it must carry — a
// model identity, a numeric score — is absent or unusable.
type RejectedRow struct {
	SourceModelName string
	SourceRowRef    string
	Reason          string
	Details         map[string]string
}

// Result is a collector's complete, self-accounting output.
type Result struct {
	// SourceRowCount is the number of rows the source published.
	SourceRowCount int
	// AcceptedRows is the number of rows that produced observations.
	AcceptedRows int

	Observations []Observation
	Rejected     []RejectedRow

	// Contexts lists every evaluation context these observations use,
	// including any the collector derived from a source-provided label during
	// this run. The refresh records them before the observations that cite
	// them, so a new upstream harness or mode needs no release.
	Contexts []EvaluationContext

	Warnings []string
}

// AddContext records a context this run uses, ignoring a repeat of one already
// recorded. It keeps the first spelling seen for a code, so callers that want a
// deterministic name across rows must present them in a deterministic order.
func (r *Result) AddContext(ec EvaluationContext) {
	for _, existing := range r.Contexts {
		if existing.Code == ec.Code {
			return
		}
	}
	r.Contexts = append(r.Contexts, ec)
}

// AccountingError reports rows that went missing between publication and
// the result.
func (r Result) AccountingError(code string) error {
	if r.AcceptedRows+len(r.Rejected) == r.SourceRowCount {
		return nil
	}
	return fmt.Errorf("source %s published %d rows but accounted for %d (%d accepted, %d rejected): rows may not disappear silently",
		code, r.SourceRowCount, r.AcceptedRows+len(r.Rejected), r.AcceptedRows, len(r.Rejected))
}

// Env is what a collector may use to retrieve its source.
type Env struct {
	HTTP retrieval.Fetcher
	// Browser provides a JavaScript-capable page session, provisioned on first
	// use. It may be nil when no runtime directory is configured; a
	// browser-backed collector must fail clearly in that case.
	Browser retrieval.Browser
}

// Collector is one explicitly supported evidence source.
type Collector interface {
	Info() Info
	Metrics() []metrics.Definition
	// Contexts lists the contexts this collector is known to emit ahead of any
	// collection, so they exist before the first refresh. A collection may
	// additionally derive contexts from source-provided labels and return them
	// in Result.Contexts; this list is not exhaustive.
	Contexts() []EvaluationContext
	Collect(ctx context.Context, env Env) (Result, error)
}
