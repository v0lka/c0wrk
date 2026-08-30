package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/core/markitdown"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// docConvertTimeout is the per-file budget for a single markitdown conversion.
// Matches the attachment conversion timeout used in manager_attachment.go.
const docConvertTimeout = 2 * time.Minute

// docConversionSubdir is the directory under the session temp dir where
// converted markdown representations of documents are cached.
const docConversionSubdir = "conversions"

// docExts is the set of document/binary extensions that the markitdown wrapper
// converts to markdown. Plain-text formats (txt, md, json, xml, csv, tsv, rst)
// are deliberately excluded — they are read natively by the inner read_file.
var docExts = map[string]struct{}{
	"pdf":  {},
	"docx": {},
	"pptx": {},
	"xlsx": {},
	"odt":  {},
	"html": {},
	"htm":  {},
}

// docFileDescription augments the sp4rk read_file description with explicit
// mention of supported document formats.
const docFileDescription = `Purpose: read a file's contents by path, optionally a line range; document formats (pdf, docx, pptx, xlsx, odt, html, htm) are transparently converted to markdown for readability.
Use when: you already know the exact path — discover paths first with list_directory, glob or ripgrep, then open the file here.
Inputs: path (absolute or workspace-relative); optional start_line / end_line (1-based, inclusive) to read a portion of a large file or converted document.
Outputs: content with a metadata header (file name, returned range, total line count); when content remains beyond the range, a continuation hint names the next start_line — continue with another read_file call; an over-long single line is truncated with a hash marker — recover it via tool_result_read(line=N).
Example: start_line 100, end_line 250 reads the second chunk of a long file.
Anti-example: not for pattern search (ripgrep), meaning search (semantic_search) or name search (glob); do not read huge files whole — paginate.`

// isDocExt reports whether the file at path has a document/binary extension
// that should be converted to markdown via markitdown.
func isDocExt(path string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	_, ok := docExts[ext]
	return ok
}

// ReadFileDocTool wraps the sp4rk ReadFileTool to transparently convert
// document formats (pdf, docx, pptx, etc.) to markdown via the markitdown CLI.
// Plain-text files are delegated to the inner ReadFileTool unchanged.
//
// It implements sdktools.ContentBackedReader so the executor caches converted
// results in memory (content-backed) instead of streaming raw bytes from disk.
type ReadFileDocTool struct {
	*builtins.ReadFileTool
	limits           builtins.FileLimits
	log              *slog.Logger
	converter        *markitdown.Converter
	converterHasPyth bool // converter was built with a resolved venv interpreter
	convMu           sync.Mutex
	markitdownPython func() string // lazily resolves the managed venv interpreter for vision-assisted conversion; nil/"" = disabled
}

// NewReadFileDocTool creates a read_file wrapper that converts document formats
// to markdown. The inner sp4rk ReadFileTool is created with the given limits
// and used for plain-text files and as a fallback on conversion errors. A nil
// logger is replaced with a discard handler so the tool never panics on a
// logging call.
//
// markitdownPython lazily resolves the managed venv interpreter
// (toolmanager.VenvPythonPath). The tool-manager installs that venv
// asynchronously after application startup, so the path is probed at the
// FIRST converter init (i.e. the first document read) rather than at
// construction: a probe baked in at app start would see an empty path on
// fresh installs and silently disable vision-assisted conversion until the
// app restarts. A nil probe or an empty result keeps the plain CLI path.
// When the resolved interpreter is available AND the execution context
// carries a vision resolver whose current model is vision-capable, document
// conversions run through the markitdown Python API so embedded images are
// captioned by that model (per-document: the model is re-resolved on every
// conversion).
func NewReadFileDocTool(limits builtins.FileLimits, logger *slog.Logger, markitdownPython func() string) *ReadFileDocTool {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &ReadFileDocTool{
		ReadFileTool:     builtins.NewReadFileToolWithLimits(limits),
		limits:           limits,
		log:              logger,
		markitdownPython: markitdownPython,
	}
}

// Description returns an augmented description that informs the agent about
// supported document formats.
func (t *ReadFileDocTool) Description() string {
	return docFileDescription
}

