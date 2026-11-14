# Contract: Conductor Tools

## Boundary Rule

Conductor tools (`delegate`, `declare_plan`, `execute_plan`, `reflect`, `cancel_delegation`) are `system`-group tools (`sdktools.GroupSystem`, ADR-024) registered on the shared registry in `core/tools/builtin_registration.go`; the Conductor's available-tool list is that registry's `ListFiltered` output, assembled in `core/orchestrator.go`. They are always allowed (bypass policy and judge) and are never available to regular plan-step executors — `conductorOnlyToolNames` in `core/conductor.go` strips them from every subagent toolset, and only a blocking subagent with `allow_redelegate: true` gets `delegate` + `cancel_delegation` added back (async delegations always take the non-redelegating path).

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
| `DelegationRegistry` | `core/tools` | `delegate`, `cancel_delegation` (via context); the `finish` join guard reads it through `orchestration.PendingDelegations` | Track active/completed delegations for one Conductor run |
| `DelegationLauncher` | `core/tools` | `delegate` tool (via context; implemented by `conductorLauncher`) | Builds and runs the subagents of a delegate call |
| `RunSubAgent` / `RunSubAgentsParallel` | `github.com/v0lka/sp4rk/agent` | `core/conductor.go` (`conductorLauncher`) | Execute an isolated `Executor.Run` in a goroutine |
| `PlanStepExecutor` | `core/tools` | `execute_plan` tool (via context; implemented by `conductorLauncher`) | Execute the declared plan's steps in DAG order |
| `PlanChecker` | `core/tools` | `delegate` tool | Reports whether a plan is declared (enforces orthogonality guard) |
| `ReflectionRunner` | `core/tools` | `reflect` tool (via context; implemented by `conductorReflectionRunner`) | Runs a reflection over the current or a sub-task trajectory |
| `Reflector.Reflect` | `github.com/v0lka/sp4rk/agent/reflector` | `core/conductor.go` (`conductorReflectionRunner`) | Produce a Reflection (root cause, action plan, suggested action) |
| `PlanPublisher` | `core/tools` | `declare_plan` tool (via context; implemented by `conductorPublisher`) | Serializes, persists, emits and blackboards the declared plan |
| `SerializePlan` | `core` | `core/conductor.go` (`conductorPublisher.Publish`) | Serialize a Plan to markdown for UI display and plan-file persistence |
| `Plan` / `PlanStep` | `github.com/v0lka/sp4rk/orchestration` | `core/conductor.go`, `core/plan_serializer.go` | DAG data structures; the wave loop selects ready steps itself, so `orchestration.FindReadySteps` stays unused in c0wrk |

## Initialization

Conductor tools are registered at startup in `core/tools/builtin_registration.go`. Each constructor tags the tool `ToolGroup: sdktools.GroupSystem` (`BaseTool` field) — the **reserved `system` group** that bypasses policy (ADR-024). Membership is declared on the tool itself; there is no out-of-band name set:

```go
// core/tools/delegate.go (representative)
return &DelegateTool{
    BaseTool: &sdktools.BaseTool{
        ToolGroup:       sdktools.GroupSystem, // bypasses security.groups policy
        ToolName:        "delegate",
        ToolDescription: toolDelegateDescription,
        Schema:          json.RawMessage(`…`),
        Policy:          sdktools.PolicyAlwaysAllow,
    },
}
// system-group c0wrk tools: delegate, cancel_delegation, declare_plan,
// execute_plan, reflect, propose_goal, declare_goal_status,
// declare_verification, ask_user, declare_step_complete (see builtins.md catalog)
```

The Conductor tool set is the shared registry's descriptor list: `core/orchestrator.go` calls `ListFiltered(disabledToolNames())`, then `stripGatedMCPTools`, and — on the non-goal path — `tools.StripGoalModeTools`. The conductor-only tools are present because `RegisterBuiltinTools` put them on that shared registry; `core/conductor.go` only ever *removes* them, from subagent toolsets: `conductorOnlyToolNames` names them, `stripConductorOnlyTools` drops them from a descriptor list, and `stripSubagentTools` additionally drops the goal-loop actor tools.

