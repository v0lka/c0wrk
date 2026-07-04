# Conductor

## Role

A single `Executor.Run` instance that owns a user task end-to-end, using the ReAct loop to think, call tools (including `delegate` for subagents and `declare_plan` for roadmaps), and finish. The Conductor is the only top-level execution entry point in the orchestration domain.

## Key Files

- `core/conductor.go` — Conductor entry point: builds system prompt, assembles tool set, injects Delegation Registry into context, launches `Executor.Run`
- `core/orchestrator_handle.go` — HandleMessage body that invokes the Conductor after routing
- `sdk/agent/executor.go` — `Executor.Run` (the ReAct loop; the Conductor is an Executor configured with Conductor-specific tools and prompt)
- `core/systemprompt.go` — `buildSystemPrompt` and Conductor-specific prompt sections
- `core/delegation_registry.go` — Delegation Registry injected into the Conductor context

## Behavior

### Lifecycle

```
Conductor.Run(ctx, message, routing, activeSkills, opts)
│
├─ 1. Build system prompt
│     ├─ Core: orchestrator system + family overlay + verification mandate
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
│     │    set_step_status, read_step_output, semantic_search
│     └─ Conductor tools (always present):
│          delegate, declare_plan, reflect, cancel_delegation
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
| Task involves multiple files or subsystems | Call `delegate` with one task per coherent unit of work. |
| Requirements are ambiguous | Call `ask_user` before delegating or implementing. |
| An interactive skill is active and prescribes an approval gate | Call `declare_plan` with `mode: "await_approval"` before implementing. |
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

### Step Limit and Circuit Breakers

The Conductor is subject to `config.Conductor.MaxSteps` (default 80) and the same circuit breakers as any `Executor.Run` instance (repeat, truncation, parse error, fruitless, same tool). On step-limit or circuit-breaker abort, `HITLHandler.OnStepLimit` is called with the same three options (AllowOnce, AllowAlways, Deny) as the prior executor. See [executor.md](executor.md) for circuit breaker details.

### Wrap-Up Nudge

A generalization of the existing `handleWrapUpNudge` heuristic nudges the Conductor when it has performed many tool calls without finishing:

- After N tool calls without `delegate` or `finish`: "This task looks large. Consider calling `delegate` to break it into subtasks, or `finish` if you are done."
- After a `delegate` returns: "Review the subagent's output. If the task is complete, call `finish`. If more work is needed, delegate the next unit or continue inline."

These are soft nudges, not enforcement.

## Error Handling

- **LLM call failure** in the Conductor loop: propagated as an `Executor.Run` error; the orchestrator records the failure on the blackboard.
- **Delegation failure** (subagent error or `Finished: false`): returned as the `delegate` tool result with `isError: true`; the Conductor decides whether to retry, reflect, or finish. This replaces the prior outer retry loop.
- **Context cancelled** (user pressed Stop): propagated immediately; pending async delegations are cancelled via context-tree cancellation.
- **Conductor step limit reached without finish**: `Finished: false`; the orchestrator records `Status: partial` (resumable).

## Invariants

- Exactly one Conductor `Executor.Run` instance is active per task at any time.
- The Conductor tool set always includes `delegate`, `declare_plan`, `reflect`, `cancel_delegation`, `ask_user`, and `finish`, regardless of routing domain, skill policy overrides, or No Project mode. These are internal tools and bypass policy.
- Active skill bodies are rendered verbatim in the Conductor system prompt (no truncation).
- The Conductor context is isolated from subagent contexts: subagents carry their own `ContextManager`, and only their summaries return to the Conductor as tool results.
- The Delegation Registry is scoped to a single Conductor run; it is injected into the context at launch and does not outlive the run.
- `finish` with pending async delegations requires either a prior `cancel_delegation` for each pending ID, or an implicit join (the Conductor waits for all pending delegations before finishing).
- The Conductor never receives a fully-specified plan step as a task message (unlike the prior executor). Its task message is the user's original message plus routing context; decomposition is the Conductor's own decision via `delegate`.

## Related Specs

- [README.md](README.md) — orchestration overview
- [delegation.md](delegation.md) — delegate tool and Delegation Registry
- [executor.md](executor.md) — ReAct loop (the primitive the Conductor is built on)
- [router.md](router.md) — routing decision feeds the Conductor
- [../../contracts/conductor-tools.md](../../contracts/conductor-tools.md) — Conductor tool surface contract
- [../../decisions/012-conductor-orchestration-pipeline.md](../../decisions/012-conductor-orchestration-pipeline.md) — architectural decision
