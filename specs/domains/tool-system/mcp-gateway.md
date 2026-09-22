# MCP Gateway

## Role

c0wrk wires sp4rk's MCP (Model Context Protocol) gateway into the orchestration builder: it starts configured MCP servers at startup, discovers their tools, and registers them into the core tool registry. The `Gateway`, `Server`, and `mcp.Tool` lifecycle, transports, and schema sanitization are **sp4rk engine** primitives — see [the sp4rk mcp-gateway spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/mcp-gateway.md).

## Key Files

- `core/builder.go` — `NewOrchestratorBuilder` starts the gateway in `runMCPInit()` (a dedicated goroutine, gated by `mcpDone`, decoupled from `runAsyncInit`) and registers discovered tools into the core registry
- `backend/frontend_api_mcp.go` — `GetMCPStatus` (live per-server status) / `UpdateMCPServers` (persist config + hot-reload, which calls `OrchestratorBuilder.ReconfigureMCP`) surface for the frontend MCP management UI
- `core/tools/registry.go` — exposes the wrapped sp4rk `ToolRegistry` (via `r.ToolRegistry`) on which the sp4rk gateway registers MCP tools (see below)

Engine files (`github.com/v0lka/sp4rk/tools/mcp/gateway.go` `Gateway`, `server.go` `Server`, `mcptool.go` `mcp.Tool`) are documented in [the sp4rk mcp-gateway spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/mcp-gateway.md).

## c0wrk Wiring

### Lifecycle

```
NewOrchestratorBuilder()
│
├─ runMCPInit() (dedicated goroutine, gated by mcpDone; decoupled from initDone):
│   └─ mcp.StartGateway(ctx, cfg, b.registry.ToolRegistry, …)  // Connect + DiscoverTools + RegisterTools (RegisterWithSourceCategory with SourceCategoryMCP; partial failure = non-fatal)
│
├─ Application running...
│   ├─ Tool execution: mcp.Tool.Execute() → server.CallTool(name, input)
│   └─ UpdateMCPServers() → persist + ReconfigureMCP → gateway.Reconfigure (atomic: unregister old, stop, start new)
│
└─ Shutdown:
    └─ StopGateway() → gateway.Stop() → close all server connections
```

`EventBackendReady` fires without waiting for the MCP gateway's async init; MCP tools become available asynchronously. The dedicated `EventMCPReady` (`mcp:ready`) event fires once the startup goroutine completes (success or failure).

### Registration into the Core Registry

MCP tools are wrapped in sp4rk `mcp.Tool` (implements the `Tool` interface) and registered by the sp4rk gateway via `RegisterWithSourceCategory(mcpTool, serverName, SourceCategoryMCP)` on the embedded sp4rk `ToolRegistry` (`b.registry.ToolRegistry`):

- `DefaultPolicy()` → `PolicyUserConfirm` (external tools, conservative default)
- `IsUntrusted()` → `true` (all MCP tool output is wrapped in `&lt;untrusted-content>` tags)
- Source tag: the MCP server's name (e.g. `filesystem`); source category `SourceCategoryMCP`

### Status Reporting (frontend UI)

`gateway.Status()` returns per-server `ServerStatus` (`Name`, `Transport`, `Connected`, `Unhealthy`, `Starting`, `ToolCount`, `Tools`, `Error`), exposed to the frontend MCP management UI via `GetMCPStatus`.

Every **configured** server is always visible in the settings UI, unavailable ones rendered with a red indicator:

- `Gateway.Status()` includes configured servers whose connection/discovery failed (`Connected: false` + last error; kept in the gateway's separate `failedServers` map, never in the live connection map).
- `FrontendAPI.GetMCPStatus` additionally merges names present in the stored config but absent from the gateway status (gateway missing or failed to start, config ahead of a failed reconfigure) as disconnected entries with error `unavailable`.
- While the gateway startup placeholder (`_gateway`, `Starting: true`) is active, the merge is suppressed — availability is unknown; the UI shows "Starting…" and refreshes on `mcp:ready`.

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
      timeout: "60s"       # handshake + default per-call bound
      call_timeout: "30s"  # optional per-call override

    remote-api:
      transport: http
      url: "http://localhost:8080/mcp"
      headers:
        Authorization: "Bearer ${MCP_TOKEN}"
```

Env vars are expanded as `${VAR}`. Transport types (stdio/http), schema sanitization, and server connection behavior are engine concerns — see [the sp4rk mcp-gateway spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/mcp-gateway.md).

### Per-server timeouts

Each server entry accepts two optional duration keys (Go duration strings — a unit suffix is required, e.g. `60s`, `2m`, `500ms`):

- `timeout` — bounds the server's **initialization handshake** (`initialize` + `tools/list`) and is the **default bound for every `tools/call`** on that server. Default **60s**.
- `call_timeout` — optional **per-call override** for a single `tools/call`; unset inherits `timeout`, so a per-call wire timeout is always in effect.

A key that is omitted, empty, unparseable, or non-positive resolves to the built-in **60s** default (empty/unset/invalid → 60s). The UI save path rejects an invalid value up front; on the load path a bad value **fails soft** to the default (with a logged warning) so it never prevents the server from starting. The resolved bounds are captured at connect time, so editing a timeout re-applies it by reconnecting that server (a timeout-only change is reconnect-worthy). The bound mechanics and the unhealthy flag are engine concerns — see [the sp4rk mcp-gateway spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/mcp-gateway.md).

## Invariants

- MCP gateway failure is non-fatal (application starts without MCP tools)
- MCP tools always carry source category `SourceCategoryMCP` (source tag = the server name)
- MCP tools default to `PolicyUserConfirm` (never auto-execute untrusted external tools)
- All MCP tools are untrusted (`IsUntrusted()` returns `true`)
- `ReconfigureMCP()` is atomic: old servers stopped before new ones started
- Every MCP server has a bounded initialization handshake (`initialize` + `tools/list`) and a bounded per-call wire timeout; both default to 60s, and an unset `call_timeout` inherits `timeout` — a server never operates with an unbounded handshake or call
- A server is marked `Unhealthy` after **3 consecutive** `tools/call` timeouts; the mark is advisory and never disconnects the server (a timeout does not kill or close the connection), and a clean call clears it
- Timeout errors surface to the agent loop as a typed error (never swallowed); the server stays connected

## Related Specs

- [sp4rk mcp-gateway](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/mcp-gateway.md) — canonical Gateway/Server/mcp.Tool lifecycle, transports, schema sanitization
- [README.md](README.md) — tool system overview
- [../../architecture/security-model.md](../../architecture/security-model.md) — MCP tool policies
- [../../contracts/backend-core.md](../../contracts/backend-core.md) — ReconfigureMCP wiring
