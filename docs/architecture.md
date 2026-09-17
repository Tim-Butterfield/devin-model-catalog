# Architecture

Devin Model Catalog is a local fact and evidence catalog. It collects, stores
and returns facts; it does not decide what a task needs. The consuming agent
chooses criteria, missing-evidence and ambiguity policies, evidence contexts,
and ordering.

## Layers

```
cmd/devmodels                 process entry point
internal/cli                  argument parsing and output formatting (thin)
internal/mcpserver            MCP stdio tools (thin)
        │   both call only ▼
internal/app                  application service: datasets, selection, refresh
                              orchestration, status, import, aliases, browser
        │
        ├── internal/devin            Devin page: dataset discovery and catalog building
        ├── internal/sources          collector contract, harness vocabulary
        │     ├── sources/epoch       Epoch AI collector
        │     ├── sources/bfcl        BFCL collector
        │     └── sources/manual      manual import documents
        ├── internal/retrieval        Fetcher and Browser/Page interfaces; bounded HTTP fetcher
        │     └── retrieval/browser   managed Chrome for Testing runtime driven over CDP
        ├── internal/identity         label classification and normalisation
        ├── internal/metrics          generic metric definitions
        ├── internal/query            evidence resolution, criteria, ordering, details
        ├── internal/store            SQLite, migrations, all SQL
        └── internal/config           per-user paths and the config file
agents.go + AGENTS.md         embedded agent guidance (single source)
devin/skills/                 Devin skills shipped for users to copy; no code
```

Import direction is strictly downward. Collectors never import `store` or
`query`; `store` and `query` never import a collector. The CLI and MCP server
contain no catalog logic and never shell out to each other.

`devin/skills/` is outside that structure because it is not part of the
program: `recommend-model/SKILL.md` is a prompt a user copies into their own
Devin skills directory, and it reaches the catalog the same way any other
client does, over MCP. Recommendation policy lives there and nowhere in the
binary — the catalog answers questions and does not decide what a task needs.
A test keeps the skill's vocabulary honest against the running server.

## Dataset lifecycle

Devin's models page presents licensing datasets as tabs. Each tab renders a
component defined in the same document:

- `<ModelCosts data={modelCostData} tier="…" />` — token-priced datasets. Rows
  come from the `modelCostData` JSON array.
- `<ModelsTable />` — the legacy credits dataset. Rows come from the
  component's `allModels` JavaScript array literal, read with a strict parser
  that accepts only strings, numbers, booleans, null and bare identifiers
  (icon references). No JavaScript runs and no browser is used. A literal
  outside that subset, or a table rendered in another unit (`unit="acus"`),
  makes that dataset unsupported without affecting the others.

1. **Discovery** (`datasets refresh`). `devin.ParsePage` reads every tab of the
   tab block that renders catalog components. Each dataset gets a **source
   key** from its structure — component plus sorted attributes, for example
   `ModelCosts|data=modelCostData|tier=TEAMS_TIER_PRO` or `ModelsTable` — and
   keeps its current title as metadata. Tabs this version cannot interpret are
   listed as unsupported with a reason. Duplicate structural keys make both tabs
   unsupported. The inventory table is replaced and renumbered `1..N`.
2. **Selection** (`use-dataset <id>`). The id is resolved against the current
   inventory and the dataset's source key, title, component and attributes are
   persisted. Selecting a *different* dataset deletes the previous catalog and
   its catalog-scope observations in the same transaction and marks the new
   selection `needs_refresh`; queries fail until it is refreshed.
3. **Catalog refresh** (`refresh`). The page is fetched again and the selected
   source key must match exactly one supported dataset. A missing, unsupported
   or ambiguous match fails the refresh with the list of current datasets and
   changes nothing; a similarly titled dataset is never adopted. A renamed tab
   with the same structure is accepted, and the new title is recorded with a
   warning.

Legacy rows publish no model id, so their uid is derived from the published
name (`legacy:claude-opus-5-medium-thinking`); a renamed row becomes a different
model.

## Refresh invariants

- **Atomic per source.** The Devin catalog, and each evidence source, is
  replaced in its own transaction, after its content has been fetched, parsed
  and accounted for. A failure at any earlier point records a failed run and
  leaves that source's previous rows intact.
