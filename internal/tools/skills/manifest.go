package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// SkillManifest represents the skill.json manifest file for a skill.
type SkillManifest struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	Version      string                 `json:"version"`
	Language     string                 `json:"language"`     // "python", etc.
	EntryPoint   string                 `json:"entry_point"`  // e.g., "main.py"
	InputSchema  map[string]interface{} `json:"input_schema"`
	OutputSchema map[string]interface{} `json:"output_schema"`
	Dependencies []string               `json:"dependencies"` // pip packages
	Capabilities []string               `json:"capabilities"` // "network", "filesystem", etc.
	CreatedAt    string                 `json:"created_at,omitempty"`
	CreatedBy    string                 `json:"created_by,omitempty"`
}

// ParseManifest reads and parses a skill.json manifest file from the given path.
func ParseManifest(path string) (*SkillManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file: %w", err)
	}

	var manifest SkillManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest JSON: %w", err)
	}

	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("manifest validation failed: %w", err)
	}

	return &manifest, nil
}

// Validate checks the manifest has required fields.
func (m *SkillManifest) Validate() error {
	if m.Name == "" {
		return errors.New("name is required")
	}
	if m.Description == "" {
		return errors.New("description is required")
	}
	if m.Version == "" {
		return errors.New("version is required")
	}
	if m.Language == "" {
		return errors.New("language is required")
	}
	if m.EntryPoint == "" {
		return errors.New("entry_point is required")
	}
	return nil
}
