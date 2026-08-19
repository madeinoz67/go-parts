package tui

// client_test.go — the typed REST client against the REAL rest.Server via
// httptest (no mocks; same discipline as rest/server_test.go). The client is
// the TUI's only dependency on the outside world.

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
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

func newClientServerFull(t *testing.T) (*Client, *parts.Store, *locations.Store, *components.Store) {
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
	return NewClient(srv.URL), ps, ls, cs
}

func TestClientGetPartAndAdjust(t *testing.T) {
	c, ps, ls, cs := newClientServerFull(t)
	p := &parts.Part{MPN: "DET1", PartType: "local", Description: "detail"}
	if err := ps.Create(p); err != nil {
		t.Fatal(err)
	}
	bin := &locations.Location{Label: "Drawer A1"}
	if err := ls.Create(bin); err != nil {
		t.Fatal(err)
	}
	if err := cs.Add(bin.ID, p.ID, 50, nil); err != nil {
		t.Fatal(err)
	}

	d, err := c.GetPart(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Part.MPN != "DET1" || len(d.Stock) != 1 || d.Stock[0].Label != "Drawer A1" || d.Stock[0].Quantity != 50 {
		t.Fatalf("GetPart decode: %+v", d)
	}
	if len(d.RecentMovements) != 1 || d.RecentMovements[0].Reason != "initial" {
		t.Fatalf("movements: %+v", d.RecentMovements)
	}

	if err := c.Adjust(bin.ID, p.ID, -5, "bench"); err != nil {
		t.Fatalf("Adjust: %v", err)
	}
	d2, _ := c.GetPart(p.ID)
	if d2.Stock[0].Quantity != 45 || len(d2.RecentMovements) != 2 || d2.RecentMovements[0].Reason != "bench" {
		t.Fatalf("post-adjust: %+v", d2)
	}
}

func TestClientAdjustErrorPropagatesBody(t *testing.T) {
	c, ps, ls, _ := newClientServerFull(t)
	p := &parts.Part{MPN: "E1", PartType: "local"}
	if err := ps.Create(p); err != nil {
		t.Fatal(err)
	}
	bin := &locations.Location{Label: "Empty"}
	if err := ls.Create(bin); err != nil {
		t.Fatal(err)
	}
	err := c.Adjust(bin.ID, p.ID, 1, "x")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("adjust against un-stocked pair must surface the engine sentinel, got %v", err)
	}
}
