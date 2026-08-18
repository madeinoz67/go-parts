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

**Boundary, stated plainly (adversary pass, 2026-08-18):** the drift tests diff two
artifacts, so a change that edits code and doc **together** passes green — a feature
deleted from both sides is caught only by review and behavior tests, not by these.
cli.md's fenced usage blocks are unchecked (a lockstep flag rename leaves the
copy-pasteable lines stale — keep fences in sync by hand). Flag shorthands and
Default-column values are name-only. And a **green** claim must come from an unfiltered
`go test ./... -race` (or name the three test names in the output) — a `-run` filter
skips them with green-looking output, the same trap as a no-match RED run in reverse.
