# Event Catalog

## Overview

Events are the real-time communication channel from backend to frontend during task execution. They enable live UI updates without polling.

## Global Events

Payloads are emitted as-is (no wrapping object) unless shown as `{...}`.

| Event Name               | Direction          | Payload                                                                                                                                                                                                              | Emitter                         | Description                                                 |
| ------------------------ | ------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------- | ----------------------------------------------------------- |
| `startup_error`          | backend → frontend | `{message: string, error: string, error_code?: string}`                                                                                                                                                             | desktop/startup_phases.go       | Fatal startup error                                         |
| `runtime_error`          | backend → frontend | `{id: string, message: string, error_code?: string}`                                                                                                                                                                | backend/frontend_api_project.go | Non-fatal runtime error (e.g. missing git binary on project switch) |
| `backend:ready`          | backend → frontend | `ProjectInfo[]` (pre-loaded projects) or no payload                                                                                                                                                                  | desktop/startup_phases.go + backend/frontend_api_config.go (No-Project auto-create on first config save) | Backend initialization complete                             |
| `projects:loaded`        | backend → frontend | `ProjectInfo[]`                                                                                                                                                                                                      | desktop/startup_phases.go       | Project list available (emitted directly as array)          |
| `sessions:loaded`        | backend → frontend | `SessionInfo[]`                                                                                                                                                                                                      | desktop/startup_phases.go       | Session list for the most recent project (emitted directly) |
| `session:renamed`        | backend → frontend | `{id, name}`                                                                                                                                                                                                         | desktop/startup_phases.go       | A session's title changed (auto or manual); lets the sidebar update a non-active session's title (global counterpart of the session-scoped `session_renamed` event) |
| `project:created`        | backend → frontend | `ProjectInfo`                                                                                                                                                                                                        | backend/frontend_api_project.go | New project created                                         |
| `project:deleted`        | backend → frontend | `string` (project id)                                                                                                                                                                                                | backend/frontend_api_project.go | Project deleted                                             |
| `project:renamed`        | backend → frontend | `{id: string, name: string}`                                                                                                                                                                                         | backend/frontend_api_project.go | Project renamed                                             |
| `project:switched`       | backend → frontend | `ProjectInfo`                                                                                                                                                                                                        | backend/frontend_api_project.go | Active project changed                                      |
| `workspace:tree_changed` | backend → frontend | no payload (`nil`)                                                                                                                                                                                                   | backend/frontend_api_project.go | File tree modified (workspace watcher callback)             |
| `files:dropped`          | backend → frontend | `{paths: string[], x: int, y: int}`                                                                                                                                                                                  | desktop/startup.go              | One or more files dragged onto the window. The webview's own drag-drop is disabled (paths never navigate inside the webview); this is the sole delivery channel for dropped paths |
| `git:status_changed`     | backend → frontend | `string` (repository path of the affected project)                                                                                                                                                                   | backend/frontend_api_git.go     | Git working tree changed (emitted after stage/unstage/commit/checkout/merge/rebase/pull/push/fetch/discard) |
| `workdirs:changed`       | backend → frontend | no payload (`nil`)                                                                                                                                                                                                   | backend/frontend_api_workdirs.go | Auxiliary work directory added/updated/deleted (triggers UI + context reload on next message) |
| `skills:changed`         | backend → frontend | no payload (`nil`)                                                                                                                                                                                                   | backend/frontend_api_skills.go  | Skill directories outside the workspace (e.g. `~/.agents/skills`, `~/.c0wrk/.agents/skills`) modified (workspace-local skill changes surface via `workspace:tree_changed` instead) |
| `agents:changed`         | backend → frontend | no payload (`nil`)                                                                                                                                                                                                   | backend/frontend_api_agents.go  | Subagent Profile directories outside the workspace (e.g. `~/.agents/agents`, `~/.c0wrk/.agents/agents`) modified — AGENT.md discovery (mirrors `skills:changed`; workspace-local agent changes surface via `workspace:tree_changed` instead) |
| `vector_index:status`    | backend → frontend | `{state, progress, files_indexed, total_files, current_file?, branch?, phase?, indices?}` (see `VectorIndexStatus`; `phase` is one of `both` \| `embedding` \| `lexical`; `indices` lists the indices being written). Startup/unavailable failure paths (`desktop/startup_phases.go`) emit `{available: false, reason: string}` instead | desktop/startup_phases.go + backend/frontend_api_project.go | Vector index status update (primary emitter: startup_phases on background init; secondary: frontend_api_project on project-switch reindex) |
| `tool_manager:start`    | backend → frontend | `{tools: [{name: string, version: string}]}`                                                                                                                                           | desktop/startup_phases.go      | Tool install/update starting (emitted before any download begins) |
| `tool_manager:progress` | backend → frontend | `{tool: string, stage: string, bytes_done: int64, bytes_total: int64}` (stage is `download` \| `extract` \| `python_bootstrap`; `bytes_total=0` means indeterminate)                     | desktop/startup_phases.go      | Per-tool progress update (bytes reported at ~100ms intervals)  |
| `tool_manager:done`     | backend → frontend | `{installed_count: int, skipped_count: int}`                                                                                                                                            | desktop/startup_phases.go      | All tool installations complete                                 |
| `mcp:ready`             | backend → frontend | no payload (`nil`)                                                                                                                                                                                                  | desktop/startup_phases.go       | MCP gateway startup goroutine finished (success or failure); lets the MCP settings dialog refresh its "Starting…" placeholder into real per-server status (once, via `startMCPReadyNotifier`) |
| `update:available`      | backend → frontend | `UpdateInfo` `{available, current_version, latest_version, release_notes, published_at, html_url, asset_name}`                                                                                                       | backend/frontend_api_updater.go | `CheckForUpdates` found a newer release |
| `update:progress`       | backend → frontend | `UpdateProgress` `{done: int64, total: int64}`                                                                                                                                                                       | backend/frontend_api_updater.go | Archive download progress (`DownloadUpdate`) |
| `update:downloaded`     | backend → frontend | `{archive: string}`                                                                                                                                                                                                  | backend/frontend_api_updater.go | Update archive downloaded + integrity-verified, ready to apply |
| `update:none`           | backend → frontend | `UpdateInfo`                                                                                                                                                                                                        | backend/frontend_api_updater.go | `CheckForUpdates` found no newer release (current, or latest skipped/updates disabled) |
| `update:error`          | backend → frontend | `{message: string}`                                                                                                                                                                                                 | backend/frontend_api_updater.go | Any update step (check/download) failed |

