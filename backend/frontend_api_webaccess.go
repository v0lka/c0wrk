package backend

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/internal/webaccess"
)

// webAccessStopBudget bounds one sidecar stop — deliberately small: a Stop
// should never become the long pole of a shutdown or of a control RPC.
const webAccessStopBudget = 3 * time.Second

// WebAccessHooks carries the transport-owned pieces of the web-access
// sidecar (ADR-101/ADR-102) — the seams a GUI host binds to its own
// plumbing (the FrontendAPIConfig.EmitEvent precedent). The service itself
// is transport-neutral: a daemon host (epic #79) can own the same service
// with different — or nil — hooks. Production desktop wiring happens once
// in desktop NewApp, before wails.Run publishes any RPC goroutine.
type WebAccessHooks struct {
	// Config returns the live config object and its on-disk path (the
	// desktop liveConfig/liveConfigPath pair). Nil → the service behaves
	// exactly like the pre-buildFrontendAPI desktop: every control
	// operation reports "config not initialized".
	Config func() (*config.Config, string)
	// Logger returns the instance logger.
	Logger func() *slog.Logger
	// API returns the dispatch target the sidecar's reflection surface
	// resolves RPCs against (the FrontendAPI seed).
	API func() *FrontendAPI
	// ExtraDispatch returns the app-level method surface the mirrored UI
	// needs (the desktop webExtraSurface adapter) — never the App itself.
	// Nil hook → the mirrored surface is simply absent from the sidecar.
	ExtraDispatch func() any
	// WSInbound builds the inbound websocket dispatcher for one sidecar
	// lifetime (the desktop webWSInboundDispatcher adapter). Nil hook →
	// inbound WS messages are discarded (webaccess.Options semantics).
	WSInbound func(log *slog.Logger) func(*webaccess.WSConn, []byte)
	// CSP returns the Content-Security-Policy middleware stamped onto the
	// sidecar's HTTP handler (the desktop CSPMiddleware adapter). Nil hook
	// → no CSP middleware (webaccess.Options semantics).
	CSP func() func(http.Handler) http.Handler
	// SetEventTap installs (non-nil) or removes (nil) the side event tap
	// alongside the GUI's own event fan-out. Nil hook → no side tap.
	SetEventTap func(tap func(name string, args []interface{}))
}

// WebAccessService owns the web-access sidecar lifecycle (ADR-101/ADR-102):
// the live server handle, the transition lock, and every control operation —
// the section toggles, the connection/token/TLS pin writers with their
// hot-restart-and-rollback discipline, the startup mount and the bounded
// shutdown stop. Contract: specs/domains/web-access.md.
//
// The zero value is usable for reads and refusals: no sidecar running, and
// with hooks unset every control operation reports "config not initialized"
// — the same face the desktop presented before buildFrontendAPI. The
// transport-neutral core (config mutation, persist, restart/rollback) never
// sees a Wails type; the GUI-only pieces arrive through WebAccessHooks.
type WebAccessService struct {
	hooks WebAccessHooks

	// mu serializes the lifecycle TRANSITIONS (startup mount, Shutdown
	// stop, SetWebAccessEnabled toggle) so two of them can never interleave
	// into a half-torn-down server; handle reads ride the atomic pointer.
	// The section mutations persist the WHOLE config file while holding
	// only mu (the host cannot take the backend configMu — the
	// zero-backend-edit constraint), so a concurrent backend save can land
	// inside that window and the two atomic renames are last-writer-wins:
	// an older snapshot may drop a just-persisted backend mutation on disk
	// (memory is unaffected; the next backend save heals the file). Rare
	// (operator-paced toggles), self-healing, and accepted.
	mu sync.Mutex

	// server is the live sidecar; nil while not running. Atomic pointer:
	// Startup mounts it, Shutdown and SetWebAccessEnabled stop it,
	// Snapshot reads it — different goroutines.
	server atomic.Pointer[webaccess.Server]

	// static is the SPA asset tree the sidecar serves at /, wired from the
	// host strictly before any mount (the SetStaticFS path), so a plain
	// field is safely published. Nil (tests, a stripped embed) degrades the
	// sidecar to API+WS only: the static route 404s.
	static fs.FS
}

