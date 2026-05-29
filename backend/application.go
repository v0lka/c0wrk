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

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/tools"
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

	// 3b. Skill discovery directories. The builder creates a per-session
	// SkillManager on each Build() and always prepends the current project's
	// `.agents/skills` directory (see core/builder.go). Here we only resolve
	// and register the shared base dirs from config.
	if len(cfg.Config.Skills.Dirs) > 0 {
		skillDirs := resolveSkillDirs(cfg.Config.Skills.Dirs, cfg.AgentDir, config.ExpandEnvVars)
		builder.SetSkillDirs(skillDirs)
	}

	// 4. Set confirmation function on the shared registry.
	if cfg.ConfirmFunc != nil {
		builder.ToolRegistry().SetConfirmFunc(cfg.ConfirmFunc)
	}

	// 5. Orchestrator factory closure for the session manager.
	factory := func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer) (*core.Orchestrator, error) {
		return builder.Build(ToBuilderConfig(cfg.Config), emitter, logger, workspacePath, bbFactory, app.stepLimitFunc, dumpWriter)
	}

	// 6. Session manager.
	manager := session.NewManager(factory, emitFunc, cfg.LogDir, cfg.ProjectsDir)
	if cfg.SessionStore != nil {
		manager.SetTokenPersist(func(sessionID string, inputTokens, outputTokens int, model, family string) {
			if err := cfg.SessionStore.UpdateSessionTokens(context.Background(), sessionID, inputTokens, outputTokens, model, family); err != nil {
				app.log().Warn("failed to persist session tokens", "session", sessionID, "error", err)
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

// EvaluateJudge performs an on-demand judge evaluation for a pending tool confirmation.
// Returns the verdict, reasoning (prefixed with "SAFE: " when allowed), and any error.
func (app *Application) EvaluateJudge(ctx context.Context, toolName string, input json.RawMessage, taskContext string) (verdict JudgeVerdict, reasoning string, err error) {
	if err := app.builder.WaitReady(ctx); err != nil {
		return VerdictConfirm, "", fmt.Errorf("judge not available: %w", err)
	}
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
// If the gateway failed to start, returns a placeholder entry surfacing the error.
func (app *Application) GetMCPStatus() []MCPServerStatus {
	gw := app.builder.MCPGateway()
	if gw == nil {
		if errMsg := app.builder.MCPGatewayError(); errMsg != "" {
			return []MCPServerStatus{{
				Name:  "_gateway",
				Error: errMsg,
			}}
		}
		return []MCPServerStatus{}
	}
	return gw.Status()
}

// ListTools returns descriptors for all registered tools.
func (app *Application) ListTools() []ToolDescriptor {
	return app.builder.ToolRegistry().List()
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

// resolveSkillDirs converts a list of configured skill directories into absolute
// paths. Leading `~` and `${ENV_VAR}` are expanded; remaining relative paths
// are resolved against agentDir. Entries that expand to an empty string after
// substitution are dropped.
func resolveSkillDirs(dirs []string, agentDir string, expandEnv func(string) string) []string {
	home, _ := os.UserHomeDir()
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
