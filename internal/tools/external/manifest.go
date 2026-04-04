package external

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// ToolManifest describes an external tool's metadata and configuration.
type ToolManifest struct {
	Name          string                 `json:"name"`
	Description   string                 `json:"description"`
	Version       string                 `json:"version"`
	Language      string                 `json:"language"` // "python" or "bash"
	EntryPoint    string                 `json:"entry_point"`
	InputSchema   map[string]interface{} `json:"input_schema"`
	DefaultPolicy string                 `json:"default_policy,omitempty"` // "always_allow", "auto", etc.
	CreatedAt     string                 `json:"created_at,omitempty"`
	CreatedBy     string                 `json:"created_by,omitempty"`
}

// ParseManifest reads and parses a tool.json file at the given path.
func ParseManifest(path string) (*ToolManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var m ToolManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	return &m, nil
}

// Validate checks that the manifest has all required fields and valid values.
func (m *ToolManifest) Validate() error {
	if m.Name == "" {
		return errors.New("manifest validation: name is required")
	}
	if m.Description == "" {
		return errors.New("manifest validation: description is required")
	}
	if m.Version == "" {
		return errors.New("manifest validation: version is required")
	}
	if m.Language == "" {
		return errors.New("manifest validation: language is required")
	}
	if m.Language != "python" && m.Language != "bash" {
		return fmt.Errorf("manifest validation: language must be \"python\" or \"bash\", got %q", m.Language)
	}
	if m.EntryPoint == "" {
		return errors.New("manifest validation: entry_point is required")
	}
	return nil
}
