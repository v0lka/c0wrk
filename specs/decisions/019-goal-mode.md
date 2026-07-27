# ADR-019: Goal Mode

## Status

Accepted

## Context

The Conductor pipeline (ADR-012) runs a single `Executor.Run` that finishes
when the agent calls `finish`. That is the right model for most tasks: the
agent owns the task end-to-end and decides when it is done. But a class of
requests — "make all tests pass", "land this feature behind a flag and verify
the build is green" — are better modeled as a **persistent objective** that the
agent pursues across multiple Conductor runs, re-evaluating after each turn
whether the success condition has actually been reached. Without an explicit
goal abstraction, three problems recur:

1. **Premature finish.** The agent calls `finish` after a plausible-looking
   change without actually verifying the condition, and the loop ends on a
   claim rather than evidence.
2. **No termination contract.** There is no single, machine-checkable
   predicate the loop consults to decide "are we done?" — termination is the
   agent's unstructured `finish` call.
3. **No resource bound.** A task that keeps trying the same non-working
   approach spins indefinitely (or until the step limit), with no turn/token
   budget and no idle-detection.

Goal mode addresses this by deriving a crisp {condition, verify} pair with
user sign-off, then iterating the Conductor turn-by-turn until the agent
**declares the goal met (with evidence)**, the budget is exhausted, the agent
goes idle, or the goal is paused.

## Decision

Six core decisions shape goal mode.

### 1. Self-agent + evidence-mandate as the primary verdict, with an independent verification backstop

The agent that does the work is the **primary** evaluator of whether the goal is
met, via the `declare_goal_status` tool. Termination is driven by the working
agent's structured verdict.

To keep that self-evaluation trustworthy, declaring status `"met"` **requires
non-empty evidence** — at least one `{type, ref, summary}` artifact (changed
file path, test output, command result). The evidence mandate is enforced at
the tool boundary so a bare "done" can never terminate the loop.

On top of the primary verdict, an **independent verification backstop** now
re-checks each claimed "met" before the goal terminates. When the agent declares
`"met"` (with evidence) and verification is enabled (the default), the loop runs
an **isolated control-plane Conductor pass** — bounded by `verificationMaxSteps`
(a small fixed step cap, not derived from routing complexity), restricted to a
read-only/test toolset, and reporting through `declare_verification` — that
re-runs the verify clause against the claimed artifacts. The verifier is
**control-plane, not a goal turn**: it does not increment `TurnCount` and is not
counted against `MaxTurns`. A confirmed claim terminates exactly as a bare "met"
did before; a **rejected** (or non-declaring) claim cannot terminate the goal —
the loop continues and the rejection reason is fed back into the next agent
turn's prompt. The backstop is configurable: `goal_loop.verification` =
`independent` (default) | `off` (`off` reproduces the original
evidence-mandate-only behavior). The full mechanics are in
[../domains/goal-mode.md](../domains/goal-mode.md) § Independent Verification.

**Rationale revisited.** The original version of this decision rejected an
external evaluator on three grounds. That rejection is **revisited** here: a
backstop verifier is adopted, but in a constrained form — call it A2+C: a
verifier (A2) that reuses the working agent's own skills, read-only/test
toolset, and project context, plus a verify-by-executing directive (C) — that
answers each of the three original objections:

- **(a) Cost.** The original objection was that an evaluator *doubles the LLM
  cost per turn*. The backstop is a single bounded pass (`verificationMaxSteps`,
  a fixed cap independent of routing complexity), not a second full agent per
  turn — it runs **once per claimed "met"**, never on every turn — and it is
  fully disable-able via `goal_loop.verification: off`.
- **(b) Project-specific knowledge.** The original objection was that *a generic
  evaluator cannot know the project-specific verification predicate*. The
  verifier is not generic: it **inherits the active skills and project-context
  prefix** via `buildSpecializedSystemPrompt` + `prompts.GoalVerification`, and is
  pointed at the **verify clause** — which is exactly the project-specific
  predicate the derivation agent grounded in the codebase (e.g. `go test ./core/...
  passes`).
