package parts

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/storage/keys"
)

// ErrDuplicateMPN is returned by Create/Update when another part already pins
// the requested MPN in the 0x14 identity index (schema v5, §6.1 uniqueness).
// A sentinel so REST maps it to 409 and the UI to a create/edit banner.
var ErrDuplicateMPN = errors.New("parts: duplicate mpn")

// ErrDuplicateLocalNumber is the LocalNumber twin of ErrDuplicateMPN. Empty
// LocalNumber is exempt (pre-backfill parts have none assigned).
var ErrDuplicateLocalNumber = errors.New("parts: duplicate local number")

// The 0x14 identity index pins kind('M'|'L') | ws(8) | value → partID,
// mirroring the via index's reserve/release discipline (§5.17 pattern):
// reserve-before-write with release-on-failure compensation, so a failed
// write never burns the operator's chosen number. The value compared is the
// exact stored string in v1 (no case folding) — MPN case is manufacturer
// intent, not taxonomy.

// reserveIdent writes the index entry for kind|value→id, refusing if the
// value is already pinned. Serialized under s.identMu so two concurrent
// reserves of one value cannot both observe absent and both write (the same
// check-then-write guard via.Store.Reserve carries). Per-INSTANCE mutex: the
// one-Store-per-DB discipline is inherited from NewStore's callers (daemon
// constructs one; CLI is one-shot), and cross-PROCESS racing is impossible —
// Pebble's file lock means a second go-parts process cannot open the DB at
// all. An idempotent re-reserve by the SAME id is a no-op (the v5 backfill
// and retry paths rely on it).
func (s *Store) reserveIdent(kind keys.IdentKind, value, id string) error {
	if value == "" {
		return nil // empty is exempt from uniqueness (pre-backfill LocalNumber; empty MPN)
	}
	s.identMu.Lock()
	defer s.identMu.Unlock()
	var ws [8]byte
	key := keys.IdentIndexKey(ws, kind, value)
	if val, closer, err := s.db.Get(key); err == nil {
		_ = closer.Close()
		if string(val) == id {
			return nil // same owner re-reserving (idempotent)
		}
		return fmt.Errorf("%w: %s", identErr(kind), value)
	} else if !errors.Is(err, pebble.ErrNotFound) {
		return fmt.Errorf("parts: ident reserve %s: %w", value, err)
	}
	if err := s.db.Set(key, []byte(id), pebble.Sync); err != nil {
		return fmt.Errorf("parts: ident reserve %s: %w", value, err)
	}
	return nil
}

// releaseIdent removes the index entry (idempotent — releasing an absent or
// differently-owned entry is a no-op, so compensation on a failed write can
// never strip a legitimate owner).
func (s *Store) releaseIdent(kind keys.IdentKind, value, id string) {
	if value == "" {
		return
	}
	s.identMu.Lock()
	defer s.identMu.Unlock()
	var ws [8]byte
	key := keys.IdentIndexKey(ws, kind, value)
	val, closer, err := s.db.Get(key)
	if err != nil {
		return // absent: nothing to release
	}
	_ = closer.Close()
	if string(val) != id {
		return // pinned by another part (dup loser after backfill): leave it
	}
	_ = s.db.Delete(key, pebble.Sync)
}

// backfillIdentIndex reserves every part's MPN + non-empty LocalNumber in
// ULID (List) order — deterministic first-writer-wins. Called from NewStore:
// a pre-v5 store has records but no index entries; the backfill is idempotent
// (same-id re-reserve is a no-op), so it is safe on every open. Duplicate
// losers simply fail their reserve and stay unindexed — surfaced later by the
// dedupe report tool, repaired by the operator editing the loser's MPN
// (Update re-reserves correctly).
func (s *Store) backfillIdentIndex() {
	// Adversary finding 1 remediation: the da878df..278bfe4 interim key shape
	// omitted the 0x14 prefix, so pins landed at raw 0x4C/0x4D. Purge that
	// stray range once (idempotent — deleting absent keys is a no-op) before
	// re-pinning everything under the corrected shape. The range was never
	// registered to anyone else and only this build's binaries ever wrote it.
	s.identMu.Lock()
	if it, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte{0x4C}, UpperBound: []byte{0x4F},
	}); err == nil {
		for it.First(); it.Valid(); it.Next() {
			_ = s.db.Delete(it.Key(), pebble.Sync)
		}
		it.Close()
	}
	s.identMu.Unlock()
	// Adversary finding 6: pin TRIMMED values, matching Create/Update's write
	// normalization — a legacy " PAD-1 " must collide with a new "PAD-1".
	for _, p := range s.List() {
		_ = s.reserveIdent(keys.IdentMPN, trimIdent(p.MPN), p.ID)
		_ = s.reserveIdent(keys.IdentLocal, trimIdent(p.LocalNumber), p.ID)
	}
}

// identErr maps a kind to its sentinel for the wrap in reserveIdent.
func identErr(kind keys.IdentKind) error {
	if kind == keys.IdentLocal {
		return ErrDuplicateLocalNumber
	}
	return ErrDuplicateMPN
}

// trimIdent normalizes an identity value for storage/comparison in v1:
// surrounding whitespace only (an operator's pasted number shouldn't fork on
// a trailing space). No case folding — MPN case is manufacturer intent.
func trimIdent(v string) string { return strings.TrimSpace(v) }
