// Package devin reads Devin's published model catalog.
//
// The source is https://docs.devin.ai/desktop/models.md, the Markdown
// alternate of the public models documentation page, listed in the site's
// /llms.txt index. It embeds the catalog as a JSON array (modelCostData) and
// presents it through MDX tabs, one per licensing dataset.
//
// Two separate concerns live here:
//
//   - Dataset discovery reads the tabs. A dataset's identity is structural —
//     the component that renders it and that component's attributes, such as
//     tier="TEAMS_TIER_PRO" — not its display title, which Cognition may
//     rename. A tab this version cannot interpret is reported as unsupported
//     rather than dropped.
//
//   - Catalog building turns the selected dataset's rows into catalog models,
//     with an explicit disposition for every row, and emits catalog-scope
//     observations (prices, recommendation flag, long-context rates).
package devin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/identity"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/metrics"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources"
)

// SourceCode identifies the Devin source.
const SourceCode = "devin"

// ModelsURL is the Markdown alternate of the models documentation page.
const ModelsURL = "https://docs.devin.ai/desktop/models.md"

// PublishedContext is the evaluation context of values Devin itself publishes.
const PublishedContext = "devin_published"

// supportedComponent renders a dataset this version can interpret.
const supportedComponent = "ModelCosts"

// supportedData is the array the supported component reads.
const supportedData = "modelCostData"

// Info describes the Devin source.
func Info() sources.Info {
	return sources.Info{
		Code:      SourceCode,
		Name:      "Devin models documentation",
		Kind:      "builtin",
		Homepage:  "https://docs.devin.ai/desktop/models",
		URL:       ModelsURL,
		Retrieval: "http_markdown",
		AccessBasis: "Public documentation page read through its Markdown alternate (/desktop/models.md), " +
			"which the site lists in its /llms.txt index for automated consumers. robots.txt does not disallow " +
			"the path and the site's Content-Signal permits ai-input. Fetched only when the user runs a refresh.",
		License:     "No license stated; factual catalog and price data read for the user's local reference.",
		Attribution: "Model names, availability and prices as published by Cognition AI, Inc. at docs.devin.ai.",
	}
}

// PublishedContextDef is the context carried by catalog-scope observations.
func PublishedContextDef() sources.EvaluationContext {
	return sources.EvaluationContext{Code: PublishedContext, Name: "Published by Devin", Kind: sources.ContextPublished}
}

// Metric keys emitted by the catalog.
const (
	MetricInputPrice        = "devin_input_price_usd_per_mtok"
	MetricOutputPrice       = "devin_output_price_usd_per_mtok"
	MetricCacheReadPrice    = "devin_cache_read_price_usd_per_mtok"
	MetricCacheWritePrice   = "devin_cache_write_price_usd_per_mtok"
	MetricRecommended       = "devin_recommended"
	MetricLCThreshold       = "devin_long_context_threshold_tokens"
	MetricLCInputPrice      = "devin_long_context_input_price_usd_per_mtok"
	MetricLCOutputPrice     = "devin_long_context_output_price_usd_per_mtok"
	MetricLCCacheReadPrice  = "devin_long_context_cache_read_price_usd_per_mtok"
	MetricLCCacheWritePrice = "devin_long_context_cache_write_price_usd_per_mtok"
)