- **(c) Out-of-scope harness.** The original objection was that *wiring a real
  verifier (running tests, executing commands) is project-and-stack-specific and
  out of scope for the core loop*. No new test-execution harness is built: the
  verifier reuses the **existing** `bash_exec` (to re-run the verify clause) and
  the existing read-only tools on the same tool surface the working agent uses.

The self-agent + evidence-mandate remains the primary verdict: the backstop runs
only after a "met" with evidence, and a confirmed claim terminates unchanged. It
closes the gap the original Negative consequence flagged — a sufficiently
convincing agent declaring "met" with fabricated-but-plausible evidence — by
making termination contingent on an independent re-check rather than on a single
unverified assertion. The evidence is still inspectable by the user.

### 2. Derive-then-confirm UX (not a slash-command for the condition)

Goal mode is entered by a leading `/goal` command on the first message, but
the user does **not** write the condition/verify clauses. The derivation agent
(`deriveGoal`, a full-context Conductor pass with the `GoalDerivation` prompt)
grounds a {condition, verify} pair in the actual codebase and submits it for
sign-off via `propose_goal`. The user **approves, edits, asks for
clarification, or cancels** — the approved `GoalState` prefers the user's
edited wording over the agent's.

**Rationale.** Asking the user to hand-write a machine-checkable verify clause
is a high-friction entry that most users would skip or get wrong. The agent
already has the codebase context to draft a crisp, verifiable condition;
surfacing it as a reviewable, editable proposal gives the user a correction
opportunity without forcing them to do the authoring. This mirrors the
`plan_review` interaction model (approve-with-edits) that users already know.

### 3. Persist + pause/resume (not ephemeral)

The `GoalState` is persisted (`PersistGoalState`/`LoadGoalState`) so a
paused or active goal survives app restart and resumes into the loop. A
cooperative pause (`PauseGoal` sets an atomic the loop polls at the top of each
turn) transitions the goal to `paused`, releases the single-flight lock, and a
later `Resume` re-enters (`resumeGoalLoop`) with the prior trajectory seeded.

**Rationale.** A goal can run for many turns (the budget ceiling is 50 by
default, and unlimited-turn goals are supported). Forcing the user to keep the
app open and the loop running for that duration is brittle — a restart, a
pause-to-think, or a hand-off would discard progress. Persisting the state and
supporting pause/resume makes a long goal durable and interruptible, matching
the resumability guarantees of normal tasks. The persistence is best-effort so
a missing store never blocks a run.

### 4. Anti-spin auto-pause (not infinite retries)

A turn that made **zero tool calls AND declared no verdict** is halted as
`blocked_idle`. The loop does not keep iterating an idle agent.

**Rationale.** Without idle-detection, an agent that gets stuck in a
no-op cycle (thinking without acting, repeating the same reasoning) would burn
the entire turn/token budget repeating the same non-action. The zero-tool-call
signal is a cheap, robust proxy for "the agent made no progress this turn".
Halting as `blocked_idle` (a resumable state) rather than `exhausted` (terminal
failure) leaves room for the user to nudge or resume, rather than treating
stuck-ness as final budget exhaustion.

### 5. Per-turn `Executor.Run` (not one long-lived executor)

Each goal-loop turn launches a **fresh `Executor.Run`** via `RunConductor`.
The loop is a turn-of-Conductors, not a single multi-turn executor that holds
the ReAct loop across turns.

**Rationale.** Reusing the normal Conductor + continuation-trajectory mechanism
(rather than building a bespoke multi-turn executor) means goal mode inherits
all of the Conductor's behavior — routing, skills, context management,
compaction, delegation — unchanged per turn. The Conductor owns the task within
a turn until `finish`; the loop then starts the next turn. Dialogue context is
preserved across the turn boundary through the blackboard trajectory, the same
mechanism normal task-resume uses. Building a separate long-lived executor
would duplicate and diverge from the Conductor's tested semantics.

### 6. Route once at goal entry (before derive), never re-route a turn

`runGoalLoop` performs exactly one routing pass — `routeAndActivateSkills` at
its very top, **before `deriveGoal`** — to establish the goal's routing decision
(domain, complexity, matched+user skills) and apply skill-policy overrides. The
enriched context (domain, complexity, active/user skills) then flows into
`deriveGoal` and **every** goal turn; no turn re-routes, and `resumeGoalLoop`
reuses the persisted routing rather than re-routing.