Subagent tool sets are assembled by `conductorLauncher.resolveTaskTools` in `core/conductor.go` — the launcher the `delegate` tool reaches through `DelegationLauncher` — from the `tools` field of each task (group tokens; the `system` group always included). Every branch ends in `stripSubagentTools`, so `delegate` and `cancel_delegation` are absent from a subagent's resolved set; `runRedelegBlocking` adds exactly those two back, by registry lookup, for a task whose `allow_redelegate` is true.

## Data Flow Across Boundary

### `delegate`

```
Conductor (ReAct loop)
  │
  ├─ tool_call: delegate({ tasks: [...] })
  │
  ▼
delegate.Execute(ctx, input)                     [core/tools/delegate.go]
  │
  ├─ Read DelegationRegistry + DelegationLauncher from ctx
  │  (either missing → error result: "not running inside a Conductor")
  ├─ Orthogonality guard: if PlanChecker.HasDeclaredPlan() → reject
  │  (delegate is disabled once a plan is declared; use execute_plan)
  ├─ validateDelegationTasks (IDs, DAG, depth, agent profile existence,
  │  model overrides — task `model` + profile pin — via the source-labeled
  │  ModelOverrideValidator: any failure rejects the whole call, ADR-123)
  ├─ Duplicate guard: an id already settled on the blackboard is replayed
  │  into the Registry via Register + Complete and refused as fresh work
  ├─ RegisterTask each remaining task as "pending"
  │
  ├─ launcher.Launch(ctx, runnable, registry)
  │    │
  │    ▼  conductorLauncher.Launch → runWave      [core/conductor.go]
  │    ├─ async tasks: launchAsync — child context, cancelFunc registered
  │    │  in the Registry, RunSubAgent detached
  │    ├─ blocking, non-redelegating: runRegularBlocking
  │    │    ├─ buildSubAgentTask (task desc + dependency context + tools + prompt)
  │    │    └─ RunSubAgentsParallel → one result channel per task
  │    ├─ blocking, allow_redelegate: runRedelegBlocking
  │    │    ├─ child DelegationRegistry at depth+1 via redelegTaskCtx
  │    │    └─ RunSubAgent with delegate + cancel_delegation added back
  │    └─ For each result:
  │         ├─ persistUnitOutcome → blackboard SetStepResult (+ unit ledger)
  │         ├─ Registry.Complete, or CompletePaused on agent.ErrPaused
  │         └─ SubAgentComplete emitted by RunSubAgent (sp4rk) — sole progress
  │            signal; no PlanStepStart/Complete emitted for delegations
  │
  └─ Return tool result (buildDelegateToolResult):
       ├─ blocking tasks: aggregated outputs
       └─ async tasks: { delegation_id, status } list
```

Direction: Conductor → `delegate` tool → `DelegationLauncher.Launch` (`conductorLauncher`) → `RunSubAgent`/`RunSubAgentsParallel` → subagent `Executor.Run`. Results flow back: subagent → `RunSubAgent` channel → `conductorLauncher` → `DelegationResult` → `delegate` tool result → Conductor.

