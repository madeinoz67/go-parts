package tui

// adjust.go — the stock-adjust overlay (spec §1/§4). Task 8 fills this in;
// the stub exists so Task 7's `a` handling compiles. The adjustModel TYPE
// itself is declared in model.go (landed with Task 6's field set); only the
// constructor stub lives here until Task 8 replaces it with the real form.

func newAdjustModel(_ PartDetail) *adjustModel { return &adjustModel{} }
