# Example 04 — Human-in-the-Loop

Intercept tool calls for user confirmation before destructive operations execute. A custom `HITLHandler` prompts on stdin whenever the agent tries to use a "dangerous" tool and decides whether to allow, deny, or modify the call.

**This example is interactive** — it reads `y`/`n` from stdin.

| Variant     | File            | Command                 | When to read                              |
|-------------|-----------------|-------------------------|-------------------------------------------|
| **Fluent**  | `main_fluent.go`| `go run -tags fluent .` | Recommended — `.HITL(handler)` + classic escape |
| **Classic** | `main.go`       | `go run .`              | `Config.HITL` wiring + manual policy overrides |

> `ConfirmingHITL` lives in `hitl.go` (tagless) so both variants share it.

### Fluent (recommended)

The HITL handler is wired in via the `.HITL` builder method. Because the registry stays fail-closed, the gated tools are relaxed with a classic escape (`registry.SetPolicyOverride`) after construction — the intended hybrid of fluent + classic control:

```go
fw, _ := sp4rk.NewF().
    Anthropic(key, "claude-sonnet-4-5").
    HITL(NewConfirmingHITL([]string{"write_file", "delete_file", "create_directory", "bash_exec"})).
    MaxSteps(10).
    FileTools().
    Tools(builtins.NewDeleteFileTool()).
    Build()
// classic escape: relax registry policy so the HITL handler is the single gate
for _, name := range []string{"write_file", "delete_file", "create_directory"} {
    fw.ToolRegistry().SetPolicyOverride(name, tools.PolicyAlwaysAllow)
}
fw.RunF(ctx).System(systemPrompt).Ask(task)
```

## What you will learn

- The `HITLHandler` interface and its two methods
- How to allow, deny, or modify tool calls before execution
- How to handle step-limit exhaustion interactively
- How to wire a HITL handler into `sp4rk.Config`

## Architecture

```
Agent decides to call write_file
    │
    ▼
Executor calls HITLHandler.OnToolCall("write_file", input)
    │
    ├─ Allow=true  → tool executes normally
    ├─ Allow=false → tool skipped, denial reason fed back to agent
    └─ Allow=true + ModifiedInput → tool executes with modified input
```

## Code walkthrough

### 1. The HITLHandler interface

```go
type HITLHandler interface {
    OnToolCall(ctx context.Context, toolName string, input json.RawMessage) (*HITLToolDecision, error)
    OnStepLimit(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error)
}
```

Two hooks, both called synchronously from the executor loop:

| Method       | When it's called                          | What you control                     |
|--------------|--------------------------------------------|--------------------------------------|
| `OnToolCall` | Before **every** tool execution            | Allow / deny / modify the input      |
| `OnStepLimit`| When the step budget is exhausted          | Grant more steps or stop             |

### 2. OnToolCall — allow, deny, or modify

```go
func (h *ConfirmingHITL) OnToolCall(_ context.Context, toolName string, input json.RawMessage) (*agent.HITLToolDecision, error) {
    if !h.DangerousTools[toolName] {
        return &agent.HITLToolDecision{Allow: true}, nil  // auto-allow safe tools
    }

    // Prompt the user for dangerous tools
    fmt.Printf("⚠️  APPROVAL REQUIRED: %s(%s)\n", toolName, input)
    // read y/n from stdin…

    if approved {
        return &agent.HITLToolDecision{Allow: true, Reason: "user approved"}, nil
    }
    return &agent.HITLToolDecision{Allow: false, Reason: "user denied"}, nil
}
```

The `HITLToolDecision` struct:

| Field           | Type             | Purpose                              |
|-----------------|------------------|--------------------------------------|
| `Allow`         | `bool`           | Whether the tool call proceeds       |
| `ModifiedInput` | `json.RawMessage`| When non-nil + Allow=true, replaces the original input |
| `Reason`        | `string`         | Human-readable explanation (shown to the agent) |

