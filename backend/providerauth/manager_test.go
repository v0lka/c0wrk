package providerauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// manualClock is an injectable wall clock.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// managerIssuer answers authorization_code and refresh_token grants and
// counts refreshes; invalidGrant flips refresh replies to invalid_grant.
type managerIssuer struct {
	*httptest.Server
	idToken      string
	refreshCalls atomic.Int64
	invalidGrant atomic.Bool
}

func newManagerIssuer(t *testing.T, idToken string) *managerIssuer {
	t.Helper()
	iss := &managerIssuer{idToken: idToken}
	iss.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "at-1",
				"refresh_token": "rt-1",
				"id_token":      iss.idToken,
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		case "refresh_token":
			iss.refreshCalls.Add(1)
			if iss.invalidGrant.Load() {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token expired"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "at-2",
				"refresh_token": "rt-2",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		default:
			http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
		}
	}))
	t.Cleanup(iss.Close)
	return iss
}

func newTestManager(t *testing.T, issuer *managerIssuer, store SecretStore, clock *manualClock) *TokenManager {
	t.Helper()
	m, err := NewTokenManager(ManagerConfig{
		Profile:    testProfile(issuer.URL, 0),
		Store:      store,
		HTTPClient: issuer.Client(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        clock.Now,
	})
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	return m
}

func managerBrowser(issuer *managerIssuer) BrowserOpener {
	browser := &fakeBrowser{client: issuer.Client()}
	return browser.open
}

// TestTokenManager_FullCycle is the acceptance test: the whole
// sign-in → refresh → sign-out lifecycle against an httptest issuer and an
// in-memory store, with no real network. It also proves persistence across
// restart (a second manager loads the stored credentials) and the
// single-flight refresh guarantee.
func TestTokenManager_FullCycle(t *testing.T) {
	idToken := makeJWT(map[string]any{
		"sub":   "user-1",
		"email": "dev@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-1",
		},
	})
	issuer := newManagerIssuer(t, idToken)
	store := NewMemorySecretStore()
	clock := newManualClock()
	ctx := context.Background()

	m := newTestManager(t, issuer, store, clock)

	// Initially signed out.
	if _, err := m.Token(ctx); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("Token before sign-in err = %v, want ErrNotSignedIn", err)
	}

	// --- Sign in ---
	res, err := m.SignIn(ctx, managerBrowser(issuer))
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if res.Tokens.AccessToken != "at-1" {
		t.Fatalf("SignIn access token = %q, want at-1", res.Tokens.AccessToken)
	}
	if res.Identity == nil || res.Identity.Email != "dev@example.com" || res.Identity.ChatGPTAccountID != "acct-1" {
		t.Fatalf("SignIn identity = %+v", res.Identity)
	}

	// Persisted under the provider key, as versioned JSON. The stored
	// secret is refresh-token-only: no access or ID token material may
	// reach the SecretStore (OS keychain size caps — see persistedAuth).
	raw, err := store.Get("providerauth:chatgpt-test")
	if err != nil {
		t.Fatalf("stored secret missing: %v", err)
	}
	var persisted persistedAuth
	if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
		t.Fatalf("stored secret is not persistedAuth JSON: %v", err)
	}
	if persisted.Version != persistedAuthVersion || persisted.Provider != "chatgpt-test" {
		t.Errorf("persisted header = %+v", persisted)
	}
	if persisted.RefreshToken != "rt-1" {
		t.Errorf("persisted refresh token = %q, want rt-1", persisted.RefreshToken)
	}
	if strings.Contains(raw, "at-1") || strings.Contains(raw, idToken) {
		t.Errorf("persisted secret carries access/ID token material (%d bytes)", len(raw))
	}

	// --- Valid token served without refresh ---
	bt, err := m.Token(ctx)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if bt.AccessToken != "at-1" {
		t.Errorf("Token = %q, want at-1", bt.AccessToken)
	}
	if bt.ExtraHeaders["ChatGPT-Account-Id"] != "acct-1" {
		t.Errorf("ChatGPT-Account-Id header = %q, want acct-1", bt.ExtraHeaders["ChatGPT-Account-Id"])
	}
	if bt.ExtraHeaders["originator"] != "c0wrk" {
		t.Errorf("originator header = %q, want c0wrk", bt.ExtraHeaders["originator"])
	}
	if got := issuer.refreshCalls.Load(); got != 0 {
		t.Fatalf("refresh calls while token valid = %d, want 0", got)
	}

	// --- Persistence across restart ---
	// The restored manager starts from the refresh-token-only secret: it
	// reports signed in with the persisted identity and reconstructs the
	// live token set with exactly one refresh on first use.
	m2 := newTestManager(t, issuer, store, clock)
	if st := m2.Status(); !st.SignedIn || st.Identity == nil || st.Identity.ChatGPTAccountID != "acct-1" {
		t.Fatalf("restored status = %+v, want signed in with identity", st)
	}
	if !m2.Status().ExpiresAt.IsZero() {
		t.Fatal("restored status should carry no expiry before the first refresh")
	}
	if bt, err := m2.Token(ctx); err != nil || bt.AccessToken != "at-2" {
		t.Fatalf("restored Token = %+v, %v; want at-2 (refreshed from the stored refresh token)", bt, err)
	}
	if got := issuer.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls after restore = %d, want 1", got)
	}

	// --- Expiry → proactive refresh ---
	clock.Advance(3600 * time.Second)
	bt, err = m.Token(ctx)
	if err != nil {
		t.Fatalf("Token after expiry: %v", err)
	}
	if bt.AccessToken != "at-2" {
		t.Errorf("Token after refresh = %q, want at-2", bt.AccessToken)
	}
	if got := issuer.refreshCalls.Load(); got != 2 {
		t.Errorf("refresh calls = %d, want 2", got)
	}

	// --- Single-flight: concurrent Token calls share one refresh ---
	clock.Advance(3600 * time.Second)
	var wg sync.WaitGroup
	tokens := make([]string, 8)
	errs := make([]error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bt, err := m.Token(ctx)
			tokens[i], errs[i] = bt.AccessToken, err
		}()
	}
	wg.Wait()
	for i := range 8 {
		if errs[i] != nil {
			t.Fatalf("concurrent Token[%d] err = %v", i, errs[i])
		}
		if tokens[i] != "at-2" {
			t.Errorf("concurrent Token[%d] = %q, want at-2", i, tokens[i])
		}
	}
	if got := issuer.refreshCalls.Load(); got != 3 {
		t.Errorf("refresh calls after concurrent burst = %d, want 3 (single-flight)", got)
	}

	// --- Sign out ---
	if err := m.SignOut(); err != nil {
		t.Fatalf("SignOut: %v", err)
	}
	if _, err := store.Get("providerauth:chatgpt-test"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("stored secret after sign-out err = %v, want ErrSecretNotFound", err)
	}
	if _, err := m.Token(ctx); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("Token after sign-out err = %v, want ErrNotSignedIn", err)
	}
	if st := m.Status(); st.SignedIn {
		t.Fatalf("status after sign-out = %+v, want signed out", st)
	}
	// Idempotent.
	if err := m.SignOut(); err != nil {
		t.Fatalf("second SignOut: %v", err)
	}
}

