package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeGitConfig creates dir/.git/config with the given content. The scan is
// pure text parsing, so a bare .git/config pair is enough — no git binary or
// real repository is needed.
func writeGitConfig(t *testing.T, dir, content string) {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("failed to create .git dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write .git/config: %v", err)
	}
}

// riskRecorder captures project:git_config_risk payloads (and every event
// name) emitted through a FrontendAPI's injected emitter.
type riskRecorder struct {
	fired bool
	data  GitConfigRiskData
	names []string
}

func newRiskRecorder(t *testing.T, f *FrontendAPI) *riskRecorder {
	t.Helper()
	r := &riskRecorder{}
	f.emitEvent = func(name string, args ...any) {
		r.names = append(r.names, name)
		if name != EventGitConfigRisk {
			return
		}
		if r.fired {
			t.Errorf("%s emitted more than once", EventGitConfigRisk)
		}
		r.fired = true
		if len(args) < 1 {
			t.Errorf("%s emitted without payload", EventGitConfigRisk)
			return
		}
		data, ok := args[0].(GitConfigRiskData)
		if !ok {
			t.Errorf("%s payload is %T, want GitConfigRiskData", EventGitConfigRisk, args[0])
			return
		}
		r.data = data
	}
	return r
}

func TestNotifyGitConfigRisk_DangerousKeysEmitted(t *testing.T) {
	dir := t.TempDir()
	writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/evil\n\thooksPath = .githooks\n[filter \"lfs\"]\n\tprocess = /tmp/evil-filter\n")

	f := &FrontendAPI{}
	rec := newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)

	if !rec.fired {
		t.Fatal("expected project:git_config_risk to be emitted for a dangerous config")
	}
	risk := &rec.data
	if risk.Path != dir || risk.Source != GitConfigRiskSourceProject {
		t.Errorf("unexpected path/source: %q/%q", risk.Path, risk.Source)
	}
	if risk.Notice == "" {
		t.Error("expected the standing hooks-do-not-run notice in the payload")
	}
	keys := map[string]string{}
	for _, fin := range risk.Findings {
		keys[fin.Key] = fin.Description
	}
	for _, want := range []string{"core.fsmonitor", "core.hookspath", "filter.lfs.process"} {
		if keys[want] == "" {
			t.Errorf("expected finding for %q, got findings %v", want, risk.Findings)
		}
	}
	// Exactly one risk event — the scan itself must not emit anything else.
	for _, n := range rec.names {
		if n != EventGitConfigRisk {
			t.Errorf("unexpected extra event %q", n)
		}
	}
}

func TestNotifyGitConfigRisk_CleanRepoSilent(t *testing.T) {
	t.Run("no git dir at all", func(t *testing.T) {
		f := &FrontendAPI{}
		rec := newRiskRecorder(t, f)
		f.notifyGitConfigRisk(GitConfigRiskSourceWorkdir, t.TempDir())
		if rec.fired {
			t.Errorf("expected no event for a non-git directory, got %+v", rec.data)
		}
	})

	t.Run("benign config", func(t *testing.T) {
		dir := t.TempDir()
		writeGitConfig(t, dir, "[user]\n\tname = Test\n\temail = test@example.com\n[init]\n\tdefaultBranch = main\n")
		f := &FrontendAPI{}
		rec := newRiskRecorder(t, f)
		f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)
		if rec.fired {
			t.Errorf("expected no event for a clean config, got %+v", rec.data)
		}
	})
}

func TestNotifyGitConfigRisk_IncludeDirectiveFailsClosed(t *testing.T) {
	dir := t.TempDir()
	writeGitConfig(t, dir, "[include]\n\tpath = ~/.gitconfig-evil\n")

	f := &FrontendAPI{}
	rec := newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)

	if !rec.fired {
		t.Fatal("expected event: an include makes the visible config incomplete")
	}
	found := false
	for _, fin := range rec.data.Findings {
		if fin.Key == "(include directive)" && fin.Description != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an include-directive finding, got %+v", rec.data.Findings)
	}
}

func TestNotifyGitConfigRisk_MalformedConfigFailsClosed(t *testing.T) {
	dir := t.TempDir()
	writeGitConfig(t, dir, "[core]\n\tfsmonitor = \"unterminated\n")

	f := &FrontendAPI{}
	rec := newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceWorkdir, dir)

	if !rec.fired {
		t.Fatal("expected event: a malformed config cannot be proven safe")
	}
	found := false
	for _, fin := range rec.data.Findings {
		if fin.Key == "(config malformed)" && fin.Description != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a config-malformed finding, got %+v", rec.data.Findings)
	}
}