// Configure binds the transport hooks. Called once by the host before any
// mount or RPC can reach the service.
func (s *WebAccessService) Configure(hooks WebAccessHooks) {
	s.hooks = hooks
}

// configRef resolves the live config through the hook, nil-safe: an
// unconfigured service reads exactly like the pre-Init desktop (nil config).
func (s *WebAccessService) configRef() (cfg *config.Config, path string) {
	if s.hooks.Config == nil {
		return nil, ""
	}
	return s.hooks.Config()
}

// log resolves the instance logger, falling back to slog.Default() when the
// hook is unset or returns nil.
func (s *WebAccessService) log() *slog.Logger {
	if s.hooks.Logger == nil {
		return slog.Default()
	}
	if log := s.hooks.Logger(); log != nil {
		return log
	}
	return slog.Default()
}

// api resolves the dispatch target, nil-safe.
func (s *WebAccessService) api() *FrontendAPI {
	if s.hooks.API == nil {
		return nil
	}
	return s.hooks.API()
}

// SetStaticFS stores the SPA asset tree the sidecar serves. Exported for the
// host wiring (main.go, before wails.Run) — the webview's own asset server
// is unrelated.
func (s *WebAccessService) SetStaticFS(static fs.FS) {
	s.static = static
}

// Running reports whether a sidecar is live.
func (s *WebAccessService) Running() bool {
	return s.server.Load() != nil
}

// WebAccessSnapshot is the backend's whole-section view behind the
// frontend's GetWebAccessInfo payload: the Settings section's view of the
// sidecar (specs/domains/web-access.md, the WebAccessInfo table). Plain
// data — the wire DTO with its JSON names stays in the desktop binding
// layer, and the two structs stay field-identical for the direct conversion.
type WebAccessSnapshot struct {
	Enabled                  bool
	Running                  bool
	URL                      string
	Port                     int
	ConfiguredPort           int
	ConfiguredBind           string
	BindIsLoopback           bool
	AllowInsecureLAN         bool
	AllowInsecureLANRequired bool
	Token                    string
	TokenPinned              bool
	TLSEnabled               bool
	TLSCert                  string
	TLSKey                   string
	CertStatus               string
	CertDetail               string
}

// Snapshot reports the sidecar state for Settings → Web Access. The section
// read is a whole-struct snapshot under mu — the same lock the control
// writers mutate it under, and the renderer-side read races them on real
// paths; Token is a multi-word string header whose torn read is UB-class,
// so the fields are copied once, never walked field-by-field off the live
// config. Classification and URL rendering stay OUTSIDE the lock on
// purpose — no file I/O rides the transition lock (ADR-102).
func (s *WebAccessService) Snapshot() WebAccessSnapshot {
	snap := WebAccessSnapshot{}
	var sec config.WebAccessConfig
	if cfg, _ := s.configRef(); cfg != nil {
		s.mu.Lock()
		sec = cfg.WebAccess
		s.mu.Unlock()
		snap.Enabled = sec.Enabled
		snap.ConfiguredBind = sec.Bind
		snap.BindIsLoopback = config.IsLoopbackBind(sec.Bind)
		snap.AllowInsecureLAN = sec.AllowInsecureLAN
		// State-only hint: would these pins be refused if the section were
		// enabled? (ValidateWebAccessExposure over the snapshot with Enabled
		// forced true — the same predicate every boundary applies.)
		asEnabled := sec
		asEnabled.Enabled = true
		snap.AllowInsecureLANRequired = config.ValidateWebAccessExposure(asEnabled) != nil
		snap.ConfiguredPort = sec.Port
		snap.TokenPinned = sec.Token != ""
		snap.TLSEnabled = sec.TLS.Enabled
		snap.TLSCert = sec.TLS.Cert
		snap.TLSKey = sec.TLS.Key
		snap.CertStatus, snap.CertDetail = config.ClassifyWebAccessCert(
			sec.TLS.Cert, sec.TLS.Key, sec.Bind)
	}
	if srv := s.server.Load(); srv != nil {
		snap.Running = true
		if addr, ok := srv.Addr(); ok {
			snap.URL = config.WebAccessBrowserURL(addr, srv.TLSEnabled())
			if _, port, perr := net.SplitHostPort(addr); perr == nil {
				snap.Port, _ = strconv.Atoi(port)
			}
		}
		snap.Token = srv.AuthToken()
	} else {
		// Not running: the configured token is what the next start would
		// use (an empty value means that start mints and persists one).
		// Same snapshot as above — a second configRef read here could
		// interleave a writer between the two reads.
		snap.Token = sec.Token
	}
	return snap
}

