package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/toolmanager"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
	"github.com/v0lka/sp4rk/tools/mcp"
)

// ApplicationConfig holds all parameters needed to construct an Application.
// Desktop provides the UI callbacks; everything else is derived from config.
type ApplicationConfig struct {
	Config   *config.Config
	Logger   *slog.Logger
	AgentDir string // base agent directory (e.g. ~/.c0wrk)

	// Persistence stores (optional — nil disables corresponding functionality).
	SessionStore session.SessionStore
	TaskStore    session.TaskStore

	// UI callbacks provided by the desktop adapter.
	UIEmitFunc       func(session.Event)    // Wails event emission
	AskUserFunc      coretools.AskUserFunc  // ask_user tool callback
	PlanApprovalFunc coretools.ApprovalFunc // declare_plan await_approval callback
	ConfirmFunc      sdktools.ConfirmFunc   // tool confirmation callback
	HITLHandler      agent.HITLHandler      // step limit and tool confirmation callback
	GoalProposer     coretools.GoalProposer // propose_goal approval flow (nil disables goal mode)

	// Vector search callbacks (optional — nil disables semantic_search tool).
	VectorSearchFunc     builtins.VectorSearchFunc
	VectorSearchWaitFunc builtins.VectorSearchWaitFunc
	// VectorSearchWaitTimeout bounds how long vector-search callers (the
	// semantic_search tool and RAG hint injection) wait for index readiness
	// before an actionable error / hint skip
	// (vector_index.search_wait_timeout_ms; 0 = fail fast).
	VectorSearchWaitTimeout time.Duration
	// VectorSearchWaitDisabled marks an EXPLICIT fail-fast
	// (vector_index.search_wait_timeout_ms: 0): the desktop layer sets it
	// after config defaults resolved "unset" to 3000ms, so a zero timeout
	// here is unambiguously the user's choice, not the default.
	VectorSearchWaitDisabled bool

	// FileChangeNotifyFunc is called after a file-mutating tool (write_file,
	// edit_file, bash_exec) completes successfully; the executor context
	// carries the session's workspace root, so the implementation routes the
	// debounced re-indexing to THAT root's vector manager (ADR-080).
	FileChangeNotifyFunc func(ctx context.Context)

	// FileChangedWorkspaceEmitter runs alongside FileChangeNotifyFunc to emit
	// workspace:tree_changed so the frontend refreshes after agent-initiated
	// writes. Best-effort; the filesystem watcher covers manual edits.
	FileChangedWorkspaceEmitter func()
}

// Application is the central ViewModel that ties together the OrchestratorBuilder,
// session Manager, and event persistence. Desktop imports only this package.
type Application struct {
	builder   *core.OrchestratorBuilder
	manager   *session.Manager
	persister *session.EventPersister
	// tokenPersist coalesces session-token updates through the persister's
	// single writer, off the emitter's goroutine. Nil without a session store.
	tokenPersist *tokenPersister
	titleGen     *session.TitleGenerator
	logger       *slog.Logger

	// agentDir (~/.c0wrk) locates the model-profile custom-profile store used
	// to resolve the effective profile on every builder-config conversion.
	agentDir string

	// emitFunc is the combined session-event emitter (UI + persistence);
	// exposed via EmitSessionEvent so desktop callbacks can emit events that
	// survive app restarts.
	emitFunc func(session.Event)

	// goalProposer is the desktop goal-approval flow injected onto every
	// per-session orchestrator by the factory; set via SetGoalProposer once
	// desktop's pending-confirmation map is ready.
	goalProposer coretools.GoalProposer

	// goalSuggestionProposer mirrors goalProposer for the goal-mode
	// suggestion card flow.
	goalSuggestionProposer core.GoalSuggestionProposer

	// hitlHandler is captured for the orchestrator factory closure.
	hitlHandler agent.HITLHandler

	// autoRetryIntervals is the published snapshot of every provider's
	// auto_retry_seconds (ADR-065), keyed by logical provider name —
	// immutable once stored; the pointer swap is the only synchronization.
	// See specs/decisions/065-auto-resend-retryable-errors.md.
	autoRetryIntervals atomic.Pointer[map[string]int]

	// vectorRootsPtr late-binds the per-root vector-index registry (ADR-081),
	// which lives on FrontendAPI (constructed after the Application);
	// registered search closures resolve it at call time and surface the
	// still-loading error until SetVectorRoots lands. Nil before wiring.
	vectorRootsPtr atomic.Pointer[VectorRoots]

	// builderConfigProvider produces the BuilderConfig every new session's
	// orchestrator is built from, once wired by NewFrontendAPI: the
	// conversion must run under FrontendAPI's configMu — a factory invoked
	// on Wails call goroutines converts the live pointer unlocked only
	// before that wiring, where no Settings save can race it.
	builderConfigProvider atomic.Pointer[func() *core.BuilderConfig]
}

