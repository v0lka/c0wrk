# Contract: LLM Providers & Routing

> This contract documents the public LLM interface an embedding application implements and consumes for multi-provider inference. It is the boundary between the generic provider/routing layer (`github.com/v0lka/sp4rk/llm`) and the host application (and the agent executor) that drives LLM calls.

## Boundary Rule

The host application consumes the LLM types from `github.com/v0lka/sp4rk/llm` directly. The `llm` package depends only on third-party SDK clients (`go-anthropic`, `openai-go`, `tiktoken-go`) and on sibling sp4rk packages where needed; it never imports host-application code. An embedder plugs in inference by **providing** a `Provider` (or using the built-in OpenAI/Anthropic providers), composing them through a `Router`, and resolving model metadata via a `ModelRegistry`. The agent executor consumes LLM capability exclusively through `LLMCaller`, which `Router` satisfies.

## Interfaces

| Interface / Type | Package | Implemented / Consumed By | Purpose |
| --- | --- | --- | --- |
| `Provider` | llm | Implemented by built-ins / host | Unified provider interface: `ChatCompletion(ctx, ChatRequest)(*ChatResponse, error)` and `Name() string` |
| `Caller` | llm | Minimal alias | Single-call interface (`Call`); higher layers may define compatible interfaces without importing `llm` |
| `Router` | llm | Constructed by host | Routes calls to the active provider; retry/backoff, context-window validation, family-aware sampling; satisfies `LLMCaller` |
| `RouterConfig` | llm | Built by host | Pre-resolved router config: providers, retries, backoffs, safety margin, HTTP client, sampling func, logger |
| `ProviderEntry` | llm | Built by host | One enabled provider: logical name, type, expanded API key/base URL, enabled models |
| `ModelRegistry` | llm | Constructed by host | 5-tier model metadata resolution (overrides → built-in → HuggingFace → registered sources → fallback) |
| `ModelMetadata` | llm | Consumed by host/executor | Context window, output limit, tokenizer type, family, capabilities |
| `ModelCapabilities` | llm | Consumed by host | `Attachment`, `Reasoning`, `Temperature`, `ToolCall` flags |
| `Message` | llm | Consumed by host | LLM message unit (role, content, reasoning, tool calls, reasoning items) |
| `ChatRequest` | llm | Consumed by host/executor | Request: model, family hint, messages, tools, max tokens, temperature, reasoning effort |
| `ChatResponse` | llm | Consumed by host/executor | Response: model, family, message, reasoning, `TokenUsage`, stop reason |
| `TokenCounter` | llm | Implemented by built-ins / host | Token counting for context budget: `Count(string)int`, `CountMessages([]Message)int` |
| `TokenUsage` | llm | Consumed by host | Input/output token consumption from a response |
| `ReasoningEffort` | llm | Consumed by host | Native reasoning level (plain `string`, not a custom type) |

## Initialization

At startup the host assembles the LLM surface in this order:

1. Resolve provider configuration (expand environment variables, parse durations) into a `RouterConfig` whose `Providers` slice contains at least one `ProviderEntry`.
2. Optionally build a `ModelRegistry` with user `overrides` and register any `ModelMetadataSource` functions (e.g. a local-model server).
3. Call `NewRouter(ctx, cfg, registry)` to construct the `Router`. The router creates the underlying `Provider` instances for each entry, builds a `model → provider` reverse index, and selects the first provider's first model as active.
4. Build a `TokenCounter` via `NewTokenCounter(tokenizerType)` and (if hosting the router's metadata/validation) attach it; the registry's context-window validation relies on a counter.
5. The host passes the `Router` to the agent executor as its `LLMCaller`. The active model can be switched later via `SetModel`.

## Data Flow Across Boundary

- **Host → Router (in):** `RouterConfig` (providers, HTTP client, retry/backoff, sampling) and a `*ModelRegistry`.
- **Host → Router (runtime):** `SetModel(ctx, compositeModelID)` selects the active provider+model; `ActiveModel()` returns the composite ID (`"provider/model"`).
- **executor → Router:** `Call(ctx, ChatRequest)` — messages, tools, max tokens, temperature, reasoning effort.
- **Router → Provider:** an internally-built provider request derived from the `ChatRequest` (provider-specific mapping).
- **Provider → Router:** `*ChatResponse` (message, reasoning, usage, stop reason).
- **Router → executor:** `*ChatResponse`, or a retryable-error path that re-attempts with backoff.
- **ModelRegistry → Router/host:** `Resolve(ctx, model)` returns `ModelMetadata` + an `ok` flag indicating a known source (vs. fallback defaults).
- **TokenCounter → Router/host:** message token counts used for pre-submission context-window validation and ongoing fill tracking.

Data is plain Go values. The composite model-ID convention (`"provider/model"`) disambiguates models that share a bare name across multiple providers; `BareModel(ActiveModel())` yields the bare name sent to the LLM API.

## Error Propagation

- **Transient errors** (HTTP 429, 502, 503, 529, network blips) are retried inside `Router.Call` with exponential backoff (1s → 2s → 4s, capped at `MaxBackoff`, ±20% jitter) before the error reaches the caller. `MaxRetries` defaults to 3 when unset; a negative value disables retries.
- **Non-retryable errors** (auth, 4xx other than 429, malformed response) propagate out of `Call` to the executor.
- **Context-window overflow** is detected pre-submission: `Router.validateContextWindow` rejects requests whose estimated token count exceeds the model's effective window (context window minus output reserve minus safety margin) by returning a context-window error before the provider is called.
- **Model switching errors** from `SetModel` (unknown composite ID, ambiguous bare name) propagate to the host. When a bare model name is ambiguous across providers, the router logs a warning (if a logger is configured) and selects deterministically.
- **Metadata fallback** is **not** an error: `Resolve` returns usable fallback `ModelMetadata` with `ok=false` when no known source matches; callers treat this as "use defaults, not found" rather than a failure.
- **Token-counter errors**: `NewTokenCounter` always returns a valid (never nil) counter, falling back to `SimpleTokenCounter` and returning an `error` only to signal that a fallback was used.

## Breaking Change Checklist

- If you change the `Provider` interface, you MUST update the built-in OpenAI and Anthropic providers and any host-supplied provider.
- If you change `Router.Call`, `SetModel`, or `ActiveModel` signatures, you MUST verify the router still satisfies `agent.LLMCaller` and update every host call site.
- If you change `ChatRequest`/`ChatResponse`/`Message`, you MUST update the agent executor (prompt building, response parsing), every provider mapping, and serialization.
- If you change the composite model-ID convention, you MUST update `SetModel` callers, `ActiveModel`/`BareModel` consumers, and persisted model selectors.
- If you change `ModelMetadata`/`ModelCapabilities`, you MUST update metadata sources, context-window validation, prompt/parameter adaptation, and any host capability gating.
- If you change the 5-tier `ModelRegistry.Resolve` ordering, you MUST document the new precedence and update host overrides expectations.
- If you change `TokenCounter`, you MUST update `SimpleTokenCounter`, `TiktokenCounter`, `NewTokenCounter`, and every counter consumer (router validation, context manager fill tracking).
- If you alter retry/backoff defaults or the retryable-status-code set, you MUST document the latency impact on callers that rely on error propagation for compaction timing, circuit-breaker resets, or budget control.
