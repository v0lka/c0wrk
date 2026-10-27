// Package modelProfiles implements tool-set selection for a small/mid-size
// conductor LLM: a static, guaranteed union of the user's pins, the
// protected ReAct-mandatory tools, every MCP tool, and turn-scoped extra
// guarantees — pure, deterministic, once per task (specs/domains/
// model-profiles.md, Essential Tools).
package modelprofiles

import (
	"sort"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// finishToolName is the completion channel — without it the loop cannot
// terminate, so it is always preserved.
const finishToolName = "finish"

// protectedToolNames are retained in every session regardless of the
// profile's pins — the ReAct loop's own minimum plus the read/recovery
// tools; MCP tools are likewise always kept (specs/domains/model-profiles.md).
var protectedToolNames = map[string]struct{}{
	finishToolName:        {},
	"store_fact":          {},
	"search_facts":        {},
	"ask_user":            {},
	"update_checklist":    {},
	"read_attachment":     {},
	"read_skill_resource": {},
	"tool_result_read":    {},
}

// ProtectedToolNames returns the sorted, freshly allocated names of the
// always-kept tools, for UI layers that render them as permanently present
// ("locked") chips.
func ProtectedToolNames() []string {
	out := make([]string, 0, len(protectedToolNames))
	for n := range protectedToolNames {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// SelectTools assembles the tool set as a static union of the user's
// always-present pins, the protected tools, every MCP-sourced tool, and the
// caller's turn-scoped extraGuaranteed names — emitted in registry order,
// deduplicated by name (first wins); unknown guaranteed names are silently
// ignored, and there is no slot budget. See the Essential Tools section of
// specs/domains/model-profiles.md.
func SelectTools(all []sdktools.ToolDescriptor, alwaysPresent []string, extraGuaranteed ...string) []sdktools.ToolDescriptor {
	guaranteedNames := make(map[string]struct{}, len(alwaysPresent)+len(protectedToolNames)+len(extraGuaranteed))
	for _, n := range alwaysPresent {
		guaranteedNames[n] = struct{}{}
	}
	for n := range protectedToolNames {
		guaranteedNames[n] = struct{}{}
	}
	for _, n := range extraGuaranteed {
		guaranteedNames[n] = struct{}{}
	}

	out := make([]sdktools.ToolDescriptor, 0, len(all))
	seen := make(map[string]struct{}, len(all))
	for _, d := range all {
		if _, dup := seen[d.Name]; dup {
			continue
		}
		if d.SourceCategory != sdktools.SourceCategoryMCP {
			if _, ok := guaranteedNames[d.Name]; !ok {
				continue
			}
		}
		seen[d.Name] = struct{}{}
		out = append(out, d)
	}
	return out
}
