package devin_test

import (
	"strings"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/devin"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/identity"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/testfixtures"
)

const (
	proKey        = "ModelCosts|data=modelCostData|tier=TEAMS_TIER_PRO"
	enterpriseKey = "ModelCosts|data=modelCostData|tier=TEAMS_TIER_ENTERPRISE_SAAS"
	legacyKey     = "ModelsTable"
)

func parse(t *testing.T, md string) *devin.Page {
	t.Helper()
	p, err := devin.ParsePage(md)
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	return p
}

func TestParseDatasets(t *testing.T) {
	p := parse(t, testfixtures.DevinPage())
	if len(p.Datasets) != 3 {
		t.Fatalf("want 3 datasets, got %d: %+v", len(p.Datasets), p.Datasets)
	}
	want := []struct {
		name, key string
		supported bool
		rows      int
	}{
		{"Self-serve", proKey, true, 10},
		{"Enterprise (ACUs)", enterpriseKey, true, 5},
		{"Legacy enterprise (credits)", legacyKey, true, 7},
	}
	for i, w := range want {
		d := p.Datasets[i]
		if d.Position != i+1 || d.DisplayName != w.name || d.SourceKey != w.key || d.Supported != w.supported || d.ModelRowCount != w.rows {
			t.Errorf("dataset %d = %+v, want %+v", i, d, w)
		}
	}
	if len(p.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", p.Warnings)
	}
}

func TestUnsupportedTabsAreListedWithReasons(t *testing.T) {
	acus := testfixtures.Tab{Title: "Legacy (ACUs)", Component: `<ModelsTable unit="acus" />`}
	empty := testfixtures.Tab{Title: "Coming soon", Component: `<Note>Details to follow.</Note>`}
	p := parse(t, testfixtures.RenderDevinPage(testfixtures.DefaultRecords(), append(testfixtures.DefaultTabs(), acus, empty)))
	if len(p.Datasets) != 5 {
		t.Fatalf("datasets: %+v", p.Datasets)
	}
	a, e := p.Datasets[3], p.Datasets[4]
	if a.Supported || a.SourceKey != "ModelsTable|unit=acus" || !strings.Contains(a.UnsupportedReason, "acus") {
		t.Errorf("acus table: %+v", a)
	}
	if e.Supported || e.SourceKey != "tab:coming soon" || e.UnsupportedReason == "" {
		t.Errorf("tab without a catalog component: %+v", e)
	}
}

func TestDatasetIdentityIsStructuralNotPositionalOrTitled(t *testing.T) {
	renamed := testfixtures.EnterpriseTab
	renamed.Title = "Enterprise (ACU billing)"
	md := testfixtures.RenderDevinPage(testfixtures.DefaultRecords(), []testfixtures.Tab{renamed, testfixtures.LegacyTab, testfixtures.SelfServeTab})
	p := parse(t, md)

	ds, ok := p.Find(enterpriseKey)
	if !ok {
		t.Fatal("enterprise dataset not found by structural key after reorder and rename")
	}
	if ds.Position != 1 || ds.DisplayName != "Enterprise (ACU billing)" {
		t.Fatalf("got %+v", ds)
	}
	if _, ok := p.Find(proKey); !ok {
		t.Fatal("self-serve dataset lost after reorder")
	}
}

func TestDuplicateStructuralIdentityIsUnsupported(t *testing.T) {
	dup := testfixtures.SelfServeTab
	dup.Title = "Self-serve (copy)"
	p := parse(t, testfixtures.RenderDevinPage(testfixtures.DefaultRecords(), []testfixtures.Tab{testfixtures.SelfServeTab, dup}))
	for _, d := range p.Datasets {
		if d.Supported {
			t.Errorf("ambiguous dataset %q must not be selectable", d.DisplayName)
		}
	}
	if _, ok := p.Find(proKey); ok {
		t.Error("an ambiguous key must not be found")
	}
}

