package ui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/components"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
	"github.com/madeinoz67/go-parts/internal/via"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := pebble.Open(filepath.Join(t.TempDir(), "p"), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	fts := index.NewFTS(db)
	vs := via.NewStore(db)
	ps := parts.NewStore(db, fts, vs)
	ls := locations.NewStore(db, vs)
	cs := components.NewStore(db, ps)
	return NewServer(ps, fts, ls, cs)
}

func TestStaticAssetsServe(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{
		"/ui/static/js/htmx.min.js",
		"/ui/static/fonts/jetbrains-mono-400.woff2",
		"/ui/static/fonts/ibm-plex-mono-600.woff2",
	} {
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		if rr.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rr.Code)
		}
	}
}

func TestShellServes(t *testing.T) {
	srv := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /ui/ = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "<!DOCTYPE html>") {
		t.Fatal("shell response is not an HTML document")
	}
}

func TestShellStructure(t *testing.T) {
	srv := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/", nil))
	body := rr.Body.String()
	for _, want := range []string{
		`id="search"`,               // search input (now in the header)
		`id="parts-tbody"`,          // table body target for htmx
		`id="detail-panel"`,         // detail panel target
		`hx-get="/ui/parts/search"`, // htmx live-filter wiring
		`<header`,                   // header band (wordmark + search + new)
		`<nav class="topnav"`,       // topnav band (six tabs)
		`go-parts`,                  // wordmark
		`-- NORMAL --`,              // footer mode segment
		`parts indexed`,             // footer count segment
		`id="selectAll"`,            // slice 4a — thead select-all checkbox
		`id="bulkBar"`,              // slice 4a — bulk-action bar
		`role="dialog"`,             // hardening — confirm overlay ARIA
		`aria-modal="true"`,         // hardening — confirm overlay ARIA
	} {
		if !strings.Contains(body, want) {
			t.Errorf("shell missing %q", want)
		}
	}
}

func TestCSSServes(t *testing.T) {
	srv := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/static/css/styles.css", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET styles.css = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, token := range []string{"--bg", "--copper", "--phosphor", "--surface", "--text"} {
		if !strings.Contains(body, token) {
			t.Errorf("styles.css missing token %q", token)
		}
	}
}

func TestSearchReturnsMatchingRow(t *testing.T) {
	srv := newTestServer(t)
	srv.store.Create(&parts.Part{MPN: "RC0805FR-0710KL", Description: "10k resistor", PartType: "linked", Tags: []string{"resistor"}})
	srv.store.Create(&parts.Part{MPN: "STM32F401", Description: "mcu", PartType: "linked"})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/parts/search?q=10k", nil))
	body := rr.Body.String()
	if !strings.Contains(body, "RC0805FR-0710KL") {
		t.Errorf("search missing the 10k resistor; body=%s", body)
	}
	if strings.Contains(body, "STM32F401") {
		t.Errorf("search leaked the non-matching mcu")
	}
}

func TestSearchEmptyQueryReturnsAll(t *testing.T) {
	srv := newTestServer(t)
	srv.store.Create(&parts.Part{MPN: "X1", PartType: "local"})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/parts/search", nil)) // no q
	if !strings.Contains(rr.Body.String(), "X1") {
		t.Errorf("empty-query search should list all parts")
	}
}

func TestSearchNoMatchesMessage(t *testing.T) {
	srv := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/parts/search?q=zzzz", nil))
	if !strings.Contains(rr.Body.String(), `no matches for "zzzz"`) {
		t.Errorf("missing no-matches empty state; body=%s", rr.Body.String())
	}
}

func TestDetailKnownPart(t *testing.T) {
	srv := newTestServer(t)
	p := &parts.Part{MPN: "C0805C104J5", Description: "100nF cap", PartType: "linked", Category: "capacitors", QtyOnHand: 42}
	srv.store.Create(p)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/parts/"+p.ID, nil))
	body := rr.Body.String()
	for _, want := range []string{"C0805C104J5", "100nF cap", "42"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q; body=%s", want, body)
		}
	}
}

func TestDetailUnknownIs404(t *testing.T) {
	srv := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/parts/nope", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown part = %d, want 404", rr.Code)
	}
}

