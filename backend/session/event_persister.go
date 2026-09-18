package session

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

// EventPersister persists chat-visible events to the session store (SQLite).
// It is decoupled from the UI — the desktop layer is responsible for emitting
// Wails events separately.
type EventPersister struct {
	store  SessionStore
	mu     sync.RWMutex
	logger *slog.Logger

	// writer is the dedicated single-writer goroutine that performs store
	// writes off the caller's goroutine. Nil until StartWriter is called; while
	// nil, writes run inline (see SubmitWrite), which keeps the persister fully
	// synchronous for unit tests and any embedder that does not opt into async
	// persistence. Guarded by mu.
	writer *singleWriter

	// assistantMu guards lastAssistantContent: per-session tracking of the most
	// recent assistant_done content. Used to dedup task_complete against the
	// streamed answer in the implicit text-only finish path (where the executor
	// emits assistant_done AND sets Output), so the final answer is not
	// persisted — and therefore not rendered — twice on session reload.
	assistantMu          sync.Mutex
	lastAssistantContent map[string]string
}

// NewEventPersister creates a new EventPersister backed by the given store.
// If store is nil, Persist is a no-op.
func NewEventPersister(store SessionStore) *EventPersister {
	return &EventPersister{store: store, lastAssistantContent: make(map[string]string)}
}

// log returns the persister's logger, falling back to slog.Default().
func (p *EventPersister) log() *slog.Logger {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.logger != nil {
		return p.logger
	}
	return slog.Default()
}

// SetLogger sets the logger for the event persister.
func (p *EventPersister) SetLogger(l *slog.Logger) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.logger = l
}

