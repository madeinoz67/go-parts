// Package ui is the embedded web UI over parts.Store (PRD §5.2 — a 5th surface
// over the core, served from the same binary). It calls parts.Store + index.FTS
// in-process; it does not use the REST layer. Routes live under /ui/*.
package ui

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/madeinoz67/go-parts/internal/components"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
)

//go:embed templates/* static/*
var embedded embed.FS

// Server is the /ui/* http.Handler. It shares the same *parts.Store and
// *index.FTS instances as the REST server (constructed once in the daemon).
type Server struct {
	store      *parts.Store
	fts        *index.FTS
	locations  *locations.Store  // feeds the Storage tab + location pickers (server-rendered, in-process)
	components *components.Store // flat-locations: a location's contents are its Components
	mux        *http.ServeMux
	tmpl       *template.Template
}

// NewServer wires a /ui/* Server over store + fts + locStore + compStore. The
// locations store populates the Storage tab + pickers; components feeds the
// "what's in this bin?" view (in-process — the UI does not call REST). locStore
// may be nil in tests that don't exercise the picker; the handlers guard a nil
// locations list as empty.
func NewServer(store *parts.Store, fts *index.FTS, locStore *locations.Store, compStore *components.Store) *Server {
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
		"formatTags": formatTags,
		"formatKV":   formatKV,
	}).ParseFS(embedded, "templates/*.html"))
	s := &Server{store: store, fts: fts, locations: locStore, components: compStore, tmpl: tmpl, mux: http.NewServeMux()}
	s.routes()
	return s
}

// locationOptions returns the locations for the part-detail + create-form
// pickers, nil-safe (a test Server with no locations store renders an empty
// picker). Read in-process from the locations store — the UI does not call REST
// (PRD §5.2). Slice 3b.
func (s *Server) locationOptions() []*locations.Location {
	if s.locations == nil {
		return nil
	}
	return s.locations.List()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// auth is the UI surface's §5.8 single interceptor point, mirroring
// rest.Server.auth. v1: a no-op for identity + an Origin same-origin check on
// mutating requests (the CSRF defense for the form-urlencoded UI surface —
// RedTeam critical finding). Real identity auth (makerspace tokens) lands here
// OR at the daemon's top-level mux wrapping both uiSrv and restSrv.
//
// CSRF: the UI mutates via application/x-www-form-urlencoded POSTs, which are
// CORS-"simple" (no preflight), so a cross-origin <form method=POST> fires
// blind. A browser ALWAYS sends an Origin header on a POST; we require it to
// match the request's own scheme://host (same-origin). A request with no Origin
// (curl, a non-browser client) is allowed — it is not a CSRF vector. REST is
// unaffected: its JSON bodies trigger a CORS preflight the server must allow,
// so cross-origin JSON POSTs are already browser-blocked. (The static asset
// handler is intentionally unwrapped — read-only CSS/JS/fonts.)
func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if isMutating(r.Method) {
			if origin := r.Header.Get("Origin"); origin != "" {
				expected := schemeOf(r) + "://" + r.Host
				if origin != expected {
					http.Error(w, "cross-origin request blocked", http.StatusForbidden)
					return
				}
			}
		}
		h(w, r)
	}
}

// isMutating reports whether the method can change server state (the CSRF
// check applies only to these — GETs are not CSRF vectors for this surface).
func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// schemeOf returns the request's URL scheme: "https" when TLS-terminated,
// else "http" (the loopback default). Used for the CSRF same-origin comparison.
func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /ui/", s.auth(s.handleShell))
	// Serve embedded static files (css/js/fonts) under /ui/static/ (no auth — read-only assets).
	staticSub, _ := fs.Sub(embedded, "static")
	s.mux.Handle("GET /ui/static/", http.StripPrefix("/ui/static/", http.FileServer(http.FS(staticSub))))
	// Fragment handlers.
	s.mux.HandleFunc("GET /ui/parts/search", s.auth(s.handleSearch))           // live-filter + sort + initial-load
	s.mux.HandleFunc("GET /ui/parts/new", s.auth(s.handleCreateForm))          // create form (literal wins over {id})
	s.mux.HandleFunc("GET /ui/parts/{id}", s.auth(s.handleDetail))             // row-select → detail-panel fragment
	s.mux.HandleFunc("POST /ui/parts", s.auth(s.handleCreate))                 // create → new-row fragment
	s.mux.HandleFunc("POST /ui/parts/bulk-delete", s.auth(s.handleBulkDelete)) // bulk delete → refreshed tbody + OOB tag-nav (§7.2, hx-include name=id)
	s.mux.HandleFunc("POST /ui/parts/bulk-tag", s.auth(s.handleBulkTag))       // bulk tag → refreshed tbody + OOB tag-nav (§7.2)
	s.mux.HandleFunc("POST /ui/parts/{id}", s.auth(s.handleEdit))              // inline edit → updated detail (409 on stale version)
	// Slice 5b — the Storage tab (locations management UI, in-process like the parts UI).
	s.mux.HandleFunc("GET /ui/locations", s.auth(s.handleLocationsPage))            // the Storage page (list + detail + tag sidebar)
	s.mux.HandleFunc("GET /ui/locations/new", s.auth(s.handleLocationCreateForm))   // create form (literal wins over {id}, mirrors /ui/parts/new)
	s.mux.HandleFunc("GET /ui/locations/bulk", s.auth(s.handleLocationBulkForm))    // bulk-create form (Task 44; literal wins over {id})
	s.mux.HandleFunc("POST /ui/locations/bulk", s.auth(s.handleLocationBulkCreate)) // bulk create → refreshed tbody + OOB sidebar (Task 44)
	s.mux.HandleFunc("GET /ui/locations/search", s.auth(s.handleLocationsSearch))   // sortable table-body fragment (mirrors /ui/parts/search)
	s.mux.HandleFunc("GET /ui/locations/{id}", s.auth(s.handleLocationDetail))      // location detail fragment (htmx into #loc-detail)
	s.mux.HandleFunc("POST /ui/locations", s.auth(s.handleLocationCreate))          // create-single → redirect to the page
	s.mux.HandleFunc("POST /ui/locations/{id}", s.auth(s.handleLocationEdit))       // edit (Update) → redirect or banner
	// Flat-locations: component management UI (htmx into #loc-detail).
	s.mux.HandleFunc("POST /ui/locations/{id}/components/add", s.auth(s.handleComponentAddUI))
	s.mux.HandleFunc("POST /ui/locations/{id}/components/{partId}/adjust", s.auth(s.handleComponentAdjustUI))
	s.mux.HandleFunc("POST /ui/locations/{id}/components/{partId}/remove", s.auth(s.handleComponentRemoveUI))
	s.mux.HandleFunc("GET /ui/parts/{id}/locations", s.auth(s.handlePartLocations)) // stock-at-locations fragment
}

// handleShell renders the full shell page.
//
// Tags is the §5.7/§6.2 dynamic sidebar facet (parts.Store.TagCounts) rendered
// by the tag-nav.html partial into #tag-nav. Empty until the operator starts
// tagging parts; the partial handles an empty .Tags range as a bare "Tags"
// eyebrow with no items.
func (s *Server) handleShell(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
		"Version":   "dev",
		"Count":     s.store.Count(),
		"Tags":      s.store.TagCounts(),
		"Locations": s.locationOptions(), // Slice 3b: feeds the bulk-bar Move picker
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
