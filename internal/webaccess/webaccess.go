// SPDX-License-Identifier: Apache-2.0
//
// Package webaccess is the browser web-access sidecar: an HTTP + WebSocket
// server that mirrors the desktop UI into a browser tab by dispatching the
// same RPC/event surface the Wails webview uses. Loopback by default; a
// non-loopback (LAN-exposed) bind is an allowed, warned operator decision,
// with optional sidecar-terminated TLS (web_access.tls). The package is
// deliberately SELF-CONTAINED — stdlib + backend/config only, never Wails
// or desktop (guarded by nodeps_test.go); everything environment-specific
// is injected through Options (dispatch target, SPA tree, CSP middleware,
// resolved config section). Surface table, security posture, lifecycle and
// the denylist: specs/domains/web-access.md,
// specs/decisions/101-browser-web-access.md,
// specs/decisions/102-web-access-tls.md.
package webaccess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
)

const (
	// cookieName is the session cookie carrying the per-launch secret.
	cookieName = "c0wrk_web"
	// maxBodyBytes bounds every JSON request body; oversize → 413.
	maxBodyBytes = 1 << 20 // 1 MiB
	// maxUploadBodyBytes bounds upload-carrying RPC bodies (uploadBodyMethods
	// in dispatch.go): the backend's 32 MiB raw attachment cap plus base64
	// inflation (~4/3) and envelope slack. Ordinary RPCs keep maxBodyBytes.
	maxUploadBodyBytes = 48 << 20 // 48 MiB
	// tokenBytes is the entropy of a generated auth token — the desktop-minted
	// persistent credential and the per-launch fallback alike.
	tokenBytes = 32
	// shutdownBudget bounds the graceful Stop wait when the caller passes a
	// deadline-free context.
	shutdownBudget = 5 * time.Second
	// readHeaderTimeout and idleTimeout harden the HTTP server against
	// slowloris-style stalling on the listener.
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 2 * time.Minute
	// requestReadDeadline bounds one request's body read (armReadDeadline):
	// ReadHeaderTimeout covers only the header phase, so without this a
	// slow-POST trickle could pin a goroutine per connection. Generous by
	// design — it must carry a full 48 MiB upload over a slow link; it
	// bounds the trickle, not normal traffic.
	requestReadDeadline = 15 * time.Minute
	// wsPath is advertised by /api/transport so the SPA shell can find the
	// event channel without hardcoding it.
	wsPath = "/ws"
)

// ErrDisabled is returned by New when the config section is off: the sidecar
// must not be constructed for a disabled section (belt-and-braces on top of
// the load-time fail-soft normalization in backend/config).
var ErrDisabled = errors.New("webaccess: web_access section is not enabled")

// Options carries every dependency the sidecar needs from the outside world.
// Nothing inside the package constructs any of them.
type Options struct {
	// Config is the resolved `web_access` config section. New refuses a
	// disabled section (ErrDisabled), an invalid bind and a TLS subsection
	// without a readable pair (fail-closed); a valid non-loopback bind is
	// accepted with the exposure warning (ADR-102).
	Config config.WebAccessConfig
	// Dispatch is the RPC target (at runtime a *backend.FrontendAPI): its
	// exported methods are reachable, minus the denylist (dispatch.go). Required.
	Dispatch any
	// ExtraDispatch is a SECONDARY reflection target, consulted only when
	// Dispatch has no such method — the few App-level capabilities the web
	// surface must mirror (today GetPendingActions). The desktop layer opts
	// in through a MINIMAL adapter struct, never the App itself: its control
	// RPCs must stay unreachable from the web. The denylist applies to the
	// union; the surface contract test pins the extra set exactly. Optional;
	// a non-nil interface holding a nil pointer fails closed in New.
	ExtraDispatch any
	// Static is the SPA asset tree served at / WITHOUT authentication (the
	// shell carries no data). Optional: nil disables the static route.
	Static fs.FS
	// CSP wraps handlers with the Content-Security-Policy middleware. The
	// caller injects desktop.CSPMiddleware() (build-tag aware); nil means
	// "no CSP" (dev builds already compile the no-op variant).
	CSP func(http.Handler) http.Handler
	// Logger receives sidecar diagnostics. Nil → slog.Default(). Secrets
	// (auth token, session cookie) are never logged, in full or in part.
	Logger *slog.Logger
	// WSInbound receives inbound websocket messages from authenticated
	// browser clients. Optional (nil discards); called on the connection's
	// read goroutine — a panic there closes the connection, never the process.
	WSInbound func(c *WSConn, data []byte)
}

