# Session Lifecycle

## Purpose

Manages the lifecycle of user sessions: creation, message handling, task execution, persistence, and resumption after failure.

## Key Files

- `backend/session/manager.go` — SessionManager (session CRUD, message routing)
- `backend/session/file_coherence.go` — FileCoherenceTracker (cross-session conflict detection)
- `backend/session/persistence.go` — SessionStore (SQLite persistence)
- `backend/session/events.go` — event data structs (session created/renamed/deleted/archived, message received)
- `backend/session/emitter.go` — WailsEmitter (bridges events to frontend)
- `backend/session/event_persister.go` — EventPersister (persists events to SQLite)
- `backend/session/task_adapter.go` — task step/fact adapter for persistence
- `backend/session/title.go` — auto title generation via LLM
- `backend/frontend_api_session.go` — FrontendAPI session methods
- `backend/frontend_api_project.go` — project switch state persistence + destination session fallback
- `core/orchestrator.go` — Orchestrator.HandleMessage, Orchestrator.Resume

## Flow

### Session Creation

```
User clicks "New Chat" (or first message in empty state)
  → Frontend: CreateSession()
  → Backend: FrontendAPI.CreateSession()
      ├─ Read active project ID + workspace path
      ├─ Call SessionManager.CreateSession(projectID, workspacePath)
      ├─ Persist to SQLite (sessions table; best-effort when store wired)
      └─ Return SessionInfo {id, name, projectId, createdAt}
  → Frontend: sessionStore.addSession()
```

### Project Switch Session Restoration

```
User switches project
  → Frontend hook: useProjectSwitchState(nextProjectId)
      ├─ Save source project UI state (best-effort):
      │   SaveProjectSwitchState({project_id, saved_session_id, open_tabs, active_file})
      ├─ SwitchProject(nextProjectId)
      ├─ Reset session store for destination project
      ├─ GetProjectSwitchState(nextProjectId)
      ├─ Restore open tabs + active file in fileViewerStore
      └─ Resolve active session deterministically:
          1) saved_session_id when it belongs to destination project
          2) latest destination session by activity timestamp
          3) create new session for empty destination project

Backend SwitchProject path
  → persistCurrentProjectSwitchState(previousProjectID) (normalize/validate persisted source state)
  → applySavedProjectSwitchState(destinationProjectID)
      ├─ resolveSavedSessionForProject(projectID, savedSessionID)
      ├─ fallback to resolveLatestSessionForProject(projectID)
      └─ fallback to createSessionForProject(projectID)
  → Persist resolved saved_session_id in project_ui_state
```

### Message Handling

```
User sends message
  → Frontend: SendMessage(sessionId, text, mode, activeSkills, modelOverride, reasoningEffort)
  → Backend: FrontendAPI.SendMessage()
      ├─ Persist original text to DB (preserves /skill and @file refs)
      ├─ Preprocess text for orchestrator:
      │   ├─ Strip /skill references from text
      │   └─ Convert @file references to fileref:// URIs
      ├─ Get or create Orchestrator for session (via factory)
      ├─ Create emitter (WailsEmitter + EventPersister)
      ├─ Enrich task context:
      │   ├─ WithWorkspacePath (project workspace)
      │   ├─ WithTempDir (session-specific temp directory)
      │   └─ WithCoherence (FileCoherenceTracker for cross-session conflict detection)
      ├─ Determine opts: {TaskID, ExecutionMode, UserSkills, ModelOverride, ReasoningEffort}
      │   ├─ First message: TaskID=""
      │   └─ Continuation: TaskID=lastCompletedTaskID
      ├─ Call orchestrator.HandleMessage(ctx, preprocessedText, sessionId, opts)
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

`task_failed_resumable` is NOT emitted when the router decides
`needs_clarification` (and the user did not explicitly invoke a /skill). In
that case the planner never runs, so there is nothing to resume — the
orchestrator marks the just-created task as completed and returns the
clarification message instead.

### Discarding an Unfinished Task

```
User clicks "Cancel" on the resume prompt
  → Frontend: CancelUnfinishedTask(sessionId)
  → Backend: FrontendAPI.CancelUnfinishedTask()
      └─ TaskStoreAdapter.PersistCompletion(taskID, "", 0)
          (marks the unfinished task as completed; no further resume prompt)
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

Persisted in SQLite (`~/.c0wrk/database.db`) — schema defined in `backend/session/persistence.go` and `backend/project/persistence.go`:

- `projects` — project roster (in `backend/project/persistence.go`)
- `project_ui_state` — project_id, saved_session_id, open_tabs (JSON), active_file, updated_at; stores per-project switch UI restoration state
- `sessions` — id, project_id, name, created_at, last_active_at, archived, total_input_tokens, total_output_tokens, model, family
- `session_messages` — id, session_id, role, content, metadata (JSON), created_at
- `tasks` — id, session_id, original_request, routing_decision (JSON), plan (JSON), reflections (JSON), final_output, attempt_count, status, created_at, completed_at
- `task_steps` — step_id, task_id, summary, full_output, error_text, steps (JSON), created_at (PRIMARY KEY (task_id, step_id))
- `task_facts` — task_id, facts (JSON), updated_at
- `terminal_commands` — id, session_id, command, created_at

Blackboard state is reconstructed from `tasks` + `task_steps` + `task_facts` on resume (no dedicated `blackboard` column). Events are streamed via the Wails runtime and are NOT persisted to a standalone table — any event state that must survive restart is folded into `session_messages` or `tasks`/`task_steps` via `backend/session/event_persister.go`.

