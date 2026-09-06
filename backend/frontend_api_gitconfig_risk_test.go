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

// TestNotifyGitConfigRisk_TransportKeysCovered pins the post-v0.7.3
// review [40]/[55] closure at the intake surface: the Git panel's remote
// operations (pull/push/fetch) and the plain git diff porcelain make
// core.sshCommand, core.askPass, the credential helpers and diff.external
// reachable, so their findings must reach the warning with the corrected
// texts (Git-panel reachability, per-key neutralization) instead of the old
// "not reachable" reassurance, and the standing notice must state that
// remote operations are covered too.
func TestNotifyGitConfigRisk_TransportKeysCovered(t *testing.T) {
	dir := t.TempDir()
	writeGitConfig(t, dir, "[core]\n\tsshCommand = /tmp/evil-ssh.sh\n\taskPass = /tmp/evil-ask.sh\n"+
		"[credential]\n\thelper = /tmp/evil-cred.sh\n"+
		"[credential \"https://x.example\"]\n\thelper = /tmp/evil-cred-url.sh\n"+
		"[diff]\n\texternal = /tmp/evil-ext.sh\n")

	f := &FrontendAPI{}
	rec := newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)

	if !rec.fired {
		t.Fatal("expected project:git_config_risk to be emitted for transport-bearing keys")
	}
	if !strings.Contains(rec.data.Notice, "remote operations (pull, push, fetch)") {
		t.Errorf("notice must state remote-operation coverage: %q", rec.data.Notice)
	}
	keys := map[string]string{}
	for _, fin := range rec.data.Findings {
		keys[fin.Key] = fin.Description
	}
	for key, phrase := range map[string]string{
		"core.sshcommand":                     "Git panel's remote RPCs",
		"core.askpass":                        "Git panel's remote RPCs",
		"credential.helper":                   "resets",
		"credential.https://x.example.helper": "credential.https://x.example.helper= (empty)",
		"diff.external":                       "BY DEFAULT",
	} {
		desc, ok := keys[key]
		if !ok {
			t.Errorf("expected finding for %q, got findings %v", key, rec.data.Findings)
			continue
		}
		if !strings.Contains(desc, phrase) {
			t.Errorf("%s description must mention %q: %q", key, phrase, desc)
		}
		if strings.Contains(desc, "not reachable") || strings.Contains(desc, "only executes with the --ext-diff flag") {
			t.Errorf("%s description carries a stale false claim: %q", key, desc)
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

// TestNotifyGitConfigRisk_AttributesDisabledDisclosure pins the review-[56]c
// closure at the intake surface: while include directives keep the blanket
// attr.tree kill engaged, the warning must carry an explicit
// "(attributes disabled)" entry disclosing the collateral (eol/CRLF
// normalization off → falsely-modified files) — and in narrow mode (a
// visible filter pinned by name, no includes) the entry must NOT appear,
// because benign attributes keep working there.
func TestNotifyGitConfigRisk_AttributesDisabledDisclosure(t *testing.T) {
	t.Run("include engages attr.tree: disclosure present", func(t *testing.T) {
		dir := t.TempDir()
		writeGitConfig(t, dir, "[include]\n\tpath = ~/.gitconfig-evil\n")

		f := &FrontendAPI{}
		rec := newRiskRecorder(t, f)
		f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)

		if !rec.fired {
			t.Fatal("expected project:git_config_risk for a config with includes")
		}
		var desc string
		for _, fin := range rec.data.Findings {
			if fin.Key == "(attributes disabled)" {
				desc = fin.Description
			}
		}
		if desc == "" {
			t.Fatalf("expected an (attributes disabled) finding, got %+v", rec.data.Findings)
		}
		for _, phrase := range []string{"attribute interpretation", "normalization", "modified"} {
			if !strings.Contains(desc, phrase) {
				t.Errorf("disclosure must mention %q: %q", phrase, desc)
			}
		}
	})

	t.Run("narrow mode (visible filter): no disclosure", func(t *testing.T) {
		dir := t.TempDir()
		writeGitConfig(t, dir, "[filter \"lfs\"]\n\tclean = git-lfs clean -- %f\n")

		f := &FrontendAPI{}
		rec := newRiskRecorder(t, f)
		f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)

		if !rec.fired {
			t.Fatal("expected project:git_config_risk for a filter-configured repo")
		}
		for _, fin := range rec.data.Findings {
			if fin.Key == "(attributes disabled)" {
				t.Errorf("narrow mode must not claim attributes are disabled: %+v", rec.data.Findings)
			}
		}
	})
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

// TestNotifyGitConfigRisk_AttributeRoutingSourcesEmitted covers the
// post-v0.7.3 [1]a closure surface: .git/info/attributes routing and
// core.attributesFile must reach the intake warning alongside the include
// directive that hides the driver definition, and an unscannable routing
// source (no kill-switch exists) must fail closed into the unreadable
// warning instead of staying silent.
func TestNotifyGitConfigRisk_AttributeRoutingSourcesEmitted(t *testing.T) {
	dir := t.TempDir()
	writeGitConfig(t, dir, "[include]\n\tpath = /abs/extra.conf\n")
	infoPath := filepath.Join(dir, ".git", "info", "attributes")
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(infoPath, []byte("*.txt filter=x merge=m\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := &FrontendAPI{}
	rec := newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)

	if !rec.fired {
		t.Fatal("expected project:git_config_risk to be emitted")
	}
	keys := map[string]string{}
	for _, fin := range rec.data.Findings {
		keys[fin.Key] = fin.Description
	}
	for _, want := range []string{"(include directive)", "info/attributes:filter=x", "info/attributes:merge=m"} {
		if keys[want] == "" {
			t.Errorf("expected finding %q, got findings %v", want, rec.data.Findings)
		}
	}

	// Fail closed: an info/attributes that exists but cannot be scanned is
	// itself a warning — the attributes mechanism has no config kill-switch.
	unscannable := t.TempDir()
	writeGitConfig(t, unscannable, "[core]\n\tfsmonitor = false\n")
	if err := os.MkdirAll(filepath.Join(unscannable, ".git", "info", "attributes"), 0o755); err != nil {
		t.Fatal(err)
	}
	f2 := &FrontendAPI{}
	rec2 := newRiskRecorder(t, f2)
	f2.notifyGitConfigRisk(GitConfigRiskSourceProject, unscannable)
	if !rec2.fired {
		t.Fatal("expected a fail-closed warning for an unscannable info/attributes")
	}
	if len(rec2.data.Findings) != 1 || rec2.data.Findings[0].Key != "(config unreadable)" {
		t.Errorf("findings = %+v, want the single (config unreadable) marker", rec2.data.Findings)
	}
}

// TestNotifyGitConfigRisk_AttributesAndTrustsRepoRoot pins review [52]:
// the scan walks up from a subdirectory workspace to the parent
// repository's config, so the warning must be attributed to — and trust
// keyed on — the repository's WORK-TREE ROOT, not the scanned
// subdirectory. Trusting any path inside a repository must silence the
// warning for every other open of that repository, and removal must prune
// the stored root even when handed the subdirectory form.
func TestNotifyGitConfigRisk_AttributesAndTrustsRepoRoot(t *testing.T) {
	repo := t.TempDir()
	writeGitConfig(t, repo, "[core]\n\tfsmonitor = /tmp/evil\n")
	sub := filepath.Join(repo, "sub", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// Warning attribution: the subdirectory open warns under the repo root.
	f := &FrontendAPI{}
	rec := newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceWorkdir, sub)
	if !rec.fired {
		t.Fatal("expected the parent repository's armed config to warn")
	}
	if rec.data.Path != repo {
		t.Errorf("risk.Path = %q, want the work-tree root %q", rec.data.Path, repo)
	}

	// Trust entered via the SUBDIRECTORY normalizes to the same root...
	f2, _, _ := newTestAPI(t)
	if err := f2.TrustGitRepo(sub); err != nil {
		t.Fatalf("TrustGitRepo(subdir): %v", err)
	}
	trusted := f2.GetTrustedGitRepos()
	if len(trusted) != 1 || trusted[0] != repo {
		t.Fatalf("stored trusted repos = %v, want exactly [%s]", trusted, repo)
	}

	// ...so both a subdirectory re-open and a direct root open stay silent.
	recSub := newRiskRecorder(t, f2)
	f2.notifyGitConfigRisk(GitConfigRiskSourceWorkdir, sub)
	if recSub.fired {
		t.Error("warning re-fired for a trusted repository opened at a subdirectory")
	}
	recRoot := newRiskRecorder(t, f2)
	f2.notifyGitConfigRisk(GitConfigRiskSourceProject, repo)
	if recRoot.fired {
		t.Error("warning fired for the trusted repository root itself")
	}

	// Removal via the subdirectory prunes the stored root entry.
	if err := f2.RemoveTrustedGitRepo(sub); err != nil {
		t.Fatalf("RemoveTrustedGitRepo(subdir): %v", err)
	}
	if got := f2.GetTrustedGitRepos(); len(got) != 0 {
		t.Errorf("trusted repos after removal = %v, want empty", got)
	}
}

// TestSwitchProject_AlreadyActiveStillScansGitConfigRisk pins review [57]:
// re-selecting the already-active project re-emits project:switched AND
// re-runs the intake risk scan — a .git/config planted since the last scan
// must warn on the next re-selection, not only after a real project switch.
func TestSwitchProject_AlreadyActiveStillScansGitConfigRisk(t *testing.T) {
	h := newProjectSwitchHarness(t)
	defer h.close(t)
	writeGitConfig(t, h.workspace, "[core]\n\tfsmonitor = /tmp/evil\n")

	// Same activation-path scaffolding as the existing SwitchProject tests.
	h.api.builderOverride = &mockBuilder{}
	t.Cleanup(func() {
		h.api.watcherMu.Lock()
		defer h.api.watcherMu.Unlock()
		if h.api.watcher != nil {
			_ = h.api.watcher.Close()
			h.api.watcher = nil
		}
	})

	riskEvents := 0
	h.api.emitEvent = func(name string, _ ...any) {
		if name == EventGitConfigRisk {
			riskEvents++
		}
	}

	if err := h.api.SwitchProject(h.projectID); err != nil {
		t.Fatalf("first SwitchProject: %v", err)
	}
	if riskEvents != 1 {
		t.Fatalf("risk events after first switch = %d, want 1", riskEvents)
	}

	if err := h.api.SwitchProject(h.projectID); err != nil {
		t.Fatalf("already-active SwitchProject: %v", err)
	}
	if riskEvents != 2 {
		t.Errorf("risk events after already-active re-selection = %d, want 2 (intake scan re-run)", riskEvents)
	}
}
