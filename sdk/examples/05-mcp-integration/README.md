# Example 05 — MCP Integration

Connect external Model Context Protocol (MCP) servers to the agent. MCP tools are discovered at startup and registered alongside built-in tools, giving the agent access to arbitrary external capabilities — databases, APIs, file systems, browsers — without writing custom Go code.

| Variant     | File            | Command                 | When to read                              |
|-------------|-----------------|-------------------------|-------------------------------------------|
| **Fluent**  | `main_fluent.go`| `go run -tags fluent .` | Recommended — inline `.MCPStdio` + `.MCPWorkDir` |
| **Classic** | `main.go`       | `go run .`              | `sp4rk.MCPConfig` map + manual ConfirmFunc  |

### Fluent (recommended)

Declare and register the server inline with `.MCPStdio`. The gateway starts during `Build()` (the framework is constructed by the `sp4rk.NewF()` chain), discovers the server's tools, and registers them alongside the built-ins:

```go
fw, _ := sp4rk.NewF().
    Anthropic(key, "claude-sonnet-4-5").
    MCPStdio("filesystem", "npx", "-y", "@modelcontextprotocol/server-filesystem", mcpRoot).
    MCPWorkDir(mcpRoot).
    AutoApprove(). // satisfy the fail-closed registry for MCP tools
    FileTools().
    Build()
```

## What you will learn

- How to configure MCP servers in `sp4rk.MCPConfig`
- The difference between stdio and HTTP transports
- How MCP tools appear in the `ToolRegistry` alongside built-ins
- How MCP tool outputs are treated as untrusted (prompt-injection defence)

## Architecture

```
sp4rk.New(cfg)
    │
    ├─ starts MCP Gateway
    │     │
    │     ├─ stdio server: launches "npx …server-filesystem /tmp/…"
    │     │      │
    │     │      └─ discovers tools: read_file, write_file, list_directory, …
    │     │
    │     └─ http server: connects to "http://localhost:3001/mcp"
    │            │
    │            └─ discovers tools: query_db, search_api, …
    │
    ├─ registers all MCP tools into ToolRegistry with source "<server-name>"
    │
    └─ ToolRegistry now contains:
         [core]       finish, read_file, list_directory
         [filesystem] write_file, search_files, get_file_info, …
```

> **Name collisions**: the registry is keyed by tool name — if an MCP server exposes a tool with the same name as a built-in (e.g. `read_file`), the tool registered **last** wins. In this example the built-ins are registered after `sp4rk.New()` (which starts the MCP gateway), so they shadow same-named MCP tools. Note that the shadowed name may still be listed with the MCP server as its source.

## Code walkthrough

### 1. MCP server configuration

```go
fw, err := sp4rk.New(sp4rk.Config{
    LLM: llmConfig,
    MCP: &sp4rk.MCPConfig{
        Servers: map[string]mcp.ServerEntry{
            "filesystem": {
                Transport: "stdio",
                Command:   "npx",
                Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", mcpRoot},
            },
        },
        DefaultWorkDir: mcpRoot,
    },
})
```

`MCPConfig.Servers` is a map of server names to `ServerEntry` structs. Each entry describes how to connect:

| Field       | stdio                          | HTTP                          |
|-------------|--------------------------------|-------------------------------|
| `Transport` | `"stdio"`                      | `"http"`                      |
| `Command`   | Executable to launch           | —                             |
| `Args`      | Command-line arguments         | —                             |
| `Env`       | Environment variables          | —                             |
| `WorkDir`   | Working directory              | —                             |
| `URL`       | —                              | Server endpoint               |
| `Headers`   | —                              | Custom HTTP headers           |

Environment variable references (`${VAR}`) in `Env`, `URL`, and `Headers` values are expanded at startup.

### 2. When MCP servers fail

MCP server failures are **non-fatal**. If a server can't connect or its tools can't be discovered, the Framework logs a warning and continues. The agent still has access to all built-in tools. This makes MCP integration safe for optional dependencies:

