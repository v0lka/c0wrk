# Router

## Role

Classifies user requests by domain, complexity, and matched skills, and selects a model for the Conductor. The Router no longer determines an execution mode or triggers clarification — both are handled inside the Conductor loop via tool calls.

## Key Files

- `sdk/agent/router/router.go` — Router struct and Route method
- `core/router_adapter.go` — core adapter wrapping SDK router
- `core/prompts/router_system.md` — routing classification prompt

## Behavior

### Input

- User message (string)
- Available tools (grouped by priority tier)
- Conversation history (last N messages, default N=10)
- Available skill descriptors (name + description)

### Output: RoutingDecision

```json
{
  "domain": "code",
  "complexity": 3,
  "matched_skills": ["go-testing", "go-error-handling"]
}
```

The `needs_clarification` and `mode` fields present in the prior pipeline are removed. Clarification is a Conductor tool call (`ask_user`); execution mode is a Conductor decision (`delegate` or not).

### Domain Values

| Domain | Meaning | Compaction Strategy (Conductor context) |
| ------ | ------- | --------------------------------------- |
| `code` | File modifications, implementation | sliding_window |
| `research` | Information gathering, analysis | summarization |
| `general` | Mixed or unclear primary activity | sliding_window (hierarchical if complexity >= 4) |
| `mixed` | Explicitly mixed activities | sliding_window |

### Complexity Scale

The complexity score informs the Conductor's system-prompt guidance on whether to delegate. It is no longer mapped to a fixed step count (the Conductor chooses its own granularity).

| Level | Meaning | Conductor guidance |
| ----- | ------- | ------------------ |
| 1 | Trivial (single action) | Handle inline; do not delegate. |
| 2 | Simple (few actions, clear path) | Handle inline or delegate one task. |
| 3 | Medium (multiple actions, some exploration) | Delegate coherent units. |
| 4 | Complex (significant exploration + implementation) | Delegate; consider `declare_plan` first. |
| 5 | Large (multiple subsystems, extensive work) | Delegate; call `declare_plan` with `await_approval` for large roadmaps. |

### Skill Matching

The router prompt includes the full list of available skills (name + description). The LLM selects which skills are relevant to the current request. Selected skills are merged with user-specified skills (from `/skill` references in the message) via `mergeSkillNames()` — a deduplicated union where router-matched skills come first. The combined set is activated for the task (system prompt injection, tool policy overrides).

### Process

1. Build system prompt from `router_system.md` template
   - Replace `AVAILABLE-TOOLS` with grouped tool list
   - Replace `AVAILABLE-SKILLS` with formatted skill list
2. Construct messages: system + history (last `historyWindow`) + "Classify this request: {msg}"
3. Use reasoning effort set by orchestrator via `SetReasoningEffort()`
4. Call LLM
5. Extract JSON from response (handles markdown code blocks)
6. Parse RoutingDecision
7. Validate and clamp values

### Validation Rules

- Domain: must be one of {"code", "research", "general", "mixed"}; invalid → "general"
- Complexity: clamped to [1, 5]
- MatchedSkills: deduplicated; unknown skill names are retained in the RoutingDecision but filtered out during orchestrator skill activation

## Error Handling

- LLM call failure → return error (no fallback routing)
- JSON parse failure → one retry with repair prompt asking LLM to fix its JSON
- Second parse failure → return error

## Invariants

- Route always returns a valid RoutingDecision (no nil on success)
- Domain is always from the valid set after validation
- Complexity is always in [1, 5] after clamping
- Skill names in MatchedSkills are always deduplicated
- User-specified skills (from HandleOptions.UserSkills) are merged with router-matched skills in the orchestrator, not in the router
- When UserSkills is non-empty, the orchestrator augments the routing message with skill descriptions (via `buildSkillAugmentedRoutingMessage`) so the router classifies domain/complexity based on the skill's purpose
- Router never modifies tool registry or any state (pure classification)
- Router never produces a clarification decision — clarification is a Conductor responsibility via `ask_user`
- The orchestrator's continuation fast-path skips the router entirely when `opts.TaskID` is non-empty AND the restored blackboard has an existing routing decision

## Related Specs

- [README.md](README.md) — orchestration overview
- [conductor.md](conductor.md) — routing decision feeds the Conductor
- [../memory/compaction.md](../memory/compaction.md) — domain → strategy mapping
- [../../decisions/012-conductor-orchestration-pipeline.md](../../decisions/012-conductor-orchestration-pipeline.md) — rationale for removing mode/clarification
