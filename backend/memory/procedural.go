package memory

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ExternalToolInfo holds metadata about a tool for ProceduralMemory.
// This type is separate from tools to avoid import cycles.
type ExternalToolInfo struct {
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Version      string     `json:"version"`
	Path         string     `json:"path"` // filesystem path to tool directory
	Language     string     `json:"language"`
	Capabilities []string   `json:"capabilities"`
	UsageCount   int        `json:"usage_count"`
	LastUsed     *time.Time `json:"last_used,omitempty"`
}

// toolManifest is used internally for parsing tool.json files.
// Mirrors the structure in tools but avoids the import.
type toolManifest struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Version      string   `json:"version"`
	Language     string   `json:"language"`
	Capabilities []string `json:"capabilities"`
}

// ProceduralMemory is an in-memory registry of available tools.
// It scans the tools directory for tool.json manifests.
type ProceduralMemory struct {
	tools    map[string]*ExternalToolInfo
	toolsDir string // base directory (~/.c0wrk/tools/)
	mu       sync.RWMutex
}

// NewProceduralMemory creates a new ProceduralMemory that scans toolsDir.
func NewProceduralMemory(toolsDir string) *ProceduralMemory {
	return &ProceduralMemory{
		tools:    make(map[string]*ExternalToolInfo),
		toolsDir: toolsDir,
	}
}

// Scan reads all */tool.json files in the tools directory and builds the index.
// Errors on individual tools are logged but don't fail the whole scan.
func (pm *ProceduralMemory) Scan() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Clear existing tools before scanning
	pm.tools = make(map[string]*ExternalToolInfo)

	// Check if directory exists
	info, err := os.Stat(pm.toolsDir)
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
	entries, err := os.ReadDir(pm.toolsDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		toolDir := filepath.Join(pm.toolsDir, entry.Name())
		manifestPath := filepath.Join(toolDir, "tool.json")

		// Check if tool.json exists
		if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
			continue
		}

		// Read and parse manifest
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			slog.Warn("failed to read tool manifest", "path", manifestPath, "error", err)
			continue
		}

		var manifest toolManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			slog.Warn("failed to parse tool manifest", "path", manifestPath, "error", err)
			continue
		}

		// Skip if no name
		if manifest.Name == "" {
			slog.Warn("tool manifest has no name, skipping", "path", manifestPath)
			continue
		}

		pm.tools[manifest.Name] = &ExternalToolInfo{
			Name:         manifest.Name,
			Description:  manifest.Description,
			Version:      manifest.Version,
			Path:         toolDir,
			Language:     manifest.Language,
			Capabilities: manifest.Capabilities,
		}
	}

	return nil
}

// GetTool returns info about a specific tool by name.
func (pm *ProceduralMemory) GetTool(name string) (*ExternalToolInfo, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	tool, ok := pm.tools[name]
	return tool, ok
}

// ListTools returns all known tools.
func (pm *ProceduralMemory) ListTools() []*ExternalToolInfo {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	result := make([]*ExternalToolInfo, 0, len(pm.tools))
	for _, tool := range pm.tools {
		result = append(result, tool)
	}
	return result
}

// Register adds or updates a tool in the index.
func (pm *ProceduralMemory) Register(info *ExternalToolInfo) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pm.tools[info.Name] = info
}

// IncrementUsage increments the usage count and updates the last used time for a tool.
func (pm *ProceduralMemory) IncrementUsage(name string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if tool, ok := pm.tools[name]; ok {
		tool.UsageCount++
		now := time.Now()
		tool.LastUsed = &now
	}
}
