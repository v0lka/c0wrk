package backend

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/gittrust"
	"github.com/v0lka/c0wrk/core/workspace"
)

// Switch-trigger and runner-level tests for the auto-fetch funnel
// (frontend_api_git_autofetch.go). The gate / min-interval / ticker suites
// live in frontend_api_git_autofetch_test.go; this file covers what only
// step "switch" owns: the SwitchProject hook contract, the fetch runner's
// env pinning (trusted and untrusted paths), the busy-remoteOpMu instant
// skip, and the quiet network-failure path. Helpers (newAutoFetchAPI,
// setupAutoFetchRepo, advanceRemote, pollUntil) are shared with
// frontend_api_git_autofetch_test.go.

// statusPayloadRecorder captures git:status_changed payloads (and a total
// event count) so tests can assert BOTH how many status events fired and
// which repo path each carried.
type statusPayloadRecorder struct {
	mu        sync.Mutex
	payloads  []string
	anyEvents int
}

func (r *statusPayloadRecorder) record(event string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.anyEvents++
	if event == EventGitStatusChanged && len(args) >= 1 {
		if p, ok := args[0].(string); ok {
			r.payloads = append(r.payloads, p)
		}
	}
}

func (r *statusPayloadRecorder) statusPayloads() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.payloads...)
}

func (r *statusPayloadRecorder) totalEvents() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.anyEvents
}

// autoFetchDisabledConfig returns a config with git.auto_fetch explicitly
// switched off (the only way to disable the funnel — see autoFetchEnabled).
func autoFetchDisabledConfig() *config.Config {
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	disabled := false
	cfg.Git.AutoFetch = &disabled
	return cfg
}

// resetAutoFetchStamp zeroes lastAutoFetchAt under autoFetchMu so a later
// autoFetchOnce is not masked by the shared min-interval gate.
func resetAutoFetchStamp(f *FrontendAPI) {
	f.autoFetchMu.Lock()
	f.lastAutoFetchAt = time.Time{}
	f.autoFetchMu.Unlock()
}

// TestAutoFetchOnce_Success_EmitsExactlyOneStatusEventWithRepoPath verifies
// the success contract: a real fetch emits git:status_changed exactly ONCE
// with the repository path as payload, and the fetched remote state becomes
// visible through GetCurrentBranch's ahead/behind.
func TestAutoFetchOnce_Success_EmitsExactlyOneStatusEventWithRepoPath(t *testing.T) {
	localDir := setupAutoFetchRepo(t)
	advanceRemote(t, localDir) // remote is now 1 ahead of local

	f, _ := newAutoFetchAPI(localDir, "proj-1", &config.Config{})
	rec := &statusPayloadRecorder{}
	f.emitEvent = rec.record

	// Sanity: before the fetch the local repo does not know it is behind.
	if info, err := f.GetCurrentBranch(); err != nil {
		t.Fatalf("GetCurrentBranch before fetch: %v", err)
	} else if info.Behind != 0 {
		t.Fatalf("before fetch: behind = %d, want 0", info.Behind)
	}

	f.autoFetchOnce("switch")

	payloads := rec.statusPayloads()
	if len(payloads) != 1 {
		t.Fatalf("git:status_changed emitted %d times, want exactly 1 (payloads: %v)", len(payloads), payloads)
	}
	if payloads[0] != localDir {
		t.Errorf("git:status_changed payload = %q, want %q", payloads[0], localDir)
	}

	info, err := f.GetCurrentBranch()
	if err != nil {
		t.Fatalf("GetCurrentBranch after fetch: %v", err)
	}
	if info.Behind != 1 || info.Ahead != 0 {
		t.Errorf("after fetch: ahead/behind = %d/%d, want 0/1", info.Ahead, info.Behind)
	}
}

