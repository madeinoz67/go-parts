// model.go — the Bubble Tea root model (spec §4): the split view's list
// region owns the filter box, the rows, and the cursor; the detail region
// (Task 7) and adjust overlay (Task 8) hang off the same root. Update is pure
// state transition; every network effect is a tea.Cmd — that split is what
// makes the model testable without a terminal.
package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

const debounceDelay = 200 * time.Millisecond

type sessionLoadedMsg struct {
	rows []PartRow
	err  error
}

type searchTickMsg struct{}

// detailMsg is the GET /parts/{id}/stock result the detail pane consumes.
// Declared here per the Task 6 interface contract; Task 7's cursor-follow
// command produces it.
type detailMsg struct {
	id  string
	d   PartDetail
	err error
}

type fetchListCmd func() tea.Msg

// model is the root Bubble Tea model. Field set is the Task 6 contract —
// later tasks extend, never rename.
type model struct {
	c             *Client
	filter        textinput.Model
	rows          []PartRow
	cursor        int
	detail        PartDetail
	hasDetail     bool
	low           bool
	status        string // phosphor status line (fetch errors, transient notices)
	width         int
	height        int
	overlay       *adjustModel
	lastFetchedID string
}

func newModel(c *Client) model {
	f := textinput.New()
	f.Placeholder = "filter…"
	f.Prompt = ""
	f.Focus() // the filter is the always-on input; an unfocused textinput drops keys
	return model{c: c, filter: f, width: 80, height: 24}
}

// NewProgramModel is the one cmd-surface seam into the package: tea.NewProgram
// needs a tea.Model and `model` is deliberately unexported (the terminal stays
// an internal implementation detail — cmd/go-parts wires it, nothing else).
func NewProgramModel(c *Client) tea.Model { return newModel(c) }

func (m model) Init() tea.Cmd { return fetchAll(m.c, m.low) }

// fetchAll is the single list-fetch command for every mode (browse, filtered,
// low) — the mode lives in the call site's params, not in separate commands.
func fetchAll(c *Client, low bool) tea.Cmd {
	return func() tea.Msg {
		if low {
			rows, err := c.LowStock()
			return sessionLoadedMsg{rows: rows, err: err}
		}
		rows, err := c.BrowseAll()
		return sessionLoadedMsg{rows: rows, err: err}
	}
}

func fetchSearch(c *Client, q string) tea.Cmd {
	return func() tea.Msg {
		rows, err := c.Search(q)
		return sessionLoadedMsg{rows: rows, err: err}
	}
}

// forceDetailMsg (re)requests the detail for the CURRENT cursor row. Cursor
// moves emit it automatically; it also exists as an exported seam so refresh
// (`r`) and post-adjust reload reuse the same path.
type forceDetailMsg struct{}

