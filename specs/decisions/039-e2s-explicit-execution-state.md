# ADR-039: E2S — Explicit Execution State Mode

## Status

Accepted (add-only extension keys, plain observation truncation, the schema-less tool catalog, and the step-limit terminal mapping are superseded by [ADR-040](./040-e2s-stabilization.md))

Amended by [ADR-083](./083-e2s-self-sufficiency.md): decision 4 (delegate-via-injection — the `conductorLauncher` + `DelegationRegistry` seam injected into `runE2SWithState` with an inert plan state) is removed entirely; E2S is self-sufficient and does not delegate. `delegate`/`cancel_delegation` join the plan-workflow tools in the always-applied catalog stripping, together with the blackboard step-store trio (`read_step_output`, `list_step_outputs`, `read_final_result`); `store_fact`, `search_facts` and `read_attachment` stay as E2S's external memory. The one-shot turn protocol, the `e2s_step` envelope, the Σ merge, the dispatch-through-`ToolRegistry.Execute` security model and the Execution State panel stand unchanged.

## Context

Every execution mode c0wrk had until now — the Conductor's ReAct loop, goal mode's turn iteration, subagent runs — is **history-based**: the model's request grows with the trajectory. Compaction mitigates the growth late, but each turn still replays (a summarized) past, costing O(T²) cumulative tokens over a T-step run, and after an external state drift (a file changed underneath the agent, a tool side effect the transcript misremembers) history-based baselines hallucinate recovery context for several turns.

