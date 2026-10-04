package backend

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
)

// gitEventRecorder captures event names emitted through the FrontendAPI
// emitEvent seam so auto-fetch tests can observe whether a fetch ran
// (git:status_changed is emitted by runSerializedRemoteOp on success).
type gitEventRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *gitEventRecorder) record(event string, _ ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *gitEventRecorder) count(event string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e == event {
			n++
		}
	}
	return n
}

// newAutoFetchAPI builds a FrontendAPI wired for auto-fetch tests: the
// given active project, an optional config, and an emitEvent recorder.
func newAutoFetchAPI(projectPath, projectID string, cfg *config.Config) (*FrontendAPI, *gitEventRecorder) {
	rec := &gitEventRecorder{}
	return &FrontendAPI{
		config:            cfg,
		activeProjectPath: projectPath,
		activeProjectID:   projectID,
		emitEvent:         rec.record,
	}, rec
}

// setupAutoFetchRepo creates a local repository with a bare remote that
// already carries its initial commit (origin/<branch> == HEAD after the
// setup push), so a fetch is a cheap no-op network round trip.
func setupAutoFetchRepo(t *testing.T) (localDir string) {
	t.Helper()
	remoteDir := t.TempDir()
	gitOut(t, remoteDir, "init", "--bare")

	localDir = t.TempDir()
	gitInit(t, localDir)
	commitFile(t, localDir, "a.txt", "a\n")
	gitOut(t, localDir, "remote", "add", "origin", remoteDir)
	branch := gitDefaultBranch(t, localDir)
	gitOut(t, localDir, "push", "-u", "origin", branch)
	return localDir
}

// advanceRemote pushes one extra commit to the bare remote (via a clone) so
// the local repository falls behind and a subsequent fetch is observable.
func advanceRemote(t *testing.T, localDir string) {
	t.Helper()
	remoteURL := gitOut(t, localDir, "remote", "get-url", "origin")
	cloneParent := t.TempDir()
	cloneDir := cloneParent + "/clone"
	gitOut(t, cloneParent, "clone", remoteURL, cloneDir)
	runGit(t, cloneDir, "config", "user.email", "test@test.com")
	runGit(t, cloneDir, "config", "user.name", "Test")
	commitFile(t, cloneDir, "b.txt", "b\n")
	branch := gitDefaultBranch(t, cloneDir)
	gitOut(t, cloneDir, "push", "origin", branch)
}

// pollUntil waits for cond to become true, polling every 10ms up to the
// timeout. It fails the test on timeout.
func pollUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// TestRequestGitRemoteRefresh_NonBlocking verifies the RPC contract: the
// call itself must return immediately (the fetch runs in a goroutine) and
// the background fetch must eventually complete — observable via the
// git:status_changed event and the branch falling behind the remote.
func TestRequestGitRemoteRefresh_NonBlocking(t *testing.T) {
	localDir := setupAutoFetchRepo(t)
	advanceRemote(t, localDir)

	f, rec := newAutoFetchAPI(localDir, "proj-1", &config.Config{})

	start := time.Now()
	f.RequestGitRemoteRefresh()
	elapsed := time.Since(start)

	// The RPC must not wait for the network fetch. A direct fetch of a tiny
	// local remote completes in tens of milliseconds; the bound is generous
	// (2s) so slow CI machines cannot flake it — the assertion's purpose is
	// to catch a synchronous implementation, not to measure latency.
	if elapsed > 2*time.Second {
		t.Errorf("RequestGitRemoteRefresh blocked for %v; want immediate return", elapsed)
	}

	// The goroutine-side fetch must complete and emit git:status_changed.
	pollUntil(t, 10*time.Second, func() bool {
		return rec.count(EventGitStatusChanged) == 1
	}, "background fetch did not emit git:status_changed within 10s")

	info, err := f.GetCurrentBranch()
	if err != nil {
		t.Fatalf("GetCurrentBranch after auto-fetch: %v", err)
	}
	if info.Behind != 1 {
		t.Errorf("after auto-fetch: behind = %d, want 1", info.Behind)
	}
}

