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
	rows  []PartRow
	total int
	err   error
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
		if m.cursor >= len(m.rows) {
			m.cursor = len(m.rows) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
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
	case tea.KeyMsg:
		if k := msg.String(); k == "q" || (k == "esc" && m.overlay == nil) {
			return m, tea.Quit
		}
		if m.overlay != nil {
			var cmd tea.Cmd
			m.overlay, cmd = m.overlay.update(msg)
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

// View is Task 9's deliverable (the render pass). The root model must satisfy
// tea.Model from day one — Update returns the model boxed as tea.Model — so a
// minimal View stands in until Task 9 replaces it.
func (m model) View() string { return "" }

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

// adjustModel is the stock-adjust overlay — Task 8's deliverable. This stub
// exists so the root model's exact field set (overlay *adjustModel) and its
// Update dispatch compile in Task 6; Task 8 replaces it with the real overlay.
type adjustModel struct{}

func (a *adjustModel) update(_ tea.Msg) (*adjustModel, tea.Cmd) { return a, nil }
