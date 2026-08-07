package storage

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cockroachdb/pebble"
	"github.com/gofrs/flock"
	"github.com/madeinoz67/go-parts/internal/storage/migrate"
)

type Store struct {
	DB   *pebble.DB
	lock *flock.Flock
	path string
}

// Open opens (or creates) the Pebble store at dataDir/pebble. A file lock
// enforces single-process access (the TUI and later surfaces are clients,
// never second openers).
//
// After pebble opens successfully, the schema-version gate runs
// (bootstrapOrMigrate, §5.13): a fresh store is bootstrapped to the binary's
// current version; a same-version store is a no-op; an older store is
// snapshotted then migrated; a newer-version store is refused (hard error,
// never silent downgrade).
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("storage: mkdir data dir: %w", err)
	}
	dbPath := filepath.Join(dataDir, "pebble")
	lockPath := dbPath + ".lock"
	fl := flock.New(lockPath)
	got, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("storage: acquire lock: %w", err)
	}
	if !got {
		return nil, fmt.Errorf("storage: %s is already in use by another go-parts process", dbPath)
	}
	db, err := pebble.Open(dbPath, &pebble.Options{})
	if err != nil {
		fl.Unlock()
		return nil, fmt.Errorf("storage: open pebble: %w", err)
	}
	if err := bootstrapOrMigrate(db, dataDir); err != nil {
		// Migration failed mid-flight: close the DB and release the flock
		// before surfacing the error so a later Open can try again (no lock
		// leak on the migration-failure path).
		_ = db.Close()
		_ = fl.Unlock()
		return nil, fmt.Errorf("storage: migrate: %w", err)
	}
	return &Store{DB: db, lock: fl, path: dbPath}, nil
}

func (s *Store) Close() error {
	err := s.DB.Close()
	if uerr := s.lock.Unlock(); uerr != nil && err == nil {
		err = uerr
	}
	return err
}

// bootstrapOrMigrate is the §5.13 schema-version gate run on every Open. The
// four branches:
//
//   - cur == 0 (no version key yet, fresh store): write LatestVersion().
//     A fresh install gets the CURRENT schema directly — it has no data to
//     transform, so registered step-migrations are NOT applied.
//   - cur > latest: refuse. A store written by a newer binary is never
//     silently downgraded; startup fails loud.
//   - cur < latest: snapshot before, then Run registered step-migrations.
//   - cur == latest: no-op.
//
// The snapshot is best-effort: a checkpoint failure (read-only FS, no space)
// is logged but does not prevent migration. Stranding the store on an old
// version because the safety net couldn't be written would be worse than the
// (already low) risk of migrating without one.
func bootstrapOrMigrate(db *pebble.DB, dataDir string) error {
	cur, err := migrate.ReadVersion(db)
	if err != nil {
		return err
	}
	latest := migrate.LatestVersion()
	if cur > latest {
		return fmt.Errorf("stored schema version %d newer than binary latest %d; refusing to start", cur, latest)
	}
	if cur == 0 {
		// Fresh store: bootstrap to the binary's current version directly.
		if err := migrate.WriteVersion(db, latest); err != nil {
			return fmt.Errorf("bootstrap write v%d: %w", latest, err)
		}
		return nil
	}
	if cur < latest {
		// Snapshot BEFORE any registered migration runs. nextVersion is
		// cur+1 — the version the first migration step would write.
		dest, snapErr := migrate.Snapshot(dataDir, cur+1)
		if snapErr != nil {
			slog.Warn("pre-migration snapshot path setup failed; continuing without snapshot",
				"error", snapErr)
		} else if ckErr := migrate.Checkpoint(db, dest); ckErr != nil {
			slog.Warn("pre-migration checkpoint failed; continuing without snapshot",
				"error", ckErr, "dest", dest)
		}
	}
	// cur == latest falls through to Run, which is a no-op when no
	// migrations have Version > cur.
	r := migrate.NewRunner(db)
	migrate.RegisterMigrations(r)
	if _, err := r.Run(); err != nil {
		return err
	}
	return nil
}
