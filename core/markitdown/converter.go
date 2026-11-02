// Package markitdown converts supported documents to Markdown via the
// managed markitdown CLI (specs/domains/tool-manager.md), optionally through
// the library API via an embedded Python driver so embedded images are
// captioned by the active vision-capable LLM (ConvertWithVision; specs/
// domains/session-lifecycle.md, Data Egress). Concurrency-safe: immutable
// configuration only, one subprocess per conversion.
package markitdown

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/v0lka/c0wrk/internal/sysproc"
)

// supportedExts is the lowercase, dotless whitelist of extensions the
// markitdown CLI converts (.htm and .html both present — the CLI accepts
// both spellings).
var supportedExts = []string{
	"pdf", "docx", "pptx", "xlsx", "odt",
	"html", "htm",
	"csv", "tsv",
	"txt", "md", "json", "xml", "rst",
}

// supportedExtSet is an O(1) lookup view over supportedExts, built at init.
var supportedExtSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(supportedExts))
	for _, e := range supportedExts {
		m[e] = struct{}{}
	}
	return m
}()

// SupportedExtensions returns the accepted extensions (no leading dot) as a
// defensive copy.
func SupportedExtensions() []string {
	out := make([]string, len(supportedExts))
	copy(out, supportedExts)
	return out
}

// IsSupported reports whether path's extension is convertible (case- and
// leading-dot tolerant).
func IsSupported(path string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if ext == "" {
		return false
	}
	_, ok := supportedExtSet[ext]
	return ok
}

// Converter converts supported documents to Markdown, optionally through the
// vision driver (see ConvertWithVision).
type Converter struct {
	log        *slog.Logger
	timeout    time.Duration
	pythonPath string // managed venv interpreter for vision-assisted conversion; "" disables it
}

// Options configures a Converter; PythonPath (the managed venv interpreter,
// toolmanager.VenvPythonPath) enables vision-assisted conversion — empty
// keeps plain conversions fully functional.
type Options struct {
	Logger     *slog.Logger
	Timeout    time.Duration
	PythonPath string
}

// NewConverter verifies markitdown is resolvable on PATH (errors.Is(err,
// exec.ErrNotFound) detects a missing binary); a nil logger becomes a
// discard handler, a non-positive timeout leaves cancellation to ctx.
func NewConverter(opts Options) (*Converter, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if _, err := exec.LookPath("markitdown"); err != nil {
		return nil, fmt.Errorf("markitdown not found on PATH: %w", err)
	}
	return &Converter{log: logger, timeout: opts.Timeout, pythonPath: opts.PythonPath}, nil
}

// Convert converts path to Markdown via the markitdown CLI; a positive
// timeout merges with ctx (sooner wins) and every failure wraps its cause
// (os.Stat, context cancellation, or exec + bounded stream excerpts).
func (c *Converter) Convert(ctx context.Context, path string) (string, error) {
	return c.convert(ctx, path, nil)
}

// ConvertWithVision converts like Convert, additionally passing connection
// parameters for a vision-capable LLM so embedded images are captioned.
// Options resolve per document (VisionResolver); degradation is always
// toward plain CLI — nil/incomplete options or no PythonPath convert
// plainly, a driver failure retries plainly on the SAME shared deadline
// (specs/domains/session-lifecycle.md).
func (c *Converter) ConvertWithVision(ctx context.Context, path string, vision *VisionOptions) (string, error) {
	return c.convert(ctx, path, vision)
}

// visionTimeoutFloor is the minimum per-file deadline for the vision attempt
// AND its plain-CLI fallback (each embedded image costs one LLM round-trip).
const visionTimeoutFloor = 5 * time.Minute

// MaxConversionOutputBytes caps buffered subprocess output; a conversion
// over the cap FAILS rather than truncating silently. The read_file document
// cache sizes its disk reads with safeio.ReadFileLimited(…, this value) —
// the two sides must move together.
// See specs/domains/tool-system/builtins.md (Document Conversion).
const MaxConversionOutputBytes = int64(32 << 20)

// maxConverterOutputBytes is MaxConversionOutputBytes as the int the bounded
// capture buffers are sized with.
const maxConverterOutputBytes = int(MaxConversionOutputBytes)

// maxErrorStreamBytes bounds how much of a captured stream an error/log
// record embeds — the head carries the diagnostic.
const maxErrorStreamBytes = 2 << 10

