package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeMarker creates a file containing marker text inside dir.
func writeMarker(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
}

func TestValidateStandardLocation_OK(t *testing.T) {
	dir := t.TempDir()
	if err := validateStandardLocation(dir); err != nil {
		t.Fatalf("expected no error for writable dir, got: %v", err)
	}
}

func TestValidateStandardLocation_TempDirRejected(t *testing.T) {
	temp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Skipf("cannot resolve temp dir: %v", err)
	}
	sub := filepath.Join(temp, "c0wrk-validate-test")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sub) })
	err = validateStandardLocation(sub)
	if !errors.Is(err, ErrNonStandardLocation) {
		t.Fatalf("expected ErrNonStandardLocation for temp dir, got: %v", err)
	}
	if !strings.Contains(err.Error(), "temporary") {
		t.Fatalf("error should mention temporary, got: %q", err.Error())
	}
}

func TestValidateStandardLocation_DownloadsRejected(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "Downloads", "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	err := validateStandardLocation(dir)
	if !errors.Is(err, ErrNonStandardLocation) {
		t.Fatalf("expected ErrNonStandardLocation for Downloads, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Downloads") {
		t.Fatalf("error should mention Downloads, got: %q", err.Error())
	}
}

func TestValidateStandardLocation_ReadOnlyRejected(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks; cannot test read-only rejection")
	}
	dir := t.TempDir()
	// Remove owner write bit so the writability probe fails.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	err := validateStandardLocation(dir)
	if !errors.Is(err, ErrNonStandardLocation) {
		t.Fatalf("expected ErrNonStandardLocation for read-only dir, got: %v", err)
	}
	if !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("error should mention not writable, got: %q", err.Error())
	}
}

// TestValidateStandardLocation_ReadOnlyParentRejected verifies that a writable
// install dir inside a read-only parent is rejected: the swap renames entries
// in the parent, so parent writability is the operationally critical check
// (e.g. a user-writable .app under an admin-only /Applications).
func TestValidateStandardLocation_ReadOnlyParentRejected(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks; cannot test read-only rejection")
	}
	base := t.TempDir()
	install := filepath.Join(base, "app")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatalf("mkdir install: %v", err)
	}
	// Make the PARENT read-only so the install tree cannot be renamed.
	if err := os.Chmod(base, 0o500); err != nil {
		t.Fatalf("chmod base: %v", err)
	}
	// Restore perms so the test framework can clean up.
	t.Cleanup(func() { _ = os.Chmod(base, 0o755) })
	err := validateStandardLocation(install)
	if !errors.Is(err, ErrNonStandardLocation) {
		t.Fatalf("expected ErrNonStandardLocation for read-only parent, got: %v", err)
	}
	if !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("error should mention not writable, got: %q", err.Error())
	}
}

func TestHasDownloadsComponent(t *testing.T) {
	cases := map[string]bool{
		"/home/user/downloads":          true,
		"/home/user/Downloads/app":      true,
		"/Applications/c0wrk.app":       false,
		"/opt/c0wrk":                    false,
		"/Users/x/Library/downloads/x":  true,
		"/usr/local/bin":                false,
	}
	for path, want := range cases {
		if got := hasDownloadsComponent(strings.ToLower(path)); got != want {
			t.Errorf("hasDownloadsComponent(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestPathContains(t *testing.T) {
	base := t.TempDir()
	inner := filepath.Join(base, "sub")
	nested := filepath.Join(base, "sub", "deep")
	other := t.TempDir()

	cases := []struct {
		cand string
		want bool
	}{
		{inner, true},
		{nested, true},
		{base, false}, // same as base is not "contained"
		{other, false},
	}
	for _, c := range cases {
		if got := pathContains(c.cand, base); got != c.want {
			t.Errorf("pathContains(%q, %q) = %v, want %v", c.cand, base, got, c.want)
		}
	}
}

func TestParseSelfUpdateFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    bool
		wantErr bool
		wantPID int
	}{
		{
			name:    "complete",
			args:    []string{"--self-update", "--pid", "12345", "--stage", "/s", "--target", "/t"},
			want:    true,
			wantPID: 12345,
		},
		{
			name:    "not self update",
			args:    []string{"--foo", "bar"},
			want:    false,
			wantPID: 0,
		},
		{
			name:    "missing pid",
			args:    []string{"--self-update", "--stage", "/s", "--target", "/t"},
			want:    true,
			wantErr: true,
		},
		{
			name:    "missing stage",
			args:    []string{"--self-update", "--pid", "5", "--target", "/t"},
			want:    true,
			wantErr: true,
		},
		{
			name:    "missing target",
			args:    []string{"--self-update", "--pid", "5", "--stage", "/s"},
			want:    true,
			wantErr: true,
		},
		{
			name:    "invalid pid",
			args:    []string{"--self-update", "--pid", "abc", "--stage", "/s", "--target", "/t"},
			want:    true,
			wantErr: true,
		},
		{
			name:    "pid missing value",
			args:    []string{"--self-update", "--pid"},
			want:    true,
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts, isSelfUpdate, err := ParseSelfUpdateFlags(c.args)
			if isSelfUpdate != c.want {
				t.Fatalf("isSelfUpdate = %v, want %v", isSelfUpdate, c.want)
			}
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if opts.PID != c.wantPID {
				t.Errorf("PID = %d, want %d", opts.PID, c.wantPID)
			}
		})
	}
}

