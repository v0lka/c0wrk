// Package session provides session management for multiple agent sessions.
package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/markitdown"
	"github.com/v0lka/c0wrk/core/toolmanager"
	"github.com/v0lka/sp4rk/ignore"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/safeio"
	sdktools "github.com/v0lka/sp4rk/tools"
)

type contextKey string

const SessionIDKey contextKey = "session_id"

// restoreDBReadTimeout bounds every lazy-restore store read (session row,
// history, continuation anchor, compaction forecast) against the app's single
// shared SQLite pool: contention must surface as a retryable error, not park
// the restore — and via the restoreInFlight single-flight, every waiter.
const restoreDBReadTimeout = 15 * time.Second

// ensureWorktreeTimeout bounds the managed-workspace ensure during lazy
// restore (a git worktree list/recreate, which materializes a full checkout).
// Two minutes is a ceiling against a wedged git, not an expected duration.
const ensureWorktreeTimeout = 2 * time.Minute

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
	ProjectID           string // immutable after creation (no lock needed for reads)
	Name                string
	CreatedAt           time.Time
	LastActiveAt        time.Time
	Archived            bool
	Pinned              bool
	ProjectPath         string            // immutable owning project checkout
	workspaceBinding    *WorkspaceBinding // immutable, accessor returns copies
	WorkspacePath       string            // immutable session execution workspace
	TempDir             string            // session-specific temp directory
	orchestrator        *core.Orchestrator
	emitter             *EventEmitter                  // session emitter; agent quality metrics are read from it on task finish
	logFile             *os.File                       // session log file handle, closed on deletion
	dumpFile            *os.File                       // LLM dump file handle (DEBUG mode only), closed on deletion
	stepDumpTracker     *orchestration.StepDumpTracker // per-plan-step dump files (DEBUG mode only); CloseAll'd on deletion and shutdown — the orchestrator's Cleanup deliberately does not own it (session-layer resource), so without this every executed step leaks one open *os.File until process exit (and blocks Windows session-dir removal)
	cancel              context.CancelFunc             // cancel for current task
	active              bool                           // is currently processing
	pausing             bool                           // pause requested: the running task is on its way to a cooperative pause checkpoint (guarded by mu)
	pauseOwner          pauseOwner                     // who requested the in-flight/latest pause: the user or the manual-compaction flow (guarded by mu); the flow's auto-resume resumes only its own pause
	stopRequestedAt     time.Time                      // when a user-visible stop (CancelTask) was requested for the running task (guarded by mu); zero when none is pending. Reset when the task settles and when a new task launches. ActiveSessions uses it to flag a session that has not answered the stop as hung, so "quit anyway" is an informed choice.
	terminalEmitted     bool                           // the run's single terminal emission has been claimed (guarded by mu): forceTerminateStuckTask sets it when it emits the forced task_cancelled so the stuck goroutine, when it finally settles, skips its own duplicate emission. Reset when a NEW task launches (SendMessage/ResumeTask), not on settle — so a late force-terminate after the goroutine's own emission still observes the claim and cannot double-emit.
	forceTerminated     bool                           // this run was force-terminated by forceTerminateStuckTask (guarded by mu): its terminal event was emitted while the stuck goroutine was still running, so the goroutine has NOT yet run its own deferred deactivateSessionTask. A live message the user sends into that window arrived AFTER the cancel was reported and so belongs to the NEXT task — deactivateSessionTask keeps it queued instead of discarding it with the dead run. Cleared when the goroutine settles (deactivateSessionTask) and when a NEW task launches.
	done                chan struct{}                  // closed when task goroutine finishes
	compacting          bool                           // manual context compaction in flight: sends/resumes rejected, UI locked (guarded by mu)
	compactCancel       context.CancelFunc             // cancels the in-flight manual compaction (guarded by mu)
	compactDone         chan struct{}                  // closed when the manual-compaction flow goroutine exits (guarded by mu); joined by Shutdown and DeleteSession
	deleting            bool                           // deletion has begun: the compaction flow's tail must not auto-resume or touch the session after teardown (guarded by mu); set by DeleteSession before it cancels/joins the flow
	lastCompletedTaskID string                         // tracks last completed task for continuations
	mu                  sync.RWMutex
	// mu guards the mutable session fields below and the orchestrator/emitter
	// references; an RWMutex so read-mostly accessors can take a read lock.
	pendingAttachments      []orchestration.Attachment // user-attached files staged via AttachFiles, flushed into the blackboard on the next SendMessage (guarded by mu)
	pendingImageAttachments []ImageAttachment          // user-attached images staged via AttachFiles, snapshotted into ContentBlocks on the next SendMessage (guarded by mu)
}

// pauseOwner distinguishes who armed the shared pausing mechanism (user vs
// the manual-compaction flow): the flow's auto-resume resumes ONLY a pause it
// still owns, so an explicit user pause always survives a racing compaction.
// See specs/domains/session-lifecycle.md (Pause ownership).
type pauseOwner int

const (
	pauseOwnerNone       pauseOwner = iota
	pauseOwnerUser                  // PauseSession — the user explicitly asked for the paused state
	pauseOwnerCompaction            // CompactSessionContext — the flow paused a running task for the compaction window
)

// ImageAttachment is a user-attached image processed and saved to the
// session's images directory. Unlike document attachments it reaches the LLM
// as an image content block; only thumbnail + on-disk path are persisted
// (specs/domains/session-lifecycle.md, File Attachments).
type ImageAttachment struct {
	ID           string `json:"id"`
	OriginalName string `json:"original_name"`
	MediaType    string `json:"media_type"` // MIME type, e.g. "image/jpeg"
	Base64Data   string `json:"-"`          // base64-encoded image data (in-memory only, not persisted to DB)
	ThumbnailB64 string `json:"thumbnail"`  // JPEG data URI for UI display
	FilePath     string `json:"path"`       // absolute path to the saved processed image (session/images/{uuid}.jpg)
	SizeBytes    int64  `json:"size_bytes"`
}

func sessionTempDir(agentDir, projectID, sessionID string) string {
	return config.SessionTempDir(agentDir, projectID, sessionID)
}

// OrchestratorFactory builds the session's Orchestrator. The workspace path is
// the project workspace (so the worktree factory captures it); a nil bbFactory
// means an in-memory MapBlackboard.
type OrchestratorFactory func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer, stepDumpTracker *orchestration.StepDumpTracker) (*core.Orchestrator, error)

// TokenPersistFunc is called with cumulative session token totals after each
// LLM call; fillPercent is the conductor's context-window fill percent (0-100).
type TokenPersistFunc func(sessionID string, inputTokens, outputTokens int, model, family string, fillPercent float64)

// ProjectResolverFunc resolves a project ID to its workspace directory path.
type ProjectResolverFunc func(projectID string) (workspacePath string, err error)

// toolCallIDEntry records the most recent tool_call_id emitted for a session,
// plus its tool name for the confirmation callback's sanity check.
type toolCallIDEntry struct {
	id   string
	tool string
}

