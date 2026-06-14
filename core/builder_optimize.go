package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/v0lka/c0wrk/core/internal/strutil"
	coreprompts "github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/sdk/llm"
)

// OptimizePromptResult holds the output of the prompt optimization pipeline.
type OptimizePromptResult struct {
	OptimizedPrompt string
	Keywords        []string
	UsedContext     bool
}

// extractResult is the expected JSON structure from the extraction LLM call.
type extractResult struct {
	Translated string   `json:"translated"`
	Keywords   []string `json:"keywords"`
}

// OptimizePrompt runs a 3-step prompt optimization pipeline:
//  1. Translate the prompt to English and extract semantic keywords (LLM).
//  2. Search the vector index for relevant codebase context (optional, skipped when unavailable).
//  3. Rewrite the prompt using the translated text and codebase context (LLM).
func (b *OrchestratorBuilder) OptimizePrompt(ctx context.Context, userPrompt string) (*OptimizePromptResult, error) {
	b.mu.RLock()
	router := b.llmRouter
	searchFunc := b.vectorSearchFunc
	baseEffort := b.baseReasoningEffort
	roleOverrides := b.roleOverrides
	b.mu.RUnlock()

	if router == nil {
		return nil, errors.New("llm router not available")
	}

	summaryEffort := llm.ResolveAgentReasoningMode("summary", baseEffort, roleOverrides)

	// Step A: Translate + extract keywords
	extractTemp := 0.3
	extractReq := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: coreprompts.PromptOptimizeExtract},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens:       500,
		Temperature:     &extractTemp,
		ReasoningEffort: summaryEffort,
	}
	extractResp, err := router.Call(ctx, extractReq)
	if err != nil {
		return nil, fmt.Errorf("optimize prompt: translate/extract: %w", err)
	}

	var extracted extractResult
	translated := userPrompt
	var keywords []string

	if err := json.Unmarshal([]byte(extractResp.Message.Content), &extracted); err != nil {
		b.log().Warn("optimize prompt: failed to parse extraction JSON, using original prompt",
			"error", err, "content", extractResp.Message.Content)
	} else {
		if extracted.Translated != "" {
			translated = extracted.Translated
		}
		keywords = extracted.Keywords
	}

	// Step B: Semantic search (optional — graceful skip)
	var contextBlock string
	usedContext := false

	if searchFunc != nil && len(keywords) > 0 {
		query := strings.Join(keywords, " ")
		results, searchErr := searchFunc(ctx, tools.VectorSearchOptions{Query: query, TopK: 5})
		if searchErr != nil {
			b.log().Warn("optimize prompt: vector search failed, proceeding without context", "error", searchErr)
		} else if len(results) > 0 {
			usedContext = true
			var sb strings.Builder
			for i, r := range results {
				content := r.Content
				if len(content) > 300 {
					content = strutil.TruncateUTF8(content, 300) + "..."
				}
				fmt.Fprintf(&sb, "%d. %s (lines %d-%d, %s)\n%s\n\n", i+1, r.FilePath, r.StartLine, r.EndLine, r.Language, content)
			}
			contextBlock = sb.String()
		}
	}

	// Step C: Optimize prompt
	var userMsg strings.Builder
	userMsg.WriteString("## Original Prompt\n\n")
	userMsg.WriteString(translated)
	if contextBlock != "" {
		userMsg.WriteString("\n\n## Codebase Context\n\n")
		userMsg.WriteString(contextBlock)
	}

	rewriteTemp := 0.5
	rewriteReq := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: coreprompts.PromptOptimizeRewrite},
			{Role: "user", Content: userMsg.String()},
		},
		MaxTokens:       2000,
		Temperature:     &rewriteTemp,
		ReasoningEffort: summaryEffort,
	}
	rewriteResp, err := router.Call(ctx, rewriteReq)
	if err != nil {
		return nil, fmt.Errorf("optimize prompt: rewrite: %w", err)
	}

	return &OptimizePromptResult{
		OptimizedPrompt: rewriteResp.Message.Content,
		Keywords:        keywords,
		UsedContext:     usedContext,
	}, nil
}
