# Built-in Tools

## Role

Provide filesystem, search, web, execution, and agent-infrastructure tools out of the box. Registered at startup in `RegisterBuiltinTools`.

## Key Files

- `sdk/tools/builtins/*.go` — tool implementations
- `sdk/tools/builtins/file_reader.go` — streaming line-range reader (`ReadFileRange`), O(1) memory, reusable by custom tools
- `sdk/tools/builtins/ripgrep.go` — wraps the `rg` CLI (`--json` event stream)
- `sdk/tools/builtins/tool_result_read.go` — reads cached tool result fragments by hash
- `sdk/tools/builtins/coherence_format.go` — conflict message formatting for cross-session coherence
- `sdk/tools/builtins/web_search/` — web search provider abstraction (brave, duckduckgo, exa, tavily)
- `sdk/tools/builtins/web_search/brave.go` — Brave Search API provider
- `sdk/tools/builtins/web_search/duckduckgo.go` — DuckDuckGo Instant Answer API provider
- `sdk/tools/builtins/web_search/exa.go` — Exa Search API provider
- `sdk/tools/builtins/web_search/tavily.go` — Tavily Search API provider
- `sdk/tools/builtins/doc.go` — package documentation
- `sdk/tools/builtins/limits.go` — limit types (per-tool truncation limits, ripgrep/read-file limits). `BuiltinToolsConfig` is defined in `core/tools/builtin_registration.go`, not here.
- `sdk/tools/builtins/paths.go` — workspace path resolution
- `sdk/tools/builtins/workspace.go` — workspace detection, `read_step_output`, `list_step_outputs`, `read_final_result` tools (blackboard-backed)
- `sdk/tools/builtins/netcheck.go` — network connectivity check for web tools
- `sdk/tools/builtins/batch.go` — batch meta-tool (sequential sub-call execution intercepted at executor level)
- `sdk/tools/builtins/checklist.go` — `update_checklist` tool (validates Markdown checkboxes, consults `ChecklistGuardFunc`, emits `step_todo_update` via callback)
- `core/tools/askuser.go` — ask_user tool implementation (c0wrk-specific, moved from sdk/)
- `core/tools/askuser_types.go` — AskUser request/response types
- `sdk/tools/coherence.go` — FileCoherenceChecker interface, FileSig, CoherenceConflict types
- `core/tools/builtin_registration.go` — registration function + config types

## Behavior

### Tool Registration

All built-in tools are registered at startup via `RegisterBuiltinTools(registry, cfg)` (in `core/tools/builtin_registration.go`). Registration is ordered: bash_exec → read_file → write_file → edit_file → list_directory → create_directory → delete_directory → delete_file → finish → web_fetch → web_search → glob → ripgrep → tool_result_read → batch → read_step_output → list_step_outputs → read_final_result → update_checklist → declare_step_complete → … Internal tools (`finish`, `ask_user`, `list_step_outputs`, `read_final_result`, `read_step_output`, `tool_result_read`, `batch`, `search_facts`, `semantic_search`, `store_fact`, `read_skill_resource`, `update_checklist`, `declare_step_complete` — 13 total) always bypass policy checks during execution. `batch` is marked internal (`internalTools` map) but is intercepted at the executor level before reaching the registry — its policy is `always_allow` to ensure the LLM can always use the schema.

### Policy Resolution

Each tool's effective policy is resolved at execution time:

1. Check per-tool override in config (`security.tool_policies`)
2. Fall back to skill-specified policy (if tool is invoked by a skill)
3. Fall back to global default (`security.default_policy`)
4. Fall back to tool's own `DefaultPolicy()`

### File Safety Judging

Eleven built-in tools implement `ToolJudger`: `write_file`, `edit_file`, `delete_file`, `delete_directory`, `create_directory`, `bash_exec` (all `PolicyUserConfirm` — judged before policy check), plus `read_file`, `list_directory`, `glob`, `ripgrep`, `web_fetch` (all `PolicyAlwaysAllow` — judge can escalate to user confirmation). File tools use `judgeReadInSessionRoots` / `judgeWriteInSessionRoots` (in `sdk/tools/builtins/file_judge.go`) to check whether the target path is inside the session workspace or temp directory (equal peers). The judge escalates to user confirmation if the path:
- Is outside both session roots (workspace AND temp directory)
- References system directories (`/etc`, `/usr`, `/System`, etc.)
- Contains path traversal sequences (`../`)

### File Coherence Checking

File tools perform cross-session conflict detection via `FileCoherenceChecker` (injected into context by the session manager). The checker tracks file signatures (mtime + size) per session and detects when a file was modified by another session since the current session last read it.

