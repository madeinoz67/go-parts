# Plan — Task 1: Component entity + keyspace 0x13 + schema v3

**Source:** `.superpowers/sdd/task-1-brief.md` (verbatim values)
**Branch:** `develop` (confirmed — CLAUDE.md §3 mandates develop/main split)
**Module:** `github.com/madeinoz67/go-parts`
**Mode:** ADDITIVE — new files + additions to existing files; nothing removed.

---

## Context gathered (read-only, via gortex)

### Existing key patterns (`internal/storage/keys/keys.go`)
- Key shape: `kind(1) | ws(8) | payload`
- `LocationKey(ws, id)` = `scopedString(locationPrefix, ws, id)` → `0x11 | ws | id`
- `LocationPrefixBound(ws)` = `(scopedBytes(0x11, ws), scopedBytes(0x12, ws))` — upper bound is `prefix+1`, exclusive
- `ftsPostingKey` precedent for multi-field payload: `kind | ws | term | 0x00 | id` (separator because term is variable-length)
- Helper: `scopedBytes(kind, ws) []byte` makes the `kind|ws` prefix; `scopedString(kind, ws, payload)` appends a string payload
- **Allocated prefixes:** `0x05,0x06,0x07` (FTS), `0x10` (parts), `0x11` (locations), `0x12` (via), `0xF0` (meta)
- **`0x13` is FREE** (keyspace-registry free ranges: `0x13-0xEF`) ✓
- **`0x13 + 1 = 0x14`** — also free, no wrap (`< 0xFF` caveat satisfied) ✓

### Component key shape decision
Brief specifies `0x13 | ws(8) | locID(26) | partID(26)`. Both `locID` and `partID` are ULIDs (26 chars, fixed-length, Crockford-base32). Because both are fixed-length, **no separator byte is needed** — the 26-byte split is unambiguous. This is simpler and more consistent with the location/parts single-payload pattern than borrowing the `0x00` separator from `ftsPostingKey` (which needs a separator only because `term` is variable-length).

A future "all components at location X" scan would use a `0x13 | ws | locID(26)` prefix — but that bound is NOT in this task's scope; only `ComponentPrefixBound(ws)` (all components in a workspace) is requested.

### Existing v2 migration pattern (`internal/storage/migrate/migrate.go`)
```go
r.Register(Migration{
    Version:     2,
    Description: "Locations: Part gains additive DefaultLocationID/...",
    Up:          func(db *pebble.DB) error { return nil },
})
```
`RegisterMigrations` is "the single source of truth, called by every open path". Adding v3 alongside v2 inside that function is the canonical insertion point. `LatestVersion()` auto-derives to 3 once v3 is registered (it takes `max(BaselineVersion, max registered)`). The refuse-newer guard and open-path bootstrap both key off `LatestVersion()` — no other wiring needed.

