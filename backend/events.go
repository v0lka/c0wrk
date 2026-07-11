package backend

// Event constant organization:
// - Global/lifecycle events: defined here in backend/events.go
// - Session-scoped orchestration events: defined in backend/session/events.go
//
// Frontend subscribes to global events via their string name and to session
// events via the `session:${id}:${type}` convention (see frontend/src/api/runtime.ts).

// ---------------------------------------------------------------------------
// Wails event names emitted TO the frontend
// ---------------------------------------------------------------------------

// EventStartupError is emitted when the desktop application fails to start.
const EventStartupError = "startup_error"

// EventRuntimeError is emitted when a non-fatal runtime error occurs that
// should be shown to the user (e.g. git not found when switching to CODE mode).
const EventRuntimeError = "runtime_error"

// EventBackendReady is emitted when the Go backend finishes initialization.
const EventBackendReady = "backend:ready"

// EventToolManagerStart is emitted before managed-tool downloads begin,
// carrying the list of tools that need to be installed or updated.
// The frontend uses it to show the tool-install splash screen.
const EventToolManagerStart = "tool_manager:start"

// EventToolManagerDone is emitted when all managed-tool installations complete.
const EventToolManagerDone = "tool_manager:done"

// EventProjectsLoaded is emitted when all projects have been loaded from disk.
const EventProjectsLoaded = "projects:loaded"

// EventProjectCreated is emitted when a new project is created.
const EventProjectCreated = "project:created"

// EventProjectDeleted is emitted when a project is deleted.
const EventProjectDeleted = "project:deleted"

// EventProjectRenamed is emitted when a project is renamed.
const EventProjectRenamed = "project:renamed"

// EventSessionRenamed is emitted when a session is renamed (either manually
// or via background auto-titling). Unlike the session-scoped
// `session:{id}:session_renamed` orchestration event, this global event lets
// the sidebar update a session's title even when it is not the active session
// (mirrors EventProjectRenamed).
const EventSessionRenamed = "session:renamed"

// EventProjectSwitched is emitted when the active project changes.
const EventProjectSwitched = "project:switched"

// EventWorkspaceTreeChanged is emitted when filesystem changes are detected
// in the active workspace.
const EventWorkspaceTreeChanged = "workspace:tree_changed"

// EventSkillsChanged is emitted when skill directories outside the workspace
// (e.g. ~/.agents/skills, ~/.c0wrk/.agents/skills) are modified. Workspace-
// local skill changes are surfaced via EventWorkspaceTreeChanged instead.
const EventSkillsChanged = "skills:changed"

// EventGitStatusChanged is emitted when the git staging area changes
// (stage, unstage, commit). The payload is the repository path so the
// frontend knows which project was affected.
const EventGitStatusChanged = "git:status_changed"

// EventSessionsLoaded is emitted when all sessions have been loaded from disk.
const EventSessionsLoaded = "sessions:loaded"

// EventSessionEvent is the prefix for session-scoped orchestration events.
// Actual events use the pattern session:{id}:{type}.
const EventSessionEvent = "session:event"

// EventVectorIndexStatus is emitted when the vector index state or progress changes.
const EventVectorIndexStatus = "vector_index:status"

// ---------------------------------------------------------------------------
// Wails event names received FROM the frontend
// ---------------------------------------------------------------------------

// EventToolConfirmResponse is received from the frontend when the user
// approves or denies a tool execution confirmation.
const EventToolConfirmResponse = "tool_confirm_response"

// EventToolJudgeRequest is received from the frontend when the user
// submits a judge-the-output evaluation.
const EventToolJudgeRequest = "tool_judge_request"

// EventAskUserResponse is received from the frontend when the user
// responds to a multi-question prompt.
const EventAskUserResponse = "ask_user_response"

// EventStepLimitResponse is received from the frontend when the user
// decides to continue, cancel, or adjust after hitting the step limit.
const EventStepLimitResponse = "step_limit_response"

// EventPlanApprovalResponse is received from the frontend when the user
// approves, requests changes to, or abandons a plan awaiting review.
const EventPlanApprovalResponse = "plan_approval_response"
