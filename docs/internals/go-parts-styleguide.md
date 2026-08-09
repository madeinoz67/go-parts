# go-parts Style Guide — Hacker / Hi-Tech

**Author:** Stephen Eaton
**Status:** v2.1 (generalized the autocomplete dropdown to cover manufacturer and footprint, not just tags)
**Companion to:** `go-parts-prd.md` §8.1, `go-parts-ui-mockup.html`

---

## 1. Design Concept

Not the generic "near-black background, acid-green terminal accent" default — that reads as an AI-generated cliché, not a considered choice. go-parts draws instead from **bench-equipment aesthetics**: PCB solder-mask green, exposed copper traces, an oscilloscope/multimeter phosphor readout. It should feel like an instrument panel for electronics work, not a hacker-movie prop.

**Signature element:** thin copper hairlines connect structural sections (header → sidebar spine → tag items) with small circular "via" dots at junctions — the layout itself reads as one connected circuit. This is the one place visual flourish is spent; everything else stays quiet and disciplined. This word made its way into the product itself, too — the scannable code printed on part/location labels is called a **Via** (PRD §5.17), the same idea in both places: a small connection point that jumps you straight to something.

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

**Fallback for tags with no hand-drawn icon.** Six common tag values (`resistor`, `capacitor`, `ic`, `connector`, `sensor`, `hardware`) have bespoke art; anything else (a tag nobody anticipated — `optocoupler`, `crystal`, whatever) needs to render *something*, not a broken icon slot or a mismatched generic-flat icon that breaks the "never generic/rounded" rule. The fallback is a dashed-outline rectangle, `--text-faint` stroke rather than full icon color — the same convention KiCad and other CAD tools already use for an unassigned/undefined footprint placeholder, so it reads as "no specific symbol designed yet" rather than as a mistake. Every tag not in the hand-drawn six gets the same fallback; they're differentiated by their text label, not a bespoke icon. Adding real art for a new tag later is a deliberate choice when one earns it through actual use, not something solved upfront for every conceivable tag.

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
- Row selected: background `--surface-alt` + 2px left border `--copper` — this is the *viewing* state (row clicked, shown in the detail panel), a different thing from bulk selection below and never triggered by the same gesture
- **Sortable columns:** every column header is clickable and keyboard-operable (`Enter`/`Space`). Inactive columns show a faint `↕` on hover; the active sort column shows a solid `▲`/`▼` in `--copper`. Clicking an already-active column reverses direction.
- Numeric columns (quantity) render in `--phosphor` to visually separate live data from descriptive text.
- **Bulk-select checkbox column** (PRD §7.2) — leftmost column, deliberately separate from row-click-for-detail-panel above. Unchecked: 1px `--border` box. Checked: `--copper` fill, `--bg` checkmark. Header checkbox selects everything currently visible, same "scoped to what's filtered" rule as the rest of the table.
- **Bulk-action bar** replaces the toolbar's chip row the moment anything is checked — `N selected` in `--text`, then outline actions (`Tag`, `Move`, `Delete` in `--warn`-bordered, `Clear`) in the same button language as everywhere else. Disappears back to the normal chip row the instant the selection is cleared or the last box is unchecked.

### Search
- Terminal-prompt style: icon + input + `/` kbd hint
- Border shifts to `--phosphor` on focus
- **Live filtering:** results update on every keystroke — no submit step, no debounce delay perceptible to the user. This is a tool, not a form.
- Empty state: an explicit `no matches for "{query}"` row inside the table itself, in the interface's own voice — never a blank table with no explanation.
- Result count updates alongside the query (`3,115 parts` → `12 matches for "10k"`) so the person always knows what they're looking at.

### Form inputs (create/edit/stock)
- Background: `--surface-alt` (`#142720`) — dark, distinct from the page `--bg`
- Text: `--text` (`#e8ede8`) — silkscreen white, high contrast on the dark field
- Border: 1px `--border` (`#223229`) at rest; shifts to `--copper-bright` (`#e6a868`) on focus (no outline — the border-color shift is the focus indicator, consistent with buttons/table rows)
- Placeholder text: `--text-faint` (`#4d6459`)
- Font: JetBrains Mono 13px — these are technical values (MPNs, specs, quantities), not prose
- Corners: 0 — sharp, consistent with the rest of the interface
- **The search input has its own `.search-input` rules (phosphor focus border, transparent background) — it is deliberately distinct from form inputs because it is a filter, not a data-entry field. Never apply form-input background to the search input.**

### Status / footer bar
- vim-style status line: mode indicator, record count, live connection dots for gateways (go-rag / muninndb) and vendor plugins, version tag
- The person should never have to wonder whether a dependent service is connected — that state is always visible, not tucked into a settings page.

### Top navigation
- Sits directly below the header, above the three-column body — six sections (Parts, Storage, Projects, Purchasing, Builds, Reports), mirroring the PartsBox-inspired information architecture
- IBM Plex Mono 11px, uppercase, letter-spacing 0.06em, `--text-faint` at rest
- Active section: `--copper-bright` text with a 2px `--copper` underline — the same active-state language used for table sort arrows and selected rows, so "this is the current thing" reads consistently everywhere in the app
- Hover (inactive): shift to `--text-dim` only — no underline until active, keeps the row calm

### Confirmation panel
- Not a native browser `confirm()` — a styled inline panel, same tokens as everything else, no drop shadow (border + `--surface-alt` background instead)
- States plainly what's about to happen, specific to the action — never a generic "are you sure?"
- Two buttons: outline default for cancel, `--warn`-bordered for the destructive confirm — same outline-only rule as every other button, just recolored to signal weight

