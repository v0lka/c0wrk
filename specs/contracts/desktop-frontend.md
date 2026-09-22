# Contract: Desktop <-> Frontend

## Boundary Rule

Frontend communicates with Go exclusively through Wails IPC. No direct Go imports. Two channels: RPC (request/response) and Events (push notifications).

## Interfaces

| Interface / Type           | Package  | Direction          | Purpose                             |
| -------------------------- | -------- | ------------------ | ----------------------------------- |
| `FrontendAPI`              | backend  | backend → frontend | Wails-bound API (promoted to `App`) |
| `SessionInfo`              | backend  | backend → frontend | Session metadata                    |
| `ProjectInfo`              | backend  | backend → frontend | Project metadata                    |
| `ProjectUIStateRequest`    | backend  | frontend → backend | Persisted project switch UI state write payload |
| `ProjectUIStateResponse`   | backend  | backend → frontend | Persisted project switch UI state read payload  |
| `FileNode`                 | backend  | backend → frontend | File tree entry                     |
| `ChatMessage`              | backend  | backend → frontend | Message history entry               |
| `VectorIndexStatus`        | backend  | backend → frontend | Index progress                      |
| `mcp.ServerStatus`        | github.com/v0lka/sp4rk/tools/mcp | backend → frontend | MCP server state (used by `GetMCPStatus`) |
| `ToolInfo`                 | backend  | backend → frontend | Tool descriptor for UI              |
| `ConfigResponse`           | backend  | backend → frontend | Sanitized config view               |
| `ExperimentalSettingsResponse` | backend | backend → frontend | Master experimental-features switch (embedded in `ConfigResponse.experimental`) |
| `ModelProfilesSettingsResponse`      | backend  | backend → frontend | Resolved Model Profiles state (embedded in `ConfigResponse.model_profiles`): `enabled` (resolved master toggle `model_profiles.enabled`, carried through verbatim — Model Profiles is not gated by the experimental switch) + `essential_tools_enabled` (resolved `essential_tools` variant). Drives the goal-mode block — see [../domains/model-profiles.md](../domains/model-profiles.md#goal-mode-gate-goalblocked) |
| `LLMFullConfigRequest`    | frontend | frontend → backend | LLM multi-provider config update |
| `SecuritySettingsResponse` | backend  | backend ↔ frontend | Security policy CRUD                |
| `ModelProfilesResponse`, `ModelProfileUpdateRequest` | backend | backend ↔ frontend | model profile catalog CRUD + `model_profiles.enabled` master toggle (see [../domains/model-profiles.md](../domains/model-profiles.md)) |
| `OptimizePromptResponse`   | backend  | backend → frontend | Prompt optimization result          |
| `SkillDescriptorDTO`       | backend  | backend → frontend | Skill listing                       |
| `ResearchStatusDTO`        | backend  | backend → frontend | RESEARCH view model: `enabled` flag + parsed research root + pinned artifacts (`pinned_research` / `pinned_hypotheses`) |
| `ResearchGraphDTO`         | backend  | backend → frontend | Lightweight hypothesis-graph + metrics response (`GetResearchGraph`) |
| `TerminalCommand`          | backend  | backend → frontend | Terminal command history            |
| `VectorStoreEntry`         | backend  | backend → frontend | Vector search result                |
| `BlackboardStateResponse`  | backend  | backend → frontend | Task state for resume UI            |
| `AttachmentInfo`           | backend  | backend → frontend | Pending attachment metadata (snake_case; content excluded). Document attachments carry markdown excluded; image attachments additionally carry `is_image: true` and a `thumbnail` JPEG data URI |

## RPC Surface

All methods on `*desktop.App` (promoted from `*backend.FrontendAPI`) are callable from frontend via `window.go.desktop.App.<MethodName>()`.

**Convention**: Methods that can fail return `(T, error)` in Go. Read-only getters that cannot fail return `T` only (e.g., `GetConfig`, `GetSecuritySettings`, `GetMCPStatus`, `GetMCPServers`, `GetToolList`, `GetVectorIndexStatus`, `ListSkills`, `GetSessionTokens`, `HasDefaultModel`). The "Returns" column shows the actual signature; Wails surfaces `error` as a rejected Promise in TypeScript.

### Session (`backend/frontend_api_session.go`)

| Method                 | Parameters                   | Returns                   | Description                                           |
| ---------------------- | ---------------------------- | ------------------------- | ----------------------------------------------------- |
| `CreateSession`        | —                            | (\*SessionInfo, error)    | Create new session (active project)                   |
| `DeleteSession`        | id                           | error                     | Delete session and cascade-remove all its internal files (logs, dumps, temp, plans, No-Project workspace, and the per-session `images/` dir) from `~/.c0wrk`. Archiving does NOT remove files |
| `RenameSession`        | id, name                     | error                     | Rename session                                        |
| `ArchiveSession`       | id                           | error                     | Archive/unarchive session                             |
| `PinSession`           | id                           | error                     | Toggle session pin (affects ordering/filtering)       |
| `ForkSession`          | id                           | (\*SessionInfo, error)    | Deep-copy a session into an independent fork (messages, tasks+steps/facts/attachments/trajectory, terminal commands, work directories, review) with regenerated identifiers in one atomic transaction; runtime counters reset, name "`<src> (fork N)`". Rejected when the session has an unfinished (`in_progress`/`failed`) task. The returned session becomes the active session |
| `ListSessions`         | —                            | ([]SessionInfo, error)    | List active project sessions                          |
| `GetSessionHistory`    | id                            | ([]session.ChatMessage, error) | The session's full content history in a single call, ordered oldest-first. Non-content activity rows (`thinking`, `step_done`) are omitted. There is no pagination — the whole session loads at once |
| `GetSessionRuntimeStatus` | id                        | (SessionRuntimeStatus, error) | Live/persisted execution state: `{active, has_unfinished_task, unfinished_task_id?, paused, compacting, compaction_availability, activity?, streaming, work_units?}`. `paused` is true when the resumable unfinished task is in the `"paused"` status (a cooperative pause checkpoint). `compacting` is true while a manual context compaction is in flight and `compaction_availability` is the per-strategy menu verdict — `[{strategy, available, reclaim_tokens, exact}]` (see [../domains/memory/compaction.md](../domains/memory/compaction.md) § Manual Context Compaction) — both reconcile the compact button/input lock on switch/restart. `activity` (omitted until the first tracked emission) is the backend-tracked live phase label ("Thinking...", "Routing request...", ...) and `streaming` reports an open assistant stream. `work_units` (omitempty) is the durable work-unit snapshot for the session's resumable task — `[{step_id, kind?, status, parent_id?}]` with `status` a durable unit status (`pending \| running \| paused \| completed \| failed \| interrupted`) — used by the session-load reconciliation (`reconcileWorkUnits`) to align paused/interrupted delegate & plan-step chat blocks so they do not stay misleadingly `running`; a unit left in flight (pending/running) by a task that is no longer executing AND is not cooperatively paused is explicitly settled `interrupted` during this call (a column-scoped conditional status write + a transient `work_unit_settled` event, emitted only by the poller that actually transitions the row); container kinds (`task`, `goal_verification`) are excluded from the snapshot. (So this read is not side-effect-free, unlike a plain status get.) Called after history load to reconcile the UI (running flag, paused flag, resume banner, stale prompts, frozen activity label/streaming text, work units) instead of defaulting to idle |
| `GetBlackboardState`   | sessionID                    | (\*BlackboardStateResponse, error) | Get blackboard task state                    |
| `GetStepOutput`        | sessionID, stepID            | (string, error)          | Full output of a single plan step (fetched lazily on hover so large outputs never ride along in `GetBlackboardState`; empty string when the step or its output is absent) |
| `SearchBlackboardStepOutputs` | sessionID, query     | ([]string, error)        | IDs of steps whose full output contains the query (case-insensitive); unioned with the viewer's local summary/id filtering so the search box matches step output content. Empty query yields no matches |
| `EmitSessionEvent`     | evt                          | —                               | Emit a session-scoped event (UI + persistence path so it survives app restarts) |
| `SendMessage`          | id, text, activeSkills, activeAgents, modelOverride, reasoningEffort, goal, goalBudget, e2s, reviewMode | error                     | Send user message (async execution). Execution mode is derived from the active project (No-Project = CHAT); `activeAgents` carries `#agent` refs; `goal`/`goalBudget` start a goal loop; `e2s` starts an E2S (explicit-execution-state) loop — mutually exclusive with `goal` and with a leading `/goal` command (rejected before any side effect), fail-closed while `experimental.enabled` is false, and rejected as a live send into a running task (an E2S run owns the whole task lifecycle); `reviewMode` renders the Code Review prompt section. A goal send (explicit `goal` flag OR a `/goal` prefix on the post-preprocess text) is refused outright while the Model Profiles essential-tools narrowing is active (`goalBlocked = ModelProfiles.Enabled && EssentialTools.Enabled`) — before any side effect, so a blocked goal leaves no phantom row (see [../domains/model-profiles.md](../domains/model-profiles.md#goal-mode-gate-goalblocked)). Rejected for archived sessions (archived history is read-only) |
| `CancelTask`           | id                           | error                     | Cancel running task                                   |
| `PauseSession`         | sessionID                    | error                     | Cooperatively pause the in-flight task (flips the universal pause signal; the conductor stops at the next step boundary, the task is persisted as `"paused"`, and `session_paused` is emitted). Applies to all tasks — goal and non-goal alike |
| `ResumeSession`        | sessionID, modelOverride, reasoningEffort, nudge | error                     | Resume a paused task (honors model/reasoning override; the optional `nudge` is injected as a trailing user message into the first resumed turn — the nudge-resume path). Emits `task_resumed` + `session_resumed`; rejected for archived sessions, and refuses a session whose unfinished task carries a non-terminal goal state while the Model Profiles essential-tools narrowing is active — see [../domains/model-profiles.md](../domains/model-profiles.md#goal-mode-gate-goalblocked) |
| `CompactSessionContext`| sessionID, strategy          | error                     | Start a manual compaction of the session's conversation history (`sliding_window` \| `summarization` \| `hierarchical`). Async: pauses a running task first (like `PauseSession`, waiting for the checkpoint), compacts, persists a `context_compaction` marker row, auto-resumes, and reports via `compaction_started`/`compaction_finished` events (`paused_without_resume` when the auto-resume failed OR the flow honoured a user-owned pause — a user-initiated pause is never stolen, whichever side paused first; the UI re-applies the paused state). A no-op (dialogue already fits) is a success with `nothing_compacted=true` and no marker; when a paused task waits, the no-op instead defers to the resume (`deferred_to_resume=true` — the one-shot resume-compaction request is armed before the auto-resume, and the resumed run force-compacts the merged trajectory). Rejects for unknown strategies, archived sessions, or `ErrCompactionInFlight`. Sends/resumes fail with `ErrSessionCompacting` for the whole window (see [../domains/memory/compaction.md](../domains/memory/compaction.md) § Manual Context Compaction) |
| `CancelSessionCompaction` | sessionID                 | error                     | Cancel an in-flight manual compaction (no-op when none is running) |
| `ResumeTask`           | id, modelOverride, reasoningEffort | error                     | Resume failed task (honors model/reasoning override chosen before resuming); rejected for archived sessions, and refuses a paused goal while the Model Profiles essential-tools narrowing is active ([../domains/model-profiles.md](../domains/model-profiles.md#goal-mode-gate-goalblocked)) |
| `CancelUnfinishedTask` | id                           | error                     | Discard a resumable task (no resume prompt next time) |
| `GetSessionTokens`    | sessionID                    | SessionTokensResponse     | Get token usage for session (getter, no error return). Overlays the live emitter token snapshot (context tokens used/max, fresh fill percent, model) over the persisted session row when the session is in memory, so the status-bar context badge survives a switch back to a running session |
| `GetBlackboardAttachmentMarkdown` | sessionID, attachmentID | (string, error) | Fetch a blackboard attachment's stored markdown (excluded from `GetAttachments` metadata) |
| `ResolvePendingMessage` | sessionID, role, matchField, matchValue, extra | error | Mark a pending tool_confirm / ask_user / step_limit / plan_review message as resolved in the DB (merging `extra` fields) so it does not reappear as pending on session reload |

### Attachments (`backend/frontend_api_attachment.go`)

| Method              | Parameters               | Returns                       | Description |
| ------------------- | ------------------------ | ----------------------------- | ----------- |
| `AttachFiles`       | sessionID, paths         | ([]AttachmentInfo, error)     | Partition files by extension: images (png/jpg/jpeg/gif/webp) are decoded, optionally downscaled/re-encoded as JPEG, and staged as pending image attachments (separate from documents); all other files are converted to markdown via `core/markitdown` and staged as document attachments. Conversion is optionally vision-assisted: when the model currently active on the session is vision-capable, embedded document images are captioned by that model's endpoint (degrades to plain CLI on any failure; egress notes in [../domains/session-lifecycle.md](../domains/session-lifecycle.md)). Emits `attachments:changed` (incremental per file + final with per-file failures). Returns the full pending list (documents + images combined). System-level errors (session missing) return `error`; file-level failures (unsupported format, conversion/decode error) are reported via the event payload's `Failed` field, not as `error`, so partial success is preserved |
| `RemoveAttachment`  | sessionID, attachmentID   | error                         | Remove a staged (pending) document or image attachment by ID; no-op if not found. Removing a pending image also deletes its on-disk copy under the session's `images/` dir. Does not touch attachments already flushed into the blackboard |
| `GetAttachments`    | sessionID                | ([]AttachmentInfo, error)     | Get the session's staged (pending) attachments (documents + images) as metadata-only values |
| `PasteFromClipboard`| sessionID, supportsVision | (PasteResult, error)        | Paste from system clipboard; stages image/files/text attachments and returns the full pending list + paste kind (`image`/`files`/`text`/`empty`) |

### Project (`backend/frontend_api_project.go`)

| Method                   | Parameters                  | Returns                         | Description |
| ------------------------ | --------------------------- | ------------------------------- | ----------- |
| `CreateProject`          | name, externalPath          | (\*ProjectInfo, error)         | Create project with external workspace (UI always supplies externalPath; internal workspaces reserved for No Project auto-creation) |
| `DeleteProject`          | id                          | error                           | Delete project |
| `RenameProject`          | id, name                    | error                           | Rename project |
| `ListProjects`           | —                           | ([]ProjectInfo, error)          | List all projects |
| `SaveProjectUIState`     | `ProjectUIStateRequest`     | error                           | Persist per-project UI switch state (saved session + open tabs + active file) |
| `GetProjectUIState`      | projectID                   | (\*ProjectUIStateResponse, error) | Load per-project UI switch state |
| `SaveProjectActiveSession` | projectID, sessionID      | error                           | Persist ONLY the per-project saved session id (targeted write that preserves previously saved open tabs / active file; project is validated, unknown/archived session ids normalize to empty, a session-ownership lookup failure returns an error and writes nothing) |
| `SaveProjectSwitchState` | `ProjectUIStateRequest`     | error                           | Backward-compatible alias for `SaveProjectUIState` |
| `GetProjectSwitchState`  | projectID                   | (\*ProjectUIStateResponse, error) | Backward-compatible alias for `GetProjectUIState` |
| `SwitchProject`          | id                          | error                           | Set active project and resolve destination session fallback |
| `GetLastActiveProjectID` | —                           | string                          | Last project activated via `SwitchProject` (persisted in `app_state`, including the No Project id); empty when never persisted. Used to restore the active project after an app restart |
| `ActiveProjectDir`       | —                           | string                          | Workspace directory of the active project, used as the default location for user-facing native file dialogs (e.g. `SaveMessageAsMarkdown`); empty when no project is active or the No Project pseudo-project is active (its workspace is c0wrk-internal storage, not a user directory) |

### Config (`backend/frontend_api_config.go`)

| Method                   | Parameters               | Returns                           | Description                    |
| ------------------------ | ------------------------ | --------------------------------- | ------------------------------ |
| `GetConfig`              | —                        | ConfigResponse                    | Get current config (sanitized); pure in-memory read — see the network-free note below |
| `HasDefaultModel`        | —                        | bool                              | Cheap probe: reports whether a default LLM model is configured (false for nil config and empty default). For UI flows that need only this fact — e.g. the settings close check — and must not pay for a full `GetConfig` response |
| `UpdateLLMConfig`       | LLMFullConfigRequest    | error                             | Update full LLM multi-provider config |
| `UpdateSearchSettings`   | SearchSettingsRequest    | error                             | Update search config           |
| `GetSecuritySettings`    | —                        | SecuritySettingsResponse          | Get security policies          |
| `UpdateSecuritySettings` | SecuritySettingsResponse | error                             | Update security policies       |
| `GetShellExecSettings`   | —                        | ShellExecSettingsResponse         | Get the `shell_exec` launch-shape override (both platform sections; empty command = built-in `bash -c` / powershell `-Command` shape). Pure in-memory read; command slices are deep-copied. See [ADR-058](../decisions/058-shell-execution-override.md) |
| `UpdateShellExecSettings` | ShellExecSettingsResponse | error                           | Replace the `shell_exec` launch-shape override (argv template + declared shell per tool) and apply it without a restart by re-registering the shell tool. Validates the argv template shape and the closed shell enum; an invalid payload mutates nothing; a failed re-registration rolls the stored section back (no partially-applied state). A no-change payload re-registers nothing |
| `GetLogLevel`            | —                        | string                            | Get current log level          |
| `SetLogLevel`            | level                    | error                             | Set log level dynamically      |
| `ListProviderModels`     | `ListProviderModelsRequest` | ([]string, error)                 | List models for a provider. Request carries `provider` plus optional draft `api_key` / `base_url` / `type` so Fetch Models works for a compatible provider that is not yet persisted (first-run / no `default_model` yet). Masked or empty `api_key` falls back to the saved key when the provider already exists. |
| `UpdateProxySettings`    | ProxySettingsRequest     | error                             | Update proxy configuration     |
| `UpdateExperimentalFeatures` | enabled            | error                             | Toggle the master experimental-features switch (it gates only the E2S execution mode; Model Profiles is not gated by it). Persists the change and rebuilds the LLM router (so the E2S mode applies immediately), then pushes the refreshed E2S gate (`SetE2SSettings`) onto live session orchestrators. It never touches Model Profiles state. RESEARCH mode is unaffected by this switch: it is always on for real projects (`GetResearchStatus`/`GetResearchGraph`) |
| `GetModelProfiles`         | —                        | ModelProfilesResponse               | Get the Model Profiles feature catalog: every profile (predefined ∪ custom `~/.c0wrk/model-profiles.yaml`) with kind/name/25-knob values, the stored master toggle `enabled` (config.yaml `model_profiles.enabled`, reported verbatim; false before config init), the stored `active_id` (verbatim — a dangling id triggers the resolver's soft fallback to `generic` with a warning), `suggested_profile_id` (normalized default-model match against the predefined slugs: provider prefixes (`Qwen/`, `openrouter/qwen/…`) and trailing decorations (`-instruct`, `-it`, `:free`) are stripped, separators collapse; `generic` is never suggested; null when nothing matches), `protected_tools`, and the picker universe — `builtin_tools` (every registered built-in that is neither MCP-sourced nor goal-mode-only, name-sorted, with registry description) and `tool_groups` (workflow clusters as atomic entries). `warnings` carries store-load warnings, resolver fallback warnings and one-shot notices (drained on read). Profiles and the universe are returned even before config init |
| `CreateModelProfile`       | baseID, name             | (string, error)                   | Duplicate the catalog profile `baseID` (empty = generic) under a new unique display name as a custom profile; returns the new id. Validation before any write; does NOT change the active profile |
| `UpdateModelProfile`       | id, ModelProfileUpdateRequest | error                          | Partially update the CUSTOM profile `id` (optional `name` and/or `config` of 25 knob values; nil fields keep their stored value). Predefined profiles are read-only (duplicate to edit); rename collisions and invalid values are rejected without a write |
| `DeleteModelProfile`       | id                       | error                             | Delete the CUSTOM profile `id`. Deleting the active profile first persists the fallback to `generic` in config.yaml (rolled back if the persist fails); the next `GetModelProfiles` reports the switched active id plus a one-shot notice |
| `SelectModelProfile`       | id                       | error                             | Make a catalog profile (predefined ∪ custom) the active one; persisted to config.yaml (`model_profiles.active_profile`) with in-memory rollback on a failed write |
| `SetModelProfilesEnabled`          | enabled                  | error                             | Toggle the global `model_profiles.enabled` master switch — the feature's only switch, persisted to config.yaml via the same Model Profiles change tail (config:updated emit + router rebuild + session-manager snapshot). Both enabling and disabling are always allowed (Model Profiles is not gated by the experimental switch). A set matching the stored value is a no-op; a failed write rolls the in-memory value back. This is the Model Profiles settings tab's master switch — it never touches the experimental gate (that is `UpdateExperimentalFeatures`) |
| `GetModelConfig`         | model                    | (ModelConfigResponse, error)      | Get per-model overrides (sampling/params) |
| `SetModelConfig`         | model, ModelConfigRequest | error                            | Set per-model overrides |

> **Experimental-features gate**: `ConfigResponse` carries `experimental.enabled` — a single, all-or-nothing switch whose sole gated feature is the E2S execution mode (Model Profiles graduated out of it — see [../decisions/044-model-profiles-out-of-experimental.md](../decisions/044-model-profiles-out-of-experimental.md)). When off, `SendMessage` rejects `e2s=true` fail-closed. The frontend hides the corresponding affordance reactively in the same session: the E2S per-message toggle. RESEARCH mode is not gated by this switch — it is always on for every real project and stays available.
>
> **Model Profiles goal gate**: `ConfigResponse.model_profiles` (`ModelProfilesSettingsResponse`: `enabled` + `essential_tools_enabled`) carries the EFFECTIVE, resolved Model Profiles state — `enabled` is the resolved master toggle carried through verbatim from `model_profiles.enabled` (`effectiveModelProfilesConfig`; no experimental gate), and both fields are false when the config is not loaded. The frontend mirrors it (`modelProfilesGateStore` / `useModelProfilesGate`) to disable the goal toggle reactively; the backend is authoritative, refusing goal sends and paused-goal resumes. `goalBlocked = enabled && essential_tools_enabled` — master-on with the variant off does NOT block. See [../domains/model-profiles.md](../domains/model-profiles.md#goal-mode-gate-goalblocked).

> **Network-free config read**: `GetConfig` performs no network I/O. `AllModels` metadata resolves through the sp4rk `ModelRegistry.ResolveLocal` (in-memory tiers: overrides, built-ins, fuzzy matches, lazy cache — including LM Studio probe results written via `SetRuntimeMetadata` to the runtime tier, which `ResolveLocal` also serves; fallback defaults for unknown models). `GetConfig` runs on every settings open, so it always returns from memory and never blocks behind an HTTP probe or timeout; `HasDefaultModel` exists so single-fact UI checks skip even the full response build.

### Workspace (`backend/frontend_api_workspace.go`)

| Method                | Parameters         | Returns                              | Description                  |
| --------------------- | ------------------ | ------------------------------------ | ---------------------------- |
| `ListDirectory`       | dirPath, recursive | ([]FileNode, error)                  | List directory contents (workspace-contained) |
| `WriteFile`           | sessionID, path, content | error                          | Write content to a file (workspace-contained; structural/write RPC, resolves via `resolveWorkspacePath`) |
| `ReadFile`            | filePath           | (string, error)                      | Read file contents as text (any absolute path; not constrained to workspace — the viewer surfaces paths the agent cites, including out-of-workspace files like SDK sources). A trailing `#L<n>` / `#L<n>-L<m>` line anchor is stripped before resolution |
| `ReadFileAsDataURL`   | filePath           | (string, error)                      | Read a file and return it as a base64 `data:` URL (RFC 2397), for embedding local images in the file-viewer markdown renderer (the webview cannot load `file://` or project-root-relative URLs directly). **Workspace-contained** — resolves via `resolveWorkspacePath`, unlike `ReadFile`, because image embedding runs during auto-render without an explicit user action and must not let a markdown document read arbitrary files (e.g. `~/.ssh/id_rsa`) into the DOM. MIME type is derived from the extension via `mime.TypeByExtension`; an 8 MiB size guard rejects oversized payloads |
| `ReadImageAsDataURL`  | filePath           | (string, error)                      | Read a file and return it as a base64 `data:` URL (RFC 2397) for the file viewer's **image tab**. Unlike `ReadFileAsDataURL` it is **not** workspace-contained — it resolves via `resolveReadablePath` (mirrors `ReadFile`), so the viewer can display an image the agent surfaced anywhere on disk (e.g. a plot under `/tmp` or an SDK asset). This is safe because it fires only on an explicit user action (opening an image tab), never during automatic rendering, so it does not widen the markdown auto-render attack surface. Image MIME comes from a deterministic `imageMimeByExt` map (png/jpg/jpeg/gif/webp/bmp/ico/svg/avif) with a `mime.TypeByExtension` fallback; 8 MiB size guard |
| `GetFileDiff`         | filePath           | (string, error)                      | Get git diff for file (any absolute path; returns `("", nil)` for files outside the active project root or a non-git path — no baseline to diff against) |
| `GetGitStatus`        | dirPath            | (map[string]GitStatusEntry, error)   | Get git status for directory |
| `GetSessionWorkspace` | sessionID          | (string, error)                      | Get session workspace path   |
| `GetFileIcon`         | filePath           | (FileIconResponse, error)            | Get devicon for file (any absolute path; not constrained to workspace) |
| `WatchDirectory`      | dirPath            | error                                | Subscribe to dir changes     |
| `UnwatchDirectory`    | dirPath            | error                                | Unsubscribe dir changes      |

> **Path-containment boundary**: read-path RPCs (`ReadFile`, `GetFileIcon`, `GetFileDiff`) resolve via `resolveReadablePath` and are **not** workspace-contained — the file viewer must display any path the agent surfaces in chat. `ReadFileAsDataURL` is the exception: it resolves via `resolveWorkspacePath` and **retains** containment, because image embedding runs during markdown auto-render (no explicit user action) and must not let a malicious document exfiltrate arbitrary files into the webview DOM. Its image-tab counterpart `ReadImageAsDataURL` deliberately does the **opposite** — it resolves via `resolveReadablePath` and is **not** contained, because the viewer must render an image the agent surfaced anywhere, and it only ever fires on an explicit user action (opening an image tab), so it does not widen the auto-render surface. Structural/write RPCs (`WriteFile`, `ListDirectory`) resolve via `resolveWorkspacePath` and **retain** containment (reject paths outside the active project workspace). This is a UI-display affordance only and does **not** affect the agent's tool surface: the `read_file` agent tool enforces its own session-root containment independently (see [../architecture/security-model.md](../architecture/security-model.md)).

### MCP (`backend/frontend_api_mcp.go`)

| Method             | Parameters                 | Returns                           | Description                |
| ------------------ | -------------------------- | --------------------------------- | -------------------------- |
| `GetMCPStatus`     | —                          | []mcp.ServerStatus               | Get MCP server statuses    |
| `GetMCPServers`    | —                          | map[string]MCPServerConfig       | Get MCP server configs   |
| `GetToolList`      | —                          | []ToolInfo                       | List all registered tools  |
| `UpdateMCPServers` | map[string]MCPServerConfig | error                             | Update MCP config + reload |

### Terminal (`backend/frontend_api_terminal.go`)

| Method               | Parameters            | Returns                      | Description         |
| -------------------- | --------------------- | ---------------------------- | ------------------- |
| `StartTerminal`      | sessionID             | error                        | Start or reattach to the session's PTY. An existing PTY remains alive across session/project switches and makes this call an idempotent reattach |
| `StartTerminalInDir` | sessionID, workDir    | error                        | Restart the session PTY in a workspace-contained directory |
| `TerminalInput`      | sessionID, data       | error                        | Write to terminal   |
| `TerminalResize`     | sessionID, cols, rows | error                        | Resize terminal     |
| `StopTerminal`       | sessionID             | error                        | Stop terminal       |
| `GetTerminalHistory` | sessionID             | ([]TerminalCommand, error)   | Get command history |

### Vector (`backend/frontend_api_vector.go`)

| Method                | Parameters                                                    | Returns                       | Description                                         |
| --------------------- | ------------------------------------------------------------- | ----------------------------- | --------------------------------------------------- |
| `SearchVectorStore`   | `SearchRequest{query, top_k, file_pattern, must_match, mode}` | ([]VectorStoreEntry, error)   | Hybrid search/browse; mode= hybrid\|vector\|lexical |
| `GetVectorIndexStatus`| —                                                             | VectorIndexStatus             | Get vector index state/progress (getter, no error)  |
| `ReindexVectorIndex`  | —                                                             | error                         | Force a full reindex of the active project's index: reconciles changed/new/deleted files, falling back to a full build when no index exists yet. Rejected for No Project (CHAT) mode |

### Git (`backend/frontend_api_git.go`)

| Method                 | Parameters              | Returns                       | Description |
| ---------------------- | ----------------------- | ----------------------------- | ----------- |
| `StageFile`            | path                    | error                         | Stage a single file |
| `UnstageFile`          | path                    | error                         | Unstage a single file |
| `StageAll`             | —                       | error                         | Stage all changes |
| `UnstageAll`           | —                       | error                         | Unstage all changes |
| `GetFileDiffHunks`     | filePath                | ([]HunkDiffInfo, error)       | Per-hunk diff info for a file (staged/unstaged ranges) |
| `GetDiffStat`          | path                    | (*DiffStat, error)            | Diff stat for a file |
| `GetDiffStats`         | —                       | (map[string]DiffStat, error)  | Diff stats for all changed files |
| `Commit`               | message, force          | (CommitResult, error)         | Create a commit (suppression-aware: an untrusted repo arming commit hooks/signing and not forced is withheld with a non-nil `Suppressed{Hooks, SigningRepo, SigningGlobal}` and a nil error; `force=true` commits hardened; a trusted repo commits raw — [ADR-059](../decisions/059-commit-hooks-signing-gate.md)) |
| `GetBranches`          | —                       | ([]Branch, error)             | List branches |
| `GetCurrentBranch`     | —                       | (BranchInfo, error)           | Get current branch |
| `GetBranchBases`       | —                       | ([]BranchBase, error)         | Branch base refs (for merge/rebase target UI) |
| `CheckoutBranch`       | name                    | error                         | Checkout a branch |
| `CreateBranch`         | name, base              | error                         | Create a new branch at a base ref |
| `RenameBranch`         | oldName, newName        | error                         | Rename a local branch (git branch -m); works when oldName is the current branch |
| `DeleteBranch`         | name, force             | error                         | Delete a local branch (git branch -d, or -D when force) |
| `PushBranch`           | name                    | (string, error)               | Push a local branch to its upstream remote, publishing (setting the upstream) when it has none yet; a configured upstream ref whose name differs from the branch name is targeted with an explicit `<branch>:<upstreamRef>` refspec |
| `CheckoutRemoteBranch` | remoteBranch            | error                         | Create a local branch from a remote-tracking branch and switch to it (git switch -c --track) |
| `DeleteRemoteBranch`   | name, remote            | (string, error)               | Delete a branch on the given remote (git push <remote> --delete; default origin) |
| `CreateTag`            | name, sha               | error                         | Create a lightweight tag at a commit |
| `DeleteTag`            | name                    | error                         | Delete a local tag |
| `PushTag`              | name, remote            | (string, error)               | Push a single tag to a remote (default origin) |
| `DeleteRemoteTag`      | name, remote            | (string, error)               | Delete a tag on the remote (default origin) |
| `GenerateCommitMessage`| —                       | (string, error)               | AI-generate a commit message from the staged/working diff |
| `Pull`                 | remote, flags []string  | (string, error)               | Pull from remote (flags: --ff-only, --rebase, --rebase --autostash) |
| `Push`                 | remote, flags []string  | (string, error)               | Push to remote (flags: --force, --force-with-lease, --no-verify). An empty `remote` pushes the current branch to its push remote, following git's push-remote precedence (`branch.<name>.pushRemote` → `remote.pushDefault` → the branch's upstream remote `branch.<name>.remote`), targeting the configured upstream ref with an explicit `<branch>:<upstreamRef>` refspec when that ref's name differs from the branch name; a branch with no upstream is published instead (`git push -u <default push remote>`, resolved `branch.<name>.pushRemote` → `remote.pushDefault` → `origin` → the alphabetically first remote — the last fallback is c0wrk's own, where a bare `git push` would error with no push destination). The refspec always pushes exactly the current branch to the ref it tracks (upstream-style `push.default`), so `push.default=matching`/`nothing` do not apply. A detached HEAD (or an unresolvable branch) falls back to a bare `git push` |
| `Fetch`                | remote, flags []string  | (string, error)               | Fetch from remote (flags: --tags, --prune) |
| `GetIsGitRepo`         | —                       | (bool, error)                 | Whether the active project's workspace resolves to a git repository (30s-cached local check; feeds the `isGitRepo`/`gitRepoProjectId` store pairing) |
| `GetGitHistory`        | limit, skip             | (*GitHistoryPage, error)      | One page of the unified commit log + DAG graph topology (each `GitHistoryCommit` carries both log fields and parents/refs; replaces the former separate `GetCommitLog`/`GetGitGraph` pair). Paginated via `git log -n <limit> --skip <skip>` (limit default 300, capped at 1000); returns `GitHistoryPage{Commits, NextSkip, HasMore}` and the frontend accumulates pages |
| `GetCommitFiles`       | sha                     | ([]CommitFile, error)         | Files changed in a commit |
| `GetCommitFilesBatch`  | shas []string           | (map[string][]CommitFile, error) | Files changed across many commits (batched) |
| `GetCommitDiff`        | sha                     | ([]ReviewFileDiff, error)     | Per-file diff for a single commit (review diff format) |
| `StashCreate`          | message                 | error                         | Create a stash entry |
| `StashPop`             | index                   | error                         | Pop a stash entry |
| `StashDrop`            | index                   | error                         | Drop a stash entry |
| `StashList`            | —                       | ([]StashEntry, error)         | List stash entries |
| `DiscardChanges`       | path                    | error                         | Discard working-tree changes for a file |
| `AppendToGitignore`    | pattern                 | error                         | Append a pattern to .gitignore |
| `Merge`                | branch                  | error                         | Merge a branch |
| `Rebase`               | branch                  | error                         | Rebase onto a branch |
| `AbortMerge`           | —                       | error                         | Abort an in-progress merge |
| `AbortRebase`          | —                       | error                         | Abort an in-progress rebase |
| `ResetToCommit`        | sha, mode               | error                         | Reset HEAD to a commit (mode: soft, mixed, hard) |
| `GetRebaseMergeState`  | —                       | (MergeRebaseState, error)     | Get in-progress merge/rebase state |

### Git Auto-Fetch (`backend/frontend_api_git_autofetch.go`)

| Method                 | Params                  | Returns                       | Purpose                                                |
| ---------------------- | ----------------------- | ----------------------------- | ------------------------------------------------------ |
| `RequestGitRemoteRefresh` | —                    | —                             | Enqueue a best-effort background `git fetch` for the active project's repo (fire-and-forget; all gating is server-side — see `git-auto-fetch.md`) |

### Git Config Risk (`backend/frontend_api_gitconfig_risk.go`)

| Method                 | Params                  | Returns                       | Purpose                                                |
| ---------------------- | ----------------------- | ----------------------------- | ------------------------------------------------------ |
| `GetTrustedGitRepos`   | —                       | []string                      | Repository paths the user has permanently trusted (gitconfig-risk intake) |
| `TrustGitRepo`         | path                    | error                         | Add a repository path to the permanent trust list      |
| `RemoveTrustedGitRepo` | path                    | error                         | Remove a repository path from the trust list           |
| `GetHardenGitRepos`    | —                       | []string                      | Repository paths force-hardened via the config scanner's neutralization |
| `HardenGitRepo`        | path                    | error                         | Force-harden a repository path                         |
| `RemoveHardenGitRepo`  | path                    | error                         | Remove a path from the harden list                     |

### Lifecycle (`backend/frontend_api.go`)

| Method             | Parameters       | Returns              | Description |
| ------------------ | ---------------- | -------------------- | ----------- |
| `Lifecycle`        | —                | *FrontendAPILifecycle | Returns lifecycle sub-API (config load state, vector manager, cleanup) |

### Desktop (`desktop/app.go` — methods on `*App`, not promoted from `FrontendAPI`)

| Method           | Parameters | Returns       | Description |
| ---------------- | ---------- | ------------- | ----------- |
| `GetPendingActions` | sessionID  | (*PendingActionsResponse, error) | Unresolved pending actions for a session (tool confirmations, ask-user forms, step-limit/resume prompts, goal proposals) |
| `PickDirectory`  | —          | (string, error) | Native directory picker dialog |
| `ConfirmExit`    | —          | error           | Close-guard answer: the user confirmed quitting despite active sessions (counterpart to the `app:exit_requested` event, see [event-catalog.md](event-catalog.md)). Arms the exit-confirmed bypass flag and triggers a graceful Wails quit, which re-enters `OnBeforeClose` (allowed through by the flag) and then runs the normal `Shutdown` sequence. Lives in `desktop/exit_guard.go`; must remain on `App` — it requires the Wails context |
| `PickAttachmentFiles` | —     | ([]string, error) | Native multi-select file picker exposing two filters: "Supported documents" (built from `core/markitdown.SupportedExtensions()`) and "Images" (`*.png;*.jpg;*.jpeg;*.gif;*.webp`). No "All files" filter — a wildcard resolves to a dynamic UTType on macOS that corrupts the panel's content-type filter. Returns `([]string{}, nil)` on cancel. Must remain on `App` — it requires the Wails context like `PickDirectory` |
| `PickStudyDocument` | —     | (string, error) | Native single-select file picker backing the Papers panel's "pick a local document" gesture beside the Study field: one "Supported documents" filter (`attachmentFilterPattern()`, the same markitdown set as `PickAttachmentFiles` — no "All files" entry, same macOS UTType caveat). The chosen absolute path is dispatched straight into a fresh `study-paper` session by the frontend (no extra Study press); cancel returns `("", nil)`. Must remain on `App` — it requires the Wails context like `PickDirectory` |
| `PickAndImportThemes` | —     | ([]ThemeImportResult, error) | Native multi-select theme picker that imports every chosen CSS file in one action: a "Theme files" filter (`*.css`), cancel returns `(nil, nil)` (nothing imported, no error), the chosen paths delegate to the package-level batch importer (`backend.ImportThemesFromPaths` bridge over the UNEXPORTED `FrontendAPI` import functions — the import flow is not renderer-callable by design). Per-file outcomes (including validation failures) come back in the result list — the frontend activates the last success and toasts the skipped files. Must remain on `App` — it requires the Wails context like `PickDirectory` |
| `SaveMessageAsMarkdown` | content | (string, error) | Native save-file dialog writing chat-message text to a user-chosen Markdown file (invoked via Shift+click on a message's copy button). Defaults to the active project directory (`ActiveProjectDir`, then the remembered dialog directory), pre-fills `message.md`, normalizes the chosen name to `.md`, and fails closed when that normalized name already exists but differs from the dialog-confirmed one (the native overwrite prompt covered only the literal name). Returns the saved absolute path, or `("", nil)` on cancel. Lives in `desktop/dialog_save.go`; must remain on `App` — it requires the Wails context like `PickDirectory` |
| `PersistWindowBounds` | —      | —               | Persist live native width/height/maximized state atomically to `~/.c0wrk/window_state.json`; frontend calls it after a debounced resize and desktop shutdown saves once more |
| `SetWailsLogger` | wl         | —             | Binding artifact: stores Wails log adapter (called internally, not from frontend) |

### Themes (`backend/frontend_api_themes.go`)

User-theme lifecycle for the theme selector: installed themes live as CSS files in the global themes directory (`~/.c0wrk/themes/*.css`). `ThemeDTO` carries `id` (the slug stem of the CSS file name), `name` (header comment or prettified file name), `type` (`dark`/`light`), and `css` — the CANONICAL SANITIZED theme body (import stores the sanitized form; listing re-sanitizes on read), present on every entry (the listing reads each file once) so the frontend can activate any theme without a second round-trip. Batch imports (`PickAndImportThemes`) return `ThemeImportResult` — one per picked file: `file` (the source path), `theme` (the installed DTO, success only), and `error` (the failure reason; success entries never carry it).

| Method                 | Parameters | Returns         | Description |
| ---------------------- | ---------- | --------------- | ----------- |
| `ListThemes`           | —          | []ThemeDTO      | Descriptors for all installed user themes, sorted by display name; a missing themes directory yields an empty (non-nil) slice, not an error. Entries carry the full `css` body |
*(No renderer-facing import RPCs: the import functions are unexported package-level functions in `backend/frontend_api_themes.go` precisely so the binding generator never publishes a path-taking method. `PickAndImportThemes` (on `desktop.App`, native picker) is the sole import entry point.)* |
| `DeleteTheme`          | id         | error           | Remove an installed user theme by id; fails when the theme does not exist. Reserved built-in ids (`default-dark`, `default-light`) are never valid import slugs and cannot be deleted |

### Prompt (`backend/frontend_api_prompt.go`)

| Method           | Parameters | Returns                             | Description          |
| ---------------- | ---------- | ----------------------------------- | -------------------- |
| `OptimizePrompt` | prompt     | (\*OptimizePromptResponse, error)   | Three-stage optimization: translate/extract keywords, optional vector-context lookup, then rewrite. Rewrite validation and LLM-call failures each retry up to two additional attempts; final failure rejects the Promise and leaves the original editor text intact |

### Skills (`backend/frontend_api_skills.go`)

| Method       | Parameters | Returns              | Description                              |
| ------------ | ---------- | -------------------- | ---------------------------------------- |
| `ListSkills` | —          | []SkillDescriptorDTO | List available skills (name+description) |

### Goal (`backend/frontend_api_goal.go`)

| Method          | Parameters                              | Returns | Description                                                                                          |
| --------------- | --------------------------------------- | ------- | ---------------------------------------------------------------------------------------------------- |
| `ConfirmGoal`   | sessionID, requestID, condition, verify, verificationMode | error   | Approve a proposed goal (optionally with edits). `verificationMode` (`executable`/`re_derivation`) overrides the derivation-chosen mode. Resolves the pending `goal_proposal` action          |
| `CancelGoal`    | sessionID, requestID                    | error   | Cancel a proposed goal                                                                               |

> Pause/resume is a **session-level** control (not goal-specific): see the Session table below for `PauseSession`/`ResumeSession`.

### Work Directories (`backend/frontend_api_workdirs.go`)

| Method                          | Parameters                                  | Returns                           | Description |
| ------------------------------- | ------------------------------------------- | --------------------------------- | ----------- |
| `ListProjectWorkDirectories`    | projectID                                   | ([]WorkDirectoryRecord, error)    | Project-scoped auxiliary directories |
| `ListSessionWorkDirectories`    | sessionID                                   | ([]WorkDirectoryRecord, error)    | Session-scoped auxiliary directories |
| `AddWorkDirectory`              | scope, ownerID, path, description           | error                             | Add directory (validates existence + non-empty description; rejects project scope for No Project); emits `workdirs:changed` |
| `UpdateWorkDirectoryDescription`| scope, ownerID, id, description              | error                             | Update a directory's description; emits `workdirs:changed` |
| `DeleteWorkDirectory`           | scope, ownerID, id                          | error                             | Delete a directory; emits `workdirs:changed` |

`scope` is `"project"` or `"session"`; `ownerID` is the corresponding project/session ID. `WorkDirectoryRecord` is `project.WorkDirectoryRecord{ID, Path, Description, CreatedAt}`. The `workdirs:changed` event triggers a UI reload; directories are loaded into the execution context on the next message (via `tools.WithAllowedRoots`), and — together with the workspace path — feed a multi-root ignore checker (`tools.WithIgnoreChecker`) so `glob`/`ripgrep` honour each root's own `.gitignore` + `.aiignore` ([ADR-016](../decisions/016-aiignore.md)).

`SendMessage` also performs best-effort session-scope discovery before execution: absolute/extractable directory paths explicitly mentioned in the prompt are normalized, required to exist, filtered against broad sensitive roots, deduplicated against existing session records, and saved with the prompt-discovered description. At least one addition emits a single `workdirs:changed`; extraction/stat/store failures never reject the message. Individual tool calls still pass through session-root containment, symlink analysis, and capability-group policy.

### Review (`backend/frontend_api_review.go`)

Code-review authoring surface (human-in-the-loop review of agent changes). Review state is persisted per session.

| Method | Parameters | Returns | Description |
| ------ | ---------- | ------- | ----------- |
| `GetReview` | sessionID | (*review.Review, error) | Load the session's review (status, comments) |
| `GetReviewDiff` | — | ([]ReviewFileDiff, error) | Working-tree diff grouped by file (review format) |
| `SaveReviewGeneralComment` | sessionID, body | error | Add/replace the general review comment |
| `SaveReviewFileComment` | sessionID, filePath, body | (string, error) | Add a file-level comment (returns comment ID) |
| `SaveReviewHunkComment` | sessionID, filePath, hunkID, body | (string, error) | Add a hunk-level comment (returns comment ID) |
| `DeleteReviewComment` | id | error | Delete a review comment by ID |
| `SetReviewStatus` | sessionID, status | error | Set review status (e.g. pending/approved/changes_requested) |
| `ClearReviewComments` | sessionID | error | Remove all comments from the review |
| `ClearReview` | sessionID | error | Clear the entire review |

> The former `SaveReviewPrompt` RPC was removed by [ADR-060](../decisions/060-remove-post-task-review-prompt.md) together with the post-task review prompt it persisted.

### Agents (`backend/frontend_api_agents.go`)

| Method | Parameters | Returns | Description |
| ------ | ---------- | ------- | ----------- |
| `ListAgents` | — | []AgentDescriptorDTO | List available Subagent Profiles (name+description+meta) |

### Research (`backend/frontend_api_research.go`)

RESEARCH view model + hypothesis-graph RPCs. RESEARCH is always on for real projects — there is no per-project toggle and no persisted root; the root is unconditionally `<workspace>/.research` (`config.ProjectResearchPath`). `loadProjectForResearch` rejects the No Project pseudo-project (which has no workspace), and `GetResearchStatus` returns a neutral empty state for it. RESEARCH is independent of the experimental-features master switch.

| Method | Parameters | Returns | Description |
| ------ | ---------- | ------- | ----------- |
| `CreateHypothesis` | projectID, NewHypothesisCard | (*ResearchGraphDTO, error) | Create a hypothesis card in the active R-NNN (next H-NNN id assigned under the per-root mutation mutex so concurrent creators cannot duplicate it), wiring catalog row + Mermaid node/edges; returns the refreshed graph of the project just written into |
| `DeleteResearch` | projectID, researchID | (*ResearchStatusDTO, error) | Delete the research project (R-NNN) from the project's research root: its `index.md` entry lines and its directory tree (ownership-checked via `ProjectDir` and containment-checked by the core writer before anything is touched). Pins referencing the deleted R-NNN — its brief in `pinned_research` and its cards in `pinned_hypotheses` — are removed from `ProjectInfo.ResearchPins` and persisted. Returns the refreshed status (remaining projects; RESEARCH stays on) and emits `research:changed` (action=`project_deleted`) |
| `GetResearchGraph` | projectID | (*ResearchGraphDTO, error) | Lightweight fetch: only the active research project's hypothesis graph + metrics + report flag (omits index, brief, and prior-art). Used for incremental `research:file_changed` refreshes; the parse cost equals `GetResearchStatus` (both parse the full research root) — only the wire payload is smaller. Empty-state DTO when the root is unparseable or there is no active project |
| `GetResearchNextStep` | projectID, hypothesisID | (*ResearchNextStepDTO, error) | The single recommended next research action. An empty `hypothesisID` yields the project-level recommendation (active R-NNN's phase); a non-empty one scopes it to that hypothesis via `RecommendNextStepForHypothesis` — open/in-progress → `research-experiment` on it, terminal without a Decision → `research-decision` on it, unknown/decided → project-level fallback. Returns the setup recommendation (`research-init`) rather than an error when there is no active R-NNN yet |
| `GetResearchStatus` | projectID | (*ResearchStatusDTO, error) | Live RESEARCH state: `enabled` plus the parsed research root (index, project list, per-project graph/metrics/brief/prior-art) and the persisted pins. For a real project `enabled=true` and `research_root` = `<workspace>/.research`; the No-Project pseudo-project (no workspace) gets a neutral empty DTO (`enabled=false`, no root) instead of an error, as does a root that is not yet parseable |
| `SetActiveResearch` | projectID, researchID | (*ResearchStatusDTO, error) | Make the R-NNN the active research project by rewriting `index.md` so its row is the last table entry (the `PickActiveProject` rule; a missing row is appended, a missing `index.md` created). Runs under the per-root mutation mutex; the rid must resolve under the requesting project's root (foreign R-NNN rejected before any file is touched). Returns the refreshed status and emits `research:changed` (action=`active_changed`) |
| `SetHypothesisPinned` | projectID, researchID, hypothesisID, pinned | error | Pin/unpin a hypothesis card: records the card's research-root-relative path under `ProjectInfo.ResearchPins.Hypotheses[hypothesisID]` (a list — the same H-NNN exists across R-NNN projects; the key drops when empty). rid is ownership-checked; pinning requires the card file to exist while unpinning tolerates a deleted card, so stale pins stay removable. Idempotent both ways; no event |
| `SetResearchPinned` | projectID, researchID, pinned | error | Pin/unpin a research project: records its brief's research-root-relative path in `ProjectInfo.ResearchPins.Research` (idempotent add/remove; rid is ownership-checked). No event — the caller's resolved promise is its refresh signal |
| `UpdateHypothesis` | projectID, researchID, hypothesisID, HypothesisUpdateFields | (*ResearchGraphDTO, error) | Structured card update for the caller's expected R-NNN (ownership-checked inside the requesting project's root before any file is touched — closes the cross-R-NNN save race); status transitions follow the lifecycle state machine and a Parents update is validated (existence, no self-reference, no cycle) and synchronized across card, Mermaid edges, and catalog. Returns the mutated project's refreshed graph |

### Papers (`backend/frontend_api_papers.go`)

The literature ("papers") library — a global subdirectory of a project's research root, `<research-root>/papers/<slug>/{paper.md, note.md, appraisal.md}` — where `<research-root>` is `<workspace>/.research` (`config.ProjectResearchPath`; RESEARCH is always on for real projects). The library lives independently of any R-NNN research project and is always watched (see `papers:changed`). Papers are available only for real projects (`loadProjectForResearch` rejects the No Project pseudo-project), and every RPC enforces workspace containment on the research root and on the library (defense in depth via `config.IsWithinPath`). A paper links to research through its card's `research_ids` (H-NNN), resolved to the owning R-NNN project(s) by parsing the research root when it is available. Every DTO collection is non-nil (empty rather than null).

`PaperDTO` carries the card's identity (`id`/`slug`/`title`/`authors`/`year`/`venue`/`identifiers`), four string-enum axes — `mode` (`skim`/`deep`/`survey`/`review`/`implement`/`teach`), `reading` (`full`/`selective`/`skip`; the reading DECISION, distinct from the soundness verdict), `verdict` (`accepted`/`rejected`/`uncertain`; the soundness judgement, appraisal-wins), `confidence` (`low`/`medium`/`high`) — the `research_ids`/`anchors`/`claims`/`red_flags`/`uncertainties` collections, and the resolution fields `dir`/`card_path`/`pinned`/`linked_research`. The frontend boundary (`normalizePaper` in `frontend/src/api/papers.ts`) mirrors the enum vocabularies exactly and folds an out-of-set value to `""`.

The study-paper skill additionally authors a `flashcards.md` deck per paper (a card table plus an append-only review log). `core/papers` parses/renders it (`ParseFlashcards`/`RenderFlashcards`) and appends self-grades in place (`ApplyReview` + `RecordCardReview`, atomic and containment-checked); the paper workspace's Flashcards section renders it as an interactive active-recall review (`frontend/src/components/papers/FlashcardsReview.tsx`), whose interval schedule (`frontend/src/lib/spacedRepetition.ts`) mirrors `core/papers`.

Multi-paper comparisons are a second global sibling of the research root — `<research-root>/comparisons/<slug>.md`, one artifact per comparison set, written by the study-paper Compare intent from its `comparison-matrix` template (path helpers `config.ComparisonDirName` / `config.ComparisonsPathIn` / `config.ComparisonsPath`). Comparisons have no dedicated RPC: the frontend reads them through the workspace file RPCs (`ListDirectory`/`ReadFile`) under the same research root. The Papers panel's multi-select ("Compare selected", enabled at ≥2 papers) dispatches the skill with that artifact path; the paper workspace's Compare section renders every comparison that names the paper (`frontend/src/components/papers/CompareMatrix.tsx`).

`RunPaperLiterature` (in `backend/frontend_api_papers_literature.go`) runs the study-paper `literature.py` helper — the seeded, stdlib-only Python 3 graph builder that looks up a paper's predecessors / citing works / contradiction candidates (OpenAlex + Crossref + arXiv + Semantic Scholar) — and writes `<paper-dir>/literature.json` (`papers.LiteratureFileName`; helper OUTPUT, not part of `PaperArtifacts`). The written file is the SOLE read source for the panel: the frontend reads `literature.json` through the workspace file RPCs (`ListDirectory` + `ReadFile`) and never fetches on render — a lookup runs only on an explicit user action (the panel's Refresh control), so merely opening a paper never issues a network request. The helper write is ATOMIC (`write_atomic`: stage in a sibling temp file, `fsync`, then `os.replace`), so a reader never observes a partially written file, and the payload carries a `generated_at` UTC timestamp the panel renders as a "generated <date>" staleness hint (an older file stays old — the panel never silently re-runs). It resolves the seed from the card's identifiers (DOI → arXiv → any id → title) and runs the helper through c0wrk's managed Python (`toolmanager.VenvPythonPath(config.ToolsDir(agentDir))`) off the seeded script at `<SkillsDir>/study-paper/scripts/literature.py`; the whole run serializes on the per-research-root mutation mutex shared with `SetPaperPinned` / `RecordFlashcardReview`, so a lookup cannot interleave with a pin/flashcard write. Every non-success outcome is an EXPLICIT status, never an opaque failure: the helper's exit codes map to `offline` (2), `unresolved` (1), `rate_limited` (3), `error` (anything else, incl. a timeout); a missing managed Python, a missing seeded script, or a card with no resolvable seed yield `no_python` / `no_script` / `no_seed` (the panel turns `no_python` into an actionable affordance that names the tool-manager install path and offers a retry, rather than a bare message). Only a transport-level problem — an unknown paper or a containment violation — is a Go `error`. A `90s` wall clock bounds the whole helper invocation (the helper owns its own per-request HTTP timeout); the helper's stderr tail is truncated to 600 runes into `Message`, and `Path`/`Content` describe the written `literature.json` (empty when nothing was written).

`FetchPaperOriginal` (in `backend/frontend_api_papers_source.go`) fetches a paper's original arXiv HTML rendition and localizes it into `<paper-dir>/paper.html` (`papers.OriginalHTMLFileName`) plus `<paper-dir>/assets/` (`papers.OriginalAssetsDirName`) via `core/papers.FetchOriginalHTML` — the native `arxiv.org/html/<id>` rendition first, the `ar5iv` mirror as fallback, with the endpoint hosts forming a hard redirect/image allowlist, streaming byte caps (8 MiB document, 2 MiB per image, ≤200 images), `script`/`style`/`iframe` stripping, and `img src` rewritten to `assets/<name>` resolved against the final document URL. The arXiv id is normalized from the card's identifiers (`papers.NormalizeArxivID`); a card with no usable id yields `no_arxiv` without touching the network or disk. Like `RunPaperLiterature`, the RPC mirrors the explicit-status contract: every non-success outcome is a STATUS (`ok` | `offline` | `no_arxiv` | `not_found` | `error`) — `offline` when no endpoint was reachable at the network level, `not_found` when every reachable endpoint answered 404/410, `error` for anything else (unexpected status, oversized document, containment violation, filesystem failure) — and only a transport-level problem (unknown paper, containment violation, or a concurrent research-root change) is a Go `error`. Concurrency mirrors `RunPaperLiterature`: the paper resolves under a SHORT hold of the per-research-root mutation mutex (re-loading the row so a concurrent root move is rejected with `errResearchRootChanged`), the mutex is RELEASED before the network-bound fetch runs (its per-request timeouts — 60s document, 20s image — bound the work; holding the row mutex would head-of-line-block every pin/flashcard/research mutation), and concurrent runs targeting the SAME paper serialize on the shared per-paper-directory guard (`paperRunMuKey`). The write is atomic (staging dir inside the paper dir, assets swapped, `paper.html` renamed last as the commit point) and containment-checked in `core/papers`; any failure leaves prior content byte-for-byte intact. No event is emitted synchronously — the write lands inside the watched library, so the file watcher emits `papers:changed` and the caller's resolved promise is its refresh signal. `PaperOriginalDTO` carries only `status` and `url` (the URL that actually produced the document, after redirects; empty unless the fetch succeeded). The frontend boundary (`normalizePaperOriginalResult` in `frontend/src/api/papers.ts`) mirrors the status vocabulary exactly and folds an unknown or missing status to `error`, so the explicit-status contract can never regress into a silent success at the boundary.

| Method | Parameters | Returns | Description |
| ------ | ---------- | ------- | ----------- |
| `GetPapers` | projectID | (*PapersDTO, error) | The project's paper library: every parsed paper (normalized DTO — non-nil collections, string enums, pin state, resolved `LinkedResearch`) plus the pinned card paths. A missing/empty library is not an error — it yields an empty, non-nil `papers` slice so the panel renders an empty state. Requires a real (non-No-Project) project and enforces workspace containment |
| `GetPaper` | projectID, paperID | (*PaperDTO, error) | A single paper by id (`P-NNN`, normalized) or slug. Same project/containment requirements and normalization as `GetPapers`; errors when the paper is not in the library |
| `SetPaperPinned` | projectID, paperID, pinned | error | Pin/unpin a paper: records the card's research-root-relative document path (`papers/<slug>/paper.md`) in `ProjectInfo.ResearchPins.Papers`. Serializes on the per-research-root mutation mutex shared with the research pin and root-level mutation RPCs, merging only the pins delta under that lock. Pinning requires the card file to exist; unpinning tolerates a deleted card, so stale pins stay removable. Idempotent both ways; no event — the caller's resolved promise is its refresh signal |
| `RecordFlashcardReview` | projectID, paperID, cardID, grade | error | Append a self-grade (`again`/`hard`/`good`/`easy`) to a paper's `flashcards.md` review log and advance that card's Stage (the fixed 1→3→7→16→35-day ladder: `again` resets, `hard` holds, `good`/`easy` promote). The paper is resolved inside the requesting project's containment-checked library before any file is touched, and the resolve→read→mutate→write chain serializes on the same per-research-root mutation mutex as `SetPaperPinned`. The write is atomic (temp file + rename) and containment-checked in `core/papers` (a symlinked paper directory is rejected); an unknown paper/card, unknown grade, or a deck with no review-log table is rejected and leaves the deck byte-for-byte unchanged. No event is emitted — the write lands in the watched library, so the file watcher emits `papers:changed` |
| `RunPaperLiterature` | projectID, paperID | (*PaperLiteratureDTO, error) | Run the study-paper `literature.py` helper for a paper and write `<paper-dir>/literature.json` — the predecessor / citing / contradiction neighbourhood, resolved via the card's identifiers (DOI → arXiv → any id → title). Runs the helper through c0wrk's managed Python off the seeded script under the global skills dir; the write is atomic (temp file + `os.replace`) and the payload carries a `generated_at` timestamp. Serializes on the per-research-root mutation mutex shared with `SetPaperPinned`/`RecordFlashcardReview`, so a lookup cannot interleave with a pin/flashcard write. Returns an EXPLICIT status rather than a failure for every non-success outcome — `ok` \| `offline` \| `unresolved` \| `rate_limited` \| `no_python` \| `no_script` \| `no_seed` \| `error` — so the UI always renders a message instead of an empty graph; only an unknown paper or a containment violation is a Go `error`. Runs only on explicit user action (the panel's Refresh control); no event is emitted — the written file lands in the watched library, so the file watcher emits `papers:changed` |
| `FetchPaperOriginal` | projectID, paperID | (*PaperOriginalDTO, error) | Fetch a paper's original arXiv HTML rendition (`arxiv.org/html`, ar5iv fallback; hard endpoint-host allowlist, byte caps, script stripping, images localized to `assets/`) and persist `<paper-dir>/paper.html` + `assets/` atomically and containment-checked via `core/papers.FetchOriginalHTML`. The arXiv id comes from the card's identifiers (`NormalizeArxivID`); no usable id yields `no_arxiv` without touching network or disk. Returns an EXPLICIT status rather than a failure for every non-success outcome — `ok` \| `offline` \| `no_arxiv` \| `not_found` \| `error` — so the UI always renders a message instead of a silent empty viewer; only an unknown paper, a containment violation, or a concurrent research-root change is a Go `error`. Concurrency mirrors `RunPaperLiterature`: short research-root mutex hold for the resolve (row re-load + root-change check), released before the network-bound fetch; concurrent runs for the SAME paper serialize on the per-paper guard. `url` is the final (post-redirect) document URL, empty unless the fetch succeeded. No event is emitted — the write lands in the watched library, so the file watcher emits `papers:changed` |

### Updater (`backend/frontend_api_updater.go`)

Self-update lifecycle. Emits `update:*` global events (see [event-catalog.md](event-catalog.md)).

| Method | Parameters | Returns | Description |
| ------ | ---------- | ------- | ----------- |
| `CheckForUpdates` | — | (*UpdateInfo, error) | Query GitHub for the latest release; emits `update:available`/`update:none`/`update:error` |
| `RunBackgroundUpdateCheck` | — | — | Schedule a background update check (fire-and-forget, no return) |
| `DownloadUpdate` | — | error | Download + integrity-verify the archive; emits `update:progress`/`update:downloaded`/`update:error` |
| `ApplyUpdate` | — | error | Stage the updater re-exec and trigger a graceful quit. The quit is subject to the close guard like any other: while sessions have live work it is intercepted (a second confirmation), and the `app:exit_requested` payload carries `update_pending: true` so the modal presents restart context. A user cancel aborts the restart; the staged updater then gives up after its parent-wait window (`core/updater.StagedUpdaterShutdownWait`), after which a later quit no longer installs the update |
| `SkipVersion` | ver | error | Mark a version as skipped (persisted in update_state.json) |
| `GetUpdateSettings` | — | UpdateSettings | Update preferences (getter, no error) |
| `SetUpdateSettings` | autoCheck | (UpdateSettings, error) | Update auto-check preference (persisted to config.yaml `updates.auto_check`); returns normalized settings |

## Event Protocol

See [event-catalog.md](event-catalog.md) for complete event reference.

### Direction

- **Backend -> Frontend**: lifecycle events during task execution (25+ types)
- **Frontend -> Backend**: confirmation responses (tool_confirm, ask_user, step_limit)

### Naming Convention

- Session-scoped: `session:${sessionId}:${eventType}`
- Global: bare event name (e.g., `backend:ready`)

## Data Flow Across Boundary

```
┌──────────────────┐                    ┌──────────────────┐
│   Desktop App    │                    │  Backend (Go)    │
│  (TypeScript)    │                    │                  │
│                  │   Wails Binding    │                  │
│  Wails API calls ├────────────────────►  App struct      │
│  (async Go fn)   │                    │  (methods)       │
│                  │                    │                  │
│  Event handlers  │◄───────────────────┤  EventsEmit()    │
│  (Go events)     │   Wails Events     │                  │
│                  │                    │                  │
│  state → store   │                    │  persistence     │
│  update (Zustand)│                    │  (SQLite)        │
└──────────────────┘                    └──────────────────┘
```

- **Synchronous**: Wails RPC method calls from frontend are async (TypeScript `Promise`) but the Go handler may block
- **Asynchronous**: real-time event stream is push-only; frontend listens with `EventsOn` and publishes to stores
- **Project switch state flow**:
  1. Frontend snapshots source project UI state (`open_tabs`, `active_file`, `saved_session_id`) and calls `SaveProjectSwitchState`/`SaveProjectUIState` (best-effort)
  2. Frontend calls `SwitchProject(id)`
  3. Backend persists/normalizes source project state, switches active project context, and records the destination project id as `last_active_project_id` in `app_state` (best-effort; exposed via `GetLastActiveProjectID` for restart restore)
  4. Backend resolves destination session fallback deterministically, skipping archived sessions at every step: saved session if valid (and not archived) for destination project → latest non-archived project session by activity (`ListSessionsByProject`) → backend creates a new project-scoped session via `SessionManager.CreateSession(projectID, workspacePath)` when no sessions exist
  5. Frontend calls `GetProjectSwitchState`/`GetProjectUIState`, restores file tabs/active file, refreshes session list, and activates the resolved session. When only the session selection changes inside an already-active project, the frontend calls `SaveProjectActiveSession` so the write cannot clobber viewer-owned tab state
- **Startup**: backend exposes RPC methods after `Startup()` completes; frontend waits for `backend:ready` event. Vector search methods may return empty results until background ONNX init completes (~1-2s after startup).
- **Teardown**: `Shutdown()` triggers backend cleanup; frontend stops polling and unregisters event listeners. **Close guard**: before any teardown can start, the Wails `OnBeforeClose` hook (`App.ShouldPreventClose`, `desktop/exit_guard.go`) intercepts a quit while sessions have live work (running task or in-flight manual compaction — paused/unfinished tasks are persisted and don't count), shows the window, and emits `app:exit_requested`; the frontend confirmation modal (`ExitConfirmDialog`) answers via `ConfirmExit`, which arms the exit-confirmed bypass and re-issues the quit so `OnBeforeClose` lets it through into the normal `Shutdown` sequence. Every quit path (window close button, OS quit menu/Cmd+Q, `runtime.Quit` — including the updater's quit) funnels through this single hook on all platforms, which is why the bypass flag is mandatory: without it the confirmed `Quit` would be intercepted again. The updater's quit is deliberately not exempt: `ApplyUpdate`'s graceful quit is intercepted like any other while sessions have live work (the user gets a second confirmation), and the payload's `update_pending` flag makes the modal present restart context — the desktop layer arms the marker when the quit is issued and it self-expires with `core/updater.StagedUpdaterShutdownWait` (plus a small slack), so a cancelled-then-retried quit after the staged updater's window degrades to a plain quit context. A payload that fails frontend validation opens the generic list-less modal variant (with the drop reported via `reportDroppedEvent`) — the quit is already prevented, so an unanswered dialog would leave the app unclosable

## Error Propagation

- **RPC errors**: Go methods return `error`; Wails serializes as `Error` thrown in the TypeScript `Promise` rejection
- **Event errors**: there is no dedicated "event error" channel; failed event emissions are logged and dropped. User-visible execution errors flow through the global `runtime_error` event and the session-scoped `error` event (see [event-catalog.md](event-catalog.md))
- **Startup failures**: if backend `Startup()` panics, Wails shows a native error dialog; if startup completes but services fail, `GetConfig()` still returns a `ConfigResponse` (no error) with `Loaded: false` plus a `ConfigErrors` list, which the frontend uses to display a "Backend unavailable" banner
- **Streaming failures**: streaming uses Wails `EventsEmit` (not SSE); if a task errors mid-stream, `assistant_done` may not fire and the partial streaming text is flushed by `chatStore.flushStreamingToMessage()` on completion/error
- **Panic recovery**: Wails runtime catches Go panics and returns them as RPC errors; backend uses `recover()` middleware in handler chain
- **Fallback**: methods invoked before backend ready return "backend not initialized" error
- **Vector not ready**: vector search methods invoked before embedder initialization completes return empty results with no error (graceful degradation)

## Initialization

```go
// desktop/startup.go — phased startup (critical path < 500ms)
// Phase 1: shell_env + logger
// Phase 2: config + deps_check (parallel)
// Phase 3: database + terminal (parallel)
// Phase 4: stores + project/session preload
// Phase 5: application + FrontendAPI
// → EventBackendReady emitted here ←
// Background: ONNX embedder + vector index manager (non-blocking)
```

```typescript
// frontend/src/main.tsx — mount sequence
// 1. React renders App shell (header, sidebar placeholders)
// 2. useWailsEvent registers for all streaming events
// 3. useProjectLoader calls listProjects() immediately; falls back to backend:ready event
// 4. On project selected: useSessionLoader fetches sessions, FileTreePanel loads directory
// 5. UI transitions from loading state as stores populate
// 6. Vector search becomes available when vector_index:status reports ready (~1-2s)
```

- Frontend must handle the case where backend RPCs return "not initialized" during startup race conditions
- All stores initialize to empty/loading state; no implicit default data

## Type Mapping

Go structs are auto-generated as TypeScript interfaces at:

- `frontend/wailsjs/go/desktop/App.js` — method stubs
- `frontend/wailsjs/go/desktop/App.d.ts` — type declarations

Frontend wraps these in `frontend/src/api/` modules (never imports wailsjs directly from components).

The generated tree also carries `frontend/wailsjs/runtime/` (the Wails event/window shim, re-created by every `wails build`/`wails dev`). Its content never changes, so it is **untracked** (see `.gitignore`); only the `frontend/wailsjs/go/` bindings above are committed — they are the Go ↔ TS contract.

## Wails Binding Regeneration

Bindings are regenerated by:

- `wails build` (production)
- `wails dev` (development with hot-reload)

Adding/removing/renaming a method on `desktop.App` or `backend.FrontendAPI` requires regeneration.

## Breaking Change Checklist

- Adding a method to FrontendAPI -> run `wails build` to regenerate bindings -> update `frontend/src/api/` wrapper
- Changing method signature -> regenerate bindings -> update frontend callers
- Changing `ProjectUIStateRequest`/`ProjectUIStateResponse` fields or renaming `SaveProjectUIState` / `GetProjectUIState` aliases -> regenerate bindings -> update `frontend/src/api/projects.ts` RPC probing and `frontend/src/types/guards.ts` shape validators
- Adding new event type -> add Go emitter method -> add TS type + type guard -> add handler in relevant hook
- Renaming event -> update both Go emitter AND all frontend subscribers
