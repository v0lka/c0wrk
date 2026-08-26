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
| `LLMFullConfigRequest`    | frontend | frontend → backend | LLM multi-provider config update |
| `SecuritySettingsResponse` | backend  | backend ↔ frontend | Security policy CRUD                |
| `SmallLLMConfigResponse`   | backend  | backend ↔ frontend | Small-LLM profile CRUD (see [../domains/small-llm.md](../domains/small-llm.md)) |
| `OptimizePromptResponse`   | backend  | backend → frontend | Prompt optimization result          |
| `SkillDescriptorDTO`       | backend  | backend → frontend | Skill listing                       |
| `ResearchStatusDTO`        | backend  | backend → frontend | RESEARCH mode view model: toggle + parsed research root + seed result |
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
| `GetSessionHistory`    | id                           | ([]ChatMessage, error)    | Get message history                                   |
| `GetSessionRuntimeStatus` | id                        | (SessionRuntimeStatus, error) | Live/persisted execution state: `{active, has_unfinished_task, unfinished_task_id?, paused, activity?, streaming}`. `paused` is true when the resumable unfinished task is in the `"paused"` status (a cooperative pause checkpoint). `activity` (omitted until the first tracked emission) is the backend-tracked live phase label ("Thinking...", "Routing request...", ...) and `streaming` reports an open assistant stream. Called after history load to reconcile the UI (running flag, paused flag, resume banner, stale prompts, frozen activity label/streaming text) instead of defaulting to idle |
| `GetBlackboardState`   | sessionID                    | (\*BlackboardStateResponse, error) | Get blackboard task state                    |
| `GetStepOutput`        | sessionID, stepID            | (string, error)          | Full output of a single plan step (fetched lazily on hover so large outputs never ride along in `GetBlackboardState`; empty string when the step or its output is absent) |
| `SearchBlackboardStepOutputs` | sessionID, query     | ([]string, error)        | IDs of steps whose full output contains the query (case-insensitive); unioned with the viewer's local summary/id filtering so the search box matches step output content. Empty query yields no matches |
| `EmitSessionEvent`     | evt                          | —                               | Emit a session-scoped event (UI + persistence path so it survives app restarts) |
| `SendMessage`          | id, text, activeSkills, activeAgents, modelOverride, reasoningEffort, goal, goalBudget, reviewMode | error                     | Send user message (async execution). Execution mode is derived from the active project (No-Project = CHAT); `activeAgents` carries `#agent` refs; `goal`/`goalBudget` start a goal loop; `reviewMode` renders the Code Review prompt section. Rejected for archived sessions (archived history is read-only) |
| `CancelTask`           | id                           | error                     | Cancel running task                                   |
| `PauseSession`         | sessionID                    | error                     | Cooperatively pause the in-flight task (flips the universal pause signal; the conductor stops at the next step boundary, the task is persisted as `"paused"`, and `session_paused` is emitted). Applies to all tasks — goal and non-goal alike |
| `ResumeSession`        | sessionID, modelOverride, reasoningEffort, nudge | error                     | Resume a paused task (honors model/reasoning override; the optional `nudge` is injected as a trailing user message into the first resumed turn — the nudge-resume path). Emits `task_resumed` + `session_resumed`; rejected for archived sessions |
| `CompactSessionContext`| sessionID, strategy          | error                     | Start a manual compaction of the session's conversation history (`sliding_window` \| `summarization` \| `hierarchical`). Async: pauses a running task first (like `PauseSession`, waiting for the checkpoint), compacts, persists a `context_compaction` marker row, auto-resumes, and reports via `compaction_started`/`compaction_finished` events (`paused_without_resume` when the auto-resume failed — the UI re-applies the paused state). Rejects for unknown strategies, archived sessions, or `ErrCompactionInFlight`. Sends/resumes fail with `ErrSessionCompacting` for the whole window (see [../domains/memory/compaction.md](../domains/memory/compaction.md) § Manual Context Compaction) |
| `CancelSessionCompaction` | sessionID                 | error                     | Cancel an in-flight manual compaction (no-op when none is running) |
| `ResumeTask`           | id, modelOverride, reasoningEffort | error                     | Resume failed task (honors model/reasoning override chosen before resuming); rejected for archived sessions |
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
| `SaveProjectSwitchState` | `ProjectUIStateRequest`     | error                           | Backward-compatible alias for `SaveProjectUIState` |
| `GetProjectSwitchState`  | projectID                   | (\*ProjectUIStateResponse, error) | Backward-compatible alias for `GetProjectUIState` |
| `SwitchProject`          | id                          | error                           | Set active project and resolve destination session fallback |

