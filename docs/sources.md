# Sources

Every source is implemented explicitly, with a recorded access basis. Nothing
is discovered or scraped opportunistically, and nothing is fetched until the
user runs a refresh. The access basis, licence and attribution below are also
stored in the database and shown by `devmodels metrics --detail full` and the
`describe_available_data` MCP tool with `detail: "full"`.

Source terms were last checked on 2026-09-14. They can change; if a publisher's
terms change, the collector should be revisited.

## Collected

### Devin models documentation — `devin`

| | |
|---|---|
| URL | `https://docs.devin.ai/desktop/models.md` |
| Retrieval | HTTP GET of the Markdown alternate; no browser |
| Access basis | Public documentation page. The Markdown alternate is listed in the site's `/llms.txt` index for automated consumers. `robots.txt` disallows only `/cdn-cgi/` and `/_next/`, and the site sends `Content-Signal: ai-train=yes, search=yes, ai-input=yes`. The `.md` response also carries `X-Robots-Tag: noindex, nofollow`, which concerns search indexing, not access. |
| Licence | None stated. Factual catalog and price data, read for the user's local reference. |
| Attribution | Model names, availability and prices as published by Cognition AI, Inc. at docs.devin.ai. |
| Provides | Dataset inventory (tabs) and, for the selected dataset, its catalog models and the values below |

The page presents its datasets as tabs. Two data shapes are supported.

**Token-priced datasets** (`<ModelCosts data={modelCostData} tier="…" />`,
currently *Self-serve* and *Enterprise (ACUs)*). Rows come from the
`modelCostData` JSON array.

