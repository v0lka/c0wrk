package desktop

import (
	"log/slog"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/backend/session"
)

// Batch transport constants; delivery rules in specs/contracts/event-catalog.md
// (Event Batching). eventBatchEventName MUST match EVENT_BATCH_NAME in
// frontend/src/api/runtime.ts — the frontend dispatcher keys off it.
const (
	eventBatchEventName = "c0wrk:events:batch" // single delivery event name; payload is a batchEnvelope

	eventBatchInterval = 16 * time.Millisecond // added latency (~one 60Hz frame); also the max flush rate

	// force an early flush so a burst (streaming turn, terminal throughput)
	// cannot grow the queue — or the per-flush payload — without bound
	eventBatchMaxEvents = 256
	eventBatchMaxBytes  = 256 * 1024

	eventBatchBaseSize = 512 // nominal byte cost of an event without a size hint
)

// EventBatcher coalesces and batches frontend events so the main thread
// performs ONE evaluateJavaScript call per flush instead of one per event —
// per-event dispatch being the dominant driver of streaming jank. Two
// delivery classes share one ordered queue: transient latest-wins events (see
// sessionEventCoalesceKey) collapse adjacent queue entries sharing a coalesce
// key (safe: they carry full snapshots, not deltas), content events queue
// verbatim. Flushes run on the ~16ms timer, on a size threshold, on barrier
// events (isImmediateFlushEvent), and at shutdown; the envelope is fanned back
// out by the frontend dispatcher with unchanged handler semantics. A merge
// only ever touches the queue TAIL, so relative order is preserved (a chunk
// can never jump ahead of the assistant_done that settled the stream).
// See specs/contracts/event-catalog.md (Event Batching).
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
		// The name must match too: the merge overwrites Args but keeps the
		// queued Name, so a cross-name merge would deliver one payload under
		// the other's name.
		if last := &b.queue[len(b.queue)-1]; last.coalesceKey == coalesceKey && last.Name == name {
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

// flush drains the queue and emits it as one envelope.
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

// Stop flushes pending events and terminates the background loop; events
// enqueued afterwards are delivered synchronously.
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
// event, or "" for content (delivered verbatim, in order). The queue is shared
// by every session, so the key is scoped by session ID first, refined by the
// stream discriminator (plan_step_id / step_id) — without it, two concurrent
// streams would merge into each other's queue entries and deliver one
// session's payload under the other's event name.
// See specs/contracts/event-catalog.md (Event Batching).
func sessionEventCoalesceKey(sessionID, eventType string, data any) string {
	switch eventType {
	case "assistant_chunk", "context_fill", "step_todo_update", "session_tokens", "agent_metrics":
	default:
		return ""
	}
	if sessionID == "" {
		// Defensive: an event without a session has no business coalescing
		// with anything; deliver it verbatim.
		return ""
	}
	return sessionID + "|" + eventType + eventStreamDiscriminator(eventType, data)
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

// isImmediateFlushEvent reports whether a session event must flush right away
// instead of waiting for the ~16ms tick: task settlement (the UI must leave
// the running state without perceptible delay) and the HITL prompts whose
// handlers block a live task. hitl_resolved rides the same class — it settles
// an open prompt on every surface, and a confirmation card lagging one tick
// behind the task it unblocked reads as a ghost.
func isImmediateFlushEvent(eventType string) bool {
	switch eventType {
	case "task_complete", "task_cancelled", "error", "task_failed_resumable",
		"ask_user", "tool_confirm", "step_limit", "plan_review_ready", "goal_proposal",
		"hitl_resolved":
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

// emitBatchedEvent enqueues one frontend event through the batcher (see Enqueue).
func (a *App) emitBatchedEvent(name string, args []any, coalesceKey string, barrier bool, sizeHint int) {
	a.sessionEventBatcher().Enqueue(name, args, coalesceKey, barrier, sizeHint)
}

// flushEvents synchronously delivers everything currently queued. It is used
// by tests to observe queued events deterministically; task-completion
// boundaries are handled in production by the batcher's barrier flag (see
// isImmediateFlushEvent), not by calling this.
func (a *App) flushEvents() {
	if b := a.batcherPtr.Load(); b != nil {
		b.Flush()
	}
}

// stopEventBatcher flushes pending events and terminates the batcher on
// shutdown; events emitted afterwards are delivered synchronously.
func (a *App) stopEventBatcher() {
	if b := a.batcherPtr.Load(); b != nil {
		b.Stop()
	}
}
