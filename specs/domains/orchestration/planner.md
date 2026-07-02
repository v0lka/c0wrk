# Planner

## Role

Generates DAG execution plans from user tasks, assigns agent profiles to steps, and handles replanning after failures.

## Key Files

- `sdk/planner/planner.go` — Planner struct (Plan, Replan)
- `core/planner_adapter.go` — core adapter (PlanContinuation, skill threading, replan callback)
- `core/plan_serializer.go` — Plan ↔ Markdown serialization and structural validation
- `core/plan_review.go` — HandlePlanReview, PlanWithFeedback, SemanticValidatePlan
- `core/prompts/planner_base.md` — base planning prompt (MODE-TOT, MODE-GUIDANCE placeholders)
- `core/prompts/planner_informed.md` — informed planning prompt (after exploration)
- `core/prompts/planner_replan.md` — replanning prompt
- `core/prompts/planner_*.md` — provider-specific variants (anthropic, openai, gemini, etc.)
- `sdk/orchestration/dag.go` — Plan and PlanStep types

## Behavior

### Plan Generation Modes

```
User message + routing decision
         │
         ├─ Normal mode: Plan(singleStep=true)
         │   → LLM call with single-step ToT + guidance
         │   → Produces exactly 1 step (truncated if LLM returns more)
         │
         └─ Advanced mode: Plan(singleStep=false)
              │
              ├─ Complexity ≤ 3: Direct planning
              │   → Single LLM call generates DAG
              │
              └─ Complexity ≥ 4: Exploration-first
                  → Mini ReAct loop (up to MaxExploreSteps=7)
                  → Uses read-only tools to understand codebase
                  → Then generates informed plan
```

All modes receive full Tree of Thoughts reasoning and structured step descriptions (What/How/Where/Acceptance Criteria). All planning entry points (`Plan` and `PlanContinuation`) receive the conversation history: the plan-mode preambles (`planner_plan_preamble.md`, `planner_single_step_preamble.md`) and the continuation preamble substitute it into the `RECENT-CONVERSATION` placeholder, so follow-up requests (including the first message after a backend restart) are planned with dialogue context. The `planPromptMode` struct parameterizes mode-varying prompt segments with fields `preamble`, `tot`, `guidance`, `extraSections`, `tail`, `jsonExample`, and `maxSteps`:

| Mode Config              | maxSteps | ToT variant     | Guidance variant     | Used by                   |
| ------------------------ | ---------- | --------------- | -------------------- | ------------------------- |
| `multiStepMode`          | "10"      | multi-step ToT  | multi-step guidance  | Advanced Plan()           |
| `singleStepMode`         | "1"       | single-step ToT | single-step guidance | Normal Plan()             |
| `continuationMultiMode`  | "10"      | multi-step ToT  | multi-step guidance  | Advanced PlanContinuation |
| `continuationSingleMode` | "1"       | single-step ToT | single-step guidance | Normal PlanContinuation   |

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
    ID             string   // unique within plan (e.g., "step_1")
    Summary        string   // 5-7 word UI label
    Description    string   // detailed What/How/Where/Acceptance Criteria
    DependsOn      []string // step IDs this depends on
    Parallelizable bool     // can run in parallel with siblings
    EstimatedTools []string // hint (non-binding)
    Profile        any      // opaque; consumers type-assert to AgentProfile (core) or equivalent
}
```

### Agent Profiles

Agent profiles are defined in the `AgentProfile` struct (`core/types.go`) and described to the LLM planner via `planModeAgentProfiles` constant in `core/planner.go`:

| Role         | Primary Tools                                        | Compaction Bias        |
| ------------ | ---------------------------------------------------- | ---------------------- |
| `researcher` | search_graph, semantic_search, web_search, read_file | Keep research findings |
| `coder`      | write_file, edit_file, bash_exec, read_file          | Keep recent edits      |
| `tester`     | bash_exec, read_file, search_graph                   | Keep test results      |
| `executor`   | All tools (default)                                  | Balanced               |

Profile fields:

- `Role` — selects system prompt variant and pruning defaults
- `SystemPrompt` — per-step system prompt override (optional; empty = use role default)
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

1. Receive: original request, existing plan, completed steps, new message, singleStep
2. Find terminal steps of existing plan
3. Generate continuation steps (IDs prefixed `continuation_`)
4. When singleStep=true, exactly 1 continuation step is produced (truncated if LLM returns more)
5. New steps DependsOn terminal steps
6. Merged with existing plan

### Plan Review Mode

When `HandleOptions.PlanReview` is true, the orchestrator pauses after planning
instead of executing immediately:

```
HandleMessage(PlanReview=true)
  → HandlePlanReview()
      ├─ Planner.Plan() — generates DAG as usual
      ├─ SerializePlan() → markdown with # Step N: headers + **What**:**Where**:**How**:**Acceptance Criteria**: fields
      ├─ Write to .c0wrk/plans/<session_prefix>_<random6>.md
      ├─ Emit plan_generated event (step list for frontend)
      └─ Return HandleResult{PlanReviewPhase: "awaiting_accept", PlanReviewPath: path}
