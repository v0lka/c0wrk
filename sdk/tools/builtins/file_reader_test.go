package builtins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	return path
}

// countContentLines counts lines in ReadFileRange output, accounting for
// trailing newlines the same way ReadFileRange counts file lines.
func countContentLines(content string) int {
	if content == "" {
		return 0
	}
	n := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		n++
	}
	return n
}

func TestReadFileRange_DefaultWindow(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 10000; i++ {
		if i > 1 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "Line %d", i)
	}
	path := writeTempFile(t, "large.txt", sb.String())

	result, err := ReadFileRange(FileReadParams{
		Path:         path,
		DefaultLines: 2000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalLines != 10000 {
		t.Errorf("TotalLines = %d, want 10000", result.TotalLines)
	}
	if result.StartLine != 1 {
		t.Errorf("StartLine = %d, want 1", result.StartLine)
	}
	if result.EndLine != 2000 {
		t.Errorf("EndLine = %d, want 2000", result.EndLine)
	}
	if n := countContentLines(result.Content); n != 2000 {
		t.Errorf("content has %d lines, want 2000", n)
	}
	if !strings.HasPrefix(result.Content, "Line 1\n") {
		t.Errorf("expected content to start with 'Line 1\\n', got: %q", result.Content[:min(80, len(result.Content))])
	}
	if !strings.Contains(result.Content, "Line 2000") {
		t.Errorf("expected 'Line 2000' in content")
	}
	if result.WindowCapped {
		t.Error("WindowCapped should be false for default window within MaxWindowLines")
	}
}

func TestReadFileRange_ExplicitRange(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 100; i++ {
		if i > 1 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "Line %d", i)
	}
	path := writeTempFile(t, "medium.txt", sb.String())

	result, err := ReadFileRange(FileReadParams{
		Path:      path,
		StartLine: 10,
		EndLine:   20,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalLines != 100 {
		t.Errorf("TotalLines = %d, want 100", result.TotalLines)
	}
	if n := countContentLines(result.Content); n != 11 {
		t.Errorf("content has %d lines, want 11", n)
	}
	if !strings.HasPrefix(result.Content, "Line 10\n") {
		t.Errorf("expected content to start with 'Line 10\\n'")
	}
	if !strings.Contains(result.Content, "Line 20") {
		t.Errorf("expected 'Line 20' in content")
	}
}

func TestReadFileRange_TrailingNewline(t *testing.T) {
	content := "Line 1\nLine 2\nLine 3\n"
	path := writeTempFile(t, "trailing.txt", content)

	result, err := ReadFileRange(FileReadParams{
		Path:         path,
		EndLine:      3,
		DefaultLines: 100,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalLines != 3 {
		t.Errorf("TotalLines = %d, want 3 (trailing newline should not add a line)", result.TotalLines)
	}
}

func TestReadFileRange_NoTrailingNewline(t *testing.T) {
	content := "Line 1\nLine 2\nLine 3"
	path := writeTempFile(t, "notrailing.txt", content)

	result, err := ReadFileRange(FileReadParams{
		Path:         path,
		EndLine:      3,
		DefaultLines: 100,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalLines != 3 {
		t.Errorf("TotalLines = %d, want 3", result.TotalLines)
	}
}

func TestReadFileRange_EmptyFile(t *testing.T) {
	path := writeTempFile(t, "empty.txt", "")

	result, err := ReadFileRange(FileReadParams{
		Path:         path,
		DefaultLines: 2000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalLines != 0 {
		t.Errorf("TotalLines = %d, want 0 for empty file", result.TotalLines)
	}
	if result.Content != "" {
		t.Errorf("Content = %q, want empty", result.Content)
	}
}

func TestReadFileRange_SingleNewline(t *testing.T) {
	path := writeTempFile(t, "nl.txt", "\n")

	result, err := ReadFileRange(FileReadParams{
		Path:         path,
		DefaultLines: 2000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalLines != 1 {
		t.Errorf("TotalLines = %d, want 1", result.TotalLines)
	}
	if result.Content != "\n" {
		t.Errorf("Content = %q, want %q", result.Content, "\n")
	}
}

func TestReadFileRange_StartLineBeyondEOF(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 50; i++ {
		if i > 1 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "Line %d", i)
	}
	path := writeTempFile(t, "fifty.txt", sb.String())

	result, err := ReadFileRange(FileReadParams{
		Path:      path,
		StartLine: 200,
		EndLine:   300,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalLines != 50 {
		t.Errorf("TotalLines = %d, want 50", result.TotalLines)
	}
	if result.Content != "" {
		t.Errorf("Content = %q, want empty (start beyond EOF)", result.Content)
	}
}

func TestReadFileRange_MaxLineBytes(t *testing.T) {
	longLine := strings.Repeat("a", 5000)
	path := writeTempFile(t, "longline.txt", longLine)

	result, err := ReadFileRange(FileReadParams{
		Path:         path,
		DefaultLines: 2000,
		MaxLineBytes: 100,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, "[...line truncated at 100 bytes...]") {
		t.Errorf("expected line truncation marker, got: %s", result.Content)
	}
}

func TestReadFileRange_MaxLineBytesPreservesNewline(t *testing.T) {
	longLine := strings.Repeat("a", 5000)
	content := longLine + "\nshort\n"
	path := writeTempFile(t, "longwithnl.txt", content)

	result, err := ReadFileRange(FileReadParams{
		Path:         path,
		DefaultLines: 2000,
		MaxLineBytes: 100,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, "[...line truncated at 100 bytes...]") {
		t.Errorf("expected truncation marker in content")
	}
	if !strings.Contains(result.Content, "\nshort\n") {
		t.Errorf("expected 'short' line after truncated line, got: %q", result.Content)
	}
}

func TestReadFileRange_MaxWindowLines(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 100000; i++ {
		if i > 1 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "Line %d", i)
	}
	path := writeTempFile(t, "huge.txt", sb.String())

	result, err := ReadFileRange(FileReadParams{
		Path:           path,
		StartLine:      1,
		EndLine:        100000,
		MaxWindowLines: 1000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.WindowCapped {
		t.Error("expected WindowCapped = true")
	}
	if result.EndLine != 1000 {
		t.Errorf("EndLine = %d, want 1000 (capped)", result.EndLine)
	}
	if n := countContentLines(result.Content); n != 1000 {
		t.Errorf("content has %d lines, want 1000", n)
	}
	if result.TotalLines != 100000 {
		t.Errorf("TotalLines = %d, want 100000", result.TotalLines)
	}
}

func TestReadFileRange_LargeFileNoOOM(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 100000; i++ {
		if i > 1 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "Line %d with some content to make it realistic", i)
	}
	path := writeTempFile(t, "big.txt", sb.String())

	result, err := ReadFileRange(FileReadParams{
		Path:         path,
		DefaultLines: 2000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalLines != 100000 {
		t.Errorf("TotalLines = %d, want 100000", result.TotalLines)
	}
	if n := countContentLines(result.Content); n != 2000 {
		t.Errorf("content has %d lines, want 2000", n)
	}
}

func TestReadFileRange_FastCountAfterWindow(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 50000; i++ {
		if i > 1 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "Line %d", i)
	}
	path := writeTempFile(t, "fiftyk.txt", sb.String())

	result, err := ReadFileRange(FileReadParams{
		Path:         path,
		DefaultLines: 100,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalLines != 50000 {
		t.Errorf("TotalLines = %d, want 50000 (fast count after window)", result.TotalLines)
	}
	if n := countContentLines(result.Content); n != 100 {
		t.Errorf("content has %d lines, want 100", n)
	}
}

func TestReadFileRange_NonExistentFile(t *testing.T) {
	_, err := ReadFileRange(FileReadParams{
		Path:         "/nonexistent/path/file.txt",
		DefaultLines: 2000,
	})
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}
