# Prompt Building

The `prompt` package provides a fluent API for constructing prompts, with
first-class support for splitting stable (cacheable) and dynamic content so
that providers can apply prompt caching.

```go
import "github.com/v0lka/c0wrk/sdk/prompt"
```

## Builder

`Builder` is a fluent API for constructing prompts from named sections with
placeholder substitution and an optional cache-break boundary.

### API

| Method | Description |
| --- | --- |
| `NewBuilder() *Builder` | Creates a new prompt builder. |
| `Core(content string) *Builder` | Adds a section that is always included in the final prompt. |
| `Replace(placeholder, value string) *Builder` | Registers a single placeholder substitution applied during `Build()`. |
| `ReplaceAll(substitutions map[string]string) *Builder` | Registers multiple placeholder substitutions. |
| `CacheBreak() *Builder` | Marks the current position as the boundary between stable (cacheable) and dynamic content. |
| `Build() string` | Assembles the final prompt string. When `CacheBreak()` was called, a `CacheBreakMarker` is inserted between the stable and dynamic parts. |
| `BuildParts() (stable, dynamic string)` | Returns the prompt split at the cache-break boundary. If no `CacheBreak` was set, `stable` contains the full prompt and `dynamic` is empty. |

### Placeholder substitution

Placeholders are resolved iteratively during `Build()` and `BuildParts()`. The
substitution loop runs up to **5 passes** (`maxSubstitutionPasses`), repeating
until the text stabilizes or the pass cap is reached. This handles nested
placeholders — for example, when one substitution value itself contains another
placeholder key (e.g. `MODE-PREAMBLE` contains `RECENT-CONVERSATION`).

```go
b := prompt.NewBuilder().
    Core("You are a coding agent.").
    Core("Available tools:\nAVAILABLE-TOOLS").
    Core("MODE-PREAMBLE").
    Replace("AVAILABLE-TOOLS", "read_file, write_file, finish").
    Replace("MODE-PREAMBLE", "Work in MODE. RECENT-CONVERSATION").
    Replace("RECENT-CONVERSATION", "(none yet)").
    Replace("MODE", "code")

result := b.Build()
// Placeholders are resolved across multiple passes, so the final string
// contains no leftover placeholder tokens.
```

The pass cap prevents infinite loops from circular placeholder references.

## CacheBreakMarker

`CacheBreakMarker` is a sentinel string used to split a system prompt into
cacheable (stable) and dynamic parts.

```go
const CacheBreakMarker = "\x00CACHE_BREAK\x00"
```