The explicit-execution-state architecture (["SKILL.state"](https://arxiv.org/abs/2608.26263), arXiv 2608.26263) replaces the append-only transcript with a **structured, mutable state Σ** as the model's only memory. Each step the model receives exactly (P = immutable procedural spec, Σₜ = its own working state as JSON, Oₜ = the latest observation) and answers with (reasoning, state_patch, action); the runtime deterministically merges the patch, executes the action, and discards the reasoning. The paper reports a 16.2× token cut at T=100, distractor robustness ≥ 0.97, and zero recovery steps after induced state drift — properties that matter most for small/local models, exactly the population c0wrk's Small-LLM profile tunes for.

c0wrk already had every seam this needs: goal mode established the alternative-loop early return in `HandleMessage`, `task_goal_state` established per-task state persistence + resume, and ADR-024's group-policy registry means tool dispatch cannot be done outside `ToolRegistry.Execute` without losing the security model. What was missing was the loop itself, a protocol for the model, and a UI surface that shows state instead of a plan.

## Decision

Add **E2S mode** — an alternative execution loop selected per message (`HandleOptions.E2S`, mutually exclusive with `Goal`), entered via an early return in `HandleMessage` exactly like goal mode. Seven decisions define it:

1. **Native meta-tool, not a JSON-fenced text protocol.** The paper's original protocol parses a json-fenced `{state_patch, action}` block out of free text. We instead expose **`e2s_step`** as a native tool definition (the only tool offered on every LLM call): provider-side tool_use parsing and JSON-schema argument validation come for free across all supported protocols, with no regex extraction and no text-vs-tool ambiguity. The loop **intercepts** the call — `StepTool.Execute` returns an error if anything ever routes it through a registry — and splits it into the patch half and the action half.

2. **Hybrid schema: fixed core keys + add-only extensions.** A fully free-form Σ drifts and gives the UI no contract; a fully rigid schema cannot carry domain-specific residue. Σ therefore has **eight fixed core keys** (`objective` string, `checklist`/`files_touched`/`findings`/`decisions`/`next_steps`/`done_criteria` arrays, `status` closed-set string) that may be updated but never deleted or re-typed, plus **extension keys that are add-only** (a value write against an existing extension is rejected; `null` is the universal tombstone for non-core keys). The merge operator `Σₜ⊕ΔΣₜ` (`e2s.ApplyPatch`) validates deterministically and rejects oversized merges against a byte cap; a SHA-256 **schema fingerprint** stamped on every state makes persisted pre-schema-change states fail closed instead of being patched under new rules.

3. **Fresh one-shot dialog per turn.** Every turn is a brand-new `[system, user]` request: system = P (the compact `core/prompts/e2s.md` directive + workspace/tools/delegation/skills sections), user = `[turn N]` + `<state>Σₜ</state>` + `<observation>Oₜ</observation>`. Nothing from turn t−1 is ever resent — request size is O(1) in turns, which also makes the composed system prompt prefix-cache-friendly. Reasoning text is discarded from the request stream (it survives only in the trajectory store for the UI). An invalid turn gets bounded corrective retries (`<correction>` suffix) — `e2s.patch_retries`, default one; once they are exhausted the failure becomes an error observation with Σ untouched.

4. **Delegate-via-injection.** E2S runs its own loop on raw sp4rk primitives (`llm.Caller` + registry + emitter — not `agent.Executor`, not the Conductor), but delegation reuses the **same** `conductorLauncher` + `DelegationRegistry` a Conductor run uses, injected into the context (`tools.WithDelegationLauncher`) with an **inert plan state** (`newPlanRunState(false)`). Subagent isolation, budgets, profiles, and security gates are identical to a Conductor delegation; only the caller differs. Plan-workflow tools (`declare_plan`, `execute_plan`, `declare_step_complete`, `reflect`) and goal-only tools are stripped from the E2S catalog, and the registry adapter rejects stripped names **fail-closed** at dispatch — Σ and its checklist replace the plan workflow.

5. **Security stays with the target tool.** `e2s_step` is declared `GroupSystem` (ADR-024): it has no filesystem/network/shell side effect of its own — it only describes the patch+action envelope. The action dispatches to the **target** tool through the real `ToolRegistry.Execute`, so group policies, the judge, HITL confirmation, symlink analysis, and verify-on-edit all apply exactly as in a Conductor run. The loop additionally mirrors the two executor-level hooks the raw dispatch does not carry: the config-authored **verify-on-edit runner** runs once after any turn whose action (or batch sub-call) contains a successful `write_file`/`edit_file` and its `[verify_on_edit]` note is appended to the observation, and a **finish-join guard** vetoes `finish` while async delegations are pending (the veto becomes the next observation, not an error). A malicious `e2s_step` envelope cannot bypass anything: the envelope is data, the dispatch is the gate. (Per ADR-024, a `system`-group tool requires security review — this ADR is that review for `e2s_step`.)

6. **Panel replacement.** E2S sessions render an **Execution State panel** (`ExecutionStatePanel.tsx`) *instead of* the Execution Plan view: Σ's checklist with checked states, the core fields (objective/status/files_touched/findings/decisions/next_steps), a turn/max counter, and a status badge. It is driven by a dedicated `e2s_state` session event (full snapshot after every applied patch) — not the phase-discriminated `service` channel — and is a live-only stream (no persisted restore).

7. **Experimental gate.** The whole feature sits behind `experimental.enabled` (fail-closed on *arming*): the backend rejects `SendMessage` with `e2s=true` when the gate is off, and the frontend toggle (`E2SToggle.tsx`) is hidden. **Declared exception — resume:** a task already checkpointed with a non-terminal Σ resumes even when the gate was turned off in the meantime; refusing would strand existing work with no way to continue it (there is no "abandon" action for gated checkpoints). The gate guards starting new E2S runs, never the recovery of existing ones. The `e2s:` config section (step budget, state byte limit, retry count, observation truncation, anti-spin thresholds) is ineffective when gated off — mirroring the Small-LLM profile's configadapter pattern.

Supporting mechanics follow the goal-mode precedents: per-step Σ checkpointing into a `task_e2s_state` table (CREATE TABLE IF NOT EXISTS migration; copied on session fork), resume re-enters the loop with the restored Σ (schema-fingerprint-guarded), pause rides the universal session pause signal at step boundaries, and anti-spin nudges at 3 identical consecutive actions and aborts at 5.

## Consequences

**Positive:**

- Request size is bounded regardless of task length — the feature is the strongest lever yet for small/local models, and it composes with (not against) the Small-LLM profile.
- Σ is a durable, inspectable artifact: the UI panel shows exactly what the model "remembers", and a resumed run continues with the same memory.
- All hard security gates are inherited by construction — the loop has no private dispatch path.
- The core/e2s package is pure data + logic (no LLM/tool imports in the domain types), independently unit-testable, and the loop is testable with a mock caller + mock registry.

**Negative / trade-offs:**

- The model must maintain state discipline. The paper reports small models' dominant failure is *premature state overwrite* (replacing instead of merging); the add-only/core-key rules catch the destructive variants, and the bounded retry converts the rest into error observations — but a weak model can still starve itself of recorded context.
- No conversation continuity: reasoning is discarded from the request stream, so follow-up questions the user asks mid-run arrive as ordinary turns without the model remembering its prior reasoning — only what Σ records.
- The schema is authored at design time. Tasks whose relevant state shape is only discovered dynamically, whose observation relevance is only known later, or whose objective **is** the trajectory (auditing) fit E2S badly (per the paper's own limitations) — the mode is opt-in per message, not a replacement for the Conductor.
- Two execution UIs exist (plan view vs. state panel); the frontend branches on session mode.

## Alternatives Considered

- **JSON-fenced text protocol (the paper's native form).** Rejected: free-text parsing is fragile across providers, duplicates validation the tool-use path already provides, and invites text-instead-of-tool responses. The tool-call form is semantically identical (the same two keys) with native parsing.
- **Grow the Conductor/Executor with aggressive compaction.** Rejected: compaction is lossy, still O(T) per turn, and the paper's own baselines (rolling summaries, summary-capped windows, LangGraph-style state-over-transcript) all underperform explicit state — including a state-block-over-full-transcript variant, which is the closest analogue of "compacted ReAct with a state card".
- **Free-form Σ with schema authored per task by the model.** Rejected: dynamic schema discovery is the paper's own first-listed failure mode; a model-authored schema has no UI contract, no type validation, and no persistence compatibility guarantee.
- **Render Σ inside the existing Plan view.** Rejected: the plan panel is a DAG renderer; Σ is a flat typed map with a checklist. A dedicated panel is smaller than a DAG adapter and keeps the plan view untouched.
- **A second Conductor pipeline variant.** Rejected: the whole point is that the loop is *not* history-based; routing, plan steps, and trajectory replay are the machinery being replaced. An early-return branch keeps the Conductor untouched (existing tests stay green) while E2S runs on primitives.
