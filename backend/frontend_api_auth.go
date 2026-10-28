package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/v0lka/sp4rk/llm"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/providerauth"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/proxy"
)

// The RPC surface of the ChatGPT subscription sign-in — the contract (read
// without error, actionable refusals, the background/cancellable flow, the
// no-network-I/O startup restore, secrets never crossing the boundary) is
// specified in specs/contracts/desktop-frontend.md and
// specs/domains/llm-providers.md (ChatGPT Subscription Auth); transitions
// ride the global chatgpt_auth:state event (specs/contracts/event-catalog.md).
//
// Ownership: this file owns the FrontendAPI-side wiring of backend/providerauth
// — the token manager's lifecycle, the sign-in busy gate, the state event. It
// owns NO OAuth policy: endpoints, PKCE, the loopback listener and token
// refresh live in backend/providerauth.

// chatGPTSignInJoinTimeout bounds how long SignOutChatGPT waits for a
// cancelled in-flight sign-in to conclude before proceeding anyway. It bounds
// the JOIN only — never the whole RPC, and never the sign-out's own
// (unbounded) keychain delete. On expiry the sign-out proceeds — last writer
// wins; a flow persisting after it re-signs the account, visible in the next
// status read.
const chatGPTSignInJoinTimeout = 5 * time.Second

// chatGPTSignInStartTimeout bounds how long StartChatGPTSignIn waits for the
// background flow to publish the authorization URL. Binding the listener and
// deriving the PKCE pair is millisecond-scale work; the bound exists only so
// a wedged flow returns an actionable error instead of hanging the RPC.
const chatGPTSignInStartTimeout = 10 * time.Second

// chatGPTProviderName is the router entry the subscription-auth seam serves —
// the name ToBuilderConfig builds the chatgpt entry under; the seam's
// serves() match fails closed on any other value.
const chatGPTProviderName = "chatgpt"

// chatgptAuthState is the ChatGPT subscription-auth subsystem state, a value
// field on FrontendAPI: the zero value (no manager) is usable before
// InitChatGPTAuth runs — RPCs report the signed-out posture with the
// construction error as last_error. mu guards the bookkeeping and is never
// held across a manager call (manager methods synchronize internally); the
// one exception is the restore-correction emit, which must not interleave
// with the inFlight check. Mirrors the embedded subsystem's rule.
type chatgptAuthState struct {
	mu sync.Mutex
	// pushMu serializes ONLY the manager SetHTTPClient installs (see
	// pushChatGPTManagerProxyClientLocked), which run OUTSIDE mu — a manager
	// call takes TokenManager.mu, possibly held by a prompt-blocked keychain
	// write. pushSeq/pushInstalledSeq keep install order across that window:
	// an older preempted push cannot revert a newer client.
	pushMu           sync.Mutex
	pushSeq          uint64
	pushInstalledSeq uint64
	// manager owns the OAuth lifecycle once constructed; nil when construction
	// failed (initError names why — e.g. no reachable Secret Service).
	manager chatGPTAuthManager
	// initError is InitChatGPTAuth's actionable construction failure; empty
	// when the manager exists.
	initError string
	// restoring marks the TRANSIENT startup window where the background
	// keychain restore has not concluded: manager nil, initError empty, but
	// the subsystem IS coming up — refused RPCs say "try again in a moment".
	restoring bool
	// inFlight marks a background sign-in run; exactly one may exist at a
	// time. cancel is its CancelFunc; runDone is closed by the run's
	// goroutine once fully concluded — both published while inFlight.
	inFlight bool
	cancel   context.CancelFunc
	runDone  chan struct{}
	// cancelRequested records that the OPERATOR asked to cancel: the failure
	// branch tells a requested cancellation (flag AND errors.Is(err,
	// context.Canceled)) from a genuine fault or a shutdown-driven one (the
	// flag alone silences nothing).
	cancelRequested bool
	// lastError carries the operator-friendly cause of the last FAILED run:
	// surfaced as status last_error, cleared when the next run starts or a
	// sign-in succeeds.
	lastError string
	// modelsBaseURLOverride points the FetchChatGPTModels live catalog fetch
	// at an httptest server. Test-only seam (empty in production).
	modelsBaseURLOverride string

	// Test seams, mirroring embeddedLLMState's (all zero in production):
	// signInStartTimeoutOverride replaces chatGPTSignInStartTimeout;
	// newManagerFn replaces the manager construction so tests inject a mock
	// without touching the OS keychain.
	signInStartTimeoutOverride time.Duration
	newManagerFn               func() (chatGPTAuthManager, error)
}

// chatGPTAuthManager is the subset of *providerauth.TokenManager this surface
// depends on; production injects the real manager (compile-time assertion
// below), tests a mock — no browser, keychain or network.
type chatGPTAuthManager interface {
	// SignIn runs the browser OAuth flow; the opener publishes the
	// authorization URL instead of opening a browser itself.
	SignIn(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error)
	// SignOut clears the persisted credentials; idempotent.
	SignOut() error
	// Status snapshots the credentials without refreshing them.
	Status() providerauth.AccountStatus
	// Token resolves a currently valid bearer token (llm.TokenSource —
	// what the builder seam hands to the router entry).
	Token(ctx context.Context) (llm.BearerToken, error)
}

