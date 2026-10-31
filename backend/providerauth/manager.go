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

// persistedAuth is the JSON shape stored in the SecretStore; it holds
// secrets and is never logged. DELIBERATELY minimal — refresh token + parsed
// identity only, never the multi-KB access/ID JWTs: keychain bridges cap
// secret sizes far below a full token set, and one refresh round-trip
// reconstructs the live set on first use. Minimal-secret contract:
// specs/domains/llm-providers.md (ChatGPT Subscription Auth).
type persistedAuth struct {
	Version      int    `json:"version"`
	Provider     string `json:"provider"`
	RefreshToken string `json:"refresh_token,omitempty"`
	// Tokens is the legacy v1 full-token-set shape, read for one-way
	// migration only (through its refresh token; the next persist rewrites
	// v2). Never written.
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

// TokenManager owns the OAuth token lifecycle of one subscription provider
// (sign-in, skewed proactive refresh, SecretStore persistence, sign-out) and
// implements llm.TokenSource. Token/SignIn/SignOut/Refresh are
// concurrency-safe: refresh is single-flight and ctx-aware, its commit
// generation-guarded so a raced sign-out or newer sign-in is never
// overwritten. See specs/domains/llm-providers.md (ChatGPT Subscription
// Auth).
type TokenManager struct {
	profile Profile
	store   SecretStore
	client  *http.Client
	logger  *slog.Logger
	now     func() time.Time
	skew    time.Duration

	mu sync.Mutex
	// tokens/identity are the live credential state; guarded by mu.
	tokens   *Tokens
	identity *Identity
	// loadWarning carries a NON-FATAL restore problem (unreadable or
	// incomplete stored record): the manager starts signed out but stays
	// fully usable — recoverable from inside the app. Empty when the
	// restore was clean.
	loadWarning string
	// generation is bumped on every credential lifecycle transition
	// (sign-in, sign-out, sign-in rollback). An in-flight refresh snapshots
	// it and discards its result if it moved — a superseded epoch's tokens
	// neither serve requests nor reach the store.
	generation uint64
	// refreshDone is non-nil while a refresh is in flight and closed when it
	// concludes; waiters select on it (or ctx.Done()) instead of blocking on
	// mu. refreshDoneSeq identifies WHICH refresh the channel belongs to;
	// refreshErr/refreshErrSeq share that refresh's outcome with waiters
	// (single-flight covers the failure path too).
	refreshDone    chan struct{}
	refreshDoneSeq uint64
	refreshErr     error
	refreshErrSeq  uint64
	refreshSeq     uint64
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

// load restores persisted credentials. The secret is refresh-token-only
// (persistedAuth), so a restored manager refreshes on its first Token call.
// A FAILING store is a construction error (every later operation would fail
// the same way); an UNUSABLE record only starts the manager signed out with
// loadWarning set — recoverable in-app.
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
		m.loadWarning = fmt.Sprintf("providerauth: stored %s credentials are unreadable (sign in again to replace them): %v",
			m.profile.ProviderID, err)
		return nil
	}
	refresh := auth.RefreshToken
	if refresh == "" && auth.Tokens != nil {
		// Legacy v1 full-set secret (written before the keychain size fix):
		// migrate through its refresh token; the next persist rewrites it
		// as the minimal v2 shape.
		refresh = auth.Tokens.RefreshToken
	}
	if refresh == "" {
		m.loadWarning = fmt.Sprintf("providerauth: stored %s credentials carry no refresh token (sign in again to replace them)",
			m.profile.ProviderID)
		return nil
	}
	m.tokens = &Tokens{RefreshToken: refresh}
	m.identity = auth.Identity
	return nil
}

// LoadWarning reports the non-fatal restore problem found by the
// constructor (unreadable/incomplete record — the keychain itself works);
// the text names the remediation.
func (m *TokenManager) LoadWarning() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadWarning
}

