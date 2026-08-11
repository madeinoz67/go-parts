// Package daemon implements go-parts' foreground server lifecycle:
// Run (serve until SIGINT/SIGTERM), Stop (signal the PID recorded in the
// state file), and Status (read the state file + check PID liveness).
//
// The state model is a single `<data_dir>/daemon.json` file with the bound
// address + PID + running flag. This mirrors go-rag's `internal/daemon`
// shape (PID + bound-address sidecar, liveness via kill(pid, 0)) at a
// simpler resolution: one JSON file instead of pid + addrs files, because
// go-parts has a single transport in v1.
//
// `start` runs the server in the FOREGROUND (it is the server process). This
// is a deliberate v1 simplification: the operator backgrounds it with `&` or
// a service manager. go-rag's `start` re-execs a detached `serve` child; we
// don't need that yet — single-operator, one transport, no stdio proxy.
//
// Confluence: storage.Open already runs the §5.13 schema-version gate
// (bootstrap/migrate/refuse-newer), so `start` inherits schema safety by
// opening the store before it binds.
package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/madeinoz67/go-parts/internal/config"
	"github.com/madeinoz67/go-parts/internal/index"
	"github.com/madeinoz67/go-parts/internal/link"
	"github.com/madeinoz67/go-parts/internal/locations"
	"github.com/madeinoz67/go-parts/internal/parts"
	"github.com/madeinoz67/go-parts/internal/rest"
	"github.com/madeinoz67/go-parts/internal/storage"
	"github.com/madeinoz67/go-parts/internal/ui"
	"github.com/madeinoz67/go-parts/internal/via"
)

// stateFileName is the single source of truth for the running daemon's state.
// A new prefix → disjoint check is required before adding any sibling file
// (§5.13 keyspace-registry discipline applies to the data dir, not just
// Pebble). v1 owns daemon.json; nothing else writes here.
const stateFileName = "daemon.json"

// State is the on-disk record of a running daemon. Running is recomputed by
// Status each call from PID liveness; the on-disk value is informational
// (Run writes true, the deferred cleanup removes the file).
type State struct {
	Bind    string `json:"bind"`
	PID     int    `json:"pid"`
	Running bool   `json:"running"`
}

func statePath(dataDir string) string { return filepath.Join(dataDir, stateFileName) }

// Run opens the store (storage.Open bootstraps/migrates the schema, §5.13),
// wires the parts.Store + FTS + rest.Server, listens on cfg.Bind, writes the
// state file, and serves until SIGINT or SIGTERM arrives. The state file is
// removed on exit (deferred) so a clean shutdown leaves no stale fixture.
//
// This function does NOT return until a signal arrives or the server fails
// to start. The caller (the cobra `start` subcommand) reports the returned
// error.
func Run(cfg config.Config) error {
	// storage.Open acquires the Pebble flock; a second `start` against the
	// same data dir fails here (clear error, no port thrash).
	storeDB, err := storage.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer storeDB.Close()

	fts := index.NewFTS(storeDB.DB)
	viaStore := via.NewStore(storeDB.DB) // shared spine; the single per-process *via.Store
	store := parts.NewStore(storeDB.DB, fts, viaStore)
	// Locations Slice 3a: construct the locations store over the same DB + via
	// spine and inject the single_part_only guard into the parts store. The
	// guard is now live on every parts PATCH/Create the daemon serves; the
	// locations REST/UI surfaces (list/picker) land in Slice 3b. locStore is
	// held for the policy composition now and consumed by the locations
	// transport when that arrives.
	locStore := locations.NewStore(storeDB.DB, viaStore)
	store.SetLocationPolicy(link.NewPolicy(store, locStore))
	// Compose: UI at /ui/, REST at root, GET / → /ui/ redirect.
	restSrv := rest.NewServer(store, fts, viaStore, locStore) // Slice 4: + via spine + locations for the resolver/labels
	restSrv.SetPublicBaseURL(cfg.PublicBaseURL)
	uiSrv := ui.NewServer(store, fts, locStore)
	mux := http.NewServeMux()
	mux.Handle("/ui/", uiSrv)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusSeeOther)
	})
	mux.Handle("/", restSrv)
	srv := &http.Server{Addr: cfg.Bind, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	// Bind before publishing the state file: a port-in-use error must surface
	// BEFORE we tell the world we're running. ln.Addr().String() captures the
	// actually-bound address (a ":0" cfg.Bind would assign an ephemeral port
	// and we'd publish the resolved one — useful for tests).
	ln, err := net.Listen("tcp", cfg.Bind)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Bind, err)
	}
	if err := writeStateFile(cfg.DataDir, State{
		Bind:    ln.Addr().String(),
		PID:     os.Getpid(),
		Running: true,
	}); err != nil {
		_ = ln.Close()
		return fmt.Errorf("write state file: %w", err)
	}
	defer os.Remove(statePath(cfg.DataDir))

	// Serve in the foreground; the goroutine's Serve call blocks until
	// srv.Close() runs (after the signal arrives). The signal handler closes
	// the listener via srv.Close, which unblocks Serve.
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ln)
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sig:
		// Clean shutdown path. srv.Close closes the listener; Serve returns
		// http.ErrServerClosed, which we suppress.
		_ = srv.Close()
		<-serveErr
		return nil
	case err := <-serveErr:
		// Serve failed before any signal (e.g. listener accept error). Report
		// it; the deferred os.Remove cleans the state file.
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	}
}