```

The serialized markdown omits hidden fields (DependsOn, Profile, EstimatedTools).
Only Summary and Description are user-visible. On approval, `MergePlanSteps()`
recombines user-edited content with original hidden fields by position.

### Feedback-Driven Replanning

When the user rejects a plan with feedback:

```
RejectPlan(sessionID, feedback)
  → PlanWithFeedback(originalMsg, previousPlanMD, feedback)
      ├─ Enriches user message: original request + previous plan + feedback
      ├─ Calls Planner.Plan() with enriched message (standard planning path)
      └─ Returns new Plan
  → SerializePlan() → save to NEW .md file
  → Emit plan_review_ready with new path
```

Previous .md files are not deleted — they remain as history in `.c0wrk/plans/`.

### Semantic Plan Validation

`SemanticValidatePlan(ctx, originalMessage, planMD)` uses an LLM call to evaluate
the edited plan against three dimensions:

- Coverage: does the plan address all aspects of the original request?
- Relevance: are any steps unnecessary or unrelated?
- Internal consistency: logical sequence, no contradictions, clean step boundaries

### Prompt Construction

The planner assembles prompts from template sections using a unified `buildSystemPromptFromMode` builder. Templates (`planner_base.md`, `planner_informed.md`) contain placeholders that are substituted with mode-appropriate content:

- `MODE-PREAMBLE` — role description + granularity rules (single vs multi-step)
- `MODE-TOT` — Tree of Thoughts reasoning block (single-step or multi-step variant)
- `MODE-GUIDANCE` — step format and granularity guidance (single-step or multi-step variant)
- `DOMAIN-ASSIGNMENT` — domain assignment guidance
- `AGENT-PROFILES` — agent profile descriptions
- `MODE-EXTRA-SECTIONS` — step description format (What/How/Where/Acceptance Criteria), parallelization rules, research decomposition
- `MODE-TAIL` — reflections (if replan), output expectations
- `MODE-JSON-EXAMPLE` — JSON output format example (single-step or multi-step variant)
- `AVAILABLE-TOOLS` — grouped tool list
- `AVAILABLE-SKILLS` — active skill list
- `WORKSPACE-PATH` — current workspace path

Provider-specific prompt variants are selected based on model family. The `maxSteps` value from `planPromptMode` is substituted into the fully assembled prompt (not the markdown template).

## Error Handling

- JSON parse failure → one retry with repair prompt
- LLM call failure → return error (orchestrator may retry at higher level)
- Exploration failure → fall back to direct planning (non-fatal)
- Invalid step IDs in DependsOn → validation error

## Invariants

- Plan steps always form a valid DAG (no cycles)
- Every step has a unique ID within the plan
- Every ID in DependsOn references an existing step ID
- Single-step mode (normal) always produces exactly 1 step (hard truncation enforced post-parse)
- All modes (single-step and multi-step) receive Tree of Thoughts reasoning and structured What/How/Where/Acceptance Criteria
- Continuation step IDs are prefixed with `continuation_`
- Planner never executes mutating tools during exploration phase
- Exploration uses a separate ContextManager (isolated from main execution)
- Active skill bodies are rendered verbatim in the planner prompt (no truncation); plan steps must reflect the full skill guidance the executor will receive
- Plan Review mode always serializes plans to `.c0wrk/plans/` as markdown before user review
- Serialized plans omit hidden fields (DependsOn, Profile, EstimatedTools); these are restored from the original plan on approval
- Plan markdown format uses `# Step N: <title>` headings with `**What**:`, `**Where**:`, `**How**:`, `**Acceptance Criteria**:` bold fields
- `MergePlanSteps()` pairs user-edited steps with original hidden fields by position (first parsed step ↔ first original step)
- `PlanWithFeedback()` enriches the user message with previous plan + feedback rather than using a separate prompt template
- Plan validation is two-phase: structural (Go, regex-based) then semantic (LLM call)

## Related Specs

- [README.md](README.md) — orchestration overview
- [executor.md](executor.md) — how steps are executed
- [router.md](router.md) — routing decision feeds into planning