**Input contract:** each task of the `tasks` array carries `id` (unique within the run), `summary`, `task`, optional `acceptance_criteria`, `tools` (`"all"` / `"read-only"` / group-token array), `depends_on` (blocking ids only), `mode` (`blocking` | `async`), `max_steps`, `allow_redelegate`, optional `model`, and optional `agent` (a Subagent Profile name). The `model` field (ADR-123) is a per-delegation override: a full composite `provider/model` id routing THIS subagent to another provider — bare names and `"provider/"` composites are rejected outright (ADR-122's mandatory-composite grammar), the value is validated at delegate time through the source-labeled `ModelOverrideValidator` (sources `"task <id>"` / `"profile <name>"`; provider-name-only, router-backed; any failure rejects the whole call), and it wins over the resolved profile's `model:` pin (which wins over the session's active model): task > profile > session. The resolved chain re-validates defensively at launch (covering mid-run drift and resume relaunches — the durable `DelegationSpec` persists the task's `model`). The full field reference and the resolution/validation funnel live in [../domains/orchestration/delegation.md](../domains/orchestration/delegation.md) (§ Model Override Resolution).

**Cooperative pause and resume:** every subagent Executor receives the session pause checker. When a subagent stops with `agent.ErrPaused`, its partial `[]agent.Step` trajectory is stored on the blackboard as the step result's checkpoint rather than treated as a fresh failure. Rebuilding the same delegation/plan step reads only that cooperative-pause result, seeds the checkpoint into both `orchestration.StepSeedable.SeedSteps` on the isolated ContextManager and `agent.WithResumeSteps` on the Executor, and continues step numbering from the checkpoint. Ordinary failed steps re-run from a fresh context. A paused checkpoint with a ContextManager that does not implement `StepSeedable` fails before dispatch; silently restarting and discarding the trajectory is not permitted.

**Duplicate guard:** `delegate` refuses a task id that already carries a successful blackboard `StepResult` (settled by the auto-resume wave or a prior leg of the task). The settled outcome is replayed into the registry via `Register` — never `RegisterTask`, so the unit-ledger spec sink does not re-fire and persisted `created_at` ordering stays stable — so co-launched dependents still resolve, while the caller gets a factual refusal instead of re-running finished work.

### `declare_plan`

```
Conductor
  │
  ├─ tool_call: declare_plan({
  │    "tasks": [{ id, summary, description, depends_on, agent? }, ...],
  │    │              // depends_on is REQUIRED on every task — an explicit
  │    │              // [] declares independence; omitting the field is
  │    │              // rejected at both validation layers
  │    "mode": "present" | "await_approval"   // default present
  │  })
  │
  ▼
declare_plan.Execute(ctx, input)
  │
  ├─ (registry Gate 1, before Execute): structural input validation —
  │    sdktools.ValidateToolInput checks the whole call against the tool's
  │    JSON schema, recursively into tasks[i] objects (a missing or
  │    mistyped id surfaces as a "tasks[2].id" violation with the valid
  │    parameters listed). This gate runs for system-group tools too —
  │    it precedes the system-group bypass in the pipeline.
  ├─ validatePlanTasks(tasks): semantic checks the schema cannot express —
  │    every task needs a non-empty id, summary, description; ids must be
  │    unique; every task must DECLARE depends_on (an absent field or JSON
  │    null is rejected — an explicit [] declares independence); every
  │    depends_on entry must reference a declared id. All
  │    violations are reported at once; on failure returns an ErrorResult
  │    BEFORE any persistence/blackboard/approval side effect.
  ├─ Resolve PlanPublisher from ctx (core's conductorPublisher)
  ├─ publisher.Publish(ctx, tasks):
  │    ├─ Build a Plan from the input tasks (PlanStep incl. optional Agent field)
  │    ├─ SerializePlan(plan) → markdown
  │    ├─ Write to SessionPlansDir as plan_<RandomSuffix>.md
  │    ├─ Emit PlanGenerated event (only after the file is persisted)
  │    ├─ Set plan on the blackboard (bb.SetPlan)
  │    └─ Mark plan declared in planRunState (activates the delegate orthogonality guard)
  │
  ├─ planExecutionWaves(tasks): Kahn-layer the validated depends_on DAG into
  │    execution waves (wave 1 = dependency-free steps; steps inside a wave
  │    keep declaration order). formatExecutionWaves renders the echo:
  │    "Execution waves: 1=[step_1, step_3] · 2=[step_2] · 3=[step_4]"
  │
  ├─ If mode == "await_approval":
  │    ├─ Call ApprovalFunc (wired from BuiltinToolsConfig.PlanApprovalFunc;
  │    │   NOT AskUserFunc) with (planPath, planMarkdown)
  │    ├─ Block until the user responds via the plan_approval_response
  │    │   frontend→backend event (desktop approval resolver)
  │    ├─ On "approve":          return ToolResult{ success content + wave echo }
  │    ├─ On "request_changes":  return ToolResult{ content incl. feedback }
  │    │   (the Conductor revises and calls declare_plan again)
  │    └─ On "abandon":          release this run's plan workflow and settle the
  │        abandoned plan's steps as terminal (optional planAbandoner capability →
  │        AbandonPlan(); normative detail: domains/orchestration/conductor.md),
  │        then return ToolResult{ IsError: true }
  │
  └─ If mode == "present": return ToolResult{ informational content + wave echo;
       plus, when the multi-step plan collapsed into a single wave, a non-blocking
       single-wave hint }
```

Direction: Conductor → `declare_plan` tool → `PlanPublisher` (blackboard + emitter + plan file) + (optionally) `ApprovalFunc`. The plan flows to the UI via the `PlanGenerated` event; when `await_approval`, a `plan_review_ready` pending action is surfaced and the user's decision flows back through the `plan_approval_response` frontend→backend event, resolved by the desktop approval resolver into the `ApprovalFunc` callback.

**Behavioral notes:**

- **Mandatory `depends_on` declaration:** every task must state its `depends_on` explicitly; a task with no dependencies declares an empty array `[]`. An omitted field (or JSON `null`, which unmarshals to the same nil slice) is rejected twice: structurally by registry Gate 1 (`depends_on` is in the schema's task-level `required`, so the violation is named as `tasks[2].depends_on` before dispatch) and semantically by `validatePlanTasks` (1-based task number + the `[]` fix). This makes a flat plan a *deliberate declaration* rather than an omission — a declared-flat plan still produces the single wave below. `execute_plan` and the plan-restore path never re-validate presence, so persisted plans from before this rule resume unaffected. See [ADR-069](../decisions/069-plan-step-dependencies-mandatory.md).
- **Execution-wave echo:** every successful call (both modes) appends the plan's execution waves to the tool result. Waves are computed by Kahn-layering the validated `depends_on` DAG (`planExecutionWaves`, rendered by `formatExecutionWaves`): wave 1 holds every dependency-free step, wave N+1 holds the steps whose prerequisites all completed in waves ≤ N, and steps inside a wave keep their declaration order. The echo looks like `Execution waves: 1=[step_1, step_3] · 2=[step_2] · 3=[step_4]` and makes an under-specified dependency graph visible to the Conductor *before* `execute_plan` fans the steps into concurrency. Acyclicity is guaranteed upstream by `validatePlanTasks`, so the layering is always total.
- **Single-wave hint (present only):** when a multi-step plan collapses into a single wave (every step declared dependency-free, so all run concurrently) the `present` result additionally carries a **non-blocking** hint (`IsError: false`): *"All N steps are in a single parallel wave — every step will run concurrently. If any step consumes another's output, re-declare with depends_on before executing."* The hint never fires for a one-step plan, and it is suppressed in `await_approval` mode (the user is already reviewing the plan there; only the wave echo is surfaced, on approval). The validation-error, continuation-hint, and `request_changes` paths are unchanged. The `abandon` path returns the same `IsError: true` result as before, but now first releases this run's plan workflow and settles the abandoned plan's steps as terminal (via the optional `planAbandoner` capability → `AbandonPlan()`; see the abandon invariant in [../domains/orchestration/conductor.md](../domains/orchestration/conductor.md)).
- **PlanContinuation guard (resumed tasks):** an optional `PlanChecker` capability (`core/tools.PlanContinuation`, implemented by the core `conductorLauncher`, detected by type assertion) reports that the current run resumed a task whose declared plan still has unreached steps. `declare_plan` then returns a soft, non-error execute_plan hint instead of publishing a replacement plan — re-declaring would replace the approved roadmap and reset step progress.

### `execute_plan`

```
Conductor
  │
  ├─ tool_call: execute_plan({})   // optional {"steps": ["step_2", ...]}; plan read from blackboard
  │
  ▼
execute_plan.Execute(ctx, input)
  │
  ├─ Read PlanStepExecutor from ctx
  ├─ executor.Execute(ctx, input.Steps):
  │    ├─ Read Plan from blackboard (GetPlan)
  │    ├─ Preflight: planStepsWellFormed — every step needs a non-empty,
  │    │  unique id (guards restored/legacy plans that bypassed declare_plan
  │    │  validation). On failure returns an actionable ErrorResult pointing
  │    │  at declare_plan; nothing is registered or dispatched.
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
  └─ Return tool result: per-step status (completed/failed/paused) + outputs,
     sorted by plan-declaration index
```

Direction: Conductor → `execute_plan` tool → `PlanStepExecutor.Execute` → `RunSubAgentsParallel` → subagent `Executor.Run` per step. Events flow through the `planStepEventTranslator` (PlanStepStart/Complete on root emitter; child events on scoped copies with `plan_step_id`). Results flow back to the Conductor as the aggregated tool result. The UI renders steps as plan steps (not subagents) because only `plan_step_start`/`plan_step_complete` events are emitted — the underlying `subagent_launch`/`subagent_complete` are intercepted by the translator.

**Behavioral notes:**

- **Resumable (no-arg retry):** a second `execute_plan` call does NOT reject and does NOT re-run everything. Steps that already have a successful result on the blackboard (`StepResult.Error == nil`) are skipped, and only failed or never-started steps re-run. Failed steps have an error result; never-started steps (whose dependencies became unsatisfiable) have no result at all — both are therefore eligible to run again. Skipped steps are replayed into the local `DelegationRegistry` as completed so their dependents can proceed and read their previous output.
- **Explicit step targets:** `{"steps": ["step_2"]}` forces the named steps — plus every step that transitively depends on them, since their inputs are now stale — to re-run even if they previously succeeded. Other already-successful steps are still skipped. Unknown step IDs are rejected before any step is dispatched.
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
reflect.Execute(ctx, input)                        [core/tools/reflect.go]
  │
  ├─ Validate scope ("trajectory" default) and require delegation_id for
  │  scope == "delegation"; resolve ReflectionRunner from ctx
  │
  ├─ runner.Reflect(ctx, scope, delegationID)
  │    │
  │    ▼  conductorReflectionRunner.Reflect        [core/conductor.go]
  │    ├─ Build the trajectory:
  │    │    ├─ scope == "trajectory": the run's TrajectoryStore steps
  │    │    └─ scope == "delegation": the named delegation's steps from the
  │    │       DelegationRegistry (unknown id → error)
  │    ├─ Reflector.Reflect(ctx, trajectory, bb.GetPlan(), bb.GetReflections())
  │    ├─ Store the Reflection on the blackboard (AddReflection)
  │    └─ Emit core.Emitter.Reflection(reflection, attempt, maxAttempts)
  │
  └─ Return tool result: { summary, suggested_action, root_cause, action_plan }
       (the Conductor decides what to do with SuggestedAction: retry, replan, abort)
```

Direction: Conductor → `reflect` tool → `core/tools.ReflectionRunner` (`conductorReflectionRunner`) → `Reflector.Reflect` → tool result → Conductor. The Reflector is a library, not a pipeline phase; the Conductor invokes it at a point of its own choosing.

### Goal-Mode Tools

Three additional system-group tools exist ONLY for goal mode (registered in `core/tools/builtin_registration.go`, listed in `goalModeTools` in `core/tools/registry.go`). They are offered to the goal-loop Conductor and the independent verifier only — `HandleMessage`/`Resume` strip them from the Conductor's available-tool list on the non-goal path, and `resolveTaskTools` strips them from EVERY delegated subagent toolset (via `stripSubagentTools`, even when the `system` group is granted explicitly). `subagentCtx` (and the redelegating path) additionally clear the goal-status/verification sinks, so a task subagent can neither see nor reach the goal loop's verdict channels; the verifier keeps `declare_verification` because `verifierToolFilter`/`verifierReDerivationToolFilter` build its toolset from the raw registry list, not via `resolveTaskTools`.

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
| Paused subagent checkpoint cannot seed its ContextManager | Tool result `isError: true` before dispatch; the checkpoint remains on the blackboard and is never replaced by a fresh restart. |
| Subagent cancelled (via `cancel_delegation` or parent cancel) | Registry status "cancelled"; no error to the Conductor (intentional). |
| `declare_plan` approval callback error | Tool result `isError: true`; Conductor may retry or proceed without approval. |
| `declare_plan` write failure (SessionPlansDir) | Tool result `isError: true` with the filesystem error. |
| `reflect` LLM call failure | Tool result `isError: true`; Conductor may proceed without reflection. |
| `cancel_delegation` on unknown ID | Tool result `isError: true` (no-op otherwise). |
| `finish` with pending async delegations | Not a tool error — `finish` is handled inline by the sp4rk Executor and never reaches `tools.Execute`. The `FinishGuard` the sp4rk Conductor installs (`agent.Executor.SetFinishGuard`, reading the registry from context as `orchestration.PendingDelegations`) rejects the finish and injects the pending IDs as a nudge step (`agent.Step.UserNudge`); the loop continues and the Conductor cancels or waits. |

Every row above except the `finish` join guard is tool-level (`ToolResult{ IsError: true, Content: "..." }`), not a Go error returned from `Execute`. The Conductor sees those as observations in its ReAct loop and decides how to react. This preserves the ReAct contract: tool failures are observations, not control-flow interruptions. The `finish` guard is an executor-level gate, so it reaches the model as a nudge step instead of an observation.

## Breaking Change Checklist

- If you change the `delegate` input schema, update `core/tools/delegate.go`, the Conductor system-prompt guidance in `core/systemprompt.go`, and [../domains/orchestration/delegation.md](../domains/orchestration/delegation.md).
- If you change the `DelegationRegistry` API, update `delegate`, `cancel_delegation`, every `conductorLauncher` call site in `core/conductor.go`, and the `ListPending` method the sp4rk Conductor's `finish` join guard calls through `orchestration.PendingDelegations`.
- If you change paused-subagent checkpoint encoding or resume seeding, update `conductorLauncher.pausedCheckpoint` / `buildSubAgentTask`, the blackboard `StepResult` persistence, and [../domains/orchestration/delegation.md](../domains/orchestration/delegation.md).
- If you add a new Conductor tool, tag it `ToolGroup: sdktools.GroupSystem` on its `BaseTool` (requires security review — it bypasses policy) and register it in `core/tools/builtin_registration.go`; registration alone puts it in the Conductor's tool set. Add its name to `conductorOnlyToolNames` in `core/conductor.go` when a subagent must not receive it, or to `goalModeTools` in `core/tools/registry.go` when it belongs to the goal loop only, then document it in this contract.
- If you change the `declare_plan` event payload, the frontend plan panel ([../domains/frontend/events.md](../domains/frontend/events.md)) must be updated to match.
- If you change the `Plan` / `PlanStep` struct in `github.com/v0lka/sp4rk/orchestration/types.go`, update `conductorPublisher.Publish` and `planStepsWellFormed` in `core/conductor.go` plus `core/plan_serializer.go` — the `core/tools` layer never imports `orchestration`, so `delegate` and `declare_plan` exchange `PlanTaskInput` and reach these types only through the injected `PlanPublisher` / `PlanStepExecutor`.
- If you change the `Reflector.Reflect` signature, update `conductorReflectionRunner` in `core/conductor.go` and the `core/tools.ReflectionRunner` shape the `reflect` tool calls.
- If you remove or rename a system-group tool, update the "always available" invariants in [conductor.md](../domains/orchestration/conductor.md) and [delegation.md](../domains/orchestration/delegation.md).

## Related Specs

- [../domains/orchestration/conductor.md](../domains/orchestration/conductor.md) — Conductor component
- [../domains/orchestration/delegation.md](../domains/orchestration/delegation.md) — delegate tool and Delegation Registry detail
- [../domains/orchestration/executor.md](../domains/orchestration/executor.md) — ReAct loop primitive
- [../domains/tool-system/README.md](../domains/tool-system/README.md) — tool registry and execution pipeline
- [../decisions/012-conductor-orchestration-pipeline.md](../decisions/012-conductor-orchestration-pipeline.md) — architectural decision
- [../decisions/122-subagent-model-provider-override.md](../decisions/122-subagent-model-provider-override.md) — the profile `model:` pin (composite grammar, provider-override caller)
- [../decisions/123-per-task-model-override.md](../decisions/123-per-task-model-override.md) — the per-task `model` override (precedence task > profile > session, source-labeled validation funnel)