- **Fail closed on incomplete input.** HTTP reads fail on a short read against
  `Content-Length`, an oversized body, a non-200 status or an empty body.
  Parsers fail when any structure they rely on is missing (catalog array,
  component definitions, the closing `</Tabs>`, expected CSV files or columns,
  empty files). A catalog that would drop below half the current model count is
  rejected unless `--allow-shrink` is given.
- **Accounting.** Every published row ends in exactly one state, and
  `published = accepted + excluded + rejected` for every source. `accepted`
  became a catalog model or one or more observations; `excluded` is a row the
  source hides from its own display (only the Devin catalog has these);
  `rejected` is a row this version cannot represent faithfully — no model
  identity, no usable score, an identity another row already claims — recorded
  with its exact reason. A collector whose counts do not add up fails.

  A label this version has not seen before is **not** a state: an unfamiliar
  harness or evaluation mode becomes its own evaluation context (below), so
  such rows are accepted. Separately, an accepted observation may be
  **unmatched** — it describes a model identity no model in the selected
  dataset has. That is not a row state and not a fault: leaderboards cover a
  far wider field than any one Devin dataset. It is reported alongside the row
  counts so a large number is visibly ordinary.
- **Rows, models and data points are three different counts.** A *row* is what
  the source published. A *model* is a catalog entry in the selected dataset.
  A *data point* is one individual metric value stored from an accepted row,
  reported as `data_points`. They do not move together. An accepted Devin
  catalog row publishes input, output and cache prices, its recommendation
  state and any long-context rates, so a dataset of roughly 232 models
  produces well over 1,200 data points — the larger number is values, never
  models. An accepted benchmark row commonly produces one data point today,
  but that is what those sources currently publish, not a rule: a source may
  publish several metrics per row, and one data point may also supply a value
  to more than one catalog model that shares its base identity.
- **Idempotent.** A refresh replaces a source's rows; running it again yields
  identical counts and alternative counts.
- **Current state only.** Observations are not versioned. `source_runs` keeps
  the last ten runs per source for diagnostics.

## Data model

All metrics are rows. There are no benchmark-specific columns.

| Table | Holds |
|---|---|
| `sources` | Code, name, kind (builtin/manual), URL, retrieval mechanism, access basis, licence, attribution |
| `metrics` | Key, display name, description, value kind, unit, direction, scope, defining source, missing-possible, status |
| `evaluation_contexts` | Harness or mode codes with a kind: `devin`, `published`, `external_harness`, `direct`. Codes are open-ended (a source may introduce one); kinds are a closed set this project owns |
| `devin_datasets`, `dataset_inventory` | The current inventory (ids 1..N) and when it was read |
| `selected_dataset` | The selection by structural identity, and its catalog state |
| `catalog_models` | Current models of the selected dataset with parsed identity and the raw published row |
| `catalog_row_dispositions` | What became of every published row of the selected dataset |
| `observations` | One value per published row per metric, with raw source name, row reference, identity dimensions, context, and `catalog_uid` for catalog-scope values |
| `rejected_rows` | Rows this version could not represent, each with its exact reason |
| `model_aliases` | User-confirmed mappings from a source's model name to a catalog identity |
| `source_runs` | Recent run outcomes and counts |

Migrations are embedded SQL files applied in order at open and recorded in
`schema_migrations`. A database newer than the binary is refused. Supporting the
legacy dataset and ambiguous resolution required no migration; `0002` replaced
the unresolved/quarantined pair with `rejected_rows`, dropping the rows that
were held back only for an unfamiliar label — they were never rejections, and
the next refresh ingests them as evidence.

## Evaluation contexts

A score's evaluation context is what produced it: an agent harness, or a
direct-evaluation mode. It changes the score, so it is never guessed.

Sources name their own contexts, and the field moves faster than releases. A
published label is resolved by **folding** it — lowercasing, and collapsing
each run of non-alphanumeric characters to one separator — and looking the fold
up in a small curated vocabulary. A fold that is not there becomes its own
context in a namespaced code (`harness_<fold>`, `bfcl_mode_<fold>`), keeping
the source's own spelling as its name.