// SetBuilderConfigProvider wires the lock-respecting config snapshot the
// session-orchestrator factory converts (see builderConfigProvider).
// Startup-only, called once from NewFrontendAPI.
func (a *Application) SetBuilderConfigProvider(fn func() *core.BuilderConfig) {
	a.builderConfigProvider.Store(&fn)
}

// sessionBuilderConfig returns the BuilderConfig a new session's orchestrator
// is built from. After Init wired the provider, the conversion runs under
// FrontendAPI's configMu: the factory is invoked on Wails call goroutines,
// concurrently with Settings saves that mutate the shared live config's maps
// in place under the same lock — converting the live pointer unlocked is a
// fatal concurrent-map-access crash. Before that wiring the startup config is
// converted directly: Init wires the provider inside its critical section,
// before raising the seedPublished flag that admits guardless RPC readers.
// See specs/domains/session-lifecycle.md (§ BuilderConfig provider under
// FrontendAPI's configMu).
func (a *Application) sessionBuilderConfig(startupCfg *config.Config) *core.BuilderConfig {
	if fn := a.builderConfigProvider.Load(); fn != nil {
		if cfg := (*fn)(); cfg != nil {
			return cfg
		}
		// Fail loud rather than nil-deref on the caller's first field
		// access: nil here means the FrontendAPI config was never wired.
		a.log().Error("builder config provider returned nil — startup wiring is broken")
		return nil
	}
	return ToBuilderConfig(startupCfg, loadModelProfilesCatalog(a.agentDir, a.logger), a.logger)
}

func (a *Application) log() *slog.Logger {
	if a.logger != nil {
		return a.logger
	}
	return slog.Default()
}

