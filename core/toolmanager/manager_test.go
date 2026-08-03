package toolmanager

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func makeTestDirs(t *testing.T) (toolsDir, binDir, pythonDir string) {
	t.Helper()
	base := t.TempDir()
	toolsDir = filepath.Join(base, "tools")
	binDir = filepath.Join(toolsDir, "bin")
	pythonDir = filepath.Join(toolsDir, "python")
	return toolsDir, binDir, pythonDir
}

func TestManager_EnsureCriticalTools_AllUpToDate(t *testing.T) {
	toolsDir, binDir, pythonDir := makeTestDirs(t)
	mgr := &Manager{
		ToolsDir:   toolsDir,
		BinDir:     binDir,
		PythonDir:  pythonDir,
		Downloader: DownloadFunc(stubDownload),
		Installer:  &stubInstaller{},
		Logger:     slog.New(slog.DiscardHandler),
	}

	// Pre-populate .versions with current versions so everything is skipped.
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	versions := ToolVersions{}
	tools, err := ManagedTools(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		versions[tool.Name] = tool.Version
	}
	if err := WriteVersions(toolsDir, versions); err != nil {
		t.Fatal(err)
	}

	if err := mgr.EnsureCriticalTools(context.Background()); err != nil {
		t.Fatalf("EnsureCriticalTools failed: %v", err)
	}
}

func TestManager_EnsureCriticalTools_DownloadError(t *testing.T) {
	toolsDir, binDir, pythonDir := makeTestDirs(t)
	mgr := &Manager{
		ToolsDir:   toolsDir,
		BinDir:     binDir,
		PythonDir:  pythonDir,
		Downloader: DownloadFunc(failingDownload),
		Installer:  &stubInstaller{},
		Logger:     slog.New(slog.DiscardHandler),
	}

	err := mgr.EnsureCriticalTools(context.Background())
	if err == nil {
		t.Fatal("expected error from failing downloader")
	}
}

func TestManager_GetToolPath_NotInstalled(t *testing.T) {
	toolsDir, binDir, pythonDir := makeTestDirs(t)
	mgr := &Manager{
		ToolsDir:   toolsDir,
		BinDir:     binDir,
		PythonDir:  pythonDir,
		Downloader: DownloadFunc(stubDownload),
		Installer:  &stubInstaller{},
		Logger:     slog.New(slog.DiscardHandler),
	}

	if p := mgr.GetToolPath("rg", StaticBinary); p != "" {
		t.Errorf("expected empty path for uninstalled tool, got %q", p)
	}
}