- Metrics: input, output, cache-read and cache-write token rates; long-context
  threshold and rates (via the page's family mapping); `devin_recommended`.
- Rows the page itself hides (`MODEL_PROVIDER_UNSPECIFIED`, `[dev]`, "backend
  only") are recorded as `excluded`. If the page's display-filter code changes,
  a warning is recorded.
- A row with no `model_uid` or no label, or whose label classifies to an
  identity another row in the same dataset also claims, is `rejected` with the
  exact reason; using either row of a collision would conflate two models.
- A zero or absent rate is rendered by the page as a dash and is not recorded;
  it is missing, not zero.
- Enterprise `Adaptive` rates are shown as variable (`*`) and are not recorded.
- `credit_multiplier` is kept in the raw published row but not exposed as a
  metric: its meaning for token-billed datasets is not documented.

**Legacy credits dataset** (`<ModelsTable />`, currently *Legacy enterprise
(credits)*). Rows come from the component's `allModels` JavaScript array
literal in the same document. It is read with a strict literal-only parser —
strings, numbers, booleans, null and bare identifiers — so no JavaScript is
executed; anything else makes the dataset unsupported.

- Metrics: `devin_legacy_credits` (the table's `credits` value) and
  `devin_recommended` (the `recommended` property; an absent property means not
  recommended, which is how the table's Recommended tab treats it).
- The table does not say what one credit buys or the metering basis, so credits
  are stored as published and never converted to token prices or ACUs.
- A credits value of `*` means variable per-request pricing (the table's
  footnote) and is not recorded.
- `provider` is the table's grouping (for example `OPENSOURCE`), not
  necessarily a vendor.
- `hasGift` and icon references are kept in the raw published row only; their
  meaning is not published as data.
- Rows publish no model id, so the uid is derived from the name
  (`legacy:<name>`).
- A `ModelsTable` rendered in other units (`unit="acus"`) is listed as
  unsupported.

### Epoch AI benchmarking data — `epoch`

| | |
|---|---|
| URL | `https://epoch.ai/data/benchmark_data.zip` |
| Retrieval | HTTP GET of one zip; CSVs parsed in memory |
| Access basis | Public bulk download. The archive README and the benchmarking hub state the data is "free to use, distribute, and reproduce provided the source and authors are credited" under Creative Commons Attribution. |
| Licence | CC BY 4.0 |
| Attribution | Epoch AI, 'Capabilities & Benchmarking'. Published online at epoch.ai. Retrieved from 'https://epoch.ai/benchmarks'. Licensed CC BY 4.0. |

| Metric | File | Score column | Context |
|---|---|---|---|
| `swe_bench_verified` | `swe_bench_verified.csv` | `mean_score` | `epoch_ai_eval` (Epoch AI's own runs) |
| `deepswe` | `deepswe_external.csv` | `Pass@1` | `Harness` column |
| `terminal_bench` | `terminalbench_external.csv` | `Accuracy mean` | `Agent` column |
| `frontiercode` | `frontiercode_external.csv` | `Main score` | `Harness` column |

Scores are 0–1 fractions stored as percentages. A value outside 0–1 is
rejected rather than rescaled. Effort comes from the explicit `Reasoning
effort` column (DeepSWE and FrontierCode) when it names a level, otherwise from
the identifier suffix (`_max`, `_xhigh`); `_unknown` means not recorded.

A row's identity is its `Model version`. When that is blank and the file has a
`Name` column with a value, the name is used instead: it is still the source's
own name for the model, and it normalises to the same identity as the versioned
spelling ("SWE-1.7" and "swe-1.7"). A row with neither is rejected, because
nothing identifies what the score describes.

The harness column names the evaluation context. Curated names (Devin, Claude
Code, Codex CLI, Cursor CLI, mini-SWE-agent, Grok Build, OpenCode, Gemini CLI,
OpenHands, Terminus) keep their established codes; any other name gets its own
`harness_<name>` context with the published spelling, so the leaderboard
gaining an agent needs no devmodels release. Matching is by fold, so case and
separators do not matter and nothing else is folded together — see
[architecture.md](architecture.md#evaluation-contexts).

A missing file or column, or an empty file, fails the source with a message
saying the published format appears to have changed; the previous refresh's
evidence is kept. The same model often appears under several harnesses; when
those equally ranked results disagree, queries report them as ambiguous rather
than choosing one. Adding harnesses therefore makes ambiguity more common,
which is the honest answer: the catalog does not rank incomparable contexts.

### Berkeley Function-Calling Leaderboard — `bfcl`

| | |
|---|---|
| URL | `https://raw.githubusercontent.com/ShishirPatil/gorilla/gh-pages/data_overall.csv` |
| Retrieval | HTTP GET of one CSV |
| Access basis | Leaderboard data file published in the Gorilla project's public GitHub repository (gh-pages branch); the repository is licensed Apache-2.0. |
| Licence | Apache-2.0 (repository licence; the gh-pages branch carries no separate licence file) |
| Attribution | Berkeley Function-Calling Leaderboard, Gorilla project (UC Berkeley) |

`bfcl_overall_accuracy` comes from `Overall Acc`. The trailing parenthetical in
the model name is the evaluation mode and becomes the context: `(FC)` →
`bfcl_fc`, `(Prompt)` → `bfcl_prompt`, `(FC thinking)` → `bfcl_fc_thinking`,
`(Prompt + Thinking)` → `bfcl_prompt_thinking`. Any other mode gets its own
`bfcl_mode_<mode>` context, and a name with no parenthetical at all becomes
`bfcl_mode_unstated` — BFCL is always a direct evaluation, so what such a row
lacks is a statement of mode, not validity. None of these is ever merged with a
curated mode. Only a row whose accuracy is not a percentage is rejected.

Columns are located by header name only; a reshaped file fails the source with
a message saying the format appears to have changed. Many models are published
in more than one mode, so their BFCL values are ambiguous until a query chooses
one. The published file can lag new model releases.

## Not collected

These were evaluated and deliberately left out. Each is an owner decision, not
an oversight. Their terms were re-checked on 2026-09-14 and had not changed.

### Artificial Analysis

Its Website Terms of Use (version 1.0, last revised 28 April 2024) grant a
licence for "personal, noncommercial use" (§2.1) and prohibit using "software or
automated agents or scripts to … generate automated searches, requests, or
queries to (or to strip, scrape, or mine data from) the Site" (§3.3(b)(vi)).
Browser automation does not change that, so no collector contacts the site.
Two components of its Coding Agent Index, DeepSWE and Terminal-Bench, are
available from Epoch AI instead.

### OpenRouter models API (context windows)

The model list endpoint (`/api/v1/models`) is public and documented, and it
carries context lengths. OpenRouter's Terms of Service, however, prohibit using
"scripts, robots or any other means or processes … to scrape or copy any
information on the Site or the Services". Whether copying the model list through
the documented API into a local catalog falls under that clause is ambiguous, so
it is not collected. Context windows can be supplied by
[manual import](manual-import.md).

### swebench.com leaderboard files

`SWE-bench/swe-bench.github.io` publishes `data/leaderboards.json` with many
scaffold-and-model entries, but that repository is licensed Creative Commons
Attribution-NonCommercial 4.0. Because users may choose models for commercial
work, it is not collected. SWE-bench Verified results are collected from Epoch
AI's own runs (CC BY 4.0) instead; they cover fewer scaffolds.

## Manual import

Evidence that cannot be collected automatically can be imported by the user,
under a `manual_*` source code with an access basis the user states. See
[manual-import.md](manual-import.md).
