# go-parts

> A single-binary, embedded-storage **electronics parts database** — one Go binary
> over a Pebble KV store, queryable via REST / RPC / MCP / Web / TUI, built to slot
> into AI-assisted workflows.

Same low-friction philosophy as [go-rag](https://github.com/madeinoz67/go-rag) and
[MuninnDB](https://github.com/scrypster/muninndb): one binary, no mandatory external
services, multiple protocol surfaces over a shared embedded store. Ask "do I have a
10k 0805 resistor?" — from the terminal, the browser, or an AI agent — and get a real
answer.

[![CI](https://github.com/madeinoz67/go-parts/actions/workflows/ci.yml/badge.svg)](https://github.com/madeinoz67/go-parts/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

## Status

**Scaffolded — pre-v1.** Project structure, build toolchain, CI, and constitution are
in place. The Phase 1 daemon (Pebble store, part CRUD, BM25 search, REST API, minimal
web UI, `start`/`stop`/`status`) is the next implementation milestone.

Full design: [`docs/internals/go-parts-prd.md`](docs/internals/go-parts-prd.md).

## What it will be

- **Single binary**, embedded Pebble KV — no Postgres/MySQL/external DB
- **Search** over parts (MPN, description, specs, datasheet text): BM25 default-on
  (zero config, zero cost), optional vector/semantic tier
- **MCP-native** — parts search/CRUD as tools for AI agents (Claude Code/Desktop)
- **REST API** + **embedded web UI** + **TUI** (`go-parts tui`) — five surfaces, one core
- **Physical location/bin tracking** built for a workshop (drawer → bin → shelf → compartment)
- **Vendor plugins** (LCSC/Mouser/DigiKey) + optional AI enrichment — compiled-in, not hardcoded
- **BOM import/export** for KiCAD projects
- **Optional gateways** to go-rag (search) and MuninnDB (memory) — additive, never required

## Build

Requires Go 1.26+.

```bash
make build      # → bin/go-parts
./bin/go-parts --version
make test       # race detector + coverage
make lint       # golangci-lint
```

## Phased rollout

| Phase | Scope |
|-------|-------|
| **1** | Pebble store, part CRUD, BM25 search, REST, minimal web UI, daemon lifecycle, schema migration |
| **2** | MCP, barcode/Via labels, low-stock, vendor plugins + background queue, TUI, images |
| **3** | Vector search, datasheet text extraction, BOM import/export, KiCAD, optional AI enrichment |
| **4** | RPC wire protocol, project/BOM linking, substitutes |
| **5** | go-rag + MuninnDB gateways |
| **6** | PartsBox-inspired depth: builds, purchase lists, orders, valuation reports |
| **8** | (future) multi-user via homelab SSO |

See PRD §10 for the full plan.

## License

[MIT](LICENSE) — © Stephen Eaton
