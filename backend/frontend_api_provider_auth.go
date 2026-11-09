package backend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/providerauth"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/proxy"
	"github.com/v0lka/sp4rk/llm"
)

// Shared subscription-auth timeouts, mode and flow states.
const (
	providerAuthSignInJoinTimeout  = 5 * time.Second  // Bound joining a cancelled sign-in.
	providerAuthSignInStartTimeout = 10 * time.Second // Bound waiting for the authorization URL.
	providerAuthModeOAuth          = "oauth"          // Browser-issued subscription credentials.
	providerAuthEventPending       = "pending"        // Authorization URL is ready.
	providerAuthEventSuccess       = "success"        // Fresh credentials are usable.
	providerAuthEventError         = "error"          // Sign-in failed.
	providerAuthEventCancelled     = "cancelled"      // Flow or credentials were invalidated.
)

// providerAuthState holds one provider's independently restored account and
// in-flight browser flow. The manager is called outside mu, except for the
// proxy-client ordering protocol documented on pushMu.
type providerAuthState struct {
	mu sync.Mutex

	pushMu           sync.Mutex
	pushSeq          uint64
	pushInstalledSeq uint64

	manager   providerAuthManager
	initError string
	restoring bool

	inFlight        bool
	cancel          context.CancelFunc
	runDone         chan struct{}
	cancelRequested bool
	// rebinding excludes new sign-ins during a region transition.
	rebinding                 bool
	rebindJoinTimeoutOverride time.Duration
	lastError                 string

	modelsBaseURL         string
	modelsBaseURLOverride string

	signInStartTimeoutOverride time.Duration
	newManagerFn               func() (providerAuthManager, error)
}

type providerAuthManager interface {
	SignIn(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error)
	SignOut() error
	Status() providerauth.AccountStatus
	Token(ctx context.Context) (llm.BearerToken, error)
}

var _ providerAuthManager = (*providerauth.TokenManager)(nil)

type providerAuthDescriptor struct {
	providerName string
	displayName  string
	profile      func() providerauth.Profile
	authMode     func() string
	// notifySignedOut publishes cancelled so account-scoped UI state is invalidated.
	notifySignedOut bool
	emit            func(providerAuthEvent)
}

type providerAuthEvent struct {
	state     string
	authURL   string
	err       string
	email     string
	accountID string
	expiresAt string
}

type providerAuthStatus struct {
	signedIn  bool
	email     string
	accountID string
	expiresAt string
	mode      string
	inFlight  bool
	lastError string
}

func (f *FrontendAPI) initProviderAuth(state *providerAuthState, desc providerAuthDescriptor) {
	if err := f.ctx().Err(); err != nil {
		return
	}
	newManager := state.newManagerFn
	if newManager == nil {
		newManager = func() (providerAuthManager, error) {
			return providerauth.NewTokenManager(providerauth.ManagerConfig{
				Profile:    desc.profile(),
				Store:      providerauth.NewKeyringSecretStore(),
				HTTPClient: f.providerAuthOutboundHTTPClient(30 * time.Second),
				Logger:     f.log(),
				AgentDir:   f.agentDir,
			})
		}
	}
	state.mu.Lock()
	state.restoring = true
	state.mu.Unlock()
	m, err := constructProviderAuthManager(newManager)
	baseURL := state.modelsBaseURLOverride
	if baseURL == "" {
		baseURL = desc.profile().APIBaseURL
	}
	state.mu.Lock()
	state.restoring = false
	if err != nil {
		state.initError = err.Error()
		state.mu.Unlock()
		f.log().Warn(desc.displayName+" subscription auth unavailable", "error", err)
		return
	}
	state.manager = m
	state.modelsBaseURL = baseURL
	state.mu.Unlock()

	var restoreWarning string
	if wm, ok := m.(interface{ LoadWarning() string }); ok {
		restoreWarning = wm.LoadWarning()
	}
	state.mu.Lock()
	f.pushProviderManagerProxyClientLocked(state)
	if restoreWarning != "" {
		state.lastError = restoreWarning
		f.log().Warn(desc.displayName+" subscription auth restored with a problem", "warning", restoreWarning)
	}
	state.mu.Unlock()
	if err := f.ctx().Err(); err != nil {
		return
	}

	f.syncProviderBuilderSeam(state, desc.providerName)
	st := m.Status()
	if st.SignedIn && desc.authMode() == providerAuthModeOAuth {
		f.rebuildRouterAfterProviderAuthChange(state, desc)
	}
	if st.SignedIn {
		state.mu.Lock()
		if !state.inFlight {
			event := providerAuthEvent{
				state:     providerAuthEventSuccess,
				email:     providerAuthIdentityField(st.Identity, func(i *providerauth.Identity) string { return i.Email }),
				accountID: providerAuthIdentityField(st.Identity, func(i *providerauth.Identity) string { return i.ChatGPTAccountID }),
			}
			if !st.ExpiresAt.IsZero() {
				event.expiresAt = st.ExpiresAt.Format(time.RFC3339)
			}
			desc.emit(event)
		}
		state.mu.Unlock()
	}
}

