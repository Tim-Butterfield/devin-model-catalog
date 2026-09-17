// Package query answers factual questions about the current catalog.
//
// It never decides what a task needs. Callers state eligibility criteria,
// each with an explicit policy for missing evidence, and separately state how
// eligible models are ordered. Every returned value carries its evidence:
// where it came from, which evaluation context produced it, whether it is an
// exact match for the model variant, and what alternatives were not selected.
package query

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/identity"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/metrics"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/store"
)

// NotReadyError reports that the catalog cannot answer queries yet.
type NotReadyError struct{ Reason string }

func (e *NotReadyError) Error() string { return e.Reason }

// InvalidError reports a malformed request.
type InvalidError struct{ Reason string }

func (e *InvalidError) Error() string { return e.Reason }

func invalid(format string, args ...any) error {
	return &InvalidError{Reason: fmt.Sprintf(format, args...)}
}

// Operators accepted in criteria.
var Operators = []string{"gte", "gt", "lte", "lt", "eq", "ne", "present"}

// Missing-evidence policies.
const (
	MissingReject = "reject"
	MissingAllow  = "allow"
)

// Ambiguity policies for criteria.
const (
	AmbiguousReject = "reject"
	AmbiguousAllow  = "allow"
)

// Ambiguity reasons.
const (
	// AmbiguityPeerContexts: equally ranked observations from different
	// evaluation contexts disagree.
	AmbiguityPeerContexts = "peer_contexts"
	// AmbiguityConflictingValues: equally ranked observations from one
	// context disagree.
	AmbiguityConflictingValues = "conflicting_values"
)

// EvidencePolicy constrains which observations may supply a value.
//
// The jsonschema descriptions on this and the other request types are the MCP
// tool's wire contract, so they say what a field accepts and nothing more.
// What the values mean, and how to choose between them, belongs in AGENTS.md
// and describe_available_data, which an agent reads once rather than
// re-downloading with every tool list.
type EvidencePolicy struct {
	Contexts       []string `json:"contexts,omitempty" jsonschema:"only these context codes"`
	ContextKinds   []string `json:"context_kinds,omitempty" jsonschema:"devin, published, external_harness or direct"`
	ExactEffort    bool     `json:"exact_effort,omitempty" jsonschema:"require the model's own reasoning effort"`
	ExactServing   bool     `json:"exact_serving,omitempty" jsonschema:"require the model's own serving variant"`
	PreferContexts []string `json:"prefer_contexts,omitempty" jsonschema:"ordered codes breaking ties between equally ranked observations; otherwise they stay ambiguous"`
}

// Criterion is one eligibility requirement.
type Criterion struct {
	Metric    string         `json:"metric" jsonschema:"metric key"`
	Op        string         `json:"op" jsonschema:"gte, gt, lte, lt, eq, ne or present"`
	Value     any            `json:"value,omitempty" jsonschema:"matches the metric's kind; omit for present"`
	Missing   string         `json:"missing,omitempty" jsonschema:"required except for present: reject or allow a model with no value (missing is never zero)"`
	Ambiguous string         `json:"ambiguous,omitempty" jsonschema:"reject (default) or allow when peer values disagree about this criterion"`
	Evidence  EvidencePolicy `json:"evidence,omitzero" jsonschema:"which observations count"`
}

// OrderTerm is one ordering key. Terms apply in sequence.
type OrderTerm struct {
	Metric    string         `json:"metric" jsonschema:"metric key"`
	Direction string         `json:"direction,omitempty" jsonschema:"asc or desc; defaults to the metric's better direction, required if neutral"`
	Missing   string         `json:"missing,omitempty" jsonschema:"where models without a value sort: last (default) or first"`
	Evidence  EvidencePolicy `json:"evidence,omitzero" jsonschema:"which observations count"`
}

// Filter restricts the candidate models by catalog fields.
type Filter struct {
	UIDs            []string `json:"uids,omitempty" jsonschema:"Devin model uids"`
	Providers       []string `json:"providers,omitempty" jsonschema:"e.g. ANTHROPIC (case-insensitive)"`
	Efforts         []string `json:"efforts,omitempty" jsonschema:"none, minimal, low, medium, high, xhigh, max or unspecified"`
	ServingVariants []string `json:"serving_variants,omitempty" jsonschema:"fast or standard"`
	ContextVariants []string `json:"context_variants,omitempty" jsonschema:"e.g. 1M, or standard"`
	LabelContains   string   `json:"label_contains,omitempty" jsonschema:"case-insensitive label substring"`
}

// Request is a structured query.
type Request struct {
	Criteria            []Criterion `json:"criteria,omitempty" jsonschema:"eligibility requirements; every one must hold"`
	OrderBy             []OrderTerm `json:"order_by,omitempty" jsonschema:"applied in sequence; ties fall back to label"`
	Filter              Filter      `json:"filter,omitzero" jsonschema:"applied before criteria"`
	Limit               int         `json:"limit,omitempty" jsonschema:"models to return; default 10, maximum 500"`
	Detail              string      `json:"detail,omitempty" jsonschema:"compact (default), summary or full"`
	IncludeAlternatives bool        `json:"include_alternatives,omitempty" jsonschema:"include non-selected observations; implies detail full"`
}

// Detail projections of a query response.
const (
	DetailCompact = "compact"
	DetailSummary = "summary"
	DetailFull    = "full"
)

// Evidence flags in a compact response. Each names weaker evidence; a value
// without flags is Devin-measured or Devin-published and exact.
const (
	FlagProxy          = "proxy"
	FlagEffortInexact  = "effort_inexact"
	FlagServingInexact = "serving_inexact"
	FlagAlias          = "alias"
)

// Resolution states.
const (
	StateResolved  = "resolved"
	StateMissing   = "missing"
	StateAmbiguous = "ambiguous"
)

// Evidence is one observation as it applies to a model.
type Evidence struct {
	ObservationID     int64    `json:"observation_id"`
	Value             any      `json:"value"`
	Unit              string   `json:"unit,omitempty"`
	Source            string   `json:"source"`
	SourceModelName   string   `json:"source_model_name"`
	SourceRowRef      string   `json:"source_row_ref"`
	EvaluationContext string   `json:"evaluation_context"`
	ContextKind       string   `json:"evaluation_context_kind"`
	ObservedEffort    string   `json:"observed_effort,omitempty"`
	ObservedServing   string   `json:"observed_serving_variant,omitempty"`
	Match             string   `json:"match" jsonschema:"catalog_row (published for this exact model), identity (matched by normalized model identity) or alias (matched by a user-confirmed alias)"`
	EffortExact       bool     `json:"effort_exact"`
	ServingExact      bool     `json:"serving_exact"`
	Proxy             bool     `json:"proxy" jsonschema:"true when the value was not measured with Devin or published by Devin"`
	Caveats           []string `json:"caveats,omitempty"`
	ObservedAt        string   `json:"observed_at"`
}

