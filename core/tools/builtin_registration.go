package tools

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/tools/builtins"
	"github.com/v0lka/sp4rk/tools/builtins/websearch"
)

// BuiltinToolsConfig holds configuration for registering built-in tools.
// All limit and callback types are imported directly from their source packages
// per ADR-008 (no type re-exports).
type BuiltinToolsConfig struct {
	FileLimits          builtins.FileLimits
	RipgrepLimits       builtins.RipgrepLimits
	WebFetchLimits      builtins.WebFetchLimits
	WebSearchLimits     builtins.WebSearchLimits
	BashTimeouts        builtins.BashTimeouts
	ShellBlacklist      []string
	ExtraShellBlacklist []string

	// Search provider configuration.
	SearchProvider string
	SearchAPIKey   string
	SearchTimeout  time.Duration

	// HTTPClient is an optional proxy-configured client for web tools.
	// If nil, tools create their own default clients.
	HTTPClient *http.Client

	// AskUserFunc is the callback for the ask_user tool.
	// If nil, the ask_user tool is not registered.
	AskUserFunc AskUserFunc

	// PlanApprovalFunc is the callback for the declare_plan tool's await_approval mode.
	// If nil, declare_plan is still registered but await_approval mode returns an error.
	PlanApprovalFunc ApprovalFunc

	// VectorSearchFunc is the callback for the semantic_search tool.
	// If nil, the semantic_search tool is not registered.
	VectorSearchFunc builtins.VectorSearchFunc

	// VectorSearchWaitFunc blocks until the vector index is ready.
	VectorSearchWaitFunc builtins.VectorSearchWaitFunc

	// Logger is threaded through to tools that log (e.g. the read_file document
	// converter). If nil, those tools fall back to a discard handler.
	Logger *slog.Logger

	// MarkitdownPythonPath lazily resolves the managed venv interpreter used
	// by the read_file document wrapper for vision-assisted conversion (nil
	// or an empty result disables vision assistance; plain conversions are
	// unaffected). Probed at the wrapper's first converter init because the
	// tool-manager installs the venv asynchronously after startup.
	// See BuilderConfig.MarkitdownPythonPath.
	MarkitdownPythonPath func() string
}

// RegisterBuiltinTools creates and registers all built-in tools into the registry.
func RegisterBuiltinTools(registry *ToolRegistry, cfg BuiltinToolsConfig) error {
	// Shell execution (bash_exec on Unix, posh_exec on Windows).
	// Merge configured blacklist with any extra patterns; the platform-specific
	// constructor call lives in shelltool_{unix,windows}.go behind build tags,
	// because sp4rk's bash.go and posh.go are mutually exclusive per OS.
	allBlacklist := make([]string, 0, len(cfg.ShellBlacklist)+len(cfg.ExtraShellBlacklist))
	allBlacklist = append(allBlacklist, cfg.ShellBlacklist...)
	allBlacklist = append(allBlacklist, cfg.ExtraShellBlacklist...)
	shellTool, err := newShellExecTool(allBlacklist, cfg.BashTimeouts)
	if err != nil {
		return fmt.Errorf("shell tool: %w", err)
	}
	registry.Register(shellTool)

	// File operations
	// read_file is wrapped to transparently convert document formats (pdf,
	// docx, pptx, etc.) to markdown via markitdown. Plain-text files delegate
	// to the inner sp4rk ReadFileTool unchanged.
	registry.Register(NewReadFileDocTool(cfg.FileLimits, cfg.Logger, cfg.MarkitdownPythonPath))
	registry.Register(builtins.NewWriteFileTool())
	registry.Register(builtins.NewEditFileTool())
	registry.Register(builtins.NewListDirectoryTool())
	registry.Register(builtins.NewCreateDirectoryTool())
	registry.Register(builtins.NewDeleteDirectoryTool())
	registry.Register(builtins.NewDeleteFileTool())

	// Finish
	registry.Register(agent.NewFinishTool())

	// Web fetch
	registry.Register(builtins.NewWebFetchToolWithClient(cfg.WebFetchLimits, cfg.HTTPClient))

	// Web search (optional)
	if provider := CreateSearchProviderWithClient(cfg.SearchProvider, cfg.SearchAPIKey, cfg.SearchTimeout, cfg.HTTPClient); provider != nil {
		registry.Register(websearch.NewTool(provider, cfg.WebSearchLimits))
	}

	// Glob and ripgrep
	registry.Register(builtins.NewGlobTool())
	registry.Register(builtins.NewRipgrepToolWithLimits(cfg.RipgrepLimits))

	// Tool result cache reader
	registry.Register(builtins.NewToolResultReadTool())

	// Batch meta-tool
	registry.Register(builtins.NewBatchTool())

	// Step output tools
	registry.Register(builtins.NewReadStepOutputTool())
	registry.Register(builtins.NewListStepOutputsTool())
	registry.Register(builtins.NewReadFinalResultTool())

	// Checklist tool (update_checklist) + inline step completion (declare_step_complete)
	registry.Register(builtins.NewUpdateChecklistTool())
	registry.Register(NewDeclareStepCompleteTool())

	// Fact memory tools
	registry.Register(builtins.NewStoreFactTool())
	registry.Register(builtins.NewSearchFactsTool())

	// Attachment reader — reads user-attached files from the blackboard via
	// the context-injected AttachmentStore. The attachment IDs are surfaced to
	// the LLM in the user message (see Orchestrator.augmentWithAttachments).
	registry.Register(builtins.NewReadAttachmentTool())

	// Semantic search (optional — requires vector index backend)
	if cfg.VectorSearchFunc != nil {
		registry.Register(builtins.NewVectorSearchTool(cfg.VectorSearchFunc, cfg.VectorSearchWaitFunc))
	}

	// Ask user (optional)
	if cfg.AskUserFunc != nil {
		registry.Register(NewAskUserTool(cfg.AskUserFunc))
	}

	// Conductor tools — delegate, cancel_delegation, reflect read their
	// dependencies from the request context (DelegationLauncher, DelegationRegistry,
	// ReflectionRunner), so they are safe to register unconditionally. They are
	// no-ops outside a Conductor run (the context values will be nil).
	registry.Register(NewDelegateTool())
	registry.Register(NewCancelDelegationTool())
	registry.Register(NewReflectTool())

	// Declare plan (approval callback optional; present mode works without it)
	registry.Register(NewDeclarePlanTool(cfg.PlanApprovalFunc))

	// Propose goal — reads the goal proposer from the request context, so it
	// is safe to register unconditionally. It is a no-op outside a Conductor
	// run (the context value will be nil), matching declare_plan's pattern.
	registry.Register(NewProposeGoalTool())

	// Declare goal status — writes a self-evaluation verdict into the
	// context-injected GoalStatusSink. Safe to register unconditionally; it is
	// a no-op outside a goal-loop run (the sink will be nil), matching
	// propose_goal's pattern. Status "met" requires non-empty evidence.
	registry.Register(NewDeclareGoalStatusTool())

	// Declare verification — writes a verifier verdict into the
	// context-injected VerificationSink. Safe to register unconditionally; it
	// is a no-op outside a verification run (the sink will be nil), matching
	// declare_goal_status's pattern. confirmed=true requires non-empty
	// evidence.
	registry.Register(NewDeclareVerificationTool())

	// Execute plan — reads the declared plan from the blackboard via a
	// PlanStepExecutor injected into the Conductor context. No-op outside a
	// Conductor run (the context value will be nil).
	registry.Register(NewExecutePlanTool())

	return nil
}