### Operation failure banner
- One component, several triggers — a stale-version conflict (PRD §5.14), a vendor lookup failure (§5.4), an AI enrichment error (§5.9) — same visual treatment every time: full-width, `--warn-bg` background, `--warn` left border
- States what happened in one line, worded from §5.4's own retry classification rather than a generic error — *"rate limited — will retry automatically"* reads differently from *"authentication failed — check the API key"* because they call for different next steps
- If it's sitting above a form, the form underneath keeps every field exactly as typed — never cleared, never silently merged
- Only for the mid-action case (something failed while you were waiting on it). A background failure doesn't get a banner — see the failed badge below, which covers the "nobody was watching" case

### Failed badge
- Small `--warn` badge on a part row/detail, same visual language as the existing low-stock badge — reflects `enrichment_status = failed` (PRD §5.10)
- The dashboard's `enrichment_failed` count (§5.19) is the discovery path — tapping it filters straight to failed parts, reusing the same tag-filter mechanism as everything else, not a separate failure inbox
- A persistent, ongoing outage (not a one-off) is a different case again — see the footer status bar's connection dots, which stay red for as long as it's actually true rather than a banner that would've scrolled away

### Inline validation
- Errors sit directly under the specific field, `--warn` text, 11px — never a summary block stacked at the top of the form
- The field's border shifts to `--warn` too, so the eye catches it before reading the message

### Status line (loading states)
- Long-running operations (reindex, bulk import, vendor sync) render as one text line, not a spinner — `reindexing… 340/3,115`, `--phosphor` for the live count, polling the same job status already shown elsewhere (PRD §5.10)
- No skeleton screens, no shimmer placeholders — consistent with the "no decorative feedback loops" motion principle below

### Quantity stepper
- `−` button · delta input (signed, defaults to `1`) · `+` button — never a bare editable number (PRD §5.14/§5.22, a correctness requirement, not a style choice)
- Confirm applies the delta and the on-hand count updates in place — no separate "save" step once the delta is set
- `−` in `--warn` on hover if the delta would take stock below zero, otherwise both buttons stay outline-default

### Spec / custom-field row
- One row per key-value pair: key input, value input, small outline `+` to add another row, `×` to remove
- Key input autocompletes from keys already used elsewhere in the catalog (PRD §6.2/§5.22) — a live dynamic list, not a fixed dropdown
- New rows append below the last; nothing about the layout implies a maximum or a required set

### Field autocomplete dropdown
- Same component behind tag entry (§5.7), manufacturer, and footprint (§5.22) — one dropdown, not three near-copies
- Appears below the input while typing, `--surface-alt` background, same border/panel treatment as everything else — no separate "dropdown" styling
- Existing matches: `--text-dim`, count in `--text-faint` where relevant (`resistor (1,204)`) — a fact being offered, not an action
- "Add new: 'xyz'" row at the bottom: `--copper`, not `--text-dim` — the same color rule from §2 (copper = a thing you can do) applied here, not a new rule invented for this one component
- Stays open while typing rather than requiring a deliberate trigger — the existing/new choice needs to be visible before it's committed, not hidden
- Tags: multi-value, feeds the add-a-row pattern. Manufacturer/footprint: single-value, one input, no row list — same dropdown, different container around it

### Breadcrumb
- Appears above a Location's label whenever it has a parent (PRD §5.21) — omitted entirely for top-level locations, no single-segment clutter for the common flat case
- Ancestor segments in `--text-faint`, IBM Plex Mono 11px, each a link that navigates up; current location in full `--text`; separated by `›`
- Same treatment on desktop and the Via mobile landing page (below) — one component, not a mobile-specific variant

### Via mobile landing page
- Single column, not the three-column desktop shell — a scanned Via (PRD §5.24) loads a minimal task-focused view, not the full app
- Minimal top bar: wordmark + a "full app" link only, no six-item top nav
- Location: breadcrumb above the label if nested, then tappable list of contents (MPN, description, `--phosphor` quantity) — tap a row to open its quantity stepper directly
- Part: detail view with the quantity stepper front and center, same component as desktop, larger tap target underneath
- Stepper `+`/`−` touch targets are sized for a thumb, not a mouse — same visual treatment as the desktop stepper, bigger hit area

### Printed labels (PRD §5.17) — deliberately not this style guide's palette
- Black on white (or transparent over label stock) — no `--bg`, no `--copper`, no glow states. Most label printers are monochrome thermal anyway, and color ink on a small adhesive label is wasted ink for no scannability benefit.
- QR code dominant; human-readable Via code (`L-7B3D1E`) printed beneath it in monospace, plus the location/part's plain label — the QR isn't the only thing on the label, the text matters too since the whole point of the Via format was staying typeable if a scanner fails
- No via-dot motif, no decorative trace lines — those are screen-only flourishes, not label content
- SVG output — same format already used for label/sticker export elsewhere in this toolset, works across thermal printers, cutters, and laser engraving alike

### First-run panel
- Replaces the whole view (table, dashboard grid, etc.), not a banner stacked above it — this is the only content on screen until there's real data
- Same tokens as everything else — no separate "welcome" styling, no illustration, no onboarding-product visual language
- One line stating what's empty, one or two outline buttons for the actual next actions (PRD §5.25) — copper-bordered like any primary action, not a special CTA treatment
- The "not built yet" variant (Purchasing/Builds/Reports pre-Phase 6) drops the action buttons entirely — `--text-faint` text only, since there's nothing to do yet and a button that goes nowhere is worse than no button

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
