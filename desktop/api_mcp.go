package desktop

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/user/agent/backend/config"
	"github.com/user/agent/core/tools"
	"github.com/user/agent/core/tools/mcp"
)

// ToolInfo represents a tool with its metadata, source, and policy for the frontend.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Policy      string `json:"policy"`
}

// GetMCPStatus returns current MCP server connection statuses.
// Returns an empty slice if the MCP gateway is not initialized.
func (a *App) GetMCPStatus() []mcp.ServerStatus {
	if a.mcpGateway == nil {
		return []mcp.ServerStatus{}
	}
	return a.mcpGateway.Status()
}

// GetMCPServers returns the current MCP server configurations.
// Returns an empty map if config is not initialized.
func (a *App) GetMCPServers() map[string]config.MCPServerConfig {
	a.configMu.RLock()
	defer a.configMu.RUnlock()

	if a.config == nil {
		return map[string]config.MCPServerConfig{}
	}

	// Deep-copy to avoid external modifications
	result := make(map[string]config.MCPServerConfig, len(a.config.MCP.Servers))
	for name, cfg := range a.config.MCP.Servers {
		srv := cfg
		if cfg.Args != nil {
			srv.Args = make([]string, len(cfg.Args))
			copy(srv.Args, cfg.Args)
		}
		if cfg.Env != nil {
			srv.Env = make(map[string]string, len(cfg.Env))
			for ek, ev := range cfg.Env {
				srv.Env[ek] = ev
			}
		}
		if cfg.Headers != nil {
			srv.Headers = make(map[string]string, len(cfg.Headers))
			for hk, hv := range cfg.Headers {
				srv.Headers[hk] = hv
			}
		}
		result[name] = srv
	}

	return result
}

// GetToolList returns all registered tools with source and policy info.
// Internal tools are filtered out from the list.
func (a *App) GetToolList() []ToolInfo {
	if a.toolRegistry == nil {
		return []ToolInfo{}
	}

	// Get descriptors from the SDK registry (embedded in core registry)
	descriptors := a.toolRegistry.List()

	a.configMu.RLock()
	defer a.configMu.RUnlock()

	toolInfos := make([]ToolInfo, 0, len(descriptors))
	for _, desc := range descriptors {
		// Filter out internal tools
		if tools.IsInternalTool(desc.Name) {
			continue
		}

		// Resolve effective policy
		policy := a.resolveToolPolicy(desc.Name)

		toolInfos = append(toolInfos, ToolInfo{
			Name:        desc.Name,
			Description: desc.Description,
			Source:      desc.Source,
			Policy:      policy,
		})
	}

	return toolInfos
}

// resolveToolPolicy returns the effective policy string for a tool.
// It checks policy overrides and default policy from config.
func (a *App) resolveToolPolicy(toolName string) string {
	if a.config == nil {
		return "user_confirm"
	}

	// Check per-tool override first
	if policyCfg, ok := a.config.Security.ToolPolicies[toolName]; ok {
		return policyCfg.Policy
	}

	// Fall back to default policy
	if a.config.Security.DefaultPolicy != "" {
		return a.config.Security.DefaultPolicy
	}

	return "user_confirm"
}

