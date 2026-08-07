package migrate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
)

func TestSnapshotCreatesDir(t *testing.T) {
	src := filepath.Join(t.TempDir(), "pebble")
	db, err := pebble.Open(src, &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Set([]byte("k"), []byte("v"), nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	// Snapshot returns the destination path and creates the parent
	// migration-backups/ dir; Checkpoint writes the actual Pebble data into
	// dest (Pebble requires dest to NOT pre-exist — Snapshot must not
	// pre-create dest itself, only its parent).
	dest, err := Snapshot(filepath.Dir(src), 2)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := Checkpoint(db, dest); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("snapshot dir missing: %v", err)
	}
	// The backup must be openable as a real Pebble store and contain the
	// snapshot's data — otherwise the safety net is illusory.
	bk, err := pebble.Open(dest, &pebble.Options{})
	if err != nil {
		t.Fatalf("reopen backup: %v", err)
	}
	defer bk.Close()
	val, closer, err := bk.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get from backup: %v", err)
	}
	defer closer.Close()
	if string(val) != "v" {
		t.Fatalf("backup value = %q, want %q", string(val), "v")
	}
}
