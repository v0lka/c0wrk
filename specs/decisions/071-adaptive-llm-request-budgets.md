# ADR-071: Adaptive per-model LLM request budgets

## Status

Accepted

## Context

Every main-agent LLM call runs under one fixed deadline: `timeouts.llmRequestTimeout` (default 600 s; `backend/config/config.go`). The fleet that deadline must cover spans three very different speed regimes — remote hosted providers, mid-size local models served over loopback (`openai_compatible` endpoints such as Ollama or LM Studio), and the backend-owned embedded model (`embedded` / `Bonsai 2 27B` supervised on `127.0.0.1`). One constant is wrong everywhere at once, in opposite directions:

- A slow local generation gets decapitated mid-reasoning. The reasoning traces measured behind the model-profiles research reached 22,276 reasoning tokens / 21 minutes for a single trivial request at high effort (`docs/development/model-profiles-defaults-research.md`) — twice the default deadline.
- A wedged remote request (hung gateway, a dead connection the OS has not yet noticed) parks the turn for the full 600 s before anything surfaces.

The raw material for doing better already flows through the system: sp4rk's `TrackingCaller.Call` measures wall-clock duration around every provider call and reports it via `UsageTracker.RecordTimed` to registered `TimedUsageObserver`s (per-call duration, token usage, input/output totals, model, family). Successful calls only — an unsuccessful call short-circuits before any recording. What the feed deliberately lacks is phase observability: sp4rk does not stream provider requests (the request model carries no `stream` field), so prefill and decode are indistinguishable; only whole-call aggregates exist.

The question this ADR settles: should the request deadline be learned per model from that live traffic — and if so, what is measured, how is a request's budget computed before its tokens exist, which knobs stay operator-owned, and what happens when there is no data yet?

## Decision

c0wrk learns per-model request budgets from live traffic and applies them as the adaptive deadline for main-agent LLM calls. The feature is always on, conservatively warm-started, and confined entirely to c0wrk. The decisions:

**D1 — Learn from live traffic only; never probe.** The sole input is the sp4rk `TimedUsageObserver` feed (duration, in/out token totals, model, family per successful call). No synthetic benchmark requests, no startup probing, no background traffic generation.

**D2 — "Median×2" rejected as the estimator.** A median-of-durations rule measures the size of the tasks a model has happened to serve, not the model's speed: a model that has only seen trivial asks gets a budget that strangles its first hard one; a model that has only seen hard asks gets an inflated one. Duration must be decomposed against the tokens that produced it.

