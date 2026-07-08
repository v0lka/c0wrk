package embedding

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// fixedSizeStrings
// =============================================================================

// fixedSizeStrings is a test adapter for fixedSizePieces preserving the old
// string-slice API used throughout these tests.
func fixedSizeStrings(text string, cfg ChunkerConfig) []string {
	pieces := fixedSizePieces(piece{text: text, startLine: 1}, cfg)
	out := make([]string, 0, len(pieces))
	for _, p := range pieces {
		out = append(out, p.text)
	}
	return out
}

// assignLineNumbers is a test adapter: converts contiguous line-aligned parts
// into sections (old API preserved for these tests).
func assignLineNumbers(parts []string) []section {
	return piecesToSections(piecesFromContiguousLines(parts))
}

func TestFixedSizeStrings_WithinLimit(t *testing.T) {
	cfg := ChunkerConfig{MaxChunkSize: 100, Overlap: 20}
	result := fixedSizeStrings("short text", cfg)
	if len(result) != 1 {
		t.Fatalf("expected 1 part, got %d", len(result))
	}
	if result[0] != "short text" {
		t.Errorf("got %q, want %q", result[0], "short text")
	}
}

func TestFixedSizeStrings_LargeText(t *testing.T) {
	// Text of 250 runes with MaxChunkSize=100 and Overlap=20
	text := strings.Repeat("0123456789", 25) // 250 runes
	cfg := ChunkerConfig{MaxChunkSize: 100, Overlap: 20}
	result := fixedSizeStrings(text, cfg)

	if len(result) < 3 {
		t.Fatalf("expected at least 3 chunks, got %d", len(result))
	}

	// First chunk: 0..100
	if len([]rune(result[0])) != 100 {
		t.Errorf("first chunk length = %d, want 100", len([]rune(result[0])))
	}

	// Second chunk: 80..180 (overlap of 20)
	if len([]rune(result[1])) != 100 {
		t.Errorf("second chunk length = %d, want 100", len([]rune(result[1])))
	}

	// Third chunk: 160..250 (overlap of 20, last chunk shorter)
	if len([]rune(result[2])) != 90 {
		t.Errorf("third chunk length = %d, want 90", len([]rune(result[2])))
	}

	// Verify overlap: first chunk ends with same chars second chunk starts with
	firstChunk := []rune(result[0])
	secondChunk := []rune(result[1])
	overlapSize := 20
	for i := 0; i < overlapSize; i++ {
		if firstChunk[100-overlapSize+i] != secondChunk[i] {
			t.Errorf("overlap mismatch at position %d: %c vs %c", i,
				firstChunk[100-overlapSize+i], secondChunk[i])
		}
	}
}

func TestFixedSizeStrings_ZeroOverlap(t *testing.T) {
	text := strings.Repeat("abc", 50) // 150 runes
	cfg := ChunkerConfig{MaxChunkSize: 60, Overlap: 0}
	result := fixedSizeStrings(text, cfg)

	if len(result) != 3 {
		t.Fatalf("expected 3 chunks (60+60+30), got %d", len(result))
	}
	// Verify no overlap: concatenated chunks should equal original
	var sb strings.Builder
	for _, r := range result {
		sb.WriteString(r)
	}
	joined := sb.String()
	if joined != text {
		t.Errorf("joined chunks don't match original text")
	}
}

func TestFixedSizeStrings_ExactBoundary(t *testing.T) {
	// Text exactly at MaxChunkSize boundary — single chunk
	text := strings.Repeat("a", 100)
	cfg := ChunkerConfig{MaxChunkSize: 100, Overlap: 20}
	result := fixedSizeStrings(text, cfg)

	if len(result) != 1 {
		t.Fatalf("expected 1 chunk for exact boundary, got %d", len(result))
	}
	if len([]rune(result[0])) != 100 {
		t.Errorf("chunk length = %d, want 100", len([]rune(result[0])))
	}
}

func TestFixedSizeStrings_OneOverBoundary(t *testing.T) {
	// Text one rune over MaxChunkSize — two chunks
	text := strings.Repeat("a", 101)
	cfg := ChunkerConfig{MaxChunkSize: 100, Overlap: 20}
	result := fixedSizeStrings(text, cfg)

	if len(result) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(result))
	}
	// First chunk: 100 runes, second chunk: 21 runes (80..101 with overlap)
	if len([]rune(result[0])) != 100 {
		t.Errorf("first chunk length = %d, want 100", len([]rune(result[0])))
	}
	if len([]rune(result[1])) != 21 {
		t.Errorf("second chunk length = %d, want 21, got %d", len([]rune(result[1])), len([]rune(result[1])))
	}
}

// =============================================================================
// assignLineNumbers
// =============================================================================

func TestAssignLineNumbers_EmptySlice(t *testing.T) {
	result := assignLineNumbers(nil)
	if len(result) != 0 {
		t.Errorf("expected 0 sections for nil, got %d", len(result))
	}

	result = assignLineNumbers([]string{})
	if len(result) != 0 {
		t.Errorf("expected 0 sections for empty, got %d", len(result))
	}
}

func TestAssignLineNumbers_SingleLine(t *testing.T) {
	parts := []string{"hello world"}
	result := assignLineNumbers(parts)

	if len(result) != 1 {
		t.Fatalf("expected 1 section, got %d", len(result))
	}
	if result[0].startLine != 1 {
		t.Errorf("startLine = %d, want 1", result[0].startLine)
	}
	if result[0].endLine != 1 {
		t.Errorf("endLine = %d, want 1", result[0].endLine)
	}
	if result[0].text != "hello world" {
		t.Errorf("text = %q, want %q", result[0].text, "hello world")
	}
}

func TestAssignLineNumbers_Multiline(t *testing.T) {
	parts := []string{
		"line1\nline2\nline3",
		"line4\nline5",
		"single",
	}
	result := assignLineNumbers(parts)

	if len(result) != 3 {
		t.Fatalf("expected 3 sections, got %d", len(result))
	}

	// Section 0: 3 lines, range 1-3
	if result[0].startLine != 1 || result[0].endLine != 3 {
		t.Errorf("section 0: got %d-%d, want 1-3", result[0].startLine, result[0].endLine)
	}
	// Section 1: 2 lines, range 4-5
	if result[1].startLine != 4 || result[1].endLine != 5 {
		t.Errorf("section 1: got %d-%d, want 4-5", result[1].startLine, result[1].endLine)
	}
	// Section 2: 1 line, range 6-6
	if result[2].startLine != 6 || result[2].endLine != 6 {
		t.Errorf("section 2: got %d-%d, want 6-6", result[2].startLine, result[2].endLine)
	}
}

