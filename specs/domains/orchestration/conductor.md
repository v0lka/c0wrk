# Conductor

## Role

A single `Executor.Run` instance that owns a user task end-to-end, using the ReAct loop to think, call tools (including `delegate` for subagents and `declare_plan` for roadmaps), and finish. The Conductor is the only top-level execution entry point in the orchestration domain.

## Key Files

- `core/conductor.go` — Conductor entry point: builds system prompt, assembles tool set, injects Delegation Registry + ChecklistGuard into context, manages inlineStepLifecycle, launches `Executor.Run`
- `core/tools/declare_step_complete.go` — `declare_step_complete` tool (inline plan-step completion signal)
- `core/tools/execute_plan.go` — `execute_plan` tool (runs declared plan steps in DAG order)
- `github.com/v0lka/sp4rk/agent/workspace.go` — `ChecklistGuardFunc` type + context helpers (`WithChecklistGuard`, `ChecklistGuardFromContext`)
- `github.com/v0lka/sp4rk/tools/builtins/checklist.go` — `update_checklist` tool (consults `ChecklistGuardFunc` before emitting update)
- `core/orchestrator_handle.go` — HandleMessage body that invokes the Conductor after routing
- `github.com/v0lka/sp4rk/agent/executor.go` — `Executor.Run` (the ReAct loop; the Conductor is an Executor configured with Conductor-specific tools and prompt)
- `core/systemprompt.go` — `buildSystemPrompt` and Conductor-specific prompt sections
- `core/prompts/shell_substitute.go` — `SubstituteShellTool` resolves the `{shell_tool}` placeholder in embedded prompt markdown to the active platform's shell-exec tool name (`bash_exec` on Unix, `posh_exec` on Windows); applied at each prompt-assembly call site so embedded prompts stay platform-agnostic raw templates
- `core/delegation_registry.go` — Delegation Registry injected into the Conductor context

## Behavior

### Lifecycle

```
Conductor.Run(ctx, message, routing, activeSkills, opts)
│
├─ 1. Build system prompt
│     ├─ Core: orchestrator system + family overlay + verification mandate
│     │    (orchestrator system + plan context have their `{shell_tool}` placeholders
│     │     resolved to the active platform's shell tool via prompts.SubstituteShellTool)
│     ├─ Workspace + temp dir
│     ├─ Environment block
│     ├─ Vector search hints
│     ├─ Active skills (verbatim bodies, no truncation)
│     └─ Conductor guidance section:
│          - when to delegate vs handle inline
│          - declare_plan before large implementations
│          - ask_user when requirements are unclear
│          - reflect when trajectory looks wrong
│
├─ 2. Assemble tool set
│     ├─ File ops, search, web (filtered by routing domain + No Project)
│     ├─ Internal tools (always present):
│     │    ask_user, finish, store_fact, search_facts,
│     │    update_checklist, declare_step_complete, read_step_output,
│     │    list_step_outputs, read_final_result, semantic_search
│     └─ Conductor tools (always present):
│          delegate, declare_plan, execute_plan, reflect, cancel_delegation
│
├─ 3. Create ContextManager via contextFactory
│
├─ 4. Inject Delegation Registry into context
│     (lives for the duration of this Conductor run)
│
├─ 5. executor.Run(ctx, conductorTools, cm)
│     │
│     │  The Conductor thinks and acts in a loop:
│     │
│     │  - reads files, searches codebase → understanding
│     │  - calls ask_user → clarifies requirements
│     │  - calls declare_plan → publishes roadmap, optionally blocks for approval
│     │  - calls execute_plan → runs all plan steps in DAG order with parallelism
│     │  - calls delegate → launches subagent(s), awaits results
│     │  - calls reflect → invokes Reflector on current trajectory
│     │  - calls finish → ends the task with a final answer
│     │
│     └─ Returns ExecutorResult { Output, Steps, Finished }
│
├─ 6. On finish: join pending async delegations (or require cancel_delegation first)
│
└─ 7. Return ExecutionResult { Output, Status, Blackboard, Delegations }
```

### Decision Heuristics (system-prompt guidance)

The Conductor chooses how to handle a task based on its system prompt, not on a structural mode switch:

| Signal | Guidance |
| ------ | -------- |
| Task is a single read/answer | Handle inline; do not call `delegate`. |
| Task involves multiple files or subsystems and warrants user sign-off | Call `declare_plan` with `mode: "await_approval"`, then `execute_plan` to run the steps. |
| Plan-less task needs context optimization | Call `delegate` with one task per coherent unit of work. |
| Requirements are ambiguous | Call `ask_user` before delegating or implementing. |
| An interactive skill is active and prescribes an approval gate | Call `declare_plan` with `mode: "await_approval"` before implementing; after approval, run `execute_plan` to execute the steps. |
| Trajectory looks wrong after several delegations | Call `reflect`; act on the SuggestedAction (retry, replan, abort). |
| A delegation failed | Call `reflect` on the failed trajectory, or retry with a revised task. |

