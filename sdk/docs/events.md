# Events

The SDK emits lifecycle events throughout agent execution so applications can observe progress in real time — streaming thoughts, tool calls, context-window pressure, and internal diagnostics. This document covers the `Events` interface (the executor-level contract), the `NoopEvents` embedding pattern, streaming, diagnostics, and the `orchestration.Events` extension used by the planner/conductor layer.

```go
import (
    "time"

    "github.com/v0lka/sp4rk/agent"
    "github.com/v0lka/sp4rk/orchestration"
)
```

## Events

`Events` defines universal agent lifecycle events. Any agent system can implement this interface — it is not tied to a specific orchestration strategy.

```go
type Events interface {
    StepStart(stepNum int)
    Thought(stepNum int, content, reasoning string)
    ToolCall(stepNum, callIdx int, toolName, argsPreview, source string)
    ToolResult(stepNum, callIdx, resultLen int, preview string, isError bool)
    StepComplete(stepNum int, duration time.Duration)
    SubAgentLaunch(stepID, description string)
    SubAgentComplete(stepID string, success bool, duration time.Duration)
    AssistantChunk(content string)
    AssistantDone(content string, inputTokens, outputTokens int)
    ContextFill(fillPercent float64, usedTokens, maxTokens int, status string, stepID string)
    ContextCompaction(beforePercent, afterPercent float64, stepID string)
    Finishing(stepNum int, summary string)
    ExecutorDiagnostic(stepNum int, event string, details map[string]any)
}
```

### Method reference

#### Step lifecycle

| Method | Description |
|--------|-------------|
| `StepStart(stepNum)` | Fired at the beginning of each ReAct iteration, before the LLM call. |
| `StepComplete(stepNum, duration)` | Fired when an iteration finishes (after tool execution and compaction). `duration` is the wall-clock time for the step. |

#### Reasoning

| Method | Description |
|--------|-------------|
| `Thought(stepNum, content, reasoning)` | Fired after the LLM responds. `content` is the model's textual response; `reasoning` is the separate chain-of-thought text from reasoning models (empty when the provider does not surface reasoning). |

#### Tool calls

| Method | Description |
|--------|-------------|
| `ToolCall(stepNum, callIdx, toolName, argsPreview, source)` | Fired before a tool executes. `callIdx` is the index of the call within the step (0-based). `argsPreview` is a truncated string preview of the arguments. `source` identifies where the tool came from (e.g. `"core"`, the MCP server name like `"filesystem"`). |
| `ToolResult(stepNum, callIdx, resultLen, preview, isError)` | Fired after a tool executes. `resultLen` is the full result length in characters; `preview` is a truncated preview. `isError` is `true` when the tool returned an error result. |

#### Sub-agent events

| Method | Description |
|--------|-------------|
| `SubAgentLaunch(stepID, description)` | Fired when a sub-agent starts running a plan step in a goroutine. `description` is the task description. |
| `SubAgentComplete(stepID, success, duration)` | Fired when a sub-agent finishes. `success` is `true` only if the executor finished without error. See [Subagents](subagents.md). |

#### Streaming

| Method | Description |
|--------|-------------|
| `AssistantChunk(content)` | Fired for each streamed text chunk from the LLM, enabling a live-typing effect. Only emitted when `suppressAssistantEvents` is `false`. |
| `AssistantDone(content, inputTokens, outputTokens)` | Fired when the full assistant response is finalized. `inputTokens`/`outputTokens` are the provider-reported token counts for the call. |

#### Context window

| Method | Description |
|--------|-------------|
| `ContextFill(fillPercent, usedTokens, maxTokens, status, stepID)` | Fired to report context-window fill. `status` is one of `ok`, `compact`, `warning`, `emergency`, `reject` (see `FillCheck`). `stepID` is empty for non-plan executions. |
| `ContextCompaction(beforePercent, afterPercent, stepID)` | Fired after a compaction runs, reporting the fill percentage before and after. |

#### Completion

| Method | Description |
|--------|-------------|
| `Finishing(stepNum, summary)` | Fired when the agent calls the `finish` tool. `summary` is the finish answer. |

#### Diagnostics

| Method | Description |
|--------|-------------|
| `ExecutorDiagnostic(stepNum, event, details)` | Fired for internal executor lifecycle events that are not part of the normal happy path: nudges, circuit-breaker triggers, truncation, compaction errors, and parse errors. `event` is a short identifier; `details` carries structured data. |

## NoopEvents and the embedding pattern

`NoopEvents` is a no-op implementation of `Events` — every method has an empty body. It satisfies the interface and serves as a base for the recommended **embed-and-override** pattern: embed `NoopEvents` in your own struct and override only the methods you care about. The embedded no-ops handle the rest, so you never need to implement the full interface.

