// Package migrate runs sequential, idempotent Pebble schema migrations for
// go-parts. Mirrors muninndb's internal/storage/migrate: a registered set of
// numbered steps, applied in order, with per-step pebble.Sync version writes
// and a refuse-newer guard (a store written by a newer binary is never silently
// downgraded — startup fails loud).
//
// v1 ships ZERO real step-migrations; the schema is v1 from first run. The
// runner exists and is proven by synthetic tests so the path is ready before
// real data exists. RegisterMigrations is the single source of truth, called
// by every open path so library and daemon cannot drift on which migrations
// exist.
package migrate

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"sort"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/storage/keys"
)

// BaselineVersion is the schema version a fresh go-parts store is bootstrapped
// to before any registered step-migration runs. v1 ships with zero
// step-migrations (the schema is v1 from first run), so this is 1. A binary
// that registers step-migrations up to version N has LatestVersion() == N as
// long as N > BaselineVersion.
//
// This constant exists because MaxRegisteredVersion() alone is not the binary's
// actual schema version: in v1 it returns 0 (no migrations registered) yet the
// binary still writes version 1 on fresh install. The refuse-newer guard in
// Runner.Run and the open-path bootstrap in storage.Open both need the binary's
// actual version, not just the highest registered migration.
const BaselineVersion = 1

var migrationVersionKey = keys.MetaSchemaVersionKey()

// Migration is a single numbered schema step. Version is unique within a
// Runner's registered set; Up runs inside a per-step Sync'd version write.
type Migration struct {
	Version     int
	Description string
	Up          func(db *pebble.DB) error
}

// Runner owns a registered set of migrations and applies them against one
// Pebble handle. Not safe for concurrent Register/Run on the same Runner —
// callers wire a Runner once at open and run it once.
type Runner struct {
	migrations []Migration
	db         *pebble.DB
}

// NewRunner returns a Runner bound to db with no migrations registered.
func NewRunner(db *pebble.DB) *Runner { return &Runner{db: db} }

// Register adds a migration to the set. Registrations need not be in version
// order; Run sorts before applying.
func (r *Runner) Register(m Migration) { r.migrations = append(r.migrations, m) }

// RegisterMigrations is the single source of truth, called by every open
// path so library and daemon cannot drift on which migrations exist.
func RegisterMigrations(r *Runner) {
	// v2 (Locations, Slice 3a): the Part record gained two ADDITIVE JSON fields
	// (DefaultLocationID, DefaultLocationMandatory). Additive fields are
	// forward-compatible (a new binary decodes old JSON to ""/false) — but they
	// are NOT backward-compatible: a pre-Locations binary (LatestVersion 1)
	// opens a post-Locations store at cur==latest==1 with no refusal, reads
	// parts via its old struct (encoding/json drops the unknown fields), and on
	// the next Update re-marshals WITHOUT the location fields — silently
	// destroying every assignment (a §5.13 "never silent downgrade-write"
	// violation). This no-op migration bumps the marker to 2 so a pre-Locations
	// binary hits cur=2 > maxRegistered=1 → refuse-newer → hard startup failure
	// (forcing re-upgrade, not silent data destruction). The Up is a no-op
	// because the fields are additive — there is no JSON to transform; only the
	// version marker moves. (RedTeam finding C10.)
	r.Register(Migration{
		Version:     2,
		Description: "Locations: Part gains additive DefaultLocationID/DefaultLocationMandatory (no-op; re-arms refuse-newer for older binaries)",
		Up:          func(db *pebble.DB) error { return nil },
	})
}

// MaxRegisteredVersion returns the highest Version RegisterMigrations would
// register, or 0 when no step-migrations are registered (the v1 case). Used
// by callers that care about registered migrations specifically.
//
// For the binary's actual schema version (what a fresh install is bootstrapped
// to), use LatestVersion — that folds BaselineVersion in.
func MaxRegisteredVersion() int {
	r := &Runner{}
	RegisterMigrations(r)
	max := 0
	for _, m := range r.migrations {
		if m.Version > max {
			max = m.Version
		}
	}
	return max
}

// LatestVersion returns the schema version this binary writes on a fresh
// install — the max of BaselineVersion and any registered step-migration.
// storage.Open uses this to (a) bootstrap a fresh store, (b) refuse a store
// written by a newer binary (stored > LatestVersion ⇒ hard fail), and (c)
// detect when migrations must be applied (stored < LatestVersion).
func LatestVersion() int {
	if max := MaxRegisteredVersion(); max > BaselineVersion {
		return max
	}
	return BaselineVersion
}

