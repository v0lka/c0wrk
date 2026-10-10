package toolmanager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/v0lka/sp4rk/safeio"
)

// versionsFileName is the name of the JSON file that tracks installed tool
// versions within the tools directory.
const versionsFileName = ".versions"

// ToolVersions maps tool names to their installed version strings.
type ToolVersions map[string]string

// ReadVersions reads the .versions JSON file from toolsDir. If the file does
// not exist, it returns an empty ToolVersions map without error.
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

// WriteVersions atomically writes the ToolVersions as JSON to toolsDir. The
// staging temp is created under a randomized name (safeio.WriteFileAtomic),
// so the write-open cannot be redirected by a FIFO or symlink planted at the
// deterministic "<file>.tmp" name the previous implementation used, and the
// rename replaces such a planted entry itself instead of writing through it.
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
