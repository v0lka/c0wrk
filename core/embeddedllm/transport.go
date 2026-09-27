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

// This file makes the embedded model's first request transparent. The router
// dials the generated provider entry (http://127.0.0.1:<port>/v1) exactly like
// any other OpenAI-compatible endpoint, and — with the idle unload armed — that
// endpoint is usually NOT listening: the bytes are on disk, the weights are not
// in memory. EnsureLoadedTransport is installed on that one provider entry
// (through llm.ProviderEntry.HTTPClient, the same hook core/llmtls uses) and
// makes the model resident before the request is sent, then restarts the idle
// budget when the response completes.
//
// Three properties are load-bearing and easy to lose:
//
//   - The wait is BOUNDED. A cold load can take minutes, but it can also wedge;
//     a request that waits forever is indistinguishable from a hung session, so
//     every REQUEST waits under DefaultLoadWaitTimeout and exceeding it is an
//     explicit ErrLoadWaitTimeout. The budget bounds the WAITING and never the
//     load: it is armed per waiter, so its expiry stops that request and leaves
//     the in-flight load alone, bounded by the supervisor's own ready budget.
//   - Concurrent requests COALESCE. One cold load serves every waiter instead of
//     N loads queueing on the supervisor's single-instance gate.
//   - The load is NOT bound to a request context, nor to the wait budget.
//     timeouts.llmRequestTimeout (default 10 min) is shorter than the
//     supervisor's own ready budget (DefaultReadyTimeout, 15 min), so a client
//     timeout during a cold load would otherwise cancel the load and throw the
//     half-loaded weights away — and the next request would start from zero, so
//     the model would never become resident. A cancelled request therefore stops
//     WAITING (promptly, with its own context error) while the load runs to
//     completion detached, and the same is true of a request that runs out of
//     wait budget. This mirrors the supervisor's own rule that the process
//     outlives the Load that started it.
//
// A fourth property is what makes the wait honest: THE LOAD IS NOT CHARGED TO
// THE REQUEST. http.Client.Timeout covers the whole exchange, gate included, so
// a client that both waits for a cold load and carries the LLM request budget
// would hand the generation whatever is left after the weights land — with the
// default 10 min budget and a 15 min ready allowance, a legitimately slow load
// would consume the request's entire timeout before a single token is generated.
// EnsureLoadedClient therefore zeroes the derived client's Timeout and hands the
// budget to this transport as requestTimeout, which arms it only AFTER the model
// is resident. The request still gets the full configured timeout; it simply
// starts when the model can answer, which is the point of the gate.

// Loader is the supervisor seam the ensure-loaded transport drives. *Server
// satisfies it as-is: Load is idempotent and single-instance, and MarkActivity
// is what restarts the auto-unload budget.
type Loader interface {
	// Load makes the model resident and blocks until it can answer. A Load
	// against an already-serving model is a no-op that only marks activity.
	Load(ctx context.Context) error
	// MarkActivity restarts the auto-unload budget from now.
	MarkActivity()
}

// *Server satisfies Loader, and so does the value production actually hands
// this transport: backend.embeddedLoaderRef, a lazy atomic reference that
// resolves to the supervisor at call time (the router is first built before the
// subsystem exists, and a per-session router is built under a lock the
// supervisor's own state must never be taken under).
//
// That indirection is why the three assertions below are NOT sufficient on
// their own. A capability discovered through an optional type assertion on the
// Loader value is invisible to the compiler at the wiring site, so asserting it
// here only proves the SUPERVISOR has it — a production Loader that forwards to
// the supervisor but forgets the method silently loses the capability, and
// every test that substitutes a double keeps passing. The backend therefore
// pins the same three interfaces on embeddedLoaderRef; this pair of assertions
// is the two halves of one contract, and neither half may be removed.
var _ Loader = (*Server)(nil)

