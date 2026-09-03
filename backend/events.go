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

// EventConfigUpdated is emitted after a config mutation is persisted (any
// Update* RPC that writes config.yaml: LLM, search, proxy, security,
// Small-LLM, model overrides, MCP servers, log level, the experimental
// toggle, trusted git repos, update preferences). No payload — consumers
// re-read the config via GetConfig. Dispatched asynchronously from
// persistConfig (backend/frontend_api_config.go) so the Wails dispatch never
// runs under configMu. Its purpose is recovery for consumers whose initial
// GetConfig landed during the startup race or failed transiently: they stay
// "not latched" and retry on this event instead of waiting for a restart.
const EventConfigUpdated = "config:updated"

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

// EventGitConfigRisk is emitted when a project switch or an added auxiliary
// work directory opens a repository whose .git/config carries dangerous keys
// (command-bearing filters, merge drivers, textconv, fsmonitor, hooksPath,
// include directives, ...). The payload is a GitConfigRiskData listing every
// detected key with a human-readable description plus the standing notice
// that repository-defined hooks do not run inside c0wrk. A clean config emits
// nothing. Emitted from notifyGitConfigRisk
// (backend/frontend_api_gitconfig_risk.go).
const EventGitConfigRisk = "project:git_config_risk"

// EventWorkspaceTreeChanged is emitted when filesystem changes are detected
// in the active workspace.
const EventWorkspaceTreeChanged = "workspace:tree_changed"

// EventFilesDropped is emitted when one or more files are dragged and dropped
// onto the application window. The payload is a map carrying the absolute
// paths of the dropped files under the "paths" key. The webview's own
// drag-and-drop handling is disabled (options.DragAndDrop.DisableWebViewDrop)
// so dropped files never navigate or open inside the webview; this event is
// the sole delivery channel for the dropped paths.
const EventFilesDropped = "files:dropped"

// EventSkillsChanged is emitted when skill directories outside the workspace
// (e.g. ~/.agents/skills, ~/.c0wrk/.agents/skills) are modified. Workspace-
// local skill changes are surfaced via EventWorkspaceTreeChanged instead.
const EventSkillsChanged = "skills:changed"

// EventAgentsChanged is emitted when Subagent Profile directories outside the
// workspace (e.g. ~/.agents/agents, ~/.c0wrk/.agents/agents) are modified.
// Workspace-local agent changes are surfaced via EventWorkspaceTreeChanged
// instead. Mirrors EventSkillsChanged for AGENT.md discovery.
const EventAgentsChanged = "agents:changed"

// EventGitStatusChanged is emitted when the git staging area changes
// (stage, unstage, commit). The payload is the repository path so the
// frontend knows which project was affected.
const EventGitStatusChanged = "git:status_changed"

// EventWorkDirsChanged is emitted when an auxiliary working directory is
// added, updated, or removed (project- or session-scoped) so the frontend
// refreshes its directory list.
const EventWorkDirsChanged = "workdirs:changed"

// EventSessionsLoaded is emitted when all sessions have been loaded from disk.
const EventSessionsLoaded = "sessions:loaded"

// EventSessionEvent is the prefix for session-scoped orchestration events.
// Actual events use the pattern session:{id}:{type}.
const EventSessionEvent = "session:event"

// EventVectorIndexStatus is emitted when the vector index state or progress changes.
const EventVectorIndexStatus = "vector_index:status"

// EventMCPReady is emitted (once) when the MCP gateway startup goroutine
// finishes — whether servers connected successfully or failed. It lets the MCP
// settings dialog refresh its transient "Starting…" placeholder into the real
// per-server status without manual polling. Emitted from the desktop layer
// (startMCPReadyNotifier), which waits on the builder's WaitMCPStartup.
const EventMCPReady = "mcp:ready"

// EventResearchChanged is emitted when RESEARCH mode is enabled or disabled
// for a project (EnableResearch / DisableResearch). The payload is a
// map[string]string carrying the "project_id" and the "action" ("enabled" /
// "disabled") so the frontend can refresh the Research panel and project list.
const EventResearchChanged = "research:changed"

// EventResearchFileChanged is emitted when a file inside the research
// directory changes (hypothesis cards, brief, prior-art, graph, etc.).
// The payload is a map[string]string carrying the "project_id" and "paths"
// (comma-separated list of changed file paths) so the frontend can
// incrementally update the hypothesis graph without a full status refetch.
const EventResearchFileChanged = "research:file_changed"

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
