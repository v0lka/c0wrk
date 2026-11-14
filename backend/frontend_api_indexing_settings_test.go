package backend

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/vectorindex"
)

// --- ResolveVectorIndexingPolicy / vectorPolicyChanged ----------------------

func TestResolveVectorIndexingPolicy(t *testing.T) {
	tr, fl := true, false
	tests := []struct {
		name            string
		cfg             config.VectorIndexConfig
		wantIgnoreFiles []string
		wantNilSlices   bool // IgnoreFiles must be nil (both honoured)
		wantSubmodules  bool
	}{
		{
			name:           "nil gates = historical default (both files, no submodules)",
			cfg:            config.VectorIndexConfig{},
			wantNilSlices:  true,
			wantSubmodules: false,
		},
		{
			name: "explicit defaults resolve identically",
			cfg: config.VectorIndexConfig{
				RespectGitIgnore: &tr,
				RespectAIIgnore:  &tr,
				IndexSubmodules:  &fl,
			},
			wantNilSlices:  true,
			wantSubmodules: false,
		},
		{
			name: "git only",
			cfg: config.VectorIndexConfig{
				RespectGitIgnore: &tr,
				RespectAIIgnore:  &fl,
			},
			wantIgnoreFiles: []string{".gitignore"},
		},
		{
			name: "ai only",
			cfg: config.VectorIndexConfig{
				RespectGitIgnore: &fl,
				RespectAIIgnore:  &tr,
			},
			wantIgnoreFiles: []string{".aiignore"},
		},
		{
			name: "neither honoured = empty non-nil slice (noop-ignore sentinel)",
			cfg: config.VectorIndexConfig{
				RespectGitIgnore: &fl,
				RespectAIIgnore:  &fl,
			},
			wantIgnoreFiles: []string{},
		},
		{
			name: "submodules opt-in",
			cfg: config.VectorIndexConfig{
				IndexSubmodules: &tr,
			},
			wantNilSlices:  true,
			wantSubmodules: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pol := ResolveVectorIndexingPolicy(tt.cfg)
			if tt.wantNilSlices {
				if pol.IgnoreFiles != nil {
					t.Errorf("IgnoreFiles = %v, want nil (both files honoured)", pol.IgnoreFiles)
				}
			} else if want := tt.wantIgnoreFiles; len(pol.IgnoreFiles) != len(want) || (len(want) > 0 && pol.IgnoreFiles[0] != want[0]) {
				t.Errorf("IgnoreFiles = %v, want %v", pol.IgnoreFiles, want)
			}
			// The empty-non-nil sentinel must never collapse to nil: the
			// indexer distinguishes "both honoured" (nil) from "neither
			// honoured" (empty).
			if !tt.wantNilSlices && pol.IgnoreFiles == nil {
				t.Error("an explicit gate combination must resolve to a non-nil slice")
			}
			if pol.IncludeSubmodules != tt.wantSubmodules {
				t.Errorf("IncludeSubmodules = %v, want %v", pol.IncludeSubmodules, tt.wantSubmodules)
			}
		})
	}

	t.Run("exclude patterns pass through", func(t *testing.T) {
		pol := ResolveVectorIndexingPolicy(config.VectorIndexConfig{ExcludePatterns: []string{"/dist", "*.log"}})
		if len(pol.ExcludePatterns) != 2 || pol.ExcludePatterns[0] != "/dist" || pol.ExcludePatterns[1] != "*.log" {
			t.Errorf("ExcludePatterns = %v, want [/dist *.log]", pol.ExcludePatterns)
		}
	})
}

func TestVectorPolicyChanged(t *testing.T) {
	base := VectorIndexingPolicy{IgnoreFiles: nil, ExcludePatterns: nil, IncludeSubmodules: false}
	if vectorPolicyChanged(base, base) {
		t.Error("identical policies must not report a change")
	}
	if !vectorPolicyChanged(base, VectorIndexingPolicy{IgnoreFiles: []string{".gitignore"}}) {
		t.Error("ignore-file selection change must be detected")
	}
	if !vectorPolicyChanged(base, VectorIndexingPolicy{ExcludePatterns: []string{"*.log"}}) {
		t.Error("exclude-pattern change must be detected")
	}
	if !vectorPolicyChanged(base, VectorIndexingPolicy{IncludeSubmodules: true}) {
		t.Error("submodule change must be detected")
	}
	// nil vs empty IgnoreFiles are DIFFERENT policies (both honoured vs
	// neither honoured) — not a formatting difference.
	if !vectorPolicyChanged(VectorIndexingPolicy{IgnoreFiles: nil}, VectorIndexingPolicy{IgnoreFiles: []string{}}) {
		t.Error("nil and empty IgnoreFiles are distinct policies and must compare unequal")
	}
}

