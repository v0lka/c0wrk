# LLM Providers

## Purpose

c0wrk does not implement LLM provider abstractions — `Provider`, `Router`, `ModelRegistry`, `TokenCounter`, and retry/backoff are **sp4rk engine** primitives. This spec documents only how c0wrk wires provider configuration into a sp4rk `Router`. The canonical provider/router/model-registry behavior is in [the sp4rk llm-providers spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/llm-providers.md) and [the sp4rk llm-providers contract](https://github.com/v0lka/sp4rk/blob/main/specs/contracts/llm-providers.md).

## Key Files

- `backend/configadapter.go` — `ToBuilderConfig(cfg)` builds `ProviderConfigs` from all known providers via `GetAllProviderConfigs()` (including providers with no models enabled — the router filters to enabled providers downstream in `buildRouter`); sets `DefaultModel` (cross-provider, resolves to owning provider). Single conversion point for all config mapping.
- `core/builder.go` — `NewOrchestratorBuilder` creates a `github.com/v0lka/sp4rk/llm.Router` with providers (async, in `runAsyncInit()`); passes a `github.com/v0lka/sp4rk/llm.TokenCounter` to the engine
- `core/lmstudio_probe.go` — `probeLMStudioModels` queries the LM Studio-native `GET {base}/api/v0/models` endpoint and returns a per-model context-window map (runtime value when loaded, capacity otherwise); `probeOpenAIModels` is the standard `GET {base}/v1/models` fallback (honors `max_model_len` / `max_context_length` / `context_length`); `probeSelfHostedContextWindow` runs the native leg first, then the OpenAI fallback
- `core/builder.go` — `buildLocalModelProbe` constructs the per-session lazy probe closure (a `LocalModelProbe`); `lookupOpenAIProviderBaseURL` restricts probing to OpenAI-compatible providers
- `core/orchestrator.go` — holds the `Router` (as `modelSwitcher`) for runtime model switching; wraps the caller in `github.com/v0lka/sp4rk/llm.TrackingCaller` for usage tracking

Engine files (`github.com/v0lka/sp4rk/llm/router.go`, `modelregistry.go`, `provider_openai.go`, `provider_anthropic.go`, `provider_openai_responses.go`, `tokencount.go`, `message.go`) are documented in [the sp4rk llm-providers spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/llm-providers.md).

## Wiring Flow

```
~/.c0wrk/config.yaml (providers + models)
         │
         ▼
backend/configadapter.go: ToBuilderConfig(cfg)
  → ProviderConfigs map (provider → {models, api_key, base_url, ...})
  → DefaultModel (composite "provider/model" ID)
         │
         ▼
core/builder.go: NewOrchestratorBuilder
  ├─ ModelRegistry (5-tier Resolve, with config overrides)
  └─ Router (async): multi-provider routing, composite IDs, retry/backoff,
                    context-window validation
         │
         ▼
per-session Orchestrator
  ├─ Router.Call → satisfies agent.LLMCaller
  ├─ Router.SetModel / ActiveModel — runtime model switch (routing, per-delegation override)
  └─ TrackingCaller wraps usage tracking → persisted via emitter callback
```

## Model Override (c0wrk consumption)

- **Per-task**: `HandleOptions.ModelOverride` (from the frontend model selector) switches the Conductor's model via `Router.SetModel`. The switch also re-binds the session's strict tool judge to the new provider/model (`ApplyRequestOverrides` invokes the `JudgeSync` closure wired by `Build`; see [ADR-028](../decisions/028-session-pinned-judge.md)) — the judge always evaluates on the provider/model the session itself runs on, and a global default-model change elsewhere never re-binds a live session's judge.
- **Per-delegation**: a targeted **Subagent Profile's** `model` frontmatter field (`.agents/agents/<name>/AGENT.md`, [ADR-021](../decisions/021-subagents.md)) overrides the model for a subagent, applied via `agent.NewModelOverrideCaller` during the subagent build (empty = Conductor's active model).
- **Small-LLM sampling override**: when `small_llm.enabled && small_llm.sampling.enabled` (see [small-llm.md](small-llm.md)), `core/builder.go` `resolveSamplingFunc` layers the explicitly set SmallLLM sampling parameters (temperature, top_p, top_k, repetition_penalty, presence_penalty; zero = unset) on top of the per-family `prompt.DefaultSampling` preset for the router's `SamplingFunc` — unset parameters inherit the vendor preset. The builder-level reasoning-effort default is also seeded from the profile (`applySmallLLMPresets`); per-request overrides (`HandleOptions.ReasoningEffort`) still take precedence. When the variant is off, the router uses the per-family default unchanged.
- Composite model IDs are `"provider/model"` (e.g. `openai/gpt-4o`, `anthropic/claude-3-7-sonnet`).

