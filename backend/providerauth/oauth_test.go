package providerauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeBrowser records the auth URL and drives the loopback redirect exactly
// like a browser would: it fetches the redirect_uri with code+state. tweak
// lets a test corrupt the callback query (bad state, OAuth error, …).
type fakeBrowser struct {
	mu      sync.Mutex
	authURL string
	tweak   func(q url.Values)
	client  *http.Client
}

func (b *fakeBrowser) open(authURL string) error {
	b.mu.Lock()
	b.authURL = authURL
	b.mu.Unlock()

	parsed, err := url.Parse(authURL)
	if err != nil {
		return fmt.Errorf("fakeBrowser: parsing auth URL: %w", err)
	}
	redirect := parsed.Query().Get("redirect_uri")
	callback := url.Values{}
	callback.Set("code", "test-auth-code")
	callback.Set("state", parsed.Query().Get("state"))
	if b.tweak != nil {
		b.tweak(callback)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, redirect+"?"+callback.Encode(), http.NoBody)
	if err != nil {
		return fmt.Errorf("fakeBrowser: building redirect request: %w", err)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("fakeBrowser: driving redirect: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// A 4xx answer is a valid loopback verdict (e.g. state mismatch): the
	// browser merely displays the page; the flow learns the outcome through
	// its own channel. Only transport failures abort here.
	if resp.StatusCode >= 500 {
		return fmt.Errorf("fakeBrowser: redirect answered %d", resp.StatusCode)
	}
	return nil
}

// tokenEndpointRecorder captures every token-endpoint request.
type tokenEndpointRecorder struct {
	mu       sync.Mutex
	grants   []string
	verifier string
	header   http.Header
}

func (rec *tokenEndpointRecorder) record(r *http.Request) {
	_ = r.ParseForm()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.grants = append(rec.grants, r.Form.Get("grant_type"))
	if v := r.Form.Get("code_verifier"); v != "" {
		rec.verifier = v
	}
	rec.header = r.Header.Clone()
}

// issuerServer is a throwaway OAuth issuer: /oauth/authorize is never hit
// (the browser is faked), /oauth/token answers per grant type.
func issuerServer(t *testing.T, rec *tokenEndpointRecorder, idToken string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "at-1",
				"refresh_token": "rt-1",
				"id_token":      idToken,
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		case "refresh_token":
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
	t.Cleanup(srv.Close)
	return srv
}

func testProfile(issuer string, port int) Profile {
	return Profile{
		ProviderID:      "chatgpt-test",
		Issuer:          issuer,
		ClientID:        "cid-test",
		Scopes:          []string{"openid", "profile", "email", "offline_access"},
		Originator:      "c0wrk",
		APIBaseURL:      "https://api.invalid/v1",
		RedirectPort:    port,
		RedirectHost:    "localhost",
		RedirectPath:    "/auth/callback",
		AuthorizeParams: map[string]string{"originator": "c0wrk"},
	}
}

func TestFlowSignIn_FullPKCEFlow(t *testing.T) {
	idToken := makeJWT(map[string]any{
		"sub":   "user-1",
		"email": "dev@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-1",
		},
	})
	rec := &tokenEndpointRecorder{}
	srv := issuerServer(t, rec, idToken)
	browser := &fakeBrowser{client: srv.Client()}

	flow := NewFlow(testProfile(srv.URL, 0), srv.Client(), browser.open)
	start := time.Now()
	res, err := flow.SignIn(context.Background())
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}

	// Authorize URL shape.
	authURL, err := url.Parse(browser.authURL)
	if err != nil {
		t.Fatalf("auth URL parse: %v", err)
	}
	if got := authURL.Scheme + "://" + authURL.Host; got != srv.URL {
		t.Errorf("authorize endpoint = %q, want issuer %q", got, srv.URL)
	}
	q := authURL.Query()
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q, want code", q.Get("response_type"))
	}
	if q.Get("client_id") != "cid-test" {
		t.Errorf("client_id = %q, want cid-test", q.Get("client_id"))
	}
	if q.Get("scope") != "openid profile email offline_access" {
		t.Errorf("scope = %q", q.Get("scope"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	if q.Get("state") == "" {
		t.Error("state is empty")
	}
	redirect := q.Get("redirect_uri")
	if !strings.HasPrefix(redirect, "http://localhost:") || !strings.HasSuffix(redirect, "/auth/callback") {
		t.Errorf("redirect_uri = %q, want localhost .../auth/callback", redirect)
	}
	// The profile's authorize extras ride along; core params stay intact.
	if q.Get("originator") != "c0wrk" {
		t.Errorf("originator authorize param = %q, want c0wrk", q.Get("originator"))
	}
	if q.Get("client_id") != "cid-test" || q.Get("response_type") != "code" {
		t.Errorf("core authorize params corrupted by extras: %v", q)
	}

	// Exchange request shape.
	rec.mu.Lock()
	grants, header, verifier := append([]string(nil), rec.grants...), rec.header, rec.verifier
	rec.mu.Unlock()
	if len(grants) != 1 || grants[0] != "authorization_code" {
		t.Fatalf("token endpoint grants = %v, want one authorization_code", grants)
	}
	if header.Get("originator") != "c0wrk" {
		t.Errorf("originator header = %q, want c0wrk", header.Get("originator"))
	}
	if header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", header.Get("Content-Type"))
	}

	// PKCE: the challenge in the authorize URL matches S256(verifier sent to
	// the token endpoint).
	sum := sha256.Sum256([]byte(verifier))
	wantChallenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if q.Get("code_challenge") != wantChallenge {
		t.Errorf("code_challenge mismatch: authorize URL and S256(verifier) differ")
	}

	// Result tokens + identity.
	if res.Tokens.AccessToken != "at-1" || res.Tokens.RefreshToken != "rt-1" {
		t.Errorf("tokens = %+v", res.Tokens)
	}
	if res.Tokens.TokenType != "Bearer" {
		t.Errorf("TokenType = %q, want Bearer", res.Tokens.TokenType)
	}
	if !strings.HasPrefix(res.Tokens.AccessToken, "at-") {
		t.Error("sanity")
	}
	if d := time.Until(res.Tokens.Expiry); d <= 0 || d > 3600*time.Second+(time.Since(start)) {
		t.Errorf("Expiry = %v, want now+3600s", res.Tokens.Expiry)
	}
	if res.Identity == nil {
		t.Fatal("Identity is nil")
	}
	if res.Identity.Email != "dev@example.com" || res.Identity.ChatGPTAccountID != "acct-1" {
		t.Errorf("Identity = %+v", res.Identity)
	}
}

func TestFlowSignIn_StateMismatchRejected(t *testing.T) {
	rec := &tokenEndpointRecorder{}
	srv := issuerServer(t, rec, "")
	browser := &fakeBrowser{client: srv.Client(), tweak: func(q url.Values) {
		q.Set("state", "forged-state")
	}}
	flow := NewFlow(testProfile(srv.URL, 0), srv.Client(), browser.open)
	_, err := flow.SignIn(context.Background())
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("SignIn err = %v, want ErrStateMismatch", err)
	}
}

