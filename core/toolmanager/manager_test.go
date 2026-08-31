package toolmanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

// Fake-tool harness ─────────────────────────────────────────────────────────
//
// Post-install and fast-path version verification executes the installed
// binary, so tests need a real, portable executable that reports a canned
// version. The stand-in is the test binary itself (the stdlib helper-process
// pattern): copies of it placed at managed-tool paths re-enter TestMain in
// "fake tool" mode when TOOLMANAGER_TEST_FAKE_TOOL=1 is present in the
// environment (t.Setenv in the parent test is inherited by the child), print
// "<name> version <v>" — v sourced from TOOLMANAGER_TEST_VERSION_<NAME> — and
// exit 0. This works on every platform, including Windows where shell scripts
// are not executable.
const (
	fakeToolEnvGate          = "TOOLMANAGER_TEST_FAKE_TOOL"
	fakeToolVersionEnvPrefix = "TOOLMANAGER_TEST_VERSION_"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeToolEnvGate) == "1" {
		name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
		fmt.Printf("%s version %s\n", strings.ToLower(name), os.Getenv(fakeToolEnvName(name)))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeToolEnvName maps a tool name to its canned-version environment variable
// (mirrors the derivation in TestMain).
func fakeToolEnvName(toolName string) string {
	return fakeToolVersionEnvPrefix + strings.ToUpper(strings.ReplaceAll(toolName, "-", "_"))
}

// setFakeToolVersion pins the version the fake-tool stand-in reports for
// toolName (see TestMain). It also arms the fake-tool gate so the child
// re-enters TestMain in fake mode — tests cannot forget it.
func setFakeToolVersion(t *testing.T, toolName, version string) {
	t.Helper()
	t.Setenv(fakeToolEnvGate, "1")
	t.Setenv(fakeToolEnvName(toolName), version)
}

// copySelfBinaryTo copies the running test binary to path. When executed with
// the fake-tool gate set it acts as a managed tool stand-in (see TestMain).
func copySelfBinaryTo(path string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return copyFile(exe, path, 0o755)
}

func copySelfBinary(t *testing.T, path string) {
	t.Helper()
	if err := copySelfBinaryTo(path); err != nil {
		t.Fatalf("copying test binary to %s: %v", path, err)
	}
}

// goBinaryPath locates the go toolchain binary — used as a stand-in "stale"
// binary whose --version output provably lacks any managed tool version
// (`go --version` exits 2 with a flag error). A byte-identical copy of a real
// executable keeps valid code signatures (darwin/arm64 enforces them), so the
// fixture works on every platform.
func goBinaryPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("go")
	if err != nil {
		// Tests always run under the go toolchain, which puts the go binary
		// on PATH (setup-go in CI, shell env locally); skipping — rather
		// than failing — keeps the suite honest about fixture availability.
		t.Skipf("go binary not found on PATH; skipping stale-binary fixture test: %v", err)
	}
	return p
}

