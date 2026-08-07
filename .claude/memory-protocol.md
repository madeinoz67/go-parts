# Memory protocol — how a finding survives the session

go-parts dogfoods **MuninnDB** for its own development memory (PRD §5.20 dogfooding note):
durable findings from building go-parts go to the dedicated **`go-parts` vault**, the way
MuninnDB's own maintainer persists findings for MuninnDB. This document is the bar for what
belongs there.

> **Vault: `go-parts`.** All go-parts development memory targets the `go-parts` vault
> (`vault: "go-parts"` on every muninn call). A session's default vault is often something
> else — name it explicitly or the finding lands in the wrong place.

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

Use the muninn MCP tools directly against the `go-parts` vault:

- **Recall first.** Before adding a fact, `muninn_recall` what's related. If the new
  knowledge *corrects, sharpens, or supersedes* an existing memory, `muninn_evolve` that one
  — don't add a rival copy. Evolve supersedes and retires the old version; a second
  `muninn_remember` leaves a stale duplicate competing in recall.
- **`muninn_remember`** for genuinely new facts. One concept each. Include `entities` (Part,
  Location, Pebble, go-rag, …) and `tags` (`go-parts`, the subsystem) so they recall cleanly.
- **Atomic and self-contained.** A memory that only makes sense next to the conversation
  that produced it is not a memory — it's a comment. Write it readable in a year.

## Privacy — this repo is public

go-parts is a **public** MIT repo. Memories are an internal artifact but the same discipline
as §5.18 applies: **no secrets, no real vendor API keys, no personal customer data** in any
memory. Measurements are welcome ("sub-100ms over 30k parts"); a real credential is not.

## What is deliberately not ported from MuninnDB

MuninnDB's own repo has a file-ledger + hook-drain machinery (`memory-propose.mjs`,
`memory-drain.mjs`, etc.) that auto-persists proposals on `PreCompact`/`SessionEnd`. That is
**MuninnDB's internal dogfooding tooling, written against its own daemon** — not ported here.
go-parts uses **direct MCP writes** against the `go-parts` vault for now. If the volume ever
justifies it, a ledger/drain can be added later; until then, direct writes with the bar
above is the protocol.