When `Allow=false`, the `Reason` is fed back to the agent as the tool result, so the agent can adapt (e.g. try a different approach or call `finish`).

**Modifying input**: You can rewrite the tool's input before execution. For example, you could sanitize a file path or cap a `bash_exec` timeout:

```go
return &agent.HITLToolDecision{
    Allow:         true,
    ModifiedInput: json.RawMessage(`{"path":"/safe/path.txt"}`),
}, nil
```

### 3. OnStepLimit — extend or stop

```go
func (h *ConfirmingHITL) OnStepLimit(_ context.Context, currentStep, maxSteps int, reason string) (agent.StepLimitResponse, error) {
    // Ask the user if they want to grant one more step
    return agent.StepLimitAllowOnce, nil  // or StepLimitDeny / StepLimitAllowAlways
}
```

Three possible responses:

| Response              | Effect                                           |
|-----------------------|--------------------------------------------------|
| `StepLimitAllowOnce`  | Grant exactly one more iteration                 |
| `StepLimitAllowAlways`| Remove the step limit for the rest of execution  |
| `StepLimitDeny`       | Stop execution (the default `NoopHITLHandler` behaviour) |

The `reason` parameter is non-empty when a circuit breaker triggered the pause (e.g. repeated identical tool calls, fruitless results). This lets you show the user *why* the agent got stuck.

### 4. Wiring into the Framework

```go
fw, err := sp4rk.New(sp4rk.Config{
    LLM:       llmConfig,
    Execution: sp4rk.ExecutionConfig{ MaxSteps: 10 },
    HITL:      NewConfirmingHITL([]string{"write_file", "delete_file", "bash_exec"}),
})
```

`Config.HITL` accepts any `agent.HITLHandler`. When `nil`, the Framework uses `NoopHITLHandler` which allows all tool calls and denies step extensions.

### 5. Tool policies vs HITL

Tool policies (`Tool.DefaultPolicy()`) and the HITL handler are two separate gates:

- **Tool policy** — enforced by the tool registry inside `Execute()`. `PolicyUserConfirm` tools require the registry's `ConfirmFunc`; with none configured they are **denied** (fail-closed).
- **HITL handler** — dynamic, called by the executor for **every** tool call before it reaches the registry. Can make per-call decisions based on the actual input.

Because this example does its confirmation in the HITL handler, it explicitly relaxes the registry-level policy for the gated tools (`registry.SetPolicyOverride(name, tools.PolicyAlwaysAllow)`) so the user is not asked twice. If you prefer registry-level confirmation instead, drop the overrides and pass a `ConfirmFunc` in `sp4rk.Config`.

## Prerequisites

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

## Run

### Fluent (recommended)

```bash
cd sdk/examples/04-human-in-the-loop
go run -tags fluent .
```

### Classic API (advanced control)

```bash
cd sdk/examples/04-human-in-the-loop
go run .
```

## Expected output

```
Workspace: /tmp/sp4rk-example-04-123456
This example is INTERACTIVE — you will be asked to approve tool calls.
Press y + Enter to allow, or just Enter to deny.

⚠️  APPROVAL REQUIRED
   Tool: write_file
   Input: {
     "path": "notes.txt",
     "content": "Hello HITL!"
   }
   Allow? [y/N]: y
   ✅ Allowed

⚠️  APPROVAL REQUIRED
   Tool: delete_file
   Input: {
     "path": "notes.txt"
   }
   Allow? [y/N]: y
   ✅ Allowed

═══════════════════════════════════════════
Status: success
Output: I created notes.txt with "Hello HITL!" and then deleted it.
═══════════════════════════════════════════
```

If you deny a tool call, the agent receives the denial reason and can adapt:

```
⚠️  APPROVAL REQUIRED
   Tool: write_file
   Allow? [y/N]: n
   ❌ Denied
```

The agent then sees: `Tool result: user denied this tool call` and can try a different approach or call `finish`.

## Next

→ **05-mcp-integration** — bring in external tools via Model Context Protocol servers.
