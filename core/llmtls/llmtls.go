// Package llmtls provides per-provider TLS verification overrides for
// self-signed LLM endpoints. The host application (c0wrk) builds a pinned
// *http.Client from a provider's tls_fingerprint config; the TLS policy
// stays entirely in this layer — the SDK transports whatever client it is
// handed (llm.ProviderEntry.HTTPClient).
//
// Semantics (per ADR-052, "the pin is the switch"):
//
//	no pin (empty)  → system verification; base is used unchanged
//	pin set         → accept ONLY the certificate whose
//	                  SubjectPublicKeyInfo hashes to the pin
//
// The pin format is base64(StdEncoding, SHA-256(SPKI DER)) — the same
// construction as Chromium's CertificatePinList / RFC 7469 — so a pin
// survives certificate renewal as long as the key pair is reused. Pin
// comparison is whitespace-tolerant.
//
// There is deliberately no "accept any certificate" mode: an override
// exists only when a pin exists, so a misconfigured provider fails closed
// (ErrPinMismatch) instead of silently disabling verification entirely.
//
// A configured HTTP proxy always wins over the pin (ADR-052): the proxy is a
// global network policy that predates the per-provider trust decision, and a
// MITM proxy re-encrypts traffic with its own certificate, so a pin layered
// on top would reject a legitimately configured setup. The two resolvers
// below encode that rule for the two kinds of dial path this application
// has; see their doc comments for why one resolver cannot serve both.
package llmtls

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// FetchFingerprintTimeout bounds the TLS handshake performed by
// FetchFingerprint.
const FetchFingerprintTimeout = 10 * time.Second

// ErrPinMismatch is returned (wrapped) when the peer certificate's SPKI hash
// does not match the configured pin. The error deliberately carries no
// certificate material — only the fact of the mismatch (see ADR-052).
var ErrPinMismatch = errors.New("fingerprint mismatch")

// Client returns an HTTP client for one provider endpoint.
//
//   - fingerprint "": base is returned unchanged (same pointer); system
//     certificate verification applies.
//   - fingerprint non-empty: a clone of base whose transport accepts only
//     the certificate whose SPKI SHA-256 (base64) equals the fingerprint.
//
// The pin IS the switch — there is no separate skip-verification knob and
// no "accept any certificate" state: an empty pin means "no override".
//
// base is never mutated; when base is nil a fresh client with the package
// default timeout is used as the starting point. The clone keeps base's
// Timeout and derives its transport from base's transport, so a caller that
// passes the shared LLM client gets the pin AND that client's request
// timeout. logger (may be nil) receives a Warn when base carries a custom
// RoundTripper that cannot hold a tls.Config and is therefore replaced.
func Client(base *http.Client, fingerprint string, logger *slog.Logger) *http.Client {
	pin := normalizePin(fingerprint)
	if pin == "" {
		return base
	}

	warnInvalidPin(pin, logger)

	start := base
	if start == nil {
		start = &http.Client{Timeout: 10 * time.Minute}
	}
	clone := *start
	clone.Transport = pinnedTransport(start.Transport, pin, logger)
	return &clone
}

// RouterEntryClient resolves the per-provider client override for an
// llm.ProviderEntry — the chat/inference dial path.
//
//	proxyActive             → nil (the pin is deliberately ignored)
//	!proxyActive, pin == "" → nil
//	!proxyActive, pin != "" → Client(base, pin, logger)
//
// nil means "this entry carries no override", which makes the SDK fall back
// to the router-level client (llm.RouterConfig.HTTPClient). That fallback is
// the whole reason this resolver returns nil rather than the proxy client
// while a proxy is active: the router-level client already carries the proxy
// transport AND the long LLM request timeout, whereas the raw proxy client
// is built with the much shorter web-fetch proxy timeout. Handing the proxy
// client back here would shadow the router-level client and silently cap
// every inference request at that shorter timeout — long enough to break
// reasoning models. Use DirectDialClient on paths that have no such
// fallback.
//
// base is the shared LLM client whose timeout a pinned client must inherit.
func RouterEntryClient(proxyActive bool, base *http.Client, fingerprint string, logger *slog.Logger) *http.Client {
	if proxyActive {
		return nil
	}
	if pin := normalizePin(fingerprint); pin != "" {
		return Client(base, pin, logger)
	}
	return nil
}

