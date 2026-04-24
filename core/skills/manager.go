package skills

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
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
	return slog.Default()
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
			continue
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

// ResolveResourcePath resolves a relative path within a skill's directory.
// Returns an error if the skill doesn't exist or the resolved path escapes the skill dir.
func (m *SkillManager) ResolveResourcePath(skillName, relPath string) (string, error) {
	skillDir, ok := m.SkillPath(skillName)
	if !ok {
		return "", fmt.Errorf("skill %q not found", skillName)
	}

	absPath := filepath.Join(skillDir, relPath)

	// Security: ensure the resolved path stays within the skill directory
	// to prevent path traversal attacks.
	cleanSkillDir := filepath.Clean(skillDir)
	cleanAbsPath := filepath.Clean(absPath)
	if cleanAbsPath != cleanSkillDir && !isSubPath(cleanSkillDir, cleanAbsPath) {
		return "", fmt.Errorf("path %q escapes skill directory", relPath)
	}

	return cleanAbsPath, nil
}

// isSubPath returns true if sub is a descendant of parent.
func isSubPath(parent, sub string) bool {
	rel, err := filepath.Rel(parent, sub)
	if err != nil {
		return false
	}
	// If the relative path starts with "..", it escapes the parent
	return len(rel) >= 1 && rel[0] != '.' || len(rel) > 2 && rel[:2] != ".."
}
