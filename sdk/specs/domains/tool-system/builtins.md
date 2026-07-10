# Built-in Tools

## Role

The SDK ships a catalog of filesystem, search, web, execution, and agent-infrastructure tools that implement `tools.Tool`. They are the out-of-the-box capabilities an agent can call; hosts register the subset they need into a `ToolRegistry`. `ask_user` is **not** an SDK built-in — interactive prompting is a host-application concern.

## Key Files

- `github.com/v0lka/sp4rk/tools/builtins` — built-in tool implementations, `file_reader.go` (`ReadFileRange`, streaming O(1)-memory line reader), `limits.go` (per-tool truncation limit types)
- `github.com/v0lka/sp4rk/tools/builtins` (web search) — `web_search/` provider abstraction (brave, duckduckgo, exa, tavily)
- `github.com/v0lka/sp4rk/tools/builtins` (blackboard-backed) — `read_step_output`, `list_step_outputs`, `read_final_result`, `tool_result_read`, `update_checklist`, `declare_step_complete`
- `github.com/v0lka/sp4rk/agent` — `FinishTool`
- `github.com/v0lka/sp4rk/skills` — `ReadSkillResourceTool`

## Behavior

### Tool Catalog

| Tool | Category | Default Policy | Untrusted | Description |
| ---- | -------- | -------------- | --------- | ----------- |
| `bash_exec` | Execution | `user_confirm` | yes | Shell command execution with timeout and blacklist. |
| `read_file` | File | `always_allow` | yes | Read file contents (streaming, O(1) memory, default 2000-line window). |
| `write_file` | File | `user_confirm` | no | Create/overwrite a file. |
| `edit_file` | File | `user_confirm` | no | Apply targeted find-and-replace edits. |
| `list_directory` | File | `always_allow` | no | List directory contents. |
| `create_directory` | File | `user_confirm` | no | Create a directory recursively. |
| `delete_directory` | File | `user_confirm` | no | Remove a directory recursively. |
| `delete_file` | File | `user_confirm` | no | Remove a single file. |
| `glob` | Search | `always_allow` | yes | Glob-pattern file matching. |
| `ripgrep` | Search | `always_allow` | yes | Fast regex content search (shells out to `rg`). |
| `semantic_search` | Search | `always_allow` | no | Vector similarity search (optional; see [../embedding.md](../embedding.md)). |
| `web_fetch` | Web | `always_allow` | yes | Fetch URL content as markdown. |
| `web_search` | Web | `always_allow` | yes | Search the web (optional; needs a search-provider config). |
| `finish` | Agent | internal | no | Signal task/step completion (auto-appended to every run). |
| `batch` | Agent | `always_allow` | no | Dispatch multiple tool calls in one turn (intercepted at the executor level). |
| `read_step_output` | Agent | `always_allow` | no | Read a specific completed step's output (blackboard-backed). |
| `list_step_outputs` | Agent | `always_allow` | no | List completed step outputs. |
| `read_final_result` | Agent | `always_allow` | no | Read the prior task's final result (continuation recovery). |
| `update_checklist` | Agent | `always_allow` | no | Update a step/sub-task checklist; validates Markdown checkboxes. |
| `declare_step_complete` | Agent | `always_allow` | no | Signal a plan-step completion. |
| `store_fact` | Agent | `always_allow` | no | Store a keyword-tagged fact to the blackboard. |
| `search_facts` | Agent | `always_allow` | no | Search blackboard facts by keyword. |
| `tool_result_read` | Agent | internal | no | Read cached tool-result fragments by hash (streaming, O(1) memory for file-backed entries). |
| `read_skill_resource` | Agent | `always_allow` | no | Read a resource file from an activated skill directory (path-traversal safe). |

`ask_user` is intentionally absent from the SDK. Interactive multi-question prompting requires host UI plumbing, so it lives in the host application; the SDK only supplies the primitives such a tool builds on.

### Trust classification

