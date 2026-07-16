# Event Catalog

## Overview

Events are the real-time communication channel from backend to frontend during task execution. They enable live UI updates without polling.

## Global Events

Payloads are emitted as-is (no wrapping object) unless shown as `{...}`.

| Event Name               | Direction          | Payload                                                                                                                                                                                                              | Emitter                         | Description                                                 |
| ------------------------ | ------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------- | ----------------------------------------------------------- |
| `startup_error`          | backend → frontend | `{message: string, error: string}`                                                                                                                                                                                   | desktop/startup_phases.go       | Fatal startup error                                         |
| `runtime_error`          | backend → frontend | `{message: string, error: string}`                                                                                                                                                                                   | backend/frontend_api_project.go | Non-fatal runtime error (e.g. missing git binary on project switch) |
| `backend:ready`          | backend → frontend | `ProjectInfo[]` (pre-loaded projects) or no payload                                                                                                                                                                  | desktop/startup_phases.go       | Backend initialization complete                             |
| `projects:loaded`        | backend → frontend | `ProjectInfo[]`                                                                                                                                                                                                      | desktop/startup_phases.go       | Project list available (emitted directly as array)          |
| `sessions:loaded`        | backend → frontend | `SessionInfo[]`                                                                                                                                                                                                      | desktop/startup_phases.go       | Session list for the most recent project (emitted directly) |
| `project:created`        | backend → frontend | `ProjectInfo`                                                                                                                                                                                                        | backend/frontend_api_project.go | New project created                                         |
| `project:deleted`        | backend → frontend | `string` (project id)                                                                                                                                                                                                | backend/frontend_api_project.go | Project deleted                                             |
| `project:renamed`        | backend → frontend | `{id: string, name: string}`                                                                                                                                                                                         | backend/frontend_api_project.go | Project renamed                                             |
| `project:switched`       | backend → frontend | `ProjectInfo`                                                                                                                                                                                                        | backend/frontend_api_project.go | Active project changed                                      |
| `workspace:tree_changed` | backend → frontend | no payload (`nil`)                                                                                                                                                                                                   | backend/frontend_api_project.go | File tree modified (workspace watcher callback)             |
| `git:status_changed`     | backend → frontend | no payload (`nil`)                                                                                                                                                                                                   | backend/frontend_api_git.go     | Git working tree changed (emitted after stage/unstage/commit/checkout/merge/rebase/pull/push/fetch/discard) |
| `workdirs:changed`       | backend → frontend | no payload (`nil`)                                                                                                                                                                                                   | backend/frontend_api_workdirs.go | Auxiliary work directory added/updated/deleted (triggers UI + context reload on next message) |
| `vector_index:status`    | backend → frontend | `{state, progress, files_indexed, total_files, current_file?, branch?, phase?, indices?}` (see `VectorIndexStatus`; `phase` is one of `both` \| `embedding` \| `lexical`; `indices` lists the indices being written) | desktop/startup_phases.go + backend/frontend_api_vector.go | Vector index status update (primary emitter: startup_phases; secondary: frontend_api_vector on reindex) |
| `tool_manager:start`    | backend → frontend | `{tools: [{name: string, version: string}]}`                                                                                                                                           | desktop/startup_phases.go      | Tool install/update starting (emitted before any download begins) |
| `tool_manager:progress` | backend → frontend | `{tool: string, stage: string, bytes_done: int64, bytes_total: int64}` (stage is `download` \| `extract` \| `python_bootstrap`; `bytes_total=0` means indeterminate)                     | desktop/startup_phases.go      | Per-tool progress update (bytes reported at ~100ms intervals)  |
| `tool_manager:done`     | backend → frontend | `{installed_count: int, skipped_count: int}`                                                                                                                                            | desktop/startup_phases.go      | All tool installations complete                                 |

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
| `tool_call`   | `{tool_call_id, step, call_idx, tool, args, source, parsed_args?}` | useToolEvents | Tool invocation started |
| `tool_result` | `{tool_call_id?, step, call_idx, result_len, result, error?}`      | useToolEvents | Tool execution result (`error: true` when the tool failed; propagated into live tool card status) |

### User Interaction

