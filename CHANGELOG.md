# Changelog

Releases of `devmodels`, newest first. Each tagged release publishes binaries
for the supported targets and a `SHA256SUMS` file; see
[README](README.md#install) for how to verify and install them.

Release history begins with v0.1.0.

## v0.1.1

Two fixes. Nothing about the catalog, the query semantics, the JSON
projections or the MCP tools changed.

- **`make install` works under a Windows shell.** Its recipe ended in a line of
  POSIX shell, which `cmd.exe` read as a call to its own `dir` builtin, so the
  target failed with `Parameter format not correct` even though `go install`
  had already succeeded. make now resolves the version and the install
  directory itself, leaving two commands any shell can run. This needs GNU
  make: the `Makefile` is GNU syntax throughout, and the `release` targets call
  the scripts in `scripts/` and so still need a POSIX shell.
- **`devmodels config` says when the config file does not exist yet.** It is
  written only when a value is set, so it is routinely absent; the line now
  reports that instead of leaving a reader to go looking for a file that was
  never created.

## v0.1.0

First public release.

`devmodels` is a local, factual catalog of the models available in a Devin
licensing dataset — their published prices or credit costs and the third-party
benchmark evidence about them — queryable from a shell or by an AI agent over
MCP. It runs entirely on one machine: a single Go binary and a SQLite file, with
no server, account or telemetry.

- **Dataset-aware catalog.** Reads the dataset inventory Devin publishes,
  selects one by its structural identity rather than its position or title, and
  loads its models, token prices or legacy credit costs, and recommended flags.
- **Third-party benchmark evidence.** Collects SWE-bench Verified, DeepSWE,
  Terminal-Bench and FrontierCode from Epoch AI (CC BY 4.0), and tool-calling
  accuracy from the Berkeley Function-Calling Leaderboard, matched to catalog
  models through an explicit identity layer.
- **Evidence with provenance, not scores.** Every value carries its source, the
  source's own model name, the evaluation context that produced it, and whether
  it is exact for the model's reasoning effort and serving variant. Missing is
  never zero.
- **Ambiguity is reported, not resolved.** When equally strong evidence from
  different harnesses disagrees, no value is chosen; the range and every peer
  observation are returned until the caller picks a context.
- **No ranking formula.** Criteria, ordering and missing-evidence policy are
  supplied by the caller. The tool answers questions; it does not recommend.
- **MCP server.** `devmodels mcp` exposes five read-only tools over stdio, with
  the agent guidance from `AGENTS.md` embedded in the binary.
- **Manual import.** Evidence that cannot be collected automatically can be
  supplied as a JSON document under a source code of its own.
- **Managed browser runtime.** For sources that need JavaScript, `devmodels`
  provisions its own pinned Chrome for Testing build; no collector currently
  requires it.

Requires Go 1.26.6 or later to build from source. Prebuilt binaries are
published for macOS (arm64, amd64), Linux (amd64, arm64) and Windows (amd64,
arm64).