**Protocol:**

- `read_file`: before reading, calls `CheckRead` to record the current file signature. If the file changed since this session's previous read, prepends a warning annotation to the result (non-blocking).
- `write_file`, `edit_file`, `delete_file`: before mutating, checks the file's current signature against this session's last-read snapshot. If mismatched, returns `IsError: true` with a conflict message instructing the LLM to re-read.
- `bash_exec`: not covered (bash modifications are detected naturally by subsequent coherence checks on affected files).

**Atomicity:** Each file tool acquires a per-file in-process mutex (`Lock`/`Unlock`) around the check-then-act window to eliminate TOCTOU races between concurrent sessions.

**Conflict resolution:** The LLM receives the conflict as a tool error and decides how to proceed (typically by re-reading the file and retrying the edit with updated content).

### Tool Execution

All built-in tools accept `json.RawMessage` input and return `ToolResult{Content, IsError}`. Tools that shell out (`ripgrep`, `bash_exec`) use `exec.CommandContext` with the caller's context.

## Error Handling

- **Tool not found**: `ToolRegistry.Execute()` returns `ToolResult{IsError: true}` with an "unknown tool" message — does not panic
- **Path validation**: file tools resolve paths via `resolvePath` (relative paths joined with workspace root and must stay within it; absolute paths symlink-resolved and returned regardless of containment). Containment within session roots (workspace or temp) is checked by the Judge layer, not by `Execute` — operations outside session roots are allowed after user confirmation. Relative paths that escape the workspace via `..` are rejected as invalid input.
- **Bash blacklist**: commands matching blacklist patterns are rejected with `IsError: true` and a "blocked by security policy" message
- **Ripgrep**: exit code 1 ("no matches") is NOT an error; exit codes ≥ 2 produce `IsError` with stderr content
- **Web tools**: network errors surface as `IsError` with a descriptive message; timeout errors include the configured timeout value
- **File I/O errors**: propagated as `IsError` with the OS error message; binary files detected by null byte presence are rejected with an appropriate message
- **File coherence conflict**: when a file was modified since the session's last read, `write_file`/`edit_file`/`delete_file` return `IsError` with a conflict description and instruction to re-read; `read_file` prepends a non-blocking warning annotation
- **Optional tool absence**: if a dependency func/key is not provided (e.g., no web search API key), the tool is silently not registered (no error at registration time)

## Tool Catalog

| Tool                  | Category  | Default Policy | Untrusted | Description                                        |
| --------------------- | --------- | -------------- | --------- | -------------------------------------------------- |
| `bash_exec`           | Execution | user_confirm   | yes       | Shell command execution with timeout and blacklist |
| `read_file`           | File      | always_allow   | yes       | Read file contents (streaming, O(1) memory, default 2000-line window) |
| `write_file`          | File      | user_confirm   | no        | Create/overwrite file                              |
| `edit_file`           | File      | user_confirm   | no        | Apply targeted edits to existing file              |
| `list_directory`      | File      | always_allow   | no        | List directory contents                            |
| `create_directory`    | File      | user_confirm   | no        | Create directory (recursive)                       |
| `delete_directory`    | File      | user_confirm   | no        | Remove directory recursively                       |
| `delete_file`         | File      | user_confirm   | no        | Remove single file                                 |
| `glob`                | Search    | always_allow   | yes       | Glob pattern file matching                         |
| `ripgrep`             | Search    | always_allow   | yes       | Fast regex content search (shells out to `rg`)     |
| `semantic_search`     | Search    | always_allow   | no        | Vector similarity search (optional)                |
| `web_fetch`           | Web       | always_allow   | yes       | Fetch URL content                                  |
| `web_search`          | Web       | always_allow   | yes       | Search the web (optional, needs API key)           |
| `finish`              | Agent     | internal       | no        | Signal task/step completion                        |
| `ask_user`            | Agent     | internal       | no        | Prompt user for information                        |
| `list_step_outputs`   | Agent     | internal       | no        | List completed step results                        |
| `read_step_output`    | Agent     | internal       | no        | Read specific step output                          |
| `read_final_result`   | Agent     | internal       | no        | Read the prior task's final result from the blackboard (recovers a prior exchange's outcome when it is not visible in conversation history) |
| `update_checklist`    | Agent     | always_allow   | no        | Update checklist for current step or standalone (no plan). Rejects standalone (empty step_id) when a plan is declared via a ChecklistGuard in context. Result includes an incremental-update reminder when items remain unchecked. |
| `declare_step_complete` | Agent   | always_allow   | no        | Signal inline plan step completion (emits plan_step_complete) |
| `store_fact`          | Agent     | always_allow   | no        | Store fact to blackboard                           |
| `search_facts`        | Agent     | always_allow   | no        | Search blackboard facts                            |
| `batch`               | Agent     | always_allow   | no        | Execute multiple tool calls sequentially in one turn (intercepted at executor level) |
| `read_skill_resource` | Agent     | always_allow   | no        | Read skill resource files                          |
| `tool_result_read`    | Agent     | internal       | no        | Read cached tool result fragments by hash          |

