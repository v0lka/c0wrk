package backend

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/llm"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/providerauth"
	"github.com/v0lka/c0wrk/core"
)

// The ChatGPT auth RPC surface, exercised against a mocked token manager
// (chatGPTAuthManager): no browser, no keychain, no network. The fake below
// mirrors the real TokenManager's contract — the opener runs INSIDE SignIn
// before the (simulated) redirect wait, Status snapshots without refreshing,
// SignOut is idempotent.

// fakeChatGPTManager is the mocked chatGPTAuthManager.
type fakeChatGPTManager struct {
	mu sync.Mutex

	// signedIn / identity / expiresAt are the Status snapshot fields.
	signedIn  bool
	identity  *providerauth.Identity
	expiresAt time.Time

	// signInHook, when set, REPLACES the default SignIn behavior so tests
	// can block the flow, cancel it, fail before the opener, etc. It runs
	// with the fake's lock NOT held and must be safe for one concurrent
	// caller.
	signInHook func(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error)

	// tokenExtraHeaders ride Token()'s BearerToken so FetchChatGPTModels
	// tests can assert they reach the catalog request (e.g.
	// ChatGPT-Account-Id).
	tokenExtraHeaders map[string]string

	// lastPushedClient records the most recent SetHTTPClient push (the
	// proxy re-point seam the real TokenManager exposes) so tests can
	// assert the manager's outbound client rides the current proxy.
	lastPushedClient *http.Client
	pushedClients    int

	signInCalls  int
	signOutErr   error
	signOutCalls int
}

// SetHTTPClient mirrors the real TokenManager's seam: the proxy push path
// type-asserts it. A nil client is ignored, exactly like the real one.
func (m *fakeChatGPTManager) SetHTTPClient(c *http.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c == nil {
		return
	}
	m.lastPushedClient = c
	m.pushedClients++
}

// defaultSignInURL is the fake authorization URL the default flow presents.
const defaultSignInURL = "https://auth.openai.com/oauth/authorize?client_id=fake&code_challenge=x"

func (m *fakeChatGPTManager) SignIn(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error) {
	m.mu.Lock()
	m.signInCalls++
	hook := m.signInHook
	m.mu.Unlock()

	if hook != nil {
		return hook(ctx, opener)
	}

	// Default behavior: present the URL (exactly what the real flow does
	// before waiting for the redirect), then succeed and become signed in.
	if err := opener(defaultSignInURL); err != nil {
		return nil, err
	}
	result := &providerauth.SignInResult{
		Tokens: &providerauth.Tokens{
			AccessToken: "fake-access",
			Expiry:      time.Now().Add(time.Hour).UTC(),
		},
		Identity: &providerauth.Identity{
			Email:            "user@example.com",
			ChatGPTAccountID: "acct-123",
		},
	}
	m.mu.Lock()
	m.signedIn = true
	m.identity = result.Identity
	m.expiresAt = result.Tokens.Expiry
	m.mu.Unlock()
	return result, nil
}

func (m *fakeChatGPTManager) SignOut() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.signOutCalls++
	if m.signOutErr != nil {
		return m.signOutErr
	}
	m.signedIn = false
	m.identity = nil
	m.expiresAt = time.Time{}
	return nil
}

func (m *fakeChatGPTManager) Status() providerauth.AccountStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := providerauth.AccountStatus{SignedIn: m.signedIn}
	if m.signedIn {
		st.Identity = m.identity
		st.ExpiresAt = m.expiresAt
	}
	return st
}

func (m *fakeChatGPTManager) Token(_ context.Context) (llm.BearerToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.signedIn {
		return llm.BearerToken{}, providerauth.ErrNotSignedIn
	}
	return llm.BearerToken{
		AccessToken:  "fake-access",
		TokenType:    "Bearer",
		ExtraHeaders: maps.Clone(m.tokenExtraHeaders),
	}, nil
}

// authPayloadEvent is one captured chatgpt_auth:state emission.
type authPayloadEvent struct {
	name string
	data ChatGPTAuthEventData
}

// authEventRecorder captures chatgpt_auth:state emissions with payloads.
type authEventRecorder struct {
	mu     sync.Mutex
	events []authPayloadEvent
}

func (r *authEventRecorder) emit(name string, args ...any) {
	var data ChatGPTAuthEventData
	if len(args) > 0 {
		if d, ok := args[0].(ChatGPTAuthEventData); ok {
			data = d
		}
	}
	r.mu.Lock()
	r.events = append(r.events, authPayloadEvent{name: name, data: data})
	r.mu.Unlock()
}

// waitForState polls until one event with the given state was captured, then
// returns it. The sign-in runs on a background goroutine, so tests must
// synchronize instead of asserting immediately.
func (r *authEventRecorder) waitForState(t *testing.T, state string) ChatGPTAuthEventData {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for _, e := range r.events {
			if e.name == EventChatGPTAuthState && e.data.State == state {
				r.mu.Unlock()
				return e.data
			}
		}
		r.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s event %q; captured: %v", EventChatGPTAuthState, state, r.states())
	return ChatGPTAuthEventData{}
}

// states lists the captured chatgpt_auth:state states in order.
func (r *authEventRecorder) states() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.events))
	for _, e := range r.events {
		if e.name == EventChatGPTAuthState {
			out = append(out, e.data.State)
		}
	}
	return out
}

// newChatGPTAuthTestAPI builds a FrontendAPI with a mock builder, a payload
// event recorder and a chatgpt-capable default model config, mirroring
// newTestAPI but with the emit/appCtx callbacks the auth surface needs.
func newChatGPTAuthTestAPI(t *testing.T) (*FrontendAPI, *mockBuilder, *authEventRecorder) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	cfg.LLM.DefaultModel = "gpt-5.5"
	cfg.LLM.ChatGPT.Models = []string{"gpt-5.5"}

	rec := &authEventRecorder{}
	mock := &mockBuilder{}
	f := &FrontendAPI{
		config:          cfg,
		configPath:      filepath.Join(dir, "config.yaml"),
		agentDir:        dir,
		builderOverride: mock,
		emitEvent:       rec.emit,
		appCtx:          context.Background,
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	return f, mock, rec
}

// installChatGPTManager injects the fake as the subsystem's manager through
// the construction seam and runs the startup init — exactly the production
// InitChatGPTAuth path, minus the keychain.
func installChatGPTManager(t *testing.T, f *FrontendAPI, m *fakeChatGPTManager) {
	t.Helper()
	f.chatgptAuth.newManagerFn = func() (chatGPTAuthManager, error) { return m, nil }
	f.Lifecycle().InitChatGPTAuth()
}

// --- StartChatGPTSignIn ---

// TestStartChatGPTSignIn_ReturnsURLForTheFrontendToOpen pins the split of
// responsibilities: the RPC returns the authorization URL (the FRONTEND opens
// the system browser), the pending event carries the same URL, and the flow
// keeps running in the background until it completes.
func TestStartChatGPTSignIn_ReturnsURLForTheFrontendToOpen(t *testing.T) {
	f, _, rec := newChatGPTAuthTestAPI(t)
	m := &fakeChatGPTManager{}
	installChatGPTManager(t, f, m)

	resp, err := f.StartChatGPTSignIn()
	if err != nil {
		t.Fatalf("StartChatGPTSignIn: %v", err)
	}
	if resp.AuthURL != defaultSignInURL {
		t.Fatalf("auth_url = %q, want %q", resp.AuthURL, defaultSignInURL)
	}

	pending := rec.waitForState(t, chatgptAuthEventPending)
	if pending.AuthURL != defaultSignInURL {
		t.Fatalf("pending event auth_url = %q, want %q", pending.AuthURL, defaultSignInURL)
	}
	if pending.Error != "" {
		t.Fatalf("pending event carries an error: %q", pending.Error)
	}

	// The flow completes in the background and reports success.
	success := rec.waitForState(t, chatgptAuthEventSuccess)
	if success.Email != "user@example.com" || success.AccountID != "acct-123" {
		t.Fatalf("success event identity = %+v", success)
	}
	if success.ExpiresAt == "" {
		t.Fatal("success event carries no expires_at")
	}
	if strings.Contains(success.ExpiresAt, defaultSignInURL) {
		t.Fatal("success event leaked the auth URL")
	}
}

