# Built-in Tools

## Role

Provide filesystem, search, web, execution, and agent-infrastructure tools out of the box. Registered at startup in `RegisterBuiltinTools`.

## Key Files

- `sdk/tools/builtins/*.go` — tool implementations
- `sdk/tools/builtins/ripgrep.go` — wraps the `rg` CLI (`--json` event stream)
- `sdk/tools/builtins/coherence_format.go` — conflict message formatting for cross-session coherence
- `sdk/tools/builtins/web_search/` — web search provider abstraction (brave, duckduckgo, exa, tavily)
- `sdk/tools/builtins/web_search/brave.go` — Brave Search API provider
- `sdk/tools/builtins/web_search/duckduckgo.go` — DuckDuckGo Instant Answer API provider
- `sdk/tools/builtins/web_search/exa.go` — Exa Search API provider
- `sdk/tools/builtins/web_search/tavily.go` — Tavily Search API provider
- `sdk/tools/builtins/doc.go` — package documentation
- `sdk/tools/builtins/limits.go` — BuiltinToolsConfig and limit types
- `sdk/tools/builtins/paths.go` — workspace path resolution
- `sdk/tools/builtins/workspace.go` — workspace detection
- `sdk/tools/builtins/netcheck.go` — network connectivity check for web tools
- `sdk/tools/coherence.go` — FileCoherenceChecker interface, FileSig, CoherenceConflict types
- `core/tools/builtin_registration.go` — registration function + config types

## Behavior

### Tool Registration

All built-in tools are registered at startup via `RegisterBuiltinTools(registry, cfg)`. Registration is ordered (earlier tools take precedence in case of name conflicts). Internal tools (`finish`, `ask_user`, `list_step_outputs`, `read_step_output`) always bypass policy checks during execution.

### Policy Resolution

Each tool's effective policy is resolved at execution time:

1. Check per-tool override in config (`security.tool_policies`)
2. Fall back to skill-specified policy (if tool is invoked by a skill)
3. Fall back to global default (`security.default_policy`)
4. Fall back to tool's own `DefaultPolicy()`

### File Safety Judging

File write/edit tools (`write_file`, `edit_file`, `delete_file`, `delete_directory`) implement `ToolJudger`. Even with `PolicyAlwaysAllow`, the judge inspects the target path and escalates to `PolicyUserConfirm` if the path:
- Is outside the workspace root
- References system directories (`/etc`, `/usr`, `/System`, etc.)
- Contains path traversal sequences (`../`)

### File Coherence Checking

File tools perform cross-session conflict detection via `FileCoherenceChecker` (injected into context by the session manager). The checker tracks file signatures (mtime + size) per session and detects when a file was modified by another session since the current session last read it.

**Protocol:**

- `read_file`: after reading, records the file's signature. If the file changed since this session's previous read, prepends a warning annotation to the result (non-blocking).
- `write_file`, `edit_file`, `delete_file`: before mutating, checks the file's current signature against this session's last-read snapshot. If mismatched, returns `IsError: true` with a conflict message instructing the LLM to re-read.
- `bash_exec`: not covered (bash modifications are detected naturally by subsequent coherence checks on affected files).

**Atomicity:** Each file tool acquires a per-file in-process mutex (`Lock`/`Unlock`) around the check-then-act window to eliminate TOCTOU races between concurrent sessions.

**Conflict resolution:** The LLM receives the conflict as a tool error and decides how to proceed (typically by re-reading the file and retrying the edit with updated content).

### Tool Execution

All built-in tools accept `json.RawMessage` input and return `ToolResult{Content, IsError}`. Tools that shell out (`ripgrep`, `bash_exec`) use `exec.CommandContext` with the caller's context.

## Error Handling

- **Tool not found**: `ToolRegistry.Execute()` returns `ToolResult{IsError: true}` with an "unknown tool" message — does not panic
- **Path validation**: file tools reject paths outside workspace with a descriptive error before any I/O
- **Bash blacklist**: commands matching blacklist patterns are rejected with `IsError: true` and a "blocked by security policy" message
- **Ripgrep**: exit code 1 ("no matches") is NOT an error; exit codes ≥ 2 produce `IsError` with stderr content
- **Web tools**: network errors surface as `IsError` with a descriptive message; timeout errors include the configured timeout value
- **File I/O errors**: propagated as `IsError` with the OS error message; binary files detected by null byte presence are rejected with an appropriate message
- **File coherence conflict**: when a file was modified since the session's last read, `write_file`/`edit_file`/`delete_file` return `IsError` with a conflict description and instruction to re-read; `read_file` prepends a non-blocking warning annotation
- **Optional tool absence**: if a dependency func/key is not provided (e.g., no web search API key), the tool is silently not registered (no error at registration time)

## Tool Catalog