## Session-Scoped Events

Pattern: `session:${sessionId}:${eventType}`

### Orchestration Lifecycle

All session-scoped events may additionally include `plan_step_id` and `retry_attempt` fields when emitted from a scoped emitter (see `WithPlanStepID` / `WithRetryAttempt`).

| Event Type           | Payload                                                                                                              | Handler Hook       | Description            |
| -------------------- | -------------------------------------------------------------------------------------------------------------------- | ------------------ | ---------------------- |
| `routing`            | `{mode, domain, complexity}`                                                                                         | usePlanEvents      | Routing decision made  |
| `plan_generated`     | `{step_count, steps[], progress, current_step_index, completed_count, total_count}`                                  | usePlanEvents      | Plan created           |
| `plan_step_start`    | `{step_id, description, summary, progress, current_step_index, completed_count, total_count}`                        | usePlanEvents      | Step execution started |
| `plan_step_complete` | `{step_id, success, duration (ms), progress, current_step_index, completed_count, total_count, error?}`              | usePlanEvents      | Step finished          |
| `reflection`         | `{summary, insights, suggested_action, root_cause, failure_analysis, action_plan, reasoning, attempt, max_attempts}` | usePlanEvents      | Failure analyzed       |
| `retry`              | `{attempt, max_attempts}`                                                                                            | useLifecycleEvents | Retry started          |
| `step_retry`         | `{step_id, attempt, max_attempts}`                                                                                   | useLifecycleEvents | Step-level retry       |

### Streaming & Content

| Event Type        | Payload                                  | Handler Hook  | Description                                                                             |
| ----------------- | ---------------------------------------- | ------------- | --------------------------------------------------------------------------------------- |
| `thought`         | `{step_num, content, reasoning}`         | useChatEvents | LLM reasoning/thinking                                                                  |
| `assistant_chunk` | `{content, accumulated_content}`         | useChatEvents | Streaming LLM text (Go emits both delta `content` and cumulative `accumulated_content`) |
| `assistant_done`  | `{content, input_tokens, output_tokens}` | useChatEvents | LLM response complete (always preceded by a `session_tokens` emission)                  |

### Tool Execution