// Manager manages multiple agent sessions.
type Manager struct {
	sessions map[string]*Session
	// restoreInFlight single-flights lazy restores of one session ID (waiters
	// block on a channel closed when it finishes); without it, racing restores
	// churn log/dump open/close until Windows TempDir cleanup breaks.
	restoreInFlight map[string]chan struct{}
	// restoreParked parks NEW lazy restores while the session-promotion flow
	// owns the session; waiters re-run their restore from scratch once released
	// (specs/domains/session-lifecycle.md, Session Promotion).
	restoreParked        map[string]chan struct{}
	mu                   sync.RWMutex
	orchestratorFactory  OrchestratorFactory
	emitFunc             func(Event) // shared event emission callback
	agentDir             string      // base agent directory (~/.c0wrk)
	logLevel             string      // current log level for session loggers
	tokenPersist         TokenPersistFunc
	taskStore            TaskStore               // optional persistent task store
	sessionStore         SessionStore            // optional persistent session store
	projectStore         project.ProjectStore    // optional persistent project store (project-scoped work dirs)
	titleGen             *TitleGenerator         // optional title generator for auto-naming
	promptSuggestGen     *PromptSuggestGenerator // optional follow-up prompt suggestion generator (ADR-088)
	promptSuggestEnabled bool                    // prompt_suggestions.enabled config gate; the feature defaults to on
	envInfo              *sdktools.EnvInfo       // environment info for context injection
	envInfoDone          chan struct{}           // closed when the background env-info collection finishes (WriteOnce)
	envInfoOnce          sync.Once               // guards StartEnvInfoCollection against double-launch
	stopTimeout          time.Duration           // how long to wait for goroutine on cancel/delete
	maxSummaryLen        int                     // character limit for auto-generated step summaries
	serviceLLMTimeout    time.Duration           // timeout for one-shot service LLM requests (session title); default 10m
	// promoteRename is the fault-injection seam for MoveSessionStorage's
	// renames; in-package tests assign it directly. nil = os.Rename.
	promoteRename func(oldpath, newpath string) error
	// serviceLLMGate gates a one-shot service LLM request BEFORE its timeout
	// context is armed — a cold embedded-model load must never be charged to
	// serviceLLMTimeout (specs/domains/embedded-llm.md; ADR-066 D13). A gate
	// failure skips the request; nil is the posture for always-on providers.
	serviceLLMGate  func(context.Context) error
	projectResolver ProjectResolverFunc // resolves projectID -> workspacePath for lazy session restoration
	// workspaceEnsurer recreates a restored managed worktree before the
	// orchestrator is built on it (ADR-080); managed restores fail closed
	// without one and never fall back to the project checkout
	// (specs/domains/session-lifecycle.md, Workspace Binding).
	workspaceEnsurer WorkspaceEnsurer
	fileTracker      *FileCoherenceTracker
	converter        *markitdown.Converter // lazy-init markitdown converter for AttachFiles
	converterMu      sync.Mutex            // guards lazy converter initialization
	modelProfiles    ModelProfilesMetaInfo // Model Profiles profile annotating agent_metrics events (guarded by mu)

	// ignoreCache caches one ignore.Resolver per symlink-resolved root so the
	// directory tree is walked once per root, not per SendMessage.
	ignoreCache sync.Map // string (resolved root) → *ignore.Resolver

	// ignoreResolverBuild defaults to ignore.NewResolverContext; overridable in
	// tests. Callers must go through startIgnoreBuild.
	ignoreResolverBuild func(ctx context.Context, root string) (*ignore.Resolver, error)

	// caseInsensitiveCache caches the per-root case-sensitivity probe for the
	// manager lifetime, sharing one in-flight probe among concurrent callers
	// (specs/architecture/security-model.md, Filesystem case sensitivity).
	caseInsensitiveCache sync.Map // string (resolved path) → *caseInsensitiveProbe

	// detectCaseInsensitiveFn defaults to pathutil.DetectCaseInsensitive;
	// overridable in tests to assert call counts. Go through detectCaseInsensitive.
	detectCaseInsensitiveFn func(path string) bool

	// shuttingDown lets the SendMessage/Resume goroutines tell an app shutdown
	// from a user cancellation: on shutdown the task stays resumable and
	// Shutdown checkpoints it as paused (specs/domains/session-lifecycle.md).
	shuttingDown atomic.Bool

	// lastToolCallIDs maps sessionID → the most recent tool_call_id (+ tool
	// name) for the tool_confirm payload (specs/contracts/event-catalog.md);
	// only the latest entry is kept — confirmation fires right after the call.
	lastToolCallIDs sync.Map // sessionID → toolCallIDEntry

	// goalProposalResolver delivers a user decision to the desktop layer's
	// blocked goal-proposal channel, so the event-based and RPC-based answer
	// paths funnel through one resolution; nil → ResolveGoalProposal no-op.
	goalProposalResolver func(requestID, decision, condition, verify, verificationMode string) bool

	// goalSuggestionResolver is goalProposalResolver's twin for goal
	// suggestions (same two-path funnel; nil → ResolveGoalSuggestion no-op).
	goalSuggestionResolver func(requestID, decision string) bool

	logger *slog.Logger

	// bg tracks manager-owned background goroutines so Shutdown can join them
	// (see background.go); spawn refuses new work once Shutdown closed it.
	bg *backgroundTracker

	// shutdownCtx is cancelled at the very start of Shutdown so long-lived
	// best-effort work (title generation) aborts instead of running out its
	// LLM timeout against a torn-down backend.
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc

	// blackboards collects every PersistentBlackboard the manager built so
	// Shutdown can stop their persistence workers; a task that never reaches a
	// terminal finalizer would otherwise leave its worker blocked
	// (specs/domains/session-lifecycle.md).
	blackboards []*PersistentBlackboard

	// autoRetryResolver maps a provider name to its auto-resend interval in
	// seconds (0 = disabled); no backend timer exists — the deadline is only
	// stamped into the task_failed_resumable payload; the UI owns the countdown
	// (specs/decisions/065-auto-resend-retryable-errors.md).
	autoRetryResolver func(provider string) int
}

// SetLogger sets the logger for the manager.
func (m *Manager) SetLogger(l *slog.Logger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logger = l
}

// log returns the manager's logger, falling back to slog.Default().
func (m *Manager) log() *slog.Logger {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.logger != nil {
		return m.logger
	}
	return slog.Default()
}

// slowShutdownWaitThreshold marks a shutdown wait worth its own log record:
// the manager runs on the main goroutine, so a wait this long freezes the app.
const slowShutdownWaitThreshold = 250 * time.Millisecond

// NewManager creates a new session Manager.
func NewManager(factory OrchestratorFactory, emitFunc func(Event), agentDir string) *Manager {
	m := &Manager{
		sessions:             make(map[string]*Session),
		restoreInFlight:      make(map[string]chan struct{}),
		restoreParked:        make(map[string]chan struct{}),
		orchestratorFactory:  factory,
		emitFunc:             emitFunc,
		agentDir:             agentDir,
		logLevel:             "DEBUG",
		stopTimeout:          10 * time.Second,
		serviceLLMTimeout:    10 * time.Minute,
		promptSuggestEnabled: true, // prompt_suggestions.enabled: nil config = on
		envInfoDone:          make(chan struct{}),
	}
	m.bg = newBackgroundTracker()
	m.shutdownCtx, m.shutdownCancel = context.WithCancel(context.Background())
	m.fileTracker = NewFileCoherenceTracker(m.resolveSessionName)
	m.detectCaseInsensitiveFn = defaultDetectCaseInsensitive
	m.ignoreResolverBuild = ignore.NewResolverContext
	return m
}

// spawnBackground runs fn on a tracked background goroutine, reporting false
// once Shutdown closed the tracker so the caller can fall back to a sync path.
// Deprecated shell for existing tests; use spawnBackgroundNamed.
func (m *Manager) spawnBackground(fn func()) bool {
	return m.spawnBackgroundNamed("", fn)
}

// spawnBackgroundNamed is spawnBackground with an attribution name: on a join
// timeout, the alive-names log and the straggler dump identify the exact
// blocked call path.
func (m *Manager) spawnBackgroundNamed(name string, fn func()) bool {
	return m.bg.spawn(name, fn)
}

// trackBlackboard registers a PersistentBlackboard so Shutdown stops its
// persistence worker; already-exited workers are pruned on the way in so the
// slice cannot grow without bound in a long-lived process.
func (m *Manager) trackBlackboard(pb *PersistentBlackboard) {
	if pb == nil {
		return
	}
	m.mu.Lock()
	live := make([]*PersistentBlackboard, 0, len(m.blackboards)+1)
	for _, b := range m.blackboards {
		if !b.persistenceWorkerStopped() {
			live = append(live, b)
		}
	}
	live = append(live, pb)
	m.blackboards = live
	m.mu.Unlock()
}

// restoreBlackboardTracked restores a persisted blackboard and registers it
// with the manager; every manager-side RestoreBlackboard must go through here
// or an abandoned task leaks its persistence worker past Shutdown.
func (m *Manager) restoreBlackboardTracked(taskID, sessionID string, store core.TaskPersistence, logger *slog.Logger, opts ...orchestration.MapBlackboardOption) (*PersistentBlackboard, error) {
	pbb, err := RestoreBlackboard(taskID, sessionID, store, logger, opts...)
	if pbb != nil {
		m.trackBlackboard(pbb)
	}
	return pbb, err
}

// stopBackground joins the manager-owned background goroutines, then stops
// every tracked blackboard's persistence worker under ONE shared stopTimeout
// budget — re-draining until the registry stays empty, abandoning leftovers
// with a WARN once it passes (specs/domains/session-lifecycle.md).
func (m *Manager) stopBackground() {
	deadline := time.Now().Add(m.stopTimeout)
	bgStart := time.Now()
	if !m.bg.closeAndWait(m.stopTimeout) {
		// Every tracked goroutine is cancellation-driven, so reaching here means
		// a cancellation path is broken; name the survivors and dump stacks.
		alive := m.bg.aliveNames()
		m.log().Warn("timed out waiting for background goroutines to stop",
			"ms", time.Since(bgStart).Milliseconds(), "alive", alive)
		if dump := stragglerGoroutineDump(); dump != "" {
			m.log().Warn("shutdown: straggler goroutine stacks (github.com/v0lka/* frames)", "dump", dump)
		}
	}
	if elapsed := time.Since(bgStart); elapsed >= slowShutdownWaitThreshold {
		m.log().Warn("shutdown: slow background goroutine join", "ms", elapsed.Milliseconds())
	}

	drainPass := 0
	for {
		m.mu.Lock()
		blackboards := m.blackboards
		m.blackboards = nil
		m.mu.Unlock()
		if len(blackboards) == 0 {
			return
		}
		drainPass++
		bbStart := time.Now()
		for _, pb := range blackboards {
			pbStart := time.Now()
			pb.Shutdown(time.Until(deadline))
			if elapsed := time.Since(pbStart); elapsed >= slowShutdownWaitThreshold {
				m.log().Warn("shutdown: slow blackboard persistence worker stop", "ms", elapsed.Milliseconds())
			}
		}
		m.log().Info("shutdown: blackboard persistence workers stopped",
			"blackboards", len(blackboards), "ms", time.Since(bbStart).Milliseconds())

		// A straggler whose join timed out may still register a blackboard after
		// the snapshot, hence the re-drain; the single shared deadline (also
		// consumed by the join above) bounds it. "Late-registered" is only
		// true from the second pass on.
		if time.Now().After(deadline) {
			reason := "persistence workers"
			if drainPass > 1 {
				reason = "late-registered persistence workers"
			}
			m.log().Warn("shutdown: blackboard drain deadline exceeded; abandoning "+reason,
				"blackboards", len(blackboards), "ms", time.Since(bgStart).Milliseconds())
			return
		}
	}
}

