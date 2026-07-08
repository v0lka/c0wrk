# LLM Providers

The `llm` package provides LLM provider abstractions, a model registry, and routing for multi-provider inference. It lets you configure one or more providers, switch models at runtime, count tokens, track usage, and recover from transient errors with automatic retry and backoff.

```go
import "github.com/v0lka/sp4rk/llm"
```

## Overview

At the center of the package is the **Router**, which holds one or more **providers** and routes every chat request to the currently active (provider, model) pair. A **ModelRegistry** supplies metadata (context window, capabilities, costs) used for pre-call validation and parameter adaptation. Token counting and usage tracking are layered on top so the agent loop can budget context and account for cost.

Key types:

| Type | Purpose |
| --- | --- |
| `ProviderEntry` | Declarative description of a single provider and its models |
| `Router` / `RouterConfig` | Routes calls to the active provider; holds retry config |
| `Provider` | Unified interface implemented by every provider backend |
| `ModelRegistry` / `ModelMetadata` | 5-tier metadata resolution for any model |
| `TokenCounter` / `ContextTokenTracker` | Token estimation and API-corrected tracking |
| `UsageTracker` / `TrackingCaller` | Per-session and per-step token accounting |
| `ChatRequest` / `ChatResponse` / `Message` | Request/response types sent to providers |

## ProviderEntry

`ProviderEntry` describes a single LLM provider with its enabled models. It is the declarative input you pass into `RouterConfig.Providers`.

```go
type ProviderEntry struct {
    Name         string   // logical name ("anthropic", "openai_compatible", …)
    ProviderType string   // provider type: "openai" or "anthropic"
    APIKey       string   // already-expanded API key
    BaseURL      string   // already-expanded base URL
    Models       []string // enabled model names for this provider
}
```

- **`Name`** — a logical, caller-chosen identifier used in logging, error reporting, and composite model IDs. It is *not* a hardcoded family name, so you can name compatible proxies anything you like (e.g. `"lmstudio"`, `"my-anthropic-proxy"`).
- **`ProviderType`** — selects the backend implementation. Supported values: `"anthropic"` and `"openai"`. The `"openai"` type covers any OpenAI-compatible endpoint (set `BaseURL` to point at a proxy, LM Studio, vLLM, etc.).
- **`APIKey`** / **`BaseURL`** — must be pre-resolved by the caller (environment variables expanded, etc.) before constructing the router.
- **`Models`** — the bare model names enabled for this provider. The first provider's first model becomes the initial active model.

## Router

`Router` routes LLM calls to the active provider. It is created with `NewRouter`:

```go
func NewRouter(ctx context.Context, cfg RouterConfig, registry *ModelRegistry) (*Router, error)
```

`NewRouter` builds a provider for every `ProviderEntry`, constructs a reverse index from composite model IDs to providers, and selects the first provider's first model as the initial active model. If `registry` is non-nil, providers may register their own metadata sources with it.

> **Concurrency:** `Router` is **safe for concurrent use** from multiple goroutines. It is protected by a `sync.RWMutex`: `SetModel` takes a write lock; `Call` snapshots the active provider and model under a read lock, then releases it before the retry loop so `SetModel` is not blocked by backoff sleeps. The Framework shares one Router across all Conductors created via `NewConductor`.

### RouterConfig

```go
type RouterConfig struct {
    Providers           []ProviderEntry
    MaxRetries          int           // default 3 when unset/zero/negative
    InitialBackoff      time.Duration // default 1s
    MaxBackoff          time.Duration // default 30s
    SafetyMarginPercent int           // default 5
    OutputTokenReserve  int           // default 4096
    HTTPClient          *http.Client  // optional proxy-configured client
    SamplingFunc        SamplingFunc  // optional family-aware temperature defaults
    Logger              *slog.Logger  // optional logger for ambiguity warnings
}
```

All values must be pre-resolved by the caller (env vars expanded, durations parsed) before calling `NewRouter`.

## Model identification: composite vs bare IDs

A **composite model identifier** has the form `"providerName/modelName"` and is the internal *selector* used to route a request to a specific (provider, model) pair. This disambiguates models that share the same bare name across multiple OpenAI-compatible providers (e.g. `"openai/gpt-4"` vs `"lmstudio/gpt-4"`).

