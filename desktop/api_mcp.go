package desktop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/user/agent/backend"
	"github.com/user/agent/backend/config"
	beMcp "github.com/user/agent/backend/mcp"
)

// ToolInfo represents a tool with its metadata, source, and policy for the frontend.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Policy      string `json:"policy"`
}

// GetMCPStatus returns current MCP server connection statuses.
// Returns an empty slice if the backend application is not initialized.
func (a *App) GetMCPStatus() []backend.MCPServerStatus {
	if a.app == nil {
		return []backend.MCPServerStatus{}
	}
	return a.app.GetMCPStatus()
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
	if a.app == nil {
		return []ToolInfo{}
	}

	// Get descriptors from the backend application.
	descriptors := a.app.ListTools()

	a.configMu.RLock()
	defer a.configMu.RUnlock()

	toolInfos := make([]ToolInfo, 0, len(descriptors))
	for _, desc := range descriptors {
		// Filter out internal tools
		if backend.IsInternalTool(desc.Name) {
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

	// Reconfigure MCP gateway via the backend builder.
	if a.app != nil {
		if err := a.app.Builder().ReconfigureMCP(context.Background(), backend.ToBuilderConfig(a.config)); err != nil {
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

// CheckCodebaseMemoryMCP checks if codebase-memory-mcp is installed and returns its status.
func (a *App) CheckCodebaseMemoryMCP() beMcp.CodeMemoryStatus {
	return beMcp.CheckCodebaseMemoryMCP()
}

// InstallCodebaseMemoryMCP downloads and installs the codebase-memory-mcp binary.
func (a *App) InstallCodebaseMemoryMCP() error {
	progress := func(status string) {
		wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", status)
	}

	installPath, err := beMcp.InstallCodebaseMemoryMCP(progress)
	if err != nil {
		return err
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

	// Reconfigure MCP gateway via the backend builder.
	if a.app != nil {
		if err := a.app.Builder().ReconfigureMCP(context.Background(), backend.ToBuilderConfig(a.config)); err != nil {
			wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "error")
			return fmt.Errorf("failed to reconfigure MCP gateway: %w", err)
		}
	}

	wailsRuntime.EventsEmit(a.ctx, "codememory:install-progress", "done")
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
	// Check if backend application exists and codebase-memory server is available
	if a.app == nil {
		return // silently skip
	}

	if !a.app.IsMCPServerConnected(codebaseMemoryServerName) {
		return // silently skip
	}

	// Emit start event
	wailsRuntime.EventsEmit(a.ctx, "codememory:indexing", map[string]any{
		"status": "start",
		"path":   projectPath,
	})

	// Call index_repository tool via the backend application
	result, err := a.app.CallMCPTool(a.ctx, codebaseMemoryServerName, "index_repository", map[string]any{
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
