# ADR-035: Remove the Small-LLM Tool Budget, Router Tool Matching, and Tool Chat Cards

## Status

Accepted (partially supersedes [ADR-022](./022-small-llm-profile.md) — only the Essential Tools variant's `max_tools` budget, router tool matching, and the `tools_assigned` / over-budget diagnostics; every other ADR-022 decision stands).

## Context

ADR-022 introduced the Essential Tools variant with a `max_tools` slot budget: the router semantically matched task-relevant tools, and at most `max_tools − len(guaranteed)` of them joined the static core (operator pins ∪ protected orchestration tools ∪ every MCP tool). The guaranteed set was never trimmed, so whenever connected MCP servers pushed the guaranteed set past `max_tools`, the final set legitimately exceeded the budget — surfaced by a per-task `small_llm_tool_budget_overflow` service diagnostic and a `tools_assigned` chat card.

Field experience showed this design mismatched how the feature is actually used:

- The budget only constrained router-matched additions; in practice operators pin everything they need in `always_present`, leaving zero free slots. The budget then does nothing except trigger the overflow diagnostic on every task — steady-state noise that reads like a failure ("Tool budget overflow: 29 tools assigned against a budget of 13") even though nothing was trimmed.
- A partially-trimmed MCP server is worse than a fully-exposed one: cutting half of one server's tools makes the integration effectively useless. The never-trim rule already prevented this, but the budget concept implied otherwise and required a diagnostic to explain itself.
- The per-task chat cards carried no actionable information for a deterministic, static selection.

## Decision

With the Essential Tools variant active, the Conductor's advertised tool set is a **static union**: `always_present` ∪ protected orchestration tools ∪ every MCP-sourced tool ∪ turn-scoped extra guarantees (e.g. `delegate` on an explicit `#agent` mention), emitted in registry order. Specifically:

- `small_llm.essential_tools.max_tools` is removed from the config surface (backend config, builder mirror, RPC DTO, settings UI). A stale `max_tools` key in an existing `config.yaml` is silently ignored by the non-strict YAML loader.
- Router semantic tool matching is removed from the production wiring: `coreRouter.SetToolMatching(...)` is no longer called, the routing prompt carries no tool-selection section, and `RoutingDecision.MatchedTools` is no longer consumed by the orchestrator. The routing-parse degradation path (unparseable routing JSON under the profile → default routing) is retained.
- The filter emits nothing: no `tools_assigned` event (removed from the emitter, event catalog, and the frontend live handler), no budget diagnostics. The frontend keeps reconstructing historical `tools_assigned` status messages already persisted in old sessions.
- `core/smallllm.SelectTools` loses the `matchedNames`/`maxTools` parameters and `HasRegisteredMatch`; `compact_descriptions` still applies to the selection.

## Consequences

- The settings UI loses the "Max tools" field; the Essential Tools section documents that the assigned set equals the selection plus every connected MCP server's tools.
- Config validation loses the `max_tools` checks and the save-time cap reconciliation (`reconcileSmallLLMCap`); `validateSmallLLMConfig` retains the remaining variant checks (sampling, loop hardening, context).
- Tool-set size is governed entirely by the operator's selection and installed MCP servers; the 10–20-tool selection-accuracy guidance from `docs/small-llm-defaults-research.md` is operator-side advice, not an enforced guard.
- The router spends no prompt tokens on tool inventory/matching instructions when the profile is on.
- The guaranteed-set WARNING in the previous spec revision (MCP inflation vs. budget) is moot by construction: there is no budget to overflow.

## Alternatives Considered

- **Keep the budget, silence the diagnostic when nothing was trimmed.** Rejected: the budget's only lever (capping router-matched additions) was unused in practice, and the semantic-matching channel added prompt overhead in the router for a selection the operator had already pinned statically.
- **Keep router matching with an unlimited budget.** Rejected: the tool list would then depend on per-task router output, contradicting the requirement that the assigned set be determined by the Essential Tools selection; it also keeps the routing-prompt tool-inventory overhead.