// Stop reads the state file at dataDir and sends SIGTERM to the recorded PID.
// A not-running data dir is an error (the operator asked to stop something
// that isn't there). SIGTERM gives the server the same clean-shutdown path
// as a Ctrl-C: Run's signal handler closes the listener + the deferred
// os.Remove runs.
func Stop(dataDir string) error {
	st, err := Status(dataDir)
	if err != nil {
		return err
	}
	if !st.Running || st.PID == 0 {
		return errors.New("daemon not running")
	}
	if err := syscall.Kill(st.PID, syscall.SIGTERM); err != nil {
		// ESRCH means the process exited between our Status check and the
		// signal — a clean race; treat as success.
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("signal pid %d: %w", st.PID, err)
	}
	return nil
}

// Status reads the state file at dataDir and checks the recorded PID's
// liveness. A missing state file yields Running=false with no error (the
// canonical "not running" state). A state file pointing at a dead PID yields
// Running=false (the file is stale — left by an unclean shutdown). Only a
// state file with a live PID yields Running=true.
//
// Status is safe to call concurrently with Run/Stop (it only reads the state
// file and probes the PID). It is the read path the `status` subcommand and
// the `stop` precondition both use.
func Status(dataDir string) (State, error) {
	b, err := os.ReadFile(statePath(dataDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{Running: false}, nil
		}
		return State{}, fmt.Errorf("read state file: %w", err)
	}
	var st State
	if jerr := json.Unmarshal(b, &st); jerr != nil {
		// Malformed state file: don't trust it, don't crash — report not
		// running and let the operator investigate. The state file is owned
		// by this package, so a malformed file is either a disk fault or a
		// hand-edit; either way, refusing to claim Running=true is safe.
		return State{Running: false}, nil
	}
	if st.PID != 0 && processAlive(st.PID) {
		st.Running = true
		return st, nil
	}
	st.Running = false
	return st, nil
}

// writeStateFile serializes st to <dataDir>/daemon.json. The file is created
// with 0600 (read/write owner-only) — the state file carries no secret, but
// tightening perms matches the rest of the data dir's posture and prevents a
// non-operator user from rewriting the PID to weaponize Stop.
func writeStateFile(dataDir string, st State) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("mkdir data dir: %w", err)
	}
	b, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := os.WriteFile(statePath(dataDir), b, 0o600); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	return nil
}

// processAlive returns true iff pid is a live process. kill(pid, 0) sends no
// signal — it only checks permission + existence. EPERM (no permission to
// signal the pid) is treated as alive: the process exists, we just can't
// signal it (a multi-user scenario where a different uid owns go-parts). On
// macOS/Linux this is the standard cheap liveness probe.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.EPERM) {
		return true
	}
	return false
}