// SetHTTPClient re-points the token-endpoint client (e.g. a proxy change)
// without reconstructing the manager; nil is ignored.
func (m *TokenManager) SetHTTPClient(client *http.Client) {
	if client == nil {
		return
	}
	m.mu.Lock()
	m.client = client
	m.mu.Unlock()
}

// maxTokenRefreshRounds bounds the refresh round-trips one Token/Refresh
// call may start. One is the normal contract; the headroom covers
// superseded-epoch re-entries, and the bound stops a renewal that keeps
// landing back inside the skew (short token TTL, host/issuer clock skew)
// from spinning on the token endpoint.
const maxTokenRefreshRounds = 3

// Token returns a currently valid access token, refreshing within the
// expiry skew (llm.TokenSource; single-flight and ctx-aware — see the
// TokenManager doc and specs/domains/llm-providers.md). The BearerToken
// carries the ChatGPT-Account-Id and originator headers; renewals are
// bounded per call (maxTokenRefreshRounds).
func (m *TokenManager) Token(ctx context.Context) (llm.BearerToken, error) {
	refreshRounds := 0
	for {
		m.mu.Lock()
		if m.tokens == nil {
			m.mu.Unlock()
			return llm.BearerToken{}, ErrNotSignedIn
		}
		if m.validLocked() {
			bt := m.bearerTokenLocked()
			m.mu.Unlock()
			// An already-expired ctx is honored even on a served-from-memory
			// token: a caller must not ride a refresh it outwaited.
			if err := ctx.Err(); err != nil {
				return llm.BearerToken{}, err
			}
			return bt, nil
		}
		if done := m.refreshDone; done != nil {
			waitedSeq := m.refreshDoneSeq
			m.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return llm.BearerToken{}, ctx.Err()
			}
			// Share THAT refresh's outcome: a failed refresh answers every
			// waiter with the same error (single-flight covers failures too).
			m.mu.Lock()
			if m.tokens == nil {
				m.mu.Unlock()
				return llm.BearerToken{}, ErrNotSignedIn
			}
			if m.validLocked() {
				bt := m.bearerTokenLocked()
				m.mu.Unlock()
				if err := ctx.Err(); err != nil {
					return llm.BearerToken{}, err
				}
				return bt, nil
			}
			if m.refreshDone == nil && m.refreshErr != nil && m.refreshErrSeq == waitedSeq {
				err := m.refreshErr
				m.mu.Unlock()
				return llm.BearerToken{}, err
			}
			m.mu.Unlock()
			continue
		}
		// We are the refresher for this epoch.
		refreshRounds++
		if refreshRounds > maxTokenRefreshRounds {
			m.mu.Unlock()
			return llm.BearerToken{}, fmt.Errorf("providerauth: %d token refreshes still left the access token inside the renewal skew (an unusually short token TTL or a clock skew between this host and the issuer); sign in again or retry later", maxTokenRefreshRounds)
		}
		m.refreshSeq++
		seq := m.refreshSeq
		m.refreshDone = make(chan struct{})
		m.refreshDoneSeq = seq
		m.refreshErr = nil
		// Snapshot epoch + refresh token + client in ONE critical section:
		// reading the token after the unlock would spend a newer sign-in's
		// (rotating) refresh token on a round-trip whose commit is discarded
		// anyway — invalidating that token server-side for nothing.
		gen := m.generation
		refreshToken := m.tokens.RefreshToken
		client := m.client
		m.mu.Unlock()

		err := m.refreshEpoch(ctx, gen, refreshToken, client)

		m.mu.Lock()
		m.refreshErr = err
		m.refreshErrSeq = seq
		done := m.refreshDone
		m.refreshDone = nil
		m.mu.Unlock()
		if done != nil {
			close(done)
		}
		if err != nil {
			return llm.BearerToken{}, err
		}
		// Loop: re-validate — the refreshed state may have been superseded.
	}
}

