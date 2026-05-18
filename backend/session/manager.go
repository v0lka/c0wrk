// Package session provides session management for multiple agent sessions.
package session

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	ID                  string
	ProjectID           string
	Name                string
	CreatedAt           time.Time
	Archived            bool
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
type OrchestratorFactory func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer) (*core.Orchestrator, error)

// TokenPersistFunc is called with cumulative session token totals after each LLM call.
// The sessionID parameter identifies which session the tokens belong to.
type TokenPersistFunc func(sessionID string, inputTokens, outputTokens int, model, family string)

// ProjectResolverFunc resolves a project ID to its workspace directory path.
type ProjectResolverFunc func(projectID string) (workspacePath string, err error)

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
	taskStore           TaskStore           // optional persistent task store
	sessionStore        SessionStore        // optional persistent session store
	titleGen            *TitleGenerator     // optional title generator for auto-naming
	envInfo             *tools.EnvInfo      // environment info for context injection
	stopTimeout         time.Duration       // how long to wait for goroutine on cancel/delete
	maxSummaryLen       int                 // character limit for auto-generated step summaries
	projectResolver     ProjectResolverFunc // resolves projectID -> workspacePath for lazy session restoration
	fileTracker         *FileCoherenceTracker
	logger              *slog.Logger
}

// SetLogger sets the logger for the manager.
func (m *Manager) SetLogger(l *slog.Logger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logger = l
}

// log returns the manager's logger, falling back to slog.Default().
func (m *Manager) log() *slog.Logger {
	if m.logger != nil {
		return m.logger
	}
	return slog.Default()
}

// NewManager creates a new session Manager.
func NewManager(factory OrchestratorFactory, emitFunc func(Event), logDir, projectsDir string) *Manager {
	m := &Manager{
		sessions:            make(map[string]*Session),
		orchestratorFactory: factory,
		emitFunc:            emitFunc,
		logDir:              logDir,
		logLevel:            "DEBUG",
		projectsDir:         projectsDir,
		stopTimeout:         10 * time.Second,
	}
	m.fileTracker = NewFileCoherenceTracker(m.resolveSessionName)
	return m
}

