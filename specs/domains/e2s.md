# E2S — Explicit Execution State Mode

## Purpose

E2S is an alternative execution mode in which the model's only memory is an **externalized, structured state Σ** ("sigma") that it reads and patches every turn, instead of replaying a growing conversation history. Each turn is a fresh one-shot request — system prompt + `[turn N of M] <state>Σₜ</state> <observation>Oₜ</observation>` — so the request size stays **O(1) in the number of turns** no matter how long the task runs. The mode is selected per message (`HandleOptions.E2S`), is mutually exclusive with goal mode, and is gated behind `experimental.enabled`. Design rationale and the arXiv 2608.26263 motivation: [decisions/039-e2s-explicit-execution-state.md](../decisions/039-e2s-explicit-execution-state.md); the stabilization revision (mutable extensions, observation hash recovery, working batch, budget visibility, semantic anti-spin, resumable step-limit): [decisions/040-e2s-stabilization.md](../decisions/040-e2s-stabilization.md); the self-sufficiency revision (no delegation, token-proportional observation budget, per-sub-call batch cache, slim `e2s.tools` catalog, scratchpad overflow channel): [decisions/071-e2s-self-sufficiency.md](../decisions/071-e2s-self-sufficiency.md).

## Key Files

- `core/e2s/types.go` — the domain layer (pure data, no LLM/tool imports): `E2SState` (Σ + bookkeeping), `StateStatus` (domain lifecycle: `active`/`paused`/`met`/`failed`/`cancelled`), the eight fixed **core keys** (`CoreKeyObjective`, `CoreKeyChecklist`, `CoreKeyFilesTouched`, `CoreKeyFindings`, `CoreKeyDecisions`, `CoreKeyNextSteps`, `CoreKeyDoneCriteria`, `CoreKeyStatus`), `SchemaFingerprint()` (SHA-256 over the sorted key→type pairs), `NewE2SState` (canonical initial state), `NotesFileName` (`e2s-notes.md` — the scratchpad contract: the overflow channel for raw evidence beyond the byte-capped Σ, reached through the ordinary `write_file`/`read_file` gates, no dedicated tool)
- `core/e2s/merge.go` — the merge operator `Σₜ⊕ΔΣₜ` (`ApplyPatch`): null-tombstone deletion (non-core keys only), core-key type validation (update-only — deletion/re-typing rejected), mutable extension keys (a value write replaces in place; ADR-040), byte-limit enforcement (`ErrStateTooLarge`, `DefaultStateByteLimit` = 16 KiB), schema-fingerprint guard (`ErrSchemaMismatch`); every failure is a sentinel-wrapped error (`ErrDeleteCoreKey`, `ErrCoreKeyType`, `ErrCoreKeyElement`, `ErrInvalidStatus`, `ErrEmptyKey`)
- `core/e2s/steptool.go` — the `e2s_step` meta-tool (`StepToolName`, **GroupSystem**): its JSON schema (`state_patch` object + `action` `{tool, args}` | `finish` `{answer}`), the canonical envelope constants quoted by every protocol error and by `BuildCorrectionSuffix`, `ParseStepCall` (first tool call must be `e2s_step`), `ActionFingerprint` (the semantic anti-spin fingerprint: tool + target anchors + content payloads, ignoring precision args like line ranges — ADR-040 §6; a batch fingerprints per sub-call, so a repeated identical batch still spins while a batch whose sub-target or payload changed does not); `Execute` is a wiring-error trap — the loop intercepts the call and never registry-dispatches it
- `core/e2s/prompt.go` — deterministic prompt composition: `BuildSystemPrompt` (core directive — `prompts.E2SSystem`, or `prompts.E2SSystemLite` under the Model Profiles Lite swap — + VerificationMandate [+ InjectionDefense when `security.injection_defense.enabled`] + Workspace (workspace/temp containment + the scratchpad section naming `<tempDir>/e2s-notes.md` and its overflow discipline) + Available Tools + Active Skills; no delegation/subagent sections — E2S is self-sufficient; sorted descriptors → prefix-cache-friendly), `availableToolsSection` (purpose line + the compact description-free input schema per tool — `compactSchema` strips only `description` keys, so parameter names/required/enums survive verbatim), `BuildUserMessage` (turn number + Σ JSON + observation), `BuildCorrectionSuffix` (the bounded-retry corrective tail: the triggering error text plus the exact `e2s_step`/finish envelope shapes)
- `core/e2s/loop.go` — the driver `Loop.Run`: one LLM call per turn on raw sp4rk primitives (`llm.Caller`, no `agent.Executor`/Conductor), `Config` knobs (`NotesPath` scratchpad path — derived from `TempDir` when unset; `ContextWindowTokens`, `ObservationBudgetTokens`, `ObservationFillFraction` — the token-proportional observation budget; `MaxObservationChars` — the no-window char fallback), `EffectiveObservationCapChars` (window-resolved: `min(budget, fill × window) × 4` chars), `RunStatus` terminal dispositions (`finished`/`step_limit`/`spin_stop`/`paused`/`canceled`/`failed`), `Result` (answer + final `Snapshot E2SState` + synthesized `agent.Step` trajectory), `ErrPaused` checkpoint, batch interception (`dispatchBatch`: per-sub-call full dispatch path with per-sub-result cache-on-truncate; nested batch and envelope targets rejected fail-closed), anti-spin nudge/abort, `Emitter`/`StateEmitter`/`Registry` structural interfaces
- `core/prompts/e2s.md` (+ `core/prompts/prompts.go` `E2SSystem`) and `core/prompts/e2s-lite.md` (`E2SSystemLite`, the Model Profiles Lite swap counterpart) — the compact E2S directive: protocol, state discipline, acting/finishing/safety rules
- `core/orchestrator_e2s.go` — orchestrator integration: `runE2SLoop`/`resumeE2SLoop`/`runE2SWithState` (the self-sufficient wiring — see below; per-step Σ persistence, trajectory store, skills resolution, staged-image restoration on resume), `e2sStrippedToolNames` + `stripE2SUnavailableTools` (the always-applied plan/delegation/goal stripping), `filterE2SToolsByConfig` (`e2s.tools` narrowing: preset core/all, allow, deny — deny absolute), `e2sRegistryAdapter` (filtered catalog + fail-closed dispatch + real tool sources), `e2sStatePersistingEmitter` (persist on every `e2s_state` emission), `e2sStatePersister` capability interface, status mappings (`e2sExecutionStatus`, `e2sDomainStatus`)
- `core/types.go` — `HandleOptions.E2S` (mutually exclusive with `Goal`), `Emitter.E2SState(data)` + noop emitter
- `core/emitter_logging.go` — logging emitter's `E2SState`
- `backend/config/config.go` + `backend/config/defaults.go` + `backend/configadapter.go` — `E2SConfig` (`e2s.*`) + `E2SToolsConfig` (`e2s.tools.*`) with defaults and load-time validation (preset closed-set; non-empty allow/deny entries; negative knobs rejected), gated fail-closed on `experimental.enabled` (configadapter gate pattern)
- `backend/frontend_api_session.go` — `SendMessage` e2s flag through the Wails RPC surface; rejects `e2s=true` when the experimental gate is off
- `backend/session/manager_execution.go` — session-manager threading of the e2s flag; live-send rejection for e2s sends into running tasks (mirrors the goal gate)
- `backend/session/persistence.go` — `task_e2s_state` table (`task_id` PK, `e2s_state` JSON, `updated_at`; `CREATE TABLE IF NOT EXISTS` is the migration), `SaveE2SState`/`LoadE2SState`
- `backend/session/task_adapter.go` — `PersistE2SState`/`LoadE2SState` (the `e2sStatePersister` implementation)
- `backend/session/persistence_fork.go` — copies `task_e2s_state` on session fork
- `frontend/src/components/chat/E2SToggle.tsx` — per-message mode toggle (visible only while the experimental master switch is on: `useExperimentalFeatures()`, config `experimental.enabled`), mutually exclusive with the goal toggle
- `frontend/src/stores/inputModeStore.ts` — `e2sEnabled` (persisted; enabling E2S disables goal and vice versa)
- `frontend/src/stores/e2sStore.ts` + `frontend/src/hooks/events/useE2SStateEvents.ts` + `e2sHandlers.ts` — per-session Σ snapshots from `e2s_state` events (the backend owns the merge — the store keeps the latest full Σ and replaces it outright; cleared on session switch/delete)
- `frontend/src/components/chat/ExecutionStatePanel.tsx` + `ExecutionPanels.tsx` — the Execution State panel that **replaces the plan view** for E2S sessions
- `frontend/src/types/events.ts` — `E2SSigma`/`E2SStateData` payload types + guard
- `frontend/src/api/chat.ts` — `sendMessage` e2s argument in the exact Go-binding position

