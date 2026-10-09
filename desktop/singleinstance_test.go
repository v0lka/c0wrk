package desktop

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/options"
)

// TestAcquireSingleInstanceLock_FirstInstance asserts the happy path: the
// lock is acquired exactly once per path and reported as first instance.
func TestAcquireSingleInstanceLock_FirstInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.lock")
	lock, first, err := AcquireSingleInstanceLock(path)
	if err != nil {
		t.Fatalf("AcquireSingleInstanceLock: %v", err)
	}
	if !first {
		t.Fatal("expected first instance for a fresh lock path")
	}
	if lock == nil {
		t.Fatal("expected a non-nil lock for the first instance")
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestAcquireSingleInstanceLock_SecondInstanceDenied pins the core gate
// semantics: while one holder keeps the lock, a second acquisition of the
// same path reports a second instance. The same-process case is exactly the
// cross-process mechanism (flock / LockFileEx are keyed to the open file
// description / handle, not the pid), so this is a faithful in-process
// rehearsal of the LaunchServices-spawned second process.
func TestAcquireSingleInstanceLock_SecondInstanceDenied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.lock")
	first, isFirst, err := AcquireSingleInstanceLock(path)
	if err != nil || !isFirst {
		t.Fatalf("first acquire: first=%v err=%v", isFirst, err)
	}
	defer func() { _ = first.Close() }()

	second, isSecondFirst, err := AcquireSingleInstanceLock(path)
	if err != nil {
		t.Fatalf("second acquire errored (must be a clean not-held report): %v", err)
	}
	if isSecondFirst {
		t.Fatal("second acquire must report second instance while the lock is held")
	}
	if second != nil {
		t.Fatal("second acquire must not return a lock")
	}
}

// TestAcquireSingleInstanceLock_ReleaseAllowsReacquire: closing the holder's
// lock frees the gate for the next process.
func TestAcquireSingleInstanceLock_ReleaseAllowsReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.lock")
	lock, _, err := AcquireSingleInstanceLock(path)
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	again, first, err := AcquireSingleInstanceLock(path)
	if err != nil || !first {
		t.Fatalf("re-acquire after release: first=%v err=%v", first, err)
	}
	_ = again.Close()
}

