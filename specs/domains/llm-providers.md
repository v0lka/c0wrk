# LLM Providers

## Purpose

Abstracts multiple LLM providers behind a unified interface, with model routing, token counting, reasoning effort control, and usage tracking.

## Key Files

- `sdk/llm/provider.go` — Provider interface
- `sdk/llm/router.go` — Router (multi-provider routing, SetModel, retries)
- `sdk/llm/modelregistry.go` — ModelRegistry (model metadata)
- `sdk/llm/family.go` — model family classification
- `sdk/llm/reasoning.go` — ReasoningEffort resolution
- `sdk/llm/tokencount.go` — token counting
- `sdk/llm/usage.go` — TrackingCaller (usage tracking wrapper)
- `sdk/llm/errors.go` — typed provider errors (rate limit, context exceeded, etc.)
- `sdk/llm/message.go` — Message / ChatRequest / ChatResponse / TokenUsage / ToolDefinition types
- `sdk/llm/schema_sanitize.go` — JSON Schema sanitization for function calling
- `sdk/llm/provider_helpers.go` — shared provider helpers (retry, stop-reason mapping, message extraction)
- `sdk/llm/provider_openai.go` — OpenAI provider (ChatGPT / OpenAI-compatible Chat Completions transport)
- `sdk/llm/provider_openai_responses.go` — OpenAI Responses API transport (for reasoning-capable models)
- `sdk/llm/provider_anthropic.go` — Anthropic provider
- `sdk/llm/provider_gemini.go` — Google Gemini provider
- `sdk/llm/provider_lmstudio.go` — LM Studio provider
- `sdk/prompt/builder.go` — fluent prompt building with cache-break markers for Anthropic prompt caching
- `sdk/prompt/sampling.go` — family-aware default temperatures (SamplingConfig, DefaultSampling)

## Core Types

```go
// Provider interface
type Provider interface {
    ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    Name() string
}

// Router selects and routes to active provider
type Router struct {
    providers          map[string]Provider // all registered providers
    activeProvider     Provider            // currently active provider
    activeModel        string              // default model
    activeProviderName string              // logical name of active provider
    maxRetries         int                 // max retry attempts on retryable errors
    initialBackoff     time.Duration       // initial backoff duration
    maxBackoff         time.Duration       // max backoff duration
    registry           *ModelRegistry      // model metadata for context validation
    tokenCounter       TokenCounter        // token estimator for pre-call validation
    sampling           SamplingFunc        // family-aware temperature defaults
    safetyMarginPercent int                // percentage of context window reserved as safety margin (default: 5)
    outputTokenReserve int                 // default output token reserve (default: 4096)
}

// RouterConfig holds pre-resolved provider entries for Router construction
type ProviderEntry struct {
    Name         string
    ProviderType string
    APIKey       string
    BaseURL      string
    Models       []string // enabled models for this provider
}

type RouterConfig struct {
    Providers           []ProviderEntry
    MaxRetries          int
    InitialBackoff      time.Duration
    MaxBackoff          time.Duration
    SafetyMarginPercent int
    OutputTokenReserve  int
    HTTPClient          *http.Client    // optional proxy-configured HTTP client (nil = default)
    SamplingFunc        SamplingFunc
}

// SamplingFunc returns a default temperature for the given model family.
// Return nil to use the provider's built-in default.
type SamplingFunc func(family string) *float64

// ModelMetadata from registry
type ModelMetadata struct {
    ContextWindow     int    // total token capacity
    OutputLimit       int    // max output tokens
    TokenizerType     string // e.g., "cl100k_base"
    Family            string // e.g., "openai", "anthropic"
    Capabilities      ModelCapabilities
    InputCostPer1M    float64 // USD per 1M input tokens
    OutputCostPer1M   float64 // USD per 1M output tokens
    CacheReadCostPer1M float64 // USD per 1M cache-read tokens (Anthropic prompt caching)
    CacheWriteCostPer1M float64 // USD per 1M cache-write tokens (Anthropic prompt caching)
}

// ReasoningEffort is a plain string — the native reasoning effort value
// for the target model family (e.g., "high"/"low" for OpenAI, "On"/"Off" for Anthropic).
// It is passed directly to the provider without translation.
type ReasoningEffort = string
```

## Flow

```
core.Orchestrator needs LLM call
  → uses LLMCaller interface (which is llm.Router or TrackingCaller)
      │
      ▼
llm.Router.Call(ctx, ChatRequest)
  ├─ Select active provider via reverse index (modelToProvider)
  ├─ Add reasoning effort to request
  ├─ Call provider.ChatCompletion()
  ├─ On rate limit: exponential backoff + retry
  ├─ On context window exceeded: return specific error
  └─ Return ChatResponse
```

