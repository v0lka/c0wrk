# ADR-022: Small-LLM Profile

## Status

Accepted

> **Drift note (2026-08-25, vibespec-check):** The negative consequence "TopP is a config value that does nothing today" is resolved — TopP is now wired end-to-end (core/builder.go sampling override; serialized by all sp4rk providers) with tests asserting explicit-value override.

## Context

c0wrk is designed around frontier-class models with large context windows, dense tool schemas, verbose system prompts, and loose sampling. When an operator points it at a "small" (low-capacity / cheaper) LLM — a local model on LM Studio, a lightweight hosted model, or a budget tier — the same configuration actively hurts: every prompt carries the full JSON schema of every advertised tool, the verbose orchestrator directive exceeds what an SLM can hold in working memory, default sampling drifts into repetition, and the baseline circuit-breaker thresholds let a looping small model burn the token budget before it is caught.

There is no reliable signal to auto-detect "small" from a model name or context window (a model may be small in capacity but large in context, or vice versa), so the profile must be opt-in. The question is how to structure the optimizations so they can be turned on individually, tuned without a rebuild, and layered without coupling.

## Decision

Introduce a **Small-LLM profile**: a master toggle (`small_llm.enabled`, manual only — no auto-detection) gating five independently sub-toggled variants, each addressing one dimension of small-model overhead:

1. **Essential Tools** — narrow the visible tool set via semantic router matching + a user-pinned always-present list + a protected orchestration base (finish, fact memory, ask_user) + every MCP tool, with an optional `max_tools` budget. Reduces per-prompt schema overhead.
2. **System Prompt Simplification** — a "Lite" core-directive swap (compact `OrchestratorSystemLite`), with optional reasoning-scaffold and few-shot blocks appended (both require Lite). The injection-defense section is never touched.
3. **Sampling Overrides** — per-parameter overrides (temperature, top_p, top_k, repetition_penalty) layered on top of the per-family vendor preset, plus an optional reasoning-effort seed. Unset (zero) parameters inherit the vendor preset; the original constant-temperature replacement (seeded to 0.1) was reverted after it clobbered vendor-tuned presets (the 27-30B regression).
4. **Loop Hardening** — tighter circuit-breaker thresholds so repetition/no-progress is caught sooner.
5. **Context Management** — tighter compaction (keep_last/block_size/trigger), harder tool-output pruning, and a larger output token reserve, applied identically at every executor-config materialization site.

Design rules:

- **Defense-in-depth gating.** Every variant is gated by BOTH the master toggle and its own sub-toggle. No variant activates on a single signal.
- **Pure selection.** The tool-set selection (`smallllm.SelectTools`) is pure and deterministic — no LLM, embedding, or network calls — factored into its own package for isolated testing and a single well-defined call site (once per task, before the non-goal ReAct loop).
- **Strict injection-defense constraint.** The Lite directive carries no injection-defense content; the shared injection-defense section is injected separately and unchanged regardless of Lite. This is the hard security constraint — prompt simplification never weakens the injection defense.
- **Master toggle = no-op when off.** Every variant's call site returns the baseline untouched when the master toggle is off, so the profile is strictly additive.
- **Per-variant no-op.** When a variant's sub-toggle is off (master on), only that variant is inert; the others still apply.

## Consequences

**Positive:**

- Operators can run c0wrk on small/local models productively, turning on only the optimizations their model benefits from.
- Each variant is independently tunable (every threshold/value is exposed in config), so no optimization requires a rebuild.
- The strict master-toggle contract makes the profile safe to leave in config disabled — it cannot accidentally degrade a frontier-model run.
- The pure tool-selection package is independently unit-testable.

**Negative:**

- More configuration surface (the `small_llm` block) — but it is fully optional and defaults to off.
- TopP is a config value that does nothing today; it must be wired into sp4rk before it takes effect (documented as forward-compatible in the spec and validated to be in range).
- The Lite directive is a parallel, shorter core directive that must be kept behaviorally aligned with the verbose one as the orchestrator evolves — a maintenance cost.

## Alternatives Considered

- **Auto-detection from model metadata.** Rejected — there is no reliable size signal (context window ≠ capacity), and a wrong auto-guess would silently degrade frontier runs or silently fail to help small ones.
- **A single on/off switch with fixed values.** Rejected — different small models benefit from different subsets; a fixed bundle would force an all-or-nothing trade-off and hide tunable thresholds behind a rebuild.
- **Domain-specific tool allow-listing in `SelectTools`.** Rejected — relevance is the router's and the user's job; hardcoding "code needs X" would fight routing and age poorly. Selection stays purely semantic (union of router match + user pins + protected base + MCP).
- **Putting injection-defense content into the Lite directive.** Rejected — it would require keeping two copies of the security-critical section in sync and risk the Lite copy drifting. Keeping it injected separately and unchanged is the safe constraint.
