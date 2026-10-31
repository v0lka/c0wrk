// Package securehttp centralizes c0wrk's HTTPS-only HTTP client hardening for
// the outbound download surfaces (core/updater release downloads,
// core/toolmanager tool archive downloads). Both channels fetch
// security-relevant bytes whose integrity is fail-closed SHA256 — but SHA256
// pins bytes, not authorship — so the transport itself must be end-to-end
// encrypted: a compromised API response, release mirror or MITM proxy must
// not be able to downgrade a verified-HTTPS fetch onto a plaintext origin via
// a redirect.
//
// The policy is one implementation on purpose: the invariant is
// security-critical (SECURITY.md ASI04), and per-package copies drift
// silently the moment one surface is hardened and the other is not.
//
// # The invariant
//
//   - The initial URL and EVERY redirect hop must be https (RequireHTTPS /
//     SecureRedirectCheck). The CheckRedirect hook sees only hops, so callers
//     MUST also check the initial URL before any network I/O.
//   - The single exception is loopback http (127.0.0.1, ::1, localhost):
//     loopback traffic never traverses the network, so the downgrade threat
//     does not apply — and the exception keeps consumers testable with
//     httptest.NewServer.
//   - The redirect chain is capped at MaxRedirects hops: installing
//     CheckRedirect replaces net/http's built-in cap, so it is re-implemented.
//   - The caller's *http.Client is never mutated: SecureHTTPClient returns a
//     shallow copy, because callers may share one proxy-aware client across
//     subsystems and every consumer must be free to layer its own policy on a
//     copy.
package securehttp

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
)

// ErrNonHTTPS is returned (wrapped) for a URL that violates the HTTPS-only
// transport rule. Assertions should use errors.Is against this sentinel; the
// message intentionally carries no channel name — callers wrap it with their
// own context ("tool %q", update downloads, …).
var ErrNonHTTPS = errors.New("download URL must use HTTPS")

// MaxRedirects bounds the redirect chain. Installing a custom CheckRedirect
// replaces net/http's built-in 10-hop cap, so the cap is re-implemented in
// SecureRedirectCheck.
const MaxRedirects = 10

// RequireHTTPS rejects any URL whose scheme is not https, with a single
// exception for loopback addresses (see the package invariant). It is the
// initial-URL half of the policy; SecureRedirectCheck is the per-hop half.
func RequireHTTPS(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url %q: %w", rawURL, err)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && IsLoopbackHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%w: %q", ErrNonHTTPS, rawURL)
}

// IsLoopbackHost reports whether host is a loopback address or the "localhost"
// name (which always resolves to loopback per RFC 6761).
func IsLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// SecureRedirectCheck is the http.Client.CheckRedirect policy shared by every
// download surface: EVERY redirect hop must satisfy the same HTTPS-only rule
// as the initial URLs (RequireHTTPS, loopback exception included), and the
// chain is capped at MaxRedirects hops.
func SecureRedirectCheck(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return fmt.Errorf("stopped after %d redirects", MaxRedirects)
	}
	return RequireHTTPS(req.URL.String())
}

// SecureHTTPClient returns a shallow copy of client with the per-hop
// HTTPS-only redirect policy installed. The caller's client is NEVER mutated
// (see the package invariant).
func SecureHTTPClient(client *http.Client) *http.Client {
	secured := *client
	secured.CheckRedirect = SecureRedirectCheck
	return &secured
}
