# LLM Providers

## Purpose

Provides LLM provider abstractions, a model registry, and routing for multi-provider inference. Configure one or more providers, switch models at runtime, count tokens, track usage, and recover from transient errors with automatic retry and backoff. At the center is the **Router**, which routes every chat request to the currently active (provider, model) pair.

## Key Files

- `github.com/v0lka/sp4rk/llm` — `Router`, `RouterConfig`, `NewRouter`, `SetModel`/`ActiveModel`/`Call`, `Provider` interface, `ProviderEntry`
- `github.com/v0lka/sp4rk/llm` (metadata) — `ModelRegistry`, `ModelMetadata`, `ModelCapabilities`, `DetectFamily`, `FamilyReasoningOptions`
- `github.com/v0lka/sp4rk/llm` (token accounting) — `TokenCounter`, `SimpleTokenCounter`, `TiktokenCounter`, `NewTokenCounter`, `ContextTokenTracker`, `UsageTracker`, `TrackingCaller`
- `github.com/v0lka/sp4rk/llm` (request/response) — `ChatRequest`, `ChatResponse`, `Message`, `ToolCall`, `ToolDefinition`, `TokenUsage`
- `github.com/v0lka/sp4rk/llm` (errors) — `Error`, `WrapProviderError`, `IsRetryable`, `ErrContextWindowExceeded`

## Core Types

```go
type ProviderEntry struct {
    Name         string   // logical name ("anthropic", "openai_compatible", …)
    ProviderType string   // "openai" | "anthropic"
    APIKey       string   // pre-expanded key
    BaseURL      string   // pre-expanded base URL
    Models       []string // enabled model names
}

type ModelMetadata struct {
    ContextWindow int
    OutputLimit   int
    TokenizerType string  // "tiktoken/o200k_base", "anthropic-api", "approximate"
    Family        string
    Capabilities  ModelCapabilities
}

type ModelCapabilities struct {
    Attachment, Reasoning, Temperature, ToolCall bool
}

type TokenCounter interface {
    Count(text string) int
    CountMessages(msgs []Message) int
}
```

## Flow

```
Router.Call(ctx, req)
│
├─ Snapshot active (provider, model) under a read lock; release before the retry loop
│      so SetModel is not blocked by backoff sleeps.
├─ Fill the bare model name when req.Model is empty; auto-detect family if empty.
├─ Apply family-aware default temperature (via SamplingFunc) unless req.Temperature is set;
│      skip temperature entirely for models whose Capabilities.Temperature is false.
├─ Validate estimated tokens against the effective context window (SafetyMarginPercent +
│      OutputTokenReserve); reject oversized requests with ErrContextWindowExceeded.
├─ Send the request to the active provider (Anthropic or OpenAI-compatible backend).
└─ Retry transient errors (HTTP 429/502/503/529, transient network errors) with
      exponential backoff + ±20% jitter, up to MaxRetries.
```

## Model identification: composite vs bare IDs

A **composite model identifier** `"providerName/modelName"` is the internal selector that routes a request to a specific (provider, model) pair, disambiguating models that share a bare name across multiple OpenAI-compatible providers. The **bare model name** (part after the first `/`) is what is sent to the API and used for metadata lookups; identifiers are split on the **first** `/` only (model names may themselves contain `/`). Helpers: `CompositeModelID`, `ParseCompositeModelID`, `IsCompositeModelID`, `BareModel`.

### Runtime model switching

`ActiveModel()` returns the composite identifier; `ActiveProviderName()` the logical provider name; `SetModel(ctx, model)` accepts a composite ID (routes directly) or a bare name (resolves to the first matching provider — deterministic, sorted by composite ID — logging a warning on ambiguity). The Router is **safe for concurrent use** (`sync.RWMutex`); a Framework shares one Router across all Conductors.

## ModelRegistry

