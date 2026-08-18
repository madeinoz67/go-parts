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
