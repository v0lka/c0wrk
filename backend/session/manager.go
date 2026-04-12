// Package session provides session management for multiple agent sessions.
package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/user/agent/core"
	"github.com/user/agent/core/tools"
	sdktools "github.com/user/agent/sdk/tools"
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
	ProjectID           string
	Name                string
	CreatedAt           time.Time
	Archived            bool
	WorkspacePath       string // workspace directory (from project)
	TempDir             string // session-specific temp directory
	orchestrator        *core.Orchestrator
	logFile             *os.File           // session log file handle, closed on deletion
	cancel              context.CancelFunc // cancel for current task
	active              bool               // is currently processing
	done                chan struct{}      // closed when task goroutine finishes
	lastCompletedTaskID string             // tracks last completed task for continuations
	mu                  sync.Mutex
}

// sessionTempDir returns the temp directory path for a session.
func sessionTempDir(projectsDir, projectID, sessionID string) string {
	return filepath.Join(projectsDir, projectID, "Temp", sessionID)
}

// OrchestratorFactory creates a new Orchestrator with the given emitter, logger, workspace path,
// and optional BlackboardFactory.
// The workspace path is the project workspace directory so the worktree factory can
// capture the correct project workspace.
// bbFactory may be nil, in which case the orchestrator uses an in-memory MapBlackboard.
// Returns an error if the orchestrator cannot be created.
type OrchestratorFactory func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory) (*core.Orchestrator, error)

// TokenPersistFunc is called with cumulative session token totals after each LLM call.
// The sessionID parameter identifies which session the tokens belong to.
type TokenPersistFunc func(sessionID string, inputTokens, outputTokens int)

// Manager manages multiple agent sessions.
type Manager struct {
	sessions            map[string]*Session
	mu                  sync.RWMutex
	orchestratorFactory OrchestratorFactory
	emitFunc            func(Event) // shared event emission callback
	logDir              string      // base directory for session logs
	logLevel            string      // current log level for session loggers
	projectsDir         string      // base directory for project temp dirs (~/.c0wrk/Projects)
	tokenPersist        TokenPersistFunc
	taskStore           TaskStore      // optional persistent task store
	envInfo             *tools.EnvInfo // environment info for context injection
	stopTimeout         time.Duration  // how long to wait for goroutine on cancel/delete
}

// NewManager creates a new session Manager.
func NewManager(factory OrchestratorFactory, emitFunc func(Event), logDir, projectsDir string) *Manager {
	return &Manager{
		sessions:            make(map[string]*Session),
		orchestratorFactory: factory,
		emitFunc:            emitFunc,
		logDir:              logDir,
		logLevel:            "DEBUG",
		projectsDir:         projectsDir,
		stopTimeout:         10 * time.Second,
	}
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
func (m *Manager) SetEnvInfo(info *tools.EnvInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.envInfo = info
}

// CreateSession creates a new session with a fresh orchestrator.
// The projectID ties the session to a project; workspacePath is the project's workspace directory.
func (m *Manager) CreateSession(projectID, workspacePath string) (*SessionInfo, error) {
	// Generate UUID for session ID
	id := uuid.New().String()

	// Create session-specific logger
	logger, logFile, err := m.createSessionLogger(id)
	if err != nil {
		return nil, fmt.Errorf("failed to create session logger: %w", err)
	}

	// Create EventEmitter for this session
	emitter := NewEventEmitter(id, m.emitFunc)

	// Wire token persistence callback if configured
	if m.tokenPersist != nil {
		persistFn := m.tokenPersist // capture for closure
		emitter.SetTokenPersist(func(inputTokens, outputTokens int) {
			persistFn(id, inputTokens, outputTokens)
		})
	}

	// Build BlackboardFactory if task persistence is configured
	var bbFactory core.BlackboardFactory
	var adapter *TaskStoreAdapter
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts != nil {
		adapter = NewTaskStoreAdapter(ts)
		sessionID := id // capture for closure
		bbFactory = func(taskID string) core.Blackboard {
			return core.NewPersistentBlackboard(taskID, sessionID, adapter, logger)
		}
	}

	// Create orchestrator using the factory
	orchestrator, err := m.orchestratorFactory(emitter, logger, workspacePath, bbFactory)
	if err != nil {
		// Close the log file since we're not creating the session
		if logFile != nil {
			_ = logFile.Close()
		}
		return nil, fmt.Errorf("failed to create orchestrator: %w", err)
	}

	// Wire task persistence into core orchestrator for continuations
	if adapter != nil {
		orchestrator.SetTaskStore(adapter)
	}

	// Create session temp directory
	tempDir := sessionTempDir(m.projectsDir, projectID, id)
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		slog.Warn("failed to create session temp directory", "session_id", id, "temp_dir", tempDir, "error", err)
	}

	// Create session
	session := &Session{
		ID:            id,
		ProjectID:     projectID,
		Name:          "Session " + id[:8], // Default name using first 8 chars of UUID
		CreatedAt:     time.Now(),
		Archived:      false,
		WorkspacePath: workspacePath,
		TempDir:       tempDir,
		orchestrator:  orchestrator,
		logFile:       logFile,
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

	return &SessionInfo{
		ID:           session.ID,
		ProjectID:    projectID,
		Name:         session.Name,
		CreatedAt:    session.CreatedAt.Format(time.RFC3339),
		LastActiveAt: session.CreatedAt.Format(time.RFC3339),
		Archived:     session.Archived,
		Active:       false,
	}, nil
}

// createSessionLogger creates a logger for a specific session.
// Returns the logger, the file handle (for cleanup), and an error.
func (m *Manager) createSessionLogger(sessionID string) (*slog.Logger, *os.File, error) {
	// Create log directory if it doesn't exist
	if err := os.MkdirAll(m.logDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// Create log file for this session
	logFile := filepath.Join(m.logDir, fmt.Sprintf("session_%s.log", sessionID))
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
			slog.Warn("timed out waiting for task goroutine to stop", "session_id", id)
		}
	}

	// Now safely remove the session from the map.
	m.mu.Lock()
	session.mu.Lock()
	// Close log file if it exists
	if session.logFile != nil {
		if err := session.logFile.Close(); err != nil {
			slog.Warn("failed to close session log file", "session_id", id, "error", err)
		}
	}
	session.mu.Unlock()
	delete(m.sessions, id)
	m.mu.Unlock()

	// Clean up temp directory
	if session.TempDir != "" {
		if err := os.RemoveAll(session.TempDir); err != nil {
			slog.Warn("failed to remove session temp directory", "session_id", id, "temp_dir", session.TempDir, "error", err)
		}
	}

	// Emit session deleted event
	m.emitFunc(Event{
		SessionID: id,
		Type:      "session_deleted",
		Data: SessionDeletedData{
			ID: id,
		},
	})

	return nil
}

