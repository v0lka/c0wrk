package core

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/oneshot"
)

// --- per-kind metrics ---

// TestServiceMetrics_PerKindCounters drives the title and prompt-optimizer
// helpers through the shared client and asserts that each kind records its own
// calls/attempts/retries/outcomes/latency: a success, a fallback after the full
// nudge loop, and a terminal refusal — and that kinds never bleed together.
func TestServiceMetrics_PerKindCounters(t *testing.T) {
	metrics := newServiceMetrics()

	// Title: one successful call. The mock must spend measurable time — the
	// test asserts recorded latency > 0, and an instant reply rounds to a flat
	// 0 on Windows' coarse monotonic clock (see mockLLMCaller.delay).
	titleMock := &mockLLMCaller{
		delay: 20 * time.Millisecond,
		responses: []*llm.ChatResponse{
			{Message: llm.Message{Content: "Fix auth"}},
		},
	}
	if _, err := generateTitleWithCaller(context.Background(), titleMock, metrics, "qwen3.8-max", slog.Default(), "fix auth", nil); err != nil {
		t.Fatalf("title: unexpected error: %v", err)
	}

	b := &OrchestratorBuilder{logger: slog.Default(), mu: sync.RWMutex{}, serviceMetrics: metrics}

	// Optimize extract: unparseable JSON → fallback after 3 attempts (2 nudges).
	extractMock := &mockLLMCaller{responses: []*llm.ChatResponse{
		{Message: llm.Message{Content: "not json"}},
	}}
	if _, err := b.optimizeExtract(context.Background(), extractMock, "qwen3.8-max", "prompt"); err != nil {
		t.Fatalf("extract: unexpected error: %v (fallback must swallow the parse refusal)", err)
	}

	// Optimize rewrite: empty output → terminal refusal after 3 attempts.
	rewriteMock := &mockLLMCaller{responses: []*llm.ChatResponse{
		{Message: llm.Message{Content: ""}},
	}}
	if _, err := b.optimizeRewrite(context.Background(), rewriteMock, "qwen3.8-max", "prompt"); err == nil {
		t.Fatal("rewrite: expected the terminal refusal, got nil")
	}

	snap := metrics.Snapshot()

	title, ok := snap[ServiceKindTitle]
	if !ok {
		t.Fatal("no title metrics recorded")
	}
	if title.Calls != 1 || title.Attempts != 1 || title.Retries != 0 || title.OK != 1 {
		t.Errorf("title metrics = %+v, want 1 call/1 attempt/0 retry/1 ok", title)
	}
	if title.TotalDuration <= 0 || title.MaxDuration <= 0 {
		t.Errorf("title durations = total %v / max %v, want both > 0", title.TotalDuration, title.MaxDuration)
	}

	extract := snap[ServiceKindOptimizeExtract]
	if extract.Calls != 1 || extract.Attempts != 3 || extract.Retries != 2 || extract.Fallback != 1 {
		t.Errorf("extract metrics = %+v, want 1 call/3 attempts/2 retries/1 fallback", extract)
	}

	rewrite := snap[ServiceKindOptimizeRewrite]
	if rewrite.Calls != 1 || rewrite.Attempts != 3 || rewrite.Retries != 2 || rewrite.Errors != 1 {
		t.Errorf("rewrite metrics = %+v, want 1 call/3 attempts/2 retries/1 error", rewrite)
	}

	if _, ok := snap[ServiceKindCommitMessage]; ok {
		t.Errorf("commit_message metrics present with no commit call: %+v", snap[ServiceKindCommitMessage])
	}
}

// TestServiceCall_TransportErrorClassified pins that a failure of the LLM call
// itself is counted as a transport error — never retried by the client — and
// not conflated with a terminal parse refusal.
func TestServiceCall_TransportErrorClassified(t *testing.T) {
	metrics := newServiceMetrics()
	mock := &mockLLMCaller{err: errors.New("connection refused")}
	b := &OrchestratorBuilder{logger: slog.Default(), mu: sync.RWMutex{}, serviceMetrics: metrics}

	if _, err := b.optimizeRewrite(context.Background(), mock, "qwen3.8-max", "prompt"); err == nil {
		t.Fatal("expected the transport error to propagate")
	}

	km := metrics.Snapshot()[ServiceKindOptimizeRewrite]
	if km.Calls != 1 || km.Attempts != 1 || km.TransportErrors != 1 || km.Errors != 0 {
		t.Errorf("metrics = %+v, want 1 call/1 attempt/1 transport error/0 refusal", km)
	}
}

