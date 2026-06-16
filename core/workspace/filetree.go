package workspace

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// IsHidden reports whether a file or directory should be considered hidden.
// On Unix-like systems this is determined by a leading dot in the name.
func IsHidden(name string) bool {
	return strings.HasPrefix(name, ".")
}

// IconResolver is called for non-directory entries during listing to obtain
// icon and icon color strings. It receives the os.FileInfo already obtained
// via DirEntry.Info(), avoiding a second os.Stat call at the caller.
type IconResolver func(info fs.FileInfo) (icon string, color string)

// ListDirFlat returns the immediate children of a directory, sorted
// directories first then alphabetically. Files/directories in ignoredPaths
// are included but flagged with GitIgnored=true.
//
// An optional IconResolver may be supplied; when non-nil it is called for
// each non-directory entry to populate Icon and IconColor.
func ListDirFlat(absDir string, ignoredPaths map[string]bool, opts ...ListDirOption) ([]FileNode, error) {
	cfg := listDirConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	var dirs, files []FileNode
	for _, entry := range entries {
		node := FileNode{
			Name:   entry.Name(),
			Path:   filepath.Join(absDir, entry.Name()),
			IsDir:  entry.IsDir(),
			Hidden: IsHidden(entry.Name()),
		}
		if ignoredPaths != nil {
			node.GitIgnored = ignoredPaths[node.Path]
		}
		if !entry.IsDir() && cfg.resolver != nil {
			if info, infoErr := entry.Info(); infoErr == nil {
				node.Icon, node.IconColor = cfg.resolver(info)
			}
		}
		if entry.IsDir() {
			dirs = append(dirs, node)
		} else {
			files = append(files, node)
		}
	}

	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	nodes := make([]FileNode, 0, len(dirs)+len(files))
	nodes = append(nodes, dirs...)
	nodes = append(nodes, files...)
	return nodes, nil
}

// ListDirRecursive returns a flat list of all files and directories found
// recursively under absDir. Within each directory level, directories are
// listed before files and both groups are sorted alphabetically. The .git
// directory and its contents are excluded. Files/directories in ignoredPaths
// are included but flagged with GitIgnored=true.
//
// An optional IconResolver may be supplied via WithIconResolver; when non-nil
// it is called for each non-directory entry to populate Icon and IconColor.
// An optional *slog.Logger may be supplied via WithLogger; when non-nil it is
// used to log warnings for unreadable paths encountered during the walk.
func ListDirRecursive(absDir string, ignoredPaths map[string]bool, opts ...ListDirOption) ([]FileNode, error) {
	cfg := listDirConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	var nodes []FileNode
	walkErr := filepath.WalkDir(absDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if cfg.logger != nil {
				cfg.logger.Warn("skipping unreadable path", "path", path, "error", walkErr)
			}
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if path == absDir {
			return nil
		}
		node := FileNode{
			Name:   d.Name(),
			Path:   path,
			IsDir:  d.IsDir(),
			Hidden: IsHidden(d.Name()),
		}
		if ignoredPaths != nil {
			node.GitIgnored = ignoredPaths[path]
		}
		if !d.IsDir() && cfg.resolver != nil {
			if info, infoErr := d.Info(); infoErr == nil {
				node.Icon, node.IconColor = cfg.resolver(info)
			}
		}
		nodes = append(nodes, node)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("failed to walk directory: %w", walkErr)
	}

	sort.SliceStable(nodes, func(i, j int) bool {
		di := filepath.Dir(nodes[i].Path)
		dj := filepath.Dir(nodes[j].Path)
		if di != dj {
			return di < dj
		}
		if nodes[i].IsDir != nodes[j].IsDir {
			return nodes[i].IsDir
		}
		return nodes[i].Name < nodes[j].Name
	})

	return nodes, nil
}

// listDirConfig holds optional configuration for ListDirFlat and ListDirRecursive.
type listDirConfig struct {
	resolver IconResolver
	logger   *slog.Logger
}

// ListDirOption configures listing behavior.
type ListDirOption func(*listDirConfig)

// WithIconResolver provides an icon resolver that is called for each
// non-directory entry. The fs.FileInfo is obtained from the DirEntry
// during the walk, avoiding a second os.Stat call at the caller.
func WithIconResolver(r IconResolver) ListDirOption {
	return func(c *listDirConfig) { c.resolver = r }
}

// WithLogger provides a logger used to report warnings for unreadable
// paths encountered during ListDirRecursive.
func WithLogger(l *slog.Logger) ListDirOption {
	return func(c *listDirConfig) { c.logger = l }
}