// TestTokenManager_PersistedSecretFitsKeychainLimits pins the regression
// behind the macOS "data passed to Set was too big" failure: a full ChatGPT
// token set (multi-kilobyte access and ID JWTs) must never reach the
// SecretStore. The persisted secret is refresh-token-only and stays far
// below every OS keychain bridge cap (the darwin `security` command budget
// of 4096 bytes, the Windows credential blob cap of 2560 bytes), and a
// restored manager still serves requests through one refresh.
func TestTokenManager_PersistedSecretFitsKeychainLimits(t *testing.T) {
	fatAccess := "eyJhbGciOiJSUzI1NiIs" + strings.Repeat("Y", 4096)
	idToken := makeJWT(map[string]any{
		"sub":   "user-1",
		"email": "dev@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-1",
		},
	})
	iss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  fatAccess,
				"refresh_token": "rt-fat",
				"id_token":      idToken,
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		case "refresh_token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "at-lean",
				"refresh_token": "rt-fat",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		default:
			http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
		}
	}))
	t.Cleanup(iss.Close)

	store := NewMemorySecretStore()
	clock := newManualClock()
	ctx := context.Background()

	newManager := func() *TokenManager {
		t.Helper()
		m, err := NewTokenManager(ManagerConfig{
			Profile:    testProfile(iss.URL, 0),
			Store:      store,
			HTTPClient: iss.Client(),
			Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
			Now:        clock.Now,
		})
		if err != nil {
			t.Fatalf("NewTokenManager: %v", err)
		}
		return m
	}

	m := newManager()
	if _, err := m.SignIn(ctx, (&fakeBrowser{client: iss.Client()}).open); err != nil {
		t.Fatalf("SignIn with multi-kilobyte tokens: %v", err)
	}

	raw, err := store.Get("providerauth:chatgpt-test")
	if err != nil {
		t.Fatalf("stored secret missing: %v", err)
	}
	if len(raw) >= 2560 {
		t.Fatalf("persisted secret is %d bytes; it must stay below the 2560-byte Windows credential-blob cap (and the 4096-byte darwin command budget)", len(raw))
	}
	if strings.Contains(raw, fatAccess) || strings.Contains(raw, idToken) {
		t.Fatal("persisted secret carries access/ID token material")
	}

	// The lean secret still restores a working manager: one refresh on
	// first use serves the request.
	m2 := newManager()
	if st := m2.Status(); !st.SignedIn || st.Identity == nil || st.Identity.Email != "dev@example.com" {
		t.Fatalf("restored status = %+v, want signed in with identity", st)
	}
	bt, err := m2.Token(ctx)
	if err != nil || bt.AccessToken != "at-lean" {
		t.Fatalf("restored Token = %+v, %v; want at-lean", bt, err)
	}
}

