package link

import (
	"errors"
	"testing"

	"github.com/madeinoz67/go-parts/internal/parts"
)

// TestDeletePart_RefusesWhenStocked pins code-review finding 3: deleting a
// Part whose Components still exist orphans their rows (bare-ULID rendering,
// phantom counts). The guard composes here — parts.Store cannot see the
// components keyspace (one-way dependency §5.1) — mirroring locations'
// ErrHasParts pattern.
func TestDeletePart_RefusesWhenStocked(t *testing.T) {
	ps, _, cs, _ := newPairWithVia(t)
	p := &parts.Part{MPN: "STOCKED-DEL", PartType: "local"}
	if err := ps.Create(p); err != nil {
		t.Fatal(err)
	}
	if err := cs.Add("loc1", p.ID, 3, nil); err != nil {
		t.Fatal(err)
	}
	err := DeletePart(ps, cs, p.ID)
	if !errors.Is(err, parts.ErrHasComponents) {
		t.Fatalf("DeletePart err = %v, want parts.ErrHasComponents", err)
	}
	if _, err := ps.Get(p.ID); err != nil {
		t.Fatalf("refused delete must leave the part: %v", err)
	}
}

// TestDeletePart_UnstockedSucceeds: with no Components referencing it, the
// delete is an ordinary parts.Delete (record, FTS, via, identity pins).
func TestDeletePart_UnstockedSucceeds(t *testing.T) {
	ps, _, cs, _ := newPairWithVia(t)
	p := &parts.Part{MPN: "FREE-DEL", PartType: "local"}
	if err := ps.Create(p); err != nil {
		t.Fatal(err)
	}
	if err := DeletePart(ps, cs, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.Get(p.ID); !errors.Is(err, parts.ErrNotFound) {
		t.Fatalf("post-delete Get err = %v, want ErrNotFound", err)
	}
}