**Rationale.** The router classifies a message's intent; a goal-loop turn's
message is a continuation of the same task, not a new request. Re-routing it
would misclassify a continuation (e.g. the router sees "continue working" and
picks a different domain/complexity), destabilizing the system prompt and skill
set mid-goal. Establishing the single routing decision once at goal entry — and
deriving the goal against the real domain + active skills — keeps the goal's
execution context stable across turns. This is the same continuation fast-path
the normal orchestrator uses (`routeOrContinue` skips the router when a restored
task has existing routing). See [../domains/goal-mode.md](../domains/goal-mode.md) § Routing Invariant.

## Consequences

**Positive:**

- Termination is governed by a structured, evidence-backed verdict instead of
  an unstructured `finish` call — the goal cannot end on a bare "done".
- Resource bounds (turns/tokens/deadline) cap runaway spend; an explicit budget
  makes a long goal predictable.
- Idle-detection prevents the most common infinite-loop failure mode cheaply.
- Persist + pause/resume make long goals durable and interruptible.
- Goal mode reuses the entire Conductor stack per turn (no bespoke executor),
  so it inherits routing, skills, compaction, and delegation for free.

**Negative:**

- Self-evaluation can be wrong: a sufficiently convincing agent could declare
  `met` with fabricated-but-plausible evidence. Mitigated on two layers — the
  evidence mandate (concrete, inspectable artifacts) and, by default, the
  independent verification backstop (Decision 1), which re-checks each claimed
  "met" via a read-only/test pass and never lets a non-confirmed claim
  terminate the goal; a "met" the verifier rejects loops back with its reason.
  Set `goal_loop.verification: off` to rely solely on the evidence mandate +
  audit, in which case this risk reverts to the original mitigation.
- A goal holds the single-flight lock for its entire multi-turn run, so a
  second request on the same orchestrator is rejected until the goal
  pauses/exits. Pause/Resume is the intended control flow, not concurrent
  requests.
- The derivation pass is an extra full-context Conductor run before any "real"
  work — a cost the user opts into via `/goal`.

## Alternatives Considered

- **External evaluator / verifier loop.** A second LLM call or a
  test-execution harness consulted by the loop to confirm "met". The *unbounded*
  form (a generic evaluator, every turn, with a bespoke test harness) is
  rejected (Decision 1) — it doubles cost without project-specific knowledge and
  is out of scope. Decision 1's *revisitation* adopts a **constrained** verifier
  backstop (A2+C): a single bounded, read-only/test pass that inherits the
  active skills + project context and re-runs the verify clause, gated behind
  `goal_loop.verification` (`independent` default / `off`). It closes the
  fabricated-evidence gap without re-introducing the unbounded evaluator.
- **Slash-command condition authoring.** `/goal <condition>` where the user
  writes the condition/verify directly. Rejected (Decision 2): high-friction,
  error-prone entry; the agent is better positioned to draft a verifiable
  condition from the codebase.
- **Ephemeral goal (no persistence).** Run the loop in-memory only. Rejected
  (Decision 3): long goals would be discarded on restart/pause, defeating the
  point of a durable objective.
- **No anti-spin (trust the budget).** Rely solely on turn/token caps to end a
  stuck loop. Rejected (Decision 4): a stuck agent burns the entire budget
  repeating no-ops; idle-detection is a cheap early-exit.
- **One long-lived multi-turn executor.** Hold a single `Executor.Run` across
  all turns. Rejected (Decision 5): would duplicate and diverge from the
  Conductor's tested per-run semantics; the per-turn approach reuses the whole
  stack.
- **Re-route every turn.** Run the router each turn for a fresh classification.
  Rejected (Decision 6): misclassifies continuation messages and destabilizes
  the execution context mid-goal.

## Related Specs

- [domains/goal-mode.md](../domains/goal-mode.md) — the full goal-mode domain spec
- [domains/orchestration/README.md](../domains/orchestration/README.md) — HandleMessage flow and the goal-loop dispatch point
- [decisions/012-conductor-orchestration-pipeline.md](012-conductor-orchestration-pipeline.md) — the Conductor pipeline goal mode reuses per turn
