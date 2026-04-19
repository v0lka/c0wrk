package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/user/agent/backend/config"
	"github.com/user/agent/backend/session"
	"github.com/user/agent/core"
	"github.com/user/agent/core/tools"
)

// ApplicationConfig holds all parameters needed to construct an Application.
// Desktop provides the UI callbacks; everything else is derived from config.
type ApplicationConfig struct {
	Config      *config.Config
	Logger      *slog.Logger
	AgentDir    string // base agent directory (e.g. ~/.c0wrk)
	LogDir      string // directory for session log files
	ProjectsDir string // base directory for project temp dirs

	// Persistence stores (optional — nil disables corresponding functionality).
	SessionStore session.SessionStore
	TaskStore    session.TaskStore

	// UI callbacks provided by the desktop adapter.
	UIEmitFunc    func(session.Event) // Wails event emission
	AskUserFunc   AskUserFunc         // ask_user tool callback
	ConfirmFunc   ConfirmFunc         // tool confirmation callback
	StepLimitFunc StepLimitFunc       // step limit callback

	// Vector search callbacks (optional — nil disables semantic_search tool).
	VectorSearchFunc     tools.VectorSearchFunc
	VectorSearchWaitFunc tools.VectorSearchWaitFunc
}

// Application is the central ViewModel that ties together the OrchestratorBuilder,
// session Manager, and event persistence. Desktop imports only this package.
type Application struct {
	builder   *core.OrchestratorBuilder
	manager   *session.Manager
	persister *session.EventPersister
	titleGen  *session.TitleGenerator
	envInfo   *EnvInfo
	logger    *slog.Logger

	// stepLimitFunc is captured for the orchestrator factory closure.
	stepLimitFunc StepLimitFunc
}

// NewApplication creates a fully-initialized Application.
// It builds the shared tool registry, MCP gateway, LLM router, session manager,
// and event persister from the given configuration.
func NewApplication(cfg ApplicationConfig) (*Application, error) {
	app := &Application{
		logger:        cfg.Logger,
		stepLimitFunc: cfg.StepLimitFunc,
	}

	// Collect environment info once for all sessions.
	envInfo := CollectEnvInfo()
	app.envInfo = envInfo

	// 1. Event persister (SQLite persistence, separate from UI emission).
	app.persister = session.NewEventPersister(cfg.SessionStore)

	// 2. Combined emit function: UI emission + persistence.
	emitFunc := func(evt session.Event) {
		if cfg.UIEmitFunc != nil {
			cfg.UIEmitFunc(evt)
		}
		app.persister.Persist(evt)
	}

	// 3. OrchestratorBuilder (owns registry, gateway, router, judge).
	builderCfg := ToBuilderConfig(cfg.Config)
	builder, err := core.NewOrchestratorBuilder(builderCfg, cfg.AskUserFunc, cfg.Logger)
	if err != nil {
		return nil, err
	}
	app.builder = builder

	// 3a. Vector search (optional — registered after builder creation)
	if cfg.VectorSearchFunc != nil {
		builder.RegisterVectorSearch(cfg.VectorSearchFunc, cfg.VectorSearchWaitFunc)
	}

	// 4. Set confirmation function on the shared registry.
	if cfg.ConfirmFunc != nil {
		builder.ToolRegistry().SetConfirmFunc(cfg.ConfirmFunc)
	}

	// 5. Orchestrator factory closure for the session manager.
	factory := func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer) (*core.Orchestrator, error) {
		return builder.Build(ToBuilderConfig(cfg.Config), emitter, logger, bbFactory, app.stepLimitFunc, dumpWriter)
	}

	// 6. Session manager.
	manager := session.NewManager(factory, emitFunc, cfg.LogDir, cfg.ProjectsDir)
	if cfg.SessionStore != nil {
		manager.SetTokenPersist(func(sessionID string, inputTokens, outputTokens int, model, family string) {
			if err := cfg.SessionStore.UpdateSessionTokens(sessionID, inputTokens, outputTokens, model, family); err != nil {
				slog.Warn("failed to persist session tokens", "session", sessionID, "error", err)
			}
		})
	}
	if cfg.TaskStore != nil {
		manager.SetTaskStore(cfg.TaskStore)
	}
	manager.SetEnvInfo(envInfo)
	manager.SetMaxSummaryLen(cfg.Config.Orchestration.MaxSummaryLength)
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

