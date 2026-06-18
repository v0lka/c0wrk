# Tool System

## Purpose

Provides tool infrastructure for the agent: discovery, registration, policy enforcement, and execution. Tools are the agent's interface to the outside world (filesystem, shell, web, vector index).

## Key Files

- `sdk/tools/tool.go` — Tool interface, ToolDescriptor, ToolPolicy, ToolResult, BaseTool
- `sdk/tools/registry.go` — SDK ToolRegistry (basic get/list/execute/ListFiltered)
- `sdk/security/wrap.go` — Content wrapping for untrusted tool output (indirect prompt injection defense)
- `core/tools/registry.go` — core ToolRegistry (wraps SDK, adds policies/judge/hooks/symlink check)
- `core/tools/symlink.go` — symlink detection, traversal, confirmation gating
- `core/tools/builtin_registration.go` — RegisterBuiltinTools function
- `core/tools/judge.go` — ToolJudge (LLM-based safety evaluation)
- `core/tools/mcp/gateway.go` — MCP Gateway (dynamic tool discovery)
- `core/toolnames.go` — tool name constants, NoProjectDisabledTools, NoProjectBashBlacklist

## Core Types

```go
// Tool interface (sdk/tools/tool.go)
type Tool interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage
    Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
    DefaultPolicy() ToolPolicy
    IsUntrusted() bool  // Returns true if tool output must be wrapped in <untrusted-content>
}

// BaseTool provides defaults for tool implementations
type BaseTool struct {
    Name        string
    Description string
    InputSchema json.RawMessage
    Untrusted   bool // Set to true for tools whose output should be delimited
}
```

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
│  + resolvePolicy()        policy resolution chain     │
│  + confirmAndExecute()    user confirmation flow      │
│  + SetJudge()             LLM safety evaluation        │
│  + SetPreExecuteHook()    pre-execution gate          │
│  + SetToolFilter()         registration filter         │
│  + SetParamInjector()     input transformation        │
│  + RegisterWithSource()   filtered registration       │
│  + SetDisabledTools()     block tools by name (e.g., No Project) │
│  + DisabledTools()        read disabled-tool set       │
│  + SetExtraBashBlacklist()  runtime bash command blacklist       │
└─────────────────────────────────────────────────────┘
```

## Flow

```
ToolRegistry.Execute(ctx, name, input)
│
├─ 1. Lookup tool by name → not found? return error result
├─ 2. Disabled tool (No Project mode)? → return error result (applies to ALL tools including internal)
├─ 3. Extra bash blacklist match? → return error result (per-session, e.g., No Project blocks dev commands)
├─ 4. Internal tool? → execute immediately (bypass remaining policy/judge/hook checks)
├─ 5. PreExecuteHook (blocking gate, e.g., index ready)
├─ 6. ParamInjector (transform input, e.g., scope paths)
├─ 7. Symlink Gate: detect symlinks in input paths
│      ├─ Symlinks found → force confirmation (unless always_deny)
│      └─ No symlinks → continue
├─ 8. resolvePolicy: per-tool > skill > default > tool's own
├─ 9. Auto-approval: all paths in workspace/temp? → execute
└─ 10. Apply policy:
      ├─ AlwaysAllow → execute (unless ToolJudger flags)
      ├─ AlwaysDeny → error result
      └─ UserConfirm → confirmFunc blocks → execute or deny
```

## Invariants

- Tool names are unique within the registry
- Internal tools (ask_user, finish, list_step_outputs, read_step_output, read_skill_resource, search_facts, semantic_search, set_step_status, store_fact, tool_result_read) bypass policy and judge checks, but disabled-tool and extra-bash-blacklist checks apply to all tools including internal ones
- The symlink gate runs before policy resolution for every non-internal tool call
- Symlinks in workspace or temp dir that are OS-level infrastructure (e.g., macOS /tmp → /private/tmp) are filtered out and do not trigger confirmation
- MCP tools are tagged with source `mcp`
- Core built-in tools are tagged with source `core`
- Tool filter can silently reject registration (no error returned)
- Disabled tools are blocked at execution time; the registry's `SetDisabledTools` and `DisabledTools` methods deep-copy the map to prevent concurrent mutation
- Extra bash blacklist patterns are compiled at set time and checked against `bash_exec` command input; the check runs before the internal-tool bypass
- The registry is thread-safe (sync.RWMutex)
- Untrusted tool output (IsUntrusted() == true) is wrapped in <untrusted-content> tags before entering the LLM context
- All MCP tools are untrusted; built-in untrusted tools set `Untrusted: true` on their `BaseTool` (classifications: web_search, web_fetch, bash_exec, ripgrep, glob, read_file)
- `ToolExecutor.IsToolUntrusted(name)` reports trust status by delegating to `Tool.IsUntrusted()` plus MCP source check

## Configuration

From `config.yaml`:

```yaml
security:
  default_policy: "user_confirm"
  tool_policies:
    bash_exec: { policy: "user_confirm" }
    write_file: { policy: "always_allow" }

toolLimits:
  perToolTruncation:
    read_file: { maxLines: 50000 }
    ripgrep: { maxLines: 5000 }
    glob: { maxLines: 5000 }
    list_directory: { maxLines: 5000 }
    web_fetch: { maxBytes: 2097152 }
    bash_exec: { maxLines: 10000 }

toolResultBudget:
  cacheTTLSeconds: 300 # seconds before cache entries expire

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
- New built-in tools: implement `Tool` interface, set `Untrusted: true` on `BaseTool` if output comes from external sources, register in `RegisterBuiltinTools`
- To disable tools at runtime (e.g., for No Project mode): call `SetDisabledTools(names)` on the core registry; all tools including internal ones are blocked at execution time
- To add runtime bash command restrictions: call `SetExtraBashBlacklist(patterns)` on the core registry; patterns are compiled regexps checked before `bash_exec` execution

## Related Specs

- [builtins.md](builtins.md) — catalog of built-in tools
- [mcp-gateway.md](mcp-gateway.md) — dynamic MCP tool lifecycle
- [../../architecture/security-model.md](../../architecture/security-model.md) — policy details
