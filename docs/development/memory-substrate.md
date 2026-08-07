# Memory substrate

Durable findings from building go-parts go to the **go-parts memory vault** so nothing is lost
when a session ends. The bar — what qualifies, atomicity, evolve-vs-remember, privacy — is in
[`.claude/memory-protocol.md`](../../.claude/memory-protocol.md).

## Two paths, same vault

- **Ledger + drain (automatic, preferred):** append a proposal with
  `node "$CLAUDE_PROJECT_DIR/.claude/hooks/memory-propose.mjs"` (validated against the schema;
  refuses a bad batch). It flushes to the vault on PreCompact / SessionEnd / Stop.
- **Immediate:** the `muninndb-goparts` MCP tools (`mcp__muninndb-goparts__*`) for a finding
  that must land now.

Decisions are `type: decision` memories; the `panel` skill's losing arguments are kept intact
in the memory content.
