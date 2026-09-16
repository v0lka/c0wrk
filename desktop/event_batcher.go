package desktop

import (
	"log/slog"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/backend/session"
)

// Batch transport constants. eventBatchEventName MUST match EVENT_BATCH_NAME in
// frontend/src/api/runtime.ts — the frontend dispatcher keys off it.
const (
	// eventBatchEventName is the single Wails event name under which batched
	// events are delivered. Its payload is a batchEnvelope.
	eventBatchEventName = "c0wrk:events:batch"

	// eventBatchInterval bounds the added latency of a queued event (~one
	// frame at 60Hz) and, therefore, the maximum flush rate.
	eventBatchInterval = 16 * time.Millisecond

	// eventBatchMaxEvents / eventBatchMaxBytes force an early flush so a burst
	// (a streaming turn, terminal throughput) cannot grow the queue — and the
	// per-flush payload — without bound.
	eventBatchMaxEvents = 256
	eventBatchMaxBytes  = 256 * 1024

	// eventBatchBaseSize is the nominal byte cost of an event that doesn't
	// supply a size hint.
	eventBatchBaseSize = 512
)

// EventBatcher coalesces and batches frontend events so the Wails/AppKit main
// thread performs ONE evaluateJavaScript call per flush instead of one per
// event. Streaming turns can emit dozens of events per second (assistant_chunk,
// session_tokens, context_fill, ...); dispatching each one individually forces
// a synchronous JS evaluation on the main thread for every event, which is the
// dominant driver of UI jank during long runs.
//
// Two delivery classes share one ordered queue:
//
//   - Transient ("latest-wins") events (see sessionEventCoalesceKey) collapse:
//     two ADJACENT queue entries sharing a coalesce key are merged, keeping the
//     last payload. This is safe because those events carry a full snapshot
//     (e.g. assistant_chunk ships accumulated_content, not a delta) so dropping
//     the intermediates loses no state.
//   - Content events are appended verbatim, preserving order and delivering
//     every event exactly once.
//
// Flushing happens on a ~16ms timer, on a size threshold (event count OR byte
// budget), on demand for "barrier" events (task settlement / HITL prompts, see
// isImmediateFlushEvent), and finally at shutdown. A flush emits a single
// c0wrk:events:batch event whose payload is a batchEnvelope (see
// batchEnvelope); the frontend runtime dispatcher fans it back out to the
// per-event subscribers, so handler semantics are unchanged.
//
// Ordering guarantee: entries are emitted in queue order, and a coalescing
// merge only ever touches the queue TAIL, so the relative order of content and
// transient events is preserved (a chunk can never jump ahead of the
// assistant_done that settled the previous stream).
type EventBatcher struct {
	// emit is the raw transport (App.emit). It is invoked outside the batcher
	// lock so a slow evaluation can never block producers.
	emit func(eventName string, data ...any)

	log *slog.Logger

	interval  time.Duration
	maxEvents int
	maxBytes  int

	mu           sync.Mutex
	queue        []batchedEvent
	pendingBytes int
	stopped      bool

	flushCh chan struct{}
	stopCh  chan struct{}
	done    chan struct{}
	once    sync.Once
}

// batchedEvent is one frontend event inside a batch envelope. coalesceKey is
// bookkeeping only and is never serialized.
type batchedEvent struct {
	Name string `json:"name"`
	Args []any  `json:"args"`

	coalesceKey string
}

// batchEnvelope is the payload of a c0wrk:events:batch emission. The frontend
// `subscribe` dispatcher expects exactly this shape ({events: [{name, args}]}).
type batchEnvelope struct {
	Events []batchedEvent `json:"events"`
}

// newEventBatcher creates and starts a batcher with the production defaults.
func newEventBatcher(log *slog.Logger, emit func(eventName string, data ...any)) *EventBatcher {
	return newEventBatcherWithOptions(eventBatchInterval, eventBatchMaxEvents, eventBatchMaxBytes, log, emit)
}

// newEventBatcherWithOptions builds and starts a batcher with explicit timing
// and size thresholds. Tests use it to disable the ticker (a large interval)
// and exercise coalescing/flush deterministically.
func newEventBatcherWithOptions(interval time.Duration, maxEvents, maxBytes int, log *slog.Logger, emit func(eventName string, data ...any)) *EventBatcher {
	if log == nil {
		log = slog.Default()
	}
	b := &EventBatcher{
		emit:      emit,
		log:       log,
		interval:  interval,
		maxEvents: maxEvents,
		maxBytes:  maxBytes,
		flushCh:   make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
		done:      make(chan struct{}),
	}
	go b.run()
	return b
}

func (b *EventBatcher) run() {
	defer close(b.done)
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	for {
		select {
		case <-b.stopCh:
			b.flush()
			return
		case <-b.flushCh:
			b.flush()
		case <-ticker.C:
			b.flush()
		}
	}
}

