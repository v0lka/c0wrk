# Contract: Conductor Tools

## Boundary Rule

Conductor tools (`delegate`, `declare_plan`, `execute_plan`, `reflect`, `cancel_delegation`) are internal tools registered in `core/tools/registry.go` and added to the Conductor's tool set in `core/conductor.go`. They are always allowed (bypass policy and judge) and are never available to regular plan-step executors — only to the Conductor and, selectively, to subagents with `allow_redelegate: true`.

Planning and delegation are **orthogonal** mechanisms:
- **Planning** (`declare_plan` + `execute_plan`) manages task complexity and gets user sign-off. `execute_plan` runs all plan steps in DAG order with parallelism, emitting `plan_step_start`/`plan_step_complete` events.
- **Delegation** (`delegate`) optimizes Conductor context usage and session time for plan-less tasks. It emits `subagent_launch`/`subagent_complete` events.
- They must never be mixed: once a plan is declared, `delegate` is disabled (enforced by a `PlanChecker` guard) and `execute_plan` is the only execution path for plan steps.

## Interfaces

| Interface | Package | Consumed By | Purpose |
| --------- | ------- | ----------- | ------- |
| `delegate` tool | `core/tools` | Conductor | Launch subagents with DAG + async (plan-less tasks only) |
| `cancel_delegation` tool | `core/tools` | Conductor, subagent (redelegate) | Cancel a pending/running async delegation |
| `declare_plan` tool | `core/tools` | Conductor | Publish a roadmap to the blackboard and UI; optionally block for user approval |
| `execute_plan` tool | `core/tools` | Conductor | Execute all steps of the declared plan in DAG order with parallelism; emits plan-step events via emitter adapter |
| `reflect` tool | `core/tools` | Conductor | Invoke the Reflector on the current trajectory or a sub-task trajectory |
| `DelegationRegistry` | `core/tools` | `delegate`, `cancel_delegation`, `read_step_output`, `finish` (via context) | Track active/completed delegations for one Conductor run |
| `RunSubAgent` / `RunSubAgentsParallel` | `github.com/v0lka/sp4rk/agent` | `delegate` tool, `execute_plan` tool | Execute an isolated `Executor.Run` in a goroutine |
| `PlanStepExecutor` | `core/tools` | `execute_plan` tool | Execute the declared plan's steps in DAG order (implemented by `conductorLauncher`) |
| `PlanChecker` | `core/tools` | `delegate` tool | Reports whether a plan is declared (enforces orthogonality guard) |
| `Reflector.Reflect` | `github.com/v0lka/sp4rk/agent/reflector` | `reflect` tool | Produce a Reflection (root cause, action plan, suggested action) |
| `SerializePlan` | `core` | `declare_plan` tool | Serialize a Plan to markdown for UI display and plan-file persistence |
| `Plan` / `PlanStep` / `FindReadySteps` | `github.com/v0lka/sp4rk/orchestration` | `delegate`, `declare_plan`, `execute_plan` tools | DAG data structures and traversal |

## Initialization

Conductor tools are registered at startup in `core/tools/builtin_registration.go` alongside the existing internal tools. They are added to the `internalTools` set in `core/tools/registry.go`:

```go
var internalTools = map[string]struct{}{
    // ... read/write + lifecycle helpers (ask_user, finish, list_step_outputs,
    //     read_final_result, read_skill_resource, read_step_output, read_attachment,
    //     search_facts, tool_result_read, semantic_search, update_checklist,
    //     declare_step_complete, store_fact) ...
    "delegate":           {},
    "cancel_delegation":  {},
    "declare_plan":       {},
    "execute_plan":       {},
    "reflect":            {},
    // goal-mode-only (also in goalModeTools; offered to the agent only during a
    // goal loop — stripped on the non-goal path):
    "propose_goal":         {},
    "declare_goal_status":  {},
    "declare_verification": {},
    // batch composite tool (sdktools.ToolBatch):
    sdktools.ToolBatch:     {},
}
```

The Conductor tool set is assembled in `core/conductor.go`:

```
conductorTools = filterByDomain(fileOps + search + web) +
                 internalTools +
                 { delegate, declare_plan, execute_plan, reflect, cancel_delegation }
```

