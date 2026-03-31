package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/user/agent/internal/tools"
)

// contextKey is the type for context keys used in this package.
type contextKey string

// sessionIDKey is the context key for session ID.
const sessionIDKey contextKey = "session_id"

var toolContextmgrDescription = `Manage persistent memory and context settings.

Actions:

- memory_store: Store a fact or knowledge entry in semantic memory. Requires "key" and "content". Use for persistent cross-session knowledge.
- memory_search: Search semantic memory by natural language query. Returns the most relevant stored facts.
- episodic_store: Store a session-scoped observation or note in episodic memory. Requires "content" (observation text). Automatically scoped to current session.
- episodic_search: Retrieve recent observations from current session's episodic memory. Optional "limit" (default: 10).
- reflexion_store: Store a cross-session failure reflection. Requires "summary", "hypotheses" (array of strings), and "suggested_action".
- reflexion_search: Search past failure reflections by relevance. Requires "query". Optional "limit" (default: 5).
- switch_compaction: Change the context compaction strategy (sliding_window, summarization, hierarchical).
`

// SemanticStore interface to avoid importing memory package.
type SemanticStore interface {
	Store(ctx context.Context, key string, content string, metadata map[string]string) error
	Search(ctx context.Context, query string, topK int) ([]SemanticSearchResult, error)
}

// SemanticSearchResult represents a search result from semantic memory.
type SemanticSearchResult struct {
	Key     string
	Content string
	Score   float64
}

// CompactionSwitcher interface to avoid importing memory package.
type CompactionSwitcher interface {
	SetStrategy(strategy string) error
}

// EpisodicStore provides session-scoped episodic memory operations.
type EpisodicStore interface {
	StoreEntry(ctx context.Context, entry EpisodicEntry) error
	RetrieveEntries(ctx context.Context, sessionID string, limit int) ([]EpisodicEntry, error)
}

// EpisodicEntry represents a session-scoped episodic memory entry.
type EpisodicEntry struct {
	SessionID   string
	UserMessage string
	Summary     string
	Mode        string
	ToolsUsed   []string
	Success     bool
	Timestamp   time.Time
}

// ReflexionStore provides cross-session reflexion memory operations.
type ReflexionStore interface {
	Store(ctx context.Context, reflection StoredReflexion) error
	Search(ctx context.Context, query string, limit int) ([]StoredReflexion, error)
}

// StoredReflexion represents a cross-session failure reflection.
type StoredReflexion struct {
	TaskDescription string
	Summary         string
	Hypotheses      []string
	SuggestedAction string
	Timestamp       time.Time
}

// ContextManagerTool provides context management operations:
// semantic memory store/search and compaction strategy switching.
type ContextManagerTool struct {
	semanticStore      SemanticStore      // optional, nil-safe
	compactionSwitcher CompactionSwitcher // optional, nil-safe
	episodic           EpisodicStore      // optional, nil-safe
	reflexion          ReflexionStore     // optional, nil-safe
}

// NewContextManagerTool creates a new ContextManagerTool instance.
// All dependencies are optional and nil-safe.
func NewContextManagerTool(semantic SemanticStore, compaction CompactionSwitcher, episodic EpisodicStore, reflexion ReflexionStore) *ContextManagerTool {
	return &ContextManagerTool{
		semanticStore:      semantic,
		compactionSwitcher: compaction,
		episodic:           episodic,
		reflexion:          reflexion,
	}
}

// contextManagerInput represents the input structure for context manager operations.
type contextManagerInput struct {
	Action   string            `json:"action"`
	Key      string            `json:"key"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata"`
	Query    string            `json:"query"`
	TopK     int               `json:"top_k"`
	Strategy string            `json:"strategy"`
	// For reflexion_store
	Summary         string   `json:"summary"`
	Hypotheses      []string `json:"hypotheses"`
	SuggestedAction string   `json:"suggested_action"`
	// For episodic_search / reflexion_search
	Limit int `json:"limit"`
}

// Name returns the tool name.
func (t *ContextManagerTool) Name() string {
	return "context_manager"
}

// Description returns the tool description.
func (t *ContextManagerTool) Description() string {
	return strings.TrimSpace(toolContextmgrDescription)
}