// ValueRange is the numeric span of disagreeing peer observations.
type ValueRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// Resolution is the value a model has for a metric, with its evidence.
//
// Exactly one of three states holds: missing (no usable observation), resolved
// (Value and Selected set), or ambiguous (no Value; Peers lists the equally
// ranked observations that disagree).
type Resolution struct {
	State            string           `json:"state,omitempty" jsonschema:"resolved, missing or ambiguous; absent only on a compact criterion entry whose value is reported in order_by (see value_in_order_by)"`
	Missing          bool             `json:"missing,omitempty" jsonschema:"detail full only; state is canonical"`
	Ambiguous        bool             `json:"ambiguous,omitempty" jsonschema:"detail full only; state is canonical"`
	AmbiguityReason  string           `json:"ambiguity_reason,omitempty" jsonschema:"peer_contexts (different evaluation contexts disagree) or conflicting_values (one context publishes different values)"`
	Value            any              `json:"value,omitempty"`
	Context          string           `json:"context,omitempty" jsonschema:"detail compact: evaluation context code of the resolved value"`
	Flags            []string         `json:"flags,omitempty" jsonschema:"detail compact: weaker evidence behind the value (for ambiguous values, shared by all peers): proxy (not measured with or published by Devin), effort_inexact, serving_inexact, alias; absent means none applies"`
	Evidence         *EvidenceSummary `json:"evidence,omitempty" jsonschema:"detail summary: compact evidence for the value (for ambiguous values, the flags all peers share)"`
	Selected         *Evidence        `json:"selected,omitempty" jsonschema:"detail full: the selected observation"`
	SelectionBasis   string           `json:"selection_basis,omitempty"`
	PeerValues       []PeerValue      `json:"peer_values,omitempty" jsonschema:"detail compact and summary, when ambiguous: each equally ranked value with its context (and, in summary, its source)"`
	Peers            []Evidence       `json:"peers,omitempty" jsonschema:"detail full, when ambiguous: every equally ranked observation"`
	PeerContexts     []string         `json:"peer_contexts,omitempty"`
	ValueRange       *ValueRange      `json:"value_range,omitempty"`
	AlternativeCount int              `json:"alternative_count,omitempty" jsonschema:"matching observations not selected (when ambiguous: those ranked below the peers); absent means none"`
	Alternatives     []Evidence       `json:"alternatives,omitempty"`
}

// EvidenceSummary is the compact provenance of a value in a summary response.
// Source and context codes are described by describe_available_data.
type EvidenceSummary struct {
	Source       string `json:"source,omitempty"`
	Context      string `json:"evaluation_context,omitempty"`
	Proxy        bool   `json:"proxy"`
	EffortExact  bool   `json:"effort_exact"`
	ServingExact bool   `json:"serving_exact"`
	Alias        bool   `json:"alias,omitempty"`
}

// PeerValue is one disagreeing peer in a summary or compact response.
type PeerValue struct {
	Value   any    `json:"value"`
	Source  string `json:"source,omitempty" jsonschema:"detail summary only"`
	Context string `json:"evaluation_context"`
}

// summarize projects a resolution to its compact form. Semantic state, value,
// ambiguity reason, peer contexts, range and alternative count are kept; full
// observation objects, caveats and the selection basis are left to detail full
// and get_model_details.
func summarize(r Resolution) Resolution {
	// state is canonical in the summary; the missing/ambiguous booleans would
	// only repeat it.
	s := Resolution{
		State: r.State, AmbiguityReason: r.AmbiguityReason, Value: r.Value,
		PeerContexts: r.PeerContexts, ValueRange: r.ValueRange, AlternativeCount: r.AlternativeCount,
	}
	compact := func(ev Evidence, located bool) *EvidenceSummary {
		es := &EvidenceSummary{Proxy: ev.Proxy, EffortExact: ev.EffortExact, ServingExact: ev.ServingExact, Alias: ev.Match == "alias"}
		if located {
			es.Source, es.Context = ev.Source, ev.EvaluationContext
		}
		return es
	}
	switch {
	case r.Selected != nil:
		s.Evidence = compact(*r.Selected, true)
	case len(r.Peers) > 0:
		// Peers share one evidence tier, so their proxy and exactness flags agree.
		// The alias match is not part of the tier, so it is reported only when
		// every peer came through an alias.
		s.Evidence = compact(r.Peers[0], false)
		s.Evidence.Alias = !slices.ContainsFunc(r.Peers, func(p Evidence) bool { return p.Match != "alias" })
		for _, p := range r.Peers {
			s.PeerValues = append(s.PeerValues, PeerValue{Value: p.Value, Source: p.Source, Context: p.EvaluationContext})
		}
	}
	return s
}

// compactify projects a resolution to its compact form: the summary's state,
// value, ambiguity reason, value range and alternative count, with the
// evidence object reduced to its context code and the flags that mark weaker
// evidence. Source codes and peer contexts are left to detail summary and
// get_model_details.
func compactify(r Resolution) Resolution {
	s := summarize(r)
	c := Resolution{State: s.State, AmbiguityReason: s.AmbiguityReason, Value: s.Value, ValueRange: s.ValueRange, AlternativeCount: s.AlternativeCount}
	if ev := s.Evidence; ev != nil {
		c.Context = ev.Context
		if ev.Proxy {
			c.Flags = append(c.Flags, FlagProxy)
		}
		if !ev.EffortExact {
			c.Flags = append(c.Flags, FlagEffortInexact)
		}
		if !ev.ServingExact {
			c.Flags = append(c.Flags, FlagServingInexact)
		}
		if ev.Alias {
			c.Flags = append(c.Flags, FlagAlias)
		}
	}
	for _, p := range s.PeerValues {
		c.PeerValues = append(c.PeerValues, PeerValue{Value: p.Value, Context: p.Context})
	}
	return c
}

// SameEvidencePolicy reports whether two evidence policies admit and rank
// observations identically, so a metric resolves to the same value under both.
func SameEvidencePolicy(a, b EvidencePolicy) bool {
	return slices.Equal(a.Contexts, b.Contexts) && slices.Equal(a.ContextKinds, b.ContextKinds) &&
		a.ExactEffort == b.ExactEffort && a.ExactServing == b.ExactServing && slices.Equal(a.PreferContexts, b.PreferContexts)
}

// DatasetRef identifies the catalog a response describes.
type DatasetRef struct {
	SourceKey          string `json:"source_key"`
	DisplayName        string `json:"display_name"`
	CatalogState       string `json:"catalog_state"`
	CatalogRefreshedAt string `json:"catalog_refreshed_at,omitempty"`
}

// ModelRef identifies a catalog model.
type ModelRef struct {
	UID            string `json:"uid"`
	Label          string `json:"label"`
	Provider       string `json:"provider"`
	BaseName       string `json:"base_name"`
	Effort         string `json:"effort,omitempty"`
	ServingVariant string `json:"serving_variant,omitempty"`
	ContextVariant string `json:"context_variant,omitempty"`
}

// CriterionOutcome reports how a returned model satisfied a criterion.
type CriterionOutcome struct {
	Metric  string `json:"metric,omitempty" jsonschema:"omitted in detail compact, where entry i reports the response's criteria[i]"`
	Op      string `json:"op,omitempty" jsonschema:"detail full only; the caller's own criterion"`
	Target  any    `json:"target,omitempty" jsonschema:"detail full only; the caller's own criterion"`
	Outcome string `json:"outcome" jsonschema:"met; met_by_all_peers (ambiguous, but every peer value satisfies the criterion); missing_allowed; or ambiguous_allowed (peer values disagree about the criterion and the criterion allowed that)"`
	Resolution
}

// OrderValue reports a model's value for an ordering term.
type OrderValue struct {
	Metric    string `json:"metric,omitempty" jsonschema:"omitted in detail compact, where entry k reports the response's order_by[k]"`
	Direction string `json:"direction,omitempty" jsonschema:"detail full only"`
	Resolution
}

// Result is one returned model.
type Result struct {
	Rank     int                `json:"rank"`
	Model    ModelRef           `json:"model"`
	Criteria []CriterionOutcome `json:"criteria,omitempty"`
	Order    []OrderValue       `json:"order,omitempty"`
}

// Exclusion counts the models one criterion excluded for one reason. Every
// criterion is evaluated, so a model failing several is counted under each and
// the entries can add up to more than Response.ExcludedModels.
type Exclusion struct {
	Metric string `json:"metric"`
	Op     string `json:"op"`
	Target any    `json:"target,omitempty"`
	Reason string `json:"reason" jsonschema:"missing (no usable observation and missing was reject), not_met, or ambiguous (peer values disagree about the criterion and ambiguous was reject)"`
	Models int    `json:"models"`
}

// Ambiguity summarises, per metric, the models whose value could not be
// resolved to one observation, so a caller can constrain contexts and ask
// again.
type Ambiguity struct {
	Metric   string   `json:"metric"`
	Models   int      `json:"models"`
	Contexts []string `json:"peer_contexts"`
	// Reasons lists the ambiguity reasons seen: peer_contexts can be resolved
	// by choosing a context; conflicting_values (one context publishing
	// different values) cannot.
	Reasons []string `json:"reasons"`
}

