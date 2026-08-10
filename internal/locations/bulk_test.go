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
