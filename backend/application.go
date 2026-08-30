package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	// before returning an actionable error / skipping hints
	// (vector_index.search_wait_timeout_ms; 0 = fail fast). The desktop
	// search closure enforces it; passed through to per-session
	// orchestrators for the RAG-hint bound.
	VectorSearchWaitTimeout time.Duration
	// VectorSearchWaitDisabled marks an EXPLICIT fail-fast
	// (vector_index.search_wait_timeout_ms: 0). The desktop layer sets it
	// after the config defaults have resolved "unset" to 3000ms, so a zero
	// timeout here is unambiguously the user's choice — the orchestrator
	// layer cannot tell "unset" (keep 3s default) from "explicit 0" (wait
	// zero) without this flag.
	VectorSearchWaitDisabled bool

	// FileChangeNotifyFunc is called after a file-mutating tool (write_file,
	// edit_file, bash_exec) completes successfully. It triggers debounced
	// incremental re-indexing so that subsequent searches reflect the change
	// without waiting for the filesystem watcher. Nil disables the hook.
	FileChangeNotifyFunc func()

	// FileChangedWorkspaceEmitter is called alongside FileChangeNotifyFunc to
	// emit the workspace:tree_changed event so the frontend (research panel,
	// file tree, etc.) refreshes its view after agent-initiated file writes.
	// Nil is safe — the event is best-effort; the filesystem watcher handles
	// manual edits.
	FileChangedWorkspaceEmitter func()
}

// Application is the central ViewModel that ties together the OrchestratorBuilder,
// session Manager, and event persistence. Desktop imports only this package.
type Application struct {
	builder   *core.OrchestratorBuilder
	manager   *session.Manager
	persister *session.EventPersister
	titleGen  *session.TitleGenerator
	logger    *slog.Logger

	// emitFunc is the combined session-event emitter (UI + persistence).
	// Exposed via EmitSessionEvent so desktop-layer callbacks (e.g. plan
	// approval) can emit events that survive app restarts.
	emitFunc func(session.Event)

	// goalProposer is the desktop goal-approval flow injected onto every
	// per-session orchestrator by the factory. Set via SetGoalProposer after
	// construction (desktop wires it once its pending-confirmation map is ready).
	goalProposer coretools.GoalProposer

	// hitlHandler is captured for the orchestrator factory closure.
	hitlHandler agent.HITLHandler
}

