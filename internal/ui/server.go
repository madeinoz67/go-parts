// Package ui is the embedded web UI over parts.Store (PRD §5.2 — a 5th surface
// over the core, served from the same binary). It calls parts.Store + index.FTS
// in-process; it does not use the REST layer. Routes live under /ui/*.
package ui

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/parts"
)

//go:embed templates/* static/*
var embedded embed.FS

// Server is the /ui/* http.Handler. It shares the same *parts.Store and
// *index.FTS instances as the REST server (constructed once in the daemon).
type Server struct {
	store *parts.Store
	fts   *index.FTS
	mux   *http.ServeMux
	tmpl  *template.Template
}

func NewServer(store *parts.Store, fts *index.FTS) *Server {
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		// dict builds a map[string]any from key/value pairs so a child template
		// invoked via {{template "x" (dict "P" .)}} receives named fields
		// instead of bearing the caller's full pipeline context. Used by
		// rows.html to invoke the shared row.html per-row fragment.
		"dict": func(pairs ...any) map[string]any {
			m := make(map[string]any, len(pairs)/2)
			for i := 0; i+1 < len(pairs); i += 2 {
				k, _ := pairs[i].(string)
				m[k] = pairs[i+1]
			}
			return m
		},
	}).ParseFS(embedded, "templates/*.html"))
	s := &Server{store: store, fts: fts, tmpl: tmpl, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /ui/", s.handleShell)
	// Serve embedded static files (css/js/fonts) under /ui/static/.
	staticSub, _ := fs.Sub(embedded, "static")
	s.mux.Handle("GET /ui/static/", http.StripPrefix("/ui/static/", http.FileServer(http.FS(staticSub))))
	// Fragment handlers.
	s.mux.HandleFunc("GET /ui/parts/search", s.handleSearch)  // live-filter + sort + initial-load
	s.mux.HandleFunc("GET /ui/parts/new", s.handleCreateForm) // create form (literal wins over {id})
	s.mux.HandleFunc("GET /ui/parts/{id}", s.handleDetail)    // row-select → detail-panel fragment
	s.mux.HandleFunc("POST /ui/parts", s.handleCreate)        // create → new-row fragment
	s.mux.HandleFunc("POST /ui/parts/{id}", s.handleEdit)     // inline edit → updated detail (409 on stale version)
}

// handleShell renders the full shell page.
func (s *Server) handleShell(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
		"Version": "dev",
		"Count":   s.store.Count(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
