// Package session provides session management for multiple agent sessions.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/markitdown"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// contextKey is a type for context keys in the session package.
type contextKey string

// SessionIDKey is the context key for the session ID.
const SessionIDKey contextKey = "session_id"

// ContextWithSessionID returns a new context with the session ID attached.
func ContextWithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, SessionIDKey, sessionID)
}

// SessionIDFromContext returns the session ID from the context, or an empty string if not found.
func SessionIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(SessionIDKey).(string); ok {
		return id
	}
	return ""
}

// Session represents a running agent session with its own orchestrator.
type Session struct {
	ID                  string
	ProjectID           string // immutable after creation (no lock needed for reads)
	Name                string
	CreatedAt           time.Time
	LastActiveAt        time.Time
	Archived            bool
	Pinned              bool
	WorkspacePath       string // workspace directory (from project)
	TempDir             string // session-specific temp directory
	orchestrator        *core.Orchestrator
	logFile             *os.File           // session log file handle, closed on deletion
	dumpFile            *os.File           // LLM dump file handle (DEBUG mode only), closed on deletion
	cancel              context.CancelFunc // cancel for current task
	active              bool               // is currently processing
	done                chan struct{}      // closed when task goroutine finishes
	lastCompletedTaskID string             // tracks last completed task for continuations
	mu                  sync.Mutex
	pendingAttachments  []orchestration.Attachment // user-attached files staged via AttachFiles, flushed into the blackboard on the next SendMessage (guarded by mu)
}

// sessionTempDir returns the temp directory path for a session.
func sessionTempDir(agentDir, projectID, sessionID string) string {
	return config.SessionTempDir(agentDir, projectID, sessionID)
}

// OrchestratorFactory creates a new Orchestrator with the given emitter, logger, workspace path,
// and optional BlackboardFactory.
// The workspace path is the project workspace directory so the worktree factory can
// capture the correct project workspace.
// bbFactory may be nil, in which case the orchestrator uses an in-memory MapBlackboard.
// Returns an error if the orchestrator cannot be created.
type OrchestratorFactory func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer, stepDumpTracker *orchestration.StepDumpTracker) (*core.Orchestrator, error)

// TokenPersistFunc is called with cumulative session token totals after each LLM call.
// The sessionID parameter identifies which session the tokens belong to.
// fillPercent is the conductor's context-window fill percent (0-100).
type TokenPersistFunc func(sessionID string, inputTokens, outputTokens int, model, family string, fillPercent float64)

// ProjectResolverFunc resolves a project ID to its workspace directory path.
type ProjectResolverFunc func(projectID string) (workspacePath string, err error)

// toolCallIDEntry records the most recent tool_call_id emitted for a session,
// alongside its tool name so the confirmation callback can sanity-check the
// match (the last tool_call's name must equal the confirmed tool's name).
type toolCallIDEntry struct {
	id   string
	tool string
}

// Manager manages multiple agent sessions.
type Manager struct {
	sessions map[string]*Session
	// restoreInFlight deduplicates concurrent lazy restores of the same session
	// ID (single-flight). Keyed by session ID; value is a channel closed when
	// the in-progress restore finishes. Guarded by mu. Without this, many
	// goroutines racing to restore the same session each open the log/dump
	// files independently; on Windows the resulting concurrent open/close
	// churn leaves the OS file lock briefly held even after every handle is
	// closed, breaking TempDir cleanup.
	restoreInFlight     map[string]chan struct{}
	mu                  sync.RWMutex
	orchestratorFactory OrchestratorFactory
	emitFunc            func(Event) // shared event emission callback
	agentDir            string      // base agent directory (~/.c0wrk)
	logLevel            string      // current log level for session loggers
	tokenPersist        TokenPersistFunc
	taskStore           TaskStore            // optional persistent task store
	sessionStore        SessionStore         // optional persistent session store
	projectStore        project.ProjectStore // optional persistent project store (project-scoped work dirs)
	titleGen            *TitleGenerator      // optional title generator for auto-naming
	envInfo             *sdktools.EnvInfo    // environment info for context injection
	envInfoDone         chan struct{}        // closed when the background env-info collection finishes (WriteOnce)
	envInfoOnce         sync.Once            // guards StartEnvInfoCollection against double-launch
	stopTimeout         time.Duration        // how long to wait for goroutine on cancel/delete
	maxSummaryLen       int                  // character limit for auto-generated step summaries
	projectResolver     ProjectResolverFunc  // resolves projectID -> workspacePath for lazy session restoration
	fileTracker         *FileCoherenceTracker
	converter           *markitdown.Converter // lazy-init markitdown converter for AttachFiles
	converterMu         sync.Mutex            // guards lazy converter initialization

	// ignoreCache caches per-root ignore.Resolver instances so the directory
	// tree is walked only once per root (not on every SendMessage). The key
	// is the symlink-resolved absolute root path. Resolvers are immutable
	// after construction so they are safe for concurrent use once cached.
	ignoreCache sync.Map // string (resolved root) → *ignore.Resolver

	// shuttingDown is set to true at the very start of Shutdown() so that the
	// SendMessage/Resume goroutines, when they observe their context cancelled,
	// can distinguish an app shutdown from a user-initiated cancellation. During
	// shutdown the in-progress task is left untouched (stays resumable after
	// restart) instead of being marked cancelled.
	shuttingDown atomic.Bool

	// lastToolCallIDs maps sessionID → the most recently emitted tool_call_id
	// (plus its tool name) for that session. The emitter's ToolCall sink writes
	// here; the desktop confirmation callback reads here to attach the matching
	// tool_call_id to the tool_confirm payload. Entries are overwritten on every
	// ToolCall — only the latest is needed because tool confirmation fires
	// sequentially, right after the triggering ToolCall, in the same goroutine.
	lastToolCallIDs sync.Map // sessionID → toolCallIDEntry

	// goalProposalResolver delivers a user decision to a blocked goal-proposal
	// channel held by the desktop layer. It is set by desktop after
	// buildGoalProposalCallback registers its pending map, so that BOTH the
	// event-based path (handleGoalProposalResponse) and the RPC-based path
	// (FrontendAPI.ConfirmGoal/CancelGoal) funnel through a single resolution.
	// Nil (before desktop wiring) makes ResolveGoalProposal a no-op.
	goalProposalResolver func(requestID, decision, condition, verify, verificationMode, clarification string) bool

	logger *slog.Logger
}