## Provider Implementations

| Provider (`provider_name`) | Transport file(s)                                 | Special Features                                  |
| -------------------------- | ------------------------------------------------- | ------------------------------------------------- |
| `anthropic`                | provider_anthropic.go                             | Prompt caching (CacheBreak + ephemeral), thinking |
| `gemini`                         | provider_gemini.go                                | Safety settings, large context                    |
| `lmstudio`                       | provider_lmstudio.go                              | Any local model, OpenAI-compatible API            |
| `openai_compatible`              | provider_openai.go                                | Generic OpenAI-compatible API endpoint. Uses Chat Completions for all models. The Responses API is NOT used — it is an OpenAI-specific endpoint, not part of the "OpenAI-compatible" standard. |
| `anthropic_compatible`           | provider_anthropic.go                             | Generic Anthropic Messages-API-compatible endpoint. Same transport as the fixed `anthropic` provider but with a custom `base_url` and logical name. Reuses Anthropic prompt caching, thinking, and tool-use conversion. `max_tokens` defaults to 8192 when caller does not set it (Anthropic API requires it > 0). |
| `chatgpt`                        | provider_openai.go + provider_openai_responses.go | Official OpenAI endpoint. `openai_codex` family routes to Responses API automatically (codex models require `/v1/responses`). Reasoning items round-tripped (see below). |

### Responses API Reasoning Round-Trip

The `openai_codex` model family (e.g. `gpt-5.3-codex`) uses the OpenAI Responses API instead of Chat Completions **on the official OpenAI endpoint only** (`chatgpt` provider). The Responses API (`/v1/responses`) is an OpenAI-specific endpoint, not part of the "OpenAI-compatible" standard — compatible providers (`openai_compatible`) use Chat Completions for all models including codex.

**Without round-tripping**, the model's committed plan (e.g. "I have enough info, now I'll edit line 42") exists only in reasoning tokens that are dropped between ReAct iterations. This causes the agent to revert to read-only exploration every turn, never escalating to mutations.

The Responses API provider (`provider_openai_responses.go`) handles this:

- **Response extraction** (`convertResponsesResponse`): reasoning items from `resp.Output` are extracted into `Message.ReasoningItems` (ID + Summary), `Message.ReasoningContent` (concatenated summaries), and `ChatResponse.Reasoning`.
- **Request round-trip** (`convertToResponsesInput`): for assistant messages with `ReasoningItems`, each item is re-emitted as a `ResponseInputItemParamOfReasoning(id, summary)` input item **before** the message content and function calls. The original `ID` is preserved, which is required by the Responses API to maintain the reasoning chain across turns.

`Message.ReasoningItems` is populated only by the Responses API provider. Other providers (Chat Completions, Anthropic, etc.) use `ReasoningContent` / `Reasoning` as plain strings.

## Reasoning Effort

Controls extended thinking (reasoning tokens) for supported models. Reasoning effort is a plain string — the native value for the model's family. No translation or role-based adaptation occurs.

```go
// Get available reasoning options and default for a model family
func FamilyReasoningOptions(family string) (options []string, default_ string, ok bool)
// e.g., FamilyReasoningOptions("anthropic") → (["On", "Off"], "On", true)
//       FamilyReasoningOptions("openai_flagship") → (["minimal", "low", "medium", "high"], "high", true)

// Model-aware variant: returns version-specific options when the model matters
// (e.g. GLM 5.2+), otherwise delegates to FamilyReasoningOptions.
func ModelReasoningOptions(family, model string) (options []string, default_ string, ok bool)
// e.g., ModelReasoningOptions("glm", "glm-5.2") → (["none", "max", "high"], "max", true)
//       ModelReasoningOptions("glm", "glm-5.1") → (["On", "Off"], "On", true)
```

The backend populates `AllModels[].Reasoning` via `ModelReasoningOptions` so the frontend shows model-version-accurate options.

### GLM 5.2+ reasoning_effort

GLM 5.2+ introduced the `reasoning_effort` parameter (`max`/`high`), honored when
thinking is enabled. The OpenAI-compatible provider (`provider_openai.go`,
`applyGLMReasoning`) maps the UI options to native API fields:

| UI option | `thinking.type` | `reasoning_effort` |
| --------- | --------------- | ------------------ |
| `none`    | `disabled`      | *(omitted)*        |
| `max`     | `enabled`       | `max`              |
| `high`    | `enabled`       | `high`             |

The UI "Auto"/Default selection sends an empty effort. GLM 5.2 enables thinking by
default with `reasoning_effort=max`, so Auto is equivalent to `max`. Older GLM
models (< 5.2) keep the legacy `thinking.type` = `On`/`Off` control unchanged.

The full flow:

```
Frontend ReasoningCombobox (native values from GetConfig().AllModels.Reasoning.Options)
  → SendMessage(sessionId, text, mode, skills, modelOverride, reasoningEffort)
    → HandleOptions.ReasoningEffort (string)
      → Orchestrator.SetReasoningEffort() passes to router, planner, engine
        → Provider sets native API parameter directly (e.g., thinking.type, reasoning_effort)
```

## Model Registry

Maps model names to metadata (context window, output limit, tokenizer type, capabilities):

- Built-in registry with common models
- User can override via config (`models` section)
- `Resolve(ctx, modelName)` returns `(ModelMetadata, bool)` — `ok=false` if model not found

## Token Counting

- `TokenCounter` interface: `Count(text string) int` + `CountMessages(msgs []Message) int`
- Implementation uses tiktoken-go
- `ContextTokenTracker`: tracks cumulative usage per-step, corrects predictive estimates with API-reported counts

## TrackingCaller

Wraps LLMCaller to track token usage:

- Counts input/output tokens per call
- Records usage to `UsageTracker` (accumulates per-session totals) and notifies registered `UsageObserver` callbacks (the session emitter observes and emits `session_tokens` events)
- Provides `WithContextTracker()` for per-step accuracy

## Configuration

From `config.yaml` (yaml keys use `snake_case` throughout this section):

```yaml
llm:
  # Cross-provider default model — must be enabled in at least one provider below.
  # The Router resolves which provider owns this model name via its reverse index.
  default_model: "claude-sonnet-4-20250514"

  anthropic:
    api_key: "${ANTHROPIC_API_KEY}"
    models:                                    # enabled models for THIS provider
      - "claude-sonnet-4-20250514"
      - "claude-opus-4-20250514"

  openai_compatible:
    api_key: "${OPENAI_API_KEY}"
    base_url: ""
    models: []

  anthropic_compatible:
    api_key: "${ANTHROPIC_API_KEY}"
    base_url: ""
    models: []

  chatgpt:
    api_key: "${OPENAI_API_KEY}"
    models: []
```

> **Note**: `config.example.yaml` shows `anthropic`, `openai_compatible`, `anthropic_compatible`, and `chatgpt` blocks. `gemini` and `lmstudio` providers are supported (see provider table above) but not included in the example config — add them following the same structure when needed.

Default-model validation: `default_model` must be non-empty and must appear in at least one provider's `models` list. Only providers with non-empty `models` are registered in the Router. There is no `active_provider` field — the Router resolves which provider to use by looking up the model name in its `modelToProvider` reverse index.

> **SDK vs desktop config**: The SDK's `sdk.LLMConfig.DefaultModel` is **optional** — when empty, the Router auto-selects the first provider's first model. The desktop app's `config.yaml` `default_model` remains **required** (validated by `backend/config/config.go`); this validation is a desktop-app concern, not an SDK contract.

## Invariants

- The Router maintains a `modelToProvider` reverse index mapping each enabled model name to its owning provider
- The `default_model` is a single cross-provider value; it resolves to whichever provider owns that model name
- When `DefaultModel` is empty, the Router auto-selects the first provider's first model as the active model. `NewConductor` and `Build` read the active model from the Router (runtime source of truth), not from the config snapshot
- Per-message model override (`ModelOverride`) selects a different model for a single request without changing the Router's active model
- Rate limit errors trigger retry with backoff (not immediate failure)
- Context window exceeded errors are returned to caller (not retried)
- Token counting is best-effort (predictive until corrected by API response)
- Provider initialization failure prevents Build() from completing
- Reasoning effort is a native family-specific string (e.g., "high", "On", "Max") passed directly to the provider without role-based translation or resolution
- Responses API (`openai_codex` family) round-trips reasoning items: `convertResponsesResponse` extracts them into `Message.ReasoningItems`, `convertToResponsesInput` re-emits them with original IDs to maintain the reasoning chain across turns
- `Message.ReasoningItems` is populated only by the Responses API provider; other providers use `ReasoningContent` / `Reasoning` as plain strings

## Extension Points

- New provider: implement `Provider` interface, add to router factory
- Custom model metadata: override in config `models` section
- Custom reasoning options: add a family mapping to `FamilyReasoningOptions()` in `sdk/llm/reasoning.go`. When options depend on the model *version* (not just family), branch on the model in `ModelReasoningOptions()` (same file); the backend already feeds it the bare model name.

## Related Specs

- [orchestration/executor.md](orchestration/executor.md) — uses LLMCaller for ReAct loop
- [orchestration/router.md](orchestration/router.md) — uses LLMCaller for classification
- [contracts/core-sdk.md](../contracts/core-sdk.md) — LLM interfaces at boundary