## Context Window Resolution

The ModelRegistry's context window for a model is resolved with a strict priority order (first match wins):

1. **config.yaml override** — `llm.models.<name>.context_window` (mapped into `BuilderConfig.LLM.Models`). Always wins; the probe never overwrites a model already present in overrides.
2. **Lazy local-model probe** — `core/builder.go` `buildLocalModelProbe` constructs a per-session probe closure (`LocalModelProbe`) that, for a given model served by an OpenAI-compatible provider (`ProviderType "openai"`), locates the provider (`lookupOpenAIProviderBaseURL`) and fires an asynchronous `probeSelfHostedContextWindow`. The probe runs once for the session's default model at orchestrator construction and on each model switch (the closure is wired into `OrchestratorDeps.LocalModelProbe`); it writes the discovered window to the registry via `SetRuntimeMetadata` (Resolution tier 1.5 — above the built-in catalog and the lazy cache, below a config.yaml override), so the server's observed runtime window beats the catalog spec and only a config override (tier 1) shadows it. `probeSelfHostedContextWindow` tries the LM Studio-native endpoint `GET {base}/api/v0/models` first — reading the **runtime** window when the model is loaded (top-level `loaded_context_length`, or `loaded_instances[].config.context_length` on older versions), otherwise the advertised **capacity** (`max_context_length`) — then falls back to the standard `GET {base}/v1/models` listing, which self-hosted servers (vLLM/TGI/Ollama) extend with `max_model_len` / `max_context_length` / `context_length` (first non-zero wins). This lets token budgets reflect what LM Studio is actually running (e.g. a model loaded at 16384 instead of its 262144 spec).
3. **Static default** — the sp4rk SDK fallback (`ContextWindow` 128000, `OutputLimit` 32768) when neither config nor the probe provides a value.

The probe is best-effort and non-fatal: only OpenAI-compatible providers are queried (anthropic has no `base_url`). A genuine cloud provider (real OpenAI) whose `/v1/models` listing omits any window field is a silent no-op — its behavior is unchanged — while self-hosted servers (vLLM/TGI/Ollama) supply a window via the OpenAI `/v1/models` fallback. Network/timeout/parse failures and **5xx server errors** (a momentarily-unwell LM Studio) are surfaced as errors and logged at Warn; client errors `< 500` (404, 401, 403) remain a silent no-op. The registry still builds normally regardless.

**Latency & safety.** The probe fires on an internal goroutine with a 3-second per-leg timeout and a detached context, so it never blocks session creation or a model switch even when an LM Studio base URL is unreachable. The discovered metadata written to the runtime tier (tier 1.5) sets `OutputLimit` to `min(32768, window/4)` — mirroring the sp4rk SDK's built-in fallback of 32768 so self-hosted models are not regressed (neither LM Studio nor vLLM expose a per-model output cap), but clamped to at most a quarter of the discovered window so a small-context model cannot disable compaction (an `OutputLimit` larger than the context window drives `EffectiveMax` negative) — and `TokenizerType` to `approximate`.

