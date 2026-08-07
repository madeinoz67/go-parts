package storage

import (
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/storage/keys"
	"github.com/madeinoz67/go-parts/internal/storage/migrate"
)

// bootstrapOrMigrate is the open-path migrate gate. v1 ships zero registered
// step-migrations, so these tests pin the three branches that matter today:
// fresh install writes the binary's current version, reopening the same
// binary is a no-op (NOT a refusal), and a store written by a newer binary
// is refused.

func TestOpenBootstrapsFreshStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatalf("Open (fresh): %v", err)
	}
	got, err := migrate.ReadVersion(s.DB)
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	if want := migrate.LatestVersion(); got != want {
		t.Fatalf("fresh stored version = %d, want %d", got, want)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOpenReopensSameVersion(t *testing.T) {
	dir := t.TempDir()
	want := migrate.LatestVersion()
	s1, err := Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatalf("Open s1: %v", err)
	}
	if got, _ := migrate.ReadVersion(s1.DB); got != want {
		t.Fatalf("after first Open, stored version = %d, want %d", got, want)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close s1: %v", err)
	}
	// Reopening a store written by this same binary must succeed AND must
	// leave the stored version unchanged. A v1 binary has LatestVersion() == 1
	// and zero registered migrations, so cur == latest must be a no-op, not a
	// refusal.
	s2, err := Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatalf("Open s2 (reopen same version): %v", err)
	}
	if got, _ := migrate.ReadVersion(s2.DB); got != want {
		t.Fatalf("after reopen, stored version = %d, want %d", got, want)
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("Close s2: %v", err)
	}
}

func TestOpenRefusesNewerStore(t *testing.T) {
	dir := t.TempDir()
	// Hand-construct a store that claims to be from a newer binary by
	// writing a version above LatestVersion() before going through Open.
	s, err := Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatalf("Open to seed: %v", err)
	}
	future := migrate.LatestVersion() + 5
	if err := migrate.WriteVersion(s.DB, future); err != nil {
		t.Fatalf("WriteVersion: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close after seed: %v", err)
	}
	if _, err := Open(filepath.Join(dir, "data")); err == nil {
		t.Fatal("Open succeeded against newer-version store; want refuse error")
	}
}

// TestOpenRefusesHighBitVersionMarker pins the §5.13 invariant that a
// corrupt (uninterpretable) schema-version marker is a hard startup failure,
// never a silent bypass of the refuse-newer guard. The version marker is
// encoded uint64 but decoded as signed int; a high-bit-set value decodes
// negative and would slip past both `cur > latest` guards in db.go and
// migrate.go, dropping the store into the migration branch against data the
// migration was not authored for. Today (zero registered migrations) the
// corruption is latent; the first real migration turns it into silent data
// corruption. Seed the exact bytes a future `WriteVersion(-1)` caller or a
// corrupt restore would leave, and require Open to refuse.
func TestOpenRefusesHighBitVersionMarker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	highBit := make([]byte, 8)
	binary.BigEndian.PutUint64(highBit, 0xFFFFFFFFFFFFFFFF)
	if err := s.DB.Set(keys.MetaSchemaVersionKey(), highBit, pebble.Sync); err != nil {
		t.Fatalf("seed high-bit: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// PRD §5.13 invariant 1: a corrupt version marker MUST refuse — not
	// fall through to the migration branch against unknown source state.
	if reopened, err := Open(dir); err == nil {
		reopened.Close()
		t.Fatal("refuse-newer BYPASS: Open succeeded against uint64-max version marker")
	}
}
