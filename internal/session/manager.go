// Package session provides session management for multiple agent sessions.
package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/user/agent/internal/core"
	"github.com/user/agent/internal/tools"
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
	Name          string
	CreatedAt     time.Time
	Archived      bool
	WorkspacePath string // session-specific workspace directory
	orchestrator  *core.Orchestrator
	logFile       *os.File           // session log file handle, closed on deletion
	cancel        context.CancelFunc // cancel for current task
	active        bool               // is currently processing
	mu            sync.Mutex
}

// OrchestratorFactory creates a new Orchestrator with the given emitter and logger.
// This allows the Manager to defer Orchestrator construction details to the caller.
// Returns an error if the orchestrator cannot be created.
type OrchestratorFactory func(emitter core.Emitter, logger *slog.Logger) (*core.Orchestrator, error)

// Manager manages multiple agent sessions.
type Manager struct {
	sessions            map[string]*Session
	mu                  sync.RWMutex
	orchestratorFactory OrchestratorFactory
	emitFunc            func(Event) // shared event emission callback
	logDir              string      // base directory for session logs
	logLevel            string      // current log level for session loggers
	workspacesDir       string      // base directory for session workspaces
}

// NewManager creates a new session Manager.
func NewManager(factory OrchestratorFactory, emitFunc func(Event), logDir, workspacesDir string) *Manager {
	return &Manager{
		sessions:            make(map[string]*Session),
		orchestratorFactory: factory,
		emitFunc:            emitFunc,
		logDir:              logDir,
		logLevel:            "DEBUG",
		workspacesDir:       workspacesDir,
	}
}

// CreateSession creates a new session with a fresh orchestrator.
func (m *Manager) CreateSession() (*SessionInfo, error) {
	// Generate UUID for session ID
	id := uuid.New().String()

	// Create session workspace directory
	workspacePath := filepath.Join(m.workspacesDir, id)
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create session workspace: %w", err)
	}

	// Create session-specific logger
	logger, logFile, err := m.createSessionLogger(id)
	if err != nil {
		return nil, fmt.Errorf("failed to create session logger: %w", err)
	}

	// Create EventEmitter for this session
	emitter := NewEventEmitter(id, m.emitFunc)

	// Create orchestrator using the factory
	orchestrator, err := m.orchestratorFactory(emitter, logger)
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
		Data: map[string]interface{}{
			"id":         id,
			"name":       session.Name,
			"created_at": session.CreatedAt,
		},
	})

	return &SessionInfo{
		ID:           session.ID,
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
		Data: map[string]string{
			"id": id,
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

// ListSessions returns metadata for all sessions.
func (m *Manager) ListSessions() []SessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		s.mu.Lock()
		sessions = append(sessions, SessionInfo{
			ID:           s.ID,
			Name:         s.Name,
			CreatedAt:    s.CreatedAt.Format(time.RFC3339),
			LastActiveAt: s.CreatedAt.Format(time.RFC3339),
			Archived:     s.Archived,
			Active:       s.active,
		})
		s.mu.Unlock()
	}
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
		Data: map[string]string{
			"id":       id,
			"old_name": oldName,
			"new_name": name,
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
		Data: map[string]interface{}{
			"id":       id,
			"archived": archived,
		},
	})

	return nil
}

// SendMessage sends a user message to a session's orchestrator (async).
// Runs in a goroutine, results come via events.
func (m *Manager) SendMessage(ctx context.Context, id, text string) error {
	m.mu.RLock()
	session, exists := m.sessions[id]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("session not found: %s", id)
	}

	session.mu.Lock()
	// Check if already active
	if session.active {
		session.mu.Unlock()
		return errors.New("session is already processing a task")
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
		Data: map[string]string{
			"session_id": id,
			"text":       text,
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
					Data: map[string]string{
						"session_id": id,
					},
				})
				return
			}

			// Emit error event
			m.emitFunc(Event{
				SessionID: id,
				Type:      "error",
				Data: map[string]string{
					"session_id": id,
					"error":      err.Error(),
				},
			})
			return
		}

		// Emit done event with result
		m.emitFunc(Event{
			SessionID: id,
			Type:      "task_complete",
			Data: map[string]interface{}{
				"session_id":       id,
				"output":           result.Output,
				"routing_decision": result.RoutingDecision,
				"plan":             result.Plan,
				"eval_result":      result.EvalResult,
				"attempt_count":    result.AttemptCount,
				"reflections":      result.Reflections,
				"escalated":        result.Escalated,
				"original_mode":    result.OriginalMode,
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
