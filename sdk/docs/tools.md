# Tools

The `tools` package provides the tool abstraction, a thread-safe registry, and
core types for agent tool execution. Tools are the agent's way of acting on the
world — reading files, running commands, calling external APIs. Every tool
implements a single `Tool` interface and is registered in a `ToolRegistry` that
the executor consults at runtime.

```go
import "github.com/v0lka/c0wrk/sdk/tools"
```

## Tool interface

Every tool — whether built-in, custom, or proxied from an MCP server —
implements the `Tool` interface:

```go
type Tool interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage
    Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
    DefaultPolicy() ToolPolicy
    IsUntrusted() bool
}
```

| Method | Purpose |
| --- | --- |
| `Name()` | Unique tool identifier used by the LLM and registry. |
| `Description()` | Natural-language description shown to the LLM so it knows when to call the tool. |
| `InputSchema()` | JSON Schema (`json.RawMessage`) describing the tool's input parameters. |
| `Execute(ctx, input)` | Runs the tool with the given JSON input. Returns a `ToolResult` and/or an error. |
| `DefaultPolicy()` | The tool's default security policy (allow / deny / confirm). |
| `IsUntrusted()` | Reports whether the tool returns external/untrusted data (web, MCP, filesystem) that should be sanitized before being placed into LLM context. |

## BaseTool

`BaseTool` provides default implementations of `Name`, `Description`,
`InputSchema`, `DefaultPolicy`, and `IsUntrusted` so concrete tools only need to
implement `Execute`. Embed it in your custom tool and set the relevant fields:

```go
type BaseTool struct {
    ToolName        string
    ToolDescription string
    Schema          json.RawMessage
    Policy          ToolPolicy
    Untrusted       bool // marks output as external/untrusted for prompt-injection defense
}
```

## ToolResult

```go
type ToolResult struct {
    Content string
    IsError bool
}
```

`Content` is the text returned to the agent. Set `IsError` to `true` when the
tool ran but produced an error condition (e.g. a file was not found) — this is
distinct from returning a Go `error`, which signals an infrastructure failure.

Two helpers construct error results consistently:

```go
// ErrorResult creates a ToolResult with IsError=true.
tools.ErrorResult("file not found: %s", path)

// ParseInputError returns a standard parse-error ToolResult.
tools.ParseInputError(err) // (ToolResult, error) — returns an error result, nil error
```

`ParseInputError` is the idiomatic response when `json.Unmarshal` of the tool
input fails. It returns a `ToolResult` (with `IsError: true`) and a `nil` error,
so the agent sees a clean error message rather than an infrastructure failure.

## ToolPolicy

`ToolPolicy` defines the security policy for a tool:

```go
const (
    PolicyAlwaysAllow ToolPolicy = iota // execute without confirmation
    PolicyAlwaysDeny                    // block execution
    PolicyUserConfirm                   // require user confirmation first
)
```

`ParseToolPolicy(s)` converts a config string to a constant. Recognized strings
are `"always_allow"`, `"always_deny"`, and `"user_confirm"`; any other value
defaults to `PolicyUserConfirm` (the safest option).

### ToolJudger (optional)

A tool with `PolicyAlwaysAllow` may optionally implement `ToolJudger` to provide
tool-specific safety heuristics. When implemented, the registry calls `Judge`
before execution; if it returns `allow=false` with non-empty reasoning, the call
is escalated to user confirmation.

```go
type ToolJudger interface {
    Judge(ctx context.Context, input json.RawMessage) (allow bool, reasoning string)
}
```

## ToolDescriptor

`ToolDescriptor` is metadata-only (no execution capability) used by the Planner
and Executor to reason about available tools without holding live `Tool`
references:

```go
type ToolDescriptor struct {
    Name           string
    Description    string
    InputSchema    json.RawMessage
    Source         string             // "core" | "mcp:<server>"
    SourceCategory ToolSourceCategory // "core" | "mcp" (cached for fast checks)
}
```

`Source` records where a tool came from. Built-in tools report `"core"`; MCP
tools report their server name (e.g. `"filesystem"`). `SourceCategory` is a
cached classification (`SourceCategoryCore` or `SourceCategoryMCP`) for fast
routing checks.