// CriterionTerm is a normalized criterion as listed in a compact response.
type CriterionTerm struct {
	Criterion
	ValueInOrderBy *int `json:"value_in_order_by,omitempty" jsonschema:"set when this criterion's metric and evidence policy equal order_by[value_in_order_by]: the value is identical, so each result's criteria entry carries only its outcome and the value is in that order entry"`
}

// Response is a query result.
type Response struct {
	Dataset        DatasetRef      `json:"dataset"`
	Detail         string          `json:"detail" jsonschema:"the projection used: compact, summary or full"`
	CatalogModels  int             `json:"catalog_models"`
	MatchedFilter  int             `json:"matched_filter"`
	Eligible       int             `json:"eligible"`
	ExcludedModels int             `json:"excluded_models"`
	Returned       int             `json:"returned"`
	Criteria       []CriterionTerm `json:"criteria,omitempty" jsonschema:"detail compact: the normalized criteria; each result's criteria[i] reports criteria[i]"`
	OrderBy        []OrderTerm     `json:"order_by,omitempty" jsonschema:"detail compact: the normalized order_by terms; each result's order[k] reports order_by[k]"`
	Results        []Result        `json:"results"`
	Exclusions     []Exclusion     `json:"exclusions,omitempty"`
	Ambiguities    []Ambiguity     `json:"ambiguities,omitempty" jsonschema:"metrics for which some considered models have disagreeing peer observations; constrain evidence.contexts or set evidence.prefer_contexts to resolve them"`
	Notes          []string        `json:"notes,omitempty"`
}

// Engine evaluates queries over a loaded snapshot.
type Engine struct {
	snap *store.Snapshot
	// evidence indexes evidence-scope observations by metric then effective
	// base key (after aliases).
	evidence map[string]map[string][]indexed
	// catalog indexes catalog-scope observations by uid then metric.
	catalog map[string]map[string][]*store.ObservationRow
}

type indexed struct {
	row     *store.ObservationRow
	baseKey string
	effort  string
	match   string
}

// NewEngine indexes a snapshot.
func NewEngine(snap *store.Snapshot) *Engine {
	e := &Engine{
		snap:     snap,
		evidence: map[string]map[string][]indexed{},
		catalog:  map[string]map[string][]*store.ObservationRow{},
	}
	aliases := map[string]store.Alias{}
	for _, a := range snap.Aliases {
		aliases[a.SourceCode+"|"+a.SourceNameNormalized] = a
	}
	for i := range snap.Observations {
		o := &snap.Observations[i]
		if o.CatalogUID != "" {
			if e.catalog[o.CatalogUID] == nil {
				e.catalog[o.CatalogUID] = map[string][]*store.ObservationRow{}
			}
			e.catalog[o.CatalogUID][o.Metric] = append(e.catalog[o.CatalogUID][o.Metric], o)
			continue
		}
		ix := indexed{row: o, baseKey: o.BaseKey, effort: o.Effort, match: "identity"}
		if a, ok := aliases[o.Source+"|"+identity.Normalise(o.SourceModelName)]; ok {
			ix.baseKey, ix.match = a.BaseKey, "alias"
			if a.Effort != "" {
				ix.effort = a.Effort
			}
		}
		if e.evidence[o.Metric] == nil {
			e.evidence[o.Metric] = map[string][]indexed{}
		}
		e.evidence[o.Metric][ix.baseKey] = append(e.evidence[o.Metric][ix.baseKey], ix)
	}
	return e
}

// Ready reports whether the catalog can answer model queries.
func (e *Engine) Ready() error {
	sel := e.snap.Selection
	switch {
	case sel == nil:
		return &NotReadyError{Reason: "no Devin dataset is selected: run `devmodels datasets refresh`, then `devmodels use-dataset <id>`, then `devmodels refresh`"}
	case sel.CatalogState != store.CatalogCurrent:
		return &NotReadyError{Reason: fmt.Sprintf("the selected dataset %q has no refreshed model catalog yet: run `devmodels refresh`", sel.DisplayName)}
	}
	return nil
}

func (e *Engine) datasetRef() DatasetRef {
	sel := e.snap.Selection
	if sel == nil {
		return DatasetRef{}
	}
	return DatasetRef{SourceKey: sel.SourceKey, DisplayName: sel.DisplayName, CatalogState: sel.CatalogState, CatalogRefreshedAt: sel.CatalogRefreshedAt}
}

func modelRef(m store.CatalogModel) ModelRef {
	return ModelRef{UID: m.UID, Label: m.Label, Provider: m.Provider, BaseName: m.BaseName, Effort: m.Effort, ServingVariant: m.ServingVariant, ContextVariant: m.ContextVariant}
}

// Resolve returns a model's value for a metric under a policy.
func (e *Engine) Resolve(m store.CatalogModel, metric string, pol EvidencePolicy, includeAlternatives bool) Resolution {
	def := e.snap.Metrics[metric]
	var candidates []Evidence

	if def.Scope == metrics.ScopeCatalog {
		for _, o := range e.catalog[m.UID][metric] {
			ev := e.evidenceFor(def, o, "catalog_row", true, true, nil)
			if e.admits(pol, ev) {
				candidates = append(candidates, ev)
			}
		}
	} else {
		for _, ix := range e.evidence[metric][m.BaseKey] {
			effortExact, servingExact := true, true
			var caveats []string
			switch {
			case ix.effort == m.Effort:
			case ix.effort == "" && m.Effort != "":
				effortExact = false
				caveats = append(caveats, fmt.Sprintf("measured without a recorded reasoning effort; this model is the %s effort variant", m.Effort))
			default:
				continue
			}
			switch {
			case ix.row.Serving == m.ServingVariant:
			case ix.row.Serving == "" && m.ServingVariant == string(identity.ServingFast):
				servingExact = false
				caveats = append(caveats, "measured on the standard serving variant; this model is the Fast serving variant")
			default:
				continue
			}
			ev := e.evidenceFor(def, ix.row, ix.match, effortExact, servingExact, caveats)
			if e.admits(pol, ev) {
				candidates = append(candidates, ev)
			}
		}
	}

	if len(candidates) == 0 {
		return Resolution{State: StateMissing, Missing: true}
	}

	// Rank by evidence strength only: exact effort, exact serving variant,
	// Devin-measured or Devin-published over proxy, then the caller's explicit
	// context preference. The value itself never ranks: choosing the most
	// favourable score among incomparable harnesses would manufacture an
	// optimistic answer.
	tier := func(ev Evidence) [4]int {
		t := [4]int{1, 1, 1, len(pol.PreferContexts)}
		if ev.EffortExact {
			t[0] = 0
		}
		if ev.ServingExact {
			t[1] = 0
		}
		if ev.ContextKind == string(sources.ContextDevin) || ev.ContextKind == string(sources.ContextPublished) {
			t[2] = 0
		}
		if i := slices.Index(pol.PreferContexts, ev.EvaluationContext); i >= 0 {
			t[3] = i
		}
		return t
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := tier(candidates[i]), tier(candidates[j])
		for k := range a {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		if candidates[i].EvaluationContext != candidates[j].EvaluationContext {
			return candidates[i].EvaluationContext < candidates[j].EvaluationContext
		}
		return candidates[i].ObservationID < candidates[j].ObservationID
	})

	best := tier(candidates[0])
	top := 1
	for top < len(candidates) && tier(candidates[top]) == best {
		top++
	}
	peers := candidates[:top]
	agree := true
	for _, p := range peers[1:] {
		if compareValues(p.Value, peers[0].Value) != 0 {
			agree = false
			break
		}
	}

	if agree {
		res := Resolution{State: StateResolved, Value: peers[0].Value, Selected: &candidates[0], AlternativeCount: len(candidates) - 1}
		switch {
		case len(candidates) == 1:
			res.SelectionBasis = "the only matching observation"
		case top > 1:
			res.SelectionBasis = fmt.Sprintf("%d equally ranked observations (%s) agree on this value", top, strings.Join(contextsOf(peers), ", "))
		default:
			res.SelectionBasis = fmt.Sprintf("the strongest of %d matching observations by exact effort, exact serving variant, Devin-measured or Devin-published over proxy, and evidence.prefer_contexts; the value itself never decides", len(candidates))
		}
		if includeAlternatives {
			res.Alternatives = slices.Clone(candidates[1:])
		}
		return res
	}

	res := Resolution{
		State:            StateAmbiguous,
		Ambiguous:        true,
		Peers:            slices.Clone(peers),
		PeerContexts:     contextsOf(peers),
		AlternativeCount: len(candidates) - top,
	}
	res.AmbiguityReason = AmbiguityConflictingValues
	if len(res.PeerContexts) > 1 {
		res.AmbiguityReason = AmbiguityPeerContexts
	}
	if lo, ok := toFloat(peers[0].Value); ok {
		hi := lo
		for _, p := range peers[1:] {
			if f, ok := toFloat(p.Value); ok {
				lo, hi = min(lo, f), max(hi, f)
			}
		}
		res.ValueRange = &ValueRange{Min: lo, Max: hi}
	}
	res.SelectionBasis = fmt.Sprintf("no value selected: %d equally ranked observations disagree (%s); restrict evidence.contexts, set evidence.prefer_contexts, or reason over the peers explicitly",
		top, strings.Join(res.PeerContexts, ", "))
	if includeAlternatives {
		res.Alternatives = slices.Clone(candidates[top:])
	}
	return res
}

