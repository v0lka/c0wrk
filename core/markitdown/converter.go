// Package markitdown converts supported documents to Markdown by shelling out
// to the managed markitdown CLI (pre-installed at ~/.c0wrk/tools/bin and
// prepended to PATH during application startup).
//
// Optionally, when constructed with the managed venv Python path and given
// per-document VisionOptions, conversions run through the markitdown library
// API via an embedded Python driver so images embedded in documents are
// captioned by the active vision-capable LLM (see ConvertWithVision).
//
// A single *Converter is safe for concurrent use: it carries only immutable
// configuration (a logger, a per-call timeout, and an optional interpreter
// path) and each Convert invocation spawns its own subprocess.
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

	"github.com/v0lka/c0wrk/internal/sysproc"
)

// supportedExts is the whitelist of document/text extensions that the
// markitdown CLI can convert. Entries carry no leading dot and are stored in
// canonical lowercase form. Both ".htm" and ".html" are present because the
// CLI accepts both spellings.
var supportedExts = []string{
	"pdf", "docx", "pptx", "xlsx", "odt",
	"html", "htm",
	"csv", "tsv",
	"txt", "md", "json", "xml", "rst",
}

// supportedExtSet is an O(1) lookup view over supportedExts. It is built once
// at package initialisation; SupportedExtensions always returns a copy so the
// package whitelist itself remains immutable.
var supportedExtSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(supportedExts))
	for _, e := range supportedExts {
		m[e] = struct{}{}
	}
	return m
}()

// SupportedExtensions returns the file extensions (without a leading dot) that
// the markitdown converter accepts. The returned slice is a defensive copy;
// callers may mutate it without affecting the package whitelist.
func SupportedExtensions() []string {
	out := make([]string, len(supportedExts))
	copy(out, supportedExts)
	return out
}

// IsSupported reports whether the file at path has an extension that markitdown
// can convert. The check is case-insensitive and tolerates a leading dot on the
// extension (e.g. both ".MD" and "md" match). Files with no extension, or an
// extension outside the whitelist (e.g. ".mp3", ".jpg"), report false.
func IsSupported(path string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if ext == "" {
		return false
	}
	_, ok := supportedExtSet[ext]
	return ok
}

// Converter shells out to the markitdown CLI to convert supported documents
// into Markdown. When constructed with a managed venv Python path AND given
// per-call VisionOptions (see ConvertWithVision), it instead runs the
// markitdown library through an embedded Python driver so embedded images can
// be captioned by a vision-capable LLM.
type Converter struct {
	log        *slog.Logger
	timeout    time.Duration
	pythonPath string // managed venv interpreter for vision-assisted conversion; "" disables it
}

// Options configures a Converter. Logger and Timeout mirror the original
// NewConverter parameters; PythonPath additionally enables vision-assisted
// conversion by pointing at the managed venv interpreter that has the
// markitdown package importable (see toolmanager.VenvPythonPath). An empty
// PythonPath keeps the Converter fully functional for plain conversions —
// ConvertWithVision then degrades to the plain CLI path.
type Options struct {
	Logger     *slog.Logger
	Timeout    time.Duration
	PythonPath string
}

// NewConverter returns a Converter backed by the markitdown CLI. It verifies
// that markitdown is resolvable on PATH; the returned error is wrapped with %w
// so callers can detect a missing binary via errors.Is(err, exec.ErrNotFound).
//
// A nil logger is replaced with a discard handler so the Converter never panics
// on a logging call. A non-positive timeout disables the per-call deadline,
// leaving cancellation entirely to the caller's context.
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

// Convert converts the file at path to Markdown by running the markitdown CLI.
//
// The caller's context governs cancellation. When the Converter has a positive
// timeout it is merged with ctx as a deadline (the sooner of the two wins), so
// a single runaway conversion cannot outlive the configured budget.
//
// On success the trimmed stdout is returned as markdown. A non-zero exit, a
// missing file, or an unsupported format each yield a wrapped error. The
// underlying cause is always reachable via errors.Is / errors.As — missing
// files wrap the os.Stat error, cancellation wraps context.Canceled or
// context.DeadlineExceeded, and CLI failures wrap the exec error with the
// command's combined stdout/stderr.
func (c *Converter) Convert(ctx context.Context, path string) (string, error) {
	return c.convert(ctx, path, nil)
}