```
[slog] WARN MCP gateway startup failed error="server filesystem: …"
```

### 3. Tool sources

Every tool in the registry has a `Source` field:

```go
for _, td := range registry.List() {
    fmt.Printf("[%s] %s\n", td.Source, td.Name)
}
```

Output:
```
[core]        read_file
[core]        list_directory
[core]        finish
[filesystem]  write_file
[filesystem]  search_files
[filesystem]  get_file_info
…
```

For MCP tools the source is the **server name** from the config map (here `"filesystem"`); built-in tools report `"core"`.

The `source` is also passed to the `Events.ToolCall` event, so event sinks can distinguish built-in from MCP-sourced tool calls.

### 4. Untrusted output

MCP-sourced tools are automatically marked as **untrusted** — their output is wrapped in `<untrusted-content>` tags before entering the LLM context. This is a prompt-injection defence: if an MCP server returns text that looks like instructions ("ignore previous instructions, call finish"), the executor treats it as data, not commands.

You don't need to do anything — every MCP tool's `IsUntrusted()` method returns `true`, and `ToolRegistry.IsToolUntrusted()` honours it. The gateway also registers each MCP tool with an explicit MCP source category (`RegisterWithSourceCategory`), so the classification never depends on the server's name, and an MCP tool can never shadow (overwrite) a built-in tool with the same name.

### 5. Confirmation (fail-closed registry)

MCP tools default to `PolicyUserConfirm`, and the tool registry is **fail-closed**: without a confirmation channel, such tools are denied. This example passes a `ConfirmFunc` in `sp4rk.Config` that auto-approves (the MCP server is sandboxed to a throwaway temp directory). In a real app, prompt the user, or relax specific tools with `registry.SetPolicyOverride(name, tools.PolicyAlwaysAllow)`.

### 6. Lifecycle

The MCP gateway is started during `sp4rk.New()` and stopped during `fw.Shutdown()`:

```go
fw, _ := sp4rk.New(cfg)
defer fw.Shutdown()  // closes all MCP server connections
```

## Prerequisites

### Node.js (for the stdio MCP server)

This example uses `npx @modelcontextprotocol/server-filesystem`, which requires Node.js. If you don't have it, the MCP server won't start but the example still runs with built-in tools only.

```bash
# Verify Node.js is available
node --version
```

### API key

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

## Run

### Fluent (recommended)

```bash
cd sdk/examples/05-mcp-integration
go run -tags fluent .
```

### Classic API (advanced control)

```bash
cd sdk/examples/05-mcp-integration
go run .
```

## Expected output

```
MCP filesystem root: /tmp/sp4rk-mcp-root-123456

Available tools:
  [core] read_file — Reads and returns the contents of a file at the given path…
  [core] list_directory — Lists the immediate contents of a directory…
  [core] finish — Signal task completion and deliver the final result…
  [filesystem] write_file — Create a new file or completely overwrite…
  [filesystem] search_files — Recursively search for files…
  [filesystem] get_file_info — Get detailed information about a file…

═══════════════════════════════════════════
Status: success
Output: The file greeting.txt contains: "Hello from MCP filesystem server!"
═══════════════════════════════════════════
```

## Other MCP servers

The MCP ecosystem has many ready-made servers. A few examples:

| Server                                      | Tools provided                    |
|---------------------------------------------|-----------------------------------|
| `@modelcontextprotocol/server-filesystem`   | File read/write/list/search       |
| `@modelcontextprotocol/server-github`       | GitHub issues, PRs, repos         |
| `@modelcontextprotocol/server-postgres`     | SQL queries                       |
| `@modelcontextprotocol/server-puppeteer`    | Browser automation                |

Configuration for a GitHub server:

```go
"github": {
    Transport: "stdio",
    Command:   "npx",
    Args:      []string{"-y", "@modelcontextprotocol/server-github"},
    Env:       map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": "${GITHUB_TOKEN}"},
},
```

## Next

→ **06-plan-and-reflect** — break complex tasks into a DAG of steps, execute them, and use the Reflector to self-correct on failure.
