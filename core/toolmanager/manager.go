package toolmanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/v0lka/c0wrk/internal/sysproc"
	"github.com/v0lka/sp4rk/safeio"
)

// ProgressCallback reports install progress to the UI: toolName, stage
// ("download", "extract", "python_bootstrap"), download bytes (0/0 if N/A).
type ProgressCallback func(toolName, stage string, bytesDone, bytesTotal int64)

// Manager orchestrates download, installation, and version-tracking of the
// managed tools; the desktop startup layer is the only consumer, sequencing
// the two EnsureCriticalTools passes — never overlapped.
// See specs/domains/tool-manager.md and specs/decisions/100-blocking-first-run-tool-install.md.
type Manager struct {
	ToolsDir  string // e.g. ~/.c0wrk/tools/
	BinDir    string // e.g. ~/.c0wrk/tools/bin/
	PythonDir string // e.g. ~/.c0wrk/tools/python/

	Downloader       Downloader
	Installer        Installer
	Logger           *slog.Logger
	ProgressCallback ProgressCallback
}

// ManagerConfig holds optional configuration for NewManager.
type ManagerConfig struct {
	// HTTPClient is used for archive downloads; nil → a 5-minute-timeout client.
	HTTPClient *http.Client
	// ProgressCallback is called during tool installation.
	ProgressCallback ProgressCallback
}

// NewManager builds a Manager with the production downloader/installer; the
// dirs must come from the canonical config path helpers; nil logger → discard.
func NewManager(toolsDir, binDir, pythonDir string, logger *slog.Logger, cfg ManagerConfig) *Manager {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Manager{
		ToolsDir:         toolsDir,
		BinDir:           binDir,
		PythonDir:        pythonDir,
		Downloader:       NewHTTPDownloader(client),
		Installer:        NewFSInstaller(),
		Logger:           logger,
		ProgressCallback: cfg.ProgressCallback,
	}
}

// EnsureOptions controls how EnsureCriticalTools acquires missing tools.
type EnsureOptions struct {
	// AllowNetwork=false restricts reconciliation to local disk — cached,
	// SHA256-verified archives only; network-needing tools are deferred
	// (errNeedsNetwork). See specs/domains/tool-manager.md.
	AllowNetwork bool
}

// ToolStatus is the per-tool outcome of one EnsureCriticalTools run.
type ToolStatus struct {
	Tool      ToolSpec
	Ready     bool  // installed and verified (before or during this run)
	Installed bool  // newly installed or updated during this run
	Err       error // why the tool is not Ready; nil iff Ready
}

// errNeedsNetwork marks a tool skipped by an offline (AllowNetwork=false)
// run and deferred to the startup layer's network pass.
var errNeedsNetwork = errors.New("requires network access; deferred to the network reconciliation pass")

