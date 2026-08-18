// Package origin implements go-parts' uniform cross-origin (CSRF) posture:
// every mutating HTTP endpoint on every surface — REST, UI, MCP — rejects a
// request whose browser-issued Origin header does not match the request's own
// scheme://host. A request with no Origin header passes: it is not a CSRF
// vector (curl, scripts, `claude mcp http`). Reads pass: a cross-origin GET
// is not a mutation, and v1 endpoints carry no ambient credentials to leak.
//
// Why an Origin guard and not CSRF tokens: v1 has no sessions, no cookies, no
// ambient credentials (§5.8 no-op auth seam) — there is nothing to bind a
// token to. The threat is the drive-by browser: any web page can fire
// CORS-"simple" requests (a <form> POST, or fetch with Content-Type
// text/plain) at the loopback-bound daemon, and JSON bodies decode regardless
// of the declared Content-Type, so "JSON APIs are preflight-protected" is NOT
// true of this server. The Origin header is the one signal browsers guarantee
// on cross-origin requests and attacker JavaScript cannot forge.
//
// Known limitations (accepted, uniform across surfaces):
//
//   - DNS rebinding — an attacker whose domain rebinds to the loopback
//     address sends Origin: http://evil.example with Host: evil.example, and
//     the two match. The guard is same-origin, not a host allowlist; pinning
//     the expected host belongs to the future auth layer (§5.8), where a
//     configured-host check closes rebinding for good.
//
//   - TLS-terminating reverse proxy — the daemon sees plain HTTP (r.TLS ==
//     nil) while the browser's Origin is https://host, so every browser
//     mutation fails CLOSED (403) behind a proxy. v1's default
//     loopback/plain-HTTP posture is unaffected; proxy deployments get the
//     same configured-scheme/host check from the future auth layer.
//     X-Forwarded-Proto is deliberately not honored — spoofable on a
//     non-loopback bind (the same reasoning as rest.Server.baseURL).
package origin

import "net/http"

// Guard wraps h with the same-origin check on mutating methods (POST, PUT,
// PATCH, DELETE). Every surface's §5.8 interceptor composes this — the
// posture lives here and nowhere else.
func Guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if isMutating(r.Method) {
			if o := r.Header.Get("Origin"); o != "" && o != expected(r) {
				http.Error(w, "cross-origin request blocked", http.StatusForbidden)
				return
			}
		}
		h(w, r)
	}
}

// expected returns the Origin value a same-origin browser client would send
// for this request: its own scheme://host.
func expected(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// isMutating reports whether the method can change server state (the CSRF
// check applies only to these).
func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}
