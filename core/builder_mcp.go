package core

import (
	"context"
	"errors"
	"maps"
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
	// Accepted policy is authoritative even when readiness or network reconciliation fails.
	b.mu.Lock()
	b.mcpModes = mcpServerModesFromConfig(cfg)
	stopping := b.mcpStopping
	b.mu.Unlock()
	if stopping {
		// StopGateway has already given up on this builder; rebuilding the
		// gateway now would race the shutdown that just declined to wait for
		// the previous build. Callers log this as a warning — a reconfigure
		// racing app exit is degenerate by definition.
		return errors.New("mcp reconfigure rejected: builder is shutting down")
	}
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
	//
	// The mcpStopping re-check is the load-bearing part: StopGateway may have
	// flipped it while this call was parked in waitMCPReady above (its context
	// is independent of shutdown), in which case acting on — or replacing —
	// the gateway would re-dial or resurrect servers nothing will stop. Because
	// StopGateway sets the flag before it joins mcpDone, any call that gets
	// here after startup settles observes it.
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

	// No gateway: startup failed earlier (waitMCPReady guarantees runMCPInit
	// has finished, so this is not the startup window). Dial outside b.mu and
	// publish under it, mirroring runMCPInit's record-and-apply.
	newGW, err := mcp.StartGateway(ctx, mcpCfg, b.registry.ToolRegistry, cfg.ExpandEnvVars, b.logger)
	if err != nil {
		return err
	}

	// Publish through the guarded helper (the same path runMCPInit uses): a
	// shutdown that began while this call was dialing stops the fresh gateway
	// instead of leaking it, and a work dir recorded meanwhile is applied.
	if !b.publishMCPGateway(newGW, nil) {
		return errors.New("mcp reconfigure rejected: builder is shutting down")
	}
	return nil
}

// currentMCPServerModes returns an independent current policy snapshot.
func (b *OrchestratorBuilder) currentMCPServerModes() map[string]string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return maps.Clone(b.mcpModes)
}

// mcpStopStartupGrace bounds how long StopGateway waits for the still-running
// MCP startup goroutine. Full server discovery takes seconds — spawning every
// configured stdio server is process-start work — and StopGateway runs on the
// app-shutdown path, so a quit during the first seconds of startup must not
// beach-ball the main thread for the whole build just to tear the result down
// immediately afterwards. Past the grace, the startup goroutine keeps
// ownership of the not-yet-published gateway (see publishMCPGateway) and
// stops it off the shutdown path.
const mcpStopStartupGrace = time.Second

// StopGateway stops the MCP gateway. Called during app shutdown.
//
// It marks the builder as shutting down under b.mu FIRST (so no gateway can be
// published or reconfigured afterwards — a ReconfigureMCP parked in
// waitMCPReady re-checks the flag at its publication site), then aborts an
// in-flight MCP startup so the gateway that goroutine is still building is
// stopped promptly, and finally joins that goroutine — for at most
// mcpStopStartupGrace. Cancelling the startup is what keeps the join short:
// Start abandons the remaining servers, runMCPInit stops the not-yet-published
// gateway it built (publishMCPGateway) and closes mcpDone well inside the
// grace. The grace still bounds a startup wedged in a connect that ignores
// cancellation; past it, the startup goroutine retains ownership of the
// not-yet-published gateway and stops it off the shutdown path.
func (b *OrchestratorBuilder) StopGateway() error {
	b.mu.Lock()
	b.mcpStopping = true
	initCancel := b.mcpInitCancel
	b.mu.Unlock()

	// Abort an in-flight MCP startup (a no-op once it has finished) so the
	// gateway it is still building is torn down promptly and the join below is
	// short rather than waiting out the whole server-spawn sequence on the
	// app-shutdown thread.
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
		// Startup is still in flight past the grace. It keeps ownership of the
		// not-yet-published gateway it built and stops it off this path; there
		// is nothing published here to stop.
		b.log().Info("MCP gateway startup still in flight during shutdown; startup goroutine owns stopping the not-yet-published gateway")
		return nil
	}
	if gw == nil {
		return nil
	}
	return gw.Stop()
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

// mcpModeDisabled mirrors config.MCPServerModeDisabled ("disabled"). core
// never imports backend/config, so the enum value is duplicated here; the
// adapter carries the config value verbatim into BuilderMCPServer.Mode.
// mcpModeAuto / mcpModeManual (the gating-relevant siblings) live in
// orchestrator_mcp.go next to their consumer.
const mcpModeDisabled = "disabled"

// mcpServerModesFromConfig projects the per-server MCP modes into the
// orchestrator's runtime map (name → mode). Empty/absent modes are omitted:
// gatedMCPServerSet treats a missing entry as auto (always available), and
// the config load path has already normalized every configured server to a
// canonical mode value (backend/config normalizeMCPModes), so this map only
// carries canonical values — including "disabled", whose servers the gateway
// never dials but whose stale registrations the orchestrator-side gating
// must still hide.
func mcpServerModesFromConfig(cfg *BuilderConfig) map[string]string {
	if len(cfg.MCP.Servers) == 0 {
		return nil
	}
	modes := make(map[string]string, len(cfg.MCP.Servers))
	for name, srv := range cfg.MCP.Servers {
		if srv.Mode != "" {
			modes[name] = srv.Mode
		}
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
		// A disabled server is never dialed: it is omitted from the gateway
		// config entirely, so no process is spawned and no connection is
		// opened — neither at startup nor on reconfigure. auto and manual
		// both stay; manual's on-demand behavior is layered above the
		// gateway, not by skipping it here.
		if srv.Mode == mcpModeDisabled {
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