// IsContentBacked reports whether the read of the file described by input
// produces a converted markdown view that must be cached in memory.
// Returns true only for document/binary formats (pdf, docx, etc.); plain-text
// files delegate to the inner read_file (file-backed, the default).
func (t *ReadFileDocTool) IsContentBacked(_ context.Context, input json.RawMessage) bool {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &params); err != nil || params.Path == "" {
		return false
	}
	return isDocExt(params.Path)
}

// Execute reads a file. For document/binary formats it converts the file to
// markdown via markitdown (cached per session), applies the requested line
// range, and returns the window. For plain-text files it delegates to the
// inner sp4rk ReadFileTool. On conversion failure it falls back to the inner
// ReadFileTool with a warning prepended.
func (t *ReadFileDocTool) Execute(ctx context.Context, input json.RawMessage) (sdktools.ToolResult, error) {
	var params builtins.ReadFileInput
	if err := json.Unmarshal(input, &params); err != nil {
		return sdktools.ParseInputError(err)
	}

	// Non-document files delegate to the inner read_file unchanged (it performs
	// its own range validation).
	if !isDocExt(params.Path) {
		return t.ReadFileTool.Execute(ctx, input)
	}

	// Validate the line range up front for the document branch, mirroring the
	// inner read_file. Without this, an inverted range (start_line > end_line)
	// would reach the window slicing logic and panic on an out-of-range slice.
	if params.StartLine < 0 {
		return sdktools.ToolResult{Content: fmt.Sprintf("validation error: start_line must be >= 1, got %d", params.StartLine), IsError: true}, nil
	}
	if params.EndLine < 0 {
		return sdktools.ToolResult{Content: fmt.Sprintf("validation error: end_line must be >= 1, got %d", params.EndLine), IsError: true}, nil
	}
	if params.StartLine > 0 && params.EndLine > 0 && params.StartLine > params.EndLine {
		return sdktools.ToolResult{Content: fmt.Sprintf("validation error: start_line (%d) must not exceed end_line (%d)", params.StartLine, params.EndLine), IsError: true}, nil
	}

	// Resolve the absolute path via the SDK's exported resolver — identical to
	// what the inner read_file uses, including workspace symlink resolution, so
	// containment checks stay consistent across plain-text and document reads.
	absPath := builtins.ResolvePath(ctx, params.Path)
	if absPath == "" {
		// Path resolution failed (escaped workspace) — delegate to inner tool
		// which will produce the proper validation error.
		return t.ReadFileTool.Execute(ctx, input)
	}

	// Get or convert the markdown, with session-temp conversion caching.
	markdown, err := t.getOrConvert(ctx, absPath)
	if err != nil {
		// Graceful fallback: delegate to inner read_file with a warning.
		t.convMu.Lock()
		conv := t.converter
		t.convMu.Unlock()
		if conv != nil {
			t.log.Warn("document conversion failed, falling back to raw read_file",
				"path", absPath, "err", err)
		} else {
			t.log.Warn("document conversion unavailable, falling back to raw read_file",
				"path", absPath, "err", err)
		}
		rawResult, rawErr := t.ReadFileTool.Execute(ctx, input)
		if rawErr != nil {
			return rawResult, rawErr
		}
		warning := fmt.Sprintf("[Warning: document conversion failed (%v); showing raw file content.]\n\n", err)
		return sdktools.ToolResult{Content: warning + rawResult.Content, IsError: rawResult.IsError}, nil
	}

	// Apply the requested line range to the converted markdown.
	content := formatMarkdownWindow(markdown, params.Path, params.StartLine, params.EndLine, t.limits)
	return sdktools.ToolResult{Content: content}, nil
}

