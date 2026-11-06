package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/proxy"
	"github.com/v0lka/c0wrk/core/version"
	"github.com/v0lka/sp4rk/llm"
)

func TestSubscriptionAuthEntrySupportsKimiWithoutForcingStreaming(t *testing.T) {
	chatgpt := &stubTokenSource{}
	kimi := &stubTokenSource{}
	seam := BuilderSubscriptionAuthConfig{TokenSources: map[string]llm.TokenSource{
		"chatgpt": chatgpt,
		"kimi":    kimi,
	}}
	shared := &http.Client{}

	chatEntry := providerEntryFromConfig("chatgpt",
		BuilderProviderConfig{ProviderType: "openai", SubscriptionAuth: true},
		shared, nil, proxy.BypassMatcher{}, BuilderEmbeddedLLMConfig{}, llmBudgetWiring{}, seam, identityExpand, nil, nil)
	if !chatEntry.RequireStreaming {
		t.Error("ChatGPT RequireStreaming = false, want Codex streaming-only behavior")
	}
	if _, err := chatEntry.TokenSource.Token(t.Context()); err != nil {
		t.Fatalf("ChatGPT dynamic source did not delegate to its explicit source: %v", err)
	}

	kimiEntry := providerEntryFromConfig("kimi",
		BuilderProviderConfig{ProviderType: "openai", SubscriptionAuth: true},
		shared, nil, proxy.BypassMatcher{}, BuilderEmbeddedLLMConfig{}, llmBudgetWiring{}, seam, identityExpand, nil, nil)
	if _, err := kimiEntry.TokenSource.Token(t.Context()); err != nil {
		t.Fatalf("Kimi dynamic source did not delegate to its explicit source: %v", err)
	}
	if kimiEntry.RequireStreaming {
		t.Error("Kimi RequireStreaming = true; streaming-only is ChatGPT-specific")
	}
}

func TestSetSubscriptionTokenSourceProvidersAreIndependent(t *testing.T) {
	b := newSubscriptionRouterBuilder()
	chatgpt := &stubTokenSource{}
	kimi := &stubTokenSource{}
	b.SetSubscriptionTokenSource(BuilderSubscriptionAuthConfig{ProviderName: "chatgpt", TokenSource: chatgpt})
	b.SetSubscriptionTokenSource(BuilderSubscriptionAuthConfig{ProviderName: "kimi", TokenSource: kimi})

	// The historical zero-value ChatGPT sign-out withdraws only ChatGPT.
	b.SetSubscriptionTokenSource(BuilderSubscriptionAuthConfig{})
	seam := b.builderSubscriptionSeam()
	if source := seam.source("chatgpt"); source != nil {
		t.Error("ChatGPT source survived its sign-out")
	}
	if source := seam.source("kimi"); source != llm.TokenSource(kimi) {
		t.Errorf("Kimi source = %v after ChatGPT sign-out, want it preserved", source)
	}
}

func TestDynamicSubscriptionSourceKimiSignInError(t *testing.T) {
	source := &dynamicSubscriptionSource{builder: newSubscriptionRouterBuilder(), providerName: "kimi"}
	_, err := source.Token(t.Context())
	if err == nil || !strings.Contains(err.Error(), "sign in with Kimi") || !strings.Contains(err.Error(), "llm.kimi.auth.mode") {
		t.Fatalf("Token error = %v, want actionable Kimi sign-in guidance", err)
	}
}

type kimiStubTokenSource struct{}

func (kimiStubTokenSource) Token(context.Context) (llm.BearerToken, error) {
	return llm.BearerToken{
		AccessToken: "kimi-subscription-token",
		TokenType:   "Bearer",
		ExtraHeaders: map[string]string{
			"User-Agent":        "c0wrk/" + version.Version,
			"anthropic-version": "2023-06-01",
		},
	}, nil
}

func TestBuildRouterKimiSubscriptionAuthUsesOpenAIWithoutKeyFallback(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request path = %q, want OpenAI chat completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer kimi-subscription-token" {
			t.Errorf("Authorization = %q, static key must not be used", got)
		}
		if got := r.Header.Get("User-Agent"); got != "c0wrk/"+version.Version {
			t.Errorf("User-Agent = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "" {
			t.Errorf("ChatGPT-Account-Id leaked to Kimi: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(subscriptionChatBody))
	}))
	t.Cleanup(srv.Close)

	b := newSubscriptionRouterBuilder()
	b.SetSubscriptionTokenSource(BuilderSubscriptionAuthConfig{ProviderName: "kimi", TokenSource: kimiStubTokenSource{}})
	cfg := &BuilderConfig{
		LLM: BuilderLLMConfig{
			DefaultModel: "kimi/k3",
			ProviderConfigs: map[string]BuilderProviderConfig{
				"kimi": {
					ProviderType: "openai", APIKey: "must-not-leak", BaseURL: srv.URL + "/v1",
					Models: []string{"k3"}, SubscriptionAuth: true,
				},
			},
		},
		Timeouts: BuilderTimeoutsConfig{LLMRequestTimeout: 60}, ExpandEnvVars: identityExpand,
	}
	router, _, err := b.buildRouter(t.Context(), cfg, nil)
	if err != nil {
		t.Fatalf("buildRouter: %v", err)
	}
	resp, err := router.Call(t.Context(), llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "ping"}}, MaxTokens: 8})
	if err != nil {
		t.Fatalf("non-streaming Kimi OAuth request: %v", err)
	}
	if resp == nil || resp.Message.Content != "ok" || hits != 1 {
		t.Fatalf("response = %+v, hits = %d", resp, hits)
	}
}

