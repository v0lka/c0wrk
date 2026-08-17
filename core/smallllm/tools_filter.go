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
// assembles their choices and enforces a slot budget on the router-matched
// portion.
//
// The tool population is split into two classes with different budget
// semantics:
//
//   - guaranteed (always-present ∪ protected ∪ MCP-sourced): NEVER trimmed.
//     These are explicit user/operator choices; dropping them would silently
//     break pinned workflows, the completion channel, or user-installed MCP
//     integrations.
//   - router-matched: fills the free slots left after the guaranteed set,
//     i.e. at most maxTools − len(guaranteed) tools, in registry order.
//
// Because guaranteed tools are never trimmed, the result can legitimately
// exceed maxTools when the guaranteed set alone is larger than the budget.
// Config-level validation (validateSmallLLMConfig in the backend) rejects
// such profiles up front; SelectTools itself never fails and never trims a
// guaranteed tool (defense in depth).
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
// lists and never consume trimmable capacity: the completion channel, the
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

// SelectTools assembles the small-LLM tool set from two classes of sources:
//
//   - guaranteed, never trimmed: the user's always-present pins, the protected
//     orchestration tools, and every MCP-sourced tool (user-installed).
//   - matchedNames: the tools the router selected for this task. When
//     maxTools > 0, at most maxTools − len(guaranteed) of them are kept,
//     filling the free slots in registry (input) order; maxTools <= 0 means
//     unlimited.
//
// The full descriptor list is swept once in registry order, deduplicated by
// name (first occurrence wins), and each surviving descriptor is classified as
// guaranteed or matched. Emission preserves registry order: every guaranteed
// descriptor, plus matched descriptors while free slots remain. When the
// guaranteed set alone meets or exceeds maxTools, zero matched tools are kept
// — and the guaranteed set is returned in full, even though its length then
// exceeds maxTools (see the package comment).
func SelectTools(all []sdktools.ToolDescriptor, matchedNames, alwaysPresent []string, maxTools int) []sdktools.ToolDescriptor {
	// a. Guaranteed name set: alwaysPresent ∪ protectedToolNames.
	guaranteedNames := make(map[string]struct{}, len(alwaysPresent)+len(protectedToolNames))
	for _, n := range alwaysPresent {
		guaranteedNames[n] = struct{}{}
	}
	for n := range protectedToolNames {
		guaranteedNames[n] = struct{}{}
	}
	matchedSet := make(map[string]struct{}, len(matchedNames))
	for _, n := range matchedNames {
		matchedSet[n] = struct{}{}
	}

	// b. Single registry-order sweep: dedup by name and classify each
	// descriptor as guaranteed (name in the guaranteed set or MCP-sourced) or
	// matched (router-selected, budgeted).
	type candidate struct {
		desc       sdktools.ToolDescriptor
		guaranteed bool
	}
	candidates := make([]candidate, 0, len(all))
	seen := make(map[string]struct{}, len(all))
	for _, d := range all {
		if _, dup := seen[d.Name]; dup {
			continue
		}
		isGuaranteed := d.SourceCategory == sdktools.SourceCategoryMCP
		if !isGuaranteed {
			_, isGuaranteed = guaranteedNames[d.Name]
		}
		if _, isMatched := matchedSet[d.Name]; !isGuaranteed && !isMatched {
			continue
		}
		seen[d.Name] = struct{}{}
		candidates = append(candidates, candidate{desc: d, guaranteed: isGuaranteed})
	}

	// c. Free slots for matched tools: the guaranteed population is counted
	// first and is never trimmed, so slots can legitimately be zero (or the
	// guaranteed set larger than the whole budget).
	freeSlots := len(candidates) // maxTools <= 0 → unlimited
	if maxTools > 0 {
		guaranteedCount := 0
		for _, c := range candidates {
			if c.guaranteed {
				guaranteedCount++
			}
		}
		freeSlots = maxTools - guaranteedCount
		if freeSlots < 0 {
			freeSlots = 0
		}
	}

	// d. Emit in registry order: guaranteed always, matched while slots last.
	out := make([]sdktools.ToolDescriptor, 0, len(candidates))
	used := 0
	for _, c := range candidates {
		if c.guaranteed {
			out = append(out, c.desc)
			continue
		}
		if used >= freeSlots {
			continue
		}
		out = append(out, c.desc)
		used++
	}
	return out
}

// HasRegisteredMatch reports whether at least one matched tool name refers to
// a tool actually present in the registry slice. An empty matched list — or a
// list of names that match nothing registered (hallucinated by the routing
// LLM) — yields false. Callers use this to detect a failed semantic tool
// selection (empty or invalid matched_tools) and fall back to the unfiltered
// tool set instead of narrowing to the guaranteed-only set, which would strip
// every file/exec tool from the Conductor.
func HasRegisteredMatch(matched []string, registry []sdktools.ToolDescriptor) bool {
	if len(matched) == 0 || len(registry) == 0 {
		return false
	}
	registered := make(map[string]struct{}, len(registry))
	for _, d := range registry {
		registered[d.Name] = struct{}{}
	}
	for _, name := range matched {
		if _, ok := registered[name]; ok {
			return true
		}
	}
	return false
}