// resolveSessionName returns a display name for the given session ID.
func (m *Manager) resolveSessionName(id string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.sessions[id]; ok {
		return s.Name
	}
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
func (m *Manager) SetEnvInfo(info *tools.EnvInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.envInfo = info
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

	// Create session logger.
	logger, logFile, err := m.createSessionLogger(id)
	if err != nil {
		return nil, fmt.Errorf("failed to create session logger: %w", err)
	}

	// Create event emitter for the session.
	emitter := NewEventEmitter(id, m.emitFunc)

	// Snapshot mutable fields under read lock.
	m.mu.RLock()
	factory := m.orchestratorFactory
	persistFn := m.tokenPersist
	ts := m.taskStore
	maxSumLen := m.maxSummaryLen
	m.mu.RUnlock()

	// Wire token persistence callback if configured.
	if persistFn != nil {
		emitter.SetTokenPersist(func(inputTokens, outputTokens int, model, family string) {
			persistFn(id, inputTokens, outputTokens, model, family)
		})
	}

	// Build BlackboardFactory if task persistence is configured.
	var bbFactory core.BlackboardFactory
	var adapter *TaskStoreAdapter
	if ts != nil {
		adapter = NewTaskStoreAdapter(ts)
		sessionID := id // capture for closure
		emitFunc := m.emitFunc
		bbFactory = func(taskID string) core.Blackboard {
			var pbb *PersistentBlackboard
			if maxSumLen > 0 {
				pbb = NewPersistentBlackboard(taskID, sessionID, adapter, logger, core.WithMaxSummaryLen(maxSumLen))
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
	if strings.EqualFold(m.logLevel, "DEBUG") {
		dumpPath := filepath.Join(m.logDir, fmt.Sprintf("session_%s_llm_dump.jsonl", id))
		dumpFile, err = os.OpenFile(dumpPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			m.log().Warn("failed to create LLM dump file", "session_id", id, "error", err)
			dumpFile = nil
		}
	}

	// Create orchestrator.
	orchestrator, err := factory(emitter, logger, workspacePath, bbFactory, dumpFile)
	if err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		if dumpFile != nil {
			_ = dumpFile.Close()
		}
		return nil, fmt.Errorf("failed to create orchestrator for restored session: %w", err)
	}

	// Wire task persistence into orchestrator.
	if adapter != nil {
		orchestrator.SetTaskStore(adapter)
		emitFn := m.emitFunc
		capturedSessionID := id
		orchestrator.SetBlackboardRestoreFunc(func(taskID, sessionID string, store core.TaskPersistence, logger *slog.Logger, opts ...core.MapBlackboardOption) (core.PersistableBlackboard, error) {
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

	// Parse creation time from stored info.
	createdAt, parseErr := time.Parse(time.RFC3339, info.CreatedAt)
	if parseErr != nil {
		createdAt = time.Now()
	}

	// Create session temp directory.
	tempDir := sessionTempDir(m.projectsDir, info.ProjectID, id)
	if mkErr := os.MkdirAll(tempDir, 0o755); mkErr != nil {
		m.log().Warn("failed to create session temp directory", "session_id", id, "temp_dir", tempDir, "error", mkErr)
	}

	sess := &Session{
		ID:            id,
		ProjectID:     info.ProjectID,
		Name:          info.Name,
		CreatedAt:     createdAt,
		Archived:      info.Archived,
		WorkspacePath: workspacePath,
		TempDir:       tempDir,
		orchestrator:  orchestrator,
		logFile:       logFile,
		dumpFile:      dumpFile,
		active:        false,
	}

	// Double-check under write lock: another goroutine may have restored the same session.
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
		return existing, nil
	}
	m.sessions[id] = sess
	m.mu.Unlock()

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
			s.mu.Unlock()
		}
	}
	m.mu.RUnlock()

	return sessions, nil
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

	// Snapshot mutable fields under read lock
	m.mu.RLock()
	factory := m.orchestratorFactory
	persistFn := m.tokenPersist
	ts := m.taskStore
	maxSumLen := m.maxSummaryLen
	m.mu.RUnlock()

	// Wire token persistence callback if configured
	if persistFn != nil {
		emitter.SetTokenPersist(func(inputTokens, outputTokens int, model, family string) {
			persistFn(id, inputTokens, outputTokens, model, family)
		})
	}

	// Build BlackboardFactory if task persistence is configured
	var bbFactory core.BlackboardFactory
	var adapter *TaskStoreAdapter
	if ts != nil {
		adapter = NewTaskStoreAdapter(ts)
		sessionID := id // capture for closure
		emitFunc := m.emitFunc
		bbFactory = func(taskID string) core.Blackboard {
			var pbb *PersistentBlackboard
			if maxSumLen > 0 {
				pbb = NewPersistentBlackboard(taskID, sessionID, adapter, logger, core.WithMaxSummaryLen(maxSumLen))
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
	if strings.EqualFold(m.logLevel, "DEBUG") {
		dumpPath := filepath.Join(m.logDir, fmt.Sprintf("session_%s_llm_dump.jsonl", id))
		dumpFile, err = os.OpenFile(dumpPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			m.log().Warn("failed to create LLM dump file", "session_id", id, "error", err)
			dumpFile = nil // non-fatal, continue without dump
		}
	}

	// Create orchestrator using the factory (called outside the lock — can be slow)
	orchestrator, err := factory(emitter, logger, workspacePath, bbFactory, dumpFile)
	if err != nil {
		// Close the log file since we're not creating the session
		if logFile != nil {
			_ = logFile.Close()
		}
		if dumpFile != nil {
			_ = dumpFile.Close()
		}
		return nil, fmt.Errorf("failed to create orchestrator: %w", err)
	}

	// Wire task persistence into core orchestrator for continuations
	if adapter != nil {
		orchestrator.SetTaskStore(adapter)
		emitFn := m.emitFunc
		capturedSessionID := id
		orchestrator.SetBlackboardRestoreFunc(func(taskID, sessionID string, store core.TaskPersistence, logger *slog.Logger, opts ...core.MapBlackboardOption) (core.PersistableBlackboard, error) {
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
	tempDir := sessionTempDir(m.projectsDir, projectID, id)
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		m.log().Warn("failed to create session temp directory", "session_id", id, "temp_dir", tempDir, "error", err)
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

	// Clean up temp directory
	if session.TempDir != "" {
		if err := os.RemoveAll(session.TempDir); err != nil {
			m.log().Warn("failed to remove session temp directory", "session_id", id, "temp_dir", session.TempDir, "error", err)
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

// SendMessage sends a user message to a session's orchestrator (async).
// Runs in a goroutine, results come via events.
func (m *Manager) SendMessage(ctx context.Context, id, text, mode string, activeSkills []string) error {
	session, err := m.getOrRestoreSession(id)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", id)
	}

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
	taskCtx = tools.WithTempDir(taskCtx, session.TempDir)
	taskCtx = tools.WithCoherence(taskCtx, m.fileTracker)
	session.cancel = cancel
	session.mu.Unlock()

	// Snapshot envInfo under read lock
	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo != nil {
		taskCtx = tools.WithEnvInfo(taskCtx, envInfo)
	}

	// Emit message received event
	m.emitFunc(Event{
		SessionID: id,
		Type:      "message_received",
		Data: MessageReceivedData{
			SessionID: id,
			Text:      text,
		},
	})

	// Check if this is the first message (session has default name)
	// and spawn title generation in background.
	session.mu.Lock()
	sessionName := session.Name
	session.mu.Unlock()
	m.mu.RLock()
	titleGen := m.titleGen
	store := m.sessionStore
	m.mu.RUnlock()
	if sessionName == "Session "+id[:8] && titleGen != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			title := titleGen.Generate(ctx, text, activeSkills)
			if title == "" {
				return
			}
			if err := m.RenameSession(id, title); err != nil {
				m.log().Warn("failed to rename session with generated title", "session", id, "error", err)
				return
			}
			m.log().Info("session auto-named", "session", id, "title", title)
			// Persist rename to store
			if store != nil {
				if err := store.RenameSession(context.Background(), id, title); err != nil {
					m.log().Warn("failed to persist session title", "session", id, "error", err)
				}
			}
		}()
	}

	// Launch goroutine to handle the message
	go func(ctx context.Context, msg string, skills []string) {
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
			TaskID:        lastTaskID,
			ExecutionMode: mode,
			UserSkills:    skills,
		})

		// Fallback: if continuation failed (restore error) and we had a TaskID, retry fresh
		if err != nil && lastTaskID != "" {
			m.log().Warn("continuation failed, falling back to fresh workflow", "session_id", id, "task_id", lastTaskID, "error", err)
			session.mu.Lock()
			session.lastCompletedTaskID = "" // clear to avoid repeated failures
			session.mu.Unlock()
			result, err = session.orchestrator.HandleMessage(ctx, msg, id, core.HandleOptions{
				TaskID:        "",
				ExecutionMode: mode,
				UserSkills:    skills,
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
							m.log().Warn("failed to persist completion on session done", "task", tid, "error", pErr)
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
		if pbb, ok := result.Blackboard.(*PersistentBlackboard); ok {
			session.mu.Lock()
			session.lastCompletedTaskID = pbb.TaskID()
			session.mu.Unlock()
		}

		// Safety net: if context was cancelled but orchestrator returned no error,
		// still treat as cancellation — do not emit partial results as final.
		if ctx.Err() == context.Canceled {
			m.emitFunc(Event{
				SessionID: id,
				Type:      "task_cancelled",
				Data: TaskCancelledData{
					SessionID: id,
				},
			})
			m.mu.RLock()
			ts := m.taskStore
			m.mu.RUnlock()
			if ts != nil {
				adapter := NewTaskStoreAdapter(ts)
				if tid, tErr := adapter.GetUnfinishedTaskID(id); tErr == nil && tid != "" {
					if pErr := adapter.PersistCompletion(tid, "", 0); pErr != nil {
						m.log().Warn("failed to persist completion on cancel safety-net", "task", tid, "error", pErr)
					}
				}
			}
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
				AttemptCount:    result.AttemptCount,
				Reflections:     result.Reflections,
			},
		})
		m.emitResumableIfUnfinished(id)
	}(taskCtx, text, activeSkills)

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

	session, restoreErr := m.getOrRestoreSession(id)
	if restoreErr != nil {
		return fmt.Errorf("failed to restore session: %w", restoreErr)
	}
	if session == nil {
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
	bb, err := RestoreBlackboard(taskID, id, adapter, nil)
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
	taskCtx = tools.WithTempDir(taskCtx, session.TempDir)
	taskCtx = tools.WithCoherence(taskCtx, m.fileTracker)
	session.cancel = cancel
	session.mu.Unlock()

	// Snapshot envInfo under read lock
	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo != nil {
		taskCtx = tools.WithEnvInfo(taskCtx, envInfo)
	}

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
		if pbb, ok := result.Blackboard.(*PersistentBlackboard); ok {
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

// CancelUnfinishedTask discards any unfinished task in the given session by
// marking it as completed in the task store. After this returns successfully,
// the session no longer has a resumable task and emitResumableIfUnfinished
// will not emit a "task_failed_resumable" event for it.
// Returns nil if no task store is configured or no unfinished task exists.
func (m *Manager) CancelUnfinishedTask(sessionID string) error {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return nil
	}

	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		return fmt.Errorf("failed to look up unfinished task: %w", err)
	}
	if taskID == "" {
		return nil
	}
	if err := adapter.PersistCompletion(taskID, "", 0); err != nil {
		return fmt.Errorf("failed to mark task as completed: %w", err)
	}
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
		m.log().Warn("failed to get unfinished task ID", "session", sessionID, "error", err)
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
	session, err := m.getOrRestoreSession(id)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
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
			m.log().Warn("timed out waiting for task goroutine to stop on cancel", "session_id", id)
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

// GetBlackboardState returns the current blackboard state for a session.
// It uses the in-memory lastCompletedTaskID if available, otherwise falls back
// to the most recent task ID from the database.
// Returns nil, nil if no task state is available.
func (m *Manager) GetBlackboardState(sessionID string) (*BlackboardState, error) {
	sess, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to restore session: %w", err)
	}
	if sess == nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()

	if ts == nil {
		return nil, nil // no task persistence — no blackboard state
	}

	// Try in-memory lastCompletedTaskID first.
	sess.mu.Lock()
	taskID := sess.lastCompletedTaskID
	sess.mu.Unlock()

	// Fallback: query the database for the latest task.
	if taskID == "" {
		dbTaskID, dbErr := ts.GetLatestTaskID(context.Background(), sessionID)
		if dbErr != nil {
			return nil, fmt.Errorf("failed to get latest task ID: %w", dbErr)
		}
		if dbTaskID == "" {
			return nil, nil // no tasks for this session
		}
		taskID = dbTaskID
	}

	adapter := NewTaskStoreAdapter(ts)
	state, err := adapter.LoadTaskState(taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to load task state: %w", err)
	}
	if state == nil {
		return nil, nil
	}

	return &BlackboardState{TaskState: state}, nil
}

// BlackboardState wraps a core.TaskState for the GetBlackboardState API.
type BlackboardState struct {
	TaskState *core.TaskState
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
		if session.dumpFile != nil {
			_ = session.dumpFile.Close()
		}
		session.mu.Unlock()

		// Remove from map
		delete(m.sessions, id)
	}
}
