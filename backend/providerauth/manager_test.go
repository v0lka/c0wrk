package providerauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// counts refreshes; invalidGrant flips refresh replies to invalid_grant,
// refreshExpiresIn overrides the refresh grant's expires_in (the constructor
// pins 3600; the bounded-rounds test sets 10). Each authorization_code grant
// mints a DISTINCT token pair (at-N/rt-N, N counting up from 1) so tests can
// distinguish one account's credentials from another's.
type managerIssuer struct {
	*httptest.Server
	idToken          string
	refreshCalls     atomic.Int64
	codeGrants       atomic.Int64
	refreshGrants    atomic.Int64
	invalidGrant     atomic.Bool
	refreshExpiresIn atomic.Int64
}

func newManagerIssuer(t *testing.T, idToken string) *managerIssuer {
	t.Helper()
	iss := &managerIssuer{idToken: idToken}
	iss.refreshExpiresIn.Store(3600)
	iss.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			n := iss.codeGrants.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  fmt.Sprintf("at-%d", n),
				"refresh_token": fmt.Sprintf("rt-%d", n),
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
			// Distinct from every code grant (at-N/rt-N): a refreshed pair
			// is at-rN/rt-rN, so a stale-epoch refresh committing over a
			// newer sign-in is observable in both memory and the store.
			rn := iss.refreshGrants.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  fmt.Sprintf("at-r%d", rn),
				"refresh_token": fmt.Sprintf("rt-r%d", rn),
				"token_type":    "Bearer",
				"expires_in":    iss.refreshExpiresIn.Load(),
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
	if bt, err := m2.Token(ctx); err != nil || bt.AccessToken != "at-r1" {
		t.Fatalf("restored Token = %+v, %v; want at-r1 (refreshed from the stored refresh token)", bt, err)
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
	if bt.AccessToken != "at-r2" {
		t.Errorf("Token after refresh = %q, want at-r2", bt.AccessToken)
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
		if tokens[i] != "at-r3" {
			t.Errorf("concurrent Token[%d] = %q, want at-r3", i, tokens[i])
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

// corruptThenFailingStore serves one pre-seeded unreadable record from Get
// (so the constructor records a load warning) and fails every Set — the
// replacement sign-in's persist then fails against the still-present record.
type corruptThenFailingStore struct {
	seed string
}

func (s *corruptThenFailingStore) Get(string) (string, error) { return s.seed, nil }
func (s *corruptThenFailingStore) Set(string, string) error {
	return errors.New("dbus: connection refused")
}
func (s *corruptThenFailingStore) Delete(string) error { return nil }

// TestTokenManager_SignInPersistFailureKeepsLoadWarning pins the
// persist-failure rollback's warning handling: a manager that started with an
// unreadable stored record keeps the load warning after a failed sign-in —
// the failed store write left that record in the keychain, so the warning
// still names the remediation instead of silently clearing the status.
func TestTokenManager_SignInPersistFailureKeepsLoadWarning(t *testing.T) {
	issuer := newManagerIssuer(t, "")
	clock := newManualClock()

	m, err := NewTokenManager(ManagerConfig{
		Profile:    testProfile(issuer.URL, 0),
		Store:      &corruptThenFailingStore{seed: "!!!"},
		HTTPClient: issuer.Client(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        clock.Now,
	})
	if err != nil {
		t.Fatalf("NewTokenManager with an unreadable record: %v", err)
	}
	warning := m.LoadWarning()
	if !strings.Contains(warning, "sign in again") {
		t.Fatalf("LoadWarning before the sign-in = %q, want the sign-in-again hint", warning)
	}
	if _, err := m.SignIn(context.Background(), managerBrowser(issuer)); err == nil {
		t.Fatal("SignIn against a failing store must fail")
	}
	if st := m.Status(); st.SignedIn {
		t.Fatal("status must stay signed out after the failed persist")
	}
	if after := m.LoadWarning(); after != warning {
		t.Fatalf("LoadWarning after the failed persist = %q, want the pre-sign-in warning %q preserved", after, warning)
	}
}

// TestTokenManager_LoadDegradesCorruptSecretToWarning pins the fail-soft
// load path: an unusable stored record (not JSON, or no refresh token) does
// NOT fail construction — the manager starts signed out and usable, the
// problem surfaces as the load warning, and the record is replaceable by a
// sign-in and removable by a sign-out.
func TestTokenManager_LoadDegradesCorruptSecretToWarning(t *testing.T) {
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
		m, err := NewTokenManager(ManagerConfig{
			Profile:    testProfile(issuer.URL, 0),
			Store:      store,
			HTTPClient: issuer.Client(),
			Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
			Now:        clock.Now,
		})
		if err != nil {
			t.Errorf("%s: constructor error = %v, want a usable signed-out manager (the corrupt record is recoverable in-app)", name, err)
			continue
		}
		if st := m.Status(); st.SignedIn {
			t.Errorf("%s: status = signed in, want signed out", name)
		}
		warning := m.LoadWarning()
		if !strings.Contains(warning, "sign in again") {
			t.Errorf("%s: LoadWarning = %q, want a sign-in-again hint", name, warning)
		}
		// The corrupt record must be replaceable: a fresh sign-in overwrites
		// it and lands a working signed-in state.
		if _, err := m.SignIn(context.Background(), managerBrowser(issuer)); err != nil {
			t.Errorf("%s: replacement SignIn: %v", name, err)
			continue
		}
		if st := m.Status(); !st.SignedIn {
			t.Errorf("%s: status after replacement sign-in = %+v, want signed in", name, st)
		}
		if m.LoadWarning() != "" {
			t.Errorf("%s: LoadWarning after replacement sign-in = %q, want empty", name, m.LoadWarning())
		}
		// And removable: sign-out deletes the record outright.
		if err := m.SignOut(); err != nil {
			t.Errorf("%s: SignOut: %v", name, err)
		}
		if _, err := store.Get("providerauth:chatgpt-test"); !errors.Is(err, ErrSecretNotFound) {
			t.Errorf("%s: stored secret after sign-out err = %v, want ErrSecretNotFound", name, err)
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
	if migrated.Version != 2 || migrated.Tokens != nil || migrated.RefreshToken != "rt-r1" {
		t.Fatalf("migrated secret = %+v, want v2 minimal with the rotated refresh token", migrated)
	}
	if strings.Contains(raw, "at-old") {
		t.Fatal("migrated secret still carries the legacy access token")
	}
}

// TestTokenManager_TokenHonorsContextCancellation verifies the renewal path
// propagates caller cancellation.
func TestTokenManager_TokenHonorsContextCancellation(t *testing.T) {
	entered := make(chan struct{})
	finished := make(chan struct{})
	release := make(chan struct{})
	unreachable := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		defer close(finished)
		if err := r.ParseForm(); err != nil {
			t.Errorf("refresh ParseForm: %v", err)
			return
		}
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(unreachable.Close)
	// Cleanup is LIFO: unblock the handler before waiting in Server.Close.
	t.Cleanup(func() { close(release) })

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

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := m.Token(ctx)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Token refresh did not enter the HTTP handler")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("in-flight Token err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Token did not conclude after cancellation")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP handler did not conclude after cancellation")
	}
}

// gatingStore wraps a SecretStore whose Set blocks until release is closed;
// setEntered is closed when the first Set arrives. Used to pin cancellation
// and ordering behavior around a (potentially prompt-blocked) keychain write.
type gatingStore struct {
	inner      SecretStore
	once       sync.Once
	setEntered chan struct{}
	release    chan struct{}
}

func newGatingStore(inner SecretStore) *gatingStore {
	return &gatingStore{
		inner:      inner,
		setEntered: make(chan struct{}),
		release:    make(chan struct{}),
	}
}

func (s *gatingStore) Get(key string) (string, error) { return s.inner.Get(key) }

func (s *gatingStore) Set(key, secret string) error {
	s.once.Do(func() { close(s.setEntered) })
	<-s.release
	return s.inner.Set(key, secret)
}

func (s *gatingStore) Delete(key string) error { return s.inner.Delete(key) }

// TestTokenManager_SignOutDuringRefreshKeepsAccountSignedOut pins the
// sign-out ordering guarantee: a sign-out that completes while a refresh's
// network round-trip is still in flight must leave the store WITHOUT
// credentials — the stale refresh result is discarded (generation guard) and
// never re-persisted, so a manager restored from the store afterwards is
// signed out, not silently signed back in.
func TestTokenManager_SignOutDuringRefreshKeepsAccountSignedOut(t *testing.T) {
	idToken := makeJWT(map[string]any{"sub": "user-1", "email": "dev@example.com"})
	issuer := newManagerIssuer(t, idToken)
	block := make(chan struct{})
	var releaseOnce sync.Once
	releaseRefresh := func() { releaseOnce.Do(func() { close(block) }) }
	t.Cleanup(releaseRefresh)
	refreshStarted := make(chan struct{})
	// Wrap the issuer's handler to gate refresh_token grants.
	origHandler := issuer.Config.Handler
	issuer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" {
			close(refreshStarted)
			<-block
		}
		origHandler.ServeHTTP(w, r)
	})

	store := NewMemorySecretStore()
	clock := newManualClock()
	m := newTestManager(t, issuer, store, clock)
	ctx := context.Background()
	if _, err := m.SignIn(ctx, managerBrowser(issuer)); err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	// Expire the access token so the next Token call must refresh.
	clock.Advance(7200 * time.Second)

	tokenErr := make(chan error, 1)
	go func() {
		_, err := m.Token(ctx)
		tokenErr <- err
	}()
	select {
	case <-refreshStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh never started")
	}

	// Sign out while the refresh is blocked mid-round-trip.
	if err := m.SignOut(); err != nil {
		t.Fatalf("SignOut: %v", err)
	}
	releaseRefresh()

	select {
	case err := <-tokenErr:
		if !errors.Is(err, ErrNotSignedIn) {
			t.Fatalf("Token during sign-out returned %v, want ErrNotSignedIn", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Token never concluded after sign-out")
	}

	// The durable state must be signed out: the store carries no record and a
	// freshly restored manager reports signed out.
	if _, err := store.Get("providerauth:chatgpt-test"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("stored secret after sign-out-during-refresh err = %v, want ErrSecretNotFound", err)
	}
	m2 := newTestManager(t, issuer, store, clock)
	if st := m2.Status(); st.SignedIn {
		t.Fatalf("restored status = %+v, want signed out (the stale refresh must not resurrect the account)", st)
	}
}

// TestTokenManager_QueuedTokenHonorsCancellation pins that a Token caller
// waiting behind another caller's in-flight refresh can cancel: it returns
// its own context error promptly instead of staying blocked until the
// (slow) refresh concludes.
func TestTokenManager_QueuedTokenHonorsCancellation(t *testing.T) {
	idToken := makeJWT(map[string]any{"sub": "user-1", "email": "dev@example.com"})
	issuer := newManagerIssuer(t, idToken)
	block := make(chan struct{})
	refreshStarted := make(chan struct{})
	origHandler := issuer.Config.Handler
	issuer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" {
			close(refreshStarted)
			<-block
		}
		origHandler.ServeHTTP(w, r)
	})

	store := NewMemorySecretStore()
	clock := newManualClock()
	m := newTestManager(t, issuer, store, clock)
	ctx := context.Background()
	if _, err := m.SignIn(ctx, managerBrowser(issuer)); err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	clock.Advance(7200 * time.Second)

	firstDone := make(chan error, 1)
	go func() {
		_, err := m.Token(ctx)
		firstDone <- err
	}()
	select {
	case <-refreshStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh never started")
	}

	waiterDone := make(chan error, 1)
	go func() {
		waiterCtx, cancel := context.WithCancel(context.Background())
		cancel() // already-cancelled context
		_, err := m.Token(waiterCtx)
		waiterDone <- err
	}()

	select {
	case err := <-waiterDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued Token returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued Token stayed blocked behind the in-flight refresh despite a cancelled context")
	}

	// The owning refresh still concludes successfully once unblocked.
	close(block)
	if err := <-firstDone; err != nil {
		t.Fatalf("owning Token: %v", err)
	}
}