// SignIn runs the browser OAuth flow and persists the minimal secret
// (persistedAuth). The flow runs WITHOUT the manager lock — Token callers
// keep getting answers during the interactive sign-in — and the store write
// precedes the in-memory update, so a failing keychain aborts the sign-in
// rather than leaving unpersisted credentials. Cancellation is honored
// around the commit: refused before the write, rolled back after it, so an
// accepted Cancel can never conclude as a durably signed-in account.
func (m *TokenManager) SignIn(ctx context.Context, opener BrowserOpener) (*SignInResult, error) {
	m.mu.Lock()
	client := m.client
	m.mu.Unlock()
	flow := &Flow{profile: m.profile, client: client, opener: opener, now: m.now}
	if flow.opener == nil {
		flow.opener = DefaultBrowserOpener
	}
	result, err := flow.SignIn(ctx)
	if err != nil {
		return nil, err
	}
	if result.Tokens.RefreshToken == "" {
		// A refresh-token-less sign-in cannot be restored after a restart.
		return nil, errors.New("providerauth: the issuer issued no refresh token; this sign-in cannot be persisted, retry it")
	}
	if err := ctx.Err(); err != nil {
		// Cancelled before the commit point: nothing was written, nothing to
		// roll back.
		return nil, err
	}

	m.mu.Lock()
	prevTokens, prevIdentity := m.tokens, m.identity
	prevWarning := m.loadWarning
	m.tokens = result.Tokens
	m.identity = result.Identity
	m.generation++
	m.loadWarning = "" // the new credentials replace whatever the warning was about
	if err := m.persistLocked(); err != nil {
		// Unpersistable tokens are not a signed-in account.
		m.tokens = prevTokens
		m.identity = prevIdentity
		m.generation++
		// The failed write leaves the PREVIOUS keychain record in place, so
		// a load warning describing it must survive instead of being
		// silently cleared.
		m.loadWarning = prevWarning
		m.mu.Unlock()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		// The cancellation was accepted while the store write was blocked
		// (e.g. a keychain unlock prompt); the write may have landed, so the
		// commit is rolled back (previous record restored, else deleted).
		// The rollback is best-effort; its failure is logged, never returned
		// in place of the cancellation. Unlike the persist-failure branch,
		// the load warning is NOT restored: persistLocked succeeded, so the
		// record it described is gone.
		m.tokens = prevTokens
		m.identity = prevIdentity
		m.generation++
		rollbackErr := m.rollbackStoreLocked()
		m.mu.Unlock()
		if rollbackErr != nil {
			m.logger.Warn("provider sign-in cancellation rollback failed",
				"provider", m.profile.ProviderID, "error", rollbackErr)
		}
		return nil, err
	}
	m.mu.Unlock()
	m.logger.Info("provider sign-in complete",
		"provider", m.profile.ProviderID,
		"account_id", identityField(result.Identity, func(i *Identity) string { return i.ChatGPTAccountID }),
		"email", identityField(result.Identity, func(i *Identity) string { return i.Email }),
		"expires_at", result.Tokens.Expiry.Format(time.RFC3339))
	return result, nil
}

// rollbackStoreLocked re-persists (or deletes) the record to match the
// already-restored in-memory credentials. Caller holds m.mu.
func (m *TokenManager) rollbackStoreLocked() error {
	if m.tokens == nil {
		err := m.store.Delete(m.profile.secretKey())
		if errors.Is(err, ErrSecretNotFound) {
			return nil
		}
		return err
	}
	return m.persistLocked()
}

// identityField extracts a display field from a possibly-nil Identity.
func identityField(id *Identity, get func(*Identity) string) string {
	if id == nil {
		return ""
	}
	return get(id)
}

