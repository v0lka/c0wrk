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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubTransport records what the budget transport actually sent downstream
// and replies with a canned response or error.
type stubTransport struct {
	mu          sync.Mutex
	hasDeadline bool
	remaining   time.Duration
	gotBody     []byte
	bodyWasNil  bool
	getBodyOK   bool
	resp        *http.Response
	err         error
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	deadline, ok := req.Context().Deadline()
	s.hasDeadline = ok
	if ok {
		s.remaining = time.Until(deadline)
	}
	if req.Body == nil {
		s.bodyWasNil = true
	} else {
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("stub: reading body: %w", err)
		}
		s.gotBody = body
		if getBody := req.GetBody; getBody != nil {
			replay, err := getBody()
			if err != nil {
				s.getBodyOK = false
			} else {
				replayed, err := io.ReadAll(replay)
				_ = replay.Close()
				s.getBodyOK = err == nil && bytes.Equal(replayed, body)
			}
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.resp != nil {
		return s.resp, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     make(http.Header),
	}, nil
}

// jsonRequest builds an openai/anthropic-shaped POST whose body carries the
// top-level "model" field.
func jsonRequest(t *testing.T, model string) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": model, "messages": []string{"hi"}})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.example.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestExtractModelOpenAIAnthropicAndGemini(t *testing.T) {
	openai, err := json.Marshal(map[string]any{"model": "gpt-test", "messages": []string{"hi"}})
	if err != nil {
		t.Fatal(err)
	}
	anthropic, err := json.Marshal(map[string]any{"model": "claude-test", "max_tokens": 1024})
	if err != nil {
		t.Fatal(err)
	}
	geminiURL := &url.URL{Path: "/v1beta/models/gemini-2.5-pro:generateContent"}
	geminiStream := &url.URL{Path: "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse"}

	tests := []struct {
		name string
		wire WireFormat
		u    *url.URL
		body []byte
		want string
		ok   bool
	}{
		{"openai json body", WireJSONModel, nil, openai, "gpt-test", true},
		{"anthropic json body", WireJSONModel, nil, anthropic, "claude-test", true},
		{"empty body", WireJSONModel, nil, nil, "", false},
		{"non-json body", WireJSONModel, nil, []byte("not json"), "", false},
		{"json without model", WireJSONModel, nil, []byte(`{"messages":[]}`), "", false},
		{"gemini url path", WireURLModel, geminiURL, nil, "gemini-2.5-pro", true},
		{"gemini streaming url", WireURLModel, geminiStream, nil, "gemini-2.5-flash", true},
		{"gemini without models segment", WireURLModel, &url.URL{Path: "/v1/chat"}, nil, "", false},
		{"gemini empty model name", WireURLModel, &url.URL{Path: "/v1beta/models/:generateContent"}, nil, "", false},
		{"nil url", WireURLModel, nil, nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ExtractModel(tt.wire, tt.u, tt.body)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("ExtractModel = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

// budgetTransport assembles a transport over the stub with the test defaults.
func budgetTransport(t *testing.T, stub *stubTransport, tb *BudgetTable, mutate func(*TransportOptions)) *Transport {
	t.Helper()
	opts := TransportOptions{
		Table:           tb,
		Class:           ClassLocal,
		Wire:            WireJSONModel,
		ProviderName:    "prov",
		AdaptiveEnabled: true,
	}
	if mutate != nil {
		mutate(&opts)
	}
	return NewTransport(stub, opts)
}

func TestTransportAlwaysArmsForUnknownModel(t *testing.T) {
	stub := &stubTransport{}
	tr := budgetTransport(t, stub, NewBudgetTable(), nil)
	req := jsonRequest(t, "") // empty model: extraction fails → provider key
	req.Body = io.NopCloser(strings.NewReader("not json at all"))
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if !stub.hasDeadline {
		t.Fatal("unknown-model request must still be armed (stalled-upstream invariant)")
	}
	// No samples under the provider key → warmup 600s, which is at least the
	// class floor for every class.
	if stub.remaining > DefaultWarmupBudget || stub.remaining <= DefaultWarmupBudget-time.Second {
		t.Fatalf("armed %v, want ~%v", stub.remaining, DefaultWarmupBudget)
	}
	if stub.remaining < FloorLocal {
		t.Fatalf("armed %v is below the class floor %v", stub.remaining, FloorLocal)
	}
}

func TestTransportUnknownModelFallsToClassFloorOnceSampled(t *testing.T) {
	tb := NewBudgetTable()
	// The provider-level key accumulates samples from earlier failed
	// extractions; zero-token samples land the ladder on the class floor.
	for _, d := range []time.Duration{time.Second, 2 * time.Second, 3 * time.Second} {
		tb.Ingest("@prov", 0, 0, d)
	}
	stub := &stubTransport{}
	tr := budgetTransport(t, stub, tb, nil)
	req := jsonRequest(t, "")
	req.Body = io.NopCloser(strings.NewReader("garbage"))
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if !nearDuration(stub.remaining, FloorLocal, time.Millisecond) {
		t.Fatalf("armed %v, want the class floor %v", stub.remaining, FloorLocal)
	}
}

func TestTransportSkipsArmingWhenDeadlineExists(t *testing.T) {
	stub := &stubTransport{}
	tr := budgetTransport(t, stub, NewBudgetTable(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 42*time.Second)
	defer cancel()
	req := jsonRequest(t, "gpt-test").WithContext(ctx)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	stub.mu.Lock()
	defer stub.mu.Unlock()
	// The caller's 42s deadline must arrive untouched — the service-call path.
	if !stub.hasDeadline || stub.remaining > 42*time.Second || stub.remaining <= 41*time.Second {
		t.Fatalf("armed %v (hasDeadline=%v), want the caller's ~42s", stub.remaining, stub.hasDeadline)
	}
}

func TestTransportPassesThroughWhenDisabled(t *testing.T) {
	stub := &stubTransport{}
	tr := budgetTransport(t, stub, NewBudgetTable(), func(o *TransportOptions) {
		o.AdaptiveEnabled = false
	})
	req := jsonRequest(t, "gpt-test")
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.hasDeadline {
		t.Fatal("kill-switch off must not arm anything")
	}
	if !strings.Contains(string(stub.gotBody), "gpt-test") {
		t.Fatalf("pass-through must forward the body intact; got %q", stub.gotBody)
	}
}

func TestTransportExtractsModelAndPreservesBody(t *testing.T) {
	tb := NewBudgetTable()
	var seen []string
	stub := &stubTransport{}
	tr := budgetTransport(t, stub, tb, func(o *TransportOptions) {
		o.Overrides = func(model string) ModelOverrides {
			seen = append(seen, model)
			return ModelOverrides{}
		}
	})
	req := jsonRequest(t, "gpt-test")
	wantBody, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(wantBody))
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if len(seen) != 1 || seen[0] != "gpt-test" {
		t.Fatalf("overrides consulted with %v, want [gpt-test]", seen)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if !bytes.Equal(stub.gotBody, wantBody) {
		t.Fatalf("downstream body = %q, want %q", stub.gotBody, wantBody)
	}
	if !stub.getBodyOK {
		t.Fatal("GetBody must replay the buffered body")
	}
}

// transportBudget computes the trained budget the transport resolves for the
// jsonRequest-shaped body of model under the fit fixture.
func transportBudget(t *testing.T, tb *BudgetTable, model string) time.Duration {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": model, "messages": []string{"hi"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return tb.ResolveDeadline(ResolverInput{
		Class:           ClassLocal,
		Key:             model,
		BodyBytes:       len(body),
		AdaptiveEnabled: true,
	})
}

func TestTransportEscalatesOnOwnExpiry(t *testing.T) {
	tb := NewBudgetTable()
	ingestFitSamples(t, tb, "gpt-test")
	handler := &captureHandler{}
	stub := &stubTransport{err: context.DeadlineExceeded}
	tr := budgetTransport(t, stub, tb, func(o *TransportOptions) {
		o.Logger = slog.New(handler)
	})
	base := transportBudget(t, tb, "gpt-test")
	resp, err := tr.RoundTrip(jsonRequest(t, "gpt-test"))
	if err == nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		t.Fatal("expected the budget-exhaustion error")
	}
	// The timeout error chains context.DeadlineExceeded (retryable by sp4rk's
	// classifyNetError) and names the budget that was armed.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error must chain context.DeadlineExceeded; got %v", err)
	}
	if !strings.Contains(err.Error(), base.String()) {
		t.Fatalf("error must name the armed budget (%v); got %q", base, err)
	}
	// The next budget for the model is ×2, and the escalation is logged.
	if got := transportBudget(t, tb, "gpt-test"); !nearDuration(got, 2*base, time.Millisecond) {
		t.Fatalf("post-expiry budget = %v, want ~%v (×2)", got, 2*base)
	}
	if !handler.has("llm request budget escalated") {
		t.Fatalf("escalation not logged; got %v", handler.all())
	}
	if !handler.has("llm request budget armed") {
		t.Fatalf("arming not logged; got %v", handler.all())
	}
}

func TestTransportDoesNotEscalateOnCallerCancel(t *testing.T) {
	tb := NewBudgetTable()
	ingestFitSamples(t, tb, "gpt-test")
	stub := &stubTransport{err: context.Canceled}
	tr := budgetTransport(t, stub, tb, nil)
	ctx, cancel := context.WithCancel(context.Background())
	req := jsonRequest(t, "gpt-test").WithContext(ctx)
	base := transportBudget(t, tb, "gpt-test")
	resp, err := tr.RoundTrip(req)
	if err == nil && resp != nil {
		_ = resp.Body.Close()
	}
	cancel()
	if got := transportBudget(t, tb, "gpt-test"); !nearDuration(got, base, time.Millisecond) {
		t.Fatalf("caller cancellation must not escalate; budget = %v, want %v", got, base)
	}
}

func TestTransportEscalatesOnBodyPhaseExpiry(t *testing.T) {
	tb := NewBudgetTable()
	ingestFitSamples(t, tb, "gpt-test")
	stub := &stubTransport{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       errBody{context.DeadlineExceeded},
	}}
	tr := budgetTransport(t, stub, tb, nil)
	base := transportBudget(t, tb, "gpt-test")
	resp, err := tr.RoundTrip(jsonRequest(t, "gpt-test"))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !errors.Is(readErr, context.DeadlineExceeded) {
		t.Fatalf("body read = %v, want the armed deadline error", readErr)
	}
	if got := transportBudget(t, tb, "gpt-test"); !nearDuration(got, 2*base, time.Millisecond) {
		t.Fatalf("mid-body expiry must escalate; budget = %v, want ~%v (×2)", got, 2*base)
	}
}