## Core Types

```go
// core/e2s/types.go — the externalized state (round-trips through JSON for persistence)
type E2SState struct {
    Sigma     map[string]any // Σ: core keys per the fixed schema + mutable extensions
    Schema    string         // SchemaFingerprint() at creation; mismatch rejects patching
    TurnCount int            // number of applied patches
    Status    StateStatus    // typed copy of the core "status" key (re-synced by ApplyPatch)
    CreatedAt, UpdatedAt time.Time
}

// Domain lifecycle (persisted; drives resume decisions):
//   active | paused | met | failed | cancelled

// core/e2s/steptool.go — the model's per-turn output (the ONLY tool it may call)
// e2s_step schema: {
//   "state_patch": { <key>: <value|null> },     // null deletes non-core keys
//   "action": { "tool": "<name>|finish", "args": {...} }  // finish requires {"answer": ...}
// }

// core/e2s/loop.go — the loop's terminal disposition (distinct from the domain status)
type RunStatus string // finished | step_limit | spin_stop | paused | canceled | failed

type Result struct {
    Answer   string      // finish payload (StatusFinished only)
    Status   RunStatus
    Finished bool
    State    map[string]any // final Σ
    Snapshot E2SState       // full domain state — the persistence/resume checkpoint
    Turns    int
    Steps    []agent.Step   // synthesized Thought/Action/Observation trajectory
}
```