// TestServiceCall_TimeoutClassifiedAsTransportError pins the single timeout
// path: a context deadline that fires inside the call surfaces as a transport
// error, and the call is not retried (the client never retries transport
// failures).
func TestServiceCall_TimeoutClassifiedAsTransportError(t *testing.T) {
	metrics := newServiceMetrics()
	mock := &mockLLMCaller{callFn: func(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	b := &OrchestratorBuilder{logger: slog.Default(), mu: sync.RWMutex{}, serviceMetrics: metrics}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := b.optimizeRewrite(ctx, mock, "qwen3.8-max", "prompt"); err == nil {
		t.Fatal("expected the deadline error to propagate")
	}

	km := metrics.Snapshot()[ServiceKindOptimizeRewrite]
	if km.Attempts != 1 || km.TransportErrors != 1 || km.Errors != 0 {
		t.Errorf("metrics = %+v, want one attempt classified as a transport error", km)
	}
}

// TestServiceCall_NilCallerRejected pins the defensive contract: the oneshot
// client's own nil-caller refusal never fires (it sees only the metering
// wrapper), so serviceCall must reject a nil caller itself — with an error,
// never a panic — and record and log nothing.
func TestServiceCall_NilCallerRejected(t *testing.T) {
	metrics := newServiceMetrics()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	_, err := serviceCall[string](context.Background(), metrics, logger, ServiceKindTitle, "m", nil, llm.ChatRequest{}, nil, oneshot.Options[string]{})
	if err == nil || !strings.Contains(err.Error(), "nil caller") {
		t.Fatalf("err = %v, want a nil-caller error", err)
	}
	if snap := metrics.Snapshot(); len(snap) != 0 {
		t.Errorf("a rejected call must record nothing, got %+v", snap)
	}
	if out := buf.String(); out != "" {
		t.Errorf("a rejected call must log nothing, got: %s", out)
	}
}

// TestServiceCall_LogsExactlyOncePerCall pins the single-emitter contract: the
// shared client emits one unified record per call and the underlying oneshot
// client logs nothing of its own (no double logging, no drifting fields).
func TestServiceCall_LogsExactlyOncePerCall(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b := &OrchestratorBuilder{logger: logger, mu: sync.RWMutex{}, serviceMetrics: newServiceMetrics()}
	mock := &mockLLMCaller{responses: []*llm.ChatResponse{
		{Message: llm.Message{Content: `{"translated": "x", "keywords": []}`}},
	}}

	if _, err := b.optimizeExtract(context.Background(), mock, "qwen3.8-max", "prompt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if n := strings.Count(out, "service call"); n != 1 {
		t.Errorf("want exactly one unified record, got %d:\n%s", n, out)
	}
	if strings.Contains(out, "oneshot:") {
		t.Errorf("the oneshot client must not log a second record:\n%s", out)
	}
}

// TestLogServiceCall_UnifiedFields pins the consistent field set every service
// kind carries: service_kind, model, outcome, attempts and duration.
func TestLogServiceCall_UnifiedFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logServiceCall(logger, ServiceKindCommitMessage, "qwen3.8-max", ServiceOutcomeOK, 2, 1500*time.Millisecond)

	out := buf.String()
	for _, want := range []string{
		"service_kind=commit_message",
		"model=qwen3.8-max",
		"outcome=ok",
		"attempts=2",
		"duration=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log record missing %q; got: %s", want, out)
		}
	}
	if !strings.Contains(out, "level=DEBUG") {
		t.Errorf("a success must log at DEBUG; got: %s", out)
	}
}

// TestLogServiceCall_DegradedAtWarn pins that every degraded outcome logs at
// WARN (so operators see fallbacks, refusals and transport failures).
func TestLogServiceCall_DegradedAtWarn(t *testing.T) {
	for _, outcome := range []ServiceOutcome{
		ServiceOutcomeFallback, ServiceOutcomeError, ServiceOutcomeTransportError,
	} {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		logServiceCall(logger, ServiceKindTitle, "m", outcome, 1, time.Millisecond)
		if out := buf.String(); !strings.Contains(out, "level=WARN") {
			t.Errorf("outcome %q must log at WARN; got: %s", outcome, out)
		}
	}
}

// TestLogServiceCall_NilLoggerSafe pins nil-safety (a bare builder passes no
// logger in some tests).
func TestLogServiceCall_NilLoggerSafe(t *testing.T) {
	logServiceCall(nil, ServiceKindTitle, "m", ServiceOutcomeOK, 1, time.Millisecond)
}

// --- collector semantics ---

// TestServiceMetrics_NilReceiverSafe pins that a nil collector (bare
// OrchestratorBuilder / Orchestrator test literals) is a valid no-op.
func TestServiceMetrics_NilReceiverSafe(t *testing.T) {
	var m *ServiceMetrics
	m.record(ServiceKindTitle, ServiceOutcomeOK, 1, time.Millisecond) // must not panic
	if got := m.Snapshot(); len(got) != 0 {
		t.Errorf("nil snapshot = %+v, want empty", got)
	}
	m2 := newServiceMetrics()
	if b := (&OrchestratorBuilder{serviceMetrics: m2}); b.ServiceMetricsSnapshot() == nil {
		t.Error("ServiceMetricsSnapshot must return a non-nil map")
	}
	if b := (&OrchestratorBuilder{}); len(b.ServiceMetricsSnapshot()) != 0 {
		t.Error("a builder with no collector must yield an empty snapshot")
	}
}

// TestServiceMetrics_SnapshotIsACopy pins that a caller cannot mutate the
// collector through a snapshot.
func TestServiceMetrics_SnapshotIsACopy(t *testing.T) {
	m := newServiceMetrics()
	m.record(ServiceKindTitle, ServiceOutcomeOK, 1, time.Millisecond)

	snap := m.Snapshot()
	k := snap[ServiceKindTitle]
	k.Calls = 99
	snap[ServiceKindTitle] = k

	if again := m.Snapshot()[ServiceKindTitle].Calls; again != 1 {
		t.Errorf("collector mutated through the snapshot: Calls = %d, want 1", again)
	}
}

// TestServiceMetrics_ConcurrentRecord exercises the collector's lock under the
// race detector.
func TestServiceMetrics_ConcurrentRecord(t *testing.T) {
	m := newServiceMetrics()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.record(ServiceKindTitle, ServiceOutcomeOK, 1, time.Millisecond)
		}()
	}
	wg.Wait()
	if got := m.Snapshot()[ServiceKindTitle].Calls; got != 50 {
		t.Errorf("Calls = %d, want 50", got)
	}
}
