package label

import (
	"strings"
	"testing"
)

// TestSVG_RendersQRCodeAndTitle pins the §5.17 label contract: the SVG carries
// a white background, a black QR-module group (the QR is actually drawn, not a
// placeholder), the human-readable code, and the title. (The QR's encoded
// payload is covered by TestPayload — the QR modules are binary, not text in
// the SVG, so the payload isn't assertable from the markup directly.)
func TestSVG_RendersQRCodeAndTitle(t *testing.T) {
	out, err := SVG("L-7B3D1E", "Bin A3", "https://parts.local")
	if err != nil {
		t.Fatalf("SVG: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg"`,
		`<rect width="100%" height="100%" fill="white"/>`,
		`<g fill="black" shape-rendering="crispEdges">`,
		`>L-7B3D1E</text>`, // human-readable code
		`>Bin A3</text>`,   // title
		`</svg>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("SVG missing %q", want)
		}
	}
	// The QR module group must contain at least one dark <rect> (the QR is
	// non-empty). A finder pattern alone guarantees many rects, so this is a
	// strong "the QR rendered" signal.
	if cnt := strings.Count(s, `<rect x="`); cnt < 50 {
		t.Errorf("SVG has %d <rect> elements, want >=50 (QR did not render modules)", cnt)
	}
}

// TestPayload pins the QR URL construction (the data the QR actually encodes):
// absolute when baseURL is set (trailing slash trimmed), relative when empty.
func TestPayload(t *testing.T) {
	cases := []struct{ base, code, want string }{
		{"https://parts.local", "L-A", "https://parts.local/via/L-A"},
		{"https://parts.local/", "L-A", "https://parts.local/via/L-A"}, // trailing slash trimmed
		{"", "L-A", "/via/L-A"}, // relative when no base (e.g. CLI without config)
	}
	for _, c := range cases {
		if got := payload(c.base, c.code); got != c.want {
			t.Errorf("payload(%q,%q) = %q, want %q", c.base, c.code, got, c.want)
		}
	}
}

// TestSVG_XMLEscapes pins that operator free-text titles don't break SVG
// parsing (& < > escaped in the text node).
func TestSVG_XMLEscapes(t *testing.T) {
	out, err := SVG("L-X", "A <B> & C", "")
	if err != nil {
		t.Fatalf("SVG: %v", err)
	}
	s := string(out)
	for _, bad := range []string{"A <B>", "& C"} {
		if strings.Contains(s, bad) {
			t.Errorf("SVG contains unescaped %q", bad)
		}
	}
	if !strings.Contains(s, "A &lt;B&gt; &amp; C") {
		t.Errorf("SVG did not escape the title; output: %s", s)
	}
}