// MetricDefinitions lists the catalog-scope metrics.
func MetricDefinitions() []metrics.Definition {
	price := func(key, name, desc string) metrics.Definition {
		return metrics.Definition{
			Key: key, DisplayName: name, Description: desc,
			ValueKind: metrics.KindNumber, Unit: "USD per 1M tokens", Direction: metrics.LowerIsBetter,
			Scope: metrics.ScopeCatalog, DefinedBy: SourceCode, MissingPossible: true, Status: metrics.StatusCurrent,
		}
	}
	// A zero or absent rate is not recorded (the page renders it as a dash).
	// describe_available_data states that rule once for all catalog metrics
	// rather than repeating it in each description.
	return []metrics.Definition{
		price(MetricInputPrice, "Devin input price", "Published standard input-token rate for this model in the selected Devin dataset."),
		price(MetricOutputPrice, "Devin output price", "Published standard output-token rate for this model in the selected Devin dataset."),
		price(MetricCacheReadPrice, "Devin cache-read price", "Published cache-read (cached input) token rate in the selected Devin dataset."),
		price(MetricCacheWritePrice, "Devin cache-write price", "Published cache-write token rate in the selected Devin dataset."),
		{
			Key: MetricRecommended, DisplayName: "Devin recommended", ValueKind: metrics.KindBoolean, Direction: metrics.Neutral,
			Description: "Whether the selected Devin dataset lists this model as recommended (is_recommended in token-priced tables, the recommended property in the legacy credits table). It is Cognition's editorial flag, not a benchmark.",
			Scope:       metrics.ScopeCatalog, DefinedBy: SourceCode, MissingPossible: true, Status: metrics.StatusCurrent,
		},
		{
			Key: MetricLCThreshold, DisplayName: "Devin long-context threshold", ValueKind: metrics.KindInteger, Unit: "prompt tokens", Direction: metrics.Neutral,
			Description: "Prompt size above which Devin publishes a separate long-context rate set for this model's family. Absent when no long-context regime is published. It is a pricing threshold, not the model's context window.",
			Scope:       metrics.ScopeCatalog, DefinedBy: SourceCode, MissingPossible: true, Status: metrics.StatusCurrent,
		},
		price(MetricLCInputPrice, "Devin long-context input price", "Published input rate that applies above the long-context threshold."),
		price(MetricLCOutputPrice, "Devin long-context output price", "Published output rate that applies above the long-context threshold."),
		price(MetricLCCacheReadPrice, "Devin long-context cache-read price", "Published cache-read rate that applies above the long-context threshold."),
		price(MetricLCCacheWritePrice, "Devin long-context cache-write price", "Published cache-write rate that applies above the long-context threshold."),
		legacyMetricDefinition(),
	}
}

// Record is one published catalog row, preserved as published.
type Record struct {
	Tier                        string   `json:"tier"`
	ModelUID                    string   `json:"model_uid"`
	Label                       string   `json:"label"`
	ModelProvider               string   `json:"model_provider"`
	InputCostPerMillionUSD      *float64 `json:"input_cost_per_million_usd"`
	OutputCostPerMillionUSD     *float64 `json:"output_cost_per_million_usd"`
	CacheWriteCostPerMillionUSD *float64 `json:"cache_write_cost_per_million_usd"`
	CacheReadCostPerMillionUSD  *float64 `json:"cache_read_cost_per_million_usd"`
	CreditMultiplier            *float64 `json:"credit_multiplier"`
	IsRecommended               *bool    `json:"is_recommended"`
}

// LongContextRates is a published long-context rate set for a model family.
type LongContextRates struct {
	Family          string   `json:"family"`
	ThresholdTokens int64    `json:"threshold_tokens"`
	InputUSD        *float64 `json:"input_usd,omitempty"`
	CacheReadUSD    *float64 `json:"cache_read_usd,omitempty"`
	OutputUSD       *float64 `json:"output_usd,omitempty"`
	CacheWriteUSD   *float64 `json:"cache_write_usd,omitempty"`
}

// Dataset is one licensing dataset (tab) the page publishes.
type Dataset struct {
	// Position is the 1-based order of the tab on the page.
	Position int `json:"position"`
	// SourceKey is the structural identity: component plus sorted attributes.
	SourceKey   string            `json:"source_key"`
	DisplayName string            `json:"display_name"`
	Component   string            `json:"component"`
	Attributes  map[string]string `json:"attributes"`

	Supported         bool   `json:"supported"`
	UnsupportedReason string `json:"unsupported_reason,omitempty"`
	ModelRowCount     int    `json:"model_row_count"`
}

