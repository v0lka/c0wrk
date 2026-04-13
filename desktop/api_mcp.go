package desktop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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
