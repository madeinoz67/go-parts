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
	// Deterministic check instead: ALL SIX token variables carry the styleguide
	// §2 hexes — copper/copper-bright/phosphor/dim foregrounds, the cursor
	// row's copper left-border, and the hairline pane border.
	pinHex(t, "copper (actions/focus)", styleCopper.GetForeground(), "#c98a4b")
	pinHex(t, "copper-bright (emphasized action)", styleCopperB.GetForeground(), "#e6a868")
	pinHex(t, "phosphor (data values)", stylePhosphr.GetForeground(), "#6fd9c9")
	pinHex(t, "dim (styleguide --text-faint)", styleDim.GetForeground(), "#4d6459")
	pinHex(t, "cursor-row left border (copper)", styleCursorRow.GetBorderLeftForeground(), "#c98a4b")
	pinHex(t, "pane border (hairline)", styleBorder.GetBorderTopForeground(), "#223229")
}

// pinHex asserts a lipgloss TerminalColor is exactly the styleguide hex, via
// the concrete-type assertion documented in TestStyleColorRuleHolds.
func pinHex(t *testing.T, what string, c lipgloss.TerminalColor, want string) {
	t.Helper()
	hex, ok := c.(lipgloss.Color)
	if !ok || string(hex) != want {
		t.Errorf("%s = %v, want %s", what, c, want)
	}
}

func TestTableWidthsMonotonic(t *testing.T) {
	ws := TableWidths(120)
	if len(ws) != 4 || ws[0] >= ws[1] || ws[1] >= ws[2] || ws[2] >= ws[3] {
		t.Fatalf("TableWidths must be 4 ascending offsets, got %v", ws)
	}
}
