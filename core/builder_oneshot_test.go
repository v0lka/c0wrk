package core

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/oneshot"
)

// --- oneshot service policy: reasoning resolution ---

// TestServiceReasoningEffort pins the oneshot service policy's reasoning
// resolution: the tier is mapped to the ACTIVE model's native wire spelling
// via the model catalog (including architecture aliases), and unknown
// models/families fail closed to "" (no reasoning field sent).
func TestServiceReasoningEffort(t *testing.T) {
	tests := []struct {
		name  string
		model string
		tier  llm.ReasoningTier
		want  string
	}{
		{"qwen3.8 off", "qwen3.8-max", llm.ReasoningTierOff, "Off"},
		{"qwen3.8 minimal", "qwen3.8-max", llm.ReasoningTierMinimal, "low"},
		{"bonsai alias off (catalog-first family)", "Bonsai 2 27B", llm.ReasoningTierOff, "Off"},
		{"deepseek off (title/commit 1-shot tier)", "deepseek-v4-pro", llm.ReasoningTierOff, "Off"},
		{"deepseek minimal", "deepseek-v4-pro", llm.ReasoningTierMinimal, "High"},
		{"openai off degrades to minimal", "gpt-5", llm.ReasoningTierOff, "minimal"},
		{"openai minimal", "gpt-5", llm.ReasoningTierMinimal, "minimal"},
		{"unknown model fails closed", "totally-unknown-model", llm.ReasoningTierOff, ""},
		{"empty model fails closed", "", llm.ReasoningTierOff, ""},
		{"empty tier fails closed", "qwen3.8-max", llm.ReasoningTier(""), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := serviceReasoningEffort(tt.model, tt.tier); got != tt.want {
				t.Errorf("serviceReasoningEffort(%q, %q) = %q, want %q", tt.model, tt.tier, got, tt.want)
			}
		})
	}
}

// --- title ---

