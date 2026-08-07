# Data model

How go-parts stores a Part, the optimistic-concurrency contract, and the
Pebble keyspace layout. Cross-references the
[keyspace registry](../internals/keyspace-registry.md) (the source of truth
for prefix bytes).

## The Part record

A single electronics part (PRD §6.1). Defined in `internal/parts/part.go`,
serialized as JSON under the `parts` keyspace. The field set is the v1
retrieval/storage contract.

| Field | Type | Notes |
|---|---|---|
| `ID` | string | ULID, assigned by `Store.Create`; the Pebble key payload |
| `MPN` | string | manufacturer part number |
| `Manufacturer` | string | |
| `Category` | string | plain-string taxonomy facet (not an enum) |
| `Subcategory` | string | |
| `PartType` | string | `linked` or `local` |
| `ViaCode` | string | `P-XXXXXX` — random 6-char code, assigned on create |
| `Description` | string | free text |
| `Specs` | `map[string]string` | spec name → value |
| `Footprint` | string | |
| `UnitOfMeasure` | string | |
| `PackageQty` | int | |
| `QtyOnHand` | int | **stock — `AdjustStock`'s exclusive domain** |
| `ReorderPoint` | int | |
| `Tags` | `[]string` | |
| `CustomFields` | `map[string]string` | |
| `DatasheetStore` | string | empty until the §5.6 datasheet slice |
| `DatasheetRef` | string | local path, or go-rag vault doc-id when that gateway is on |
| `CreatedBy` | string | `"local"` in v1 (no auth); caller identity post-auth |
| `UpdatedBy` | string | mirrors `CreatedBy` |
| `CreatedAt` | `time.Time` | UTC, set by `Create` |
| `UpdatedAt` | `time.Time` | UTC, bumped by `Update` (not by `AdjustStock`) |
| `Version` | int | optimistic-concurrency token, see below |

**JSON field names are Go PascalCase** — the struct has **no `json:` tags**.
This is deliberate for v1 (REST and the e2e test use Go field names), but it
means adding tags later is a **breaking change for any v1 client**. See the
note in `internal/parts/part.go` and the [API reference](api.md).

### FTS field weights

The field-weighted BM25 index (`internal/index`) consumes a derived field map
from each Part (`indexText`). Weights:

- **HIGH (3.0)** — `mpn`, `manufacturer`, `via_code`
- **MID (2.0)** — `description`, `category`, `subcategory`, `footprint`
- **BODY (1.0)** — everything else (`tags`, and `spec:`/`custom:` prefixed
  map entries, which fall through to the body-weight default branch)

## Optimistic concurrency (§5.14)

`Version` is the optimistic-concurrency token. The contract has two halves:

- **`Update` rejects a stale `expectedVersion`, never silently overwrites.**
  The caller passes the version they loaded (typically from a `GET`'s `ETag`);
  `Store.Update` re-reads under the per-id striped lock and errors if the
  stored version no longer matches. On success the stored version is bumped
  by one and the same bump is reflected on the caller's `*Part`. REST surfaces
  this as `409 Conflict` (`PATCH /parts/{id}`).
- **`AdjustStock` does NOT bump `Version`.** Stock is a commutative delta
  (±N) serialized under the per-id striped lock; two concurrent `-10` calls
  always net `-20`. Because stock isn't an FTS-indexed field, `AdjustStock`
  touches only the `parts` keyspace — never the FTS.

A full-record `Update` **preserves the in-lock `QtyOnHand`** and ignores the
caller's value (the F3 invariant — a `Get→modify→Update` caller carrying a
stale `QtyOnHand` must not overwrite a concurrent `AdjustStock` delta, which
didn't bump `Version` and so wouldn't fail the version check). Stock changes
go through `POST /parts/{id}/stock`.

## `schema_version` marker (§5.13)

The migration marker lives under the `meta` keyspace. Single source of truth:
`internal/storage/migrate/migrate.go` + `internal/storage/keys/keys.go`.

- **Key** — `metaPrefix (0xF0)` `| ws(8 zero)` `| "schemaver"`.
- **Value** — `uint64`, 8 bytes, big-endian.
- **Valid range** — `[0, math.MaxInt32]`. Values outside that range are
  rejected at read time as corrupt (a high-bit value would decode to a
  negative signed `int` and bypass both refuse-newer guards — see
  `readMigrationVersion`); the guard is platform-independent (bound against
  `MaxInt32`, not `MaxInt`, so a 32-bit build is not re-exposed).
- **`BaselineVersion` = 1** — a fresh store is bootstrapped to 1 on first
  open; v1 ships zero step-migrations (the schema is v1 from first run).

The open-path gate (`storage.Open` → `bootstrapOrMigrate`) has four branches:

| Stored `cur` | Action |
|---|---|
| `0` (no key) | bootstrap: write `LatestVersion()` directly |
| `== latest` | no-op |
| `< latest` | snapshot (best-effort), then run registered step-migrations |
| `> latest` | **hard startup failure** — never a silent downgrade-write |

## Pebble keyspace layout

Shape: `kind(1) | ws(8) | payload`. `ws` is the workspace prefix, **fixed to
zero in v1** (single-vault; reserved for Phase-8 multi-vault). One embedded
Pebble store holds every keyspace; prefix collisions silently corrupt scans,
so every prefix is [registered](../internals/keyspace-registry.md).

| Keyspace | Byte | Purpose |
|---|---|---|
| `parts` | `0x10` | Part records, keyed by ULID |
| `meta` | `0xF0` | `schema_version` marker + future migration cursors, sub-keyed by payload (`"schemaver"`) |
| `search_index` FTS postings | `0x05` | term → id (verbatim shape from go-rag) |
| `search_index` FTS indexed-set | `0x07` | ids already indexed |
| `search_index` FTS global stats | `0x06` | doc-frequency stats |

The three FTS prefixes are go-parts' own allocation but the payload shapes are
byte-identical to go-rag's `internal/storage/keys`, keeping the BM25 port
portable. `search_index` is fully derived from `parts` — a reindex loses no
data (PRD §5.15).