// UpdateMCPServers updates MCP server configuration and hot-reloads the gateway.
func (a *App) UpdateMCPServers(servers map[string]config.MCPServerConfig) error {
	// Validate config first
	for name, cfg := range servers {
		if err := validateMCPServerConfig(name, cfg); err != nil {
			return fmt.Errorf("invalid config for server %q: %w", name, err)
		}
	}

	a.configMu.Lock()
	defer a.configMu.Unlock()

	if a.config == nil {
		return errors.New("config not initialized")
	}

	// Deep-copy the servers map to avoid external modifications
	a.config.MCP.Servers = make(map[string]config.MCPServerConfig, len(servers))
	for name, cfg := range servers {
		srv := cfg
		if cfg.Args != nil {
			srv.Args = make([]string, len(cfg.Args))
			copy(srv.Args, cfg.Args)
		}
		if cfg.Env != nil {
			srv.Env = make(map[string]string, len(cfg.Env))
			for ek, ev := range cfg.Env {
				srv.Env[ek] = ev
			}
		}
		if cfg.Headers != nil {
			srv.Headers = make(map[string]string, len(cfg.Headers))
			for hk, hv := range cfg.Headers {
				srv.Headers[hk] = hv
			}
		}
		a.config.MCP.Servers[name] = srv
	}

	// Persist config
	if err := a.persistConfig(); err != nil {
		slog.Warn("failed to persist MCP server settings", "error", err)
	}

	// Build gateway config from server configs
	mcpEntries := make(map[string]mcp.ServerEntry, len(a.config.MCP.Servers))
	for name, cfg := range a.config.MCP.Servers {
		mcpEntries[name] = mcp.ServerEntry{
			Transport: cfg.Transport,
			Command:   cfg.Command,
			Args:      cfg.Args,
			Env:       cfg.Env,
			URL:       cfg.URL,
			Headers:   cfg.Headers,
		}
	}

	// Get logger
	var log *slog.Logger
	if a.sessionLogger != nil {
		log = a.sessionLogger.Logger()
	} else {
		log = slog.Default()
	}

	// Reconfigure gateway (or start if nil)
	if a.mcpGateway == nil {
		gateway, err := mcp.StartGateway(context.Background(), mcp.GatewayConfig{Servers: mcpEntries}, a.toolRegistry, config.ExpandEnvVars, log)
		if err != nil {
			return fmt.Errorf("failed to start MCP gateway: %w", err)
		}
		a.mcpGateway = gateway
	} else {
		if err := a.mcpGateway.Reconfigure(context.Background(), mcp.GatewayConfig{Servers: mcpEntries}, a.toolRegistry, config.ExpandEnvVars, log); err != nil {
			return fmt.Errorf("failed to reconfigure MCP gateway: %w", err)
		}
	}

	return nil
}

// validateMCPServerConfig validates a single MCP server configuration.
func validateMCPServerConfig(name string, cfg config.MCPServerConfig) error {
	transport := cfg.Transport
	if transport == "" {
		transport = "stdio" // default
	}

	switch transport {
	case "stdio":
		if cfg.Command == "" {
			return errors.New("stdio transport requires a command")
		}
	case "http":
		if cfg.URL == "" {
			return errors.New("http transport requires a URL")
		}
	default:
		return fmt.Errorf("unsupported transport: %q", transport)
	}

	return nil
}

// CheckCodebaseMemoryMCP checks if codebase-memory-mcp is installed and returns its path.
// CodeMemoryStatus represents the installation status of codebase-memory-mcp.
type CodeMemoryStatus struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path"`
}

// CheckCodebaseMemoryMCP checks if codebase-memory-mcp is installed and returns its status.
func (a *App) CheckCodebaseMemoryMCP() CodeMemoryStatus {
	// Try exec.LookPath first
	if path, err := exec.LookPath("codebase-memory-mcp"); err == nil {
		return CodeMemoryStatus{Installed: true, Path: path}
	}
	// Check common install location
	home, _ := os.UserHomeDir()
	localPath := filepath.Join(home, ".local", "bin", "codebase-memory-mcp")
	if _, err := os.Stat(localPath); err == nil {
		return CodeMemoryStatus{Installed: true, Path: localPath}
	}
	return CodeMemoryStatus{}
}

