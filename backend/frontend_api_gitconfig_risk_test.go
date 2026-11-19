package backend

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/gittrust"
	"github.com/v0lka/c0wrk/core/workspace"
	"github.com/v0lka/c0wrk/internal/gittest"
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

	// mu guards names: the injected emitter runs both synchronously (the
	// project:git_config_risk event) and asynchronously — emitConfigUpdated
	// dispatches EventConfigUpdated on its own goroutine — so concurrent
	// appends are possible.
	mu    sync.Mutex
	names []string
}

func newRiskRecorder(t *testing.T, f *FrontendAPI) *riskRecorder {
	t.Helper()
	r := &riskRecorder{}
	f.emitEvent = func(name string, args ...any) {
		r.mu.Lock()
		r.names = append(r.names, name)
		r.mu.Unlock()
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

// eventNames returns a copy of the recorded event names, safe to call while
// the emitter may still be appending from its asynchronous dispatch.
func (r *riskRecorder) eventNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.names...)
}

func TestNotifyGitConfigRisk_DangerousKeysEmitted(t *testing.T) {
	dir := t.TempDir()
	writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/evil\n\thooksPath = .githooks\n[filter \"lfs\"]\n\tprocess = /tmp/evil-filter\n")

	f := &FrontendAPI{}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
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
	for _, n := range rec.eventNames() {
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
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
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

// TestNotifyGitConfigRisk_SigningKeysReported pins the intake surface of the
// baseline-covered signing findings: an armed commit.gpgsign (with its gpg.*
// and user.signingkey companions) must reach the warning payload with the
// baseline-pin description, while a plain [user]-only config stays silent.
// These keys derive no spawn hardening (the sysproc baseline's
// -c commit.gpgsign=false is the whole neutralization) — the event is the
// intake picture only.
func TestNotifyGitConfigRisk_SigningKeysReported(t *testing.T) {
	dir := t.TempDir()
	writeGitConfig(t, dir, "[commit]\n\tgpgsign = true\n[gpg]\n\tformat = ssh\n\tprogram = /tmp/evil-gpg.sh\n"+
		"[user]\n\tname = a\n\temail = b\n\tsigningkey = /tmp/evil-key\n")

	f := &FrontendAPI{}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	rec := newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)

	if !rec.fired {
		t.Fatal("expected project:git_config_risk for an armed signing config")
	}
	keys := map[string]string{}
	for _, fin := range rec.data.Findings {
		keys[fin.Key] = fin.Description
	}
	for _, want := range []string{"commit.gpgsign", "gpg.format", "gpg.program", "user.signingkey"} {
		desc, ok := keys[want]
		if !ok {
			t.Errorf("expected finding for %q, got findings %v", want, rec.data.Findings)
			continue
		}
		if !strings.Contains(desc, "-c commit.gpgsign=false") {
			t.Errorf("%s description must name the baseline pin: %q", want, desc)
		}
	}
	// The benign [user] keys in the same config must not add findings.
	for key := range keys {
		if key == "user.name" || key == "user.email" {
			t.Errorf("benign key %q must not produce a finding", key)
		}
	}

	// A clean [user]-only config stays silent (name/email are identity, not
	// signing vectors); commit.gpgsign=false — the value c0wrk's own
	// fixtures write — is silent too.
	t.Run("clean user config silent", func(t *testing.T) {
		dir := t.TempDir()
		writeGitConfig(t, dir, "[user]\n\tname = Test\n\temail = test@example.com\n[commit]\n\tgpgsign = false\n")
		f := &FrontendAPI{}
		f.seedPublished.Store(true)
		f.seedPublished.Store(true)
		rec := newRiskRecorder(t, f)
		f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)
		if rec.fired {
			t.Errorf("expected no event for a clean [user] config, got %+v", rec.data)
		}
	})

	// Inert signing siblings (review finding 3): gpg.format / gpg.program /
	// user.signingkey WITHOUT an armed commit.gpgsign cannot execute
	// anything — a repository that merely documents a signing setup must
	// not raise the intake warning.
	t.Run("inert signing siblings stay silent", func(t *testing.T) {
		dir := t.TempDir()
		writeGitConfig(t, dir, "[gpg]\n\tformat = ssh\n\tprogram = /tmp/evil-gpg.sh\n[user]\n\tsigningkey = /tmp/evil-key\n")
		f := &FrontendAPI{}
		f.seedPublished.Store(true)
		f.seedPublished.Store(true)
		rec := newRiskRecorder(t, f)
		f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)
		if rec.fired {
			t.Errorf("expected no event for inert signing siblings without armed commit.gpgsign, got %+v", rec.data)
		}
	})
}

