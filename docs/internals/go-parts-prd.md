# PRD: go-parts — Standalone Electronics Parts Database

**Author:** Stephen Eaton
**Status:** Draft v3.7 (corrected §5.20 against MuninnDB's actual CLAUDE.md — review agent is a repo-local subagent invoked via /code-review, not a GitHub Actions auto-trigger; added CLAUDE.md structure and a MuninnDB dogfooding note)
**Date:** 2026-08-07
**License:** MIT

---

## 1. Overview

**Problem:** Electronics component inventory (resistors, capacitors, ICs, connectors, sensors, dev boards, engraving/CNC hardware) is scattered across bins, drawers, browser bookmarks, and memory. There's no single source of truth for "do I have this part," "where is it physically," "what did I use it in," or "when do I need to reorder."

**Goal:** A single-binary, embedded-storage electronics parts database that stands up in minutes, is queryable via REST/MCP/CLI/web, and slots directly into AI-assisted workflows — so Claude Code/Desktop can be asked "do I have a 10k 0805 resistor" and get a real answer.

Follows the same low-friction philosophy as **go-rag** and **MuninnDB**: one binary, no mandatory external services, multiple protocol surfaces over a shared embedded store.

---

## 2. Goals

- Single Go binary, embedded **Pebble** KV store — no Postgres/MySQL/external DB dependency
- Sub-5-minute cold start (binary or `docker compose up`)
- Full-text + semantic search over parts (BM25 + optional vector embeddings), including datasheet PDF text
- **MCP-native**: parts search/CRUD exposed as MCP tools for direct AI-agent querying
- **REST API** for programmatic access, web frontend, and integrations (KiCAD plugin, barcode scanner)
- Minimal **embedded web UI** served from the same binary — no separate frontend build/deploy
- **Terminal UI (TUI)** — `go-parts tui` subcommand for search/browse/stock-adjust straight from the command line, no browser required (§5.11)
- Physical **location/bin tracking** suited to a workshop/homelab (drawer, bin, shelf, box, nested)
- **BOM import/export** for KiCAD projects
- Backup-friendly (flat file / Pebble snapshot) — compatible with your existing Backblaze B2 + Backrest pattern
- Optional **gateway to go-rag and MuninnDB** so parts data joins your broader personal knowledge/RAG/memory ecosystem rather than sitting in a silo
- Navigation, data model, and table behavior guided by **PartsBox.io** — a mature reference for this exact problem — scoped down to what a solo maker needs rather than its full team/enterprise feature set

## 3. Non-Goals (v1)

- Multi-user / auth / RBAC **in v1** (single operator, consistent with go-rag) — the architecture is deliberately kept forward-compatible with this so it isn't a rewrite later; see §5.8
- Distributed or clustered deployment
- Marketplace/e-commerce integration beyond read-only supplier links
- **True real-time** stock/price sync (v1 vendor plugins support on-demand enrichment and periodic background refresh, not live push updates)
- Multi-currency conversion (PartsBox's ECB-rate style handling) — single currency for v1, consistent with single-operator scope
- PDF export of tables/pricing — CSV export only (§7.2)
- Audit trail / regulatory traceability (e.g. 21 CFR Part 11 style) — not relevant to a personal workshop

## 4. Users / Personas

- **Primary:** Stephen, solo maker — fast "do I have X" lookups mid-build; AI-assisted queries from Claude while working on PCB/Home Assistant/engraving projects.
- **Secondary (future):** a makerspace's members sharing one physical inventory — same catalog and stock pool, multiple people accessing it concurrently, each attributable for what they changed. Not a multi-tenant SaaS scenario (separate orgs with private data, like PartsBox's company-switching) — one shared space, one shared database, multiple identified users. See §5.8.

---

## 5. Architecture

### 5.1 Storage Engine
- Pebble as the embedded LSM KV store (same pattern as go-rag/MuninnDB)
- Core keyspaces: `parts`, `categories`, `locations`, `suppliers`, `projects/boms`, `datasheets` (blob or path ref), `search_index` (BM25 postings + optional vector index), `stock_history`, `meta` (schema version marker — see §5.13)
- **Search reuse (resolved):** go-parts embeds go-rag's search module directly as a Go library rather than forking a lightweight subset — BM25 + vector search comes for free, stays in sync with go-rag's improvements over time, and lines up with the embedded-mode go-rag gateway in §5.5 (same library, same decision, not two separate builds)

**How search actually works — two tiers, only one needs a model:**

- **BM25 (default, Phase 1, no model required):** pure lexical/statistical text matching — the same ranking algorithm Elasticsearch and Lucene use by default. It tokenizes MPN, description, specs, tags, and datasheet text, then ranks by term frequency / inverse document frequency. No embedding, no model download, no GPU, no API calls. This is what "find my 10k resistor" runs on, and it's on from the very first run with zero setup — consistent with the whole "start using it straight away" goal.
- **Vector/semantic search (optional, Phase 3, model required):** this is the part that actually needs an embedding model — turning part text into vectors so semantically related queries match even without shared keywords. Since this is optional and off by default until Phase 3, it doesn't need resolving now, but when it is built, it uses the same `GOPARTS_EMBED_URL` env var pattern as §5.9's enrichment config — configured independently, not sharing a setting with enrichment, following MuninnDB's own separation between its `_ENRICH_URL` and its embedder config.
- **Net effect:** with the model provider unconfigured (the default), go-parts runs on BM25 alone — full-text search works completely, at zero cost and zero external calls. Vector search is additive on top, not a replacement, and only activates once a provider is configured.

### 5.2 Interface Layers (parallel surfaces over one core service)

| Layer | Purpose |
|---|---|
| **RPC** | Wire-level API (JSON-RPC) — decouples core logic from HTTP; used by CLI and reserved as a tight-integration surface for local tools, including a future KiCAD plugin |
| **REST** | Public HTTP API — CRUD, search, BOM import/export, barcode lookup; also usable by a future KiCAD plugin |
| **MCP** | MCP server — exposes parts operations as tools for AI agents |
| **Website** | Embedded static web UI — browse/search/edit without touching the API directly |
| **TUI** | Terminal UI — `go-parts tui` subcommand of the same binary, a REST client for the command line (§5.11), not a fifth server-side protocol |

**RPC scope (resolved):** RPC is built as a real wire-level protocol from the start, not just in-process Go function calls — a future KiCAD plugin (typically a Python process, external to the go-parts binary) rules out an in-process-only design. Both RPC and REST are kept as viable surfaces for that plugin; which one it actually uses is decided when the plugin is built (§10 Phase 4), not now.

### 5.3 Deployment
- Single binary + Docker image (fits your UnRaid/Portainer/Nginx Proxy Manager stack)
- Config via env vars or one YAML file
- systemd unit as bare-metal alternative
- The TUI (§5.11) is a subcommand of the same binary (`go-parts tui`), not a separate artifact to build or deploy
- Default network exposure and secrets handling — §5.18

### 5.4 Vendor Plugin Architecture

No supplier (LCSC, Mouser, DigiKey, ...) is hardcoded into core logic. Each vendor integration is a plugin implementing a common interface, registered at startup.

**Plugin interface (draft)**
```go
type VendorPlugin interface {
    Name() string
    LookupByMPN(ctx context.Context, mpn string) (VendorPart, error)
    Search(ctx context.Context, query string) ([]VendorPart, error)
    GetPricing(ctx context.Context, mpn string) (PricingInfo, error)
    GetStock(ctx context.Context, mpn string) (StockInfo, error)
}
```

**Loading model (resolved)**
- **Compiled-in plugins** — a registry of `VendorPlugin` implementations built into the binary. Simplest, no dynamic-loading complexity, keeps the "single binary" low-friction goal intact. Out-of-process plugins (subprocess + RPC, e.g. `hashicorp/go-plugin`) are deferred indefinitely — only worth revisiting if third-party/community vendor plugins become a real need.
- **Per-provider implementation, not architecture:** the `VendorPlugin` interface is the only thing that's formal up front. How any given vendor actually satisfies it — official API, scraped data, third-party dataset — is an implementation detail decided when that provider is built, not a blocker on the architecture itself. This applies directly to LCSC, which has no broad official public parts API: whatever route it ends up using (JLCPCB/EasyEDA data, scraping, etc.) is contained entirely inside its own `VendorPlugin` implementation and doesn't affect the interface, the registry, or any other vendor.

**Config**
- Per-vendor enable/disable + priority order live in `.go-parts/config.json` — not secrets, safe to read/edit/back up
- API keys/credentials are **env var only** — `GOPARTS_LCSC_API_KEY`, `GOPARTS_MOUSER_API_KEY`, etc. — never written to `config.json` (see §5.18)
- This simple priority order is the v1 version of what becomes named, reorderable **vendor rule groups** in Phase 6 (§7.3), modeled on PartsBox's fallback-chain approach to offer selection

**Use cases enabled**
- Auto-enrich a part from an MPN lookup (description, package, datasheet URL, pricing, stock)
- Periodic background refresh of pricing/stock for parts with `supplier_links`
- Cross-vendor substitute search (future)

**Reliability: not every failure means the same thing, so retry can't be one-size-fits-all.** §5.10's generic job-retry-with-backoff is fine for background jobs in general, but applying it uniformly to vendor calls wastes retry budget on failures that will never succeed, and ignores signals the vendor is already giving.

| Failure | Retry? | Handling |
|---|---|---|
| **429 Rate Limited** | Yes | Honor the vendor's `Retry-After` header if it sends one; fall back to exponential backoff (§5.10's pattern) only if it doesn't |
| **5xx / connection failure / timeout** | Yes | Exponential backoff, same as everywhere else, capped retry count |
| **401/403 Auth failure** | No | Fails immediately — retrying with a bad key just wastes calls for a result that's never going to change |
| **404 / no match at this vendor** | Not a failure | A valid negative result. Falls through to the next vendor in priority order (already-established fallback), doesn't retry or error |