```go
type NoopEvents struct{}

var _ Events = (*NoopEvents)(nil)
```

This is especially useful because `Events` is a large interface. Without embedding, adding a new method would break every implementation. With embedding, only implementations that care about the new method need updating.

```go
// PrintingEvents observes step lifecycle, tool calls, and streaming.
// All other events fall through to the embedded NoopEvents no-ops.
type PrintingEvents struct {
    agent.NoopEvents
}

func (e *PrintingEvents) StepStart(stepNum int) {
    fmt.Printf("\n┌─ Step %d ─────────────────────────────\n", stepNum)
}

func (e *PrintingEvents) StepComplete(stepNum int, duration time.Duration) {
    fmt.Printf("└─ Step %d complete (%v) ──────────────\n", stepNum, duration)
}

func (e *PrintingEvents) Thought(stepNum int, content, reasoning string) {
    fmt.Printf("│ 💭 Thought: %s\n", truncate(content, 120))
    if reasoning != "" {
        fmt.Printf("│    (reasoning: %s)\n", truncate(reasoning, 80))
    }
}
```

## Streaming

`AssistantChunk` and `AssistantDone` together enable a live-typing effect for assistant output. The executor emits `AssistantChunk` for each text chunk as it arrives from the provider, then `AssistantDone` once with the full content and token counts.

```go
func (e *PrintingEvents) AssistantChunk(content string) {
    // Print without a newline for a live-typing effect.
    fmt.Print(content)
}

func (e *PrintingEvents) AssistantDone(content string, inputTokens, outputTokens int) {
    fmt.Printf("\n│ 📝 Assistant done: %d input / %d output tokens\n", inputTokens, outputTokens)
}
```

> **Note:** streaming events are suppressed when the executor is created with `suppressAssistantEvents = true`. This is typically set for plan-step executors where the orchestration layer handles the final output, to avoid duplicate assistant messages.

## Context-window monitoring

`ContextFill` and `ContextCompaction` let applications track how full the context window is and react to compaction events — for example, to show a progress bar or warn the user that older tool outputs are being pruned.

```go
func (e *PrintingEvents) ContextFill(fillPercent float64, usedTokens, maxTokens int, status, stepID string) {
    fmt.Printf("│ 📊 Context: %.1f%% (%d/%d tokens) — %s\n", fillPercent, usedTokens, maxTokens, status)
}

func (e *PrintingEvents) ContextCompaction(beforePercent, afterPercent float64, stepID string) {
    fmt.Printf("│ ♻️  Compaction: %.1f%% → %.1f%%\n", beforePercent, afterPercent)
}
```

The `status` field mirrors `FillCheck.Status`: `ok`, `compact`, `warning`, `emergency`, or `reject`.

## Diagnostics

`ExecutorDiagnostic` surfaces internal lifecycle events that are not part of the normal execution flow. These are invaluable for debugging agent behavior and understanding why an execution stopped.

```go
func (e *PrintingEvents) ExecutorDiagnostic(stepNum int, event string, details map[string]any) {
    fmt.Printf("│ ⚠️  Diagnostic (step %d): %s %v\n", stepNum, event, details)
}
```

Typical `event` values include:

- **Nudges** — corrective system messages injected by circuit breakers (repeat, fruitless, same-tool-repeat, truncation, parse-error, wrap-up, finish).
- **Circuit-breaker triggers** — when an abort threshold is crossed.
- **Truncation** — when a tool result or LLM response was truncated.
- **Compaction errors** — when compaction failed.
- **Parse errors** — when tool input could not be parsed.

The `details` map carries structured, event-specific data (e.g. the tool name, the repeat count, the cache hash).

## Complete PrintingEvents example

The following is a complete, runnable event sink that prints every lifecycle event to stdout. It embeds `NoopEvents` and overrides each method.

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"
    "strings"
    "time"

    "github.com/v0lka/sp4rk"
    "github.com/v0lka/sp4rk/agent"
    "github.com/v0lka/sp4rk/llm"
    "github.com/v0lka/sp4rk/tools"
    "github.com/v0lka/sp4rk/tools/builtins"
)

// PrintingEvents implements agent.Events by embedding agent.NoopEvents
// (which provides no-op stubs for every method) and overriding the methods
// we want to observe. This is the recommended pattern: embed NoopEvents,
// override only what you need.
type PrintingEvents struct {
    agent.NoopEvents
}

// --- Step lifecycle ---

func (e *PrintingEvents) StepStart(stepNum int) {
    fmt.Printf("\n┌─ Step %d ─────────────────────────────\n", stepNum)
}