// EnsureCriticalTools reconciles the managed registry against the local
// install: per-tool failures are isolated in the returned statuses, structural
// failures return the error, and each success persists to .versions at once so
// a partial run resumes. Startup policy (sequencing, fatality) is the caller's.
// See specs/domains/tool-manager.md.
func (m *Manager) EnsureCriticalTools(ctx context.Context, opts EnsureOptions) ([]ToolStatus, error) {
	cacheDir := filepath.Join(m.ToolsDir, ".cache")

	// Dirs first: checkDiskSpace needs an existing path (first-run ENOENT
	// silently skips the check); the parent exists after Phase 1. MkdirAllReal
	// refuses a dangling or swapped-in symlink at the fixed .cache/python/bin
	// paths so downloads, extraction and venv writes stay inside ~/.c0wrk
	// (a pre-existing operator symlink resolves as intent).
	for _, dir := range []string{m.BinDir, cacheDir, m.PythonDir} {
		if err := safeio.MkdirAllReal(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	if err := checkDiskSpace(m.ToolsDir, 200*1024*1024, m.Logger); err != nil {
		return nil, fmt.Errorf("insufficient disk space: %w", err)
	}

	// Read installed versions.
	versions, err := ReadVersions(m.ToolsDir)
	if err != nil {
		m.Logger.Warn("failed to read versions file, treating as fresh install", "error", err)
		versions = ToolVersions{}
	}

	tools, err := ManagedTools(m.Logger)
	if err != nil {
		return nil, fmt.Errorf("resolving tool registry: %w", err)
	}

	statuses := make([]ToolStatus, 0, len(tools))
	for _, tool := range tools {
		status := m.reconcileTool(ctx, tool, versions[tool.Name], cacheDir, opts)
		if status.Ready && status.Installed {
			versions[tool.Name] = tool.Version
			if writeErr := WriteVersions(m.ToolsDir, versions); writeErr != nil {
				m.Logger.Warn("failed to write versions file", "error", writeErr)
			}
		}
		statuses = append(statuses, status)
	}

	ready := 0
	for _, s := range statuses {
		if s.Ready {
			ready++
		}
	}
	m.Logger.Info("tool reconciliation complete", "ready", ready, "total", len(tools), "allow_network", opts.AllowNetwork)
	return statuses, nil
}

// reconcileTool brings one tool to its pinned version under the spec's
// reconciliation guarantees (forced reinstall on pin change, probe-backed
// staleness detection, secure-bytes-before-destroy).
// See specs/domains/tool-manager.md.
func (m *Manager) reconcileTool(ctx context.Context, tool ToolSpec, installed, cacheDir string, opts EnsureOptions) ToolStatus {
	status := ToolStatus{Tool: tool, Ready: true}

	if installed == tool.Version {
		// .versions can outlive the binary (manual deletion) — verify on disk.
		binPath := m.binaryPath(tool.Name, tool.Type)
		if _, statErr := os.Stat(binPath); statErr == nil {
			if tool.Type != StaticBinary || m.binaryVersionMatches(ctx, tool, binPath) {
				m.Logger.Debug("tool already up-to-date", "tool", tool.Name, "version", tool.Version)
				return status
			}
			// .versions can also mask a stale binary: a prior install short-
			// circuited by InstallStaticBinary's destination-exists check left
			// the old binary under the new version. A probe mismatch falls
			// through to a reinstall; the old binary stays until secured.
			m.Logger.Warn("fast-path version probe mismatch, reinstalling", "tool", tool.Name)
		} else {
			m.Logger.Warn("version file present but binary missing, re-installing", "tool", tool.Name)
		}
	}

	// The tool needs (re)installation.
	if installErr := m.installOne(ctx, tool, cacheDir, opts); installErr != nil {
		status.Ready = false
		status.Err = installErr
		m.Logger.Error("tool install failed; continuing with remaining tools",
			"tool", tool.Name, "error", installErr)
		return status
	}
	status.Installed = true
	return status
}

// NeedsInstall returns tools with a version mismatch or a missing binary —
// the startup layer's splash-screen decision input.
func (m *Manager) NeedsInstall() ([]ToolSpec, error) {
	versions, err := ReadVersions(m.ToolsDir)
	if err != nil {
		versions = ToolVersions{}
	}
	tools, err := ManagedTools(m.Logger)
	if err != nil {
		return nil, err
	}
	var needed []ToolSpec
	for _, tool := range tools {
		if versions[tool.Name] != tool.Version {
			needed = append(needed, tool)
			continue
		}
		binPath := m.binaryPath(tool.Name, tool.Type)
		if _, statErr := os.Stat(binPath); statErr != nil {
			needed = append(needed, tool)
		}
	}
	return needed, nil
}

// GetToolPath returns the managed binary path, or "" if not managed or not
// installed. typ must match the spec's ToolType (Windows extension).
func (m *Manager) GetToolPath(name string, typ ToolType) string {
	p := m.binaryPath(name, typ)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// binaryPath is the expected binary path (.cmd wrapper for PythonPackage on
// Windows, .exe for StaticBinary).
func (m *Manager) binaryPath(name string, typ ToolType) string {
	p := filepath.Join(m.BinDir, name)
	if runtime.GOOS == "windows" {
		if typ == PythonPackage {
			p += ".cmd"
		} else {
			p += ".exe"
		}
	}
	return p
}

// PrependToPATH returns the bin directory path for PATH prepending.
func (m *Manager) PrependToPATH() string {
	return m.BinDir
}

func (m *Manager) reportProgress(toolName, stage string, bytesDone, bytesTotal int64) {
	if m.ProgressCallback != nil {
		m.ProgressCallback(toolName, stage, bytesDone, bytesTotal)
	}
}

func (m *Manager) installOne(ctx context.Context, tool ToolSpec, cacheDir string, opts EnsureOptions) error {
	switch tool.Type {
	case StaticBinary:
		mode := DownloadOnline
		if !opts.AllowNetwork {
			mode = DownloadCacheOnly
		}
		if err := m.installStatic(ctx, tool, cacheDir, mode); err != nil {
			return err
		}
		// Fail-closed post-install probe: a binary that can't report the pinned
		// version is removed, .versions untouched — the next run retries.
		binPath := m.binaryPath(tool.Name, tool.Type)
		out, probeErr := m.probeBinaryVersion(ctx, binPath)
		if probeErr == nil && !versionInOutput(out, tool.Version) {
			probeErr = fmt.Errorf("installed binary does not report expected version %q", tool.Version)
		}
		if probeErr != nil {
			m.removeStaleStaticBinary(tool) // never keep a binary that failed verification
			return fmt.Errorf("tool %q: post-install version probe failed: %w (output: %s)",
				tool.Name, probeErr, truncForLog(out))
		}
		return nil
	case PythonPackage:
		if !opts.AllowNetwork {
			// Nothing local can satisfy a missing/outdated package — uv needs the network.
			return fmt.Errorf("tool %q: %w", tool.Name, errNeedsNetwork)
		}
		return m.installPython(ctx, tool)
	default:
		return fmt.Errorf("unknown tool type: %s", tool.Type)
	}
}

// versionProbeTimeout bounds one --version probe: real calls finish in
// milliseconds; the ceiling only stops a wedged binary from stalling startup.
const versionProbeTimeout = 10 * time.Second

// probeBinaryVersion runs binPath with --version and returns its combined
// output: timeout-bounded, console hidden on Windows; on failure the
// collected output still comes back alongside the error.
func (m *Manager) probeBinaryVersion(ctx context.Context, binPath string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, binPath, "--version")
	sysproc.HideConsole(cmd) // avoid flashing console windows on Windows (GUI app)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("running %s --version: %w", binPath, err)
	}
	return string(out), nil
}

// versionInOutput is deliberately substring containment ("15.2.0" matches
// "15.2.00") — the safe direction: bytes are SHA256-pinned, so the probe only
// catches a stale/wrong binary that survived an install, not version schemes.
func versionInOutput(output, want string) bool {
	return strings.Contains(output, want)
}

// maxProbeLogBytes caps probe output echoed into logs/errors so a broken
// binary that dumps garbage cannot flood them.
const maxProbeLogBytes = 512

// truncForLog caps output for logs/errors; the cut backs up to a rune
// boundary so the result stays valid UTF-8.
func truncForLog(s string) string {
	if len(s) <= maxProbeLogBytes {
		return s
	}
	cut := maxProbeLogBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…(truncated)"
}

// binaryVersionMatches probes binPath for the version pinned in the tool
// spec. Probe errors count as a mismatch (fail-closed): the caller answers
// with a reinstall — a cheap cached re-extraction — never with an error.
func (m *Manager) binaryVersionMatches(ctx context.Context, tool ToolSpec, binPath string) bool {
	out, err := m.probeBinaryVersion(ctx, binPath)
	if err != nil {
		m.Logger.Warn("binary version probe failed, treating as out-of-date",
			"tool", tool.Name, "error", err, "output", truncForLog(out))
		return false
	}
	if !versionInOutput(out, tool.Version) {
		m.Logger.Warn("binary reports unexpected version, treating as out-of-date",
			"tool", tool.Name, "want", tool.Version, "output", truncForLog(out))
		return false
	}
	return true
}

// installStatic secures the archive (download or verified cache hit), then
// removes the stale destination binary and extracts — secure-bytes-before-
// destroy. See specs/domains/tool-manager.md.
func (m *Manager) installStatic(ctx context.Context, tool ToolSpec, cacheDir string, mode DownloadMode) error {
	downloadProgress := func(done, total int64) {
		m.reportProgress(tool.Name, "download", done, total)
	}
	result, err := m.Downloader.Download(ctx, tool, cacheDir, mode, downloadProgress)
	if err != nil && mode == DownloadOnline {
		// One retry for transient failures; a CacheOnly failure is
		// deterministic (nothing cached) — retrying would be noise.
		m.Logger.Warn("download failed, retrying", "tool", tool.Name, "error", err)
		result, err = m.Downloader.Download(ctx, tool, cacheDir, mode, downloadProgress)
	}
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	m.Logger.Debug("archive ready", "tool", tool.Name, "downloaded", result.Downloaded)

	// Bytes secured — clear the destination so the installer's exists
	// short-circuit cannot leave a stale binary under the recorded new version.
	m.removeStaleStaticBinary(tool)

	m.reportProgress(tool.Name, "extract", 0, 0)
	if _, err := m.Installer.InstallStaticBinary(result.ArchivePath, tool, m.BinDir); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	m.Logger.Debug("binary installed", "tool", tool.Name)
	m.cleanupOldArchives(tool, cacheDir)
	return nil
}

// cleanupOldArchives removes cached archives of past versions (tool-name
// prefix, not the current ArchiveName) so bumps don't accumulate.
func (m *Manager) cleanupOldArchives(tool ToolSpec, cacheDir string) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return // cache dir doesn't exist or is unreadable — nothing to clean
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == tool.ArchiveName {
			continue
		}
		if strings.HasPrefix(name, tool.Name+"-") || strings.HasPrefix(name, tool.Name+".") {
			path := filepath.Join(cacheDir, name)
			if err := os.Remove(path); err != nil {
				m.Logger.Debug("failed to remove old archive", "path", path, "error", err)
			}
		}
	}
}

