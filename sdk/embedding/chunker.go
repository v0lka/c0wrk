// Package embedding provides file chunking and embedding utilities for local vector search.
package embedding

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Chunk represents a segment of a file with location metadata.
type Chunk struct {
	Content   string // the actual text content
	FilePath  string // absolute path to source file
	FileName  string // basename of the file
	StartLine int    // 1-based start line in original file
	EndLine   int    // 1-based end line in original file
	Language  string // detected language/type (e.g., "go", "typescript", "markdown", "yaml", "json", "text")
}

// ChunkerConfig controls chunking behavior.
type ChunkerConfig struct {
	MaxChunkSize int // max characters per chunk (default: 1500)
	Overlap      int // character overlap for fixed splits (default: 200)
}

func (c ChunkerConfig) withDefaults() ChunkerConfig {
	if c.MaxChunkSize <= 0 {
		c.MaxChunkSize = 1500
	}
	if c.Overlap <= 0 {
		c.Overlap = 200
	}
	if c.Overlap >= c.MaxChunkSize {
		c.Overlap = c.MaxChunkSize / 5
	}
	return c
}

// ComputeFileHash returns the SHA-256 hex digest of the content.
func ComputeFileHash(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

// ChunkFile splits a file's content into semantically meaningful chunks.
func ChunkFile(filePath string, content []byte, cfg ChunkerConfig) ([]Chunk, error) {
	cfg = cfg.withDefaults()

	// Empty file
	if len(content) == 0 {
		return nil, nil
	}

	// Binary detection: null bytes in first 512 bytes
	checkLen := len(content)
	if checkLen > 512 {
		checkLen = 512
	}
	for i := 0; i < checkLen; i++ {
		if content[i] == 0 {
			return nil, nil
		}
	}

	text := string(content)
	ext := strings.ToLower(filepath.Ext(filePath))
	lang := detectLanguage(ext)
	fileName := filepath.Base(filePath)

	var sections []section
	switch classifyFile(ext) {
	case fileTypeCode:
		sections = chunkCode(text, cfg)
	case fileTypeMarkdown:
		sections = chunkMarkdown(text, cfg)
	case fileTypeConfig:
		sections = chunkConfig(text, ext, cfg)
	default:
		sections = fixedSizeSplit(text, cfg)
	}

	chunks := make([]Chunk, 0, len(sections))
	for _, s := range sections {
		if strings.TrimSpace(s.text) == "" {
			continue
		}
		chunks = append(chunks, Chunk{
			Content:   s.text,
			FilePath:  filePath,
			FileName:  fileName,
			StartLine: s.startLine,
			EndLine:   s.endLine,
			Language:  lang,
		})
	}
	return chunks, nil
}

type section struct {
	text      string
	startLine int
	endLine   int
}

type fileType int

const (
	fileTypeCode fileType = iota
	fileTypeMarkdown
	fileTypeConfig
	fileTypeOther
)

var codeExtensions = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".py": true, ".rs": true, ".java": true, ".c": true, ".cpp": true,
	".h": true, ".rb": true, ".swift": true, ".kt": true,
}

var configExtensions = map[string]bool{
	".json": true, ".yaml": true, ".yml": true, ".toml": true,
	".xml": true, ".env": true, ".ini": true, ".properties": true,
}

var languageMap = map[string]string{
	".go": "go", ".ts": "typescript", ".tsx": "typescript",
	".js": "javascript", ".jsx": "javascript", ".py": "python",
	".rs": "rust", ".java": "java", ".rb": "ruby",
	".c": "c", ".h": "c", ".cpp": "cpp", ".swift": "swift", ".kt": "kotlin",
	".md": "markdown", ".mdx": "markdown",
	".json": "json", ".yaml": "yaml", ".yml": "yaml",
	".toml": "toml", ".xml": "xml", ".html": "html", ".css": "css",
	".sql": "sql", ".sh": "shell", ".bash": "shell", ".dockerfile": "dockerfile",
}

func detectLanguage(ext string) string {
	if lang, ok := languageMap[ext]; ok {
		return lang
	}
	return "text"
}

func classifyFile(ext string) fileType {
	if ext == ".md" || ext == ".mdx" {
		return fileTypeMarkdown
	}
	if codeExtensions[ext] {
		return fileTypeCode
	}
	if configExtensions[ext] {
		return fileTypeConfig
	}
	return fileTypeOther
}

// lineCount returns the number of lines in s (at least 1 for non-empty).
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// chunkCode splits code by double-blank-line boundaries, then blank lines, then fixed.
func chunkCode(text string, cfg ChunkerConfig) []section {
	// Split on double blank lines (two or more consecutive empty lines)
	parts := splitByDoubleBlanks(text)
	parts = splitOversized(parts, cfg, splitBySingleBlanks)
	parts = splitOversized(parts, cfg, fixedSizeStrings)
	return assignLineNumbers(parts)
}

// splitByDoubleBlanks splits text at boundaries where 2+ consecutive blank lines appear.
var doubleBlankRe = regexp.MustCompile(`\n\s*\n\s*\n`)

