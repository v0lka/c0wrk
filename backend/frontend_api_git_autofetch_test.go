package backend

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
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

// TestStartAutoFetch_TicksAtInterval verifies the ticker dispatches into the
// funnel with the "ticker" trigger and no faster than the configured rate
// (seam-shortened to 40ms for test speed).
func TestStartAutoFetch_TicksAtInterval(t *testing.T) {
	f, _ := newAutoFetchAPI(t.TempDir(), "proj-1", nil)
	const interval = 40 * time.Millisecond
	f.autoFetchIntervalOverride = interval
	rec := &tickRecorder{}
	f.autoFetchTickFn = rec.record

	f.Lifecycle().StartAutoFetch()
	t.Cleanup(func() { f.Lifecycle().Cleanup() })

	pollUntil(t, 5*time.Second, func() bool { return rec.count() >= 1 },
		"ticker did not fire within 5s")

	if got := rec.lastReason(); got != "ticker" {
		t.Errorf("tick trigger = %q, want %q", got, "ticker")
	}

	// The "at most one dispatch per interval" guarantee is checked as an
	// UPPER BOUND on the dispatch count inside a fixed window — never as a
	// per-gap minimum. A per-gap minimum is unsound for a time.Ticker: the
	// ticker holds an ABSOLUTE schedule, so a delivery delayed by the
	// scheduler (routine on a loaded CI runner) is followed by the next
	// already-due slot, and adjacent gaps drop well below the period (a 40ms
	// ticker measured ~24ms gaps under load) even though the long-run rate
	// stays exactly the period. A count ceiling is immune to that: a delay
	// can only push ticks later, i.e. lower the count, so it can never cause
	// a false failure here — while an implementation that genuinely fired
	// faster than the period would blow past the ceiling (2x rate → ~2x the
	// dispatches).
	const window = 20 * interval
	start := rec.count()
	time.Sleep(window)
	dispatches := rec.count() - start

	maxDispatches := int(window/interval) + 2 // + slack for a tick on the window edge
	if dispatches > maxDispatches {
		t.Errorf("dispatches in %v = %d, want <= %d (interval %v)", window, dispatches, maxDispatches, interval)
	}
}

// TestStartAutoFetch_Idempotent verifies a second StartAutoFetch does not
// spawn a second loop: the registered done channel keeps its identity, so
// the desktop startup path can never accidentally double the fetch cadence.
func TestStartAutoFetch_Idempotent(t *testing.T) {
	f, _ := newAutoFetchAPI(t.TempDir(), "proj-1", nil)
	f.autoFetchIntervalOverride = 50 * time.Millisecond
	f.autoFetchTickFn = func(string) {}

	f.Lifecycle().StartAutoFetch()

	f.autoFetchLoopMu.Lock()
	done1 := f.autoFetchLoopDone
	f.autoFetchLoopMu.Unlock()

	f.Lifecycle().StartAutoFetch() // must be a no-op

	f.autoFetchLoopMu.Lock()
	done2 := f.autoFetchLoopDone
	f.autoFetchLoopMu.Unlock()

	if done1 == nil {
		t.Fatal("first StartAutoFetch did not register loop state")
	}
	if done2 != done1 {
		t.Error("second StartAutoFetch replaced the running loop; want idempotent no-op")
	}

	f.Lifecycle().Cleanup()
}

// TestCleanup_StopsAutoFetchLoop proves the loop goroutine terminates after
// Cleanup (the done channel closes) and no further ticks fire afterwards —
// no goroutine leak. The test API has no appCtx, so Cleanup's cancel is the
// ONLY stop signal, exercising exactly the production stop path.
func TestCleanup_StopsAutoFetchLoop(t *testing.T) {
	f, _ := newAutoFetchAPI(t.TempDir(), "proj-1", nil)
	f.autoFetchIntervalOverride = 40 * time.Millisecond
	rec := &tickRecorder{}
	f.autoFetchTickFn = rec.record

	f.Lifecycle().StartAutoFetch()
	pollUntil(t, 5*time.Second, func() bool { return rec.count() >= 1 },
		"ticker did not fire before cleanup")

	f.Lifecycle().Cleanup()

	f.autoFetchLoopMu.Lock()
	done := f.autoFetchLoopDone
	f.autoFetchLoopMu.Unlock()
	if done == nil {
		t.Fatal("loop done channel missing")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("auto-fetch loop goroutine did not exit within 2s after Cleanup")
	}

	// No further ticks after Cleanup.
	before := rec.count()
	time.Sleep(150 * time.Millisecond)
	if after := rec.count(); after != before {
		t.Errorf("ticks after Cleanup: %d → %d, want stable", before, after)
	}
}

