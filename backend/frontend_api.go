package backend

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/logger"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/review"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core"
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
	config   *config.Config
	configMu sync.RWMutex
	// seedPublished is the Init-publication flag guardless seed-field readers
	// wait on via seedAcquire; the atomic pair — not the RWMutex barrier — is
	// what orders them against Init's writes. Full protocol at Init.
	seedPublished    atomic.Bool
	configPath       string
	configLoadErrors []string
	// modelProfilesGateResp caches the EFFECTIVE Model Profiles gate
	// (ConfigResponse.model_profiles): the two toggles plus the resolved
	// active id, refreshed on every Model Profiles mutation so GetConfig
	// stays a pure in-memory read; the identity half (verbatim active/suggested
	// ids) is filled fresh by GetConfig. Guarded by configMu.
	// See specs/domains/model-profiles.md.
	modelProfilesGateResp ModelProfilesSettingsResponse
	// modelProfilesNotices carries one-shot Model Profiles profile notices
	// (e.g. "the active profile was deleted; switched to generic") for the
	// NEXT GetModelProfiles call. Guarded by configMu; drained on read.
	modelProfilesNotices []string
	// saveMu serializes full config-save sequences for writers that must not
	// hold configMu across slow work (persist → No-Project provisioning →
	// judge/router rebuild, currently UpdateLLMConfig). It guards save ORDER
	// (acquired BEFORE configMu: saveMu → configMu) so debounced updates apply
	// strictly sequentially, and is never taken by readers.
	saveMu sync.Mutex

	// Persistence stores
	store       *session.SQLiteSessionStore
	projStore   *project.SQLiteProjectStore
	reviewStore *review.SQLiteReviewStore

	// Session
	sessionLogger *logger.SessionLogger
	logLevel      string

	// webviewGpuRunning/Source capture what THIS launch resolved (canonical
	// policy and the deciding layer), derived once in Init from the same
	// DeriveWebviewGpuRun inputs main.go's wails.Run options used. "Restart
	// required" is the pending-vs-running delta. Written once under configMu;
	// readers snapshot both under configMu.RLock.
	// See specs/domains/webview-gpu-policy.md.
	webviewGpuRunning       string
	webviewGpuRunningSource string

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
	// behind GetProcessMemory. Test-only seam (nil in production), mirroring
	// gitStatusFn.
	readProcessRSSFn func() (uint64, error)
	// readSystemFontsFn, when non-nil, overrides the desktop-environment font
	// reads behind GetSystemFonts. Test-only seam (nil in production),
	// mirroring readProcessRSSFn.
	readSystemFontsFn func() (systemFontPair, error)
	// listFontFamiliesFn, when non-nil, overrides the installed-font-family
	// enumeration behind ListFontFamilies. Test-only seam (nil in production),
	// mirroring readProcessRSSFn.
	listFontFamiliesFn func(monospace bool) ([]string, error)
	// fetchPaperOriginalFn, when non-nil, replaces the papers.FetchOriginalHTML
	// call behind FetchPaperOriginal. Test-only seam (nil in production),
	// mirroring gitStatusFn, so the RPC's plumbing is testable without the
	// network.
	fetchPaperOriginalFn func(ctx context.Context, client *http.Client, libraryRoot string, rec papers.PaperRecord) papers.FetchResult
	// remoteOpMu serializes remote git operations (pull/push/fetch) so that
	// only one network operation runs at a time per app instance.
	remoteOpMu sync.Mutex

	// Auto-fetch funnel state (see frontend_api_git_autofetch.go and
	// specs/domains/git-auto-fetch.md). autoFetchMu guards lastAutoFetchAt —
	// the shared min-interval stamp, taken on every attempt that passes the
	// static gates, whatever the outcome — so a burst of triggers can never
	// hammer the remote.
	autoFetchMu     sync.Mutex
	lastAutoFetchAt time.Time

	// Periodic auto-fetch ticker lifecycle (see frontend_api_git_autofetch.go
	// and specs/domains/git-auto-fetch.md). autoFetchLoopMu guards
	// autoFetchLoopCancel / autoFetchLoopDone so StartAutoFetch starts at
	// most one loop and Cleanup can stop it from any goroutine — separate
	// from autoFetchMu (the min-interval stamp).
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

	// autoFetchTickFn, when non-nil, replaces the autoFetchOnce call made by
	// the periodic ticker loop. Test-only seam (nil in production).
	autoFetchTickFn func(trigger string)

	// Project
	projectManager    *project.Manager
	agentDir          string
	activeProjectID   string
	activeProjectPath string
	activeProjectMu   sync.RWMutex

	// Git-panel focus target (ADR-080's GitPanelTarget consumption): the
	// worktree every git RPC operates on; empty = the project checkout. Set
	// only through SetGitPanelFocus, which validates membership in the active
	// project's worktree list (the focus model is specified in
	// specs/domains/session-worktrees.md). Guarded by gitFocusMu, separate
	// from activeProjectMu: different change cadences, neither write ever
	// holds both.
	gitFocusMu   sync.RWMutex
	gitFocusPath string

	// managedTreeMu serializes the shared-worktree critical sections (ADR-082):
	// the deletion's [count co-owners → own-row removal → release tree] and the
	// adoption's [commit binding → re-validate tree] must not interleave — see
	// specs/decisions/082-shared-managed-worktrees.md. Held only across the DB
	// count/writes plus one or two git calls, never across task cancellation,
	// terminal stops, or vector cleanup; a wait is bounded in practice (the
	// adoption side holds it under managedProvisionTimeout), so a wedged git
	// delays but never deadlocks.
	managedTreeMu sync.Mutex
	// bindInFlight holds the session ids with a workspace bind currently in
	// flight (the send-time consumption window): a second concurrent send
	// carrying a workspace choice is refused (ErrBindInProgress) instead of
	// racing the same fresh window — the backend backstop to the frontend's
	// own refusal. Zero-value-ready (sync.Map).
	bindInFlight sync.Map

	// switchMu serializes the whole SwitchProject body (teardown → vector →
	// watcher → activate → event). Wails runs each binding call in its own
	// goroutine, so without it a slower earlier switch could overwrite
	// activeProjectID after a later switch completed — the backend ends on
	// the older project while the frontend (whose switch chain is serialized)
	// believes the newer; every ListDirectory then fails containment and
	// @-file completions stay empty until an app restart. Acquisition is
	// bounded (switchLockTimeout, see acquireSwitchLock); the section hold by
	// switchSectionTimeout.
	switchMu sync.Mutex

	// switchLockTimeoutOverride, when > 0, replaces switchLockTimeout as the
	// deadline for acquiring switchMu. Test-only seam (0 in production).
	switchLockTimeoutOverride time.Duration

	// researchSeedMu serializes c0wrk-owned pack seeding (seedGlobalPacks —
	// see specs/decisions/056-research-always-on-global-seeding.md): a
	// concurrent writer would race the pack staging swap, so the whole run is
	// serialized process-wide; seeding is millisecond-scale local IO.
	researchSeedMu sync.Mutex

	// switchInProgressHook is a test-only seam invoked inside SwitchProject
	// while switchMu is held (i.e. mid-switch). Nil in production.
	switchInProgressHook func(id string)

	// switchProjectSetupVectorFn, when non-nil, overrides
	// switchProjectSetupVector from SwitchProject. Test-only seam (nil in
	// production) letting a test drive the fallible pre-watcher step to
	// verify the switch stays atomic when it fails.
	switchProjectSetupVectorFn func(ctx context.Context, p *project.ProjectInfo) error

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

	// Research hypothesis mutations. researchRootsMu guards researchRootMus —
	// one mutex per research root path, each serializing the whole
	// load→mutate→write chain of the UpdateHypothesis / CreateHypothesis RPCs
	// (no lost updates, no duplicated max+1 H-NNN id); per-root granularity
	// keeps unrelated projects concurrent. In-process only — no cross-process
	// lock against a second app instance on the same workspace.
	researchRootsMu sync.Mutex
	researchRootMus map[string]*sync.Mutex

	// Skill cache (invalidated on project switch)
	skillCache            []SkillDescriptorDTO
	skillCacheGen         uint64 // atomic — bumped to invalidate
	skillCacheGenSnapshot uint64
	skillCacheProjectDir  string
	skillCacheMu          sync.Mutex

	// Skill directory watchers monitor global skill dirs (outside any
	// workspace) so the autocomplete / ListSkills cache stays fresh without
	// an app restart; workspace-local skills ride the workspace watcher above.
	skillWatchers   []*workspace.Watcher
	skillWatchersMu sync.Mutex

	// Agent cache (invalidated on project switch). Mirrors the skill cache
	// for Subagent Profile (AGENT.md) discovery.
	agentCache            []AgentDescriptorDTO
	agentCacheGen         uint64 // atomic — bumped to invalidate
	agentCacheGenSnapshot uint64
	agentCacheProjectDir  string
	agentCacheMu          sync.Mutex

	// Agent directory watchers monitor global Subagent Profile dirs (outside
	// any workspace). Mirrors skillWatchers.
	agentWatchers   []*workspace.Watcher
	agentWatchersMu sync.Mutex

	// Command cache (invalidated on project switch). Mirrors the skill cache
	// for custom command template discovery (ADR-096).
	commandCache            []CommandDescriptorDTO
	commandCacheGen         uint64 // atomic — bumped to invalidate
	commandCacheGenSnapshot uint64
	commandCacheProjectDir  string
	commandCacheMu          sync.Mutex

	// commandDirs holds the resolved global command directories (the same
	// list the watchers get), stored once at startup; guarded by
	// commandWatchersMu.
	commandDirs []string

	// Command directory watchers monitor global command dirs (outside any
	// workspace). Mirrors skillWatchers.
	commandWatchers   []*workspace.Watcher
	commandWatchersMu sync.Mutex

	// Vector search: the per-root registry (ADR-081) — one manager per
	// workspace root, created on demand through the late-bound factory; the
	// registry's routing split (sessions via the executor context, user RPCs
	// via the Git-panel focus) is specified in
	// specs/decisions/081-vector-per-root-registry.md. vectorRootsMu guards
	// the lazy construction of the registry itself.
	vectorRoots   *VectorRoots
	vectorRootsMu sync.Mutex

	// pruneStaleBranchesInFlight single-flights the background stale-branch
	// index cleanup (pruneAbsentBranchVectorIndexes): rapid project toggles
	// each fire a detached goroutine, and a second concurrent sweep would only
	// duplicate work (the skip is silent; the running sweep reads fresh git
	// state anyway).
	pruneStaleBranchesInFlight atomic.Bool

	// purgeBranchLaunchFn is a test seam (nil in production) overriding the
	// launch of the detached delete-triggered branch purge; tests swap it for
	// a synchronous call so the purge completes before the RPC returns.
	purgeBranchLaunchFn func(fn func())

	// vectorSetupMu guards deferredVectorProject — the handshake that applies a
	// switch whose vector setup was skipped (factory not yet wired) later, via
	// SetVectorRootsFactory. Acquired OUTSIDE vectorRootsMu and switchMu
	// (vectorSetupMu last) to keep one global lock order.
	vectorSetupMu sync.Mutex
	// deferredVectorProject is the project whose vector-index setup was
	// skipped because the factory was not ready yet; drained once by
	// InitVectorIndexForActiveProject. Nil when nothing is pending.
	deferredVectorProject *project.ProjectInfo

	// vectorEmbedderInfo records the embedder's execution-provider facts
	// (effective/requested provider, CUDA verdict) for vector-index status
	// payloads. Written once by desktop's background init, read by every
	// status producer; guarded by vectorRootsMu to avoid a third lock.
	vectorEmbedderInfo VectorEmbedderInfo

	// Self-update state: updateMu guards lastCheckResult and
	// downloadedArchivePath across the stateful
	// CheckForUpdates → DownloadUpdate → ApplyUpdate RPC sequence.
	updateMu              sync.Mutex
	lastCheckResult       *updater.Result
	downloadedArchivePath string

	// Embedded local-model subsystem state: storage layout, the
	// core/embeddedllm supervisor and installer, the cached manifest
	// snapshot, the in-flight-install flag. Value field with its own two
	// mutexes, lazily constructed — see frontend_api_embedded.go.
	embedded embeddedLLMState

	// ChatGPT subscription-auth subsystem state: the providerauth token
	// manager, the in-flight browser sign-in run, its last error. Value
	// field with its own mutex, constructed by InitChatGPTAuth on the
	// startup path — see frontend_api_auth.go.
	chatgptAuth chatgptAuthState

	// Terminal
	terminalManager TerminalManager

	// managedForkCommit, when non-nil, replaces the store fork call used by
	// ForkSession's managed branch. Test-only seam (nil in production)
	// letting rollback tests fail persistence after the fork tree was
	// provisioned; mirrors gitStatusFn and friends.
	managedForkCommit func(ctx context.Context, srcID, dstID string, binding *session.WorkspaceBinding, cloner session.ForkReviewCloner) (*session.SessionInfo, error)

	// bindEvictHook, when non-nil, replaces the in-memory eviction both bind
	// paths perform after persisting the workspace binding. Test-only seam
	// (nil in production) replaying the interleavings the guards exclude: an
	// eviction refusal after the persist, and the adoption↔deletion losing
	// race. Same pattern as managedForkCommit and gitStatusFn.
	bindEvictHook func(id string) (*session.PendingAttachmentSnapshot, error)

	// Injected Wails callbacks (set by desktop during construction).
	emitEvent func(string, ...any)
	appCtx    func() context.Context
	// quitApp triggers a graceful Wails quit (wailsRuntime.Quit). Used by
	// ApplyUpdate after launching the self-update re-exec. Nil in tests.
	quitApp func()
	// onExperimentalChanged is the desktop hook invoked by
	// UpdateExperimentalFeatures after a SUCCESSFUL experimental-gate persist
	// (ADR-101) — the web-access sidecar mount follows the master gate live.
	// Installed inside Init alongside the other seed fields, before the
	// seedPublished flag. Nil keeps the toggle a pure config+router
	// operation. See specs/decisions/101-browser-web-access.md.
	onExperimentalChanged func(enabled bool)

	// builderOverride, when non-nil, replaces f.app.Builder() in the builder
	// accessor. Test-only seam substituting a fake appBuilder so config/MCP
	// mutations are verifiable without the real router or gateway.
	builderOverride appBuilder

	// Embedded-context refresh state. The /props context read-back lands the
	// corrected llm.models window while Server.Load is on the request path,
	// where a SYNCHRONOUS router rebuild is forbidden (see
	// specs/domains/embedded-llm.md); the persist instead schedules
	// rebuildAfterEmbeddedConfigChange on its own goroutine.
	// embeddedRefreshScheduled owns the single-flight window and
	// embeddedRefreshDirty records a change that landed mid-refresh, so the
	// loop reruns once more and no correction is lost between loads. Both
	// guarded by embeddedRefreshMu.
	embeddedRefreshMu        sync.Mutex
	embeddedRefreshScheduled bool
	embeddedRefreshDirty     bool
	embeddedRefreshDispatch  func(func())

	// displayWindowPush, when non-nil (tests), replaces the default
	// session-manager fan-out of the corrected display context window (the
	// pushDisplayContextWindow path — see specs/domains/embedded-llm.md).
	// Production leaves it nil.
	displayWindowPush func(model string, window int)

	// activeSessionCount reports how many sessions carry live background work
	// — the agent-idle seam the embedded service gate waits on (see
	// specs/domains/embedded-llm.md, wired by installServiceLLMGate). Nil
	// (tests, headless embedding) reads as "no agent is running", keeping the
	// gate inert.
	activeSessionCount func() int
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
	TerminalManager TerminalManager
	EmitEvent       func(string, ...any)
	AppCtx          func() context.Context
	// QuitApp triggers a graceful application quit (wired to wailsRuntime.Quit
	// in desktop), used by ApplyUpdate after launching the self-update re-exec
	// so the updater process — which waits for the parent PID to die — can
	// proceed while Wails Shutdown hooks run. Nil in tests (no-op).
	QuitApp func()
	// OnExperimentalChanged is the desktop hook invoked by
	// UpdateExperimentalFeatures after a SUCCESSFUL experimental-gate persist
	// (ADR-101) — the web-access sidecar mount follows the master gate live.
	// Rides the same construction seam as AppCtx/QuitApp, consumed by Init
	// with the other seed fields, so the write is ordered before the
	// seedPublished flag. Nil keeps the toggle a pure config+router
	// operation. See specs/decisions/101-browser-web-access.md.
	OnExperimentalChanged func(enabled bool)
	// WebviewGpuStderrObserved mirrors what main.go resolved at launch: the
	// crash capture installed (and was not opted out of). The Init
	// running-policy snapshot must consume the SAME fact the wails.Run
	// options did, or the Settings report would name a policy this launch
	// does not run with. Zero value (false) is the safe direction (an
	// unobserved run demotes fresh auto to never).
	// See specs/domains/webview-gpu-policy.md.
	WebviewGpuStderrObserved bool
}

