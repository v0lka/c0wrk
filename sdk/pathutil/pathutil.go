// Package pathutil provides reusable filesystem-path algorithms with no
// knowledge of any application's directory layout. Safe for use from any layer.
package pathutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// IsWithinPath returns true if child is equal to or a descendant of parent.
// Both paths are symlink-resolved through their longest existing prefix
// (ResolveExistingPrefix) to handle OS-level symlinks like macOS
// /var → /private/var even when paths don't exist on disk.
//
// Returns an error when parent is empty (containment cannot be determined —
// callers must guard empty roots explicitly) or when Rel fails (e.g., paths
// on different volumes).
func IsWithinPath(parent, child string) (bool, error) {
	// Empty parent means containment cannot be determined — fail closed.
	if parent == "" {
		return false, errors.New("pathutil: empty parent path — containment cannot be determined")
	}
	parentResolved := ResolveExistingPrefix(filepath.Clean(parent))
	childResolved := ResolveExistingPrefix(filepath.Clean(child))

	rel, err := filepath.Rel(parentResolved, childResolved)
	if err != nil {
		return false, err
	}
	// rel == "." means same path (child is within parent).
	// rel == ".." or rel starting with "../" means child escapes above parent.
	// Note: strings.HasPrefix(rel, "..") alone is incorrect — it would also
	// reject legitimate children whose name begins with ".." (e.g. "..foo").
	if rel == "." {
		return true, nil
	}
	return rel != ".." && !strings.HasPrefix(rel, "../"), nil
}

// SplitPathComponents splits a cleaned absolute path into non-empty components,
// stripping the root separator. For example:
//
//	"/home/user/file.txt" → ["home", "user", "file.txt"]
//	"/"                   → []
func SplitPathComponents(absPath string) []string {
	sep := string(filepath.Separator)
	parts := strings.Split(absPath, sep)
	// Remove leading empty string from split of absolute path
	// (e.g., "/a/b" → ["", "a", "b"]).
	if len(parts) > 0 && parts[0] == "" {
		parts = parts[1:]
	}
	// Filter empty parts.
	var result []string
	for _, part := range parts {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

// ResolveExistingPrefix resolves symlinks on the longest existing prefix of
// path, joining the non-existent suffix back. Used when validating paths for
// files or directories that may not exist yet (e.g., write/mkdir tool targets).
//
// Example: if "/ws/link" is a symlink but "/ws/link/newfile.txt" doesn't exist,
// returns the symlink-resolved prefix + "/newfile.txt".
func ResolveExistingPrefix(path string) string {
	candidate := path
	for {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err == nil {
			if candidate == path {
				return resolved
			}
			rel, relErr := filepath.Rel(candidate, path)
			if relErr != nil {
				// Paths on different volumes — fall back to unresolved.
				return path
			}
			return filepath.Join(resolved, rel)
		}
		if errors.Is(err, os.ErrNotExist) {
			parent := filepath.Dir(candidate)
			if parent == candidate {
				// Reached root — nothing exists, return as-is.
				return path
			}
			candidate = parent
			continue
		}
		// Permission or other error — return as-is.
		return path
	}
}
