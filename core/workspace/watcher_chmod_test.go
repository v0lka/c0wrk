package workspace

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Pure-Chmod suppression tests ─────────────────────────────────────────────
//
// On macOS the kqueue backend reports NOTE_ATTRIB (→ fsnotify Chmod) for
// .git/index every time a git command reads it. The event loop skips
// attribute-only events so a git read never wakes the workspace:tree_changed
// consumers (which would re-run git, whose index reads emit the next Chmod —
// a self-sustaining loop; see watcher.go eventLoop for the full picture).

// TestWatcher_SkipsPureChmodEvents asserts that attribute-only events neither
// trigger onChange nor extend the debounce window: after a Chmod storm a
// subsequent real Write still flushes within the normal debounce interval.
func TestWatcher_SkipsPureChmodEvents(t *testing.T) {
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe.txt")
	chmodFile := filepath.Join(dir, "chmod-only.txt")
	// Both files exist BEFORE the watcher starts, so no Create event can leak
	// into the measurement window below.
	if err := os.WriteFile(probe, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chmodFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var called atomic.Int32
	w, err := NewWatcher(dir, func(paths []string) {
		called.Add(1)
		for _, p := range paths {
			if strings.HasSuffix(p, "chmod-only.txt") {
				t.Errorf("onChange received suppressed pure-Chmod path %q", p)
			}
		}
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Settle: flush whatever setup produced, then reset the counter.
	time.Sleep(defaultDebounce + 150*time.Millisecond)
	called.Store(0)

	// Storm: many attribute-only changes on a file. os.Chmod raises
	// NOTE_ATTRIB on kqueue and IN_ATTRIB on inotify without any content
	// change (verified: the raw event op is exactly CHMOD on macOS).
	for i := 0; i < 5; i++ {
		if err := os.Chmod(chmodFile, 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Past the debounce window: the Chmod storm must have produced no flush.
	time.Sleep(defaultDebounce + 150*time.Millisecond)
	if c := called.Load(); c != 0 {
		t.Errorf("onChange fired %d times for pure-Chmod events, want 0", c)
	}

	// A real write after the storm still flushes promptly.
	if err := os.WriteFile(probe, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(defaultDebounce + 150*time.Millisecond)
	if c := called.Load(); c == 0 {
		t.Error("onChange never fired for a real Write after Chmod suppression")
	}
}

// TestWatcher_ChmodWithWriteStillFires asserts the filter is not over-eager:
// an event that carries Chmod ORed with a write-ish op (how kqueue reports
// real mutations) must reach onChange.
func TestWatcher_ChmodWithWriteStillFires(t *testing.T) {
	dir := t.TempDir()

	var fired atomic.Int32
	w, err := NewWatcher(dir, func(_ []string) { fired.Add(1) })
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	// os.WriteFile on an existing file: on kqueue this surfaces as
	// Write|Chmod (NOTE_WRITE|NOTE_ATTRIB); on inotify as Write. Both must
	// pass the filter.
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(defaultDebounce + 150*time.Millisecond)
	if c := fired.Load(); c == 0 {
		t.Error("onChange did not fire for a real content write")
	}
}

// TestWatcher_GitReadDoesNotTriggerOnChange is the end-to-end regression test
// for the self-sustaining loop: with GIT_OPTIONAL_LOCKS=0 pinned by
// gitCmdInRepoScanned, running the same read-only git commands the frontend
// issues on workspace:tree_changed must not itself trigger onChange.
//
// Diagnosis aid: the watcher is wired to a Debug-level logger so every raw
// fsnotify event (op + path) lands in the test's output on failure — with
// FSNOTIFY_DEBUG=1 in the environment the fsnotify backend additionally
// prints the RAW ReadDirectoryChangesW/inotify/kqueue actions. The failure
// message also lists the paths that reached onChange.
func TestWatcher_GitReadDoesNotTriggerOnChange(t *testing.T) {
	dir := t.TempDir()
	runGitPlain(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitPlain(t, dir, "add", "a.txt")
	runGitPlain(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init")

	var fired atomic.Int32
	var firedMu sync.Mutex
	var firedPaths []string
	debugLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	w, err := NewWatcher(dir, func(paths []string) {
		firedMu.Lock()
		firedPaths = append(firedPaths, paths...)
		firedMu.Unlock()
		fired.Add(1)
	}, debugLogger)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Drain watcher startup + repo creation noise.
	time.Sleep(defaultDebounce + 200*time.Millisecond)
	fired.Store(0)

	// The exact read commands the frontend refresh paths run: git status,
	// git diff HEAD, git ls-files --others, git diff --numstat HEAD.
	for range 3 {
		if _, err := GitStatus(context.Background(), dir); err != nil {
			t.Fatalf("GitStatus: %v", err)
		}
		if _, err := BuildReviewDiff(context.Background(), dir, 5); err != nil {
			t.Fatalf("BuildReviewDiff: %v", err)
		}
	}
	time.Sleep(defaultDebounce + 200*time.Millisecond)

	if c := fired.Load(); c != 0 {
		firedMu.Lock()
		paths := append([]string(nil), firedPaths...)
		firedMu.Unlock()
		t.Errorf("onChange fired %d times for read-only git commands, want 0 (loop regression); paths: %q", c, paths)
	}

	// Control: a real working-tree edit must still fire.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(defaultDebounce + 200*time.Millisecond)
	if c := fired.Load(); c == 0 {
		t.Error("onChange did not fire for a real working-tree edit")
	}
}

// TestWatcher_WriteEchoClassification is the deterministic unit behind the
// directory-Write suppression in watcher.go's event loop: a Write-only event
// whose path is a directory must classify as a suppressible echo whether or
// not the directory is watched (the NTFS echo for .git/logs arrives through
// the .git watch and is NOT in the watch list), while file paths and missing
// paths must not. The end-to-end timing — the echo landing inside a later
// debounce window — is what TestWatcher_GitReadDoesNotTriggerOnChange
// exercises; it only manifests when NTFS defers the directory-mtime update
// far enough (observed on the Windows CI runner).
func TestWatcher_WriteEchoClassification(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	file := filepath.Join(dir, "f.txt")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(dir, func([]string) {})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"watched root directory", dir, true},
		{"unwatched subdirectory", sub, true},
		{"regular file", file, false},
		{"missing path", filepath.Join(dir, "gone.txt"), false},
		{"empty path", "", false},
	}
	for _, tt := range tests {
		if got := w.pathIsDirectory(tt.path); got != tt.want {
			t.Errorf("pathIsDirectory(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestGitCmdInRepoPinsOptionalLocksOff asserts the env pin on the hardened
// path (and documents the raw/trusted path's nil-Env contract).
func TestGitCmdInRepoPinsOptionalLocksOff(t *testing.T) {
	dir := t.TempDir()
	runGitPlain(t, dir, "init", "-q")

	cmd, err := GitCmdInRepo(context.Background(), dir, "status", "--porcelain")
	if err != nil {
		t.Fatalf("GitCmdInRepo: %v", err)
	}
	if cmd.Env == nil {
		t.Fatal("hardened git cmd must carry an explicit Env")
	}
	var pins int
	for _, kv := range cmd.Env {
		if kv == "GIT_OPTIONAL_LOCKS=0" {
			pins++
		}
	}
	if pins != 1 {
		t.Errorf("GIT_OPTIONAL_LOCKS=0 pinned %d times in cmd.Env, want exactly 1", pins)
	}
}

// TestGitCmdInRepoOptionalLocksEffective proves the pin behaves: running
// `git status` through GitCmdInRepo must not rewrite .git/index (the rewrite
// is what used to close the watcher loop; with locks off it cannot happen).
func TestGitCmdInRepoOptionalLocksEffective(t *testing.T) {
	dir := t.TempDir()
	runGitPlain(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitPlain(t, dir, "add", "a.txt")
	runGitPlain(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init")

	indexStat := func() (int64, int64) {
		fi, err := os.Stat(filepath.Join(dir, ".git", "index"))
		if err != nil {
			t.Fatalf("stat index: %v", err)
		}
		return fi.ModTime().UnixNano(), fi.Size()
	}

	// Make the stat-cache stale so a locking `git status` would WANT to
	// rewrite the index (the pre-fix behaviour).
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(dir, "a.txt"), future, future); err != nil {
		t.Fatal(err)
	}
	m1, s1 := indexStat()

	cmd, err := GitCmdInRepo(context.Background(), dir, "status", "--porcelain")
	if err != nil {
		t.Fatalf("GitCmdInRepo: %v", err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git status: %v (%s)", err, out)
	}
	m2, s2 := indexStat()

	if m1 != m2 || s1 != s2 {
		t.Errorf("git status rewrote .git/index through GitCmdInRepo (mtime %d→%d, size %d→%d); GIT_OPTIONAL_LOCKS pin is not effective", m1, m2, s1, s2)
	}
}

// runGitPlain runs git with a minimal environment, without the hardened
// chokepoint (test setup only — the code under test must go through
// GitCmdInRepo so its pins are exercised).
func runGitPlain(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd, err := GitCmdInRepo(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	cmd.Env = append(cmd.Env,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out.String())
	}
}
