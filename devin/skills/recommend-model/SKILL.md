---
name: recommend-model
description: Recommend which Devin model to use for a task the user describes, from the live devmodels catalog. Asks for the task if none is given.
argument-hint: <task description>
triggers:
  - user
---

# Recommend a Devin model

Recommend the model to use **for a task the user describes**, using the
`devmodels` MCP server as the only source of current model facts.

The division of labour is fixed:

- `devmodels` supplies current facts — which models the selected Devin dataset
  offers, their published prices, benchmark evidence, and the provenance,
  ambiguity and weakness of that evidence.
- This skill supplies the policy — reading the task, choosing which metrics
  matter, building the query, and interpreting what comes back.

Never state a price, a score or a model's availability from memory or from this
file. Those come from a tool result in this session or they are not said.

## The task description

The task is: `$ARGUMENTS`

**If that is empty, ask for it and stop until it arrives.** Something like:

> What's the task you want a model for?

Then work from the answer. Do not:

- abort because no argument was given;
- infer the task from the conversation so far, the open files, the repository,
  the current branch or recent activity;
- pick a default kind of task.

An empty argument means the user has not said yet, not that you should guess.

**If a task description was given, that is the task.** Use it as stated; do not
rewrite it into a different or larger one.

## Do not invent requirements

Use only what the user stated, plus what is genuinely inherent in the task they
described. Do not assume a latency budget, a cost ceiling, a language
preference, a context-window need, a tool-use need, a quality bar, a reasoning
effort, production criticality, a privacy constraint, time pressure, or a
preference for the newest, largest or cheapest model.

If one missing detail would actually change the recommendation, ask one focused
question about that detail. If it would not, do not ask. Two questions is
almost always one too many.

## The recommendation is for a new conversation

The answer is which model the user should **start a new conversation with** to
do the work. Do not switch the current conversation's model, do not start doing
the task here, and do not try to automate session switching. Close with a short
line to that effect.

## Normal path

Keep this cheap — the point of the skill is to spend a small, predictable
amount of context on a decision, not to re-derive the catalog every time.

1. Read the task. Decide which metrics actually bear on it, using the
   vocabulary below.
2. Call **`query_models`** once, with the default `compact` detail.
3. Read the result — values, states, flags, `exclusions`, `ambiguities`.
4. Recommend.

That is the whole path for an ordinary request. Do not call every tool "to be
safe", and do not fetch full detail for candidates you are not weighing.

### When to reach for the other tools

- **`data_status`** — only when usable data is actually in doubt: a tool result
  says the catalog is not ready, the dataset is unselected, a query fails, or
  the returned refresh time looks stale enough to matter for the answer. If it
  reports `usable: false`, relay its `problems` and say what the user must run
  (`devmodels datasets refresh`, `devmodels use-dataset <id>`, `devmodels
  refresh`). Do not produce a recommendation from absent or stale data.
- **`describe_available_data`** — a fallback, not a warm-up. Call it (default
  summary) when a metric key below is rejected as unknown, when the task needs
  something this vocabulary does not cover, when the catalog may have gained a
  metric that fits the task better, or when the user asks what evidence exists.
  It is authoritative: if it disagrees with this file, it wins, and you should
  say so rather than forcing the task into a stale key.
- **`get_model_details`** — an escalation. Use it when a result that decides
  the choice is ambiguous, when peer contexts disagree, when proxy or
  inexactness materially changes the ranking, when two finalists are close
  enough that provenance settles it, or when the user asks for the evidence.
  Its default projection is usually enough; ask for `detail: "full"` only when
  the observation rows themselves are the question. Not for every candidate.
- **`agents_md`** — the catalog's own account of its semantics. Read it once if
  you want the long form; not needed per invocation. It opens with
  `data_status` and `describe_available_data` because it is written for an agent
  that holds none of this vocabulary. This skill holds it, which is what makes
  those two conditional here. Everything else it says — how evidence is ranked,
  what the flags mean, how to read a result — applies unchanged and wins over
  this file wherever they differ.

