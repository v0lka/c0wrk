package core

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/proxy"
	"github.com/v0lka/sp4rk/llm"
)

// This file pins the subscription-auth seam (BuilderSubscriptionAuthConfig,
// SetSubscriptionTokenSource) — the ChatGPT oauth counterpart of the embedded
// seam pinned in builder_embedded_gate_test.go. The three guarantees:
//
//  1. api_key mode (no SubscriptionAuth marker) is byte-for-byte historical:
//     no TokenSource, no RequireStreaming, whatever the seam carries.
//  2. A subscription entry served by the seam carries the live token source
//     and its requests go out with those credentials — including through the
//     per-session router, whose BuilderConfig carries no seam at all.
//  3. A subscription entry with no serving seam (signed out) keeps its place
//     in the router but fails every request with the actionable "sign in
//     with ChatGPT" error — never a silent fallback to the static api_key.

// stubTokenSource hands out one fixed subscription credential per call and
// counts the resolutions, so tests can prove the wire saw freshly resolved
// credentials rather than a stale or static one.
type stubTokenSource struct {
	mu    sync.Mutex
	calls int
}

func (s *stubTokenSource) Token(context.Context) (llm.BearerToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return llm.BearerToken{
		AccessToken:  "sub-token-1",
		TokenType:    "Bearer",
		ExtraHeaders: map[string]string{"ChatGPT-Account-Id": "acct-1"},
	}, nil
}