// Page is a parsed models page.
type Page struct {
	Datasets []Dataset
	Records  []Record
	// RawRecords holds each record's exact JSON, index-aligned with Records.
	RawRecords []json.RawMessage

	LongContextFamilies      map[string]LongContextRates
	LongContextModelFamilies map[string]string

	// Legacy holds the rows of the legacy credits table, when it could be read.
	Legacy []LegacyEntry
	// LegacyErr explains why the legacy table could not be read.
	LegacyErr error

	Warnings []string
	SHA256   string
}

// pageFacts is what dataset description needs to know about the page.
type pageFacts struct {
	rowsByTier map[string]int
	legacyRows int
	legacyErr  error
}

var (
	exportedComponent = regexp.MustCompile(`(?m)^export const ([A-Z][A-Za-z0-9_]*)\s*=`)
	tabsOpen          = regexp.MustCompile(`<Tabs(\s[^>]*)?>`)
	tabOpen           = regexp.MustCompile(`<Tab\b[^>]*>`)
	componentTag      = regexp.MustCompile(`<([A-Z][A-Za-z0-9_]*)((?:\s+[A-Za-z_][\w-]*(?:=(?:"[^"]*"|\{[^}]*\}))?)*)\s*/?>`)
	tagAttribute      = regexp.MustCompile(`([A-Za-z_][\w-]*)=(?:"([^"]*)"|\{([^}]*)\})`)
)

// ParsePage parses the Markdown source. It fails, rather than returning a
// partial result, when any structure it depends on is missing: a truncated or
// reshaped page must never look like a smaller valid catalog.
func ParsePage(md string) (*Page, error) {
	sum := sha256.Sum256([]byte(md))
	page := &Page{SHA256: hex.EncodeToString(sum[:])}

	records, raws, err := extractModelCostData(md)
	if err != nil {
		return nil, err
	}
	page.Records, page.RawRecords = records, raws

	components := map[string]bool{}
	for _, m := range exportedComponent.FindAllStringSubmatch(md, -1) {
		components[m[1]] = true
	}
	if !components[supportedComponent] {
		return nil, fmt.Errorf("the page defines no %s component: the source shape has changed or the response is incomplete", supportedComponent)
	}

	page.LongContextFamilies = extractLongContextFamilies(md)
	page.LongContextModelFamilies = extractStringMap(md, "const longContextModelFamilies")
	// A family a model is mapped to but whose rates could not be read would
	// otherwise silently lose its long-context prices.
	var unparsed []string
	for _, family := range page.LongContextModelFamilies {
		if _, ok := page.LongContextFamilies[family]; !ok && !slices.Contains(unparsed, family) {
			unparsed = append(unparsed, family)
		}
	}
	if len(unparsed) > 0 {
		slices.Sort(unparsed)
		page.Warnings = append(page.Warnings, fmt.Sprintf("long-context rates could not be read for %s; those models have no long-context prices", strings.Join(unparsed, ", ")))
	}
	page.Warnings = append(page.Warnings, checkUserFacingFilter(md)...)
	if components[legacyComponent] {
		page.Legacy, page.LegacyErr = extractLegacyModels(md)
	}

	facts := pageFacts{rowsByTier: map[string]int{}, legacyRows: len(page.Legacy), legacyErr: page.LegacyErr}
	for _, r := range records {
		facts.rowsByTier[r.Tier]++
	}
	datasets, err := parseDatasets(md, components, facts)
	if err != nil {
		return nil, err
	}
	page.Datasets = datasets

	referenced := map[string]bool{}
	for _, d := range datasets {
		if d.Component == supportedComponent {
			referenced[d.Attributes["tier"]] = true
		}
	}
	var unreferenced []string
	for _, r := range records {
		if !referenced[r.Tier] && !slices.Contains(unreferenced, r.Tier) {
			unreferenced = append(unreferenced, r.Tier)
		}
	}
	if len(unreferenced) > 0 {
		page.Warnings = append(page.Warnings, fmt.Sprintf("catalog rows carry tiers no tab displays: %s", strings.Join(unreferenced, ", ")))
	}

	return page, nil
}

