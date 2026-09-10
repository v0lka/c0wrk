package core

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
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
//
// Manual compaction is PURELY STRUCTURAL: each strategy applies its
// message-count window (sliding KeepFirst+KeepLast, summarization KeepLast,
// hierarchical ratios) — token counts never gate the no-op decision. With the
// harness config (sliding 2+4, summarization keep_last 4) the per-strategy
// verbatim floors are: sliding 6, summarization 4, hierarchical 2 (default
// ratios 0.4/0.3).
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
	cfg.Compaction.SafetyMarginPercent = 5
	o := &Orchestrator{
		emitter:      spy,
		tokenCounter: llm.NewSimpleTokenCounter(),
		modelRegistry: llm.NewModelRegistry(map[string]llm.ModelMetadata{
			// OutputLimit must be explicit: a partial override inherits the
			// unknown-model fallback (32768), which would swallow the whole
			// 1000-token window in the effective-base math.
			"test-model": {ContextWindow: 1000, OutputLimit: 100, Family: "test"},
		}),
		config:             cfg,
		compactionForecast: resolveCompactionForecast(BuilderCompactionForecast{}),
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

// lightHistory builds pairs of tiny messages ("hi!" ≈ 6/8 tokens). Under the
// OLD token-budget semantics many such messages (17 pairs = 34 messages ≈ 238
// tokens) were a no-op because the token count fit the 30% budget; under the
// NEW purely-structural semantics the message COUNT decides, so the same
// 34-message history is compactable by every strategy.
func lightHistory(pairs int) []llm.Message {
	msgs := make([]llm.Message, 0, pairs*2)
	for i := 0; i < pairs; i++ {
		msgs = append(msgs,
			llm.Message{Role: "user", Content: "hi!"},
			llm.Message{Role: "assistant", Content: "hi!"},
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

func TestCompactConversationHistory_NoOpReturnsErrNothingCompacted(t *testing.T) {
	o, spy := newCompactionTestOrchestrator(nil)
	// 4 messages — within sliding's 2+4 window: structural mode returns them
	// verbatim (the prediction's WillCompact is exact and false).
	hist := compactionHistory(2)
	o.SetConversationHistory(hist)

	before, after, err := o.CompactConversationHistory(context.Background(), "sliding_window")
	if !errors.Is(err, ErrNothingCompacted) {
		t.Fatalf("expected ErrNothingCompacted, got %v", err)
	}
	if before != 0 || after != 0 {
		t.Errorf("no-op must return zero percentages, got %.1f/%.1f", before, after)
	}
	// History untouched — the no-op path must not swap.
	if got := o.ConversationHistory(); len(got) != len(hist) {
		t.Fatalf("history must be untouched on no-op, got %d of %d messages", len(got), len(hist))
	}
	if len(spy.compactions) != 0 || len(spy.fills) != 0 {
		t.Fatal("no events may be emitted when nothing was compacted")
	}
}

// TestCompactConversationHistory_LongLightHistoryCompacts pins the fix that
// the whole refactor exists for: a LONG but LIGHT history — 34 messages, tiny
// tokens — must compact. The OLD token-budget gate (30% of the window) made
// this a no-op because the TOKEN count fit the budget even though the MESSAGE
// count far exceeded every strategy's window; the new purely-structural mode
// decides on message counts, so it compacts.
func TestCompactConversationHistory_LongLightHistoryCompacts(t *testing.T) {
	o, spy := newCompactionTestOrchestrator(nil)
	hist := lightHistory(17) // 34 messages
	o.SetConversationHistory(hist)

	before, after, err := o.CompactConversationHistory(context.Background(), "sliding_window")
	if err != nil {
		t.Fatalf("a 34-message history must compact in structural mode, got %v", err)
	}
	got := o.ConversationHistory()
	if len(got) >= len(hist) {
		t.Fatalf("a 34-message history must compact, got %d of %d messages", len(got), len(hist))
	}
	if got[len(got)-1].Content != hist[len(hist)-1].Content {
		t.Error("last message must be preserved")
	}
	if before <= after {
		t.Errorf("expected fill reduction, got %.1f → %.1f", before, after)
	}
	if len(spy.compactions) != 1 || len(spy.fills) != 1 {
		t.Error("a real compaction must emit the compaction card + refreshed fill")
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

	// History replaced — structural mode sizes the window in MESSAGES:
	// keepFirst 2 + omission note + keepLast 4 = 7 messages.
	got := o.ConversationHistory()
	if len(got) != 7 {
		t.Fatalf("expected 7 compacted messages (2 head + note + 4 tail), got %d", len(got))
	}
	if got[len(got)-1].Content != hist[len(hist)-1].Content {
		t.Error("last message must be preserved")
	}

	// Returned percentages use the DISPLAY base (the advertised window 1000):
	// the caller persists them into the marker row and the
	// compaction_finished event, and the frontend renders the marker's
	// metadata verbatim on reload — they must match what the live card
	// shows.
	const displayMax = 1000
	counter := llm.NewSimpleTokenCounter()
	wantBefore := float64(counter.CountMessages(hist)) / displayMax * 100
	wantAfter := float64(counter.CountMessages(got)) / displayMax * 100
	if math.Abs(before-wantBefore) > 0.001 || math.Abs(after-wantAfter) > 0.001 {
		t.Errorf("display-base percentages: got %.2f/%.2f, want %.2f/%.2f", before, after, wantBefore, wantAfter)
	}
	if before <= after {
		t.Errorf("expected before (%.1f) > after (%.1f)", before, after)
	}

	// Events: compaction card + refreshed fill — on the EFFECTIVE base
	// (window 1000 − output limit 100 − safety margin 1000*5/100=50 → 850),
	// the basis the emitter's ContextCompaction display scaling expects.
	const effectiveMax = 850
	wantBeforeEff := float64(counter.CountMessages(hist)) / effectiveMax * 100
	wantAfterEff := float64(counter.CountMessages(got)) / effectiveMax * 100
	if len(spy.compactions) != 1 {
		t.Fatalf("expected exactly 1 ContextCompaction emission, got %d", len(spy.compactions))
	}
	if math.Abs(spy.compactions[0].before-wantBeforeEff) > 0.001 || math.Abs(spy.compactions[0].after-wantAfterEff) > 0.001 {
		t.Errorf("ContextCompaction effective-base percentages: got %.2f/%.2f, want %.2f/%.2f", spy.compactions[0].before, spy.compactions[0].after, wantBeforeEff, wantAfterEff)
	}
	if len(spy.fills) != 1 {
		t.Fatalf("expected exactly 1 ContextFill emission, got %d", len(spy.fills))
	}
	// ContextFill carries the effective max (executor basis); the emitter's
	// display override recomputes the user-facing values itself.
	if spy.fills[0].status != "ok" || spy.fills[0].maxTokens != effectiveMax {
		t.Errorf("unexpected ContextFill payload: %+v", spy.fills[0])
	}
	if math.Abs(spy.fills[0].percent-wantAfterEff) > 0.001 {
		t.Errorf("ContextFill percent = %.2f, want effective-based %.2f", spy.fills[0].percent, wantAfterEff)
	}
}

// TestCompactConversationHistory_NilTokenCounterStillCompacts verifies the
// structural no-op decision does not need a token counter: the prediction's
// WillCompact is exact regardless. A nil counter still compacts an
// over-window history (reporting zero percentages) and still returns
// ErrNothingCompacted for an under-window one.
func TestCompactConversationHistory_NilTokenCounterStillCompacts(t *testing.T) {
	o, spy := newCompactionTestOrchestrator(nil)
	o.tokenCounter = nil

	// Under the window (4 messages ≤ sliding 6): structural no-op.
	hist := compactionHistory(2)
	o.SetConversationHistory(hist)
	if _, _, err := o.CompactConversationHistory(context.Background(), "sliding_window"); !errors.Is(err, ErrNothingCompacted) {
		t.Fatalf("under-window history must no-op without a counter, got %v", err)
	}
	if len(spy.compactions) != 0 || len(spy.fills) != 0 {
		t.Fatal("no events may be emitted for a no-op")
	}

	// Over the window: compacts, zero percentages (no counter → unknown).
	hist = compactionHistory(30)
	o.SetConversationHistory(hist)
	before, after, err := o.CompactConversationHistory(context.Background(), "sliding_window")
	if err != nil {
		t.Fatalf("over-window history must compact without a counter, got %v", err)
	}
	if before != 0 || after != 0 {
		t.Errorf("expected 0 percentages without a token counter, got %.1f/%.1f", before, after)
	}
	if len(spy.compactions) != 1 || len(spy.fills) != 1 {
		t.Error("events must be emitted for a real compaction")
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
	// No model registry → effective base unknown → percents 0 but compaction
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
	// Structural window is independent of the model window: 2 head + note +
	// 4 tail = 7 messages.
	if got := o.ConversationHistory(); len(got) != 7 {
		t.Errorf("structural window must not depend on the model window: expected 7 messages, got %d", len(got))
	}
}

// fixedCounter is a deterministic token counter for refine tests: every message
// counts as exactly `fixedCounter` tokens, so zone token sums are controlled by
// message counts alone.
type fixedCounter int

func (c fixedCounter) Count(_ string) int                   { return int(c) }
func (c fixedCounter) CountMessages(msgs []llm.Message) int { return int(c) * len(msgs) }

// TestRefineCompactionForecast pins the EWMA calibration: a summarization
// compaction whose summaries occupied half the summarized input refines the
// SummarizationRatio toward 0.5; hierarchical refines the distant and middle
// ratios INDEPENDENTLY from their own zones' observed ratios; sliding_window
// never calibrates; a zero summarized-input leaves the forecast untouched.
func TestRefineCompactionForecast(t *testing.T) {
	o, _ := newCompactionTestOrchestrator(nil)
	o.tokenCounter = fixedCounter(100)

	// summarization: 10 messages (1000 tokens), 200 verbatim → 800 summarized;
	// 6-message output (600 tokens) → summaries at 400 → observed 0.5.
	o.refineCompactionForecast("summarization", fixedMessages(10), fixedMessages(6), 200)
	got := o.CompactionForecast()
	// EWMA from the 0.3 seed: 0.3 + 0.3*(0.5-0.3) = 0.36.
	if math.Abs(got.SummarizationRatio-0.36) > 0.0001 {
		t.Errorf("SummarizationRatio = %.4f, want 0.36", got.SummarizationRatio)
	}

	// hierarchical, default ratios 0.4/0.3/0.3 over 5 messages: distant=2,
	// middle=1, recent=2. Compacted = [1 distant summary] + [1 middle summary]
	// + [2 recent verbatim] = 4 messages. With 100 tokens/message the distant
	// zone observed ratio is 100/200 = 0.5 and the middle zone 100/100 = 1.0 —
	// each field calibrates from its OWN zone, not one aggregate.
	o.refineCompactionForecast("hierarchical", fixedMessages(5), fixedMessages(4), 200)
	got = o.CompactionForecast()
	// distant: 0.15 + 0.3*(0.5-0.15) = 0.255; middle: 0.3 + 0.3*(1.0-0.3) = 0.51.
	if math.Abs(got.HierarchicalDistantRatio-0.255) > 0.0001 {
		t.Errorf("HierarchicalDistantRatio = %.4f, want 0.255 (0.15 + 0.3*0.35)", got.HierarchicalDistantRatio)
	}
	if math.Abs(got.HierarchicalMiddleRatio-0.51) > 0.0001 {
		t.Errorf("HierarchicalMiddleRatio = %.4f, want 0.51 (0.3 + 0.3*0.7)", got.HierarchicalMiddleRatio)
	}

	// sliding_window never calibrates.
	before := o.CompactionForecast()
	o.refineCompactionForecast("sliding_window", fixedMessages(10), fixedMessages(6), 200)
	if o.CompactionForecast() != before {
		t.Error("sliding_window must not calibrate the forecast")
	}

	// Zero summarized input → nothing to learn (2 messages = 200 tokens, all
	// verbatim).
	before = o.CompactionForecast()
	o.refineCompactionForecast("summarization", fixedMessages(2), fixedMessages(2), 200)
	if o.CompactionForecast() != before {
		t.Error("zero summarized input must leave the forecast untouched")
	}
}

func fixedMessages(n int) []llm.Message {
	msgs := make([]llm.Message, n)
	for i := range msgs {
		msgs[i] = llm.Message{Role: "user", Content: "x"}
	}
	return msgs
}

// compactionSummarizePromptForTest mirrors the embedded prompt identity used
// by the manual-compaction summarize wiring (kept here as an indirection so a
// prompt file rename breaks this test loudly).
func compactionSummarizePromptForTest() string {
	return coreprompts.CompactionSummarize
}

// availability returns the availability entry for the named strategy, failing
// the test if it is absent.
func availability(t *testing.T, avail []CompactionAvailability, strategy string) CompactionAvailability {
	t.Helper()
	for _, a := range avail {
		if a.Strategy == strategy {
			return a
		}
	}
	t.Fatalf("strategy %q not present in availability %+v", strategy, avail)
	return CompactionAvailability{}
}

func TestManualCompactionAvailability_EmptyHistory(t *testing.T) {
	o, _ := newCompactionTestOrchestrator(nil)
	avail := o.ManualCompactionAvailability()
	if len(avail) != 3 {
		t.Fatalf("expected 3 strategies, got %d", len(avail))
	}
	for _, a := range avail {
		if a.Available {
			t.Errorf("%s: empty history must not be available", a.Strategy)
		}
	}
}

func TestManualCompactionAvailability_ShortHistoryAllUnavailable(t *testing.T) {
	// 2 messages: under every strategy's window (sliding 6, summarization 4,
	// hierarchical 2) — nothing is available.
	o, _ := newCompactionTestOrchestrator(nil)
	o.SetConversationHistory(lightHistory(1)) // 2 messages
	for _, a := range o.ManualCompactionAvailability() {
		if a.Available {
			t.Errorf("%s: 2 messages must be unavailable", a.Strategy)
		}
	}
}

func TestManualCompactionAvailability_PerStrategyVerdicts(t *testing.T) {
	// 34 light messages: over EVERY window — all three available. Reclaim is
	// positive and sliding is exact (dry run) while the LLM strategies are
	// forecasts.
	o, _ := newCompactionTestOrchestrator(nil)
	o.SetConversationHistory(lightHistory(17)) // 34 messages

	avail := o.ManualCompactionAvailability()
	for _, a := range avail {
		if !a.Available {
			t.Errorf("%s: 34 messages must be available", a.Strategy)
		}
		if a.ReclaimTokens <= 0 {
			t.Errorf("%s: expected positive reclaim, got %d", a.Strategy, a.ReclaimTokens)
		}
	}
	if got := availability(t, avail, "sliding_window"); !got.Exact {
		t.Error("sliding_window reclaim must be exact (dry run)")
	}
	if got := availability(t, avail, "summarization"); got.Exact {
		t.Error("summarization reclaim must be a forecast (not exact)")
	}
	if got := availability(t, avail, "hierarchical"); got.Exact {
		t.Error("hierarchical reclaim must be a forecast (not exact)")
	}
}

func TestManualCompactionAvailability_StructuralVerdictIndependentOfWindow(t *testing.T) {
	// The availability is structural — a known model window does NOT change
	// it. A 34-message history is compactable with and without a registry.
	o, _ := newCompactionTestOrchestrator(nil)
	o.SetConversationHistory(lightHistory(17))

	withWindow := o.ManualCompactionAvailability()
	o.modelRegistry = nil
	withoutWindow := o.ManualCompactionAvailability()

	for i := range withWindow {
		if withWindow[i].Available != withoutWindow[i].Available {
			t.Errorf("%s: availability must not depend on the model window (with=%v, without=%v)",
				withWindow[i].Strategy, withWindow[i].Available, withoutWindow[i].Available)
		}
	}
}

func TestManualCompactionAvailability_TracksStrategyConfig(t *testing.T) {
	// Exotic config: summarization keep_last 1 makes a 2-message history
	// compactable by summarization (the first message gets summarized) while
	// sliding (2+4) and hierarchical (default ratios → floor 2) stay verbatim.
	o, _ := newCompactionTestOrchestrator(nil)
	o.config.Compaction.Summarization.KeepLast = 1
	o.SetConversationHistory(lightHistory(1)) // 2 messages

	avail := o.ManualCompactionAvailability()
	if got := availability(t, avail, "summarization"); !got.Available {
		t.Error("keep_last=1 summarization must be available for 2 messages")
	}
	if got := availability(t, avail, "sliding_window"); got.Available {
		t.Error("sliding (2+4) must stay unavailable for 2 messages")
	}
}

func TestManualCompactionAvailability_ConcurrentWithHistoryWriters(t *testing.T) {
	// The prediction runs on Wails-RPC goroutines (the runtime status poll,
	// the post-flow recomputation after ResumeTask) while the request
	// goroutine appends the outcome exchange — every read must go through
	// historyMu. Hammer writers and readers concurrently; `go test -race`
	// turns any unsynchronized access into a failure.
	o, _ := newCompactionTestOrchestrator(nil)

	const writers, iterations = 4, 200
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				o.appendHistory("", llm.Message{Role: "assistant", Content: "tick"})
				_ = o.ManualCompactionAvailability()
				_ = o.historySnapshot()
			}
		}()
	}
	wg.Wait()

	if got := len(o.ConversationHistory()); got != writers*iterations {
		t.Fatalf("history length = %d, want %d", got, writers*iterations)
	}
}

func TestModelIdentity_ConcurrentReadersAndWriters(t *testing.T) {
	// The model-identity fields (config.Model / config.ReasoningEffort) are
	// written by the request goroutine (ApplyRequestOverrides /
	// SetReasoningEffort) and read from Wails-RPC goroutines (the runtime
	// status poll → ManualCompactionAvailability → contextBases) — the exact
	// cross-goroutine window historyMu covers for the history. All access
	// goes through the modelMu-guarded accessors; `go test -race` turns any
	// unsynchronized access into a failure.
	o, _ := newCompactionTestOrchestrator(nil)
	o.SetConversationHistory(lightHistory(2))

	const writers, iterations = 4, 200
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				// Mirrors ApplyRequestOverrides' writer side.
				o.setCurrentModel("test-model")
				o.setCurrentReasoningEffort("high")
			}
		}()
	}
	// Reader side: the status-poll path resolves the model's window while a
	// request may be switching models mid-flight.
	for i := 0; i < iterations; i++ {
		_ = o.currentModel()
		_ = o.currentReasoningEffort()
		_, _ = o.contextBases()
		_ = o.ManualCompactionAvailability()
	}
	wg.Wait()
}

