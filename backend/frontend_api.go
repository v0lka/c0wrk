package backend

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/logger"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/review"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/papers"
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
	// modelProfilesGateResp caches the EFFECTIVE Model Profiles gate reported as
	// ConfigResponse.model_profiles. Resolving it needs the profile catalog, which is
	// read from disk, so caching keeps GetConfig a pure in-memory read (it runs
	// on every settings open — see the GUARANTEE on collectAllModels). Seeded at
	// construction and refreshed by refreshModelProfilesGateLocked at every Model Profiles
	// mutation. Guarded by configMu.
	modelProfilesGateResp ModelProfilesSettingsResponse
	// modelProfilesNotices carries one-shot Model Profiles profile notices (e.g. "the
	// active profile was deleted; switched to generic") for the NEXT
	// GetModelProfiles call. Guarded by configMu; drained on read.
	modelProfilesNotices []string
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
	// gitStatusCache / gitIgnoredCache memoize the two heavy per-repo git
	// subprocesses (git status --porcelain -uall; git ls-files --others
	// --ignored) that dominate GetGitStatus / ListDirectory. See
	// frontend_api_gitcache.go.
	gitStatusCache    map[string]gitStatusCacheEntry
	gitStatusCacheMu  sync.Mutex
	gitIgnoredCache   map[string]gitIgnoredCacheEntry
	gitIgnoredCacheMu sync.Mutex
	// gitStatusFn / gitIgnoredFn are test seams overriding the workspace git
	// helpers; nil in production, where the real workspace functions run.
	gitStatusFn  func(root string) (map[string]GitStatusEntry, error)
	gitIgnoredFn func(root string) (map[string]bool, error)
	// readProcessRSSFn, when non-nil, overrides the resident-set-size read
	// behind GetProcessMemory (frontend_api_system.go). Test-only seam (nil
	// in production, where readProcessRSS from processmem.go runs), mirroring
	// gitStatusFn.
	readProcessRSSFn func() (uint64, error)
	// fetchPaperOriginalFn, when non-nil, replaces the
	// papers.FetchOriginalHTML call behind FetchPaperOriginal
	// (frontend_api_papers_source.go). Test-only seam (nil in production),
	// mirroring gitStatusFn, so the RPC's plumbing is testable without the
	// network.
	fetchPaperOriginalFn func(ctx context.Context, client *http.Client, libraryRoot string, rec papers.PaperRecord) papers.FetchResult
	// remoteOpMu serializes remote git operations (pull/push/fetch) so that
	// only one network operation runs at a time per app instance.
	remoteOpMu sync.Mutex

	// Auto-fetch funnel state (see frontend_api_git_autofetch.go).
	// autoFetchMu guards lastAutoFetchAt — the timestamp of the last
	// automatic fetch ATTEMPT, stamped whenever a trigger gets past the
	// static gates (config / active project / is-a-repo), whatever the
	// fetch outcome. All automatic triggers share one
	// autoFetchMinInterval window through it, so a burst of triggers can
	// never hammer the remote even when every attempt fails fast.
	autoFetchMu     sync.Mutex
	lastAutoFetchAt time.Time

	// Periodic auto-fetch loop state (the git.auto_fetch_interval ticker,
	// see frontend_api_git_autofetch.go). autoFetchLoopMu guards
	// autoFetchLoopCancel / autoFetchLoopDone so StartAutoFetch starts at
	// most one loop and Cleanup can stop it from any goroutine. It is
	// separate from autoFetchMu above, which guards only the shared
	// min-interval timestamp of the fetch funnel.
	autoFetchLoopMu     sync.Mutex
	autoFetchLoopCancel context.CancelFunc
	autoFetchLoopDone   chan struct{}

	// autoFetchIntervalOverride, when > 0, replaces the configured
	// git.auto_fetch_interval in autoFetchInterval. Test-only seam (0 in
	// production), mirroring switchLockTimeoutOverride.
	autoFetchIntervalOverride time.Duration

	// autoFetchDisabledRecheckOverride, when > 0, replaces the parked-state
	// re-check cadence (autoFetchDisabledRecheck) in autoFetchLoop. Test-only
	// seam (0 in production), mirroring autoFetchIntervalOverride.
	autoFetchDisabledRecheckOverride time.Duration

	// autoFetchTickFn, when non-nil, replaces the autoFetchOnce call made
	// by the periodic ticker loop. Test-only seam (nil in production) so
	// loop tests observe ticks without touching git or the network.
	autoFetchTickFn func(trigger string)

	// Project
	projectManager    *project.Manager
	agentDir          string
	activeProjectID   string
	activeProjectPath string
	activeProjectMu   sync.RWMutex

	// switchMu serializes the whole SwitchProject body (teardown → vector →
	// watcher → activate → event). Wails runs each binding call in its own
	// goroutine, so two rapid CHAT↔CODE toggles used to interleave inside the
	// backend: a slower earlier switch could overwrite activeProjectID AFTER a
	// later switch had completed, leaving the backend on the older project
	// while the frontend (whose switch chain is serialized) believes the newer
	// one. Every subsequent ListDirectory against the frontend's rootPath then
	// fails containment ("path outside project workspace") and @-file
	// completions in the chat input stay empty until an app restart.
	// Acquisition is bounded by switchLockTimeout (see acquireSwitchLock) so a
	// wedged in-flight switch yields an error instead of an unbounded wait.
	switchMu sync.Mutex

	// switchLockTimeoutOverride, when > 0, replaces switchLockTimeout as the
	// deadline for acquiring switchMu. Test-only seam (0 in production).
	switchLockTimeoutOverride time.Duration

	// researchSeedMu serializes c0wrk-owned pack seeding. The startup
	// seedGlobalPacks run writes the GLOBAL .agents/{skills,agents}
	// directories; a concurrent writer targeting the same destination would
	// race the pack staging swap (one rename failing on the vanished source),
	// so the whole run is serialized. The lock is coarse (process-wide)
	// because seeding is millisecond-scale local IO.
	researchSeedMu sync.Mutex

	// switchInProgressHook is a test-only seam invoked inside SwitchProject
	// while switchMu is held (i.e. mid-switch). Nil in production.
	switchInProgressHook func(id string)

	// switchProjectSetupVectorFn, when non-nil, overrides
	// switchProjectSetupVector from SwitchProject. Test-only seam (nil in
	// production) letting a test drive the fallible pre-watcher step to
	// verify the switch stays atomic when it fails.
	switchProjectSetupVectorFn func(*project.ProjectInfo) error

	// Active research root path (empty for the No Project pseudo-project).
	// Guarded by activeProjectMu so it stays in sync with project switches.
	activeResearchRoot string

	// activePapersRoot holds the active paper-library root path
	// (<research-root>/papers; empty for the No Project pseudo-project).
	// Tracked separately from activeResearchRoot because the papers library is
	// a global subdirectory that always exists at the canonical research root
	// of a real project, even before any R-NNN project exists.
	// Guarded by activeProjectMu so it stays in sync with project switches.
	activePapersRoot string

	// activeComparisonsRoot holds the active multi-paper comparison root path
	// (<research-root>/comparisons; empty for the No Project pseudo-project).
	// Like the paper library it is a global subdirectory of the research root
	// and must be watched for every real project, so a comparison artifact
	// written before any R-NNN exists still refreshes the UI.
	// Guarded by activeProjectMu so it stays in sync with project switches.
	activeComparisonsRoot string

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

	// vectorSetupMu guards deferredVectorProject — the handshake that lets a
	// project switch whose vector-index setup was skipped (the manager was
	// still being built by the background ONNX init) be applied later, once the
	// manager is wired in via SetVectorManager. Acquired OUTSIDE
	// vectorManagerMu (vectorSetupMu → vectorManagerMu) and OUTSIDE switchMu
	// (switchMu → vectorSetupMu) to keep one global lock order.
	vectorSetupMu sync.Mutex
	// deferredVectorProject is the project whose vector-index setup was
	// skipped because getVectorManager() was nil (background ONNX init still
	// in flight). Drained once by InitVectorIndexForActiveProject when the
	// manager becomes available. Nil when no setup is pending.
	deferredVectorProject *project.ProjectInfo

	// vectorEmbedderInfo records the embedder's execution-provider facts for
	// vector-index status payloads (effective/requested provider, CUDA
	// verification verdict). Written once by desktop's background init and
	// read by every VectorIndexStatus producer; guarded by vectorManagerMu
	// to avoid a third lock for the same lifecycle.
	vectorEmbedderInfo VectorEmbedderInfo

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

	// Seed the effective Model Profiles gate cache (ConfigResponse.model_profiles) so GetConfig
	// stays a pure in-memory read. Every later Model Profiles mutation
	// refreshes it via refreshModelProfilesGateLocked.
	f.configMu.Lock()
	f.refreshModelProfilesGateLocked()
	f.configMu.Unlock()

	// Mirror the trusted-repo list into the process-wide git trust registry
	// (core/gittrust), which core/workspace consults to decide whether a
	// repository may spawn raw git. Nothing is trusted when config is nil or
	// the list is empty (fail-closed).
	f.syncGitTrustRegistry()

	// Seed the c0wrk-owned packs into the GLOBAL agent directories
	// (~/.c0wrk/.agents/{skills,agents}) BEFORE the directory watchers are
	// created, so the freshly created directories are watched on this launch.
	// The seeding is idempotent: missing entries are written and pack-marked
	// outdated ones upgraded, while user-owned directories are preserved.
	f.seedGlobalPacks()

	// The c0wrk packs are seeded into the compiled-in global dirs
	// (config.SkillsDir / config.AgentsDir off the agent dir). Discovery and
	// the watchers below follow the CONFIGURED dir lists, so warn when the
	// global dir is absent — a custom `skills.dirs`/`agents.dirs` list that
	// omits it silently orphans every seeded pack. Best-effort and non-fatal;
	// the seeding target is unchanged.
	seedSkillsDir, seedAgentsDir := "", ""
	if cfg.AgentDir != "" {
		seedSkillsDir = config.SkillsDir(cfg.AgentDir)
		seedAgentsDir = config.AgentsDir(cfg.AgentDir)
	}

	// Start watchers for global skill directories (those outside any
	// workspace). Changes invalidate the skill cache and emit skills:changed
	// so the frontend autocomplete refreshes without an app restart.
	if cfg.Config != nil && len(cfg.Config.Skills.Dirs) > 0 {
		dirs := resolveSkillDirs(cfg.Config.Skills.Dirs, cfg.AgentDir, config.ExpandEnvVars, f.logger)
		f.startSkillsWatchers(dirs)
		warnIfSeedDirUndiscovered(f.logger, "skills", seedSkillsDir, dirs)
	} else {
		warnIfSeedDirUndiscovered(f.logger, "skills", seedSkillsDir, nil)
	}

	// Start watchers for global Subagent Profile directories. Mirrors the
	// skill watchers: changes invalidate the agent cache and emit
	// agents:changed so the frontend #-autocomplete refreshes.
	if cfg.Config != nil && len(cfg.Config.Agents.Dirs) > 0 {
		dirs := resolveSkillDirs(cfg.Config.Agents.Dirs, cfg.AgentDir, config.ExpandEnvVars, f.logger)
		f.startAgentsWatchers(dirs)
		warnIfSeedDirUndiscovered(f.logger, "agents", seedAgentsDir, dirs)
	} else {
		warnIfSeedDirUndiscovered(f.logger, "agents", seedAgentsDir, nil)
	}

	return f
}

