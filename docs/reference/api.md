# REST API

The REST surface is a stdlib `net/http` `ServeMux` over one `parts.Store` +
one field-weighted BM25 FTS, the locations/components stores, and the shared
via index (`internal/rest/server.go`). Go 1.26 method-patterns make the
routing table executable documentation; `{id}` is read via `r.PathValue`.

v1 has a **no-op identity seam plus a uniform CSRF posture** (`Server.auth`
composes `internal/origin.Guard`, PRD §5.8): every request passes through one
interceptor point so makerspace auth is additive later rather than a rewrite.
There is no identity auth in v1.

**CSRF posture (all surfaces — REST, UI, MCP):** every mutating endpoint
(`POST`/`PUT`/`PATCH`/`DELETE`) returns `403 cross-origin request blocked`
when the request carries a browser-issued `Origin` header that does not match
the request's own `scheme://host`. Requests without an `Origin` header
(curl, scripts, `claude mcp http`) pass — they are not CSRF vectors; v1 has no
cookies or ambient credentials for a forged request to ride. Reads are
unguarded. This exists because the daemon binds loopback and CORS-"simple"
requests (`<form>` POSTs, `fetch` with `Content-Type: text/plain`) reach it
from any web page you visit — and REST never enforced Content-Type, so such a
request can carry a JSON body the decoder accepts. The one authority for the
rule is `internal/origin`. Two known accepted limitations, both closed by the
future auth layer's configured-scheme/host check: DNS rebinding (an `Origin`
matching a rebound `Host`), and a TLS-terminating reverse proxy (the daemon
sees plain HTTP and expects an `http` Origin, so browser mutations fail
**closed** behind a proxy — v1's default loopback posture is unaffected).
`X-Forwarded-Proto` is deliberately not honored (spoofable on a non-loopback
bind).

## Routes

| Method | Path | Status codes | Notes |
|---|---|---|---|
| `GET` | `/healthz` | `200` | liveness probe; returns text/plain `ok`, no store touch |
| `GET` | `/stats` | `200` | `{"parts_total": N}` via `Store.Count()` |
| `GET` | `/parts?q=…` | `200` | BM25 search (FTS); empty/absent `q` → empty list. TUI companion params: `?all=1` lists the corpus (browse); `?low=1` keeps `QtyOnHand` ≤ `ReorderPoint` (incl. 0/0); both compose with `q` |
| `POST` | `/parts` | `201` (+`ETag`) / `400` / `409` / `500` | create; caller MUST NOT set `ID`/`Version`/audit. `409` if the `MPN` or `LocalNumber` is already taken (identity uniqueness, schema v5) |
| `GET` | `/parts/{id}` | `200` (+`ETag`) / `404` | one part by ID |
| `GET` | `/parts/{id}/stock` | `200` / `404` | the part + per-location stock (`{Label,ViaCode,Quantity}`) + 10 most recent movements (newest-first); `{id}` accepts a part id **or** a `P-` via-code — the REST twin of MCP `get_part`'s aggregate |
| `PATCH` | `/parts/{id}` | `200` (+`ETag`) / `428` / `400` / `404` / `409` | edit; `If-Match` required. `409` = version conflict **or** taken identity |
| `DELETE` | `/parts/{id}` | `204` / `404` / `409` | remove record + FTS entry; `409` if Components still reference it (`ErrHasComponents` — un-stock first) |
| `GET` | `/via/{code}` | `200` / `303` / `404` | generic Via resolver (§5.17): location → `{Type,Location,Contents}`; part → `{Type,Part}`; browsers get `303` → UI deep-link |
| `POST` | `/parts/{id}/label` | `200` (`image/svg+xml`) / `404` | render the part's scannable label SVG; `{id}` accepts a part id **or** a `P-` via-code |
| `POST` | `/locations/{id}/label` | `200` (`image/svg+xml`) / `404` | render the location's label SVG; `{id}` accepts a location id **or** an `L-` via-code |
| `GET` | `/locations` | `200` | list every location (JSON array) |
| `GET` | `/locations/{id}` | `200` (+`ETag`) / `404` | one location by ID |
| `POST` | `/locations` | `201` (+`ETag`) / `400` | create; caller MUST NOT set `ID`/`Version`/`CreatedBy` |
| `PATCH` | `/locations/{id}` | `200` (+`ETag`) / `400` / `404` / `409` | RFC 7396 edit (`Label`/`Notes`/`Tags`); `ViaCode` immutable; `If-Match` required; `409` on version conflict |
| `DELETE` | `/locations/{id}` | `204` / `404` / `409` | remove; `409` if Components are still assigned (`ErrHasParts` — un-stock first) |
| `GET` | `/locations/{id}/components` | `200` | list the bin's components (part ref, quantity, tags, movement history) |
| `POST` | `/locations/{id}/components` | `201` / `400` / `409` / `500` | stock a part into the bin — body `{"PartID","Quantity","Tags"}`; `409` if the pair already exists (`ErrDuplicate`); a missing Part record is refused by the referential-integrity guard |
| `PATCH` | `/locations/{id}/components/{partId}` | `200` / `400` / `404` | stock in/out — body `{"Delta","Reason"}`; appends a Movement, re-derives the part's `QtyOnHand` |
| `DELETE` | `/locations/{id}/components/{partId}` | `204` / `404` | remove the component row (un-stock; the part record is untouched) |

All bodies are `application/json`.

## `ETag` / `If-Match` contract

Optimistic concurrency (§5.14) is exposed via `ETag` and `If-Match`:

- **`ETag`** — emitted on every record-bearing response (parts `POST`/`GET`/
  `PATCH`; locations `POST`/`GET`/`PATCH`). Format: a quoted integer = the
  record's `Version` — e.g. `"3"`.
- **`If-Match`** — **required** on `PATCH`. Send the `ETag` value you got
  from a prior `GET`.
  - Missing header → `428 Precondition Required`.
  - Header not of the form `"<int>"` (or a bare integer, accepted as a
    courtesy) → `400 Bad Request`.
  - `int < 1` → `400 Bad Request`.
  - The loaded version no longer matches the in-lock stored version →
    `409 Conflict` — **never silently overwritten**.

Stock moves (`PATCH /locations/{id}/components/{partId}`) do not require
`If-Match`: a stock delta is commutative and does not run the record-Update
path (§5.14).

## PATCH semantics

`PATCH` does **Get-then-edit**: the handler loads the current record, copies
writable fields from the patch body onto it, then `Store.Update(loaded,
currentVersion)`. This round-trips `CreatedAt`/`CreatedBy` (a bare `Update`
would zero them).

**Stock is not editable on part `PATCH`** — the F3 invariant (§5.14):
`QtyOnHand` is derived from the part's Components. The handler never copies
`QtyOnHand` from the patch body, and `Store.Update` preserves the in-lock
`cur.QtyOnHand` server-side. Even a patch body carrying `QtyOnHand` has no
effect. Stock changes go through the component endpoints (or CLI
`go-parts locations adjust` / the UI's stock in-out).

Authoritative fields never taken from the patch body: `ID`, `Version`,
`QtyOnHand`, `CreatedAt`/`CreatedBy`, `UpdatedAt`/`UpdatedBy`.

**Patch semantics are RFC 7396 JSON Merge Patch**: a key PRESENT in the body
overwrites (non-null) or clears (JSON `null` → the field's zero value); an
ABSENT key is left unchanged. So `"Description":null` clears,
`"ReorderPoint":null` resets to 0, `"Tags":null` empties. Writable part
fields: `MPN`, `LocalNumber`, `Manufacturer`, `Category`, `Subcategory`,
`PartType`, `Description`, `Footprint`, `UnitOfMeasure`, `DatasheetRef`,
`PackageQty`, `ReorderPoint`, `Tags`, `Specs`, `CustomFields`.

**Two distinct `409 Conflict` cases on part `PATCH`** (both surface as 409;
the response body distinguishes them): (1) the `expectedVersion` no longer
matches the in-lock stored version — the §5.14 optimistic-concurrency race,
"edited elsewhere"; (2) the patched `MPN`/`LocalNumber` is already taken by
another part (schema v5 identity uniqueness). A client should read the body
to tell them apart.

## Components (stock) endpoints

Stock lives on Component rows — one per (location, part) pair, keyed
`LocationID(26) | PartID(26)` (see the
[data model](data-model.md#the-component-record-stock-junction)):

- `GET /locations/{id}/components` — the bin's contents: part ref, quantity,
  tags, and the full movement history.
- `POST /locations/{id}/components` — body
  `{"PartID":"…","Quantity":N,"Tags":[…]}` (Go field names — no `json:`
  tags). The Part must exist (referential-integrity guard — no orphan
  Components). `409` if the pair already exists; use PATCH for quantities.
- `PATCH /locations/{id}/components/{partId}` — body `{"Delta":N,"Reason":"…"}`.
  A commutative delta under the component's striped lock; each move appends a
  `Movement{Timestamp, Delta, Reason}` and re-derives the part's `QtyOnHand`
  as the sum across its Components. Returns the updated component. A missing
  pair → `404`.
- `DELETE /locations/{id}/components/{partId}` — removes the row (un-stock).
  The part record itself is untouched.

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

## Via resolver + labels (§5.17)

`GET /via/{code}` is the **generic** Via resolver — one endpoint, not one per
entity. It dispatches on the via index's type tag and returns a `Resolved`
object (PascalCase, no json tags):

- **location** → `{Type:"location", Location:{…}, Contents:[{…component…}]}`.
  The embedded `Contents` IS scan-to-find — the Components stocked at this
  location (part + quantity). `Contents` is `null`/empty when the bin holds
  no stock.
- **part** → `{Type:"part", Part:{…}}`.
- unknown code → `404` (`via.ErrNotFound`).

**Browser redirect (content-negotiation):** a client sending `Accept: text/html`
(a browser scanning the QR) is `303`-redirected to the entity's UI deep-link —
a part → `/ui/?part={id}`, a location → `/ui/locations?loc={id}`. API clients
(`Accept: application/json`, or curl's `*/*`) still get the JSON above. So the
QR's encoded `/via/{code}` URL serves both a browser scan (lands on the entity
open in the UI) and an API call, without a separate endpoint or re-cutting
labels.

`POST /parts/{id}/label` and `POST /locations/{id}/label` render a black-on-
white scannable **SVG label** (`Content-Type: image/svg+xml`). The QR encodes
`{public_base_url}/via/{via_code}` — the resolution URL — with the
human-readable via-code (monospace) and the entity title (a location's `Label`
or a part's MPN·description) beneath. `{id}` accepts a bare id **or** the
entity's via-code (`P-`/`L-`).

**`public_base_url`** (config.json, non-secret §5.18) is the base for the QR
URL. When unset, the REST label endpoints derive it from the request's
`scheme://host` **only for loopback hosts** (a Host-header-poisoning guard); on
a non-loopback bind without a configured base the QR encodes the relative
`/via/{code}` (safe but less useful — set `public_base_url`). The CLI
`locations label` derives it from config the same way. Physical printing (page
layout, the OS print dialog) is a client concern; go-parts renders the SVG,
not a print driver.

## MCP endpoint (POST /mcp)

The daemon serves the Model Context Protocol at `POST /mcp` on the same bind
as REST and the UI (`127.0.0.1:7890` by default) — stateless JSON-RPC 2.0,
one request per POST (`initialize`, `tools/list`, `tools/call`). Connect an
agent with `claude mcp add --transport http http://127.0.0.1:7890/mcp`.

Tools are thin renderings of the same composed stores REST and the UI use —
identical results, never a hop through REST (§5.2). Tool ARGUMENT keys and
compact summaries use snake_case; full part records render with the same
PascalCase Go field names as REST bodies.

| Tool | Input | Notes |
|---|---|---|
| `search_parts` | `query?`, `tag?` (lowercase — tags are stored lowercased), `low?`, `limit?` (≤100, default 20) | same filters as the UI live filter (FTS or list → tag → low), with one depth difference: a query fetches the top **20** BM25 hits (the REST `GET /parts` depth) before tag/low filtering, where the UI's live filter fetches 500 |
| `get_part` | one of `id` / `mpn` / `local_number` (also `P-` via-code as `id`) | full record + per-location stock + 10 most recent movements; an ambiguous selector errors listing every match |
| `upsert_part` | `part` (full record, PascalCase fields) | empty `ID` creates; otherwise the record's `Version` is the optimistic-concurrency token — a stale `Version` is rejected with the conflict error, never a silent overwritten write. `QtyOnHand` is honored only at create; stock moves go through `adjust_stock` |
| `stock_part` | `part`, `location` (label or `L-` via-code), `qty` (≥ 0) — all required; `tags?` (array of strings) | places FIRST stock — creates the (location, part) row with an "initial" movement and re-derives part qty (the same engine call as REST `POST /locations/{id}/components`); a duplicate pair errors pointing at `adjust_stock`; a negative `qty` is rejected as a usage error |
| `adjust_stock` | `part`, `location` (label or `L-` via-code), `delta`, `reason` — all required | appends a movement and re-derives part qty; a location the part isn't stocked at is an error listing where it IS stocked |
| `list_low_stock` | — | parts with `QtyOnHand` ≤ `ReorderPoint` (the UI low filter's comparison) |
| `get_inventory_stats` | — | `parts_total`, `locations_total`, `low_stock`, `out_of_stock` |

Domain failures (not-found, version conflict, duplicate identity, bad
location) return tool results with `isError: true` carrying the engine
sentinel's message verbatim — the same strings REST surfaces. Only
protocol-level failures (malformed JSON-RPC, unknown method, unknown tool)
are JSON-RPC errors. No identity auth in v1; the endpoint is behind the
uniform CSRF posture (`internal/origin` — a cross-origin browser `POST` gets
`403`, no-`Origin` clients pass).