func TestManualCompactionAvailability_AgreesWithCompactionOutcome(t *testing.T) {
	// The per-strategy availability must match what CompactConversationHistory
	// actually does: an unavailable strategy returns ErrNothingCompacted; an
	// available one compacts.
	o, _ := newCompactionTestOrchestrator(nil)

	// Under sliding's window → unavailable AND ErrNothingCompacted.
	o.SetConversationHistory(lightHistory(2)) // 4 messages ≤ sliding 6
	if got := availability(t, o.ManualCompactionAvailability(), "sliding_window"); got.Available {
		t.Fatal("setup: 4 messages must be unavailable for sliding")
	}
	if _, _, err := o.CompactConversationHistory(context.Background(), "sliding_window"); !errors.Is(err, ErrNothingCompacted) {
		t.Fatalf("unavailable strategy but compaction returned %v", err)
	}

	// Over the window → available, compacts, and the history shrinks.
	o.SetConversationHistory(lightHistory(17))
	if got := availability(t, o.ManualCompactionAvailability(), "sliding_window"); !got.Available {
		t.Fatal("setup: 34 messages must be available for sliding")
	}
	if _, _, err := o.CompactConversationHistory(context.Background(), "sliding_window"); err != nil {
		t.Fatalf("available strategy failed: %v", err)
	}
	if got := len(o.ConversationHistory()); got >= 34 {
		t.Fatalf("compaction must shrink the history, got %d", got)
	}
}
