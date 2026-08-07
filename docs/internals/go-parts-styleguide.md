# go-parts Style Guide — Hacker / Hi-Tech

**Author:** Stephen Eaton
**Status:** v1.0
**Companion to:** `go-parts-prd.md` §8.1, `go-parts-ui-mockup.html`

---

## 1. Design Concept

Not the generic "near-black background, acid-green terminal accent" default — that reads as an AI-generated cliché, not a considered choice. go-parts draws instead from **bench-equipment aesthetics**: PCB solder-mask green, exposed copper traces, an oscilloscope/multimeter phosphor readout. It should feel like an instrument panel for electronics work, not a hacker-movie prop.

**Signature element:** thin copper hairlines connect structural sections (header → sidebar spine → category items) with small circular "via" dots at junctions — the layout itself reads as one connected circuit. This is the one place visual flourish is spent; everything else stays quiet and disciplined. This word made its way into the product itself, too — the scannable code printed on part/location labels is called a **Via** (PRD §5.17), the same idea in both places: a small connection point that jumps you straight to something.

**Beyond the browser:** the same token language extends to the TUI (`go-parts tui`, see PRD §5.11) via ANSI 256-color/truecolor terminal output where supported — copper accent, phosphor data color, dark background — so the command-line tool reads as the same product, not a disconnected one.

---

## 2. Color Tokens

| Token | Hex / Value | Usage |
|---|---|---|
| `--bg` | `#0b1411` | Page background (PCB solder mask) |
| `--bg-grid` | `#0d1713` | Faint background grid lines |
| `--surface` | `#0f1c17` | Panel / shell background |
| `--surface-alt` | `#142720` | Hover / active row background |
| `--border` | `#223229` | Default borders, dividers |
| `--border-soft` | `#1a2820` | Subtle row dividers |
| `--copper` | `#c98a4b` | Primary accent — buttons, focus, active state, trace lines |
| `--copper-bright` | `#e6a868` | Copper hover / active state |
| `--phosphor` | `#6fd9c9` | Data values — quantities, live/dynamic numbers |
| `--text` | `#e8ede8` | Primary text (silkscreen white) |
| `--text-dim` | `#82998d` | Secondary text |
| `--text-faint` | `#4d6459` | Tertiary text, labels, meta |
| `--ok` | `#7cc48f` | In-stock / success state |
| `--ok-bg` | `rgba(124,196,143,0.08)` | Success badge background |
| `--warn` | `#e2574c` | Low-stock / warning / error state |
| `--warn-bg` | `rgba(226,87,76,0.10)` | Warning badge background |

**Rule:** copper is reserved for primary actions and structural trace lines. Phosphor is reserved for live data values. Never swap their roles — the color itself should tell you whether you're looking at "a thing you can do" (copper) or "a fact about a part" (phosphor).

---

## 3. Typography

| Role | Face | Weight | Notes |
|---|---|---|---|
| Labels / eyebrows / nav | IBM Plex Mono | 600–700 | Uppercase, letter-spacing 0.10–0.14em, 10–11px |
| Data / body | JetBrains Mono | 400–500 | MPNs, specs, quantities, table content, 13px base |
| Panel title | IBM Plex Mono | 600 | 15px |

No serif or humanist sans anywhere. Monospace throughout is a deliberate signal: this is a technical instrument, not a lifestyle app. Every character occupies the same width — fitting for a tool where alignment of numeric columns matters.

---

## 4. Layout & Spacing

- Base spacing unit: 8px. Common values in use: 8, 10, 14, 18, 20px.
- Corners: 0–2px radius everywhere. No rounded cards.
- Borders: 1px hairlines only. No drop shadows — state changes communicate through border-color shift, not elevation.
- Shell: three-column layout — fixed sidebar (220px) / flexible main / fixed detail panel (300px), echoing a control-panel-plus-readout arrangement.
- **Responsive floor:** below ~900px the sidebar collapses into a horizontal scrollable chip row above the table; below ~700px the detail panel moves to a bottom sheet triggered by row selection rather than a persistent third column.