**D3 — Three timeout classes: `remote`, `local`, `embedded`.** Classification per provider: the backend-owned name `embedded` is class `embedded` by definition; a provider whose resolved `base_url` targets loopback is `local`; everything else is `remote`. An optional per-provider `timeout_class` override (declared on the provider's config entry) wins over the inferred class — except on the reserved `embedded` provider, whose class is fixed by its identity and is not configurable: a `timeout_class` on it (or an invalid enum value anywhere) is rejected by `validate()` at load and by the `UpdateLLMConfig` trust boundary, both through the one shared `config.ValidateProviderTimeoutClass` gate.

**D4 — Always on, conservative warmup.** Until a model has 3 samples, its deadline is the legacy 600 s (`DefaultWarmupBudget`) — exactly today's behavior — for every class. There is no separate warmup knob: an operator who wants a longer deadline from the first call sets the fixed per-model `request_timeout` (or the global `llmRequestTimeout`), which is senior and applies immediately (D5). The adaptive budget engages only on accumulated evidence.

**D5 — Key semantics: tri-state opinion, senior per-model override.** Deadline resolution order:

1. `llm.models.<name>.request_timeout` (new, seconds) > 0 — fixed, never escalated;
2. `timeouts.llmRequestTimeout` > 0 — fixed, never escalated;
3. the adaptive budget (kill-switch on; warmup per D4, formula per D6/D7);
4. kill-switch off and both keys 0 — the legacy fixed 600 s.

`0` means "no opinion at this level". The shipped default of `timeouts.llmRequestTimeout` becomes `0` so the default configuration runs adaptive; an operator who pins a positive value gets exactly that deadline — an explicit override is never silently extended by escalation (D9 applies only to adaptive budgets).

**D6 — Two-parameter fit with a degradation ladder.** Per model, fit `duration ≈ in·r_in + out·r_out` over the observed samples and price the budget at the p85 (85th-percentile) rates rather than the mean, so tail slowness is absorbed into the budget. Whole-call aggregates are the finest decomposition available (no prefill/decode labels without streaming). When the fit is degenerate — too few effective samples, non-positive or wildly unstable rates — degrade: first to a size-normalized percentile of observed durations (per-token duration renormalized against the current request's estimated size), then to the class constants (D8).

**D7 — Budget formula.** `budget = clamp(est_in·r_in + out_reserve·r_out, floor(class), ceiling(class))`, where `est_in` is derived conservatively from the request body size (`bytes/3` — erring toward a larger input estimate) and `out_reserve = clamp(p85(out), 1024, OutputLimit)` — the model's observed p85 output, never below 1024, never above the explicit `llm.models.<name>.output_limit` override when one is set. The clamp uses only that explicit override, not the resolved `llm.ModelMetadata.OutputLimit`: the observed p85 can never exceed the model's real ceiling, so the clamp is a defence against a mis-set override rather than a load-bearing bound.

**D8 — Class constants, exported.** Floors: `remote` 120 s, `local` 300 s, `embedded` 600 s. Ceilings: `remote` 600 s, `local` 1800 s, `embedded` 1800 s. Today's fixed 600 s is exactly the `remote` ceiling; local models gain headroom to 30 minutes. The constants are exported from `core/llmbudget`, following the `core/embeddedllm/limits.go` precedent — compile-time bounds shared with `backend/config`, so the envelope is auditable and cannot be walked around by hand-editing `config.yaml`.

**D9 — Escalation on self-expiry, bounded.** When the transport detects that the expiry that killed a request was its own adaptive budget (not a provider-side abort), the next attempt runs under a budget escalated ×2, capped at the class ceiling. A timeout is retryable by sp4rk's `classifyNetError` (`llm/errors.go`), so it re-enters the router's in-flight retry path and every retry is issued under a fresh budget rather than inheriting the exhausted one. This is distinct from ADR-065's task-level auto-resend (post-failure, `rate_limit`/`overloaded` only), which is unaffected.

**D10 — Session-scoped, in-memory.** Samples and fitted rates live in a session-scoped in-memory table. Persistence across sessions and restarts is deferred; a fresh session re-warms under D4.

**D11 — Kill-switch.** `timeouts.adaptive_budget.enabled`, default `true`. Off: the whole feature is inert and the fixed-timeout regime applies, including the legacy meaning of `0` (600 s) — "no opinion" never means "no deadline".

**D12 — Implementation stays in c0wrk.** Observer wiring, classification, the sample table, fitting, and budget resolution live in c0wrk (`core/llmbudget` plus the builder/transport seams). sp4rk is not modified — it already exposes everything required (the timed-usage seam, `classifyNetError`, the in-flight retry path); no cross-repo development cycle (ADR-031) is needed.

**D13′ — Streaming + idle-watchdog deferred.** The structurally stronger design — observable phases and a "no progress" bound instead of a total-duration bound — requires sp4rk to stream and new provider plumbing. Deferred, not rejected (see Alternatives Considered).

## Consequences

**Positive:**

- Deadlines converge per model from traffic the user was generating anyway; the default configuration needs no tuning.
- Warmup is behavior-preserving: until evidence exists, every request runs under today's 600 s (or the operator's override), for every class.
- Slow local generations stop being cut mid-reasoning (local/embedded ceilings 1800 s); a wedged remote request stops parking the turn any longer than it does today.
- Override semantics stay sharp: a pinned value is exactly a pinned value, with no escalation on either key.
- The envelope is compile-time exported constants shared across layers (the `limits.go` precedent) — auditable, and not walk-around-able by hand-editing config.
- No sp4rk change: no SDK release coupling, no mid-cycle `go.work` window.

**Negative:**

- Learning is session-scoped (D10): every session start re-warms, so a long-lived session's hard-won rates die with it. Persistence is the obvious follow-up.
- `bytes/3` and aggregate-only fitting are approximations; a model whose prefill and decode speeds diverge sharply gets blended rates until D13′ lands.
- An early p85 over few samples can inflate a budget toward the class ceiling, and a run of trivial asks can keep budgets tight; floors and ceilings bound both failure directions.
- Escalation can stretch a genuinely wedged request to the class ceiling before the failure surfaces — bounded, but longer than a fixed deadline would allow.
- One more moving part on the request path (classification, table lookup, fit refresh), and the budget table is not user-visible in this iteration.

**Neutral:**

- `timeouts.serviceLLMRequestTimeout` (session title, commit message, prompt optimization) keeps its fixed path — this ADR scopes the main-agent `llmRequestTimeout` only.
- No UI surface: the sample table and the resolved budgets are diagnostics-level, not user-facing.

## Alternatives Considered

- **Probing (synthetic benchmark requests at startup or first use).** Rejected: it pays real latency, tokens, and money to learn what the first three real calls teach for free — D4's warmup makes the probe redundant — and a synthetic workload measures the probe author's task mix, not the user's. It also adds a cold-start failure surface.
- **Median×2 without normalization.** Rejected per D2: it tracks the size of the tasks served, not the speed of the model — it strangles the first hard task on a model that has seen only easy ones and inflates budgets on models that have only seen hard ones. The two-parameter fit keeps size sensitivity, adds the speed signal, and its degradation ladder covers the sparse-data cases where a median at least degrades gracefully.
- **Implementing in sp4rk.** Rejected: the budget policy is c0wrk product surface — config keys, class constants, kill-switch, and escalation semantics are all c0wrk-facing decisions. sp4rk already exposes the required seams, and SDK-side policy would tax every sp4rk consumer with c0wrk's opinions and couple config semantics to the SDK's release cycle.
- **Reinterpreting `llmRequestTimeout` as a hard ceiling on the adaptive budget.** Rejected: it overloads one key with two meanings (a pinned deadline and a cap), and every operator whose legacy `600` remains in config would silently neuter the feature while it reports itself enabled. The tri-state (`0` = no opinion, `>0` = fixed, never escalated) keeps one meaning per key; the class ceilings — compile-time constants, not config — bound the adaptive budget.
- **Streaming + idle-watchdog (D13′).** The structurally right long-term answer: prefill/decode become observable, and "no bytes for N seconds" bounds a wedged stream regardless of total duration. But it requires sp4rk to stream (it deliberately does not today), new provider plumbing, and per-chunk watchdog semantics across every transport. Deferred; D6's degradation ladder is designed to be replaced by phase-aware rates when it lands.

## Related Specs

- [llm-providers.md](../domains/llm-providers.md) — provider wiring, the sp4rk timed-usage seam, and retryable-error classification.
- [embedded-llm.md](../domains/embedded-llm.md) — the backend-owned `embedded` provider supervised on loopback (class `embedded` by name).
- [ADR-065](065-auto-resend-retryable-errors.md) — the distinct task-level auto-resend layer (post-failure, 429/529 only); unaffected by this ADR.
- [ADR-067](067-memory-aware-embedded-llm-provisioning.md) — the exported-limits precedent (`core/embeddedllm/limits.go`) that D8 follows.
- [ADR-031](031-gowork-repo-root.md) — the cross-repo development cycle D12 deliberately avoids requiring.
