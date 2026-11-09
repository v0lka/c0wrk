package desktop

import (
	"log/slog"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/options/linux"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/core/webviewgpu"
)

// TestWailsWebviewGpuPolicyCompensatesCMapping pins the compensation by
// NUMERIC value — the integer the C layer actually switches on — never by
// named constant: if a future Wails upgrade reorders the Go enum again
// (exactly what happened in v2.13.0), the constant names stay the same but
// their numbers change, so a name-based assertion would keep passing while
// the runtime silently swaps "always" and "on-demand". The numeric pin fails
// loudly instead.
func TestWailsWebviewGpuPolicyCompensatesCMapping(t *testing.T) {
	// wantC is the integer crossing wails.Run into SetupWebview's switch in
	// window.c (v2.16.0): 0→WEBKIT_…POLICY_ALWAYS, 1→…POLICY_ON_DEMAND,
	// 2→…POLICY_NEVER.
	cases := []struct {
		policy webviewgpu.Policy
		wantC  int
	}{
		{webviewgpu.PolicyAlways, 0},   // → C case 0 → WEBKIT_HARDWARE_ACCELERATION_POLICY_ALWAYS
		{webviewgpu.PolicyOnDemand, 1}, // → C case 1 → WEBKIT_HARDWARE_ACCELERATION_POLICY_ON_DEMAND
		{webviewgpu.PolicyNever, 2},    // → C case 2 → WEBKIT_HARDWARE_ACCELERATION_POLICY_NEVER
	}
	for _, tc := range cases {
		if got := int(wailsWebviewGpuPolicy(tc.policy)); got != tc.wantC {
			t.Fatalf("policy %v: want C-layer integer %d, got %d", tc.policy, tc.wantC, got)
		}
	}
}

// TestWailsWebviewGpuPolicyEnumOrderCanary pins the numeric order of the
// Wails Go enum itself (OnDemand=0, Always=1, Never=2 as of v2.16.0). The
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

// dmabufFallbackFixture mirrors the backend watchdog tests' fixture (the
// same shape): an active DMABUF auto-fallback record, used here to pin the
// resolution order around an active fallback.
func dmabufFallbackFixture() *backend.WebviewGpuAutoFallback {
	return &backend.WebviewGpuAutoFallback{
		Policy:     backend.WebviewGpuPolicyNever,
		Reason:     backend.GpuFallbackReasonDmabufFailure,
		DetectedAt: "2026-10-15T00:00:00Z",
	}
}

func TestWebviewGpuPolicyOptions(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	// wantC pins the integer the C layer receives (window.c switch in the
	// pinned Wails v2.16.0: 0→ALWAYS, 1→ON_DEMAND, 2→NEVER) — not the named
	// constants, which would keep passing through a Go-enum reorder while
	// the runtime silently swaps policies (see
	// TestWailsWebviewGpuPolicyCompensatesCMapping).
	//
	// Resolution order under test: valid env override → persisted setting →
	// auto (on-demand unless the watchdog fallback is active, or this run's
	// stderr is unobservable — then never, review finding #155.2). An INVALID env
	// value falls through to the setting instead of forcing never — deliberate
	// change from the env-only era: the setting is user-visible now, the env
	// var is an advanced hatch, and a typo must not silently disable GPU
	// compositing (issue #34).
	cases := []struct {
		name     string
		goos     string
		env      string
		state    *backend.WebviewGpuState
		observed bool
		wantNil  bool
		wantC    int
	}{
		{name: "unset env + no setting = auto → on-demand", goos: "linux", env: "", observed: true, wantC: 1},
		{name: "whitespace env + no setting = auto → on-demand", goos: "linux", env: "   ", observed: true, wantC: 1},
		{name: "always override", goos: "linux", env: "always", wantC: 0},
		{name: "on-demand override", goos: "linux", env: "on-demand", wantC: 1},
		{name: "ondemand alias", goos: "linux", env: "ondemand", wantC: 1},
		{name: "never override", goos: "linux", env: "never", wantC: 2},
		{name: "invalid env falls through to auto", goos: "linux", env: "speed", observed: true, wantC: 1},
		// Review finding #155.2: fresh auto without a usable stderr capture is
		// demoted to never — the DMABUF watchdog could never detect the grey
		// window the accelerated default risks, so the run starts safe instead.
		{name: "unobserved fresh auto → never", goos: "linux", env: "", observed: false, wantC: 2},
		{name: "invalid env + unobserved falls through to demoted auto", goos: "linux", env: "speed", observed: false, wantC: 2},
		{
			name:  "invalid env falls through to setting",
			goos:  "linux",
			env:   "speed",
			state: &backend.WebviewGpuState{GPUPolicy: backend.WebviewGpuPolicyNever},
			wantC: 2,
		},
		{
			name: "env override beats setting and fallback",
			goos: "linux", env: "always",
			state: &backend.WebviewGpuState{
				GPUPolicy:    backend.WebviewGpuPolicyNever,
				AutoFallback: dmabufFallbackFixture(),
			},
			wantC: 0,
		},
		{
			name: "setting always",
			goos: "linux", env: "",
			state: &backend.WebviewGpuState{GPUPolicy: backend.WebviewGpuPolicyAlways},
			wantC: 0,
		},
		{
			name: "setting never",
			goos: "linux", env: "",
			state: &backend.WebviewGpuState{GPUPolicy: backend.WebviewGpuPolicyNever},
			wantC: 2,
		},
		{
			// An explicit choice is never demoted: the user picked the
			// accelerated policy knowing the trade-off.
			name: "explicit on-demand survives unobserved",
			goos: "linux", env: "",
			state: &backend.WebviewGpuState{GPUPolicy: backend.WebviewGpuPolicyOnDemand},
			wantC: 1,
		},
		{
			name: "explicit setting auto = auto",
			goos: "linux", env: "",
			observed: true,
			state:    &backend.WebviewGpuState{GPUPolicy: backend.WebviewGpuPolicyAuto},
			wantC:    1,
		},
		{
			name: "auto + active fallback → never",
			goos: "linux", env: "",
			observed: true,
			state:    &backend.WebviewGpuState{AutoFallback: dmabufFallbackFixture()},
			wantC:    2,
		},
		{
			// Already never through the fallback: unobserved changes nothing.
			name: "auto + fallback unobserved stays never",
			goos: "linux", env: "",
			observed: false,
			state:    &backend.WebviewGpuState{AutoFallback: dmabufFallbackFixture()},
			wantC:    2,
		},
		{
			name: "explicit setting beats stale fallback",
			goos: "linux", env: "",
			state: &backend.WebviewGpuState{
				GPUPolicy:    backend.WebviewGpuPolicyAlways,
				AutoFallback: dmabufFallbackFixture(),
			},
			wantC: 0,
		},
		{
			name: "unknown persisted setting degrades to auto",
			goos: "linux", env: "",
			observed: true,
			state:    &backend.WebviewGpuState{GPUPolicy: "turbo"},
			wantC:    1,
		},
		{name: "darwin ignores override", goos: "darwin", env: "always", wantNil: true},
		{name: "windows ignores override", goos: "windows", env: "always", wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(backend.WebviewGpuPolicyEnvVar, tc.env)
			agentDir := t.TempDir()
			if tc.state != nil {
				if err := backend.SaveWebviewGpuState(agentDir, *tc.state); err != nil {
					t.Fatalf("save state: %v", err)
				}
			}
			got := webviewGpuPolicyOptions(tc.goos, agentDir, tc.observed, logger)
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