The **bare model name** (the part after the first `/`) is what is sent to the LLM API and used for model metadata lookups. Model names may themselves contain a `/` (e.g. `"meta-llama/Llama-3-70b"`), so identifiers are always split on the **first** `/` only.

Helper functions in the package:

```go
// Build a composite identifier from a provider name and a bare model name.
id := llm.CompositeModelID("openai", "gpt-4o") // "openai/gpt-4o"

// Split a composite identifier. ok is false when id has no "/".
provider, model, ok := llm.ParseCompositeModelID(id)

// Report whether id carries a provider prefix.
llm.IsCompositeModelID(id) // true

// Return the bare model name portion (unchanged if already bare).
llm.BareModel("openai/gpt-4o") // "gpt-4o"

// Parse a composite identifier into its parts.
provider, model, ok := llm.ParseCompositeModelID("openai/gpt-4o") // "openai", "gpt-4o", true
```

## Runtime model switching

The router exposes the active selection and lets you switch it at runtime:

```go
// ActiveModel returns the composite identifier ("provider/model").
func (r *Router) ActiveModel() string

// ActiveProviderName returns the logical name of the active provider.
func (r *Router) ActiveProviderName() string

// SetModel switches the active provider and model.
func (r *Router) SetModel(ctx context.Context, model string) error
```

`SetModel` accepts either a composite `"provider/model"` identifier (which routes directly to the named provider) or a bare model name (resolved to the first matching provider for backward compatibility). When a bare name matches multiple providers, the first match — deterministic, sorted by composite ID — is selected and a warning is logged when a logger is configured. Use a composite identifier to disambiguate explicitly.

```go
// Switch to a specific provider's model.
if err := router.SetModel(ctx, "openai/gpt-4o"); err != nil {
    log.Fatal(err)
}
fmt.Printf("active: %s (provider: %s)\n",
    router.ActiveModel(), router.ActiveProviderName())

// Bare name resolves to the first matching provider.
if err := router.SetModel(ctx, "claude-sonnet-4-5"); err != nil {
    log.Fatal(err)
}
```

## Multi-provider configuration

Configure multiple providers in a single `RouterConfig` to enable runtime model switching — for example, using a strong reasoning model for planning and a faster model for execution:

```go
package main

import (
    "context"
    "log"
    "os"

    "github.com/v0lka/sp4rk/llm"
)

func main() {
    registry := llm.NewModelRegistry(nil)

    providers := []llm.ProviderEntry{
        {
            Name:         "anthropic",
            ProviderType: "anthropic",
            APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
            Models:       []string{"claude-sonnet-4-5"},
        },
        {
            Name:         "openai",
            ProviderType: "openai",
            APIKey:       os.Getenv("OPENAI_API_KEY"),
            Models:       []string{"gpt-4o", "gpt-4o-mini"},
        },
    }

    router, err := llm.NewRouter(context.Background(), llm.RouterConfig{
        Providers:           providers,
        MaxRetries:          3,
        InitialBackoff:      1 * 1000_000_000, // 1s
        MaxBackoff:          30 * 1000_000_000, // 30s
        SafetyMarginPercent: 5,
        OutputTokenReserve:  4096,
    }, registry)
    if err != nil {
        log.Fatal(err)
    }

    // Initial active model is the first provider's first model.
    log.Printf("active: %s (%s)",
        router.ActiveModel(), router.ActiveProviderName())

    // Switch to an OpenAI model for execution.
    if err := router.SetModel(context.Background(), "openai/gpt-4o"); err != nil {
        log.Fatal(err)
    }

    resp, err := router.Call(context.Background(), llm.ChatRequest{
        Messages: []llm.Message{
            {Role: "user", Content: "Say hello in one sentence."},
        },
        MaxTokens: 64,
    })
    if err != nil {
        log.Fatal(err)
    }
    log.Print(resp.Message.Content)
}
```

## ModelRegistry

`ModelRegistry` provides a 5-tier resolution system for model metadata. It is thread-safe and lazily fetches from external sources.

```go
registry := llm.NewModelRegistry(nil) // nil = no user overrides
```