## Flow

```
 user message (E2S toggle on)
        │
        ▼
HandleMessage ── E2S && Goal both set? ──► explicit error (mutually exclusive)
        │ opts.E2S
        ▼  (early return — routing/Conductor never run)
  runE2SLoop ── loadE2SResumeState: persisted resumable Σ (active|paused,
        │      schema fingerprint current)? ──► resume with restored Σ
        ▼      else NewE2SState(objective = message + attachments)
  runE2SWithState
        │  filtered catalog, narrowing inward: e2s.tools config filter ──
        │  Model Profiles essential-tools ── StripGoalModeTools +
        │  stripE2SUnavailableTools (goal/plan/delegation tools never reach
        │  the model; the adapter rejects every filtered name fail-closed)
        │  scratchpad path derived from TempDir (<temp>/e2s-notes.md)
        │  wire per-step Σ persistence (e2sStatePersistingEmitter → task_e2s_state)
        ▼
   e2s.Loop.Run ──────────────────────────────────────────────┐
   for turn := 1..MaxSteps:                                   │
     pause-check / ctx-cancel at the step boundary ──► ErrPaused (checkpoint)
     ONE LLM call: fresh [system P, user (Σₜ, Oₜ)]            │
       └─ response must be exactly one e2s_step tool call     │
            state_patch ─► ApplyPatch (validate + merge)      │
            │   invalid → up to patch_retries retries → error   │
            │   observation, Σ untouched (rollback)           │
            └─► emit e2s_state snapshot (full Σ + turn)       │
                 + persist checkpoint                         │
     action: finish ──► terminal (answer)                     │
     action: tool ──► ToolRegistry.Execute (ALL security      │
            gates: group policy, judge, HITL, verify-on-edit) │
            → truncated result = Oₜ₊₁ (+ hash nudge,          │
              cache-on-truncate via tool_result_read)         │
     anti-spin: semantic fingerprint (tool + target anchor);  │
            nudge @ 3, abort @ 5                              │
   step budget exhausted → step_limit (resumable: Σ persists, │
        resume re-enters with a fresh budget + resume note)   │
        │  (every path: final Snapshot persisted)             │
        ▼                                                     │
   Result ─► RunStatus→StateStatus→ExecutionStatus mapping ───┘
        │
        ▼
   HandleResult: answer/status to the session; trajectory stored;
   frontend Execution State panel (replaces PlanView) tracked Σ live
```

## Invariants

