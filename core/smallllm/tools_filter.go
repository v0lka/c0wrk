// Package smallllm implements tool-set selection for running the conductor
// against a "small" LLM. Small models are disproportionately penalized by
// large tool schemas (every prompt carries the full JSON schema of every
// advertised tool), so narrowing the visible tool set reduces both token
// overhead and decision fatigue.
//
// Selection is purely semantic: SelectTools unions the router-matched tool
// names, the user's always-present list, the protected orchestration tools
// (the completion channel, fact memory, and the human-interaction channel),
// and every MCP-sourced tool. There is no domain-specific allow-listing — the
// router and the user decide which tools are relevant; this function only
// assembles their choices and (optionally) enforces a size budget.
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
// regardless of the matched set or budget — without it the conductor loop
// cannot terminate.
const finishToolName = "finish"

// protectedToolNames are retained regardless of the matched/always-present
// lists and always survive the maxTools budget: the completion channel, the
// fact memory (store/search), and the human-interaction channel. MCP-sourced
// tools are likewise always kept (they are user-installed and not part of the
// orchestration-noise problem).
var protectedToolNames = map[string]struct{}{
	finishToolName:     {},
	"store_fact":       {},
	"search_facts":     {},
	"ask_user":         {},
	"update_checklist": {},
}

// ProtectedToolNames returns the sorted names of the orchestration-mandatory
// tools that SelectTools always keeps regardless of the matched/always-present
// input or the maxTools budget: the completion channel (finish), the fact
// memory (store/search_facts), and the human-interaction channel (ask_user),
// plus update_checklist. Exposed so UI layers can surface these as permanently
// present ("locked") alongside the user's always-present list. The result is
// deterministic (sorted) and freshly allocated.
func ProtectedToolNames() []string {
	out := make([]string, 0, len(protectedToolNames))
	for n := range protectedToolNames {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// SelectTools assembles the small-LLM tool set from four sources:
//
//   - matchedNames:  tools the router selected for this task.
//   - alwaysPresent: tools the user pinned to every task.
//   - protectedToolNames: orchestration-mandatory tools (finish + memory +
//     human interaction) that must never be dropped.
//   - every MCP-sourced tool (user-installed, outside the noise problem).
//
// It builds the union keep-set, filters the full descriptor list to those
// whose Name is in the keep-set OR whose SourceCategory is MCP, deduplicates
// by name (first occurrence wins), and — when maxTools > 0 and the result
// exceeds the budget — trims it down while guaranteeing that protected tools
// and MCP tools always survive.
func SelectTools(all []sdktools.ToolDescriptor, matchedNames, alwaysPresent []string, maxTools int) []sdktools.ToolDescriptor {
	// a. Build keep-set: union of alwaysPresent + matchedNames + protectedToolNames.
	keep := make(map[string]struct{}, len(matchedNames)+len(alwaysPresent)+len(protectedToolNames))
	for _, n := range matchedNames {
		keep[n] = struct{}{}
	}
	for _, n := range alwaysPresent {
		keep[n] = struct{}{}
	}
	for n := range protectedToolNames {
		keep[n] = struct{}{}
	}

	// b + c. Filter to keep-set / MCP and dedup by name.
	out := make([]sdktools.ToolDescriptor, 0, len(all))
	seen := make(map[string]struct{}, len(all))
	for _, d := range all {
		if _, dup := seen[d.Name]; dup {
			continue
		}
		_, nameKept := keep[d.Name]
		if nameKept || d.SourceCategory == sdktools.SourceCategoryMCP {
			out = append(out, d)
			seen[d.Name] = struct{}{}
		}
	}

	// d. Enforce the size budget, preserving protected + MCP tools.
	if maxTools > 0 && len(out) > maxTools {
		out = trimToBudget(out, maxTools)
	}
	return out
}

// trimToBudget shrinks result so its length is at most maxTools, never dropping
// a protected descriptor (a tool whose Name is in protectedToolNames or whose
// SourceCategory is MCP). Non-protected descriptors are trimmed, in input
// order, to fit the remaining budget. If the protected set alone already
// meets/exceeds the budget, only the protected descriptors are returned.
func trimToBudget(result []sdktools.ToolDescriptor, maxTools int) []sdktools.ToolDescriptor {
	protectedCount := 0
	for _, d := range result {
		if isProtected(d) {
			protectedCount++
		}
	}
	budget := maxTools - protectedCount
	if budget <= 0 {
		// Protected tools alone fill the budget; they always survive.
		out := make([]sdktools.ToolDescriptor, 0, protectedCount)
		for _, d := range result {
			if isProtected(d) {
				out = append(out, d)
			}
		}
		return out
	}
	out := make([]sdktools.ToolDescriptor, 0, maxTools)
	kept := 0
	for _, d := range result {
		if isProtected(d) {
			out = append(out, d)
			continue
		}
		if kept < budget {
			out = append(out, d)
			kept++
		}
	}
	return out
}

// isProtected reports whether a descriptor must survive the maxTools budget:
// MCP-sourced tools (user-installed) and the protected built-in base.
func isProtected(d sdktools.ToolDescriptor) bool {
	if d.SourceCategory == sdktools.SourceCategoryMCP {
		return true
	}
	_, ok := protectedToolNames[d.Name]
	return ok
}
