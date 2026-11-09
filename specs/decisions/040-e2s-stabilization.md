# ADR-040: E2S Stabilization — Mutable Extensions, Observation Recovery, Real Batch, Budget Visibility

## Status

Accepted (supersedes the add-only extension policy and the plain-truncation/batch/catalog mechanics of [ADR-039](./039-e2s-explicit-execution-state.md); ADR-039's architecture — the loop, the e2s_step envelope, the security model, the panel — stands unchanged)

Amended by [ADR-083](./083-e2s-self-sufficiency.md) in three of its mechanics. The observation cap is token-proportional: `observation_truncate` becomes the fallback (raised 2000 → 8000 chars) used only when the loop resolves no model context window, and with one the effective per-observation cap is `min(observation_budget_tokens, observation_fill_fraction × window) × 4` characters (defaults 8192 tokens, 0.4). The `batch` observation cache is keyed per sub-call — each oversized sub-result is cached in full under its own hash — with the join-level cap left as the last barrier. The Available Tools catalog this ADR gave full input schemas is narrowed by the `e2s.tools` preset (default `core`, roughly 19 tools selected by capability group). The mutable-extension hybrid schema with null-tombstones and its fingerprint, cache-on-truncate recovery via `tool_result_read`, structural pre-dispatch validation of action args, real `batch` sub-call dispatch and the `[turn N of M]` budget header stand unchanged.

## Context

The first real E2S production run (a code-review task on `deepseek-flash`, 2026-09-12) failed at the step budget with zero `finish` calls: 477K input / 60K output tokens burned, ~40% of all LLM calls consumed by corrective retries, the target diff re-read ~30 times without converging. Post-mortem of the dump identified six implementation defects, none of which implicate the E2S architecture itself — all six are mismatches between the paper's protocol and what the loop actually offered the model:

1. **Add-only extension keys made mutable facts unrepresentable.** The model's natural progress keys (`next_action`, `report_phase`, read cursors) must change every turn; add-only forced either two-turn tombstone+re-add dances (33 rejected patches in the session — `next_action` alone 14×) or rename-churn (`note`→`note2`→`note3`, `reread_at`→`reread_at_b`→`reread_c`) that desynchronized the state and pushed Σ toward the byte cap. ADR-039 chose add-only to counter the paper's "premature state overwrite" failure mode; in practice the cure was worse than the disease: the protected arrays are core keys (still typed and validated), while the collateral damage landed on exactly the scalar working memory a state-based agent needs. The system prompt even contradicted itself ("Overwrite stale keys" in the protocol section vs "add-only" in the schema section).
2. **Plain 2000-char observation truncation with no recovery path.** The Conductor executor truncates with a cache hash + nudge so the model can page through the dropped content via `tool_result_read`; the E2S loop's `strutil.TruncateUTF8` cut silently, so a 48KB diff was physically unreadable — the model could never see more than ~2KB per turn and "knew" it hadn't finished reading.
3. **The Available Tools catalog carried no parameter schemas** (first description line, 160 chars). The model guessed argument names (`offset`/`limit`, `line_start`/`line_end`, `line_range`, `startLine`); Go's `json.Unmarshal` silently ignored the unknown fields, the tool executed with defaults (whole file), the observation truncated — an undetectable wrong-argument loop. The provider-side schema validation that catches this in the Conductor never runs in E2S because the only native tool is the free-form `e2s_step` envelope.
4. **`batch` was a trap**: listed in the catalog, rejected at dispatch ("handled at the executor level") — two wasted turns and a state note "batch must not be called directly".
5. **No budget visibility**: the model saw `[turn 37]`, never `[turn 37 of 50]`, and could not pace itself; invalid turns consumed the budget invisibly.
6. **The anti-spin detector was byte-exact** (tool + canonical args), so re-reading one file with shifting line ranges — the dominant observed spin — produced a fresh fingerprint every time and was never caught (0 nudges in 50 steps).

## Decision

Six targeted revisions to `core/e2s` (plus one exported helper in sp4rk), all preserving ADR-039's security model — every dispatch still goes through `ToolRegistry.Execute`:

1. **Extension keys are mutable.** `ApplyPatch` replaces an existing extension value in place; `null` remains the universal tombstone; core keys keep their fixed typing and the byte cap still bounds Σ. `ErrExtensionKeyExists` is removed; the schema fingerprint is unchanged (it covers only the core key set and types, so persisted states remain valid). The prompt's schema section now states the mutable rule and the protocol section's "Overwrite stale keys" is no longer contradicted.
2. **Cache-on-truncate with hash nudge.** The loop carries the shared `agent.ToolResultCache` (the same instance the Conductor executor uses): an observation truncated by `MaxObservationChars` is stored in full and the standard fragmentation nudge — exported from sp4rk as `agent.FormatFragmentationNudge` so both modes show the identical recovery contract — is appended; the dispatch context carries the cache so `tool_result_read` actions resolve.
3. **Full schemas in the catalog + pre-dispatch structural validation.** The Available Tools section renders each tool's complete input schema (compact JSON). The SDK's shared structural validator (`sdktools.ValidateToolInput`) checks action args against the target schema before dispatch: required keys, declared types, and unknown keys against a closed property set (fail-open for open/unparseable schemas). A violation is an ACTION error observation naming the valid parameters — the patch stays applied and no patch-retry budget is consumed.
4. **`batch` really executes in E2S.** The loop intercepts `action.tool = "batch"` and dispatches each sub-call through the same registry path (per-sub-call schema validation, per-sub-call untrusted wrapping, per-call errors never abort); nested batch and the envelope targets (`e2s_step`, `finish`) are rejected fail-closed so the finish interception cannot be bypassed.
5. **Budget visibility.** The user message header renders `[turn N of M]`, and inside the final window (the greater of 20% of the budget or 3 turns) it carries an explicit wrap-up directive.
6. **Semantic anti-spin fingerprint.** `ActionFingerprint` anchors on the tool plus the identity arguments (`path`, `pattern`, `command`, `query`, `url`, `name`, `skill`, `hash`) and ignores precision arguments (line ranges, limits); batch fingerprints per sub-call; tools exposing no anchor fall back to the canonical (key-sorted) args fingerprint. **Content payloads are part of the identity:** for mutating tools the `content`/`old_string`/`new_string` arguments join the fingerprint whenever present, so successive edits of the same file with different content are distinct actions (an anchor-only fingerprint counted every consecutive edit of one file as an identical action and silently dropped the third at the nudge threshold), while re-writing identical content to the same path is a true repeat.

Additionally, **step-limit termination became a resumable checkpoint**: `RunStatusStepLimit` maps to `ExecutionStatusPartial` (the task stays resumable) and to the non-terminal domain status `active`, so Resume re-enters the loop with the accumulated Σ and a fresh turn budget. The `e2s_state` event now carries the run-local `turn` (coherent with `max_turns`) plus the cumulative `total_turns` (the persistence continuation point), and a plain resume (no user nudge) delivers a turn-1 observation telling the model the budget was refreshed and Σ is the continuation point.

## Consequences

**Positive:**

- Read-heavy tasks (reviews, broad exploration) become feasible: large results are recoverable instead of silently lost, and one batched action can gather several files per turn.
- The model's working memory behaves like memory: cursors and phase markers update in place under stable names; Σ no longer accumulates suffixed clones.
- Wrong argument names fail loudly and cheaply (action-error observation listing valid parameters) instead of executing with defaults.
- The observed spin pattern (same file, shifting ranges) is nudged at 3 and aborted at 5.
- Budget exhaustion no longer destroys the work: Σ persists, the task offers Resume, and the continuation gets a fresh budget plus an explicit resume note.

**Negative / trade-offs:**

- Mutable extensions lose the mechanical guard against premature overwrite of extension data; the mitigation is prompt-level state discipline plus the byte cap (the paper's failure mode applied to free-form state, which core keys never had anyway).
- The catalog with full schemas grows the one-shot system prompt by roughly 10–25KB — mitigated by deterministic, prefix-cache-friendly composition (unchanged per session).
- Pre-dispatch validation adds a Go-side structural check that can, in principle, reject a call the real tool would have accepted (e.g. a tool whose schema under-declares its inputs); the validator fails open on open/undocumented schemas to keep that surface minimal.

## Alternatives Considered

- **Keep add-only, add a mutable `notes` core key.** Rejected: two memory systems in one Σ re-introduces the naming-discipline problem the core schema exists to remove.
- **Raise `observation_truncate` instead of hash recovery.** Rejected: no cap makes a 48KB diff readable in a bounded observation; recovery-on-demand (the Conductor's proven mechanism) scales instead of shifting the cliff.
- **Strip `batch` from the catalog.** Rejected: batch is the single strongest lever for E2S's turn economy (one turn = several independent observations), and the owner's direction is that E2S must expose the full relevant toolset, working.
- **Byte-exact spin detection with a lower threshold.** Rejected: the observed failure varied args on every call; only semantic anchoring catches it without false-positiving legitimate re-reads of different targets.