// toolSpec returns the registry spec for the named managed tool.
func toolSpec(t *testing.T, name string) ToolSpec {
	t.Helper()
	tools, err := ManagedTools(nil)
	if err != nil {
		t.Fatalf("ManagedTools: %v", err)
	}
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found in registry", name)
	return ToolSpec{}
}

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
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A failing downloader proves the fast path really skips: if any tool
	// were reinstalled, EnsureCriticalTools would surface a download error.
	mgr := &Manager{
		ToolsDir:   toolsDir,
		BinDir:     binDir,
		PythonDir:  pythonDir,
		Downloader: DownloadFunc(failingDownload),
		Installer:  &stubInstaller{},
		Logger:     slog.New(slog.DiscardHandler),
	}

	tools, err := ManagedTools(nil)
	if err != nil {
		t.Fatal(err)
	}

	// Healthy install state: every static binary exists and reports its
	// pinned version (fake-tool stand-ins, see TestMain), and .versions
	// matches the registry. The startup version probe must accept this
	// state without any download or install.
	versions := ToolVersions{}
	for _, tool := range tools {
		versions[tool.Name] = tool.Version
		if tool.Type == StaticBinary {
			copySelfBinary(t, mgr.binaryPath(tool.Name, tool.Type))
		} else {
			// PythonPackage tools only need their wrapper to exist for the
			// fast path to accept them (no probe outside the offline-relevant
			// static-binary check).
			if err := os.WriteFile(mgr.binaryPath(tool.Name, tool.Type), []byte("wrapper"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := WriteVersions(toolsDir, versions); err != nil {
		t.Fatal(err)
	}
	setFakeToolVersion(t, "uv", toolSpec(t, "uv").Version)
	setFakeToolVersion(t, "rg", toolSpec(t, "rg").Version)

	// The offline pass must fully accept an already-healthy install: no
	// network, no installs — just probes.
	statuses, err := mgr.EnsureCriticalTools(context.Background(), EnsureOptions{AllowNetwork: false})
	if err != nil {
		t.Fatalf("EnsureCriticalTools failed: %v", err)
	}
	for _, s := range statuses {
		if !s.Ready || s.Installed || s.Err != nil {
			t.Errorf("tool %q: got Ready=%v Installed=%v Err=%v, want Ready=true Installed=false Err=nil",
				s.Tool.Name, s.Ready, s.Installed, s.Err)
		}
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
	// stubs (the stubs write fake-tool stand-in binaries whose --version
	// output must satisfy the post-install verification, so their versions
	// are pinned via the environment — see TestMain).
	if err := WriteVersions(toolsDir, ToolVersions{"markitdown": "0.0.0"}); err != nil {
		t.Fatal(err)
	}
	setFakeToolVersion(t, "uv", toolSpec(t, "uv").Version)
	setFakeToolVersion(t, "rg", toolSpec(t, "rg").Version)

	if _, err := mgr.EnsureCriticalTools(context.Background(), EnsureOptions{AllowNetwork: true}); err != nil {
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

// TestManager_EnsureCriticalTools_StaticVersionBumpReplacesBinary reproduces
// the incident: a .versions entry from an older release alongside a
// still-present old binary. EnsureCriticalTools must remove the stale binary
// before reinstalling — the installer short-circuits on an existing
// destination, so without removal the old binary survives the "upgrade" while
// .versions already records the new version. Against pre-fix code (no
// stale-binary removal) this test fails: the destination survives untouched
// and still reports the wrong version.
func TestManager_EnsureCriticalTools_StaticVersionBumpReplacesBinary(t *testing.T) {
	toolsDir, binDir, pythonDir := makeTestDirs(t)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mgr := &Manager{
		ToolsDir:   toolsDir,
		BinDir:     binDir,
		PythonDir:  pythonDir,
		Downloader: DownloadFunc(stubDownload),
		Installer:  &shortCircuitStaticInstaller{},
		Logger:     slog.New(slog.DiscardHandler),
	}

	rg := toolSpec(t, "rg")
	uv := toolSpec(t, "uv")

	// The stale binary: a real executable whose --version output provably
	// lacks rg's pinned version (go --version exits with a flag error).
	rgPath := mgr.binaryPath(rg.Name, StaticBinary)
	if err := copyFile(goBinaryPath(t), rgPath, 0o755); err != nil {
		t.Fatal(err)
	}

	// .versions still records the previous release.
	if err := WriteVersions(toolsDir, ToolVersions{rg.Name: "14.1.0"}); err != nil {
		t.Fatal(err)
	}

	// Freshly installed binaries must report their pinned versions.
	setFakeToolVersion(t, uv.Name, uv.Version)
	setFakeToolVersion(t, rg.Name, rg.Version)

	if _, err := mgr.EnsureCriticalTools(context.Background(), EnsureOptions{AllowNetwork: true}); err != nil {
		t.Fatalf("EnsureCriticalTools failed: %v", err)
	}

	// The binary at rg's path must no longer be the stale one: it must now
	// report the pinned version.
	out := runVersion(t, rgPath)
	if !strings.Contains(out, rg.Version) {
		t.Errorf("binary at %s still reports stale content (output %q); version bump did not replace it", rgPath, out)
	}
	if versions, readErr := ReadVersions(toolsDir); readErr != nil || versions[rg.Name] != rg.Version {
		t.Errorf(".versions rg = %q (read error: %v), want %q", versions[rg.Name], readErr, rg.Version)
	}
}

// TestManager_EnsureCriticalTools_FastPathStaleBinaryReinstalls verifies the
// fast-path half of the fix: when .versions claims the pinned version but the
// binary on disk reports something else, EnsureCriticalTools must warn and
// fall through to a reinstall instead of trusting the version file — a stale
// .versions entry must never mask an outdated binary.
func TestManager_EnsureCriticalTools_FastPathStaleBinaryReinstalls(t *testing.T) {
	toolsDir, binDir, pythonDir := makeTestDirs(t)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	installer := &shortCircuitStaticInstaller{}
	mgr := &Manager{
		ToolsDir:   toolsDir,
		BinDir:     binDir,
		PythonDir:  pythonDir,
		Downloader: DownloadFunc(stubDownload),
		Installer:  installer,
		Logger:     slog.New(slog.DiscardHandler),
	}

	uv := toolSpec(t, "uv")
	rg := toolSpec(t, "rg")

	// Masked state: both static binaries exist but are stale (go toolchain
	// stand-ins whose --version output lacks any managed tool version),
	// while .versions already claims the pinned versions.
	for _, name := range []string{uv.Name, rg.Name} {
		if err := copyFile(goBinaryPath(t), mgr.binaryPath(name, StaticBinary), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tools, err := ManagedTools(nil)
	if err != nil {
		t.Fatal(err)
	}
	versions := ToolVersions{}
	for _, tool := range tools {
		versions[tool.Name] = tool.Version
	}
	if err := WriteVersions(toolsDir, versions); err != nil {
		t.Fatal(err)
	}

	setFakeToolVersion(t, uv.Name, uv.Version)
	setFakeToolVersion(t, rg.Name, rg.Version)

	if _, err := mgr.EnsureCriticalTools(context.Background(), EnsureOptions{AllowNetwork: true}); err != nil {
		t.Fatalf("EnsureCriticalTools failed: %v", err)
	}

	if installer.installCalls != 2 {
		t.Errorf("expected 2 reinstalls for stale binaries, got %d (skips: %d)", installer.installCalls, installer.skipCalls)
	}
	if installer.skipCalls != 0 {
		t.Errorf("expected no destination-exists short-circuits (stale binaries must be removed first), got %d", installer.skipCalls)
	}
	// Both binaries must now report their pinned versions.
	for _, tc := range []struct {
		name string
		spec ToolSpec
	}{{uv.Name, uv}, {rg.Name, rg}} {
		binPath := mgr.binaryPath(tc.name, StaticBinary)
		out := runVersion(t, binPath)
		if !strings.Contains(out, tc.spec.Version) {
			t.Errorf("binary at %s reports %q after reinstall; want version %q", binPath, out, tc.spec.Version)
		}
	}
}

// TestManager_InstallOne_PostInstallVerification covers the fail-closed
// post-install check: when the freshly installed binary does not report the
// pinned version, the tool is reported not Ready, the broken binary is
// removed from disk, and the .versions file stays untouched for the failed
// tool so the next startup retries.
func TestManager_InstallOne_PostInstallVerification(t *testing.T) {
	toolsDir, binDir, pythonDir := makeTestDirs(t)
	mgr := &Manager{
		ToolsDir:   toolsDir,
		BinDir:     binDir,
		PythonDir:  pythonDir,
		Downloader: DownloadFunc(stubDownload),
		Installer:  &shortCircuitStaticInstaller{},
		Logger:     slog.New(slog.DiscardHandler),
	}

	uv := toolSpec(t, "uv")
	rg := toolSpec(t, "rg")

	// The installer writes fake-tool stand-ins; rg's stand-in reports a
	// WRONG version, uv's the pinned one.
	setFakeToolVersion(t, uv.Name, uv.Version)
	setFakeToolVersion(t, rg.Name, "0.0.9")

	statuses, err := mgr.EnsureCriticalTools(context.Background(), EnsureOptions{AllowNetwork: true})
	if err != nil {
		t.Fatalf("EnsureCriticalTools returned structural error: %v", err)
	}

	var rgStatus *ToolStatus
	for i := range statuses {
		if statuses[i].Tool.Name == rg.Name {
			rgStatus = &statuses[i]
		}
	}
	if rgStatus == nil {
		t.Fatal("rg status missing from reconciliation results")
	}
	if rgStatus.Ready {
		t.Fatal("rg must not be Ready when the installed binary reports a wrong version")
	}
	if rgStatus.Err == nil || !strings.Contains(rgStatus.Err.Error(), rg.Version) {
		t.Errorf("status error should mention the expected version %q, got: %v", rg.Version, rgStatus.Err)
	}

	// The binary that failed verification must be removed from disk, not
	// left behind to masquerade as a working rg.
	rgPath := mgr.binaryPath(rg.Name, rg.Type)
	if _, statErr := os.Stat(rgPath); statErr == nil {
		t.Error("broken rg binary still on disk after failed post-install verification; it must be removed")
	}

	// Fail-closed: rg must NOT be recorded as installed...
	versions, readErr := ReadVersions(toolsDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got, ok := versions[rg.Name]; ok {
		t.Errorf("rg recorded as installed with version %q despite failed verification; .versions must stay untouched on failure", got)
	}
	// ...while uv, which verified successfully before rg failed, is recorded.
	if got := versions[uv.Name]; got != uv.Version {
		t.Errorf("uv should be recorded as installed, got %q", got)
	}
}

// offlineCacheMissDownload models the production HTTPDownloader behaviour in
// DownloadCacheOnly mode when nothing usable is cached: a fast, deterministic
// failure wrapping ErrCacheUnavailable, with no network I/O.
func offlineCacheMissDownload(ctx context.Context, tool ToolSpec, cacheDir string, mode DownloadMode, progress func(int64, int64)) (*DownloadResult, error) {
	return nil, fmt.Errorf("tool %q: %w", tool.Name, ErrCacheUnavailable)
}

// cacheAwareDownload models the production HTTPDownloader cache contract: in
// CacheOnly mode a cached archive file satisfies the request; without one it
// fails exactly like the production downloader.
func cacheAwareDownload(ctx context.Context, tool ToolSpec, cacheDir string, mode DownloadMode, progress func(int64, int64)) (*DownloadResult, error) {
	archivePath := filepath.Join(cacheDir, tool.ArchiveName)
	if _, err := os.Stat(archivePath); err != nil {
		return nil, fmt.Errorf("tool %q: %w", tool.Name, ErrCacheUnavailable)
	}
	return &DownloadResult{ToolName: tool.Name, ArchivePath: archivePath}, nil
}

// writeCachedArchives drops a (fake) cached archive for every static-binary
// spec so cache-aware stub downloads succeed offline.
func writeCachedArchives(t *testing.T, cacheDir string, specs ...ToolSpec) {
	t.Helper()
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		if spec.Type != StaticBinary {
			continue
		}
		path := filepath.Join(cacheDir, spec.ArchiveName)
		if err := os.WriteFile(path, []byte("cached archive"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestManager_EnsureCriticalTools_OfflineInstallsFromCache verifies the core
// offline guarantee: when the network is unavailable but a valid cached
// archive exists, a version bump still completes during the synchronous
// startup pass — stale binaries are replaced and .versions is updated with
// zero network access.
func TestManager_EnsureCriticalTools_OfflineInstallsFromCache(t *testing.T) {
	toolsDir, binDir, pythonDir := makeTestDirs(t)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mgr := &Manager{
		ToolsDir:   toolsDir,
		BinDir:     binDir,
		PythonDir:  pythonDir,
		Downloader: DownloadFunc(cacheAwareDownload),
		Installer:  &shortCircuitStaticInstaller{},
		Logger:     slog.New(slog.DiscardHandler),
	}

	uv := toolSpec(t, "uv")
	rg := toolSpec(t, "rg")

	// Version bump in progress: stale binaries on disk, .versions records
	// the previous release, and a valid cached archive sits in the cache.
	for _, name := range []string{uv.Name, rg.Name} {
		if err := copyFile(goBinaryPath(t), mgr.binaryPath(name, StaticBinary), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteVersions(toolsDir, ToolVersions{uv.Name: "previous", rg.Name: "previous"}); err != nil {
		t.Fatal(err)
	}
	writeCachedArchives(t, filepath.Join(toolsDir, ".cache"), uv, rg)

	setFakeToolVersion(t, uv.Name, uv.Version)
	setFakeToolVersion(t, rg.Name, rg.Version)

	statuses, err := mgr.EnsureCriticalTools(context.Background(), EnsureOptions{AllowNetwork: false})
	if err != nil {
		t.Fatalf("EnsureCriticalTools failed: %v", err)
	}

	versions, readErr := ReadVersions(toolsDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, spec := range []ToolSpec{uv, rg} {
		status := findStatus(t, statuses, spec.Name)
		if !status.Ready || !status.Installed {
			t.Errorf("%s: Ready=%v Installed=%v Err=%v; want a completed offline install from cache",
				spec.Name, status.Ready, status.Installed, status.Err)
		}
		out := runVersion(t, mgr.binaryPath(spec.Name, spec.Type))
		if !strings.Contains(out, spec.Version) {
			t.Errorf("%s reports %q after offline reinstall; want version %q", spec.Name, out, spec.Version)
		}
		if got := versions[spec.Name]; got != spec.Version {
			t.Errorf(".versions %s = %q, want %q", spec.Name, got, spec.Version)
		}
	}
}

// TestManager_EnsureCriticalTools_OfflineKeepsStaleBinaryWhenNoCache verifies
// the no-regression half of the offline contract: when the fast-path probe
// flags a stale binary but no cached archive exists, the failed reinstall
// must leave the existing binary on disk and .versions untouched — an
// offline failure must never leave the machine with less than it had.
func TestManager_EnsureCriticalTools_OfflineKeepsStaleBinaryWhenNoCache(t *testing.T) {
	toolsDir, binDir, pythonDir := makeTestDirs(t)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mgr := &Manager{
		ToolsDir:   toolsDir,
		BinDir:     binDir,
		PythonDir:  pythonDir,
		Downloader: DownloadFunc(offlineCacheMissDownload),
		Installer:  &shortCircuitStaticInstaller{},
		Logger:     slog.New(slog.DiscardHandler),
	}

	uv := toolSpec(t, "uv")
	rg := toolSpec(t, "rg")

	// Masked stale state: binaries exist but report the wrong version while
	// .versions already claims the pinned versions.
	for _, spec := range []ToolSpec{uv, rg} {
		if err := copyFile(goBinaryPath(t), mgr.binaryPath(spec.Name, StaticBinary), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteVersions(toolsDir, ToolVersions{uv.Name: uv.Version, rg.Name: rg.Version}); err != nil {
		t.Fatal(err)
	}

	setFakeToolVersion(t, uv.Name, uv.Version)
	setFakeToolVersion(t, rg.Name, rg.Version)

	statuses, err := mgr.EnsureCriticalTools(context.Background(), EnsureOptions{AllowNetwork: false})
	if err != nil {
		t.Fatalf("EnsureCriticalTools returned structural error: %v", err)
	}

	for _, spec := range []ToolSpec{uv, rg} {
		status := findStatus(t, statuses, spec.Name)
		if status.Ready {
			t.Errorf("%s must not be Ready when the reinstall could not secure replacement bytes", spec.Name)
		}
		if status.Err == nil || !errors.Is(status.Err, ErrCacheUnavailable) {
			t.Errorf("%s status error = %v; want an error wrapping ErrCacheUnavailable", spec.Name, status.Err)
		}
		// The old binary must have survived the failed reinstall.
		if _, statErr := os.Stat(mgr.binaryPath(spec.Name, spec.Type)); statErr != nil {
			t.Errorf("%s binary was removed by a failed offline reinstall: %v", spec.Name, statErr)
		}
	}

	// .versions must be untouched — nothing was actually installed.
	versions, readErr := ReadVersions(toolsDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := versions[uv.Name]; got != uv.Version {
		t.Errorf(".versions uv = %q after failed offline pass, want unchanged %q", got, uv.Version)
	}
	if got := versions[rg.Name]; got != rg.Version {
		t.Errorf(".versions rg = %q after failed offline pass, want unchanged %q", got, rg.Version)
	}
}

// TestManager_EnsureCriticalTools_PerToolIsolation verifies that one tool's
// install failure never blocks the remaining tools: with uv's downloads
// failing and rg satisfiable from cache, the offline pass must still install
// rg and record it — a failing first tool in dependency order must not abort
// the reconciliation loop.
func TestManager_EnsureCriticalTools_PerToolIsolation(t *testing.T) {
	toolsDir, binDir, pythonDir := makeTestDirs(t)

	downloader := DownloadFunc(func(ctx context.Context, tool ToolSpec, cacheDir string, mode DownloadMode, progress func(int64, int64)) (*DownloadResult, error) {
		if tool.Name == "uv" {
			return nil, errors.New("network unavailable")
		}
		return cacheAwareDownload(ctx, tool, cacheDir, mode, progress)
	})
	mgr := &Manager{
		ToolsDir:   toolsDir,
		BinDir:     binDir,
		PythonDir:  pythonDir,
		Downloader: downloader,
		Installer:  &shortCircuitStaticInstaller{},
		Logger:     slog.New(slog.DiscardHandler),
	}

	uv := toolSpec(t, "uv")
	rg := toolSpec(t, "rg")
	markitdown := toolSpec(t, "markitdown")

	writeCachedArchives(t, filepath.Join(toolsDir, ".cache"), rg)
	setFakeToolVersion(t, uv.Name, uv.Version)
	setFakeToolVersion(t, rg.Name, rg.Version)

	statuses, err := mgr.EnsureCriticalTools(context.Background(), EnsureOptions{AllowNetwork: false})
	if err != nil {
		t.Fatalf("EnsureCriticalTools returned structural error: %v", err)
	}

	uvStatus := findStatus(t, statuses, uv.Name)
	if uvStatus.Ready || uvStatus.Err == nil {
		t.Errorf("uv: Ready=%v Err=%v; want failed status", uvStatus.Ready, uvStatus.Err)
	}
	rgStatus := findStatus(t, statuses, rg.Name)
	if !rgStatus.Ready || !rgStatus.Installed {
		t.Errorf("rg: Ready=%v Installed=%v Err=%v; want installed despite uv failing first",
			rgStatus.Ready, rgStatus.Installed, rgStatus.Err)
	}
	mdStatus := findStatus(t, statuses, markitdown.Name)
	if mdStatus.Ready || mdStatus.Err == nil || !errors.Is(mdStatus.Err, errDeferredToBackground) {
		t.Errorf("markitdown: Ready=%v Err=%v; want deferred status", mdStatus.Ready, mdStatus.Err)
	}

	versions, readErr := ReadVersions(toolsDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := versions[rg.Name]; got != rg.Version {
		t.Errorf(".versions rg = %q, want %q (installed tool must be recorded)", got, rg.Version)
	}
	for _, name := range []string{uv.Name, markitdown.Name} {
		if got, ok := versions[name]; ok {
			t.Errorf(".versions %s = %q; failed/deferred tools must not be recorded", name, got)
		}
	}
}

// TestManager_EnsureCriticalTools_OfflineDefersPython verifies that a
// Python-package tool that needs installation is deferred (not attempted,
// not destroyed) by the offline pass: the uv bootstrap requires the network,
// and a missing wrapper/venv must be left alone rather than half-rebuilt.
func TestManager_EnsureCriticalTools_OfflineDefersPython(t *testing.T) {
	toolsDir, binDir, pythonDir := makeTestDirs(t)

	mgr := &Manager{
		ToolsDir:   toolsDir,
		BinDir:     binDir,
		PythonDir:  pythonDir,
		Downloader: DownloadFunc(stubDownload),
		Installer:  &stubInstaller{},
		Logger:     slog.New(slog.DiscardHandler),
	}

	markitdown := toolSpec(t, "markitdown")
	// Fresh machine: no .versions at all — every tool needs installing.
	setFakeToolVersion(t, "uv", toolSpec(t, "uv").Version)
	setFakeToolVersion(t, "rg", toolSpec(t, "rg").Version)

	statuses, err := mgr.EnsureCriticalTools(context.Background(), EnsureOptions{AllowNetwork: false})
	if err != nil {
		t.Fatalf("EnsureCriticalTools returned structural error: %v", err)
	}

	status := findStatus(t, statuses, markitdown.Name)
	if status.Ready {
		t.Fatal("markitdown must not be Ready in the offline pass")
	}
	if status.Err == nil || !errors.Is(status.Err, errDeferredToBackground) {
		t.Fatalf("markitdown status error = %v; want errDeferredToBackground", status.Err)
	}
	// The venv must not have been touched or created by the offline pass.
	if _, statErr := os.Stat(filepath.Join(pythonDir, "venv")); !os.IsNotExist(statErr) {
		t.Errorf("offline pass created or removed the venv: %v", statErr)
	}
}

// findStatus returns the reconciliation status for the named tool.
func findStatus(t *testing.T, statuses []ToolStatus, name string) ToolStatus {
	t.Helper()
	for _, s := range statuses {
		if s.Tool.Name == name {
			return s
		}
	}
	t.Fatalf("no reconciliation status for tool %q", name)
	return ToolStatus{}
}

// runVersion executes the binary at path with --version and returns its
// combined output. Deliberately independent of the manager's own probe helper
// so the regression tests exercise the observable on-disk result and compile
// against both pre-fix and post-fix code.
func runVersion(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), path, "--version").CombinedOutput() //nolint:gosec // test fixture path
	if err != nil {
		t.Logf("runVersion(%s): %v (output: %s)", path, err, out)
	}
	return string(out)
}

// TestTruncForLog covers the log-echo truncation helper: short input passes
// through untouched, long input is cut at the cap plus the marker, and a cut
// landing mid-rune backs up to a UTF-8 boundary so logs stay valid UTF-8.
func TestTruncForLog(t *testing.T) {
	if got := truncForLog("ok"); got != "ok" {
		t.Errorf("truncForLog(%q) = %q, want unchanged", "ok", got)
	}

	longASCII := strings.Repeat("a", maxProbeLogBytes+10)
	got := truncForLog(longASCII)
	wantLen := maxProbeLogBytes + len("…(truncated)")
	if len(got) != wantLen || !strings.HasSuffix(got, "…(truncated)") {
		t.Errorf("truncForLog(long ASCII) = %d bytes, suffix %q; want %d bytes with %q suffix",
			len(got), got[len(got)-12:], wantLen, "…(truncated)")
	}

	// The cut lands in the middle of the 3-byte '★' rune; the result must
	// back up to the rune boundary instead of emitting invalid UTF-8.
	midRune := strings.Repeat("a", maxProbeLogBytes-1) + "★" + strings.Repeat("b", 10)
	if got := truncForLog(midRune); !utf8.ValidString(got) {
		t.Errorf("truncForLog(mid-rune cut) produced invalid UTF-8: %q", got)
	}
}

// ── Stubs for testing ──────────────────────────────────────────────────────

func stubDownload(ctx context.Context, tool ToolSpec, cacheDir string, mode DownloadMode, progress func(int64, int64)) (*DownloadResult, error) {
	return &DownloadResult{ToolName: tool.Name, ArchivePath: "/fake/path", Downloaded: false}, nil
}

func failingDownload(ctx context.Context, tool ToolSpec, cacheDir string, mode DownloadMode, progress func(int64, int64)) (*DownloadResult, error) {
	return nil, errors.New("network unavailable")
}

type stubInstaller struct{}

// InstallStaticBinary writes a real fake-tool stand-in binary (see TestMain)
// so post-install version verification can execute it; the version it reports
// comes from TOOLMANAGER_TEST_VERSION_<NAME>, pinned by the test.
func (s *stubInstaller) InstallStaticBinary(archivePath string, tool ToolSpec, binDir string) (*InstallResult, error) {
	dst := filepath.Join(binDir, tool.BinName)
	if runtime.GOOS == "windows" {
		dst += ".exe"
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return nil, err
	}
	if err := copySelfBinaryTo(dst); err != nil {
		return nil, err
	}
	return &InstallResult{ToolName: tool.Name, BinPath: dst, Installed: true}, nil
}

func (s *stubInstaller) InstallPythonPackage(ctx context.Context, tool ToolSpec, toolsDir, binDir string) (*InstallResult, error) { //nolint:gocritic // must match Installer interface
	return &InstallResult{ToolName: tool.Name, BinPath: filepath.Join(binDir, tool.BinName), Installed: true}, nil
}

// shortCircuitStaticInstaller models the production FSInstaller behaviour
// that caused the incident: it refuses to overwrite an existing destination
// binary (Installed=false, file untouched). When the destination is absent it
// installs a fake-tool stand-in binary. Counters let tests assert whether a
// reinstall really happened.
type shortCircuitStaticInstaller struct {
	installCalls int // InstallStaticBinary calls that wrote a binary
	skipCalls    int // InstallStaticBinary calls short-circuited by dst-exists
}

func (s *shortCircuitStaticInstaller) InstallStaticBinary(archivePath string, tool ToolSpec, binDir string) (*InstallResult, error) {
	dst := filepath.Join(binDir, tool.BinName)
	if runtime.GOOS == "windows" {
		dst += ".exe"
	}
	if _, err := os.Stat(dst); err == nil {
		s.skipCalls++
		return &InstallResult{ToolName: tool.Name, BinPath: dst, Installed: false}, nil
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return nil, err
	}
	if err := copySelfBinaryTo(dst); err != nil {
		return nil, err
	}
	s.installCalls++
	return &InstallResult{ToolName: tool.Name, BinPath: dst, Installed: true}, nil
}

func (s *shortCircuitStaticInstaller) InstallPythonPackage(ctx context.Context, tool ToolSpec, toolsDir, binDir string) (*InstallResult, error) { //nolint:gocritic // must match Installer interface
	return &InstallResult{ToolName: tool.Name, BinPath: filepath.Join(binDir, tool.BinName), Installed: true}, nil
}

// TestVenvPythonPath probes the exported venv-interpreter lookup against the
// managed layout this package owns: <toolsDir>/python/venv/{bin/python3 on
// Unix, Scripts/python.exe on Windows}. Missing venv must return "".
func TestVenvPythonPath(t *testing.T) {
	toolsDir, _, _ := makeTestDirs(t)

	// Missing venv → empty path.
	if got := VenvPythonPath(toolsDir); got != "" {
		t.Errorf("VenvPythonPath(missing venv) = %q, want \"\"", got)
	}

	// Platform-correct interpreter inside the venv.
	var rel string
	if runtime.GOOS == "windows" {
		rel = filepath.Join("python", "venv", "Scripts", "python.exe")
	} else {
		rel = filepath.Join("python", "venv", "bin", "python3")
	}
	interp := filepath.Join(toolsDir, rel)
	if err := os.MkdirAll(filepath.Dir(interp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(interp, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := VenvPythonPath(toolsDir)
	if got == "" {
		t.Fatal("VenvPythonPath returned \"\" for an existing venv interpreter")
	}
	if filepath.Clean(got) != filepath.Clean(interp) {
		t.Errorf("VenvPythonPath = %q, want %q", got, interp)
	}
}
