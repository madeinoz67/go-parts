package components

import (
	"errors"
	"testing"

	"github.com/madeinoz67/go-parts/internal/parts"
)

// TestAddMissingPartRejectsWithoutOrphan pins code-review finding 2 (Add half):
// a Component for a partID that does not exist must be refused BEFORE any
// write. The old order (write, then recompute → parts.SetQty → parts.Get miss)
// left the Component persisted — an orphan whose only retry outcome was
// ErrDuplicate.
func TestAddMissingPartRejectsWithoutOrphan(t *testing.T) {
	cs, _ := newStore(t)
	err := cs.Add("loc1", "NO-SUCH-PART", 5, nil)
	if !errors.Is(err, parts.ErrNotFound) {
		t.Fatalf("Add err = %v, want parts.ErrNotFound", err)
	}
	if _, err := cs.Get("loc1", "NO-SUCH-PART"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("orphan check: component must NOT exist after refusal, got err=%v", err)
	}
}

// TestAdjustQtyAfterPartDeletedNoHistoryAppend pins finding 2 (AdjustQty
// half): the component's Part is gone (deleted store-level, e.g. pre-guard
// data). AdjustQty must fail BEFORE appending History — the old order appended
// + wrote the component first, so a retry double-counted the movement.
func TestAdjustQtyAfterPartDeletedNoHistoryAppend(t *testing.T) {
	cs, ps := newStore(t)
	pid := makePart(t, ps, "ORPHAN-ADJ")
	if err := cs.Add("loc1", pid, 2, nil); err != nil {
		t.Fatal(err)
	}
	// Store-level delete bypasses the composed link guard — exactly how
	// pre-guard orphans arise.
	if err := ps.Delete(pid); err != nil {
		t.Fatal(err)
	}
	err := cs.AdjustQty("loc1", pid, 3, "restock")
	if !errors.Is(err, parts.ErrNotFound) {
		t.Fatalf("AdjustQty err = %v, want parts.ErrNotFound", err)
	}
	c, err := cs.Get("loc1", pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.History) != 1 {
		t.Fatalf("History len = %d, want 1 (no movement appended on refusal)", len(c.History))
	}
}

// TestRemoveOrphanedComponentSucceeds: Remove is the sanctioned cleanup path
// for an orphaned component — it must succeed (the recompute's parts miss is
// expected, not an error the operator can act on).
func TestRemoveOrphanedComponentSucceeds(t *testing.T) {
	cs, ps := newStore(t)
	pid := makePart(t, ps, "ORPHAN-RM")
	if err := cs.Add("loc1", pid, 4, nil); err != nil {
		t.Fatal(err)
	}
	if err := ps.Delete(pid); err != nil {
		t.Fatal(err)
	}
	if err := cs.Remove("loc1", pid); err != nil {
		t.Fatalf("Remove orphaned component err = %v, want nil", err)
	}
	if _, err := cs.Get("loc1", pid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("post-remove Get err = %v, want ErrNotFound", err)
	}
}
