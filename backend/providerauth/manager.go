package providerauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/v0lka/sp4rk/llm"
)

// DefaultExpirySkew is how long before the reported expiry a token is
// considered stale and refreshed proactively.
const DefaultExpirySkew = 30 * time.Second

// persistedAuthVersion is the on-disk schema version of the stored secret.
const persistedAuthVersion = 2

// ErrNotSignedIn reports that Token was called with no stored credentials.
var ErrNotSignedIn = errors.New("providerauth: not signed in")

// persistedAuth is the JSON shape stored in the SecretStore. It contains
// secrets and must never be logged.
//
// The shape is DELIBERATELY minimal: the refresh token plus the parsed
// identity — never the access or ID token. OS keychain bridges cap secret
// sizes far below a full ChatGPT token set (go-keyring's darwin backend
// shells out to `security add-generic-password` with a 4096-byte command
// budget, and its Windows backend caps the credential blob at 2560 bytes),
// while the access and ID tokens alone are multi-kilobyte JWTs. A restored
// sign-in reconstructs the live token set with one refresh round-trip on
// first use, so nothing durable is lost by leaving them out.
type persistedAuth struct {
	Version      int    `json:"version"`
	Provider     string `json:"provider"`
	RefreshToken string `json:"refresh_token,omitempty"`
	// Tokens is the legacy v1 full-token-set shape, read for one-way
	// migration only: a v1 secret written before the size fix is accepted
	// through its refresh token and rewritten as the minimal v2 secret on
	// the next persist. Never written anymore.
	Tokens   *Tokens   `json:"tokens,omitempty"`
	Identity *Identity `json:"identity,omitempty"`
}

// ManagerConfig assembles a TokenManager.
type ManagerConfig struct {
	// Profile identifies the provider (endpoints, client id, secret key).
	Profile Profile
	// Store persists tokens across restarts; required.
	Store SecretStore
	// HTTPClient is used for token-endpoint calls; defaults to a bounded
	// 30 s client. Callers with TLS/proxy policies should supply their own.
	HTTPClient *http.Client
	// Logger receives lifecycle events; defaults to slog.Default(). Secret
	// values are never logged.
	Logger *slog.Logger
	// Now overrides the wall clock (tests only); defaults to time.Now.
	Now func() time.Time
	// ExpirySkew refreshes this long before the reported expiry; defaults to
	// DefaultExpirySkew.
	ExpirySkew time.Duration
}

// TokenManager owns the OAuth token lifecycle of one subscription provider:
// browser sign-in, proactive refresh before expiry (with skew), persistence
// to the SecretStore, and sign-out. It implements llm.TokenSource.
//
// Concurrency: Token, SignIn, SignOut, and Refresh are safe for concurrent
// use. A refresh is single-flight — concurrent callers block on one network
// round-trip and share its result, so at most one refresh is in flight at any
// time.
type TokenManager struct {
	profile Profile
	store   SecretStore
	client  *http.Client
	logger  *slog.Logger
	now     func() time.Time
	skew    time.Duration

	mu       sync.Mutex
	tokens   *Tokens
	identity *Identity
}

// Compile-time assertion that TokenManager satisfies sp4rk's TokenSource.
var _ llm.TokenSource = (*TokenManager)(nil)

