package desktop

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/session"
)

// batchChanSink is a channel-backed event sink for deterministic batcher tests.
// A channel (unlike a shared slice) lets the tests wait for asynchronous
// flushes without data races.
type batchChanSink struct {
	ch chan emittedEvent
}

func newBatchChanSink() *batchChanSink {
	return &batchChanSink{ch: make(chan emittedEvent, 8192)}
}

func (s *batchChanSink) emit(name string, data ...any) {
	s.ch <- emittedEvent{Name: name, Data: data}
}

// collectFlattened gathers `want` individual events (expanding any batch
// envelopes) from the sink, failing the test on timeout.
func (s *batchChanSink) collectFlattened(t *testing.T, want int) []emittedEvent {
	t.Helper()
	out := make([]emittedEvent, 0, want)
	deadline := time.After(3 * time.Second)
	for len(out) < want {
		select {
		case ev := <-s.ch:
			if ev.Name == eventBatchEventName {
				if len(ev.Data) != 1 {
					t.Fatalf("batch event carries %d args, want 1", len(ev.Data))
				}
				env, ok := ev.Data[0].(batchEnvelope)
				if !ok {
					t.Fatalf("batch payload type = %T, want batchEnvelope", ev.Data[0])
				}
				for _, inner := range env.Events {
					out = append(out, emittedEvent{Name: inner.Name, Data: inner.Args})
				}
				continue
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("timed out: gathered %d of %d events", len(out), want)
		}
	}
	return out
}

// newTestBatcher creates a batcher with the ticker effectively disabled so
// tests observe only explicit/barrier/size flushes.
func newTestBatcher(sink *batchChanSink, maxEvents int) *EventBatcher {
	return newEventBatcherWithOptions(time.Hour, maxEvents, eventBatchMaxBytes, nil, sink.emit)
}

// mapPayload returns the single map[string]any argument of a recorded event,
// failing the test on a shape mismatch.
func mapPayload(t *testing.T, ev emittedEvent) map[string]any {
	t.Helper()
	if len(ev.Data) != 1 {
		t.Fatalf("event %q carries %d args, want 1", ev.Name, len(ev.Data))
	}
	m, ok := ev.Data[0].(map[string]any)
	if !ok {
		t.Fatalf("event %q payload type = %T, want map[string]any", ev.Name, ev.Data[0])
	}
	return m
}

func TestEventBatcher_CoalescesAdjacentTransientEventsLatestWins(t *testing.T) {
	sink := newBatchChanSink()
	b := newTestBatcher(sink, eventBatchMaxEvents)
	defer b.Stop()

	// Three adjacent assistant_chunk events on the same (root) stream collapse
	// to the last one — the accumulated content is a full snapshot, so dropping
	// the intermediates loses no state.
	b.Enqueue("session:s1:assistant_chunk", []any{map[string]any{"accumulated_content": "a"}}, "assistant_chunk", false, 0)
	b.Enqueue("session:s1:assistant_chunk", []any{map[string]any{"accumulated_content": "ab"}}, "assistant_chunk", false, 0)
	b.Enqueue("session:s1:assistant_chunk", []any{map[string]any{"accumulated_content": "abc"}}, "assistant_chunk", false, 0)
	b.Flush()

	got := sink.collectFlattened(t, 1)
	if d := mapPayload(t, got[0]); d["accumulated_content"] != "abc" {
		t.Fatalf("coalesced payload = %+v, want accumulated_content=abc", d)
	}
}

func TestEventBatcher_DistinctStreamsAreNotCoalesced(t *testing.T) {
	sink := newBatchChanSink()
	b := newTestBatcher(sink, eventBatchMaxEvents)
	defer b.Stop()

	// Two different plan-step streams (different coalesce keys) must both
	// survive even though they are adjacent.
	b.Enqueue("session:s1:context_fill", []any{map[string]any{"plan_step_id": "step_1", "fill_percent": 10.0}}, "context_fill|step_1", false, 0)
	b.Enqueue("session:s1:context_fill", []any{map[string]any{"plan_step_id": "step_2", "fill_percent": 20.0}}, "context_fill|step_2", false, 0)
	b.Flush()

	got := sink.collectFlattened(t, 2)
	if mapPayload(t, got[0])["plan_step_id"] != "step_1" {
		t.Fatalf("first event = %+v, want step_1", got[0].Data[0])
	}
	if mapPayload(t, got[1])["plan_step_id"] != "step_2" {
		t.Fatalf("second event = %+v, want step_2", got[1].Data[0])
	}
}

func TestEventBatcher_ContentOrderPreservedAcrossTransientRuns(t *testing.T) {
	sink := newBatchChanSink()
	b := newTestBatcher(sink, eventBatchMaxEvents)
	defer b.Stop()

	b.Enqueue("session:s1:assistant_chunk", []any{map[string]any{"accumulated_content": "A"}}, "assistant_chunk", false, 0)
	b.Enqueue("session:s1:assistant_done", []any{map[string]any{"content": "A"}}, "", false, 0)
	b.Enqueue("session:s1:assistant_chunk", []any{map[string]any{"accumulated_content": "B"}}, "assistant_chunk", false, 0)
	b.Enqueue("session:s1:assistant_done", []any{map[string]any{"content": "B"}}, "", false, 0)
	b.Flush()

	got := sink.collectFlattened(t, 4)
	if mapPayload(t, got[0])["accumulated_content"] != "A" ||
		mapPayload(t, got[2])["accumulated_content"] != "B" {
		t.Fatalf("chunk streams reordered: %+v", got)
	}
	if got[1].Name != "session:s1:assistant_done" || got[3].Name != "session:s1:assistant_done" {
		t.Fatalf("assistant_done events not interleaved in order: %+v", got)
	}
}

func TestEventBatcher_ContentEventsNeverLost(t *testing.T) {
	sink := newBatchChanSink()
	b := newTestBatcher(sink, eventBatchMaxEvents)
	defer b.Stop()

	const n = 200 // below the size threshold → delivered by the explicit flush
	for i := 0; i < n; i++ {
		b.Enqueue("session:s1:thought", []any{map[string]any{"content": i}}, "", false, 0)
	}
	b.Flush()

	got := sink.collectFlattened(t, n)
	for i, ev := range got {
		if content := mapPayload(t, ev)["content"]; content != i {
			t.Fatalf("event %d out of order: content=%v", i, content)
		}
	}
}

func TestEventBatcher_SizeThresholdTriggersFlush(t *testing.T) {
	sink := newBatchChanSink()
	b := newTestBatcher(sink, 4) // flush every 4 enqueued events
	defer b.Stop()

	// No explicit Flush: the size threshold must deliver everything.
	for i := 0; i < 10; i++ {
		b.Enqueue("session:s1:thought", []any{map[string]any{"content": i}}, "", false, 0)
	}
	got := sink.collectFlattened(t, 10)
	if len(got) != 10 {
		t.Fatalf("size-triggered flush delivered %d events, want 10", len(got))
	}
}

func TestEventBatcher_BarrierForcesImmediateFlush(t *testing.T) {
	sink := newBatchChanSink()
	b := newTestBatcher(sink, eventBatchMaxEvents)
	defer b.Stop()

	// A content event alone would sit in the queue until the (disabled) ticker;
	// the barrier settlement event must force the flush on its own.
	b.Enqueue("session:s1:thought", []any{map[string]any{"content": 1}}, "", false, 0)
	b.Enqueue("session:s1:task_complete", []any{map[string]any{"success": true}}, "", true, 0)

	got := sink.collectFlattened(t, 2)
	if got[0].Name != "session:s1:thought" || got[1].Name != "session:s1:task_complete" {
		t.Fatalf("barrier flush order = %+v", got)
	}
}

func TestEventBatcher_FlushOnEmptyIsNoOp(t *testing.T) {
	sink := newBatchChanSink()
	b := newTestBatcher(sink, eventBatchMaxEvents)
	defer b.Stop()

	b.Flush()
	select {
	case ev := <-sink.ch:
		t.Fatalf("empty flush emitted %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestEventBatcher_StreamingBurstCollapsesToOneFlush demonstrates the core
// acceptance property: a run of coalescable streaming events becomes a single
// Wails emission carrying only the final value.
func TestEventBatcher_StreamingBurstCollapsesToOneFlush(t *testing.T) {
	var mu sync.Mutex
	flushes := 0
	b := newEventBatcherWithOptions(time.Hour, eventBatchMaxEvents, eventBatchMaxBytes, nil,
		func(_ string, _ ...any) {
			mu.Lock()
			flushes++
			mu.Unlock()
		})
	defer b.Stop()

	// 70+ chunk events inside one interval — the pathological case the batcher
	// exists to collapse.
	for i := 0; i < 70; i++ {
		b.Enqueue("session:s1:assistant_chunk",
			[]any{map[string]any{"content": "x", "accumulated_content": strings.Repeat("x", i+1)}},
			"assistant_chunk", false, 0)
	}
	b.Flush()

	mu.Lock()
	got := flushes
	mu.Unlock()
	if got != 1 {
		t.Fatalf("70 coalescable events produced %d flushes, want 1", got)
	}
}

// TestEventBatcher_ContentBurstFitsOneFlush shows 70+ content events are
// delivered in a single envelope (one EventsEmit) when below the size threshold.
func TestEventBatcher_ContentBurstFitsOneFlush(t *testing.T) {
	var mu sync.Mutex
	flushes := 0
	b := newEventBatcherWithOptions(time.Hour, eventBatchMaxEvents, eventBatchMaxBytes, nil,
		func(_ string, _ ...any) {
			mu.Lock()
			flushes++
			mu.Unlock()
		})
	defer b.Stop()

	for i := 0; i < 70; i++ {
		b.Enqueue("session:s1:thought", []any{map[string]any{"content": i}}, "", false, 0)
	}
	b.Flush()

	mu.Lock()
	got := flushes
	mu.Unlock()
	if got != 1 {
		t.Fatalf("70 content events produced %d flushes, want 1", got)
	}
}

func TestEventBatcher_StopFlushesPending(t *testing.T) {
	sink := newBatchChanSink()
	b := newTestBatcher(sink, eventBatchMaxEvents)

	b.Enqueue("session:s1:thought", []any{map[string]any{"content": 1}}, "", false, 0)
	b.Stop() // final flush at shutdown

	got := sink.collectFlattened(t, 1)
	if got[0].Name != "session:s1:thought" {
		t.Fatalf("stop flush delivered %+v", got)
	}
}

func TestSessionEventCoalesceKey(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
		data      any
		want      string
	}{
		{"root chunk", "assistant_chunk", map[string]any{"content": "x"}, "assistant_chunk"},
		{"scoped chunk", "assistant_chunk", map[string]any{"plan_step_id": "step_9"}, "assistant_chunk|step_9"},
		{"session tokens", "session_tokens", map[string]any{"model": "m"}, "session_tokens"},
		{"agent metrics", "agent_metrics", map[string]any{"finish": "full"}, "agent_metrics"},
		{"scoped todo", "step_todo_update", map[string]any{"step_id": "step_1"}, "step_todo_update|step_1"},
		{"root todo", "step_todo_update", map[string]any{}, "step_todo_update"},
		{"scoped fill struct", "context_fill", session.ContextFillEventData{PlanStepID: "step_3"}, "context_fill|step_3"},
		{"root fill struct", "context_fill", session.ContextFillEventData{}, "context_fill"},
		{"content event", "assistant_done", map[string]any{"content": "x"}, ""},
		{"terminal output", "terminal_output", map[string]any{"data": "x"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionEventCoalesceKey(tc.eventType, tc.data); got != tc.want {
				t.Fatalf("sessionEventCoalesceKey(%q) = %q, want %q", tc.eventType, got, tc.want)
			}
		})
	}
}

func TestIsImmediateFlushEvent(t *testing.T) {
	for _, typ := range []string{"task_complete", "task_cancelled", "error", "task_failed_resumable", "ask_user", "tool_confirm", "step_limit", "plan_review_ready", "goal_proposal"} {
		if !isImmediateFlushEvent(typ) {
			t.Errorf("isImmediateFlushEvent(%q) = false, want true", typ)
		}
	}
	for _, typ := range []string{"assistant_chunk", "thought", "terminal_output", "session_tokens", ""} {
		if isImmediateFlushEvent(typ) {
			t.Errorf("isImmediateFlushEvent(%q) = true, want false", typ)
		}
	}
}