// NewApplication creates a fully-initialized Application.
// It builds the shared tool registry, MCP gateway, LLM router, session manager,
// and event persister from the given configuration.
func NewApplication(cfg ApplicationConfig) (*Application, error) {
	app := &Application{
		logger:       cfg.Logger,
		hitlHandler:  cfg.HITLHandler,
		goalProposer: cfg.GoalProposer,
		agentDir:     cfg.AgentDir,
	}

	// 1. Event persister (SQLite persistence, separate from UI emission).
	app.persister = session.NewEventPersister(cfg.SessionStore)
	app.persister.StartWriter()

	// 2. Combined emit function: UI emission + persistence.
	//
	// Persistence is offloaded to the persister's single-writer goroutine, so
	// this (the emit goroutine) never blocks on SQLite. Terminal events double
	// as a durability checkpoint: the write queue (and coalesced token
	// updates) is drained at task end so a completed task's messages are on
	// disk before the app can be killed.
	emitFunc := func(evt session.Event) {
		if cfg.UIEmitFunc != nil {
			cfg.UIEmitFunc(evt)
		}
		app.persister.Persist(evt)
		if terminalPersistTypes[evt.Type] {
			if app.tokenPersist != nil {
				app.tokenPersist.Flush()
			}
			app.persister.Flush()
		}
	}
	app.emitFunc = emitFunc

	// 3. OrchestratorBuilder (owns registry, gateway, router, judge).
	builderCfg := ToBuilderConfig(cfg.Config, loadModelProfilesCatalog(app.agentDir, app.log()), app.log())
	// Managed venv interpreter (imports markitdown) enables vision-assisted
	// document conversion. Resolved LAZILY — the tool-manager installs the
	// venv asynchronously after startup, so an eager probe would see an empty
	// path on fresh installs and silently disable vision for the whole run.
	markitdownPython := func() string {
		return toolmanager.VenvPythonPath(config.ToolsDir(cfg.AgentDir))
	}
	builderCfg.MarkitdownPythonPath = markitdownPython
	builder, err := core.NewOrchestratorBuilder(builderCfg, cfg.AskUserFunc, cfg.PlanApprovalFunc, cfg.Logger)
	if err != nil {
		return nil, err
	}
	app.builder = builder

	// 3a. Vector search (per-root router, ADR-081): late-binds the
	// FrontendAPI-owned registry (SetVectorRoots) and resolves the workspace
	// root from the executor context — the session's own tree for agent-side
	// calls, the Git-panel focus otherwise. Registered unconditionally: the
	// pre-wiring window surfaces the still-loading error. An injected
	// cfg.VectorSearchFunc (tests) wins.
	if cfg.VectorSearchFunc != nil {
		builder.RegisterVectorSearch(cfg.VectorSearchFunc, cfg.VectorSearchWaitFunc, cfg.VectorSearchWaitTimeout, cfg.VectorSearchWaitDisabled)
	} else {
		searchFunc, waitFunc := app.buildVectorRouter(cfg.VectorSearchWaitTimeout)
		builder.RegisterVectorSearch(searchFunc, waitFunc, cfg.VectorSearchWaitTimeout, cfg.VectorSearchWaitDisabled)
	}

	// 3b. Skill discovery directories. The builder creates a per-session
	// SkillManager on each Build() and always prepends the current project's
	// `.agents/skills` directory (see core/builder.go); here only the shared
	// base dirs from config are resolved and registered.
	if len(cfg.Config.Skills.Dirs) > 0 {
		skillDirs := resolveCatalogDirs(cfg.Config.Skills.Dirs, cfg.AgentDir, config.ExpandEnvVars, cfg.Logger)
		builder.SetSkillDirs(skillDirs)
	}

	// 3c. Subagent Profile discovery directories — mirrors the skill dirs
	// wiring (the builder prepends the project's `.agents/agents` per Build();
	// defaults applied in config.ApplyDefaults).
	if len(cfg.Config.Agents.Dirs) > 0 {
		agentDirs := resolveCatalogDirs(cfg.Config.Agents.Dirs, cfg.AgentDir, config.ExpandEnvVars, cfg.Logger)
		builder.SetAgentDirs(agentDirs)
	}

	// 4. Set confirmation function on the shared registry.
	if cfg.ConfirmFunc != nil {
		builder.ToolRegistry().SetConfirmFunc(cfg.ConfirmFunc)
	}

	// 4a. Post-execute hook: after a successful file-mutating tool (write_file,
	// edit_file, bash_exec), notify the vector index manager (debounced
	// incremental re-indexing) and emit workspace:tree_changed — the
	// synchronous hook covers the latency window fsnotify cannot.
	if cfg.FileChangeNotifyFunc != nil {
		notifyFn := cfg.FileChangeNotifyFunc
		emitFn := cfg.FileChangedWorkspaceEmitter
		builder.ToolRegistry().SetPostExecuteHook(func(ctx context.Context, toolName string, res sdktools.ToolResult, execErr error) {
			if execErr != nil || res.IsError {
				return
			}
			if !core.FileMutatingTools[toolName] {
				return
			}
			notifyFn(ctx)
			if emitFn != nil {
				emitFn()
			}
		})
	}

	// 4b. Smart Approve judge observer: the strict judge evaluates an
	// escalated call BEFORE any confirmation card exists, so the session
	// status must say a judge is working — the observer emits the transient
	// tool_judge_started/finished events. See specs/contracts/event-catalog.md.
	builder.ToolRegistry().SetJudgeObserver(func(ctx context.Context, phase coretools.JudgePhase, toolName string) {
		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			app.log().Debug("judge observer: no session in context", "tool", toolName)
			return
		}
		app.manager.EmitJudgePhase(sessionID, phase == coretools.JudgePhaseStarted, toolName)
	})

	// 4c. Autonomy-decision observer: a decision taken without a human must be
	// visible and auditable, never a silent skip (OWASP ASI10 — the trajectory
	// must stay reconstructable). The executor context is forwarded so a
	// subagent's decision renders inside the subagent's chat block.
	// See specs/architecture/security-model.md.
	builder.ToolRegistry().SetAutonomyDecisionObserver(func(ctx context.Context, decision coretools.AutonomyDecision) {
		if app.manager == nil {
			return
		}
		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			app.log().Debug("autonomy decision observer: no session in context",
				"kind", decision.Kind, "tool", decision.Tool, "verdict", decision.Verdict)
			return
		}
		app.manager.EmitAutonomyDecision(ctx, sessionID, decision)
	})

	// 5. Orchestrator factory closure for the session manager.
	factory := func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer, stepDumpTracker *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		// Converted under FrontendAPI's configMu once NewFrontendAPI wires the
		// provider (see sessionBuilderConfig): the factory runs on Wails call
		// goroutines and must not range the live config's maps unlocked.
		orchCfg := app.sessionBuilderConfig(cfg.Config)
		if orchCfg == nil {
			// sessionBuilderConfig already logged the wiring break; surface it
			// as a session-creation error instead of a nil dereference below.
			return nil, errors.New("builder config unavailable (provider returned nil)")
		}
		// The lazy python probe is consumed at tool registration (builder
		// creation); propagate it so any future Build-side consumer sees the
		// closure instead of a zero value.
		orchCfg.MarkitdownPythonPath = markitdownPython
		orch, err := builder.Build(orchCfg, emitter, logger, workspacePath, bbFactory, app.hitlHandler, dumpWriter, stepDumpTracker)
		if err != nil {
			return nil, err
		}
		// Inject the goal proposer so propose_goal can reach the desktop
		// approval flow; nil is valid (goal mode fails fast when invoked).
		if app.goalProposer != nil {
			orch.SetGoalProposer(app.goalProposer)
		}
		// Inject the goal-suggestion proposer so the post-routing suggestion
		// gate can reach the desktop pending card; nil keeps the gate
		// disabled (plain Conductor path).
		if app.goalSuggestionProposer != nil {
			orch.SetGoalSuggestionProposer(app.goalSuggestionProposer)
		}
		return orch, nil
	}

	// 6. Session manager.
	manager := session.NewManager(factory, emitFunc, cfg.AgentDir)
	if cfg.SessionStore != nil {
		app.tokenPersist = newTokenPersister(
			cfg.SessionStore,
			app.persister.SubmitWrite,
			tokenPersistInterval,
			tokenPersistTimeout,
			app.log(),
		)
		manager.SetTokenPersist(app.tokenPersist.Record)
	}
	if cfg.TaskStore != nil {
		manager.SetTaskStore(cfg.TaskStore)
	}
	// Launch environment-info collection in the background. envInfo is
	// optional (SendMessage/ResumeTask tolerate nil) and only enriches the
	// system prompt once ready — collecting it synchronously blocked cold
	// startup on ~7 subprocess probes (~0.75s).
	manager.StartEnvInfoCollection()
	manager.SetMaxSummaryLen(cfg.Config.Orchestration.MaxSummaryLength)
	// Annotate agent quality metrics with the active Model Profiles profile;
	// resolution warnings were already surfaced at load time.
	modelProfilesCatalog := loadModelProfilesCatalog(app.agentDir, app.log())
	modelProfile, _ := effectiveModelProfilesConfig(cfg.Config, modelProfilesCatalog)
	manager.SetModelProfile(modelProfile, activeModelProfile(cfg.Config.ModelProfiles, modelProfilesCatalog))
	manager.SetServiceLLMTimeout(time.Duration(cfg.Config.Timeouts.ServiceLLMRequestTimeout) * time.Second)
	if cfg.SessionStore != nil {
		manager.SetSessionStore(cfg.SessionStore)
	}
	// Auto-retry resolver: resolves a provider name to its auto_retry_seconds
	// interval from the ATOMIC snapshot — the manager calls it without
	// configMu, and reading the live config maps would race Settings saves
	// (a fatal concurrent-map-access crash). Fixed providers resolve to 0.
	// See specs/decisions/065-auto-resend-retryable-errors.md.
	app.publishAutoRetryIntervals(cfg.Config)
	manager.SetAutoRetryResolver(func(provider string) int {
		return app.autoRetryIntervalSnapshot()[provider]
	})
	app.manager = manager

	// 7. Title generator backed by the builder's cached LLM router.
	app.titleGen = session.NewTitleGenerator(app.builder)
	manager.SetTitleGenerator(app.titleGen)

	// Prompt-suggestion generator (ADR-088), backed by the same builder. The
	// prompt_suggestions.enabled gate (nil config = on) is applied at the
	// manager's outcome gate: a disabled feature never spawns a pass.
	manager.SetPromptSuggestionsGenerator(session.NewPromptSuggestGenerator(app.builder))
	manager.SetPromptSuggestionsEnabled(cfg.Config.PromptSuggestions.SuggestionsEnabled())

	return app, nil
}