// SetLogger sets the logger for the manager.
func (m *Manager) SetLogger(l *slog.Logger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logger = l
}

// log returns the manager's logger, falling back to slog.Default().
func (m *Manager) log() *slog.Logger {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.logger != nil {
		return m.logger
	}
	return slog.Default()
}

// NewManager creates a new session Manager.
func NewManager(factory OrchestratorFactory, emitFunc func(Event), agentDir string) *Manager {
	m := &Manager{
		sessions:            make(map[string]*Session),
		restoreInFlight:     make(map[string]chan struct{}),
		orchestratorFactory: factory,
		emitFunc:            emitFunc,
		agentDir:            agentDir,
		logLevel:            "DEBUG",
		stopTimeout:         10 * time.Second,
		envInfoDone:         make(chan struct{}),
	}
	m.fileTracker = NewFileCoherenceTracker(m.resolveSessionName)
	return m
}

// resolveSessionName returns a display name for the given session ID.
func (m *Manager) resolveSessionName(id string) string {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return safeSessionPrefix(id)
	}
	s.mu.Lock()
	name := s.Name
	s.mu.Unlock()
	return name
}

// safeSessionPrefix returns the first 8 characters of id, or the full id if
// it is shorter than 8 characters. Guards against slicing beyond length.
func safeSessionPrefix(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// SetFactory replaces the orchestrator factory used for new sessions.
// Existing sessions are not affected.
func (m *Manager) SetFactory(factory OrchestratorFactory) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orchestratorFactory = factory
}

// SetTokenPersist sets the callback used to persist cumulative session token totals.
func (m *Manager) SetTokenPersist(fn TokenPersistFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokenPersist = fn
}

// SetTaskStore sets the TaskStore used to persist orchestration tasks.
// When set, CreateSession will construct a BlackboardFactory that creates
// PersistentBlackboard instances backed by this store.
func (m *Manager) SetTaskStore(store TaskStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskStore = store
}

// SetEnvInfo sets the environment info that will be injected into task contexts.
func (m *Manager) SetEnvInfo(info *sdktools.EnvInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.envInfo = info
}

// StartEnvInfoCollection launches environment-info collection in a background
// goroutine and stores the result via SetEnvInfo when ready. It is safe to call
// SendMessage/ResumeTask concurrently — they tolerate a nil envInfo until the
// collection completes. WaitEnvInfo allows callers (notably tests) to block
// until the result is available.
//
// Calling this method more than once is a no-op: the underlying goroutine is
// launched exactly once (guarded by envInfoOnce), so envInfoDone is closed a
// single time and never panics on a double close.
func (m *Manager) StartEnvInfoCollection() {
	m.envInfoOnce.Do(func() {
		go func() {
			defer close(m.envInfoDone)
			m.SetEnvInfo(sdktools.CollectEnvInfo())
		}()
	})
}