// Server is the running sidecar core. Construct with New; serve with Start;
// stop with Stop. Safe for concurrent use; Start is single-use — a second
// Start (or Start after Stop) errors instead of leaking a listener.
type Server struct {
	cfg           config.WebAccessConfig
	dispatch      any
	extraDispatch any
	static        fs.FS
	csp           func(http.Handler) http.Handler
	log           *slog.Logger

	authToken    string // effective auth token (configured or per-launch)
	sessionToken string // per-launch cookie secret, revealed only via login

	limiter *loginLimiter
	handler http.Handler
	tls     *tls.Config

	mu       sync.Mutex
	ln       net.Listener
	httpSrv  *http.Server
	stopOnce sync.Once
	stopped  chan struct{}
	// servingHosts snapshots the machine's serving addresses at Start
	// (net.InterfaceAddrs()) for the wildcard-bind Host gate (hostAllowed).
	// Written once under mu before the serve goroutine starts; nil until
	// then (fail-closed: with no snapshot only the loopback/localhost
	// structural cases pass on a wildcard bind).
	servingHosts []net.IP
	// dnsMu guards dnsCache — the bounded TTL cache of hostname→served
	// verdicts for the wildcard-bind Host gate (nameServes).
	dnsMu    sync.Mutex
	dnsCache map[string]dnsCacheEntry
	// started makes Start single-use: a second call — or after Stop — would
	// bind a listener this type never tracks. Misuse fails loudly.
	started atomic.Bool

	wsMu    sync.Mutex
	wsConns map[*WSConn]struct{}
	// wsReserved counts in-flight websocket slot reservations: a handshake
	// reserves capacity before Hijack (handleWS) and the reservation
	// converts into a registration or is released on every failure path.
	// The cap check is len(wsConns)+wsReserved >= maxWSConns, so a burst of
	// parallel handshakes cannot each see "one slot free" and all proceed.
	wsReserved int

	wsInbound func(c *WSConn, data []byte)
}