// Compile-time assertion that the production manager satisfies the seam.
var _ chatGPTAuthManager = (*providerauth.TokenManager)(nil)

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// InitChatGPTAuth constructs the ChatGPT token manager and restores whatever
// credentials the OS keychain already holds — startup path only
// (desktop/startup_phases.go), no network I/O, no browser flow. A failed
// construction records the actionable error and leaves the signed-out
// posture instead of failing startup. The live token source is mirrored into
// the builder seam for EVERY restored state, mode-independent (inert in
// api_key mode), so a later switch to oauth picks it up without a restart —
// request correctness rides the dynamic request-time stand-in either way;
// the immediate rebuild (oauth mode + signed in) only eagerly swaps to the
// direct seam source and refreshes judge/model metadata, and is skipped when
// nothing was restored. See specs/domains/llm-providers.md. A signed-in
// restore also emits the chatgpt_auth:state success transition carrying the
// restored identity — the restore runs on a background goroutine, so a panel
// that rendered against the pre-restore zero state needs the event to
// correct itself; skipped while an interactive sign-in is in flight (see the
// emit site below and specs/contracts/event-catalog.md).
func (l *FrontendAPILifecycle) InitChatGPTAuth() {
	f := l.f
	// The restore may run on a background goroutine (the OS keychain read is
	// not provably bounded — a locked login collection prompts first). Do not
	// publish a restore into an app that is already shutting down: the seam
	// mirror and the rebuild would race the teardown.
	if err := f.ctx().Err(); err != nil {
		return
	}
	newManager := f.chatgptAuth.newManagerFn
	if newManager == nil {
		newManager = func() (chatGPTAuthManager, error) {
			return providerauth.NewTokenManager(providerauth.ManagerConfig{
				Profile: providerauth.ChatGPT(),
				Store:   providerauth.NewKeyringSecretStore(),
				// Generate the manager's HTTP client under the CURRENT effective
				// proxy configuration — a restored account must be able to
				// refresh behind the operator's proxy (chatGPTOutboundHTTPClient).
				HTTPClient: f.chatGPTOutboundHTTPClient(30 * time.Second),
				Logger:     f.log(),
			})
		}
	}
	// The keyring backend is native code; an unwrapped panic here would skip
	// the restoring=false bookkeeping below and wedge every mutating auth RPC
	// on the transient refusal forever. Convert it into an error for the
	// normal failure path (mirrors desktop's safeGo, scoped to this call).
	f.chatgptAuth.mu.Lock()
	f.chatgptAuth.restoring = true
	f.chatgptAuth.mu.Unlock()
	m, err := constructChatGPTAuthManager(newManager)
	f.chatgptAuth.mu.Lock()
	f.chatgptAuth.restoring = false
	if err != nil {
		f.chatgptAuth.initError = err.Error()
		f.chatgptAuth.mu.Unlock()
		f.log().Warn("ChatGPT subscription auth unavailable", "error", err)
		return
	}
	f.chatgptAuth.manager = m
	f.chatgptAuth.mu.Unlock()
	// A non-fatal restore problem (unreadable/incomplete record) surfaces as
	// the status's last_error: signed out but usable, with the remediation
	// named. LoadWarning is genuinely read OUTSIDE mu (the manager call takes
	// TokenManager.mu internally); the write back under it is atomic, so a
	// reader sees no warning or the complete one.
	var restoreWarning string
	if wm, ok := m.(interface{ LoadWarning() string }); ok {
		restoreWarning = wm.LoadWarning()
	}
	// Re-point the manager at the CURRENT proxy configuration: the
	// construction client was captured before the keychain read, so a proxy
	// change during that read must not be missed (the push releases mu
	// around the install).
	f.chatgptAuth.mu.Lock()
	f.pushChatGPTManagerProxyClientLocked()
	if restoreWarning != "" {
		f.chatgptAuth.lastError = restoreWarning
		f.log().Warn("ChatGPT subscription auth restored with a problem", "warning", restoreWarning)
	}
	f.chatgptAuth.mu.Unlock()

	// Re-check the app context after the (potentially slow) keychain read:
	// a shutdown that started while the record was loading discards the
	// restore instead of racing the teardown with the seam mirror + rebuild.
	if err := f.ctx().Err(); err != nil {
		return
	}

	// Mirror the live token source into the builder seam INDEPENDENTLY of the
	// configured auth mode: inert in api_key mode (only SubscriptionAuth-marked
	// entries consume it), and it makes a later oauth switch pick up the
	// restored credentials — without it, an account restored under api_key
	// leaves every router, fresh ones included, answering sign-in-required
	// while status reports signed-in. See specs/domains/llm-providers.md.
	f.syncChatGPTBuilderSeam()

	st := m.Status()
	if st.SignedIn && f.chatGPTAuthModeIsOAuth() {
		f.rebuildRouterAfterChatGPTAuthChange()
	}
	if st.SignedIn {
		// Skip the correction while an interactive sign-in is in flight: its
		// pending event established the busy posture and its own terminal
		// event re-reads the authoritative snapshot — a mid-flow
		// restore-success would clear the busy flag prematurely (see
		// specs/contracts/event-catalog.md).
		//
		// Check and emission run under ONE critical section: emitting after
		// releasing the lock would let a sign-in starting in between publish
		// its pending event first and still receive this success — the exact
		// interleaving the skip prevents. Safe because the emitter never
		// re-enters the auth subsystem.
		f.chatgptAuth.mu.Lock()
		if !f.chatgptAuth.inFlight {
			// The restore runs on a BACKGROUND goroutine: a Settings panel
			// that rendered against the pre-restore zero state refreshes its
			// snapshot only on mount, so publish the same success transition
			// an interactive sign-in emits, after any router rebuild (a chat
			// request issued after the event already carries subscription
			// credentials).
			event := ChatGPTAuthEventData{
				State:     chatgptAuthEventSuccess,
				Email:     chatGPTIdentityField(st.Identity, func(i *providerauth.Identity) string { return i.Email }),
				AccountID: chatGPTIdentityField(st.Identity, func(i *providerauth.Identity) string { return i.ChatGPTAccountID }),
			}
			if !st.ExpiresAt.IsZero() {
				event.ExpiresAt = st.ExpiresAt.Format(time.RFC3339)
			}
			f.emitChatGPTAuthState(event)
		}
		f.chatgptAuth.mu.Unlock()
	}
}