// WaitEnvInfo blocks until the background environment-info collection finishes
// (or ctx is cancelled). It returns nil once envInfo is ready, or ctx.Err() if
// the context expires first. Because StartEnvInfoCollection closes envInfoDone
// via defer (even on panic), WaitEnvInfo never blocks forever after collection
// has been started.
func (m *Manager) WaitEnvInfo(ctx context.Context) error {
	select {
	case <-m.envInfoDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetMaxSummaryLen sets the character limit for auto-generated step summaries.
func (m *Manager) SetMaxSummaryLen(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxSummaryLen = n
}

// SetTitleGenerator sets the title generator for auto-naming sessions.
func (m *Manager) SetTitleGenerator(gen *TitleGenerator) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.titleGen = gen
}

// SetSessionStore sets the persistent session store.
func (m *Manager) SetSessionStore(store SessionStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionStore = store
}

// SetProjectStore sets the persistent project store, used to load project-scoped
// auxiliary work directories into each task context.
func (m *Manager) SetProjectStore(store project.ProjectStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projectStore = store
}

// SetProjectResolver sets the function used to resolve a project ID to its
// workspace path. This is required for lazy session restoration from the database.
func (m *Manager) SetProjectResolver(fn ProjectResolverFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projectResolver = fn
}

// getOrRestoreSession looks up a session in the in-memory map. If not found and
// a session store + project resolver are configured, it lazily restores the
// session from the database, creating a fully-functional Session object.
// Returns (nil, nil) when the session genuinely does not exist.
func (m *Manager) getOrRestoreSession(id string) (*Session, error) {
	// Fast path: check in-memory map.
	m.mu.RLock()
	if sess, ok := m.sessions[id]; ok {
		m.mu.RUnlock()
		return sess, nil
	}
	store := m.sessionStore
	resolver := m.projectResolver
	m.mu.RUnlock()

	if store == nil {
		m.log().Warn("session restoration skipped: session store not configured", "session_id", id)
		return nil, nil
	}
	if resolver == nil {
		m.log().Warn("session restoration skipped: project resolver not configured", "session_id", id)
		return nil, nil
	}

	// Load session metadata from the persistent store.
	info, err := store.LoadSession(context.Background(), id)
	if err != nil {
		return nil, fmt.Errorf("failed to load session from store: %w", err)
	}
	if info == nil {
		return nil, nil // session does not exist in DB either
	}

	// Resolve workspace path for the session's project.
	workspacePath, err := resolver(info.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workspace for project %s: %w", info.ProjectID, err)
	}

	// For No Project, each session gets its own isolated workspace.
	// The resolver above returns the project-level workspace which is
	// shared, but No Project sessions must use per-session workspaces
	// (the same logic as CreateSession). Re-derive it here so that
	// lazily restored sessions use the correct directory.
	if info.ProjectID == project.NoProjectID {
		workspacePath = config.NoProjectSessionWorkspace(m.agentDir, id)
		// Ensure the path is always absolute so tools and prompts receive
		// a stable, fully qualified workspace directory.
		if absPath, absErr := filepath.Abs(workspacePath); absErr == nil {
			workspacePath = absPath
		} else {
			m.log().Warn("failed to resolve absolute workspace path for restored session",
				"session_id", id, "path", workspacePath, "error", absErr)
		}
		if mkErr := os.MkdirAll(workspacePath, 0o755); mkErr != nil {
			m.log().Warn("failed to recreate per-session workspace on restore", "session_id", id, "error", mkErr)
		}
	}

	// Single-flight reservation: if another goroutine is already restoring
	// this same session, wait for it instead of opening duplicate log/dump
	// files. On Windows, many concurrent open/close cycles on the same file
	// cause "process cannot access the file" during TempDir cleanup even when
	// every handle is closed — CloseHandle returns before the OS file lock is
	// actually released (compounded by Defender real-time scanning on CI
	// runners). Serializing restores so only one goroutine ever opens the
	// files eliminates the contention at the source.
	m.mu.Lock()
	if existing, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		return existing, nil
	}
	if waitCh, inflight := m.restoreInFlight[id]; inflight {
		m.mu.Unlock()
		<-waitCh
		m.mu.RLock()
		sess := m.sessions[id]
		m.mu.RUnlock()
		if sess != nil {
			return sess, nil
		}
		// The restorer failed; the session is not restorable right now.
		return nil, nil
	}
	waitCh := make(chan struct{})
	m.restoreInFlight[id] = waitCh
	m.mu.Unlock()

	// finishRestore releases the reservation and wakes any waiters. It MUST be
	// invoked on every return path below this point (success and error),
	// otherwise concurrent and subsequent restores of the same session would
	// block forever.
	finishRestore := func() {
		m.mu.Lock()
		delete(m.restoreInFlight, id)
		m.mu.Unlock()
		close(waitCh)
	}

	// Create session logger.
	logger, logFile, err := m.createSessionLogger(info.ProjectID, id)
	if err != nil {
		finishRestore()
		return nil, fmt.Errorf("failed to create session logger: %w", err)
	}

	// Create event emitter for the session.
	emitter := NewEventEmitter(id, m.emitFunc)
	// Record each emitted tool_call_id so the desktop confirmation callback can
	// attach the matching id to the tool_confirm payload.
	emitter.SetToolCallIDSink(func(tool, toolCallID string) {
		m.lastToolCallIDs.Store(id, toolCallIDEntry{id: toolCallID, tool: tool})
	})

	// Snapshot mutable fields under read lock.
	m.mu.RLock()
	factory := m.orchestratorFactory
	persistFn := m.tokenPersist
	ts := m.taskStore
	maxSumLen := m.maxSummaryLen
	m.mu.RUnlock()

	// Wire token persistence callback if configured.
	if persistFn != nil {
		emitter.SetTokenPersist(func(inputTokens, outputTokens int, model, family string, fillPercent float64) {
			persistFn(id, inputTokens, outputTokens, model, family, fillPercent)
		})
	}

	// Build BlackboardFactory if task persistence is configured.
	var bbFactory core.BlackboardFactory
	var adapter *TaskStoreAdapter
	if ts != nil {
		adapter = NewTaskStoreAdapter(ts)
		sessionID := id // capture for closure
		emitFunc := m.emitFunc
		bbFactory = func(taskID string) orchestration.Blackboard {
			var pbb *PersistentBlackboard
			if maxSumLen > 0 {
				pbb = NewPersistentBlackboard(taskID, sessionID, adapter, logger, orchestration.WithMaxSummaryLen(maxSumLen))
			} else {
				pbb = NewPersistentBlackboard(taskID, sessionID, adapter, logger)
			}
			pbb.SetOnChanged(func(changeType string) {
				emitFunc(Event{
					SessionID: sessionID,
					Type:      "blackboard_updated",
					Data:      map[string]any{"change_type": changeType},
				})
			})
			return pbb
		}
	}

	// Create LLM dump file when DEBUG logging is enabled.
	var dumpFile *os.File
	var stepDumpTracker *orchestration.StepDumpTracker
	if strings.EqualFold(m.logLevel, "DEBUG") {
		dumpPath := config.SessionDumpPath(m.agentDir, info.ProjectID, id)
		if mkErr := os.MkdirAll(filepath.Dir(dumpPath), 0o755); mkErr != nil {
			m.log().Warn("failed to create dumps directory", "session_id", id, "error", mkErr)
		} else {
			dumpFile, err = os.OpenFile(dumpPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				m.log().Warn("failed to create LLM dump file", "session_id", id, "error", err)
				dumpFile = nil
			}
		}
		// Per-step dump tracker uses a "steps" subdirectory
		if dumpFile != nil {
			stepDumpDir := config.SessionStepDumpDir(m.agentDir, info.ProjectID, id)
			stepDumpTracker = orchestration.NewStepDumpTracker(stepDumpDir)
		}
	}

	// Create orchestrator.
	orchestrator, err := factory(emitter, logger, workspacePath, bbFactory, dumpFile, stepDumpTracker)
	if err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		if dumpFile != nil {
			_ = dumpFile.Close()
		}
		if stepDumpTracker != nil {
			_ = stepDumpTracker.CloseAll()
		}
		finishRestore()
		return nil, fmt.Errorf("failed to create orchestrator for restored session: %w", err)
	}

	// Configure No Project mode: disable code tools + extended bash blacklist.
	if info.ProjectID == project.NoProjectID {
		orchestrator.SetNoProjectMode()
	}

	// Wire task persistence into orchestrator.
	if adapter != nil {
		orchestrator.SetTaskStore(adapter)
		emitFn := m.emitFunc
		capturedSessionID := id
		orchestrator.SetBlackboardRestoreFunc(func(taskID, sessionID string, store core.TaskPersistence, logger *slog.Logger, opts ...orchestration.MapBlackboardOption) (core.PersistableBlackboard, error) {
			pbb, err := RestoreBlackboard(taskID, sessionID, store, logger, opts...)
			if pbb != nil {
				pbb.SetOnChanged(func(changeType string) {
					emitFn(Event{
						SessionID: capturedSessionID,
						Type:      "blackboard_updated",
						Data:      map[string]any{"change_type": changeType},
					})
				})
			}
			return pbb, err
		})
	}

	// Restore full conversation history from persistent storage so the planner
	// sees all previous messages across backend restarts.
	if m.sessionStore != nil {
		storedMsgs, loadErr := m.sessionStore.LoadMessages(context.Background(), id)
		if loadErr != nil {
			m.log().Warn("failed to load session messages for history restore", "session_id", id, "error", loadErr)
		} else {
			history := m.convertChatMessagesToLLM(storedMsgs, workspacePath)
			if len(history) > 0 {
				orchestrator.SetConversationHistory(history)
				m.log().Debug("restored conversation history from store", "session_id", id, "messages", len(history))
			}
		}
	}

	// Restore the continuation anchor from the task store so the next user
	// message continues the previous task via PlanContinuation (which receives
	// the conversation history) instead of planning from scratch. Mirrors the
	// in-memory behavior where lastCompletedTaskID survives between messages.
	var restoredTaskID string
	if ts != nil {
		latestTaskID, taskErr := ts.GetLatestTaskID(context.Background(), id)
		switch {
		case taskErr != nil:
			m.log().Warn("failed to restore last task ID for session", "session_id", id, "error", taskErr)
		case latestTaskID != "":
			restoredTaskID = latestTaskID
			m.log().Debug("restored last task ID from store", "session_id", id, "task_id", latestTaskID)
		}
	}

	// Parse creation time from stored info.
	createdAt, parseErr := time.Parse(time.RFC3339, info.CreatedAt)
	if parseErr != nil {
		createdAt = time.Now()
	}

	// Create session temp directory.
	tempDir := sessionTempDir(m.agentDir, info.ProjectID, id)
	if mkErr := os.MkdirAll(tempDir, 0o755); mkErr != nil {
		m.log().Warn("failed to create session temp directory", "session_id", id, "temp_dir", tempDir, "error", mkErr)
	}

	sess := &Session{
		ID:                  id,
		ProjectID:           info.ProjectID,
		Name:                info.Name,
		CreatedAt:           createdAt,
		Archived:            info.Archived,
		Pinned:              info.Pinned,
		WorkspacePath:       workspacePath,
		TempDir:             tempDir,
		orchestrator:        orchestrator,
		logFile:             logFile,
		dumpFile:            dumpFile,
		active:              false,
		lastCompletedTaskID: restoredTaskID,
	}

	// Double-check under write lock: in normal operation (single-flight
	// reservation above) this is unreachable for concurrent restores of the
	// same ID, but it guards against the rare case where the session was
	// inserted by a code path that bypassed the reservation.
	m.mu.Lock()
	if existing, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		// Clean up the duplicate we just created.
		if logFile != nil {
			_ = logFile.Close()
		}
		if dumpFile != nil {
			_ = dumpFile.Close()
		}
		if stepDumpTracker != nil {
			_ = stepDumpTracker.CloseAll()
		}
		finishRestore()
		return existing, nil
	}
	m.sessions[id] = sess
	m.mu.Unlock()

	finishRestore()
	m.log().Info("restored session from database", "session_id", id, "project_id", info.ProjectID)
	return sess, nil
}

