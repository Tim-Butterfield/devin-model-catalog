# Manual import

`devmodels import <file.json>` records observations you supply — for example
context windows transcribed from vendor documentation — as a source of their
own. Imported data is stored, matched and queried exactly like collected data.

Each entry in the file's `observations` array is one metric value for one
model, so it becomes one stored data point. That is what the command reports
back and what `data_points` counts elsewhere.

Importing the same `source.code` again replaces that source's observations. An
import is all-or-nothing: any invalid row rejects the whole file and changes
nothing. `devmodels import remove <code>` deletes the source, its observations,
aliases and run history, and the metrics it defined. It refuses while another
source still has observations of one of those metrics; remove that source
first.

## Format

```json
{
  "source": {
    "code": "manual_context_windows",
    "name": "Context windows from vendor documentation",
    "access_basis": "Transcribed by me from each vendor's public model documentation on 2026-09-14",
    "homepage": "https://example.com/optional",
    "url": "https://example.com/optional",
    "license": "optional",
    "attribution": "optional"
  },
  "metrics": [
    {
      "key": "context_window_tokens",
      "display_name": "Context window",
      "description": "Maximum input context length the vendor documents for the model. Devin may grant less.",
      "value_kind": "integer",
      "unit": "tokens",
      "direction": "higher_is_better"
    }
  ],
  "evaluation_contexts": [
    { "code": "vendor_documentation", "name": "Vendor documentation", "kind": "direct" }
  ],
  "observations": [
    { "metric": "context_window_tokens", "model": "Claude Opus 5", "evaluation_context": "vendor_documentation", "value": 200000 },
    { "metric": "context_window_tokens", "model": "Claude Sonnet 4.6 1M", "evaluation_context": "vendor_documentation", "value": 1000000,
      "note": "1M variant" }
  ]
}
```

Rules:

- `source.code` must match `manual_[a-z0-9_]{1,40}`. `source.name` and
  `source.access_basis` are required. The file must contain exactly one JSON
  document.
- `metrics` defines new metrics. Keys are lowercase with underscores.
  `value_kind` is `number`, `integer`, `boolean` or `text`; `direction` is
  `higher_is_better`, `lower_is_better` or `neutral` (required for non-numeric
  kinds). A key already defined by another source cannot be redefined, but you
  may contribute observations to any existing evidence metric by omitting the
  definition.
- `evaluation_contexts` defines contexts, with `kind` `devin`,
  `external_harness` or `direct`. Existing context codes can be used without
  redefining them; declaring an existing code with a different kind is
  rejected, because the kind decides whether values are proxies.
- Each observation names a `metric`, a `model` written as the model is named
  (it is classified like any source name, so "Claude Opus 5 High" carries
  effort `high`), an `evaluation_context`, and a `value` of the metric's kind.
  Optional `effort` and `serving_variant` (`fast` or `standard`) override what
  the name implies. Catalog-scope metrics (Devin prices) cannot be imported.

A value for a base model (no effort word) matches every effort variant of that
model as `effort_exact=false`; a value for "… 1M" matches only the 1M variant.
