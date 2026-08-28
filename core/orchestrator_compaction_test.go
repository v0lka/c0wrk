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
// Token-budget arithmetic shared by the tests below: window 1000 − output
// limit 100 − safety margin 5% (50) → effective base 850; the unset
// ManualTargetPercent falls back to 30 → SDK budget 850×30/100 = 255 tokens.
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

// lightHistory builds pairs of tiny messages ("hi!" ≈ 6/8 tokens) for
// token-budget no-op tests: many messages, few tokens — 17 pairs (34
// messages) total ≈ 238 tokens, under the 255-token budget yet far past the
// count-based window (KeepFirst+KeepLast = 6).
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

// heavyHistory builds pairs of heavy messages sized by contentChars: 380
// chars → ~100/102 tokens per message (~202/pair); 192 chars → ~53/55
// tokens per message (~108/pair).
func heavyHistory(pairs, contentChars int) []llm.Message {
	body := strings.Repeat("x", contentChars)
	msgs := make([]llm.Message, 0, pairs*2)
	for i := 0; i < pairs; i++ {
		msgs = append(msgs,
			llm.Message{Role: "user", Content: body},
			llm.Message{Role: "assistant", Content: body},
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
	// 4 messages ≈ 144 tokens — within the 255-token budget (30% of the
	// 850-token effective window): token-budget mode returns them verbatim.
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

// TestCompactConversationHistory_LongLightHistoryWithinTargetIsNoOp pins the
// token-budget no-op semantics: a LONG but LIGHT history — 34 messages
// totaling ≈238 tokens, under the 255-token budget (30% of the 850-token
// effective window) yet far past the count-based window (KeepFirst+KeepLast
// = 6) — must return ErrNothingCompacted. In token-budget mode the no-op
// decision is made on tokens alone; the message count is irrelevant.
func TestCompactConversationHistory_LongLightHistoryWithinTargetIsNoOp(t *testing.T) {
	o, spy := newCompactionTestOrchestrator(nil)
	hist := lightHistory(17) // 34 messages ≈ 238 tokens ≤ 255 budget
	o.SetConversationHistory(hist)

	before, after, err := o.CompactConversationHistory(context.Background(), "sliding_window")
	if !errors.Is(err, ErrNothingCompacted) {
		t.Fatalf("expected ErrNothingCompacted for a history within the target budget, got %v", err)
	}
	if before != 0 || after != 0 {
		t.Errorf("no-op must return zero percentages, got %.1f/%.1f", before, after)
	}
	if got := o.ConversationHistory(); len(got) != len(hist) {
		t.Fatalf("history must be untouched on no-op, got %d of %d messages", len(got), len(hist))
	}
	if len(spy.compactions) != 0 || len(spy.fills) != 0 {
		t.Fatal("no events may be emitted when nothing was compacted")
	}
}

// TestCompactConversationHistory_ShortHeavyHistoryAboveTargetCompacts is the
// budget wiring's core scenario: a SHORT but HEAVY history — 6 messages ≈606
// tokens, exactly at the count-based window (KeepFirst 2 + KeepLast 4, where
// the count-based fallback would no-op) yet far over the 255-token budget —
// must compact, and the result must land within the target on both bases
// (afterPercent ≤ 30).
func TestCompactConversationHistory_ShortHeavyHistoryAboveTargetCompacts(t *testing.T) {
	o, spy := newCompactionTestOrchestrator(nil)
	hist := heavyHistory(3, 380) // 6 messages ≈ 606 tokens > 255 budget
	o.SetConversationHistory(hist)

	before, after, err := o.CompactConversationHistory(context.Background(), "sliding_window")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := o.ConversationHistory()
	if len(got) >= len(hist) {
		t.Fatalf("an over-budget history must compact, got %d of %d messages", len(got), len(hist))
	}
	if got[len(got)-1].Content != hist[len(hist)-1].Content {
		t.Error("last message must be preserved")
	}
	// afterPercent ≤ target: the compacted history fits the budget (the
	// effective-base target); the display-base return value is computed
	// against the LARGER advertised window, so it is within the target too.
	const budgetTokens = 255 // 850 × 30%
	if gotTokens := llm.NewSimpleTokenCounter().CountMessages(got); gotTokens > budgetTokens {
		t.Errorf("compacted history must fit the %d-token budget, got %d", budgetTokens, gotTokens)
	}
	if after > defaultManualTargetPercent {
		t.Errorf("after percent %.2f exceeds the %d%% target", after, defaultManualTargetPercent)
	}
	if before <= after {
		t.Errorf("expected fill reduction, got %.1f → %.1f", before, after)
	}
	if len(spy.compactions) != 1 || len(spy.fills) != 1 {
		t.Error("a real compaction must emit the compaction card + refreshed fill")
	}
}

// TestCompactConversationHistory_TwelveHeavyMessagesAt75PercentWindowCompacts
// pins the acceptance scenario "12 heavy messages ≈75% of the window": 12
// messages ≈648 tokens ≈ 76% of the 850-token effective window — below the
// executor's compaction thresholds, but over the 30% manual target (255
// tokens) — so a user-triggered compaction must actually compact it and land
// within the target.
func TestCompactConversationHistory_TwelveHeavyMessagesAt75PercentWindowCompacts(t *testing.T) {
	o, spy := newCompactionTestOrchestrator(nil)
	hist := heavyHistory(6, 192) // 12 messages ≈ 648 tokens ≈ 76% of 850
	o.SetConversationHistory(hist)

	before, after, err := o.CompactConversationHistory(context.Background(), "sliding_window")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := o.ConversationHistory()
	if len(got) >= len(hist) {
		t.Fatalf("a ~75%%-fill history is over the 30%% target and must compact, got %d of %d messages", len(got), len(hist))
	}
	if got[len(got)-1].Content != hist[len(hist)-1].Content {
		t.Error("last message must be preserved")
	}
	const budgetTokens = 255 // 850 × 30%
	if gotTokens := llm.NewSimpleTokenCounter().CountMessages(got); gotTokens > budgetTokens {
		t.Errorf("compacted history must fit the %d-token budget, got %d", budgetTokens, gotTokens)
	}
	if after > defaultManualTargetPercent {
		t.Errorf("after percent %.2f exceeds the %d%% target", after, defaultManualTargetPercent)
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

	// History replaced — token-budget mode sizes the verbatim zones as budget
	// shares (budget 255, harness arithmetic in newCompactionTestOrchestrator):
	// head share 0.15×255 = 38 tokens fits only the 32-token first message
	// (the 40-token second busts it) → 1 head; tail share 0.8×255 = 204 fits
	// the 4-message (144-token) tail capped at KeepLast=4. Result:
	// 1 head + 1 omission note + 4 tail = 6 messages.
	got := o.ConversationHistory()
	if len(got) != 6 {
		t.Fatalf("expected 6 compacted messages, got %d", len(got))
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

// TestCompactConversationHistory_NilTokenCounterSkipsNoOpDetection verifies
// the no-op detection guard: without a token counter the token equality is
// vacuous (0 == 0) and the check would degrade to length-only, misclassifying
// a real same-length content-rewriting compaction as a no-op and silently
// dropping it. The guard skips the detection entirely — the compacted history
// is always swapped and the events emitted; an identical result is a harmless
// idempotent swap.
func TestCompactConversationHistory_NilTokenCounterSkipsNoOpDetection(t *testing.T) {
	o, spy := newCompactionTestOrchestrator(nil)
	o.tokenCounter = nil
	hist := compactionHistory(2) // 4 messages — within KeepFirst(2)+KeepLast(4): nothing would drop
	o.SetConversationHistory(hist)

	before, after, err := o.CompactConversationHistory(context.Background(), "sliding_window")
	if err != nil {
		t.Fatalf("nil token counter must skip the no-op detection, got error: %v", err)
	}
	if len(spy.compactions) != 1 || len(spy.fills) != 1 {
		t.Error("events must be emitted when the no-op detection is skipped")
	}
	// No counter → no token counts → both percentage bases report unknown (0).
	if before != 0 || after != 0 {
		t.Errorf("expected 0 percentages without a token counter, got %.1f/%.1f", before, after)
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
	// Unknown window → budget 0 → sp4rk's count-based fallback with the exact
	// pre-budget behavior: the count-mode window shape (2 head + 1 note +
	// 4 tail = 7 messages), not a budget-share shape.
	if got := o.ConversationHistory(); len(got) != 7 {
		t.Errorf("unknown window must keep the count-mode behavior: expected 7 messages, got %d", len(got))
	}
}

// compactionSummarizePromptForTest mirrors the embedded prompt identity used
// by the manual-compaction summarize wiring (kept here as an indirection so a
// prompt file rename breaks this test loudly).
func compactionSummarizePromptForTest() string {
	return coreprompts.CompactionSummarize
}

func TestManualCompactionWouldNoOp_EmptyHistory(t *testing.T) {
	o, _ := newCompactionTestOrchestrator(nil)
	if !o.ManualCompactionWouldNoOp() {
		t.Error("empty history must predict a no-op (nothing to compact)")
	}
	// The empty verdict holds in the fallback mode too (no registry).
	o.modelRegistry = nil
	if !o.ManualCompactionWouldNoOp() {
		t.Error("empty history must predict a no-op in the fallback mode too")
	}
}

func TestManualCompactionWouldNoOp_BudgetMode(t *testing.T) {
	o, _ := newCompactionTestOrchestrator(nil)
	// Budget arithmetic (see harness comment): effective base 850, unset
	// target → 30 → budget 255 tokens.
	// 34 light messages ≈ 238 tokens ≤ 255 — within budget → no-op.
	o.SetConversationHistory(lightHistory(17))
	if !o.ManualCompactionWouldNoOp() {
		t.Error("history within the token budget must predict a no-op")
	}
	// 6 heavy messages ≈ 606 tokens > 255 — above budget → would compact.
	o.SetConversationHistory(heavyHistory(3, 380))
	if o.ManualCompactionWouldNoOp() {
		t.Error("history above the token budget must not predict a no-op")
	}
}

func TestManualCompactionWouldNoOp_FallbackMode(t *testing.T) {
	// Unknown window (no registry): the count-mode fallback, predicted by
	// the conservative length bound len ≤ 2.
	o, _ := newCompactionTestOrchestrator(nil)
	o.modelRegistry = nil
	o.SetConversationHistory(lightHistory(1)) // 2 messages
	if !o.ManualCompactionWouldNoOp() {
		t.Error("two messages without a known window must predict a no-op")
	}
	o.SetConversationHistory(lightHistory(2)) // 4 messages — token-trivial but long
	if o.ManualCompactionWouldNoOp() {
		t.Error("more than two messages without a known window must not predict a no-op")
	}

	// Nil token counter: same fallback bound even with a known window (the
	// SDK cannot run budget mode without a counter).
	o2, _ := newCompactionTestOrchestrator(nil)
	o2.tokenCounter = nil
	o2.SetConversationHistory(lightHistory(1))
	if !o2.ManualCompactionWouldNoOp() {
		t.Error("nil counter must fall back to the strategy-zone bound: 2 messages → no-op")
	}
	o2.SetConversationHistory(lightHistory(2))
	if o2.ManualCompactionWouldNoOp() {
		t.Error("nil counter must fall back to the strategy-zone bound: 4 messages → not a no-op")
	}
}

func TestManualCompactionWouldNoOp_TinyWindowBudgetRoundsToZero(t *testing.T) {
	// A window so tiny the budget rounds to zero runs the real compaction in
	// the count-mode fallback — the predicate must mirror that (the
	// strategy-zone bound), not the (vacuous) token comparison against
	// budget 0.
	o, _ := newCompactionTestOrchestrator(nil)
	o.modelRegistry = llm.NewModelRegistry(map[string]llm.ModelMetadata{
		// 10 − 7 output − 10×5% safety (=0) → effective 3; 3×30/100 = 0.
		"test-model": {ContextWindow: 10, OutputLimit: 7, Family: "test"},
	})
	o.SetConversationHistory(lightHistory(1)) // len 2
	if !o.ManualCompactionWouldNoOp() {
		t.Error("zero budget on a tiny window must use the strategy-zone fallback: 2 messages → no-op")
	}
	o.SetConversationHistory(lightHistory(2)) // len 4
	if o.ManualCompactionWouldNoOp() {
		t.Error("zero budget on a tiny window must use the strategy-zone fallback: 4 messages → not a no-op")
	}
}

func TestManualCompactionWouldNoOp_FallbackBoundTracksStrategyZones(t *testing.T) {
	// The fallback bound is the MINIMUM of the strategies' verbatim floors,
	// each resolved with sp4rk's zero-value defaults. With the harness
	// config (sliding 2+4, summarization keep_last 4, hierarchical default
	// ratios 0.4/0.3) the floors are 6/4/2 → bound 2 — the same verdicts as
	// the old hard-coded 2, now derived from the config.
	o, _ := newCompactionTestOrchestrator(nil)
	o.modelRegistry = nil // unknown window → count-mode fallback
	if got := o.manualCompactionNoOpLength(); got != 2 {
		t.Fatalf("default-zone bound = %d, want 2 (min of sliding 6, summarization 4, hierarchical 2)", got)
	}

	// Exotic zone: summarization keep_last 1 tightens the bound to 1 — a
	// two-message history is genuinely compactable (its first message gets
	// summarized), so the button must stay enabled instead of being
	// disabled by a stale hard-coded 2.
	o.config.Compaction.Summarization.KeepLast = 1
	if got := o.manualCompactionNoOpLength(); got != 1 {
		t.Fatalf("summarization keep_last=1 bound = %d, want 1", got)
	}
	o.SetConversationHistory(lightHistory(1)) // len 2
	if o.ManualCompactionWouldNoOp() {
		t.Error("2 messages must NOT predict a no-op when summarization keep_last=1 — the history is compactable")
	}

	// Exotic ratio: a hierarchical distant ratio ≥ 0.5 fills the distant
	// zone at n=2, dropping that floor to 1 as well.
	o2, _ := newCompactionTestOrchestrator(nil)
	o2.modelRegistry = nil
	o2.config.Compaction.Hierarchical.DistantRatio = 0.5
	if got := o2.manualCompactionNoOpLength(); got != 1 {
		t.Fatalf("hierarchical distant_ratio=0.5 bound = %d, want 1", got)
	}

	// Wider zones widen the bound: tiny hierarchical ratios (0.1/0.1 →
	// floor 9) with the default 2+4/keep_last 4 zones lift the bound to
	// min(6, 4, 9) = 4 — a 4-message history is verbatim under every
	// strategy, so the prediction correctly disables the button.
	o3, _ := newCompactionTestOrchestrator(nil)
	o3.modelRegistry = nil
	o3.config.Compaction.Hierarchical.DistantRatio = 0.1
	o3.config.Compaction.Hierarchical.MiddleRatio = 0.1
	if got := o3.manualCompactionNoOpLength(); got != 4 {
		t.Fatalf("widened-zone bound = %d, want 4 (min of sliding 6, summarization 4, hierarchical 9)", got)
	}
	o3.SetConversationHistory(lightHistory(2)) // len 4
	if !o3.ManualCompactionWouldNoOp() {
		t.Error("4 messages must predict a no-op when every strategy keeps them verbatim")
	}
	o3.SetConversationHistory(lightHistory(3)) // len 6 > summarization floor 4
	if o3.ManualCompactionWouldNoOp() {
		t.Error("6 messages exceed the summarization keep_last=4 floor → not a no-op")
	}
}

func TestManualCompactionWouldNoOp_FallbackBoundSoundAgainstSDK(t *testing.T) {
	// The bound must never claim a no-op for a history the SDK would really
	// compact (fail-open is only sound when every claimed no-op is real):
	// summarization with keep_last=1 rewrites a two-message history (the
	// first message becomes a summary block), so the prediction must say
	// "would compact" AND the real fallback-mode compaction must not return
	// ErrNothingCompacted.
	caller := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{Message: llm.Message{Role: "assistant", Content: "SUMMARY"}}, nil
		},
	}
	o, _ := newCompactionTestOrchestrator(caller)
	o.modelRegistry = nil // unknown window → count-mode fallback for the real compaction too
	o.config.Compaction.Summarization.KeepLast = 1
	o.SetConversationHistory(lightHistory(1)) // len 2

	if o.ManualCompactionWouldNoOp() {
		t.Fatal("predicted no-op, but keep_last=1 summarization rewrites a 2-message history")
	}
	if _, _, err := o.CompactConversationHistory(context.Background(), "summarization"); errors.Is(err, ErrNothingCompacted) {
		t.Fatal("the SDK must really compact this history — the fail-open prediction was justified")
	}
}

func TestManualCompactionWouldNoOp_ConcurrentWithHistoryWriters(t *testing.T) {
	// The predicate runs on Wails-RPC goroutines (the runtime status poll,
	// the post-flow recomputation after ResumeTask) while the request
	// goroutine appends the outcome exchange — every read must go through
	// historyMu. Hammer writers and readers concurrently; `go test -race`
	// turns any unsynchronized access into a failure.
	o, _ := newCompactionTestOrchestrator(nil)
	o.modelRegistry = nil // fallback mode: full length/token walk over the snapshot

	const writers, iterations = 4, 200
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				o.appendHistory("", llm.Message{Role: "assistant", Content: "tick"})
				_ = o.ManualCompactionWouldNoOp()
				_ = o.historySnapshot()
			}
		}()
	}
	wg.Wait()

	if got := len(o.ConversationHistory()); got != writers*iterations {
		t.Fatalf("history length = %d, want %d", got, writers*iterations)
	}
}

func TestManualCompactionWouldNoOp_AgreesWithCompactionOutcome(t *testing.T) {
	// The prediction must match what CompactConversationHistory actually
	// does: a predicted no-op returns ErrNothingCompacted; a predicted
	// compaction succeeds (and afterwards the flag flips to true).
	o, _ := newCompactionTestOrchestrator(nil)

	// Within budget → no-op both in prediction and in outcome.
	o.SetConversationHistory(lightHistory(17))
	if !o.ManualCompactionWouldNoOp() {
		t.Fatal("setup: light history must predict a no-op")
	}
	if _, _, err := o.CompactConversationHistory(context.Background(), "sliding_window"); !errors.Is(err, ErrNothingCompacted) {
		t.Fatalf("predicted no-op but compaction returned %v", err)
	}

	// Above budget → compacts, and the post-compaction state flips the flag.
	o.SetConversationHistory(heavyHistory(3, 380))
	if o.ManualCompactionWouldNoOp() {
		t.Fatal("setup: heavy history must not predict a no-op")
	}
	if _, _, err := o.CompactConversationHistory(context.Background(), "sliding_window"); err != nil {
		t.Fatalf("predicted compaction failed: %v", err)
	}
	if !o.ManualCompactionWouldNoOp() {
		t.Error("after a successful compaction the compacted history must predict a no-op")
	}
}
