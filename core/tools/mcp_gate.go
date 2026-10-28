package tools

import "context"

// gatedMCPServersKey carries the task's gated-OFF MCP server set (disabled ∪
// manual-without-mention) as the complement of the allowed set, so servers
// absent from the mode map stay allowed; attached once per task, inherited by
// everything derived from the context. See specs/domains/tool-system/mcp-gateway.md.
type gatedMCPServersKey struct{}

// WithGatedMCPServers attaches the gated-off MCP server set to the context.
// A nil/empty set is equivalent to no value: nothing is gated.
func WithGatedMCPServers(ctx context.Context, gated map[string]bool) context.Context {
	return context.WithValue(ctx, gatedMCPServersKey{}, gated)
}

// GatedMCPServersFromContext extracts the gated-off MCP server set from the
// context; nil when absent (nothing gated).
func GatedMCPServersFromContext(ctx context.Context) map[string]bool {
	if v, ok := ctx.Value(gatedMCPServersKey{}).(map[string]bool); ok {
		return v
	}
	return nil
}
