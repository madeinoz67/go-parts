// styles.go — the style guide's token language mapped to the terminal
// (styleguide §2 + PRD §5.11): copper = a thing you can do (actions, focus,
// cursor); phosphor = live data values; NEVER swapped. Lip Gloss's renderer
// profile degrades truecolor → 256 → 16 → monochrome automatically; layout
// widths are computed before styling so color capability never affects layout.
package tui

import "github.com/charmbracelet/lipgloss"

var (
	styleCopper    = lipgloss.NewStyle().Foreground(lipgloss.Color("#c98a4b"))                                                              // actions/focus
	styleCopperB   = lipgloss.NewStyle().Foreground(lipgloss.Color("#e6a868"))                                                              // emphasized action
	stylePhosphr   = lipgloss.NewStyle().Foreground(lipgloss.Color("#6fd9c9"))                                                              // data values
	styleDim       = lipgloss.NewStyle().Foreground(lipgloss.Color("#4d6459"))                                                              // faint chrome (styleguide --text-faint)
	styleCursorRow = lipgloss.NewStyle().Background(lipgloss.Color("#0b1411")).BorderLeft(true).BorderForeground(lipgloss.Color("#c98a4b")) // selected row — copper left rule (web .selected)
	styleBorder    = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("#223229"))                        // hairline panes
)

// TableWidths returns the 4 ascending column offsets (MPN, Desc, Pkg, Qty)
// for a terminal of the given width. Computed BEFORE any styling so wrapping
// is deterministic across color profiles.
func TableWidths(total int) []int {
	// MPN 24% · Desc 46% · Pkg 14% · Qty rest — minimums keep narrow terminals sane.
	mpn := clamp(total*24/100, 16, 26)
	desc := clamp(total*46/100, 24, 60)
	pkg := clamp(total*14/100, 8, 12)
	qty := total - mpn - desc - pkg
	if qty < 6 {
		qty = 6
	}
	return []int{mpn, mpn + desc, mpn + desc + pkg, mpn + desc + pkg + qty}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
