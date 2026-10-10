package providerauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"
)

// DefaultCallbackWait bounds how long the flow waits for the browser redirect
// before giving up. It can be shortened through the context passed to SignIn.
const DefaultCallbackWait = 5 * time.Minute

// ErrBrowserCallback reports that the browser redirect never arrived.
var ErrBrowserCallback = errors.New("providerauth: no browser redirect arrived before timeout")

// ErrStateMismatch reports a redirect whose OAuth state does not match the
// request (a forged or replayed callback).
var ErrStateMismatch = errors.New("providerauth: OAuth state mismatch on redirect")

// ErrAccessDenied reports the user declining consent in the browser.
var ErrAccessDenied = errors.New("providerauth: authorization was denied in the browser")

// BrowserOpener presents the authorization URL to the user. Production code
// uses DefaultBrowserOpener; tests inject a fake that drives the loopback
// listener programmatically.
type BrowserOpener func(authURL string) error

// DefaultBrowserOpener opens authURL in the OS default browser. It returns an
// error when no browser launcher exists for the platform.
func DefaultBrowserOpener(authURL string) error {
	var command string
	var args []string
	switch hostOS() {
	case "darwin":
		command, args = "open", []string{authURL}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", authURL}
	default:
		command, args = "xdg-open", []string{authURL}
	}
	cmd := exec.CommandContext(context.Background(), command, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("providerauth: opening the system browser (%s): %w", command, err)
	}
	// Reap the launcher child (xdg-open / open / rundll32): Start must be
	// followed by Wait (or Release) or the exited child stays a zombie in the
	// process table for the app's lifetime — one leaked entry per sign-in.
	// The Wait goroutine detaches on purpose: the browser launcher must not
	// be killed when the sign-in flow returns (the context has no deadline
	// and no cancellation), and its error carries no actionable signal.
	go func() { _ = cmd.Wait() }()
	return nil
}

// Flow runs one browser OAuth sign-in for a Profile: it binds the loopback
// listener, opens the browser, awaits the redirect, and exchanges the
// authorization code for tokens. Flow values are single-use per SignIn call
// and safe to share across calls.
type Flow struct {
	profile Profile
	client  *http.Client
	opener  BrowserOpener
	now     func() time.Time
}

// NewFlow returns a Flow for profile. client defaults to a conservative
// HTTP client with a bounded timeout; opener defaults to
// DefaultBrowserOpener.
func NewFlow(profile Profile, client *http.Client, opener BrowserOpener) *Flow {
	if client == nil {
		client = defaultHTTPClient()
	}
	if opener == nil {
		opener = DefaultBrowserOpener
	}
	return &Flow{profile: profile, client: client, opener: opener, now: time.Now}
}

// SignInResult is the outcome of a successful sign-in.
type SignInResult struct {
	// Tokens are the freshly issued OAuth tokens.
	Tokens *Tokens
	// Identity is extracted from the ID token (unverified; see
	// ParseIDTokenClaims).
	Identity *Identity
}

// SignIn performs the full authorization-code flow with PKCE and returns the
// issued tokens. ctx bounds both the wait for the browser redirect and the
// token exchange. Secrets never appear in returned errors or logs.
func (f *Flow) SignIn(ctx context.Context) (*SignInResult, error) {
	verifier, err := newCodeVerifier()
	if err != nil {
		return nil, err
	}
	state, err := newState()
	if err != nil {
		return nil, err
	}

	listener, err := listenLoopback(f.profile.RedirectPort)
	if err != nil {
		return nil, err
	}
	defer func() { _ = listener.Close() }()

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return nil, fmt.Errorf("providerauth: loopback listener bound a non-TCP address %T", listener.Addr())
	}
	redirectURI := f.profile.RedirectURI(tcpAddr.Port)

	authURL, err := f.authorizeURL(redirectURI, state, verifier)
	if err != nil {
		return nil, err
	}

	// Start serving the redirect BEFORE handing the URL to the browser, so an
	// opener that synchronously drives the redirect cannot deadlock against a
	// listener that is not accepting yet.
	callback := startCallbackServer(listener, f.profile.RedirectPath, state)
	if err := f.opener(authURL); err != nil {
		callback.stop()
		return nil, err
	}

	code, err := callback.wait(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := f.exchange(ctx, code, verifier, redirectURI)
	if err != nil {
		return nil, err
	}

	tokens := resp.tokens(f.now())
	var identity *Identity
	if resp.IDToken != "" {
		identity, err = ParseIDTokenClaims(resp.IDToken)
		if err != nil {
			return nil, err
		}
	}
	return &SignInResult{Tokens: tokens, Identity: identity}, nil
}

