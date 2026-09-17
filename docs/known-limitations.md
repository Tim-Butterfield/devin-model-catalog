# Known limitations

## Data coverage

- **No context windows out of the box.** Devin publishes no context window, and
  the automated source that carries them (OpenRouter) is not collected
  ([sources.md](sources.md)). Import them if you need them.
  `devin_long_context_threshold_tokens` is a pricing threshold, not a context
  window.
- **Benchmark coverage is sparse.** Leaderboards cover a wide field and only
  some rows correspond to models Devin offers. On 2026-09-14 a self-serve
  refresh matched SWE-bench Verified values for 22 of 229 catalog models. Use
  `missing: "allow"` where absence should not disqualify.
- **All benchmark evidence is proxy evidence** unless its context is `devin`.
  Scores from different harnesses are not directly comparable.
- **Ambiguity is common.** Many models are published under several harnesses
  (Terminal-Bench, FrontierCode) or modes (BFCL FC and Prompt). Such values are
  reported as ambiguous until a query restricts or prefers a context, and by
  default a criterion excludes a model whose disagreeing peers straddle it.
  This is deliberate: the catalog does not choose among incomparable contexts.
  Because a harness this version has not seen is ingested as its own context
  rather than held back, disagreeing peers are ordinary rather than rare. Use
  `evidence.contexts` or `prefer_contexts` to pick the harness your task cares
  about.
- **Availability is an upper bound.** The catalog is the published price list
  for a dataset. Organisation or group model restrictions are not published and
  are not modelled.
- **Legacy credits have no published unit semantics.** `devin_legacy_credits`
  is stored exactly as the table shows it and is only comparable within the
  legacy dataset. Legacy rows publish no model id, so their uid is derived from
  the name and changes if Devin renames a row. The table's `hasGift` marker is
  kept only in the raw row.
- **Promotional prices** described in page notes (for example, temporary free
  periods) are not parsed; the recorded value is the table value.
- **BFCL's published file can lag** current model releases.

## Identity

- Label classification is conservative. "Claude Opus 4.6 Thinking" keeps
  "Thinking" in its base name (the effort it denotes is unstated), so evidence
  for "Claude Opus 4.6" does not attach to it. Use an alias if you judge them
  equivalent.
- Names that differ in spelling beyond version dots, dates, parentheses and
  punctuation do not match automatically. `devmodels unmatched` lists stored
  evidence with suggested catalog identities; confirm with
  `devmodels alias add`.
- An evaluation harness this version does not know gets its own context rather
  than a release, but two spellings that differ by more than case and separator
  characters stay two contexts. If a source publishes the same harness as both
  "ForgeCode" and "Forge Code", the catalog treats them as different and no
  setting merges them; nothing in the source says they are the same product.
  The same goes for a harness that is renamed upstream: the old and new names
  are separate contexts until a release records the judgement that they are one
  (as it does for "Grok Shell" and "Grok Build").
- BFCL identity is read by stripping a trailing parenthetical as the evaluation
  mode. A model whose published name genuinely ends in a parenthetical that is
  not a mode would have it read as one: the value stays in its own distinct
  context and is never merged with a curated mode, but the model name loses the
  suffix. The live leaderboard publishes no such name today.
- There is no configured per-metric context preference. Preferences are stated
  per query (`prefer_contexts`), because which harness or mode matters depends
  on the task.
- `describe_available_data` grows with the number of evaluation contexts in
  use, and ingesting uncurated harnesses means there are many. Its default
  summary lists only contexts some metric actually uses, and states each of
  those codes twice: once in the metric that uses it, once in
  `evaluation_contexts_by_kind`. The full list of contexts the catalog knows,
  each with its name, is sent only by `detail: "full"`.
- The default `describe_available_data` response is the largest of the MCP
  tool responses, and most of it is not repetition: metric descriptions and
  the JSON field names of fifteen metric objects. The one large repetition is
  outside the payload — the MCP result carries the same JSON twice, once as
  `structuredContent` and once as an escaped text block, which costs more than
  the payload itself (19,838 wire bytes for a 9,364-byte response on the live
  catalog).

## Runtime

- The browser runtime download is not verified against a publisher checksum:
  Chrome for Testing does not publish one. It is fetched over HTTPS from
  Google's storage and its SHA-256 is recorded in the manifest.
- No shipped collector currently uses the browser runtime. Real provisioning
  and interaction have been exercised with a local test page and
  `devmodels browser check`, on macOS arm64 only.
- The browser runtime is about 200 MB once extracted, in the per-user cache.
- Archive members (the Epoch AI zip, the browser runtime) are not capped by
  decompressed size; downloads are size-capped and fetched over HTTPS from the
  publishers.
- An Epoch AI row whose reasoning-effort column holds an unrecognised value (a
  number, say) takes its effort from the model identifier instead. When last
  checked, the only such rows named no model in any Devin dataset.
- The Epoch AI collector does not detect an archive that contains two members
  with the same file name; the published archive has none.
- If a later release adds a built-in metric whose key a manual import already
  uses, the database cannot be opened until that import's rows are removed.
- Deferred write transactions can fail at once with a busy error when two
  processes write within milliseconds of each other; retrying succeeds.
- Chrome for Testing publishes builds for macOS (arm64, x64), Linux x64 and
  Windows (x64, x86) only. On Linux arm64 and Windows arm64, `devmodels browser
  install` and `check` report that no build is available; nothing else depends
  on the browser today.
- One SQLite connection per process; a CLI refresh and a running MCP server can
  share a database (WAL mode, busy timeout), but concurrent refreshes from two
  processes are not coordinated beyond SQLite locking.

## Validation

- Development and runtime validation, including the real browser runtime, were
  performed natively on macOS arm64. The test suite, a live refresh, the CLI
  and the MCP stdio server have also been run natively on Windows arm64 (build
  26200); the race detector is not available for windows/arm64, and neither is
  the browser runtime. Linux (amd64, arm64), Windows amd64 and macOS amd64
  builds are cross-compiled on every validation run but have not been run
  natively; Linux path resolution is unit-tested only.
- Output is UTF-8, and a few characters carry meaning: `—` marks a missing
  value, `·` separates facts, `…` marks a shortened value. Windows Terminal and
  PowerShell 7 render them; a legacy console window still on code page 437 or
  1252 shows them as mojibake. `chcp 65001` before running, or a modern
  terminal, fixes it. Nothing is lost either way — `--json` carries the same
  values without any of these markers.
- `devmodels mcp` answers requests only while its input stays open, as MCP
  clients keep it; piping a fixed batch of requests and closing input ends the
  session before replies are written.
- Releases publish a plain executable per platform, not an installer: there is
  no package-manager entry, no code signing and no notarisation. macOS
  Gatekeeper therefore quarantines a downloaded build until it is cleared
  (`xattr -d com.apple.quarantine devmodels`), and Windows SmartScreen may warn
  on first run. The published `SHA256SUMS` and the build provenance attestation
  are what establish that a download is the artifact this repository's workflow
  produced.
- Release archives for the four targets that have not been run natively
  (Linux amd64 and arm64, Windows amd64, macOS amd64) are cross-compiled and
  covered by CI, but nobody has executed those binaries on real hardware.
