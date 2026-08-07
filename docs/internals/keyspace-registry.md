# Pebble keyspace registry

**The single most important artifact for preventing a whole class of bug.** go-parts runs its
entire store — parts, locations, projects, search index, jobs, stock history, meta — over
**one embedded Pebble database**. Keys are prefix-partitioned. If two subsystems claim the same
prefix, they silently corrupt each other's scans — the bug class this registry exists to make
unrepresentable.

> **Pre-v1 charter.** go-parts has no Pebble code yet — Phase 1 builds `internal/storage`. This
> document is the source of truth for the **planned** keyspaces (from PRD §5.1 / §5.10);
> byte-prefix assignment happens in Phase 1's storage design, and **every assignment must be
> registered here as it lands**. Once `internal/storage` exists, the Go prefix constants and
> this doc are the joint source of truth, and a disjointness test derived from the same table
> enforces it in CI (the single-table, no-bound-to-bump pattern).

**Rule for any change that adds or changes a Pebble key:** the new prefix must be **disjoint**
from every registered keyspace and **added to this registry**. The `code-reviewer` agent treats
an unregistered or colliding prefix as a **blocking** finding.

## Planned keyspaces (PRD §5.1 / §5.10)

| Keyspace | Purpose | PRD | Status |
|---|---|---|---|
| `parts` | Part records (mpn, specs, stock, `via_code`, `version`, …) | §6.1 | planned — prefix TBD |
| `categories` | category / subcategory (plain-string taxonomy facet, not an enum) | §5.1, §6.2 | planned |
| `locations` | Location records (nested storage, `via_code`) | §6.1, §5.17 | planned |
| `suppliers` | Supplier records + part-number mapping conventions | §6.1 | planned |
| `projects/boms` | Project + BOM (`part_refs[]`) | §6.1 | planned |
| `datasheets` | datasheet ref (local path, or go-rag vault doc-id when that gateway is on) | §5.1, §5.6 | planned |
| `search_index` | BM25 postings (+ optional vector index) | §5.1 | planned — fully derived/rebuildable (§5.15) |
| `stock_history` | stock-adjust history log (commutative deltas, §5.14) | §5.1, §5.14 | planned |
| `meta` | `schema_version` marker + migration cursors | §5.1, §5.13 | planned |
| `jobs` | background job queue (`embed_part`, `enrich_part_*`) | §5.10 | planned |

**Prefix assignment is deferred to Phase 1.** When `internal/storage` allocates the first
prefix, it lands here as a row with its byte, key shape, value, and notes — and the
disjointness test is added in the same change.

## Scope notes

- **v1 is single-vault** — one Pebble store, one operator (PRD §4, §5.8). No vault-name prefix
  is needed yet. If multi-user / a shared makerspace vault lands (§5.8, future Phase 8), the
  prefix layout is revisited here **before** any code.
- **`search_index` is derived** — fully rebuildable from `parts` (§5.15); a reindex loses no
  data. That distinguishes it from the record keyspaces: index corruption is recoverable, but a
  prefix collision between `search_index` and a record keyspace is still blocking.
- **`meta` holds `schema_version`** — the migration marker (§5.13): a missing key on an empty
  store = fresh install; a lower version = migrate; a higher version = hard startup failure,
  never a silent downgrade-write.

## Free / reserved

No bytes allocated yet. Phase 1's first allocation establishes the table; this section then
tracks free ranges alongside it (so a new prefix picks a free byte, not one in use).

## Reviewer obligation

Adding a Pebble prefix without registering it here — or colliding with an existing one — is a
**blocking** review finding. See `.claude/agents/code-reviewer.md` (Store bucket) and
`docs/development/doc-obligations.md`.
