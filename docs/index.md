# go-parts

A single-binary, embedded-storage **electronics parts database** — one Go binary
over a Pebble KV store, queryable via REST / RPC / MCP / Web / TUI.

Ask "do I have a 10k 0805 resistor?" — from the terminal, the browser, or an AI
agent — and get a real answer.

## Why

Electronics component inventory is scattered across bins, drawers, browser
bookmarks, and memory. There's no single source of truth for *do I have this
part*, *where is it physically*, *what did I use it in*, or *when do I reorder*.

go-parts is that source of truth — and it's built to plug into the same knowledge
ecosystem as [go-rag](https://github.com/madeinoz67/go-rag) and
[MuninnDB](https://github.com/scrypster/muninndb) rather than sit in a silo.

## Status

Scaffolded, pre-v1. See the full design: [PRD v3.7](internals/go-parts-prd.md).
