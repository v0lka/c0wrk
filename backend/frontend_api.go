package backend

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/logger"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/review"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/updater"
	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/c0wrk/core/workspace"
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
	// saveMu serializes full config-save sequences for writers that must not
	// hold configMu across slow work (persist → No-Project provisioning →
	// judge/router rebuild, currently UpdateLLMConfig). configMu above guards
	// the config fields themselves; saveMu guards the ORDER of saves so
	// debounced updates apply strictly sequentially — a later save never
	// mutates f.config while an earlier save is still persisting/rebuilding.
	// It is acquired BEFORE configMu (saveMu → configMu), and readers such as
	// GetConfig never take it, so they stay responsive throughout a save.
	saveMu sync.Mutex

	// Persistence stores
	store       *session.SQLiteSessionStore
	projStore   *project.SQLiteProjectStore
	reviewStore *review.SQLiteReviewStore

	// Session
	sessionLogger *logger.SessionLogger
	logLevel      string

	// Workspace
	watcher        *workspace.Watcher
	watcherMu      sync.Mutex
	gitRepoCache   map[string]gitRepoCacheEntry
	gitRepoCacheMu sync.Mutex
	// remoteOpMu serializes remote git operations (pull/push/fetch) so that
	// only one network operation runs at a time per app instance.
	remoteOpMu sync.Mutex

	// Project
	projectManager    *project.Manager
	agentDir          string
	activeProjectID   string
	activeProjectPath string
	activeProjectMu   sync.RWMutex

	// switchMu serializes the whole SwitchProject body (teardown → activate →
	// watcher → vector → event). Wails runs each binding call in its own
	// goroutine, so two rapid CHAT↔CODE toggles used to interleave inside the
	// backend: a slower earlier switch could overwrite activeProjectID AFTER a
	// later switch had completed, leaving the backend on the older project
	// while the frontend (whose switch chain is serialized) believes the newer
	// one. Every subsequent ListDirectory against the frontend's rootPath then
	// fails containment ("path outside project workspace") and @-file
	// completions in the chat input stay empty until an app restart.
	switchMu sync.Mutex

	// switchInProgressHook is a test-only seam invoked inside SwitchProject
	// while switchMu is held (i.e. mid-switch). Nil in production.
	switchInProgressHook func(id string)

	// Active research root path (empty when RESEARCH is off). Guarded by
	// activeProjectMu so it stays in sync with project switches.
	activeResearchRoot string

	// Research hypothesis mutations. researchRootsMu guards researchRootMus,
	// which holds one mutex per research root path. Each per-root mutex
	// serializes the whole load→mutate→write chain of the UpdateHypothesis /
	// CreateHypothesis RPCs (Wails runs each binding call in its own
	// goroutine) so concurrent calls on one root cannot interleave their
	// read-modify-write of card+graph (lost updates) or duplicate the max+1
	// H-NNN id assignment of CreateHypothesis. Per-root granularity keeps
	// mutations on unrelated projects concurrent. In-process only — a second
	// app instance sharing a workspace has no cross-process lock.
	researchRootsMu sync.Mutex
	researchRootMus map[string]*sync.Mutex

	// Skill cache (invalidated on project switch)
	skillCache            []SkillDescriptorDTO
	skillCacheGen         uint64 // atomic — bumped to invalidate
	skillCacheGenSnapshot uint64
	skillCacheProjectDir  string
	skillCacheMu          sync.Mutex

	// Skill directory watchers monitor global skill dirs (outside any
	// workspace) for changes so the autocomplete / ListSkills cache stays
	// fresh without an app restart. Workspace-local skills are covered by
	// the workspace watcher above.
	skillWatchers   []*workspace.Watcher
	skillWatchersMu sync.Mutex

	// Agent cache (invalidated on project switch). Mirrors the skill cache for
	// Subagent Profile (AGENT.md) discovery.
	agentCache            []AgentDescriptorDTO
	agentCacheGen         uint64 // atomic — bumped to invalidate
	agentCacheGenSnapshot uint64
	agentCacheProjectDir  string
	agentCacheMu          sync.Mutex

	// Agent directory watchers monitor global Subagent Profile dirs (outside
	// any workspace) for changes. Mirrors skillWatchers.
	agentWatchers   []*workspace.Watcher
	agentWatchersMu sync.Mutex

	// Vector search
	vectorManager   *vectorindex.Manager
	vectorManagerMu sync.RWMutex

	// Self-update state. updateMu guards lastCheckResult and
	// downloadedArchivePath, which carry data across the stateful
	// CheckForUpdates → DownloadUpdate → ApplyUpdate RPC sequence.
	updateMu              sync.Mutex
	lastCheckResult       *updater.Result
	downloadedArchivePath string

	// Terminal
	terminalManager TerminalManager

	// Injected Wails callbacks (set by desktop during construction).
	emitEvent func(string, ...any)
	appCtx    func() context.Context
	// quitApp triggers a graceful Wails quit (wailsRuntime.Quit). Used by
	// ApplyUpdate after launching the self-update re-exec. Nil in tests.
	quitApp func()

	// builderOverride, when non-nil, replaces f.app.Builder() in the builder()
	// accessor. Used by tests to substitute a fake appBuilder so config/MCP
	// mutations can be verified without the real LLM router or MCP gateway.
	builderOverride appBuilder
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
	ReviewStore     *review.SQLiteReviewStore
	SessionLogger   *logger.SessionLogger
	LogLevel        string
	Watcher         *workspace.Watcher
	ProjectManager  *project.Manager
	AgentDir        string
	VectorManager   *vectorindex.Manager
	TerminalManager TerminalManager
	EmitEvent       func(string, ...any)
	AppCtx          func() context.Context
	// QuitApp triggers a graceful application quit (wired to wailsRuntime.Quit
	// in desktop). Used by ApplyUpdate after launching the self-update re-exec
	// so the updater process — which waits for the parent PID to die — can
	// proceed while Wails Shutdown hooks still run. Nil in tests (no-op).
	QuitApp func()
}