// NewTokenManager builds a manager and loads any persisted credentials.
// A missing stored secret (signed out) is not an error; a failing store
// (e.g. Linux without a Secret Service) surfaces its actionable error here.
func NewTokenManager(cfg ManagerConfig) (*TokenManager, error) {
	if cfg.Profile.ProviderID == "" {
		return nil, errors.New("providerauth: ManagerConfig.Profile is required")
	}
	if cfg.Store == nil {
		return nil, errors.New("providerauth: ManagerConfig.Store is required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = defaultHTTPClient()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ExpirySkew <= 0 {
		cfg.ExpirySkew = DefaultExpirySkew
	}

	m := &TokenManager{
		profile: cfg.Profile,
		store:   cfg.Store,
		client:  cfg.HTTPClient,
		logger:  cfg.Logger,
		now:     cfg.Now,
		skew:    cfg.ExpirySkew,
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

// load restores persisted credentials, if any. The persisted secret is
// refresh-token-only (see persistedAuth), so a restored manager starts with
// no access token and refreshes on its first Token call.
func (m *TokenManager) load() error {
	raw, err := m.store.Get(m.profile.secretKey())
	if errors.Is(err, ErrSecretNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var auth persistedAuth
	if err := json.Unmarshal([]byte(raw), &auth); err != nil {
		return fmt.Errorf("providerauth: stored %s credentials are unreadable (sign in again to replace them): %w", m.profile.ProviderID, err)
	}
	refresh := auth.RefreshToken
	if refresh == "" && auth.Tokens != nil {
		// Legacy v1 full-set secret (written before the keychain size fix):
		// migrate through its refresh token; the next persist rewrites it
		// as the minimal v2 shape.
		refresh = auth.Tokens.RefreshToken
	}
	if refresh == "" {
		return fmt.Errorf("providerauth: stored %s credentials carry no refresh token (sign in again to replace them)", m.profile.ProviderID)
	}
	m.tokens = &Tokens{RefreshToken: refresh}
	m.identity = auth.Identity
	return nil
}

// Token returns a currently valid access token, refreshing it first when it
// is within the expiry skew. It implements sp4rk's llm.TokenSource: the ctx
// is honored during a renewal round-trip, and the returned BearerToken
// carries the ChatGPT-Account-Id and originator headers the provider stamps
// onto outgoing requests.
func (m *TokenManager) Token(ctx context.Context) (llm.BearerToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureFreshLocked(ctx); err != nil {
		return llm.BearerToken{}, err
	}
	return m.bearerTokenLocked(), nil
}

// SignIn runs the browser OAuth flow and persists the issued credentials
// (the refresh token and parsed identity only — see persistedAuth; the
// multi-kilobyte access/ID JWTs never reach the SecretStore).
// The browser flow runs WITHOUT the manager lock, so Token() callers keep
// getting their (possibly ErrNotSignedIn) answer instead of blocking for the
// whole interactive sign-in. The store write happens before the in-memory
// update, so a failing keychain (e.g. Linux without a Secret Service) aborts
// the sign-in with an actionable error instead of leaving unpersisted
// credentials behind.
func (m *TokenManager) SignIn(ctx context.Context, opener BrowserOpener) (*SignInResult, error) {
	flow := &Flow{profile: m.profile, client: m.client, opener: opener, now: m.now}
	if flow.opener == nil {
		flow.opener = DefaultBrowserOpener
	}
	result, err := flow.SignIn(ctx)
	if err != nil {
		return nil, err
	}
	if result.Tokens.RefreshToken == "" {
		// The persisted secret is refresh-token-only, so a sign-in the
		// issuer issued no refresh token for cannot be restored after a
		// restart — it must not be reported as a signed-in account.
		return nil, errors.New("providerauth: the issuer issued no refresh token; this sign-in cannot be persisted, retry it")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens = result.Tokens
	m.identity = result.Identity
	if err := m.persistLocked(); err != nil {
		// Fail the sign-in: tokens that cannot be persisted must not be
		// treated as a signed-in account.
		m.tokens = nil
		m.identity = nil
		return nil, err
	}
	m.logger.Info("provider sign-in complete",
		"provider", m.profile.ProviderID,
		"account_id", identityField(result.Identity, func(i *Identity) string { return i.ChatGPTAccountID }),
		"email", identityField(result.Identity, func(i *Identity) string { return i.Email }),
		"expires_at", result.Tokens.Expiry.Format(time.RFC3339))
	return result, nil
}

// identityField extracts a display field from a possibly-nil Identity.
func identityField(id *Identity, get func(*Identity) string) string {
	if id == nil {
		return ""
	}
	return get(id)
}

// SignOut clears the persisted credentials and the in-memory state. It is
// idempotent: signing out while already signed out succeeds.
func (m *TokenManager) SignOut() error {
	err := m.store.Delete(m.profile.secretKey())
	if err != nil && !errors.Is(err, ErrSecretNotFound) {
		return err
	}
	m.mu.Lock()
	m.tokens = nil
	m.identity = nil
	m.mu.Unlock()
	m.logger.Info("provider sign-out complete", "provider", m.profile.ProviderID)
	return nil
}

// AccountStatus reports whether credentials exist for the provider.
type AccountStatus struct {
	SignedIn bool
	Identity *Identity
	// ExpiresAt is the access-token expiry of the stored credentials.
	ExpiresAt time.Time
}

// Status snapshots the current credentials without refreshing them.
func (m *TokenManager) Status() AccountStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := AccountStatus{SignedIn: m.tokens != nil}
	if m.tokens != nil {
		st.ExpiresAt = m.tokens.Expiry
		st.Identity = m.identity
	}
	return st
}

// Refresh forces a token refresh regardless of the current expiry. Use it
// when a provider rejected a still-unexpired token.
func (m *TokenManager) Refresh(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.refreshLocked(ctx)
}

// ensureFreshLocked refreshes when the token is missing, expired, or inside
// the expiry skew. Caller holds m.mu.
func (m *TokenManager) ensureFreshLocked(ctx context.Context) error {
	if m.tokens == nil {
		return ErrNotSignedIn
	}
	if m.validLocked() {
		return nil
	}
	return m.refreshLocked(ctx)
}

// validLocked reports whether the current access token is usable now.
func (m *TokenManager) validLocked() bool {
	if m.tokens == nil || m.tokens.AccessToken == "" {
		return false
	}
	if m.tokens.Expiry.IsZero() {
		return true
	}
	return m.now().Add(m.skew).Before(m.tokens.Expiry)
}

// refreshLocked performs the single-flight refresh: the caller holds m.mu
// for the whole network round-trip, so concurrent Token callers queue here
// and observe the refreshed token instead of issuing parallel refreshes.
func (m *TokenManager) refreshLocked(ctx context.Context) error {
	if m.tokens == nil {
		return ErrNotSignedIn
	}
	if m.tokens.RefreshToken == "" {
		return fmt.Errorf("%w: no refresh token stored", ErrReauthRequired)
	}

	resp, err := refreshTokens(ctx, m.client, m.profile, m.tokens.RefreshToken)
	if err != nil {
		var tErr *TokenEndpointError
		if errors.As(err, &tErr) && (tErr.Code == "invalid_grant" || tErr.Code == "invalid_client") {
			return fmt.Errorf("%w: %w", ErrReauthRequired, tErr)
		}
		return err
	}

	updated := resp.tokens(m.now())
	if updated.RefreshToken == "" {
		updated.RefreshToken = m.tokens.RefreshToken
	}
	if updated.IDToken != "" {
		if identity, perr := ParseIDTokenClaims(updated.IDToken); perr == nil {
			m.identity = identity
		}
	}
	m.tokens = updated

	// A failed persist degrades durability, not availability: the refreshed
	// token still serves this session, and the warning names the store issue.
	if err := m.persistLocked(); err != nil {
		m.logger.Warn("provider token refresh persist failed",
			"provider", m.profile.ProviderID, "error", err)
	} else {
		m.logger.Debug("provider token refreshed",
			"provider", m.profile.ProviderID,
			"expires_at", updated.Expiry.Format(time.RFC3339))
	}
	return nil
}

// llmTokenLocked maps the internal token set to the sp4rk wire type. Caller
// holds m.mu.
func (m *TokenManager) bearerTokenLocked() llm.BearerToken {
	bt := llm.BearerToken{
		AccessToken: m.tokens.AccessToken,
		TokenType:   m.tokens.TokenType,
		ExpiresAt:   m.tokens.Expiry,
	}
	if m.identity != nil && m.identity.ChatGPTAccountID != "" {
		bt.ExtraHeaders = map[string]string{"ChatGPT-Account-Id": m.identity.ChatGPTAccountID}
	}
	if m.profile.Originator != "" {
		if bt.ExtraHeaders == nil {
			bt.ExtraHeaders = make(map[string]string, 1)
		}
		bt.ExtraHeaders["originator"] = m.profile.Originator
	}
	return bt
}

// persistLocked writes the current credentials to the SecretStore. Caller
// holds m.mu. Only the minimal restorable secret is written (see
// persistedAuth): the refresh token and the parsed identity. The live
// access and ID tokens stay in memory for this session alone.
func (m *TokenManager) persistLocked() error {
	auth := persistedAuth{
		Version:      persistedAuthVersion,
		Provider:     m.profile.ProviderID,
		RefreshToken: m.tokens.RefreshToken,
		Identity:     m.identity,
	}
	data, err := json.Marshal(auth)
	if err != nil {
		return fmt.Errorf("providerauth: encoding %s credentials: %w", m.profile.ProviderID, err)
	}
	return m.store.Set(m.profile.secretKey(), string(data))
}
