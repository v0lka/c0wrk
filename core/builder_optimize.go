package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	coreprompts "github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/oneshot"
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

// errOptimizeUnparseable marks optimize-rewrite parse failures so the final
// oneshot refusal can be recognized and re-surfaced with operator-facing
// advice; transport errors never carry it.
var errOptimizeUnparseable = errors.New("optimized prompt unparseable")

// optimizeRewriteRetryHint restates the marker contract inside the oneshot
// "[System]" nudge; the failed output itself rides the assistant echo.
const optimizeRewriteRetryHint = "Your output MUST be a clear, actionable prompt wrapped between the markers:\n" +
	"### OPTIMIZED_PROMPT_START\n<your prompt>\n### OPTIMIZED_PROMPT_END\n" +
	"Place NOTHING before the start marker and NOTHING after the end marker."

// OptimizePrompt runs the 3-step prompt optimization pipeline (translate/
// extract keywords, optional vector-context lookup, rewrite) as two oneshot
// service calls — specs/domains/llm-providers.md (one-shot service calls).
func (b *OrchestratorBuilder) OptimizePrompt(ctx context.Context, userPrompt string) (*OptimizePromptResult, error) {
	b.mu.RLock()
	router := b.llmRouter
	searchFunc := b.vectorSearchFunc
	b.mu.RUnlock()

	if router == nil {
		return nil, errors.New("llm router not available")
	}
	model := bareActiveModel(router)

	// Step A: Translate + extract keywords. The zero result keeps the
	// original prompt and skips the context step.
	extracted, err := b.optimizeExtract(ctx, router, model, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("optimize prompt: translate/extract: %w", err)
	}
	translated := userPrompt
	if extracted.Translated != "" {
		translated = extracted.Translated
	}
	keywords := extracted.Keywords

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

	// Step C: Build the rewrite prompt.
	var userMsg strings.Builder
	userMsg.WriteString("## Original Prompt\n\n")
	userMsg.WriteString(translated)
	if contextBlock != "" {
		userMsg.WriteString("\n\n## Codebase Context\n\n")
		userMsg.WriteString(contextBlock)
	}

	// Step C: Rewrite the prompt (client-owned nudge retry policy).
	result, err := b.optimizeRewrite(ctx, router, model, userMsg.String())
	if err != nil {
		return nil, err
	}

	return &OptimizePromptResult{
		OptimizedPrompt: result,
		Keywords:        keywords,
		UsedContext:     usedContext,
	}, nil
}

// optimizeExtract is a separate method so tests can inject a mock caller.
func (b *OrchestratorBuilder) optimizeExtract(ctx context.Context, caller oneshot.Caller, model, userPrompt string) (extractResult, error) {
	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: coreprompts.PromptOptimizeExtract},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens:       500,
		Temperature:     &oneshotTempExtract, // oneshot service policy — explicit value wins over any profile
		ReasoningEffort: serviceReasoningEffort(model, oneshotTierOptimize),
		// Auxiliary text composition call — summarization class.
		CallPurpose: llm.CallPurposeSummarization,
	}
	return serviceCall(ctx, b.serviceMetrics, b.log(), ServiceKindOptimizeExtract, model, caller, req, oneshot.ParseJSON[extractResult], oneshot.Options[extractResult]{
		OnFailure:     oneshot.OnFailureFallback,
		FallbackValue: extractResult{},
	})
}

// optimizeRewrite issues the rewrite one-shot; the final parse refusal is
// re-surfaced with operator-facing advice, transport errors wrapped as
// "optimize prompt: rewrite".
func (b *OrchestratorBuilder) optimizeRewrite(ctx context.Context, caller oneshot.Caller, model, userPrompt string) (string, error) {
	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: coreprompts.PromptOptimizeRewrite},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens:       2000,
		Temperature:     &oneshotTempRewrite, // oneshot service policy — explicit value wins over any profile
		ReasoningEffort: serviceReasoningEffort(model, oneshotTierOptimize),
		// Auxiliary text composition call — summarization class.
		CallPurpose: llm.CallPurposeSummarization,
	}
	result, err := serviceCall(ctx, b.serviceMetrics, b.log(), ServiceKindOptimizeRewrite, model, caller, req, b.optimizeRewriteParse(), oneshot.Options[string]{
		RetryHint: optimizeRewriteRetryHint,
	})
	if err != nil {
		if errors.Is(err, errOptimizeUnparseable) {
			return "", fmt.Errorf("the model produced no optimized prompt after multiple attempts; "+
				"ensure the model follows the OPTIMIZED_PROMPT_START / OPTIMIZED_PROMPT_END markers, "+
				"try a non-reasoning model, or use a shorter original prompt: %w", err)
		}
		return "", fmt.Errorf("optimize prompt: rewrite: %w", err)
	}
	return result, nil
}

// optimizeRewriteParse extracts the optimized prompt: markers first, then the
// legacy heuristic fallback. Neither markers nor usable text is a retryable
// parse failure; the Warn diagnostic carries no raw model output.
func (b *OrchestratorBuilder) optimizeRewriteParse() oneshot.Parse[string] {
	return func(resp *llm.ChatResponse) (string, error) {
		if optimized := extractOptimizedPrompt(resp); optimized != "" {
			return optimized, nil
		}
		hasReasoning := resp != nil && (resp.Message.ReasoningContent != "" || resp.Reasoning != "")
		contentLen := 0
		if resp != nil {
			contentLen = len(resp.Message.Content)
		}
		b.log().Warn("optimize prompt: rewrite produced no usable output",
			"has_reasoning", hasReasoning,
			"content_len", contentLen,
		)
		return "", fmt.Errorf("%w: no OPTIMIZED_PROMPT_START / OPTIMIZED_PROMPT_END markers "+
			"and no usable prompt text in any response field", errOptimizeUnparseable)
	}
}