// Find returns the dataset with the given structural identity. It reports
// false when none, or more than one, matches.
func (p *Page) Find(sourceKey string) (Dataset, bool) {
	var found []Dataset
	for _, d := range p.Datasets {
		if d.SourceKey == sourceKey {
			found = append(found, d)
		}
	}
	if len(found) != 1 {
		return Dataset{}, false
	}
	return found[0], true
}

func parseDatasets(md string, components map[string]bool, facts pageFacts) ([]Dataset, error) {
	var blocks [][]Dataset
	for _, loc := range tabsOpen.FindAllStringIndex(md, -1) {
		rest := md[loc[1]:]
		end := strings.Index(rest, "</Tabs>")
		if end == -1 {
			return nil, fmt.Errorf("a <Tabs> block is never closed: the response is incomplete")
		}
		tabs := parseTabs(rest[:end], components, facts)
		hasData := slices.ContainsFunc(tabs, func(d Dataset) bool { return d.Component != "" })
		if hasData {
			blocks = append(blocks, tabs)
		}
	}

	switch len(blocks) {
	case 0:
		return nil, fmt.Errorf("no tab block renders a catalog component: the source shape has changed or the response is incomplete")
	case 1:
	default:
		return nil, fmt.Errorf("%d tab blocks render catalog components; this version cannot tell which one lists the licensing datasets", len(blocks))
	}

	datasets := blocks[0]
	counts := map[string]int{}
	for _, d := range datasets {
		counts[d.SourceKey]++
	}
	for i := range datasets {
		d := &datasets[i]
		if counts[d.SourceKey] > 1 {
			d.Supported = false
			d.UnsupportedReason = "more than one tab has this structural identity, so neither can be selected unambiguously"
			d.SourceKey = fmt.Sprintf("%s#%d", d.SourceKey, d.Position)
		}
	}
	return datasets, nil
}

func parseTabs(block string, components map[string]bool, facts pageFacts) []Dataset {
	var out []Dataset
	opens := tabOpen.FindAllStringIndex(block, -1)
	for i, loc := range opens {
		// The title may be any attribute of the tag; a tab is never dropped
		// because its markup changed order.
		title := ""
		for _, attr := range tagAttribute.FindAllStringSubmatch(block[loc[0]:loc[1]], -1) {
			if attr[1] == "title" {
				title = strings.TrimSpace(attr[2] + strings.Trim(strings.TrimSpace(attr[3]), `"'`))
			}
		}
		bodyEnd := len(block)
		if i+1 < len(opens) {
			bodyEnd = opens[i+1][0]
		}
		body := block[loc[1]:bodyEnd]
		if end := strings.Index(body, "</Tab>"); end != -1 {
			body = body[:end]
		}

		d := Dataset{Position: len(out) + 1, DisplayName: title, Attributes: map[string]string{}}
		for _, tag := range componentTag.FindAllStringSubmatch(body, -1) {
			if !components[tag[1]] {
				continue
			}
			d.Component = tag[1]
			for _, attr := range tagAttribute.FindAllStringSubmatch(tag[2], -1) {
				value := attr[2]
				if value == "" {
					value = strings.TrimSpace(attr[3])
				}
				d.Attributes[attr[1]] = value
			}
			break
		}
		describeDataset(&d, facts)
		out = append(out, d)
	}
	return out
}