### Config (`backend/frontend_api_config.go`)

| Method                   | Parameters               | Returns                           | Description                    |
| ------------------------ | ------------------------ | --------------------------------- | ------------------------------ |
| `GetConfig`              | —                        | ConfigResponse                    | Get current config (sanitized); pure in-memory read — see the network-free note below |
| `HasDefaultModel`        | —                        | bool                              | Cheap probe: reports whether a default LLM model is configured (false for nil config and empty default). For UI flows that need only this fact — e.g. the settings close check — and must not pay for a full `GetConfig` response |
| `UpdateLLMConfig`       | LLMFullConfigRequest    | error                             | Update full LLM multi-provider config |
| `UpdateSearchSettings`   | SearchSettingsRequest    | error                             | Update search config           |
| `GetSecuritySettings`    | —                        | SecuritySettingsResponse          | Get security policies          |
| `UpdateSecuritySettings` | SecuritySettingsResponse | error                             | Update security policies       |
| `GetLogLevel`            | —                        | string                            | Get current log level          |
| `SetLogLevel`            | level                    | error                             | Set log level dynamically      |
| `ListProviderModels`     | provider                 | ([]string, error)                 | List models for a provider     |
| `UpdateProxySettings`    | ProxySettingsRequest     | error                             | Update proxy configuration     |
| `UpdateExperimentalFeatures` | enabled            | error                             | Toggle the master experimental-features switch. Persists the change, rebuilds the LLM router (so the gated Small-LLM profile applies immediately), and updates the session manager's Small-LLM snapshot. RESEARCH mode is gated at its own RPC boundary (`EnableResearch`/`GetResearchStatus`/`GetResearchGraph`) |
| `GetSmallLLMConfig`      | —                        | SmallLLMConfigResponse            | Get the small-LLM profile (always_present normalized to non-nil; protected orchestration tools unioned in) |
| `UpdateSmallLLMConfig`   | SmallLLMConfigResponse   | error                             | Validate + persist the small-LLM profile, then rebuild the LLM router. Validation runs before mutation; an invalid payload produces no partial write. |
| `GetModelConfig`         | model                    | (ModelConfigResponse, error)      | Get per-model overrides (sampling/params) |
| `SetModelConfig`         | model, ModelConfigRequest | error                            | Set per-model overrides |

> **Experimental-features gate**: `ConfigResponse` carries `experimental.enabled` — a single, all-or-nothing switch for features under active development. When off, RESEARCH mode is treated as off for every project (`GetResearchStatus`/`GetResearchGraph` return the empty-state DTO and `EnableResearch` rejects) and the Small-LLM profile is forced off in `ToBuilderConfig` (the stored `small_llm.enabled` is preserved but never activates). The frontend hides the corresponding affordances reactively in the same session: the sidebar research icon/tab and the Small-LLM settings tab.

> **Network-free config read**: `GetConfig` performs no network I/O. `AllModels` metadata resolves through the sp4rk `ModelRegistry.ResolveLocal` (in-memory tiers: overrides, built-ins, fuzzy matches, lazy cache — including LM Studio probe results written via `SetRuntimeMetadata` to the runtime tier, which `ResolveLocal` also serves; fallback defaults for unknown models). `GetConfig` runs on every settings open, so it always returns from memory and never blocks behind an HTTP probe or timeout; `HasDefaultModel` exists so single-fact UI checks skip even the full response build.

