package daemon

import (
	"os/exec"
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
//
// The stale PID is a recycled one: spawn a throwaway process, Wait for it,
// then use its now-free PID as the fixture. This is provably-dead at the
// moment we write the state file, regardless of any kernel's pid_max — the
// previous fixture (4194303) sat INSIDE Linux's default pid_max
// (/proc/sys/kernel/pid_max defaults to 4194304 on 64-bit) and could falsely
// appear alive if the kernel reused it. kern.maxproc (cited in the old
// comment) bounds process COUNT, not PID values — the rationale was wrong.
func TestStatusStaleStateFileNotRunning(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	// Acquire a provably-dead PID: run a process to completion and read its
	// (now-recycled) PID back. The reuse window before our Status call is
	// sub-millisecond; a kernel re-assigning this exact PID to a live process
	// inside that window is astronomically unlikely (and strictly less likely
	// than the fixed-constant fixture it replaces).
	dead := exec.Command("sleep", "0")
	if err := dead.Run(); err != nil {
		t.Fatalf("setup throwaway process: %v", err)
	}
	stalePID := dead.ProcessState.Pid()
	if err := writeStateForTest(dataDir, State{Bind: "127.0.0.1:7890", PID: stalePID, Running: true}); err != nil {
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
