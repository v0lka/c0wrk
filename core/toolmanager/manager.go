package toolmanager

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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
// Manager is not safe for concurrent use. It is intended to be constructed,
// used for EnsureCriticalTools during synchronous startup, and then discarded
// or used only from the same goroutine that created it.
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

// EnsureCriticalTools ensures all managed tools are downloaded and installed.
// On first run this may take several minutes (downloading archives, bootstrapping
// Python). On subsequent runs it checks the .versions file and skips already-
// installed tools. Returns an error describing which tool could not be installed.
func (m *Manager) EnsureCriticalTools(ctx context.Context) error {
	cacheDir := filepath.Join(m.ToolsDir, ".cache")

	// Create required directories FIRST so the disk-space check below
	// has a valid path to stat. Without this, on first run Statfs would
	// receive ENOENT for ~/.c0wrk/tools/ and silently skip the check.
	// The parent ~/.c0wrk/ exists by now (created during Phase 1 logger init).
	for _, dir := range []string{m.BinDir, cacheDir, m.PythonDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	// Check available disk space (now that the tools directory exists).
	if err := checkDiskSpace(m.ToolsDir, 200*1024*1024); err != nil {
		return fmt.Errorf("insufficient disk space: %w", err)
	}

	// Read installed versions.
	versions, err := ReadVersions(m.ToolsDir)
	if err != nil {
		m.Logger.Warn("failed to read versions file, treating as fresh install", "error", err)
		versions = ToolVersions{}
	}

	tools, err := ManagedTools()
	if err != nil {
		return fmt.Errorf("resolving tool registry: %w", err)
	}
	for _, tool := range tools {
		if versions[tool.Name] == tool.Version {
			// Verify the binary still exists on disk — the version file
			// could be stale if the user manually deleted the binary.
			binPath := m.binaryPath(tool.Name, tool.Type)
			if _, statErr := os.Stat(binPath); statErr == nil {
				m.Logger.Debug("tool already up-to-date", "tool", tool.Name, "version", tool.Version)
				continue
			}
			m.Logger.Warn("version file present but binary missing, re-installing", "tool", tool.Name)
		}

		m.Logger.Info("installing tool", "tool", tool.Name, "version", tool.Version)
		m.reportProgress(tool.Name, "download", 0, 0)
		if installErr := m.installOne(ctx, tool, cacheDir); installErr != nil {
			return fmt.Errorf("failed to install %s: %w", tool.Name, installErr)
		}

		versions[tool.Name] = tool.Version
		// Persist after each successful install so partial progress is saved.
		if writeErr := WriteVersions(m.ToolsDir, versions); writeErr != nil {
			m.Logger.Warn("failed to write versions file", "error", writeErr)
		}
	}

	m.Logger.Info("all critical tools ready", "count", len(tools))
	return nil
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
func (m *Manager) installOne(ctx context.Context, tool ToolSpec, cacheDir string) error {
	switch tool.Type {
	case StaticBinary:
		return m.installStatic(ctx, tool, cacheDir)
	case PythonPackage:
		return m.installPython(ctx, tool)
	default:
		return fmt.Errorf("unknown tool type: %s", tool.Type)
	}
}

// installStatic downloads and installs a static binary. Retries once on
// transient download failures.
func (m *Manager) installStatic(ctx context.Context, tool ToolSpec, cacheDir string) error {
	result, err := m.Downloader.Download(ctx, tool, cacheDir)
	if err != nil {
		// Retry once for transient failures.
		m.Logger.Warn("download failed, retrying", "tool", tool.Name, "error", err)
		result, err = m.Downloader.Download(ctx, tool, cacheDir)
		if err != nil {
			return fmt.Errorf("download: %w", err)
		}
	}
	m.Logger.Debug("archive ready", "tool", tool.Name, "downloaded", result.Downloaded)

	m.reportProgress(tool.Name, "extract", 0, 0)
	_, err = m.Installer.InstallStaticBinary(result.ArchivePath, tool, m.BinDir)
	if err != nil {
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
		// (e.g. "uv-", "ripgrep-", "rtk-").
		if strings.HasPrefix(name, tool.Name+"-") || strings.HasPrefix(name, tool.Name+".") {
			path := filepath.Join(cacheDir, name)
			if err := os.Remove(path); err != nil {
				m.Logger.Debug("failed to remove old archive", "path", path, "error", err)
			}
		}
	}
}

// installPython bootstraps a Python environment and installs the package.
func (m *Manager) installPython(ctx context.Context, tool ToolSpec) error {
	m.reportProgress(tool.Name, "python_bootstrap", 0, 0)
	_, err := m.Installer.InstallPythonPackage(ctx, tool, m.ToolsDir, m.BinDir)
	if err != nil {
		return fmt.Errorf("python install: %w", err)
	}
	m.Logger.Debug("python package installed", "tool", tool.Name)
	return nil
}