## Stable metric vocabulary

Meanings only. Coverage, scores, prices and which model leads all change and
must come from a tool result.

**Benchmark evidence** — external, matched to models by identity. All are
numbers in percent where higher is better, and each value names the evaluation
context it came from:

| Key | What it measures |
|---|---|
| `swe_bench_verified` | SWE-bench Verified issues resolved, from Epoch AI's own evaluation runs. General software-engineering issue resolution. |
| `deepswe` | DeepSWE Pass@1 on realistic implementation tasks. Each value names the agent harness that produced it. |
| `terminal_bench` | Terminal-Bench mean accuracy on agentic command-line tasks. Each value names the harness. |
| `frontiercode` | FrontierCode main score on hard real-world coding tasks. Each value names the harness, including Devin itself where published. |
| `bfcl_overall_accuracy` | Berkeley Function-Calling Leaderboard overall accuracy: tool and function-calling correctness. A direct model evaluation; the context names the mode. The leaderboard can lag current releases. |

**Catalog facts** — published by the selected Devin dataset for that exact
model:

| Key | Kind |
|---|---|
| `devin_input_price_usd_per_mtok` | USD per 1M tokens, lower is better |
| `devin_output_price_usd_per_mtok` | USD per 1M tokens, lower is better |
| `devin_cache_read_price_usd_per_mtok` | USD per 1M tokens, lower is better |
| `devin_cache_write_price_usd_per_mtok` | USD per 1M tokens, lower is better |
| `devin_recommended` | boolean; Cognition's editorial flag, not a benchmark |
| `devin_legacy_credits` | credits, lower is better; only the legacy credits dataset |
| `devin_long_context_threshold_tokens` | prompt tokens; the size above which a separate rate set applies |

Above that threshold a parallel rate set applies, in the same unit:
`devin_long_context_input_price_usd_per_mtok`,
`devin_long_context_output_price_usd_per_mtok`,
`devin_long_context_cache_read_price_usd_per_mtok` and
`devin_long_context_cache_write_price_usd_per_mtok`.

Four traps worth remembering:

- **What the catalog lists is an upper bound on what can be selected.** It is
  the price list Devin publishes for the dataset; organisation and group
  restrictions are not published and are not modelled, so a model listed here
  may not be offered in the IDE for a given account. Such restrictions fall
  hardest on the costly end, where an organisation has the most reason to
  exclude something. So never tell anyone a model is available to them, always
  name a fallback, and when your first choice is a premium one make the
  fallback materially cheaper rather than the next most expensive row — a
  second unavailable model is no fallback at all.
- `devin_long_context_threshold_tokens` is a **pricing** threshold, not the
  model's context window. The catalog publishes no context-window metric, so do
  not answer a context-length question from it.
- Credits and token prices are different units. Never compare, add or convert
  across them; compare credits only within the legacy dataset.
- There is no latency or speed metric. A `fast` serving variant is a separately
  priced way of serving the same model, not a weaker or stronger one — filter
  on it only for a constraint the user stated.

## Task to metric

Judgement, not a lookup table. Pick the few metrics the task actually turns on;
no single benchmark decides whether a model suits a task.

- Writing or changing code in a repository → `swe_bench_verified` is the
  general signal; `deepswe` speaks to implementation work.
- Hard, long-running autonomous work over a whole repository → `frontiercode`,
  with `deepswe` and `swe_bench_verified` as support.
- Work that leans on tools, APIs or structured calls → `bfcl_overall_accuracy`.
- Shell, CLI or environment-heavy work → `terminal_bench`.
- A stated budget → the price metrics as a criterion; otherwise as a secondary
  ordering term.
- Small, ordinary, low-risk work → do not demand the strongest evidence
  available. Sufficient is the target.

