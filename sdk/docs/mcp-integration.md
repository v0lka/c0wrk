# MCP Integration

The `mcp` package provides Model Context Protocol (MCP) server integration and tool proxying. MCP is an open protocol for connecting external tool servers to LLM-powered agents. With it, an agent can call tools hosted by databases, APIs, filesystems, browsers, or any process that speaks MCP — without writing custom Go code for each one.

```go
import "github.com/v0lka/sp4rk/tools/mcp"
```

## Overview

The package is organized around three layers:

| Type | Role |
| --- | --- |
| `Gateway` | Manages connections to multiple MCP servers and registers their tools into a `ToolRegistry`. |
| `Server` | Represents a single connection to one MCP server process (stdio or HTTP). |
| `Tool` | Wraps a single MCP server tool as a `tools.Tool` so it is indistinguishable from a built-in tool to the executor. |

At startup, the gateway connects to every configured server, discovers each server's tools via the MCP `tools/list` call, and registers them in the shared `ToolRegistry`. From that point on, MCP tools participate in the agent loop exactly like built-in tools.

## ServerEntry

`ServerEntry` describes how to launch or reach a single MCP server. It is the declarative configuration type you place in `GatewayConfig.Servers`.

```go
type ServerEntry struct {
    Transport string            // "stdio" | "http"; default "stdio"
    Command   string            // stdio: command to execute
    Args      []string          // stdio: command arguments
    Env       map[string]string // stdio: environment variables (values may contain ${VAR})
    URL       string            // http: server URL (may contain ${VAR})
    Headers   map[string]string // http: custom headers (values may contain ${VAR})
    WorkDir   string            // stdio: working directory for the server process
}
```

