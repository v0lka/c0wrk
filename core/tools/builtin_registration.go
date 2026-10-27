package tools

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/v0lka/sp4rk/agent"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
	"github.com/v0lka/sp4rk/tools/builtins/websearch"
)

// BuiltinToolsConfig holds built-in tool configuration; limit/callback types
// are imported from their source packages (ADR-008, no re-exports).
type BuiltinToolsConfig struct {
	FileLimits    builtins.FileLimits
	RipgrepLimits builtins.RipgrepLimits
	GlobLimits    builtins.GlobLimits
	// GlobLimitsExplicit selects the no-fallback glob constructor so an
	// explicitly all-zero config ("0 disables") is honored verbatim; the
	// default constructor's zero-struct→defaults fallback protects a fully
	// unset config from an unbounded walk.
	GlobLimitsExplicit bool
	WebFetchLimits     builtins.WebFetchLimits
	WebSearchLimits    builtins.WebSearchLimits
	BashTimeouts       builtins.BashTimeouts
	ShellBlocklist     []string
	// BashShellInvocation / PoshShellInvocation carry the optional shell_exec
	// launch-shape override; only the platform-matching entry is consumed,
	// nil keeps the built-in default (see newShellExecTool).
	BashShellInvocation *sdktools.ShellInvocation
	PoshShellInvocation *sdktools.ShellInvocation

	// Search provider configuration; SearchAPIKey/SearchBaseURL arrive
	// already ${ENV_VAR}-expanded (BuilderConfig.ExpandEnvVars).
	SearchProvider string
	SearchAPIKey   string
	SearchBaseURL  string
	SearchTimeout  time.Duration

	HTTPClient *http.Client

	// AskUserFunc nil = the ask_user tool is not registered at all.
	AskUserFunc AskUserFunc

	// SilentMode is the security.silent_mode posture; with AutonomyMode
	// silent and ask_user "disable", ask_user registers with a nil callback
	// so calls resolve to an explicit "not available" result
	// (specs/architecture/security-model.md, Silent Mode).
	SilentMode SilentModeState

	// AutonomyMode is the security.autonomy_mode posture; only "silent"
	// activates the silent-mode sub-policies above.
	AutonomyMode string

	// PlanApprovalFunc backs declare_plan's await_approval mode; nil keeps the
	// tool registered but errors that mode.
	PlanApprovalFunc ApprovalFunc

	// VectorSearchFunc is the callback for the semantic_search tool.
	// If nil, the semantic_search tool is not registered.
	VectorSearchFunc builtins.VectorSearchFunc

	// VectorSearchWaitFunc blocks until the vector index is ready.
	VectorSearchWaitFunc builtins.VectorSearchWaitFunc

	// Logger is threaded through to tools that log (e.g. the read_file document
	// converter). If nil, those tools fall back to a discard handler.
	Logger *slog.Logger

	// MarkitdownPythonPath lazily resolves the managed venv interpreter for
	// vision-assisted conversion; probed at the wrapper's first converter init
	// (the venv installs asynchronously). See BuilderConfig.MarkitdownPythonPath.
	MarkitdownPythonPath func() string
}

// AskUserDisabled is the single predicate behind ask_user's nil-callback
// registration, kept in lockstep with the config- and builder-level
// AskUserDisabled predicates.
func (c BuiltinToolsConfig) AskUserDisabled() bool {
	return c.AutonomyMode == AutonomyModeSilent && c.SilentMode.AskUser == SilentAskUserDisable
}

// RegisterBuiltinTools creates and registers all built-in tools into the registry.
func RegisterBuiltinTools(registry *ToolRegistry, cfg BuiltinToolsConfig) error {
	// Shell execution — the build-tagged constructor split is documented in
	// specs/domains/tool-system/builtins.md (Shell-Execution Tool).
	shellTool, err := newShellExecTool(cfg.ShellBlocklist, cfg.BashTimeouts, cfg.BashShellInvocation, cfg.PoshShellInvocation)
	if err != nil {
		return fmt.Errorf("shell tool: %w", err)
	}
	registry.Register(shellTool)

	// File operations — read_file is the document-converting wrapper
	// (specs/domains/tool-system/builtins.md, Document Conversion).
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
	searchProvider, err := CreateSearchProviderWithClient(cfg.SearchProvider, cfg.SearchAPIKey, cfg.SearchBaseURL, cfg.SearchTimeout, cfg.HTTPClient)
	if err != nil {
		return err
	}
	if searchProvider != nil {
		registry.Register(websearch.NewTool(searchProvider, cfg.WebSearchLimits))
	}

	// An explicitly customized glob config uses the no-fallback constructor
	// (all-zero "disable every bound" survives); otherwise defaults apply.
	if cfg.GlobLimitsExplicit {
		registry.Register(builtins.NewGlobToolWithLimitsOverride(cfg.GlobLimits))
	} else {
		registry.Register(builtins.NewGlobToolWithLimits(cfg.GlobLimits))
	}
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

	// Attachment reader — IDs are surfaced to the LLM in the user message
	// (Orchestrator.augmentWithAttachments).
	registry.Register(builtins.NewReadAttachmentTool())

	// Semantic search (optional — requires vector index backend)
	if cfg.VectorSearchFunc != nil {
		registry.Register(builtins.NewVectorSearchTool(cfg.VectorSearchFunc, cfg.VectorSearchWaitFunc))
	}

	// Ask user: nil callback registers the tool so calls resolve to the
	// explicit "not available" result instead of a missing-tool error
	// (specs/architecture/security-model.md, Silent Mode).
	if cfg.AskUserFunc != nil {
		askUser := cfg.AskUserFunc
		if cfg.AskUserDisabled() {
			askUser = nil
		}
		registry.Register(NewAskUserTool(askUser))
	}

	// Conductor tools read their collaborators from the request context, so
	// they register unconditionally and no-op outside a Conductor run
	// (specs/contracts/conductor-tools.md).
	registry.Register(NewDelegateTool())
	registry.Register(NewCancelDelegationTool())
	registry.Register(NewReflectTool())

	// Declare plan (approval callback optional; present mode works without it)
	registry.Register(NewDeclarePlanTool(cfg.PlanApprovalFunc))

	// Propose goal — context-injected proposer; no-op outside a derivation
	// run, matching declare_plan's pattern.
	registry.Register(NewProposeGoalTool())

	// Declare goal status — context-injected sink; "met" requires non-empty
	// evidence (specs/domains/goal-mode.md).
	registry.Register(NewDeclareGoalStatusTool())

	// Declare verification — context-injected sink; confirmed=true requires
	// non-empty evidence (specs/domains/goal-mode.md).
	registry.Register(NewDeclareVerificationTool())

	// Execute plan — reads the declared plan from the blackboard via a
	// PlanStepExecutor injected into the Conductor context. No-op outside a
	// Conductor run (the context value will be nil).
	registry.Register(NewExecutePlanTool())

	return nil
}

