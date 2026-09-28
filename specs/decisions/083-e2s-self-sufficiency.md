# ADR-083: E2S Self-Sufficiency — No Delegation, Bounded Observations, Slim Catalog, Scratchpad

## Status

Accepted

## Context

The trigger case (session `331e5582`, a `/code-review` E2S run over the last commit on a local small-context model) exposed four compounding problems in the E2S mode as shipped by ADR-039/ADR-040:

1. **Delegation did not fit the mode.** E2S inherited the Conductor's delegation seam by injection (the `conductorLauncher` + delegation registry with an inert plan state), so `delegate`/`cancel_delegation` appeared in the tool catalog, the system prompt carried the "Delegation" and "Subagents" sections, and a finish-join guard vetoed finishing while delegations were pending. But an E2S run has no plan steps for a subagent to be accountable to, no `declare_plan`/`execute_plan` machinery for the parent to track, and its one-shot turn protocol has no place for the async subagent bookkeeping the delegation protocol assumes. The coupling produced the worst of both worlds: the model was invited to delegate into a structure that cannot represent the delegation's lifecycle, and every turn paid the prompt cost of machinery the mode cannot use. Session `331e5582` did delegate — and the delegations had to carry their own ad-hoc plan scaffolding precisely because the parent's Σ-based lifecycle does not extend to subagents.
2. **The system prompt was unaffordable.** The "Available Tools" catalog rendered the full registered surface (~85 tools, dominated by MCP endpoints) with input schemas, plus the delegation/subagent sections. Session `331e5582`'s E2S turns carried ~90 KB system prompts — the per-turn cost of a mode whose entire premise (ADR-039) is a bounded, O(1)-in-turns request.
3. **Observations were starved by a fixed character cap.** The `observation_truncate` knob (then 2000 chars) truncated the large majority of real tool results; the model then re-read the same file in shifting ranges (looking past the truncation), which the anti-spin fingerprint correctly tolerated but which wasted turns. A fixed 2000-character budget is blind to the model's actual context window — a 128K-window model gets the same starvation as a 4K-window one.
4. **Batch results truncated as one blob.** The batch meta-tool joined sub-results into one observation; when the join overflowed the observation cap, a single cache entry was keyed on the whole (itself truncated) join — three big reads yielded one recoverable fragment, not three independently recoverable ones.

## Decision

E2S is **self-sufficient**: it does not delegate, and its per-turn prompt is bounded by construction. Five concrete decisions:

