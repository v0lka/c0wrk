//go:build linux

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

// findInstallRoot returns the directory containing the running binary on Linux.
func findInstallRoot() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}

// hasInstallMarker reports whether root contains the canonical c0wrk-desktop
// binary — the install-root shape the updater swaps on Linux.
func hasInstallMarker(root string) bool {
	info, err := os.Stat(filepath.Join(root, "c0wrk-desktop"))
	return err == nil && !info.IsDir()
}

// resolveNewTreePlatform returns the extraction directory itself on Linux: the
// update archive root is the build/output directory, so its contents become the
// new install tree directly.
func resolveNewTreePlatform(extractDir string) (string, error) {
	if info, err := os.Stat(extractDir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("extraction dir missing: %w", err)
	}
	return extractDir, nil
}

// relaunchApp launches the freshly installed binary. The target directory is
// the binary directory; the new binary is launched detached so the updater can
// exit cleanly.
func relaunchApp(targetDir string, log *slog.Logger) error {
	binPath := findExecutableIn(targetDir)
	if binPath == "" {
		return fmt.Errorf("no executable found in install dir %s", targetDir)
	}
	cmd := exec.CommandContext(context.Background(), binPath)
	cmd.Env = os.Environ()
	// Detach so the relaunched app survives the updater's exit.
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", binPath, err)
	}
	// Release the child so it is not reaped when the updater exits.
	if err := cmd.Process.Release(); err != nil {
		log.Debug("could not release relaunched process (best-effort)", "error", err)
	}
	log.Info("relaunched app", "target", binPath)
	return nil
}

// findExecutableIn returns the path to the c0wrk executable inside dir, or ""
// if none is found.
func findExecutableIn(dir string) string {
	const exeName = "c0wrk-desktop"
	candidate := filepath.Join(dir, exeName)
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if info.Mode().Perm()&0o111 != 0 {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
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
		return errors.Is(err, syscall.EPERM)
	}
	return true
}

// cleanupStaleUpdatersPlatform removes leftover staging artifacts from prior
// crashed runs.
func cleanupStaleUpdatersPlatform(log *slog.Logger) {
	cleanupTempGlobs(log, "c0wrk-update-*", "c0wrk-extract-*")
}