// TestGenerateTitleWithCaller_RequestShape pins the title one-shot request
// shape: oneshot service policy sampling (temperature 0.3), the resolved
// reasoning spelling, the summarization purpose, and the [system, user] pair
// with the activated-skills suffix.
func TestGenerateTitleWithCaller_RequestShape(t *testing.T) {
	mock := &mockLLMCaller{responses: []*llm.ChatResponse{
		{Message: llm.Message{Content: "Refactor auth middleware"}},
	}}

	title, err := generateTitleWithCaller(context.Background(), mock, "qwen3.8-max", slog.Default(),
		"please refactor my auth middleware", []string{"review", "study-paper"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if title != "Refactor auth middleware" {
		t.Errorf("title = %q, want the model content passed through", title)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("call count = %d, want 1 (titles never nudge)", len(mock.calls))
	}

	req := mock.calls[0]
	if req.Temperature == nil || *req.Temperature != oneshotTempTitle {
		t.Errorf("Temperature = %v, want the oneshot service policy pin %v", req.Temperature, oneshotTempTitle)
	}
	if req.ReasoningEffort != "Off" {
		t.Errorf("ReasoningEffort = %q, want the tier-off spelling %q for the active model", req.ReasoningEffort, "Off")
	}
	if req.MaxTokens != 30 {
		t.Errorf("MaxTokens = %d, want 30", req.MaxTokens)
	}
	if req.CallPurpose != llm.CallPurposeSummarization {
		t.Errorf("CallPurpose = %q, want %q", req.CallPurpose, llm.CallPurposeSummarization)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(req.Messages))
	}
	if req.Messages[0].Role != "system" || !strings.Contains(req.Messages[0].Content, "review, study-paper") {
		t.Errorf("system message must carry the activated skills, got %+v", req.Messages[0])
	}
	if req.Messages[1].Role != "user" || req.Messages[1].Content != "please refactor my auth middleware" {
		t.Errorf("user message = %+v, want the raw user message", req.Messages[1])
	}
}

// TestGenerateTitleWithCaller_EmptyContentNoRetry pins the fallback contract:
// an empty title is a successful result (the backend TitleGenerator replaces
// it with its fallback text), so no nudge loop engages.
func TestGenerateTitleWithCaller_EmptyContentNoRetry(t *testing.T) {
	mock := &mockLLMCaller{responses: []*llm.ChatResponse{
		{Message: llm.Message{Content: ""}},
	}}

	title, err := generateTitleWithCaller(context.Background(), mock, "qwen3.8-max", slog.Default(), "hello", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if title != "" {
		t.Errorf("title = %q, want empty", title)
	}
	if len(mock.calls) != 1 {
		t.Errorf("call count = %d, want 1", len(mock.calls))
	}
}

// --- prompt optimizer ---

// TestOptimizeExtract_RequestShapeAndFallback pins the extract step: the
// oneshot service policy request shape, JSON parse over the response, and the
// OnFailureFallback contract — an unparseable extraction returns the zero
// result (original prompt preserved upstream) after the full nudge loop.
func TestOptimizeExtract_RequestShapeAndFallback(t *testing.T) {
	t.Run("valid JSON is parsed", func(t *testing.T) {
		mock := &mockLLMCaller{responses: []*llm.ChatResponse{
			{Message: llm.Message{Content: `{"translated": "fix the login bug", "keywords": ["auth", "login"]}`}},
		}}
		b := &OrchestratorBuilder{logger: slog.Default(), mu: sync.RWMutex{}}

		got, err := b.optimizeExtract(context.Background(), mock, "qwen3.8-max", "почини баг логина")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Translated != "fix the login bug" || len(got.Keywords) != 2 {
			t.Errorf("extract result = %+v, want translated text and two keywords", got)
		}
		if len(mock.calls) != 1 {
			t.Errorf("call count = %d, want 1", len(mock.calls))
		}
		req := mock.calls[0]
		if req.Temperature == nil || *req.Temperature != oneshotTempExtract {
			t.Errorf("Temperature = %v, want the oneshot service policy pin %v", req.Temperature, oneshotTempExtract)
		}
		if req.ReasoningEffort != "low" {
			t.Errorf("ReasoningEffort = %q, want the tier-minimal spelling %q", req.ReasoningEffort, "low")
		}
		if req.MaxTokens != 500 {
			t.Errorf("MaxTokens = %d, want 500", req.MaxTokens)
		}
		if req.CallPurpose != llm.CallPurposeSummarization {
			t.Errorf("CallPurpose = %q, want %q", req.CallPurpose, llm.CallPurposeSummarization)
		}
	})

	t.Run("garbage falls back to the zero result after the nudge loop", func(t *testing.T) {
		mock := &mockLLMCaller{responses: []*llm.ChatResponse{
			{Message: llm.Message{Content: "not json at all"}},
		}}
		b := &OrchestratorBuilder{logger: slog.Default(), mu: sync.RWMutex{}}

		got, err := b.optimizeExtract(context.Background(), mock, "qwen3.8-max", "почини баг логина")
		if err != nil {
			t.Fatalf("unexpected error: %v (fallback must swallow the parse refusal)", err)
		}
		if got.Translated != "" || len(got.Keywords) != 0 {
			t.Errorf("extract result = %+v, want the zero value fallback", got)
		}
		if len(mock.calls) != 3 {
			t.Errorf("call count = %d, want 3 (two nudges before the fallback)", len(mock.calls))
		}
	})
}

// TestOptimizeRewrite_ParseAndRefusal pins the rewrite step: marker
// extraction wins, the heuristic fallback still recovers unfenced text, and
// the final refusal keeps the operator-facing advice.
func TestOptimizeRewrite_ParseAndRefusal(t *testing.T) {
	t.Run("markers are extracted", func(t *testing.T) {
		mock := &mockLLMCaller{responses: []*llm.ChatResponse{
			{Message: llm.Message{Content: "### OPTIMIZED_PROMPT_START\nFix the login bug\n### OPTIMIZED_PROMPT_END"}},
		}}
		b := &OrchestratorBuilder{logger: slog.Default(), mu: sync.RWMutex{}}

		got, err := b.optimizeRewrite(context.Background(), mock, "qwen3.8-max", "## Original Prompt\n\nfix login")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "Fix the login bug" {
			t.Errorf("rewrite = %q, want the marker content", got)
		}
		req := mock.calls[0]
		if req.Temperature == nil || *req.Temperature != oneshotTempRewrite {
			t.Errorf("Temperature = %v, want the oneshot service policy pin %v", req.Temperature, oneshotTempRewrite)
		}
		if req.ReasoningEffort != "low" {
			t.Errorf("ReasoningEffort = %q, want the tier-minimal spelling %q", req.ReasoningEffort, "low")
		}
		if req.MaxTokens != 2000 {
			t.Errorf("MaxTokens = %d, want 2000", req.MaxTokens)
		}
	})

	t.Run("heuristic fallback recovers unmarked text", func(t *testing.T) {
		mock := &mockLLMCaller{responses: []*llm.ChatResponse{
			{Message: llm.Message{Content: "Sure, fix the auth module"}},
		}}
		b := &OrchestratorBuilder{logger: slog.Default(), mu: sync.RWMutex{}}

		got, err := b.optimizeRewrite(context.Background(), mock, "qwen3.8-max", "## Original Prompt\n\nfix auth")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "fix the auth module" {
			t.Errorf("rewrite = %q, want the preamble-stripped heuristic text", got)
		}
	})

	t.Run("final refusal keeps the operator-facing advice", func(t *testing.T) {
		mock := &mockLLMCaller{responses: []*llm.ChatResponse{
			{Message: llm.Message{Content: ""}},
		}}
		b := &OrchestratorBuilder{logger: slog.Default(), mu: sync.RWMutex{}}

		_, err := b.optimizeRewrite(context.Background(), mock, "qwen3.8-max", "## Original Prompt\n\nfix auth")
		if err == nil {
			t.Fatal("expected the final refusal error, got nil")
		}
		if !strings.Contains(err.Error(), "no optimized prompt after multiple attempts") ||
			!strings.Contains(err.Error(), "OPTIMIZED_PROMPT_START / OPTIMIZED_PROMPT_END") {
			t.Errorf("error = %q, want the operator-facing advice", err.Error())
		}
		if !errors.Is(err, errOptimizeUnparseable) {
			t.Errorf("error = %v, want it to wrap errOptimizeUnparseable", err)
		}
		if len(mock.calls) != 3 {
			t.Errorf("call count = %d, want 3 (two nudges before the refusal)", len(mock.calls))
		}
		if nudge := mock.calls[1].Messages[3]; nudge.Role != "user" ||
			!strings.HasPrefix(nudge.Content, "[System]") ||
			!strings.Contains(nudge.Content, "OPTIMIZED_PROMPT_START") {
			t.Errorf("nudge = (%q, %q), want the [System] marker-contract restatement", nudge.Role, nudge.Content)
		}
	})
}

// TestOneshotServicePinsAreDistinct guards the policy table against a copy
// accident: the rewrite temperature is deliberately warmer than the extract
// one, and the tiers are declared per kind.
func TestOneshotServicePinsAreDistinct(t *testing.T) {
	if oneshotTempTitle != 0.3 || oneshotTempCommit != 0.3 || oneshotTempExtract != 0.3 || oneshotTempRewrite != 0.5 {
		t.Errorf("service temperatures drifted: title=%v commit=%v extract=%v rewrite=%v",
			oneshotTempTitle, oneshotTempCommit, oneshotTempExtract, oneshotTempRewrite)
	}
	if oneshotTierTitle != llm.ReasoningTierOff || oneshotTierCommit != llm.ReasoningTierOff {
		t.Errorf("title/commit tiers must stay off: %q / %q", oneshotTierTitle, oneshotTierCommit)
	}
	if oneshotTierOptimize != llm.ReasoningTierMinimal || oneshotTierCompaction != llm.ReasoningTierMinimal {
		t.Errorf("optimize/compaction tiers must stay minimal: %q / %q", oneshotTierOptimize, oneshotTierCompaction)
	}
}

// compile-time guard: the mock caller satisfies the oneshot Caller surface.
var _ oneshot.Caller = (*mockLLMCaller)(nil)