### Workspace (`backend/frontend_api_workspace.go`)

| Method                | Parameters         | Returns                              | Description                  |
| --------------------- | ------------------ | ------------------------------------ | ---------------------------- |
| `ListDirectory`       | dirPath, recursive | ([]FileNode, error)                  | List directory contents (workspace-contained) |
| `WriteFile`           | sessionID, path, content | error                          | Write content to a file (workspace-contained; structural/write RPC, resolves via `resolveWorkspacePath`) |
| `ReadFile`            | filePath           | (string, error)                      | Read file contents as text (any absolute path; not constrained to workspace — the viewer surfaces paths the agent cites, including out-of-workspace files like SDK sources). A trailing `#L<n>` / `#L<n>-L<m>` line anchor is stripped before resolution |
| `ReadFileAsDataURL`   | filePath           | (string, error)                      | Read a file and return it as a base64 `data:` URL (RFC 2397), for embedding local images in the file-viewer markdown renderer (the webview cannot load `file://` or project-root-relative URLs directly). **Workspace-contained** — resolves via `resolveWorkspacePath`, unlike `ReadFile`, because image embedding runs during auto-render without an explicit user action and must not let a markdown document read arbitrary files (e.g. `~/.ssh/id_rsa`) into the DOM. MIME type is derived from the extension via `mime.TypeByExtension`; an 8 MiB size guard rejects oversized payloads |
| `GetFileDiff`         | filePath           | (string, error)                      | Get git diff for file (any absolute path; returns `("", nil)` for files outside the active project root or a non-git path — no baseline to diff against) |
| `GetGitStatus`        | dirPath            | (map[string]GitStatusEntry, error)   | Get git status for directory |
| `GetSessionWorkspace` | sessionID          | (string, error)                      | Get session workspace path   |
| `GetFileIcon`         | filePath           | (FileIconResponse, error)            | Get devicon for file (any absolute path; not constrained to workspace) |
| `WatchDirectory`      | dirPath            | error                                | Subscribe to dir changes     |
| `UnwatchDirectory`    | dirPath            | error                                | Unsubscribe dir changes      |

> **Path-containment boundary**: read-path RPCs (`ReadFile`, `GetFileIcon`, `GetFileDiff`) resolve via `resolveReadablePath` and are **not** workspace-contained — the file viewer must display any path the agent surfaces in chat. `ReadFileAsDataURL` is the exception: it resolves via `resolveWorkspacePath` and **retains** containment, because image embedding runs during markdown auto-render (no explicit user action) and must not let a malicious document exfiltrate arbitrary files into the webview DOM. Structural/write RPCs (`WriteFile`, `ListDirectory`) resolve via `resolveWorkspacePath` and **retain** containment (reject paths outside the active project workspace). This is a UI-display affordance only and does **not** affect the agent's tool surface: the `read_file` agent tool enforces its own session-root containment independently (see [../architecture/security-model.md](../architecture/security-model.md)).

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
| `Commit`               | message                 | (string, error)               | Create a commit |
| `GetBranches`          | —                       | ([]Branch, error)             | List branches |
| `GetCurrentBranch`     | —                       | (BranchInfo, error)           | Get current branch |
| `GetBranchBases`       | —                       | ([]BranchBase, error)         | Branch base refs (for merge/rebase target UI) |
| `CheckoutBranch`       | name                    | error                         | Checkout a branch |
| `CreateBranch`         | name, base              | error                         | Create a new branch at a base ref |
| `RenameBranch`         | oldName, newName        | error                         | Rename a local branch (git branch -m); works when oldName is the current branch |
| `DeleteBranch`         | name, force             | error                         | Delete a local branch (git branch -d, or -D when force) |
| `PushBranch`           | name                    | (string, error)               | Push a local branch to its upstream remote, publishing (setting the upstream) when it has none yet |
| `CheckoutRemoteBranch` | remoteBranch            | error                         | Create a local branch from a remote-tracking branch and switch to it (git switch -c --track) |
| `DeleteRemoteBranch`   | name, remote            | (string, error)               | Delete a branch on the given remote (git push <remote> --delete; default origin) |
| `CreateTag`            | name, sha               | error                         | Create a lightweight tag at a commit |
| `DeleteTag`            | name                    | error                         | Delete a local tag |
| `PushTag`              | name, remote            | (string, error)               | Push a single tag to a remote (default origin) |
| `DeleteRemoteTag`      | name, remote            | (string, error)               | Delete a tag on the remote (default origin) |
| `GenerateCommitMessage`| —                       | (string, error)               | AI-generate a commit message from the staged/working diff |
| `Pull`                 | remote, flags []string  | (string, error)               | Pull from remote (flags: --ff-only, --rebase, --rebase --autostash) |
| `Push`                 | remote, flags []string  | (string, error)               | Push to remote (flags: --force, --force-with-lease, --no-verify) |
| `Fetch`                | remote, flags []string  | (string, error)               | Fetch from remote (flags: --tags, --prune) |
| `GetGitHistory`        | —                       | ([]GitHistoryCommit, error)   | Unified commit log + DAG graph topology (each `GitHistoryCommit` carries both log fields and parents/refs; replaces the former separate `GetCommitLog`/`GetGitGraph` pair) |
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