Consumers such as `ContextWindow` check for this marker and emit **multiple
system messages** — one for each part — enabling provider-level prompt caching
(e.g. Anthropic's ephemeral cache control). The stable prefix can be cached
across turns while the dynamic suffix changes per request.

## SplitCacheBreak

`SplitCacheBreak` splits a system prompt on `CacheBreakMarker` and returns the
non-empty parts (trimmed). It returns one part when no marker is present, two
parts when the marker is present, and omits any empty parts.

```go
func SplitCacheBreak(systemPrompt string) []string
```

```go
parts := prompt.SplitCacheBreak(stable + prompt.CacheBreakMarker + dynamic)
// parts[0] == stable
// parts[1] == dynamic
```

This is the function `ContextWindow.BuildPrompt` uses to turn a single system
prompt string into multiple cacheable system messages.

## CacheBreak

`CacheBreak()` marks the boundary between stable and dynamic prompt content.
Sections added **before** the call form the stable part; sections added
**after** form the dynamic part.

```go
b := prompt.NewBuilder().
    Core("You are a coding agent. (stable, cacheable)").
    CacheBreak().
    Core("Current task: refactor auth. (dynamic, per-request)")

stable, dynamic := b.BuildParts()
// stable  == "You are a coding agent. (stable, cacheable)"
// dynamic == "Current task: refactor auth. (dynamic, per-request)"
```

`Build()` inserts the `CacheBreakMarker` between the two parts (or returns just
the stable part when the dynamic part is empty), so the marker survives into
the final string and can be split downstream by `SplitCacheBreak`.

## BuildParts

`BuildParts()` returns the prompt split at the cache-break boundary as
`(stable, dynamic string)`. Substitutions are applied to each part
independently. If no `CacheBreak` was set, `stable` contains the full prompt
and `dynamic` is empty.

## SystemPromptBuilder

`SystemPromptBuilder` wraps `Builder` for constructing system prompts with
cache-break support. It is intended for use by orchestrator
`SystemPromptFactory` implementations to build stable (cacheable) + dynamic
prompt parts.

### API

| Method | Description |
| --- | --- |
| `NewSystemPromptBuilder() *SystemPromptBuilder` | Creates a new system prompt builder. |
| `Core(content string) *SystemPromptBuilder` | Adds a section that is always included in the system prompt. |
| `Replace(placeholder, value string) *SystemPromptBuilder` | Registers a placeholder substitution. |
| `Dynamic(content string) *SystemPromptBuilder` | Adds a section that appears after the cache-break boundary (not cached by providers). Must be called after `CacheBreak()`. |
| `CacheBreak() *SystemPromptBuilder` | Marks the boundary between stable and dynamic content. |
| `Build() string` | Returns the full system prompt string with `CacheBreakMarker` between stable and dynamic parts (when `CacheBreak` was called). |

`Dynamic` is a semantic alias: it delegates to `Core` but documents intent —
the content belongs after the cache break and is therefore not cached.

## Provider-Level Prompt Caching

When a system prompt contains a `CacheBreakMarker`, downstream consumers split
it into multiple system messages. Providers that support prompt caching (such
as Anthropic's ephemeral cache control) can then cache the stable prefix across
turns while the dynamic suffix changes per request. This reduces latency and
token costs for long-running agent sessions where the system prompt is large
but mostly stable.

The flow is:

1. Build the system prompt with `SystemPromptBuilder`, calling `CacheBreak()`
   between the stable and dynamic sections.
2. `Build()` returns a single string with the `CacheBreakMarker` embedded.
3. `ContextWindow.BuildPrompt()` calls `SplitCacheBreak` on that string and
   emits one system message per non-empty part.

## Complete Example

```go
package main

import (
	"fmt"

	"github.com/v0lka/c0wrk/sdk/prompt"
)

func main() {
	// Build a system prompt with a stable (cacheable) prefix and a dynamic
	// suffix that changes per request.
spb := prompt.NewSystemPromptBuilder().
		// Stable, cacheable sections:
		Core("You are a task execution agent.").
		Core("Available tools:\nAVAILABLE-TOOLS").
		Replace("AVAILABLE-TOOLS", "read_file, write_file, edit_file, finish").
		// Mark the cache boundary:
		CacheBreak().
		// Dynamic, per-request sections (not cached):
		Dynamic("Current workspace: WORKSPACE").
		Dynamic("Active skills: ACTIVE-SKILLS").
		Replace("WORKSPACE", "/home/user/project").
		Replace("ACTIVE-SKILLS", "go-testing")

	systemPrompt := spb.Build()

	// Downstream, ContextWindow splits on the marker into multiple system
	// messages so the stable prefix can be cached by the provider.
	parts := prompt.SplitCacheBreak(systemPrompt)
	for i, p := range parts {
		fmt.Printf("system message %d:\n%s\n\n", i+1, p)
	}

	// You can also get the parts directly from a Builder:
	b := prompt.NewBuilder().
		Core("Stable instructions.").
		CacheBreak().
		Core("Dynamic instructions.")
	stable, dynamic := b.BuildParts()
	fmt.Println("stable:", stable)
	fmt.Println("dynamic:", dynamic)
}
```

## Sampling Defaults

The package also provides `SamplingConfig` and `DefaultSampling` for
family-aware generation parameter defaults. These are advisory — providers
should use them only when no explicit user overrides are set.

```go
type SamplingConfig struct {
    Temperature *float64
    TopP        *float64
    MaxTokens   *int
}
```

Pointer fields indicate "set" vs "unset": `nil` means no override.

```go
cfg := prompt.DefaultSampling("anthropic") // all nil — let the model self-select
cfg = prompt.DefaultSampling("google")      // Temperature: 1.0
cfg = prompt.DefaultSampling("deepseek")    // Temperature: 0.0 (coding/math)
cfg = prompt.DefaultSampling("qwen")        // Temperature: 0.6 (reasoning)
```

| Family | Default |
| --- | --- |
| `anthropic` | all nil (model self-selects) |
| `openai_flagship`, `openai_standard` | Temperature 0.3 |
| `google` | Temperature 1.0 (low values cause looping) |
| `mistral` | Temperature 0.3 |
| `deepseek` | Temperature 0.0 (coding/math; ignored when thinking enabled) |
| `qwen` | Temperature 0.6 (reasoning/analytical) |
| `glm` | Temperature 0.2 (analytical/coding) |
| `kimi` | all nil (server enforces per model) |
| default | Temperature 0.5, TopP 0.95 |
