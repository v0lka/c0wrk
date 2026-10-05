package core

import (
	"context"
	"sort"

	sdktools "github.com/v0lka/sp4rk/tools"

	"github.com/v0lka/c0wrk/core/tools"
)

// Per-server MCP mode values (config `mcp.servers.<name>.mode`), mirrored
// from backend/config — core never imports backend/config, so the enum is
// duplicated here. "disabled" is mirrored next to the gateway mapping in
// builder_mcp.go (mcpModeDisabled); these two join it for the gating side.
const (
	mcpModeAuto   = "auto"
	mcpModeManual = "manual"
)

// gatedMCPServerSet computes the set of MCP server names whose tools are
// gated OFF for a task:
//
//   - every "disabled" server — defense-in-depth: the builder already omits
//     disabled servers from the gateway config so they are never dialed, and
//     this covers stale tool registrations left by a reconfigure race;
//   - every "manual" server the user did NOT mention in the task's messages.
//
// "auto" servers and servers absent from the mode map (gateway-only
// registrations) stay enabled. The task's allowed set is the complement:
// auto ∪ (manual ∩ mentioned). A nil/empty result means nothing is gated —
// the common case when no manual/disabled servers are configured, so callers
// treat it as a zero-regression no-op.
func gatedMCPServerSet(modes map[string]string, mentioned []string) map[string]bool {
	if len(modes) == 0 {
		return nil
	}
	mentionedSet := make(map[string]struct{}, len(mentioned))
	for _, name := range mentioned {
		mentionedSet[name] = struct{}{}
	}
	var gated map[string]bool
	for name, mode := range modes {
		var gate bool
		switch mode {
		case mcpModeDisabled:
			gate = true
		case mcpModeManual:
			_, ok := mentionedSet[name]
			gate = !ok
		}
		if gate {
			if gated == nil {
				gated = make(map[string]bool, len(modes))
			}
			gated[name] = true
		}
	}
	return gated
}

// filterGatedMCPTools removes the MCP-sourced descriptors of gated-off
// servers from a descriptor list. Non-MCP tools and MCP tools of allowed
// servers pass through untouched. A nil/empty gated set returns the input
// unchanged.
func filterGatedMCPTools(descs []sdktools.ToolDescriptor, gated map[string]bool) []sdktools.ToolDescriptor {
	if len(gated) == 0 || len(descs) == 0 {
		return descs
	}
	out := make([]sdktools.ToolDescriptor, 0, len(descs))
	for _, d := range descs {
		if d.SourceCategory == sdktools.SourceCategoryMCP && gated[d.Source] {
			continue
		}
		out = append(out, d)
	}
	return out
}

// stripGatedMCPTools is the launcher-side mirror of filterGatedMCPTools: it
// reads the task's gated set from the context (attached at the task boundary by
// prepareTaskMCP) so the delegation launcher's subagent toolsets — built
// from the RAW registry list, bypassing the orchestrator's filtered view —
// apply exactly the same source filter. Without a gated set in the context it
// is a no-op.
func stripGatedMCPTools(ctx context.Context, descs []sdktools.ToolDescriptor) []sdktools.ToolDescriptor {
	return filterGatedMCPTools(descs, tools.GatedMCPServersFromContext(ctx))
}

// applyMCPModeGating snapshots current modes and filters descriptors for direct
// callers. Production task entry uses prepareTaskMCP once, then filters every
// catalog with stripGatedMCPTools so reconfiguration cannot change a running
// task's authorization snapshot. A nil resolver uses the constructor's static
// MCPServerModes map. When nothing is gated, both the list and context pass
// through unchanged.
func (o *Orchestrator) applyMCPModeGating(ctx context.Context, descs []sdktools.ToolDescriptor, mentioned []string) ([]sdktools.ToolDescriptor, context.Context) {
	modes := o.config.MCPServerModes
	if o.config.MCPServerModesResolver != nil {
		modes = o.config.MCPServerModesResolver()
	}
	gated := gatedMCPServerSet(modes, mentioned)
	if len(gated) == 0 {
		return descs, ctx
	}
	o.logDebug("orchestrator: MCP per-server mode gating applied", "gated_servers", len(gated))
	return filterGatedMCPTools(descs, gated), tools.WithGatedMCPServers(ctx, gated)
}

// mcpMentionSet is the immutable value stored per task in the mcpMentions
// sync.Map: a sorted, deduplicated name slice behind a POINTER so the map's
// CompareAndSwap (which requires comparable values) compares the pointer,
// never the slice — copy-on-write merge under contention.
type mcpMentionSet struct {
	names []string
}

// recordMCPMentions retains monotonic task intent for explicitly nonpersistent
// orchestrators. Persistent tasks use prepareTaskMCP and never fall back to this
// ephemeral store after a durable read or write failure.
func (o *Orchestrator) recordMCPMentions(taskID string, mentioned []string) {
	if taskID == "" || len(mentioned) == 0 {
		return
	}
	for {
		existing, _ := o.mcpMentions.LoadOrStore(taskID, &mcpMentionSet{})
		set, _ := existing.(*mcpMentionSet)
		if set == nil {
			set = &mcpMentionSet{}
		}
		merged := mergeSortedUniqueNames(set.names, mentioned)
		if merged == nil {
			return // nothing new
		}
		if o.mcpMentions.CompareAndSwap(taskID, existing, &mcpMentionSet{names: merged}) {
			return
		}
	}
}

// mcpMentionsFor returns the sorted union of MCP servers mentioned across a
// task's messages. Empty for unknown tasks / tasks without mentions.
func (o *Orchestrator) mcpMentionsFor(taskID string) []string {
	if taskID == "" {
		return nil
	}
	v, ok := o.mcpMentions.Load(taskID)
	if !ok {
		return nil
	}
	set, _ := v.(*mcpMentionSet)
	if set == nil {
		return nil
	}
	return set.names
}

// mergeSortedUniqueNames unions a sorted, deduplicated base list with extra
// names and returns the new sorted, deduplicated list — or nil when the extras
// add nothing (the caller treats nil as "no write needed").
func mergeSortedUniqueNames(base, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	for _, name := range base {
		seen[name] = struct{}{}
	}
	changed := false
	for _, name := range extra {
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			changed = true
		}
	}
	if !changed {
		return nil
	}
	merged := make([]string, 0, len(seen))
	for name := range seen {
		merged = append(merged, name)
	}
	sort.Strings(merged)
	return merged
}
