# Built-in Tools

## Role

c0wrk registers sp4rk's built-in tools plus the c0wrk-specific `ask_user` tool at startup via `RegisterBuiltinTools`. This spec documents c0wrk's tool catalog, registration order, and configuration. The tool implementations (filesystem, search, web, agent-infrastructure), file-safety judging, and file-coherence checking are **sp4rk engine** behavior — see [the sp4rk builtins spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/builtins.md).

## Key Files

- `core/tools/builtin_registration.go` — `RegisterBuiltinTools(registry, cfg)` + `BuiltinToolsConfig`; assembles the merged shell blacklist (config + No-Project extra patterns) and delegates the platform-specific constructor call to `newShellExecTool`
- `core/tools/shelltool_unix.go` / `core/tools/shelltool_windows.go` — build-tag split for the shell-exec tool constructor (`builtins.NewBashExecToolWithTimeouts` on Unix, `builtins.NewPoshExecToolWithTimeouts` on Windows); sp4rk's `bash.go`/`posh.go` are mutually exclusive per OS
- `core/tools/read_file_doc.go` — c0wrk `ReadFileDocTool` wrapper over sp4rk `ReadFileTool` that converts document formats (pdf, docx, pptx, xlsx, odt, html, htm) to markdown via `core/markitdown`; implements sp4rk's `ContentBackedReader` so converted results are content-backed cached
- `core/tools/askuser.go` / `core/tools/askuser_types.go` — c0wrk-specific `ask_user` tool + AskUser request/response types (moved out of sp4rk per ADR-011)
- `core/toolmanager/` — manages external binaries (`rg`, `uv`, `markitdown`), auto-downloaded on first run to `~/.c0wrk/tools/bin/`, PATH-prepended at startup (ADR-010)

Engine files (`github.com/v0lka/sp4rk/tools/builtins/*.go`, including `file_reader.go`, `ripgrep.go`, `tool_result_read.go`, `web_search/`, `batch.go`, `checklist.go`, `coherence.go`) are documented in [the sp4rk builtins spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/builtins.md).

## Tool Catalog

c0wrk's registered tools and their default policy / trust classification:

