//go:build darwin

package storageprobe

import (
	"os"
	"path/filepath"
	"testing"
)

// withHomeFake replaces userHomeDir for the duration of t so the sync-root
// containment runs against a synthetic home.
func withHomeFake(t *testing.T, home string) {
	t.Helper()
	orig := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = orig })
}

// TestStatfsVolume_LocalTempIsNotNetwork is the false-positive guard from the
// acceptance criteria: statfs on a REAL local directory (the test temp dir —
// APFS on any darwin CI runner) must report a local volume with no
// placeholder management, so the advisory stays silent for ordinary local
// projects.
func TestStatfsVolume_LocalTempIsNotNetwork(t *testing.T) {
	vc, err := statfsVolume(t.TempDir())
	if err != nil {
		t.Fatalf("statfsVolume: %v", err)
	}
	if vc.Network {
		t.Errorf("local volume reported Network=true (%s), want false", vc.NetworkDetail)
	}
	if vc.Placeholder {
		t.Errorf("local volume reported Placeholder=true (%s), want false", vc.PlaceholderDetail)
	}
}

// TestProbeWorkspace_RealLocalDirSilent runs the full probe over the real
// temp dir (with the seam left at the real statfs reader) — the acceptance
// criteria's "local APFS gives no false positives", end to end.
func TestProbeWorkspace_RealLocalDirSilent(t *testing.T) {
	if findings := ProbeWorkspace(t.TempDir()); findings != nil {
		t.Fatalf("real local dir findings = %+v, want none", findings)
	}
}

// TestConfigDirFindings_UnderMobileDocuments covers the iCloud-Drive leg: a
// config dir resolving under ~/Library/Mobile Documents must produce exactly
// one synced_config_dir finding.
func TestConfigDirFindings_UnderMobileDocuments(t *testing.T) {
	home := t.TempDir()
	withHomeFake(t, home)

	dir := filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs", "c0wrk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	findings := configDirFindings(dir)
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one", findings)
	}
	if findings[0].Kind != KindSyncedConfigDir {
		t.Errorf("finding kind = %q, want %q", findings[0].Kind, KindSyncedConfigDir)
	}
}

// TestConfigDirFindings_UnderCloudStorage covers the FileProvider leg: OneDrive
// and other third-party providers present themselves under
// ~/Library/CloudStorage.
func TestConfigDirFindings_UnderCloudStorage(t *testing.T) {
	home := t.TempDir()
	withHomeFake(t, home)

	dir := filepath.Join(home, "Library", "CloudStorage", "OneDrive-Personal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	findings := configDirFindings(dir)
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one", findings)
	}
	if findings[0].Kind != KindSyncedConfigDir {
		t.Errorf("finding kind = %q, want %q", findings[0].Kind, KindSyncedConfigDir)
	}
}

// TestConfigDirFindings_SymlinkedConfigDirCaught pins the resolution step: a
// ~/.c0wrk symlinked INTO a sync root must still be caught — the containment
// runs on the resolved path, not the spelled one.
func TestConfigDirFindings_SymlinkedConfigDirCaught(t *testing.T) {
	home := t.TempDir()
	withHomeFake(t, home)

	synced := filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs", "c0wrk")
	if err := os.MkdirAll(synced, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	link := filepath.Join(home, "c0wrk-link")
	if err := os.Symlink(synced, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	findings := configDirFindings(link)
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one (the resolved path is inside the sync root)", findings)
	}
	if findings[0].Kind != KindSyncedConfigDir {
		t.Errorf("finding kind = %q, want %q", findings[0].Kind, KindSyncedConfigDir)
	}
}

// TestConfigDirFindings_LocalHomeSilent pins the negative half: an ordinary
// config dir outside the sync roots must produce nothing — a local machine
// never sees this advisory.
func TestConfigDirFindings_LocalHomeSilent(t *testing.T) {
	home := t.TempDir()
	withHomeFake(t, home)

	dir := filepath.Join(home, ".c0wrk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// A similarly-named sibling of the sync roots must not match either.
	decoy := filepath.Join(home, "Library", "Mobile Documents Backup", "c0wrk")
	if err := os.MkdirAll(decoy, 0o755); err != nil {
		t.Fatalf("MkdirAll decoy: %v", err)
	}

	if findings := configDirFindings(dir); findings != nil {
		t.Fatalf("local config dir findings = %+v, want none", findings)
	}
	if findings := configDirFindings(decoy); findings != nil {
		t.Fatalf("sync-root sibling findings = %+v, want none", findings)
	}
}

// TestConfigDirFindings_CaseInsensitiveMatch pins the fold: macOS filesystems
// are case-insensitive by default, so a path spelled with different casing
// than the sync root still matches (pathutil.IsWithinPathFold).
func TestConfigDirFindings_CaseInsensitiveMatch(t *testing.T) {
	home := t.TempDir()
	withHomeFake(t, home)

	dir := filepath.Join(home, "library", "mobile documents", "com~apple~CloudDocs", "c0wrk")
	if err := os.MkdirAll(filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs", "c0wrk"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	findings := configDirFindings(dir)
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one (containment folds case)", findings)
	}
}

// TestConfigDirFindings_HomeErrorSilent pins the seam-failure behavior: an
// unreadable home must produce nothing, never a guess.
func TestConfigDirFindings_HomeErrorSilent(t *testing.T) {
	orig := userHomeDir
	userHomeDir = func() (string, error) { return "", os.ErrPermission }
	t.Cleanup(func() { userHomeDir = orig })

	if findings := configDirFindings("/any/.c0wrk"); findings != nil {
		t.Fatalf("findings = %+v, want none", findings)
	}
}