func (app *Application) log() *slog.Logger {
	if app.logger != nil {
		return app.logger
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
	}

	// 1. Event persister (SQLite persistence, separate from UI emission).
	app.persister = session.NewEventPersister(cfg.SessionStore)

	// 2. Combined emit function: UI emission + persistence.
	emitFunc := func(evt session.Event) {
		if cfg.UIEmitFunc != nil {
			cfg.UIEmitFunc(evt)
		}
		app.persister.Persist(evt)
	}
	app.emitFunc = emitFunc

	// 3. OrchestratorBuilder (owns registry, gateway, router, judge).
	builderCfg := ToBuilderConfig(cfg.Config)
	// Managed venv interpreter (imports markitdown) enables vision-assisted
	// document conversion. Machine-local fact, resolved LAZILY: the
	// tool-manager installs the venv asynchronously after startup, so probing
	// eagerly here would see an empty path on fresh installs and silently
	// disable vision for the whole app run. The probe runs at the read_file
	// document wrapper's first converter init instead.
	markitdownPython := func() string {
		return toolmanager.VenvPythonPath(config.ToolsDir(cfg.AgentDir))
	}
	builderCfg.MarkitdownPythonPath = markitdownPython
	builder, err := core.NewOrchestratorBuilder(builderCfg, cfg.AskUserFunc, cfg.PlanApprovalFunc, cfg.Logger)
	if err != nil {
		return nil, err
	}
	app.builder = builder

	// 3a. Vector search (optional — registered after builder creation)
	if cfg.VectorSearchFunc != nil {
		builder.RegisterVectorSearch(cfg.VectorSearchFunc, cfg.VectorSearchWaitFunc, cfg.VectorSearchWaitTimeout, cfg.VectorSearchWaitDisabled)
	}

	// 3b. Skill discovery directories. The builder creates a per-session
	// SkillManager on each Build() and always prepends the current project's
	// `.agents/skills` directory (see core/builder.go). Here we only resolve
	// and register the shared base dirs from config.
	if len(cfg.Config.Skills.Dirs) > 0 {
		skillDirs := resolveSkillDirs(cfg.Config.Skills.Dirs, cfg.AgentDir, config.ExpandEnvVars, cfg.Logger)
		builder.SetSkillDirs(skillDirs)
	}

	// 3c. Subagent Profile discovery directories. Mirrors the skill dirs
	// wiring: the builder creates a per-session AgentManager on each Build()
	// and always prepends the current project's `.agents/agents` directory
	// (see core/builder.go). Here we resolve and register the shared base
	// dirs from config (defaults applied in config.ApplyDefaults).
	if len(cfg.Config.Agents.Dirs) > 0 {
		agentDirs := resolveSkillDirs(cfg.Config.Agents.Dirs, cfg.AgentDir, config.ExpandEnvVars, cfg.Logger)
		builder.SetAgentDirs(agentDirs)
	}

	// 4. Set confirmation function on the shared registry.
	if cfg.ConfirmFunc != nil {
		builder.ToolRegistry().SetConfirmFunc(cfg.ConfirmFunc)
	}

	// 4a. Post-execute hook: after a file-mutating tool (write_file, edit_file,
	// bash_exec) completes successfully, notify the vector index manager so it
	// triggers debounced incremental re-indexing. Also emit the
	// workspace:tree_changed event so the frontend (research panel, file tree,
	// etc.) refreshes its view after agent-initiated file writes. This is
	// essential because the filesystem watcher (fsnotify/FSEvents) has latency
	// on macOS and may miss same-process writes — the post-execute hook fires
	// synchronously after the tool returns, ensuring the frontend is notified
	// without delay.
	if cfg.FileChangeNotifyFunc != nil {
		notifyFn := cfg.FileChangeNotifyFunc
		emitFn := cfg.FileChangedWorkspaceEmitter
		builder.ToolRegistry().SetPostExecuteHook(func(_ context.Context, toolName string, res sdktools.ToolResult, execErr error) {
			// Only notify on a genuine successful file mutation. Skip error
			// results (policy deny, tool error) and non-nil execution errors
			// (confirmation denied, context cancellation, confirm-func failure)
			// where no file was actually modified.
			if execErr != nil || res.IsError {
				return
			}
			if !core.FileMutatingTools[toolName] {
				return
			}
			notifyFn()
			if emitFn != nil {
				emitFn()
			}
		})
	}

	// 4b. Smart Approve judge observer: the strict judge evaluates an
	// escalated call BEFORE any confirmation card exists, so the session
	// status must say a judge is working instead of implying a pending user
	// response. The observer resolves the session from the executor context
	// and emits the transient tool_judge_started/finished session events
	// (activity-tracked via the session emitter). Session registries inherit
	// the observer through Clone.
	builder.ToolRegistry().SetJudgeObserver(func(ctx context.Context, phase coretools.JudgePhase, toolName string) {
		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			app.log().Debug("judge observer: no session in context", "tool", toolName)
			return
		}
		app.manager.EmitJudgePhase(sessionID, phase == coretools.JudgePhaseStarted, toolName)
	})

	// 5. Orchestrator factory closure for the session manager.
	factory := func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer, stepDumpTracker *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		orchCfg := ToBuilderConfig(cfg.Config)
		// The lazy python probe is consumed at tool registration (builder
		// creation); propagate it here as well so any future Build-side
		// consumer sees the closure instead of a zero value.
		orchCfg.MarkitdownPythonPath = markitdownPython
		orch, err := builder.Build(orchCfg, emitter, logger, workspacePath, bbFactory, app.hitlHandler, dumpWriter, stepDumpTracker)
		if err != nil {
			return nil, err
		}
		// Inject the goal proposer so goal-mode derivation (propose_goal) can
		// reach the desktop approval flow. Nil is valid — goal mode simply
		// fails fast when invoked.
		if app.goalProposer != nil {
			orch.SetGoalProposer(app.goalProposer)
		}
		return orch, nil
	}

	// 6. Session manager.
	manager := session.NewManager(factory, emitFunc, cfg.AgentDir)
	if cfg.SessionStore != nil {
		manager.SetTokenPersist(func(sessionID string, inputTokens, outputTokens int, model, family string, fillPercent float64) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := cfg.SessionStore.UpdateSessionTokens(ctx, sessionID, inputTokens, outputTokens, model, family, fillPercent); err != nil {
				app.log().Warn("failed to persist session tokens", "session", sessionID, "error", err)
			}
		})
	}
	if cfg.TaskStore != nil {
		manager.SetTaskStore(cfg.TaskStore)
	}
	// Launch environment-info collection in the background. envInfo is
	// optional (SendMessage/ResumeTask tolerate nil) and only enriches the
	// system prompt once ready — collecting it synchronously here blocked
	// cold startup on ~7 subprocess probes (~0.75s).
	manager.StartEnvInfoCollection()
	manager.SetMaxSummaryLen(cfg.Config.Orchestration.MaxSummaryLength)
	// Annotate agent quality metrics with the active Small-LLM profile (if any).
	manager.SetSmallLLMProfile(effectiveSmallLLMConfig(cfg.Config))
	manager.SetServiceLLMTimeout(time.Duration(cfg.Config.Timeouts.ServiceLLMRequestTimeout) * time.Second)
	if cfg.SessionStore != nil {
		manager.SetSessionStore(cfg.SessionStore)
	}
	app.manager = manager

	// 7. Title generator backed by the builder's cached LLM router.
	app.titleGen = session.NewTitleGenerator(app.builder)
	manager.SetTitleGenerator(app.titleGen)

	return app, nil
}

