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

	signInCalls  int
	signOutErr   error
	signOutCalls int
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
	if _, sets := mock.lastSubscriptionSeam(); sets != 0 {
		t.Fatal("the subscription seam was pushed although the run failed")
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

// TestInitChatGPTAuth_RestoreMirrorsSeamOnlyInOAuthMode pins the startup
// restore matrix: the seam + rebuild happen only when an account was
// restored AND the config selects oauth mode.
func TestInitChatGPTAuth_RestoreMirrorsSeamOnlyInOAuthMode(t *testing.T) {
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

	t.Run("signed in + api_key mode installs nothing", func(t *testing.T) {
		f, mock, _ := newChatGPTAuthTestAPI(t)
		m := &fakeChatGPTManager{signedIn: true}
		installChatGPTManager(t, f, m)

		if _, sets := mock.lastSubscriptionSeam(); sets != 0 {
			t.Fatal("the seam was pushed although the config keeps api_key mode")
		}
		if got := mock.rebuildRouterCallsSnapshot(); got != 0 {
			t.Fatal("the router was rebuilt although the config keeps api_key mode")
		}
	})

	t.Run("signed out + oauth installs nothing", func(t *testing.T) {
		f, mock, _ := newChatGPTAuthTestAPI(t)
		f.configMu.Lock()
		f.config.LLM.ChatGPT.Auth.Mode = config.ChatGPTAuthModeOAuth
		f.configMu.Unlock()
		m := &fakeChatGPTManager{}
		installChatGPTManager(t, f, m)

		if _, sets := mock.lastSubscriptionSeam(); sets != 0 {
			t.Fatal("the seam was pushed although no account was restored")
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
		{"slug":"gpt-6-luna","visibility":"list","priority":3,"context_window":272000},
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
	wantOrder := []string{"gpt-5.5", "gpt-5.4", "gpt-6-luna"}
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

	// A slug ahead of the registry keeps the endpoint's own window and
	// zero output metadata — usable, just unenriched.
	luna := resp.Models[indexOfString(t, resp.Models, "gpt-6-luna")]
	if luna.ContextWindow != 272000 {
		t.Errorf("gpt-6-luna context window = %d, want the endpoint's 272000", luna.ContextWindow)
	}
	if luna.OutputLimit != 0 {
		t.Errorf("gpt-6-luna output limit = %d, want 0 (registry fallback posture)", luna.OutputLimit)
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
