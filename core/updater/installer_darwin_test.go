//go:build darwin

package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestStagingLaunchPath_BundleLayoutAndSignature covers the happy path: the
// staged updater payload is renamed into a minimal LSUIElement .app bundle,
// the written Info.plist is sealed by an ad-hoc codesign, and the returned
// launch path is the bundle itself (what `open -n` then launches).
func TestStagingLaunchPath_BundleLayoutAndSignature(t *testing.T) {
	staging := t.TempDir()
	// A valid Mach-O the ad-hoc signature can seal: the test binary itself.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(staging, "updater-payload")
	if err := os.WriteFile(payload, data, 0o755); err != nil {
		t.Fatal(err)
	}

	appPath, err := stagingLaunchPath(staging, payload)
	if err != nil {
		t.Fatalf("stagingLaunchPath: %v", err)
	}
	wantApp := filepath.Join(staging, "c0wrk-updater.app")
	if appPath != wantApp {
		t.Fatalf("launch path = %q; want %q", appPath, wantApp)
	}
	binDest := filepath.Join(appPath, "Contents", "MacOS", stagingUpdaterName())
	info, err := os.Stat(binDest)
	if err != nil {
		t.Fatalf("staged binary not in bundle layout: %v", err)
	}
	if info.Mode()&0o100 == 0 {
		t.Fatalf("staged binary mode = %v; want the executable bit carried through the rename", info.Mode())
	}
	if _, err := os.Stat(filepath.Join(appPath, "Contents", "Info.plist")); err != nil {
		t.Fatalf("bundle Info.plist missing: %v", err)
	}
	if _, err := os.Stat(payload); !os.IsNotExist(err) {
		t.Fatal("payload must be renamed INTO the bundle, not copied")
	}
	// The seal LaunchServices/amfid will enforce at exec time must verify.
	if out, err := exec.Command("codesign", "--verify", appPath).CombinedOutput(); err != nil {
		t.Fatalf("ad-hoc re-signed bundle fails codesign --verify: %v (%s)", err, out)
	}
}

// TestStagingLaunchPath_CodesignFailureIsReported covers the seal failure: a
// payload that is not a Mach-O makes codesign refuse the bundle, and
// stagingLaunchPath must surface that as the launch-path error (PrepareSelfUpdate
// then removes the whole staging tree on any stagingLaunchPath failure — its
// own contract, verified in installer.go's failure branches).
func TestStagingLaunchPath_CodesignFailureIsReported(t *testing.T) {
	staging := t.TempDir()
	payload := filepath.Join(staging, "updater-payload")
	if err := os.WriteFile(payload, []byte("this is not a Mach-O binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := stagingLaunchPath(staging, payload); err == nil {
		t.Fatal("codesign on a non-Mach-O payload must fail the launch path")
	}
}
