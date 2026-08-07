package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cockroachdb/pebble"
)

// Snapshot writes a Pebble checkpoint to
// <dataDir>/migration-backups/pre-v{N}-{ts}/ BEFORE a migration touches the
// store. Non-interactive safety net (§5.13) — there is no prompt to hang a
// container.
//
// Snapshot computes the destination path and ensures the parent
// migration-backups/ directory exists; it does NOT create dest itself;
// Checkpoint (which calls pebble's DB.Checkpoint) creates dest, and pebble
// requires dest to not pre-exist. The returned path is what the caller should
// pass to Checkpoint.
//
// A Snapshot/Checkpoint failure is best-effort: log it and continue. A system
// where the checkpoint cannot be created (read-only FS, no space, etc.) should
// still migrate, not strand the store on an old version.
func Snapshot(dataDir string, nextVersion int) (string, error) {
	ts := time.Now().UTC().Format("20060102-150405")
	dir := filepath.Join(dataDir, "migration-backups", fmt.Sprintf("pre-v%d-%s", nextVersion, ts))
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", fmt.Errorf("snapshot mkdir migration-backups: %w", err)
	}
	return dir, nil
}

// Checkpoint creates dest from a live db via Pebble's checkpoint API. dest
// must NOT pre-exist (Pebble refuses with ErrExist otherwise); callers obtain
// dest from Snapshot, which only pre-creates the parent directory.
func Checkpoint(db *pebble.DB, dest string) error {
	return db.Checkpoint(dest)
}