// Manager returns the session manager.
func (a *Application) Manager() *session.Manager {
	return a.manager
}

// Builder returns the orchestrator builder for advanced operations.
func (a *Application) Builder() *core.OrchestratorBuilder {
	return a.builder
}

// SetGoalProposer sets the goal-proposer hook the orchestrator factory
// injects onto every per-session orchestrator; desktop calls it after
// construction, so the proposer is in place before any orchestrator is built.
func (a *Application) SetGoalProposer(proposer coretools.GoalProposer) {
	a.goalProposer = proposer
}

// SetGoalSuggestionProposer mirrors SetGoalProposer for the goal-mode
// suggestion card flow.
func (a *Application) SetGoalSuggestionProposer(proposer core.GoalSuggestionProposer) {
	a.goalSuggestionProposer = proposer
}

// TitleGenerator returns the session title generator.
func (a *Application) TitleGenerator() *session.TitleGenerator {
	return a.titleGen
}

// EvaluateJudge performs an on-demand judge evaluation for a pending tool
// confirmation using the SHARED registry's judge (bound to the builder's
// global active provider/model). Prefer EvaluateJudgeForSession for live
// sessions — see specs/decisions/028-session-pinned-judge.md.
func (a *Application) EvaluateJudge(ctx context.Context, toolName string, input json.RawMessage, taskContext string) (verdict sdktools.JudgeVerdict, reasoning string, err error) {
	if err := a.builder.WaitReady(ctx); err != nil {
		return sdktools.VerdictConfirm, "", fmt.Errorf("judge not available: %w", err)
	}
	registry := a.builder.ToolRegistry()
	if registry == nil {
		return sdktools.VerdictConfirm, "", ErrJudgeNotAvailable
	}
	judge := registry.GetJudge()
	if judge == nil {
		return sdktools.VerdictConfirm, "", ErrJudgeNotAvailable
	}
	tool, _ := registry.Get(toolName)
	return evaluateJudgeWith(ctx, judge, tool, toolName, input, taskContext)
}

