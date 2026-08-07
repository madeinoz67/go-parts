// Package rest exposes go-parts' parts.Store + FTS over a stdlib net/http
// ServeMux (Go 1.26 method-patterns). These tests drive every route end-to-end
// against a real Pebble + FTS + Store — no mocks — so the §5.14 concurrency
// safety built into Store.Update/AdjustStock is exercised through the HTTP
// layer that will be the first multi-writer surface in production.
//
// JSON field names are Go field names verbatim (parts.Part ships no json tags;
// REST T10 and the e2e test T12 use Go field names per the part.go comment —
// adding tags later is additive).
package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/parts"
)

// newTestServer wires a real Pebble + FTS + Store + REST Server. The DB lives
// under t.TempDir() and is closed on cleanup, mirroring parts.newStore. The
// FTS is created once and shared between the store (which writes to it) and
// the server (which reads from it) so the read path sees the writes — two FTS
// instances over the same Pebble would share on-disk postings but diverge on
// the in-memory idfCache, which is not what production does.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := pebble.Open(filepath.Join(t.TempDir(), "p"), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fts := index.NewFTS(db)
	store := parts.NewStore(db, fts)
	return NewServer(store, fts)
}

// post/get/patch/delete are thin dispatch helpers that drive srv.ServeHTTP via
// httptest. No real listening socket — keeps tests fast and inspection easy.
func post(srv *Server, path string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func get(srv *Server, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func patch(srv *Server, path, body, ifMatch string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func deleteReq(srv *Server, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

// extractID unmarshals the response body (a single Part JSON) and returns its
// ID. Used to chain create→get→patch in one test.
func extractID(t *testing.T, body []byte) string {
	t.Helper()
	var p parts.Part
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("extractID: decode %q: %v", string(body), err)
	}
	if p.ID == "" {
		t.Fatalf("extractID: empty ID in body %q", string(body))
	}
	return p.ID
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t)
	rr := get(srv, "/healthz")
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", rr.Code)
	}
	if rr.Body.String() != "ok" {
		t.Fatalf("healthz body = %q, want %q", rr.Body.String(), "ok")
	}
}

func TestStats(t *testing.T) {
	srv := newTestServer(t)
	rr := get(srv, "/stats")
	if rr.Code != http.StatusOK {
		t.Fatalf("stats on empty = %d, want 200", rr.Code)
	}
	var m map[string]int
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("stats decode: %v", err)
	}
	if m["parts_total"] != 0 {
		t.Fatalf("empty stats parts_total = %d, want 0", m["parts_total"])
	}
	// Add one part, count should reflect it.
	post(srv, "/parts", `{"MPN":"S1","PartType":"local"}`)
	rr = get(srv, "/stats")
	if rr.Code != http.StatusOK {
		t.Fatalf("stats after create = %d, want 200", rr.Code)
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("stats decode: %v", err)
	}
	if m["parts_total"] != 1 {
		t.Fatalf("stats parts_total = %d, want 1", m["parts_total"])
	}
}