// authorizeURL builds the authorization endpoint URL with PKCE S256 and state.
func (f *Flow) authorizeURL(redirectURI, state, verifier string) (string, error) {
	challenge, err := codeChallengeS256(verifier)
	if err != nil {
		return "", err
	}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {f.profile.ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {f.profile.ScopeString()},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	// Provider-specific extras (simplified-flow switches, originator, …);
	// core OAuth parameters above always win, so the extras cannot corrupt
	// them.
	for k, v := range f.profile.AuthorizeParams {
		if _, core := q[k]; !core && v != "" {
			q.Set(k, v)
		}
	}
	return f.profile.AuthorizeURL() + "?" + q.Encode(), nil
}

// callbackWaiter owns the one-shot redirect server.
type callbackWaiter struct {
	srv      *http.Server
	done     chan callbackResult
	serveErr chan error
	// publish serializes result publication: exactly ONE callback result
	// ever enters the (buffered) channel, so a stray second redirect (the
	// browser retrying, a second tab, a local process hammering the port)
	// cannot fill the buffer and leave later handlers blocked forever on
	// the send. Late callbacks are answered (the browser gets its page) and
	// dropped.
	publishOnce sync.Once
}

// startCallbackServer begins serving exactly one redirect request on
// listener at callbackPath, validating the OAuth state on it.
func startCallbackServer(listener net.Listener, callbackPath, state string) *callbackWaiter {
	w := &callbackWaiter{
		srv: &http.Server{
			ReadHeaderTimeout: 10 * time.Second,
		},
		done:     make(chan callbackResult, 1),
		serveErr: make(chan error, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(rw http.ResponseWriter, r *http.Request) {
		cb := handleCallbackQuery(r.URL.Query(), state)
		// Answer the browser regardless of validation outcome; the verdict
		// travels back through the channel.
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		rw.Header().Set("Cache-Control", "no-store")
		if cb.err != nil {
			rw.WriteHeader(http.StatusBadRequest)
			_, _ = rw.Write([]byte("<html><body><h1>Sign-in failed</h1><p>You can close this tab and return to c0wrk.</p></body></html>"))
		} else {
			rw.WriteHeader(http.StatusOK)
			_, _ = rw.Write([]byte("<html><body><h1>Sign-in complete</h1><p>You can close this tab and return to c0wrk.</p></body></html>"))
		}
		// Publish the verdict at most once; late callbacks (a second
		// redirect while the waiter is concluding) are answered above and
		// never block this handler goroutine.
		w.publishOnce.Do(func() { w.done <- cb })
	})
	w.srv.Handler = mux

	go func() { w.serveErr <- w.srv.Serve(listener) }()
	return w
}

// wait blocks for the browser redirect until ctx or DefaultCallbackWait
// elapses.
func (w *callbackWaiter) wait(ctx context.Context) (string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, DefaultCallbackWait)
	defer cancel()

	select {
	case cb := <-w.done:
		_ = w.srv.Close()
		if cb.err != nil {
			return "", cb.err
		}
		return cb.code, nil
	case <-waitCtx.Done():
		_ = w.srv.Close()
		if errors.Is(waitCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return "", fmt.Errorf("%w after %s", ErrBrowserCallback, DefaultCallbackWait)
		}
		return "", fmt.Errorf("providerauth: sign-in cancelled: %w", ctx.Err())
	case err := <-w.serveErr:
		return "", fmt.Errorf("providerauth: loopback listener failed: %w", err)
	}
}

// stop tears the server down without waiting for a redirect.
func (w *callbackWaiter) stop() {
	_ = w.srv.Close()
}

// handleCallbackQuery validates one redirect query and extracts the code.
func handleCallbackQuery(q url.Values, wantState string) callbackResult {
	if errCode := q.Get("error"); errCode != "" {
		desc := q.Get("error_description")
		if desc == "" {
			desc = "the authorization endpoint reported an error"
		}
		if errCode == "access_denied" {
			return callbackResult{err: fmt.Errorf("%w: %s", ErrAccessDenied, desc)}
		}
		return callbackResult{err: fmt.Errorf("providerauth: authorization endpoint error %q: %s", errCode, desc)}
	}
	if got := q.Get("state"); got != wantState {
		return callbackResult{err: ErrStateMismatch}
	}
	code := q.Get("code")
	if code == "" {
		return callbackResult{err: errors.New("providerauth: redirect carried no authorization code")}
	}
	return callbackResult{code: code}
}

// callbackResult is the internal verdict of one redirect.
type callbackResult struct {
	code string
	err  error
}

// newCodeVerifier returns a fresh RFC 7636 code verifier (43-char base64url
// of 32 random octets).
func newCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("providerauth: generating PKCE verifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// codeChallengeS256 derives the S256 code challenge for a verifier.
func codeChallengeS256(verifier string) (string, error) {
	if verifier == "" {
		return "", errors.New("providerauth: empty PKCE verifier")
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// newState returns a fresh single-use OAuth state value.
func newState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("providerauth: generating OAuth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// listenLoopback binds the redirect port on every loopback family the host
// provides (IPv4 127.0.0.1 and, when available, IPv6 ::1), because the
// redirect URI names "localhost" and browsers may connect through either
// family. The port itself is not negotiable: the issuer allow-lists the
// exact redirect URI, so a busy port must fail the sign-in with an
// actionable error rather than silently moving to an unregistered port.
//
// A family the host cannot provide (no IPv6 stack) is skipped silently, but
// EADDRINUSE on a family the host DOES provide is fatal even when the other
// family bound fine: the redirect URI names "localhost", and the browser may
// resolve it to the busy family — another process would then accept the
// redirect and the sign-in would wait for its full timeout instead of
// failing fast with the busy-port error.
func listenLoopback(port int) (net.Listener, error) {
	var listeners []net.Listener
	for _, host := range []string{"127.0.0.1", "::1"} {
		ln, err := listenTCP(net.JoinHostPort(host, strconv.Itoa(port)))
		switch {
		case err == nil:
			listeners = append(listeners, ln)
		case isAddrInUse(err):
			// Busy on a family the host provides: fail the whole bind.
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return nil, fmt.Errorf("providerauth: binding the loopback redirect listener on port %d: the port is already in use on %s (another sign-in or the Codex CLI may be listening there)", port, host)
		default:
			// Unavailable family (no stack / no address): skip it.
		}
	}
	switch len(listeners) {
	case 0:
		return nil, fmt.Errorf("providerauth: binding the loopback redirect listener on port %d: no loopback address is free (another sign-in or the Codex CLI may be listening there)", port)
	case 1:
		return listeners[0], nil
	default:
		return newMultiListener(listeners), nil
	}
}

// multiListener merges several bound listeners behind one net.Listener so a
// single http.Server serves every loopback family. Addr reports the first
// bound listener (all families share the port in production flows).
type multiListener struct {
	listeners []net.Listener
	accepts   chan net.Conn
	closed    chan struct{}
	closeOnce sync.Once
}

// newMultiListener starts pumping connections from every listener.
func newMultiListener(listeners []net.Listener) *multiListener {
	m := &multiListener{
		listeners: listeners,
		accepts:   make(chan net.Conn),
		closed:    make(chan struct{}),
	}
	for _, ln := range listeners {
		go func(ln net.Listener) {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return // listener closed or broken; its sibling keeps serving
				}
				select {
				case m.accepts <- conn:
				case <-m.closed:
					_ = conn.Close()
					return
				}
			}
		}(ln)
	}
	return m
}

// Accept returns the next connection from any family.
func (m *multiListener) Accept() (net.Conn, error) {
	select {
	case conn := <-m.accepts:
		return conn, nil
	case <-m.closed:
		select {
		case conn := <-m.accepts:
			return conn, nil
		default:
			return nil, net.ErrClosed
		}
	}
}

// Close tears every bound listener down.
func (m *multiListener) Close() error {
	var err error
	m.closeOnce.Do(func() {
		close(m.closed)
		for _, ln := range m.listeners {
			if cerr := ln.Close(); err == nil {
				err = cerr
			}
		}
	})
	return err
}

// Addr reports the first bound listener's address.
func (m *multiListener) Addr() net.Addr {
	return m.listeners[0].Addr()
}

// listenTCP binds one loopback address through a context-aware ListenConfig.
func listenTCP(addr string) (net.Listener, error) {
	return (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
}

// defaultHTTPClient is the fallback HTTP client used for token-endpoint
// calls when the caller does not supply one.
func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// hostOS returns the running OS; a variable so tests can exercise the
// non-native browser-launcher branch.
var hostOS = func() string { return runtime.GOOS }
