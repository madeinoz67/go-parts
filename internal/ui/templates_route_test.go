package ui

// Template-route drift gate — the pin for htmx URL consumption.
//
// The UI's routes are consumed by hx-get/hx-post attributes inside the
// embedded templates, which is invisible to static graph analysis (Gortex
// indexes the .html files as text but has no htmx extractor — upstream
// zzet/gortex#606). Every route whose only consumer is a template therefore
// reads as an "orphan provider", and a template pointing at a renamed or
// deleted route ships silently broken. This gate closes that hole the same
// way routes_doc_test.go closes docs-vs-code drift.
//
// Mechanism: for each hx-* request URL in each shipped template, resolve the
// request through the REAL Server's ServeMux via mux.Handler(r), which
// returns the pattern that would serve it — then require a specific pattern,
// never the subtree fallbacks. This distinction matters: a bare status probe
// is useless here because "GET /ui/" (the shell subtree) swallows every
// miss with a 200, so a renamed fragment route looks "resolved". Only the
// matched pattern separates live wiring from shell-fallback masking. The
// handler is never invoked, so no entities are seeded — a {{...}} path
// segment just substitutes an opaque probe token. A 405 (method mismatch)
// also surfaces through Handler as a non-specific pattern, which is exactly
// the second drift class this gate exists to catch (hx-post to a GET-only
// route).
//
// Scope: static hx-get/hx-post/hx-put/hx-patch/hx-delete attributes only.
// Fully dynamic URLs (the whole value is a template action, e.g.
// hx-post="{{.Action}}") cannot be resolved statically; they are pinned to a
// frozen allowlist below so a new one is a conscious gate edit, not a silent
// escape hatch.

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// htmxAttrRe matches the request-bearing htmx attributes. Capture 1 is the
// method verb, capture 2 the raw URL value (possibly a template action).
var htmxAttrRe = regexp.MustCompile(`hx-(get|post|put|patch|delete)="([^"]*)"`)

// Comment strippers: templates embed inline <script> whose comments sketch
// example hx-* markup ("// Sort headers: <a hx-get=\"...?sort=mpn\">") and
// HTML comments do the same — prose, not wiring. Stripping before matching
// keeps the gate on live attributes only. Newlines are preserved in the
// replacement so reported line numbers stay true.
var (
	htmlCommentRe   = regexp.MustCompile(`(?s)<!--.*?-->`)
	jsBlockComment  = regexp.MustCompile(`(?s)/\*.*?\*/`)
	jsLineComment   = regexp.MustCompile(`//[^\n]*`)
	blankLineKeeper = func(s string) string { return strings.Repeat("\n", strings.Count(s, "\n")) }
)

func stripTemplateComments(body string) string {
	body = htmlCommentRe.ReplaceAllStringFunc(body, blankLineKeeper)
	body = jsBlockComment.ReplaceAllStringFunc(body, blankLineKeeper)
	// Line comments stop at EOL, so the newline survives; only the comment
	// text goes. (No real hx-* value contains "//" — they are path-absolute
	// /ui/... URLs.)
	return jsLineComment.ReplaceAllString(body, "")
}

// dynamicURLAllowlist is the frozen set of fully-dynamic hx-* URL values —
// every template action the handlers interpolate whole. confirm-footprint.html
// posts to {{.Action}} and cancels via {{.CancelURL}}; conflict.html reloads
// {{.Reload}}. Adding a value here means a handler now controls the URL: name
// the template and the handler that supplies it in this comment.
var dynamicURLAllowlist = map[string]bool{
	"{{.Action}}":    true, // confirm-footprint.html ← handleConfirmFootprint's .Action
	"{{.CancelURL}}": true, // confirm-footprint.html ← the cancel link
	"{{.Reload}}":    true, // conflict.html ← the stale-version reload target
}

