package storage

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cockroachdb/pebble"
	"github.com/gofrs/flock"
)

type Store struct {
	DB   *pebble.DB
	lock *flock.Flock
	path string
}

// Open opens (or creates) the Pebble store at dataDir/pebble. A file lock
// enforces single-process access (the TUI and later surfaces are clients,
// never second openers).
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
	return &Store{DB: db, lock: fl, path: dbPath}, nil
}

func (s *Store) Close() error {
	err := s.DB.Close()
	if uerr := s.lock.Unlock(); uerr != nil && err == nil {
		err = uerr
	}
	return err
}