// NewFrontendAPI creates a new FrontendAPI with the given configuration.
func NewFrontendAPI(cfg FrontendAPIConfig) *FrontendAPI {
	return (&FrontendAPI{}).Lifecycle().Init(cfg)
}

// installServiceLLMGate routes the session manager's one-shot service LLM
// requests (session title generation) through the embedded readiness gate.
// Installed unconditionally — the gate costs one config read for providers
// that are always listening; it is a method because the manager is built
// inside NewApplication before any FrontendAPI exists, and an unobservable
// wiring line silently goes missing. See specs/domains/embedded-llm.md.
func (f *FrontendAPI) installServiceLLMGate() {
	if f.appCell() == nil {
		return
	}
	if m := f.app.Manager(); m != nil {
		f.activeSessionCount = func() int { return len(m.ActiveSessions()) }
		m.SetServiceLLMGate(f.serviceEmbeddedGateBackground)
	}
}

// warnIfSeedDirUndiscovered warns at startup when the compiled-in global
// directory the c0wrk packs are seeded into is NOT among the effective
// configured discovery dirs the skill/agent watchers use — a custom list
// that omits it silently orphans every seeded pack. Best-effort, non-fatal;
// an empty seedDir (nothing seeded) is a no-op.
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

// FrontendAPILifecycle holds infrastructure/lifecycle methods that must NOT
// be exposed as Wails RPC methods (Init and SetConfigLoadState included).
// FrontendAPI owns a pointer; desktop reaches it via FrontendAPI.Lifecycle(),
// a benign RPC getter — Wails does not recursively bind methods on returned
// struct pointers.
type FrontendAPILifecycle struct {
	f *FrontendAPI
}