// TestTokenManager_SkewRefreshesBeforeExpiry pins the skew semantics: with
// expires_in=100 s and skew=30 s, a token 71 s old is already refreshed,
// one 60 s old is not.
func TestTokenManager_SkewRefreshesBeforeExpiry(t *testing.T) {
	shortLived := newShortLivedIssuer(t)
	clock := newManualClock()
	ctx := context.Background()

	m := newTestManager(t, shortLived, NewMemorySecretStore(), clock)
	if _, err := m.SignIn(ctx, managerBrowser(shortLived)); err != nil {
		t.Fatalf("SignIn: %v", err)
	}

	// 60 s into a 100 s life: outside the 30 s skew — no refresh.
	clock.Advance(60 * time.Second)
	if _, err := m.Token(ctx); err != nil {
		t.Fatalf("Token at 60 s: %v", err)
	}
	if got := shortLived.refreshCalls.Load(); got != 0 {
		t.Fatalf("refresh at 60 s of a 100 s token = %d, want 0 (outside skew)", got)
	}

	// 71 s: inside the skew — exactly one refresh.
	clock.Advance(11 * time.Second)
	if _, err := m.Token(ctx); err != nil {
		t.Fatalf("Token at 71 s: %v", err)
	}
	if got := shortLived.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1 (inside 30 s skew before 100 s expiry)", got)
	}
}

// newShortLivedIssuer issues 100 s access tokens.
func newShortLivedIssuer(t *testing.T) *managerIssuer {
	t.Helper()
	iss := &managerIssuer{idToken: ""}
	iss.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at-short", "refresh_token": "rt-short", "token_type": "Bearer", "expires_in": 100,
			})
		case "refresh_token":
			iss.refreshCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at-short-2", "refresh_token": "rt-short", "token_type": "Bearer", "expires_in": 100,
			})
		default:
			http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
		}
	}))
	t.Cleanup(iss.Close)
	return iss
}

// TestTokenManager_InvalidGrantDemandsReauth verifies a rejected refresh
// token surfaces ErrReauthRequired (wrapped, actionable) while the stored
// state stays intact for an explicit sign-out.
func TestTokenManager_InvalidGrantDemandsReauth(t *testing.T) {
	issuer := newManagerIssuer(t, "")
	clock := newManualClock()
	ctx := context.Background()
	m := newTestManager(t, issuer, NewMemorySecretStore(), clock)

	if _, err := m.SignIn(ctx, managerBrowser(issuer)); err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	issuer.invalidGrant.Store(true)
	clock.Advance(3600 * time.Second)

	_, err := m.Token(ctx)
	if !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("Token err = %v, want ErrReauthRequired", err)
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("err should name the OAuth code: %v", err)
	}
	var tErr *TokenEndpointError
	if !errors.As(err, &tErr) {
		t.Errorf("err should expose *TokenEndpointError via errors.As: %v", err)
	}
	if st := m.Status(); !st.SignedIn {
		t.Error("status should stay signed in until explicit sign-out")
	}
}