// PortSource is an OPTIONAL Loader capability: the loopback port the supervisor
// actually bound. *Server satisfies it as-is, and so does production's
// embeddedLoaderRef — the redirect below is a live control, not a hypothetical.
//
// It exists because the request URL is not authoritative. The router builds it
// from the provider base_url, which is derived from the PERSISTED port — and the
// persisted port is a preference the load re-checks (see port.go): when it is
// taken the server moves to the next free one. Without this seam the request
// that triggered the load would still be sent to the old port, i.e. to whatever
// unrelated local process holds it, leaking the prompt and reporting a bogus
// answer. Declaring it as optional (rather than growing Loader) keeps every
// existing double and test loader valid: one that does not report a port simply
// gets no redirect.
type PortSource interface {
	// Port is the live loopback port, or 0 when no server has been started.
	Port() int
}

var _ PortSource = (*Server)(nil)

// RequestTracker is an OPTIONAL Loader capability: the count of requests the
// transport currently has open against the model. *Server satisfies it as-is, and
// so does production's embeddedLoaderRef — the mid-generation deferral below is a
// live control, not a hypothetical.
//
// It exists because activity is stamped on COMPLETION (Loader.MarkActivity),
// which cannot protect a single generation that outlives the whole idle budget:
// the timer would fire mid-answer and stop the server out from under the request
// that is using it. Bracketing the exchange with a count lets the idle path defer
// that unload instead. Declaring it as optional — rather than growing Loader —
// keeps every existing double and test loader valid: one that does not track
// requests simply gets the completion stamp alone, which is the pre-existing
// behaviour — and which is exactly why the backend pins this interface on its own
// Loader value too (see the Loader assertion above).
type RequestTracker interface {
	// BeginRequest records that a request is now in flight.
	BeginRequest()
	// EndRequest releases one in-flight request.
	EndRequest()
}

var _ RequestTracker = (*Server)(nil)

// DefaultLoadWaitTimeout bounds how long one request waits for the model to
// become resident.
//
// It deliberately exceeds DefaultReadyTimeout: the supervisor's own ready budget
// is the authority on "this load is wedged", and a transport budget shorter than
// it would cut a legitimate cold load short and report a transport timeout
// instead of the diagnosis Load produces (the server's own log tail). The slack
// covers the rest of Load — the port re-check, the spawn and the final probe.
const DefaultLoadWaitTimeout = DefaultReadyTimeout + 2*time.Minute

// ErrLoadWaitTimeout reports that the transport's own wait budget expired before
// the model became resident. It is distinct from ErrLoadTimeout, which is the
// supervisor's ready budget.
//
// The load is NOT cancelled by it. The budget is armed per waiter (see
// ensureLoaded), so its expiry stops THAT request from waiting and leaves the
// in-flight load running detached, bounded by the supervisor's own
// ReadyTimeout — which is the budget that decides a load is wedged and carries
// the diagnosis. A following request therefore joins the same load, or finds the
// model already resident, instead of paying for a second cold start.
//
// An ErrLoadWaitTimeout means exactly one of: the load was still legitimately in
// progress, or the supervisor's ready budget is misconfigured above the
// transport's wait budget. Since the load survives, the honest recovery for a
// caller is to retry — not to report the model as broken.
var ErrLoadWaitTimeout = errors.New("the embedded LLM did not become resident within the load wait budget")

// EnsureLoadedTransport is an http.RoundTripper that makes the embedded model
// resident before every request it carries, and marks activity when that
// request's response completes.
//
// Build one with NewEnsureLoadedTransport; attach it to a provider entry with
// EnsureLoadedClient. It is safe for concurrent use.
type EnsureLoadedTransport struct {
	base        http.RoundTripper
	loader      Loader
	waitTimeout time.Duration
	// requestTimeout, when > 0, bounds the request itself and is armed only
	// after the model is resident — see the file comment. It replaces
	// http.Client.Timeout, which cannot distinguish the wait from the exchange.
	requestTimeout time.Duration
	logger         *slog.Logger

	// mu guards inflight only — never held across a Load.
	mu       sync.Mutex
	inflight *loadCall

	// waiters counts the requests currently blocked on a load. It feeds the
	// "the model is ready" diagnostic — how many requests one cold load served
	// is the number that tells an operator whether the coalescing is doing its
	// job — and it is what a test polls to know every waiter has joined.
	waiters atomic.Int64
}

