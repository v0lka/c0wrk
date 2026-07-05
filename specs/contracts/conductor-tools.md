# Contract: Conductor Tools

## Boundary Rule

Conductor tools (`delegate`, `declare_plan`, `reflect`, `cancel_delegation`) are internal tools registered in `core/tools/registry.go` and added to the Conductor's tool set in `core/conductor.go`. They are always allowed (bypass policy and judge) and are never available to regular plan-step executors — only to the Conductor and, selectively, to subagents with `allow_redelegate: true`.

## Interfaces

| Interface | Package | Consumed By | Purpose |
| --------- | ------- | ----------- | ------- |
| `delegate` tool | `core/tools` | Conductor | Launch subagents with DAG + async |
| `cancel_delegation` tool | `core/tools` | Conductor, subagent (redelegate) | Cancel a pending/running async delegation |
| `declare_plan` tool | `core/tools` | Conductor | Publish a roadmap to the blackboard and UI; optionally block for user approval |
| `reflect` tool | `core/tools` | Conductor | Invoke the Reflector on the current trajectory or a sub-task trajectory |
| `DelegationRegistry` | `core` | `delegate`, `cancel_delegation`, `read_step_output`, `finish` (via context) | Track active/completed delegations for one Conductor run |
| `RunSubAgent` / `RunSubAgentsParallel` | `sdk/agent` | `delegate` tool | Execute an isolated `Executor.Run` in a goroutine |
| `Reflector.Reflect` | `sdk/agent/reflector` | `reflect` tool | Produce a Reflection (root cause, action plan, suggested action) |
| `SerializePlan` | `core/plan_serializer` | `declare_plan` tool | Serialize a Plan to markdown for UI display and plan-file persistence |
| `Plan` / `PlanStep` / `FindReadySteps` | `sdk/orchestration` | `delegate` tool, `declare_plan` tool | DAG data structures and traversal |

## Initialization

Conductor tools are registered at startup in `core/tools/builtin_registration.go` alongside the existing internal tools. They are added to the `internalTools` set in `core/tools/registry.go`:

```go
var internalTools = map[string]struct{}{
    // ... existing internal tools ...
    "delegate":           {},
    "cancel_delegation":  {},
    "declare_plan":       {},
    "reflect":            {},
}
```

The Conductor tool set is assembled in `core/conductor.go`:

```
conductorTools = filterByDomain(fileOps + search + web) +
                 internalTools +
                 { delegate, declare_plan, reflect, cancel_delegation }
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
  │    └─ SubAgentComplete emitted by RunSubAgent (SDK) — sole progress signal;
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
  │    "plan": { tasks: [...] },
  │    "mode": "present" | "await_approval"
  │  })
  │
  ▼
declare_plan.Execute(ctx, input)
  │
  ├─ Build a Plan from the input tasks (reuse PlanStep struct)
  ├─ SerializePlan(plan) → markdown
  ├─ Write to SessionPlansDir (from ctx) as <session_prefix>_<random6>.md
  ├─ Emit PlanGenerated event (same event type as the prior pipeline)
  ├─ Set plan on the blackboard
  │
  ├─ If mode == "await_approval":
  │    ├─ Call AskUserFunc with an approval prompt
  │    │   (options: Approve, Request changes, Abandon)
  │    ├─ Block until the user responds
  │    ├─ On "Approve": return tool result { approved: true }
  │    ├─ On "Request changes": return tool result { approved: false, feedback: "..." }
  │    │   (the Conductor revises and calls declare_plan again)
  │    └─ On "Abandon": return tool result { approved: false, abandoned: true }
  │
  └─ If mode == "present": return tool result { approved: null } (informational)
```

Direction: Conductor → `declare_plan` tool → blackboard + emitter + (optionally) `AskUserFunc`. The plan flows to the UI via the existing `PlanGenerated` event; the approval response flows back through the `AskUserFunc` callback (same mechanism as `ask_user`).

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
- If you change the `Plan` / `PlanStep` struct in `sdk/orchestration/types.go`, both `delegate` and `declare_plan` are affected (they share these types).
- If you change the `Reflector.Reflect` signature, update the `reflect` tool wrapper.
- If you remove or rename an internal tool, update the `internalTools` set and the "always available" invariants in [conductor.md](../domains/orchestration/conductor.md) and [delegation.md](../domains/orchestration/delegation.md).

## Related Specs

- [../domains/orchestration/conductor.md](../domains/orchestration/conductor.md) — Conductor component
- [../domains/orchestration/delegation.md](../domains/orchestration/delegation.md) — delegate tool and Delegation Registry detail
- [../domains/orchestration/executor.md](../domains/orchestration/executor.md) — ReAct loop primitive
- [../domains/tool-system/README.md](../domains/tool-system/README.md) — tool registry and execution pipeline
- [../decisions/012-conductor-orchestration-pipeline.md](../decisions/012-conductor-orchestration-pipeline.md) — architectural decision
