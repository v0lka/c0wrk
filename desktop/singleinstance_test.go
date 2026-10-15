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

// assertSingleInstanceIDShape fails the test unless id is exactly
// SingleInstanceLockID + "_" + 12 lowercase hex characters — the shape every
// platform derivation (D-Bus bus name, darwin lockfile, Windows mutex/window
// class) is documented against. The literal 12 deliberately pins the shape
// rather than echoing singleInstanceLockIDSuffixHexLen.
func assertSingleInstanceIDShape(t *testing.T, id string) {
	t.Helper()
	suffix, found := strings.CutPrefix(id, SingleInstanceLockID+"_")
	if !found {
		t.Fatalf("id %q missing the %q_ prefix", id, SingleInstanceLockID)
	}
	if len(suffix) != 12 {
		t.Fatalf("id %q suffix = %q, want 12 characters", id, suffix)
	}
	for i, r := range suffix {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			t.Fatalf("id %q suffix character %d = %q is not [0-9a-f]", id, i, r)
		}
	}
}

// TestSingleInstanceOptions_Wiring: the Wails option carries the derived
// agent-dir identity — the SingleInstanceLockID prefix plus a well-formed
// hex suffix, never the bare constant — threaded from the caller's agentDir,
// and relays second launches to the app handler.
func TestSingleInstanceOptions_Wiring(t *testing.T) {
	app := NewApp()
	agentDir := t.TempDir()
	opts := SingleInstanceOptions(app, agentDir)
	if opts == nil {
		t.Fatal("SingleInstanceOptions must return a non-nil option")
	}
	assertSingleInstanceIDShape(t, opts.UniqueId)
	if want := SingleInstanceLockIDFor(agentDir); opts.UniqueId != want {
		t.Fatalf("UniqueId = %q, want %q", opts.UniqueId, want)
	}
	if opts.OnSecondInstanceLaunch == nil {
		t.Fatal("OnSecondInstanceLaunch must be wired")
	}
}

// TestSingleInstanceLockIDFor_Deterministic pins the contract the lock
// depends on: the same agent dir yields the same id on every call, and the
// derived id has the prefix+hex-suffix shape.
func TestSingleInstanceLockIDFor_Deterministic(t *testing.T) {
	dir := t.TempDir()
	first := SingleInstanceLockIDFor(dir)
	if first != SingleInstanceLockIDFor(dir) {
		t.Fatalf("id not deterministic: %q vs %q", first, SingleInstanceLockIDFor(dir))
	}
	assertSingleInstanceIDShape(t, first)
	// A trailing separator and an unclean relative spelling of the same
	// absolute directory canonicalize to the identical id.
	if cleaned := SingleInstanceLockIDFor(dir + string(filepath.Separator)); cleaned != first {
		t.Fatalf("trailing separator changed the id: %q vs %q", cleaned, first)
	}
}

// TestSingleInstanceLockIDFor_DistinctDirs: different agent directories must
// never share a lock identity — the lock would wrongly merge two independent
// profiles.
func TestSingleInstanceLockIDFor_DistinctDirs(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "c0wrk-a")
	b := filepath.Join(root, "c0wrk-b")
	for _, dir := range []string{a, b} {
		if err := os.Mkdir(dir, 0o750); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
	}
	if idA, idB := SingleInstanceLockIDFor(a), SingleInstanceLockIDFor(b); idA == idB {
		t.Fatalf("distinct dirs share an id: %q", idA)
	}
	if idRoot, idA := SingleInstanceLockIDFor(root), SingleInstanceLockIDFor(a); idRoot == idA {
		t.Fatalf("parent and child share an id: %q", idA)
	}
}

// TestSingleInstanceLockIDFor_SuffixShape: the derived id must be exactly
// the SingleInstanceLockID prefix plus 12 lowercase hex characters — the
// shape every platform derivation (bus name, lockfile, mutex/window class)
// is documented against.
func TestSingleInstanceLockIDFor_SuffixShape(t *testing.T) {
	assertSingleInstanceIDShape(t, SingleInstanceLockIDFor(t.TempDir()))
}

