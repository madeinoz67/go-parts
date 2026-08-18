# Documentation obligations

If a PR touches… | it must also update…
---|---
`cmd/go-parts/` (CLI surface) | `docs/guide/` — the matching page (e.g. `cli.md`)
a Pebble record shape / a `schema_version` bump | `docs/reference/data-model.md`
a new Pebble keyspace / prefix byte | `docs/internals/keyspace-registry.md` (source of truth)
a `GOPARTS_*` env var or `.go-parts/config.json` key | `docs/reference/config.md`
a new REST route or MCP tool | `docs/reference/api.md`
a Tier-3 / design fork | a decision in the go-parts memory vault (see [memory-substrate.md](memory-substrate.md))
a new invariant or principle | `CLAUDE.md` or the PRD (`docs/internals/go-parts-prd.md`)

The `code-reviewer` agent reads this table and treats a missed obligation as a **blocking**
finding. Pages that don't exist yet (e.g. `data-model.md`) are created by the change that
first needs them.

**The sweep is also mechanical.** Three drift tests run as ordinary `go test`s and turn the
table's top rows into red tests instead of reviewer memory:

- `cmd/go-parts/docs_cli_test.go` — the real cobra tree vs `docs/guide/cli.md` (commands
  and flag tables, both directions)
- `internal/rest/routes_doc_test.go` — the registered routes vs `docs/reference/api.md`'s
  Routes table
- `internal/storage/keys/registry_doc_test.go` — the prefix consts vs the registry's
  Allocated line

A red drift test is the same blocking finding as a missed row above — fix the doc (or the
code) in the same change; never skip the test. Prose truthfulness is still the reviewer's:
the tests read tables, not meaning.