func contextsOf(evs []Evidence) []string {
	var out []string
	for _, ev := range evs {
		if !slices.Contains(out, ev.EvaluationContext) {
			out = append(out, ev.EvaluationContext)
		}
	}
	sort.Strings(out)
	return out
}

func (e *Engine) evidenceFor(def metrics.Definition, o *store.ObservationRow, match string, effortExact, servingExact bool, caveats []string) Evidence {
	ctx := e.snap.Contexts[o.EvaluationContext]
	ev := Evidence{
		ObservationID: o.ID, Value: typedValue(def, o), Unit: def.Unit, Source: o.Source,
		SourceModelName: o.SourceModelName, SourceRowRef: o.SourceRowRef,
		EvaluationContext: o.EvaluationContext, ContextKind: string(ctx.Kind),
		ObservedEffort: o.Effort, ObservedServing: o.Serving, Match: match,
		EffortExact: effortExact, ServingExact: servingExact, ObservedAt: o.ObservedAt,
		Caveats: caveats,
	}
	switch ctx.Kind {
	case sources.ContextExternalHarness:
		ev.Proxy = true
		ev.Caveats = append(ev.Caveats, fmt.Sprintf("measured under %s, not Devin; a proxy for performance in Devin", ctx.Name))
	case sources.ContextDirect:
		ev.Proxy = true
		ev.Caveats = append(ev.Caveats, fmt.Sprintf("direct model evaluation (%s) without an agent harness; a proxy for performance in Devin", ctx.Name))
	}
	if match == "alias" {
		ev.Caveats = append(ev.Caveats, "matched through a user-confirmed alias")
	}
	return ev
}

func (e *Engine) admits(pol EvidencePolicy, ev Evidence) bool {
	if pol.ExactEffort && !ev.EffortExact {
		return false
	}
	if pol.ExactServing && !ev.ServingExact {
		return false
	}
	if len(pol.Contexts) > 0 && !slices.Contains(pol.Contexts, ev.EvaluationContext) {
		return false
	}
	if len(pol.ContextKinds) > 0 && !slices.Contains(pol.ContextKinds, ev.ContextKind) {
		return false
	}
	return true
}

func typedValue(def metrics.Definition, o *store.ObservationRow) any {
	switch {
	case o.Text != nil:
		return *o.Text
	case o.Number == nil:
		return nil
	case def.ValueKind == metrics.KindBoolean:
		return *o.Number != 0
	case def.ValueKind == metrics.KindInteger:
		return int64(math.Round(*o.Number))
	default:
		return *o.Number
	}
}

// compareValues orders two values of the same kind: -1, 0 or 1.
func compareValues(a, b any) int {
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	if aok && bok {
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		}
		return 0
	}
	ab, aIsBool := a.(bool)
	bb, bIsBool := b.(bool)
	if aIsBool && bIsBool {
		switch {
		case ab == bb:
			return 0
		case !ab:
			return -1
		}
		return 1
	}
	return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// ---- Query ------------------------------------------------------------------

func (e *Engine) validatePolicy(where string, pol EvidencePolicy) error {
	for _, c := range append(slices.Clone(pol.Contexts), pol.PreferContexts...) {
		if _, ok := e.snap.Contexts[c]; !ok {
			return invalid("%s: unknown evaluation context %q; known contexts: %s", where, c, strings.Join(e.contextCodes(), ", "))
		}
	}
	for _, k := range pol.ContextKinds {
		switch sources.ContextKind(k) {
		case sources.ContextDevin, sources.ContextPublished, sources.ContextExternalHarness, sources.ContextDirect:
		default:
			return invalid("%s: unknown context kind %q; use devin, published, external_harness or direct", where, k)
		}
	}
	return nil
}