// stopSessionBlackboards stops the persistence workers of every blackboard
// tracked for the given session: a deleted session's task can no longer reach
// a terminal finalizer, so without this each deleted session leaks its worker
// (plus blackboard and adapter) until Shutdown. Same stopTimeout budget and
// re-drain as stopBackground.
func (m *Manager) stopSessionBlackboards(sessionID string) {
	deadline := time.Now().Add(m.stopTimeout)
	for {
		m.mu.Lock()
		var sessionBBs []*PersistentBlackboard
		remaining := make([]*PersistentBlackboard, 0, len(m.blackboards))
		for _, b := range m.blackboards {
			if b.SessionID() == sessionID {
				sessionBBs = append(sessionBBs, b)
				continue
			}
			// Prune workers that already exited while we are here — same dead
			// weight trackBlackboard skips on the way in.
			if !b.persistenceWorkerStopped() {
				remaining = append(remaining, b)
			}
		}
		m.blackboards = remaining
		m.mu.Unlock()
		if len(sessionBBs) == 0 {
			return
		}
		for _, pb := range sessionBBs {
			pb.Shutdown(time.Until(deadline))
		}
		// Bounded re-drain: once the shared deadline passes, abandon the rest
		// with a WARN instead of spinning.
		if time.Now().After(deadline) {
			m.log().Warn("delete: blackboard stop deadline exceeded; abandoning late-registered persistence workers",
				"session_id", sessionID, "blackboards", len(sessionBBs))
			return
		}
	}
}

// resolveSessionName returns a display name for the given session ID.
func (m *Manager) resolveSessionName(id string) string {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return safeSessionPrefix(id)
	}
	s.mu.Lock()
	name := s.Name
	s.mu.Unlock()
	return name
}

