package core

import (
	"context"
	"errors"
	"maps"
	"time"

	"github.com/v0lka/sp4rk/tools/mcp"
)

// ReconfigureMCP reconfigures the MCP gateway with the given config, starting
// one if none exists (lifecycle: specs/domains/tool-system/mcp-gateway.md).
// It waits on mcpDone — NOT initDone — so a Reconfigure during the first
// seconds of startup cannot observe b.gateway == nil and race runMCPInit with
// a duplicate gateway.
//
// b.mu is held only around the snapshot and publication, never the network
// work: an unreachable endpoint can hold a dial for minutes, and b.mu sits on
// the GetConfig path, so convoying config readers behind it would freeze the
// settings dialog. Consistency is unaffected — the gateway serializes
// Reconfigures on its own mutex and reconfigureMu serializes this method.
func (b *OrchestratorBuilder) ReconfigureMCP(ctx context.Context, cfg *BuilderConfig) error {
	// Accepted policy is authoritative even when readiness or network reconciliation fails.
	b.mu.Lock()
	b.mcpModes = mcpServerModesFromConfig(cfg)
	stopping := b.mcpStopping
	b.mu.Unlock()
	if stopping {
		// StopGateway already gave up on this builder; rebuilding would race
		// the shutdown that declined to wait for the previous build.
		return errors.New("mcp reconfigure rejected: builder is shutting down")
	}
	if err := b.waitMCPReady(ctx); err != nil {
		return err
	}

	mcpCfg := configToGatewayConfig(cfg)

	// Serializes this call end to end: two callers that both find no gateway
	// would each dial one and orphan the loser. Taken BEFORE b.mu, never after.
	b.reconfigureMu.Lock()
	defer b.reconfigureMu.Unlock()

	// Snapshot the proxy client (also closes the race against RebuildProxy's
	// write) and the gateway pointer, then RELEASE the lock before network
	// work. The mcpStopping re-check is load-bearing: StopGateway may have
	// flipped it while this call was parked in waitMCPReady, and acting on —
	// or replacing — the gateway then would resurrect servers nothing stops.
	b.mu.RLock()
	mcpCfg.HTTPClient = b.proxyClient
	gw := b.gateway
	stopping = b.mcpStopping
	b.mu.RUnlock()
	if stopping {
		return errors.New("mcp reconfigure rejected: builder is shutting down")
	}

	if gw != nil {
		return gw.Reconfigure(ctx, mcpCfg, b.registry.ToolRegistry, cfg.ExpandEnvVars)
	}

	// No gateway: startup failed earlier (waitMCPReady rules out the startup
	// window). Dial outside b.mu, publish under it (runMCPInit's pattern).
	newGW, err := mcp.StartGateway(ctx, mcpCfg, b.registry.ToolRegistry, cfg.ExpandEnvVars, b.logger)
	if err != nil {
		return err
	}

	// Publish through the guarded helper: a shutdown that began while dialing
	// stops the fresh gateway instead of leaking it, and a meanwhile-recorded
	// work dir is applied.
	if !b.publishMCPGateway(newGW, nil) {
		return errors.New("mcp reconfigure rejected: builder is shutting down")
	}
	return nil
}

// currentMCPServerModes returns an independent current policy snapshot.
func (b *OrchestratorBuilder) currentMCPServerModes() map[string]CapabilityModes {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return maps.Clone(b.mcpModes)
}

// mcpStopStartupGrace bounds StopGateway's wait for an in-flight MCP startup;
// past it the startup goroutine keeps ownership of the not-yet-published
// gateway and stops it off the shutdown path. Protocol:
// specs/domains/tool-system/mcp-gateway.md.
const mcpStopStartupGrace = time.Second