**Settings-facing resolution.** UI paths that must never block resolve model metadata through the registry's network-free `ModelRegistry.ResolveLocal` (sp4rk): `GetConfig`'s `AllModels` enrichment (`backend/frontend_api_config.go` `collectAllModels`) serves overrides, built-ins, fuzzy matches, and cached entries (including LM Studio probe results written via `SetRuntimeMetadata` to the runtime tier 1.5, which `ResolveLocal` also serves) purely from memory, returning fallback defaults for unknown models. Runtime resolution keeps the full `Resolve` path (network tiers, guarded by a negative cache that suppresses repeat failed probes). The network-free `GetConfig` invariant is specified in [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md).

## Output-Token Reserve

The output-token budget for a model resolves through the same tiering as the context window (first match wins):

1. **Per-model override** — `llm.models.<name>.output_limit` (user config tier).
2. **Per-provider override** — `llm.<provider>.output_token_reserve` (`anthropic`, `chatgpt`, `openai_compatible.<name>`, `anthropic_compatible.<name>`), applied at router construction by `core/builder.go` `applyProviderOutputReserves`: it seeds `ModelMetadata.OutputLimit` into the registry overrides for every model the provider lists. An explicit per-model `output_limit` is never clobbered, and because the seeded value lands in the overrides tier it also shadows the runtime probe cache — an operator-level statement that the gateway's real budget differs from the catalog.
3. **Global** — `executor.output_token_reserve` (default **8192**; modern coding/reasoning models regularly emit multi-thousand-token tool-call replies), carried into `llm.RouterConfig.OutputTokenReserve` as the fallback for models whose metadata carries no `OutputLimit`. The Small-LLM context variant raises this global fallback to 16384 when enabled (see [small-llm.md](small-llm.md)); the generation ceiling itself is always set by the per-model/per-provider tiers above.
4. **Discovered/static** — the probe cache (`min(32768, window/4)`) and the sp4rk SDK static fallback (32768).

The budget plays two roles: it is subtracted from the context window during overflow validation, and it caps the executor's per-request `MaxTokens` (the agent loop reads the model's `ContextWindow.OutputLimit()`), so a single provider-level knob adjusts both the validation reserve and the generation ceiling — the right granularity for self-hosted gateways (LM Studio, vLLM) whose effective limits differ from the built-in catalog.

## Configuration

Provider configuration lives in `config.yaml` under each provider block (api key, base URL, model list, defaults). Main-loop calls use `timeouts.llmRequestTimeout` (default 600 seconds); one-shot service calls for session titles, commit messages, and prompt optimization use the independent `timeouts.serviceLLMRequestTimeout` (default 120 seconds), so a stuck auxiliary request cannot inherit the ten-minute chat-loop budget. The authoritative reference for every tunable is `config.example.yaml`. Env vars are expanded as `${VAR}`; on macOS `config.LoadShellEnvironment()` runs before any other init so Finder-launched apps inherit shell env. For self-hosted servers (vLLM, llama.cpp, LM Studio, Ollama), reliable tool calling additionally requires **server-side** configuration — tool-call parser/chat-template selection per model family, sampling defaults, context-window sizing; the operational guide is [../../docs/self-hosted-models.md](../../docs/self-hosted-models.md).

## Related Specs

- [sp4rk llm-providers](https://github.com/v0lka/sp4rk/blob/main/specs/domains/llm-providers.md) — canonical `Router`, `ModelRegistry` (5-tier Resolve), token counting, `TrackingCaller`, retry/backoff
- [sp4rk llm-providers contract](https://github.com/v0lka/sp4rk/blob/main/specs/contracts/llm-providers.md) — `Provider`/`Router`/`Message`/`ChatRequest`/`ChatResponse` interface definitions
- [orchestration/router.md](orchestration/router.md) — routing uses the router for classification
- [../contracts/core-sp4rk.md](../contracts/core-sp4rk.md) — LLM interfaces at the core↔sp4rk boundary
