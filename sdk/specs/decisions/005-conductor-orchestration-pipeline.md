# ADR-005: Conductor-Driven Orchestration Pipeline

## Status

Accepted

## Context

The orchestration pipeline originally used a **system-driven** sequence of phases — `Router → (optional plan-review gate) → Planner → Executor-subagents → (optional Reflector on failure)` — where each phase was owned by the system and the LLM lived only inside fixed slots with a fixed mandate. Two structural problems emerged from this design.

### Problem 1: Interactive workflows are structurally unreachable

Portable skill documents (markdown shared across agents) can prescribe an **interactive workflow** with hard gates: ask clarifying questions before acting, present a roadmap for explicit approval, never implement until approved. These instructions are prose; the skill carries no agent-specific metadata and must remain portable.

In the system-driven pipeline these instructions could be injected into prompts as soft guidance, but the pipeline gave the LLM no capability to actually follow them:

- The **clarification gate was system-owned.** The Router produced a "needs clarification" verdict and the orchestrator decided whether to return early. A skill could not trigger a clarifying question mid-task.
- The **approval gate was system-owned.** Plan review ran only when the host toggled it on. A skill could not request approval at a point of its own choosing.
- **Clarification was suppressed for explicitly-invoked skills.** The pipeline suppressed "needs clarification" whenever a skill was explicitly invoked, on the rationale that an explicit invocation implies clear intent. For an interactive skill this is exactly backwards: an explicit invocation means "I want to think together", not "execute immediately".
- **The executor received conflicting mandates.** The skill body ("never write code here", "get approval") was injected into the executor prompt, but the executor's task message — a fully-specified plan step with acceptance criteria and a "complete this step" directive — was more specific and authoritative. The executor implemented, because the pipeline put it in execution phase with an execution mandate.

The observed failure: an interactive skill was invoked, the router returned "no clarification needed", the planner produced a single fully-specified step, the executor implemented the entire change, and the session ended without a single clarifying question or approval request. Every hard gate in the skill was bypassed.

### Problem 2: Pipeline rigidity beyond skills

- **Planner ran once.** Decomposition happened up front; mid-execution rethinking was possible only via the Reflector after a step failure. There was no "cancel the remaining steps, I have a better idea" path.
- **Reflector was reactive-only.** It fired on failure, never proactively.
- **ReAct mode was a degenerate plan.** "Simple" execution was a single-step plan through the full planner+executor machinery — overhead for "read a file and answer".
- **Two interaction points, both system-owned.** Clarification (router) and plan review (toggle) were the only places to intervene mid-task, and both were controlled by the system, not the agent.

### Constraint: skill portability

A fix that attaches agent-specific metadata to skills (e.g. an `interaction:` block declaring `requires_plan_approval: true`) was rejected: it would make the skill meaningful only for agents with a router, a planner, and a plan-review gate. Other consumers of the same skill directory have none of those. Portability is a hard constraint.

## Decision

Replace the system-driven pipeline with a **conductor-driven ReAct loop**: a single `Executor.Run` instance (the **Conductor**) owns the entire task lifecycle. Planning, decomposition, interaction, and reflection become **tool calls inside the loop**, not pipeline phases owned by the system.

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

### What sp4rk provides (engine foundation)

sp4rk supplies the load-bearing primitives that make the conductor design possible. These are general agent-building concerns and remain in the framework:

- `github.com/v0lka/sp4rk/agent` — `Executor.Run`, the ReAct loop with circuit breakers, truncation handling, implicit-finish detection, and a mutation gate.
- `github.com/v0lka/sp4rk/agent/subagent.go` — `RunSubAgent` / `RunSubAgentsParallel`: an isolated executor in a goroutine with task-context injection, a step ID, and a scoped emitter.
- `github.com/v0lka/sp4rk/agent/reflector` — the Reflector as a **callable library** (`Reflect`), no longer an external pipeline phase.
- `github.com/v0lka/sp4rk/orchestration` — the DAG data structures (`Plan`, `PlanStep`, ready-step resolution), the `Blackboard` (shared task state, facts, step outputs), and the `Conductor` that drives the agent-owned loop.
- `github.com/v0lka/sp4rk/planner` — `Plan`/`Replan` as a **library** used by conductor tools, not as a pipeline phase.
- The context manager, compaction strategies, tool-result cache, and two-stage truncation.

### What the host application supplies (conductor meta-tools)

The host wires the orchestration cycle by registering **meta-tools** into the sp4rk tool registry that the Conductor can call inside its loop. These are host-level orchestration policy, built on sp4rk primitives:

| Tool | Purpose | Built on |
| --- | --- | --- |
| `ask_user` | Interactive: clarifications, plan approval, direction choice | host-defined interaction types (ask-user UI types were removed from sp4rk per [004-application-concept-extraction.md](004-application-concept-extraction.md)) |
| `delegate` | Launch subagents with a task, acceptance criteria, tool set, DAG dependencies, blocking or async mode | `agent.RunSubAgent` / `RunSubAgentsParallel` |
| `declare_plan` | Publish a roadmap to the blackboard / host UI; optionally block for approval | `orchestration.Plan` serialization + the `PlanGenerated` event |
| `reflect` | Invoke the Reflector on the current or a sub-task trajectory | `agent/reflector.Reflect` |
| `store_fact` / `search_facts` | Memory across delegations | `orchestration.Blackboard` |
| `read_step_output` | Read results of completed delegations | `orchestration.Blackboard` step outputs |

### Delegation model

`delegate` accepts an **array of tasks** plus a DAG expressed via `depends_on`. It reuses the sub-agent mechanism: tasks with unsatisfied dependencies are held; ready tasks dispatch in parallel; results land on the blackboard and are returned to the Conductor as the tool result.

