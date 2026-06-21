//go:build !windows

package toolmanager

import (
	"fmt"
	"log/slog"
	"os"
	"syscall"
)

// checkDiskSpace verifies that at least minFree bytes are available on the
// volume containing path. Uses syscall.Statfs on Unix platforms.
//
// Decision (2026-06-21): the caller (EnsureCriticalTools) creates
// directories before calling this function, so path always exists.
// Pre-existing non-existence (ENOENT/ENOTDIR) is treated as non-critical
// (best-effort check). Real filesystem errors are logged but not surfaced
// as fatal — the download will fail with a clearer error if space runs out.
func checkDiskSpace(path string, minFree int64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		if os.IsNotExist(err) {
			// Path doesn't exist — caller should have created directories
			// first. Non-critical; the download will fail with a clearer
			// error if space is insufficient.
			return nil
		}
		// Real filesystem error (permission denied, I/O error, etc.).
		// Logged at warn level for diagnostics; the check is best-effort.
		slog.Warn("disk space check unavailable", "path", path, "error", err)
		return nil //nolint:nilerr // best-effort check, non-fatal
	}
	free := int64(stat.Bavail) * int64(stat.Bsize)
	if free < minFree {
		return fmt.Errorf("only %d MB free (need at least %d MB)", free/(1024*1024), minFree/(1024*1024))
	}
	return nil
}