Subagent tool sets are assembled in the `delegate` tool based on the `tools` field of each task, plus internal tools, plus `delegate` + `cancel_delegation` only when `allow_redelegate: true`.

## Data Flow Across Boundary

### `delegate`

```
Conductor (ReAct loop)
  │
  ├─ tool_call: delegate({ tasks: [...] })
  │
  ▼
delegate.Execute(ctx, input)
  │
  ├─ Read DelegationRegistry from ctx
  ├─ Orthogonality guard: if PlanChecker.HasDeclaredPlan() → reject
  │  (delegate is disabled once a plan is declared; use execute_plan)
  ├─ Validate tasks (IDs, DAG, depth)
  ├─ Register tasks as "pending"
  ├─ For each ready task:
  │    ├─ Build SubAgentTask (task desc + dependency context + tools + prompt)
  │    ├─ Create child context (cancellable)
  │    ├─ Register cancelFunc in the Registry
  │    └─ RunSubAgent(ctx, ...) → result channel
  ├─ RunSubAgentsParallel collects results
  ├─ For each result:
  │    ├─ Store on blackboard (SetStepResult)
  │    ├─ Update Registry (status, output, error, steps)
  │    └─ SubAgentComplete emitted by RunSubAgent (sp4rk) — sole progress signal;
  │       no PlanStepStart/Complete emitted for delegations
  │
  └─ Return tool result:
       ├─ blocking tasks: aggregated outputs
       └─ async tasks: { delegation_id, status } list
```

Direction: Conductor → `delegate` tool → `RunSubAgent` → subagent `Executor.Run`. Results flow back: subagent → `RunSubAgent` channel → `delegate` tool → tool result → Conductor.

### `declare_plan`

```
Conductor
  │
  ├─ tool_call: declare_plan({
  │    "tasks": [{ id, summary, description, depends_on?, agent? }, ...],
  │    "mode": "present" | "await_approval"   // default present
  │  })
  │
  ▼
declare_plan.Execute(ctx, input)
  │
  ├─ Resolve PlanPublisher from ctx (core's conductorPublisher)
  ├─ publisher.Publish(ctx, tasks):
  │    ├─ Build a Plan from the input tasks (PlanStep incl. optional Agent field)
  │    ├─ SerializePlan(plan) → markdown
  │    ├─ Write to SessionPlansDir as plan_<RandomSuffix>.md
  │    ├─ Emit PlanGenerated event (only after the file is persisted)
  │    ├─ Set plan on the blackboard (bb.SetPlan)
  │    └─ Mark plan declared in planRunState (activates the delegate orthogonality guard)
  │
  ├─ If mode == "await_approval":
  │    ├─ Call ApprovalFunc (wired from BuiltinToolsConfig.PlanApprovalFunc;
  │    │   NOT AskUserFunc) with (planPath, planMarkdown)
  │    ├─ Block until the user responds via the plan_approval_response
  │    │   frontend→backend event (desktop approval resolver)
  │    ├─ On "approve":          return ToolResult{ success content }
  │    ├─ On "request_changes":  return ToolResult{ content incl. feedback }
  │    │   (the Conductor revises and calls declare_plan again)
  │    └─ On "abandon":          return ToolResult{ IsError: true }
  │
  └─ If mode == "present": return ToolResult{ informational content }
```

Direction: Conductor → `declare_plan` tool → `PlanPublisher` (blackboard + emitter + plan file) + (optionally) `ApprovalFunc`. The plan flows to the UI via the `PlanGenerated` event; when `await_approval`, a `plan_review_ready` pending action is surfaced and the user's decision flows back through the `plan_approval_response` frontend→backend event, resolved by the desktop approval resolver into the `ApprovalFunc` callback.

### `execute_plan`

