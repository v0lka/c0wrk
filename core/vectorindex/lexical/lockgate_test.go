package lexical

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

// shrinkGateWait bounds the gate wait for real-clock tests: a busy refusal
// then costs one bounded watchdog window instead of DefaultGateWait. The
// value is restored on cleanup so package-level state never leaks.
func shrinkGateWait(t *testing.T, d time.Duration) {
	t.Helper()
	saved := gateWait
	gateWait = d
	t.Cleanup(func() { gateWait = saved })
}

// TestLockGate_SentinelCreatedInsideIndexDir pins the sentinel's placement:
// inside the index directory, so every quarantine scope (which removes the
// whole lexical leg) removes the doomed gate with it and the rebuild lands
// on a gate-free directory.
func TestLockGate_SentinelCreatedInsideIndexDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lex")
	idx, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	if _, err := os.Stat(filepath.Join(dir, gateFileName)); err != nil {
		t.Fatalf("lock-gate sentinel missing after Open: %v", err)
	}
}

// TestLockGate_BusyRefusesWithinDeadlineVirtualTime pins the whole point of
// the gate: with a live exclusive holder, a second open must refuse with
// ErrLockBusy after roughly gateWait — not hang on bbolt's unbounded lock.
// The bubble's virtual clock traverses the full wait instantly (the probes
// themselves are instant non-blocking syscalls), so the test costs no real
// time and no sleep.
func TestLockGate_BusyRefusesWithinDeadlineVirtualTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "lex")
		holder, err := Open(dir)
		if err != nil {
			t.Fatalf("Open (holder): %v", err)
		}
		defer func() { _ = holder.Close() }()

		start := time.Now()
		if _, err := Open(dir); !errors.Is(err, ErrLockBusy) {
			t.Fatalf("busy Open error = %v, want ErrLockBusy", err)
		}
		if elapsed := time.Since(start); elapsed < gateWait || elapsed >= 2*gateWait {
			t.Fatalf("busy Open refused after %v, want [%v, 2*%v)", elapsed, gateWait, gateWait)
		}

		// A read-only open shares the gate only with other readers: an
		// exclusive holder refuses it too, within the same budget.
		start = time.Now()
		if _, err := OpenReadOnly(dir); !errors.Is(err, ErrLockBusy) {
			t.Fatalf("busy OpenReadOnly error = %v, want ErrLockBusy", err)
		}
		if elapsed := time.Since(start); elapsed < gateWait || elapsed >= 2*gateWait {
			t.Fatalf("busy OpenReadOnly refused after %v, want [%v, 2*%v)", elapsed, gateWait, gateWait)
		}
	})
}

// TestLockGate_ReadersShareGate pins the ADR-104 concurrent-readers shape:
// read-only opens hold the gate SHARED, so several corpus readers keep one
// published snapshot open at once — while a single writer's exclusive open
// refuses fast against them (and they against it).
func TestLockGate_ReadersShareGate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lex")
	ctx := context.Background()

	writer, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := writer.Upsert(ctx, []Doc{{ID: "s:0", Content: "shared snapshot content", FilePath: "/p/s.go", Language: "go"}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	shrinkGateWait(t, 30*time.Millisecond)

	r1, err := OpenReadOnly(dir)
	if err != nil {
		t.Fatalf("first read-only open: %v", err)
	}
	defer func() { _ = r1.Close() }()
	r2, err := OpenReadOnly(dir)
	if err != nil {
		t.Fatalf("second read-only open while the first is live = %v, want shared admission", err)
	}
	defer func() { _ = r2.Close() }()
	for name, r := range map[string]Index{"r1": r1, "r2": r2} {
		if n, err := r.Count(); err != nil || n != 1 {
			t.Errorf("%s Count = %d, %v; want 1, nil", name, n, err)
		}
	}

	// A writer open while the readers hold the gate refuses fast — the
	// exact shape bbolt used to wedge on forever.
	if _, err := Open(dir); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("writer open against live readers error = %v, want ErrLockBusy", err)
	}
}

// TestLockGate_CloseReleasesGate pins the gate's lifetime: it spans the open
// exactly, so a closed index leaves the directory lock-free for the next
// opener immediately.
func TestLockGate_CloseReleasesGate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lex")
	shrinkGateWait(t, 30*time.Millisecond)

	idx, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	idx2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after Close = %v, want the gate released", err)
	}
	if err := idx2.Close(); err != nil {
		t.Fatalf("reopen Close: %v", err)
	}
}

// TestLockGate_QuarantineRebuildWithLiveHolder pins the ADR-120 attach rung
// end to end: a busy open refuses fast while a stuck holder lives, the
// quarantine (which removes the whole lexical leg — sentinel included) frees
// the path, the rebuild lands on a fresh directory whose gate locks again,
// and the orphaned holder keeps its fds and closes cleanly — it never
// wedges the rebuilt index.
func TestLockGate_QuarantineRebuildWithLiveHolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lex")
	ctx := context.Background()
	shrinkGateWait(t, 30*time.Millisecond)

	holder, err := Open(dir)
	if err != nil {
		t.Fatalf("Open (stuck holder): %v", err)
	}

	// The busy refusal costs one bounded window, not an eternal bbolt wait.
	if _, err := Open(dir); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("busy open while holder lives error = %v, want ErrLockBusy", err)
	}

	// Quarantine: the repair ladder's store scope removes the whole leg.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("quarantine RemoveAll: %v", err)
	}

	// Rebuild: a fresh directory (new inode) with a fresh, free gate.
	rebuilt, err := Open(dir)
	if err != nil {
		t.Fatalf("rebuild while holder still lives = %v, want a fresh index", err)
	}
	if err := rebuilt.Upsert(ctx, []Doc{{ID: "n:0", Content: "rebuilt corpus content", FilePath: "/p/n.go", Language: "go"}}); err != nil {
		t.Fatalf("rebuilt Upsert: %v", err)
	}
	if n, err := rebuilt.Count(); err != nil || n != 1 {
		t.Fatalf("rebuilt Count = %d, %v; want 1, nil", n, err)
	}
	if hits, err := rebuilt.Query(ctx, "rebuilt", 5); err != nil || len(hits) != 1 {
		t.Fatalf("rebuilt Query hits = %+v, %v; want 1 hit", hits, err)
	}

	// The fresh gate is itself enforced for the next opener.
	if _, err := Open(dir); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("opener against rebuilt index error = %v, want ErrLockBusy", err)
	}

	// The orphaned holder never invalidated: it closes cleanly even though
	// its directory was removed from under it.
	if err := holder.Close(); err != nil {
		t.Fatalf("stuck holder Close after quarantine = %v, want clean fd close", err)
	}
	if err := rebuilt.Close(); err != nil {
		t.Fatalf("rebuilt Close: %v", err)
	}
}