// InputSchema returns the JSON schema for the tool input.
func (t *ContextManagerTool) InputSchema() json.RawMessage {
	schema := `{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["memory_store", "memory_search", "episodic_store", "episodic_search", "reflexion_store", "reflexion_search", "switch_compaction"],
				"description": "The context management action to perform"
			},
			"key": {
				"type": "string",
				"description": "Key for storing content in semantic memory (for memory_store action)"
			},
			"content": {
				"type": "string",
				"description": "Content to store in semantic memory (for memory_store action)"
			},
			"metadata": {
				"type": "object",
				"additionalProperties": {"type": "string"},
				"description": "Optional metadata for the stored content (for memory_store action)"
			},
			"query": {
				"type": "string",
				"description": "Search query for semantic memory (for memory_search action)"
			},
			"top_k": {
				"type": "integer",
				"description": "Number of results to return (for memory_search action, default: 5)"
			},
			"strategy": {
				"type": "string",
				"enum": ["sliding_window", "summarization", "hierarchical"],
				"description": "Compaction strategy to switch to (for switch_compaction action)"
			},
			"summary": {
				"type": "string",
				"description": "Summary of the failure reflection (for reflexion_store action)"
			},
			"hypotheses": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Array of hypotheses about what went wrong (for reflexion_store action)"
			},
			"suggested_action": {
				"type": "string",
				"description": "Suggested action to prevent similar failures (for reflexion_store action)"
			},
			"limit": {
				"type": "integer",
				"description": "Number of results to return (for episodic_search and reflexion_search actions, default: 10 for episodic, 5 for reflexion)"
			}
		},
		"required": ["action"]
	}`
	return json.RawMessage(schema)
}

// DefaultPolicy returns PolicyAlwaysAllow because context manager is a service tool.
func (t *ContextManagerTool) DefaultPolicy() tools.ToolPolicy {
	return tools.PolicyAlwaysAllow
}

// Execute performs the requested context management operation.
func (t *ContextManagerTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params contextManagerInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}

	switch params.Action {
	case "memory_store":
		return t.memoryStore(ctx, params)
	case "memory_search":
		return t.memorySearch(ctx, params)
	case "episodic_store":
		return t.episodicStore(ctx, params)
	case "episodic_search":
		return t.episodicSearch(ctx, params)
	case "reflexion_store":
		return t.reflexionStore(ctx, params)
	case "reflexion_search":
		return t.reflexionSearch(ctx, params)
	case "switch_compaction":
		return t.switchCompaction(params)
	default:
		return tools.ToolResult{Content: fmt.Sprintf("unknown action: %s", params.Action), IsError: true}, nil
	}
}

// memoryStore stores content in semantic memory.
func (t *ContextManagerTool) memoryStore(ctx context.Context, params contextManagerInput) (tools.ToolResult, error) {
	if t.semanticStore == nil {
		return tools.ToolResult{Content: "semantic memory is not available", IsError: true}, nil
	}

	if params.Key == "" {
		return tools.ToolResult{Content: "missing required parameter: key", IsError: true}, nil
	}

	if params.Content == "" {
		return tools.ToolResult{Content: "missing required parameter: content", IsError: true}, nil
	}

	if err := t.semanticStore.Store(ctx, params.Key, params.Content, params.Metadata); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to store in semantic memory: %v", err), IsError: true}, nil
	}

	return tools.ToolResult{Content: fmt.Sprintf("successfully stored content with key: %s", params.Key), IsError: false}, nil
}

// memorySearch searches semantic memory.
func (t *ContextManagerTool) memorySearch(ctx context.Context, params contextManagerInput) (tools.ToolResult, error) {
	if t.semanticStore == nil {
		return tools.ToolResult{Content: "semantic memory is not available", IsError: true}, nil
	}

	if params.Query == "" {
		return tools.ToolResult{Content: "missing required parameter: query", IsError: true}, nil
	}

	topK := params.TopK
	if topK <= 0 {
		topK = 5 // default
	}

	results, err := t.semanticStore.Search(ctx, params.Query, topK)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to search semantic memory: %v", err), IsError: true}, nil
	}

	if len(results) == 0 {
		return tools.ToolResult{Content: "no results found", IsError: false}, nil
	}

	// Format results
	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d results:\n\n", len(results))
	for i, r := range results {
		fmt.Fprintf(&sb, "--- Result %d (score: %.4f) ---\n", i+1, r.Score)
		fmt.Fprintf(&sb, "Key: %s\n", r.Key)
		fmt.Fprintf(&sb, "Content: %s\n\n", r.Content)
	}

	return tools.ToolResult{Content: sb.String(), IsError: false}, nil
}

// switchCompaction switches the compaction strategy.
func (t *ContextManagerTool) switchCompaction(params contextManagerInput) (tools.ToolResult, error) {
	if t.compactionSwitcher == nil {
		return tools.ToolResult{Content: "compaction switcher is not available", IsError: true}, nil
	}

	if params.Strategy == "" {
		return tools.ToolResult{Content: "missing required parameter: strategy", IsError: true}, nil
	}

	// Validate strategy
	validStrategies := map[string]bool{
		"sliding_window": true,
		"summarization":  true,
		"hierarchical":   true,
	}
	if !validStrategies[params.Strategy] {
		return tools.ToolResult{
			Content: fmt.Sprintf("invalid strategy: %s (valid options: sliding_window, summarization, hierarchical)", params.Strategy),
			IsError: true,
		}, nil
	}

	if err := t.compactionSwitcher.SetStrategy(params.Strategy); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to switch compaction strategy: %v", err), IsError: true}, nil
	}

	return tools.ToolResult{Content: fmt.Sprintf("successfully switched compaction strategy to: %s", params.Strategy), IsError: false}, nil
}