func TestCreateReturnsNewRow(t *testing.T) {
	srv := newTestServer(t)
	form := strings.NewReader("mpn=NEW123&description=created&part_type=local")
	req := httptest.NewRequest("POST", "/ui/parts", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("create = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "NEW123") {
		t.Errorf("create response should contain the new row's MPN; body=%s", rr.Body.String())
	}
	// And the part was actually created in the store.
	hits := srv.fts.Search([8]byte{}, "NEW123", 10)
	if len(hits) != 1 {
		t.Errorf("create did not index the new part (hits=%d)", len(hits))
	}
}

// TestCreateClearsDetailPanelOOB locks in the OOB swap that clears the detail
// panel after a successful create. Both create paths (normal form + confirm
// footprint) leave #detail-panel holding stale content (the form / the confirm
// prompt) because the create form's hx-target is #parts-tbody, not the panel.
// row-created.html ships an OOB <section id="detail-panel" hx-swap-oob> that
// resets the panel to the "select a part" hint. This test would fail against
// the old row.html response (no OOB marker, no panel reset).
func TestCreateClearsDetailPanelOOB(t *testing.T) {
	srv := newTestServer(t)
	// Normal create path (known footprint 0805 → no confirm).
	rr := newTestServerRecorder(t, srv, postForm("POST", "/ui/parts", "mpn=OOB1&part_type=local&footprint=0805"))
	body := rr.Body.String()
	if !strings.Contains(body, `id="detail-panel"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Errorf("normal create should ship an OOB #detail-panel clear; body=%s", body)
	}
	if !strings.Contains(body, "select a part") {
		t.Errorf("OOB panel should reset to the 'select a part' hint; body=%s", body)
	}
}

// TestCreateConfirmClearsDetailPanelOOB is the bug-report case: after the
// confirm-footprint prompt is accepted, the prompt (which lived in
// #detail-panel) must be cleared. The confirm POST re-posts footprint_confirmed
// → handleCreate saves and returns row-created.html (row + OOB panel-clear).
func TestCreateConfirmClearsDetailPanelOOB(t *testing.T) {
	srv := newTestServer(t)
	rr := newTestServerRecorder(t, srv, postForm("POST", "/ui/parts", "mpn=OOB2&part_type=local&footprint=TYPOOOB&footprint_confirmed=true"))
	if rr.Code != http.StatusOK {
		t.Fatalf("confirmed create = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `id="detail-panel"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Errorf("confirmed create should ship an OOB #detail-panel clear; body=%s", body)
	}
	if strings.Contains(body, "confirm-footprint") {
		t.Errorf("confirmed create must not leak the stale confirm prompt; body=%s", body)
	}
}

// newTestServerRecorder runs req against srv and returns the recorder, purely
// to keep the OOB tests' assertion blocks focused on the body rather than the
// boilerplate.
func newTestServerRecorder(t *testing.T, srv *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

// TestEditRefreshesTagSidebar pins the parts side of "tags are not refreshed
// if a tag is changed — page needs a manual refresh": handleEdit's success
// response must ship an OOB #tag-nav swap alongside the updated detail, so a
// tag change lands in the sidebar live (same class as the Storage edit fix).
func TestEditRefreshesTagSidebar(t *testing.T) {
	srv := newTestServer(t)
	p := uiCreatePart(t, srv, "TAGEDIT1")
	body := "version=" + fmt.Sprintf("%d", p.Version) + "&tags=freshtag"
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/parts/"+p.ID, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("edit = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	b := rr.Body.String()
	if !strings.Contains(b, `id="tag-nav"`) || !strings.Contains(b, `hx-swap-oob="true"`) {
		t.Errorf("edit success should ship an OOB #tag-nav refresh; body=%s", b)
	}
	if !strings.Contains(b, "freshtag") {
		t.Errorf("refreshed sidebar should list the new tag; body=%s", b)
	}
}

// TestCreateRefreshesTagSidebar — the create path (row-created.html) ships the
// same OOB tag-nav refresh, so a part created with a new tag updates the facet
// without a manual reload.
func TestCreateRefreshesTagSidebar(t *testing.T) {
	srv := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/parts", "mpn=NEWTAG1&part_type=local&tags=newtag1"))
	if rr.Code != http.StatusOK {
		t.Fatalf("create = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	b := rr.Body.String()
	if !strings.Contains(b, `id="tag-nav"`) || !strings.Contains(b, `hx-swap-oob="true"`) {
		t.Errorf("create success should ship an OOB #tag-nav refresh; body=%s", b)
	}
	if !strings.Contains(b, "newtag1") {
		t.Errorf("refreshed sidebar should list the new tag; body=%s", b)
	}
}

func TestCreateFormRenders(t *testing.T) {
	srv := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/parts/new", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /ui/parts/new = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{`hx-post="/ui/parts"`, `name="mpn"`, `name="description"`} {
		if !strings.Contains(body, want) {
			t.Errorf("create form missing %q; body=%s", want, body)
		}
	}
}

func TestEditUpdatesDetail(t *testing.T) {
	srv := newTestServer(t)
	p := &parts.Part{MPN: "EDIT1", Description: "orig", PartType: "local"}
	srv.store.Create(p)
	form := strings.NewReader("version=" + fmt.Sprintf("%d", p.Version) + "&description=edited")
	req := httptest.NewRequest("POST", "/ui/parts/"+p.ID, form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("edit = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "edited") {
		t.Errorf("edit response should show edited description; body=%s", rr.Body.String())
	}
}

func TestEditStaleVersionReturns409(t *testing.T) {
	srv := newTestServer(t)
	p := &parts.Part{MPN: "EDIT2", PartType: "local"}
	srv.store.Create(p)
	// Bump the stored version out from under the form (simulate a concurrent edit).
	p2, _ := srv.store.Get(p.ID)
	p2.Description = "winner"
	srv.store.Update(p2, p.Version) // stored now at version 2
	// The stale form still believes version 1.
	form := strings.NewReader("version=1&description=loser")
	req := httptest.NewRequest("POST", "/ui/parts/"+p.ID, form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("stale edit = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "edited elsewhere") {
		t.Errorf("conflict fragment should say 'edited elsewhere'; body=%s", rr.Body.String())
	}
}

// postForm builds a urlencoded POST request mirroring how the browser submits
// the create/edit forms (application/x-www-form-urlencoded).
func postForm(method, url, body string) *http.Request {
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestCreateUnknownFootprintReturnsConfirm(t *testing.T) {
	srv := newTestServer(t)
	// TYPO123 is not in commonFootprints and the DB is empty → unknown.
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/parts", "mpn=NEW1&part_type=local&footprint=TYPO123&description=d"))
	body := rr.Body.String()
	if !strings.Contains(body, "confirm-footprint") {
		t.Errorf("unknown footprint should render the confirm prompt; body=%s", body)
	}
	if !strings.Contains(body, "TYPO123") {
		t.Errorf("confirm prompt should name the unknown footprint; body=%s", body)
	}
	if !strings.Contains(body, `name="footprint_confirmed"`) {
		t.Errorf("confirm prompt must carry the hidden confirm flag; body=%s", body)
	}
	// Retargeted into #detail-panel (create form's own target is #parts-tbody).
	if got := rr.Header().Get("Hx-Retarget"); got != "#detail-panel" {
		t.Errorf("Hx-Retarget = %q, want #detail-panel", got)
	}
	// The part must NOT have been created.
	if hits := srv.fts.Search([8]byte{}, "NEW1", 10); len(hits) != 0 {
		t.Errorf("unknown-footprint create should not save; got %d hits", len(hits))
	}
	// The original fields are preserved as hidden inputs so confirm re-submit
	// does not lose them.
	if !strings.Contains(body, `name="mpn"`) || !strings.Contains(body, `value="NEW1"`) {
		t.Errorf("confirm prompt should preserve mpn as a hidden input; body=%s", body)
	}
}

func TestCreateConfirmFlagSavesUnknownFootprint(t *testing.T) {
	srv := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/parts", "mpn=NEW2&part_type=local&footprint=TYPO456&footprint_confirmed=true"))
	if rr.Code != http.StatusOK {
		t.Fatalf("confirmed create = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	// Saved — the unknown footprint is now a real part.
	if hits := srv.fts.Search([8]byte{}, "NEW2", 10); len(hits) != 1 {
		t.Errorf("confirmed create should save; got %d hits", len(hits))
	}
}

func TestCreateKnownFootprintSavesDirectly(t *testing.T) {
	srv := newTestServer(t)
	// 0805 is in commonFootprints → no prompt, direct save.
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/parts", "mpn=NEW3&part_type=local&footprint=0805"))
	body := rr.Body.String()
	if strings.Contains(body, "confirm-footprint") {
		t.Errorf("known footprint should not trigger confirm; body=%s", body)
	}
	if hits := srv.fts.Search([8]byte{}, "NEW3", 10); len(hits) != 1 {
		t.Errorf("known-footprint create should save directly; got %d hits", len(hits))
	}
}

func TestCreateEmptyFootprintSavesDirectly(t *testing.T) {
	srv := newTestServer(t)
	// Empty footprint must NOT trigger the guard (footprint is optional).
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/parts", "mpn=NEW4&part_type=local"))
	body := rr.Body.String()
	if strings.Contains(body, "confirm-footprint") {
		t.Errorf("empty footprint should not trigger confirm; body=%s", body)
	}
	if hits := srv.fts.Search([8]byte{}, "NEW4", 10); len(hits) != 1 {
		t.Errorf("empty-footprint create should save; got %d hits", len(hits))
	}
}

func TestEditUnknownFootprintReturnsConfirm(t *testing.T) {
	srv := newTestServer(t)
	p := &parts.Part{MPN: "EDT1", PartType: "local", Footprint: "0805"}
	srv.store.Create(p)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/parts/"+p.ID, "version="+fmt.Sprintf("%d", p.Version)+"&footprint=TYPO789"))
	body := rr.Body.String()
	if !strings.Contains(body, "confirm-footprint") {
		t.Errorf("unknown footprint on edit should render the confirm prompt; body=%s", body)
	}
	if !strings.Contains(body, "TYPO789") {
		t.Errorf("confirm prompt should name the unknown footprint; body=%s", body)
	}
	// Not applied — the stored footprint is unchanged.
	got, _ := srv.store.Get(p.ID)
	if got.Footprint != "0805" {
		t.Errorf("unknown-footprint edit should not save; stored footprint = %q, want 0805", got.Footprint)
	}
}

func TestEditConfirmFlagSavesUnknownFootprint(t *testing.T) {
	srv := newTestServer(t)
	p := &parts.Part{MPN: "EDT2", PartType: "local", Footprint: "0603"}
	srv.store.Create(p)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/parts/"+p.ID,
		"version="+fmt.Sprintf("%d", p.Version)+"&footprint=WEIRD&footprint_confirmed=true"))
	if rr.Code != http.StatusOK {
		t.Fatalf("confirmed edit = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	got, _ := srv.store.Get(p.ID)
	if got.Footprint != "WEIRD" {
		t.Errorf("confirmed edit should save the unknown footprint; got %q", got.Footprint)
	}
}

// TestSearchRowShowsPackageAndStatus locks in slice 2's table columns: the
// row fragment must surface Footprint (Package column) and a status badge,
// and must no longer carry a Category column header. The layout thead is
// rendered once on /ui/ but the row fragment is what search swaps in — since
// search returns a fresh <tbody id="parts-tbody"> (rows.html) the header lives
// only in layout.html; this test asserts the row body carries the new cells.
// The Category-header check guards against a stale layout thead regressing.
func TestSearchRowShowsPackageAndStatus(t *testing.T) {
	srv := newTestServer(t)
	srv.store.Create(&parts.Part{MPN: "R1", Description: "10k", PartType: "local", Footprint: "0805", QtyOnHand: 50, ReorderPoint: 10})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/parts/search", nil))
	body := rr.Body.String()
	for _, want := range []string{"0805", "badge-ok", ">OK<"} {
		if !strings.Contains(body, want) {
			t.Errorf("row missing %q; body=%s", want, body)
		}
	}
	// Layout thead must not surface a Category column anymore — fetch the shell.
	shellRR := httptest.NewRecorder()
	srv.ServeHTTP(shellRR, httptest.NewRequest("GET", "/ui/", nil))
	shellBody := shellRR.Body.String()
	if strings.Contains(shellBody, "<th>Category</th>") {
		t.Errorf("Category column should be gone; shell=%s", shellBody)
	}
}

// TestSearchRowLowStockBadge pins the LOW branch of the shared status-badge
// partial: a part at/below ReorderPoint renders badge-warn + LOW text.
func TestSearchRowLowStockBadge(t *testing.T) {
	srv := newTestServer(t)
	srv.store.Create(&parts.Part{MPN: "LOW1", PartType: "local", QtyOnHand: 2, ReorderPoint: 10})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/parts/search", nil))
	body := rr.Body.String()
	if !strings.Contains(body, "badge-warn") || !strings.Contains(body, ">LOW<") {
		t.Errorf("low-stock row should show a LOW warn badge; body=%s", body)
	}
}

// TestSortByFootprint covers the new footprint sort case in applySort — asc
// must put "0805" before "SOT-23" in the rendered rows.
func TestSortByFootprint(t *testing.T) {
	srv := newTestServer(t)
	srv.store.Create(&parts.Part{MPN: "B", PartType: "local", Footprint: "SOT-23"})
	srv.store.Create(&parts.Part{MPN: "A", PartType: "local", Footprint: "0805"})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/parts/search?sort=footprint&dir=asc", nil))
	body := rr.Body.String()
	if !strings.Contains(body, "0805") || strings.Index(body, "0805") > strings.Index(body, "SOT-23") {
		t.Errorf("asc footprint sort should put 0805 before SOT-23; body=%s", body)
	}
}

// TestTagFilter pins slice 3b's ?tag= filter: a tag query returns only parts
// carrying that tag. The filter is applied after the empty-q list path (the
// tag sidebar hits /ui/parts/search?tag=resistor with no q), so without the
// filteredParts helper's tag branch the resistor query would leak the
// capacitor row.
func TestTagFilter(t *testing.T) {
	srv := newTestServer(t)
	srv.store.Create(&parts.Part{MPN: "R", PartType: "local", Tags: []string{"resistor"}})
	srv.store.Create(&parts.Part{MPN: "C", PartType: "local", Tags: []string{"capacitor"}})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/parts/search?tag=resistor", nil))
	body := rr.Body.String()
	if !strings.Contains(body, "R") || strings.Contains(body, ">C<") {
		t.Errorf("?tag=resistor should show only the resistor; body=%s", body)
	}
}

// TestShellRendersTagSidebar pins slice 3b's shell-side render: handleShell
// must surface Store.TagCounts() through the tag-nav.html partial so the
// sidebar lists each tag with its count. Without the Tags field on the shell
// data + the partial invoke in layout.html, the sidebar stays empty.
func TestShellRendersTagSidebar(t *testing.T) {
	srv := newTestServer(t)
	srv.store.Create(&parts.Part{MPN: "R", PartType: "local", Tags: []string{"resistor"}})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/", nil))
	body := rr.Body.String()
	if !strings.Contains(body, `id="tag-nav"`) || !strings.Contains(body, "resistor") || !strings.Contains(body, "1") {
		t.Errorf("shell should render the tag sidebar with resistor(1); body=%s", body)
	}
}

// TestTagFilterActiveHighlight pins the OOB tag-nav swap on search: when
// handleSearch runs with ?tag=resistor it must emit, alongside #parts-tbody,
// an OOB <aside id="tag-nav" hx-swap-oob="true"> carrying the active tag so
// the sidebar's copper highlight (cat-item active) lands on the resistor
// entry and NOT on the capacitor entry. Without the OOB swap the sidebar is
// never re-rendered after initial shell load — layout.html hard-codes
// Active="" — so clicking a tag filters the table but no highlight shows.
//
// The active-class assertion is the load-bearing one: resistor's <a> must
// carry "cat-item active" while capacitor's <a> must carry only "cat-item"
// (no active). The two are distinguished by tying the class string to the
// tag-specific hx-get URL so a substring check can't conflate them.
func TestTagFilterActiveHighlight(t *testing.T) {
	srv := newTestServer(t)
	srv.store.Create(&parts.Part{MPN: "R", PartType: "local", Tags: []string{"resistor"}})
	srv.store.Create(&parts.Part{MPN: "C", PartType: "local", Tags: []string{"capacitor"}})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/parts/search?tag=resistor", nil))
	body := rr.Body.String()
	// OOB swap fired — the sidebar refreshed alongside the rows.
	if !strings.Contains(body, `id="tag-nav"`) {
		t.Errorf("search response should ship an OOB #tag-nav aside; body=%s", body)
	}
	if !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Errorf("OOB aside must carry hx-swap-oob=\"true\"; body=%s", body)
	}
	// Resistor entry is the active one — class+URL tied together so this
	// cannot match the capacitor line.
	resistorActive := `cat-item active"` + "\n" + `     hx-get="/ui/parts/search?tag=resistor"`
	if !strings.Contains(body, resistorActive) {
		t.Errorf("resistor entry should carry the active highlight; body=%s", body)
	}
	// Capacitor entry must NOT be active — the closing quote lands right
	// after "cat-item" (no " active" inserted).
	capInactive := `cat-item"` + "\n" + `     hx-get="/ui/parts/search?tag=capacitor"`
	if !strings.Contains(body, capInactive) {
		t.Errorf("capacitor entry must NOT carry the active highlight; body=%s", body)
	}
}

// TestTagFilterNoTagLeavesNothingActive pins the plain-search branch: a search
// with no ?tag= sets Active="" so no sidebar entry picks up the highlight.
// Guards against a regression where the OOB swap hard-codes a tag or where
// Active defaults to something non-empty.
func TestTagFilterNoTagLeavesNothingActive(t *testing.T) {
	srv := newTestServer(t)
	srv.store.Create(&parts.Part{MPN: "R", PartType: "local", Tags: []string{"resistor"}})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/parts/search?q=R", nil))
	body := rr.Body.String()
	if !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Errorf("plain search should still ship the OOB sidebar (Active=\"\" ); body=%s", body)
	}
	if strings.Contains(body, "cat-item active") {
		t.Errorf("plain search must not highlight any tag; body=%s", body)
	}
}

func TestBulkDeleteRemovesParts(t *testing.T) {
	srv := newTestServer(t)
	p1 := &parts.Part{MPN: "D1", PartType: "local"}
	p2 := &parts.Part{MPN: "D2", PartType: "local"}
	p3 := &parts.Part{MPN: "D3", PartType: "local"}
	srv.store.Create(p1)
	srv.store.Create(p2)
	srv.store.Create(p3)

	body := "id=" + p1.ID + "&id=" + p2.ID
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/parts/bulk-delete", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("bulk-delete = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := srv.store.Count(); got != 1 {
		t.Errorf("after deleting 2 of 3, Count = %d, want 1", got)
	}
	resp := rr.Body.String()
	// Match the MPN as it renders in the row cell (<td class="mpn">D1</td>),
	// NOT as a bare substring — ULIDs are Crockford base32 and routinely
	// contain "D1"/"D2" as substrings (e.g. ...4DD1K2M8), which made the old
	// loose check flake. The cell-scoped match pins the test's actual intent:
	// the deleted parts' MPNs do not render as rows.
	if strings.Contains(resp, `class="mpn">D1<`) || strings.Contains(resp, `class="mpn">D2<`) {
		t.Errorf("deleted parts should not appear in the refreshed rows; body=%s", resp)
	}
	if !strings.Contains(resp, `class="mpn">D3<`) {
		t.Errorf("the remaining part should appear; body=%s", resp)
	}
}

func TestBulkDeleteUnknownIdIgnored(t *testing.T) {
	srv := newTestServer(t)
	p := &parts.Part{MPN: "K", PartType: "local"}
	srv.store.Create(p)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/parts/bulk-delete", "id=nope&id="+p.ID))
	if rr.Code != http.StatusOK {
		t.Fatalf("bulk-delete = %d, want 200", rr.Code)
	}
	if got := srv.store.Count(); got != 0 {
		t.Errorf("unknown id should be ignored, real id deleted; Count=%d want 0", got)
	}
}

// TestLowStockFilter locks the low-stock chip (?low=1) — Task 4 wired the `low`
// param into filteredParts (QtyOnHand <= ReorderPoint); this pins it as a
// regression test. It passes on the Task-4 behavior (no new production code).
func TestLowStockFilter(t *testing.T) {
	srv := newTestServer(t)
	srv.store.Create(&parts.Part{MPN: "OK1", PartType: "local", QtyOnHand: 100, ReorderPoint: 10})
	srv.store.Create(&parts.Part{MPN: "LOW1", PartType: "local", QtyOnHand: 2, ReorderPoint: 10})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/parts/search?low=1", nil))
	body := rr.Body.String()
	if !strings.Contains(body, "LOW1") || strings.Contains(body, "OK1") {
		t.Errorf("?low=1 should show only low-stock parts; body=%s", body)
	}
}

// TestBulkTagAddsTag covers slice 5b's bulk-tag action: posting new_tag=smd
// with two ids should add 'smd' to both parts' Tags. p1 already carries
// 'resistor' so the idempotent append-if-absent path is also exercised —
// re-tagging with 'resistor' (already present) must not duplicate.
func TestBulkTagAddsTag(t *testing.T) {
	srv := newTestServer(t)
	p1 := &parts.Part{MPN: "T1", PartType: "local", Tags: []string{"resistor"}}
	p2 := &parts.Part{MPN: "T2", PartType: "local"}
	srv.store.Create(p1)
	srv.store.Create(p2)
	body := "new_tag=smd&id=" + p1.ID + "&id=" + p2.ID
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/parts/bulk-tag", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("bulk-tag = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	for _, id := range []string{p1.ID, p2.ID} {
		got, _ := srv.store.Get(id)
		if !slices.Contains(got.Tags, "smd") {
			t.Errorf("part %s should now have tag 'smd': %+v", id, got.Tags)
		}
	}
	// Idempotency guard: re-apply a tag p1 already has ('resistor') via a
	// SECOND bulk-tag POST, then assert the tag is still present exactly once.
	// The first POST above added 'smd' — it never touched 'resistor' — so a
	// check after only the first POST passes whether or not the
	// !slices.Contains(p.Tags, newTag) guard in handleBulkTag exists. This
	// second POST is what actually exercises the guard: without it, 'resistor'
	// would be appended a second time (count == 2).
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, postForm("POST", "/ui/parts/bulk-tag", "new_tag=resistor&id="+p1.ID))
	if rr2.Code != http.StatusOK {
		t.Fatalf("second bulk-tag = %d, want 200; body=%s", rr2.Code, rr2.Body.String())
	}
	got, _ := srv.store.Get(p1.ID)
	if testCountStr(got.Tags, "resistor") != 1 {
		t.Errorf("resistor duplicated after re-tag: %+v", got.Tags)
	}
	// And 'smd' (added by the first POST) survives the second POST untouched.
	if testCountStr(got.Tags, "smd") != 1 {
		t.Errorf("smd should still be present exactly once: %+v", got.Tags)
	}
}

func testCountStr(s []string, v string) int {
	n := 0
	for _, x := range s {
		if x == v {
			n++
		}
	}
	return n
}

// TestDetailRendersSpecRows pins slice 6's detail-panel restructure: the
// rebuilt detail.html must surface the Specs map as one spec-row per entry
// (key/value) and replace the old separate stock form with an inline
// qty-stepper (the −/+ buttons fold the delta field into the form). The
// panel-icon, title, and sub (mfr · tag · footprint) line are the mockup's
// header layout. This test would fail against the old detail.html: no
// qty-stepper, no per-key spec-row.
func TestDetailRendersSpecRows(t *testing.T) {
	srv := newTestServer(t)
	p := &parts.Part{MPN: "DT1", PartType: "local", Manufacturer: "Yageo", Footprint: "0805",
		Tags: []string{"resistor"}, Specs: map[string]string{"resistance": "10k", "power": "1/8W"}, QtyOnHand: 42}
	srv.store.Create(p)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/parts/"+p.ID, nil))
	body := rr.Body.String()
	for _, want := range []string{"DT1", "Yageo", "0805", "resistance", "10k", "qty-value", "42"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q; body=%s", want, body)
		}
	}
}

// TestDetailSubLineNoLeadingSeparator pins the hardening fix for the .part-sub
// line in detail.html. When Manufacturer is empty but Tags/Footprint are
// present, the line must NOT emit a leading " · " separator — it should render
// "x · 0805" (tag first, then footprint), not " · x · 0805". The fix uses
// nested {{if}} guards so a separator only appears BEFORE a segment when a
// PRIOR segment was non-empty. Also asserts the full-manufacturer case still
// renders "Yageo · resistor · 0805".
func TestDetailSubLineNoLeadingSeparator(t *testing.T) {
	srv := newTestServer(t)
	// Empty Manufacturer, one tag, one footprint — the bug case.
	empty := &parts.Part{MPN: "SUB1", PartType: "local", Tags: []string{"x"}, Footprint: "0805"}
	srv.store.Create(empty)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/parts/"+empty.ID, nil))
	body := rr.Body.String()
	if !strings.Contains(body, "x · 0805") {
		t.Errorf("empty-manufacturer sub-line should render 'x · 0805'; body=%s", body)
	}
	if strings.Contains(body, "> · ") {
		t.Errorf("sub-line must not emit a leading separator ('> · '); body=%s", body)
	}
	// Full-manufacturer case — all three segments present, separators between each.
	full := &parts.Part{MPN: "SUB2", PartType: "local", Manufacturer: "Yageo",
		Tags: []string{"resistor"}, Footprint: "0805"}
	srv.store.Create(full)
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, httptest.NewRequest("GET", "/ui/parts/"+full.ID, nil))
	body2 := rr2.Body.String()
	if !strings.Contains(body2, "Yageo · resistor · 0805") {
		t.Errorf("full sub-line should render 'Yageo · resistor · 0805'; body=%s", body2)
	}
}

func TestParseTags(t *testing.T) {
	got := parseTags("resistor, smd , ic,,")
	if len(got) != 3 || got[0] != "resistor" || got[1] != "smd" || got[2] != "ic" {
		t.Errorf("parseTags = %v, want [resistor smd ic]", got)
	}
	if parseTags("") != nil {
		t.Errorf("parseTags(\"\") should return nil")
	}
}

func TestParseKV(t *testing.T) {
	got := parseKV("resistance=10k\ntolerance=1%\n\nbadline\n =empty")
	if len(got) != 2 || got["resistance"] != "10k" || got["tolerance"] != "1%" {
		t.Errorf("parseKV = %v, want {resistance:10k tolerance:1%%}", got)
	}
	if parseKV("") != nil {
		t.Errorf("parseKV(\"\") should return nil")
	}
}

func TestFormatTags(t *testing.T) {
	if got := formatTags([]string{"resistor", "smd"}); got != "resistor, smd" {
		t.Errorf("formatTags = %q, want \"resistor, smd\"", got)
	}
}

func TestFormatKVSorted(t *testing.T) {
	got := formatKV(map[string]string{"b": "2", "a": "1"})
	if got != "a=1\nb=2" {
		t.Errorf("formatKV should be sorted: %q", got)
	}
}

func TestCreateWithAllFields(t *testing.T) {
	srv := newTestServer(t)
	body := "mpn=NEW1&part_type=local&manufacturer=Yageo&reorder_threshold=50&tags=resistor,smd&unit_of_measure=pieces&package_qty=100&footprint=0805"
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/parts", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("create = %d; body=%s", rr.Code, rr.Body.String())
	}
	pts := srv.store.List()
	if len(pts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(pts))
	}
	p := pts[0]
	if p.Manufacturer != "Yageo" {
		t.Errorf("Manufacturer = %q, want Yageo", p.Manufacturer)
	}
	if p.ReorderPoint != 50 {
		t.Errorf("ReorderPoint = %d, want 50", p.ReorderPoint)
	}
	if !slices.Contains(p.Tags, "resistor") || !slices.Contains(p.Tags, "smd") {
		t.Errorf("Tags = %v, want [resistor smd]", p.Tags)
	}
	if p.UnitOfMeasure != "pieces" {
		t.Errorf("UnitOfMeasure = %q, want pieces", p.UnitOfMeasure)
	}
	if p.PackageQty != 100 {
		t.Errorf("PackageQty = %d, want 100", p.PackageQty)
	}
}

func TestEditWithSpecsAndCustomFields(t *testing.T) {
	srv := newTestServer(t)
	p := &parts.Part{MPN: "EDT1", PartType: "local"}
	srv.store.Create(p)
	body := "version=" + fmt.Sprintf("%d", p.Version) +
		"&manufacturer=STMicro&reorder_threshold=10&tags=ic,power" +
		"&unit_of_measure=pieces&package_qty=10" +
		"&specs=voltage=3.3V%0Acurrent=1A&custom_fields=shelf=2024"
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/parts/"+p.ID, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("edit = %d; body=%s", rr.Code, rr.Body.String())
	}
	got, _ := srv.store.Get(p.ID)
	if got.Manufacturer != "STMicro" {
		t.Errorf("Manufacturer = %q", got.Manufacturer)
	}
	if got.ReorderPoint != 10 {
		t.Errorf("ReorderPoint = %d", got.ReorderPoint)
	}
	if !slices.Contains(got.Tags, "ic") || !slices.Contains(got.Tags, "power") {
		t.Errorf("Tags = %v", got.Tags)
	}
	if got.Specs["voltage"] != "3.3V" || got.Specs["current"] != "1A" {
		t.Errorf("Specs = %v", got.Specs)
	}
	if got.CustomFields["shelf"] != "2024" {
		t.Errorf("CustomFields = %v", got.CustomFields)
	}
	if got.UnitOfMeasure != "pieces" || got.PackageQty != 10 {
		t.Errorf("UoM=%q PackageQty=%d", got.UnitOfMeasure, got.PackageQty)
	}
}

