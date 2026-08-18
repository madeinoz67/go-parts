package locations

import (
	"math"
	"reflect"
	"testing"
)

func TestGenerateLabels_Single(t *testing.T) {
	got, err := GenerateLabels("single", LabelParams{Label: "junk-box"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"junk-box"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("single = %v, want %v", got, want)
	}
}

func TestGenerateLabels_Row(t *testing.T) {
	got, err := GenerateLabels("row", LabelParams{Prefix: "box", From: 1, To: 5}, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"box-1", "box-2", "box-3", "box-4", "box-5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("row = %v, want %v", got, want)
	}
}

func TestGenerateLabels_Grid(t *testing.T) {
	got, err := GenerateLabels("grid", LabelParams{Prefix: "shelf", RowFrom: "A", RowTo: "B", ColFrom: 1, ColTo: 2}, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"shelf-A1", "shelf-A2", "shelf-B1", "shelf-B2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("grid = %v, want %v", got, want)
	}
}

func TestGenerateLabels_3DGrid(t *testing.T) {
	got, err := GenerateLabels("3d_grid", LabelParams{
		Prefix: "rack", LevelFrom: 1, LevelTo: 2, RowFrom: "A", RowTo: "B", ColFrom: 1, ColTo: 2,
	}, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"rack-1-A1", "rack-1-A2", "rack-1-B1", "rack-1-B2",
		"rack-2-A1", "rack-2-A2", "rack-2-B1", "rack-2-B2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("3d_grid = %v, want %v", got, want)
	}
}

// TestGenerateLabels_Separator pins Task 44a's single separator knob across
// every method: nil = the "-" default; "" = glued (the legacy row style);
// any other short non-whitespace literal joins prefix and each coordinate
// group verbatim. Grid/3d output under the default is unchanged from the
// pre-44a formats; row output changes (box1 → box-1) by the principal's
// 2026-08-18 directive ("custom, with default as '-'").
func TestGenerateLabels_Separator(t *testing.T) {
	dot := "."
	glue := ""
	for _, tc := range []struct {
		name   string
		method string
		p      LabelParams
		want   []string
	}{
		{"row default sep", "row", LabelParams{Prefix: "box", From: 1, To: 2}, []string{"box-1", "box-2"}},
		{"row dot sep", "row", LabelParams{Prefix: "box", From: 1, To: 2, Separator: &dot}, []string{"box.1", "box.2"}},
		{"row glue sep", "row", LabelParams{Prefix: "box", From: 1, To: 2, Separator: &glue}, []string{"box1", "box2"}},
		{"grid default sep", "grid", LabelParams{Prefix: "shelf", RowFrom: "A", RowTo: "A", ColFrom: 1, ColTo: 2}, []string{"shelf-A1", "shelf-A2"}},
		{"grid dot sep", "grid", LabelParams{Prefix: "shelf", RowFrom: "A", RowTo: "A", ColFrom: 1, ColTo: 2, Separator: &dot}, []string{"shelf.A1", "shelf.A2"}},
		{"grid glue sep", "grid", LabelParams{Prefix: "shelf", RowFrom: "A", RowTo: "A", ColFrom: 1, ColTo: 2, Separator: &glue}, []string{"shelfA1", "shelfA2"}},
		{"3d default sep", "3d_grid", LabelParams{Prefix: "rack", LevelFrom: 1, LevelTo: 1, RowFrom: "A", RowTo: "A", ColFrom: 1, ColTo: 1}, []string{"rack-1-A1"}},
		{"3d dot sep", "3d_grid", LabelParams{Prefix: "rack", LevelFrom: 1, LevelTo: 1, RowFrom: "A", RowTo: "A", ColFrom: 1, ColTo: 1, Separator: &dot}, []string{"rack.1.A1"}},
		{"3d glue sep", "3d_grid", LabelParams{Prefix: "rack", LevelFrom: 1, LevelTo: 1, RowFrom: "A", RowTo: "A", ColFrom: 1, ColTo: 1, Separator: &glue}, []string{"rack1A1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GenerateLabels(tc.method, tc.p, 100)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%s = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
	// Invalid separators are rejected regardless of method: too long, or
	// whitespace inside (a label that cannot be re-typed cannot be searched).
	long := "toolong"
	spacey := "a b"
	for _, bad := range []*string{&long, &spacey} {
		if _, err := GenerateLabels("row", LabelParams{Prefix: "box", From: 1, To: 2, Separator: bad}, 100); err == nil {
			t.Fatalf("expected error for separator %q, got nil", *bad)
		}
	}
	// single ignores the separator — it labels nothing to join.
	hyphen := "-"
	got, err := GenerateLabels("single", LabelParams{Label: "junk-box", Separator: &hyphen}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"junk-box"}) {
		t.Fatalf("single with separator = %v, want [junk-box]", got)
	}
}

func TestGenerateLabels_Errors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		p      LabelParams
	}{
		{"single missing label", "single", LabelParams{}},
		{"row reversed range", "row", LabelParams{Prefix: "box", From: 5, To: 1}},
		{"grid bad row letter", "grid", LabelParams{Prefix: "shelf", RowFrom: "AB", RowTo: "B", ColFrom: 1, ColTo: 2}},
		{"grid reversed rows", "grid", LabelParams{Prefix: "shelf", RowFrom: "B", RowTo: "A", ColFrom: 1, ColTo: 2}},
		{"grid reversed cols", "grid", LabelParams{Prefix: "shelf", RowFrom: "A", RowTo: "B", ColFrom: 2, ColTo: 1}},
		{"3d reversed levels", "3d_grid", LabelParams{Prefix: "rack", LevelFrom: 2, LevelTo: 1, RowFrom: "A", RowTo: "B", ColFrom: 1, ColTo: 2}},
		{"unknown method", "banana", LabelParams{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := GenerateLabels(tc.method, tc.p, 100); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestGenerateLabels_RowSingleValue(t *testing.T) {
	// from == to yields exactly one label
	got, err := GenerateLabels("row", LabelParams{Prefix: "x", From: 3, To: 3}, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"x-3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("row single = %v, want %v", got, want)
	}
}

// TestGenerateLabels_OverLimit — the cap fires BEFORE any allocation, returning
// a clean error instead of a makeslice panic on pathological inputs.
func TestGenerateLabels_OverLimit(t *testing.T) {
	// row over the cap: 200 labels > 100 limit.
	_, err := GenerateLabels("row", LabelParams{Prefix: "x", From: 1, To: 200}, 100)
	if err == nil {
		t.Fatal("expected error for row 200 > limit 100, got nil")
	}
	// pathological: --to MaxInt64 (makeslice-cap overflow without the guard).
	_, err = GenerateLabels("row", LabelParams{Prefix: "x", From: 0, To: math.MaxInt64}, 100)
	if err == nil {
		t.Fatal("expected error for row To=MaxInt64, got nil")
	}
	// grid over the cap: 26 rows * 1000 cols = 26000 > 100.
	_, err = GenerateLabels("grid", LabelParams{Prefix: "x", RowFrom: "A", RowTo: "Z", ColFrom: 1, ColTo: 1000}, 100)
	if err == nil {
		t.Fatal("expected error for grid 26*1000=26000 > limit 100, got nil")
	}
	// maxLabels boundary: exactly at the limit is allowed.
	got, err := GenerateLabels("row", LabelParams{Prefix: "x", From: 1, To: 100}, 100)
	if err != nil {
		t.Fatalf("expected exactly-100 to pass, got %v", err)
	}
	if len(got) != 100 {
		t.Fatalf("got %d labels, want 100", len(got))
	}
	// maxLabels <= 0 rejected.
	_, err = GenerateLabels("row", LabelParams{Prefix: "x", From: 1, To: 5}, 0)
	if err == nil {
		t.Fatal("expected error for maxLabels=0, got nil")
	}
}
