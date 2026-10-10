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

// The RPC surface of the ChatGPT subscription sign-in (see
// specs/contracts/desktop-frontend.md and
// specs/contracts/event-catalog.md).
//
// Ownership: this file owns the FrontendAPI-side wiring of
// backend/providerauth — the token manager's lifecycle, the sign-in flow's
// busy gate, and the global chatgpt_auth:state event. It owns NO OAuth
// policy: the endpoints, PKCE, the loopback listener and the token refresh
// all live in backend/providerauth.
//
// The conventions the surface follows (mirroring the embedded-LLM surface):
//
//   - GetChatGPTAuthStatus and GetChatGPTModelPreset are read-only getters
//     and therefore return no error (desktop-frontend.md "RPC Surface"): an
//     unavailable subsystem reports the signed-out state instead of failing.
//   - every mutating method returns an error, and an error it returns is
//     actionable — it names the refused operation and the reason.
//   - the interactive sign-in runs in the BACKGROUND: StartChatGPTSignIn
//     performs only the busy-gate check, then waits — bounded — for the flow
//     to publish the authorization URL and returns it, so the FRONTEND opens
//     the system browser (runtime.BrowserOpenURL) and the RPC never blocks
//     on the user completing the flow in that browser. Progress arrives
//     through chatgpt_auth:state (pending → success | error | cancelled).
//   - the background sign-in is CANCELLABLE: CancelChatGPTSignIn delivers
//     the operator's stop through the run's context, and a REQUESTED
//     cancellation is the quiet outcome — no recorded last_error, the
//     cancelled event is the report — while a genuine failure (the keychain
//     refusing the persist, a state mismatch, the listener failing to bind)
//     lands in both the error event and the status's last_error.
//   - startup constructs the manager but performs NO network I/O and never
//     blocks: InitChatGPTAuth only loads whatever credentials the keychain
//     already holds and mirrors the live token source into the builder seam
//     for every restored state (mode-independent — the seam is inert in
//     api_key mode); the router rebuild happens only when oauth mode is
//     selected and an account was restored.
//   - secrets never cross this boundary: the status carries identity fields
//     only (email, account id, expiry), and no OAuth token value ever
//     appears in an event, an RPC result, or a log line.

// chatGPTSignInJoinTimeout bounds how long SignOutChatGPT waits for a
// cancelled in-flight sign-in to conclude before proceeding anyway. The
// flow's redirect wait and token exchange both honor the run's context, so
// a cancelled run concludes in milliseconds; the bound exists only so the
// JOIN cannot hang on a run wedged in a stubborn network call. It does NOT
// bound the whole RPC: after the join (or its expiry) the manager's own
// sign-out deletes the keychain record under the manager state lock, and a
// run wedged inside its keychain write holds that same lock — the same
// unbounded keychain behavior every auth path here tolerates. On expiry the
// sign-out proceeds — last writer wins, and a flow that somehow persists
// after it leaves the account signed in again, visible in the next status
// read and removable by a second sign-out.
const chatGPTSignInJoinTimeout = 5 * time.Second

// chatGPTSignInStartTimeout bounds how long StartChatGPTSignIn waits for the
// background flow to publish the authorization URL. Binding the loopback
// listener and deriving the PKCE pair is local, millisecond-scale work; the
// bound exists only so a wedged flow returns an actionable error instead of
// hanging the RPC forever.
const chatGPTSignInStartTimeout = 10 * time.Second

// chatGPTProviderName is the router entry the subscription-auth seam serves.
// It matches the provider name ToBuilderConfig builds the chatgpt entry
// under (backend/configadapter.go); the seam's serves() match fails closed
// on any other value.
const chatGPTProviderName = "chatgpt"