`NewModelRegistry(overrides)` is thread-safe and lazily fetches external metadata. `Resolve(ctx, model)` uses a 5-tier resolution (first match wins): (1) user overrides; (2) built-in registry of well-known models; (3) cache; (4) external sources — HuggingFace API lookup (lazy, cached), then sources registered via `RegisterSource` (e.g. an LM Studio provider); (5) fallback defaults (`ContextWindow: 128000`, `OutputLimit: 4096`, `ok == false`). `Invalidate(model)` clears a cached entry; `SetHTTPClient` replaces the HF lookup client. `DetectFamily(modelID)` determines a model's family from its ID string, driving prompt and parameter adaptation (families: `anthropic`, `openai_flagship`/`openai_standard`/`openai_codex`, `google`, `mistral`, `deepseek`, `qwen`, `glm`, `kimi`, `default`).

## Token counting & usage tracking

Two counters: `SimpleTokenCounter` (~4 chars = 1 token approximation) and `TiktokenCounter` (accurate, tiktoken-go, mutex-guarded). `NewTokenCounter(tokenizerType)` selects by metadata type (`tiktoken/*` → Tiktoken; `anthropic-api`/`approximate`/unknown → Simple; always returns a valid counter).

`ContextTokenTracker` combines predictive counting with API-corrected actuals (`AddDelta`/`EstimateTotal`/`Correct(apiInputTokens)`/`Reset`). `UsageTracker` accumulates token usage across a session (thread-safe, observer callbacks). `TrackingCaller` wraps a `Caller` to record usage into a `UsageTracker` and correct a `ContextTokenTracker`; `WithContextTracker` returns a step-local caller sharing the same inner caller and session tracker — use it for parallel execution branches.

## Invariants

- The Router is safe for concurrent use; `SetModel` takes a write lock, `Call` snapshots under a read lock and releases before backoff.
- `Resolve` always returns usable metadata, even for unknown models (fallback defaults).
- `NewTokenCounter` always returns a non-nil counter.
- Pre-call validation rejects oversized requests with `ErrContextWindowExceeded` (detectable via `errors.Is`); this is independent of the agent loop's ongoing context-fill tracking.
- Retryable errors are classified by `WrapProviderError` (HTTP 429/502/503/529, transient network errors); `IsRetryable` reports whether a chain contains a retryable `*Error`.

## Configuration

`RouterConfig`:

| Field | Default | Description |
| ----- | ------- | ----------- |
| `MaxRetries` | `3` (when unset/zero/negative) | Retry attempts for transient errors. Any non-positive value is replaced with 3 — there is no way to fully disable retries via this field; set a small value + short `MaxBackoff` to minimize latency. |
| `InitialBackoff` / `MaxBackoff` | `1s` / `30s` | Exponential backoff bounds (doubles each attempt, ±20% jitter). |
| `SafetyMarginPercent` | `5` | Effective context window fraction reserved for counting inaccuracy. |
| `OutputTokenReserve` | `4096` | Default output reserve when metadata lacks an `OutputLimit`. |
| `HTTPClient` | optional | Proxy-configured client. |
| `SamplingFunc` | optional | `func(family string) *float64`; return `nil` to use the provider default. |
| `Logger` | optional | Logs ambiguity warnings on bare-name resolution. |

`APIKey`/`BaseURL`/`Models` must be pre-resolved by the caller (env vars expanded, durations parsed) before `NewRouter`.

## Extension Points

- **Add a provider**: a new `ProviderType` backend implementing the `Provider` interface; the Router dispatches by `ProviderType` (`"anthropic"` / `"openai"`). The `"openai"` type covers any OpenAI-compatible endpoint (set `BaseURL` to a proxy, LM Studio, vLLM, …).
- **Custom metadata source**: `ModelRegistry.RegisterSource(src)` for a source consulted after the HuggingFace lookup (e.g. a local model server).
- **Family-aware sampling**: supply `RouterConfig.SamplingFunc` (see [../prompt-building.md](prompt-building.md) for the SDK's `DefaultSampling` defaults).
- **Step-local callers**: `TrackingCaller.WithContextTracker` for independent context trackers in parallel branches.

## Related Specs

- [prompt-building.md](prompt-building.md) — family-aware sampling defaults
- [../orchestration/README.md](orchestration/README.md) — the Router is the LLM caller for the Conductor, planner, router, and reflector
- [../memory/README.md](memory/README.md) — `ModelMetadata` drives `ContextWindow` sizing
- [../embedding.md](embedding.md) — local embeddings (no LLM provider required)