### SessionStore Interface

The `backend/session/persistence.go` defines the `SessionStore` interface:

| Method                                                       | Description                                                      |
| ------------------------------------------------------------ | ---------------------------------------------------------------- |
| `SaveSession(ctx, info)`                                     | Upsert session (INSERT OR REPLACE)                               |
| `LoadSession(ctx, id)`                                       | Load session by ID (returns nil if not found)                    |
| `ListSessions(ctx)`                                          | List all sessions ordered by last activity                       |
| `ListSessionsByProject(ctx, projectID)`                      | List sessions for a specific project                             |
| `DeleteSession(ctx, id)`                                     | Delete session and cascade messages                              |
| `ArchiveSession(ctx, id, archived)`                          | Set archived flag on session                                     |
| `RenameSession(ctx, id, name)`                               | Update session name                                              |
| `UpdateSessionTokens(ctx, id, input, output, model, family)` | Update accumulated token counts and model info                   |
| `UpdateSessionActivity(ctx, id)`                             | Update last_active_at timestamp to now                           |
| `SaveMessage(ctx, msg)`                                      | Insert a new chat message                                        |
| `LoadMessages(ctx, sessionID)`                               | Load all messages for session (ordered by created_at)            |
| `DeleteMessages(ctx, sessionID)`                             | Delete all messages for session                                  |
| `SaveTerminalCommand(ctx, sessionID, command)`               | Save terminal command to history                                 |
| `LoadTerminalCommands(ctx, sessionID, limit)`                | Load most recent terminal commands                               |
| `Close()`                                                    | Close the store (no-op for SQLite, lifecycle managed externally) |

### Conversation History

The orchestrator maintains a conversation history window:

- `MaxHistoryMessages` (default: 20) messages retained
- Older messages pruned from history (not from persistence)
- History sent to Router for context-aware classification
- History NOT sent to executor (executor has its own context window)

### Auto Title Generation

When the first message is received for a session with the default auto-generated name:

- Backend calls LLM to generate session title from user message
- Emits `session_renamed` event
- Frontend updates session list

## Core Types

```go
// SessionInfo — session metadata returned to frontend
type SessionInfo struct {
    ID               string
    ProjectID        string
    Name             string
    CreatedAt        string // RFC 3339
    LastActiveAt     string // RFC 3339
    Archived         bool
    Active           bool
    TotalInputTokens  int
    TotalOutputTokens int
    Model            string
    Family           string
}

// HandleOptions — execution mode + user-specified skill overrides
type HandleOptions struct {
    TaskID          string
    ExecutionMode   string   // "normal" | "advanced"
    UserSkills      []string
    ModelOverride   string   // non-empty → use this model for all LLM calls; empty → router default
    ReasoningEffort string   // non-empty → native reasoning value for all LLM calls; empty → use family default
}

// HandleResult — orchestration output
type HandleResult struct {
    Output          string           `json:"output"`
    RoutingDecision *RoutingDecision `json:"routing_decision"`
    Plan            *Plan            `json:"plan,omitempty"`
    Blackboard      Blackboard       `json:"-"`
    AttemptCount    int              `json:"attempt_count,omitempty"`
    Reflections     []Reflection     `json:"reflections,omitempty"`
}
```

## Extension Points

- **SessionStore interface**: replace SQLite with a different backend by implementing all methods in `backend/session/persistence.go`
- **Auto-title generation**: customize the LLM prompt or model used for title generation in `backend/session/title.go`
- **Preprocessing pipeline**: add custom message transforms (e.g., additional filter types) before orchestrator invocation in `FrontendAPI.SendMessage()`
- **Event persistence**: implement `EventPersister` interface for alternative storage backends
- **Session metadata enrichment**: add custom fields to `SessionInfo` and populate them in `SessionManager.Create()`
- **File coherence strategy**: replace `FileCoherenceTracker` in `backend/session/file_coherence.go` with an alternative conflict detection implementation (must satisfy `FileCoherenceChecker` interface from `sdk/tools/coherence.go`)

## Invariants

- One Orchestrator per session (created lazily on first message)
- Session state survives app restart (SQLite persistence)
- Project switch session restore order is deterministic: valid saved session for destination project, otherwise latest destination session, otherwise new destination session
- Destination project switch state always persists the resolved `saved_session_id` in `project_ui_state` when project persistence is wired
- Messages are persisted immediately on receive (not after processing)
- Task state is checkpointed on each step completion (enables resume)
- Cancellation is cooperative (executor checks context at each iteration)
- A failed task can be resumed exactly once (subsequent failure = new task)
- Router-driven `needs_clarification` never produces a resumable task: the
  planner has not run yet and the just-created task record is closed before
  the orchestrator returns.
- The Cancel button on the resume prompt is a hard discard: it persists
  completion on the unfinished task without launching the orchestrator.

## Configuration

| Parameter                          | Default                | Description                 |
| ---------------------------------- | ---------------------- | --------------------------- |
| `orchestration.maxHistoryMessages` | 20                     | Conversation history window |
| Database path                      | `~/.c0wrk/database.db` | SQLite file location        |

## Related Specs

- [orchestration/README.md](orchestration/README.md) — orchestration cycle
- [memory/blackboard.md](memory/blackboard.md) — blackboard persistence
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — session RPC methods
- [../contracts/event-catalog.md](../contracts/event-catalog.md) — task lifecycle events
