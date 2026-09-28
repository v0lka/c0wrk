package llmbudget

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// WireFormat selects how the transport extracts the model name from the wire.
type WireFormat int

const (
	// WireJSONModel reads the top-level "model" field of the JSON request
	// body — the openai and anthropic wire shapes.
	WireJSONModel WireFormat = iota

	// WireURLModel reads the model from the request URL path — the
	// "{baseURL}/models/{model}:generateContent" gemini shape.
	WireURLModel
)

// ModelOverrides carries the per-model operator opinions the transport feeds
// the resolver (ADR-071 D5/D7). Zero values mean "no opinion". The wiring
// supplies them from llm.models.<name>.{request_timeout,output_limit}; only a
// wire-extracted model name can match config. There is deliberately no warmup
// override: a fixed per-model request_timeout already gives an operator a
// longer deadline from the first call, so warmup stays the legacy
// DefaultWarmupBudget until the model has samples (D4).
type ModelOverrides struct {
	RequestTimeout time.Duration // >0: fixed deadline, never escalated
	OutputLimit    int           // >0: explicit llm.models.<name>.output_limit ceiling for out_reserve
}

// TransportOptions configures a budget Transport for one provider entry.
type TransportOptions struct {
	// Table is the session-scoped sample table. nil gets a private table
	// (useful for tests; production wiring shares one per session).
	Table *BudgetTable

	// Class is the provider's timeout class (see Classify).
	Class Class

	// Wire selects the model-extraction strategy for this provider.
	Wire WireFormat

	// ProviderName is the logical provider name; it becomes the provider-level
	// budget key ("@" + name) whenever model extraction fails.
	ProviderName string

	// Overrides resolves per-model operator opinions; may be nil.
	Overrides func(model string) ModelOverrides

	// GlobalRequestTimeout is timeouts.llmRequestTimeout as a duration:
	// a positive value is a fixed deadline that bypasses the adaptive budget
	// entirely (ADR-071 D5).
	GlobalRequestTimeout time.Duration

	// AdaptiveEnabled mirrors timeouts.adaptive_budget.enabled (ADR-071 D11).
	// The wiring should not install the transport at all when disabled, but
	// the transport also refuses to arm in that state, so a stale wire can
	// never impose adaptive budgets against a disabled kill-switch.
	AdaptiveEnabled bool

	// Logger receives Debug-level budget diagnostics; may be nil.
	Logger *slog.Logger
}

// Transport is the adaptive-budget http.RoundTripper (ADR-071 D9). Installed
// on a provider's HTTP client (the llm.ProviderEntry.HTTPClient seam, like
// core/llmtls and the embeddedllm ensure-loaded transport), it arms every
// main-agent LLM request with the resolver's budget as a context deadline.
//
// Load-bearing properties:
//
//   - It ALWAYS arms when enabled and the request context carries no deadline
//     of its own — the stalled-upstream invariant. Even a request whose model
//     could not be extracted gets a deadline under the provider-level key
//     ("@" + provider). That key is never TRAINED: samples are ingested keyed
//     by the model the provider actually served (resp.Model), and no ingest ever
//     writes the provider-level key, so it never accumulates evidence and
//     resolves to DefaultWarmupBudget (at least the class floor for every
//     class) rather than a fitted budget. The one exception is the
//     service-call path, where the caller already owns the deadline — arming
//     over it would shorten someone else's budget, so the request passes
//     through untouched.
//   - Expiry detection is honest: only an error that chains
//     context.DeadlineExceeded while the CALLER's context is still alive is
//     this transport's own expiry. That is what escalates the model's next
//     budget ×2 (latched in the table, cleared by the next successful
//     ingest); a caller cancellation or a pre-existing deadline never does.
//   - The armed deadline outlives RoundTrip: a streamed body is read long
//     after the headers arrive, so the timer is released by the body wrapper
//     (on EOF, error, or Close), never by this frame.
//   - The handed request is not modified — the armed context and the buffered
//     body ride on a clone.
type Transport struct {
	base      http.RoundTripper
	opts      TransportOptions
	mu        sync.Mutex
	lastArmed map[string]time.Duration
}

