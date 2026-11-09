package webviewgpu

import "testing"

func TestWebviewGpuPolicyString(t *testing.T) {
	cases := map[Policy]string{
		PolicyAlways:   CanonicalAlways,
		PolicyOnDemand: CanonicalOnDemand,
		PolicyNever:    CanonicalNever,
		Policy(9):      CanonicalNever,
	}
	for policy, want := range cases {
		if got := policy.String(); got != want {
			t.Fatalf("policy %d: want %q, got %q", policy, want, got)
		}
	}
}

// TestWebviewGpuPolicyFromCanonical pins the canonical-spelling mapping: the
// three alphabet values resolve to their enum, everything else (including an
// unknown or empty spelling) resolves to never.
func TestWebviewGpuPolicyFromCanonical(t *testing.T) {
	cases := []struct {
		canonical string
		want      Policy
	}{
		{CanonicalAlways, PolicyAlways},
		{CanonicalOnDemand, PolicyOnDemand},
		{CanonicalNever, PolicyNever},
		{"ondemand", PolicyNever}, // the alias is folded by backend's resolvers, not here
		{"", PolicyNever},
		{"turbo", PolicyNever},
	}
	for _, tc := range cases {
		if got := webviewGpuPolicyFromCanonical(tc.canonical); got != tc.want {
			t.Fatalf("webviewGpuPolicyFromCanonical(%q) = %v, want %v", tc.canonical, got, tc.want)
		}
	}
}
