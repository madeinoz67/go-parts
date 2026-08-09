package ui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/parts"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := pebble.Open(filepath.Join(t.TempDir(), "p"), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	fts := index.NewFTS(db)
	return NewServer(parts.NewStore(db, fts), fts)
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

func TestStockAdjustUpdatesQty(t *testing.T) {
	srv := newTestServer(t)
	p := &parts.Part{MPN: "STK1", PartType: "local", QtyOnHand: 100}
	srv.store.Create(p)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/ui/parts/"+p.ID+"/stock", strings.NewReader("delta=-5"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("stock = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	got, _ := srv.store.Get(p.ID)
	if got.QtyOnHand != 95 {
		t.Errorf("after -5, QtyOnHand = %d, want 95", got.QtyOnHand)
	}
	if !strings.Contains(rr.Body.String(), ">95<") && !strings.Contains(rr.Body.String(), "95") {
		t.Errorf("detail fragment should show updated qty 95; body=%s", rr.Body.String())
	}
}

func TestStockAdjustUnknownIs404(t *testing.T) {
	srv := newTestServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/ui/parts/nope/stock", strings.NewReader("delta=-5"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("stock on unknown part = %d, want 404; body=%s", rr.Code, rr.Body.String())
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
	// Idempotent: re-tagging p1 with 'resistor' (already present) doesn't duplicate.
	got, _ := srv.store.Get(p1.ID)
	if testCountStr(got.Tags, "resistor") != 1 {
		t.Errorf("resistor duplicated: %+v", got.Tags)
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
	for _, want := range []string{"DT1", "Yageo", "0805", "resistance", "10k", "qty-stepper", "42"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q; body=%s", want, body)
		}
	}
}
