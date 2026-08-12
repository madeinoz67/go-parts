# Task 9 Report — web UI inline stock-adjust handler

**Status:** DONE
**Branch:** `develop` (per CLAUDE.md — develop is the integration trunk; main stays pristine)

## What was implemented

The last web UI handler: `POST /ui/parts/{id}/stock` → `store.AdjustStock(id, delta, "ui")` → re-rendered `detail.html` fragment with the post-delta `*Part`. 404 on `parts.ErrNotFound`. The inline stock form is wired into `detail.html` next to the qty display.

### Files changed

- `internal/ui/handlers.go` — new `handleStock` (+38 lines)
- `internal/ui/server.go` — registered `POST /ui/parts/{id}/stock` route (1 line, gofmt-aligned adjacent comments)
- `internal/ui/templates/detail.html` — inline stock form (`<form class="inline stock">`, hx-post, hx-target, `−1` quick button + free-form number input + apply)
- `internal/ui/handlers_test.go` — two tests (+31 lines)

### Design

**`handleStock` flow:**
1. `r.PathValue("id")` → parse form → `fmt.Sscanf(r.PostFormValue("delta"), "%d", &delta)`. Non-numeric deltas parse as 0 (no-op adjust) — `Sscanf` returns the count of items scanned, which we deliberately ignore; a malformed delta is treated as a zero delta rather than a 400. Matches the brief's snippet verbatim.
2. `s.store.AdjustStock(id, delta, "ui")` — IN-PROCESS (PRD §5.2 — web UI is a 5th surface over the core, never over REST). `parts.ErrNotFound` → `http.NotFound`; any other storage error → 500.
3. `s.store.Get(id)` → re-render `detail.html` with the post-delta `*Part`. The second `Get` is required because `AdjustStock` returns only an error, not the updated record. The brief's snippet treats a missing record on this re-Get as 404 — kept verbatim (the canonical single-part not-found recovery).
4. The round-tripped `*Part` carries the unchanged `Version` (AdjustStock does not bump Version per §5.14 — stock is authoritative), so the inline edit form's hidden `version` field stays consistent for the next submit.

**Route registration:** `POST /ui/parts/{id}/stock` is registered AFTER `POST /ui/parts/{id}`. Go's `ServeMux` matches by longest literal prefix and `{id}` matches one segment, so the longer `/stock` suffix wins for stock posts and `{id}` catches everything else. No conflict with the bare `POST /ui/parts` create route.

**Inline form:** quick `−1` decrement button + free-form number input (default 0) + apply button, all posting `delta` to the stock endpoint and swapping `#detail-panel`. Two fields named `delta` is deliberate — the browser submits only the activated one (clicked button), so `−1` quick-decrement and free-form apply are independent paths over the same form.

## TDD evidence

### RED (route not registered)

```
--- FAIL: TestStockAdjustUpdatesQty (0.08s)
    handlers_test.go:205: stock = 405, want 200; body=Method Not Allowed
--- FAIL: TestStockAdjustUnknownIs404 (0.08s)
    handlers_test.go:223: stock on unknown part = 405, want 404; body=Method Non-Allowed
FAIL
FAIL	github.com/madeinoz67/go-parts/internal/ui	0.849s
```

The 405 (not 404 or 200) is the right failure shape — ServeMux returns 405 when a path matches but the method does not (only `GET /ui/parts/{id}` was registered; POST fell through).

### GREEN

```
$ go test -race -run TestStockAdjust ./internal/ui/
ok  	github.com/madeinoz67/go-parts/internal/ui	1.881s
```

### Full gate

```
$ go build ./... && go vet ./... && gofmt -l . && go test -race ./internal/ui/ -count=2
===BUILD/VET/FMT CLEAN===
ok  	github.com/madeinoz67/go-parts/internal/ui	3.262s
```

`go test -race ./...` — every package green (parts / index / storage / keys / migrate / rest / daemon / e2e / ui).

## Self-review

| # | Invariant | Verdict |
|---|---|---|
| 1 | AdjustStock called IN-PROCESS (not REST) | OK — `s.store.AdjustStock(id, delta, "ui")` direct |
| 2 | `parts.ErrNotFound` → 404 | OK — `errors.Is` check on AdjustStock error path; second Get error also falls to `http.NotFound` |
| 3 | Returns updated detail.html fragment | OK — re-renders `detail.html` with the post-delta `*Part`; test asserts body contains `95` |
| 4 | Inline stock form present in detail.html | OK — `<form class="inline stock">` with `hx-post`/`hx-target` next to the qty display |
| 5 | AdjustStock does not bump Version | OK (inherited) — Store.AdjustStock contract; the detail fragment's hidden `version` field is consistent post-adjust |
| 6 | Route does not collide with edit | OK — longest-prefix match: `/stock` suffix wins for stock posts; `{id}` catches the rest |
| 7 | Non-numeric delta does not 500 | OK — `Sscanf` count ignored; a malformed delta becomes a 0-delta no-op adjust (matches brief) |
| 8 | Concurrency-safe | OK (inherited) — AdjustStock is serialized per-id under `lockFor(id)` (§5.14); two concurrent stock adjusts on the same part always net their sum |

## Divergences from brief

1. **Added `TestStockAdjustUnknownIs404`.** Brief listed only `TestStockAdjustUpdatesQty`. The 404 path is part of the contract (and the brief's step-3 snippet implements it explicitly), so a regression test for it is the minimum bar — same pattern as `TestDetailUnknownIs404` for the detail handler.
2. **Detail re-Get falls to `http.NotFound` on the second miss.** The brief's step-3 snippet inlines `p, err := s.store.Get(id); if err != nil { http.NotFound(w, r); return }`. Kept verbatim, but added a comment explaining why (AdjustStock succeeded but the record is now unreadable — treat as not-found rather than 500; canonical single-part not-found recovery).
3. **Unrelated `internal/daemon/daemon.go` and `task-8-report.md` left unstaged.** `git status` showed a daemon-wiring change from a prior session (composes UI + REST + redirect) and a stale report file. Neither is part of Task 9; per the brief's "Do NOT commit scratch" + `git add internal/ui/`, only the four UI files were staged.

## Concerns

None.