// installPython removes the stale wrapper+venv immediately before the
// bootstrap (never earlier — a network failure must not strip a working old
// environment); the offline pass never reaches it. See specs/domains/tool-manager.md.
func (m *Manager) installPython(ctx context.Context, tool ToolSpec) error {
	m.reportProgress(tool.Name, "python_bootstrap", 0, 0)
	m.removeStalePythonEnv(tool)
	if _, err := m.Installer.InstallPythonPackage(ctx, tool, m.ToolsDir, m.BinDir); err != nil {
		return fmt.Errorf("python install: %w", err)
	}
	m.Logger.Debug("python package installed", "tool", tool.Name)
	return nil
}

// removeStalePythonEnv deletes the wrapper and venv so a version bump forces
// a full rebootstrap — a kept wrapper would ride InstallPythonPackage's
// existence short-circuit past the upgrade (a silent security gap). The
// uv-managed interpreter (python/install/) stays, reinstalled idempotently.
func (m *Manager) removeStalePythonEnv(tool ToolSpec) {
	wrapperPath := m.binaryPath(tool.Name, tool.Type)
	if err := os.Remove(wrapperPath); err != nil && !os.IsNotExist(err) {
		m.Logger.Warn("failed to remove stale wrapper", "tool", tool.Name, "path", wrapperPath, "error", err)
	}
	venvDir := filepath.Join(m.PythonDir, "venv")
	if err := os.RemoveAll(venvDir); err != nil {
		m.Logger.Warn("failed to remove stale venv", "path", venvDir, "error", err)
	}
}

// removeStaleStaticBinary deletes the installed binary — only once the
// replacement bytes are secured or the post-install probe has failed; never
// ahead of the download, so an offline cache miss keeps the working binary.
func (m *Manager) removeStaleStaticBinary(tool ToolSpec) {
	binPath := m.binaryPath(tool.Name, tool.Type)
	if err := os.Remove(binPath); err != nil && !os.IsNotExist(err) {
		m.Logger.Warn("failed to remove stale binary", "tool", tool.Name, "path", binPath, "error", err)
	}
}

// VenvPythonPath returns the managed venv interpreter or "" when absent —
// the canonical access for other layers' vision-assisted conversion; pure
// probing, never installs. See specs/domains/tool-manager.md.
func VenvPythonPath(toolsDir string) string {
	venvDir := filepath.Join(toolsDir, "python", "venv")
	return findPythonInDir(venvDir, "", runtime.GOOS)
}
