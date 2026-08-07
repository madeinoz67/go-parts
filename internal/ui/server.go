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
	tmpl := template.Must(template.ParseFS(embedded, "templates/*.html"))
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
	// Fragment handlers added in later tasks: search, detail, create, edit, stock.
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
