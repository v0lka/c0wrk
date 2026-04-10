package desktop

import (
	"bufio"
	"context"
	"database/sql"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/user/agent/sdk/llm"

	"github.com/user/agent/backend/config"
	"github.com/user/agent/backend/logger"
	"github.com/user/agent/backend/project"
	"github.com/user/agent/backend/session"
	"github.com/user/agent/backend/workspace"
	"github.com/user/agent/core/tools"
	"github.com/user/agent/core/tools/mcp"
)

// loadShellEnvironment loads environment variables from the user's shell profile.
// This is necessary on macOS where apps launched from Finder/Dock don't inherit
// shell environment variables (like those set in .zshrc/.bash_profile).
// The function is best-effort: failures are logged but don't block startup.
func loadShellEnvironment() {
	// Only needed on macOS; Linux inherits environment normally
	if runtime.GOOS != "darwin" {
		return
	}

	// Get user's shell from SHELL env var, fallback to zsh on macOS
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}

	// Run shell with -l (login) flag to source profile files
	// We avoid -i (interactive) to prevent extra output from .zshrc/.bashrc
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-l", "-c", "printenv")
	cmd.Stderr = nil // Discard stderr to avoid noise

	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			slog.Warn("timeout loading shell environment", "shell", shell)
		} else {
			slog.Warn("failed to load shell environment", "shell", shell, "error", err)
		}
		return
	}

	// Parse KEY=VALUE lines and set environment variables
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	loaded := 0
	var setErrors int
	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines
		if line == "" {
			continue
		}

		// Find first '=' to split key and value
		eqIdx := strings.Index(line, "=")
		if eqIdx <= 0 {
			// Line without '=' or starting with '=' is invalid; skip
			continue
		}

		key := line[:eqIdx]
		value := line[eqIdx+1:]

		// Don't override already-set variables; shell profile is source of truth
		// but we want to respect explicit env vars if set by launcher
		if os.Getenv(key) != "" {
			continue
		}

		if err := os.Setenv(key, value); err != nil {
			setErrors++
			// Log but continue - some vars may not be settable
			slog.Debug("failed to set env var", "key", key, "error", err)
			continue
		}
		loaded++
	}

	if setErrors > 0 {
		slog.Warn("some shell environment variables could not be set", "failed", setErrors, "loaded", loaded)
	}

	if loaded > 0 {
		slog.Debug("loaded shell environment variables", "count", loaded, "shell", shell)
	}
}

// App struct holds the Wails application state and exposes methods to the frontend.

// llmTitleCaller adapts the LLM router to the session.LLMTitleCaller interface.
type llmTitleCaller struct {
	router *llm.Router
}

func (c *llmTitleCaller) GenerateTitle(ctx context.Context, userMessage string) (string, error) {
	temp := 0.3
	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "Generate a concise title (3-7 words) for a conversation that starts with the following user message. Output ONLY the title text, no quotes, no punctuation at the end."},
			{Role: "user", Content: userMessage},
		},
		MaxTokens:   30,
		Temperature: &temp,
	}
	resp, err := c.router.Call(ctx, req)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}
	return resp.Message.Content, nil
}

// App holds the Wails application state and exposes methods to the frontend.
type App struct {
	ctx        context.Context
	manager    *session.Manager
	db         *sql.DB // shared SQLite connection
	store      *session.SQLiteSessionStore
	projStore  *project.SQLiteProjectStore
	config     *config.Config
	configMu   sync.RWMutex // protects config and config-related state
	configPath string

	llmRouter    *llm.Router
	toolRegistry *tools.ToolRegistry
	mcpGateway   *mcp.Gateway

	sessionLogger *logger.SessionLogger
	logLevel      string

	// Config loading state for UI warnings
	configMigrated     bool
	configMigrationMsg string
	configLoadErrors   []string

	pendingConfirmations sync.Map
	pendingAskUser       sync.Map

	envInfo *tools.EnvInfo

	watcher        *workspace.Watcher
	projectManager *project.Manager

	projectsDir       string       // ~/.c0wrk/Projects/
	activeProjectID   string       // currently active project ID
	activeProjectPath string       // workspace path of active project
	activeProjectMu   sync.RWMutex // protects activeProjectID/Path
}

// currentConfig returns a shallow copy of the current config, safe to read without holding the lock.
// Slices and maps are deep-copied so the snapshot is fully independent from the live config.
func (a *App) currentConfig() *config.Config {
	a.configMu.RLock()
	defer a.configMu.RUnlock()
	if a.config == nil {
		return nil
	}
	cfgCopy := *a.config

	// Deep-copy LLM.Models (map)
	if a.config.LLM.Models != nil {
		cfgCopy.LLM.Models = make(map[string]config.ModelOverride, len(a.config.LLM.Models))
		for k, v := range a.config.LLM.Models {
			cfgCopy.LLM.Models[k] = v
		}
	}

	// Deep-copy MCP.Servers (map containing slices and maps)
	if a.config.MCP.Servers != nil {
		cfgCopy.MCP.Servers = make(map[string]config.MCPServerConfig, len(a.config.MCP.Servers))
		for k, v := range a.config.MCP.Servers {
			srv := v
			if v.Args != nil {
				srv.Args = make([]string, len(v.Args))
				copy(srv.Args, v.Args)
			}
			if v.Env != nil {
				srv.Env = make(map[string]string, len(v.Env))
				for ek, ev := range v.Env {
					srv.Env[ek] = ev
				}
			}
			cfgCopy.MCP.Servers[k] = srv
		}
	}

	// Deep-copy Security.ToolPolicies (map containing slices)
	if a.config.Security.ToolPolicies != nil {
		cfgCopy.Security.ToolPolicies = make(map[string]config.ToolPolicyConfig, len(a.config.Security.ToolPolicies))
		for k, v := range a.config.Security.ToolPolicies {
			tp := v
			if v.Blacklist != nil {
				tp.Blacklist = make([]string, len(v.Blacklist))
				copy(tp.Blacklist, v.Blacklist)
			}
			cfgCopy.Security.ToolPolicies[k] = tp
		}
	}

	return &cfgCopy
}

// NewApp creates a new App instance.
func NewApp() *App {
	return &App{}
}