func TestSubscriptionEntryDynamicSourceTracksBuilderSignOutAndSignIn(t *testing.T) {
	for _, providerName := range []string{"chatgpt", "kimi"} {
		t.Run(providerName, func(t *testing.T) {
			b := newSubscriptionRouterBuilder()
			source := &stubTokenSource{}
			b.SetSubscriptionTokenSource(BuilderSubscriptionAuthConfig{ProviderName: providerName, TokenSource: source})
			entry := providerEntryFromConfig(providerName,
				BuilderProviderConfig{ProviderType: "openai", SubscriptionAuth: true},
				&http.Client{}, nil, proxy.BypassMatcher{}, BuilderEmbeddedLLMConfig{}, llmBudgetWiring{},
				b.subscriptionAuthSeam(&BuilderConfig{}), identityExpand, nil, b)
			dynamic, ok := entry.TokenSource.(*dynamicSubscriptionSource)
			if !ok || !dynamic.builderDefault {
				t.Fatalf("entry source = %T, builderDefault = %v", entry.TokenSource, ok && dynamic.builderDefault)
			}

			if providerName == "chatgpt" {
				b.SetSubscriptionTokenSource(BuilderSubscriptionAuthConfig{})
			} else {
				b.SetSubscriptionTokenSource(BuilderSubscriptionAuthConfig{ProviderName: providerName})
			}
			_, err := entry.TokenSource.Token(t.Context())
			want := "sign in with ChatGPT"
			if providerName == "kimi" {
				want = "sign in with Kimi"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("cached entry after sign-out error = %v, want %q", err, want)
			}

			b.SetSubscriptionTokenSource(BuilderSubscriptionAuthConfig{ProviderName: providerName, TokenSource: source})
			token, err := entry.TokenSource.Token(t.Context())
			if err != nil || token.AccessToken == "" {
				t.Fatalf("cached entry after later sign-in = token %+v, err %v", token, err)
			}
		})
	}
}

func TestSubscriptionAuthExplicitMapNeverFallsBackToBuilderDefaults(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources map[string]llm.TokenSource
	}{
		{name: "empty non-nil map", sources: map[string]llm.TokenSource{}},
		{name: "ChatGPT-only partial map", sources: map[string]llm.TokenSource{"chatgpt": &stubTokenSource{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newSubscriptionRouterBuilder()
			defaultKimi := &stubTokenSource{}
			b.SetSubscriptionTokenSource(BuilderSubscriptionAuthConfig{ProviderName: "kimi", TokenSource: defaultKimi})
			cfg := subscriptionRouterCfg("http://127.0.0.1:1")
			cfg.SubscriptionAuth.TokenSources = tc.sources
			seam := b.subscriptionAuthSeam(cfg)
			if seam.followBuilderDefault {
				t.Fatal("explicit map follows builder defaults")
			}
			if source := seam.source("kimi"); source != nil {
				t.Fatalf("explicit map exposes default Kimi source: %T", source)
			}
			entry := providerEntryFromConfig("kimi",
				BuilderProviderConfig{ProviderType: "openai", SubscriptionAuth: true},
				&http.Client{}, nil, proxy.BypassMatcher{}, BuilderEmbeddedLLMConfig{}, llmBudgetWiring{}, seam, identityExpand, nil, b)
			_, err := entry.TokenSource.Token(t.Context())
			if err == nil || !strings.Contains(err.Error(), "sign in with Kimi") {
				t.Fatalf("explicit-map Kimi error = %v, want sign-in guidance", err)
			}
			if calls := defaultKimi.count(); calls != 0 {
				t.Fatalf("default Kimi source called %d times, want zero", calls)
			}
		})
	}

	t.Run("nil map inherits builder defaults", func(t *testing.T) {
		b := newSubscriptionRouterBuilder()
		defaultKimi := &stubTokenSource{}
		b.SetSubscriptionTokenSource(BuilderSubscriptionAuthConfig{ProviderName: "kimi", TokenSource: defaultKimi})
		seam := b.subscriptionAuthSeam(&BuilderConfig{})
		if !seam.followBuilderDefault {
			t.Fatal("nil map is treated as an explicit replacement")
		}
		if token, err := seam.source("kimi").Token(t.Context()); err != nil || token.AccessToken == "" {
			t.Fatalf("default source = token %+v, err %v", token, err)
		}
	})
}