// GetSession returns a session by ID.
func (m *Manager) GetSession(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, exists := m.sessions[id]
	return session, exists
}

// GetSessionWorkspacePath returns the workspace path for a session.
func (m *Manager) GetSessionWorkspacePath(id string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, exists := m.sessions[id]
	if !exists {
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
		sessions = append(sessions, SessionInfo{
			ID:           s.ID,
			ProjectID:    s.ProjectID,
			Name:         s.Name,
			CreatedAt:    s.CreatedAt.Format(time.RFC3339),
			LastActiveAt: s.CreatedAt.Format(time.RFC3339),
			Archived:     s.Archived,
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
	m.mu.RLock()
	session, exists := m.sessions[id]
	m.mu.RUnlock()

	if !exists {
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
	m.mu.RLock()
	session, exists := m.sessions[id]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("session not found: %s", id)
	}

	session.mu.Lock()
	session.Archived = !session.Archived
	archived := session.Archived
	session.mu.Unlock()

	// If archiving, clean up temp directory
	if archived && session.TempDir != "" {
		if err := os.RemoveAll(session.TempDir); err != nil {
			slog.Warn("failed to remove session temp directory on archive", "session_id", id, "temp_dir", session.TempDir, "error", err)
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

// SendMessage sends a user message to a session's orchestrator (async).
// Runs in a goroutine, results come via events.
// planFirst controls whether to use Plan&Execute mode (true) or ReAct mode (false).
func (m *Manager) SendMessage(ctx context.Context, id, text string, planFirst bool) error {
	m.mu.RLock()
	session, exists := m.sessions[id]
	if !exists {
		m.mu.RUnlock()
		return fmt.Errorf("session not found: %s", id)
	}

	m.mu.RUnlock()

	session.mu.Lock()
	// Check if already active (prevent double-send on the same session)
	if session.active {
		session.mu.Unlock()
		return errors.New("session is already processing a task")
	}

	// Set active and create cancellable context with session ID
	session.active = true
	doneCh := make(chan struct{})
	session.done = doneCh
	taskCtx, cancel := context.WithCancel(ContextWithSessionID(ctx, id))
	// Enrich context with session workspace path for tool security heuristics
	taskCtx = tools.WithWorkspacePath(taskCtx, session.WorkspacePath)
	taskCtx = sdktools.WithTempDir(taskCtx, session.TempDir)
	if m.envInfo != nil {
		taskCtx = tools.WithEnvInfo(taskCtx, m.envInfo)
	}
	session.cancel = cancel
	session.mu.Unlock()

	// Emit message received event
	m.emitFunc(Event{
		SessionID: id,
		Type:      "message_received",
		Data: MessageReceivedData{
			SessionID: id,
			Text:      text,
		},
	})

	// Launch goroutine to handle the message
	go func(ctx context.Context, msg string) {
		defer close(doneCh)
		defer func() {
			session.mu.Lock()
			session.active = false
			session.cancel = nil
			session.done = nil
			session.mu.Unlock()
		}()

			// Get last completed task ID for continuation
		session.mu.Lock()
		lastTaskID := session.lastCompletedTaskID
		session.mu.Unlock()

		result, err := session.orchestrator.HandleMessage(ctx, msg, id, core.HandleOptions{
			PlanFirst: planFirst,
			TaskID:    lastTaskID,
		})

		// Fallback: if continuation failed (restore error) and we had a TaskID, retry fresh
		if err != nil && lastTaskID != "" {
			slog.Warn("continuation failed, falling back to fresh workflow", "session_id", id, "task_id", lastTaskID, "error", err)
			session.mu.Lock()
			session.lastCompletedTaskID = "" // clear to avoid repeated failures
			session.mu.Unlock()
			result, err = session.orchestrator.HandleMessage(ctx, msg, id, core.HandleOptions{
				PlanFirst: planFirst,
				TaskID:    "",
			})
		}

		if err != nil {
			// Check if it was a cancellation
			if ctx.Err() == context.Canceled {
				m.emitFunc(Event{
					SessionID: id,
					Type:      "task_cancelled",
					Data: TaskCancelledData{
						SessionID: id,
					},
				})
				// Mark any in-progress task as completed so it's not left resumable.
				m.mu.RLock()
				ts := m.taskStore
				m.mu.RUnlock()
				if ts != nil {
					adapter := NewTaskStoreAdapter(ts)
					if tid, tErr := adapter.GetUnfinishedTaskID(id); tErr == nil && tid != "" {
						if pErr := adapter.PersistCompletion(tid, "", 0); pErr != nil {
							slog.Warn("failed to persist completion on session done", "task", tid, "error", pErr)
						}
					}
				}
				return
			}

			// Emit error event
			m.emitFunc(Event{
				SessionID: id,
				Type:      "error",
				Data: ErrorData{
					SessionID: id,
					Error:     err.Error(),
				},
			})
			m.emitResumableIfUnfinished(id)
			return
		}

		// Store the task ID for potential continuations
		if pbb, ok := result.Blackboard.(*core.PersistentBlackboard); ok {
			session.mu.Lock()
			session.lastCompletedTaskID = pbb.TaskID()
			session.mu.Unlock()
		}

		// Emit done event with result
		m.emitFunc(Event{
			SessionID: id,
			Type:      "task_complete",
			Data: TaskCompleteData{
				SessionID:       id,
				Output:          result.Output,
				RoutingDecision: result.RoutingDecision,
				Plan:            result.Plan,
				AttemptCount:    result.AttemptCount,
				Reflections:     result.Reflections,
			},
		})
		m.emitResumableIfUnfinished(id)
	}(taskCtx, text)

	return nil
}

// ResumeTask checks for an unfinished task in the given session and resumes it.
// Returns nil if no unfinished task exists or if the task store is not configured.
// This is called on app restart to resume interrupted tasks.
func (m *Manager) ResumeTask(ctx context.Context, id string) error {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()

	if ts == nil {
		return nil // no task persistence — nothing to resume
	}

	m.mu.RLock()
	session, exists := m.sessions[id]
	m.mu.RUnlock()
	if !exists {
		return fmt.Errorf("session not found: %s", id)
	}

	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(id)
	if err != nil {
		return fmt.Errorf("failed to check unfinished tasks: %w", err)
	}
	if taskID == "" {
		return nil // no unfinished task
	}

	// Load task state and restore blackboard.
	bb, err := core.RestoreBlackboard(taskID, id, adapter, nil)
	if err != nil {
		return fmt.Errorf("failed to restore blackboard: %w", err)
	}
	if bb == nil {
		return nil // task record not found (race condition or cleanup)
	}

	// Load routing decision from task state.
	state, err := adapter.LoadTaskState(taskID)
	if err != nil {
		return fmt.Errorf("failed to load task state: %w", err)
	}
	if state == nil || state.RoutingDecision == nil {
		return fmt.Errorf("cannot resume task %s: missing routing decision", taskID)
	}

	if bb.GetPlan() == nil {
		return fmt.Errorf("cannot resume task %s: no plan in restored state", taskID)
	}

	session.mu.Lock()
	if session.active {
		session.mu.Unlock()
		return errors.New("session is already processing a task")
	}
	session.active = true
	resumeDoneCh := make(chan struct{})
	session.done = resumeDoneCh
	taskCtx, cancel := context.WithCancel(ContextWithSessionID(ctx, id))
	taskCtx = tools.WithWorkspacePath(taskCtx, session.WorkspacePath)
	taskCtx = sdktools.WithTempDir(taskCtx, session.TempDir)
	if m.envInfo != nil {
		taskCtx = tools.WithEnvInfo(taskCtx, m.envInfo)
	}
	session.cancel = cancel
	session.mu.Unlock()

	// Emit resume event so the frontend knows a task is resuming.
	m.emitFunc(Event{
		SessionID: id,
		Type:      "task_resumed",
		Data: MessageReceivedData{
			SessionID: id,
			Text:      state.OriginalRequest,
		},
	})

	// Launch goroutine (same pattern as SendMessage).
	go func() {
		defer close(resumeDoneCh)
		defer func() {
			session.mu.Lock()
			session.active = false
			session.cancel = nil
			session.done = nil
			session.mu.Unlock()
		}()

		result, err := session.orchestrator.Resume(taskCtx, bb, state.RoutingDecision)

		if err != nil {
			if taskCtx.Err() == context.Canceled {
				m.emitFunc(Event{
					SessionID: id,
					Type:      "task_cancelled",
					Data: TaskCancelledData{
						SessionID: id,
					},
				})
				// Mark the restored task as completed so it's not left resumable.
				bb.CompleteTask(0)
				return
			}

			m.emitFunc(Event{
				SessionID: id,
				Type:      "error",
				Data: ErrorData{
					SessionID: id,
					Error:     err.Error(),
				},
			})
			m.emitResumableIfUnfinished(id)
			return
		}

		// Store the task ID for potential continuations
		if pbb, ok := result.Blackboard.(*core.PersistentBlackboard); ok {
			session.mu.Lock()
			session.lastCompletedTaskID = pbb.TaskID()
			session.mu.Unlock()
		}

		m.emitFunc(Event{
			SessionID: id,
			Type:      "task_complete",
			Data: TaskCompleteData{
				SessionID:       id,
				Output:          result.Output,
				RoutingDecision: result.RoutingDecision,
				Plan:            result.Plan,
				AttemptCount:    result.AttemptCount,
				Reflections:     result.Reflections,
			},
		})
		m.emitResumableIfUnfinished(id)
	}()

	return nil
}

// emitResumableIfUnfinished checks whether the session has an unfinished task
// in the task store and, if so, emits a "task_failed_resumable" event so the
// frontend can offer a Resume button.
func (m *Manager) emitResumableIfUnfinished(sessionID string) {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return
	}

	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		slog.Debug("failed to get unfinished task ID", "session", sessionID, "error", err)
	}
	if taskID == "" {
		return
	}

	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      "task_failed_resumable",
		Data: TaskFailedResumableData{
			Message: "Plan execution failed. You can resume to retry from where it left off.",
		},
	})
}

// CancelTask cancels the currently running task in a session.
// It signals cancellation and waits (with timeout) for the task goroutine to finish.
func (m *Manager) CancelTask(id string) error {
	m.mu.RLock()
	session, exists := m.sessions[id]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("session not found: %s", id)
	}

	session.mu.Lock()
	if !session.active {
		session.mu.Unlock()
		return errors.New("no active task to cancel")
	}

	doneCh := session.done
	if session.cancel != nil {
		session.cancel()
	}
	session.mu.Unlock()

	// Wait for the goroutine to finish so the task_cancelled event is emitted
	// before this method returns to the frontend.
	if doneCh != nil {
		select {
		case <-doneCh:
		case <-time.After(m.stopTimeout):
			slog.Warn("timed out waiting for task goroutine to stop on cancel", "session_id", id)
		}
	}

	return nil
}

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

// Shutdown closes all sessions and releases resources.
// This should be called when the application is shutting down.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, session := range m.sessions {
		session.mu.Lock()
		// Cancel any active task
		if session.active && session.cancel != nil {
			session.cancel()
		}
		// Close log file
		if session.logFile != nil {
			_ = session.logFile.Close()
		}
		session.mu.Unlock()

		// Remove from map
		delete(m.sessions, id)
	}
}