// NewTransport wraps base (nil → http.DefaultTransport) with the budget
// arming described on Transport.
func NewTransport(base http.RoundTripper, opts TransportOptions) *Transport {
	if base == nil {
		base = http.DefaultTransport
	}
	if opts.Table == nil {
		opts.Table = NewBudgetTable()
	}
	return &Transport{
		base:      base,
		opts:      opts,
		lastArmed: make(map[string]time.Duration),
	}
}

// Base returns the transport beneath the budget arming. It is a diagnostic
// and test seam for the wiring's layering contract: the builder must keep the
// llmtls pinned transport BENEATH this wrapper (the pin resolver needs a
// concrete *http.Transport to hold its tls.Config) and the embedded
// ensure-loaded gate ABOVE it (ADR-071 D13), so the wrapped chain reads
// ensure-loaded → budget → pin → dial. Reading the layering back through this
// accessor is how the builder tests pin it.
func (tr *Transport) Base() http.RoundTripper { return tr.base }

// CloseIdleConnections forwards to the transport beneath the budget arming when
// it supports it (symmetric with embeddedllm.EnsureLoadedTransport). Without it
// the wrapper silently truncates the closeIdler chain the pin resolver and the
// ensure-loaded gate deliberately maintain: http.Client.CloseIdleConnections
// only recognises the method on the RoundTripper it was handed, so a budget
// wrapper without it would strand the provider's keep-alive pool.
func (tr *Transport) CloseIdleConnections() {
	type closeIdler interface{ CloseIdleConnections() }
	if idler, ok := tr.base.(closeIdler); ok {
		idler.CloseIdleConnections()
	}
}

// RoundTrip implements http.RoundTripper.
func (tr *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("llmbudget: nil request")
	}
	if _, hasDeadline := req.Context().Deadline(); !tr.opts.AdaptiveEnabled || hasDeadline {
		// Kill-switch off (defensively; the wiring should not install the
		// transport at all in that state), or the caller already owns the
		// deadline — the service-call path. Forward untouched, but still report
		// the attempt's duration when the caller installed a recorder, so the
		// ingest never falls back to the retrying-router aggregate.
		return tr.passThrough(req)
	}

	bodyHad := req.Body != nil
	body, err := readBody(req)
	if err != nil {
		return nil, err
	}
	model, ok := ExtractModel(tr.opts.Wire, req.URL, body)
	key := tr.budgetKey(model, ok)
	ov := tr.overridesFor(model, ok)
	budget := tr.opts.Table.ResolveDeadline(ResolverInput{
		Class:                  tr.opts.Class,
		Key:                    key,
		BodyBytes:              len(body),
		OutputLimit:            ov.OutputLimit,
		PerModelRequestTimeout: ov.RequestTimeout,
		GlobalRequestTimeout:   tr.opts.GlobalRequestTimeout,
		AdaptiveEnabled:        true,
	})
	tr.logBudgetChange(key, budget)

	// The caller may have installed an AttemptRecorder to receive the duration
	// of the attempt that actually wins (ADR-071 D1): the router wraps retries
	// and backoff around this transport, so the caller-level timer aggregates
	// them and would inflate the learned rates. nil when the caller did not ask.
	rec := attemptRecorderFrom(req.Context())

	parent := req.Context()
	armedCtx, cancel := context.WithTimeout(parent, budget)
	armed := req.Clone(armedCtx)
	if bodyHad {
		armed.Body = io.NopCloser(bytes.NewReader(body))
		armed.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}

	start := time.Now()
	resp, err := tr.base.RoundTrip(armed)
	if err != nil {
		cancel()
		if isOwnExpiry(parent, err) {
			tr.escalate(key, budget)
			// The error chains context.DeadlineExceeded (%w), so sp4rk's
			// classifyNetError still reads it as a retryable timeout and the
			// retry re-enters under a fresh budget (ADR-071 D9).
			return nil, fmt.Errorf("adaptive request budget exhausted: %s armed for %q (own budget expiry, not a provider abort; the next budget escalates): %w", budget, key, err)
		}
		return nil, err
	}
	if resp == nil {
		// A RoundTripper must not return (nil, nil); a base that does would
		// strand the deadline's timer.
		cancel()
		return nil, errors.New("llmbudget: the wrapped transport returned no response")
	}
	if resp.Body == nil {
		if rec != nil {
			rec.record(time.Since(start))
		}
		cancel()
		return resp, nil
	}
	// The deadline must outlive RoundTrip for the streamed body; the wrapper
	// releases the timer, detects a mid-body own-expiry, and reports the
	// attempt's duration once the body is over.
	resp.Body = &budgetBody{
		ReadCloser: resp.Body,
		cancel:     cancel,
		parent:     parent,
		escalate: func() {
			tr.escalate(key, budget)
		},
		rec:   rec,
		start: start,
	}
	return resp, nil
}

