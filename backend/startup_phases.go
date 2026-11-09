// Startup phases relocated from the desktop binding layer (migration stage
// P-14): subsystem orchestration lives here in transport-neutral form —
// every UI touchpoint (event emit, splash denominator, native window reveal,
// fatal dialog + quit, batched terminal output) arrives as an injected hook,
// so this package keeps zero Wails imports (§9.1 of the desktop-layering
// audit). The desktop host binds the hooks and calls the exported wrappers.

package backend

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/review"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/terminal"
	"github.com/v0lka/c0wrk/core/toolmanager"
)

// SafeGo runs fn in a goroutine that recovers from any panic and logs it —
// a startup-phase failure degrades instead of crashing the app. The caller
// owns WaitGroup accounting (defer wg.Done() inside fn; fn's defers unwind
// before this recover sees the panic).
func SafeGo(log *slog.Logger, label string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic during startup", "phase", label, "panic", r)
			}
		}()
		fn()
	}()
}

// ToolFatalActions carries the fatal-exit seam of the tool-install flow.
type ToolFatalActions struct {
	Dialog func(ctx context.Context, message string)
	Quit   func(ctx context.Context)
}

// ToolPhaseDeps carries the transport seams of the tool-install startup
// phases: production binds the desktop host's hooks (Wails event emit, the
// native splash reveal, the splash-denominator store, the fatal modal+quit),
// tests bind recorders, so the two-pass flow stays exercisable without a UI.
type ToolPhaseDeps struct {
	// Progress forwards one manager progress report to the frontend, stamping
	// the splash denominator (specs/contracts/event-catalog.md,
	// tool_manager:progress).
	Progress func(toolmanager.ToolProgress)
	// ShowWindow performs the safety-net reveal before the tool-install
	// splash and backend:ready need the window.
	ShowWindow func(ctx context.Context)
	// SetToolsTotal records the splash denominator: Wails drops events
	// emitted before the webview subscribes, so tool_manager:start can be
	// lost and every progress event must self-describe the total.
	SetToolsTotal func(total int64)
	// Emit delivers one frontend event.
	Emit func(eventName string, data ...any)
	// Fatal carries the fatal-exit actions (native error dialog + quit).
	Fatal ToolFatalActions
}

// toolEnsurer is the manager seam of the startup tool orchestration:
// production binds a *toolmanager.Manager, tests bind a fake, so the
// two-pass flow is exercisable without the filesystem or the network.
type toolEnsurer interface {
	NeedsInstall() ([]toolmanager.ToolSpec, error)
	EnsureCriticalTools(ctx context.Context, opts toolmanager.EnsureOptions) ([]toolmanager.ToolStatus, error)
}

// initTools ensures the managed tools (rg, uv, markitdown) exist in
// <agentDir>/tools/ before the app becomes usable; the two-pass reconcile and
// its fatal/toast outcomes are specified in specs/domains/tool-manager.md
// (ADR-100). ("", false) on the fatal path so initConfigAndDeps aborts.
func initTools(ctx context.Context, log *slog.Logger, deps ToolPhaseDeps) (toolsBinPath string, toolsInstalled bool) {
	agentDir := config.AgentDir()

	toolsDir := config.ToolsDir(agentDir)
	binDir := config.ToolsBinDir(agentDir)
	pythonDir := config.ToolsPythonDir(agentDir)

	mgr := toolmanager.NewManager(toolsDir, binDir, pythonDir, log, toolmanager.ManagerConfig{
		ProgressCallback: deps.Progress,
	})

	// Safety-net reveal before the tool-install splash and backend:ready need
	// the window: it is already visible (main.go creates it so on purpose),
	// but a window hidden for any other reason must come back — showWindow
	// also raises/focuses (see App.showWindow's activation matrix).
	deps.ShowWindow(ctx)

	installed, ok := reconcileToolsWithSplash(ctx, log, mgr, deps)
	if !ok {
		// Fatal path already showed the modal and quit; the empty bin path
		// makes initConfigAndDeps report toolsOK=false so Startup aborts.
		return "", false
	}
	return mgr.PrependToPATH(), installed
}

