package mcp

// tools_test.go — per-tool tests over a real store. Tool-result SHAPE is
// pinned end-to-end through the JSON-RPC envelope (call → result.content[0]
// .text → unmarshalled payload): these are the cross-surface parity tests
// the spec's test strategy demands (same stores, same filters as the UI).

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
)

// mustCreate creates a part through the real store (Create fills ID/ViaCode/
// audit/Version — resolvePart's via-code path depends on that).
func mustCreate(t *testing.T, srv *Server, p *parts.Part) *parts.Part {
	t.Helper()
	if err := srv.store.Create(p); err != nil {
		t.Fatal(err)
	}
	fresh, err := srv.store.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return fresh
}

// toolText calls tools/call and returns the content text (fails on isError).
func toolText(t *testing.T, srv *Server, name, argsJSON string) string {
	t.Helper()
	_, out := call(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+argsJSON+`}}`)
	if errObj, hasErr := out["error"]; hasErr {
		t.Fatalf("tool %s: protocol error %v", name, errObj)
	}
	res := out["result"].(map[string]any)
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("tool %s: unexpected isError: %v", name, res["content"])
	}
	content := res["content"].([]any)
	return content[0].(map[string]any)["text"].(string)
}

// toolError calls tools/call and returns the isError content text (fails if
// the tool succeeded) — the domain-error path must carry the sentinel
// verbatim, so callers assert on the exact engine strings.
func toolError(t *testing.T, srv *Server, name, argsJSON string) string {
	t.Helper()
	_, out := call(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+argsJSON+`}}`)
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("want isError result, got protocol error: %v", out)
	}
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("tool %s unexpectedly succeeded: %v", name, res["content"])
	}
	content := res["content"].([]any)
	return content[0].(map[string]any)["text"].(string)
}

func seedFixture(t *testing.T) *Server {
	t.Helper()
	srv := newTestServer(t)
	mustCreate(t, srv, &parts.Part{MPN: "RC0805FR-0710KL", Description: "10k 0805 resistor", QtyOnHand: 120, ReorderPoint: 50, Tags: []string{"passive"}})
	mustCreate(t, srv, &parts.Part{MPN: "ESP32-WROOM-32E", Description: "mcu module", QtyOnHand: 2, ReorderPoint: 5, Tags: []string{"mcu"}})
	return srv
}

func TestSearchPartsFTS(t *testing.T) {
	srv := seedFixture(t)
	txt := toolText(t, srv, "search_parts", `{"query":"resistor"}`)
	var hits []map[string]any
	if err := json.Unmarshal([]byte(txt), &hits); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, txt)
	}
	if len(hits) != 1 || hits[0]["mpn"] != "RC0805FR-0710KL" {
		t.Fatalf("want the resistor only, got %s", txt)
	}
	for _, key := range []string{"id", "local_number", "mpn", "description", "qty_on_hand", "locations"} {
		if _, ok := hits[0][key]; !ok {
			t.Errorf("hit missing %q: %s", key, txt)
		}
	}
}

func TestSearchPartsEmptyQueryListsCorpus(t *testing.T) {
	// Parity with ui.filteredParts: empty q → store.List(), NOT an error.
	srv := seedFixture(t)
	txt := toolText(t, srv, "search_parts", `{}`)
	var hits []map[string]any
	if err := json.Unmarshal([]byte(txt), &hits); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("want both parts on empty query, got %s", txt)
	}
}

func TestSearchPartsTagAndLowFilters(t *testing.T) {
	srv := seedFixture(t) // esp32: qty 2 <= reorder 5 → low
	var hits []map[string]any
	txt := toolText(t, srv, "search_parts", `{"low":true}`)
	json.Unmarshal([]byte(txt), &hits)
	if len(hits) != 1 || hits[0]["mpn"] != "ESP32-WROOM-32E" {
		t.Fatalf("low filter parity with ui.filteredParts: %s", txt)
	}
	txt = toolText(t, srv, "search_parts", `{"query":"","tag":"passive"}`)
	json.Unmarshal([]byte(txt), &hits)
	if len(hits) != 1 || hits[0]["mpn"] != "RC0805FR-0710KL" {
		t.Fatalf("tag filter: %s", txt)
	}
}

func TestSearchPartsLimit(t *testing.T) {
	srv := seedFixture(t)
	txt := toolText(t, srv, "search_parts", `{"limit":1}`)
	var hits []map[string]any
	json.Unmarshal([]byte(txt), &hits)
	if len(hits) != 1 {
		t.Fatalf("limit: %s", txt)
	}
}

