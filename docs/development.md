# Development

Requires Go 1.26.6 or later. No cgo, Node or other toolchain.

## Build

```sh
make install             # go install with the git-derived version into $GOBIN or $(go env GOPATH)/bin
make install VERSION=v0.1.0
go build -o devmodels ./cmd/devmodels
go build -ldflags "-X github.com/Tim-Butterfield/devin-model-catalog/internal/buildinfo.Version=v0.1.0" -o devmodels ./cmd/devmodels
```

Cross-compile any supported target with `CGO_ENABLED=0 GOOS=… GOARCH=…`.

`install` is written to run under whatever shell make picked, including
`cmd.exe`: its recipe is two commands with no shell syntax, and the version and
install directory are resolved by make rather than by the shell. The `release`
and `release-check` targets call the scripts in `scripts/` and so need a POSIX
shell; on Windows that means Git Bash or WSL. The `Makefile` is GNU make syntax
throughout — `nmake` and the other Windows makes cannot read it.

## Test

```sh
go test ./...            # deterministic; no network
go test -race ./...
scripts/validate.sh      # gofmt, go mod verify and tidy check, vet, tests, race, cross-compile, tagged builds
scripts/validate_test.sh # checks that validate.sh reports failures, using stub go and gofmt commands
scripts/release_test.sh  # checks release.sh's version rules, target matrix, packaging and checksums
scripts/workflow_test.sh # checks the workflows' privilege split, artifact handoff and action pins
```

`validate.sh` runs every check even when an earlier one fails and exits
non-zero if any failed. CI (`.github/workflows/ci.yml`) runs both scripts on
Linux, plus `go vet` and `go test` on macOS and Windows, for every push and pull
request; like the scripts, it never contacts the live sources or downloads the
browser runtime.

Two network-dependent suites are kept behind build tags:

```sh
go test ./internal/app -run TestLiveRefresh -tags=live -v
go test ./internal/retrieval/browser -run TestRealBrowserRuntime -tags=browser -v
```

- `live` refreshes the dataset inventory, then selects and refreshes each
  supported dataset in turn (evidence sources with the first, the Devin catalog
  for each), and logs per-source counts and metric coverage. A sharp change in those counts usually means a source
  changed shape.
- `browser` downloads the pinned Chrome for Testing build (about 200 MB
  extracted) and drives it against a local page: client-rendered content,
  waits, clicks, script evaluation, text/attribute/HTML reads, a bounded
  timeout, reuse without a second download, and a failed upgrade that leaves the
  install intact. Set `DEVMODELS_BROWSER_TEST_ROOT` to reuse a runtime directory
  between runs.

`devmodels browser check` performs the same kind of end-to-end check against a
built-in page from an installed binary.

## Releasing

A release is driven entirely by a version tag. Pushing `vX.Y.Z` runs
`.github/workflows/release.yml`. Nothing is published without a tag, and
ordinary CI holds no release permissions.

The workflow is two jobs, split so that no single job can both run repository
code and publish:

- **build** (`contents: read`) checks out, validates the tag, extracts the
  notes, builds and packages every target, verifies the checksums it produced,
  and uploads the payload as an artifact. It cannot create a release, mint an
  OIDC token or write an attestation.
- **publish** (`contents: write`, `id-token: write`, `attestations: write`)
  never checks out and never runs repository code. It downloads the payload,
  checks the inventory is exactly the six archives plus `SHA256SUMS` and the
  notes — rejecting anything missing or extra — re-verifies the checksums after
  the handoff, attests, and creates the release.

`scripts/workflow_test.sh` asserts that split, so an edit that quietly gives the
build job a publishing token fails in CI.

The workflow builds by running `scripts/release.sh`, which is the same script
you can run locally, so a dry run produces what a tag would publish:

```sh
make release RELEASE_VERSION=v0.1.0   # or: scripts/release.sh v0.1.0
```

Artifacts land in `dist/`, which is ignored and rebuilt from scratch on every
run:

```
devmodels_0.1.0_darwin_arm64.tar.gz    devmodels_0.1.0_windows_amd64.zip
devmodels_0.1.0_darwin_amd64.tar.gz    devmodels_0.1.0_windows_arm64.zip
devmodels_0.1.0_linux_amd64.tar.gz     SHA256SUMS
devmodels_0.1.0_linux_arm64.tar.gz
```

Each archive contains only the executable and `LICENSE` — a binary archive is
not a copy of the source tree. Verify the checksums the way a user would:

```sh
cd dist && sha256sum -c SHA256SUMS    # or: shasum -a 256 -c SHA256SUMS
```

**Version injection.** The tag is passed to the linker as
`-X …/internal/buildinfo.Version=vX.Y.Z`, the same mechanism `make install`
uses, so `devmodels version` in a release binary reports exactly the tag.
Release builds add `-buildvcs=false`: without it Go stamps the building
repository's commit hash and dirty flag into the binary, which is both noise and
a leak of whatever tree happened to produce it.

**Reproducibility.** Two runs from the same source and version produce
byte-identical archives and executables. That relies on `-trimpath`,
`-buildvcs=false`, a fixed timestamp for everything entering an archive (the
commit date, or `SOURCE_DATE_EPOCH` when set), a fixed file order, and `gzip -n`
so the gzip header carries no name or time.

**Release notes** are the version's section of [CHANGELOG.md](../CHANGELOG.md).
`scripts/release.sh --notes vX.Y.Z` prints it, and the workflow fails before
building if the section is missing, so a tag cannot publish an empty release.

**Before tagging**, run `scripts/release_test.sh` and a local dry run, and
confirm the native binary reports the version you expect.

## Human output is laid out for 80 columns

Everything the command line composes for a person to read is laid out for 80
columns: help, usage, tables, notes, warnings and errors. `internal/cli.Width`
is that number, and `wrap`, `truncate` and `pad` beside it are how output
reaches it.

It is a fixed design width, not a measurement. Nothing detects the terminal,
deliberately: output that depends on the window produces different bytes in
different places, which is worse for anyone reading it later in a log, a
transcript or a pasted issue.

What the rule does not cover:

- **JSON and the MCP payloads.** They are not read as columns and are not
  width-limited. `--json` always carries values whole.
- **`agents-md`.** It returns `AGENTS.md` verbatim; byte identity with the file
  is the contract, and the document's own width is not this program's business.
- **A single raw value that cannot be broken** — a filesystem path, a URL, a
  long identifier. Cutting one across lines makes it uncopyable and truncating
  one makes it wrong, so it is allowed to run long on a line of its own. In
  practice this is the database path in `status` and `config`.

Per surface, long values are handled deliberately rather than by accident:

| Surface | Policy |
|---|---|
| Paths (`status`, `config`) | Never truncated. Labelled line of its own; runs long if it must |
| Rejection reasons (`rejected`) | Never truncated; wrapped under the row. The reason is the point |
| Suggested identities (`unmatched`) | Wrapped onto their own line under the entry |
| Source model names (`unmatched`) | Truncated to a column budget; `--json` has them whole |
| Model labels and uids (`query`) | The uid column sizes itself to the widest uid present, because that is what you copy into `devmodels model`; the label gives way first |
| Metric keys (`metrics`, `model`) | Never truncated — each takes its own line, described beneath |

A query can name more metric terms than any row has room for, and there the
columns do not give way further — the layout does. `queryTableFits` is the
rule: the rank column plus the label, uid and value floors have to add up to
no more than the width, which holds up to six value columns. Past that the
results are stacked, one model per block, each value on its own line under the
number the legend already gave it. The alternative was a table whose columns
had all been squeezed past the width where they still said anything.

Help has two levels for the same reason. `devmodels` on its own is a landing
page — what this is and which command to reach for — and each command's
`--help` is the authority on that command, including the shape of a file it
reads. Both come from `internal/cli/help.go`.

Wrapping and truncation measure display columns, not bytes, and cut on
character boundaries: a name in a script whose characters are two columns wide,
or one carrying a combining mark, is shortened correctly rather than corrupted.

`internal/cli/cli_width_test.go` enforces this. It discovers the commands from
the help text rather than listing them, so a new command with a long
description fails the suite rather than quietly wrapping in someone's terminal.
Lines over the width are only accepted when they consist of one unbreakable
token, which is a classification the test makes rather than a list of
exemptions — a long sentence is made of short words and is never excused.

## Fixtures

Parser and integration tests use documents synthesized in the published shapes
by `internal/testfixtures` (a Devin models page including the legacy
`allModels` literal, an Epoch AI archive, a BFCL CSV). They are not copies of
the live sources: the repository does not redistribute third-party datasets,
and tests do not depend on the network. When a source changes shape, update the
fixture builder to the new shape and add a test for the change.

## What the tests cover