**Circuit breaker on auth failure, not per-part retries.** If a vendor's API key is invalid, every remaining call to that vendor in the current run will fail the same way — retrying part 2 through 500 of a bulk import against a dead credential is pure waste. The first 401/403 from a vendor disables that plugin *for the rest of the current run* and falls through to the next vendor in priority order for every remaining part, rather than re-discovering the same failure hundreds of times. `go-parts vendor status` (§5.12) surfaces this state clearly — "LCSC: disabled this run (auth failure)" — so it's visible, not silently degraded.

**Per-run caching for repeated MPNs.** A BOM with the same MPN on fifteen lines, or an import that touches parts already looked up recently, shouldn't hit the vendor fifteen times for one answer. Lookups are cached in memory, keyed by vendor + MPN, for the duration of one run (one bulk import, one `vendor sync` invocation) — then discarded. Deliberately not a long-lived cache: pricing and stock data going stale defeats the point of the periodic refresh this section already supports, so nothing here should outlive the run that populated it.

This is stage one of enrichment — raw data from a vendor. §5.9 covers an optional second stage that turns raw data (or just a datasheet, for parts with no MPN at all) into clean structured fields via a configurable AI model.

Ties directly into the existing `Supplier` / `supplier_links[]` data model — each `supplier_links` entry maps to a registered plugin's vendor name.

### 5.5 Gateway: go-rag & MuninnDB

go-parts shouldn't be a data silo — it should plug into the same knowledge ecosystem as your other tools. Two independent, optional gateways:

**go-rag gateway (search)**
- Purpose: part records (description, specs, datasheet text) become discoverable through go-rag's cross-project semantic search — "what did I use for X" surfaces parts alongside notes/docs, not just within go-parts itself
- **Remote mode is the v1 target** (see Deployment Topology below): go-parts pushes/updates a `parts` collection into a running go-rag instance via go-rag's REST or MCP surface over the Docker Compose network
- Embedded mode (in-process, shared Pebble store via the go-rag library reuse from §5.1) stays possible in principle but isn't the deployment target right now, since go-rag runs as its own container
- Direction: one-way, go-parts → go-rag index. go-parts stays the source of truth for part data itself.

**MuninnDB gateway (cognitive memory)**
- Purpose: part usage and project associations become graph memory — "what did I use for the last 3.3V regulator, and why" becomes a queryable, recallable fact over time, not something you have to remember
- **Event granularity (resolved):** only high-level, high-signal events are pushed — `part_used_in_project` and `substitute_found` are the core ones. Routine, high-frequency events like individual stock adjustments and vendor-enrichment refreshes stay local to go-parts and are **not** pushed — they're operational noise, not the kind of thing worth recalling later.
- Recall: go-parts can query MuninnDB for related historical context when adding or enriching a part (e.g. "you used a similar part in project Y last time")
- **Remote mode is the v1 target**: MuninnDB runs as its own container, so go-parts talks to it over the network rather than via the shared-Pebble embedded-bridge pattern used between go-rag and MuninnDB themselves (`bridge-muninn.md`)

**Deployment Topology (resolved)**
go-rag and MuninnDB run as **separate containers**, orchestrated alongside go-parts in the same docker-compose stack — not embedded/co-located in the go-parts process. This makes **remote mode the primary path for v1** for both gateways.

That settles the topology, but leaves real implementation details to work through when the stack gets built:
- **Service discovery** — env-var-configured endpoints (e.g. `GORAG_URL=http://go-rag:PORT`, `MUNINNDB_URL=http://muninndb:PORT`), matching the Docker service-name-routing pattern already used elsewhere in your homelab, rather than hardcoded IPs
- **Startup ordering** — compose doesn't guarantee go-rag/MuninnDB are ready before go-parts starts; gateway calls need retry/backoff on boot rather than failing hard
- **Failure isolation** — a gateway call failing (container down, network hiccup) must degrade gracefully; go-parts keeps working standalone per the "additive, never a hard dependency" rule below

