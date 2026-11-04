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

// cleanupStaleMaxAge bounds the reap: an artifact (or anything inside it)
// modified within this window may be a LIVE update — a download in progress or
// a staged apply pending relaunch — and is left alone; a manual
// CleanupStaleUpdaters racing an in-flight update can only be younger.
// See specs/decisions/023-auto-update.md.
const cleanupStaleMaxAge = 24 * time.Hour

// cleanupTempGlobs removes OS-temp entries matching the given globs, skipping
// anything younger than cleanupStaleMaxAge (dated on the entry and its direct
// children — an in-flight update keeps touching files inside). Best-effort:
// errors are logged at debug and ignored.
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
// direct child — was modified within cleanupStaleMaxAge. Unreadable entries
// count as live (a reap must not delete what it cannot date); regular files
// are dated on their own mtime only, else the fail-open rule would exempt
// file-shaped leftovers (the Windows c0wrk-updater.exe) forever.
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