// TestStartChatGPTSignIn_SuccessMirrorsSeamAndRebuilds covers the success
// tail: the live token source lands in the builder-level seam (the net that
// covers per-session routers), the judge + router are rebuilt, and the status
// RPC reflects the signed-in state.
func TestStartChatGPTSignIn_SuccessMirrorsSeamAndRebuilds(t *testing.T) {
	f, mock, rec := newChatGPTAuthTestAPI(t)
	m := &fakeChatGPTManager{}
	installChatGPTManager(t, f, m)

	if _, err := f.StartChatGPTSignIn(); err != nil {
		t.Fatalf("StartChatGPTSignIn: %v", err)
	}
	// The seam + rebuild run on the background run's success tail — wait
	// for the success event (emitted after them) so the assertions observe
	// the final state instead of racing the goroutine.
	rec.waitForState(t, chatgptAuthEventSuccess)

	seam, sets := mock.lastSubscriptionSeam()
	if sets == 0 {
		t.Fatal("the subscription seam was never pushed after a successful sign-in")
	}
	if seam.ProviderName != chatGPTProviderName {
		t.Fatalf("seam provider = %q, want %q", seam.ProviderName, chatGPTProviderName)
	}
	if seam.TokenSource == nil {
		t.Fatal("seam carries no token source after a successful sign-in")
	}
	if mock.rebuildRouterCallsSnapshot() == 0 {
		t.Fatal("the router was not rebuilt after a successful sign-in")
	}

	st := f.GetChatGPTAuthStatus()
	if !st.SignedIn {
		t.Fatal("status reports signed out after a successful sign-in")
	}
	if st.Email != "user@example.com" || st.AccountID != "acct-123" {
		t.Fatalf("status identity = %+v", st)
	}
	if st.ExpiresAt == "" {
		t.Fatal("status carries no expires_at while signed in")
	}
	if st.LastError != "" {
		t.Fatalf("status last_error = %q, want empty after success", st.LastError)
	}
}

// TestStartChatGPTSignIn_FailureRecordsError covers the failure tail: the
// error event carries the cause, the status keeps it as last_error, no seam
// is installed, and a new run may start immediately afterwards.
func TestStartChatGPTSignIn_FailureRecordsError(t *testing.T) {
	f, mock, rec := newChatGPTAuthTestAPI(t)
	boom := errors.New("providerauth: no browser redirect arrived before timeout")
	m := &fakeChatGPTManager{signInHook: func(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error) {
		// Present the URL first (the RPC must return it), then fail.
		_ = opener(defaultSignInURL)
		return nil, boom
	}}
	installChatGPTManager(t, f, m)

	if _, err := f.StartChatGPTSignIn(); err != nil {
		t.Fatalf("StartChatGPTSignIn: %v", err)
	}
	failure := rec.waitForState(t, chatgptAuthEventError)
	if failure.Error != boom.Error() {
		t.Fatalf("error event = %q, want %q", failure.Error, boom.Error())
	}

	st := f.GetChatGPTAuthStatus()
	if st.SignedIn {
		t.Fatal("status reports signed in after a failed sign-in")
	}
	if st.LastError != boom.Error() {
		t.Fatalf("status last_error = %q, want the failure cause", st.LastError)
	}
	if seam, _ := mock.lastSubscriptionSeam(); seam.TokenSource != nil {
		t.Fatal("the subscription seam carries a live source although the run failed")
	}

	// The busy gate is free again: a second run may start.
	m.mu.Lock()
	m.signInHook = nil
	m.mu.Unlock()
	if _, err := f.StartChatGPTSignIn(); err != nil {
		t.Fatalf("second StartChatGPTSignIn after a failure: %v", err)
	}
	rec.waitForState(t, chatgptAuthEventSuccess)
}

// TestStartChatGPTSignIn_EarlyFailureRefusesTheRPC covers the flow dying
// BEFORE it could present a URL: the RPC itself refuses with the cause
// instead of waiting out the start timeout, the bookkeeping is released, and
// the error event still fires.
func TestStartChatGPTSignIn_EarlyFailureRefusesTheRPC(t *testing.T) {
	f, _, rec := newChatGPTAuthTestAPI(t)
	boom := errors.New("providerauth: binding loopback listener failed")
	m := &fakeChatGPTManager{signInHook: func(context.Context, providerauth.BrowserOpener) (*providerauth.SignInResult, error) {
		return nil, boom
	}}
	installChatGPTManager(t, f, m)
	f.chatgptAuth.signInStartTimeoutOverride = time.Hour // prove the refusal is the early path, not the timeout

	_, err := f.StartChatGPTSignIn()
	if err == nil {
		t.Fatal("StartChatGPTSignIn succeeded although the flow failed before presenting a URL")
	}
	if !strings.Contains(err.Error(), boom.Error()) {
		t.Fatalf("refusal = %q, want it to carry %q", err.Error(), boom.Error())
	}
	failure := rec.waitForState(t, chatgptAuthEventError)
	if failure.Error != boom.Error() {
		t.Fatalf("error event = %q, want %q", failure.Error, boom.Error())
	}

	f.chatgptAuth.mu.Lock()
	inFlight := f.chatgptAuth.inFlight
	f.chatgptAuth.mu.Unlock()
	if inFlight {
		t.Fatal("the busy gate stayed held after an early failure")
	}
}