// chatgptAuthState is the ChatGPT subscription-auth subsystem state. It
// lives on FrontendAPI as the value field `chatgptAuth`, so the zero value
// (no manager) is usable before InitChatGPTAuth runs: every RPC then reports
// the signed-out posture with the construction error as last_error instead
// of panicking.
//
// mu guards the whole subsystem bookkeeping. It is never held across a
// manager call: SignIn runs on its own goroutine (the inFlight flag and the
// cancel are captured under mu before it spawns), SignOut/Status are
// manager-synchronized themselves, and the proxy push builds its client
// under mu but installs it outside (pushMu). The one deliberate exception is
// the restore-correction emit at the end of InitChatGPTAuth, which holds mu
// across emitChatGPTAuthState so the inFlight check and the emission cannot
// interleave (the emitter never re-enters the auth surface). This mirrors
// the embedded subsystem's "never hold mu across a call into the supervised
// component" rule.
type chatgptAuthState struct {
	mu sync.Mutex
	// pushMu serializes ONLY the manager SetHTTPClient installs (see
	// pushChatGPTManagerProxyClientLocked): they run OUTSIDE mu — a manager
	// call internally takes TokenManager.mu, which a prompt-blocked keychain
	// write may hold. pushSeq/pushInstalledSeq carry the ordering guarantee
	// across that outside-mu window: seq is assigned under mu (config-read
	// order) and an install is skipped when a newer push already installed —
	// a preempted older push cannot revert a newer client.
	pushMu           sync.Mutex
	pushSeq          uint64
	pushInstalledSeq uint64
	// manager owns the OAuth lifecycle once constructed; nil when
	// construction failed (initError names why — most commonly a Linux
	// desktop without a reachable Secret Service).
	manager chatGPTAuthManager
	// initError is the actionable construction failure recorded by
	// InitChatGPTAuth. Empty when the manager exists.
	initError string
	// restoring marks the TRANSIENT startup window in which the background
	// keychain restore launched by InitChatGPTAuth (desktop startup runs it
	// off the readiness path — see initChatGPTAuth) has not concluded yet:
	// the manager is still nil and initError still empty, but the subsystem
	// IS coming up. RPCs refused in that window say so ("try again in a
	// moment") instead of claiming the subsystem was never initialized.
	restoring bool
	// inFlight marks a background sign-in run; exactly one may exist at a
	// time. cancel is its context's CancelFunc and runDone is closed by the
	// run's goroutine when it has fully concluded (bookkeeping done) — both
	// published while inFlight.
	inFlight bool
	cancel   context.CancelFunc
	runDone  chan struct{}
	// cancelRequested records that the OPERATOR asked to cancel the current
	// run, so the failure branch can tell a REQUESTED cancellation (this
	// flag AND errors.Is(err, context.Canceled)) from a genuine fault, and
	// from a shutdown-driven cancellation of the parent context (the flag
	// alone must not silence anything).
	cancelRequested bool
	// lastError carries the operator-friendly cause of the last FAILED
	// sign-in run. It surfaces as the status's last_error field and is
	// cleared when the next run starts or a sign-in succeeds.
	lastError string
	// modelsBaseURLOverride replaces the ChatGPT profile's API base URL in
	// FetchChatGPTModels, so tests can point the live catalog fetch at an
	// httptest server instead of chatgpt.com. Zero in production.
	modelsBaseURLOverride string

	// Test seams, mirroring embeddedLLMState's. All are zero in production.
	// signInStartTimeoutOverride replaces chatGPTSignInStartTimeout.
	// newManagerFn replaces the manager construction in InitChatGPTAuth, so
	// tests inject a mock without touching the OS keychain.
	signInStartTimeoutOverride time.Duration
	newManagerFn               func() (chatGPTAuthManager, error)
}

