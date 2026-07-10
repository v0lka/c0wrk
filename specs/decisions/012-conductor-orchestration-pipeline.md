# ADR-012: Conductor-Driven Orchestration Pipeline

## Status

Accepted

## Context

The orchestration pipeline prior to this ADR was **system-driven**: a fixed
sequence of phases — `Router → (optional PlanReview gate) → Planner →
Executor-subagents → (optional Reflector on failure)` — where each phase was
owned by the system and the LLM lived only inside fixed slots with a fixed
mandate. Two structural problems emerged from this design.

### Problem 1: Skill enforcement is structurally unreachable

Agent Skills (`~/.agents/skills/*/SKILL.md`) are portable markdown documents
shared across agents (c0wrk, opencode, others). Some skills — `explore`,
`research-init`, `code-review` — prescribe an **interactive workflow** with
hard gates: ask clarifying questions before acting, present a roadmap for
explicit user approval, never implement until approved. These instructions are
prose; the skill carries no agent-specific metadata and must remain portable.

In the system-driven pipeline these instructions were injected into the
planner and executor system prompts as soft guidance
(`core/systemprompt.go` `formatActiveSkills`), but the pipeline gave the LLM
no capability to actually follow them:

- **Clarification gate was system-owned.** The Router produced
  `needsClarification` and the orchestrator decided whether to return early
  (`core/orchestrator_handle.go` clarification branch). A skill could not
  trigger a clarifying question mid-task.
- **Approval gate was system-owned.** `HandlePlanReview` ran only when the
  user toggled `PlanReview` (`core/orchestrator.go`). A skill could not
  request approval at a point of its own choosing.
- **Clarification was actively suppressed for explicitly-invoked skills.**
  `core/orchestrator_handle.go` suppressed `needsClarification` whenever
  `opts.UserSkills` was non-empty, on the rationale that an explicit `/skill`
  invocation implies clear intent. For an interactive skill like `explore`
  this is exactly backwards: `/explore` means "I want to think together", not
  "execute immediately".
