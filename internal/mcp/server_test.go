package mcp

// server_test.go — the JSON-RPC envelope tests. The 84b3a99 parsing-context
// lesson applied to JSON-RPC: pin the FULL envelope (status code, result
// shape, error codes), not just the payload — a handler that swaps a result
// for an error of the same id must turn a test red.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/components"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
	"github.com/madeinoz67/go-parts/internal/via"
)

// newTestServer mirrors rest/server_test.go's newTestServer: a real Pebble in
// a temp dir, the five composed stores, cleanup-registered close. The MCP
// server is the 4th adapter over the same instances — identical results to
// REST and the UI by construction (PRD §5.2).
func newTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := pebble.Open(filepath.Join(t.TempDir(), "p"), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fts := index.NewFTS(db)
	vs := via.NewStore(db)
	store := parts.NewStore(db, fts, vs)
	ls := locations.NewStore(db, vs)
	cs := components.NewStore(db, store)
	return NewServer(store, fts, vs, ls, cs)
}

// call drives one JSON-RPC request through the HTTP handler — no listening
// socket, same httptest discipline as the REST suite.
func call(t *testing.T, srv *Server, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.HTTPHandler().ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("response is not JSON: %v (%q)", err, rec.Body.String())
		}
	}
	return rec.Code, out
}

// resultOf digs result.<key> out of an envelope.
func resultOf(t *testing.T, out map[string]any, key string) any {
	t.Helper()
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result object in %v", out)
	}
	return res[key]
}

func TestInitialize(t *testing.T) {
	code, out := call(t, newTestServer(t), `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got := resultOf(t, out, "protocolVersion"); got != protocolVersion {
		t.Errorf("protocolVersion = %v, want %q", got, protocolVersion)
	}
	caps, ok := resultOf(t, out, "capabilities").(map[string]any)
	if !ok || caps["tools"] == nil {
		t.Errorf("capabilities.tools missing: %v", out)
	}
	info, ok := resultOf(t, out, "serverInfo").(map[string]any)
	if !ok || info["name"] != "go-parts" {
		t.Errorf("serverInfo.name = %v, want go-parts", out)
	}
}

func TestToolsListGrowsWithTasks(t *testing.T) {
	_, out := call(t, newTestServer(t), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools, ok := resultOf(t, out, "tools").([]any)
	if !ok {
		t.Fatalf("tools/list result.tools missing: %v", out)
	}
	if len(tools) != 6 {
		t.Fatalf("want 6 tools (Task 3), got %d: %v", len(tools), out)
	}
	srv := newTestServer(t)
	for _, ta := range tools {
		td := ta.(map[string]any)
		name, _ := td["name"].(string)
		if td["description"] == nil || td["inputSchema"] == nil {
			t.Errorf("tool %q missing description/inputSchema", name)
		}
		_, out2 := call(t, srv, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"`+name+`","arguments":{}}}`)
		if _, hasErr := out2["error"]; hasErr {
			t.Errorf("tools/list advertises %q but tools/call errors", name)
		}
	}
}

func TestUnknownMethodIsJSONRPCError(t *testing.T) {
	_, out := call(t, newTestServer(t), `{"jsonrpc":"2.0","id":4,"method":"resources/list"}`)
	errObj, ok := out["error"].(map[string]any)
	if !ok || errObj["code"] != float64(-32601) {
		t.Fatalf("want -32601 method-not-found, got %v", out)
	}
}

func TestUnknownToolIsJSONRPCError(t *testing.T) {
	_, out := call(t, newTestServer(t), `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope"}}`)
	errObj, ok := out["error"].(map[string]any)
	if !ok || errObj["code"] != float64(-32602) {
		t.Fatalf("want -32602 invalid-params for unknown tool, got %v", out)
	}
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "nope") {
		t.Errorf("error message should name the tool: %v", errObj)
	}
}

func TestMalformedJSONIsHTTP400(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{not json`))
	rec := httptest.NewRecorder()
	srv.HTTPHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	srv.HTTPHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestNotificationHasNoBody(t *testing.T) {
	// A JSON-RPC notification (no id) gets no response body. go-rag handles
	// only notifications/initialized; we accept ANY id-less message silently —
	// clients send notifications/cancelled in the wild.
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{}}`))
	rec := httptest.NewRecorder()
	srv.HTTPHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("notification got a response body: %q", rec.Body.String())
	}
}

// F3: /mcp is a mutating endpoint with an Origin same-origin guard
// (ui.Server.auth parity): a browser ALWAYS sends Origin on a POST, so a
// mismatched Origin is a blind cross-origin form fire → 403. A matching or
// ABSENT Origin passes (curl and `claude mcp add --transport http` send no
// Origin — they are not CSRF vectors).
func TestOriginGuardBlocksCrossOriginPost(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	srv.HTTPHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (cross-origin POST blocked)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "cross-origin request blocked") {
		t.Fatalf("body should name the block: %q", rec.Body.String())
	}
}

func TestOriginGuardAllowsSameOriginAndNoOriginPost(t *testing.T) {
	srv := newTestServer(t)
	// Same-origin: httptest.NewRequest defaults the host to example.com,
	// scheme http (r.TLS nil) — expected Origin is "http://example.com".
	same := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	same.Header.Set("Origin", "http://example.com")
	rec := httptest.NewRecorder()
	srv.HTTPHandler().ServeHTTP(rec, same)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-origin POST status = %d, want 200", rec.Code)
	}
	// No Origin header at all (curl, claude mcp http): allowed.
	none := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	rec2 := httptest.NewRecorder()
	srv.HTTPHandler().ServeHTTP(rec2, none)
	if rec2.Code != http.StatusOK {
		t.Fatalf("no-origin POST status = %d, want 200", rec2.Code)
	}
}
