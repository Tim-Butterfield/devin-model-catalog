// Package testfixtures builds deterministic source documents for tests.
//
// Fixtures are synthesized in the published shapes rather than copied from the
// live sources, so tests never depend on the network and the repository never
// redistributes third-party datasets. Model names and prices are illustrative.
package testfixtures

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Record is a Devin modelCostData row.
type Record struct {
	Tier          string   `json:"tier"`
	ModelUID      string   `json:"model_uid"`
	Label         string   `json:"label"`
	ModelProvider string   `json:"model_provider"`
	Input         *float64 `json:"input_cost_per_million_usd"`
	Output        *float64 `json:"output_cost_per_million_usd"`
	CacheWrite    *float64 `json:"cache_write_cost_per_million_usd"`
	CacheRead     *float64 `json:"cache_read_cost_per_million_usd"`
	Credit        *float64 `json:"credit_multiplier"`
	Recommended   *bool    `json:"is_recommended"`
}

// Tab is one dataset tab.
type Tab struct {
	Title string
	// Component is the rendered tag, e.g. `<ModelCosts data={modelCostData} tier="TEAMS_TIER_PRO" />`.
	Component string
}

const (
	Pro        = "TEAMS_TIER_PRO"
	Enterprise = "TEAMS_TIER_ENTERPRISE_SAAS"
)

func f(v float64) *float64 { return &v }
func b(v bool) *bool       { return &v }

func rec(tier, uid, label, provider string, in, out, cw, cr float64, recommended bool) Record {
	return Record{Tier: tier, ModelUID: uid, Label: label, ModelProvider: "MODEL_PROVIDER_" + provider,
		Input: f(in), Output: f(out), CacheWrite: f(cw), CacheRead: f(cr), Credit: f(1), Recommended: b(recommended)}
}

// DefaultRecords is the standard catalog: ten self-serve rows (one hidden by
// the page's display filter) and five enterprise rows.
func DefaultRecords() []Record {
	return []Record{
		rec(Pro, "claude-opus-5-medium", "Claude Opus 5 Medium", "ANTHROPIC", 5, 25, 6.25, 0.5, true),
		rec(Pro, "claude-opus-5-medium-fast", "Claude Opus 5 Medium Fast", "ANTHROPIC", 10, 50, 12.5, 1, false),
		rec(Pro, "claude-opus-5-high", "Claude Opus 5 High", "ANTHROPIC", 5, 25, 6.25, 0.5, false),
		rec(Pro, "gpt-5-6-sol-medium", "GPT-5.6 Sol Medium Thinking", "OPENAI", 1.5, 6, 0, 0.15, true),
		rec(Pro, "gpt-5-6-sol-max", "GPT-5.6 Sol Max Thinking", "OPENAI", 1.5, 6, 0, 0.15, false),
		rec(Pro, "claude-sonnet-4-6", "Claude Sonnet 4.6", "ANTHROPIC", 3, 15, 3.75, 0.3, false),
		rec(Pro, "claude-sonnet-4-6-1m", "Claude Sonnet 4.6 1M", "ANTHROPIC", 6, 22.5, 7.5, 0.6, false),
		rec(Pro, "swe-check", "SWE-check", "WINDSURF", 0, 0, 0, 0, false),
		rec(Pro, "adaptive", "Adaptive", "WINDSURF", 0.5, 2, 0.5, 0.1, true),
		rec(Pro, "penguin-high", "Penguin High", "UNSPECIFIED", 0.5, 2.5, 0, 0.05, false),
		rec(Enterprise, "claude-opus-5-medium", "Claude Opus 5 Medium", "ANTHROPIC", 5, 25, 6.25, 0.5, true),
		rec(Enterprise, "gpt-5-6-sol-medium", "GPT-5.6 Sol Medium Thinking", "OPENAI", 1.5, 6, 0, 0.15, true),
		rec(Enterprise, "adaptive", "Adaptive", "WINDSURF", 0.5, 2, 0.5, 0.1, true),
		rec(Enterprise, "kimi-k3-high", "Kimi K3 High", "MOONSHOT", 3, 15, 0, 0.3, false),
		rec(Enterprise, "glm-5-2-max", "GLM-5.2 Max", "ZAI", 0.6, 2.2, 0, 0.11, false),
	}
}