## ToolRegistry

`ToolRegistry` stores all available tools and provides them to the executor. It
is **thread-safe** via `sync.RWMutex`.

```go
registry := tools.NewToolRegistry()
```

| Method | Behavior |
| --- | --- |
| `Register(tool)` | Add a tool by its name. |
| `RegisterWithSource(tool, source)` | Add a tool with an explicit source tag (used by MCP). |
| `Unregister(name)` | Remove a tool by name. |
| `UnregisterBySource(source)` | Remove all tools registered with a given source. |
| `Get(name)` | Look up a tool by name; returns `(Tool, bool)`. |
| `List()` | Return `[]ToolDescriptor` for all registered tools. |
| `ListFiltered(excludeNames)` | Return descriptors for all tools except those in `excludeNames`. |
| `Execute(ctx, name, input)` | Look up and execute a tool. **No policy enforcement** — that is the executor's job. |
| `GetToolSource(name)` | Return a tool's source (`"core"` or its source tag); `""` if not found. |
| `IsToolUntrusted(name)` | `true` if the tool's `IsUntrusted()` is true **or** it is MCP-sourced. |
| `SetParamManager(pm)` | Install a `ParamManager` for execution-time parameter injection. |

`Execute` applies parameter injection (if a `ParamManager` is configured) before
invoking the tool. If the tool is not found it returns an error `ToolResult`
rather than a Go error.

## Context helpers

Several values are passed to tools through `context.Context` rather than tool
parameters. This keeps the LLM-facing schema clean while giving tools access to
session-scoped state.

```go
// Workspace path — used by file tools to resolve relative paths.
ctx = tools.WithWorkspacePath(ctx, "/path/to/workspace")
ws := tools.WorkspacePathFrom(ctx) // "" if not set

// Temporary directory — scratch space for intermediate artifacts.
ctx = tools.WithTempDir(ctx, tmpDir)
tmp := tools.TempDirFrom(ctx)

// Task context — the current task description.
ctx = tools.WithTaskContext(ctx, taskDesc)
desc := tools.TaskContextFrom(ctx)
```

Built-in file tools retrieve the workspace path via `WorkspacePathFrom(ctx)`,
so always attach it before executing tasks that touch the filesystem.

## Custom tool implementation

This is a complete, step-by-step guide using a `calculator` tool that evaluates
arithmetic expressions. The same pattern applies to any custom tool.

### 1. Define the tool struct, embedding `BaseTool`

Embedding `BaseTool` gives you `Name`, `Description`, `InputSchema`,
`DefaultPolicy`, and `IsUntrusted` for free — you only implement `Execute`.

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"

    "github.com/v0lka/c0wrk/sdk/tools"
)

// CalculatorTool evaluates simple arithmetic expressions.
type CalculatorTool struct {
    *tools.BaseTool
}
```

### 2. Write a constructor that sets the `BaseTool` fields

Set `ToolName`, `ToolDescription`, `Schema` (a JSON Schema), and `Policy`.

```go
func NewCalculatorTool() *CalculatorTool {
    return &CalculatorTool{BaseTool: &tools.BaseTool{
        ToolName:        "calculator",
        ToolDescription: "Evaluate an arithmetic expression (supports +, -, *, /, parentheses). Example: calculator(expression=\"15 * 37 + 4\")",
        Schema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "expression": {
                    "type": "string",
                    "description": "The arithmetic expression to evaluate, e.g. \"2 + 3 * 4\""
                }
            },
            "required": ["expression"]
        }`),
        Policy: tools.PolicyAlwaysAllow,
    }}
}
```

### 3. Implement `Execute`

Parse the JSON input, validate it, perform the work, and return a `ToolResult`.
Use `ParseInputError` for JSON parse failures and `ErrorResult` (or an
`IsError` result) for logical errors.

```go
func (t *CalculatorTool) Execute(_ context.Context, input json.RawMessage) (tools.ToolResult, error) {
    var params struct {
        Expression string `json:"expression"`
    }
    if err := json.Unmarshal(input, &params); err != nil {
        return tools.ParseInputError(err)
    }
    if params.Expression == "" {
        return tools.ToolResult{Content: "validation error: expression is required", IsError: true}, nil
    }

    result, err := evaluate(params.Expression)
    if err != nil {
        return tools.ToolResult{Content: fmt.Sprintf("evaluation error: %v", err), IsError: true}, nil
    }
    return tools.ToolResult{Content: fmt.Sprintf("%s = %g", params.Expression, result)}, nil
}
```

### 4. Register the tool and run

The agent can only use tools that are in the registry. Register your custom tool
alongside any built-in tools and a `finish` tool (required for task completion).

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/v0lka/c0wrk/sdk"
    "github.com/v0lka/c0wrk/sdk/agent"
    "github.com/v0lka/c0wrk/sdk/llm"
    "github.com/v0lka/c0wrk/sdk/tools"
    "github.com/v0lka/c0wrk/sdk/tools/builtins"
)