func fetchDetail(c *Client, id string) tea.Cmd {
	return func() tea.Msg {
		d, err := c.GetPart(id)
		return detailMsg{id: id, d: d, err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case sessionLoadedMsg:
		if msg.err != nil {
			m.status = msg.err.Error() // table keeps its last good rows — nothing blanked
			return m, nil
		}
		m.status = ""
		m.rows = msg.rows
		// Stale-detail fix (Task 7 review carry-in): the rows just changed
		// under the pane (filter narrowed, refresh, low toggle) — the part the
		// detail pane still shows may no longer be selected, or present at
		// all. Drop hasDetail so the pane stops rendering it, and re-arm a
		// detail fetch for the (possibly clamped) cursor row so the pane
		// repopulates without waiting for a cursor move. The re-arm is also
		// what keeps `r` and post-adjust refresh deterministic: whatever
		// order the list and detail fetches land in, the LAST fetch re-settles
		// the pane.
		m.hasDetail = false
		if m.cursor >= len(m.rows) {
			m.cursor = len(m.rows) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		return m, tea.Cmd(func() tea.Msg { return forceDetailMsg{} })
	case searchTickMsg:
		if q := m.filter.Value(); q != "" {
			return m, fetchSearch(m.c, q)
		}
		return m, fetchAll(m.c, m.low)
	case forceDetailMsg:
		if len(m.rows) == 0 {
			return m, nil
		}
		id := m.rows[m.cursor].ID
		if id == m.lastFetchedID && m.hasDetail {
			return m, nil // unchanged selection — no refetch storm (spec §4)
		}
		m.lastFetchedID = id
		return m, fetchDetail(m.c, id)
	case detailMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			return m, nil
		}
		m.status = ""
		m.detail = msg.d
		m.hasDetail = true
	case adjustDoneMsg:
		// The overlay's network submit settled. Failure keeps the form open —
		// route the msg INTO it so the engine's own message renders in-form
		// and the fields survive for fix-and-retry. Success closes it and
		// refreshes BOTH panes through the same path `r` uses: stock changed,
		// so the detail rows AND the table's QtyOnHand are stale.
		if m.overlay == nil {
			return m, nil // stray (overlay already closed) — nothing to do
		}
		m.overlay, _ = m.overlay.update(msg) // err → stays open; success → nil
		if m.overlay != nil {
			return m, nil
		}
		m.hasDetail = false // the detail is stale by construction after a write
		return m, tea.Batch(tea.Cmd(func() tea.Msg { return forceDetailMsg{} }), fetchAll(m.c, m.low))
	case tea.KeyMsg:
		// q/esc quit only at the ROOT: with the overlay open, both keys route
		// INTO it (its update closes on Esc; "q" is typed text in the focused
		// field). Checking q before the overlay routing would destroy a
		// half-typed reason ("seq", "req"…) on its first keystroke.
		if k := msg.String(); (k == "q" || k == "esc") && m.overlay == nil {
			return m, tea.Quit
		}
		if m.overlay != nil {
			var cmd tea.Cmd
			m.overlay, cmd = m.overlay.update(msg)
			if m.overlay != nil && m.overlay.submitted {
				// The overlay validated the form and asked to submit. The
				// ROOT owns the client, so it dispatches the real network cmd
				// here (dropping the overlay's marker cmd) and consumes the
				// flag — a later key must not re-fire a submit that already
				// went out.
				m.overlay.submitted = false
				return m, m.overlay.submitWith(m.c)
			}
			return m, cmd
		}
		switch msg.Type {
		case tea.KeyUp:
			if m.cursor > 0 {
				m.cursor--
			}
			return m, tea.Cmd(func() tea.Msg { return forceDetailMsg{} })
		case tea.KeyDown:
			if m.cursor < len(m.rows)-1 {
				m.cursor++
			}
			return m, tea.Cmd(func() tea.Msg { return forceDetailMsg{} })
		case tea.KeyRunes:
			switch string(msg.Runes) {
			case "l":
				m.low = !m.low
				return m, fetchAll(m.c, m.low)
			case "a":
				// Stale-detail guard (final review): a session load clears
				// hasDetail and re-arms a fetch; until that detailMsg lands,
				// m.detail still holds the PREVIOUS part — `a` must not open
				// the adjust form against it. No-op until detail lands.
				if !m.hasDetail {
					return m, nil
				}
				if len(m.detail.Stock) == 0 {
					m.status = "not stocked anywhere yet — stock it first (stock_part / web UI)"
					return m, nil
				}
				m.overlay = newAdjustModel(m.detail)
				return m, textinput.Blink
			case "r":
				m.hasDetail = false
				return m, tea.Batch(tea.Cmd(func() tea.Msg { return forceDetailMsg{} }), fetchAll(m.c, m.low))
			}
			// any other typing lands in the filter and re-arms the debounce
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			return m, tea.Batch(cmd, debounce())
		default:
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

// debounce re-arms the 200ms timer. The timer's Send lands as searchTickMsg;
// tests deliver searchTickMsg directly (deterministic), so debounce itself is
// never on the critical test path.
//
// tea.Tick fires unconditionally after the delay; rapid typing batches
// multiple ticks — harmless: each searchTickMsg re-reads the CURRENT filter
// value, so the last keystroke always wins and identical queries are
// idempotent GETs. The 200ms is delivered as-spaced-by-tea.Tick, not a
// resettable timer — acceptable because queries are idempotent; no
// timer-state is kept.
func debounce() tea.Cmd {
	return tea.Tick(debounceDelay, func(time.Time) tea.Msg { return searchTickMsg{} })
}