// InstallCodebaseMemoryMCP downloads and installs the codebase-memory-mcp binary.
func (a *App) InstallCodebaseMemoryMCP() error {
	// Determine OS/arch
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	// Map Go arch to release arch
	arch := goarch
	if goarch == "amd64" {
		arch = "x86_64"
	}

	// Determine file extension
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}

	// Build download URL
	url := fmt.Sprintf("https://github.com/DeusData/codebase-memory-mcp/releases/latest/download/codebase-memory-mcp-%s-%s.%s", goos, arch, ext)

	slog.Info("downloading codebase-memory-mcp", "url", url)
	wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "downloading")

	// Create temp directory
	tempDir, err := os.MkdirTemp("", "codebase-memory-mcp-*")
	if err != nil {
		wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "error")
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	// Download file
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if err != nil {
		wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "error")
		return fmt.Errorf("failed to create request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "error")
		return fmt.Errorf("failed to download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "error")
		return fmt.Errorf("download failed with status: %s", resp.Status)
	}

	// Save to temp file
	archivePath := filepath.Join(tempDir, "codebase-memory-mcp."+ext)
	out, err := os.Create(archivePath)
	if err != nil {
		wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "error")
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	_, err = io.Copy(out, resp.Body)
	_ = out.Close()
	if err != nil {
		wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "error")
		return fmt.Errorf("failed to save download: %w", err)
	}

	slog.Info("extracting codebase-memory-mcp")
	wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "installing")

	// Extract archive
	extractDir := filepath.Join(tempDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "error")
		return fmt.Errorf("failed to create extract directory: %w", err)
	}

	if goos == "windows" {
		if err := extractZip(archivePath, extractDir); err != nil {
			wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "error")
			return fmt.Errorf("failed to extract zip: %w", err)
		}
	} else {
		cmd := exec.CommandContext(context.Background(), "tar", "xzf", archivePath, "-C", extractDir)
		if output, err := cmd.CombinedOutput(); err != nil {
			wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "error")
			return fmt.Errorf("failed to extract tar.gz: %w, output: %s", err, string(output))
		}
	}

	// Run installer
	slog.Info("running codebase-memory-mcp installer")
	var installCmd *exec.Cmd
	if goos == "windows" {
		installCmd = exec.CommandContext(context.Background(), "powershell", "-File", "install.ps1", "-SkipConfig")
	} else {
		installCmd = exec.CommandContext(context.Background(), "./install.sh", "--skip-config")
	}
	installCmd.Dir = extractDir
	if output, err := installCmd.CombinedOutput(); err != nil {
		wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "error")
		return fmt.Errorf("installer failed: %w, output: %s", err, string(output))
	}

	// Verify installation
	var installPath string
	if path, err := exec.LookPath("codebase-memory-mcp"); err == nil {
		installPath = path
	} else {
		home, _ := os.UserHomeDir()
		localPath := filepath.Join(home, ".local", "bin", "codebase-memory-mcp")
		if _, err := os.Stat(localPath); err == nil {
			installPath = localPath
		} else {
			wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "error")
			return errors.New("installation verification failed: binary not found after install")
		}
	}

	slog.Info("codebase-memory-mcp installed", "path", installPath)
	wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "configuring")

	// Add MCP config entry
	a.configMu.Lock()
	defer a.configMu.Unlock()

	if a.config.MCP.Servers == nil {
		a.config.MCP.Servers = make(map[string]config.MCPServerConfig)
	}
	a.config.MCP.Servers["codebase-memory"] = config.MCPServerConfig{
		Transport: "stdio",
		Command:   "codebase-memory-mcp",
	}

	// Persist config
	if err := a.persistConfig(); err != nil {
		slog.Warn("failed to persist MCP server settings", "error", err)
	}

	// Build gateway config from server configs
	mcpEntries := make(map[string]mcp.ServerEntry, len(a.config.MCP.Servers))
	for name, cfg := range a.config.MCP.Servers {
		mcpEntries[name] = mcp.ServerEntry{
			Transport: cfg.Transport,
			Command:   cfg.Command,
			Args:      cfg.Args,
			Env:       cfg.Env,
			URL:       cfg.URL,
			Headers:   cfg.Headers,
		}
	}

	// Get logger
	var log *slog.Logger
	if a.sessionLogger != nil {
		log = a.sessionLogger.Logger()
	} else {
		log = slog.Default()
	}

	// Reconfigure gateway (or start if nil)
	if a.mcpGateway == nil {
		gateway, err := mcp.StartGateway(context.Background(), mcp.GatewayConfig{Servers: mcpEntries}, a.toolRegistry, config.ExpandEnvVars, log)
		if err != nil {
			wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "error")
			return fmt.Errorf("failed to start MCP gateway: %w", err)
		}
		a.mcpGateway = gateway
	} else {
		if err := a.mcpGateway.Reconfigure(context.Background(), mcp.GatewayConfig{Servers: mcpEntries}, a.toolRegistry, config.ExpandEnvVars, log); err != nil {
			wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "error")
			return fmt.Errorf("failed to reconfigure MCP gateway: %w", err)
		}
	}

	wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "done")
	return nil
}