func TestNotifyGitConfigRisk_UnreadableConfigFailsClosed(t *testing.T) {
	dir := t.TempDir()
	// Make .git/config a directory: reading it fails on every platform,
	// unlike permission-based tricks that behave differently for root/Windows.
	if err := os.MkdirAll(filepath.Join(dir, ".git", "config"), 0o755); err != nil {
		t.Fatalf("failed to create .git/config dir: %v", err)
	}

	f := &FrontendAPI{}
	rec := newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)

	if !rec.fired {
		t.Fatal("expected event: an unreadable config cannot be proven safe")
	}
	if len(rec.data.Findings) != 1 || rec.data.Findings[0].Key != "(config unreadable)" {
		t.Errorf("expected a single (config unreadable) finding, got %+v", rec.data.Findings)
	}
}

func TestSwitchProject_EmitsGitConfigRiskForDangerousWorkspace(t *testing.T) {
	h := newProjectSwitchHarness(t)
	defer h.close(t)
	writeGitConfig(t, h.workspace, "[core]\n\tfsmonitor = /tmp/evil\n")

	// Same activation-path scaffolding as the existing SwitchProject tests:
	// the harness Application has no real builder and the watcher must be
	// closed before TempDir cleanup.
	h.api.builderOverride = &mockBuilder{}
	t.Cleanup(func() {
		h.api.watcherMu.Lock()
		defer h.api.watcherMu.Unlock()
		if h.api.watcher != nil {
			_ = h.api.watcher.Close()
			h.api.watcher = nil
		}
	})

	switched, risk := false, false
	h.api.emitEvent = func(name string, _ ...any) {
		switch name {
		case EventProjectSwitched:
			switched = true
		case EventGitConfigRisk:
			risk = true
		}
	}

	if err := h.api.SwitchProject(h.projectID); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	if !switched {
		t.Error("expected project:switched to be emitted")
	}
	if !risk {
		t.Error("expected project:git_config_risk to be emitted for a workspace with a dangerous config")
	}
}

func TestSwitchProject_CleanWorkspaceDoesNotEmitRiskEvent(t *testing.T) {
	h := newProjectSwitchHarness(t)
	defer h.close(t)

	h.api.builderOverride = &mockBuilder{}
	t.Cleanup(func() {
		h.api.watcherMu.Lock()
		defer h.api.watcherMu.Unlock()
		if h.api.watcher != nil {
			_ = h.api.watcher.Close()
			h.api.watcher = nil
		}
	})

	h.api.emitEvent = func(name string, _ ...any) {
		if name == EventGitConfigRisk {
			t.Errorf("project:git_config_risk emitted for a clean workspace")
		}
	}

	if err := h.api.SwitchProject(h.projectID); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
}

func TestAddWorkDirectory_EmitsGitConfigRiskForDangerousDir(t *testing.T) {
	h := newWorkDirsHarness(t)
	dir := h.existingDir(t)
	writeGitConfig(t, dir, "[filter \"evil\"]\n\tprocess = /tmp/evil\n")

	var risk *GitConfigRiskData
	h.api.emitEvent = func(name string, args ...any) {
		h.events = append(h.events, name)
		if name == EventGitConfigRisk && len(args) >= 1 {
			if data, ok := args[0].(GitConfigRiskData); ok {
				risk = &data
			}
		}
	}

	if err := h.api.AddWorkDirectory("project", h.projectID, dir, "untrusted checkout"); err != nil {
		t.Fatalf("AddWorkDirectory: %v", err)
	}
	if risk == nil {
		t.Fatal("expected project:git_config_risk to be emitted for a workdir with a dangerous config")
	}
	if risk.Source != GitConfigRiskSourceWorkdir {
		t.Errorf("expected source %q, got %q", GitConfigRiskSourceWorkdir, risk.Source)
	}
	keys := make([]string, 0, len(risk.Findings))
	for _, fin := range risk.Findings {
		keys = append(keys, fin.Key)
	}
	if len(keys) != 1 || keys[0] != "filter.evil.process" {
		t.Errorf("expected exactly [filter.evil.process], got %v", keys)
	}
	if h.emitCount(EventWorkDirsChanged) == 0 {
		t.Error("expected workdirs:changed to still be emitted")
	}
}

func TestAddWorkDirectory_CleanDirDoesNotEmitRiskEvent(t *testing.T) {
	h := newWorkDirsHarness(t)
	dir := h.existingDir(t)

	h.api.emitEvent = func(name string, _ ...any) {
		if name == EventGitConfigRisk {
			t.Errorf("project:git_config_risk emitted for a clean workdir")
		}
	}

	if err := h.api.AddWorkDirectory("session", h.sessionID, dir, "benign"); err != nil {
		t.Fatalf("AddWorkDirectory: %v", err)
	}
}