Two labels are the same context exactly when their folds are equal. There is no
edit distance, no substring match and no token overlap, so "Grok CLI" and
"grok-cli" are one context while "ForgeCode" and "Forge Code" stay two: one has
a separator where the other has none, and nothing in the source says they are
the same product. The curated vocabulary exists only for what folding cannot
know — the stable codes callers already filter on, and the few judgements that
two genuinely different names are one harness ("Codex" and "Codex CLI",
"Grok Shell" and its later name "Grok Build").

A collector reports the contexts a run used in its `Result`, and the refresh
records them inside the same transaction, before the observations that cite
them. The **kind** is always the collector's, never the source's: kinds decide
whether a value is a proxy, so they stay a closed set, and a refresh that would
reclassify an existing context fails rather than silently changing what every
stored observation citing it means.

### Metric scopes

- `catalog` metrics are emitted by the Devin catalog for a specific model and
  belong to the selected dataset: token rates, long-context pricing and the
  recommended flag for token-priced datasets; `devin_legacy_credits` and the
  recommended flag for the legacy credits dataset.
- `evidence` metrics (benchmarks, imported values) describe a model identity
  and are independent of the dataset; they are matched to catalog models at
  query time.

## Identity and evidence resolution

`identity.Classify` peels recognised trailing dimensions off a label —
reasoning effort (`High`, `Max Thinking`, `Extra High Reasoning`, `X-High`…),
serving variant (`Fast`) and context variant (`1M`) — and leaves everything else
in the base name. Parentheses are treated as spacing, so the legacy table's
"Claude Opus 5 Fast (High Thinking)" and the token table's "Claude Opus 5 High
Fast" are the same identity. Unrecognised words (`Flash`, `Lightning`,
`Thinking`, `Review`) are identity. Source-specific normalisation
(`NormaliseIdentifier`) removes release dates and turns `4-6` into `4.6` before
classification.

For a catalog model and an evidence metric, an observation matches when its
base key (after any alias) equals the model's base key, and:

| Observation effort vs model | Result |
|---|---|
| equal (including both unspecified) | exact |
| unspecified, model has an effort | match, `effort_exact=false` |
| different, or specified while the model's is not | no match |

| Observation serving vs model | Result |
|---|---|
| equal | exact |
| standard, model is Fast | match, `serving_exact=false` |
| Fast, model is standard | no match |

Fuzzy similarity is used only to *suggest* identities (`devmodels unmatched`,
`devmodels rejected`, model details in `full`); it never creates a match.
Aliases are explicit user decisions.

### Selecting among matching observations

Matching observations (after any `evidence.contexts`, `context_kinds` or
exactness restrictions) are ranked only by strength of evidence:

1. exact effort over effort-inexact;
2. exact serving variant over a borrowed standard variant;
3. `devin` or `published` contexts over proxy contexts (`external_harness`,
   `direct`);
4. the caller's `evidence.prefer_contexts` order, when given.

The observation value never ranks. Among the best-ranked observations:

- if they all have the same value, it is selected;
- otherwise the resolution is **ambiguous**: no value is selected, and every
  peer observation is returned with the peer contexts and the value range.
  `ambiguity_reason` is `peer_contexts` when different contexts disagree and
  `conflicting_values` when one context disagrees with itself.

There is no global context ranking. An ambiguity is resolved only by the caller
restricting or preferring contexts.

## Querying

A request has `criteria` (eligibility), `order_by` (ordering of eligible models
only), `filter` (catalog fields) and `limit`. Every criterion except `present`
must state `missing: reject|allow`. Validation happens before data readiness, so
malformed requests are reported as such.

For an ambiguous resolution, a criterion every peer satisfies is met
(`met_by_all_peers`) and one no peer satisfies is not met; when peers disagree
about it, the criterion's `ambiguous` policy applies (`reject` by default,
reported as exclusion reason `ambiguous`; or `allow`, reported as
`ambiguous_allowed`). In ordering, resolved values sort first, ambiguous values
after them, and missing values last (or first on request). The response includes
per-criterion exclusion counts and an `ambiguities` summary per metric. Queries
fail with a not-ready error when no dataset is selected or its catalog is not
current.

Results use one response model with three projections, chosen by `detail`.
Resolution, filtering, ordering, exclusions and ambiguity summaries are computed
identically for all of them; the projection is applied to returned results last.

