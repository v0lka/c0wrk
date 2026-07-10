# Tool System

## Purpose

Provides the tool abstraction for the agent: a single `Tool` interface, a thread-safe `ToolRegistry`, the execution pipeline with fail-closed policy enforcement, and metadata for the planner/executor to reason about available tools without holding live references. Tools are the agent's interface to the outside world (filesystem, shell, web, external MCP servers). Host applications extend the SDK by registering built-in tools, custom tools, and MCP-proxied tools into the registry.

## Key Files

- `github.com/v0lka/sp4rk/tools` — `Tool` interface, `BaseTool`, `ToolResult`, `ToolPolicy`, `ToolJudger`, `ToolDescriptor`, `ToolRegistry`, `ParamManager`
- `github.com/v0lka/sp4rk/tools` (registry) — `Register`/`RegisterWithSource`/`RegisterWithSourceCategory`, `Execute` (fail-closed policy enforcement), `List`/`ListFiltered`, MCP shadowing protection
- `github.com/v0lka/sp4rk/tools` (context helpers) — `WithWorkspacePath`, `WithTempDir`, `WithTaskContext`
- `github.com/v0lka/sp4rk/tools/builtins` — built-in tool catalog
- `github.com/v0lka/sp4rk/tools/mcp` — MCP gateway (dynamic tool discovery/proxying)
- `github.com/v0lka/sp4rk/security` — untrusted-content wrapping for tool output

## Core Types

```go
// Every tool — built-in, custom, or MCP-proxied — implements Tool.
type Tool interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage
    Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
    DefaultPolicy() ToolPolicy
    IsUntrusted() bool
}

type ToolResult struct {
    Content string
    IsError bool
}

type ToolPolicy int
const (
    PolicyAlwaysAllow ToolPolicy = iota
    PolicyAlwaysDeny
    PolicyUserConfirm
)

// Optional per-tool safety heuristic.
type ToolJudger interface {
    Judge(ctx context.Context, input json.RawMessage) (allow bool, reasoning string)
}

// Metadata-only representation for the planner/executor.
type ToolDescriptor struct {
    Name           string
    Description    string
    InputSchema    json.RawMessage
    Source         string             // "core" | <server-name>
    SourceCategory ToolSourceCategory // "core" | "mcp"
}
```

## Flow

```
ToolRegistry.Execute(ctx, name, input)
│
├─ 1. Look up the tool by name → not found ⇒ error ToolResult
├─ 2. Apply parameter injection (if a ParamManager is configured)
└─ 3. Resolve effective policy (per-tool override, else the tool's DefaultPolicy):
      ├─ PolicyAlwaysAllow  → execute (escalate to confirmation if a ToolJudger flags it)
      ├─ PolicyAlwaysDeny   → reject with an error result
      └─ PolicyUserConfirm  → consult ConfirmFunc; DENIED (fail-closed) if none configured
```

The executor calls the registry through the narrow `ToolExecutor` interface (`Execute`, `GetToolSource`, `IsToolUntrusted`). `GetToolSource` returns `"core"` or the MCP server name; `IsToolUntrusted` reports whether a tool's output is from an untrusted source (`tool.IsUntrusted()` true **or** MCP source category) — driving the `<untrusted-content>` wrapping of observations.

## Invariants

- Tool names are unique within the registry.
- The registry is thread-safe (`sync.RWMutex`).
- `Execute` is **fail-closed**: a `PolicyUserConfirm` tool with no `ConfirmFunc` configured is DENIED — mutating tools never execute silently.
- A `PolicyAlwaysAllow` tool may implement `ToolJudger` to escalate a call to confirmation; a denied escalation is also fail-closed.
- An MCP tool may **not** shadow an already-registered non-MCP tool of the same name (`RegisterWithSourceCategory` errors; the legacy path logs and skips). A built-in tool can always replace an MCP tool; an MCP server re-registering its own tools is allowed.
- MCP tools default to `PolicyUserConfirm` and always report `IsUntrusted() == true`.
- Built-in untrusted tools set `Untrusted: true` on their `BaseTool`.

## Configuration

Policy is set per tool. Override a tool's effective policy explicitly, or relax tools for non-interactive use:

```go
registry.SetPolicyOverride("bash_exec", tools.PolicyAlwaysAllow) // deliberate opt-in
registry.ClearPolicyOverride("bash_exec")
registry.SetConfirmFunc(myConfirmFunc)   // consulted for PolicyUserConfirm + judge escalation
registry.SetParamManager(pm)             // execution-time parameter injection
```

Stage 1 truncation limits are configured per tool on the executor (see [../orchestration/executor.md](../orchestration/executor.md)); tool result budget (Stage 2) is configured on the executor's `ToolResultBudget`.

## Extension Points

- **New built-in tool**: embed `tools.BaseTool`, implement `Execute`, optionally implement `ToolJudger`, set `Untrusted: true` for external-output tools, and register. See [builtins.md](builtins.md).
- **Custom policy enforcement layer**: hosts may wrap the registry and shadow `Execute` (calling `tool.Execute` directly after their own checks); the SDK-level enforcement only applies to calls routed through `ToolRegistry.Execute`.
- **`ParamManager`**: transform tool input at execution time (e.g. inject a `project` parameter into MCP tools); share one instance with the MCP gateway so schema sanitization and injection agree on the auto-injected parameter set.
- **MCP servers**: add external tools without writing Go code per server. See [mcp-gateway.md](mcp-gateway.md).

## Related Specs

- [builtins.md](builtins.md) — built-in tool catalog and extension guide
- [mcp-gateway.md](mcp-gateway.md) — MCP server lifecycle and dynamic tool discovery
- [../orchestration/executor.md](../orchestration/executor.md) — tool execution, truncation, caching, trust classification
- [../memory/compaction.md](../memory/compaction.md) — `ToolResultCache` and history mutation