// cancelChatGPTSignInLocked tears down an in-flight sign-in's bookkeeping.
// Caller holds chatgptAuth.mu.
func (f *FrontendAPI) cancelChatGPTSignInLocked() {
	f.chatgptAuth.inFlight = false
	f.chatgptAuth.cancel = nil
	f.chatgptAuth.runDone = nil
	f.chatgptAuth.cancelRequested = false
}

// chatGPTAuthManagerLocked returns the live manager (nil when construction
// failed). Caller holds chatgptAuth.mu.
func (f *FrontendAPI) chatGPTAuthManagerLocked() chatGPTAuthManager {
	return f.chatgptAuth.manager
}

// chatGPTAuthUnavailableErr is the refusal every mutating RPC returns when
// the manager could not be constructed: the recorded construction error
// (captured by the caller under chatgptAuth.mu) names the remediation (e.g.
// unlocking the keychain service).
func (f *FrontendAPI) chatGPTAuthUnavailableErr(action, reason string) error {
	return fmt.Errorf("ChatGPT %s is unavailable on this machine: %s", action, reason)
}

// chatGPTAuthNilManagerRefusalLocked builds the refusal a mutating RPC
// returns when the live manager is nil. Caller holds chatgptAuth.mu. The
// reason names the three nil states in the operator's terms: the permanent
// construction failure (initError), the transient restore window
// ("try again in a moment"), and the genuinely-uninitialized state
// (production never sees it — desktop startup calls InitChatGPTAuth
// unconditionally).
func (f *FrontendAPI) chatGPTAuthNilManagerRefusalLocked(action string) error {
	switch {
	case f.chatgptAuth.initError != "":
		return f.chatGPTAuthUnavailableErr(action, f.chatgptAuth.initError)
	case f.chatgptAuth.restoring:
		return fmt.Errorf("ChatGPT %s is unavailable while the account restore is still reading the OS keychain — try again in a moment", action)
	default:
		return fmt.Errorf("ChatGPT %s is unavailable: the auth subsystem was not initialized", action)
	}
}

// constructChatGPTAuthManager runs the manager construction (the native
// keyring read) with panic containment: a panicking keyring backend becomes
// an error for the caller's normal failure path (initError recorded,
// restoring cleared) instead of a top-level recovered panic that would skip
// that bookkeeping.
func constructChatGPTAuthManager(newManager func() (chatGPTAuthManager, error)) (m chatGPTAuthManager, err error) {
	defer func() {
		if r := recover(); r != nil {
			m = nil
			err = fmt.Errorf("the OS keychain backend panicked during the account restore: %v", r)
		}
	}()
	return newManager()
}

// chatGPTAuthModeOrAPIKey normalizes a stored auth mode for the wire: the
// empty value (a config that bypassed ApplyDefaults) reads as the documented
// default, api_key — the same semantics the config layer's consumers apply.
func chatGPTAuthModeOrAPIKey(mode string) string {
	if mode == "" {
		return config.ChatGPTAuthModeAPIKey
	}
	return mode
}

// chatGPTAuthModeIsOAuth reports whether the live config selects oauth mode
// for the chatgpt provider. The empty mode reads as api_key (the config
// layer's documented default semantics), so an uninitialized config never
// masquerades as subscription auth.
func (f *FrontendAPI) chatGPTAuthModeIsOAuth() bool {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	return f.config != nil && f.config.LLM.ChatGPT.Auth.Mode == config.ChatGPTAuthModeOAuth
}

// ---------------------------------------------------------------------------
// RPC surface
// ---------------------------------------------------------------------------