// ListSessionsByProject returns sessions for a project, merging in-memory active
// state with persistent store data. Falls back to in-memory sessions if no store.
func (m *Manager) ListSessionsByProject(projectID string) ([]SessionInfo, error) {
	m.mu.RLock()
	store := m.sessionStore
	m.mu.RUnlock()

	if store == nil {
		// Fallback: filter in-memory sessions by project
		all := m.ListSessions()
		result := make([]SessionInfo, 0)
		for _, s := range all {
			if s.ProjectID == projectID {
				result = append(result, s)
			}
		}
		return result, nil
	}

	sessions, err := store.ListSessionsByProject(context.Background(), projectID)
	if err != nil {
		return nil, err
	}

	// Overlay in-memory active state from live sessions.
	m.mu.RLock()
	for i := range sessions {
		if s, ok := m.sessions[sessions[i].ID]; ok {
			s.mu.Lock()
			sessions[i].Active = s.active
			sessions[i].Pinned = s.Pinned
			s.mu.Unlock()
		}
	}
	m.mu.RUnlock()

	return sessions, nil
}

// CreateSession creates a new session with a fresh orchestrator.
// The projectID ties the session to a project; workspacePath is the project's workspace directory.
func (m *Manager) CreateSession(projectID, workspacePath string) (*SessionInfo, error) {
	// Per-phase timing (DEBUG only) so session-creation latency can be
	// pinpointed in real-world environments where MCP gateways, large skill
	// directories, or slow filesystems add overhead not visible in unit tests.
	overallStart := time.Now()

	// Generate UUID for session ID
	id := uuid.New().String()

	var phaseT0, phaseT1 time.Time
	debugTiming := strings.EqualFold(m.logLevel, "DEBUG")
	phaseStart := func() {
		if debugTiming {
			phaseT0 = time.Now()
		}
	}
	phaseMark := func(name string) {
		if debugTiming {
			phaseT1 = time.Now()
			m.log().Debug("create_session phase", "phase", name, "elapsed_ms", phaseT1.Sub(phaseT0).Milliseconds(), "session_id", id)
			phaseT0 = phaseT1
		}
	}

	phaseStart()
	// For No Project, each session gets its own isolated workspace.
	if projectID == project.NoProjectID {
		workspacePath = config.NoProjectSessionWorkspace(m.agentDir, id)
		// Ensure the path is always absolute so tools and prompts receive
		// a stable, fully qualified workspace directory.
		if absPath, absErr := filepath.Abs(workspacePath); absErr == nil {
			workspacePath = absPath
		} else {
			m.log().Warn("failed to resolve absolute workspace path for new session",
				"session_id", id, "path", workspacePath, "error", absErr)
		}
		if err := os.MkdirAll(workspacePath, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create per-session workspace: %w", err)
		}
	}
	phaseMark("workspace_setup")

	// Create session-specific logger
	logger, logFile, err := m.createSessionLogger(projectID, id)
	if err != nil {
		return nil, fmt.Errorf("failed to create session logger: %w", err)
	}
	phaseMark("logger")

	// Create EventEmitter for this session
	emitter := NewEventEmitter(id, m.emitFunc)
	// Record each emitted tool_call_id so the desktop confirmation callback can
	// attach the matching id to the tool_confirm payload.
	emitter.SetToolCallIDSink(func(tool, toolCallID string) {
		m.lastToolCallIDs.Store(id, toolCallIDEntry{id: toolCallID, tool: tool})
	})

	// Snapshot mutable fields under read lock
	m.mu.RLock()
	factory := m.orchestratorFactory
	persistFn := m.tokenPersist
	ts := m.taskStore
	maxSumLen := m.maxSummaryLen
	m.mu.RUnlock()

	// Wire token persistence callback if configured
	if persistFn != nil {
		emitter.SetTokenPersist(func(inputTokens, outputTokens int, model, family string, fillPercent float64) {
			persistFn(id, inputTokens, outputTokens, model, family, fillPercent)
		})
	}

	// Build BlackboardFactory if task persistence is configured
	var bbFactory core.BlackboardFactory
	var adapter *TaskStoreAdapter
	if ts != nil {
		adapter = NewTaskStoreAdapter(ts)
		sessionID := id // capture for closure
		emitFunc := m.emitFunc
		bbFactory = func(taskID string) orchestration.Blackboard {
			var pbb *PersistentBlackboard
			if maxSumLen > 0 {
				pbb = NewPersistentBlackboard(taskID, sessionID, adapter, logger, orchestration.WithMaxSummaryLen(maxSumLen))
			} else {
				pbb = NewPersistentBlackboard(taskID, sessionID, adapter, logger)
			}
			pbb.SetOnChanged(func(changeType string) {
				emitFunc(Event{
					SessionID: sessionID,
					Type:      "blackboard_updated",
					Data:      map[string]any{"change_type": changeType},
				})
			})
			return pbb
		}
	}

	// Create LLM request/response dump file when DEBUG logging is enabled
	var dumpFile *os.File
	var stepDumpTracker *orchestration.StepDumpTracker
	if strings.EqualFold(m.logLevel, "DEBUG") {
		dumpPath := config.SessionDumpPath(m.agentDir, projectID, id)
		if mkErr := os.MkdirAll(filepath.Dir(dumpPath), 0o755); mkErr != nil {
			m.log().Warn("failed to create dumps directory", "session_id", id, "error", mkErr)
		} else {
			dumpFile, err = os.OpenFile(dumpPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				m.log().Warn("failed to create LLM dump file", "session_id", id, "error", err)
				dumpFile = nil // non-fatal, continue without dump
			}
		}
		// Per-step dump tracker uses a "steps" subdirectory
		if dumpFile != nil {
			stepDumpDir := config.SessionStepDumpDir(m.agentDir, projectID, id)
			stepDumpTracker = orchestration.NewStepDumpTracker(stepDumpDir)
		}
	}

	// Create orchestrator using the factory (called outside the lock — can be slow)
	phaseStart()
	orchestrator, err := factory(emitter, logger, workspacePath, bbFactory, dumpFile, stepDumpTracker)
	if err != nil {
		// Close the log file since we're not creating the session
		if logFile != nil {
			_ = logFile.Close()
		}
		if dumpFile != nil {
			_ = dumpFile.Close()
		}
		if stepDumpTracker != nil {
			_ = stepDumpTracker.CloseAll()
		}
		return nil, fmt.Errorf("failed to create orchestrator: %w", err)
	}
	phaseMark("orchestrator_build")

	// Configure No Project mode: disable code tools + extended bash blacklist.
	if projectID == project.NoProjectID {
		orchestrator.SetNoProjectMode()
	}

	// Wire task persistence into core orchestrator for continuations
	if adapter != nil {
		orchestrator.SetTaskStore(adapter)
		emitFn := m.emitFunc
		capturedSessionID := id
		orchestrator.SetBlackboardRestoreFunc(func(taskID, sessionID string, store core.TaskPersistence, logger *slog.Logger, opts ...orchestration.MapBlackboardOption) (core.PersistableBlackboard, error) {
			pbb, err := RestoreBlackboard(taskID, sessionID, store, logger, opts...)
			if pbb != nil {
				pbb.SetOnChanged(func(changeType string) {
					emitFn(Event{
						SessionID: capturedSessionID,
						Type:      "blackboard_updated",
						Data:      map[string]any{"change_type": changeType},
					})
				})
			}
			return pbb, err
		})
	}

	// Create session temp directory
	tempDir := sessionTempDir(m.agentDir, projectID, id)
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		m.log().Warn("failed to create session temp directory", "session_id", id, "temp_dir", tempDir, "error", err)
	}

	// Create session
	session := &Session{
		ID:            id,
		ProjectID:     projectID,
		Name:          "Session " + safeSessionPrefix(id), // Default name using first 8 chars of UUID
		CreatedAt:     time.Now(),
		Archived:      false,
		WorkspacePath: workspacePath,
		TempDir:       tempDir,
		orchestrator:  orchestrator,
		logFile:       logFile,
		dumpFile:      dumpFile,
		active:        false,
	}

	// Store session
	m.mu.Lock()
	m.sessions[id] = session
	m.mu.Unlock()

	// Emit session created event
	m.emitFunc(Event{
		SessionID: id,
		Type:      "session_created",
		Data: SessionCreatedData{
			ID:        id,
			Name:      session.Name,
			CreatedAt: session.CreatedAt,
		},
	})

	// Log total CreateSession wall-clock time (always at INFO so it shows up
	// without DEBUG). If >250ms, log at WARN so a regression in this
	// critical-path method is visible by default.
	totalElapsed := time.Since(overallStart)
	switch {
	case totalElapsed > time.Second:
		m.log().Warn("create_session slow (>1s)", "elapsed_ms", totalElapsed.Milliseconds(), "session_id", id, "project_id", projectID)
	case totalElapsed > 250*time.Millisecond:
		m.log().Warn("create_session slow (>250ms)", "elapsed_ms", totalElapsed.Milliseconds(), "session_id", id, "project_id", projectID)
	default:
		m.log().Debug("create_session complete", "elapsed_ms", totalElapsed.Milliseconds(), "session_id", id, "project_id", projectID)
	}

	return &SessionInfo{
		ID:           session.ID,
		ProjectID:    projectID,
		Name:         session.Name,
		CreatedAt:    session.CreatedAt.Format(time.RFC3339),
		LastActiveAt: session.CreatedAt.Format(time.RFC3339),
		Archived:     session.Archived,
		Pinned:       session.Pinned,
		Active:       false,
	}, nil
}