// EvaluateJudgeForSession performs an on-demand judge evaluation using the
// SESSION-pinned judge — the judge bound to the session's own router, so a
// manual "Judge" action evaluates on the same provider/model the session runs
// on, exactly like automatic escalations (ADR-028). Falls back to the shared
// registry's judge when the session is unknown or unbound.
// See specs/decisions/028-session-pinned-judge.md.
func (a *Application) EvaluateJudgeForSession(ctx context.Context, sessionID, toolName string, input json.RawMessage, taskContext string) (verdict sdktools.JudgeVerdict, reasoning string, err error) {
	if sessionID != "" && a.manager != nil {
		if sess, ok := a.manager.GetSession(sessionID); ok {
			if orch := sess.GetOrchestrator(); orch != nil {
				if registry := orch.ToolRegistry(); registry != nil {
					if judge := registry.GetJudge(); judge != nil {
						tool, _ := registry.Get(toolName)
						return evaluateJudgeWith(ctx, judge, tool, toolName, input, taskContext)
					}
				}
			}
		}
	}
	return a.EvaluateJudge(ctx, toolName, input, taskContext)
}

// evaluateJudgeWith runs a single judge evaluation and prefixes the reasoning
// for safe/unsafe verdicts: "SAFE: " contextualizes an allow, "UNSAFE: " turns
// a deliberate VerdictDeny into an explicit rejection recommendation on the
// OPEN card (the advisory judge never decides — the operator stays free to
// allow).
//
// Shell-exec tools get the deterministic flowsh digest attached to ctx first
// (the Ask-Agent advisory path renders it as its "Static Analysis Report"
// block — the same evidence the strict judge and the tool's own Judge
// consume); the registered tool instance rides along so an operator-configured
// shell invocation override picks the analysis dialect. Evidence, never a
// verdict override.
func evaluateJudgeWith(ctx context.Context, judge *sdktools.ToolJudge, tool sdktools.Tool, toolName string, input json.RawMessage, taskContext string) (sdktools.JudgeVerdict, string, error) {
	ctx = coretools.AttachShellAnalysisForTool(ctx, tool, toolName, input, nil)
	verdict, reasoning, err := judge.Judge(ctx, toolName, input, taskContext)
	if err != nil {
		return verdict, reasoning, err
	}
	switch verdict {
	case sdktools.VerdictAllow:
		reasoning = "SAFE: " + reasoning
	case sdktools.VerdictDeny:
		reasoning = "UNSAFE: " + reasoning
	}
	return verdict, reasoning, nil
}