// streamExcerpt returns at most maxErrorStreamBytes bytes of s, UTF-8-safe:
// invalid bytes become U+FFFD and the cut never splits a rune.
func streamExcerpt(s string) string {
	if len(s) <= maxErrorStreamBytes {
		return strings.ToValidUTF8(s, "\uFFFD")
	}
	cut := s[:maxErrorStreamBytes]
	// Trim at most the one rune split by the cut point; an invalid byte
	// anywhere else cannot be fixed by trimming and becomes U+FFFD below.
	for i := 0; i < utf8.UTFMax-1 && cut != ""; i++ {
		if utf8.ValidString(cut) {
			break
		}
		if cut[len(cut)-1] < utf8.RuneSelf {
			break // ASCII tails cannot be repaired by trimming
		}
		cut = cut[:len(cut)-1]
	}
	return strings.ToValidUTF8(cut, "\uFFFD")
}

// boundedBuffer buffers at most max bytes, then fails the write (surfacing
// through cmd.Run); overflow marks that the cap was hit for a precise error.
type boundedBuffer struct {
	max      int
	buf      bytes.Buffer
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.overflow {
		return 0, fmt.Errorf("output exceeds the %d byte capture cap", b.max)
	}
	if b.buf.Len()+len(p) > b.max {
		b.overflow = true
		return 0, fmt.Errorf("output exceeds the %d byte capture cap", b.max)
	}
	return b.buf.Write(p)
}

func (b *boundedBuffer) String() string { return b.buf.String() }

// convert implements both Convert and ConvertWithVision: mode selection,
// the vision fallback chain, and shared validation live here.
func (c *Converter) convert(ctx context.Context, path string, vision *VisionOptions) (string, error) {
	start := time.Now()

	if !IsSupported(path) {
		return "", fmt.Errorf("markitdown: unsupported file format: %s", filepath.Ext(path))
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("markitdown: cannot access file %q: %w", path, err)
	}

	if vision != nil {
		switch {
		case !vision.complete():
			c.log.Debug("markitdown: incomplete vision options, using plain conversion", "path", path)
		case c.pythonPath == "":
			c.log.Debug("markitdown: no managed python configured, using plain conversion", "path", path)
		default:
			// One derived context bounds the vision attempt AND the plain
			// fallback — the deadline only shortens, never resets.
			budget := c.timeout
			if budget > 0 && budget < visionTimeoutFloor {
				budget = visionTimeoutFloor
			}
			runCtx, cancel := c.withDeadline(ctx, budget)
			defer cancel()

			markdown, err := c.runVisionDriver(runCtx, path, vision, start)
			if err == nil {
				return markdown, nil
			}
			// The driver failed as a whole (per-image failures are swallowed
			// inside markitdown): plain-CLI fallback on the same budget.
			c.log.Warn("markitdown: vision-assisted conversion failed, falling back to plain conversion",
				"path", path,
				"duration", time.Since(start),
				"err", err,
			)
			return c.runPlainCLI(runCtx, path, start)
		}
	}

	return c.runPlainCLI(ctx, path, start)
}

// runVisionDriver executes the embedded Python driver with the vision
// connection parameters in the child's environment (shared budget from
// convert); only stdout becomes the markdown, stderr stays in logs/errors.
func (c *Converter) runVisionDriver(ctx context.Context, path string, vision *VisionOptions, start time.Time) (string, error) {
	cmd := exec.CommandContext(ctx, c.pythonPath, "-c", visionDriverScript, path)
	sysproc.HideConsole(cmd) // avoid flashing console windows on Windows (GUI app)
	cmd.Env = append(os.Environ(),
		visionEnvAPIKey+"="+vision.APIKey,
		visionEnvBaseURL+"="+vision.BaseURL,
		visionEnvModel+"="+vision.Model,
		visionEnvPrompt+"="+vision.Prompt,
	)

	var stdout, stderr boundedBuffer
	stdout.max = maxConverterOutputBytes
	stderr.max = maxConverterOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// A capture-cap overflow is a conversion failure, not a context
		// event: report it precisely before the deadline/cancellation checks.
		if stdout.overflow || stderr.overflow {
			c.log.Warn("markitdown vision conversion output exceeded capture cap",
				"path", path,
				"duration", time.Since(start),
			)
			return "", fmt.Errorf("markitdown: vision conversion output exceeds the %d byte capture cap", maxConverterOutputBytes)
		}
		// Distinguish deadline/cancellation from a genuine driver failure so
		// the caller can errors.Is the context cause.
		if cerr := ctx.Err(); cerr != nil {
			c.log.Warn("markitdown vision conversion aborted",
				"path", path,
				"duration", time.Since(start),
				"err", cerr,
				"stderr", streamExcerpt(strings.TrimSpace(stderr.String())),
			)
			return "", fmt.Errorf("markitdown: vision conversion aborted: %w", cerr)
		}
		return "", fmt.Errorf("markitdown: vision conversion failed: %w: %s", err, streamExcerpt(strings.TrimSpace(stderr.String())))
	}
	if stdout.overflow || stderr.overflow {
		return "", fmt.Errorf("markitdown: vision conversion output exceeds the %d byte capture cap", maxConverterOutputBytes)
	}

	markdown := strings.TrimSpace(stdout.String())
	if diag := strings.TrimSpace(stderr.String()); diag != "" {
		// The driver loads PIL/pdfminer/bs4, which routinely emit warnings;
		// keep them in the debug log, out of the document.
		c.log.Debug("markitdown vision driver emitted stderr", "path", path, "stderr", streamExcerpt(diag))
	}
	c.log.Debug("markitdown vision conversion succeeded",
		"path", path,
		"duration", time.Since(start),
		"bytes", len(markdown),
	)
	return markdown, nil
}

