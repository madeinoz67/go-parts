package tui

// adjust_test.go — the stock-adjust overlay's state machine: focus cycling,
// the location picker, in-form validation (no submit on invalid input), Esc
// closure, and the root-model submit routing (the overlay itself holds no
// client). The end-to-end test drives a REAL adjust through the REST server.

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
)

func formDetail() PartDetail {
	return PartDetail{
		Part:  PartRow{ID: "p1", MPN: "M1"},
		Stock: []StockEntry{{Label: "Drawer A1", ViaCode: "L-X", Quantity: 45}, {Label: "Bin B3", ViaCode: "L-Y", Quantity: 5}},
	}
}

func TestAdjustTabCyclesFocus(t *testing.T) {
	a := newAdjustModel(formDetail())
	a2, _ := a.update(tea.KeyMsg{Type: tea.KeyTab})
	if a2.focus != 1 {
		t.Fatalf("tab: focus = %d, want 1", a2.focus)
	}
}

func TestAdjustLocationArrows(t *testing.T) {
	a := newAdjustModel(formDetail())
	a2, _ := a.update(tea.KeyMsg{Type: tea.KeyRight}) // locIdx 0 → 1 (focus 0 = location)
	if a2.locIdx != 1 {
		t.Fatalf("right arrow must move location: %d", a2.locIdx)
	}
	a3, _ := a2.update(tea.KeyMsg{Type: tea.KeyLeft})
	if a3.locIdx != 0 {
		t.Fatalf("left arrow must move back: %d", a3.locIdx)
	}
}

func TestAdjustEnterValidatesAndSubmits(t *testing.T) {
	a := newAdjustModel(formDetail())
	a.delta.SetValue("-5")
	a.reason.SetValue("bench")
	a2, cmd := a.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("valid form must submit")
	}
	_ = a2

	b := newAdjustModel(formDetail())
	b.delta.SetValue("-5")
	// reason empty
	b2, cmd2 := b.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd2 != nil || b2.err == nil {
		t.Fatal("empty reason must be an in-form error, no submit")
	}

	c := newAdjustModel(formDetail())
	c.delta.SetValue("2.5")
	c.reason.SetValue("x")
	c2, cmd3 := c.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd3 != nil || c2.err == nil {
		t.Fatal("fractional delta must be an in-form error, no submit")
	}
}

func TestAdjustEscCloses(t *testing.T) {
	a := newAdjustModel(formDetail())
	a2, _ := a.update(tea.KeyMsg{Type: tea.KeyEsc})
	if a2 != nil {
		t.Fatal("Esc must close the overlay (nil model)")
	}
}

func TestAdjustDoneMsgRefreshes(t *testing.T) {
	c, ps, ls, cs := newClientServerFull(t)
	p := &parts.Part{MPN: "ADJ1", PartType: "local"}
	if err := ps.Create(p); err != nil {
		t.Fatal(err)
	}
	bin := &locations.Location{Label: "Bin"}
	if err := ls.Create(bin); err != nil {
		t.Fatal(err)
	}
	if err := cs.Add(bin.ID, p.ID, 10, nil); err != nil {
		t.Fatal(err)
	}
	m := testModel(c)
	m.rows = []PartRow{{ID: p.ID, MPN: "ADJ1"}}
	m.hasDetail = true
	// Brief adaptation: fetch the detail through the REAL wire instead of
	// hand-building it — the submit path needs Stock[].LocationID, which only
	// exists once the server emits it (this task's wire addition). Fetching it
	// here makes the test the end-to-end proof of that addition.
	d, err := c.GetPart(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Stock) != 1 || d.Stock[0].LocationID != bin.ID {
		t.Fatalf("stock wire must carry the location id: %+v", d.Stock)
	}
	m.detail = d
	m.overlay = newAdjustModel(m.detail)
	m.overlay.delta.SetValue("-3")
	m.overlay.reason.SetValue("test")
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("submit must run")
	}
	m3, _ := m2.Update(waitMsgValue(cmd)) // adjustDoneMsg{err:nil}
	if m3.(model).overlay != nil {
		t.Fatal("successful adjust must close the overlay")
	}
	d2, _ := c.GetPart(p.ID)
	if d2.Stock[0].Quantity != 7 {
		t.Fatalf("stock after -3 = %d, want 7 (real end-to-end through REST)", d2.Stock[0].Quantity)
	}
}

func waitMsgValue(cmd tea.Cmd) tea.Msg { return cmd() }
