package vectorindex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContentResolver_ReconstructsLineRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.go")
	content := "line1\nline2\nline3\nline4\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cr := newContentResolver(0)

	// Middle range.
	if got, ok := cr.chunkContentOK(path, 2, 3); !ok || got != "line2\nline3" {
		t.Errorf("chunkContent(2,3) = %q, ok=%v; want %q", got, ok, "line2\nline3")
	}
	// Whole file including the trailing newline element semantics: lines
	// 1..4 join to the file without the final "\n" separator's empty tail.
	if got, ok := cr.chunkContentOK(path, 1, 4); !ok || got != "line1\nline2\nline3\nline4" {
		t.Errorf("chunkContent(1,4) = %q, ok=%v; want full file sans trailing newline", got, ok)
	}
	// End clamped past EOF: the split keeps the trailing "" element after
	// the final newline, so the clamp lands on it and the join reproduces
	// the file's trailing newline.
	if got, ok := cr.chunkContentOK(path, 3, 99); !ok || got != "line3\nline4\n" {
		t.Errorf("chunkContent(3,99) = %q, ok=%v; want %q", got, ok, "line3\nline4\n")
	}
	// Degenerate/absent markers normalized.
	if got, ok := cr.chunkContentOK(path, 0, 0); !ok || got != "line1" {
		t.Errorf("chunkContent(0,0) = %q, ok=%v; want first line", got, ok)
	}
	// Start beyond EOF fails (file shrank under us).
	if _, ok := cr.chunkContentOK(path, 99, 100); ok {
		t.Error("chunkContent(99,100) should fail for a 4-line file")
	}
	// The per-call cache served every request: exactly one entry cached.
	if len(cr.lines) != 1 {
		t.Errorf("resolver cached %d files; want 1", len(cr.lines))
	}
}

func TestContentResolver_MissingFileYieldsPlaceholder(t *testing.T) {
	cr := newContentResolver(0)
	got := cr.chunkContent("/definitely/not/there.go", 1, 2)
	if got == "" {
		t.Fatal("placeholder must not be empty")
	}
	if !strings.Contains(got, contentUnavailablePrefix) {
		t.Errorf("placeholder %q lacks the unavailable marker %q", got, contentUnavailablePrefix)
	}
	if !strings.Contains(got, "/definitely/not/there.go") {
		t.Errorf("placeholder %q lacks the file path", got)
	}
	// The failure is cached: no re-read attempt, same placeholder again.
	if again := cr.chunkContent("/definitely/not/there.go", 3, 4); again != got {
		t.Errorf("second resolution %q differs from cached placeholder %q", again, got)
	}
}

func TestContentResolver_BoundedRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	// 5 lines of 10 bytes each ("0123456789\n").
	if err := os.WriteFile(path, []byte(strings.Repeat("0123456789\n", 5)), 0o644); err != nil {
		t.Fatal(err)
	}

	// Bound of 25 bytes: 2×11 bytes cover the first two full lines, the
	// remaining 3 bytes are the partial third line — the resolver sees
	// exactly the bounded prefix.
	cr := newContentResolver(25)
	got := cr.chunkContent(path, 1, 5)
	if got != "0123456789\n0123456789\n012" {
		t.Errorf("bounded reconstruction = %q", got)
	}
}

func TestContentResolver_UnreadableFileYieldsPlaceholder(t *testing.T) {
	dir := t.TempDir()
	// A directory: stat-succeeds, read fails.
	got := newContentResolver(0).chunkContent(dir, 1, 2)
	if !strings.Contains(got, contentUnavailablePrefix) {
		t.Errorf("directory read should yield placeholder, got %q", got)
	}
}

func TestHydrateSearchContent_KeepsStoredContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.go")
	if err := os.WriteFile(path, []byte("stored is authoritative\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	results := []SearchResult{
		{FilePath: path, StartLine: 1, EndLine: 1, Content: "kept"},
		{FilePath: path, StartLine: 1, EndLine: 1, Content: ""},
	}
	hydrateSearchContent(results, newContentResolver(0))
	if results[0].Content != "kept" {
		t.Errorf("stored content was overwritten: %q", results[0].Content)
	}
	if results[1].Content != "stored is authoritative" {
		t.Errorf("empty content was not hydrated: %q", results[1].Content)
	}
}
