//go:build !windows

package toolmanager

import (
	"fmt"
	"syscall"
)

// checkDiskSpace verifies that at least minFree bytes are available on the
// volume containing path. Uses syscall.Statfs on Unix platforms.
func checkDiskSpace(path string, minFree int64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		// Non-critical — skip check if we can't stat the filesystem.
		return nil //nolint:nilerr // best-effort check, non-fatal
	}
	free := int64(stat.Bavail) * int64(stat.Bsize)
	if free < minFree {
		return fmt.Errorf("only %d MB free (need at least %d MB)", free/(1024*1024), minFree/(1024*1024))
	}
	return nil
}
