package providerauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/version"
)

func TestKimiProfilePinsPublicOAuthContract(t *testing.T) {
	p := Kimi()
	if p.ProviderID != "kimi" || p.ClientID != "17e5f671-d194-4dfb-9706-5516cb48c098" {
		t.Fatalf("unexpected Kimi client identity: %+v", p)
	}
	if !p.DeviceFlow {
		t.Fatal("Kimi must use Device Authorization Grant")
	}
	if got, want := p.DeviceAuthorizationURL(), "https://auth.kimi.ai/api/oauth/device_authorization"; got != want {
		t.Errorf("DeviceAuthorizationURL = %q, want %q", got, want)
	}
	if got, want := p.TokenURL(), "https://auth.kimi.ai/api/oauth/token"; got != want {
		t.Errorf("TokenURL = %q, want %q", got, want)
	}
	if p.APIBaseURL != "https://api.kimi.ai/coding/v1" {
		t.Errorf("APIBaseURL = %q", p.APIBaseURL)
	}
}

func TestKimiTokenRequestUsesDeviceEndpointAndOmitsLegacyHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/oauth/token" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("device_id"); got != "" {
			t.Errorf("legacy device_id header = %q, want absent", got)
		}
		if got := r.Header.Get("User-Agent"); got == "claude-code/0.1.0" {
			t.Error("legacy claude-code User-Agent is still present")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":60}`))
	}))
	t.Cleanup(srv.Close)
	p := Kimi()
	p.Issuer = srv.URL
	if _, err := refreshTokens(context.Background(), srv.Client(), p, "secret"); err != nil {
		t.Fatalf("refreshTokens: %v", err)
	}
}

// TestKimiDeviceFlowRefreshThresholdMatchesKimiCLI pins the Moonshot
// half-life / 5-minute floor: a 900 s access token refreshes once remaining
// life drops below 450 s (not the ChatGPT 30 s skew).
func TestKimiDeviceFlowRefreshThresholdMatchesKimiCLI(t *testing.T) {
	clock := newManualClock()
	m := &TokenManager{
		profile: Kimi(),
		skew:    DefaultExpirySkew,
		now:     clock.Now,
		tokens: &Tokens{
			AccessToken:  "at",
			RefreshToken: "rt",
			ExpiresIn:    900,
			Expiry:       clock.Now().Add(900 * time.Second),
		},
	}
	if got := m.refreshThresholdLocked(); got != 450*time.Second {
		t.Fatalf("threshold = %v, want 450s (max(5m, 900s/2))", got)
	}

	clock.Advance(400 * time.Second) // 500 s remaining — still outside the 450 s window
	if !m.validLocked() {
		t.Fatal("token at 400 s of a 900 s life should still be valid")
	}
	clock.Advance(60 * time.Second) // 440 s remaining — inside the window
	if m.validLocked() {
		t.Fatal("token at 460 s of a 900 s life must refresh (kimi-cli half-life)")
	}
}

func TestRefreshTokensRetriesTransientHTTPStatuses(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			http.Error(w, `{"error":"server_error"}`, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-ok","refresh_token":"rt-ok","token_type":"Bearer","expires_in":900}`))
	}))
	t.Cleanup(srv.Close)
	p := Kimi()
	p.Issuer = srv.URL
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := refreshTokens(ctx, srv.Client(), p, "secret")
	if err != nil {
		t.Fatalf("refreshTokens: %v", err)
	}
	if resp.AccessToken != "at-ok" || calls.Load() != 3 {
		t.Fatalf("access = %q, calls = %d, want at-ok after 3 attempts", resp.AccessToken, calls.Load())
	}
}

func TestRefreshTokensDoesNotRetryInvalidGrant(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":"invalid_grant","error_description":"refresh token expired"}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	p := Kimi()
	p.Issuer = srv.URL
	_, err := refreshTokens(context.Background(), srv.Client(), p, "secret")
	var tErr *TokenEndpointError
	if !errors.As(err, &tErr) || tErr.Code != "invalid_grant" {
		t.Fatalf("err = %v, want invalid_grant", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on invalid_grant)", calls.Load())
	}
}

func TestTokenResponsePrefersEarlierJWTExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	access := makeJWT(map[string]any{"exp": now.Add(900 * time.Second).Unix()})
	got := (&tokenResponse{
		AccessToken: access, RefreshToken: "rt", TokenType: "Bearer", ExpiresIn: 3600,
	}).tokens(now)
	if !got.Expiry.Equal(now.Add(900 * time.Second)) {
		t.Fatalf("Expiry = %v, want JWT exp (900s), not expires_in 3600", got.Expiry)
	}
	if got.ExpiresIn != 900 {
		t.Fatalf("ExpiresIn = %d, want 900 (synced to JWT)", got.ExpiresIn)
	}
}

func TestKimiBearerHeadersExcludeChatGPTAccountIdentity(t *testing.T) {
	m := &TokenManager{
		profile:  Kimi(),
		tokens:   &Tokens{AccessToken: "at", TokenType: "Bearer"},
		identity: &Identity{ChatGPTAccountID: "must-not-cross-providers"},
	}
	got := m.bearerTokenLocked()
	if _, exists := got.ExtraHeaders["ChatGPT-Account-Id"]; exists {
		t.Fatal("Kimi bearer token carries ChatGPT-Account-Id")
	}
	if got.ExtraHeaders["User-Agent"] != "c0wrk/"+version.Version || got.ExtraHeaders["anthropic-version"] != "2023-06-01" {
		t.Errorf("ExtraHeaders = %v", got.ExtraHeaders)
	}
}

