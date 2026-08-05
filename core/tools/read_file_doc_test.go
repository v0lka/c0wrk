package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// --- IsContentBacked extension matrix ---

func TestIsContentBacked_DocumentFormats(t *testing.T) {
	tool := NewReadFileDocTool(builtins.DefaultFileLimits(), nil)

	docFiles := []string{"report.pdf", "doc.docx", "slides.pptx", "data.xlsx",
		"text.odt", "page.html", "page.htm"}
	for _, f := range docFiles {
		input, _ := json.Marshal(map[string]string{"path": f})
		if !tool.IsContentBacked(context.Background(), input) {
			t.Errorf("IsContentBacked(%q) = false, want true", f)
		}
	}
}

func TestIsContentBacked_PlainTextFormats(t *testing.T) {
	tool := NewReadFileDocTool(builtins.DefaultFileLimits(), nil)

	textFiles := []string{"notes.txt", "readme.md", "data.json", "config.xml",
		"data.csv", "data.tsv", "doc.rst", "main.go", "app.ts"}
	for _, f := range textFiles {
		input, _ := json.Marshal(map[string]string{"path": f})
		if tool.IsContentBacked(context.Background(), input) {
			t.Errorf("IsContentBacked(%q) = true, want false", f)
		}
	}
}

func TestIsContentBacked_EmptyPath(t *testing.T) {
	tool := NewReadFileDocTool(builtins.DefaultFileLimits(), nil)
	input := json.RawMessage(`{"path":""}`)
	if tool.IsContentBacked(context.Background(), input) {
		t.Error("IsContentBacked with empty path should be false")
	}
}

func TestIsContentBacked_CaseInsensitive(t *testing.T) {
	tool := NewReadFileDocTool(builtins.DefaultFileLimits(), nil)
	for _, f := range []string{"report.PDF", "doc.DOCX", "page.HTML"} {
		input, _ := json.Marshal(map[string]string{"path": f})
		if !tool.IsContentBacked(context.Background(), input) {
			t.Errorf("IsContentBacked(%q) = false, want true (case-insensitive)", f)
		}
	}
}

// --- Description ---

func TestDescription_MentionsDocumentFormats(t *testing.T) {
	tool := NewReadFileDocTool(builtins.DefaultFileLimits(), nil)
	desc := tool.Description()
	for _, keyword := range []string{"pdf", "docx", "markdown"} {
		if !strings.Contains(desc, keyword) {
			t.Errorf("Description should mention %q, got: %s", keyword, desc)
		}
	}
}

// --- Name and schema delegation ---

func TestReadFileDocTool_Name(t *testing.T) {
	tool := NewReadFileDocTool(builtins.DefaultFileLimits(), nil)
	if tool.Name() != "read_file" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "read_file")
	}
}

// --- Delegation for plain-text files ---

func TestExecute_DelegatesPlainTextFile(t *testing.T) {
	ws := t.TempDir()
	testFile := filepath.Join(ws, "test.txt")
	content := "line one\nline two\nline three\n"
	if err := os.WriteFile(testFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileDocTool(builtins.DefaultFileLimits(), nil)
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)

	input, _ := json.Marshal(map[string]string{"path": "test.txt"})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	if !strings.Contains(result.Content, "line one") {
		t.Errorf("expected inner read_file content, got: %s", result.Content)
	}
}

// --- Range validation (Issue #1: inverted/invalid ranges must not panic) ---

func TestExecute_DocInvertedRangeReturnsError(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "doc.pdf"), []byte("%PDF fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	// markitdown absent — but validation runs before any conversion attempt.
	t.Setenv("PATH", t.TempDir())

	tool := NewReadFileDocTool(builtins.DefaultFileLimits(), nil)
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	ctx = sdktools.WithTempDir(ctx, t.TempDir())

	input, _ := json.Marshal(map[string]any{"path": "doc.pdf", "start_line": 10, "end_line": 5})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected validation error for inverted range, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "must not exceed end_line") {
		t.Errorf("expected inverted-range message, got: %s", result.Content)
	}
}

func TestExecute_DocNegativeStartLineReturnsError(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "doc.pdf"), []byte("%PDF fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())

	tool := NewReadFileDocTool(builtins.DefaultFileLimits(), nil)
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	ctx = sdktools.WithTempDir(ctx, t.TempDir())

	input, _ := json.Marshal(map[string]any{"path": "doc.pdf", "start_line": -3})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "start_line must be >= 1") {
		t.Errorf("expected negative start_line error, got: %s", result.Content)
	}
}