// --- Storage tab (flat-locations model) -----------------------------------

// uiCreateLocation creates a location via the test server's locations store.
func uiCreateLocation(t *testing.T, srv *Server, label string) *locations.Location {
	t.Helper()
	l := &locations.Location{Label: label}
	if err := srv.locations.Create(l); err != nil {
		t.Fatalf("create location %q: %v", label, err)
	}
	return l
}

func uiCreatePart(t *testing.T, srv *Server, mpn string) *parts.Part {
	t.Helper()
	p := &parts.Part{MPN: mpn, PartType: "local"}
	if err := srv.store.Create(p); err != nil {
		t.Fatalf("create part %q: %v", mpn, err)
	}
	return p
}

// TestLocationsPageRendersList pins the Storage page: GET /ui/locations/search
// (the async tbody-fill the shell loads) lists every location. The flat-
// locations contents-count comes from the components keyspace.
func TestLocationsPageRendersList(t *testing.T) {
	srv := newTestServer(t)
	drawer := uiCreateLocation(t, srv, "Drawer 1")
	uiCreateLocation(t, srv, "Bin A")
	// one component in Drawer 1 → contents-count "1" in the Counts map
	p := uiCreatePart(t, srv, "LP")
	if err := srv.components.Add(drawer.ID, p.ID, 1, nil); err != nil {
		t.Fatalf("components.Add: %v", err)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/locations/search", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("search = %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Drawer 1") || !strings.Contains(body, "Bin A") {
		t.Errorf("search missing a location; body: %s", body)
	}
}