func TestFlowSignIn_AccessDenied(t *testing.T) {
	rec := &tokenEndpointRecorder{}
	srv := issuerServer(t, rec, "")
	browser := &fakeBrowser{client: srv.Client(), tweak: func(q url.Values) {
		q.Del("code")
		q.Set("error", "access_denied")
	}}
	flow := NewFlow(testProfile(srv.URL, 0), srv.Client(), browser.open)
	_, err := flow.SignIn(context.Background())
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("SignIn err = %v, want ErrAccessDenied", err)
	}
}

func TestFlowSignIn_MissingCode(t *testing.T) {
	rec := &tokenEndpointRecorder{}
	srv := issuerServer(t, rec, "")
	browser := &fakeBrowser{client: srv.Client(), tweak: func(q url.Values) {
		q.Del("code")
	}}
	flow := NewFlow(testProfile(srv.URL, 0), srv.Client(), browser.open)
	_, err := flow.SignIn(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no authorization code") {
		t.Fatalf("SignIn err = %v, want missing-code error", err)
	}
}

func TestFlowSignIn_BusyPortFailsWithActionableError(t *testing.T) {
	rec := &tokenEndpointRecorder{}
	srv := issuerServer(t, rec, "")

	// Occupy the port on EVERY loopback family; the redirect URI is
	// registered exactly, so a fallback to another port would be rejected
	// by the issuer ("invalid_authorize_request") — the flow must fail
	// loudly instead.
	var occupiers []net.Listener
	defer func() {
		for _, ln := range occupiers {
			_ = ln.Close()
		}
	}()
	ln4, err4 := listenTCP("127.0.0.1:0")
	if err4 != nil {
		t.Fatalf("occupying IPv4 loopback: %v", err4)
	}
	occupiers = append(occupiers, ln4)
	tcpAddr, ok := ln4.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("occupier bound a non-TCP address %T", ln4.Addr())
	}
	busyPort := tcpAddr.Port
	if ln6, err6 := listenTCP(net.JoinHostPort("::1", strconv.Itoa(busyPort))); err6 == nil {
		occupiers = append(occupiers, ln6)
	} else {
		// IPv6 loopback unavailable: force the failure through the lone family.
		t.Logf("IPv6 loopback bind failed (%v); continuing IPv4-only", err6)
	}

	browser := &fakeBrowser{client: srv.Client()}
	flow := NewFlow(testProfile(srv.URL, busyPort), srv.Client(), browser.open)
	_, err := flow.SignIn(context.Background())
	if err == nil {
		t.Fatal("SignIn with a fully occupied port succeeded; want failure")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(busyPort)) {
		t.Errorf("error %q does not name the busy port %d", err, busyPort)
	}
}

