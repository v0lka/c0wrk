# MCP Gateway

## Role

Manages connections to external MCP (Model Context Protocol) servers, discovers their tools at runtime via the MCP `tools/list` call, and proxies tool execution through the `ToolRegistry`. With it, an agent can call tools hosted by databases, APIs, filesystems, browsers, or any process that speaks MCP — without writing custom Go code per server.

## Key Files

- `github.com/v0lka/sp4rk/tools/mcp` — `Gateway`, `GatewayConfig`, `ServerEntry`, `StartGateway`, `Server`, `Tool`, `SchemaSanitizer`, `ServerStatus`, error types (`StartError`/`StopError`/`ReconfigureError`)
- `github.com/v0lka/sp4rk/tools` — `ToolRegistry`, `RegisterWithSource`, `RegisterWithSourceCategory`, `UnregisterBySource`, `ParamManager`

## Behavior

The package is organized around three layers: `Gateway` (manages many servers), `Server` (one connection), and `Tool` (wraps a single MCP tool as a `tools.Tool`).

### Lifecycle

```
StartGateway(ctx, cfg, registry, expandEnv, logger)
│
├─ 1. Expand ${VAR} references in every ServerEntry; build ServerConfig values.
├─ 2. Create a Gateway; store config, default work dir, schema sanitizer, logger.
├─ 3. gateway.Start(ctx, configs):
│      for each server:
│        ├─ connect (spawn process or HTTP connect)
│        ├─ DiscoverTools (MCP tools/list)
│        └─ on failure: collect error, continue with remaining servers
├─ 4. gateway.RegisterTools(registry):
│      for each discovered tool:
│        ├─ wrap as tools.Tool via NewTool
│        └─ registry.RegisterWithSource(tool, serverName)
└─ returns (nil, nil) when no servers are configured — safe to call unconditionally
```

`StartGateway` returns `(nil, nil)` when no servers are configured. `Gateway.Start` collects per-server errors and continues connecting to the rest; if any server fails it returns a `*StartError` holding all individual errors, but `StartGateway` still returns the gateway (with whatever servers connected) so the agent keeps working.

### Transports

| Transport | Description | Config fields |
| --------- | ----------- | ------------- |
| `stdio` (default) | Spawn a local subprocess; communicate over stdin/stdout | `Command`, `Args`, `Env`, `WorkDir` |
| `http` | Connect to a remote server over HTTP | `URL`, `Headers`, `HTTPClient` |

For HTTP, the client first tries the **Streamable HTTP** transport and falls back to **SSE** (Server-Sent Events) if initialization fails — compatible with both modern and legacy MCP HTTP servers.

### Environment variable expansion

`${VAR}` references may appear in `Env` values (stdio), `URL` (http), and `Headers` values (http). Expansion is performed by the `expandEnv` function passed to `StartGateway` (typically `os.ExpandEnv`, but a custom function can inject secrets from a vault). Expansion happens **before** the server starts, and the gateway persists the *expanded* config so subsequent reconfigurations compare against post-expansion values (avoiding spurious reconnects).

### Tool discovery & MCP Tool characteristics

After connecting, each server is queried with `tools/list`. Returned tools become `ToolInfo` records wrapped by `NewTool` into a `Tool` implementing `tools.Tool`, and registered with the server name as `Source`. From the executor's perspective, MCP tools are indistinguishable from built-in tools. Every MCP tool wrapper has fixed behaviors:

- `DefaultPolicy()` → `PolicyUserConfirm` (conservative default — remote, opaque tools).
- `IsUntrusted()` → always `true` (output from external servers may be adversarial).
- `Judge()` → always `(false, "")`, deferring to the LLM Judge (the gateway cannot inspect remote semantics).
- `Execute()` → calls the MCP server's `tools/call`, extracts text content (joins it; marshals structured content to JSON when no text is present).

### SchemaSanitizer

`SchemaSanitizer func(source string, schema json.RawMessage) json.RawMessage` transforms an input schema before it is exposed to the LLM (applied in `NewTool`). The typical use is stripping auto-injected parameters (e.g. `project`) that a `ParamManager` adds at execution time. Share one `ParamManager` instance between the gateway (`SanitizeSchema`) and the registry (`InjectParams`) so both sides agree on the auto-injected set.

### Reconfigure

`Gateway.Reconfigure(ctx, newConfig, registry, expandEnv, logger)` updates server connections based on a new config while preserving unchanged connections: removed servers are unregistered (`UnregisterBySource`) and closed; added servers are connected/discovered/registered; changed servers get a fresh connection; unchanged servers are left alive. Config comparison uses the *expanded* config.

### Status & introspection

`Status()` returns per-server `ServerStatus` (name, transport, connected, tool count, tools, error), sorted by name for deterministic output. `ServerNames()`, `ToolCount()`, and `GetServer(name)` provide further introspection.

## Error Handling

MCP failures are **non-fatal** — a single broken server never prevents the agent from running with the remaining tools.

- `Gateway.Start` collects per-server errors into a `*StartError` and continues.
- `StartGateway` logs start errors as warnings and still returns the gateway.
- `Gateway.Stop` returns a `*StopError` if any server fails to close cleanly.
- `Gateway.Reconfigure` returns a `*ReconfigureError` for failed operations.
- `Gateway.Stop()` always attempts graceful close of all connections.

## Invariants

- MCP gateway failure is non-fatal (the application runs without MCP tools).
- MCP tools always have source tag `<server_name>` and `SourceCategory == MCP`.
- MCP tools default to `PolicyUserConfirm` and always report `IsUntrusted() == true`.
- An MCP tool may never shadow an already-registered non-MCP tool; an MCP server re-registering its own tools is allowed.
- `Reconfigure` is additive/preserving: unchanged servers are not reconnected.
- `Stop` always attempts graceful close.

## Related Specs

- [README.md](README.md) — tool system overview and policy enforcement
- [builtins.md](builtins.md) — built-in tool catalog
- [../orchestration/executor.md](../orchestration/executor.md) — MCP cache TTL and untrusted wrapping
- [../memory/compaction.md](../memory/compaction.md) — MCP-sourced cache entries expire on TTL