- `compact` (default) lists the normalized `criteria` and `order_by` once, and
  each result's `criteria[i]` and `order[k]` report them by position, without
  repeating metric names. A value keeps its `state`, `value`, evaluation
  `context`, `flags` for weaker evidence (`proxy`, `effort_inexact`,
  `serving_inexact`, `alias`), ambiguity reason, value range, alternative count
  and `peer_values` (value and context). A criterion whose metric and evidence
  policy equal an order term is marked `value_in_order_by` and carries only its
  outcome. It is meant for broad candidate selection.
- `summary` keeps, per value, its metric, `state`, `value`, ambiguity reason,
  peer contexts, value range, alternative count, and compact `evidence` (source
  and context codes, proxy and exactness flags) or compact `peer_values`.
- `full` returns complete observation objects (`selected`, `peers`, caveats,
  selection basis, and `alternatives` when requested). `include_alternatives`
  needs `full`: it selects `full` when `detail` is omitted and is rejected with
  `compact` or `summary`.

`get_model_details` has its own two projections, computed from the same
resolutions so they never disagree. `compact` (the default over MCP) carries
identity, dataset, and per metric its value, state, evidence source and
evaluation context, weaker-evidence flags, ambiguity range and peer values,
plus missing metrics and material caveats — everything needed to make and
explain a recommendation. `full` adds the diagnostic material: complete
observation objects for the selected value and every peer, per-observation
caveats, the selection basis, the published Devin row, and similarly named
rejected rows. `devmodels model` defaults to `full`, because nothing is charged
for context locally.

## Browser runtime

`retrieval.Browser` gives collectors a page session through a deliberately small
`retrieval.Page` interface: `Navigate`, `WaitVisible`, `WaitFor` (a JavaScript
condition), `Click`, `Evaluate` (JSON result), `HTML`, `Text` and `Attribute`.
Every operation is bounded by an action timeout.

`retrieval/browser.Manager` implements it:

- **Provisioning.** On first use it downloads the pinned Chrome for Testing
  `chrome-headless-shell` build for the platform (macOS, Linux x64, or Windows
  x64 or x86; none is published for arm64 Linux or Windows) into the per-user cache,
  extracts it into a staging directory while rejecting paths and links that
  would leave it, verifies the executable, writes a manifest (version, platform,
  archive SHA-256), activates it by rename, and then removes builds of other
  versions on a best-effort basis. An install lock serialises installs; a second
  process waits for it. Changing the pinned version is the upgrade path; a
  failed download leaves the previous install in place.
- **Sessions.** `WithPage` launches the executable with a throwaway profile and
  drives it over the Chrome DevTools Protocol with
  [chromedp](https://github.com/chromedp/chromedp), a pure-Go client. The
  browser process and profile are removed when the session ends. Launch is
  bounded separately and a failure names the executable.
- **Diagnostics.** `devmodels status` and `devmodels browser status` report the
  runtime; `devmodels browser check` provisions it if needed and drives a
  built-in page (click, wait, read text, evaluate).

No built-in collector uses the browser in this release: every source currently
collected — including the legacy credits table — is available without
JavaScript, and the source that would need it does not permit automated
collection. See [sources.md](sources.md).

## Adding a source

1. Establish a defensible access basis and licence; record them in `Info()`.
2. Implement `sources.Collector`: `Info`, `Metrics` (definitions with scope
   `evidence`), `Contexts` (the contexts known before any collection), and
   `Collect` using `env.HTTP`, or `env.Browser` when the source genuinely needs
   JavaScript or interaction.
3. Map source names through `identity.NormaliseIdentifier` and
   `identity.Classify`; carry effort and serving variant only when the source
   states them.
4. Resolve an evaluation context from the source's own label with
   `sources.ResolveHarness` or a collector-local equivalent built on
   `sources.DeriveContext`, and report every context the run used in
   `Result.Contexts`. Choose the kind yourself; never take it from the source.
   Reject a row only when it carries nothing usable, with an exact reason.
5. Fail the whole collection on structural change; account for every row.
6. Add synthesized fixtures to `internal/testfixtures` and parser tests.
7. Register the collector in `app.BuiltinCollectors`.
8. Document it in `docs/sources.md` and, where interpretation matters, in
   `AGENTS.md`.

No migration or query change is needed.