// TestTokenManager_SignInRefusesWhenStoreUnavailable is the
// Linux-without-Secret-Service path: a sign-in that cannot persist must not
// report a signed-in account.
func TestTokenManager_SignInRefusesWhenStoreUnavailable(t *testing.T) {
	issuer := newManagerIssuer(t, "")
	storeErr := errors.New("dbus: connection refused")
	store := &setFailingStore{err: storeErr}
	clock := newManualClock()

	m, err := NewTokenManager(ManagerConfig{
		Profile:    testProfile(issuer.URL, 0),
		Store:      store,
		HTTPClient: issuer.Client(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        clock.Now,
	})
	if err != nil {
		t.Fatalf("NewTokenManager with empty store: %v", err)
	}
	if _, err := m.SignIn(context.Background(), managerBrowser(issuer)); !errors.Is(err, storeErr) {
		t.Fatalf("SignIn err = %v, want the store error", err)
	}
	if st := m.Status(); st.SignedIn {
		t.Fatal("status must not be signed in after a failed persist")
	}
}

type setFailingStore struct {
	err error
}

func (s *setFailingStore) Get(string) (string, error) { return "", ErrSecretNotFound }
func (s *setFailingStore) Set(string, string) error   { return s.err }
func (s *setFailingStore) Delete(string) error        { return nil }

// TestTokenManager_LoadRejectsCorruptSecret pins the fail-loud load path.
func TestTokenManager_LoadRejectsCorruptSecret(t *testing.T) {
	issuer := newManagerIssuer(t, "")
	clock := newManualClock()

	for name, seed := range map[string]string{
		"not json": "!!!",
		// A v1 full-set secret without a refresh token is as unusable as a
		// v2 one without it — both fail with the sign-in-again hint.
		"legacy v1, no refresh token": `{"version":1,"provider":"chatgpt-test","tokens":{"access_token":"at"}}`,
		"no refresh token":            `{"version":2,"provider":"chatgpt-test","identity":{"email":"dev@example.com"}}`,
	} {
		store := NewMemorySecretStore()
		if err := store.Set("providerauth:chatgpt-test", seed); err != nil {
			t.Fatalf("seeding store: %v", err)
		}
		_, err := NewTokenManager(ManagerConfig{
			Profile:    testProfile(issuer.URL, 0),
			Store:      store,
			HTTPClient: issuer.Client(),
			Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
			Now:        clock.Now,
		})
		if err == nil {
			t.Errorf("%s: expected load error, got nil", name)
			continue
		}
		if !strings.Contains(err.Error(), "sign in again") {
			t.Errorf("%s: err = %v, want a sign-in-again hint", name, err)
		}
	}
}

// TestTokenManager_LoadMigratesLegacyV1Shape pins the one-way migration: a
// v1 full-token-set secret (written before the keychain size fix, e.g. on
// Linux where the keychain has no small cap) is accepted through its
// refresh token and rewritten as the minimal v2 secret on the next persist.
func TestTokenManager_LoadMigratesLegacyV1Shape(t *testing.T) {
	issuer := newManagerIssuer(t, "")
	store := NewMemorySecretStore()
	if err := store.Set("providerauth:chatgpt-test", `{"version":1,"provider":"chatgpt-test","tokens":{"access_token":"at-old","refresh_token":"rt-1","token_type":"Bearer"},"identity":{"email":"dev@example.com"}}`); err != nil {
		t.Fatalf("seeding store: %v", err)
	}

	m := newTestManager(t, issuer, store, newManualClock())
	if st := m.Status(); !st.SignedIn {
		t.Fatal("a legacy v1 secret with a refresh token must restore a signed-in state")
	}

	// First use refreshes (no access token is carried anymore) and the
	// persist that follows rewrites the secret in the minimal v2 shape.
	if _, err := m.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	raw, err := store.Get("providerauth:chatgpt-test")
	if err != nil {
		t.Fatalf("stored secret missing: %v", err)
	}
	var migrated persistedAuth
	if err := json.Unmarshal([]byte(raw), &migrated); err != nil {
		t.Fatalf("stored secret is not persistedAuth JSON: %v", err)
	}
	if migrated.Version != 2 || migrated.Tokens != nil || migrated.RefreshToken != "rt-2" {
		t.Fatalf("migrated secret = %+v, want v2 minimal with the rotated refresh token", migrated)
	}
	if strings.Contains(raw, "at-old") {
		t.Fatal("migrated secret still carries the legacy access token")
	}
}

// TestTokenManager_TokenHonorsContextCancellation verifies the renewal path
// propagates caller cancellation.
func TestTokenManager_TokenHonorsContextCancellation(t *testing.T) {
	unreachable := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(30 * time.Second)
	}))
	t.Cleanup(unreachable.Close)

	// Seed a signed-in state whose persisted secret is refresh-token-only,
	// so Token() must refresh (there is no stored access token at all).
	store := NewMemorySecretStore()
	data, err := json.Marshal(persistedAuth{
		Version:      persistedAuthVersion,
		Provider:     "chatgpt-test",
		RefreshToken: "rt",
	})
	if err != nil {
		t.Fatalf("marshaling seed: %v", err)
	}
	if err := store.Set("providerauth:chatgpt-test", string(data)); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	m, err := NewTokenManager(ManagerConfig{
		Profile:    testProfile(unreachable.URL, 0),
		Store:      store,
		HTTPClient: unreachable.Client(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Token(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Token with cancelled ctx err = %v, want context.Canceled", err)
	}
}
