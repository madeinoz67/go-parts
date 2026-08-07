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

The drain and the `muninndb-goparts` MCP server need a **go-parts-vault-scoped** key. The key
is a secret — it lives (as the `MUNINN_MCP_TOKEN` env var) in `.claude/settings.local.json`,
which is gitignored and **never committed**. The MCP *server* itself is registered at local
scope, not in a settings file — Claude Code does not load a `mcpServers` key from
`settings*.json`; it must be added via the CLI.

1. **Mint a key** scoped to the `go-parts` vault on your MuninnDB instance (full mode), via
   its admin API / CLI. Keep the secret it shows you — it is shown once.
2. **Create `.claude/settings.local.json`** (env only) from the template below, replacing
   `<GOPARTS_KEY>` with your key and `<MCP_URL>` with the daemon's MCP endpoint:

   ```json
   {
     "env": {
       "MUNINN_MCP_URL": "<MCP_URL>",
       "MUNINN_MCP_TOKEN": "<GOPARTS_KEY>",
       "MUNINN_PROPOSAL_VAULT": "go-parts"
     }
   }
   ```

   - `env` feeds the drain/hooks (session-wide). A `mcpServers` key here is silently ignored
     by Claude Code — don't put the server config in this file.
3. **Register the MCP server** at local scope (writes to `~/.claude.json`, per-project, not
   committed), run from the repo root — note the URL comes *before* `--header`, which is
   variadic and would otherwise swallow it:

   ```bash
   claude mcp add --transport http muninndb-goparts <MCP_URL> \
     --scope local \
     --header "Authorization: Bearer <GOPARTS_KEY>"
   ```

   This exposes the `mcp__muninndb-goparts__*` tools scoped to the `go-parts` vault.

4. **Verify** the drain reaches the vault (a Claude Code session loads the env automatically;
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
