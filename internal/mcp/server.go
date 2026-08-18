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

// dispatch routes a tool call to its renderer. Task 1 ships the envelope with
// zero tools; read tools land in Task 2 and write tools in Task 3.
func (s *Server) dispatch(name string, args map[string]any) (string, error) {
	_ = args
	return "", fmt.Errorf("%w: %q", ErrUnknownTool, name)
}

// toolDefs is the tools/list manifest. Grows with each task.
func toolDefs() []map[string]any {
	return []map[string]any{}
}

// --- JSON-RPC helpers (go-rag's shape) ---

func ok(id any, result any) any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func errResp(id any, code int, msg string) any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}}
}