// converterOrInit lazily initializes the markitdown converter on first use.
// Returns an error (wrapped exec.ErrNotFound) if markitdown is not on PATH.
// The venv interpreter path is probed HERE (not at construction): the
// tool-manager may still be installing during early startup. When the first
// init raced that installation (probe returned ""), the probe is RETRIED on
// every call until it resolves — a cached empty-python converter would pin
// vision off for the whole app run, which the constructor probe cannot
// repair without a restart. Once a non-empty path is probed the converter
// is frozen for the process lifetime (the CLI and its venv are installed by
// a single tool-manager operation).
func (t *ReadFileDocTool) converterOrInit() (*markitdown.Converter, error) {
	t.convMu.Lock()
	defer t.convMu.Unlock()
	pythonPath := ""
	if t.markitdownPython != nil {
		pythonPath = t.markitdownPython()
	}
	if t.converter != nil {
		if t.converterHasPyth || pythonPath == "" {
			// Frozen: either the converter was already built with the
			// interpreter (a resolved path never changes for this
			// process), or the venv probe still comes up empty — the
			// plain-CLI converter stays in place (vision-less but
			// functional).
			return t.converter, nil
		}
		// Cached converter built with an empty python path and the probe
		// has NOW resolved: rebuild so the fresh-install race does not pin
		// vision off for the whole app run.
		c, err := markitdown.NewConverter(markitdown.Options{
			Logger:     t.log,
			Timeout:    docConvertTimeout,
			PythonPath: pythonPath,
		})
		if err != nil {
			// Keep the old (plain) converter — a failed rebuild must not
			// break document reads.
			return t.converter, nil
		}
		t.converter = c
		t.converterHasPyth = true
		return c, nil
	}
	c, err := markitdown.NewConverter(markitdown.Options{
		Logger:     t.log,
		Timeout:    docConvertTimeout,
		PythonPath: pythonPath,
	})
	if err != nil {
		return nil, err
	}
	t.converter = c
	t.converterHasPyth = pythonPath != ""
	return c, nil
}

// getOrConvert returns the markdown representation of the document at absPath.
// The result is cached in <session-temp>/conversions/<sha256(path+mtime+size+vision)>.md.
// A cache hit avoids re-running the markitdown subprocess on repeated reads
// (including paginated reads with different line ranges).
//
// Vision assistance is resolved PER DOCUMENT from the execution context: the
// resolver (attached by the orchestrator) inspects the model active at THIS
// moment, so a mid-task model switch is honored by the next conversion. The
// resolved vision identity (endpoint+model+prompt) participates in the cache
// key — a document converted without captions must not be served from the
// cache after switching to a vision-capable model, and vice versa.
func (t *ReadFileDocTool) getOrConvert(ctx context.Context, absPath string) (string, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("cannot access file: %w", err)
	}

	var vision *markitdown.VisionOptions
	if r := markitdown.VisionResolverFrom(ctx); r != nil {
		vision = r()
	}

	cacheKey := docCacheKey(absPath, info.ModTime().UnixNano(), info.Size(), vision.CacheKey())

	tempDir := sdktools.TempDirFrom(ctx)
	if tempDir != "" {
		cacheFile := filepath.Join(tempDir, docConversionSubdir, cacheKey+".md")
		if cached, rErr := os.ReadFile(cacheFile); rErr == nil {
			return string(cached), nil
		}
	}

	conv, err := t.converterOrInit()
	if err != nil {
		return "", err
	}

	markdown, err := conv.ConvertWithVision(ctx, absPath, vision)
	if err != nil {
		return "", err
	}

	// Persist to the conversion cache so paginated reads don't re-convert.
	// The write is atomic (temp file + rename) so concurrent readers of the
	// same newly-converted document cannot observe a half-written cache entry.
	if tempDir != "" {
		cacheDir := filepath.Join(tempDir, docConversionSubdir)
		if mkErr := os.MkdirAll(cacheDir, 0o755); mkErr != nil {
			t.log.Warn("failed to create conversion cache dir",
				"path", absPath, "err", mkErr)
		} else {
			cacheFile := filepath.Join(cacheDir, cacheKey+".md")
			if wErr := atomicWriteFile(cacheFile, []byte(markdown), 0o644); wErr != nil {
				t.log.Warn("failed to write conversion cache",
					"path", absPath, "err", wErr)
			}
		}
	}

	return markdown, nil
}

// docCacheKey computes a deterministic cache key from the absolute file path,
// modification time, size, and the identity of the vision configuration used
// for the conversion. The key changes when the source file is modified or when
// the vision configuration differs, invalidating the cached conversion.
func docCacheKey(absPath string, mtime, size int64, visionKey string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d\x00%s", absPath, mtime, size, visionKey)))
	return hex.EncodeToString(h[:])
}

