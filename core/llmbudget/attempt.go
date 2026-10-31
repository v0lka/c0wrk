package llmbudget

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// AttemptRecorder carries the wall-clock duration of the SINGLE provider HTTP
// attempt back to the installer (ADR-071 D1): the caller-level timer measures
// the whole router call — failed attempt, backoff, success — which would
// inflate the fitted rates, so the budget Transport, the only seam seeing one
// attempt at a time, records the winning attempt here; the LAST cleanly-
// completed attempt wins (each retry overwrites its predecessor).
// Why: specs/domains/llm-providers.md ("The learning model").
type AttemptRecorder struct {
	mu  sync.Mutex
	d   time.Duration
	set bool
}

// NewAttemptRecorder returns an empty recorder.
func NewAttemptRecorder() *AttemptRecorder { return &AttemptRecorder{} }

// Duration returns the recorded attempt duration and whether one was
// recorded — false when no attempt completed cleanly (caller falls back).
func (r *AttemptRecorder) Duration() (time.Duration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.d, r.set
}

// record stores the duration of a cleanly-completed attempt, overwriting any
// earlier one so the successful retry wins.
func (r *AttemptRecorder) record(d time.Duration) {
	r.mu.Lock()
	r.d = d
	r.set = true
	r.mu.Unlock()
}

// recordSince records the elapsed time since start when the body ended
// cleanly (EOF, or a close with no read error) — the shared completion rule
// for the transport's two body wrappers; a mid-body error records nothing.
func (r *AttemptRecorder) recordSince(err error, start time.Time) {
	if err == nil || errors.Is(err, io.EOF) {
		r.record(time.Since(start))
	}
}

// attemptRecorderCtxKey is the unexported context key under which a caller
// stashes an *AttemptRecorder for the budget transport to fill.
type attemptRecorderCtxKey struct{}

// WithAttemptRecorder returns ctx carrying r for the budget Transport to
// report the winning attempt's duration back; the value rides the ORIGINAL
// request's context, so the armed clone inherits it — no per-attempt plumbing.
func WithAttemptRecorder(ctx context.Context, r *AttemptRecorder) context.Context {
	return context.WithValue(ctx, attemptRecorderCtxKey{}, r)
}

// attemptRecorderFrom returns the recorder carried by ctx, or nil when none was
// installed (the transport then skips recording).
func attemptRecorderFrom(ctx context.Context) *AttemptRecorder {
	r, _ := ctx.Value(attemptRecorderCtxKey{}).(*AttemptRecorder)
	return r
}
