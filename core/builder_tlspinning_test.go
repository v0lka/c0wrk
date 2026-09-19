package core

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/llmtls"
	"github.com/v0lka/c0wrk/core/proxy"
)

// pinFixture is a well-formed (base64 of 32 bytes) pin that matches no real
// certificate.
const pinFixture = "k3J9vQ1Z0mF7hD2xS8pL4wR6tY5uI3oP1aE9cX0bN7g="

func identityExpand(s string) string { return s }

// newModelListServer starts an HTTPS server answering the OpenAI-compatible
// /v1/models listing, and returns it plus the SPKI pin of its certificate.
func newModelListServer(t *testing.T, ids ...string) (srv *httptest.Server, spkiPin string) {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(`{"object":"list","data":[`)
	for i, id := range ids {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"id":"` + id + `","object":"model","owned_by":"test"}`)
	}
	sb.WriteString(`]}`)
	body := sb.String()

	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	// Pin-mismatch cases deliberately provoke rejected handshakes.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)

	cert := srv.Certificate()
	if cert == nil {
		t.Fatal("test server presented no certificate")
	}
	return srv, llmtls.SPKIFingerprint(cert)
}

// ---------------------------------------------------------------------------
// Router entries
// ---------------------------------------------------------------------------

// providerEntryFromConfig must attach a pinned client ONLY when a pin is set
// and the proxy dials for the provider's endpoint (proxy active AND the host
// not bypassed). While the proxy dials the entry stays nil so the SDK falls
// back to the router-level client — which carries both the proxy transport
// and the long LLM request timeout; attaching the proxy client here would
// cap inference at the much shorter proxy timeout. A bypassed host dials
// directly, so its pin applies (ADR-054, re-armed by proxy.bypass_list).
func TestProviderEntryFromConfig_PinAndProxyMatrix(t *testing.T) {
	const llmTimeout = 10 * time.Minute
	llmClient := &http.Client{Timeout: llmTimeout}

	tests := []struct {
		name        string
		pin         string
		proxyClient *http.Client
		bypass      []string
		wantClient  bool
	}{
		{"no proxy, pin set", pinFixture, nil, nil, true},
		{"no proxy, no pin", "", nil, nil, false},
		{"proxy dials, pin set", pinFixture, proxyPlaceholder, nil, false},
		{"proxy dials, no pin", "", proxyPlaceholder, nil, false},
		{"proxy bypassed, pin set", pinFixture, proxyPlaceholder, []string{"llm.lan"}, true},
		{"proxy bypassed, no pin", "", proxyPlaceholder, []string{"llm.lan"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pc := BuilderProviderConfig{
				ProviderType:   "openai",
				APIKey:         "key",
				BaseURL:        "https://llm.lan:8443/v1",
				Models:         []string{"qwen3"},
				TLSFingerprint: tc.pin,
			}

			entry := providerEntryFromConfig("selfhosted", pc, llmClient, tc.proxyClient, proxy.NewBypassMatcher(tc.bypass), identityExpand, nil)

			if entry.Name != "selfhosted" || entry.ProviderType != "openai" ||
				entry.APIKey != "key" || entry.BaseURL != "https://llm.lan:8443/v1" {
				t.Errorf("entry identity/credentials wrong: %+v", entry)
			}
			if !tc.wantClient {
				if entry.HTTPClient != nil {
					t.Fatalf("HTTPClient = %p, want nil (the router-level client must dial)", entry.HTTPClient)
				}
				return
			}
			if entry.HTTPClient == nil {
				t.Fatal("HTTPClient = nil, want a pinned client")
			}
			if entry.HTTPClient == llmClient {
				t.Error("the pinned client must be a distinct clone of the shared LLM client")
			}
			if entry.HTTPClient.Timeout != llmTimeout {
				t.Errorf("pinned client Timeout = %v, want %v (the LLM request timeout must be inherited)",
					entry.HTTPClient.Timeout, llmTimeout)
			}
		})
	}
}

// proxyPlaceholder stands in for a non-nil proxy client in the matrix above;
// its value is never dereferenced by providerEntryFromConfig, only checked
// for nil (the effective-proxy rule).
var proxyPlaceholder = &http.Client{}

// providerEntryFromConfig must expand ${VAR} in credentials like every other
// dial path, and must not invent models.
func TestProviderEntryFromConfig_ExpandsEnvVars(t *testing.T) {
	expand := func(s string) string {
		switch s {
		case "${KEY}":
			return "secret"
		case "${URL}":
			return "https://llm.lan:8443/v1"
		}
		return s
	}
	pc := BuilderProviderConfig{
		ProviderType: "openai",
		APIKey:       "${KEY}",
		BaseURL:      "${URL}",
		Models:       []string{"qwen3"},
	}

	entry := providerEntryFromConfig("selfhosted", pc, nil, nil, proxy.BypassMatcher{}, expand, nil)

	if entry.APIKey != "secret" {
		t.Errorf("APIKey = %q, want expanded 'secret'", entry.APIKey)
	}
	if entry.BaseURL != "https://llm.lan:8443/v1" {
		t.Errorf("BaseURL = %q, want expanded URL", entry.BaseURL)
	}
	if len(entry.Models) != 1 || entry.Models[0] != "qwen3" {
		t.Errorf("Models = %v, want [qwen3]", entry.Models)
	}
}

// A pinned entry built from a nil shared client must still work (the pin is
// the switch; the shared client is only a source of transport/timeout
// settings).
func TestProviderEntryFromConfig_NilSharedClient(t *testing.T) {
	pc := BuilderProviderConfig{ProviderType: "openai", Models: []string{"m"}, TLSFingerprint: pinFixture}

	entry := providerEntryFromConfig("selfhosted", pc, nil, nil, proxy.BypassMatcher{}, identityExpand, nil)

	if entry.HTTPClient == nil {
		t.Fatal("HTTPClient = nil, want a pinned client even without a shared base client")
	}
	if entry.HTTPClient.Transport == nil {
		t.Error("pinned client carries no transport")
	}
}

// ---------------------------------------------------------------------------
// Fetch Models listing
// ---------------------------------------------------------------------------

func newListingBuilder(proxyClient *http.Client) *OrchestratorBuilder {
	b := &OrchestratorBuilder{}
	b.proxyClient = proxyClient
	return b
}

func listingConfig(providerName, baseURL, pin, providerType string) *BuilderConfig {
	return &BuilderConfig{
		LLM: BuilderLLMConfig{
			ProviderConfigs: map[string]BuilderProviderConfig{
				providerName: {
					ProviderType:   providerType,
					APIKey:         "key",
					BaseURL:        baseURL,
					Models:         []string{"qwen3"},
					TLSFingerprint: pin,
				},
			},
		},
		ExpandEnvVars: identityExpand,
	}
}

// With no proxy, a correct pin lets the Fetch Models listing reach a
// self-signed endpoint — the same reachability the chat path gets.
func TestFetchProviderModels_PinnedListingSucceeds(t *testing.T) {
	srv, pin := newModelListServer(t, "qwen3", "llama-3.1-8b")
	b := newListingBuilder(nil)
	cfg := listingConfig("selfhosted", srv.URL+"/v1", pin, "openai")

	names, err := b.fetchProviderModels(context.Background(), "selfhosted", cfg)
	if err != nil {
		t.Fatalf("pinned listing must succeed: %v", err)
	}
	want := []string{"llama-3.1-8b", "qwen3"} // listOpenAIModels sorts
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("names = %v, want %v", names, want)
			break
		}
	}
}

// A wrong pin must fail closed rather than silently falling back to system
// verification (which would also fail here, but for the wrong reason — the
// assertion checks the mismatch specifically).
func TestFetchProviderModels_WrongPinFailsClosed(t *testing.T) {
	srv, _ := newModelListServer(t, "qwen3")
	b := newListingBuilder(nil)
	cfg := listingConfig("selfhosted", srv.URL+"/v1", pinFixture, "openai")

	names, err := b.fetchProviderModels(context.Background(), "selfhosted", cfg)
	if err == nil {
		t.Fatalf("a mismatching pin must fail the listing; got %v", names)
	}
	if !errors.Is(err, llmtls.ErrPinMismatch) && !strings.Contains(err.Error(), llmtls.ErrPinMismatch.Error()) {
		t.Errorf("error should report a fingerprint mismatch; got %v", err)
	}
}

// With no proxy and no pin the listing uses the SDK default transport, so a
// self-signed endpoint is rejected by system verification — the pre-pin
// behavior, unchanged.
func TestFetchProviderModels_NoPinUsesSystemVerification(t *testing.T) {
	srv, _ := newModelListServer(t, "qwen3")
	b := newListingBuilder(nil)
	cfg := listingConfig("selfhosted", srv.URL+"/v1", "", "openai")

	if _, err := b.fetchProviderModels(context.Background(), "selfhosted", cfg); err == nil {
		t.Error("without a pin a self-signed endpoint must fail system verification")
	}
}

// recordingTransport answers every request with a canned OpenAI model listing
// and counts the calls, standing in for a proxy transport.
type recordingTransport struct {
	calls atomic.Int32
	body  string
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.calls.Add(1)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Request:    req,
	}, nil
}

// While the proxy dials (active and the host not bypassed), the listing goes
// through EXACTLY the proxy client and the pin is not applied. The recording
// transport proves both — it is reached (so the proxy client was used) and
// the deliberately wrong pin did not reject anything (so no pinning was
// layered on top). For a bypassed host see
// TestFetchProviderModels_BypassedHostPinnedDirect.
func TestFetchProviderModels_ProxyWinsOverPin(t *testing.T) {
	rec := &recordingTransport{body: `{"object":"list","data":[{"id":"via-proxy","object":"model"}]}`}
	proxyClient := &http.Client{Transport: rec, Timeout: 30 * time.Second}
	b := newListingBuilder(proxyClient)

	for _, providerType := range []string{"openai", "anthropic"} {
		t.Run(providerType, func(t *testing.T) {
			before := rec.calls.Load()
			cfg := listingConfig("selfhosted", "https://llm.lan:8443/v1", pinFixture, providerType)

			names, err := b.fetchProviderModels(context.Background(), "selfhosted", cfg)
			if err != nil {
				t.Fatalf("listing through the proxy client must succeed: %v", err)
			}
			if rec.calls.Load() == before {
				t.Fatal("the proxy client was not used — the pin must not replace it")
			}
			if len(names) == 0 {
				t.Error("expected a non-empty model list from the proxy transport")
			}
		})
	}
}

// A host on proxy.bypass_list dials directly, so its pin applies even while
// the proxy is active for everyone else (ADR-054: bypass re-arms the pin).
// The pinned direct client must reach the self-signed endpoint that system
// verification — and therefore the proxy route — would reject.
func TestFetchProviderModels_BypassedHostPinnedDirect(t *testing.T) {
	srv, pin := newModelListServer(t, "qwen3")
	b := newListingBuilder(&http.Client{Transport: &http.Transport{}})
	cfg := listingConfig("selfhosted", srv.URL+"/v1", pin, "openai")
	host, _ := url.Parse(srv.URL)
	cfg.Proxy.BypassList = []string{host.Hostname()}

	names, err := b.fetchProviderModels(context.Background(), "selfhosted", cfg)
	if err != nil {
		t.Fatalf("bypassed pinned listing must succeed: %v", err)
	}
	if len(names) != 1 || names[0] != "qwen3" {
		t.Errorf("names = %v, want [qwen3]", names)
	}
}

// The fixed "chatgpt" provider has no pin key, but it must still honor the
// proxy — the pre-pin code path ignored the proxy entirely here.
func TestFetchProviderModels_ChatGPTHonorsProxy(t *testing.T) {
	rec := &recordingTransport{body: `{"object":"list","data":[{"id":"gpt-4o","object":"model"}]}`}
	proxyClient := &http.Client{Transport: rec, Timeout: 30 * time.Second}
	b := newListingBuilder(proxyClient)
	cfg := &BuilderConfig{
		LLM: BuilderLLMConfig{
			ProviderConfigs: map[string]BuilderProviderConfig{
				"chatgpt": {ProviderType: "openai", APIKey: "key", Models: []string{"gpt-4o"}},
			},
		},
		ExpandEnvVars: identityExpand,
	}

	names, err := b.fetchProviderModels(context.Background(), "chatgpt", cfg)
	if err != nil {
		t.Fatalf("chatgpt listing through the proxy must succeed: %v", err)
	}
	if rec.calls.Load() == 0 {
		t.Error("the chatgpt listing bypassed the proxy client")
	}
	if len(names) != 1 || names[0] != "gpt-4o" {
		t.Errorf("names = %v, want [gpt-4o]", names)
	}
}

// ---------------------------------------------------------------------------
// Lazy context-window probe lookup
// ---------------------------------------------------------------------------

// The probe resolves its dial client from the same lookup that finds the
// provider, so the pin must ride along with the base URL and key.
func TestLookupOpenAIProviderBaseURL_CarriesTLSFingerprint(t *testing.T) {
	cfg := &BuilderConfig{
		LLM: BuilderLLMConfig{
			ProviderConfigs: map[string]BuilderProviderConfig{
				"selfhosted": {
					ProviderType:   "openai",
					APIKey:         "pinned-key",
					BaseURL:        "https://llm.lan:8443/v1",
					Models:         []string{"qwen3"},
					TLSFingerprint: pinFixture,
				},
				"plain": {
					ProviderType: "openai",
					APIKey:       "plain-key",
					BaseURL:      "http://127.0.0.1:1234/v1",
					Models:       []string{"llama-3.1-8b"},
				},
			},
		},
		ExpandEnvVars: identityExpand,
	}

	base, key, pin, ok := lookupOpenAIProviderBaseURL(cfg, "qwen3", identityExpand)
	if !ok {
		t.Fatal("expected the pinned provider to match qwen3")
	}
	if base != "https://llm.lan:8443/v1" || key != "pinned-key" {
		t.Errorf("base=%q key=%q, want the pinned provider's values", base, key)
	}
	if pin != pinFixture {
		t.Errorf("pin = %q, want %q", pin, pinFixture)
	}

	_, _, pin, ok = lookupOpenAIProviderBaseURL(cfg, "llama-3.1-8b", identityExpand)
	if !ok {
		t.Fatal("expected the unpinned provider to match llama-3.1-8b")
	}
	if pin != "" {
		t.Errorf("unpinned provider reported pin %q, want empty", pin)
	}

	if _, _, _, ok := lookupOpenAIProviderBaseURL(cfg, "no-such-model", identityExpand); ok {
		t.Error("an unknown model must not match any provider")
	}
}
