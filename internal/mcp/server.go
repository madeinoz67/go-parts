// Package mcp exposes go-parts' parts operations as Model Context Protocol
// tools (PRD §5.2 — a 4th queryable surface over the same composed stores as
// REST and the UI, in-process: the daemon mounts POST /mcp beside /ui/ and
// the REST root on one mux). Hand-rolled JSON-RPC 2.0 mirroring go-rag's
// twice-proven internal/mcp shape — no SDK dependency (spec anti-goal).
//
// Error model (spec): domain failures (not-found, version conflict, identity
// uniqueness, bad location) are tool RESULTS with isError:true carrying the
// engine sentinel's message verbatim — the same strings REST surfaces. Only
// protocol-level failures (malformed JSON-RPC, unknown method, unknown tool)
// become JSON-RPC errors.
package mcp

import (
	"errors"
	"fmt"

	"github.com/madeinoz67/go-parts/internal/components"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
	"github.com/madeinoz67/go-parts/internal/via"
)

// protocolVersion is the MCP revision this surface speaks (same as go-rag's).
const protocolVersion = "2024-11-05"

// ErrUnknownTool is the protocol-level sentinel for tools/call against a name
// the server does not dispatch. Protocol-level per the spec's error model —
// surfaces as JSON-RPC -32602, never as an isError tool result.
var ErrUnknownTool = errors.New("mcp: unknown tool")

// Server is the MCP surface over the SAME store instances the REST and UI
// servers use (constructed once in daemon.Run). Tools are thin renderings of
// store calls — identical results across surfaces, never a hop through REST.
type Server struct {
	store      *parts.Store
	fts        *index.FTS
	via        *via.Store
	locations  *locations.Store
	components *components.Store
}

// NewServer wires the MCP Server. Same parameter order as rest.NewServer.
// All five stores are required (unlike the UI's nil-tolerant locStore) — the
// daemon always has the full composition, and read tools need locations for
// label resolution from day one.
func NewServer(store *parts.Store, fts *index.FTS, viaStore *via.Store, locStore *locations.Store, compStore *components.Store) *Server {
	return &Server{store: store, fts: fts, via: viaStore, locations: locStore, components: compStore}
}

// rpcReq is the JSON-RPC 2.0 request envelope (go-rag's shape).
type rpcReq struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

// handle dispatches one JSON-RPC method. A nil return means "no response
// body" (a notification).
func (s *Server) handle(req rpcReq) any {
	if req.ID == nil {
		// A notification per JSON-RPC 2.0 — never answered, whatever the
		// method. go-rag special-cases notifications/initialized only; we
		// accept any id-less message (clients send notifications/cancelled).
		return nil
	}
	switch req.Method {
	case "initialize":
		return ok(req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "go-parts", "version": "dev"},
		})
	case "tools/list":
		return ok(req.ID, map[string]any{"tools": toolDefs()})
	case "tools/call":
		return s.callTool(req)
	}
	return errResp(req.ID, -32601, "method not found: "+req.Method)
}

// callTool renders one tool invocation. Domain errors become isError tool
// results (MCP convention — the client self-corrects in one round-trip);
// ErrUnknownTool stays protocol-level (-32602).
func (s *Server) callTool(req rpcReq) any {
	name, _ := req.Params["name"].(string)
	args, _ := req.Params["arguments"].(map[string]any)
	out, err := s.dispatch(name, args)
	if err != nil {
		if errors.Is(err, ErrUnknownTool) {
			return errResp(req.ID, -32602, err.Error())
		}
		return ok(req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
		})
	}
	return ok(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": out}},
	})
}

// dispatch routes a tool call to its renderer.
func (s *Server) dispatch(name string, args map[string]any) (string, error) {
	switch name {
	case "search_parts":
		return s.toolSearchParts(args)
	case "get_part":
		return s.toolGetPart(args)
	case "list_low_stock":
		return s.toolListLowStock(args)
	case "get_inventory_stats":
		return s.toolGetInventoryStats(args)
	case "upsert_part":
		return s.toolUpsertPart(args)
	case "adjust_stock":
		return s.toolAdjustStock(args)
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownTool, name)
}

// toolDefs is the tools/list manifest (read tools + write tools).
func toolDefs() []map[string]any {
	return []map[string]any{
		{
			"name":        "search_parts",
			"description": "Search the parts inventory (BM25 full-text, same search as the web UI). Empty query lists the corpus; optional tag and low-stock filters.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "free-text search (mpn, description, specs...)"},
					"tag":   map[string]any{"type": "string"},
					"low":   map[string]any{"type": "boolean", "description": "keep only parts at/below their reorder point"},
					"limit": map[string]any{"type": "integer", "default": 20, "maximum": 100},
				},
			},
		},
		{
			"name":        "get_part",
			"description": "Full part record (PascalCase fields) plus per-location stock and the 10 most recent stock movements. Selector: id, P- via-code, mpn, or local_number.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":           map[string]any{"type": "string"},
					"mpn":          map[string]any{"type": "string"},
					"local_number": map[string]any{"type": "string"},
				},
			},
		},
		{
			"name":        "list_low_stock",
			"description": "Parts at or below their reorder point (QtyOnHand <= ReorderPoint).",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "get_inventory_stats",
			"description": "Inventory totals: parts_total, locations_total, low_stock, out_of_stock.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "upsert_part",
			"description": "Create or update a part from a full record (the get_part → edit → upsert loop). Empty ID creates; on update the record's Version field is the concurrency token — a stale Version is rejected with the conflict error, never silently overwritten. QtyOnHand is only set at create; ongoing stock changes go through adjust_stock.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"part"},
				"properties": map[string]any{
					"part": map[string]any{
						"type":        "object",
						"description": "full Part record with PascalCase fields, same shape get_part returns",
					},
				},
			},
		},
		{
			"name":        "adjust_stock",
			"description": "Move stock at one location: appends a movement (with your reason) and re-derives the part's total. location is a bin label or L- via-code; if the part isn't stocked there, the error lists where it actually is.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"part", "location", "delta", "reason"},
				"properties": map[string]any{
					"part":     map[string]any{"type": "string", "description": "id, P- via-code, mpn, or local_number"},
					"location": map[string]any{"type": "string", "description": "bin label or L- via-code"},
					"delta":    map[string]any{"type": "integer", "description": "signed: positive in, negative out"},
					"reason":   map[string]any{"type": "string"},
				},
			},
		},
	}
}

// --- JSON-RPC helpers (go-rag's shape) ---

func ok(id any, result any) any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func errResp(id any, code int, msg string) any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}}
}
