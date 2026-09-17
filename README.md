# Devin Model Catalog

A local, factual catalog of the models available in a Devin licensing dataset —
their published prices or credit costs and the third-party benchmark evidence
about them — queryable from a shell or by an AI agent over MCP.

> **Devin Model Catalog is an independent third-party project.** It is not
> affiliated with, endorsed by, sponsored by, or supported by Cognition AI,
> Inc. Devin is a product of Cognition AI, Inc.

The command is `devmodels`. It runs entirely on your machine: a single Go
binary and a SQLite file. There is no server, account or telemetry, and
nothing is contacted until you run a refresh or install the browser runtime.

## What it is for

You describe a task to your agent. The agent works out what matters — say, a
strong issue-resolution score is required, tool-calling accuracy would be nice,
and cheaper is better — and asks `devmodels` which models in *your* Devin
dataset meet those criteria, with the evidence behind every number. The agent
then recommends a primary model and a backup.

`devmodels` itself stays factual:

- **It has no task profiles and no hidden ranking formula.** Criteria and
  ordering are always supplied by the caller.
- **Missing evidence is never zero.** Each criterion states whether a model
  without a value is excluded or kept.
- **It never picks the flattering number.** When equally strong evidence from
  different harnesses disagrees, the value is reported as ambiguous with every
  peer observation, until the caller chooses which context counts.
- **Every value carries provenance**: its source, the source's own model name,
  the harness or mode that produced it, whether it is exact for the model's
  reasoning-effort and serving variant, and what other observations exist.
- **An unfamiliar harness is still evidence.** A score from a harness or mode
  this version has never seen keeps its published name and becomes its own
  evaluation context, so it is usable immediately and no release is needed. It
  is never folded into a harness whose name merely looks similar — only
  spelling differences such as case and separators are treated as the same
  thing. A published row is set aside only when it carries nothing usable, and
  then the exact reason is recorded.

## Install

### Download a release

Each tagged release publishes a binary for every supported platform, plus a
`SHA256SUMS` file, on the repository's GitHub releases page:

| Platform | Asset |
|---|---|
| macOS arm64 | `devmodels_<version>_darwin_arm64.tar.gz` |
| macOS amd64 | `devmodels_<version>_darwin_amd64.tar.gz` |
| Linux amd64 | `devmodels_<version>_linux_amd64.tar.gz` |
| Linux arm64 | `devmodels_<version>_linux_arm64.tar.gz` |
| Windows amd64 | `devmodels_<version>_windows_amd64.zip` |
| Windows arm64 | `devmodels_<version>_windows_arm64.zip` |

Each archive holds just the executable and `LICENSE`. Put the executable
anywhere on your `PATH`; nothing else is installed and no administrator rights
are needed.

#### Check the download

`SHA256SUMS` lists all six archives, so checking it as a whole reports the five
you did not download as failures. Check just the one you have, by passing only
its line to the checker — no editing of `SHA256SUMS` required.

**macOS and Linux**, from the directory holding the archive and `SHA256SUMS`:

```sh
archive=devmodels_<version>_darwin_arm64.tar.gz
grep " $archive\$" SHA256SUMS | shasum -a 256 -c -    # Linux: sha256sum -c -
```

It prints `<archive>: OK` and exits 0 on a match, and fails loudly otherwise.
If you did download all six, `shasum -a 256 -c SHA256SUMS` checks them together.

**Windows PowerShell**:

```powershell
$archive  = 'devmodels_<version>_windows_arm64.zip'
$expected = ((Select-String -Path SHA256SUMS -Pattern ([regex]::Escape($archive) + '$')).Line -split '\s+')[0]
$actual   = (Get-FileHash -Algorithm SHA256 $archive).Hash.ToLower()
if ($actual -eq $expected) { 'OK' } else { "MISMATCH`nexpected $expected`nactual   $actual" }
```

#### Check where it came from

A checksum only proves the archive matches the `SHA256SUMS` you downloaded
beside it. Anyone who could replace the archive could replace that file too, and
the two would still agree.

