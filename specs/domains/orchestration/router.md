# Router

## Role

c0wrk classifies each user request by domain, complexity, and matched skills, then selects a model for the Conductor. Classification itself is a **sp4rk engine** primitive (`github.com/v0lka/sp4rk/agent/router`); c0wrk wraps it via `core/router_adapter.go` and consumes the `RoutingDecision` to drive skill activation, compaction selection, and the continuation fast-path. The Router no longer determines an execution mode or triggers clarification — both are handled inside the Conductor loop via tool calls.

The canonical classification algorithm (prompt, domain/complexity scales, skill matching, validation) is documented in [the sp4rk router spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/router.md).

## Key Files

- `core/router_adapter.go` — core adapter wrapping the sp4rk router
- `core/prompts/router_system.md` — routing classification prompt
- `core/orchestrator_handle.go` — invokes the router and applies c0wrk-specific routing policy (continuation fast-path, skill augmentation, No Project override)

Engine file: `github.com/v0lka/sp4rk/agent/router/router.go` (Router struct, `Route` method) — see [the sp4rk router spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/router.md).

## RoutingDecision (consumed by c0wrk)

```json
{
  "domain": "code",
  "complexity": 3,
  "needs_clarification": false,
  "matched_skills": ["go-testing", "go-error-handling"]
}
```

The `mode` field from the prior pipeline is removed (the Conductor decides execution granularity via `delegate` or not). The `needs_clarification` field **still exists** on the sp4rk `RoutingDecision` type, but c0wrk **ignores** it (`core/orchestrator_handle.go`: "Router.NeedsClarification is ignored: the Conductor decides when to ask") — clarification is a Conductor tool call (`ask_user`), not a routing-driven pipeline branch.

### Tool Matching (optional, gated on the Small-LLM profile)

When semantic tool selection is enabled (`coreRouter.SetToolMatching`, gated on `small_llm.enabled && small_llm.essential_tools.enabled` — see [../small-llm.md](../small-llm.md)), the router additionally classifies which tools are relevant to the task and returns them in `RoutingDecision.MatchedTools`. The router system prompt's `TOOL-MATCHING` and `JSON-OUTPUT-SCHEMA` placeholders are resolved conditionally based on this flag; when disabled both resolve to empty/default content and the router output carries no `matched_tools` field (behavior unchanged). `MatchedTools` feeds the Small-LLM essential-tools narrowing in `HandleMessage`. When the profile is off, `MatchedTools` is empty and unused.

### Domain → Compaction Strategy (c0wrk consumption)

c0wrk's Conductor selects a compaction strategy from `routing.Domain`:

| Domain | Meaning | Compaction Strategy (Conductor context) |
| ------ | ------- | --------------------------------------- |
| `code` | File modifications, implementation | sliding_window |
| `research` | Information gathering, analysis | summarization |
| `general` | Mixed or unclear primary activity | sliding_window (hierarchical if complexity >= 4) |
| `mixed` | Explicitly mixed activities | sliding_window |

### Complexity → Conductor Guidance (c0wrk consumption)

The complexity score informs the Conductor's system-prompt guidance on whether to delegate. It is no longer mapped to a fixed step count (the Conductor chooses its own granularity).

| Complexity band | Conductor guidance |
| --------------- | ------------------ |
| `<= 1` (simple) | Handle inline — read files, search, answer, call `finish`. No checklist, `delegate`, or plan needed. |
| `>= 2` | The Conductor decides whether to plan. Planning is RECOMMENDED (not required) when complexity is high (`> 3`) OR the task decomposes into a DAG of independent steps — `declare_plan` then `execute_plan`. Otherwise handle it plan-less: proceed inline or `delegate` coherent units to subagents. A plan-less path MUST open a checklist (`update_checklist` with empty `step_id`). |

Regardless of band, a global skill-prescribed clause overrides the bands when an active skill mandates an approval gate (`declare_plan` with `await_approval`), and the `>= 2` band carries the planning-vs-delegation orthogonality reminder (`declare_plan` and `delegate` must not be mixed).

## c0wrk Routing Policy

### Skill Merge

Selected (router-matched) skills are merged with user-specified skills (from `/skill` references in the message) via `mergeSkillNames()` — a deduplicated union where router-matched skills come first. The combined set is activated for the task (system prompt injection, tool policy overrides). When `HandleOptions.UserSkills` is non-empty, the orchestrator augments the routing message with skill descriptions (`buildSkillAugmentedRoutingMessage`) so the router classifies domain/complexity based on the skill's purpose.

### Continuation Fast-Path

When `opts.TaskID` is non-empty (a continuation) AND the restored blackboard has BOTH an existing plan AND a routing decision, the orchestrator **skips the router LLM call entirely**, reuses the prior `RoutingDecision`, and reactivates skills. The plan gate prevents a plan-less continuation from mis-routing (e.g. "continue step 10" without a plan would be misclassified). Every FIRST user message passes through `Route`; only continuations with a restored plan AND routing take the fast-path.

### No Project Mode

In No Project (CHAT) mode, `SetNoProjectMode()` disables only `semantic_search` — there is no vector index without a project, so the tool would fail or return stale results from the previous CODE-mode project. All other tools, including `ripgrep` and `glob`, stay available under the normal `security.groups` policies, and no extra shell-command blacklist is installed (dev/build commands such as `git` follow the regular `execute` group policy). The routing domain is honest: code-flavored CHAT questions classify as `"code"` — its compaction strategy (`sliding_window`) assumes no project — instead of being rewritten to `"general"`.

## Error Handling

- LLM call failure → return error (no fallback routing)
- JSON parse failure → one retry with repair prompt asking the LLM to fix its JSON
- Second parse failure → return error — except when the Small-LLM essential-tools/tool-matching profile is enabled (`smallLLMToolMatchingEnabled`): the orchestrator then continues with a default routing decision (`RoutingDecision{Domain: general, Complexity: defaultResumeComplexity}`, logged/emitted "Routing fallback: unparseable routing JSON — continuing with default routing") and the tool filter degrades to the full tool set. The unprofiled path errors as documented.

(Validation rules — domain clamping, complexity range, skill dedup — are engine behavior; see the sp4rk router spec.)

## Invariants

- Route always returns a valid `RoutingDecision` (no nil on success)
- The orchestrator's continuation fast-path skips the router entirely when `opts.TaskID` is non-empty AND the restored blackboard has BOTH an existing plan AND a routing decision
- The router never modifies the tool registry or any state (pure classification)
- c0wrk never branches on `RoutingDecision.NeedsClarification` (explicitly ignored in `orchestrator_handle.go`); clarification is a Conductor responsibility via `ask_user`. The field may still be set by the engine, but it drives no c0wrk pipeline branch.
- User-specified skills are merged with router-matched skills in the orchestrator, not in the router
- Tool matching (`MatchedTools`) is only emitted when the Small-LLM profile's essential-tools variant is active (`coreRouter.SetToolMatching`); when off, the router never modifies the tool set and `MatchedTools` is empty

## Related Specs

- [sp4rk router](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/router.md) — canonical classification algorithm, prompt, validation
- [README.md](README.md) — orchestration overview
- [conductor.md](conductor.md) — routing decision feeds the Conductor
- [../memory/compaction.md](../memory/compaction.md) — domain → strategy mapping
- [../small-llm.md](../small-llm.md) — tool matching consumed by the essential-tools narrowing
- [../../decisions/012-conductor-orchestration-pipeline.md](../../decisions/012-conductor-orchestration-pipeline.md) — rationale for removing mode/clarification
