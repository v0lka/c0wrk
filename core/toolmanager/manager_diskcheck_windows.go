//go:build windows

package toolmanager

import "log/slog"

// checkDiskSpace is a no-op on Windows: the syscall package exposes no
// Statfs_t, and the UTF16PtrFromString API dance is not worth it for a
// best-effort pre-flight — os.Create fails with a clear "disk full" instead.
func checkDiskSpace(path string, minFree int64, logger *slog.Logger) error {
	return nil
}