- **Bounded context** — a turn's LLM request contains exactly two messages (`system`, `user`); messages from previous turns are never resent. The model's only memory is Σ.
- **Σ is validated deterministically** — every mutation goes through `ApplyPatch`: core keys can be updated but never deleted or re-typed; extension keys are mutable (a value write replaces the previous value in place; `null` deletes — ADR-040); merged Σ over the byte limit is rejected; the receiver is never mutated, so a rejected patch leaves the previous Σ authoritative (rollback by construction). The core list keys also enforce their element shape — `checklist` holds `{text, checked}` objects and every other list key holds strings — so a wrong-shaped element is rejected at merge rather than persisted and then dropped by the UI guard.
- **Schema evolution fails closed** — a persisted state whose fingerprint differs from the compiled-in schema is never patched; resume treats it as absent (fresh state).
- **`e2s_step` never executes through a registry** — the loop intercepts it; `StepTool.Execute` is a wiring-error trap that returns an error result.
- **All security gates apply to the target tool** — actions dispatch through the real `ToolRegistry.Execute`; the E2S adapter only filters the *catalog* and rejects filtered-out names (goal-only, plan-workflow, and delegation tools — plus everything `e2s.tools` narrowed away) fail-closed at dispatch. `e2s_step` itself is `GroupSystem` (ADR-024): the envelope has no side effects, the dispatch carries the policy.
- **The per-turn prompt is bounded by construction** — the "Available Tools" catalog is the filtered E2S view (the `e2s.tools` core preset by default), and the observation is capped against the model's resolved window (token-proportional budget) or the character fallback; the scratchpad (`<temp>/e2s-notes.md`) is the overflow channel that keeps raw evidence out of Σ instead of growing the state.
- **The catalog is the dispatch contract** — a name absent from the rendered catalog is unreachable: the adapter's allow-set is built from the same filtered descriptor list, so a hallucinated call to a preset-excluded, denied, or stripped tool returns an error observation and never executes the real tool.
- **E2S and Goal are mutually exclusive** — `HandleMessage` returns an explicit error when both flags are set.
- **Bounded corrective retries** — an invalid `e2s_step` (bad envelope or rejected patch) is re-requested up to `e2s.patch_retries` times (default 1); once the retries are exhausted the failure becomes an error observation and the run continues with Σ unchanged.
- **The initial Σ is bounded too** — the seeded state (objective, or a resumed checkpoint) is checked against the byte limit before turn 1; an oversized seed fails fast with an actionable error instead of wedging every turn on `ErrStateTooLarge` until the budget runs out.
- **Every terminal path checkpoints Σ** — `Result.Snapshot` is persisted (with the domain status mapped from the run status) even on pause/cancel/error; a resume decision (`active`/`paused` = resumable, terminal statuses = never) reads exactly this checkpoint.
- **The gate is fail-closed on both sides** — backend rejects `e2s=true` when `experimental.enabled` is off; the frontend toggle is hidden.
- **The panel replaces, never augments** — an E2S session renders the Execution State panel instead of the plan view; non-E2S sessions are unchanged.

## Conductor Parity and Documented Divergences

E2S replaces the executor, so the Conductor-level behaviors that are NOT registry gates are mirrored explicitly in `runE2SWithState` — with one deliberate divergence class: **E2S is self-sufficient and does not delegate** ([ADR-071](../decisions/071-e2s-self-sufficiency.md)), so the Conductor's delegation wiring is absent by design rather than missing.

Parity (mirrored):