// TestAutoFetchOnce_SilentSkipGates verifies every server-side gate skips
// the fetch silently (no git:status_changed event, no error surfaced —
// autoFetchOnce returns nothing by design).
func TestAutoFetchOnce_SilentSkipGates(t *testing.T) {
	disabled := false
	cfgDisabled := &config.Config{}
	config.ApplyDefaults(cfgDisabled)
	cfgDisabled.Git.AutoFetch = &disabled

	tests := []struct {
		name       string
		projectID  string
		projectDir func(t *testing.T) string
		cfg        *config.Config
	}{
		{
			name:       "auto_fetch disabled in config",
			projectID:  "proj-1",
			projectDir: setupAutoFetchRepo,
			cfg:        cfgDisabled,
		},
		{
			name:       "no active project",
			projectID:  "",
			projectDir: func(t *testing.T) string { return t.TempDir() },
			cfg:        &config.Config{},
		},
		{
			name:       "No Project (CHAT mode)",
			projectID:  project.NoProjectID,
			projectDir: func(t *testing.T) string { return t.TempDir() },
			cfg:        &config.Config{},
		},
		{
			name:      "workspace is not a git repository",
			projectID: "proj-1",
			projectDir: func(t *testing.T) string {
				dir := t.TempDir()
				writeFileT(t, dir, "plain.txt", "not a repo\n")
				return dir
			},
			cfg: &config.Config{},
		},
		{
			name:      "git repository without a remote",
			projectID: "proj-1",
			projectDir: func(t *testing.T) string {
				dir := t.TempDir()
				gitInit(t, dir)
				commitFile(t, dir, "a.txt", "a\n")
				return dir
			},
			cfg: &config.Config{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, rec := newAutoFetchAPI(tt.projectDir(t), tt.projectID, tt.cfg)
			f.autoFetchOnce("focus") // must silently skip
			if n := rec.count(EventGitStatusChanged); n != 0 {
				t.Errorf("gated auto-fetch emitted git:status_changed %d times, want 0", n)
			}
		})
	}
}

// TestAutoFetchOnce_MinInterval verifies the shared 60s gate: an immediate
// second trigger must not produce a second fetch, even though the first one
// succeeded.
func TestAutoFetchOnce_MinInterval(t *testing.T) {
	localDir := setupAutoFetchRepo(t)

	f, rec := newAutoFetchAPI(localDir, "proj-1", &config.Config{})

	f.autoFetchOnce("focus")
	if n := rec.count(EventGitStatusChanged); n != 1 {
		t.Fatalf("first auto-fetch: git:status_changed count = %d, want 1", n)
	}

	f.autoFetchOnce("focus") // within autoFetchMinInterval → silent skip
	if n := rec.count(EventGitStatusChanged); n != 1 {
		t.Errorf("second auto-fetch within min interval: git:status_changed count = %d, want 1", n)
	}
}

// TestAutoFetchOnce_MinIntervalSharedAcrossTriggers verifies the timestamp
// is shared across triggers (a "ticker" fetch reserves the slot; a "focus"
// fetch right after is skipped) — the gate is per app instance, not per
// trigger source.
func TestAutoFetchOnce_MinIntervalSharedAcrossTriggers(t *testing.T) {
	localDir := setupAutoFetchRepo(t)

	f, rec := newAutoFetchAPI(localDir, "proj-1", &config.Config{})

	f.autoFetchOnce("ticker")
	f.autoFetchOnce("focus")
	if n := rec.count(EventGitStatusChanged); n != 1 {
		t.Errorf("cross-trigger fetches within min interval: git:status_changed count = %d, want 1", n)
	}
}

// TestAutoFetchOnce_AfterMinInterval verifies the gate reopens once the
// interval has elapsed.
func TestAutoFetchOnce_AfterMinInterval(t *testing.T) {
	localDir := setupAutoFetchRepo(t)

	f, rec := newAutoFetchAPI(localDir, "proj-1", &config.Config{})

	f.autoFetchOnce("focus")

	// Simulate the interval elapsing without waiting a real minute.
	f.autoFetchMu.Lock()
	f.lastAutoFetchAt = time.Now().Add(-autoFetchMinInterval - time.Second)
	f.autoFetchMu.Unlock()

	f.autoFetchOnce("focus")
	if n := rec.count(EventGitStatusChanged); n != 2 {
		t.Errorf("auto-fetch after min interval: git:status_changed count = %d, want 2", n)
	}
}