func TestNotifyGitConfigRisk_CleanRepoSilent(t *testing.T) {
	t.Run("no git dir at all", func(t *testing.T) {
		f := &FrontendAPI{}
		f.seedPublished.Store(true)
		f.seedPublished.Store(true)
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
		f.seedPublished.Store(true)
		f.seedPublished.Store(true)
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
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	captureMiscDiagnostics(t, f, miscExpectedDiagnostic{
		message: "git config include directive ignored (not followed); config is an incomplete view",
		attrs:   map[string]string{"line": "2", "conditional": "false", "condition": "", "path": "~/.gitconfig-evil"},
	})
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
		f.seedPublished.Store(true)
		f.seedPublished.Store(true)
		captureMiscDiagnostics(t, f, miscExpectedDiagnostic{
			message: "git config include directive ignored (not followed); config is an incomplete view",
			attrs:   map[string]string{"line": "2", "conditional": "false", "condition": "", "path": "~/.gitconfig-evil"},
		})
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
		f.seedPublished.Store(true)
		f.seedPublished.Store(true)
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
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
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
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
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
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
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
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	captureMiscDiagnostics(t, f, miscExpectedDiagnostic{
		message: "git config include directive ignored (not followed); config is an incomplete view",
		attrs:   map[string]string{"line": "2", "conditional": "false", "condition": "", "path": "/abs/extra.conf"},
	})
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
	f2.seedPublished.Store(true)
	f2.seedPublished.Store(true)
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
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
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

// --- Trust snapshot + fingerprint recheck (step_3 RPC) ---

// TestTrustGitRepo_WorktreePathStoresRepositoryRoot pins the trust-key
// normalization for linked worktrees: trusting from a worktree path — the
// form the Commit-withheld dialog's Trust action passes (the project
// workspace) — records the repository's MAIN work-tree root, not the
// worktree, so a decision made from any path inside the repository covers
// the whole repository and the commit gate (which keys a worktree commit on
// the same root) agrees with what was stored. Before the unification this
// was the reported no-op Trust loop: the root was already trusted, the
// worktree commit keyed on the worktree path, and the trust RPC re-trusted
// the already-covered root without effect.
func TestTrustGitRepo_WorktreePathStoresRepositoryRoot(t *testing.T) {
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	wt := filepath.Join(t.TempDir(), "wt")
	repo.Git(t, "worktree", "add", wt, "-b", "wtbranch")

	f, _, _ := newTestAPI(t)
	if err := f.TrustGitRepo(wt); err != nil {
		t.Fatalf("TrustGitRepo(worktree): %v", err)
	}
	got := f.GetTrustedGitRepos()
	if len(got) != 1 || got[0] != repo.Root {
		t.Fatalf("GetTrustedGitRepos = %v, want exactly [%s] (the main work-tree root)", got, repo.Root)
	}
	// The gate must agree for a commit in the worktree.
	if !f.gitRepoTrusted(wt) {
		t.Fatal("gitRepoTrusted(worktree) = false — the gate must honor repository-root trust from the worktree")
	}
	if !f.gitRepoTrusted(repo.Root) {
		t.Fatal("gitRepoTrusted(main root) = false — the entry IS the main root")
	}
}

// TestTrustGitRepo_StoresFingerprintAndSnapshot pins acceptance (1): trusting
// a repo records the work-tree root plus a non-empty fingerprint of the
// scanned configuration, stores the snapshot bytes under the snapshots dir,
// and (via the existing registry test) enables raw git for the root.
func TestTrustGitRepo_StoresFingerprintAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/evil\n")

	f, _, _ := newTestAPI(t)
	if err := f.TrustGitRepo(dir); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}

	if len(f.config.Security.TrustedGitRepos) != 1 {
		t.Fatalf("trusted repos = %v, want exactly one", f.config.Security.TrustedGitRepos)
	}
	entry := f.config.Security.TrustedGitRepos[0]
	if entry.Path != dir {
		t.Errorf("trusted path = %q, want %q", entry.Path, dir)
	}
	if entry.Fingerprint == "" {
		t.Error("expected a non-empty fingerprint on the trusted entry")
	}
	if entry.SemanticFingerprint == "" {
		t.Error("expected a non-empty semantic fingerprint on the trusted entry")
	}
	if entry.SemanticFingerprint == entry.Fingerprint {
		t.Error("the semantic fingerprint must be a distinct identity from the raw one")
	}
	snap := filepath.Join(config.GitConfigSnapshotsDir(f.agentDir), entry.Fingerprint)
	if _, err := os.Stat(snap); err != nil {
		t.Errorf("expected snapshot file %s: %v", snap, err)
	}
	semSnap := filepath.Join(config.GitConfigSnapshotsDir(f.agentDir), entry.SemanticFingerprint)
	if _, err := os.Stat(semSnap); err != nil {
		t.Errorf("expected semantic snapshot file %s: %v", semSnap, err)
	}
}

// TestHardenGitRepo_RoundTripAndMutualExclusion pins acceptance (2): the
// HardenGitRepo / GetHardenGitRepos / RemoveHardenGitRepo RPCs validate and
// deduplicate paths, return defensive copies, are idempotent, and harden/trust
// are mutually exclusive in both directions.
func TestHardenGitRepo_RoundTripAndMutualExclusion(t *testing.T) {
	f, _, _ := newTestAPI(t)

	if err := f.HardenGitRepo(""); err == nil {
		t.Error("expected error for an empty path")
	}
	if err := f.HardenGitRepo("relative/repo"); err == nil {
		t.Error("expected error for a relative path")
	}
	if got := f.GetHardenGitRepos(); len(got) != 0 {
		t.Errorf("GetHardenGitRepos = %v, want empty after rejected adds", got)
	}

	dir := t.TempDir()
	if err := f.HardenGitRepo(dir); err != nil {
		t.Fatalf("HardenGitRepo: %v", err)
	}
	if err := f.HardenGitRepo(dir + string(filepath.Separator)); err != nil {
		t.Fatalf("HardenGitRepo (trailing-slash form): %v", err)
	}
	got := f.GetHardenGitRepos()
	if len(got) != 1 || got[0] != dir {
		t.Fatalf("GetHardenGitRepos = %v, want exactly [%s]", got, dir)
	}
	got[0] = "/mutated"
	if f.GetHardenGitRepos()[0] != dir {
		t.Error("GetHardenGitRepos leaked the live config slice")
	}

	// Hardening is the inverse of trust: trusting a hardened repo drops the
	// hardening, and hardening a trusted repo drops the trust.
	if err := f.TrustGitRepo(dir); err != nil {
		t.Fatalf("TrustGitRepo (from hardened): %v", err)
	}
	if got := f.GetHardenGitRepos(); len(got) != 0 {
		t.Errorf("GetHardenGitRepos = %v, want empty after trusting a hardened repo", got)
	}
	if got := f.GetTrustedGitRepos(); len(got) != 1 || got[0] != dir {
		t.Errorf("GetTrustedGitRepos = %v, want exactly [%s]", got, dir)
	}

	if err := f.HardenGitRepo(dir); err != nil {
		t.Fatalf("HardenGitRepo (from trusted): %v", err)
	}
	if got := f.GetTrustedGitRepos(); len(got) != 0 {
		t.Errorf("GetTrustedGitRepos = %v, want empty after hardening a trusted repo", got)
	}

	if err := f.RemoveHardenGitRepo(dir); err != nil {
		t.Fatalf("RemoveHardenGitRepo: %v", err)
	}
	if err := f.RemoveHardenGitRepo(dir); err != nil {
		t.Fatalf("RemoveHardenGitRepo (repeat): %v", err)
	}
	if got := f.GetHardenGitRepos(); len(got) != 0 {
		t.Errorf("GetHardenGitRepos = %v, want empty after removal", got)
	}
}

// injectLegacyGitSecurityEntry writes the pre-unification form of a trust or
// harden record directly into the live config — keyed on an arbitrary path
// inside the repository (a linked worktree), the spelling older builds
// stored before trust normalized to the repository trust root — and mirrors
// the trust form into the spawn registry the way syncGitTrustRegistry does.
func injectLegacyGitSecurityEntry(t *testing.T, f *FrontendAPI, trusted, hardened *string) {
	t.Helper()
	f.configMu.Lock()
	defer f.configMu.Unlock()
	if trusted != nil {
		f.config.Security.TrustedGitRepos = append(f.config.Security.TrustedGitRepos, config.TrustedGitRepo{Path: *trusted})
	}
	if hardened != nil {
		f.config.Security.HardenGitRepos = append(f.config.Security.HardenGitRepos, *hardened)
	}
	f.syncGitTrustRegistry()
}

// worktreeTrustFixture builds a main repository plus a linked worktree — the
// shape every legacy worktree-keyed entry in the wild points at.
func worktreeTrustFixture(t *testing.T) (root, wt string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	wt = filepath.Join(t.TempDir(), "wt")
	repo.Git(t, "worktree", "add", wt, "-b", "wtbranch")
	return root, wt
}

// TestHardenGitRepo_RemovesLegacyWorktreeKeyedTrust pins the security-
// relevant direction of the trust⊥harden mutual exclusion: hardening the
// repository through its MAIN root must remove a trusted entry that a
// pre-unification build keyed on the WORKTREE path. The spawn layer probes
// every trust-key form, so a surviving legacy entry would keep that
// worktree spawning raw git after the user's explicit hardening — the
// hardening silently losing for part of the repository, with Settings
// listing the same repository as both hardened and trusted.
func TestHardenGitRepo_RemovesLegacyWorktreeKeyedTrust(t *testing.T) {
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	root, wt := worktreeTrustFixture(t)
	f, _, _ := newTestAPI(t)
	wtPath := wt
	injectLegacyGitSecurityEntry(t, f, &wtPath, nil)
	if !f.gitRepoTrusted(root) {
		t.Fatal("precondition: the legacy worktree-keyed entry must cover the main root through the probe forms")
	}

	if err := f.HardenGitRepo(root); err != nil {
		t.Fatalf("HardenGitRepo: %v", err)
	}
	if got := f.GetTrustedGitRepos(); len(got) != 0 {
		t.Errorf("trusted repos after hardening = %v, want empty — the legacy entry must not survive as a raw-git zombie", got)
	}
	if got := f.GetHardenGitRepos(); len(got) != 1 || got[0] != root {
		t.Errorf("harden repos = %v, want exactly [%s]", got, root)
	}
	if gittrust.IsTrusted(wt) || gittrust.IsTrusted(root) {
		t.Error("expected the spawn registry to drop every form of the repository after hardening")
	}
}

// TestTrustGitRepo_RemovesLegacyWorktreeKeyedHardening pins the mirror
// direction: trusting the repository through its MAIN root must remove a
// hardened entry keyed on the legacy worktree form — the spawn layer's
// multi-form probes would otherwise read the worktrees as trusted while the
// user's explicit hardening decision silently survived.
func TestTrustGitRepo_RemovesLegacyWorktreeKeyedHardening(t *testing.T) {
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	root, wt := worktreeTrustFixture(t)
	f, _, _ := newTestAPI(t)
	wtPath := wt
	injectLegacyGitSecurityEntry(t, f, nil, &wtPath)

	if err := f.TrustGitRepo(root); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}
	if got := f.GetHardenGitRepos(); len(got) != 0 {
		t.Errorf("harden repos after trusting = %v, want empty — the legacy hardening must not survive", got)
	}
	if got := f.GetTrustedGitRepos(); len(got) != 1 || got[0] != root {
		t.Errorf("trusted repos = %v, want exactly [%s]", got, root)
	}
	if !gittrust.IsTrusted(root) {
		t.Error("expected the spawn registry to carry the new trust")
	}
}