// --- Trusted repositories (security.trusted_git_repos) ---

// TestNotifyGitConfigRisk_TrustedRepoSuppressed verifies the trust-list
// carve-out: a repository the user explicitly trusted emits no intake
// warning; a different armed repository still warns (trust is per exact
// root — no prefix or subtree semantics); and removing the trust restores
// the warning on the next open.
func TestNotifyGitConfigRisk_TrustedRepoSuppressed(t *testing.T) {
	dir := t.TempDir()
	writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/evil\n")

	f, _, _ := newTestAPI(t)
	if err := f.TrustGitRepo(dir); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}

	rec := newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)
	if rec.fired {
		t.Error("expected no project:git_config_risk for a trusted repository")
	}

	other := t.TempDir()
	writeGitConfig(t, other, "[core]\n\tfsmonitor = /tmp/evil\n")
	rec = newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceWorkdir, other)
	if !rec.fired {
		t.Error("expected project:git_config_risk for an untrusted repository")
	}

	if err := f.RemoveTrustedGitRepo(dir); err != nil {
		t.Fatalf("RemoveTrustedGitRepo: %v", err)
	}
	rec = newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)
	if !rec.fired {
		t.Error("expected project:git_config_risk after the trust was removed")
	}
}

// TestTrustGitRepo_RoundTrip covers the RPC surface: path validation (empty,
// relative, nonexistent), the idempotent add (a trailing-slash form of an
// already-trusted root cleans to the same entry), the defensive copy from
// GetTrustedGitRepos, persistence to config.yaml, and the idempotent remove.
func TestTrustGitRepo_RoundTrip(t *testing.T) {
	f, _, cfgPath := newTestAPI(t)

	if err := f.TrustGitRepo(""); err == nil {
		t.Error("expected error for an empty path")
	}
	if err := f.TrustGitRepo("relative/repo"); err == nil {
		t.Error("expected error for a relative path")
	}
	if err := f.TrustGitRepo(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("expected error for a nonexistent path")
	}
	if got := f.GetTrustedGitRepos(); len(got) != 0 {
		t.Errorf("GetTrustedGitRepos = %v, want empty after rejected adds", got)
	}

	dir := t.TempDir()
	if err := f.TrustGitRepo(dir); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}
	if err := f.TrustGitRepo(dir + string(filepath.Separator)); err != nil {
		t.Fatalf("TrustGitRepo (trailing-slash form): %v", err)
	}
	got := f.GetTrustedGitRepos()
	if len(got) != 1 || got[0] != dir {
		t.Fatalf("GetTrustedGitRepos = %v, want exactly [%s]", got, dir)
	}

	// The returned slice is a copy: mutating it must not touch live config.
	got[0] = "/mutated"
	if f.GetTrustedGitRepos()[0] != dir {
		t.Error("GetTrustedGitRepos leaked the live config slice")
	}

	// The trusted root survives the persist path (config.yaml on disk).
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	if !strings.Contains(string(raw), dir) {
		t.Errorf("persisted config does not contain the trusted repo %s", dir)
	}

	if err := f.RemoveTrustedGitRepo(dir); err != nil {
		t.Fatalf("RemoveTrustedGitRepo: %v", err)
	}
	if err := f.RemoveTrustedGitRepo(dir); err != nil {
		t.Fatalf("RemoveTrustedGitRepo (repeat): %v", err)
	}
	if got := f.GetTrustedGitRepos(); len(got) != 0 {
		t.Errorf("GetTrustedGitRepos = %v, want empty after removal", got)
	}
}

// TestGitRepoTrusted_NilConfigIsFailClosed: with no config loaded nothing is
// trusted and nothing can be trusted — the warning path stays on, mirroring
// the fail-closed direction of the scan itself.
func TestGitRepoTrusted_NilConfigIsFailClosed(t *testing.T) {
	f := &FrontendAPI{}
	if f.gitRepoTrusted("/some/repo") {
		t.Error("gitRepoTrusted must be false with no config loaded")
	}
	if got := f.GetTrustedGitRepos(); len(got) != 0 {
		t.Errorf("GetTrustedGitRepos = %v, want empty with no config", got)
	}
	if err := f.TrustGitRepo(t.TempDir()); err == nil {
		t.Error("expected error from TrustGitRepo with no config loaded")
	}
}