// createSessionLogger creates a logger for a specific session.
// Returns the logger, the file handle (for cleanup), and an error.
func (m *Manager) createSessionLogger(projectID, sessionID string) (*slog.Logger, *os.File, error) {
	logDir := config.SessionLogsDir(m.agentDir, projectID, sessionID)
	// Create log directory if it doesn't exist
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// Create log file for this session
	logFile := config.SessionLogPath(m.agentDir, projectID, sessionID)
	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open log file: %w", err)
	}

	handler := slog.NewJSONHandler(file, &slog.HandlerOptions{
		Level: parseSlogLevel(m.logLevel),
	})
	return slog.New(handler), file, nil
}

// parseSlogLevel converts a string log level to slog.Level.
func parseSlogLevel(level string) slog.Level {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// SetLogLevel sets the log level for new session loggers.
func (m *Manager) SetLogLevel(level string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logLevel = level
}

// DeleteSession removes a session, cancelling any active task.
func (m *Manager) DeleteSession(id string) error {
	// Try lazy restoration before checking the map.
	if _, restoreErr := m.getOrRestoreSession(id); restoreErr != nil {
		m.log().Warn("failed to restore session for deletion", "session_id", id, "error", restoreErr)
	}

	m.mu.Lock()
	session, exists := m.sessions[id]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("session not found: %s", id)
	}

	// Cancel any active task and grab the done channel for waiting.
	session.mu.Lock()
	var doneCh chan struct{}
	if session.active && session.cancel != nil {
		session.cancel()
		doneCh = session.done
	}
	session.mu.Unlock()
	m.mu.Unlock()

	// Wait for the task goroutine to finish so events are fully flushed.
	if doneCh != nil {
		select {
		case <-doneCh:
		case <-time.After(m.stopTimeout):
			m.log().Warn("timed out waiting for task goroutine to stop", "session_id", id)
		}
	}

	// Now safely remove the session from the map.
	m.mu.Lock()
	session.mu.Lock()
	// Close per-step dump files via orchestrator cleanup (idempotent).
	if session.orchestrator != nil {
		session.orchestrator.Cleanup()
	}
	// Close log file if it exists
	if session.logFile != nil {
		if err := session.logFile.Close(); err != nil {
			m.log().Warn("failed to close session log file", "session_id", id, "error", err)
		}
	}
	if session.dumpFile != nil {
		if err := session.dumpFile.Close(); err != nil {
			m.log().Warn("failed to close session LLM dump file", "session_id", id, "error", err)
		}
	}
	session.mu.Unlock()
	delete(m.sessions, id)
	m.mu.Unlock()

	// Purge file coherence state for this session.
	m.fileTracker.PurgeSession(id)

	// Remove all internal files belonging to this session: logs, dumps, temp,
	// and plans. For No Project sessions the session directory also contains the
	// isolated per-session workspace, which is removed here too. Regular
	// projects share a project-scoped workspace (<projectDir>/Workspace) that
	// lives outside the session directory and is only removed when the project
	// itself is deleted. ArchiveSession deliberately keeps these files so an
	// archived session can be restored.
	sessionDir := config.SessionDir(m.agentDir, session.ProjectID, id)
	if err := os.RemoveAll(sessionDir); err != nil {
		m.log().Warn("failed to remove session directory", "session_id", id, "dir", sessionDir, "error", err)
	}

	// Emit session deleted event
	m.emitFunc(Event{
		SessionID: id,
		Type:      "session_deleted",
		Data: SessionDeletedData{
			ID: id,
		},
	})

	// Drop the per-session tool_call_id tracking entry.
	m.lastToolCallIDs.Delete(id)

	return nil
}

