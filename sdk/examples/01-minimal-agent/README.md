# Example 01 — Minimal Agent

The smallest possible **full** agent: one LLM provider, the finish tool, and a single `Execute` call. No custom tools, no event handling, no orchestration.

This example ships in **two equivalent variants**:

| Variant     | File            | Command                 | When to read                                   |
|-------------|-----------------|-------------------------|------------------------------------------------|
| **Fluent**  | `main_fluent.go`| `go run -tags fluent .` | Recommended entry point — concise & declarative |
| **Classic** | `main.go`       | `go run .`              | Full low-level control via `sp4rk.Config`        |

Both build the identical `*sp4rk.Framework` and produce the same result. The fluent builders live in the root `sp4rk` package and return the original SDK types — see [`docs/fluent-api.md`](../../docs/fluent-api.md).

## What you will learn

- How to create a `sp4rk.Framework` (via `sp4rk.NewF` or classic `sp4rk.New`)
- Why the `finish` tool must be registered
- How to define a system prompt factory
- How to call `Framework.Execute` and read the result

### Fluent (recommended)

```go
fw, err := sp4rk.NewF().
    Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
    Build()
defer fw.Shutdown() // finish tool is auto-registered

result, err := fw.RunF(ctx).
    System("You are a helpful assistant.").
    Ask("What is the capital of France?")
```

`sp4rk.NewF()` returns a real `*sp4rk.Framework` (no shadow types), and the finish tool is registered by convention. `fw.RunF(ctx)` delegates to `Framework.Execute` and returns the original `*orchestration.ExecutionResult`.

## Architecture

```
User message
    │
    ▼
Framework.Execute()
    │  creates a Conductor (single ReAct loop)
    ▼
Executor.Run()
    │  iterates: Thought → Action → Observation
    │  until the agent calls "finish" or the step budget is exhausted
    ▼
ExecutionResult { Output, Status }
```

The agent has exactly one tool — `finish`. It receives the user message, thinks about the answer, and calls `finish` with its response. This is the minimum viable agent: the ReAct loop with a completion signal.

## Code walkthrough

### 1. Framework creation

```go
fw, err := sp4rk.New(sp4rk.Config{
    LLM: sp4rk.LLMConfig{
        Providers: []llm.ProviderEntry{{
            Name:         "anthropic",
            ProviderType: "anthropic",
            APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
            Models:       []string{"claude-sonnet-4-5"},
        }},
    },
})
```

`sp4rk.Config.LLM.Providers` is a slice — you can register multiple providers (see example 07). Each `ProviderEntry` has a logical `Name`, a `ProviderType` (`"anthropic"` or `"openai"`), an `APIKey`, and a list of `Models`.

### 2. The finish tool

```go
fw.ToolRegistry().Register(agent.NewFinishTool())
```

The `finish` tool is the agent's way of saying "I'm done." The executor handles `finish` inline — it does not call `ToolRegistry.Execute` for it — but the tool **must** be in the registry so its descriptor is sent to the LLM as an available tool. Without it, the LLM has no way to signal completion.

### 3. System prompt factory

```go
systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
    return "You are a helpful assistant. …"
}
```

The factory is `orchestration.SystemPromptFactory`:

```go
func(ctx context.Context, stepDescription string, modelMeta llm.ModelMetadata) string
```

- `stepDescription` — the task the agent is working on
- `modelMeta` — the active model's capabilities (context window, family, …)

For a minimal agent, a static string is fine. Later examples use `prompt.NewSystemPromptBuilder()` for structured prompts with cache-break support.

### 4. Execute

```go
result, err := fw.Execute(ctx, systemPrompt, &agent.NoopEvents{}, "What is the capital of France?")
```

`Execute` is a convenience method that creates a `Conductor`, runs one ReAct loop, and returns an `*orchestration.ExecutionResult`. For repeated calls, use `fw.NewConductor()` once and call `conductor.Run()` multiple times.

The third argument (`&agent.NoopEvents{}`) is the event sink. `NoopEvents` discards all lifecycle events — see **example 03** for a custom implementation that prints a live trace.

### 5. Result

```go
fmt.Println("Status:", result.Status) // "success" | "partial" | "failed"
fmt.Println("Output:", result.Output)
```

`ExecutionStatus` values:

| Status      | Meaning                                              |
|-------------|------------------------------------------------------|
| `success`   | The agent called `finish` — task complete            |
| `partial`   | Step budget exhausted before `finish` was called     |
| `failed`    | An error occurred during execution                   |
| `aborted`   | The reflector recommended aborting after step failures |
| `cancelled` | The context was cancelled mid-execution              |

## Prerequisites

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

## Run

### Fluent (recommended)

```bash
cd sdk/examples
go mod tidy          # first time only
cd 01-minimal-agent
go run -tags fluent .
```

### Classic API (advanced control)

```bash
cd sdk/examples/01-minimal-agent
go run .
```

## Expected output

```
Status: success
Output: The capital of France is Paris.
```

## Next

→ **02-custom-tools** — add a custom tool and built-in file operations.