// runPlainCLI runs the markitdown CLI exactly as conversions were performed
// before vision support existed.
func (c *Converter) runPlainCLI(ctx context.Context, path string, start time.Time) (string, error) {
	runCtx, cancel := c.withDeadline(ctx, c.timeout)
	defer cancel()

	mdCmd := exec.CommandContext(runCtx, "markitdown", path)
	sysproc.HideConsole(mdCmd) // avoid flashing console windows on Windows (GUI app)
	// Capture through bounded buffers instead of CombinedOutput: the whole
	// stdout+stderr of the conversion must not be bufferable without bound.
	var out, errOut boundedBuffer
	out.max = maxConverterOutputBytes
	errOut.max = maxConverterOutputBytes
	mdCmd.Stdout = &out
	mdCmd.Stderr = &errOut
	err := mdCmd.Run()
	if err == nil && (out.overflow || errOut.overflow) {
		err = fmt.Errorf("output exceeds the %d byte capture cap", maxConverterOutputBytes)
	}
	if err != nil {
		// A capture-cap overflow is a genuine conversion failure even when a
		// deadline is also armed: report it precisely.
		if out.overflow || errOut.overflow {
			c.log.Warn("markitdown conversion output exceeded capture cap",
				"path", path,
				"duration", time.Since(start),
			)
			return "", fmt.Errorf("markitdown: conversion output exceeds the %d byte capture cap", maxConverterOutputBytes)
		}
		// Distinguish deadline/cancellation from a genuine CLI failure so the
		// caller can errors.Is the context cause.
		if cerr := runCtx.Err(); cerr != nil {
			c.log.Warn("markitdown conversion aborted",
				"path", path,
				"duration", time.Since(start),
				"err", cerr,
			)
			return "", fmt.Errorf("markitdown: conversion aborted: %w", cerr)
		}
		c.log.Warn("markitdown conversion failed",
			"path", path,
			"duration", time.Since(start),
			"outputBytes", len(out.String()),
			"err", err,
			"output", streamExcerpt(strings.TrimSpace(out.String())),
			"stderr", streamExcerpt(strings.TrimSpace(errOut.String())),
		)
		// Surface BOTH streams — stderr carries the CLI's real diagnostics;
		// bounded excerpts only (buffers may hold 32 MiB per stream).
		if stderr := streamExcerpt(strings.TrimSpace(errOut.String())); stderr != "" {
			return "", fmt.Errorf("markitdown: conversion failed: %w: %s (stderr: %s)",
				err, streamExcerpt(strings.TrimSpace(out.String())), stderr)
		}
		return "", fmt.Errorf("markitdown: conversion failed: %w: %s",
			err, streamExcerpt(strings.TrimSpace(out.String())))
	}

	markdown := strings.TrimSpace(out.String())
	c.log.Debug("markitdown conversion succeeded",
		"path", path,
		"duration", time.Since(start),
		"bytes", len(markdown),
	)
	return markdown, nil
}

// withDeadline merges ctx with the per-call timeout (sooner deadline wins);
// a non-positive timeout leaves cancellation to ctx alone.
func (c *Converter) withDeadline(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}
