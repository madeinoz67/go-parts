package mcp

// http.go — the streamable-HTTP-style transport: stateless POST /mcp, one
// JSON-RPC request per POST, JSON response (go-rag's shape minus its auth
// plumbing — go-parts v1 has no auth, §5.8). No session state is kept; a
// session id is not minted because there is nothing to key one to.

import (
	"encoding/json"
	"net/http"
)

// HTTPHandler returns the http.Handler the daemon mounts at /mcp.
func (s *Server) HTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Origin same-origin guard (ui.Server.auth parity — the CSRF defense
		// this mutating endpoint was missing, F3). A browser ALWAYS sends an
		// Origin header on a POST; a blind cross-origin form fire has a
		// browser-issued Origin that cannot equal the request's own
		// scheme://host. A request with no Origin (curl, claude mcp http) is
		// allowed — it is not a CSRF vector.
		if origin := r.Header.Get("Origin"); origin != "" {
			expected := schemeOf(r) + "://" + r.Host
			if origin != expected {
				http.Error(w, "cross-origin request blocked", http.StatusForbidden)
				return
			}
		}
		var req rpcReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		resp := s.handle(req)
		if resp == nil {
			// Notification — no response body per JSON-RPC 2.0.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// schemeOf returns the request's URL scheme: "https" when TLS-terminated,
// else "http" (the loopback default). Used for the CSRF same-origin
// comparison — same semantics as ui.Server's helper (duplicated rather than
// imported: pulling internal/ui in would drag the embedded templates along).
func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
