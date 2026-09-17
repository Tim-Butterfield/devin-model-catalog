package cli

// The help this program prints is laid out for Width columns: a command or a
// flag on its own line and its description indented beneath it, so a long
// description wraps deliberately instead of at whatever width the reader's
// terminal happens to be.
//
// There are two levels of it, and they answer different questions. `devmodels`
// on its own is a landing page: what this is, and which command to reach for
// next. Each command's own --help is the authority on that command — its
// arguments, its flags, and whatever a caller has to know to use it, including
// the shape of a file it reads. Detail belongs in the second place, not the
// first; a reference manual is the wrong thing to meet a new reader with.

// usage is the landing page.
const usage = `devmodels — a local catalog of Devin models and the evidence
for choosing between them.

An independent third-party tool, not affiliated with or endorsed by
Cognition AI, Inc.

Usage:
  devmodels <command> [arguments]

Global options:
  --db PATH
      Use this database instead of the configured one.
  --json
      Write machine-readable JSON instead of text.

Datasets:
  datasets
      List the stored Devin dataset inventory.
  datasets refresh
      Refresh the dataset inventory from Devin.
  use-dataset <id>
      Select a dataset.
  refresh
      Refresh the selected dataset's catalog and evidence sources.

Querying:
  status
      Whether the data is usable, and what is wrong if it is not.
  metrics
      The metrics, sources and evaluation contexts available.
  query
      Filter and order the current models.
  model <uid-or-label>
      Every fact and observation about one model.
  unmatched
      Evidence about models this dataset does not offer.
  rejected
      Published rows this version could not represent.

Evidence:
  alias
      Manage confirmed mappings from source names to catalog models.
  import
      Manage user-supplied observations.

Runtime and integration:
  browser
      Manage and test the optional browser runtime.
  config
      Show or change local configuration.
  mcp
      Serve the MCP tools on stdio.
  agents-md
      Print the embedded agent guidance (AGENTS.md).
  version
      Print the version.

Run ` + "`devmodels <command> --help`" + ` for a command's own options.
`

// commonFlags closes every command's help. The two flags it names work
// everywhere, so each command's own help lists only what is its own.
const commonFlags = `
Every command also takes --db PATH and --json.
`

// helpFor returns the help text for one command.
func helpFor(command string) string { return commandHelp[command] + commonFlags }