`NewModelRegistry` accepts an optional `overrides` map that is defensively copied at construction time, so callers (e.g. config reloads) can mutate their own map without racing the registry's concurrent readers.

### 5-tier resolution

`Resolve(ctx, model)` returns `ModelMetadata` and a boolean indicating whether the model was found in a known source. When `ok` is false, the returned metadata contains usable fallback defaults.

```go
func (r *ModelRegistry) Resolve(ctx context.Context, model string) (ModelMetadata, bool)
```

Resolution order (first match wins):

1. **User overrides** — from the `overrides` map passed to `NewModelRegistry`.
2. **Built-in registry** — a hardcoded table of well-known models (OpenAI, Anthropic, Google, DeepSeek, Qwen, GLM, Kimi, xAI Grok, …).
3. **Cache** — results from previous external lookups.
4. **External sources** — HuggingFace API lookup (lazy, cached), then any sources registered via `RegisterSource` (e.g. an LM Studio provider).
5. **Fallback defaults** — `ContextWindow: 128000`, `OutputLimit: 4096`, `TokenizerType: "approximate"`; `ok` is false.

Additional methods:

- `RegisterSource(src ModelMetadataSource)` — add a custom metadata source. Sources are called in order after the HuggingFace lookup fails.
- `Invalidate(model)` — remove a cached entry (e.g. on a mid-session model change).
- `SetHTTPClient(client)` — replace the HTTP client used for HuggingFace lookups.

### ModelMetadata

```go
type ModelMetadata struct {
    ContextWindow int
    OutputLimit   int
    TokenizerType string
    Family        string
    Capabilities  ModelCapabilities
}
```

- **`ContextWindow`** — maximum input context size in tokens.
- **`OutputLimit`** — maximum output tokens the model can produce.
- **`TokenizerType`** — e.g. `"tiktoken/o200k_base"`, `"anthropic-api"`, `"approximate"`. Drives token counter selection.
- **`Family`** — model family string used for prompt and parameter adaptation. Resolved from metadata or auto-detected via `DetectFamily`.
- **`Capabilities`** — see below.

### ModelCapabilities

```go
type ModelCapabilities struct {
    Attachment  bool // image/PDF support
    Reasoning   bool // reasoning/thinking mode
    Temperature bool // accepts the temperature parameter
    ToolCall    bool // function calling support
}
```

The router uses `Capabilities.Temperature` to decide whether to send a temperature parameter at all — reasoning models that reject temperature are skipped automatically.

### Model families

`DetectFamily(modelID)` determines a model's family from its ID string. Families drive prompt and parameter adaptation. Recognized families include `anthropic`, `openai_flagship`, `openai_standard`, `openai_codex`, `google`, `mistral`, `deepseek`, `qwen`, `glm`, `kimi`, and `default`.

```go
family := llm.DetectFamily("claude-sonnet-4-5") // FamilyAnthropic
```

`FamilyReasoningOptions(family)` returns the native reasoning/thinking options available for a family, the recommended default (always the maximum available effort), and whether the family supports reasoning at all.

## Token counting

```go
type TokenCounter interface {
    Count(text string) int
    CountMessages(msgs []Message) int
}
```

Two implementations are provided:

- **`SimpleTokenCounter`** — fast approximation using the ~4 chars = 1 token rule. Created with `NewSimpleTokenCounter()`.
- **`TiktokenCounter`** — accurate counter using `tiktoken-go` for OpenAI models. Created with `NewTiktokenCounter(encoding)`. Its `Encode` method is not safe for concurrent use, so it is guarded by an internal mutex.

`NewTokenCounter(tokenizerType)` creates a `TokenCounter` based on the tokenizer type from model metadata. Supported types:

| Tokenizer type | Counter |
| --- | --- |
| `tiktoken/o200k_base`, `tiktoken/cl100k_base`, … | `TiktokenCounter` |
| `anthropic-api` | `SimpleTokenCounter` (relies on API correction) |
| `approximate`, `""`, or unknown | `SimpleTokenCounter` |

The returned counter is always valid (never nil). The error indicates that a fallback counter was used instead of the requested type.

```go
counter, err := llm.NewTokenCounter("tiktoken/o200k_base")
// err is nil on success; non-nil means a fallback SimpleTokenCounter was used.
n := counter.CountMessages(msgs)
```