// StartChatGPTSignIn starts the browser OAuth flow and returns the
// authorization URL for the FRONTEND to open (runtime.BrowserOpenURL — the
// backend never launches a browser); this RPC waits only, bounded by
// chatGPTSignInStartTimeout, for the flow to bind its listener and derive
// the URL. Later transitions arrive through chatgpt_auth:state. At most one
// sign-in may be in flight; a second call is refused with an actionable
// error naming the in-flight run (two interleaved flows would paint a
// pending state no listener can complete).
// See specs/contracts/desktop-frontend.md.
func (f *FrontendAPI) StartChatGPTSignIn() (*ChatGPTSignInResponse, error) {
	f.chatgptAuth.mu.Lock()
	if f.chatgptAuth.inFlight {
		f.chatgptAuth.mu.Unlock()
		return nil, errors.New("a ChatGPT sign-in is already in progress — cancel it before starting another")
	}
	m := f.chatGPTAuthManagerLocked()
	if m == nil {
		err := f.chatGPTAuthNilManagerRefusalLocked("sign-in")
		f.chatgptAuth.mu.Unlock()
		return nil, err
	}

	ctx, cancel := context.WithCancel(f.ctx())
	f.chatgptAuth.inFlight = true
	f.chatgptAuth.cancel = cancel
	runDone := make(chan struct{})
	f.chatgptAuth.runDone = runDone
	f.chatgptAuth.cancelRequested = false
	f.chatgptAuth.lastError = ""
	f.chatgptAuth.mu.Unlock()

	// urlCh carries the flow's first observable fact to the waiting RPC:
	// either the authorization URL (the opener ran) or the error that ended
	// the flow before the opener ran. Buffered so the goroutine never blocks
	// on a waiter that already timed out.
	urlCh := make(chan chatGPTSignInStart, 1)
	openerCalled := false // written and read on the flow goroutine only

	opener := func(authURL string) error {
		openerCalled = true
		f.emitChatGPTAuthState(ChatGPTAuthEventData{
			State:   chatgptAuthEventPending,
			AuthURL: authURL,
		})
		urlCh <- chatGPTSignInStart{url: authURL}
		return nil
	}

	go func() {
		defer close(runDone)
		result, err := m.SignIn(ctx, opener)
		if err != nil && !openerCalled {
			// The flow died before it could present a URL: unblock the
			// waiting RPC instead of leaving it to the start timeout.
			urlCh <- chatGPTSignInStart{err: err}
		}

		f.chatgptAuth.mu.Lock()
		requested := f.chatgptAuth.cancelRequested
		f.cancelChatGPTSignInLocked()
		f.chatgptAuth.mu.Unlock()
		cancel()

		switch {
		case err == nil:
			f.chatgptAuth.mu.Lock()
			f.chatgptAuth.lastError = ""
			f.chatgptAuth.mu.Unlock()
			f.syncChatGPTBuilderSeam()
			f.rebuildRouterAfterChatGPTAuthChange()
			// The identity is OPTIONAL in providerauth — never dereference it
			// directly in this background goroutine: a nil deref would take
			// the whole desktop process down instead of reporting the
			// outcome. The nil-safe extractors emit empty identity fields.
			f.emitChatGPTAuthState(ChatGPTAuthEventData{
				State: chatgptAuthEventSuccess,
				Email: chatGPTIdentityField(chatGPTIdentity(result),
					func(i *providerauth.Identity) string { return i.Email }),
				AccountID: chatGPTIdentityField(chatGPTIdentity(result),
					func(i *providerauth.Identity) string { return i.ChatGPTAccountID }),
				ExpiresAt: chatGPTExpiry(result),
			})
		case requested && errors.Is(err, context.Canceled):
			f.emitChatGPTAuthState(ChatGPTAuthEventData{State: chatgptAuthEventCancelled})
		default:
			f.chatgptAuth.mu.Lock()
			f.chatgptAuth.lastError = err.Error()
			f.chatgptAuth.mu.Unlock()
			f.emitChatGPTAuthState(ChatGPTAuthEventData{State: chatgptAuthEventError, Error: err.Error()})
		}
	}()

	timeout := f.chatgptAuth.signInStartTimeoutOverride
	if timeout <= 0 {
		timeout = chatGPTSignInStartTimeout
	}
	select {
	case start := <-urlCh:
		return chatGPTSignInStartResponse(start)
	case <-time.After(timeout):
		// Prefer a URL (or an early failure) that landed inside the race
		// window over the timeout: select picks randomly among ready cases,
		// and a value that arrived just as the timer fired is the fresher
		// truth.
		select {
		case start := <-urlCh:
			return chatGPTSignInStartResponse(start)
		default:
		}
		// The flow never published anything within the bound: tear it down
		// and refuse. The goroutine's own bookkeeping cleans up whenever it
		// eventually returns (the cancel makes that prompt).
		f.chatgptAuth.mu.Lock()
		if f.chatgptAuth.inFlight {
			f.chatgptAuth.cancelRequested = true
		}
		f.chatgptAuth.mu.Unlock()
		cancel()
		return nil, fmt.Errorf("starting the ChatGPT sign-in flow: no authorization URL within %s", timeout)
	}
}

// chatGPTSignInStartResponse maps the flow's first observable fact to the RPC
// answer: the URL, or the refusal carrying the error that ended the flow
// before a URL existed. The flow goroutine's bookkeeping has already run.
func chatGPTSignInStartResponse(start chatGPTSignInStart) (*ChatGPTSignInResponse, error) {
	if start.err != nil {
		return nil, fmt.Errorf("starting the ChatGPT sign-in flow: %w", start.err)
	}
	return &ChatGPTSignInResponse{AuthURL: start.url}, nil
}

