# Human-in-the-Loop (HITL)

The SDK provides hooks for human-in-the-loop interaction during agent execution. A `HITLHandler` can intercept tool calls for confirmation or modification before they execute, and decide what happens when the agent reaches its step budget. This document covers the `HITLHandler` interface, the decision types, the default no-op handler, and complete integration examples.

```go
import (
    "context"
    "encoding/json"

    "github.com/v0lka/sp4rk/agent"
)
```

## HITLHandler interface

`HITLHandler` provides two hooks: one invoked before every tool execution, and one invoked when the step budget is exhausted or a circuit breaker fires.

```go
type HITLHandler interface {
    // OnToolCall is invoked before executing a tool.
    OnToolCall(ctx context.Context, toolName string, input json.RawMessage) (*HITLToolDecision, error)

    // OnStepLimit is invoked when the agent exhausts its step budget or a
    // circuit breaker abort threshold is reached.
    OnStepLimit(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error)
}
```

> **Synchronous execution:** all methods are called synchronously from the executor loop. Implementations should return promptly or respect context cancellation to avoid blocking execution. A handler that blocks indefinitely on user input will stall the entire agent.

### OnToolCall

`OnToolCall` is invoked before **every** tool execution. The handler can:

- **Allow** the tool call as-is (`Allow: true`, `ModifiedInput: nil`).
- **Deny** the tool call (`Allow: false`).
- **Modify** the tool input (`Allow: true` with a non-nil `ModifiedInput`).

Returning a `nil` decision allows the call unchanged (equivalent to `Allow: true` with no modification).

The `input` parameter is the raw JSON the model generated for the tool. The handler can inspect, validate, or rewrite it before execution.

### OnStepLimit

`OnStepLimit` is invoked when the agent exhausts its step budget or a circuit breaker abort threshold is reached. The `reason` parameter describes why execution was paused — it is an empty string for normal step-limit exhaustion, and a descriptive message (e.g. a circuit-breaker trigger) otherwise. The response determines whether execution continues or stops.

## HITLToolDecision

`HITLToolDecision` represents the handler's decision about a tool call.

```go
type HITLToolDecision struct {
    // Allow determines whether the tool call should proceed.
    Allow bool
    // ModifiedInput, when non-nil and Allow is true, replaces the original tool input.
    ModifiedInput json.RawMessage
    // Reason is a human-readable explanation for the decision (shown in UI).
    Reason string
}
```

| Field | Description |
|-------|-------------|
| `Allow` | `true` to execute the tool; `false` to reject the call. |
| `ModifiedInput` | When non-nil and `Allow` is `true`, this raw JSON replaces the model's original input. Use it to sanitize arguments, enforce constraints, or fix malformed input. |
| `Reason` | A human-readable explanation shown in the UI. For denied calls, this is surfaced to the user; for allowed calls it is informational. |

### Allow / deny / modify semantics

```go
// Allow as-is
return &agent.HITLToolDecision{Allow: true}, nil

// Deny
return &agent.HITLToolDecision{
    Allow:  false,
    Reason: "user denied this tool call",
}, nil

// Modify the input (e.g. force a safe path)
modified, _ := json.Marshal(map[string]string{"path": "/safe/output.txt"})
return &agent.HITLToolDecision{
    Allow:         true,
    ModifiedInput: modified,
    Reason:        "rewrote path to a safe location",
}, nil
```

## StepLimitResponse

`StepLimitResponse` represents the user's decision when the agent's step limit is reached. Three constants are defined:

```go
type StepLimitResponse string

const (
    StepLimitAllowOnce  StepLimitResponse = "allow_once"
    StepLimitAllowAlways StepLimitResponse = "allow_always"
    StepLimitDeny       StepLimitResponse = "deny"
)
```

| Constant | Behavior |
|----------|----------|
| `StepLimitAllowOnce` | Grants exactly one additional iteration. The handler will be consulted again if the limit is reached once more. |
| `StepLimitAllowAlways` | Removes the step limit for the remainder of this execution. The agent runs until it finishes or a circuit breaker fires. |
| `StepLimitDeny` | Terminates execution. The executor returns with `Finished: false`. |

## NoopHITLHandler

`NoopHITLHandler` is the default handler used when no HITL handler is configured. It:

- **Allows all tool calls** unchanged.
- **Denies step-limit extensions** — execution stops at the budget.

```go
type NoopHITLHandler struct{}
```

Like `NoopEvents`, it is designed for the embed-and-override pattern: embed it in your own handler and override only the methods you need. The embedded no-op handles the rest.

```go
// autoApproveHITL allows everything except a configured denylist.
type autoApproveHITL struct {
    agent.NoopHITLHandler
    deniedTools map[string]bool
}

func (h *autoApproveHITL) OnToolCall(_ context.Context, name string, _ json.RawMessage) (*agent.HITLToolDecision, error) {
    if h.deniedTools[name] {
        return &agent.HITLToolDecision{Allow: false, Reason: name + " is blocked by policy"}, nil
    }
    return &agent.HITLToolDecision{Allow: true}, nil
}
```

