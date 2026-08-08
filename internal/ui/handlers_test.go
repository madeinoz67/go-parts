package ui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
		`id="search"`,               // search input
		`id="parts-tbody"`,          // table body target for htmx
		`id="detail-panel"`,         // detail panel target
		`hx-get="/ui/parts/search"`, // htmx live-filter wiring
		`<nav`,                      // top nav
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
