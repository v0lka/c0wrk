// Package smallllm implements tool-set selection for running the conductor
// against a "small" LLM. Small models are disproportionately penalized by
// large tool schemas (every prompt carries the full JSON schema of every
// advertised tool), so narrowing the visible tool set reduces both token
// overhead and decision fatigue.
//
// Selection is purely static: SelectTools unions the user's always-present
// list, the protected orchestration tools (the completion channel, fact
// memory, and the human-interaction channel), every MCP-sourced tool, and any
// turn-scoped extra-guaranteed names the caller passes (e.g. delegate when
// the user explicitly requested subagents). There is no quantitative budget,
// no router matching, and no domain-specific allow-listing — the user decides
// which tools are essential; this function only assembles their selection.
//
// Every tool in the selection is guaranteed and never trimmed: the pins are
// explicit user choices, MCP tools are user-installed integrations, and the
// protected set carries the completion channel — dropping any of them would
// silently break pinned workflows, user-installed MCP servers, or the
// conductor loop's ability to terminate. SelectTools never fails and never
// drops a selected tool.
//
// All functions in this package are pure and deterministic — no LLM, embedding,
// or network calls. They are factored out so they can be unit-tested in
// isolation and applied at a single, well-defined point in the orchestration
// lifecycle (once per task, before the ReAct loop).
package smallllm

import (
	"sort"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// finishToolName is the mandatory completion channel. It is ALWAYS preserved
// regardless of the selected set — without it the conductor loop cannot
// terminate.
const finishToolName = "finish"

// protectedToolNames are retained regardless of the always-present list: the
// completion channel, the fact memory (store/search), and the
// human-interaction channel. MCP-sourced tools are likewise always kept (they
// are user-installed and not part of the orchestration-noise problem).
var protectedToolNames = map[string]struct{}{
	finishToolName:     {},
	"store_fact":       {},
	"search_facts":     {},
	"ask_user":         {},
	"update_checklist": {},
}

// ProtectedToolNames returns the sorted names of the orchestration-mandatory
// tools that SelectTools always keeps regardless of the always-present input:
// the completion channel (finish), the fact memory (store/search_facts), and
// the human-interaction channel (ask_user), plus update_checklist. Exposed so
// UI layers can surface these as permanently present ("locked") alongside the
// user's always-present list. The result is deterministic (sorted) and freshly
// allocated.
func ProtectedToolNames() []string {
	out := make([]string, 0, len(protectedToolNames))
	for n := range protectedToolNames {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// SelectTools assembles the small-LLM tool set as a static union of:
//
//   - the user's always-present pins,
//   - the protected orchestration tools,
//   - every MCP-sourced tool (user-installed),
//   - the caller's extraGuaranteed names — turn-scoped tools this task's
//     directives require (e.g. delegate when the user explicitly requested
//     subagents; like the MCP class the guarantee is per-call, never static).
//
// The full descriptor list is swept once in registry order, deduplicated by
// name (first occurrence wins), and every surviving descriptor is emitted in
// registry order. A guaranteed name matching nothing registered is silently
// ignored (a guarantee cannot invent a tool). There is no slot budget: the
// result is exactly the selection described above, nothing more, nothing less.
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