// Manager returns the session manager.
func (app *Application) Manager() *session.Manager {
	return app.manager
}

// Builder returns the orchestrator builder for advanced operations.
func (app *Application) Builder() *core.OrchestratorBuilder {
	return app.builder
}

// SetGoalProposer sets the goal-proposer hook that the orchestrator factory
// injects onto every per-session orchestrator. Desktop calls this after
// construction, once its pending-confirmation map + emitter are ready, so the
// proposer is in place before any session's orchestrator is built.
func (app *Application) SetGoalProposer(proposer coretools.GoalProposer) {
	app.goalProposer = proposer
}

// TitleGenerator returns the session title generator.
func (app *Application) TitleGenerator() *session.TitleGenerator {
	return app.titleGen
}

// EvaluateJudge performs an on-demand judge evaluation for a pending tool
// confirmation using the SHARED registry's judge, which is bound to the
// builder's global active provider/model. Prefer EvaluateJudgeForSession for
// evaluations that belong to a live session: it pins the verdict to the
// session's own provider/model so manual and automatic judge evaluations
// cannot disagree across models. Returns the verdict, reasoning (prefixed
// with "SAFE: " when allowed), and any error.
func (app *Application) EvaluateJudge(ctx context.Context, toolName string, input json.RawMessage, taskContext string) (verdict sdktools.JudgeVerdict, reasoning string, err error) {
	if err := app.builder.WaitReady(ctx); err != nil {
		return sdktools.VerdictConfirm, "", fmt.Errorf("judge not available: %w", err)
	}
	registry := app.builder.ToolRegistry()
	if registry == nil {
		return sdktools.VerdictConfirm, "", ErrJudgeNotAvailable
	}
	judge := registry.GetJudge()
	if judge == nil {
		return sdktools.VerdictConfirm, "", ErrJudgeNotAvailable
	}
	return evaluateJudgeWith(ctx, judge, toolName, input, taskContext)
}

// EvaluateJudgeForSession performs an on-demand judge evaluation for a pending
// tool confirmation using the SESSION-pinned judge: the judge bound to the
// session's own router, so the manual "Judge" action on a confirmation card
// evaluates on the same provider and model the session runs on — exactly like
// automatic escalations (session-pinning invariant, ADR-028). Falls back to
// the shared registry's judge (EvaluateJudge) when the session is unknown,
// has no orchestrator or registry yet, or its judge is not bound, so a manual
// evaluation never fails merely because session context is unavailable.
func (app *Application) EvaluateJudgeForSession(ctx context.Context, sessionID, toolName string, input json.RawMessage, taskContext string) (verdict sdktools.JudgeVerdict, reasoning string, err error) {
	if sessionID != "" && app.manager != nil {
		if sess, ok := app.manager.GetSession(sessionID); ok {
			if orch := sess.GetOrchestrator(); orch != nil {
				if registry := orch.ToolRegistry(); registry != nil {
					if judge := registry.GetJudge(); judge != nil {
						return evaluateJudgeWith(ctx, judge, toolName, input, taskContext)
					}
				}
			}
		}
	}
	return app.EvaluateJudge(ctx, toolName, input, taskContext)
}

