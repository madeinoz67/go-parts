# Pebble keyspace registry

**The single most important artifact for preventing a whole class of bug.** go-parts runs its
entire store — parts, locations, projects, search index, jobs, stock history, meta — over
**one embedded Pebble database**. Keys are prefix-partitioned. If two subsystems claim the same
prefix, they silently corrupt each other's scans — the bug class this registry exists to make
unrepresentable.

> **Pre-v1 charter — first allocations landed.** Phase 1's `internal/storage` has now allocated
> the first prefixes (Task 2 / commit 0cfbed0); the Go prefix constants in
> `internal/storage/keys/keys.go` and this doc are the **joint source of truth**, enforced by the
> single-table disjointness test in `internal/storage/keys/disjoint_test.go` (the no-bound-to-bump
> pattern — add every public prefix to the slice; a duplicate is a blocking review finding).
> Remaining keyspaces are still planned (PRD §5.1 / §5.10); **every new assignment must be
> registered here as it lands**.

**Rule for any change that adds or changes a Pebble key:** the new prefix must be **disjoint**
from every registered keyspace and **added to this registry**. The `code-reviewer` agent treats
an unregistered or colliding prefix as a **blocking** finding.

## Keyspaces (PRD §5.1 / §5.10)

| Keyspace | Purpose | PRD | Byte | Status |
|---|---|---|---|---|
| `parts` | Part records (mpn, specs, stock, `via_code`, `version`, …) | §6.1 | `0x10` | **allocated** |
| `via` | Via-code index (`via_code` → `{type,id}`); uniqueness + resolver | §5.17 | `0x12` | **allocated** |
| `categories` | category / subcategory (plain-string taxonomy facet, not an enum) | §5.1, §6.2 | — | planned |
| `locations` | Location records (nested storage, `via_code`) | §6.1, §5.17 | `0x11` | **allocated** |
| `components` | Component records (Part-at-Location junction): `0x13 \| ws(8) \| LocationID(26) \| PartID(26)` | §6.1 | `0x13` | **allocated** |
| `suppliers` | Supplier records + part-number mapping conventions | §6.1 | — | planned |
| `projects/boms` | Project + BOM (`part_refs[]`) | §6.1 | — | planned |
| `datasheets` | datasheet ref (local path, or go-rag vault doc-id when that gateway is on) | §5.1, §5.6 | — | planned |
| `search_index` FTS postings | BM25 postings (term→id); optional vector index is additive/future | §5.1 | `0x05` | **allocated** (verbatim, go-rag) |
| `search_index` FTS indexed-set | ids already indexed | §5.1 | `0x07` | **allocated** (verbatim, go-rag) |
| `search_index` FTS global stats | doc-frequency stats | §5.1 | `0x06` | **allocated** (verbatim, go-rag) |
| `stock_history` | stock-adjust history log (commutative deltas, §5.14) | §5.1, §5.14 | — | planned |
| `meta` (schema_version) | `schema_version` marker + migration cursors | §5.1, §5.13 | `0xF0` | **allocated** |
| `jobs` | background job queue (`embed_part`, `enrich_part_*`) | §5.10 | — | planned |

`search_index` is fully derived/rebuildable from `parts` (§5.15) — a reindex loses no data; that
makes index corruption recoverable, but a prefix collision between `search_index` and a record
keyspace is still blocking. The three `search_index` prefixes are imported verbatim from go-rag's
`internal/storage/keys` to keep the FTS implementation portable (PRD §5.1).

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

Allocated: `0x05`, `0x06`, `0x07` (FTS), `0x10` (parts), `0x11` (locations), `0x12` (via), `0x13` (components), `0xF0` (meta).

Free ranges: `0x00-0x04`, `0x08-0x0F`, `0x14-0xEF`, `0xF1-0xFF`. A new prefix picks a free
byte, not one in use. (`0xF1-0xFF` is free — the `meta` keyspace is `0xF0` only, sub-keyed
by payload after the single prefix, so the top nybble above `0xF0` is not reserved.)

## Reviewer obligation

Adding a Pebble prefix without registering it here — or colliding with an existing one — is a
**blocking** review finding. See `.claude/agents/code-reviewer.md` (Store bucket) and
`docs/development/doc-obligations.md`.