| Tool                  | Category  | Default Policy | Untrusted | Description                                        |
| --------------------- | --------- | -------------- | --------- | -------------------------------------------------- |
| `bash_exec` / `posh_exec` | Execution | user_confirm | yes | Shell command execution with timeout and blacklist. `bash_exec` (bash) on Unix, `posh_exec` (PowerShell) on Windows — exactly one registers, selected by build tag (see [Shell-Execution Tool](#shell-execution-tool-bash_exec--posh_exec)) |
| `read_file`           | File      | always_allow   | yes       | Read file contents (streaming, O(1) memory, default 2000-line window); document formats (pdf, docx, pptx, xlsx, odt, html, htm) auto-converted to markdown via markitdown |
| `write_file`          | File      | user_confirm   | no        | Create/overwrite file                              |
| `edit_file`           | File      | user_confirm   | no        | Apply targeted edits to existing file              |
| `list_directory`      | File      | always_allow   | no        | List directory contents                            |
| `create_directory`    | File      | user_confirm   | no        | Create directory (recursive)                       |
| `delete_directory`    | File      | user_confirm   | no        | Remove directory recursively                       |
| `delete_file`         | File      | user_confirm   | no        | Remove single file                                 |
| `glob`                | Search    | always_allow   | yes       | Glob pattern file matching                         |
| `ripgrep`             | Search    | always_allow   | yes       | Fast regex content search (shells out to `rg`)     |
| `semantic_search`     | Search    | internal       | no        | Vector similarity search (optional)                |
| `web_fetch`           | Web       | always_allow   | yes       | Fetch URL content                                  |
| `web_search`          | Web       | always_allow   | yes       | Search the web (optional, needs API key)           |
| `finish`              | Agent     | internal       | no        | Signal task/step completion                        |
| `ask_user`            | Agent     | internal       | no        | Prompt user for information (c0wrk-specific, `core/tools/askuser.go`) |
| `propose_goal`        | Agent     | internal       | no        | Goal-mode derivation: submit a {condition, verify} goal proposal for user sign-off. Blocks until the user approves (optionally with edits), clarifies, or cancels. A no-op outside a derivation Conductor run (no `GoalProposer` in context). See [../goal-mode.md](../goal-mode.md). |
| `declare_goal_status` | Agent     | internal       | no        | Goal-mode self-evaluation: write a structured {status, evidence, reason} verdict into the context-injected `GoalStatusSink`. Status `"met"` **requires non-empty evidence** (each entry must have non-empty `type`/`ref`/`summary`). A no-op outside a goal-loop turn (no sink in context). See [../goal-mode.md](../goal-mode.md). |
| `list_step_outputs`   | Agent     | internal       | no        | List completed step results                        |
| `read_step_output`    | Agent     | internal       | no        | Read specific step output                          |
| `read_final_result`   | Agent     | internal       | no        | Read the prior task's final result from the blackboard |
| `update_checklist`    | Agent     | internal       | no        | Update checklist for current step or standalone. Rejects standalone (empty step_id) when a plan is declared via a `ChecklistGuard` in context. |
| `declare_step_complete` | Agent   | internal       | no        | Signal inline plan step completion (emits `plan_step_complete`) |
| `store_fact`          | Agent     | internal       | no        | Store fact to blackboard                           |
| `search_facts`        | Agent     | internal       | no        | Search blackboard facts                            |
| `read_attachment`     | Agent     | internal       | no        | Read the markdown content of a user-attached file by ID (from the context-injected `AttachmentStore`) |
| `batch`               | Agent     | internal       | no        | Execute multiple tool calls sequentially in one turn (intercepted at executor level) |
| `read_skill_resource` | Agent     | internal       | no        | Read skill resource files                          |
| `tool_result_read`    | Agent     | internal       | no        | Read cached tool result fragments by hash          |
| `delegate`            | Agent     | internal       | no        | Launch a subagent for a delegated task (`core/tools/delegate.go`) |
| `cancel_delegation`   | Agent     | internal       | no        | Cancel a running delegation (`core/tools/cancel_delegation.go`) |
| `reflect`             | Agent     | internal       | no        | Trigger a reflection pass over the task (`core/tools/reflect.go`) |
| `declare_plan`        | Agent     | internal       | no        | Publish a plan for user sign-off (`core/tools/declare_plan.go`) |
| `execute_plan`        | Agent     | internal       | no        | Execute a declared plan inline (`core/tools/execute_plan.go`) |

The internal-tools set is defined in `core/tools/registry.go` (`internalTools`): `ask_user`, `delegate`, `cancel_delegation`, `declare_plan`, `execute_plan`, `propose_goal`, `declare_goal_status`, `declare_verification`, `reflect`, `finish`, `list_step_outputs`, `read_step_output`, `read_final_result`, `read_skill_resource`, `read_attachment`, `search_facts`, `semantic_search`, `update_checklist`, `declare_step_complete`, `store_fact`, `tool_result_read`, and `batch` (`sdktools.ToolBatch`). All of them bypass policy/judge checks during execution. `batch` is additionally intercepted at the executor level before reaching the registry.

Three internal tools are **goal-mode-only** (`goalModeTools`, gated by `IsGoalModeTool`/`StripGoalModeTools`): `propose_goal`, `declare_goal_status`, and `declare_verification`. They are offered to the agent **only** when the session is running a goal loop — a non-goal Conductor run strips them from the available-tool list so the agent never sees goal-specific tools when goal mode is off. The goal loop and the independent verifier receive the unstripped list. See [../goal-mode.md](../goal-mode.md).

### Shell-Execution Tool (`bash_exec` / `posh_exec`)

The shell-execution tool is platform-specific: sp4rk's `bash.go` is `//go:build !windows` and `posh.go` is `//go:build windows`, and they are mutually exclusive per OS. A single unconditional constructor call would fail to compile on the other OS, so the registration path is split behind build tags:

- `core/tools/shelltool_unix.go` → `builtins.NewBashExecToolWithTimeouts` → registers `bash_exec`
- `core/tools/shelltool_windows.go` → `builtins.NewPoshExecToolWithTimeouts` → registers `posh_exec`

Both expose the same constructor signature `newShellExecTool(blacklist, timeouts)`; the caller (`RegisterBuiltinTools`) assembles the merged blacklist (config + No-Project extra patterns) and passes it through unchanged. The registered name differs per platform, so all name-keyed configuration and policy lookups resolve through `core.activeShellToolName()` (`bash_exec` on Unix, `posh_exec` on Windows) — see [Blacklist / Policy Key](#blacklist--policy-key) below and [../../architecture/security-model.md](../../architecture/security-model.md).

Prompt data references the shell tool through the `{shell_tool}` placeholder rather than a hardcoded name, so tool-priority guidance always points at the tool actually registered on the current platform. The placeholder is resolved by `prompts.SubstituteShellTool` at each prompt-assembly call site (`core/systemprompt.go`); the embedded prompt vars are kept as raw templates (placeholder recoverable).

#### Blacklist / Policy Key

The blacklist and per-tool policy are read from the policy entry whose key matches the platform's active shell tool name (`bash_exec` on Unix, `posh_exec` on Windows). In `core/builder.go` → `configToBuiltinToolsConfig`, the blacklist is sourced from `cfg.Security.ToolPolicies[activeShellToolName()].Blacklist`; in `applySecurityPolicies` the policy is resolved the same way. A Windows deployment therefore configures `posh_exec`, not `bash_exec`.

## Registration Order

```go
RegisterBuiltinTools(registry, cfg):
  1. shell-exec tool — `bash_exec` on Unix, `posh_exec` on Windows (with blacklist + timeouts; constructor call split by build tag in `core/tools/shelltool_{unix,windows}.go`)
  2. File tools (read, write, edit, list, mkdir, rmdir, rm)
     — read_file is registered as `NewReadFileDocTool`, a wrapper over sp4rk `ReadFileTool` that transparently converts document formats to markdown (plain-text files delegate to the inner tool unchanged)
  3. finish
  4. web_fetch
  5. web_search (optional: needs search provider config)
  6. glob, ripgrep
  7. tool_result_read
  8. batch
  9. read_step_output, list_step_outputs, read_final_result
  10. update_checklist, declare_step_complete
  11. store_fact, search_facts
  12. read_attachment (reads user-attached files via the context-injected `AttachmentStore`; attachment IDs are surfaced by `Orchestrator.augmentWithAttachments`)
  13. semantic_search (optional: needs vector search func)
  14. ask_user (optional: needs ask_user func)
  15. delegate, cancel_delegation, reflect — delegation/reflection coordination primitives (always registered; no-ops without a `DelegationRegistry`/`ReflectionRunner`)
  16. declare_plan, execute_plan — plan declaration/execution coordination primitives (always registered; `declare_plan`'s `await_approval` mode needs a `PlanApprovalFunc`)
  17. propose_goal — goal-mode derivation coordination primitive (always registered; a no-op outside a derivation Conductor run)
  18. declare_goal_status — goal-mode self-evaluation verdict writer (always registered; a no-op outside a goal-loop turn; `met` requires non-empty evidence)
```

Note: `read_skill_resource` is registered separately in `NewOrchestratorBuilder` (not in `RegisterBuiltinTools`).

## `ask_user` (c0wrk-specific)

The `ask_user` tool and its UI types (`AskUserFunc`, `AskUserRequest`, `AskUserResponse`, `AskUserQuestion`, `AskUserAnswer`, `AskUserOption`) live in `core/tools/` — they were moved out of sp4rk per ADR-011 because they are host-application UI concerns. The tool is registered only when an `ask_user` func is provided.

## Goal-Mode Tools (`propose_goal`, `declare_goal_status`)

Goal mode adds two internal coordination tools (both `PolicyAlwaysAllow` — they bypass the tool judge because they are coordination primitives, not user-facing capabilities). They are safe to register unconditionally and are no-ops outside a goal-mode run (the context value they read is nil).

- **`propose_goal`** (`core/tools/propose_goal.go`) — used by the derivation agent to submit a {condition, verify, clarification?, needs_clarification?} goal for user sign-off. It reads a `GoalProposer` from the context (`GoalProposerFrom`), which the orchestrator injects during `deriveGoal` (desktop supplies the implementation that emits a `goal_proposal` event and blocks for the user response). The approved (possibly user-edited) values are echoed back so the agent commits to the user's wording. A no-op (clear error) when no proposer is in context.
- **`declare_goal_status`** (`core/tools/declare_goal_status.go`) — the single channel through which the goal loop learns a structured verdict. It writes a typed `goal.Verdict` into the per-turn `GoalStatusSink` (`GoalStatusSinkFrom`), which `runGoalTurns` injects. **Declaring status `"met"` requires non-empty evidence** — enforced at the tool boundary so a bare "done" can never terminate the loop without a concrete, inspectable artifact. The tool executor does not validate inputs against the JSON schema, so the check rejects both an absent array and a present-but-empty entry (`evidence:[{}]`, `evidence:[{"ref":""}]`): each entry must have non-empty `type`, `ref`, and `summary`. A no-op (clear error) when no sink is in context.

Both follow the same context-injection pattern as `declare_plan`/`ask_user`: the orchestrator injects the dependency via a context value before the relevant Conductor run; the tool reads it back at execution time. See [../goal-mode.md](../goal-mode.md) for the full goal-mode lifecycle.

## Tool-Manager Wiring (`rg`)

`ripgrep` invokes the `rg` binary via `exec.CommandContext` and parses the `rg --json` event stream (exit code 1 = "no matches", not an error; ≥ 2 = `IsError`). The `rg` binary is a managed runtime dependency provided by `core/toolmanager/` — downloaded on first run to `~/.c0wrk/tools/bin/` and PATH-prepended at startup (ADR-010).

## Ignore Filtering (`glob` / `ripgrep`)

Discovery tools honour the project's ignore files via an `ignore.IgnoreChecker` attached to the task context. The session manager builds a **multi-root resolver** over the symlink-resolved, deduplicated workspace path **plus** the session's auxiliary work directories (`backend/session/manager_execution.go` → `injectIgnoreChecker` → `tools.WithIgnoreChecker`) once per task; `glob` and `ripgrep` read it back through `tools.IgnoreCheckerFrom` and filter per the containing root's own `.gitignore` + `.aiignore`.

- `glob` honours `.gitignore` **and** nested `.aiignore` files fully (resolver-based).
- `ripgrep` honours `.gitignore` natively; the root-level `.aiignore` at the search root is passed to `rg` via `--ignore-file`. Nested `.aiignore` is **not** honoured by `rg` — a documented, accepted limitation.
- When no checker is plumbed through the context (e.g., No Project with no workspace and no work directories), the tools keep their unfiltered behaviour.
- `read_file` deliberately does **not** consult the checker: ignore filtering governs *discovery*, not *access*.

See [ADR-016](../../decisions/016-aiignore.md) for the rationale and the engine primitive (`github.com/v0lka/sp4rk/ignore`).

## Document Conversion (`read_file` wrapper)

`read_file` is registered as `NewReadFileDocTool` (`core/tools/read_file_doc.go`), a wrapper that embeds sp4rk's `ReadFileTool`. Plain-text files delegate to the inner streaming reader unchanged. Document formats — pdf, docx, pptx, xlsx, odt, html, htm — are converted to markdown via `core/markitdown` (the managed markitdown CLI, ADR-010; the same converter used by the attachment flow in [../session-lifecycle.md](../session-lifecycle.md)).

**Caching** — the wrapper implements sp4rk's optional `ContentBackedReader` interface (`IsContentBacked` returns true only for document extensions). The executor therefore stores converted results in memory (content-backed, paginatable via `tool_result_read`) instead of treating the file on disk as the backing store. Converted markdown is additionally persisted under `<session-temp>/conversions/<sha256(path+mtime+size)>.md`, so paginated reads and re-reads don't re-run the subprocess; the key changes when the source file is modified. Writes are atomic (temp file + rename) so concurrent readers never observe a half-written entry.

**Fallback** — on conversion failure, or when markitdown is unavailable, the wrapper falls back to the inner streaming reader with a warning prepended (`[Warning: document conversion failed …; showing raw file content.]`).

**Limits** — the same `DefaultFileLimits()` caps (`MaxLineBytes`, `MaxWindowLines`) apply to converted output, and the requested line range is validated up front to prevent inverted-range panics. The converter is lazily initialized on first document read (2-minute per-file timeout).

## Limits Configuration

Per-tool output truncation is centralized in the executor's two-stage pipeline (see [../orchestration/executor.md](../orchestration/executor.md)). No individual tool performs per-tool output truncation; all truncation happens in Stage 1 → Stage 2 after the full result is cached.

**`read_file` is special**: for plain-text files it uses a streaming line-range reader (sp4rk `ReadFileRange`) that reads only the requested window from disk — O(1) memory. The file on disk serves as the cache backing store (file-backed cache entry), so `ToolResultCache` stores zero bytes of content for plain-text `read_file` results. Document formats are the exception — converted markdown is content-backed cached (see [Document Conversion](#document-conversion-read_file-wrapper) below).

| Config                              | Affects                    | Default            |
| ----------------------------------- | -------------------------- | ------------------ |
| `toolLimits.readDefaultLines`       | `read_file` window size    | 2000               |
| `toolLimits.perToolTruncation`      | All cacheable tools (map)  | per-tool           |
| `toolResultBudget.cacheTTLSeconds`  | ToolResultCache eviction   | 300                |

Default per-tool Stage 1 truncation: `read_file` 2000 lines, `ripgrep` 5000, `glob` 2000, `list_directory` 2000, the shell-exec tool (`bash_exec`) 10000, `web_fetch` 2097152 bytes (2 MiB).

`read_file` internal safety caps (hardcoded in `DefaultFileLimits()`, not configurable): `MaxLineBytes` 1 MiB (per-line), `MaxWindowLines` 50000 (hard cap per call).

Non-truncation tool limits:

| Config                       | Affects                   | Default             |
| ---------------------------- | ------------------------- | ------------------- |
| `BashTimeouts.MaxTimeout`    | shell-exec (`bash_exec`/`posh_exec`) | 120s                |
| `BashTimeouts.WaitDelay`     | shell-exec (`bash_exec`/`posh_exec`) | 5s                  |
| `BashBlacklist`              | shell-exec (`bash_exec`/`posh_exec`) | [] (regex patterns) |
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
