//go:build windows

package updater

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// findInstallRoot returns the directory containing the running .exe on Windows.
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

// resolveNewTreePlatform returns the extraction directory itself on Windows: the
// update archive (build\bin\*) unpacks to the new install tree directly.
func resolveNewTreePlatform(extractDir string) (string, error) {
	if info, err := os.Stat(extractDir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("extraction dir missing: %w", err)
	}
	return extractDir, nil
}

// relaunchApp launches the freshly installed .exe directly with
// DETACHED_PROCESS so it survives the updater process. It deliberately does
// NOT route through `cmd /c start`: `start` treats the first quoted argument
// as the window title, so a path containing spaces (e.g. under "Program
// Files") is mis-split and the relaunch silently fails. A direct exec avoids
// any shell quoting pitfalls.
func relaunchApp(targetDir string, log *slog.Logger) error {
	exePath := findExecutableIn(targetDir)
	if exePath == "" {
		return fmt.Errorf("no executable found in install dir %s", targetDir)
	}
	// DETACHED_PROCESS (0x00000008): the child has no console and is not tied
	// to this process, so it keeps running after the updater exits.
	const detachedProcess = 0x00000008
	cmd := exec.CommandContext(context.Background(), exePath)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", exePath, err)
	}
	log.Info("relaunched app", "target", exePath)
	return nil
}

// findExecutableIn returns the path to the c0wrk .exe inside dir, or "".
func findExecutableIn(dir string) string {
	const exeName = "c0wrk-desktop.exe"
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
		if filepath.Ext(e.Name()) == ".exe" {
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

// copyExecutable copies src to dst. Windows ignores the Unix mode bits, but
// the copy is functionally executable by virtue of the .exe extension.
func copyExecutable(src, dst string) (string, error) {
	if err := copyFile(src, dst, 0o755); err != nil {
		return "", fmt.Errorf("copy staging updater: %w", err)
	}
	return dst, nil
}

// processAlive reports whether the given PID currently has a live process. On
// Windows this uses OpenProcess; an access-denied result still implies the
// process exists.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const (
		procQueryLimitedInfo = 0x1000
		stillActive          = 259
	)
	handle, err := syscall.OpenProcess(procQueryLimitedInfo, false, uint32(pid))
	if err != nil {
		// ERROR_INVALID_PARAMETER (87) means the process is gone.
		return false
	}
	defer func() { _ = syscall.CloseHandle(handle) }()
	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == stillActive
}

// cleanupStaleUpdatersPlatform removes leftover c0wrk-updater-*.exe copies in
// %TEMP%. Because a running Windows updater .exe cannot self-delete, this runs
// at the *next* normal startup to reap orphans from prior updates.
func cleanupStaleUpdatersPlatform(log *slog.Logger) {
	cleanupTempGlobs(log,
		"c0wrk-updater.exe", // staging updater copy that cannot self-delete on Windows
		"c0wrk-update-*",    // staging dirs
		"c0wrk-updater-*",   // leftover updater-related dirs
	)
}