// New validates the options and builds the server. It does not bind anything
// until Start.
func New(opts Options) (*Server, error) {
	if !opts.Config.Enabled {
		return nil, ErrDisabled
	}
	if !config.IsValidWebAccessBind(opts.Config.Bind) {
		// Fail-closed: an invalid bind is a wiring bug, not an exposure
		// decision. Same two messages the config load/save paths produce.
		if opts.Config.Bind == "" {
			return nil, errors.New("webaccess: bind is required when enabled")
		}
		return nil, fmt.Errorf("webaccess: bind %q is not a valid IP literal or \"localhost\"", opts.Config.Bind)
	}
	if opts.Config.Port < 0 || opts.Config.Port > 65535 {
		return nil, fmt.Errorf("webaccess: port %d out of range (0 = auto)", opts.Config.Port)
	}
	// Insecure-LAN fail-closed backstop (the mount half of the exposure
	// gate — specs/domains/web-access.md): a non-loopback bind over plain
	// HTTP without the allow_insecure_lan opt-in is refused; inert under TLS.
	if err := config.ValidateWebAccessExposure(opts.Config); err != nil {
		return nil, err
	}
	// TLS termination (ADR-102): an enabled subsection MUST yield a readable
	// matching pair — New refuses otherwise, never a silent plaintext
	// downgrade (specs/domains/web-access.md). The empty-path state is
	// already reset fail-soft at config load.
	var tlsCfg *tls.Config
	if opts.Config.TLS.Enabled {
		if opts.Config.TLS.Cert == "" || opts.Config.TLS.Key == "" {
			return nil, errors.New("webaccess: tls is enabled but the cert/key paths are empty")
		}
		pair, err := tls.LoadX509KeyPair(opts.Config.TLS.Cert, opts.Config.TLS.Key)
		if err != nil {
			return nil, fmt.Errorf("webaccess: tls is enabled but the certificate pair is unreadable: %w", err)
		}
		tlsCfg = &tls.Config{
			// TLS 1.3 floor: mainstream browsers have required 1.3 since
			// ~2020-21, so the original 1.2 floor (ADR-102 decision 6) only
			// kept downgrade surface alive.
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{pair},
		}
	}
	if opts.Dispatch == nil {
		return nil, errors.New("webaccess: Dispatch target is required")
	}
	// Fail-closed on a dispatch target we cannot reflect over: a nil pointer
	// would resolve no methods and silently expose an empty surface.
	if v := reflect.ValueOf(opts.Dispatch); v.Kind() == reflect.Pointer && v.IsNil() {
		return nil, errors.New("webaccess: Dispatch target is a nil pointer")
	}
	// Same fail-closed rule for the optional extra surface: an explicitly
	// provided but nil extra target is a wiring bug, not an empty feature.
	if v := reflect.ValueOf(opts.ExtraDispatch); v.IsValid() && v.Kind() == reflect.Pointer && v.IsNil() {
		return nil, errors.New("webaccess: ExtraDispatch target is a nil pointer")
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	// Stage-2 posture: the non-loopback warning names the effective
	// transport so it stays truthful under web_access.tls (ADR-102);
	// exposure is about reachability, never encryption.
	if !config.IsLoopbackBind(opts.Config.Bind) {
		if tlsCfg != nil {
			log.Warn(fmt.Sprintf("webaccess: bind %q is not loopback — the listener is reachable beyond this machine, the transport is TLS-encrypted, the auth token is the only protection", opts.Config.Bind))
		} else {
			log.Warn(fmt.Sprintf("webaccess: bind %q is not loopback — the listener is reachable beyond this machine, traffic is plain HTTP without TLS (accepted via web_access.allow_insecure_lan), the auth token is the only protection", opts.Config.Bind))
		}
	}

	authToken := opts.Config.Token
	if authToken == "" {
		// Defense-in-depth fallback: the desktop layer generates and persists
		// the token before mounting, so an empty config token should not
		// reach New. A fresh per-launch secret is still better than none.
		gen, err := GenerateToken()
		if err != nil {
			return nil, fmt.Errorf("webaccess: generating per-launch token: %w", err)
		}
		authToken = gen
	} else if len(authToken) < 16 {
		// Operator-chosen weak secret: allowed (the operator owns the
		// risk — a short token is the weakest link already on loopback,
		// and the only protection on a non-loopback bind) but surfaced.
		log.Warn("webaccess: configured token is short; leave it empty to use the generated credential or use at least 16 characters")
	}
	sessionToken, err := randomHex(tokenBytes)
	if err != nil {
		return nil, fmt.Errorf("webaccess: generating session secret: %w", err)
	}

	s := &Server{
		cfg:           opts.Config,
		dispatch:      opts.Dispatch,
		extraDispatch: opts.ExtraDispatch,
		static:        opts.Static,
		csp:           opts.CSP,
		log:           log,
		authToken:     authToken,
		sessionToken:  sessionToken,
		limiter:       newLoginLimiter(loginMaxAttempts, loginWindow),
		stopped:       make(chan struct{}),
		wsConns:       make(map[*WSConn]struct{}),
		dnsCache:      make(map[string]dnsCacheEntry),
		wsInbound:     opts.WSInbound,
		tls:           tlsCfg,
	}
	s.handler = s.buildHandler()
	return s, nil
}

// buildHandler wires the route table. API handlers are recovered and emit
// no-store JSON; static assets go through the injected CSP middleware.
func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/transport", s.recoverWrap(noStore(s.handleTransport)))
	mux.Handle("POST /api/login", s.recoverWrap(noStore(s.sameOriginGate(armReadDeadline(s.handleLogin)))))
	mux.Handle("POST /api/rpc/{method}", s.recoverWrap(noStore(s.sameOriginGate(armReadDeadline(s.requireAuth(jsonContentTypeGate(s.handleRPC)))))))
	mux.Handle("GET /ws", s.recoverWrap(noStore(s.requireAuth(s.handleWS))))
	if s.static != nil {
		static := noDirListing(http.FileServerFS(s.static), s.static)
		if s.csp != nil {
			static = s.csp(static)
		}
		mux.Handle("/", static)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
	}
	// The outer recover is the last line of defense for every route.
	// securityHeaders sits INSIDE it (a panicking route cannot strip the
	// headers from the 500) and OUTSIDE everything else, covering API, /ws
	// and the static tree in one place.
	return s.recoverWrap(securityHeaders(mux))
}

