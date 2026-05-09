package builtins

import (
	"context"
	"net"
	"net/url"
)

// privateNetworks contains CIDR ranges considered private or reserved.
// Requests to these ranges are flagged for user confirmation to prevent SSRF.
var privateNetworks []*net.IPNet

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
			panic("invalid CIDR in privateNetworks: " + cidr)
		}
		privateNetworks = append(privateNetworks, network)
	}
}

// isPrivateIP reports whether ip falls within a private or reserved range.
func isPrivateIP(ip net.IP) bool {
	for _, network := range privateNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// resolveHostIsPrivate resolves the hostname from rawURL and reports whether
// any of its resolved addresses are private/reserved. Returns the resolved
// address string and whether it is private. On resolution failure, returns
// an empty string and false (letting the actual fetch produce a clearer error).
func resolveHostIsPrivate(ctx context.Context, rawURL string) (addr string, private bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}

	host := parsed.Hostname()
	if host == "" {
		return "", false
	}

	// Check if host is already a literal IP
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), isPrivateIP(ip)
	}

	// Resolve hostname with context for cancellation support
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", false
	}

	for _, a := range addrs {
		if isPrivateIP(a.IP) {
			return a.IP.String(), true
		}
	}
	return "", false
}