### ContextTokenTracker

`ContextTokenTracker` is a hybrid coordinator that combines predictive counting with API-corrected actuals. It uses a predictive counter for estimates between API calls, then corrects with the real `input_tokens` from API responses.

```go
tracker := llm.NewContextTokenTracker(counter)
tracker.AddDelta("some text to estimate") // add to pending estimate
total := tracker.EstimateTotal()          // lastKnownUsed + pendingDelta
tracker.Correct(resp.Usage.InputTokens)   // reconcile with API actuals
tracker.Reset()                           // zero both counters
```

## Usage tracking

`UsageTracker` accumulates token usage across all LLM calls in a session. It is thread-safe and supports observer callbacks.

```go
tracker := llm.NewUsageTracker()
tracker.AddObserver(func(usage llm.TokenUsage, totalIn, totalOut int, model, family string) {
    log.Printf("call: in=%d out=%d | session total: in=%d out=%d (%s)",
        usage.InputTokens, usage.OutputTokens, totalIn, totalOut, model)
})
tracker.Record(resp.Usage, resp.Model, resp.Family)
in, out := tracker.Totals()
```

### TrackingCaller

`TrackingCaller` wraps a `Caller` and automatically records usage from each response into a `UsageTracker` and corrects an optional `ContextTokenTracker`. It implements the `Caller` interface, so it can be dropped in anywhere a caller is expected.

```go
type TrackingCaller struct { /* ... */ }

func NewTrackingCaller(inner Caller, tracker *UsageTracker) *TrackingCaller
func (tc *TrackingCaller) Call(ctx context.Context, req ChatRequest) (*ChatResponse, error)
func (tc *TrackingCaller) WithContextTracker(t *ContextTokenTracker) *TrackingCaller
```

`WithContextTracker` returns a new `TrackingCaller` that shares the same inner caller and session-level `UsageTracker` but corrects a per-step `ContextTokenTracker`. Use this to create step-local callers for parallel execution.

```go
sessionTracker := llm.NewUsageTracker()
caller := llm.NewTrackingCaller(router, sessionTracker)

// Per-step context tracker for a single execution branch.
stepTracker := llm.NewContextTokenTracker(counter)
stepCaller := caller.WithContextTracker(stepTracker)
resp, err := stepCaller.Call(ctx, req)
```

## Retry & backoff

The router retries transient errors with exponential backoff plus ±20% jitter. Retryable errors are classified by `WrapProviderError` and include:

- **HTTP 429, 502, 503, 529** — rate limits and transient server errors.
- **Transient network errors** — timeouts, connection refused/reset, EHOSTUNREACH, DNS errors, unexpected EOF.

`IsRetryable(err)` reports whether an error chain contains a retryable `*Error`.

Backoff doubles after each attempt and is capped at `MaxBackoff`:

| Attempt | Backoff (default config) |
| --- | --- |
| 1 → 2 | 1s (±20% jitter) |
| 2 → 3 | 2s (±20% jitter) |
| 3 → 4 | 4s (±20% jitter) |

With the default `MaxRetries: 3`, the worst-case retry path adds up to ~7s of latency. Callers that rely on error propagation for compaction timing, circuit-breaker resets, or budget control should account for this.

### Disabling retries

`MaxRetries` defaults to **3** when unset or zero. Any non-positive value (including negatives) is replaced with the default of 3 — there is currently no way to disable retries entirely via this field. To minimise retry latency, set a small `MaxRetries` (e.g. `1`) and a short `MaxBackoff`:

```go
router, err := llm.NewRouter(ctx, llm.RouterConfig{
    Providers:      providers,
    MaxRetries:     1,    // one retry attempt
    MaxBackoff:     1 * time.Second,
}, registry)
```

## ChatRequest / ChatResponse

```go
type ChatRequest struct {
    Model           string           // bare model name (filled from active model if empty)
    ModelFamily     string           // family hint; auto-detected if empty
    Messages        []Message
    Tools           []ToolDefinition
    MaxTokens       int
    Temperature     *float64         // nil = use provider/sampling default
    ReasoningEffort string           // native reasoning value (e.g. "On", "high")
}

type ChatResponse struct {
    Model      string
    Family     string
    Message    Message
    Reasoning  string     // extended thinking/reasoning (if supported)
    Usage      TokenUsage
    StopReason string     // "end_turn" | "tool_use" | "max_tokens"
}

type TokenUsage struct {
    InputTokens  int
    OutputTokens int
}
```

