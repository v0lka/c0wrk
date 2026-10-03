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
// use. A refresh is single-flight — concurrent callers wait on one network
// round-trip and share its result, so at most one refresh is in flight at any
// time — and the wait is context-aware: a waiter whose ctx is cancelled stops
// waiting immediately instead of queueing on a lock held across the network
// round-trip (the refresh itself runs WITHOUT the state lock; its commit is
// guarded by a generation counter so a sign-out that raced the network call
// can never be overwritten by the stale refresh result).
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
	// loadWarning carries a NON-FATAL restore problem: the stored record is
	// unreadable (malformed JSON) or incomplete (no refresh token). The
	// manager starts signed out but stays fully usable — the next sign-in
	// REPLACES the record and a sign-out DELETES it — so a corrupt keychain
	// entry is recoverable from inside the app instead of bricking both
	// auth RPCs. Empty when the restore was clean.
	loadWarning string
	// generation is bumped on every credential lifecycle transition
	// (successful sign-in, sign-out, sign-in rollback). An in-flight
	// refresh snapshots the generation when it starts and discards its
	// result if the generation moved while its network call ran — the
	// refreshed tokens of a superseded epoch must neither serve requests
	// nor be persisted over the newer state.
	generation uint64
	// refreshDone is non-nil while a refresh is in flight; it is closed when
	// that refresh fully concludes. Waiters select on it (or their own
	// ctx.Done()) instead of blocking on mu. refreshDoneSeq identifies WHICH
	// refresh the channel belongs to; refreshErr/refreshErrSeq carry that
	// refresh's outcome so waiters woken by it share the SAME error instead
	// of each starting a redundant retry of their own (single-flight covers
	// the failure path too).
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

// load restores persisted credentials, if any. The persisted secret is
// refresh-token-only (see persistedAuth), so a restored manager starts with
// no access token and refreshes on its first Token call.
//
// Two failure classes are distinguished:
//   - a FAILING STORE (the OS keychain service is unreachable) is returned
//     as an error: the manager cannot be constructed because every later
//     operation (sign-in, sign-out, refresh persist) would fail the same
//     way, and the caller surfaces the actionable construction error.
//   - an UNUSABLE RECORD (malformed JSON, or a record with no refresh
//     token) is NOT fatal: the manager starts signed out with the problem
//     recorded in loadWarning (see LoadWarning) and remains fully usable —
//     the next sign-in overwrites the record and a sign-out deletes it —
//     so a corrupt entry is recoverable from inside the app.
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
// constructor, if any: the stored record was unreadable or incomplete, so
// the manager started signed out although the keychain itself works. The
// warning names the remediation (sign in again to replace the record).
func (m *TokenManager) LoadWarning() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadWarning
}

// SetHTTPClient replaces the client used for token-endpoint calls. It lets
// the host re-point the manager at a new outbound configuration (e.g. a
// proxy settings change) without reconstructing the manager and losing the
// live credential state; a nil client is ignored (keep the current one).
// Safe for concurrent use with Token/SignIn/SignOut/Refresh.
func (m *TokenManager) SetHTTPClient(client *http.Client) {
	if client == nil {
		return
	}
	m.mu.Lock()
	m.client = client
	m.mu.Unlock()
}

// maxTokenRefreshRounds bounds the refresh round-trips a single Token call
// may start before giving up with an actionable error. One round is the
// normal contract ("refreshing it first when it is within the expiry
// skew"); the headroom covers re-entries after a superseded epoch (a
// concurrent sign-in/sign-out discarded the round's commit) while still
// stopping the pathological loop where every renewal lands back inside the
// skew — a short-lived-token issuer or a host/issuer clock skew would
// otherwise spin on the token endpoint until the caller's deadline.
const maxTokenRefreshRounds = 3

// Token returns a currently valid access token, refreshing it first when it
// is within the expiry skew. It implements sp4rk's llm.TokenSource: the ctx
// is honored during a renewal round-trip AND while waiting for another
// caller's in-flight renewal to conclude (a waiter never blocks on a lock
// held across the network), and the returned BearerToken carries the
// ChatGPT-Account-Id and originator headers the provider stamps onto
// outgoing requests. Renewals are bounded per call (maxTokenRefreshRounds):
// a refresh whose result still lands inside the expiry skew fails with an
// actionable error instead of spinning.
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
			// Even a served-from-memory token honors an already-expired
			// context: a caller whose deadline passed while waiting behind
			// another caller's refresh must not ride that refresh's result.
			if err := ctx.Err(); err != nil {
				return llm.BearerToken{}, err
			}
			return bt, nil
		}
		if done := m.refreshDone; done != nil {
			// Another caller's refresh is in flight: wait for its completion
			// or our own cancellation, then re-evaluate the (possibly
			// refreshed) state under the lock.
			waitedSeq := m.refreshDoneSeq
			m.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return llm.BearerToken{}, ctx.Err()
			}
			// Share THAT refresh's outcome: a failed refresh answers every
			// waiter with the same error (single-flight covers failures too)
			// instead of letting each waiter start its own retry.
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
		// Snapshot the epoch AND its refresh token + client in ONE critical
		// section: reading the token inside refreshEpoch (after the unlock)
		// would let a SignIn committing in the gap hand this round-trip the
		// NEWER account's refresh token while the commit check still discards
		// the result — with a rotating-refresh issuer that invalidates the
		// newer account's token server-side for nothing.
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
		// Loop: re-validate the refreshed state under the lock (it may have
		// been superseded by a concurrent lifecycle transition).
	}
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
//
// Cancellation is honored around the commit: if ctx was cancelled before the
// keychain write started, the sign-in refuses without touching the store; if
// it was cancelled while the (potentially prompt-blocked) write ran, the
// newly written credentials are rolled back — the previous account is
// restored when one existed, the record is deleted otherwise — and the
// cancellation error is returned, so an accepted Cancel can never conclude
// as a successful, durably signed-in account.
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
		// The persisted secret is refresh-token-only, so a sign-in the
		// issuer issued no refresh token for cannot be restored after a
		// restart — it must not be reported as a signed-in account.
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
		// Fail the sign-in: tokens that cannot be persisted must not be
		// treated as a signed-in account.
		m.tokens = prevTokens
		m.identity = prevIdentity
		m.generation++
		// The failed store write leaves the PREVIOUS keychain record in
		// place — when that record is what the load warning described
		// (unreadable or incomplete), the warning still names the
		// remediation and must survive the failed sign-in instead of
		// silently clearing the status.
		m.loadWarning = prevWarning
		m.mu.Unlock()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		// The cancellation was accepted while the store write was blocked
		// (e.g. a keychain unlock prompt). The write may have landed, so the
		// commit must be rolled back consistently: restore the previous
		// account's record when one existed, delete the record otherwise.
		// The rollback is best-effort — a keychain that just failed to
		// answer in time may fail again — and any rollback failure is
		// logged (never returned in place of the cancellation, which is the
		// operator's requested outcome).
		//
		// Unlike the persist-failure branch above, the load warning is NOT
		// restored here: persistLocked already SUCCEEDED, so the previous
		// keychain record is gone (overwritten by the new credentials, then
		// deleted or re-persisted by the rollback) — a warning describing
		// that dead record would be wrong. The warning was also necessarily
		// empty whenever a previous account existed (a signed-in manager
		// carries no load warning).
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

