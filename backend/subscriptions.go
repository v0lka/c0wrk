package backend

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/credentials"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/llm"
)

const (
	subscriptionAccount    = "default"
	chatGPTClientID        = "app_EMoamEEZ73f0CkXaXp7hrann"
	chatGPTAuthorizeURL    = "https://auth.openai.com/oauth/authorize"
	chatGPTTokenURL        = "https://auth.openai.com/oauth/token"
	chatGPTCodexBaseURL    = "https://chatgpt.com/backend-api/codex"
	chatGPTCallbackAddress = "127.0.0.1:1455"
	chatGPTRedirectURI     = "http://localhost:1455/auth/callback"
	chatGPTCallbackPath    = "/auth/callback"
)

var chatGPTCodexModels = []string{
	"gpt-5.1-codex",
	"gpt-5.1-codex-max",
	"gpt-5.1-codex-mini",
	"gpt-5.3-chat-latest",
	"gpt-5.3-codex",
	"gpt-5.3-codex-spark",
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-5.4-nano",
	"gpt-5.4-pro",
	"gpt-5.5",
	"gpt-5.5-pro",
	"gpt-5.6-luna",
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-6-astra",
}

// legacyChatGPTCodexModels is the initial, incomplete built-in subscription
// catalogue. A persisted selection equal to this exact set was created before
// users could choose individual subscription models, so it means "all known
// models", not an intentional four-model restriction.
var legacyChatGPTCodexModels = []string{"gpt-5.4", "gpt-5.4-mini", "gpt-5.5", "gpt-5.3-codex-spark"}

type subscriptionManager struct {
	store          *credentials.CredentialStore
	client         *http.Client
	mu             sync.Mutex
	pending        map[string]*subscriptionLogin
	configs        func() (config.SubscriptionProvidersConfig, bool)
	profile        func() config.SubscriptionProviderConfig
	onConnected    func() error
	routingHeaders map[string]string
}

type subscriptionLogin struct {
	provider, state, verifier string
	server                    *http.Server
	listener                  net.Listener
	done                      chan error
}

type SubscriptionStatus struct {
	Provider   string `json:"provider"`
	Connected  bool   `json:"connected"`
	Connecting bool   `json:"connecting"`
	Available  bool   `json:"available"`
	Message    string `json:"message,omitempty"`
}
type SubscriptionLoginResponse struct {
	AuthorizationURL string `json:"authorization_url"`
}

func newSubscriptionManager(agentDir string, configs func() (config.SubscriptionProvidersConfig, bool)) *subscriptionManager {
	return &subscriptionManager{
		store:          credentials.New(agentDir),
		client:         &http.Client{Timeout: 30 * time.Second},
		pending:        map[string]*subscriptionLogin{},
		configs:        configs,
		routingHeaders: map[string]string{},
	}
}
func (m *subscriptionManager) providerConfig(provider string) (config.SubscriptionProviderConfig, bool) {
	subscriptions, experimental := m.configs()
	if provider != "chatgpt" || !experimental {
		return config.SubscriptionProviderConfig{}, false
	}
	if m.profile != nil {
		return m.profile(), true
	}
	return config.SubscriptionProviderConfig{
		Enabled:          true,
		BaseURL:          chatGPTCodexBaseURL,
		AuthorizationURL: chatGPTAuthorizeURL,
		TokenURL:         chatGPTTokenURL,
		ClientID:         chatGPTClientID,
		Scopes:           []string{"openid", "profile", "email", "offline_access"},
		Models:           append([]string(nil), chatGPTCodexModels...),
		EnabledModels:    append([]string(nil), subscriptions.ChatGPT.EnabledModels...),
	}, true
}
func (m *subscriptionManager) status(provider string) SubscriptionStatus {
	_, available := m.providerConfig(provider)
	status := SubscriptionStatus{Provider: provider, Available: available}
	if !available {
		status.Message = "Not enabled"
		return status
	}
	_, err := m.store.Load(provider, subscriptionAccount)
	status.Connected = err == nil
	m.mu.Lock()
	_, status.Connecting = m.pending[provider]
	m.mu.Unlock()
	if err != nil && !errors.Is(err, credentials.ErrNotFound) {
		status.Message = "Reconnect required"
	}
	return status
}
func (m *subscriptionManager) statuses() []SubscriptionStatus {
	return []SubscriptionStatus{m.status("chatgpt")}
}