// TestStartChatGPTSignIn_RefusesSecondRunWhileInFlight pins the single-run
// gate: the second call is refused with an actionable error while the first
// run waits for its redirect.
func TestStartChatGPTSignIn_RefusesSecondRunWhileInFlight(t *testing.T) {
	f, _, rec := newChatGPTAuthTestAPI(t)
	release := make(chan struct{})
	m := &fakeChatGPTManager{signInHook: func(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error) {
		_ = opener(defaultSignInURL)
		select {
		case <-release:
			return nil, errors.New("first run concluded")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	installChatGPTManager(t, f, m)

	if _, err := f.StartChatGPTSignIn(); err != nil {
		t.Fatalf("first StartChatGPTSignIn: %v", err)
	}
	_, err := f.StartChatGPTSignIn()
	if err == nil {
		t.Fatal("a second sign-in was accepted while one was in flight")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("refusal = %q, want it to name the in-flight run", err.Error())
	}

	// Cancel the in-flight run and confirm the gate frees up.
	if err := f.CancelChatGPTSignIn(); err != nil {
		t.Fatalf("CancelChatGPTSignIn: %v", err)
	}
	rec.waitForState(t, chatgptAuthEventCancelled)
	close(release)
}

// TestStartChatGPTSignIn_StartTimeoutRefusesAndCovers the bounded wait: a
// flow that never presents a URL is refused after the (shortened) start
// timeout, and its run is torn down quietly.
func TestStartChatGPTSignIn_StartTimeoutRefusesAndCancels(t *testing.T) {
	f, _, rec := newChatGPTAuthTestAPI(t)
	m := &fakeChatGPTManager{signInHook: func(ctx context.Context, _ providerauth.BrowserOpener) (*providerauth.SignInResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	installChatGPTManager(t, f, m)
	f.chatgptAuth.signInStartTimeoutOverride = 30 * time.Millisecond

	start := time.Now()
	_, err := f.StartChatGPTSignIn()
	if err == nil {
		t.Fatal("StartChatGPTSignIn succeeded although no URL arrived")
	}
	if !strings.Contains(err.Error(), "no authorization URL") {
		t.Fatalf("refusal = %q, want the timeout cause", err.Error())
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("refusal took %s, want the bounded timeout", elapsed)
	}
	rec.waitForState(t, chatgptAuthEventCancelled)
}

// --- CancelChatGPTSignIn ---

// TestCancelChatGPTSignIn_QuietOutcome pins the requested-cancellation
// semantics: the cancelled event fires, no last_error is recorded, and
// cancelling an idle subsystem succeeds (the click is the report).
func TestCancelChatGPTSignIn_QuietOutcome(t *testing.T) {
	f, _, rec := newChatGPTAuthTestAPI(t)
	m := &fakeChatGPTManager{signInHook: func(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error) {
		_ = opener(defaultSignInURL)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	installChatGPTManager(t, f, m)

	if _, err := f.StartChatGPTSignIn(); err != nil {
		t.Fatalf("StartChatGPTSignIn: %v", err)
	}
	if err := f.CancelChatGPTSignIn(); err != nil {
		t.Fatalf("CancelChatGPTSignIn: %v", err)
	}
	rec.waitForState(t, chatgptAuthEventCancelled)

	st := f.GetChatGPTAuthStatus()
	if st.LastError != "" {
		t.Fatalf("a requested cancellation recorded last_error = %q, want none", st.LastError)
	}

	// Idempotent: cancelling with nothing in flight succeeds.
	if err := f.CancelChatGPTSignIn(); err != nil {
		t.Fatalf("idle CancelChatGPTSignIn: %v", err)
	}
}

// TestCleanupCancelsAnInFlightSignIn pins the teardown: Cleanup marks the
// stop REQUESTED (a quit is a deliberate stop, not a fault), so the run
// closes with the quiet cancelled event and records no error.
func TestCleanupCancelsAnInFlightSignIn(t *testing.T) {
	f, _, rec := newChatGPTAuthTestAPI(t)
	m := &fakeChatGPTManager{signInHook: func(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error) {
		_ = opener(defaultSignInURL)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	installChatGPTManager(t, f, m)

	if _, err := f.StartChatGPTSignIn(); err != nil {
		t.Fatalf("StartChatGPTSignIn: %v", err)
	}
	f.Lifecycle().Cleanup()
	rec.waitForState(t, chatgptAuthEventCancelled)

	st := f.GetChatGPTAuthStatus()
	if st.LastError != "" {
		t.Fatalf("a shutdown cancellation recorded last_error = %q, want none", st.LastError)
	}
}

// --- GetChatGPTAuthStatus ---

// TestGetChatGPTAuthStatus_UnavailableSubsystem covers the failed
// construction posture: the getter still answers (signed out, mode from
// config, the construction error as last_error) and the mutating RPCs refuse
// with the actionable cause.
func TestGetChatGPTAuthStatus_UnavailableSubsystem(t *testing.T) {
	f, _, _ := newChatGPTAuthTestAPI(t)
	captureAPIDiagnostic(t, f, "ChatGPT subscription auth unavailable", "error", "no Secret Service reachable")
	boom := errors.New("providerauth: reading \"providerauth:chatgpt\" from the OS keychain: no Secret Service reachable")
	f.chatgptAuth.newManagerFn = func() (chatGPTAuthManager, error) { return nil, boom }
	f.Lifecycle().InitChatGPTAuth()

	st := f.GetChatGPTAuthStatus()
	if st.SignedIn {
		t.Fatal("status reports signed in although the subsystem failed to construct")
	}
	if st.Mode != config.ChatGPTAuthModeAPIKey {
		t.Fatalf("mode = %q, want the configured api_key default", st.Mode)
	}
	if !strings.Contains(st.LastError, "no Secret Service reachable") {
		t.Fatalf("last_error = %q, want the construction cause", st.LastError)
	}

	if _, err := f.StartChatGPTSignIn(); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("StartChatGPTSignIn refusal = %v, want the unavailable cause", err)
	}
	if err := f.SignOutChatGPT(); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("SignOutChatGPT refusal = %v, want the unavailable cause", err)
	}
}

// TestChatGPTRPCsDuringRestoreWindowNameTheTransientState covers the
// startup window in which the background keychain restore is still in
// flight: the manager is nil and no construction error exists yet, so the
// refusal must say the restore is still running (try again in a moment)
// instead of claiming the subsystem was never initialized — and the RPC
// must succeed once the restore concludes.
func TestChatGPTRPCsDuringRestoreWindowNameTheTransientState(t *testing.T) {
	f, _, _ := newChatGPTAuthTestAPI(t)
	m := &fakeChatGPTManager{}

	unblock := make(chan struct{})
	enteredKeychainRead := make(chan struct{})
	f.chatgptAuth.newManagerFn = func() (chatGPTAuthManager, error) {
		// Signal entry AFTER InitChatGPTAuth has published restoring=true
		// (the flag write precedes this call in the same goroutine), so the
		// test's RPC below deterministically observes the window.
		close(enteredKeychainRead)
		<-unblock
		return m, nil
	}
	initDone := make(chan struct{})
	go func() {
		defer close(initDone)
		f.Lifecycle().InitChatGPTAuth()
	}()
	select {
	case <-enteredKeychainRead:
	case <-time.After(5 * time.Second):
		t.Fatal("InitChatGPTAuth never reached the keychain read")
	}

	// The restore is blocked inside newManagerFn: the mutating RPCs refuse
	// with the TRANSIENT wording, not the not-initialized claim.
	if _, err := f.StartChatGPTSignIn(); err == nil ||
		!strings.Contains(err.Error(), "still reading the OS keychain") {
		t.Fatalf("StartChatGPTSignIn during the restore window = %v, want the transient still-reading refusal", err)
	}
	if err := f.SignOutChatGPT(); err == nil ||
		!strings.Contains(err.Error(), "still reading the OS keychain") {
		t.Fatalf("SignOutChatGPT during the restore window = %v, want the transient still-reading refusal", err)
	}
	if _, err := f.FetchChatGPTModels(); err == nil ||
		!strings.Contains(err.Error(), "still reading the OS keychain") {
		t.Fatalf("FetchChatGPTModels during the restore window = %v, want the transient still-reading refusal", err)
	}

	close(unblock)
	select {
	case <-initDone:
	case <-time.After(5 * time.Second):
		t.Fatal("InitChatGPTAuth did not conclude after the keychain read unblocked")
	}

	// After the restore concludes the same RPC proceeds past the nil-manager
	// refusal (the fake is signed out, so the sign-in RPC returns its URL).
	if _, err := f.StartChatGPTSignIn(); err != nil {
		t.Fatalf("StartChatGPTSignIn after the restore concluded: %v", err)
	}
}

// TestGetChatGPTAuthStatus_ReportsConfiguredMode covers the mode field's
// both values, read from the live config.
func TestGetChatGPTAuthStatus_ReportsConfiguredMode(t *testing.T) {
	f, _, _ := newChatGPTAuthTestAPI(t)
	m := &fakeChatGPTManager{}
	installChatGPTManager(t, f, m)

	if got := f.GetChatGPTAuthStatus().Mode; got != config.ChatGPTAuthModeAPIKey {
		t.Fatalf("default mode = %q, want api_key", got)
	}

	f.configMu.Lock()
	f.config.LLM.ChatGPT.Auth.Mode = config.ChatGPTAuthModeOAuth
	f.configMu.Unlock()
	if got := f.GetChatGPTAuthStatus().Mode; got != config.ChatGPTAuthModeOAuth {
		t.Fatalf("oauth mode = %q, want oauth", got)
	}
}

// --- SignOutChatGPT ---

// TestSignOutChatGPT_WithdrawsSeamAndIsIdempotent covers the sign-out tail:
// the manager clears the credentials, the seam is withdrawn to its zero
// value, the router is rebuilt, and a second sign-out succeeds.
func TestSignOutChatGPT_WithdrawsSeamAndIsIdempotent(t *testing.T) {
	f, mock, rec := newChatGPTAuthTestAPI(t)
	m := &fakeChatGPTManager{}
	installChatGPTManager(t, f, m)

	if _, err := f.StartChatGPTSignIn(); err != nil {
		t.Fatalf("StartChatGPTSignIn: %v", err)
	}
	// Wait for the success tail so the sign-out below observes the signed-in
	// seam rather than racing the background run.
	rec.waitForState(t, chatgptAuthEventSuccess)

	if err := f.SignOutChatGPT(); err != nil {
		t.Fatalf("SignOutChatGPT: %v", err)
	}
	m.mu.Lock()
	calls := m.signOutCalls
	m.mu.Unlock()
	if calls != 1 {
		t.Fatalf("manager SignOut calls = %d, want 1", calls)
	}
	seam, sets := mock.lastSubscriptionSeam()
	if sets == 0 {
		t.Fatal("the zero seam was never pushed after sign-out")
	}
	if seam != (core.BuilderSubscriptionAuthConfig{}) {
		t.Fatalf("seam after sign-out = %+v, want the zero value", seam)
	}
	if st := f.GetChatGPTAuthStatus(); st.SignedIn {
		t.Fatal("status reports signed in after sign-out")
	}

	// Idempotent.
	if err := f.SignOutChatGPT(); err != nil {
		t.Fatalf("second SignOutChatGPT: %v", err)
	}
}

// TestSignOutChatGPT_CancelsAndJoinsAnInFlightSignIn pins the ordering
// guarantee: an in-flight sign-in is cancelled and JOINED before the
// credentials are cleared, so a completing flow can never re-persist them
// after the sign-out (the account cannot silently sign back in).
func TestSignOutChatGPT_CancelsAndJoinsAnInFlightSignIn(t *testing.T) {
	f, _, rec := newChatGPTAuthTestAPI(t)
	events := make(chan string, 4)
	m := &fakeChatGPTManager{signInHook: func(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error) {
		_ = opener(defaultSignInURL)
		<-ctx.Done()
		events <- "flow-returned"
		return nil, ctx.Err()
	}}
	m.signOutErr = nil
	installChatGPTManager(t, f, m)

	if _, err := f.StartChatGPTSignIn(); err != nil {
		t.Fatalf("StartChatGPTSignIn: %v", err)
	}
	if err := f.SignOutChatGPT(); err != nil {
		t.Fatalf("SignOutChatGPT during an in-flight run: %v", err)
	}
	rec.waitForState(t, chatgptAuthEventCancelled)

	// The flow must have concluded BEFORE the credential clear ran.
	select {
	case first := <-events:
		if first != "flow-returned" {
			t.Fatalf("first recorded event = %q, want flow-returned", first)
		}
	default:
		t.Fatal("SignOutChatGPT returned before the cancelled flow concluded (join missing)")
	}
	m.mu.Lock()
	calls := m.signOutCalls
	signedIn := m.signedIn
	m.mu.Unlock()
	if calls != 1 {
		t.Fatalf("manager SignOut calls = %d, want 1", calls)
	}
	if signedIn {
		t.Fatal("the account signed back in after the sign-out")
	}
}

// --- InitChatGPTAuth restore ---

// TestInitChatGPTAuth_RestoreMirrorsSeamRebuildsOnlyInOAuth pins the startup
// restore matrix: the seam is mirrored for EVERY restored state (it is
// inert in api_key mode — only SubscriptionAuth-marked entries consume it —
// and guarantees a later mode switch to oauth picks the credentials up),
// while the router REBUILD happens only when an account was restored AND
// the config selects oauth mode.
func TestInitChatGPTAuth_RestoreMirrorsSeamRebuildsOnlyInOAuth(t *testing.T) {
	t.Run("signed in + oauth restores the seam", func(t *testing.T) {
		f, mock, _ := newChatGPTAuthTestAPI(t)
		f.configMu.Lock()
		f.config.LLM.ChatGPT.Auth.Mode = config.ChatGPTAuthModeOAuth
		f.configMu.Unlock()
		m := &fakeChatGPTManager{signedIn: true,
			identity:  &providerauth.Identity{Email: "user@example.com", ChatGPTAccountID: "acct-123"},
			expiresAt: time.Now().Add(time.Hour).UTC()}
		installChatGPTManager(t, f, m)

		seam, sets := mock.lastSubscriptionSeam()
		if sets == 0 {
			t.Fatal("the restore never pushed the subscription seam")
		}
		if seam.ProviderName != chatGPTProviderName || seam.TokenSource == nil {
			t.Fatalf("restored seam = %+v, want the live chatgpt source", seam)
		}
		if got := mock.rebuildRouterCallsSnapshot(); got == 0 {
			t.Fatal("the router was not rebuilt on the oauth restore")
		}
	})

	t.Run("signed in + api_key mode mirrors the inert seam but rebuilds nothing", func(t *testing.T) {
		f, mock, _ := newChatGPTAuthTestAPI(t)
		m := &fakeChatGPTManager{signedIn: true}
		installChatGPTManager(t, f, m)

		// The seam IS mirrored (mode-independent): a later switch to oauth
		// through UpdateLLMConfig rebuilds the router and must find the live
		// source installed — otherwise the switch leaves the account
		// signed-in on the surface but every request failing.
		seam, sets := mock.lastSubscriptionSeam()
		if sets == 0 {
			t.Fatal("the restore never pushed the subscription seam")
		}
		if seam.ProviderName != chatGPTProviderName || seam.TokenSource == nil {
			t.Fatalf("restored seam = %+v, want the live chatgpt source (inert until the mode flips)", seam)
		}
		if got := mock.rebuildRouterCallsSnapshot(); got != 0 {
			t.Fatal("the router was rebuilt although the config keeps api_key mode")
		}
	})

	t.Run("signed out + oauth mirrors the empty seam and rebuilds nothing", func(t *testing.T) {
		f, mock, _ := newChatGPTAuthTestAPI(t)
		f.configMu.Lock()
		f.config.LLM.ChatGPT.Auth.Mode = config.ChatGPTAuthModeOAuth
		f.configMu.Unlock()
		m := &fakeChatGPTManager{}
		installChatGPTManager(t, f, m)

		// The zero-value seam is mirrored too (the signed-out stand-in);
		// no live source is installed and no rebuild happens.
		seam, sets := mock.lastSubscriptionSeam()
		if sets == 0 {
			t.Fatal("the restore never pushed the subscription seam")
		}
		if seam.TokenSource != nil {
			t.Fatalf("restored seam = %+v, want no live source while signed out", seam)
		}
		if got := mock.rebuildRouterCallsSnapshot(); got != 0 {
			t.Fatal("the router was rebuilt although no account was restored")
		}
	})
}

// --- GetChatGPTModelPreset ---

// TestGetChatGPTModelPreset_PinsNamesAndOrder pins the preset itself: the
// five Codex models, most capable first, exactly the intersection of sp4rk's
// registry and the subscription allowed-list.
func TestGetChatGPTModelPreset_PinsNamesAndOrder(t *testing.T) {
	want := []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex", "codex-mini-latest"}

	f, mock, _ := newChatGPTAuthTestAPI(t)
	// With no registry wired the entries carry names only (fail-soft).
	resp := f.GetChatGPTModelPreset()
	if len(resp.Models) != len(want) {
		t.Fatalf("preset length = %d, want %d", len(resp.Models), len(want))
	}
	for i, entry := range resp.Models {
		if entry.Name != want[i] {
			t.Fatalf("preset[%d] = %q, want %q", i, entry.Name, want[i])
		}
	}

	// With the real registry every name must resolve (a name the registry
	// does not know would break the router's overflow arithmetic) and carry
	// non-zero windows.
	mock.registry = llm.NewModelRegistry(nil)
	reg := llm.NewModelRegistry(nil)
	for _, name := range want {
		meta, ok := reg.ResolveLocal(name)
		if !ok {
			t.Errorf("preset model %q does not resolve from the sp4rk registry", name)
		}
		if meta.ContextWindow <= 0 {
			t.Errorf("preset model %q resolves with context window %d", name, meta.ContextWindow)
		}
	}
	enriched := f.GetChatGPTModelPreset()
	for _, entry := range enriched.Models {
		if entry.ContextWindow <= 0 {
			t.Errorf("preset entry %q carries no context window with the registry wired", entry.Name)
		}
	}
}

// --- Auth-mode round-trip (GetConfig / UpdateLLMConfig) ---

// TestChatGPTAuthMode_RoundTrip covers the config surface: GetConfig reports
// the effective mode (empty stored value normalized to api_key), nil keeps
// the persisted mode, a valid value applies and persists, and anything else
// is rejected without touching state.
func TestChatGPTAuthMode_RoundTrip(t *testing.T) {
	f, _, _ := newChatGPTAuthTestAPI(t)

	// Default (empty stored mode) reads as api_key on the wire.
	if got := f.GetConfig().LLM.ChatGPT.AuthMode; got != config.ChatGPTAuthModeAPIKey {
		t.Fatalf("default auth_mode = %q, want api_key", got)
	}

	// nil keeps the persisted mode (debounced partial saves).
	apiKey := config.ChatGPTAuthModeAPIKey
	if err := f.UpdateLLMConfig(LLMFullConfigRequest{ChatGPT: &ProviderConfigRequest{AuthMode: nil}}); err != nil {
		t.Fatalf("nil auth_mode update: %v", err)
	}
	if got := f.GetConfig().LLM.ChatGPT.AuthMode; got != config.ChatGPTAuthModeAPIKey {
		t.Fatalf("after nil update auth_mode = %q, want unchanged api_key", got)
	}

	// oauth applies, persists and round-trips; no secret appears anywhere.
	oauth := config.ChatGPTAuthModeOAuth
	if err := f.UpdateLLMConfig(LLMFullConfigRequest{ChatGPT: &ProviderConfigRequest{AuthMode: &oauth}}); err != nil {
		t.Fatalf("oauth update: %v", err)
	}
	if got := f.GetConfig().LLM.ChatGPT.AuthMode; got != config.ChatGPTAuthModeOAuth {
		t.Fatalf("auth_mode after oauth update = %q, want oauth", got)
	}
	persisted, err := config.Load(f.configPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if persisted.LLM.ChatGPT.Auth.Mode != config.ChatGPTAuthModeOAuth {
		t.Fatalf("persisted mode = %q, want oauth", persisted.LLM.ChatGPT.Auth.Mode)
	}

	// Back to api_key explicitly.
	if err := f.UpdateLLMConfig(LLMFullConfigRequest{ChatGPT: &ProviderConfigRequest{AuthMode: &apiKey}}); err != nil {
		t.Fatalf("api_key update: %v", err)
	}
	if got := f.GetConfig().LLM.ChatGPT.AuthMode; got != config.ChatGPTAuthModeAPIKey {
		t.Fatalf("auth_mode after api_key update = %q, want api_key", got)
	}

	// Invalid values are rejected without touching state — including the
	// empty string and a case variant, so a typo can never silently keep
	// key auth while the operator believes subscription auth is on.
	for _, bad := range []string{"", "OAuth", "subscription"} {
		mode := bad
		before := f.GetConfig().LLM.ChatGPT.AuthMode
		err := f.UpdateLLMConfig(LLMFullConfigRequest{ChatGPT: &ProviderConfigRequest{AuthMode: &mode}})
		if err == nil {
			t.Fatalf("auth_mode %q was accepted", bad)
		}
		if !strings.Contains(err.Error(), "auth_mode") {
			t.Fatalf("rejection for %q = %v, want it to name auth_mode", bad, err)
		}
		if after := f.GetConfig().LLM.ChatGPT.AuthMode; after != before {
			t.Fatalf("a rejected update changed auth_mode from %q to %q", before, after)
		}
	}
}

// TestChatGPTAuth_RpcSignatures pins the binding-generator-visible shapes:
// read-only getters return no error (the desktop-frontend "RPC Surface"
// convention), mutators return exactly error, and the sign-in returns its
// response struct — mirroring the embedded surface's signature contract.
func TestChatGPTAuth_RpcSignatures(t *testing.T) {
	errType := reflect.TypeOf((*error)(nil)).Elem()
	respType := reflect.TypeOf((*ChatGPTSignInResponse)(nil))
	statusType := reflect.TypeOf(ChatGPTAuthStatusResponse{})
	presetType := reflect.TypeOf(ChatGPTModelPresetResponse{})

	cases := []struct {
		name string
		in   []reflect.Type
		out  []reflect.Type
	}{
		{name: "StartChatGPTSignIn", out: []reflect.Type{respType, errType}},
		{name: "CancelChatGPTSignIn", out: []reflect.Type{errType}},
		{name: "GetChatGPTAuthStatus", out: []reflect.Type{statusType}},
		{name: "SignOutChatGPT", out: []reflect.Type{errType}},
		{name: "GetChatGPTModelPreset", out: []reflect.Type{presetType}},
		{name: "FetchChatGPTModels", out: []reflect.Type{presetType, errType}},
	}
	api := reflect.TypeOf(&FrontendAPI{})
	for _, tc := range cases {
		m, ok := api.MethodByName(tc.name)
		if !ok {
			t.Errorf("%s is not exported on *FrontendAPI", tc.name)
			continue
		}
		got := m.Type
		if n := got.NumIn() - 1; n != len(tc.in) {
			t.Errorf("%s takes %d argument(s), want %d", tc.name, n, len(tc.in))
		}
		if got.NumOut() != len(tc.out) {
			t.Errorf("%s returns %d value(s), want %d", tc.name, got.NumOut(), len(tc.out))
			continue
		}
		for i, want := range tc.out {
			if got.Out(i) != want {
				t.Errorf("%s return %d is %s, want %s", tc.name, i, got.Out(i), want)
			}
		}
	}
}

// --- FetchChatGPTModels ---

// recordedCatalogRequest captures what one live catalog request carried.
type recordedCatalogRequest struct {
	method    string
	pathQuery string
	auth      string
	accountID string
}

// newChatGPTCatalogServer serves the given status/body at the catalog
// endpoint and records the request. The recorder is read only after the
// fetch returns, so no synchronization is needed.
func newChatGPTCatalogServer(t *testing.T, status int, body string) (*httptest.Server, *recordedCatalogRequest) {
	t.Helper()
	rec := &recordedCatalogRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method = r.Method
		rec.pathQuery = r.URL.RequestURI()
		rec.auth = r.Header.Get("Authorization")
		rec.accountID = r.Header.Get("ChatGPT-Account-Id")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// signedInChatGPTAPI returns an API whose fake manager is signed in with a
// known identity, the pattern every FetchChatGPTModels test starts from.
func signedInChatGPTAPI(t *testing.T) (*FrontendAPI, *fakeChatGPTManager, *mockBuilder) {
	t.Helper()
	f, mock, _ := newChatGPTAuthTestAPI(t)
	m := &fakeChatGPTManager{}
	installChatGPTManager(t, f, m)
	m.mu.Lock()
	m.signedIn = true
	m.identity = &providerauth.Identity{Email: "user@example.com", ChatGPTAccountID: "acct-123"}
	m.mu.Unlock()
	return f, m, mock
}

// TestFetchChatGPTModels_LiveCatalog pins the picker contract against the
// backend's own catalog: only visibility=="list" entries survive, the order
// is the backend's ascending priority rank, registry-known slugs carry full
// catalog metadata, unknown slugs fall back to the endpoint's own context
// window, and the request authenticates exactly like a chat request
// (bearer + account id) with the client_version parameter present.
func TestFetchChatGPTModels_LiveCatalog(t *testing.T) {
	catalog := `{"models":[
		{"slug":"c0wrk-registry-unknown-fixture","visibility":"list","priority":3,"context_window":272000},
		{"slug":"gpt-5.5","visibility":"list","priority":1},
		{"slug":"gpt-5.4","visibility":"list","priority":2,"max_context_window":400000},
		{"slug":"gpt-5.5-pro","visibility":"hide","priority":4},
		{"slug":"internal-guardian","visibility":"none","priority":5}
	]}`
	srv, rec := newChatGPTCatalogServer(t, http.StatusOK, catalog)
	f, m, mock := signedInChatGPTAPI(t)
	m.tokenExtraHeaders = map[string]string{"ChatGPT-Account-Id": "acct-123"}
	mock.registry = llm.NewModelRegistry(nil)
	f.chatgptAuth.modelsBaseURLOverride = srv.URL

	resp, err := f.FetchChatGPTModels()
	if err != nil {
		t.Fatalf("FetchChatGPTModels: %v", err)
	}

	// Picker order: priority rank ascending, hide/none entries dropped.
	wantOrder := []string{"gpt-5.5", "gpt-5.4", "c0wrk-registry-unknown-fixture"}
	if len(resp.Models) != len(wantOrder) {
		t.Fatalf("models = %v, want exactly %v", resp.Models, wantOrder)
	}
	for i, want := range wantOrder {
		if resp.Models[i].Name != want {
			t.Fatalf("models[%d] = %q, want %q (full order: %v)", i, resp.Models[i].Name, want, resp.Models)
		}
	}

	// Registry-known slugs carry the registry's metadata — including
	// precedence over the endpoint's own (larger) window for gpt-5.4.
	reg := mock.registry
	for _, name := range []string{"gpt-5.5", "gpt-5.4"} {
		meta, ok := reg.ResolveLocal(name)
		if !ok {
			t.Fatalf("fixture slug %q unknown to the sp4rk registry — pick a known one", name)
		}
		entry := resp.Models[indexOfString(t, resp.Models, name)]
		if entry.ContextWindow != meta.ContextWindow {
			t.Errorf("%s context window = %d, want the registry's %d", name, entry.ContextWindow, meta.ContextWindow)
		}
		if entry.OutputLimit != meta.OutputLimit {
			t.Errorf("%s output limit = %d, want the registry's %d", name, entry.OutputLimit, meta.OutputLimit)
		}
	}

	// A slug the registry cannot know keeps the endpoint's own window and
	// zero output metadata — usable, just unenriched. The slug is
	// deliberately synthetic (it names the fixture's purpose, not a real
	// model) so a future registry entry can never make it known and silently
	// flip this branch into the registry-first one.
	unknownSlug := "c0wrk-registry-unknown-fixture"
	if _, known := mock.registry.ResolveLocal(unknownSlug); known {
		t.Fatalf("fixture slug %q is already known to the sp4rk registry — pick a fresh synthetic one", unknownSlug)
	}
	luna := resp.Models[indexOfString(t, resp.Models, unknownSlug)]
	if luna.ContextWindow != 272000 {
		t.Errorf("%s context window = %d, want the endpoint's 272000", unknownSlug, luna.ContextWindow)
	}
	if luna.OutputLimit != 0 {
		t.Errorf("%s output limit = %d, want 0 (registry fallback posture)", unknownSlug, luna.OutputLimit)
	}

	// The request authenticates like a chat request and names its client.
	if rec.method != http.MethodGet {
		t.Errorf("catalog method = %q, want GET", rec.method)
	}
	if rec.pathQuery != chatGPTModelsPath+"?client_version="+chatGPTCatalogClientVersion {
		t.Errorf("catalog request URI = %q, want %q", rec.pathQuery, chatGPTModelsPath+"?client_version="+chatGPTCatalogClientVersion)
	}
	if rec.auth != "Bearer fake-access" {
		t.Errorf("catalog Authorization = %q, want the manager's bearer", rec.auth)
	}
	if rec.accountID != "acct-123" {
		t.Errorf("catalog ChatGPT-Account-Id = %q, want acct-123 (ExtraHeaders pass-through)", rec.accountID)
	}
}

// TestFetchChatGPTModels_RidesConfiguredProxy pins the wire-level transit:
// with the proxy enabled, the catalog request leaves through the configured
// proxy (an absolute-form request URI naming the catalog origin, carrying
// the same authentication) instead of a direct default-route connection —
// the parity the OAuth exchange relies on in a proxy-only network. The
// default bypass list (localhost, 127.0.0.1) is cleared so the loopback
// test servers do not accidentally bypass the proxy under test.
func TestFetchChatGPTModels_RidesConfiguredProxy(t *testing.T) {
	// The catalog origin: if the request bypassed the proxy and dialed
	// directly, this recorder stays empty and the test fails.
	origin, originRec := newChatGPTCatalogServer(t, http.StatusOK, `{"models":[]}`)

	var mu sync.Mutex
	seenURI, seenAuth := "", ""
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenURI, seenAuth = r.RequestURI, r.Header.Get("Authorization")
		mu.Unlock()
		// Answer from the proxy: the transit (not the origin) is what this
		// test pins.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	t.Cleanup(proxySrv.Close)

	f, _, _ := signedInChatGPTAPI(t)
	f.configMu.Lock()
	f.config.Proxy.Enabled = true
	f.config.Proxy.URL = proxySrv.URL
	f.config.Proxy.BypassList = []string{}
	f.configMu.Unlock()
	f.chatgptAuth.modelsBaseURLOverride = origin.URL

	if _, err := f.FetchChatGPTModels(); err != nil {
		t.Fatalf("FetchChatGPTModels through the proxy: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	wantPrefix := origin.URL + chatGPTModelsPath
	if !strings.HasPrefix(seenURI, wantPrefix) {
		t.Fatalf("proxy saw request URI %q, want the absolute-form %q… (the request did not transit the proxy)", seenURI, wantPrefix)
	}
	if seenAuth != "Bearer fake-access" {
		t.Fatalf("proxy saw Authorization %q, want the manager's bearer", seenAuth)
	}
	if originRec.method != "" {
		t.Fatalf("the catalog origin saw a direct %s request — the fetch must ride the proxy, not the default route", originRec.method)
	}
}

// TestInitChatGPTAuth_RestorePicksUpProxyChangeFromTheWindow pins the
// restore-window proxy gap: the manager's construction client is captured
// BEFORE the (unbounded) keychain read, so a proxy change landing while that
// read is still in flight cannot reach it through the construction capture —
// and UpdateProxySettings' push would skip it too (the manager is still
// nil). The post-publication push must therefore re-point the manager at
// the CURRENT configuration, provable at the wire level: a request through
// the pushed client transits the proxy the config selected mid-restore.
func TestInitChatGPTAuth_RestorePicksUpProxyChangeFromTheWindow(t *testing.T) {
	var mu sync.Mutex
	proxyHit := ""
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proxyHit = r.RequestURI
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(proxySrv.Close)

	m := &fakeChatGPTManager{}
	enteredKeychainRead := make(chan struct{})
	unblock := make(chan struct{})
	f, _, _ := newChatGPTAuthTestAPI(t)
	f.chatgptAuth.newManagerFn = func() (chatGPTAuthManager, error) {
		close(enteredKeychainRead)
		<-unblock
		return m, nil
	}
	initDone := make(chan struct{})
	go func() {
		defer close(initDone)
		f.Lifecycle().InitChatGPTAuth()
	}()
	select {
	case <-enteredKeychainRead:
	case <-time.After(5 * time.Second):
		t.Fatal("InitChatGPTAuth never reached the keychain read")
	}

	// The proxy changes while the keychain read is still in flight.
	f.configMu.Lock()
	f.config.Proxy.Enabled = true
	f.config.Proxy.URL = proxySrv.URL
	f.config.Proxy.BypassList = []string{}
	f.configMu.Unlock()

	close(unblock)
	select {
	case <-initDone:
	case <-time.After(5 * time.Second):
		t.Fatal("InitChatGPTAuth did not conclude after the keychain read unblocked")
	}

	m.mu.Lock()
	client, pushes := m.lastPushedClient, m.pushedClients
	m.mu.Unlock()
	if client == nil || pushes == 0 {
		t.Fatal("the published manager never received a SetHTTPClient push")
	}

	// One request through the pushed client must transit the NEW proxy
	// (absolute-form URI naming the origin; the proxy answers, so the
	// origin host never needs to resolve).
	origin := "http://chatgpt-catalog-origin.test/v1/models"
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin, http.NoBody)
	if err != nil {
		t.Fatalf("building the probe request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request through the pushed client: %v", err)
	}
	_ = resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if proxyHit != origin {
		t.Fatalf("proxy saw %q, want the origin %q (the pushed client must ride the proxy the config selected mid-restore)", proxyHit, origin)
	}
}

// TestFetchChatGPTModels_ClientVersionPin guards the root cause of the
// empty-checklist bug: the backend gates catalog entries per model by
// minimal_client_version and answers a low client_version with a 200 and an
// EMPTY catalog (verified live: "0.0.1" yields exactly {"models":[]}). The
// request must therefore carry the pinned Codex CLI release, never a dev
// fallback like "0.0.1" and never c0wrk's own (smaller) build version.
func TestFetchChatGPTModels_ClientVersionPin(t *testing.T) {
	// The pin is a real semver release of the Codex CLI, not a placeholder.
	for _, bad := range []string{"", "0.0.1", "dev", "0.0.0"} {
		if chatGPTCatalogClientVersion == bad {
			t.Fatalf("client_version pin = %q — a dev placeholder blanks the catalog", bad)
		}
	}
	var major, minor, patch int
	if _, err := fmt.Sscanf(chatGPTCatalogClientVersion, "%d.%d.%d", &major, &minor, &patch); err != nil {
		t.Fatalf("client_version pin %q is not a semantic version: %v", chatGPTCatalogClientVersion, err)
	}
	if major == 0 && minor == 0 {
		t.Fatalf("client_version pin %q is a 0.0.x placeholder — the backend answers it with an empty catalog", chatGPTCatalogClientVersion)
	}

	// And the wire request carries exactly the pin.
	srv, rec := newChatGPTCatalogServer(t, http.StatusOK, `{"models":[]}`)
	f, _, _ := signedInChatGPTAPI(t)
	f.chatgptAuth.modelsBaseURLOverride = srv.URL
	if _, err := f.FetchChatGPTModels(); err != nil {
		t.Fatalf("FetchChatGPTModels: %v", err)
	}
	if want := chatGPTModelsPath + "?client_version=" + chatGPTCatalogClientVersion; rec.pathQuery != want {
		t.Fatalf("catalog request URI = %q, want %q", rec.pathQuery, want)
	}
}

// TestFetchChatGPTModels_EmptyCatalogPassesThrough pins the gating
// signature: a 200 with an empty list is a VALID answer (a client_version
// below every model's minimum, or an account with no listable models), so
// the RPC returns an empty list without inventing an error — the frontend
// keeps its offline preset in that case instead of blanking the checklist.
func TestFetchChatGPTModels_EmptyCatalogPassesThrough(t *testing.T) {
	srv, _ := newChatGPTCatalogServer(t, http.StatusOK, `{"models":[]}`)
	f, _, _ := signedInChatGPTAPI(t)
	f.chatgptAuth.modelsBaseURLOverride = srv.URL
	resp, err := f.FetchChatGPTModels()
	if err != nil {
		t.Fatalf("FetchChatGPTModels: %v (an empty catalog is a valid answer)", err)
	}
	if len(resp.Models) != 0 {
		t.Fatalf("models = %v, want the empty list passed through", resp.Models)
	}
}

// TestFetchChatGPTModels_Refusals pins the actionable refusals: the
// subsystem being unavailable and not being signed in each name the
// remediation instead of dialing anything.
func TestFetchChatGPTModels_Refusals(t *testing.T) {
	t.Run("no manager", func(t *testing.T) {
		f, _, _ := newChatGPTAuthTestAPI(t)
		f.chatgptAuth.initError = "keychain unavailable"
		_, err := f.FetchChatGPTModels()
		if err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("err = %v, want the unavailable refusal", err)
		}
	})
	t.Run("signed out", func(t *testing.T) {
		f, _, _ := newChatGPTAuthTestAPI(t)
		installChatGPTManager(t, f, &fakeChatGPTManager{})
		_, err := f.FetchChatGPTModels()
		if err == nil || !strings.Contains(err.Error(), "sign in") {
			t.Fatalf("err = %v, want the sign-in refusal", err)
		}
	})
}

// TestFetchChatGPTModels_BackendFailures pins the failure surface: a non-200
// answer names the status and a bounded body snippet, and a 200 that is not
// the catalog shape is a decoding error — both actionable, neither silent.
func TestFetchChatGPTModels_BackendFailures(t *testing.T) {
	t.Run("unauthorized", func(t *testing.T) {
		srv, _ := newChatGPTCatalogServer(t, http.StatusUnauthorized, `{"error":{"message":"token expired"}}`)
		f, _, _ := signedInChatGPTAPI(t)
		f.chatgptAuth.modelsBaseURLOverride = srv.URL
		_, err := f.FetchChatGPTModels()
		if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "token expired") {
			t.Fatalf("err = %v, want it to name the 401 and the body snippet", err)
		}
	})
	t.Run("bad payload", func(t *testing.T) {
		srv, _ := newChatGPTCatalogServer(t, http.StatusOK, `<html>login page</html>`)
		f, _, _ := signedInChatGPTAPI(t)
		f.chatgptAuth.modelsBaseURLOverride = srv.URL
		_, err := f.FetchChatGPTModels()
		if err == nil || !strings.Contains(err.Error(), "decoding the catalog") {
			t.Fatalf("err = %v, want the decoding refusal", err)
		}
	})
}

// indexOfString returns the index of name in the entry slice (test helper).
func indexOfString(t *testing.T, models []ChatGPTModelPresetEntry, name string) int {
	t.Helper()
	for i, m := range models {
		if m.Name == name {
			return i
		}
	}
	t.Fatalf("model %q not found in %v", name, models)
	return -1
}

// TestStartChatGPTSignIn_SuccessWithoutIdentity pins the nil-safe success
// path: an issuer response that issued tokens but no ID token yields a
// successful sign-in with a nil identity (providerauth treats the identity
// as optional), and the success event must carry empty identity fields —
// not panic in the background goroutine (which would take the whole desktop
// process down).
func TestStartChatGPTSignIn_SuccessWithoutIdentity(t *testing.T) {
	f, _, rec := newChatGPTAuthTestAPI(t)
	m := &fakeChatGPTManager{}
	expiry := time.Now().Add(time.Hour).UTC()
	m.signInHook = func(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error) {
		_ = opener(defaultSignInURL)
		// Mirror the manager state transition the real flow performs: signed
		// in, but with a nil identity — exactly the no-ID-token response.
		m.mu.Lock()
		m.signedIn = true
		m.identity = nil
		m.expiresAt = expiry
		m.mu.Unlock()
		return &providerauth.SignInResult{
			Tokens:   &providerauth.Tokens{AccessToken: "at", RefreshToken: "rt", TokenType: "Bearer", Expiry: expiry},
			Identity: nil,
		}, nil
	}
	installChatGPTManager(t, f, m)

	if _, err := f.StartChatGPTSignIn(); err != nil {
		t.Fatalf("StartChatGPTSignIn: %v", err)
	}
	success := rec.waitForState(t, chatgptAuthEventSuccess)
	if success.Email != "" || success.AccountID != "" {
		t.Errorf("success event identity = (%q, %q), want empty fields for a nil identity", success.Email, success.AccountID)
	}
	// The status reflects the signed-in state with empty identity fields
	// (chatGPTIdentityField is nil-safe there too).
	st := f.GetChatGPTAuthStatus()
	if !st.SignedIn {
		t.Fatal("status reports signed out after a nil-identity sign-in")
	}
	if st.Email != "" || st.AccountID != "" {
		t.Errorf("status identity = (%q, %q), want empty fields for a nil identity", st.Email, st.AccountID)
	}
}

// TestChatGPTProxyConfig_ExpandsEnvVarsInURLAndTLSCertDir pins the parity
// with the builder's own normalization (backend/configadapter.go): BOTH the
// proxy URL and the TLS CA directory are ${VAR}-expanded on the auth/catalog
// outbound path, so a proxy-only network whose CA directory is configured
// through an env var does not hand the OAuth exchange or the model-catalog
// fetch a literal "${VAR}" path and fail the TLS handshake that LLM traffic
// survives.
func TestChatGPTProxyConfig_ExpandsEnvVarsInURLAndTLSCertDir(t *testing.T) {
	f, _, _ := newChatGPTAuthTestAPI(t)
	f.configMu.Lock()
	f.config.Proxy.URL = "${C0WRK_TEST_PROXY_URL}"
	f.config.Proxy.TLSCertDir = "${C0WRK_TEST_CA_DIR}/certs"
	f.config.Proxy.BypassList = []string{"localhost"}
	f.configMu.Unlock()

	t.Setenv("C0WRK_TEST_PROXY_URL", "http://127.0.0.1:3128")
	t.Setenv("C0WRK_TEST_CA_DIR", "/tmp/test-ca")

	got := f.chatGPTProxyConfig()
	if got.URL != "http://127.0.0.1:3128" {
		t.Errorf("proxy URL = %q, want the expanded http://127.0.0.1:3128", got.URL)
	}
	if got.TLSCertDir != "/tmp/test-ca/certs" {
		t.Errorf("TLS cert dir = %q, want the expanded /tmp/test-ca/certs", got.TLSCertDir)
	}
	if len(got.BypassList) != 1 || got.BypassList[0] != "localhost" {
		t.Errorf("bypass list = %v, want it carried verbatim", got.BypassList)
	}
}

// TestInitChatGPTAuth_RestoreEmitsSuccessCorrection pins the cold-start
// correction: the restore may complete after the frontend has already
// rendered the pre-restore zero state (a Settings panel keeps its
// mount-time snapshot and nothing else refreshes it), so a SIGNED-IN
// restore must publish the same success transition an interactive sign-in
// emits. A signed-out restore publishes nothing — the pre-restore zero
// state it would "correct" is already the truth.
func TestInitChatGPTAuth_RestoreEmitsSuccessCorrection(t *testing.T) {
	f, _, rec := newChatGPTAuthTestAPI(t)
	expiry := time.Now().Add(time.Hour).UTC()
	m := &fakeChatGPTManager{
		signedIn:  true,
		identity:  &providerauth.Identity{Email: "restored@example.com", ChatGPTAccountID: "acct-restore"},
		expiresAt: expiry,
	}
	installChatGPTManager(t, f, m)

	success := rec.waitForState(t, chatgptAuthEventSuccess)
	if success.Email != "restored@example.com" || success.AccountID != "acct-restore" {
		t.Errorf("restore success event identity = (%q, %q), want the restored account", success.Email, success.AccountID)
	}
	if success.ExpiresAt != expiry.Format(time.RFC3339) {
		t.Errorf("restore success event expires_at = %q, want %q", success.ExpiresAt, expiry.Format(time.RFC3339))
	}

	// A signed-out restore emits no state event.
	f2, _, rec2 := newChatGPTAuthTestAPI(t)
	installChatGPTManager(t, f2, &fakeChatGPTManager{})
	if states := rec2.states(); len(states) != 0 {
		t.Errorf("signed-out restore emitted %v, want no state events", states)
	}
}

// TestInitChatGPTAuth_RestoreSkipsCorrectionDuringInFlightSignIn pins the
// mid-flow guard: a restore that concludes while an interactive sign-in is
// already running must NOT emit its success correction — the flow's pending
// event has the panel in the busy posture, a restore-success would clear it
// prematurely, and the flow's own terminal event re-reads the authoritative
// snapshot anyway.
func TestInitChatGPTAuth_RestoreSkipsCorrectionDuringInFlightSignIn(t *testing.T) {
	f, _, rec := newChatGPTAuthTestAPI(t)

	// Block the interactive flow AFTER its pending event, so the restore
	// runs while inFlight is true.
	flowReleased := make(chan struct{})
	signInStarted := make(chan struct{})
	m := &fakeChatGPTManager{
		signInHook: func(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error) {
			if err := opener(defaultSignInURL); err != nil {
				return nil, err
			}
			close(signInStarted)
			<-flowReleased
			expiry := time.Now().Add(time.Hour).UTC()
			return &providerauth.SignInResult{
				Tokens:   &providerauth.Tokens{AccessToken: "at", RefreshToken: "rt", TokenType: "Bearer", Expiry: expiry},
				Identity: &providerauth.Identity{Email: "new@example.com", ChatGPTAccountID: "acct-new"},
			}, nil
		},
	}
	installChatGPTManager(t, f, m)

	if _, err := f.StartChatGPTSignIn(); err != nil {
		t.Fatalf("StartChatGPTSignIn: %v", err)
	}
	<-signInStarted
	rec.waitForState(t, chatgptAuthEventPending)

	// The restore concludes mid-flow: signed in, but NO success correction
	// may be emitted while the flow is in flight. The restore runs through
	// the REAL InitChatGPTAuth path with newManagerFn re-pointed at the
	// signed-in fake — a direct manager-field assignment would be OVERWRITTEN
	// by newManagerFn's return value before the emit decision runs, leaving
	// the signed-out fake published and the whole emit block trivially
	// skipped (the guard never even consulted).
	restored := &fakeChatGPTManager{
		signedIn: true,
		identity: &providerauth.Identity{Email: "restored@example.com", ChatGPTAccountID: "acct-restore"},
		expiresAt: func() time.Time {
			return time.Now().Add(2 * time.Hour).UTC()
		}(),
	}
	f.chatgptAuth.mu.Lock()
	f.chatgptAuth.newManagerFn = func() (chatGPTAuthManager, error) { return restored, nil }
	f.chatgptAuth.mu.Unlock()
	f.Lifecycle().InitChatGPTAuth()

	// Give the (synchronous) restore a moment; only the flow's own events
	// may exist. pending is expected; a restore-success is not.
	time.Sleep(50 * time.Millisecond)
	for _, s := range rec.states() {
		if s == chatgptAuthEventSuccess {
			t.Fatalf("restore emitted a success correction while a sign-in is in flight; states so far: %v", rec.states())
		}
	}

	// Release the flow; its own terminal event arrives normally.
	close(flowReleased)
	rec.waitForState(t, chatgptAuthEventSuccess)
}

// TestInitChatGPTAuth_PanickingKeychainReadDoesNotWedgeRestoring pins the
// panic-containment around the manager construction: a keyring backend that
// panics (native code surface) must surface as an actionable construction
// error with the restoring flag CLEARED — not as a permanent "still reading
// the OS keychain" refusal that only an app restart clears.
func TestInitChatGPTAuth_PanickingKeychainReadDoesNotWedgeRestoring(t *testing.T) {
	f, _, _ := newChatGPTAuthTestAPI(t)
	captureAPIDiagnostic(t, f, "ChatGPT subscription auth unavailable", "error", "keyring: native crash")
	f.chatgptAuth.newManagerFn = func() (chatGPTAuthManager, error) {
		panic("keyring: native crash")
	}
	f.Lifecycle().InitChatGPTAuth()

	// The flag is cleared and the panic is recorded as the construction
	// failure; the RPC refusal is the permanent unavailable one (naming the
	// panic), never the transient still-reading one.
	if _, err := f.StartChatGPTSignIn(); err == nil ||
		!strings.Contains(err.Error(), "keychain backend panicked") ||
		strings.Contains(err.Error(), "still reading the OS keychain") {
		t.Fatalf("StartChatGPTSignIn after a panicking keychain read = %v, want the unavailable refusal naming the panic (not the transient wording)", err)
	}
}