// InitTools forwards to initTools for the desktop startup adapter.
func InitTools(ctx context.Context, log *slog.Logger, deps ToolPhaseDeps) (string, bool) {
	return initTools(ctx, log, deps)
}

// reconcileToolsWithSplash runs the two-pass managed-tool reconciliation and
// owns the splash lifecycle (tool_manager:start before installable work,
// tool_manager:done exactly once). The pass structure, blocking network
// retry, fatal modal+quit and the single toast degradation live in
// specs/domains/tool-manager.md (ADR-100); ok=false aborts startup.
func reconcileToolsWithSplash(ctx context.Context, log *slog.Logger, mgr toolEnsurer, deps ToolPhaseDeps) (installed, ok bool) {
	// Early detection: show the splash before any work whenever tools are
	// (or may be) missing.
	needed, needsErr := mgr.NeedsInstall()
	toolsMissing := needsErr != nil // unknown counts as missing until probed
	startEmitted := false
	if needsErr != nil {
		log.Warn("failed to check tool install status, showing splash as fallback", "error", needsErr)
		deps.Emit(EventToolManagerStart, map[string]any{
			"tools": []map[string]string{},
		})
		startEmitted = true
	} else if len(needed) > 0 {
		toolsMissing = true
		toolNames := make([]map[string]string, len(needed))
		for i, t := range needed {
			toolNames[i] = map[string]string{"name": t.Name, "version": t.Version}
		}
		// Every progress event carries the splash denominator — the start
		// event itself can be lost to the webview-subscription race.
		deps.SetToolsTotal(int64(len(needed)))
		deps.Emit(EventToolManagerStart, map[string]any{
			"tools": toolNames,
		})
		startEmitted = true
	}

	// Pass one: strictly local work. Per-tool failures surface as not-Ready
	// statuses; only structural failures (directories, disk space, registry)
	// come back as an error.
	statuses, ensureErr := mgr.EnsureCriticalTools(ctx, toolmanager.EnsureOptions{AllowNetwork: false})
	if ensureErr == nil {
		offlineInstalled := 0
		var notReady []toolmanager.ToolStatus
		for _, s := range statuses {
			if s.Installed {
				offlineInstalled++
			}
			if !s.Ready {
				notReady = append(notReady, s)
			}
		}
		if len(notReady) == 0 {
			deps.Emit(EventToolManagerDone, map[string]any{
				"installed_count": offlineInstalled,
				"skipped_count":   len(statuses) - offlineInstalled,
			})
			return offlineInstalled > 0, true
		}
		if !startEmitted {
			// NeedsInstall saw a complete install but probing disagrees (e.g.
			// a stale binary behind a matching .versions entry) — the
			// blocking network pass needs its splash after all.
			toolNames := make([]map[string]string, len(notReady))
			for i, s := range notReady {
				toolNames[i] = map[string]string{"name": s.Tool.Name, "version": s.Tool.Version}
			}
			deps.SetToolsTotal(int64(len(notReady)))
			deps.Emit(EventToolManagerStart, map[string]any{
				"tools": toolNames,
			})
		}
	} else if !toolsMissing {
		// Structural failure over a complete install: nothing is missing, so
		// degrade to a toast instead of blocking or quitting.
		log.Error("tool reconciliation failed structurally", "error", ensureErr)
		deps.Emit(EventToolManagerDone, map[string]any{
			"installed_count": 0,
			"skipped_count":   0,
		})
		emitToolRuntimeError(deps.Emit, "Tool initialization failed: "+ensureErr.Error())
		return false, true
	}

	// Pass two: blocking network retry. The splash is up by now and done is
	// emitted exactly once after this pass — the frontend leaves the splash
	// on it.
	pendingNames := make([]string, 0, len(needed))
	if ensureErr == nil {
		for _, s := range statuses {
			if !s.Ready {
				pendingNames = append(pendingNames, s.Tool.Name)
			}
		}
	} else {
		for _, t := range needed {
			pendingNames = append(pendingNames, t.Name)
		}
	}
	log.Info("retrying tool install with network access (blocking)", "tools", pendingNames)

	netStatuses, netErr := mgr.EnsureCriticalTools(ctx, toolmanager.EnsureOptions{AllowNetwork: true})
	if netErr != nil {
		log.Error("network tool reconciliation failed structurally", "error", netErr)
		deps.Emit(EventToolManagerDone, map[string]any{
			"installed_count": 0,
			"skipped_count":   0,
		})
		deps.Fatal.Dialog(ctx, "c0wrk was unable to download required tools:\n\n"+netErr.Error())
		deps.Fatal.Quit(ctx)
		return false, false
	}

	netInstalled := 0
	var failed []toolmanager.ToolStatus
	for _, s := range netStatuses {
		if s.Installed {
			netInstalled++
		}
		if !s.Ready {
			failed = append(failed, s)
		}
	}
	deps.Emit(EventToolManagerDone, map[string]any{
		"installed_count": netInstalled,
		"skipped_count":   len(netStatuses) - netInstalled,
	})
	if len(failed) == 0 {
		return netInstalled > 0, true
	}

	failedNames := make([]string, 0, len(failed))
	for _, s := range failed {
		failedNames = append(failedNames, s.Tool.Name)
		log.Error("managed tool unavailable after network install", "tool", s.Tool.Name, "error", s.Err)
	}
	deps.Fatal.Dialog(ctx, "c0wrk was unable to install required tools:\n\n"+strings.Join(failedNames, ", "))
	deps.Fatal.Quit(ctx)
	return false, false
}