Because `OnStepLimit` is inherited from `NoopHITLHandler` (which returns `StepLimitDeny`), this handler stops at the step budget without any extra code.

## Integration

The HITL handler is passed to the framework via `Config.HITL`. A `nil` value uses the defaults (`NoopHITLHandler`).

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
    Execution: sp4rk.ExecutionConfig{
        MaxSteps: 10, // low limit to demonstrate OnStepLimit
    },
    // HITL is the human-in-the-loop handler. Nil means defaults
    // (allow all tool calls, deny step extensions).
    HITL: NewConfirmingHITL([]string{
        "write_file",
        "delete_file",
        "bash_exec",
    }),
})
```

You can also set the handler directly on an `Executor` via `SetHITLHandler` (nil-safe):

```go
exec := agent.NewExecutor(/* ... */, nil)
exec.SetHITLHandler(&myHITLHandler{})
```

## Complete ConfirmingHITL example

The following example implements a `ConfirmingHITL` handler that prompts on stdin whenever the agent tries to use a "dangerous" tool. It is interactive — it reads `y`/`n` from stdin.

```go
package main

import (
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "log"
    "os"
    "strings"

    "github.com/v0lka/sp4rk"
    "github.com/v0lka/sp4rk/agent"
    "github.com/v0lka/sp4rk/llm"
    "github.com/v0lka/sp4rk/tools"
    "github.com/v0lka/sp4rk/tools/builtins"
)

// ConfirmingHITL implements agent.HITLHandler. It allows all tool calls
// by default but prompts for confirmation on a configurable denylist of
// "dangerous" tool names.
type ConfirmingHITL struct {
    // DangerousTools lists tool names that require explicit confirmation.
    DangerousTools map[string]bool

    // reader is used for stdin prompts.
    reader *bufio.Reader
}

// NewConfirmingHITL creates a HITL handler that confirms calls to the
// given dangerous tool names.
func NewConfirmingHITL(dangerousTools []string) *ConfirmingHITL {
    dangerous := make(map[string]bool, len(dangerousTools))
    for _, name := range dangerousTools {
        dangerous[name] = true
    }
    return &ConfirmingHITL{
        DangerousTools: dangerous,
        reader:         bufio.NewReader(os.Stdin),
    }
}

// OnToolCall is invoked before every tool execution. It can:
//   - Allow the call as-is (Allow=true, ModifiedInput=nil)
//   - Deny the call (Allow=false)
//   - Modify the input (Allow=true, ModifiedInput=non-nil)
func (h *ConfirmingHITL) OnToolCall(_ context.Context, toolName string, input json.RawMessage) (*agent.HITLToolDecision, error) {
    // Non-dangerous tools are allowed immediately.
    if !h.DangerousTools[toolName] {
        return &agent.HITLToolDecision{Allow: true}, nil
    }

    // Pretty-print the tool call for the user.
    fmt.Printf("\n⚠️  APPROVAL REQUIRED\n")
    fmt.Printf("   Tool: %s\n", toolName)
    fmt.Printf("   Input: %s\n", formatJSON(input))
    fmt.Printf("   Allow? [y/N]: ")

    line, _ := h.reader.ReadString('\n')
    line = strings.TrimSpace(strings.ToLower(line))

    if line == "y" || line == "yes" {
        fmt.Printf("   ✅ Allowed\n")
        return &agent.HITLToolDecision{Allow: true, Reason: "user approved"}, nil
    }

    fmt.Printf("   ❌ Denied\n")
    return &agent.HITLToolDecision{
        Allow:  false,
        Reason: "user denied this tool call",
    }, nil
}

// OnStepLimit is invoked when the agent exhausts its step budget or a
// circuit breaker fires. The handler decides whether to grant more steps
// or terminate execution.
func (h *ConfirmingHITL) OnStepLimit(_ context.Context, currentStep, maxSteps int, reason string) (agent.StepLimitResponse, error) {
    fmt.Printf("\n⏰ STEP LIMIT REACHED (step %d/%d", currentStep, maxSteps)
    if reason != "" {
        fmt.Printf(", reason: %s", reason)
    }
    fmt.Printf(")\n   Grant one more step? [y/N]: ")

    line, _ := h.reader.ReadString('\n')
    line = strings.TrimSpace(strings.ToLower(line))

    if line == "y" || line == "yes" {
        fmt.Printf("   ✅ One more step granted\n")
        return agent.StepLimitAllowOnce, nil
    }

    fmt.Printf("   🛑 Execution stopped\n")
    return agent.StepLimitDeny, nil
}