// Lifecycle returns the accessor for infrastructure methods that must not be
// directly callable from the frontend.
func (f *FrontendAPI) Lifecycle() *FrontendAPILifecycle {
	return &FrontendAPILifecycle{f: f}
}

// appCell reads the app seed field under configMu — the nil guard of the
// guarded RPC entry points (see Init for the publication invariant).
func (f *FrontendAPI) appCell() *Application {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	return f.app
}

// seedAcquire performs the Init-publication acquire for entry points that
// read seed fields without an app nil guard — it waits on the seedPublished
// flag (see Init). Single atomic load in steady state.
func (f *FrontendAPI) seedAcquire() {
	for !f.seedPublished.Load() {
		// Park briefly instead of a hot Gosched spin: the WAIT is unbounded
		// by design (a guardless RPC cannot proceed without the seed, and
		// first-run tool installs legitimately take minutes), but waiting
		// must not pin a whole core for that duration.
		time.Sleep(200 * time.Microsecond)
	}
}

// Init wires an already-allocated FrontendAPI in place and returns it. It
// lives on FrontendAPILifecycle because every exported FrontendAPI method is
// bound as a frontend-callable Wails RPC, and Init is startup-only
// infrastructure. The desktop App embeds a *FrontendAPI seed whose pointer
// the binding dispatch re-reads on every call, so startup must INITIALIZE
// that seed in place instead of swapping the pointer — no reader may observe
// two distinct values (review finding #52). Standalone instances use
// NewFrontendAPI.
func (l *FrontendAPILifecycle) Init(cfg FrontendAPIConfig) *FrontendAPI {
	f := l.f
	// Publish every seed field under ONE configMu critical section, then raise
	// seedPublished. THIS instance is already reachable when Init runs (the App
	// embeds this seed from NewApp onward), so a Wails binding goroutine may
	// serve an RPC while Startup is still here — a structural happens-before is
	// not achievable, so the ordering rests on two explicit synchronization
	// points:
	//
	//   - Guarded entries read through appCell(): before Init the nil guard
	//     errors out WITHOUT touching other seed fields; after this section,
	//     appCell's RLock is ordered after the Unlock below, so every plain
	//     seed-field read later in the call chain is ordered too.
	//   - Guardless entries call seedAcquire(): it waits on the seedPublished
	//     flag stored immediately after this Unlock; the atomic pair is the
	//     happens-before edge for the caller's subsequent plain reads. The
	//     flag goes up BEFORE the rest of Init (wiring, trust registry, pack
	//     seeding, watchers): those steps only READ seed fields on the Init
	//     goroutine, and goroutines created from here inherit the ordering by
	//     creation.
	//
	// Every Wails RPC entry point performs one of the two acquires before its
	// first seed-field read; helpers touching seed fields carry the same
	// acquire; non-RPC goroutines are ordered by creation. Cleanup re-checks
	// seedPublished with a bare Load, deliberately NOT a blocking seedAcquire,
	// because shutdown may run on a different goroutine than Startup.
	//
	// Except config/logLevel (written under configMu) and watcher (watcherMu),
	// the seed fields are written ONLY here, exactly once; test-constructed
	// FrontendAPI values never race an Init.
	f.configMu.Lock()
	f.app = cfg.App
	f.logger = cfg.Logger
	f.config = cfg.Config
	f.configPath = cfg.ConfigPath
	f.store = cfg.Store
	f.projStore = cfg.ProjStore
	f.reviewStore = cfg.ReviewStore
	f.sessionLogger = cfg.SessionLogger
	f.logLevel = cfg.LogLevel
	f.watcher = cfg.Watcher
	f.projectManager = cfg.ProjectManager
	f.agentDir = cfg.AgentDir
	// Derive the webview GPU run snapshot inside the same critical section
	// (issue #34): the running policy must reflect the state file as it was
	// at launch, not after any Settings write; the stderr-observation flag
	// comes from desktop so the snapshot matches the wails.Run resolution.
	// See specs/domains/webview-gpu-policy.md.
	run := DeriveWebviewGpuRun(runtime.GOOS, cfg.AgentDir, cfg.WebviewGpuStderrObserved, f.logger)
	f.webviewGpuRunning, f.webviewGpuRunningSource = run.Policy, run.Source
	f.terminalManager = cfg.TerminalManager
	f.emitEvent = cfg.EmitEvent
	f.appCtx = cfg.AppCtx
	f.quitApp = cfg.QuitApp
	f.onExperimentalChanged = cfg.OnExperimentalChanged
	// Wire the session-orchestrator factory's config source to a
	// lock-respecting conversion: the factory runs on Wails call goroutines,
	// concurrently with Settings saves that mutate f.config's maps IN PLACE
	// under configMu, so it must convert under the same lock instead of
	// ranging the live maps unlocked. toBuilderConfigLocked also attaches the
	// embedded loader seam (inert while nothing is installed).
	//
	// The wiring MUST happen inside this critical section, BEFORE the
	// seedPublished store below: an early CreateSession between the store and
	// a post-Unlock wiring would take the factory's unlocked startup-config
	// fallback — ranging the live maps while a settings save mutates them.
	// SetBuilderConfigProvider itself is a lock-free atomic store, so holding
	// configMu here cannot deadlock it.
	if cfg.App != nil {
		f.app.SetBuilderConfigProvider(func() *core.BuilderConfig {
			f.configMu.RLock()
			defer f.configMu.RUnlock()
			if f.config == nil {
				return nil
			}
			return f.toBuilderConfigLocked()
		})
	}
	// Publication point for seedAcquire: raised as soon as the sixteen seed
	// fields are written — BEFORE the remaining in-critical-section work —
	// so helpers Init itself calls (which pass their own seedAcquire) do not
	// deadlock on a flag only Init can raise. Readers released here see every
	// seed field above.
	f.seedPublished.Store(true)

	// Seed the effective Model Profiles gate cache (ConfigResponse.model_profiles)
	// so GetConfig stays a pure in-memory read; every later Model Profiles
	// mutation refreshes it. Folded into the same critical section as the
	// seed publication (the refresh reads f.config). See
	// specs/domains/model-profiles.md.
	f.refreshModelProfilesGateLocked()
	f.configMu.Unlock()

	// Mirror the trusted-repo list into the process-wide git trust registry
	// (core/gittrust) — fail-closed when config is nil or the list is empty.
	f.syncGitTrustRegistry()

	// Seed the c0wrk-owned packs into the GLOBAL agent directories BEFORE the
	// directory watchers are created, so the fresh directories are watched on
	// this launch. Idempotent, user-owned dirs preserved — see
	// specs/decisions/056-research-always-on-global-seeding.md.
	f.seedGlobalPacks()

	// Discovery and the watchers below follow the CONFIGURED dir lists; warn
	// when the compiled-in seed dir is absent (see warnIfSeedDirUndiscovered).
	seedSkillsDir, seedAgentsDir := "", ""
	if cfg.AgentDir != "" {
		seedSkillsDir = config.SkillsDir(cfg.AgentDir)
		seedAgentsDir = config.AgentsDir(cfg.AgentDir)
	}

	// Start watchers for global skill directories (those outside any
	// workspace). Changes invalidate the skill cache and emit skills:changed
	// so the frontend autocomplete refreshes without an app restart.
	if cfg.Config != nil && len(cfg.Config.Skills.Dirs) > 0 {
		dirs := resolveCatalogDirs(cfg.Config.Skills.Dirs, cfg.AgentDir, config.ExpandEnvVars, f.logger)
		f.startSkillsWatchers(dirs)
		warnIfSeedDirUndiscovered(f.logger, "skills", seedSkillsDir, dirs)
	} else {
		warnIfSeedDirUndiscovered(f.logger, "skills", seedSkillsDir, nil)
	}

	// Start watchers for global Subagent Profile directories. Mirrors the
	// skill watchers: changes invalidate the agent cache and emit
	// agents:changed so the frontend autocomplete refreshes.
	if cfg.Config != nil && len(cfg.Config.Agents.Dirs) > 0 {
		dirs := resolveCatalogDirs(cfg.Config.Agents.Dirs, cfg.AgentDir, config.ExpandEnvVars, f.logger)
		f.startAgentsWatchers(dirs)
		warnIfSeedDirUndiscovered(f.logger, "agents", seedAgentsDir, dirs)
	} else {
		warnIfSeedDirUndiscovered(f.logger, "agents", seedAgentsDir, nil)
	}

	// Start watchers for global custom command directories (ADR-096). Mirrors
	// the skill watchers: changes invalidate the command cache and emit
	// commands:changed. The resolved list also seeds ListCommands's discovery
	// — the backend owns command scanning (core never consumes commands).
	if cfg.Config != nil && len(cfg.Config.Commands.Dirs) > 0 {
		dirs := resolveCatalogDirs(cfg.Config.Commands.Dirs, cfg.AgentDir, config.ExpandEnvVars, f.logger)
		f.setCommandBaseDirs(dirs)
		f.startCommandsWatchers(dirs)
	} else {
		f.setCommandBaseDirs(nil)
	}

	f.installServiceLLMGate()

	// Wire the managed-worktree lifecycle owner into the session manager's
	// lazy restore: managed sessions fail closed (never fall back to the
	// project checkout) until this ensurer guarantees their tree exists.
	f.installWorkspaceEnsurer()

	return f
}