// chatGPTSignInStart is the one-shot handshake between the waiting RPC and
// the background flow's first observable fact.
type chatGPTSignInStart struct {
	url string
	err error
}

// CancelChatGPTSignIn cancels the in-flight sign-in. Idempotent — cancelling
// when no run exists succeeds; the cancelled event fires from the run's own
// failure branch, not here.
func (f *FrontendAPI) CancelChatGPTSignIn() error {
	f.chatgptAuth.mu.Lock()
	defer f.chatgptAuth.mu.Unlock()
	if !f.chatgptAuth.inFlight || f.chatgptAuth.cancel == nil {
		return nil
	}
	f.chatgptAuth.cancelRequested = true
	f.chatgptAuth.cancel()
	return nil
}

// GetChatGPTAuthStatus snapshots the ChatGPT subscription auth state. A
// read-only getter returning no error — an unavailable subsystem reports
// signed_out with the construction failure as last_error; secrets never
// appear (identity fields, mode, authoritative in_flight only).
// See specs/contracts/desktop-frontend.md.
func (f *FrontendAPI) GetChatGPTAuthStatus() ChatGPTAuthStatusResponse {
	mode := config.ChatGPTAuthModeAPIKey
	f.configMu.RLock()
	if f.config != nil && f.config.LLM.ChatGPT.Auth.Mode != "" {
		mode = f.config.LLM.ChatGPT.Auth.Mode
	}
	f.configMu.RUnlock()

	f.chatgptAuth.mu.Lock()
	m := f.chatgptAuth.manager
	initErr := f.chatgptAuth.initError
	resp := ChatGPTAuthStatusResponse{
		Mode:      mode,
		InFlight:  f.chatgptAuth.inFlight,
		LastError: f.chatgptAuth.lastError,
	}
	f.chatgptAuth.mu.Unlock()

	if m == nil {
		if resp.LastError == "" && initErr != "" {
			resp.LastError = initErr
		}
		return resp
	}
	st := m.Status()
	resp.SignedIn = st.SignedIn
	if st.SignedIn {
		resp.Email = chatGPTIdentityField(st.Identity, func(i *providerauth.Identity) string { return i.Email })
		resp.AccountID = chatGPTIdentityField(st.Identity, func(i *providerauth.Identity) string { return i.ChatGPTAccountID })
		if !st.ExpiresAt.IsZero() {
			resp.ExpiresAt = st.ExpiresAt.Format(time.RFC3339)
		}
	}
	return resp
}

// SignOutChatGPT clears the persisted credentials and withdraws the builder
// seam, so subsequent requests through the chatgpt provider fail with the
// actionable sign-in-required error instead of riding a token nobody asked
// to keep (in oauth mode; in api_key mode the static key resumes its
// historical role the moment the seam is gone). The manager's own sign-out
// is idempotent — signing out while already signed out succeeds.
func (f *FrontendAPI) SignOutChatGPT() error {
	f.chatgptAuth.mu.Lock()
	m := f.chatgptAuth.manager
	if m == nil {
		err := f.chatGPTAuthNilManagerRefusalLocked("sign-out")
		f.chatgptAuth.mu.Unlock()
		return err
	}
	// An in-flight sign-in is CANCELLED and JOINED first: its run could
	// otherwise complete after the sign-out and re-persist the very
	// credentials being cleared. The stop is marked REQUESTED (quiet
	// cancelled event); the join is bounded (chatGPTSignInJoinTimeout) and
	// no lock is held while waiting.
	if f.chatgptAuth.inFlight {
		f.chatgptAuth.cancelRequested = true
		f.chatgptAuth.cancel()
		done := f.chatgptAuth.runDone
		f.chatgptAuth.mu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-time.After(chatGPTSignInJoinTimeout):
				f.log().Warn("sign-out proceeded while a cancelled ChatGPT sign-in was still concluding")
			}
		}
	} else {
		f.chatgptAuth.mu.Unlock()
	}

	if err := m.SignOut(); err != nil {
		return fmt.Errorf("signing out of ChatGPT: %w", err)
	}
	f.chatgptAuth.mu.Lock()
	f.chatgptAuth.lastError = ""
	f.chatgptAuth.mu.Unlock()
	f.syncChatGPTBuilderSeam()
	f.rebuildRouterAfterChatGPTAuthChange()
	return nil
}

// GetChatGPTModelPreset returns the curated ChatGPT (Codex) model preset
// offered in oauth mode: the OFFLINE fallback shown before any live fetch —
// see specs/domains/llm-providers.md. Read-only getter; metadata enrichment
// is fail-soft (names only before the registry is wired).
func (f *FrontendAPI) GetChatGPTModelPreset() ChatGPTModelPresetResponse {
	var reg *llm.ModelRegistry
	if b := f.builder(); b != nil {
		reg = b.ModelRegistry()
	}
	models := make([]ChatGPTModelPresetEntry, 0, len(chatGPTModelPreset))
	for _, name := range chatGPTModelPreset {
		entry := ChatGPTModelPresetEntry{Name: name}
		if reg != nil {
			if meta, ok := reg.ResolveLocal(name); ok {
				entry.ContextWindow = meta.ContextWindow
				entry.OutputLimit = meta.OutputLimit
				entry.Reasoning = meta.Capabilities != nil && meta.Capabilities.Reasoning
			}
		}
		models = append(models, entry)
	}
	return ChatGPTModelPresetResponse{Models: models}
}