func TestAssignLineNumbers_EmptyPart(t *testing.T) {
	// Empty parts should be skipped (startLine advances correctly)
	parts := []string{"hello", "", "world\n!"}
	result := assignLineNumbers(parts)

	if len(result) != 2 {
		t.Fatalf("expected 2 sections (empty skipped), got %d", len(result))
	}
	if result[0].startLine != 1 || result[0].endLine != 1 {
		t.Errorf("section 0: got %d-%d, want 1-1", result[0].startLine, result[0].endLine)
	}
	if result[1].startLine != 2 || result[1].endLine != 3 {
		t.Errorf("section 1: got %d-%d, want 2-3", result[1].startLine, result[1].endLine)
	}
}

func TestAssignLineNumbers_OnlyEmptyParts(t *testing.T) {
	result := assignLineNumbers([]string{"", ""})
	if len(result) != 0 {
		t.Errorf("expected 0 sections for only empty parts, got %d", len(result))
	}
}

// =============================================================================
// lineCount
// =============================================================================

func TestLineCount_Empty(t *testing.T) {
	if n := lineCount(""); n != 0 {
		t.Errorf("lineCount(\"\") = %d, want 0", n)
	}
}

func TestLineCount_SingleLine(t *testing.T) {
	if n := lineCount("hello"); n != 1 {
		t.Errorf("lineCount(\"hello\") = %d, want 1", n)
	}
}

func TestLineCount_TrailingNewline(t *testing.T) {
	// "hello\n" → strings.Split gives ["hello", ""] → len=2, but
	// strings.Count("\n") = 1, so 1+1 = 2.
	// Behavior: trailing newline counts as an extra (empty) line.
	if n := lineCount("hello\n"); n != 2 {
		t.Errorf("lineCount(\"hello\\n\") = %d, want 2", n)
	}
}

func TestLineCount_Multiple(t *testing.T) {
	if n := lineCount("a\nb\nc"); n != 3 {
		t.Errorf("lineCount(\"a\\nb\\nc\") = %d, want 3", n)
	}
}

// =============================================================================
// splitBySingleBlanks
// =============================================================================

func TestSplitBySingleBlanks_LeadingBlanks(t *testing.T) {
	// Leading blank lines create an initial chunk, then the rest is another chunk.
	// The first blank line (current=[""]) doesn't trigger a split because len(current)>0
	// is false (current has one empty string). The second blank line sees current=["",""]
	// (non-empty after the first blank was appended to current via the else branch,
	// wait actually the first blank: TrimSpace("")=="" and len(current)==0, so we go to
	// else: current=[""]. Second blank: TrimSpace("")=="" and len(current)>0, so we
	// split off current=["",""] as a part and reset. Then the code lines form a second part.
	text := "\n\nfunc hello() {\n  return 1\n}\n"
	result := splitBySingleBlanksStrings(text)

	// Two parts: the leading blank lines and the function body
	if len(result) != 2 {
		t.Fatalf("expected 2 parts, got %d: %q", len(result), result)
	}
	if !strings.Contains(result[1], "func hello") {
		t.Errorf("second part should contain func hello, got %q", result[1])
	}
}

func TestSplitBySingleBlanks_NoBlanks(t *testing.T) {
	text := "line1\nline2\nline3"
	result := splitBySingleBlanksStrings(text)

	if len(result) != 1 {
		t.Fatalf("expected 1 part for text without blank lines, got %d", len(result))
	}
	if result[0] != text {
		t.Errorf("got %q, want %q", result[0], text)
	}
}

func TestSplitBySingleBlanks_ConsecutiveBlanks(t *testing.T) {
	// Consecutive blank lines: the first blank line group is kept with previous block,
	// then subsequent blank lines start a new block.
	text := "a\n\n\nb"
	result := splitBySingleBlanksStrings(text)

	// "a\n" → encounters "\n" (blank), appends it to current → "a\n\n"
	// Then next "\n" → TrimSpace("") is true, len(current)="a\n\n" > 0 → append, current=nil
	// Then "b" → current=["b"]
	// End: append "b"
	if len(result) != 2 {
		t.Fatalf("expected 2 parts, got %d: %q", len(result), result)
	}
}

// =============================================================================
// splitJSONTopLevel
// =============================================================================

func TestSplitJSONTopLevel_SingleKey(t *testing.T) {
	// Single-line JSON should remain as one part (no indented keys match).
	text := `{"name": "test", "version": 1}`
	result := splitJSONTopLevel(text)
	if len(result) != 1 {
		t.Fatalf("expected 1 part for compact JSON, got %d: %q", len(result), result)
	}
}

func TestSplitJSONTopLevel_MultipleKeys(t *testing.T) {
	// Indented JSON: the regex matches lines starting with exactly 2 spaces + quoted key.
	// The opening brace `{` doesn't match → it forms a separate (preamble) part.
	// Each "key": line forms a new split boundary.
	text := `{
  "name": "test",
  "version": 1,
  "dependencies": {
    "lib": "1.0"
  },
  "scripts": {
    "build": "go build"
  }
}`
	result := splitJSONTopLevel(text)

	// Expect: "{" | "  \"name\": ..." | "  \"version\": ..." | "  \"dependencies\": ..." | "  \"scripts\": ..."
	if len(result) < 4 {
		t.Fatalf("expected at least 4 parts, got %d: %q", len(result), result)
	}

	// Part 0: opening brace
	if !strings.HasPrefix(result[0], "{") {
		t.Errorf("first part should be opening brace, got %q", result[0])
	}
	// Part 1: name key
	if !strings.Contains(result[1], `"name"`) {
		t.Errorf("second part should contain name key, got %q", result[1])
	}
	// Part 2: version key
	if !strings.Contains(result[2], `"version"`) {
		t.Errorf("third part should contain version key, got %q", result[2])
	}
	// Part 3: dependencies (nested)
	if !strings.Contains(result[3], `"dependencies"`) {
		t.Errorf("fourth part should contain dependencies key, got %q", result[3])
	}
}