// TestFlowSignIn_ServesRedirectThroughAnyLoopbackFamily pins the dual-stack
// listener: the redirect URI names "localhost", and the request must be
// served whichever family the client resolves it to.
func TestFlowSignIn_ServesRedirectThroughAnyLoopbackFamily(t *testing.T) {
	rec := &tokenEndpointRecorder{}
	srv := issuerServer(t, rec, "")
	// The fake browser dials "localhost" directly (the URI's host), exactly
	// like a real one; the flow must have bound a family that answers.
	browser := &fakeBrowser{client: srv.Client()}
	flow := NewFlow(testProfile(srv.URL, 0), srv.Client(), browser.open)
	if _, err := flow.SignIn(context.Background()); err != nil {
		t.Fatalf("SignIn through the localhost redirect: %v", err)
	}
	redirect, err := url.Parse(browser.authURL)
	if err != nil {
		t.Fatalf("auth URL parse: %v", err)
	}
	if host := redirect.Query().Get("redirect_uri"); !strings.Contains(host, "://localhost:") {
		t.Errorf("redirect_uri = %q, want the localhost host", host)
	}
}

func TestFlowSignIn_OpenerErrorAborts(t *testing.T) {
	openerErr := errors.New("no browser")
	flow := NewFlow(testProfile("https://issuer.invalid", 0), nil, func(string) error { return openerErr })
	_, err := flow.SignIn(context.Background())
	if !errors.Is(err, openerErr) {
		t.Fatalf("SignIn err = %v, want opener error", err)
	}
}