These are soft heuristics. The Conductor follows them via instruction-following, not via structural enforcement. See [../../decisions/012-conductor-orchestration-pipeline.md](../../decisions/012-conductor-orchestration-pipeline.md#skill-enforcement-resolved-structurally) for the rationale.

### Skill Rendering

Active skill bodies are injected verbatim into the Conductor system prompt via `formatActiveSkills` (unchanged from the prior pipeline). The Conductor can follow interactive skill instructions because it has the tools to do so: `ask_user` for clarifications, `declare_plan` for approval gates, `delegate` for decomposition. No agent-specific metadata is attached to the skill — portability is preserved.

### Context Management

The Conductor uses the same `ContextManager` and compaction strategies as the prior executor. For long tasks, the Conductor is expected to delegate heavy investigation to subagents (which carry their own context); the Conductor context then holds only summaries and decisions, keeping it bounded. The compaction strategy is selected from `routing.Domain`:

| Domain | Strategy |
| ------ | -------- |
| `code` | sliding_window |
| `research` | summarization |
| `general` | sliding_window (hierarchical if complexity >= 4) |
| `mixed` | sliding_window |

### Conversation History Injection

When launched from `HandleMessage` (new message or continuation), the
Conductor receives the recent conversation history (last
`ConductorHistoryWindow` messages, default 20) via `SetPriorConversation`
on the ContextManager. The history appears in the prompt between the system
message(s) and the current task content, giving the LLM dialogue context for
follow-up messages. Without this, a follow-up like "implement variant a"
would have no referent — the Conductor would see only the current message.

`Resume` does NOT inject conversation history: the Conductor continues the
same interrupted task, the original request is the task message, and the
restored blackboard carries the task state (plan, step results, facts).

Instead of conversation history, `Resume` seeds the persisted ReAct
**trajectory** (`resumeSteps`) into the ContextManager via its optional
`StepSeedable.SeedSteps` capability and into the Executor via the
`WithResumeSteps` option. The seeded steps render as assistant+tool messages in
the prompt, the step counter continues from `len(resumeSteps)+1`, and the full
trajectory (seeded + new steps) syncs to the TrajectoryStore on every step. A
routing decision and a plan are **optional** — routing is reused if persisted
(otherwise the `general` domain), and a plan-less task runs the standalone
checklist.

### Step Limit and Circuit Breakers

The Conductor's ReAct iteration limit is derived from routing complexity, not configured: `complexity × stepsPerComplexity` (constant 20 in `core/conductor.go`), giving a budget of 20 (complexity 1) to 100 (complexity 5). The complexity is read from the routing context (`ComplexityFromContext`). The Conductor is subject to the same circuit breakers as any `Executor.Run` instance (repeat, truncation, parse error, fruitless, same tool). On step-limit or circuit-breaker abort, `HITLHandler.OnStepLimit` is called with the same three options (AllowOnce, AllowAlways, Deny) as the prior executor. See [executor.md](executor.md) for circuit breaker details.

### Wrap-Up Nudge

A generalization of the existing `handleWrapUpNudge` heuristic nudges the Conductor when it has performed many tool calls without finishing:

- After N tool calls without `delegate` or `finish`: "This task looks large. Consider calling `delegate` to break it into subtasks, or `finish` if you are done."
- After a `delegate` returns: "Review the subagent's output. If the task is complete, call `finish`. If more work is needed, delegate the next unit or continue inline."

These are soft nudges, not enforcement.

### Inline Step Lifecycle

When the Conductor executes a declared plan inline (without delegating to subagents), plan-step lifecycle is managed by `inlineStepLifecycle` in `core/conductor.go`:

- **PlanStepStart** is inferred from the first `update_checklist(step_id=X)` call. No separate start tool is needed.
- **PlanStepComplete** is emitted by the `declare_step_complete` tool (explicit signal). The Conductor calls it after finishing an inline step.
- **Finish fallback**: after `executor.Run` returns, `inlineStepLifecycle.completeAll()` auto-completes any steps that were started but not explicitly completed via `declare_step_complete`. This prevents steps from being stuck in "running" state. The fallback only sweeps steps of a plan **declared in the current run** (`planDeclaredInRun()`); a plan restored from a previous (completed) task is ignored, so a plan-less continuation does not synthesize terminal events for the old task's steps.

This inline lifecycle governs only steps the Conductor executes itself. When the Conductor instead calls **`execute_plan`**, each step runs as a parallel subagent via `RunSubAgentsParallel`, and plan-step lifecycle is driven by the `planStepEventTranslator` — not by `inlineStepLifecycle`. The translator emits `PlanStepStart` / `PlanStepComplete` on the root emitter (adapting `SubAgentLaunch` / `SubAgentComplete`). To prevent `completeAll()` from double-completing steps already finished by `execute_plan`, the `inlineStepLifecycle.markCompleted(stepID)` records each completed step; `completeAll()` skips any step already marked completed. Steps that never launched (unsatisfiable dependencies or `SubAgentTask` build failure) never trigger the translator, so `Execute` / `defaultPlanStepWave` emit a synthesized `PlanStepStart` + `PlanStepComplete(success=false)` pair directly before marking them — ensuring they are not left stuck "pending". `execute_plan` is idempotent: it runs at most once per declared plan, so a retry requires publishing a new plan. It also refuses a plan restored from a previous (completed) task: the guard consults `planRunState` (fresh per run), so a restored plan — not declared in the current run — is rejected with an error directing the Conductor to publish a new plan or use `delegate`.

Checklist updates (`update_checklist`) are purely observational — they emit `step_todo_update` events but do not drive plan-step lifecycle. The checklist and plan-step lifecycle are decoupled: the checklist tracks work-in-progress **sub-tasks within a single step** (e.g. "read file X", "modify function Y", "run tests"); `declare_step_complete` marks the step done. A checklist must NOT list plan steps as its items — the plan panel already tracks plan steps, and duplicating them as checklist items is an error.

A standalone checklist (no `step_id`) is emitted when the Conductor calls `update_checklist` without a declared plan. The `step_todo_update` event carries an empty `step_id`, and the frontend renders it as a first-class `DisplayItem.kind='checklist'` card in the chat (sinking to the end while active).

### Checklist Guard

The Conductor installs a `ChecklistGuardFunc` into the context (`agent.WithChecklistGuard`) that rejects standalone (empty `step_id`) `update_checklist` calls once a plan is declared **in the current Conductor run**. The guard consults `launcher.HasDeclaredPlan()`, which reads a per-run `planRunState` flipped by `conductorPublisher.Publish` — NOT the raw blackboard plan. A plan restored from a previous (completed) task does NOT trip the guard: on a continuation, the new Conductor run starts with a fresh `planRunState` (false), so the continuation is free to act plan-less (standalone checklist), declare its own plan, or delegate. The guard returns a rejection message instructing the agent to pass a `step_id` and to not list plan steps as checklist items. This enforces the conceptual separation between plan-level tracking (`declare_plan` / plan panel) and step-level sub-task tracking (`update_checklist`).

The guard is consulted in `UpdateChecklistTool.Execute` after parsing succeeds and before the update callback is invoked. Subagents inherit the guard via context, but it is inert for them because subagent `step_id` is always set (inferred from context by `RunSubAgent`).

## Error Handling

- **LLM call failure** in the Conductor loop: propagated as an `Executor.Run` error; the orchestrator records the failure on the blackboard.
- **Delegation failure** (subagent error or `Finished: false`): returned as the `delegate` tool result with `isError: true`; the Conductor decides whether to retry, reflect, or finish. This replaces the prior outer retry loop.
- **Context cancelled** (user pressed Stop): propagated immediately; pending async delegations are cancelled via context-tree cancellation.
- **Conductor step limit reached without finish**: `Finished: false`; the orchestrator records `Status: partial` (resumable).

## Invariants

- Exactly one Conductor `Executor.Run` instance is active per task at any time.
- The Conductor tool set always includes `delegate`, `declare_plan`, `execute_plan`, `reflect`, `cancel_delegation`, `ask_user`, `finish`, `update_checklist`, `declare_step_complete`, `read_step_output`, `list_step_outputs`, `read_final_result`, and `search_facts`, regardless of routing domain, skill policy overrides, or No Project mode. These are internal tools and bypass policy.
- The Conductor installs a `ChecklistGuardFunc` that rejects standalone (empty `step_id`) `update_checklist` calls once a plan is declared **in the current run**; a standalone checklist is only valid for plan-less tasks. A restored plan from a previous (completed) task does NOT count as declared — the guard consults a per-run `planRunState`, not the raw blackboard plan.
- When a plan is declared **in the current run**, `delegate` is disabled (PlanChecker guard via `launcher.HasDeclaredPlan()`); `execute_plan` is the only execution path for plan steps. A restored plan does not disable `delegate`.
- Active skill bodies are rendered verbatim in the Conductor system prompt (no truncation).
- The Conductor context is isolated from subagent contexts: subagents carry their own `ContextManager`, and only their summaries return to the Conductor as tool results.
- The Delegation Registry is scoped to a single Conductor run; it is injected into the context at launch and does not outlive the run.
- `finish` with pending async delegations requires either a prior `cancel_delegation` for each pending ID, or an implicit join (the Conductor waits for all pending delegations before finishing).
- The Conductor never receives a fully-specified plan step as a task message (unlike the prior executor). Its task message is the user's original message plus routing context; decomposition is the Conductor's own decision via `delegate`.

## Related Specs

- [sp4rk Conductor](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/conductor.md) — canonical engine Conductor (single-loop task owner)
- [sp4rk Executor](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/executor.md) — canonical ReAct loop primitive (circuit breakers, gates, truncation)
- [README.md](README.md) — orchestration overview
- [delegation.md](delegation.md) — delegate tool and Delegation Registry
- [executor.md](executor.md) — ReAct loop (the primitive the Conductor is built on)
- [router.md](router.md) — routing decision feeds the Conductor
- [../../contracts/conductor-tools.md](../../contracts/conductor-tools.md) — Conductor tool surface contract
- [../../decisions/012-conductor-orchestration-pipeline.md](../../decisions/012-conductor-orchestration-pipeline.md) — architectural decision
