package ui

import (
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
