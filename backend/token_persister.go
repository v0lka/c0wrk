package backend

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Coalescing window for session-token persistence. The emitter reports
// cumulative token totals after every LLM call; writing each report straight to
// SQLite would issue one UPDATE per call for a value that only ever needs its
// latest state. Coalescing keeps at most one write per session per interval.
const (
	tokenPersistInterval = time.Second
	tokenPersistTimeout  = 2 * time.Second
)

// tokenStore is the single session-store method the token coalescer needs.
type tokenStore interface {
	UpdateSessionTokens(ctx context.Context, id string, inputTokens, outputTokens int, model, family string, fillPercent float64) error
}

// tokenPersister coalesces the high-frequency session-token updates reported by
// the session emitter into at most one UPDATE per session per flush interval,
// and runs those writes off the reporting goroutine.
//
// The latest report per session always wins, so a burst of updates collapses to
// a single row write; Close flushes the pending map so a task's final totals are
// captured even between ticks. Writes are routed through the shared persistence
// single writer (submit), so they are serialized with event persistence instead
// of contending for SQLite's write lock.
type tokenPersister struct {
	store    tokenStore
	submit   func(func())
	interval time.Duration
	timeout  time.Duration
	logger   *slog.Logger

	mu        sync.Mutex
	pending   map[string]tokenUpdate
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

type tokenUpdate struct {
	inputTokens  int
	outputTokens int
	model        string
	family       string
	fillPercent  float64
}

func newTokenPersister(store tokenStore, submit func(func()), interval, timeout time.Duration, logger *slog.Logger) *tokenPersister {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = tokenPersistInterval
	}
	if timeout <= 0 {
		timeout = tokenPersistTimeout
	}
	t := &tokenPersister{
		store:    store,
		submit:   submit,
		interval: interval,
		timeout:  timeout,
		logger:   logger,
		pending:  make(map[string]tokenUpdate),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go t.run()
	return t
}

// Record stores the latest token totals for a session, replacing any value
// recorded since the last flush. It never touches the database, so it is safe
// to call on the emitter's goroutine.
func (t *tokenPersister) Record(sessionID string, inputTokens, outputTokens int, model, family string, fillPercent float64) {
	if sessionID == "" {
		return
	}
	t.mu.Lock()
	t.pending[sessionID] = tokenUpdate{
		inputTokens:  inputTokens,
		outputTokens: outputTokens,
		model:        model,
		family:       family,
		fillPercent:  fillPercent,
	}
	t.mu.Unlock()
}

// Flush submits the coalesced updates collected so far to the single writer. It
// returns without waiting for them to complete (the shared writer's Flush is the
// barrier used at task end / shutdown).
func (t *tokenPersister) Flush() {
	t.flush()
}

// Close stops the periodic flusher and performs a final flush of any pending
// updates. Idempotent.
func (t *tokenPersister) Close() {
	t.closeOnce.Do(func() {
		close(t.stop)
		<-t.done
		t.flush()
	})
}

func (t *tokenPersister) run() {
	defer close(t.done)
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-ticker.C:
			t.flush()
		}
	}
}

func (t *tokenPersister) flush() {
	t.mu.Lock()
	if len(t.pending) == 0 {
		t.mu.Unlock()
		return
	}
	batch := t.pending
	t.pending = make(map[string]tokenUpdate)
	t.mu.Unlock()

	for id, u := range batch {
		t.submit(func() {
			ctx, cancel := context.WithTimeout(context.Background(), t.timeout)
			defer cancel()
			if err := t.store.UpdateSessionTokens(ctx, id, u.inputTokens, u.outputTokens, u.model, u.family, u.fillPercent); err != nil {
				t.logger.Warn("failed to persist session tokens", "session", id, "error", err)
			}
		})
	}
}
