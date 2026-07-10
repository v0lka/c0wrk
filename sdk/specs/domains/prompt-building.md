# Prompt Building

## Purpose

A fluent API for constructing prompts with first-class support for splitting stable (cacheable) and dynamic content so providers can apply prompt caching. Also provides family-aware sampling defaults. Host `SystemPromptFactory` implementations use these builders to assemble the prompts passed to the [Conductor](orchestration/conductor.md).

## Key Files

- `github.com/v0lka/sp4rk/prompt` — `Builder`, `NewBuilder`, `Core`/`Replace`/`ReplaceData`/`CacheBreak`/`Build`/`BuildParts`
- `github.com/v0lka/sp4rk/prompt` — `SystemPromptBuilder`, `NewSystemPromptBuilder`, `Dynamic`, `CacheBreakMarker`, `SplitCacheBreak`
- `github.com/v0lka/sp4rk/prompt` — `SamplingConfig`, `DefaultSampling`
- `github.com/v0lka/sp4rk/memory` — `ContextWindow.BuildPrompt` (splits on `CacheBreakMarker`)

## Core Types

```go
const CacheBreakMarker = "\x00CACHE_BREAK\x00"

type SamplingConfig struct {
    Temperature *float64
    TopP        *float64
    MaxTokens   *int
}
// Pointer fields indicate "set" vs "unset": nil means no override.
```

## Flow

```
SystemPromptFactory(ctx, stepDescription, modelMeta)
  │
  ├─ NewSystemPromptBuilder()
  │     ├─ Core(stable sections)…          // cacheable prefix
  │     ├─ CacheBreak()                    // boundary marker
  │     └─ Dynamic(per-request sections)…  // not cached
  ├─ Replace(trusted placeholders) / ReplaceData(untrusted values)
  └─ Build() → single string with CacheBreakMarker embedded
        │
        ▼
ContextWindow.BuildPrompt()
  └─ SplitCacheBreak(systemPrompt) → one system message per non-empty part
        → providers cache the stable prefix across turns
```

## Placeholder substitution

Placeholders are resolved iteratively during `Build()`/`BuildParts()`. The substitution loop runs up to **5 passes** (`maxSubstitutionPasses`), repeating until the text stabilizes or the cap is reached. This handles nested placeholders — e.g. when one substitution value itself contains another placeholder key. The pass cap prevents infinite loops from circular placeholder references.

### Trusted vs untrusted values

- **`Replace` / `ReplaceAll`** — trusted placeholders; substituted iteratively (re-scans replaced content). Intended for static prompt fragments that may legitimately reference other placeholders.
- **`ReplaceData` / `ReplaceDataAll`** — untrusted values (user input, conversation history, tool output, LLM-generated reflections). Applied **after** all trusted substitutions, in a **single pass** (left-to-right via `strings.Replacer`) — a placeholder name occurring inside an untrusted value is left as literal text. This closes the placeholder-injection vector.

Rule of thumb: `Replace` for static prompt fragments; `ReplaceData` for everything derived from external input.

## CacheBreak

`CacheBreak()` marks the boundary between stable and dynamic content. Sections added **before** form the stable (cacheable) part; sections added **after** form the dynamic part. `Build()` inserts `CacheBreakMarker` between them (or returns just the stable part when the dynamic part is empty), so the marker survives into the final string. `BuildParts()` returns the prompt split at the boundary as `(stable, dynamic)`; if no `CacheBreak` was set, `stable` contains the full prompt and `dynamic` is empty.

### Provider-level prompt caching

When a system prompt contains a `CacheBreakMarker`, downstream consumers split it into multiple system messages. Providers that support prompt caching (e.g. Anthropic's ephemeral cache control) can then cache the stable prefix across turns while the dynamic suffix changes per request — reducing latency and token cost for long-running sessions.

## Sampling Defaults

`DefaultSampling(family)` returns `SamplingConfig` advisory defaults — providers use them only when no explicit overrides are set.

| Family | Default |
| ------ | ------- |
| `anthropic` | all nil (model self-selects) |
| `openai_flagship`, `openai_standard` | Temperature 0.3 |
| `google` | Temperature 1.0 (low values cause looping) |
| `mistral` | Temperature 0.3 |
| `deepseek` | Temperature 0.0 (coding/math; ignored when thinking enabled) |
| `qwen` | Temperature 0.6 (reasoning/analytical) |
| `glm` | Temperature 0.2 (analytical/coding) |
| `kimi` | all nil (server enforces per model) |
| default | Temperature 0.5, TopP 0.95 |

## Invariants

- Trusted substitutions run iteratively (up to 5 passes); untrusted substitutions run once and never expand placeholders inside the value.
- `Build()` with a `CacheBreak` always embeds `CacheBreakMarker` between stable and dynamic parts.
- `SplitCacheBreak` omits empty parts (returns one part when no marker, two when present).
- `DefaultSampling` returns `nil` fields (no override) for families with provider-managed defaults.

## Configuration

There is no runtime configuration file for this package. Behavior is driven entirely by the builder API: which sections are `Core` (stable) vs `Dynamic`, where `CacheBreak()` is placed, and which substitutions are trusted (`Replace`) vs untrusted (`ReplaceData`). `DefaultSampling` is keyed by the model family resolved in the [llm](llm-providers.md) package.

## Extension Points

- **Custom `SystemPromptFactory`**: compose a `SystemPromptBuilder`, place `CacheBreak()` between stable and dynamic sections, and return `Build()`. The [Conductor](orchestration/conductor.md) receives model metadata so the prompt can adapt.
- **Custom sampling**: supply a `SamplingFunc` to `RouterConfig` (see [llm-providers.md](llm-providers.md)) overriding `DefaultSampling` per family.
- **Cache-aware ContextManager**: a custom `ContextManager` should split its system prompt on `CacheBreakMarker` (via `SplitCacheBreak`) into multiple system messages to benefit from provider caching.

## Related Specs

- [llm-providers.md](llm-providers.md) — `SamplingFunc` and family detection
- [../memory/README.md](memory/README.md) — `ContextWindow.BuildPrompt` splits on the marker
- [../orchestration/conductor.md](orchestration/conductor.md) — `SystemPromptFactory` consumers