- **Executor received conflicting mandates.** The skill body ("NEVER write
  code in Explore Mode", "get approval") was injected into the executor
  system prompt, but the executor's task message — a fully-specified plan
  step with acceptance criteria and a "complete this step, call finish"
  directive — was more specific and authoritative. The executor implemented,
  because the pipeline put it in execution phase with an execution mandate.

The observed failure: a user activated `/explore` for a multi-provider model
disambiguation task. The Router returned `needsClarification=false`; the
Planner produced a single step with full What/How/Where/Acceptance Criteria;
the Executor-subagent implemented the entire change across config, sp4rk, core,
backend, and frontend; the session ended without a single clarifying question
or approval request. Every hard gate in the skill was bypassed.

### Problem 2: Pipeline rigidity beyond skills

- **Planner ran once.** Decomposition happened up-front; mid-execution
  rethinking was possible only via the Reflector after a step failure. There
  was no "cancel the remaining steps, I have a better idea" path.
- **Reflector was reactive-only.** It fired on failure, never proactively
  ("I've done 3 steps and this direction seems wrong").
- **ReAct mode was a degenerate plan.** "Normal" execution mode was a
  single-step plan through the full planner+executor machinery — overhead
  for "read a file and answer".
- **Two interaction points, both system-owned.** Clarification (router) and
  plan review (toggle) were the only places the user could intervene
  mid-task, and both were controlled by the system, not the agent.

### Constraint: skill portability

A fix that attaches agent-specific metadata to skills (e.g. an
`interaction:` block in the frontmatter declaring `requires_plan_approval:
true`) was rejected: it would make the skill meaningful only for agents with
a router, a planner, and a plan-review gate. opencode and other consumers of
the same `~/.agents/skills/` directory have none of those. Portability is a
hard constraint.

## Decision

Replace the system-driven pipeline with a **conductor-driven ReAct loop**:
a single `Executor.Run` instance (the **Conductor**) owns the entire task
lifecycle. Planning, decomposition, interaction, and reflection become
**tool calls inside the loop**, not pipeline phases owned by the system.

```
                    ┌─────────────────────────────────────────┐
                    │            Conductor (ReAct)            │
                    │  one executor.Run, owns the task        │
                    │                                         │
   user msg  ──────▶│  think → tool → think → tool → ... → finish
                    │       │            │                    │
                    └───────┼────────────┼────────────────────┘
                            │            │
              ┌─────────────┼────────────┼──────────────┐
              ▼             ▼            ▼              ▼
         ask_user      delegate      declare_plan    reflect
         (interactive) (subagent)    (roadmap→UI)    (on trajectory)
                            │
                            ▼
                    ┌──────────────┐
                    │   SubAgent   │  isolated executor.Run
                    │  (ReAct loop)│  own ContextManager, scoped emitter
                    │  task+accept │  store_fact / read_step_output
                    │  → finish    │
                    └──────────────┘
```

### Conductor tool surface

| Tool | Purpose | Origin |
| ---- | ------- | ------ |
| `ask_user` | Interactive: clarifications, plan approval, direction choice | Reused from `core/tools/askuser.go` |
| `delegate` | Launch one or more subagents with a task, acceptance criteria, tool set, DAG dependencies, blocking or async mode | New, built on `github.com/v0lka/sp4rk/agent/subagent.go` `RunSubAgent` / `RunSubAgentsParallel` |
| `declare_plan` | Publish a roadmap to the blackboard and UI plan panel; optionally block for user approval | New, reuses `core/plan_serializer.go` `SerializePlan` and the existing `PlanGenerated` event |
| `reflect` | Invoke the Reflector on the current trajectory or a sub-task trajectory | New tool wrapping `github.com/v0lka/sp4rk/agent/reflector/reflector.go` `Reflect` |
| `store_fact` / `search_facts` | Memory across delegations | Reused |
| `read_step_output` | Read results of completed delegations | Reused |
| `set_step_status` | Visible todo progress within the Conductor | Reused |
| `semantic_search` / `search_code` / `ripgrep` / file ops | Codebase investigation | Reused |
| `finish` | End the task with a final answer | Reused |

### Delegation model

`delegate` accepts an **array of tasks** plus a DAG expressed via
`depends_on`. Internally it reuses the existing `SubAgentTask` /
`RunSubAgentsParallel` mechanism: tasks with unsatisfied dependencies are
held; ready tasks dispatch in parallel; results land on the blackboard and
are returned to the Conductor as the tool result.

**Async mode:** `delegate` supports `mode: "blocking" | "async"`. Blocking
returns the output in the tool result. Async returns a `delegation_id`
immediately; the Conductor reads results later via `read_step_output(id)`.
A **Delegation Registry** (in-context, per Conductor run) tracks active and
completed delegations, their lifecycle, and cancellation. `finish` with
pending async delegations requires either joining (wait for all) or explicit
`cancel_delegation` first.

### Recursion

Subagents are **flat by default** — they do not have the `delegate` tool.
`delegate` accepts `allow_redelegate: true` to grant a subagent the ability
to spawn further subagents, capped by a configurable depth (default 2) and a
reduced step budget. This prevents unbounded delegation trees while allowing
hierarchical decomposition when explicitly requested.

### What is removed (clean break)

- `github.com/v0lka/sp4rk/planner/planner.go` `Plan` / `Replan` / `PlanContinuation` as
  pipeline phases. The DAG data structures (`Plan`, `PlanStep`,
  `FindReadySteps`) are retained as a library used by `delegate` and
  `declare_plan`.
- `github.com/v0lka/sp4rk/orchestration/orchestrator.go` `executePlanWithSteps` and
  `runPlanExecute` (the plan-execute-reflect outer loop).
- `core/plan_review.go` `HandlePlanReview` as a pipeline stage. Its
  serialization and approval-callback logic moves into the `declare_plan`
  tool.
- `core/orchestrator_handle.go` clarification gate (the
  `NeedsClarification` early-return and the UserSkills suppression branch).
- `executionModeStore` ("normal" vs "advanced") as a structural pipeline
  branch. The Conductor handles both simple and complex tasks in one loop;
  "normal" is simply a Conductor that does not call `delegate`.
- Reflector as an external phase. It remains as a library invoked through
  the `reflect` tool.
- Router `routing.mode` (plan_execute vs react). The Router retains only
  domain, complexity, matched skills, and model selection.

### What is preserved (unchanged foundation)

- `github.com/v0lka/sp4rk/agent/executor.go` `Executor.Run` — the ReAct loop, circuit breakers,
  truncation handling, implicit-finish detection, mutation gate.
- `github.com/v0lka/sp4rk/agent/subagent.go` `RunSubAgent` / `RunSubAgentsParallel` —
  isolated executor in a goroutine with task-context injection, step ID,
  scoped emitter.
- Context manager, compaction strategies, tool result cache, two-stage
  truncation.
- `core/tools/registry.go` tool registry, policy enforcement, internal-tools
  set, HITL hooks.
- Blackboard, facts, step outputs, session persistence.
- Reflector (`github.com/v0lka/sp4rk/agent/reflector/reflector.go`) as a callable library.
- All frontend event types and the plan panel rendering — `declare_plan`
  emits the same `PlanGenerated` / `OnStepStarted` / `OnStepCompleted`
  events, so the UI requires no changes.

### Skill enforcement, resolved structurally

The Conductor **owns the loop** and **has the tools** (`ask_user`,
`declare_plan`) to follow interactive skill instructions. The skill remains
pure markdown with no agent-specific metadata. Enforcement is soft by form
(instruction-following) but **executable by capability**: the Conductor can
ask a question, present a roadmap, and wait for approval because those are
tool calls available inside its loop. If a model ignores a skill's
instructions, that is an instruction-following failure, not an architectural
one. Optional hard guardrails (e.g. blocking `delegate` until a
`declare_plan` is approved) can be added later via per-skill tool-policy
overrides (`core/orchestrator.go` `buildSkillPolicyOverrides` already
exists), but are not required for the design to function.

## Consequences

### Positive

- **One pipeline, not four.** Router → Conductor. No plan_execute/react
  branch, no plan_review toggle, no clarification gate, no executionMode
  toggle.
- **Skills work as written.** Interactive skills (`explore`, `research-init`,
  `code-review`) become executable because the Conductor can ask, present,
  and wait. Portability is preserved — the skill carries no agent-specific
  metadata.
- **Proactive reflection.** The Conductor can call `reflect` at any point,
  not only after a failure.
- **Mid-execution replan.** The Conductor cancels pending delegations and
  changes approach without a full restart.
- **No planner overhead for simple tasks.** "Read a file and answer" is a
  Conductor that never calls `delegate` — one ReAct loop, no plan
  generation.
- **Preserved UI.** `declare_plan` and `delegate` emit existing event types;
  the frontend plan panel and activity feed work unchanged.
- **Preserved foundation.** Executor, subagent, context manager, tool
  registry, blackboard — the load-bearing primitives are untouched.

### Negative

- **Conductor context grows for long tasks.** Mitigated by compaction
  (already present) and by delegation — subagents carry the heavy context,
  the Conductor sees only summaries.
- **Risk of under-delegation.** The Conductor may try to do everything
  itself and overflow its context. Mitigated by system-prompt guidance
  ("delegate multi-step work") and a generalization of the existing
  `handleWrapUpNudge` heuristic ("task looks large, consider delegate").
- **Risk of over-delegation.** The Conductor may delegate too granularly.
  Mitigated by tool description and few-shot examples in the system prompt;
  an optional delegation count cap is available.
- **Large test rewrite.** `core/orchestrator_test.go`,
  `core/planner_test.go`, and reflector-as-phase tests are invalidated.
  sp4rk-level tests (`github.com/v0lka/sp4rk/agent/executor_test.go`,
  `github.com/v0lka/sp4rk/agent/subagent_test.go`, `github.com/v0lka/sp4rk/agent/reflector/reflector_test.go`)
  are preserved — the foundation is unchanged.
- **Async delegation complexity.** The Delegation Registry, lifecycle
  management, cancellation propagation, and finish-join semantics add
  surface area. Accepted because async unlocks parallel exploration and
  background work that the system-driven pipeline could not express.

## Alternatives Considered

### A — Skill-specific interaction metadata in frontmatter

Add an `interaction:` block to skill YAML frontmatter declaring
`requires_clarification`, `requires_plan_approval`, `phases`, etc. The
orchestrator reads these and forces clarification / plan review / execution
embargoes.

**Rejected:** violates portability. opencode and other agents consuming
`~/.agents/skills/` have no router, no planner, no plan-review gate. The
metadata would be dead weight there and behaviour would diverge across
agents for the same skill file.

### B — Bind skill approval gate to the existing plan_review toggle

When an interactive skill is active, force `opts.PlanReview = true` so
`HandlePlanReview` presents the plan for approval before execution.

**Rejected:** still system-driven. The approval point is fixed (after
planning, before execution); a skill that wants to approve a partial plan
mid-execution, or approve a design decision before planning, cannot. Also
couples skill behaviour to a UI toggle, which is fragile. Subsumed by the
Conductor's `declare_plan` tool, which makes approval a tool call the agent
invokes at a point of its own choosing.

### C — Fix the clarification suppression only

Remove the `NeedsClarification` suppression for UserSkills in
`core/orchestrator_handle.go`, or make it metadata-aware.

**Rejected:** treats one symptom. The approval gate and the
executor-conflicting-mandate problems remain. The suppression was a latent
bug, but fixing it alone does not make interactive skills executable.

### D — Hardcode `explore` as a third execution mode alongside normal/advanced

Add an "explore" mode to `executionModeStore` that runs the explore workflow
as a special pipeline.

**Rejected:** kills skill portability (the mode is c0wrk-specific),
proliferates modes (one per interactive skill: `research-init`, `code-review`,
`deeper-research`...), and addresses only `explore` rather than the class of
interactive skills. The Conductor design subsumes this: any interactive
skill works because the Conductor has the tools and owns the loop.

## Related Specs

- Canonical sp4rk decision: [sdk/specs/decisions/005-conductor-orchestration-pipeline.md](../../sdk/specs/decisions/005-conductor-orchestration-pipeline.md) — the conductor-driven ReAct pipeline framed from the engine's perspective
- [../domains/orchestration/README.md](../domains/orchestration/README.md) — rewritten orchestration domain overview
- [../domains/orchestration/conductor.md](../domains/orchestration/conductor.md) — Conductor component detail
- [../domains/orchestration/delegation.md](../domains/orchestration/delegation.md) — delegate tool and async delegation registry
- [../contracts/conductor-tools.md](../contracts/conductor-tools.md) — Conductor tool surface contract
