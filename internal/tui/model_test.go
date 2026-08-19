package tui

// model_test.go — Update()-level tests with synthetic messages: no terminal,
// no pty. The debounce TIMER is not unit-tested (time.AfterFunc); its FIRE is
// modeled by delivering searchTickMsg directly — deterministic.

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
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

func TestCursorChangeFetchesDetailOnce(t *testing.T) {
	c, ps, ls, cs := newClientServerFull(t)
	p := &parts.Part{MPN: "D1", PartType: "local"}
	if err := ps.Create(p); err != nil {
		t.Fatal(err)
	}
	bin := &locations.Location{Label: "Bin"}
	if err := ls.Create(bin); err != nil {
		t.Fatal(err)
	}
	if err := cs.Add(bin.ID, p.ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	m := testModel(c)
	m.rows = []PartRow{{ID: p.ID, MPN: "D1"}}
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown}) // cursor 0 → stays? (no move) — force fetch path:
	m3, cmd := m2.(model).Update(forceDetailMsg{})
	if cmd == nil {
		t.Fatal("a detail fetch command must be issued")
	}
	_ = m3
	got := (<-waitMsg(cmd)).(detailMsg)
	if got.id != p.ID || got.err != nil || got.d.Part.MPN != "D1" {
		t.Fatalf("detailMsg = %+v", got)
	}
	m4, _ := m3.Update(got)
	if !m4.(model).hasDetail {
		t.Fatal("detailMsg must set hasDetail")
	}
}

func waitMsg(cmd tea.Cmd) <-chan tea.Msg {
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	return ch
}

func TestDetailSkipsUnchangedSelection(t *testing.T) {
	m := testModel(nil)
	m.rows = []PartRow{{ID: "p1"}}
	m.lastFetchedID = "p1"
	m.hasDetail = true
	m2, cmd := m.Update(forceDetailMsg{})
	if cmd != nil {
		t.Fatal("unchanged selection with detail shown must not refetch (spec §4)")
	}
	if !m2.(model).hasDetail {
		t.Fatal("the skip must not clear the detail pane")
	}
}

func TestAdjustRefusedWhenUnstocked(t *testing.T) {
	c, ps, _, _ := newClientServerFull(t)
	p := &parts.Part{MPN: "D2", PartType: "local"}
	if err := ps.Create(p); err != nil {
		t.Fatal(err)
	}
	m := testModel(c)
	m.rows = []PartRow{{ID: p.ID, MPN: "D2"}}
	m.detail = PartDetail{Part: PartRow{ID: p.ID}}
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m2.(model).overlay != nil {
		t.Fatal("a with zero stock rows must NOT open the form")
	}
	if !strings.Contains(m2.(model).status, "not stocked anywhere yet") {
		t.Fatalf("status must say why: %q", m2.(model).status)
	}
}

// TestSearchTickReReadsFilter is the Task 6 review carry-in: each fired
// debounce tick must re-read the CURRENT filter value, not a stale one.
func TestSearchTickReReadsFilter(t *testing.T) {
	c, ps := newClientServer(t)
	if err := ps.Create(&parts.Part{MPN: "ZZ9", PartType: "local"}); err != nil {
		t.Fatal(err)
	}
	m := testModel(c)
	m.filter.SetValue("ZZ9")
	m2, cmd := m.Update(searchTickMsg{})
	if cmd == nil {
		t.Fatal("searchTickMsg must issue a query command")
	}
	msg := cmd()
	loaded, ok := msg.(sessionLoadedMsg)
	if !ok {
		t.Fatalf("cmd must produce sessionLoadedMsg, got %T", msg)
	}
	if loaded.err != nil {
		t.Fatal(loaded.err)
	}
	m3, _ := m2.Update(loaded)
	if len(m3.(model).rows) != 1 || m3.(model).rows[0].MPN != "ZZ9" {
		t.Fatalf("rows = %+v; want exactly the seeded part", m3.(model).rows)
	}
}

// --- Task 7 review carry-ins (landed alongside Task 8's batch) --------------

// TestCursorMoveEmitsDetailFetch pins the cursor-follow contract: every cursor
// move returns a non-nil cmd that EXECUTES to a forceDetailMsg — the detail
// pane follows the selection with no other trigger.
func TestCursorMoveEmitsDetailFetch(t *testing.T) {
	m := testModel(nil)
	m.rows = []PartRow{{ID: "p1"}, {ID: "p2"}}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmd == nil {
		t.Fatal("cursor move must emit a detail-fetch cmd")
	}
	msg := cmd()
	if _, ok := msg.(forceDetailMsg); !ok {
		t.Fatalf("cursor-move cmd must execute to forceDetailMsg, got %T", msg)
	}
}

// TestDetailSkipConjunctionSides pins BOTH sides of the refetch-skip rule: the
// skip fires only when the id is unchanged AND detail is currently shown —
// either side false must still fetch.
func TestDetailSkipConjunctionSides(t *testing.T) {
	// different id + hasDetail=true → fetch.
	m := testModel(nil)
	m.rows = []PartRow{{ID: "p2"}}
	m.lastFetchedID = "p1"
	m.hasDetail = true
	if _, cmd := m.Update(forceDetailMsg{}); cmd == nil {
		t.Fatal("different id with detail shown must refetch")
	}
	// same id + hasDetail=false → fetch.
	m2 := testModel(nil)
	m2.rows = []PartRow{{ID: "p1"}}
	m2.lastFetchedID = "p1"
	m2.hasDetail = false
	if _, cmd := m2.Update(forceDetailMsg{}); cmd == nil {
		t.Fatal("same id with detail NOT shown must refetch")
	}
}

// TestRefreshRRefetches pins `r`: refresh re-issues the fetch commands.
func TestRefreshRRefetches(t *testing.T) {
	c, _ := newClientServer(t)
	m := testModel(c)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd == nil {
		t.Fatal("r must re-issue the fetch commands")
	}
}

// TestSessionLoadedClearsStaleDetail pins the stale-detail fix: a successful
// session load REPLACES the rows (filter narrowed, refresh, low toggle) — the
// detail pane must not keep rendering the previously selected part, which may
// no longer be selected (or even present) in the new row set.
func TestSessionLoadedClearsStaleDetail(t *testing.T) {
	m := testModel(nil)
	m.rows = []PartRow{{ID: "old"}}
	m.detail = PartDetail{Part: PartRow{ID: "old"}}
	m.hasDetail = true
	m2, _ := m.Update(sessionLoadedMsg{rows: []PartRow{{ID: "new1"}, {ID: "new2"}}})
	if m2.(model).hasDetail {
		t.Fatal("sessionLoadedMsg must clear hasDetail — the pane must not keep the stale part")
	}
}