// formatJSON pretty-prints a JSON RawMessage for display.
func formatJSON(raw json.RawMessage) string {
    var v any
    if err := json.Unmarshal(raw, &v); err != nil {
        return string(raw)
    }
    pretty, err := json.MarshalIndent(v, "   ", "  ")
    if err != nil {
        return string(raw)
    }
    return string(pretty)
}

func run() error {
    // Create the Framework with our custom HITL handler.
    // The handler is passed via Config.HITL.
    fw, err := sp4rk.New(sp4rk.Config{
        LLM: sp4rk.LLMConfig{
            Providers: []llm.ProviderEntry{{
                Name:         "anthropic",
                ProviderType: "anthropic",
                APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
                Models:       []string{"claude-sonnet-4-5"},
            }},
        },
        Execution: sp4rk.ExecutionConfig{
            MaxSteps: 10, // low limit to demonstrate OnStepLimit
        },
        // HITL is the human-in-the-loop handler. Nil means defaults
        // (allow all tool calls, deny step extensions).
        HITL: NewConfirmingHITL([]string{
            "write_file",
            "delete_file",
            "bash_exec",
        }),
    })
    if err != nil {
        return fmt.Errorf("failed to create framework: %w", err)
    }
    defer func() { _ = fw.Shutdown() }()

    // Register tools — including dangerous ones that will trigger confirmation.
    registry := fw.ToolRegistry()
    registry.Register(builtins.NewReadFileTool())
    registry.Register(builtins.NewWriteFileTool())
    registry.Register(builtins.NewDeleteFileTool())
    registry.Register(builtins.NewListDirectoryTool())
    registry.Register(builtins.NewCreateDirectoryTool())
    registry.Register(agent.NewFinishTool())

    // Set up workspace
    workspaceDir, err := os.MkdirTemp("", "sdk-example-hitl-*")
    if err != nil {
        return fmt.Errorf("failed to create temp dir: %w", err)
    }
    defer func() { _ = os.RemoveAll(workspaceDir) }()

    fmt.Printf("Workspace: %s\n", workspaceDir)
    fmt.Println("This example is INTERACTIVE — you will be asked to approve tool calls.")
    fmt.Println("Press y + Enter to allow, or just Enter to deny.")
    fmt.Println()

    ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)

    systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
        return fmt.Sprintf(`You are a file management assistant working in %s.
Create a file called "notes.txt" with some content, then delete it.
Use the available file tools. Call finish when done.`, workspaceDir)
    }

    task := "Create a file called notes.txt with the text 'Hello HITL!', then delete it."

    result, err := fw.Execute(ctx, systemPrompt, &agent.NoopEvents{}, task)
    if err != nil {
        return fmt.Errorf("execution failed: %w", err)
    }

    fmt.Println("\n═══════════════════════════════════════════")
    fmt.Println("Status:", result.Status)
    fmt.Println("Output:", result.Output)
    fmt.Println("═══════════════════════════════════════════")
    return nil
}

func main() {
    if err := run(); err != nil {
        log.Fatalf("%v", err)
    }
}
```

## Auto-approve pattern

For non-interactive deployments (e.g. CI or batch processing), you typically want to auto-approve tool calls while still enforcing a policy denylist. The embed-and-override pattern with `NoopHITLHandler` makes this concise:

```go
// autoApproveHITL allows everything except a configured denylist.
// OnStepLimit is inherited from NoopHITLHandler (denies extensions).
type autoApproveHITL struct {
    agent.NoopHITLHandler
    deniedTools map[string]bool
}

func (h *autoApproveHITL) OnToolCall(_ context.Context, name string, _ json.RawMessage) (*agent.HITLToolDecision, error) {
    if h.deniedTools[name] {
        return &agent.HITLToolDecision{Allow: false, Reason: name + " is blocked by policy"}, nil
    }
    return &agent.HITLToolDecision{Allow: true}, nil
}
```

Wire it into the framework the same way:

```go
fw, err := sp4rk.New(sp4rk.Config{
    // ...
    HITL: &autoApproveHITL{
        deniedTools: map[string]bool{"delete_directory": true}, // block destructive ops
    },
})
```

## Design notes

- **Every tool call is intercepted.** `OnToolCall` fires before *every* tool, including read-only ones. Filter by `toolName` if you only want to confirm destructive operations.
- **Modification is powerful.** `ModifiedInput` lets you enforce invariants (e.g. clamping line ranges, redirecting paths) without denying the call. The modified input replaces the model's arguments verbatim.
- **Step-limit reasons.** When `OnStepLimit` fires with a non-empty `reason`, a circuit breaker (not plain budget exhaustion) triggered the pause. You can use this to decide whether more steps are likely to help. See [Agent Executor](agent-executor.md#circuit-breakers) for the breaker thresholds.
- **Promptness matters.** Because handlers run on the executor loop, a slow handler delays the entire agent. For interactive handlers, read input with a timeout or respect `ctx.Done()`.