// writeFileT is a tiny test helper creating a file with content.
func writeFileT(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// ─── Periodic auto-fetch ticker tests (git.auto_fetch_interval) ──────────

// tickRecorder captures ticker-loop dispatches — the trigger name and the
// number of ticks — so tests can observe that ticks happen, how many come in
// a window, and with which trigger, without touching git or the network.
type tickRecorder struct {
	mu     sync.Mutex
	n      int
	reason string
}

func (r *tickRecorder) record(trigger string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	r.reason = trigger
}

func (r *tickRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

func (r *tickRecorder) lastReason() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reason
}

// TestAutoFetchInterval_Resolution verifies the interval resolution order:
// the test seam override wins, a parseable config value is used as-is, an
// empty or unparseable value falls back to the 2m default, and the "0"
// sentinel (or any non-positive value) disables only the ticker.
func TestAutoFetchInterval_Resolution(t *testing.T) {
	tests := []struct {
		name     string
		override time.Duration
		cfg      *config.Config
		want     time.Duration
	}{
		{
			name:     "seam override wins over config",
			override: 5 * time.Second,
			cfg:      &config.Config{Git: config.GitConfig{AutoFetchInterval: "1m"}},
			want:     5 * time.Second,
		},
		{
			name: "config value parsed",
			cfg:  &config.Config{Git: config.GitConfig{AutoFetchInterval: "30s"}},
			want: 30 * time.Second,
		},
		{
			name: "empty config value falls back to default",
			cfg:  &config.Config{Git: config.GitConfig{AutoFetchInterval: ""}},
			want: defaultAutoFetchInterval,
		},
		{
			name: "unparseable value falls back to default",
			cfg:  &config.Config{Git: config.GitConfig{AutoFetchInterval: "garbage"}},
			want: defaultAutoFetchInterval,
		},
		{
			name: "zero sentinel disables the ticker",
			cfg:  &config.Config{Git: config.GitConfig{AutoFetchInterval: "0"}},
			want: 0,
		},
		{
			name: "negative value disables the ticker",
			cfg:  &config.Config{Git: config.GitConfig{AutoFetchInterval: "-1m"}},
			want: -time.Minute,
		},
		{
			name: "nil config falls back to default",
			cfg:  nil,
			want: defaultAutoFetchInterval,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _ := newAutoFetchAPI(t.TempDir(), "proj-1", tt.cfg)
			f.autoFetchIntervalOverride = tt.override
			if got := f.autoFetchInterval(); got != tt.want {
				t.Errorf("autoFetchInterval() = %v, want %v", got, tt.want)
			}
		})
	}
}

// startVirtualAutoFetch creates only in-memory state; the real git funnel is
// replaced by the existing tick seam. All callers run inside a synctest bubble.
func startVirtualAutoFetch(t *testing.T, raw string) (*FrontendAPI, *tickRecorder) {
	t.Helper()
	f, _ := newAutoFetchAPI("", "proj-1", &config.Config{Git: config.GitConfig{AutoFetchInterval: raw}})
	rec := &tickRecorder{}
	f.autoFetchTickFn = rec.record
	f.Lifecycle().StartAutoFetch()
	synctest.Wait() // The loop has constructed its ticker and parked.
	return f, rec
}

func joinVirtualAutoFetch(t *testing.T, f *FrontendAPI) {
	t.Helper()
	f.Lifecycle().Cleanup()
	synctest.Wait()
	select {
	case <-f.autoFetchLoopDone:
	default:
		t.Fatal("auto-fetch loop did not join after Cleanup")
	}
}

func setVirtualAutoFetchInterval(f *FrontendAPI, raw string) {
	f.configMu.Lock()
	cfg := *f.config
	cfg.Git.AutoFetchInterval = raw
	f.config = &cfg // Immutable snapshot, matching runtime config updates.
	f.configMu.Unlock()
}

func TestStartAutoFetch_TicksAtInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, rec := startVirtualAutoFetch(t, "40ms")
		defer joinVirtualAutoFetch(t, f)
		time.Sleep(40*time.Millisecond - time.Nanosecond)
		synctest.Wait()
		if got := rec.count(); got != 0 {
			t.Fatalf("ticks before boundary = %d, want 0", got)
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if got := rec.count(); got != 1 {
			t.Fatalf("ticks at boundary = %d, want 1", got)
		}
		if got := rec.lastReason(); got != autoFetchTriggerTicker {
			t.Errorf("reason = %q, want %q", got, autoFetchTriggerTicker)
		}
		for want := 2; want <= 21; want++ {
			time.Sleep(40 * time.Millisecond)
			synctest.Wait()
			if got := rec.count(); got != want {
				t.Fatalf("ticks at boundary %d = %d, want %d", want, got, want)
			}
		}
	})
}

