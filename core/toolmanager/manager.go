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
)

// ProgressCallback is called during tool installation to report progress
// to the UI. toolName is the tool being installed, stage describes the
// current phase ("download", "extract", "python_bootstrap"), bytesDone
// and bytesTotal are the download progress (0/0 when not applicable).
type ProgressCallback func(toolName, stage string, bytesDone, bytesTotal int64)

// Manager orchestrates the download, installation, and version-tracking of
// managed external tools. It is the single public API consumed by the desktop
// startup layer.
//
// Manager is not safe for concurrent use. The startup layer runs a first
// EnsureCriticalTools pass with AllowNetwork=false during synchronous startup
// and, when tools are left not Ready, a second pass with AllowNetwork=true
// from a background goroutine. The two runs must be sequenced (the offline
// pass completes before the background pass starts) — they must never overlap.
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
	// HTTPClient is used for tool archive downloads. If nil, a client with
	// a 5-minute timeout is created.
	HTTPClient *http.Client
	// ProgressCallback is called during tool installation.
	ProgressCallback ProgressCallback
}

// NewManager creates a Manager with production HTTP downloader and filesystem
// installer. toolsDir, binDir, and pythonDir should come from the canonical
// config path helpers (config.ToolsDir, config.ToolsBinDir, config.ToolsPythonDir)
// to ensure a single source of truth for directory layout. If logger is nil,
// a discard logger is used.
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
	// AllowNetwork permits reaching the network (archive downloads, Python
	// bootstrap via uv). When false, reconciliation is strictly local:
	// static binaries install only from an already-cached, SHA256-verified
	// archive, and tools that would need the network are reported as
	// deferred. This is what keeps app startup fully functional offline —
	// a hard product requirement, not a tradeoff.
	AllowNetwork bool
}

// ToolStatus is the per-tool outcome of one EnsureCriticalTools run.
type ToolStatus struct {
	Tool      ToolSpec
	Ready     bool  // installed and verified (before or during this run)
	Installed bool  // newly installed or updated during this run
	Err       error // why the tool is not Ready; nil iff Ready
}

// errDeferredToBackground marks a tool that needs network access and was
// therefore skipped by an offline (AllowNetwork=false) run; the startup layer
// re-runs EnsureCriticalTools with AllowNetwork=true in the background.
var errDeferredToBackground = errors.New("requires network access; deferred to background install")

// EnsureCriticalTools reconciles the managed tool registry against the local
// install. It never blocks or fails app startup over the network: per-tool
// failures are isolated in the returned statuses instead of aborting the run,
// so one unavailable tool never prevents the others from being installed.
//
// The startup layer calls it twice: first with AllowNetwork=false during
// synchronous startup (local disk work only — existing binaries are probed,
// cached archives are installed), then, if any tool is left not Ready, with
// AllowNetwork=true from a background goroutine that completes installation
// while the app is already usable.
//
// Structural failures (directory creation, disk-space guard, registry
// resolution) are returned as the error; per-tool outcomes come back as
// statuses. Successful installs are persisted to .versions immediately, so a
// partially completed run resumes on the next launch.
func (m *Manager) EnsureCriticalTools(ctx context.Context, opts EnsureOptions) ([]ToolStatus, error) {
	cacheDir := filepath.Join(m.ToolsDir, ".cache")

	// Create required directories FIRST so the disk-space check below
	// has a valid path to stat. Without this, on first run Statfs would
	// receive ENOENT for ~/.c0wrk/tools/ and silently skip the check.
	// The parent ~/.c0wrk/ exists by now (created during Phase 1 logger init).
	for _, dir := range []string{m.BinDir, cacheDir, m.PythonDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	// Check available disk space (now that the tools directory exists).
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
			// Persist after each successful install so partial progress is
			// saved and the next launch skips the completed tools.
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

// reconcileTool brings one tool to its pinned version. Contract: a
// pinned-version change always forces a reinstall; a stale or missing binary
// is detected even when .versions claims a match; and stale artifacts are
// only destroyed once the bytes that replace them are secured — an offline
// failure must never leave the machine with less than it had before.
func (m *Manager) reconcileTool(ctx context.Context, tool ToolSpec, installed, cacheDir string, opts EnsureOptions) ToolStatus {
	status := ToolStatus{Tool: tool, Ready: true}

	if installed == tool.Version {
		// Verify the binary still exists on disk — the version file
		// could be stale if the user manually deleted the binary.
		binPath := m.binaryPath(tool.Name, tool.Type)
		if _, statErr := os.Stat(binPath); statErr == nil {
			if tool.Type != StaticBinary || m.binaryVersionMatches(ctx, tool, binPath) {
				m.Logger.Debug("tool already up-to-date", "tool", tool.Name, "version", tool.Version)
				return status
			}
			// The .versions file can also mask a stale binary: a prior
			// install may have been short-circuited by the
			// destination-exists check in InstallStaticBinary, leaving
			// the old binary in place while the version file already
			// records the new one. For static binaries the actual version
			// is verified; a probe mismatch falls through to a reinstall
			// attempt below (the old binary stays in place until
			// replacement bytes are secured).
			m.Logger.Warn("fast-path version probe mismatch, reinstalling", "tool", tool.Name)
		} else {
			m.Logger.Warn("version file present but binary missing, re-installing", "tool", tool.Name)
		}
	}

	// The tool needs (re)installation.
	if installErr := m.installOne(ctx, tool, cacheDir, opts); installErr != nil {
		status.Ready = false
		status.Err = installErr
		// Per-tool isolation: log and move on — remaining tools must still
		// be reconciled, and app startup must not be blocked.
		m.Logger.Error("tool install failed; continuing with remaining tools",
			"tool", tool.Name, "error", installErr)
		return status
	}
	status.Installed = true
	return status
}

// NeedsInstall returns the tools that need to be installed (version mismatch
// or missing binary). The caller can use this to decide whether to show a
// splash screen before starting the actual download/install work.
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

// GetToolPath returns the absolute path to a managed tool's binary.
// typ must match the tool's ToolType (StaticBinary or PythonPackage)
// to resolve the correct file extension on Windows.
// Returns empty string if the tool is not managed or not installed.
func (m *Manager) GetToolPath(name string, typ ToolType) string {
	p := m.binaryPath(name, typ)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// binaryPath returns the expected binary path for a managed tool.
// For PythonPackage tools on Windows, the wrapper script uses .cmd;
// for StaticBinary tools, the executable uses .exe.
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

// reportProgress calls the ProgressCallback if set.
func (m *Manager) reportProgress(toolName, stage string, bytesDone, bytesTotal int64) {
	if m.ProgressCallback != nil {
		m.ProgressCallback(toolName, stage, bytesDone, bytesTotal)
	}
}

// installOne installs a single tool.
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
		// Post-install verification (fail-closed): run the freshly installed
		// binary and require the pinned version to appear in its output
		// before the install is acknowledged. A binary that cannot report
		// the expected version must not let the .versions file record the
		// install as successful — the fresh binary is removed, the version
		// entry is not persisted, and the next startup retries.
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
			// The uv bootstrap and pip install require the network; nothing
			// local can satisfy a missing or outdated Python package.
			return fmt.Errorf("tool %q: %w", tool.Name, errDeferredToBackground)
		}
		return m.installPython(ctx, tool)
	default:
		return fmt.Errorf("unknown tool type: %s", tool.Type)
	}
}