// emitToolRuntimeError raises a non-fatal toast about the managed tools —
// the degradation path for a structurally failed offline pass over an
// already-complete install (fatal outcomes go through ToolFatalActions).
func emitToolRuntimeError(emit func(eventName string, data ...any), message string) {
	emit(EventRuntimeError, map[string]string{
		"id":         uuid.New().String(),
		"message":    message,
		"error_code": "tool_install_failed",
	})
}

// initDatabase opens the shared SQLite connection; on failure it logs and
// returns nil — callers must tolerate a nil DB (downstream stores are nil
// too and behavior degrades rather than panicking).
func initDatabase(dbPath string, log *slog.Logger) *sql.DB {
	db, err := OpenDatabase(dbPath, log)
	if err != nil {
		log.Error("failed to open sqlite database", "error", err)
		return nil
	}
	return db
}

// InitDatabase forwards to initDatabase for the desktop startup adapter.
func InitDatabase(dbPath string, log *slog.Logger) *sql.DB {
	return initDatabase(dbPath, log)
}

// initTerminalManager constructs the PTY-backed terminal manager. Output is
// base64-encoded before emission — string(data) corrupts invalid UTF-8 split
// across read boundaries and json.Marshal replaces it with U+FFFD. userEnv
// carries config `terminal.env` entries; ${VAR} refs are expanded here and
// values are never logged — they may contain user secrets. ctx is the
// application lifetime context supplied by the host; emitBatched is the
// batched event funnel (terminal output rides the coalescing batcher).
func initTerminalManager(ctx context.Context, log *slog.Logger, userEnv map[string]string, emitBatched func(name string, args []any, coalesceKey string, barrier bool, sizeHint int)) *terminal.Manager {
	env := make(map[string]string, len(userEnv))
	for k, v := range userEnv {
		env[k] = config.ExpandEnvVars(v)
	}
	return terminal.NewManager(ctx, log,
		func(sessionID string, data []byte) {
			eventName := fmt.Sprintf("session:%s:terminal_output", sessionID)
			encoded := base64.StdEncoding.EncodeToString(data)
			emitBatched(eventName, []any{map[string]string{"data": encoded}}, "", false, len(encoded))
		},
		func(sessionID string) {
			// Natural shell exit: the UI keeps the instance mounted and
			// resurrects the shell lazily on next activation
			// (specs/contracts/event-catalog.md, terminal_exited).
			// Empty-object payload passes the frontend's null-payload filter.
			eventName := fmt.Sprintf("session:%s:terminal_exited", sessionID)
			emitBatched(eventName, []any{map[string]string{}}, "", false, 0)
		},
		env,
	)
}

// InitTerminalManager forwards to initTerminalManager for the desktop
// startup adapter.
func InitTerminalManager(ctx context.Context, log *slog.Logger, userEnv map[string]string, emitBatched func(name string, args []any, coalesceKey string, barrier bool, sizeHint int)) *terminal.Manager {
	return initTerminalManager(ctx, log, userEnv, emitBatched)
}