// Filter returns the records keep accepts, in order.
func Filter(records []Record, keep func(Record) bool) []Record {
	var out []Record
	for _, r := range records {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

// SelfServeTab, EnterpriseTab and LegacyTab are the standard tabs.
var (
	SelfServeTab  = Tab{Title: "Self-serve", Component: `<ModelCosts data={modelCostData} tier="TEAMS_TIER_PRO" />`}
	EnterpriseTab = Tab{Title: "Enterprise (ACUs)", Component: `<ModelCosts data={modelCostData} tier="TEAMS_TIER_ENTERPRISE_SAAS" />`}
	LegacyTab     = Tab{Title: "Legacy enterprise (credits)", Component: `<ModelsTable />`}
)

// DefaultTabs is the standard tab order.
func DefaultTabs() []Tab { return []Tab{SelfServeTab, EnterpriseTab, LegacyTab} }

// DevinPage renders the default page.
func DevinPage() string { return RenderDevinPage(DefaultRecords(), DefaultTabs()) }

// DefaultLegacyModels is the legacy credits table's allModels literal, in the
// JavaScript shape the page publishes.
const DefaultLegacyModels = `[{
    name: "Adaptive",
    icon: windsurfIcon,
    credits: "*",
    provider: "windsurf",
    recommended: true
  }, {
    name: "SWE-2 High",
    icon: windsurfIcon,
    credits: "9",
    provider: "windsurf",
    recommended: true
  }, {
    name: "Claude Opus 5 (Medium Thinking)",
    icon: claudeIcon,
    credits: "80",
    provider: "anthropic",
    recommended: true
  }, {
    name: "Claude Opus 5 Fast (High Thinking)",
    icon: claudeIcon,
    credits: "230",
    provider: "anthropic"
  }, {
    name: "Claude Fable 5.1 (Low Thinking)",
    icon: claudeIcon,
    credits: "40",
    provider: "anthropic",
    recommended: true,
    hasGift: true
  }, {
    name: "GPT-5.6 Sol (Extra High Reasoning) Fast",
    icon: openaiIcon,
    credits: "300",
    provider: "openai"
  }, {
    name: "GLM-5.2 1M (No Thinking)",
    icon: zaiIcon,
    credits: "2",
    provider: "opensource"
  }]`

// RenderDevinPage renders a models.md document in the published shape.
func RenderDevinPage(records []Record, tabs []Tab) string {
	return RenderDevinPageWithLegacy(records, tabs, DefaultLegacyModels)
}

// RenderDevinPageWithLegacy renders a page with a specific allModels literal.
func RenderDevinPageWithLegacy(records []Record, tabs []Tab, legacyModels string) string {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		panic(err)
	}
	var sb strings.Builder
	sb.WriteString("> ## Documentation Index\n> Fetch the complete documentation index at: https://docs.devin.ai/llms.txt\n\n# AI Models\n\n")
	sb.WriteString("> Available AI models (synthetic test fixture).\n\n")
	sb.WriteString("export const modelCostData = ")
	sb.Write(data)
	sb.WriteString(";\n\n")
	sb.WriteString(`export const ModelCosts = ({data, tier}) => {
  const longContextFamilies = {
    'GPT-5.6 Sol': {
      threshold: '272K',
      input: '$8.00',
      cacheInput: '$0.80',
      output: '$30.00',
      cacheWrite: '$10.00'
    },
    'Gemini 3.1 Pro': {
      threshold: '200K',
      input: '$4.00',
      output: '$18.00'
    }
  };
  const longContextModelFamilies = {
    'gpt-5-6-sol-medium': 'GPT-5.6 Sol',
    'gpt-5-6-sol-max': 'GPT-5.6 Sol',
    MODEL_GOOGLE_GEMINI_3_1_PRO: 'Gemini 3.1 Pro'
  };
  const isUserFacing = model => {
    if (model.model_provider === 'MODEL_PROVIDER_UNSPECIFIED') return false;
    if (model.label.includes('[dev]')) return false;
    if (model.label.toLowerCase().includes('backend only')) return false;
    return true;
  };
  const tierFiltered = tier ? data.filter(m => m.tier === tier) : data;
  const visibleModels = tierFiltered.filter(isUserFacing);
  return <>
      <div className="cost-table">{visibleModels.length}</div>
    </>;
};

export const ModelsTable = ({unit = 'credits'}) => {
  const CREDITS_PER_ACU = 12.5;
  const windsurfIcon = {
    light: "https://example.invalid/windsurf.png",
    dark: "https://example.invalid/windsurf-dark.png"
  };
  const allModels = ` + legacyModels + `;
  const formatCost = credits => credits;
  return <>
      <table>{allModels.length}</table>
    </>;
};

## Recommended

<Note>
  Some models are **Devin Local only**.
</Note>

<Tabs>
`)
	for _, t := range tabs {
		fmt.Fprintf(&sb, "  <Tab title=%q>\n    Pricing for this plan.\n\n    %s\n\n    <Note>Promotional pricing may apply.</Note>\n  </Tab>\n\n", t.Title, t.Component)
	}
	sb.WriteString("</Tabs>\n\n# SWE-2, swe-grep, swe-check\n\nOur in-house models.\n")
	return sb.String()
}

// DefaultEpochFiles are the Epoch AI CSVs the collector reads.
//
// The rows are chosen to exercise every outcome the collector can reach:
//
//   - curated harnesses, including spellings that differ only in case and
//     separators ("Claude Code", "claude-code", " Codex CLI ", "Mini-SWE-Agent");
//   - harnesses no build curates ("SomeNewAgent", "Droid"), which must become
//     their own contexts rather than being discarded or folded into a
//     neighbour — and which must make Claude Opus 5's Terminal-Bench value
//     ambiguous, because Claude Code and Droid disagree about it;
//   - a row whose versioned identifier is blank but whose Name is not, which
//     is real evidence read from the fallback identity column;
//   - rows that genuinely cannot be represented: no identity at all, and a
//     score that is not a number.
func DefaultEpochFiles() map[string]string {
	return map[string]string{
		"swe_bench_verified.csv": `Model version,mean_score,Best score (across scorers),Release date,Organization
claude-opus-5_medium,0.781,0.781,2026-07-24,Anthropic
gpt-5.6-sol_max,0.802,0.802,2026-07-09,OpenAI
claude-opus-5,0.765,0.765,2026-07-24,Anthropic
,0.5,0.5,2026-01-01,Nobody
some-other-model,0.4,0.4,2026-01-01,Other
`,
		"deepswe_external.csv": `Model version,Pass@1,Pass@4,Harness,Reasoning effort,Name,Release date
gpt-5.6-sol_max,0.7266,0.85,mini-swe-agent,max,GPT-5.6 Sol,2026-07-09
claude-opus-5_high,0.61,0.7,mini-swe-agent,high,Claude Opus 5,2026-07-24
kimi-k3_high,0.55,0.6,SomeNewAgent,high,Kimi K3,2026-08-01
,0.48,0.5,Mini-SWE-Agent,medium,Zephyr 2,2026-08-01
`,
		"terminalbench_external.csv": `Model version,Agent,Accuracy mean,Name,Release date
claude-opus-5_unknown,Claude Code,0.66,Claude Opus 5,2026-07-24
claude-opus-5_unknown,Droid,0.70,Claude Opus 5,2026-07-24
gpt-5.6-sol_unknown, Codex CLI ,0.71,GPT-5.6 Sol,2026-07-09
,Terminus 2,0.55,,2026-07-24
`,
		"frontiercode_external.csv": `Model version,Main score,Harness,Reasoning effort,Name,Release date
claude-opus-5_max,0.5338,claude-code,medium,Claude Opus 5,2026-07-24
swe-1.7,0.40,devin,none,SWE-1.7,2026-06-01
gpt-5.6-sol_unknown,not-a-number,codex,,GPT-5.6 Sol,2026-07-09
`,
		"README.md": "## Licensing\nCreative Commons Attribution (synthetic test fixture).\n",
	}
}

// EpochArchive zips files under a data/ prefix, as the archive publishes them,
// in name order so the same files always produce the same bytes.
func EpochArchive(files map[string]string) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		fw, err := w.Create("data/" + name)
		if err != nil {
			panic(err)
		}
		fw.Write([]byte(files[name]))
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// DefaultEpochArchive is the standard Epoch AI archive.
func DefaultEpochArchive() []byte { return EpochArchive(DefaultEpochFiles()) }

// BFCLCSV is the standard BFCL overall-results file. It covers the curated
// modes, a mode no build curates ("Weird Mode"), a row whose name states no
// mode at all, and a row whose accuracy is not a percentage. Only the last is
// a rejection; the other two are evidence in their own contexts.
const BFCLCSV = `Rank,Overall Acc,Model,Model Link,Organization
1,77.47%,Claude-Opus-5 (FC),https://example.invalid/a,Anthropic
2,70.00%,Claude-Opus-5 (Prompt),https://example.invalid/b,Anthropic
3,65.5%,GPT-5.6-Sol-2026-07-09 (FC),https://example.invalid/c,OpenAI
4,60%,Kimi-K3 (Weird Mode),https://example.invalid/d,Moonshot
5,50%,BitAgent-8B,https://example.invalid/e,Bit
6,abc,Broken (FC),https://example.invalid/f,Broken
`
