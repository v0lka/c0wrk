package core

import (
	"context"
	"time"

	"github.com/v0lka/c0wrk/sdk/tools/mcp"
)

// ReconfigureMCP reconfigures the MCP gateway with the given config.
// If no gateway exists, starts a new one.
func (b *OrchestratorBuilder) ReconfigureMCP(ctx context.Context, cfg *BuilderConfig) error {
	if err := b.waitReady(ctx); err != nil {
		return err
	}

	mcpCfg := configToGatewayConfig(cfg)
	mcpCfg.HTTPClient = b.proxyClient

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.gateway != nil {
		return b.gateway.Reconfigure(ctx, mcpCfg, b.registry.ToolRegistry, cfg.ExpandEnvVars, b.logger)
	}

	mcpCfg.SchemaSanitizer = b.paramManager.SanitizeSchema
	gw, err := mcp.StartGateway(ctx, mcpCfg, b.registry.ToolRegistry, cfg.ExpandEnvVars, b.logger)
	if err != nil {
		return err
	}
	b.gateway = gw
	return nil
}

// StopGateway stops the MCP gateway. Called during app shutdown.
// Does not wait for async init — if the gateway hasn't been created yet,
// there is nothing to stop.
func (b *OrchestratorBuilder) StopGateway() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gateway != nil {
		return b.gateway.Stop()
	}
	return nil
}

// SetMCPWorkDir updates the default working directory for MCP stdio server processes.
// New or restarted MCP servers will use this directory as their cwd.
func (b *OrchestratorBuilder) SetMCPWorkDir(path string) {
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = b.waitReady(waitCtx)
	b.mu.RLock()
	gw := b.gateway
	b.mu.RUnlock()
	if gw != nil {
		gw.SetDefaultWorkDir(path)
	}
}

// configToGatewayConfig converts BuilderConfig to MCP GatewayConfig.
func configToGatewayConfig(cfg *BuilderConfig) mcp.GatewayConfig {
	entries := make(map[string]mcp.ServerEntry, len(cfg.MCP.Servers))
	for name, srv := range cfg.MCP.Servers {
		entries[name] = mcp.ServerEntry{
			Transport: srv.Transport,
			Command:   srv.Command,
			Args:      srv.Args,
			Env:       srv.Env,
			URL:       srv.URL,
			Headers:   srv.Headers,
			WorkDir:   srv.WorkDir,
		}
	}
	return mcp.GatewayConfig{
		Servers:        entries,
		DefaultWorkDir: cfg.MCP.DefaultWorkDir,
	}
}
