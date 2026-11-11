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
	"time"
)

// findInstallRoot resolves the .app bundle enclosing the running executable on
// macOS (<bundle>.app/Contents/MacOS/<name>). A bare dev binary (no enclosing
// .app) has no safe install root — swapping its directory could displace
// unrelated user files — so it is refused with ErrNonStandardLocation.
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

// stagingLaunchPath wraps the staged updater binary into a minimal
// LSUIElement .app bundle and returns the bundle's path. On macOS the
// updater MUST be launched through LaunchServices (`open`), never as a bare
// fork/exec child of the quitting app: a bare child keeps the dying GUI app
// as its responsible process — an attribution that survives even a Setsid
// detach — and is killed silently by the system about a second after the
// app exits, mid-apply: no error, no crash report, only a one-line
// updater-output.log and a leftover staging dir (the "app closes and
// nothing happens" failure, reproduced on macOS 27). A LaunchServices
// launch hands the updater to launchd as its parent with responsibility of
// its own, which survives the app's death — the same discipline
// Squirrel.Mac's ShipIt helper follows. The updater itself never notices
// the bundle: main.go handles --self-update before anything Wails-related
// runs, and it redirects its own stdio to the staging log.
func stagingLaunchPath(stagingDir, updaterPath string) (string, error) {
	appPath := filepath.Join(stagingDir, "c0wrk-updater.app")
	binDest := filepath.Join(appPath, "Contents", "MacOS", stagingUpdaterName())
	if err := os.MkdirAll(filepath.Dir(binDest), 0o750); err != nil {
		return "", fmt.Errorf("create updater bundle dirs: %w", err)
	}
	if err := os.Rename(updaterPath, binDest); err != nil {
		return "", fmt.Errorf("move staged updater into bundle: %w", err)
	}
	plistPath := filepath.Join(appPath, "Contents", "Info.plist")
	if err := os.WriteFile(plistPath, []byte(updaterBundleInfoPlist), 0o644); err != nil {
		return "", fmt.Errorf("write updater Info.plist: %w", err)
	}
	// Seal the freshly written Info.plist into the bundle's signature, the
	// same ad-hoc re-sign copyExecutable applies to the binary (same
	// bounded-context reasoning: a valid re-sign of a ~80 MB bundle is
	// sub-second).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codesign", "--force", "--sign", "-", appPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("re-sign updater bundle (codesign): %w (%s)", err, string(out))
	}
	return appPath, nil
}

// updaterBundleInfoPlist is the staging updater bundle's entire identity: an
// LSUIElement (faceless — no Dock icon, no menu bar) executable wrapper. The
// bundle identifier is deliberately distinct from the app's so LaunchServices
// never confuses the two.
const updaterBundleInfoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key>
	<string>c0wrk-updater</string>
	<key>CFBundleIdentifier</key>
	<string>app.c0wrk.self-updater</string>
	<key>CFBundleName</key>
	<string>c0wrk-updater</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleVersion</key>
	<string>1</string>
	<key>LSUIElement</key>
	<true/>
</dict>
</plist>
`

// normalizedTempRoot returns the cleaned OS temporary directory.
func normalizedTempRoot() (string, error) {
	return filepath.EvalSymlinks(os.TempDir())
}

// copyExecutable copies src to dst with Unix exec perms, then re-signs the
// copy ad-hoc: the running binary's embedded signature is sealed to its bundle
// context, so a bare copy is SIGKILLed at exec while cmd.Start still reports
// success (observed on macOS 27 — the flow "succeeds" but no update lands).
// The bytes stay the SHA256-verified release's own; only the signature wrapper
// is regenerated locally (specs/decisions/023-auto-update.md).
func copyExecutable(src, dst string) (string, error) {
	if err := copyFile(src, dst, 0o755); err != nil {
		return "", fmt.Errorf("copy staging updater: %w", err)
	}
	// Bounded context (the caller has none): a wedged codesign must not hang
	// the apply path — a valid ad-hoc re-sign of a ~80 MB Mach-O is sub-second.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codesign", "--force", "--sign", "-", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("re-sign staging updater (codesign): %w (%s)", err, string(out))
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
	cleanupTempGlobs(os.TempDir(), log, "c0wrk-update-*", "c0wrk-extract-*")
}