// SetConfigLoadState sets the config loading state for display by GetConfig.
// Called by desktop after initial config loading; on the Lifecycle like Init
// to stay off the Wails RPC surface.
func (l *FrontendAPILifecycle) SetConfigLoadState(errors []string) {
	// OnStartup runs on its own Wails goroutine while bound RPCs may already
	// be served — this writer takes the same configMu lock every other
	// configLoadErrors writer holds.
	l.f.configMu.Lock()
	l.f.configLoadErrors = errors
	l.f.configMu.Unlock()
}

// webAccessLoadWarningPrefix is the shared prefix of every warning the
// web_access section produces — the exact cut the refresh below replaces on;
// no other section's warning carries it.
const webAccessLoadWarningPrefix = "web_access"

// RefreshWebAccessLoadWarnings recomputes the web_access section's warnings
// in the config-load-warning channel so the Settings banner live-follows the
// transport (ADR-102): every webAccessLoadWarningPrefix entry is dropped and
// the CURRENT state's warnings are appended — a surgical one-section
// replacement, never a clear-all. The desktop control RPCs MUST invoke it
// OUTSIDE webAccessMu (it takes configMu; UpdateExperimentalFeatures holds
// configMu while taking webAccessMu, so the reverse order is an ABBA cycle).
// A concurrent GetConfig across the mutation+refresh gap can see the new
// section beside the old warning — the staleness this method bounds.
// See specs/domains/web-access.md.
func (l *FrontendAPILifecycle) RefreshWebAccessLoadWarnings() {
	l.f.configMu.Lock()
	defer l.f.configMu.Unlock()

	if l.f.config == nil {
		return
	}
	fresh := config.WebAccessLoadWarnings(l.f.config.WebAccess, l.f.config.Experimental.Enabled)
	kept := make([]string, 0, len(l.f.configLoadErrors)+len(fresh))
	for _, w := range l.f.configLoadErrors {
		if strings.HasPrefix(w, webAccessLoadWarningPrefix) {
			continue
		}
		kept = append(kept, w)
	}
	kept = append(kept, fresh...)
	l.f.configLoadErrors = kept
}

