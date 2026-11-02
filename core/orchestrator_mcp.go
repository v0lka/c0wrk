package core

import (
	"context"
	"sort"

	sdktools "github.com/v0lka/sp4rk/tools"

	"github.com/v0lka/c0wrk/core/tools"
)

// Per-server MCP mode values, mirrored from backend/config (core never imports
// it); "disabled" is builder_mcp.go's mcpModeDisabled — these join it for gating.
const (
	mcpModeAuto   = "auto"
	mcpModeManual = "manual"
)

// mcpSessionContext selects which side of a CapabilityModes entry a session
// reads: CHAT (No Project) → chat_mode, CODE → code_mode.
type mcpSessionContext int

const (
	mcpSessionChat mcpSessionContext = iota
	mcpSessionCode
)

// capabilityModesForSession projects the two-slot map through a session context
// into the flat name→mode map; nil = all-auto. See specs/domains/capabilities.md.
func capabilityModesForSession(modes map[string]CapabilityModes, ctx mcpSessionContext) map[string]string {
	if len(modes) == 0 {
		return nil
	}
	flat := make(map[string]string, len(modes))
	for name, cm := range modes {
		mode := cm.CodeMode
		if ctx == mcpSessionChat {
			mode = cm.ChatMode
		}
		if mode != "" {
			flat[name] = mode
		}
	}
	if len(flat) == 0 {
		return nil
	}
	return flat
}

// currentMCPCapabilityModes returns the current per-server capability
// snapshot: the live resolver's when wired, else the constructor's static map.
func (o *Orchestrator) currentMCPCapabilityModes() map[string]CapabilityModes {
	if o.config.MCPServerModesResolver != nil {
		return o.config.MCPServerModesResolver()
	}
	return o.config.MCPServerModes
}

// sessionCapabilityContext reports the session's capability context: CHAT
// (No Project) or CODE (real project). See specs/domains/capabilities.md.
func (o *Orchestrator) sessionCapabilityContext() mcpSessionContext {
	if o.isNoProject {
		return mcpSessionChat
	}
	return mcpSessionCode
}

// gatedMCPServerSet computes the names gated OFF for a task — "disabled" plus
// unmentioned "manual" servers, as the gated complement (absent name stays
// allowed); nil/empty means nothing is gated. See specs/domains/tool-system/mcp-gateway.md (mention gating).
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

// filterGatedMCPTools strips the MCP-sourced descriptors of gated-off servers
// from a descriptor list (gate layer 1). See specs/domains/tool-system/mcp-gateway.md (mention gating).
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

// stripGatedMCPTools is the launcher-side mirror of filterGatedMCPTools (gate
// layer 2): subagent toolsets come from the RAW registry list, so the same
// filter re-applies. See specs/domains/tool-system/mcp-gateway.md (mention gating).
func stripGatedMCPTools(ctx context.Context, descs []sdktools.ToolDescriptor) []sdktools.ToolDescriptor {
	return filterGatedMCPTools(descs, tools.GatedMCPServersFromContext(ctx))
}

// applyMCPModeGating snapshots modes and filters descriptors for DIRECT callers;
// production entry uses prepareTaskMCP once + per-catalog stripGatedMCPTools.
// See specs/domains/capabilities.md (session-kind context).
func (o *Orchestrator) applyMCPModeGating(ctx context.Context, descs []sdktools.ToolDescriptor, mentioned []string) ([]sdktools.ToolDescriptor, context.Context) {
	modes := capabilityModesForSession(o.currentMCPCapabilityModes(), o.sessionCapabilityContext())
	gated := gatedMCPServerSet(modes, mentioned)
	if len(gated) == 0 {
		return descs, ctx
	}
	o.logDebug("orchestrator: MCP per-server mode gating applied", "gated_servers", len(gated))
	return filterGatedMCPTools(descs, gated), tools.WithGatedMCPServers(ctx, gated)
}

// mcpMentionSet is the immutable per-task value in the mcpMentions sync.Map:
// a sorted, deduplicated slice behind a POINTER (CompareAndSwap compares pointers).
type mcpMentionSet struct {
	names []string
}

// recordMCPMentions retains monotonic task intent for explicitly nonpersistent
// orchestrators; persistent tasks use prepareTaskMCP, never this ephemeral fallback.
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