// ConvertWithVision converts the file at path like Convert, additionally
// passing connection parameters for a vision-capable LLM so markitdown can
// describe images embedded in the document (e.g. pictures in pptx decks).
//
// Per-document semantics: callers resolve vision options for THE CURRENT
// active model on every call (see VisionResolver); the Converter applies — or
// ignores — whatever it receives, so a mid-session model switch takes effect
// on the very next conversion.
//
// Degradation is always graceful and toward the current behavior:
//   - nil or incomplete options → plain CLI conversion;
//   - no PythonPath configured → plain CLI conversion (logged at debug);
//   - driver failure (non-zero exit, import error, …) → the conversion is
//     retried once via the plain CLI and only that error can surface.
//
// The vision attempt runs under an elevated per-file deadline (at least
// visionTimeoutFloor): captioning performs one LLM round-trip per embedded
// image, which routinely exceeds the plain 2-minute conversion budget. That
// deadline is a single budget for the whole vision-assisted conversion: a
// driver failure falls back to the plain CLI on the SAME derived context, so
// the retry only gets whatever budget remains — a vision attempt that
// exhausted (or exceeded) the deadline never unlocks a fresh full plain
// conversion window.
func (c *Converter) ConvertWithVision(ctx context.Context, path string, vision *VisionOptions) (string, error) {
	return c.convert(ctx, path, vision)
}

// visionTimeoutFloor is the minimum per-file deadline for vision-assisted
// conversion — the budget covers the vision attempt AND the plain-CLI
// fallback that may follow it. Each embedded image costs one LLM round-trip
// (seconds each), so a deck with dozens of pictures legitimately needs more
// than the plain conversion budget.
const visionTimeoutFloor = 5 * time.Minute

// convert implements both Convert and ConvertWithVision. See those methods
// for the documented semantics; this helper owns the mode selection, the
// vision fallback chain, and the shared validation.
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
			// fallback together: withDeadline (context.WithTimeout) only ever
			// shortens, so the fallback sees just the remaining budget
			// instead of a fresh full conversion window.
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
			// The driver failed as a whole (not per-image captioning, which
			// markitdown already swallows internally): degrade to the plain
			// CLI so a broken vision endpoint never makes document conversion
			// WORSE than it is today. The fallback shares the remaining
			// budget of the same deadline, so a hung vision endpoint costs at
			// most one vision budget per file. Only a simultaneous plain
			// failure (or the exhausted shared deadline) surfaces to the
			// caller.
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
// connection parameters in the child's environment. The caller supplies the
// context carrying the shared vision budget (see convert). Only stdout
// becomes the converted markdown; stderr — Python warnings, deprecation
// notices, driver diagnostics — is kept out of the document body and
// surfaced solely in logs and error messages.
func (c *Converter) runVisionDriver(ctx context.Context, path string, vision *VisionOptions, start time.Time) (string, error) {
	cmd := exec.CommandContext(ctx, c.pythonPath, "-c", visionDriverScript, path)
	sysproc.HideConsole(cmd) // avoid flashing console windows on Windows (GUI app)
	cmd.Env = append(os.Environ(),
		visionEnvAPIKey+"="+vision.APIKey,
		visionEnvBaseURL+"="+vision.BaseURL,
		visionEnvModel+"="+vision.Model,
		visionEnvPrompt+"="+vision.Prompt,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Distinguish deadline/cancellation from a genuine driver failure so
		// the caller can errors.Is the context cause.
		if cerr := ctx.Err(); cerr != nil {
			c.log.Warn("markitdown vision conversion aborted",
				"path", path,
				"duration", time.Since(start),
				"err", cerr,
				"stderr", strings.TrimSpace(stderr.String()),
			)
			return "", fmt.Errorf("markitdown: vision conversion aborted: %w", cerr)
		}
		return "", fmt.Errorf("markitdown: vision conversion failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	markdown := strings.TrimSpace(stdout.String())
	if diag := strings.TrimSpace(stderr.String()); diag != "" {
		// The driver loads the full markitdown surface (PIL, pdfminer, bs4),
		// which routinely emits warnings; keep them reachable for
		// troubleshooting without polluting the document.
		c.log.Debug("markitdown vision driver emitted stderr", "path", path, "stderr", diag)
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
	out, err := mdCmd.CombinedOutput()
	if err != nil {
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
			"err", err,
			"output", strings.TrimSpace(string(out)),
		)
		return "", fmt.Errorf("markitdown: conversion failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	markdown := strings.TrimSpace(string(out))
	c.log.Debug("markitdown conversion succeeded",
		"path", path,
		"duration", time.Since(start),
		"bytes", len(markdown),
	)
	return markdown, nil
}

// withDeadline merges the caller's context with the given per-call timeout
// (the sooner deadline wins). A non-positive timeout leaves cancellation to
// the caller's context alone.
func (c *Converter) withDeadline(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}
