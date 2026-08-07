// Package rest exposes go-parts' parts.Store + field-weighted BM25 FTS over a
// stdlib net/http ServeMux using Go 1.26 method-patterns (no chi/gin/echo).
//
// This is the first MULTI-WRITER surface over the parts keyspace (Task 10):
// every mutating HTTP request lands on parts.Store.Update / AdjustStock /
// Create / Delete, all of which are §5.14-safe (per-id striped locks +
// optimistic Version). The handlers here are deliberately thin — they marshal
// JSON and route status codes; every concurrency-sensitive decision is made
// below this layer in Store.
//
// v1 = no-op auth middleware seam (PRD §5.8): every request passes through one
// interceptor point (`Server.auth`), so makerspace auth is additive later
// rather than a rewrite. Stock is AdjustStock's exclusive domain — the PATCH
// handler does NOT touch QtyOnHand even if the body carries one (Store.Update
// enforces this server-side as the F3 invariant; the handler documents it).
package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/parts"
)

// Server is a REST handler over one parts.Store + one FTS. Construct with
// NewServer and serve with http.ListenAndServe(addr, srv). The store and FTS
// must be the SAME instances the rest of the process uses — the FTS that the
// store writes to is the FTS the search handler reads from.
type Server struct {
	store *parts.Store
	fts   *index.FTS
	mux   *http.ServeMux
}

// NewServer wires a Server over store + fts and registers every route. It does
// NOT listen — the caller does http.ListenAndServe(addr, srv) — so the Server
// is also a http.Handler usable from httptest.NewServer or in-process tests.
func NewServer(store *parts.Store, fts *index.FTS) *Server {
	s := &Server{store: store, fts: fts, mux: http.NewServeMux()}
	s.routes()
	return s
}

// ServeHTTP dispatches through the registered ServeMux. Every route is wrapped
// by s.auth (the §5.8 single interceptor point) at registration time, so this
// method is just the mux.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// routes registers every spec §8 route. Go 1.26 method-patterns ("METHOD /path")
// make the routing table executable documentation; the {id} path value is read
// via r.PathValue. Every handler is wrapped in s.auth — the one place a future
// auth layer is added without touching the handlers.
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.auth(s.handleHealthz))
	s.mux.HandleFunc("GET /stats", s.auth(s.handleStats))
	s.mux.HandleFunc("GET /parts", s.auth(s.handleSearch)) // ?q=… (FTS); absent q → empty list
	s.mux.HandleFunc("POST /parts", s.auth(s.handleCreate))
	s.mux.HandleFunc("GET /parts/{id}", s.auth(s.handleGet))
	s.mux.HandleFunc("PATCH /parts/{id}", s.auth(s.handlePatch))
	s.mux.HandleFunc("DELETE /parts/{id}", s.auth(s.handleDelete))
	s.mux.HandleFunc("POST /parts/{id}/stock", s.auth(s.handleStock))
}

// auth is the §5.8 single interceptor point. v1 is a no-op: every request
// passes straight through to h. Real auth (makerspace tokens, etc.) lands
// here, in this function only, when added — every handler then benefits
// without a single change to its code. This is the "single-operator in v1,
// shaped for multi-user later" invariant.
func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h(w, r)
	}
}

// handleHealthz is the liveness probe. Cheap, no store touch, returns "ok".
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("ok"))
}

// handleStats returns the parts-row count. Goes through Store.Count() so the
// REST layer never imports the Pebble keyspace directly.
func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"parts_total": s.store.Count()})
}

