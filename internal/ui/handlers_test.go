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
