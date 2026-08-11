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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/link"
	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
	"github.com/madeinoz67/go-parts/internal/via"
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
	vs := via.NewStore(db)
	store := parts.NewStore(db, fts, vs)
	ls := locations.NewStore(db, vs) // Slice 4: resolver + label handlers read it
	return NewServer(store, fts, vs, ls)
}

// newTestServerWithLocations wires a REST server whose parts.Store has the
// single_part_only guard live (link.NewPolicy over a locations.Store), so PATCH
// /parts DefaultLocationID assignment exercises the guard through the HTTP
// layer. Mirrors daemon wiring. Slice 3b — this is the surface that makes 3a's
// locLocks TOCTOU fix reachable.
func newTestServerWithLocations(t *testing.T) (*Server, *locations.Store) {
	t.Helper()
	db, err := pebble.Open(filepath.Join(t.TempDir(), "p"), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fts := index.NewFTS(db)
	vs := via.NewStore(db)
	ps := parts.NewStore(db, fts, vs)
	ls := locations.NewStore(db, vs)
	ps.SetLocationPolicy(link.NewPolicy(ps, ls))
	return NewServer(ps, fts, vs, ls), ls
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

// --- Slice 3b: REST DefaultLocationID + the guard on the PATCH surface -----

// restCreate POSTs a part body and returns the decoded stored record (with the
// server-assigned ID + Version). Slice 3b helper.
func restCreate(t *testing.T, srv *Server, body string) parts.Part {
	t.Helper()
	rr := post(srv, "/parts", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var p parts.Part
	if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode created part: %v", err)
	}
	return p
}

// TestPatchSetsDefaultLocation pins applyPatch copies DefaultLocationID into the
// store (the guard fires in Update). Slice 3b.
func TestPatchSetsDefaultLocation(t *testing.T) {
	srv, ls := newTestServerWithLocations(t)
	loc := &locations.Location{Label: "RA"}
	if err := ls.Create(loc); err != nil {
		t.Fatal(err)
	}
	p := restCreate(t, srv, `{"MPN":"R-LOC","PartType":"local"}`)
	etag := strconv.Quote(strconv.Itoa(p.Version))
	rr := patch(srv, "/parts/"+p.ID, `{"DefaultLocationID":"`+loc.ID+`"}`, etag)
	if rr.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rr.Code, rr.Body.String())
	}
	got, _ := srv.store.Get(p.ID)
	if got.DefaultLocationID != loc.ID {
		t.Errorf("stored DefaultLocationID = %q, want %q", got.DefaultLocationID, loc.ID)
	}
}

// TestPatchGuardConflict409 pins the guard surfaces as 409 on the PATCH surface
// (a second distinct part onto an occupied SinglePartOnly location).
func TestPatchGuardConflict409(t *testing.T) {
	srv, ls := newTestServerWithLocations(t)
	solo := &locations.Location{Label: "SOLO", SinglePartOnly: true}
	if err := ls.Create(solo); err != nil {
		t.Fatal(err)
	}
	restCreate(t, srv, `{"MPN":"A","PartType":"local","DefaultLocationID":"`+solo.ID+`"}`) // sole occupant
	b := restCreate(t, srv, `{"MPN":"B","PartType":"local"}`)
	etag := strconv.Quote(strconv.Itoa(b.Version))
	rr := patch(srv, "/parts/"+b.ID, `{"DefaultLocationID":"`+solo.ID+`"}`, etag)
	if rr.Code != http.StatusConflict {
		t.Fatalf("patch into occupied single-part-only = %d, want 409; body=%s", rr.Code, rr.Body)
	}
}

// TestPatchConcurrentSinglePartOnlyExactlyOneWins is the load-bearing
// confirmation that 3a's locLocks hold on the now-reachable REST PATCH surface:
// N concurrent PATCHes, each assigning a distinct part to the SAME
// SinglePartOnly location, must yield exactly one 200 — the rest get 409. Before
// 3a's locLocks this would multi-win; before 3b's applyPatch the surface could
// not even set the field. Run with -race.
func TestPatchConcurrentSinglePartOnlyExactlyOneWins(t *testing.T) {
	srv, ls := newTestServerWithLocations(t)
	solo := &locations.Location{Label: "SOLO", SinglePartOnly: true}
	if err := ls.Create(solo); err != nil {
		t.Fatal(err)
	}
	const N = 30
	ids := make([]string, N)
	vers := make([]int, N)
	for i := range N {
		p := restCreate(t, srv, `{"MPN":"C","PartType":"local"}`)
		ids[i], vers[i] = p.ID, p.Version
	}
	var wg sync.WaitGroup
	var ok int32
	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			etag := strconv.Quote(strconv.Itoa(vers[i]))
			rr := patch(srv, "/parts/"+ids[i], `{"DefaultLocationID":"`+solo.ID+`"}`, etag)
			if rr.Code == http.StatusOK {
				atomic.AddInt32(&ok, 1)
			}
		}(i)
	}
	wg.Wait()
	if ok != 1 {
		t.Errorf("concurrent PATCH onto single-part-only: %d ok, want exactly 1 (locLocks not holding on REST surface)", ok)
	}
}