func TestSplitJSONTopLevel_Empty(t *testing.T) {
	// splitJSONTopLevel("") returns [""] because strings.Split("", "\n") = [""],
	// and the final "if len(current) > 0" appends the empty string.
	result := splitJSONTopLevel("")
	if len(result) != 1 || result[0] != "" {
		t.Errorf("expected [\"\"], got %q", result)
	}
}

// =============================================================================
// splitGenericConfig
// =============================================================================

func TestSplitGenericConfig_Single(t *testing.T) {
	text := "key=value"
	result := splitGenericConfig(text)
	if len(result) != 1 {
		t.Fatalf("expected 1 part, got %d", len(result))
	}
	if result[0] != "key=value" {
		t.Errorf("got %q, want %q", result[0], text)
	}
}

func TestSplitGenericConfig_Multiple(t *testing.T) {
	text := `[section]
key=value
# comment
another=true
[other]
x = 1`
	result := splitGenericConfig(text)

	// splitGenericConfig splits on top-level keys (non-space-starting, non-comment).
	// "key=value" is top-level → splits off "[section]"
	// "# comment" not top-level (starts with #) → stays with key=value
	// "another=true" is top-level → splits off "key=value\n# comment"
	// "[other]" is top-level → splits off "another=true"
	// "x = 1" is top-level → splits off "[other]"
	// Parts: "[section]", "key=value\n# comment", "another=true", "[other]", "x = 1"
	if len(result) != 5 {
		t.Fatalf("expected 5 parts, got %d: %q", len(result), result)
	}
	// First part: [section]
	if result[0] != "[section]" {
		t.Errorf("first part: got %q, want %q", result[0], "[section]")
	}
	// Third part: another=true
	if result[2] != "another=true" {
		t.Errorf("third part: got %q, want %q", result[2], "another=true")
	}
	// Fourth part: [other]
	if result[3] != "[other]" {
		t.Errorf("fourth part: got %q, want %q", result[3], "[other]")
	}
}

func TestSplitGenericConfig_Empty(t *testing.T) {
	// splitGenericConfig("") returns [""] (same as splitJSONTopLevel).
	result := splitGenericConfig("")
	if len(result) != 1 || result[0] != "" {
		t.Errorf("expected [\"\"], got %q", result)
	}
}

// =============================================================================
// ChunkFile – JSON and generic config types
// =============================================================================