Make hard requirements `criteria` and preferences `order_by`. Criteria decide
eligibility; ordering never excludes anyone.

## Cost policy

Prefer the **least expensive model that clearly meets what the task actually
needs**. Do not spend inference splitting hairs over small price differences,
and do not chase the cheapest row.

Cost is a decision factor. It is not evidence. So:

- never rank or select evidence by which value is more favourable;
- never treat a missing benchmark value as a low one, and never pick a cheap
  model *because* it has no evidence;
- never assume the most expensive model is the most capable.

## Evidence semantics

These come from `devmodels` and must survive into the recommendation intact.

- **Missing is not zero.** A model with no value for a metric has not scored
  badly on it; nothing is known. Say "no value published", never "0".
- **Ambiguity is not yours to settle.** When equally ranked observations
  disagree, no value is chosen and you get `ambiguity_reason`, `value_range`
  and `peer_values`. If the task makes one context clearly relevant, query
  again with `contexts` or `prefer_contexts` and **say which you chose and
  why**. If none is clearly relevant, report the range, or ask the user which
  context matters. Never pick one quietly to make the ranking come out.
- **Proxy is different evidence, not bad evidence.** `proxy` means it was not
  measured with Devin. Report it when it carries the decision; scores from
  different contexts are not directly comparable.
- **`effort_inexact` / `serving_inexact`** mean the value was borrowed from
  another effort level or the standard serving variant. Weaker; name it when it
  matters to the choice.
- **`alias`** means a user-confirmed name mapping let the evidence attach to
  that model. It is a matching fact about that source's name, not a claim that
  two labels are interchangeable in general.

## When not to recommend at all

If the request is trivial and obviously low-cost — a one-line edit, a quick
question — it is reasonable to say the user's usual model is fine rather than
running an optimisation. Keep this narrow: it does not apply to medium or large
tasks, and if the user explicitly asked for a recommendation, give one unless
the catalog is unusable or the task is still undescribed.

## Answer shape

Short. The recommendation must not cost more than it saves.

- **Recommended:** the model, named as the catalog names it.
- One or two sentences on why, grounded in the values that came back, with
  their units and the dataset they belong to.
- The evidence or constraint that actually drove it.
- Any caveat that matters: missing, ambiguous, proxy or inexact evidence.
- **At least one alternative**, with a few words on what taking it costs.
  Never skip this, even when the first choice is clear: the reader may find it
  is not offered to their account, and a recommendation they cannot act on
  sends them back to ask again.
- A closing line that the recommendation is for a new conversation.

Do not dump the catalog, restate these metric definitions, or give more decimal
places than the source did.

## Query shape

Structure only — the metrics, thresholds and policies come from the task in
front of you.

```json
{
  "criteria": [
    {"metric": "<metric key>", "op": "gte", "value": 0, "missing": "reject"},
    {"metric": "<metric key>", "op": "lte", "value": 0, "missing": "allow"}
  ],
  "order_by": [
    {"metric": "<metric key>", "direction": "desc"},
    {"metric": "<metric key>", "direction": "asc"}
  ],
  "limit": 10
}
```

- `op`: `gte`, `gt`, `lte`, `lt`, `eq`, `ne`, `present`.
- `missing`: required on every criterion except `present` — `reject` when the
  task requires the evidence to exist, `allow` when sparse coverage should not
  disqualify a model.
- `ambiguous`: `reject` (default) or `allow`, per criterion.
- `evidence` on a criterion or order term: `contexts`, `prefer_contexts`,
  `context_kinds`, `exact_effort`, `exact_serving`.
- `filter`: `providers`, `efforts`, `serving_variants`, `context_variants`,
  `label_contains`, `uids`.
- `detail`: `compact` is the default and the normal choice.

`limit` 10 is nearly always enough: the response's counts, `exclusions` and
`ambiguities` describe every eligible model, not just the rows returned.