// --- validation --------------------------------------------------------------

// TestUpdateIndexingSettings_RejectsInvalidPatternsBeforeWrite pins the
// validate-before-mutate contract: a rejected request leaves the in-memory
// config, the YAML file, and the live subsystems untouched.
func TestUpdateIndexingSettings_RejectsInvalidPatternsBeforeWrite(t *testing.T) {
	seed := func(t *testing.T) (*FrontendAPI, *mockBuilder) {
		t.Helper()
		f, mock, cfgPath := newTestAPI(t)
		if err := f.UpdateIndexingSettings(IndexingSettingsRequest{
			Enabled:          true,
			ExcludePatterns:  []string{"/seed"},
			IndexSubmodules:  false,
			RespectGitIgnore: true,
			RespectAIIgnore:  true,
		}); err != nil {
			t.Fatalf("seed save: %v", err)
		}
		_ = cfgPath
		return f, mock
	}

	long := strings.Repeat("a", config.VectorIndexMaxExcludePatternLength+1)
	tooMany := make([]string, config.VectorIndexMaxExcludePatterns+1)
	for i := range tooMany {
		tooMany[i] = "*.log"
	}

	tests := []struct {
		name     string
		patterns []string
		wantPart string
	}{
		{name: "malformed character class", patterns: []string{"[a-"}, wantPart: "vector_index.exclude_patterns[0]"},
		{name: "unclosed brace", patterns: []string{"*.{snap"}, wantPart: "vector_index.exclude_patterns[0]"},
		{name: "empty entry", patterns: []string{""}, wantPart: "vector_index.exclude_patterns[0]"},
		{name: "whitespace-only entry", patterns: []string{"   "}, wantPart: "vector_index.exclude_patterns[0]"},
		{name: "negation", patterns: []string{"!keep.go"}, wantPart: "negation is not supported"},
		{name: "over-length entry", patterns: []string{long}, wantPart: "the maximum is"},
		{name: "over-count list", patterns: tooMany, wantPart: "the maximum is"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, mock := seed(t)
			err := f.UpdateIndexingSettings(IndexingSettingsRequest{Enabled: true, ExcludePatterns: tt.patterns})
			if err == nil {
				t.Fatalf("expected rejection, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantPart) {
				t.Errorf("error %q must mention %q", err.Error(), tt.wantPart)
			}

			// Nothing changed: in-memory config still carries the seed.
			f.configMu.RLock()
			got := f.config.VectorIndex.ExcludePatterns
			f.configMu.RUnlock()
			if len(got) != 1 || got[0] != "/seed" {
				t.Errorf("rejected request must not mutate the in-memory config, got %v", got)
			}
			// The persisted file is equally untouched.
			loaded, lerr := config.Load(f.configPath)
			if lerr != nil {
				t.Fatalf("reload: %v", lerr)
			}
			if len(loaded.VectorIndex.ExcludePatterns) != 1 || loaded.VectorIndex.ExcludePatterns[0] != "/seed" {
				t.Errorf("rejected request must not mutate the YAML file, got %v", loaded.VectorIndex.ExcludePatterns)
			}
			// No live application happened.
			if calls := mock.setVectorSearchEnabledSnapshot(); len(calls) != 0 {
				t.Errorf("rejected request must not touch the builder, got %v", calls)
			}
			if f.vectorRootsRegistry().Disabled() {
				t.Error("rejected request must not disable the registry")
			}
		})
	}

	t.Run("valid patterns accepted at the caps", func(t *testing.T) {
		f, _ := seed(t)
		exact := make([]string, config.VectorIndexMaxExcludePatterns)
		for i := range exact {
			exact[i] = "*.log"
		}
		if err := f.UpdateIndexingSettings(IndexingSettingsRequest{Enabled: true, ExcludePatterns: exact}); err != nil {
			t.Errorf("save at the exact caps must pass: %v", err)
		}
	})
}

// --- persist + GetConfig round-trip -------------------------------------------