// CreateSearchProvider resolves the provider; nil when a required API key is
// unset, an error when the configuration is invalid.
func CreateSearchProvider(providerName, apiKey, baseURL string, timeout time.Duration) (websearch.SearchProvider, error) {
	return CreateSearchProviderWithClient(providerName, apiKey, baseURL, timeout, nil)
}

// CreateSearchProviderWithClient adds an optional HTTP client. Nil provider =
// optional tool stays unregistered; an invalid configured provider is an
// error so a broken setup never degrades into a silently missing tool.
// Case-insensitive names; unrecognized falls back to tavily.
func CreateSearchProviderWithClient(providerName, apiKey, baseURL string, timeout time.Duration, client *http.Client) (websearch.SearchProvider, error) {
	switch strings.ToLower(providerName) {
	case "tavily", "":
		if apiKey == "" {
			return nil, nil
		}
		return websearch.NewTavilyProviderWithClient(apiKey, timeout, client), nil
	case "brave":
		if apiKey == "" {
			return nil, nil
		}
		return websearch.NewBraveProviderWithClient(apiKey, timeout, client), nil
	case "exa":
		if apiKey == "" {
			return nil, nil
		}
		return websearch.NewExaProviderWithClient(apiKey, timeout, client), nil
	case "duckduckgo":
		return websearch.NewDuckDuckGoProviderWithClient(timeout, client), nil
	case "searxng":
		// SearXNG has no key to be silently missing: an invalid instance URL
		// is an explicit error. Consumption-time gate for values reaching the
		// builder around the config load (post-${VAR} expansion).
		if err := validateSearxngBaseURL(baseURL); err != nil {
			return nil, err
		}
		return websearch.NewSearXNGProviderWithClient(baseURL, timeout, client), nil
	default:
		// Unrecognized provider name — treat as tavily.
		if apiKey == "" {
			return nil, nil
		}
		return websearch.NewTavilyProviderWithClient(apiKey, timeout, client), nil
	}
}

// validateSearxngBaseURL checks an EXPANDED SearXNG instance URL: it must be
// set and parse as an absolute http(s) URL with a host.
func validateSearxngBaseURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("search.provider searxng requires search.base_url (the SearXNG instance URL, e.g. %s)", websearch.DefaultSearxngBaseURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("search.base_url %q is not a valid URL: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("search.base_url %q must be an absolute http(s) URL, got scheme %q", raw, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("search.base_url %q must be an absolute http(s) URL with a host", raw)
	}
	return nil
}

// UpdateSearchTool replaces or removes the web_search tool in the registry
// based on the given search configuration.
func UpdateSearchTool(registry *ToolRegistry, providerName, apiKey, baseURL string, limits builtins.WebSearchLimits) error {
	return UpdateSearchToolWithClient(registry, providerName, apiKey, baseURL, limits, nil)
}

// UpdateShellTool applies runtime blocklist/launch-shape edits by replacing
// the registered instance (specs/domains/tool-system/builtins.md, Shell
// Invocation Override). Compile or invocation failures are errors that leave
// the previous tool in place.
func UpdateShellTool(registry *ToolRegistry, blocklist []string, timeouts builtins.BashTimeouts, bashInvocation, poshInvocation *sdktools.ShellInvocation) error {
	shellTool, err := newShellExecTool(blocklist, timeouts, bashInvocation, poshInvocation)
	if err != nil {
		return fmt.Errorf("shell tool: %w", err)
	}
	registry.Register(shellTool)
	return nil
}

// UpdateSearchToolWithClient replaces/removes web_search; an invalid provider
// is an error that leaves the previous tool registered.
func UpdateSearchToolWithClient(registry *ToolRegistry, providerName, apiKey, baseURL string, limits builtins.WebSearchLimits, client *http.Client) error {
	provider, err := CreateSearchProviderWithClient(providerName, apiKey, baseURL, limits.Timeout, client)
	if err != nil {
		return fmt.Errorf("web_search provider: %w", err)
	}
	if provider != nil {
		registry.Register(websearch.NewTool(provider, limits))
	} else {
		registry.Unregister("web_search")
	}
	return nil
}

// UpdateWebFetchTool replaces the web_fetch tool in the registry with an optional HTTP client.
func UpdateWebFetchTool(registry *ToolRegistry, limits builtins.WebFetchLimits, client *http.Client) {
	registry.Register(builtins.NewWebFetchToolWithClient(limits, client))
}
