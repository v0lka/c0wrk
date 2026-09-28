package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/embeddedllm"
	"github.com/v0lka/c0wrk/core/llmtls"
	"github.com/v0lka/c0wrk/core/proxy"
	"github.com/v0lka/sp4rk/llm"
)

// This file pins the wiring of the embedded provider's two ProviderEntry
// effects, both decided by the same name-scoped guard in
// providerEntryFromConfig: the ensure-loaded transport on
// ProviderEntry.HTTPClient, and the reasoning wire on
// ProviderEntry.ReasoningWire. The transport invariant it guards is the one
// llm-providers.md states for that hook: a client attached to an entry shadows
// RouterConfig.HTTPClient, so it MUST carry timeouts.llmRequestTimeout and
// never the 30 s web-fetch proxy budget.

// fakeEmbeddedLoader stands in for *embeddedllm.Server, counting the loads and
// activity marks the transport owes it.
type fakeEmbeddedLoader struct {
	mu    sync.Mutex
	loads int
	marks int
	err   error
}

func (l *fakeEmbeddedLoader) Load(context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.loads++
	return l.err
}

func (l *fakeEmbeddedLoader) MarkActivity() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.marks++
}

func (l *fakeEmbeddedLoader) counts() (loads, marks int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loads, l.marks
}

// embeddedProviderConfig is the shape backend/config generates for the embedded
// entry: a loopback base URL, no API key and no TLS pin (plain HTTP, so the
// ADR-054 pin does not apply).
func embeddedProviderConfig(baseURL string) BuilderProviderConfig {
	return BuilderProviderConfig{
		ProviderType: "openai",
		BaseURL:      baseURL,
		Models:       []string{"Bonsai 2 27B"},
	}
}

