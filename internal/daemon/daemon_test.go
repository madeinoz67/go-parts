package daemon

import (
	"path/filepath"
	"testing"
)

// TestStatusReportsNotRunning is the brief's RED-sanity test: a fresh data
// directory with no daemon.json MUST report Running=false (and never error —
// absence of a state file is the canonical "not running" state, not a failure).
func TestStatusReportsNotRunning(t *testing.T) {
	dir := t.TempDir()
	st, err := Status(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Running {
		t.Fatal("fresh dir reports Running=true; want false")
	}
}

// TestStatusStaleStateFileNotRunning guards the liveness check: a daemon.json
// pointing at a PID that no longer exists (a stale state file left by an
// unclean exit) MUST report Running=false, not blindly trust the file.
// We use PID 1 because PID 1 always exists on macOS/Linux — so we instead
// write a PID that is essentially guaranteed-not-to-be-us: a very large PID
// that is unlikely to correspond to any live process. This test asserts the
// processAlive path is consulted, not just the file's presence.
func TestStatusStaleStateFileNotRunning(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	// Write a state file pointing at a PID that will not be alive. Use a very
	// high PID — on most Unixes the max is 99999 (sysctl kern.maxproc), so
	// 4194303 is essentially guaranteed-unused. processAlive must return false.
	if err := writeStateForTest(dataDir, State{Bind: "127.0.0.1:7890", PID: 4194303, Running: true}); err != nil {
		t.Fatal(err)
	}
	st, err := Status(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Running {
		t.Fatalf("stale state file with dead PID reports Running=true; want false (pid liveness check missing?)")
	}
}

// writeStateForTest is a test-only helper that writes a state file directly,
// bypassing the daemon, so we can exercise the Status read path against a
// known on-disk fixture.
func writeStateForTest(dataDir string, st State) error {
	return writeStateFile(dataDir, st)
}
