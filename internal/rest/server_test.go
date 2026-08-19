// Package rest exposes go-parts' parts.Store + FTS over a stdlib net/http
// ServeMux (Go 1.26 method-patterns). These tests drive every route end-to-end
// against a real Pebble + FTS + Store — no mocks — so the §5.14 concurrency
// safety built into Store.Update is exercised through the HTTP layer that will
// be the first multi-writer surface in production.
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
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/components"
	"github.com/madeinoz67/go-parts/internal/index"
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
	ls := locations.NewStore(db, vs)
	cs := components.NewStore(db, store)
	return NewServer(store, fts, vs, ls, cs)
}

// newTestServerWithLocations wires a REST server over the same store wiring as
// newTestServer and also returns the locations.Store for tests that drive the
// via resolver or location CRUD. Flat-locations model: the policy seam is
// gone; this helper exists for tests that need the locations handle.
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
	cs := components.NewStore(db, ps)
	return NewServer(ps, fts, vs, ls, cs), ls
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
	// Schema v5: a second part with the same MPN is a 409 conflict.
	if rr := post(srv, "/parts", `{"MPN":"S1","PartType":"local"}`); rr.Code != http.StatusConflict {
		t.Fatalf("duplicate MPN create = %d, want 409", rr.Code)
	}
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
// AdjustStock/component-stock exclusive domain (§5.14). Store.Update enforces
// this server-side; this test proves the REST handler doesn't bypass it. The
// AdjustStock endpoint was removed in the flat-locations redesign (stock is
// now Component-driven); the create body's QtyOnHand is the only way to set
// stock from REST, and PATCH still MUST NOT change it.
func TestPatchPreservesStock(t *testing.T) {
	srv := newTestServer(t)
	rr := post(srv, "/parts", `{"MPN":"SKU1","PartType":"local","Description":"cap","QtyOnHand":5}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201", rr.Code)
	}
	id := extractID(t, rr.Body.Bytes())
	etag := rr.Header().Get("ETag")

	// PATCH with a bogus QtyOnHand in the body — the F3 invariant says it
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
	if got.QtyOnHand != 5 {
		t.Fatalf("PATCH leaked stock change: QtyOnHand = %d, want 5 (create-time value preserved)", got.QtyOnHand)
	}
	if got.Description != "cap-edited" {
		t.Fatalf("PATCH didn't apply Description: got %q, want %q", got.Description, "cap-edited")
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

// restCreate POSTs a part body and returns the decoded stored record (with the
// server-assigned ID + Version).
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
// location's code returns the location WITH its embedded Components. Flat-
// locations model: a location's contents are its Components (one per part per
// location, with quantity + history).
func TestViaResolvesLocationWithContents(t *testing.T) {
	srv, ls := newTestServerWithLocations(t)
	bin := &locations.Location{Label: "Bin"}
	if err := ls.Create(bin); err != nil {
		t.Fatal(err)
	}
	// Two parts, each with a Component at bin via the components store.
	pa := restCreate(t, srv, `{"MPN":"a","PartType":"local"}`)
	pb := restCreate(t, srv, `{"MPN":"b","PartType":"local"}`)
	if err := srv.components.Add(bin.ID, pa.ID, 1, nil); err != nil {
		t.Fatalf("Add component a: %v", err)
	}
	if err := srv.components.Add(bin.ID, pb.ID, 1, nil); err != nil {
		t.Fatalf("Add component b: %v", err)
	}
	rr := get(srv, "/via/"+bin.ViaCode)
	if rr.Code != http.StatusOK {
		t.Fatalf("via = %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"Type":"location"`) {
		t.Errorf("via body missing Type=location: %s", body)
	}
	// 2 components embedded (each carries the partID it stocks).
	if c := strings.Count(body, `"PartID"`); c != 2 {
		t.Errorf("via body has %d Component PartID fields, want 2 (contents embedded): %s", c, body)
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
// (was: silently dropped). Description/ReorderPoint/Tags all clear.
func TestPatch_RFC7396ClearsNull(t *testing.T) {
	srv := newTestServer(t)
	p := restCreate(t, srv, `{"MPN":"CLR","PartType":"local","Description":"to-clear","ReorderPoint":10,"Tags":["x"]}`)
	etag := strconv.Quote(strconv.Itoa(p.Version))
	rr := patch(srv, "/parts/"+p.ID, `{"Description":null,"ReorderPoint":null,"Tags":null}`, etag)
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
	if len(got.Tags) != 0 {
		t.Errorf("Tags = %v, want nil (cleared)", got.Tags)
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
	// Stock the location with a Component → delete refuses (has-components → 409).
	p := restCreate(t, srv, `{"MPN":"DP","PartType":"local"}`)
	if err := srv.components.Add(loc.ID, p.ID, 1, nil); err != nil {
		t.Fatalf("components.Add: %v", err)
	}
	if rr := deleteReq(srv, "/locations/"+loc.ID); rr.Code != http.StatusConflict {
		t.Errorf("delete with components = %d, want 409", rr.Code)
	}
	// Remove the Component → delete succeeds.
	if err := srv.components.Remove(loc.ID, p.ID); err != nil {
		t.Fatalf("components.Remove: %v", err)
	}
	rr2 := deleteReq(srv, "/locations/"+loc.ID)
	if rr2.Code != http.StatusNoContent {
		t.Fatalf("delete after remove = %d, want 204; body: %s", rr2.Code, rr2.Body.String())
	}
}

// --- repo-wide CSRF posture: uniform same-origin Origin guard (follow-up (b)) ---

// originReq drives srv with a request carrying an Origin header — the drive-by
// browser shape the guard exists for (attacker page, CORS-"simple" request).
func originReq(srv *Server, method, path, body, origin string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	req.Header.Set("Origin", origin)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

// TestOriginGuardBlocksCrossOriginMutations pins the repo-wide CSRF posture on
// REST (final-review follow-up (b)): a browser-issued Origin that is not the
// request's own scheme://host is a drive-by cross-site fire and gets 403 on
// every mutating route. This closes the text/plain-fetch vector — REST never
// enforced Content-Type, so a CORS-"simple" request can carry a JSON body the
// decoder happily parses; the ui.auth comment's old "REST is unaffected —
// JSON triggers preflight" rationale had exactly this hole. The guard fires
// before the handler, so placeholder ids suffice for the id-bearing rows.
func TestOriginGuardBlocksCrossOriginMutations(t *testing.T) {
	srv := newTestServer(t)
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/parts", `{"MPN":"EVIL"}`},
		{http.MethodPatch, "/parts/01PLACEHOLDER", `{"Description":"evil"}`},
		{http.MethodDelete, "/parts/01PLACEHOLDER", ""},
		{http.MethodPost, "/locations", `{"Label":"EVIL"}`},
		{http.MethodDelete, "/locations/01PLACEHOLDER", ""},
		{http.MethodPost, "/locations/01PLACEHOLDER/components", `{"PartID":"x","Quantity":1}`},
	}
	for _, tc := range cases {
		rr := originReq(srv, tc.method, tc.path, tc.body, "http://evil.example")
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s %s with foreign Origin = %d, want 403", tc.method, tc.path, rr.Code)
		}
	}
	// And nothing was written by the blocked POST.
	if got := srv.store.Count(); got != 0 {
		t.Fatalf("blocked cross-origin create must not write: count=%d", got)
	}
}

// TestOriginGuardAllowsNoOriginAndSameOrigin pins the pass side: curl/scripts
// send no Origin (pass — not CSRF vectors), and a legitimate same-origin
// browser client's Origin equals the request's scheme://host (httptest
// defaults the host to example.com, scheme http).
func TestOriginGuardAllowsNoOriginAndSameOrigin(t *testing.T) {
	srv := newTestServer(t)
	if rr := post(srv, "/parts", `{"MPN":"NOORIGIN"}`); rr.Code != http.StatusCreated {
		t.Fatalf("no-Origin POST = %d, want 201 (not a CSRF vector)", rr.Code)
	}
	if rr := originReq(srv, http.MethodPost, "/parts", `{"MPN":"SAMEORIGIN"}`, "http://example.com"); rr.Code != http.StatusCreated {
		t.Fatalf("same-Origin POST = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
}

// TestOriginGuardGETUnaffected: reads are not mutations — a foreign Origin on
// a GET passes (same posture as ui/mcp: nothing to forge on a read).
func TestOriginGuardGETUnaffected(t *testing.T) {
	srv := newTestServer(t)
	if rr := originReq(srv, http.MethodGet, "/parts", "", "http://evil.example"); rr.Code != http.StatusOK {
		t.Fatalf("GET with foreign Origin = %d, want 200 (reads unguarded)", rr.Code)
	}
}

// --- TUI companion: browse-all + low filters (spec §3) ---

func TestSearchAllListsCorpus(t *testing.T) {
	srv := newTestServer(t)
	post(srv, "/parts", `{"MPN":"A1","PartType":"local","QtyOnHand":50,"ReorderPoint":5}`)
	post(srv, "/parts", `{"MPN":"B2","PartType":"local","QtyOnHand":2,"ReorderPoint":5}`)
	rr := get(srv, "/parts?all=1")
	if rr.Code != http.StatusOK {
		t.Fatalf("all=1 = %d, want 200", rr.Code)
	}
	var pts []parts.Part
	if err := json.Unmarshal(rr.Body.Bytes(), &pts); err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 {
		t.Fatalf("all=1 must list the corpus, got %d: %s", len(pts), rr.Body.String())
	}
}

func TestSearchAllLowFilters(t *testing.T) {
	srv := newTestServer(t)
	post(srv, "/parts", `{"MPN":"A1","PartType":"local","QtyOnHand":50,"ReorderPoint":5}`) // fine
	post(srv, "/parts", `{"MPN":"B2","PartType":"local","QtyOnHand":2,"ReorderPoint":5}`)  // low
	post(srv, "/parts", `{"MPN":"C3","PartType":"local","QtyOnHand":0,"ReorderPoint":0}`)  // 0/0 = LOW
	rr := get(srv, "/parts?all=1&low=1")
	var pts []parts.Part
	if err := json.Unmarshal(rr.Body.Bytes(), &pts); err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 || pts[0].MPN == "A1" {
		t.Fatalf("low must keep qty<=reorder incl. 0/0 (ui/mcp parity): %s", rr.Body.String())
	}
}

func TestSearchBareStillEmpty(t *testing.T) {
	srv := newTestServer(t)
	post(srv, "/parts", `{"MPN":"A1","PartType":"local"}`)
	rr := get(srv, "/parts")
	body := strings.TrimSpace(rr.Body.String())
	if body != "null" && body != "[]" {
		t.Fatalf("bare GET /parts behavior must be unchanged (empty), got %q", body)
	}
}

// TestSearchQLowComposition (Task-1 review carry-in): low composes with q —
// the query narrows to its hit set, THEN low keeps only QtyOnHand <=
// ReorderPoint within it, so q=<low part's MPN> keeps its one hit and
// q=<high part's MPN> drops it to zero.
func TestSearchQLowComposition(t *testing.T) {
	srv := newTestServer(t)
	post(srv, "/parts", `{"MPN":"QLOWONE","PartType":"local","QtyOnHand":2,"ReorderPoint":5}`)  // low
	post(srv, "/parts", `{"MPN":"QLOWTEN","PartType":"local","QtyOnHand":50,"ReorderPoint":5}`) // fine
	rr := get(srv, "/parts?q=QLOWONE&low=1")
	var pts []parts.Part
	if err := json.Unmarshal(rr.Body.Bytes(), &pts); err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 || pts[0].MPN != "QLOWONE" {
		t.Fatalf("q=low-part&low=1 must return exactly the low hit, got %d: %s", len(pts), rr.Body.String())
	}
	rr = get(srv, "/parts?q=QLOWTEN&low=1")
	var again []parts.Part
	if err := json.Unmarshal(rr.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("q=high-part&low=1 must return 0 hits, got %d: %s", len(again), rr.Body.String())
	}
}

// TestSearchZeroHitRendersNull (Task-1 review carry-in): a q matching nothing
// renders body "null" (nil slice through Encode) — pinned exactly, so a future
// switch to "[]" is a conscious shape change, not an accident.
func TestSearchZeroHitRendersNull(t *testing.T) {
	srv := newTestServer(t)
	rr := get(srv, "/parts?q=definitelynomatch")
	if rr.Code != http.StatusOK {
		t.Fatalf("zero-hit q = %d, want 200", rr.Code)
	}
	if got := strings.TrimSpace(rr.Body.String()); got != "null" {
		t.Fatalf("zero-hit body = %q, want %q (today's shape)", got, "null")
	}
}

// --- TUI companion: stock aggregate (spec 2026-08-19-tui §3) ---------------

func TestPartStockAggregate(t *testing.T) {
	srv, ls := newTestServerWithLocations(t)
	bin := &locations.Location{Label: "Drawer A1"}
	if err := ls.Create(bin); err != nil {
		t.Fatal(err)
	}
	p := restCreate(t, srv, `{"MPN":"STK1","PartType":"local","Description":"agg"}`)
	if err := srv.components.Add(bin.ID, p.ID, 50, nil); err != nil {
		t.Fatal(err)
	}
	if err := srv.components.AdjustQty(bin.ID, p.ID, -5, "bench"); err != nil {
		t.Fatal(err)
	}
	rr := get(srv, "/parts/"+p.ID+"/stock")
	if rr.Code != http.StatusOK {
		t.Fatalf("stock = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	b := rr.Body.String()
	for _, want := range []string{`"MPN":"STK1"`, `"Label":"Drawer A1"`, `"Quantity":45`, `"Delta":-5`, `"Reason":"bench"`, `"Reason":"initial"`} {
		if !strings.Contains(b, want) {
			t.Errorf("stock aggregate missing %s: %s", want, b)
		}
	}
	// newest-first: bench (-5) appears before initial (+50).
	if strings.Index(b, `"Reason":"bench"`) > strings.Index(b, `"Reason":"initial"`) {
		t.Errorf("movements must be newest-first: %s", b)
	}
	// Via-code resolution: the bin's L- code rides the stock entry.
	if !strings.Contains(b, bin.ViaCode) {
		t.Errorf("stock entry must carry the location via-code: %s", b)
	}
	// Task 8 (LocationID wire addition): the bin's ID rides the stock entry
	// too — the TUI adjust overlay PATCHes
	// /locations/{id}/components/{partId}, and that path takes the id (the
	// label/via-code alone cannot address the write).
	if !strings.Contains(b, `"LocationID":"`+bin.ID+`"`) {
		t.Errorf("stock entry must carry the location id: %s", b)
	}
}

func TestPartStockByViaCodeAnd404(t *testing.T) {
	srv, _ := newTestServerWithLocations(t)
	p := restCreate(t, srv, `{"MPN":"STK2","PartType":"local"}`)
	if rr := get(srv, "/parts/"+p.ViaCode+"/stock"); rr.Code != http.StatusOK {
		t.Fatalf("stock by P- via-code = %d, want 200", rr.Code)
	}
	if rr := get(srv, "/parts/nope/stock"); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown = %d, want 404", rr.Code)
	}
}