// The warning-channel refresh (ADR-102) stays with the desktop binding
// adapters: transport-affecting control RPCs recompute the section's
// config-load-warning contribution AFTER the service call returns (the
// transition lock is released inside the service), outside the lock on
// purpose, and the refresh is idempotent so it also runs after refusals and
// rollbacks. The token RPCs deliberately do not refresh: the credential
// cannot move the warning state.

// SetEnabled persists the web_access.enabled switch and applies it live:
// enabling starts the sidecar, disabling stops it. Same-state is a no-op; a
// failed disk write rolls the in-memory flip back
// (specs/domains/web-access.md). Enabling preflights a configured-but-
// unusable TLS pair fail-closed (ADR-102), and with the master experimental
// gate closed it persists operator intent but mounts nothing (ADR-101
// decision 13). Locking: mu serializes the whole transition against the
// startup mount and the Shutdown stop; the config write is the host-owned
// whole-file window documented on the service struct.
func (s *WebAccessService) SetEnabled(on bool) error {
	s.mu.Lock()
	err := s.setWebAccessEnabledLocked(on)
	s.mu.Unlock()
	return err
}

func (s *WebAccessService) setWebAccessEnabledLocked(on bool) error {
	cfg, cfgPath := s.configRef()
	if cfg == nil || cfgPath == "" {
		return errors.New("config not initialized")
	}
	if cfg.WebAccess.Enabled == on {
		return nil
	}

	// ADR-102: a flip onto an unusable TLS is refused up front — fail-closed
	// at this interactive boundary.
	if on && cfg.WebAccess.TLS.Enabled {
		if err := config.ValidateWebAccessTLSPair(cfg.WebAccess.TLS); err != nil {
			return fmt.Errorf("cannot enable web access: %w", err)
		}
	}
	// Insecure-LAN gate: enabling onto a non-loopback plaintext bind without
	// the explicit allow_insecure_lan opt-in is refused up front — the same
	// exposure predicate the load, save and mount paths enforce
	// (specs/domains/web-access.md).
	if on {
		next := cfg.WebAccess
		next.Enabled = true
		if err := config.ValidateWebAccessExposure(next); err != nil {
			return fmt.Errorf("cannot enable web access: %w", err)
		}
	}

	prev := cfg.WebAccess
	cfg.WebAccess.Enabled = on
	if err := config.Save(cfg, cfgPath); err != nil {
		cfg.WebAccess = prev
		return fmt.Errorf("failed to persist web access config: %w", err)
	}

	if on {
		if !webAccessMountAllowed(cfg, cfg.Experimental.Enabled) {
			// ADR-101 decision 13: with the master gate closed the toggle
			// still persists operator intent (the same paper state a
			// restart would produce) but mounts no sidecar; opening the
			// gate live-follows through ApplyExperimental.
			s.log().Debug("web access enabled while the experimental gate is closed; the sidecar stays unmounted",
				"experimental_enabled", cfg.Experimental.Enabled)
			return nil
		}
		srv, err := s.startWebAccessLocked(s.log())
		if err != nil {
			// The persisted switch stays on (operator intent; the next
			// launch retries) but the failure must be visible now.
			return fmt.Errorf("web access enabled, but the sidecar failed to start: %w", err)
		}
		logWebAccessReady(s.log(), srv)
		return nil
	}

	sctx, cancel := context.WithTimeout(context.Background(), webAccessStopBudget)
	defer cancel()
	if err := s.stopWebAccessLocked(sctx); err != nil {
		return fmt.Errorf("web access disabled, but the sidecar stop failed: %w", err)
	}
	return nil
}

