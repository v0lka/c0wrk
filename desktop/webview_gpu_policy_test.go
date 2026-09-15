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
// the Go enum orders OnDemand=0, Always=1, Never=2. This test pins the
// compensation so "always" on the env var reaches WebKit as ALWAYS. If it
// fails after a Wails upgrade, upstream fixed the mismatch — swap or drop
// the non-default arms of wailsWebviewGpuPolicy and update this test.
func TestWailsWebviewGpuPolicyCompensatesCMapping(t *testing.T) {
	cases := []struct {
		policy webviewGpuPolicy
		want   linux.WebviewGpuPolicy
	}{
		{gpuPolicyAlways, linux.WebviewGpuPolicyOnDemand},
		{gpuPolicyOnDemand, linux.WebviewGpuPolicyAlways},
		{gpuPolicyNever, linux.WebviewGpuPolicyNever},
	}
	for _, tc := range cases {
		if got := tc.policy.wailsWebviewGpuPolicy(); got != tc.want {
			t.Fatalf("policy %v: want Wails value %d, got %d", tc.policy, tc.want, got)
		}
	}
}

func TestWebviewGpuPolicyOptions(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	cases := []struct {
		name       string
		goos       string
		raw        string
		wantNil    bool
		wantPolicy linux.WebviewGpuPolicy
	}{
		{name: "unset defaults to never on linux", goos: "linux", raw: "", wantPolicy: linux.WebviewGpuPolicyNever},
		{name: "whitespace-only defaults to never", goos: "linux", raw: "   ", wantPolicy: linux.WebviewGpuPolicyNever},
		{name: "always override", goos: "linux", raw: "always", wantPolicy: linux.WebviewGpuPolicyOnDemand},
		{name: "on-demand override", goos: "linux", raw: "on-demand", wantPolicy: linux.WebviewGpuPolicyAlways},
		{name: "ondemand alias", goos: "linux", raw: "ondemand", wantPolicy: linux.WebviewGpuPolicyAlways},
		{name: "never override", goos: "linux", raw: "never", wantPolicy: linux.WebviewGpuPolicyNever},
		{name: "invalid value warns and defaults to never", goos: "linux", raw: "speed", wantPolicy: linux.WebviewGpuPolicyNever},
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
			if got.WebviewGpuPolicy != tc.wantPolicy {
				t.Fatalf("want Wails policy %d, got %d", tc.wantPolicy, got.WebviewGpuPolicy)
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