// initStores creates the project + session + review SQLite stores; the
// session store must precede the review store (FK references).
func initStores(db *sql.DB, log *slog.Logger) (*project.SQLiteProjectStore, *session.SQLiteSessionStore, *review.SQLiteReviewStore) {
	if db == nil {
		return nil, nil, nil
	}
	var projStore *project.SQLiteProjectStore
	if ps, err := project.NewSQLiteProjectStore(db); err != nil {
		log.Error("failed to init project store", "error", err)
	} else {
		projStore = ps
	}
	var sessStore *session.SQLiteSessionStore
	if s, err := session.NewSQLiteSessionStore(db); err != nil {
		log.Error("failed to init session store", "error", err)
	} else {
		sessStore = s
	}
	var reviewStore *review.SQLiteReviewStore
	if rs, err := review.NewSQLiteReviewStore(db); err != nil {
		log.Error("failed to init review store", "error", err)
	} else {
		rs.SetLogger(log)
		reviewStore = rs
	}
	return projStore, sessStore, reviewStore
}

// InitStores forwards to initStores for the desktop startup adapter.
func InitStores(db *sql.DB, log *slog.Logger) (*project.SQLiteProjectStore, *session.SQLiteSessionStore, *review.SQLiteReviewStore) {
	return initStores(db, log)
}

// preloadProjectsAndSessions emits projects + sessions from the most recent
// project before the slow NewApplication() call so the sidebar populates
// immediately; the returned list is reused by emitBackendReady.
//
// It deliberately does NOT set activeProjectID: that would make SwitchProject's
// idempotency guard reject the frontend's first call (which follows
// EventBackendReady) and skip vector index initialization — the frontend's
// SwitchProject sets it properly.
// See specs/domains/workspace.md (startup preload / first-SwitchProject
// idempotency contract).
func preloadProjectsAndSessions(projectMgr *project.Manager, sessStore *session.SQLiteSessionStore, log *slog.Logger, emit func(eventName string, data ...any)) []project.ProjectInfo {
	if projectMgr == nil {
		return nil
	}
	projects, err := projectMgr.ListProjects()
	if err != nil {
		log.Warn("failed to pre-load projects for early emit", "error", err)
		return nil
	}
	if len(projects) == 0 {
		return nil
	}

	emit(EventProjectsLoaded, projects)

	if sessStore != nil {
		// Bounded read: this runs synchronously inside Startup, which arms no
		// hard watchdog — a never-deadlined context would park the OnStartup
		// goroutine (splash forever) behind a contended SQLite pool; expiry
		// just skips the early sessions emit.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		sessions, sErr := sessStore.ListSessionsByProject(ctx, projects[0].ID)
		cancel()
		if sErr == nil {
			emit(EventSessionsLoaded, sessions)
		} else {
			log.Warn("failed to pre-load sessions for early emit", "error", sErr)
		}
	}
	return projects
}

// PreloadProjectsAndSessions forwards to preloadProjectsAndSessions for the
// desktop startup adapter.
func PreloadProjectsAndSessions(projectMgr *project.Manager, sessStore *session.SQLiteSessionStore, log *slog.Logger, emit func(eventName string, data ...any)) []project.ProjectInfo {
	return preloadProjectsAndSessions(projectMgr, sessStore, log, emit)
}

// initEmbeddedLLM restores the embedded local-model state: a manifest.json
// read plus one embedded_llm:state event — no download, no network, no probe,
// no model load (startup stays offline-first and never blocks on weights; see
// specs/domains/embedded-llm.md). One stat + one small JSON read, far below
// the phase budget — hence inline in Phase 5, complete before the first
// GetEmbeddedLLMStatus.
func initEmbeddedLLM(f *FrontendAPI, log *slog.Logger) {
	if f == nil {
		return
	}
	startTime := time.Now()
	f.Lifecycle().InitEmbeddedLLM()
	log.Info("startup phase complete", "phase", "embedded_llm",
		"elapsed_ms", time.Since(startTime).Milliseconds())
}