// ApplyExperimental is the backend's post-persist hook for
// experimental.enabled (UpdateExperimentalFeatures → the OnExperimentalChanged
// FrontendAPIConfig seam, ADR-101): gate open mounts an enabled-and-stopped
// sidecar, gate closed stops a running one (webAccessStopBudget), everything
// else is a no-op (specs/domains/web-access.md). Failure is fail-soft and
// loud exactly like the startup mount: an Error log, the host app fully
// usable, the persisted switch retrying on the next launch or successful
// toggle. Locking: mu serializes the whole transition against the startup
// mount, the Shutdown stop and the control RPCs; no lock-order cycle with
// the caller — UpdateExperimentalFeatures holds configMu while this takes
// mu, and no service code path takes configMu.
func (s *WebAccessService) ApplyExperimental(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, _ := s.configRef()
	if cfg == nil {
		// A startup that never reached the seed init: nothing to mount,
		// and no live config to read the section's own switch from.
		return
	}

	switch {
	case webAccessMountAllowed(cfg, enabled):
		srv, err := s.startWebAccessLocked(s.log())
		if err != nil {
			s.log().Error("web access sidecar failed to start after the experimental gate opened; continuing without it", "error", err)
			return
		}
		// Idempotent on a live server: a repeated open returns the running
		// sidecar instead of double-binding, so this is quiet for that case.
		logWebAccessReady(s.log(), srv)
	case !enabled:
		if s.server.Load() == nil {
			return
		}
		sctx, cancel := context.WithTimeout(context.Background(), webAccessStopBudget)
		defer cancel()
		if err := s.stopWebAccessLocked(sctx); err != nil {
			s.log().Error("web access sidecar failed to stop after the experimental gate closed", "error", err)
		}
	}
}

// SetConnection persists the pinned bind and port (Settings → Web Access:
// the single address field parses into these two) and applies them live — a
// running sidecar hot-restarts onto the new connection. The token is
// deliberately NOT part of this call: the credential has its own RPCs, all
// serialized by mu. Empty bind = the loopback default; port 0 = auto,
// 1–65535 = pinned; a same-pin request is a no-op
// (specs/domains/web-access.md). If the restart with the new settings fails,
// the previous pin is restored, persisted, and the sidecar brought back on
// it (see restartWebAccessOrRollbackLocked). Locking: mu, like SetEnabled.
func (s *WebAccessService) SetConnection(bind string, port int) error {
	s.mu.Lock()
	err := s.setWebAccessConnectionLocked(bind, port)
	s.mu.Unlock()
	return err
}

func (s *WebAccessService) setWebAccessConnectionLocked(bind string, port int) error {
	cfg, cfgPath := s.configRef()
	if cfg == nil || cfgPath == "" {
		return errors.New("config not initialized")
	}
	bind = strings.TrimSpace(bind)
	if bind == "" {
		bind = config.DefaultWebAccessBind
	}
	if err := config.ValidateWebAccessBind(bind); err != nil {
		return err
	}
	if err := config.ValidateWebAccessPort(port); err != nil {
		return err
	}
	// Insecure-LAN gate on the re-bind boundary: refused before anything
	// mutates or persists so the caller's error is a single honest step
	// instead of a failed restart rollback. A disabled section keeps the
	// paper-state doctrine: the pin persists regardless and the exposure
	// gate fires at the enable boundary (and at load/save).
	next := cfg.WebAccess
	next.Bind = bind
	next.Port = port
	if err := config.ValidateWebAccessExposure(next); err != nil {
		return err
	}

	if cfg.WebAccess.Bind == bind && cfg.WebAccess.Port == port {
		return nil
	}

	prev := cfg.WebAccess
	cfg.WebAccess.Bind = bind
	cfg.WebAccess.Port = port
	if err := config.Save(cfg, cfgPath); err != nil {
		cfg.WebAccess = prev
		return fmt.Errorf("failed to persist web access config: %w", err)
	}
	return s.restartWebAccessOrRollbackLocked(prev, cfg, cfgPath, "connection")
}

