package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	coreprompts "github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/sp4rk/llm"
)

// compactionSpyEmitter records the emissions the manual-compaction path must
// produce (ContextCompaction + refreshed ContextFill).
type compactionSpyEmitter struct {
	mockEmitter
	compactions []compactionRecord
	fills       []fillRecord
}

type compactionRecord struct {
	before, after float64
	stepID        string
}

type fillRecord struct {
	percent    float64
	usedTokens int
	maxTokens  int
	status     string
}

func (s *compactionSpyEmitter) ContextCompaction(before, after float64, stepID string) {
	s.compactions = append(s.compactions, compactionRecord{before, after, stepID})
}

func (s *compactionSpyEmitter) ContextFill(percent float64, used, maxTokens int, status, stepID string) {
	s.fills = append(s.fills, fillRecord{percent, used, maxTokens, status})
}

// newCompactionTestOrchestrator builds a minimal orchestrator wired for
// CompactConversationHistory tests: a spy emitter, the simple token counter,
// a model registry with a 1000-token test model, and configurable compaction
// settings + LLM caller.
func newCompactionTestOrchestrator(llmCaller interface {
	Call(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
}) (*Orchestrator, *compactionSpyEmitter) {
	spy := &compactionSpyEmitter{}
	cfg := OrchestratorConfig{
		Model: "test-model",
	}
	cfg.Compaction.SlidingWindow.KeepFirst = 2
	cfg.Compaction.SlidingWindow.KeepLast = 4
	cfg.Compaction.Summarization.BlockSize = 10
	cfg.Compaction.Summarization.KeepLast = 4
	o := &Orchestrator{
		emitter:      spy,
		tokenCounter: llm.NewSimpleTokenCounter(),
		modelRegistry: llm.NewModelRegistry(map[string]llm.ModelMetadata{
			"test-model": {ContextWindow: 1000, Family: "test"},
		}),
		config: cfg,
	}
	if llmCaller != nil {
		o.llm = llmCaller
	}
	return o, spy
}

func compactionHistory(n int) []llm.Message {
	msgs := make([]llm.Message, 0, n*2)
	for i := 0; i < n; i++ {
		msgs = append(msgs,
			llm.Message{Role: "user", Content: strings.Repeat("user message ", 8) + " #" + string(rune('0'+i%10))},
			llm.Message{Role: "assistant", Content: strings.Repeat("assistant reply ", 8) + " #" + string(rune('0'+i%10))},
		)
	}
	return msgs
}

func TestCompactConversationHistory_EmptyHistory(t *testing.T) {
	o, spy := newCompactionTestOrchestrator(nil)
	if _, _, err := o.CompactConversationHistory(context.Background(), "sliding_window"); !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("expected ErrNothingToCompact, got %v", err)
	}
	if len(spy.compactions) != 0 || len(spy.fills) != 0 {
		t.Fatal("no events may be emitted for an empty history")
	}
}

func TestCompactConversationHistory_MissingStrategy(t *testing.T) {
	o, _ := newCompactionTestOrchestrator(nil)
	o.SetConversationHistory(compactionHistory(3))
	if _, _, err := o.CompactConversationHistory(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty strategy")
	}
}

func TestCompactConversationHistory_UnknownStrategyKeepsHistory(t *testing.T) {
	o, spy := newCompactionTestOrchestrator(nil)
	hist := compactionHistory(10)
	o.SetConversationHistory(hist)
	if _, _, err := o.CompactConversationHistory(context.Background(), "bogus"); err == nil {
		t.Fatal("expected error for unknown strategy")
	}
	if got := o.ConversationHistory(); len(got) != len(hist) {
		t.Fatalf("history must be untouched on failure, got %d of %d messages", len(got), len(hist))
	}
	if len(spy.compactions) != 0 || len(spy.fills) != 0 {
		t.Fatal("no events may be emitted on failure")
	}
}

