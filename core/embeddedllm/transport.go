package embeddedllm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// This file makes the embedded model's first request transparent: the router
// dials the generated provider entry (http://127.0.0.1:<port>/v1) like any
// OpenAI-compatible endpoint, and — with the idle unload armed — that endpoint
// is usually NOT listening. EnsureLoadedTransport is installed on that one
// provider entry (through llm.ProviderEntry.HTTPClient, the same hook
// core/llmtls uses) and makes the model resident before the request is sent,
// then restarts the idle budget when the response completes.
//
// Three properties are load-bearing: the wait is BOUNDED per waiter and never
// cancels the load (an expired wait leaves the in-flight load running detached
// under the supervisor's own ready budget, so no request ever pays a second
// cold start); concurrent first requests COALESCE onto one load; and the load
// is charged to NO deadline — the request budget is armed only after the model
// can answer, and under the adaptive budget wiring (ADR-071) the fixed arming
// is suppressed in favour of the llmbudget transport beneath this gate.
// See specs/domains/embedded-llm.md (Key Files; Invariants).

// Loader is the supervisor seam the ensure-loaded transport drives; *Server
// satisfies it as-is (see specs/domains/embedded-llm.md, Key Files).
type Loader interface {
	// Load makes the model resident and blocks until it can answer. A Load
	// against an already-serving model is a no-op that only marks activity.
	Load(ctx context.Context) error
	// MarkActivity restarts the auto-unload budget from now.
	MarkActivity()
}

// Production hands this transport backend.embeddedLoaderRef, a lazy reference
// resolving to the supervisor at call time. That indirection is why these
// assertions alone are NOT sufficient: a capability reachable only through a
// type assertion is invisible to the compiler at the wiring site, so the
// package pins the interfaces on *Server AND the backend pins them on
// embeddedLoaderRef — two halves of one contract; neither may be removed.
var _ Loader = (*Server)(nil)

// PortSource is an OPTIONAL Loader capability: the loopback port the supervisor
// actually bound. The request URL is built from the PERSISTED port, a
// preference the load re-checks and may move (see port.go); without this seam
// the triggering request would be sent to whatever unrelated local process
// holds the old port, leaking the prompt. Optional, not grown into Loader, so
// doubles that do not report a port simply get no redirect.
type PortSource interface {
	// Port is the live loopback port, or 0 when no server has been started.
	Port() int
}

var _ PortSource = (*Server)(nil)

// RequestTracker is an OPTIONAL Loader capability: the count of requests the
// transport currently has open against the model. Activity is stamped on
// COMPLETION (Loader.MarkActivity), which cannot protect a generation that
// outlives the whole idle budget; bracketing the exchange with a count lets
// the idle path defer that unload instead. Optional, so doubles that do not
// track requests simply get the completion stamp alone.
type RequestTracker interface {
	// BeginRequest records that a request is now in flight.
	BeginRequest()
	// EndRequest releases one in-flight request.
	EndRequest()
}

var _ RequestTracker = (*Server)(nil)

// DefaultLoadWaitTimeout bounds one request's wait for a cold model. It
// deliberately exceeds DefaultReadyTimeout so a wedged load is always
// diagnosed by Load (which carries the server's log tail), never cut short
// here; the slack covers the rest of Load — port re-check, spawn, final probe.
const DefaultLoadWaitTimeout = DefaultReadyTimeout + 2*time.Minute

// ErrLoadWaitTimeout reports that THIS request's wait budget expired — distinct
// from ErrLoadTimeout, the supervisor's ready budget. The load is NOT
// cancelled: the budget is armed per waiter, so the in-flight load runs on
// detached and a following request joins it or finds the model resident. It
// means exactly one of "still legitimately loading" or "the ready budget is
// misconfigured above the wait budget" — retry, not breakage, is the recovery.
var ErrLoadWaitTimeout = errors.New("the embedded LLM did not become resident within the load wait budget")