// handleCreate accepts a Part JSON body (Go field names — no json tags on the
// struct), assigns ID/audit/Version via Store.Create, and returns the stored
// record with ETag. Store.Create fills CreatedBy="local" when absent; a future
// auth layer would substitute the caller's identity here.
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var p parts.Part
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "invalid part body: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Caller MUST NOT set ID/Version/audit fields on create — server-assigned.
	// Zeroing them defensively keeps a caller-supplied ID from silently taking
	// precedence over the ULID Store.Create would assign.
	p.ID = ""
	p.Version = 0
	p.CreatedAt = time.Time{}
	p.UpdatedAt = time.Time{}
	if err := s.store.Create(&p); err != nil {
		http.Error(w, "create: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writePart(w, http.StatusCreated, &p)
}

// handleGet returns one part by ID with an ETag header pinned to its Version.
// The ETag is the optimistic-concurrency token PATCH requires (If-Match).
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		// Store wraps parts.ErrNotFound with %w, so errors.Is unwraps cleanly
		// without REST needing to import the storage engine (§5.1).
		if errors.Is(err, parts.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "get: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writePart(w, http.StatusOK, p)
}

// handlePatch edits an existing part. Two invariants govern this handler
// (folded-in from the Task 8/9 reviews):
//
//  1. GET-THEN-EDIT. The handler loads the current part (Store.Get), applies
//     the patched fields onto it, then Store.Update(loaded, currentVersion).
//     This round-trips CreatedAt/CreatedBy (a bare Update(&Part{ID:…,
//     Description:…}, v) would zero them — Task 8's partial-Part caveat) and
//     is the consistent F3-decision shape.
//
//  2. STOCK IS NOT EDITABLE HERE. Update preserves cur.QtyOnHand server-side
//     (F3 invariant — stock is AdjustStock-only; §5.14). Even if the PATCH
//     body carries QtyOnHand it has no effect. Stock changes go to
//     POST /parts/{id}/stock.
//
// If-Match is REQUIRED: a missing header is 428 Precondition Required; a stale
// version (the loaded version no longer matches the stored version under the
// striped lock) is 409 Conflict — never silently overwritten (§5.14).
func (s *Server) handlePatch(w http.ResponseWriter, r *http.Request) {
	etag := r.Header.Get("If-Match")
	if etag == "" {
		http.Error(w, "If-Match required", http.StatusPreconditionRequired)
		return
	}
	expectedVersion, err := parseETagVersion(etag)
	if err != nil {
		http.Error(w, "invalid If-Match: "+err.Error(), http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	loaded, err := s.store.Get(id)
	if err != nil {
		if errors.Is(err, parts.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "get: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Decode the patch body onto a fresh Part, then copy ONLY the writable
	// fields onto loaded. Authoritative fields (ID, Version, QtyOnHand,
	// CreatedAt/CreatedBy, UpdatedAt/UpdatedBy) are never taken from the
	// patch body — round-tripped from `loaded` or set by Store.Update.
	// io.EOF (empty body) is treated as a no-op patch.
	var patch parts.Part
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid patch body: "+err.Error(), http.StatusBadRequest)
		return
	}
	applyPatch(loaded, &patch)
	if err := s.store.Update(loaded, expectedVersion); err != nil {
		// Update calls Get under the striped lock, so a part deleted between
		// our outer Get and Update surfaces here as parts.ErrNotFound → 404
		// (mirrors handleDelete/handleStock's not-found handling). The only
		// other recoverable path is a version conflict (the expectedVersion no
		// longer matches the in-lock stored version) — the §5.14 race window
		// optimistic concurrency exists for → 409.
		if errors.Is(err, parts.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "version conflict: "+err.Error(), http.StatusConflict)
		return
	}
	writePart(w, http.StatusOK, loaded)
}

// applyPatch copies writable fields from src onto dst using a zero-means-skip
// rule per field. Slices and maps are taken by reference when present in src.
// Authoritative fields (QtyOnHand, audit, Version, ID) are deliberately NOT
// copied — Update enforces QtyOnHand + audit round-trip server-side; the
// caller cannot influence them via PATCH.
func applyPatch(dst, src *parts.Part) {
	if src.MPN != "" {
		dst.MPN = src.MPN
	}
	if src.Manufacturer != "" {
		dst.Manufacturer = src.Manufacturer
	}
	if src.Category != "" {
		dst.Category = src.Category
	}
	if src.Subcategory != "" {
		dst.Subcategory = src.Subcategory
	}
	if src.PartType != "" {
		dst.PartType = src.PartType
	}
	if src.Description != "" {
		dst.Description = src.Description
	}
	if src.Footprint != "" {
		dst.Footprint = src.Footprint
	}
	if src.UnitOfMeasure != "" {
		dst.UnitOfMeasure = src.UnitOfMeasure
	}
	if src.PackageQty != 0 {
		dst.PackageQty = src.PackageQty
	}
	if src.ReorderPoint != 0 {
		dst.ReorderPoint = src.ReorderPoint
	}
	if len(src.Tags) > 0 {
		dst.Tags = src.Tags
	}
	if src.Specs != nil {
		dst.Specs = src.Specs
	}
	if src.CustomFields != nil {
		dst.CustomFields = src.CustomFields
	}
	if src.DatasheetRef != "" {
		dst.DatasheetRef = src.DatasheetRef
	}
}

// handleDelete removes a part. 204 on success; 404 if the part is missing (the
// store's Get-inside-Delete returns wrapped parts.ErrNotFound, which we
// unwrap). Store.Delete is serialized under the per-id striped lock, so a
// concurrent Update cannot resurrect a just-deleted record (§5.14).
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Delete(r.PathValue("id")); err != nil {
		if errors.Is(err, parts.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "delete: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleStock applies a commutative stock delta (§5.14) via Store.AdjustStock.
// The body is `{"Delta": <int>}` (Go field name; no json tags on the body
// struct). AdjustStock does NOT bump Version and does NOT touch the FTS —
// QtyOnHand is not an indexed field. The `reason` field is accepted by the
// store for a future stock-movement audit log; the REST layer passes "rest"
// as a placeholder pending a richer caller-identity story (post-auth).
func (s *Server) handleStock(w http.ResponseWriter, r *http.Request) {
	// io.EOF (empty body) is treated as Delta=0 — a no-op stock adjustment.
	var body struct{ Delta int }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid stock body: "+err.Error(), http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	if err := s.store.AdjustStock(id, body.Delta, "rest"); err != nil {
		if errors.Is(err, parts.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "adjust stock: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Return the updated part so callers see the new QtyOnHand without a
	// follow-up GET. ETag is pinned to the unchanged Version (stock doesn't
	// bump version — §5.14).
	p, err := s.store.Get(id)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	writePart(w, http.StatusOK, p)
}

// handleSearch drives GET /parts?q=… through the field-weighted BM25 FTS,
// hydrating the top hits via Store.Get. An empty query returns an empty list
// (FTS.Tokenize drops everything → Search returns nil → empty slice). The
// workspace is the zero [8]byte — v1 is single-workspace.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	var ws [8]byte
	hits := s.fts.Search(ws, q, 20)
	out := make([]parts.Part, 0, len(hits))
	for _, h := range hits {
		p, err := s.store.Get(h.ID)
		if err != nil {
			continue // part vanished between FTS hit and hydrate — skip
		}
		out = append(out, *p)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// writePart sets ETag + Content-Type, status, and writes the JSON body. ETag
// is the quoted Version (If-Match format) — the optimistic-concurrency token
// PATCH requires.
func writePart(w http.ResponseWriter, status int, p *parts.Part) {
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, p.Version))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(p)
}

// parseETagVersion parses an If-Match header value of the form `"<int>"`,
// returning the integer version. The quotes are required (writePart emits
// them); a bare integer is also accepted as a courtesy.
func parseETagVersion(s string) (int, error) {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"`)
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("If-Match %q: %w", s, err)
	}
	if v < 1 {
		return 0, fmt.Errorf("If-Match version must be >= 1, got %d", v)
	}
	return v, nil
}