### Lifecycle (`backend/frontend_api.go`)

| Method             | Parameters       | Returns              | Description |
| ------------------ | ---------------- | -------------------- | ----------- |
| `Lifecycle`        | —                | *FrontendAPILifecycle | Returns lifecycle sub-API (config load state, vector manager, cleanup) |

### Desktop (`desktop/app.go` — methods on `*App`, not promoted from `FrontendAPI`)

| Method           | Parameters | Returns       | Description |
| ---------------- | ---------- | ------------- | ----------- |
| `GetPendingActions` | sessionID  | (*PendingActionsResponse, error) | Unresolved pending actions for a session (tool confirmations, ask-user forms, step-limit/resume prompts, goal proposals) |
| `PickDirectory`  | —          | (string, error) | Native directory picker dialog |
| `PickAttachmentFiles` | —     | ([]string, error) | Native multi-select file picker exposing two filters: "Supported documents" (built from `core/markitdown.SupportedExtensions()`) and "Images" (`*.png;*.jpg;*.jpeg;*.gif;*.webp`). No "All files" filter — a wildcard resolves to a dynamic UTType on macOS that corrupts the panel's content-type filter. Returns `([]string{}, nil)` on cancel. Must remain on `App` — it requires the Wails context like `PickDirectory` |
| `PersistWindowBounds` | —      | —               | Persist live native width/height/maximized state atomically to `~/.c0wrk/window_state.json`; frontend calls it after a debounced resize and desktop shutdown saves once more |
| `SetWailsLogger` | wl         | —             | Binding artifact: stores Wails log adapter (called internally, not from frontend) |

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
| `GetReview` | sessionID | (*review.Review, error) | Load the session's review (status, comments, prompt) |
| `GetReviewDiff` | — | ([]ReviewFileDiff, error) | Working-tree diff grouped by file (review format) |
| `SaveReviewGeneralComment` | sessionID, body | error | Add/replace the general review comment |
| `SaveReviewFileComment` | sessionID, filePath, body | (string, error) | Add a file-level comment (returns comment ID) |
| `SaveReviewHunkComment` | sessionID, filePath, hunkID, body | (string, error) | Add a hunk-level comment (returns comment ID) |
| `DeleteReviewComment` | id | error | Delete a review comment by ID |
| `SetReviewStatus` | sessionID, status | error | Set review status (e.g. pending/approved/changes_requested) |
| `ClearReviewComments` | sessionID | error | Remove all comments from the review |
| `ClearReview` | sessionID | error | Clear the entire review |
| `SaveReviewPrompt` | sessionID | (*ReviewPromptMessage, error) | Persist/refresh the review prompt surfaced to the agent |