| Event Type     | Payload                                          | Handler Hook    | Description                                    |
| -------------- | ------------------------------------------------ | --------------- | ---------------------------------------------- |
| `tool_confirm` | `{confirm_id, tool, args, reasoning}`            | useActionEvents | Confirmation required (`reasoning` is a human-readable explanation of why approval is needed — symlink traversal, judge flag, auto-approve denial, or the tool's default mutating-action policy; rendered as "Why approval is needed") |
| `ask_user`     | `{request_id, questions: AskUserQuestion[]}`     | useActionEvents | Agent asks user                                |
| `step_limit`   | `{request_id, current_step, max_steps, reason?}` | useActionEvents | Step limit or circuit breaker reached          |

### Context & Memory

| Event Type           | Payload                                                                                                                      | Handler Hook     | Description                                                 |
| -------------------- | ---------------------------------------------------------------------------------------------------------------------------- | ---------------- | ----------------------------------------------------------- |
| `context_fill`       | `{fill_percent, used_tokens, max_tokens, status, plan_step_id?, session_input_tokens, session_output_tokens, model, family}` | useContextEvents | Context window status (per-agent, enriched w/ session sums) |
| `context_compaction` | `{before_percent, after_percent, plan_step_id?}`                                                                             | useContextEvents | Compaction performed                                        |
| `session_tokens`     | `{session_input_tokens, session_output_tokens, model, family}`                                                               | useChatEvents    | Cumulative session token totals update                      |

### Task Lifecycle

| Event Type              | Payload                                                                       | Handler Hook       | Description                                                 |
| ----------------------- | ----------------------------------------------------------------------------- | ------------------ | ----------------------------------------------------------- |
| `task_complete`         | `{session_id, output, routing_decision, plan?, attempt_count?, reflections?, success, completion?, failed_steps?}` | useLifecycleEvents | Task finished. `success: false` + `completion: "partial"/"failed"/"aborted"` for degraded executions delivered with best-effort output; the UI renders an explicit warning. A degraded completion is always followed by `task_failed_resumable` or a `service` warning (never a silent visual success) |
| `task_cancelled`        | `{session_id}`                                                                | useLifecycleEvents | Task cancelled by user                                      |
| `task_failed_resumable` | `{message}`                                                                   | useLifecycleEvents | Task failed/incomplete, can resume                          |
| `error`                 | `{session_id, error}`                                                         | useChatEvents      | Execution error                                             |
| `service`               | `{content, ...meta}` (meta fields flattened directly, e.g. `phase`)           | useChatEvents      | Service/status message (via `Service` or `ServiceWithMeta`) |

### Agent Internals

| Event Type          | Payload                                                             | Handler Hook      | Description             |
| ------------------- | ------------------------------------------------------------------- | ----------------- | ----------------------- |
| `subagent_launch`   | `{step_id, description}`                                            | useSubagentEvents | Subagent started        |
| `subagent_complete` | `{step_id, success, duration (ms)}`                                 | useSubagentEvents | Subagent finished       |
| `skills_activated`  | `{skills: string[]}`                                                | useChatEvents     | Skills matched for task |
| `step_todo_update`  | `{step_id?, items: {text, checked}[], completed_count, total_count}` | usePlanEvents     | Checklist update (step_id optional — empty for standalone Conductor checklist without a declared plan) |
| `memory_read`       | `{step_num, content}`                                               | useChatEvents     | Agent read from persistent memory |

### Session Lifecycle

| Event Type         | Payload                    | Handler Hook       | Description                    |
| ------------------ | -------------------------- | ------------------ | ------------------------------ |
| `session_created`  | `{id, name, created_at}`   | useLifecycleEvents | New session created            |
| `session_deleted`  | `{id}`                     | useLifecycleEvents | Session permanently deleted    |
| `session_archived` | `{id, archived}`           | useLifecycleEvents | Session archive state toggled  |
| `session_renamed`  | `{id, old_name, new_name}` | useLifecycleEvents | Title changed (auto or manual) |
| `message_received` | `{session_id, text}`       | useLifecycleEvents | User message persisted         |

### Attachments

| Event Type          | Payload                                                            | Handler Hook         | Description                 |
| ------------------- | ------------------------------------------------------------------ | -------------------- | --------------------------- |
| `attachments:changed` | `{attachments: AttachmentInfo[], failed?: {path, error}[]}`      | useAttachmentEvents  | Pending attachment list changed. `attachments` is the full current pending list — the UI replaces its store. `failed` carries per-file failures from the most recent attach operation (absent on remove/send-clear). On `SendMessage` the pending list is flushed into the blackboard and the event carries an empty `attachments` list, so chips clear automatically |

### Executor Internals

| Event Type      | Payload                     | Handler Hook       | Description              |
| --------------- | --------------------------- | ------------------ | ------------------------ |
| `step_start`    | `{step_num}`                | useLifecycleEvents | ReAct loop step started  |
| `step_complete` | `{step_num, duration (ms)}` | useLifecycleEvents | ReAct loop step finished |
| `finishing`     | `{step_num, summary}`       | useLifecycleEvents | Agent called finish tool |

### Task Resumption & Terminal

| Event Type        | Payload                           | Handler Hook    | Description                  |
| ----------------- | --------------------------------- | --------------- | ---------------------------- |
| `task_resumed`    | no payload (session-scoped only)  | useActionEvents | Failed task resumed          |
| `terminal_output` | `{data: string}` (base64-encoded) | Terminal.tsx    | PTY output for terminal mode |

### Plan Review

| Event Type                       | Payload                                                                     | Handler Hook         | Description                                                   |
| -------------------------------- | --------------------------------------------------------------------------- | -------------------- | ------------------------------------------------------------- |
| `plan_review_ready`              | `{plan_path: string, plan_content: string}`                                  | usePlanReviewEvents  | Plan generated, saved to .md, awaiting user review            |
| `plan_validation_failed`         | `{issues: [{step_index?, field, severity, description, suggestion?}]}`      | usePlanReviewEvents  | Plan structural or semantic validation failed                 |
| `plan_review_awaiting_feedback`  | no payload (session-scoped only)                                            | usePlanReviewEvents  | Plan rejected without feedback; waiting for user to describe  |
| `plan_review_accepted`           | no payload (session-scoped only)                                            | usePlanReviewEvents  | Plan approved, execution starting                             |
| `plan_review_rejected`           | no payload (session-scoped only)                                            | usePlanReviewEvents  | Plan rejected (emitted alongside `plan_review_awaiting_feedback` when no immediate feedback is provided) |

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
| `step_limit_response`   | frontend → backend | `{request_id, response}` (`allow_once` / `allow_always` / `deny`) | User's step limit decision        |

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
