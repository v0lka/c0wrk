# ADR-019: Goal Mode

## Status

Accepted

> **Drift note (2026-08-25, vibespec-check):** The budget consequence mentions turns/tokens/deadline caps; only the turn budget exists — GoalBudget is a turn-only cap (MaxTurns, 0 = unlimited, core/goal/types.go); tokens are counted per turn for display and no goal-level deadline is enforced.

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
goes idle, or the task is paused (a session-level control).

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
an **isolated control-plane Conductor pass** — running on a **fresh blackboard
that is isolated from the still-active goal task** (it does not inherit the
working task's partial plan or pending step state), **seeded with the met turn's
work product** so the verifier can inspect what was actually produced, restricted
to a read-only/test toolset, and reporting through `declare_verification`. The
verifier is **as capable as the executor**: it runs on the **same
complexity-derived step budget as a normal executor run** (`complexity ×
stepsPerComplexity`), not a tight, cheap bounded pass. It is **control-plane,
not a goal turn**: it does not increment `TurnCount` and is not counted against
`MaxTurns` — the turn-budget distinction holds, but the verifier's own
per-pass step budget is full.

**Mode-driven verification.** *How* the verifier checks "done" is set per goal at
derivation time and stored on `GoalState.VerificationMode` (editable by the user
at the approval step):

- **`executable` (default)** — the verify clause is a runnable predicate (a test
  run, a command, a check). The verifier independently re-runs it over a
  read-only/test toolset and lets the pass/fail decide the verdict.
- **`re_derivation`** — "done" cannot be settled by a single command and must be
  proven by re-running an open-ended process whose clean outcome is itself the
  proof (review, audit, refactor-until-clean). The verifier **delegates a fresh,
  read-only execution of the goal's process** via `delegate` and confirms *only*
  if that run comes back clean, citing the delegated run's own findings.

A confirmed claim terminates exactly as a bare "met" did before; a **rejected**
(or non-declaring) claim cannot terminate the goal — the loop continues and the
rejection reason is fed back into the next agent turn's prompt. The backstop has
two knobs: the **global on/off gate** `goal_loop.verification` = `independent`
(default) | `off` (`off` reproduces the original evidence-mandate-only behavior),
and the **per-goal `VerificationMode`** (`executable`/`re_derivation`, which only
matters while the gate is `independent`). The full mechanics are in
[../domains/goal-mode.md](../domains/goal-mode.md) § Independent Verification.

**Rationale revisited.** The original version of this decision rejected an
external evaluator on three grounds. That rejection is **revisited** here: a
backstop verifier is adopted, in a constrained form whose constraints are no
longer "cheap and shallow" but rather **capability parity under isolation**:

- **Capability parity (same step budget as the executor).** The verifier is now
  *as capable as the executor*: it runs on the same `complexity ×
  stepsPerComplexity` step budget as a normal executor run, not a tight, cheap
  bounded pass. A shallow probe cannot catch a fabricated-but-plausible "met";
  only a genuinely capable re-check can. The cost this introduces is paid only
  for claims of "met" (see (a)).
- **Isolation from the active task (fresh blackboard).** The verifier runs on a
  **fresh blackboard**, not the goal loop's blackboard, so it is a genuinely
  separate execution context with no leak of the still-active task's incomplete
  state (partial plan, pending step outputs). It is **seeded with the met turn's
  work product** (the turn's `Output`), so the verifier can inspect what was
  actually produced without depending on the working session's live trajectory.
- **Read-only/test toolset, control-plane placement.** Every mutating tool and
  every goal-control tool is hard-excluded; the verifier's only output channel
  is `declare_verification`. It runs between two agent turns inside the held
  single-flight, does **not** increment `TurnCount`, and is **not** counted
  against `MaxTurns`.
- **Mode-driven verification.** *How* "done" is checked is chosen per goal
  (`GoalState.VerificationMode`): `executable` (default) re-runs a runnable
  verify clause; `re_derivation` delegates a fresh read-only run of the goal's
  process and confirms only on a clean outcome. Matching the verification to the
  nature of "done" is what makes an `executable` goal cheap and decisive while
  letting a re_derivation goal pay the heavier delegation cost only when the
  predicate genuinely cannot be a single command.

This constrained form answers each of the three original objections:

- **(a) Cost.** The original objection was that an evaluator *doubles the LLM
  cost per turn*. The verifier is no longer cheap — it is executor-scale by
  design — so cost is controlled structurally instead: it runs **once per
  claimed "met"**, never on every turn; it is control-plane (not counted against
  `MaxTurns`, so it does not inflate the turn budget); the per-goal mode keeps
  `executable` goals cheap while only `re_derivation` goals pay the full
  delegation cost; and the whole backstop is disable-able via
  `goal_loop.verification: off`.
- **(b) Project-specific knowledge.** The original objection was that *a generic
  evaluator cannot know the project-specific verification predicate*. The
  verifier is not generic: it **inherits the active skills and project-context
  prefix** via `buildSpecializedSystemPrompt`, and is pointed at the **verify
  clause** — which is exactly the project-specific predicate the derivation agent
  grounded in the codebase (e.g. `go test ./core/... passes`). The mode-specific
  directive (`prompts.GoalVerification` for `executable`, `prompts.GoalReDerivation`
  for `re_derivation`, selected by `GoalVerificationDirectiveByMode`) is
  substituted with that clause by `GoalVerificationSubstitute`.
- **(c) Out-of-scope harness.** The original objection was that *wiring a real
  verifier (running tests, executing commands) is project-and-stack-specific and
  out of scope for the core loop*. No new test-execution harness is built:
  `executable` mode reuses the **existing** `bash_exec` (to re-run the verify
  clause) and read-only tools on the same tool surface the working agent uses;
  `re_derivation` mode reuses the **existing** `delegate` sub-agent mechanism —
  no bespoke re-execution harness is introduced.

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
sign-off via `propose_goal`. The user **approves (optionally with edits) or cancels** — the approved
`GoalState` prefers the user's edited wording over the agent's. Ambiguous
requests are disambiguated earlier, at derivation time, via `ask_user` (the
derivation agent asks follow-up questions before proposing), not via a clarify
mode on the proposal itself.

**Rationale.** Asking the user to hand-write a machine-checkable verify clause
is a high-friction entry that most users would skip or get wrong. The agent
already has the codebase context to draft a crisp, verifiable condition;
surfacing it as a reviewable, editable proposal gives the user a correction
opportunity without forcing them to do the authoring. This mirrors the
`plan_review` interaction model (approve-with-edits) that users already know.

### 3. Persist + pause/resume (not ephemeral)

The `GoalState` is persisted (`PersistGoalState`/`LoadGoalState`) so a
paused or active goal survives app restart and resumes into the loop. A
cooperative pause is a **session-level** control (`PauseSession` flips a
universal pause signal that every conductor run's pause-checker reads at each
step boundary; the executor stops mid-turn with `ErrPaused` →
`ExecutionStatusPaused`) — it persists the **task** as paused and exits,
releasing the single-flight lock. The **goal stays `active`** (pause is
task-level); a later `ResumeSession` re-enters (`resumeGoalLoop`) with the
prior trajectory seeded. There is no goal-specific `PauseGoal`; the same
`PauseSession`/`ResumeSession` RPCs serve all tasks (goal and non-goal alike).

**Rationale.** A goal can run for many turns (an explicit turn budget, or an
unlimited ∞ goal the user controls via pause/stop). Forcing the user to keep the
app open and the loop running for that duration is brittle — a restart, a
pause-to-think, or a hand-off would discard progress. Persisting the state and
supporting pause/resume makes a long goal durable and interruptible, matching
the resumability guarantees of normal tasks. Making pause session-level (rather
than goal-specific) means one control surface serves every task and the pause
semantics are identical whether or not a goal is active. The persistence is
best-effort so a missing store never blocks a run.

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
  "met" via a read-only/test pass on an **isolated fresh blackboard** (seeded
  with the met turn's work product), driven by the goal's `VerificationMode`
  (`executable`/`re_derivation`), and never lets a non-confirmed claim terminate
  the goal; a "met" the verifier rejects loops back with its reason. The
  verifier is **executor-scale** (same step budget as a working turn), so each
  claimed "met" costs one extra full pass — but only then, and never per turn.
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
  backstop: a capable (executor-scale, same step budget) but **isolated** (fresh
  blackboard, read-only/test toolset) pass that inherits the active skills +
  project context and re-checks the goal's outcome according to its
  `VerificationMode` (`executable` re-runs the verify clause; `re_derivation`
  delegates a fresh read-only run of the process), gated behind
  `goal_loop.verification` (`independent` default / `off`). The *cheap-bounded-pass*
  variant was considered and dropped: a shallow probe cannot catch a
  fabricated-but-plausible "met", so the verifier was raised to executor
  capability while the cost is instead controlled by running only per claimed
  "met", staying off the turn budget, and being disable-able. It closes the
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
