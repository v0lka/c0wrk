# Getting Started

This guide covers installation, configuration, and building your first agent. By the end you will have a working agent that uses custom tools and a workspace context.

## Installation

The SDK is a standard Go module. Add it to your project:

```bash
go get github.com/v0lka/sp4rk@latest
```

### Go version

Go **1.26+** is required. The SDK's `go.mod` declares this version; older toolchains will fail to build.

### API keys

You need at least one LLM provider API key. Export it before running your program:

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
# or
export OPENAI_API_KEY="sk-..."
```

## Configuration

Everything starts with [`sp4rk.Config`](../framework.go), passed to [`sp4rk.New`](../framework.go). Zero-value fields are replaced with sensible defaults, so a minimal config only needs an LLM provider.

### Config

| Field | Type | Description |
| --- | --- | --- |
| `LLM` | `LLMConfig` | LLM providers and default model. **Required.** |
| `MCP` | `*MCPConfig` | Optional Model Context Protocol server configuration. `nil` means no MCP integration. |
| `Execution` | `ExecutionConfig` | Agent execution parameters (step budgets, retries, truncation, circuit breakers). |
| `Compaction` | `CompactionConfig` | Context-window management thresholds and strategy. |
| `HITL` | `agent.HITLHandler` | Optional human-in-the-loop hooks. `nil` uses defaults: allow all tool calls, deny step extensions. |
| `ConfirmFunc` | `tools.ConfirmFunc` | Confirmation callback for tools whose effective policy is `PolicyUserConfirm` (file writers, `bash_exec`, MCP tools). **The registry is fail-closed:** with no `ConfirmFunc`, such tools are denied instead of executing silently. See [tools.md](tools.md#policy-enforcement-in-execute-fail-closed). |
| `Checkpointer` | `orchestration.Checkpointer` | Optional blackboard state persistence. `nil` means no checkpointing. |
| `OnBlackboardChanged` | `func(changeType string)` | Optional callback invoked after every successful blackboard write (plan, step result, fact, reflection). `nil` means no notifications. |
| `Logger` | `*slog.Logger` | Optional structured logger. Uses `slog.Default()` if `nil`. |

### LLMConfig

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `Providers` | `[]llm.ProviderEntry` | — | Enabled LLM providers. **At least one is required.** |
| `DefaultModel` | `string` | first provider's first model | Override the auto-selected default model. Accepts a bare name (`"claude-sonnet-4-5"`) or composite ID (`"anthropic/claude-sonnet-4-5"`). |
| `MaxRetries` | `int` | `3` | Retry attempts for transient errors (HTTP 429, 502, 503, 529, network blips). `0` means the default (3); a **negative value** means explicitly 0 — retries disabled. |
| `InitialBackoff` | `string` | `"1s"` | Starting backoff duration for retries (parsed with `time.ParseDuration`). Empty means the default; a **negative duration** (e.g. `"-1s"`) means explicitly 0. |
| `MaxBackoff` | `string` | `"30s"` | Maximum backoff duration for retries. Empty means the default; a **negative duration** means explicitly 0. |
| `OutputTokenReserve` | `int` | `4096` | Context-window space reserved for model output; affects context-window validation. |

### ExecutionConfig

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `MaxSteps` | `int` | `50` | Maximum ReAct loop iterations per step. `0` means the default; a **negative value** means explicitly 0. |
| `MaxRetries` | `int` | `2` | Maximum retry attempts per plan step. `0` means the default; a **negative value** means explicitly 0 — retries disabled. |
| `ToolResultBudget` | `agent.ToolResultBudget` | `DefaultToolResultBudget()` | Tool result truncation: `HardCapTokens` and `MaxFillFraction`. |
| `CircuitBreaker` | `agent.CircuitBreakerConfig` | `DefaultCircuitBreakerConfig()` | Thresholds for repeat-call, truncation, parse-error, and fruitless-result detection. |
| `SafetyMarginPercent` | `int` | `5` | Percentage of the context window reserved as a safety margin. |
| `PreWarningPercent` | `int` | `0` (disabled) | Context fill percentage that triggers a pre-compaction warning listing vulnerable tool outputs. |
| `ToolCacheTTLSeconds` | `int` | `300` | TTL for cached tool results, enabling `tool_result_read` fragmentation for truncated outputs. Negative disables the cache. |
| `MaxDependencyContextChars` | `int` | `8000` | Character limit for step dependency summaries passed to dependent steps. |

### CompactionConfig

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `Strategy` | `string` | `"sliding_window"` | Compaction algorithm: `"sliding_window"`, `"summarization"`, or `"hierarchical"`. |
| `PredictivePercent` | `int` | `85` | Context fill percentage that triggers predictive compaction. |
| `WarningPercent` | `int` | `92` | Context fill percentage that triggers warning-level compaction. |
| `EmergencyPercent` | `int` | `98` | Context fill percentage that triggers emergency compaction. |

## Your first agent

This tutorial builds an agent with a custom calculator tool and built-in file tools, working inside a workspace directory.

### Step 1 — Create the Framework

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
if err != nil {
	return fmt.Errorf("failed to create framework: %w", err)
}
defer func() { _ = fw.Shutdown() }()
```