// securityHeaders stamps the transport hardening headers onto every
// response: nosniff (a sniffed type must not turn a JSON error or an asset
// into executable content — defense-in-depth over CSP) and no-referrer
// (the sidecar origin is the operator's machine host:port — it must not
// leak into outbound Referer).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// armReadDeadline arms the per-request body-read deadline (see
// requestReadDeadline) on the body-carrying POST routes; the header phase
// stays under ReadHeaderTimeout. /ws is never armed: its hijacked
// connection must not inherit a read deadline.
func armReadDeadline(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Best-effort: a writer that cannot take a deadline leaves the
		// request under the listener's timeouts alone.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(requestReadDeadline)) //nolint:errcheck // best-effort bound
		next(w, r)
	}
}

// noDirListing makes a directory URL without an index.html answer 404 — the
// SPA file manifest is not something the sidecar hands out. Directories
// WITH an index.html (the SPA root) keep serving it, byte-identical.
func noDirListing(next http.Handler, fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "."
		}
		if d, err := fs.Stat(fsys, name); err == nil && d.IsDir() {
			if _, err := fs.Stat(fsys, path.Join(name, "index.html")); err != nil {
				http.NotFound(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Handler returns the fully wired HTTP handler (tests and embedding).
func (s *Server) Handler() http.Handler { return s.handler }

// AuthToken returns the effective auth token the browser login screen
// needs. The ONLY channel the token leaves the package through — never
// logged, never in a URL.
func (s *Server) AuthToken() string { return s.authToken }

// TLSEnabled reports whether this server terminates TLS (ADR-102); the
// desktop renders the browser-facing URL's scheme from it. Fixed at New —
// no synchronization beyond the constructor's happens-before edge.
func (s *Server) TLSEnabled() bool { return s.tls != nil }

// GenerateToken mints a fresh auth token (32 crypto/rand bytes as 64 hex
// chars). The desktop layer calls it when the persisted token is empty: the
// credential is created once and persisted (ADR-101), never re-minted per
// launch.
func GenerateToken() (string, error) {
	return randomHex(tokenBytes)
}

// Addr returns the resolved listen address after a successful Start
// (host:port; the port is the actual one when config port was 0/auto).
func (s *Server) Addr() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return "", false
	}
	return s.ln.Addr().String(), true
}

// Start binds the configured interface (resolving port 0 to a free port) and
// serves until Stop or ctx cancellation; TLS terminates on the same listener
// when the section enables it (ADR-102). It returns after the listener is
// bound; serve errors after that are logged, not returned. Start is
// single-use — a second Start (or Start after Stop) errors.
func (s *Server) Start(ctx context.Context) error {
	if !s.started.CompareAndSwap(false, true) {
		return errors.New("webaccess: server already started (Start is single-use)")
	}
	addr := net.JoinHostPort(s.cfg.Bind, strconv.Itoa(s.cfg.Port))
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("webaccess: listen on %s: %w", addr, err)
	}

	// Snapshot the serving addresses for the wildcard-bind Host gate
	// (hostAllowed → ipServed). Computed once per Start; an empty snapshot
	// on error keeps the gate fail-closed (loopback/localhost still pass).
	serving := servingHostSnapshot()
	s.mu.Lock()
	s.servingHosts = serving
	s.ln = ln
	// Pin HTTP/1.1-only: /ws is a Hijacker-based Upgrade handshake and cannot
	// survive HTTP/2, and automatic h2 would let browsers attempt
	// WebSocket-over-HTTP/2 (RFC 8441 extended CONNECT) for wss://. Go's h2
	// server currently rejects extended CONNECT, but that default is
	// explicitly temporary (GODEBUG http2xconnect, go issue #71128) — the
	// non-nil Protocols disables automatic h2 outright instead of riding it.
	// TestServer_TLSEndToEnd's ALPN assertion fails if this pin is removed.
	protos := new(http.Protocols)
	protos.SetHTTP1(true)     // the zero (empty) set denies every protocol
	s.httpSrv = &http.Server{ //nolint:gosec // local sidecar for the desktop UI; TLS opt-in via web_access.tls (ADR-102), floor 1.3 pinned in New; timeouts set below
		Handler:           s.handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		TLSConfig:         s.tls,
		Protocols:         protos,
		// Route the stdlib server's internal diagnostics (refused TLS
		// handshakes, protocol misuse) through the injected slog logger at
		// Debug: client-triggered at line rate, no operator value at the
		// default verbosity. Tests intercept these records at the source.
		ErrorLog: slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	srv := s.httpSrv
	s.mu.Unlock()

	s.log.Info("webaccess: listening", "addr", ln.Addr().String(), "tls", s.tls != nil)

	go func() {
		var serr error
		if s.tls != nil {
			// TLS termination on the same listener (ADR-102): the pair is
			// preloaded in New, so the file arguments stay empty and the key
			// material is never re-read on the serve path.
			serr = srv.ServeTLS(ln, "", "")
		} else {
			serr = srv.Serve(ln)
		}
		if serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			s.log.Error("webaccess: serve stopped", "err", serr)
		}
	}()
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				if serr := s.stop(shutdownBudget); serr != nil {
					s.log.Error("webaccess: context shutdown failed", "err", serr)
				}
			case <-s.stopped:
			}
		}()
	}
	return nil
}

