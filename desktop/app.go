package desktop

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"

	"github.com/user/agent/backend"
	"github.com/user/agent/backend/config"
	"github.com/user/agent/backend/logger"
	"github.com/user/agent/backend/project"
	"github.com/user/agent/backend/session"
	"github.com/user/agent/backend/vectorindex"
	"github.com/user/agent/backend/workspace"
)

// App holds the Wails application state and exposes methods to the frontend.
type App struct {
	ctx        context.Context
	logger     *slog.Logger         // instance logger; nil-safe via log()
	app        *backend.Application // central ViewModel (owns builder, manager, persister)
	manager    *session.Manager     // convenience alias for app.Manager()
	db         *sql.DB              // shared SQLite connection
	store      *session.SQLiteSessionStore
	projStore  *project.SQLiteProjectStore
	config     *config.Config
	configMu   sync.RWMutex // protects config and config-related state
	configPath string

	sessionLogger *logger.SessionLogger
	logLevel      string

	// Config loading state for UI warnings
	configMigrated     bool
	configMigrationMsg string
	configLoadErrors   []string

	pendingConfirmations sync.Map
	pendingAskUser       sync.Map
	pendingStepLimit     sync.Map

	watcher        *workspace.Watcher
	projectManager *project.Manager

	projectsDir         string       // ~/.c0wrk/Projects/
	activeProjectID     string       // currently active project ID
	activeProjectPath   string       // workspace path of active project
	activeProjectMu     sync.RWMutex // protects activeProjectID/Path and codebaseProjectName
	codebaseProjectName string       // resolved codebase-memory-mcp project name for active project

	// Codebase indexing state
	restoreAutoIndex func()        // called on Shutdown to restore original auto_index value
	indexingDone     chan struct{} // closed when indexing completes; nil if not indexing
	indexingMu       sync.Mutex    // protects indexingDone

	// Vector search state
	vectorManager *vectorindex.Manager
}

// NewApp creates a new App instance.
func NewApp() *App {
	return &App{}
}

// log returns the instance logger, falling back to slog.Default() when nil.
func (a *App) log() *slog.Logger {
	if a.logger != nil {
		return a.logger
	}
	return slog.Default()
}
