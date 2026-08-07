package ui

import (
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/madeinoz67/go-parts/internal/parts"
)

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
	if err := s.tmpl.ExecuteTemplate(w, "detail.html", map[string]any{"P": p}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleCreateForm renders the create.html form fragment (the "+ new" nav
// button's target). The form POSTs to /ui/parts (handleCreate). Renders into
// the detail panel — same target as row-select, so the create form and the
// detail view share one swap surface by design.
func (s *Server) handleCreateForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "create.html", nil); err != nil {
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
	if err := s.tmpl.ExecuteTemplate(w, "row.html", map[string]any{"P": p}); err != nil {
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