// TestSyncGitTrustRegistry_NormalizesLegacyWorktreeEntry pins the registry
// half of the legacy-entry story: a trusted entry a pre-unification build
// keyed on a linked-worktree path must register the repository's MAIN root —
// the form the spawn layer's probes of any path inside the repository reach.
// The config-side trust check matches every key form of both sides, so an
// un-normalized registry would leave a main-root commit reading as trusted
// at the gate while the spawn layer keeps hardening it — silently skipping
// the hooks trust promised. A path with no discoverable repository degrades
// to the verbatim form, which the spawn layer's own degraded probes still
// reach.
func TestSyncGitTrustRegistry_NormalizesLegacyWorktreeEntry(t *testing.T) {
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	root, wt := worktreeTrustFixture(t)
	f, _, _ := newTestAPI(t)
	wtPath := wt
	injectLegacyGitSecurityEntry(t, f, &wtPath, nil)

	if !gittrust.IsTrusted(root) {
		t.Fatal("expected the registry to carry the main root for a legacy worktree-keyed entry")
	}
	// The spawn layer probes every trust-key form of the operating path and
	// trusts on the FIRST match — the registered root must be reachable from
	// the worktree's probe set (mirroring the trustedRoot loop in
	// gitCmdInRepoScanned), not every individual form.
	trusted := false
	for _, form := range workspace.TrustProbePaths(wt) {
		if gittrust.IsTrusted(form) {
			trusted = true
			break
		}
	}
	if !trusted {
		t.Error("no spawn probe form of the legacy worktree matches the registry")
	}

	// No repository discoverable: the entry mirrors verbatim (fail-closed
	// pairing with the trust check's own degraded forms).
	plain := filepath.Join(t.TempDir(), "no-repo")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	gittrust.Clear()
	plainPath := plain
	injectLegacyGitSecurityEntry(t, f, &plainPath, nil)
	if !gittrust.IsTrusted(plain) {
		t.Error("expected an unresolvable entry to mirror verbatim")
	}
	if len(gittrust.Snapshot()) == 0 {
		t.Error("expected the verbatim entry in the registry")
	}
}