// NewFrontendAPI creates a new FrontendAPI with the given configuration.
func NewFrontendAPI(cfg FrontendAPIConfig) *FrontendAPI {
	f := &FrontendAPI{
		app:             cfg.App,
		logger:          cfg.Logger,
		config:          cfg.Config,
		configPath:      cfg.ConfigPath,
		store:           cfg.Store,
		projStore:       cfg.ProjStore,
		reviewStore:     cfg.ReviewStore,
		sessionLogger:   cfg.SessionLogger,
		logLevel:        cfg.LogLevel,
		watcher:         cfg.Watcher,
		projectManager:  cfg.ProjectManager,
		agentDir:        cfg.AgentDir,
		vectorManager:   cfg.VectorManager,
		terminalManager: cfg.TerminalManager,
		emitEvent:       cfg.EmitEvent,
		appCtx:          cfg.AppCtx,
		quitApp:         cfg.QuitApp,
	}

	// Mirror the trusted-repo list into the process-wide git trust registry
	// (core/gittrust), which core/workspace consults to decide whether a
	// repository may spawn raw git. Nothing is trusted when config is nil or
	// the list is empty (fail-closed).
	f.syncGitTrustRegistry()

	// Start watchers for global skill directories (those outside any
	// workspace). Changes invalidate the skill cache and emit skills:changed
	// so the frontend autocomplete refreshes without an app restart.
	if cfg.Config != nil && len(cfg.Config.Skills.Dirs) > 0 {
		dirs := resolveSkillDirs(cfg.Config.Skills.Dirs, cfg.AgentDir, config.ExpandEnvVars, f.logger)
		f.startSkillsWatchers(dirs)
	}

	// Start watchers for global Subagent Profile directories. Mirrors the
	// skill watchers: changes invalidate the agent cache and emit
	// agents:changed so the frontend #-autocomplete refreshes.
	if cfg.Config != nil && len(cfg.Config.Agents.Dirs) > 0 {
		dirs := resolveSkillDirs(cfg.Config.Agents.Dirs, cfg.AgentDir, config.ExpandEnvVars, f.logger)
		f.startAgentsWatchers(dirs)
	}

	return f
}

// FrontendAPILifecycle holds infrastructure/lifecycle methods that must NOT be
// exposed as Wails RPC methods. FrontendAPI owns a pointer to this struct;
// desktop accesses it via FrontendAPI.Lifecycle() which acts as a benign RPC
// getter (returns a struct pointer — Wails does not recursively bind methods
// on returned objects).
type FrontendAPILifecycle struct {
	f *FrontendAPI
}

// Lifecycle returns the lifecycle accessor for infrastructure methods that
// should not be directly callable from the frontend.
func (f *FrontendAPI) Lifecycle() *FrontendAPILifecycle {
	return &FrontendAPILifecycle{f: f}
}

// SetConfigLoadState sets the config loading state for display by GetConfig.
// Called by desktop after initial config loading.
// Moved to FrontendAPILifecycle to avoid exposure on the Wails RPC surface.
func (l *FrontendAPILifecycle) SetConfigLoadState(errors []string) {
	l.f.configLoadErrors = errors
}

// ctx returns the application context, falling back to context.Background()
// when the appCtx callback is not configured (e.g. in tests).
func (f *FrontendAPI) ctx() context.Context {
	if f.appCtx != nil {
		return f.appCtx()
	}
	return context.Background()
}

