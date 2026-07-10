# LLM Providers

## Purpose

c0wrk does not implement LLM provider abstractions — `Provider`, `Router`, `ModelRegistry`, `TokenCounter`, and retry/backoff are **sp4rk engine** primitives. This spec documents only how c0wrk wires provider configuration into a sp4rk `Router`. The canonical provider/router/model-registry behavior is in [the sp4rk llm-providers spec](../../sdk/specs/domains/llm-providers.md) and [the sp4rk llm-providers contract](../../sdk/specs/contracts/llm-providers.md).

## Key Files

- `backend/configadapter.go` — `ToBuilderConfig(cfg)` builds `ProviderConfigs` from all providers with non-empty `Models`; sets `DefaultModel` (cross-provider, resolves to owning provider). Single conversion point for all config mapping.
- `core/builder.go` — `NewOrchestratorBuilder` creates a `github.com/v0lka/sp4rk/llm.Router` with providers (async, in `runAsyncInit()`); passes a `github.com/v0lka/sp4rk/llm.TokenCounter` to the engine
- `core/orchestrator.go` — holds the `Router` (as `modelSwitcher`) for runtime model switching; wraps the caller in `github.com/v0lka/sp4rk/llm.TrackingCaller` for usage tracking

Engine files (`github.com/v0lka/sp4rk/llm/router.go`, `modelregistry.go`, `provider_openai.go`, `provider_anthropic.go`, `provider_openai_responses.go`, `tokencount.go`, `message.go`) are documented in [the sp4rk llm-providers spec](../../sdk/specs/domains/llm-providers.md).

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

- **Per-task**: `HandleOptions.ModelOverride` (from the frontend model selector) switches the Conductor's model via `Router.SetModel`.
- **Per-delegation**: the `delegate` tool's `tasks[].model` field overrides the model for a subagent (empty = Conductor's model).
- Composite model IDs are `"provider/model"` (e.g. `openai/gpt-4o`, `anthropic/claude-3-7-sonnet`).

## Configuration

Provider configuration lives in `config.yaml` under each provider block (api key, base URL, model list, defaults). The authoritative reference for every tunable is `config.example.yaml`. Env vars are expanded as `${VAR}`; on macOS `config.LoadShellEnvironment()` runs before any other init so Finder-launched apps inherit shell env.

## Related Specs

- [sp4rk llm-providers](../../sdk/specs/domains/llm-providers.md) — canonical `Router`, `ModelRegistry` (5-tier Resolve), token counting, `TrackingCaller`, retry/backoff
- [sp4rk llm-providers contract](../../sdk/specs/contracts/llm-providers.md) — `Provider`/`Router`/`Message`/`ChatRequest`/`ChatResponse` interface definitions
- [orchestration/router.md](orchestration/router.md) — routing uses the router for classification
- [../contracts/core-sp4rk.md](../contracts/core-sp4rk.md) — LLM interfaces at the core↔sp4rk boundary