// versionProbeTimeout bounds a single --version probe invocation. Real tool
// --version calls complete in milliseconds; the generous ceiling only guards
// against a wedged binary stalling startup indefinitely.
const versionProbeTimeout = 10 * time.Second

// probeBinaryVersion runs the binary at binPath with --version and returns
// its combined output. The probe runs under a timeout so a hung binary cannot
// stall startup, and hides the console window on Windows (GUI app). The child
// inherits the parent environment. On a non-zero exit (or start failure) the
// collected output is still returned alongside the error so callers can log
// or inspect it.
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

// versionInOutput reports whether the expected version string appears in the
// probe output — the fail-closed post-install contract: the install only
// counts as successful when the expected version string is present.
//
// The check is substring containment by design; a pinned "14.1.1" also
// matches "14.1.10". Accepting that superset is the safe direction here
// because the downloaded bytes are SHA256-pinned anyway — the probe's job is
// to catch a stale/wrong binary that survived an install, not to parse
// upstream version schemes.
func versionInOutput(output, want string) bool {
	return strings.Contains(output, want)
}

// maxProbeLogBytes caps probe output echoed into logs/errors so a broken
// binary that dumps garbage cannot flood them.
const maxProbeLogBytes = 512

// truncForLog shortens arbitrary command output for inclusion in a log line
// or error message. The cut backs up to a UTF-8 rune boundary so the result
// stays valid UTF-8 even when the boundary lands mid-rune.
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

// binaryVersionMatches reports whether the static binary at binPath — run
// with --version — reports the version pinned in the tool spec. Probe errors
// are treated as a mismatch (fail-closed): the caller responds with a
// reinstall, never with an error, so a probe hiccup degrades to a cheap
// cached re-extraction instead of failing app startup.
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

