//go:build windows

package toolmanager

// checkDiskSpace is a no-op on Windows. The Go syscall package does not expose
// Statfs_t on Windows, and the Windows API for disk space queries requires
// syscall.UTF16PtrFromString which is not worth the complexity for a pre-flight
// check. The disk-space guard is best-effort — on Windows, the download will
// fail with a clear "disk full" error from os.Create if space is insufficient.
func checkDiskSpace(path string, minFree int64) error {
	return nil
}
