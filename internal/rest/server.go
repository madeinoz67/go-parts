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

	"github.com/madeinoz67/go-parts/internal/components"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/label"
	"github.com/madeinoz67/go-parts/internal/link"
	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
	"github.com/madeinoz67/go-parts/internal/via"
)

// Server is a REST handler over one parts.Store + one FTS, plus (Slice 4) the
// shared via index + locations store powering the generic Via resolver and the
// label endpoints. Construct with NewServer and serve with
// http.ListenAndServe(addr, srv). The stores must be the SAME instances the
// rest of the process uses — the FTS that the store writes to is the FTS the
// search handler reads from, and the via/locations stores are the ones the
// daemon's injected policy composes with.
type Server struct {
	store         *parts.Store
	fts           *index.FTS
	via           *via.Store        // Slice 4: GET /via/{code} resolver spine
	locations     *locations.Store  // Slice 4: location branch of the resolver + label endpoint
	components    *components.Store // Flat-locations: via-resolver embeds a location's Components
	publicBaseURL string            // Slice 4: base for label QR URLs (config; "" = request-derived)
	mux           *http.ServeMux
}

// NewServer wires a Server over store + fts + the shared via index + locations
// store + components store, and registers every route. It does NOT listen —
// the caller does http.ListenAndServe(addr, srv) — so the Server is also a
// http.Handler usable from httptest.NewServer or in-process tests. Set the
// label base URL (if any) via SetPublicBaseURL.
func NewServer(store *parts.Store, fts *index.FTS, viaStore *via.Store, locStore *locations.Store, compStore *components.Store) *Server {
	s := &Server{store: store, fts: fts, via: viaStore, locations: locStore, components: compStore, mux: http.NewServeMux()}
	s.routes()
	return s
}

// SetPublicBaseURL sets the base URL used for Via-resolver QR URLs in labels
// (Slice 4, §5.18 non-secret). Empty (the default) means labels derive the
// base from the HTTP request's scheme+host. The daemon calls this with
// cfg.PublicBaseURL at bootstrap.
func (s *Server) SetPublicBaseURL(u string) { s.publicBaseURL = u }

// baseURL resolves the label QR base URL: the configured public_base_url if
// set, else the request's scheme://host (so a label rendered behind a proxy or
// on the loopback bind still encodes a scannable absolute URL for the operator).
func (s *Server) baseURL(r *http.Request) string {
	if s.publicBaseURL != "" {
		return s.publicBaseURL
	}
	// Derive from the request ONLY for loopback (the v1 default). For a
	// non-loopback Host without a configured public_base_url, return "" → the
	// QR encodes a relative /via/{code} (safe but less useful). This prevents
	// Host-header poisoning of a persistent printed label (RedTeam MEDIUM:
	// r.Host is attacker-controllable; a QR is a durable artifact). The
	// operator sets public_base_url for a useful absolute URL on non-loopback.
	//
	// X-Forwarded-Proto is deliberately NOT honored (spoofable on non-loopback).
	if isLoopbackHost(r.Host) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		return scheme + "://" + r.Host
	}
	return "" // non-loopback without public_base_url → safe fallback
}

