package backend

import (
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/workspace"
)

// ListAgents returns lightweight descriptors for all available Subagent
// Profiles (AGENT.md), including project-local agents. Mirrors ListSkills:
// cached, invalidated on project switch or watcher change; hidden agents
// excluded from the public roster. See specs/domains/capabilities.md.
func (f *FrontendAPI) ListAgents() []AgentDescriptorDTO {
	b := f.builder()
	if b == nil {
		return []AgentDescriptorDTO{}
	}

	// Snapshot active project info for cache key.
	f.activeProjectMu.RLock()
	wsPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()

	projectAgentDir := ""
	if wsPath != "" {
		projectAgentDir = config.ProjectAgentsPath(wsPath)
	}

	// Check cache: valid if gen matches and project path hasn't changed.
	gen := atomic.LoadUint64(&f.agentCacheGen)
	f.agentCacheMu.Lock()
	if f.agentCache != nil && f.agentCacheGenSnapshot == gen && f.agentCacheProjectDir == projectAgentDir {
		cached := f.agentCache
		f.agentCacheMu.Unlock()
		return cached
	}
	f.agentCacheMu.Unlock()

	descriptors := b.GetAgentDescriptors(projectAgentDir)
	result := make([]AgentDescriptorDTO, 0, len(descriptors))
	for _, d := range descriptors {
		if d.Hidden {
			continue
		}
		result = append(result, AgentDescriptorDTO{Name: d.Name, Description: d.Description})
	}

	// Store in cache.
	f.agentCacheMu.Lock()
	f.agentCache = result
	f.agentCacheGenSnapshot = gen
	f.agentCacheProjectDir = projectAgentDir
	f.agentCacheMu.Unlock()

	return result
}

// invalidateAgentCache bumps the generation counter so the next ListAgents
// call re-scans the agent directories. Mirrors invalidateSkillCache.
func (f *FrontendAPI) invalidateAgentCache() {
	atomic.AddUint64(&f.agentCacheGen, 1)
}

// RefreshAgents forces a re-discovery of every Subagent Profile directory
// and returns the fresh (hidden-excluded) descriptors — the manual path
// behind the Capabilities tab's refresh button (the generation bump makes it
// always re-scan). See specs/domains/capabilities.md.
func (f *FrontendAPI) RefreshAgents() []AgentDescriptorDTO {
	f.invalidateAgentCache()
	return f.ListAgents()
}

// startAgentsWatchers creates a debounced watcher per existing Subagent
// Profile directory; a change invalidates the cache and emits agents:changed.
func (f *FrontendAPI) startAgentsWatchers(dirs []string) {
	f.seedAcquire()
	f.agentWatchersMu.Lock()
	defer f.agentWatchersMu.Unlock()

	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		watcher, err := workspace.NewWatcher(dir, func(_ []string) {
			f.invalidateAgentCache()
			f.emitEvent(EventAgentsChanged, nil)
		}, f.log())
		if err != nil {
			f.log().Warn("failed to watch agent directory", "dir", dir, "error", err)
			continue
		}
		// Watch each immediate subdirectory so edits to an existing agent's
		// AGENT.md are detected where fsnotify does not watch recursively
		// (e.g. Linux inotify).
		if entries, err := os.ReadDir(dir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					_ = watcher.WatchDir(filepath.Join(dir, entry.Name()))
				}
			}
		}
		f.agentWatchers = append(f.agentWatchers, watcher)
	}
}

// closeAgentsWatchers stops all agent directory watchers. Safe to call
// multiple times. Mirrors closeSkillsWatchers.
func (f *FrontendAPI) closeAgentsWatchers() {
	f.agentWatchersMu.Lock()
	defer f.agentWatchersMu.Unlock()
	for _, w := range f.agentWatchers {
		_ = w.Close()
	}
	f.agentWatchers = nil
}
