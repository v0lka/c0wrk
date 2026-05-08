package backend

import (
	"context"
	"log/slog"
	"sync"

	"github.com/user/agent/backend/config"
	"github.com/user/agent/backend/logger"
	"github.com/user/agent/backend/project"
	"github.com/user/agent/backend/session"
	"github.com/user/agent/backend/vectorindex"
	"github.com/user/agent/backend/workspace"
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
	vectorManager *vectorindex.Manager

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

// Cleanup releases resources owned by FrontendAPI.
// Called from desktop.Shutdown.
func (f *FrontendAPI) Cleanup() {
	if f.terminalManager != nil {
		f.terminalManager.StopAll()
	}
	if f.vectorManager != nil {
		f.vectorManager.Shutdown()
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