### Entity struct conventions (`internal/parts/part.go`, `internal/locations/location.go`)
- Package doc comment (one paragraph: what the entity is, PRD §ref, storage notes)
- PascalCase field names, **no json tags** (Go field names are the JSON keys — REST + e2e rely on this)
- `time.Time` for timestamps; `int` for Version
- `newID() string` returns `ulid.Make().String()` (ULID is the canonical ID)
- A field-rename-is-breaking comment is conventional on the struct (Part has it; Location doesn't — not mandatory)

### Disjoint test (`internal/storage/keys/disjoint_test.go`)
The slice currently lists 7 prefixes:
```go
prefixes := []byte{
    partsPrefix, locationPrefix, metaPrefix, viaPrefix,
    ftsPostingPrefix, ftsIndexedPrefix, ftsGlobalStatsPrefix,
}
```
Adding `componentPrefix` makes it 8. The test catches duplicates at registration time — a blocking review finding on collision.

### Keyspace registry (`docs/internals/keyspace-registry.md`)
Markdown table with columns: Keyspace | Purpose | PRD | Byte | Status. Existing allocated rows have `**allocated**` in the Status column. The "Free / reserved" section lists allocated bytes in a single sentence — needs `0x13` appended, and removed from the free-ranges sentence.

---

## Verbatim values from the brief (the source of truth)

### `internal/components/component.go` — structs
```go
package components

import "time"

type Component struct {
    LocationID string
    PartID     string
    Quantity   int
    Tags       []string
    History    []Movement
    CreatedAt  time.Time
    UpdatedAt  time.Time
    Version    int
}

type Movement struct {
    Timestamp time.Time
    Delta     int
    Reason    string
}
```

### `internal/storage/keys/keys.go` — additions
```go
const componentPrefix byte = 0x13

func ComponentKey(ws [8]byte, locID, partID string) []byte { ... }       // 0x13 | ws | locID(26) | partID(26)
func ComponentPrefixBound(ws [8]byte) (lower, upper []byte) { ... }      // 0x13 | ws → 0x14
```

### `internal/storage/migrate/migrate.go` — v3
```go
r.Register(Migration{
    Version:     3,
    Description: "Flat Locations redesign: Location loses ParentID/SinglePartOnly (gains Tags); Part loses DefaultLocationID/Mandatory; new components keyspace 0x13 (no-op; re-arms refuse-newer)",
    Up:          func(db *pebble.DB) error { return nil },
})
```

### `docs/internals/keyspace-registry.md` — row
```
| `components` | `0x13` | Component records (Part-at-Location junction): `0x13 | ws(8) | LocationID(26) | PartID(26)` |
```
**Note:** the existing table has 5 columns (Keyspace | Purpose | PRD | Byte | Status). The brief's row shows 3 columns (Keyspace | Byte | Purpose). I'll adapt the brief's content to the existing 5-column shape so the table stays consistent: Keyspace=`components`, Purpose=`Component records (Part-at-Location junction): 0x13 | ws(8) | LocationID(26) | PartID(26)`, PRD=`§6.1`, Byte=`0x13`, Status=`**allocated**`.

---

## Steps (in execution order)

### Step 0 — Verify clean starting state
```bash
cd /Users/seaton/Documents/src/go-parts
git branch --show-current    # expect: develop
git status --short           # note untracked files (Plans/, screenshots, task-*-report.md) — leave them
```

### Step 1 — Create `internal/components/component.go`
New file (directory `internal/components/` does not exist — confirmed). Content:
- Package doc comment naming it the Component entity layer (Part-at-Location junction, PRD §6.1, Movement = stock-delta history).
- The two structs verbatim from the brief.
- A struct-level comment noting: no json tags (PascalCase JSON ABI, same convention as parts.Part); field renames are breaking (need a registered migration); additive fields are safe (forward-compatible JSON decode).
- No `newID()` — Component's key is the (LocationID, PartID) pair, NOT a ULID. This is a deliberate difference from parts/locations and worth a one-line comment.

### Step 2 — Create `internal/components/component_test.go`
Basic round-trip test:
- Construct a `Component` with a `Movement` entry, populated Tags, timestamps, Version.
- `json.Marshal` → `json.Unmarshal` into a fresh `Component`.
- Assert all fields round-trip (use `reflect.DeepEqual` or field-by-field; `reflect.DeepEqual` is fine for this basic gate).
- One sub-test for an empty Component (zero-value round-trip) to pin the null-case.
- This is NOT a TDD red test (no bug being fixed) — it's a struct-shape pin: catches an accidental field rename or json-tag addition that would break the persisted ABI.

### Step 3 — Modify `internal/storage/keys/keys.go`
Add after the `locationKey`/`LocationPrefixBound` block (keeps keys grouped by prefix order 0x10→0x11→0x12→0x13):
1. Add `componentPrefix byte = 0x13` to the `const` block (after `viaPrefix`).
2. Add `componentKey` (lowercase, unexported) = `scopedString(componentPrefix, ws, locID+partID)` — exploit fixed 26-char ULID length, no separator. Actually cleaner: build manually as `kind | ws | locID | partID` via two appends for readability, since this is the first two-string-payload key. **Decision: build inline** to make the two-field shape visible:
   ```go
   func componentKey(ws [8]byte, locID, partID string) []byte {
       b := scopedBytes(componentPrefix, ws)
       b = append(b, locID...)
       b = append(b, partID...)
       return b
   }
   ```
3. Exported `ComponentKey` alias (same pattern as `LocationKey`/`PartsKey` — lowercase canonical, uppercase seam).
4. `ComponentPrefixBound(ws)` mirroring `LocationPrefixBound`: `lower = scopedBytes(componentPrefix, ws)`, `upper = scopedBytes(componentPrefix+1, ws)`. Comment the same `< 0xFF` caveat + note that `0x13+1 = 0x14` is the next free byte (no adjacent-allocated collision because the bound is exclusive).

### Step 4 — Modify `internal/storage/keys/disjoint_test.go`
Add `componentPrefix` to the `prefixes` slice. Result:
```go
prefixes := []byte{
    partsPrefix, locationPrefix, metaPrefix, viaPrefix, componentPrefix,
    ftsPostingPrefix, ftsIndexedPrefix, ftsGlobalStatsPrefix,
}
```

### Step 5 — Modify `docs/internals/keyspace-registry.md`
1. Add the `components` row to the table (5-column shape, adapted from brief's 3-column form):
   ```
   | `components` | Component records (Part-at-Location junction): `0x13 \| ws(8) \| LocationID(26) \| PartID(26)` | §6.1 | `0x13` | **allocated** |
   ```
   (Pipe chars inside the Purpose cell must be escaped as `\|` in markdown table rows.)
2. Update the "Allocated:" sentence in **Free / reserved** to append `0x13 (components)`:
   ```
   Allocated: `0x05`, `0x06`, `0x07` (FTS), `0x10` (parts), `0x11` (locations), `0x12` (via), `0x13` (components), `0xF0` (meta).
   ```
3. Update the "Free ranges" sentence to remove `0x13` from `0x13-0xEF`:
   ```
   Free ranges: `0x00-0x04`, `0x08-0x0F`, `0x14-0xEF`, `0xF1-0xFF`.
   ```

### Step 6 — Modify `internal/storage/migrate/migrate.go`
Inside `RegisterMigrations`, after the v2 `r.Register(...)` block, add the v3 block verbatim from the brief.

### Step 7 — Gate
```bash
cd /Users/seaton/Documents/src/go-parts
go build ./... && go vet ./... && gofmt -l .
go test ./internal/components/... ./internal/storage/... -race
```
All must pass. `gofmt -l .` must print nothing. `-race` is mandatory for the storage package (touches the key registry + migrations). Expect the disjoint test to pass with 8 prefixes; expect existing migrate tests to still pass (v3 is additive; `LatestVersion()` returns 3, refuse-newer tests that hard-coded "2" may need inspection — see Concerns).

### Step 8 — Commit
```bash
git add internal/components/component.go internal/components/component_test.go \
        internal/storage/keys/keys.go internal/storage/keys/disjoint_test.go \
        docs/internals/keyspace-registry.md internal/storage/migrate/migrate.go
git commit -m "feat(components): Component entity + keyspace 0x13 + schema v3"
```
Conventional-commit format (CLAUDE.md §3). Do NOT add "Generated with Claude" (CLAUDE.md §6).

### Step 9 — Write report
Write `/Users/seaton/Documents/src/go-parts/.superpowers/sdd/task-1-report.md` with: files created/modified (list with absolute paths), the actual gate command output (paste), any concerns. Return status + commit hash.

---

## Verification checklist (definition of done)

- [ ] `internal/components/component.go` exists with `Component` + `Movement` structs matching the brief verbatim.
- [ ] `internal/components/component_test.go` round-trips a populated Component through JSON.
- [ ] `internal/storage/keys/keys.go` exports `ComponentKey(ws, locID, partID)` + `ComponentPrefixBound(ws)`.
- [ ] `0x13` registered in `disjoint_test.go` slice (8 entries, no dup).
- [ ] `docs/internals/keyspace-registry.md` has the `components` row + updated free/allocated lists.
- [ ] `internal/storage/migrate/migrate.go` registers v3 no-op with the brief's Description.
- [ ] `go build ./... && go vet ./... && gofmt -l .` all clean.
- [ ] `go test ./internal/components/... ./internal/storage/... -race` passes.
- [ ] Single conventional commit on `develop`.
- [ ] Report file written; status + hash returned.

---

## Concerns / things to watch

1. **Existing migrate tests may hard-code version 2.** `LatestVersion()` flips from 2 to 3 once v3 is registered. If any test in `internal/storage/migrate/` asserts `LatestVersion() == 2` or constructs a Runner expecting exactly one registered migration, it will break. **Mitigation:** before running the gate, grep `internal/storage/migrate/` for `LatestVersion` and `== 2` / `Version: 2` assertions; update any hard-coded constants to 3 (or to `LatestVersion()` references). This is the single most likely gate-breaker.

2. **Description-string length.** The brief's v3 Description is long (one-line essay). It matches the v2 style (also a long one-liner with rationale), so stylistically consistent. Using it verbatim per "exact values verbatim" instruction.

3. **Task-message vs brief Description discrepancy.** The outer task message shortens the Description to "Flat Locations redesign"; the brief (which the message says is verbatim) has the full text. Going with the brief's full text. Flag in the report.

4. **No separator in the Component key.** Choosing no separator byte (fixed 26-char ULIDs make the split unambiguous). If a future ID scheme uses variable-length IDs, this becomes a latent bug. Mitigation: a comment on `componentKey` noting the fixed-length-ULID invariant. Not blocking — ULIDs are the canonical ID everywhere in go-parts.

5. **No `newID()` in `components`.** Component is keyed by `(LocationID, PartID)`, not a minted ULID. This is correct for a junction table but differs from parts/locations convention — worth a comment so a future reader doesn't go looking for the missing `newID`.

6. **Untracked files in the worktree.** `Plans/`, screenshots, and `task-8-report.md`/`task-9-report.md` are untracked. The `git add` in Step 8 stages ONLY the 6 task-1 files explicitly — these untracked files stay untracked and out of the commit.

7. **Gortex subagent mandate (CLAUDE.md).** This task does NOT dispatch subagents — all work is done directly in this session, where the Gortex `deny` hook enforces graph-grounded reads. No subagent mandate applies. The `code-reviewer` / `adversary` agents (CLAUDE.md §5) are invoked before opening a PR, not for an additive struct+key task on `develop` — but the gate is the same `go build/vet/test -race` they would run.