func splitByDoubleBlanks(text string) []string {
	indices := doubleBlankRe.FindAllStringIndex(text, -1)
	if len(indices) == 0 {
		return []string{text}
	}

	var parts []string
	prev := 0
	for _, idx := range indices {
		parts = append(parts, text[prev:idx[0]+1]) // include the first \n
		prev = idx[1]
	}
	if prev < len(text) {
		parts = append(parts, text[prev:])
	}
	return parts
}

func splitBySingleBlanks(text string, _ ChunkerConfig) []string {
	lines := strings.Split(text, "\n")
	var parts []string
	var current []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" && len(current) > 0 {
			current = append(current, line)
			parts = append(parts, strings.Join(current, "\n"))
			current = nil
		} else {
			current = append(current, line)
		}
	}
	if len(current) > 0 {
		parts = append(parts, strings.Join(current, "\n"))
	}
	return parts
}

func splitOversized(parts []string, cfg ChunkerConfig, splitter func(string, ChunkerConfig) []string) []string {
	var result []string
	for _, p := range parts {
		if utf8.RuneCountInString(p) > cfg.MaxChunkSize {
			result = append(result, splitter(p, cfg)...)
		} else {
			result = append(result, p)
		}
	}
	return result
}

func fixedSizeStrings(text string, cfg ChunkerConfig) []string {
	runes := []rune(text)
	if len(runes) <= cfg.MaxChunkSize {
		return []string{text}
	}
	var parts []string
	for i := 0; i < len(runes); {
		end := i + cfg.MaxChunkSize
		if end > len(runes) {
			end = len(runes)
		}
		parts = append(parts, string(runes[i:end]))
		if end == len(runes) {
			break
		}
		i = end - cfg.Overlap
		if i < 0 {
			i = 0
		}
	}
	return parts
}

func assignLineNumbers(parts []string) []section {
	sections := make([]section, 0, len(parts))
	currentLine := 1
	for _, p := range parts {
		if p == "" {
			continue
		}
		lc := lineCount(p)
		sections = append(sections, section{
			text:      p,
			startLine: currentLine,
			endLine:   currentLine + lc - 1,
		})
		currentLine += lc
	}
	return sections
}

// chunkMarkdown splits markdown by H2 headers.
func chunkMarkdown(text string, cfg ChunkerConfig) []section {
	lines := strings.Split(text, "\n")
	var parts []string
	var current []string //nolint:prealloc // false positive: conditionally appended

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") && len(current) > 0 {
			parts = append(parts, strings.Join(current, "\n"))
			current = nil
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		parts = append(parts, strings.Join(current, "\n"))
	}

	// Handle oversized sections
	parts = splitOversized(parts, cfg, splitBySingleBlanks)
	parts = splitOversized(parts, cfg, fixedSizeStrings)

	return assignLineNumbers(parts)
}

// chunkConfig splits config files by top-level keys if oversized.
func chunkConfig(text, ext string, cfg ChunkerConfig) []section {
	if utf8.RuneCountInString(text) <= cfg.MaxChunkSize {
		return assignLineNumbers([]string{text})
	}

	var parts []string
	switch ext {
	case ".json":
		parts = splitJSONTopLevel(text)
	case ".yaml", ".yml":
		parts = splitYAMLTopLevel(text)
	default:
		parts = splitGenericConfig(text)
	}

	// Fallback if splitting didn't help
	if len(parts) <= 1 {
		fixedParts := fixedSizeStrings(text, cfg)
		return assignLineNumbers(fixedParts)
	}

	parts = splitOversized(parts, cfg, fixedSizeStrings)

	return assignLineNumbers(parts)
}

var jsonTopLevelKeyRe = regexp.MustCompile(`^ {2}"([^"]+)"\s*:`)

func splitJSONTopLevel(text string) []string {
	lines := strings.Split(text, "\n")
	var parts []string
	var current []string //nolint:prealloc // false positive: conditionally appended

	for _, line := range lines {
		if jsonTopLevelKeyRe.MatchString(line) && len(current) > 0 {
			parts = append(parts, strings.Join(current, "\n"))
			current = nil
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		parts = append(parts, strings.Join(current, "\n"))
	}
	return parts
}

var yamlTopLevelKeyRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_\-]*\s*:`)

func splitYAMLTopLevel(text string) []string {
	lines := strings.Split(text, "\n")
	var parts []string
	var current []string //nolint:prealloc // false positive: conditionally appended

	for i, line := range lines {
		if i > 0 && yamlTopLevelKeyRe.MatchString(line) && len(current) > 0 {
			parts = append(parts, strings.Join(current, "\n"))
			current = nil
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		parts = append(parts, strings.Join(current, "\n"))
	}
	return parts
}

func splitGenericConfig(text string) []string {
	lines := strings.Split(text, "\n")
	var parts []string
	var current []string //nolint:prealloc // false positive: conditionally appended

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		isTopLevel := i > 0 && line != "" && line[0] != ' ' && line[0] != '\t' &&
			trimmed != "" && trimmed[0] != '#' && trimmed[0] != ';'
		if isTopLevel && len(current) > 0 {
			parts = append(parts, strings.Join(current, "\n"))
			current = nil
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		parts = append(parts, strings.Join(current, "\n"))
	}
	return parts
}

// fixedSizeSplit splits text into fixed-size chunks with overlap.
func fixedSizeSplit(text string, cfg ChunkerConfig) []section {
	parts := fixedSizeStrings(text, cfg)
	return assignLineNumbers(parts)
}