// --- formatMarkdownWindow range logic ---

func TestFormatMarkdownWindow_DefaultRange(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 100; i++ {
		sb.WriteString("line ")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString("\n")
	}
	// Remove trailing newline to match markitdown output.
	markdown := strings.TrimSuffix(sb.String(), "\n")

	result := formatMarkdownWindow(markdown, "doc.pdf", 0, 0, builtins.FileLimits{ReadDefaultLines: 20})
	if !strings.Contains(result, "Lines 1-20 of 100") {
		t.Errorf("expected default window 1-20 of 100, got: %s", result[:min2(80, len(result))])
	}
	if !strings.Contains(result, "Use start_line=21 to continue reading") {
		t.Error("expected continuation hint")
	}
}

func TestFormatMarkdownWindow_ExplicitRange(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 50; i++ {
		sb.WriteString("line ")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString("\n")
	}
	markdown := strings.TrimSuffix(sb.String(), "\n")

	// Range that reaches the end — no continuation hint.
	result := formatMarkdownWindow(markdown, "doc.docx", 40, 50, builtins.DefaultFileLimits())
	if !strings.Contains(result, "Lines 40-50 of 50") {
		t.Errorf("expected range 40-50 of 50")
	}
	if strings.Contains(result, "continue reading") {
		t.Error("should not have continuation hint (range reaches EOF)")
	}
}

func TestFormatMarkdownWindow_ClampedToEnd(t *testing.T) {
	markdown := "only\na\nfew\nlines"
	result := formatMarkdownWindow(markdown, "f.pdf", 1, 100, builtins.FileLimits{})
	if !strings.Contains(result, "Lines 1-4 of 4") {
		t.Errorf("expected clamped range 1-4 of 4, got prefix: %s", result[:min2(80, len(result))])
	}
}

func TestFormatMarkdownWindow_ConvertedFromHeader(t *testing.T) {
	result := formatMarkdownWindow("hello", "doc.pdf", 1, 1, builtins.FileLimits{})
	if !strings.Contains(result, "converted from .pdf") {
		t.Errorf("expected 'converted from .pdf' in header, got: %s", result)
	}
}

// Issue #6: start_line past EOF must report explicitly, not clamp to last line.
func TestFormatMarkdownWindow_PastEOF(t *testing.T) {
	markdown := "a\nb\nc" // 3 lines
	result := formatMarkdownWindow(markdown, "doc.pdf", 600, 0, builtins.FileLimits{ReadDefaultLines: 20})
	if !strings.Contains(result, "past end of file") {
		t.Errorf("expected past-EOF message, got: %s", result)
	}
	if strings.Contains(result, "Lines 600") {
		t.Errorf("should not present a window for a past-EOF start, got: %s", result)
	}
}

// Issue #3: MaxWindowLines caps an explicit end_line beyond the cap.
func TestFormatMarkdownWindow_MaxWindowLinesCap(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 1000; i++ {
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString("\n")
	}
	markdown := strings.TrimSuffix(sb.String(), "\n")

	// Request 1..1000 but cap the window at 50 lines.
	result := formatMarkdownWindow(markdown, "doc.pdf", 1, 1000, builtins.FileLimits{MaxWindowLines: 50})
	if !strings.Contains(result, "Lines 1-50 of 1000") {
		t.Errorf("expected window capped to 1-50 of 1000, got: %s", result[:min2(80, len(result))])
	}
	if !strings.Contains(result, "Use start_line=51 to continue reading") {
		t.Error("expected continuation hint right after the capped window")
	}
}

// Issue #7: MaxLineBytes truncates pathological converted lines.
func TestFormatMarkdownWindow_LineTruncation(t *testing.T) {
	huge := strings.Repeat("x", 5000)
	markdown := huge + "\nsecond"
	result := formatMarkdownWindow(markdown, "doc.pdf", 1, 0, builtins.FileLimits{ReadDefaultLines: 2000, MaxLineBytes: 100})
	if !strings.Contains(result, "[...line 1 truncated at 100 bytes. Use tool_result_read(hash, line=1) to request the full line (full for file-backed reads; cached for converted documents)...]") {
		t.Errorf("expected truncation marker, got: %s", result[:min2(120, len(result))])
	}
}