// CreateSearchProvider creates a search provider based on the configured provider name.
// Returns nil if the provider requires an API key but none is configured.
func CreateSearchProvider(providerName, apiKey string, timeout time.Duration) websearch.SearchProvider {
	return CreateSearchProviderWithClient(providerName, apiKey, timeout, nil)
}

// CreateSearchProviderWithClient creates a search provider with an optional HTTP client.
// Returns nil if the provider requires an API key but none is configured.
// Provider name matching is case-insensitive; unrecognized names fall back to tavily.
func CreateSearchProviderWithClient(providerName, apiKey string, timeout time.Duration, client *http.Client) websearch.SearchProvider {
	switch strings.ToLower(providerName) {
	case "tavily", "":
		if apiKey == "" {
			return nil
		}
		return websearch.NewTavilyProviderWithClient(apiKey, timeout, client)
	case "brave":
		if apiKey == "" {
			return nil
		}
		return websearch.NewBraveProviderWithClient(apiKey, timeout, client)
	case "exa":
		if apiKey == "" {
			return nil
		}
		return websearch.NewExaProviderWithClient(apiKey, timeout, client)
	case "duckduckgo":
		return websearch.NewDuckDuckGoProviderWithClient(timeout, client)
	default:
		// Unrecognized provider name — treat as tavily.
		if apiKey == "" {
			return nil
		}
		return websearch.NewTavilyProviderWithClient(apiKey, timeout, client)
	}
}

// UpdateSearchTool replaces or removes the web_search tool in the registry
// based on the given search configuration.
func UpdateSearchTool(registry *ToolRegistry, providerName, apiKey string, limits builtins.WebSearchLimits) {
	UpdateSearchToolWithClient(registry, providerName, apiKey, limits, nil)
}

// UpdateShellTool re-registers the shell-execution tool (bash_exec on Unix,
// posh_exec on Windows) with an updated command blacklist. The blacklist is
// compiled into the tool at construction time, so a runtime edit of
// security.groups.execute.blacklist takes effect by replacing the registered
// instance — mirroring UpdateSearchTool. A pattern that fails to compile is
// reported as an error and leaves the previously registered tool in place.
func UpdateShellTool(registry *ToolRegistry, blacklist []string, timeouts builtins.BashTimeouts) error {
	shellTool, err := newShellExecTool(blacklist, timeouts)
	if err != nil {
		return fmt.Errorf("shell tool: %w", err)
	}
	registry.Register(shellTool)
	return nil
}

// UpdateSearchToolWithClient replaces or removes the web_search tool in the registry
// with an optional HTTP client for proxy support.
func UpdateSearchToolWithClient(registry *ToolRegistry, providerName, apiKey string, limits builtins.WebSearchLimits, client *http.Client) {
	if provider := CreateSearchProviderWithClient(providerName, apiKey, limits.Timeout, client); provider != nil {
		registry.Register(websearch.NewTool(provider, limits))
	} else {
		registry.Unregister("web_search")
	}
}

// UpdateWebFetchTool replaces the web_fetch tool in the registry with an optional HTTP client.
func UpdateWebFetchTool(registry *ToolRegistry, limits builtins.WebFetchLimits, client *http.Client) {
	registry.Register(builtins.NewWebFetchToolWithClient(limits, client))
}