// TestTokenManager_SignInCancelledDuringPersistRollsBack pins the
// cancellation-around-commit contract: a Cancel accepted while the keychain
// write is blocked must not conclude as a successful sign-in. The manager's
// state is rolled back — the previous account is restored when one existed,
// the record is deleted otherwise — and the cancellation error is returned.
// Each subtest builds its OWN issuer so the per-grant token counter (at-N /
// rt-N, N from 1) starts fresh: the assertions compare against the literal
// first/second grant values.
func TestTokenManager_SignInCancelledDuringPersistRollsBack(t *testing.T) {
	idToken := makeJWT(map[string]any{"sub": "user-1", "email": "dev@example.com"})

	t.Run("no_previous_account_deletes_record", func(t *testing.T) {
		issuer := newManagerIssuer(t, idToken)
		inner := NewMemorySecretStore()
		store := newGatingStore(inner)
		clock := newManualClock()
		m := newTestManager(t, issuer, inner, clock)
		m.store = store

		ctx, cancel := context.WithCancel(context.Background())
		signInDone := make(chan error, 1)
		go func() {
			_, err := m.SignIn(ctx, managerBrowser(issuer))
			signInDone <- err
		}()
		select {
		case <-store.setEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("persist never started")
		}
		cancel()
		close(store.release)

		select {
		case err := <-signInDone:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("SignIn returned %v, want context.Canceled", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("SignIn never concluded")
		}
		if st := m.Status(); st.SignedIn {
			t.Fatalf("status after cancelled sign-in = %+v, want signed out (rolled back)", st)
		}
		if _, err := inner.Get("providerauth:chatgpt-test"); !errors.Is(err, ErrSecretNotFound) {
			t.Fatalf("stored secret after cancelled sign-in err = %v, want ErrSecretNotFound (rolled back)", err)
		}
	})

	t.Run("previous_account_restored", func(t *testing.T) {
		issuer := newManagerIssuer(t, idToken)
		inner := NewMemorySecretStore()
		clock := newManualClock()
		m := newTestManager(t, issuer, inner, clock)
		ctx := context.Background()
		// Establish a first account.
		if _, err := m.SignIn(ctx, managerBrowser(issuer)); err != nil {
			t.Fatalf("first SignIn: %v", err)
		}

		store := newGatingStore(inner)
		m.store = store
		cancelCtx, cancel := context.WithCancel(context.Background())
		signInDone := make(chan error, 1)
		go func() {
			_, err := m.SignIn(cancelCtx, managerBrowser(issuer))
			signInDone <- err
		}()
		select {
		case <-store.setEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("persist never started")
		}
		cancel()
		close(store.release)

		select {
		case err := <-signInDone:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("SignIn returned %v, want context.Canceled", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("SignIn never concluded")
		}
		// The previous account survives the rollback, in memory and in the store.
		// The issuer mints a DISTINCT pair per grant (rt-1 for the first
		// sign-in, rt-2 for the cancelled one), so the store assertion below
		// genuinely distinguishes "rolled back to rt-1" from "no rollback
		// (rt-2 leaked through)" — and the live token must be the first
		// grant's at-1, not the cancelled second's.
		if st := m.Status(); !st.SignedIn {
			t.Fatalf("status after cancelled re-sign-in = %+v, want the previous account restored", st)
		}
		raw, err := inner.Get("providerauth:chatgpt-test")
		if err != nil {
			t.Fatalf("stored secret after rollback: %v", err)
		}
		var persisted persistedAuth
		if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
			t.Fatalf("stored secret is not persistedAuth JSON: %v", err)
		}
		if persisted.RefreshToken != "rt-1" {
			t.Errorf("persisted refresh token = %q, want the previous account's rt-1 (a leaked rt-2 means the rollback never ran)", persisted.RefreshToken)
		}
		bt, err := m.Token(context.Background())
		if err != nil {
			t.Fatalf("Token after the rollback: %v", err)
		}
		if bt.AccessToken != "at-1" {
			t.Errorf("live access token after the rollback = %q, want the previous account's at-1 (the cancelled grant's at-2 must not survive in memory)", bt.AccessToken)
		}
	})
}

// TestTokenManager_ReSignInDuringRefreshKeepsNewerAccount pins the
// generation half of the epoch guard: a refresh whose network round-trip
// straddles a SECOND sign-in must not commit the stale epoch's tokens over
// the newer account — neither in memory (the stale access token must not
// serve) nor in the store (the newer account's refresh token must survive).
func TestTokenManager_ReSignInDuringRefreshKeepsNewerAccount(t *testing.T) {
	idToken := makeJWT(map[string]any{"sub": "user-1", "email": "dev@example.com"})
	issuer := newManagerIssuer(t, idToken)
	block := make(chan struct{})
	refreshStarted := make(chan struct{})
	origHandler := issuer.Config.Handler
	issuer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" {
			close(refreshStarted)
			<-block
		}
		origHandler.ServeHTTP(w, r)
	})

	store := NewMemorySecretStore()
	clock := newManualClock()
	m := newTestManager(t, issuer, store, clock)
	ctx := context.Background()
	// First account (grant 1: rt-1/at-1).
	if _, err := m.SignIn(ctx, managerBrowser(issuer)); err != nil {
		t.Fatalf("first SignIn: %v", err)
	}
	// Expire the access token so the next Token call must refresh.
	clock.Advance(7200 * time.Second)

	tokenDone := make(chan error, 1)
	go func() {
		_, err := m.Token(ctx)
		tokenDone <- err
	}()
	select {
	case <-refreshStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh never started")
	}

	// A SECOND sign-in completes while the refresh round-trip is blocked:
	// grant 2 (rt-2/at-2) commits and persists.
	if _, err := m.SignIn(ctx, managerBrowser(issuer)); err != nil {
		t.Fatalf("second SignIn during the blocked refresh: %v", err)
	}

	close(block)
	select {
	case <-tokenDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Token never concluded after the refresh unblocked")
	}

	// The newer account survives, in memory and in the store: the stale
	// epoch's refreshed tokens were discarded at the commit check.
	raw, err := store.Get("providerauth:chatgpt-test")
	if err != nil {
		t.Fatalf("stored secret: %v", err)
	}
	var persisted persistedAuth
	if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
		t.Fatalf("stored secret is not persistedAuth JSON: %v", err)
	}
	if persisted.RefreshToken != "rt-2" {
		t.Errorf("persisted refresh token = %q, want the newer account's rt-2 (a stale-epoch refresh must not overwrite it)", persisted.RefreshToken)
	}
	bt, err := m.Token(ctx)
	if err != nil {
		t.Fatalf("Token after the interleaved sign-in: %v", err)
	}
	// The newer account's access token (code grant 2 = at-2) is still valid
	// under the manual clock; a committed stale refresh would have rotated
	// it to the refresh grant's at-r1.
	if bt.AccessToken != "at-2" {
		t.Errorf("served access token = %q, want the newer account's at-2 (a committed stale refresh would have rotated it to at-r1)", bt.AccessToken)
	}
}

