// view.go — the split screen (spec §4): header (wordmark · count · filter box),
// table region, hairline, detail pane, key hints. Widths come from
// TableWidths(m.width) BEFORE any styling; colors follow the styleguide rule
// (copper = actions/focus, phosphor = data values).
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m model) View() string {
	if len(m.rows) == 0 && m.status == "" {
		return emptyState(m)
	}
	var b strings.Builder
	b.WriteString(headerLine(m))
	b.WriteString("\n")
	b.WriteString(tableRegion(m))
	b.WriteString("\n")
	b.WriteString(hairline(m.width))
	b.WriteString("\n")
	b.WriteString(detailRegion(m))
	if m.overlay != nil {
		b.WriteString("\n" + m.overlay.view(m.width))
	}
	return b.String()
}

func headerLine(m model) string {
	left := fmt.Sprintf("go-parts ─ %d parts", len(m.rows))
	if m.low {
		left += styleCopperB.Render(" ─ LOW")
	}
	f := m.filter.View()
	if s := m.status; s != "" {
		return left + "  " + stylePhosphr.Render(s) + "\n" + f
	}
	return left + "  " + f
}

func tableRegion(m model) string {
	ws := TableWidths(m.width)
	var b strings.Builder
	b.WriteString(rowLine("MPN", "Desc", "Pkg", "Qty", ws, true))
	b.WriteString("\n")
	for i, r := range m.rows {
		qtyCell := fmt.Sprintf("%d", r.QtyOnHand)
		line := strings.TrimRight(rowLine(r.MPN, r.Description, r.Footprint, qtyCell, ws, false), " ")
		if r.QtyOnHand <= r.ReorderPoint {
			// Same comparison everywhere a low verdict appears (table badge,
			// low-mode fetch, stats) — QtyOnHand <= ReorderPoint, 0/0 low.
			line += " " + styleCopperB.Render(">LOW<")
		}
		marker := "  "
		if i == m.cursor {
			marker = styleCopper.Render("▸ ")
		}
		b.WriteString(marker + line + "\n")
	}
	return b.String()
}

// rowLine lays out one table row as four `>cell<` tokens. The delimiters are
// load-bearing: tests pin exact cell contents through them (the ULID-collision
// lesson — a bare Contains("RC1") would also match RC10). Cells truncate to
// their column width BEFORE the closing delimiter and pad AFTER it, so the
// tokens stay assertable and the columns stay aligned; a too-narrow terminal
// degrades to cramped but aligned. dim renders the whole line in faint chrome
// (the header row).
func rowLine(a, bb, c, d string, ws []int, dim bool) string {
	trunc := func(s string, w int) string {
		if w < 0 {
			w = 0
		}
		if len(s) > w {
			s = s[:w]
		}
		return s
	}
	cols := []int{ws[0] - 2, ws[1] - ws[0] - 2, ws[2] - ws[1] - 2, ws[3] - ws[2] - 2}
	var b strings.Builder
	for i, cell := range []string{a, bb, c, d} {
		cell = trunc(cell, cols[i])
		b.WriteString(">" + cell + "<")
		b.WriteString(strings.Repeat(" ", maxInt(0, cols[i]-len(cell))))
	}
	if dim {
		return styleDim.Render(b.String())
	}
	return b.String()
}

// hairline is the pane divider: the styleBorder token with its box sides
// disabled (a plain styleBorder.Render would wrap the divider in a full
// 3-line box) and the styleguide border color promoted to text foreground —
// one faint green line.
func hairline(width int) string {
	h := styleBorder.
		BorderTop(false).BorderRight(false).BorderBottom(false).BorderLeft(false).
		Foreground(lipgloss.Color("#223229"))
	return h.Render(strings.Repeat("─", maxInt(1, width-2)))
}

func detailRegion(m model) string {
	if !m.hasDetail {
		return styleDim.Render("(select a part — ↑↓ move)")
	}
	d := m.detail
	var b strings.Builder
	b.WriteString(d.Part.MPN + "  " + stylePhosphr.Render(d.Part.Description) + "\n")
	for _, s := range d.Stock {
		b.WriteString(fmt.Sprintf("  >%s< (%s): %s\n", s.Label, s.ViaCode, stylePhosphr.Render(fmt.Sprintf("%d", s.Quantity))))
	}
	for i, mv := range d.RecentMovements {
		if i >= 3 {
			b.WriteString("  …\n")
			break
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", stylePhosphr.Render(fmt.Sprintf("%+d", mv.Delta)), mv.Reason))
	}
	b.WriteString("\n" + styleCopper.Render("[a]djust") + " · " + styleCopper.Render("[l]ow") + " · " + styleCopper.Render("[r]efresh") + " · " + styleCopper.Render("[q]uit"))
	return b.String()
}

func emptyState(m model) string {
	if m.filter.Value() != "" {
		return "no matches — loosen the filter: " + m.filter.Value()
	}
	return "no parts yet — create one in the web UI (http://127.0.0.1:7890/ui/) or via the CLI"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