// makeZip builds a zip archive at dest whose entries mirror the files passed as
// name->content pairs (names use forward slashes).
func makeZip(t *testing.T, dest string, files map[string]string) {
	t.Helper()
	f, err := os.Create(dest)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer func() { _ = f.Close() }()
	w := zip.NewWriter(f)
	for name, content := range files {
		out, err := w.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := io.WriteString(out, content); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
}

// makeTarGz builds a tar.gz archive at dest from the name->content pairs.
func makeTarGz(t *testing.T, dest string, files map[string]string) {
	t.Helper()
	f, err := os.Create(dest)
	if err != nil {
		t.Fatalf("create tar.gz: %v", err)
	}
	defer func() { _ = f.Close() }()
	gz := gzip.NewWriter(f)
	defer func() { _ = gz.Close() }()
	tw := tar.NewWriter(gz)
	defer func() { _ = tw.Close() }()
	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if strings.HasSuffix(name, "/") {
			hdr.Typeflag = tar.TypeDir
			hdr.Size = 0
			if err := tw.WriteHeader(hdr); err != nil {
				t.Fatalf("write header %s: %v", name, err)
			}
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write body %s: %v", name, err)
		}
	}
}

func TestExtractZip(t *testing.T) {
	dest := t.TempDir()
	archive := filepath.Join(t.TempDir(), "test.zip")
	makeZip(t, archive, map[string]string{
		"a.txt":              "alpha",
		"sub/b.txt":          "beta",
		"sub/":               "",
	})
	if err := extractArchive(archive, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "a.txt")); string(got) != "alpha" {
		t.Errorf("a.txt = %q, want alpha", string(got))
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "sub", "b.txt")); string(got) != "beta" {
		t.Errorf("sub/b.txt = %q, want beta", string(got))
	}
}

func TestExtractZip_TraversalRejected(t *testing.T) {
	dest := t.TempDir()
	archive := filepath.Join(t.TempDir(), "evil.zip")
	makeZip(t, archive, map[string]string{
		"../escape.txt": "pwned",
	})
	err := extractArchive(archive, dest)
	if err == nil {
		t.Fatal("expected error for path-traversal zip entry, got nil")
	}
	escape := filepath.Join(filepath.Dir(dest), "escape.txt")
	if _, err := os.Stat(escape); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("escape file should not exist outside dest: %v", err)
	}
}

func TestExtractTarGz(t *testing.T) {
	dest := t.TempDir()
	archive := filepath.Join(t.TempDir(), "test.tar.gz")
	makeTarGz(t, archive, map[string]string{
		"x.txt": "xyz",
		"d/y.txt": "yy",
		"d/":    "",
	})
	if err := extractArchive(archive, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "x.txt")); string(got) != "xyz" {
		t.Errorf("x.txt = %q, want xyz", string(got))
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "d", "y.txt")); string(got) != "yy" {
		t.Errorf("d/y.txt = %q, want yy", string(got))
	}
}

func TestExtractTarGz_TraversalRejected(t *testing.T) {
	dest := t.TempDir()
	archive := filepath.Join(t.TempDir(), "evil.tar.gz")
	makeTarGz(t, archive, map[string]string{
		"../escape.txt": "pwned",
	})
	err := extractArchive(archive, dest)
	if err == nil {
		t.Fatal("expected error for path-traversal tar entry, got nil")
	}
}

func TestFindStagedArchive(t *testing.T) {
	dir := t.TempDir()
	if _, err := findStagedArchive(dir); err == nil {
		t.Fatal("expected error when no archive present")
	}
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "c0wrk-desktop.tar.gz")
	if err := os.WriteFile(archive, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := findStagedArchive(dir)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got != archive {
		t.Errorf("got %q, want %q", got, archive)
	}
}

func TestSwapInstallTrees_Success(t *testing.T) {
	root := t.TempDir()
	// Current install tree.
	target := filepath.Join(root, "c0wrk-install")
	writeMarker(t, target, "old.txt", "old-content")
	// New tree.
	newTree := filepath.Join(root, "new-tree")
	writeMarker(t, newTree, "new.txt", "new-content")

	backup := target + ".old"
	if err := swapInstallTrees(target, newTree, backup); err != nil {
		t.Fatalf("swap: %v", err)
	}
	// New tree is now at target.
	got, err := os.ReadFile(filepath.Join(target, "new.txt"))
	if err != nil {
		t.Fatalf("read new marker: %v", err)
	}
	if string(got) != "new-content" {
		t.Errorf("target has wrong content: %q", string(got))
	}
	// Old content backed up.
	got, err = os.ReadFile(filepath.Join(backup, "old.txt"))
	if err != nil {
		t.Fatalf("read backup marker: %v", err)
	}
	if string(got) != "old-content" {
		t.Errorf("backup has wrong content: %q", string(got))
	}
	// New tree source is gone.
	if _, err := os.Stat(newTree); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("new tree source should be gone after move")
	}
}

