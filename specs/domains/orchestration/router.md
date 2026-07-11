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
  "matched_skills": ["go-testing", "go-error-handling"]
}
```

The `needs_clarification` and `mode` fields present in the prior pipeline are removed. Clarification is a Conductor tool call (`ask_user`); execution mode is a Conductor decision (`delegate` or not).

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

| Level | Meaning | Conductor guidance |
| ----- | ------- | ------------------ |
| 1 | Trivial (single action) | Handle inline; do not delegate. |
| 2 | Simple (few actions, clear path) | Handle inline or delegate one task. |
| 3 | Medium (multiple actions, some exploration) | Delegate coherent units. |
| 4 | Complex (significant exploration + implementation) | Delegate; consider `declare_plan` first. |
| 5 | Large (multiple subsystems, extensive work) | Delegate; call `declare_plan` with `await_approval` for large roadmaps. |

## c0wrk Routing Policy

### Skill Merge

Selected (router-matched) skills are merged with user-specified skills (from `/skill` references in the message) via `mergeSkillNames()` — a deduplicated union where router-matched skills come first. The combined set is activated for the task (system prompt injection, tool policy overrides). When `HandleOptions.UserSkills` is non-empty, the orchestrator augments the routing message with skill descriptions (`buildSkillAugmentedRoutingMessage`) so the router classifies domain/complexity based on the skill's purpose.

### Continuation Fast-Path

When `opts.TaskID` is non-empty (a continuation) AND the restored blackboard has an existing routing decision, the orchestrator **skips the router LLM call entirely**, reuses the prior `RoutingDecision`, and reactivates skills. Every FIRST user message passes through `Route`; only continuations with restored routing take the fast-path.

### No Project Override

In No Project (CHAT) mode, `routing.Domain` is overridden from `"code"` to `"general"` after classification, and code tools are disabled (`SetNoProjectMode()`).

## Error Handling

- LLM call failure → return error (no fallback routing)
- JSON parse failure → one retry with repair prompt asking the LLM to fix its JSON
- Second parse failure → return error

(Validation rules — domain clamping, complexity range, skill dedup — are engine behavior; see the sp4rk router spec.)

## Invariants

- Route always returns a valid `RoutingDecision` (no nil on success)
- The orchestrator's continuation fast-path skips the router entirely when `opts.TaskID` is non-empty AND the restored blackboard has an existing routing decision
- The router never modifies the tool registry or any state (pure classification)
- The router never produces a clarification decision — clarification is a Conductor responsibility via `ask_user`
- User-specified skills are merged with router-matched skills in the orchestrator, not in the router

## Related Specs

- [sp4rk router](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/router.md) — canonical classification algorithm, prompt, validation
- [README.md](README.md) — orchestration overview
- [conductor.md](conductor.md) — routing decision feeds the Conductor
- [../memory/compaction.md](../memory/compaction.md) — domain → strategy mapping
- [../../decisions/012-conductor-orchestration-pipeline.md](../../decisions/012-conductor-orchestration-pipeline.md) — rationale for removing mode/clarification