// ctx returns the application context, falling back to context.Background()
// when appCtx is not configured (tests).
func (f *FrontendAPI) ctx() context.Context {
	f.seedAcquire()
	if f.appCtx != nil {
		return f.appCtx()
	}
	return context.Background()
}

// serviceLLMTimeout returns the configured timeout for one-shot "service"
// LLM requests (session title, commit message, prompt optimization) —
// never part of the main chat loop. Falls back to 600s when config is unset
// or zero, so the frontend never hangs on an unresponsive provider.
func (f *FrontendAPI) serviceLLMTimeout() time.Duration {
	f.configMu.RLock()
	cfg := f.config
	f.configMu.RUnlock()
	if cfg != nil && cfg.Timeouts.ServiceLLMRequestTimeout > 0 {
		return time.Duration(cfg.Timeouts.ServiceLLMRequestTimeout) * time.Second
	}
	return 600 * time.Second
}

// EmitSessionEvent emits a session-scoped event through the combined UI +
// persistence path (delegates to Application) — used by desktop-layer
// callbacks (e.g. plan approval) so events survive app restarts.
func (f *FrontendAPI) EmitSessionEvent(evt session.Event) {
	f.seedAcquire()
	if f.app != nil {
		f.app.EmitSessionEvent(evt)
	}
}

