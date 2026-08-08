package ui

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/madeinoz67/go-parts/internal/parts"
)

// commonFootprints is the baseline set shown in the footprint datalist even
// when the DB is empty. Merged with store.DistinctFootprints() so the dropdown
// always shows common packages + any custom ones the operator has used.
var commonFootprints = []string{
	"0402", "0603", "0805", "1206", "1210",
	"SOT-23", "SOT-223", "SOD-123", "SOD-323",
	"SOIC-8", "SOIC-14", "SOIC-16", "TSSOP-8", "TSSOP-14", "TSSOP-20",
	"MSOP-8", "MSOP-10",
	"QFN-24", "QFN-32", "QFN-48",
	"TQFP-44", "TQFP-64", "TQFP-100",
	"DPAK", "D2PAK", "TO-220", "TO-252",
	"DIP-8", "DIP-14", "DIP-16", "DIP-28",
	"TO-92", "THT",
}

// mergeFootprints returns the sorted union of common + db-sourced footprints
// (deduped), so the datalist always shows the baseline + any custom values.
func mergeFootprints(common, db []string) []string {
	seen := make(map[string]bool, len(common)+len(db))
	out := make([]string, 0, len(common)+len(db))
	for _, f := range common {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	for _, f := range db {
		if f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// handleSearch renders the rows.html fragment (a <tbody id="parts-tbody">) for a
// search query. It is the live-filter + sort + initial-load endpoint wired into
// the shell (layout.html): the search input's hx-trigger="keyup", the column
// headers' sort links, and the table body's hx-trigger="load" all hit this route.
//
// The handler calls fts.Search + store.Get IN-PROCESS (PRD §5.2 — the web UI is
// a 5th surface over the core, never over REST). An empty query falls through
// to store.List() because FTS.Search returns nil when tokenize yields no terms
// (so the table's initial-load + cleared-search cases still surface the corpus).
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	sortKey := r.URL.Query().Get("sort") // "mpn" | "qty" | ""
	sortDir := r.URL.Query().Get("dir")  // "asc" | "desc"

	var pts []*parts.Part
	if q == "" {
		// Empty query: FTS.Search returns nil (no tokens), so list the corpus.
		pts = s.store.List()
	} else {
		var ws [8]byte
		hits := s.fts.Search(ws, q, 500)
		pts = make([]*parts.Part, 0, len(hits))
		for _, h := range hits {
			if p, err := s.store.Get(h.ID); err == nil {
				pts = append(pts, p)
			}
		}
	}
	applySort(pts, sortKey, sortDir)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "rows.html", map[string]any{"Parts": pts, "Q": q}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleDetail renders the detail.html fragment for a single part (the row-
// select → detail-panel flow wired into rows.html: each <tr> carries
// hx-get="/ui/parts/{id}" hx-target="#detail-panel"). Calls store.Get
// IN-PROCESS (PRD §5.2 — the web UI is a 5th surface over the core, never over
// REST). parts.ErrNotFound maps to HTTP 404; any other storage error degrades
// loudly to 500 rather than rendering a partial record.
func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.store.Get(id)
	if err != nil {
		if errors.Is(err, parts.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "detail.html", map[string]any{
		"P":          p,
		"Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints()),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleCreateForm renders the create.html form fragment (the "+ new" nav
// button's target). The form POSTs to /ui/parts (handleCreate). Renders into
// the detail panel — same target as row-select, so the create form and the
// detail view share one swap surface by design.
func (s *Server) handleCreateForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "create.html", map[string]any{
		"Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints()),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleCreate parses the create form, calls store.Create IN-PROCESS (PRD §5.2
// — the web UI is a 5th surface over the core, never over REST), and renders
// the single new row as a fragment. The form's hx-swap="afterbegin" prepends
// the row to #parts-tbody. store.Create assigns ID/ViaCode/timestamps and
// indexes the FTS — the row template reads them straight off the populated
// *Part, so the response carries the canonical ULID for subsequent row-select.
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p := &parts.Part{
		MPN:         r.PostFormValue("mpn"),
		Description: r.PostFormValue("description"),
		PartType:    r.PostFormValue("part_type"),
		Category:    r.PostFormValue("category"),
		Footprint:   r.PostFormValue("footprint"),
	}
	if v := r.PostFormValue("qty"); v != "" {
		fmt.Sscanf(v, "%d", &p.QtyOnHand)
	}
	if err := s.store.Create(p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "row.html", map[string]any{"P": p, "Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints())}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleEdit applies an inline edit to a single part (the detail-panel form
// wired into detail.html: hx-post="/ui/parts/{id}" with a hidden version field
// for optimistic concurrency). It follows the Get-then-edit contract required
// by store.Update (PRD §5.14): the current record is loaded, the patched
// fields are applied onto it, then store.Update(cur, expectedVersion) is
// called. Constructing a fresh Part from the form would zero CreatedAt/
// CreatedBy and — per store.Update's stock contract — would still see
// QtyOnHand reset to the in-lock current value, but the create-time audit
// fields cannot be reconstructed from a form post. Get-then-edit round-trips
// them. Calls store IN-PROCESS (PRD §5.2 — the web UI is a 5th surface over
// the core, never over REST).
//
// On version conflict the response is 409 + the conflict.html fragment ("edited
// elsewhere — reload") so the client can re-fetch the canonical record. Not-
// found maps to 404 (parts.ErrNotFound), matching handleDetail.
func (s *Server) handleEdit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Get-then-edit (T8 contract): load current, apply patched fields, Update.
	cur, err := s.store.Get(id)
	if err != nil {
		if errors.Is(err, parts.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	expected, _ := strconv.Atoi(r.PostFormValue("version"))
	if v := r.PostFormValue("description"); v != "" {
		cur.Description = v
	}
	if v := r.PostFormValue("category"); v != "" {
		cur.Category = v
	}
	if v := r.PostFormValue("footprint"); v != "" {
		cur.Footprint = v
	}
	if err := s.store.Update(cur, expected); err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		if tErr := s.tmpl.ExecuteTemplate(w, "conflict.html", map[string]any{"ID": id}); tErr != nil {
			// Header already sent (409); the best we can do is nothing — the
			// fragment is short and the template engine doesn't error mid-write.
			_ = tErr
		}
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "detail.html", map[string]any{"P": cur, "Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints())}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleStock applies a commutative stock delta inline from the detail panel
// (the inline stock form wired into detail.html: hx-post="/ui/parts/{id}/stock"
// with a delta field). It calls store.AdjustStock IN-PROCESS (PRD §5.2 — the
// web UI is a 5th surface over the core, never over REST), then re-renders the
// detail.html fragment with the updated QtyOnHand. AdjustStock does not bump
// Version (§5.14 — stock is authoritative), so the round-tripped *Part from
// store.Get carries the post-delta QtyOnHand and the unchanged Version, which
// keeps the inline edit form's hidden version field consistent on the next
// submit. parts.ErrNotFound maps to HTTP 404, matching handleDetail/handleEdit.
func (s *Server) handleStock(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var delta int
	fmt.Sscanf(r.PostFormValue("delta"), "%d", &delta)
	if err := s.store.AdjustStock(id, delta, "ui"); err != nil {
		if errors.Is(err, parts.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p, err := s.store.Get(id)
	if err != nil {
		// AdjustStock succeeded but the record is now unreadable — treat as
		// not-found (the canonical not-found recovery for a single-part read).
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "detail.html", map[string]any{"P": p, "Footprints": mergeFootprints(commonFootprints, s.store.DistinctFootprints())}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// applySort re-orders pts in place by the requested key/direction. No-op when
// key is unrecognized (the default BM25 relevance order from FTS.Search is
// preserved).
func applySort(pts []*parts.Part, key, dir string) {
	switch key {
	case "mpn":
		sort.Slice(pts, func(i, j int) bool { return less(pts[i].MPN, pts[j].MPN, dir) })
	case "qty":
		sort.Slice(pts, func(i, j int) bool { return lessInt(pts[i].QtyOnHand, pts[j].QtyOnHand, dir) })
	}
}

func less(a, b, dir string) bool {
	if dir == "desc" {
		return a > b
	}
	return a < b
}

func lessInt(a, b int, dir string) bool {
	if dir == "desc" {
		return a > b
	}
	return a < b
}
