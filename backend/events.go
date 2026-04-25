package backend

// ---------------------------------------------------------------------------
// Wails event names emitted TO the frontend
// ---------------------------------------------------------------------------

const (
	// Startup/lifecycle events
	EventStartupError = "startup_error"
	EventBackendReady = "backend:ready"

	// Project events
	EventProjectsLoaded       = "projects:loaded"
	EventProjectCreated       = "project:created"
	EventProjectDeleted       = "project:deleted"
	EventProjectRenamed       = "project:renamed"
	EventProjectSwitched      = "project:switched"
	EventWorkspaceTreeChanged = "workspace:tree_changed"

	// Session events
	EventSessionsLoaded = "sessions:loaded"
	EventSessionEvent   = "session:event"

	// Vector index events
	EventVectorIndexStatus = "vector_index:status"


)

// ---------------------------------------------------------------------------
// Wails event names received FROM the frontend
// ---------------------------------------------------------------------------

const (
	EventToolConfirmResponse = "tool_confirm_response"
	EventToolJudgeRequest    = "tool_judge_request"
	EventAskUserResponse     = "ask_user_response"
	EventStepLimitResponse   = "step_limit_response"
)