// safeSessionPrefix returns the first 8 characters of id, or the full id if
// it is shorter than 8 characters. Guards against slicing beyond length.
func safeSessionPrefix(id string) string {
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

// SetTaskStore sets the TaskStore used to persist orchestration tasks; when
// set, CreateSession builds PersistentBlackboard-backed sessions from it.
func (m *Manager) SetTaskStore(store TaskStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskStore = store
}

// SetEnvInfo sets the environment info that will be injected into task contexts.
func (m *Manager) SetEnvInfo(info *sdktools.EnvInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.envInfo = info
}

// StartEnvInfoCollection launches environment-info collection on a background
// goroutine and stores the result via SetEnvInfo; SendMessage and ResumeTask
// tolerate a nil envInfo until it completes. A second call is absorbed
// by envInfoOnce.
func (m *Manager) StartEnvInfoCollection() {
	m.envInfoOnce.Do(func() {
		collect := func() {
			defer close(m.envInfoDone)
			m.SetEnvInfo(sdktools.CollectEnvInfo())
		}
		if !m.spawnBackgroundNamed("env-info-collection", collect) {
			// Shutdown already closed the tracker: never leave WaitEnvInfo
			// blocked on envInfoDone — publish "no info" and return.
			close(m.envInfoDone)
		}
	})
}

// WaitEnvInfo blocks until collection finishes (or ctx is cancelled);
// envInfoDone is closed via defer, so it never blocks forever once started.
func (m *Manager) WaitEnvInfo(ctx context.Context) error {
	select {
	case <-m.envInfoDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetMaxSummaryLen sets the character limit for auto-generated step summaries.
func (m *Manager) SetMaxSummaryLen(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxSummaryLen = n
}

// SetModelProfile records the Model Profiles profile sessions run under so
// agent_metrics events can be grouped by the active variants; annotated even
// when the master toggle is off (specs/domains/model-profiles.md). Applies to
// emitters created after the call.
func (m *Manager) SetModelProfile(cfg config.ModelProfilesConfig, profile config.ModelProfile) {
	info := modelProfileFromConfig(cfg, profile)
	m.mu.Lock()
	m.modelProfiles = info
	m.mu.Unlock()
}

// SetE2SSettings refreshes the E2S settings on every live session orchestrator:
// the builder seeds config.E2S once at Build, so an already-built orchestrator
// would keep a stale gate until restart. Safe while a task runs — the
// orchestrator pointer is published once and never reassigned.
func (m *Manager) SetE2SSettings(settings core.E2SSettings) {
	m.mu.RLock()
	orchestrators := make([]*core.Orchestrator, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.orchestrator != nil {
			orchestrators = append(orchestrators, s.orchestrator)
		}
	}
	m.mu.RUnlock()
	for _, o := range orchestrators {
		o.SetE2SSettings(settings)
	}
}

// SetModelProfilesSettings refreshes the model-profile settings on every live
// session orchestrator (master toggle, profile switch, essential-tools flip);
// otherwise sessions keep the stale build-time snapshot, e.g. a goal refused
// by a since-disabled narrowing. Mirrors SetE2SSettings.
func (m *Manager) SetModelProfilesSettings(settings core.ModelProfilesSettings) {
	m.mu.RLock()
	orchestrators := make([]*core.Orchestrator, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.orchestrator != nil {
			orchestrators = append(orchestrators, s.orchestrator)
		}
	}
	m.mu.RUnlock()
	for _, o := range orchestrators {
		o.SetModelProfilesSettings(settings)
	}
}

// modelProfile returns the recorded Model Profiles profile snapshot.
func (m *Manager) modelProfile() ModelProfilesMetaInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.modelProfiles
}

// SetServiceLLMTimeout sets the timeout for one-shot "service" LLM requests
// (session title generation); <= 0 leaves the default (10 min) in place.
func (m *Manager) SetServiceLLMTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.serviceLLMTimeout = d
}

// SetServiceLLMGate installs the pre-dispatch readiness gate for one-shot
// service LLM requests (see serviceLLMGate); a failure skips the request.
func (m *Manager) SetServiceLLMGate(fn func(context.Context) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.serviceLLMGate = fn
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

// SetProjectStore sets the persistent project store, used to load project-scoped
// auxiliary work directories into each task context.
func (m *Manager) SetProjectStore(store project.ProjectStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projectStore = store
}

// SetProjectResolver sets the function used to resolve a project ID to its
// workspace path. This is required for lazy session restoration from the database.
func (m *Manager) SetProjectResolver(fn ProjectResolverFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projectResolver = fn
}

// SetWorkspaceEnsurer installs the managed-workspace ensurer used during lazy
// restore (see WorkspaceEnsurer); managed restores fail closed without one.
func (m *Manager) SetWorkspaceEnsurer(fn WorkspaceEnsurer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workspaceEnsurer = fn
}

// getOrRestoreSession looks up a session in the in-memory map, lazily
// restoring it from the database when a session store + project resolver are
// configured. Returns (nil, nil) when the session genuinely does not exist.
func (m *Manager) getOrRestoreSession(id string) (*Session, error) {
	// Fast path: check in-memory map.
	m.mu.RLock()
	if sess, ok := m.sessions[id]; ok {
		m.mu.RUnlock()
		return sess, nil
	}
	// Shutdown choke point: once Shutdown has begun, never resurrect a session
	// from the store — its drain closed the file handles; a re-inserted
	// session would leak fresh ones plus task goroutines that outlive teardown.
	if m.shuttingDown.Load() {
		m.mu.RUnlock()
		return nil, nil
	}
	store := m.sessionStore
	resolver := m.projectResolver
	ensurer := m.workspaceEnsurer
	m.mu.RUnlock()

	if store == nil {
		m.log().Warn("session restoration skipped: session store not configured", "session_id", id)
		return nil, nil
	}
	if resolver == nil {
		m.log().Warn("session restoration skipped: project resolver not configured", "session_id", id)
		return nil, nil
	}

	// Bounded by restoreDBReadTimeout (see its doc): the restore is still
	// side-effect-free, so a timeout aborts cleanly and a retry starts over.
	restoreReadCtx, restoreReadCancel := context.WithTimeout(context.Background(), restoreDBReadTimeout)
	info, err := store.LoadSession(restoreReadCtx, id)
	if err != nil {
		restoreReadCancel()
		return nil, fmt.Errorf("failed to load session from store: %w", err)
	}
	if info == nil {
		restoreReadCancel()
		return nil, nil // session does not exist in DB either
	}

	// Resolve workspace path for the session's project.
	workspacePath, err := resolver(info.ProjectID)
	restoreReadCancel()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workspace for project %s: %w", info.ProjectID, err)
	}

	projectPath := workspacePath
	binding, err := NormalizeWorkspaceBinding(info.ProjectID, projectPath, info.WorkspaceBinding)
	if err != nil {
		return nil, fmt.Errorf("validate restored workspace: %w", err)
	}
	if binding != nil {
		workspacePath = binding.WorkspacePath
	}

	// No Project: re-derive the per-session isolated workspace exactly like
	// CreateSession so lazily restored sessions use the correct directory.
	if info.ProjectID == project.NoProjectID {
		workspacePath = config.NoProjectSessionWorkspace(m.agentDir, id)
		if absPath, absErr := filepath.Abs(workspacePath); absErr == nil {
			workspacePath = absPath
		} else {
			m.log().Warn("failed to resolve absolute workspace path for restored session",
				"session_id", id, "path", workspacePath, "error", absErr)
		}
		// MkdirAllReal, not os.MkdirAll: refuse to recreate through a dangling
		// or swapped-in symlink (review finding #101).
		if mkErr := safeio.MkdirAllReal(workspacePath, 0o755); mkErr != nil {
			m.log().Warn("failed to recreate per-session workspace on restore", "session_id", id, "error", mkErr)
		}
	}

	m.mu.Lock()
	if existing, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		return existing, nil
	}
	// Session-promotion window (restoreParked): wait for the release, then
	// restart — both the store state and the workspace are rewritten by then.
	if parkCh, parked := m.restoreParked[id]; parked {
		m.mu.Unlock()
		<-parkCh
		return m.getOrRestoreSession(id)
	}
	if waitCh, inflight := m.restoreInFlight[id]; inflight {
		m.mu.Unlock()
		<-waitCh
		m.mu.RLock()
		sess := m.sessions[id]
		m.mu.RUnlock()
		if sess != nil {
			return sess, nil
		}
		// The restorer failed; the session is not restorable right now.
		return nil, nil
	}
	waitCh := make(chan struct{})
	m.restoreInFlight[id] = waitCh
	m.mu.Unlock()

	// finishRestore releases the reservation and wakes waiters; it MUST run on
	// every return path below or later restores block forever.
	finishRestore := func() {
		m.mu.Lock()
		delete(m.restoreInFlight, id)
		m.mu.Unlock()
		close(waitCh)
	}

	// Managed binding: the ensurer guarantees the session-owned tree exists
	// before anything is built on it — fail-closed, never falling back to the
	// project checkout (specs/domains/session-lifecycle.md, Workspace Binding).
	// Inside the single-flight window and after the No-Project re-derivation,
	// so never for a CHAT session.
	if binding != nil && binding.Kind == WorkspaceManagedWorktree {
		if ensurer == nil {
			finishRestore()
			return nil, fmt.Errorf("managed session %q cannot be restored: no workspace ensurer is configured", id)
		}
		ensureCtx, ensureCancel := context.WithTimeout(context.Background(), ensureWorktreeTimeout)
		recreated, ensureErr := ensurer(ensureCtx, projectPath, binding)
		ensureCancel()
		if ensureErr != nil {
			finishRestore()
			return nil, fmt.Errorf("ensure session worktree %q on branch %q: %w", binding.WorktreeName, binding.Branch, ensureErr)
		}
		if recreated {
			m.log().Warn("recreated missing session worktree during restore",
				"session_id", id, "worktree", binding.WorktreeName, "branch", binding.Branch)
			// phase "orchestration" makes this a persisted chat row, not a
			// transient activity label (specs/contracts/event-catalog.md).
			m.emitFunc(Event{
				SessionID: id,
				Type:      "service",
				Data: map[string]any{
					"content": fmt.Sprintf("This session's worktree (%s, branch %s) was missing and has been recreated from the pinned branch. Uncommitted changes that existed only in the missing tree could not be recovered.", binding.WorktreeName, binding.Branch),
					"phase":   "orchestration",
				},
			})
		}
	}

	logger, logFile, err := m.createSessionLogger(info.ProjectID, id)
	if err != nil {
		finishRestore()
		return nil, fmt.Errorf("failed to create session logger: %w", err)
	}

	emitter := NewEventEmitter(id, m.emitFunc)
	// Record each emitted tool_call_id so the desktop confirmation callback can
	// attach the matching id to the tool_confirm payload.
	emitter.SetToolCallIDSink(func(tool, toolCallID string) {
		m.lastToolCallIDs.Store(id, toolCallIDEntry{id: toolCallID, tool: tool})
	})
	// Annotate agent metrics with the Model Profiles profile the session runs under.
	modelProfiles := m.modelProfile()
	emitter.SetModelProfile(modelProfiles)

	m.mu.RLock()
	factory := m.orchestratorFactory
	persistFn := m.tokenPersist
	ts := m.taskStore
	maxSumLen := m.maxSummaryLen
	m.mu.RUnlock()

	if persistFn != nil {
		emitter.SetTokenPersist(func(inputTokens, outputTokens int, model, family string, fillPercent float64) {
			persistFn(id, inputTokens, outputTokens, model, family, fillPercent)
		})
	}

	var bbFactory core.BlackboardFactory
	var adapter *TaskStoreAdapter
	if ts != nil {
		adapter = NewTaskStoreAdapter(ts)
		sessionID := id // capture for closure
		emitFunc := m.emitFunc
		bbFactory = func(taskID string) orchestration.Blackboard {
			var pbb *PersistentBlackboard
			if maxSumLen > 0 {
				pbb = NewPersistentBlackboard(taskID, sessionID, adapter, logger, orchestration.WithMaxSummaryLen(maxSumLen))
			} else {
				pbb = NewPersistentBlackboard(taskID, sessionID, adapter, logger)
			}
			m.trackBlackboard(pbb)
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

	dumpFile, stepDumpTracker := m.openDumpArtifacts(info.ProjectID, id)

	orchestrator, err := factory(emitter, logger, workspacePath, bbFactory, dumpFile, stepDumpTracker)
	if err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		if dumpFile != nil {
			_ = dumpFile.Close()
		}
		if stepDumpTracker != nil {
			_ = stepDumpTracker.CloseAll()
		}
		finishRestore()
		return nil, fmt.Errorf("failed to create orchestrator for restored session: %w", err)
	}

	// No Project mode: disable index-dependent tools (no vector index exists
	// without a project).
	if info.ProjectID == project.NoProjectID {
		orchestrator.SetNoProjectMode()
	}

	// Wire task persistence into orchestrator.
	if adapter != nil {
		orchestrator.SetTaskStore(adapter)
		emitFn := m.emitFunc
		capturedSessionID := id
		orchestrator.SetBlackboardRestoreFunc(func(taskID, sessionID string, store core.TaskPersistence, logger *slog.Logger, opts ...orchestration.MapBlackboardOption) (core.PersistableBlackboard, error) {
			pbb, err := m.restoreBlackboardTracked(taskID, sessionID, store, logger, opts...)
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

	// Restore the conversation history, bounded by restoreDBReadTimeout like
	// the head read; failure is non-fatal
	// (specs/domains/session-lifecycle.md, Conversation History).
	if m.sessionStore != nil {
		historyCtx, historyCancel := context.WithTimeout(context.Background(), restoreDBReadTimeout)
		storedMsgs, loadErr := m.sessionStore.LoadMessages(historyCtx, id)
		historyCancel()
		if loadErr != nil {
			m.log().Warn("failed to load session messages for history restore", "session_id", id, "error", loadErr)
		} else {
			history := m.convertChatMessagesToLLM(storedMsgs, workspacePath)
			if len(history) > 0 {
				orchestrator.SetConversationHistory(history)
				m.log().Debug("restored conversation history from store", "session_id", id, "messages", len(history))
			}
		}
	}

	// Restore the persisted EWMA compaction-forecast calibration
	// (specs/domains/memory/compaction.md).
	m.loadCompactionForecast(orchestrator)

	// Restore the continuation anchor (lastCompletedTaskID) so the next message
	// continues the previous task (specs/domains/session-lifecycle.md).
	var restoredTaskID string
	if ts != nil {
		anchorCtx, anchorCancel := context.WithTimeout(context.Background(), restoreDBReadTimeout)
		latestTaskID, taskErr := ts.GetLatestTaskID(anchorCtx, id)
		anchorCancel()
		switch {
		case taskErr != nil:
			m.log().Warn("failed to restore last task ID for session", "session_id", id, "error", taskErr)
		case latestTaskID != "":
			restoredTaskID = latestTaskID
			m.log().Debug("restored last task ID from store", "session_id", id, "task_id", latestTaskID)
		}
	}

	createdAt, parseErr := time.Parse(time.RFC3339, info.CreatedAt)
	if parseErr != nil {
		createdAt = time.Now().UTC()
	}

	tempDir := sessionTempDir(m.agentDir, info.ProjectID, id)
	// MkdirAllReal, not os.MkdirAll: refuse a dangling or swapped-in symlink
	// (review finding #101).
	if mkErr := safeio.MkdirAllReal(tempDir, 0o755); mkErr != nil {
		m.log().Warn("failed to create session temp directory", "session_id", id, "temp_dir", tempDir, "error", mkErr)
	}

	sess := &Session{
		ID:                  id,
		ProjectID:           info.ProjectID,
		Name:                info.Name,
		CreatedAt:           createdAt,
		Archived:            info.Archived,
		Pinned:              info.Pinned,
		ProjectPath:         projectPath,
		workspaceBinding:    binding,
		WorkspacePath:       workspacePath,
		TempDir:             tempDir,
		orchestrator:        orchestrator,
		emitter:             emitter,
		logFile:             logFile,
		dumpFile:            dumpFile,
		stepDumpTracker:     stepDumpTracker,
		active:              false,
		lastCompletedTaskID: restoredTaskID,
	}

	// Double-check under the write lock: unreachable through the single-flight
	// reservation, but guards against an insert that bypassed it. The
	// shuttingDown re-check closes the TOCTOU window with Shutdown — the
	// entry-time check ran before the slow restore work.
	m.mu.Lock()
	if existing, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		if logFile != nil {
			_ = logFile.Close()
		}
		if dumpFile != nil {
			_ = dumpFile.Close()
		}
		if stepDumpTracker != nil {
			_ = stepDumpTracker.CloseAll()
		}
		finishRestore()
		return existing, nil
	}
	if m.shuttingDown.Load() {
		m.mu.Unlock()
		// Shutdown began mid-restore: discard the session instead of
		// inserting it, closing the handles we just opened.
		if logFile != nil {
			_ = logFile.Close()
		}
		if dumpFile != nil {
			_ = dumpFile.Close()
		}
		if stepDumpTracker != nil {
			_ = stepDumpTracker.CloseAll()
		}
		finishRestore()
		return nil, nil
	}
	m.sessions[id] = sess
	m.mu.Unlock()

	finishRestore()
	m.log().Info("restored session from database", "session_id", id, "project_id", info.ProjectID)
	return sess, nil
}

// ListSessionsByProject returns sessions for a project, merging in-memory active
// state with persistent store data. Falls back to in-memory sessions if no store.
func (m *Manager) ListSessionsByProject(projectID string) ([]SessionInfo, error) {
	listCtx, listCancel := context.WithTimeout(context.Background(), restoreDBReadTimeout)
	defer listCancel()
	return m.ListSessionsByProjectCtx(listCtx, projectID)
}

// ListSessionsByProjectCtx is the caller-deadline variant of
// ListSessionsByProject: the project-switch critical section derives every
// store round-trip from ONE switch-scoped deadline, since bounded reads still
// sum to minutes of switchMu hold time under a saturated pool.
func (m *Manager) ListSessionsByProjectCtx(ctx context.Context, projectID string) ([]SessionInfo, error) {
	m.mu.RLock()
	store := m.sessionStore
	m.mu.RUnlock()

	if store == nil {
		all := m.ListSessions()
		result := make([]SessionInfo, 0)
		for _, s := range all {
			if s.ProjectID == projectID {
				result = append(result, s)
			}
		}
		return result, nil
	}

	// Bounded by restoreDBReadTimeout; the switch path's ctx is stricter on top.
	sessions, err := store.ListSessionsByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}

	m.mu.RLock()
	for i := range sessions {
		if s, ok := m.sessions[sessions[i].ID]; ok {
			s.mu.Lock()
			sessions[i].Active = s.active
			sessions[i].Pinned = s.Pinned
			s.mu.Unlock()
		}
	}
	m.mu.RUnlock()

	return sessions, nil
}

// ListSessionsAll returns session metadata for ALL projects — the source for
// cross-project indicators. Rows come from store.ListSessions (pinned first,
// then effective activity) with live Active/Pinned overlaid; without a store
// it falls back to the in-memory list.
func (m *Manager) ListSessionsAll() ([]SessionInfo, error) {
	m.mu.RLock()
	store := m.sessionStore
	m.mu.RUnlock()

	var sessions []SessionInfo
	if store == nil {
		sessions = m.ListSessions()
	} else {
		listCtx, listCancel := context.WithTimeout(context.Background(), restoreDBReadTimeout)
		var err error
		sessions, err = store.ListSessions(listCtx)
		listCancel()
		if err != nil {
			return nil, err
		}

		m.mu.RLock()
		for i := range sessions {
			if s, ok := m.sessions[sessions[i].ID]; ok {
				s.mu.Lock()
				sessions[i].Active = s.active
				sessions[i].Pinned = s.Pinned
				s.mu.Unlock()
			}
		}
		m.mu.RUnlock()
	}

	// The cross-project indicator surfaces only live work: drop archived rows
	// so a lingering task state cannot resurrect an archived session into it.
	filtered := make([]SessionInfo, 0, len(sessions))
	for _, s := range sessions {
		if !s.Archived {
			filtered = append(filtered, s)
		}
	}
	return filtered, nil
}

// CreateSession creates a new session with a fresh orchestrator.
// The projectID ties the session to a project; workspacePath is the project's workspace directory.
func (m *Manager) CreateSession(projectID, workspacePath string) (*SessionInfo, error) {
	return m.CreateSessionFromDraft(NewSessionDraft(projectID, nil), workspacePath)
}

// CreateSessionFromDraft commits a prepared execution workspace into a runtime
// session; managed Git provisioning is the caller's responsibility, and the
// workspace/branch cannot change after creation.
func (m *Manager) CreateSessionFromDraft(draft SessionDraft, repositoryPath string) (*SessionInfo, error) {
	projectID, id := draft.ProjectID, draft.ID
	if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, "\\/\x00") || id == "." || id == ".." {
		return nil, fmt.Errorf("invalid session draft identity: %q", id)
	}
	binding, err := NormalizeWorkspaceBinding(projectID, repositoryPath, draft.WorkspaceBinding)
	if err != nil {
		return nil, fmt.Errorf("validate session draft: %w", err)
	}
	m.mu.RLock()
	_, exists := m.sessions[id]
	m.mu.RUnlock()
	if exists {
		return nil, fmt.Errorf("session draft identity already exists: %s", id)
	}
	workspacePath := repositoryPath
	if binding != nil {
		workspacePath = binding.WorkspacePath
	}
	// Per-phase timing (DEBUG only): field session-creation latency is driven
	// by MCP gateways, large skill directories, or slow filesystems unit tests
	// never see.
	overallStart := time.Now()

	var phaseT0, phaseT1 time.Time
	debugTiming := strings.EqualFold(m.logLevelValue(), "DEBUG")
	phaseStart := func() {
		if debugTiming {
			phaseT0 = time.Now()
		}
	}
	phaseMark := func(name string) {
		if debugTiming {
			phaseT1 = time.Now()
			m.log().Debug("create_session phase", "phase", name, "elapsed_ms", phaseT1.Sub(phaseT0).Milliseconds(), "session_id", id)
			phaseT0 = phaseT1
		}
	}

	phaseStart()
	// For No Project, each session gets its own isolated workspace.
	if projectID == project.NoProjectID {
		workspacePath = config.NoProjectSessionWorkspace(m.agentDir, id)
		if absPath, absErr := filepath.Abs(workspacePath); absErr == nil {
			workspacePath = absPath
		} else {
			m.log().Warn("failed to resolve absolute workspace path for new session",
				"session_id", id, "path", workspacePath, "error", absErr)
		}
		// MkdirAllReal: refuse a dangling or swapped-in symlink (review finding #101).
		if err := safeio.MkdirAllReal(workspacePath, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create per-session workspace: %w", err)
		}
	}
	phaseMark("workspace_setup")

	logger, logFile, err := m.createSessionLogger(projectID, id)
	if err != nil {
		return nil, fmt.Errorf("failed to create session logger: %w", err)
	}
	phaseMark("logger")

	emitter := NewEventEmitter(id, m.emitFunc)
	// Record each emitted tool_call_id so the desktop confirmation callback can
	// attach the matching id to the tool_confirm payload.
	emitter.SetToolCallIDSink(func(tool, toolCallID string) {
		m.lastToolCallIDs.Store(id, toolCallIDEntry{id: toolCallID, tool: tool})
	})
	// Annotate agent metrics with the Model Profiles profile the session runs under.
	modelProfiles := m.modelProfile()
	emitter.SetModelProfile(modelProfiles)

	m.mu.RLock()
	factory := m.orchestratorFactory
	persistFn := m.tokenPersist
	ts := m.taskStore
	maxSumLen := m.maxSummaryLen
	m.mu.RUnlock()

	if persistFn != nil {
		emitter.SetTokenPersist(func(inputTokens, outputTokens int, model, family string, fillPercent float64) {
			persistFn(id, inputTokens, outputTokens, model, family, fillPercent)
		})
	}

	var bbFactory core.BlackboardFactory
	var adapter *TaskStoreAdapter
	if ts != nil {
		adapter = NewTaskStoreAdapter(ts)
		sessionID := id // capture for closure
		emitFunc := m.emitFunc
		bbFactory = func(taskID string) orchestration.Blackboard {
			var pbb *PersistentBlackboard
			if maxSumLen > 0 {
				pbb = NewPersistentBlackboard(taskID, sessionID, adapter, logger, orchestration.WithMaxSummaryLen(maxSumLen))
			} else {
				pbb = NewPersistentBlackboard(taskID, sessionID, adapter, logger)
			}
			m.trackBlackboard(pbb)
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

	dumpFile, stepDumpTracker := m.openDumpArtifacts(projectID, id)

	// The factory runs outside the lock — it can be slow.
	phaseStart()
	orchestrator, err := factory(emitter, logger, workspacePath, bbFactory, dumpFile, stepDumpTracker)
	if err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		if dumpFile != nil {
			_ = dumpFile.Close()
		}
		if stepDumpTracker != nil {
			_ = stepDumpTracker.CloseAll()
		}
		return nil, fmt.Errorf("failed to create orchestrator: %w", err)
	}
	phaseMark("orchestrator_build")

	// No Project mode: disable index-dependent tools (no vector index exists
	// without a project).
	if projectID == project.NoProjectID {
		orchestrator.SetNoProjectMode()
	}

	if adapter != nil {
		orchestrator.SetTaskStore(adapter)
		emitFn := m.emitFunc
		capturedSessionID := id
		orchestrator.SetBlackboardRestoreFunc(func(taskID, sessionID string, store core.TaskPersistence, logger *slog.Logger, opts ...orchestration.MapBlackboardOption) (core.PersistableBlackboard, error) {
			pbb, err := m.restoreBlackboardTracked(taskID, sessionID, store, logger, opts...)
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
	tempDir := sessionTempDir(m.agentDir, projectID, id)
	// MkdirAllReal: refuse a dangling or swapped-in symlink (review finding #101).
	if err := safeio.MkdirAllReal(tempDir, 0o755); err != nil {
		m.log().Warn("failed to create session temp directory", "session_id", id, "temp_dir", tempDir, "error", err)
	}

	session := &Session{
		ID:               id,
		ProjectID:        projectID,
		Name:             "Session " + safeSessionPrefix(id), // Default name using first 8 chars of UUID
		CreatedAt:        time.Now().UTC(),
		Archived:         false,
		ProjectPath:      repositoryPath,
		workspaceBinding: binding,
		WorkspacePath:    workspacePath,
		TempDir:          tempDir,
		orchestrator:     orchestrator,
		emitter:          emitter,
		logFile:          logFile,
		dumpFile:         dumpFile,
		stepDumpTracker:  stepDumpTracker,
		active:           false,
	}

	m.mu.Lock()
	m.sessions[id] = session
	m.mu.Unlock()

	m.emitFunc(Event{
		SessionID: id,
		Type:      "session_created",
		Data: SessionCreatedData{
			ID:        id,
			Name:      session.Name,
			CreatedAt: session.CreatedAt,
		},
	})

	// Log total CreateSession wall-clock time; >250ms is a WARN so a
	// regression in this critical-path method is visible by default.
	totalElapsed := time.Since(overallStart)
	switch {
	case totalElapsed > time.Second:
		m.log().Warn("create_session slow (>1s)", "elapsed_ms", totalElapsed.Milliseconds(), "session_id", id, "project_id", projectID)
	case totalElapsed > 250*time.Millisecond:
		m.log().Warn("create_session slow (>250ms)", "elapsed_ms", totalElapsed.Milliseconds(), "session_id", id, "project_id", projectID)
	default:
		m.log().Debug("create_session complete", "elapsed_ms", totalElapsed.Milliseconds(), "session_id", id, "project_id", projectID)
	}

	return &SessionInfo{
		ID:               session.ID,
		ProjectID:        projectID,
		WorkspaceBinding: session.WorkspaceBinding(),
		Name:             session.Name,
		CreatedAt:        session.CreatedAt.Format(time.RFC3339),
		LastActiveAt:     session.CreatedAt.Format(time.RFC3339),
		Archived:         session.Archived,
		Pinned:           session.Pinned,
		Active:           false,
	}, nil
}

// createSessionLogger creates a logger for a specific session.
// Returns the logger, the file handle (for cleanup), and an error.
// See specs/domains/session-lifecycle.md (§ Link-Safe Creation of Session
// File Artifacts).
func (m *Manager) createSessionLogger(projectID, sessionID string) (*slog.Logger, *os.File, error) {
	logDir := config.SessionLogsDir(m.agentDir, projectID, sessionID)
	// MkdirAllRealWithin makes the agent dir the containment boundary: an
	// escaping link planted anywhere in the path is refused, aborting session
	// creation instead of redirecting the log out of ~/.c0wrk (review finding
	// #59). The agent dir is the boundary because the session chain may not
	// exist yet at first lazy creation; in-tree links stay operator intent.
	if err := safeio.MkdirAllRealWithin(m.agentDir, logDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// OpenFileNoFollow fails a final-component symlink with ELOOP; Windows has
	// no O_NOFOLLOW, tempered by symlink creation requiring elevated rights.
	logFile := config.SessionLogPath(m.agentDir, projectID, sessionID)
	file, err := safeio.OpenFileNoFollow(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open log file: %w", err)
	}

	handler := slog.NewJSONHandler(file, &slog.HandlerOptions{
		Level: parseSlogLevel(m.logLevelValue()),
	})
	return slog.New(handler), file, nil
}

// openDumpArtifacts creates the DEBUG-mode LLM dump file and the per-step dump
// tracker. Both are best-effort: a failure warns and disables the artifact
// (nil) instead of failing the session. The dump file is opened O_NOFOLLOW —
// plain safeio.OpenFile would follow a final-component symlink before its
// regularity fstat could catch the redirection.
func (m *Manager) openDumpArtifacts(projectID, sessionID string) (*os.File, *orchestration.StepDumpTracker) {
	if !strings.EqualFold(m.logLevelValue(), "DEBUG") {
		return nil, nil
	}
	dumpPath := config.SessionDumpPath(m.agentDir, projectID, sessionID)
	if mkErr := safeio.MkdirAllRealWithin(m.agentDir, filepath.Dir(dumpPath), 0o755); mkErr != nil {
		m.log().Warn("failed to create dumps directory", "session_id", sessionID, "error", mkErr)
		return nil, nil
	}
	dumpFile, err := safeio.OpenFileNoFollow(dumpPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		m.log().Warn("failed to create LLM dump file", "session_id", sessionID, "error", err)
		return nil, nil
	}
	// Pre-create the "steps" subdirectory as a real directory: the tracker's
	// own os.MkdirAll would follow a planted symlink; a refusal disables step dumps.
	stepDumpDir := config.SessionStepDumpDir(m.agentDir, projectID, sessionID)
	if mkErr := safeio.MkdirAllRealWithin(m.agentDir, stepDumpDir, 0o755); mkErr != nil {
		m.log().Warn("failed to create step dump directory", "session_id", sessionID, "error", mkErr)
		return dumpFile, nil
	}
	return dumpFile, orchestration.NewStepDumpTracker(stepDumpDir, m.log().With("session_id", sessionID))
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

// logLevelValue returns the current log level under m.mu: SetLogLevel writes
// from a Wails RPC goroutine while sessions are created on others, so reads
// must take the same lock.
func (m *Manager) logLevelValue() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.logLevel
}

// DeleteSession removes an in-memory session: cancel + join the running task,
// close resources, purge tracking state, remove files, emit session_deleted.
// Deletion never RESTORES — a managed restore would re-provision the tree the
// caller may have just released (specs/domains/session-lifecycle.md);
// store-only sessions are the caller's fallback (FrontendAPI).
func (m *Manager) DeleteSession(id string) error {
	// Capture the logger BEFORE m.mu: the close-error branches run under the
	// WRITE lock and m.log() takes RLock — sync.RWMutex is not reentrant.
	logger := m.log()
	m.mu.Lock()
	session, exists := m.sessions[id]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("session not found: %s", id)
	}

	// Cancel any active task and grab the done channels. The in-flight
	// compaction flow is cancelled too and flagged via deleting: otherwise a
	// compacting session is torn down while runSessionCompaction still runs,
	// and its auto-resume would resurrect the deleted session.
	session.mu.Lock()
	var doneCh chan struct{}
	if session.active && session.cancel != nil {
		session.cancel()
		doneCh = session.done
	}
	session.deleting = true
	var compactDone chan struct{}
	if session.compactCancel != nil {
		session.compactCancel()
		compactDone = session.compactDone
	}
	session.mu.Unlock()
	m.mu.Unlock()

	// Wait for the task goroutine to finish so events are fully flushed.
	if doneCh != nil {
		select {
		case <-doneCh:
		case <-time.After(m.stopTimeout):
			logger.Warn("timed out waiting for task goroutine to stop", "session_id", id)
		}
	}
	// Join the compaction flow BEFORE teardown: it drives this orchestrator's
	// compaction and must be gone before cleanup. Bounded by stopTimeout.
	if compactDone != nil {
		select {
		case <-compactDone:
		case <-time.After(m.stopTimeout):
			logger.Warn("timed out waiting for manual compaction goroutine to stop", "session_id", id)
		}
	}

	m.mu.Lock()
	session.mu.Lock()
	// Close per-step dump files via orchestrator cleanup (idempotent).
	if session.orchestrator != nil {
		session.orchestrator.Cleanup()
	}
	if session.stepDumpTracker != nil {
		if err := session.stepDumpTracker.CloseAll(); err != nil {
			logger.Warn("failed to close per-step dump files", "session_id", id, "error", err)
		}
	}
	if session.logFile != nil {
		if err := session.logFile.Close(); err != nil {
			logger.Warn("failed to close session log file", "session_id", id, "error", err)
		}
	}
	if session.dumpFile != nil {
		if err := session.dumpFile.Close(); err != nil {
			logger.Warn("failed to close session LLM dump file", "session_id", id, "error", err)
		}
	}
	session.mu.Unlock()
	delete(m.sessions, id)
	m.mu.Unlock()

	// Stop the session's blackboard persistence workers (see
	// stopSessionBlackboards).
	m.stopSessionBlackboards(id)

	m.fileTracker.PurgeSession(id)

	// Remove all internal session files (logs, dumps, temp, plans). For No
	// Project this includes the isolated per-session workspace; regular
	// projects share a project-scoped workspace removed only with the project.
	// ArchiveSession keeps these files so an archived session restores.
	sessionDir := config.SessionDir(m.agentDir, session.ProjectID, id)
	if err := os.RemoveAll(sessionDir); err != nil {
		m.log().Warn("failed to remove session directory", "session_id", id, "dir", sessionDir, "error", err)
	}

	m.emitFunc(Event{
		SessionID: id,
		Type:      "session_deleted",
		Data: SessionDeletedData{
			ID: id,
		},
	})

	m.lastToolCallIDs.Delete(id)

	return nil
}

// LastToolCallID returns the most recently emitted tool_call_id for a session
// with its tool name, or empty strings if none — the confirmation callback's
// tool_confirm correlation (specs/contracts/event-catalog.md).
func (m *Manager) LastToolCallID(sessionID string) (id, tool string) {
	if v, ok := m.lastToolCallIDs.Load(sessionID); ok {
		if entry, ok := v.(toolCallIDEntry); ok {
			return entry.id, entry.tool
		}
	}
	return "", ""
}

// GetSession returns a session by ID, lazily restoring it from the persistent
// store if it is not in memory.
func (m *Manager) GetSession(id string) (*Session, bool) {
	sess, err := m.getOrRestoreSession(id)
	if err != nil {
		m.log().Warn("failed to restore session", "session_id", id, "error", err)
		return nil, false
	}
	return sess, sess != nil
}

// HasSession reports whether the session is live in memory WITHOUT GetSession's
// lazy restore — deletion flows use it so a lookup never resurrects
// (and re-provisions) a session (specs/domains/session-worktrees.md).
func (m *Manager) HasSession(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.sessions[id]
	return exists
}

// SessionProjectID returns the project ID of a session LIVE IN MEMORY without
// GetSession's lazy restore; use it when a store row may be missing but a
// resident session's identity is still authoritative.
func (m *Manager) SessionProjectID(id string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, exists := m.sessions[id]
	if !exists || sess == nil {
		return "", false
	}
	return sess.ProjectID, true
}

// GetSessionWorkspacePath returns the workspace path for a session.
func (m *Manager) GetSessionWorkspacePath(id string) (string, bool) {
	sess, ok := m.GetSession(id)
	if !ok {
		return "", false
	}
	return sess.WorkspacePath, true
}

// WorkspacePathFor returns a session's workspace path WITHOUT the full lazy
// restore: no orchestrator build, no session insertion, no event wiring —
// read-only RPC surfaces must never trigger a restore whose store reads can
// park on a saturated SQLite pool (specs/domains/session-worktrees.md). The
// ctx bounds the session-row read; the No Project derivation mirrors
// getOrRestoreSession, including directory creation.
func (m *Manager) WorkspacePathFor(ctx context.Context, id string) (string, bool) {
	m.mu.RLock()
	sess, ok := m.sessions[id]
	var workspacePath string
	if ok {
		workspacePath = sess.WorkspacePath
	}
	m.mu.RUnlock()
	if ok {
		return workspacePath, true
	}

	// Read-only slow path: snapshot store + resolver under the lock, use outside.
	m.mu.RLock()
	store := m.sessionStore
	resolver := m.projectResolver
	m.mu.RUnlock()
	if store == nil || resolver == nil {
		return "", false
	}

	info, err := store.LoadSession(ctx, id)
	if err != nil || info == nil {
		return "", false
	}

	workspacePath, err = resolver(info.ProjectID)
	if err != nil {
		return "", false
	}

	binding, err := NormalizeWorkspaceBinding(info.ProjectID, workspacePath, info.WorkspaceBinding)
	if err != nil {
		return "", false
	}
	if binding != nil {
		workspacePath = binding.WorkspacePath
	}

	// No Project: re-derive the per-session workspace like getOrRestoreSession.
	if info.ProjectID == project.NoProjectID {
		workspacePath = config.NoProjectSessionWorkspace(m.agentDir, id)
		if absPath, absErr := filepath.Abs(workspacePath); absErr == nil {
			workspacePath = absPath
		}
		// MkdirAllReal: refuse a dangling or swapped-in symlink (review finding #101).
		if mkErr := safeio.MkdirAllReal(workspacePath, 0o755); mkErr != nil {
			m.log().Warn("failed to ensure per-session workspace on path lookup",
				"session_id", id, "error", mkErr)
		}
	}
	return workspacePath, true
}

// ListSessions returns metadata for all sessions, sorted by LastActiveAt descending.
func (m *Manager) ListSessions() []SessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		s.mu.Lock()
		lastActive := s.LastActiveAt
		if lastActive.IsZero() {
			lastActive = s.CreatedAt
		}
		sessions = append(sessions, SessionInfo{
			ID:               s.ID,
			ProjectID:        s.ProjectID,
			WorkspaceBinding: s.WorkspaceBinding(),
			Name:             s.Name,
			CreatedAt:        s.CreatedAt.Format(time.RFC3339),
			LastActiveAt:     lastActive.Format(time.RFC3339),
			Archived:         s.Archived,
			Pinned:           s.Pinned,
			Active:           s.active,
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
	archiving := !session.Archived
	// The toggle's target is fixed HERE, under this lock: the stop/finish
	// block below can wait up to stopTimeout, so Wails can interleave a
	// duplicate ArchiveSession there. Re-flipping would double-apply the
	// concurrent flip and desync the store toggle; the captured target is idempotent.
	target := archiving
	session.mu.Unlock()

	// Archiving a session with a running or unfinished task first stops and
	// settles it: an archived session is read-only (specs/domains/session-lifecycle.md).
	if archiving {
		session.mu.Lock()
		var doneCh chan struct{}
		if session.active && session.cancel != nil {
			session.cancel()
			doneCh = session.done
		}
		tempDir := session.TempDir
		session.mu.Unlock()

		removeTempDir := func() {
			if tempDir == "" {
				return
			}
			if err := os.RemoveAll(tempDir); err != nil {
				m.log().Warn("failed to remove session temp directory on archive", "session_id", id, "temp_dir", tempDir, "error", err)
			}
		}

		switch doneCh {
		case nil:
			// No running task: the temp dir is quiescent and safe to remove.
			removeTempDir()
		default:
			select {
			case <-doneCh:
				removeTempDir()
			case <-time.After(m.stopTimeout):
				// Cancellation is cooperative: never remove a possibly still-in-use
				// temp dir; defer until the goroutine actually finishes.
				m.log().Warn("timed out waiting for task goroutine to stop before archiving; deferring temp cleanup", "session_id", id)
				if !m.spawnBackgroundNamed("deferred-temp-dir-removal session="+id, func() {
					<-doneCh
					removeTempDir()
				}) {
					// Shutdown already closed the tracker; leave the directory to OS
					// temp cleanup rather than racing a torn-down manager.
					m.log().Debug("shutdown in progress; skipping deferred temp dir removal", "session_id", id)
				}
			}
		}

		// Discard any unfinished task: an archived session keeps no resumable state.
		if err := m.CancelUnfinishedTask(id); err != nil {
			m.log().Warn("failed to discard unfinished task before archiving", "session_id", id, "error", err)
		}
	}

	session.mu.Lock()
	session.Archived = target
	archived := session.Archived
	session.mu.Unlock()

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

// PinSession toggles the pinned flag.
func (m *Manager) PinSession(id string) error {
	session, err := m.getOrRestoreSession(id)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", id)
	}

	session.mu.Lock()
	session.Pinned = !session.Pinned
	pinned := session.Pinned
	session.mu.Unlock()

	eventType := "session_unpinned"
	if pinned {
		eventType = "session_pinned"
	}
	m.emitFunc(Event{
		SessionID: id,
		Type:      eventType,
		Data: SessionPinnedData{
			ID:     id,
			Pinned: pinned,
		},
	})

	return nil
}

// emitResumableIfUnfinished, GetBlackboardState and the BlackboardState type
// live in manager_execution.go (W-21 file split; types shared with this file).

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

// IsCompacting returns whether a manual context compaction is in flight.
func (s *Session) IsCompacting() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.compacting
}

// DumpFile returns a duplicated handle for the session's LLM dump file (nil
// when DEBUG is disabled); the caller owns and closes it. Duping gives
// background goroutines handles that survive session deletion.
func (s *Session) DumpFile() *os.File {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dumpFile == nil {
		return nil
	}
	f, err := dupFile(s.dumpFile)
	if err != nil {
		return nil
	}
	return f
}

// EmitSessionEvent emits a session-scoped event through the manager's emit
// pipeline (persister included) for flows outside a live session goroutine.
func (m *Manager) EmitSessionEvent(sessionID, eventType string, data any) {
	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      eventType,
		Data:      data,
	})
}

// Shutdown closes all sessions and releases resources: running tasks are
// checkpointed as paused (resumable), manager-owned background goroutines are
// joined and every tracked blackboard's persistence worker stopped under one
// stopTimeout budget (specs/domains/session-lifecycle.md, Invariants). The
// conductor's compositeTrajectoryStore is NOT tracked here: it is created per
// run, and the run's final Flush drains it before the goroutine returns.
func (m *Manager) Shutdown() {
	// Mark shutdown BEFORE cancelling anything: the send/resume goroutines key
	// off this when their context is cancelled.
	m.shuttingDown.Store(true)

	m.shutdownCancel()

	// Every session goes into pendingList, not just active ones: idle sessions
	// still hold open log/dump handles that must close before process exit or
	// Windows TempDir cleanup fails. doneCh is waited on only for in-flight tasks.
	type pending struct {
		session       *Session
		doneCh        chan struct{}
		compactDoneCh chan struct{}
	}
	m.mu.Lock()
	pendingList := make([]pending, 0, len(m.sessions))
	// activeIDs: still-running tasks, checkpointed as paused once goroutines settle.
	activeIDs := make([]string, 0, len(m.sessions))
	for id, session := range m.sessions {
		session.mu.Lock()
		// Close per-step dump files via orchestrator cleanup (idempotent).
		if session.orchestrator != nil {
			session.orchestrator.Cleanup()
		}
		if session.active && session.cancel != nil {
			session.cancel()
			activeIDs = append(activeIDs, id)
		}
		// Cancel an in-flight manual compaction too: its flow goroutine must not
		// outlive Shutdown (its LLM calls and auto-resume would run against a
		// torn-down backend); this unblocks it.
		if session.compactCancel != nil {
			session.compactCancel()
		}
		doneCh := session.done
		compactDoneCh := session.compactDone
		session.mu.Unlock()

		pendingList = append(pendingList, pending{session: session, doneCh: doneCh, compactDoneCh: compactDoneCh})

		// Remove from map
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	// Wait for task and compaction goroutines outside any lock; the compaction
	// flow skips its shutdown-gated tail phases and exits.
	taskWaitStart := time.Now()
	slowWaits := 0
	// All waits share ONE stopTimeout budget: N stuck goroutines cost at most
	// one stopTimeout of frozen quit; past the budget, remaining waits are skipped.
	budgetDeadline := time.Now().Add(m.stopTimeout)
	budgetExhaustedWarned := false
	waitShutdownGoroutine := func(kind string, doneCh chan struct{}) {
		if doneCh == nil {
			return
		}
		remaining := time.Until(budgetDeadline)
		if remaining <= 0 {
			slowWaits++
			if !budgetExhaustedWarned {
				budgetExhaustedWarned = true
				m.log().Warn("shutdown: task goroutine wait budget exhausted; skipping remaining goroutine waits",
					"ms", time.Since(taskWaitStart).Milliseconds())
			}
			return
		}
		waitStart := time.Now()
		select {
		case <-doneCh:
		case <-time.After(remaining):
		}
		if time.Since(waitStart) >= slowShutdownWaitThreshold {
			slowWaits++
			m.log().Warn("shutdown: slow "+kind+" goroutine wait", "ms", time.Since(waitStart).Milliseconds())
		}
	}
	for _, p := range pendingList {
		waitShutdownGoroutine("task", p.doneCh)
		waitShutdownGoroutine("compaction", p.compactDoneCh)
	}
	m.log().Info("shutdown: task goroutine wait complete",
		"sessions", len(pendingList), "slow_waits", slowWaits,
		"ms", time.Since(taskWaitStart).Milliseconds())

	// Join background goroutines BEFORE closing session file handles: title
	// generation writes through a session dump file. Bounded by stopTimeout.
	m.stopBackground()

	// Close file handles for ALL sessions (idle and active) after any active
	// goroutines have stopped.
	for _, p := range pendingList {
		p.session.mu.Lock()
		if p.session.stepDumpTracker != nil {
			_ = p.session.stepDumpTracker.CloseAll()
		}
		if p.session.logFile != nil {
			_ = p.session.logFile.Close()
		}
		if p.session.dumpFile != nil {
			_ = p.session.dumpFile.Close()
		}
		p.session.mu.Unlock()
	}

	// Checkpoint still-running tasks as paused AFTER the goroutines settled,
	// so no pending persist races.
	for _, id := range activeIDs {
		m.persistPauseIfUnfinished(id)
	}
}

// convertChatMessagesToLLM converts stored ChatMessages to llm.Message format,
// reconstructing the conversation history exactly as the live session saw it:
// user rows re-preprocessed, consecutive assistant rows collapsed to the final
// output, error/cancelled rows becoming the live outcome notes, other roles
// skipped (specs/domains/session-lifecycle.md, Conversation History).
func (m *Manager) convertChatMessagesToLLM(msgs []ChatMessage, workspacePath string) []llm.Message {
	result := make([]llm.Message, 0, len(msgs))
	appendAssistant := func(lm llm.Message) {
		if len(result) > 0 && result[len(result)-1].Role == "assistant" {
			result[len(result)-1] = lm
			return
		}
		result = append(result, lm)
	}
	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			// Skill and agent names are unknown at restore time, so only the
			// @file normalization re-applies; relative paths resolve against
			// the session workspace, mirroring live preprocessing.
			lm := llm.Message{Role: "user", Content: core.PreprocessMessageText(msg.Content, nil, nil, nil, workspacePath)}
			// Reconstruct image content blocks from persisted metadata (thumbnail + path).
			if blocks := reconstructImageBlocks(msg.Metadata, m.log()); len(blocks) > 0 {
				lm.ContentBlocks = blocks
			}
			result = append(result, lm)
		case "assistant":
			lm := llm.Message{Role: "assistant", Content: msg.Content}
			if msg.ReasoningContent != nil {
				lm.ReasoningContent = *msg.ReasoningContent
			}
			if msg.ToolCalls != nil {
				var toolCalls []llm.ToolCall
				if err := json.Unmarshal(*msg.ToolCalls, &toolCalls); err == nil {
					lm.ToolCalls = toolCalls
				}
			}
			appendAssistant(lm)
		case "error":
			appendAssistant(llm.Message{Role: "assistant", Content: core.HistoryNoteFailed(extractPersistedError(msg))})
		case "task_cancelled":
			appendAssistant(llm.Message{Role: "assistant", Content: core.HistoryNoteCancelled})
		case compactMarkerRole:
			// Compaction marker: the snapshot IS the history at that point —
			// earlier rows drop, later ones append; no-snapshot markers are
			// no-ops (specs/domains/memory/compaction.md).
			if snapshot := compactedHistoryFromMarker(msg.Metadata); snapshot != nil {
				result = append(result[:0], snapshot...)
			}
		default:
			m.log().Debug("convertChatMessagesToLLM: skipping non-conversational role", "role", msg.Role)
		}
	}
	return result
}

// reconstructImageBlocks rebuilds LLM image content blocks from the thumbnail
// + path persisted in a user message's Metadata — the restart half of the
// image lifecycle (specs/domains/session-lifecycle.md, File Attachments).
// Missing or unreadable files are skipped so one vanished image cannot
// break the whole history restore.
func reconstructImageBlocks(metadata json.RawMessage, log *slog.Logger) []llm.ContentBlock {
	if len(metadata) == 0 {
		return nil
	}
	var md StoredImagesMetadata
	if err := json.Unmarshal(metadata, &md); err != nil {
		log.Debug("reconstructImageBlocks: failed to parse metadata", "error", err)
		return nil
	}
	if len(md.Images) == 0 {
		return nil
	}
	blocks := make([]llm.ContentBlock, 0, len(md.Images))
	for _, img := range md.Images {
		raw, err := safeio.ReadFile(img.Path)
		if err != nil {
			log.Warn("reconstructImageBlocks: failed to read image file; skipping", "path", img.Path, "error", err)
			continue
		}
		blocks = append(blocks, llm.ContentBlock{
			Type:      "image",
			ImageB64:  base64.StdEncoding.EncodeToString(raw),
			MediaType: img.MediaType,
		})
	}
	if len(blocks) == 0 {
		return nil
	}
	return blocks
}

// extractPersistedError extracts the error text from a persisted "error" row.
// The event persister stores ErrorData as metadata JSON ({"error": "..."}).
func extractPersistedError(msg ChatMessage) string {
	var data struct {
		Error string `json:"error"`
	}
	if len(msg.Metadata) > 0 {
		if err := json.Unmarshal(msg.Metadata, &data); err == nil && data.Error != "" {
			return data.Error
		}
	}
	return "unknown error"
}

// markitdownPythonPath resolves the managed venv interpreter used by the
// attachment converter for vision-assisted conversion. The probe runs on each
// converter-init attempt, but the resolved path is frozen into the converter
// once init succeeds; an incomplete install leaves vision disabled for this
// app run (plain conversion is unaffected).
func (m *Manager) markitdownPythonPath() string {
	return toolmanager.VenvPythonPath(config.ToolsDir(m.agentDir))
}

// resolveSessionVision returns markitdown vision connection parameters for the
// model currently active on the session's orchestrator, or nil when the
// session has no orchestrator (not yet restored) or the active model must not
// caption. Called PER DOCUMENT by AttachFiles.
func (m *Manager) resolveSessionVision(s *Session) *markitdown.VisionOptions {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	orch := s.orchestrator
	s.mu.Unlock()
	if orch == nil {
		return nil
	}
	return orch.ResolveVisionOptions()
}
