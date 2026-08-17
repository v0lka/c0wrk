package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	coreprompts "github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/strutil"
	"github.com/v0lka/sp4rk/tools/builtins"
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

// OptimizePrompt runs a 3-step prompt optimization pipeline with retry:
//  1. Translate the prompt to English and extract semantic keywords (LLM).
//  2. Search the vector index for relevant codebase context (optional, skipped when unavailable).
//  3. Rewrite the prompt using the translated text and codebase context (LLM) — retried up to 2 times
//     on empty or invalid output, with feedback from previous failures.
func (b *OrchestratorBuilder) OptimizePrompt(ctx context.Context, userPrompt string) (*OptimizePromptResult, error) {
	b.mu.RLock()
	router := b.llmRouter
	searchFunc := b.vectorSearchFunc
	reasoningEffort := b.reasoningEffort
	b.mu.RUnlock()

	if router == nil {
		return nil, errors.New("llm router not available")
	}

	// Step A: Translate + extract keywords (single attempt — no retry needed).
	extractTemp := 0.3
	extractReq := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: coreprompts.PromptOptimizeExtract},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens:   500,
		Temperature: &extractTemp, // explicit value wins over any profile
		// Auxiliary text composition call — summarization class.
		CallPurpose:     llm.CallPurposeSummarization,
		ReasoningEffort: reasoningEffort,
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

	// Step B: Semantic search (optional — graceful skip).
	var contextBlock string
	usedContext := false

	if searchFunc != nil && len(keywords) > 0 {
		query := strings.Join(keywords, " ")
		results, searchErr := searchFunc(ctx, builtins.VectorSearchOptions{Query: query, TopK: 5})
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

	// Step C: Build the rewrite prompt (shared across retries).
	var userMsg strings.Builder
	userMsg.WriteString("## Original Prompt\n\n")
	userMsg.WriteString(translated)
	if contextBlock != "" {
		userMsg.WriteString("\n\n## Codebase Context\n\n")
		userMsg.WriteString(contextBlock)
	}

	// Step C (with retries): Rewrite the prompt.
	result, err := b.runOptimizeRewriteLoop(ctx, router, reasoningEffort, userMsg.String())
	if err != nil {
		return nil, err
	}

	return &OptimizePromptResult{
		OptimizedPrompt: result,
		Keywords:        keywords,
		UsedContext:     usedContext,
	}, nil
}

// runOptimizeRewriteLoop runs the rewrite step with up to 2 retries.
// On each retry, the previous failed output is fed back to the model as
// context so it can correct its behavior. LLM call failures (network errors,
// timeouts) are retried independently up to 2 extra times.
func (b *OrchestratorBuilder) runOptimizeRewriteLoop(
	ctx context.Context,
	router *llm.Router,
	reasoningEffort string,
	userPrompt string,
) (string, error) {
	const (
		maxRetries     = 2
		maxCallRetries = 2
	)

	var lastFailedOutput string

	for attempt := 0; attempt <= maxRetries; attempt++ {
		rewriteTemp := 0.5
		rewriteReq := llm.ChatRequest{
			Messages: []llm.Message{
				{Role: "system", Content: coreprompts.PromptOptimizeRewrite},
				{Role: "user", Content: userPrompt},
			},
			MaxTokens:   2000,
			Temperature: &rewriteTemp, // explicit value wins over any profile
			// Auxiliary text composition call — summarization class.
			CallPurpose:     llm.CallPurposeSummarization,
			ReasoningEffort: reasoningEffort,
		}

		// On retry, replace the user message with an augmented version
		// that includes feedback from the previous attempt.
		if attempt > 0 {
			var augmentedUser strings.Builder
			augmentedUser.WriteString(userPrompt)
			augmentedUser.WriteString("\n\n## Previous Attempt (DO NOT repeat this format)\n\n")
			augmentedUser.WriteString("The previous output was empty or not a usable prompt. Here is what was produced:\n\n")
			augmentedUser.WriteString("```\n")
			augmentedUser.WriteString(lastFailedOutput)
			augmentedUser.WriteString("\n```\n\n")
			augmentedUser.WriteString("Your output MUST be a clear, actionable prompt wrapped between the markers:\n")
			augmentedUser.WriteString("### OPTIMIZED_PROMPT_START\n<your prompt>\n### OPTIMIZED_PROMPT_END\n")
			augmentedUser.WriteString("Place NOTHING before the start marker and NOTHING after the end marker.")
			rewriteReq.Messages = []llm.Message{
				{Role: "system", Content: coreprompts.PromptOptimizeRewrite},
				{Role: "user", Content: augmentedUser.String()},
			}
		}

		// Retry LLM call failures independently (up to maxCallRetries extra attempts).
		var rewriteResp *llm.ChatResponse
		callErr := error(nil)
		for callRetry := 0; callRetry <= maxCallRetries; callRetry++ {
			rewriteResp, callErr = router.Call(ctx, rewriteReq)
			if callErr == nil {
				break
			}
			if callRetry < maxCallRetries {
				b.log().Warn("optimize prompt: rewrite LLM call failed, retrying",
					"attempt", attempt+1, "call_retry", callRetry+1, "error", callErr)
			}
		}
		if callErr != nil {
			return "", fmt.Errorf("optimize prompt: rewrite attempt %d (after %d call retries): %w", attempt+1, maxCallRetries, callErr)
		}

		// Extract the optimized prompt from the best available response field.
		optimized := extractOptimizedPrompt(rewriteResp)
		if optimized == "" {
			// The LLM call succeeded but produced no usable text.
			// Collect what was actually in the response for diagnostic feedback.
			hasReasoning := rewriteResp.Message.ReasoningContent != "" || rewriteResp.Reasoning != ""
			b.log().Warn("optimize prompt: rewrite produced no usable output",
				"attempt", attempt+1,
				"has_reasoning", hasReasoning,
				"stop_reason", rewriteResp.StopReason,
				"content_len", len(rewriteResp.Message.Content),
				"reasoning_content_len", len(rewriteResp.Message.ReasoningContent),
			)

			if hasReasoning {
				// The model likely consumed the output budget with reasoning.
				// Include the reasoning content as feedback for the next retry.
				lastFailedOutput = rewriteResp.Message.ReasoningContent
				if lastFailedOutput == "" {
					lastFailedOutput = rewriteResp.Reasoning
				}
				continue
			}

			// No reasoning content either — empty response.
			// Provide a diagnostic message so the next retry has something to work with.
			if rewriteResp.Message.Content == "" {
				lastFailedOutput = "The output was empty — no content or reasoning was produced."
			} else {
				lastFailedOutput = rewriteResp.Message.Content
			}
			continue
		}

		// Success — valid optimized prompt extracted.
		return optimized, nil
	}

	// All attempts exhausted.
	b.log().Warn("optimize prompt: rewrite failed after all retries",
		"last_output", lastFailedOutput)
	return "", errors.New("the model produced no optimized prompt after multiple attempts; " +
		"ensure the model follows the OPTIMIZED_PROMPT_START / OPTIMIZED_PROMPT_END markers, " +
		"try a non-reasoning model, or use a shorter original prompt")
}