func TestManager_GetToolPath_Installed(t *testing.T) {
	toolsDir, binDir, pythonDir := makeTestDirs(t)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// GetToolPath appends ".exe" on Windows for StaticBinary tools (see
	// Manager.binaryPath), so the on-disk fixture must match the platform's
	// expected name.
	binPath := filepath.Join(binDir, "rg")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	if err := os.WriteFile(binPath, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	mgr := &Manager{
		ToolsDir:   toolsDir,
		BinDir:     binDir,
		PythonDir:  pythonDir,
		Downloader: DownloadFunc(stubDownload),
		Installer:  &stubInstaller{},
		Logger:     slog.New(slog.DiscardHandler),
	}

	if p := mgr.GetToolPath("rg", StaticBinary); p != binPath {
		t.Errorf("GetToolPath = %q, want %q", p, binPath)
	}
}

func TestManager_PrependToPATH(t *testing.T) {
	_, binDir, _ := makeTestDirs(t)
	mgr := &Manager{BinDir: binDir}
	if got := mgr.PrependToPATH(); got != binDir {
		t.Errorf("PrependToPATH = %q, want %q", got, binDir)
	}
}

func TestNewManager_ProductionDefaults(t *testing.T) {
	mgr := NewManager("/tmp/tools", "/tmp/tools/bin", "/tmp/tools/python", nil, ManagerConfig{})
	if mgr.ToolsDir != "/tmp/tools" {
		t.Errorf("ToolsDir = %q, want /tmp/tools", mgr.ToolsDir)
	}
	if mgr.BinDir != "/tmp/tools/bin" {
		t.Errorf("BinDir = %q, want /tmp/tools/bin", mgr.BinDir)
	}
	if mgr.PythonDir != "/tmp/tools/python" {
		t.Errorf("PythonDir = %q, want /tmp/tools/python", mgr.PythonDir)
	}
	if mgr.Downloader == nil {
		t.Error("Downloader is nil")
	}
	if mgr.Installer == nil {
		t.Error("Installer is nil")
	}
	if mgr.Logger == nil {
		t.Error("Logger is nil")
	}
}

func TestManager_NeedsInstall_AllUpToDate(t *testing.T) {
	toolsDir, binDir, pythonDir := makeTestDirs(t)
	mgr := &Manager{
		ToolsDir:   toolsDir,
		BinDir:     binDir,
		PythonDir:  pythonDir,
		Downloader: DownloadFunc(stubDownload),
		Installer:  &stubInstaller{},
		Logger:     slog.New(slog.DiscardHandler),
	}

	// Pre-populate .versions and create binaries so nothing is needed.
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	versions := ToolVersions{}
	tools, err := ManagedTools(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		versions[tool.Name] = tool.Version
		// Create a fake binary at the path NeedsInstall checks (binaryPath).
		binPath := mgr.binaryPath(tool.Name, tool.Type)
		if err := os.WriteFile(binPath, []byte("fake"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteVersions(toolsDir, versions); err != nil {
		t.Fatal(err)
	}

	needed, err := mgr.NeedsInstall()
	if err != nil {
		t.Fatalf("NeedsInstall failed: %v", err)
	}
	if len(needed) != 0 {
		t.Errorf("expected 0 tools needed, got %d", len(needed))
	}
}

func TestManager_NeedsInstall_AllMissing(t *testing.T) {
	toolsDir, binDir, pythonDir := makeTestDirs(t)
	mgr := &Manager{
		ToolsDir:   toolsDir,
		BinDir:     binDir,
		PythonDir:  pythonDir,
		Downloader: DownloadFunc(stubDownload),
		Installer:  &stubInstaller{},
		Logger:     slog.New(slog.DiscardHandler),
	}

	tools, err := ManagedTools(nil)
	if err != nil {
		t.Fatal(err)
	}

	needed, err := mgr.NeedsInstall()
	if err != nil {
		t.Fatalf("NeedsInstall failed: %v", err)
	}
	if len(needed) != len(tools) {
		t.Errorf("expected %d tools needed, got %d", len(tools), len(needed))
	}
}

func TestManager_NeedsInstall_VersionMismatch(t *testing.T) {
	toolsDir, binDir, pythonDir := makeTestDirs(t)
	mgr := &Manager{
		ToolsDir:   toolsDir,
		BinDir:     binDir,
		PythonDir:  pythonDir,
		Downloader: DownloadFunc(stubDownload),
		Installer:  &stubInstaller{},
		Logger:     slog.New(slog.DiscardHandler),
	}

	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a stale version for uv only.
	versions := ToolVersions{"uv": "0.0.0"}
	if err := WriteVersions(toolsDir, versions); err != nil {
		t.Fatal(err)
	}

	needed, err := mgr.NeedsInstall()
	if err != nil {
		t.Fatalf("NeedsInstall failed: %v", err)
	}
	// uv should be in needed (version mismatch), others too (no version entry).
	found := false
	for _, t := range needed {
		if t.Name == "uv" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected uv to be in needed list due to version mismatch")
	}
}

// TestManager_EnsureCriticalTools_PythonVersionBumpRebuildsEnv verifies that a
// pinned-version bump for a Python-package tool tears down the stale wrapper
// and virtual environment before reinstalling. Without this, the wrapper-
// existence short-circuit in InstallPythonPackage would skip the upgrade,
// leaving the old (potentially vulnerable) package in place while the
// .versions file already records the new version.
func TestManager_EnsureCriticalTools_PythonVersionBumpRebuildsEnv(t *testing.T) {
	toolsDir, binDir, pythonDir := makeTestDirs(t)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pythonDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mgr := &Manager{
		ToolsDir:   toolsDir,
		BinDir:     binDir,
		PythonDir:  pythonDir,
		Downloader: DownloadFunc(stubDownload),
		Installer:  &stubInstaller{},
		Logger:     slog.New(slog.DiscardHandler),
	}

	// Simulate a stale markitdown install from the previous version: a
	// wrapper script and a virtual environment that must be rebuilt.
	wrapperPath := mgr.binaryPath("markitdown", PythonPackage)
	if err := os.WriteFile(wrapperPath, []byte("stale wrapper"), 0o755); err != nil {
		t.Fatal(err)
	}
	venvDir := filepath.Join(pythonDir, "venv")
	marker := filepath.Join(venvDir, "marker")
	if err := os.MkdirAll(venvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("old venv"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Record a stale markitdown version. uv/rg have no entry and install via
	// stubs (the stubs succeed without touching disk for static binaries).
	if err := WriteVersions(toolsDir, ToolVersions{"markitdown": "0.0.0"}); err != nil {
		t.Fatal(err)
	}

	if err := mgr.EnsureCriticalTools(context.Background()); err != nil {
		t.Fatalf("EnsureCriticalTools failed: %v", err)
	}

	// The stale wrapper must be gone so the reinstall is not short-circuited.
	if _, err := os.Stat(wrapperPath); err == nil {
		t.Error("stale markitdown wrapper still present after version bump; env was not rebuilt")
	}
	// The stale venv must be gone for a clean reinstall.
	if _, err := os.Stat(marker); err == nil {
		t.Error("stale venv marker still present after version bump; env was not rebuilt")
	}
}

// ── Stubs for testing ──────────────────────────────────────────────────────

func stubDownload(ctx context.Context, tool ToolSpec, cacheDir string, progress func(int64, int64)) (*DownloadResult, error) {
	return &DownloadResult{ToolName: tool.Name, ArchivePath: "/fake/path", Downloaded: false}, nil
}

func failingDownload(ctx context.Context, tool ToolSpec, cacheDir string, progress func(int64, int64)) (*DownloadResult, error) {
	return nil, errors.New("network unavailable")
}

type stubInstaller struct{}

func (s *stubInstaller) InstallStaticBinary(archivePath string, tool ToolSpec, binDir string) (*InstallResult, error) {
	return &InstallResult{ToolName: tool.Name, BinPath: filepath.Join(binDir, tool.BinName), Installed: true}, nil
}

func (s *stubInstaller) InstallPythonPackage(ctx context.Context, tool ToolSpec, toolsDir, binDir string) (*InstallResult, error) { //nolint:gocritic // must match Installer interface
	return &InstallResult{ToolName: tool.Name, BinPath: filepath.Join(binDir, tool.BinName), Installed: true}, nil
}
