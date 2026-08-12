# Locations Redesign: Flat Bins + Tags + Components with Qty/History

## Context

The principal directed a fundamental pivot from the nested-storage model (ParentID tree, treeMu, cycle-guard, SinglePartOnly) to a **flat bin-level inventory** with **tags as the organizational layer** + **per-location component quantities with movement history**. The nesting model is rejected; tags replace it (a bin is tagged "garage" / "workbench" rather than nested under a parent). This is how real stockrooms work.

## The new data model

```
Location   = { Label, ViaCode, Type(1D/2D/3D), Tags[], Notes, CreatedBy, CreatedAt, UpdatedAt, Version }
Component  = { LocationID, PartID, Quantity, Tags[], History[{Timestamp, Delta, Reason}] }
Part       = { MPN, ... (unchanged, minus DefaultLocationID/DefaultLocationMandatory) }
             QtyOnHand stays as the global total; Component.Quantity is the per-location allocation (sum ≤ QtyOnHand; unallocated stock is valid)
```

- **No nesting.** ParentID, treeMu, cycle-guard, Children, wouldCycle → all removed.
- **Tags replace nesting.** A Location has Tags[] = physical context ("garage", "workbench"). A Component has Tags[] = component-level metadata. Tags are searchable (the live search filters by label, via-code, OR tag).
- **The 1D/2D/3D Type** drives label naming (GenerateLabels from slice 2, repurposed as a stored field). Not a nesting dimension.
- **Component = a Part at a Location with a quantity + history.** Adding a part to a location creates a Component. Adjusting stock (in/out) updates Component.Quantity + appends a History entry. A Part can be at multiple locations (different quantities).
- **Part.QtyOnHand stays as the global authoritative total** (for the Parts table + low-stock badge). Component.Quantity is a per-location allocation (sum of components ≤ QtyOnHand; unallocated stock is valid). AdjustStock remains global (Part-level); a per-location movement API is added (updates the Component.Quantity + History).

## Keyspace allocation

| Keyspace | Byte | Purpose |
|---|---|---|
| `parts` | `0x10` | Part records (unchanged) |
| `locations` | `0x11` | Location records (restructured: flat + Tags, no ParentID) |
| `via` | `0x12` | Via-code index (unchanged) |
| `components` | `0x13` | Component records: `0x13 | ws(8) | LocationID(26) | PartID(26)` — prefix scan by LocationID gives all components at a location |
| `meta` | `0xF0` | Schema marker (bump to v3) |
| `search_index` | `0x05/06/07` | FTS for parts (unchanged) |

Schema v3 migration: bump the marker (v2→v3). Old Location JSON (with ParentID/SinglePartOnly) decodes to the new struct (unknown fields dropped). The old nesting info is lost (the principal is OK with this — test data is disposable). The new `components` keyspace (0x13) is additive (empty on migration).

## What to remove

- **Location**: ParentID, SinglePartOnly, CreationMethod (→ rename to Type), the nesting fields.
- **locations.Store**: treeMu, locks (already removed in RedTeam), wouldCycle, Children, CreateBulk's BulkOpts.ParentID/SinglePartOnly, the tree CLI command.
- **parts.Store**: locLocks, lockForLoc, LocationPolicy/SetLocationPolicy, the single_part_only guard in Create/Update, ErrLocationNotFound, ErrLocationSinglePartConflict, ListByLocation, CountByLocation.
- **internal/link**: NewPolicy (the guard composition — GONE). Resolve stays (the via resolver).
- **rest.Server**: the policy-related error mapping in handlePatch/handleCreate. The RFC 7396 applyPatch still works (minus DefaultLocationID/DefaultLocationMandatory fields).
- **ui.Server**: the location picker on parts detail (DefaultLocationID select + Mandatory checkbox), the bulk-Move action, the guard-error banner on parts edit. The conflict.html reload stays (version conflicts still happen).
- **detail.html**: remove the Location select + Mandatory checkbox.
- **bulk-bar.html**: remove the Move form.
- **CLI**: remove `locations tree`. Rethink `locations bulk` (no parent nesting; just label generation).

## What to keep (unchanged)

- Via index (internal/via) + the resolver (link.Resolve) + labels (internal/label) + the scan-to-find loop (slice 4+6).
- GenerateLabels (1D/2D/3D naming) — repurposed as the Location.Type convention.
- Part entity (minus DefaultLocationID/DefaultLocationMandatory + QtyOnHand stays).
- FTS + the Parts shell (search, table, detail minus the location fields).
- Pebble storage + migration framework.
- Reindex.
- REST parts CRUD (minus DefaultLocationID in applyPatch).
- The daemon/REST/UI skeleton.

## What to add

- **Location.Tags[]** — replaces ParentID as the organizational layer.
- **Component entity** + **Component store** (internal/components or methods on locations.Store) — add/remove/list components at a location, adjust quantity (records history).
- **Component history** — embedded in the Component JSON ([]HistoryEntry). Small at homelab scale.
- **Location live search** — a GET /ui/locations/search that filters by label/via/tag (substring match over the in-memory list; not FTS). The Storage tab uses a search input (like the Parts shell) instead of a table.
- **Location detail** — shows the component list (Part MPN, qty, tags, history) + component management (add a part, adjust qty, remove).
- **REST locations** — add component management endpoints (POST /locations/{id}/components, PATCH /locations/{id}/components/{partId}, DELETE /locations/{id}/components/{partId}).
- **CLI** — add component management (add/list/remove components at a location, adjust qty).

## Implementation steps

### Step 1: Data model + migration (the foundation)
- Restructure Location: remove ParentID/SinglePartOnly; add Tags[]; rename CreationMethod→Type.
- Define Component + HistoryEntry structs.
- Allocate keyspace 0x13 (components). Register in keyspace-registry.md + disjoint test.
- Schema v3 migration (bump marker; no-op transform).
- Part: remove DefaultLocationID/DefaultLocationMandatory from the struct + applyPatch.

### Step 2: Store layer
- Flatten locations.Store: remove treeMu/wouldCycle/Children/nesting guards. Location CRUD becomes simple (no tree serialization).
- New: a component store (or component methods on locations.Store): AddComponent(locID, partID, qty, tags), ListComponents(locID), AdjustComponentQty(locID, partID, delta, reason), RemoveComponent(locID, partID).
- Remove parts.Store: locLocks/lockForLoc/LocationPolicy/SetLocationPolicy/ListByLocation/CountByLocation.
- Remove internal/link.NewPolicy (keep link.Resolve).
- Update daemon wiring (remove the policy injection).

### Step 3: REST + CLI
- REST: add component endpoints on locations. Remove the policy error mapping. Remove DefaultLocationID from parts applyPatch.
- CLI: remove `locations tree`. Update `locations add/bulk` (no parent). Add `locations components` commands (add/list/remove/adjust).

### Step 4: UI
- Storage tab: replace the table with a live search (search input + dynamic filter by label/via/tag, like the Parts shell). Location detail shows the component list + management.
- Parts detail: remove the location picker + Mandatory checkbox.
- Bulk bar: remove the Move form.

### Step 5: Tests + docs
- Update all locations tests (remove nesting/guard tests; add component tests).
- Update api.md, cli.md, data-model.md.
- The RedTeam findings that referenced the removed code (guard, locLocks, treeMu) are moot.

## Verification
- `go build ./... && go vet ./... && gofmt -l . && go test ./... -race`
- Start the daemon + browser-verify the Storage live search + component management.
- Scan a via-code → resolver still works (link.Resolve unchanged).
- The Parts shell still works (minus the location fields).