- **Context seams** — the blackboard-backed memory stores (`store_fact`, `search_facts`, `read_attachment` resolve the blackboard exactly as in a Conductor run; without them every call errors "store not available") and the task context (`sdktools.WithTaskContext`, feeding the strict judge on user-confirm escalations). The step-output/final-result stores are deliberately NOT wired: their reader tools (`read_step_output`, `list_step_outputs`, `read_final_result`) exist to serve launched subagents and their parent, are stripped from the E2S catalog with the delegation pair, and would have no consumer here.
- **Executor-level hooks** — the config-authored verify-on-edit runner runs after any successful `write_file`/`edit_file` action (single, or batch sub-call) and its `[verify_on_edit]` note is appended to the observation. There is no finish-join guard: with delegation removed, no async work can be pending at finish time.
- **Request shape** — `ReasoningEffort` (the per-message override / Model Profiles `sampling.reasoning_effort`) is passed to every turn's `ChatRequest`.
- **Model Profiles feature** — the essential-tools narrowing (`applyModelProfilesToolFilter`) applies to the E2S catalog (goal mode is the only documented exception), the Lite prompt swap selects `prompts.E2SSystemLite` for the core directive, and the loop-hardening `RepeatNudgeThreshold` override tightens the anti-spin nudge under the same profile gates. FewShot/ReasoningScaffold stay orchestrator-only (ReAct-worked examples do not apply to the e2s_step protocol).
- **Injection defense** — the system prompt carries the unconditional `VerificationMandate` plus the config-gated `InjectionDefense` directive (SECURITY.md mandates both for every model-facing loop).
- **Tool-call source** — the loop emits the real `GetToolSource` value ("core" / MCP server name), so the UI renders genuine MCP tools as MCP and everything else as built-in.
- **Empty-answer terminations** — `step_limit` and `spin_stop` surface explicit, honest wrap-up messages (never the user's own message echoed as the assistant answer); both persist the Σ checkpoint, but their resumability differs: `step_limit` maps to the non-terminal `active` status, so Resume re-enters the loop with Σ and a fresh budget, whereas `spin_stop` maps to the terminal `failed` status and is not resumable (the checkpoint is kept for the record only — its message must not promise a Resume into the E2S loop).

**Documented divergences** (deliberate — [ADR-071](../decisions/071-e2s-self-sufficiency.md) unless noted): E2S installs none of the Conductor's delegation wiring — no delegation registry or `conductorLauncher`, no Subagent Profile resolver (`tools.WithAgentResolver`), no subagent roster or `#agent`-mention context (`enrichAgentContext`), no routing seeds (`WithDomain`/`WithComplexity` — they size subagent budgets), no delegation spec sink (`wireDelegationSpecSink`), no finish-join guard — so `delegate(agent: …)` is neither offered nor executable, and the prompt renders no "Available Subagents"/"Requested Subagents"/"Delegation" sections. Long tasks run single-threaded within the turn budget; delegation belongs to the Conductor path. Also divergent: mid-run user interjections are not delivered to the live E2S run (the Resume/nudge path is the delivery channel — the Conductor's `userMessageSource` polling has no E2S equivalent; a pre-existing deliberate exception), and the E2S system prompt does not carry the full project prefix (AGENTS.md, env block, auxiliary work dirs, family overlay, research/vector sections) — its context stays bounded by design; the workspace, tools, and skills sections carry the operative parts.

## Configuration

`e2s:` section (see `config.example.yaml`; effective only while `experimental.enabled` is true — the section seeds defaults unconditionally so values stay visible/editable while the mode is a no-op):