// loadCall is one coalesced load. Concurrent requests share the in-flight call
// instead of each starting their own, which is the singleflight semantics the
// cold path needs: N parallel first requests must produce ONE weight load.
type loadCall struct {
	// done is closed after err has been written, so a waiter that observes the
	// close may read err without further synchronisation.
	done chan struct{}
	err  error
}

// NewEnsureLoadedTransport wraps base (nil → http.DefaultTransport) so it loads
// the model before sending and marks activity on completion.
//
// The request keeps whatever budget its own context carries: requestTimeout is
// zero, so the transport arms no deadline of its own. Production goes through
// EnsureLoadedClient, which does arm one (see newEnsureLoadedTransport) because
// it also zeroes the derived client's Timeout.
//
// loader nil yields a pass-through transport rather than a panic: the request
// path is the wrong place to fail on a wiring mistake, and the builder already
// declines to install a transport when no supervisor is configured.
// waitTimeout <= 0 → DefaultLoadWaitTimeout. logger may be nil.
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

// RoundTrip implements http.RoundTripper: ensure the model is resident, aim the
// request at the port it is actually serving on, hand it to the wrapped
// transport, and arrange for the request to be counted in flight — and activity
// to be marked — when the response is finished rather than when its headers
// arrive.
//
// A load failure is returned instead of the request being sent — dialing a
// socket nothing is listening on would only add a confusing "connection
// refused" on top of the real diagnosis. The same reasoning makes the redirect
// load-bearing: a load that MOVED the port leaves the original URL pointing at
// a socket this process does not own, and sending it would hand the prompt to a
// stranger instead of failing.
func (t *EnsureLoadedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("embeddedllm: nil request")
	}
	if t.loader != nil {
		if err := t.ensureLoaded(req.Context()); err != nil {
			return nil, err
		}
		// The load may have moved the server to a different loopback port than
		// the one this request's URL was built from; aim it at the live one.
		req = redirectToLivePort(req, t.loader, t.logger)
	}

	// The request budget is armed HERE, not by http.Client.Timeout: the load
	// above may have taken minutes, and charging them to the exchange would
	// leave the generation with whatever is left. The caller's own cancellation
	// still applies — the derived context inherits it — but the wait no longer
	// counts against the configured timeout.
	var release func()
	if t.requestTimeout > 0 {
		var ctx context.Context
		ctx, release = context.WithTimeout(req.Context(), t.requestTimeout)
		req = req.WithContext(ctx)
	}

	// The in-flight count is the other half of "a long generation is activity":
	// the mark below only fires when the response COMPLETES, so a single request
	// that outlives the whole idle budget would look idle for its entire life and
	// be killed mid-answer. Bracketing the exchange lets the supervisor defer
	// that unload instead. See RequestTracker.
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

	// Marking on COMPLETION rather than on the response headers is the point: a
	// streamed generation may run for minutes, and charging it to the idle
	// budget would unload the model out from under its own request. Releasing
	// the deadline rides along for the same reason: it must outlive RoundTrip,
	// because a streamed body is read long after the headers arrive, and
	// cancelling early would tear the connection down mid-generation.
	var mark func()
	if t.loader != nil {
		mark = t.loader.MarkActivity
	}
	if resp.Body != nil && (mark != nil || end != nil || release != nil) {
		resp.Body = &activityBody{ReadCloser: resp.Body, mark: mark, end: end, release: release}
		return resp, nil
	}
	// A RoundTripper must return a body; a nil one leaves nothing to read, so
	// this request is over now — the count and the deadline are released at once
	// instead of leaking.
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
// already aimed there.
//
// A RoundTripper must not modify the request it was handed, so the rewrite
// happens on a clone; the body is shared, which is safe because nothing has
// read it yet. The explicit Host field is rewritten alongside the URL when it is
// set, so the header the server sees matches the socket it is sent to.
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

