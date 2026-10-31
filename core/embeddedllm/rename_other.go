//go:build !windows

package embeddedllm

// isTransientRenameError is always false off Windows: POSIX rename(2) onto an
// existing target never fails with a sharing error, so a failed promotion is
// a real failure.
func isTransientRenameError(_ error) bool { return false }