// Run applies every registered migration with Version > current stored version,
// in ascending version order, persisting the new version after each step with
// pebble.Sync. Returns the count of migrations applied.
//
// If the stored version is newer than the highest registered migration, Run
// refuses (hard error) rather than silently downgrade-writing the store.
func (r *Runner) Run() (int, error) {
	if len(r.migrations) == 0 {
		return 0, nil
	}
	sort.Slice(r.migrations, func(i, j int) bool { return r.migrations[i].Version < r.migrations[j].Version })
	current, err := readMigrationVersion(r.db)
	if err != nil {
		return 0, fmt.Errorf("migrate: read version: %w", err)
	}
	// maxRegistered starts at BaselineVersion: a v1 binary with no registered
	// step-migrations still understands schema version 1, so reopening a v1
	// store is a no-op (current == maxRegistered), not a refusal
	// (current > maxRegistered). A binary that registers higher versions
	// raises maxRegistered accordingly.
	maxRegistered := BaselineVersion
	for _, m := range r.migrations {
		if m.Version > maxRegistered {
			maxRegistered = m.Version
		}
	}
	if current > maxRegistered {
		return 0, fmt.Errorf("migrate: stored version %d newer than this binary knows (%d); refusing to start", current, maxRegistered)
	}
	applied := 0
	for _, m := range r.migrations {
		if m.Version <= current {
			continue
		}
		slog.Info("applying migration", "version", m.Version, "description", m.Description)
		if err := m.Up(r.db); err != nil {
			return applied, fmt.Errorf("migrate v%d (%s): %w", m.Version, m.Description, err)
		}
		if err := writeMigrationVersion(r.db, m.Version); err != nil {
			return applied, fmt.Errorf("migrate persist v%d: %w", m.Version, err)
		}
		applied++
	}
	return applied, nil
}

// readMigrationVersion returns the stored schema version, or 0 with no error
// when no version key exists yet (fresh store). A short / corrupt value is an
// error, never silently zero.
//
// The marker is encoded as uint64 (8 bytes, big-endian) but the in-memory
// representation is signed int. A high-bit-set value (e.g. 0xFFFFFFFFFFFFFFFF)
// would decode to a negative int and bypass both refuse-newer guards
// (db.go's `cur > latest` and Runner.Run's `current > maxRegistered`, both
// with latest/maxRegistered small positives). A negative cur never trips
// either, so Open would fall into the cur < latest branch and run registered
// migrations against data NOT in the source state they were authored for —
// silent corruption. Reject any value outside [0, math.MaxInt32] at the
// source; both guards inherit readMigrationVersion, so the bypass is closed
// here, once. Platform-independent: bound against MaxInt32, not MaxInt, so a
// 32-bit build is not silently re-exposed.
func readMigrationVersion(db *pebble.DB) (int, error) {
	val, closer, err := db.Get(migrationVersionKey)
	if err == pebble.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer closer.Close()
	if len(val) < 8 {
		return 0, fmt.Errorf("migrate: corrupt version value (len=%d)", len(val))
	}
	v := int64(binary.BigEndian.Uint64(val))
	if v < 0 || v > math.MaxInt32 {
		return 0, fmt.Errorf("migrate: corrupt schema version marker (value=%d)", v)
	}
	return int(v), nil
}

// writeMigrationVersion persists v as the stored schema version with
// pebble.Sync — durability is mandatory between migration steps (a crash here
// must not leave the store at an unknown version).
//
// v must be a sane non-negative version; a negative v would cast to a
// high-bit-set uint64 and, on the next Open, decode back to a negative int
// that bypasses the refuse-newer guard (see readMigrationVersion). Reject at
// the write side too so a future caller cannot persist the bad marker.
func writeMigrationVersion(db *pebble.DB, v int) error {
	if v < 0 {
		return fmt.Errorf("migrate: negative version %d not allowed", v)
	}
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(v))
	return db.Set(migrationVersionKey, buf, pebble.Sync)
}

// ReadVersion is the exported wrapper around readMigrationVersion for the
// open-path bootstrap in storage.Open. Package-internal callers (Task 5's
// tests) keep using the lowercase name; the wrapper is additive.
func ReadVersion(db *pebble.DB) (int, error) { return readMigrationVersion(db) }

// WriteVersion is the exported wrapper around writeMigrationVersion for the
// open-path bootstrap in storage.Open.
func WriteVersion(db *pebble.DB, v int) error { return writeMigrationVersion(db, v) }