### Agents (`backend/frontend_api_agents.go`)

| Method | Parameters | Returns | Description |
| ------ | ---------- | ------- | ----------- |
| `ListAgents` | — | []AgentDescriptorDTO | List available Subagent Profiles (name+description+meta) |

### Research (`backend/frontend_api_research.go`)

RESEARCH mode toggle + hypothesis-graph view model. RESEARCH mode is available only for real projects (`loadProjectForResearch` rejects the No Project pseudo-project); the persisted toggle is `ProjectInfo.ResearchRoot`. RESEARCH mode is additionally gated by the experimental-features master switch: when experimental features are off, `EnableResearch` rejects and `GetResearchStatus`/`GetResearchGraph` return the empty-state DTO regardless of any persisted research root, so every project reports research=off.

| Method | Parameters | Returns | Description |
| ------ | ---------- | ------- | ----------- |
| `EnableResearch` | projectID, rootPath | (*ResearchStatusDTO, error) | Activate RESEARCH mode: resolve the research root (default `<workspace>/.research`; an explicit `rootPath` must be inside the workspace — containment enforced via `config.IsWithinPath`), create it, recursively watch the research tree, seed the research-* skill-pack into the project's `.agents/skills` (idempotent, non-destructive; failure is non-fatal and logged, while successful per-skill outcomes are returned via `SeedResult`), persist the root, invalidate the skill cache and rescan skills for already-running sessions, then emit `research:changed` (action=`enabled`). Idempotent on re-enable. Returns the full status (toggle + parsed root + seed result) |
| `DisableResearch` | projectID | error | Deactivate RESEARCH mode: clear the persisted research root and unwatch the research tree. Does not delete the research directory or the seeded skills — re-enabling restores prior state. Emits `research:changed` (action=`disabled`) |
| `GetResearchStatus` | projectID | (*ResearchStatusDTO, error) | Live RESEARCH state: the toggle plus the parsed research root (index, project list, per-project graph/metrics/brief/prior-art). Returns an empty-state DTO (`Enabled=false`, nil `Root`) rather than an error when disabled or the root is not yet parseable |
| `GetResearchGraph` | projectID | (*ResearchGraphDTO, error) | Lightweight fetch: only the active research project's hypothesis graph + metrics + report flag (omits index, brief, prior-art, and seed result). Used for incremental `research:file_changed` refreshes; the parse cost equals `GetResearchStatus` (both parse the full research root) — only the wire payload is smaller. Empty-state DTO when disabled, the root is unparseable, or there is no active project |

### Updater (`backend/frontend_api_updater.go`)

Self-update lifecycle. Emits `update:*` global events (see [event-catalog.md](event-catalog.md)).

| Method | Parameters | Returns | Description |
| ------ | ---------- | ------- | ----------- |
| `CheckForUpdates` | — | (*UpdateInfo, error) | Query GitHub for the latest release; emits `update:available`/`update:none`/`update:error` |
| `RunBackgroundUpdateCheck` | — | — | Schedule a background update check (fire-and-forget, no return) |
| `DownloadUpdate` | — | error | Download + integrity-verify the archive; emits `update:progress`/`update:downloaded`/`update:error` |
| `ApplyUpdate` | — | error | Stage the updater re-exec and trigger a graceful quit |
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
  3. Backend persists/normalizes source project state and switches active project context
  4. Backend resolves destination session fallback deterministically: saved session if valid for destination project → latest project session by activity (`ListSessionsByProject`) → backend creates a new project-scoped session via `SessionManager.CreateSession(projectID, workspacePath)` when no sessions exist
  5. Frontend calls `GetProjectSwitchState`/`GetProjectUIState`, restores file tabs/active file, refreshes session list, and activates the resolved session
- **Startup**: backend exposes RPC methods after `Startup()` completes; frontend waits for `backend:ready` event. Vector search methods may return empty results until background ONNX init completes (~1-2s after startup).
- **Teardown**: `Shutdown()` triggers backend cleanup; frontend stops polling and unregisters event listeners

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