// --- Slice 4: generic Via resolver + label endpoints ----------------------

// checkLabel is the shared label-response assertion: 200, image/svg+xml, an
// SVG body, and the human-readable via-code present in the markup.
func checkLabel(t *testing.T, rr *httptest.ResponseRecorder, code string) {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("label = %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("label Content-Type = %q, want image/svg+xml", ct)
	}
	body := rr.Body.String()
	if !strings.HasPrefix(body, "<svg") {
		t.Errorf("label body is not an SVG: %q", body)
	}
	if !strings.Contains(body, code) {
		t.Errorf("label missing the via-code %q in the markup", code)
	}
}

// TestViaResolvesLocationWithContents pins scan-to-find (§5.17): resolving a
// location's code returns the location WITH its assigned parts embedded.
func TestViaResolvesLocationWithContents(t *testing.T) {
	srv, ls := newTestServerWithLocations(t)
	bin := &locations.Location{Label: "Bin"}
	if err := ls.Create(bin); err != nil {
		t.Fatal(err)
	}
	post(srv, "/parts", `{"MPN":"a","PartType":"local","DefaultLocationID":"`+bin.ID+`"}`)
	post(srv, "/parts", `{"MPN":"b","PartType":"local","DefaultLocationID":"`+bin.ID+`"}`)
	rr := get(srv, "/via/"+bin.ViaCode)
	if rr.Code != http.StatusOK {
		t.Fatalf("via = %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"Type":"location"`) {
		t.Errorf("via body missing Type=location: %s", body)
	}
	// 2 parts embedded (2 DefaultLocationID occurrences in Contents).
	if c := strings.Count(body, `"DefaultLocationID"`); c != 2 {
		t.Errorf("via body has %d part DefaultLocationID fields, want 2 (contents embedded): %s", c, body)
	}
}

// TestViaResolvesPart pins the part branch: a P- code → type=part + the part.
func TestViaResolvesPart(t *testing.T) {
	srv, _ := newTestServerWithLocations(t)
	p := restCreate(t, srv, `{"MPN":"VP","PartType":"local"}`)
	rr := get(srv, "/via/"+p.ViaCode)
	if rr.Code != http.StatusOK {
		t.Fatalf("via part = %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"Type":"part"`) || !strings.Contains(body, p.ID) {
		t.Errorf("via part body wrong: %s", body)
	}
}

// TestViaUnknown404 pins the via-miss → 404.
func TestViaUnknown404(t *testing.T) {
	srv, _ := newTestServerWithLocations(t)
	if rr := get(srv, "/via/L-NOPE"); rr.Code != http.StatusNotFound {
		t.Errorf("via unknown = %d, want 404", rr.Code)
	}
}

// TestViaDanglingReference404 pins the Slice-4 review should-fix: a via code
// whose entity was deleted (a via.Release that failed mid-delete, leaving the
// index pointing at a gone record) resolves via.Lookup-ok → locations.Get-miss
// → locations.ErrNotFound. That's "the code resolved to nothing" → 404, not a
// 500. Constructed by deleting a location then re-reserving its code at the
// now-gone id (a real dangling entry the happy path can't produce).
func TestViaDanglingReference404(t *testing.T) {
	srv, ls := newTestServerWithLocations(t)
	loc := &locations.Location{Label: "Ghost"}
	if err := ls.Create(loc); err != nil {
		t.Fatal(err)
	}
	code, id := loc.ViaCode, loc.ID
	if err := ls.Delete(id); err != nil {
		t.Fatal(err) // happy path releases the via code
	}
	// Re-create a DANGLING index entry: code → the deleted location's id.
	if err := srv.via.Reserve(code, via.TypeLocation, id); err != nil {
		t.Fatalf("re-reserve dangling: %v", err)
	}
	if rr := get(srv, "/via/"+code); rr.Code != http.StatusNotFound {
		t.Errorf("via dangling ref = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// TestPartLabelSVG pins POST /parts/{id}/label renders an SVG, by id AND by
// via-code.
func TestPartLabelSVG(t *testing.T) {
	srv, _ := newTestServerWithLocations(t)
	p := restCreate(t, srv, `{"MPN":"LBL","Description":"desc","PartType":"local"}`)
	checkLabel(t, post(srv, "/parts/"+p.ID+"/label", ""), p.ViaCode)
	checkLabel(t, post(srv, "/parts/"+p.ViaCode+"/label", ""), p.ViaCode)
}

// TestLocationLabelSVG pins POST /locations/{id}/label, by id AND via-code.
func TestLocationLabelSVG(t *testing.T) {
	srv, ls := newTestServerWithLocations(t)
	loc := &locations.Location{Label: "Bin Z"}
	if err := ls.Create(loc); err != nil {
		t.Fatal(err)
	}
	checkLabel(t, post(srv, "/locations/"+loc.ID+"/label", ""), loc.ViaCode)
	checkLabel(t, post(srv, "/locations/"+loc.ViaCode+"/label", ""), loc.ViaCode)
}

// TestLabelUnknown404 pins a missing entity on a label endpoint → 404.
func TestLabelUnknown404(t *testing.T) {
	srv, _ := newTestServerWithLocations(t)
	if rr := post(srv, "/parts/no-such/label", ""); rr.Code != http.StatusNotFound {
		t.Errorf("label unknown part = %d, want 404", rr.Code)
	}
	if rr := post(srv, "/locations/no-such/label", ""); rr.Code != http.StatusNotFound {
		t.Errorf("label unknown location = %d, want 404", rr.Code)
	}
}

// --- Slice 6: via-resolver browser redirect (content-negotiation) ----------

// viaGet is a GET /via/{code} with a caller-set Accept header (content-
// negotiation: text/html → browser redirect; else JSON).
func viaGet(srv *Server, code, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/via/"+code, nil)
	req.Header.Set("Accept", accept)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

// TestViaBrowserRedirect pins the Slice-6 content-negotiation: a browser (Accept
// text/html) scanning /via/{code} is 303-redirected to the entity's UI deep-link
// (part → /ui/?part=, location → /ui/locations?loc=); an API client (Accept
// json or curl's */*) still gets JSON; an unknown code → 404 either way. This
// keeps the slice-4 QR (/via/{code}) browser-friendly without a separate
// endpoint or re-cutting labels.
func TestViaBrowserRedirect(t *testing.T) {
	srv, ls := newTestServerWithLocations(t)
	loc := &locations.Location{Label: "RB"}
	if err := ls.Create(loc); err != nil {
		t.Fatal(err)
	}
	p := restCreate(t, srv, `{"MPN":"RB","PartType":"local"}`)

	// location, browser → /ui/locations?loc={id}
	rr := viaGet(srv, loc.ViaCode, "text/html,application/xhtml+xml")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("via location (html) = %d, want 303; body=%s", rr.Code, rr.Body)
	}
	if got := rr.Header().Get("Location"); !strings.Contains(got, "/ui/locations?loc=") {
		t.Errorf("location redirect = %q, want /ui/locations?loc=...", got)
	}
	// part, browser → /ui/?part={id}
	rr2 := viaGet(srv, p.ViaCode, "text/html")
	if rr2.Code != http.StatusSeeOther || !strings.Contains(rr2.Header().Get("Location"), "/ui/?part=") {
		t.Fatalf("via part (html) = %d %q, want 303 /ui/?part=", rr2.Code, rr2.Header().Get("Location"))
	}
	// API client (json) → still JSON
	rr3 := viaGet(srv, p.ViaCode, "application/json")
	if rr3.Code != http.StatusOK || !strings.Contains(rr3.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("via part (json) = %d %s, want 200 json", rr3.Code, rr3.Header().Get("Content-Type"))
	}
	// curl-style */* → JSON (only browsers redirect)
	rr4 := viaGet(srv, p.ViaCode, "*/*")
	if rr4.Code != http.StatusOK {
		t.Fatalf("via part (*/*) = %d, want 200 json (curl gets JSON, only browsers redirect)", rr4.Code)
	}
	// unknown → 404 (browser + api)
	if rr := viaGet(srv, "L-NOPE", "text/html"); rr.Code != http.StatusNotFound {
		t.Errorf("via unknown (html) = %d, want 404", rr.Code)
	}
	if rr := viaGet(srv, "L-NOPE", "application/json"); rr.Code != http.StatusNotFound {
		t.Errorf("via unknown (json) = %d, want 404", rr.Code)
	}
}

// TestCreate_ZeroesCreatedBy (RedTeam C19/evolution-v2): handleCreate MUST zero
// CreatedBy from the request body so a caller cannot spoof the audit trail —
// the store's "local" default (or a future auth layer) is authoritative, like
// ID/Version/timestamps. Before the fix, POST {"CreatedBy":"attacker"} persisted
// verbatim.
func TestCreate_ZeroesCreatedBy(t *testing.T) {
	srv := newTestServer(t)
	rr := post(srv, "/parts", `{"MPN":"SPOOF","PartType":"local","CreatedBy":"attacker"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var p parts.Part
	if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.CreatedBy != "local" {
		t.Errorf("CreatedBy = %q, want \"local\" (server-assigned; a caller must not spoof the audit trail)", p.CreatedBy)
	}
}

// TestPatch_RFC7396ClearsNull (RedTeam HIGH) pins the RFC 7396 JSON Merge Patch
// semantics that replace the zero-means-skip rule: a JSON null CLEARS a field
// (was: silently dropped). Description/ReorderPoint/DefaultLocationID all clear.
func TestPatch_RFC7396ClearsNull(t *testing.T) {
	srv, ls := newTestServerWithLocations(t)
	loc := &locations.Location{Label: "CLR-loc"}
	if err := ls.Create(loc); err != nil {
		t.Fatal(err)
	}
	p := restCreate(t, srv, `{"MPN":"CLR","PartType":"local","Description":"to-clear","ReorderPoint":10,"DefaultLocationID":"`+loc.ID+`"}`)
	etag := strconv.Quote(strconv.Itoa(p.Version))
	rr := patch(srv, "/parts/"+p.ID, `{"Description":null,"ReorderPoint":null,"DefaultLocationID":null}`, etag)
	if rr.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rr.Code, rr.Body.String())
	}
	got, _ := srv.store.Get(p.ID)
	if got.Description != "" {
		t.Errorf("Description = %q, want \"\" (cleared)", got.Description)
	}
	if got.ReorderPoint != 0 {
		t.Errorf("ReorderPoint = %d, want 0 (cleared)", got.ReorderPoint)
	}
	if got.DefaultLocationID != "" {
		t.Errorf("DefaultLocationID = %q, want \"\" (cleared)", got.DefaultLocationID)
	}
}

// --- RedTeam: REST locations CRUD -----------------------------------------

func TestLocationList(t *testing.T) {
	srv, ls := newTestServerWithLocations(t)
	if err := ls.Create(&locations.Location{Label: "A"}); err != nil {
		t.Fatal(err)
	}
	rr := get(srv, "/locations")
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"Label":"A"`) {
		t.Errorf("list missing A; body: %s", rr.Body)
	}
}

func TestLocationGet(t *testing.T) {
	srv, ls := newTestServerWithLocations(t)
	loc := &locations.Location{Label: "G"}
	if err := ls.Create(loc); err != nil {
		t.Fatal(err)
	}
	rr := get(srv, "/locations/"+loc.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("get = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), loc.ID) {
		t.Errorf("get missing id; body: %s", rr.Body)
	}
	if get(srv, "/locations/no-such").Code != http.StatusNotFound {
		t.Error("get unknown = want 404")
	}
}

func TestLocationCreateREST(t *testing.T) {
	srv := newTestServer(t)
	rr := post(srv, "/locations", `{"Label":"REST-bin","Notes":"test"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var l locations.Location
	if err := json.Unmarshal(rr.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	if l.Label != "REST-bin" {
		t.Errorf("Label = %q", l.Label)
	}
	if l.CreatedBy != "local" {
		t.Errorf("CreatedBy = %q, want local (server-assigned)", l.CreatedBy)
	}
	if l.ViaCode == "" {
		t.Error("no ViaCode assigned")
	}
}

func TestLocationPatch(t *testing.T) {
	srv, ls := newTestServerWithLocations(t)
	loc := &locations.Location{Label: "P"}
	if err := ls.Create(loc); err != nil {
		t.Fatal(err)
	}
	etag := strconv.Quote(strconv.Itoa(loc.Version))
	rr := patch(srv, "/locations/"+loc.ID, `{"Notes":"patched"}`, etag)
	if rr.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rr.Code, rr.Body.String())
	}
	got, _ := ls.Get(loc.ID)
	if got.Notes != "patched" {
		t.Errorf("Notes = %q, want patched", got.Notes)
	}
}

func TestLocationDeleteREST(t *testing.T) {
	srv, ls := newTestServerWithLocations(t)
	loc := &locations.Location{Label: "D"}
	if err := ls.Create(loc); err != nil {
		t.Fatal(err)
	}
	// Assign a part → delete refuses (has-parts → 409).
	p := restCreate(t, srv, `{"MPN":"DP","PartType":"local","DefaultLocationID":"`+loc.ID+`"}`)
	if rr := deleteReq(srv, "/locations/"+loc.ID); rr.Code != http.StatusConflict {
		t.Errorf("delete with parts = %d, want 409", rr.Code)
	}
	// Clear the part's location (RFC 7396 null) → delete succeeds.
	patch(srv, "/parts/"+p.ID, `{"DefaultLocationID":null}`, strconv.Quote(strconv.Itoa(p.Version)))
	rr2 := deleteReq(srv, "/locations/"+loc.ID)
	if rr2.Code != http.StatusNoContent {
		t.Fatalf("delete after clear = %d, want 204; body: %s", rr2.Code, rr2.Body.String())
	}
}
