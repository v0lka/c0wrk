package tools

import (
	"time"

	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/tools/builtins"
	websearch "github.com/user/agent/sdk/tools/builtins/web_search"
)

// Limit type re-exports so backend can populate BuiltinToolsConfig
// without importing sdk/tools/builtins.
type (
	FileLimits           = builtins.FileLimits
	RipgrepLimits        = builtins.RipgrepLimits
	GlobLimits           = builtins.GlobLimits
	WebFetchLimits       = builtins.WebFetchLimits
	WebSearchLimits      = builtins.WebSearchLimits
	BashTimeouts         = builtins.BashTimeouts
	VectorSearchFunc     = builtins.VectorSearchFunc
	VectorSearchWaitFunc = builtins.VectorSearchWaitFunc
	VectorSearchResult   = builtins.VectorSearchResult
)

// BuiltinToolsConfig holds configuration for registering built-in tools.
type BuiltinToolsConfig struct {
	FileLimits      FileLimits
	RipgrepLimits   RipgrepLimits
	GlobLimits      GlobLimits
	WebFetchLimits  WebFetchLimits
	WebSearchLimits WebSearchLimits
	BashTimeouts    BashTimeouts
	BashBlacklist   []string

	// Search provider configuration.
	SearchProvider string
	SearchAPIKey   string
	SearchTimeout  time.Duration

	// AskUserFunc is the callback for the ask_user tool.
	// If nil, the ask_user tool is not registered.
	AskUserFunc AskUserFunc

	// VectorSearchFunc is the callback for the semantic_search tool.
	// If nil, the semantic_search tool is not registered.
	VectorSearchFunc VectorSearchFunc

	// VectorSearchWaitFunc blocks until the vector index is ready.
	VectorSearchWaitFunc VectorSearchWaitFunc
}

// RegisterBuiltinTools creates and registers all built-in tools into the registry.
func RegisterBuiltinTools(registry *ToolRegistry, cfg BuiltinToolsConfig) {
	// Bash
	registry.Register(builtins.NewBashExecToolWithTimeouts(cfg.BashBlacklist, cfg.BashTimeouts))

	// File operations
	registry.Register(builtins.NewReadFileToolWithLimits(cfg.FileLimits))
	registry.Register(builtins.NewWriteFileTool())
	registry.Register(builtins.NewEditFileTool())
	registry.Register(builtins.NewListDirectoryTool())
	registry.Register(builtins.NewSearchFilesTool())
	registry.Register(builtins.NewSearchContentToolWithLimits(cfg.FileLimits))
	registry.Register(builtins.NewCreateDirectoryTool())
	registry.Register(builtins.NewDeleteDirectoryTool())
	registry.Register(builtins.NewDeleteFileTool())

	// Finish
	registry.Register(agent.NewFinishTool())

	// Web fetch
	registry.Register(builtins.NewWebFetchToolWithLimits(cfg.WebFetchLimits))

	// Web search (optional)
	if provider := CreateSearchProvider(cfg.SearchProvider, cfg.SearchAPIKey, cfg.SearchTimeout); provider != nil {
		registry.Register(websearch.NewWebSearchToolWithLimits(provider, cfg.WebSearchLimits))
	}

	// Glob and ripgrep
	registry.Register(builtins.NewGlobToolWithLimits(cfg.GlobLimits))
	registry.Register(builtins.NewRipgrepToolWithLimits(cfg.RipgrepLimits))

	// Step output tools
	registry.Register(builtins.NewReadStepOutputTool())
	registry.Register(builtins.NewListStepOutputsTool())

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
}

// CreateSearchProvider creates a search provider based on the configured provider name.
// Returns nil if the provider requires an API key but none is configured.
func CreateSearchProvider(providerName, apiKey string, timeout time.Duration) websearch.SearchProvider {
	switch providerName {
	case "brave":
		if apiKey == "" {
			return nil
		}
		return websearch.NewBraveProviderWithTimeout(apiKey, timeout)
	case "exa":
		if apiKey == "" {
			return nil
		}
		return websearch.NewExaProviderWithTimeout(apiKey, timeout)
	case "duckduckgo":
		return websearch.NewDuckDuckGoProviderWithTimeout(timeout)
	default: // "tavily" or empty
		if apiKey == "" {
			return nil
		}
		return websearch.NewTavilyProviderWithTimeout(apiKey, timeout)
	}
}

// UpdateSearchTool replaces or removes the web_search tool in the registry
// based on the given search configuration.
func UpdateSearchTool(registry *ToolRegistry, providerName, apiKey string, limits WebSearchLimits) {
	if provider := CreateSearchProvider(providerName, apiKey, limits.Timeout); provider != nil {
		registry.Register(websearch.NewWebSearchToolWithLimits(provider, limits))
	} else {
		registry.Unregister("web_search")
	}
}
