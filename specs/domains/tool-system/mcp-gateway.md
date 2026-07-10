# MCP Gateway

## Role

c0wrk wires sp4rk's MCP (Model Context Protocol) gateway into the orchestration builder: it starts configured MCP servers at startup, discovers their tools, and registers them into the core tool registry. The `Gateway`, `Server`, and `mcp.Tool` lifecycle, transports, and schema sanitization are **sp4rk engine** primitives — see [the sp4rk mcp-gateway spec](../../../sdk/specs/domains/tool-system/mcp-gateway.md).

## Key Files

- `core/builder.go` — `NewOrchestratorBuilder` starts the gateway in `runAsyncInit()` (goroutine) and registers discovered tools into the core registry
- `backend/frontend_api_mcp.go` (or matching `frontend_api_*.go`) — `GetMCPStatus` / `ReconfigureMCP` surface for the frontend MCP management UI
- `core/tools/registry.go` — `RegisterWithSource(mcpTool, "mcp:"+serverName)` registers MCP tools with the `mcp` source tag

Engine files (`github.com/v0lka/sp4rk/tools/mcp/gateway.go` `Gateway`, `server.go` `Server`, `mcptool.go` `mcp.Tool`) are documented in [the sp4rk mcp-gateway spec](../../../sdk/specs/domains/tool-system/mcp-gateway.md).

## c0wrk Wiring

### Lifecycle

```
NewOrchestratorBuilder()
│
├─ runAsyncInit() (goroutine):
│   ├─ gateway.Start(ctx, configs)        // per-server Connect + DiscoverTools (partial failure = non-fatal)
│   └─ gateway.RegisterTools(registry)    // sanitize schema + RegisterWithSource("mcp:"+serverName)
│
├─ Application running...
│   ├─ Tool execution: mcp.Tool.Execute() → server.CallTool(name, input)
│   └─ ReconfigureMCP() → Stop + Start with new config (atomic)
│
└─ Shutdown:
    └─ gateway.Stop() → close all server connections
```

`EventBackendReady` fires without waiting for the MCP gateway's async init; MCP tools become available asynchronously.

### Registration into the Core Registry

MCP tools are wrapped in sp4rk `mcp.Tool` (implements the `Tool` interface) and registered via the core registry's `RegisterWithSource`:

- `DefaultPolicy()` → `PolicyUserConfirm` (external tools, conservative default)
- `IsUntrusted()` → `true` (all MCP tool output is wrapped in `&lt;untrusted-content>` tags)
- Source tag: `mcp:<server_name>`

### Status Reporting (frontend UI)

`gateway.Status()` returns per-server `ServerStatus` (`Name`, `Transport`, `Connected`, `ToolCount`, `Tools`, `Error`), exposed to the frontend MCP management UI via `GetMCPStatus`.

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

Env vars are expanded as `${VAR}`. Transport types (stdio/http), schema sanitization, and server connection behavior are engine concerns — see [the sp4rk mcp-gateway spec](../../../sdk/specs/domains/tool-system/mcp-gateway.md).

## Invariants

- MCP gateway failure is non-fatal (application starts without MCP tools)
- MCP tools always have source tag `mcp:<server_name>`
- MCP tools default to `PolicyUserConfirm` (never auto-execute untrusted external tools)
- All MCP tools are untrusted (`IsUntrusted()` returns `true`)
- `ReconfigureMCP()` is atomic: old servers stopped before new ones started

## Related Specs

- [sp4rk mcp-gateway](../../../sdk/specs/domains/tool-system/mcp-gateway.md) — canonical Gateway/Server/mcp.Tool lifecycle, transports, schema sanitization
- [README.md](README.md) — tool system overview
- [../../architecture/security-model.md](../../architecture/security-model.md) — MCP tool policies
- [../../contracts/backend-core.md](../../contracts/backend-core.md) — ReconfigureMCP wiring
