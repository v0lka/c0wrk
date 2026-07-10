# Planner

## Role

Generates DAG execution plans from free-text tasks. A `Planner` takes a task and the available tools and produces an `*orchestration.Plan` — a directed acyclic graph of `PlanStep`s an orchestrator can execute step by step. It also regenerates plans after failures (`Replan`) and produces continuation plans for follow-up messages (`PlanContinuation`).

## Key Files

- `github.com/v0lka/sp4rk/planner` — `Planner`, `NewPlanner`, `Config`, `DefaultConfig`, `PromptSet`, `AgentProfile`, `Plan`/`Replan`/`PlanContinuation` methods
- `github.com/v0lka/sp4rk/orchestration` — `Planner` interface (satisfied by `planner.Planner`), `Plan`, `PlanStep`, `CompletedStep`, `Reflection`
- `github.com/v0lka/sp4rk/agent` — `LLMCaller`, `ContextManager`, exploration `Executor`
- `github.com/v0lka/sp4rk/skills` — `SkillDescriptor`
- `github.com/v0lka/sp4rk/llm` — `ModelRegistry`, `TokenCounter`

## Behavior

The planner supports two strategies:

- **Direct planning** — a single LLM call produces the plan. Used for simple or general-domain tasks, or when no exploration tools are configured.
- **Informed (exploration) planning** — the planner first runs a short read-only ReAct loop with exploration tools to gather context about the codebase, then produces a plan informed by what it found. Used for complex, code-domain tasks.

### Config

`Config` holds all planner configuration. It separates stable SDK interfaces from host wiring (context-extraction functions, formatters, and appenders the host application provides). Notable fields:

| Field | Purpose |
| ----- | ------- |
| `Prompts` | All parameterizable prompt templates (`PromptSet`). |
| `DomainFromContext` / `ComplexityFromContext` | Extract routing domain/complexity from `ctx`. Drives the exploration-vs-direct decision. |
| `UserSkillsFromContext` / `FormatSkillList` | Extract/format activated skills. |
| `FormatWorkspacePath` / `AppendContextSections` | Workspace instruction block and appended context sections. |
| `ToolRegistry` | Provides tools for the exploration executor and tool listing. Must combine `agent.ToolExecutor` + a `ToolLister`. |
| `PlannerToolNames` | Set of tool names allowed for exploration. **Empty means no exploration tools are available** — the planner falls back to direct planning. |
| `ModelRegistry` / `Model` | Resolve model metadata/family for the exploration executor. |
| `ContextFactory` | Creates a `ContextManager` for the exploration loop. When nil, falls back to direct planning. |
| `CallerForStep` | Returns a step-local `LLMCaller` for a given context manager + step ID (independent context trackers). |
| `MaxExploreSteps` | Step budget for the exploration loop. Defaults to `7`. |
| `ReasoningEffort` | Applied to the exploration executor and plan-generation calls. |

`DefaultConfig()` returns sensible standalone defaults (no-op context functions, `MaxExploreSteps` of `7`); override `Prompts`, `Model`, and (for exploration) `ToolRegistry` + `PlannerToolNames` + `ContextFactory`.

### PromptSet

`PromptSet` holds all parameterizable templates the host injects: `BasePrompt`, `InformedPrompt`, `ReplanPrompt`; mode sections (single-step/multi-step/continuation variants); shared sections (`DomainAssignment`, `AgentProfiles`, `ExtraSections`); a `FamilyPrompt` adapter keyed by agent role + model family; and a `VerificationMandate` appended to all planner prompts.

The placeholder system substitutes keys iteratively (up to a pass cap) so nested placeholders (e.g. a preamble containing another placeholder) resolve correctly; values from external input are substituted in a single pass to prevent placeholder injection.

### AgentProfile

`AgentProfile` is the step-level configuration carried in `PlanStep.Profile` (typed as `any` on the plan, deserialized by consumers). It selects a system-prompt variant, pruning defaults, allowed tools, and reasoning behavior per step (e.g. `coder`, `researcher`, `tester`, `executor`).

### Methods

- **`Plan(ctx, task, availableTools, reflections, availableSkills, singleStep, conversationHistory)`** — generate a DAG for a new task. Sets `Plan.ExplorationContext` to a summary of any exploration performed.
- **`Replan(ctx, originalPlan, completed, failedStep, reflection, sessionReflections, availableSkills)`** — regenerate the plan after a step failure, preserving successfully completed work where possible (see `orchestration.BuildCarryForward`).
- **`PlanContinuation(ctx, originalRequest, existingPlan, completedSteps, newMessage, …, taskComplete)`** — produce a continuation plan for a follow-up message that reuses completed steps.

### Exploration vs direct planning

Exploration runs when `domain == code`, complexity is high, and both `PlannerToolNames` and `ContextFactory` are configured. Otherwise direct planning is used. The exploration executor is a normal `agent.Executor` limited to the read-only `PlannerToolNames`; its findings seed the plan's `ExplorationContext`.

### Single-step vs multi-step mode

`singleStep == true` selects single-step prompting (one-step plan) — used when the host has decided the task does not warrant decomposition. Otherwise multi-step prompting produces the full DAG.

## Error Handling

- **`NewPlanner` with a nil caller**: returns an error (caller is required).
- **LLM call failure / unparseable plan JSON**: `Plan`/`Replan`/`PlanContinuation` return an error; the orchestrator may retry or surface the failure.
- **No exploration tools**: silently falls back to direct planning (not an error).

## Invariants

- `planner.Planner` satisfies `orchestration.Planner` (compile-time check `var _ orchestration.Planner = (*Planner)(nil)`).
- When `PlannerToolNames` is empty, no exploration runs regardless of other config.
- `MaxExploreSteps` is at least `7` (defaulted when zero or negative).
- `Replan` preserves successfully completed steps that still appear in the new plan and whose dependencies are all carried forward.
- `PlanStep.Profile` is `any` on the wire; consumers convert it to a domain profile (e.g. `*planner.AgentProfile`).

## Related Specs

- [README.md](README.md) — orchestration overview
- [router.md](router.md) — domain/complexity drive the planning strategy
- [reflector.md](reflector.md) — reflections feed `Replan`
- [conductor.md](conductor.md) — the executor that runs each generated `PlanStep`
- [../memory/blackboard.md](../memory/blackboard.md) — `BuildCarryForward` / `CompletedStep` carry-forward semantics