// Stop gracefully shuts the listener down within ctx (or shutdownBudget when
// ctx has no deadline), then force-closes what remains — including hijacked
// WebSocket connections, which http.Server.Shutdown does not track.
func (s *Server) Stop(ctx context.Context) error {
	budget := shutdownBudget
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return s.stopWith(ctx)
	}
	sctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return s.stopWith(sctx)
}

func (s *Server) stop(budget time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	return s.stopWith(ctx)
}

func (s *Server) stopWith(ctx context.Context) (err error) {
	s.stopOnce.Do(func() {
		close(s.stopped)
		s.mu.Lock()
		srv := s.httpSrv
		ln := s.ln
		s.mu.Unlock()
		if srv != nil {
			if serr := srv.Shutdown(ctx); serr != nil {
				err = errors.Join(serr, srv.Close())
			}
		}
		if ln != nil {
			// Shutdown only closes listeners the serve goroutine has tracked;
			// a stop winning the race against Serve's first instruction
			// finds nothing tracked, Shutdown returns nil — and the raw
			// listener (the bound port) outlives the stop. This is the
			// port-freeing close in that window; once http.Server owns the
			// close it is an idempotent no-op.
			_ = ln.Close()
		}
		s.closeAllWS()
		s.log.Info("webaccess: stopped")
	})
	return err
}

// redactToken renders a secret safe for logs: a short SHA-256 prefix and the
// length — never the value itself.
func redactToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return fmt.Sprintf("sha256:%s(len=%d)", hex.EncodeToString(sum[:2]), len(secret))
}

// randomHex returns 2*n hex characters from crypto/rand.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}