func (e *PrintingEvents) StepComplete(stepNum int, duration time.Duration) {
    fmt.Printf("└─ Step %d complete (%v) ──────────────\n", stepNum, duration)
}

// --- Reasoning ---

func (e *PrintingEvents) Thought(stepNum int, content, reasoning string) {
    fmt.Printf("│ 💭 Thought: %s\n", truncate(content, 120))
    if reasoning != "" {
        fmt.Printf("│    (reasoning: %s)\n", truncate(reasoning, 80))
    }
}

// --- Tool calls ---

func (e *PrintingEvents) ToolCall(stepNum, callIdx int, toolName, argsPreview, source string) {
    fmt.Printf("│ 🔧 ToolCall #%d: %s(%s) [source: %s]\n", callIdx, toolName, truncate(argsPreview, 80), source)
}

func (e *PrintingEvents) ToolResult(stepNum, callIdx, resultLen int, preview string, isError bool) {
    icon := "✅"
    if isError {
        icon = "❌"
    }
    fmt.Printf("│ %s Result #%d (%d chars): %s\n", icon, callIdx, resultLen, truncate(preview, 100))
}

// --- Assistant output ---

func (e *PrintingEvents) AssistantChunk(content string) {
    // Streaming chunks — print without newline for a live-typing effect
    fmt.Print(content)
}

func (e *PrintingEvents) AssistantDone(content string, inputTokens, outputTokens int) {
    fmt.Printf("\n│ 📝 Assistant done: %d input / %d output tokens\n", inputTokens, outputTokens)
}

// --- Context window ---

func (e *PrintingEvents) ContextFill(fillPercent float64, usedTokens, maxTokens int, status, stepID string) {
    fmt.Printf("│ 📊 Context: %.1f%% (%d/%d tokens) — %s\n", fillPercent, usedTokens, maxTokens, status)
}

func (e *PrintingEvents) ContextCompaction(beforePercent, afterPercent float64, stepID string) {
    fmt.Printf("│ ♻️  Compaction: %.1f%% → %.1f%%\n", beforePercent, afterPercent)
}

// --- Completion ---

func (e *PrintingEvents) Finishing(stepNum int, summary string) {
    fmt.Printf("│ 🏁 Finishing at step %d: %s\n", stepNum, truncate(summary, 100))
}

// --- Diagnostics ---

func (e *PrintingEvents) ExecutorDiagnostic(stepNum int, event string, details map[string]any) {
    fmt.Printf("│ ⚠️  Diagnostic (step %d): %s %v\n", stepNum, event, details)
}

// --- Sub-agent events ---

func (e *PrintingEvents) SubAgentLaunch(stepID, description string) {
    fmt.Printf("│ 🚀 SubAgent launched: %s — %s\n", stepID, truncate(description, 80))
}

func (e *PrintingEvents) SubAgentComplete(stepID string, success bool, duration time.Duration) {
    status := "succeeded"
    if !success {
        status = "failed"
    }
    fmt.Printf("│ 📥 SubAgent %s %s (%v)\n", stepID, status, duration)
}

// truncate shortens a string to maxLen characters, appending "…" if truncated.
func truncate(s string, maxLen int) string {
    s = strings.ReplaceAll(s, "\n", " ")
    if len(s) <= maxLen {
        return s
    }
    return s[:maxLen-1] + "…"
}

func run() error {
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
    if err != nil {
        return fmt.Errorf("failed to create framework: %w", err)
    }
    defer func() { _ = fw.Shutdown() }()

    registry := fw.ToolRegistry()
    registry.Register(builtins.NewReadFileTool())
    registry.Register(builtins.NewListDirectoryTool())
    registry.Register(builtins.NewGlobTool())
    registry.Register(agent.NewFinishTool())

    workspaceDir, _ := os.Getwd()
    ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)

    systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
        return "You are a code exploration assistant. " +
            "Use the available tools to investigate the codebase. " +
            "Call finish when you have your answer."
    }

    task := "List the Go files in the current directory using the glob tool, " +
        "then read the first one you find and summarize what it does in one sentence."

    // Pass our custom events implementation instead of NoopEvents.
    events := &PrintingEvents{}

    result, err := fw.Execute(ctx, systemPrompt, events, task)
    if err != nil {
        return fmt.Errorf("execution failed: %w", err)
    }

    fmt.Println("\nFinal Status:", result.Status)
    fmt.Println("Final Output:", result.Output)
    return nil
}