// Persist saves a chat-visible event to the session store.
// Transient events (session_tokens, etc.) are silently skipped.
// Errors are logged but never returned — persistence is best-effort.
func (p *EventPersister) Persist(evt Event) {
	if p.store == nil {
		return
	}

	var role, content string
	var (
		isStepTodoUpdate bool
		stepTodoStepID   string
	)

	// Reset per-session assistant tracking on a new user message or session
	// deletion so the task_complete dedup (below) is scoped to the current
	// task only and cannot false-positive against a prior task's streamed
	// answer. session_deleted also prevents unbounded growth of the
	// lastAssistantContent map in the long-lived persister singleton.
	if evt.Type == "message_received" || evt.Type == "session_deleted" {
		p.assistantMu.Lock()
		delete(p.lastAssistantContent, evt.SessionID)
		p.assistantMu.Unlock()
	}

	switch evt.Type {
	case "routing":
		role = "routing"
	case "tool_call":
		role = "tool_call"
	case "tool_result":
		role = "tool_result"
	case "evaluation":
		role = "eval"
	case "reflection":
		role = "reflection"
	case "plan_generated":
		role = "plan"
	case "error":
		role = "error"
	case "assistant_done":
		role = "assistant"
		switch d := evt.Data.(type) {
		case AssistantDoneEventData:
			content = d.Content
		case map[string]any:
			if c, ok := d["content"].(string); ok {
				content = c
			}
		}
		// Track the streamed content so task_complete can dedup against it.
		p.assistantMu.Lock()
		p.lastAssistantContent[evt.SessionID] = content
		p.assistantMu.Unlock()
	case "task_complete":
		var output string
		switch d := evt.Data.(type) {
		case TaskCompleteData:
			output = d.Output
		case map[string]any:
			if o, ok := d["output"].(string); ok {
				output = o
			} else {
				return // no output field — nothing to persist
			}
		}
		// Dedup: in the implicit text-only finish path the executor streams
		// the answer via assistant_done (persisted above) AND sets it as
		// Output, so task_complete would otherwise persist a duplicate
		// assistant row — rendering the final answer twice on reload. Skip
		// when the output matches the last streamed assistant content.
		if output != "" {
			p.assistantMu.Lock()
			last := p.lastAssistantContent[evt.SessionID]
			p.assistantMu.Unlock()
			if output == last {
				return
			}
		}
		role = "assistant"
		content = output
		// Guard against empty output: a task that completes with
		// no content must still persist a message so that session
		// continuations can see the full conversation history.
		// Without this, an empty-output completion is silently
		// dropped from the message store, breaking continuation
		// context.
		if content == "" {
			content = "[Task completed]"
		}
	case "thought":
		role = "thought"
		switch d := evt.Data.(type) {
		case ThoughtEventData:
			content = d.Content
		case map[string]any:
			if c, ok := d["content"].(string); ok {
				content = c
			}
		}
	case "step_start":
		role = "thinking"
	case "step_complete":
		role = "step_done"
	case "plan_step_start":
		role = "plan_step_start"
	case "plan_step_complete":
		role = "plan_step_complete"
	case "plan_step_paused":
		// Cooperative pause checkpoint: persisted with its full metadata
		// (step_id, duration, optional error) so the paused step reappears
		// after a session reload and the Resume flow has a durable record.
		role = "plan_step_paused"
	case "retry":
		role = "retry"
	case "subagent_launch":
		role = "subagent_launch"
	case "subagent_complete":
		role = "subagent_complete"
	case "subagent_paused":
		// Cooperative pause checkpoint for a delegated subagent: persisted
		// with its metadata (step_id, duration) so the pause survives a
		// reload; the delegation is recoverable via Resume, not lost.
		role = "subagent_paused"
	case "task_failed_resumable":
		role = "task_failed_resumable"
	case "task_resumed":
		role = "task_resumed"
	case "ask_user":
		role = "ask_user"
	case "step_limit":
		role = "step_limit"
	case EventAutonomyDecision:
		// Automatic (no-human) security decisions are persisted so the audit
		// trail survives a reload — the trajectory must stay reconstructable
		// (OWASP ASI10). Unlike tool_judge_started/finished (transient activity
		// telemetry), this row is durable: it records WHAT was auto-decided.
		role = EventAutonomyDecision
	case "task_cancelled":
		role = "task_cancelled"
	case "step_retry":
		role = "step_retry"
	case "service":
		// Only persist orchestration phase service events.
		if data, ok := evt.Data.(map[string]any); ok {
			if phase, _ := data["phase"].(string); phase == "orchestration" {
				role = "status"
			}
		}
	case "skills_activated":
		role = "status"
	case "agent_metrics":
		role = "status"
	case "step_todo_update":
		role = "step_todo_update"
		isStepTodoUpdate = true
		if d, ok := evt.Data.(map[string]any); ok {
			if sid, ok := d["step_id"].(string); ok {
				stepTodoStepID = sid
			}
		}
	case "session_tokens",
		"assistant_chunk", "context_fill", "context_compaction", "finishing",
		"memory_read", "message_received", "blackboard_updated",
		"tool_judge_response", "session_created", "session_deleted",
		"session_renamed",
		// Strict-judge (Smart Approve) phase telemetry: transient activity
		// labels for a judge run that predates any confirmation card —
		// persisting them would replay stale "judge working" rows on reload.
		"tool_judge_started", "tool_judge_finished",
		// Tool confirmations are transient by design: the pending-confirm
		// channel map is process-local, so a persisted row could never be
		// resolved after a restart and would render as a dead card on reload.
		"tool_confirm",
		"goal_progress",
		// Manual-compaction lifecycle: the marker row (with the compacted
		// history snapshot) is persisted directly by the manager's flow
		// (persistCompactionMarker), not via the event pipeline. Persisting
		// these transient events would duplicate the marker card on reload.
		"compaction_started", "compaction_finished",
		// UI-only state events emitted with a SessionID. These drive live UI
		// updates (attachment chips, sidebar pin/archive toggles) but carry no
		// conversational content — persisting them would store the raw JSON
		// payload as an event_unknown message row (content = metadata) that
		// renders as garbage JSON text on session reload.
		"attachments:changed",
		"session_pinned", "session_unpinned",
		"session_archived", "session_unarchived",
		// Work-unit settlement is transient: the durable source is the unit
		// ledger surfaced via GetSessionRuntimeStatus.work_units, so persisting
		// this event would only add a dead row on reload.
		"work_unit_settled",
		// E2S execution-state Σ snapshots are live-only UI events: the
		// frontend Execution State panel renders them in real time and has no
		// persisted restore, so a row would store the full Σ JSON as an
		// event_unknown message (content = metadata) that renders as garbage
		// on reload — one dead row per E2S turn.
		"e2s_state":
		return // transient — no persistence needed
	case "plan_review_ready":
		role = "plan_review"
	case "goal_status":
		// Persist the full goal state snapshot so the frontend can rebuild the
		// goal store (status-bar badge + settled goal card verdict) and re-render
		// the turn-transition notice after a session reload. goal_progress stays
		// transient — the snapshot already carries turn/budget telemetry.
		role = "goal_status"
	case "goal_proposal":
		// Persist the goal-proposal pending action so it reappears via
		// GetPendingActions after a reload (the agent remains blocked until
		// the user confirms/cancels via the goal_proposal_response flow).
		role = "goal_proposal"
	default:
		// Unknown event types are persisted with a generic role so they survive
		// session reloads. The frontend can still show them (or ignore them).
		// Log so schema drift between emitters and the persister is visible.
		p.log().Warn("persisting unknown event type with generic role", "type", evt.Type, "session", evt.SessionID)
		role = "event_unknown"
	}

	if role == "" {
		return
	}

	// Serialize event data as metadata JSON.
	var metadata json.RawMessage
	if evt.Data != nil {
		if b, err := json.Marshal(evt.Data); err == nil {
			metadata = b
		} else {
			metadata = json.RawMessage("{}")
		}
	} else {
		metadata = json.RawMessage("{}")
	}

	// For non-assistant roles, use metadata as content if content is empty.
	// Exception: for "thought" events, empty content is valid (reasoning lives in metadata).
	if content == "" && role != "thought" {
		content = string(metadata)
	}

	// The store writes below are offloaded to the dedicated single-writer
	// goroutine (when one is running, see SubmitWrite) so the goroutine emitting
	// the UI event never blocks on SQLite. Every mapping/dedup decision above
	// still runs synchronously on the caller, preserving the exact emit order.
	createdAt := time.Now().UTC().Format(time.RFC3339)
	store := p.store

	if isStepTodoUpdate {
		p.SubmitWrite(func() {
			if err := store.UpsertStepTodoUpdate(context.Background(), evt.SessionID, stepTodoStepID, ChatMessage{
				SessionID: evt.SessionID,
				Role:      role,
				Content:   content,
				Metadata:  metadata,
				CreatedAt: createdAt,
			}); err != nil {
				p.log().Error("failed to persist step_todo_update message", "session", evt.SessionID, "step_id", stepTodoStepID, "error", err)
			}
		})
		return
	}

	p.SubmitWrite(func() {
		if err := store.SaveMessage(context.Background(), ChatMessage{
			SessionID: evt.SessionID,
			Role:      role,
			Content:   content,
			Metadata:  metadata,
			CreatedAt: createdAt,
		}); err != nil {
			p.log().Error("failed to persist event message", "type", evt.Type, "session", evt.SessionID, "error", err)
		}
	})
}

