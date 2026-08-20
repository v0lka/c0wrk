# Delegation

## Role

The `delegate` tool lets the Conductor launch one or more subagents to execute units of work in isolated ReAct loops, with DAG dependencies between them and a choice of blocking or async execution. It replaces the prior system-driven DAG execution (`executePlanWithSteps`) with an agent-driven invocation.

Delegation is an **execution** mechanism, not a planning one. It has its own UI progress tracking via `SubAgentLaunch`/`SubAgentComplete` events emitted by sp4rk's `RunSubAgent`. The Conductor should NOT call `declare_plan` to display or mirror delegated tasks — `declare_plan` is for user roadmaps and approval gates only.

`delegate` is for **plan-less** tasks only. Once a plan is declared via `declare_plan`, `delegate` is disabled (enforced by the PlanChecker guard) and `execute_plan` must be used to run the plan steps instead. The two are orthogonal execution paths — `delegate` optimizes Conductor context; `execute_plan` executes a user-approved roadmap. See [conductor.md](conductor.md) for the `execute_plan` path and the `planStepEventTranslator` that adapts subagent events to plan-step lifecycle.

## Key Files

- `core/tools/delegate.go` — `delegate` tool implementation
- `core/tools/cancel_delegation.go` — `cancel_delegation` tool (async cancellation)
- `core/tools/delegation_registry.go` — Delegation Registry (active/completed delegations per Conductor run)
- `github.com/v0lka/sp4rk/agent/subagent.go` — `RunSubAgent` / `RunSubAgentsParallel` (the primitive that runs an isolated executor in a goroutine)
- `github.com/v0lka/sp4rk/orchestration/dag.go` — `FindReadySteps` and DAG traversal (reused for dependency resolution)
- `github.com/v0lka/sp4rk/orchestration/types.go` — `Plan` and `PlanStep` types (reused as delegation task descriptors)

## Behavior

### `delegate` Tool

#### Input Schema

```json
{
  "tasks": [
    {
      "id": "del_1",
      "summary": "5-7 word UI label",
      "task": "Full task description with What/How/Where/Acceptance Criteria",
      "acceptance_criteria": ["criterion 1", "criterion 2"],
      "tools": ["local-read", "local-write", "execute"] | "all" | "read-only",
      "depends_on": ["del_0"],
      "mode": "blocking" | "async",
      "max_steps": 50 | null,
      "allow_redelegate": false,
      "agent": "code-reviewer"
    }
  ]
}
```

| Field | Required | Description |
| ----- | -------- | ----------- |
| `tasks` | yes | Array of 1+ delegation tasks. Multiple tasks with no `depends_on` between them run in parallel via `RunSubAgentsParallel`. |
| `tasks[].id` | yes | Unique within this Conductor run. Used by `depends_on`, `read_step_output`, `cancel_delegation`. |
| `tasks[].summary` | yes | Short label emitted to the UI plan panel. |
| `tasks[].task` | yes | Full task description. Convention: What/How/Where/Acceptance Criteria (same format as the prior plan-step description, so existing UI rendering works unchanged). |
| `tasks[].acceptance_criteria` | no | Optional explicit list; if present, the subagent verifies before calling `finish`. |
| `tasks[].tools` | no | Tool subset for the subagent, resolved by **capability group** (ADR-024). `"all"` (default) = full tool set minus Conductor-only tools; `"read-only"` = the `system ∪ local_read ∪ remote_read` groups, no MCP; array = group tokens (kebab-case: `execute`, `local-read`, `local-write`, `remote-read`, `remote-write`, `local-mcp`, `remote-mcp`, `system`) granted on top of the ALWAYS-included `system` group (finish, facts, checklist, meta tools). Unknown tokens fail closed — the delegation never launches. |
| `tasks[].depends_on` | no | IDs of blocking delegations that must complete before this one starts. Used to express a DAG across multiple `delegate` calls within one Conductor run. `depends_on` can only reference **blocking** tasks — async tasks run in the background and cannot be depended upon. |
| `tasks[].mode` | no | `"blocking"` (default): the tool result contains the subagent output. `"async"`: the tool result returns immediately with `delegation_id`; the Conductor reads results later via `read_step_output(id)`. |
| `tasks[].max_steps` | no | Per-subagent ReAct iteration cap. Empty = derived from routing complexity (`complexity × 30`, same formula as the Conductor via `stepsPerComplexity`). A Subagent Profile's `max_steps` (when > 0) overrides this field and the default. |
| `tasks[].allow_redelegate` | no | `true` grants the subagent the `delegate` and `cancel_delegation` tools (depth-capped by `OrchestratorConfig.MaxRedelegationDepth`, default 2). A Subagent Profile with `allow-redelegate: true` overrides this field to `true`. Default `false` (flat). |
| `tasks[].agent` | no | Name of a Subagent Profile (`<workspace>/.agents/agents/<name>/AGENT.md`). When set, the launcher resolves the profile and applies it: the profile body replaces the orchestrator core directive (the shared project-context prefix is preserved via `buildSpecializedSystemPrompt`), and the profile's tool preference / `max_steps` / `model` / `allow-redelegate` override the task fields. Unknown name fails fast (delegate validation rejects it before any subagent launches). Empty = no profile (the legacy behavior). |