func constructProviderAuthManager(newManager func() (providerAuthManager, error)) (m providerAuthManager, err error) {
	defer func() {
		if r := recover(); r != nil {
			m = nil
			err = fmt.Errorf("the OS keychain backend panicked during the account restore: %v", r)
		}
	}()
	return newManager()
}

func providerAuthNilManagerRefusalLocked(state *providerAuthState, desc providerAuthDescriptor, action string) error {
	switch {
	case state.initError != "":
		return fmt.Errorf("%s %s is unavailable on this machine: %s", desc.displayName, action, state.initError)
	case state.restoring:
		return fmt.Errorf("%s %s is unavailable while the account restore is still reading the OS keychain — try again in a moment", desc.displayName, action)
	default:
		return fmt.Errorf("%s %s is unavailable: the auth subsystem was not initialized", desc.displayName, action)
	}
}

type providerAuthSignInStart struct {
	url string
	err error
}

func (f *FrontendAPI) startProviderSignIn(state *providerAuthState, desc providerAuthDescriptor) (string, error) {
	state.mu.Lock()
	if state.rebinding {
		state.mu.Unlock()
		return "", fmt.Errorf("%s sign-in is unavailable while the account region is changing — try again in a moment", desc.displayName)
	}
	if state.inFlight {
		state.mu.Unlock()
		return "", fmt.Errorf("a %s sign-in is already in progress — cancel it before starting another", desc.displayName)
	}
	m := state.manager
	if m == nil {
		err := providerAuthNilManagerRefusalLocked(state, desc, "sign-in")
		state.mu.Unlock()
		return "", err
	}
	ctx, cancel := context.WithCancel(f.ctx())
	state.inFlight = true
	state.cancel = cancel
	runDone := make(chan struct{})
	state.runDone = runDone
	state.cancelRequested = false
	state.lastError = ""
	state.mu.Unlock()

	urlCh := make(chan providerAuthSignInStart, 1)
	openerCalled := false
	opener := func(authURL string) error {
		openerCalled = true
		desc.emit(providerAuthEvent{state: providerAuthEventPending, authURL: authURL})
		urlCh <- providerAuthSignInStart{url: authURL}
		return nil
	}
	go func() {
		defer close(runDone)
		result, err := m.SignIn(ctx, opener)
		if err != nil && !openerCalled {
			urlCh <- providerAuthSignInStart{err: err}
		}
		state.mu.Lock()
		requested := state.cancelRequested
		rebinding := state.rebinding
		state.inFlight = false
		state.cancel = nil
		state.runDone = nil
		state.cancelRequested = false
		state.mu.Unlock()
		cancel()

		switch {
		case err == nil && !rebinding:
			state.mu.Lock()
			state.lastError = ""
			state.mu.Unlock()
			f.syncProviderBuilderSeam(state, desc.providerName)
			f.rebuildRouterAfterProviderAuthChange(state, desc)
			desc.emit(providerAuthEvent{
				state:     providerAuthEventSuccess,
				email:     providerAuthIdentityField(providerAuthIdentity(result), func(i *providerauth.Identity) string { return i.Email }),
				accountID: providerAuthIdentityField(providerAuthIdentity(result), func(i *providerauth.Identity) string { return i.ChatGPTAccountID }),
				expiresAt: providerAuthExpiry(result),
			})
		case requested && (err == nil || errors.Is(err, context.Canceled)):
			desc.emit(providerAuthEvent{state: providerAuthEventCancelled})
		case rebinding && err == nil:
			// The region rebind owns the terminal event and builder state.
		default:
			state.mu.Lock()
			state.lastError = err.Error()
			state.mu.Unlock()
			desc.emit(providerAuthEvent{state: providerAuthEventError, err: err.Error()})
		}
	}()

	timeout := state.signInStartTimeoutOverride
	if timeout <= 0 {
		timeout = providerAuthSignInStartTimeout
	}
	select {
	case start := <-urlCh:
		if start.err != nil {
			return "", fmt.Errorf("starting the %s sign-in flow: %w", desc.displayName, start.err)
		}
		return start.url, nil
	case <-time.After(timeout):
		select {
		case start := <-urlCh:
			if start.err != nil {
				return "", fmt.Errorf("starting the %s sign-in flow: %w", desc.displayName, start.err)
			}
			return start.url, nil
		default:
		}
		state.mu.Lock()
		if state.inFlight {
			state.cancelRequested = true
		}
		state.mu.Unlock()
		cancel()
		return "", fmt.Errorf("starting the %s sign-in flow: no authorization URL within %s", desc.displayName, timeout)
	}
}

func (f *FrontendAPI) cancelProviderSignIn(state *providerAuthState) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.inFlight && state.cancel != nil {
		state.cancelRequested = true
		state.cancel()
	}
	return nil
}

