package backend

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/mcp"
)

// GetMCPStatus returns the status of every CONFIGURED MCP server so the
// settings UI always renders the full configuration, unavailable servers
// included (shown with a red indicator). Configured names missing from the
// live gateway status — the gateway is missing entirely, failed to start, or
// dropped a server — are synthesized as disconnected entries. While gateway
// startup is still in flight (the "_gateway" starting placeholder) nothing is
// merged: availability is genuinely unknown, and the frontend refreshes on
// the mcp:ready event once startup completes.
// Returns an empty slice if the backend application is not initialized.
func (f *FrontendAPI) GetMCPStatus() []mcp.ServerStatus {
	if f.app == nil {
		return []mcp.ServerStatus{}
	}
	status := f.app.GetMCPStatus()
	if isMCPStartupPlaceholder(status) {
		return status
	}
	return mergeConfiguredMCPServers(status, f.GetMCPServers())
}

// isMCPStartupPlaceholder reports whether status is the synthetic
// gateway-starting placeholder Application.GetMCPStatus returns while the MCP
// startup goroutine is still running.
func isMCPStartupPlaceholder(status []mcp.ServerStatus) bool {
	return len(status) == 1 && status[0].Name == "_gateway" && status[0].Starting
}

// mergeConfiguredMCPServers appends a disconnected entry (Error "unavailable")
// for every configured server absent from status, then sorts the result by
// name. This keeps the settings list a mirror of the configuration even when
// the gateway cannot report a server itself (failed startup, gateway missing,
// config ahead of a failed reconfigure).
func mergeConfiguredMCPServers(status []mcp.ServerStatus, configured map[string]config.MCPServerConfig) []mcp.ServerStatus {
	if len(configured) == 0 {
		return status
	}
	present := make(map[string]bool, len(status))
	for _, s := range status {
		present[s.Name] = true
	}
	for name, cfg := range configured {
		if present[name] {
			continue
		}
		transport := cfg.Transport
		if transport == "" {
			transport = "stdio"
		}
		status = append(status, mcp.ServerStatus{
			Name:      name,
			Transport: transport,
			Connected: false,
			Tools:     []string{},
			Error:     "unavailable",
		})
	}
	sort.Slice(status, func(i, j int) bool { return status[i].Name < status[j].Name })
	return status
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

// GetToolList returns all registered tools with source, security group, and
// effective policy info. System-group tools (internal orchestration tools)
// are filtered out — classification is by the descriptor's group, never by
// tool name. The policy is read from the LIVE registry group-policy map (the
// same map Execute consults), so the list reflects what is actually enforced
// after runtime updates, not a re-derivation from config.
func (f *FrontendAPI) GetToolList() []ToolInfo {
	if f.app == nil {
		return []ToolInfo{}
	}

	// Descriptors and effective group policies from the backend application.
	return buildToolInfos(f.app.ListTools(), f.app.GroupPolicies())
}

// buildToolInfos converts tool descriptors into frontend ToolInfo entries,
// skipping system-group tools. A group without a configured entry fails safe
// to user_confirm — mirroring the registry's own resolution.
func buildToolInfos(
	descriptors []sdktools.ToolDescriptor,
	policies map[sdktools.ToolGroup]sdktools.ToolPolicy,
) []ToolInfo {
	toolInfos := make([]ToolInfo, 0, len(descriptors))
	for _, desc := range descriptors {
		// Filter out internal (system-group) tools.
		if desc.Group == sdktools.GroupSystem {
			continue
		}

		toolInfos = append(toolInfos, ToolInfo{
			Name:        desc.Name,
			Description: desc.Description,
			Source:      desc.Source,
			Group:       string(desc.Group),
			Policy:      effectiveGroupPolicy(policies, desc.Group),
		})
	}

	return toolInfos
}

// effectiveGroupPolicy renders the effective policy for a tool group as the
// config enum string ("allow"|"user_confirm"|"deny"). Missing entries and
// unrecognized values fail safe to user_confirm, exactly like
// core/tools.ToolRegistry.groupPolicy (a raw map index is NOT enough — the
// ToolPolicy zero value is PolicyAlwaysAllow, so an absent group would
// otherwise render as "allow").
func effectiveGroupPolicy(policies map[sdktools.ToolGroup]sdktools.ToolPolicy, group sdktools.ToolGroup) string {
	if p, ok := policies[group]; ok {
		switch p {
		case sdktools.PolicyAlwaysAllow:
			return config.GroupPolicyAllow
		case sdktools.PolicyAlwaysDeny:
			return config.GroupPolicyDeny
		}
	}
	return config.GroupPolicyUserConfirm
}

// mcpReconfigureTimeout bounds the propagation phase of an MCP server config
// change. It matches the budget proxyRebuildTimeout and runMCPInit give MCP
// work: ReconfigureMCP reconnects every changed server and retries every
// previously-failed one, and a failed HTTP server is retried with no
// deadline of its own.
const mcpReconfigureTimeout = 30 * time.Second

// UpdateMCPServers updates MCP server configuration and hot-reloads the
// gateway.
//
// Locking contract (mirrors UpdateProxySettings): saveMu serializes whole
// save sequences, while configMu is held only for the field mutation and the
// disk write — never across the ReconfigureMCP propagation. Reconfigure
// reconnects every changed server and retries every previously-failed one
// with no deadline of its own; holding the configMu WRITE lock across it
// blocks every concurrent GetConfig and — because Go's RWMutex stops
// admitting readers once a writer is queued — freezes the whole settings
// dialog for as long as the reconfigure takes (observed at 169 s when an
// unreachable endpoint was retried). The disk write stays inside the lock:
// it is a bounded local atomic rewrite.
func (f *FrontendAPI) UpdateMCPServers(servers map[string]config.MCPServerConfig) error {
	// Validate config first
	for name, cfg := range servers {
		if err := validateMCPServerConfig(name, cfg); err != nil {
			return fmt.Errorf("invalid config for server %q: %w", name, err)
		}
	}

	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
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

	// Snapshot what the reconfigure needs while the lock is still held; after
	// the unlock f.config must only be touched under configMu again.
	b := f.builder()
	bcfg := ToBuilderConfig(f.config, f.modelProfilesCatalog())
	f.configMu.Unlock()

	// --- Heavy work below runs OUTSIDE configMu (readers stay responsive) ---
	// saveMu is still held, so concurrent UpdateMCPServers calls are
	// serialized and a later save never propagates before an earlier one.
	//
	// The propagation is bounded: ReconfigureMCP hands this context to the
	// gateway reconfigure, whose server.Connect honours it, so one
	// unreachable endpoint can no longer stall the update (and the dialog)
	// indefinitely.
	if b != nil {
		reconfigureCtx, cancel := context.WithTimeout(context.Background(), mcpReconfigureTimeout)
		err := b.ReconfigureMCP(reconfigureCtx, bcfg)
		cancel()
		if err != nil {
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

	if err := validateMCPTimeout(name, "timeout", cfg.Timeout); err != nil {
		return err
	}
	if err := validateMCPTimeout(name, "call_timeout", cfg.CallTimeout); err != nil {
		return err
	}

	return nil
}

// validateMCPTimeout rejects a non-empty timeout / call_timeout that does not
// parse as a Go duration or is non-positive. Empty is allowed — the server
// falls back to the mcp package default (and call_timeout to timeout). Shares
// the duration rule with the load path and the builder adapter through
// config.ParseMCPDuration, so all three agree on exactly which values are valid.
func validateMCPTimeout(name, field, raw string) error {
	if _, err := config.ParseMCPDuration(raw); err != nil {
		return fmt.Errorf("server %q: invalid %s %q: %w", name, field, raw, err)
	}
	return nil
}
