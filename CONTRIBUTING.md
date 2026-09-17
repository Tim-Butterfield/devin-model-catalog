# Contributing

Thanks for looking. This is a small, opinionated project; the rules below exist
because they are what makes the catalog trustworthy, not as ceremony.

Please open an issue before a large change, so we can agree on the shape before
you spend time on it. Small fixes can go straight to a pull request.

## Before you push

```sh
scripts/validate.sh       # gofmt, module hygiene, vet, tests, race, cross-compiles, tagged builds
scripts/validate_test.sh  # checks that validate.sh actually reports failures
scripts/release_test.sh   # checks the release script's packaging and version rules
scripts/workflow_test.sh  # checks the workflows' privilege split and action pins
```

`validate.sh` is what CI runs, and it runs every check even when an earlier one
fails, so one pass tells you everything that needs fixing. It needs a POSIX
shell; on Windows, run the individual `go` commands or use WSL.

Requires Go 1.26.6 or later. Nothing else — no cgo, Node or npm.

## What the tests must stay

**Deterministic tests never touch the network.** `go test ./...` must pass with
no connectivity. Parser and integration tests use documents synthesized in the
published shapes by `internal/testfixtures`; they are not copies of live source
data. The two network-dependent suites are behind build tags (`live`,
`browser`) and are never part of the default run.

**Do not commit upstream data as a fixture.** The repository does not
redistribute third-party datasets. If a source changes shape, update the fixture
*builder* to produce the new shape and add a test for the change.

**Cover the failure path, not just the happy one.** A collector that silently
drops a row, or reports a partial refresh as success, is the class of bug this
project cares most about.

## Adding or changing a data source

A new source needs a defensible access basis before it needs code. Concretely:

1. Establish that the publisher's terms permit automated access for this
   purpose, and record the access basis, licence and attribution in the
   collector's `Info()`.
2. Document the same thing in [docs/sources.md](docs/sources.md), including the
   date the terms were checked.
3. Satisfy the attribution the licence requires, everywhere the data appears.
4. If the terms do not permit collection, the source is not collected. Several
   are deliberately left out for exactly this reason, and each records why.
   Browser automation does not change a source's terms.

The full walkthrough is in
[docs/architecture.md](docs/architecture.md#adding-a-source).

## Project rules worth knowing

These are enforced by tests, and a change that breaks one is a change of intent,
not a refactor:

- The CLI and MCP server stay thin; catalog logic lives in `app` and below.
- Metrics are data. No metric-specific columns, no metric-specific query
  branches.
- Evidence is ranked by strength of evidence only — never by how favourable the
  value is. When equally strong evidence disagrees, the answer is "ambiguous",
  not a choice.
- Missing is never zero.
- A row is rejected only when it cannot be represented at all, with an exact
  reason recorded. An unfamiliar harness name is not a reason to reject a row;
  it becomes its own evaluation context.
- Collectors fail closed on structural change, and account for every published
  row.
- `AGENTS.md` describes semantics. It never names a current model, score or
  price, and it is embedded verbatim — tests assert the file, the CLI output and
  the MCP tool output are byte-identical.
- Human command-line output is laid out for 80 columns, using the helpers in
  `internal/cli/width.go`. Nothing detects the terminal. JSON and MCP payloads
  are not width-limited. See
  [docs/development.md](docs/development.md#human-output-is-laid-out-for-80-columns).

More detail: [docs/development.md](docs/development.md).

## Commits and pull requests

Write the commit subject as what the change does, in the imperative. Keep the
diff to one concern. If behaviour changed, say so in the body, and update the
documentation in the same commit — `docs/` and `README.md` are expected to
describe what the code actually does.

## Licence

Contributions are accepted under the [MIT licence](LICENSE) that covers the
project. Third-party data remains subject to its publishers' terms.