// RegenerateToken mints a fresh persistent credential (the Settings →
// Web Access button) and hot-restarts a running sidecar onto it — every
// open browser tab must sign in again with the token Settings now shows.
// With the sidecar stopped the persisted write is the whole effect. Failure
// ordering and locking discipline: see restartWebAccessOrRollbackLocked and
// specs/domains/web-access.md (mu).
func (s *WebAccessService) RegenerateToken() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, cfgPath := s.configRef()
	if cfg == nil || cfgPath == "" {
		return errors.New("config not initialized")
	}
	tok, err := webaccess.GenerateToken()
	if err != nil {
		return fmt.Errorf("web access: generating auth token: %w", err)
	}

	prev := cfg.WebAccess
	cfg.WebAccess.Token = tok
	if err := config.Save(cfg, cfgPath); err != nil {
		cfg.WebAccess = prev
		return fmt.Errorf("failed to persist web access config: %w", err)
	}
	return s.restartWebAccessOrRollbackLocked(prev, cfg, cfgPath, "token regeneration")
}

// SetToken persists an operator-typed credential (the Settings → Web Access
// token field) and applies it live exactly like the rotation above. The
// value is trimmed and bounds-checked by config.ValidateWebAccessToken; the
// empty value is rejected — emptiness means GENERATED, and clearing the
// field reverts the UI instead of overwriting an existing credential. A
// request matching the stored token is a no-op; failure ordering and locking
// mirror RegenerateToken.
func (s *WebAccessService) SetToken(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, cfgPath := s.configRef()
	if cfg == nil || cfgPath == "" {
		return errors.New("config not initialized")
	}
	token = strings.TrimSpace(token)
	if token == cfg.WebAccess.Token {
		return nil
	}
	if err := config.ValidateWebAccessToken(token); err != nil {
		return err
	}

	prev := cfg.WebAccess
	cfg.WebAccess.Token = token
	if err := config.Save(cfg, cfgPath); err != nil {
		cfg.WebAccess = prev
		return fmt.Errorf("failed to persist web access config: %w", err)
	}
	return s.restartWebAccessOrRollbackLocked(prev, cfg, cfgPath, "token change")
}

// SetTLS persists the web_access.tls subsection (the HTTPS switch, ADR-102
// decision 1) and applies it live: a running sidecar is hot-restarted onto
// the new transport. The caller authors both PEM paths explicitly — the key
// path is never derived from the cert path. An enabled subsection with empty
// paths is refused (config.ValidateWebAccessTLS), and a non-empty pair is
// loaded BEFORE the no-op check or any persist — the interactive boundary of
// the ADR-102 usability rule, with the commit check's own two-bucket error
// split (see setWebAccessTLSLocked); a broken pair never reaches disk or the
// sidecar. The subsection deliberately consults neither web_access.enabled
// nor the experimental gate. Failure ordering mirrors the other pin writers
// (persist rollback, restart restore); locking: mu.
func (s *WebAccessService) SetTLS(enabled bool, cert, key string) error {
	s.mu.Lock()
	err := s.setWebAccessTLSLocked(enabled, cert, key)
	s.mu.Unlock()
	return err
}

func (s *WebAccessService) setWebAccessTLSLocked(enabled bool, cert, key string) error {
	cfg, cfgPath := s.configRef()
	if cfg == nil || cfgPath == "" {
		return errors.New("config not initialized")
	}
	cert = strings.TrimSpace(cert)
	key = strings.TrimSpace(key)
	if cert != "" && key != "" {
		// Refuse an unloadable pair before the no-op check or any persist,
		// with the commit check's own two-bucket error split (NOT the
		// cert_status vocabulary — see config.ClassifyWebAccessCert): a key
		// that does not match its certificate is a mismatch ("does not
		// match"), every other load failure an unreadable pair.
		if _, err := tls.LoadX509KeyPair(cert, key); err != nil {
			if strings.Contains(err.Error(), "does not match") {
				return fmt.Errorf("web access: TLS private key %q does not match the certificate %q: %w", key, cert, err)
			}
			return fmt.Errorf("web access: TLS certificate/key pair (%q, %q) is unreadable: %w", cert, key, err)
		}
	}
	next := config.WebAccessTLSConfig{Enabled: enabled, Cert: cert, Key: key}
	if err := config.ValidateWebAccessTLS(next); err != nil {
		return err
	}
	// Insecure-LAN gate on the TLS-off boundary: the transport must not
	// silently become a plaintext LAN exposure through the very switch that
	// was added to encrypt it; refusing here keeps the caller's error one
	// honest step instead of a failed restart rollback.
	exposureNext := cfg.WebAccess
	exposureNext.TLS = next
	if err := config.ValidateWebAccessExposure(exposureNext); err != nil {
		return err
	}
	if cfg.WebAccess.TLS == next {
		return nil
	}

	prev := cfg.WebAccess
	cfg.WebAccess.TLS = next
	if err := config.Save(cfg, cfgPath); err != nil {
		cfg.WebAccess.TLS = prev.TLS
		return fmt.Errorf("failed to persist web access config: %w", err)
	}
	return s.restartWebAccessOrRollbackLocked(prev, cfg, cfgPath, "TLS switch")
}