// atomicWriteFile writes data to path atomically by first writing to a unique
// temporary file in the same directory and then renaming it into place. Rename
// is atomic on POSIX, so concurrent writers cannot produce a partially-written
// file that a later cache hit would serve verbatim. The temporary file is
// removed if any step fails (a no-op after a successful rename).
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".conv-*.tmp")
	if err != nil {
		return err
	}
	tmpName := f.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// formatMarkdownWindow applies a line range to a markdown string and formats
// the result with a header matching the sp4rk read_file output format.
//
// It honours the same limits as the inner read_file: MaxWindowLines caps the
// requested window (defense against unbounded payloads to the LLM) and
// MaxLineBytes truncates pathological per-line content (e.g. an embedded blob
// in a converted table row). Range/limit semantics mirror ReadFileRange:
//   - An unset range yields the first ReadDefaultLines lines.
//   - start_line past the end of file returns an explicit "past end of file"
//     message instead of silently clamping down to the last line.
//   - The window is always clamped to [1, totalLines], and endLine is never
//     allowed below startLine (defense-in-depth against inverted callers).
func formatMarkdownWindow(markdown, sourcePath string, startLine, endLine int, limits builtins.FileLimits) string {
	filename := filepath.Base(sourcePath)
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(sourcePath), "."))

	if markdown == "" {
		return fmt.Sprintf("[File: %s | converted from .%s | 0 lines | empty document]\n", filename, ext)
	}

	lines := strings.Split(markdown, "\n")
	totalLines := len(lines)

	// Resolve defaults: no range → first defaultLines lines.
	if startLine <= 0 {
		startLine = 1
	}
	if endLine <= 0 {
		defaultLines := limits.ReadDefaultLines
		if defaultLines <= 0 {
			defaultLines = 1
		}
		endLine = startLine + defaultLines - 1
	}

	// Enforce the MaxWindowLines hard cap (matches ReadFileRange semantics).
	if limits.MaxWindowLines > 0 && endLine-startLine+1 > limits.MaxWindowLines {
		endLine = startLine + limits.MaxWindowLines - 1
	}

	// Past end of file: the requested start is beyond the document. Signal it
	// explicitly rather than clamping down to the last line.
	if startLine > totalLines {
		return fmt.Sprintf("[File: %s | converted from .%s | start_line %d is past end of file (%d lines)]\n",
			filename, ext, startLine, totalLines)
	}

	// Clamp end to bounds and guard against an inverted range regardless of the
	// caller (defense-in-depth so the slice below is always valid).
	if endLine > totalLines {
		endLine = totalLines
	}
	if endLine < startLine {
		endLine = startLine
	}

	selected := lines[startLine-1 : endLine]

	// Apply the per-line byte cap, matching the inner read_file's truncation
	// marker so converted output is no more permissive than raw file reads.
	if limits.MaxLineBytes > 0 {
		for i, ln := range selected {
			if len(ln) > limits.MaxLineBytes {
				selected[i] = truncateConvertedLine(ln, startLine+i, limits.MaxLineBytes)
			}
		}
	}

	content := strings.Join(selected, "\n")
	header := fmt.Sprintf("[File: %s | converted from .%s | Lines %d-%d of %d]\n",
		filename, ext, startLine, endLine, totalLines)

	if endLine < totalLines {
		content = header + content + fmt.Sprintf("\n[Use start_line=%d to continue reading]", endLine+1)
	} else {
		content = header + content
	}

	return content
}

// truncateConvertedLine truncates a converted markdown line to at most
// maxBytes and appends the same recovery-hinting marker used by the inner
// read_file (builtins.writeLine). The marker points at tool_result_read's line
// escape-hatch so an agent can request the full line explicitly. Note: for
// content-backed (converted-document) results the cached representation may
// already reflect this truncation, so recovery returns what the cache holds.
// Unlike builtins.writeLine, the input here carries no trailing newline (lines
// come from strings.Split, which strips the delimiter), so there is nothing to
// preserve after the marker.
func truncateConvertedLine(line string, lineNum, maxBytes int) string {
	if len(line) <= maxBytes {
		return line
	}
	return line[:maxBytes] + fmt.Sprintf(
		"[...line %d truncated at %d bytes. Use tool_result_read(hash, line=%d) to request the full line (full for file-backed reads; cached for converted documents)...]",
		lineNum, maxBytes, lineNum)
}