// refreshWaiterContext acknowledges evaluation of the cancellation arm in
// Token's in-flight wait select. The leader already holds the real HTTP gate;
// these contexts belong only to its followers, not to the network refresher.
// Embedding preserves the caller's cancellation, deadline and values.
type refreshWaiterContext struct {
	context.Context
	admitted chan<- struct{}
	once     sync.Once
}

func (c *refreshWaiterContext) Done() <-chan struct{} {
	c.once.Do(func() { c.admitted <- struct{}{} })
	return c.Context.Done()
}

// TestTokenManager_FailedRefreshIsSharedByWaiters pins that single-flight
// covers the FAILURE path too: when the in-flight refresh fails (e.g.
// invalid_grant), every waiter woken by it returns the SAME error instead of
// each starting its own retry — one network round-trip total.
func TestTokenManager_FailedRefreshIsSharedByWaiters(t *testing.T) {
	idToken := makeJWT(map[string]any{"sub": "user-1", "email": "dev@example.com"})
	issuer := newManagerIssuer(t, idToken)
	issuer.invalidGrant.Store(true)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce, enterOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock) // Release before issuer.Close, including setup failures.
	original := issuer.Config.Handler
	issuer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("refresh ParseForm() = %v, want nil", err)
			return
		}
		if r.Form.Get("grant_type") == "refresh_token" {
			enterOnce.Do(func() { close(entered) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		original.ServeHTTP(w, r)
	})

	store := NewMemorySecretStore()
	clock := newManualClock()
	m := newTestManager(t, issuer, store, clock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := m.SignIn(ctx, managerBrowser(issuer)); err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	clock.Advance(7200 * time.Second)

	const waiters = 4
	errs := make([]error, waiters)
	admitted := make(chan struct{}, waiters-1) // One acknowledgement per follower.
	var wg sync.WaitGroup
	launch := func(i int, caller context.Context) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = m.Token(caller)
		}()
	}
	launch(0, ctx) // Establish the leader before admitting any follower.
	t.Cleanup(func() {
		unblock()
		cancel()
		done := make(chan struct{})
		go func() { defer close(done); wg.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Token callers did not join after refresh release/cancellation")
		}
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("leader refresh did not enter the real HTTP handler")
	}
	for i := 1; i < waiters; i++ {
		launch(i, &refreshWaiterContext{Context: ctx, admitted: admitted})
	}
	for range waiters - 1 {
		select {
		case <-admitted:
		case <-time.After(5 * time.Second):
			t.Fatal("Token follower did not enter the in-flight wait select")
		}
	}
	unblock()
	wg.Wait() // All callers complete before result and network-count assertions.
	for i, err := range errs {
		if !errors.Is(err, ErrReauthRequired) {
			t.Errorf("waiter %d err = %v, want ErrReauthRequired (shared failure)", i, err)
		}
	}
	if got := issuer.refreshCalls.Load(); got != 1 {
		t.Errorf("refresh calls = %d, want 1 (the failed refresh is shared, not retried per waiter)", got)
	}
}