func describeDataset(d *Dataset, facts pageFacts) {
	if d.Component == "" {
		d.SourceKey = "tab:" + identity.Normalise(d.DisplayName)
		d.UnsupportedReason = "the tab renders no catalog component this version recognises"
		return
	}

	keys := make([]string, 0, len(d.Attributes))
	for k := range d.Attributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := []string{d.Component}
	for _, k := range keys {
		parts = append(parts, k+"="+d.Attributes[k])
	}
	d.SourceKey = strings.Join(parts, "|")

	switch {
	case d.Component == legacyComponent:
		unit := d.Attributes["unit"]
		switch {
		case unit != "" && unit != "credits":
			d.UnsupportedReason = fmt.Sprintf("the %s component is rendered in %q units; this version only interprets the credits table", d.Component, unit)
		case facts.legacyErr != nil:
			d.UnsupportedReason = "the legacy model table could not be read: " + facts.legacyErr.Error()
		case facts.legacyRows == 0:
			d.UnsupportedReason = "the legacy model table publishes no rows"
		default:
			d.ModelRowCount = facts.legacyRows
			d.Supported = true
		}
	case d.Component != supportedComponent:
		d.UnsupportedReason = fmt.Sprintf("rendered by the %s component, which this version does not interpret", d.Component)
	case d.Attributes["data"] != supportedData:
		d.UnsupportedReason = fmt.Sprintf("the %s component reads %q rather than %s", d.Component, d.Attributes["data"], supportedData)
	case d.Attributes["tier"] == "":
		d.UnsupportedReason = "the tab shows every tier together; this version only interprets a single-tier dataset"
	default:
		d.ModelRowCount = facts.rowsByTier[d.Attributes["tier"]]
		if d.ModelRowCount == 0 {
			d.UnsupportedReason = "the page publishes no catalog rows for this tier"
			return
		}
		d.Supported = true
	}
}

// Disposition values for catalog rows. Every published row of the selected
// dataset ends as exactly one of these.
const (
	// DispositionAccepted became a catalog model.
	DispositionAccepted = "accepted"
	// DispositionExcluded was hidden by the page's own display filter, so the
	// catalog lists what the dataset tab shows. Nothing is wrong with the row.
	DispositionExcluded = "excluded"
	// DispositionRejected could not be represented faithfully, with an exact
	// reason: no model id, no label, or an identity another row also claims.
	DispositionRejected = "rejected"
)

// Model is one accepted catalog model.
type Model struct {
	UID      string           `json:"uid"`
	Label    string           `json:"label"`
	Provider string           `json:"provider"`
	Variant  identity.Variant `json:"variant"`
	RawJSON  string           `json:"-"`
}

// RowDisposition records what became of one published row.
type RowDisposition struct {
	RowRef      string `json:"row_ref"`
	UID         string `json:"model_uid"`
	Label       string `json:"label"`
	Disposition string `json:"disposition"`
	Reason      string `json:"reason,omitempty"`
}

// Catalog is the complete, self-accounting result for one dataset.
type Catalog struct {
	Dataset       Dataset
	PublishedRows int
	Models        []Model
	Dispositions  []RowDisposition
	Observations  []sources.Observation
	Warnings      []string
}

// Count returns the number of dispositions with the given value.
func (c *Catalog) Count(disposition string) int {
	n := 0
	for _, d := range c.Dispositions {
		if d.Disposition == disposition {
			n++
		}
	}
	return n
}

// BuildCatalog turns the selected dataset's rows into catalog models.
func BuildCatalog(p *Page, ds Dataset) (*Catalog, error) {
	if !ds.Supported {
		return nil, fmt.Errorf("dataset %q is not supported by this version: %s", ds.DisplayName, ds.UnsupportedReason)
	}
	if ds.Component == legacyComponent {
		return buildLegacyCatalog(p, ds)
	}
	tier := ds.Attributes["tier"]
	cat := &Catalog{Dataset: ds, Warnings: slices.Clone(p.Warnings)}

	var cands []candidate
	for i, rec := range p.Records {
		if rec.Tier != tier {
			continue
		}
		cat.PublishedRows++
		ref := fmt.Sprintf("modelCostData[%d]", i)
		reject := func(disposition, reason string) {
			cat.Dispositions = append(cat.Dispositions, RowDisposition{RowRef: ref, UID: rec.ModelUID, Label: rec.Label, Disposition: disposition, Reason: reason})
		}

		switch {
		case strings.TrimSpace(rec.ModelUID) == "":
			reject(DispositionRejected, "row has no model_uid, so it cannot be identified stably")
			continue
		case strings.TrimSpace(rec.Label) == "":
			reject(DispositionRejected, "row has no label, so no identity can be read from it")
			continue
		}
		if reason, hidden := hiddenByPage(rec); hidden {
			reject(DispositionExcluded, reason)
			continue
		}

		record := rec
		cands = append(cands, candidate{
			ref: ref, uid: rec.ModelUID, label: rec.Label,
			provider: strings.TrimPrefix(rec.ModelProvider, "MODEL_PROVIDER_"),
			variant:  identity.Classify(rec.Label), raw: string(p.RawRecords[i]),
			observations: func(v identity.Variant) []sources.Observation {
				return catalogObservations(p, tier, ref, record, v)
			},
		})
	}
	cat.settle(cands)
	if err := cat.verify(); err != nil {
		return nil, err
	}
	return cat, nil
}