**Config**
- Both gateways independently enable/disable
- Connection mode per target: `remote` (endpoint + optional credentials, v1 default) vs `embedded` (in-process, shared store — future option if co-location ever changes)
- Credentials are optional per target, not assumed mandatory — MuninnDB itself supports running in "no-auth mode" for a trusted local network (the same pattern go-rag's own bridge already handles: prompt for a token, allow skipping it). When a token is set, it's env-var only (`GORAG_TOKEN`, `MUNINNDB_TOKEN`) — never written to `.go-parts/config.json` (§5.18). Everything else about the connection (endpoint, vault name) is a normal config-file value.
- If disabled, go-parts operates fully standalone — the gateway is additive, never a hard dependency

### 5.6 Datasheet Storage (resolved)

Storage location is conditional on the go-rag gateway, so day-one usage stays zero-config while leaving a clear upgrade path:

- **go-rag gateway enabled:** datasheets are pushed into a dedicated go-rag vault (`go-parts-datasheets`) at ingest time. The Part record stores only a reference (vault + doc id). This reuses go-rag's own PDF ingestion and text-extraction pipeline for free — go-parts doesn't build its own.
- **go-rag gateway disabled (default path):** datasheets are stored as flat files on local disk under `<data_dir>/datasheets/`, auto-created on first run. The Part record stores just the relative path. No new service, no config, no schema complexity — works the moment the binary starts, and is automatically covered once that directory is included in your existing Backrest → B2 job.
- **Text extraction for search:** in local mode, go-parts runs its own lightweight PDF-to-text step (Phase 3) so datasheet content stays searchable even without the go-rag gateway on.
- **OCR is explicitly out of scope for go-parts itself.** The local PDF-to-text step (above) extracts an existing text layer — it doesn't create one. A scanned or image-only datasheet with no text layer stays as an attached, viewable file but won't be text-searchable in local mode. Two ways to actually get OCR, neither of which is go-parts building its own: (1) attach a datasheet that's already been OCR'd by something else before it reaches go-parts — the text layer is just there to extract; or (2) enable the go-rag gateway, since OCR is part of go-rag's own ingestion pipeline (§5.5) — go-parts defers to it rather than duplicating that work, same rationale as the "go-rag reuses go-rag's own pipeline" line above.

This keeps the "start using it straight away" goal intact — nothing to configure to get datasheets working — while giving datasheets a better home automatically once go-rag is switched on. Product images follow a simpler version of the same local-disk-by-default philosophy — see §5.16.

### 5.7 Navigation & Information Architecture (PartsBox-inspired)

PartsBox.io is a mature, purpose-built reference for exactly this problem space — its navigation and functionality are the guide for where go-parts is headed, scoped down from its team/enterprise depth to what a solo maker actually needs.

**Top-level navigation (mirrors PartsBox)**

| Section | Purpose |
|---|---|
| **Parts** | Browse/search/edit the catalog — default landing view |
| **Storage** | Manage storage locations; view stock by location |
| **Projects** | BOMs — create/import, price, and build from |
| **Purchasing** | Purchase lists (cart built from projects) → orders (Open/Ordered/Received) |
| **Builds** | Build history and in-progress builds, reached from a project's Builds tab |
| **Reports** | Low-stock, inventory valuation — updates live |

**Storage philosophy (adopted directly):** don't organize storage by part category — resistors together, capacitors together, and so on. That creates constant reorganization work and doesn't scale. Parts go wherever physically fits; search and location lookup do the finding. This means the "Categories" sidebar in the UI mockup is a **search/filter facet**, not a storage organization scheme — worth being explicit about, since the two are easy to conflate. Location creation follows PartsBox's four methods, detailed in §7.1: Single, Row (1D), Grid (2D), 3D Grid (3D) — covers everything from one drawer to a compartmented SMD box, starting basic and expanding as the collection grows.

**Default storage location per part (adopted):** a part can have a "home" location, optionally marked mandatory so stock can only be added there. Cheap to build now, meaningfully reduces drift as inventory grows.

**Deliberately not adopted:** multi-tenant organizations/company switching, RBAC roles, audit trail, multi-currency (ECB-rate) conversion, PDF exports. Those solve team/enterprise/regulatory problems go-parts doesn't have — see §3 Non-Goals.

### 5.8 Multi-User Readiness (future — not v1)

v1 is single-operator, no auth, matching go-rag. But a makerspace is a real future use case — one shared physical inventory, several people accessing it concurrently — so the architecture is shaped now to make that an additive change later, not a rewrite.

**What "multi-user" actually means here:** one shared catalog and one shared stock pool for a single physical space, with multiple identified users — not a multi-tenant SaaS with separate private organizations (PartsBox's company-switching model). That's a meaningfully simpler problem, and it's the one worth designing for.

**Cheap groundwork done now:**
- **Attribution fields** — `created_by` / `updated_by` (nullable) added to Part, Location, Project, and stock-history entries in the v1 schema (§6.1), defaulting to a fixed `local` user. Costs nothing today; avoids a schema migration to retrofit "who did this" later.
- **Auth as a seam, not a redesign** — REST/RPC/MCP request handling goes through a single middleware/interceptor point from day one. v1's implementation is a no-op (always authenticated as `local`, no token required). Swapping in real auth later means replacing that one seam, not touching route handlers.
- **Concurrency is already there** — go-parts is a network-accessible service (REST/RPC/MCP) from v1, so handling multiple simultaneous clients isn't new work. What's missing for multi-user is identity and authorization on top of that, not a storage rearchitecture.

**Auth approach when it's built:** plug into your existing homelab SSO stack (Pocket-ID OIDC + TinyAuth + LLDAP) rather than building custom user/password management — consistent with how the rest of the homelab already handles auth, and far less work than a bespoke system.

**That covers the browser, not everything else — two different credential types, not one.** Pocket-ID/TinyAuth is an OIDC redirect flow, which is exactly right for the web UI. But the TUI (§5.11), MCP, RPC, and direct REST calls/scripts can't do a browser redirect — none of them have a browser to redirect. They need a second, different credential: a **personal API token**, generated per-user through the web UI once they're authenticated via SSO, then used directly by anything that isn't a browser.

**Design (documented now, built in Phase 8, same as the rest of this section):**
- Each `User` (§6.1's schema, unused in v1) can generate one or more named personal tokens through the web UI — "TUI on my laptop," "homelab script" — once logged in via Pocket-ID/TinyAuth
- Tokens are opaque, high-entropy random strings, shown once at creation and never again — only a hash is retained
- Hashed with a fast hash (SHA-256), not a slow password hash (bcrypt/argon2) — these are random 256-bit values, not human-chosen passwords, so brute-force resistance doesn't need deliberate slowness the way password hashing does
- Used as a Bearer token on RPC/REST/MCP requests — the same shape MuninnDB itself uses for its own non-browser MCP clients (a token referenced from local client config, not pushed through the OIDC flow)
- Individually revocable — losing a laptop means revoking one token, not rotating every credential in the system
- Lives with the client, not the server: the TUI reads its token from its own local client-side config or an env var, never from the server's `.go-parts/config.json` (§5.18) — this is a user's personal credential, not a service secret

**Data model addition (Phase 8, alongside `User`):** `PersonalToken` — `id`, `user_id`, `name`, `token_hash`, `created_at`, `last_used_at`, `revoked_at` (nullable)

**Permission model (target shape, not built now):** a simple two-tier model — `admin` (locations, vendors, settings, users) and `member` (day-to-day parts/stock/project work) — rather than PartsBox's full RBAC. Matches makerspace scale; a single admin role covers what a handful of trusted members need.

**Open for later, not decided now:**
- Live updates for the shared web UI (WebSocket/SSE) so members see each other's stock changes without refreshing — matches PartsBox's real-time behavior, genuinely more valuable once stock is actually shared
- Stock attribution UX — a shared physical bin means "who took the last one" becomes a real question, not just a nice-to-have

### 5.9 AI Enrichment Model (optional, configurable)

A second, distinct enrichment stage on top of vendor plugins (§5.4). Vendor plugins fetch *raw* data (description, package, pricing, stock, datasheet URL) from a supplier by MPN. The enrichment model takes that raw data — or just a datasheet, or just a rough description for a local part with no MPN at all — and turns it into clean, structured fields: normalized description, suggested category/subcategory/tags, extracted spec table from datasheet text, and a short human-readable summary.

**Interface (draft)**
```go
type EnrichmentModel interface {
    Name() string
    Enrich(ctx context.Context, input EnrichmentInput) (EnrichmentResult, error)
}

type EnrichmentInput struct {
    RawDescription string
    DatasheetText  string
    VendorData     *VendorPart // optional, populated when a §5.4 vendor plugin already ran
}

type EnrichmentResult struct {
    Specs       map[string]string
    Category    string
    Subcategory string
    Tags        []string
    Summary     string
}
```

**Configurable via a URL, not four separate variables — adopted directly from MuninnDB.** MuninnDB configures its own AI providers with a single scheme-prefixed URL plus a matching key — `MUNINN_ENRICH_URL=anthropic://claude-haiku-4-5-20251001` + `MUNINN_ANTHROPIC_KEY=sk-ant-...` — rather than separate provider/model/endpoint variables. go-parts adopts the identical shape:
```
GOPARTS_ENRICH_URL=anthropic://claude-sonnet-5           # §5.9 — this feature
GOPARTS_EMBED_URL=openai://text-embedding-3-small        # §5.1 — vector search, configured independently
GOPARTS_ANTHROPIC_KEY=sk-ant-...                          # only needed if either URL above uses anthropic://
GOPARTS_OPENAI_KEY=sk-...                                 # only needed if either URL above uses openai://
```
The scheme (`anthropic://`, `openai://`, `ollama://`) selects the provider; the rest of the URL is the model name for a hosted API (`anthropic://claude-sonnet-5`) or `host:port/model` for something self-hosted (`ollama://localhost:11434/nomic-embed-text`). Ollama needs no key — it's local. All entirely optional and unset by default, matching MuninnDB's own "works out of the box with no configuration" default.

**Enrichment and embedding are two independent settings, not one shared one — also adopted from MuninnDB, correcting an earlier draft of this doc.** MuninnDB keeps `MUNINN_ENRICH_URL` separate from its embedder configuration; you can point enrichment at Anthropic and embedding at a local Ollama model simultaneously. go-parts follows the same separation: `GOPARTS_ENRICH_URL` and `GOPARTS_EMBED_URL` are set (or not) independently. They can point at the same provider if that's simpler for your setup, but nothing requires it — and a provider key like `GOPARTS_ANTHROPIC_KEY` is shared automatically by whichever of the two URLs selects that scheme, so setting up both against the same provider doesn't mean duplicating a key.

**Provider/model selection is the env var itself here** — the `.go-parts/config.json` split from §5.18 doesn't apply to these, since even the non-secret part (which provider, which model) is more naturally one env var than a config-file section, matching MuninnDB exactly rather than introducing a third pattern.

**Optional and off by default** — same "additive, never a hard dependency" rule as the go-rag/MuninnDB gateways (§5.8's neighbor in spirit): a fresh install works with zero AI calls, zero API keys, zero cost. Turning it on is an explicit choice.

**Loading model:** compiled-in providers, same registry pattern as vendor plugins (§5.4) — consistent with the rest of the plugin architecture rather than introducing a second pattern.

**Use cases enabled**
- Structured spec extraction from datasheet text (pairs directly with the Phase 3 datasheet text-extraction work)
- Enrichment for **local parts** that have no MPN and so get nothing from vendor plugins — a rough description or attached datasheet is enough
- Auto-suggested category/subcategory/tags on part creation, reducing manual tagging
- A short summary field for browse/search readability, independent of the raw vendor description

**Provenance:** each Part tracks where its enrichment came from — `enrichment_source[]` (e.g. `["vendor:lcsc", "model:claude-sonnet-5"]`) — so it's always clear whether a field came from a vendor, a model, or manual entry.

**Independently configurable with vector search, not sharing a setting:** `GOPARTS_EMBED_URL` (§5.1's vector search tier) and `GOPARTS_ENRICH_URL` above are two separate env vars, matching MuninnDB's own separation between enrichment and embedding config — set one, both, or neither, and point them at different providers if you want to.

### 5.10 Background Processing (bulk-import friendly)

Embedding generation (§5.1) and AI enrichment (§5.9) are exactly the kind of work that shouldn't block a request — especially a bulk one. A 500-line BOM import shouldn't sit there waiting on external API calls before it returns. This follows the same pattern go-rag and MuninnDB already use.

**Pattern:** an in-process job queue backed by Pebble (a `jobs` keyspace), consumed by a small goroutine worker pool within the same binary. No external queue infrastructure (no Redis, no RabbitMQ) — stays inside the single-binary, low-friction footprint.

**Job types**, all going through the same queue for consistency:
- `embed_part` — vector embedding generation (§5.1, only runs if that tier is enabled)
- `enrich_part_model` — AI enrichment (§5.9, only runs if a provider is configured)
- `enrich_part_vendor` — vendor plugin refresh (§5.4), including the periodic background pricing/stock refresh already specified there

**Bulk import behavior:** BOM import and bulk part creation write the records and enqueue one job per part, then return immediately — the import call completes once records exist, not once enrichment finishes. Since BM25 search (§5.1) needs none of this, everything imported is fully searchable and usable right away; embeddings and enrichment catch up in the background afterward.

**Visibility, not silence:** each Part carries an `enrichment_status` (`pending`|`processing`|`done`|`failed`|`skipped`) so the UI can show what's still catching up rather than silently having incomplete data — consistent with the style guide's "the person should never have to wonder" principle for connection status.

**Failure handling:** failed jobs retry with backoff (same pattern already used for gateway calls in §5.5), up to a limit, then mark `failed` and stop rather than retrying forever — visible in the UI so it's a known gap, not a silent one. `enrich_part_vendor` jobs use the more specific error-classified backoff and per-run caching described in §5.4, rather than this generic pattern — not every vendor failure is worth retrying the same way.

**Config:** worker concurrency is configurable (e.g. `GOPARTS_MODEL_WORKERS=2`) so a large bulk import doesn't hammer a rate-limited vendor API or model provider all at once.

**Cost safety valve — concurrency limits pace, not volume.** Two workers still eventually burn through 500 calls, just slower; nothing so far stops a 500-part bulk import with AI enrichment on from actually making 500 paid API calls. `embed_part` and `enrich_part_model` may now use different providers since §5.9's `GOPARTS_ENRICH_URL` and `GOPARTS_EMBED_URL` are configured independently, but they still share one background worker pool and one combined safety cap — simpler than tracking two separate budgets, and the failure mode (an accidentally-huge bulk import) is the same regardless of which provider each job type happens to be calling: `GOPARTS_MODEL_MAX_CALLS_PER_RUN` (default `50`). A single bulk import or `go-parts enrich run --all` invocation stops enqueueing AI-model calls once it hits that number — parts beyond the cap get `enrichment_status = skipped` (an existing status value, not a new one), not silently dropped and not endlessly retried. Re-running `go-parts enrich run --all` later picks up the skipped parts and applies a fresh cap to that new run, so nothing is lost, it's just paced across more than one invocation. Setting the cap to `0` disables it explicitly, for anyone who's deliberately decided unlimited is fine for their catalog size and budget.

`enrich_part_vendor` (vendor plugin lookups) isn't covered by this cap — those calls are typically free and already governed by their own rate-limit retry/backoff, a different concern tracked separately.

**`--dry-run` reports the number before anything runs**, the same pattern already used for reindex (§5.15) — `go-parts enrich run --all --dry-run` shows how many AI-model calls the run would make and whether that exceeds the configured cap, so the number is visible before it's a bill.

### 5.11 Terminal UI (TUI)

Full parts lookup and stock management from the command line — SSH into the box, run one command, get an interactive terminal browser. No browser, no port-forwarding a web UI just to check if a part's in stock.

**Architecture: a client, not a new server surface.** The TUI is a subcommand of the same `go-parts` binary (`go-parts tui`), not a separate binary and not a new backend protocol. It talks to a running go-parts server over REST — the same API the web UI uses — so there's one source of truth and one code path for "what can happen to a part," not a TUI-specific reimplementation of business logic.

**Why REST, not RPC:** REST already exists from Phase 1, so the TUI isn't blocked on RPC formalization (Phase 4) to ship. RPC stays available as a future transport if there's ever a concrete reason to switch (lower overhead, streaming) — but for a request/response terminal browser, REST is simpler and sufficient.

**Why it can't just open Pebble directly:** Pebble, like any embedded LSM store, enforces single-process access via a directory lock. If `go-parts start` already has the data directory open, a second process can't also open it — so the TUI is deliberately always a network client of the running server, never a second process reading the store directly. That also means it works identically whether it's on the same box as the server or SSH'd in from elsewhere on the homelab network — just pointing at an endpoint either way.

**Invocation**
```
go-parts tui                      # connects to localhost, default port, from config
go-parts tui --host homelab.lan   # connects to a remote instance over the network
```
v1 needs no credential here — the no-op auth middleware (§5.8) accepts any local client. Once Phase 8's real auth lands, `go-parts tui` reads a personal API token (§5.8) from its own local client config or an env var, not from anything on the server.

**v1 TUI scope** (mirrors the web UI's core workflows, not full feature parity):
- Search/browse parts (live filter, same behavior as the web UI's search box)
- Part detail view (specs, location, quantity, vendor links)
- Stock adjust (increment/decrement)
- Low-stock list
- Image presence shown as text only ("3 images attached") — not rendered, not uploadable (§5.16)
- Everything else (builds, purchase lists, vendor rule groups) stays web-only until there's an actual reason to bring it to the terminal.

**Visual identity carries over:** the same design tokens from the style guide (copper accent, phosphor data color, dark background) map to ANSI 256-color/truecolor terminal output where supported, so the TUI reads as the same product as the web UI, not a disconnected tool. Built with Bubble Tea + Lip Gloss (Charm's Go TUI framework) — a natural fit for a dark, accent-driven, keyboard-first interface.

### 5.12 CLI Conventions (shared with go-rag / MuninnDB)

go-parts' CLI follows the same conventions already established for `go-rag bridge muninn`, rather than inventing a second style. Someone who already knows one of your tools' CLIs shouldn't have to relearn the shape of the other.

**Daemon lifecycle — top-level verbs, matching `go-rag start`/`stop`/`status`:**
```
go-parts start                 # starts REST/RPC/MCP/Website
go-parts stop                  # graceful shutdown, drains job queue, 30s max
go-parts status                # search index size, job queue depth, gateway/vendor health
go-parts status --json         # machine-readable
```

**Feature-area subcommands — `<binary> <area> <subsystem> <verb>`, matching `go-rag bridge muninn <verb>`:**
```
go-parts gateway gorag init [--non-interactive --addr ... --vault ...]
go-parts gateway gorag start          # integrated into `go-parts start` if configured
go-parts gateway gorag stop
go-parts gateway gorag status [--json]
go-parts gateway gorag sync [--dry-run]

go-parts gateway muninndb init / start / stop / status / sync   # same shape
```

**Vendor plugins (§5.4) and AI enrichment (§5.9) as top-level subsystems, same verb set:**
```
go-parts vendor status [--json]
go-parts vendor sync [--vendor lcsc] [--dry-run]

go-parts enrich init [--non-interactive]   # walks through both GOPARTS_ENRICH_URL and GOPARTS_EMBED_URL, each optional/skippable; keys read from env either way
go-parts enrich status [--json]
go-parts enrich run --part <id> | --all [--dry-run]
```

**Background job queue (§5.10):**
```
go-parts jobs status [--json]
```

**Search (§5.15):**
```
go-parts search reindex [--bm25-only] [--dry-run]
```

**TUI (§5.11):** a single top-level verb, same as `start`/`stop`/`status` — `go-parts tui [--host <addr>]`.

**Conventions carried over directly:**
- **Guided `init` wizards** — interactive prompts, capability/connectivity check against the target, confirmation before overwriting existing config, and a `--non-interactive` flag accepting every prompted value as a flag for scripted deployment (exact pattern from `go-rag bridge muninn init`)
- **Re-running `init` never silently overwrites** — prompts `Config already exists. Overwrite? [y/N]`
- **`--dry-run`** on anything that would write or mutate (`sync`, `run`, future `reset`) — reports what would happen without doing it
- **`--json`** on every `status` command for scripting/monitoring
- **Config file:** `.go-parts/config.json` — project-local dot-directory, matching `.go-rag/config.json` exactly, not `/etc` or XDG paths. Holds non-secret settings only; credentials are env-var only and never written here (§5.18)
- **Shared metrics endpoint** — gateway, vendor, and enrichment subsystems all report through go-parts' one existing metrics endpoint, no new port per subsystem (same principle as go-rag's bridge metrics landing on its existing `:7881/metrics`); concrete metric names in §5.19
- **Exit codes and messages** — `0` on success; a destination-unreachable error (gateway down, vendor API down) exits `1` with a clear, specific message, never a bare stack trace
- **`--help` on every subcommand**, listing its flags

### 5.13 Schema Versioning & Migrations

The gap: nothing in the design so far says what happens to existing data when the binary is upgraded and the Pebble record format changes — new fields, renamed keyspaces, a different `specs` convention. For a personal database meant to last years across many upgrades, this needs a plan before v1, not after the first breaking change.

**Version marker:** a single key in the `meta` keyspace (§5.1), `schema_version` (plain integer — 1, 2, 3… — not semver, since this tracks internal record format, not a public API). Separate from the go-parts binary's own release version; a bugfix release doesn't necessarily bump it.

**First run vs. needs-migrating are different cases:** an empty store with no `schema_version` key at all is a fresh install — `go-parts start` writes the current version immediately, no migration runs. A store with a `schema_version` *lower* than the binary expects needs migrating. A store with a version *higher* than the binary expects means an older binary is running against newer data (e.g. rolled back a Docker tag after data already migrated forward) — this is a hard startup failure with a clear message, never a silent downgrade-write.

**Migrations run automatically on `go-parts start`, safely:**
- Sequential, one version step at a time (v1→v2, v2→v3…), each a plain Go function reading and rewriting whatever records changed shape
- **Auto-snapshot before migrating** — reuses the existing Pebble snapshot mechanism from the backup design (§9), written to `<data_dir>/migration-backups/pre-v{N}-{timestamp}/` before anything is touched. This is the actual recovery path if a migration goes wrong — restore that snapshot — not a generic "undo migration" command, since not every data rewrite is cleanly reversible.
- **Must be non-interactive by default** — go-parts runs as a Docker container in a compose stack (§5.3); a confirmation prompt on every startup would hang the container waiting on a TTY that isn't there. The auto-snapshot is the safety net *instead of* a prompt, not in addition to one.
- Fails loudly and refuses to start on a failed migration step, rather than leaving the store in a half-migrated state

**CLI, matching §5.12's conventions:**
```
go-parts status                 # now also reports current schema_version
go-parts db migrate --dry-run   # preview what a pending migration would change, no writes
```

**Scope:** this versions go-parts' own store only. The go-rag/MuninnDB gateways (§5.5) are separate systems in separate containers with their own data — go-parts migrating its schema has no bearing on them, and vice versa.

### 5.14 Concurrency & Write Conflicts

The gap: §5.8 already establishes that go-parts handles multiple simultaneous clients (REST, RPC, MCP, TUI, and later multiple makerspace users all hit the same running process). Nothing yet says what stops two of them from racing on the same record.

**Where this actually happens:** everything goes through one process — Pebble is only ever opened by the single `go-parts start` process (§5.11 already relies on this for the TUI). So this isn't a distributed-systems problem; it's ordinary in-process concurrent request handling, and it needs two different mechanisms depending on the kind of write.

**Stock adjustments — no version needed, because the operation is commutative.** `adjust_stock(id, delta, reason)` is a delta, not a replace — two concurrent `-1` adjustments should always net to `-2` regardless of ordering. v1 serializes this with an in-process lock keyed per part ID (a striped lock pool, not one global lock), so a read-modify-write pair can't interleave with another and lose an update. No client-side retry, no conflict to handle — it just always composes correctly. (Pebble's `Merge` operator could do this at the storage layer instead of an app-level lock — worth it only if lock contention ever actually shows up, which is unlikely at homelab/makerspace scale; not needed now.)

**Full-record edits — optimistic concurrency via a `version` field.** Editing description/specs/tags/etc. (`PATCH /parts/{id}`, `upsert_part`) is a replace, not a delta, so it needs the client to prove it edited from the current state. Every Part carries a `version` (integer, incremented on every write). A write must include the version it read; if the stored version has since moved, the write is rejected rather than silently overwriting a concurrent edit — the client re-fetches and retries.

**Same mechanism across every protocol, not a REST-only trick:** `version` lives in the actual Part record, so RPC/MCP/TUI all see and pass it the same way. REST additionally exposes it the idiomatic HTTP way — `GET /parts/{id}` returns an `ETag`, `PATCH` requires `If-Match` — but underneath it's the same field, not a second mechanism.

**Background jobs (§5.10) play by the same rule.** An enrichment job re-checks the part's version before writing its results back. If it's moved — you edited the part manually while the job was running — the job merges only the fields it touched rather than blindly overwriting your edit, and logs a skip for anything that now conflicts.

**This is the same mechanism the future makerspace scenario needs (§5.8) — not a second design.** Multiple protocol surfaces today and multiple human users later are the same problem from the server's point of view: concurrent writers hitting the same process. Nothing here needs revisiting when Phase 8 arrives.

### 5.15 Search Reindexing

The gap: no equivalent to go-rag's `bridge muninn reset` ("wipe sync cursors and engram index, forces full re-sync"). Needed when the index gets corrupted, or when switching AI/embedding providers mid-life (§5.9) makes existing vectors incompatible with whatever generated them before.

**The index is fully derived, never a source of truth.** `search_index` (§5.1) is rebuildable from `parts` records at any time with zero data loss — the Part records are what matters; the index is just a fast lookup structure built from them. That makes reindexing safe in a way schema migration (§5.13) isn't: no auto-snapshot needed first, because nothing irreplaceable is being touched.

**Reuses the background job queue (§5.10) — not a separate mechanism.** A reindex is really "bulk import against your own existing data": walk every Part, re-enqueue its indexing job(s), same as a BOM import does for newly created ones. No new job-processing code path.

**CLI**
```
go-parts search reindex              # full rebuild: BM25 + vector (if enabled)
go-parts search reindex --bm25-only  # skip vector regeneration
go-parts search reindex --dry-run    # report scope (part count, estimated API calls) without running
```

**`--bm25-only` matters for cost, not just speed.** A full reindex re-triggers embedding generation for every part if vector search is on — with a paid provider, that's a real bill for a catalog of any size, tying directly into the AI-enrichment cost concern already tracked in §11. `--bm25-only` rebuilds the free tier only; the default path warns how many parts are about to be re-embedded before it proceeds.

**Triggered automatically where it matters, not left as a manual chore:**
- Changing the AI/embedding provider via `go-parts enrich init` (§5.12) offers to trigger a reindex immediately, since the old vectors came from a different model and are no longer comparable to new ones
- A schema migration (§5.13) that changes anything search-relevant can enqueue a reindex as one of its own steps

**Deliberately not an MCP tool.** Everything else in §8's MCP surface is safe for an AI agent to call without much thought. A full reindex can mean re-embedding an entire catalog against a paid API — that shouldn't be one accidental agent action away. REST and CLI only.

**Progress visibility reuses what already exists** — `enrichment_status` per part and `GET /jobs?status=pending` (§5.10) already show what's queued; reindex progress is just watching that drain, not a new status mechanism.

### 5.16 Image Handling

The gap: `image_refs[]` has sat in the data model since the first draft but never got the treatment datasheets got in §5.6 — no storage decision, no upload path, no thumbnail generation. Fixing that here, mirroring §5.6's approach where it fits and diverging where images are genuinely different from datasheets.

**Storage: always local disk, no gateway branching.** Datasheets had a go-rag-vault option because they're text-searchable documents a RAG system can meaningfully index. A product photo isn't — there's no equivalent second backend worth building. Images live under `<data_dir>/images/`, auto-created on first run, same as the datasheet default path, and are swept up by the same existing Backrest → B2 job.

**Two files per image, not one.** Every image gets an original plus a generated thumbnail (~256px), both stored locally. The web UI's dense table/detail views (§8.1) render the thumbnail, never the original, so a part list with images doesn't turn into a slow image-loading page.

**Where images come from — two different paths, two different mechanisms:**
- **User upload** (web UI only — see below) — a fast, local, synchronous operation (resize + save), so it doesn't need the background job queue the way enrichment does. Upload, thumbnail, done, in one request.
- **Vendor-fetched** — when a vendor plugin (§5.4) matches an MPN and the vendor's data includes a product image URL, downloading and thumbnailing it is folded into the existing `enrich_part_vendor` background job (§5.10) rather than adding a fifth job type — it's already fetching everything else about that part in the same job. The image is downloaded and stored locally rather than hot-linked, so it survives even if the vendor's URL later goes stale.

**Data model — richer than a flat path list**, with the same provenance pattern already used for `enrichment_source[]`:
```
image_refs[]: [
  { id, original_path, thumbnail_path, source ("upload" | "vendor:lcsc" | ...), created_at }
]
```

**REST**
```
POST   /parts/{id}/images              (multipart upload)
GET    /parts/{id}/images/{image_id}
GET    /parts/{id}/images/{image_id}/thumb
DELETE /parts/{id}/images/{image_id}
```

**Web UI only for upload — not TUI.** Binary file upload doesn't fit a terminal interface well, and rendering images in a terminal is a whole separate can of worms (sixel/kitty graphics protocols, not every terminal supports them). The TUI (§5.11) shows image *presence* as text — "3 images attached" — with a note to view them in the web UI, rather than attempting to render them. Consistent with §5.11's existing "not full feature parity" scope.

**Deliberately not an MCP tool**, for the same reason as §5.15's reindex — binary upload doesn't fit a text-based tool call well, and there's no real benefit to an AI agent triggering it over REST/web anyway.

### 5.17 Via Codes & Location Labels

The gap: bin-label printing (§7.1) and `/parts/barcode/{code}` only ever covered Parts — Location has no label/code field at all, despite "print a label, stick it on the bin, scan to find" being fundamentally about locations, not parts. Untangling this surfaces a distinction the doc was conflating: two different things were both being called "barcode."

**Naming: called a Via, not "ID Anything."** PartsBox's equivalent concept is a trademarked product name and doesn't belong on this project. But the underlying idea — a small connection point that lets you jump straight to something — already has a name in this exact codebase: the style guide's signature element is copper traces meeting at small circular **via** dots (§style guide §1, §6). A via, literally, is a plated hole connecting one layer of a PCB to another. Reusing that word for "the code that connects a physical object to its record" isn't a stretch — it's the same idea in both places, so it gets the same name instead of a second one.

**Two different codes, two different purposes:**
- **Vendor barcodes** (existing `/parts/barcode/{code}`) — decode a label a *vendor* already printed (an LCSC reel, a Mouser bag) to look up the matching Part. External format, vendor-specific, already covered.
- **Via codes** (new) — a compact code *go-parts generates itself*, for anything worth printing a label for and scanning later. This is what was actually missing.

**Adopted, generalized (PartsBox's "ID Anything" precedent, renamed):** PartsBox assigns every object — part, lot, location, build, order, project — a unique scannable code. go-parts adopts the same underlying mechanism now, under its own name, but only issues Via codes for the two entity types that exist today: Part and Location. Extending it to Build/Order/Project when those arrive in Phase 6 (§6.3) means reusing the same generator, not redesigning anything.

**Format:** short, typeable-if-a-scanner-ever-fails, type-prefixed — `L-7B3D1E` for a location, `P-4F2A9C` for a part. Not a raw UUID; those are miserable to read off a small label or key in by hand.

**Data model:** `via_code` (unique, indexed) added to both Part and Location.

**Generic resolver, not a per-type endpoint:** `GET /via/{code}` reads the prefix and routes to the right entity. A phone scanning a go-parts-printed QR code doesn't need to know in advance whether it's pointing at a bin or a part — one endpoint handles both today, and Build/Order/Project later, with no new endpoint per type.

**Why Parts need this too, not just Locations:** a local part with no MPN (§6.1's `part_type = local`) — a custom PCB, a 3D-printed bracket — has no vendor barcode to fall back on. Its Via code is the only scannable identity it will ever have.

**Label printing:**
```
POST /locations/{id}/label   # renders a printable QR label (encodes the Via code)
POST /parts/{id}/label       # same, for parts with no vendor barcode
```
go-parts renders the label; actually printing it is a client/OS concern (send to whatever printer's configured), not something go-parts manages drivers for.

**Scan-to-find — the actual point of a bin label:** scanning a Location's Via resolves via `GET /via/{code}` to that location's current contents. That's what "print a label, scan to find" was always supposed to mean, and until now there was no mechanism behind it.

### 5.18 Secrets & Network Exposure

**Revised: secrets are env-var only, never written to `config.json` — this deliberately diverges from go-rag's own literal pattern.** go-rag's bridge stores its MuninnDB token as a plain field in `.go-rag/config.json` (`"token": ""`), and that was the initial model for go-parts too. Plain text at rest is a real, avoidable risk, though, and worth fixing here even though it means not mirroring go-rag on this one specific point — "match go-rag's conventions" was always in service of low friction, not a reason to keep an avoidable weakness once it's flagged.

**The split:** anything that identifies *what* to connect to (endpoint, vault name, enable/disable, priority order) is ordinary config and lives in `.go-parts/config.json` — safe to read, edit, and back up. Anything that *authenticates* (vendor API keys — §5.4, the AI model provider key — §5.9, gateway tokens — §5.5) is env-var only and is never written to disk by go-parts itself:
```
GOPARTS_LCSC_API_KEY, GOPARTS_MOUSER_API_KEY, GOPARTS_DIGIKEY_API_KEY   (§5.4)
GOPARTS_ENRICH_URL, GOPARTS_EMBED_URL, GOPARTS_ANTHROPIC_KEY, GOPARTS_OPENAI_KEY   (§5.9, §5.1 — MuninnDB-style URL-scheme config)
GORAG_TOKEN, MUNINNDB_TOKEN                      (§5.5, only if that target requires one)
```

**Init wizards adapt accordingly.** `go-parts vendor init` / `go-parts enrich init` / `go-parts gateway <target> init` (§5.12) still walk through setup interactively and still validate connectivity before finishing — but for the secret field specifically, the wizard checks whether the required env var is already set and tests it, rather than prompting to type a value that then gets written to `config.json`. If it's missing, the wizard names the exact variable to export and stops there; re-running the wizard once it's set continues from where it left off.

**How the env var actually gets supplied is a deployment-layer concern, not go-parts' problem to solve — and Docker already has a good answer.** For the primary Docker Compose deployment (§5.3), Compose's native `secrets:` mechanism (files mounted at `/run/secrets/`, not visible via `docker inspect` or `/proc/<pid>/environ` the way plain environment variables are) is the preferred route over a plain `.env` file. A gitignored, `0600` `.env` file is an acceptable simpler fallback, just a weaker one. For bare-metal/systemd (§5.3's fallback), `EnvironmentFile=` pointing at a `0600` file outside anything web-served or backed up in plaintext is the equivalent.

**Consequence, stated plainly: after a restore, secrets have to be re-supplied.** Since nothing secret is ever in `.go-parts/config.json`, nothing secret is in the Backrest → B2 backup either — not because it's being deliberately excluded, but because there's nothing there to exclude. A restore brings back every part, location, and setting exactly as it was; it does not bring back API keys, because those never lived in the backed-up file in the first place. That's a real, worthwhile trade for not having credentials sitting in plaintext on disk (or in an off-site backup) at all.

**What's still worth doing on top of this:**
- `.go-parts/config.json` still gets `0600` permissions regardless — it holds no secrets now, but there's no reason to make it more readable than it needs to be
- Setup docs should say plainly: `.env` (or wherever the env vars are sourced from) belongs in `.gitignore` if the homelab stack lives in a git repo

**Network exposure: the real lever is Docker topology, not a bind flag.** go-parts runs as a container (§5.3), and inside a container the process has to bind `0.0.0.0` for Docker's own port mapping to work at all — a "bind to localhost" default wouldn't even be reachable from other containers. So "secure by default" means something different here than for a bare-metal daemon:
- **Docker Compose (primary deployment):** go-rag and MuninnDB reach go-parts over the internal Docker network by service name, matching the service-name-routing pattern already used elsewhere in the homelab — with **no host port published by default**. Publishing a port, or fronting it with Nginx Proxy Manager (the existing pattern for Pocket-ID/TinyAuth), is an explicit choice made in the compose file, not the out-of-the-box behavior.
- **Bare-metal/systemd (§5.3's fallback):** here a bind address is a meaningful lever, and it defaults to `127.0.0.1` — mirroring the same loopback-by-default pattern go-rag itself uses for its own bridge target address. Reaching it from elsewhere on the network requires explicitly setting `--bind 0.0.0.0` or a specific interface.

**Worth saying plainly, since v1 ships with zero auth (§5.8):** exposing go-parts beyond the Docker-internal network or localhost — before Phase 8's real auth lands — means exposing an unauthenticated REST/RPC/MCP API to whatever can reach it. The topology above is the safe default; publishing a port or fronting it with NPM is an informed, deliberate choice, not something the defaults nudge toward.

### 5.19 Metrics & Dashboard Stats

The gap: a metrics endpoint has been mentioned since §5.12 but never had a concrete list, unlike go-rag's bridge (`bridge_sync_lag_seconds`, etc.). Worth splitting into two genuinely different things rather than one — they have different audiences and go through different surfaces.

**Operational metrics (Prometheus, ops-facing) — named now, following go-rag's bridge style exactly:**
```
goparts_jobs_pending / goparts_jobs_processing                    (gauge, by job_type — §5.10)
goparts_jobs_failed_total                                          (counter, by job_type)
goparts_vendor_healthy                                             (gauge, by vendor — §5.4)
goparts_vendor_calls_total / goparts_vendor_errors_total           (counter, by vendor + error_type: rate_limited|auth_failed|not_found|connection)
goparts_ai_calls_total                                              (counter, by purpose: enrich|embed — §5.9/§5.1)
goparts_ai_calls_capped_total                                       (counter — times GOPARTS_MODEL_MAX_CALLS_PER_RUN stopped a run)
goparts_gateway_connection_healthy                                  (gauge, by target: gorag|muninndb — §5.5)
goparts_search_query_duration_seconds                               (histogram — §5.1)
goparts_write_conflicts_total                                       (counter — §5.14 optimistic-concurrency rejections)
```
These land on the same shared metrics endpoint already established in §5.12 — no new port, same principle as go-rag's bridge metrics reusing its existing `:7881/metrics`.

**Dashboard stats (product-facing, PartsBox-style) — a different thing entirely.** PartsBox's Reports section shows live counts and inventory valuation; the toolbar in this project's own UI mockup already displays a running part count. Formalizing that into a real endpoint rather than leaving it as mockup-only text:
```
GET /stats
{
  "parts_total": 3115,
  "parts_low_stock": 12,
  "parts_out_of_stock": 3,
  "locations_total": 48,
  "projects_total": 9,
  "enrichment_pending": 4,
  "enrichment_failed": 1
}
```
Cheap to compute (record counts and a few filtered counts, no valuation math), so it's v1 scope, not deferred — see §7.1. **Inventory valuation is different and stays in Phase 6** (§7.3): it requires aggregating pricing across `supplier_links` and (once it exists) `Lot` records, which is real computation, not a cheap count — no reason to hold basic counts hostage to a feature that needs more machinery.

**Surfaces:** `GET /stats` (REST), `get_inventory_stats()` (MCP — a natural fit for "how many parts do I have"-style questions), a dashboard view in the web UI fulfilling §5.7's "Reports" nav item, and a compact summary line in the TUI status view (§5.11).

### 5.20 Testing & CI (shared with go-rag / MuninnDB)

Read MuninnDB's actual `CLAUDE.md` directly rather than assuming from generic Claude Code documentation — it's a real project constitution, not boilerplate, and the mechanism is meaningfully different from what a first pass at this section assumed.

**The review agent is a repo-local subagent, not a GitHub Actions auto-trigger.** MuninnDB's `.claude/agents/code-reviewer.md` is a custom subagent definition, invoked locally via `/code-review` and used proactively by the developer *before* opening a PR — not a mandatory workflow that runs automatically on every push. That's the right shape for a solo repo: the developer chooses to run it, the same way they'd choose to re-read their own diff, rather than waiting on a CI gate. go-parts adopts the same mechanism: `.claude/agents/code-reviewer.md`, invoked via `/code-review`, checking a change against the principles in go-parts' own `CLAUDE.md` (below) — not generic style feedback.

**`CLAUDE.md` as a constitution, not a style guide — structure adopted directly from MuninnDB's:**
```
1. What go-parts is — one-line promise + architecture map (table of §5.x subsystems → what each owns)
2. Core principles — distilled from decisions already made in this PRD, not aspirational:
   - Low friction is load-bearing (§2) — every feature is measured against "still works in
     under 5 minutes with zero config"
   - Degrade loudly-but-gracefully, never silently-wrong — BM25 works with zero AI config (§5.1);
     gateways fail to standalone operation (§5.5); a stale optimistic-concurrency write is
     rejected, never silently overwritten (§5.14)
   - Explicit config is never silently substituted — a schema version mismatch is a hard
     startup failure, never a silent downgrade-write (§5.13)
   - Extend proven in-tree mechanisms over inventing new architecture — reindex reuses the
     background job queue rather than a new one (§5.15); AI enrichment reuses the vendor-plugin
     loading pattern (§5.9); the Via resolver is one generic endpoint, not one per entity (§5.17)
   - Secrets never touch a tracked or backed-up file — env-var only, always (§5.18)
3. How we work — verify the branch before asserting what code does; build and test the actual
   change (`go build ./... && go vet ./... && gofmt -l .` + relevant `go test ./... -race`);
   -race is mandatory (not optional) for anything touching §5.14, §5.10, or §5.13 — exactly
   where races hide; RED-sanity-check bug fixes — a test for a fixed bug must be shown to fail
   without the fix, a test that passes both ways proves nothing; keep the full CI gate fast
4. Findings that outlive the session — see below
5. The code-review agent — .claude/agents/code-reviewer.md, invoked via /code-review
6. Attribution — whether "Generated with Claude" gets added to commits/PRs (MuninnDB omits it;
   worth deciding the same way here)
```

**A dogfooding opportunity MuninnDB's own file made obvious:** MuninnDB's `CLAUDE.md` describes its own team using MuninnDB itself to persist durable findings across Claude Code sessions — a measured number, a decision and why it beat the alternative, a trap that looks safe — so nothing gets lost when a session ends. go-parts already has a MuninnDB gateway (§5.5). Development on go-parts itself is a natural place to use that same pattern: durable findings from building go-parts get written to MuninnDB the way MuninnDB's own maintainer already does it for MuninnDB.

**CI (build/vet/test) still runs in GitHub Actions, confirmed from MuninnDB's own CI badge:**
```
.github/workflows/ci.yml
  build → go vet / gofmt check → go test ./... -race
```
This part of the original design holds — it's the review-agent mechanism that needed correcting, not the CI pipeline itself.

**Priority test coverage** (unchanged from the original review pass, still the right list):
- **§5.14 concurrency** — striped locks hold up under concurrent `adjust_stock` calls; optimistic-concurrency correctly rejects a write against a stale `version`
- **§5.13 migrations** — a migration step applied to representative fixture data produces the expected output, and the pre-migration snapshot is actually taken before anything is touched
- **§5.4 vendor plugin backoff** — mocked HTTP 429/401/404 responses verified against the classification table
- **§5.10 background job queue** — a capped run leaves the correct parts `skipped`, and a resumed run picks them back up

---

## 6. Data Model

### 6.1 v1 Data Model

**Part**
- `id`, `mpn`, `manufacturer`, `category`, `subcategory`
- `part_type` (`linked` | `local`) — linked = has an MPN and gets vendor-enriched; local = generic/no-name/custom, name-only (adopted from PartsBox's part-type model; `meta` and `sub_assembly` types arrive in §6.3)
- `via_code` (unique, indexed — go-parts' own scannable identity, distinct from any vendor barcode — see §5.17)
- `description`, `specs` (flexible key/value — resistance, tolerance, voltage, etc.)
- `footprint/package` (0805, SOT-23, THT, ...)
- `unit_of_measure` (optional — pieces by default; length/area/mass/volume/time when set) + `package_quantity` (how much of the part, in its unit, one vendor package contains — adopted from PartsBox, matters the moment a part is wire, paste, or anything sold by length/weight rather than piece count)
- `datasheet_storage` (`local` | `go-rag-vault`), `datasheet_ref` (local path or go-rag doc id)
- `image_refs[]` (original + thumbnail per image, with source provenance — see §5.16)
- `quantity_on_hand`, `reorder_threshold` (both interpreted in the part's unit)
- `default_location_id`, `default_location_mandatory` (bool) — adopted from PartsBox; cheap to build now, keeps inventory from drifting as it grows
- `supplier_links[]` (Mouser/DigiKey/LCSC part #, price, last_seen_price)
- `kicad_symbol_ref`, `kicad_footprint_ref`
- `tags[]`, `custom_fields` (flexible key/value, indexed for search/filter — adopted from PartsBox)
- `enrichment_source[]` (e.g. `["vendor:lcsc", "model:claude-sonnet-5"]` — provenance for §5.4/§5.9 enrichment)
- `enrichment_status` (`pending`|`processing`|`done`|`failed`|`skipped` — background job state from §5.10)
- `created_by`, `updated_by` (nullable, defaults to `local` in v1 — see §5.8)
- `created_at`, `updated_at`
- `version` (integer, incremented on every write — optimistic concurrency for full-record edits, see §5.14; stock adjustments don't use this)

**Location** — `id`, `label` ("Bin A3", "Drawer 12"), `via_code` (unique, indexed — see §5.17), `parent_location_id` (nested storage), `creation_method` (`single`|`row`|`grid`|`3d_grid`, recorded for reference), `single_part_only` (bool), `notes`, `created_by` (nullable)

**Project / BOM** — `id`, `name`, `part_refs[]` w/ quantities, linked KiCAD project path (optional), `created_by` (nullable)

**Supplier** — `id`, `name`, `base_url`, part-number mapping conventions

**User** (schema only, unused in v1) — `id`, `display_name`, `email`, `external_id` (OIDC subject, for the future Pocket-ID link), `role` (`admin`|`member`). v1 never creates a row here; every attribution field just stores the literal string `local`.

### 6.2 Handling Varying and Unknown Component Attributes

A resistor has resistance/tolerance/power. A capacitor has capacitance/voltage/dielectric. A crystal has frequency/load capacitance/stability. A part type nobody's thought of yet has whatever it has. This can't be handled with a fixed schema per category — so it isn't one.

**`specs` and `custom_fields` are both plain, unstructured key-value maps — the same mechanism, not a rigid schema.** There is no predefined attribute list per category, no "Resistor" schema to define or migrate when a new one is needed. A Part's `specs` map just holds whatever keys are relevant to that specific component: a resistor gets `{"resistance": "10k", "tolerance": "1%", "power": "1/8W"}`, a capacitor gets `{"capacitance": "100nF", "voltage": "50V", "dielectric": "X7R"}`, and something entirely novel just gets whatever keys make sense — nothing in the data model changes to support it.

**The distinction between the two maps is provenance, not structure:**
- `specs` — populated by a vendor plugin (§5.4) or the AI enrichment model (§5.9), i.e. "what the part actually is"
- `custom_fields` — user-defined additions, same flexible shape, for anything you want to track that isn't a component spec (e.g. "shelf-life", "reel-count") — matches PartsBox's same split

**How search and filtering work over an unstructured map:**
- **Full-text search (BM25, §5.1)** indexes every spec value as searchable text regardless of key name — it already works over any spec, known or brand new, with zero configuration. Nothing needs to be declared for `"10kΩ"` to be findable.
- **Structured filtering** builds its list of filterable keys dynamically from what actually exists across your current parts, not from a fixed dropdown — the same approach PartsBox uses for custom-field filtering. A key only shows up as filterable once at least one part actually uses it.
- **Unit-prefix numeric filtering** (§7.2 — `10k` instead of `10000`) is a parsing step applied at filter time to whatever key you're filtering on, not a declared type per key. Any spec value that looks like a number with a unit prefix can be range-filtered; nothing about the schema needs to know in advance that "resistance" is numeric and "dielectric" isn't.

**New attributes need zero migration.** A vendor plugin encountering a component type it's never seen before, the AI enrichment model extracting an unfamiliar field from a datasheet, or you typing a new custom field by hand — all three just add a new key to the map. `category`/`subcategory` are plain strings too, not an enum, so a genuinely new component category is also just a new value, not a schema change.

**Deliberately not built for v1:** per-key type/unit declarations (formally marking "resistance" as a numeric field with unit "Ω") for stronger validation or nicer auto-generated form inputs. Worth revisiting if the freeform approach ever proves too loose in practice, but it's not needed for correctness and would be exactly the kind of upfront schema design this section is avoiding.

### 6.3 Extended Data Model (PartsBox-inspired, later phases)

These arrive in Phase 6+ (§10) — deliberately not v1 scope, so the "start using it straight away" promise stays intact. Documented now so v1's schema doesn't have to be reworked to fit them later.

**Substitutes (three-tier model, adopted from PartsBox)**
- **Meta-part** — a Part with `part_type = meta` and `member_part_ids[]`. Stock is the aggregate of all members' stock; used in BOMs like any other part. Good for the same component in different packaging.
- **Part substitute** — `substitute_part_ids[]` on a Part, global but with no stock grouping — each part's stock stays independent.
- **BOM substitute** — a substitute list scoped to one BOM entry only, not the part globally.

**Lot** (optional, off by default) — `id`, `part_id`, `location_id`, `quantity`, `unit_cost`, `source`/`comment`, `created_at`. With lot control off (the low-friction default), stock is tracked per part+location without batch-level granularity. Turning it on is an explicit per-install choice, never automatic.

**Build** — `id`, `project_id`, `quantity`, `stage` (single, or a stage number for multi-stage), `status` (`in_progress`|`completed`), `stock_consumed[]` (part_id, lot_id?, location_id, quantity), `created_at`

**PurchaseList** — `id`, `name`, `source_project_ids[]` with build quantities, `entries[]` (aggregated across the source projects)

**Order** — `id`, `vendor`, `status` (`open`|`ordered`|`received`), `lines[]` (part_id, quantity_packages, unit_price), `expected_delivery_date`

---

## 7. Core Features

### 7.1 v1

- CRUD for parts, locations, projects
- Full-text + semantic search (name, MPN, description, specs, datasheet text)
- Stock adjust (increment/decrement, with history log)
- Low-stock flagging (below `reorder_threshold`)
- BOM import (CSV / KiCAD BOM export) → match or create parts, decrement stock
- BOM export for a project
- Barcode/QR: scan a vendor's own barcode to look up a part, or scan a go-parts-generated **Via** to find a location's contents or a local part with no vendor barcode — two distinct code types, see §5.17
- Datasheet ingestion: attach PDF → local disk by default, or go-rag vault when that gateway is enabled (see §5.6); text extracted for search either way
- Unit-of-measure support (pieces by default; length/area/mass/volume/time when set) with package quantity for ordering
- Custom fields — flexible, indexed, filterable, per part
- Default storage location per part, optionally mandatory
- **Location creation — start basic, expand as needed (matches PartsBox's four methods exactly):**
  - **Single** — one location, one name (e.g. `junk-box`). Zero planning required — this is the day-one default, fits "start using it straight away."
  - **Row (1D)** — a linear sequence from a prefix + range: prefix `box`, range `1`–`5` → `box1`, `box2`, `box3`, `box4`, `box5`
  - **Grid (2D)** — row × column labels: prefix `shelf`, rows `A,B`, columns `1,2` → `shelf-A1`, `shelf-A2`, `shelf-B1`, `shelf-B2`
  - **3D Grid (3D)** — level × row × column labels: prefix `rack`, levels `1,2`, rows `A,B`, columns `1,2` → `rack-1-A1`, `rack-1-A2`, `rack-1-B1`, `rack-1-B2`, `rack-2-A1`, ...
  - All four just create ordinary `Location` rows — `creation_method` is reference metadata, not a structural constraint, so anything created in bulk can be renamed or reorganized individually afterward, same as PartsBox
  - Progressive by design: start with one `Single` junk-box location, add `Row`/`Grid`/`3D Grid` batches later as the collection grows — no migration needed for what's already there
- Dashboard stats — part/location/project counts, low-stock and out-of-stock counts, enrichment queue status, via `GET /stats` (§5.19); cheap to compute, no reason to wait for Phase 6

### 7.2 Table Behavior (adopted from PartsBox, v1)

- In-table search — filters the current table's rows as you type (already in the mockup)
- Sortable columns (already in the mockup)
- Filtering with unit-prefix numeric entry — type `10k` instead of `10000` for a 10kΩ resistor, matching PartsBox's convention (`k`, `M`, `m`, `u`/`μ`, `n`, `p`, etc.)
- Bulk operations on a selection — tag, move to location, delete
- CSV export of the current filtered/sorted view

### 7.3 Extended Features (PartsBox-inspired, later phases — see §10)

- Substitutes: meta-parts, part substitutes, BOM substitutes (§6.3)
- Builds: single-stage first; multi-stage, kitting, and per-device serial-number tracking as a stretch goal
- Purchase lists: shopping-cart-style aggregation across multiple projects' BOMs, de-duplicated
- Orders: Open → Ordered → Received lifecycle, receiving parts into stock
- Vendor rule groups: named, reorderable fallback chains for offer selection (extends the simple priority-order registry in §5.4 into something closer to PartsBox's rule groups)
- Reports: inventory valuation, updating live — basic counts (parts, locations, low-stock) are already v1 via `GET /stats` (§5.19); valuation needs `supplier_links`/`Lot` price aggregation, real computation this phase adds

---

## 8. Interfaces (detail)

**REST (representative)**
```
GET    /parts?q=...
GET    /parts/{id}                 (returns ETag header — §5.14)
POST   /parts
PATCH  /parts/{id}                 (requires If-Match: <etag> — rejects on version conflict, §5.14)
DELETE /parts/{id}
POST   /parts/{id}/stock           (delta-based, no version required — §5.14)
GET    /locations
POST   /locations                  (single: {label, parent_location_id?, single_part_only?})
POST   /locations/bulk             (row | grid | 3d_grid — see §7.1 for the four methods)
POST   /bom/import                 (returns immediately; enrichment/embedding run in background — §5.10)
GET    /projects/{id}/bom/export
GET    /parts/barcode/{code}       (decodes an external vendor barcode — LCSC/Mouser/etc. — §5.17)
GET    /via/{code}                 (generic resolver for go-parts' own Via codes — Part or Location — §5.17)
POST   /locations/{id}/label       (renders a printable QR label)
POST   /parts/{id}/label           (same, for parts with no vendor barcode)
POST   /parts/{id}/enrich          (?vendor= optional, else registry priority order)
POST   /parts/{id}/enrich/model    (runs the configurable AI model stage — §5.9)
GET    /jobs?status=pending        (background queue status — §5.10)
GET    /stats                      (dashboard counts — §5.19)
POST   /search/reindex             (?bm25_only=true optional — §5.15; REST/CLI only, not an MCP tool)
```

**REST — extended (Phase 6+, PartsBox-inspired)**
```
GET    /parts/{id}/substitutes
POST   /parts/{id}/substitutes
POST   /meta-parts
POST   /builds
GET    /projects/{id}/builds
POST   /purchase-lists
GET    /purchase-lists/{id}
POST   /orders
PATCH  /orders/{id}/status         (open → ordered → received)
```

**MCP Tools (representative)**
- `search_parts(query, filters)`
- `get_part(id | mpn)`
- `upsert_part(...)` — full-record edits require the current `version` (§5.14); rejected on conflict rather than overwriting
- `adjust_stock(id, delta, reason)` — delta-based, no version needed (§5.14)
- `find_substitutes(id)` — same category/specs within tolerance
- `list_low_stock()`
- `enrich_part_from_vendor(mpn, vendor?)` — pull description/pricing/stock/datasheet via a registered vendor plugin
- `enrich_part_with_model(id)` — run the optional configurable AI model stage (§5.9) to structure specs/category/tags/summary from raw data or a datasheet
- `list_pending_enrichment()` — check the background job queue (§5.10), useful right after a bulk import
- `get_inventory_stats()` — part/location/project counts, low-stock summary (§5.19), a natural fit for "how many parts do I have"-style questions

**Website**
- Search/browse, part detail with datasheet preview, bin label printing, low-stock dashboard
- Same architectural approach as go-rag — embedded static UI served from the same binary, no separate frontend build/deploy — but a **deliberately different visual identity**: hacker / technology / hi-tech, not go-rag's look

**8.1 Style Guide (hacker/hi-tech)**
- **Palette:** dark-first, near-black background, single accent color for primary actions/links (green or cyan terminal-style), amber/red reserved strictly for warnings (low stock, out-of-stock) — not decorative
- **Typography:** monospace throughout (e.g. JetBrains Mono, IBM Plex Mono, Fira Code) for MPNs, specs, quantities, and code-like data — these are technical values, not prose, and should read like it
- **Layout:** dense data grids and tables over airy whitespace, sharp corners (no rounded SaaS-style cards), thin 1px borders with a subtle glow on focus/hover rather than drop shadows
- **Motifs:** subtle circuit-trace line patterns as dividers or background texture, grid overlays, blinking-cursor accent on the search input — used sparingly, not gimmicky
- **Iconography:** technical/schematic-style line icons (resistor, capacitor, IC symbols) rather than rounded flat icons
- **Interaction:** keyboard-first — `/` to focus search, command-palette-style quick actions — leaning into the "tool for someone who lives in a terminal" feel rather than a consumer app
- Full design-token detail (exact palette, spacing scale, component states) gets worked out at build time against this direction, not frozen here

---

## 9. Non-Functional Requirements

- Cold start < 5 min from binary/compose to usable
- Sub-100ms search over tens of thousands of parts (single node)
- Backup via Pebble snapshot / flat-file export — compatible with existing Backrest → B2 pattern
- Schema migrations are automatic, non-interactive-safe (no TTY prompts blocking container startup), and always preceded by a local snapshot — see §5.13
- Tests run with the race detector enabled (`-race`) on concurrency/migration/queue code; CI builds and tests every push; a repo-local review subagent checked before every PR — see §5.20
- No mandatory external dependencies for core operation

---

## 10. Phased Rollout

- **Phase 1** — Pebble store, part CRUD, BM25 search, REST API, minimal web UI, `go-parts start`/`stop`/`status` daemon lifecycle (§5.12 conventions apply from here on), `schema_version` marker + migration runner (§5.13) — must exist before any real data does
- **Phase 2** — MCP server, barcode/label support, low-stock alerts, vendor plugin interface + LCSC/Mouser/DigiKey plugins (compiled-in), background job queue (§5.10) powering periodic vendor refresh, TUI (§5.11) covering search/browse/stock-adjust/low-stock over REST, image upload + vendor-fetched thumbnails (§5.16)
- **Phase 3** — Vector/semantic search, datasheet text extraction, BOM import/export, KiCAD integration, optional configurable AI enrichment model (§5.9, off by default) — embedding and enrichment jobs run through the Phase 2 background queue so bulk imports stay fast
- **Phase 4** — RPC layer formalized as wire-level protocol (CLI + future KiCAD plugin consumer), project/BOM linking, substitutes engine
- **Phase 5** — go-rag gateway (embedded or remote indexing), MuninnDB gateway (event push + recall query)
- **Phase 6** — PartsBox-inspired depth: substitutes model (meta-parts, part substitutes, BOM substitutes), single-stage builds, purchase lists + orders (Open/Ordered/Received), vendor rule groups, live low-stock/valuation reports
- **Phase 7 (stretch)** — optional lot control toggle, multi-stage builds + kitting (pick list / build worksheet), per-device serial-number tracking
- **Phase 8 (future, makerspace trigger)** — multi-user: real auth via Pocket-ID/TinyAuth SSO (replacing the no-op middleware from §5.8), admin/member permission tiers, live web UI updates (WebSocket/SSE)

---

## 11. Open Questions

1. **KiCAD plugin protocol** — when the plugin is actually built, does it end up using RPC or REST? KiCAD's Python-based plugin environment can consume either comfortably, so this is a build-time choice rather than an architectural one.