| Invariant | Where |
|---|---|
| Dataset ids regenerate from 1 on every inventory refresh | `app`, `store` |
| Reordering or renaming tabs cannot change the selected dataset | `app`, `devin` |
| A vanished or look-alike selected dataset fails refresh without touching the catalog | `app` |
| Network, truncated and reshaped Devin responses preserve the catalog | `app`, `devin`, `retrieval` |
| Switching datasets makes queries fail until refreshed, never returning prior models | `app` |
| Legacy credits dataset: strict literal parsing, selection, refresh, credits metric, identity, idempotence, fail-closed on non-literals | `devin`, `app`, `identity` |
| Repeated refreshes do not duplicate observations or inflate alternatives | `app` |
| A failed collector keeps its previous evidence | `app` |
| New metrics need no schema change; no benchmark-specific columns exist | `app` (import), `store` |
| Missing is not zero; missing allow/reject; eligibility separate from ordering | `query` |
| Peer contexts that disagree are ambiguous, never resolved to the favourable value; context restriction/preference; ambiguous criteria and ordering | `query`, `app` |
| Exactness, proxy flags, stronger evidence over peers, aliases | `query`, `app` |
| Query `detail` projections: compact (default) lists terms once with positional values, context and flags; summary adds per-value evidence objects; full keeps observations; identical semantics in all three; compact and summary materially smaller than full | `query`, `app`, `mcpserver`, `cli` |
| CLI table keeps repeated metrics under different evidence policies as separate columns; CLI evidence flags leave published prices alone | `cli` |
| Query results stay tabulated while the columns fit and stack once they do not, at the width the floors allow and not before; the numbered legend, the evidence marks and the term order are the same in both layouts | `cli` |
| Malformed collector input (unreadable CSV rows, corrupt archive members, NaN or infinite values, reshaped page fields) is rejected with an exact reason or fails the source, never crashes or misfiles a value | `epoch`, `bfcl`, `devin` |
| Snapshot reads use one transaction; an import cannot redefine an existing evaluation context, and neither can a collector | `app`, `store` |
| Row accounting (`published = accepted + excluded + rejected`) and identity collisions | `devin`, `epoch`, `bfcl`, `app` |
| `data_points` counts individual metric values, so it is neither the row count nor the model count; the catalog's data points exceed its rows, and every public surface emits that one name | `app`, `cli` |
| The normal `refresh` table carries the row accounting only, fits 80 columns at live count sizes, and says something further only when there are rejected rows or unmatched evidence to inspect | `cli` |
| A harness or mode this build does not curate becomes its own context, keeps its published name, and is never folded into a neighbour; spelling variants (case, separators) are one context | `sources`, `epoch`, `bfcl`, `app` |
| A blank versioned identifier falls back to the source's name column; a row with no identity at all is rejected | `epoch` |
| Upgrading a schema-1 database drops rows held back only for an unfamiliar label, keeps genuine rejections, and reclassifies collided catalog rows | `store` |
| `get_model_details` projections: compact (default over MCP) keeps value, state, source, context, flags, peers and caveats; full adds observations and the published row; identical semantics | `query`, `mcpserver` |
| MCP tool declarations stay within a byte ceiling and send the evidence policy once, without weakening the request contract | `mcpserver` |
| Embedded AGENTS.md equals the file, the CLI output and the MCP tool output | `cli`, `mcpserver` |
| The shipped Devin skill names only tools, metric keys and request fields the running server actually has, shows only requests the query schema accepts, and states no model, price or score of its own | `mcpserver` |
| CLI JSON and MCP results equal the service's result for the same query | `cli`, `mcpserver` |
| Migrations idempotent; database path precedence; per-OS path layout | `store`, `config` |
| Browser runtime provisioning, reuse, upgrade, failure cleanup, zip-slip and escaping links, waiting install lock, launch-failure diagnostics | `retrieval/browser` (real browser: `browser` tag) |

## Project rules

- The CLI and MCP server stay thin; logic belongs in `app` and below.
- Metrics are data. Do not add metric-specific columns or query branches.
- Evidence selection ranks by evidence strength only, never by value.
- Collectors fail closed on structural change and account for every row.
- A source-provided context label this build has not seen becomes its own
  context, never one that already exists, and never by similarity. Context
  kinds stay a closed set this project owns.
- A row is rejected only when it cannot be represented at all, with an exact
  reason. An unfamiliar label is not a reason.
- Browser automation is used only where a source needs it and its terms allow
  automated access.
- `AGENTS.md` describes semantics, never current models or values.
- Every collector documents its access basis in code and in `docs/sources.md`.