The `Framework` owns shared infrastructure: the LLM router, the tool registry, and (optionally) an MCP gateway. At least one provider is required.

### Step 2 — Register tools

The agent can only use tools that are in the registry. Register built-in tools, the finish tool, and any custom tools:

```go
registry := fw.ToolRegistry()

// Built-in file tools from github.com/v0lka/sp4rk/tools/builtins
registry.Register(builtins.NewReadFileTool())
registry.Register(builtins.NewWriteFileTool())
registry.Register(builtins.NewListDirectoryTool())

// The finish tool — required for task completion
registry.Register(agent.NewFinishTool())

// A custom tool
registry.Register(NewCalculatorTool())
```

> **Fail-closed enforcement:** tools whose policy is `PolicyUserConfirm` (like `write_file` above) are **denied** by the registry unless a confirmation channel is configured. Either pass a `ConfirmFunc` in `sp4rk.Config`:
>
> ```go
> ConfirmFunc: func(_ context.Context, req tools.ConfirmationRequest) (tools.ConfirmationResponse, error) {
>     // prompt the user here; this example auto-approves
>     return tools.ConfirmAllowOnce, nil
> },
> ```
>
> or explicitly relax individual tools: `registry.SetPolicyOverride("write_file", tools.PolicyAlwaysAllow)`. Read-only tools (`read_file`, `list_directory`, `glob`, …) have `PolicyAlwaysAllow` and need no configuration.

### The FinishTool

The `FinishTool` is a special tool that signals task completion. The agent calls it with its final answer, and the executor stops the ReAct loop.

**Why it is required:** the ReAct loop has no other way to know the task is done. Without a finish tool, the loop runs until the step budget (`ExecutionConfig.MaxSteps`) is exhausted and returns an `ExecutionResult` with `Status == "partial"` — the agent never cleanly signals completion.

Register it once after creating the framework:

```go
fw.ToolRegistry().Register(agent.NewFinishTool())
```

### Step 3 — Define a system prompt factory

The factory receives the context, the task description, and model metadata so it can adapt the prompt per model:

```go
systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
	return fmt.Sprintf(`You are a coding assistant working in the directory %s.
You have a calculator tool for arithmetic and file tools for reading/writing files.
When you have completed the task, call the finish tool with a summary.`, workspaceDir)
}
```

### Step 4 — Execute a message

Inject the workspace path into the context so built-in file tools know where to read and write — they retrieve it via `tools.WorkspacePathFrom(ctx)`:

```go
ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)

result, err := fw.Execute(ctx, systemPrompt, &agent.NoopEvents{}, task)
if err != nil {
	return fmt.Errorf("execution failed: %w", err)
}

fmt.Println("Status:", result.Status)
fmt.Println("Output:", result.Output)
```

### Step 5 — Inspect the result

`fw.Execute` returns an `*orchestration.ExecutionResult`. Check `result.Status` before trusting the output:

| Status | Meaning |
| --- | --- |
| `success` | All work completed; the agent called finish. |
| `partial` | Execution ended before completion (step budget exhausted, no finish call). |
| `failed` | Steps were attempted but failed and the retry budget is exhausted. |
| `aborted` | The reflector recommended aborting. |
| `cancelled` | The context was cancelled mid-execution. |

## Framework.Execute vs NewConductor

`Framework.Execute` is a convenience method: it creates a `Conductor`, runs one ReAct loop against a single user message, and returns the result. It is ideal for one-shot tasks and getting started.

```go
result, err := fw.Execute(ctx, systemPrompt, events, userMessage)
```

For repeated use — multiple messages in a session, conversation history, or when you want to reuse the same conductor configuration — call `NewConductor` once and reuse it:

```go
conductor, err := fw.NewConductor(systemPrompt)
if err != nil {
	return err
}
defer conductor.Cleanup()

bb := orchestration.NewMapBlackboard()
bb.SetOriginalRequest(userMessage)
result, err := conductor.Run(ctx, userMessage, bb, fw.ToolRegistry().List(), events, "")
```