func TestStartAutoFetch_Idempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, rec := startVirtualAutoFetch(t, "40ms")
		defer joinVirtualAutoFetch(t, f)
		done := f.autoFetchLoopDone
		f.Lifecycle().StartAutoFetch()
		synctest.Wait()
		if f.autoFetchLoopDone != done {
			t.Fatal("second start replaced running loop")
		}
		time.Sleep(40 * time.Millisecond)
		synctest.Wait()
		if got := rec.count(); got != 1 {
			t.Errorf("ticks after second start = %d, want 1", got)
		}
	})
}

func TestCleanup_StopsAutoFetchLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, rec := startVirtualAutoFetch(t, "40ms")
		defer joinVirtualAutoFetch(t, f)
		time.Sleep(40 * time.Millisecond)
		synctest.Wait()
		if got := rec.count(); got != 1 {
			t.Fatal("first tick missing")
		}
		joinVirtualAutoFetch(t, f)
		time.Sleep(time.Hour)
		synctest.Wait()
		if got := rec.count(); got != 1 {
			t.Errorf("ticks after loop join = %d, want 1", got)
		}
	})
}

func TestAutoFetchLoop_IntervalChangePickedUpAtRuntime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, rec := startVirtualAutoFetch(t, "40ms")
		defer joinVirtualAutoFetch(t, f)
		time.Sleep(80 * time.Millisecond)
		synctest.Wait()
		if got := rec.count(); got != 2 {
			t.Fatalf("ticks before edit = %d, want 2", got)
		}
		setVirtualAutoFetchInterval(f, "0")
		time.Sleep(40 * time.Millisecond) // Old tick observes the edit, without fetching.
		synctest.Wait()
		time.Sleep(2 * autoFetchDisabledRecheck)
		synctest.Wait()
		if got := rec.count(); got != 2 {
			t.Errorf("ticks after parking = %d, want 2", got)
		}
	})
}

func TestAutoFetchLoop_ZeroInterval_NeverTicks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, rec := startVirtualAutoFetch(t, "0")
		defer joinVirtualAutoFetch(t, f)
		time.Sleep(3 * autoFetchDisabledRecheck)
		synctest.Wait()
		if got := rec.count(); got != 0 {
			t.Errorf("ticks while disabled = %d, want 0", got)
		}
		joinVirtualAutoFetch(t, f)
	})
}

func TestAutoFetchLoop_ZeroIntervalReArmsWhenRestored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, rec := startVirtualAutoFetch(t, "0")
		defer joinVirtualAutoFetch(t, f)
		setVirtualAutoFetchInterval(f, "40ms")
		time.Sleep(autoFetchDisabledRecheck - time.Nanosecond)
		synctest.Wait()
		if got := rec.count(); got != 0 {
			t.Fatal("parked loop dispatched before recheck")
		}
		time.Sleep(time.Nanosecond) // Recheck re-arms, but is not itself a fetch.
		synctest.Wait()
		if got := rec.count(); got != 0 {
			t.Fatal("recheck dispatched a fetch")
		}
		time.Sleep(40*time.Millisecond - time.Nanosecond)
		synctest.Wait()
		if got := rec.count(); got != 0 {
			t.Fatal("restored ticker fired early")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if got := rec.count(); got != 1 {
			t.Errorf("ticks after restored boundary = %d, want 1", got)
		}
	})
}

func TestAutoFetchLoop_NoProjectMode_NoPanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, ticks := startVirtualAutoFetch(t, "40ms")
		defer joinVirtualAutoFetch(t, f)
		f.activeProjectID = project.NoProjectID
		events := &gitEventRecorder{}
		f.emitEvent = events.record
		// Keep enabled: exercise the No Project gate, not the master switch.
		time.Sleep(80 * time.Millisecond)
		synctest.Wait()
		if got := ticks.count(); got != 2 {
			t.Fatalf("No Project ticks = %d, want 2", got)
		}
		f.autoFetchOnce(autoFetchTriggerTicker)
		if got := events.count(EventGitStatusChanged); got != 0 {
			t.Errorf("No Project git events = %d, want 0", got)
		}
	})
}
