package locations

import (
	"errors"
	"testing"
)

func mk(t *testing.T, s *Store, label, parentID string) *Location {
	t.Helper()
	l := &Location{Label: label, ParentID: parentID}
	if err := s.Create(l); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestChildren(t *testing.T) {
	s, _ := newStore(t)
	a := mk(t, s, "A", "")
	b := mk(t, s, "B", a.ID)
	mk(t, s, "C", a.ID)
	mk(t, s, "D", b.ID)
	ch := s.Children(a.ID)
	if len(ch) != 2 {
		t.Fatalf("Children(A) = %d, want 2", len(ch))
	}
	if len(s.Children(b.ID)) != 1 {
		t.Fatalf("Children(B) = %d, want 1", len(s.Children(b.ID)))
	}
	if len(s.Children("nope")) != 0 {
		t.Fatalf("Children(missing) = %d, want 0", len(s.Children("nope")))
	}
}

// TestUpdate_CycleGuard_Self — setting ParentID to the location's own ID is a
// cycle. RED without the guard (the self-parent writes), GREEN with (ErrCycle).
func TestUpdate_CycleGuard_Self(t *testing.T) {
	s, _ := newStore(t)
	a := mk(t, s, "A", "")
	a.ParentID = a.ID
	err := s.Update(a, a.Version)
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("self-parent err = %v, want ErrCycle", err)
	}
}

// TestUpdate_CycleGuard_Descendant — reparenting A under C, where C is A's
// descendant (A→B→C), would form A→C→B→A. Must be rejected.
func TestUpdate_CycleGuard_Descendant(t *testing.T) {
	s, _ := newStore(t)
	a := mk(t, s, "A", "")
	b := mk(t, s, "B", a.ID)
	c := mk(t, s, "C", b.ID)
	a.ParentID = c.ID // c is a descendant of a → cycle
	err := s.Update(a, a.Version)
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("descendant-parent err = %v, want ErrCycle", err)
	}
}

// TestUpdate_AllowsValidReparent — reparenting C under A (no cycle) succeeds.
func TestUpdate_AllowsValidReparent(t *testing.T) {
	s, _ := newStore(t)
	a := mk(t, s, "A", "")
	b := mk(t, s, "B", "")
	c := mk(t, s, "C", b.ID)
	c.ParentID = a.ID // valid: a is not a descendant of c
	if err := s.Update(c, c.Version); err != nil {
		t.Fatalf("valid reparent err = %v", err)
	}
}

// TestDelete_RefusesWithChildren — Delete on a location with children errors;
// the children are NOT removed.
func TestDelete_RefusesWithChildren(t *testing.T) {
	s, _ := newStore(t)
	a := mk(t, s, "A", "")
	mk(t, s, "B", a.ID)
	err := s.Delete(a.ID)
	if !errors.Is(err, ErrHasChildren) {
		t.Fatalf("delete-with-children err = %v, want ErrHasChildren", err)
	}
	if _, err := s.Get(a.ID); err != nil {
		t.Fatalf("A was deleted despite children: %v", err)
	}
}

// TestDelete_AllowsLeaf — a leaf (no children) deletes fine.
func TestDelete_AllowsLeaf(t *testing.T) {
	s, _ := newStore(t)
	a := mk(t, s, "A", "")
	if err := s.Delete(a.ID); err != nil {
		t.Fatalf("leaf delete err = %v", err)
	}
}

func TestByVia(t *testing.T) {
	s, _ := newStore(t)
	l := mk(t, s, "x", "")
	got, err := s.ByVia(l.ViaCode)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != l.ID {
		t.Fatalf("ByVia = %s, want %s", got.ID, l.ID)
	}
	if _, err := s.ByVia("L-NOPE00"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ByVia miss err = %v, want ErrNotFound", err)
	}
}