// DirectDialClient resolves the client for a dial path that has no
// router-level fallback: the Fetch Models listing and the lazy
// context-window probe, both of which bound their own requests with a
// context deadline.
//
//	proxyClient != nil             → proxyClient, unchanged (pin ignored)
//	proxyClient == nil, pin == ""  → nil (SDK/default transport, system
//	                                 verification, no proxy)
//	proxyClient == nil, pin != ""  → Client(nil, pin, logger): a fresh
//	                                 direct client pinned to the SPKI
//
// The proxy branch returns the proxy client verbatim — no clone, no derived
// transport — so exactly the pre-pin behavior is restored whenever a proxy
// is active. Unlike RouterEntryClient, returning the proxy client here is
// correct: there is no shared client behind these calls, so nil would mean
// "dial directly" and quietly bypass the operator's routing policy.
func DirectDialClient(proxyClient *http.Client, fingerprint string, logger *slog.Logger) *http.Client {
	if proxyClient != nil {
		return proxyClient
	}
	if pin := normalizePin(fingerprint); pin != "" {
		return Client(nil, pin, logger)
	}
	return nil
}

// warnInvalidPin logs a Warn when pin is non-empty but cannot be the base64
// (standard encoding) form of a 32-byte SHA-256 digest — the only well-formed
// pin format this package accepts (ADR-052). A malformed pin still flows
// through to the handshake, where it fails with ErrPinMismatch regardless;
// the Warn exists so a typo'd paste (hex characters, missing "=" padding, a
// truncated value) is distinguishable in the log from a genuine server key
// rotation. Diagnostic only — never blocks the request path.
func warnInvalidPin(pin string, logger *slog.Logger) {
	if pin == "" || logger == nil {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(pin)
	if err != nil || len(raw) != sha256.Size {
		logger.Warn("llmtls: configured TLS pin is not base64(SHA-256) — it can never match any certificate; check for a copy/paste error (hex encoding, missing padding, or truncation)",
			"pin_length", len(pin))
	}
}

// pinnedTransport derives an *http.Transport carrying the TLS pinning
// configuration from base (may be nil → default transport). pin is already
// normalized and MUST be non-empty (Client returns base unchanged for an
// empty pin, so the any-certificate state is unreachable); verification is
// pin-only: system verification is replaced by the SPKI comparison. The
// result is a concrete *http.Transport — not a RoundTripper wrapper — so it
// satisfies the stdlib's closeIdler interface and http.Client.CloseIdleConnections
// actually reaches the connection pool of the derived client.
func pinnedTransport(base http.RoundTripper, pin string, logger *slog.Logger) *http.Transport {
	tlsCfg := &tls.Config{
		// System verification is replaced by our own check below; the
		// #nosec comment documents that this is the deliberate,
		// user-opted-in bypass (gosec G402).
		InsecureSkipVerify:    true, // #nosec G402 -- user-opted per-provider override
		MinVersion:            tls.VersionTLS12,
		VerifyPeerCertificate: pinVerifier(pin),
	}

	return transportWithTLS(base, tlsCfg, logger)
}

// transportWithTLS clones base into a private *http.Transport whose
// TLSClientConfig is tlsCfg. The clone happens ONCE per derived client, not
// per request: http.Transport.Clone() does not carry over the idle-connection
// pool, so cloning inside RoundTrip would drop every keep-alive connection
// and force a fresh TCP+TLS handshake per request. Custom (non-*http.Transport)
// RoundTrippers cannot hold a tls.Config; they are replaced by a
// default-transport clone with a Warn on logger (when non-nil) — retry or
// observability wrappers must not disappear silently.
//
// Note on Clone's side effect: the stdlib's Transport.Clone runs the base
// transport's lazy HTTP/2 setup, which populates base.TLSClientConfig with
// the default h2 protocol list if it was nil. That is the same
// initialization the base would perform on its own first request, and it
// carries no policy — Clone deep-copies the config, so the pinned
// tlsCfg assigned below lands only on the clone. The base transport never
// inherits InsecureSkipVerify or the pin verifier.
func transportWithTLS(base http.RoundTripper, tlsCfg *tls.Config, logger *slog.Logger) *http.Transport {
	if ht, ok := base.(*http.Transport); ok {
		ht = ht.Clone()
		ht.TLSClientConfig = tlsCfg
		return ht
	}
	if base != nil && logger != nil {
		logger.Warn("llmtls: base transport is a custom RoundTripper that cannot carry a TLS config; falling back to a default-transport clone",
			"type", fmt.Sprintf("%T", base))
	}
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		ht := dt.Clone()
		ht.TLSClientConfig = tlsCfg
		return ht
	}
	// Unreachable with the stdlib default transport; kept fail-safe.
	return &http.Transport{TLSClientConfig: tlsCfg}
}

