package migrate

import (
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/storage/keys"
)

func newDB(t *testing.T) *pebble.DB {
	t.Helper()
	db, err := pebble.Open(filepath.Join(t.TempDir(), "p"), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRunAppliesInOrder(t *testing.T) {
	db := newDB(t)
	var saw []int
	r := NewRunner(db)
	r.Register(Migration{3, "c", func(*pebble.DB) error { saw = append(saw, 3); return nil }})
	r.Register(Migration{1, "a", func(*pebble.DB) error { saw = append(saw, 1); return nil }})
	r.Register(Migration{2, "b", func(*pebble.DB) error { saw = append(saw, 2); return nil }})
	applied, err := r.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if applied != 3 || len(saw) != 3 || saw[0] != 1 || saw[1] != 2 || saw[2] != 3 {
		t.Fatalf("applied=%d saw=%v", applied, saw)
	}
	got, _ := readMigrationVersion(db)
	if got != 3 {
		t.Fatalf("stored version = %d, want 3", got)
	}
}

func TestRunIdempotent(t *testing.T) {
	db := newDB(t)
	calls := 0
	r := NewRunner(db)
	r.Register(Migration{1, "x", func(*pebble.DB) error { calls++; return nil }})
	r.Run()
	if _, err := r.Run(); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if calls != 1 {
		t.Fatalf("Up called %d times, want 1", calls)
	}
}

func TestRefuseNewer(t *testing.T) {
	db := newDB(t)
	if err := writeMigrationVersion(db, 9); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(db)
	r.Register(Migration{1, "x", func(*pebble.DB) error { return nil }})
	if _, err := r.Run(); err == nil {
		t.Fatal("Run succeeded against newer store; want refuse error")
	}
	_ = keys.PartsKey // keep import alive if unused otherwise
}