// chatGPTModelPreset is the ordered OFFLINE fallback preset (most capable
// first), shown before a live FetchChatGPTModels answers; every name exists
// in sp4rk's built-in registry. Pinned in TestGetChatGPTModelPreset.
var chatGPTModelPreset = []string{
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-5.3-codex",
	"codex-mini-latest",
}

// ---------------------------------------------------------------------------
// Live subscription model catalog (FetchChatGPTModels)
// ---------------------------------------------------------------------------

// The ChatGPT Codex backend publishes the per-account model catalog at
// GET {APIBaseURL}/models?client_version=<semver> — the same endpoint the
// Codex CLI uses. The wire shape is the backend's own (`models[].slug`, not
// the public API's data/id), and the picker contract mirrors the CLI:
// visibility=="list" only, ascending priority. client_version is gated PER
// MODEL (minimal_client_version): a too-low version yields a valid-but-empty
// catalog — see specs/decisions/074-chatgpt-subscription-auth.md (D9).

const (
	// chatGPTModelsPath is the catalog path under the profile's API base.
	chatGPTModelsPath = "/models"
	// chatGPTModelsTimeout bounds one live catalog request.
	chatGPTModelsTimeout = 15 * time.Second
	// chatGPTModelsMaxBytes bounds the catalog download before decoding — the
	// Codex CLI's MAX_MODEL_CATALOG_BYTES analogue against a ballooning body.
	chatGPTModelsMaxBytes = 1 << 20
	// chatGPTCatalogClientVersion is the client_version the catalog request
	// reports: the current Codex CLI release whose wire contract c0wrk
	// implements — a lower version is answered with a 200 and an EMPTY
	// catalog (see specs/decisions/074-chatgpt-subscription-auth.md D9, which
	// also guards the pin by test). Bump alongside the pinned Codex contract
	// whenever the catalog starts coming back empty.
	chatGPTCatalogClientVersion = "0.159.3"
)

// chatGPTModelsCatalog is the wire shape of the subscription /models
// response. Only the fields c0wrk consumes are decoded; unknown fields are
// ignored by encoding/json.
type chatGPTModelsCatalog struct {
	Models []chatGPTCatalogModel `json:"models"`
}

// chatGPTCatalogModel is one catalog entry (the Codex backend's ModelInfo).
type chatGPTCatalogModel struct {
	Slug             string `json:"slug"`
	Visibility       string `json:"visibility"`
	Priority         int    `json:"priority"`
	ContextWindow    *int64 `json:"context_window"`
	MaxContextWindow *int64 `json:"max_context_window"`
}

// chatGPTProxyConfig snapshots the effective proxy configuration for the
// auth/catalog outbound paths, mirroring the builder's own normalization:
// the URL AND the TLS CA directory are env-expanded (${VAR}) while the
// bypass list rides along verbatim. Without the TLSCertDir expansion a
// proxy-only network whose CA directory is configured through an env var
// would hand the OAuth exchange and the catalog fetch a literal "${VAR}"
// path and fail the TLS handshake the LLM traffic survives.
func (f *FrontendAPI) chatGPTProxyConfig() proxy.Config {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	if f.config == nil {
		return proxy.Config{}
	}
	return proxy.Config{
		Enabled:    f.config.Proxy.Enabled,
		URL:        config.ExpandEnvVars(f.config.Proxy.URL),
		BypassList: f.config.Proxy.BypassList,
		TLSCertDir: config.ExpandEnvVars(f.config.Proxy.TLSCertDir),
	}
}

// chatGPTOutboundHTTPClient builds the HTTP client the auth/catalog paths
// use (token exchange/refresh and the live model-catalog fetch): the SAME
// effective proxy configuration the LLM requests ride on, with the caller's
// timeout. Without it those requests leave through the default transport and
// break in a proxy-only network. A disabled or failing proxy config degrades
// to the default transport, exactly like the builder's own BuildClient
// fallback.
func (f *FrontendAPI) chatGPTOutboundHTTPClient(timeout time.Duration) *http.Client {
	proxyCfg := f.chatGPTProxyConfig()

	client, err := proxy.BuildClient(proxyCfg, timeout, f.log())
	if err != nil {
		// The builder's own fallback LOGS the failure (core/builder.go):
		// an operator with a malformed proxy URL otherwise sees OAuth
		// exchange/catalog failures over a direct route with no line naming
		// the bypassed proxy. Mirror that diagnostic here.
		f.log().Warn("failed to build the ChatGPT outbound proxy client, proceeding without proxy", "error", err)
		return &http.Client{Timeout: timeout}
	}
	if client == nil {
		// Proxy disabled: the bounded default transport, silently.
		return &http.Client{Timeout: timeout}
	}
	return client
}