// pinVerifier returns a VerifyPeerCertificate callback that accepts the leaf
// certificate only when base64(SHA-256(SPKI DER)) equals pin.
func pinVerifier(pin string) func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return fmt.Errorf("%w: server presented no certificate", ErrPinMismatch)
		}
		cert, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("parsing peer certificate: %w", err)
		}
		if SPKIFingerprint(cert) != pin {
			return ErrPinMismatch
		}
		return nil
	}
}

// SPKIFingerprint returns base64(StdEncoding, SHA-256(cert.RawSubjectPublicKeyInfo)).
func SPKIFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// normalizePin strips whitespace from a configured pin so values pasted with
// line wraps or spaces still match. Comparison itself is case-sensitive
// (base64 standard alphabet is well-defined).
func normalizePin(pin string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		default:
			return r
		}
	}, pin)
}

// FetchFingerprint connects to the host:port of rawURL without verifying the
// server certificate, performs the TLS handshake, and returns the leaf
// certificate's SPKI fingerprint (base64 SHA-256). It exists for the
// settings UI "Get" button: the user pins the certificate the server
// presents RIGHT NOW rather than copying hashes from the server.
//
// The call is UNCONDITIONAL with respect to the configured pin (ADR-052): it
// takes no fingerprint argument and never reads one, so pressing "Get"
// behaves identically whether the provider already has a pin or not — it
// always reports what the endpoint currently serves. Deciding what to do
// with the result is the caller's (the user's) business.
//
// Only the handshake happens — no HTTP request is sent — so no API key is
// involved. rawURL must carry a scheme and host; a path is ignored.
func FetchFingerprint(ctx context.Context, rawURL string, logger *slog.Logger) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parsing URL: %w", err)
	}
	host := parsed.Hostname()
	port := parsed.Port()
	if host == "" {
		return "", fmt.Errorf("URL %q has no host", rawURL)
	}
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(host, port)

	ctx, cancel := context.WithTimeout(ctx, FetchFingerprintTimeout)
	defer cancel()

	dialer := &tls.Dialer{
		// The whole point is to READ the untrusted certificate; verification
		// is the user's next step (pinning it). #nosec G402.
		Config: &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 -- deliberate: fetching the pin
			MinVersion:         tls.VersionTLS12,
		},
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("TLS dial %s: %w", addr, err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil && logger != nil {
			logger.Debug("closing fingerprint probe connection", "error", cerr)
		}
	}()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return "", errors.New("fingerprint probe returned a non-TLS connection")
	}
	certs := tlsConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("server presented no certificate")
	}
	return SPKIFingerprint(certs[0]), nil
}
