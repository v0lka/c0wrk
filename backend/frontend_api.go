package backend

import (
	"context"
	"log/slog"
	"sync"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/logger"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/backend/vectorindex"
	"github.com/v0lka/c0wrk/backend/workspace"
)

// FrontendAPI holds state and methods that are exposed to the Wails frontend.
// It is embedded into the desktop.App struct so that promoted methods appear
// as direct methods of App to the Wails binding generator.
type FrontendAPI struct {
	app    *Application
	logger *slog.Logger

	// Config state
	config           *config.Config
	configMu         sync.RWMutex
	configPath       string
	configLoadErrors []string

	// Persistence stores
	store     *session.SQLiteSessionStore
	projStore *project.SQLiteProjectStore

	// Session
	sessionLogger *logger.SessionLogger
	logLevel      string

	// Workspace
	watcher *workspace.Watcher

	// Project
	projectManager    *project.Manager
	projectsDir       string
	activeProjectID   string
	activeProjectPath string
	activeProjectMu   sync.RWMutex

	// Vector search
	vectorManager   *vectorindex.Manager
	vectorManagerMu sync.RWMutex

	// Terminal
	terminalManager TerminalManager

	// Injected Wails callbacks (set by desktop during construction).
	emitEvent func(string, ...any)
	appCtx    func() context.Context
}

// TerminalManager is the interface for the terminal subsystem.
type TerminalManager interface {
	Start(sessionID, workDir string) error
	Write(sessionID string, data []byte) error
	Resize(sessionID string, cols, rows int) error
	Stop(sessionID string) error
	StopAll()
	IsActive(sessionID string) bool
}

// FrontendAPIConfig holds all parameters needed to construct a FrontendAPI.
type FrontendAPIConfig struct {
	App             *Application
	Logger          *slog.Logger
	Config          *config.Config
	ConfigPath      string
	Store           *session.SQLiteSessionStore
	ProjStore       *project.SQLiteProjectStore
	SessionLogger   *logger.SessionLogger
	LogLevel        string
	Watcher         *workspace.Watcher
	ProjectManager  *project.Manager
	ProjectsDir     string
	VectorManager   *vectorindex.Manager
	TerminalManager TerminalManager
	EmitEvent       func(string, ...any)
	AppCtx          func() context.Context
}

// NewFrontendAPI creates a new FrontendAPI with the given configuration.
func NewFrontendAPI(cfg FrontendAPIConfig) *FrontendAPI {
	return &FrontendAPI{
		app:             cfg.App,
		logger:          cfg.Logger,
		config:          cfg.Config,
		configPath:      cfg.ConfigPath,
		store:           cfg.Store,
		projStore:       cfg.ProjStore,
		sessionLogger:   cfg.SessionLogger,
		logLevel:        cfg.LogLevel,
		watcher:         cfg.Watcher,
		projectManager:  cfg.ProjectManager,
		projectsDir:     cfg.ProjectsDir,
		vectorManager:   cfg.VectorManager,
		terminalManager: cfg.TerminalManager,
		emitEvent:       cfg.EmitEvent,
		appCtx:          cfg.AppCtx,
	}
}

// SetConfigLoadState sets the config loading state for display by GetConfig.
// Called by desktop after initial config loading.
func (f *FrontendAPI) SetConfigLoadState(errors []string) {
	f.configLoadErrors = errors
}

// ctx returns the application context, falling back to context.Background()
// when the appCtx callback is not configured (e.g. in tests).
func (f *FrontendAPI) ctx() context.Context {
	if f.appCtx != nil {
		return f.appCtx()
	}
	return context.Background()
}

// Cleanup releases resources owned by FrontendAPI.
// Called from desktop.Shutdown.
func (f *FrontendAPI) Cleanup() {
	if f.terminalManager != nil {
		f.terminalManager.StopAll()
	}
	if vm := f.getVectorManager(); vm != nil {
		vm.Shutdown()
	}
	if f.watcher != nil {
		if err := f.watcher.Close(); err != nil {
			f.log().Error("failed to close workspace watcher", "error", err)
		}
		f.watcher = nil
	}
	if f.store != nil {
		if err := f.store.Close(); err != nil {
			f.log().Error("failed to close session store", "error", err)
		}
	}
	if f.projStore != nil {
		if err := f.projStore.Close(); err != nil {
			f.log().Error("failed to close project store", "error", err)
		}
	}
	if f.sessionLogger != nil {
		if err := f.sessionLogger.Close(); err != nil {
			f.log().Error("failed to close session logger", "error", err)
		}
	}
}

// log returns the instance logger, falling back to slog.Default() when nil.
func (f *FrontendAPI) log() *slog.Logger {
	if f.logger != nil {
		return f.logger
	}
	return slog.Default()
}

// SetVectorManager sets the vector index manager after background initialization.
// Thread-safe; may be called from any goroutine.
func (f *FrontendAPI) SetVectorManager(m *vectorindex.Manager) {
	f.vectorManagerMu.Lock()
	f.vectorManager = m
	f.vectorManagerMu.Unlock()
}

// getVectorManager returns the vector index manager (may be nil if not yet initialized).
func (f *FrontendAPI) getVectorManager() *vectorindex.Manager {
	f.vectorManagerMu.RLock()
	defer f.vectorManagerMu.RUnlock()
	return f.vectorManager
}
