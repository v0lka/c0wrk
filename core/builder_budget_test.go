package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/embeddedllm"
	"github.com/v0lka/c0wrk/core/llmbudget"
	"github.com/v0lka/c0wrk/core/llmtls"
	"github.com/v0lka/c0wrk/core/proxy"
	"github.com/v0lka/sp4rk/llm"
)

// This file pins the wiring of the adaptive per-model LLM request budget
// (ADR-071) into providerEntryFromConfig: the budget RoundTripper sits on TOP
// of the llmtls pin and BENEATH the embedded ensure-loaded gate, the entry
// clones carry Client.Timeout = 0 (arming is the budget transport's job), the
// kill-switch-off posture is byte-for-byte the pre-ADR-071 shape, and the
// per-session BudgetTable is the one shared by the session's UsageTracker
// observer and every entry transport.

// adaptiveWiring returns the enabled wiring with the given table and global
// override — the shape buildRouter derives from an adaptive-on config.
func adaptiveWiring(table *llmbudget.BudgetTable, global time.Duration) llmBudgetWiring {
	return llmBudgetWiring{
		enabled:   true,
		table:     table,
		global:    global,
		overrides: map[string]BuilderModelOverride{},
	}
}

// budgetSpyTransport records what the budget transport handed DOWNWARD. The
// armed clone's context is the only place the client-side deadline is
// observable — the server sees a fresh context of its own — so the spy sits
// directly beneath the budget wrapper (the wiring clones the shared client's
// transport, which is the spy) and timestamps every hand-off.
type budgetSpyTransport struct {
	inner http.RoundTripper

	mu          sync.Mutex
	armedAt     time.Time
	deadline    time.Time
	hasDeadline bool
}

func (s *budgetSpyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.armedAt = time.Now()
	s.deadline, s.hasDeadline = req.Context().Deadline()
	s.mu.Unlock()
	inner := s.inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	return inner.RoundTrip(req)
}

func (s *budgetSpyTransport) observed() (armedAt, deadline time.Time, hasDeadline bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.armedAt, s.deadline, s.hasDeadline
}

// budgetWiringFromConfig gates the wiring: the kill-switch AND a session
// table must both be present, and the global override is carried only when
// positive (0 = "no opinion").
func TestBudgetWiringFromConfig_Gating(t *testing.T) {
	table := llmbudget.NewBudgetTable()
	enabledCfg := &BuilderConfig{
		Timeouts: BuilderTimeoutsConfig{AdaptiveBudgetEnabled: true},
		LLM:      BuilderLLMConfig{Models: map[string]BuilderModelOverride{"m": {RequestTimeout: 60}}},
	}
	if w := budgetWiringFromConfig(enabledCfg, table); !w.enabled || w.table != table || w.global != 0 {
		t.Errorf("adaptive on + table → enabled=%v table=%v global=%v, want enabled with the table and no global", w.enabled, w.table != nil, w.global)
	}
	enabledCfg.Timeouts.LLMRequestTimeout = 300
	if w := budgetWiringFromConfig(enabledCfg, table); w.global != 300*time.Second {
		t.Errorf("global = %v, want the positive override carried as a duration", w.global)
	}
	enabledCfg.Timeouts.LLMRequestTimeout = -5
	if w := budgetWiringFromConfig(enabledCfg, table); w.global != 0 {
		t.Errorf("global = %v, want 0 for a non-positive override (no opinion)", w.global)
	}
	// Kill-switch off → disabled regardless of the table.
	offCfg := &BuilderConfig{Timeouts: BuilderTimeoutsConfig{LLMRequestTimeout: 300}}
	if w := budgetWiringFromConfig(offCfg, table); w.enabled {
		t.Error("kill-switch off must disable the wiring")
	}
	// Builder-level router (nil table) → disabled even with the kill-switch on.
	if w := budgetWiringFromConfig(enabledCfg, nil); w.enabled {
		t.Error("nil table must disable the wiring (no tracker feeds it)")
	}
}

// The legacy 0→600 semantics live at the client layer too: with the kill
// switch off and no opinion configured, the shared client carries the same
// 600 s the pre-ADR-071 default coerced into config.
func TestBuildLLMHTTPClient_ZeroIsLegacySixHundred(t *testing.T) {
	if got := buildLLMHTTPClient(nil, 0).Timeout; got != 600*time.Second {
		t.Errorf("buildLLMHTTPClient(nil, 0).Timeout = %v, want the legacy 600 s", got)
	}
}