func (e *Engine) contextCodes() []string {
	var out []string
	for code := range e.snap.Contexts {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

func (e *Engine) metric(where, key string) (metrics.Definition, error) {
	def, ok := e.snap.Metrics[key]
	if !ok {
		return def, invalid("%s: unknown metric %q; known metrics: %s", where, key, strings.Join(e.snap.MetricOrder, ", "))
	}
	return def, nil
}

func (e *Engine) validateCriterion(i int, c *Criterion) (metrics.Definition, error) {
	where := fmt.Sprintf("criteria[%d]", i)
	def, err := e.metric(where, c.Metric)
	if err != nil {
		return def, err
	}
	c.Op = strings.ToLower(strings.TrimSpace(c.Op))
	c.Missing = strings.ToLower(strings.TrimSpace(c.Missing))
	switch c.Ambiguous = strings.ToLower(strings.TrimSpace(c.Ambiguous)); c.Ambiguous {
	case "":
		c.Ambiguous = AmbiguousReject
	case AmbiguousReject, AmbiguousAllow:
	default:
		return def, invalid("%s: ambiguous must be reject or allow, not %q", where, c.Ambiguous)
	}
	if !slices.Contains(Operators, c.Op) {
		return def, invalid("%s: unknown op %q; use one of %s", where, c.Op, strings.Join(Operators, ", "))
	}
	if c.Op == "present" {
		if c.Value != nil {
			return def, invalid("%s: op present takes no value", where)
		}
		if c.Missing == MissingAllow {
			return def, invalid("%s: op present with missing allow would accept every model", where)
		}
		c.Missing = MissingReject
		return def, e.validatePolicy(where, c.Evidence)
	}
	switch c.Missing {
	case MissingReject, MissingAllow:
	case "":
		return def, invalid("%s: missing is required (reject or allow): decide whether a model with no %s observation stays eligible", where, c.Metric)
	default:
		return def, invalid("%s: missing must be reject or allow, not %q", where, c.Missing)
	}
	if c.Value == nil {
		return def, invalid("%s: op %s needs a value", where, c.Op)
	}
	switch def.ValueKind {
	case metrics.KindNumber, metrics.KindInteger:
		f, ok := toFloat(c.Value)
		if !ok {
			return def, invalid("%s: %s is numeric, so value must be a number, not %v", where, c.Metric, c.Value)
		}
		// NaN compares as equal to everything here and cannot be encoded as JSON.
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return def, invalid("%s: value must be a finite number, not %v", where, f)
		}
		c.Value = f
	case metrics.KindBoolean:
		if _, ok := c.Value.(bool); !ok {
			return def, invalid("%s: %s is boolean, so value must be true or false", where, c.Metric)
		}
		if c.Op != "eq" && c.Op != "ne" {
			return def, invalid("%s: boolean metric %s supports only eq and ne", where, c.Metric)
		}
	case metrics.KindText:
		if _, ok := c.Value.(string); !ok {
			return def, invalid("%s: %s is text, so value must be a string", where, c.Metric)
		}
		if c.Op != "eq" && c.Op != "ne" {
			return def, invalid("%s: text metric %s supports only eq and ne", where, c.Metric)
		}
	}
	return def, e.validatePolicy(where, c.Evidence)
}

func (e *Engine) validateOrder(i int, o *OrderTerm) (metrics.Definition, error) {
	where := fmt.Sprintf("order_by[%d]", i)
	def, err := e.metric(where, o.Metric)
	if err != nil {
		return def, err
	}
	o.Direction = strings.ToLower(strings.TrimSpace(o.Direction))
	o.Missing = strings.ToLower(strings.TrimSpace(o.Missing))
	switch o.Direction {
	case "asc", "desc":
	case "":
		switch def.Direction {
		case metrics.HigherIsBetter:
			o.Direction = "desc"
		case metrics.LowerIsBetter:
			o.Direction = "asc"
		default:
			return def, invalid("%s: %s is neutral, so direction (asc or desc) is required", where, o.Metric)
		}
	default:
		return def, invalid("%s: direction must be asc or desc, not %q", where, o.Direction)
	}
	switch o.Missing {
	case "":
		o.Missing = "last"
	case "last", "first":
	default:
		return def, invalid("%s: missing must be last or first, not %q", where, o.Missing)
	}
	return def, e.validatePolicy(where, o.Evidence)
}

func satisfies(op string, actual, target any) bool {
	c := compareValues(actual, target)
	switch op {
	case "gte":
		return c >= 0
	case "gt":
		return c > 0
	case "lte":
		return c <= 0
	case "lt":
		return c < 0
	case "eq":
		return c == 0
	case "ne":
		return c != 0
	case "present":
		return true
	}
	return false
}

func (f Filter) matches(m store.CatalogModel) bool {
	anyFold := func(list []string, value string) bool {
		if len(list) == 0 {
			return true
		}
		for _, v := range list {
			if strings.EqualFold(v, value) {
				return true
			}
		}
		return false
	}
	orDefault := func(v, def string) string {
		if v == "" {
			return def
		}
		return v
	}
	return (len(f.UIDs) == 0 || slices.Contains(f.UIDs, m.UID)) &&
		anyFold(f.Providers, m.Provider) &&
		anyFold(f.Efforts, orDefault(m.Effort, "unspecified")) &&
		anyFold(f.ServingVariants, orDefault(m.ServingVariant, "standard")) &&
		anyFold(f.ContextVariants, orDefault(m.ContextVariant, "standard")) &&
		(f.LabelContains == "" || strings.Contains(strings.ToLower(m.Label), strings.ToLower(f.LabelContains)))
}

// Filter values with a fixed vocabulary. A misspelt value would otherwise match
// no model and look like an empty answer.
var (
	filterEfforts         = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "unspecified"}
	filterServingVariants = []string{"fast", "standard"}
)

func validateFilter(f Filter) error {
	check := func(field string, values, allowed []string) error {
		for _, v := range values {
			if !slices.ContainsFunc(allowed, func(a string) bool { return strings.EqualFold(a, v) }) {
				return invalid("filter.%s: unknown value %q; use %s", field, v, strings.Join(allowed, ", "))
			}
		}
		return nil
	}
	if err := check("efforts", f.Efforts, filterEfforts); err != nil {
		return err
	}
	return check("serving_variants", f.ServingVariants, filterServingVariants)
}