// extractZip extracts a zip file to the specified directory.
func extractZip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)

		// Check for ZipSlip
		if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path: %s", fpath)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(fpath, f.Mode()); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(fpath), 0o755); err != nil {
			return err
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			_ = outFile.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		_ = outFile.Close()
		_ = rc.Close()

		if err != nil {
			return err
		}
	}
	return nil
}

// codebaseMemoryServerName is the name of the MCP server that provides
// codebase indexing functionality.
const codebaseMemoryServerName = "codebase-memory"

// indexDebounceTimer tracks the debounce timer for workspace change indexing.
var indexDebounceTimer *time.Timer
var indexDebounceMu sync.Mutex

// IndexRepository triggers codebase-memory-mcp indexing for the given project path.
// It runs asynchronously and emits events for progress tracking.
func (a *App) IndexRepository(projectPath string) {
	go a.indexRepositoryAsync(projectPath)
}

func (a *App) indexRepositoryAsync(projectPath string) {
	// Check if gateway exists and codebase-memory server is available
	if a.mcpGateway == nil {
		return // silently skip
	}

	// Get the codebase-memory server
	server := a.mcpGateway.GetServer(codebaseMemoryServerName)
	if server == nil || !server.IsConnected() {
		return // silently skip
	}

	// Emit start event
	wailsRuntime.EventsEmit(a.ctx, "codememory:indexing", map[string]any{
		"status": "start",
		"path":   projectPath,
	})

	// Call index_repository tool via the server
	result, err := server.CallTool(a.ctx, "index_repository", map[string]any{
		"path": projectPath,
	})

	if err != nil {
		slog.Warn("codebase indexing failed", "path", projectPath, "error", err)
		wailsRuntime.EventsEmit(a.ctx, "codememory:indexing", map[string]any{
			"status": "error",
			"error":  err.Error(),
		})
		return
	}

	// Check if the tool call itself returned an error
	if result != nil && result.IsError {
		errMsg := "indexing tool returned error"
		if len(result.Content) > 0 {
			// Extract text from first content item if available
			for _, c := range result.Content {
				if text := extractTextFromMCPContent(c); text != "" {
					errMsg = text
					break
				}
			}
		}
		slog.Warn("codebase indexing tool error", "path", projectPath, "error", errMsg)
		wailsRuntime.EventsEmit(a.ctx, "codememory:indexing", map[string]any{
			"status": "error",
			"error":  errMsg,
		})
		return
	}

	// Success
	slog.Debug("codebase indexing completed", "path", projectPath)
	wailsRuntime.EventsEmit(a.ctx, "codememory:indexing", map[string]any{
		"status": "done",
	})
}

// debouncedIndexRepository triggers indexing with a 5-second debounce.
// This is used for workspace change events to avoid excessive indexing.
func (a *App) debouncedIndexRepository(projectPath string) {
	indexDebounceMu.Lock()
	defer indexDebounceMu.Unlock()

	if indexDebounceTimer != nil {
		indexDebounceTimer.Stop()
	}
	indexDebounceTimer = time.AfterFunc(5*time.Second, func() {
		a.IndexRepository(projectPath)
	})
}

// extractTextFromMCPContent extracts text from an MCP Content interface.
func extractTextFromMCPContent(content interface{}) string {
	// Try type assertion for map with text field (common MCP content format)
	if m, ok := content.(map[string]interface{}); ok {
		if text, ok := m["text"].(string); ok {
			return text
		}
	}
	// Try type assertion for string
	if s, ok := content.(string); ok {
		return s
	}
	return ""
}
