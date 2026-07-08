package skills

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/v0lka/sp4rk/pathutil"
)

// SkillManager discovers, parses, and serves Agent Skills from configured directories.
// Directories are scanned in priority order; the first occurrence of a skill name wins.
type SkillManager struct {
	mu     sync.RWMutex
	skills map[string]*Skill // keyed by skill name
	dirs   []string          // discovery directories in priority order
	logger *slog.Logger
}

// NewSkillManager creates a SkillManager that will discover skills from the given
// directories (highest priority first). Call Scan() to populate the catalog.
func NewSkillManager(dirs []string, logger *slog.Logger) *SkillManager {
	return &SkillManager{
		skills: make(map[string]*Skill),
		dirs:   dirs,
		logger: logger,
	}
}

func (m *SkillManager) log() *slog.Logger {
	if m.logger != nil {
		return m.logger
	}
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// Scan walks all discovery directories and loads valid skills.
// Skills with the same name in a higher-priority directory override those
// in a lower-priority one. Invalid SKILL.md files are logged and skipped.
func (m *SkillManager) Scan() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Clear existing skills (allows re-scanning)
	m.skills = make(map[string]*Skill)

	// Walk directories in reverse priority so higher-priority entries overwrite
	for i := len(m.dirs) - 1; i >= 0; i-- {
		dir := m.dirs[i]
		m.scanDir(dir)
	}

	m.log().Info("skill scan complete", "count", len(m.skills), "dirs", m.dirs)
	return nil
}

// scanDir reads all subdirectories of dir and attempts to parse each as a skill.
// Symlinks pointing to directories are followed.
func (m *SkillManager) scanDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			m.log().Warn("skill dir unreadable", "dir", dir, "error", err)
		}
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			// Follow symlinks: os.ReadDir reports symlinks as non-dirs even
			// if they point to a directory. Use os.Stat to resolve.
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			target := filepath.Join(dir, entry.Name())
			info, err := os.Stat(target)
			if err != nil || !info.IsDir() {
				continue
			}
		}
		skillDir := filepath.Join(dir, entry.Name())
		skillMD := filepath.Join(skillDir, "SKILL.md")

		skill, err := ParseSkill(skillMD, skillDir)
		if err != nil {
			// Skip invalid skills silently (they may not be agent skills at all)
			m.log().Debug("skipped invalid skill", "dir", skillDir, "error", err)
			continue
		}

		m.skills[skill.Metadata.Name] = skill
		m.log().Debug("loaded skill", "name", skill.Metadata.Name, "dir", skillDir)
	}
}

// List returns lightweight descriptors for all discovered skills (discovery phase).
func (m *SkillManager) List() []SkillDescriptor {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]SkillDescriptor, 0, len(m.skills))
	for _, s := range m.skills {
		result = append(result, s.Descriptor())
	}
	return result
}

// Get returns the full Skill by name, or nil if not found.
func (m *SkillManager) Get(name string) (*Skill, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.skills[name]
	return s, ok
}

// SkillPath returns the absolute directory path for a named skill.
// Returns ("", false) if the skill is not found.
func (m *SkillManager) SkillPath(name string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.skills[name]
	if !ok {
		return "", false
	}
	return s.DirPath, true
}

// SafeResolvePath resolves a relative path within a base directory, preventing
// path traversal attacks. Returns the cleaned absolute path or an error if the
// resolved path escapes the base directory. Used by both SkillManager and
// ReadSkillResourceTool to eliminate duplication (S-19).
func SafeResolvePath(baseDir, relPath string) (string, error) {
	cleanBase := filepath.Clean(baseDir)
	// Only resolve baseDir symlinks when baseDir itself is a symlink
	// (not when an ancestor directory like /var → /private/var is).
	// This preserves textual path consistency on macOS while still
	// protecting against symlinked skill directories.
	if fi, err := os.Lstat(cleanBase); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if realBase, err := filepath.EvalSymlinks(cleanBase); err == nil {
			cleanBase = filepath.Clean(realBase)
		}
	}

	// Join and clean the path first (textual resolution of "..").
	// Use cleanBase (possibly symlink-resolved) so the textual fallback
	// path is consistent with the containment boundary computed above.
	joined := filepath.Clean(filepath.Join(cleanBase, relPath))
	// Resolve any symlinks to their real filesystem paths to prevent symlink-based
	// traversal bypass (e.g., a symlink inside the skill directory pointing outside).
	// ResolveExistingPrefix resolves symlinks on the longest existing prefix and
	// joins the non-existent suffix back, so partially-existing paths are still
	// symlink-checked (no textual-only fallback).
	cleanAbs := filepath.Clean(pathutil.ResolveExistingPrefix(joined))
	if cleanAbs != cleanBase {
		ok, err := pathutil.IsWithinPath(cleanBase, cleanAbs)
		if err != nil || !ok {
			return "", fmt.Errorf("path %q escapes skill directory", relPath)
		}
	}
	return cleanAbs, nil
}