// InitEmbeddedLLM forwards to initEmbeddedLLM for the desktop startup
// adapter.
func InitEmbeddedLLM(f *FrontendAPI, log *slog.Logger) {
	initEmbeddedLLM(f, log)
}

// initChatGPTAuth constructs the ChatGPT subscription-auth token manager and
// restores keychain credentials (see specs/domains/llm-providers.md, ChatGPT
// Subscription Auth). Mirrors initEmbeddedLLM in doing no network I/O and no
// browser flow — a failed construction records the actionable error and
// leaves the app usable on api_key auth.
//
// UNLIKE initEmbeddedLLM, the restore runs on a BACKGROUND goroutine: the OS
// keychain read is not provably bounded (a locked Linux login collection
// unlocks with an interactive prompt, no timeout), so a synchronous restore
// could hold backend readiness hostage for an api_key-only user. The auth
// surface tolerates the gap by design (status RPC answers the signed-out
// zero state; mutating RPCs refuse transiently; the late restore publishes
// itself like an interactive sign-in). Nothing can cancel a native keyring
// call, so on shutdown the context checks inside InitChatGPTAuth DISCARD the
// result — a late restore never races the teardown.
func initChatGPTAuth(f *FrontendAPI, log *slog.Logger) {
	if f == nil {
		return
	}
	// Off the startup path through SafeGo — the keyring backend is native
	// code, and an unrecovered panic would take the process down for a
	// feature designed to degrade gracefully. Cancellation is checked before
	// touching FrontendAPI state, so a shutdown is never raced.
	SafeGo(log, "chatgpt_auth", func() {
		startTime := time.Now()
		f.Lifecycle().InitChatGPTAuth()
		log.Info("startup phase complete", "phase", "chatgpt_auth",
			"elapsed_ms", time.Since(startTime).Milliseconds())
	})
}

// InitChatGPTAuth forwards to initChatGPTAuth for the desktop startup
// adapter.
func InitChatGPTAuth(f *FrontendAPI, log *slog.Logger) {
	initChatGPTAuth(f, log)
}

// initKimiAuth mirrors the ChatGPT restore: a locked OS keychain must not
// block backend readiness, and a late result is discarded during shutdown.
func initKimiAuth(f *FrontendAPI, log *slog.Logger) {
	if f == nil {
		return
	}
	SafeGo(log, "kimi_auth", func() {
		startTime := time.Now()
		f.Lifecycle().InitKimiAuth()
		log.Info("startup phase complete", "phase", "kimi_auth",
			"elapsed_ms", time.Since(startTime).Milliseconds())
	})
}

// InitKimiAuth forwards to initKimiAuth for the desktop startup adapter.
func InitKimiAuth(f *FrontendAPI, log *slog.Logger) {
	initKimiAuth(f, log)
}

// startUpdateCheckerBackground runs one best-effort automatic update check in
// a goroutine; FrontendAPI.RunBackgroundUpdateCheck is the sole auto-check
// path (gates, interval, caching).
//
// Deliberately NO updater.CleanupStaleUpdaters here: Startup runs only in the
// owning (first) instance — exactly while the app-level lock is held — so a
// reap now would delete the live staging of a self-update that is mid-apply.
// The only reap site is main.go's post-lock, first-instance-gated call.
func startUpdateCheckerBackground(f *FrontendAPI, log *slog.Logger) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("automatic update check panicked", "panic", r)
			}
		}()
		f.RunBackgroundUpdateCheck()
	}()
}

// StartUpdateCheckerBackground forwards to startUpdateCheckerBackground for
// the desktop startup adapter.
func StartUpdateCheckerBackground(f *FrontendAPI, log *slog.Logger) {
	startUpdateCheckerBackground(f, log)
}

// startAutoFetchBackground starts the periodic git auto-fetch ticker exactly
// once after the backend is ready (mirroring startUpdateCheckerBackground);
// stopped by FrontendAPILifecycle.Cleanup. See
// specs/domains/git-auto-fetch.md.
func startAutoFetchBackground(f *FrontendAPI) {
	f.Lifecycle().StartAutoFetch()
}

// StartAutoFetchBackground forwards to startAutoFetchBackground for the
// desktop startup adapter.
func StartAutoFetchBackground(f *FrontendAPI) {
	startAutoFetchBackground(f)
}
