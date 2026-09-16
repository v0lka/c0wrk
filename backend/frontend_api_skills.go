package backend

import (
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/papers"
	"github.com/v0lka/c0wrk/core/workspace"
)

// ListSkills returns lightweight descriptors for all available skills,
// including project-local skills from the active project's .agents/skills directory.
// Results are cached; the cache is invalidated when the active project changes
// or when a skill directory watcher detects a change.
func (f *FrontendAPI) ListSkills() []SkillDescriptorDTO {
	b := f.builder()
	if b == nil {
		return []SkillDescriptorDTO{}
	}

	// Snapshot active project info for cache key.
	f.activeProjectMu.RLock()
	wsPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()

	projectSkillDir := ""
	if wsPath != "" {
		projectSkillDir = config.ProjectSkillsPath(wsPath)
	}

	// Check cache: valid if gen matches and project path hasn't changed.
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

	// Store in cache.
	f.skillCacheMu.Lock()
	f.skillCache = result
	f.skillCacheGenSnapshot = gen
	f.skillCacheProjectDir = projectSkillDir
	f.skillCacheMu.Unlock()

	return result
}

// seedPapersSkillPack materializes the built-in paper-study skill-pack (the
// `study-paper` skill, see core/papers) into the GLOBAL agent skills directory
// (config.SkillsDir(agentDir) = ~/.c0wrk/.agents/skills).
//
// This is "hybrid" global seeding: the skill becomes discoverable by every
// project — including projects and No-Project sessions that never enable
// RESEARCH mode — because the global directory is one of
// config.defaultSkillDirs and is watched by the global skill watchers started
// in NewFrontendAPI. Seeding is idempotent and non-destructive to user edits
// (core/papers classifies by content hash), so it is safe to run on every
// startup. An empty agentDir (e.g. in tests) is a no-op, and a seeding failure
// is logged but never fatal to startup.
func (f *FrontendAPI) seedPapersSkillPack(agentDir string) {
	if agentDir == "" {
		return
	}
	skillsDir := config.SkillsDir(agentDir)
	res, err := papers.SeedSkills(skillsDir, f.log())
	if err != nil {
		f.log().Warn("paper skill-pack seeding failed", "skills_dir", skillsDir, "error", err)
		return
	}
	if res != nil {
		f.log().Info("paper skill-pack seeded",
			"skills_dir", skillsDir,
			"seeded", len(res.Seeded), "updated", len(res.Updated),
			"current", len(res.Current), "preserved", len(res.Preserved),
			"modified", len(res.Modified))
	}
}

// invalidateSkillCache bumps the generation counter so the next ListSkills
// call re-scans the skill directories.
func (f *FrontendAPI) invalidateSkillCache() {
	atomic.AddUint64(&f.skillCacheGen, 1)
}

// startSkillsWatchers creates a debounced file watcher for each existing
// skill directory. When any change is detected the skill cache is
// invalidated and a skills:changed event is emitted so the frontend
// autocomplete refreshes. Non-existent directories are silently skipped;
// the caller may create them later, but changes inside them won't be
// detected until an app restart. This is an acceptable trade-off — skill
// directories are typically created once during installation.
func (f *FrontendAPI) startSkillsWatchers(dirs []string) {
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
		// Also watch each immediate subdirectory so that modifications to
		// an existing skill's SKILL.md are detected on platforms where
		// fsnotify does not watch recursively (e.g. Linux inotify).
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

// closeSkillsWatchers stops all skill directory watchers. Safe to call
// multiple times.
func (f *FrontendAPI) closeSkillsWatchers() {
	f.skillWatchersMu.Lock()
	defer f.skillWatchersMu.Unlock()
	for _, w := range f.skillWatchers {
		_ = w.Close()
	}
	f.skillWatchers = nil
}