| Tool                  | Category  | Default Policy | Description                                        |
| --------------------- | --------- | -------------- | -------------------------------------------------- |
| `bash_exec`           | Execution | user_confirm   | Shell command execution with timeout and blacklist |
| `read_file`           | File      | always_allow   | Read file contents (with size limits)              |
| `write_file`          | File      | always_allow   | Create/overwrite file                              |
| `edit_file`           | File      | always_allow   | Apply targeted edits to existing file              |
| `list_directory`      | File      | always_allow   | List directory contents                            |
| `create_directory`    | File      | always_allow   | Create directory (recursive)                       |
| `delete_directory`    | File      | user_confirm   | Remove directory recursively                       |
| `delete_file`         | File      | user_confirm   | Remove single file                                 |
| `glob`                | Search    | always_allow   | Glob pattern file matching                         |
| `ripgrep`             | Search    | always_allow   | Fast regex content search (shells out to `rg`)     |
| `semantic_search`     | Search    | always_allow   | Vector similarity search (optional)                |
| `web_fetch`           | Web       | always_allow   | Fetch URL content                                  |
| `web_search`          | Web       | always_allow   | Search the web (optional, needs API key)           |
| `finish`              | Agent     | internal       | Signal task/step completion                        |
| `ask_user`            | Agent     | internal       | Prompt user for information                        |
| `list_step_outputs`   | Agent     | internal       | List completed step results                        |
| `read_step_output`    | Agent     | internal       | Read specific step output                          |
| `set_step_status`     | Agent     | always_allow   | Update step status/checklist                       |
| `store_fact`          | Agent     | always_allow   | Store fact to blackboard                           |
| `search_facts`        | Agent     | always_allow   | Search blackboard facts                            |
| `read_skill_resource` | Agent     | always_allow   | Read skill resource files                          |

## Registration Order

```go
RegisterBuiltinTools(registry, cfg):
  1. bash_exec (with blacklist + timeouts)
  2. File tools (read, write, edit, list, mkdir, rmdir, rm)
  3. finish
  4. web_fetch
  5. web_search (optional: needs search provider config)
  6. glob, ripgrep
  7. read_step_output, list_step_outputs
  8. set_step_status
  9. store_fact, search_facts
  10. semantic_search (optional: needs vector search func)
  11. ask_user (optional: needs ask_user func)
```

Note: `read_skill_resource` is registered separately in `NewOrchestratorBuilder` (not in `RegisterBuiltinTools`).

## File Tools — ToolJudger

File write/edit tools implement the `ToolJudger` interface (`sdk/tools/builtins/file_judge.go`). When a file tool has `PolicyAlwaysAllow` but the target path looks risky (outside workspace, system files), the judge escalates to user confirmation.

## Limits Configuration

| Config                       | Affects                   | Default             |
| ---------------------------- | ------------------------- | ------------------- |
| `FileLimits.ReadMaxBytes`    | read_file                 | 50KB                |
| `RipgrepLimits.MaxResults`   | ripgrep                   | 200                 |
| `GlobLimits.MaxResults`      | glob                      | 200                 |
| `WebFetchLimits.MaxBodySize` | web_fetch                 | 2MB                 |
| `WebSearchLimits.MaxResults` | web_search                | 5                   |
| `BashTimeouts.MaxTimeout`    | bash_exec                 | 120s                |
| `BashTimeouts.WaitDelay`     | bash_exec                 | 100ms               |
| `BashBlacklist`              | bash_exec                 | [] (regex patterns) |

## Adding a New Built-in Tool — Checklist

1. Create `sdk/tools/builtins/<name>.go` implementing `tools.Tool` interface
2. Add constructor: `NewXxxTool(...)` with relevant limits/config
3. Register in `core/tools/builtin_registration.go` → `RegisterBuiltinTools()`
4. If tool needs config, add field to `BuiltinToolsConfig` struct
5. If config comes from config.yaml, update `backend/configadapter.go` → `configToBuiltinToolsConfig()`
6. If tool is mutating (writes files, runs commands), set `DefaultPolicy()` to `PolicyUserConfirm`
7. If tool should be available in specific roles only, update `core/toolprofiles.go`
8. Add tests: `sdk/tools/builtins/<name>_test.go`
9. Run `make lint && make test`

## Invariants

- All built-in tools have static descriptors (name/description/schema don't change at runtime)
- Tools with `PolicyUserConfirm` default ALWAYS require confirmation unless overridden by config
- Limits are applied at tool creation time (immutable after registration)
- Optional tools (semantic_search, web_search, ask_user) are only registered if their dependency func/key is provided
- `ripgrep` invokes the `rg` binary via `exec.CommandContext`; the binary is a hard runtime dependency verified at startup by `desktop.verifyExternalDependencies`
- `ripgrep` parses the `rg --json` event stream (match/context/end events); exit code 1 means "no matches" and is not an error, exit codes ≥ 2 surface as `IsError` with stderr content

## Related Specs

- [README.md](README.md) — tool system overview
- [../../architecture/security-model.md](../../architecture/security-model.md) — policy enforcement
- [../../contracts/core-sdk.md](../../contracts/core-sdk.md) — tool interface boundary
