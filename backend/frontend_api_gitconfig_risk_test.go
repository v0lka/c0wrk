package backend

import (
	"os"
	"path/filepath"
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
