// adjust.go — the stock-adjust overlay (spec §1/§4): a single centered form
// with a location ◂▸ picker (preseeded from the part's stock rows), a signed
// delta input, and a REQUIRED reason (movement history is an audit trail — no
// default-reason shortcuts). Tab cycles fields, Enter validates + submits,
// Esc closes. Failures render INSIDE the form with the engine's own message
// so input is preserved for fix-and-retry.
//
// The overlay deliberately holds NO client reference. update() is a pure
// state machine: a validated Enter sets `submitted` and returns a marker cmd;
// the ROOT model (which owns the Client) sees the flag right after routing,
// consumes it, and dispatches submitWith(m.c) — the single submit path. That
// is what keeps the form testable without a server and makes a double-applied
// delta structurally impossible.
package tui

import (
	"errors"
	"strconv"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// adjustDoneMsg is the result of one Client.Adjust round trip: err == nil
// means the movement landed (close the overlay); err != nil carries the
// engine's own message (HTTP status + body verbatim) to render IN the form.
type adjustDoneMsg struct{ err error }

// adjustSubmitIntentMsg is the marker a VALIDATED Enter returns from
// update(). The root model intercepts the `submitted` flag and returns the
// real network cmd (submitWith) in this cmd's place, so under the event loop
// the message never actually fires — it exists so direct update() callers
// observe "the form validated and wants to submit" as a non-nil tea.Cmd.
// Nothing may dispatch a second submit off it: Adjust applies a signed delta,
// and a double dispatch would double-apply it.
type adjustSubmitIntentMsg struct{}

var adjustSubmitIntent = tea.Cmd(func() tea.Msg { return adjustSubmitIntentMsg{} })

// In-form validation sentinels. The view renders a.err verbatim, so these
// strings are the UI copy.
var (
	errNoStock        = errors.New("not stocked anywhere yet — stock it first")
	errReasonRequired = errors.New("reason is required (movement history is the audit trail)")
	errDeltaIntegral  = errors.New("delta must be an integer (e.g. -5)")
)

// adjustModel is the Task 8 overlay. Field set per the brief's interface
// contract (Task 9's View renders it): part is the detail snapshot the form
// was opened against; locIdx is the selected stock row; delta/reason are the
// two text inputs; focus is 0 = location, 1 = delta, 2 = reason.
type adjustModel struct {
	part   PartDetail
	locIdx int
	delta  textinput.Model
	reason textinput.Model
	focus  int
	// submitted is set on the Enter that PASSES validation and consumed
	// (cleared) by the root when it dispatches submitWith — a later key must
	// not re-fire a submit that already went out.
	submitted bool
	err       error
}

func newAdjustModel(d PartDetail) *adjustModel {
	dl := textinput.New()
	dl.Placeholder = "-5"
	rs := textinput.New()
	rs.Placeholder = "why (required)"
	return &adjustModel{part: d, delta: dl, reason: rs}
}

// update transitions the overlay. A nil model return means CLOSED (Esc, or a
// successful adjustDoneMsg routed in by the root); a non-nil cmd is returned
// for the text inputs' own blink/rune commands and the submit marker.
func (a *adjustModel) update(msg tea.Msg) (*adjustModel, tea.Cmd) {
	switch msg := msg.(type) {
	case adjustDoneMsg:
		if msg.err != nil {
			a.err = msg.err // in-form error — fields survive for retry
			return a, nil
		}
		return nil, nil // success — closed (the root refreshes both panes)
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			return nil, nil
		case tea.KeyTab:
			a.focus = (a.focus + 1) % 3
			return a, nil
		case tea.KeyShiftTab:
			a.focus = (a.focus + 2) % 3
			return a, nil
		case tea.KeyEnter:
			if a.validate() {
				a.submitted = true
				return a, adjustSubmitIntent
			}
			return a, nil
		case tea.KeyLeft, tea.KeyRight:
			if a.focus == 0 && len(a.part.Stock) > 0 {
				if msg.Type == tea.KeyRight {
					a.locIdx = (a.locIdx + 1) % len(a.part.Stock)
				} else {
					a.locIdx = (a.locIdx + len(a.part.Stock) - 1) % len(a.part.Stock)
				}
				return a, nil
			}
		case tea.KeyRunes:
			if a.focus == 0 {
				return a, nil // the location field is a picker, not a text input
			}
		}
		// Anything else (typing, cursor keys inside an input, backspace…)
		// routes to whichever text input currently has focus.
		if a.focus == 1 {
			var cmd tea.Cmd
			a.delta, cmd = a.delta.Update(msg)
			return a, cmd
		}
		if a.focus == 2 {
			var cmd tea.Cmd
			a.reason, cmd = a.reason.Update(msg)
			return a, cmd
		}
	}
	return a, nil
}

// validate re-checks the whole form, setting a.err (cleared when it passes).
// Order per the brief's submit(): stock exists → reason present → delta
// integral — so the user is told the cheapest problem first.
func (a *adjustModel) validate() bool {
	a.err = nil
	switch {
	case len(a.part.Stock) == 0:
		a.err = errNoStock
	case a.reason.Value() == "":
		a.err = errReasonRequired
	default:
		if _, err := strconv.Atoi(a.delta.Value()); err != nil {
			a.err = errDeltaIntegral
		}
	}
	return a.err == nil
}

// submitWith builds the network submit against the ROOT model's client — the
// only point where a Client enters the overlay's lifecycle. Call it only
// after validate() passed (the root's submitted-flag routing guarantees
// that); the strconv error is deliberately discarded because validate()
// already gated integrality.
func (a *adjustModel) submitWith(c *Client) tea.Cmd {
	loc := a.part.Stock[a.locIdx]
	delta, _ := strconv.Atoi(a.delta.Value())
	return func() tea.Msg {
		return adjustDoneMsg{err: c.Adjust(loc.LocationID, a.part.Part.ID, delta, a.reason.Value())}
	}
}