func TestChunkFile_JSONLarge(t *testing.T) {
	// Large JSON file that exceeds MaxChunkSize to trigger splitJSONTopLevel path
	var b strings.Builder
	b.WriteString("{\n")
	for i := 0; i < 10; i++ {
		b.WriteString("  \"key")
		b.WriteString(strings.Repeat("x", 1))
		b.WriteString("\": \"value with lots of content ")
		b.WriteString(strings.Repeat("y", 80))
		b.WriteString("\",\n")
	}
	b.WriteString("  \"last\": true\n")
	b.WriteString("}\n")

	chunks, err := ChunkFile("/large.json", []byte(b.String()), ChunkerConfig{MaxChunkSize: 200, Overlap: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for large JSON, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Language != "json" {
			t.Errorf("expected language json, got %s", c.Language)
		}
		if c.FileName != "large.json" {
			t.Errorf("expected filename large.json, got %s", c.FileName)
		}
	}
}

func TestChunkFile_JSONSubChunk(t *testing.T) {
	// JSON small enough for single chunk (covers the "<= MaxChunkSize" branch in chunkConfig)
	content := `{"name": "test", "version": 1}`
	chunks, err := ChunkFile("/small.json", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Language != "json" {
		t.Errorf("expected language json, got %s", chunks[0].Language)
	}
}

func TestChunkFile_CompactJSONExceedsMaxSize(t *testing.T) {
	// Compact JSON on one line that exceeds MaxChunkSize.
	// splitJSONTopLevel returns 1 part (no indented keys match regex),
	// so chunkConfig falls back to fixedSizeStrings.
	text := `{"key": "` + strings.Repeat("v", 200) + `"}`
	chunks, err := ChunkFile("/compact.json", []byte(text), ChunkerConfig{MaxChunkSize: 100, Overlap: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for compact JSON exceeding MaxChunkSize, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Language != "json" {
			t.Errorf("expected language json, got %s", c.Language)
		}
	}
}

func TestChunkFile_ConfigFallbackToFixedSize(t *testing.T) {
	// Generic config where all lines are indented (not top-level).
	// splitGenericConfig returns 1 part, so chunkConfig falls back to fixedSizeStrings.
	content := "  indented line 1: " + strings.Repeat("a", 50) + "\n" +
		"  indented line 2: " + strings.Repeat("b", 50) + "\n" +
		"  indented line 3: " + strings.Repeat("c", 50) + "\n"
	chunks, err := ChunkFile("/indented.ini", []byte(content), ChunkerConfig{MaxChunkSize: 100, Overlap: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for indented config, got %d", len(chunks))
	}
}

func TestChunkFile_TOML(t *testing.T) {
	// TOML uses generic config splitting
	var b strings.Builder
	b.WriteString("[package]\n")
	b.WriteString("name = \"test\"\n")
	b.WriteString("version = \"1.0\"\n")
	b.WriteString("\n")
	b.WriteString("[dependencies]\n")
	b.WriteString("serde = \"1.0\"\n")
	b.WriteString("tokio = \"1.0\"\n")

	chunks, err := ChunkFile("/Cargo.toml", []byte(b.String()), ChunkerConfig{MaxChunkSize: 50, Overlap: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for TOML with small MaxChunkSize, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Language != "toml" {
			t.Errorf("expected language toml, got %s", c.Language)
		}
	}
}

func TestChunkFile_XML(t *testing.T) {
	// XML uses generic config splitting
	content := `<project>
  <name>test</name>
  <version>1.0</version>
  <dependencies>
    <dep>lib1</dep>
    <dep>lib2</dep>
  </dependencies>
</project>`
	chunks, err := ChunkFile("/pom.xml", []byte(content), ChunkerConfig{MaxChunkSize: 50, Overlap: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for XML with small MaxChunkSize, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Language != "xml" {
			t.Errorf("expected language xml, got %s", c.Language)
		}
	}
}

func TestChunkFile_GenericTextFile(t *testing.T) {
	// A .txt file should use fixedSizeSplit path (fileTypeOther)
	content := strings.Repeat("The quick brown fox jumps over the lazy dog.\n", 100)
	chunks, err := ChunkFile("/notes.txt", []byte(content), ChunkerConfig{MaxChunkSize: 300, Overlap: 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for large text file, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Language != "text" {
			t.Errorf("expected language text, got %s", c.Language)
		}
	}
}

func TestChunkFile_LargeMarkdown(t *testing.T) {
	// Large markdown that exceeds MaxChunkSize to test the splitBySingleBlanks fallback
	var b strings.Builder
	b.WriteString("# Title\n\n")
	b.WriteString("Intro paragraph that is quite long. ")
	b.WriteString(strings.Repeat("More intro text. ", 30))
	b.WriteString("\n\n## Section 1\n\n")
	b.WriteString(strings.Repeat("Content of section 1. ", 20))
	b.WriteString("\n\n## Section 2\n\n")
	b.WriteString(strings.Repeat("Content of section 2. ", 20))
	b.WriteString("\n")

	chunks, err := ChunkFile("/large.md", []byte(b.String()), ChunkerConfig{MaxChunkSize: 300, Overlap: 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for large markdown, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Language != "markdown" {
			t.Errorf("expected language markdown, got %s", c.Language)
		}
	}
	// First chunk should contain the H1 and intro
	if !strings.Contains(chunks[0].Content, "# Title") {
		t.Errorf("first chunk should contain H1 title")
	}
}

func TestChunkFile_LargeCode(t *testing.T) {
	// Large Go code that needs the splitOversized → fixedSizeStrings fallback
	var b strings.Builder
	b.WriteString("package main\n\n")
	b.WriteString("import \"fmt\"\n\n")
	b.WriteString("// ")
	b.WriteString(strings.Repeat("A very long comment. ", 40))
	b.WriteString("\n")
	b.WriteString("func main() {\n")
	for i := 0; i < 20; i++ {
		b.WriteString("  fmt.Println(\"line ")
		b.WriteString(strings.Repeat("x", 80))
		b.WriteString("\")\n")
	}
	b.WriteString("}\n")

	chunks, err := ChunkFile("/bigcomment.go", []byte(b.String()), ChunkerConfig{MaxChunkSize: 500, Overlap: 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for large code, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Language != "go" {
			t.Errorf("expected language go, got %s", c.Language)
		}
	}
}

func TestChunkFile_WhitespaceSectionsSkipped(t *testing.T) {
	// Code file with leading blank lines: splitBySingleBlanks produces
	// a whitespace-only first section that should be skipped by ChunkFile.
	content := "\n\npackage main\n\nimport \"fmt\"\n"
	chunks, err := ChunkFile("/leadingblanks.go", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The leading blank-line section should be skipped; only code chunks remain.
	if len(chunks) < 1 {
		t.Fatalf("expected at least 1 chunk after skipping whitespace, got %d", len(chunks))
	}
	// Verify no chunk has only whitespace content.
	for _, c := range chunks {
		if strings.TrimSpace(c.Content) == "" {
			t.Errorf("found whitespace-only chunk that should have been skipped: start=%d end=%d",
				c.StartLine, c.EndLine)
		}
	}
	// First chunk should contain "package main"
	if !strings.Contains(chunks[0].Content, "package main") {
		t.Errorf("first non-whitespace chunk should contain package main, got %q", chunks[0].Content)
	}
}

// =============================================================================
// Embedder – edge cases without ONNX
// =============================================================================

func TestEmbedder_EmbedDocuments_EmptyTexts(t *testing.T) {
	e := &Embedder{
		maxSeqLen: DefaultMaxSeqLength,
		hiddenDim: DefaultHiddenDim,
	}

	result, err := e.EmbedDocuments(context.Background(), nil)
	if err != nil {
		t.Errorf("EmbedDocuments(nil) error = %v", err)
	}
	if result != nil {
		t.Errorf("EmbedDocuments(nil) = %v, want nil", result)
	}

	result, err = e.EmbedDocuments(context.Background(), []string{})
	if err != nil {
		t.Errorf("EmbedDocuments([]) error = %v", err)
	}
	if result != nil {
		t.Errorf("EmbedDocuments([]) = %v, want nil", result)
	}
}

func TestEmbedder_EmbedDocuments_CancelledContext_EmptyTexts(t *testing.T) {
	e := &Embedder{
		maxSeqLen: DefaultMaxSeqLength,
		hiddenDim: DefaultHiddenDim,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Empty texts should return nil before checking context (early return at top)
	result, err := e.EmbedDocuments(ctx, nil)
	if err != nil {
		t.Errorf("EmbedDocuments(nil) with cancelled ctx should not check context, got error: %v", err)
	}
	if result != nil {
		t.Errorf("EmbedDocuments(nil) = %v, want nil", result)
	}
}

// =============================================================================
// Tokenizer – EncodeBatch edge cases
// =============================================================================

func TestTokenizer_EncodeBatch_MultipleTexts_MaxLenZero(t *testing.T) {
	tok := &Tokenizer{inner: nil}
	ids, mask, typeIDs := tok.EncodeBatch([]string{"hello", "world"}, 0)

	// batchSize=2, maxLen=0, totalLen=0
	if len(ids) != 0 {
		t.Errorf("inputIDs length = %d, want 0", len(ids))
	}
	if len(mask) != 0 {
		t.Errorf("attentionMask length = %d, want 0", len(mask))
	}
	if len(typeIDs) != 0 {
		t.Errorf("tokenTypeIDs length = %d, want 0", len(typeIDs))
	}
}

func TestTokenizer_EncodeBatch_MultipleTexts_MaxLenOne(t *testing.T) {
	tok := &Tokenizer{inner: nil}
	ids, mask, typeIDs := tok.EncodeBatch([]string{"a", "b", "c"}, 1)

	// batchSize=3, maxLen=1, totalLen=3 — but Encode returns nil for maxLen<2,
	// so copy(nil, nil) is a no-op, leaving zero slices.
	if len(ids) != 3 {
		t.Errorf("inputIDs length = %d, want 3", len(ids))
	}
	if len(mask) != 3 {
		t.Errorf("attentionMask length = %d, want 3", len(mask))
	}
	if len(typeIDs) != 3 {
		t.Errorf("tokenTypeIDs length = %d, want 3", len(typeIDs))
	}
	// All values should be 0 (copy from nil slices)
	for i := 0; i < 3; i++ {
		if ids[i] != 0 {
			t.Errorf("inputIDs[%d] = %d, want 0", i, ids[i])
		}
		if mask[i] != 0 {
			t.Errorf("attentionMask[%d] = %d, want 0", i, mask[i])
		}
		if typeIDs[i] != 0 {
			t.Errorf("tokenTypeIDs[%d] = %d, want 0", i, typeIDs[i])
		}
	}
}

// =============================================================================
// ChunkerConfig withDefaults edge cases
// =============================================================================

func TestChunkerConfig_WithDefaults_LargeOverlap(t *testing.T) {
	// Overlap >= MaxChunkSize should be capped to MaxChunkSize/5
	cfg := ChunkerConfig{MaxChunkSize: 100, Overlap: 200}
	cfg = cfg.withDefaults()
	if cfg.Overlap != 20 {
		t.Errorf("Overlap = %d, want 20 (MaxChunkSize/5)", cfg.Overlap)
	}
}

func TestChunkerConfig_WithDefaults_NegativeValues(t *testing.T) {
	cfg := ChunkerConfig{MaxChunkSize: -1, Overlap: -5}
	cfg = cfg.withDefaults()
	if cfg.MaxChunkSize != 1500 {
		t.Errorf("MaxChunkSize = %d, want 1500", cfg.MaxChunkSize)
	}
	if cfg.Overlap != 200 {
		t.Errorf("Overlap = %d, want 200", cfg.Overlap)
	}
}

func TestChunkerConfig_WithDefaults_OverlapTooLarge(t *testing.T) {
	// Overlap exactly equals MaxChunkSize
	cfg := ChunkerConfig{MaxChunkSize: 100, Overlap: 100}
	cfg = cfg.withDefaults()
	if cfg.Overlap != 20 {
		t.Errorf("Overlap = %d, want 20", cfg.Overlap)
	}
	if cfg.MaxChunkSize != 100 {
		t.Errorf("MaxChunkSize = %d, want 100", cfg.MaxChunkSize)
	}
}

// =============================================================================
// detectLanguage
// =============================================================================

func TestDetectLanguage_Unknown(t *testing.T) {
	if lang := detectLanguage(".unknown"); lang != "text" {
		t.Errorf("detectLanguage(.unknown) = %s, want text", lang)
	}
	if lang := detectLanguage(""); lang != "text" {
		t.Errorf("detectLanguage(\"\") = %s, want text", lang)
	}
}

func TestDetectLanguage_Known(t *testing.T) {
	tests := []struct{ ext, expected string }{
		{".go", "go"},
		{".ts", "typescript"},
		{".js", "javascript"},
		{".py", "python"},
		{".md", "markdown"},
		{".json", "json"},
		{".yaml", "yaml"},
		{".css", "css"},
		{".sh", "shell"},
	}
	for _, tt := range tests {
		if got := detectLanguage(tt.ext); got != tt.expected {
			t.Errorf("detectLanguage(%s) = %s, want %s", tt.ext, got, tt.expected)
		}
	}
}

// =============================================================================
// classifyFile
// =============================================================================

func TestClassifyFile(t *testing.T) {
	tests := []struct {
		ext      string
		expected fileType
	}{
		{".md", fileTypeMarkdown},
		{".mdx", fileTypeMarkdown},
		{".go", fileTypeCode},
		{".py", fileTypeCode},
		{".json", fileTypeConfig},
		{".yaml", fileTypeConfig},
		{".toml", fileTypeConfig},
		{".xml", fileTypeConfig},
		{".txt", fileTypeOther},
		{".unknown", fileTypeOther},
		{"", fileTypeOther},
	}
	for _, tt := range tests {
		if got := classifyFile(tt.ext); got != tt.expected {
			t.Errorf("classifyFile(%s) = %d, want %d", tt.ext, got, tt.expected)
		}
	}
}

// =============================================================================
// ChunkFile – CSS file (language detection edge case)
// =============================================================================

func TestChunkFile_CSS(t *testing.T) {
	content := `body { margin: 0; padding: 0; }
.header { font-size: 20px; }
.footer { color: gray; }`
	chunks, err := ChunkFile("/style.css", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Language != "css" {
		t.Errorf("expected language css, got %s", chunks[0].Language)
	}
	if chunks[0].FileName != "style.css" {
		t.Errorf("expected filename style.css, got %s", chunks[0].FileName)
	}
}

// =============================================================================
// NewTokenizer – error path
// =============================================================================

func TestNewTokenizer_InvalidFile(t *testing.T) {
	// Point to a file that doesn't contain valid tokenizer JSON.
	tmp := t.TempDir()
	invalidPath := tmp + "/not_a_tokenizer.json"
	if err := os.WriteFile(invalidPath, []byte("not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	tok, err := NewTokenizer(invalidPath)
	if err == nil {
		t.Error("NewTokenizer with invalid file should return error")
	}
	if tok != nil {
		t.Error("NewTokenizer with invalid file should return nil tokenizer")
	}
}

func TestNewTokenizer_NonexistentFile(t *testing.T) {
	tok, err := NewTokenizer("/nonexistent/path/tokenizer.json")
	if err == nil {
		t.Error("NewTokenizer with nonexistent file should return error")
	}
	if tok != nil {
		t.Error("NewTokenizer with nonexistent file should return nil tokenizer")
	}
}

// =============================================================================
// Embedder – closed state and edge cases
// =============================================================================

func TestEmbedder_EmbeddingFunc_Nil(t *testing.T) {
	// EmbeddingFunc always returns a non-nil function regardless of embedder state.
	e := &Embedder{
		tokenizer: nil,
		maxSeqLen: DefaultMaxSeqLength,
		hiddenDim: DefaultHiddenDim,
	}
	fn := e.EmbeddingFunc()
	if fn == nil {
		t.Fatal("EmbeddingFunc() returned nil")
	}
}

// =============================================================================
// Embedder.Close – on uninitialized embedder (no ONNX env)
// =============================================================================

func TestEmbedder_Close_NoONNXEnv(t *testing.T) {
	// Closing an embedder that was manually constructed (no ONNX env initialized)
	// should return an error from destroyONNXRuntime since the env was never created.
	e := &Embedder{
		tokenizer: nil,
		maxSeqLen: DefaultMaxSeqLength,
		hiddenDim: DefaultHiddenDim,
		sess:      nil,
		logger:    slog.New(slog.DiscardHandler),
	}
	err := e.Close()
	// Expect an error because ONNX environment is not initialized.
	if err == nil {
		t.Error("Close() on uninitialized embedder should return error")
	}
}

func TestEmbedder_Close_WithTokenizerNil(t *testing.T) {
	// Close with nil tokenizer and nil sess – should attempt destroyONNXRuntime.
	e := &Embedder{
		tokenizer: nil,
		maxSeqLen: DefaultMaxSeqLength,
		hiddenDim: DefaultHiddenDim,
		sess:      nil,
		logger:    slog.New(slog.DiscardHandler),
	}
	err := e.Close()
	if err == nil {
		t.Error("Close() should return error when ONNX env not initialized")
	}
	// After close, tokenizer is still nil (was nil before).
	if e.tokenizer != nil {
		t.Error("tokenizer should still be nil after close")
	}
	if e.sess != nil {
		t.Error("sess should still be nil after close")
	}
}

// =============================================================================
// splitGenericConfig – more edge cases
// =============================================================================

func TestSplitGenericConfig_SemicolonComments(t *testing.T) {
	// Lines starting with ';' are treated as comments and don't trigger a split.
	text := "[main]\nkey=val\n; this is a comment\nnext=1"
	result := splitGenericConfig(text)
	// Parts: "[main]", "key=val\n; this is a comment", "next=1"
	if len(result) != 3 {
		t.Fatalf("expected 3 parts, got %d: %q", len(result), result)
	}
	if result[0] != "[main]" {
		t.Errorf("first part: got %q, want %q", result[0], "[main]")
	}
	if !strings.Contains(result[1], "; this is a comment") {
		t.Errorf("second part should contain semicolon comment, got %q", result[1])
	}
}

func TestSplitGenericConfig_TabIndented(t *testing.T) {
	// Tab-indented lines should not trigger a split (they're not top-level).
	text := "section\n\tkey=tab\n\tval=1\nanother=top"
	result := splitGenericConfig(text)
	// Parts: "section\n\tkey=tab\n\tval=1", "another=top"
	if len(result) != 2 {
		t.Fatalf("expected 2 parts, got %d: %q", len(result), result)
	}
	if !strings.Contains(result[0], "\tkey=tab") {
		t.Errorf("first part should contain tab-indented key, got %q", result[0])
	}
	if result[1] != "another=top" {
		t.Errorf("second part: got %q, want %q", result[1], "another=top")
	}
}

func TestSplitGenericConfig_LeadingComment(t *testing.T) {
	// A file starting with a comment line: the comment line is NOT top-level
	// (starts with '#'), but the following "key=value" IS top-level, causing
	// a split. The comment becomes its own part.
	text := "# header comment\nkey=value"
	result := splitGenericConfig(text)
	// Parts: "# header comment", "key=value"
	if len(result) != 2 {
		t.Fatalf("expected 2 parts, got %d: %q", len(result), result)
	}
	if result[0] != "# header comment" {
		t.Errorf("first part: got %q, want %q", result[0], "# header comment")
	}
	if result[1] != "key=value" {
		t.Errorf("second part: got %q, want %q", result[1], "key=value")
	}
}

func TestSplitGenericConfig_AllBlankLines(t *testing.T) {
	// Multiple blank lines — should not create spurious splits.
	text := "\n\n\n"
	result := splitGenericConfig(text)
	// Blank lines are not top-level (TrimSpace is ""), so they don't split.
	// Should be 1 part containing all blank lines.
	if len(result) != 1 {
		t.Fatalf("expected 1 part for blank lines, got %d: %q", len(result), result)
	}
}

// =============================================================================
// splitYAMLTopLevel – more edge cases
// =============================================================================

func TestSplitYAMLTopLevel_Empty(t *testing.T) {
	result := splitYAMLTopLevel("")
	if len(result) != 1 || result[0] != "" {
		t.Errorf("expected [\"\"], got %q", result)
	}
}

func TestSplitYAMLTopLevel_SingleKey(t *testing.T) {
	text := "name: test"
	result := splitYAMLTopLevel(text)
	if len(result) != 1 {
		t.Fatalf("expected 1 part, got %d: %q", len(result), result)
	}
	if result[0] != "name: test" {
		t.Errorf("got %q, want %q", result[0], text)
	}
}

// =============================================================================
// Embedder.EmbeddingFunc – calling returned func with closed embedder
// =============================================================================

func TestEmbedder_EmbeddingFunc_CallOnClosed(t *testing.T) {
	e := &Embedder{
		tokenizer: nil,
		maxSeqLen: DefaultMaxSeqLength,
		hiddenDim: DefaultHiddenDim,
		logger:    slog.New(slog.DiscardHandler),
	}
	fn := e.EmbeddingFunc()
	if fn == nil {
		t.Fatal("EmbeddingFunc() returned nil")
	}
	// Calling the returned function should propagate the "embedder is closed" error.
	_, err := fn(context.Background(), "test")
	if err == nil {
		t.Error("calling EmbeddingFunc on closed embedder should return error")
	}
}

// =============================================================================
// Embedder.EmbedDocuments – closed embedder with non-empty texts
// =============================================================================

func TestEmbedder_EmbedDocuments_ClosedEmbedder(t *testing.T) {
	// An embedder with nil tokenizer is considered "closed".
	// EmbedDocuments should return an error for non-empty texts.
	e := &Embedder{
		tokenizer: nil,
		maxSeqLen: DefaultMaxSeqLength,
		hiddenDim: DefaultHiddenDim,
		logger:    slog.New(slog.DiscardHandler),
	}
	_, err := e.EmbedDocuments(context.Background(), []string{"test"})
	if err == nil {
		t.Error("EmbedDocuments with closed embedder should return error")
	}
}

func TestEmbedder_EmbedDocuments_Closed_EmptyTexts(t *testing.T) {
	// Empty texts should return nil before checking tokenizer (early return).
	e := &Embedder{
		tokenizer: nil,
		maxSeqLen: DefaultMaxSeqLength,
		hiddenDim: DefaultHiddenDim,
		logger:    slog.New(slog.DiscardHandler),
	}
	result, err := e.EmbedDocuments(context.Background(), []string{})
	if err != nil {
		t.Errorf("EmbedDocuments([]) with closed embedder should not error, got: %v", err)
	}
	if result != nil {
		t.Errorf("EmbedDocuments([]) should return nil, got: %v", result)
	}
}

// =============================================================================
// ChunkFile – additional file types
// =============================================================================

func TestChunkFile_Shell(t *testing.T) {
	content := "#!/bin/bash\necho hello\n"
	chunks, err := ChunkFile("/script.sh", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Language != "shell" {
		t.Errorf("expected language shell, got %s", chunks[0].Language)
	}
}

func TestChunkFile_UnknownExtension(t *testing.T) {
	content := "just some text\nmore text\n"
	chunks, err := ChunkFile("/data.xyz", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Language != "text" {
		t.Errorf("expected language text, got %s", chunks[0].Language)
	}
}

func TestChunkFile_NoExtension(t *testing.T) {
	content := "Dockerfile content here\n"
	chunks, err := ChunkFile("/Dockerfile", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	// No extension → fileTypeOther → fixedSizeSplit
	if chunks[0].Language != "text" {
		t.Errorf("expected language text, got %s", chunks[0].Language)
	}
}

func TestChunkFile_Python(t *testing.T) {
	content := "def hello():\n    print('hello')\n\n\ndef world():\n    print('world')\n"
	chunks, err := ChunkFile("/app.py", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks (split by blank line), got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Language != "python" {
			t.Errorf("expected language python, got %s", c.Language)
		}
	}
}

func TestChunkFile_Java(t *testing.T) {
	content := `public class Foo {
  void bar() { }
}

public class Baz {
  void qux() { }
}
`
	chunks, err := ChunkFile("/Foo.java", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Language != "java" {
			t.Errorf("expected language java, got %s", c.Language)
		}
	}
}

func TestChunkFile_MarkdownNoH2(t *testing.T) {
	// Markdown file without any H2 headers → single chunk
	content := "# Just a title\n\nSome text here.\n"
	chunks, err := ChunkFile("/note.md", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for markdown without H2, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Content, "# Just a title") {
		t.Errorf("chunk should contain H1 title")
	}
}

func TestChunkFile_EnvFile(t *testing.T) {
	content := "DB_HOST=localhost\nDB_PORT=5432\n"
	chunks, err := ChunkFile("/.env", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	// .env is a config file type (in configExtensions), uses generic config split
	if chunks[0].Language != "text" {
		t.Errorf("expected language text (no mapping for .env), got %s", chunks[0].Language)
	}
}

// =============================================================================
// ChunkFile – markdown with H2 followed by single blank, then content
// =============================================================================

func TestChunkFile_MarkdownH2WithContentAfterBlank(t *testing.T) {
	// H2 header followed by a blank line, then content → header stays with content
	content := "# Title\n\nIntro.\n\n## Section\n\nContent here.\n"
	chunks, err := ChunkFile("/doc.md", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks (preamble + 1 H2), got %d", len(chunks))
	}
	if !strings.HasPrefix(chunks[0].Content, "# Title") {
		t.Errorf("first chunk should start with H1")
	}
	if !strings.HasPrefix(chunks[1].Content, "## Section") {
		t.Errorf("second chunk should start with H2")
	}
	if !strings.Contains(chunks[1].Content, "Content here") {
		t.Errorf("second chunk should contain content after H2")
	}
}

// =============================================================================
// Embedder.EmbedDocuments – cancelled context AFTER lock acquisition
// =============================================================================

func TestEmbedder_EmbedDocuments_CancelledAfterLock(t *testing.T) {
	// The second context check (after mu.Lock()) should detect cancellation
	// and return before reaching the tokenizer/ONNX path.
	e := &Embedder{
		tokenizer: &Tokenizer{}, // non-nil to get past the "closed" guard
		maxSeqLen: DefaultMaxSeqLength,
		hiddenDim: DefaultHiddenDim,
		logger:    slog.New(slog.DiscardHandler),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before call

	// Lock externally so EmbedDocuments blocks on Lock().
	e.mu.Lock()

	errCh := make(chan error, 1)
	go func() {
		_, err := e.EmbedDocuments(ctx, []string{"test"})
		errCh <- err
	}()

	// Give the goroutine time to reach the Lock call.
	time.Sleep(20 * time.Millisecond)

	// Unlock — the goroutine acquires the lock, then checks ctx.Done() → cancelled.
	e.mu.Unlock()

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("EmbedDocuments after cancelled context (post-lock) should return error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for EmbedDocuments to return")
	}
}

// =============================================================================
// Embedder.Close – with non-nil onnxSession (covers the sess.destroy branch)
// =============================================================================

func TestEmbedder_Close_WithSession(t *testing.T) {
	// When sess is non-nil, Close should call sess.destroy() and set sess=nil.
	// Using an empty onnxSession (all nil fields) is safe — destroy() checks
	// each field before calling Destroy.
	e := &Embedder{
		tokenizer: &Tokenizer{}, // non-nil so we can verify it stays
		maxSeqLen: DefaultMaxSeqLength,
		hiddenDim: DefaultHiddenDim,
		logger:    slog.New(slog.DiscardHandler),
		sess:      &onnxSession{},
	}
	err := e.Close()
	if err == nil {
		t.Error("Close() on uninitialized ONNX env should return error")
	}
	// sess should be set to nil after destroy.
	if e.sess != nil {
		t.Error("sess should be nil after Close")
	}
	// tokenizer is cleared on Close so subsequent Embed calls fail fast
	// instead of touching the destroyed ONNX environment.
	if e.tokenizer != nil {
		t.Error("tokenizer should be nil after Close (marks embedder closed)")
	}
	// EmbedDocuments after Close must return an error, not run inference.
	if _, embedErr := e.EmbedDocuments(context.Background(), []string{"x"}); embedErr == nil {
		t.Error("EmbedDocuments after Close should return an error")
	}
}

// =============================================================================
// Embedder.EmbedQuery – empty results path (len(results) == 0)
// =============================================================================

func TestEmbedder_EmbedQuery_EmptyResults(t *testing.T) {
	// EmbedQuery returns "no embedding returned" error when EmbedDocuments
	// returns empty results without error. This path is reached when
	// called with empty input (which can't happen via EmbedQuery directly
	// since it always passes a non-empty slice).
	// We verify the error propagation from the closed embedder path.
	e := &Embedder{
		tokenizer: nil,
		maxSeqLen: DefaultMaxSeqLength,
		hiddenDim: DefaultHiddenDim,
		logger:    slog.New(slog.DiscardHandler),
	}
	_, err := e.EmbedQuery(context.Background(), "")
	if err == nil {
		t.Error("EmbedQuery with closed embedder should return error")
	}
}

// =============================================================================
// ChunkerConfig.withDefaults – Overlap == MaxChunkSize/5 (exact boundary)
// =============================================================================

func TestChunkerConfig_WithDefaults_OverlapEqualsOneFifth(t *testing.T) {
	// When Overlap is exactly MaxChunkSize/5, it should be kept as-is.
	cfg := ChunkerConfig{MaxChunkSize: 500, Overlap: 100}
	cfg = cfg.withDefaults()
	if cfg.MaxChunkSize != 500 {
		t.Errorf("MaxChunkSize = %d, want 500", cfg.MaxChunkSize)
	}
	if cfg.Overlap != 100 {
		t.Errorf("Overlap = %d, want 100", cfg.Overlap)
	}
}

func TestChunkerConfig_WithDefaults_ZeroMaxChunkSize(t *testing.T) {
	// Zero MaxChunkSize with valid Overlap — MaxChunkSize gets default,
	// then Overlap is checked against the new default.
	cfg := ChunkerConfig{MaxChunkSize: 0, Overlap: 200}
	cfg = cfg.withDefaults()
	if cfg.MaxChunkSize != 1500 {
		t.Errorf("MaxChunkSize = %d, want 1500", cfg.MaxChunkSize)
	}
	if cfg.Overlap != 200 {
		t.Errorf("Overlap = %d, want 200 (unchanged, below default MaxChunkSize/5=300)", cfg.Overlap)
	}
}

func TestChunkerConfig_WithDefaults_BothZero(t *testing.T) {
	// Both fields zero — should get both defaults.
	cfg := ChunkerConfig{MaxChunkSize: 0, Overlap: 0}
	cfg = cfg.withDefaults()
	if cfg.MaxChunkSize != 1500 {
		t.Errorf("MaxChunkSize = %d, want 1500", cfg.MaxChunkSize)
	}
	if cfg.Overlap != 200 {
		t.Errorf("Overlap = %d, want 200", cfg.Overlap)
	}
}

func TestEmbedder_EmbedQuery_NilTokenizer(t *testing.T) {
	e := &Embedder{tokenizer: nil, maxSeqLen: 512, hiddenDim: 512}
	_, err := e.EmbedQuery(context.Background(), "test")
	if err == nil {
		t.Error("expected error for nil tokenizer")
	}
}

// splitBySingleBlanksStrings is a test adapter for splitBySingleBlanks
// preserving the old string-slice API used in these tests.
func splitBySingleBlanksStrings(text string) []string {
	pieces := splitBySingleBlanks(piece{text: text, startLine: 1}, ChunkerConfig{})
	out := make([]string, 0, len(pieces))
	for _, p := range pieces {
		out = append(out, p.text)
	}
	return out
}

// =============================================================================
// R5 regression: line numbers after mid-line fixed-size splits
// =============================================================================

func TestChunkFile_OversizedChunk_LineNumbersStayAligned(t *testing.T) {
	// Build a code file with one oversized paragraph (no blank lines inside)
	// followed by a normal paragraph. The oversized paragraph forces a
	// fixed-size split that lands mid-line; subsequent sections must still
	// report line numbers matching the original file.
	var sb strings.Builder
	const bigLines = 20
	for i := 1; i <= bigLines; i++ {
		// Each line is 40 chars incl. newline → 20 lines ≈ 800 runes.
		sb.WriteString(strings.Repeat("x", 37))
		sb.WriteString("//\n")
	}
	sb.WriteString("\n")             // blank line 21 ends the paragraph
	sb.WriteString("func tail() {}") // line 22
	text := sb.String()

	cfg := ChunkerConfig{MaxChunkSize: 300, Overlap: 50}
	chunks, err := ChunkFile("/tmp/file.go", []byte(text), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 3 {
		t.Fatalf("expected >=3 chunks, got %d", len(chunks))
	}

	lines := strings.Split(text, "\n")
	for i, c := range chunks {
		if c.StartLine < 1 || c.EndLine > len(lines) {
			t.Fatalf("chunk %d: line range %d-%d out of file bounds (1-%d)", i, c.StartLine, c.EndLine, len(lines))
		}
		// The chunk's first line fragment must be a substring of the
		// original line it claims to start on.
		firstLine := strings.SplitN(c.Content, "\n", 2)[0]
		if firstLine != "" && !strings.Contains(lines[c.StartLine-1], firstLine) {
			t.Errorf("chunk %d: StartLine=%d but content's first line %q not found in original line %q",
				i, c.StartLine, firstLine, lines[c.StartLine-1])
		}
		// Same for the last line fragment and EndLine.
		parts := strings.Split(c.Content, "\n")
		lastLine := parts[len(parts)-1]
		if lastLine != "" && !strings.Contains(lines[c.EndLine-1], lastLine) {
			t.Errorf("chunk %d: EndLine=%d but content's last line %q not found in original line %q",
				i, c.EndLine, lastLine, lines[c.EndLine-1])
		}
	}

	// The final chunk (tail paragraph) must start at line 22.
	last := chunks[len(chunks)-1]
	if !strings.Contains(last.Content, "func tail") {
		t.Fatalf("last chunk should contain tail function, got %q", last.Content)
	}
	if last.StartLine > 22 || last.EndLine < 22 {
		t.Errorf("tail chunk line range %d-%d must include line 22", last.StartLine, last.EndLine)
	}
}