// isLoopbackHost reports whether host (with optional :port) is a loopback
// address. Used by baseURL to guard against Host-header poisoning of labels.
func isLoopbackHost(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i] // strip the port
	}
	switch h {
	case "127.0.0.1", "localhost", "[::1]", "::1":
		return true
	}
	return false
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
	// Slice 4 — generic Via resolver + per-entity label endpoints (§5.17, §8).
	s.mux.HandleFunc("GET /via/{code}", s.auth(s.handleVia))
	s.mux.HandleFunc("POST /parts/{id}/label", s.auth(s.handlePartLabel))
	s.mux.HandleFunc("POST /locations/{id}/label", s.auth(s.handleLocationLabel))
	// RedTeam — REST locations CRUD (the §5.2 peer-surface promise).
	s.mux.HandleFunc("GET /locations", s.auth(s.handleLocationList))
	s.mux.HandleFunc("GET /locations/{id}", s.auth(s.handleLocationGet))
	s.mux.HandleFunc("POST /locations", s.auth(s.handleLocationCreateREST))
	s.mux.HandleFunc("PATCH /locations/{id}", s.auth(s.handleLocationPatch))
	s.mux.HandleFunc("DELETE /locations/{id}", s.auth(s.handleLocationDeleteREST))
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
	// Zeroing them defensively keeps a caller-supplied value from taking
	// precedence over the ULID Store.Create assigns — INCLUDING CreatedBy (a
	// REST client POSTing {"CreatedBy":"attacker"} must not spoof the audit
	// trail; the store's "local" default / a future auth layer is authoritative).
	p.ID = ""
	p.CreatedBy = ""
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
	// RFC 7396 JSON Merge Patch (RedTeam HIGH fix): decode the body into a raw
	// map so ABSENT keys (skip) are distinguishable from JSON null (clear) and a
	// present value (overwrite). The old zero-means-skip rule couldn't clear
	// fields — PATCH returned 200 while silently dropping the operation. io.EOF
	// (empty body) → nil map → no-op patch.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid patch body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := applyPatch(loaded, raw); err != nil {
		http.Error(w, "invalid patch field: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.Update(loaded, expectedVersion); err != nil {
		// Update calls Get under the striped lock, so a part deleted between
		// our outer Get and Update surfaces here as parts.ErrNotFound → 404.
		// The remaining case is a version conflict (the expectedVersion no
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

// applyPatch applies RFC 7396 JSON Merge Patch semantics to dst (RedTeam HIGH
// fix — replaces the zero-means-skip rule that couldn't clear fields): a key
// PRESENT in raw overwrites (non-null) or clears (null); an ABSENT key is left
// unchanged. Authoritative fields (ID, Version, QtyOnHand, audit) are never
// patched — Update round-trips/enforces them server-side.
func applyPatch(dst *parts.Part, raw map[string]json.RawMessage) error {
	type pf struct {
		key string
		fn  func() error
	}
	fs := []pf{
		{"MPN", func() error { return patchStr(raw, "MPN", &dst.MPN) }},
		{"Manufacturer", func() error { return patchStr(raw, "Manufacturer", &dst.Manufacturer) }},
		{"Category", func() error { return patchStr(raw, "Category", &dst.Category) }},
		{"Subcategory", func() error { return patchStr(raw, "Subcategory", &dst.Subcategory) }},
		{"PartType", func() error { return patchStr(raw, "PartType", &dst.PartType) }},
		{"Description", func() error { return patchStr(raw, "Description", &dst.Description) }},
		{"Footprint", func() error { return patchStr(raw, "Footprint", &dst.Footprint) }},
		{"UnitOfMeasure", func() error { return patchStr(raw, "UnitOfMeasure", &dst.UnitOfMeasure) }},
		{"DatasheetRef", func() error { return patchStr(raw, "DatasheetRef", &dst.DatasheetRef) }},
		{"PackageQty", func() error { return patchInt(raw, "PackageQty", &dst.PackageQty) }},
		{"ReorderPoint", func() error { return patchInt(raw, "ReorderPoint", &dst.ReorderPoint) }},
		{"Tags", func() error { return patchTags(raw, "Tags", &dst.Tags) }},
		{"Specs", func() error { return patchMap(raw, "Specs", &dst.Specs) }},
		{"CustomFields", func() error { return patchMap(raw, "CustomFields", &dst.CustomFields) }},
	}
	for _, f := range fs {
		if err := f.fn(); err != nil {
			return fmt.Errorf("patch field %q: %w", f.key, err)
		}
	}
	return nil
}

// patchStr / patchInt / patchBool / patchTags / patchMap are the RFC 7396
// per-type helpers: absent key → skip (preserve); JSON null → clear (zero
// value); present value → unmarshal + overwrite.
func patchStr(raw map[string]json.RawMessage, key string, dst *string) error {
	v, ok := raw[key]
	if !ok {
		return nil
	}
	if string(v) == "null" {
		*dst = ""
		return nil
	}
	return json.Unmarshal(v, dst)
}

func patchInt(raw map[string]json.RawMessage, key string, dst *int) error {
	v, ok := raw[key]
	if !ok {
		return nil
	}
	if string(v) == "null" {
		*dst = 0
		return nil
	}
	return json.Unmarshal(v, dst)
}

func patchTags(raw map[string]json.RawMessage, key string, dst *[]string) error {
	v, ok := raw[key]
	if !ok {
		return nil
	}
	if string(v) == "null" {
		*dst = nil
		return nil
	}
	return json.Unmarshal(v, dst)
}

func patchMap(raw map[string]json.RawMessage, key string, dst *map[string]string) error {
	v, ok := raw[key]
	if !ok {
		return nil
	}
	if string(v) == "null" {
		*dst = nil
		return nil
	}
	return json.Unmarshal(v, dst)
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

// --- Slice 4: generic Via resolver + label endpoints (§5.17, §8) -----------

// handleVia is GET /via/{code} — the generic Via resolver (§5.17, "one
// endpoint, not one per entity"). link.Resolve dispatches on the via index's
// type tag: a location resolves WITH its embedded contents (scan-to-find), a
// part resolves to itself. An unknown code → 404 (via.ErrNotFound). The
// response shape is link.Resolved (PascalCase, no json tags — api.md §"JSON
// field names").
func (s *Server) handleVia(w http.ResponseWriter, r *http.Request) {
	res, err := link.Resolve(s.via, s.store, s.components, s.locations, r.PathValue("code"))
	if err != nil {
		// A via-miss OR a dangling reference (via.Lookup succeeded but the
		// entity record is gone — e.g. a via.Release that failed mid-delete
		// left the index pointing at nothing) are both "the code resolved to
		// nothing the client can use" → 404, matching the label handlers.
		// Anything else is a real storage fault → 500.
		if errors.Is(err, via.ErrNotFound) || errors.Is(err, parts.ErrNotFound) || errors.Is(err, locations.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "via resolve: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Slice 6: a BROWSER scanning the QR (Accept: text/html) is redirected to
	// the entity's UI deep-link; API clients (Accept: application/json, or curl's
	// */*) keep getting JSON. One endpoint serves both — the slice-4 QR (encoding
	// /via/{code}) becomes browser-friendly without re-cutting labels.
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		switch res.Type {
		case string(via.TypePart):
			http.Redirect(w, r, "/ui/?part="+res.Part.ID, http.StatusSeeOther)
		case string(via.TypeLocation):
			http.Redirect(w, r, "/ui/locations?loc="+res.Location.ID, http.StatusSeeOther)
		default:
			http.Redirect(w, r, "/ui/", http.StatusSeeOther)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

// handlePartLabel is POST /parts/{id}/label — renders the part's scannable SVG
// label (§5.17). {id} accepts a part id OR a P- via-code (resolved through the
// via index). The QR encodes the part's resolution URL; the title is MPN (plus
// description when set). Content-Type image/svg+xml.
func (s *Server) handlePartLabel(w http.ResponseWriter, r *http.Request) {
	p, err := s.partFromPath(r)
	if err != nil {
		if errors.Is(err, parts.ErrNotFound) || errors.Is(err, via.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "part label: "+err.Error(), http.StatusInternalServerError)
		return
	}
	title := p.MPN
	if p.Description != "" {
		title = p.MPN + " · " + p.Description
	}
	s.writeLabel(w, p.ViaCode, title, r)
}

// handleLocationLabel is POST /locations/{id}/label — renders the location's
// scannable SVG label. {id} accepts a location id OR an L- via-code. The title
// is the location's Label.
func (s *Server) handleLocationLabel(w http.ResponseWriter, r *http.Request) {
	l, err := s.locationFromPath(r)
	if err != nil {
		if errors.Is(err, locations.ErrNotFound) || errors.Is(err, via.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "location label: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeLabel(w, l.ViaCode, l.Label, r)
}

// partFromPath resolves r.PathValue("id") to a part, accepting either a bare id
// or a "P-" via-code (resolved through the via index). A via-miss propagates
// as via.ErrNotFound; a missing record as parts.ErrNotFound — both map to 404
// in the label handlers.
func (s *Server) partFromPath(r *http.Request) (*parts.Part, error) {
	id := r.PathValue("id")
	if strings.HasPrefix(id, "P-") {
		_, vid, err := s.via.Lookup(id)
		if err != nil {
			return nil, err
		}
		id = vid
	}
	return s.store.Get(id)
}

// locationFromPath resolves r.PathValue("id") to a location, accepting either a
// bare id or an "L-" via-code. A mismatched code (e.g. an L- code on the parts
// endpoint) surfaces naturally: via.Lookup returns a location id, then
// store.Get misses → ErrNotFound → 404.
func (s *Server) locationFromPath(r *http.Request) (*locations.Location, error) {
	id := r.PathValue("id")
	if strings.HasPrefix(id, "L-") {
		_, vid, err := s.via.Lookup(id)
		if err != nil {
			return nil, err
		}
		id = vid
	}
	return s.locations.Get(id)
}

// writeLabel renders the SVG via internal/label and writes it with an SVG
// content type. Physical printing (page layout, the OS print dialog) is a
// client concern — go-parts renders the label, not the print driver (§8).
func (s *Server) writeLabel(w http.ResponseWriter, code, title string, r *http.Request) {
	svg, err := label.SVG(code, title, s.baseURL(r))
	if err != nil {
		http.Error(w, "label render: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write(svg)
}

// --- RedTeam: REST locations CRUD (the §5.2 peer-surface promise) ----------

// handleLocationList is GET /locations — every location in JSON array order.
func (s *Server) handleLocationList(w http.ResponseWriter, r *http.Request) {
	out := s.locations.List()
	if out == nil {
		out = []*locations.Location{} // emit [] not null (jq-friendly)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleLocationGet is GET /locations/{id} — one location by id. 404 on miss.
func (s *Server) handleLocationGet(w http.ResponseWriter, r *http.Request) {
	l, err := s.locations.Get(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, locations.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeLocation(w, http.StatusOK, l)
}

// handleLocationCreateREST is POST /locations — create a single location.
// Server-assigned: ID, Version, CreatedBy ("local"), timestamps are zeroed
// from the body (same defensive posture as parts handleCreate).
func (s *Server) handleLocationCreateREST(w http.ResponseWriter, r *http.Request) {
	var l locations.Location
	if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	l.ID = ""
	l.CreatedBy = ""
	l.Version = 0
	l.CreatedAt = time.Time{}
	l.UpdatedAt = time.Time{}
	if err := s.locations.Create(&l); err != nil {
		http.Error(w, "create: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeLocation(w, http.StatusCreated, &l)
}

// handleLocationPatch is PATCH /locations/{id} — RFC 7396 JSON Merge Patch
// (Label, Notes, Tags; ViaCode immutable). If-Match required.
// Version-conflict → 409.
func (s *Server) handleLocationPatch(w http.ResponseWriter, r *http.Request) {
	etag := r.Header.Get("If-Match")
	if etag == "" {
		http.Error(w, "If-Match required", http.StatusPreconditionRequired)
		return
	}
	expected, err := parseETagVersion(etag)
	if err != nil {
		http.Error(w, "invalid If-Match: "+err.Error(), http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	loaded, err := s.locations.Get(id)
	if err != nil {
		if errors.Is(err, locations.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	for _, f := range []func() error{
		func() error { return patchStr(raw, "Label", &loaded.Label) },
		func() error { return patchStr(raw, "Notes", &loaded.Notes) },
		func() error { return patchTags(raw, "Tags", &loaded.Tags) },
	} {
		if err := f(); err != nil {
			http.Error(w, "invalid field: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := s.locations.Update(loaded, expected); err != nil {
		if errors.Is(err, locations.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, locations.ErrVersionConflict) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeLocation(w, http.StatusOK, loaded)
}

// handleLocationDeleteREST is DELETE /locations/{id}. One composed refusal:
// has-components (components.List(id) is non-empty → 409; the location still
// holds stock). locations.Store cannot see the components keyspace (§5.1), so
// the check is composed here. 204 on success.
func (s *Server) handleLocationDeleteREST(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if comps := s.components.List(id); len(comps) > 0 {
		http.Error(w, fmt.Sprintf("location has %d component(s) assigned — reassign first", len(comps)), http.StatusConflict)
		return
	}
	if err := s.locations.Delete(id); err != nil {
		if errors.Is(err, locations.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeLocation sets ETag + Content-Type + writes the location JSON.
func writeLocation(w http.ResponseWriter, status int, l *locations.Location) {
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, l.Version))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(l)
}
