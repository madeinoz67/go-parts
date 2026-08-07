# Documentation obligations

If a PR touches… | it must also update…
---|---
`cmd/go-parts/` (CLI surface) | `docs/guide/` — the matching page (e.g. `cli.md`)
a Pebble record shape / a `schema_version` bump | `docs/reference/data-model.md`
a `GOPARTS_*` env var or `.go-parts/config.json` key | `docs/reference/config.md`
a new REST route or MCP tool | `docs/reference/api.md`
a Tier-3 / design fork | a decision in the go-parts memory vault (see [memory-substrate.md](memory-substrate.md))
a new invariant or principle | `CLAUDE.md` or the PRD (`docs/internals/go-parts-prd.md`)

The `code-reviewer` agent reads this table and treats a missed obligation as a **blocking**
finding. Pages that don't exist yet (e.g. `data-model.md`) are created by the change that
first needs them.
