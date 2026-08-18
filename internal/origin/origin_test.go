package origin

// origin_test.go pins the uniform CSRF posture's mechanics per method and
// scheme — the surface tests (rest/ui/mcp) pin that each surface actually
// wires Guard in; these pin Guard itself.

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

// guarded wraps a marker handler and drives one request through it.
func guarded(t *testing.T, method, originHeader string, useTLS bool) *httptest.ResponseRecorder {
	t.Helper()
	called := false
	h := Guard(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot) // marker: handler ran
	})
	req := httptest.NewRequest(method, "/x", nil)
	if originHeader != "" {
		req.Header.Set("Origin", originHeader)
	}
	if useTLS {
		req.TLS = &tls.ConnectionState{}
	}
	rr := httptest.NewRecorder()
	h(rr, req)
	if called && rr.Code != http.StatusTeapot {
		t.Fatalf("handler ran but status = %d", rr.Code)
	}
	return rr
}

func TestGuardBlocksCrossOriginMutations(t *testing.T) {
	// httptest defaults host to example.com, scheme http.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rr := guarded(t, method, "http://evil.example", false)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s with foreign Origin = %d, want 403", method, rr.Code)
		}
		if rr.Body.String() != "cross-origin request blocked\n" {
			t.Errorf("%s body = %q", method, rr.Body.String())
		}
	}
}

func TestGuardAllowsSameOriginAndNoOrigin(t *testing.T) {
	if rr := guarded(t, http.MethodPost, "http://example.com", false); rr.Code != http.StatusTeapot {
		t.Fatalf("same-origin POST = %d, want handler to run", rr.Code)
	}
	if rr := guarded(t, http.MethodPost, "", false); rr.Code != http.StatusTeapot {
		t.Fatalf("no-Origin POST = %d, want handler to run (not a CSRF vector)", rr.Code)
	}
	// An https request expects an https Origin (scheme joins the comparison).
	if rr := guarded(t, http.MethodPost, "http://example.com", true); rr.Code != http.StatusForbidden {
		t.Fatalf("http Origin on https request = %d, want 403 (scheme mismatch)", rr.Code)
	}
	if rr := guarded(t, http.MethodPost, "https://example.com", true); rr.Code != http.StatusTeapot {
		t.Fatalf("https Origin on https request = %d, want handler to run", rr.Code)
	}
}

func TestGuardReadsUnaffected(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if rr := guarded(t, method, "http://evil.example", false); rr.Code != http.StatusTeapot {
			t.Errorf("%s with foreign Origin = %d, want handler to run (reads unguarded)", method, rr.Code)
		}
	}
}