// SetAllowInsecureLAN persists the explicit accepted-risk opt-in for the one
// exposure shape refused by default: an enabled, non-loopback bind served
// over plain HTTP (specs/domains/web-access.md — the exposure gate the
// load/save paths and the sidecar mount enforce). Flag-off while the section
// sits in that shape is refused — the operator must first disable web
// access, re-bind loopback, or enable TLS; a disabled section's flag is pure
// paper state and flips freely. The flag never changes a RUNNING listener
// (transport and bind untouched — no hot-restart). Locking: mu, exactly
// like SetTLS.
func (s *WebAccessService) SetAllowInsecureLAN(allow bool) error {
	s.mu.Lock()
	err := s.setWebAccessAllowInsecureLANLocked(allow)
	s.mu.Unlock()
	return err
}

func (s *WebAccessService) setWebAccessAllowInsecureLANLocked(allow bool) error {
	cfg, cfgPath := s.configRef()
	if cfg == nil || cfgPath == "" {
		return errors.New("config not initialized")
	}
	if cfg.WebAccess.AllowInsecureLAN == allow {
		return nil
	}
	next := cfg.WebAccess
	next.AllowInsecureLAN = allow
	if err := config.ValidateWebAccessExposure(next); err != nil {
		return err
	}
	prev := cfg.WebAccess
	cfg.WebAccess.AllowInsecureLAN = allow
	if err := config.Save(cfg, cfgPath); err != nil {
		cfg.WebAccess = prev
		return fmt.Errorf("failed to persist web access config: %w", err)
	}
	return nil
}

// GenerateCertificate regenerates the app-owned self-signed pair for the
// CURRENTLY pinned bind (ADR-102 decision 3) at the fixed locations
// ~/.c0wrk/web-access.crt + .key, persists the pair's paths into
// web_access.tls (never flipping tls.enabled, never touching
// operator-authored paths), and hot-restarts a running sidecar onto the
// fresh certificate. Works while the sidecar is stopped too. The files are
// written BEFORE the config save — the reverse would persist paths at files
// that do not exist yet; leftover files on a failed persist are harmless
// (the idempotent standard pair nothing references until the paths persist).
// Failure ordering mirrors the other control RPCs; locking: mu.
func (s *WebAccessService) GenerateCertificate() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, cfgPath := s.configRef()
	if cfg == nil || cfgPath == "" {
		return errors.New("config not initialized")
	}
	agentDir := config.AgentDir()
	certPath := config.WebAccessCertPath(agentDir)
	keyPath := config.WebAccessKeyPath(agentDir)
	if err := webaccess.GenerateWebAccessCertificate(cfg.WebAccess.Bind, certPath, keyPath); err != nil {
		return err
	}

	prev := cfg.WebAccess
	next := config.WebAccessTLSConfig{
		Enabled: cfg.WebAccess.TLS.Enabled,
		Cert:    certPath,
		Key:     keyPath,
	}
	if cfg.WebAccess.TLS != next {
		cfg.WebAccess.TLS = next
		if err := config.Save(cfg, cfgPath); err != nil {
			cfg.WebAccess.TLS = prev.TLS
			return fmt.Errorf("failed to persist web access config: %w", err)
		}
	}
	// Hot-restart even when the persisted paths did not change: regeneration
	// replaced the key material on disk, and a running sidecar keeps serving
	// the pair it loaded at start until it is rebuilt.
	return s.restartWebAccessOrRollbackLocked(prev, cfg, cfgPath, "certificate generation")
}