// passThrough forwards the request untouched (the kill-switch-off or
// caller-owned-deadline path) while still reporting the provider attempt's
// duration to a caller-installed recorder, measured to body completion like the
// armed path. This keeps the learned sample free of any router retry and its
// backoff even for a call the budget transport does not itself arm.
func (tr *Transport) passThrough(req *http.Request) (*http.Response, error) {
	rec := attemptRecorderFrom(req.Context())
	if rec == nil {
		return tr.base.RoundTrip(req)
	}
	start := time.Now()
	resp, err := tr.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	if resp.Body == nil {
		rec.record(time.Since(start))
		return resp, nil
	}
	resp.Body = &recordingBody{ReadCloser: resp.Body, rec: rec, start: start}
	return resp, nil
}

// recordingBody reports a provider attempt's wall-clock duration to the caller's
// AttemptRecorder once the body is over — the pass-through counterpart of
// budgetBody, which does the same on top of releasing the armed deadline and
// detecting an own-expiry.
type recordingBody struct {
	io.ReadCloser
	rec   *AttemptRecorder
	start time.Time
	once  sync.Once
}

func (b *recordingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(func() { b.rec.recordSince(err, b.start) })
	}
	return n, err
}

func (b *recordingBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { b.rec.recordSince(nil, b.start) })
	return err
}

// budgetBody releases the armed deadline exactly once the response is over
// (EOF, read error, or Close), escalates when that end was the transport's own
// expiry — a body read that died of the armed deadline while the caller's
// context is still alive — and reports the attempt's duration to the caller's
// recorder once the body completes cleanly.
//
// A single sync.Once guards the WHOLE end-of-request handler: the io.ReadCloser
// contract does not forbid a Close concurrent with a Read (a context
// cancellation while a streamed body is still being read, for instance), and a
// bare bool checked-then-set without synchronisation is a data race that can
// let two goroutines through and escalate twice.
type budgetBody struct {
	io.ReadCloser
	cancel   context.CancelFunc
	parent   context.Context
	escalate func()
	once     sync.Once
	rec      *AttemptRecorder
	start    time.Time
}

func (b *budgetBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.finish(err)
	}
	return n, err
}

func (b *budgetBody) Close() error {
	err := b.ReadCloser.Close()
	b.finish(nil)
	return err
}

// finish ends the request's budget exactly once. A non-EOF error that chains
// context.DeadlineExceeded while the parent is alive is the transport's own
// expiry and escalates; an EOF or a Close is a completed (or abandoned)
// response and does not. The attempt's duration is reported only when the body
// ended cleanly (EOF, or a close with no read error): the value is consumed
// only on a successful call, and a later retried attempt overwrites an earlier
// failed one, so a partial read never pollutes the sample.
func (b *budgetBody) finish(err error) {
	b.once.Do(func() {
		if err != nil && !errors.Is(err, io.EOF) && isOwnExpiry(b.parent, err) {
			b.escalate()
		}
		if b.rec != nil {
			b.rec.recordSince(err, b.start)
		}
		b.cancel()
	})
}