// EnsureLoadedTransport is an http.RoundTripper that makes the embedded model
// resident before every request it carries, aims it at the live port, and
// marks activity when the response COMPLETES. Build one with
// NewEnsureLoadedTransport; attach it to a provider entry with
// EnsureLoadedClient. It is safe for concurrent use.
type EnsureLoadedTransport struct {
	base        http.RoundTripper
	loader      Loader
	waitTimeout time.Duration
	// requestTimeout, when > 0, bounds the request itself and is armed only
	// after the model is resident; it replaces http.Client.Timeout, which
	// cannot distinguish the wait from the exchange.
	requestTimeout time.Duration
	logger         *slog.Logger

	// mu guards inflight only — never held across a Load.
	mu       sync.Mutex
	inflight *loadCall

	// waiters counts requests blocked on a load — the coalescing diagnostic
	// (how many requests one cold load served) and a test join-barrier.
	waiters atomic.Int64
}

// loadCall is one coalesced load: N parallel first requests must produce ONE
// weight load.
type loadCall struct {
	// done is closed after err has been written, so a waiter that observes
	// the close may read err without further synchronisation.
	done chan struct{}
	err  error
}

// NewEnsureLoadedTransport wraps base (nil → http.DefaultTransport) so it
// loads the model before sending and marks activity on completion. No request
// budget of its own — production goes through EnsureLoadedClient. loader nil
// yields a pass-through transport (the request path is the wrong place to fail
// on a wiring mistake); waitTimeout <= 0 → DefaultLoadWaitTimeout.
func NewEnsureLoadedTransport(base http.RoundTripper, loader Loader, waitTimeout time.Duration, logger *slog.Logger) *EnsureLoadedTransport {
	return newEnsureLoadedTransport(base, loader, waitTimeout, 0, logger)
}

// newEnsureLoadedTransport is NewEnsureLoadedTransport plus the post-gate
// request budget. requestTimeout <= 0 leaves the request on its own context.
func newEnsureLoadedTransport(base http.RoundTripper, loader Loader, waitTimeout, requestTimeout time.Duration, logger *slog.Logger) *EnsureLoadedTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	if waitTimeout <= 0 {
		waitTimeout = DefaultLoadWaitTimeout
	}
	return &EnsureLoadedTransport{
		base:           base,
		loader:         loader,
		waitTimeout:    waitTimeout,
		requestTimeout: requestTimeout,
		logger:         logger,
	}
}

// RoundTrip implements http.RoundTripper: ensure the model is resident, aim
// the request at the port it is actually serving on, hand it to the wrapped
// transport, and arrange for the in-flight count, the activity stamp and the
// request deadline to be released when the response FINISHES rather than when
// its headers arrive.
//
// A load failure is returned instead of sending the request — dialing a socket
// nothing is listening on would only add a confusing "connection refused" —
// and the redirect is load-bearing for the same reason: a load that MOVED the
// port leaves the original URL pointing at a socket this process does not own.
func (t *EnsureLoadedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("embeddedllm: nil request")
	}
	if t.loader != nil {
		if err := t.ensureLoaded(req.Context()); err != nil {
			return nil, err
		}
		// Aim the request at the live port: the load may have moved the
		// server off the port this URL was built from.
		req = redirectToLivePort(req, t.loader, t.logger)
	}

	// The request budget is armed HERE, not by http.Client.Timeout: the load
	// above may have taken minutes, and charging them to the exchange would
	// leave the generation with whatever is left. The caller's own
	// cancellation still applies — the derived context inherits it.
	var release func()
	if t.requestTimeout > 0 {
		var ctx context.Context
		ctx, release = context.WithTimeout(req.Context(), t.requestTimeout)
		req = req.WithContext(ctx)
	}

	// The in-flight count is the other half of "a long generation is
	// activity": the mark below fires only when the response COMPLETES, so a
	// request that outlives the idle budget is bracketed for the supervisor
	// to defer against. See RequestTracker.
	var begin, end func()
	if tracker, ok := t.loader.(RequestTracker); ok {
		begin, end = tracker.BeginRequest, tracker.EndRequest
	}
	if begin != nil {
		begin()
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		if end != nil {
			end()
		}
		if release != nil {
			release()
		}
		return nil, err
	}
	if resp == nil {
		// A RoundTripper must not return (nil, nil); a base that does would
		// panic on the body access below and strand the deadline's timer.
		if end != nil {
			end()
		}
		if release != nil {
			release()
		}
		return nil, errors.New("embeddedllm: the wrapped transport returned no response")
	}

	// Marking on COMPLETION rather than on the response headers is the point:
	// a streamed generation may run for minutes. The deadline's release rides
	// along: the derived context must outlive RoundTrip, because the body is
	// read long after the headers arrive.
	var mark func()
	if t.loader != nil {
		mark = t.loader.MarkActivity
	}
	if resp.Body != nil && (mark != nil || end != nil || release != nil) {
		resp.Body = &activityBody{ReadCloser: resp.Body, mark: mark, end: end, release: release}
		return resp, nil
	}
	// A RoundTripper must return a body; a nil one ends this request now —
	// release the count and the deadline instead of leaking them.
	if end != nil {
		end()
	}
	if release != nil {
		release()
	}
	return resp, nil
}

