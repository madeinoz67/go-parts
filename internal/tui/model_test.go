package tui

// model_test.go — Update()-level tests with synthetic messages: no terminal,
// no pty. The debounce TIMER is not unit-tested (time.AfterFunc); its FIRE is
// modeled by delivering searchTickMsg directly — deterministic.

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func testModel(c *Client) model { m := newModel(c); return m }

func TestFilterTypesIntoBox(t *testing.T) {
	m := testModel(nil)
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if m2.(model).filter.Value() != "1" {
		t.Fatalf("typing must land in the filter: %q", m2.(model).filter.Value())
	}
}

func TestLowTogglesAndQueries(t *testing.T) {
	c, ps := newClientServer(t)
	_ = ps
	m := testModel(c)
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if cmd == nil {
		t.Fatal("low toggle must issue a query command")
	}
	if !m2.(model).low {
		t.Fatal("low must be on after first toggle")
	}
}

func TestCursorMovesAndClamps(t *testing.T) {
	m := testModel(nil)
	m.rows = []PartRow{{MPN: "a"}, {MPN: "b"}}
	down := tea.KeyMsg{Type: tea.KeyDown}
	m2, _ := m.Update(down)
	if m2.(model).cursor != 1 {
		t.Fatalf("cursor after down = %d, want 1", m2.(model).cursor)
	}
	m3, _ := m2.Update(down) // past the end — clamp
	if m3.(model).cursor != 1 {
		t.Fatalf("cursor must clamp: %d", m3.(model).cursor)
	}
	m4, _ := m3.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m4.(model).cursor != 0 {
		t.Fatalf("cursor up = %d", m4.(model).cursor)
	}
}

func TestSessionLoadedReplacesRows(t *testing.T) {
	m := testModel(nil)
	m2, _ := m.Update(sessionLoadedMsg{rows: []PartRow{{MPN: "x"}}})
	if len(m2.(model).rows) != 1 {
		t.Fatal("sessionLoadedMsg must replace the table rows")
	}
	m3, _ := m2.Update(sessionLoadedMsg{err: errBoom})
	if m3.(model).status == "" {
		t.Fatal("a fetch error must set the status line (loud, not silent)")
	}
	m4, _ := m3.Update(sessionLoadedMsg{rows: []PartRow{{MPN: "y"}}})
	if m4.(model).status != "" || len(m4.(model).rows) != 1 {
		t.Fatal("a good load clears the status line and replaces rows")
	}
}

var errBoom = errors.New("boom")
