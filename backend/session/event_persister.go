package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// EventPersister persists chat-visible events to the session store (SQLite);
// the desktop layer emits Wails events separately.
type EventPersister struct {
	store  SessionStore
	mu     sync.RWMutex
	logger *slog.Logger

	// writer is the dedicated single-writer goroutine for store writes. Nil
	// until StartWriter; while nil, writes run inline (see SubmitWrite) —
	// fully synchronous for unit tests and embedders that never opt in.
	writer *singleWriter

	// assistantMu guards lastAssistantContent — per-session last assistant_done
	// content, used to dedup task_complete against the streamed answer so the
	// final answer is not persisted twice on reload.
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

// Persist saves a chat-visible event to the session store; transient events
// are silently skipped and errors are logged, never returned (best-effort).
func (p *EventPersister) Persist(evt Event) {
	if p.store == nil {
		return
	}

	var role, content string

	// Reset per-session assistant tracking on a new user message or session
	// deletion: the task_complete dedup (below) must not false-positive
	// against a prior task's streamed answer, and the map must not grow
	// unboundedly in the long-lived singleton.
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
		// In the implicit text-only finish path the executor streams the answer
		// via assistant_done AND sets it as Output; skip when the output
		// matches the last streamed content, or the row duplicates on reload.
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
		// A task completing with no content still needs a row, or session
		// continuations lose the conversation context.
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
		// Cooperative pause checkpoint: persisted with its metadata so the
		// paused step reappears after a reload.
		role = "plan_step_paused"
	case "retry":
		role = "retry"
	case "subagent_launch":
		role = "subagent_launch"
	case "subagent_complete":
		role = "subagent_complete"
	case "subagent_paused":
		// Cooperative pause checkpoint for a delegated subagent; survives a
		// reload, recoverable via Resume.
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
		// Durable audit row (unlike the transient judge telemetry): see
		// specs/contracts/event-catalog.md.
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
		// Replace semantics bound the checklist to one row per step; see
		// specs/domains/session-lifecycle.md.
		role = "step_todo_update"
	case "session_tokens",
		"assistant_chunk", "context_fill", "finishing",
		"message_received", "blackboard_updated",
		"tool_judge_response", "session_created", "session_deleted",
		"session_renamed",
		"tool_judge_started", "tool_judge_finished",
		"tool_confirm",
		"goal_progress",
		"compaction_started", "compaction_finished",
		"attachments:changed",
		"session_pinned", "session_unpinned",
		"session_archived", "session_unarchived",
		"task_started",
		"work_unit_settled",
		"e2s_state":
		// Transient — each event's durable source is named in its
		// specs/contracts/event-catalog.md row; persisting would add one dead
		// event_unknown card per event on reload.
		return
	case "plan_review_ready":
		role = "plan_review"
	case "goal_status":
		// Full goal-state snapshot so the frontend rebuilds the goal store
		// after a reload; goal_progress stays transient (turn/budget telemetry).
		role = "goal_status"
	case "goal_proposal":
		// Pending action persists so it reappears via GetPendingActions after
		// a reload; see specs/contracts/event-catalog.md.
		role = "goal_proposal"
	case "goal_suggestion":
		// Pending action survives reloads, mirroring goal_proposal.
		role = "goal_suggestion"
	case "context_compaction":
		// AUTO compactions persist a durable row (the card must survive
		// switches/restarts); the MANUAL flow's marker is persisted by the
		// manager instead. See specs/contracts/event-catalog.md and
		// specs/domains/memory/compaction.md.
		role = "context_compaction"
		switch d := evt.Data.(type) {
		case ContextCompactionEventData:
			content = fmt.Sprintf("Context compacted from %.0f%% to %.0f%%", d.BeforePercent, d.AfterPercent)
		case map[string]any:
			bp, _ := d["before_percent"].(float64)
			ap, _ := d["after_percent"].(float64)
			content = fmt.Sprintf("Context compacted from %.0f%% to %.0f%%", bp, ap)
		}
	case "memory_read":
		// Durable row: the card must survive switches/restarts (the history
		// merge keeps only persisted rows). Content carries the summary;
		// metadata keeps the raw payload with the plan-step nesting hint.
		role = "memory_read"
		if d, ok := evt.Data.(map[string]any); ok {
			if c, ok := d["content"].(string); ok {
				content = c
			}
		}
	default:
		// Generic role so unknown types survive reloads; logged so schema
		// drift between emitters and the persister is visible.
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

	// Non-assistant roles fall back to metadata as content; "thought" may be
	// empty (reasoning lives in metadata).
	if content == "" && role != "thought" {
		content = string(metadata)
	}

	// Store writes are offloaded to the single-writer goroutine (see
	// SubmitWrite) so the emitting goroutine never blocks on SQLite; the
	// mapping/dedup decisions above stay synchronous, preserving emit order.
	createdAt := time.Now().UTC().Format(time.RFC3339)
	store := p.store

	if role == "step_todo_update" {
		// Replace semantics: the step_id is recovered from the event data here
		// rather than carried across the switch.
		var stepID string
		if d, ok := evt.Data.(map[string]any); ok {
			if sid, ok := d["step_id"].(string); ok {
				stepID = sid
			}
		}
		p.SubmitWrite(func() {
			// Bounded (restoreDBReadTimeout): an unbounded write here would
			// wedge ALL event persistence behind a saturated pool.
			ctx, cancel := context.WithTimeout(context.Background(), restoreDBReadTimeout)
			defer cancel()
			if err := store.ReplaceStepTodoUpdate(ctx, evt.SessionID, stepID, ChatMessage{
				SessionID: evt.SessionID,
				Role:      role,
				Content:   content,
				Metadata:  metadata,
				CreatedAt: createdAt,
			}); err != nil {
				p.log().Error("failed to persist step_todo_update message", "session", evt.SessionID, "step_id", stepID, "error", err)
			}
		})
		return
	}

	p.SubmitWrite(func() {
		// Bounded like the ReplaceStepTodoUpdate write above — same
		// single-writer wedge hazard.
		ctx, cancel := context.WithTimeout(context.Background(), restoreDBReadTimeout)
		defer cancel()
		if err := store.SaveMessage(ctx, ChatMessage{
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

// queueHighWater is the pending-write depth at which a warning is logged — not
// a hard limit: the queue is unbounded so bursts are never dropped; the warning
// only surfaces a pathological write/consume imbalance.
const queueHighWater = 4096

// singleWriter serializes store writes onto one dedicated goroutine: Submit is
// a non-blocking append, SQLite sees one writer at a time, and no write is
// lost (drained on Flush/Close; a Submit racing shutdown runs inline).
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

// StartWriter launches the single-writer goroutine; afterwards Persist
// schedules store writes and returns immediately. Idempotent.
func (p *EventPersister) StartWriter() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.writer != nil {
		return
	}
	p.writer = newSingleWriter(p.logger)
}

// SubmitWrite schedules op on the single-writer goroutine, or runs it inline
// when no writer is running. Never blocks on SQLite I/O.
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

// Flush blocks until every previously submitted write is processed (no-op
// without a writer); the durability checkpoint for task completion/shutdown.
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