func TestGetPartByMPNAndStock(t *testing.T) {
	srv := seedFixture(t)
	loc := &locations.Location{Label: "Bin A3"}
	if err := srv.locations.Create(loc); err != nil {
		t.Fatal(err)
	}
	part := srv.store.List()[0]
	if err := srv.components.Add(loc.ID, part.ID, 120, nil); err != nil {
		t.Fatal(err)
	}
	if err := srv.components.AdjustQty(loc.ID, part.ID, -20, "used twenty for board rev B"); err != nil {
		t.Fatal(err)
	}
	txt := toolText(t, srv, "get_part", `{"mpn":"RC0805FR-0710KL"}`)
	var got map[string]any
	if err := json.Unmarshal([]byte(txt), &got); err != nil {
		t.Fatal(err)
	}
	rec, _ := got["part"].(map[string]any)
	if rec == nil || rec["MPN"] != "RC0805FR-0710KL" || rec["QtyOnHand"] != float64(100) {
		t.Fatalf("part record (PascalCase, recomputed qty): %s", txt)
	}
	stock, _ := got["stock"].([]any)
	if len(stock) != 1 {
		t.Fatalf("stock per location: %s", txt)
	}
	s := stock[0].(map[string]any)
	if s["label"] != "Bin A3" || s["qty"] != float64(100) || s["via_code"] != loc.ViaCode {
		t.Fatalf("stock entry: %s", txt)
	}
	movs, _ := got["recent_movements"].([]any)
	if len(movs) != 2 {
		t.Fatalf("movement tail: %s", txt)
	}
	m := movs[0].(map[string]any)
	if m["Delta"] != float64(-20) || m["Reason"] != "used twenty for board rev B" {
		t.Fatalf("movement fields: %s", txt)
	}
}

func TestGetPartByViaCode(t *testing.T) {
	srv := seedFixture(t)
	p := srv.store.List()[0]
	txt := toolText(t, srv, "get_part", `{"id":"`+p.ViaCode+`"}`)
	if !strings.Contains(txt, `"MPN":"RC0805FR-0710KL"`) {
		t.Fatalf("via-code selector: %s", txt)
	}
}

func TestGetPartAmbiguousSelector(t *testing.T) {
	srv := newTestServer(t)
	// Cross-field collision: part A's MPN equals part B's LocalNumber — both
	// match the "X1" selector. (Same-field duplicates are impossible through
	// Create; legacy pre-v5 dupes are the production path to this branch.)
	mustCreate(t, srv, &parts.Part{MPN: "X1"})
	mustCreate(t, srv, &parts.Part{MPN: "other", LocalNumber: "X1"})
	errTxt := toolError(t, srv, "get_part", `{"mpn":"X1"}`)
	if !strings.Contains(errTxt, "matches 2 parts") {
		t.Fatalf("disambiguation error must list matches: %q", errTxt)
	}
}

func TestGetPartNotFound(t *testing.T) {
	srv := newTestServer(t)
	errTxt := toolError(t, srv, "get_part", `{"mpn":"nope"}`)
	if !strings.Contains(errTxt, "parts: not found") {
		t.Fatalf("sentinel verbatim: %q", errTxt)
	}
}

func TestListLowStock(t *testing.T) {
	srv := newTestServer(t)
	mustCreate(t, srv, &parts.Part{MPN: "a", QtyOnHand: 2, ReorderPoint: 5})  // low
	mustCreate(t, srv, &parts.Part{MPN: "b", QtyOnHand: 50, ReorderPoint: 5}) // fine
	mustCreate(t, srv, &parts.Part{MPN: "c", QtyOnHand: 0, ReorderPoint: 0})  // 0<=0 — LOW (ui parity)
	txt := toolText(t, srv, "list_low_stock", `{}`)
	var hits []map[string]any
	json.Unmarshal([]byte(txt), &hits)
	if len(hits) != 2 {
		t.Fatalf("low = qty<=reorder incl. 0/0 (ui.filteredParts parity): %s", txt)
	}
}

func TestGetInventoryStats(t *testing.T) {
	srv := seedFixture(t)
	if err := srv.locations.Create(&locations.Location{Label: "Bin A3"}); err != nil {
		t.Fatal(err)
	}
	txt := toolText(t, srv, "get_inventory_stats", `{}`)
	var got map[string]int
	if err := json.Unmarshal([]byte(txt), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"parts_total": 2, "locations_total": 1, "low_stock": 1, "out_of_stock": 0}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("stats[%s] = %d, want %d (got %s)", k, got[k], v, txt)
		}
	}
}