// TestAutoFetchOnce_DisabledByConfig_StartsNoGitProcess pins that a disabled
// config bails out at the FIRST gate: the isGitRepo cache stays untouched
// (no `git rev-parse` was ever spawned) and no attempt was stamped.
func TestAutoFetchOnce_DisabledByConfig_StartsNoGitProcess(t *testing.T) {
	localDir := setupAutoFetchRepo(t)

	f, rec := newAutoFetchAPI(localDir, "proj-1", autoFetchDisabledConfig())

	f.autoFetchOnce("switch")

	if n := rec.count(EventGitStatusChanged); n != 0 {
		t.Errorf("disabled auto-fetch emitted git:status_changed %d times, want 0", n)
	}
	if !f.lastAutoFetchAt.IsZero() {
		t.Error("disabled auto-fetch stamped lastAutoFetchAt; want no attempt recorded")
	}
	f.gitRepoCacheMu.Lock()
	cached := len(f.gitRepoCache)
	f.gitRepoCacheMu.Unlock()
	if cached != 0 {
		t.Errorf("disabled auto-fetch touched the git-repo cache (%d entries); want no git process at all", cached)
	}
}

// TestAutoFetchOnce_ManualRemoteOpInFlight_SkipsInstantly verifies the
// remoteOpMu TryLock contract: while a manual pull/push/fetch holds the
// network-operation lock, the automatic fetch skips IMMEDIATELY (it must
// not queue behind the user's operation), stays silent, and the skip is
// attributable to the lock alone — once the lock frees, the same state
// fetches fine.
func TestAutoFetchOnce_ManualRemoteOpInFlight_SkipsInstantly(t *testing.T) {
	localDir := setupAutoFetchRepo(t)
	advanceRemote(t, localDir)

	f, _ := newAutoFetchAPI(localDir, "proj-1", &config.Config{})
	rec := &statusPayloadRecorder{}
	f.emitEvent = rec.record

	// Simulate a manual remote operation holding the lock.
	f.remoteOpMu.Lock()

	start := time.Now()
	f.autoFetchOnce("switch")
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("auto-fetch with busy remoteOpMu took %v; want an instant TryLock skip", elapsed)
	}
	if n := len(rec.statusPayloads()); n != 0 {
		t.Errorf("auto-fetch under a busy remoteOpMu emitted git:status_changed %d times, want 0", n)
	}
	// The fetch never ran: the local repo still does not know it is behind.
	if info, err := f.GetCurrentBranch(); err != nil {
		t.Fatalf("GetCurrentBranch while locked: %v", err)
	} else if info.Behind != 0 {
		t.Fatalf("fetch ran despite busy remoteOpMu: behind = %d, want 0", info.Behind)
	}

	// The lock (not another gate) caused the skip: release it, clear the
	// min-interval stamp the skipped attempt took, and the identical state
	// must now fetch successfully.
	f.remoteOpMu.Unlock()
	resetAutoFetchStamp(f)
	f.autoFetchOnce("switch")
	if payloads := rec.statusPayloads(); len(payloads) != 1 {
		t.Fatalf("auto-fetch after releasing remoteOpMu: git:status_changed %d times, want 1", len(payloads))
	}
	if info, err := f.GetCurrentBranch(); err != nil {
		t.Fatalf("GetCurrentBranch after fetch: %v", err)
	} else if info.Behind != 1 {
		t.Errorf("after fetch: behind = %d, want 1", info.Behind)
	}
}

// TestAutoFetchOnce_NetworkFailure_IsQuiet verifies the failure contract:
// a fetch that cannot reach the remote produces zero events (no error
// toast, no status_changed) and simply Debug-logs inside the funnel.
func TestAutoFetchOnce_NetworkFailure_IsQuiet(t *testing.T) {
	localDir := setupAutoFetchRepo(t)

	// Break the remote: fetch to a deleted directory fails fast, standing
	// in for any network/credentials failure.
	remoteURL := gitOut(t, localDir, "remote", "get-url", "origin")
	if err := os.RemoveAll(remoteURL); err != nil {
		t.Fatalf("remove bare remote %s: %v", remoteURL, err)
	}

	f, _ := newAutoFetchAPI(localDir, "proj-1", &config.Config{})
	rec := &statusPayloadRecorder{}
	f.emitEvent = rec.record

	f.autoFetchOnce("switch") // must fail quietly

	if total := rec.totalEvents(); total != 0 {
		t.Errorf("failed auto-fetch emitted %d events, want 0", total)
	}
}