// subtreeFallbacks are the patterns that match BROADLY rather than naming a
// route: the shell subtree and the static-asset subtree. A template URL that
// resolves to one of these (or to "" — no pattern at all) is drift: it
// missed the specific fragment route it was written against and is being
// masked by the fallback. /ui/ itself is in the set because no hx-* attribute
// legitimately targets the shell page — fragments only. NOTE: Handler returns
// patterns AS REGISTERED, method-qualified — "GET /ui/", not "/ui/".
var subtreeFallbacks = map[string]bool{
	"":            true, // no pattern matched (plain 404/405 territory)
	"/ui/":        true, // the shell subtree — masks every miss with a 200
	"/ui/static/": true, // the static-asset file server
}

// isSubtreeFallback reports whether a mux-matched pattern is a broad
// fallback rather than a specific route. Handler returns patterns AS
// REGISTERED, method-qualified ("GET /ui/"), so strip a method prefix before
// comparing against the bare-path fallback set.
func isSubtreeFallback(pattern string) bool {
	for _, m := range []string{"GET ", "POST ", "PUT ", "PATCH ", "DELETE ", "HEAD "} {
		if path, ok := strings.CutPrefix(pattern, m); ok {
			return subtreeFallbacks[path]
		}
	}
	return subtreeFallbacks[pattern]
}

func TestTemplateHTMXURLsResolveToRegisteredRoutes(t *testing.T) {
	srv := newTestServer(t)
	files, err := fs.Glob(embedded, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	// Vacuity guards: if the walker or the regex silently breaks, the gate
	// must not pass on zero work. The floors sit just under today's real
	// counts (27 template files; ~40 hx-* request attributes).
	if len(files) < 25 {
		t.Fatalf("walked only %d template files — expected ≥25; embed or glob broken?", len(files))
	}
	urlCount := 0
	methodsSeen := map[string]bool{}
	gotDynamic := map[string]bool{}

	for _, name := range files {
		b, err := embedded.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := stripTemplateComments(string(b))
		for _, m := range htmxAttrRe.FindAllStringSubmatchIndex(body, -1) {
			method := strings.ToUpper(body[m[2]:m[3]])
			raw := body[m[4]:m[5]]
			line := 1 + strings.Count(body[:m[0]], "\n")
			urlCount++
			methodsSeen[method] = true

			// Fully dynamic URL: cannot resolve statically — collect for the
			// allowlist pin below.
			if strings.HasPrefix(raw, "{{") {
				gotDynamic[raw] = true
				continue
			}

			// Substitute {{...}} path segments with an opaque probe token:
			// mux.Handler matches the pattern without invoking the handler,
			// so any token works — no real entity ids needed.
			path, _, _ := strings.Cut(raw, "?")
			segs := strings.Split(path, "/")
			for i, seg := range segs {
				if strings.Contains(seg, "{{") {
					segs[i] = "probe"
				}
			}
			resolved := strings.Join(segs, "/")

			// A live relative URL (no leading /) would panic NewRequest —
			// loud, which is the right failure mode for unwired markup.
			req := httptest.NewRequest(method, resolved, nil)
			_, pattern := srv.mux.Handler(req)
			if isSubtreeFallback(pattern) {
				t.Errorf("%s:%d: hx-%s %q resolved %q → pattern %q — no specific route (renamed/removed, or method mismatch)", name, line, strings.ToLower(method), raw, resolved, pattern)
			}
		}
	}

	if urlCount < 30 {
		t.Fatalf("extracted only %d hx-* request URLs — expected ≥30; attribute regex broken?", urlCount)
	}
	if !methodsSeen[http.MethodGet] || !methodsSeen[http.MethodPost] {
		t.Fatalf("expected both GET and POST among hx-* attributes, saw %v", methodsSeen)
	}
	if !reflect.DeepEqual(gotDynamic, dynamicURLAllowlist) {
		t.Errorf("fully-dynamic hx-* URL set drifted: got %v, want exactly %v (a new dynamic URL is a conscious gate edit — see dynamicURLAllowlist)", gotDynamic, dynamicURLAllowlist)
	}
}