// TestTokenManager_TokenBoundedRefreshRounds pins the anti-spin bound: when
// every renewal lands back inside the expiry skew (a short-lived-token
// issuer here — expires_in below the 30 s renewal skew), Token must fail
// with the actionable bound error after exactly maxTokenRefreshRounds
// round-trips instead of hammering the token endpoint until the caller's
// own deadline. Without the bound this test hangs on the refresh loop.
func TestTokenManager_TokenBoundedRefreshRounds(t *testing.T) {
	issuer := newManagerIssuer(t, "")
	// Every refreshed token expires 10 s out — inside the 30 s skew.
	issuer.refreshExpiresIn.Store(10)
	store := NewMemorySecretStore()
	clock := newManualClock()
	ctx := context.Background()

	m := newTestManager(t, issuer, store, clock)
	if _, err := m.SignIn(ctx, managerBrowser(issuer)); err != nil {
		t.Fatalf("SignIn: %v", err)
	}

	// The sign-in token (expires_in 3600) ages out; every refresh from here
	// lands back inside the skew.
	clock.Advance(2 * time.Hour)

	_, err := m.Token(ctx)
	if err == nil {
		t.Fatal("Token succeeded although every renewal lands inside the renewal skew")
	}
	if !strings.Contains(err.Error(), "renewal skew") {
		t.Errorf("Token error = %v, want the actionable renewal-skew bound message", err)
	}
	if calls := issuer.refreshCalls.Load(); calls != maxTokenRefreshRounds {
		t.Errorf("refresh round-trips = %d, want exactly %d (the bound must stop the spin, not retry less)", calls, maxTokenRefreshRounds)
	}
}