// candidate is a published row that passed row-level checks and awaits the
// dataset-wide identity checks.
type candidate struct {
	ref, uid, label, provider string
	variant                   identity.Variant
	raw                       string
	observations              func(identity.Variant) []sources.Observation
}

// settle rejects rows whose uid repeats or whose label classifies to an
// identity another row also claims, and accepts the rest. Collisions are
// detected across the whole dataset, so every candidate is known first.
func (cat *Catalog) settle(cands []candidate) {
	byUID := map[string][]int{}
	byKey := map[string][]int{}
	for i, c := range cands {
		byUID[c.uid] = append(byUID[c.uid], i)
		byKey[c.variant.CanonicalKey()] = append(byKey[c.variant.CanonicalKey()], i)
	}
	for idx, c := range cands {
		disp := RowDisposition{RowRef: c.ref, UID: c.uid, Label: c.label}
		if n := len(byUID[c.uid]); n > 1 {
			disp.Disposition = DispositionRejected
			disp.Reason = fmt.Sprintf("model id %s appears %d times in this dataset; using either row would conflate them", c.uid, n)
			cat.Dispositions = append(cat.Dispositions, disp)
			continue
		}
		if peers := byKey[c.variant.CanonicalKey()]; len(peers) > 1 {
			var uids []string
			for _, p := range peers {
				if p != idx {
					uids = append(uids, cands[p].uid)
				}
			}
			disp.Disposition = DispositionRejected
			disp.Reason = fmt.Sprintf("label classifies to identity %q, shared with %s; merging distinct models would conflate them",
				c.variant.CanonicalKey(), strings.Join(uids, ", "))
			cat.Dispositions = append(cat.Dispositions, disp)
			continue
		}
		disp.Disposition = DispositionAccepted
		cat.Dispositions = append(cat.Dispositions, disp)
		cat.Models = append(cat.Models, Model{UID: c.uid, Label: c.label, Provider: c.provider, Variant: c.variant, RawJSON: c.raw})
		cat.Observations = append(cat.Observations, c.observations(c.variant)...)
	}
}

func (cat *Catalog) verify() error {
	if len(cat.Dispositions) != cat.PublishedRows {
		return fmt.Errorf("catalog accounting failed: %d rows published, %d dispositions", cat.PublishedRows, len(cat.Dispositions))
	}
	if len(cat.Models) == 0 {
		return fmt.Errorf("dataset %q produced no usable models; refusing to treat that as a valid catalog", cat.Dataset.DisplayName)
	}
	return nil
}

// hiddenByPage mirrors the page's own display filter (isUserFacing), so the
// catalog lists what the dataset tab shows.
func hiddenByPage(r Record) (string, bool) {
	switch {
	case r.ModelProvider == "MODEL_PROVIDER_UNSPECIFIED":
		return "the page hides rows whose provider is MODEL_PROVIDER_UNSPECIFIED", true
	case strings.Contains(r.Label, "[dev]"):
		return "the page hides rows labelled [dev]", true
	case strings.Contains(strings.ToLower(r.Label), "backend only"):
		return "the page hides rows labelled backend only", true
	}
	return "", false
}

var expectedFilterFragments = []string{
	"model.model_provider === 'MODEL_PROVIDER_UNSPECIFIED'",
	"model.label.includes('[dev]')",
	"includes('backend only')",
}