// queueHighWater is the pending-write depth at which a warning is logged. It is
// not a hard limit: the queue is unbounded so that a burst of events (e.g. many
// subagents emitting concurrently) is never dropped and the emit path is never
// blocked — the warning only surfaces a pathological write/consume imbalance.
const queueHighWater = 4096

// singleWriter serializes store writes onto one dedicated goroutine. It makes
// the caller's Submit non-blocking (a mutex-guarded append) so the event-emit
// path never waits on SQLite, while guaranteeing that SQLite only ever sees one
// writer at a time and that no write is lost: the queue is drained on Flush and
// on Close, and a Submit that races shutdown is run inline rather than dropped.
type singleWriter struct {
	mu              sync.Mutex
	cond            *sync.Cond
	queue           []func()
	closed          bool
	highWaterLogged bool
	logger          *slog.Logger
	wg              sync.WaitGroup
}

func newSingleWriter(logger *slog.Logger) *singleWriter {
	if logger == nil {
		logger = slog.Default()
	}
	w := &singleWriter{logger: logger}
	w.cond = sync.NewCond(&w.mu)
	w.wg.Add(1)
	go w.run()
	return w
}

// Submit enqueues op without blocking on I/O. Only a brief mutex hold precedes
// the append, so the caller is never delayed by SQLite.
func (w *singleWriter) Submit(op func()) {
	if op == nil {
		return
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		// Writer is shutting down: run inline so a late write is not lost.
		op()
		return
	}
	w.queue = append(w.queue, op)
	if len(w.queue) >= queueHighWater && !w.highWaterLogged {
		w.highWaterLogged = true
		w.logger.Warn("persistence write queue depth is high; writes are outpacing SQLite", "depth", len(w.queue))
	}
	w.mu.Unlock()
	w.cond.Signal()
}

