//go:build !windows

package embeddedllm

// isTransientRenameError always reports false off Windows: POSIX rename(2)
// onto an existing target is atomic and never fails with a sharing error, so
// a failed promotion is always a real failure and retrying it would only
// delay the inevitable.
func isTransientRenameError(_ error) bool { return false }