func TestParseFailsClosedOnIncompleteSource(t *testing.T) {
	full := testfixtures.DevinPage()
	cases := map[string]string{
		"cut inside the array":   full[:strings.Index(full, "claude-sonnet-4-6")],
		"cut before the tabs":    full[:strings.Index(full, "<Tabs>")],
		"cut inside the tabs":    full[:strings.Index(full, "</Tabs>")],
		"no catalog array":       strings.Replace(full, "export const modelCostData", "export const somethingElse", 1),
		"no ModelCosts":          strings.ReplaceAll(full, "ModelCosts", "PriceGrid"),
		"empty catalog":          testfixtures.RenderDevinPage([]testfixtures.Record{}, testfixtures.DefaultTabs()),
		"no data tabs":           testfixtures.RenderDevinPage(testfixtures.DefaultRecords(), nil),
		"only the markdown body": "# AI Models\n\nNothing here.\n",
	}
	for name, md := range cases {
		if _, err := devin.ParsePage(md); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestBuildCatalogAccountsForEveryRow(t *testing.T) {
	p := parse(t, testfixtures.DevinPage())
	ds, _ := p.Find(proKey)
	cat, err := devin.BuildCatalog(p, ds)
	if err != nil {
		t.Fatal(err)
	}
	if cat.PublishedRows != 10 || len(cat.Dispositions) != 10 {
		t.Fatalf("published %d, dispositions %d", cat.PublishedRows, len(cat.Dispositions))
	}
	if len(cat.Models) != 9 || cat.Count(devin.DispositionExcluded) != 1 {
		t.Fatalf("models %d excluded %d", len(cat.Models), cat.Count(devin.DispositionExcluded))
	}
	for _, m := range cat.Models {
		if m.UID == "penguin-high" {
			t.Fatal("a row the page hides must not become a catalog model")
		}
	}

	obs := map[string]map[string]any{}
	for _, o := range cat.Observations {
		if obs[o.CatalogUID] == nil {
			obs[o.CatalogUID] = map[string]any{}
		}
		obs[o.CatalogUID][o.Metric] = o.Value.Any()
	}
	if got := obs["claude-opus-5-medium"][devin.MetricOutputPrice]; got != 25.0 {
		t.Errorf("output price = %v", got)
	}
	if _, ok := obs["swe-check"][devin.MetricOutputPrice]; ok {
		t.Error("a zero price is rendered as a dash and must not be recorded as a zero observation")
	}
	if _, ok := obs["gpt-5-6-sol-medium"][devin.MetricCacheWritePrice]; ok {
		t.Error("a zero cache-write price must be missing, not zero")
	}
	if got := obs["gpt-5-6-sol-medium"][devin.MetricLCThreshold]; got != 272000.0 {
		t.Errorf("long-context threshold = %v", got)
	}
	if got := obs["gpt-5-6-sol-medium"][devin.MetricLCOutputPrice]; got != 30.0 {
		t.Errorf("long-context output = %v", got)
	}
	if _, ok := obs["claude-opus-5-medium"][devin.MetricLCThreshold]; ok {
		t.Error("a model outside any long-context family must have no threshold")
	}
	if got := obs["adaptive"][devin.MetricRecommended]; got != true {
		t.Errorf("recommended = %v", got)
	}
	if got := obs["adaptive"][devin.MetricOutputPrice]; got != 2.0 {
		t.Errorf("self-serve Adaptive has a published price, got %v", got)
	}
	for _, o := range cat.Observations {
		if o.Metric == devin.MetricLegacyCredits {
			t.Fatal("token-priced datasets publish no legacy credits")
		}
	}
}

func TestEnterpriseAdaptivePricesAreVariable(t *testing.T) {
	p := parse(t, testfixtures.DevinPage())
	ds, _ := p.Find(enterpriseKey)
	cat, err := devin.BuildCatalog(p, ds)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range cat.Observations {
		if o.CatalogUID == "adaptive" && strings.Contains(o.Metric, "price") {
			t.Errorf("enterprise Adaptive is variable-priced; got %s", o.Metric)
		}
	}
}

func TestLegacyCatalog(t *testing.T) {
	p := parse(t, testfixtures.DevinPage())
	ds, ok := p.Find(legacyKey)
	if !ok || !ds.Supported {
		t.Fatalf("legacy dataset: %+v", ds)
	}
	cat, err := devin.BuildCatalog(p, ds)
	if err != nil {
		t.Fatal(err)
	}
	if cat.PublishedRows != 7 || len(cat.Models) != 7 || len(cat.Dispositions) != 7 {
		t.Fatalf("published %d models %d dispositions %d", cat.PublishedRows, len(cat.Models), len(cat.Dispositions))
	}

	models := map[string]devin.Model{}
	for _, m := range cat.Models {
		models[m.UID] = m
	}
	opus := models["legacy:claude-opus-5-medium-thinking"]
	if opus.Label != "Claude Opus 5 (Medium Thinking)" || opus.Provider != "ANTHROPIC" ||
		opus.Variant.BaseName != "Claude Opus 5" || opus.Variant.Effort != identity.EffortMedium {
		t.Fatalf("opus: %+v", opus)
	}
	fast := models["legacy:claude-opus-5-fast-high-thinking"]
	if fast.Variant.Serving != identity.ServingFast || fast.Variant.Effort != identity.EffortHigh {
		t.Fatalf("fast: %+v", fast)
	}
	if sol := models["legacy:gpt-5.6-sol-extra-high-reasoning-fast"]; sol.Variant.Effort != identity.EffortXHigh || sol.Variant.Serving != identity.ServingFast {
		t.Fatalf("sol: %+v", sol)
	}
	if glm := models["legacy:glm-5.2-1m-no-thinking"]; glm.Variant.ContextVariant != "1M" || glm.Variant.Effort != identity.EffortNone {
		t.Fatalf("glm: %+v", glm)
	}
	if !strings.Contains(models["legacy:claude-fable-5.1-low-thinking"].RawJSON, `"hasGift":true`) ||
		!strings.Contains(opus.RawJSON, `"icon":{"js_identifier":"claudeIcon"}`) {
		t.Fatalf("raw rows must be preserved: %s", models["legacy:claude-fable-5.1-low-thinking"].RawJSON)
	}

	obs := map[string]map[string]any{}
	for _, o := range cat.Observations {
		if strings.Contains(o.Metric, "price") || strings.Contains(o.Metric, "long_context") {
			t.Fatalf("the legacy table publishes no token prices; got %s", o.Metric)
		}
		if obs[o.CatalogUID] == nil {
			obs[o.CatalogUID] = map[string]any{}
		}
		obs[o.CatalogUID][o.Metric] = o.Value.Any()
	}
	if got := obs[opus.UID][devin.MetricLegacyCredits]; got != 80.0 {
		t.Errorf("credits = %v", got)
	}
	if _, ok := obs["legacy:adaptive"][devin.MetricLegacyCredits]; ok {
		t.Error("variable (*) credits must not be recorded")
	}
	if got := obs[fast.UID][devin.MetricRecommended]; got != false {
		t.Errorf("an absent recommended property is published as not recommended, got %v", got)
	}
	if got := obs[opus.UID][devin.MetricRecommended]; got != true {
		t.Errorf("recommended = %v", got)
	}
}

func TestMalformedLegacyTableOnlyDisablesThatDataset(t *testing.T) {
	bad := strings.Replace(testfixtures.DefaultLegacyModels, `credits: "9"`, `credits: computeCredits()`, 1)
	p := parse(t, testfixtures.RenderDevinPageWithLegacy(testfixtures.DefaultRecords(), testfixtures.DefaultTabs(), bad))
	legacy, _ := p.Find(legacyKey)
	if legacy.Supported || !strings.Contains(legacy.UnsupportedReason, "not a literal") {
		t.Fatalf("legacy: %+v", legacy)
	}
	if pro, _ := p.Find(proKey); !pro.Supported {
		t.Fatal("a malformed legacy table must not affect the token-priced datasets")
	}
}

func TestLegacyParserRejectsNonLiterals(t *testing.T) {
	for name, literal := range map[string]string{
		"spread":        `[{...base, name: "X", credits: "1"}]`,
		"template":      "[{name: `X`, credits: \"1\"}]",
		"duplicate key": `[{name: "X", name: "Y"}]`,
		"trailing code": `[{name: "X"}].map(f)`,
	} {
		md := testfixtures.RenderDevinPageWithLegacy(testfixtures.DefaultRecords(), testfixtures.DefaultTabs(), literal)
		p, err := devin.ParsePage(md)
		if err != nil {
			continue // a literal that breaks bracket matching fails the page: also closed
		}
		if legacy, _ := p.Find(legacyKey); legacy.Supported {
			t.Errorf("%s: legacy dataset should be unsupported", name)
		}
	}
}

func TestIdentityCollisionIsRejected(t *testing.T) {
	records := testfixtures.DefaultRecords()
	clash := records[2] // Claude Opus 5 High
	clash.ModelUID = "claude-opus-5-high-alt"
	records = append(records, clash)
	p := parse(t, testfixtures.RenderDevinPage(records, testfixtures.DefaultTabs()))
	ds, _ := p.Find(proKey)
	cat, err := devin.BuildCatalog(p, ds)
	if err != nil {
		t.Fatal(err)
	}
	if cat.Count(devin.DispositionRejected) != 2 {
		t.Fatalf("both colliding rows must be rejected, got %d", cat.Count(devin.DispositionRejected))
	}
	for _, m := range cat.Models {
		if strings.HasPrefix(m.UID, "claude-opus-5-high") {
			t.Fatalf("colliding row %s became a model", m.UID)
		}
	}
	if len(cat.Dispositions) != cat.PublishedRows {
		t.Fatal("accounting broken")
	}
}

func TestBuildCatalogRefusesUnsupportedAndEmpty(t *testing.T) {
	acus := testfixtures.Tab{Title: "Legacy (ACUs)", Component: `<ModelsTable unit="acus" />`}
	p := parse(t, testfixtures.RenderDevinPage(testfixtures.DefaultRecords(), []testfixtures.Tab{testfixtures.SelfServeTab, acus}))
	unsupported, _ := p.Find("ModelsTable|unit=acus")
	if _, err := devin.BuildCatalog(p, unsupported); err == nil {
		t.Fatal("unsupported dataset must not build")
	}

	hidden := testfixtures.Filter(testfixtures.DefaultRecords(), func(r testfixtures.Record) bool {
		return r.Tier == testfixtures.Enterprise || r.ModelUID == "penguin-high"
	})
	p = parse(t, testfixtures.RenderDevinPage(hidden, testfixtures.DefaultTabs()))
	ds, _ := p.Find(proKey)
	if _, err := devin.BuildCatalog(p, ds); err == nil {
		t.Fatal("a dataset with no usable models must fail rather than produce an empty catalog")
	}
}

func TestChangedDisplayFilterWarns(t *testing.T) {
	md := strings.Replace(testfixtures.DevinPage(), "model.label.includes('[dev]')", "model.label.startsWith('dev:')", 1)
	p := parse(t, md)
	if len(p.Warnings) == 0 {
		t.Fatal("a changed display filter should produce a warning")
	}
}