// TestTrustGitRepo_IdempotentAcrossLegacyWorktreeEntry pins idempotency:
// trusting a repository whose trust entry a pre-unification build keyed on
// the worktree path is a no-op — no duplicate record for the same
// repository. A duplicate would leave commits keyed on the new form
// suppressed and re-fire the intake warning until a second Trust click,
// contradicting "trusting any path inside a repository trusts that whole
// repository".
func TestTrustGitRepo_IdempotentAcrossLegacyWorktreeEntry(t *testing.T) {
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	root, wt := worktreeTrustFixture(t)
	f, _, _ := newTestAPI(t)
	wtPath := wt
	injectLegacyGitSecurityEntry(t, f, &wtPath, nil)

	if err := f.TrustGitRepo(root); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}
	got := f.GetTrustedGitRepos()
	if len(got) != 1 {
		t.Fatalf("trusted repos after re-trust = %v, want exactly the one legacy entry (no duplicate)", got)
	}
	if got[0] != wt {
		t.Errorf("trusted entry = %q, want the untouched legacy worktree key %q", got[0], wt)
	}
	if !f.gitRepoTrusted(root) || !f.gitRepoTrusted(wt) {
		t.Error("the legacy entry must cover the whole repository through the probe forms")
	}
}

// TestHardenGitRepo_IdempotentAcrossLegacyWorktreeEntry pins the hardening
// side of the same idempotency: hardening a repository whose hardened entry
// is keyed on the legacy worktree form appends no duplicate.
func TestHardenGitRepo_IdempotentAcrossLegacyWorktreeEntry(t *testing.T) {
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	root, wt := worktreeTrustFixture(t)
	f, _, _ := newTestAPI(t)
	wtPath := wt
	injectLegacyGitSecurityEntry(t, f, nil, &wtPath)

	if err := f.HardenGitRepo(root); err != nil {
		t.Fatalf("HardenGitRepo: %v", err)
	}
	got := f.GetHardenGitRepos()
	if len(got) != 1 {
		t.Fatalf("harden repos after re-harden = %v, want exactly the one legacy entry (no duplicate)", got)
	}
	if got[0] != wt {
		t.Errorf("hardened entry = %q, want the untouched legacy worktree key %q", got[0], wt)
	}
}

