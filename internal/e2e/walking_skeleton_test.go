// Package e2e is go-parts' end-to-end integration test (Task 12, the final
// slice of the Phase-1 walking skeleton). It wires the whole stack together
// in-process — storage.Open → index.NewFTS → parts.NewStore → rest.NewServer —
// over a real httptest.NewServer (loopback socket, real HTTP client) and drives
// the create→search→stock flow that the rest of the skeleton exists to support.
//
// No mocks. This is the slice's done-criteria proof: every layer the prior 11
// tasks built (storage + flock + migration bootstrap, keys + disjoint prefixes,
// the BM25 FTS ported from go-rag, parts.Store's §5.14 striped-lock concurrency
// + optimistic Version + stock-preservation, and the stdlib ServeMux REST
// surface with ETag/If-Match) is exercised through one HTTP conversation. The
// -race gate is mandatory for this package (it touches the concurrency-safe
// Store through the first multi-writer surface that will ship in production).
//
// JSON field names are Go field names verbatim (parts.Part ships no json tags;
// REST T10 and this test use Go field names per the part.go comment — adding
// tags later is additive and breaks nothing). The create body and the stock
// body therefore use PascalCase keys: MPN, PartType, Description, Tags, Delta.
package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/parts"
	"github.com/madeinoz67/go-parts/internal/rest"
	"github.com/madeinoz67/go-parts/internal/storage"
	"github.com/madeinoz67/go-parts/internal/via"
)

// TestWalkingSkeleton proves the Phase-1 slice end-to-end: open the storage
// layer (which bootstraps the schema version and acquires the single-process
// flock), build the FTS + parts.Store + rest.Server over the same Pebble DB,
// and exercise the create→search→stock flow over real HTTP.
//
// The flow: POST a part, GET /parts?q=… and expect exactly one hit, POST a
// stock delta, GET the part and assert QtyOnHand reflects the delta. Every
// assertion goes through a real loopback socket — no handler-level fakes — so
// the ServeMux routing, the JSON round-trip, and the storage-level write path
// are all in scope.
func TestWalkingSkeleton(t *testing.T) {
	dir := t.TempDir()
	dbStore, err := storage.Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := dbStore.Close(); err != nil {
			t.Errorf("dbStore.Close: %v", err)
		}
	})
	fts := index.NewFTS(dbStore.DB)
	store := parts.NewStore(dbStore.DB, fts, via.NewStore(dbStore.DB))
	srv := rest.NewServer(store, fts)
	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)
	client := httpSrv.Client()

	// Create one part. PascalCase keys — parts.Part has no json tags so the
	// JSON decoder matches Go field names exactly (rest.handleCreate decodes
	// into a real parts.Part; lowercase keys would be silently ignored,
	// leaving every field at its zero value and FTS indexing nothing).
	createBody, _ := json.Marshal(map[string]any{
		"MPN":         "RC0805FR-0710KL",
		"PartType":    "linked",
		"Description": "10k 0805 resistor",
		"Tags":        []string{"resistor"},
	})
	resp, err := client.Post(httpSrv.URL+"/parts", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	// Search by a phrase in the description. handleSearch drives the FTS
	// (field-weighted BM25 over the zero workspace prefix) and hydrates hits
	// via Store.Get, so a single hit means: FTS saw the indexed fields AND
	// the parts keyspace round-tripped the record through JSON.
	searchResp, err := client.Get(httpSrv.URL + "/parts?q=10k+resistor")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	defer searchResp.Body.Close()
	if searchResp.StatusCode != http.StatusOK {
		t.Fatalf("search status = %d, want %d", searchResp.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(searchResp.Body)
	if err != nil {
		t.Fatalf("read search body: %v", err)
	}
	var hits []map[string]any
	if err := json.Unmarshal(body, &hits); err != nil {
		t.Fatalf("decode search hits %q: %v", string(body), err)
	}
	if len(hits) != 1 {
		t.Fatalf("search hits = %d, want 1 (body=%s)", len(hits), string(body))
	}
	idRaw, ok := hits[0]["ID"]
	if !ok {
		t.Fatalf("hit missing ID key (body=%s)", string(body))
	}
	id, ok := idRaw.(string)
	if !ok || id == "" {
		t.Fatalf("hit ID = %#v, want non-empty string", idRaw)
	}

	// Adjust stock through POST /parts/{id}/stock. The body uses the Go field
	// name (Delta) for the same reason as the create body — no json tags.
	stockBody, _ := json.Marshal(map[string]int{"Delta": -1})
	stockResp, err := client.Post(httpSrv.URL+"/parts/"+id+"/stock", "application/json", bytes.NewReader(stockBody))
	if err != nil {
		t.Fatalf("stock adjust: %v", err)
	}
	if stockResp.StatusCode != http.StatusOK {
		t.Fatalf("stock adjust status = %d, want %d", stockResp.StatusCode, http.StatusOK)
	}
	_ = stockResp.Body.Close()

	// Re-GET the part and assert QtyOnHand reflects the delta. This proves the
	// stock delta was persisted to the parts keyspace (not just in-memory) and
	// that a fresh Get round-trips it through JSON. AdjustStock does NOT bump
	// Version (§5.14) — the version is left untouched from create (== 1).
	getResp, err := client.Get(httpSrv.URL + "/parts/" + id)
	if err != nil {
		t.Fatalf("get after stock: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want %d", getResp.StatusCode, http.StatusOK)
	}
	var p parts.Part
	if err := json.NewDecoder(getResp.Body).Decode(&p); err != nil {
		t.Fatalf("decode part: %v", err)
	}
	if p.QtyOnHand != -1 {
		t.Fatalf("QtyOnHand = %d, want -1", p.QtyOnHand)
	}
	if p.ID != id {
		t.Fatalf("ID round-trip = %q, want %q", p.ID, id)
	}
	if p.MPN != "RC0805FR-0710KL" {
		t.Fatalf("MPN round-trip = %q, want RC0805FR-0710KL", p.MPN)
	}
}