// flush blocks until every op submitted before this call has been processed.
func (w *singleWriter) flush() {
	done := make(chan struct{})
	w.Submit(func() { close(done) })
	<-done
}

// close drains remaining ops and stops the worker. Idempotent.
func (w *singleWriter) close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()
	w.cond.Signal()
	w.wg.Wait()
}

func (w *singleWriter) run() {
	defer w.wg.Done()
	for {
		w.mu.Lock()
		for len(w.queue) == 0 && !w.closed {
			w.cond.Wait()
		}
		if len(w.queue) == 0 { // closed and fully drained
			w.mu.Unlock()
			return
		}
		op := w.queue[0]
		w.queue[0] = nil
		w.queue = w.queue[1:]
		if len(w.queue) == 0 {
			w.queue = nil // release the backing array
			w.highWaterLogged = false
		}
		w.mu.Unlock()
		w.runOp(op)
	}
}

// runOp executes a queued write, isolating a panic so a single bad write can
// never kill the writer goroutine (which would otherwise wedge Flush/Close).
func (w *singleWriter) runOp(op func()) {
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error("recovered from panic while persisting write", "panic", r)
		}
	}()
	op()
}

// StartWriter launches the dedicated single-writer goroutine. After this call,
// Persist schedules its store writes on that goroutine and returns immediately,
// so the goroutine emitting the UI event never blocks on SQLite. Idempotent.
func (p *EventPersister) StartWriter() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.writer != nil {
		return
	}
	p.writer = newSingleWriter(p.logger)
}

// SubmitWrite schedules op on the single-writer goroutine, or runs it inline
// when no writer is running (unit tests / non-desktop embedders). It never
// blocks on SQLite I/O.
func (p *EventPersister) SubmitWrite(op func()) {
	p.mu.RLock()
	w := p.writer
	p.mu.RUnlock()
	if w == nil {
		op()
		return
	}
	w.Submit(op)
}

// Flush blocks until every write submitted before this call has been processed
// (a no-op when no writer is running). It is the durability checkpoint used at
// task completion and during shutdown.
func (p *EventPersister) Flush() {
	p.mu.RLock()
	w := p.writer
	p.mu.RUnlock()
	if w != nil {
		w.flush()
	}
}

// Close drains any queued writes and stops the single-writer goroutine.
// Idempotent; after Close, Persist falls back to inline writes.
func (p *EventPersister) Close() {
	p.mu.Lock()
	w := p.writer
	p.writer = nil
	p.mu.Unlock()
	if w != nil {
		w.close()
	}
}