// evaluateJudgeWith runs a single judge evaluation and prefixes the reasoning
// for safe verdicts so the UI can display contextual info.
func evaluateJudgeWith(ctx context.Context, judge *sdktools.ToolJudge, toolName string, input json.RawMessage, taskContext string) (sdktools.JudgeVerdict, string, error) {
	verdict, reasoning, err := judge.Judge(ctx, toolName, input, taskContext)
	if err != nil {
		return verdict, reasoning, err
	}
	if verdict == sdktools.VerdictAllow {
		reasoning = "SAFE: " + reasoning
	}
	return verdict, reasoning, nil
}

// GetMCPStatus returns the status of all MCP servers. It is non-blocking so the
// settings dialog does not stall during the first seconds of startup while the
// MCP gateway is still discovering remote servers.
//
// While MCP startup is in flight (!MCPStartupDone()), it returns a single
// placeholder entry (Name "_gateway", Starting true) that the frontend renders
// as a neutral "Starting…" state rather than an error. Once startup finishes,
// if the gateway failed to start the same placeholder surfaces the error;
// otherwise it returns the live per-server status from the gateway.
func (app *Application) GetMCPStatus() []mcp.ServerStatus {
	if !app.builder.MCPStartupDone() {
		return []mcp.ServerStatus{{
			Name:     "_gateway",
			Starting: true,
		}}
	}
	gw := app.builder.MCPGatewayNoWait()
	if gw == nil {
		if errMsg := app.builder.MCPGatewayError(); errMsg != "" {
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
func (app *Application) ListTools() []sdktools.ToolDescriptor {
	return app.builder.ToolRegistry().List()
}

// GroupPolicies returns the live group→policy map enforced by the shared
// tool registry (what security.groups resolved to after the builder applied
// them). Callers use it to report each tool's EFFECTIVE policy — the same
// map Execute consults — instead of re-deriving it from config.
func (app *Application) GroupPolicies() map[sdktools.ToolGroup]sdktools.ToolPolicy {
	return app.builder.ToolRegistry().GroupPolicies()
}

// Shutdown stops all managed resources (manager, MCP gateway).
func (app *Application) Shutdown() {
	if app.manager != nil {
		app.manager.Shutdown()
	}
	if app.builder != nil {
		if err := app.builder.StopGateway(); err != nil {
			app.log().Error("failed to stop MCP gateway", "error", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

// ErrJudgeNotAvailable is returned when a judge evaluation is requested but
// no judge is configured.
var ErrJudgeNotAvailable = errJudgeNotAvailable("judge is not available; check LLM provider configuration")

type errJudgeNotAvailable string

func (e errJudgeNotAvailable) Error() string { return string(e) }

// resolveSkillDirs converts a list of configured skill or agent directories
// into absolute paths. Leading `~` and `${ENV_VAR}` are expanded; remaining
// relative paths are resolved against agentDir. Entries that expand to an empty
// string after substitution are dropped.
//
// log is used to emit warnings on non-critical resolution failures (e.g.,
// missing home directory for tilde expansion). If nil, warnings are suppressed.
func resolveSkillDirs(dirs []string, agentDir string, expandEnv func(string) string, log *slog.Logger) []string {
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
// directory. Paths that do not start with `~` are returned unchanged.
// When home is empty, a leading `~` is left intact so the caller can decide
// how to handle the failure (e.g. treat it as a relative path).
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

// EmitSessionEvent emits a session event through the combined UI + persistence
// path. Desktop-layer callbacks (e.g. plan approval) use this instead of the
// raw UI emitter so events survive app restarts.
func (app *Application) EmitSessionEvent(evt session.Event) {
	if app.emitFunc != nil {
		app.emitFunc(evt)
	}
}

// EmitToolConfirm routes a tool-confirmation request through the live session
// emitter so the activity tracker (and the runtime-status snapshot read on
// session switches) reports "Awaiting confirmation..." while the agent
// goroutine blocks on the user's decision. Desktop's confirm callback calls
// this instead of the raw UI emitter.
func (app *Application) EmitToolConfirm(sessionID string, payload session.ToolConfirmPayload) {
	if app.manager != nil {
		app.manager.EmitToolConfirm(sessionID, payload)
		return
	}
	app.EmitSessionEvent(session.Event{SessionID: sessionID, Type: "tool_confirm", Data: payload})
}

// LastToolCallID returns the most recently emitted tool_call_id for a session
// (and its tool name). The desktop tool-confirmation callback uses it to
// attach the matching tool_call_id to the tool_confirm payload so the frontend
// can correlate a confirmation with the exact tool_call event.
func (app *Application) LastToolCallID(sessionID string) (id, tool string) {
	if app.manager == nil {
		return "", ""
	}
	return app.manager.LastToolCallID(sessionID)
}