**Async mode:** `delegate` supports `mode: "blocking" | "async"`. Blocking returns the output in the tool result. Async returns a delegation ID immediately; the Conductor reads results later via `read_step_output`. A delegation registry (per Conductor run, in-context) tracks active and completed delegations, their lifecycle, and cancellation. Finishing with pending async delegations requires either joining (wait for all) or an explicit cancellation first.

### Recursion

Subagents are **flat by default** — they do not have the `delegate` tool. `delegate` accepts an opt-in flag to grant a subagent the ability to spawn further subagents, capped by a configurable depth and a reduced step budget. This prevents unbounded delegation trees while allowing hierarchical decomposition when explicitly requested.

### What is removed (clean break)

- `planner.Plan` / `Replan` / continuation as **pipeline phases**. The DAG data structures are retained as a library used by `delegate` and `declare_plan`.
- The plan-execute-reflect **outer loop**. There is no structural "plan_execute vs react" branch, no plan-review toggle stage, no system-owned clarification gate, and no "normal vs advanced" execution-mode toggle. The Conductor handles both simple and complex tasks in one loop; "simple" is a Conductor that never calls `delegate`.
- The Router's `routing.mode`. The Router retains only domain, complexity, matched skills, and model selection.
- The Reflector as an external phase. It remains a library invoked through the `reflect` tool.

### Skill enforcement, resolved structurally

The Conductor **owns the loop** and **has the tools** (`ask_user`, `declare_plan`) to follow interactive skill instructions. The skill remains pure markdown with no agent-specific metadata. Enforcement is soft by form (instruction-following) but **executable by capability**: the Conductor can ask a question, present a roadmap, and wait for approval because those are tool calls available inside its loop. If a model ignores a skill's instructions, that is an instruction-following failure, not an architectural one. Optional hard guardrails (e.g. blocking `delegate` until a `declare_plan` is approved) can be added later via per-skill tool-policy overrides, but are not required for the design to function.

## Consequences

**Positive:**

- **One pipeline, not four.** Router → Conductor. No plan_execute/react branch, no plan-review toggle, no clarification gate, no execution-mode toggle.
- **Skills work as written.** Interactive skills become executable because the Conductor can ask, present, and wait. Portability is preserved — the skill carries no agent-specific metadata.
- **Proactive reflection.** The Conductor can call `reflect` at any point, not only after a failure.
- **Mid-execution replan.** The Conductor cancels pending delegations and changes approach without a full restart.
- **No planner overhead for simple tasks.** "Read a file and answer" is a Conductor that never calls `delegate` — one ReAct loop, no plan generation.
- **Preserved foundation.** Executor, sub-agent, context manager, tool registry, blackboard — the sp4rk load-bearing primitives are untouched.

**Negative:**

- **Conductor context grows for long tasks.** Mitigated by compaction (already present) and by delegation — subagents carry the heavy context, the Conductor sees only summaries.
- **Risk of under-delegation.** The Conductor may try to do everything itself and overflow its context. Mitigated by system-prompt guidance and a "task looks large, consider delegate" heuristic.
- **Risk of over-delegation.** The Conductor may delegate too granularly. Mitigated by tool description and few-shot examples in the system prompt; an optional delegation count cap is available.
- **Async delegation complexity.** The delegation registry, lifecycle management, cancellation propagation, and finish-join semantics add surface area. Accepted because async unlocks parallel exploration and background work the system-driven pipeline could not express.

## Alternatives Considered

### A — Skill-specific interaction metadata in frontmatter

Add an `interaction:` block to skill YAML declaring `requires_clarification`, `requires_plan_approval`, `phases`, etc. The orchestrator reads these and forces clarification / approval / execution embargoes.

**Rejected:** violates portability. Other agents consuming the same skill directory have no router, no planner, no plan-review gate. The metadata would be dead weight there and behaviour would diverge across agents for the same skill file.

### B — Bind the skill approval gate to the existing plan-review toggle

When an interactive skill is active, force plan review on so a plan is presented for approval before execution.

**Rejected:** still system-driven. The approval point is fixed (after planning, before execution); a skill that wants to approve a partial plan mid-execution, or approve a design decision before planning, cannot. Also couples skill behaviour to a host UI toggle, which is fragile. Subsumed by the Conductor's `declare_plan` tool, which makes approval a tool call the agent invokes at a point of its own choosing.

### C — Fix the clarification suppression only

Remove the "needs clarification" suppression for explicitly-invoked skills, or make it metadata-aware.

**Rejected:** treats one symptom. The approval gate and the executor-conflicting-mandate problems remain. The suppression was a latent bug, but fixing it alone does not make interactive skills executable.

### D — Hardcode an interactive skill as a special execution mode

Add a special mode that runs the interactive workflow as a bespoke pipeline.

**Rejected:** kills skill portability (the mode is host-specific), proliferates modes (one per interactive skill), and addresses only one skill rather than the class of interactive skills. The Conductor design subsumes this: any interactive skill works because the Conductor has the tools and owns the loop.

## Related

- [001-separate-module.md](001-separate-module.md) — the orchestration engine and executor live in `github.com/v0lka/sp4rk`.
- [004-application-concept-extraction.md](004-application-concept-extraction.md) — the `ask_user` interaction types are host-owned; the framework supplies the executor loop and tool registry the Conductor builds on.
- [../contracts/agent-execution.md](../contracts/agent-execution.md) — `Executor.Run`, sub-agents, `Step`/`ExecutorResult`, and the `Events`/`HITLHandler` interfaces the Conductor drives.
- [../contracts/tools.md](../contracts/tools.md) — the `ToolRegistry` into which the host registers the conductor meta-tools.
