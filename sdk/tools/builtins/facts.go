package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/tools"
)

const toolStoreFactDescription = "Store a fact with 3-5 keywords for later retrieval by yourself or other agents. Use this to record important discoveries, decisions, or intermediate results that may be useful in subsequent steps."

const toolSearchFactsDescription = "Search stored facts by keywords. Returns facts matching any of the given keywords, ranked by relevance (most keyword matches first)."

// ---------------------------------------------------------------------------
// store_fact
// ---------------------------------------------------------------------------

// StoreFactTool stores a keyword-tagged fact in the shared fact memory.
type StoreFactTool struct {
	*tools.BaseTool
}

// NewStoreFactTool creates a new StoreFactTool instance.
func NewStoreFactTool() *StoreFactTool {
	return &StoreFactTool{BaseTool: &tools.BaseTool{
		ToolName:        "store_fact",
		ToolDescription: toolStoreFactDescription,
		Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"keywords": {
				"type": "array",
				"items": {"type": "string"},
				"minItems": 3,
				"maxItems": 5,
				"description": "3-5 keywords for retrieval"
			},
			"content": {
				"type": "string",
				"description": "The fact to store"
			}
		},
		"required": ["keywords", "content"]
	}`),
		Policy: tools.PolicyAlwaysAllow,
	}}
}

// StoreFactInput represents the input parameters for store_fact.
type StoreFactInput struct {
	Keywords []string `json:"keywords"`
	Content  string   `json:"content"`
}

// Execute stores a fact via the FactStore from context.
func (t *StoreFactTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params StoreFactInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if len(params.Keywords) < 3 {
		return tools.ToolResult{Content: fmt.Sprintf("validation error: keywords must have at least 3 items, got %d", len(params.Keywords)), IsError: true}, nil
	}
	if len(params.Keywords) > 5 {
		return tools.ToolResult{Content: fmt.Sprintf("validation error: keywords must have at most 5 items, got %d", len(params.Keywords)), IsError: true}, nil
	}
	if strings.TrimSpace(params.Content) == "" {
		return tools.ToolResult{Content: "validation error: content is required", IsError: true}, nil
	}

	fs := agent.FactStoreFromContext(ctx)
	if fs == nil {
		return tools.ErrorResult("Fact store not available"), nil
	}

	author := agent.StepIDFromContext(ctx)
	fs.StoreFact(params.Keywords, params.Content, author)

	return tools.ToolResult{Content: "Fact stored with keywords: " + strings.Join(params.Keywords, ", ")}, nil
}

// ---------------------------------------------------------------------------
// search_facts
// ---------------------------------------------------------------------------

// SearchFactsTool searches stored facts by keywords.
type SearchFactsTool struct {
	*tools.BaseTool
}

// NewSearchFactsTool creates a new SearchFactsTool instance.
func NewSearchFactsTool() *SearchFactsTool {
	return &SearchFactsTool{BaseTool: &tools.BaseTool{
		ToolName:        "search_facts",
		ToolDescription: toolSearchFactsDescription,
		Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"keywords": {
				"type": "array",
				"items": {"type": "string"},
				"minItems": 1,
				"maxItems": 5,
				"description": "Keywords to search for"
			}
		},
		"required": ["keywords"]
	}`),
		Policy: tools.PolicyAlwaysAllow,
	}}
}

// SearchFactsInput represents the input parameters for search_facts.
type SearchFactsInput struct {
	Keywords []string `json:"keywords"`
}

// Execute searches facts via the FactStore from context.
func (t *SearchFactsTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params SearchFactsInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if len(params.Keywords) == 0 {
		return tools.ToolResult{Content: "validation error: keywords must have at least 1 item", IsError: true}, nil
	}

	fs := agent.FactStoreFromContext(ctx)
	if fs == nil {
		return tools.ErrorResult("Fact store not available"), nil
	}

	entries := fs.SearchFacts(params.Keywords)
	if len(entries) == 0 {
		return tools.ToolResult{Content: "No facts found matching the given keywords"}, nil
	}

	var b strings.Builder
	for i, e := range entries {
		fmt.Fprintf(&b, "%d. [%s] (by %s)\n   %s\n", i+1, strings.Join(e.Keywords, ", "), e.Author, e.Content)
	}

	return tools.ToolResult{Content: b.String()}, nil
}
