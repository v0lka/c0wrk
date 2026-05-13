# Tool System

## Purpose

Provides tool infrastructure for the agent: discovery, registration, policy enforcement, and execution. Tools are the agent's interface to the outside world (filesystem, shell, web, vector index).

## Key Files

- `sdk/tools/tool.go` — Tool interface, ToolDescriptor, ToolPolicy, ToolResult
- `sdk/tools/registry.go` — SDK ToolRegistry (basic get/list/execute)
- `core/tools/registry.go` — core ToolRegistry (wraps SDK, adds policies/judge/hooks)
- `core/tools/builtin_registration.go` — RegisterBuiltinTools function
- `core/tools/judge.go` — ToolJudge (LLM-based safety evaluation)
- `core/tools/mcp/gateway.go` — MCP Gateway (dynamic tool discovery)

## Core Types

```go
// Tool interface (sdk/tools/tool.go)
type Tool interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage
    Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
    DefaultPolicy() ToolPolicy
}

// Tool metadata for planner/executor (no execution capability)
type ToolDescriptor struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    InputSchema json.RawMessage `json:"input_schema"`
    Source      string          `json:"source"` // "core" | "mcp:<server>"
}

// Execution result
type ToolResult struct {
    Content string
    IsError bool
}

// Security policies
type ToolPolicy int
const (
    PolicyAlwaysAllow  ToolPolicy = iota
    PolicyAlwaysDeny
    PolicyUserConfirm
)
```

## Two-Layer Registry

```
┌─────────────────────────────────────────────────────┐
│  core/tools.ToolRegistry                            │
│  (policy enforcement, judge, hooks, filters)        │
│                                                     │
│  ┌─────────────────────────────────────────────┐   │
│  │  sdk/tools.ToolRegistry (embedded)           │   │
│  │  (basic store: Register, Get, List, Execute) │   │
│  └─────────────────────────────────────────────┘   │
│                                                     │
│  + resolvePolicy()      policy resolution chain     │
│  + confirmAndExecute()  user confirmation flow      │
│  + SetJudge()           LLM safety evaluation       │
│  + SetPreExecuteHook()  pre-execution gate          │
│  + SetToolFilter()      registration filter         │
│  + SetParamInjector()   input transformation        │
│  + RegisterWithSource() filtered registration       │
└─────────────────────────────────────────────────────┘
```

## Flow

```
ToolRegistry.Execute(ctx, name, input)
│
├─ 1. Lookup tool by name → not found? return error result
├─ 2. Internal tool? → execute immediately (bypass all checks)
├─ 3. PreExecuteHook (blocking gate, e.g., index ready)
├─ 4. ParamInjector (transform input, e.g., scope paths)
├─ 5. resolvePolicy: per-tool > skill > default > tool's own
├─ 6. Auto-approval: all paths in workspace/temp? → execute
└─ 7. Apply policy:
      ├─ AlwaysAllow → execute (unless ToolJudger flags)
      ├─ AlwaysDeny → error result
      └─ UserConfirm → confirmFunc blocks → execute or deny
```

## Invariants

- Tool names are unique within the registry
- Internal tools (ask_user, finish, list_step_outputs, read_step_output) bypass all checks
- MCP tools are tagged with source `mcp:<server_name>`
- Core built-in tools are tagged with source `core`
- Tool filter can silently reject registration (no error returned)
- The registry is thread-safe (sync.RWMutex)

## Configuration

From `config.yaml`:

```yaml
security:
  default_policy: "user_confirm"
  tool_policies:
    bash_exec: { policy: "user_confirm" }
    write_file: { policy: "always_allow" }

toolLimits:
  readDefaultLines: 2000 # max lines per read call
  readMaxLineLength: 2000 # max characters per line
  readMaxBytes: 51200 # total output cap (50 KB)
  ripgrepMaxResults: 200 # max matches for ripgrep
  ripgrepMaxLineLength: 2000 # max chars per ripgrep line
  globMaxResults: 200 # max glob results
  fileSearchMaxMatches: 100 # max matches for file content search
  webSearchMaxResults: 5 # max web search results
  webFetchMaxBodySize: 102400 # max response body size (100 KB)

timeouts:
  bashMaxTimeout: 120 # seconds
  bashWaitDelay: 5 # seconds
  ripgrepTimeout: 60 # seconds
  webFetchTimeout: 30 # seconds
  webSearchTimeout: 30 # seconds
  persistenceTimeout: 5 # seconds
```

Note: `security.*` keys use `snake_case`; `toolLimits.*` and `timeouts.*` keys use `camelCase`. Both conventions match the yaml struct tags in `backend/config/config.go`.

## Extension Points

- `PreExecuteHook` — block until preconditions met (e.g., vector index ready)
- `ToolFilter` — reject tools during registration (e.g., filter MCP tools by server)
- `ParamInjector` — transform tool input (e.g., inject workspace path for MCP tools)
- `ToolJudger` interface — per-tool safety evaluation (implement on tool struct)
- New built-in tools: implement `Tool` interface, register in `RegisterBuiltinTools`

## Related Specs

- [builtins.md](builtins.md) — catalog of built-in tools
- [mcp-gateway.md](mcp-gateway.md) — dynamic MCP tool lifecycle
- [../../architecture/security-model.md](../../architecture/security-model.md) — policy details
