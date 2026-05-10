# Session Lifecycle

## Purpose

Manages the lifecycle of user sessions: creation, message handling, task execution, persistence, and resumption after failure.

## Key Files

- `backend/session/manager.go` — SessionManager (session CRUD, message routing)
- `backend/session/store.go` — SessionStore (SQLite persistence)
- `backend/frontend_api_session.go` — FrontendAPI session methods
- `core/orchestrator.go` — Orchestrator.HandleMessage, Orchestrator.Resume

## Flow

### Session Creation

```
User clicks "New Chat" (or first message in empty state)
  → Frontend: CreateSession(projectID)
  → Backend: SessionManager.Create()
      ├─ Generate session ID
      ├─ Persist to SQLite (sessions table)
      └─ Return SessionInfo {id, name, projectId, createdAt}
  → Frontend: sessionStore.addSession()
```

### Message Handling

```
User sends message
  → Frontend: SendMessage(sessionId, text, mode)
  → Backend: FrontendAPI.SendMessage()
      ├─ Get or create Orchestrator for session (via factory)
      ├─ Create emitter (WailsEmitter + EventPersister)
      ├─ Determine opts: {TaskID, ExecutionMode}
      │   ├─ First message: TaskID=""
      │   └─ Continuation: TaskID=lastCompletedTaskID
      ├─ Call orchestrator.HandleMessage(ctx, text, sessionId, opts)
      │   (executes asynchronously, events stream to frontend)
      ├─ On success: persist result, emit task_complete
      └─ On failure: emit task_failed_resumable or error
```

### Task Resumption

```
User clicks "Resume" (after task_failed_resumable)
  → Frontend: ResumeTask(sessionId)
  → Backend: FrontendAPI.ResumeTask()
      ├─ Restore Blackboard from SQLite (bbRestoreFunc)
      ├─ Restore routing decision
      └─ Call orchestrator.Resume(ctx, bb, routing)
          (continues from last checkpoint)
```

### Task Cancellation

```
User clicks "Cancel"
  → Frontend: CancelTask(sessionId)
  → Backend: FrontendAPI.CancelTask()
      └─ Cancel context → executor stops at next iteration
          → emit task_cancelled
```

### Session Persistence

Persisted in SQLite (`~/.c0wrk/database.db`):

- `sessions` table: id, project_id, name, created_at, last_active_at
- `messages` table: id, session_id, role, content, metadata (JSON), created_at
- `tasks` table: id, session_id, status, blackboard (JSON), routing, created_at
- `events` table: id, session_id, type, data (JSON), created_at

### Conversation History

The orchestrator maintains a conversation history window:

- `MaxHistoryMessages` (default: 20) messages retained
- Older messages pruned from history (not from persistence)
- History sent to Router for context-aware classification
- History NOT sent to executor (executor has its own context window)

### Auto Title Generation

After first successful task completion:

- Backend calls LLM to generate session title from user message + response
- Emits `session_renamed` event
- Frontend updates session list

## Invariants

- One Orchestrator per session (created lazily on first message)
- Session state survives app restart (SQLite persistence)
- Messages are persisted immediately on receive (not after processing)
- Task state is checkpointed on each step completion (enables resume)
- Cancellation is cooperative (executor checks context at each iteration)
- A failed task can be resumed exactly once (subsequent failure = new task)

## Configuration

| Parameter                       | Default                | Description                 |
| ------------------------------- | ---------------------- | --------------------------- |
| `executor.max_history_messages` | 20                     | Conversation history window |
| Database path                   | `~/.c0wrk/database.db` | SQLite file location        |

## Related Specs

- [orchestration/README.md](orchestration/README.md) — orchestration cycle
- [memory/blackboard.md](memory/blackboard.md) — blackboard persistence
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — session RPC methods
- [../contracts/event-catalog.md](../contracts/event-catalog.md) — task lifecycle events
