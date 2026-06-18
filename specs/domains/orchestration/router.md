# Router

## Role

Classifies user requests by domain, complexity, and matched skills to determine execution strategy.

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
  "needs_clarification": false,
  "matched_skills": ["go-testing", "go-error-handling"]
}
```

### Domain Values

| Domain     | Meaning                            | Compaction Strategy                              |
| ---------- | ---------------------------------- | ------------------------------------------------ |
| `code`     | File modifications, implementation | sliding_window                                   |
| `research` | Information gathering, analysis    | summarization                                    |
| `general`  | Mixed or unclear primary activity  | sliding_window (hierarchical if complexity >= 4) |
| `mixed`    | Explicitly mixed activities        | sliding_window                                   |

### Complexity Scale

| Level | Meaning                                            | Typical Plan Size |
| ----- | -------------------------------------------------- | ----------------- |
| 1     | Trivial (single action)                            | 1 step            |
| 2     | Simple (few actions, clear path)                   | 1-2 steps         |
| 3     | Medium (multiple actions, some exploration)        | 2-4 steps         |
| 4     | Complex (significant exploration + implementation) | 4-7 steps         |
| 5     | Large (multiple subsystems, extensive work)        | 6-10 steps        |

### Skill Matching

The router prompt includes the full list of available skills (name + description). The LLM selects which skills are relevant to the current request. Selected skills are merged with user-specified skills (from `/skill` references in the message) via `mergeSkillNames()` — a deduplicated union where router-matched skills come first. The combined set is activated for the task (system prompt injection, tool policy overrides).

### Process

1. Build system prompt from `router_system.md` template
   - Replace `AVAILABLE-TOOLS` with grouped tool list
   - Replace `AVAILABLE-SKILLS` with formatted skill list
2. Construct messages: system + history (last `historyWindow`) + "Classify this request: {msg}"
3. Use reasoning effort set by orchestrator via `SetReasoningEffort()` (native string, passed directly to provider)
4. Call LLM
5. Extract JSON from response (handles markdown code blocks)
6. Parse RoutingDecision
7. Validate and clamp values

### Validation Rules

- Domain: must be one of {"code", "research", "general", "mixed"}; invalid → "general"
- Complexity: clamped to [1, 5]
- MatchedSkills: deduplicated, unknown skills silently dropped

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
- When UserSkills is non-empty, the orchestrator augments the routing message with skill descriptions (via `buildSkillAugmentedRoutingMessage`) so the router classifies domain/complexity based on the skill's purpose, not just the stripped arguments. NeedsClarification is also suppressed as a safety net.
- Router never modifies tool registry or any state (pure classification)

## Related Specs

- [README.md](README.md) — orchestration overview
- [planner.md](planner.md) — planner uses routing domain for step assignment
- [../memory/compaction.md](../memory/compaction.md) — domain → strategy mapping