// rollbackStoreLocked restores the persisted record to the in-memory
// credentials AFTER a rolled-back sign-in: the previous account is written
// back when one existed, and the record is deleted when the manager was
// signed out before the sign-in. Caller holds m.mu (the in-memory state has
// already been restored by the caller).
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

// SignOut clears the persisted credentials and the in-memory state. It is
// idempotent: signing out while already signed out succeeds.
//
// The store record is deleted UNDER the state lock, and the lock is held
// across the delete and the memory clear: a refresh that already started its
// network round-trip snapshots the generation before the delete and discards
// its (stale) result at commit time instead of re-persisting the cleared
// credentials — so a sign-out that reports success can never leave a
// durable credential behind that a racing refresh resurrects.
func (m *TokenManager) SignOut() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	err := m.store.Delete(m.profile.secretKey())
	if err != nil && !errors.Is(err, ErrSecretNotFound) {
		// The delete failed: the stored record is still there, so the load
		// warning describing it must survive too — the memory clear below
		// does NOT run on this path (a failed sign-out keeps the manager
		// signed in), and the warning keeps naming the remediation until
		// the next successful delete or a replacement sign-in.
		return err
	}
	// The record is gone (deleted, or never existed): the warning about it
	// is obsolete. Cleared only after a successful delete, mirroring the
	// tokens/identity clear.
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

// Refresh forces a token refresh regardless of the current expiry. Use it
// when a provider rejected a still-unexpired token. Concurrent callers wait
// for one in-flight refresh and share its FAILURE; a waiter whose waited
// refresh SUCCEEDED performs its own round-trip (the success is not shared —
// each Refresh call means "rotate now"). A caller whose ctx is cancelled
// stops waiting for the in-flight refresh. A refresh whose epoch was
// discarded mid-round-trip (superseded by a sign-in or sign-out) is not
// reported as success: the loop re-evaluates the current state — a
// signed-out manager answers ErrNotSignedIn, and a newer epoch's
// credentials are rotated in a follow-up round (each Refresh call means
// rotate-now, so a superseded round does not leave the newer epoch
// untouched; the re-evaluation is bounded by maxTokenRefreshRounds, turning
// endless supersession into an actionable error instead of a spin).
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
			// Share that refresh's outcome (see Token): a failed refresh
			// answers every waiter with the same error.
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
		// Same one-section snapshot as Token's refresher: gen, token and
		// client are captured together so the round-trip spends this
		// epoch's token only.
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
		// Distinguish a COMMITTED refresh (the generation still matches this
		// epoch — the rotation landed and nothing else happened since) from
		// a DISCARDED one (superseded mid-round-trip): only the discarded
		// case re-evaluates. Without this check the loop would re-enter the
		// refresher branch after every successful commit and spin forever
		// (a committed refresh leaves tokens non-nil and refreshDone unset).
		superseded := err == nil && m.generation != gen
		m.mu.Unlock()
		if done != nil {
			close(done)
		}
		if err != nil {
			return err
		}
		if !superseded {
			// The refresh committed for this epoch: done.
			return nil
		}
		// Superseded: loop and re-evaluate the current state — a signed-out
		// manager answers ErrNotSignedIn, a newer epoch's credentials serve
		// as-is (and the loop bound above turns endless supersession into
		// an actionable error).
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

// refreshEpoch performs one refresh round-trip for the credential epoch gen,
// using the refresh token and client snapshotted TOGETHER with gen by the
// caller (one lock hold — see Token/Refresh): the round-trip can never spend
// a newer epoch's token. The network call runs WITHOUT the state lock (so
// Token waiters observe the in-flight channel instead of queueing on the
// mutex); the commit re-takes the lock and is discarded when the generation
// moved while the call ran — a sign-out or a new sign-in superseded this
// epoch, and its tokens must neither serve requests nor reach the store. A
// discarded epoch returns nil: the caller re-evaluates the current state
// (signed out → ErrNotSignedIn, newer credentials → served as-is).
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
		// Stale epoch: the credentials were cleared or replaced while the
		// network call ran. Discard the refreshed tokens entirely —
		// persisting them would resurrect a signed-out account or overwrite
		// a newer sign-in.
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