// chatGPTAuthManager is the subset of *providerauth.TokenManager this
// surface depends on. Production always injects the real manager (the
// compile-time assertion below); tests substitute a mock so the RPC flows
// are exercised without a browser, a keychain or a network.
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
// credentials the OS keychain already holds. Called from
// desktop/startup_phases.go on the startup path — never from the RPC
// surface. It performs no network I/O and no browser flow; a failed
// construction (most commonly a Linux desktop without a reachable Secret
// Service) records the actionable error and leaves the subsystem in its
// signed-out posture instead of failing startup: the rest of the app works
// with api_key auth, and the status RPC names the reason.
//
// The live token source is mirrored into the builder seam for EVERY restored
// state (signed in or not), independent of the configured auth mode: the
// seam is consumed only by SubscriptionAuth-marked entries (oauth mode), so
// the mirror is inert in api_key mode — but it guarantees that a later
// auth-mode switch to oauth picks the restored credentials up without a
// restart or a re-sign-in. When the restored state is signed in AND the
// config selects oauth mode, the router is also rebuilt immediately — the
// exact restore shape of the embedded subsystem. Request correctness does
// not depend on that rebuild: every subscription entry carries the dynamic
// request-time stand-in, which re-reads the builder's current seam on every
// call, so the app-level router built inside NewApplication (before any
// FrontendAPI existed) starts serving subscription credentials the moment
// the seam mirror lands. The rebuild still matters: it eagerly swaps the
// entry to the direct seam source (no per-request indirection) and refreshes
// the judge and the model metadata alongside it. The rebuild is
// skipped when nothing was restored (the signed-out stand-in the router
// already carries is the truth); the seam mirror still runs, so the first
// sign-in's rebuild is covered either way. A signed-in restore also emits a
// chatgpt_auth:state success transition carrying the restored identity:
// the restore runs on a background goroutine, so a Settings panel that
// rendered against the pre-restore zero state needs the event to correct
// itself (its snapshot refreshes on mount only); the emission is skipped
// when an interactive sign-in is already in flight — its own terminal
// event re-reads the authoritative snapshot, and a mid-flow restore-success
// would clear the busy posture prematurely.
func (l *FrontendAPILifecycle) InitChatGPTAuth() {
	f := l.f
	// The restore may run on a background goroutine launched by the desktop
	// startup (the OS keychain read is not provably bounded — a locked Linux
	// login collection prompts before it even looks for the record). Do not
	// publish a restore into an app that is already shutting down: the seam
	// mirror and the router rebuild would race the teardown.
	if err := f.ctx().Err(); err != nil {
		return
	}
	newManager := f.chatgptAuth.newManagerFn
	if newManager == nil {
		newManager = func() (chatGPTAuthManager, error) {
			return providerauth.NewTokenManager(providerauth.ManagerConfig{
				Profile: providerauth.ChatGPT(),
				Store:   providerauth.NewKeyringSecretStore(),
				// Token-endpoint exchange/refresh ride the SAME effective
				// proxy configuration the LLM requests do (see
				// chatGPTOutboundHTTPClient) — a restored account must be
				// able to refresh behind the operator's proxy, not only on
				// the default route.
				HTTPClient: f.chatGPTOutboundHTTPClient(30 * time.Second),
				Logger:     f.log(),
			})
		}
	}
	// Mark the restore IN FLIGHT around the (possibly slow) keychain read so
	// RPCs refused meanwhile report the transient truth instead of "not
	// initialized". The read itself is wrapped in a recover: the keyring
	// backend is native code (the panic surface the desktop startup's safeGo
	// containment exists for), and while safeGo keeps the PROCESS alive, an
	// unwrapped panic would skip the restoring=false below — wedging every
	// mutating auth RPC on the transient "still reading the OS keychain"
	// refusal forever. Converting the panic into an error runs the normal
	// error path instead: the flag is cleared and initError names the cause.
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
	// A non-fatal restore problem (unreadable or incomplete stored record)
	// surfaces as the status's last_error: the manager is signed out but
	// usable, and the warning names the remediation (sign in again to
	// replace the record) instead of leaving the account silently gone.
	// LoadWarning is genuinely read OUTSIDE mu (the manager call takes
	// TokenManager.mu internally, which a wedged keychain may hold); the
	// result is written back under it below. The manager IS observable by
	// other goroutines in that window — RPCs already operate on the
	// published manager — which is fine: the warning is advisory, and the
	// write itself is atomic under mu, so a reader sees either no warning
	// or the complete one.
	var restoreWarning string
	if wm, ok := m.(interface{ LoadWarning() string }); ok {
		restoreWarning = wm.LoadWarning()
	}
	// Re-point the manager at the CURRENT proxy configuration: the
	// construction client above was captured before the keychain read, so
	// a proxy change that landed while that read was in flight must not be
	// missed (see pushChatGPTManagerProxyClientLocked, which releases mu
	// around the install — the manager is already published at this point;
	// the push closes the restore-window gap the construction-era client
	// left).
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
	// configured auth mode. The seam is consumed only by router entries
	// carrying the SubscriptionAuth marker (set by ToBuilderConfig for oauth
	// mode), so installing it in api_key mode is inert — but it means a later
	// UpdateLLMConfig switch to oauth (which rebuilds the router) picks the
	// restored credentials up without another restart or re-sign-in. Without
	// this, an account restored while the config kept api_key mode leaves the
	// seam empty: status reports signed-in, yet every router — fresh ones
	// included — answers with the sign-in-required error.
	f.syncChatGPTBuilderSeam()

	st := m.Status()
	if st.SignedIn && f.chatGPTAuthModeIsOAuth() {
		f.rebuildRouterAfterChatGPTAuthChange()
	}
	if st.SignedIn {
		// Skip the correction when an interactive sign-in is already in
		// flight: its pending event has the panel in the busy posture, and
		// its own terminal event (success | error | cancelled) re-reads
		// the authoritative snapshot anyway — a restore-success arriving
		// mid-flow would clear the busy flag prematurely. The restore
		// itself is unaffected (the seam mirror and the rebuild above
		// already ran).
		//
		// The check and the emission run under ONE critical section: reading
		// inFlight, releasing the lock and only then emitting would let a
		// sign-in that starts in between publish its pending event FIRST and
		// still receive this success — clearing the busy posture the pending
		// event just established, the exact interleaving the skip exists to
		// prevent. Holding the lock across the emit is safe because the
		// emitter never re-enters the auth subsystem.
		f.chatgptAuth.mu.Lock()
		if !f.chatgptAuth.inFlight {
			// The restore runs on a BACKGROUND goroutine, so a Settings panel
			// that already rendered against the pre-restore zero state (the
			// signed-out posture every RPC served while the keychain read was
			// still in flight) has no later fact that would correct it — the
			// panel refreshes its snapshot only on mount. Publish the same
			// success transition an interactive sign-in emits, carrying the
			// restored identity — after any router rebuild, so a chat request
			// issued after the event already carries subscription credentials.
			// The event is the designed correction channel, and a panel that
			// mounted after the restore never sees it (its mount-read already
			// returned the truth).
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
// reason distinguishes the three nil states in the operator's terms:
//   - a PERMANENT construction failure — the recorded initError names the
//     remediation (e.g. unlocking the keychain service);
//   - the TRANSIENT startup window — the background keychain restore
//     launched by the desktop startup is still in flight, so the honest
//     answer is "try again in a moment", not a claim that the subsystem
//     was never initialized;
//   - the genuinely-uninitialized state (InitChatGPTAuth never ran — the
//     desktop startup calls it unconditionally, so production never sees
//     it; tests construct FrontendAPI directly).
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
// an error the caller's normal failure path handles (initError records it,
// the restoring flag is cleared) instead of a recovered-at-the-top panic
// that would skip that bookkeeping. This mirrors the desktop startup's
// safeGo containment, scoped to the one call that needs it.
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
// backend never launches a browser on this path). The flow itself runs in
// the background: this RPC waits only until the flow has bound its loopback
// listener and derived the URL (bounded by chatGPTSignInStartTimeout), then
// returns. Every later transition arrives through chatgpt_auth:state
// (pending → success | error | cancelled).
//
// At most one sign-in may be in flight: a second call is refused with an
// actionable error naming the in-flight run (the events of two interleaved
// flows would paint a pending state no listener can complete).
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
	// the flow before the opener ever ran (listener bind failure, PKCE
	// generation failure). Buffered so the goroutine never blocks on a
	// waiter that already timed out.
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
			// The identity is OPTIONAL in providerauth (a token response
			// without an ID token yields a successful sign-in with a nil
			// identity) — never dereference it directly in this background
			// goroutine; a nil deref here would take the whole desktop
			// process down instead of reporting the sign-in outcome. The
			// nil-safe extractors emit empty identity fields instead.
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

// chatGPTSignInStartResponse maps the flow's first observable fact to the
// RPC answer: the URL for the frontend to open, or the refusal carrying the
// error that ended the flow before a URL existed. The bookkeeping has
// already run on the flow's goroutine by the time a value sits in urlCh.
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

// CancelChatGPTSignIn cancels the in-flight sign-in. Idempotent: cancelling
// when no run exists (it already completed, or none ever started) succeeds —
// the click is the report, and the cancelled event fires from the run's own
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

// GetChatGPTAuthStatus snapshots the ChatGPT subscription auth state. It is
// a read-only getter and returns no error: an unavailable subsystem reports
// signed_out with the construction failure as last_error. Secrets never
// appear — the payload carries identity fields (email, account id, token
// expiry), the configured auth mode, and the authoritative in_flight flag
// (a running browser sign-in) so a remounted panel restores its busy
// posture.
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
	// otherwise complete AFTER the sign-out below and re-persist the very
	// credentials being cleared — the account would silently sign back in.
	// The stop is marked REQUESTED so the run closes with the quiet
	// cancelled event instead of an error nobody asked for. The join is
	// bounded (chatGPTSignInJoinTimeout) and no lock is held while waiting;
	// the bound covers the JOIN only — see the const's doc for the residual
	// (unbounded) keychain delete the sign-out itself performs.
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
// offered in oauth mode: the offline FALLBACK the checklist shows before any
// live fetch — the models the ChatGPT subscription backend serves that
// c0wrk's model registry knows. The full, per-subscription list comes from
// FetchChatGPTModels (the backend's own /models catalog); this static preset
// exists so the checklist is never empty while offline or before the fetch.
// A read-only getter: it returns no error, and metadata enrichment is
// fail-soft — before the async model registry is wired the entries carry
// names only, mirroring collectAllModels' behavior.
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
// first), shown before a live FetchChatGPTModels answers. Every name exists
// in sp4rk's built-in registry so the fallback always renders with context
// metadata; the live catalog is the source of truth for what the
// subscription actually serves, so the fallback deliberately stays
// conservative. The list is pinned in TestGetChatGPTModelPreset against the
// live registry.
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
// Codex CLI uses for its own model picker. The response shape is NOT the
// public API's {"data":[{"id":…}]}: the key is "models" and the model
// identifier is "slug" (verified against codex-rs ModelsClient/ModelInfo).
// The picker contract mirrors the CLI: only visibility=="list" models are
// offered, ordered by ascending "priority" (rank 1 = the flagship).
// client_version gates the answer PER MODEL (each entry carries its own
// minimal_client_version): a client reporting a version below a model's
// minimum receives a 200 WITHOUT that model — a too-low version (e.g.
// "0.0.1") yields a valid-but-empty catalog, so the pinned CLI release
// above is what makes the subscription's full list visible.

const (
	// chatGPTModelsPath is the catalog path under the profile's API base.
	chatGPTModelsPath = "/models"
	// chatGPTModelsTimeout bounds one live catalog request.
	chatGPTModelsTimeout = 15 * time.Second
	// chatGPTModelsMaxBytes bounds the catalog download before decoding
	// (mirrors the Codex CLI's MAX_MODEL_CATALOG_BYTES). The live catalog is
	// ~600 KB today; the bound keeps a hostile answer from ballooning.
	chatGPTModelsMaxBytes = 1 << 20
	// chatGPTCatalogClientVersion is the client_version the catalog request
	// reports: the current Codex CLI release whose Responses wire contract
	// c0wrk implements. The backend gates catalog entries per model by
	// minimal_client_version — a lower client_version is answered with a
	// 200 and an EMPTY {"models":[]} (verified live: 0.0.1 yields exactly
	// that, silently), so reporting c0wrk's own (smaller) version would
	// blank the oauth-mode checklist. Bump this alongside the pinned Codex
	// contract whenever the catalog starts coming back empty.
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
// auth/catalog outbound paths, mirroring the builder's own normalization
// (backend/configadapter.go): the URL AND the TLS CA directory are
// env-expanded (${VAR} — the same expansion the LLM path applies), while
// the bypass list rides along verbatim. Without the TLSCertDir expansion a
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
// use (token-endpoint exchange/refresh and the live model-catalog fetch):
// the SAME effective proxy configuration the LLM requests ride on — proxy
// routing with authenticated credentials, the bypass list, and the custom CA
// directory — with the caller's own timeout. Without this, those requests
// would leave through the default transport and break in a proxy-only
// network (LLM traffic flows; the OAuth exchange or the catalog fetch times
// out). A disabled or failing proxy config degrades to the default
// transport, exactly like the builder's own BuildClient fallback.
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
// token-endpoint client at the CURRENT effective proxy configuration.
// Caller holds chatgptAuth.mu. The CONFIG READ and the manager lookup happen
// inside the critical section; the SetHTTPClient INSTALL runs outside it —
// the manager call takes TokenManager.mu internally, which can be held
// across a prompt-blocked keychain write, and chatgptAuth.mu must never sit
// above that (see chatgptAuthState.mu's rule). Install-order atomicity is
// preserved even across the mu-release window by a sequence guard: each
// push is assigned a monotonically increasing seq under chatgptAuth.mu (so
// the seq order matches the CONFIG-read order), and an install under pushMu
// is skipped when a NEWER push has already installed — a pusher preempted
// between its unlock and its pushMu acquisition cannot revert a newer
// client. (UpdateProxySettings' own calls are additionally serialized by
// saveMu, but InitChatGPTAuth is not — the seq guard covers that gap.)
//
// Two callers keep the manager's transport honest across the proxy config's
// lifetime:
//   - UpdateProxySettings, right after a proxy change lands in the config;
//   - InitChatGPTAuth, once the restored manager is published: the
//     construction client is captured BEFORE the (unbounded) keychain read,
//     so a proxy change landing while that read is still in flight is seen
//     by neither the construction capture nor the nil-manager push skip —
//     the post-publication push closes that restore-window gap.
//
// A manager without the seam (the test fakes) is skipped.
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

// FetchChatGPTModels returns the models THE SUBSCRIPTION actually serves,
// live from the ChatGPT Codex backend's model catalog (its /models
// endpoint, filtered to picker-visible entries and ordered by the backend's
// priority rank). This is the authoritative oauth-mode model list — the
// static preset from GetChatGPTModelPreset is only its offline fallback.
//
// Refusals are actionable: the subsystem being unavailable (keychain
// missing), not being signed in, and a failed/expired token each name the
// remediation. Metadata enrichment is fail-soft per entry: models the sp4rk
// registry knows carry their full catalog metadata (output limit, reasoning
// flag); unknown slugs — the catalog is ahead of the registry by design —
// fall back to the endpoint's own context window and zero output metadata,
// and the router's own registry fallback keeps such models usable.
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
	// The snapshot is a BearerToken: the same headers a chat request would
	// carry (Authorization + ExtraHeaders, e.g. ChatGPT-Account-Id). The
	// token value itself never reaches a log line.
	if tok.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	}
	for k, v := range tok.ExtraHeaders {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	// The catalog fetch rides the SAME effective proxy configuration the
	// chat requests do (see chatGPTOutboundHTTPClient) — a plain GET to the
	// subscription backend must not take the default route while every LLM
	// request flows through the operator's proxy.
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

	// The backend's picker contract: only visibility=="list" entries, in
	// ascending priority order (rank 1 = the flagship), slug as the
	// deterministic tiebreaker.
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
// default subscription-auth seam (core.OrchestratorBuilder.
// SetSubscriptionTokenSource) — the net that covers every router build whose
// BuilderConfig carries no TokenSource of its own, most importantly the
// per-session routers whose configs are converted deep inside the session
// factory where the token manager is not in scope. A signed-in manager
// installs the live source; a signed-out (or failed) manager installs the
// zero value, which leaves the oauth-marked entry with the actionable
// sign-in-required stand-in. Exactly the shape of syncEmbeddedBuilderSeam.
//
// MUST NOT be called with configMu held (nothing here takes it, and the
// rebuild that follows takes saveMu → configMu.RLock).
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
// so the auth-state change reaches the already-cached router without a
// restart. Runs under saveMu and takes configMu.RLock across snapshot +
// rebuild exactly like UpdateLLMConfig and
// rebuildAfterEmbeddedConfigChange, so a concurrent config writer cannot be
// rolled back and the debounced-save ordering holds. A rebuild failure is
// logged, not returned: the auth state itself already changed, the keychain
// is authoritative, and the next load picks the seam up.
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

// chatGPTIdentity extracts the identity of a successful sign-in result.
func chatGPTIdentity(result *providerauth.SignInResult) *providerauth.Identity {
	if result == nil {
		return nil
	}
	return result.Identity
}

// chatGPTExpiry extracts the access-token expiry of a sign-in result.
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
