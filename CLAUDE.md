# CLAUDE.md — go-parts

> Project constitution for Claude Code sessions. Distilled from
> `docs/internals/go-parts-prd.md` (v3.7). Not a style guide — a set of
> load-bearing decisions and the reasons they beat the alternatives.

## 1. What go-parts is

A single-binary, embedded-storage **electronics parts database**: one Go binary
over a Pebble KV store, queryable via REST / RPC / MCP / Web / TUI, that slots
into AI-assisted workflows ("do I have a 10k 0805 resistor?"). Same low-friction
philosophy as **go-rag** and **MuninnDB** — one binary, no mandatory external
services, multiple protocol surfaces over a shared embedded store.

**Architecture map** (PRD §5.x — each subsystem owns one concern):

| Subsystem | PRD | Owns |
|---|---|---|
| Store | §5.1 | Pebble keyspaces; BM25 search default-on, vector search optional/additive |
| Interfaces | §5.2 | RPC (wire) + REST + MCP + Website + TUI over one core |
| Vendor plugins | §5.4 | Compiled-in `VendorPlugin` registry; per-failure retry classification |
| Gateways | §5.5 | Optional go-rag (search) + MuninnDB (memory); remote mode, additive |
| Background jobs | §5.10 | In-process Pebble-backed queue; bulk import returns before enrichment |
| Schema migration | §5.13 | `schema_version` marker; auto, non-interactive, snapshot-first |
| Concurrency | §5.14 | Commutative stock deltas (striped locks) + optimistic `version` on edits |
| Secrets | §5.18 | Env-var only, **never** in `.go-parts/config.json` or backups |

## 2. Core principles

- **Low friction is load-bearing (§2).** Every feature is measured against
  "still works in under 5 minutes with zero config." BM25 search runs from first
  start with no model, no API key, no external call.
- **Degrade loudly-but-gracefully, never silently-wrong.** A gateway down falls
  back to standalone operation (§5.5); a stale optimistic-concurrency write is
  **rejected**, never silently overwritten (§5.14); enrichment gaps surface via
  `enrichment_status`, not missing data (§5.10).
- **Explicit config is never silently substituted.** A schema-version mismatch
  is a **hard startup failure**, never a silent downgrade-write (§5.13). A
  newer-version store against an older binary refuses to start.
- **Extend proven in-tree mechanisms over inventing new architecture.** Reindex
  reuses the background job queue (§5.15); AI enrichment reuses the vendor-plugin
  loading pattern (§5.9); the Via resolver is one generic endpoint, not one per
  entity (§5.17); datasheet storage reuses go-rag's pipeline when that gateway is on.
- **Secrets never touch a tracked or backed-up file — env-var only, always (§5.18).**
  `.go-parts/config.json` holds only *what* to connect to; *authentication* lives
  in `GOPARTS_*` / `GORAG_TOKEN` / `MUNINNDB_TOKEN` env vars and is never written
  to disk by go-parts. After a restore, secrets must be re-supplied — that's the
  deliberate trade.
- **Single-operator in v1, shaped for multi-user later (§5.8).** No auth in v1
  (no-op middleware seam), but `created_by`/`updated_by` and a single interceptor
  point are there now so makerspace auth is additive, not a rewrite.

## 3. How we work

- **Verify the branch before asserting what code does.** Read the actual change,
  not your memory of it.
- **Build and test the real change before claiming done:**
  `go build ./... && go vet ./... && gofmt -l .` plus the relevant `go test ./... -race`.
- **`-race` is mandatory**, not optional, for anything touching concurrency (§5.14),
  the background job queue (§5.10), or migrations (§5.13) — exactly where races hide.
- **RED-sanity-check bug fixes.** A test for a fixed bug must be shown to fail
  without the fix; a test that passes both ways proves nothing.
- **Keep the full CI gate fast** (§5.20): build → vet/gofmt → `go test -race`.
- **CLI conventions match go-rag / MuninnDB (§5.12):** `start`/`stop`/`status`
  top-level verbs; `<area> <subsystem> <verb>` for feature areas; guided `init`
  wizards with `--non-interactive`; `--dry-run` on anything that mutates;
  `--json` on every `status`; `.go-parts/config.json` (non-secret) like `.go-rag/`.
- **Spec-driven workflow: superpowers** (not SpecKit). Brainstorm (`/brainstorm`)
  → write a plan (`/writing-plans`) → execute via subagent-driven-development +
  TDD (`/tdd`), with `/systematic-debugging` when something breaks. Specs and
  plans live in `docs/superpowers/` (gitignored, local-only — same convention as
  the MuninnDB contribution). Verify before completion; request code review.
- **Branching: `develop` is the integration trunk; `main` stays pristine.** All
  feature and bug-fix work lands on `develop` and merges there; `main` receives
  only release-ready merges (Stephen's directive, 2026-08-07). This **deliberately
  diverges from go-rag's** single-author "commit straight to main" model — go-parts
  uses a develop/main split. Default working branch is `develop`.
- **Conventional commits** (parsed by `cliff.toml` → CHANGELOG).

## 4. Findings that outlive the session

Durable findings — a measured number, a decision and why it beat the alternative,
a trap that looks safe — get written to the **`go-parts` memory vault**, reached via the
project-local **`muninndb-goparts`** MCP server (`.claude/settings.local.json`, gitignored;
key scoped to `go-parts`). Findings flow through a ledger + drain: append a proposal
(`.claude/hooks/memory-propose.mjs`) and it flushes to the vault on PreCompact/SessionEnd/Stop.
The bar — what qualifies, atomicity, evolve-vs-remember, privacy — is in
`.claude/memory-protocol.md`. Nothing important is lost when a session ends.

## 5. The review agents

Two repo-local subagents, invoked before opening a PR — not auto-triggered CI gates;
the developer chooses to run them:

- **`.claude/agents/code-reviewer.md`** (`/code-review`) — reviews a change against the
  go-parts constitution and PRD §5.x invariants, routing by subsystem. Builds/tests the
  real change (`-race` where it matters) and RED-sanity-checks bug fixes.
- **`.claude/agents/adversary.md`** — the mandatory second pass on Tier-3 changes
  (concurrency §5.14, on-disk/migration §5.13, secrets §5.18, auth seams §5.8). Tries to
  break things under an executable-finding standard, or enumerates what it failed to break.

Build-loop orchestration (design → plan → implement) is the superpowers workflow; the two
agents above are the review surface on top of it.

## 6. Attribution

No "Generated with Claude" line on commits or PRs.