// deleteFailingStore fails every Delete but passes Get/Set through, so a
// sign-out against a refusing keychain can be simulated.
type deleteFailingStore struct {
	inner     SecretStore
	deleteErr error
}

func (s *deleteFailingStore) Get(key string) (string, error) { return s.inner.Get(key) }
func (s *deleteFailingStore) Set(key, secret string) error   { return s.inner.Set(key, secret) }
func (s *deleteFailingStore) Delete(key string) error        { return s.deleteErr }

// TestTokenManager_SignOutKeepsLoadWarningWhenDeleteFails pins the
// failed-delete branch: the corrupt record stays in the keychain, so the
// load warning describing it must survive the failed sign-out (the manager
// stays signed in, and the warning keeps naming the remediation) and clear
// only after a sign-out whose delete succeeds.
func TestTokenManager_SignOutKeepsLoadWarningWhenDeleteFails(t *testing.T) {
	issuer := newManagerIssuer(t, "")
	inner := NewMemorySecretStore()
	if err := inner.Set("providerauth:chatgpt-test", "!!!"); err != nil {
		t.Fatalf("seeding the unreadable record: %v", err)
	}
	store := &deleteFailingStore{inner: inner, deleteErr: errors.New("dbus: service unavailable")}
	clock := newManualClock()

	m, err := NewTokenManager(ManagerConfig{
		Profile:    testProfile(issuer.URL, 0),
		Store:      store,
		HTTPClient: issuer.Client(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        clock.Now,
	})
	if err != nil {
		t.Fatalf("NewTokenManager with an unreadable record: %v", err)
	}
	warning := m.LoadWarning()
	if !strings.Contains(warning, "sign in again") {
		t.Fatalf("LoadWarning before the sign-out = %q, want the sign-in-again hint", warning)
	}

	if err := m.SignOut(); err == nil {
		t.Fatal("SignOut against a failing keychain must fail")
	}
	// The warning survives the failed delete: the record it describes is
	// still in the store (this manager loaded no tokens — the record was
	// unreadable — so the signed-in posture is unchanged either way; the
	// decisive assertion is the warning, which a premature clear would
	// have erased while the bad record still exists).
	if after := m.LoadWarning(); after != warning {
		t.Fatalf("LoadWarning after the failed sign-out = %q, want the pre-sign-out warning %q preserved", after, warning)
	}
	if _, err := inner.Get("providerauth:chatgpt-test"); err != nil {
		t.Fatalf("the unreadable record must still exist after the failed delete: %v", err)
	}

	// A sign-out whose delete succeeds clears the warning with the record.
	m.mu.Lock()
	store.deleteErr = nil
	m.mu.Unlock()
	if err := m.SignOut(); err != nil {
		t.Fatalf("SignOut after the keychain recovered: %v", err)
	}
	if after := m.LoadWarning(); after != "" {
		t.Fatalf("LoadWarning after the successful sign-out = %q, want empty (the record is gone)", after)
	}
}

// TestTokenManager_RefreshReturnsAfterACommittedRefresh pins the termination
// contract: a successful Refresh returns nil after exactly ONE round-trip —
// the loop must not treat the committed case as "superseded, re-evaluate"
// and spin (an earlier shape of this loop performed tens of thousands of
// back-to-back refreshes without returning).
func TestTokenManager_RefreshReturnsAfterACommittedRefresh(t *testing.T) {
	issuer := newManagerIssuer(t, "")
	store := NewMemorySecretStore()
	clock := newManualClock()
	m := newTestManager(t, issuer, store, clock)
	ctx := context.Background()
	if _, err := m.SignIn(ctx, managerBrowser(issuer)); err != nil {
		t.Fatalf("SignIn: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- m.Refresh(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Refresh after a committed refresh: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Refresh did not return after its committed round-trip — the loop spins")
	}
	if got := issuer.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh round-trips = %d, want exactly 1 (a committed refresh terminates the loop)", got)
	}
}

