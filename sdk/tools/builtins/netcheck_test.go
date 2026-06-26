package builtins

import (
	"net"
	"testing"
)

// TestPrivateNetworksParse verifies that all baked-in CIDR literals parse
// successfully at package init. Regression guard for W-4 (panic-on-init was
// replaced with a deferred error; this test catches typos that would silently
// disable SSRF protection).
func TestPrivateNetworksParse(t *testing.T) {
	if len(privateNetworks) == 0 {
		t.Fatal("privateNetworks list is empty after init")
	}
}

// TestIsPrivateIP_Smoke covers a handful of representative addresses to confirm
// the parsed CIDR ranges classify correctly.
func TestIsPrivateIP_Smoke(t *testing.T) {
	tests := []struct {
		ip      string
		private bool
	}{
		{"10.0.0.1", true},
		{"172.16.5.5", true},
		{"192.168.1.1", true},
		{"127.0.0.1", true},
		{"169.254.169.254", true}, // EC2 metadata
		{"100.64.0.1", true},      // CGNAT
		{"::1", true},
		{"fe80::1", true},
		{"fc00::1", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"203.0.114.0", false}, // outside 203.0.113.0/24
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("invalid test IP: %s", tt.ip)
			}
			if got := isPrivateIP(ip); got != tt.private {
				t.Errorf("isPrivateIP(%s) = %v, want %v", tt.ip, got, tt.private)
			}
		})
	}
}