// restartWebAccessOrRollbackLocked hot-restarts a running sidecar onto the
// just-persisted config state; a no-op when the sidecar is not running (the
// persisted write was the whole effect). If the restart cannot serve, prev —
// the section state the sidecar was running with — is restored in memory,
// persisted, and the sidecar brought back on it. Every failure, including a
// failed rollback persist, is named in the returned error: the operator is
// never told the previous settings were restored while disk still holds the
// pin that failed to serve. Callers hold mu and have already persisted the
// new state into cfg; `what` names the change in errors.
func (s *WebAccessService) restartWebAccessOrRollbackLocked(prev config.WebAccessConfig, cfg *config.Config, cfgPath, what string) error {
	if s.server.Load() == nil {
		return nil
	}

	// Hot-restart onto the new state. The stop's error is deliberately not
	// fatal: the start below needs the listener gone, and a failed stop
	// surfaces through the start failure — the rollback path then restores
	// the previous state.
	sctx, cancel := context.WithTimeout(context.Background(), webAccessStopBudget)
	defer cancel()
	_ = s.stopWebAccessLocked(sctx)
	srv, err := s.startWebAccessLocked(s.log())
	if err == nil {
		logWebAccessReady(s.log(), srv)
		return nil
	}

	// The new state does not serve. Restore the previous one on disk and
	// bring the sidecar back up on it; every failure is named to the caller.
	cfg.WebAccess = prev
	saveErr := config.Save(cfg, cfgPath)
	if saveErr != nil {
		s.log().Error("web access: failed to persist the rolled-back settings", "err", saveErr)
	}
	restored, restartErr := s.startWebAccessLocked(s.log())
	if restartErr != nil {
		if saveErr != nil {
			return fmt.Errorf(
				"web access %s not applied: the sidecar failed to start with the new settings (%w) and could not be restored on the previous ones (%w); persisting the rolled-back settings also failed (%w) — disk still holds the settings that failed to serve, and the sidecar is down until the next launch",
				what, err, restartErr, saveErr)
		}
		return fmt.Errorf(
			"web access %s not applied: the sidecar failed to start with the new settings (%w) and could not be restored on the previous ones (%w) — it will retry on the next launch",
			what, err, restartErr)
	}
	logWebAccessReady(s.log(), restored)
	if saveErr != nil {
		return fmt.Errorf(
			"web access %s not applied: the sidecar failed to start with the new settings (%w); the sidecar is running again on the previous settings, but persisting the rollback failed (%w) — disk still holds the settings that failed to serve and will retry them on the next launch",
			what, err, saveErr)
	}
	return fmt.Errorf(
		"web access %s not applied: the sidecar failed to start with the new settings (%w); the previous settings were restored and the sidecar is running again",
		what, err)
}

// webAccessMountAllowed is the ADR-101 decision-13 availability conjunction —
// the master experimental gate AND the feature's own web_access switch — as
// ONE predicate for every mount decision: the startup mount, the live-follow
// hook and the section toggle. experimentalEnabled is passed explicitly
// because the live-follow hook carries the post-toggle gate value the
// in-memory config has only just converged to.
func webAccessMountAllowed(cfg *config.Config, experimentalEnabled bool) bool {
	return cfg != nil && experimentalEnabled && cfg.WebAccess.Enabled
}

// WebAccessMountAllowed exposes webAccessMountAllowed to the host layer: the
// ADR-101 decision-13 mount conjunction, for the startup phase's gate log.
func WebAccessMountAllowed(cfg *config.Config, experimentalEnabled bool) bool {
	return webAccessMountAllowed(cfg, experimentalEnabled)
}

// MountWebAccess mounts the sidecar at startup: the gate-closed case is the
// caller's decision (webAccessMountAllowed), everything under the transition
// lock happens here — the idempotent start plus the ready log. Returning
// nil means the sidecar is live and its URL has been logged.
func (s *WebAccessService) MountWebAccess(log *slog.Logger) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	srv, err := s.startWebAccessLocked(log)
	if err != nil {
		return err
	}
	logWebAccessReady(log, srv)
	return nil
}

