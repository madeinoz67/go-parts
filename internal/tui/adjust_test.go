package tui

// adjust_test.go — the stock-adjust overlay's state machine: focus cycling,
// the location picker, in-form validation (no submit on invalid input), Esc
// closure, and the root-model submit routing (the overlay itself holds no
// client). The end-to-end test drives a REAL adjust through the REST server.

import (
	"errors"
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

// --- Task 8 review findings -------------------------------------------------

// TestAdjustDoubleEnterSingleDispatch pins the in-flight latch: a second
// Enter while the PATCH is out must be a no-op (Adjust applies a signed
// delta — a second dispatch double-applies it), and the latch must release
// when the result lands: success closes the form, failure leaves it open,
// editable, and retryable.
func TestAdjustDoubleEnterSingleDispatch(t *testing.T) {
	c, ps, ls, cs := newClientServerFull(t)
	p := &parts.Part{MPN: "ADJ2", PartType: "local"}
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
	m.rows = []PartRow{{ID: p.ID, MPN: "ADJ2"}}
	d, err := c.GetPart(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	m.detail = d
	m.overlay = newAdjustModel(d)
	m.overlay.delta.SetValue("-3")
	m.overlay.reason.SetValue("test")

	// First Enter: the root routes to submitWith — ONE dispatch, latch armed.
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("the first Enter on a valid form must dispatch the submit")
	}
	if !m2.(model).overlay.submitting {
		t.Fatal("the in-flight latch must be armed once the submit is dispatched")
	}
	// Second Enter while the PATCH is in flight: a NO-OP — no new cmd, latch held.
	m3, cmd2 := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd2 != nil {
		t.Fatal("a second Enter while a submit is in flight must not dispatch again")
	}
	if !m3.(model).overlay.submitting {
		t.Fatal("the latch must hold until the adjustDoneMsg lands")
	}
	// The dispatch goes out exactly once — executing the cmd IS the PATCH —
	// and its result message closes the overlay (clearing the latch with it).
	done := cmd()
	if _, ok := done.(adjustDoneMsg); !ok {
		t.Fatalf("the submit cmd must produce adjustDoneMsg, got %T", done)
	}
	m4, refresh := m3.Update(done)
	if m4.(model).overlay != nil {
		t.Fatal("a successful adjust must close the overlay")
	}
	if refresh == nil {
		t.Fatal("success must refresh both panes")
	}
	d2, err := c.GetPart(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Stock[0].Quantity != 7 {
		t.Fatalf("two Enters must apply the delta ONCE: qty = %d, want 7", d2.Stock[0].Quantity)
	}

	// Failure leg: the latch must RELEASE on error so the form can retry.
	mm := m4.(model)
	mm.overlay = newAdjustModel(d2)
	mm.overlay.delta.SetValue("-3")
	mm.overlay.reason.SetValue("retry")
	m5, cmdB := mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmdB == nil {
		t.Fatal("a fresh valid Enter after reopening must dispatch")
	}
	m6, _ := m5.Update(adjustDoneMsg{err: errors.New("500: engine says no")})
	if m6.(model).overlay == nil {
		t.Fatal("a failed adjust must keep the form open")
	}
	if m6.(model).overlay.submitting {
		t.Fatal("the latch must release on a failed adjust — the form must be retryable")
	}
	m7, cmdC := m6.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmdC == nil {
		t.Fatal("after a released latch, Enter must re-validate and dispatch the retry")
	}
	doneC := cmdC()
	if _, ok := doneC.(adjustDoneMsg); !ok {
		t.Fatalf("the retry cmd must produce adjustDoneMsg, got %T", doneC)
	}
	m8, _ := m7.Update(doneC)
	if m8.(model).overlay != nil {
		t.Fatal("the successful retry must close the overlay")
	}
	d3, _ := c.GetPart(p.ID)
	if d3.Stock[0].Quantity != 4 {
		t.Fatalf("after the retry, qty = %d, want 4 (10 −3 once, then −3 once)", d3.Stock[0].Quantity)
	}
}

// TestAdjustDoneRefreshesViaBatch pins the success-refresh shape: a landed
// adjustDoneMsg at the root closes the overlay, marks the detail stale, and
// returns a tea.Batch whose executed cmds include the forceDetailMsg re-arm —
// both panes refresh through the same path `r` uses.
func TestAdjustDoneRefreshesViaBatch(t *testing.T) {
	c, _ := newClientServer(t)
	m := testModel(c)
	m.rows = []PartRow{{ID: "p1"}}
	m.hasDetail = true
	m.overlay = newAdjustModel(formDetail())
	m.overlay.submitting = true // the PATCH is in flight when the result lands
	m2, cmd := m.Update(adjustDoneMsg{})
	if m2.(model).overlay != nil {
		t.Fatal("a successful adjustDoneMsg must close the overlay")
	}
	if m2.(model).hasDetail {
		t.Fatal("the detail must be marked stale after a write")
	}
	if cmd == nil {
		t.Fatal("success must return a refresh batch")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("the refresh must be a tea.Batch, got %T", msg)
	}
	found := false
	for _, bc := range batch {
		if _, ok := bc().(forceDetailMsg); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("the batch must contain a cmd producing forceDetailMsg")
	}
}
