# AGENTS.md — using Devin Model Catalog

`devmodels` is a local, factual catalog of the models in one selected Devin
licensing dataset, with published prices and third-party benchmark evidence. It
answers questions; it does not recommend. **You** turn the user's task into
criteria, query, and recommend. It is an independent third-party tool, not
affiliated with or endorsed by Cognition AI, Inc.

This file is embedded in the binary (`agents_md` over MCP, `devmodels
agents-md` in a shell). It describes semantics only and never lists models,
scores or prices: query those instead of recalling them.

## What the catalog contains

- **One Devin dataset** (for example self-serve, enterprise ACU, or legacy
  credits). Models and costs depend on it; responses name it.
- **Models** are Devin-addressable variants. Reasoning effort ("High"),
  serving variant ("Fast") and context variant ("1M") are part of identity.
- **Metrics** are data. `catalog` metrics are published by the dataset for that
  exact model (token rates, or credits in the legacy dataset — never compare a
  credit cost with a token price). `evidence` metrics are external benchmarks
  matched to models. A metric with no values in the selected dataset does not
  apply to it.

`describe_available_data` is authoritative for which metrics exist, their
meaning, unit, better direction, scope, coverage, sources and evaluation
contexts. Its default summary is enough to build queries. Each metric names the
context codes it was measured in, and `evaluation_contexts_by_kind` lists every
code in use once more, grouped under its kind — so a code's kind is read from
the group it appears in. `detail: "full"` adds source access basis, licences,
attribution, `data_points` (how many individual metric values are stored for a
metric, which is not a model count), and `evaluation_contexts`: every context
the catalog knows, used or not, with its human name.

## Workflow

1. `data_status`. If `usable` is false, relay its `problems`; do not answer from
   memory. Note freshness, and mention a failed source refresh when that
   source's metric decides the answer.
2. `describe_available_data` (default summary).
3. One broad `query_models` with only task-relevant criteria and ordering,
   `limit` 10 and the default `detail` (`compact`); no alternatives. Its
   counts, `exclusions` and `ambiguities` cover every eligible model, not just
   the returned rows, so a larger limit is not needed to learn what exists.
4. `get_model_details` on the primary and a few finalists. Its default carries
   each metric's value and state, the source and evaluation context behind it,
   the weaker-evidence flags, peer values for anything ambiguous, missing
   metrics and caveats — everything needed to explain a choice. Ask for
   `detail: "full"` only when a specific question needs the observation rows,
   the non-selected alternatives or the published Devin row.
5. Only if a specific question stays open (an ambiguity or close call that
   decides the choice): ask a targeted follow-up query — add a criterion, a
   filter, or a context restriction or preference — instead of returning every
   eligible model. Widen beyond 10 rarely and to about 15 at most; use
   `detail: "summary"` or `"full"` only when that question needs it.

Let the query do filtering and ordering. Avoid large limits "just in case",
full detail for every candidate, re-ranking outside the tools, synthetic or
combined scores, and choosing a context arbitrarily to remove an ambiguity.
Refreshing data is a user action (`devmodels datasets refresh`, `use-dataset`,
`refresh`); the MCP tools are read-only.

## Building a query

Decide from the task: which metrics matter (not all do), which are hard
requirements (criteria) and which are preferences (order_by), thresholds, and
which evaluation contexts count. Criteria decide eligibility; ordering never
excludes a model.

**Every criterion except `present` states `missing`:** `reject` when the requirement must be
demonstrated, `allow` when absence should not disqualify (coverage is sparse).
**Missing is not zero** — never describe or compare a missing value as a low
score.

**Serving variants are factual, not a quality signal.** A Fast variant is a
separately priced way of serving the same model; nothing in the catalog makes
it less capable. Do not exclude Fast because a task is hard or not
latency-bound; filter `serving_variants` only for user constraints such as
cost. Fast benchmark values are often borrowed from the standard variant
(flag `serving_inexact`).