// StopGateway stops the MCP gateway (app shutdown). It first marks the builder
// as shutting down under b.mu (no gateway can be published or reconfigured
// afterwards), aborts the in-flight startup so the join stays short, then
// joins within mcpStopStartupGrace. Protocol and the wedged-startup fallback:
// specs/domains/tool-system/mcp-gateway.md.
func (b *OrchestratorBuilder) StopGateway() error {
	b.mu.Lock()
	b.mcpStopping = true
	initCancel := b.mcpInitCancel
	b.mu.Unlock()

	// Abort the in-flight startup so the join below stays short.
	if initCancel != nil {
		initCancel()
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), mcpStopStartupGrace)
	defer cancel()
	waitErr := b.waitMCPReady(waitCtx)

	b.mu.Lock()
	gw := b.gateway
	b.mu.Unlock()

	if waitErr != nil && gw == nil {
		// Startup still in flight past the grace: it owns stopping the
		// not-yet-published gateway; nothing is published here to stop.
		b.log().Info("MCP gateway startup still in flight during shutdown; startup goroutine owns stopping the not-yet-published gateway")
		return nil
	}
	if gw == nil {
		return nil
	}
	return gw.Stop()
}

// SetMCPWorkDir updates the default working directory for MCP stdio server
// processes: recorded in b.mcpWorkDir (applied by runMCPInit when the gateway
// is not yet assigned) and applied immediately otherwise. It deliberately does
// NOT wait on mcpDone — the apply is a pure field write, and waiting would
// queue this cheap call behind multi-second server discovery. Both writers
// take b.mu, so the last writer wins for the recorded field and the live
// gateway alike; gw.SetDefaultWorkDir takes the gateway's own mutex, so there
// is no nested-lock risk.
func (b *OrchestratorBuilder) SetMCPWorkDir(path string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mcpWorkDir = path
	if b.gateway != nil {
		b.gateway.SetDefaultWorkDir(path)
	}
}

// mcpModeDisabled mirrors config.MCPServerModeDisabled; core never imports
// backend/config, so the value is duplicated. The gating-relevant siblings
// live in orchestrator_mcp.go next to their consumer.
const mcpModeDisabled = "disabled"

// mcpServerModesFromConfig projects per-server MCP capability modes into the
// runtime map; both-empty entries are omitted (absent = auto). Dial rule and
// canonical values: specs/domains/capabilities.md.
func mcpServerModesFromConfig(cfg *BuilderConfig) map[string]CapabilityModes {
	if len(cfg.MCP.Servers) == 0 {
		return nil
	}
	modes := make(map[string]CapabilityModes, len(cfg.MCP.Servers))
	for name, srv := range cfg.MCP.Servers {
		if srv.ChatMode == "" && srv.CodeMode == "" {
			continue // both auto — absent entry
		}
		modes[name] = CapabilityModes{ChatMode: srv.ChatMode, CodeMode: srv.CodeMode}
	}
	if len(modes) == 0 {
		return nil
	}
	return modes
}

// configToGatewayConfig converts BuilderConfig to MCP GatewayConfig.
func configToGatewayConfig(cfg *BuilderConfig) mcp.GatewayConfig {
	entries := make(map[string]mcp.ServerEntry, len(cfg.MCP.Servers))
	for name, srv := range cfg.MCP.Servers {
		// A both-disabled server is omitted from the gateway config entirely —
		// never spawned, never connected (specs/domains/capabilities.md, dial
		// rule); a single-context-disabled server stays dialed for the other
		// context and is gated per task there.
		if srv.ChatMode == mcpModeDisabled && srv.CodeMode == mcpModeDisabled {
			continue
		}
		entries[name] = mcp.ServerEntry{
			Transport:   srv.Transport,
			Command:     srv.Command,
			Args:        srv.Args,
			Env:         srv.Env,
			URL:         srv.URL,
			Headers:     srv.Headers,
			WorkDir:     srv.WorkDir,
			Timeout:     srv.Timeout,
			CallTimeout: srv.CallTimeout,
		}
	}
	return mcp.GatewayConfig{
		Servers:        entries,
		DefaultWorkDir: cfg.MCP.DefaultWorkDir,
	}
}
