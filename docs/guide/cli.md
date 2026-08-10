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
`locations`. An empty value resolves to `~/.go-parts` (mirrors go-rag /
MuninnDB's home-dir default).

## Subcommands

### `go-parts start`

Starts the foreground HTTP server. The `start` process **is** the server — it
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
file (`<data_dir>/daemon.json`) is published. **The listener binds before the
state file is written** — a port-in-use error surfaces before the daemon
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

## Locations

`go-parts locations` manages physical storage locations (bins, drawers,
shelves, boxes — optionally nested). Locations are addressed by a Via code
(`L-XXXXXX`, §5.17) and are navigated/scanned, **not** full-text-indexed (no
FTS, unlike parts). Defined in `internal/locations`; records live under the
`locations` keyspace (`0x11`, see [data model](../reference/data-model.md)).

The `locations` subcommands open the store directly (they do not talk to the
daemon). They hold the Pebble flock, so they **fail if the daemon is running**
— stop the daemon first for bulk CLI ops, or use the future web UI (v1
single-operator posture).

```
go-parts locations [--data-dir DIR] <subcommand> [flags]
```

### `go-parts locations add`

Creates a single location record. Via code is auto-assigned (`L-XXXXXX`) unless
`--parent` is given, in which case the parent must already exist (cycle guard
runs on `update`, not on `create` — a missing parent is rejected here).

```
go-parts locations add --label "Bin A3" [--parent ID] [--single-part-only] [--notes ...] [--dry-run]
```

| Flag | Default | Notes |
|---|---|---|
| `--label` | (required) | location label, e.g. `"Bin A3"` |
| `--parent` | `""` (top-level) | parent location id (nested storage) |
| `--single-part-only` | `false` | bin holds only one part type |
| `--notes` | `""` | free-text notes |
| `--dry-run` | `false` | print what would happen without executing |

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

Removes a location by id or Via code (`L-...` resolves to the id). **Refuses if
the location has children** (`ErrHasChildren`) — reparent or delete the
children first; never cascade.

```
go-parts locations remove <id|viacode> [--dry-run]
```

| Flag | Default | Notes |
|---|---|---|
| `--dry-run` | `false` | print what would happen (incl. child-count refusal) without executing |

### `go-parts locations tree`

Prints the location hierarchy as an indented tree (roots are top-level
locations; nesting follows `parent_id`).

```
go-parts locations tree
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