```
Conductor
  │
  ├─ tool_call: execute_plan({})   // no args; plan read from blackboard
  │
  ▼
execute_plan.Execute(ctx, input)
  │
  ├─ Read PlanStepExecutor from ctx
  ├─ executor.Execute(ctx):
  │    ├─ Read Plan from blackboard (GetPlan)
  │    ├─ Build a local DelegationRegistry for dependency resolution
  │    ├─ Wave loop (DAG-ordered):
  │    │    ├─ Find ready steps (all DependsOn completed)
  │    │    ├─ For each ready step:
  │    │    │    ├─ Build SubAgentTask with planStepEventTranslator
  │    │    │    │  (translates SubAgentLaunch→PlanStepStart,
  │    │    │    │   SubAgentComplete→PlanStepComplete on root emitter;
  │    │    │    │   child events carry plan_step_id via scoped copy)
  │    │    │    ├─ Task description includes checklist instruction +
  │    │    │    │  dependency results from prior waves
  │    │    │    └─ Each step executor builds its own checklist
  │    │    ├─ RunSubAgentsParallel (independent steps run concurrently)
  │    │    └─ For each result:
  │    │         ├─ Store on blackboard (SetStepResult)
  │    │         ├─ Mark completed in inlineStepLifecycle (no event —
  │    │         │  PlanStepComplete already emitted by translator)
  │    │         └─ Inject output into dependent steps' task descriptions
  │    └─ Return aggregated PlanStepResult[]
  │
  └─ Return tool result: per-step status (completed/failed) + outputs
```

Direction: Conductor → `execute_plan` tool → `PlanStepExecutor.Execute` → `RunSubAgentsParallel` → subagent `Executor.Run` per step. Events flow through the `planStepEventTranslator` (PlanStepStart/Complete on root emitter; child events on scoped copies with `plan_step_id`). Results flow back to the Conductor as the aggregated tool result. The UI renders steps as plan steps (not subagents) because only `plan_step_start`/`plan_step_complete` events are emitted — the underlying `subagent_launch`/`subagent_complete` are intercepted by the translator.

**Behavioral notes:**

- **Idempotent per plan:** `execute_plan` runs at most once per declared plan. A second call is rejected with an error so the Conductor publishes a new plan via `declare_plan` to retry rather than re-running every step (which would waste tokens and risk duplicated side effects).
- **Deterministic result ordering:** the aggregated `PlanStepResult[]` is sorted by plan-declaration index. Without this the order would be randomised by map iteration (steps within a parallel wave are dispatched in non-deterministic order).
- **Never-started steps emit a terminal pair:** when a step never launches — because its dependencies became unsatisfiable (an upstream step failed) or because its `SubAgentTask` construction failed — the translator never fired `SubAgentLaunch`/`SubAgentComplete` for it. In that case `Execute` (or `defaultPlanStepWave` for build failures) emits a synthesized `PlanStepStart` + `PlanStepComplete(success=false)` directly on the root emitter, then `markCompleted` records it. This guarantees no plan step is left stuck "pending" in the plan panel after a failure cascade.

### `reflect`

```
Conductor
  │
  ├─ tool_call: reflect({
  │    "scope": "trajectory" | "delegation",
  │    "delegation_id": "del_3"   // only when scope == "delegation"
  │  })
  │
  ▼
reflect.Execute(ctx, input)
  │
  ├─ Build the trajectory:
  │    ├─ scope == "trajectory": current Conductor steps + delegation summaries
  │    └─ scope == "delegation": steps of the named delegation from the Registry
  ├─ Call Reflector.Reflect(ctx, trajectory, plan, sessionReflections)
  ├─ Store the Reflection on the blackboard (AddReflection)
  ├─ Emit OnReflected event
  │
  └─ Return tool result: { summary, suggested_action, root_cause, action_plan }
       (the Conductor decides what to do with SuggestedAction: retry, replan, abort)
```

Direction: Conductor → `reflect` tool → `Reflector.Reflect` → tool result → Conductor. The Reflector is a library, not a pipeline phase; the Conductor invokes it at a point of its own choosing.

### Goal-Mode Tools

Three additional internal tools exist ONLY for goal mode (registered in `core/tools/builtin_registration.go`, listed in `goalModeTools` in `core/tools/registry.go`). They are offered to the agent only during an active goal loop — `HandleMessage`/`ResumeTask` strip them from the available-tool list on the non-goal path. The goal loop and the independent verifier deliberately receive the unstripped list.