| Key                      | Default | Meaning                                                             |
| ------------------------ | ------- | ------------------------------------------------------------------- |
| `max_steps`              | `50`    | turn budget per run (patch+action cycles) before `step_limit`       |
| `state_byte_limit`       | `16384` | JSON-encoded Σ byte cap (mirrors `core/e2s.DefaultStateByteLimit`)  |
| `patch_retries`          | `1`     | corrective re-requests for a rejected patch before the failure becomes an error observation (the run continues; Σ unchanged) |
| `observation_truncate`   | `8000`  | per-turn observation character FALLBACK cap, used only when the loop has no resolved model context window; on truncation the full result is cached and a hash nudge (recovered via `tool_result_read`) is appended (ADR-040) |
| `observation_budget_tokens` | `8192` | per-turn observation budget in TOKENS (the ReAct-scale cap — the executor's tool-result budget is 30K tokens against a comparable window share); engages when the loop resolves a context window: the effective cap is `min(observation_budget_tokens, observation_fill_fraction × window) × 4` characters (ADR-071) |
| `observation_fill_fraction` | `0.4` | caps the observation budget as a fraction of the model's context window, so a small-window model gets proportionally smaller observations; only meaningful together with `observation_budget_tokens` and a resolved window |
| `repeat_nudge_threshold` | `3`     | semantically identical consecutive actions (same tool + target anchor; precision args like line ranges are ignored, while the content payloads of `write_file`/`edit_file` are part of the identity — successive different edits of one file do not collide; a batch fingerprints per sub-call) before a nudge observation |
| `repeat_abort_threshold` | `5`     | identical consecutive actions before `spin_stop` abort              |

`e2s.tools:` narrows the available-tool catalog (ADR-071) — both the "Available Tools" section of every turn's system prompt AND the action-dispatch surface (the catalog is the dispatch contract; filtered-out names are rejected fail-closed). Filtering happens before the always-applied goal/plan/delegation stripping, so a stripped name stays unreachable under any `e2s.tools` value:

| Key     | Default  | Meaning                                                              |
| ------- | -------- | -------------------------------------------------------------------- |
| `preset` | `core`  | `core` keeps the relevant local-work core (~19 tools): the `local_read` + `local_write` + `execute` + `remote_read` capability groups plus the system-group plumbing (`batch`, `tool_result_read`, `ask_user`, `store_fact`, `search_facts`, `read_attachment`); `all` restores the full registered surface (minus the always-applied stripping). Any unrecognized value fails closed to `core` — and is rejected at config load (no silent fallback) |
| `allow`  | `[]`    | re-includes specific tools the preset excluded (e.g. the system-group `semantic_search`, or one MCP tool by name); ignored under `preset: all` |
| `deny`   | `[]`    | removes specific tools with PRECEDENCE over the preset and allow — a denied name is absent from the catalog AND rejected at dispatch even when explicitly allowed |

## Security (e2s_step under ADR-024)

`e2s_step` declares `ToolGroup: GroupSystem`. Per ADR-024 the `system` group is the reserved, unconfigurable bypass class — membership therefore requires a security review, which this section records:

- **No side effect of its own.** The tool's input is a description of a patch and an action. Executing it does nothing — in fact the E2S loop intercepts every `e2s_step` call before any dispatch, and `StepTool.Execute` (the only path a registry could take) is an error trap.
- **The dispatch carries the policy.** The `action` half is dispatched to the *target* tool through the real `ToolRegistry.Execute`, where the full pipeline applies in order: structural input validation (`sdktools.ValidateToolInput` — required keys, declared types, unknown keys, recursively into nested objects and array items; closed-set + fail-open semantics owned by the SDK), disabled-tools check, per-session shell blacklist, group-policy deny, judge outcome (hard/soft), symlink analysis, HITL confirmation, verify-on-edit hooks. An `e2s_step` envelope cannot weaken, reorder, or bypass any of these — the envelope is data; the registry is the gate.
- **Catalog narrowing is enforcement, not decoration.** The E2S adapter strips goal-only, plan-workflow, and delegation tools from the model-visible catalog *and* rejects those names at dispatch (fail-closed), so a hallucinated `execute_plan`/`declare_goal_status`/`delegate` call cannot execute the real tool; the `e2s.tools` narrowing (ADR-071) composes the same way — the rendered catalog is the adapter's allow-set, so a preset-excluded or denied name is unreachable even if the model names it. The scratchpad (`<temp>/e2s-notes.md`) adds no new surface: it is an ordinary file reached through `write_file`/`read_file`, under their full policy pipeline.
- **Untrusted observations are contained.** Tool results are fed back as the next observation under the E2S prompt's injection-defense rules (treat output as data); `Registry.IsToolUntrusted` marks MCP/untrusted sources for the loop's wrapping.
- **The mode cannot be smuggled in.** `HandleOptions.E2S` is set only through the SendMessage chain, which rejects it fail-closed when the experimental gate is off; it is mutually exclusive with `Goal`.

## Extension Points

- **Adding a core key** — extend `coreTypes` in `core/e2s/types.go` (the fingerprint changes automatically, so old persisted states fail closed and resume fresh); document it in `core/prompts/e2s.md` and update the frontend `E2SSigma` type + panel rendering.
- **Tuning loop behavior** — every behavioral knob (budgets, caps, thresholds) lives in `E2SConfig`; the loop-level defaults in `core/e2s` `Config.withDefaults` are the fallbacks.
- **New consumers of Σ** — anything needing live state subscribes to the `e2s_state` session event (see [contracts/event-catalog.md](../contracts/event-catalog.md)); persisted consumers read `task_e2s_state` via the task adapter.

## Related Specs

- [decisions/039-e2s-explicit-execution-state.md](../decisions/039-e2s-explicit-execution-state.md) — why the mode exists and its seven shaping decisions
- [decisions/040-e2s-stabilization.md](../decisions/040-e2s-stabilization.md) — the stabilization revision: mutable extension keys, cache-on-truncate observation recovery, real batch execution, pre-dispatch schema validation, budget visibility, semantic anti-spin, resumable step-limit
- [decisions/071-e2s-self-sufficiency.md](../decisions/071-e2s-self-sufficiency.md) — the self-sufficiency revision: delegation removed from the mode (wiring, tools, prompt sections), token-proportional observation budget, per-sub-call batch cache, the `e2s.tools` slim catalog, the scratchpad overflow channel
- [decisions/024-group-policies.md](../decisions/024-group-policies.md) — the group-policy model behind the Security section
- [domains/goal-mode.md](goal-mode.md) — the sibling alternative-loop mode (early-return pattern, per-task state persistence, pause/resume semantics E2S mirrors)
- [contracts/event-catalog.md](../contracts/event-catalog.md) — the `e2s_state` event
- [contracts/backend-core.md](../contracts/backend-core.md) — how backend wraps the core `HandleOptions`
