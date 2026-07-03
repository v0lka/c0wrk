# MCP Gateway

## Role

Manages connections to external MCP (Model Context Protocol) servers, discovers their tools at runtime, and proxies tool execution through the core tool registry.

## Key Files

- `sdk/tools/mcp/gateway.go` — Gateway struct (Start, Stop, RegisterTools, Status) + config types
- `sdk/tools/mcp/server.go` — Server struct (Connect, DiscoverTools, CallTool, Close)
- `sdk/tools/mcp/mcptool.go` — `mcp.Tool` (wraps MCP tool as sdk Tool interface; struct is `Tool`, not `MCPTool`)

## Behavior

### Lifecycle

```
NewOrchestratorBuilder()
│
├─ runAsyncInit() (goroutine):
│   ├─ gateway.Start(ctx, configs)
│   │   ├─ For each configured server:
│   │   │   ├─ server.Connect(ctx, cfg) — spawn process or HTTP connect
│   │   │   ├─ server.DiscoverTools(ctx) — list available tools
│   │   │   └─ On failure: log error, continue to next server
│   │   └─ Return aggregate error (partial failure = non-fatal)
│   │
│   └─ gateway.RegisterTools(registry)
│       └─ For each server's tools:
│           ├─ Sanitize schema (remove auto-injected params)
│           └─ registry.RegisterWithSource(mcpTool, "mcp:"+serverName)
│
├─ Application running...
│   ├─ Tool execution: mcp.Tool.Execute() → server.CallTool(name, input)
│   └─ ReconfigureMCP() → Stop + Start with new config
│
└─ Shutdown:
    └─ gateway.Stop() → close all server connections
```

### Transport Types

| Transport | Description                                       | Config Fields                        |
| --------- | ------------------------------------------------- | ------------------------------------ |
| `stdio`   | Spawn child process, communicate via stdin/stdout | `command`, `args`, `env`, `work_dir` |
| `http`    | Connect to HTTP server                            | `url`, `headers`                     |

### Tool Registration

MCP tools are wrapped in `mcp.Tool` struct (in `sdk/tools/mcp/mcptool.go`) that implements `sdk/tools.Tool`:

- `Name()` — prefixed or as-is from MCP server
- `Description()` — from MCP tool metadata
- `InputSchema()` — JSON schema from MCP (sanitized)
- `Execute()` — proxies to `server.CallTool()`
- `DefaultPolicy()` — `PolicyUserConfirm` (MCP tools are external, conservative default)
- `IsUntrusted()` — returns `true` (all MCP tool output is wrapped in `<untrusted-content>` tags)
- `Judge()` — implements `ToolJudger`: defers to the LLM judge (same evaluation flow as built-in tools)

### Schema Sanitization

The gateway's `SchemaSanitizer` removes parameters from tool schemas that are auto-injected at execution time (via `ParamManager`). This prevents the LLM from seeing and filling parameters that will be overwritten.

### Status Reporting

`gateway.Status()` returns per-server `ServerStatus`:

- `Name` — server name
- `Transport` — transport type (stdio, sse, http)
- `Connected` (bool)
- `ToolCount` — number of discovered tools
- `Tools` — list of tool names (`[]string`)
- `Error` — error message (if failed)

Used by frontend MCP management UI.

## Error Handling

- Individual server connection failure → logged, other servers continue
- Tool discovery failure → server closed, logged as error
- Gateway startup failure → stored as `gatewayErr`, non-fatal (app starts without MCP tools)
- Tool execution failure → returned as ToolResult with IsError=true
- Server process crash → connection marked as broken (reconnect on next ReconfigureMCP)

## Configuration

From `config.yaml`:

```yaml
mcp:
  servers:
    filesystem:
      transport: stdio
      command: "npx"
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/path"]
      env:
        NODE_PATH: "/usr/local/lib/node_modules"

    remote-api:
      transport: http
      url: "http://localhost:8080/mcp"
      headers:
        Authorization: "Bearer ${MCP_TOKEN}"
```

## Invariants

- MCP gateway failure is non-fatal (application starts without MCP tools)
- MCP tools always have source tag `mcp:<server_name>`
- MCP tools default to `PolicyUserConfirm` (never auto-execute untrusted external tools)
- Schema sanitization happens at registration time (not per-call)
- Gateway.Stop() always attempts graceful close of all server connections
- ReconfigureMCP() is atomic: old servers stopped before new ones started
- All MCP tools are untrusted (`IsUntrusted()` returns `true`); their output is wrapped in `<untrusted-content>` tags before entering the LLM context

## Related Specs

- [README.md](README.md) — tool system overview
- [../../architecture/security-model.md](../../architecture/security-model.md) — MCP tool policies
- [../../contracts/backend-core.md](../../contracts/backend-core.md) — ReconfigureMCP wiring