func main() {
    if err := run(); err != nil {
        log.Fatalf("%v", err)
    }
}
```

## orchestration.Events

The orchestration layer (planner → DAG → conductor → reflector) extends `Events` with additional hooks for plan-level lifecycle events. `orchestration.Events` embeds `agent.Events`, so any implementation must satisfy both interfaces.

```go
type Events interface {
    agent.Events
    OnPlanGenerated(stepCount int, steps []PlanStepEvent)
    OnStepStarted(stepID, description, summary string)
    OnStepCompleted(stepID string, success bool, duration time.Duration, errMsg string)
    OnReflected(reflection *Reflection, attempt, maxAttempts int)
    OnRetry(attempt, maxAttempts int)
    OnStepRetry(stepID string, attempt, maxAttempts int)
    OnService(content string)
    OnServiceMeta(content string, meta map[string]any)
    OnReplanFailed(err error)
    OnStepTodoUpdate(stepID string, items []agent.TodoItem)
}
```

| Method | Description |
|--------|-------------|
| `OnPlanGenerated(stepCount, steps)` | Fired when the planner produces a new DAG. `steps` is a slice of `PlanStepEvent` describing each step. |
| `OnStepStarted(stepID, description, summary)` | Fired when a plan step begins execution. |
| `OnStepCompleted(stepID, success, duration, errMsg)` | Fired when a plan step finishes. `errMsg` is non-empty on failure. |
| `OnReflected(reflection, attempt, maxAttempts)` | Fired after the reflector analyzes a failure and produces corrective insights. |
| `OnRetry(attempt, maxAttempts)` | Fired when the conductor retries a failed step. |
| `OnStepRetry(stepID, attempt, maxAttempts)` | Fired for a per-step retry, identifying which step is being retried. |
| `OnService(content)` | Fired for service messages (non-tool, non-assistant output shown to the user). |
| `OnServiceMeta(content, meta)` | Fired for service messages with structured metadata. |
| `OnReplanFailed(err)` | Fired when replanning after a failure itself fails. |
| `OnStepTodoUpdate(stepID, items)` | Fired when a step's checklist/to-do list is updated. |

### Implementing orchestration.Events

Because `orchestration.Events` embeds `agent.Events`, the embed-and-override pattern works here too — but you need a no-op base for the orchestration methods as well. A common approach is to embed both `agent.NoopEvents` and provide no-op stubs for the orchestration-specific methods, then override what you need:

```go
type consoleEvents struct {
    agent.NoopEvents
}

func (e *consoleEvents) OnPlanGenerated(n int, steps []orchestration.PlanStepEvent) {
    fmt.Printf("\n📋 Plan: %d steps\n", n)
    for _, s := range steps {
        fmt.Printf("   • %s: %s\n", s.ID, s.Summary)
    }
}

func (e *consoleEvents) OnStepStarted(id, desc, summary string) {
    fmt.Printf("\n▶ %s: %s\n", id, summary)
}

func (e *consoleEvents) OnStepCompleted(id string, ok bool, d time.Duration, errMsg string) {
    if ok {
        fmt.Printf("  ✅ %s done (%v)\n", id, d)
    } else {
        fmt.Printf("  ❌ %s failed (%v): %s\n", id, d, errMsg)
    }
}

func (e *consoleEvents) OnReflected(r *orchestration.Reflection, attempt, maxAttempts int) {
    fmt.Printf("  🔍 reflection (attempt %d/%d): %s → %s\n",
        attempt, maxAttempts, r.Summary, r.SuggestedAction)
}
```

> The `agent.NoopEvents` embedding covers all `Events` methods. The orchestration-specific methods (`OnPlanGenerated`, `OnStepStarted`, etc.) must be implemented or stubbed by your type to satisfy `orchestration.Events`.

## Optional interfaces: StepScopable and RetryScopable

Some event sinks want to scope events to a specific plan step or retry attempt. The orchestration layer checks for two optional interfaces and, if present, uses them to produce scoped event emitters.

```go
// StepScopable lets an Events implementation support scoping to a plan step.
type StepScopable interface {
    WithStepID(id string) Events
}

// RetryScopable lets an Events implementation tag events with a retry attempt.
type RetryScopable interface {
    WithRetryAttempt(attempt int) Events
}
```

- `WithStepID(id)` returns a new `Events` instance whose emissions are tagged with the given step ID. This is useful when a single sink handles multiple concurrent steps and needs to route events back to the right step.
- `WithRetryAttempt(attempt)` returns a new `Events` instance whose emissions are tagged with a retry attempt number, so retries can be distinguished from the original attempt.

These are **optional** — implementations that do not need scoping simply omit them. The orchestration layer falls back to the un-scoped emitter when the interfaces are not implemented.