// switchLockTimeout bounds how long SwitchProject waits to acquire switchMu
// before failing with errSwitchLockTimeout instead of blocking indefinitely
// behind an in-flight switch.
const switchLockTimeout = 5 * time.Second

// switchSectionTimeout bounds how long a switch may HOLD switchMu once
// acquired: the section runs a SEQUENCE of soft-failing ops, so per-op store
// deadlines alone would let holds stack under a saturated machine — every
// store round-trip in it derives from one ctx carrying this deadline. Two
// waits are deliberately NOT derived from it: createSessionForProject's
// deadline-less orchestrator build (pre-checked; may hold past this) and the
// vector leg's ManagerForRoot eviction-drain wait (vectorRootsEvictWait).
// Overruns still surface as a WARN at switchHeldWarnThreshold, letting the
// next bounded acquireSwitchLock recover the UI.
const switchSectionTimeout = 30 * time.Second

// switchHeldWarnThreshold is the hold duration at or above which
// SwitchProject logs a WARN after releasing switchMu.
const switchHeldWarnThreshold = 3 * time.Second

const gitRepoCacheTTL = 30 * time.Second

const gitRepoCacheMaxSize = 100

// gitRepoCacheEntry holds a cached IsGitRepo result with expiry.
type gitRepoCacheEntry struct {
	isRepo bool
	expiry time.Time
}