```json
{
  "criteria": [
    {"metric": "swe_bench_verified", "op": "gte", "value": 70, "missing": "reject"},
    {"metric": "bfcl_overall_accuracy", "op": "gte", "value": 60, "missing": "allow",
     "evidence": {"prefer_contexts": ["bfcl_fc"]}}
  ],
  "order_by": [{"metric": "swe_bench_verified", "direction": "desc"}],
  "limit": 10
}
```

- `op`: `gte`, `gt`, `lte`, `lt`, `eq`, `ne`, `present` (a value exists; always
  rejects missing).
- `ambiguous` (criteria): `reject` (default) or `allow`.
- `evidence`: `contexts` (only these codes), `prefer_contexts` (ordered
  tie-break), `context_kinds` (`devin`, `published`, `external_harness`,
  `direct`), `exact_effort`, `exact_serving`. Context **codes are open-ended**:
  a source naming a harness or mode this build has not seen before gets its own
  code rather than being folded into a neighbour, so read the current codes
  from `describe_available_data` instead of assuming a fixed list. The four
  `context_kinds` are fixed.
- `filter`: `providers`, `efforts`, `serving_variants` (`fast`/`standard`),
  `context_variants`, `label_contains`, `uids`.
- `detail`: `compact` (default); `summary` repeats metric names per value with
  `evidence` objects and sources; `full` adds observation objects.
  `include_alternatives` needs `full` (omit `detail` or set it to `full`).

The response counts exclusions per criterion (`missing`, `not_met`,
`ambiguous`) and summarises `ambiguities`; use them to explain or relax
constraints, and say when you relax one.

## Reading results

The response lists your normalized `criteria` and `order_by` once; each
result's `criteria[i]` and `order[k]` report them by position. Each value has
`state`: `resolved` (with `value` and `context`, the evaluation context code),
`missing` (no value; never zero), or `ambiguous`. A criterion with
`value_in_order_by: k` has the same metric and evidence as `order_by[k]`, so
its entries carry only `outcome`; read the value from `order[k]`.

`flags` marks weaker evidence; no flags means Devin-measured or
Devin-published and exact:

- `proxy` — not measured with Devin (another harness or a direct
  evaluation). A proxy for behaviour in Devin; say so when it matters. Scores
  from different contexts are not directly comparable.
- `effort_inexact` — measured without the model's effort level;
  `serving_inexact` — borrowed from the standard serving variant. Weaker
  evidence.
- `alias` — matched by a user-confirmed alias.

Observations are ranked only by evidence strength: exact effort, exact serving
variant, Devin-measured or Devin-published over proxy, then your
`prefer_contexts`. **The value never decides.** If the best-ranked observations
agree, that value is used; `alternative_count` counts the rest.

### Ambiguous evidence

If the best-ranked observations disagree, no value is chosen. The result gives
`ambiguity_reason` — `peer_contexts` (different contexts disagree) or
`conflicting_values` (one context publishes different values) — plus
`value_range` and `peer_values` (each peer's value and context); the response's
`ambiguities` lists peer contexts per metric.

- A criterion every peer satisfies is `met_by_all_peers`; one no peer satisfies
  is not met. When peers disagree about it, `ambiguous: reject` excludes the
  model (reason `ambiguous`) and `allow` keeps it (`ambiguous_allowed`).
- In ordering, ambiguous values sort after resolved ones and before missing
  ones, never by a guessed value.
- For `peer_contexts`, pick the context the task makes relevant and query again
  with `contexts` or `prefer_contexts`; if none is clearly relevant, report the
  range and contexts. `conflicting_values` cannot be resolved by choosing a
  context: report the range or set `ambiguous` deliberately.

## Recommending

- Give a **primary** and at least one **backup**, each with a sentence or two
  grounded in returned values, units and contexts, for the named dataset.
- Flag proxy, inexact, ambiguous or missing evidence that affects the choice,
  and which context you chose for an ambiguity.
- If nothing meets the constraints, say which criterion excluded models and
  offer the closest alternatives.
- Never invent metrics, values, models or prices.

Tool errors are returned as results with a message (no dataset selected,
catalog not refreshed, unknown metric or context — valid keys are listed,
missing `missing` policy, invalid `ambiguous` or `detail`).