| Tool | Input | Purpose |
| ---- | ----- | ------- |
| `propose_goal` | `{condition, verify, verification_mode}` | Submit a candidate `{condition, verify}` goal for user sign-off (derivation phase). Blocks until the user approves (optionally with edits to `condition`/`verify`/`verification_mode`) or cancels. Resolves via the `goal_proposal` event + `goal_proposal_response` frontend→backend event (or the `ConfirmGoal`/`CancelGoal` RPC), funnelled through a single desktop resolver. `verification_mode` is `executable` (default) or `re_derivation` |
| `declare_goal_status` | `{status, evidence[], reason}` | The agent's self-evaluation verdict. `status` is `met` (requires concrete `evidence`), `not_met` (keep working), or `blocked` (needs external input). Single channel through which the goal loop learns the agent's structured verdict |
| `declare_verification` | `{confirmed, reason, evidence[]}` | The independent verifier's verdict (`re_derivation` mode). Single channel through which the verification pass reports `{confirmed, reason, evidence}`. `confirmed: true` requires concrete evidence |

All three bypass policy and judge (they are internal). They are NOT in `conductorOnlyToolNames` — their availability is gated by goal mode, not by the conductor/subagent split. See [../domains/goal-mode.md](../domains/goal-mode.md).

## Error Propagation

| Failure | Propagation |
| ------- | ----------- |
| `delegate` validation error (duplicate ID, cycle, depth exceeded) | Tool result `isError: true`; no subagents launch. Conductor continues its loop. |
| Subagent `Finished: false` | Tool result `isError: true` with the abort reason. Conductor decides retry/reflect/finish. |
| Subagent runtime error | Same as above. |
| Subagent cancelled (via `cancel_delegation` or parent cancel) | Registry status "cancelled"; no error to the Conductor (intentional). |
| `declare_plan` approval callback error | Tool result `isError: true`; Conductor may retry or proceed without approval. |
| `declare_plan` write failure (SessionPlansDir) | Tool result `isError: true` with the filesystem error. |
| `reflect` LLM call failure | Tool result `isError: true`; Conductor may proceed without reflection. |
| `cancel_delegation` on unknown ID | Tool result `isError: true` (no-op otherwise). |
| `finish` with pending async delegations | Tool result `isError: true` listing pending IDs; Conductor must cancel or wait. |

All errors are tool-level (`ToolResult{ IsError: true, Content: "..." }`), not Go errors returned from `Execute`. The Conductor sees them as observations in its ReAct loop and decides how to react. This preserves the ReAct contract: tool failures are observations, not control-flow interruptions.

## Breaking Change Checklist

- If you change the `delegate` input schema, update `core/tools/delegate.go`, the Conductor system-prompt guidance in `core/systemprompt.go`, and [../domains/orchestration/delegation.md](../domains/orchestration/delegation.md).
- If you change the `DelegationRegistry` API, update `delegate`, `cancel_delegation`, `read_step_output`, and the `finish` join check in the executor.
- If you add a new Conductor tool, add it to `internalTools` in `core/tools/registry.go`, register it in `core/tools/builtin_registration.go`, add it to the Conductor tool set in `core/conductor.go`, and document it in this contract.
- If you change the `declare_plan` event payload, the frontend plan panel ([../domains/frontend/events.md](../domains/frontend/events.md)) must be updated to match.
- If you change the `Plan` / `PlanStep` struct in `github.com/v0lka/sp4rk/orchestration/types.go`, both `delegate` and `declare_plan` are affected (they share these types).
- If you change the `Reflector.Reflect` signature, update the `reflect` tool wrapper.
- If you remove or rename an internal tool, update the `internalTools` set and the "always available" invariants in [conductor.md](../domains/orchestration/conductor.md) and [delegation.md](../domains/orchestration/delegation.md).

## Related Specs

- [../domains/orchestration/conductor.md](../domains/orchestration/conductor.md) — Conductor component
- [../domains/orchestration/delegation.md](../domains/orchestration/delegation.md) — delegate tool and Delegation Registry detail
- [../domains/orchestration/executor.md](../domains/orchestration/executor.md) — ReAct loop primitive
- [../domains/tool-system/README.md](../domains/tool-system/README.md) — tool registry and execution pipeline
- [../decisions/012-conductor-orchestration-pipeline.md](../decisions/012-conductor-orchestration-pipeline.md) — architectural decision