// serviceLLMTimeout returns the configured timeout for one-shot "service"
// LLM requests (session title, commit message, prompt optimization) —
// i.e. requests that are not part of the main chat loop. It reads the
// ServiceLLMRequestTimeout config value (seconds) and falls back to the
// default of 120s (2 min) when config is unset or the value is zero, so the
// frontend never hangs on an unresponsive provider even before config load.
func (f *FrontendAPI) serviceLLMTimeout() time.Duration {
	f.configMu.RLock()
	cfg := f.config
	f.configMu.RUnlock()
	if cfg != nil && cfg.Timeouts.ServiceLLMRequestTimeout > 0 {
		return time.Duration(cfg.Timeouts.ServiceLLMRequestTimeout) * time.Second
	}
	return 120 * time.Second
}

// EmitSessionEvent emits a session-scoped event through the combined UI +
// persistence path (delegates to Application). Used by desktop-layer
// callbacks (e.g. plan approval) so events survive app restarts.
func (f *FrontendAPI) EmitSessionEvent(evt session.Event) {
	if f.app != nil {
		f.app.EmitSessionEvent(evt)
	}
}

const gitRepoCacheTTL = 30 * time.Second

const gitRepoCacheMaxSize = 100

// gitRepoCacheEntry holds a cached IsGitRepo result with expiry.
type gitRepoCacheEntry struct {
	isRepo bool
	expiry time.Time
}

// isGitRepo reports whether dir is inside a git work tree. Results are
// cached for gitRepoCacheTTL to avoid repeated git process spawning during
// rapid file-tree refresh cycles. The cache is a ViewModel concern — the
// underlying workspace.IsGitRepo is stateless per ADR-009.
// Returns false for No Project (no git operations).
func (f *FrontendAPI) isGitRepo(dir string) bool {
	if f.isNoProject() {
		return false
	}
	now := time.Now()

	f.gitRepoCacheMu.Lock()
	if e, ok := f.gitRepoCache[dir]; ok && now.Before(e.expiry) {
		f.gitRepoCacheMu.Unlock()
		return e.isRepo
	}
	// Lightweight sweep: remove expired entries when the cache grows large.
	// If all entries are still valid (within TTL), evict the oldest to bound memory.
	if len(f.gitRepoCache) > gitRepoCacheMaxSize {
		type cacheEntry struct {
			key    string
			expiry time.Time
		}
		entries := make([]cacheEntry, 0, len(f.gitRepoCache))
		for k, e := range f.gitRepoCache {
			entries = append(entries, cacheEntry{key: k, expiry: e.expiry})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].expiry.Before(entries[j].expiry) })
		toDelete := len(f.gitRepoCache) - gitRepoCacheMaxSize
		for i := 0; i < toDelete; i++ {
			delete(f.gitRepoCache, entries[i].key)
		}
	}
	f.gitRepoCacheMu.Unlock()

	isRepo := workspace.IsGitRepo(f.ctx(), dir)

	f.gitRepoCacheMu.Lock()
	if f.gitRepoCache == nil {
		f.gitRepoCache = make(map[string]gitRepoCacheEntry)
	}
	f.gitRepoCache[dir] = gitRepoCacheEntry{isRepo: isRepo, expiry: now.Add(gitRepoCacheTTL)}
	f.gitRepoCacheMu.Unlock()

	return isRepo
}

// isNoProject reports whether the active project is the "No Project"
// pseudo-project. Thread-safe.
func (f *FrontendAPI) isNoProject() bool {
	f.activeProjectMu.RLock()
	defer f.activeProjectMu.RUnlock()
	return f.activeProjectID == project.NoProjectID
}

// Cleanup releases resources owned by FrontendAPI.
// Called from desktop.Shutdown.
// Moved to FrontendAPILifecycle to avoid exposure on the Wails RPC surface.
func (l *FrontendAPILifecycle) Cleanup() {
	f := l.f
	if f.terminalManager != nil {
		f.terminalManager.StopAll()
	}
	if vm := f.getVectorManager(); vm != nil {
		vm.Shutdown()
	}
	f.watcherMu.Lock()
	if f.watcher != nil {
		if err := f.watcher.Close(); err != nil {
			f.log().Error("failed to close workspace watcher", "error", err)
		}
		f.watcher = nil
	}
	f.watcherMu.Unlock()
	f.closeSkillsWatchers()
	f.closeAgentsWatchers()
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
	if f.reviewStore != nil {
		if err := f.reviewStore.Close(); err != nil {
			f.log().Error("failed to close review store", "error", err)
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
// Moved to FrontendAPILifecycle to avoid exposure on the Wails RPC surface.
func (l *FrontendAPILifecycle) SetVectorManager(m *vectorindex.Manager) {
	l.f.vectorManagerMu.Lock()
	l.f.vectorManager = m
	l.f.vectorManagerMu.Unlock()
}

// getVectorManager returns the vector index manager (may be nil if not yet initialized).
func (f *FrontendAPI) getVectorManager() *vectorindex.Manager {
	f.vectorManagerMu.RLock()
	defer f.vectorManagerMu.RUnlock()
	return f.vectorManager
}
