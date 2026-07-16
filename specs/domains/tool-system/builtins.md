# Built-in Tools

## Role

c0wrk registers sp4rk's built-in tools plus the c0wrk-specific `ask_user` tool at startup via `RegisterBuiltinTools`. This spec documents c0wrk's tool catalog, registration order, and configuration. The tool implementations (filesystem, search, web, agent-infrastructure), file-safety judging, and file-coherence checking are **sp4rk engine** behavior — see [the sp4rk builtins spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/builtins.md).

## Key Files

- `core/tools/builtin_registration.go` — `RegisterBuiltinTools(registry, cfg)` + `BuiltinToolsConfig`
- `core/tools/askuser.go` / `core/tools/askuser_types.go` — c0wrk-specific `ask_user` tool + AskUser request/response types (moved out of sp4rk per ADR-011)
- `core/toolmanager/` — manages external binaries (`rg`, `rtk`, `uv`, `markitdown`), auto-downloaded on first run to `~/.c0wrk/tools/bin/`, PATH-prepended at startup (ADR-010)

Engine files (`github.com/v0lka/sp4rk/tools/builtins/*.go`, including `file_reader.go`, `ripgrep.go`, `tool_result_read.go`, `web_search/`, `batch.go`, `checklist.go`, `coherence.go`) are documented in [the sp4rk builtins spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/builtins.md).

## Tool Catalog

c0wrk's registered tools and their default policy / trust classification:

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
| `ask_user`            | Agent     | internal       | no        | Prompt user for information (c0wrk-specific, `core/tools/askuser.go`) |
| `list_step_outputs`   | Agent     | internal       | no        | List completed step results                        |
| `read_step_output`    | Agent     | internal       | no        | Read specific step output                          |
| `read_final_result`   | Agent     | internal       | no        | Read the prior task's final result from the blackboard |
| `update_checklist`    | Agent     | always_allow   | no        | Update checklist for current step or standalone. Rejects standalone (empty step_id) when a plan is declared via a `ChecklistGuard` in context. |
| `declare_step_complete` | Agent   | always_allow   | no        | Signal inline plan step completion (emits `plan_step_complete`) |
| `store_fact`          | Agent     | always_allow   | no        | Store fact to blackboard                           |
| `search_facts`        | Agent     | always_allow   | no        | Search blackboard facts                            |
| `read_attachment`     | Agent     | always_allow   | no        | Read the markdown content of a user-attached file by ID (from the context-injected `AttachmentStore`) |
| `batch`               | Agent     | always_allow   | no        | Execute multiple tool calls sequentially in one turn (intercepted at executor level) |
| `read_skill_resource` | Agent     | always_allow   | no        | Read skill resource files                          |
| `tool_result_read`    | Agent     | internal       | no        | Read cached tool result fragments by hash          |

Internal tools (`finish`, `ask_user`, `list_step_outputs`, `read_final_result`, `read_step_output`, `tool_result_read`, `batch`, `search_facts`, `semantic_search`, `store_fact`, `read_skill_resource`, `update_checklist`, `declare_step_complete`) bypass policy checks during execution. `batch` is marked internal but is intercepted at the executor level before reaching the registry.

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
  10. read_attachment (reads user-attached files via the context-injected `AttachmentStore`; attachment IDs are surfaced by `Orchestrator.augmentWithAttachments`)
  11. tool_result_read
  12. batch
  13. semantic_search (optional: needs vector search func)
  14. ask_user (optional: needs ask_user func)