// isGitRepo reports whether dir is inside a git work tree, cached for
// gitRepoCacheTTL to avoid repeated git spawning during rapid file-tree
// refresh cycles (see specs/domains/git-auto-fetch.md). Returns false for
// No Project.
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
// pseudo-project.
func (f *FrontendAPI) isNoProject() bool {
	f.activeProjectMu.RLock()
	defer f.activeProjectMu.RUnlock()
	return f.activeProjectID == project.NoProjectID
}

// Cleanup releases resources owned by FrontendAPI (desktop.Shutdown; on the
// Lifecycle to stay off the Wails RPC surface). Every step logs its duration
// — this runs on the main goroutine, so a slow step is a frozen app and the
// per-step records make it attributable. NOTE: f.sessionLogger is
// deliberately NOT closed here — desktop owns it and closes it at the very
// end of App.Shutdown; closing it here silenced every later shutdown record.
func (l *FrontendAPILifecycle) Cleanup() {
	// Deliberately NOT seedAcquire: a blocking wait would hang Shutdown on
	// any seed Init never published (a quit racing a slow startup phase, or
	// a hand-constructed test instance). Unpublished seed = nothing to clean
	// up and no seed fields read; a true reading orders every plain read
	// below exactly like seedAcquire does.
	if !l.f.seedPublished.Load() {
		return
	}
	f := l.f
	cleanupStart := time.Now()
	cleanupStep := func(name string) {
		f.log().Info("cleanup step complete",
			"step", name,
			"step_ms", time.Since(cleanupStart).Milliseconds())
	}
	// Cancel an in-flight ChatGPT browser sign-in FIRST: its loopback
	// listener and goroutine must not outlive the teardown. The stop is
	// marked REQUESTED — a quit closes with the quiet cancelled event, and
	// the run's own failure branch does the bookkeeping (fire-and-forget,
	// non-blocking by construction).
	f.chatgptAuth.mu.Lock()
	if f.chatgptAuth.inFlight && f.chatgptAuth.cancel != nil {
		f.chatgptAuth.cancelRequested = true
		f.chatgptAuth.cancel()
	}
	f.chatgptAuth.mu.Unlock()
	// Stop the periodic auto-fetch ticker first so no new background fetch
	// starts while the rest of the backend tears down (non-blocking; an
	// in-flight fetch is bounded by remoteGitCmdTimeout — see
	// specs/domains/git-auto-fetch.md).
	f.stopAutoFetchLoop()
	cleanupStep("stopAutoFetchLoop")
	if f.terminalManager != nil {
		f.terminalManager.StopAll()
	}
	cleanupStep("terminalStopAll")
	f.vectorRootsRegistry().ShutdownAll()
	cleanupStep("vectorShutdown")
	f.watcherMu.Lock()
	if f.watcher != nil {
		if err := f.watcher.Close(); err != nil {
			f.log().Error("failed to close workspace watcher", "error", err)
		}
		f.watcher = nil
	}
	f.watcherMu.Unlock()
	cleanupStep("workspaceWatcherClose")
	f.closeSkillsWatchers()
	f.closeAgentsWatchers()
	f.closeCommandsWatchers()
	cleanupStep("skillAgentWatchersClose")
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
	cleanupStep("storeClose")
}