// installStatic downloads (or cache-hits) the pinned archive and extracts the
// binary. The stale destination binary is removed only AFTER the replacement
// archive is secured on disk: an offline cache miss must leave the previous
// binary in place rather than strip the machine of a possibly still
// functional tool. This ordering is what lets the offline startup phase
// attempt a fix from cache with zero risk. mode is DownloadCacheOnly in the
// offline startup pass (no network I/O) and DownloadOnline otherwise.
func (m *Manager) installStatic(ctx context.Context, tool ToolSpec, cacheDir string, mode DownloadMode) error {
	downloadProgress := func(done, total int64) {
		m.reportProgress(tool.Name, "download", done, total)
	}
	result, err := m.Downloader.Download(ctx, tool, cacheDir, mode, downloadProgress)
	if err != nil && mode == DownloadOnline {
		// Retry once for transient failures. CacheOnly failures are
		// deterministic (nothing on disk) — retrying would be noise.
		m.Logger.Warn("download failed, retrying", "tool", tool.Name, "error", err)
		result, err = m.Downloader.Download(ctx, tool, cacheDir, mode, downloadProgress)
	}
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	m.Logger.Debug("archive ready", "tool", tool.Name, "downloaded", result.Downloaded)

	// Replacement bytes are secured — now clear the destination so the
	// installer's exists short-circuit cannot leave the stale binary in
	// place while .versions already records the new version.
	m.removeStaleStaticBinary(tool)

	m.reportProgress(tool.Name, "extract", 0, 0)
	if _, err := m.Installer.InstallStaticBinary(result.ArchivePath, tool, m.BinDir); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	m.Logger.Debug("binary installed", "tool", tool.Name)
	m.cleanupOldArchives(tool, cacheDir)
	return nil
}

// cleanupOldArchives removes cached archives for the given tool that don't
// match the current ArchiveName. This prevents stale version archives from
// accumulating in ~/.c0wrk/tools/.cache/ across version bumps.
func (m *Manager) cleanupOldArchives(tool ToolSpec, cacheDir string) {
	// List all files in cacheDir matching the tool's prefix (tool name).
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return // cache dir doesn't exist or is unreadable — nothing to clean
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Skip the current archive.
		if name == tool.ArchiveName {
			continue
		}
		// Remove archives that start with the tool name prefix
		// (e.g. "uv-", "ripgrep-").
		if strings.HasPrefix(name, tool.Name+"-") || strings.HasPrefix(name, tool.Name+".") {
			path := filepath.Join(cacheDir, name)
			if err := os.Remove(path); err != nil {
				m.Logger.Debug("failed to remove old archive", "path", path, "error", err)
			}
		}
	}
}

// installPython bootstraps a Python environment and installs the package.
// The stale wrapper and virtual environment are removed immediately before
// the bootstrap runs — never earlier in the reconciliation — so a network
// failure cannot strip an existing (old-version) environment that would
// otherwise keep working. The offline startup phase never reaches this
// function at all (PythonPackage installs are deferred when network access
// is not allowed).
func (m *Manager) installPython(ctx context.Context, tool ToolSpec) error {
	m.reportProgress(tool.Name, "python_bootstrap", 0, 0)
	m.removeStalePythonEnv(tool)
	if _, err := m.Installer.InstallPythonPackage(ctx, tool, m.ToolsDir, m.BinDir); err != nil {
		return fmt.Errorf("python install: %w", err)
	}
	m.Logger.Debug("python package installed", "tool", tool.Name)
	return nil
}

// removeStalePythonEnv deletes the wrapper script and the virtual environment
// for a Python-package tool so that a version bump triggers a full rebootstrap.
// The uv-managed Python interpreter (python/install/) is intentionally left
// intact: it is shared and reinstalled idempotently by `uv python install`.
//
// Called from installPython immediately before InstallPythonPackage runs:
// the wrapper-existence short-circuit in InstallPythonPackage would otherwise
// skip the upgrade, leaving the previously installed (possibly vulnerable)
// package version in the venv while the .versions file already records the new
// version — a silent security gap.
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

// removeStaleStaticBinary deletes the installed binary for a StaticBinary
// tool. Call sites: installStatic (right before extraction — the installer
// short-circuits on an existing destination, so without removal the old
// binary would survive the "upgrade" while .versions already records the new
// version) and installOne's post-install verification (a binary that failed
// the version probe must not linger on disk). The reconciliation loop never
// removes the binary ahead of the download: an offline cache miss must leave
// the previous binary in place.
func (m *Manager) removeStaleStaticBinary(tool ToolSpec) {
	binPath := m.binaryPath(tool.Name, tool.Type)
	if err := os.Remove(binPath); err != nil && !os.IsNotExist(err) {
		m.Logger.Warn("failed to remove stale binary", "tool", tool.Name, "path", binPath, "error", err)
	}
}

// VenvPythonPath returns the absolute path of the Python interpreter inside
// the managed markitdown virtual environment (<toolsDir>/python/venv), or ""
// when the venv (or the interpreter inside it) does not exist. The layout is
// owned by this package (see InstallPythonPackage), so this is the canonical
// way for other layers to obtain the interpreter that can `import markitdown`
// — required for vision-assisted conversion, which must go through the
// markitdown Python API rather than the CLI.
//
// Pure filesystem probing; no installation is triggered.
func VenvPythonPath(toolsDir string) string {
	venvDir := filepath.Join(toolsDir, "python", "venv")
	return findPythonInDir(venvDir, "", runtime.GOOS)
}