Release archives also carry a signed build provenance attestation, which is a
different guarantee: that this exact file was produced by this repository's
release workflow, not built or altered somewhere else.

```sh
gh attestation verify devmodels_<version>_darwin_arm64.tar.gz --repo Tim-Butterfield/devin-model-catalog
```

Use both: the checksum catches a corrupted or truncated download, the
attestation catches a substituted one.

### Install with Go

```sh
go install github.com/Tim-Butterfield/devin-model-catalog/cmd/devmodels@latest
```

Needs [Go](https://go.dev/) 1.26.6 or later.

### Build from a checkout

```sh
make install                          # installs into $GOBIN or $(go env GOPATH)/bin, stamped with the git version
go build -o devmodels ./cmd/devmodels # or build a binary in place
```

`make install` needs GNU make, which is what the `Makefile` is written for, and
works from a Windows shell as well as a POSIX one. Without make,
`go install ./cmd/devmodels` does the same thing without the version stamp. The
Makefile's release targets call the scripts in `scripts/`, so those do need a
POSIX shell.

The binary is pure Go (no cgo) and needs nothing else at runtime. It has been
run natively on macOS arm64 and Windows arm64; the other four targets are
cross-compiled and tested in CI but have not been exercised natively, and the
optional browser runtime has no build for Linux arm64 or Windows arm64 (see
[known limitations](docs/known-limitations.md)).

## Quick start

```sh
devmodels datasets refresh     # read the datasets Devin currently publishes
devmodels datasets             # list them with short ids
devmodels use-dataset 2        # select yours by id
devmodels refresh              # load its catalog and every evidence source
devmodels status               # confirm the data is usable
```

```
ID  DATASET                      MODEL ROWS  SELECTABLE  SELECTED
1   Self-serve                   232         yes
2   Enterprise (ACUs)            216         yes         *
3   Legacy enterprise (credits)  203         yes
```

The ids are short-lived handles, renumbered `1..N` every time you refresh the
inventory. Your selection is stored by the dataset's structural identity, so
reordered or renamed tabs cannot silently switch you to a different price list.
If the selected dataset disappears from Devin's page, `refresh` fails and
leaves your catalog untouched until you select a current one.

Then query:

```sh
devmodels metrics              # what metrics exist, their meaning and coverage

devmodels query \
  --where 'swe_bench_verified>=75' \
  --where-or-missing 'bfcl_overall_accuracy>=60' \
  --order swe_bench_verified:desc \
  --order devin_output_price_usd_per_mtok:asc \
  --limit 5

devmodels model claude-opus-5-medium   # every fact and observation about one model
```

In text output, `*` marks a proxy value (not measured with Devin), `~` a value
borrowed from a sibling variant, `55..71?` an ambiguous value whose peers
disagree, and `—` a missing value. Choose a context with `--context` or
`--prefer-context` to resolve ambiguity. Add `--json` to data commands for
machine-readable output.

## Using it from an agent (MCP)

`devmodels mcp` serves the Model Context Protocol over stdio. For example, in a
client's MCP configuration:

```json
{
  "mcpServers": {
    "devmodels": { "command": "devmodels", "args": ["mcp"] }
  }
}
```

Tools:

| Tool | Purpose |
|---|---|
| `agents_md` | How to interpret the catalog and turn a task into criteria (the embedded [AGENTS.md](AGENTS.md)) |
| `data_status` | Whether data is usable: selected dataset, freshness, source failures, per-source `data_points`, rejected and unmatched counts |
| `describe_available_data` | Every metric with its meaning, unit, better direction, coverage, source and context codes, plus the query vocabulary; the default summary adds `evaluation_contexts_by_kind`, every context code in use grouped under its kind; `detail: "full"` replaces that with `evaluation_contexts` (every context the catalog knows, with its name) and adds source access bases, licences, attribution, `data_points` counts and lifecycle fields |
| `query_models` | Criteria with explicit missing and ambiguity policies, evidence context restrictions and preferences, ordering and limit; compact by default (terms listed once, each value with its state, evaluation context and weaker-evidence flags), `detail: "summary"` for per-value evidence objects, `detail: "full"` for complete observations |
| `get_model_details` | One model's metrics with their values, sources, evaluation contexts, flags, peer values and caveats; `detail: "full"` adds observation rows, alternatives and the published Devin row |

The tools are read-only. Refreshing is a CLI action. The same guidance is
available from a shell with `devmodels agents-md`.

### A Devin skill for choosing a model

`devin/skills/recommend-model/` is a portable [Devin
skill](https://docs.devin.ai/product-guides/skills): it recommends which model
to use for a task you describe, with this catalog as its only source of current
facts. The skill holds the recommendation policy and the durable meaning of
each metric; every price, score and model name in its answer comes from the MCP
server at the moment you ask.

1. Configure `devmodels mcp` in Devin, as above.
2. Copy the whole `recommend-model` folder into your Devin skills directory —
   `.agents/skills/recommend-model/` in a repository, or the per-user skills
   directory Devin documents for your platform. The folder is the unit; it
   needs nothing else from this repository.
3. Invoke it with the task you want a model for:
   `/recommend-model migrate a large service to a new database`. Invoked with
   no task, it asks for one rather than guessing.

The recommendation names the model to start a **new** conversation with for the
work — the skill does not switch the conversation you ran it in.

## Commands

| Command | Does |
|---|---|
| `datasets` / `datasets refresh` | List / re-read the published dataset inventory |
| `use-dataset <id>` | Select a dataset; a different selection clears the previous catalog immediately |
| `refresh [--source a,b] [--allow-shrink]` | Verify the selection, replace its catalog, refresh evidence sources; `--json` adds each source's `data_points` |
| `status` | Usability, freshness, per-source outcomes, problems |
| `metrics [--detail full]` | Metrics, sources and contexts; `--detail full` adds access bases, licences and attribution |
| `query` | Criteria, evidence context options, ordering and filters (`devmodels query --help`), or `--request file.json` for the full request, including `context_kinds` and `exact_serving` |
| `model <uid-or-label> [--detail compact\|full]` | Details for one model; full by default here, where nothing is charged for context |
| `unmatched [--source S]` | Evidence about models this dataset does not offer, with suggested identities to confirm as aliases |
| `rejected [--source S]` | Published rows that carried nothing usable, each with its exact reason |
| `alias add\|list\|remove` | Confirm that a source's model name denotes a catalog model |
| `import <file.json>` / `import remove <code>` | Add user-supplied observations ([format](docs/manual-import.md)) |
| `browser status\|install\|check` | Managed headless browser runtime; `check` drives a built-in test page |
| `config [show]` / `config set db-path PATH` / `config unset db-path` | Resolved paths and the database location |
| `agents-md`, `mcp`, `version` | |

Exit codes: `0` success, `1` failure, `2` invalid usage, `3` not ready or
invalid selection, `4` a source refresh failed (its previous data is kept).

### What the counts count

Three different things get counted, and they are not interchangeable:

- **rows** — what a source published. Every published row is `accepted`,
  `excluded` (hidden by the source's own display rules) or `rejected` (nothing
  usable, with a recorded reason), and `published = accepted + excluded +
  rejected`.
- **models** — entries in the selected dataset's catalog. This is the number
  `refresh` and `status` print next to the dataset name.
- **data points** — individual metric values stored from accepted rows,
  reported as `data_points`. One accepted catalog row publishes several: input,
  output and cache prices, the recommendation state, any long-context rates. A
  dataset of ~232 models therefore holds well over 1,200 data points. A count
  of 1221 is 1221 *values*, not 1221 models.

`unmatched` sits outside the row accounting: it counts accepted data points
whose model identity matches no model in the selected dataset. That is normal
for a leaderboard, which covers a far wider field than any one Devin dataset.

## Data sources

| Source | Provides | Basis |
|---|---|---|
| Devin models documentation | Datasets; catalog models; token prices, long-context pricing and recommended flag (token-priced datasets); credit costs and recommended flag (legacy credits dataset) | The page's Markdown alternate, listed in the site's `llms.txt` |
| Epoch AI benchmarking data | SWE-bench Verified, DeepSWE, Terminal-Bench, FrontierCode, with effort and harness | Public bulk download, Creative Commons Attribution 4.0 |
| Berkeley Function-Calling Leaderboard | Overall tool-calling accuracy by evaluation mode | Data file in the project's public Apache-2.0 repository |

Each source's access basis and attribution are stored with its data and shown
by `devmodels metrics --detail full`. Details, and the sources deliberately **not** collected
(Artificial Analysis, the OpenRouter models API, the swebench.com leaderboard
files), are in [docs/sources.md](docs/sources.md).

> Benchmark data from Epoch AI: *Epoch AI, 'Capabilities & Benchmarking'.
> Published online at epoch.ai. Retrieved from 'https://epoch.ai/benchmarks'.*
> Licensed CC BY 4.0.

Benchmark values describe models under other harnesses or direct evaluation.
They are proxies for behaviour in Devin, and are marked as such.

Context windows are not collected automatically in this release (see
[known limitations](docs/known-limitations.md)); you can import them.

## Browser runtime

Every source collected today is read without a browser. For sources that need
JavaScript or interaction, `devmodels` manages its own headless browser: on
first use it downloads a pinned Chrome for Testing build into your cache
directory and drives it over the Chrome DevTools Protocol. You do not install
Chrome, Node, npm or Playwright. `devmodels browser check` provisions it if
needed and runs a self-test; `devmodels browser status` reports its state.
Chrome for Testing is published for macOS, Linux x64 and Windows x64 and x86
only, so there is no browser runtime on Linux arm64 or Windows arm64.

## Where data lives

| Platform | Database (default) | Config file | Cache (browser runtime) |
|---|---|---|---|
| macOS | `~/Library/Application Support/devmodels/devmodels.db` | `~/Library/Application Support/devmodels/config.json` | `~/Library/Caches/devmodels/` |
| Windows | `%LOCALAPPDATA%\devmodels\devmodels.db` | `%APPDATA%\devmodels\config.json` | `%LOCALAPPDATA%\devmodels\cache\` |
| Linux | `$XDG_DATA_HOME/devmodels/devmodels.db` (`~/.local/share`) | `$XDG_CONFIG_HOME/devmodels/config.json` (`~/.config`) | `$XDG_CACHE_HOME/devmodels/` (`~/.cache`) |

On Linux an XDG variable that is not an absolute path is ignored. The database
path can be set with `devmodels config set db-path PATH`, for one invocation
with `--db PATH`, or with `DEVMODELS_DB`; `DEVMODELS_CONFIG` points at a
different config file. `devmodels config` shows what is in effect and why.

## Documentation

- [AGENTS.md](AGENTS.md) — guidance for agents using the catalog
- [docs/architecture.md](docs/architecture.md) — layers, data model, refresh and evidence rules
- [docs/sources.md](docs/sources.md) — collectors, access bases, and sources not collected
- [docs/manual-import.md](docs/manual-import.md) — importing your own observations
- [docs/known-limitations.md](docs/known-limitations.md)
- [docs/development.md](docs/development.md) — building, testing, validation, adding a source
- [CONTRIBUTING.md](CONTRIBUTING.md) — what a change has to satisfy
- [SECURITY.md](SECURITY.md) — reporting a vulnerability, and what is in scope
- [CHANGELOG.md](CHANGELOG.md) — what each release contains

## Licence

[MIT](LICENSE).

Devin is a trademark of Cognition AI, Inc. This project uses the name only to
identify the product it works with, and uses no Cognition branding. Third-party
data remains subject to its publishers' terms; see [docs/sources.md](docs/sources.md).