// commandHelp is what `devmodels <command> --help` prints, and what a command
// prints when it is used wrongly. Each entry is the authority on its command.
var commandHelp = map[string]string{
	"datasets": `usage: devmodels datasets [refresh]

List the stored Devin dataset inventory, or replace it from the live
Devin models page.

  datasets
      Each dataset's ID, name, model row count, whether this version can
      use it, and which one is currently selected.
  datasets refresh
      Re-read the Devin models page and replace the inventory. IDs are
      renumbered 1..N on every refresh, so read them back before
      selecting one.
`,

	"use-dataset": `usage: devmodels use-dataset <id>

Select the dataset every other command reads. The id is the one
` + "`devmodels datasets`" + ` shows now; refreshing the inventory renumbers
them.

Selecting a different dataset clears the previous dataset's catalog, so
follow it with ` + "`devmodels refresh`" + ` to load the new one.
`,

	"refresh": `usage: devmodels refresh [--source a,b] [--allow-shrink]

Verify the selected dataset, replace its catalog, and refresh every
evidence source. A source whose refresh fails keeps the data it already
had, and the exit status says that happened.

  --source a,b
      Refresh only these source codes. The default is all of them.
  --allow-shrink
      Accept a catalog less than half the size of the current one, which
      is otherwise refused as a sign the published page changed shape.
`,

	"status": `usage: devmodels status

Whether the data is usable: how fresh the catalog is, what each source's
last refresh did, how much evidence is stored, and anything wrong that
would make a query misleading.
`,

	"metrics": `usage: devmodels metrics [--detail summary|full]

The metrics this dataset can be queried on: what kind of number each one
is, which direction is better, how many models carry a value, and which
sources supply it.

  --detail summary|full
      summary, the default, names each metric. full adds descriptions,
      data point counts, and each source's access basis, licence and
      attribution.
`,

	"model": `usage: devmodels model <uid-or-label> [--detail compact|full]

Every fact and observation about one model: its catalog identity, each
metric's selected value, and the evidence that value came from.

  --detail compact|full
      full, the default here, shows each value's own observation.
      compact shows exactly what the MCP get_model_details tool returns,
      where the response is charged for as context.
`,

	"unmatched": `usage: devmodels unmatched [--source S]

Evidence about models this dataset does not offer, with the catalog
identities each published name resembles.

A source that spells a model differently shows up here rather than being
guessed at. Confirm one of the suggestions with ` + "`devmodels alias add`" + `.

  --source S
      Only evidence from this source code.
`,

	"rejected": `usage: devmodels rejected [--source S]

Published rows this version could not represent, each with the reason it
was rejected. A row listed here is data the catalog is knowingly not
using.

  --source S
      Only rows from this source code.
`,

	"alias": `usage: devmodels alias add --source S --name NAME --target LABEL
                          [--note TEXT]
       devmodels alias list
       devmodels alias remove --source S --name NAME

Confirm, list or remove a mapping from the name a source publishes to a
catalog model. A mapping is how evidence published under a different
spelling reaches the model it belongs to; ` + "`devmodels unmatched`" + ` lists
the candidates.

  --source S
      The source code the name comes from.
  --name NAME
      The model name exactly as that source publishes it.
  --target LABEL
      The catalog model, written as a Devin-style label.
  --note TEXT
      Why the mapping is correct. Stored with it.
`,

	"import": `usage: devmodels import <file.json>
       devmodels import remove <code>

Record observations you supply as a source of their own. Imported data is
stored, matched and queried exactly like collected data. Importing the
same source code again replaces that source's observations, and an
import is all or nothing: one invalid row rejects the whole file and
changes nothing.

The file is one JSON object. source says where the numbers came from,
metrics and evaluation_contexts define anything new, and observations
carries one metric value for one model each:

  {
    "source": {
      "code": "manual_context_windows",
      "name": "Context windows from vendor documentation",
      "access_basis": "Transcribed from each vendor's public docs"
    },
    "metrics": [{
      "key": "context_window_tokens",
      "display_name": "Context window",
      "description": "Input context length the vendor documents",
      "value_kind": "integer",
      "unit": "tokens",
      "direction": "higher_is_better"
    }],
    "evaluation_contexts": [{
      "code": "vendor_documentation",
      "name": "Vendor documentation",
      "kind": "direct"
    }],
    "observations": [{
      "metric": "context_window_tokens",
      "model": "Claude Opus 5",
      "evaluation_context": "vendor_documentation",
      "value": 200000
    }]
  }

A source code must match manual_[a-z0-9_]{1,40}. value_kind is number,
integer, boolean or text; direction is higher_is_better, lower_is_better
or neutral. A context kind is devin, external_harness or direct, and it
decides whether values from it count as proxies.

metrics and evaluation_contexts may be left out when every key and code
the observations use already exists; ` + "`devmodels metrics --detail full`" + `
lists those. A model is named the way a source names it, so "Claude Opus
5 High" carries effort high. Devin's own catalog metrics, the published
prices, cannot be imported.

  import remove <code>
      Remove a manual source, its observations, aliases and run history,
      and the metrics it defined. It is refused while another source
      still has observations of one of those metrics; remove that source
      first.
`,

	"browser": `usage: devmodels browser status|install|check

The managed headless browser runtime, used to read pages that do not come
back as plain HTTP. It is provisioned under this program's own cache
directory and is never the browser you use.

  browser status
      Whether it is installed, which Chrome for Testing build is pinned,
      and where it lives.
  browser install
      Download and install the pinned build for this platform.
  browser check
      Provision it if needed, then drive a built-in test page and report
      what worked.
`,

	"config": `usage: devmodels config [show]
       devmodels config set db-path PATH
       devmodels config unset db-path

Show the resolved paths, or choose where the database lives.

  config show
      The config file, the database and where its path came from, the
      data and cache directories, and the browser runtime directory.
  config set db-path PATH
      Record a database path in the config file.
  config unset db-path
      Remove it, returning to DEVMODELS_DB or the default location.
`,

	"mcp": `usage: devmodels mcp

Serve this catalog's tools on stdio, for an agent that speaks the Model
Context Protocol. The protocol has stdout to itself, so --json does
nothing here, and the server answers for as long as stdin stays open.
`,

	"query": `usage: devmodels query [flags]

Criteria — every one must hold:
  --where EXPR
      metric>=N, and >, <=, <, = or != a value. A model without a value is
      excluded. Numbers may use K or M (200K); quote a value to compare it
      as text. A bare metric name (--where swe_bench_verified) requires a
      value to exist.
  --where-or-missing EXPR
      The same, but a model without a value stays eligible.

Ordering — applied in sequence. Ambiguous values sort after resolved ones,
missing values last:
  --order METRIC[:asc|desc]
      The default direction is the metric's own better direction.

Filters:
  --provider P
  --effort E
  --serving fast|standard
  --context-variant V
  --label TEXT

Evidence — applies to benchmark metrics, not to published Devin prices. A
--request term that already sets contexts or preferences keeps its own:
  --context CODE
      Only use observations from this evaluation context. Repeatable.
  --prefer-context CODE
      Prefer this context when equally ranked observations disagree.
      Repeatable, in order of preference.
  --allow-ambiguous
      Keep models whose disagreeing peer values straddle a criterion.
  --exact-effort
      Only use values measured at the model's own effort level.

Other:
  --limit N
      Default 10.
  --detail compact|summary|full
      JSON projection: terms listed once with positional values (default),
      per-value evidence objects, or full observation objects.
  --alternatives
      Include every non-selected observation. Implies --detail full.
  --request FILE
      Read a full JSON request, as accepted by the MCP query_models tool.
      "-" reads stdin.

Results are shown as a table while its columns fit, and one model at a
time once they do not. Either way the value columns are numbered and the
numbers are explained underneath.

Example:
  devmodels query \
    --where 'swe_bench_verified>=70' \
    --where-or-missing 'bfcl_overall_accuracy>=60' \
    --order swe_bench_verified:desc \
    --limit 5
`,
}
