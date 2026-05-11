# Built-in Tools

## Role

Provide filesystem, search, web, execution, and agent-infrastructure tools out of the box. Registered at startup in `RegisterBuiltinTools`.

## Key Files

- `sdk/tools/builtins/*.go` — tool implementations
- `sdk/tools/builtins/ripgrep.go` — wraps the `rg` CLI (`--json` event stream)
- `sdk/tools/builtins/web_search/` — web search provider abstraction
- `core/tools/builtin_registration.go` — registration function + config types

## Tool Catalog

| Tool                  | Category  | Default Policy | Description                                        |
| --------------------- | --------- | -------------- | -------------------------------------------------- |
| `bash_exec`           | Execution | user_confirm   | Shell command execution with timeout and blacklist |
| `read_file`           | File      | always_allow   | Read file contents (with size limits)              |
| `write_file`          | File      | always_allow   | Create/overwrite file                              |
| `edit_file`           | File      | always_allow   | Apply targeted edits to existing file              |
| `list_directory`      | File      | always_allow   | List directory contents                            |
| `search_files`        | File      | always_allow   | Find files by name pattern                         |
| `search_content`      | File      | always_allow   | Search file contents (grep-like)                   |
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
  2. File tools (read, write, edit, list, search, search_content, mkdir, rmdir, rm)
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
| `FileLimits.MaxReadSize`     | read_file, search_content | 1MB                 |
| `RipgrepLimits.MaxMatches`   | ripgrep                   | 100                 |
| `GlobLimits.MaxResults`      | glob                      | 1000                |
| `WebFetchLimits.MaxSize`     | web_fetch                 | 512KB               |
| `WebSearchLimits.MaxResults` | web_search                | 10                  |
| `BashTimeouts.Default`       | bash_exec                 | 30s                 |
| `BashTimeouts.Max`           | bash_exec                 | 300s                |
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