// TestAutoFetchLoop_IntervalChangePickedUpAtRuntime verifies a runtime edit
// of git.auto_fetch_interval (under configMu, exactly how UpdateConfig
// mutates f.config) is picked up on the next tick without an app restart:
// switching to "0" stops the periodic fetches mid-run.
func TestAutoFetchLoop_IntervalChangePickedUpAtRuntime(t *testing.T) {
	f, _ := newAutoFetchAPI(t.TempDir(), "proj-1",
		&config.Config{Git: config.GitConfig{AutoFetchInterval: "40ms"}})
	rec := &tickRecorder{}
	f.autoFetchTickFn = rec.record

	f.Lifecycle().StartAutoFetch()
	t.Cleanup(func() { f.Lifecycle().Cleanup() })

	pollUntil(t, 5*time.Second, func() bool { return rec.count() >= 2 },
		"ticker did not fire twice before the config change")

	// Flip the interval to "0" under configMu — the next tick must observe
	// it and park the ticker.
	f.configMu.Lock()
	f.config.Git.AutoFetchInterval = "0"
	f.configMu.Unlock()

	// Give the loop a chance to (wrongly) keep ticking, then assert it has
	// stopped: the count must be stable across a second quiet window.
	time.Sleep(300 * time.Millisecond)
	baseline := rec.count()
	time.Sleep(300 * time.Millisecond)
	if after := rec.count(); after != baseline {
		t.Errorf("ticks continued after interval changed to %q: %d → %d, want stable", "0", baseline, after)
	}
}

// TestAutoFetchLoop_ZeroInterval_NeverTicks verifies the "0" sentinel: the
// ticker never fires from the start, yet the loop goroutine is alive and
// terminates cleanly on Cleanup (no leak in the disabled state).
func TestAutoFetchLoop_ZeroInterval_NeverTicks(t *testing.T) {
	f, _ := newAutoFetchAPI(t.TempDir(), "proj-1",
		&config.Config{Git: config.GitConfig{AutoFetchInterval: "0"}})
	rec := &tickRecorder{}
	f.autoFetchTickFn = rec.record

	f.Lifecycle().StartAutoFetch()
	time.Sleep(250 * time.Millisecond)
	if n := rec.count(); n != 0 {
		t.Errorf("ticker fired %d times with interval \"0\", want 0", n)
	}

	// The parked loop must still stop cleanly on Cleanup.
	f.Lifecycle().Cleanup()
	f.autoFetchLoopMu.Lock()
	done := f.autoFetchLoopDone
	f.autoFetchLoopMu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("parked (interval \"0\") loop did not exit after Cleanup")
	}
}

// TestAutoFetchLoop_ZeroIntervalReArmsWhenRestored pins review [C3-3]: a
// parked loop (git.auto_fetch_interval "0") keeps re-reading the interval at
// the re-check cadence, so restoring a positive value re-arms the periodic
// fetch without an app restart — the loop must not strand itself on
// ctx.Done() until the app exits.
func TestAutoFetchLoop_ZeroIntervalReArmsWhenRestored(t *testing.T) {
	f, _ := newAutoFetchAPI(t.TempDir(), "proj-1",
		&config.Config{Git: config.GitConfig{AutoFetchInterval: "0"}})
	rec := &tickRecorder{}
	f.autoFetchTickFn = rec.record
	f.autoFetchDisabledRecheckOverride = 20 * time.Millisecond

	f.Lifecycle().StartAutoFetch()
	t.Cleanup(func() { f.Lifecycle().Cleanup() })

	// Parked: the re-check cadence must never dispatch a fetch.
	time.Sleep(150 * time.Millisecond)
	if n := rec.count(); n != 0 {
		t.Fatalf("ticker fired %d times while interval was \"0\", want 0", n)
	}

	// Restore a positive interval under configMu. The pointer swap (rather
	// than an in-place field write) keeps this test clean under -race:
	// autoFetchInterval snapshots f.config under RLock and reads the field
	// after releasing it, so a swapped-in immutable copy is always safe to
	// read, while the loop's next re-check observes the new value exactly as
	// a runtime config edit would.
	f.configMu.Lock()
	cfg := *f.config
	cfg.Git.AutoFetchInterval = "40ms"
	f.config = &cfg
	f.configMu.Unlock()

	pollUntil(t, 5*time.Second, func() bool { return rec.count() >= 2 },
		"ticker did not re-arm after the interval changed from \"0\" to \"40ms\"")
}

// TestAutoFetchLoop_NoProjectMode_NoPanic verifies the ticker in CHAT (No
// Project) mode: the loop ticks normally, and the real funnel — invoked
// directly, exactly as the loop's production dispatch would — is a silent
// no-op (its internal gates reject No Project). Nothing panics; the loop
// stops cleanly on Cleanup.
func TestAutoFetchLoop_NoProjectMode_NoPanic(t *testing.T) {
	f, rec := newAutoFetchAPI(t.TempDir(), project.NoProjectID, nil)
	f.autoFetchIntervalOverride = 30 * time.Millisecond
	ticks := &tickRecorder{}
	f.autoFetchTickFn = ticks.record

	f.Lifecycle().StartAutoFetch()
	t.Cleanup(func() { f.Lifecycle().Cleanup() })

	pollUntil(t, 5*time.Second, func() bool { return ticks.count() >= 2 },
		"ticker did not fire in No Project mode")

	// The real funnel must be a silent no-op in CHAT mode (no fetch event,
	// no panic) — belt and braces next to the loop-level ticking check.
	f.autoFetchOnce("ticker")
	if n := rec.count(EventGitStatusChanged); n != 0 {
		t.Errorf("auto-fetch in No Project mode emitted git:status_changed %d times, want 0", n)
	}
}
