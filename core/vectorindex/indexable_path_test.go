package vectorindex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsIndexablePath(t *testing.T) {
	root := t.TempDir()

	// Seed a couple of real source files.
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a .git directory with some internal churn target.
	if err := os.MkdirAll(filepath.Join(root, ".git", "objects", "pack"), 0o755); err != nil {
		t.Fatal(err)
	}
	// node_modules and build are excluded via .gitignore now that hardcoded
	// default ignore dirs have been removed.
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("node_modules/\nbuild/\n*.exe\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		rel     string
		wantIdx bool
	}{
		{"source file", "src/main.go", true},
		{"git internal object", ".git/objects/pack/foo.pack", false},
		{"git index", ".git/index", false},
		{"git HEAD", ".git/HEAD", false},
		{"DS_Store", ".DS_Store", false},
		{"hidden dir churn", ".cache/data.bin", false},
		{"node_modules", "node_modules/pkg/index.js", false},
		{"binary by extension", "build/app.exe", false},
		{"plain text", "README.md", true},
		{"outside root", "../other/file.go", false},
		{"root itself", ".", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			abs := tt.rel
			if tt.rel != "." {
				abs = filepath.Join(root, tt.rel)
			}
			got := IsIndexablePath(abs, root)
			if got != tt.wantIdx {
				t.Errorf("IsIndexablePath(%q) = %v, want %v", tt.rel, got, tt.wantIdx)
			}
		})
	}
}

func TestIsAnyIndexablePath(t *testing.T) {
	root := t.TempDir()

	// All-ignored set → false.
	allIgnored := make([]string, 0, 3)
	allIgnored = append(allIgnored,
		filepath.Join(root, ".git", "objects", "x"),
		filepath.Join(root, ".DS_Store"),
	)
	if IsAnyIndexablePath(allIgnored, root) {
		t.Error("expected false when all paths are ignored")
	}

	// Mixed set with one indexable → true.
	mixed := make([]string, 0, len(allIgnored)+1)
	mixed = append(mixed, allIgnored...)
	mixed = append(mixed, filepath.Join(root, "main.go"))
	if !IsAnyIndexablePath(mixed, root) {
		t.Error("expected true when at least one path is indexable")
	}

	// Empty → false.
	if IsAnyIndexablePath(nil, root) {
		t.Error("expected false for empty path list")
	}
}

// TestIsIndexablePath_GitignoreDir verifies that a directory excluded ONLY via
// .gitignore (not a hidden directory) is filtered by the watcher's path check,
// mirroring the walker's filtering. Before the fix, the watcher filter matched
// gitignore patterns against the file path but not its directory segments, so
// churn under such a directory fired a spurious reindex the walker then silently
// dropped.
func TestIsIndexablePath_GitignoreDir(t *testing.T) {
	root := t.TempDir()

	// "coverage_report/" and "logs/old/" are excluded only via .gitignore
	// (they are not hidden directories).
	gitignore := "coverage_report/\nlogs/old/\n"
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		rel     string
		wantIdx bool
	}{
		{"file under gitignored top-level dir", "coverage_report/out.txt", false},
		{"nested file under gitignored dir", "coverage_report/sub/deep.log", false},
		{"file under nested-pattern dir", "logs/old/archive.txt", false},
		{"sibling of nested pattern stays indexable", "logs/recent.txt", true},
		{"unrelated source indexable", "src/main.go", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsIndexablePath(filepath.Join(root, tt.rel), root)
			if got != tt.wantIdx {
				t.Errorf("IsIndexablePath(%q) = %v, want %v", tt.rel, got, tt.wantIdx)
			}
		})
	}

	// Batch view: a change set entirely under gitignored dirs must report
	// nothing indexable.
	allIgnored := []string{
		filepath.Join(root, "coverage_report", "out.txt"),
		filepath.Join(root, "logs", "old", "archive.txt"),
	}
	if IsAnyIndexablePath(allIgnored, root) {
		t.Error("expected false when all changed paths are under gitignored dirs")
	}
}
