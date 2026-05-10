# Planner

## Role

Generates DAG execution plans from user tasks, assigns agent profiles to steps, and handles replanning after failures.

## Key Files

- `core/planner.go` — Planner struct (Plan, Replan, PlanContinuation, CreateSyntheticPlan)
- `core/prompts/planner_base.md` — base planning prompt
- `core/prompts/planner_informed.md` — informed planning prompt (after exploration)
- `core/prompts/planner_replan.md` — replanning prompt
- `core/prompts/planner_*.md` — provider-specific variants (anthropic, openai, gemini, etc.)
- `sdk/orchestration/dag.go` — Plan and PlanStep types

## Behavior

### Plan Generation Modes

```
User message + routing decision
         │
         ├─ Normal mode: CreateSyntheticPlan()
         │   → Single step, no LLM call
         │
         └─ Advanced mode: Plan()
              │
              ├─ Complexity ≤ 3: Direct planning
              │   → Single LLM call generates DAG
              │
              └─ Complexity ≥ 4: Exploration-first
                  → Mini ReAct loop (up to MaxExploreSteps=7)
                  → Uses read-only tools to understand codebase
                  → Then generates informed plan
```

### Granularity Rules

| Complexity | Step Count |
| ---------- | ---------- |
| 1-2        | 1-2 steps  |
| 3          | 2-4 steps  |
| 4          | 4-7 steps  |
| 5          | 6-10 steps |

Hard cap: MAX-STEPS (configured, typically 10).

### PlanStep Structure

```go
type PlanStep struct {
    ID             string       // unique within plan (e.g., "step_1")
    Summary        string       // 5-7 word UI label
    Description    string       // detailed What/How/Where/Acceptance Criteria
    DependsOn      []string     // step IDs this depends on
    Parallelizable bool         // can run in parallel with siblings
    EstimatedTools []string     // hint (non-binding)
    Profile        AgentProfile // role, tools, domain, skills
}
```

### Agent Profiles

| Role         | Primary Tools                                        | Compaction Bias        |
| ------------ | ---------------------------------------------------- | ---------------------- |
| `researcher` | search_graph, semantic_search, web_search, read_file | Keep research findings |
| `coder`      | write_file, edit_file, bash_exec, read_file          | Keep recent edits      |
| `tester`     | bash_exec, read_file, search_graph                   | Keep test results      |
| `executor`   | All tools (default)                                  | Balanced               |

Profile fields:

- `Role` — selects system prompt variant and pruning defaults
- `AllowedTools` — restricts available tools (nil = all)
- `Skills` — restricts active skills (nil = full task-scope pool)
- `Domain` — controls compaction strategy for this step
- `MaxSteps` — per-step iteration budget (0 = use config default)
- `KeepLastN` — pruning override
- `ProtectedTools` — tools whose results are never pruned

### Domain Assignment Logic

Domain controls compaction strategy:

- **code**: step primarily modifies files → sliding window (keeps recent edits visible)
- **research**: step primarily reads and analyzes → summarization (condenses findings)
- **general**: mixed activities → sliding window (hierarchical if complexity >= 4)

### Exploration Phase (Complexity >= 4)

1. Create exploration ContextManager with model's context window
2. Run a mini Executor with read-only tools (search_graph, semantic_search, ripgrep, glob, read_file, list_directory, web_search, web_fetch)
3. Budget: `MaxExploreSteps` iterations (default: 7)
4. Circuit breaker defaults: repeat=3/5, truncation=3, parse_error=3
5. On completion: use exploration output as additional context for planning prompt
6. On failure: fall back to direct planning (non-fatal)

### Replanning (Replan)

When execution fails and Reflector produces a reflection:

1. Receive: original plan, completed steps, failed step, reflection
2. Call `BuildCarryForward()`: preserves completed work, marks failed step
3. Generate new plan that accounts for what already succeeded and what failed
4. New steps build on completed work (DependsOn completed step IDs)

### Continuation (PlanContinuation)

For follow-up messages in an existing task:

1. Receive: original request, existing plan, completed steps, new message
2. Find terminal steps of existing plan
3. Generate continuation steps (IDs prefixed `continuation_`)
4. New steps DependsOn terminal steps
5. Merged with existing plan

### Prompt Construction

The planner assembles prompts from template sections:

- Preamble (role description + granularity rules)
- Domain assignment guidance
- Agent profile descriptions
- Step description format (What/How/Where/Acceptance Criteria)
- Output expectations per role
- Research decomposition guidance
- Parallelization rules
- Available tools (grouped list)
- Available skills
- Reflections (if replan)
- JSON example for output format

Provider-specific prompt variants are selected based on model family.

## Error Handling

- JSON parse failure → one retry with repair prompt
- LLM call failure → return error (orchestrator may retry at higher level)
- Exploration failure → fall back to direct planning (non-fatal)
- Invalid step IDs in DependsOn → validation error

## Invariants

- Plan steps always form a valid DAG (no cycles)
- Every step has a unique ID within the plan
- Every ID in DependsOn references an existing step ID
- Synthetic plans always have exactly 1 step
- Continuation step IDs are prefixed with `continuation_`
- Planner never executes mutating tools during exploration phase
- Exploration uses a separate ContextManager (isolated from main execution)

## Related Specs

- [README.md](README.md) — orchestration overview
- [executor.md](executor.md) — how steps are executed
- [router.md](router.md) — routing decision feeds into planning