// ensureLoaded blocks until the model is resident, the load fails, the caller's
// own context gives up, or THIS request's wait budget expires.
//
// The wait budget is armed here, per waiter, and deliberately NOT around the
// Load call — that placement is the whole contract of ErrLoadWaitTimeout. A
// budget that bounded the load itself would cancel it, and a cancelled Load
// discards the half-loaded weights, so an expiry would cost the NEXT request a
// second multi-minute cold start: precisely the failure the file header promises
// cannot happen. Bounding the wait instead leaves the load running detached,
// bounded by the supervisor's own ready budget, and the next request joins it.
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
			// The request's own budget ran out first — typically
			// timeouts.llmRequestTimeout, which is shorter than a cold weight
			// load. The load keeps running detached (see the file comment) so
			// the NEXT request finds the model resident instead of starting over.
			return fmt.Errorf("embeddedllm: waiting for the model to load: %w", ctxErr)
		}
		// The transport's wait budget expired while the load was still
		// legitimately in progress. Report it as such — the supervisor's own
		// ErrLoadTimeout carries the ready-budget diagnosis, and conflating the
		// two would send an operator tuning the wrong knob — and leave the load
		// running: it is bounded by the supervisor, and a retry joins it.
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

// load performs the one coalesced load. It runs on its own goroutine with a
// context derived from NOTHING the caller owns and carrying NO deadline of the
// transport's: the wait budget belongs to each waiter (see ensureLoaded), and
// arming it here would cancel the load — and a cancelled Load discards the
// half-loaded weights — the moment a request ran out of patience.
//
// The load is still bounded. The supervisor's own ReadyTimeout is the authority
// on "this load is wedged", and its error carries the diagnosis (the server's
// own log tail), which is what DefaultLoadWaitTimeout's slack exists to let it
// deliver.
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
// keeps a long streamed generation from being charged to the idle budget, and
// from being mistaken for an idle model while it is still streaming.
//
// It also owns the request deadline's release hook for the same reason: the
// context armed after the gate must stay alive while the body is being read, so
// it is cancelled on Close rather than when RoundTrip returns.
//
// mark and end may each be nil (a pass-through transport that only carries a
// deadline, or a loader that does not track requests) and release may be nil (a
// transport with no request budget of its own).
type activityBody struct {
	io.ReadCloser
	mark    func()
	end     func()
	release func()
	once    sync.Once
}

// finish runs mark and end exactly once between them. The once matters for end in
// a way it does not for mark: releasing an in-flight count twice would take
// another request's slot with it, and the idle path would then unload a model
// that is still serving.
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
		// EOF or a broken stream: either way this response is over.
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
// entry: a clone of base whose transport ensures the model is resident.
//
//	base   the client the TLS-pin resolver produced for this entry
//	       (llmtls.RouterEntryClient) — nil when no pin applies
//	shared the router-level LLM client, whose Timeout the result must preserve
//
// The clone is what keeps the llm-providers invariant intact: the client handed
// to a ProviderEntry shadows RouterConfig.HTTPClient, so inference MUST be
// bounded by timeouts.llmRequestTimeout rather than the 30 s web-fetch proxy
// timeout. base is preferred because a pinned client is itself cloned from
// shared, so either way the long timeout is the source — and layering on top of
// base keeps the pin (and the proxy-stripping a bypassed host needs) in place.
//
// That budget is then MOVED, not dropped. The client's own Timeout would cover
// the cold-load wait together with the exchange, so the transport takes it over
// and arms it only once the model is resident: the request still gets the full
// configured timeout, and the load no longer eats into it. The zero Timeout on
// the clone is therefore deliberate — read it together with
// EnsureLoadedTransport.requestTimeout.
//
// base is used as the timeout source when present so a pinned entry keeps the
// budget the pin resolver gave it. Neither client carrying a timeout yields a
// transport with no request budget, which matches the ungated behaviour.
//
// loader nil returns base unchanged — including nil, which is the "this entry
// carries no override, let the SDK use the router-level client" answer the pin
// resolver already gives. The resolution order is therefore preserved: the pin
// rule decides first and this only decorates its result.
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
