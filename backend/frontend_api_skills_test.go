package backend

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/sp4rk/skills"
)

// writeSkillMD writes a minimal valid SKILL.md into dir.
func writeSkillMD(t *testing.T, dir, name, desc string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	content := "---\nname: " + name + "\ndescription: \"" + desc + "\"\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile SKILL.md: %v", err)
	}
}

// TestStartSkillsWatchers_SkipsMissingDirs verifies that non-existent skill
// directories are silently skipped without errors.
func TestStartSkillsWatchers_SkipsMissingDirs(t *testing.T) {
	f := &FrontendAPI{
		emitEvent: func(string, ...any) {},
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	f.startSkillsWatchers([]string{"/nonexistent/skills/dir"})
	if len(f.skillWatchers) != 0 {
		t.Errorf("expected 0 watchers for non-existent dir, got %d", len(f.skillWatchers))
	}
}

// TestStartSkillsWatchers_InvalidatesCacheOnChange verifies that a file change
// in a watched skill directory invalidates the skill cache and emits
// skills:changed so the frontend autocomplete refreshes.
func TestStartSkillsWatchers_InvalidatesCacheOnChange(t *testing.T) {
	skillDir := t.TempDir()
	// Create a skill subdirectory with a SKILL.md so the watcher has
	// something to monitor inside the skill dir.
	skillSubdir := filepath.Join(skillDir, "my-skill")
	writeSkillMD(t, skillSubdir, "my-skill", "A test skill.")

	var emitted atomic.Int32
	f := &FrontendAPI{
		emitEvent: func(name string, _ ...any) {
			if name == EventSkillsChanged {
				emitted.Add(1)
			}
		},
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	t.Cleanup(f.closeSkillsWatchers)

	f.startSkillsWatchers([]string{skillDir})
	if len(f.skillWatchers) != 1 {
		t.Fatalf("expected 1 skill watcher, got %d", len(f.skillWatchers))
	}

	// Populate the skill cache via ListSkills with a mock builder.
	mock := &mockBuilder{
		getSkillDescriptorsRes: []skills.SkillDescriptor{
			{Name: "my-skill", Description: "A test skill."},
		},
	}
	f.builderOverride = mock
	result := f.ListSkills()
	if len(result) != 1 || result[0].Name != "my-skill" {
		t.Fatalf("ListSkills initial: got %v", result)
	}
	if mock.getSkillDescriptorsCalls != 1 {
		t.Fatalf("expected 1 GetSkillDescriptors call, got %d", mock.getSkillDescriptorsCalls)
	}

	// Modify a file inside the skill directory to trigger the watcher.
	skillMD := filepath.Join(skillSubdir, "SKILL.md")
	if err := os.WriteFile(skillMD, []byte("---\nname: my-skill\ndescription: \"Updated.\"\n---\n\nBody.\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Wait for the debounced skills:changed emission (fsnotify latency on
	// macOS + 200ms debounce in the watcher).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if emitted.Load() >= 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if emitted.Load() < 1 {
		t.Fatal("expected skills:changed emission after skill file modification")
	}

	// The cache must have been invalidated — the next ListSkills call
	// should re-invoke GetSkillDescriptors.
	mock.getSkillDescriptorsRes = []skills.SkillDescriptor{
		{Name: "my-skill", Description: "Updated."},
	}
	result = f.ListSkills()
	if len(result) != 1 || result[0].Description != "Updated." {
		t.Fatalf("ListSkills after change: got %v", result)
	}
	if mock.getSkillDescriptorsCalls != 2 {
		t.Fatalf("expected 2 GetSkillDescriptors calls (cache invalidated), got %d", mock.getSkillDescriptorsCalls)
	}
}

// TestRefreshSkills_ForcesRediscovery verifies that RefreshSkills bypasses
// the skill cache: every call re-invokes GetSkillDescriptors and returns the
// fresh catalog even when no watcher fired — the manual re-discovery path
// behind the Capabilities tab's refresh button.
func TestRefreshSkills_ForcesRediscovery(t *testing.T) {
	mock := &mockBuilder{
		getSkillDescriptorsRes: []skills.SkillDescriptor{
			{Name: "my-skill", Description: "A test skill."},
		},
	}
	f := &FrontendAPI{emitEvent: func(string, ...any) {}}
	f.builderOverride = mock

	if got := f.ListSkills(); len(got) != 1 || got[0].Name != "my-skill" {
		t.Fatalf("ListSkills initial: got %v", got)
	}
	// A plain second ListSkills is served from the cache.
	if got := f.ListSkills(); len(got) != 1 {
		t.Fatalf("ListSkills cached: got %v", got)
	}
	if mock.getSkillDescriptorsCalls != 1 {
		t.Fatalf("expected 1 GetSkillDescriptors call after cached reload, got %d", mock.getSkillDescriptorsCalls)
	}

	// A skill appears on disk (no watcher for it); RefreshSkills must see it.
	mock.getSkillDescriptorsRes = append(mock.getSkillDescriptorsRes,
		skills.SkillDescriptor{Name: "new-skill", Description: "Created after startup."})
	fresh := f.RefreshSkills()
	if mock.getSkillDescriptorsCalls != 2 {
		t.Fatalf("expected RefreshSkills to re-scan (2 GetSkillDescriptors calls), got %d", mock.getSkillDescriptorsCalls)
	}
	if len(fresh) != 2 || fresh[1].Name != "new-skill" {
		t.Fatalf("RefreshSkills: got %v", fresh)
	}
	// The re-scan result replaces the cache: the next ListSkills is fresh too.
	if got := f.ListSkills(); len(got) != 2 || got[1].Name != "new-skill" {
		t.Fatalf("ListSkills after refresh: got %v", got)
	}
	if mock.getSkillDescriptorsCalls != 2 {
		t.Fatalf("expected the refresh result to be cached (still 2 calls), got %d", mock.getSkillDescriptorsCalls)
	}
}

// TestCloseSkillsWatchers_Idempotent verifies that closeSkillsWatchers can be
// called multiple times safely.
func TestCloseSkillsWatchers_Idempotent(t *testing.T) {
	skillDir := t.TempDir()
	f := &FrontendAPI{
		emitEvent: func(string, ...any) {},
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	f.startSkillsWatchers([]string{skillDir})
	f.closeSkillsWatchers()
	f.closeSkillsWatchers() // must not panic
	if len(f.skillWatchers) != 0 {
		t.Errorf("expected 0 watchers after close, got %d", len(f.skillWatchers))
	}
}

// TestListSkills_DiscoverySet pins the RPC layer's side of the multi-directory
// discovery: ListSkills forwards the workspace's whole discovery set (the
// builder's SkillDirsForWorkspace — project .agents/skills plus the SDD
// framework's declared dirs such as .claude/skills) to GetSkillDescriptors, and
// the cache is keyed on that set, so a project/session change that alters the
// set re-scans even when no watcher fired and no generation bump happened.
func TestListSkills_DiscoverySet(t *testing.T) {
	wsOne := filepath.Join(string(filepath.Separator), "tmp", "ws-one")
	wsTwo := filepath.Join(string(filepath.Separator), "tmp", "ws-two")
	dirsOne := []string{
		config.ProjectSkillsPath(wsOne),
		filepath.Join(wsOne, ".claude", "skills"),
	}
	dirsTwo := []string{
		config.ProjectSkillsPath(wsTwo),
		filepath.Join(wsTwo, ".claude", "skills"),
	}

	mock := &mockBuilder{
		skillDirsRes:           dirsOne,
		getSkillDescriptorsRes: []skills.SkillDescriptor{{Name: "openspec-propose", Description: "Propose a change"}},
	}
	f := &FrontendAPI{emitEvent: func(string, ...any) {}}
	f.seedPublished.Store(true)
	f.builderOverride = mock
	f.activeProjectPath = wsOne

	got := f.ListSkills()
	if len(got) != 1 || got[0].Name != "openspec-propose" {
		t.Fatalf("ListSkills: got %v, want the .claude/skills entry", got)
	}
	if !reflect.DeepEqual(mock.getSkillDescriptorsDirs, dirsOne) {
		t.Fatalf("GetSkillDescriptors dirs = %v, want the full discovery set %v",
			mock.getSkillDescriptorsDirs, dirsOne)
	}

	// Second call is cache-served: same set, same generation.
	f.ListSkills()
	if mock.getSkillDescriptorsCalls != 1 {
		t.Fatalf("expected the second ListSkills to be cached (1 scan), got %d", mock.getSkillDescriptorsCalls)
	}

	// A different project contributes a different set — the cache key changes,
	// so the catalog is re-scanned without any explicit invalidation.
	mock.skillDirsRes = dirsTwo
	f.activeProjectPath = wsTwo
	f.ListSkills()
	if mock.getSkillDescriptorsCalls != 2 {
		t.Fatalf("expected a set change to re-scan (2 scans), got %d", mock.getSkillDescriptorsCalls)
	}
	if !reflect.DeepEqual(mock.getSkillDescriptorsDirs, dirsTwo) {
		t.Fatalf("GetSkillDescriptors dirs = %v, want %v", mock.getSkillDescriptorsDirs, dirsTwo)
	}

	// An explicit invalidation (project switch / watcher) still re-scans the
	// unchanged set, so per-directory edits are never served stale.
	f.invalidateSkillCache()
	f.ListSkills()
	if mock.getSkillDescriptorsCalls != 3 {
		t.Fatalf("expected invalidation to re-scan (3 scans), got %d", mock.getSkillDescriptorsCalls)
	}
}

// TestResolveSkillResourcePath_RPCWiring verifies the RPC-level wiring: the
// builder receives the active project's WORKSPACE (the same snapshot ListSkills
// uses — the workspace's full ordered discovery set resolves inside the
// builder) plus the raw (skill, path) arguments, and its result and error are
// propagated verbatim. The catalog resolution itself (precedence, framework
// dirs + SafeResolvePath) is covered by the core builder test
// TestOrchestratorBuilder_ResolveSkillResourcePath.
func TestResolveSkillResourcePath_RPCWiring(t *testing.T) {
	t.Run("no builder", func(t *testing.T) {
		f := &FrontendAPI{emitEvent: func(string, ...any) {}}
		f.seedPublished.Store(true)
		_, err := f.ResolveSkillResourcePath("my-skill", "references/api.md")
		if err == nil {
			t.Fatal("expected an error when the backend is not ready")
		}
	})

	t.Run("with active project", func(t *testing.T) {
		mock := &mockBuilder{resolveSkillResourceRes: "/skills/my-skill/references/api.md"}
		f := &FrontendAPI{emitEvent: func(string, ...any) {}}
		f.seedPublished.Store(true)
		f.builderOverride = mock
		f.activeProjectPath = string(filepath.Separator) + filepath.Join("tmp", "ws")

		got, err := f.ResolveSkillResourcePath("my-skill", "references/api.md")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "/skills/my-skill/references/api.md" {
			t.Fatalf("result not propagated: %q", got)
		}
		if mock.resolveSkillResourceCalls != 1 {
			t.Fatalf("expected 1 builder call, got %d", mock.resolveSkillResourceCalls)
		}
		if mock.resolveSkillResourceArgs[0] != f.activeProjectPath {
			t.Errorf("workspace = %q, want %q", mock.resolveSkillResourceArgs[0], f.activeProjectPath)
		}
		if mock.resolveSkillResourceArgs[1] != "my-skill" || mock.resolveSkillResourceArgs[2] != "references/api.md" {
			t.Errorf("args = %v, want [workspace my-skill references/api.md]", mock.resolveSkillResourceArgs)
		}
	})

	t.Run("without active project", func(t *testing.T) {
		mock := &mockBuilder{resolveSkillResourceRes: "/global/my-skill/SKILL.md"}
		f := &FrontendAPI{emitEvent: func(string, ...any) {}}
		f.seedPublished.Store(true)
		f.builderOverride = mock

		if _, err := f.ResolveSkillResourcePath("my-skill", "SKILL.md"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.resolveSkillResourceArgs[0] != "" {
			t.Errorf("projectSkillDir = %q, want empty", mock.resolveSkillResourceArgs[0])
		}
	})

	t.Run("builder error propagates", func(t *testing.T) {
		wantErr := errors.New(`skill "nope" not found`)
		mock := &mockBuilder{resolveSkillResourceErr: wantErr}
		f := &FrontendAPI{emitEvent: func(string, ...any) {}}
		f.seedPublished.Store(true)
		f.builderOverride = mock

		_, err := f.ResolveSkillResourcePath("nope", "references/api.md")
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
	})
}
