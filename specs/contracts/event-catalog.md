# Event Catalog

## Overview

Events are the real-time communication channel from backend to frontend during task execution. They enable live UI updates without polling.

## Global Events

| Event Name               | Direction          | Payload                             | Emitter                         | Description                     |
| ------------------------ | ------------------ | ----------------------------------- | ------------------------------- | ------------------------------- |
| `startup_error`          | backend → frontend | `{error: string}`                   | desktop/startup.go              | Fatal startup error             |
| `backend:ready`          | backend → frontend | `{}`                                | desktop/startup.go              | Backend initialization complete |
| `projects:loaded`        | backend → frontend | `{projects: ProjectInfo[]}`         | backend/application.go          | Project list available          |
| `sessions:loaded`        | backend → frontend | `{sessions: SessionInfo[]}`         | backend/session                 | Session list available          |
| `project:created`        | backend → frontend | `{project: ProjectInfo}`            | backend/frontend_api_project.go | New project created             |
| `project:deleted`        | backend → frontend | `{id: string}`                      | backend/frontend_api_project.go | Project deleted                 |
| `project:renamed`        | backend → frontend | `{id: string, name: string}`        | backend/frontend_api_project.go | Project renamed                 |
| `project:switched`       | backend → frontend | `{id: string}`                      | backend/frontend_api_project.go | Active project changed          |
| `workspace:tree_changed` | backend → frontend | `{path: string}`                    | backend/workspace               | File tree modified              |
| `vector_index:status`    | backend → frontend | `{status: string, progress: float}` | backend/vectorindex             | Index status update             |

## Session-Scoped Events

Pattern: `session:${sessionId}:${eventType}`

### Orchestration Lifecycle

| Event Type           | Payload                                        | Handler Hook       | Description            |
| -------------------- | ---------------------------------------------- | ------------------ | ---------------------- |
| `routing`            | `{mode, domain, complexity}`                   | usePlanEvents      | Routing decision made  |
| `plan_generated`     | `{step_count, steps[]}`                        | usePlanEvents      | Plan created           |
| `plan_step_start`    | `{step_id, description, summary}`              | usePlanEvents      | Step execution started |
| `plan_step_complete` | `{step_id, success, duration_ms, error}`       | usePlanEvents      | Step finished          |
| `reflection`         | `{analysis, corrective_insight, attempt, max}` | usePlanEvents      | Failure analyzed       |
| `retry`              | `{attempt, max_attempts}`                      | useLifecycleEvents | Retry started          |
| `step_retry`         | `{step_id, attempt, max_attempts}`             | useLifecycleEvents | Step-level retry       |

### Streaming & Content

| Event Type        | Payload                          | Handler Hook  | Description            |
| ----------------- | -------------------------------- | ------------- | ---------------------- |
| `thought`         | `{content}`                      | useChatEvents | LLM reasoning/thinking |
| `assistant_chunk` | `{content, accumulated_content}` | useChatEvents | Streaming LLM text     |
| `assistant_done`  | `{content, reasoning_content}`   | useChatEvents | LLM response complete  |

### Tool Execution

| Event Type    | Payload                                   | Handler Hook  | Description             |
| ------------- | ----------------------------------------- | ------------- | ----------------------- |
| `tool_call`   | `{tool_call_id, name, input}`             | useToolEvents | Tool invocation started |
| `tool_result` | `{tool_call_id, name, content, is_error}` | useToolEvents | Tool execution result   |

### User Interaction

| Event Type     | Payload                                   | Handler Hook    | Description           |
| -------------- | ----------------------------------------- | --------------- | --------------------- |
| `tool_confirm` | `{id, tool_name, input, judge_reasoning}` | useActionEvents | Confirmation required |
| `ask_user`     | `{id, questions[]}`                       | useActionEvents | Agent asks user       |
| `step_limit`   | `{id, step_count, max_steps}`             | useActionEvents | Step limit reached    |

### Context & Memory

| Event Type           | Payload                                                 | Handler Hook     | Description           |
| -------------------- | ------------------------------------------------------- | ---------------- | --------------------- |
| `context_fill`       | `{fill_pct, tokens_used, tokens_max, status, strategy}` | useContextEvents | Context window status |
| `context_compaction` | `{strategy, before_pct, after_pct}`                     | useContextEvents | Compaction performed  |
| `session_tokens`     | `{input, output, total, model}`                         | useChatEvents    | Token usage update    |

### Task Lifecycle

| Event Type              | Payload             | Handler Hook       | Description                |
| ----------------------- | ------------------- | ------------------ | -------------------------- |
| `task_complete`         | `{output, task_id}` | useLifecycleEvents | Task finished successfully |
| `task_cancelled`        | `{}`                | useLifecycleEvents | Task cancelled by user     |
| `task_failed_resumable` | `{error, task_id}`  | useLifecycleEvents | Task failed, can resume    |
| `error`                 | `{message}`         | useChatEvents      | Execution error            |
| `service`               | `{content, meta}`   | useChatEvents      | Service/status message     |

### Agent Internals

| Event Type          | Payload                     | Handler Hook       | Description             |
| ------------------- | --------------------------- | ------------------ | ----------------------- |
| `subagent_launch`   | `{step_id, parent_step_id}` | useSubagentEvents  | Subagent started        |
| `subagent_complete` | `{step_id, success}`        | useSubagentEvents  | Subagent finished       |
| `skills_activated`  | `{skills: string[]}`        | useChatEvents      | Skills matched for task |
| `step_todo_update`  | `{step_id, items[]}`        | usePlanEvents      | Step checklist update   |
| `session_renamed`   | `{id, old_name, new_name}`  | useLifecycleEvents | Auto-generated title    |

### Executor Internals

| Event Type      | Payload                | Handler Hook       | Description              |
| --------------- | ---------------------- | ------------------ | ------------------------ |
| `step_start`    | `{step_num}`           | useLifecycleEvents | ReAct loop step started  |
| `step_complete` | `{step_num, duration}` | useLifecycleEvents | ReAct loop step finished |
| `finishing`     | `{step_num, summary}`  | useLifecycleEvents | Agent called finish tool |

### Task Resumption & Terminal

| Event Type        | Payload                   | Handler Hook    | Description                  |
| ----------------- | ------------------------- | --------------- | ---------------------------- |
| `task_resumed`    | `{session_id, text}`      | useActionEvents | Failed task resumed          |
| `terminal_output` | `{data}` (base64-encoded) | Terminal.tsx    | PTY output for terminal mode |

### Blackboard & Judge

| Event Type            | Payload                           | Handler Hook         | Description                 |
| --------------------- | --------------------------------- | -------------------- | --------------------------- |
| `blackboard_updated`  | `{change_type}`                   | useBlackboardEvents  | Blackboard state changed    |
| `tool_judge_response` | `{confirm_id, reasoning, error?}` | ToolConfirmation.tsx | LLM judge evaluation result |

## Frontend-to-Backend Events

| Event                   | Direction          | Payload                  | Purpose                           |
| ----------------------- | ------------------ | ------------------------ | --------------------------------- |
| `tool_confirm_response` | frontend → backend | `{id, response}`         | User's tool confirmation decision |
| `tool_judge_request`    | frontend → backend | `{id, tool_name, input}` | Request LLM judge evaluation      |
| `ask_user_response`     | frontend → backend | `{id, answers}`          | User's answers to agent questions |
| `step_limit_response`   | frontend → backend | `{id, response}`         | User's step limit decision        |

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
