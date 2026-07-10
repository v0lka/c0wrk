# Agent Executor

The `agent` package provides the core **ReAct loop engine** — the component that drives an AI agent through iterative *Thought → Action → Observation* cycles until a task is finished or the step budget is exhausted. This document covers the `Executor`, the data types it produces and consumes, the circuit breakers that protect it, and the context-management interfaces it depends on.

```go
import (
    "github.com/v0lka/sp4rk/agent"
    "github.com/v0lka/sp4rk/llm"
    "github.com/v0lka/sp4rk/tools"
)
```

## Overview

The `Executor` is the heart of the agent. Given a set of tools and a `ContextManager` (which owns the conversation history), it repeatedly:

1. Builds a prompt from the current context.
2. Calls the LLM.
3. Parses the response into a thought and zero or more tool calls.
4. Executes each tool call (subject to human-in-the-loop approval).
5. Records the observation back into the context.
6. Repeats — unless the agent calls the `finish` tool or a limit is hit.

Each iteration is represented as a `Step`. The full sequence of steps is returned in an `ExecutorResult`.

## The Executor

`Executor` runs the ReAct loop. It is **not safe for concurrent use on a single instance** — `Run` must be called one at a time. Orchestrators that need parallelism create a fresh `Executor` per step (see [Subagents](subagents.md)).

```go
type Executor struct {
    // ...configuration fields, set via NewExecutor and the Set* methods
}
```

### NewExecutor

`NewExecutor` constructs an `Executor` with the required dependencies and optional configuration supplied via functional options. Both the event emitter and the HITL handler are **nil-safe** — `nil` is replaced with `NoopEvents` and `NoopHITLHandler` respectively.

```go
func NewExecutor(
    llmRouter LLMCaller,            // the LLM provider/router to call
    toolRegistry ToolExecutor,      // tool execution + source/untrusted metadata
    maxSteps int,                   // hard cap on ReAct iterations
    opts ...Option,                 // optional configuration (see below)
) *Executor
```

Available options:

| Option | Purpose |
|--------|---------|
| `WithTokenCounter(c llm.TokenCounter)` | Token counting for context management. |
| `WithEvents(e Events)` | Lifecycle events (nil → `NoopEvents`). |
| `WithSuppressAssistantEvents(bool)` | Hide `AssistantChunk`/`AssistantDone` (for sub-steps). |
| `WithToolResultBudget(b ToolResultBudget)` | Stage 2 token-based truncation. Defaults to `DefaultToolResultBudget()` when unset. |
| `WithCircuitBreaker(c CircuitBreakerConfig)` | Loop-protection thresholds. Defaults to `DefaultCircuitBreakerConfig()` when unset. |
| `WithHITL(h HITLHandler)` | Human-in-the-loop hooks (nil → `NoopHITLHandler`). |

A minimal construction using the SDK defaults:

```go
exec := agent.NewExecutor(
    llmRouter,
    toolRegistry,
    25,                              // maxSteps
    agent.WithTokenCounter(tokenCounter),
    agent.WithEvents(&myEvents{}),   // Events implementation
    agent.WithToolResultBudget(agent.DefaultToolResultBudget()),
    // CircuitBreaker defaults to DefaultCircuitBreakerConfig() when omitted.
    // HITL defaults to NoopHITLHandler when omitted.
)
exec.SetLogger(slog.Default())
```

### Executor.Run

`Run` is the main execution method. The caller is responsible for setting up the task context (workspace path, task description, etc.) **before** calling `Run`.

```go
func (e *Executor) Run(
    ctx context.Context,
    taskTools []tools.ToolDescriptor,
    cw ContextManager,
) (*ExecutorResult, error)
```

