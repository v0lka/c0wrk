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
// oneshot refusal (which wraps the last parse error) can be recognized and
// re-surfaced with the operator-facing advice. Transport errors never carry
// this sentinel.
var errOptimizeUnparseable = errors.New("optimized prompt unparseable")

// optimizeRewriteRetryHint restates the marker contract inside the oneshot
// "[System]" nudge (verbatim from the previous manual retry feedback; the
// failed output itself rides the assistant echo).
const optimizeRewriteRetryHint = "Your output MUST be a clear, actionable prompt wrapped between the markers:\n" +
	"### OPTIMIZED_PROMPT_START\n<your prompt>\n### OPTIMIZED_PROMPT_END\n" +
	"Place NOTHING before the start marker and NOTHING after the end marker."

// OptimizePrompt runs a 3-step prompt optimization pipeline:
//  1. Translate the prompt to English and extract semantic keywords (LLM) —
//     a oneshot call with the JSON parse; an unparseable extraction falls
//     back to the original prompt after the standard nudge loop.
//  2. Search the vector index for relevant codebase context (optional, skipped when unavailable).
//  3. Rewrite the prompt using the translated text and codebase context (LLM) —
//     a oneshot call with the marker parse and legacy heuristic fallback.
//
// Both calls run under the oneshot service policy (temperature 0.3 / 0.5,
// reasoning tier minimal resolved per active model); the retry loop is the
// client's — the manual c0wrk retry loops are gone, and transport failures
// are never retried here (the Router owns provider retry).
func (b *OrchestratorBuilder) OptimizePrompt(ctx context.Context, userPrompt string) (*OptimizePromptResult, error) {
	b.mu.RLock()
	router := b.llmRouter
	searchFunc := b.vectorSearchFunc
	b.mu.RUnlock()

	if router == nil {
		return nil, errors.New("llm router not available")
	}
	model := bareActiveModel(router)

	// Step A: Translate + extract keywords. An unparseable extraction falls
	// back to the zero result — translated=="" keeps the original prompt and
	// no keywords skips the context step (the historical fallback behavior).
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

// optimizeExtract issues the translate/extract one-shot: oneshot.ParseJSON
// over all candidate response fields, OnFailureFallback to the zero
// extractResult after the standard two-nudge loop, transport errors returned
// as-is. It is a separate method so that tests can inject a mock caller.
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
	return oneshot.Do(ctx, caller, req, oneshot.ParseJSON[extractResult], oneshot.Options[extractResult]{
		Kind:          "optimize_extract",
		Logger:        b.log(),
		OnFailure:     oneshot.OnFailureFallback,
		FallbackValue: extractResult{},
	})
}

// optimizeRewrite issues the rewrite one-shot with the marker parse and the
// legacy heuristic fallback. Parse failures ride the client's two-nudge loop;
// the final refusal is re-surfaced with the operator-facing advice, while
// transport errors pass through wrapped as "optimize prompt: rewrite".
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
	result, err := oneshot.Do(ctx, caller, req, b.optimizeRewriteParse(), oneshot.Options[string]{
		Kind:      "optimize_rewrite",
		Logger:    b.log(),
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

// optimizeRewriteParse extracts the optimized prompt: markers first
// (extractOptimizedPrompt — any candidate field, first hit wins), then the
// legacy heuristic fallback. A response with neither markers nor usable text
// is a retryable parse failure; the Warn diagnostic mirrors the previous
// manual loop's empty-output log (no raw model output in logs).
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