// redirectToLivePort returns req aimed at the port the supervisor actually
// bound, or req unchanged when the loader reports no port or the request is
// already aimed there. The rewrite happens on a clone (a RoundTripper must not
// modify the request it was handed; the body is shared, nothing has read it
// yet), and the explicit Host field is rewritten alongside the URL so the
// header the server sees matches the socket it is sent to.
func redirectToLivePort(req *http.Request, loader Loader, logger *slog.Logger) *http.Request {
	source, ok := loader.(PortSource)
	if !ok {
		return req
	}
	port := source.Port()
	if port <= 0 || req.URL == nil {
		return req
	}
	host := net.JoinHostPort(LoopbackHost, strconv.Itoa(port))
	if req.URL.Host == host {
		return req
	}

	previous := req.URL.Host
	clone := req.Clone(req.Context())
	if clone.URL == nil {
		return req
	}
	clone.URL.Host = host
	if clone.Host != "" {
		clone.Host = host
	}
	if logger != nil {
		logger.Debug("embeddedllm: redirecting the request to the live embedded LLM port",
			"from", previous, "to", host)
	}
	return clone
}

// CloseIdleConnections forwards to the wrapped transport when it supports it, so
// http.Client.CloseIdleConnections still reaches the real connection pool
// through this wrapper (the stdlib only recognises the method on the
// RoundTripper it was handed).
func (t *EnsureLoadedTransport) CloseIdleConnections() {
	type closeIdler interface{ CloseIdleConnections() }
	if idler, ok := t.base.(closeIdler); ok {
		idler.CloseIdleConnections()
	}
}

// ensureLoaded blocks until the model is resident, the load fails, the
// caller's own context gives up, or THIS request's wait budget expires. The
// budget is armed here, per waiter, and deliberately NOT around the Load call:
// a cancelled Load discards the half-loaded weights and would cost the NEXT
// request a second multi-minute cold start. See ErrLoadWaitTimeout.
func (t *EnsureLoadedTransport) ensureLoaded(ctx context.Context) error {
	call := t.joinOrStart()
	t.waiters.Add(1)
	defer t.waiters.Add(-1)

	waitCtx, cancel := context.WithTimeout(ctx, t.waitTimeout)
	defer cancel()

	select {
	case <-call.done:
		return call.err
	case <-waitCtx.Done():
		if ctxErr := ctx.Err(); ctxErr != nil {
			// The request's own budget ran out first. The load keeps running
			// detached, so the NEXT request finds the model resident.
			return fmt.Errorf("embeddedllm: waiting for the model to load: %w", ctxErr)
		}
		// The transport's wait budget expired while the load was still
		// legitimately in progress; report it as such (conflating it with the
		// supervisor's ErrLoadTimeout would send an operator tuning the wrong
		// knob) and leave the load running — a retry joins it.
		err := fmt.Errorf("%w of %s: %w", ErrLoadWaitTimeout, t.waitTimeout, waitCtx.Err())
		if t.logger != nil {
			t.logger.Warn("embeddedllm: the load wait budget expired before the model became resident; the load continues",
				"wait_budget", t.waitTimeout, "error", err)
		}
		return err
	}
}

