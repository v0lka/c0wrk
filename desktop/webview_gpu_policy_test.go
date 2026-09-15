package desktop

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/options/linux"
)

func TestParseWebviewGpuPolicy(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    webviewGpuPolicy
		wantErr bool
	}{
		{name: "always", raw: "always", want: gpuPolicyAlways},
		{name: "on-demand hyphenated", raw: "on-demand", want: gpuPolicyOnDemand},
		{name: "ondemand alias", raw: "ondemand", want: gpuPolicyOnDemand},
		{name: "never", raw: "never", want: gpuPolicyNever},
		{name: "case-insensitive", raw: "  ALWAYS ", want: gpuPolicyAlways},
		{name: "empty is invalid for parser", raw: "", wantErr: true},
		{name: "unknown value", raw: "speed", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseWebviewGpuPolicy(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got policy %v", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse %q: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("parse %q: want %v, got %v", tc.raw, tc.want, got)
			}
		})
	}
}

func TestParseWebviewGpuPolicyErrorContainsAllowedValues(t *testing.T) {
	_, err := parseWebviewGpuPolicy("turbo")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"turbo", "always", "on-demand", "never"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q should mention %q", err, want)
		}
	}
}

// The Wails conversion deliberately does NOT use like-named constants: the
// pinned Wails v2.15.0 C layer maps 0→ALWAYS, 1→ON_DEMAND, 2→NEVER while
// the Go enum orders OnDemand=0, Always=1, Never=2. These tests pin the
// compensation by NUMERIC value — the integer the C layer actually switches
// on — never by named constant: if a future Wails upgrade reorders the Go
// enum again (exactly what happened in v2.13.0), the constant names stay
// the same but their numbers change, so a name-based assertion would keep
// passing while the runtime silently swaps "always" and "on-demand". The
// numeric pin fails loudly instead.
func TestWailsWebviewGpuPolicyCompensatesCMapping(t *testing.T) {
	// wantC is the integer crossing wails.Run into SetupWebview's switch in
	// window.c (v2.15.0): 0→WEBKIT_…POLICY_ALWAYS, 1→…POLICY_ON_DEMAND,
	// 2→…POLICY_NEVER.
	cases := []struct {
		policy webviewGpuPolicy
		wantC  int
	}{
		{gpuPolicyAlways, 0},   // → C case 0 → WEBKIT_HARDWARE_ACCELERATION_POLICY_ALWAYS
		{gpuPolicyOnDemand, 1}, // → C case 1 → WEBKIT_HARDWARE_ACCELERATION_POLICY_ON_DEMAND
		{gpuPolicyNever, 2},    // → C case 2 → WEBKIT_HARDWARE_ACCELERATION_POLICY_NEVER
	}
	for _, tc := range cases {
		if got := int(tc.policy.wailsWebviewGpuPolicy()); got != tc.wantC {
			t.Fatalf("policy %v: want C-layer integer %d, got %d", tc.policy, tc.wantC, got)
		}
	}
}

// TestWailsWebviewGpuPolicyEnumOrderCanary pins the numeric order of the
// Wails Go enum itself (OnDemand=0, Always=1, Never=2 as of v2.15.0). The
// compensation test above already fails on any reorder — production returns
// the named constants, whose numbers would drift — but its failure alone
// does not say WHY. This canary names the root cause explicitly ("the Go
// enum was reordered") and points at wailsWebviewGpuPolicy for
// re-verification against window.c before swapping or dropping the
// non-default compensation arms.
func TestWailsWebviewGpuPolicyEnumOrderCanary(t *testing.T) {
	cases := []struct {
		name  string
		got   linux.WebviewGpuPolicy
		wantC int
	}{
		{"OnDemand", linux.WebviewGpuPolicyOnDemand, 0},
		{"Always", linux.WebviewGpuPolicyAlways, 1},
		{"Never", linux.WebviewGpuPolicyNever, 2},
	}
	for _, tc := range cases {
		if int(tc.got) != tc.wantC {
			t.Fatalf("linux.WebviewGpuPolicy%s = %d, want %d — the Wails Go enum order changed; re-verify wailsWebviewGpuPolicy against window.c",
				tc.name, int(tc.got), tc.wantC)
		}
	}
}

func TestWebviewGpuPolicyOptions(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	// wantC pins the integer the C layer receives (window.c switch in the
	// pinned Wails v2.15.0: 0→ALWAYS, 1→ON_DEMAND, 2→NEVER) — not the named
	// constants, which would keep passing through a Go-enum reorder while
	// the runtime silently swaps policies (see
	// TestWailsWebviewGpuPolicyCompensatesCMapping).
	cases := []struct {
		name    string
		goos    string
		raw     string
		wantNil bool
		wantC   int
	}{
		{name: "unset defaults to never on linux", goos: "linux", raw: "", wantC: 2},
		{name: "whitespace-only defaults to never", goos: "linux", raw: "   ", wantC: 2},
		{name: "always override", goos: "linux", raw: "always", wantC: 0},
		{name: "on-demand override", goos: "linux", raw: "on-demand", wantC: 1},
		{name: "ondemand alias", goos: "linux", raw: "ondemand", wantC: 1},
		{name: "never override", goos: "linux", raw: "never", wantC: 2},
		{name: "invalid value warns and defaults to never", goos: "linux", raw: "speed", wantC: 2},
		{name: "darwin ignores override", goos: "darwin", raw: "always", wantNil: true},
		{name: "windows ignores override", goos: "windows", raw: "always", wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := webviewGpuPolicyOptions(tc.goos, tc.raw, logger)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil options on %s, got %+v", tc.goos, got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil options")
			}
			if int(got.WebviewGpuPolicy) != tc.wantC {
				t.Fatalf("want C-layer integer %d, got %d", tc.wantC, int(got.WebviewGpuPolicy))
			}
		})
	}
}

func TestWebviewGpuPolicyString(t *testing.T) {
	cases := map[webviewGpuPolicy]string{
		gpuPolicyAlways:     "always",
		gpuPolicyOnDemand:   "on-demand",
		gpuPolicyNever:      "never",
		webviewGpuPolicy(9): "never",
	}
	for policy, want := range cases {
		if got := policy.String(); got != want {
			t.Fatalf("policy %d: want %q, got %q", policy, want, got)
		}
	}
}
