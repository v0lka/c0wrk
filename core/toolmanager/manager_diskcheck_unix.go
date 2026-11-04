//go:build !windows

package toolmanager

import (
	"fmt"
	"log/slog"
	"os"
	"syscall"
)

// checkDiskSpace verifies minFree bytes free on path's volume (statfs).
// Best-effort: the caller creates the directories first, so a missing path
// (ENOENT/ENOTDIR) is non-critical, and real statfs errors are logged, not
// fatal — a real shortage fails later with a clearer download error.
func checkDiskSpace(path string, minFree int64, logger *slog.Logger) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		if logger != nil {
			logger.Warn("disk space check unavailable", "path", path, "error", err)
		}
		return nil //nolint:nilerr // best-effort check, non-fatal
	}
	free := int64(stat.Bavail) * int64(stat.Bsize) //nolint:unconvert // Bsize is int64 on Linux, uint32 on Darwin
	if free < minFree {
		return fmt.Errorf("only %d MB free (need at least %d MB)", free/(1024*1024), minFree/(1024*1024))
	}
	return nil
}