// The kill-switch off must leave every entry byte-for-byte as before: no
// budget wrapper anywhere, pinned clients keep their inherited timeout, the
// load-bearing nil survives.
func TestProviderEntryFromConfig_AdaptiveDisabledLeavesEntriesUntouched(t *testing.T) {
	shared := buildLLMHTTPClient(nil, 600)
	pc := BuilderProviderConfig{
		ProviderType:   "openai",
		BaseURL:        "https://llm.lan:8443/v1",
		Models:         []string{"m"},
		TLSFingerprint: pinFixture,
	}
	entry := providerEntryFromConfig("selfhosted", pc, shared, nil, proxy.BypassMatcher{},
		BuilderEmbeddedLLMConfig{}, llmBudgetWiring{}, BuilderSubscriptionAuthConfig{}, identityExpand, nil)
	if entry.HTTPClient == nil {
		t.Fatal("HTTPClient = nil, want the pinned client")
	}
	if entry.HTTPClient.Transport == nil || strings.Contains(fmt.Sprintf("%T", entry.HTTPClient.Transport), "llmbudget") {
		t.Errorf("Transport = %T, want the pinned transport with no budget wrapper", entry.HTTPClient.Transport)
	}
	if entry.HTTPClient.Timeout != shared.Timeout {
		t.Errorf("Timeout = %v, want the inherited %v", entry.HTTPClient.Timeout, shared.Timeout)
	}

	// No pin → the load-bearing nil (the SDK falls back to the router client).
	plain := BuilderProviderConfig{ProviderType: "openai", BaseURL: "https://api.example.com/v1", Models: []string{"m"}}
	entry = providerEntryFromConfig("chatgpt", plain, shared, nil, proxy.BypassMatcher{},
		BuilderEmbeddedLLMConfig{}, llmBudgetWiring{}, BuilderSubscriptionAuthConfig{}, identityExpand, nil)
	if entry.HTTPClient != nil {
		t.Errorf("HTTPClient = %T, want nil (kill-switch off must not materialize a client)", entry.HTTPClient)
	}
}

// The budget wrapper must keep the pinned transport BENEATH it: llmtls needs
// a concrete *http.Transport under itself, and the pin is the only
// verification the endpoint gets. The entry clone also carries Timeout = 0 —
// arming moved to the budget transport.
func TestProviderEntryFromConfig_AdaptiveWrapsPinUnderBudget(t *testing.T) {
	shared := buildLLMHTTPClient(nil, 600)
	pc := BuilderProviderConfig{
		ProviderType:   "openai",
		BaseURL:        "https://llm.lan:8443/v1",
		Models:         []string{"m"},
		TLSFingerprint: pinFixture,
	}
	control := llmtls.RouterEntryClient(llmtls.ZeroDialPolicy, shared, pc.TLSFingerprint, nil)
	entry := providerEntryFromConfig("selfhosted", pc, shared, nil, proxy.BypassMatcher{},
		BuilderEmbeddedLLMConfig{}, adaptiveWiring(llmbudget.NewBudgetTable(), 0), BuilderSubscriptionAuthConfig{}, identityExpand, nil)

	if entry.HTTPClient == nil {
		t.Fatal("HTTPClient = nil, want the budget-wrapped pinned client")
	}
	if entry.HTTPClient.Timeout != 0 {
		t.Errorf("Timeout = %v, want 0 (the budget transport arms instead)", entry.HTTPClient.Timeout)
	}
	bt, ok := entry.HTTPClient.Transport.(*llmbudget.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *llmbudget.Transport outermost", entry.HTTPClient.Transport)
	}
	if got, want := fmt.Sprintf("%T", bt.Base()), fmt.Sprintf("%T", control.Transport); got != want {
		t.Errorf("transport beneath the budget = %s, want the pinned transport %s", got, want)
	}
	// The clone must not have mutated the client the control resolution built:
	// the pinned client keeps its inherited timeout.
	if control.Timeout != shared.Timeout {
		t.Errorf("control Timeout = %v, want the untouched %v", control.Timeout, shared.Timeout)
	}
}

