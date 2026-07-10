# Example 02 — Custom Tools

Build a custom tool that implements the `tools.Tool` interface, register it alongside SDK built-in tools, and give the agent a workspace to read/write files.

| Variant     | File            | Command                 | When to read                              |
|-------------|-----------------|-------------------------|-------------------------------------------|
| **Fluent**  | `main_fluent.go`| `go run -tags fluent .` | Recommended — bundled tools + declarative registration |
| **Classic** | `main.go`       | `go run .`              | Manual `registry.Register` of each tool   |

### Fluent (recommended)

The fluent builder bundles the common file tools via `FileTools()`; a custom tool is passed alongside. The finish tool is auto-registered:

```go
fw, err := sp4rk.NewF().
    Anthropic(key, "claude-sonnet-4-5").
    FileTools().
    Tools(NewCalculatorTool()).
    AutoApprove(). // satisfy the fail-closed registry for write_file
    Build()
// workspace is injected via the context (Run has no .Workspace() helper)
ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)
result, err := fw.RunF(ctx).System(systemPrompt).Ask(task)
```

## What you will learn

- How to implement the `tools.Tool` interface
- How `tools.BaseTool` reduces boilerplate
- How to register built-in tools from `github.com/v0lka/sp4rk/tools/builtins`
- How to inject a workspace path via context

## Architecture

```
                    ToolRegistry
                   ┌──────────────────────────────────┐
                   │  finish        (agent.FinishTool) │
                   │  read_file     (builtins)         │
                   │  write_file    (builtins)         │
                   │  list_directory(builtins)         │
                   │  calculator    (custom — this example) │
                   └──────────────────────────────────┘
                              │
                              ▼
                Framework.Execute(ctx, …)
                    │  ctx carries workspace path
                    ▼
                Agent: "compute 17*23+100, write to result.txt, read it back"
```

## Code walkthrough

### 1. Implementing the Tool interface

Every tool must implement `tools.Tool`:

```go
type Tool interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage
    Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
    DefaultPolicy() ToolPolicy
    IsUntrusted() bool
}
```

`tools.BaseTool` provides default implementations for everything except `Execute`, so concrete tools only need to implement that one method:

```go
type CalculatorTool struct {
    *tools.BaseTool
}

func NewCalculatorTool() *CalculatorTool {
    return &CalculatorTool{BaseTool: &tools.BaseTool{
        ToolName:        "calculator",
        ToolDescription: "Evaluate an arithmetic expression …",
        Schema:          json.RawMessage(`{ "type": "object", "properties": { … } }`),
        Policy:          tools.PolicyAlwaysAllow,
    }}
}

func (t *CalculatorTool) Execute(_ context.Context, input json.RawMessage) (tools.ToolResult, error) {
    // parse input → evaluate → return result
}
```

### 2. Tool policies

`DefaultPolicy()` controls how the tool registry treats the tool at execution time:

| Policy             | Behaviour                                                        |
|--------------------|------------------------------------------------------------------|
| `PolicyAlwaysAllow`| Execute without confirmation                                     |
| `PolicyUserConfirm`| Ask via the registry's `ConfirmFunc`; **denied if none is set** (fail-closed) |
| `PolicyAlwaysDeny` | Block the tool entirely                                          |

The calculator uses `PolicyAlwaysAllow` because it's read-only and safe. Destructive tools (write_file, delete_file) use `PolicyUserConfirm`, which is why this example passes a `ConfirmFunc` in `sp4rk.Config` — without it, `write_file` would be denied. You can also relax specific tools deliberately with `registry.SetPolicyOverride(name, tools.PolicyAlwaysAllow)`. See **example 04** for interactive confirmation via a custom HITL handler.

### 3. IsUntrusted

`IsUntrusted() bool` marks whether the tool returns external data (web, MCP, filesystem). When `true`, the executor wraps the tool's output in `<untrusted-content>` tags before injecting it into the LLM context as a prompt-injection defence. The calculator returns trusted internal data, so it's `false` (the `BaseTool` default).

### 4. Registering built-in tools

The SDK ships ready-made tools in `github.com/v0lka/sp4rk/tools/builtins`:

```go
registry.Register(builtins.NewReadFileTool())
registry.Register(builtins.NewWriteFileTool())
registry.Register(builtins.NewListDirectoryTool())
```

Other available built-ins include `bash_exec`, `edit_file`, `glob`, `ripgrep`, `web_fetch`, `create_directory`, `delete_file`, and more. Each has a `New*Tool()` constructor (some take configuration arguments, e.g. `NewBashExecTool(blacklist)` and `NewWebFetchTool(limits)`). The `web_search` tool lives in the `tools/builtins/websearch` subpackage.

### 5. Workspace path via context

File tools need to know where the workspace is. They retrieve it via `tools.WorkspacePathFrom(ctx)`. The caller injects it with `tools.WithWorkspacePath`:

```go
ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)
result, err := fw.Execute(ctx, systemPrompt, events, task)
```

This context propagates through the Conductor → Executor → tool execution, so every tool call sees the workspace path.

## Files

| File                 | Description                                  |
|----------------------|----------------------------------------------|
| `main.go`            | Classic SDK wiring: Framework, tool registration, execution (`//go:build !fluent`) |
| `main_fluent.go`     | Fluent wiring: `sp4rk.NewF` + bundled tools (`//go:build fluent`) |
| `calculator_tool.go` | The custom `CalculatorTool` (shared by both variants — tagless) |
| `calculator.go`      | Arithmetic expression evaluator (implementation detail, not SDK-specific) |

## Prerequisites

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

## Run

### Fluent (recommended)

```bash
cd sdk/examples/02-custom-tools
go run -tags fluent .
```

### Classic API (advanced control)

```bash
cd sdk/examples/02-custom-tools
go run .
```

## Expected output

```
Workspace: /tmp/sp4rk-example-02-123456

Status: success
Output: I computed 17 * 23 + 100 = 491 using the calculator tool, wrote the
result to result.txt, and read it back to verify the file contains "491".

File /tmp/sp4rk-example-02-123456/result.txt contains:
491
```

## Next

→ **03-event-streaming** — observe the agent's thoughts, tool calls, and results in real time via a custom `Events` implementation.
