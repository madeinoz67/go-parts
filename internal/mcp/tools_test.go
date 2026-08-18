package mcp

// tools_test.go — per-tool tests over a real store. Tool-result SHAPE is
// pinned end-to-end through the JSON-RPC envelope (call → result.content[0]
// .text → unmarshalled payload): these are the cross-surface parity tests
// the spec's test strategy demands (same stores, same filters as the UI).

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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

// --- write tools (spec Unit 2) ---

func TestUpsertPartCreates(t *testing.T) {
	srv := newTestServer(t)
	txt := toolText(t, srv, "upsert_part", `{"part":{"MPN":"RC0805FR-0710KL","Description":"10k 0805 resistor","QtyOnHand":120,"ReorderPoint":50}}`)
	var rec map[string]any
	if err := json.Unmarshal([]byte(txt), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["ID"] == nil || rec["Version"] != float64(1) || rec["CreatedBy"] != "local" {
		t.Fatalf("create must return the canonical stored record: %s", txt)
	}
	// The created part is immediately retrievable through the read surface.
	if got := toolText(t, srv, "get_part", `{"mpn":"RC0805FR-0710KL"}`); !strings.Contains(got, `"MPN":"RC0805FR-0710KL"`) {
		t.Fatalf("created part not readable back: %s", got)
	}
}

func TestUpsertPartUpdateRequiresVersion(t *testing.T) {
	srv := newTestServer(t)
	created := mustCreate(t, srv, &parts.Part{MPN: "M1", QtyOnHand: 5})
	errTxt := toolError(t, srv, "upsert_part", `{"part":{"ID":"`+created.ID+`","MPN":"M1"}}`)
	if !strings.Contains(errTxt, "Version") {
		t.Fatalf("update without version must say so: %q", errTxt)
	}
}

func TestUpsertPartStaleVersionRejected(t *testing.T) {
	srv := newTestServer(t)
	created := mustCreate(t, srv, &parts.Part{MPN: "M1", Description: "original", QtyOnHand: 5}) // Version 1
	// First edit succeeds (v1 → v2).
	toolText(t, srv, "upsert_part", `{"part":{"ID":"`+created.ID+`","MPN":"M1","Description":"edited","Version":1}}`)
	// Stale retry at v1 must be REJECTED — never a silent overwrite (§5.14).
	errTxt := toolError(t, srv, "upsert_part", `{"part":{"ID":"`+created.ID+`","MPN":"M1","Description":"stale write","Version":1}}`)
	if !strings.Contains(errTxt, "version conflict") {
		t.Fatalf("stale version must surface the conflict sentinel: %q", errTxt)
	}
	fresh, _ := srv.store.Get(created.ID)
	if fresh.Description != "edited" || fresh.Version != 2 {
		t.Fatalf("stale write leaked: description=%q version=%d", fresh.Description, fresh.Version)
	}
}

func TestUpsertPartDuplicateMPNRejected(t *testing.T) {
	srv := newTestServer(t)
	mustCreate(t, srv, &parts.Part{MPN: "DUP"})
	errTxt := toolError(t, srv, "upsert_part", `{"part":{"MPN":"DUP"}}`)
	if !strings.Contains(errTxt, "parts: duplicate mpn") {
		t.Fatalf("identity sentinel verbatim: %q", errTxt)
	}
}

func TestAdjustStockHappyPath(t *testing.T) {
	srv := newTestServer(t)
	part := mustCreate(t, srv, &parts.Part{MPN: "M1", QtyOnHand: 5})
	loc := &locations.Location{Label: "Bin A3"}
	if err := srv.locations.Create(loc); err != nil {
		t.Fatal(err)
	}
	if err := srv.components.Add(loc.ID, part.ID, 5, nil); err != nil {
		t.Fatal(err)
	}
	txt := toolText(t, srv, "adjust_stock", `{"part":"M1","location":"Bin A3","delta":-2,"reason":"used two"}`)
	var got map[string]any
	if err := json.Unmarshal([]byte(txt), &got); err != nil {
		t.Fatal(err)
	}
	if got["part_qty_on_hand"] != float64(3) {
		t.Fatalf("AdjustQty must re-derive part qty (recomputePartQty path): %s", txt)
	}
	comp, _ := got["component"].(map[string]any)
	if comp == nil || comp["Quantity"] != float64(3) {
		t.Fatalf("component quantity: %s", txt)
	}
	mov, _ := got["movement"].(map[string]any)
	if mov == nil || mov["Delta"] != float64(-2) || mov["Reason"] != "used two" {
		t.Fatalf("movement recorded: %s", txt)
	}
}

func TestAdjustStockNotStockedThere(t *testing.T) {
	srv := newTestServer(t)
	part := mustCreate(t, srv, &parts.Part{MPN: "M1", QtyOnHand: 5})
	stocked := &locations.Location{Label: "Bin A3"}
	empty := &locations.Location{Label: "Drawer 9"}
	if err := srv.locations.Create(stocked); err != nil {
		t.Fatal(err)
	}
	if err := srv.locations.Create(empty); err != nil {
		t.Fatal(err)
	}
	if err := srv.components.Add(stocked.ID, part.ID, 5, nil); err != nil {
		t.Fatal(err)
	}
	errTxt := toolError(t, srv, "adjust_stock", `{"part":"M1","location":"Drawer 9","delta":1,"reason":"x"}`)
	if !strings.Contains(errTxt, "not stocked at") || !strings.Contains(errTxt, "Bin A3") {
		t.Fatalf("error must name the part's ACTUAL stocked locations: %q", errTxt)
	}
}

func TestAdjustStockUnknownLocation(t *testing.T) {
	srv := newTestServer(t)
	mustCreate(t, srv, &parts.Part{MPN: "M1"})
	errTxt := toolError(t, srv, "adjust_stock", `{"part":"M1","location":"nowhere","delta":1,"reason":"x"}`)
	if !strings.Contains(errTxt, "locations: not found") {
		t.Fatalf("unknown location sentinel: %q", errTxt)
	}
}

func TestAdjustStockAmbiguousLabel(t *testing.T) {
	srv := newTestServer(t)
	mustCreate(t, srv, &parts.Part{MPN: "M1"})
	for range 2 {
		if err := srv.locations.Create(&locations.Location{Label: "Bench"}); err != nil {
			t.Fatal(err)
		}
	}
	errTxt := toolError(t, srv, "adjust_stock", `{"part":"M1","location":"Bench","delta":1,"reason":"x"}`)
	if !strings.Contains(errTxt, "matches 2 locations") || !strings.Contains(errTxt, "L-") {
		t.Fatalf("ambiguous label must list via-codes: %q", errTxt)
	}
}

func TestAdjustStockRequiresAllArguments(t *testing.T) {
	srv := newTestServer(t)
	errTxt := toolError(t, srv, "adjust_stock", `{"part":"M1","location":"x","delta":1}`)
	if !strings.Contains(errTxt, "required") {
		t.Fatalf("missing reason is a usage error: %q", errTxt)
	}
}

// --- final-review fix wave (F1, F2, M4, M5) ---

// F1: a fractional delta must be a USAGE ERROR naming the argument, never a
// silently truncated movement (argInt's int(f) recorded 2 for {"delta":2.5}
// with no signal — the never-silently-wrong constitution).
func TestAdjustStockFractionalDeltaRejected(t *testing.T) {
	srv := newTestServer(t)
	part := mustCreate(t, srv, &parts.Part{MPN: "M1", QtyOnHand: 5})
	loc := &locations.Location{Label: "Bin A3"}
	if err := srv.locations.Create(loc); err != nil {
		t.Fatal(err)
	}
	if err := srv.components.Add(loc.ID, part.ID, 5, nil); err != nil {
		t.Fatal(err)
	}
	errTxt := toolError(t, srv, "adjust_stock", `{"part":"M1","location":"Bin A3","delta":2.5,"reason":"found two and a half"}`)
	if !strings.Contains(errTxt, "delta must be an integer") {
		t.Fatalf("fractional delta must name the problem: %q", errTxt)
	}
	// And nothing was written: quantity and movement history unchanged
	// (components.Add appends the one initial movement).
	comp, err := srv.components.Get(loc.ID, part.ID)
	if err != nil {
		t.Fatal(err)
	}
	if comp.Quantity != 5 || len(comp.History) != 1 {
		t.Fatalf("rejected delta must not record a movement: qty=%d history=%d", comp.Quantity, len(comp.History))
	}
}

// F1 sibling (confirm, don't change): upsert_part unmarshals the part body
// into int struct fields directly, so a fractional number is already a LOUD
// unmarshal error — pin it so the two write paths can't drift apart.
func TestUpsertPartFractionalIntRejectedLoudly(t *testing.T) {
	srv := newTestServer(t)
	errTxt := toolError(t, srv, "upsert_part", `{"part":{"MPN":"FRAC","QtyOnHand":2.5}}`)
	if !strings.Contains(errTxt, "part fields") || !strings.Contains(errTxt, "2.5") {
		t.Fatalf("fractional int must surface the unmarshal error verbatim: %q", errTxt)
	}
	if got := srv.store.Count(); got != 0 {
		t.Fatalf("rejected create must not write: count=%d", got)
	}
}

// F2: the create branch zeroes audit fields (rest.handleCreate parity) — a
// spoofed CreatedBy must not survive into the stored record.
func TestUpsertPartCreateZeroesAuditFields(t *testing.T) {
	srv := newTestServer(t)
	txt := toolText(t, srv, "upsert_part", `{"part":{"MPN":"SPOOF","CreatedBy":"attacker","UpdatedBy":"attacker","CreatedAt":"2020-01-01T00:00:00Z","UpdatedAt":"2020-01-01T00:00:00Z","Version":99}}`)
	var rec map[string]any
	if err := json.Unmarshal([]byte(txt), &rec); err != nil {
		t.Fatal(err)
	}
	id, _ := rec["ID"].(string)
	stored, err := srv.store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CreatedBy != "local" {
		t.Fatalf("CreatedBy = %q, want \"local\" (server-assigned; a caller must not spoof the audit trail)", stored.CreatedBy)
	}
	spoofed := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if !stored.CreatedAt.After(spoofed) {
		t.Fatalf("CreatedAt = %v, want store-assigned now (not caller-supplied)", stored.CreatedAt)
	}
	if stored.Version != 1 {
		t.Fatalf("Version = %d, want store-assigned 1", stored.Version)
	}
}

// M4: a part with ZERO components says "not stocked anywhere yet" — not an
// empty "stocked at: " list.
func TestAdjustStockNotStockedAnywhere(t *testing.T) {
	srv := newTestServer(t)
	mustCreate(t, srv, &parts.Part{MPN: "M1"})
	if err := srv.locations.Create(&locations.Location{Label: "Bin A3"}); err != nil {
		t.Fatal(err)
	}
	errTxt := toolError(t, srv, "adjust_stock", `{"part":"M1","location":"Bin A3","delta":1,"reason":"x"}`)
	if !strings.Contains(errTxt, "not stocked anywhere yet") {
		t.Fatalf("zero-component part must say so: %q", errTxt)
	}
}

// --- adversary fix wave (I1, M1, M2, M3) ---

// I1: an integral float OUTSIDE int64 is a usage error naming the bound —
// argInt's int(f) saturates to ±MaxInt64, and adjust_stock then wraps stock
// negative (1e19 + 5 == -9223372036854775804). Never silently-wrong.
func TestAdjustStockDeltaBeyondInt64Rejected(t *testing.T) {
	srv := newTestServer(t)
	part := mustCreate(t, srv, &parts.Part{MPN: "M1", QtyOnHand: 5})
	loc := &locations.Location{Label: "Bin A3"}
	if err := srv.locations.Create(loc); err != nil {
		t.Fatal(err)
	}
	if err := srv.components.Add(loc.ID, part.ID, 5, nil); err != nil {
		t.Fatal(err)
	}
	const bound = "between -9223372036854775808 and 9223372036854775807"
	for _, delta := range []string{"1e19", "-1e19"} {
		errTxt := toolError(t, srv, "adjust_stock", `{"part":"M1","location":"Bin A3","delta":`+delta+`,"reason":"x"}`)
		if !strings.Contains(errTxt, "delta must be an integer "+bound) {
			t.Fatalf("out-of-int64 delta %s must name the bound: %q", delta, errTxt)
		}
	}
	// Nothing moved: component qty, movement history (Add appends the one
	// initial movement), and the part's recomputed qty are all untouched.
	comp, err := srv.components.Get(loc.ID, part.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := srv.store.Get(part.ID)
	if err != nil {
		t.Fatal(err)
	}
	if comp.Quantity != 5 || len(comp.History) != 1 || fresh.QtyOnHand != 5 {
		t.Fatalf("rejected delta must not move stock: comp=%d history=%d part=%d",
			comp.Quantity, len(comp.History), fresh.QtyOnHand)
	}
}

// I1 sibling: search_parts' limit goes through the same argInt — an
// out-of-int64 limit must ERROR, not silently fall back to the default 20.
// The call-site range check (1..100 honored, outside → default) is unchanged.
func TestSearchPartsLimitBeyondInt64Rejected(t *testing.T) {
	srv := seedFixture(t)
	errTxt := toolError(t, srv, "search_parts", `{"limit":1e19}`)
	if !strings.Contains(errTxt, "limit must be an integer between") {
		t.Fatalf("out-of-int64 limit must error loudly, not default: %q", errTxt)
	}
	if txt := toolText(t, srv, "search_parts", `{"limit":100}`); !strings.Contains(txt, "RC0805FR-0710KL") {
		t.Fatalf("in-range limit 100 must still be honored: %s", txt)
	}
}

// M1: a partial update must not wipe create-time audit. store.Update does NOT
// preserve CreatedBy/CreatedAt (the REST surface compensates via
// get-then-merge; applyPatch's contract says audit is never patched) — the
// MCP update branch must do the same before Update.
func TestUpsertPartUpdatePreservesCreateAudit(t *testing.T) {
	srv := newTestServer(t)
	txt := toolText(t, srv, "upsert_part", `{"part":{"MPN":"AUDIT","Description":"d1"}}`)
	var created map[string]any
	if err := json.Unmarshal([]byte(txt), &created); err != nil {
		t.Fatal(err)
	}
	id, _ := created["ID"].(string)
	createdAt, _ := created["CreatedAt"].(string)
	if createdAt == "" {
		t.Fatalf("create must stamp CreatedAt: %s", txt)
	}
	// Partial record: no audit fields in the body.
	txt2 := toolText(t, srv, "upsert_part", `{"part":{"ID":"`+id+`","MPN":"AUDIT","Description":"d2","Version":1}}`)
	var updated map[string]any
	if err := json.Unmarshal([]byte(txt2), &updated); err != nil {
		t.Fatal(err)
	}
	if updated["CreatedBy"] != "local" || updated["CreatedAt"] != createdAt {
		t.Fatalf("update must preserve create-time audit, want CreatedBy=local CreatedAt=%s: %s", createdAt, txt2)
	}
	if updated["Description"] != "d2" {
		t.Fatalf("caller-supplied description must apply: %s", txt2)
	}
}

// M2: a whitespace-only selector trims to "" and would match parts whose
// MPN/LocalNumber are empty (empty identities are exempt from the uniqueness
// index, so they exist) — resolvePart must reject it like the empty selector.
func TestAdjustStockWhitespacePartSelectorRejected(t *testing.T) {
	srv := newTestServer(t)
	part := mustCreate(t, srv, &parts.Part{Description: "no identity", QtyOnHand: 5}) // empty MPN/LocalNumber
	loc := &locations.Location{Label: "Bin A3"}
	if err := srv.locations.Create(loc); err != nil {
		t.Fatal(err)
	}
	if err := srv.components.Add(loc.ID, part.ID, 5, nil); err != nil {
		t.Fatal(err)
	}
	errTxt := toolError(t, srv, "adjust_stock", `{"part":" ","location":"Bin A3","delta":1,"reason":"x"}`)
	if !strings.Contains(errTxt, "part selector is empty") {
		t.Fatalf("whitespace-only selector is a usage error: %q", errTxt)
	}
	comp, err := srv.components.Get(loc.ID, part.ID)
	if err != nil {
		t.Fatal(err)
	}
	if comp.Quantity != 5 || len(comp.History) != 1 {
		t.Fatalf("rejected selector must not move stock: qty=%d history=%d", comp.Quantity, len(comp.History))
	}
}

// M3: a selector BEGINNING "P-" may be a literal MPN, not a via code — a
// via-miss must fall through to the identity scan, not error "via: not found".
// ("LCC100" contains base32-impossible chars, so P-LCC100 is a deterministic
// via-miss.)
func TestGetPartMPNBeginningLikeViaCode(t *testing.T) {
	srv := newTestServer(t)
	mustCreate(t, srv, &parts.Part{MPN: "P-LCC100", Description: "socket adapter"})
	txt := toolText(t, srv, "get_part", `{"mpn":"P-LCC100"}`)
	if !strings.Contains(txt, `"MPN":"P-LCC100"`) {
		t.Fatalf("a P- MPN must resolve by the identity scan: %s", txt)
	}
}

// M3 sibling: the same fallthrough for locations — a label beginning "L-"
// ("BENCH" is 5 chars; via codes carry 6) resolves by label, and a REAL
// L- via-code still resolves through the index (prefix fast-path intact).
func TestAdjustStockLocationLabelBeginningLikeViaCode(t *testing.T) {
	srv := newTestServer(t)
	part := mustCreate(t, srv, &parts.Part{MPN: "M1", QtyOnHand: 5})
	loc := &locations.Location{Label: "L-BENCH"}
	if err := srv.locations.Create(loc); err != nil {
		t.Fatal(err)
	}
	if err := srv.components.Add(loc.ID, part.ID, 5, nil); err != nil {
		t.Fatal(err)
	}
	txt := toolText(t, srv, "adjust_stock", `{"part":"M1","location":"L-BENCH","delta":1,"reason":"restock"}`)
	if !strings.Contains(txt, `"part_qty_on_hand":6`) {
		t.Fatalf("an L- label must resolve by label match: %s", txt)
	}
	txt2 := toolText(t, srv, "adjust_stock", `{"part":"M1","location":"`+loc.ViaCode+`","delta":1,"reason":"restock"}`)
	if !strings.Contains(txt2, `"part_qty_on_hand":7`) {
		t.Fatalf("real via-code %s must still resolve through the index: %s", loc.ViaCode, txt2)
	}
}

// M5: adjust_stock's multi-match error carries the candidate list in
// get_part's "id (mpn)" formatting — an agent disambiguates in one round-trip.
func TestAdjustStockAmbiguousPartListsCandidates(t *testing.T) {
	srv := newTestServer(t)
	a := mustCreate(t, srv, &parts.Part{MPN: "X1"})
	b := mustCreate(t, srv, &parts.Part{MPN: "other", LocalNumber: "X1"})
	errTxt := toolError(t, srv, "adjust_stock", `{"part":"X1","location":"x","delta":1,"reason":"r"}`)
	if !strings.Contains(errTxt, "matches 2 parts") {
		t.Fatalf("multi-match must say the count: %q", errTxt)
	}
	for _, id := range []string{a.ID, b.ID} {
		if !strings.Contains(errTxt, id+" (") {
			t.Fatalf("multi-match error must list candidate %s as \"id (mpn)\": %q", id, errTxt)
		}
	}
}