#### Execution Flow

```
delegate.Execute(ctx, input)
│
├─ 1. Parse input into a list of DelegationTask structs.
│
├─ 2. Validate:
│     ├─ IDs unique within the Conductor run
│     ├─ depends_on references exist (in the Registry or in this batch)
│     ├─ no cycles in the combined DAG (existing + new tasks)
│     └─ allow_redelegate depth does not exceed OrchestratorConfig.MaxRedelegationDepth
│
├─ 3. Register all tasks in the Delegation Registry as "pending".
│
├─ 4. Resolve ready tasks (depends_on satisfied by completed delegations
│     or tasks in this same batch that are blocking).
│
├─ 5. For each ready task, build a SubAgentTask:
│     ├─ Task description = task + injected context from depends_on outputs
│     │   (truncated to OrchestratorConfig.MaxDependencyContextChars, shared
│     │   across dependencies — same logic as the prior step dependency injection)
│     ├─ Tool set = resolveTaskTools(tools field) — group-based:
│     │   system group always included + granted groups (ADR-024)
│     ├─ System prompt = buildSystemPrompt; when an agent profile is set,
│     │   buildSpecializedSystemPrompt applies it (profile body replaces the
│     │   core directive, shared project-context prefix preserved)
│     ├─ ContextManager via contextFactory (isolated per subagent)
│     ├─ Cooperative-pause checkpoint (when the blackboard result for this ID
│     │   has Error == agent.ErrPaused): seed its Steps into both the
│     │   ContextManager (StepSeedable) and Executor (WithResumeSteps)
│     ├─ Executor = agent.NewExecutor with max_steps, circuit breakers, HITL,
│     │   and the session pause checker
│     ├─ Scoped emitter (subagent events flow to UI under the delegation ID)
│     └─ If allow_redelegate: inject a child Delegation Registry and add
│         delegate/cancel_delegation to the subagent tool set (depth-capped)
│
├─ 6. Dispatch:
│     ├─ RunSubAgentsParallel for all ready tasks in this call
│     └─ Each subagent runs in its own goroutine with its own context
│        (child of the Conductor context, cancellable)
│
├─ 7. Collect results:
│     ├─ For each completed subagent:
│     │   ├─ Store result on the blackboard (SetStepResult)
│     │   ├─ Update the Registry: status "completed" or "failed"
│     │   └─ SubAgentComplete emitted by RunSubAgent (sp4rk) — the sole
│     │      progress signal for delegations; no PlanStepStart/Complete
│     └─ Tasks with depends_on that are now satisfied remain "pending"
│        until a later delegate call or read_step_output triggers them
│
└─ 8. Return tool result:
       ├─ All blocking: aggregate outputs of blocking tasks
       ├─ All async: list of { delegation_id, status: "pending" | "running" }
       └─ Mixed: blocking outputs + async IDs
```

### Cooperative Subagent Pause and Resume

Every subagent Executor created by `conductorLauncher.configureExecutor` receives the session-level pause checker. At a step boundary, a flipped pause signal returns `agent.ErrPaused` together with the partial trajectory. The launcher stores that trajectory in the blackboard's `StepResult` for the delegation or plan-step ID, preserving the checkpoint across the outer session pause.