// LastToolCallID returns the most recently emitted tool_call_id for a session
// along with its tool name, or empty strings if none has been recorded. The
// desktop confirmation callback uses this to attach the matching tool_call_id
// to the tool_confirm payload so the frontend can correlate the confirmation
// with the exact tool_call event (rather than matching by tool name).
func (m *Manager) LastToolCallID(sessionID string) (id, tool string) {
	if v, ok := m.lastToolCallIDs.Load(sessionID); ok {
		if entry, ok := v.(toolCallIDEntry); ok {
			return entry.id, entry.tool
		}
	}
	return "", ""
}

// GetSession returns a session by ID.
// If the session is not in memory but exists in the persistent store,
// it is lazily restored.
func (m *Manager) GetSession(id string) (*Session, bool) {
	sess, err := m.getOrRestoreSession(id)
	if err != nil {
		m.log().Warn("failed to restore session", "session_id", id, "error", err)
		return nil, false
	}
	return sess, sess != nil
}

// GetSessionWorkspacePath returns the workspace path for a session.
func (m *Manager) GetSessionWorkspacePath(id string) (string, bool) {
	sess, ok := m.GetSession(id)
	if !ok {
		return "", false
	}
	return sess.WorkspacePath, true
}

// ListSessions returns metadata for all sessions, sorted by LastActiveAt descending.
func (m *Manager) ListSessions() []SessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		s.mu.Lock()
		lastActive := s.LastActiveAt
		if lastActive.IsZero() {
			lastActive = s.CreatedAt
		}
		sessions = append(sessions, SessionInfo{
			ID:           s.ID,
			ProjectID:    s.ProjectID,
			Name:         s.Name,
			CreatedAt:    s.CreatedAt.Format(time.RFC3339),
			LastActiveAt: lastActive.Format(time.RFC3339),
			Archived:     s.Archived,
			Pinned:       s.Pinned,
			Active:       s.active,
		})
		s.mu.Unlock()
	}

	// Sort by LastActiveAt descending (most recent first)
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastActiveAt > sessions[j].LastActiveAt
	})

	return sessions
}