// GetMCPStatus returns the status of all MCP servers, non-blocking so the
// settings dialog does not stall during gateway startup. While startup is in
// flight it returns the "_gateway" placeholder the frontend renders as a
// neutral "Starting…" state; a failed gateway surfaces its error the same way.
// See specs/domains/tool-system/mcp-gateway.md.
func (a *Application) GetMCPStatus() []mcp.ServerStatus {
	if !a.builder.MCPStartupDone() {
		return []mcp.ServerStatus{{
			Name:     "_gateway",
			Starting: true,
		}}
	}
	gw := a.builder.MCPGatewayNoWait()
	if gw == nil {
		if errMsg := a.builder.MCPGatewayError(); errMsg != "" {
			return []mcp.ServerStatus{{
				Name:  "_gateway",
				Error: errMsg,
			}}
		}
		return []mcp.ServerStatus{}
	}
	return gw.Status()
}

// ListTools returns descriptors for all registered tools.
func (a *Application) ListTools() []sdktools.ToolDescriptor {
	return a.builder.ToolRegistry().List()
}

// GroupPolicies returns the live group→policy map enforced by the shared
// tool registry, so callers report each tool's EFFECTIVE policy — the same
// map Execute consults — instead of re-deriving it from config.
// See specs/decisions/024-group-policies.md.
func (a *Application) GroupPolicies() map[sdktools.ToolGroup]sdktools.ToolPolicy {
	return a.builder.ToolRegistry().GroupPolicies()
}

// Shutdown stops all managed resources (manager, persistence pipeline, MCP
// gateway). This runs on the main goroutine during app shutdown; per-step
// duration logs keep a slow teardown attributable.
func (a *Application) Shutdown() {
	shutdownStart := time.Now()
	shutdownStep := func(name string) {
		a.log().Info("application teardown step complete",
			"step", name,
			"step_ms", time.Since(shutdownStart).Milliseconds())
	}
	if a.manager != nil {
		a.manager.Shutdown()
	}
	shutdownStep("managerShutdown")
	// Drain the persistence pipeline AFTER the manager has stopped every task
	// goroutine (so nothing new is enqueued): flush the coalesced token updates,
	// then drain the event-write queue so no pending write is lost on exit.
	if a.tokenPersist != nil {
		a.tokenPersist.Close()
	}
	shutdownStep("tokenPersistClose")
	if a.persister != nil {
		a.persister.Close()
	}
	shutdownStep("eventPersisterClose")
	if a.builder != nil {
		if err := a.builder.StopGateway(); err != nil {
			a.log().Error("failed to stop MCP gateway", "error", err)
		}
	}
	shutdownStep("stopMCPGateway")
}

// terminalPersistTypes lists the task-terminal event types after which the
// async persistence pipeline is drained — keeping the emit path non-blocking
// during a run while making a finished task's messages durable promptly.
var terminalPersistTypes = map[string]bool{
	"task_complete":         true,
	"task_failed_resumable": true,
	"task_cancelled":        true,
}

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

// ErrJudgeNotAvailable is returned when a judge evaluation is requested but
// no judge is configured.
var ErrJudgeNotAvailable = errJudgeNotAvailable("judge is not available; check LLM provider configuration")

type errJudgeNotAvailable string

