package backend

import (
	"context"
	"errors"
	"fmt"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/tools/mcp"
)

// GetMCPStatus returns current MCP server connection statuses.
// Returns an empty slice if the backend application is not initialized.
func (f *FrontendAPI) GetMCPStatus() []mcp.ServerStatus {
	if f.app == nil {
		return []mcp.ServerStatus{}
	}
	return f.app.GetMCPStatus()
}

// GetMCPServers returns the current MCP server configurations.
// Returns an empty map if config is not initialized.
func (f *FrontendAPI) GetMCPServers() map[string]config.MCPServerConfig {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		return map[string]config.MCPServerConfig{}
	}

	// Deep-copy to avoid external modifications
	result := make(map[string]config.MCPServerConfig, len(f.config.MCP.Servers))
	for name, cfg := range f.config.MCP.Servers {
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
func (f *FrontendAPI) GetToolList() []ToolInfo {
	if f.app == nil {
		return []ToolInfo{}
	}

	// Get descriptors from the backend application.
	descriptors := f.app.ListTools()

	f.configMu.RLock()
	defer f.configMu.RUnlock()

	toolInfos := make([]ToolInfo, 0, len(descriptors))
	for _, desc := range descriptors {
		// Filter out internal tools
		if tools.IsInternalTool(desc.Name) {
			continue
		}

		// Resolve effective policy
		policy := f.resolveToolPolicy(desc.Name)

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
func (f *FrontendAPI) resolveToolPolicy(toolName string) string {
	if f.config == nil {
		return "user_confirm"
	}

	// Check per-tool override first
	if policyCfg, ok := f.config.Security.ToolPolicies[toolName]; ok {
		return policyCfg.Policy
	}

	// Fall back to default policy
	if f.config.Security.DefaultPolicy != "" {
		return f.config.Security.DefaultPolicy
	}

	return "user_confirm"
}

// UpdateMCPServers updates MCP server configuration and hot-reloads the gateway.
func (f *FrontendAPI) UpdateMCPServers(servers map[string]config.MCPServerConfig) error {
	// Validate config first
	for name, cfg := range servers {
		if err := validateMCPServerConfig(name, cfg); err != nil {
			return fmt.Errorf("invalid config for server %q: %w", name, err)
		}
	}

	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	// Deep-copy the servers map to avoid external modifications
	f.config.MCP.Servers = make(map[string]config.MCPServerConfig, len(servers))
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
		f.config.MCP.Servers[name] = srv
	}

	// Persist config
	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist MCP server settings", "error", err)
	}

	// Reconfigure MCP gateway via the backend builder.
	if b := f.builder(); b != nil {
		if err := b.ReconfigureMCP(context.Background(), ToBuilderConfig(f.config)); err != nil {
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
			return fmt.Errorf("server %q: stdio transport requires a command", name)
		}
	case "http":
		if cfg.URL == "" {
			return fmt.Errorf("server %q: http transport requires a URL", name)
		}
	default:
		return fmt.Errorf("server %q: unsupported transport: %q", name, transport)
	}

	return nil
}
