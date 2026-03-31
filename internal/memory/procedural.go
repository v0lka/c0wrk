package memory

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SkillInfo holds metadata about a skill for ProceduralMemory.
// This type is separate from tools/skills to avoid import cycles.
type SkillInfo struct {
	Name         string
	Description  string
	Version      string
	Path         string // filesystem path to skill directory
	Language     string
	Capabilities []string
	UsageCount   int        `json:"usage_count"`
	LastUsed     *time.Time `json:"last_used,omitempty"`
}

// skillManifest is used internally for parsing skill.json files.
// Mirrors the structure in tools/skills/manifest.go but avoids the import.
type skillManifest struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Version      string   `json:"version"`
	Language     string   `json:"language"`
	Capabilities []string `json:"capabilities"`
}

// ProceduralMemory is an in-memory registry of available skills.
// It scans the skills directory for skill.json manifests.
type ProceduralMemory struct {
	skills    map[string]*SkillInfo
	skillsDir string // base directory (~/.c0wrk/skills/)
	mu        sync.RWMutex
}

// NewProceduralMemory creates a new ProceduralMemory that scans skillsDir.
func NewProceduralMemory(skillsDir string) *ProceduralMemory {
	return &ProceduralMemory{
		skills:    make(map[string]*SkillInfo),
		skillsDir: skillsDir,
	}
}

// Scan reads all */skill.json files in the skills directory and builds the index.
// Errors on individual skills are logged but don't fail the whole scan.
func (pm *ProceduralMemory) Scan() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Clear existing skills before scanning
	pm.skills = make(map[string]*SkillInfo)

	// Check if directory exists
	info, err := os.Stat(pm.skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Directory doesn't exist, return empty (no error)
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}

	// Read directory entries
	entries, err := os.ReadDir(pm.skillsDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillDir := filepath.Join(pm.skillsDir, entry.Name())
		manifestPath := filepath.Join(skillDir, "skill.json")

		// Check if skill.json exists
		if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
			continue
		}

		// Read and parse manifest
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			log.Printf("failed to read skill manifest %s: %v", manifestPath, err)
			continue
		}

		var manifest skillManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			log.Printf("failed to parse skill manifest %s: %v", manifestPath, err)
			continue
		}

		// Skip if no name
		if manifest.Name == "" {
			log.Printf("skill manifest %s has no name, skipping", manifestPath)
			continue
		}

		pm.skills[manifest.Name] = &SkillInfo{
			Name:         manifest.Name,
			Description:  manifest.Description,
			Version:      manifest.Version,
			Path:         skillDir,
			Language:     manifest.Language,
			Capabilities: manifest.Capabilities,
		}
	}

	return nil
}

// GetSkill returns info about a specific skill by name.
func (pm *ProceduralMemory) GetSkill(name string) (*SkillInfo, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	skill, ok := pm.skills[name]
	return skill, ok
}

// ListSkills returns all known skills.
func (pm *ProceduralMemory) ListSkills() []*SkillInfo {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	result := make([]*SkillInfo, 0, len(pm.skills))
	for _, skill := range pm.skills {
		result = append(result, skill)
	}
	return result
}

// Register adds or updates a skill in the index.
func (pm *ProceduralMemory) Register(info *SkillInfo) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pm.skills[info.Name] = info
}

// IncrementUsage increments the usage count and updates the last used time for a skill.
func (pm *ProceduralMemory) IncrementUsage(name string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if skill, ok := pm.skills[name]; ok {
		skill.UsageCount++
		now := time.Now()
		skill.LastUsed = &now
	}
}
