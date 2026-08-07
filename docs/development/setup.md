# Development setup

What a contributor (or a fresh checkout) needs beyond `make build`: the Claude Code agents and
memory hooks, and the MuninnDB memory connection that backs them.

## Prerequisites

- **Go 1.26+** — `make build` / `make test`.
- **Node.js** — the memory hooks under `.claude/hooks/` are dependency-free `.mjs` scripts.
- **A reachable MuninnDB daemon** — the go-parts memory vault lives there. Note its MCP
  endpoint (a localhost tunnel like `http://localhost:8125/mcp`, or the remote URL).

## Claude Code: agents, hooks, and the memory drain

The committed `.claude/settings.json` wires the memory hooks automatically:

- `PreCompact` / `SessionEnd` / `Stop` → `memory-drain.mjs` (flushes the ledger to the vault)
- `SessionStart` → `memory-freshness.mjs` (reports only if the drain is unhealthy)
- `PostToolUse` (Write|Edit|Bash) → `ledger-guard.mjs` (catches a malformed proposal in-session)

The `code-reviewer` + `adversary` agents and the `panel` skill live under `.claude/` and are
picked up automatically. Nothing to do here beyond opening Claude Code in the repo root —
**once the local environment below is in place.**

## MuninnDB memory connection (local, gitignored)

The drain and the `muninndb-goparts` MCP server need a **go-parts-vault-scoped** key. This is
a secret: it lives in `.claude/settings.local.json`, which is gitignored and **never
committed**.

1. **Mint a key** scoped to the `go-parts` vault on your MuninnDB instance (full mode), via
   its admin API / CLI. Keep the secret it shows you — it is shown once.
2. **Create `.claude/settings.local.json`** from the template below, replacing
   `<GOPARTS_KEY>` with your key and `<MCP_URL>` with the daemon's MCP endpoint:

   ```json
   {
     "env": {
       "MUNINN_MCP_URL": "<MCP_URL>",
       "MUNINN_MCP_TOKEN": "<GOPARTS_KEY>",
       "MUNINN_PROPOSAL_VAULT": "go-parts"
     },
     "mcpServers": {
       "muninndb-goparts": {
         "type": "http",
         "url": "<MCP_URL>",
         "headers": { "Authorization": "Bearer <GOPARTS_KEY>" }
       }
     }
   }
   ```

   - `env` feeds the drain/hooks (session-wide); `mcpServers.muninndb-goparts` lets the DA read
     and write the vault directly via the `mcp__muninndb-goparts__*` tools.

3. **Verify** the drain reaches the vault (a Claude Code session loads the env automatically;
   for a manual check, `export` the three `MUNINN_*` values first):

   ```bash
   node "$CLAUDE_PROJECT_DIR/.claude/hooks/memory-propose.mjs" <<'JSON'
   {"concept":"setup smoke test","content":"Verify the go-parts memory drain can write to the go-parts vault. Safe to forget.","type":"fact","tags":["setup","self-test"]}
   JSON
   node "$CLAUDE_PROJECT_DIR/.claude/hooks/memory-drain.mjs"     # expect: 1 written
   ```

   `.claude/memory-drain-receipt.json` should read `"outcome": "ok"`.

## Troubleshooting

- **`vault mismatch: this key is scoped to a specific vault`** — the key isn't scoped to
  `go-parts`; mint one that is.
- **drain reports `unreachable`** — `MUNINN_MCP_URL` is wrong, or the daemon is down.
- **`memory-freshness` reports problems at SessionStart** — the last drain didn't consume the
  ledger; run `node .claude/hooks/memory-drain.mjs` by hand and check the receipt.

See also the [memory protocol](../../.claude/memory-protocol.md) for what belongs in the vault.
