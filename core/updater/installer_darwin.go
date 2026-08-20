package updater

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// findInstallRoot resolves the .app bundle enclosing the running executable on
// macOS. The executable lives at <bundle>.app/Contents/MacOS/<name>; we climb
// ancestors until we find a directory whose suffix is ".app". A bare dev binary
// (no enclosing .app bundle) has no safe install root — the directory it was
// placed in may contain unrelated user files, and swapping that directory in
// place would displace them — so it is refused with ErrNonStandardLocation.
func findInstallRoot() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		// Fall back to the raw path if symlink resolution fails.
		exe = filepath.Clean(exe)
	}
	dir := filepath.Dir(exe)
	for d := dir; d != "/" && d != "."; d = filepath.Dir(d) {
		if filepath.Ext(d) == ".app" {
			return d, nil
		}
	}
	// No .app ancestor: refuse a bare-binary in-place update.
	return "", ErrNonStandardLocation
}

// hasInstallMarker reports whether root is a macOS .app bundle — the only
// install-root shape the updater swaps on this platform.
func hasInstallMarker(root string) bool {
	return filepath.Ext(root) == ".app"
}

// resolveNewTreePlatform locates the top-level *.app bundle inside the
// extraction directory. macOS update archives package exactly one .app.
func resolveNewTreePlatform(extractDir string) (string, error) {
	entries, err := os.ReadDir(extractDir)
	if err != nil {
		return "", fmt.Errorf("read extraction dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() && filepath.Ext(e.Name()) == ".app" {
			return filepath.Join(extractDir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("no .app bundle found in extraction dir %s", extractDir)
}

// relaunchApp launches the freshly swapped .app bundle using `open -n` so the
// new process starts independently of the (exiting) updater.
func relaunchApp(targetDir string, log *slog.Logger) error {
	cmd := exec.CommandContext(context.Background(), "open", "-n", targetDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("open %s: %w (%s)", targetDir, err, string(out))
	}
	log.Info("relaunched app via open", "target", targetDir)
	return nil
}

// updaterStagingBase is the directory under which staging directories are
// created.
func updaterStagingBase() string { return os.TempDir() }

// normalizedTempRoot returns the cleaned OS temporary directory.
func normalizedTempRoot() (string, error) {
	return filepath.EvalSymlinks(os.TempDir())
}

// copyExecutable copies src to dst and marks it executable (Unix perms).
func copyExecutable(src, dst string) (string, error) {
	if err := copyFile(src, dst, 0o755); err != nil {
		return "", fmt.Errorf("copy staging updater: %w", err)
	}
	return dst, nil
}

// processAlive reports whether the given PID currently has a live process, via
// signal 0.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		// ESRCH = no such process; EPERM = exists but not ours.
		return errors.Is(err, syscall.EPERM)
	}
	return true
}

// cleanupStaleUpdatersPlatform removes leftover c0wrk-update-* staging dirs in
// the OS temp directory from prior crashed runs.
func cleanupStaleUpdatersPlatform(log *slog.Logger) {
	cleanupTempGlobs(log, "c0wrk-update-*", "c0wrk-extract-*")
}