func checkUserFacingFilter(md string) []string {
	start := strings.Index(md, "const isUserFacing")
	if start == -1 {
		return []string{"the page no longer defines its isUserFacing display filter; this version still applies the filter rules it knows"}
	}
	end := min(len(md), start+600)
	body := md[start:end]
	for _, frag := range expectedFilterFragments {
		if !strings.Contains(body, frag) {
			return []string{"the page's isUserFacing display filter has changed; this version applies the filter rules it knows, which may no longer match the page"}
		}
	}
	return nil
}

func catalogObservations(p *Page, tier, ref string, r Record, v identity.Variant) []sources.Observation {
	base := sources.Observation{
		SourceModelName:   r.Label,
		SourceRowRef:      ref,
		CatalogUID:        r.ModelUID,
		BaseKey:           v.BaseKey(),
		Effort:            v.Effort,
		Serving:           v.Serving,
		ContextVariant:    v.ContextVariant,
		EvaluationContext: PublishedContext,
	}
	var out []sources.Observation
	add := func(metric string, value sources.Value, details map[string]string) {
		o := base
		o.Metric = metric
		o.Value = value
		o.Details = details
		out = append(out, o)
	}
	addPrice := func(metric string, rate *float64, details map[string]string) {
		if rate != nil && *rate > 0 {
			add(metric, sources.NumberValue(*rate), details)
		}
	}

	// The page renders Adaptive's enterprise prices as "*": variable per request.
	variable := r.ModelUID == "adaptive" && tier == "TEAMS_TIER_ENTERPRISE_SAAS"
	if !variable {
		addPrice(MetricInputPrice, r.InputCostPerMillionUSD, nil)
		addPrice(MetricOutputPrice, r.OutputCostPerMillionUSD, nil)
		addPrice(MetricCacheReadPrice, r.CacheReadCostPerMillionUSD, nil)
		addPrice(MetricCacheWritePrice, r.CacheWriteCostPerMillionUSD, nil)
	}
	if r.IsRecommended != nil {
		add(MetricRecommended, sources.BoolValue(*r.IsRecommended), nil)
	}

	if family, ok := p.LongContextModelFamilies[r.ModelUID]; ok {
		if rates, ok := p.LongContextFamilies[family]; ok {
			details := map[string]string{"family": family}
			add(MetricLCThreshold, sources.NumberValue(float64(rates.ThresholdTokens)), details)
			addPrice(MetricLCInputPrice, rates.InputUSD, details)
			addPrice(MetricLCOutputPrice, rates.OutputUSD, details)
			addPrice(MetricLCCacheReadPrice, rates.CacheReadUSD, details)
			addPrice(MetricLCCacheWritePrice, rates.CacheWriteUSD, details)
		}
	}
	return out
}

// extractModelCostData finds `export const modelCostData = [ ... ]` and decodes
// the bracket-matched array.
func extractModelCostData(md string) ([]Record, []json.RawMessage, error) {
	const marker = "export const modelCostData"
	start := strings.Index(md, marker)
	if start == -1 {
		return nil, nil, fmt.Errorf("modelCostData not found: the source shape has changed or the response is incomplete")
	}
	open := strings.Index(md[start:], "[")
	if open == -1 {
		return nil, nil, fmt.Errorf("modelCostData is present but has no array")
	}
	open += start
	end, err := matchBracket(md, open, '[', ']')
	if err != nil {
		return nil, nil, fmt.Errorf("modelCostData array is unterminated (the response is incomplete): %w", err)
	}

	var raws []json.RawMessage
	if err := json.Unmarshal([]byte(md[open:end+1]), &raws); err != nil {
		return nil, nil, fmt.Errorf("decoding modelCostData: %w", err)
	}
	if len(raws) == 0 {
		return nil, nil, fmt.Errorf("modelCostData is empty; refusing to treat that as a valid catalog")
	}
	records := make([]Record, len(raws))
	for i, raw := range raws {
		if err := json.Unmarshal(raw, &records[i]); err != nil {
			return nil, nil, fmt.Errorf("decoding modelCostData[%d]: %w", i, err)
		}
	}
	return records, raws, nil
}