// The embedded entry gets a client that loads the model before the request and
// marks activity when the response completes — while inheriting the shared LLM
// client's timeout, NOT the proxy client's 30 s.
func TestProviderEntryFromConfig_EmbeddedGetsTheEnsureLoadedClient(t *testing.T) {
	const (
		llmTimeout   = 10 * time.Minute
		proxyTimeout = 30 * time.Second
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	t.Cleanup(srv.Close)

	shared := &http.Client{Timeout: llmTimeout, Transport: &http.Transport{}}
	proxyClient := &http.Client{Timeout: proxyTimeout, Transport: &http.Transport{}}
	loader := &fakeEmbeddedLoader{}
	embedded := BuilderEmbeddedLLMConfig{
		ProviderName:    "embedded",
		Loader:          loader,
		LoadWaitTimeout: 42 * time.Second,
	}

	entry := providerEntryFromConfig("embedded", embeddedProviderConfig(srv.URL+"/v1"),
		shared, proxyClient, proxy.BypassMatcher{}, embedded, llmBudgetWiring{}, identityExpand, nil)

	if entry.HTTPClient == nil {
		t.Fatal("HTTPClient = nil, want the ensure-loaded client (a cold model is not listening)")
	}
	if entry.HTTPClient == shared {
		t.Error("the shared LLM client must be cloned, not reused: it carries no ensure-loaded transport")
	}
	// The LLM budget is preserved but MOVED into the transport: a client-level
	// Timeout would also cover the cold-load wait, so the entry client carries
	// none and the transport arms the budget once the model is resident.
	if got := entry.HTTPClient.Timeout; got != 0 {
		t.Errorf("Timeout = %v, want 0 (the transport carries the budget instead)", got)
	}
	if _, ok := entry.HTTPClient.Transport.(*embeddedllm.EnsureLoadedTransport); !ok {
		t.Fatalf("Transport = %T, want *embeddedllm.EnsureLoadedTransport", entry.HTTPClient.Transport)
	}

	// And the client actually does the job: a cold request loads the model,
	// reaches the endpoint, and stamps activity when the response completes.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/v1/chat/completions", http.NoBody)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := entry.HTTPClient.Do(req)
	if err != nil {
		t.Fatalf("the request through the entry client must succeed: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing the response: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "choices") {
		t.Errorf("response = %d %q, want the endpoint's 200 body", resp.StatusCode, body)
	}
	loads, marks := loader.counts()
	if loads != 1 {
		t.Errorf("loads = %d, want 1 (the cold request must start the model)", loads)
	}
	if marks != 1 {
		t.Errorf("activity marks = %d, want 1 (the completed response must restart the idle budget)", marks)
	}
}

// Every other provider entry is untouched — including the "no override, let the
// SDK use the router-level client" answer the pin resolver gives. The guard is
// name-scoped, so an embedded supervisor can never decorate a cloud provider.
func TestProviderEntryFromConfig_EmbeddedGuardIsNameScoped(t *testing.T) {
	shared := &http.Client{Timeout: 10 * time.Minute}
	embedded := BuilderEmbeddedLLMConfig{ProviderName: "embedded", Loader: &fakeEmbeddedLoader{}}

	for _, name := range []string{"anthropic", "chatgpt", "selfhosted", "Embedded", ""} {
		entry := providerEntryFromConfig(name, embeddedProviderConfig("http://127.0.0.1:1/v1"),
			shared, nil, proxy.BypassMatcher{}, embedded, llmBudgetWiring{}, identityExpand, nil)
		if entry.HTTPClient != nil {
			t.Errorf("provider %q got a client (%T) — only the embedded entry may carry the transport",
				name, entry.HTTPClient.Transport)
		}
	}
}

// A configured provider name with no supervisor behind it (the posture until the
// app owns a *Server) must leave the entry exactly as the pin resolver built it.
func TestProviderEntryFromConfig_EmbeddedWithoutALoaderIsInert(t *testing.T) {
	shared := &http.Client{Timeout: 10 * time.Minute}

	entry := providerEntryFromConfig("embedded", embeddedProviderConfig("http://127.0.0.1:1/v1"),
		shared, nil, proxy.BypassMatcher{}, BuilderEmbeddedLLMConfig{ProviderName: "embedded"},
		llmBudgetWiring{}, identityExpand, nil)

	if entry.HTTPClient != nil {
		t.Errorf("HTTPClient = %T, want nil with no loader wired", entry.HTTPClient)
	}
	if (BuilderEmbeddedLLMConfig{ProviderName: "embedded"}).guards("embedded") {
		t.Error("a name with no loader must not guard anything")
	}
	if (BuilderEmbeddedLLMConfig{Loader: &fakeEmbeddedLoader{}}).guards("embedded") {
		t.Error("a loader with no provider name must not guard anything — core does not guess the name")
	}
	if !(BuilderEmbeddedLLMConfig{ProviderName: "embedded", Loader: &fakeEmbeddedLoader{}}).guards("embedded") {
		t.Error("both halves present must guard the named provider")
	}
}

// The resolution order is fixed: the TLS pin resolver decides first, the
// ensure-loaded transport only decorates its result — so a pinned embedded
// endpoint keeps its pin AND inherits the long LLM timeout.
func TestProviderEntryFromConfig_EmbeddedKeepsThePinResolution(t *testing.T) {
	srv, _ := newModelListServer(t, "Bonsai 2 27B")
	shared := &http.Client{Timeout: 10 * time.Minute, Transport: &http.Transport{}}
	loader := &fakeEmbeddedLoader{}
	embedded := BuilderEmbeddedLLMConfig{ProviderName: "embedded", Loader: loader}

	pc := embeddedProviderConfig(srv.URL + "/v1")
	pc.TLSFingerprint = pinFixture // a well-formed pin that matches no certificate

	entry := providerEntryFromConfig("embedded", pc, shared, nil, proxy.BypassMatcher{}, embedded, llmBudgetWiring{}, identityExpand, nil)

	if entry.HTTPClient == nil {
		t.Fatal("HTTPClient = nil, want the pinned client wrapped by the ensure-loaded transport")
	}
	if _, ok := entry.HTTPClient.Transport.(*embeddedllm.EnsureLoadedTransport); !ok {
		t.Fatalf("Transport = %T, want the ensure-loaded wrapper outermost", entry.HTTPClient.Transport)
	}
	if got := entry.HTTPClient.Timeout; got != 0 {
		t.Errorf("Timeout = %v, want 0 — the budget moves into the transport so the "+
			"cold-load wait is not charged to it", got)
	}

	// The pin is still enforced UNDER the wrapper: a mismatching pin must fail
	// the handshake rather than being silently dropped by the layering.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/v1/models", http.NoBody)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := entry.HTTPClient.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("a mismatching pin must fail closed even with the ensure-loaded transport installed")
	}
	if !errors.Is(err, llmtls.ErrPinMismatch) && !strings.Contains(err.Error(), llmtls.ErrPinMismatch.Error()) {
		t.Errorf("err = %v, want a fingerprint mismatch", err)
	}
	if loads, _ := loader.counts(); loads != 1 {
		t.Errorf("loads = %d, want 1 — the model is loaded before the dial, whatever the dial then does", loads)
	}
}

// The embedded guard is also the ONLY selector of the reasoning wire. The
// supervised server is the pinned PrismML-Eng/llama.cpp fork, which reads
// enable_thinking exclusively from chat_template_kwargs — a top-level field is
// silently ignored there, so the entry must opt into that spelling or "Off"
// would not turn thinking off. Every sibling keeps the vendor-default top-level
// spelling, which is what LM Studio/vLLM/Ollama/DashScope read.
func TestProviderEntryFromConfig_EmbeddedSelectsTheChatTemplateKwargsWire(t *testing.T) {
	shared := &http.Client{Timeout: 10 * time.Minute}
	embedded := BuilderEmbeddedLLMConfig{ProviderName: "embedded", Loader: &fakeEmbeddedLoader{}}

	entry := providerEntryFromConfig("embedded", embeddedProviderConfig("http://127.0.0.1:1/v1"),
		shared, nil, proxy.BypassMatcher{}, embedded, llmBudgetWiring{}, identityExpand, nil)

	if entry.ReasoningWire != llm.ReasoningWireChatTemplateKwargs {
		t.Errorf("embedded ReasoningWire = %q, want %q", entry.ReasoningWire, llm.ReasoningWireChatTemplateKwargs)
	}

	// The wire follows the supervisor, never the name or the URL shape: a user
	// provider pointed at the very same loopback llama-server keeps the vendor
	// default, because c0wrk does not know which binary answers there.
	for _, name := range []string{"anthropic", "chatgpt", "selfhosted", "lmstudio", "Embedded", ""} {
		sibling := providerEntryFromConfig(name, embeddedProviderConfig("http://127.0.0.1:1/v1"),
			shared, nil, proxy.BypassMatcher{}, embedded, llmBudgetWiring{}, identityExpand, nil)
		if sibling.ReasoningWire != llm.ReasoningWireVendorDefault {
			t.Errorf("provider %q ReasoningWire = %q, want the zero value %q",
				name, sibling.ReasoningWire, llm.ReasoningWireVendorDefault)
		}
	}

	// A configured name with no supervisor behind it (the posture before an
	// install and after a Remove) must not select the wire either — the guard
	// is inert as a whole, so a stale embedded_llm.provider_name cannot change
	// the request body of an unrelated provider.
	inert := providerEntryFromConfig("embedded", embeddedProviderConfig("http://127.0.0.1:1/v1"),
		shared, nil, proxy.BypassMatcher{}, BuilderEmbeddedLLMConfig{ProviderName: "embedded"},
		llmBudgetWiring{}, identityExpand, nil)
	if inert.ReasoningWire != llm.ReasoningWireVendorDefault {
		t.Errorf("ReasoningWire without a loader = %q, want the vendor default %q",
			inert.ReasoningWire, llm.ReasoningWireVendorDefault)
	}
}