When the same ID is built again after session resume:

1. `pausedCheckpoint(id)` accepts only a prior `StepResult` whose error is `agent.ErrPaused`; failed and never-started steps start fresh.
2. A defensive copy of `StepResult.Steps` is seeded into the isolated ContextManager via `orchestration.StepSeedable.SeedSteps` so prior assistant/tool turns reappear in the prompt.
3. The same steps are passed to the Executor via `agent.WithResumeSteps`, so execution continues at `len(checkpoint)+1` and the returned trajectory contains both checkpoint and new steps.
4. If the ContextManager does not implement `StepSeedable`, task construction fails before dispatch. The launcher never silently discards a cooperative-pause checkpoint.

This behavior is shared by direct `delegate` tasks and the subagents underlying `execute_plan`, because both use `conductorLauncher.buildSubAgentTask`. The resumed subagent retains its isolated ContextManager and tool budget; only its own checkpoint is restored.

### `cancel_delegation` Tool

```
cancel_delegation.Execute(ctx, { "id": "del_3" })
│
├─ Look up the delegation in the Registry
├─ If "completed": return success (no-op)
├─ If "pending" or "running": cancel the subagent context
│   └─ context-tree cancellation propagates to the subagent's Executor.Run
│      and any child delegations
├─ Update the Registry: status "cancelled"
└─ Return success
```

### Delegation Registry

Per-Conductor-run registry tracking all delegations. Injected into the Conductor context; child registries are injected into subagent contexts when `allow_redelegate` is true.

```go
type DelegationRegistry struct {
    mu        sync.Mutex
    delegations map[string]*Delegation
    cancelFuncs map[string]context.CancelFunc
    depth       int
}

type Delegation struct {
    ID          string
    Summary     string
    Status      DelegationStatus  // "pending" | "running" | "completed" | "failed" | "cancelled" | "paused"
    Output      string
    Error       error
    Steps       []agent.Step
    DependsOn   []string
    Mode        string  // "blocking" | "async"
    StartedAt   time.Time
    CompletedAt time.Time
}
```

Operations:

| Operation | Purpose |
| --------- | ------- |
| `Register(id, summary, dependsOn, mode)` | Add a new delegation as "pending" (errors on duplicate ID) |
| `Start(id, cancelFunc)` | Mark "running", store the cancellation handle |
| `Complete(id, output, err, steps)` | Mark "completed" or "failed", store output/error/steps |
| `CompletePaused(id, output, steps)` | Mark "paused" (a cooperative-pause checkpoint distinct from `Complete`), store the partial trajectory/steps |
| `Cancel(id)` | Cancel via the stored CancelFunc, mark "cancelled" |
| `Get(id)` | Read a delegation (used by `read_step_output`) |
| `ListPending()` | Return IDs of all pending/running delegations (used by `finish` join check) |
| `Has(id)` | Report whether a delegation ID is registered |
| `IsCompleted(id)` | Report whether a delegation is in a terminal state (completed/failed/cancelled) |
| `All()` | Snapshot of all delegations in insertion order |
| `Depth()` | Return the registry's delegation depth (0 for the Conductor's root registry) |

Child registries (for `allow_redelegate`) are created at an incremented depth via `NewDelegationRegistryWithDepth(depth)`; the launcher checks `registry.Depth() >= OrchestratorConfig.MaxRedelegationDepth` before building a redelegating subagent.

### Finish Join Semantics

When the Conductor calls `finish`:

1. The executor's finish handler checks the Delegation Registry for pending or running async delegations.
2. If any exist and none have been cancelled via `cancel_delegation`:
   - The finish tool returns an error: "N async delegations are still pending (ids: ...). Call cancel_delegation for each, or wait for them via read_step_output before finishing."
   - The Conductor continues its loop (this is a soft gate, implemented as a tool error).
3. If all async delegations are completed, failed, or cancelled: `finish` proceeds normally.

This prevents the Conductor from abandoning background work silently. The gate is a tool-level error, not a pipeline-level structural gate.

### Recursive Delegation

When `allow_redelegate: true`:

- The subagent receives `delegate` and `cancel_delegation` in its tool set.
- A child Delegation Registry is injected into the subagent context with `depth = parent_depth + 1`.
- The launcher checks `registry.Depth() >= OrchestratorConfig.MaxRedelegationDepth` (default 2) before building the subagent. At the cap, the delegation fails with a descriptive error.
- Redelegating subagents are launched individually (not through `RunSubAgentsParallel`) because each needs its own per-task context with the child registry.
- The subagent's `delegate` calls use the same `max_steps` budget as regular delegations (configurable per-task via `max_steps`).

### Dependency Context Injection

Before a subagent runs, outputs from its `depends_on` delegations are injected into its task description (`conductorLauncher.buildTaskDescription`):

```
subagent task = task field + "\n## Context from previous delegations\n"
  + For each dep in depends_on (in declaration order):
      "\n### [{dep.ID}]: {dep.Summary}\n{dep.Output}\n"
```

The **combined** context is tail-truncated to `OrchestratorConfig.MaxDependencyContextChars` (default 8000), shared across all dependencies (not divided per-dependency), and aligned forward to a UTF-8 rune boundary so the tail does not begin inside a multi-byte sequence.

## Error Handling

- **Subagent returns `Finished: false`**: stored as `Status: "failed"` with the abort reason as the error; the `delegate` tool result for that task has `isError: true` with the abort reason. The Conductor decides whether to retry, reflect, or finish.
- **Subagent returns error**: same as above.
- **Subagent context cancelled** (via `cancel_delegation` or parent cancellation): stored as `Status: "cancelled"`; no error propagated to the Conductor (cancellation is intentional).
- **Paused checkpoint cannot seed the ContextManager**: `buildSubAgentTask` returns a descriptive error before launch; the checkpoint remains intact and is not replaced by a fresh run.
- **Validation failure** (duplicate ID, cycle, depth exceeded): the `delegate` tool call returns `isError: true` with a descriptive message; no subagents launch.
- **Dependency failed**: a task whose `depends_on` includes a failed or cancelled delegation is marked "failed" with reason "dependency del_X failed"; it does not run.

## Invariants

- Delegation IDs are unique within a Conductor run (and within each child registry for recursive delegations).
- The combined graph of delegations across all `delegate` calls in a Conductor run is always a valid DAG (no cycles).
- A subagent's context is always a child of the Conductor's context, so cancelling the Conductor cancels all subagents.
- A subagent never shares its `ContextManager` with the Conductor or with other subagents.
- Every subagent Executor receives the session pause checker; a cooperative pause stores the subagent's partial trajectory in its blackboard `StepResult` with `agent.ErrPaused`.
- A resumed paused subagent seeds the same checkpoint into both a `StepSeedable` ContextManager and the Executor; step numbering and the returned trajectory continue from the checkpoint. A non-seedable ContextManager fails task construction before launch.
- The `system` group (agent-infrastructure/meta tools) is always included in a subagent's toolset regardless of the `tools` field; toolsets resolve from capability groups via `resolveTaskTools` (`core/conductor.go`) — see [ADR-024](../../decisions/024-group-policies.md).
- `delegate`, `cancel_delegation` are available to a subagent only when `allow_redelegate` is true; `declare_plan` and `reflect` remain Conductor-only.
- Recursive delegation depth never exceeds `OrchestratorConfig.MaxRedelegationDepth`.
- The Delegation Registry is scoped to a single Conductor run (or a single subagent run for child registries); it does not persist across sessions.
- `finish` with pending async delegations returns a tool error (via `finishJoinExecutor` wrapping the tool executor) unless each pending delegation has been cancelled or completed.
- Subagent success requires `result.Finished && !DetectToolCallSyntaxInContent(result.Output)` (defense-in-depth, unchanged from the prior subagent logic).

## Related Specs

- [sp4rk Subagents](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/subagents.md) — canonical `RunSubAgent`/`RunSubAgentsParallel` primitive (isolated executor in a goroutine, trajectory capture, defense-in-depth)
- [conductor.md](conductor.md) — the Conductor that invokes `delegate`
- [executor.md](executor.md) — the ReAct loop primitive shared by Conductor and subagents
- [README.md](README.md) — orchestration overview
- [../../contracts/conductor-tools.md](../../contracts/conductor-tools.md) — Conductor tool surface contract