`NewConductor` wires the conductor with the framework's shared infrastructure (LLM router, tool registry, MCP tools). The conductor is a single ReAct loop that owns a task end-to-end.

## Accessor methods

The framework exposes its shared infrastructure so you can configure it after construction:

- **`fw.ToolRegistry() *tools.ToolRegistry`** — the shared tool registry. Register and unregister tools here.
- **`fw.LLMRouter() *llm.Router`** — the shared LLM router. Switch the active model at runtime via the router's `SetModel` method.

## Shutdown

Call `Shutdown` to release resources held by the framework — primarily MCP server connections. It is safe to call multiple times:

```go
defer func() { _ = fw.Shutdown() }()
```

If you configured MCP servers, `Shutdown` stops the gateway and terminates the server processes. Without MCP, it is a no-op.

## Complete working example

Here is the full program with a custom calculator tool, built-in file tools, and a workspace context:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// CalculatorTool evaluates simple arithmetic expressions.
// It embeds tools.BaseTool which provides default implementations of
// Name, Description, InputSchema, DefaultPolicy, and IsUntrusted.
type CalculatorTool struct {
	*tools.BaseTool
}

func NewCalculatorTool() *CalculatorTool {
	return &CalculatorTool{BaseTool: &tools.BaseTool{
		ToolName:        "calculator",
		ToolDescription: "Evaluate an arithmetic expression (supports +, -, *, /, parentheses).",
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"expression": {
					"type": "string",
					"description": "The arithmetic expression to evaluate, e.g. \"2 + 3 * 4\""
				}
			},
			"required": ["expression"]
		}`),
		Policy: tools.PolicyAlwaysAllow,
	}}
}

// Execute parses the expression and returns the numeric result.
func (t *CalculatorTool) Execute(_ context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params struct {
		Expression string `json:"expression"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}
	if params.Expression == "" {
		return tools.ToolResult{Content: "validation error: expression is required", IsError: true}, nil
	}
	// evaluate(params.Expression) would go here — omitted for brevity.
	return tools.ToolResult{Content: fmt.Sprintf("%s = 491", params.Expression)}, nil
}

func main() {
	fw, err := sp4rk.New(sp4rk.Config{
		LLM: sp4rk.LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "anthropic",
				ProviderType: "anthropic",
				APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
				Models:       []string{"claude-sonnet-4-5"},
			}},
		},
		// write_file has PolicyUserConfirm; the registry is fail-closed, so a
		// confirmation channel is required. This example runs in a throwaway
		// temp workspace, so we auto-approve. In an interactive app, prompt
		// the user here.
		ConfirmFunc: func(_ context.Context, req tools.ConfirmationRequest) (tools.ConfirmationResponse, error) {
			fmt.Printf("[auto-approving %s]\n", req.ToolName)
			return tools.ConfirmAllowOnce, nil
		},
	})
	if err != nil {
		log.Fatalf("failed to create framework: %v", err)
	}
	defer func() { _ = fw.Shutdown() }()

	registry := fw.ToolRegistry()
	registry.Register(builtins.NewReadFileTool())
	registry.Register(builtins.NewWriteFileTool())
	registry.Register(builtins.NewListDirectoryTool())
	registry.Register(agent.NewFinishTool())
	registry.Register(NewCalculatorTool())

	workspaceDir, err := os.MkdirTemp("", "agent-workspace-*")
	if err != nil {
		log.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(workspaceDir) }()

	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)

	systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
		return fmt.Sprintf(`You are a coding assistant working in the directory %s.
You have a calculator tool for arithmetic and file tools for reading/writing files.
When you have completed the task, call the finish tool with a summary.`, workspaceDir)
	}

	task := fmt.Sprintf(
		"Use the calculator tool to compute 17 * 23 + 100, "+
			"then write the result to a file called 'result.txt' in the workspace (%s). "+
			"Finally, read the file back to verify its contents.",
		workspaceDir,
	)

	result, err := fw.Execute(ctx, systemPrompt, &agent.NoopEvents{}, task)
	if err != nil {
		log.Fatalf("execution failed: %v", err)
	}

	fmt.Println("Status:", result.Status)
	fmt.Println("Output:", result.Output)

	if content, err := os.ReadFile(filepath.Join(workspaceDir, "result.txt")); err == nil {
		fmt.Printf("\nresult.txt contains:\n%s\n", string(content))
	}
}
```

## Next steps

- Read the [architecture overview](architecture.md) to understand the layered design and data flow.
- See [tools.md](tools.md) for the full `Tool` interface and built-in tool catalog.
- See [events.md](events.md) to stream execution lifecycle events for live observability.