// episodicStore stores an entry in episodic memory.
func (t *ContextManagerTool) episodicStore(ctx context.Context, params contextManagerInput) (tools.ToolResult, error) {
	if t.episodic == nil {
		return tools.ToolResult{Content: "episodic memory is not available", IsError: true}, nil
	}

	if params.Content == "" {
		return tools.ToolResult{Content: "missing required parameter: content", IsError: true}, nil
	}

	// Extract session ID from context
	sessionID, _ := ctx.Value(sessionIDKey).(string)
	if sessionID == "" {
		return tools.ToolResult{Content: "session ID not found in context", IsError: true}, nil
	}

	entry := EpisodicEntry{
		SessionID:   sessionID,
		UserMessage: params.Content,
		Summary:     params.Content,
		Timestamp:   time.Now(),
	}

	if err := t.episodic.StoreEntry(ctx, entry); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to store in episodic memory: %v", err), IsError: true}, nil
	}

	return tools.ToolResult{Content: "successfully stored observation in episodic memory", IsError: false}, nil
}

// episodicSearch retrieves entries from episodic memory.
func (t *ContextManagerTool) episodicSearch(ctx context.Context, params contextManagerInput) (tools.ToolResult, error) {
	if t.episodic == nil {
		return tools.ToolResult{Content: "episodic memory is not available", IsError: true}, nil
	}

	// Extract session ID from context
	sessionID, _ := ctx.Value(sessionIDKey).(string)
	if sessionID == "" {
		return tools.ToolResult{Content: "session ID not found in context", IsError: true}, nil
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 10 // default
	}

	entries, err := t.episodic.RetrieveEntries(ctx, sessionID, limit)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to retrieve from episodic memory: %v", err), IsError: true}, nil
	}

	if len(entries) == 0 {
		return tools.ToolResult{Content: "no episodic entries found", IsError: false}, nil
	}

	// Format results as JSON
	resultJSON, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to format results: %v", err), IsError: true}, nil
	}

	return tools.ToolResult{Content: string(resultJSON), IsError: false}, nil
}

// reflexionStore stores a reflection in reflexion memory.
func (t *ContextManagerTool) reflexionStore(ctx context.Context, params contextManagerInput) (tools.ToolResult, error) {
	if t.reflexion == nil {
		return tools.ToolResult{Content: "reflexion memory is not available", IsError: true}, nil
	}

	if params.Summary == "" {
		return tools.ToolResult{Content: "missing required parameter: summary", IsError: true}, nil
	}

	if len(params.Hypotheses) == 0 {
		return tools.ToolResult{Content: "missing required parameter: hypotheses", IsError: true}, nil
	}

	if params.SuggestedAction == "" {
		return tools.ToolResult{Content: "missing required parameter: suggested_action", IsError: true}, nil
	}

	reflection := StoredReflexion{
		Summary:         params.Summary,
		Hypotheses:      params.Hypotheses,
		SuggestedAction: params.SuggestedAction,
		Timestamp:       time.Now(),
	}

	if err := t.reflexion.Store(ctx, reflection); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to store in reflexion memory: %v", err), IsError: true}, nil
	}

	return tools.ToolResult{Content: "successfully stored reflection in reflexion memory", IsError: false}, nil
}

// reflexionSearch searches reflections in reflexion memory.
func (t *ContextManagerTool) reflexionSearch(ctx context.Context, params contextManagerInput) (tools.ToolResult, error) {
	if t.reflexion == nil {
		return tools.ToolResult{Content: "reflexion memory is not available", IsError: true}, nil
	}

	if params.Query == "" {
		return tools.ToolResult{Content: "missing required parameter: query", IsError: true}, nil
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 5 // default
	}

	reflections, err := t.reflexion.Search(ctx, params.Query, limit)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to search reflexion memory: %v", err), IsError: true}, nil
	}

	if len(reflections) == 0 {
		return tools.ToolResult{Content: "no reflections found", IsError: false}, nil
	}

	// Format results as JSON
	resultJSON, err := json.MarshalIndent(reflections, "", "  ")
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to format results: %v", err), IsError: true}, nil
	}

	return tools.ToolResult{Content: string(resultJSON), IsError: false}, nil
}
