package backend

import (
	"sync/atomic"

	"github.com/v0lka/c0wrk/backend/config"
)

// ListSkills returns lightweight descriptors for all available skills,
// including project-local skills from the active project's .agents/skills directory.
// Results are cached; the cache is invalidated when the active project changes.
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

// invalidateSkillCache bumps the generation counter so the next ListSkills
// call re-scans the skill directories.
func (f *FrontendAPI) invalidateSkillCache() {
	atomic.AddUint64(&f.skillCacheGen, 1)
}
