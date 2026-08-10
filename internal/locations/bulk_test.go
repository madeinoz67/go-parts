package locations

import (
	"errors"
	"testing"
)

func TestCreateBulk_RowCreatesEach(t *testing.T) {
	s, _ := newStore(t)
	labels := []string{"box1", "box2", "box3"}
	created, err := s.CreateBulk(labels, BulkOpts{CreationMethod: "row"})
	if err != nil {
		t.Fatalf("CreateBulk: %v", err)
	}
	if len(created) != 3 {
		t.Fatalf("created %d, want 3", len(created))
	}
	for i, l := range created {
		if l.Label != labels[i] {
			t.Fatalf("row %d label = %q, want %q", i, l.Label, labels[i])
		}
		if l.ID == "" || l.ViaCode == "" {
			t.Fatalf("row %d missing ID/ViaCode", i)
		}
		if l.CreationMethod != "row" {
			t.Fatalf("CreationMethod = %q, want row", l.CreationMethod)
		}
	}
	// Each is an ordinary, Get-able Location.
	if s.Count() != 3 {
		t.Fatalf("Count = %d, want 3", s.Count())
	}
}

func TestCreateBulk_SharedOpts(t *testing.T) {
	s, _ := newStore(t)
	parent := &Location{Label: "Parent"}
	if err := s.Create(parent); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateBulk([]string{"c1", "c2"}, BulkOpts{
		ParentID:       parent.ID,
		SinglePartOnly: true,
		Notes:          "batch",
		CreationMethod: "grid",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range created {
		if l.ParentID != parent.ID || !l.SinglePartOnly || l.Notes != "batch" || l.CreationMethod != "grid" {
			t.Fatalf("bulk row not carrying shared opts: %+v", l)
		}
	}
	// Children reflect the shared parent.
	if len(s.Children(parent.ID)) != 2 {
		t.Fatalf("Children(parent) = %d, want 2", len(s.Children(parent.ID)))
	}
}

func TestCreateBulk_PartialOnFailure(t *testing.T) {
	s, _ := newStore(t)
	// A non-existent parent makes EVERY Create fail at the parent-existence
	// check (Slice 1's Create validates ParentID). So nothing is created and
	// the error surfaces — verify the partial result is empty + error is non-nil.
	created, err := s.CreateBulk([]string{"a", "b"}, BulkOpts{ParentID: "ghost", CreationMethod: "single"})
	if err == nil {
		t.Fatal("expected error for ghost parent, got nil")
	}
	if len(created) != 0 {
		t.Fatalf("partial = %d, want 0 (parent check fails before any create)", len(created))
	}
}

// TestCreateBulk_TruePartialMidBulk is a DEFERRED test (Fix F). The contract it
// would pin: CreateBulk returns [N created], error when the N+1th Create fails
// mid-bulk (the "report-not-rollback" contract, N > 0). The code-correctness is
// clear by inspection — store.go's CreateBulk does `return out, err` on the
// first Create failure, where `out` holds the successfully-created rows so far.
//
// A clean, deterministic injection is not achievable without refactoring:
// Create has no label-dependent failure mode (labels don't collide; via codes
// are random; ParentID/opts are shared across all rows in a bulk). The only way
// to make the N+1th Create fail while the first N succeed is to mutate external
// state mid-loop (close the DB, delete the parent concurrently), which is a
// racy, fragile test. An interface-based mock would require changing Store.db
// from *pebble.DB to an interface — a larger refactor than this fix-wave's
// scope. Left as a follow-up; the empty-partial case (above) covers the
// all-fail path, and the code path is straight-line inspection.

// TestCreateBulk_BulkRowsAreIndependent — a row created by bulk is editable +
// removable like any single-created location (creation_method is metadata only).
func TestCreateBulk_RowsAreIndependent(t *testing.T) {
	s, _ := newStore(t)
	created, _ := s.CreateBulk([]string{"a", "b"}, BulkOpts{CreationMethod: "row"})
	// Rename one (it's an ordinary Update).
	created[0].Label = "renamed"
	if err := s.Update(created[0], created[0].Version); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(created[0].ID)
	if got.Label != "renamed" {
		t.Fatalf("bulk row not independently editable: label=%q", got.Label)
	}
	// Remove one (leaf, no children) — succeeds.
	if err := s.Delete(created[1].ID); err != nil {
		t.Fatalf("delete bulk row: %v", err)
	}
	if _, err := s.Get(created[1].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("post-delete err = %v, want ErrNotFound", err)
	}
}