---

## 5. Iconography

Custom line-art SVGs styled as schematic symbols — resistor zigzag, capacitor plates, IC chip outline, connector plug, sensor circle — never a generic flat/rounded icon set. 14–22px, stroke-width 1.6–2, using `currentColor` so hover/active states recolor automatically without a second icon variant.

---

## 6. Components

### Buttons
- Default: 1px `--border`, text `--text-dim`, uppercase, 11px, letter-spacing 0.04em
- Primary: border-color `--copper`, text `--copper-bright`
- Hover: background `--surface-alt`
- Outline-only — no filled buttons. Consistent with an instrument-panel feel, not a consumer app.

### Badges
- OK: text `--ok` on `--ok-bg`
- LOW / WARN: text `--warn` on `--warn-bg`
- Sharp corners, 10px text, 2–7px padding

### Tables
- Header: IBM Plex Mono 10px uppercase, letter-spacing 0.1em, `--text-faint`
- Body: JetBrains Mono, `--text-dim`, 1px bottom border `--border-soft`
- Row hover: background `--surface-alt`
- Row selected: background `--surface-alt` + 2px left border `--copper`
- **Sortable columns:** every column header is clickable and keyboard-operable (`Enter`/`Space`). Inactive columns show a faint `↕` on hover; the active sort column shows a solid `▲`/`▼` in `--copper`. Clicking an already-active column reverses direction.
- Numeric columns (quantity) render in `--phosphor` to visually separate live data from descriptive text.

### Search
- Terminal-prompt style: icon + input + `/` kbd hint
- Border shifts to `--phosphor` on focus
- **Live filtering:** results update on every keystroke — no submit step, no debounce delay perceptible to the user. This is a tool, not a form.
- Empty state: an explicit `no matches for "{query}"` row inside the table itself, in the interface's own voice — never a blank table with no explanation.
- Result count updates alongside the query (`3,115 parts` → `12 matches for "10k"`) so the person always knows what they're looking at.

### Status / footer bar
- vim-style status line: mode indicator, record count, live connection dots for gateways (go-rag / muninndb) and vendor plugins, version tag
- The person should never have to wonder whether a dependent service is connected — that state is always visible, not tucked into a settings page.

### Top navigation
- Sits directly below the header, above the three-column body — six sections (Parts, Storage, Projects, Purchasing, Builds, Reports), mirroring the PartsBox-inspired information architecture
- IBM Plex Mono 11px, uppercase, letter-spacing 0.06em, `--text-faint` at rest
- Active section: `--copper-bright` text with a 2px `--copper` underline — the same active-state language used for table sort arrows and selected rows, so "this is the current thing" reads consistently everywhere in the app
- Hover (inactive): shift to `--text-dim` only — no underline until active, keeps the row calm

---

## 7. Motion

- Color and border transitions only, ~150ms ease. No page-load choreography, no easing tricks.
- The blinking cursor after the wordmark is the only ambient animation in the entire interface — restrained, not decorative overload.
- `prefers-reduced-motion` disables the cursor blink and all transitions.

---

## 8. Accessibility Floor

- Every interactive element has a visible focus state (border-color shift) — never an outline removed without a replacement.
- `--text` on `--bg` clears WCAG AA comfortably; `--text-dim` is reserved for secondary content only and is checked against both `--bg` and `--surface-alt`.
- Keyboard-first is a real requirement, not a decoration: search-focus shortcut, and every sortable header must be reachable and operable via keyboard, not mouse-only.

---

## 9. Voice

- Interface copy: plain, active voice, no exclamation marks.
- Errors and empty states are factual, not apologetic — `no matches for "x"`, not `oops, nothing found!`.
- Status text reads like a system log line: terse and factual, never marketing copy.