// Issue #1b (defense-in-depth): a directly-inverted range does not panic and
// yields a degenerate (single-line) window rather than crashing.
func TestFormatMarkdownWindow_InvertedRangeNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("formatMarkdownWindow panicked on inverted range: %v", r)
		}
	}()
	markdown := "a\nb\nc\nd"
	// start > end, bypassing Execute-level validation (direct call).
	_ = formatMarkdownWindow(markdown, "doc.pdf", 4, 2, builtins.FileLimits{})
}

// --- docCacheKey stability ---

func TestDocCacheKey_Stable(t *testing.T) {
	k1 := docCacheKey("/path/doc.pdf", 12345, 6789)
	k2 := docCacheKey("/path/doc.pdf", 12345, 6789)
	if k1 != k2 {
		t.Error("docCacheKey should be deterministic for same inputs")
	}
}

func TestDocCacheKey_ChangesOnMtime(t *testing.T) {
	k1 := docCacheKey("/path/doc.pdf", 12345, 6789)
	k2 := docCacheKey("/path/doc.pdf", 12346, 6789)
	if k1 == k2 {
		t.Error("docCacheKey should change when mtime changes")
	}
}

func TestDocCacheKey_ChangesOnPath(t *testing.T) {
	k1 := docCacheKey("/path/a.pdf", 12345, 6789)
	k2 := docCacheKey("/path/b.pdf", 12345, 6789)
	if k1 == k2 {
		t.Error("docCacheKey should change when path changes")
	}
}

// --- Fallback on conversion failure ---

func TestExecute_FallbackOnConversionFailure(t *testing.T) {
	// When markitdown is not on PATH, converterOrInit fails and the wrapper
	// should fall back to raw read_file with a warning.
	ws := t.TempDir()
	testFile := filepath.Join(ws, "doc.pdf")
	if err := os.WriteFile(testFile, []byte("%PDF-1.4 raw bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Scope PATH to empty dir so markitdown is definitely not found.
	t.Setenv("PATH", t.TempDir())

	tool := NewReadFileDocTool(builtins.DefaultFileLimits(), nil)
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	ctx = sdktools.WithTempDir(ctx, t.TempDir())

	input, _ := json.Marshal(map[string]string{"path": "doc.pdf"})
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(result.Content, "Warning: document conversion failed") {
		t.Errorf("expected fallback warning, got: %s", result.Content)
	}
}

// --- Conversion cache hit (no markitdown needed) ---

func TestGetOrConvert_CacheHit(t *testing.T) {
	ws := t.TempDir()
	tempDir := t.TempDir()

	// Create a fake PDF file.
	pdfPath := filepath.Join(ws, "doc.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pre-populate the conversion cache with known markdown.
	info, _ := os.Stat(pdfPath)
	cacheKey := docCacheKey(pdfPath, info.ModTime().UnixNano(), info.Size())
	cacheDir := filepath.Join(tempDir, docConversionSubdir)
	_ = os.MkdirAll(cacheDir, 0o755)
	cachedMarkdown := "# Cached Title\n\nThis is cached content."
	cacheFile := filepath.Join(cacheDir, cacheKey+".md")
	if err := os.WriteFile(cacheFile, []byte(cachedMarkdown), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileDocTool(builtins.DefaultFileLimits(), nil)
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	ctx = sdktools.WithTempDir(ctx, tempDir)

	// getOrConvert should read from cache, never invoking markitdown.
	markdown, err := tool.getOrConvert(ctx, pdfPath)
	if err != nil {
		t.Fatalf("getOrConvert returned error: %v", err)
	}
	if markdown != cachedMarkdown {
		t.Errorf("expected cached markdown, got: %s", markdown)
	}
}

// --- atomicWriteFile correctness (Issue #5) ---

func TestAtomicWriteFile_WritesAndRenames(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cache.md")
	data := []byte("# converted\nbody")

	if err := atomicWriteFile(target, data, 0o644); err != nil {
		t.Fatalf("atomicWriteFile failed: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("target not readable after write: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("content mismatch: got %q", got)
	}

	// No leftover temp files should remain in the directory.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

// --- Helpers ---

// min2 is kept for backward-compatible use across the test file.
// It delegates to the stdlib min builtin (Go 1.21+).
func min2(a, b int) int {
	return min(a, b)
}