// TestTokenManager_RefreshBoundedSupersessionReleasesTheLock pins the
// round-bound exit: consecutive refresh rounds each superseded by a
// re-sign-in trip the bound with an actionable error, and — critically —
// Refresh must return WITHOUT the state lock: the branch runs under the
// lock taken at the loop top, so a plain return would wedge every later
// Token/Status/SignIn call forever (the companion test below asserts the
// manager stays answerable after the bounded error).
func TestTokenManager_RefreshBoundedSupersessionReleasesTheLock(t *testing.T) {
	idToken := makeJWT(map[string]any{"sub": "user-1", "email": "dev@example.com"})
	issuer := newManagerIssuer(t, idToken)
	store := NewMemorySecretStore()
	clock := newManualClock()
	m := newTestManager(t, issuer, store, clock)
	ctx := context.Background()
	if _, err := m.SignIn(ctx, managerBrowser(issuer)); err != nil {
		t.Fatalf("first SignIn: %v", err)
	}
	clock.Advance(7200 * time.Second)

	refreshDone := make(chan error, 1)
	// The gate parks ONLY refresh grants: the superseding sign-ins must run
	// their authorization_code exchanges while a refresh is parked.
	entered := make(chan struct{}, maxTokenRefreshRounds+1)
	release := make(chan struct{})
	orig := issuer.Config.Handler
	issuer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" {
			entered <- struct{}{}
			<-release
		}
		orig.ServeHTTP(w, r)
	})
	go func() { refreshDone <- m.Refresh(ctx) }()

	// Drive rounds until the bound trips: each parked refresh is overtaken
	// by a re-sign-in (a new epoch), so the commit check discards it and
	// the loop re-arms. The loop must conclude with the bounded error —
	// never a stall, never an early success.
	for round := 1; round <= maxTokenRefreshRounds+2; round++ {
		select {
		case err := <-refreshDone:
			if err == nil || !strings.Contains(err.Error(), "superseded") {
				t.Fatalf("Refresh under endless supersession = %v, want the bounded supersession error", err)
			}
			return
		case <-entered:
			if _, err := m.SignIn(ctx, managerBrowser(issuer)); err != nil {
				t.Fatalf("superseding SignIn in round %d: %v", round, err)
			}
			clock.Advance(7200 * time.Second)
			release <- struct{}{}
		case <-time.After(5 * time.Second):
			t.Fatalf("stalled after round %d", round)
		}
	}
	t.Fatal("Refresh kept arming rounds past the bound")
}