// Enqueue adds one event to the batch. coalesceKey, when non-empty, marks the
// event as transient/merged-by-key. barrier forces an immediate flush so
// settlement/HITL events are never delayed. sizeHint bounds the envelope's
// byte budget (0 → a nominal base size); pass the payload size for large
// events such as terminal output.
func (b *EventBatcher) Enqueue(name string, args []any, coalesceKey string, barrier bool, sizeHint int) {
	ev := batchedEvent{Name: name, Args: args, coalesceKey: coalesceKey}
	if sizeHint < eventBatchBaseSize {
		sizeHint = eventBatchBaseSize
	}

	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		// Shutdown already stopped the loop — deliver synchronously so nothing
		// enqueued during teardown is lost.
		b.emitSingle(ev)
		return
	}
	if coalesceKey != "" && len(b.queue) > 0 {
		if last := &b.queue[len(b.queue)-1]; last.coalesceKey == coalesceKey {
			last.Args = args
			b.mu.Unlock()
			if barrier {
				b.requestFlush()
			}
			return
		}
	}
	b.queue = append(b.queue, ev)
	b.pendingBytes += sizeHint
	queued := len(b.queue)
	overflow := queued >= b.maxEvents || b.pendingBytes >= b.maxBytes
	b.mu.Unlock()

	if overflow {
		b.log.Debug("desktop: event batch hit size threshold", "queued", queued)
	}
	if barrier || overflow {
		b.requestFlush()
	}
}

// flush drains the queue and emits it as one envelope. A no-op when empty.
func (b *EventBatcher) flush() {
	b.mu.Lock()
	if len(b.queue) == 0 {
		b.mu.Unlock()
		return
	}
	batch := b.queue
	b.queue = nil
	b.pendingBytes = 0
	b.mu.Unlock()

	b.emit(eventBatchEventName, batchEnvelope{Events: batch})
}

// emitSingle delivers a lone event as an envelope of one. Used only after Stop
// so late producers still reach the frontend through the same envelope channel.
func (b *EventBatcher) emitSingle(ev batchedEvent) {
	b.emit(eventBatchEventName, batchEnvelope{Events: []batchedEvent{ev}})
}

// Flush forces an immediate synchronous flush of everything currently queued.
// Safe to call from any goroutine.
func (b *EventBatcher) Flush() {
	b.flush()
}

// Stop flushes any pending events and terminates the background loop exactly
// once. Events enqueued afterwards are delivered synchronously.
func (b *EventBatcher) Stop() {
	b.once.Do(func() {
		b.mu.Lock()
		b.stopped = true
		b.mu.Unlock()
		b.flush()
		close(b.stopCh)
		<-b.done
	})
}

func (b *EventBatcher) requestFlush() {
	select {
	case b.flushCh <- struct{}{}:
	default: // a flush is already pending; it will drain everything queued.
	}
}

// --- event classification -------------------------------------------------

// sessionEventCoalesceKey returns the latest-wins coalescing key for a session
// event, or "" when the event is content (must be delivered verbatim, in
// order). Transient events collapse by session+type, refined by their stream
// discriminator where one exists (plan_step_id / step_id) so two concurrent
// streams of the same type never clobber each other.
func sessionEventCoalesceKey(eventType string, data any) string {
	switch eventType {
	case "assistant_chunk", "context_fill", "step_todo_update", "session_tokens", "agent_metrics":
	default:
		return ""
	}
	return eventType + eventStreamDiscriminator(eventType, data)
}

// eventStreamDiscriminator extracts the per-stream id that separates otherwise
// identical transient events (scoped plan-step/subagent emissions). Empty when
// the event is session-root.
func eventStreamDiscriminator(eventType string, data any) string {
	switch d := data.(type) {
	case map[string]any:
		switch eventType {
		case "assistant_chunk", "context_fill":
			if id, _ := d["plan_step_id"].(string); id != "" {
				return "|" + id
			}
		case "step_todo_update":
			if id, _ := d["step_id"].(string); id != "" {
				return "|" + id
			}
		}
	case session.ContextFillEventData:
		if d.PlanStepID != "" {
			return "|" + d.PlanStepID
		}
	case *session.ContextFillEventData:
		if d != nil && d.PlanStepID != "" {
			return "|" + d.PlanStepID
		}
	}
	return ""
}

// isImmediateFlushEvent reports whether a session event must be flushed right
// away rather than waiting for the ~16ms tick: task settlement (so the UI
// leaves the running state without a perceptible delay) and the HITL prompts
// whose handlers block a live task until the user answers.
func isImmediateFlushEvent(eventType string) bool {
	switch eventType {
	case "task_complete", "task_cancelled", "error", "task_failed_resumable",
		"ask_user", "tool_confirm", "step_limit", "plan_review_ready", "goal_proposal":
		return true
	default:
		return false
	}
}

// --- App integration ------------------------------------------------------

// sessionEventBatcher returns the app's lazily-created event batcher, creating
// and starting it on first use. Lazy construction keeps zero-value App
// fixtures (tests) working without an explicit startup hook.
func (a *App) sessionEventBatcher() *EventBatcher {
	a.batcherOnce.Do(func() {
		a.batcherPtr.Store(newEventBatcher(a.log(), a.emit))
	})
	return a.batcherPtr.Load()
}

// emitBatchedEvent enqueues one frontend event through the batcher. sizeHint
// bounds the envelope's byte budget; pass the payload size for large events
// (terminal output) and 0 otherwise.
func (a *App) emitBatchedEvent(name string, args []any, coalesceKey string, barrier bool, sizeHint int) {
	a.sessionEventBatcher().Enqueue(name, args, coalesceKey, barrier, sizeHint)
}

// flushEvents synchronously delivers everything currently queued. Used at
// task-completion boundaries and by tests.
func (a *App) flushEvents() {
	if b := a.batcherPtr.Load(); b != nil {
		b.Flush()
	}
}

// stopEventBatcher flushes pending events and terminates the batcher. Called
// from Shutdown so no queued event is lost on quit; events emitted afterwards
// are delivered synchronously.
func (a *App) stopEventBatcher() {
	if b := a.batcherPtr.Load(); b != nil {
		b.Stop()
	}
}