A tool's output is **untrusted** when it may carry adversarial content (web, MCP, filesystem reads of external data). Such tools set `Untrusted: true` on their `BaseTool`; their observations are wrapped in `<untrusted-content>` tags before entering the LLM context when injection defense is enabled (see [../memory/README.md](../memory/README.md)). Mutating filesystem tools (`write_file`, `edit_file`, `delete_*`, `create_directory`) and `bash_exec` are not marked untrusted.

### File tools

File tools resolve paths via context helpers (`WorkspacePathFrom`/`TempDirFrom`). Relative paths are joined with the workspace root and must stay within it; absolute paths are symlink-resolved and returned regardless of containment (containment is a policy concern, not a parse failure). `read_file` uses `ReadFileRange` for O(1)-memory streaming of line ranges and is file-backed in the `ToolResultCache` (zero content bytes stored; fragments streamed from disk on demand). Binary files (null bytes in the leading window) are detected and rejected.

### Web search providers

`web_search` is optional: it is silently not registered when no search-provider config/API key is supplied. The provider abstraction supports Brave, DuckDuckGo, Exa, and Tavily.

### Agent-infrastructure tools

Blackboard-backed tools (`read_step_output`, `list_step_outputs`, `read_final_result`, `store_fact`, `search_facts`) read/write shared state through the `agent.*Store` adapters the [Conductor](../orchestration/conductor.md) injects (see [../memory/blackboard.md](../memory/blackboard.md)). `update_checklist` validates Markdown checkboxes and emits to-do updates via a context-injected callback; `declare_step_complete` and `read_step_output` are likewise context-aware. `tool_result_read` validates cache coherence on every read (file mtime+size for file tools; TTL for MCP tools).

## Error Handling

- **Tool not found**: `ToolRegistry.Execute` returns an `IsError` `ToolResult` (does not panic).
- **Parse failure**: idiomatically returned via `tools.ParseInputError` — an `IsError` result, nil Go error (clean message, not an infrastructure failure).
- **Bash blacklist / timeout**: `IsError: true` with a descriptive message; timeout messages include the configured value.
- **ripgrep exit 1**: not an error (no matches); exit ≥ 2 produces `IsError` with stderr.
- **Optional tool absence**: a missing dependency (e.g. no search API key) silently skips registration — no error at registration time.

## Invariants

- `finish` is always available (auto-appended to every run if absent).
- `batch` is intercepted at the executor level before reaching the registry; its own `Execute()` returns an error.
- Blackboard-backed tools read only error-free completed steps; outputs are listed in deterministic step-ID order.
- `read_skill_resource` resolves paths via `skills.SafeResolvePath` (path-traversal safe).
- Untrusted-output tools always set `Untrusted: true` and are wrapped when injection defense is enabled.

## Extension Guide

To add a new built-in tool:

1. Define a struct embedding `*tools.BaseTool`.
2. In the constructor, set `ToolName`, `ToolDescription`, `Schema` (JSON Schema), and `Policy` (`PolicyAlwaysAllow` / `PolicyUserConfirm`).
3. Implement `Execute(ctx, input)` — use `ParseInputError` for JSON parse failures and `ErrorResult` for logical errors.
4. Set `Untrusted: true` if the tool returns external data (web, MCP, filesystem reads of external content).
5. Optionally implement `ToolJudger` for tool-specific safety escalation on `PolicyAlwaysAllow` tools.
6. Register the tool in the `ToolRegistry` alongside the `finish` tool.

## Related Specs

- [README.md](README.md) — tool system overview and execution pipeline
- [mcp-gateway.md](mcp-gateway.md) — dynamic MCP tool discovery
- [../orchestration/executor.md](../orchestration/executor.md) — `batch` interception, two-stage truncation, `ToolResultCache`
- [../memory/blackboard.md](../memory/blackboard.md) — store adapters backing the blackboard tools
- [../embedding.md](../embedding.md) — `semantic_search` vector tool