// --- SwitchProject hook ---

// TestSwitchProject_HookFiresOnRealSwitch verifies the first trigger of the
// funnel: a REAL project switch onto a git project starts the background
// fetch (observable via git:status_changed with the workspace path and the
// refreshed behind count), while the already-active early return of
// SwitchProject does NOT trigger it.
func TestSwitchProject_HookFiresOnRealSwitch(t *testing.T) {
	h := newProjectSwitchHarness(t)
	defer h.close(t)

	// Turn the harness workspace into a git project tracking a bare origin
	// that is one commit ahead, so the background fetch is observable.
	remoteDir := t.TempDir()
	gitOut(t, remoteDir, "init", "--bare")
	gitInit(t, h.workspace)
	commitFile(t, h.workspace, "committed.txt", "v1\n")
	gitOut(t, h.workspace, "remote", "add", "origin", remoteDir)
	branch := gitDefaultBranch(t, h.workspace)
	gitOut(t, h.workspace, "push", "-u", "origin", branch)
	advanceRemote(t, h.workspace)

	// Mirror the standard SwitchProject test setup: a seeded session (so
	// applySavedProjectSwitchState resolves a fallback) and the mock builder
	// (switchProjectActivate calls SetMCPWorkDir on it). A non-nil config
	// keeps the auto-fetch gate enabled (nil config fails closed).
	now := time.Now().UTC().Format(time.RFC3339)
	h.seedSession(t, "session-a", now, now)
	h.api.config = &config.Config{}
	h.api.builderOverride = &mockBuilder{}
	t.Cleanup(func() {
		h.api.watcherMu.Lock()
		defer h.api.watcherMu.Unlock()
		if h.api.watcher != nil {
			_ = h.api.watcher.Close()
			h.api.watcher = nil
		}
	})

	rec := &statusPayloadRecorder{}
	h.api.emitEvent = rec.record

	// Real switch: the hook must fire in the background.
	if err := h.api.SwitchProject(h.projectID); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	pollUntil(t, 10*time.Second, func() bool {
		return len(rec.statusPayloads()) == 1
	}, "project switch did not trigger a background fetch (git:status_changed)")

	payloads := rec.statusPayloads()
	if payloads[0] != h.workspace {
		t.Errorf("git:status_changed payload = %q, want workspace %q", payloads[0], h.workspace)
	}
	info, err := h.api.GetCurrentBranch()
	if err != nil {
		t.Fatalf("GetCurrentBranch after switch-triggered fetch: %v", err)
	}
	if info.Behind != 1 {
		t.Errorf("after switch-triggered fetch: behind = %d, want 1", info.Behind)
	}

	// Already-active early return: must NOT trigger the funnel. Reset the
	// min-interval stamp so a (wrongly fired) hook could not hide behind it,
	// then assert the stamp is still untouched afterwards — the stamp is
	// taken before any git subprocess, so an untouched stamp proves the
	// funnel never even started, with no timing flakiness.
	resetAutoFetchStamp(h.api)
	if err := h.api.SwitchProject(h.projectID); err != nil {
		t.Fatalf("SwitchProject (already active): %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	h.api.autoFetchMu.Lock()
	stamp := h.api.lastAutoFetchAt
	h.api.autoFetchMu.Unlock()
	if !stamp.IsZero() {
		t.Error("already-active SwitchProject triggered the auto-fetch funnel; want no fetch on the early-return path")
	}
	if n := len(rec.statusPayloads()); n != 1 {
		t.Errorf("git:status_changed fired %d times after the already-active switch, want still 1", n)
	}
}

// --- Fetch runner env pinning ---

// TestAutoFetchEnv_UntrustedPath_PinsTerminalPromptExactlyOnce builds the
// fetch command on an UNTRUSTED repository (the hardened workspace.GitCmdInRepo
// path) and verifies the env contract: GIT_TERMINAL_PROMPT appears exactly
// once, pinned to 0 (an inherited =1 must be stripped, not duplicated), and
// the GitCmdInRepo hardening baseline (GIT_EDITOR=true) stays intact.
func TestAutoFetchEnv_UntrustedPath_PinsTerminalPromptExactlyOnce(t *testing.T) {
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	repo := setupAutoFetchRepo(t)
	t.Setenv("GIT_TERMINAL_PROMPT", "1") // inherited value that must be stripped

	cmd, err := newAutoFetchCmd(context.Background(), repo)
	if err != nil {
		t.Fatalf("newAutoFetchCmd: %v", err)
	}

	var promptValues []string
	editorPinned := false
	for _, kv := range cmd.Env {
		switch {
		case strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT="):
			promptValues = append(promptValues, strings.TrimPrefix(kv, "GIT_TERMINAL_PROMPT="))
		case kv == "GIT_EDITOR=true":
			editorPinned = true
		}
	}
	if len(promptValues) != 1 || promptValues[0] != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT entries = %v, want exactly one %q", promptValues, "0")
	}
	if !editorPinned {
		t.Error("untrusted fetch lost the GitCmdInRepo GIT_EDITOR=true hardening pin")
	}
}

// TestAutoFetchEnv_TrustedPath_InheritsFullEnvironment verifies the trusted
// repository path (GitCmdRaw leaves cmd.Env nil): the runner must FIRST
// materialize os.Environ() and then strip+append, so the child inherits the
// complete parent environment (not a wiped single-var env) and still carries
// GIT_TERMINAL_PROMPT=0 exactly once.
func TestAutoFetchEnv_TrustedPath_InheritsFullEnvironment(t *testing.T) {
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	repo := setupAutoFetchRepo(t)
	// Trust bound to the repo's recorded semantic identity: the spawn-path
	// recheck requires a registry entry whose fingerprint reproduces the live
	// config, so the test registers the way the backend mirror does — a
	// fingerprint-less entry keeps the hardened path (ADR-070 on the spawn
	// path).
	info, err := workspace.ScanGitConfig(repo)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	info.ResolveIncludes()
	gittrust.TrustWithFingerprint(repo, info.SemanticFingerprint())
	t.Setenv("GIT_TERMINAL_PROMPT", "1")

	cmd, err := newAutoFetchCmd(context.Background(), repo)
	if err != nil {
		t.Fatalf("newAutoFetchCmd: %v", err)
	}
	if cmd.Env == nil {
		t.Fatal("trusted fetch cmd.Env is nil; the GIT_TERMINAL_PROMPT pin was never applied")
	}

	envSet := make(map[string]int, len(cmd.Env))
	promptCount := 0
	for _, kv := range cmd.Env {
		envSet[kv]++
		if strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT=") {
			promptCount++
		}
	}
	if promptCount != 1 || envSet["GIT_TERMINAL_PROMPT=0"] != 1 {
		t.Errorf("GIT_TERMINAL_PROMPT=0 occurrences: total prompt entries = %d, pinned = %d; want exactly one pin",
			promptCount, envSet["GIT_TERMINAL_PROMPT=0"])
	}

	// The full inherited environment must survive: every parent variable
	// (except the stripped-and-repinned prompt) appears in cmd.Env. A
	// wiped env (the single-var assignment the runner must avoid) would
	// drop almost all of them.
	parent := os.Environ()
	missing := 0
	for _, kv := range parent {
		if strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT=") {
			continue
		}
		if envSet[kv] == 0 {
			missing++
			if missing <= 3 {
				t.Errorf("trusted fetch env lost inherited variable %q", kv)
			}
		}
	}
	if missing > 3 {
		t.Errorf("trusted fetch env lost %d inherited variables in total", missing)
	}

	// The trusted path spawns raw git: it must NOT carry the hardened
	// baseline pins (guarded on the parent not providing GIT_EDITOR, which
	// would be inherited either way).
	if os.Getenv("GIT_EDITOR") == "" && envSet["GIT_EDITOR=true"] != 0 {
		t.Error("trusted fetch env carries the GIT_EDITOR=true hardening pin; trusted repos spawn raw git")
	}
}
