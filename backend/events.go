package backend

// Event constant organization:
// - Global/lifecycle events: defined here in backend/events.go
// - Session-scoped orchestration events: defined in backend/session/events.go
//
// Frontend subscribes to global events via their string name and to session
// events via the `session:${id}:${type}` convention (see frontend/src/api/runtime.ts).
// Every event is cataloged with payload, emission site and consumer in
// specs/contracts/event-catalog.md — the authoritative reference; these doc
// comments carry only the local delta.

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

// EventConfigUpdated is emitted after a config mutation is persisted — no
// payload, consumers re-read via GetConfig. Dispatched asynchronously from
// persistConfig so the Wails dispatch never runs under configMu; it is the
// retry trigger for consumers whose initial GetConfig landed during the
// startup race.
const EventConfigUpdated = "config:updated"

// EventToolManagerStart is emitted before managed-tool downloads begin,
// carrying the tools to install; the frontend shows the install splash.
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

// EventSessionRenamed is emitted when a session is renamed (manual or
// auto-titled). Unlike the session-scoped orchestration event, this global
// one lets the sidebar update any session's title.
const EventSessionRenamed = "session:renamed"

// EventProjectSwitched is emitted when the active project changes.
const EventProjectSwitched = "project:switched"

// EventGitConfigRisk is emitted when a project switch or added work directory
// opens a repository whose .git/config carries dangerous keys; the payload
// lists every detected key plus the notice that repository-defined hooks do
// not run inside c0wrk. A clean config emits nothing. Emitted from
// notifyGitConfigRisk (backend/frontend_api_gitconfig_risk.go).
const EventGitConfigRisk = "project:git_config_risk"

// EventWorkspaceTreeChanged is emitted when filesystem changes are detected
// in the active workspace.
const EventWorkspaceTreeChanged = "workspace:tree_changed"

// EventFilesDropped is emitted for files dragged onto the window; the payload
// carries the absolute paths under "paths". The webview's own drag-and-drop
// is disabled (dropped files never navigate inside it), making this event the
// sole delivery channel.
const EventFilesDropped = "files:dropped"

// EventSkillsChanged is emitted when skill directories outside the workspace
// change; workspace-local changes surface via EventWorkspaceTreeChanged.
const EventSkillsChanged = "skills:changed"

// EventAgentsChanged is emitted when Subagent Profile directories outside the
// workspace change; mirrors EventSkillsChanged for AGENT.md discovery.
const EventAgentsChanged = "agents:changed"

// EventCommandsChanged is emitted when custom command directories outside the
// workspace change; mirrors EventSkillsChanged for command templates (ADR-096).
const EventCommandsChanged = "commands:changed"

// EventGitStatusChanged is emitted when the git staging area changes; the
// payload is the repository path so the frontend knows which project moved.
const EventGitStatusChanged = "git:status_changed"

// EventWorkDirsChanged is emitted when an auxiliary working directory is
// added, updated, or removed so the frontend refreshes its directory list.
const EventWorkDirsChanged = "workdirs:changed"

// EventSessionsLoaded is emitted when all sessions have been loaded from disk.
const EventSessionsLoaded = "sessions:loaded"

// EventSessionsChanged is emitted after every list-affecting session mutation
// and, via the desktop task-lifecycle mirror (buildUIEmitFunc), for every
// forwarded task transition — surfaces refresh within their debounce window
// instead of the 30s safety poll. No payload: consumers re-read via
// ListSessions/ListAllSessions. Emitted through the shared emitEvent funnel
// (emitSessionsChanged).
const EventSessionsChanged = "sessions:changed"

// EventSessionEvent is the prefix for session-scoped orchestration events
// (actual events: session:{id}:{type}).
const EventSessionEvent = "session:event"

// EventVectorIndexStatus is emitted when the vector index state or progress changes.
const EventVectorIndexStatus = "vector_index:status"

// EventMCPReady is emitted once when the MCP gateway startup goroutine
// finishes (success or failure), letting the settings dialog replace its
// "Starting…" placeholder without polling. Emitted from the desktop layer
// (startMCPReadyNotifier).
const EventMCPReady = "mcp:ready"

// EventResearchChanged is emitted on a RESEARCH mutation for a project (active
// R-NNN switched or research project deleted). RESEARCH is always on for real
// projects — there is no enable/disable action. Payload: project_id + action.
const EventResearchChanged = "research:changed"

// EventResearchFileChanged is emitted when a file inside the research
// directory changes; payload project_id + paths, so the frontend can update
// the hypothesis graph incrementally.
const EventResearchFileChanged = "research:file_changed"

// EventPapersChanged is emitted when a file in the paper library changes. The
// library is a global subdirectory of the research root that lives
// independently of the active R-NNN project — unlike research:file_changed,
// which is scoped to the research artifact tree. Emitted via
// emitPapersChanged.
const EventPapersChanged = "papers:changed"

// ---------------------------------------------------------------------------
// Self-update events (emitted by FrontendAPI updater methods)
// ---------------------------------------------------------------------------

// EventUpdateAvailable is emitted when CheckForUpdates finds a newer release.
// The payload is the UpdateInfo DTO.
const EventUpdateAvailable = "update:available"

// EventUpdateProgress is emitted periodically during DownloadUpdate, carrying
// the bytes downloaded so far and the total size as an UpdateProgress DTO.
const EventUpdateProgress = "update:progress"

// EventUpdateDownloaded is emitted when an update archive has been downloaded
// and integrity-verified, ready to be applied.
const EventUpdateDownloaded = "update:downloaded"

// EventUpdateError is emitted when any update step fails. The payload is a
// map[string]string carrying a human-readable "message".
const EventUpdateError = "update:error"

// EventUpdateNone is emitted when CheckForUpdates finds no newer release (the
// running version is current or the latest was skipped).
const EventUpdateNone = "update:none"

// ---------------------------------------------------------------------------
// Embedded local-model events (emitted by FrontendAPI embedded-LLM methods)
// ---------------------------------------------------------------------------

// EventEmbeddedLLMInstallProgress reports one per-component install step. A
// global event with a bare name: the install belongs to the machine, not to a
// conversation.
const EventEmbeddedLLMInstallProgress = "embedded_llm:install_progress"

// EventEmbeddedLLMState reports the supervision state of the embedded local
// model plus the install record. It fires on every observable transition,
// once at startup after the manifest restore, and after an install, a removal
// or an auto-unload policy change; a background failure additionally raises
// EventRuntimeError (no RPC is left to carry it).
const EventEmbeddedLLMState = "embedded_llm:state"

// EventChatGPTAuthState reports a transition of the ChatGPT sign-in flow. A
// global event with a bare name: the account belongs to the app, not to a
// conversation. The synchronous sign-out does not emit — its RPC return is
// the report.
const EventChatGPTAuthState = "chatgpt_auth:state"

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

// EventGoalProposalResponse is received from the frontend when the user
// confirms (optionally with edits), asks for clarification, or cancels a
// proposed goal awaiting sign-off.
const EventGoalProposalResponse = "goal_proposal_response"

// EventGoalSuggestionResponse is received from the frontend when the user
// answers a pending goal-mode suggestion card ("accept" or "decline").
const EventGoalSuggestionResponse = "goal_suggestion_response"
