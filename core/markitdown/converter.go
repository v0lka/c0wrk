// Package markitdown converts supported documents to Markdown by shelling out
// to the managed markitdown CLI (pre-installed at ~/.c0wrk/tools/bin and
// prepended to PATH during application startup).
//
// A single *Converter is safe for concurrent use: it carries only immutable
// configuration (a logger and a per-call timeout) and each Convert invocation
// spawns its own subprocess.
package markitdown

import (
	"context"
	"errors"
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
// into Markdown.
type Converter struct {
	log     *slog.Logger
	timeout time.Duration
}

// NewConverter returns a Converter backed by the markitdown CLI. It verifies
// that markitdown is resolvable on PATH; the returned error is wrapped with %w
// so callers can detect a missing binary via errors.Is(err, exec.ErrNotFound).
//
// A nil logger is replaced with a discard handler so the Converter never panics
// on a logging call. A non-positive timeout disables the per-call deadline,
// leaving cancellation entirely to the caller's context.
func NewConverter(logger *slog.Logger, timeout time.Duration) (*Converter, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if _, err := exec.LookPath("markitdown"); err != nil {
		return nil, fmt.Errorf("markitdown not found on PATH: %w", err)
	}
	return &Converter{log: logger, timeout: timeout}, nil
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
	start := time.Now()

	if !IsSupported(path) {
		return "", errors.New("markitdown: unsupported file format: " + filepath.Ext(path))
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("markitdown: cannot access file %q: %w", path, err)
	}

	runCtx := ctx
	if c.timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

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