```

Note: `read_skill_resource` is registered separately in `NewOrchestratorBuilder` (not in `RegisterBuiltinTools`).

## `ask_user` (c0wrk-specific)

The `ask_user` tool and its UI types (`AskUserFunc`, `AskUserRequest`, `AskUserResponse`, `AskUserQuestion`, `AskUserAnswer`, `AskUserOption`) live in `core/tools/` — they were moved out of sp4rk per ADR-011 because they are host-application UI concerns. The tool is registered only when an `ask_user` func is provided.

## Tool-Manager Wiring (`rg`)

`ripgrep` invokes the `rg` binary via `exec.CommandContext` and parses the `rg --json` event stream (exit code 1 = "no matches", not an error; ≥ 2 = `IsError`). The `rg` binary is a managed runtime dependency provided by `core/toolmanager/` — downloaded on first run to `~/.c0wrk/tools/bin/` and PATH-prepended at startup (ADR-010).

## Limits Configuration

Per-tool output truncation is centralized in the executor's two-stage pipeline (see [../orchestration/executor.md](../orchestration/executor.md)). No individual tool performs per-tool output truncation; all truncation happens in Stage 1 → Stage 2 after the full result is cached.

**`read_file` is special**: it uses a streaming line-range reader (sp4rk `ReadFileRange`) that reads only the requested window from disk — O(1) memory. The file on disk serves as the cache backing store (file-backed cache entry), so `ToolResultCache` stores zero bytes of content for `read_file` results.

| Config                              | Affects                    | Default            |
| ----------------------------------- | -------------------------- | ------------------ |
| `toolLimits.readDefaultLines`       | `read_file` window size    | 2000               |
| `toolLimits.perToolTruncation`      | All cacheable tools (map)  | per-tool           |
| `toolResultBudget.cacheTTLSeconds`  | ToolResultCache eviction   | 300                |

Default per-tool Stage 1 truncation: `read_file` 2000 lines, `ripgrep` 5000, `glob` 2000, `list_directory` 2000, `bash_exec` 10000, `web_fetch` 2097152 bytes (2 MiB).

`read_file` internal safety caps (hardcoded in `DefaultFileLimits()`, not configurable): `MaxLineBytes` 1 MiB (per-line), `MaxWindowLines` 50000 (hard cap per call).

Non-truncation tool limits:

| Config                       | Affects                   | Default             |
| ---------------------------- | ------------------------- | ------------------- |
| `BashTimeouts.MaxTimeout`    | bash_exec                 | 120s                |
| `BashTimeouts.WaitDelay`     | bash_exec                 | 5s                  |
| `BashBlacklist`              | bash_exec                 | [] (regex patterns) |
| `WebSearchLimits.MaxResults` | web_search                | 5                   |

## Engine Behavior (canonical in sp4rk)

The following are sp4rk engine primitives, documented in [the sp4rk builtins spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/builtins.md) — do not duplicate here:

- Tool implementations (file_reader streaming, ripgrep `--json` parsing, web_search provider abstraction, batch interception, checklist validation)
- File-safety judging (`judgeReadInSessionRoots` / `judgeWriteInSessionRoots`, the 11 `ToolJudger` implementations) — c0wrk's session-root/auto-approval layer on top is in [../../architecture/security-model.md](../../architecture/security-model.md)
- File-coherence checking (`FileCoherenceChecker`, cross-session conflict detection) — wired into context by the c0wrk session manager
- `tool_result_read` fragment reading (file-backed + content-backed entries, coherence validation)

## Adding a New Built-in Tool — Checklist

1. Create the tool file: sp4rk builtins go in `github.com/v0lka/sp4rk/tools/builtins/<name>.go`; c0wrk-specific tools (like `ask_user`) go in `core/tools/`
2. Implement the sp4rk `Tool` interface; add constructor `NewXxxTool(...)` with relevant limits/config
3. Register in `core/tools/builtin_registration.go` → `RegisterBuiltinTools()`
4. If the tool needs config, add a field to `BuiltinToolsConfig`
5. If config comes from `config.yaml`, update `core/builder.go` → `configToBuiltinToolsConfig()`
6. If the tool reads data from external sources (filesystem, web, subprocess), set `Untrusted: true` on `BaseTool` so output is wrapped before entering the LLM context
7. If the tool is mutating (writes files, runs commands), set `DefaultPolicy: PolicyUserConfirm`

## Related Specs

- [sp4rk builtins](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/builtins.md) — canonical tool implementations, file-safety judging, coherence checking
- [README.md](README.md) — tool system overview
- [../../architecture/security-model.md](../../architecture/security-model.md) — policy enforcement and session-root auto-approval
- [../../contracts/core-sp4rk.md](../../contracts/core-sp4rk.md) — tool interface boundary
