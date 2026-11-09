package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"time"
)

// Web-access TLS certificate statuses (ADR-102 decision 5): the cert_status
// vocabulary. The empty string (the zero value, not a constant) means no
// cert path is configured — nothing to classify.
const (
	webAccessCertStatusOK         = "ok"         // loads and covers the bind within its validity window
	webAccessCertStatusMismatch   = "mismatch"   // leaf does not cover the pinned bind's browser-facing host
	webAccessCertStatusUnreadable = "unreadable" // pair cannot be loaded or parsed
	webAccessCertStatusExpired    = "expired"    // outside the validity window (either end)
)

// webAccessURLHost maps a listener host to the host a browser opens: an
// unspecified wildcard host (0.0.0.0, ::) becomes "localhost" — the wildcard
// names "every interface", not an openable address. Shared by the URL
// renderer and the certificate status check (ADR-102 decision 5) so both
// name the SAME browser-facing host the certificate's SANs must cover.
func webAccessURLHost(host string) string {
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return "localhost"
	}
	return host
}

// webAccessBrowserURL renders the URL a browser opens for the sidecar's
// resolved listener address: the scheme follows the transport the sidecar is
// actually serving (https under web_access.tls, ADR-102 decision 1 — what
// the operator sees is always what the browser must open) plus host:port,
// with one rewrite — an unspecified wildcard host becomes "localhost"
// (webAccessURLHost).
func webAccessBrowserURL(addr string, tlsEnabled bool) string {
	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Not host:port — never expected from a TCP listener address; pass
		// the raw value through rather than invent a port.
		return scheme + "://" + addr
	}
	return scheme + "://" + net.JoinHostPort(webAccessURLHost(host), port)
}

// ClassifyWebAccessCert computes the cert_status/cert_detail pair of the
// web-access section view (ADR-102 decision 5): whenever a cert path is
// configured, the pair is loaded exactly the way the sidecar loads it and
// the leaf is checked against the pinned bind — SAN coverage of the bind's
// browser-facing host (the same rewrite the rendered URL applies, so the
// status certifies the address the browser actually opens) and the validity
// window. The check order is fixed for determinism: unreadable → mismatch →
// expired → ok; an unconfigured cert path is the empty status. Pure stdlib —
// the section rule lives with the other web_access validators, and the
// sidecar's certificate generator consumes the SAME coverage predicate
// (WebAccessCertCoversHost) so generation and validation cannot drift.
func ClassifyWebAccessCert(certPath, keyPath, bind string) (status, detail string) {
	if certPath == "" {
		return "", ""
	}
	host := webAccessURLHost(bind)
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return webAccessCertStatusUnreadable, fmt.Sprintf(
			"the certificate pair could not be loaded (%v) — the sidecar will refuse to start while this pair is configured; fix the files or regenerate the pair", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return webAccessCertStatusUnreadable, fmt.Sprintf(
			"the certificate could not be parsed (%v) — the sidecar will refuse to start while this pair is configured; fix the files or regenerate the pair", err)
	}
	if !WebAccessCertCoversHost(leaf, host) {
		return webAccessCertStatusMismatch, fmt.Sprintf(
			"the certificate does not cover %q (the current bind) — regenerate the pair for this address or point cert/key at a pair that covers it", host)
	}
	if !time.Now().Before(leaf.NotAfter) {
		return webAccessCertStatusExpired, fmt.Sprintf(
			"the certificate expired %s — regenerate the pair to renew it", leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	if time.Now().Before(leaf.NotBefore) {
		// Same clock-derived bucket: outside the validity window.
		return webAccessCertStatusExpired, fmt.Sprintf(
			"the certificate is not valid until %s — check the system clock or regenerate the pair", leaf.NotBefore.UTC().Format(time.RFC3339))
	}
	return webAccessCertStatusOK, ""
}

// webAccessCertCoversHost reports whether the leaf's SANs cover host: an IP
// host must appear among the IP SANs, a name host among the DNS SANs
// (RFC 6125 — the two lists are not interchangeable; a DNS SAN may carry the
// RFC 6125 §6.4.3 wildcard form).
func webAccessCertCoversHost(leaf *x509.Certificate, host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		for _, san := range leaf.IPAddresses {
			if san.Equal(ip) {
				return true
			}
		}
		return false
	}
	for _, san := range leaf.DNSNames {
		if dnsSANCovers(san, host) {
			return true
		}
	}
	return false
}

// dnsSANCovers reports whether one DNS SAN pattern covers host: exact match,
// or the RFC 6125 §6.4.3 wildcard — a single "*" as the complete left-most
// label matching exactly one label ("*.example.com" covers "app.example.com"
// but not "example.com" nor "a.b.example.com").
func dnsSANCovers(pattern, host string) bool {
	pattern = strings.ToLower(pattern)
	host = strings.ToLower(host)
	if pattern == host {
		return true
	}
	wildLabel, wildRest, wildOK := strings.Cut(pattern, ".")
	if !wildOK || wildLabel != "*" {
		return false
	}
	hostLabel, hostRest, hostOK := strings.Cut(host, ".")
	return hostOK && hostLabel != "" && hostRest == wildRest
}

// WebAccessBrowserURL exposes webAccessBrowserURL to the backend service: the
// URL a browser opens for the sidecar's resolved listener address.
func WebAccessBrowserURL(addr string, tlsEnabled bool) string {
	return webAccessBrowserURL(addr, tlsEnabled)
}

// WebAccessURLHost exposes webAccessURLHost to the sidecar's certificate
// generator: the browser-facing host of a listener bind.
func WebAccessURLHost(host string) string {
	return webAccessURLHost(host)
}

// WebAccessCertCoversHost exposes webAccessCertCoversHost to the certificate
// generator: the SAME coverage predicate the status classification applies,
// so generation and validation cannot drift apart (ADR-102 decision 5 — one
// SAN rule, consumed by internal/webaccess/certgen and by classification).
func WebAccessCertCoversHost(leaf *x509.Certificate, host string) bool {
	return webAccessCertCoversHost(leaf, host)
}