// TestLocationDetailShowsContents pins scan-to-find at the UI: the detail
// fragment shows the location + its embedded contents (components.List). The
// template renders each Component's PartID (the ULID); the count comes from
// len(Contents).
func TestLocationDetailShowsContents(t *testing.T) {
	srv := newTestServer(t)
	bin := uiCreateLocation(t, srv, "Bin C")
	// Two components — capture the partIDs so the assertion can look for them.
	pa := uiCreatePart(t, srv, "C-1")
	pb := uiCreatePart(t, srv, "C-2")
	for _, p := range []*parts.Part{pa, pb} {
		if err := srv.components.Add(bin.ID, p.ID, 1, nil); err != nil {
			t.Fatalf("components.Add %s: %v", p.MPN, err)
		}
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/locations/"+bin.ID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("detail = %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Bin C") {
		t.Errorf("detail missing the label; body: %s", body)
	}
	if !strings.Contains(body, "2 component(s)") {
		t.Errorf("detail missing the contents count; body: %s", body)
	}
	if !strings.Contains(body, pa.ID) || !strings.Contains(body, pb.ID) {
		t.Errorf("detail missing the component PartIDs; body: %s", body)
	}
}

// TestLocationCreate pins POST /ui/locations (create-single) → 200 + the
// loc-row-created fragment (Task 43: htmx, no full-page redirect). The fragment
// prepends the new row into #loc-tbody and ships OOB swaps that open the fresh
// bin's detail (#loc-detail) and refresh the tag sidebar (#loc-tag-nav),
// mirroring the Parts create flow (handleCreate → row-created.html). The old
// flow returned a 303 redirect.
func TestLocationCreate(t *testing.T) {
	srv := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/locations", url.Values{"label": {"New Bin"}}.Encode()))
	if rr.Code != http.StatusOK {
		t.Fatalf("create = %d, want 200 (htmx fragment); body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	// The new row + its via-code render (the primary swap into #loc-tbody).
	if !strings.Contains(body, "New Bin") {
		t.Errorf("create fragment should contain the new row's label; body=%s", body)
	}
	// OOB detail open + sidebar refresh — mirrors Parts' row-created flow.
	if !strings.Contains(body, `id="loc-detail"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Errorf("create fragment should ship OOB #loc-detail + swap markers; body=%s", body)
	}
	if !strings.Contains(body, `id="loc-tag-nav"`) {
		t.Errorf("create fragment should refresh the tag sidebar; body=%s", body)
	}
	// And the location was actually persisted.
	found := false
	for _, l := range srv.locations.List() {
		if l.Label == "New Bin" {
			found = true
		}
	}
	if !found {
		t.Error("create did not persist the location")
	}
}

// TestLocationCreateFormRenders pins the header "+ new" button's target
// (Task 43): GET /ui/locations/new renders the create form into #loc-detail,
// mirroring Parts' /ui/parts/new. The literal route must win over {id}.
func TestLocationCreateFormRenders(t *testing.T) {
	srv := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/locations/new", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /ui/locations/new = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{`hx-post="/ui/locations"`, `name="label"`, `name="tags"`, `name="notes"`} {
		if !strings.Contains(body, want) {
			t.Errorf("create form missing %q; body=%s", want, body)
		}
	}
}

// TestLocationsPageRendersTagSidebar pins the Storage shell's sidebar facet
// (Task 43): handleLocationsPage must surface locationTagCounts through the
// loc-tag-nav.html partial so the sidebar lists each location tag with its
// count — mirroring Parts' tag sidebar. Without the LocTags field on the page
// data + the partial invoke in locations.html, the sidebar stays empty.
func TestLocationsPageRendersTagSidebar(t *testing.T) {
	srv := newTestServer(t)
	l := &locations.Location{Label: "Drawer", Tags: []string{"garage"}}
	if err := srv.locations.Create(l); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/locations", nil))
	body := rr.Body.String()
	if !strings.Contains(body, `id="loc-tag-nav"`) || !strings.Contains(body, "garage") {
		t.Errorf("Storage shell should render the tag sidebar with garage; body=%s", body)
	}
	// The header create button + search kbd hint are present (chrome parity).
	if !strings.Contains(body, `hx-get="/ui/locations/new"`) || !strings.Contains(body, `class="search-kbd"`) {
		t.Errorf("Storage shell missing the +new button or search-kbd hint; body=%s", body)
	}
	// The inline create form that used to sit above the table is gone.
	if strings.Contains(body, `action="/ui/locations" method="post"`) {
		t.Errorf("inline create form should be removed from the Storage shell; body=%s", body)
	}
}

// TestLocationTagFilter pins the Storage sidebar facet's ?tag= filter (Task
// 43): a tag query returns only locations carrying that tag, mirroring Parts'
// tag sidebar. Without the tag branch in handleLocationsSearch the garage query
// would leak the workbench row.
func TestLocationTagFilter(t *testing.T) {
	srv := newTestServer(t)
	srv.locations.Create(&locations.Location{Label: "G1", Tags: []string{"garage"}})
	srv.locations.Create(&locations.Location{Label: "W1", Tags: []string{"workbench"}})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/locations/search?tag=garage", nil))
	body := rr.Body.String()
	if !strings.Contains(body, "G1") {
		t.Errorf("?tag=garage should show G1; body=%s", body)
	}
	if strings.Contains(body, "W1") {
		t.Errorf("?tag=garage should not leak the workbench row; body=%s", body)
	}
}

// TestLocationBulkFormRenders pins the "+ bulk" header button's target (Task
// 44): GET /ui/locations/bulk renders the bulk-create form with the method
// selector, prefix, and the sanity cap. The literal route must win over {id}.
func TestLocationBulkFormRenders(t *testing.T) {
	srv := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/locations/bulk", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /ui/locations/bulk = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{`hx-post="/ui/locations/bulk"`, `name="method"`, `name="prefix"`, `name="max_labels"`} {
		if !strings.Contains(body, want) {
			t.Errorf("bulk form missing %q; body=%s", want, body)
		}
	}
}

// TestLocationBulkCreateRow pins the row method end-to-end: a bulk POST
// creates the labels via GenerateLabels+CreateBulk, refreshes the tbody with
// every new location, ships the OOB tag-sidebar swap, and resets the panel to
// a created-count notice.
func TestLocationBulkCreateRow(t *testing.T) {
	srv := newTestServer(t)
	before := len(srv.locations.List())
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/locations/bulk",
		"method=row&prefix=box&from=1&to=3&max_labels=100"))
	if rr.Code != http.StatusOK {
		t.Fatalf("bulk = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"box1", "box2", "box3"} {
		if !strings.Contains(body, want) {
			t.Errorf("refreshed tbody missing %q; body=%s", want, body)
		}
	}
	if !strings.Contains(body, `id="loc-detail" hx-swap-oob`) || !strings.Contains(body, "created 3") {
		t.Errorf("bulk response should reset the panel to a created-3 notice; body=%s", body)
	}
	if !strings.Contains(body, `id="loc-tag-nav" hx-swap-oob`) {
		t.Errorf("bulk response should ship the OOB sidebar refresh; body=%s", body)
	}
	if got := len(srv.locations.List()); got != before+3 {
		t.Errorf("locations = %d, want %d", got, before+3)
	}
}

// TestLocationBulkCreateGrid pins the grid method + the UI→engine method
// adapter shape (grid params flow through LabelParams): rows A-B × cols 1-2
// → four shelf-A1..shelf-B2 locations.
func TestLocationBulkCreateGrid(t *testing.T) {
	srv := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/locations/bulk",
		"method=grid&prefix=shelf&row_from=A&row_to=B&col_from=1&col_to=2"))
	if rr.Code != http.StatusOK {
		t.Fatalf("bulk = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"shelf-A1", "shelf-A2", "shelf-B1", "shelf-B2"} {
		if !strings.Contains(body, want) {
			t.Errorf("refreshed tbody missing %q; body=%s", want, body)
		}
	}
	if !strings.Contains(body, "created 4") {
		t.Errorf("panel notice should say created 4; body=%s", body)
	}
}

// TestLocationBulkCreateBadRange pins the no-write error path: GenerateLabels
// is pure and errors BEFORE CreateBulk, so a reversed range re-renders the
// form with the error (retargeted into #loc-detail) and creates nothing.
func TestLocationBulkCreateBadRange(t *testing.T) {
	srv := newTestServer(t)
	before := len(srv.locations.List())
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/locations/bulk",
		"method=row&prefix=box&from=5&to=1"))
	if got := rr.Header().Get("Hx-Retarget"); got != "#loc-detail" {
		t.Errorf("Hx-Retarget = %q, want #loc-detail", got)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "invalid") {
		t.Errorf("bad range should re-render the form with the error; body=%s", body)
	}
	if got := len(srv.locations.List()); got != before {
		t.Errorf("bad range must create nothing; locations = %d, want %d", got, before)
	}
}

// TestComponentStockMoveRequiresReason pins Task 45's server-side contract: a
// stock movement without a reason is rejected and records nothing (no
// Quantity change, no History entry). The client `required` attribute is
// convenience only.
func TestComponentStockMoveRequiresReason(t *testing.T) {
	srv := newTestServer(t)
	bin := uiCreateLocation(t, srv, "RBin")
	p := uiCreatePart(t, srv, "RSN1")
	if err := srv.components.Add(bin.ID, p.ID, 5, nil); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/locations/"+bin.ID+"/components/"+p.ID+"/adjust",
		"qty=3&dir=in"))
	body := rr.Body.String()
	if !strings.Contains(body, "reason is required") {
		t.Errorf("reason-less move should be rejected with a banner; body=%s", body)
	}
	c, err := srv.components.Get(bin.ID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Add itself appends the initial-stock movement, so the baseline is 1
	// history entry — the rejected move must leave it at exactly that.
	if c.Quantity != 5 || len(c.History) != 1 {
		t.Errorf("rejected move must record nothing; qty=%d history=%d (want 5 and 1)", c.Quantity, len(c.History))
	}
}

// TestComponentStockInOutAndHistory pins the Task 45 flow end-to-end: in/out
// with a reason adjusts the quantity (via AdjustQty's signed delta + Part
// re-derivation) AND the movement history renders in the detail — collapsed
// <details> with count, timestamp, signed delta, reason.
func TestComponentStockInOutAndHistory(t *testing.T) {
	srv := newTestServer(t)
	bin := uiCreateLocation(t, srv, "HBin")
	p := uiCreatePart(t, srv, "HIST1")
	if err := srv.components.Add(bin.ID, p.ID, 2, nil); err != nil {
		t.Fatal(err)
	}
	// Stock IN 5.
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/locations/"+bin.ID+"/components/"+p.ID+"/adjust",
		"qty=5&dir=in&reason=restock"))
	body := rr.Body.String()
	if !strings.Contains(body, `>+5<`) || !strings.Contains(body, "restock") {
		t.Errorf("stock-in should render a +5 history entry with the reason; body=%s", body)
	}
	if !strings.Contains(body, "history (2)") {
		t.Errorf("history summary should show count 2 (initial add + this move); body=%s", body)
	}
	// Stock OUT 3.
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, postForm("POST", "/ui/locations/"+bin.ID+"/components/"+p.ID+"/adjust",
		"qty=3&dir=out&reason=used for repair"))
	body2 := rr2.Body.String()
	if !strings.Contains(body2, `>-3<`) || !strings.Contains(body2, "used for repair") {
		t.Errorf("stock-out should render a -3 history entry with the reason; body=%s", body2)
	}
	if !strings.Contains(body2, "history (3)") {
		t.Errorf("history summary should show count 3 (add + in + out); body=%s", body2)
	}
	// Store truth: 2 + 5 - 3 = 4, three movements (initial add + in + out),
	// and the part cache followed.
	c, err := srv.components.Get(bin.ID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Quantity != 4 || len(c.History) != 3 {
		t.Errorf("qty=%d history=%d, want 4 and 3", c.Quantity, len(c.History))
	}
	got, _ := srv.store.Get(p.ID)
	if got.QtyOnHand != 4 {
		t.Errorf("Part.QtyOnHand=%d, want 4 (re-derived from components)", got.QtyOnHand)
	}
}

// TestLocationsLastUsedColumn pins the derived last-used column: a location
// with movements renders its most-recent Movement.Timestamp (expected value
// computed from the store's own History, so the test is deterministic), a
// never-used location renders "-", and the sortable header is present.
func TestLocationsLastUsedColumn(t *testing.T) {
	srv := newTestServer(t)
	used := uiCreateLocation(t, srv, "UsedBin")
	idle := uiCreateLocation(t, srv, "IdleBin")
	p := uiCreatePart(t, srv, "LUP1")
	if err := srv.components.Add(used.ID, p.ID, 7, nil); err != nil {
		t.Fatal(err)
	}
	// Expected timestamp straight from the store: the newest movement at "used".
	c, err := srv.components.Get(used.ID, p.ID)
	if err != nil || len(c.History) == 0 {
		t.Fatalf("component history missing: %+v err=%v", c, err)
	}
	want := c.History[len(c.History)-1].Timestamp.Format("Jan 02 15:04")

	// The data rows come from the search fragment; the thead (header + sort
	// link) lives only in the shell — fetch both.
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/locations/search", nil))
	body := rr.Body.String()
	if !strings.Contains(body, want) {
		t.Errorf("used bin should render its latest movement time %q; body=%s", want, body)
	}
	if !strings.Contains(body, ">-<") {
		t.Errorf("never-used bin should render '-'; body=%s", body)
	}
	_ = idle // its row is the "-" assertion above (the only movement-free bin)
	shellRR := httptest.NewRecorder()
	srv.ServeHTTP(shellRR, httptest.NewRequest("GET", "/ui/locations", nil))
	shell := shellRR.Body.String()
	if !strings.Contains(shell, ">Last used") {
		t.Errorf("shell table missing the Last used header; shell=%s", shell)
	}
	if !strings.Contains(shell, "sort=used") {
		t.Errorf("Last used column should be sortable via sort=used; shell=%s", shell)
	}
}

// TestCopyLinkButton pins the copy-URL affordance in both detail panels: each
// renders a copyVia button carrying the entity's via-code (the JS itself is
// shell-side; this pins the wiring — button + code reach the browser).
func TestCopyLinkButton(t *testing.T) {
	srv := newTestServer(t)
	p := uiCreatePart(t, srv, "COPY1")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/ui/parts/"+p.ID, nil))
	if body := rr.Body.String(); !strings.Contains(body, "copyVia(this,'"+p.ViaCode+"')") {
		t.Errorf("part detail should carry a copyVia button with the via-code; body=%s", body)
	}
	bin := uiCreateLocation(t, srv, "CopyBin")
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, httptest.NewRequest("GET", "/ui/locations/"+bin.ID, nil))
	if body := rr2.Body.String(); !strings.Contains(body, "copyVia(this,'"+bin.ViaCode+"')") {
		t.Errorf("location detail should carry a copyVia button with the via-code; body=%s", body)
	}
	// Both shells define the function.
	for _, path := range []string{"/ui/", "/ui/locations"} {
		shellRR := httptest.NewRecorder()
		srv.ServeHTTP(shellRR, httptest.NewRequest("GET", path, nil))
		if s := shellRR.Body.String(); !strings.Contains(s, "function copyVia") {
			t.Errorf("shell %s should define copyVia", path)
		}
	}
}

// TestLocationEditVersionConflictReload pins §5.14 on the locations surface: a
// stale expectedVersion → 409 + the reload prompt (NOT a banner). The banner
// path would re-render with the canonical Version + the user's stale fields,
// enabling a blind-overwrite on retry; the reload forces a re-fetch. The stale
// edit must NOT be applied.
func TestLocationEditVersionConflictReload(t *testing.T) {
	srv := newTestServer(t)
	a := uiCreateLocation(t, srv, "VC")
	// Bump the stored version so the form's expected (1) is stale.
	cur, _ := srv.locations.Get(a.ID)
	cur.Notes = "concurrent-edit"
	if err := srv.locations.Update(cur, cur.Version); err != nil { // now version 2
		t.Fatal(err)
	}
	body := url.Values{"version": {"1"}, "label": {"VC-overwrite"}}.Encode()
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/locations/"+a.ID, body))
	if rr.Code != http.StatusConflict {
		t.Fatalf("version-conflict edit = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "reload") {
		t.Errorf("version-conflict should render the reload prompt; body: %s", rr.Body.String())
	}
	got, _ := srv.locations.Get(a.ID)
	if got.Label == "VC-overwrite" {
		t.Error("the stale version-conflict edit was applied (blind overwrite — §5.14 violation)")
	}
}

// TestLocationEditWithComponents is the reported bug (2026-08-14): editing a
// location that holds components — e.g. adding a tag — hit handleLocationEdit's
// hand-built template data, which omitted PartLookup; location-detail.html's
// `index $.PartLookup .PartID` then failed the whole render with "index of
// untyped nil" and the raw error text replaced the detail panel. The edit path
// must build its data via locationDetailData (PartLookup + PartsList included),
// and the success response must ship an OOB #loc-tag-nav swap so a tag change
// refreshes the sidebar without a manual reload.
func TestLocationEditWithComponents(t *testing.T) {
	srv := newTestServer(t)
	bin := uiCreateLocation(t, srv, "EditBin")
	p := uiCreatePart(t, srv, "EDITMPN1")
	if err := srv.components.Add(bin.ID, p.ID, 5, nil); err != nil {
		t.Fatalf("components.Add: %v", err)
	}
	body := url.Values{"version": {"1"}, "label": {"EditBin"}, "tags": {"garage,workbench"}}.Encode()
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, postForm("POST", "/ui/locations/"+bin.ID, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("edit = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	b := rr.Body.String()
	// The component renders with its MPN — PartLookup resolved (was: 500 +
	// "index of untyped nil" replacing the panel).
	if !strings.Contains(b, "EDITMPN1") {
		t.Errorf("edited detail should resolve the component's MPN; body=%s", b)
	}
	// The add-component picker is present — PartsList included.
	if !strings.Contains(b, `name="part_id"`) {
		t.Errorf("edited detail should carry the add-component picker; body=%s", b)
	}
	// OOB sidebar refresh — the tag change lands without a manual reload.
	if !strings.Contains(b, `id="loc-tag-nav"`) || !strings.Contains(b, `hx-swap-oob="true"`) {
		t.Errorf("edit success should ship an OOB #loc-tag-nav refresh; body=%s", b)
	}
	if !strings.Contains(b, "garage") {
		t.Errorf("refreshed sidebar should list the new tag; body=%s", b)
	}
}
