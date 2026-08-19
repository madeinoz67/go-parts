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
// is what keeps the form testable without a server. What the routing actually
// guarantees against a double-applied delta: the `submitting` latch arms on
// the validated Enter and holds until the matching adjustDoneMsg lands, so no
// second dispatch can go out while one is in flight, and after a FAILED
// submit the latch releases (the old attempt cannot re-fire; the form accepts
// edits and a fresh Enter re-validates and retries).
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
	// submitting is the in-flight latch for the network submit: armed on the
	// Enter that passes validation, held while the PATCH is out, and released
	// only by the matching adjustDoneMsg (success OR failure). While held,
	// Enter is a no-op — without it a second Enter during the round trip
	// re-validates and dispatches a SECOND signed PATCH, double-applying the
	// delta. The release-on-error half is what leaves the form editable and
	// retryable after a failed submit.
	submitting bool
	err        error
}

func newAdjustModel(d PartDetail) *adjustModel {
	dl := textinput.New()
	dl.Placeholder = "-5"
	rs := textinput.New()
	rs.Placeholder = "why (required)"
	a := &adjustModel{part: d, delta: dl, reason: rs}
	a.syncFocus()
	return a
}

// syncFocus aligns the bubbles-level focus of the two text inputs with the
// logical focus index. An unfocused textinput silently drops runes, so
// without this the "focused" field could never actually be typed into — and
// the root's overlay routing (which must let a bare "q" land as text) would
// be observable only as swallowed keys. Focus 0 (the location picker)
// unfocuses both: the picker is not a text input. The blink cmds Focus()
// returns are discarded; cursor rendering is Task 9's concern.
func (a *adjustModel) syncFocus() {
	if a.focus == 1 {
		a.delta.Focus()
	} else {
		a.delta.Blur()
	}
	if a.focus == 2 {
		a.reason.Focus()
	} else {
		a.reason.Blur()
	}
}

// update transitions the overlay. A nil model return means CLOSED (Esc, or a
// successful adjustDoneMsg routed in by the root); a non-nil cmd is returned
// for the text inputs' own blink/rune commands and the submit marker.
func (a *adjustModel) update(msg tea.Msg) (*adjustModel, tea.Cmd) {
	switch msg := msg.(type) {
	case adjustDoneMsg:
		// The in-flight latch releases with the result on BOTH outcomes:
		// success closes the form (the latch is moot), failure must leave it
		// editable and retryable.
		a.submitting = false
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
			a.syncFocus()
			return a, nil
		case tea.KeyShiftTab:
			a.focus = (a.focus + 2) % 3
			a.syncFocus()
			return a, nil
		case tea.KeyEnter:
			if a.submitting {
				// A submit is in flight — checked FIRST, before validation:
				// a second Enter must not re-validate and re-arm another
				// dispatch (Adjust applies a signed delta; two dispatches
				// double-apply it).
				return a, nil
			}
			if a.validate() {
				a.submitted = true
				a.submitting = true
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