func (s *stubTokenSource) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// subscriptionChatBody is a minimal well-formed /chat/completions response.
const subscriptionChatBody = `{"id":"1","object":"chat.completion","created":1700000000,` +
	`"model":"gpt-test","choices":[{"index":0,` +
	`"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// subscriptionRouterCfg is the shape the session factory produces: a plain
// conversion of the live config, carrying NO subscription seam — the builder
// default must be the net that catches it.
func subscriptionRouterCfg(baseURL string) *BuilderConfig {
	return &BuilderConfig{
		LLM: BuilderLLMConfig{
			DefaultModel: "chatgpt/gpt-test",
			ProviderConfigs: map[string]BuilderProviderConfig{
				"chatgpt": {
					ProviderType:     "openai",
					APIKey:           "sk-static",
					BaseURL:          baseURL + "/v1",
					Models:           []string{"gpt-test"},
					SubscriptionAuth: true,
				},
			},
		},
		Timeouts:      BuilderTimeoutsConfig{LLMRequestTimeout: 60},
		ExpandEnvVars: identityExpand,
	}
}

func newSubscriptionRouterBuilder() *OrchestratorBuilder {
	return &OrchestratorBuilder{
		logger:  slog.New(slog.DiscardHandler),
		goAsync: func(fn func()) { fn() },
	}
}

// TestProviderEntryFromConfig_APIKeyModeIsHistorical is the api_key AC at the
// entry level: with the SubscriptionAuth marker off, the entry is built
// exactly as before the mode existed — even when a fully-populated seam is
// installed on the builder.
func TestProviderEntryFromConfig_APIKeyModeIsHistorical(t *testing.T) {
	shared := &http.Client{Timeout: 10 * time.Minute}
	seam := BuilderSubscriptionAuthConfig{ProviderName: "chatgpt", TokenSource: &stubTokenSource{}}

	entry := providerEntryFromConfig("chatgpt",
		BuilderProviderConfig{ProviderType: "openai", APIKey: "sk-static", Models: []string{"gpt-test"}},
		shared, nil, proxy.BypassMatcher{}, BuilderEmbeddedLLMConfig{}, llmBudgetWiring{}, seam, identityExpand, nil)

	if entry.TokenSource != nil {
		t.Errorf("TokenSource = %T, want nil — api_key mode must never carry credentials", entry.TokenSource)
	}
	if entry.RequireStreaming {
		t.Error("RequireStreaming = true — api_key mode must keep the historical false")
	}
	if entry.APIKey != "sk-static" {
		t.Errorf("APIKey = %q, want the static key verbatim", entry.APIKey)
	}
}

// TestProviderEntryFromConfig_SubscriptionEntryCarriesTheSeamSource verifies
// the marker-on/serving-seam shape: the live token source and the streaming
// requirement land on the entry, and the static APIKey still travels (the
// middleware overrides it on the wire; it is not a fallback).
func TestProviderEntryFromConfig_SubscriptionEntryCarriesTheSeamSource(t *testing.T) {
	shared := &http.Client{Timeout: 10 * time.Minute}
	src := &stubTokenSource{}
	seam := BuilderSubscriptionAuthConfig{ProviderName: "chatgpt", TokenSource: src}

	entry := providerEntryFromConfig("chatgpt",
		BuilderProviderConfig{ProviderType: "openai", APIKey: "sk-static", Models: []string{"gpt-test"}, SubscriptionAuth: true},
		shared, nil, proxy.BypassMatcher{}, BuilderEmbeddedLLMConfig{}, llmBudgetWiring{}, seam, identityExpand, nil)

	if entry.TokenSource == nil {
		t.Fatal("TokenSource = nil, want the seam's live source")
	}
	if entry.TokenSource != llm.TokenSource(src) {
		t.Errorf("TokenSource = %T, want the exact seam source", entry.TokenSource)
	}
	if !entry.RequireStreaming {
		t.Error("RequireStreaming = false, want true on a subscription entry")
	}
	if entry.APIKey != "sk-static" {
		t.Errorf("APIKey = %q, want it carried through untouched", entry.APIKey)
	}
}

// TestProviderEntryFromConfig_SignedOutEntryFailsActionably verifies the
// signed-out posture: the entry survives (models stay registered) but its
// token source fails every resolution with the actionable sign-in error —
// the request aborts before the wire, and the static api_key is never
// consulted as a fallback.
func TestProviderEntryFromConfig_SignedOutEntryFailsActionably(t *testing.T) {
	shared := &http.Client{Timeout: 10 * time.Minute}

	for name, seam := range map[string]BuilderSubscriptionAuthConfig{
		"no seam at all":       {},
		"seam serving another": {ProviderName: "other", TokenSource: &stubTokenSource{}},
		"name with no source":  {ProviderName: "chatgpt"},
	} {
		t.Run(name, func(t *testing.T) {
			entry := providerEntryFromConfig("chatgpt",
				BuilderProviderConfig{ProviderType: "openai", APIKey: "sk-static", Models: []string{"gpt-test"}, SubscriptionAuth: true},
				shared, nil, proxy.BypassMatcher{}, BuilderEmbeddedLLMConfig{}, llmBudgetWiring{}, seam, identityExpand, nil)

			if entry.TokenSource == nil {
				t.Fatal("TokenSource = nil, want the signed-out stand-in (the entry must survive)")
			}
			if !entry.RequireStreaming {
				t.Error("RequireStreaming = false, want true — the entry is still the subscription shape")
			}
			_, err := entry.TokenSource.Token(t.Context())
			if err == nil {
				t.Fatal("Token() succeeded — a signed-out entry must fail every resolution")
			}
			if !errors.Is(err, errSignInRequired) {
				t.Errorf("err = %v, want errSignInRequired", err)
			}
			if want := "sign in with ChatGPT"; !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain the actionable %q", err, want)
			}
		})
	}
}

// TestSubscriptionAuthSeamPrecedence pins the resolution rule (the mirror of
// TestEmbeddedSeamPrecedence): an explicit per-build TokenSource wins, the
// builder-level default fills in only when the config carries none, a
// half-populated per-config seam must not shadow the default, and a withdrawn
// default serves nothing.
func TestSubscriptionAuthSeamPrecedence(t *testing.T) {
	perBuild := &stubTokenSource{}
	builderLevel := &stubTokenSource{}

	b := newSubscriptionRouterBuilder()
	b.SetSubscriptionTokenSource(BuilderSubscriptionAuthConfig{ProviderName: "chatgpt", TokenSource: builderLevel})

	t.Run("per-config source wins", func(t *testing.T) {
		cfg := subscriptionRouterCfg("http://127.0.0.1:1")
		cfg.SubscriptionAuth = BuilderSubscriptionAuthConfig{ProviderName: "chatgpt", TokenSource: perBuild}
		if got := b.subscriptionAuthSeam(cfg); got.TokenSource != llm.TokenSource(perBuild) {
			t.Errorf("seam source = %v, want the per-config one", got.TokenSource)
		}
	})

	t.Run("builder default fills the gap", func(t *testing.T) {
		if got := b.subscriptionAuthSeam(subscriptionRouterCfg("http://127.0.0.1:1")); got.TokenSource == nil {
			t.Error("seam source = nil, want the builder-level default")
		}
	})

	t.Run("a name without a source does not shadow the default", func(t *testing.T) {
		cfg := subscriptionRouterCfg("http://127.0.0.1:1")
		cfg.SubscriptionAuth = BuilderSubscriptionAuthConfig{ProviderName: "chatgpt"}
		if got := b.subscriptionAuthSeam(cfg); got.TokenSource == nil {
			t.Error("seam source = nil; a half-populated per-config seam must fall " +
				"back to the builder default rather than serve nothing")
		}
	})

	t.Run("withdrawn default serves nothing", func(t *testing.T) {
		b.SetSubscriptionTokenSource(BuilderSubscriptionAuthConfig{})
		if got := b.subscriptionAuthSeam(subscriptionRouterCfg("http://127.0.0.1:1")); got.TokenSource != nil {
			t.Errorf("seam = %+v after the default was withdrawn; a sign-out must stop serving", got)
		}
	})
}

// TestBuildRouterSubscriptionAuthCoversTheSessionRouter is the per-session
// guarantee: the BuilderConfig carries no seam — exactly what the session
// factory produces — and the request still goes out with the subscription
// credentials resolved by the builder-level seam, overriding the static key.
func TestBuildRouterSubscriptionAuthCoversTheSessionRouter(t *testing.T) {
	var mu sync.Mutex
	authorizations, accountIDs := []string{}, []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		accountIDs = append(accountIDs, r.Header.Get("ChatGPT-Account-Id"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(subscriptionChatBody))
	}))
	defer srv.Close()

	src := &stubTokenSource{}
	b := newSubscriptionRouterBuilder()
	b.SetSubscriptionTokenSource(BuilderSubscriptionAuthConfig{ProviderName: "chatgpt", TokenSource: src})

	router, _, err := b.buildRouter(t.Context(), subscriptionRouterCfg(srv.URL), nil)
	if err != nil {
		t.Fatalf("buildRouter: %v", err)
	}
	if got := router.ActiveProviderName(); got != "chatgpt" {
		t.Fatalf("active provider = %q, want chatgpt", got)
	}
	resp, err := router.Call(t.Context(), llm.ChatRequest{
		Messages:  []llm.Message{{Role: "user", Content: "ping"}},
		MaxTokens: 8,
	})
	if err != nil {
		t.Fatalf("the request through the session-factory-shaped config: %v", err)
	}
	if resp == nil || resp.Message.Content != "ok" {
		t.Fatalf("response = %+v, want the endpoint's reply", resp)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(authorizations) != 1 {
		t.Fatalf("endpoint saw %d requests, want 1", len(authorizations))
	}
	if got := authorizations[0]; got != "Bearer sub-token-1" {
		t.Errorf("Authorization = %q, want the subscription credential (the static key must be overridden)", got)
	}
	if got := accountIDs[0]; got != "acct-1" {
		t.Errorf("ChatGPT-Account-Id = %q, want the extra header from the token source", got)
	}
	if src.count() != 1 {
		t.Errorf("Token() calls = %d, want 1 (freshly resolved per request)", src.count())
	}
}

// TestBuildRouterSignedOutFailsActionably is the signed-out guarantee through
// the real router: the entry stays registered (the default model resolves),
// but the request fails with the actionable sign-in error and NOTHING leaves
// the process — no request, and therefore no silent api_key fallback.
func TestBuildRouterSignedOutFailsActionably(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(subscriptionChatBody))
	}))
	defer srv.Close()

	// No seam anywhere: neither per-config nor builder-level.
	b := newSubscriptionRouterBuilder()
	router, _, err := b.buildRouter(t.Context(), subscriptionRouterCfg(srv.URL), nil)
	if err != nil {
		t.Fatalf("buildRouter: %v", err)
	}
	if got := router.ActiveProviderName(); got != "chatgpt" {
		t.Fatalf("active provider = %q — the signed-out entry must stay registered", got)
	}

	_, err = router.Call(t.Context(), llm.ChatRequest{
		Messages:  []llm.Message{{Role: "user", Content: "ping"}},
		MaxTokens: 8,
	})
	if err == nil {
		t.Fatal("the request succeeded — a signed-out subscription entry must fail")
	}
	if want := "sign in with ChatGPT"; !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want it to contain the actionable %q", err, want)
	}
	if hits != 0 {
		t.Errorf("endpoint hits = %d, want 0 — a signed-out entry must abort before the wire "+
			"(and must never fall back to the static api_key)", hits)
	}
}