// TestAcquireSingleInstanceLock_CloseIdempotent: the deferred Close in main
// must be safe to call repeatedly (and on a nil lock — the fail-open path
// returns none).
func TestAcquireSingleInstanceLock_CloseIdempotent(t *testing.T) {
	var nilLock *InstanceLock
	if err := nilLock.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
	lock, _, err := AcquireSingleInstanceLock(filepath.Join(t.TempDir(), "app.lock"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestAcquireSingleInstanceLock_FailOpenOnSetupError: an unusable lock path
// (parent is a regular file, so MkdirAll cannot create the directory) must
// fail OPEN — reported as first instance with the error, never as a second
// instance and never as a hard failure that bricks startup.
func TestAcquireSingleInstanceLock_FailOpenOnSetupError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o640); err != nil {
		t.Fatalf("creating blocker file: %v", err)
	}
	lock, first, err := AcquireSingleInstanceLock(filepath.Join(blocker, "nested", "app.lock"))
	if err == nil {
		t.Fatal("expected a setup error for a lock path under a regular file")
	}
	if !first {
		t.Fatal("setup errors must fail open to first instance")
	}
	if lock != nil {
		t.Fatal("no lock may be returned on a setup error")
	}
}

// TestAcquireSingleInstanceLock_IndependentPaths: distinct lock paths gate
// independently — the temp-dir scoping of t.TempDir in the other tests, made
// explicit so a future refactor to a fixed global path cannot pass unnoticed.
func TestAcquireSingleInstanceLock_IndependentPaths(t *testing.T) {
	root := t.TempDir()
	lockA, firstA, err := AcquireSingleInstanceLock(filepath.Join(root, "a.lock"))
	if err != nil || !firstA {
		t.Fatalf("acquire a: first=%v err=%v", firstA, err)
	}
	defer func() { _ = lockA.Close() }()
	lockB, firstB, err := AcquireSingleInstanceLock(filepath.Join(root, "b.lock"))
	if err != nil || !firstB {
		t.Fatalf("acquire b: first=%v err=%v", firstB, err)
	}
	_ = lockB.Close()
}

// TestSingleInstanceOptions_Wiring: the Wails option carries the bundle-id
// identity and relays to the app handler.
func TestSingleInstanceOptions_Wiring(t *testing.T) {
	app := NewApp()
	opts := SingleInstanceOptions(app)
	if opts == nil {
		t.Fatal("SingleInstanceOptions must return a non-nil option")
	}
	if opts.UniqueId != SingleInstanceLockID {
		t.Fatalf("UniqueId = %q, want %q", opts.UniqueId, SingleInstanceLockID)
	}
	if opts.OnSecondInstanceLaunch == nil {
		t.Fatal("OnSecondInstanceLaunch must be wired")
	}
}

// TestHandleSecondInstanceLaunch_FocusesWindow: a relay on a live app focuses
// the existing window through the showWindow funnel (the same reveal-AND-raise
// the notification-click path uses) and logs the relay for diagnostics.
func TestHandleSecondInstanceLaunch_FocusesWindow(t *testing.T) {
	app := NewApp()
	app.ctx = context.Background()
	showCtx := make(chan context.Context, 1)
	app.windowShowFn = func(ctx context.Context) { showCtx <- ctx }

	log, buf := captureLogger()
	app.logger = log

	app.handleSecondInstanceLaunch(options.SecondInstanceData{
		Args:             []string{"--late-arg"},
		WorkingDirectory: "/tmp/second",
	})

	select {
	case ctx := <-showCtx:
		if ctx != app.ctx {
			t.Fatal("showWindow must receive the app context")
		}
	default:
		t.Fatal("showWindow was not called for the second-instance relay")
	}
	if out := buf.String(); !strings.Contains(out, "second instance launch relayed") {
		t.Fatalf("relay must be logged, got: %s", out)
	}
}

// TestHandleSecondInstanceLaunch_BeforeStartupNoop: Wails starts the
// second-instance processor before OnStartup binds the context, so a relay
// can arrive with ctx still nil — it must be a logged no-op, never a panic.
func TestHandleSecondInstanceLaunch_BeforeStartupNoop(t *testing.T) {
	app := NewApp() // ctx deliberately nil
	called := false
	app.windowShowFn = func(context.Context) { called = true }

	log, buf := captureLogger()
	app.logger = log

	app.handleSecondInstanceLaunch(options.SecondInstanceData{})

	if called {
		t.Fatal("showWindow must not run before Startup binds the context")
	}
	if out := buf.String(); !strings.Contains(out, "before Startup") {
		t.Fatalf("the skipped focus must be logged, got: %s", out)
	}
}

// TestAcquireSingleInstanceLock_RefusesSymlinkedLockFile pins the #93 fix:
// the fixed app.lock path is opened no-follow, so a planted symlink fails
// the open — and the caller's documented fail-open contract degrades the
// run to "first instance" without creating the link's target or locking a
// foreign file.
func TestAcquireSingleInstanceLock_RefusesSymlinkedLockFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The lock open is no-follow via safeio.OpenFileNoFollow, which still
		// follows the final symlink on Windows (sp4rk safeio parity
		// limitation); the fail-closed refusal this pins is unix-specific.
		t.Skip("no-follow symlink refusal is unix-specific")
	}
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.Symlink(victim, filepath.Join(dir, "app.lock")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	lock, first, err := AcquireSingleInstanceLock(filepath.Join(dir, "app.lock"))
	if err == nil {
		t.Fatal("expected a fail-closed open error for a symlinked lock file")
	}
	if lock != nil {
		t.Fatal("expected no lock handle on the symlink refusal")
	}
	if !first {
		t.Fatal("expected the documented fail-open degradation to first instance")
	}
	if _, err := os.Lstat(victim); !os.IsNotExist(err) {
		t.Fatalf("symlink target was created: %v", err)
	}
}