func (m *subscriptionManager) begin(ctx context.Context, provider string) (SubscriptionLoginResponse, error) {
	cfg, available := m.providerConfig(provider)
	if !available {
		return SubscriptionLoginResponse{}, errors.New("subscription provider is unavailable")
	}
	if cfg.AuthorizationURL == "" || cfg.TokenURL == "" || cfg.ClientID == "" {
		return SubscriptionLoginResponse{}, errors.New("subscription provider metadata is incomplete")
	}
	m.cancel(provider)
	state, err := randomURLToken(32)
	if err != nil {
		return SubscriptionLoginResponse{}, errors.New("could not start sign-in")
	}
	verifier, err := randomURLToken(48)
	if err != nil {
		return SubscriptionLoginResponse{}, errors.New("could not start sign-in")
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", chatGPTCallbackAddress)
	if err != nil {
		return SubscriptionLoginResponse{}, errors.New("could not reserve the ChatGPT sign-in callback; close Codex or OpenCode and try again")
	}
	login := &subscriptionLogin{provider: provider, state: state, verifier: verifier, listener: ln, done: make(chan error, 1)}
	mux := http.NewServeMux()
	login.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	mux.HandleFunc(chatGPTCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "Invalid sign-in state", http.StatusBadRequest)
			select {
			case login.done <- errors.New("invalid OAuth state"):
			default:
			}
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Missing authorization code", http.StatusBadRequest)
			login.done <- errors.New("missing authorization code")
			return
		}
		if err := m.exchange(r.Context(), provider, cfg, code, verifier); err != nil {
			http.Error(w, "Sign-in failed", http.StatusBadGateway)
			login.done <- err
			return
		}
		if m.onConnected != nil {
			if err := m.onConnected(); err != nil {
				http.Error(w, "Sign-in could not be activated", http.StatusBadGateway)
				login.done <- err
				return
			}
		}
		_, _ = io.WriteString(w, "Sign-in complete. You can close this tab.")
		login.done <- nil
	})
	m.mu.Lock()
	m.pending[provider] = login
	m.mu.Unlock()
	go func() { _ = login.server.Serve(ln) }()
	go func() {
		select {
		case <-ctx.Done():
			m.cancel(provider)
		case <-login.done:
			m.cancel(provider)
		}
	}()
	u, err := url.Parse(cfg.AuthorizationURL)
	if err != nil {
		m.cancel(provider)
		return SubscriptionLoginResponse{}, errors.New("invalid authorization URL")
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", chatGPTRedirectURI)
	q.Set("state", state)
	q.Set("code_challenge", pkceChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	if provider == "chatgpt" {
		q.Set("id_token_add_organizations", "true")
		q.Set("codex_cli_simplified_flow", "true")
		q.Set("originator", "codex_cli_rs")
	}
	if len(cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	u.RawQuery = q.Encode()
	return SubscriptionLoginResponse{AuthorizationURL: u.String()}, nil
}
func (m *subscriptionManager) cancel(provider string) {
	m.mu.Lock()
	login := m.pending[provider]
	delete(m.pending, provider)
	m.mu.Unlock()
	if login != nil {
		_ = login.server.Shutdown(context.Background())
	}
}
func (m *subscriptionManager) exchange(ctx context.Context, provider string, cfg config.SubscriptionProviderConfig, code, verifier string) error {
	vals := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {cfg.ClientID}, "code_verifier": {verifier}, "redirect_uri": {chatGPTRedirectURI}}
	return m.tokenRequest(ctx, provider, cfg, vals, "")
}
func (m *subscriptionManager) tokenRequest(ctx context.Context, provider string, cfg config.SubscriptionProviderConfig, vals url.Values, preserveRefreshToken string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, strings.NewReader(vals.Encode()))
	if err != nil {
		return errors.New("token request failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.client.Do(req)
	if err != nil {
		return errors.New("token request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode > 299 {
		return errors.New("sign-in was rejected")
	}
	type subscriptionTokenResult struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	var result subscriptionTokenResult
	if json.Unmarshal(body, &result) != nil || result.AccessToken == "" {
		return errors.New("sign-in returned invalid credentials")
	}
	// Some token endpoints rotate refresh tokens on every exchange while others
	// return one only on the initial authorization. When a refresh response
	// omits the refresh token, keep the prior value so a future refresh does not
	// fail because the stored credential lost its refresh token.
	refreshToken := result.RefreshToken
	if refreshToken == "" {
		refreshToken = preserveRefreshToken
	}
	credential := credentials.Credential{
		AccessToken:  result.AccessToken,
		RefreshToken: refreshToken,
		AccountID:    chatGPTAccountID(result.IDToken, result.AccessToken),
		Residency:    chatGPTResidency(result.AccessToken),
	}
	if result.ExpiresIn > 0 {
		credential.ExpiresAt = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	if err := m.store.Save(provider, subscriptionAccount, credential); err != nil {
		return errors.New("could not securely save sign-in")
	}
	return nil
}

type managedResolver struct {
	manager  *subscriptionManager
	provider string
}

func (r managedResolver) ResolveAccessToken(ctx context.Context, force bool) (llm.AccessToken, error) {
	credential, err := r.manager.store.Load(r.provider, subscriptionAccount)
	if err != nil {
		return llm.AccessToken{}, errors.New("sign-in required")
	}
	expiry, _ := time.Parse(time.RFC3339, credential.ExpiresAt)
	if force || (!expiry.IsZero() && time.Until(expiry) < time.Minute) {
		cfg, ok := r.manager.providerConfig(r.provider)
		if !ok || credential.RefreshToken == "" {
			return llm.AccessToken{}, errors.New("sign-in required")
		}
		vals := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {credential.RefreshToken}, "client_id": {cfg.ClientID}}
		if err := r.manager.tokenRequest(ctx, r.provider, cfg, vals, credential.RefreshToken); err != nil {
			return llm.AccessToken{}, errors.New("sign-in required")
		}
		credential, err = r.manager.store.Load(r.provider, subscriptionAccount)
		if err != nil {
			return llm.AccessToken{}, errors.New("sign-in required")
		}
		expiry, _ = time.Parse(time.RFC3339, credential.ExpiresAt)
	}
	return llm.AccessToken{Value: credential.AccessToken, ExpiresAt: expiry, AccountID: credential.AccountID, Residency: credential.Residency}, nil
}

func chatGPTAccountID(tokens ...string) string {
	for _, token := range tokens {
		claims := chatGPTClaims(token)
		if account, _ := claims["chatgpt_account_id"].(string); account != "" {
			return account
		}
		if auth, _ := claims["https://api.openai.com/auth"].(map[string]any); auth != nil {
			if account, _ := auth["chatgpt_account_id"].(string); account != "" {
				return account
			}
		}
	}
	return ""
}

func chatGPTResidency(token string) string {
	claims := chatGPTClaims(token)
	if auth, _ := claims["https://api.openai.com/auth"].(map[string]any); auth != nil {
		if residency, _ := auth["chatgpt_compute_residency"].(string); residency != "" && residency != "no_constraint" {
			return residency
		}
	}
	if residency, _ := claims["chatgpt_compute_residency"].(string); residency != "" && residency != "no_constraint" {
		return residency
	}
	return ""
}

func chatGPTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}

func (m *subscriptionManager) catalog(provider string) []string {
	cfg, available := m.providerConfig(provider)
	if !available {
		return nil
	}
	return append([]string(nil), cfg.Models...)
}

func (m *subscriptionManager) connected(provider string) bool {
	_, err := m.store.Load(provider, subscriptionAccount)
	return err == nil
}

func (m *subscriptionManager) models(provider string) []string {
	cfg, available := m.providerConfig(provider)
	if !available || !m.connected(provider) {
		return nil
	}
	if cfg.EnabledModels == nil || sameModelSet(cfg.EnabledModels, legacyChatGPTCodexModels) {
		return append([]string(nil), cfg.Models...)
	}
	catalog := make(map[string]struct{}, len(cfg.Models))
	for _, model := range cfg.Models {
		catalog[model] = struct{}{}
	}
	models := make([]string, 0, len(cfg.EnabledModels))
	for _, model := range cfg.EnabledModels {
		if _, ok := catalog[model]; ok {
			models = append(models, model)
		}
	}
	return models
}

func sameModelSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[string]struct{}, len(left))
	for _, model := range left {
		set[model] = struct{}{}
	}
	if len(set) != len(right) {
		return false
	}
	for _, model := range right {
		if _, ok := set[model]; !ok {
			return false
		}
	}
	return true
}

func (m *subscriptionManager) augment(b *core.BuilderConfig) {
	cfg, available := m.providerConfig("chatgpt")
	if b.LLM.ProviderConfigs == nil {
		b.LLM.ProviderConfigs = map[string]core.BuilderProviderConfig{}
	}
	if models := m.models("chatgpt"); available && len(models) > 0 {
		b.LLM.ProviderConfigs["chatgpt_subscription"] = core.BuilderProviderConfig{ProviderType: string(llm.SubscriptionChatGPTCodex), BaseURL: cfg.BaseURL, Models: models, TokenResolver: managedResolver{m, "chatgpt"}}
	}
}
func randomURLToken(n int) (string, error) {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func pkceChallenge(v string) string {
	s := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(s[:])
}