func (e errJudgeNotAvailable) Error() string { return string(e) }

// resolveCatalogDirs converts a list of configured skill/agent/command
// directories into absolute paths: leading `~` and `${ENV_VAR}` expand,
// relative paths resolve against agentDir, empty results drop. log receives
// warnings on non-critical resolution failures; nil suppresses them.
func resolveCatalogDirs(dirs []string, agentDir string, expandEnv func(string) string, log *slog.Logger) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		if log != nil {
			log.Warn("failed to resolve user home directory; tilde-prefixed dirs will remain unresolved", "error", err)
		}
	}
	resolved := make([]string, 0, len(dirs))
	for _, d := range dirs {
		d = expandEnv(d)
		d = expandTilde(d, home)
		if d == "" {
			continue
		}
		if !filepath.IsAbs(d) {
			d = filepath.Join(agentDir, d)
		}
		resolved = append(resolved, d)
	}
	return resolved
}

// expandTilde replaces a leading `~` or `~/` in p with the user's home
// directory. With an empty home, a leading `~` is left intact so the caller
// decides how to handle the failure.
func expandTilde(p, home string) string {
	if home == "" || p == "" {
		return p
	}
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~"+string(filepath.Separator)):
		return filepath.Join(home, p[2:])
	case strings.HasPrefix(p, "~/"): // also handle forward-slash form on Windows
		return filepath.Join(home, p[2:])
	}
	return p
}

// EmitSessionEvent emits a session event through the combined UI +
// persistence path so it survives app restarts. See
// specs/contracts/desktop-frontend.md.
func (a *Application) EmitSessionEvent(evt session.Event) {
	if a.emitFunc != nil {
		a.emitFunc(evt)
	}
}

// EmitToolConfirm routes a tool-confirmation request through the live session
// emitter so the activity tracker (and the runtime-status snapshot read on
// session switches) reports "Awaiting confirmation..." while the agent
// goroutine blocks on the user's decision. See specs/contracts/event-catalog.md.
func (a *Application) EmitToolConfirm(sessionID string, payload session.ToolConfirmPayload) {
	if a.manager != nil {
		a.manager.EmitToolConfirm(sessionID, payload)
		return
	}
	a.EmitSessionEvent(session.Event{SessionID: sessionID, Type: "tool_confirm", Data: payload})
}

// LastToolCallID returns the most recently emitted tool_call_id for a session
// (and its tool name), so the desktop tool-confirmation callback can attach
// the matching id to the tool_confirm payload and the frontend can correlate
// the confirmation with the exact tool_call event.
func (a *Application) LastToolCallID(sessionID string) (id, tool string) {
	if a.manager == nil {
		return "", ""
	}
	return a.manager.LastToolCallID(sessionID)
}

// buildAutoRetryIntervals derives the provider→interval snapshot from a
// config; the returned map is immutable by convention — every consumer of
// the published pointer treats it read-only.
// See specs/decisions/065-auto-resend-retryable-errors.md.
func buildAutoRetryIntervals(cfg *config.Config) map[string]int {
	if cfg == nil {
		return map[string]int{}
	}
	intervals := make(map[string]int, len(cfg.LLM.OpenAICompatible)+len(cfg.LLM.AnthropicCompatible))
	for name, c := range cfg.LLM.OpenAICompatible {
		intervals[name] = c.AutoRetrySeconds
	}
	for name, f := range cfg.LLM.AnthropicCompatible {
		intervals[name] = f.AutoRetrySeconds
	}
	return intervals
}

// publishAutoRetryIntervals rebuilds and stores the atomic snapshot from the
// given config — at Application construction and after every committed LLM
// config mutation. See specs/decisions/065-auto-resend-retryable-errors.md.
func (a *Application) publishAutoRetryIntervals(cfg *config.Config) {
	snapshot := buildAutoRetryIntervals(cfg)
	a.autoRetryIntervals.Store(&snapshot)
}

// autoRetryIntervalSnapshot returns the published provider→interval snapshot
// (nil before the first publish — reads on a nil map are safe and yield 0,
// "no provider has an interval").
func (a *Application) autoRetryIntervalSnapshot() map[string]int {
	if p := a.autoRetryIntervals.Load(); p != nil {
		return *p
	}
	return nil
}
