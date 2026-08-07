# Review process

Review is two repo-local agents plus a decision skill, run **before** a PR — not CI
auto-gates; the developer chooses to run them.

## The reviewers

- **`code-reviewer`** (`.claude/agents/code-reviewer.md`, invoked via `/code-review`) —
  reviews a change against the PRD §5.x invariants, routing by subsystem. Builds/tests the
  real change (`-race` where it matters) and RED-sanity-checks bug fixes. Routes include
  concurrency (§5.14), migration (§5.13), secrets (§5.18), store/search (§5.1), vendor
  (§5.4), jobs (§5.10), gateways (§5.5), **documentation** (see [doc obligations](doc-obligations.md)),
  and cross-surface drift.
- **`adversary`** (`.claude/agents/adversary.md`) — the mandatory second pass on Tier-3
  changes (concurrency, on-disk/migration, secrets, auth seams). Tries to break things under
  an executable-finding standard, or enumerates exactly what it failed to break.

## Decisions

- **`panel`** (`.claude/skills/panel/`) — for a genuine design fork (two defensible shapes, or
  a Tier-3/doctrine decision): independent panelists, a decision rule written before anyone
  runs, a judge that may not invent a new option. The decision and the losing arguments are
  recorded in the go-parts memory vault — see [memory-substrate.md](memory-substrate.md).

## The documentation gate

No behavior change lands without the documentation it implies. A missed obligation is a
**blocking** finding, not a nit. See [doc-obligations.md](doc-obligations.md).
