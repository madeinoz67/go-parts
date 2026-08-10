# REST API

The REST surface is a stdlib `net/http` `ServeMux` over one `parts.Store` +
one field-weighted BM25 FTS (`internal/rest/server.go`). Go 1.26
method-patterns make the routing table executable documentation; `{id}` is
read via `r.PathValue`.

v1 has a **no-op auth middleware seam** (`Server.auth`, PRD §5.8): every
request passes through one interceptor point so makerspace auth is additive
later rather than a rewrite. There is no auth in v1.

## Routes

| Method | Path | Status codes | Notes |
|---|---|---|---|
| `GET` | `/healthz` | `200` | liveness probe; returns text/plain `ok`, no store touch |
| `GET` | `/stats` | `200` | `{"parts_total": N}` via `Store.Count()` |
| `GET` | `/parts?q=…` | `200` | BM25 search (FTS); empty/absent `q` → empty list |
| `POST` | `/parts` | `201` (+`ETag`) / `400` / `409` / `500` | create; caller MUST NOT set `ID`/`Version`/audit. `400` if `DefaultLocationID` references a missing location; `409` if it targets an occupied `SinglePartOnly` location (Slice 3b) |
| `GET` | `/parts/{id}` | `200` (+`ETag`) / `404` | one part by ID |
| `PATCH` | `/parts/{id}` | `200` (+`ETag`) / `428` / `400` / `404` / `409` | edit; `If-Match` required |
| `DELETE` | `/parts/{id}` | `204` / `404` | remove record + FTS entry |
| `POST` | `/parts/{id}/stock` | `200` (+`ETag`) / `400` / `404` | commutative stock delta |

All bodies are `application/json`.

## `ETag` / `If-Match` contract

Optimistic concurrency (§5.14) is exposed via `ETag` and `If-Match`:

- **`ETag`** — emitted on every Part-bearing response (`POST` `201`, `GET`
  `/parts/{id}`, `PATCH` `200`, `POST /parts/{id}/stock` `200`). Format: a
  quoted integer = the Part's `Version` — e.g. `"3"`.
- **`If-Match`** — **required** on `PATCH`. Send the `ETag` value you got
  from a prior `GET`.
  - Missing header → `428 Precondition Required`.
  - Header not of the form `"<int>"` (or a bare integer, accepted as a
    courtesy) → `400 Bad Request`.
  - `int < 1` → `400 Bad Request`.
  - The loaded version no longer matches the in-lock stored version →
    `409 Conflict` — **never silently overwritten**.

`POST /parts/{id}/stock` does **not** require `If-Match`: stock adjustment is
a commutative delta that does not bump `Version` (§5.14). The response's
`ETag` is pinned to the unchanged `Version`.

## PATCH semantics

`PATCH` does **Get-then-edit**: the handler loads the current part, copies
writable fields from the patch body onto it (zero-means-skip per field), then
`Store.Update(loaded, currentVersion)`. This round-trips `CreatedAt`/
`CreatedBy` (a bare `Update` would zero them).

**Stock is not editable on `PATCH`** — the F3 invariant (§5.14): `QtyOnHand`
is `AdjustStock`'s exclusive domain. The handler never copies `QtyOnHand`
from the patch body, and `Store.Update` preserves the in-lock `cur.QtyOnHand`
server-side. Even a patch body carrying `QtyOnHand` has no effect. Stock
changes go to `POST /parts/{id}/stock`.

Authoritative fields never taken from the patch body: `ID`, `Version`,
`QtyOnHand`, `CreatedAt`/`CreatedBy`, `UpdatedAt`/`UpdatedBy`.

**`DefaultLocationID` (Slice 3b) is writable but cannot be CLEARED over REST.**
A patch carrying a non-empty `DefaultLocationID` assigns the part's home
location (the `single_part_only` guard runs in `Store.Update`: `409 Conflict`
if the target is a `SinglePartOnly` location already holding a different part;
`400 Bad Request` if the id references no location). Because `applyPatch` uses
zero-means-skip, a patch omitting the field leaves it unchanged and a patch
carrying `"DefaultLocationID": ""` is skipped (not a clear). Clearing over REST
needs a dedicated path (a field-clear convention or a `DELETE …/location`),
deferred — the web UI clears via the `<select>`'s explicit `(unassigned)`
option, which the handler reads directly (not through `applyPatch`).

**Two distinct `409 Conflict` cases on PATCH** (both surface as 409; the
response body distinguishes them): (1) the `expectedVersion` no longer matches
the in-lock stored version — the §5.14 optimistic-concurrency race, "edited
elsewhere"; (2) a `DefaultLocationID` assignment that trips the
`single_part_only` guard, "that bin holds another part". A client should read
the body to tell them apart.

## Stock endpoint

`POST /parts/{id}/stock` with body `{"Delta": <int>}` (Go field name — no
`json:` tags). Applies a commutative delta to `QtyOnHand` under the per-id
striped lock; returns the updated Part so callers see the new `QtyOnHand`
without a follow-up `GET`. Empty body is treated as `Delta=0` (no-op). A
missing Part → `404`.

## JSON field names — Go PascalCase

The `Part` struct has **no `json:` tags** (see
[the data model note](data-model.md#the-part-record) and the comment in
`internal/parts/part.go`). JSON field names are Go PascalCase — `MPN`,
`QtyOnHand`, `ViaCode`, `CreatedAt`, etc.

This is the v1 wire contract: the e2e test and any v1 client depend on
PascalCase names. **Adding `json:` tags later is a breaking change** for any
v1 client, so any such change is a deliberate major-version bump, not a silent
rename. A `POST /parts` or `PATCH /parts/{id}` body must use PascalCase keys.

## Search

`GET /parts?q=…` drives the query through the field-weighted BM25 FTS,
hydrating the top 20 hits via `Store.Get`. If a hit's Part was deleted between
the FTS read and the hydrate (a benign race — the FTS is eventually
consistent), that hit is skipped. An empty or absent `q` returns an empty
list (the tokenizer drops everything → search returns nil).
