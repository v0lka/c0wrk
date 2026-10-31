package backend

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/workspace"
)

// ListSkills returns lightweight descriptors for all available skills,
// including the active project's .agents/skills entries. Cache-served;
// invalidated on project switches and by the skill directory watchers.
// See specs/domains/capabilities.md.
func (f *FrontendAPI) ListSkills() []SkillDescriptorDTO {
	b := f.builder()
	if b == nil {
		return []SkillDescriptorDTO{}
	}

	f.activeProjectMu.RLock()
	wsPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()

	projectSkillDir := ""
	if wsPath != "" {
		projectSkillDir = config.ProjectSkillsPath(wsPath)
	}

	gen := atomic.LoadUint64(&f.skillCacheGen)
	f.skillCacheMu.Lock()
	if f.skillCache != nil && f.skillCacheGenSnapshot == gen && f.skillCacheProjectDir == projectSkillDir {
		cached := f.skillCache
		f.skillCacheMu.Unlock()
		return cached
	}
	f.skillCacheMu.Unlock()

	descriptors := b.GetSkillDescriptors(projectSkillDir)
	result := make([]SkillDescriptorDTO, len(descriptors))
	for i, d := range descriptors {
		result[i] = SkillDescriptorDTO{Name: d.Name, Description: d.Description}
	}

	f.skillCacheMu.Lock()
	f.skillCache = result
	f.skillCacheGenSnapshot = gen
	f.skillCacheProjectDir = projectSkillDir
	f.skillCacheMu.Unlock()

	return result
}

// RefreshSkills forces re-discovery of every skill directory and returns the
// fresh descriptors — the Capabilities tab's manual reload. Watchers never
// see directories created after startup; the generation bump always re-scans.
// See specs/domains/capabilities.md.
func (f *FrontendAPI) RefreshSkills() []SkillDescriptorDTO {
	f.invalidateSkillCache()
	return f.ListSkills()
}

// ResolveSkillResourcePath resolves a read_skill_resource tool call's
// (skill, path) arguments to the absolute file the tool actually read, so a
// tool-call card can open the resource. The builder runs the same discovery
// set and SafeResolvePath traversal guard as the tool itself; refusals
// (no builder, unknown skill, traversal) degrade the card to plain text.
// See specs/decisions/095-chat-file-reference-path-semantics.md.
func (f *FrontendAPI) ResolveSkillResourcePath(skill, path string) (string, error) {
	b := f.builder()
	if b == nil {
		return "", errors.New("backend is not ready")
	}

	// Same workspace snapshot ListSkills uses: the active project's
	// .agents/skills directory joins the front of the discovery set.
	f.activeProjectMu.RLock()
	wsPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()

	projectSkillDir := ""
	if wsPath != "" {
		projectSkillDir = config.ProjectSkillsPath(wsPath)
	}

	return b.ResolveSkillResourcePath(projectSkillDir, skill, path)
}

// The c0wrk-owned skill packs (research-* and study-paper) are seeded into the
// GLOBAL skills dir once per launch by seedGlobalPacks; the project-local
// .agents/skills dir stays user-owned.
// See specs/decisions/056-research-always-on-global-seeding.md.

func (f *FrontendAPI) invalidateSkillCache() {
	atomic.AddUint64(&f.skillCacheGen, 1)
}

// startSkillsWatchers watches each existing skill directory (debounced):
// cache invalidation plus a skills:changed event. Missing directories are
// skipped until an app restart — the accepted trade-off.
func (f *FrontendAPI) startSkillsWatchers(dirs []string) {
	f.seedAcquire()
	f.skillWatchersMu.Lock()
	defer f.skillWatchersMu.Unlock()

	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		watcher, err := workspace.NewWatcher(dir, func(_ []string) {
			f.invalidateSkillCache()
			f.emitEvent(EventSkillsChanged, nil)
		}, f.log())
		if err != nil {
			f.log().Warn("failed to watch skill directory", "dir", dir, "error", err)
			continue
		}
		// Also watch each immediate subdirectory: on platforms where fsnotify
		// is not recursive (Linux inotify) SKILL.md edits are invisible
		// otherwise.
		if entries, err := os.ReadDir(dir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					_ = watcher.WatchDir(filepath.Join(dir, entry.Name()))
				}
			}
		}
		f.skillWatchers = append(f.skillWatchers, watcher)
	}
}

func (f *FrontendAPI) closeSkillsWatchers() {
	f.skillWatchersMu.Lock()
	defer f.skillWatchersMu.Unlock()
	for _, w := range f.skillWatchers {
		_ = w.Close()
	}
	f.skillWatchers = nil
}