// While no pin applies (proxy dials or plain endpoint) the load-bearing nil is
// the pre-adaptive answer — the SDK falls back to RouterConfig.HTTPClient,
// which carries no budget transport. With the kill-switch on, the wiring must
// therefore build an EXPLICIT clone of the shared client that preserves the
// proxy transport and replaces the fixed timeout with the budget arming.
func TestProviderEntryFromConfig_AdaptiveNilPathClonesSharedClient(t *testing.T) {
	proxyTransport := &http.Transport{}
	proxyClient := &http.Client{Transport: proxyTransport}
	shared := buildLLMHTTPClient(proxyClient, 600)
	plain := BuilderProviderConfig{ProviderType: "openai", BaseURL: "https://api.example.com/v1", Models: []string{"m"}}
	entry := providerEntryFromConfig("chatgpt", plain, shared, proxyClient, proxy.BypassMatcher{},
		BuilderEmbeddedLLMConfig{}, adaptiveWiring(llmbudget.NewBudgetTable(), 0), BuilderSubscriptionAuthConfig{}, identityExpand, nil)

	if entry.HTTPClient == nil {
		t.Fatal("HTTPClient = nil, want an explicit clone (nil would lose the budget arming)")
	}
	if entry.HTTPClient == shared {
		t.Fatal("HTTPClient is the shared client itself — the wiring must clone, never mutate")
	}
	if entry.HTTPClient.Timeout != 0 {
		t.Errorf("Timeout = %v, want 0 (the budget transport arms instead)", entry.HTTPClient.Timeout)
	}
	bt, ok := entry.HTTPClient.Transport.(*llmbudget.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *llmbudget.Transport outermost", entry.HTTPClient.Transport)
	}
	if bt.Base() != http.RoundTripper(proxyTransport) {
		t.Errorf("transport beneath the budget = %T, want the shared proxy transport %T",
			bt.Base(), shared.Transport)
	}
}

// slowLoader is a fakeEmbeddedLoader whose Load takes `delay` — a stand-in for
// a cold weight load the request budget must NOT be charged with (D13).
type slowLoader struct {
	fakeEmbeddedLoader
	delay time.Duration
}

func (l *slowLoader) Load(ctx context.Context) error {
	time.Sleep(l.delay)
	return l.fakeEmbeddedLoader.Load(ctx)
}

// trainedTable returns a table whose fit is rank-sufficient and exact: every
// sample satisfies duration = 0.5s/token·in + 0.4s/token·out with in and out
// deliberately NOT proportional (a proportional pair is rank-deficient and
// drops the estimator to its percentile rung). The p85 pricing sees ratio 1
// for every sample, so the rates survive pricing unchanged.
func trainedTable(t *testing.T) *llmbudget.BudgetTable {
	t.Helper()
	table := llmbudget.NewBudgetTable()
	samples := []struct {
		in, out int
		d       time.Duration
	}{
		{1000, 500, 700 * time.Second},
		{2000, 250, 1100 * time.Second},
		{500, 1000, 650 * time.Second},
		{1500, 750, 1050 * time.Second},
	}
	for _, s := range samples {
		table.Ingest("m", s.in, s.out, s.d)
	}
	return table
}

// budgetBody builds a chat-completions-shaped request body of ~3000 bytes, so
// est_in = ceil(bytes/3) = 1000 input tokens. The envelope around the padding
// costs 52 bytes ({"model":"m","messages":[{"role":"user","content":"…"}]}),
// and the range check below pins the total.
func budgetBody(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model": "m",
		"messages": []map[string]string{
			{"role": "user", "content": strings.Repeat("x", 3000-52)},
		},
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	if len(body) < 2997 || len(body) > 3003 {
		t.Fatalf("body = %d bytes, want ~3000 so est_in = 1000", len(body))
	}
	return body
}

