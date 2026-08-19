package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestStyleColorRuleHolds(t *testing.T) {
	// The styleguide rule, enforced: copper renders actions, phosphor renders
	// data. If someone swaps the roles the hexes move — pin the mapping.
	//
	// API note (lipgloss v1.1.0): Color has no TrueColor() method and
	// GetForeground() returns the TerminalColor interface (no String()), so
	// the hex is pinned by asserting the concrete lipgloss.Color — a string
	// type — and comparing it directly. A failed assertion also covers the
	// unset case (NoColor{}), which the original nil-check guarded.
	got := styleCopper.Render(">")
	_ = got // truecolor terminals escape-color; plain == unchanged is WRONG only under NO_COLOR
	// Deterministic check instead: the style VARIABLES carry the right hex.
	fg, ok := styleCopper.GetForeground().(lipgloss.Color)
	if !ok || string(fg) != "#c98a4b" {
		t.Errorf("copper = %v, want #c98a4b", styleCopper.GetForeground())
	}
	fg, ok = stylePhosphr.GetForeground().(lipgloss.Color)
	if !ok || string(fg) != "#6fd9c9" {
		t.Errorf("phosphor = %v, want #6fd9c9", stylePhosphr.GetForeground())
	}
}

func TestTableWidthsMonotonic(t *testing.T) {
	ws := TableWidths(120)
	if len(ws) != 4 || ws[0] >= ws[1] || ws[1] >= ws[2] || ws[2] >= ws[3] {
		t.Fatalf("TableWidths must be 4 ascending offsets, got %v", ws)
	}
}