## Registration Order

```go
RegisterBuiltinTools(registry, cfg):
  1. bash_exec (with blacklist + timeouts)
  2. File tools (read, write, edit, list, mkdir, rmdir, rm)
  3. finish
  4. web_fetch
  5. web_search (optional: needs search provider config)
  6. glob, ripgrep
  7. read_step_output, list_step_outputs, read_final_result
  8. update_checklist, declare_step_complete
  9. store_fact, search_facts
  10. tool_result_read
  11. batch
  12. semantic_search (optional: needs vector search func)
  13. ask_user (optional: needs ask_user func)
```

Note: `read_skill_resource` is registered separately in `NewOrchestratorBuilder` (not in `RegisterBuiltinTools`).

## File Tools — ToolJudger

Eleven built-in tools implement `ToolJudger` (see File Safety Judging above). When a tool with `PolicyAlwaysAllow` has a judge and the target path looks risky (outside session roots, system files), the judge escalates to user confirmation. The judge runs **before** workspace/temp auto-approval (see PolicyAlwaysAllow Judge Gate in [security-model.md](../../architecture/security-model.md)) — safety checks are never bypassed by path-locality.

## Limits Configuration

Per-tool output truncation is centralized in the executor's two-stage pipeline (see [executor.md](../orchestration/executor.md)). All truncation happens exclusively in the centralized truncation pipeline (Stage 1 → Stage 2), after the full result is cached. No individual tool performs per-tool output truncation.

**`read_file` is special**: it uses a streaming line-range reader (`ReadFileRange` in `file_reader.go`) that reads only the requested window from disk — O(1) memory, the file is never fully materialized. When no line range is specified, a default window of `ReadDefaultLines` (2000) lines is returned. The file on disk serves as the cache backing store (file-backed cache entry — see [executor.md](../orchestration/executor.md)), so `ToolResultCache` stores zero bytes of content for `read_file` results. Stage 1 truncation acts as a secondary safety net on the already-capped window.

| Config                              | Affects                    | Default            |
| ----------------------------------- | -------------------------- | ------------------ |
| `toolLimits.readDefaultLines`       | `read_file` window size    | 2000               |
| `toolLimits.perToolTruncation`      | All cacheable tools (map)  | per-tool (see below)|
| `toolResultBudget.cacheTTLSeconds`  | ToolResultCache eviction   | 300                |

> Stage 1 is a memory-exhaustion prevention layer. Values are conservative (2000 lines for most tools, 5000 for ripgrep) — truncation triggers on large outputs to keep context manageable. Defaults are configurable via `config.yaml` `toolLimits.perToolTruncation`. For `read_file`, the primary cap is the streaming reader's `MaxWindowLines` (50000, hardcoded) — Stage 1 MaxLines is a secondary safety net on the already-windowed output.

Default per-tool Stage 1 truncation:

| Tool           | MaxLines      | MaxBytes        |
| -------------- | ------------- | --------------- |
| `read_file`    | 2000          | — (secondary; primary cap is streaming window) |
| `ripgrep`      | 5000          | —               |
| `glob`         | 2000          | —               |
| `list_directory`| 2000         | —               |
| `web_fetch`    | —             | 2097152 (2 MiB)  |
| `bash_exec`    | 10000         | —               |

`read_file` internal safety caps (hardcoded in `DefaultFileLimits()`, not configurable):

| Cap               | Default | Purpose                                                     |
| ----------------- | ------- | ----------------------------------------------------------- |
| `MaxLineBytes`    | 1 MiB   | Per-line byte cap; lines exceeding this are truncated with a marker |
| `MaxWindowLines`  | 50000   | Hard cap on lines returned per call even for explicit ranges |

Non-truncation tool limits (timeouts, search limits, etc.)— still in code:

| Config                       | Affects                   | Default             |
| ---------------------------- | ------------------------- | ------------------- |
| `BashTimeouts.MaxTimeout`    | bash_exec                 | 120s                |
| `BashTimeouts.WaitDelay`     | bash_exec                 | 5s                  |
| `BashBlacklist`              | bash_exec                 | [] (regex patterns) |
| `WebSearchLimits.MaxResults` | web_search                | 5                   |