// TestSingleInstanceLockIDFor_UnresolvableDirFailsSoft pins the fail-soft
// canonicalization: a not-yet-created directory (the pre-Acquire
// first-launch case) still yields a well-formed id instead of an error or an
// empty identity. Note the ordering invariant keeps this form from ever
// reaching production (AcquireSingleInstanceLock creates the dir first);
// this only pins that the degradation cannot panic or corrupt the id.
func TestSingleInstanceLockIDFor_UnresolvableDirFailsSoft(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	assertSingleInstanceIDShape(t, SingleInstanceLockIDFor(missing))
}

// TestSingleInstanceLockIDFor_UncleanPathsSameID pins the canonicalization
// contract with every non-canonical spelling of one agent directory: a
// relative path, a path with redundant separators, and a symlink to the same
// directory must all derive the identical id — otherwise two spellings of
// one profile would run as two "first" instances. Unix-guarded after
// TestAcquireSingleInstanceLock_RefusesSymlinkedLockFile: the symlink
// spelling needs symlink-creation rights windows only grants to elevated or
// developer-mode processes, and a skip beats asserting a degraded case.
func TestSingleInstanceLockIDFor_UncleanPathsSameID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not reliably available on windows")
	}
	dir := t.TempDir()
	want := SingleInstanceLockIDFor(dir)

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	// The relative spelling is resolved from dir's parent directory.
	t.Chdir(filepath.Dir(dir))
	for _, tc := range []struct {
		name string
		path string
	}{
		{"relative", filepath.Base(dir)},
		{
			name: "redundant separators",
			path: strings.Repeat(string(filepath.Separator), 3) +
				dir + strings.Repeat(string(filepath.Separator), 2),
		},
		{"symlink", link},
	} {
		if got := SingleInstanceLockIDFor(tc.path); got != want {
			t.Fatalf("%s spelling %q derived %q, want the canonical %q", tc.name, tc.path, got, want)
		}
	}
}

// dbusNameElementValid reports whether s is a valid single element of a
// D-Bus well-known name: non-empty, [A-Za-z0-9_] only, and never led by a
// digit (D-Bus specification, "valid names").
func dbusNameElementValid(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		alnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
		if !alnum {
			return false
		}
		if i == 0 && r >= '0' && r <= '9' {
			return false
		}
	}
	return true
}

// TestSingleInstanceLockIDFor_DBusValidAfterWailsMapping replicates the
// Wails v2.16 Linux derivation (internal/frontend/desktop/linux/
// single_instance.go: underscore the id, then "org." + id + ".SingleInstance"
// as the bus name and "/org/" + id + "/SingleInstance" as the object path)
// and asserts the result satisfies the D-Bus name/path grammar. This is the
// one platform where a malformed id fails OPEN-but-silently (every error is
// swallowed), so an invalid name would permanently kill the relay.
func TestSingleInstanceLockIDFor_DBusValidAfterWailsMapping(t *testing.T) {
	id := SingleInstanceLockIDFor(t.TempDir())

	mapped := strings.ReplaceAll(strings.ReplaceAll(id, "-", "_"), ".", "_")
	mapped = "wails_app_" + mapped
	busName := "org." + mapped + ".SingleInstance"
	objectPath := "/org/" + mapped + "/SingleInstance"

	elements := strings.Split(busName, ".")
	if len(elements) < 2 {
		t.Fatalf("bus name %q must have at least two elements", busName)
	}
	for _, el := range elements {
		if !dbusNameElementValid(el) {
			t.Fatalf("bus name %q has invalid element %q", busName, el)
		}
	}
	if len(busName) > 255 {
		t.Fatalf("bus name %q exceeds the 255-byte D-Bus limit", busName)
	}
	if !strings.HasPrefix(objectPath, "/") || strings.HasSuffix(objectPath, "/") {
		t.Fatalf("object path %q must start and not end with '/'", objectPath)
	}
	for _, el := range strings.Split(strings.TrimPrefix(objectPath, "/"), "/") {
		if el == "" {
			t.Fatalf("object path %q has an empty element", objectPath)
		}
		for _, r := range el {
			alnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
			if !alnum {
				t.Fatalf("object path %q has invalid character %q in %q", objectPath, r, el)
			}
		}
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