// extractLongContextFamilies reads the long-context rate sets. Absence is not
// an error: the block is optional and a missing rate stays missing.
func extractLongContextFamilies(md string) map[string]LongContextRates {
	block, ok := objectBlock(md, "const longContextFamilies")
	if !ok {
		return nil
	}
	out := map[string]LongContextRates{}
	rest := block[1 : len(block)-1]
	for {
		nameStart := strings.IndexAny(rest, "'\"")
		if nameStart == -1 {
			break
		}
		quote := rest[nameStart]
		nameEnd := strings.IndexByte(rest[nameStart+1:], quote)
		if nameEnd == -1 {
			break
		}
		family := rest[nameStart+1 : nameStart+1+nameEnd]
		rest = rest[nameStart+1+nameEnd+1:]

		braceStart := strings.IndexByte(rest, '{')
		if braceStart == -1 {
			break
		}
		braceEnd, err := matchBracket(rest, braceStart, '{', '}')
		if err != nil {
			break
		}
		body := rest[braceStart : braceEnd+1]
		rest = rest[braceEnd+1:]

		threshold := parseTokenCount(fieldValue(body, "threshold"))
		if family == "" || threshold == 0 {
			continue
		}
		out[family] = LongContextRates{
			Family:          family,
			ThresholdTokens: threshold,
			InputUSD:        parseUSD(fieldValue(body, "input")),
			CacheReadUSD:    parseUSD(fieldValue(body, "cacheInput")),
			OutputUSD:       parseUSD(fieldValue(body, "output")),
			CacheWriteUSD:   parseUSD(fieldValue(body, "cacheWrite")),
		}
	}
	return out
}

var stringMapEntry = regexp.MustCompile(`(?m)(?:'([^']+)'|"([^"]+)"|([A-Za-z_][A-Za-z0-9_]*))\s*:\s*['"]([^'"]*)['"]`)

// extractStringMap reads a flat JS object literal of string values.
func extractStringMap(md, marker string) map[string]string {
	block, ok := objectBlock(md, marker)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, m := range stringMapEntry.FindAllStringSubmatch(block, -1) {
		key := m[1] + m[2] + m[3]
		out[key] = m[4]
	}
	return out
}

func objectBlock(md, marker string) (string, bool) {
	start := strings.Index(md, marker)
	if start == -1 {
		return "", false
	}
	open := strings.Index(md[start:], "{")
	if open == -1 {
		return "", false
	}
	open += start
	end, err := matchBracket(md, open, '{', '}')
	if err != nil {
		return "", false
	}
	return md[open : end+1], true
}

// matchBracket returns the index of the bracket closing the one at open,
// ignoring brackets inside string literals.
func matchBracket(s string, open int, opener, closer byte) (int, error) {
	depth := 0
	inString := false
	var quote byte
	for i := open; i < len(s); i++ {
		c := s[i]
		if inString {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				inString = false
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			inString = true
			quote = c
		case opener:
			depth++
		case closer:
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("no matching %c for the %c at offset %d", closer, opener, open)
}

// fieldValue returns the string literal assigned to key in an object body, or
// "" when the key is absent or its value is not a string literal. Only the
// value directly after the key counts: taking the next quote anywhere would read
// a neighbouring field's value when this one is unquoted.
func fieldValue(body, key string) string {
	at := strings.Index(body, key+":")
	if at == -1 {
		return ""
	}
	rest := strings.TrimLeft(body[at+len(key)+1:], " \t\r\n")
	if rest == "" || (rest[0] != '\'' && rest[0] != '"') {
		return ""
	}
	end := strings.IndexByte(rest[1:], rest[0])
	if end == -1 {
		return ""
	}
	return rest[1 : 1+end]
}

func parseTokenCount(s string) int64 {
	s = strings.TrimSpace(strings.ToUpper(s))
	multiplier := 1.0
	switch {
	case strings.HasSuffix(s, "M"):
		multiplier, s = 1_000_000, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "K"):
		multiplier, s = 1_000, strings.TrimSuffix(s, "K")
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || value <= 0 {
		return 0
	}
	return int64(value * multiplier)
}

func parseUSD(s string) *float64 {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$"))
	if s == "" {
		return nil
	}
	value, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &value
}