## Adding a New Built-in Tool — Checklist

1. Create `sdk/tools/builtins/<name>.go` implementing `tools.Tool` interface (c0wrk-specific tools like `ask_user` go in `core/tools/`)
2. Add constructor: `NewXxxTool(...)` with relevant limits/config
3. Register in `core/tools/builtin_registration.go` → `RegisterBuiltinTools()`
4. If tool needs config, add field to `BuiltinToolsConfig` struct
5. If config comes from config.yaml, update `core/builder.go` → `configToBuiltinToolsConfig()`
6. If the tool reads data from external sources (filesystem, web, subprocess), set `Untrusted: true` on `BaseTool` so output is wrapped before entering the LLM context
7. If tool is mutating (writes files, runs commands), set `DefaultPolicy()` to `PolicyUserConfirm`
8. If tool should be available in specific roles only, update `core/toolprofiles.go`
9. Add tests: `sdk/tools/builtins/<name>_test.go`
10. If the tool reads files, use `ReadFileRange` from `file_reader.go` for O(1) memory streaming (see `read_file` for reference)
11. Run `make lint && make test`

## Invariants

- `batch` is intercepted at the executor level — `BatchTool.Execute()` returns an error if called directly; the executor parses the batch input and runs each sub-call through the full policy + truncation + caching pipeline sequentially, emitting each sub-call as `"<original_tool> (batched)"` via the event emitter
- All built-in tools have static descriptors (name/description/schema don't change at runtime)
- Tools with `PolicyUserConfirm` default ALWAYS require confirmation unless overridden by config
- Limits are applied at tool creation time (immutable after registration); output truncation limits are centralized in the executor, not in individual tools
- `tool_result_read` and `batch` are non-cacheable internal tools that bypass policy checks and the caching layer; `batch` is intercepted at the executor level and never reaches the registry's `Execute()` path
- Per-tool Stage 1 truncation produces a fragmentation nudge with the SHA256 hash of the full result; the LLM uses `tool_result_read(hash, start_line, num_lines)` to retrieve fragments
- `read_file` uses a streaming line-range reader (`ReadFileRange`) — O(1) memory, the file is never fully materialized. Default window is `ReadDefaultLines` (2000) lines when no range is specified. `MaxLineBytes` (1 MiB) and `MaxWindowLines` (50000) are hardcoded safety caps
- `read_file` cache entries are file-backed: `ToolResultCache` stores zero bytes of content (the file on disk is the backing store). The hash is derived from file metadata (path + mtime + size), stable for an unchanged file. `tool_result_read` streams fragments from disk for file-backed entries
- `tool_result_read` serves both truncation recovery and token economy for `read_file`: a file-backed nudge with the cache hash is appended even when Stage 1 truncation did not fire, informing the LLM that additional fragments are available via `tool_result_read(hash, start_line, num_lines)`
- Optional tools (semantic_search, web_search, ask_user) are only registered if their dependency func/key is provided
- `ripgrep` invokes the `rg` binary via `exec.CommandContext`; the binary is a managed runtime dependency provided by the tool-manager (`core/toolmanager/`), downloaded on first run to `~/.c0wrk/tools/bin/`, and PATH-prepended at startup (see ADR-010)
- `ripgrep` parses the `rg --json` event stream (match/context/end events); exit code 1 means "no matches" and is not an error, exit codes ≥ 2 surface as `IsError` with stderr content
- Untrusted built-in tools (`bash_exec`, `read_file`, `glob`, `ripgrep`, `web_fetch`, `web_search`) have `Untrusted: true` on `BaseTool`; their output is wrapped in `<untrusted-content>` tags before entering the LLM context
- `update_checklist` consults a `ChecklistGuardFunc` from context (if present) after parsing and before emitting the update. A non-empty return string rejects the call with that message as an `IsError` tool result. The Conductor installs a guard that rejects standalone (empty `step_id`) checklists once a plan is declared on the blackboard — a standalone checklist is only valid for plan-less tasks
- `update_checklist` tool result includes an incremental-update reminder ("Remember to call update_checklist again after completing the next item") when items remain unchecked; all-checked checklists produce a plain "N/N done" message

## Related Specs

- [README.md](README.md) — tool system overview
- [../../architecture/security-model.md](../../architecture/security-model.md) — policy enforcement
- [../../contracts/core-sdk.md](../../contracts/core-sdk.md) — tool interface boundary