// pushChatGPTManagerProxyClientLocked re-points the published manager's
// token-endpoint client at the CURRENT effective proxy configuration
// (protocol at chatgptAuthState.pushMu).
// Caller holds chatgptAuth.mu; the install runs outside it. Skipped for a
// manager without the seam (test fakes).
func (f *FrontendAPI) pushChatGPTManagerProxyClientLocked() {
	m := f.chatgptAuth.manager
	if m == nil {
		return
	}
	setter, ok := m.(interface{ SetHTTPClient(*http.Client) })
	if !ok {
		return
	}
	client := f.chatGPTOutboundHTTPClient(30 * time.Second)
	f.chatgptAuth.pushSeq++
	seq := f.chatgptAuth.pushSeq
	f.chatgptAuth.mu.Unlock()
	f.chatgptAuth.pushMu.Lock()
	if seq > f.chatgptAuth.pushInstalledSeq {
		f.chatgptAuth.pushInstalledSeq = seq
		setter.SetHTTPClient(client)
	}
	f.chatgptAuth.pushMu.Unlock()
	f.chatgptAuth.mu.Lock()
}

// FetchChatGPTModels returns the models THE SUBSCRIPTION actually serves —
// the authoritative oauth-mode list from the live Codex catalog (the static
// preset is only its offline fallback; see specs/decisions/074-chatgpt-subscription-auth.md).
// Refusals are actionable (subsystem unavailable, not signed in, token
// failure); metadata enrichment is fail-soft per entry (registry-known slugs
// carry full catalog metadata, unknown slugs keep the endpoint's context
// window and stay usable via the router's registry fallback).
func (f *FrontendAPI) FetchChatGPTModels() (ChatGPTModelPresetResponse, error) {
	f.chatgptAuth.mu.Lock()
	m := f.chatgptAuth.manager
	baseURL := f.chatgptAuth.modelsBaseURLOverride
	if m == nil {
		err := f.chatGPTAuthNilManagerRefusalLocked("model list")
		f.chatgptAuth.mu.Unlock()
		return ChatGPTModelPresetResponse{}, err
	}
	f.chatgptAuth.mu.Unlock()
	if !m.Status().SignedIn {
		return ChatGPTModelPresetResponse{}, errors.New("fetching the ChatGPT model list requires a subscription sign-in — sign in with ChatGPT first")
	}

	ctx, cancel := context.WithTimeout(f.ctx(), chatGPTModelsTimeout)
	defer cancel()
	tok, err := m.Token(ctx)
	if err != nil {
		return ChatGPTModelPresetResponse{}, fmt.Errorf("fetching the ChatGPT model list: resolving the access token: %w", err)
	}

	if baseURL == "" {
		baseURL = providerauth.ChatGPT().APIBaseURL
	}
	catalogURL := baseURL + chatGPTModelsPath + "?client_version=" + url.QueryEscape(chatGPTCatalogClientVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, catalogURL, http.NoBody)
	if err != nil {
		return ChatGPTModelPresetResponse{}, fmt.Errorf("fetching the ChatGPT model list: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// The snapshot is a BearerToken: the same headers a chat request carries;
	// the token value itself never reaches a log line.
	if tok.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	}
	for k, v := range tok.ExtraHeaders {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	// The catalog fetch rides the SAME effective proxy configuration the
	// chat requests do (chatGPTOutboundHTTPClient).
	resp, err := f.chatGPTOutboundHTTPClient(chatGPTModelsTimeout).Do(req)
	if err != nil {
		return ChatGPTModelPresetResponse{}, fmt.Errorf("fetching the ChatGPT model list: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, chatGPTModelsMaxBytes))
	if err != nil {
		return ChatGPTModelPresetResponse{}, fmt.Errorf("fetching the ChatGPT model list: reading the response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return ChatGPTModelPresetResponse{}, fmt.Errorf("fetching the ChatGPT model list: the subscription backend answered %s: %s",
			resp.Status, chatGPTModelsErrorSnippet(body))
	}

	var catalog chatGPTModelsCatalog
	if err := json.Unmarshal(body, &catalog); err != nil {
		return ChatGPTModelPresetResponse{}, fmt.Errorf("fetching the ChatGPT model list: decoding the catalog: %w", err)
	}

	// The backend's picker contract (specs/decisions/074-chatgpt-subscription-auth.md):
	// visibility=="list" only, ascending priority, slug tiebreaker.
	visible := make([]chatGPTCatalogModel, 0, len(catalog.Models))
	for _, cm := range catalog.Models {
		if cm.Slug == "" || cm.Visibility != "list" {
			continue
		}
		visible = append(visible, cm)
	}
	slices.SortStableFunc(visible, func(a, b chatGPTCatalogModel) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		return strings.Compare(a.Slug, b.Slug)
	})

	var reg *llm.ModelRegistry
	if b := f.builder(); b != nil {
		reg = b.ModelRegistry()
	}
	models := make([]ChatGPTModelPresetEntry, 0, len(visible))
	for _, cm := range visible {
		models = append(models, chatGPTCatalogEntry(cm, reg))
	}
	return ChatGPTModelPresetResponse{Models: models}, nil
}

// chatGPTCatalogEntry maps one catalog entry to the preset entry shape,
// enriching it with registry metadata when the registry knows the slug.
func chatGPTCatalogEntry(cm chatGPTCatalogModel, reg *llm.ModelRegistry) ChatGPTModelPresetEntry {
	entry := ChatGPTModelPresetEntry{Name: cm.Slug}
	if reg != nil {
		if meta, ok := reg.ResolveLocal(cm.Slug); ok {
			entry.ContextWindow = meta.ContextWindow
			entry.OutputLimit = meta.OutputLimit
			entry.Reasoning = meta.Capabilities != nil && meta.Capabilities.Reasoning
		}
	}
	if entry.ContextWindow <= 0 {
		if cm.ContextWindow != nil && *cm.ContextWindow > 0 {
			entry.ContextWindow = int(*cm.ContextWindow)
		} else if cm.MaxContextWindow != nil && *cm.MaxContextWindow > 0 {
			entry.ContextWindow = int(*cm.MaxContextWindow)
		}
	}
	return entry
}

// chatGPTModelsErrorSnippet renders a bounded, secret-free snippet of a
// non-200 catalog response for the actionable error message.
func chatGPTModelsErrorSnippet(body []byte) string {
	snippet := strings.TrimSpace(string(body))
	if snippet == "" {
		return "(empty response body)"
	}
	if len(snippet) > 200 {
		snippet = snippet[:200] + "…"
	}
	return snippet
}

// ---------------------------------------------------------------------------
// Builder seam + router rebuild
// ---------------------------------------------------------------------------

// syncChatGPTBuilderSeam mirrors the live auth state into the BUILDER's
// default subscription-auth seam — the net covering every router build whose
// BuilderConfig carries no TokenSource of its own, most importantly the
// per-session routers (ADR-074 D8). A signed-in manager installs the live
// source; a signed-out one installs the zero value (the sign-in-required
// stand-in remains). Exactly the shape of syncEmbeddedBuilderSeam.
//
// MUST NOT be called with configMu held (the rebuild that follows takes
// saveMu → configMu.RLock).
func (f *FrontendAPI) syncChatGPTBuilderSeam() {
	b := f.builder()
	if b == nil {
		return
	}
	f.chatgptAuth.mu.Lock()
	m := f.chatgptAuth.manager
	f.chatgptAuth.mu.Unlock()

	var seam core.BuilderSubscriptionAuthConfig
	if m != nil && m.Status().SignedIn {
		seam = core.BuilderSubscriptionAuthConfig{
			ProviderName: chatGPTProviderName,
			TokenSource:  m,
		}
	}
	b.SetSubscriptionTokenSource(seam)
}

// rebuildRouterAfterChatGPTAuthChange rebuilds the judge and the LLM router
// so the auth-state change reaches the cached router without a restart. Runs
// under saveMu and takes configMu.RLock across snapshot + rebuild exactly
// like UpdateLLMConfig and rebuildAfterEmbeddedConfigChange. A rebuild
// failure is logged, not returned: the auth state already changed, the
// keychain is authoritative, and the next load picks the seam up.
func (f *FrontendAPI) rebuildRouterAfterChatGPTAuthChange() {
	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.syncChatGPTBuilderSeam()
	b := f.builder()
	if b == nil {
		return
	}
	f.configMu.RLock()
	fresh := f.toBuilderConfigLocked()
	b.RebuildJudge(fresh)
	// Push the fresh model metadata into every LIVE per-session registry —
	// the same ordering rationale as the embedded rebuild: live sessions
	// keep their own registries and would otherwise guard prompts with
	// stale metadata forever.
	b.UpdateModelOverrides(fresh)
	err := b.RebuildRouter(fresh)
	f.configMu.RUnlock()
	if err != nil {
		f.log().Warn("failed to rebuild the LLM router after a ChatGPT auth change", "error", err)
	}
}

// ---------------------------------------------------------------------------
// Event emission + identity helpers
// ---------------------------------------------------------------------------

// chatgpt_auth:state payload states.
const (
	chatgptAuthEventPending   = "pending"
	chatgptAuthEventSuccess   = "success"
	chatgptAuthEventError     = "error"
	chatgptAuthEventCancelled = "cancelled"
)

// emitChatGPTAuthState publishes one chatgpt_auth:state transition. The
// payload never carries a secret: URLs and identity fields only.
func (f *FrontendAPI) emitChatGPTAuthState(data ChatGPTAuthEventData) {
	f.seedAcquire()
	if f.emitEvent == nil {
		return
	}
	f.emitEvent(EventChatGPTAuthState, data)
}

func chatGPTIdentity(result *providerauth.SignInResult) *providerauth.Identity {
	if result == nil {
		return nil
	}
	return result.Identity
}

func chatGPTExpiry(result *providerauth.SignInResult) string {
	if result == nil || result.Tokens == nil || result.Tokens.Expiry.IsZero() {
		return ""
	}
	return result.Tokens.Expiry.Format(time.RFC3339)
}

// chatGPTIdentityField extracts a display field from a possibly-nil
// Identity (mirrors providerauth.identityField, which is unexported).
func chatGPTIdentityField(id *providerauth.Identity, get func(*providerauth.Identity) string) string {
	if id == nil {
		return ""
	}
	return get(id)
}