// TestTokenManager_RefreshBoundedSupersessionManagerStaysAnswerable is the
// companion to the bound test: after the bounded supersession error
// returns, the state lock must be free — every later Token/Status/SignIn
// call must still answer.
func TestTokenManager_RefreshBoundedSupersessionManagerStaysAnswerable(t *testing.T) {
	idToken := makeJWT(map[string]any{"sub": "user-1", "email": "dev@example.com"})
	issuer := newManagerIssuer(t, idToken)
	store := NewMemorySecretStore()
	clock := newManualClock()
	m := newTestManager(t, issuer, store, clock)
	ctx := context.Background()
	if _, err := m.SignIn(ctx, managerBrowser(issuer)); err != nil {
		t.Fatalf("first SignIn: %v", err)
	}
	clock.Advance(7200 * time.Second)

	entered := make(chan struct{}, maxTokenRefreshRounds+1)
	release := make(chan struct{})
	orig := issuer.Config.Handler
	issuer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" {
			entered <- struct{}{}
			<-release
		}
		orig.ServeHTTP(w, r)
	})
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- m.Refresh(ctx) }()
	for round := 1; round <= maxTokenRefreshRounds+2; round++ {
		select {
		case <-refreshDone:
			// The lock must be free: the manager still answers. A leaked
			// lock would block this Status call forever.
			statusDone := make(chan AccountStatus, 1)
			go func() { statusDone <- m.Status() }()
			select {
			case <-statusDone:
				return
			case <-time.After(5 * time.Second):
				t.Fatal("m.Status() blocked after Refresh returned — the state lock leaked")
			}
		case <-entered:
			if _, err := m.SignIn(ctx, managerBrowser(issuer)); err != nil {
				t.Fatalf("superseding SignIn in round %d: %v", round, err)
			}
			clock.Advance(7200 * time.Second)
			release <- struct{}{}
		case <-time.After(5 * time.Second):
			t.Fatal("stalled")
		}
	}
	t.Fatal("Refresh kept arming rounds past the bound")
}