// joinOrStart returns the in-flight load, starting one only when there is none.
func (t *EnsureLoadedTransport) joinOrStart() *loadCall {
	t.mu.Lock()
	if t.inflight != nil {
		call := t.inflight
		t.mu.Unlock()
		return call
	}
	call := &loadCall{done: make(chan struct{})}
	t.inflight = call
	t.mu.Unlock()

	go t.load(call)
	return call
}

// load performs the one coalesced load on its own goroutine, with a context
// derived from NOTHING the caller owns and NO transport deadline: the wait
// budget belongs to each waiter, and arming it here would cancel the load —
// discarding the half-loaded weights — the moment a request ran out of
// patience. The load is still bounded: the supervisor's ReadyTimeout is the
// authority on "wedged" and carries the diagnosis.
func (t *EnsureLoadedTransport) load(call *loadCall) {
	started := time.Now()

	if t.logger != nil {
		t.logger.Debug("embeddedllm: ensuring the model is loaded before a request",
			"wait_budget", t.waitTimeout)
	}

	err := t.loader.Load(context.Background())
	switch {
	case err == nil && t.logger != nil:
		t.logger.Debug("embeddedllm: the model is resident, releasing the waiting requests",
			"waiters", t.waiters.Load(), "waited", time.Since(started))
	case err != nil && t.logger != nil:
		t.logger.Warn("embeddedllm: the load failed",
			"error", err, "waited", time.Since(started))
	}

	call.err = err
	t.mu.Lock()
	if t.inflight == call {
		t.inflight = nil
	}
	t.mu.Unlock()
	close(call.done)
}

// activityBody marks activity and releases the in-flight count once, when the
// response it carries is finished — fully read or closed, whichever happens
// first. Wrapping the body (instead of acting when RoundTrip returns) is what
// keeps a long streamed generation from being charged to the idle budget; the
// request deadline's release hook rides along for the same reason (the context
// armed after the gate must stay alive while the body is being read). mark,
// end and release may each be nil.
type activityBody struct {
	io.ReadCloser
	mark    func()
	end     func()
	release func()
	once    sync.Once
}

// finish runs mark and end exactly once between them; a double release of the
// in-flight count would take another request's slot with it, and the idle path
// would then unload a model that is still serving.
func (b *activityBody) finish() {
	b.once.Do(func() {
		if b.mark != nil {
			b.mark()
		}
		if b.end != nil {
			b.end()
		}
	})
}

func (b *activityBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.finish()
	}
	return n, err
}

func (b *activityBody) Close() error {
	err := b.ReadCloser.Close()
	b.finish()
	if b.release != nil {
		b.release()
	}
	return err
}

// EnsureLoadedClient derives the inference client for the embedded provider
// entry: a clone of base (or shared) whose transport ensures the model is
// resident, and whose Timeout is moved — not dropped — into the transport as
// the post-gate requestTimeout, so the cold-load wait is never charged to the
// exchange and the request still gets the full configured budget. A 0 source
// Timeout is legitimate only under the adaptive budget wiring (ADR-071), where
// the llmbudget transport beneath this gate arms the resolved deadline.
// See specs/domains/embedded-llm.md (Invariants).
//
// base is the pin resolver's client for this entry (nil when no pin applies);
// preferring it keeps the pin and the proxy-stripping in place while the long
// timeout is the source either way. loader nil returns base unchanged —
// including nil, the "no override, use the router-level client" answer — so
// the pin rule decides first and this only decorates its result.
func EnsureLoadedClient(base, shared *http.Client, loader Loader, waitTimeout time.Duration, logger *slog.Logger) *http.Client {
	if loader == nil {
		return base
	}
	start := base
	if start == nil {
		start = shared
	}
	if start == nil {
		start = &http.Client{}
	}
	requestTimeout := start.Timeout
	clone := *start
	clone.Timeout = 0
	clone.Transport = newEnsureLoadedTransport(start.Transport, loader, waitTimeout, requestTimeout, logger)
	return &clone
}