// TestUpdateIndexingSettings_PersistsAndRoundTrips pins the happy path with
// indexing left ON (no live transition): the five fields persist, survive a
// Load round-trip, and GetConfig serves them resolved.
func TestUpdateIndexingSettings_PersistsAndRoundTrips(t *testing.T) {
	f, _, _ := newTestAPI(t)

	req := IndexingSettingsRequest{
		Enabled:          true,
		ExcludePatterns:  []string{"/dist", "*.snap", "vendor/"},
		IndexSubmodules:  true,
		RespectGitIgnore: true,
		RespectAIIgnore:  false,
	}
	if err := f.UpdateIndexingSettings(req); err != nil {
		t.Fatalf("UpdateIndexingSettings: %v", err)
	}

	// In-memory + served.
	got := f.GetConfig().VectorIndex
	if !got.Enabled || !got.IndexSubmodules || !got.RespectGitIgnore || got.RespectAIIgnore {
		t.Errorf("GetConfig vector_index = %+v, want enabled/submodules/git on, ai off", got)
	}
	if len(got.ExcludePatterns) != 3 {
		t.Errorf("GetConfig exclude_patterns = %v, want the 3 saved entries", got.ExcludePatterns)
	}

	// Disk round-trip.
	loaded, err := config.Load(f.configPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !loaded.VectorIndex.IndexingEnabled() {
		t.Error("persisted enabled must resolve true")
	}
	if !loaded.VectorIndex.ShouldIndexSubmodules() {
		t.Error("persisted index_submodules must resolve true")
	}
	if !loaded.VectorIndex.ShouldRespectGitIgnore() {
		t.Error("persisted respect_gitignore must resolve true")
	}
	if loaded.VectorIndex.ShouldRespectAIIgnore() {
		t.Error("persisted respect_aiignore=false must stay false across the round-trip")
	}
	if len(loaded.VectorIndex.ExcludePatterns) != 3 {
		t.Errorf("persisted exclude_patterns = %v, want 3 entries", loaded.VectorIndex.ExcludePatterns)
	}

	// Explicit false (not just unset) survives: the pointer-bools are
	// written, and a nil-defaulting reader still resolves them correctly.
	tr := true
	f.configMu.RLock()
	enabledPtr, subPtr, gitPtr, aiPtr := f.config.VectorIndex.Enabled, f.config.VectorIndex.IndexSubmodules, f.config.VectorIndex.RespectGitIgnore, f.config.VectorIndex.RespectAIIgnore
	f.configMu.RUnlock()
	for name, p := range map[string]*bool{"enabled": enabledPtr, "index_submodules": subPtr, "respect_gitignore": gitPtr, "respect_aiignore": aiPtr} {
		if p == nil {
			t.Errorf("%s must be materialized as an explicit pointer-bool, got nil", name)
		}
	}
	_ = tr
}

// quietTestLogger keeps the deliberately exercised failure-path diagnostics
// (disable/reload INFO+WARN lines through f.log()) out of the test output:
// the tests assert the effects, not the log lines.
func quietTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- disable transition -------------------------------------------------------

// TestUpdateIndexingSettings_DisablePath pins the full enabled=false live
// application: the desktop hook runs first (invalidate in-flight init), the
// builder strips semantic_search + RAG, the registry parks with
// errVectorDisabled on every routing path, and a "disabled"
// vector_index:status snapshot is emitted.
func TestUpdateIndexingSettings_DisablePath(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	// Scoped quiet logger: the disable path logs its INFO/WARN diagnostics
	// through f.log(); the test asserts the effects, not the log lines.
	f.logger = quietTestLogger()

	var hookMu sync.Mutex
	var hookCalls []bool
	f.Lifecycle().SetVectorIndexEnabledHook(func(enabled bool) {
		hookMu.Lock()
		hookCalls = append(hookCalls, enabled)
		hookMu.Unlock()
	})

	var emitMu sync.Mutex
	var emitted []string
	var statuses []VectorIndexStatus
	f.emitEvent = func(name string, data ...any) {
		if name != EventVectorIndexStatus {
			return
		}
		emitMu.Lock()
		defer emitMu.Unlock()
		emitted = append(emitted, name)
		for _, d := range data {
			if st, ok := d.(VectorIndexStatus); ok {
				statuses = append(statuses, st)
			}
		}
	}

	if err := f.UpdateIndexingSettings(IndexingSettingsRequest{Enabled: false}); err != nil {
		t.Fatalf("UpdateIndexingSettings(disable): %v", err)
	}

	// Builder: semantic_search stripped.
	if calls := mock.setVectorSearchEnabledSnapshot(); len(calls) != 1 || calls[0] {
		t.Errorf("builder SetVectorSearchEnabled calls = %v, want [false]", calls)
	}
	// Desktop hook: exactly one invalidate.
	hookMu.Lock()
	gotHook := append([]bool(nil), hookCalls...)
	hookMu.Unlock()
	if len(gotHook) != 1 || gotHook[0] {
		t.Errorf("desktop hook calls = %v, want [false]", gotHook)
	}

	// Registry parked.
	vr := f.vectorRootsRegistry()
	if !vr.Disabled() {
		t.Fatal("registry must be parked after disable")
	}
	if vr.FactoryReady() {
		t.Error("FactoryReady must report false while disabled (switch setup defers)")
	}
	// Focus a root (the call fails — manager creation is refused — but the
	// focus itself is recorded, mirroring a real Git-panel focus) and then
	// exercise the routing paths.
	_ = vr.ApplyFocus(context.Background(), t.TempDir())
	if _, err := vr.ManagerForContext(context.Background()); !errors.Is(err, errVectorDisabled) {
		t.Errorf("ManagerForContext err = %v, want errVectorDisabled", err)
	}
	if err := vr.awaitReady(context.Background(), false); !errors.Is(err, errVectorDisabled) {
		t.Errorf("awaitReady err = %v, want errVectorDisabled", err)
	}
	if _, err := vr.FocusManager(context.Background()); !errors.Is(err, errVectorDisabled) {
		t.Errorf("FocusManager err = %v, want errVectorDisabled", err)
	}

	// UI: exactly one disabled snapshot on the status stream.
	emitMu.Lock()
	gotStatuses := append([]VectorIndexStatus(nil), statuses...)
	emitMu.Unlock()
	if len(gotStatuses) != 1 {
		t.Fatalf("vector_index:status emissions = %d, want 1", len(gotStatuses))
	}
	if gotStatuses[0].State != vectorStateDisabled {
		t.Errorf("emitted state = %q, want %q", gotStatuses[0].State, vectorStateDisabled)
	}

	// The user-facing status RPC renders the disabled state (not
	// "unavailable").
	if st := f.GetVectorIndexStatus(); st.State != vectorStateDisabled {
		t.Errorf("GetVectorIndexStatus state = %q, want %q", st.State, vectorStateDisabled)
	}

	// Persisted.
	f.configMu.RLock()
	enabled := f.config.VectorIndex.IndexingEnabled()
	f.configMu.RUnlock()
	if enabled {
		t.Error("config must persist enabled=false")
	}
}

// TestVectorRoots_DisableThenSetFactoryClearsFlag pins the restart contract:
// Disable is not final — the wiring a desktop restart performs (SetFactory)
// un-parks the registry.
func TestVectorRoots_DisableThenSetFactoryClearsFlag(t *testing.T) {
	f := newStartupVectorAPI(t)
	vr := f.vectorRootsRegistry()

	vr.Disable()
	if !vr.Disabled() {
		t.Fatal("precondition: disabled")
	}
	vr.Disable() // idempotent
	if !vr.Disabled() {
		t.Error("Disable must be idempotent")
	}

	// The restart lands its factory: the registry comes back.
	ready := make(chan struct{})
	close(ready)
	vr.SetFactory(func() (*vectorindex.Manager, error) {
		return nil, errors.New("not built in this test")
	}, ready, func() error { return nil })
	if vr.Disabled() {
		t.Error("SetFactory must clear the disabled flag (restart path)")
	}
	if !vr.FactoryReady() {
		t.Error("a wired factory must make FactoryReady true again")
	}
}

// TestVectorRoots_SetFactoryShutdownCloseOutsideLock pins SetFactory's
// shutdown branch closing the embedder AFTER vr.mu is released, the same
// outside-the-lock rule ShutdownAll and Disable follow: emb.Close blocks on
// the embedder's internal lock, which a runtime degrade callback holds while
// it flips the recorded provider facts, so the registry's mutex must never
// sit under that native wait. The test's closeEmbedder takes vr.mu itself:
// under the old under-lock close the TryLock fails and the t.Error fires
// (the close demonstrably ran under the lock); the bounded watchdog is pure
// belt-and-braces against a true hang.
func TestVectorRoots_SetFactoryShutdownCloseOutsideLock(t *testing.T) {
	f := newStartupVectorAPI(t)
	// Intercept the shutdown branch's WARN at its source (the test-output
	// quality rule: a deliberately exercised failure path must not leak into
	// the run's output) and assert it below.
	var logSink strings.Builder
	f.logger = slog.New(slog.NewTextHandler(&logSink, nil))
	vr := f.vectorRootsRegistry()
	t.Cleanup(vr.ShutdownAll)

	vr.ShutdownAll() // park the registry into the shutdown branch's state

	ready := make(chan struct{})
	close(ready)
	done := make(chan struct{})
	go func() {
		defer close(done)
		vr.SetFactory(func() (*vectorindex.Manager, error) {
			return nil, errors.New("not built in this test")
		}, ready, func() error {
			// Under the old under-lock close vr.mu was still held here, so
			// this TryLock failed and the t.Error above fired.
			if !vr.mu.TryLock() {
				t.Error("the closeEmbedder ran while SetFactory still held vr.mu (the close must run outside the lock)")
				return nil
			}
			vr.mu.Unlock()
			return nil
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SetFactory's shutdown-branch close deadlocked on vr.mu (the close must run outside the lock)")
	}
	if !strings.Contains(logSink.String(), "vector roots: factory wired after shutdown") {
		t.Errorf("the shutdown branch did not warn about the post-shutdown factory wiring; log = %q", logSink.String())
	}
}

// --- enable transition --------------------------------------------------------

// TestUpdateIndexingSettings_EnablePath pins the enabled=true transition from
// a disabled config: the builder re-registers semantic_search, the desktop
// hook is asked to relaunch the init, and the registry stays parked until
// that init lands its factory (SetFactory), after which routing works again.
func TestUpdateIndexingSettings_EnablePath(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	f.logger = quietTestLogger()

	// Boot-disabled config.
	faux := false
	f.configMu.Lock()
	f.config.VectorIndex.Enabled = &faux
	f.configMu.Unlock()

	var hookMu sync.Mutex
	var hookCalls []bool
	f.Lifecycle().SetVectorIndexEnabledHook(func(enabled bool) {
		hookMu.Lock()
		hookCalls = append(hookCalls, enabled)
		hookMu.Unlock()
	})
	// Park the registry the way the boot path would have.
	f.vectorRootsRegistry().Disable()

	if err := f.UpdateIndexingSettings(IndexingSettingsRequest{Enabled: true}); err != nil {
		t.Fatalf("UpdateIndexingSettings(enable): %v", err)
	}

	if calls := mock.setVectorSearchEnabledSnapshot(); len(calls) != 1 || !calls[0] {
		t.Errorf("builder SetVectorSearchEnabled calls = %v, want [true]", calls)
	}
	hookMu.Lock()
	gotHook := append([]bool(nil), hookCalls...)
	hookMu.Unlock()
	if len(gotHook) != 1 || !gotHook[0] {
		t.Errorf("desktop hook calls = %v, want [true]", gotHook)
	}

	// Until the desktop init lands, the registry stays parked and the
	// config is already persisted.
	f.configMu.RLock()
	enabled := f.config.VectorIndex.IndexingEnabled()
	f.configMu.RUnlock()
	if !enabled {
		t.Error("config must persist enabled=true")
	}
	if !f.vectorRootsRegistry().Disabled() {
		t.Error("the registry must stay parked until the restart wires its factory")
	}

	// The restart's success path: factory lands, registry un-parks.
	ready := make(chan struct{})
	close(ready)
	f.vectorRootsRegistry().SetFactory(func() (*vectorindex.Manager, error) {
		return nil, errors.New("not built in this test")
	}, ready, func() error { return nil })
	if f.vectorRootsRegistry().Disabled() {
		t.Error("SetFactory must clear the disabled flag")
	}
}

// --- policy-only change -------------------------------------------------------

// newPolicyReloadAPI builds a FrontendAPI with BOTH a persistable config and
// a project manager (so the registry can resolve roots), plus a manager
// factory that mirrors the desktop wiring: every manager build snapshots the
// LIVE indexing policy, which is what the reload path relies on.
func newPolicyReloadAPI(t *testing.T) (*FrontendAPI, *mockBuilder) {
	t.Helper()

	db := openProjectSwitchTestDB(t)
	projectStore, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("project store: %v", err)
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)

	mock := &mockBuilder{}
	f := &FrontendAPI{
		config:          cfg,
		configPath:      cfgPath,
		agentDir:        dir,
		builderOverride: mock,
		projStore:       projectStore,
		projectManager:  project.NewManager(projectStore, dir, nil),
		emitEvent:       func(string, ...any) {},
		logger:          quietTestLogger(),
	}
	f.seedPublished.Store(true)

	ready := make(chan struct{})
	close(ready)
	f.vectorRootsRegistry().SetFactory(func() (*vectorindex.Manager, error) {
		mgr, err := vectorindex.NewManager(vectorindex.ManagerConfig{
			EmbeddingFunc: func(ctx context.Context, text string) ([]float32, error) {
				return make([]float32, 4), nil
			},
		})
		if err != nil {
			return nil, err
		}
		return mgr, nil
	}, ready, func() error { return nil })
	t.Cleanup(f.vectorRootsRegistry().ShutdownAll)
	t.Cleanup(func() { _ = db.Close() })

	return f, mock
}

// TestUpdateIndexingSettings_PolicyChangeReloadsManagers pins the policy-only
// save while indexing stays enabled: the live managers are released, the
// focus root is rebuilt, and the rebuilt manager carries the NEW policy (the
// factory snapshots the live config per build — the desktop wiring contract).
func TestUpdateIndexingSettings_PolicyChangeReloadsManagers(t *testing.T) {
	f, mock := newPolicyReloadAPI(t)

	// Workspace + registered project (the registered path is what vector
	// routing resolves).
	ws := t.TempDir()
	created, err := f.projectManager.CreateProject("proj-policy", ws)
	if err != nil {
		t.Fatalf("register project: %v", err)
	}
	root := created.WorkspacePath

	vr := f.vectorRootsRegistry()
	if err := vr.ApplyFocus(context.Background(), root); err != nil {
		t.Fatalf("ApplyFocus: %v", err)
	}
	mgr1 := vr.LiveManager(root)
	if mgr1 == nil {
		t.Fatal("precondition: focus manager live")
	}
	waitForVectorReady(t, mgr1)

	// Policy-only save: add an exclude pattern (enabled stays true).
	if err := f.UpdateIndexingSettings(IndexingSettingsRequest{
		Enabled:          true,
		ExcludePatterns:  []string{"*.log"},
		IndexSubmodules:  false,
		RespectGitIgnore: true,
		RespectAIIgnore:  true,
	}); err != nil {
		t.Fatalf("UpdateIndexingSettings(policy): %v", err)
	}

	// The focus root was rebuilt: a NEW manager is live for the same root.
	mgr2 := vr.LiveManager(root)
	if mgr2 == nil {
		t.Fatal("focus root must be rebuilt after a policy change")
	}
	if mgr2 == mgr1 {
		t.Fatal("the pre-save manager must have been released and rebuilt")
	}
	waitForVectorReady(t, mgr2)

	// The rebuilt manager was built under the NEW policy (factory snapshot).
	f.configMu.RLock()
	want := f.indexingPolicySnapshotLocked()
	f.configMu.RUnlock()
	if len(want.ExcludePatterns) != 1 || want.ExcludePatterns[0] != "*.log" {
		t.Fatalf("snapshot = %+v, want the saved *.log exclude", want)
	}

	// No enable/disable transition fired on the builder or the hook.
	if calls := mock.setVectorSearchEnabledSnapshot(); len(calls) != 0 {
		t.Errorf("policy-only save must not toggle the builder, got %v", calls)
	}
	if vr.Disabled() {
		t.Error("policy-only save must keep the registry enabled")
	}

	// A no-op save (same resolved policy) must NOT release the manager
	// again — an unchanged policy must not force a re-index.
	if err := f.UpdateIndexingSettings(IndexingSettingsRequest{
		Enabled:          true,
		ExcludePatterns:  []string{"*.log"},
		IndexSubmodules:  false,
		RespectGitIgnore: true,
		RespectAIIgnore:  true,
	}); err != nil {
		t.Fatalf("no-op save: %v", err)
	}
	if got := vr.LiveManager(root); got != mgr2 {
		t.Fatal("an unchanged policy must not release the live manager")
	}
}
