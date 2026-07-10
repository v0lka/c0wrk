# Router

## Role

Classifies user requests by domain, complexity, and matched skills. The Router is a pure classification primitive: it produces a `RoutingDecision` and never mutates state. Downstream components (the host orchestrator, the planner, the context manager) consume the decision to select a compaction strategy, step profiles, and activated skills.

## Key Files

- `github.com/v0lka/sp4rk/agent/router` — `Router`, `Config`, `New`, `Route`, `RoutingDecision`, domain constants
- `github.com/v0lka/sp4rk/llm` — `LLMCaller`, `ExtractJSON`, `Message`
- `github.com/v0lka/sp4rk/prompt` — prompt builder (system prompt assembled with cache-aware substitutions)
- `github.com/v0lka/sp4rk/skills` — `SkillDescriptor` (minimal name+description used for matching)

## Behavior

### Input

`Route(ctx, userMessage, availableTools, history, availableSkills)` takes:

- the user message (string)
- available tools (`[]tools.ToolDescriptor`, grouped by priority tier for the prompt)
- conversation history (the last `HistoryWindow` messages, default 10)
- available skill descriptors (name + description)

### Output: RoutingDecision

```go
type RoutingDecision struct {
    Domain             string   // "code" | "research" | "general" | "mixed"
    Complexity         int      // 1-5
    NeedsClarification bool     // retained from the JSON contract
    MatchedSkills      []string // skills selected by the router
}
```

### Domain values

| Domain | Meaning | Compaction strategy (downstream) |
| ------ | ------- | -------------------------------- |
| `code` | File modifications, implementation, build/test | `sliding_window` |
| `research` | Information gathering, analysis | `summarization` |
| `general` | Mixed or unclear primary activity | `sliding_window` (`hierarchical` when complexity >= 4) |
| `mixed` | Explicitly mixed activities | `sliding_window` (`hierarchical` when complexity >= 4) |

### Complexity scale

Complexity (1–5) is advisory information for downstream components (e.g. host delegation guidance or planner step profiles). It is not mapped to a fixed step count by the Router itself.

### Skill matching

The router prompt includes the full list of available skills (name + description). The LLM selects which skills are relevant. `MatchedSkills` is deduplicated and trimmed during validation. Merging router-matched skills with explicitly user-activated skills is the host's responsibility, not the Router's.

### Process

1. Build the system prompt from the caller-supplied template. The template must contain `AVAILABLE-TOOLS` and `AVAILABLE-SKILLS` placeholders. Tool/skill lists and any project-context section are substituted via single-pass data substitution (placeholders inside these externally-influenced values are never expanded).
2. Construct messages: system + history (last `HistoryWindow`) + `"Classify this request: {msg}"`.
3. Apply the reasoning effort set via `SetReasoningEffort`.
4. Call the LLM.
5. Extract JSON from the response via `llm.ExtractJSON` (handles surrounding prose and markdown fences).
6. Unmarshal into `RoutingDecision`; validate and clamp.

### Validation rules

- Domain: must be one of `{"code", "research", "general", "mixed"}`; otherwise `"general"`.
- Complexity: clamped to `[1, 5]`.
- MatchedSkills: deduplicated, empty entries removed.

### Optional dependencies

- `SetModelRegistry(*llm.ModelRegistry)` — model metadata resolution.
- `Config.AppendContextSections` — a function producing additional prompt sections (e.g. project conventions) inserted via a `PROJECT-CONTEXT` placeholder; if the template lacks that placeholder the section is appended for backward compatibility.

## Error Handling

- **LLM call failure**: returns an error wrapping the failure (no fallback routing).
- **JSON parse failure**: one retry with a repair prompt asking the LLM to fix its JSON.
- **Second parse failure**: returns an error.

## Invariants

- `Route` always returns a valid `RoutingDecision` on success (never nil).
- Domain is always from the valid set after validation; complexity is always in `[1, 5]`.
- `MatchedSkills` is always deduplicated.
- The Router never modifies the tool registry or any other state — it is pure classification.

## Related Specs

- [README.md](README.md) — orchestration overview
- [planner.md](planner.md) — consumes domain/complexity to drive exploration vs direct planning
- [../memory/compaction.md](../memory/compaction.md) — domain → strategy mapping
- [../skills.md](../skills.md) — skill discovery and descriptors