func TestFlowSignIn_ContextCancellation(t *testing.T) {
	// A browser that never arrives.
	flow := NewFlow(testProfile("https://issuer.invalid", 0), nil, func(string) error { return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := flow.SignIn(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SignIn err = %v, want context.DeadlineExceeded", err)
	}
}

// TestPKCEHelpers pins the RFC 7636 shapes: verifier length and alphabet,
// challenge derivation.
func TestPKCEHelpers(t *testing.T) {
	verifier, err := newCodeVerifier()
	if err != nil {
		t.Fatalf("newCodeVerifier: %v", err)
	}
	if len(verifier) != 43 { // 32 octets -> 43 unpadded base64url chars
		t.Errorf("verifier length = %d, want 43", len(verifier))
	}
	challenge, err := codeChallengeS256(verifier)
	if err != nil {
		t.Fatalf("codeChallengeS256: %v", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	if challenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Error("challenge is not base64url(SHA256(verifier))")
	}
	if _, err := codeChallengeS256(""); err == nil {
		t.Error("empty verifier must be rejected")
	}

	s1, _ := newState()
	s2, _ := newState()
	if s1 == s2 || len(s1) != 43 {
		t.Errorf("state values not fresh/uniform: %q %q", s1, s2)
	}
}

// TestStartCallbackServer_LateCallbacksDoNotBlock pins the one-shot publish
// contract: after the first callback's verdict entered the channel, any
// number of additional redirects must be ANSWERED and dropped — never left
// blocked on the send — so a browser retry (or a second tab, or a local
// process hitting the port) cannot leak handler goroutines past the flow's
// conclusion.
func TestStartCallbackServer_LateCallbacksDoNotBlock(t *testing.T) {
	ln, err := listenLoopback(0)
	if err != nil {
		t.Fatalf("listenLoopback: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("loopback listener bound a non-TCP address %T", ln.Addr())
	}
	port := tcpAddr.Port
	w := startCallbackServer(ln, "/auth/callback", "state-1")
	t.Cleanup(w.stop)

	client := &http.Client{Timeout: 3 * time.Second}
	statusOf := func(t *testing.T, target string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("callback request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	// First callback publishes the verdict.
	if code := statusOf(t, fmt.Sprintf("http://localhost:%d/auth/callback?code=code-1&state=state-1", port)); code != http.StatusOK {
		t.Errorf("first callback status = %d, want 200", code)
	}

	// A burst of late callbacks must all be answered promptly — a blocked
	// send would hang the requests (and this test) long before the server
	// is stopped.
	for i := range 5 {
		code := statusOf(t, fmt.Sprintf("http://localhost:%d/auth/callback?error=access_denied&state=late-%d", port, i))
		if code != http.StatusBadRequest {
			t.Errorf("late callback %d status = %d, want 400 (answered, verdict dropped)", i, code)
		}
	}

	// Exactly one verdict was published and it is the first callback's.
	select {
	case cb := <-w.done:
		if cb.code != "code-1" || cb.err != nil {
			t.Errorf("published verdict = %+v, want the first callback's code-1", cb)
		}
	default:
		t.Fatal("no verdict was published")
	}
	select {
	case cb := <-w.done:
		t.Errorf("a second verdict was published: %+v", cb)
	default:
	}
}

// TestListenLoopback_PortBusyOnOneFamilyFails pins the busy-port rule: when
// another process holds the port on ONE loopback family (the other being
// free — or unavailable on this host), the bind must fail with the
// actionable in-use error instead of succeeding on the remaining family and
// leaving the sign-in waiting for a redirect another process will swallow.
func TestListenLoopback_PortBusyOnOneFamilyFails(t *testing.T) {
	// Hold the port on IPv4 loopback only.
	hold, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("holding IPv4 listener: %v", err)
	}
	t.Cleanup(func() { _ = hold.Close() })
	holdAddr, ok := hold.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("hold listener bound a non-TCP address %T", hold.Addr())
	}
	port := holdAddr.Port

	if _, err := listenLoopback(port); err == nil {
		t.Fatal("listenLoopback succeeded although the port is busy on 127.0.0.1 (the browser may resolve localhost to the busy family)")
	} else if !strings.Contains(err.Error(), "already in use") {
		t.Errorf("listenLoopback error = %v, want the actionable in-use message", err)
	}
}

// TestIsAddrInUse_RecognizesThePlatformErrno pins the synthetic spelling on
// both platforms and the real Winsock code on Windows: the syscall package's
// EADDRINUSE is an invented value there that no OS call produces, so the
// Windows implementation must additionally recognize WSAEADDRINUSE (10048) —
// the code an actual busy bind returns.
func TestIsAddrInUse_RecognizesThePlatformErrno(t *testing.T) {
	if !isAddrInUse(syscall.EADDRINUSE) {
		t.Errorf("isAddrInUse(syscall.EADDRINUSE) = false, want true")
	}
	if isAddrInUse(errors.New("providerauth: not an errno")) {
		t.Errorf("isAddrInUse(plain error) = true, want false")
	}
	if runtime.GOOS == "windows" && !isAddrInUse(syscall.Errno(10048)) {
		t.Errorf("isAddrInUse(WSAEADDRINUSE 10048) = false, want true on Windows")
	}
}
