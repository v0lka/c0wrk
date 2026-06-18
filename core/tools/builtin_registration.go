package tools

import (
	"fmt"
	"net/http"
	"time"

	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/tools/builtins"
	websearch "github.com/v0lka/c0wrk/sdk/tools/builtins/web_search"
	sdktools "github.com/v0lka/c0wrk/sdk/tools"
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
	BashBlacklist       []string
	ExtraBashBlacklist  []string

	// Search provider configuration.
	SearchProvider string
	SearchAPIKey   string
	SearchTimeout  time.Duration

	// HTTPClient is an optional proxy-configured client for web tools.
	// If nil, tools create their own default clients.
	HTTPClient *http.Client

	// AskUserFunc is the callback for the ask_user tool.
	// If nil, the ask_user tool is not registered.
	AskUserFunc sdktools.AskUserFunc

	// VectorSearchFunc is the callback for the semantic_search tool.
	// If nil, the semantic_search tool is not registered.
	VectorSearchFunc builtins.VectorSearchFunc

	// VectorSearchWaitFunc blocks until the vector index is ready.
	VectorSearchWaitFunc builtins.VectorSearchWaitFunc
}

// RegisterBuiltinTools creates and registers all built-in tools into the registry.
func RegisterBuiltinTools(registry *ToolRegistry, cfg BuiltinToolsConfig) error {
	// Bash (merge configured blacklist with any extra patterns)
	allBlacklist := make([]string, 0, len(cfg.BashBlacklist)+len(cfg.ExtraBashBlacklist))
	allBlacklist = append(allBlacklist, cfg.BashBlacklist...)
	allBlacklist = append(allBlacklist, cfg.ExtraBashBlacklist...)
	bashTool, err := builtins.NewBashExecToolWithTimeouts(allBlacklist, cfg.BashTimeouts)
	if err != nil {
		return fmt.Errorf("bash tool: %w", err)
	}
	registry.Register(bashTool)

	// File operations
	registry.Register(builtins.NewReadFileToolWithLimits(cfg.FileLimits))
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
		registry.Register(websearch.NewWebSearchTool(provider, cfg.WebSearchLimits))
	}

	// Glob and ripgrep
	registry.Register(builtins.NewGlobTool())
	registry.Register(builtins.NewRipgrepToolWithLimits(cfg.RipgrepLimits))

	// Tool result cache reader
	registry.Register(builtins.NewToolResultReadTool())

	// Step output tools
	registry.Register(builtins.NewReadStepOutputTool())
	registry.Register(builtins.NewListStepOutputsTool())

	// Step status / to-do checklist tool
	registry.Register(builtins.NewSetStepStatusTool())

	// Fact memory tools
	registry.Register(builtins.NewStoreFactTool())
	registry.Register(builtins.NewSearchFactsTool())

	// Semantic search (optional — requires vector index backend)
	if cfg.VectorSearchFunc != nil {
		registry.Register(builtins.NewVectorSearchTool(cfg.VectorSearchFunc, cfg.VectorSearchWaitFunc))
	}

	// Ask user (optional)
	if cfg.AskUserFunc != nil {
		registry.Register(builtins.NewAskUserTool(cfg.AskUserFunc))
	}

	return nil
}

// CreateSearchProvider creates a search provider based on the configured provider name.
// Returns nil if the provider requires an API key but none is configured.
func CreateSearchProvider(providerName, apiKey string, timeout time.Duration) websearch.SearchProvider {
	return CreateSearchProviderWithClient(providerName, apiKey, timeout, nil)
}

// CreateSearchProviderWithClient creates a search provider with an optional HTTP client.
// Returns nil if the provider requires an API key but none is configured.
func CreateSearchProviderWithClient(providerName, apiKey string, timeout time.Duration, client *http.Client) websearch.SearchProvider {
	switch providerName {
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
	default: // "tavily" or empty
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

// UpdateSearchToolWithClient replaces or removes the web_search tool in the registry
// with an optional HTTP client for proxy support.
func UpdateSearchToolWithClient(registry *ToolRegistry, providerName, apiKey string, limits builtins.WebSearchLimits, client *http.Client) {
	if provider := CreateSearchProviderWithClient(providerName, apiKey, limits.Timeout, client); provider != nil {
		registry.Register(websearch.NewWebSearchTool(provider, limits))
	} else {
		registry.Unregister("web_search")
	}
}

// UpdateWebFetchTool replaces the web_fetch tool in the registry with an optional HTTP client.
func UpdateWebFetchTool(registry *ToolRegistry, limits builtins.WebFetchLimits, client *http.Client) {
	registry.Register(builtins.NewWebFetchToolWithClient(limits, client))
}
