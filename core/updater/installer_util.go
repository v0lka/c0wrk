package updater

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// copyFile copies the regular file at src to dst, applying the given mode.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// cleanupTempGlobs removes entries in the OS temp directory matching any of the
// given glob patterns. Errors are logged at debug level and otherwise ignored
// (best-effort cleanup).
func cleanupTempGlobs(log *slog.Logger, patterns ...string) {
	tempDir, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		tempDir = os.TempDir()
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(tempDir, pattern))
		if err != nil {
			continue
		}
		for _, m := range matches {
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
