# CLI reference

The `go-parts` command drives the daemon lifecycle and is the primary
single-operator interface. It mirrors the go-rag / MuninnDB CLI conventions
(PRD §5.12): `start` / `stop` / `status` top-level verbs, a persistent
`--data-dir`, and JSON on every `status`.

```
go-parts [--data-dir DIR] [--version] <command> [flags]
```

## Global flags

| Flag | Default | Applies to |
|---|---|---|
| `--data-dir` | empty → `~/.go-parts` | every subcommand (persistent) |
| `--version` / `-v` | — | root; prints the binary version and exits |

`--data-dir` is persistent: it applies to `start`, `stop`, `status`, and
every store-touching subcommand. An empty value resolves to `~/.go-parts`
(mirrors go-rag / MuninnDB's home-dir default).

## Subcommands

### `go-parts start`

Starts the foreground HTTP server. The `start` process **is the server** — it
does not return until `SIGINT` or `SIGTERM` arrives. Background it with `&` or
a service manager; v1 does not re-exec a detached child (single-operator,
one transport).

```
go-parts start [--bind ADDR] [--data-dir DIR]
```

| Flag | Default | Notes |
|---|---|---|
| `--bind` | `127.0.0.1:7890` | overrides the config's `bind`; loopback by default (§5.18) |

On start the store opens (running the §5.13 schema-version gate:
bootstrap / migrate / refuse-newer), the field-weighted BM25 FTS and
`parts.Store` are wired to the REST server, the listener binds, and the state
file (`<data_dir>/daemon.json`) is published. **The listener binds before
the state file is written** — a port-in-use error surfaces before the daemon
claims to be running.

A second `start` against the same data dir fails at the Pebble flock (clear
error, no port thrash).

### `go-parts stop`

Reads `<data_dir>/daemon.json` and sends `SIGTERM` to the recorded PID. Gives
the server the same clean-shutdown path as `Ctrl-C` (the `start` signal
handler closes the listener and removes the state file).

```
go-parts stop [--data-dir DIR]
```

A not-running data dir is an error. A process that exits between the status
check and the signal (`ESRCH`) is treated as success (clean race).

### `go-parts status`

Reads `<data_dir>/daemon.json`, probes the recorded PID's liveness via
`kill(pid, 0)`, and prints the `State` JSON to stdout (one line):

```
go-parts status [--data-dir DIR] [--json]
```

| Flag | Default | Notes |
|---|---|---|
| `--json` | `true` | reserved for the §5.12 "human format by default, `--json` for machine" split; today both modes emit JSON |

Output shape:

```json
{"bind":"127.0.0.1:7890","pid":12345,"running":true}
```

A missing state file yields `running:false` with no error (the canonical
"not running" state). A state file pointing at a dead PID also yields
`running:false` (stale fixture left by an unclean exit) — the file's presence
is never trusted over the liveness probe.

### `go-parts tui`

Terminal UI (§5.11) — a REST client of the running daemon, never a second
store opener: a split view with a live filter box, the parts table, and a
detail pane (stock per bin + recent movements) with an adjust overlay.
Requires `go-parts start` to be running; if the daemon cannot be reached it
prints the reason and exits 1 rather than opening a blank screen.

```
go-parts tui                  # connects to the configured default bind (127.0.0.1:7890)
go-parts tui --host homelab.lan:7890
```

| Flag | Default | Notes |
|---|---|---|
| `--host` | the configured `bind` | go-parts server address `host:port`; `http://` is prepended |

Keys: type to filter (live) · `↑`/`↓` select · `a` adjust stock · `l`
low-stock toggle · `r` refresh · `q` quit · `esc` closes an open overlay,
or quits when none is open.

Because the filter box is always focused, the single-rune commands `a`,
`l`, and `r` are intercepted **before** it — those three letters can
never be *typed* into a filter query (a query like `relay` or `lm358`
cannot be typed letter by letter; pasting it into the box works). `esc`
closes the adjust overlay when one is open and quits at the root
otherwise.

## Locations

`go-parts locations` manages physical storage locations (bins, drawers,
shelves, boxes). The model is **flat** (§6.1): every location is a
self-contained record tagged with its physical context (`garage`,
`workbench`, …) — no hierarchy, no parent, no nesting. A location's contents
are its **Components** (the stock junction: which parts, what quantity, with
movement history). Locations are addressed by a Via code (`L-XXXXXX`, §5.17)
and are navigated/scanned, **not** full-text-indexed (no FTS, unlike parts).
Defined in `internal/locations`; records live under the `locations` keyspace
(`0x11`, see [data model](../reference/data-model.md)).

The `locations` subcommands open the store directly (they do not talk to the
daemon). They hold the Pebble flock, so they **fail if the daemon is running**
— stop the daemon first for bulk CLI ops, or use the web UI (v1
single-operator posture).

```
go-parts locations [--data-dir DIR] <subcommand> [flags]
```

### `go-parts locations add`

Creates a single location record. Via code is auto-assigned (`L-XXXXXX`).

```
go-parts locations add --label "Bin A3" [--tag garage --tag workbench] [--notes ...] [--dry-run]
```

| Flag | Default | Notes |
|---|---|---|
| `--label` | (required) | location label, e.g. `"Bin A3"` or a bulk-generated `box-1` |
| `--tag` | (none) | physical-context tag (repeatable: `--tag garage --tag workbench`) |
| `--notes` | `""` | free-text notes |
| `--dry-run` | `false` | print what would happen without executing |

### `go-parts locations bulk`

Creates many locations at once by enumerating labels along a numeric row, a
2-D grid (alpha rows × numeric columns), or a 3-D grid (numeric levels × alpha
rows × numeric columns). The creation methods are expressed as flag
combinations on the one `bulk` subcommand (§7.1); each generated label is
passed to `Store.CreateBulk`, which loops `Create` with the shared opts. For a
single location use `go-parts locations add`.

```
go-parts locations bulk --method row|grid|3d --prefix box \
  [--from N --to N] \
  [--row-from A --row-to B --col-from N --col-to N] \
  [--level-from N --level-to N] \
  [--separator "-"] [--notes ...] [--dry-run] [--max-labels 100]
```

| Flag | Default | Notes |
|---|---|---|
| `--method` | (required) | `row`, `grid`, or `3d` (single uses `add`) |
| `--prefix` | `""` | label prefix (e.g. `box`, `shelf`, `rack`) |
| `--separator` | `"-"` | label separator joining prefix and coordinates; an explicit empty string glues (`box1`) |
| `--from`, `--to` | `0` | `row`: numeric range start/end (inclusive) |
| `--row-from`, `--row-to` | `""` | `grid`/`3d`: first and last row letter (A-Z, inclusive) |
| `--col-from`, `--col-to` | `0` | `grid`/`3d`: first and last column (inclusive) |
| `--level-from`, `--level-to` | `0` | `3d`: first and last level (inclusive) |
| `--notes` | `""` | free-text notes applied to every row |
| `--dry-run` | `false` | print the labels that would be created without writing |
| `--max-labels` | `100` | sanity cap on labels a single bulk may generate |

The methods, by flag combination:

- **Row** — `go-parts locations bulk --method row --prefix box --from 1 --to 5`
  yields `box-1`, `box-2`, `box-3`, `box-4`, `box-5` (the default `--separator -`;
  `--separator ""` yields the glued legacy form `box1`…`box5`).
- **Grid** — `go-parts locations bulk --method grid --prefix shelf --row-from A --row-to B --col-from 1 --col-to 2`
  yields `shelf-A1`, `shelf-A2`, `shelf-B1`, `shelf-B2`.
- **3-D grid** — `go-parts locations bulk --method 3d --prefix rack --level-from 1 --level-to 2 --row-from A --row-to B --col-from 1 --col-to 2`
  yields `rack-1-A1` … `rack-2-B2` (8 labels).
- **Single** — one location is `go-parts locations add`; `bulk --method single`
  is rejected with a pointer to it.

`--dry-run` runs the same label generator and prints `--dry-run: would create
N location(s):` followed by the labels — the store is never opened, so it is
safe to run against a live data dir.

`--max-labels N` (default 100) caps how many labels a single bulk may generate.
The generator checks the total BEFORE allocating anything, so a pathological
input (e.g. `--to 9223372036854775807`) returns a clean error instead of a
panic. Raise the cap for larger bulks; set it to 0 or negative and the call
errors immediately.

**Partial failure:** `CreateBulk` is NOT atomic — a failure mid-bulk returns the
successfully-created rows so far plus the error. It does NOT roll back. To
recover from a partial failure, list the created locations (`go-parts locations
list`), diff against intent, and delete the unwanted rows (`go-parts locations
remove`). Re-running the same bulk collides on already-reserved via-codes and
creates duplicates (same labels, different codes). A true idempotent-retry
design (batch-id, deterministic codes) is deferred to a later slice.

### `go-parts locations list`

Lists every location in Pebble key order (ULID-ordered). `--json` emits the
record array (empty result is `[]`, not `null`).

```
go-parts locations list [--json]
```

| Flag | Default | Notes |
|---|---|---|
| `--json` | `false` | machine-readable JSON array |

### `go-parts locations remove`

Removes a location by id or Via code (`L-...` resolves to the id). One
refusal guards it:

- **Refuses if components are still assigned** (`ErrHasParts`) — any part
  stocked here (a Component row) blocks the delete. Remove those components
  first (`go-parts locations remove-component`) or re-stock them elsewhere.
  The check composes at the CLI caller over the components store because the
  locations store cannot see the components keyspace (§5.1); the same refusal
  guards the REST and web-UI delete paths.

```
go-parts locations remove <id|viacode> [--dry-run]
```

| Flag | Default | Notes |
|---|---|---|
| `--dry-run` | `false` | print what would happen — either REFUSE (with the assigned-components count and the remediation) or a clean `would remove` |

### `go-parts locations label`

Renders the location's scannable SVG label (§5.17) to stdout — pipe to a file
(`> bin.svg`) or open in a browser/print dialog. The QR encodes the configured
`public_base_url` + `/via/{code}`; with no base configured (config.json) it
encodes the relative `/via/{code}`. Accepts an id or an `L-` via-code.

```
go-parts locations label <id|viacode>
```

## Components (stock)

Stock lives on Component rows — the junction between a part and a location.
These subcommands create, inspect, adjust, and remove that stock; every id
argument accepts a Via code (`L-…` location, `P-…` part), resolved to the id.

### `go-parts locations add-component`

Stocks a part into a location with an initial quantity (creates the Component
row; the part and location must already exist).

```
go-parts locations add-component <locID|via> <partID|via> <qty> [--tag ...]
```

| Flag | Default | Notes |
|---|---|---|
| `--tag` | (none) | component tags (repeatable) |

### `go-parts locations list-components`

Lists the components at a location — the bin's contents: part id, quantity,
tags.

```
go-parts locations list-components <locID|via> [--json]
```

| Flag | Default | Notes |
|---|---|---|
| `--json` | `false` | machine-readable JSON array |

### `go-parts locations adjust`

Adjusts a component's quantity by a delta — stock in or stock out. The reason
is recorded in the movement history (the same history that drives the UI's
last-used timestamps).

```
go-parts locations adjust <locID|via> <partID|via> <delta> [--reason "..."]
```

| Flag | Default | Notes |
|---|---|---|
| `--reason` | `""` | reason for the adjustment (recorded in history) |

### `go-parts locations remove-component`

Removes the component row — the part assignment — from a location. The part
record itself is untouched; this is the un-stock path that unblocks
`go-parts locations remove`.

```
go-parts locations remove-component <locID|via> <partID|via>
```

## Via

### `go-parts via`

`go-parts via` resolves a Via code (§5.17) — the generic resolver at the CLI.
A `P-` code resolves to its part; an `L-` code resolves to its location **with
its contents embedded** (scan-to-find — the components stocked there, with
part and quantity). Output is JSON (the same shape as `GET /via/{code}`); pipe
through `jq` for a readable view. An unknown code exits non-zero.

```
go-parts via <code>
```

## Maintenance

Store-level reads and repairs. All of them open the store directly and hold
the Pebble flock — stop the daemon first.

### `go-parts reindex`

Rebuilds the BM25 search index from the parts store. Fixes parts persisted
but unsearchable (a SIGKILL/OOM between the record write and the FTS write —
the two-write partial-failure gap). Parts already correctly indexed are
no-ops (the FTS idempotency guard).

```
go-parts reindex
```

### `go-parts fix-qty`

Re-derives every part's `QtyOnHand` as the sum of its Component quantities
across all locations (the flat-model derivation). A part with stock but zero
Component records is pre-flat-model legacy stock: it is **skipped and
reported, never silently zeroed** — re-stock it into a location, or pass
`--force` to zero it deliberately.

```
go-parts fix-qty [--force]
```

| Flag | Default | Notes |
|---|---|---|
| `--force` / `-f` | `false` | zero legacy stock (parts with `QtyOnHand` but no Component records) |

### `go-parts dedupe-report`

Lists duplicate MPN / local-number values (identity uniqueness, schema v5).
Collisions are grouped by whitespace-trimmed value — a legacy `" PAD-1 "` and
a new `"PAD-1"` are the same identity — and every colliding part id is named.
The records are never modified, but note the open runs the normal migration +
identity-index backfill path, so it is not a purely read-only open. Fix a
collision by editing the losing part's MPN or local number (Update
re-reserves identities once the collision is resolved).

```
go-parts dedupe-report
```

## Defaults and safety

- **Loopback bind** — `127.0.0.1:7890` (config `DefaultBind`, PRD §5.18). go-parts
  never exposes a writable surface to the network without an explicit operator
  action (`--bind` or `config.json`).
- **No secrets on disk** — `~/.go-parts/config.json` holds only non-secret
  `data_dir` and `bind` (§5.18, see [config reference](../reference/config.md)).
- **State file perms** — `<data_dir>/daemon.json` is written `0600`; it carries
  no secret, but tightening perms prevents a non-operator user from rewriting
  the PID to weaponize `stop`.
