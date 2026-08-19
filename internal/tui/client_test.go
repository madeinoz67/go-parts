package tui

// client_test.go — the typed REST client against the REAL rest.Server via
// httptest (no mocks; same discipline as rest/server_test.go). The client is
// the TUI's only dependency on the outside world.

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/components"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
	"github.com/madeinoz67/go-parts/internal/rest"
	"github.com/madeinoz67/go-parts/internal/via"
)

func newClientServer(t *testing.T) (*Client, *parts.Store) {
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
	rs := rest.NewServer(ps, fts, vs, ls, cs)
	srv := httptest.NewServer(rs)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL), ps
}

func TestClientSearchBrowseLow(t *testing.T) {
	c, ps := newClientServer(t)
	for _, mpn := range []string{"AAA1", "BBB2"} {
		if err := ps.Create(&parts.Part{MPN: mpn, PartType: "local", Description: "row", QtyOnHand: 2, ReorderPoint: 5}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := c.BrowseAll()
	if err != nil || len(rows) != 2 {
		t.Fatalf("BrowseAll = %v, %v; want 2 rows", rows, err)
	}
	if rows[0].MPN == "" || rows[0].QtyOnHand != 2 {
		t.Fatalf("PartRow decode: %+v", rows[0])
	}
	st, err := c.Stats()
	if err != nil || st.PartsTotal != 2 {
		t.Fatalf("Stats = %+v, %v", st, err)
	}
	low, err := c.LowStock()
	if err != nil || len(low) != 2 { // 2 <= 5 → both low
		t.Fatalf("LowStock = %d rows, %v; want 2", len(low), err)
	}
	got, err := c.Search("AAA")
	if err != nil || len(got) != 1 || got[0].MPN != "AAA1" {
		t.Fatalf("Search = %v, %v", got, err)
	}
}

func TestClientEmptyQueries(t *testing.T) {
	c, _ := newClientServer(t)
	rows, err := c.Search("") // bare /parts?q= → server returns null → nil rows, NO error
	if err != nil || len(rows) != 0 {
		t.Fatalf("empty Search = %v, %v; want empty, nil", rows, err)
	}
}
