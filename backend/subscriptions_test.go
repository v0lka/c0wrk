package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/credentials"
	keyring "github.com/zalando/go-keyring"
)

type testKeyStore struct{ values map[string]string }

func (s *testKeyStore) Get(a, b string) (string, error) {
	if s.values == nil {
		return "", keyring.ErrNotFound
	}
	v, ok := s.values[a+"/"+b]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}
func (s *testKeyStore) Set(a, b, c string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[a+"/"+b] = c
	return nil
}
func (s *testKeyStore) Delete(a, b string) error { return nil }

func TestSubscriptionManager_PKCEStateRefreshAndLogout(t *testing.T) {
	var tokenForms []url.Values
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		tokenForms = append(tokenForms, r.PostForm)
		if r.PostForm.Get("grant_type") == "refresh_token" {
			_, _ = w.Write([]byte(`{"access_token":"fresh","refresh_token":"refresh","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"access","refresh_token":"refresh","expires_in":1}`))
	}))
	defer token.Close()
	cfg := config.SubscriptionProvidersConfig{ChatGPT: config.SubscriptionProviderConfig{Enabled: true, BaseURL: "https://example.invalid", AuthorizationURL: "https://auth.example/authorize", TokenURL: token.URL, ClientID: "public-client", Scopes: []string{"openid"}}}
	m := newSubscriptionManager(t.TempDir(), func() (config.SubscriptionProvidersConfig, bool) { return config.SubscriptionProvidersConfig{}, true })
	m.profile = func() config.SubscriptionProviderConfig { return cfg.ChatGPT }
	m.store = credentials.NewWithKeyStore(t.TempDir(), &testKeyStore{})
	login, err := m.begin(context.Background(), "chatgpt")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(login.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("code_verifier") != "" {
		t.Fatalf("unsafe OAuth query: %s", u)
	}
	if q.Get("redirect_uri") != chatGPTRedirectURI || q.Get("originator") != "codex_cli_rs" {
		t.Fatalf("unexpected ChatGPT authorization parameters: %s", u)
	}
	m.mu.Lock()
	pending := m.pending["chatgpt"]
	m.mu.Unlock()
	bad := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+pending.listener.Addr().String()+chatGPTCallbackPath+"?code=x&state=wrong", http.NoBody)
	pending.server.Handler.ServeHTTP(bad, req)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad state status=%d", bad.Code)
	}
	good := httptest.NewRecorder()
	req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+pending.listener.Addr().String()+chatGPTCallbackPath+"?code=code&state="+q.Get("state"), http.NoBody)
	pending.server.Handler.ServeHTTP(good, req)
	if good.Code != http.StatusOK {
		t.Fatalf("good callback status=%d", good.Code)
	}
	_, err = managedResolver{m, "chatgpt"}.ResolveAccessToken(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokenForms) != 2 || tokenForms[1].Get("grant_type") != "refresh_token" {
		t.Fatalf("refresh forms=%v", tokenForms)
	}
	if tokenForms[0].Get("redirect_uri") != chatGPTRedirectURI {
		t.Fatalf("authorization-code redirect URI = %q, want %q", tokenForms[0].Get("redirect_uri"), chatGPTRedirectURI)
	}
	if err := m.store.Logout("chatgpt", subscriptionAccount); err != nil {
		t.Fatal(err)
	}
	if m.status("chatgpt").Connected {
		t.Fatal("logout left provider connected")
	}
}

func TestSubscriptionManager_StatusNeverExposesToken(t *testing.T) {
	m := newSubscriptionManager(t.TempDir(), func() (config.SubscriptionProvidersConfig, bool) {
		return config.SubscriptionProvidersConfig{Kimi: config.SubscriptionProviderConfig{Enabled: true}}, true
	})
	m.store = credentials.NewWithKeyStore(t.TempDir(), &testKeyStore{})
	if err := m.store.Save("kimi", subscriptionAccount, credentials.Credential{AccessToken: "secret-token"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.status("kimi").Message, "secret-token") {
		t.Fatal("secret leaked in status")
	}
	m.cancel("kimi")
	time.Sleep(1 * time.Millisecond)
}