// TestNotifyGitConfigRisk_TrustedRepoDriftEvictsAndWarns pins acceptance (3)
// and (4): a trusted repo with an unchanged config stays silent; once its
// config changes the next open evicts the trust (raw git disabled again) and
// emits a warning carrying a drift reason and a diff of the change.
func TestNotifyGitConfigRisk_TrustedRepoDriftEvictsAndWarns(t *testing.T) {
	dir := t.TempDir()
	writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/evil\n")

	f, _, _ := newTestAPI(t)
	if err := f.TrustGitRepo(dir); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}

	// Unchanged config: no event.
	rec := newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)
	if rec.fired {
		t.Error("expected no event for a trusted repo with an unchanged config")
	}

	// Change the config: evict + warn with diff + reason.
	writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/evil\n\thooksPath = .evil-hooks\n")
	rec = newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)
	if !rec.fired {
		t.Fatal("expected project:git_config_risk after the trusted config changed")
	}
	if rec.data.Reason == "" {
		t.Error("expected a drift reason in the payload")
	}
	if rec.data.Diff == "" {
		t.Error("expected a diff in the payload")
	}
	// The drift diff is SEMANTIC now: one canonical (lowercased-key) line per
	// dangerous record, not the raw file text.
	if !strings.Contains(rec.data.Diff, "[core] hookspath = .evil-hooks") {
		t.Errorf("diff should mention the added key canonically, got: %q", rec.data.Diff)
	}
	if got := f.GetTrustedGitRepos(); len(got) != 0 {
		t.Errorf("trusted repos after drift = %v, want empty (evicted)", got)
	}
	if gittrust.IsTrusted(dir) {
		t.Error("expected the repo to be unregistered from the git trust registry after drift")
	}
}

