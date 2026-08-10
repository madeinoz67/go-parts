// Package label renders scannable black-on-white Via labels (PRD §5.17):
// a QR encoding the entity's resolution URL, with the human-readable via-code
// (monospace) and the entity title beneath. Pure — no I/O, no config
// knowledge; the caller computes the base URL (from config or the request).
//
// The QR is drawn as native SVG <rect> modules (qrcode.Bitmap), not a base64
// PNG blob — keeps the output compact, crisp at any print scale, and text-
// diffable. QR generation is via github.com/skip2/go-qrcode (pure Go, CGO-free
// — fits the CGO_ENABLED=0 release build).
package label

import (
	"bytes"
	"fmt"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// modulePx is the rendered size of one QR module in SVG user units. 8 keeps a
// typical short-URL QR (25–33 modules) compact for a bin label.
const modulePx = 8

// SVG renders a black-on-white scannable label. The QR encodes
// baseURL+"/via/"+code (an absolute URL when baseURL is set, or the relative
// "/via/"+code when baseURL is empty — e.g. the CLI with no configured base).
// Beneath the QR: the human-readable code (monospace) and the title.
//
// code is the via-code ("L-7B3D1E"); title is the human label (the location's
// Label, or the part's MPN/description). Returns a self-contained SVG document.
func SVG(code, title, baseURL string) ([]byte, error) {
	qr, err := qrcode.New(payload(baseURL, code), qrcode.Medium)
	if err != nil {
		return nil, fmt.Errorf("label: qr encode: %w", err)
	}
	bm := qr.Bitmap() // [][]bool, [row][col]; true = dark module
	n := len(bm)
	if n == 0 {
		return nil, fmt.Errorf("label: empty qr bitmap for %q", code)
	}
	const (
		pad    = 16 // white margin around the QR (quiet zone, scannability)
		codeH  = 30 // code text line height
		titleH = 22 // title text line height
		gap    = 6  // gap between QR and text block
	)
	qrPx := n * modulePx
	w := qrPx + pad*2
	h := pad + qrPx + gap + codeH + titleH + pad
	var b bytes.Buffer
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, w, h, w, h)
	b.WriteString(`<rect width="100%" height="100%" fill="white"/>`)
	// QR modules — one <rect> per dark module. shape-rendering=crispEdges keeps
	// the squares sharp when the SVG is scaled for print.
	b.WriteString(`<g fill="black" shape-rendering="crispEdges">`)
	for y, row := range bm {
		for x, dark := range row {
			if dark {
				fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d"/>`, pad+x*modulePx, pad+y*modulePx, modulePx, modulePx)
			}
		}
	}
	b.WriteString(`</g>`)
	// Human-readable code (monospace) directly beneath the QR's quiet zone.
	codeBaseline := pad + qrPx + gap + codeH - 6
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-family="ui-monospace, 'JetBrains Mono', monospace" font-size="24" fill="black">%s</text>`,
		pad, codeBaseline, esc(code))
	// Title beneath the code.
	titleBaseline := codeBaseline + titleH
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-family="sans-serif" font-size="16" fill="black">%s</text>`,
		pad, titleBaseline, esc(title))
	b.WriteString(`</svg>`)
	return b.Bytes(), nil
}

// payload builds the QR's resolution URL: an absolute URL when baseURL is set,
// a relative "/via/{code}" path otherwise. A trailing slash on baseURL is
// trimmed so "https://x/" + "/via/c" does not double-slash.
func payload(baseURL, code string) string {
	if baseURL != "" {
		return strings.TrimRight(baseURL, "/") + "/via/" + code
	}
	return "/via/" + code
}

// esc XML-escapes a text node's three significant characters. Via-codes are
// alnum+dash, but titles are operator free text, so the escapes that would
// break SVG parsing are covered.
func esc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