// warnIfSeedDirUndiscovered emits a startup warning when the compiled-in
// global directory the c0wrk packs are seeded into (config.SkillsDir /
// config.AgentsDir off the agent dir) is NOT among the effective configured
// discovery dirs the skill/agent watchers use. Discovery follows the
// configured list, so a custom list that omits the global directory silently
// orphans every seeded pack. Best-effort and non-fatal — an empty seedDir
// (no agent dir) is a no-op, since nothing is seeded then.
func warnIfSeedDirUndiscovered(lg *slog.Logger, kind, seedDir string, discovered []string) {
	if lg == nil || seedDir == "" {
		return
	}
	seed := filepath.Clean(seedDir)
	for _, d := range discovered {
		if filepath.Clean(d) == seed {
			return
		}
	}
	lg.Warn("c0wrk packs are seeded into a global directory that is absent from the configured discovery dirs; the seeded packs may be undiscoverable — add the directory to the dirs list",
		"kind", kind, "seed_dir", seedDir, "discovered_dirs", discovered)
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

// switchLockTimeout bounds how long SwitchProject waits to acquire switchMu
// before failing with errSwitchLockTimeout instead of blocking indefinitely
// behind an in-flight switch. Tests override it via
// FrontendAPI.switchLockTimeoutOverride.
const switchLockTimeout = 5 * time.Second

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
	// Stop the periodic auto-fetch ticker first so no new background fetch
	// starts while the rest of the backend tears down. Non-blocking: an
	// in-flight fetch is already bounded by remoteGitCmdTimeout and
	// cancelled through f.ctx() (see frontend_api_git_autofetch.go).
	f.stopAutoFetchLoop()
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

// SetVectorEmbedderInfo records the embedder's execution-provider facts
// (effective/requested provider, CUDA verification verdict) for inclusion in
// every subsequent VectorIndexStatus payload. Called once by desktop's
// background vector init after the embedder creation outcome is known —
// including the unavailable paths, where the info makes the failure
// self-explanatory in the status (which provider was asked for and why it did
// not come up). Thread-safe.
func (l *FrontendAPILifecycle) SetVectorEmbedderInfo(info VectorEmbedderInfo) {
	l.f.vectorManagerMu.Lock()
	l.f.vectorEmbedderInfo = info
	l.f.vectorManagerMu.Unlock()
}

// applyEmbedderInfo fills the execution-provider fields of a VectorIndexStatus
// from the stored embedder info. Called by every status producer before
// emitting, so the provider facts stay consistent across all status variants
// (success, unavailable, failure-reason). Zero-value fields remain empty and
// are omitted from the JSON payload.
func (f *FrontendAPI) applyEmbedderInfo(st *VectorIndexStatus) {
	f.vectorManagerMu.RLock()
	info := f.vectorEmbedderInfo
	f.vectorManagerMu.RUnlock()
	if info.IsZero() {
		return
	}
	st.ExecutionProvider = info.EffectiveProvider
	st.RequestedExecutionProvider = info.RequestedProvider
	st.ProviderFallbackReason = info.FallbackReason
	st.CUDAVerified = info.CUDAVerified
	st.DeviceID = info.DeviceID
}

// getVectorManager returns the vector index manager (may be nil if not yet initialized).
func (f *FrontendAPI) getVectorManager() *vectorindex.Manager {
	f.vectorManagerMu.RLock()
	defer f.vectorManagerMu.RUnlock()
	return f.vectorManager
}

// InitVectorIndexForActiveProject applies a project-switch vector setup that was
// skipped because the vector manager was not yet wired in. It is called by the
// desktop background ONNX goroutine immediately after SetVectorManager.
//
// Why it is needed: the frontend issues its first SwitchProject on
// backend:ready, which almost always arrives BEFORE the background ONNX init
// finishes (embedder load + manager construction). switchProjectSetupVector then
// sees getVectorManager() == nil and skips setup, so the startup project's index
// is never built and semantic search stays unavailable until the user manually
// switches projects. This drains the setup deferred by that skipped switch.
//
// It serializes with SwitchProject via switchMu, so it runs either entirely
// before the in-flight switch (the deferred slot is then empty and the switch's
// own setup observes the now-ready manager) or entirely after it (the deferred
// slot holds the destination project and is applied here). It is a no-op when
// nothing was deferred (the switch won the race and initialized the index
// itself), for No Project (CHAT mode), and when no project is active.
func (l *FrontendAPILifecycle) InitVectorIndexForActiveProject() {
	f := l.f
	if f == nil || f.getVectorManager() == nil {
		return
	}

	f.switchMu.Lock()
	defer f.switchMu.Unlock()

	f.vectorSetupMu.Lock()
	p := f.deferredVectorProject
	f.deferredVectorProject = nil
	f.vectorSetupMu.Unlock()
	if p == nil {
		return
	}

	// Only apply when the deferred project is still the active one. A
	// superseding switch (serialized behind switchMu above) would have already
	// run its own setup and cleared the deferred slot.
	f.activeProjectMu.RLock()
	activeID := f.activeProjectID
	f.activeProjectMu.RUnlock()
	if p.ID != activeID {
		return
	}

	if err := f.switchProjectSetupVector(p); err != nil {
		f.log().Warn("deferred vector index setup failed", "project", p.ID, "error", err)
	}
}