func (f *FrontendAPI) providerAuthStatus(state *providerAuthState, desc providerAuthDescriptor) providerAuthStatus {
	mode := desc.authMode()
	state.mu.Lock()
	m := state.manager
	resp := providerAuthStatus{mode: mode, inFlight: state.inFlight, lastError: state.lastError}
	initErr := state.initError
	state.mu.Unlock()
	if m == nil {
		if resp.lastError == "" {
			resp.lastError = initErr
		}
		return resp
	}
	st := m.Status()
	resp.signedIn = st.SignedIn
	if st.SignedIn {
		resp.email = providerAuthIdentityField(st.Identity, func(i *providerauth.Identity) string { return i.Email })
		resp.accountID = providerAuthIdentityField(st.Identity, func(i *providerauth.Identity) string { return i.ChatGPTAccountID })
		if !st.ExpiresAt.IsZero() {
			resp.expiresAt = st.ExpiresAt.Format(time.RFC3339)
		}
	}
	return resp
}

func (f *FrontendAPI) signOutProvider(state *providerAuthState, desc providerAuthDescriptor) error {
	state.mu.Lock()
	m := state.manager
	if m == nil {
		err := providerAuthNilManagerRefusalLocked(state, desc, "sign-out")
		state.mu.Unlock()
		return err
	}
	if state.inFlight {
		state.cancelRequested = true
		state.cancel()
		done := state.runDone
		state.mu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-time.After(providerAuthSignInJoinTimeout):
				f.log().Warn("sign-out proceeded while a cancelled " + desc.displayName + " sign-in was still concluding")
			}
		}
	} else {
		state.mu.Unlock()
	}
	if err := m.SignOut(); err != nil {
		return fmt.Errorf("signing out of %s: %w", desc.displayName, err)
	}
	state.mu.Lock()
	state.lastError = ""
	state.mu.Unlock()
	f.syncProviderBuilderSeam(state, desc.providerName)
	f.rebuildRouterAfterProviderAuthChange(state, desc)
	if desc.notifySignedOut {
		desc.emit(providerAuthEvent{state: providerAuthEventCancelled})
	}
	return nil
}

func (f *FrontendAPI) syncProviderBuilderSeam(state *providerAuthState, providerName string) {
	b := f.builder()
	if b == nil {
		return
	}
	state.mu.Lock()
	m := state.manager
	state.mu.Unlock()
	seam := core.BuilderSubscriptionAuthConfig{}
	if providerName != chatGPTProviderName {
		seam.ProviderName = providerName
	}
	if m != nil && m.Status().SignedIn {
		seam.ProviderName = providerName
		seam.TokenSource = m
	}
	b.SetSubscriptionTokenSource(seam)
}

func (f *FrontendAPI) rebuildRouterAfterProviderAuthChange(state *providerAuthState, desc providerAuthDescriptor) {
	f.saveMu.Lock()
	defer f.saveMu.Unlock()
	f.syncProviderBuilderSeam(state, desc.providerName)
	b := f.builder()
	if b == nil {
		return
	}
	f.configMu.RLock()
	fresh := f.toBuilderConfigLocked()
	b.RebuildJudge(fresh)
	b.UpdateModelOverrides(fresh)
	err := b.RebuildRouter(fresh)
	f.configMu.RUnlock()
	if err != nil {
		f.log().Warn("failed to rebuild the LLM router after a "+desc.displayName+" auth change", "error", err)
	}
}

func (f *FrontendAPI) providerAuthProxyConfig() proxy.Config {
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

func (f *FrontendAPI) providerAuthOutboundHTTPClient(timeout time.Duration) *http.Client {
	client, err := proxy.BuildClient(f.providerAuthProxyConfig(), timeout, f.log())
	if err != nil {
		f.log().Warn("failed to build the subscription-auth outbound proxy client, proceeding without proxy", "error", err)
		return &http.Client{Timeout: timeout}
	}
	if client == nil {
		return &http.Client{Timeout: timeout}
	}
	return client
}

// pushProviderManagerProxyClientLocked refreshes the token-endpoint client.
// Caller holds state.mu; installation runs outside it and preserves sequence.
func (f *FrontendAPI) pushProviderManagerProxyClientLocked(state *providerAuthState) {
	m := state.manager
	if m == nil {
		return
	}
	setter, ok := m.(interface{ SetHTTPClient(*http.Client) })
	if !ok {
		return
	}
	client := f.providerAuthOutboundHTTPClient(30 * time.Second)
	state.pushSeq++
	seq := state.pushSeq
	state.mu.Unlock()
	state.pushMu.Lock()
	if seq > state.pushInstalledSeq {
		state.pushInstalledSeq = seq
		setter.SetHTTPClient(client)
	}
	state.pushMu.Unlock()
	state.mu.Lock()
}

func providerAuthIdentity(result *providerauth.SignInResult) *providerauth.Identity {
	if result == nil {
		return nil
	}
	return result.Identity
}

func providerAuthExpiry(result *providerauth.SignInResult) string {
	if result == nil || result.Tokens == nil || result.Tokens.Expiry.IsZero() {
		return ""
	}
	return result.Tokens.Expiry.Format(time.RFC3339)
}

func providerAuthIdentityField(id *providerauth.Identity, get func(*providerauth.Identity) string) string {
	if id == nil {
		return ""
	}
	return get(id)
}