// log returns the instance logger, falling back to slog.Default() when nil.
func (f *FrontendAPI) log() *slog.Logger {
	f.seedAcquire()
	if f.logger != nil {
		return f.logger
	}
	return slog.Default()
}

// vectorRootsRegistry returns the per-root vector-index registry, creating
// it on first use (tests may pre-set f.vectorRoots).
func (f *FrontendAPI) vectorRootsRegistry() *VectorRoots {
	f.vectorRootsMu.Lock()
	defer f.vectorRootsMu.Unlock()
	if f.vectorRoots == nil {
		f.vectorRoots = newVectorRoots(f)
	}
	return f.vectorRoots
}

// SetVectorRootsFactory wires the manager factory the desktop background
// ONNX init produced, together with the readiness channel (closed once the
// init settles) and the once-guarded embedder close run by ShutdownAll.
// On the Lifecycle to stay off the Wails RPC surface.
func (l *FrontendAPILifecycle) SetVectorRootsFactory(factory func() (*vectorindex.Manager, error), ready <-chan struct{}, closeEmbedder func() error) {
	l.f.vectorRootsRegistry().SetFactory(factory, ready, closeEmbedder)
}

// VectorRoots exposes the per-root registry for desktop-side wiring (the
// Application's late-bound router needs the same instance).
func (l *FrontendAPILifecycle) VectorRoots() *VectorRoots {
	return l.f.vectorRootsRegistry()
}

// NotifyVectorFileChange triggers debounced incremental re-indexing on the
// manager of the given workspace root ("" = the focus root) — the
// file-mutating post-execute hook's entry point; a root with no live manager
// is a no-op (it indexes from scratch when next routed to).
func (l *FrontendAPILifecycle) NotifyVectorFileChange(root string) {
	l.f.vectorRootsRegistry().NotifyFileChangeForRoot(root)
}

// SetVectorEmbedderInfo records the embedder's execution-provider facts for
// every subsequent VectorIndexStatus payload. Called once by desktop's
// background vector init — including the unavailable paths, where the info
// makes the failure self-explanatory in the status.
func (l *FrontendAPILifecycle) SetVectorEmbedderInfo(info VectorEmbedderInfo) {
	l.f.vectorRootsMu.Lock()
	l.f.vectorEmbedderInfo = info
	l.f.vectorRootsMu.Unlock()
}

// applyEmbedderInfo fills the execution-provider fields of a
// VectorIndexStatus from the stored embedder info; called by every status
// producer before emitting so the facts stay consistent across variants.
// Zero-value fields stay empty and are omitted from the JSON payload.
func (f *FrontendAPI) applyEmbedderInfo(st *VectorIndexStatus) {
	f.vectorRootsMu.Lock()
	info := f.vectorEmbedderInfo
	f.vectorRootsMu.Unlock()
	if info.IsZero() {
		return
	}
	st.ExecutionProvider = info.EffectiveProvider
	st.RequestedExecutionProvider = info.RequestedProvider
	st.ProviderFallbackReason = info.FallbackReason
	st.CUDAVerified = info.CUDAVerified
	st.OVDevice = info.OVDevice
	st.DeviceID = info.DeviceID
}

// InitVectorIndexForActiveProject drains the project-switch vector setup
// deferred while the manager factory was still being built (see
// deferredVectorProject and specs/decisions/081-vector-per-root-registry.md).
// The frontend's first SwitchProject on backend:ready usually beats the
// background ONNX init, so switchProjectSetupVector skips and defers; this is
// called right after the factory lands. It serializes with SwitchProject via
// switchMu — it runs entirely before the in-flight switch (empty deferred
// slot; the switch's own setup sees the ready registry) or after it (the slot
// holds the destination and is applied here). No-op when nothing was
// deferred, for No Project, and when no project is active.
func (l *FrontendAPILifecycle) InitVectorIndexForActiveProject() {
	f := l.f
	if f == nil || !f.vectorRootsRegistry().FactoryReady() {
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

	// Only apply when the deferred project is still the active one: a
	// superseding switch (serialized behind switchMu above) would already
	// have run its own setup and cleared the deferred slot.
	f.activeProjectMu.RLock()
	activeID := f.activeProjectID
	f.activeProjectMu.RUnlock()
	if p.ID != activeID {
		return
	}

	if err := f.switchProjectSetupVector(context.Background(), p); err != nil {
		f.log().Warn("deferred vector index setup failed", "project", p.ID, "error", err)
	}
}
