# Example 03 — Event Streaming

Observe the agent's execution in real time by implementing the `agent.Events` interface. A custom `PrintingEvents` sink formats each lifecycle event — thoughts, tool calls, results, context fill — and prints it to stdout.

| Variant     | File            | Command                 | When to read                              |
|-------------|-----------------|-------------------------|-------------------------------------------|
| **Fluent**  | `main_fluent.go`| `go run -tags fluent .` | Recommended — `.Events(sink)` on Run builder |
| **Classic** | `main.go`       | `go run .`              | Pass events manually to `Framework.Execute` |

> `PrintingEvents` lives in `events.go` (tagless) so both variants share it.

### Fluent (recommended)

Attach a custom event sink to the Run builder; everything else is the same as example 01:

```go
result, err := fw.RunF(ctx).
    Events(&PrintingEvents{}).   // live trace instead of NoopEvents
    System("You are a code exploration assistant.").
    Ask(task)
```

## What you will learn

- The `Events` interface and its 13 methods
- How to embed `NoopEvents` and override only the methods you need
- What events fire during a ReAct loop and in what order
- How to track token usage and context-window fill

## Architecture

```
Executor.Run()
    │
    ├─ StepStart(1)
    ├─ Thought(1, "I should list Go files…")
    ├─ ToolCall(1, 0, "glob", "**/*.go", "core")
    ├─ ToolResult(1, 0, 142, "main.go\ncalculator.go…", false)
    ├─ ContextFill(12.3%, 15744/128000, "ok")
    ├─ StepComplete(1, 1.2s)
    │
    ├─ StepStart(2)
    ├─ Thought(2, "Now I'll read the first file…")
    ├─ ToolCall(2, 0, "read_file", …)
    ├─ ToolResult(2, 0, 890, "package main…", false)
    ├─ StepComplete(2, 0.8s)
    │
    ├─ Finishing(3, "The file implements…")
    └─ AssistantDone("The file…", 18200, 45)
```

## Code walkthrough

### 1. The Events interface

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

### 2. Embed NoopEvents

Implementing all 13 methods is tedious. The SDK provides `agent.NoopEvents` with no-op stubs for every method. Embed it and override only what you need:

```go
type PrintingEvents struct {
    agent.NoopEvents  // provides no-op stubs for all 13 methods
}

func (e *PrintingEvents) StepStart(stepNum int) {
    fmt.Printf("┌─ Step %d ─────────────\n", stepNum)
}
// override only the methods you care about…
```

This is the **recommended pattern** for custom event sinks. It also future-proofs your code: if new methods are added to `Events`, the embedded `NoopEvents` provides a default.

### 3. Event reference

| Event              | When it fires                              | Key fields                         |
|--------------------|--------------------------------------------|------------------------------------|
| `StepStart`        | Beginning of each ReAct iteration          | `stepNum`                          |
| `Thought`          | After the LLM responds with reasoning      | `content`, `reasoning`             |
| `ToolCall`         | Before a tool is executed                  | `toolName`, `argsPreview`, `source`|
| `ToolResult`       | After a tool returns                       | `resultLen`, `preview`, `isError`  |
| `StepComplete`     | End of a ReAct iteration                   | `duration`                         |
| `AssistantChunk`   | Streaming text chunk from the LLM          | `content`                          |
| `AssistantDone`    | Final assistant text + token usage         | `inputTokens`, `outputTokens`      |
| `ContextFill`      | After context-window fill check            | `fillPercent`, `status`            |
| `ContextCompaction`| After context compaction runs              | `beforePercent`, `afterPercent`    |
| `Finishing`        | When the agent calls `finish`              | `summary`                          |
| `ExecutorDiagnostic`| Internal nudges, circuit breakers         | `event`, `details`                 |
| `SubAgentLaunch`   | A delegated sub-agent starts               | `stepID`, `description`            |
| `SubAgentComplete` | A delegated sub-agent finishes             | `success`, `duration`              |

### 4. Context fill status

The `status` field in `ContextFill` can be:

| Status    | Meaning                                          |
|-----------|--------------------------------------------------|
| `ok`      | Context window has plenty of room                |
| `compact` | Predictive compaction triggered                  |
| `warning` | Warning-level compaction triggered               |
| `emergency`| Emergency compaction triggered                  |
| `reject`  | Context window exhausted — request rejected      |

### 5. Tool source

`ToolCall` includes a `source` field that identifies where the tool came from: `"core"` for built-in tools, the MCP server name (e.g. `"filesystem"`) for MCP-sourced tools (see example 05).

## Prerequisites

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

## Run

### Fluent (recommended)

```bash
cd sdk/examples/03-event-streaming
go run -tags fluent .
```

### Classic API (advanced control)

```bash
cd sdk/examples/03-event-streaming
go run .
```

## Expected output

```
═══════════════════════════════════════════════════════════
  Task: List the Go files in the current directory…
═══════════════════════════════════════════════════════════

┌─ Step 1 ─────────────────────────────
│ 💭 Thought: I'll use the glob tool to find Go files…
│ 🔧 ToolCall #0: glob({"pattern":"**/*.go"}) [source: core]
│ ✅ Result #0 (142 chars): main.go…
│ 📊 Context: 12.3% (15744/128000 tokens) — ok
└─ Step 1 complete (1.2s) ──────────────

┌─ Step 2 ─────────────────────────────
│ 💭 Thought: Now I'll read the first file…
│ 🔧 ToolCall #0: read_file({"path":"main.go"}) [source: core]
│ ✅ Result #0 (890 chars): package main…
│ 📊 Context: 15.7% (20096/128000 tokens) — ok
└─ Step 2 complete (0.8s) ──────────────

│ 🏁 Finishing at step 3: The file main.go implements…
│ 📝 Assistant done: 18200 input / 45 output tokens

═══════════════════════════════════════════════════════════
Final Status: success
Final Output: The file main.go implements a custom event streaming example…
═══════════════════════════════════════════════════════════
```

## Next

→ **04-human-in-the-loop** — intercept tool calls for user confirmation before destructive operations execute.
