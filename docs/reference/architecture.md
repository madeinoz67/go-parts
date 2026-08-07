# Architecture

go-parts is one Go binary over an embedded **Pebble** KV store, with five interface layers
(RPC, REST, MCP, Website, TUI) over a single core service.

## Subsystem map

| Subsystem | PRD | Owns |
|---|---|---|
| Store | §5.1 | Pebble keyspaces; BM25 search default-on, vector search optional/additive |
| Interfaces | §5.2 | RPC (wire) + REST + MCP + Website + TUI over one core |
| Vendor plugins | §5.4 | Compiled-in `VendorPlugin` registry; per-failure retry classification |
| Gateways | §5.5 | Optional go-rag (search) + MuninnDB (memory); remote mode, additive |
| Background jobs | §5.10 | In-process Pebble-backed queue; bulk import returns before enrichment |
| Schema migration | §5.13 | `schema_version` marker; auto, non-interactive, snapshot-first |
| Concurrency | §5.14 | Commutative stock deltas (striped locks) + optimistic `version` on edits |
| Secrets | §5.18 | Env-var only, never in config files or backups |

## Principles

- **Low friction is load-bearing** — works in under 5 minutes, zero config, BM25-only.
- **Degrade loudly-but-gracefully, never silently-wrong.**
- **Explicit config is never silently substituted.**
- **Extend proven in-tree mechanisms over inventing new architecture.**
- **Secrets never touch a tracked or backed-up file.**

The authoritative design is the [PRD](../internals/go-parts-prd.md); the section references
above point into it.
