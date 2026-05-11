# LLM Providers

## Purpose

Abstracts multiple LLM providers behind a unified interface, with model routing, token counting, reasoning effort control, and usage tracking.

## Key Files

- `sdk/llm/provider.go` — Provider interface
- `sdk/llm/router.go` — Router (active provider selection, retries)
- `sdk/llm/modelregistry.go` — ModelRegistry (model metadata)
- `sdk/llm/family.go` — model family classification
- `sdk/llm/reasoning.go` — ReasoningEffort resolution
- `sdk/llm/tokencount.go` — token counting
- `sdk/llm/usage.go` — TrackingCaller (usage tracking wrapper)
- `sdk/llm/errors.go` — typed provider errors (rate limit, context exceeded, etc.)
- `sdk/llm/message.go` — ChatMessage / ChatRequest / ChatResponse types
- `sdk/llm/schema_sanitize.go` — JSON Schema sanitization for function calling
- `sdk/llm/provider_helpers.go` — shared provider helpers (retry, streaming)
- `sdk/llm/provider_openai.go` — OpenAI provider (ChatGPT / OpenAI-compatible Chat Completions transport)
- `sdk/llm/provider_openai_responses.go` — OpenAI Responses API transport (for reasoning-capable models)
- `sdk/llm/provider_anthropic.go` — Anthropic provider
- `sdk/llm/provider_gemini.go` — Google Gemini provider
- `sdk/llm/provider_lmstudio.go` — LM Studio provider

## Core Types

```go
// Provider interface
type Provider interface {
    ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    StreamChatCompletion(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error)
    Name() string
}

// Router selects and routes to active provider
type Router struct {
    activeProvider Provider
    // retry config, backoff settings
}

// ModelMetadata from registry
type ModelMetadata struct {
    ContextWindow int    // total token capacity
    OutputLimit   int    // max output tokens
    TokenizerType string // e.g., "cl100k_base"
    Family        string // e.g., "openai", "anthropic"
    Capabilities  ModelCapabilities
}

// Reasoning effort levels
type ReasoningEffort string
const (
    ReasoningOff     ReasoningEffort = "off"
    ReasoningMinimal ReasoningEffort = "minimal"
    ReasoningLow     ReasoningEffort = "low"
    ReasoningMedium  ReasoningEffort = "medium"
    ReasoningHigh    ReasoningEffort = "high"
    ReasoningMaximum ReasoningEffort = "maximum"
)
```

## Flow

```
core.Orchestrator needs LLM call
  → uses LLMCaller interface (which is llm.Router or TrackingCaller)
      │
      ▼
llm.Router.Call(ctx, ChatRequest)
  ├─ Select active provider
  ├─ Add reasoning effort to request
  ├─ Call provider.ChatCompletion() or .StreamChatCompletion()
  ├─ On rate limit: exponential backoff + retry
  ├─ On context window exceeded: return specific error
  └─ Return ChatResponse
```

## Provider Implementations

| Provider (`llm.active_provider`) | Transport file(s)                                 | Special Features                                  |
| -------------------------------- | ------------------------------------------------- | ------------------------------------------------- |
| `anthropic`                      | provider_anthropic.go                             | Prompt caching (CacheBreak + ephemeral), thinking |
| `gemini`                         | provider_gemini.go                                | Safety settings, large context                    |
| `lmstudio`                       | provider_lmstudio.go                              | Any local model, OpenAI-compatible API            |
| `openai_compatible`              | provider_openai.go + provider_openai_responses.go | Generic OpenAI-compatible API endpoint            |
| `chatgpt`                        | provider_openai.go                                | Simplified OpenAI mode (Chat Completions only)    |

## Reasoning Effort

Controls extended thinking (reasoning tokens) for supported models:

```go
// Base effort from config
config.Reasoning.BaseEffort = "high"

// Per-role overrides
config.Reasoning.RoleOverrides = {
    "researcher": "high",
    "router": "low",
}

// Resolution at call time
ResolveAgentReasoningMode(role, baseEffort, overrides) → effective effort
```

## Model Registry

Maps model names to metadata (context window, output limit, tokenizer type, capabilities):

- Built-in registry with common models
- User can override via config (`models` section)
- `Resolve(ctx, modelName)` returns ModelMetadata

## Token Counting

- `TokenCounter` interface: `CountTokens(text) int`
- Implementation uses tiktoken-go
- `ContextTokenTracker`: tracks cumulative usage per-step, corrects predictive estimates with API-reported counts

## TrackingCaller

Wraps LLMCaller to track token usage:

- Counts input/output tokens per call
- Emits `session_tokens` events
- Provides `WithContextTracker()` for per-step accuracy

## Configuration

From `config.yaml` (yaml keys use `snake_case` throughout this section):

```yaml
llm:
  # One of: "anthropic" | "gemini" | "lmstudio" | "openai_compatible" | "chatgpt"
  active_provider: "anthropic"

  anthropic:
    api_key: "${ANTHROPIC_API_KEY}"
    model: "claude-sonnet-4-20250514"

  gemini:
    api_key: "${GEMINI_API_KEY}"
    model: ""

  lmstudio:
    api_key: ""
    base_url: "http://localhost:1234" # default; no "/v1" suffix
    model: ""

  openai_compatible:
    api_key: "${OPENAI_API_KEY}"
    base_url: "" # user-supplied endpoint
    model: ""

  chatgpt:
    api_key: "${OPENAI_API_KEY}"
    model: ""

reasoning:
  base_effort: "high" # default when model supports reasoning
  role_overrides:
    researcher: "high"
    router: "low"
```

Active-provider validation: any value not in `ValidProviders` (`backend/config/config.go`) is rejected at load time. There is no top-level `llm.model` — the model is always nested under the selected provider.

## Invariants

- Exactly one provider is active at any time
- Rate limit errors trigger retry with backoff (not immediate failure)
- Context window exceeded errors are returned to caller (not retried)
- Token counting is best-effort (predictive until corrected by API response)
- Provider initialization failure prevents Build() from completing

## Extension Points

- New provider: implement `Provider` interface, add to router factory
- Custom model metadata: override in config `models` section
- Custom reasoning resolution: modify `ResolveAgentReasoningMode`

## Related Specs

- [orchestration/executor.md](orchestration/executor.md) — uses LLMCaller for ReAct loop
- [orchestration/router.md](orchestration/router.md) — uses LLMCaller for classification
- [contracts/core-sdk.md](../contracts/core-sdk.md) — LLM interfaces at boundary