- `ctx` — carries cancellation, workspace path, trajectory store, and other injected dependencies (see [Context helpers](#context-helpers)).
- `taskTools` — the tools available for this run. The `finish` tool is appended automatically if not already present.
- `cw` — the `ContextManager` that owns the prompt history and compaction logic.

Returns an `*ExecutorResult` and an error. A non-nil error indicates a fatal failure (LLM error, context cancellation). A `nil` error with `Finished == false` means the step budget was exhausted or a circuit breaker aborted the loop.

```go
result, err := exec.Run(ctx, taskTools, cm)
if err != nil {
    return err
}
if !result.Finished {
    // budget exhausted or circuit breaker fired
    log.Printf("did not finish: %s", result.Output)
}
fmt.Println(result.Output)
```

### The ReAct loop lifecycle

Each iteration of `Run` proceeds as follows:

1. **Trajectory sync** — if a `TrajectoryStore` is in the context, the current step history is synced so tools (e.g. a reflector) can read it.
2. **Step-limit boundary** — if the step budget is reached, `OnStepLimit` is consulted via the HITL handler to decide whether to grant more steps.
3. **StepStart event** — `emitter.StepStart(stepNum)` fires.
4. **LLM call** — the prompt is built from the context manager and sent to the LLM. If the provider reports a context-window-exceeded error, a reactive compaction is triggered and the call is retried.
5. **Thought event** — `emitter.Thought(stepNum, content, reasoning)` fires with the model's reasoning and content.
6. **Implicit finish check** — if the model returns no tool calls, the executor checks whether this is a legitimate finish or a failure mode (e.g. printed tool-call syntax as text). A nudge may be injected to force an explicit `finish` call.
7. **Truncation detection** — if the stop reason is `max_tokens` and tool calls were present, the call was truncated; a nudge is injected and the truncation counter is checked against the circuit breaker.
8. **Tool execution** — each tool call is executed (after HITL approval). Results are truncated in two stages, cached if applicable, and recorded as observations. `ToolCall` and `ToolResult` events fire per call.
9. **StepComplete event** — `emitter.StepComplete(stepNum, duration)` fires.
10. **Compaction** — the context fill is checked; if it crosses a threshold, compaction runs and `ContextFill` / `ContextCompaction` events fire.

The loop terminates when the `finish` tool is called (`Finished: true`) or the budget is exhausted (`Finished: false`).

## Step

A `Step` is a single iteration of the ReAct loop.

```go
type Step struct {
    Thought          string
    ReasoningContent string
    ReasoningItems   []llm.ReasoningItem
    Action           llm.ToolCall
    Observation      string
    IsError          bool
    TokensUsed       int
    UserNudge        string
    ResponseGroup    int64
    IsUntrusted      bool
    CacheHash        string
}
```

| Field | Description |
|-------|-------------|
| `Thought` | The model's textual response (the "thought" in ReAct). |
| `ReasoningContent` | Chain-of-thought text from reasoning models. Populated by providers that surface a separate reasoning channel. |
| `ReasoningItems` | Structured reasoning output items from the OpenAI Responses API. Each item carries an ID required to round-trip the reasoning chain across iterations. Empty for non-Responses-API providers. |
| `Action` | The `llm.ToolCall` the model chose to execute (tool name + arguments). |
| `Observation` | The tool result content, recorded back into the context as the next user/tool message. |
| `IsError` | `true` when the tool returned an error result. Used by the mutation gate to avoid counting failed mutating tools as successful mutations. |
| `TokensUsed` | Token usage for this step's LLM call. |
| `UserNudge` | An optional user message injected after the step's normal messages (e.g. step-limit nudges, circuit-breaker warnings). |
| `ResponseGroup` | Links steps that came from a single LLM response containing multiple tool calls. Steps sharing a non-zero value are rendered as one assistant message with multiple tool calls. Zero means a standalone step. |
| `IsUntrusted` | `true` when the observation came from an untrusted external source (web, MCP, filesystem). Such observations are wrapped in `<untrusted-content>` tags before entering the LLM context as a prompt-injection defense. |
| `CacheHash` | SHA-256 hash of the full (pre-truncation) tool result, stored in the `ToolResultCache`. Empty for non-cacheable tools or when caching is disabled. Used to replace old tool results with a cache reference, reducing replay cost. |

## ExecutorResult

The result of `Executor.Run`.

```go
type ExecutorResult struct {
    Output   string
    Steps    []Step
    Finished bool
}
```

- `Output` — the final answer (from the `finish` tool) or, when not finished, the abort reason (e.g. circuit breaker, fruitless abort, max steps).
- `Steps` — the full sequence of executed steps.
- `Finished` — `true` if the agent called `finish`; `false` if the budget was exhausted or a circuit breaker aborted.

## FillCheck

`FillCheck` represents the result of a context-window fill check.

```go
type FillCheck struct {
    Percent float64
    Status  string // "ok", "compact", "warning", "emergency", "reject"
    Used    int
    Max     int
}
```

| Status | Meaning |
|--------|---------|
| `ok` | Context is comfortably within limits. |
| `compact` | Compaction should run to free space. |
| `warning` | Context is filling up; a pre-compaction nudge may be emitted. |
| `emergency` | Context is nearly full; aggressive compaction is required. |
| `reject` | Context is too full to proceed even after compaction. |

`Used` and `Max` are the current and maximum token counts.

## FinishTool

`FinishTool` is the special tool that signals task completion. The executor appends it automatically to every run's tool set (unless a `finish` tool is already registered). When the model calls `finish`, the loop terminates with `Finished: true` and the tool's `answer` argument becomes `ExecutorResult.Output`.

```go
finishTool := agent.NewFinishTool()
registry.Register(finishTool)
```

The finish tool's input schema requires a single `answer` string. Internally, the tool unmarshals the input into an anonymous struct:

```go
var params struct {
    Answer string `json:"answer"`
}
```

**Why it is required:** an LLM that simply stops emitting tool calls has not necessarily completed the task — it may have hit a failure mode (e.g. printing tool-call syntax as text). The executor treats a text-only response as an *implicit finish* and injects a nudge demanding an explicit `finish` call. Only an actual `finish` tool call is accepted as completion (subject to the finish guard and mutation/checklist gates).

## Circuit breakers

`CircuitBreakerConfig` holds thresholds that protect the executor from unproductive infinite loops. When a threshold is crossed, the executor injects a nudge (a corrective system message) and, if the behavior persists, aborts the loop with `Finished: false`.

```go
type CircuitBreakerConfig struct {
    RepeatNudgeThreshold     int
    RepeatAbortThreshold     int
    TruncationAbortThreshold int
    ParseErrorAbortThreshold int

    FruitlessNudgeThreshold int
    FruitlessAbortThreshold int
    FruitlessMaxResultLen   int

    SameToolRepeatNudgeThreshold int
    SameToolRepeatAbortThreshold int
    SameToolResultSizeDelta      int
}
```

| Field | Description |
|-------|-------------|
| `RepeatNudgeThreshold` | Consecutive identical tool calls (same name + same arguments) before a nudge is injected. |
| `RepeatAbortThreshold` | Consecutive identical tool calls before the loop aborts. |
| `TruncationAbortThreshold` | Consecutive `max_tokens`-truncated responses containing tool calls before abort. |
| `ParseErrorAbortThreshold` | Consecutive parse errors on the same tool before abort. |
| `FruitlessNudgeThreshold` | Consecutive minimal-result calls before a nudge. |
| `FruitlessAbortThreshold` | Consecutive minimal-result calls before abort. |
| `FruitlessMaxResultLen` | Result length at or below which a call is considered "fruitless". |
| `SameToolRepeatNudgeThreshold` | Same tool called with varied args but similar results, before a nudge. |
| `SameToolRepeatAbortThreshold` | Same-tool varied-args repetition before abort. |
| `SameToolResultSizeDelta` | Maximum result-length difference to consider results "similar". |

Use the SDK defaults unless you have a specific reason to tune:

```go
cfg := agent.DefaultCircuitBreakerConfig()
// RepeatNudgeThreshold: 3, RepeatAbortThreshold: 5, TruncationAbortThreshold: 3,
// ParseErrorAbortThreshold: 3, FruitlessNudgeThreshold: 5, FruitlessAbortThreshold: 8,
// FruitlessMaxResultLen: 48, SameToolRepeatNudgeThreshold: 8,
// SameToolRepeatAbortThreshold: 12, SameToolResultSizeDelta: 128
```

## Tool result budget and truncation

Tool outputs are truncated in **two stages** to keep the context window manageable.

### Stage 1 — per-tool line/byte truncation

`ToolTruncationConfig` applies line- and byte-based limits *before* token-based budgeting. Byte truncation is UTF-8 safe (it walks back to the last valid codepoint boundary).

```go
type ToolTruncationConfig struct {
    MaxLines int // 0 = no line-based truncation
    MaxBytes int // 0 = no byte-based truncation
}
```

Configure per-tool defaults via `SetPerToolTruncation`. The SDK ships sensible defaults:

```go
// agent.DefaultToolTruncationConfig — a function returning a fresh map copy.
func DefaultToolTruncationConfig() map[string]ToolTruncationConfig {
    return map[string]ToolTruncationConfig{
        "read_file":      {MaxLines: 50000},
        "ripgrep":        {MaxLines: 5000},
        "glob":           {MaxLines: 5000},
        "list_directory": {MaxLines: 5000},
        "web_fetch":      {MaxBytes: 2097152}, // 2 MiB
        "bash_exec":      {MaxLines: 10000},
    }
}
```

### Stage 2 — token-based budget

`ToolResultBudget` caps how much of the context a single tool result may occupy.

```go
type ToolResultBudget struct {
    HardCapTokens   int     // absolute token cap per tool result
    MaxFillFraction float64 // max fraction of the context window a result may fill
}
```

```go
budget := agent.DefaultToolResultBudget()
// HardCapTokens: 30000, MaxFillFraction: 0.4
```

When a result exceeds the budget, it is truncated and a fragmentation nudge is appended telling the model how to retrieve the full output in fragments (see [Tool result cache](#tool-result-cache)).

## Tool result cache

`ToolResultCache` stores raw (pre-truncation) tool outputs indexed by `SHA256(toolName + "\x00" + content)`. When a result is truncated, the cache holds the full content and the executor appends a nudge instructing the model to retrieve fragments via a `tool_result_read` tool using the cache hash.

```go
cache := agent.NewToolResultCache(5 * time.Minute) // TTL for MCP-sourced entries
exec.SetToolCache(cache)
```

Key behaviors:

- **Hashing** — `ComputeToolResultHash(toolName, content)` returns the same hash `Store` would produce. Identical content from different tools gets different hashes.
- **Deduplication** — repeated identical calls produce the same hash; no duplicate entries.
- **File coherence** — for file-based tools (`read_file`, `write_file`, `edit_file`), the cache records the file path, mtime, and size. `CheckCoherence(hash)` returns `false` if the file has changed since caching.
- **MCP TTL** — MCP-sourced entries expire after the configured TTL. Non-MCP entries never expire. Expired entries are swept periodically.
- **Non-cacheable tools** — internal meta-tools (`finish`, `tool_result_read`, `store_fact`, etc.) are excluded from caching by default. Add your own meta-tools via `AddNonCacheableTools`.

```go
// Exclude an application-layer meta-tool from caching.
exec.AddNonCacheableTools("delegate", "reflect")
```

## ContextManager interface

`ContextManager` is the interface the executor needs for context-window management. It owns the prompt history and the compaction strategy.

```go
type ContextManager interface {
    BuildPrompt() []llm.Message
    AddStep(step Step)
    Compact(ctx context.Context) *CompactionResult
    SetStrategy(strategy CompactionStrategy)
    CheckFill() FillCheck
    CorrectTokenCount(apiInputTokens int)
    FillPercent() float64
    AvailableTokens() int
    OutputLimit() int
    VulnerableOutputs() []VulnerableOutput
}
```

| Method | Description |
|--------|-------------|
| `BuildPrompt()` | Returns the current message history for the next LLM call. |
| `AddStep(step)` | Records a completed step (thought + action + observation) into the history. |
| `Compact(ctx)` | Runs the compaction strategy, returning before/after fill percentages. |
| `SetStrategy(strategy)` | Sets the `CompactionStrategy` used by `Compact`. |
| `CheckFill()` | Returns a `FillCheck` describing the current fill state. |
| `CorrectTokenCount(apiInputTokens)` | Reconciles the local token estimate with the actual count reported by the provider. |
| `FillPercent()` | Returns the current context fill as a percentage. |
| `AvailableTokens()` | Returns the number of tokens still available. |
| `OutputLimit()` | Returns the maximum output tokens the model may produce. |
| `VulnerableOutputs()` | Returns tool outputs that will be pruned on the next pruning cycle. |

### CompactionStrategy

`CompactionStrategy` defines an algorithm for compressing step history into a smaller set of messages when the context fills up.

```go
type CompactionStrategy interface {
    Compact(ctx context.Context, steps []Step, budgetTokens int) []llm.Message
}
```

A compaction produces a `CompactionResult` with before/after fill percentages:

```go
type CompactionResult struct {
    BeforePercent float64
    AfterPercent  float64
}
```

### VulnerableOutput

`VulnerableOutput` describes a tool output that will be pruned on the next pruning cycle. The pre-compaction nudge lists these so the model can preserve key findings (e.g. via a fact-store tool) before they are lost.

```go
type VulnerableOutput struct {
    ToolName  string
    InputHint string // human-readable summary of tool input (file path, pattern, etc.)
}
```

## LLMCaller and ToolExecutor interfaces

The executor depends on two narrow interfaces so it is decoupled from any specific LLM provider or tool registry.

```go
type LLMCaller interface {
    Call(ctx context.Context, req llm.ChatRequest) (resp *llm.ChatResponse, err error)
}

type ToolExecutor interface {
    Execute(ctx context.Context, name string, input json.RawMessage) (result tools.ToolResult, err error)
    GetToolSource(name string) string
    IsToolUntrusted(name string) bool
}
```

- `LLMCaller.Call` — sends a chat request and returns the response. The executor handles context-exceeded errors by triggering reactive compaction and retrying.
- `ToolExecutor.Execute` — runs a tool by name with raw JSON input.
- `GetToolSource` — returns the source of a tool (e.g. `"core"`, the MCP server name like `"filesystem"`); empty string if not found. Used to detect MCP tools for cache TTL handling.
- `IsToolUntrusted` — reports whether a tool's output is from an untrusted external source (MCP tools and tools flagged `IsUntrusted()`). Drives the `<untrusted-content>` wrapping of observations.

## TrajectoryStore

`TrajectoryStore` is a mutable holder for the executor's current trajectory. The executor syncs its step history to the store at each loop iteration so tools (e.g. a reflector) can access the trajectory via context.

```go
type TrajectoryStore interface {
    Sync(steps []Step)
    Steps() []Step
}
```

Inject and retrieve a store through the context:

```go
ctx = agent.WithTrajectoryStore(ctx, &myTrajectoryStore{})
// ... inside a tool:
if ts := agent.TrajectoryStoreFrom(ctx); ts != nil {
    steps := ts.Steps()
}
```

A minimal thread-safe implementation:

```go
type trajStore struct {
    mu    sync.Mutex
    steps []agent.Step
}

func (s *trajStore) Sync(steps []agent.Step) { s.mu.Lock(); s.steps = steps; s.mu.Unlock() }
func (s *trajStore) Steps() []agent.Step     { s.mu.Lock(); defer s.mu.Unlock(); return s.steps }
```

## Executor configuration methods

These setters configure optional behavior. Call them **before** `Run`.

| Method | Description |
|--------|-------------|
| `SetLogger(l *slog.Logger)` | Sets the structured logger. Defaults to a discard handler. |
| `SetReasoningEffort(effort string)` | Sets the reasoning effort passed to LLM calls (empty = no reasoning control). |
| `SetToolCache(cache *ToolResultCache)` | Sets the shared tool-result cache. All tool results are stored before truncation. |
| `SetPerToolTruncation(cfg map[string]ToolTruncationConfig)` | Sets per-tool Stage 1 truncation defaults. |
| `SetPreWarningPercent(percent int)` | Sets the context-fill percentage that triggers the pre-compaction `store_fact` warning nudge (0 = disabled). |
| `SetFinishGuard(fn func(ctx) error)` | Sets a callback invoked before finish is accepted. A non-nil error rejects finish with a nudge. Used to prevent abandoning pending async work. |
| `AddNonCacheableTools(names ...string)` | Adds tool names to the non-cacheable set (extends SDK defaults). |
| `SetMutationRequired(required bool)` | When `true`, finish is rejected unless a mutating tool ran successfully. |
| `SetChecklistGateEnabled(enabled bool)` | Enables/disables the checklist gate (default: enabled). |
| `SetPlanContext(stepID string, index, total int)` | Sets plan-step metadata for structured logging. |
| `SetHITLHandler(h HITLHandler)` | Sets the human-in-the-loop handler (nil-safe). |

### Finish guard example

The finish guard lets a caller block premature completion — for instance, to prevent an agent from finishing while async sub-tasks are still running:

```go
exec.SetFinishGuard(func(ctx context.Context) error {
    if pendingSubTasks() {
        return errors.New("async sub-tasks are still running; wait for them before finishing")
    }
    return nil
})
```

If the guard returns an error, finish is rejected and a nudge containing the error message is injected. On the second attempt, finish is accepted regardless (soft gate).

## Context helpers

The executor reads several optional values from the context. These are injected by the caller or by the orchestration layer before `Run`.

| Helper | Purpose |
|--------|---------|
| `WithStepID(ctx, id)` / `StepIDFromContext(ctx)` | Attaches the current plan-step ID. |
| `WithTrajectoryStore(ctx, store)` / `TrajectoryStoreFrom(ctx)` | Injects a trajectory store. |
| `WithToolResultCache(ctx, cache)` / `ToolResultCacheFromContext(ctx)` | Makes the cache available to the `tool_result_read` tool. |
| `WithPerToolTruncation(ctx, cfg)` / `PerToolTruncationFromContext(ctx)` | Shares truncation config with `tool_result_read` for `num_lines` enforcement. |
| `WithStepTodoUpdateFunc(ctx, fn)` / `StepTodoUpdateFuncFromContext(ctx)` | Callback for checklist/to-do updates. |
| `WithDumpWriter(ctx, w)` / `DumpWriterFromContext(ctx)` | Injects an `io.Writer` for LLM request/response dumps. |

Additional stores for inter-step communication live in the `agent` package: `StepOutputStore`, `FactStore`, and `FinalResultStore`, each with a `With*`/`*FromContext` pair. See [Subagents](subagents.md) for how these are wired during parallel plan execution.

## LLM debugging callers

The executor talks to the model through the [`LLMCaller`](#llmcaller-and-toolexecutor-interfaces) interface, which makes it trivially wrappable. The `agent` package ships two debugging decorators you can stack onto the caller the executor receives — no changes to the executor itself:

```go
import "github.com/v0lka/sp4rk/agent"
```

### NewDumpCaller — full JSONL request/response dumps

```go
func NewDumpCaller(inner agent.LLMCaller, w io.Writer, logger *slog.Logger) agent.LLMCaller
```

Wraps `inner` so that every `Call` writes the **full, untruncated** `ChatRequest` and `ChatResponse` as JSONL records to `w`. Each record carries a UTC timestamp, a `direction` (`request`/`response`), the payload, and (for responses) an `error` field. Pass `nil` for `w` (or `logger`) to no-op. This is the mechanism behind `WithDumpWriter` above — but `NewDumpCaller` lets you attach a dump writer to the caller directly, independent of context plumbing (e.g. when wiring an executor that runs many steps into one shared file).

### NewLoggingLLMCaller — structured token-usage logs

```go
func NewLoggingLLMCaller(inner agent.LLMCaller, provider string, logger *slog.Logger) agent.LLMCaller
```

Logs, at **DEBUG** level, the outgoing request (provider, model, message/tool counts) and the response token usage (input/output/total tokens, stop reason, tool-call count). `provider` is a logical label (e.g. `"anthropic"`) used only for log attribution; a `nil` logger returns `inner` unchanged. Because logging is DEBUG-level, it is toggled purely by the logger's level — no code changes needed to enable it in production.

### Stacking and combining

Both decorators are transparent — they implement `LLMCaller` and forward the response/error untouched — so they compose freely and can be stacked with `llm.NewTrackingCaller` (for [usage tracking](llm-providers.md#usage-tracking)):

```go
caller := agent.NewLoggingLLMCaller(
    agent.NewDumpCaller(rawCaller, dumpFile, logger),
    "anthropic",
    logger,
)
exec := agent.NewExecutor(caller, toolRegistry, 25)
```

For per-step dumps orchestrated by the conductor (one `step_<id>.jsonl` file per plan step), the orchestration layer uses a [`StepDumpTracker`](orchestration.md) and feeds each step's writer into the context via `WithDumpWriter`.

## Putting it together

A complete standalone executor run:

```go
exec := agent.NewExecutor(
    llmRouter,
    toolRegistry,
    25,
    agent.WithTokenCounter(tokenCounter),
    agent.WithEvents(&myEvents{}),
    agent.WithToolResultBudget(agent.DefaultToolResultBudget()),
)
exec.SetLogger(logger)
exec.SetToolCache(agent.NewToolResultCache(5 * time.Minute))
exec.SetPerToolTruncation(agent.DefaultToolTruncationConfig())
exec.SetPreWarningPercent(80)

ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)
ctx = agent.WithTrajectoryStore(ctx, &trajStore{})

result, err := exec.Run(ctx, taskTools, cm)
if err != nil {
    log.Fatalf("execution failed: %v", err)
}
fmt.Printf("finished=%v, steps=%d\n", result.Finished, len(result.Steps))
```