// SignOut clears the persisted credentials and the in-memory state;
// idempotent. The delete runs UNDER the state lock so a refresh racing the
// round-trip discards its stale result at commit time instead of
// resurrecting the cleared credentials.
func (m *TokenManager) SignOut() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	err := m.store.Delete(m.profile.secretKey())
	if err != nil && !errors.Is(err, ErrSecretNotFound) {
		// The delete failed: the record — and the load warning describing
		// it — must survive; the manager stays signed in.
		return err
	}
	// The record is gone (or never existed): its warning is obsolete.
	m.loadWarning = ""
	m.tokens = nil
	m.identity = nil
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

// Refresh forces a token refresh regardless of expiry; use it when a
// provider rejected a still-unexpired token. Failure is shared across
// waiters; success is NOT — every call means "rotate now". A superseded
// epoch (raced sign-in/sign-out) is not success: the loop re-evaluates
// (signed out → ErrNotSignedIn; a newer epoch rotates in a follow-up
// round), bounded by maxTokenRefreshRounds so endless supersession becomes
// an error, not a spin.
func (m *TokenManager) Refresh(ctx context.Context) error {
	refreshRounds := 0
	for {
		m.mu.Lock()
		if m.tokens == nil {
			m.mu.Unlock()
			return ErrNotSignedIn
		}
		if done := m.refreshDone; done != nil {
			waitedSeq := m.refreshDoneSeq
			m.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
			m.mu.Lock()
			if m.refreshDone == nil && m.refreshErr != nil && m.refreshErrSeq == waitedSeq {
				err := m.refreshErr
				m.mu.Unlock()
				return err
			}
			m.mu.Unlock()
			continue
		}
		refreshRounds++
		if refreshRounds > maxTokenRefreshRounds {
			m.mu.Unlock()
			return fmt.Errorf("providerauth: %d token refreshes were all superseded by concurrent lifecycle transitions (sign-in/sign-out); sign in again or retry later", maxTokenRefreshRounds)
		}
		m.refreshSeq++
		seq := m.refreshSeq
		m.refreshDone = make(chan struct{})
		m.refreshDoneSeq = seq
		m.refreshErr = nil
		// Same one-section snapshot as Token: this epoch's token only.
		gen := m.generation
		refreshToken := m.tokens.RefreshToken
		client := m.client
		m.mu.Unlock()

		err := m.refreshEpoch(ctx, gen, refreshToken, client)

		m.mu.Lock()
		m.refreshErr = err
		m.refreshErrSeq = seq
		done := m.refreshDone
		m.refreshDone = nil
		// Only a DISCARDED refresh re-evaluates; a committed one leaves
		// tokens non-nil and refreshDone unset, so re-entering the loop
		// would spin forever.
		superseded := err == nil && m.generation != gen
		m.mu.Unlock()
		if done != nil {
			close(done)
		}
		if err != nil {
			return err
		}
		if !superseded {
			return nil
		}
	}
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

// refreshEpoch performs one refresh round-trip for credential epoch gen
// (token and client snapshotted together with it — this epoch's token
// only). The network call runs WITHOUT the state lock; the commit
// re-takes it and is discarded (returning nil) when the generation moved —
// the caller then re-evaluates the current state.
func (m *TokenManager) refreshEpoch(ctx context.Context, gen uint64, refreshToken string, client *http.Client) error {
	if refreshToken == "" {
		return fmt.Errorf("%w: no refresh token stored", ErrReauthRequired)
	}

	resp, err := refreshTokens(ctx, client, m.profile, refreshToken)
	if err != nil {
		var tErr *TokenEndpointError
		if errors.As(err, &tErr) && (tErr.Code == "invalid_grant" || tErr.Code == "invalid_client") {
			return fmt.Errorf("%w: %w", ErrReauthRequired, tErr)
		}
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generation != gen || m.tokens == nil {
		// Stale epoch: discard — persisting would resurrect a signed-out
		// account or overwrite a newer sign-in.
		return nil
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

// persistLocked writes the minimal restorable secret (persistedAuth) to the
// SecretStore. Caller holds m.mu; the live access/ID tokens stay in memory.
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
