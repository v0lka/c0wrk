package toolmanager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/v0lka/sp4rk/safeio"
)

// versionsFileName tracks installed tool versions inside the tools directory.
const versionsFileName = ".versions"

// ToolVersions maps tool names to their installed version strings.
type ToolVersions map[string]string

// ReadVersions reads .versions; a missing file yields an empty map, nil error.
func ReadVersions(toolsDir string) (ToolVersions, error) {
	path := filepath.Join(toolsDir, versionsFileName)
	data, err := safeio.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ToolVersions{}, nil
		}
		return ToolVersions{}, fmt.Errorf("reading versions file: %w", err)
	}
	var tv ToolVersions
	if err := json.Unmarshal(data, &tv); err != nil {
		return nil, fmt.Errorf("parsing versions file: %w", err)
	}
	return tv, nil
}

// WriteVersions writes atomically via safeio.WriteFileAtomic — the randomized
// staging name defeats a FIFO/symlink planted at the old deterministic
// "<file>.tmp" path; the rename replaces a plant instead of writing through it.
func WriteVersions(toolsDir string, versions ToolVersions) error {
	path := filepath.Join(toolsDir, versionsFileName)

	data, err := json.MarshalIndent(versions, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling versions: %w", err)
	}
	if err := safeio.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("writing versions file: %w", err)
	}
	return nil
}