// Query evaluates a request against the current catalog.
func (e *Engine) Query(req Request) (*Response, error) {
	// Validate first: a malformed request is wrong whatever the data state.
	switch {
	case req.Limit == 0:
		req.Limit = 10
	case req.Limit < 0 || req.Limit > 500:
		return nil, invalid("limit must be between 1 and 500")
	}
	switch req.Detail = strings.ToLower(strings.TrimSpace(req.Detail)); req.Detail {
	case "":
		req.Detail = DetailCompact
		if req.IncludeAlternatives {
			req.Detail = DetailFull
		}
	case DetailCompact, DetailSummary:
		if req.IncludeAlternatives {
			return nil, invalid("include_alternatives returns full observation objects, so it needs detail full (or omit detail); for one finalist, get_model_details lists every alternative")
		}
	case DetailFull:
	default:
		return nil, invalid("detail must be compact, summary or full, not %q", req.Detail)
	}
	if err := validateFilter(req.Filter); err != nil {
		return nil, err
	}
	criteria := slices.Clone(req.Criteria)
	for i := range criteria {
		if _, err := e.validateCriterion(i, &criteria[i]); err != nil {
			return nil, err
		}
	}
	order := slices.Clone(req.OrderBy)
	for i := range order {
		if _, err := e.validateOrder(i, &order[i]); err != nil {
			return nil, err
		}
	}
	if err := e.Ready(); err != nil {
		return nil, err
	}

	resp := &Response{Dataset: e.datasetRef(), Detail: req.Detail, CatalogModels: len(e.snap.Models), Results: []Result{}}
	exclusions := make([]map[string]int, len(criteria))
	for i := range exclusions {
		exclusions[i] = map[string]int{}
	}
	ambiguousModels := map[string]map[string]bool{}
	ambiguousContexts := map[string]map[string]bool{}
	ambiguousReasons := map[string]map[string]bool{}
	noteAmbiguity := func(metric, uid string, res Resolution) {
		if !res.Ambiguous {
			return
		}
		if ambiguousModels[metric] == nil {
			ambiguousModels[metric], ambiguousContexts[metric], ambiguousReasons[metric] = map[string]bool{}, map[string]bool{}, map[string]bool{}
		}
		ambiguousModels[metric][uid] = true
		ambiguousReasons[metric][res.AmbiguityReason] = true
		for _, c := range res.PeerContexts {
			ambiguousContexts[metric][c] = true
		}
	}

	var eligible []Result
	for _, m := range e.snap.Models {
		if !req.Filter.matches(m) {
			continue
		}
		resp.MatchedFilter++

		result := Result{Model: modelRef(m)}
		ok := true
		for i, c := range criteria {
			res := e.Resolve(m, c.Metric, c.Evidence, req.IncludeAlternatives)
			noteAmbiguity(c.Metric, m.UID, res)
			outcome := CriterionOutcome{Metric: c.Metric, Op: c.Op, Target: c.Value, Resolution: res}
			switch {
			case res.Missing && c.Missing == MissingAllow:
				outcome.Outcome = "missing_allowed"
			case res.Missing:
				exclusions[i]["missing"]++
				ok = false
			case res.Ambiguous:
				pass := 0
				for _, p := range res.Peers {
					if satisfies(c.Op, p.Value, c.Value) {
						pass++
					}
				}
				switch {
				case pass == len(res.Peers):
					outcome.Outcome = "met_by_all_peers"
				case pass == 0:
					exclusions[i]["not_met"]++
					ok = false
				case c.Ambiguous == AmbiguousAllow:
					outcome.Outcome = "ambiguous_allowed"
				default:
					exclusions[i]["ambiguous"]++
					ok = false
				}
			case satisfies(c.Op, res.Value, c.Value):
				outcome.Outcome = "met"
			default:
				exclusions[i]["not_met"]++
				ok = false
			}
			result.Criteria = append(result.Criteria, outcome)
		}
		if !ok {
			resp.ExcludedModels++
			continue
		}
		for _, o := range order {
			res := e.Resolve(m, o.Metric, o.Evidence, req.IncludeAlternatives)
			noteAmbiguity(o.Metric, m.UID, res)
			result.Order = append(result.Order, OrderValue{Metric: o.Metric, Direction: o.Direction, Resolution: res})
		}
		eligible = append(eligible, result)
	}

	// Resolved values sort first, ambiguous values after them, missing values
	// last (or first when requested). Ambiguous values are never placed by a
	// guessed representative value.
	const (
		stateResolved = iota
		stateAmbiguous
		stateMissing
	)
	state := func(v OrderValue) int {
		switch {
		case v.Missing:
			return stateMissing
		case v.Ambiguous:
			return stateAmbiguous
		}
		return stateResolved
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		for k, o := range order {
			a, b := eligible[i].Order[k], eligible[j].Order[k]
			if sa, sb := state(a), state(b); sa != sb {
				if o.Missing == "first" && (sa == stateMissing || sb == stateMissing) {
					return sa == stateMissing
				}
				return sa < sb
			}
			if a.Missing || a.Ambiguous {
				continue
			}
			if c := compareValues(a.Value, b.Value); c != 0 {
				if o.Direction == "desc" {
					return c > 0
				}
				return c < 0
			}
		}
		if c := strings.Compare(eligible[i].Model.Label, eligible[j].Model.Label); c != 0 {
			return c < 0
		}
		return eligible[i].Model.UID < eligible[j].Model.UID
	})

	resp.Eligible = len(eligible)
	if len(eligible) > req.Limit {
		eligible = eligible[:req.Limit]
	}
	// Projection happens here, after eligibility, ordering and the limit.
	if req.Detail == DetailCompact {
		for _, c := range criteria {
			term := CriterionTerm{Criterion: c}
			for k, o := range order {
				// Same metric and policy resolve identically, so the value is
				// reported once, in the order entry.
				if o.Metric == c.Metric && SameEvidencePolicy(o.Evidence, c.Evidence) {
					term.ValueInOrderBy = &k
					break
				}
			}
			resp.Criteria = append(resp.Criteria, term)
		}
		resp.OrderBy = order
	}
	for i := range eligible {
		eligible[i].Rank = i + 1
		switch req.Detail {
		case DetailSummary:
			for k := range eligible[i].Criteria {
				c := &eligible[i].Criteria[k]
				c.Op, c.Target, c.Resolution = "", nil, summarize(c.Resolution)
			}
			for k := range eligible[i].Order {
				o := &eligible[i].Order[k]
				o.Direction, o.Resolution = "", summarize(o.Resolution)
			}
		case DetailCompact:
			for k := range eligible[i].Criteria {
				c := &eligible[i].Criteria[k]
				if resp.Criteria[k].ValueInOrderBy != nil {
					c.Resolution = Resolution{}
				} else {
					c.Resolution = compactify(c.Resolution)
				}
				c.Metric, c.Op, c.Target = "", "", nil
			}
			for k := range eligible[i].Order {
				o := &eligible[i].Order[k]
				o.Metric, o.Direction, o.Resolution = "", "", compactify(o.Resolution)
			}
		}
	}
	resp.Results = eligible
	resp.Returned = len(eligible)

	for i, c := range criteria {
		for _, reason := range []string{"missing", "not_met", "ambiguous"} {
			if n := exclusions[i][reason]; n > 0 {
				resp.Exclusions = append(resp.Exclusions, Exclusion{Metric: c.Metric, Op: c.Op, Target: c.Value, Reason: reason, Models: n})
			}
		}
	}
	for _, key := range e.snap.MetricOrder {
		if models := ambiguousModels[key]; len(models) > 0 {
			resp.Ambiguities = append(resp.Ambiguities, Ambiguity{Metric: key, Models: len(models), Contexts: sortedKeys(ambiguousContexts[key]), Reasons: sortedKeys(ambiguousReasons[key])})
		}
	}
	if len(order) == 0 {
		resp.Notes = append(resp.Notes, "no order_by was given, so eligible models are listed by label; ordering is never inferred")
	}
	var evidenceOrder []string
	for _, o := range order {
		if e.snap.Metrics[o.Metric].Scope == metrics.ScopeEvidence && !slices.Contains(evidenceOrder, o.Metric) {
			evidenceOrder = append(evidenceOrder, o.Metric)
		}
	}
	if len(evidenceOrder) > 0 {
		resp.Notes = append(resp.Notes, fmt.Sprintf("ordering excludes nothing: for %s, ambiguous values sort after resolved values and missing values sort last (or first where requested)", strings.Join(evidenceOrder, ", ")))
	}
	switch req.Detail {
	case DetailCompact:
		resp.Notes = append(resp.Notes, "detail compact: each result's criteria[i] and order[k] report this response's criteria[i] and order_by[k]; a resolved value names its evaluation context, flags mark weaker evidence (proxy, effort_inexact, serving_inexact, alias) and no flags means none applies, and ambiguous values list peer values; call get_model_details on finalists for sources, observation rows, caveats and alternatives, or query with detail summary or full")
	case DetailSummary:
		resp.Notes = append(resp.Notes, "detail summary: each value shows its state, source, evaluation context, proxy and exactness flags, and ambiguous values list peer values; call get_model_details on finalists for observation rows, caveats, selection basis and alternatives")
	}
	if len(resp.Ambiguities) > 0 {
		resp.Notes = append(resp.Notes, "some values are ambiguous: equally ranked observations disagree and no value was chosen for them. Reason peer_contexts: different evaluation contexts disagree; restrict evidence.contexts or set evidence.prefer_contexts. Reason conflicting_values: one context publishes different values; no context choice resolves it, so inspect the peers")
	}
	return resp, nil
}

// ---- Details ------------------------------------------------------------

// MetricEvidence is a model's resolution for one metric.
type MetricEvidence struct {
	Metric      string `json:"metric"`
	DisplayName string `json:"display_name"`
	Unit        string `json:"unit,omitempty"`
	Direction   string `json:"direction"`
	Scope       string `json:"scope"`
	Resolution
}

// RejectedHint is a rejected source row whose name resembles the model. It
// answers "is evidence for this model missing because a row was thrown away?",
// which is a diagnostic question, so it appears only in detail full.
type RejectedHint struct {
	Source          string  `json:"source"`
	SourceModelName string  `json:"source_model_name"`
	Reason          string  `json:"reason"`
	Similarity      float64 `json:"similarity"`
}

// Details is what is known about one catalog model, in one of two
// projections. Both are computed from the same resolutions, so they never
// disagree: compact is a strict subset of full, never a different answer.
//
//   - compact (the default) carries everything needed to make and explain a
//     recommendation: identity, dataset, each metric's value and state, its
//     evidence's source and evaluation context, the flags that mark weaker
//     evidence, ambiguity ranges and peer values, missing metrics and
//     material caveats.
//   - full adds the diagnostic material: complete observation objects for the
//     selected value and every peer, per-observation caveats, the selection
//     basis, the published Devin row, and similarly named rejected rows.
type Details struct {
	Detail         string           `json:"detail"`
	Dataset        DatasetRef       `json:"dataset"`
	Model          ModelRef         `json:"model"`
	CanonicalKey   string           `json:"canonical_key"`
	PublishedRow   json.RawMessage  `json:"published_row,omitempty"`
	Metrics        []MetricEvidence `json:"metrics"`
	MissingMetrics []string         `json:"missing_metrics"`
	Rejected       []RejectedHint   `json:"possibly_related_rejected_rows,omitempty"`
	Caveats        []string         `json:"caveats,omitempty"`
}