// StopForShutdown stops the sidecar during Shutdown (behind the transition
// lock, so it cannot interleave with a SetEnabled transition) before the
// judge drain and store close — a connected tab sees its stream end while
// the app winds down. webAccessStopBudget (~3s) converts a wedged graceful
// Shutdown into Server.Stop's force close; a failure is returned for the
// caller to log, never fatal (see specs/domains/web-access.md).
func (s *WebAccessService) StopForShutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sctx, cancel := context.WithTimeout(ctx, webAccessStopBudget)
	defer cancel()
	return s.stopWebAccessLocked(sctx)
}

// startWebAccessLocked constructs and starts the sidecar and publishes the
// handle + event tap. Callers must hold mu. Idempotent on a live server: it
// returns the running one so a startup mount and a toggle can never
// double-bind.
func (s *WebAccessService) startWebAccessLocked(log *slog.Logger) (*webaccess.Server, error) {
	if cur := s.server.Load(); cur != nil {
		return cur, nil
	}
	cfg, cfgPath := s.configRef()
	if cfg == nil || cfgPath == "" {
		return nil, errors.New("config not initialized")
	}
	// The persistent credential: an empty config token means "mint one, now"
	// — generated once and written back immediately (ADR-101), so the
	// credential is stable across restarts from the first start on.
	if cfg.WebAccess.Token == "" {
		tok, err := webaccess.GenerateToken()
		if err != nil {
			return nil, fmt.Errorf("web access: generating auth token: %w", err)
		}
		prev := cfg.WebAccess
		cfg.WebAccess.Token = tok
		if err := config.Save(cfg, cfgPath); err != nil {
			cfg.WebAccess = prev
			return nil, fmt.Errorf("web access: failed to persist the generated auth token: %w", err)
		}
		log.Info("web access: generated a persistent auth token (the config token was empty)")
	}
	var wsInbound func(*webaccess.WSConn, []byte)
	if s.hooks.WSInbound != nil {
		wsInbound = s.hooks.WSInbound(log)
	}
	var extraDispatch any
	if s.hooks.ExtraDispatch != nil {
		extraDispatch = s.hooks.ExtraDispatch()
	}
	var csp func(http.Handler) http.Handler
	if s.hooks.CSP != nil {
		csp = s.hooks.CSP()
	}
	srv, err := webaccess.New(webaccess.Options{
		Config:   cfg.WebAccess,
		Dispatch: s.api(),
		// Host-level methods the mirrored UI needs, behind the injected
		// adapter (desktop webExtraSurface) — never the host app itself.
		ExtraDispatch: extraDispatch,
		Static:        s.static,
		CSP:           csp,
		Logger:        log,
		// The browser-side twin of the GUI's event listeners: an
		// authenticated tab resolves the whitelisted HITL response events
		// through the same handlers the Wails runtime path uses (desktop
		// webaccess_inbound.go).
		WSInbound: wsInbound,
	})
	if err != nil {
		return nil, err
	}
	if err := srv.Start(context.Background()); err != nil {
		return nil, err
	}
	// Publish the handle before the tap: once events fan out, a concurrent
	// Snapshot must already see Running.
	s.server.Store(srv)
	if s.hooks.SetEventTap != nil {
		s.hooks.SetEventTap(srv.EventTap())
	}
	return srv, nil
}

// stopWebAccessLocked detaches and stops the live sidecar. Callers must hold
// mu. The handle and tap are cleared BEFORE the stop so events stop flowing
// to a dying server and a concurrent toggle sees "not running" immediately;
// the stop itself is idempotent (Server.Stop is once-guarded). A nil handle
// is a no-op.
func (s *WebAccessService) stopWebAccessLocked(ctx context.Context) error {
	srv := s.server.Load()
	if srv == nil {
		return nil
	}
	s.server.Store(nil)
	if s.hooks.SetEventTap != nil {
		s.hooks.SetEventTap(nil)
	}
	return srv.Stop(ctx)
}

// logWebAccessReady records the sidecar's URL at Info. The auth token is
// deliberately NOT part of the record — it is app-secret material the
// Settings section reveals on demand; the log only names where a browser
// connects.
func logWebAccessReady(log *slog.Logger, srv *webaccess.Server) {
	addr, ok := srv.Addr()
	if !ok {
		addr = "(unresolved)"
	}
	log.Info("web access sidecar ready", "url", config.WebAccessBrowserURL(addr, srv.TLSEnabled()),
		"token", "(hidden — see Settings → Web Access)")
}
