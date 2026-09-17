package devin_test

import (
	"strings"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/devin"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/testfixtures"
)

// Page shape changes must never move a value to the wrong field or drop a
// dataset silently.

func TestUnquotedLongContextFieldIsNotReadFromItsNeighbour(t *testing.T) {
	page := strings.Replace(testfixtures.DevinPage(), "input: '$8.00',", "input: 8.00,", 1)
	p := parse(t, page)
	rates, ok := p.LongContextFamilies["GPT-5.6 Sol"]
	if !ok {
		t.Fatal("the family is still readable")
	}
	if rates.InputUSD != nil {
		t.Fatalf("an unquoted input must not take a neighbouring value, got %v", *rates.InputUSD)
	}
	if rates.OutputUSD == nil || *rates.OutputUSD != 30 {
		t.Fatalf("output rate: %v", rates.OutputUSD)
	}
}

func TestUnreadableLongContextFamilyIsWarned(t *testing.T) {
	page := strings.Replace(testfixtures.DevinPage(), "threshold: '200K',", "threshold: 200000,", 1)
	p := parse(t, page)
	if _, ok := p.LongContextFamilies["Gemini 3.1 Pro"]; ok {
		t.Fatal("fixture changed: the family should be unreadable")
	}
	if !strings.Contains(strings.Join(p.Warnings, "\n"), "Gemini 3.1 Pro") {
		t.Fatalf("a mapped family whose rates cannot be read must be warned: %v", p.Warnings)
	}
}

func TestTabTitleNeedNotBeTheFirstAttribute(t *testing.T) {
	page := strings.Replace(testfixtures.DevinPage(), `<Tab title="Self-serve">`, `<Tab id="pro" title="Self-serve">`, 1)
	if page == testfixtures.DevinPage() {
		t.Fatal("fixture changed: tab markup not found")
	}
	p := parse(t, page)
	if len(p.Datasets) != 3 || p.Datasets[0].DisplayName != "Self-serve" || p.Datasets[0].SourceKey != proKey {
		t.Fatalf("datasets: %+v", p.Datasets)
	}
}

func TestLegacyLiteralWithAChainedCallOnTheNextLineIsRejected(t *testing.T) {
	page := testfixtures.RenderDevinPageWithLegacy(testfixtures.DefaultRecords(), testfixtures.DefaultTabs(), testfixtures.DefaultLegacyModels+"\n    .map(m => m)")
	p := parse(t, page)
	if p.LegacyErr == nil || !strings.Contains(p.LegacyErr.Error(), "not a plain array literal") {
		t.Fatalf("computed allModels data must be rejected: %v", p.LegacyErr)
	}
	if d, ok := p.Find(legacyKey); ok && d.Supported {
		t.Fatalf("the legacy dataset must not be selectable: %+v", d)
	}
}

func TestLegacyNonNumericCreditsAreNotRecorded(t *testing.T) {
	legacy := strings.Replace(testfixtures.DefaultLegacyModels, `credits: "9"`, `credits: "NaN"`, 1)
	p := parse(t, testfixtures.RenderDevinPageWithLegacy(testfixtures.DefaultRecords(), testfixtures.DefaultTabs(), legacy))
	ds, ok := p.Find(legacyKey)
	if !ok {
		t.Fatal("legacy dataset missing")
	}
	cat, err := devin.BuildCatalog(p, ds)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range cat.Observations {
		if o.Metric == devin.MetricLegacyCredits && o.SourceModelName == "SWE-2 High" {
			t.Fatalf("NaN credits must not be recorded: %+v", o)
		}
	}
	if !strings.Contains(strings.Join(cat.Warnings, "\n"), `"NaN"`) {
		t.Fatalf("expected a warning naming the value: %v", cat.Warnings)
	}
}
