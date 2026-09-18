package core

import (
	"context"
	"time"

	"github.com/v0lka/sp4rk/tools/mcp"
)

// ReconfigureMCP reconfigures the MCP gateway with the given config.
// If no gateway exists, starts a new one.
//
// Note: MCP startup (runMCPInit) is decoupled from initDone, so this method
// waits on mcpDone — NOT initDone — to guarantee the initial gateway assignment
// has completed before reading/acting on b.gateway. Otherwise a Reconfigure
// arriving during the first ~seconds of startup could observe b.gateway == nil
// and start a duplicate gateway that races with runMCPInit.
//
// LOCKING: b.mu is NEVER held across the gateway work below, only around the
// snapshot and the publication. Both Reconfigure and StartGateway connect to
// MCP servers over the network, and a single unreachable endpoint can hold
// them for minutes — a previously-failed HTTP server is retried on every
// Reconfigure, silently and with no deadline of its own. b.mu sits on the
// GetConfig path (GetConfig takes configMu.RLock, then b.mu.RLock via
// ModelRegistry), so holding the write lock across that work convoys every
// config reader behind it and freezes the whole settings dialog for the
// duration. Dropping b.mu costs nothing in consistency: the gateway
// serializes concurrent Reconfigure calls on its own mutex, and reconfigureMu
// below serializes this method.
func (b *OrchestratorBuilder) ReconfigureMCP(ctx context.Context, cfg *BuilderConfig) error {
	if err := b.waitMCPReady(ctx); err != nil {
		return err
	}

	mcpCfg := configToGatewayConfig(cfg)

	// Serializes this call end to end: without it two callers that both find
	// no gateway would each dial one and orphan the loser. Acquired BEFORE
	// b.mu, never the other way round.
	b.reconfigureMu.Lock()
	defer b.reconfigureMu.Unlock()

	// Snapshot the proxy client and the gateway pointer, then RELEASE the
	// lock before any network work. Reading b.proxyClient here under the lock
	// also closes a data race against RebuildProxy, which writes it.
	b.mu.RLock()
	mcpCfg.HTTPClient = b.proxyClient
	gw := b.gateway
	b.mu.RUnlock()

	if gw != nil {
		return gw.Reconfigure(ctx, mcpCfg, b.registry.ToolRegistry, cfg.ExpandEnvVars)
	}

	// No gateway: startup failed earlier (waitMCPReady guarantees runMCPInit
	// has finished, so this is not the startup window). Dial outside b.mu and
	// publish under it, mirroring runMCPInit's record-and-apply.
	newGW, err := mcp.StartGateway(ctx, mcpCfg, b.registry.ToolRegistry, cfg.ExpandEnvVars, b.logger)
	if err != nil {
		return err
	}

	b.mu.Lock()
	b.gateway = newGW
	// Apply a work dir recorded by SetMCPWorkDir while we were dialing — the
	// same record-and-apply race runMCPInit closes.
	if b.mcpWorkDir != "" {
		newGW.SetDefaultWorkDir(b.mcpWorkDir)
	}
	b.mu.Unlock()
	return nil
}

// StopGateway stops the MCP gateway. Called during app shutdown.
// Waits for the MCP startup goroutine to finish so that a Stop arriving while
// startup is still in flight does not race with the initial gateway assignment
// (MCP startup is decoupled from initDone). If the startup goroutine failed to
// create a gateway, there is nothing to stop.
func (b *OrchestratorBuilder) StopGateway() error {
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = b.waitMCPReady(waitCtx)

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gateway != nil {
		return b.gateway.Stop()
	}
	return nil
}

// SetMCPWorkDir updates the default working directory for MCP stdio server processes.
// New or restarted MCP servers will use this directory as their cwd.
//
// This uses a record-and-apply pattern: it records the directory in b.mcpWorkDir
// (so it survives and is applied by runMCPInit if the gateway has not yet been
// assigned) and, if the gateway is already assigned, applies it immediately.
// It intentionally does NOT wait on mcpDone: SetDefaultWorkDir is a pure field
// write on the gateway and needs no network, so blocking here would serialise
// this cheap call behind the (potentially multi-second) MCP server discovery
// that runMCPInit performs. Both this method and runMCPInit take b.mu, so they
// are serialised and the last writer of mcpWorkDir wins — for BOTH the recorded
// field and the live gateway value, because the apply (gw.SetDefaultWorkDir) is
// performed under b.mu too. gw.SetDefaultWorkDir takes the gateway's own mutex,
// not b.mu, so there is no nested-lock/deadlock risk.
func (b *OrchestratorBuilder) SetMCPWorkDir(path string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mcpWorkDir = path
	if b.gateway != nil {
		b.gateway.SetDefaultWorkDir(path)
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
