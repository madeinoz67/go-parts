# Memory protocol — how a finding survives the session

Durable findings from building go-parts go to the **`go-parts` memory vault** — so nothing
important is lost when a session ends. This document is the bar for what belongs there.

> **Vault: `go-parts`, reached via the `muninndb-goparts` MCP server.** go-parts dev memory
> goes through the project-local `muninndb-goparts` connection (registered at local scope via
> `claude mcp add -s local` → `~/.claude.json`, per-project, not committed) — its key is scoped
> to `go-parts`, so its tools (`mcp__muninndb-goparts__*`)
> default to that vault with **no `vault` arg**. Do **not** use the global `muninndb` server
> for go-parts memory — that is the default/LifeOS vault, and its key is scoped there.

## The bar — what qualifies

A proposal must be **durable, non-obvious, and not recoverable elsewhere.**

Propose:

- A **decision and the reason it beat the alternative** — especially PRD-internal choices
  that will be re-litigated (e.g. "secrets diverge from go-rag's config.json pattern because
  plaintext-at-rest is an avoidable risk" — §5.18).
- A **measured result and the number** ("BM25 over a 30k-part catalog returns sub-100ms" —
  §9, when measured).
- An **honest negative** — a thing that did not work, with the evidence that killed it.
  These are the most valuable memories: they stop an idea being re-proposed.
- A **trap** — a thing that looks safe and is not ("Pebble's directory lock means the TUI
  can't open the store directly — it's always a REST client" — §5.11).
- A **defect pattern**, not a single defect. "Three concurrency reviews found the same
  stale-`version` writeback bug" is durable; the three instances are in their PRs.
- **Cross-project context** — how go-parts relates to go-rag/MuninnDB (the gateway topology,
  the shared conventions, the deliberate divergences).

Do **not** propose:

- Progress narration ("ran the tests, they passed").
- A restatement of a diff, commit, PR body, or PRD section. Git and the PRD have those.
- Anything you'd have to look up again anyway to trust it.
- Five variations of one idea. **One concept per memory, atomic.** If it needs "and", it is
  probably two memories.

The bar exists because **a noisy vault is worse than a small one.**

## How to write

Two paths, same vault (`go-parts`):

- **Preferred — the ledger + drain (automatic).** Append a proposal with
  `node "$CLAUDE_PROJECT_DIR/.claude/hooks/memory-propose.mjs"` (validates against the schema;
  refuses a bad batch rather than queueing it). It lands in `.claude/memory-proposals.jsonl`
  and `memory-drain.mjs` flushes it to the go-parts vault on `PreCompact` / `SessionEnd` / `Stop`
  (idempotent, concurrency-safe). This is the path that does not depend on remembering to
  remember — the whole reason the machinery exists.
- **Immediate — the `muninndb-goparts` MCP tools** (`mcp__muninndb-goparts__*`), for a finding
  that must land right now. No `vault` arg; the key is go-parts-scoped.

Either way:

- **Recall first.** Before adding a fact, recall what's related. If the new knowledge
  *corrects, sharpens, or supersedes* an existing memory, `evolve` that one — don't add a
  rival copy. Evolve supersedes and retires the old version; a second `remember` leaves a
  stale duplicate competing in recall.
- **One concept per memory, atomic.** Include `entities` (Part, Location, Pebble, go-rag, …)
  and `tags` (`go-parts`, the subsystem) so they recall cleanly.
- **Self-contained.** A memory that only makes sense next to the conversation that produced
  it is not a memory — it's a comment. Write it readable in a year.

## Privacy — this repo is public

go-parts is a **public** MIT repo. Memories are an internal artifact but the same discipline
as §5.18 applies: **no secrets, no real vendor API keys, no personal customer data** in any
memory. Measurements are welcome ("sub-100ms over 30k parts"); a real credential is not.

## How it's wired

The ledger + drain loop lives in `.claude/hooks/`: `memory-schema.mjs` (the one proposal
shape), `memory-propose.mjs` (validates + appends), `memory-ledger.mjs` (paths + lock +
safe-touch), `memory-drain.mjs` (flushes ledger → vault on PreCompact/SessionEnd/Stop),
`memory-freshness.mjs` (SessionStart health), and `ledger-guard.mjs` (catches a bad append
in-session). Hook wiring is in `.claude/settings.json`; the connection env (`MUNINN_MCP_URL`,
`MUNINN_MCP_TOKEN`, `MUNINN_PROPOSAL_VAULT`) is in `.claude/settings.local.json` (gitignored).
For an immediate write, use the `muninndb-goparts` MCP tools directly.

Intentionally absent: a one-time ledger-repair step (go-parts is greenfield — no pre-schema
legacy) and a cross-surface code-drift guard (that's source-code drift, not memory).
