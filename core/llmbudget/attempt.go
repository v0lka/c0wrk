package llmbudget

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// AttemptRecorder carries the wall-clock duration of the SINGLE provider HTTP
// attempt that produced a response body back to the caller that installed it
// (ADR-071 D1's "duration around every provider call").
//
// Why a side channel is needed: the router retries a request that died of its
// own armed budget (D9), sleeping an exponential backoff between attempts, and
// the caller-level timer (sp4rk's TrackingCaller) measures the whole router
// call — failed attempt + backoff + successful attempt. Feeding that aggregate
// to the estimator would inflate the fitted r_in/r_out, worst of all in exactly
// the escalation scenario the feature exists for. The budget Transport, which
// wraps each provider's HTTP client, is the only seam that sees one attempt at
// a time; it records the winning attempt's duration here so the ingest can use
// it instead of the router-call aggregate.
//
// The zero value is ready to use; construct one per logical call with
// NewAttemptRecorder. The recorded duration is the LAST cleanly-completed
// attempt: the transport records only on a body that finished (EOF or a close
// with no read error), and each retried attempt overwrites its predecessor, so
// the successful final attempt always wins.
type AttemptRecorder struct {
	mu  sync.Mutex
	d   time.Duration
	set bool
}

// NewAttemptRecorder returns an empty recorder.
func NewAttemptRecorder() *AttemptRecorder { return &AttemptRecorder{} }

// Duration returns the recorded provider-attempt duration and whether any
// attempt recorded one. The second result is false when no attempt completed
// cleanly through the budget transport — no transport sat on the request path,
// or every attempt's body ended in error — leaving the caller to fall back to
// its own measurement.
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

// recordSince records the elapsed time since start when the body ended cleanly
// (EOF, or a close with no read error) — the shared completion rule for the
// transport's two body wrappers. A mid-body error (including the transport's
// own armed-budget expiry) records nothing: the value is consumed only on a
// successful call, and the wrapper deliberately does not report a partial read.
func (r *AttemptRecorder) recordSince(err error, start time.Time) {
	if err == nil || errors.Is(err, io.EOF) {
		r.record(time.Since(start))
	}
}

// attemptRecorderCtxKey is the unexported context key under which a caller
// stashes an *AttemptRecorder for the budget transport to fill.
type attemptRecorderCtxKey struct{}

// WithAttemptRecorder returns ctx carrying r, so the budget Transport on the
// request path can report the provider attempt's duration back to the caller
// that installed it. The value rides the request context, which the transport
// reads from the ORIGINAL request (its cloned, deadline-armed request inherits
// the value), so no per-attempt plumbing is required.
func WithAttemptRecorder(ctx context.Context, r *AttemptRecorder) context.Context {
	return context.WithValue(ctx, attemptRecorderCtxKey{}, r)
}

// attemptRecorderFrom returns the recorder carried by ctx, or nil when none was
// installed (the transport then skips recording).
func attemptRecorderFrom(ctx context.Context) *AttemptRecorder {
	r, _ := ctx.Value(attemptRecorderCtxKey{}).(*AttemptRecorder)
	return r
}
