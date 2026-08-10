package locations

import (
	"fmt"
	"strconv"
)

// LabelParams carries the inputs to GenerateLabels. Which fields are required
// depends on the method: single uses Label; row uses Prefix+From+To; grid uses
// Prefix+RowFrom/RowTo+ColFrom/ColTo; 3d_grid adds LevelFrom/LevelTo.
type LabelParams struct {
	Label string // single

	Prefix string // row | grid | 3d_grid
	From   int    // row (numeric range start, inclusive)
	To     int    // row (numeric range end, inclusive)

	RowFrom string // grid | 3d_grid (single uppercase letter A-Z)
	RowTo   string // grid | 3d_grid
	ColFrom int    // grid | 3d_grid (numeric, inclusive)
	ColTo   int    // grid | 3d_grid

	LevelFrom int // 3d_grid only (numeric, inclusive)
	LevelTo   int // 3d_grid only
}

// GenerateLabels returns the labels a creation method produces (PRD §7.1). It
// is PURE: no I/O, no Store. The label formats are load-bearing (pinned by the
// four PRD examples):
//   - single:  the Label as-is.
//   - row:     Prefix + strconv.Itoa(n) — NO separator ("box1").
//   - grid:    Prefix + "-" + row + strconv.Itoa(col) ("shelf-A1").
//   - 3d_grid: Prefix + "-" + level + "-" + row + strconv.Itoa(col) ("rack-1-A1").
//
// Rows are single uppercase letters A-Z. An invalid range, a multi-character
// row letter, or an unknown method returns an error.
//
// maxLabels caps the label count as an operator-controlled sanity guard. If
// maxLabels <= 0 the call errors immediately; if the total label count the
// method WOULD generate exceeds maxLabels, the call errors BEFORE allocating any
// slice — so a pathological input (e.g. --to MaxInt64) returns a clean error,
// never a makeslice panic. The total is computed overflow-safe: each factor is
// checked against maxLabels before multiplying (labelTotal), and the row
// additive total guards the +1.
func GenerateLabels(method string, p LabelParams, maxLabels int) ([]string, error) {
	if maxLabels <= 0 {
		return nil, fmt.Errorf("locations: maxLabels must be positive, got %d", maxLabels)
	}
	switch method {
	case "single":
		if p.Label == "" {
			return nil, fmt.Errorf("locations: single requires Label")
		}
		return []string{p.Label}, nil
	case "row":
		if p.From < 0 || p.To < 0 || p.To < p.From {
			return nil, fmt.Errorf("locations: row range %d-%d invalid", p.From, p.To)
		}
		// Guard the +1: check span against maxLabels first. If span >= maxLabels,
		// total = span+1 exceeds maxLabels — but span+1 could overflow int when
		// span is near MaxInt64, so compute it only after confirming span < MaxInt.
		span := p.To - p.From // safe on 64-bit: both non-negative, From <= To
		if span >= maxLabels {
			total := span + 1
			if total <= 0 { // overflow (span ≈ MaxInt64)
				total = span // undercount, but clearly exceeds maxLabels
			}
			return nil, fmt.Errorf("locations: row bulk would generate %d labels, exceeds limit %d", total, maxLabels)
		}
		var out []string
		for n := p.From; n <= p.To; n++ {
			out = append(out, p.Prefix+strconv.Itoa(n))
		}
		return out, nil
	case "grid":
		rows, err := letterRange(p.RowFrom, p.RowTo)
		if err != nil {
			return nil, fmt.Errorf("locations: grid rows: %w", err)
		}
		if p.ColTo < p.ColFrom {
			return nil, fmt.Errorf("locations: grid cols %d-%d invalid", p.ColFrom, p.ColTo)
		}
		cols := p.ColTo - p.ColFrom + 1
		total, exceeded := labelTotal(maxLabels, len(rows), cols)
		if exceeded {
			return nil, fmt.Errorf("locations: grid bulk would generate %d labels, exceeds limit %d", total, maxLabels)
		}
		var out []string
		for _, r := range rows {
			for c := p.ColFrom; c <= p.ColTo; c++ {
				out = append(out, p.Prefix+"-"+r+strconv.Itoa(c))
			}
		}
		return out, nil
	case "3d_grid":
		rows, err := letterRange(p.RowFrom, p.RowTo)
		if err != nil {
			return nil, fmt.Errorf("locations: 3d_grid rows: %w", err)
		}
		if p.ColTo < p.ColFrom {
			return nil, fmt.Errorf("locations: 3d_grid cols %d-%d invalid", p.ColFrom, p.ColTo)
		}
		if p.LevelTo < p.LevelFrom {
			return nil, fmt.Errorf("locations: 3d_grid levels %d-%d invalid", p.LevelFrom, p.LevelTo)
		}
		levels := p.LevelTo - p.LevelFrom + 1
		cols := p.ColTo - p.ColFrom + 1
		total, exceeded := labelTotal(maxLabels, levels, len(rows), cols)
		if exceeded {
			return nil, fmt.Errorf("locations: 3d_grid bulk would generate %d labels, exceeds limit %d", total, maxLabels)
		}
		var out []string
		for lvl := p.LevelFrom; lvl <= p.LevelTo; lvl++ {
			for _, r := range rows {
				for c := p.ColFrom; c <= p.ColTo; c++ {
					out = append(out, p.Prefix+"-"+strconv.Itoa(lvl)+"-"+r+strconv.Itoa(c))
				}
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("locations: unknown creation method %q", method)
	}
}

// labelTotal computes the product of non-negative factors, returning the total
// and whether it exceeds maxLabels. It guards against int overflow by
// short-circuiting the moment a factor or the running product crosses the cap:
// the multiply only runs on values each <= maxLabels, whose product fits
// comfortably in int for any sane limit. A negative factor (overflow from the
// caller's +1 on a near-MaxInt range) is treated as exceeded.
func labelTotal(maxLabels int, factors ...int) (total int, exceeded bool) {
	total = 1
	for _, f := range factors {
		if f < 0 || f > maxLabels {
			return maxLabels + 1, true
		}
		total *= f
		if total > maxLabels || total <= 0 {
			return total, true
		}
	}
	return total, false
}

// letterRange expands a single-letter range "A".."B" into ["A","B"]. Each bound
// must be exactly one uppercase A-Z letter; from <= to.
func letterRange(from, to string) ([]string, error) {
	if len(from) != 1 || len(to) != 1 {
		return nil, fmt.Errorf("row letters must be single chars, got %q..%q", from, to)
	}
	lo, hi := from[0], to[0]
	if lo < 'A' || lo > 'Z' || hi < 'A' || hi > 'Z' {
		return nil, fmt.Errorf("row letters must be A-Z, got %q..%q", from, to)
	}
	if hi < lo {
		return nil, fmt.Errorf("row range reversed: %q..%q", from, to)
	}
	out := make([]string, 0, hi-lo+1)
	for c := lo; c <= hi; c++ {
		out = append(out, string(c))
	}
	return out, nil
}
