package builtins

import (
	"context"
	"fmt"
	"net"
	"net/url"
)

// privateNetworks contains CIDR ranges considered private or reserved.
// Requests to these ranges are flagged for user confirmation to prevent SSRF.
var privateNetworks []*net.IPNet

// privateNetworksInitErr is non-nil if any baked-in CIDR literal failed to
// parse during package initialization. Callers of resolveHostIsPrivate MUST
// check the returned error before trusting the result — the error propagates
// through the call chain so consumers can fail-open (report the misconfiguration)
// instead of silently accepting a degraded protection state.
var privateNetworksInitErr error

// PrivateNetworksInitErr exposes the initialization error so tests and upstream
// consumers can detect a broken CIDR list at startup or during operation.
func PrivateNetworksInitErr() error { return privateNetworksInitErr }

func init() {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
		"0.0.0.0/8",
		"100.64.0.0/10",
		"192.0.0.0/24",
		"192.0.2.0/24",
		"198.18.0.0/15",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"240.0.0.0/4",
	}
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			// Defer reporting so package init does not panic the whole binary.
			privateNetworksInitErr = err
			privateNetworks = nil
			return
		}
		privateNetworks = append(privateNetworks, network)
	}
}

// isPrivateIP reports whether ip falls within a private or reserved range.
// When the CIDR list failed to initialize (privateNetworksInitErr != nil),
// returns false — callers MUST check resolveHostIsPrivate's returned error
// rather than relying on the boolean alone.
func isPrivateIP(ip net.IP) bool {
	if privateNetworksInitErr != nil {
		return false
	}
	for _, network := range privateNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// resolveHostIsPrivate resolves the hostname from rawURL and reports whether
// any of its resolved addresses are private/reserved. Returns the resolved
// address string, whether it is private, and an error. The error is non-nil
// when the CIDR list failed to initialize (privateNetworksInitErr) — callers
// MUST check err first and treat an init failure as "SSRF protection unavailable,
// confirmation required." On DNS resolution failure, returns an empty string,
// false, and nil (letting the actual fetch produce a clearer error).
func resolveHostIsPrivate(ctx context.Context, rawURL string) (addr string, private bool, err error) {
	if privateNetworksInitErr != nil {
		return "", false, fmt.Errorf("SSRF protection unavailable: %w", privateNetworksInitErr)
	}
	parsed, _ := url.Parse(rawURL)
	if parsed == nil {
		return "", false, nil
	}

	host := parsed.Hostname()
	if host == "" {
		return "", false, nil
	}

	// Check if host is already a literal IP
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), isPrivateIP(ip), nil
	}

	// Resolve hostname with context for cancellation support
	addrs, _ := net.DefaultResolver.LookupIPAddr(ctx, host)
	if len(addrs) == 0 {
		return "", false, nil
	}

	for _, a := range addrs {
		if isPrivateIP(a.IP) {
			return a.IP.String(), true, nil
		}
	}
	return "", false, nil
}
