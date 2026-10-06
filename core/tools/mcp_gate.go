package tools

import "context"

// gatedMCPServersKey is the context key for the set of MCP server names whose
// tools are gated OFF for the current task: every server configured mode
// "disabled", and every mode "manual" server the user did not mention in the
// task's messages. Conceptually the task's allowed set is the complement —
// auto ∪ (manual ∩ mentioned) — but it is carried as the GATED set so that a
// server absent from the mode map (a gateway-only registration, or a server
// added by a config change while the task runs) stays ALLOWED instead of
// being silently dropped by an exhaustive allowlist snapshot.
//
// The set is attached once at task start (HandleMessage / Resume, see
// core/orchestrator_mcp.go) and inherited by everything derived from that
// context: the Conductor's executor, delegated subagents, the goal verifier,
// and the E2S loop. The registry's Execute gate reads it as
// defense-in-depth — the LLM-facing descriptor catalogs are already filtered
// upstream, but a hallucinated (or stale-name) call must fail closed at
// dispatch time too.
type gatedMCPServersKey struct{}

// WithGatedMCPServers attaches the gated-off MCP server set to the context.
// A nil/empty set is equivalent to no value: nothing is gated.
func WithGatedMCPServers(ctx context.Context, gated map[string]bool) context.Context {
	return context.WithValue(ctx, gatedMCPServersKey{}, gated)
}

// GatedMCPServersFromContext extracts the gated-off MCP server set from the
// context. Returns nil when absent (nothing gated — the default for tasks
// with no manual/disabled servers and for callers that never gate).
func GatedMCPServersFromContext(ctx context.Context) map[string]bool {
	if v, ok := ctx.Value(gatedMCPServersKey{}).(map[string]bool); ok {
		return v
	}
	return nil
}