func TestTransportCleanEOFDoesNotEscalate(t *testing.T) {
	tb := NewBudgetTable()
	ingestFitSamples(t, tb, "gpt-test")
	stub := &stubTransport{}
	tr := budgetTransport(t, stub, tb, nil)
	base := transportBudget(t, tb, "gpt-test")
	resp, err := tr.RoundTrip(jsonRequest(t, "gpt-test"))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("body read: %v", err)
	}
	_ = resp.Body.Close()
	if got := transportBudget(t, tb, "gpt-test"); !nearDuration(got, base, time.Millisecond) {
		t.Fatalf("a completed response must not escalate; budget = %v, want %v", got, base)
	}
}

// errBody fails every read with the carried error.
type errBody struct{ err error }

func (b errBody) Read([]byte) (int, error) { return 0, b.err }
func (b errBody) Close() error             { return nil }

// captureHandler collects Debug records for assertions.
type captureHandler struct {
	mu       sync.Mutex
	messages []string
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, r.Message)
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) has(msg string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.messages {
		if m == msg {
			return true
		}
	}
	return false
}

func (h *captureHandler) all() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.messages...)
}

func TestTransportConcurrentUse(t *testing.T) {
	tb := NewBudgetTable()
	handler := &captureHandler{}
	stub := &stubTransport{}
	tr := budgetTransport(t, stub, tb, func(o *TransportOptions) {
		o.Logger = slog.New(handler)
	})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			model := "m" + strconv.Itoa(i%3)
			for j := range 25 {
				tb.Ingest(model, j*10, j*5, time.Duration(j+1)*time.Second)
				_ = tb.ResolveDeadline(ResolverInput{
					Class:           ClassLocal,
					Key:             model,
					BodyBytes:       j * 100,
					AdaptiveEnabled: true,
				})
				tb.Escalate(model)
				resp, err := tr.RoundTrip(jsonRequest(t, model))
				if err != nil {
					t.Errorf("RoundTrip: %v", err)
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}(i)
	}
	wg.Wait()
}
