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

	"github.com/v0lka/sp4rk/safeio"
)

// docConvertTimeout is the per-file budget for one markitdown conversion; the
// attachment flow uses the same budget.
const docConvertTimeout = 2 * time.Minute

const docConversionSubdir = "conversions"

// docExts holds the document extensions markitdown converts; plain-text
// formats are read natively by the inner read_file.
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
Anti-example: not for pattern search (ripgrep), meaning search (semantic_search) or name search (glob); there is no limit or count parameter — paginate with start_line/end_line instead; do not read huge files whole.`

// isDocExt reports whether the file at path has a document/binary extension
// that should be converted to markdown via markitdown.
func isDocExt(path string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	_, ok := docExts[ext]
	return ok
}

// ReadFileDocTool wraps the sp4rk ReadFileTool, converting document formats to
// markdown via markitdown (plain text delegates unchanged); content-backed so
// converted results cache in memory. See specs/domains/tool-system/builtins.md
// (Document Conversion).
type ReadFileDocTool struct {
	*builtins.ReadFileTool
	limits           builtins.FileLimits
	log              *slog.Logger
	converter        *markitdown.Converter
	converterHasPyth bool // converter was built with a resolved venv interpreter
	convMu           sync.Mutex
	markitdownPython func() string // lazily resolves the managed venv interpreter for vision-assisted conversion; nil/"" = disabled
}

// NewReadFileDocTool builds the wrapper; a nil logger becomes a discard
// handler. markitdownPython is probed at the FIRST converter init, not at
// construction: the tool-manager installs the venv asynchronously after
// startup, so a construction-time probe would pin vision assistance off on
// fresh installs until restart. See specs/domains/tool-system/builtins.md
// (Document Conversion) for the vision path.
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

func (t *ReadFileDocTool) Description() string {
	return docFileDescription
}

// IsContentBacked is true only for document extensions — converted markdown
// caches in memory; plain text stays file-backed.
func (t *ReadFileDocTool) IsContentBacked(_ context.Context, input json.RawMessage) bool {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &params); err != nil || params.Path == "" {
		return false
	}
	return isDocExt(params.Path)
}

// Execute converts document formats to markdown (session-cached) and returns
// the requested window; plain text and conversion failures delegate to the
// inner ReadFileTool, the latter with a warning prepended.
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

	// Inverted ranges are rejected up front or the window slicing below
	// panics on an out-of-range slice.
	if params.StartLine < 0 {
		return sdktools.ToolResult{Content: fmt.Sprintf("validation error: start_line must be >= 1, got %d", params.StartLine), IsError: true}, nil
	}
	if params.EndLine < 0 {
		return sdktools.ToolResult{Content: fmt.Sprintf("validation error: end_line must be >= 1, got %d", params.EndLine), IsError: true}, nil
	}
	if params.StartLine > 0 && params.EndLine > 0 && params.StartLine > params.EndLine {
		return sdktools.ToolResult{Content: fmt.Sprintf("validation error: start_line (%d) must not exceed end_line (%d)", params.StartLine, params.EndLine), IsError: true}, nil
	}

	// Resolve via the SDK resolver, identical to the inner read_file including
	// symlink handling, so containment stays consistent across both branches.
	absPath := builtins.ResolvePath(ctx, params.Path)
	if absPath == "" {
		// Escaped workspace: the inner tool produces the proper error.
		return t.ReadFileTool.Execute(ctx, input)
	}

	// Get or convert the markdown, with session-temp conversion caching.
	markdown, err := t.getOrConvert(ctx, absPath)
	if err != nil {
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

// converterOrInit lazily builds the converter (error if markitdown is not on
// PATH). The venv probe retries on every call until it resolves — a first
// init that raced the tool-manager install must not pin vision off for the
// run — and freezes once non-empty (the venv is installed by one operation).
func (t *ReadFileDocTool) converterOrInit() (*markitdown.Converter, error) {
	t.convMu.Lock()
	defer t.convMu.Unlock()
	pythonPath := ""
	if t.markitdownPython != nil {
		pythonPath = t.markitdownPython()
	}
	if t.converter != nil {
		if t.converterHasPyth || pythonPath == "" {
			// Frozen: built with the interpreter, or still no path to add.
			return t.converter, nil
		}
		// Probe has now resolved past the fresh-install race: rebuild with it.
		c, err := markitdown.NewConverter(markitdown.Options{
			Logger:     t.log,
			Timeout:    docConvertTimeout,
			PythonPath: pythonPath,
		})
		if err != nil {
			// Keep the plain converter: a failed rebuild must not break reads.
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

// getOrConvert returns the document's markdown, cached under the session temp
// dir keyed by path+mtime+size+vision identity. See
// specs/domains/tool-system/builtins.md (Document Conversion) for the cache
// and per-document vision resolution contract.
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
		if cacheDir, ok := conversionCacheDir(tempDir); ok {
			cacheFile := filepath.Join(cacheDir, cacheKey+".md")
			// A planted symlink at the deterministic entry name is a miss,
			// never followed.
			if fi, lErr := os.Lstat(cacheFile); lErr != nil || fi.Mode()&os.ModeSymlink == 0 {
				// Read back with the converter's own write cap: the safeio
				// default would refuse exactly the large documents the cache
				// exists for, while anything over the write cap is planted or
				// corrupt and falls through to a fresh conversion.
				if cached, rErr := safeio.ReadFileLimited(cacheFile, markitdown.MaxConversionOutputBytes); rErr == nil {
					return string(cached), nil
				}
			}
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

	// Atomic write (temp + rename) so concurrent readers never see a half
	// entry; a non-real cache directory disables persistence entirely —
	// MkdirAll/rename would follow a planted symlink out of the session tree.
	if tempDir != "" {
		cacheDir, ok := conversionCacheDir(tempDir)
		if !ok {
			t.log.Warn("conversion cache dir is not a real directory; skipping cache write",
				"dir", filepath.Join(tempDir, docConversionSubdir), "path", absPath)
		} else if mkErr := os.MkdirAll(cacheDir, 0o755); mkErr != nil {
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

// conversionCacheDir returns the cache dir under tempDir only if the existing
// entry is a real directory — a planted symlink there would redirect writes
// outside the session tree and let planted entries spoof reads. A missing
// directory is fine; the caller creates it.
func conversionCacheDir(tempDir string) (dir string, ok bool) {
	cacheDir := filepath.Join(tempDir, docConversionSubdir)
	fi, err := os.Lstat(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return cacheDir, true
		}
		return "", false
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return "", false
	}
	return cacheDir, true
}

// docCacheKey is deterministic over path+mtime+size+vision identity, so the
// key changes when the file or the vision configuration changes.
func docCacheKey(absPath string, mtime, size int64, visionKey string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d\x00%s", absPath, mtime, size, visionKey)))
	return hex.EncodeToString(h[:])
}

// atomicWriteFile writes via a unique temp file + rename so no reader ever
// sees a half-written entry; a failed step removes the temp file.
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

// formatMarkdownWindow applies a line range to converted markdown with a
// header matching the sp4rk read_file output. It mirrors ReadFileRange
// semantics: unset range → first ReadDefaultLines lines, start past EOF
// signals explicitly instead of clamping, and the window is clamped to
// [1,totalLines] with endLine never below startLine.
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

	// Clamp regardless of caller so the slice below is always valid.
	if endLine > totalLines {
		endLine = totalLines
	}
	if endLine < startLine {
		endLine = startLine
	}

	selected := lines[startLine-1 : endLine]

	// Per-line cap, matching the inner read_file's truncation marker.
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

// truncateConvertedLine caps a converted markdown line at maxBytes and appends
// the inner read_file's recovery marker (tool_result_read line escape-hatch).
// For content-backed results recovery returns what the cache holds; the input
// has no trailing newline (strings.Split stripped it).
func truncateConvertedLine(line string, lineNum, maxBytes int) string {
	if len(line) <= maxBytes {
		return line
	}
	return line[:maxBytes] + fmt.Sprintf(
		"[...line %d truncated at %d bytes. Use tool_result_read(hash, line=%d) to request the full line (full for file-backed reads; cached for converted documents)...]",
		lineNum, maxBytes, lineNum)
}