// FindModel resolves a uid or an exact (case-insensitive) label.
func (e *Engine) FindModel(ref string) (store.CatalogModel, error) {
	ref = strings.TrimSpace(ref)
	for _, m := range e.snap.Models {
		if m.UID == ref {
			return m, nil
		}
	}
	var matches []store.CatalogModel
	for _, m := range e.snap.Models {
		if strings.EqualFold(m.Label, ref) {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
	default:
		var uids []string
		for _, m := range matches {
			uids = append(uids, m.UID)
		}
		return store.CatalogModel{}, invalid("label %q matches %d models; use one of their uids: %s", ref, len(matches), strings.Join(uids, ", "))
	}
	want := identity.TokenSet(ref)
	type scored struct {
		label string
		score float64
	}
	var suggestions []scored
	for _, m := range e.snap.Models {
		if s := identity.Jaccard(want, identity.TokenSet(m.Label)); s >= 0.3 {
			suggestions = append(suggestions, scored{fmt.Sprintf("%s (%s)", m.Label, m.UID), s})
		}
	}
	sort.SliceStable(suggestions, func(i, j int) bool { return suggestions[i].score > suggestions[j].score })
	var names []string
	for i := 0; i < len(suggestions) && i < 5; i++ {
		names = append(names, suggestions[i].label)
	}
	msg := fmt.Sprintf("model not found: no model in the current catalog has uid or label %q", ref)
	if len(names) > 0 {
		msg += "; similar: " + strings.Join(names, "; ")
	}
	return store.CatalogModel{}, &InvalidError{Reason: msg}
}

// Details reports what is known about a model in the requested projection:
// "compact" (the default when empty) or "full".
func (e *Engine) Details(ref, detail string, rejected []store.RejectedRow) (*Details, error) {
	switch detail = strings.ToLower(strings.TrimSpace(detail)); detail {
	case "":
		detail = DetailCompact
	case DetailCompact, DetailFull:
	default:
		return nil, invalid("detail must be compact or full, not %q", detail)
	}
	full := detail == DetailFull

	if err := e.Ready(); err != nil {
		return nil, err
	}
	m, err := e.FindModel(ref)
	if err != nil {
		return nil, err
	}
	d := &Details{Detail: detail, Dataset: e.datasetRef(), Model: modelRef(m), CanonicalKey: m.CanonicalKey, Metrics: []MetricEvidence{}, MissingMetrics: []string{}}
	if full {
		// The published row is the dataset's own JSON for this model. Its
		// priced fields are already exposed as catalog-scope metrics, so
		// compact leaves it to the diagnostic view.
		d.PublishedRow = json.RawMessage(m.RawJSON)
	}

	proxyOnly, sawEvidence := true, false
	for _, key := range e.snap.MetricOrder {
		def := e.snap.Metrics[key]
		res := e.Resolve(m, key, EvidencePolicy{}, true)
		if res.Missing {
			d.MissingMetrics = append(d.MissingMetrics, key)
			continue
		}
		sawEvidence = sawEvidence || def.Scope == metrics.ScopeEvidence
		if def.Scope == metrics.ScopeEvidence && res.Selected != nil && !res.Selected.Proxy {
			proxyOnly = false
		}
		for _, p := range res.Peers {
			if def.Scope == metrics.ScopeEvidence && !p.Proxy {
				proxyOnly = false
			}
		}
		if res.Ambiguous {
			d.Caveats = append(d.Caveats, fmt.Sprintf("%s has %d equally ranked observations that disagree (%s), so no single value is selected", key, len(res.Peers), strings.Join(res.PeerContexts, ", ")))
		}
		// summarize keeps the state, value, evidence provenance, ambiguity
		// range and peer values, and drops the observation objects, their
		// per-observation caveats and the selection basis. Nothing it keeps
		// differs from full; it simply says less.
		if !full {
			res = summarize(res)
		}
		d.Metrics = append(d.Metrics, MetricEvidence{Metric: key, DisplayName: def.DisplayName, Unit: def.Unit, Direction: string(def.Direction), Scope: string(def.Scope), Resolution: res})
	}
	if sawEvidence && proxyOnly {
		d.Caveats = append(d.Caveats, "no benchmark value for this model was measured with Devin; every benchmark value is a proxy from another harness or a direct evaluation")
	}
	if m.Effort != "" {
		d.Caveats = append(d.Caveats, "this is an effort-specific variant; values marked effort_exact=false were measured without that effort level")
	}

	if !full {
		return d, nil
	}
	want := identity.TokenSet(m.BaseKey)
	for _, r := range rejected {
		name := r.SourceModelName
		// Strictly above one half: "claude opus 4.7" against "claude opus 4.6"
		// scores exactly 0.5, and a neighbouring version is not a useful hint.
		if s := identity.Jaccard(want, identity.TokenSet(identity.NormaliseIdentifier(name))); s > 0.5 {
			d.Rejected = append(d.Rejected, RejectedHint{Source: r.Source, SourceModelName: name, Reason: r.Reason, Similarity: math.Round(s*100) / 100})
		}
	}
	sort.SliceStable(d.Rejected, func(i, j int) bool { return d.Rejected[i].Similarity > d.Rejected[j].Similarity })
	if len(d.Rejected) > 10 {
		d.Rejected = d.Rejected[:10]
	}
	if len(d.Rejected) > 0 {
		d.Caveats = append(d.Caveats, "some rejected source rows have similar names; they carry no value that could be used, and are listed for review")
	}
	return d, nil
}

// ---- Describe -------------------------------------------------------------

// Describe has the same two projections as Query (DetailSummary, DetailFull).
// Coverage and metric semantics are computed identically for both; summary
// leaves out provenance prose and lifecycle fields that initial metric
// selection does not need.

// MetricDescription is a metric with current coverage. Fields marked
// "full only" are empty in the summary projection.
type MetricDescription struct {
	Key                    string            `json:"key"`
	DisplayName            string            `json:"display_name"`
	Description            string            `json:"description"`
	ValueKind              metrics.ValueKind `json:"value_kind"`
	Unit                   string            `json:"unit,omitempty"`
	Direction              metrics.Direction `json:"direction"`
	Scope                  metrics.Scope     `json:"scope"`
	DefinedBy              string            `json:"defined_by,omitempty" jsonschema:"full only"`
	MissingPossible        *bool             `json:"missing_possible,omitempty" jsonschema:"full only"`
	Status                 metrics.Status    `json:"status,omitempty" jsonschema:"full; in summary only when not current"`
	DataPoints             *int              `json:"data_points,omitempty" jsonschema:"individual metric values stored for this metric, not models; full only"`
	CatalogModelsWithValue int               `json:"catalog_models_with_value"`
	Sources                []string          `json:"sources"`
	EvaluationContexts     []string          `json:"evaluation_contexts"`
}

// SourceDescription describes a source. Summary keeps code and name.
type SourceDescription struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Kind        string `json:"kind,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	URL         string `json:"url,omitempty"`
	Retrieval   string `json:"retrieval,omitempty"`
	AccessBasis string `json:"access_basis,omitempty"`
	License     string `json:"license,omitempty"`
	Attribution string `json:"attribution,omitempty"`
}

// ContextDescription describes an evaluation context. It is the full
// projection's form; summary states the same codes and kinds through
// Description.EvaluationContextsByKind, which repeats nothing.
type ContextDescription struct {
	Code string              `json:"code"`
	Name string              `json:"name,omitempty"`
	Kind sources.ContextKind `json:"kind"`
}

// Description reports what data exists and how to query it.
type Description struct {
	Detail         string      `json:"detail" jsonschema:"summary or full"`
	Dataset        *DatasetRef `json:"dataset,omitempty"`
	CatalogReady   bool        `json:"catalog_ready"`
	NotReadyReason string      `json:"not_ready_reason,omitempty"`
	CatalogModels  int         `json:"catalog_models"`
	// Metrics lists every metric in full detail. In summary, once the catalog
	// is ready, metrics with no value for any current model are listed only by
	// key in MetricsWithoutValues.
	Metrics              []MetricDescription `json:"metrics"`
	MetricsWithoutValues []string            `json:"metrics_without_values,omitempty" jsonschema:"summary only: metric keys with no value for any model in the selected dataset"`
	Sources              []SourceDescription `json:"sources"`
	// EvaluationContextsByKind lists every evaluation context code the listed
	// metrics use, grouped under its kind. Each metric already names its own
	// codes, so a second list of code/kind objects would restate every one of
	// them to add a single word; grouping states each kind once instead.
	EvaluationContextsByKind map[string][]string `json:"evaluation_contexts_by_kind,omitempty" jsonschema:"summary only: evaluation context codes in use, grouped by kind"`
	// EvaluationContexts names every context the catalog knows, used or not,
	// with its human name. Full only.
	EvaluationContexts []ContextDescription `json:"evaluation_contexts,omitempty" jsonschema:"full only"`
	Query              QueryHelp            `json:"query"`
	Notes              []string             `json:"notes,omitempty"`
}

// QueryHelp summarises the query contract.
type QueryHelp struct {
	Operators         []string `json:"operators"`
	MissingPolicies   []string `json:"missing_policies"`
	AmbiguousPolicies []string `json:"ambiguous_policies"`
	DetailValues      []string `json:"detail_values"`
	ContextKinds      []string `json:"context_kinds"`
	FilterFields      []string `json:"filter_fields"`
	ModelIdentifiers  string   `json:"model_identifiers,omitempty"`
	Notes             []string `json:"notes,omitempty"`
}

// Describe reports the metrics, sources and contexts currently known, in the
// requested projection ("" means summary).
func (e *Engine) Describe(detail string) (*Description, error) {
	switch detail = strings.ToLower(strings.TrimSpace(detail)); detail {
	case "":
		detail = DetailSummary
	case DetailSummary, DetailFull:
	default:
		return nil, invalid("detail must be summary or full, not %q", detail)
	}
	full := detail == DetailFull

	d := &Description{
		Detail:        detail,
		CatalogModels: len(e.snap.Models),
		Metrics:       []MetricDescription{},
		Sources:       []SourceDescription{},
		Query: QueryHelp{
			Operators:         Operators,
			MissingPolicies:   []string{MissingReject, MissingAllow},
			AmbiguousPolicies: []string{AmbiguousReject, AmbiguousAllow},
			DetailValues:      []string{DetailCompact, DetailSummary, DetailFull},
			ContextKinds:      []string{string(sources.ContextDevin), string(sources.ContextPublished), string(sources.ContextExternalHarness), string(sources.ContextDirect)},
			FilterFields:      []string{"uids", "providers", "efforts", "serving_variants", "context_variants", "label_contains"},
		},
		Notes: []string{
			"catalog-scope values a dataset shows as zero or blank are not recorded: they are missing, not zero",
		},
	}
	if full {
		d.EvaluationContexts = []ContextDescription{}
		d.Query.ModelIdentifiers = "get_model_details accepts a Devin model uid or an exact label"
		d.Query.Notes = []string{
			"criteria decide eligibility; order_by only orders eligible models",
			"every non-present criterion needs an explicit missing policy; missing is never treated as zero",
			"catalog-scope metrics are published per model by the selected Devin dataset; evidence-scope metrics come from external sources and are matched by model identity",
			"when several observations match, they are ranked by exact effort, exact serving variant, Devin-measured or Devin-published over proxy, and evidence.prefer_contexts; the value itself never ranks",
			"if the best-ranked observations disagree, the resolution is ambiguous: no value is chosen and every peer is returned; criteria use the ambiguous policy (reject by default) only when peers disagree about the criterion",
			"every criterion may set ambiguous: reject|allow; restrict evidence.contexts or set evidence.prefer_contexts to resolve an ambiguity deliberately",
		}
	} else {
		// What detail full adds is stated in AGENTS.md, which an agent reads
		// before its first call, and by `devmodels metrics` for a human; the
		// summary does not repeat it.
		d.EvaluationContextsByKind = map[string][]string{}
	}
	if err := e.Ready(); err != nil {
		d.NotReadyReason = err.Error()
	} else {
		d.CatalogReady = true
	}
	if e.snap.Selection != nil {
		ref := e.datasetRef()
		d.Dataset = &ref
	}

	obsCount := map[string]int{}
	srcs := map[string]map[string]bool{}
	ctxs := map[string]map[string]bool{}
	for _, o := range e.snap.Observations {
		obsCount[o.Metric]++
		if srcs[o.Metric] == nil {
			srcs[o.Metric], ctxs[o.Metric] = map[string]bool{}, map[string]bool{}
		}
		srcs[o.Metric][o.Source] = true
		ctxs[o.Metric][o.EvaluationContext] = true
	}
	usedContexts := map[string]bool{}
	for _, key := range e.snap.MetricOrder {
		def := e.snap.Metrics[key]
		md := MetricDescription{
			Key: def.Key, DisplayName: def.DisplayName, Description: def.Description, ValueKind: def.ValueKind,
			Unit: def.Unit, Direction: def.Direction, Scope: def.Scope,
			Sources: sortedKeys(srcs[key]), EvaluationContexts: sortedKeys(ctxs[key]),
		}
		if d.CatalogReady {
			for _, m := range e.snap.Models {
				if !e.Resolve(m, key, EvidencePolicy{}, false).Missing {
					md.CatalogModelsWithValue++
				}
			}
		}
		if full {
			missing, count := def.MissingPossible, obsCount[key]
			md.DefinedBy, md.MissingPossible, md.Status, md.DataPoints = def.DefinedBy, &missing, def.Status, &count
		} else {
			if def.Status != metrics.StatusCurrent {
				md.Status = def.Status
			}
			if d.CatalogReady && md.CatalogModelsWithValue == 0 {
				d.MetricsWithoutValues = append(d.MetricsWithoutValues, key)
				continue
			}
		}
		for _, c := range md.EvaluationContexts {
			usedContexts[c] = true
		}
		d.Metrics = append(d.Metrics, md)
	}

	for _, s := range e.snap.Sources {
		sd := SourceDescription{Code: s.Code, Name: s.Name}
		if full {
			sd.Kind, sd.Homepage, sd.URL, sd.Retrieval = s.Kind, s.Homepage, s.URL, s.Retrieval
			sd.AccessBasis, sd.License, sd.Attribution = s.AccessBasis, s.License, s.Attribution
		}
		d.Sources = append(d.Sources, sd)
	}
	sort.Slice(d.Sources, func(i, j int) bool { return d.Sources[i].Code < d.Sources[j].Code })

	for _, c := range e.snap.Contexts {
		switch {
		case full:
			d.EvaluationContexts = append(d.EvaluationContexts, ContextDescription{Code: c.Code, Name: c.Name, Kind: c.Kind})
		case usedContexts[c.Code]:
			d.EvaluationContextsByKind[string(c.Kind)] = append(d.EvaluationContextsByKind[string(c.Kind)], c.Code)
		}
	}
	sort.Slice(d.EvaluationContexts, func(i, j int) bool { return d.EvaluationContexts[i].Code < d.EvaluationContexts[j].Code })
	// Snapshot contexts are a map, so each group is ordered here; the groups
	// themselves are ordered by encoding/json, which sorts object keys.
	for _, codes := range d.EvaluationContextsByKind {
		sort.Strings(codes)
	}
	return d, nil
}

// UnmatchedEvidence returns evidence observations that match no model in the
// current catalog by base identity, in observation order. Most leaderboard rows
// describe models Devin does not offer, so a non-empty result is normal.
func (e *Engine) UnmatchedEvidence() []store.ObservationRow {
	bases := map[string]bool{}
	for _, m := range e.snap.Models {
		bases[m.BaseKey] = true
	}
	var out []store.ObservationRow
	for _, byBase := range e.evidence {
		for base, list := range byBase {
			if bases[base] {
				continue
			}
			for _, ix := range list {
				out = append(out, *ix.row)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Suggestion is a candidate catalog identity for an unresolved name.
type Suggestion struct {
	Label      string  `json:"label"`
	UID        string  `json:"uid"`
	Similarity float64 `json:"similarity"`
}

// Suggest offers catalog models whose names resemble name. Suggestions are
// for human review only; nothing is applied automatically.
func (e *Engine) Suggest(name string, max int) []Suggestion {
	want := identity.TokenSet(identity.NormaliseIdentifier(name))
	var out []Suggestion
	seen := map[string]bool{}
	for _, m := range e.snap.Models {
		if seen[m.BaseKey] {
			continue
		}
		if s := identity.Jaccard(want, identity.TokenSet(m.BaseKey)); s >= 0.4 {
			seen[m.BaseKey] = true
			out = append(out, Suggestion{Label: m.BaseName, UID: m.UID, Similarity: math.Round(s*100) / 100})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	if len(out) > max {
		out = out[:max]
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