`Router.Call` fills in the active bare model when `Model` is empty, applies the default temperature, validates the context window, then sends the request. On success it ensures `Model` and `Family` are set on the response and trims trailing whitespace from content and reasoning fields.

## Message types

```go
type Message struct {
    Role             string          // "system" | "user" | "assistant" | "tool"
    Content          string
    ReasoningContent string          // chain-of-thought (e.g. DeepSeek)
    ToolCalls        []ToolCall      // tool calls (for assistant)
    ToolCallID       string          // call ID (for tool responses)
    ReasoningItems   []ReasoningItem // Responses API reasoning output items
}

type ToolCall struct {
    ID    string
    Name  string
    Input json.RawMessage
}

type ReasoningItem struct {
    ID      string // required to round-trip to the Responses API
    Summary string
}
```

- **`system` / `user` / `assistant` / `tool`** — the four roles. `tool` messages carry a `ToolCallID` correlating them to the originating call.
- **`ToolCalls`** — emitted by assistant messages when the model decides to call a tool.
- **`ReasoningContent`** — chain-of-thought text (e.g. DeepSeek).
- **`ReasoningItems`** — reasoning output items from the OpenAI Responses API. Each item's `ID` is required when sending the item back to the API in subsequent requests to maintain the reasoning chain across turns.

`ToolDefinition` describes a tool to the LLM as a JSON Schema:

```go
type ToolDefinition struct {
    Name        string
    Description string
    InputSchema json.RawMessage
}
```

## Safety margin and output token reserve

Before every call, the router validates that the estimated token count of the messages fits within the model's context window minus the output reserve.

- **`SafetyMarginPercent`** (default 5) — a percentage of the effective context window reserved to account for counting inaccuracy. The effective maximum is reduced by this fraction before comparison.
- **`OutputTokenReserve`** (default 4096) — the default output token reserve used when model metadata does not specify an `OutputLimit`.

If the estimated count exceeds the effective maximum, the router returns a non-retryable `*Error` wrapping the `ErrContextWindowExceeded` sentinel. Detect it with `errors.Is`:

```go
if errors.Is(err, llm.ErrContextWindowExceeded) {
    // request was too large — compact or trim before retrying
}
```

> This pre-submission guard rejects obviously oversized requests. It is intentionally independent from ongoing context-fill tracking in the agent loop.

## SamplingFunc for family-aware temperature defaults

```go
type SamplingFunc func(family string) *float64
```

When `ChatRequest.Temperature` is nil, the router applies a default temperature based on the active model's family. Set `RouterConfig.SamplingFunc` to control this. Return `nil` to use the provider's built-in default (no temperature parameter sent).

The router skips temperature application entirely for models whose `Capabilities.Temperature` is false (e.g. reasoning models like o1/o3). When no sampling function is configured, the fallback default is deterministic (temperature 0.0).

```go
router, err := llm.NewRouter(ctx, llm.RouterConfig{
    Providers: providers,
    SamplingFunc: func(family string) *float64 {
        switch family {
        case "anthropic":
            t := 1.0
            return &t
        case "openai_flagship":
            t := 0.7
            return &t
        default:
            return nil // use provider default
        }
    },
}, registry)
```

## Error types

Provider errors are wrapped in a classified `*Error`:

```go
type Error struct {
    Provider   string // e.g. "openai", "anthropic"
    StatusCode int    // HTTP status code (0 if not applicable)
    Retryable  bool   // whether this error is safe to retry
    Err        error  // the original underlying error
}
```

- `NewError(provider, statusCode, retryable, err)` — construct directly.
- `WrapProviderError(provider, statusCode, err)` — classify by HTTP status and network error type.
- `IsRetryable(err)` — true when the chain contains a `*Error` with `Retryable == true`.
- `NewContextWindowError(...)` — non-retryable error for context window overflow, wrapping `ErrContextWindowExceeded`.
