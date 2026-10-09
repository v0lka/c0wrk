package updater

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/v0lka/sp4rk/safeio"
)

// copyFile copies the regular file at src to dst, applying the given mode.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := safeio.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := safeio.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// cleanupStaleMaxAge bounds the reap: any temp artifact (or anything inside
// it) modified within this window is considered potentially LIVE — an update
// download in progress or a staged apply pending its relaunch — and is left
// alone. A genuine leftover is older than any sane update cycle; a manual
// CleanupStaleUpdaters racing an in-flight update (review finding #37) can
// only be younger than the bound.
const cleanupStaleMaxAge = 24 * time.Hour

// cleanupTempGlobs removes entries in the OS temp directory matching any of
// the given glob patterns, skipping everything younger than
// cleanupStaleMaxAge (measured on the entry itself and its direct children —
// a download or a staged apply keeps touching files inside). Errors are
// logged at debug level and otherwise ignored (best-effort cleanup).
func cleanupTempGlobs(base string, log *slog.Logger, patterns ...string) {
	tempDir, err := filepath.EvalSymlinks(base)
	if err != nil {
		tempDir = base
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(tempDir, pattern))
		if err != nil {
			continue
		}
		for _, m := range matches {
			if cleanupTempEntryLive(m) {
				if log != nil {
					log.Debug("skipping temp artifact younger than the staleness bound (possibly a live update)", "path", m)
				}
				continue
			}
			if err := os.RemoveAll(m); err != nil {
				if log != nil {
					log.Debug("could not remove stale temp artifact (best-effort)", "path", m, "error", err)
				}
			} else if log != nil {
				log.Debug("removed stale temp artifact", "path", m)
			}
		}
	}
}

// cleanupTempEntryLive reports whether the entry — or, for directories, any
// of its direct children — was modified within cleanupStaleMaxAge. Unreadable
// entries are treated as live (fail-open for the skip decision: a reap must
// not delete what it cannot date). A regular-file artifact is decided on its
// own mtime only: ReadDir would always fail with ENOTDIR, and the fail-open
// rule would then exempt file-shaped leftovers (e.g. the Windows
// c0wrk-updater.exe this cleanup exists to reap) forever.
func cleanupTempEntryLive(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	if !info.IsDir() {
		return time.Since(info.ModTime()) < cleanupStaleMaxAge
	}
	if time.Since(info.ModTime()) < cleanupStaleMaxAge {
		return true
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if modTimeWithinCleanupStaleMaxAge(filepath.Join(path, e.Name())) {
			return true
		}
	}
	return false
}

func modTimeWithinCleanupStaleMaxAge(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return time.Since(info.ModTime()) < cleanupStaleMaxAge
}