func main() {
    fw, err := sdk.New(sdk.Config{
        LLM: sdk.LLMConfig{
            Providers: []llm.ProviderEntry{{
                Name:         "anthropic",
                ProviderType: "anthropic",
                APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
                Models:       []string{"claude-sonnet-4-5"},
            }},
        },
    })
    if err != nil {
        log.Fatal(err)
    }
    defer func() { _ = fw.Shutdown() }()

    registry := fw.ToolRegistry()

    // Built-in tools.
    registry.Register(builtins.NewReadFileTool())
    registry.Register(builtins.NewWriteFileTool())
    registry.Register(builtins.NewListDirectoryTool())
    // The finish tool is required for task completion.
    registry.Register(agent.NewFinishTool())

    // Our custom calculator tool.
    registry.Register(NewCalculatorTool())

    // Set up a workspace so file tools know where to read/write.
    workspaceDir, err := os.MkdirTemp("", "example-*")
    if err != nil {
        log.Fatal(err)
    }
    defer func() { _ = os.RemoveAll(workspaceDir) }()

    ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)

    systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
        return fmt.Sprintf("You are a coding assistant working in %s. "+
            "You have a calculator tool for arithmetic and file tools for "+
            "reading/writing files. Call finish with a summary when done.",
            workspaceDir)
    }

    task := "Use the calculator tool to compute 17 * 23 + 100, then write " +
        "the result to a file called 'result.txt' in the workspace. " +
        "Finally, read the file back to verify its contents."

    result, err := fw.Execute(ctx, systemPrompt, &agent.NoopEvents{}, task)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("Status:", result.Status)
    fmt.Println("Output:", result.Output)
}
```

### Error handling summary

| Situation | Return |
| --- | --- |
| JSON input failed to parse | `tools.ParseInputError(err)` → error `ToolResult`, `nil` error |
| Validation failure (missing/invalid field) | `tools.ToolResult{Content: "...", IsError: true}, nil` |
| Logical failure (e.g. division by zero) | `tools.ToolResult{Content: "...", IsError: true}, nil` |
| Infrastructure failure (panic, broken invariant) | `tools.ToolResult{}, err` (a Go error) |

Returning a Go `error` signals that the executor should treat the failure as
infrastructure-level (and potentially retry or reflect); an `IsError` result is
a normal, reportable tool failure that the agent can reason about.

## ParamManager

`ParamManager` handles auto-injected tool parameters. It has two
responsibilities:

- **`SanitizeSchema(source, schema)`** — strip auto-injected params from tool
  schemas so the LLM never sees them.
- **`InjectParams(ctx, toolName, source, input)`** — add their values at
  execution time.

A single `ParamManager` instance should be shared between the MCP gateway (which
calls `SanitizeSchema`) and the tool registry (which calls `InjectParams`) so
both sides agree on the set of auto-injected parameters.

```go
type ParamManager interface {
    SanitizeSchema(source string, schema json.RawMessage) json.RawMessage
    InjectParams(ctx context.Context, toolName, source string, input json.RawMessage) json.RawMessage
}
```

`DefaultParamManager()` handles all known auto-injected parameters (currently
the `"project"` parameter, which is injected from the workspace path in the
context). Install it on the registry with `SetParamManager`:

```go
registry.SetParamManager(tools.DefaultParamManager())
```

When set, `ToolRegistry.Execute` consults the manager and injects the workspace
path as the `"project"` parameter before invoking the tool — but only if the
parameter is not already present in the input.

## Built-in tools reference

All built-in tools live in the `builtins` package:

```go
import "github.com/v0lka/c0wrk/sdk/tools/builtins"
```

| Tool name | Constructor | Description |
| --- | --- | --- |
| `read_file` | `NewReadFileTool()` | Reads and returns the contents of a file at the given path. Supports pagination via optional line range parameters. Output includes a metadata header with the file name, returned line range, and total line count. |
| `write_file` | `NewWriteFileTool()` | Creates or overwrites a file with the provided content. Parent directories are created automatically if they do not exist. Prefer `edit_file` for targeted changes. |
| `edit_file` | `NewEditFileTool()` | Performs a find-and-replace edit on an existing file. Locates a single exact occurrence of `old_string` and replaces it with `new_string`. Fails if the match is not found or matches more than once. |
| `delete_file` | `NewDeleteFileTool()` | Deletes a single file at the specified path. Fails if the path points to a directory — use `delete_directory` for directories. |
| `list_directory` | `NewListDirectoryTool()` | Lists the immediate contents of a directory, returning each entry's name, type (file or dir), and size in bytes. Does not recurse into subdirectories. |
| `create_directory` | `NewCreateDirectoryTool()` | Creates a directory at the specified path, including any necessary parent directories (like `mkdir -p`). Succeeds silently if the directory already exists. |
| `delete_directory` | `NewDeleteDirectoryTool()` | Deletes a directory at the specified path. By default only empty directories can be deleted; set `recursive` to `true` to remove the directory and all its contents. |
| `glob` | `NewGlobTool()` | Find files and directories by name using glob patterns. Supports `**` for recursive directory matching. Respects `.gitignore` rules automatically. |
| `ripgrep` | `NewRipgrepTool()` | Search file contents using regex or literal patterns. Returns matches in `file:line: content` format with optional context lines. Respects `.gitignore` rules and skips binary files. |
| `batch` | `NewBatchTool()` | Execute multiple tool calls sequentially in one turn. All calls execute in order even if one fails — errors are captured per-call and do not abort the batch. |
| `update_checklist` | `NewUpdateChecklistTool()` | Update the checklist for the current step (or the task as a whole). Call first to initialize, then after completing each item. Uses ASCII checkboxes only. |
| `tool_result_read` | `NewToolResultReadTool()` | Read a previously cached tool result in fragments using the hash from a truncation nudge. Retrieves more content from a truncated output without re-executing the original tool. |
| `store_fact` | `NewStoreFactTool()` | Store a fact with 3–5 keywords for later retrieval by yourself or other agents. Records important discoveries, decisions, or intermediate results. |
| `search_facts` | `NewSearchFactsTool()` | Search stored facts by keywords. Returns facts matching any of the given keywords, ranked by relevance (most keyword matches first). |
| `read_step_output` | `NewReadStepOutputTool()` | Read the complete output of a specific completed step by its ID. Use when a dependency step's summary is insufficient and the full, untruncated result is needed. |
| `list_step_outputs` | `NewListStepOutputsTool()` | List all available step outputs with short previews (up to 200 characters each). Use to discover which completed step results are available. |
| `read_final_result` | `NewReadFinalResultTool()` | Read the final result of the previously completed task on the blackboard. Use to retrieve a prior exchange's outcome when it is not visible in the conversation history. |
| `web_fetch` | `NewWebFetchTool(limits WebFetchLimits)` | Fetch a web page by URL and convert its HTML content to markdown. Only HTTP/HTTPS URLs are supported. Requests time out after 30 seconds; up to 10 redirects are followed. Supports paginated reading. |
| `web_search` | `websearch.NewWebSearchTool(provider, limits)` | Search the web and return a list of results with titles, URLs, and text snippets. Uses a pluggable `SearchProvider` — see [Web search providers](#web-search-providers) below. |
| `bash_exec` | `NewBashExecTool(blacklist []string) (*BashExecTool, error)` | Execute shell commands via `bash -c`. Use for build commands, running scripts, installing packages, git operations, and system tasks. Returns combined stdout and stderr. Commands time out after 60 seconds by default (configurable up to 120s). |
| `semantic_search` | `NewVectorSearchTool(searchFunc VectorSearchFunc, waitFunc VectorSearchWaitFunc)` | Search the project codebase using hybrid (vector + BM25) similarity matching. Finds code by meaning and intent as well as by literal symbol/keyword match. Returns file paths, line ranges, fused relevance scores, and content previews. |

> **Note on `semantic_search`:** the type is `VectorSearchTool`, but the name
> exposed to the LLM is `semantic_search`. Its constructor requires a
> `VectorSearchFunc` (performs the search) and a `VectorSearchWaitFunc` (blocks
> until the vector index is ready), both provided by the backend layer at wiring
> time.
>
> **Note on SSRF protection:** the `netcheck.go` file in the `builtins` package
> provides private-network detection helpers (`isPrivateIP`,
> `resolveHostIsPrivate`) consumed by `web_fetch` to guard against SSRF. It is
> not itself a registered tool.

### Tool name constants

The `tools` package exports name constants for tools that need special handling
in the executor (truncation hints, caching, etc.):

```go
const (
    ToolReadFile  = "read_file"
    ToolWriteFile = "write_file"
    ToolEditFile  = "edit_file"
    ToolRipgrep   = "ripgrep"
    ToolGrep      = "grep"
    ToolGlob      = "glob"
    ToolWebFetch  = "web_fetch"
    ToolBashExec  = "bash_exec"
    ToolBatch     = "batch"
)
```

### Web search providers

The `web_search` tool lives in the `tools/builtins/web_search` sub-package and uses a pluggable `SearchProvider` interface so you can choose the backend that fits your needs:

```go
type SearchProvider interface {
    Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error)
    Name() string
}
```

Four providers are included:

| Provider | Constructor | API key required | Notes |
| --- | --- | --- | --- |
| **Brave** | `websearch.NewBraveProviderWithClient(apiKey, timeout, client)` | Yes (`BRAVE_API_KEY`) | Brave Search API |
| **Tavily** | `websearch.NewTavilyProviderWithClient(apiKey, timeout, client)` | Yes (`TAVILY_API_KEY`) | Tavily Search API |
| **Exa** | `websearch.NewExaProviderWithClient(apiKey, timeout, client)` | Yes (`EXA_API_KEY`) | Exa Search API |
| **DuckDuckGo** | `websearch.NewDuckDuckGoProviderWithClient(timeout, client)` | No | HTML scraping; no API key needed |

Each constructor accepts a `time.Duration` timeout and an optional `*http.Client` (pass `nil` for the default client). The `SearchResult` struct is provider-agnostic:

```go
type SearchResult struct {
    Title   string
    URL     string
    Snippet string
}
```

**Usage example:**

```go
import (
    "github.com/v0lka/c0wrk/sdk/tools/builtins"
    "github.com/v0lka/c0wrk/sdk/tools/builtins/web_search"
)

// DuckDuckGo requires no API key — simplest setup
provider := websearch.NewDuckDuckGoProviderWithClient(
    30*time.Second, // timeout
    nil,            // default HTTP client
)
tool := websearch.NewWebSearchTool(provider, builtins.DefaultWebSearchLimits())
registry.Register(tool)
```

The `WebSearchLimits` struct controls the default maximum result count and timeout:

```go
type WebSearchLimits struct {
    MaxResults int           // default: 5
    Timeout    time.Duration // default: 30s
}
```

Use `builtins.DefaultWebSearchLimits()` for sensible defaults, or construct a custom `WebSearchLimits` to override them. The tool is marked `IsUntrusted: true` — its output is automatically wrapped in `<untrusted-content>` tags by the context window's prompt injection defense.
