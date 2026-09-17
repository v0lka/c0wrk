// Package llmtls provides per-provider TLS verification overrides for
// self-signed LLM endpoints. The host application (c0wrk) builds a pinned
// *http.Client from a provider's tls_fingerprint config; the TLS policy
// stays entirely in this layer — the SDK transports whatever client it is
// handed (llm.ProviderEntry.HTTPClient).
//
// Semantics (per ADR-050, "pin is the switch"):
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
// certificate material — only the fact of the mismatch (see ADR-050).
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
// Timeout and derives its transport from base's transport so proxy settings
// survive. logger (may be nil) receives a Warn when base carries a custom
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

// ResolveProviderClient picks the HTTP client for one provider dial path
// according to the proxy-wins rule (ADR-051): a configured proxy is a global
// network policy and takes precedence over the per-provider TLS pin.
//
//	proxyClient != nil → proxyClient, unchanged (pin deliberately ignored)
//	proxyClient == nil, pin == ""  → nil (direct client, system verification)
//	proxyClient == nil, pin != ""  → Client(nil, pin, logger): a fresh direct
//	                                client pinned to the SPKI
//
// The proxy branch returns the proxy client verbatim — no clone, no derived
// transport — so exactly the pre-ADR-050 behavior is restored whenever a
// proxy is active. The nil return for the no-override case keeps callers
// free to interpret it as "no client override" (SDK default transport with
// system verification, no proxy).
func ResolveProviderClient(proxyClient *http.Client, fingerprint string, logger *slog.Logger) *http.Client {
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
// pin format this package accepts (ADR-050). A malformed pin still flows
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
// settings UI "Get fingerprint" button: the user pins the certificate the
// server presents RIGHT NOW rather than copying hashes from the server.
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