// TestCreateGetPatch is the spec's primary happy-path: create → GET returns
// ETag → PATCH without If-Match (428 Precondition Required) → PATCH with the
// correct If-Match (200 OK, version bumped). Field names are Go field names.
func TestCreateGetPatch(t *testing.T) {
	srv := newTestServer(t)

	// Create.
	rr := post(srv, "/parts", `{"MPN":"R1","PartType":"linked","Description":"10k"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	id := extractID(t, rr.Body.Bytes())
	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("create response missing ETag")
	}

	// GET returns ETag too.
	rr = get(srv, "/parts/"+id)
	if rr.Code != http.StatusOK {
		t.Fatalf("get = %d, want 200", rr.Code)
	}
	if rr.Header().Get("ETag") == "" {
		t.Fatal("GET missing ETag")
	}
	var got parts.Part
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("get decode: %v", err)
	}
	if got.Description != "10k" {
		t.Fatalf("get Description = %q, want %q", got.Description, "10k")
	}

	// PATCH without If-Match → 428 Precondition Required.
	rr = patch(srv, "/parts/"+id, `{"Description":"edited"}`, "")
	if rr.Code != http.StatusPreconditionRequired {
		t.Fatalf("patch without If-Match = %d, want 428", rr.Code)
	}

	// PATCH with correct If-Match → 200, version bumped to 2.
	rr = patch(srv, "/parts/"+id, `{"Description":"edited"}`, etag)
	if rr.Code != http.StatusOK {
		t.Fatalf("patch = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var patched parts.Part
	if err := json.Unmarshal(rr.Body.Bytes(), &patched); err != nil {
		t.Fatalf("patch decode: %v", err)
	}
	if patched.Version != 2 {
		t.Fatalf("patched Version = %d, want 2", patched.Version)
	}
	if patched.Description != "edited" {
		t.Fatalf("patched Description = %q, want %q", patched.Description, "edited")
	}

	// Round-trip invariant (folded-in req #1): CreatedAt/CreatedBy must survive
	// the Get-then-edit PATCH, not be zeroed.
	if patched.CreatedAt.IsZero() {
		t.Fatal("PATCH zeroed CreatedAt (Get-then-edit broken)")
	}
	if patched.CreatedBy == "" {
		t.Fatal("PATCH zeroed CreatedBy (Get-then-edit broken)")
	}
	// MPN should round-trip too (it wasn't in the patch body).
	if patched.MPN != "R1" {
		t.Fatalf("PATCH dropped MPN: got %q, want %q", patched.MPN, "R1")
	}
}

// TestPatchStaleVersion pins §5.14 at the REST layer: a PATCH whose If-Match
// is not the current stored version is rejected with 409 Conflict — never
// silently overwritten.
func TestPatchStaleVersion(t *testing.T) {
	srv := newTestServer(t)
	rr := post(srv, "/parts", `{"MPN":"V1","PartType":"local","Description":"initial"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201", rr.Code)
	}
	id := extractID(t, rr.Body.Bytes())
	firstEtag := rr.Header().Get("ETag")

	// First PATCH succeeds, bumps version to 2.
	rr = patch(srv, "/parts/"+id, `{"Description":"v2"}`, firstEtag)
	if rr.Code != http.StatusOK {
		t.Fatalf("first patch = %d, want 200", rr.Code)
	}

	// Second PATCH with the SAME stale If-Match (version 1) → 409.
	rr = patch(srv, "/parts/"+id, `{"Description":"stale-write"}`, firstEtag)
	if rr.Code != http.StatusConflict {
		t.Fatalf("stale patch = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}

	// And the stored record must reflect the v2 state, not the stale write.
	rr = get(srv, "/parts/"+id)
	var got parts.Part
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Description != "v2" {
		t.Fatalf("after stale patch: Description = %q, want %q", got.Description, "v2")
	}
	if got.Version != 2 {
		t.Fatalf("after stale patch: Version = %d, want 2", got.Version)
	}
}

// TestPatchPreservesStock pins the F3 invariant at the REST layer: even when a
// PATCH body carries a QtyOnHand, it MUST NOT change the stock — stock is the
// AdjustStock endpoint's exclusive domain (§5.14). Store.Update enforces this
// server-side; this test proves the REST handler doesn't bypass it.
func TestPatchPreservesStock(t *testing.T) {
	srv := newTestServer(t)
	rr := post(srv, "/parts", `{"MPN":"SKU1","PartType":"local","Description":"cap","QtyOnHand":5}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201", rr.Code)
	}
	id := extractID(t, rr.Body.Bytes())
	etag := rr.Header().Get("ETag")

	// Adjust stock the authoritative way: +3 → QtyOnHand = 8.
	rr = post(srv, "/parts/"+id+"/stock", `{"Delta":3}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("stock adjust = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	// Now PATCH with a bogus QtyOnHand in the body — the F3 invariant says it
	// must be ignored (Update preserves cur.QtyOnHand).
	rr = patch(srv, "/parts/"+id, `{"Description":"cap-edited","QtyOnHand":9999}`, etag)
	if rr.Code != http.StatusOK {
		t.Fatalf("patch = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	rr = get(srv, "/parts/"+id)
	var got parts.Part
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.QtyOnHand != 8 {
		t.Fatalf("PATCH leaked stock change: QtyOnHand = %d, want 8 (AdjustStock-only)", got.QtyOnHand)
	}
	if got.Description != "cap-edited" {
		t.Fatalf("PATCH didn't apply Description: got %q, want %q", got.Description, "cap-edited")
	}
}

// TestStockAdjustAdjustUnknown returns 5xx (AdjustStock errors on missing part).
func TestStockAdjustUnknown(t *testing.T) {
	srv := newTestServer(t)
	rr := post(srv, "/parts/nope/stock", `{"Delta":1}`)
	if rr.Code == http.StatusOK {
		t.Fatalf("stock adjust on missing part = 200, want error")
	}
}

func TestDelete(t *testing.T) {
	srv := newTestServer(t)
	rr := post(srv, "/parts", `{"MPN":"DEL","PartType":"local","Description":"delete me unique-token"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201", rr.Code)
	}
	id := extractID(t, rr.Body.Bytes())

	rr = deleteReq(srv, "/parts/"+id)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", rr.Code)
	}
	// Subsequent GET → 404.
	rr = get(srv, "/parts/"+id)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", rr.Code)
	}
	// Idempotent delete — store.Delete errors on missing record now; REST
	// surfaces it as a non-204 (the spec didn't promise idempotent delete).
	rr = deleteReq(srv, "/parts/"+id)
	if rr.Code == http.StatusNoContent {
		t.Fatalf("re-delete = 204 — store.Delete did not error on missing record")
	}
}

func TestGetUnknown(t *testing.T) {
	srv := newTestServer(t)
	rr := get(srv, "/parts/nope")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("get unknown = %d, want 404", rr.Code)
	}
}

// TestSearch drives GET /parts?q= through the field-weighted BM25 FTS, hydrating
// hits via store.Get. The query matches the Description field (MID weight).
func TestSearch(t *testing.T) {
	srv := newTestServer(t)
	if rr := get(srv, "/parts?q=mcu"); rr.Code != http.StatusOK {
		t.Fatalf("empty search = %d, want 200", rr.Code)
	}
	post(srv, "/parts", `{"MPN":"STM32F4","PartType":"linked","Description":"mcu"}`)
	rr := get(srv, "/parts?q=mcu")
	if rr.Code != http.StatusOK {
		t.Fatalf("search = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "STM32F4") {
		t.Fatalf("search did not return STM32F4: body=%s", rr.Body.String())
	}
}

// TestSearchEmptyQuery returns 200 with an empty list (no panic).
func TestSearchEmptyQuery(t *testing.T) {
	srv := newTestServer(t)
	rr := get(srv, "/parts")
	if rr.Code != http.StatusOK {
		t.Fatalf("search no-q = %d, want 200", rr.Code)
	}
	body := strings.TrimSpace(rr.Body.String())
	if body != "null" && body != "[]" {
		t.Fatalf("empty search body = %q, want null or []", body)
	}
}