- **`Transport`** — selects the connection mechanism. `"stdio"` (the default when empty) launches a local subprocess; `"http"` connects to a remote server over HTTP.
- **`Command` / `Args` / `Env` / `WorkDir`** — stdio-only fields describing the subprocess to launch.
- **`URL` / `Headers`** — http-only fields describing the remote endpoint.
- Environment variable references (`${VAR}`) may appear in `Env` values, `URL`, and `Headers` values. They are expanded before the server is started (see [Environment variable expansion](#environment-variable-expansion)).

## GatewayConfig

`GatewayConfig` holds the raw MCP server entries for gateway initialization:

```go
type GatewayConfig struct {
    Servers        map[string]ServerEntry
    DefaultWorkDir string            // fallback working directory for stdio servers
    HTTPClient     *http.Client      // optional proxy-configured client for HTTP servers
    SchemaSanitizer SchemaSanitizer  // optional schema transform applied to every MCP tool
}
```

- **`Servers`** — a map from server name to `ServerEntry`. The name is the key used as the tool `Source` tag and for unregistration.
- **`DefaultWorkDir`** — applied to any stdio server that does not specify its own `WorkDir`.
- **`HTTPClient`** — optional custom HTTP client (e.g. proxy-configured) used for HTTP-transport servers.
- **`SchemaSanitizer`** — transforms tool input schemas before they are exposed to the LLM (see [SchemaSanitizer](#schemasanitizer)).

## Gateway lifecycle

### StartGateway

`StartGateway` is the one-call entry point: it creates a gateway, configures it, starts all servers, and registers their tools into the registry.

```go
func StartGateway(
    ctx context.Context,
    cfg GatewayConfig,
    registry *tools.ToolRegistry,
    expandEnv func(string) string,
    logger *slog.Logger,
) (*Gateway, error)
```

`expandEnv` resolves `${VAR}` references in environment values, URLs, and headers. `StartGateway` returns `(nil, nil)` when no servers are configured, so calling it unconditionally is safe.

Internally it:

1. Expands environment variables in every `ServerEntry` and builds `ServerConfig` values.
2. Creates a `Gateway` and stores the config, default work dir, schema sanitizer, and logger.
3. Calls `gateway.Start` to connect to every server and discover its tools.
4. Calls `gateway.RegisterTools` to register all discovered tools into the registry.

```go
gateway, err := mcp.StartGateway(ctx, mcp.GatewayConfig{
    Servers: map[string]mcp.ServerEntry{
        "filesystem": {
            Transport: "stdio",
            Command:   "npx",
            Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", workDir},
        },
    },
    DefaultWorkDir: workDir,
}, registry, os.ExpandEnv, logger)
if err != nil {
    log.Printf("MCP start errors: %v", err) // non-fatal — see Error handling
}
```

### Start

`Gateway.Start(ctx, configs)` connects to all configured servers and discovers their tools. It applies the default working directory to servers that do not specify one, connects each server, and runs tool discovery. If a server fails to connect or discover tools, the error is collected and the gateway continues with the remaining servers.

### RegisterTools

`Gateway.RegisterTools(registry)` wraps every discovered tool as a `Tool` (via `NewTool`) and registers it with `registry.RegisterWithSource(mcpTool, name)`, tagging it with the server name as its source. This source tag is what enables `UnregisterBySource` to cleanly remove a server's tools later.

### Stop

`Gateway.Stop()` gracefully shuts down all MCP server connections and clears the internal server map. It returns a `*StopError` if any server fails to close cleanly. Always call `Stop` (or rely on the framework's shutdown) to release subprocesses and HTTP connections.

```go
defer func() { _ = gateway.Stop() }()
```

## Stdio transport

When `Transport` is `"stdio"` (the default), the gateway launches a local subprocess and communicates with it over stdin/stdout using the MCP protocol.

- **`Command`** is the executable to run (e.g. `"npx"`, `"node"`, `"python"`).
- **`Args`** are the command-line arguments passed to it.
- **`Env`** is merged onto the current process environment (`os.Environ()`).
- **`WorkDir`** sets the subprocess's working directory (falls back to `DefaultWorkDir`).

The subprocess is spawned with `exec.CommandContext`, inheriting the merged environment and the configured working directory. Communication happens over the process's stdin/stdout pipes via the MCP client library.

```go
"filesystem": {
    Transport: "stdio",
    Command:   "npx",
    Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", rootDir},
    Env:       map[string]string{"NODE_ENV": "production"},
    WorkDir:   rootDir,
},
```

## HTTP transport

When `Transport` is `"http"`, the gateway connects to a remote MCP server over HTTP. The `URL` field is required.

- **`URL`** — the server endpoint (may contain `${VAR}` references).
- **`Headers`** — custom HTTP headers (e.g. `Authorization`), with values that may contain `${VAR}` references.
- **`HTTPClient`** — an optional custom HTTP client (e.g. proxy-configured).

The HTTP client first tries the **Streamable HTTP** transport; if that fails to initialize, it falls back to **SSE** (Server-Sent Events). This fallback makes the gateway compatible with both modern and legacy MCP HTTP servers.

```go
"api": {
    Transport: "http",
    URL:       "http://localhost:3001/mcp",
    Headers:   map[string]string{"Authorization": "Bearer ${MCP_TOKEN}"},
},
```

## Environment variable expansion

`${VAR}` references may appear in three places within a `ServerEntry`:

- `Env` values (stdio)
- `URL` (http)
- `Headers` values (http)

Expansion is performed by the `expandEnv` function passed to `StartGateway` (or `Reconfigure`). The caller controls how variables are resolved — typically with `os.ExpandEnv`, but a custom function can inject secrets from a vault or config store without putting them in the process environment.

Expansion happens **before** the server is started, so the running subprocess and HTTP client only ever see resolved values. The gateway also persists the *expanded* config so that subsequent reconfigurations compare against post-expansion values (avoiding false-positive "config changed" detections that would bounce servers every time).

## Tool discovery

Tool discovery is automatic at startup. After connecting, each server is queried with the MCP `tools/list` call. The returned tools are converted to internal `ToolInfo` records:

```go
type ToolInfo struct {
    Name        string
    Description string
    InputSchema json.RawMessage
}
```

Each `ToolInfo` is wrapped by `NewTool` into a `Tool` that implements the `tools.Tool` interface and registered in the `ToolRegistry` with the server name as its source. From the executor's perspective, MCP tools are indistinguishable from built-in tools — they appear in `registry.List()` with their `Source` set to the server name.

### MCP Tool characteristics

Every MCP tool wrapper has these fixed behaviors:

- **`DefaultPolicy()`** returns `PolicyUserConfirm` — a conservative default because MCP tools are remote and opaque.
- **`IsUntrusted()`** always returns `true` — MCP tool output comes from external servers and may contain adversarial content, so it is treated as untrusted for prompt-injection defense.
- **`Judge()`** (implements `ToolJudger`) always defers to the LLM Judge by returning `(false, "")`, since the gateway cannot inspect remote tool semantics.
- **`Execute()`** calls the MCP server's `tools/call` endpoint with the provided input and converts the result to a `tools.ToolResult`. Text content is extracted and joined; structured content is marshaled to JSON when no text is present.

## SchemaSanitizer

`SchemaSanitizer` is a function type that transforms an input schema (JSON Schema) before it is exposed to the LLM:

```go
type SchemaSanitizer func(source string, schema json.RawMessage) json.RawMessage
```

When set in `GatewayConfig`, it is applied to every tool registered from MCP servers (in `NewTool`). Return the input unchanged to pass through. The typical use is to strip auto-injected parameters (e.g. `"project"`) that should not be visible to the LLM but are added at execution time by a `ParamManager`.

A single `ParamManager` instance should be shared between the MCP gateway (which calls `SanitizeSchema`) and the tool registry (which calls `InjectParams`) so both sides agree on the set of auto-injected parameters:

```go
pm := tools.DefaultParamManager()

gateway, err := mcp.StartGateway(ctx, mcp.GatewayConfig{
    Servers: servers,
    SchemaSanitizer: pm.SanitizeSchema, // strip "project" from MCP schemas
}, registry, os.ExpandEnv, logger)

registry.SetParamManager(pm) // inject "project" at execution time
```

## Error handling

MCP failures are **non-fatal**. The gateway is designed so that a single broken server never prevents the agent from running with the remaining tools.

- `Gateway.Start` collects per-server errors and continues connecting to the rest. If any server fails, it returns a `*StartError` holding all the individual errors.
- `StartGateway` logs start errors as warnings and still returns the gateway (with whatever servers did connect), so the agent keeps working.
- `Gateway.Stop` returns a `*StopError` if any server fails to close cleanly.
- `Gateway.Reconfigure` returns a `*ReconfigureError` for failed add/remove/ change operations.

```go
type StartError struct{ Errors []error }
type StopError struct{ Errors []error }
type ReconfigureError struct{ Errors []error }
```

Each error type renders a summary when there are multiple failures, or the single underlying error when there is one.

## Reconnect capability

`Gateway.Reconfigure` updates the gateway's server connections based on a new `GatewayConfig`, handling added, removed, and changed servers while preserving unchanged connections:

```go
func (g *Gateway) Reconfigure(
    ctx context.Context,
    newConfig GatewayConfig,
    registry *tools.ToolRegistry,
    expandEnv func(string) string,
    logger *slog.Logger,
) error
```

It diffs the new configuration against the current set of servers:

- **Removed servers** — their tools are unregistered (`UnregisterBySource`) and the connection is closed.
- **Added servers** — connected, discovered, and registered.
- **Changed servers** — old tools are unregistered, the old connection is closed, and a fresh connection is established with the new config.
- **Unchanged servers** — left alive (no reconnect).

Config comparison uses the *expanded* config (see [Environment variable expansion](#environment-variable-expansion)) so that raw `${VAR}` placeholders do not trigger spurious reconnects.

### Status and introspection

```go
g.Status()      // []ServerStatus — per-server name, transport, connected, tool count, tools, error
g.ServerNames() // []string — all connected server names
g.ToolCount()   // int — total tools across all servers
g.GetServer(n)  // *Server — a specific server by name, or nil
```

`ServerStatus` is sorted by server name for deterministic output.

## Complete example: stdio filesystem server

This example configures a stdio MCP filesystem server. The gateway starts during framework initialization, connects to the server, discovers its tools, and registers them alongside the built-in tools. You need Node.js (`npx`) installed to run the filesystem server; if it is unavailable, the agent still works with the built-in tools — MCP failures are logged as warnings, not fatal errors.

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/v0lka/sp4rk"
    "github.com/v0lka/sp4rk/agent"
    "github.com/v0lka/sp4rk/llm"
    "github.com/v0lka/sp4rk/tools"
    "github.com/v0lka/sp4rk/tools/builtins"
    "github.com/v0lka/sp4rk/tools/mcp"
)

func main() {
    if err := run(); err != nil {
        log.Fatalf("%v", err)
    }
}

func run() error {
    // Create a directory the MCP filesystem server will expose.
    mcpRoot, err := os.MkdirTemp("", "mcp-root-*")
    if err != nil {
        return fmt.Errorf("failed to create MCP root: %w", err)
    }
    defer func() { _ = os.RemoveAll(mcpRoot) }()

    // Seed it with a sample file so the agent has something to read.
    if err := os.WriteFile(mcpRoot+"/greeting.txt",
        []byte("Hello from MCP filesystem server!\n"), 0o644); err != nil {
        return fmt.Errorf("failed to seed MCP root: %w", err)
    }

    // Create the Framework with an MCP server configuration.
    // The MCP gateway starts during sp4rk.New(), connects to all configured
    // servers, discovers their tools, and registers them in the ToolRegistry.
    fw, err := sp4rk.New(sp4rk.Config{
        LLM: sp4rk.LLMConfig{
            Providers: []llm.ProviderEntry{{
                Name:         "anthropic",
                ProviderType: "anthropic",
                APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
                Models:       []string{"claude-sonnet-4-5"},
            }},
        },
        MCP: &sp4rk.MCPConfig{
            Servers: map[string]mcp.ServerEntry{
                // A stdio MCP server: the SDK launches the command,
                // communicates over stdin/stdout, and discovers tools via MCP.
                "filesystem": {
                    Transport: "stdio",
                    Command:   "npx",
                    Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", mcpRoot},
                },
                // An HTTP MCP server (uncomment if you have one):
                // "api": {
                //     Transport: "http",
                //     URL:       "http://localhost:3001/mcp",
                //     Headers:   map[string]string{"Authorization": "Bearer ${MCP_TOKEN}"},
                // },
            },
            DefaultWorkDir: mcpRoot,
        },
    })
    if err != nil {
        return fmt.Errorf("failed to create framework: %w", err)
    }
    defer func() { _ = fw.Shutdown() }()

    // Register built-in tools alongside the MCP-discovered tools.
    registry := fw.ToolRegistry()
    registry.Register(builtins.NewReadFileTool())
    registry.Register(builtins.NewListDirectoryTool())
    registry.Register(agent.NewFinishTool())

    // List all available tools so we can see what MCP contributed.
    fmt.Println("\nAvailable tools:")
    for _, td := range registry.List() {
        fmt.Printf("  [%s] %s\n", td.Source, td.Name)
    }

    // Use the MCP root as the workspace for built-in tools too.
    ctx := tools.WithWorkspacePath(context.Background(), mcpRoot)

    systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
        return "You are a file exploration assistant with access to both " +
            "built-in tools and MCP-provided tools. " +
            "Use any available tool to accomplish the task. " +
            "Call finish when done."
    }

    task := "Read the file greeting.txt in the workspace and tell me its contents."

    result, err := fw.Execute(ctx, systemPrompt, &agent.NoopEvents{}, task)
    if err != nil {
        return fmt.Errorf("execution failed: %w", err)
    }

    fmt.Println("Status:", result.Status)
    fmt.Println("Output:", result.Output)
    return nil
}
```

## Integration with the Framework

The framework (`sp4rk.Config.MCP`) wires MCP integration into the agent lifecycle so you do not have to call `StartGateway` manually:

- **`MCPConfig.Servers`** — a `map[string]mcp.ServerEntry`, identical to `GatewayConfig.Servers`.
- **`MCPConfig.DefaultWorkDir`** — fallback working directory for stdio servers.
- During `sp4rk.New`, the framework calls `StartGateway` with the configured servers, the shared `ToolRegistry`, an env-expansion function, and a logger. Discovered MCP tools are auto-registered alongside any built-in tools you register afterwards.
- On `fw.Shutdown()`, the gateway is stopped and all server connections are closed.

Because MCP tools are registered into the same `ToolRegistry` as built-in tools, they are immediately available to the executor, planner, and any custom tool selection logic. Their `Source` tag (the bare server name, e.g. `"filesystem"`) lets you filter or exclude them with `ListFiltered` and remove them wholesale with `UnregisterBySource`.