// The embedded entry under an adaptive budget trained past 600 s must NOT be
// capped by the fixed post-readiness budget the ensure-loaded gate used to
// arm: the wiring zeroes the entry clone's Timeout, so the gate arms nothing
// and the budget transport below arms the trained deadline — AFTER the gate,
// so a cold load is never charged to it (D13).
//
// The trained budget here is ~909.6 s (0.5·1000 + 0.4·1024, embedded class
// ceiling 1800 s). The assertions, all read off the spy beneath the budget
// wrapper:
//
//   - the armed deadline is ≈ the trained budget, NOT 600 s — a failed
//     suppression would leave the gate's fixed 600 s as the only deadline
//     (the budget transport skips arming when one exists);
//   - the arming moment sits ≈ the load delay after request start — the cold
//     load stayed outside the budget (D13).
func TestProviderEntryFromConfig_AdaptiveEmbeddedNotCappedAtFixedBudget(t *testing.T) {
	const loadDelay = 5 * time.Second
	table := trainedTable(t)
	loader := &slowLoader{delay: loadDelay}
	embedded := BuilderEmbeddedLLMConfig{
		ProviderName:    "embedded",
		Loader:          loader,
		LoadWaitTimeout: 30 * time.Second,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	// The pre-adaptive shared client: the fixed 600 s budget the legacy
	// posture carried, with a spy beneath it to observe the hand-off. Under
	// the wiring the entry clone drops the fixed budget and the spy lands
	// directly beneath the budget wrapper (gate → budget → spy → dial).
	spy := &budgetSpyTransport{}
	shared := &http.Client{Timeout: 600 * time.Second, Transport: spy}
	pc := BuilderProviderConfig{ProviderType: "openai", BaseURL: srv.URL, Models: []string{"m"}}
	entry := providerEntryFromConfig("embedded", pc, shared, nil, proxy.BypassMatcher{},
		embedded, adaptiveWiring(table, 0), BuilderSubscriptionAuthConfig{}, identityExpand, nil)

	// Outermost = the ensure-loaded gate; the budget transport beneath it.
	if _, ok := entry.HTTPClient.Transport.(*embeddedllm.EnsureLoadedTransport); !ok {
		t.Fatalf("Transport = %T, want the ensure-loaded gate outermost", entry.HTTPClient.Transport)
	}

	// The budget the resolver computes for exactly this request — computed
	// through the same table the wiring handed the transport.
	body := budgetBody(t)
	expectedBudget := table.ResolveDeadline(llmbudget.ResolverInput{
		Class:           llmbudget.Classify("embedded", srv.URL, ""),
		Key:             "m",
		BodyBytes:       len(body),
		AdaptiveEnabled: true,
	})
	if expectedBudget <= 600*time.Second {
		t.Fatalf("expected trained budget %v must exceed the legacy 600 s for this scenario to mean anything", expectedBudget)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := entry.HTTPClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)

	armedAt, deadline, hasDeadline := spy.observed()
	if !hasDeadline {
		t.Fatal("the request handed down carried no deadline — the stalled-upstream invariant is lost")
	}
	budget := deadline.Sub(armedAt)
	if budget <= 650*time.Second {
		t.Errorf("armed budget = %v, want ≈ the trained budget %v (a ~600 s budget means the fixed post-readiness arming was NOT suppressed)", budget, expectedBudget)
	}
	if budget > expectedBudget+2*time.Second {
		t.Errorf("armed budget = %v, want ≈ %v (the class ceiling must not be exceeded)", budget, expectedBudget)
	}
	// D13: the deadline was armed only once the model was resident —
	// armedAt ≈ start + load, so (deadline − start) ≈ load + budget and NOT
	// ≈ budget (which would mean the load was charged to the request).
	gap := deadline.Sub(start)
	if gap < loadDelay+expectedBudget-time.Second || gap > loadDelay+expectedBudget+time.Second {
		t.Errorf("deadline armed %v after request start, want ≈ load(%v) + budget(%v) — the cold load must stay outside the budget (D13)",
			gap, loadDelay, expectedBudget)
	}
	if loads, _ := loader.counts(); loads != 1 {
		t.Errorf("loader loads = %d, want 1", loads)
	}
}

// A caller that owns its deadline — the one-shot service path
// (serviceLLMRequestTimeout) — must pass through the budget transport without
// a second arming: with the table untrained the transport would otherwise
// replace a 5 s service budget with the 600 s warmup. The spy beneath the
// wrapper must see the caller's own deadline, handed down untouched.
func TestProviderEntryFromConfig_AdaptiveSkipsArmingWhenCallerOwnsDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	spy := &budgetSpyTransport{}
	shared := &http.Client{Timeout: 600 * time.Second, Transport: spy}
	entry := providerEntryFromConfig("chatgpt",
		BuilderProviderConfig{ProviderType: "openai", BaseURL: srv.URL, Models: []string{"m"}},
		shared, nil, proxy.BypassMatcher{}, BuilderEmbeddedLLMConfig{},
		adaptiveWiring(llmbudget.NewBudgetTable(), 0), BuilderSubscriptionAuthConfig{}, identityExpand, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	callerDeadline, _ := ctx.Deadline()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/chat/completions",
		bytes.NewReader([]byte(`{"model":"m"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := entry.HTTPClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()

	_, deadline, hasDeadline := spy.observed()
	if !hasDeadline || deadline.Sub(callerDeadline).Abs() > 100*time.Millisecond {
		t.Errorf("handed-down deadline = %v (caller's %v) — the budget transport must pass a caller-owned deadline through untouched, not re-arm (the untrained table would have imposed the 600 s warmup)",
			deadline, callerDeadline)
	}
}

// A positive global llmRequestTimeout is a FIXED override under the
// kill-switch-on regime (ADR-071 D5): the armed deadline is exactly the
// override — not the 600 s warmup of an untrained table.
func TestProviderEntryFromConfig_AdaptiveGlobalOverrideIsFixed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	spy := &budgetSpyTransport{}
	shared := &http.Client{Timeout: 600 * time.Second, Transport: spy}
	entry := providerEntryFromConfig("chatgpt",
		BuilderProviderConfig{ProviderType: "openai", BaseURL: srv.URL, Models: []string{"m"}},
		shared, nil, proxy.BypassMatcher{}, BuilderEmbeddedLLMConfig{},
		adaptiveWiring(llmbudget.NewBudgetTable(), 120*time.Second), BuilderSubscriptionAuthConfig{}, identityExpand, nil)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/chat/completions",
		bytes.NewReader([]byte(`{"model":"m"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := entry.HTTPClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()

	armedAt, deadline, hasDeadline := spy.observed()
	if !hasDeadline {
		t.Fatal("no deadline armed — a fixed global override must still arm")
	}
	if budget := deadline.Sub(armedAt); budget < 110*time.Second || budget > 120*time.Second {
		t.Errorf("armed budget = %v, want the fixed 120 s override (no warmup, no escalation)", budget)
	}
}

// The per-session table is shared BY REFERENCE between the wiring and the
// entry transports: training the table the router was built with must move
// the budgets the (already built) entries arm — warmup 600 s before the first
// samples, the trained budget after.
func TestProviderEntryFromConfig_AdaptiveSharesTheSessionTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	spy := &budgetSpyTransport{}
	shared := &http.Client{Timeout: 600 * time.Second, Transport: spy}
	table := llmbudget.NewBudgetTable()
	entry := providerEntryFromConfig("chatgpt",
		BuilderProviderConfig{ProviderType: "openai", BaseURL: srv.URL, Models: []string{"m"}},
		shared, nil, proxy.BypassMatcher{}, BuilderEmbeddedLLMConfig{},
		adaptiveWiring(table, 0), BuilderSubscriptionAuthConfig{}, identityExpand, nil)

	do := func(body []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/chat/completions",
			bytes.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := entry.HTTPClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		_ = resp.Body.Close()
	}

	do([]byte(`{"model":"m"}`))
	if armedAt, deadline, ok := spy.observed(); !ok {
		t.Fatal("no deadline armed on the first request")
	} else if budget := deadline.Sub(armedAt); budget < 590*time.Second || budget > 600*time.Second {
		t.Fatalf("untrained armed budget = %v, want the 600 s warmup", budget)
	}

	// Train past the warmup through the SAME table (as the session's
	// UsageTracker observer would) and re-arm.
	table.Ingest("m", 1000, 500, 700*time.Second)
	table.Ingest("m", 2000, 250, 1100*time.Second)
	table.Ingest("m", 500, 1000, 650*time.Second)
	table.Ingest("m", 1500, 750, 1050*time.Second)
	// The trained budget is size-sensitive, so the re-request carries the
	// same ~3000-byte body the expectation is computed from.
	body := budgetBody(t)
	expected := table.ResolveDeadline(llmbudget.ResolverInput{
		Class:           llmbudget.Classify("chatgpt", srv.URL, ""),
		Key:             "m",
		BodyBytes:       len(body),
		AdaptiveEnabled: true,
	})
	do(body)
	armedAt, deadline, ok := spy.observed()
	if !ok {
		t.Fatal("no deadline armed on the re-request")
	}
	// The transport armed `expected` at the hand-off the spy timestamped.
	if budget := deadline.Sub(armedAt); budget < expected-2*time.Second || budget > expected+time.Second {
		t.Errorf("trained armed budget = %v, want ≈ %v — the entry transport must read the session table, not a private one", budget, expected)
	}
}

// attemptStubTransport sleeps a fixed delay and replies OK — the provider
// attempt the budget transport measures.
type attemptStubTransport struct{ delay time.Duration }

func (s attemptStubTransport) RoundTrip(*http.Request) (*http.Response, error) {
	time.Sleep(s.delay)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     make(http.Header),
	}, nil
}

// budgetIngestCaller must ingest one sample per successful call, keyed by the
// model the provider served, with the duration of the SINGLE provider attempt
// that produced the response — not the whole router call, which may wrap a
// retry and its backoff (ADR-071 D1/D9). The fake inner caller sleeps a long
// "backoff" around a short provider attempt, so the sample's duration has to
// track the attempt: the transport's recorder must win over the caller's clock.
func TestBudgetIngestCallerFeedsTableWithAttemptDuration(t *testing.T) {
	table := llmbudget.NewBudgetTable()
	const attempt = 20 * time.Millisecond
	tr := llmbudget.NewTransport(attemptStubTransport{delay: attempt}, llmbudget.TransportOptions{
		Table:           table,
		Class:           llmbudget.ClassRemote,
		Wire:            llmbudget.WireJSONModel,
		ProviderName:    "prov",
		AdaptiveEnabled: true,
	})
	client := &http.Client{Transport: tr}

	inner := &mockLLMCaller{callFn: func(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
		time.Sleep(200 * time.Millisecond) // simulated router retry backoff
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"https://api.example.com/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return &llm.ChatResponse{Model: "m", Usage: llm.TokenUsage{InputTokens: 42, OutputTokens: 7}}, nil
	}}

	caller := &budgetIngestCaller{inner: inner, table: table}
	if _, err := caller.Call(context.Background(), llm.ChatRequest{}); err != nil {
		t.Fatalf("Call: %v", err)
	}

	samples := table.Samples("m")
	if len(samples) != 1 {
		t.Fatalf("ingested samples = %d, want 1 (keyed by the served model)", len(samples))
	}
	if samples[0].Duration >= 100*time.Millisecond {
		t.Fatalf("sample duration = %v, want the ~%v provider attempt, not the router call incl. backoff", samples[0].Duration, attempt)
	}
	if samples[0].Duration <= 0 {
		t.Fatalf("sample duration = %v, want positive", samples[0].Duration)
	}
}

// A failed call ingests nothing (the feed is successful-calls-only); without a
// transport recorder the caller still ingests, falling back to its own
// wall-clock measurement (the adaptive-off / pass-through path).
func TestBudgetIngestCallerSkipsFailureAndFallsBack(t *testing.T) {
	table := llmbudget.NewBudgetTable()
	failing := &budgetIngestCaller{inner: &mockLLMCaller{err: errors.New("upstream down")}, table: table}
	if _, err := failing.Call(context.Background(), llm.ChatRequest{}); err == nil {
		t.Fatal("expected the inner error")
	}
	if got := table.SampleCount("m"); got != 0 {
		t.Fatalf("SampleCount = %d after a failed call, want 0", got)
	}

	// The fallback path measures the call with the WALL CLOCK, so the mock must
	// occupy measurable time: an instant reply measures as 0 on the coarse
	// Windows runners and the table then drops it as an unmeasured sample
	// (Ingest ignores a non-positive duration). This is a property of the mock,
	// not of the fallback logic under test.
	fallback := &budgetIngestCaller{
		inner: &mockLLMCaller{callFn: func(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
			time.Sleep(20 * time.Millisecond)
			return &llm.ChatResponse{Model: "m", Usage: llm.TokenUsage{InputTokens: 5, OutputTokens: 5}}, nil
		}},
		table: table,
	}
	if _, err := fallback.Call(context.Background(), llm.ChatRequest{}); err != nil {
		t.Fatalf("fallback Call: %v", err)
	}
	if got := table.SampleCount("m"); got != 1 {
		t.Fatalf("SampleCount = %d, want 1 on the fallback path", got)
	}
}

// budgetWire picks the gemini URL extraction only when EVERY enabled model of
// the provider carries an explicit google protocol override — local google
// checkpoints are remapped to chat_completions before the entry is built, so
// the JSON wire stays the default.
func TestBudgetWireSelection(t *testing.T) {
	pc := BuilderProviderConfig{ProviderType: "openai", Models: []string{"gemma", "m2"}}
	if got := budgetWire(pc, nil); got != llmbudget.WireJSONModel {
		t.Errorf("no overrides → wire %v, want JSON", got)
	}
	google := string(llm.ProtocolGoogle)
	overrides := map[string]BuilderModelOverride{
		"gemma": {Protocol: google},
		"m2":    {Protocol: google},
	}
	if got := budgetWire(pc, overrides); got != llmbudget.WireURLModel {
		t.Errorf("all-google provider → wire %v, want URL", got)
	}
	overrides["m2"] = BuilderModelOverride{}
	if got := budgetWire(pc, overrides); got != llmbudget.WireJSONModel {
		t.Errorf("mixed provider → wire %v, want JSON (google-wire requests degrade to the provider-level key)", got)
	}
}