func TestSwapInstallTrees_Rollback(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "c0wrk-install")
	writeMarker(t, target, "keep.txt", "keep-content")
	// Point newTree at a nonexistent path so the move step fails after the
	// backup rename succeeds, forcing the rollback path.
	newTree := filepath.Join(root, "does-not-exist")
	backup := target + ".old"

	err := swapInstallTrees(target, newTree, backup)
	if err == nil {
		t.Fatal("expected swap to fail when new tree is missing")
	}
	// Target must be restored from the rollback.
	got, rerr := os.ReadFile(filepath.Join(target, "keep.txt"))
	if rerr != nil {
		t.Fatalf("target not restored after rollback: %v", rerr)
	}
	if string(got) != "keep-content" {
		t.Errorf("target content after rollback = %q", string(got))
	}
}

// buildPlatformArchive builds an update archive at archivePath whose extraction
// satisfies resolveNewTree on the current platform, planting a marker file so
// the swap result can be verified. It returns the relative marker path that
// should exist at the install root after the swap.
func buildPlatformArchive(t *testing.T, archivePath string) string {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		// macOS: archive a top-level .app bundle containing a marker.
		makeZip(t, archivePath, map[string]string{
			"TestApp.app/Contents/Info.plist": "plist",
			"TestApp.app/Contents/MacOS/":     "",
			"TestApp.app/Contents/MacOS/TestApp": "binary",
			"TestApp.app/marker.txt":          "installed-via-swap",
		})
		return "marker.txt"
	default:
		// Linux/Windows: archive root is the install tree.
		if strings.HasSuffix(archivePath, ".zip") {
			makeZip(t, archivePath, map[string]string{
				"marker.txt": "installed-via-swap",
			})
		} else {
			makeTarGz(t, archivePath, map[string]string{
				"marker.txt": "installed-via-swap",
			})
		}
		return "marker.txt"
	}
}

// TestApplySelfUpdate_Smoke runs the full ApplySelfUpdate orchestration in a
// controlled environment on the current OS: a dead parent PID, a staged
// archive, a real install root, and a no-op relaunch recorder.
func TestApplySelfUpdate_Smoke(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test in -short mode")
	}
	// Shorten the poll window so a stale PID is noticed quickly.
	origTimeout := shutdownTimeout
	shutdownTimeout = 2 * time.Second
	t.Cleanup(func() { shutdownTimeout = origTimeout })

	// A parent PID that is already dead: spawn a child, wait for it to exit.
	dead := spawnDeadPID(t)

	root := t.TempDir()
	target := filepath.Join(root, "install-target")
	writeMarker(t, target, "version.txt", "old")
	stageDir := t.TempDir()

	// Build the staged update archive in the platform-appropriate format.
	var archivePath string
	switch runtime.GOOS {
	case "darwin":
		archivePath = filepath.Join(stageDir, "c0wrk-desktop-macos-arm64.zip")
	case "windows":
		archivePath = filepath.Join(stageDir, "c0wrk-desktop-windows-amd64.zip")
	default:
		archivePath = filepath.Join(stageDir, "c0wrk-desktop-linux-amd64.tar.gz")
	}
	markerRel := buildPlatformArchive(t, archivePath)

	// Substitute a no-op relaunch so no GUI process is actually started.
	relaunched := false
	origRelaunch := relaunchFn
	relaunchFn = func(targetDir string, log *slog.Logger) error {
		relaunched = true
		return nil
	}
	t.Cleanup(func() { relaunchFn = origRelaunch })

	opts := SelfUpdateOptions{PID: dead, StageDir: stageDir, TargetDir: target}
	if err := ApplySelfUpdate(opts, slog.Default()); err != nil {
		t.Fatalf("ApplySelfUpdate: %v", err)
	}

	if !relaunched {
		t.Error("relaunch was not invoked")
	}
	// New content must be at the install target.
	got, err := os.ReadFile(filepath.Join(target, markerRel))
	if err != nil {
		t.Fatalf("new marker missing at target: %v", err)
	}
	if string(got) != "installed-via-swap" {
		t.Errorf("new marker content = %q", string(got))
	}
	// Previous version backed up for rollback.
	if _, err := os.Stat(target + ".old"); err != nil {
		t.Errorf(".old backup missing: %v", err)
	}
	// Staging directory cleaned up.
	if _, err := os.Stat(stageDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staging dir should be removed after update, stat err = %v", err)
	}
}

// spawnDeadPID starts a trivial child process, waits for it to exit, and
// returns its (now-defunct) PID. Used as a parent PID that waitForProcessExit
// immediately observes as gone.
func spawnDeadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "true")
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(context.Background(), "cmd", "/c", "exit", "0")
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn helper process: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	return pid
}