// isOwnExpiry reports whether err is the deadline THIS transport armed, as
// opposed to a caller cancellation, a caller-set deadline, or a provider-side
// abort: the exceeded deadline must be in the error chain AND the caller's
// context must still be alive.
func isOwnExpiry(parent context.Context, err error) bool {
	return err != nil && errors.Is(err, context.DeadlineExceeded) && parent.Err() == nil
}

// escalate latches the ×2 escalation for the key and logs it.
func (tr *Transport) escalate(key string, budget time.Duration) {
	tr.opts.Table.Escalate(key)
	if tr.opts.Logger != nil {
		tr.opts.Logger.Debug("llm request budget escalated", "key", key, "budget", budget)
	}
}

// budgetKey returns the table key for the request: the wire-extracted model
// name when extraction succeeded, else the provider-level key — "@" + the
// provider name — so a model name and a provider name can never collide in
// the shared table. A nameless provider falls back to the shared "@unknown".
func (tr *Transport) budgetKey(model string, ok bool) string {
	if ok && model != "" {
		return model
	}
	if tr.opts.ProviderName == "" {
		return "@unknown"
	}
	return "@" + tr.opts.ProviderName
}

// overridesFor looks up the per-model operator opinions. Only a
// wire-extracted model name can match config; the provider-level key has none.
func (tr *Transport) overridesFor(model string, ok bool) ModelOverrides {
	if !ok || tr.opts.Overrides == nil {
		return ModelOverrides{}
	}
	return tr.opts.Overrides(model)
}

// logBudgetChange emits a Debug line when the key's armed budget first appears
// or moves — the diagnostic trail of the budget converging (ADR-071 D6).
func (tr *Transport) logBudgetChange(key string, budget time.Duration) {
	if tr.opts.Logger == nil {
		return
	}
	tr.mu.Lock()
	prev, seen := tr.lastArmed[key]
	tr.lastArmed[key] = budget
	tr.mu.Unlock()
	switch {
	case !seen:
		tr.opts.Logger.Debug("llm request budget armed", "key", key, "budget", budget, "class", string(tr.opts.Class))
	case prev != budget:
		tr.opts.Logger.Debug("llm request budget changed", "key", key, "from", prev, "to", budget)
	}
}

// readBody buffers the request body for wire inspection (model extraction and
// the size estimate) and closes the original. The buffered bytes are replayed
// onto the armed clone; the handed request is left exactly as the RoundTripper
// contract permits (body consumed and closed). A body that cannot be read is
// a hard error: the request could not be forwarded intact anyway.
func readBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(req.Body)
	closeErr := req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("llmbudget: buffering request body: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("llmbudget: closing buffered request body: %w", closeErr)
	}
	return data, nil
}

// ExtractModel pulls the model name out of one outbound request:
// WireJSONModel reads the top-level "model" field of the JSON body (the
// openai/anthropic shapes), WireURLModel reads the path segment after
// "/models/" up to the ":" action suffix or the next "/" (the gemini shape).
// ok is false on any failure — non-JSON body, absent field, unparseable path —
// and the caller falls back to the provider-level key.
func ExtractModel(w WireFormat, u *url.URL, body []byte) (string, bool) {
	switch w {
	case WireURLModel:
		return extractURLModel(u)
	default:
		return extractJSONModel(body)
	}
}

// extractJSONModel reads the top-level "model" field of the JSON request body.
func extractJSONModel(body []byte) (string, bool) {
	if len(body) == 0 {
		return "", false
	}
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return "", false
	}
	return probe.Model, probe.Model != ""
}

// extractURLModel reads the model from the gemini wire path
// "{baseURL}/models/{model}:generateContent". The colon action suffix and any
// deeper path segment end the name.
func extractURLModel(u *url.URL) (string, bool) {
	if u == nil {
		return "", false
	}
	const marker = "/models/"
	i := strings.Index(u.Path, marker)
	if i < 0 {
		return "", false
	}
	rest := u.Path[i+len(marker):]
	if end := strings.IndexAny(rest, "/:"); end >= 0 {
		rest = rest[:end]
	}
	return rest, rest != ""
}