func TestKimiForRegionEndpoints(t *testing.T) {
	for _, tc := range []struct {
		region  string
		issuer  string
		baseURL string
	}{
		{region: KimiRegionGlobal, issuer: "https://auth.kimi.ai", baseURL: "https://api.kimi.ai/coding/v1"},
		{region: KimiRegionMainlandCN, issuer: "https://auth.kimi.com", baseURL: "https://api.kimi.com/coding/v1"},
	} {
		t.Run(tc.region, func(t *testing.T) {
			profile := KimiForRegion(tc.region)
			if profile.Issuer != tc.issuer || profile.APIBaseURL != tc.baseURL {
				t.Errorf("profile = issuer %q, base URL %q", profile.Issuer, profile.APIBaseURL)
			}
			if profile.ClientID != "17e5f671-d194-4dfb-9706-5516cb48c098" || !profile.DeviceFlow {
				t.Errorf("device profile changed for region %q", tc.region)
			}
		})
	}
	if got := Kimi(); got.Issuer != "https://auth.kimi.ai" {
		t.Errorf("Kimi default issuer = %q, want global", got.Issuer)
	}
}

func TestKimiCredentialRegionSurvivesPersistence(t *testing.T) {
	store := NewMemorySecretStore()
	m := &TokenManager{
		profile:  KimiForRegion(KimiRegionMainlandCN),
		store:    store,
		tokens:   &Tokens{RefreshToken: "rt-mainland"},
		agentDir: t.TempDir(),
	}
	if err := m.persistLocked(); err != nil {
		t.Fatalf("persistLocked: %v", err)
	}
	raw, err := store.Get("providerauth:kimi")
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
		t.Fatal(err)
	}
	if got := persisted["region"]; got != KimiRegionMainlandCN {
		t.Errorf("persisted region = %v, want %q", got, KimiRegionMainlandCN)
	}
}

func TestKimiCredentialRegionMismatchRequiresFreshAuthorization(t *testing.T) {
	store := NewMemorySecretStore()
	seed := `{"version":2,"provider":"kimi","refresh_token":"rt-global","region":"global"}`
	if err := store.Set("providerauth:kimi", seed); err != nil {
		t.Fatal(err)
	}
	m, err := NewTokenManager(ManagerConfig{
		Profile:  KimiForRegion(KimiRegionMainlandCN),
		Store:    store,
		AgentDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Status().SignedIn {
		t.Fatal("global credential restored against the mainland issuer")
	}
	if _, err := m.Token(context.Background()); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("Token after region mismatch = %v, want ErrNotSignedIn", err)
	}
	if warning := m.LoadWarning(); !strings.Contains(warning, "region") || !strings.Contains(warning, "sign in again") {
		t.Errorf("region mismatch warning = %q", warning)
	}
	if _, err := store.Get("providerauth:kimi"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("mismatched credential was not cleared: %v", err)
	}

	global, err := NewTokenManager(ManagerConfig{
		Profile: Kimi(), Store: store, AgentDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if global.Status().SignedIn {
		t.Fatal("cleared credential resurrected when the config returned to global")
	}
}

type regionDeleteStore struct {
	SecretStore
	deleteErr error
}

func (s *regionDeleteStore) Delete(key string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.SecretStore.Delete(key)
}

func TestKimiRegionRejectionPersistsWhenCredentialDeleteFails(t *testing.T) {
	agentDir := t.TempDir()
	inner := NewMemorySecretStore()
	seed := `{"version":2,"provider":"kimi","refresh_token":"rt-global","region":"global"}`
	if err := inner.Set("providerauth:kimi", seed); err != nil {
		t.Fatal(err)
	}
	store := &regionDeleteStore{SecretStore: inner, deleteErr: errors.New("keychain delete blocked")}
	if _, err := NewTokenManager(ManagerConfig{
		Profile: KimiForRegion(KimiRegionMainlandCN), Store: store, AgentDir: agentDir,
	}); err == nil {
		t.Fatal("manager construction succeeded although the mismatched credential could not be cleared")
	}
	tombstone := filepath.Join(agentDir, "kimi_auth_region_rejection.json")
	if _, err := os.Stat(tombstone); err != nil {
		t.Fatalf("durable region rejection missing: %v", err)
	}

	store.deleteErr = nil
	global, err := NewTokenManager(ManagerConfig{Profile: Kimi(), Store: store, AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	if global.Status().SignedIn {
		t.Fatal("old credential resurrected after a failed region rejection cleanup")
	}
	if _, err := store.Get("providerauth:kimi"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("old credential was not cleared on retry: %v", err)
	}
	global.tokens = &Tokens{RefreshToken: "rt-fresh"}
	if err := global.persistLocked(); err != nil {
		t.Fatalf("fresh sign-in persist: %v", err)
	}
	if _, err := os.Stat(tombstone); !os.IsNotExist(err) {
		t.Fatalf("fresh sign-in did not clear the region rejection: %v", err)
	}
}

func TestKimiLegacyGlobalCredentialRestores(t *testing.T) {
	store := NewMemorySecretStore()
	seed := `{"version":2,"provider":"kimi","refresh_token":"rt-global"}`
	if err := store.Set("providerauth:kimi", seed); err != nil {
		t.Fatal(err)
	}
	m, err := NewTokenManager(ManagerConfig{
		Profile: Kimi(), Store: store, AgentDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Status().SignedIn {
		t.Fatal("pre-region global credential must remain restorable in the global region")
	}
}
