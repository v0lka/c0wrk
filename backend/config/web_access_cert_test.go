package config

import (
	"crypto/x509"
	"net"
	"testing"
)

func TestWebAccessBrowserURL(t *testing.T) {
	cases := []struct {
		addr       string
		tlsEnabled bool
		want       string
	}{
		{"0.0.0.0:8080", false, "http://localhost:8080"},
		{"[::]:9000", false, "http://localhost:9000"},
		{"127.0.0.1:41234", false, "http://127.0.0.1:41234"},
		{"[::1]:41235", false, "http://[::1]:41235"},
		{"192.168.1.10:8080", false, "http://192.168.1.10:8080"},
		{"127.0.0.1:41234", true, "https://127.0.0.1:41234"},
		{"0.0.0.0:8080", true, "https://localhost:8080"},
		{"192.168.1.10:8080", true, "https://192.168.1.10:8080"},
	}
	for _, tc := range cases {
		if got := webAccessBrowserURL(tc.addr, tc.tlsEnabled); got != tc.want {
			t.Errorf("webAccessBrowserURL(%q, %v) = %q, want %q", tc.addr, tc.tlsEnabled, got, tc.want)
		}
	}
}

func TestWebAccessCertCoversHost(t *testing.T) {
	leaf := &x509.Certificate{
		DNSNames:    []string{"localhost", "MyHost.Example", "*.example.com"},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.ParseIP("::1")},
	}
	cases := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"LOCALHOST", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"myhost.example", true},
		{"192.0.2.1", false},
		{"other.example", false},
		// net.IP.Equal — and browser verification with it — treats the
		// IPv4-mapped form as the same address as its 4-byte form: covered.
		{"::ffff:127.0.0.1", true},
		// RFC 6125 §6.4.3 wildcard: a single "*" left-most label covers
		// exactly one label.
		{"app.example.com", true},
		{"APP.EXAMPLE.COM", true},
		{"example.com", false},
		{"a.b.example.com", false},
		{"example.com.", false},
	}
	for _, tc := range cases {
		if got := webAccessCertCoversHost(leaf, tc.host); got != tc.want {
			t.Errorf("webAccessCertCoversHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestClassifyWebAccessCert_NoPathIsEmptyStatus(t *testing.T) {
	if status, detail := ClassifyWebAccessCert("", "", "127.0.0.1"); status != "" || detail != "" {
		t.Fatalf("unconfigured cert path → (%q, %q), want the empty status", status, detail)
	}
}
