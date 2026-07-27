# Goal Mode

## Purpose

Goal mode is a multi-turn, agent-driven execution loop that pursues a single user-approved success condition to completion. Instead of a single route→Conductor pass that finishes when the agent calls `finish`, a goal request derives a crisp {condition, verify} pair (with user sign-off), then iterates the Conductor turn-by-turn until the agent **declares the goal met**, the **budget is exhausted**, the agent goes **idle (anti-spin)**, or the goal is **paused**. Goal mode is selected by a leading `/goal` command or an explicit goal flag (e.g. a UI toggle) on any message — including a continuation message, in which case the goal loop runs on the restored blackboard of the prior task (see [Mechanism](#mechanism)).

## Key Files

- `core/goal/types.go` — the `goal` domain package: `GoalStatus`, `GoalBudget`, `GoalEvidence`, `Verdict`, `GoalState` (the runtime state machine; `LastVerification` carries the independent-verifier outcome marker `""`/`confirmed`/`rejected`/`off`)
- `core/orchestrator_goal.go` — the goal loop: `deriveGoal`, `runGoalLoop`, `resumeGoalLoop`, `runGoalTurns`, budget/pause/anti-spin logic, the `countingToolExec` wrapper (anti-spin tool-call counter), `emitGoalStatus`/`emitGoalProgress`, `PauseGoal`; the **independent-verification gate** in `runGoalTurns` (the "met" branch); `defaultGoalVerifier` (the production verifier pass), `resolveGoalVerifier` (verifier nil→default resolution + test seam), `verifierToolFilter`/`verifierExcludedToolNames` (the read-only/test toolset), `verificationMaxSteps` (the fixed step cap), `goalVerifierDefaultRejectReason` (synthesized rejection reason), `renderReportedEvidence`
- `core/orchestrator.go` — `HandleMessage` dispatch (`opts.Goal`), `Resume` goal-loop branch, `WithGoalState`, `activeGoalPause atomic.Pointer[atomic.Bool]` field (cross-goroutine pause signal), `ApplyRequestOverrides` (shared step 0 for HandleMessage and the resume path), the `goalVerifier` field (the verifier injection seam), `GoalLoopSettings.Verification` (runtime config field mirroring the config layer)
- `core/message_preprocess.go` — `DetectAndStripGoalMode` (`/goal` prefix detection)
- `core/types.go` — `HandleOptions.Goal`, `HandleOptions.GoalBudgetOverride`
- `core/systemprompt.go` — goal-mode system-prompt section rendering (`prompts.GoalModeSubstitute`) and the derivation prompt selection (`prompts.GoalDerivation`); `renderGoalModeVolatile` (the per-turn volatile section, including the one-shot rejection notice when the prior "met" was rejected by the verifier)
- `core/tools/propose_goal.go` — `propose_goal` tool + `GoalProposer` interface + context plumbing (`WithGoalProposer`/`GoalProposerFrom`)
- `core/tools/declare_goal_status.go` — `declare_goal_status` tool + `GoalStatusSink` interface + context plumbing (`WithGoalStatusSink`/`GoalStatusSinkFrom`) — the agent's **primary** self-evaluation verdict channel
- `core/tools/declare_verification.go` — `declare_verification` tool (`PolicyAlwaysAllow` internal tool — the verifier's ONLY verdict channel) + `VerificationOutcome` + `VerificationSink` interface (`Declare`/`Last`) + context plumbing (`WithVerificationSink`/`VerificationSinkFrom`). Mirrors `declare_goal_status`'s evidence mandate (`confirmed=true` requires non-empty evidence)
- `core/prompts/goal_verification_substitute.go` — `GoalVerificationSubstitute` (resolves the `prompts.GoalVerification` directive's condition/verify/reported-evidence placeholders and substitutes the shell-tool name)
- `backend/config/config.go` / `backend/config/defaults.go` — `GoalLoopConfig.Verification` (`independent` default | `off`), validated in `Validate`
- `desktop/startup_phases.go` — `goalProposerAdapter` (the desktop `GoalProposer` that emits `goal_proposal` and blocks for the user response)
- `desktop/startup.go` — goal-proposal pending map, `goal_proposal_response` event handler, goal-proposal resolver wiring
- `backend/frontend_api_goal.go` — RPC surface: `ConfirmGoal`/`CancelGoal`/`PauseGoal`/`ResumeGoal`/`ClearGoal`
- `backend/session/manager_goal.go` — `ResumeGoal`, `SetGoalProposalResolver`, `ResolveGoalProposal`, `PauseGoal`, `ClearGoal`
- `backend/session/events.go` — `GoalProposalPayload`

## Core Types

```go
// goal.GoalStatus — the lifecycle state machine (string enum; round-trips through JSON)
//   active        — in force; the agent is working toward it
//   paused        — suspended (user pause); resumable
//   met           — condition satisfied (terminal success)
//   exhausted     — budget consumed without meeting the condition (terminal failure)
//   blocked_idle  — agent cannot make further progress and is idle (terminal-ish; resumable)
//   cancelled     — abandoned by the user (terminal)

type GoalBudget struct {
    MaxTurns int // 0 = unlimited
}

type GoalEvidence struct {
    Type    string // test_output | file | command | qualitative
    Ref     string // artifact reference (path, command, id, or note)
    Summary string // human-readable description
}

type Verdict struct {
    Status     string         // "met" | "not_met" | "blocked" (free-form; the loop maps onto GoalStatus)
    Evidence   []GoalEvidence
    Reason     string
    DeclaredAt time.Time
}

type GoalState struct {
    Condition    string     // natural-language success condition ("what does done look like?")
    VerifyClause string     // checkable predicate for the condition
    Budget       GoalBudget
    TurnCount    int        // turns spent so far
    Status       GoalStatus
    LastVerdict  *Verdict   // most recent verification outcome (nil = none yet)
    CreatedAt    time.Time
}
```

## Mechanism

Goal mode is a three-phase loop. Entry is **per-message**: `HandleMessage` checks `opts.Goal` and dispatches to `runGoalLoop`. This holds on **both a fresh task (`TaskID == ""`) and a continuation (`TaskID != ""`)** — there is no `TaskID` gate.

- **Fresh task (`TaskID == ""`)** — the goal loop runs against a freshly-created blackboard; routing is decided by the full router (`routeOrContinue` falls through to `routeAndActivateSkills`).
- **Continuation (`TaskID != ""`)** — the goal loop runs **on the restored blackboard of the prior task**. The restored blackboard carries the inherited facts, the prior plan/trajectory, and the accumulated conversation history. A fresh `{condition, verify}` goal is derived from the new continuation message (with user sign-off), and routing is **reused** from the restored task via `routeOrContinue`'s continuation fast-path when the restored blackboard carries BOTH a plan and a routing decision (the router is blind to the restored plan and would misclassify a continuation message); when no plan/routing was persisted it falls through to the full router, exactly as a normal continuation does. In short: **goal on a continuation inherits the prior task's blackboard (facts, plan, trajectory, history) and routing, and derives a new goal from the new message.**

The continuation-inheritance semantics apply only on the goal-loop entry path (`runGoalLoop`); a non-goal continuation (`opts.Goal == false`) takes the normal Conductor continuation path unchanged. Goal state from the prior task is **not** inherited on a goal-on-continuation — a brand-new `GoalState` is derived from the new message and replaces any prior goal state, since the prior task had either completed (terminal) or never ran a goal loop.

### Phase 1 — Derivation (deriveGoal)

A full-context Conductor pass whose only job is to derive a crisp {condition, verify} goal from the user's request and submit it for user sign-off. It is a **Conductor run with a different instruction set**, not a separate engine:

1. The `GoalProposer` (desktop-supplied) is wrapped in a `capturingProposer` and injected into the Conductor context (`tools.WithGoalProposer`).
2. `ensureProposeGoalTool` guarantees the `propose_goal` tool descriptor is in the available-tool list (rebuilt from the registry if absent).
3. The Conductor system prompt is overridden with `prompts.GoalDerivation`; the rest of the Conductor wiring (context injection, trajectory, tool executor/registry) is reused.
4. `RunConductor` runs. The agent grounds the goal in the actual codebase using its normal read/search/probe tools, then calls `propose_goal` with `{condition, verify, clarification?, needs_clarification?}`.
5. `propose_goal` delegates to the `GoalProposer`, which emits a `goal_proposal` event and **blocks** until the user responds.
6. After the run, `buildGoalState` reconstructs the `GoalState` from the captured proposal + response. On "approve" it **prefers the user's edited condition/verify** over the agent's wording (unedited fields fall back to the proposal). The budget is left unlimited here; caps are applied at activation.

If the agent never calls `propose_goal`, the run cancelled, or the user cancelled the proposal, `deriveGoal` returns an error and the loop exits cleanly with the original message as output.

### Phase 2 — Activation

`runGoalLoop` resolves the budget (`resolveGoalBudget`: applies `opts.GoalBudgetOverride` — `MaxTurns` when set, otherwise unlimited), stamps `Status = active`, persists the `GoalState`, and installs the pause signal. Conversation history is truncated once to the configured window; the trajectory accumulates across turns via the blackboard.

### Phase 3 — Turn iteration (runGoalTurns)

```
for gs.Status == active:
  ┌─ top of turn: pause signal set? → Status=paused, emit goal_status, break (release single-flight)
  │  ctx cancelled? → Status=paused, break
  │
  ├─ run ONE turn via the turn runner (fresh Executor.Run via RunConductor)
  │   — every turn: ReAct+Conductor consuming the pre-established routing
  │   — NO turn routes — routing was decided once at the top of runGoalLoop,
  │     before derivation (re-routing a continuation misclassifies it; see Routing Invariant)
  │   — tool executor wrapped in countingToolExec (per-turn tool-call count for anti-spin)
  │   — context carries a fresh GoalStatusSink (declare_goal_status writes into it)
  │   — context carries the GoalState (WithGoalState) so the system prompt renders the goal
  │
  ├─ read the verdict sink:
  │     "met"     → INDEPENDENT VERIFICATION GATE (see § Independent Verification):
  │                  ┌─ verification "off" → Status=met, break (evidence-mandate-only)
  │                  ├─ verifier resolved (resolveGoalVerifier; nil seam → confirm)
  │                  ├─ confirmed → Status=met, break (the agent's verdict stands)
  │                  └─ rejected (Confirmed=false, nil outcome, or error)
  │                       → synthesize not_met {reason} into gs.LastVerdict,
  │                         set gs.LastVerification="rejected", emit goal_status,
  │                         CONTINUE the loop (no break, no TurnCount re-increment
  │                         — the agent turn already counted; falls through to
  │                         anti-spin / budget guards). A rejected "met" can
  │                         NEVER terminate the goal as met.
  │     "blocked" → Status=blocked_idle, break
  │     "not_met" → keep iterating
  │
  ├─ anti-spin: toolCalls == 0 AND no verdict → Status=blocked_idle, break
  │
  ├─ budget checks (any hit → Status=exhausted, break):
  │     MaxTurns > 0 && TurnCount >= MaxTurns
  │     MaxTurns == 0 && TurnCount >= goalLoopMaxTurns (hard ceiling = 50, only when no explicit turn cap is set)
  │
  └─ emit goal_progress (turn/budget telemetry) + goal_status (full snapshot)
```

A turn **error does not abort the loop** — the agent may recover next turn (`_ = terr`). The Conductor's conversation history accumulates across turns via the blackboard, so dialogue context is preserved.

## Routing Invariant (One Routing Decision per Goal Task)

Routing is established **exactly once, at the top of `runGoalLoop`, before the derivation pass**, and inherited unchanged by derivation and every goal turn. It is **never** re-done on a continuation turn.

**What happens, once.** `runGoalLoop`'s first act — before `deriveGoal` — is `routeOrContinue`, the same continuation-aware routing helper the normal `HandleMessage` path uses:

- **Fresh task (`opts.TaskID == ""`)** — no restored routing exists, so `routeOrContinue` falls through to `routeAndActivateSkills`, a full routing pass that classifies the message and activates skills.
- **Continuation (`opts.TaskID != ""`)** — when the restored blackboard carries BOTH a plan and a routing decision, `routeOrContinue` reuses the routing via its continuation fast-path and **does not** re-run the router; when neither is persisted it falls through to the full router. This is the same behavior the normal continuation path has; the goal loop just inherits it.

Either way, the routing pass (when it runs) produces the `RoutingDecision`: **domain** (`code`/`research`/`general`/`mixed`), **complexity** (`[1,5]`), and **matched skills**; merges in **user-specified skills** (`opts.UserSkills`); applies **skill-policy overrides** to the per-session tool registry; enriches the context (`WithDomain`, `WithComplexity`, `WithActiveSkills`, `WithUserSkills`) and persists the decision (`SetRouting`) so finalization and resume see it.

That enriched context is then threaded into **`deriveGoal` and every goal turn** (`runGoalTurns`). The turn runner (`defaultGoalTurnRunner` → `RunConductor`) only *consumes* routing from the context — it never calls the router. So **no turn, turn 1 included, re-routes**.

**Why route before derive (and never again).**

- **Derivation must ground the goal against the real domain and active skills.** A `code` goal should frame `verify` around `go test`/`go lint` evidence; a `research` goal around qualitative artifacts. The derivation agent also needs the active-skill instructions in its system prompt to shape a realistic {condition, verify} pair. Routing first means derivation sees the same domain context + skills the working turns will.
- **Turns need domain/complexity for step-limit and compaction.** `RunConductor` derives the per-turn ReAct step ceiling from complexity (`stepsPerComplexity = 20`, i.e. `complexity × 20`) and the compaction strategy from domain+complexity (`compactionStrategyForDomain`). Both come from the single routing decision carried on the context.
- **Skills drive tool-policy overrides.** Skill-activated policy overrides are applied once at routing time and inherited by every turn's registry; re-routing would re-apply (or drop) them mid-goal.

**The invariant — one routing decision per goal task.** This mirrors the normal orchestrator's continuation fast-path (`routeOrContinue` skips the router when a restored task already has a routing decision): a goal-loop turn's message is a continuation of the same task, not a new request. Re-routing it would misclassify a continuation (e.g. the router sees "continue working" and picks a different domain/complexity), destabilizing the system prompt and skill set mid-goal. Note that on a **goal-on-continuation** entry (a continuation message with `opts.Goal == true`), routing is reused from the restored task at the top of `runGoalLoop` via `routeOrContinue` — the goal loop never re-routes the inherited routing. See [../decisions/019-goal-mode.md](../decisions/019-goal-mode.md) §6 and [orchestration/router.md](orchestration/router.md) (continuation fast-path) for the rationale.

**Resume reuses, never re-routes.** `resumeGoalLoop` receives the persisted `RoutingDecision` and re-installs it (`SetRouting`) **without** calling `routeOrContinue`/`routeAndActivateSkills`; when none was persisted it falls back to the `general` domain. A resumed goal therefore continues under the same routing it was started with.

## Self-Evaluation: `declare_goal_status`

The single channel through which the loop learns a structured verdict is the `declare_goal_status` tool (an internal tool — `PolicyAlwaysAllow`, bypasses the tool judge). It writes a typed `goal.Verdict` into the per-turn `GoalStatusSink`; the loop reads the sink after each turn.

**Evidence mandate**: declaring status `"met"` **requires non-empty evidence** — at least one `{type, ref, summary}` artifact (changed file path, test output, command result). Enforced at the tool boundary so a bare "done" can never terminate the goal loop without a concrete, inspectable artifact. The tool executor does **not** validate inputs against the JSON schema, so the check rejects both an absent array **and** a present-but-empty entry (e.g. `evidence:[{}]` or `evidence:[{"ref":""}]`): each entry must have non-empty `type`, `ref`, and `summary` after trimming. A `met` verdict that fails this check is rejected with an error and the loop keeps iterating.

The agent is self-evaluating its own work — see the ADR for the rationale (self-agent + evidence-mandate as the primary verdict, with an independent verification backstop).

## Independent Verification

The agent's `declare_goal_status` verdict is the **primary** signal, but it is a single unverified assertion — a sufficiently convincing agent can declare `"met"` with fabricated-but-plausible evidence. An **independent verification backstop** re-checks each claimed `"met"` before the goal terminates. It is the mechanism Decision 1 of [../decisions/019-goal-mode.md](../decisions/019-goal-mode.md) adopts (the constrained A2+C form — a verifier that reuses the working agent's own skills, read-only/test toolset, and project context, with a verify-by-executing directive).

**When it runs.** Only after the agent declares `"met"` **with evidence** (i.e. the evidence mandate already passed). It runs **once per claimed "met"**, never on every turn, and never on `not_met`/`blocked`/idle turns.

**What it is — a control-plane pass, NOT a goal turn.** The verifier is an **isolated `RunConductor` pass** launched inside the held single-flight, **between two agent turns**. It is explicitly **not** a goal-loop turn:

- It does **not** increment `TurnCount` and is **not** counted against `MaxTurns`. A rejected `"met"` therefore costs the budget **one agent turn + one verifier pass** (the agent turn already counted; the verifier is free of the turn budget).
- It does **not** route — it inherits the routing decision established once at goal entry (see [Routing Invariant](#routing-invariant-one-routing-decision-per-goal-task)). It is part of the control plane that *governs* the turn loop, not part of it.
- It reuses all Conductor wiring (context injection, trajectory, tool executor/registry) and inherits the **active skills + project-context prefix** via `buildSpecializedSystemPrompt` + `prompts.GoalVerification` (resolved by `GoalVerificationSubstitute` with the condition, the verify clause, and the agent's **reported evidence presented as unverified claims** to re-check).

**Bounded and read-only.** The pass is bounded by `verificationMaxSteps` — a small **fixed** step cap (`12`), **not** derived from routing complexity — so a focused re-check cannot run unbounded. Its toolset is built by `verifierToolFilter`: read-only/meta tools, the platform shell-execution tool (`bash_exec`/`posh_exec`, so it can re-run the verify clause e.g. `go test`), MCP tools, and `declare_verification`. It **hard-excludes** (`verifierExcludedToolNames`) all mutating tools (`write_file`/`edit_file`/`delete_*`/`create_directory`) and all goal-control/coordination tools (`declare_goal_status`, `declare_step_complete`, `declare_plan`, `delegate`, `subagent`, `propose_goal`, `reflect`, `cancel_delegation`) — the verifier's ONLY output channel is `declare_verification`; it cannot loop the loop, change the goal it is judging, or escape its read-only mandate.

**The verdict — `declare_verification` into a `VerificationSink`.** The verifier reports a `tools.VerificationOutcome {Confirmed, Reason, Evidence, DeclaredAt}` through `declare_verification` (an internal `PolicyAlwaysAllow` tool, mirroring `declare_goal_status`) into a fresh per-pass `VerificationSink` (`memVerificationSink`). A confirmed verdict **requires non-empty evidence**, mirroring the evidence mandate on the agent's own `"met"` — the verifier must back its confirmation with concrete artifacts too. A pass that ends **without** declaring a verdict (hit the step budget, errored, or context cancelled) is treated as a **REJECT** — the condition could not be independently confirmed.

**The gate in `runGoalTurns`.** The `"met"` branch is intercepted:

- **`verification: "off"`** → `Status=met`, break (evidence-mandate-only behavior; reproduces the original loop exactly).
- **No verifier resolved** (`resolveGoalVerifier` returns nil — the defensive seam) → `Status=met`, break (treated as confirmed).
- **Confirmed** → `Status=met`, break. The agent's verdict stands; termination is unchanged from the pre-verifier behavior (plus one verifier pass).
- **Rejected** (`Confirmed==false`, a nil outcome, or an error) → the `"met"` is overridden: a synthesized `not_met` verdict carrying the rejection reason (the verifier's `Reason`, or `goalVerifierDefaultRejectReason` when none) is assigned to `gs.LastVerdict`, `gs.LastVerification` is set to `"rejected"`, `goal_status` is emitted, and the loop **CONTINUES** — no break, no `TurnCount` re-increment (the agent turn already counted). The turn then falls through to the normal anti-spin / budget guards. **A `"met"` rejected by the verifier can never terminate the goal as met.**

**Rejection feedback to the next agent turn.** The marker `gs.LastVerification` (`""` / `"confirmed"` / `"rejected"` / `"off"`) is the single value threaded through two consumers. It is reset to `""` at the top of each agent turn — **after** that turn's prompt already rendered the prior marker — so the rejection notice is **one-shot**: it surfaces on exactly the turn after the rejected `"met"` claim, then is gone. `renderGoalModeVolatile` (`core/systemprompt.go`) reads `LastVerification == "rejected"` and prepends a prominent notice — *"Previous met claim was REJECTED by independent verification: \<reason\>. Address this before re-declaring met."* (reason from the synthesized `gs.LastVerdict.Reason`) — before the budget line. `emitGoalStatus` carries the same marker out via the `verification` event meta key (see [Events](#events)). This makes the rejection visible to exactly the one turn that must address it, without trajectory-plumbing.

**Configuration.** Gated by `goal_loop.verification` (`independent` default | `off`), defined in `backend/config/config.go` (`GoalLoopConfig`), defaulted in `backend/config/defaults.go`, and validated in `Validate`. See [Budgets](#budgets) / [Configuration](#configuration).

## Lifecycle States

```
                            deriveGoal
                          (propose_goal)
                                │
                          ┌─────▼─────┐
              approve ──▶ │  active    │ ◀── resume (paused/active re-enter)
                          └─────┬──────┘
                                │ turn loop
            ┌───────────┬───────┼────────┬───────────┐
            ▼           ▼       ▼        ▼           ▼
         met       exhausted  paused  blocked   cancelled
       (terminal)  (terminal)         _idle    (terminal)
                    (budget)    ▲      (resumable)
                                │ PauseGoal signal / ctx cancel
                                │
                          (re-enters active on Resume)
```

Terminal states (`met`, `exhausted`, `cancelled`) are never re-entered — `Resume` guards on `IsTerminal()` before delegating to `resumeGoalLoop`. `paused` and `blocked_idle` are resumable: a paused goal is re-activated to `active` on resume so the `for gs.Status == active` guard enters the loop.

## Budgets

`GoalBudget` caps the resources the agent may spend. **It is turn-only**: the single dimension is `MaxTurns`, and `MaxTurns == 0` means "unlimited". The agent stops only when the turn cap is exceeded.

- **Resolution**: `resolveGoalBudget(opts.GoalBudgetOverride)` — the override sets MaxTurns when it is non-zero; a nil override (or `MaxTurns == 0`) means unlimited. There are no config-level defaults now; the budget is resolved entirely from the per-message override.
- **Override path**: parsed from a JSON string in `SendMessage`'s goal-budget field (`backend/session/manager_execution.go` `parseGoalBudget`); an empty/invalid string falls back to unlimited so a malformed budget never blocks a send. The frontend `BudgetCombobox` offers ∞ / 3 / 5 / 10 turns plus a custom turn-count input, producing `{"max_turns":N}`.
- **Hard ceiling**: `goalLoopMaxTurns = 50` is a safety net applied **only when no explicit turn cap is set** (`Budget.MaxTurns == 0`). It does **not** override an explicitly-set `MaxTurns`: the override contract ("the override wins for the field it sets") means a caller that sets `MaxTurns > goalLoopMaxTurns` is entitled to that many turns. The ceiling only guards the no-cap case so a truly-unlimited goal is never an actually-infinite loop.

Budgets are applied at **activation (turn 1)**, not at derivation — derivation only decides WHAT the goal is, not how much it may cost.

## Anti-Spin

A turn that made **zero tool calls AND declared no verdict** is idle — the agent is stuck and further turns would likely repeat the same non-action. The loop halts such a turn as `blocked_idle` rather than spinning. The per-turn tool-call count comes from the `countingToolExec` wrapper installed around the turn's tool executor.

## Persistence & Resume

The `GoalState` is persisted so a paused/active goal survives app restart and resumes into the loop. It is **best-effort** (`persistGoalStateBestEffort`): a missing task store or task ID (tests, non-persistent sessions) is a no-op, and a persistence failure is logged but never propagates — losing the checkpoint degrades only resumability, not the current run.

- **Persistence contract**: `PersistableBlackboard`/`TaskPersistence` (`core/persistent_blackboard.go`) — `PersistGoalState(taskID, gs)` / `LoadGoalState(taskID)`. The restored state is carried on `TaskState.GoalState` (nil for non-goal tasks). See [memory/blackboard.md](memory/blackboard.md).
- **Resume entry**: `Orchestrator.Resume` checks `goalState != nil && !goalState.Status.IsTerminal()` and delegates to `resumeGoalLoop` (non-terminal) instead of the plain Conductor path. Terminal statuses fall through to normal resume.
- **`resumeGoalLoop`** mirrors `runGoalLoop`'s post-derivation body but skips `deriveGoal` — the condition and verify clause are already known. A paused goal is re-activated to `active`. The prior trajectory (`resumeSteps`) is seeded into the executor on the **first resumed turn** via a once-flag wrapper so the resumed run continues the step counter/history from the checkpoint; the turn counter continues from `gs.TurnCount` (not reset to 1). Subsequent turns rely on the Conductor's own accumulated trajectory.
- **Backend resume**: `Manager.ResumeGoal` → `ResumeTask` loads the unfinished task + persisted `GoalState` and dispatches to the orchestrator's resume path. See [session-lifecycle.md](session-lifecycle.md).

## Events

Goal mode uses two channels: a dedicated session event for the proposal sign-off, and `service`-phase sub-events (via `ServiceWithMeta`) for loop telemetry.

| Event | Direction | Payload / Phase | Source | Description |
| ----- | --------- | --------------- | ------ | ----------- |
| `goal_proposal` | backend → frontend | `GoalProposalPayload {request_id, session_id, condition, verify, clarification?, needs_clarification}` | `goalProposerAdapter.Propose` | Derivation agent called `propose_goal`; surfaces as a pending action that **blocks the agent** until the user responds. Persisted (role `goal_proposal`) so it reappears on reload. |
| `goal_proposal_response` | frontend → backend | `{request_id, decision, condition?, verify?, clarification?}` | `GoalProposalPanel` (Approve/Cancel) | User's sign-off decision. Both the event path and the RPC path (`ConfirmGoal`/`CancelGoal`) funnel through a single resolver on the desktop pending map. |
| `goal_status` | backend → frontend | `service` event, `phase: "goal_status"`, meta: `{status, turn, condition, max_turns, verdict?, reason?, verification?}` | `emitGoalStatus` | Full goal snapshot, emitted on every state transition (paused/met/exhausted/blocked_idle) and after each turn. The `verification` meta key carries the independent-verifier outcome (`"confirmed"` / `"rejected"` / `"off"`) — present only when `gs.LastVerification` is one of those three values, i.e. immediately after a claimed `"met"` was adjudicated. `verdict`/`reason` reflect `gs.LastVerdict`; on a rejection, the synthesized `not_met` verdict carries the verifier's rejection reason. |
| `goal_progress` | backend → frontend | `service` event, `phase: "goal_progress"`, meta: `{turn, max_turns, condition}` | `emitGoalProgress` | Mid-loop turn/budget telemetry (emitted after a non-terminal turn). |

The `goal_status`/`goal_progress` events reuse the existing `ServiceWithMeta` channel with a `phase` discriminator — no new `Emitter`-interface methods. The frontend goal store (`useGoalEvents`) reconciles the store from these events; the `goal_proposal` event is handled by the goal handlers hook which writes both the goal store and a chat message (`goal_proposal` DisplayItem). See [../contracts/event-catalog.md](../contracts/event-catalog.md) and [frontend/events.md](frontend/events.md).

## Per-Turn `Executor.Run` Invariant

**Each goal-loop turn launches a fresh `Executor.Run`** (via `RunConductor`). The goal loop does **not** hold one long-lived executor across turns — it is a turn-of-Conductors, not a single multi-turn executor. Consequences:

- The Conductor owns the task within a turn until it calls `finish`; the loop then starts a new turn (a new `Executor.Run`).
- The Conductor's conversation history accumulates across turns via the blackboard trajectory (the same mechanism normal continuation resume uses), so dialogue context is preserved across the turn boundary despite each turn being a fresh executor.
- No turn routes. Routing (domain, complexity, matched+user skills, skill-policy overrides) is decided exactly once at the top of `runGoalLoop` — before derivation — and inherited unchanged by every turn; re-routing a continuation message would misclassify it. See [Routing Invariant](#routing-invariant-one-routing-decision-per-goal-task).
- The goal state and a fresh `GoalStatusSink` are injected into each turn's context (`WithGoalState`, `WithGoalStatusSink`); the system prompt renders the goal-mode section from the `GoalState` on every turn.

This is the same continuation convention the normal resume path uses (separate `Executor.Run`, trajectory-seeded), extended to iterate until the goal terminates.

> **The independent verifier is a fresh `RunConductor`, but NOT a goal turn.** The [Independent Verification](#independent-verification) backstop also launches an isolated `RunConductor` pass — but it is a **control-plane** pass, not one of the per-turn `Executor.Run`s this invariant covers: it does not increment `TurnCount`, is not counted against `MaxTurns`, and runs only after a claimed `"met"` with evidence.

## Single-Flight / Pause-Signal Interaction

The goal loop runs under the `Orchestrator` single-flight guard (`requestInFlight.CompareAndSwap`), acquired in `HandleMessage` **before** `runGoalLoop` is entered. The loop holds that guard for its entire multi-turn duration.

- **Pause is cooperative**: `PauseGoal()` loads the pointer (`o.activeGoalPause.Load()`) and sets the `atomic.Bool` it points to; the loop polls it at the **top of each turn** (not mid-turn). A pause transitions `Status → paused`, emits `goal_status`, and `break`s out of the turn loop — which releases the single-flight lock so a later `Resume` can re-enter.
- **Signal lifetime**: the pause signal is installed at loop entry (`o.activeGoalPause.Store(&atomic.Bool{})`) and cleared on exit (`defer func() { o.activeGoalPause.Store(nil) }()`), so a stale signal from a prior goal cannot pause a future non-goal request. The field itself is an `atomic.Pointer[atomic.Bool]` — **not** a bare pointer — because the write (in the `HandleMessage`/`Resume` goroutine) and the read (in `PauseGoal`, a Wails-RPC goroutine) are **not** both covered by the single-flight guard. Single-flight serializes `HandleMessage` calls against each other, but `PauseGoal` runs independently and may race a loop's entry/exit swap; the atomic pointer makes that swap race-free. The `*atomic.Bool` it points to is, of course, atomic.
- **No concurrent HandleMessage**: because the loop holds single-flight for its whole run, a second `HandleMessage` on the same orchestrator returns `ErrRequestInFlight` until the loop pauses/exits. Pause/Resume is the intended control flow, not concurrent requests.
- **Resume re-enters the loop**: `Resume` (the public task-resume path) takes its own single-flight-equivalent path via the resume goroutine; when it sees a non-terminal `GoalState` it calls `resumeGoalLoop`, which installs its own fresh pause signal.

## Configuration

The goal budget is **turn-only**. There are no config-level token or wall-clock caps; a per-goal turn limit is selected in the UI (`BudgetCombobox`: ∞ / 3 / 5 / 10 turns, or a custom number). An unlimited goal (∞) is bounded only by the internal `goalLoopMaxTurns = 50` safety ceiling. The independent verifier is gated by the config-level `goal_loop.verification` toggle (see [Independent Verification](#independent-verification)).

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| `HandleOptions.GoalBudgetOverride` | nil | Per-request JSON override (`{"max_turns":N}`); `MaxTurns` when set, otherwise unlimited |
| `goal_loop.verification` | `independent` | Whether the independent verification backstop re-checks each claimed `"met"` before the goal terminates. `independent` (default): run the bounded, read-only/test verifier pass — a rejected/ non-declaring verdict keeps the loop going; `off`: rely solely on the agent's `declare_goal_status` verdict + the evidence mandate (reproduces the pre-verifier loop). Defined in `backend/config/config.go` (`GoalLoopConfig`), defaulted in `backend/config/defaults.go`, validated in `Validate` (`independent`/`off`), surfaced at runtime as `OrchestratorConfig.GoalLoop.Verification` (`GoalLoopSettings`). |

## Invariants

- Goal mode is **per-message, both fresh and continuation**: `opts.Goal` (no `TaskID` gate). A goal on a continuation runs on the **restored blackboard** of the prior task — inheriting facts, plan/trajectory, conversation history, and routing — and derives a fresh `{condition, verify}` goal from the new message (the prior task's `GoalState`, if any, is not inherited). See [Mechanism](#mechanism) and [Routing Invariant](#routing-invariant-one-routing-decision-per-goal-task).
- **One routing decision per goal task.** Routing (domain, complexity, matched+user skills, skill-policy overrides) is established exactly once at the top of `runGoalLoop`, before derivation, and inherited unchanged by derivation + every goal turn; no turn re-routes, and `resumeGoalLoop` reuses the persisted routing. See [Routing Invariant](#routing-invariant-one-routing-decision-per-goal-task).
- Each goal-loop turn is a **fresh `Executor.Run`** (via `RunConductor); the loop is a turn-of-Conductors, not one long-lived executor.
- A `met` verdict **requires non-empty evidence** with each entry having non-empty `type`/`ref`/`summary` (enforced in `declare_goal_status`); a bare "done" cannot terminate the loop.
- **Independent verification is control-plane, not a goal turn.** When enabled (`goal_loop.verification: independent`, the default), the verifier pass that re-checks a claimed `"met"` runs **between two agent turns** inside the held single-flight; it does **not** increment `TurnCount` and is **not** counted against `MaxTurns` — a rejected `"met"` costs the budget one agent turn + one verifier pass (the agent turn already counted). See [Independent Verification](#independent-verification).
- **A `"met"` rejected by the verifier can never terminate the goal as met.** Only a *confirmed* claim (or `verification: off`, or the nil-verifier seam) terminates as `met`; a rejected/non-declaring claim synthesizes a `not_met` verdict, feeds the reason back into the next agent turn, and continues the loop without re-incrementing the turn counter.
- Anti-spin: a turn with **zero tool calls AND no verdict** halts as `blocked_idle`.
- The pause signal is cleared on loop exit; a stale signal never affects a future request.
- The loop holds the single-flight guard for its entire multi-turn run; `PauseGoal` releases it by breaking out.
- Terminal goal states (`met`, `exhausted`, `cancelled`) are never re-entered; `Resume` guards on `IsTerminal()`.
- The `GoalState` is persisted best-effort; a persistence failure never propagates (degrades only resumability).
- A turn error does not abort the loop — the agent may recover next turn.
- The budget is resolved at activation (turn 1), not at derivation.
- `propose_goal` and `declare_goal_status` are internal tools (`PolicyAlwaysAllow`) — coordination primitives, not user-facing capabilities.

## Related Specs

- [orchestration/README.md](orchestration/README.md) — HandleMessage flow, the goal-loop dispatch point
- [orchestration/executor.md](orchestration/executor.md) — the `Executor.Run` primitive launched once per turn
- [memory/blackboard.md](memory/blackboard.md) — `GoalState` persistence and `TaskState.GoalState`
- [session-lifecycle.md](session-lifecycle.md) — task resume, `resumeGoalLoop`, `ResumeGoal`
- [tool-system/builtins.md](tool-system/builtins.md) — `propose_goal` and `declare_goal_status` registration
- [../contracts/event-catalog.md](../contracts/event-catalog.md) — `goal_proposal`, `goal_proposal_response`, `goal_status`/`goal_progress` phases
- [frontend/events.md](frontend/events.md) — goal event handlers
- [frontend/rendering.md](frontend/rendering.md) — `goal_proposal` DisplayItem
- [frontend/stores.md](frontend/stores.md) — the goal store
- [../decisions/019-goal-mode.md](../decisions/019-goal-mode.md) — rationale for the six core decisions