1. **No delegate-via-injection.** The delegation seam is removed from `runE2SWithState` entirely: no delegation registry/launcher, no Subagent Profile resolver, no agent roster or `#mention` context, no routing seeds, no delegation spec sink, no finish-join guard. The `delegate`/`cancel_delegation` tools join the plan-workflow tools in the always-applied catalog stripping (`e2sStrippedToolNames`), and with them the blackboard step-store trio (`read_step_output`, `list_step_outputs`, `read_final_result`) — those reader tools exist only to serve launched subagents and their parent. The remaining blackboard stores (`store_fact`, `search_facts`, `read_attachment`) stay: they are E2S's external memory. A delegation action can never reach the real registry — the adapter rejects stripped names fail-closed.
2. **Token-proportional observation budget.** `observation_truncate` becomes the *fallback* cap (raised 2000 → 8000 chars) used only when the loop has no resolved model context window. With a window, the effective per-observation cap is `min(observation_budget_tokens, observation_fill_fraction × window) × 4` characters — defaults 8192 tokens and 0.4 (the executor's comparable window share), so an observation breathes with the model's window instead of a fixed budget.
3. **Per-sub-call batch cache.** The loop intercepts `batch` and dispatches each sub-call through the full single-dispatch path: schema validation, registry execution with every security gate, per-sub-result cache-on-truncate (an oversized sub-result is cached in full under its own hash, recoverable via `tool_result_read`), and per-sub-call untrusted wrapping. The join-level cap remains the last barrier for a batch whose combined results still overflow it. Nested batch and the envelope targets (`e2s_step`, `finish`) are rejected fail-closed; per-call errors never abort the batch.
4. **Slim tool catalog (`e2s.tools`).** The default `core` preset keeps the relevant local-work core — the `local_read`, `local_write`, `execute`, and `remote_read` capability groups plus six system-group plumbing tools the protocol itself depends on (`batch`, `tool_result_read`, `ask_user`, `store_fact`, `search_facts`, `read_attachment`) — roughly 19 tools instead of ~85. Group membership alone selects preset tools (ADR-024's rule); `allow` re-includes excluded names, `deny` removes with precedence over both preset and allow. Filtering runs before the always-applied plan/delegation/goal stripping, and the resulting catalog is the dispatch contract: every filtered-out name is rejected fail-closed at dispatch. Anything but `all` fails closed to the narrower core.
5. **Scratchpad overflow channel.** Σ stays the distilled state; raw evidence (tool output, dumps, long excerpts) overflows into a scratchpad file at `<session-temp>/e2s-notes.md` (`core/e2s.NotesFileName`), reached through the ordinary `write_file`/`read_file` gates — no dedicated tool, no new security surface. The system prompt states the discipline: raw content longer than ~5 lines does not belong in Σ; keep the distilled fact plus a pointer (scratchpad path + line range); the final answer is assembled from Σ and the scratchpad together. An unset `TempDir` (no scratchpad) falls back to keeping a few key lines plus source-file pointers.

## Consequences

**Positive**

- The per-turn prompt is bounded by construction: the catalog is ~19 tools and the observation scales with the model's window. Small-context local models are the mode's primary target, and they are the ones that could least afford the ~90 KB turn prompts of session `331e5582`.
- The Conductor-parity surface shrinks to what the mode actually exercises; the delegation machinery is gone from the prompt, the catalog, and the wiring, removing a whole class of "delegated into a structure that cannot track it" failures.
- Batch joins degrade per-sub-call: each oversized sub-result is independently recoverable via its own hash.
- Σ is no longer pressured into storing raw evidence: the scratchpad gives the byte-capped state a legitimate overflow channel through existing, policy-gated tools.

**Negative**

- E2S cannot parallelize into subagents — long tasks run single-threaded within the turn budget. Operators who want delegation use the Conductor path.
- `e2s.tools` adds an operator-tunable surface to reason about; a wrong preset value fails closed to the narrower core, which is safe but can silently hide a tool an operator expected.
- The scratchpad lives in the session temp directory and is not persisted with the task state: a scratchpad-dependent answer assumes the file still exists within the session's lifetime, and resume does not restore it beyond what Σ's pointers reference.
- The token-proportional budget engages only when the loop resolves a context window; callers that never resolve one stay on the character fallback.

## Alternatives Considered

- **Fix delegation-via-injection instead of removing it** (give subagents E2S loops of their own, track them in Σ): rejected — it multiplies the protocol surface (nested Σ lifecycles, subagent-specific resume), and the mode's core value is a single bounded state for one task; delegation adds unbounded structure back.
- **Cap the catalog by a tool-count budget** (ADR-022's removed `max_tools` idea): rejected — ADR-035 removed the slot budget because a count is arbitrary; capability-group selection states the *intent* (local coding work) and fails closed.
- **Keep the fixed 2000-char cap and rely on `tool_result_read` paging**: rejected — the paging nudge does not help a model that never received enough of the result to know what to page for; the window-proportional budget fixes the root cause.
- **Cache the batch join as one entry** (status quo): rejected — one fragment for N oversized reads makes the Nth sub-result unrecoverable after the join cap consumed the hash budget.
- **A dedicated scratchpad tool** (e.g. `notes_append`): rejected — a new tool is a new security surface and a new catalog entry to pay for every turn; `write_file`/`read_file` already carry the full policy pipeline and are in the core preset anyway.