func TestCompactConversationHistory_SlidingWindowReplacesHistoryAndEmits(t *testing.T) {
	o, spy := newCompactionTestOrchestrator(nil)
	hist := compactionHistory(30) // 60 messages
	o.SetConversationHistory(hist)

	before, after, err := o.CompactConversationHistory(context.Background(), "sliding_window")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// History replaced: 2 head + 1 note + 4 tail = 7 messages.
	got := o.ConversationHistory()
	if len(got) != 7 {
		t.Fatalf("expected 7 compacted messages, got %d", len(got))
	}
	if got[len(got)-1].Content != hist[len(hist)-1].Content {
		t.Error("last message must be preserved")
	}

	// Fill percentages against the 1000-token display window.
	if before <= after {
		t.Errorf("expected before (%.1f) > after (%.1f)", before, after)
	}
	if before <= 0 || after < 0 {
		t.Errorf("unexpected percentages: before=%.1f after=%.1f", before, after)
	}

	// Events: compaction card + refreshed fill.
	if len(spy.compactions) != 1 {
		t.Fatalf("expected exactly 1 ContextCompaction emission, got %d", len(spy.compactions))
	}
	if spy.compactions[0].before != before || spy.compactions[0].after != after {
		t.Errorf("ContextCompaction percentages mismatch: %+v (want %.1f→%.1f)", spy.compactions[0], before, after)
	}
	if len(spy.fills) != 1 {
		t.Fatalf("expected exactly 1 ContextFill emission, got %d", len(spy.fills))
	}
	if spy.fills[0].status != "ok" || spy.fills[0].maxTokens != 1000 {
		t.Errorf("unexpected ContextFill payload: %+v", spy.fills[0])
	}
}

func TestCompactConversationHistory_SummarizationFailureKeepsHistory(t *testing.T) {
	caller := &mockLLMCaller{err: errors.New("llm unavailable")}
	o, spy := newCompactionTestOrchestrator(caller)
	hist := compactionHistory(30)
	o.SetConversationHistory(hist)

	if _, _, err := o.CompactConversationHistory(context.Background(), "summarization"); err == nil {
		t.Fatal("expected summarization failure to propagate")
	}
	if got := o.ConversationHistory(); len(got) != len(hist) {
		t.Fatalf("history must be untouched on summarization failure, got %d of %d", len(got), len(hist))
	}
	if len(spy.compactions) != 0 || len(spy.fills) != 0 {
		t.Fatal("no events may be emitted on failure")
	}
}

func TestCompactConversationHistory_SummarizationUsesCompactionCallPurpose(t *testing.T) {
	caller := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[0].Content != compactionSummarizePromptForTest() {
				t.Errorf("unexpected summarize request shape: %+v", req.Messages)
			}
			if req.CallPurpose != llm.CallPurposeCompaction {
				t.Errorf("expected CallPurposeCompaction, got %q", req.CallPurpose)
			}
			return &llm.ChatResponse{Message: llm.Message{Role: "assistant", Content: "SUMMARY"}}, nil
		},
	}
	o, _ := newCompactionTestOrchestrator(caller)
	hist := compactionHistory(30)
	o.SetConversationHistory(hist)

	before, after, err := o.CompactConversationHistory(context.Background(), "summarization")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if before <= after {
		t.Errorf("expected fill reduction, got %.1f → %.1f", before, after)
	}
	got := o.ConversationHistory()
	if len(got) == 0 || got[len(got)-1].Content != hist[len(hist)-1].Content {
		t.Error("compacted history must preserve the last message")
	}
	if len(got) >= len(hist) {
		t.Errorf("expected fewer messages after compaction, got %d of %d", len(got), len(hist))
	}
}

func TestCompactConversationHistory_ZeroWindowYieldsZeroPercent(t *testing.T) {
	// No model registry → display window unknown → percents 0 but compaction
	// still succeeds and emits.
	o, spy := newCompactionTestOrchestrator(nil)
	o.modelRegistry = nil
	o.SetConversationHistory(compactionHistory(30))

	before, after, err := o.CompactConversationHistory(context.Background(), "sliding_window")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if before != 0 || after != 0 {
		t.Errorf("expected 0 percentages without a window, got %.1f/%.1f", before, after)
	}
	if len(spy.compactions) != 1 || len(spy.fills) != 1 {
		t.Error("events must still be emitted without a known window")
	}
}

// compactionSummarizePromptForTest mirrors the embedded prompt identity used
// by the manual-compaction summarize wiring (kept here as an indirection so a
// prompt file rename breaks this test loudly).
func compactionSummarizePromptForTest() string {
	return coreprompts.CompactionSummarize
}