| Event Type    | Payload                                                            | Handler Hook  | Description             |
| ------------- | ------------------------------------------------------------------ | ------------- | ----------------------- |
| `tool_call`   | `{tool_call_id, step, call_idx, tool, args, source, parsed_args?, attachment_name?}` | useToolEvents | Tool invocation started (`attachment_name` enriches `read_attachment` calls with the original file name, resolved by the backend so cards render it after restart when the frontend cache is empty) |
| `tool_result` | `{tool_call_id?, step, call_idx, result_len, result, error?}`      | useToolEvents | Tool execution result (`error: true` when the tool failed; propagated into live tool card status) |

### User Interaction

| Event Type     | Payload                                          | Handler Hook    | Description                                    |
| -------------- | ------------------------------------------------ | --------------- | ---------------------------------------------- |
| `tool_confirm` | `{confirm_id, tool, args, reasoning, tool_call_id?, disable_judge?}`            | useActionEvents | Confirmation required (`tool_call_id` anchors the card to the triggering `tool_call`; `reasoning` is a human-readable explanation of why approval is needed — symlink traversal, judge flag, auto-approve denial, Smart Approve verdict, or the tool's default mutating-action policy; rendered as "Why approval is needed"; `disable_judge` hides the advisory "Ask Agent" button when the strict judge already evaluated the call) |
| `ask_user`     | `{request_id, questions: AskUserQuestion[]}`     | useActionEvents | Agent asks user                                |
| `step_limit`   | `{request_id, current_step, max_steps, reason?}` | useActionEvents | Step limit or circuit breaker reached          |
| `goal_proposal` | `{request_id, session_id, condition, verify, verification_mode?}` | useGoalEvents | Goal-mode derivation agent called `propose_goal`; surfaces as a pending action that **blocks the agent** until the user responds. `verification_mode` (`executable`/`re_derivation`) is the derivation-chosen mode the approval panel shows/edits (absent = default `executable`). Persisted (role `goal_proposal`) so it reappears on reload; renders as a `goal_proposal` DisplayItem (Approve-with-edits / Cancel). See [domains/goal-mode.md](../domains/goal-mode.md). |

### Context & Memory

| Event Type           | Payload                                                                                                                      | Handler Hook     | Description                                                 |
| -------------------- | ---------------------------------------------------------------------------------------------------------------------------- | ---------------- | ----------------------------------------------------------- |
| `context_fill`       | `{fill_percent, used_tokens, max_tokens, status, plan_step_id?, session_input_tokens, session_output_tokens, model, family}` | useContextEvents | Context window status (per-agent, enriched w/ session sums) |
| `context_compaction` | `{before_percent, after_percent, plan_step_id?}`                                                                             | useContextEvents | Compaction performed                                        |
| `session_tokens`     | `{session_input_tokens, session_output_tokens, model, family, fill_percent, used_tokens, max_tokens}`                                                               | useChatEvents    | Cumulative session token totals update (mirrors session-root context-window fill so the status bar can render a "N of M" tooltip) |

### Task Lifecycle

| Event Type              | Payload                                                                       | Handler Hook       | Description                                                 |
| ----------------------- | ----------------------------------------------------------------------------- | ------------------ | ----------------------------------------------------------- |
| `task_complete`         | `{session_id, output, routing_decision, plan?, reflections?, success, completion?}` | useLifecycleEvents | Task finished. `success: false` + `completion: "partial"/"failed"/"aborted"` for degraded executions delivered with best-effort output; the UI renders an explicit warning. A degraded completion is always followed by `task_failed_resumable` or a `service` warning (never a silent visual success) |
| `task_cancelled`        | `{session_id}`                                                                | useLifecycleEvents | Task cancelled by user                                      |
| `session_paused`        | no payload (session-scoped only)                                              | useChatEvents      | Cooperative pause checkpoint: a `PauseSession` flipped the universal pause signal; the in-flight conductor run stopped at the next step boundary (`ErrPaused` → `ExecutionStatusPaused`), the task was persisted as `"paused"`, and the request exited. The UI unlocks input (shows Resume + Stop). Emitted for **all** tasks — goal and non-goal alike (a goal task's goal stays `active`; see [../domains/goal-mode.md § Pause is Session-Level](../domains/goal-mode.md#pause-is-session-level)) |
| `session_resumed`       | no payload (session-scoped only)                                              | useActionEvents    | A paused task was resumed (`ResumeSession`/nudge-resume). Complementary to `session_paused`: clears the UI's paused state so the input re-locks and Pause/Stop controls reappear. Emitted alongside `task_resumed` |
| `task_failed_resumable` | `{message, task_id?, reason?}`                                                 | useLifecycleEvents | Task failed/incomplete, can resume (`task_id` lets the persisted message be matched/resolved on resume or cancel; `reason` carries a concise contextual cause). A **paused** task does NOT emit this — it surfaces via `session_paused` and resumes via the Resume button / nudge, not a "did not finish" banner |
| `error`                 | `{session_id, error}`                                                         | useChatEvents      | Execution error                                             |
| `service`               | `{content, ...meta}` (meta fields flattened directly, e.g. `phase`)           | useChatEvents      | Service/status message (via `Service` or `ServiceWithMeta`); meta fields are flattened directly into the payload alongside `content`. Goal mode emits dedicated `goal_status` / `goal_progress` events (see Goal Mode below), not this channel. |

### Goal Mode

| Event Type      | Payload                                                                                                                                                                                                                                                                                                      | Handler Hook  | Description             |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------- | ----------------------- |
| `goal_status`   | `{status, turn, condition, max_turns, verification_mode, verdict?, reason?, evidence?, verification?, verification_reason?, verification_evidence?}` — dedicated event type (NOT a `service` phase). Full goal-state snapshot emitted on every transition and after each turn. `verification` carries the independent verifier's outcome (`confirmed`/`rejected`/`off`); `verification_reason`/`verification_evidence` appear only when the verifier confirmed | useGoalEvents | Goal loop state snapshot |
| `goal_progress` | `{turn, max_turns, condition}` — dedicated event type emitted mid-loop (after a non-terminal turn) so the frontend shows live progress toward the budget                                                                                                                                                       | useGoalEvents | Mid-loop goal progress  |

See [../domains/goal-mode.md](../domains/goal-mode.md).

### Agent Internals

| Event Type          | Payload                                                             | Handler Hook      | Description             |
| ------------------- | ------------------------------------------------------------------- | ----------------- | ----------------------- |
| `subagent_launch`   | `{step_id, description}`                                            | useSubagentEvents | Subagent started        |
| `subagent_complete` | `{step_id, success, duration (ms)}`                                 | useSubagentEvents | Subagent finished       |
| `skills_activated`  | `{skills: string[]}`                                                | useChatEvents     | Skills matched for task |
| `tools_assigned`    | `{tools: string[]}`                                                 | useLifecycleEvents | Tools curated for the task by Small-LLM essential-tools narrowing (mirrors `skills_activated` as a `status` card). Persisted (role `status`). See [../domains/small-llm.md](../domains/small-llm.md). |
| `step_todo_update`  | `{step_id?, items: {text, checked}[], completed_count, total_count}` | usePlanEvents     | Checklist update (step_id optional — empty for standalone Conductor checklist without a declared plan) |
| `memory_read`       | `{step_num, content}`                                               | useChatEvents     | Agent read from persistent memory |

### Session Lifecycle

| Event Type         | Payload                    | Handler Hook       | Description                    |
| ------------------ | -------------------------- | ------------------ | ------------------------------ |
| `session_created`  | `{id, name, created_at}`   | useLifecycleEvents | New session created            |
| `session_deleted`  | `{id}`                     | useLifecycleEvents | Session permanently deleted    |
| `session_archived` | `{id, archived}`           | useLifecycleEvents | Session archive state toggled (archived=true)  |
| `session_unarchived` | `{id, archived}`         | useLifecycleEvents | Session archive state toggled (archived=false) |
| `session_renamed`  | `{id, old_name, new_name}` | useLifecycleEvents | Title changed (auto or manual) |
| `session_pinned`   | `{id, pinned}`             | useLifecycleEvents | Session pin toggled on (affects ordering/filtering) |
| `session_unpinned` | `{id, pinned}`             | useLifecycleEvents | Session pin toggled off |
| `message_received` | `{session_id, text}`       | useLifecycleEvents | User message persisted         |

### Attachments

| Event Type          | Payload                                                            | Handler Hook         | Description                 |
| ------------------- | ------------------------------------------------------------------ | -------------------- | --------------------------- |
| `attachments:changed` | `{attachments: AttachmentInfo[], failed?: {path, error}[]}`      | useAttachmentEvents  | Pending attachment list changed. `attachments` is the full current pending list (documents + images combined — image entries carry `is_image: true` and a `thumbnail` JPEG data URI); the UI replaces its store. `failed` carries per-file failures from the most recent attach operation (absent on remove/send-clear). On `SendMessage` the pending list is flushed (documents into the blackboard, images into image content blocks) and the event carries an empty `attachments` list, so chips clear automatically |

### Executor Internals

| Event Type      | Payload                     | Handler Hook       | Description              |
| --------------- | --------------------------- | ------------------ | ------------------------ |
| `step_start`    | `{step_num}`                | useLifecycleEvents | ReAct loop step started  |
| `step_complete` | `{step_num, duration (ms)}` | useLifecycleEvents | ReAct loop step finished |
| `finishing`     | `{step_num, summary}`       | useLifecycleEvents | Agent called finish tool |

### Task Resumption & Terminal

| Event Type        | Payload                           | Handler Hook    | Description                  |
| ----------------- | --------------------------------- | --------------- | ---------------------------- |
| `task_resumed`    | no payload (session-scoped only)  | useActionEvents | A paused or failed task resumed (`ResumeSession`/`ResumeTask`). Emitted alongside `session_resumed` when resuming a paused task |
| `terminal_output` | `{data: string}` (base64-encoded) | Terminal.tsx    | PTY output for terminal mode |

### Plan Review

| Event Type          | Payload                                                  | Handler Hook    | Description                                                                                                                               |
| ------------------- | -------------------------------------------------------- | --------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| `plan_review_ready` | `{request_id: string, plan_path: string, plan_content: string}` | useActionEvents | Plan awaiting user review (`declare_plan` mode=`await_approval`). Surfaced as a pending action; resolved by the frontend→backend `plan_approval_response` event. Emitted by the desktop approval resolver (e.g. `desktop/startup_phases.go` restore path); persisted (role `plan_review`) so it survives app restart. |

### Blackboard & Judge

| Event Type            | Payload                           | Handler Hook         | Description                 |
| --------------------- | --------------------------------- | -------------------- | --------------------------- |
| `blackboard_updated`  | `{change_type}`                   | useBlackboardEvents  | Blackboard state changed    |
| `tool_judge_response` | `{confirm_id, reasoning, error?}` | useToolJudgeEvents | LLM judge evaluation result (response to `tool_judge_request` frontend→backend event) |

## Frontend-to-Backend Events

| Event                   | Direction          | Payload                                                           | Purpose                           |
| ----------------------- | ------------------ | ----------------------------------------------------------------- | --------------------------------- |
| `tool_confirm_response` | frontend → backend | `{confirm_id, response}`                                          | User's tool confirmation decision |
| `tool_judge_request`    | frontend → backend | `{confirm_id}` (see `JudgeRequestPayload`)                        | Request LLM judge evaluation (response via `tool_judge_response` backend→frontend event) |
| `ask_user_response`     | frontend → backend | `{request_id, answers}`                                           | User's answers to agent questions |
| `step_limit_response`   | frontend → backend | `{request_id, response}` (`allow_once` / `allow_more` / `allow_always` / `deny`) | User's step limit decision        |
| `plan_approval_response` | frontend → backend | `{request_id, decision, feedback?}` (`approve` / `request_changes` / `abandon`; `feedback` non-empty when `request_changes`) (see `PlanApprovalResponsePayload`) | User's decision on a plan awaiting review (`declare_plan` mode=`await_approval`). Resolves the pending plan-review action surfaced by `plan_review_ready` |
| `goal_proposal_response` | frontend → backend | `{request_id, decision, condition?, verify?, verification_mode?}` (`approve` / `cancel`) | User's sign-off on a proposed goal. `verification_mode` overrides the derivation-chosen mode. Both the event path and the RPC path (`ConfirmGoal`/`CancelGoal`) funnel through a single resolver on the desktop pending map. See [../domains/goal-mode.md](../domains/goal-mode.md). |

## Event Handling Pattern (Frontend)

```
useSessionEvents(sessionId)
  ├─ Subscribes to all session events on mount
  ├─ Dispatches to type-specific handler hooks
  └─ Unsubscribes on unmount / session change

Each handler:
  1. Type guard validates payload structure
  2. Update activity status (chatStore)
  3. Update domain-specific store (planStore, chatStore, etc.)
  4. Trigger scroll if needed (via ScrollContext)
```

## Streaming Pattern

```
assistant_chunk events (multiple):
  → chatStore.setStreamingText(accumulated_content, sessionId)
  → Component renders streaming text

assistant_done event (once):
  → chatStore.flushStreamingToMessage(content, sessionId)
  → Streaming cleared, permanent message added
```

## Breaking Change Checklist

- New event type: Go emitter method + event data struct + TS interface + type guard + handler hook
- Modified payload: update Go struct + TS interface + type guard + handler logic
- Removed event: remove emitter method + remove TS type + remove handler subscription
- Renamed event: update BOTH Go event name constant AND all frontend `EventsOn` calls