// TestNotifyGitConfigRisk_TrustedRepoDriftCoversAttributes pins acceptance (5):
// the trust snapshot (and therefore the drift diff) covers the attribute
// routing sources, not just the .git/config file — changing info/attributes
// after trust must be detected and shown in the diff.
func TestNotifyGitConfigRisk_TrustedRepoDriftCoversAttributes(t *testing.T) {
	dir := t.TempDir()
	writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/evil\n")
	infoPath := filepath.Join(dir, ".git", "info", "attributes")
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(infoPath, []byte("*.bin filter=lfs\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, _, _ := newTestAPI(t)
	if err := f.TrustGitRepo(dir); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}

	if err := os.WriteFile(infoPath, []byte("*.bin filter=lfs\n*.dat filter=evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)
	if !rec.fired {
		t.Fatal("expected project:git_config_risk after the attribute routing changed")
	}
	if !strings.Contains(rec.data.Diff, "info/attributes") {
		t.Errorf("diff should attribute the change to info/attributes, got: %q", rec.data.Diff)
	}
	if !strings.Contains(rec.data.Diff, "filter=evil") {
		t.Errorf("diff should show the added routing line, got: %q", rec.data.Diff)
	}
}

// TestNotifyGitConfigRisk_TrustedRepoDriftCoversIncludes pins the include-hidden
// drift fix at the intake surface: a repo whose config includes another file
// has that file bound into the trust snapshot, so changing only the included
// file after trust evicts the trust and warns with a diff attributing the
// change to the include target.
func TestNotifyGitConfigRisk_TrustedRepoDriftCoversIncludes(t *testing.T) {
	dir := t.TempDir()
	extra := filepath.Join(dir, "extra.conf")
	if err := os.WriteFile(extra, []byte("[filter \"x\"]\n\tclean = /tmp/evil.sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeGitConfig(t, dir, "[include]\n\tpath = "+extra+"\n")

	f, _, _ := newTestAPI(t)
	wantDiagnostic := miscExpectedDiagnostic{
		message: "git config include directive ignored (not followed); config is an incomplete view",
		attrs:   map[string]string{"line": "2", "conditional": "false", "condition": "", "path": extra},
	}
	captureMiscDiagnostics(t, f, wantDiagnostic, wantDiagnostic)
	if err := f.TrustGitRepo(dir); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}

	// Change only the included file, leaving .git/config byte-identical.
	if err := os.WriteFile(extra, []byte("[filter \"x\"]\n\tclean = /tmp/other.sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)
	if !rec.fired {
		t.Fatal("expected project:git_config_risk after the included file changed")
	}
	if rec.data.Reason == "" {
		t.Error("expected a drift reason in the payload")
	}
	if !strings.Contains(rec.data.Diff, "extra.conf") {
		t.Errorf("diff should attribute the change to the include target, got: %q", rec.data.Diff)
	}
	if got := f.GetTrustedGitRepos(); len(got) != 0 {
		t.Errorf("trusted repos after included-file drift = %v, want empty (evicted)", got)
	}
	if gittrust.IsTrusted(dir) {
		t.Error("expected the repo to be unregistered from the git trust registry after included-file drift")
	}
}

// TestOnGitTrustDrift_EvictsAndRewarns pins the spawn-path drift wiring: when
// core/workspace's raw-spawn gate catches a trusted repository whose live
// config no longer reproduces its recorded semantic fingerprint, it evicts
// the registry entry and hands the root to the backend drift handler, which
// must remove the config record (so the next registry sync cannot resurrect
// the evicted trust) and re-emit the same reasoned drift warning the intake
// recheck produces, attributed to the spawn source. A root with no config
// record behind it (removed concurrently) must be a no-op.
func TestOnGitTrustDrift_EvictsAndRewarns(t *testing.T) {
	gittrust.Clear()
	t.Cleanup(func() {
		gittrust.SetDriftHandler(nil)
		gittrust.Clear()
	})

	dir := t.TempDir()
	writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/evil\n")

	f, _, _ := newTestAPI(t)
	if err := f.TrustGitRepo(dir); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}
	if !gittrust.IsTrusted(dir) {
		t.Fatal("fixture: expected the root in the git trust registry after TrustGitRepo")
	}

	// Drift AFTER trust: an fsmonitor where the decision saw none.
	writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/other\n")

	rec := newRiskRecorder(t, f)
	f.onGitTrustDrift(dir)

	if !rec.fired {
		t.Fatal("expected project:git_config_risk from the spawn drift handler")
	}
	if rec.data.Source != GitConfigRiskSourceSpawn {
		t.Errorf("risk source = %q, want %q", rec.data.Source, GitConfigRiskSourceSpawn)
	}
	if rec.data.Reason == "" {
		t.Error("expected a drift reason in the spawn-drift payload")
	}
	if got := f.GetTrustedGitRepos(); len(got) != 0 {
		t.Errorf("trusted repos after spawn drift = %v, want empty (record removed)", got)
	}
	if gittrust.IsTrusted(dir) {
		t.Error("expected the repo to be unregistered from the git trust registry after spawn drift")
	}

	// A root with no config record (already removed concurrently): the
	// registry eviction is the whole job — no re-warning.
	rec.fired = false
	f.onGitTrustDrift(filepath.Join(t.TempDir(), "elsewhere"))
	if rec.fired {
		t.Error("spawn drift for an unregistered root must not emit a risk event")
	}
}

// TestSyncGitTrustRegistry_MirrorsSemanticFingerprint pins that the registry
// mirror carries each record's semantic fingerprint — the identity the spawn
// layer re-checks before every raw git — and that a record without one (a v1
// record awaiting its next-open migration) mirrors fingerprint-less, which
// the spawn layer reads as an unverifiable decision and keeps hardened.
func TestSyncGitTrustRegistry_MirrorsSemanticFingerprint(t *testing.T) {
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	dir := t.TempDir()
	writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/evil\n")

	f, _, _ := newTestAPI(t)
	if err := f.TrustGitRepo(dir); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}

	info, err := workspace.ScanGitConfig(dir)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	info.ResolveIncludes()
	got, ok := gittrust.SemanticFingerprint(dir)
	if !ok {
		t.Fatal("expected a registered semantic fingerprint after TrustGitRepo")
	}
	if got != info.SemanticFingerprint() {
		t.Errorf("registered fingerprint = %q, want the record's semantic fingerprint %q", got, info.SemanticFingerprint())
	}

	// An empty-identity record mirrors without a fingerprint.
	f2 := &FrontendAPI{}
	f2.seedPublished.Store(true)
	f2.config = &config.Config{Security: config.SecurityConfig{TrustedGitRepos: []config.TrustedGitRepo{
		{Path: dir, Fingerprint: "raw", SemanticFingerprint: ""},
	}}}
	f2.syncGitTrustRegistry()
	if fp, ok := gittrust.SemanticFingerprint(dir); !ok || fp != "" {
		t.Errorf("SemanticFingerprint for a record without an identity = %q (ok=%v), want empty (the spawn layer must keep the hardened path)", fp, ok)
	}
}