// RenameSession changes a session's display name.
func (m *Manager) RenameSession(id, name string) error {
	session, err := m.getOrRestoreSession(id)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", id)
	}

	session.mu.Lock()
	oldName := session.Name
	session.Name = name
	session.mu.Unlock()

	// Emit session renamed event
	m.emitFunc(Event{
		SessionID: id,
		Type:      "session_renamed",
		Data: SessionRenamedData{
			ID:      id,
			OldName: oldName,
			NewName: name,
		},
	})

	return nil
}

// ArchiveSession toggles the archived flag.
func (m *Manager) ArchiveSession(id string) error {
	session, err := m.getOrRestoreSession(id)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", id)
	}

	session.mu.Lock()
	session.Archived = !session.Archived
	archived := session.Archived
	session.mu.Unlock()

	// If archiving, clean up temp directory
	if archived && session.TempDir != "" {
		if err := os.RemoveAll(session.TempDir); err != nil {
			m.log().Warn("failed to remove session temp directory on archive", "session_id", id, "temp_dir", session.TempDir, "error", err)
		}
	}

	// Emit session archived/unarchived event
	eventType := "session_unarchived"
	if archived {
		eventType = "session_archived"
	}
	m.emitFunc(Event{
		SessionID: id,
		Type:      eventType,
		Data: SessionArchivedData{
			ID:       id,
			Archived: archived,
		},
	})

	return nil
}

// PinSession toggles the pinned flag.
func (m *Manager) PinSession(id string) error {
	session, err := m.getOrRestoreSession(id)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", id)
	}

	session.mu.Lock()
	session.Pinned = !session.Pinned
	pinned := session.Pinned
	session.mu.Unlock()

	// Emit session pinned/unpinned event
	eventType := "session_unpinned"
	if pinned {
		eventType = "session_pinned"
	}
	m.emitFunc(Event{
		SessionID: id,
		Type:      eventType,
		Data: SessionPinnedData{
			ID:     id,
			Pinned: pinned,
		},
	})

	return nil
}

// emitResumableIfUnfinished, GetBlackboardState, and the BlackboardState
// helper type live in manager_execution.go. They share the *Manager and
// *Session types defined in this file (W-21 file split).

// GetOrchestrator returns the orchestrator for a session (for testing/advanced use).
func (s *Session) GetOrchestrator() *core.Orchestrator {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.orchestrator
}

