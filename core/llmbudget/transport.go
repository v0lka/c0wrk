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
	WireJSONModel WireFormat = iota // top-level JSON "model" field — the openai/anthropic shapes

	WireURLModel // request URL path — the "{baseURL}/models/{model}:generateContent" gemini shape
)

// ModelOverrides carries the per-model operator opinions fed to the resolver
// (ADR-071 D5/D7); zero values mean "no opinion", and only a wire-extracted
// model name can match config. No warmup override by design (D4) — a fixed
// request_timeout already gives a longer first-call deadline.
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

	// GlobalRequestTimeout is timeouts.llmRequestTimeout: positive means a
	// fixed deadline that bypasses the adaptive budget (ADR-071 D5).
	GlobalRequestTimeout time.Duration

	// AdaptiveEnabled mirrors timeouts.adaptive_budget.enabled (ADR-071 D11);
	// the transport also refuses to arm when false — a stale wire cannot arm.
	AdaptiveEnabled bool

	// Logger receives Debug-level budget diagnostics; may be nil.
	Logger *slog.Logger
}

// Transport is the adaptive-budget http.RoundTripper (ADR-071 D9) installed
// via the llm.ProviderEntry.HTTPClient seam: it arms every main-agent request
// that carries no deadline of its own with the resolver's budget — the
// stalled-upstream invariant (load-bearing properties: always-arm, never-
// trained provider-level key, service-call pass-through, honest own-expiry,
// deadline outliving RoundTrip — specs/domains/llm-providers.md). The armed
// context and any buffered body ride on a clone.
type Transport struct {
	base      http.RoundTripper
	opts      TransportOptions
	mu        sync.Mutex
	lastArmed map[string]time.Duration
}

// NewTransport wraps base (nil → http.DefaultTransport).
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

// Base returns the transport beneath the budget arming — the seam the builder
// tests use to pin the layering contract (ensure-loaded → budget → pin →
// dial, specs/domains/llm-providers.md).
func (tr *Transport) Base() http.RoundTripper { return tr.base }

// CloseIdleConnections forwards to the transport beneath the budget arming:
// http.Client.CloseIdleConnections only reaches the RoundTripper it was
// handed, so a wrapper without this strands the keep-alive pool.
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
		// Kill-switch off, or the caller owns the deadline — forward untouched;
		// passThrough still reports the attempt's duration.
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

	// The caller-installed AttemptRecorder receives the winning attempt's
	// duration (ADR-071 D1); nil when none was installed.
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
			// Chains context.DeadlineExceeded (%w), so sp4rk's classifyNetError
			// still retries under a fresh budget (ADR-071 D9).
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

// passThrough forwards the request untouched but still reports the attempt's
// duration to a caller-installed recorder (measured to body completion like
// the armed path), so no router retries pollute the sample.
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

// recordingBody reports the attempt's duration to the caller's
// AttemptRecorder once the body is over — the pass-through counterpart of
// budgetBody.
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

// budgetBody ends the request exactly once the response is over: releases the
// armed deadline, escalates on own expiry, reports the duration to the
// caller's recorder. One sync.Once guards the handler — a Close concurrent
// with a Read is legal, and an unsynchronised flag could escalate twice.
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

// finish ends the request's budget exactly once: a non-EOF error chaining
// context.DeadlineExceeded while the parent is alive escalates; the duration
// is reported only on a cleanly-ended body (a retried attempt overwrites).
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

// isOwnExpiry reports whether err is the deadline this transport armed: the
// exceeded deadline must chain AND the caller's context must still be alive.
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

// budgetKey returns the table key: the wire-extracted model name, else the
// "@"-prefixed provider name (collision-proof); a nameless provider is "@unknown".
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

// readBody buffers the request body for wire inspection and closes the
// original; the buffered bytes replay onto the armed clone, leaving the
// handed request exactly as the RoundTripper contract permits. An unreadable
// body is a hard error — it could not be forwarded intact anyway.
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

// ExtractModel pulls the model name out of one outbound request per the wire
// format (see the WireFormat variants); ok is false on any failure, and the
// caller falls back to the provider-level key.
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