// EnvInfo returns the collected environment info.
func (app *Application) EnvInfo() *EnvInfo {
	return app.envInfo
}

// TitleGenerator returns the session title generator.
func (app *Application) TitleGenerator() *session.TitleGenerator {
	return app.titleGen
}

// RebuildFactory creates a new orchestrator factory closure from the given config.
// Call this when config changes so that new sessions use the updated settings.
func (app *Application) RebuildFactory(cfg *config.Config) {
	factory := func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer) (*core.Orchestrator, error) {
		return app.builder.Build(ToBuilderConfig(cfg), emitter, logger, bbFactory, app.stepLimitFunc, dumpWriter)
	}
	app.manager.SetFactory(factory)
}

// EvaluateJudge performs an on-demand judge evaluation for a pending tool confirmation.
// Returns the verdict, reasoning (prefixed with "SAFE: " when allowed), and any error.
func (app *Application) EvaluateJudge(ctx context.Context, toolName string, input json.RawMessage, taskContext string) (verdict JudgeVerdict, reasoning string, err error) {
	judge := app.builder.ToolRegistry().GetJudge()
	if judge == nil {
		return VerdictConfirm, "", ErrJudgeNotAvailable
	}
	verdict, reasoning, err = judge.Judge(ctx, toolName, input, taskContext)
	if err != nil {
		return verdict, reasoning, err
	}
	// Prefix reasoning for safe verdicts so the UI can display contextual info.
	if verdict == VerdictAllow {
		reasoning = "SAFE: " + reasoning
	}
	return verdict, reasoning, nil
}

// GetMCPStatus returns the status of all MCP servers.
func (app *Application) GetMCPStatus() []MCPServerStatus {
	gw := app.builder.MCPGateway()
	if gw == nil {
		return []MCPServerStatus{}
	}
	return gw.Status()
}

// ListTools returns descriptors for all registered tools.
func (app *Application) ListTools() []ToolDescriptor {
	return app.builder.ToolRegistry().List()
}

// MCPToolResult wraps the result of calling an MCP tool.
// This avoids exposing the MCP SDK types to the desktop layer.
type MCPToolResult struct {
	IsError bool
	Content []any
}

// IsMCPServerConnected returns whether the named MCP server is connected.
func (app *Application) IsMCPServerConnected(serverName string) bool {
	gw := app.builder.MCPGateway()
	if gw == nil {
		return false
	}
	server := gw.GetServer(serverName)
	return server != nil && server.IsConnected()
}

// CallMCPTool invokes a tool on the named MCP server and returns a wrapped result.
func (app *Application) CallMCPTool(ctx context.Context, serverName, toolName string, args map[string]any) (*MCPToolResult, error) {
	gw := app.builder.MCPGateway()
	if gw == nil {
		return nil, errors.New("MCP gateway not available")
	}
	server := gw.GetServer(serverName)
	if server == nil || !server.IsConnected() {
		return nil, fmt.Errorf("MCP server %q not connected", serverName)
	}
	result, err := server.CallTool(ctx, toolName, args)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	content := make([]any, len(result.Content))
	for i, c := range result.Content {
		content[i] = c
	}
	return &MCPToolResult{
		IsError: result.IsError,
		Content: content,
	}, nil
}

// Shutdown stops all managed resources (manager, MCP gateway).
func (app *Application) Shutdown() {
	if app.manager != nil {
		app.manager.Shutdown()
	}
	if app.builder != nil {
		if err := app.builder.StopGateway(); err != nil {
			slog.Error("failed to stop MCP gateway", "error", err)
		}
	}
}

// SetBashRtkPath updates the rtk binary path on the bash_exec tool.
func (app *Application) SetBashRtkPath(path string) {
	if app.builder != nil {
		app.builder.SetBashRtkPath(path)
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
