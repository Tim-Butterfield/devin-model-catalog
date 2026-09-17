# Security

## Reporting a vulnerability

Report privately through GitHub's [private vulnerability
reporting](https://docs.github.com/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability)
on this repository — **Security → Report a vulnerability** — rather than in a
public issue.

Please include what an attacker can do, the steps that demonstrate it, and the
`devmodels version` output. Reports are acknowledged as soon as they are read.
This is a personal project with no on-call rotation, so please allow a
reasonable period before disclosing publicly.

## What is in scope

`devmodels` is a local command-line tool and MCP server. It has no server side,
no accounts and no telemetry, so the interesting boundaries are the data it
reads and the files it writes:

- **Source content.** Anything fetched from the Devin models documentation,
  Epoch AI or the BFCL leaderboard is untrusted input. A parser that can be
  made to crash, hang, consume unbounded memory, or attribute a value to the
  wrong model is a bug worth reporting.
- **Manual import files.** `devmodels import` reads a user-supplied JSON
  document. Validation is meant to be strict and all-or-nothing.
- **The browser runtime.** `devmodels browser install` downloads and extracts a
  Chrome for Testing archive. Anything that lets an archive write outside the
  cache directory — a traversal path, a symlink escape — is in scope.
- **The database and config file.** Paths come from the environment, a flag or
  the config file. A path that escapes where it should write is in scope.
- **The MCP server.** Its tools are read-only by design. A request that mutates
  state through them is in scope.

## What is not in scope

- The accuracy of third-party benchmark data or published prices. `devmodels`
  reports what its sources publish; a wrong number upstream is a data issue,
  not a vulnerability. Misattributing a correct number to the wrong model *is*
  a bug — report it as one.
- Vulnerabilities in Chrome for Testing itself. Report those to the Chromium
  project. What belongs here is how `devmodels` obtains and runs it.
- An attacker who already has write access to the user's account. The database,
  config file and cache have no privilege boundary against their own owner.

## Supported versions

The most recent commit on the default branch is what receives fixes. There are
no long-lived maintenance branches.

## Known limitations

Several deliberate trade-offs are documented rather than fixed — for example,
the browser archive is not verified against a publisher checksum, because
Chrome for Testing does not publish one. See
[docs/known-limitations.md](docs/known-limitations.md) before reporting one of
those as a finding.
