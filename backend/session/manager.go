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
	ID            string
	ProjectID     string
	Name          string
	CreatedAt     time.Time
	Archived      bool
	WorkspacePath string // workspace directory (from project)
	orchestrator  *core.Orchestrator
	logFile       *os.File           // session log file handle, closed on deletion
	cancel        context.CancelFunc // cancel for current task
	active        bool               // is currently processing
	mu            sync.Mutex
}

// OrchestratorFactory creates a new Orchestrator with the given emitter, logger, and workspace path.
// The workspace path is the project workspace directory so the worktree factory can
// capture the correct project workspace.
// Returns an error if the orchestrator cannot be created.
type OrchestratorFactory func(emitter core.Emitter, logger *slog.Logger, workspacePath string) (*core.Orchestrator, error)

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
	tokenPersist        TokenPersistFunc
}

// NewManager creates a new session Manager.
func NewManager(factory OrchestratorFactory, emitFunc func(Event), logDir string) *Manager {
	return &Manager{
		sessions:            make(map[string]*Session),
		orchestratorFactory: factory,
		emitFunc:            emitFunc,
		logDir:              logDir,
		logLevel:            "DEBUG",
	}
}

// SetTokenPersist sets the callback used to persist cumulative session token totals.
func (m *Manager) SetTokenPersist(fn TokenPersistFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokenPersist = fn
}

// CreateSession creates a new session with a fresh orchestrator.
// The projectID ties the session to a project; workspacePath is the project's workspace directory.
func (m *Manager) CreateSession(projectID, workspacePath string) (*SessionInfo, error) {
	// Generate UUID for session ID
	id := uuid.New().String()

	// Check no other session in the same project is active
	m.mu.Lock()
	for _, s := range m.sessions {
		if s.ProjectID == projectID {
			s.mu.Lock()
			isActive := s.active
			s.mu.Unlock()
			if isActive {
				m.mu.Unlock()
				return nil, fmt.Errorf("another session is active in project %s", projectID)
			}
		}
	}
	m.mu.Unlock()

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

	// Create orchestrator using the factory
	orchestrator, err := m.orchestratorFactory(emitter, logger, workspacePath)
	if err != nil {
		// Close the log file since we're not creating the session
		if logFile != nil {
			_ = logFile.Close()
		}
		return nil, fmt.Errorf("failed to create orchestrator: %w", err)
	}

	// Create session
	session := &Session{
		ID:            id,
		ProjectID:     projectID,
		Name:          "Session " + id[:8], // Default name using first 8 chars of UUID
		CreatedAt:     time.Now(),
		Archived:      false,
		WorkspacePath: workspacePath,
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

	// Cancel any active task
	session.mu.Lock()
	if session.active && session.cancel != nil {
		session.cancel()
	}
	// Close log file if it exists
	if session.logFile != nil {
		_ = session.logFile.Close()
	}
	session.mu.Unlock()

	// Remove from map
	delete(m.sessions, id)
	m.mu.Unlock()

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
func (m *Manager) SendMessage(ctx context.Context, id, text string) error {
	m.mu.RLock()
	session, exists := m.sessions[id]
	if !exists {
		m.mu.RUnlock()
		return fmt.Errorf("session not found: %s", id)
	}

	// Snapshot other sessions in the same project under the manager lock
	// to avoid racing with CreateSession/DeleteSession/Shutdown.
	others := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.ProjectID == session.ProjectID && s.ID != id {
			others = append(others, s)
		}
	}
	m.mu.RUnlock()

	session.mu.Lock()
	// Check if already active
	if session.active {
		session.mu.Unlock()
		return errors.New("session is already processing a task")
	}

	// Check no other session in the same project is active
	for _, s := range others {
		s.mu.Lock()
		isActive := s.active
		s.mu.Unlock()
		if isActive {
			session.mu.Unlock()
			return fmt.Errorf("another session is active in project %s", session.ProjectID)
		}
	}

	// Set active and create cancellable context with session ID
	session.active = true
	taskCtx, cancel := context.WithCancel(ContextWithSessionID(ctx, id))
	// Enrich context with session workspace path for tool security heuristics
	taskCtx = tools.WithWorkspacePath(taskCtx, session.WorkspacePath)
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
	go func() {
		defer func() {
			session.mu.Lock()
			session.active = false
			session.cancel = nil
			session.mu.Unlock()
		}()

		// Call orchestrator
		result, err := session.orchestrator.Handle(taskCtx, text)

		if err != nil {
			// Check if it was a cancellation
			if taskCtx.Err() == context.Canceled {
				m.emitFunc(Event{
					SessionID: id,
					Type:      "task_cancelled",
					Data: TaskCancelledData{
						SessionID: id,
					},
				})
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
			return
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
				EvalResult:      result.EvalResult,
				AttemptCount:    result.AttemptCount,
				Reflections:     result.Reflections,
			},
		})
	}()

	return nil
}

// CancelTask cancels the currently running task in a session.
func (m *Manager) CancelTask(id string) error {
	m.mu.RLock()
	session, exists := m.sessions[id]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("session not found: %s", id)
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	if !session.active {
		return errors.New("no active task to cancel")
	}

	if session.cancel != nil {
		session.cancel()
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