// IsActive returns whether the session is currently processing a task.
func (s *Session) IsActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// DumpFile returns a duplicated file handle for the session's LLM dump file,
// or nil if DEBUG is disabled. The caller owns the returned handle and must
// close it when done. Duping ensures that background goroutines (title generation,
// ToolJudge) have independent handles that survive session deletion.
func (s *Session) DumpFile() *os.File {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dumpFile == nil {
		return nil
	}
	f, err := dupFile(s.dumpFile)
	if err != nil {
		return nil
	}
	return f
}

// EmitSessionEvent emits a session-scoped event through the manager's emit
// pipeline (event persister included). Used by recovery flows that need to
// emit events outside of a live session goroutine.
func (m *Manager) EmitSessionEvent(sessionID, eventType string, data any) {
	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      eventType,
		Data:      data,
	})
}

// Shutdown closes all sessions and releases resources.
// This should be called when the application is shutting down.
func (m *Manager) Shutdown() {
	// Signal that we are shutting down BEFORE cancelling any task. The
	// SendMessage/Resume goroutines check this flag when they observe their
	// context cancelled: on shutdown they leave the task in_progress (so it
	// can be resumed after restart) instead of marking it cancelled.
	m.shuttingDown.Store(true)

	// Collect sessions and done channels under lock.
	//
	// NOTE: every session must be added to pendingList, not just the active
	// ones. Idle sessions (session.done == nil) still hold open logFile and
	// dumpFile handles that must be closed before the process exits —
	// otherwise Windows cannot delete them and TempDir cleanup fails with
	// "process cannot access the file". The done channel is optional and only
	// waited on for sessions that had an in-flight task at shutdown time.
	type pending struct {
		session *Session
		doneCh  chan struct{}
	}
	m.mu.Lock()
	pendingList := make([]pending, 0, len(m.sessions))
	for id, session := range m.sessions {
		session.mu.Lock()
		// Close per-step dump files via orchestrator cleanup (idempotent).
		if session.orchestrator != nil {
			session.orchestrator.Cleanup()
		}
		// Cancel any active task
		if session.active && session.cancel != nil {
			session.cancel()
		}
		doneCh := session.done
		session.mu.Unlock()

		pendingList = append(pendingList, pending{session: session, doneCh: doneCh})

		// Remove from map
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	// Wait for active task goroutines to finish outside any lock.
	for _, p := range pendingList {
		if p.doneCh == nil {
			continue
		}
		select {
		case <-p.doneCh:
		case <-time.After(m.stopTimeout):
		}
	}

	// Close file handles for ALL sessions (idle and active) after any active
	// goroutines have stopped.
	for _, p := range pendingList {
		p.session.mu.Lock()
		if p.session.logFile != nil {
			_ = p.session.logFile.Close()
		}
		if p.session.dumpFile != nil {
			_ = p.session.dumpFile.Close()
		}
		p.session.mu.Unlock()
	}
}

// convertChatMessagesToLLM converts stored ChatMessages to llm.Message format,
// reconstructing the conversation history exactly as the router and planner
// saw it during the live session:
//
//   - "user" rows keep only user/assistant conversational content; the raw
//     text is normalized with the same preprocessing the orchestrator applied
//     live (@file → fileref:// URIs).
//   - Consecutive "assistant" rows are collapsed to the most recent one: the
//     store records every intermediate step output (assistant_done) plus the
//     final task output (task_complete), while the live history keeps only
//     the final output per exchange.
//   - "error" and "task_cancelled" rows are converted to the same assistant
//     notes that recordConversationOutcome appends live, so failed and
//     cancelled exchanges survive a restart identically.
//
// Other roles (tool calls, thoughts, status, etc.) are non-conversational and
// skipped.
func (m *Manager) convertChatMessagesToLLM(msgs []ChatMessage, workspacePath string) []llm.Message {
	result := make([]llm.Message, 0, len(msgs))
	appendAssistant := func(lm llm.Message) {
		if len(result) > 0 && result[len(result)-1].Role == "assistant" {
			result[len(result)-1] = lm
			return
		}
		result = append(result, lm)
	}
	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			// The store keeps the raw text (with /skill and @file markers)
			// for display; the live history stored the preprocessed form.
			// Skill names are unknown at restore time, so only the @file
			// normalization is applied. Relative @file paths are resolved
			// against the session workspace, mirroring the live preprocessing.
			result = append(result, llm.Message{Role: "user", Content: core.PreprocessMessageText(msg.Content, nil, workspacePath)})
		case "assistant":
			lm := llm.Message{Role: "assistant", Content: msg.Content}
			if msg.ReasoningContent != nil {
				lm.ReasoningContent = *msg.ReasoningContent
			}
			if msg.ToolCalls != nil {
				var toolCalls []llm.ToolCall
				if err := json.Unmarshal(*msg.ToolCalls, &toolCalls); err == nil {
					lm.ToolCalls = toolCalls
				}
			}
			appendAssistant(lm)
		case "error":
			appendAssistant(llm.Message{Role: "assistant", Content: core.HistoryNoteFailed(extractPersistedError(msg))})
		case "task_cancelled":
			appendAssistant(llm.Message{Role: "assistant", Content: core.HistoryNoteCancelled})
		default:
			m.log().Debug("convertChatMessagesToLLM: skipping non-conversational role", "role", msg.Role)
		}
	}
	return result
}

// extractPersistedError extracts the error text from a persisted "error" row.
// The event persister stores ErrorData as metadata JSON ({"error": "..."}).
func extractPersistedError(msg ChatMessage) string {
	var data struct {
		Error string `json:"error"`
	}
	if len(msg.Metadata) > 0 {
		if err := json.Unmarshal(msg.Metadata, &data); err == nil && data.Error != "" {
			return data.Error
		}
	}
	return "unknown error"
}
