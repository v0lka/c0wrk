package backend

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/crashlog"
)

const (
	testRunBanner      = "=== c0wrk-desktop pid=1 version=v0.12.1 commit=x start=2026-10-19T22:36:44+03:00 ===\n"
	testCurrentBanner  = "=== c0wrk-desktop pid=2 version=v0.12.1 commit=x start=2026-10-19T22:36:49+03:00 ===\n"
	testExitBanner     = "=== c0wrk-desktop exit code=0 uptime=2s at=2026-10-19T22:36:46+03:00 ===\n"
	testWaylandEPROTO  = "Gdk-Message: 22:36:44.560: Error 71 (Ошибка протокола) dispatching to Wayland display.\n"
	testWaylandEPIPE   = "Gdk-Message: 22:36:44.560: Error 32 (Broken pipe) dispatching to Wayland display.\n"
	testWebkitSignalGC = "Overriding existing handler for signal 10. Set JSC_SIGNAL_FOR_GC if you want WebKit to use a different signal\n"
)

func TestPreviousRunDiedOnWaylandProtocolError(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"eproto crash", testRunBanner + testWebkitSignalGC + testWaylandEPROTO + testCurrentBanner, true},
		{"eproto crash, no earlier banner in tail", testWaylandEPROTO + testCurrentBanner, true},
		{"clean exit after eproto", testRunBanner + testWaylandEPROTO + testExitBanner + testCurrentBanner, false},
		{"epipe is not a gpu signal", testRunBanner + testWaylandEPIPE + testCurrentBanner, false},
		{"eproto only in an older run", testRunBanner + testWaylandEPROTO + testRunBanner + testWebkitSignalGC + testCurrentBanner, false},
		{"eproto in the current run only", testRunBanner + testExitBanner + testCurrentBanner + testWaylandEPROTO, false},
		{"no current banner", testRunBanner + testWaylandEPROTO, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "stderr.log")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := previousRunDiedOnWaylandProtocolError(path); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPreviousRunDiedOnWaylandProtocolErrorMissingFile(t *testing.T) {
	if previousRunDiedOnWaylandProtocolError(filepath.Join(t.TempDir(), "absent.log")) {
		t.Fatal("missing capture must not trigger the fallback")
	}
}

func writeCrashedRunLog(t *testing.T, logDir string) {
	t.Helper()
	content := testRunBanner + testWaylandEPROTO + testCurrentBanner
	if err := os.WriteFile(crashlog.StderrLogPath(logDir), []byte(content), 0o600); err != nil {
		t.Fatalf("write stderr.log: %v", err)
	}
}

func TestApplyWebviewGpuCrashFallbackPersistsFallback(t *testing.T) {
	agentDir, logDir := t.TempDir(), t.TempDir()
	writeCrashedRunLog(t, logDir)
	fixed := time.Date(2026, 10, 19, 19, 36, 49, 0, time.UTC)

	applyWebviewGpuCrashFallback("linux", "", agentDir, logDir, true, nil, func() time.Time { return fixed })

	state := LoadWebviewGpuState(agentDir, nil)
	if state.AutoFallback == nil {
		t.Fatal("expected a persisted auto-fallback")
	}
	if state.AutoFallback.Policy != WebviewGpuPolicyNever {
		t.Errorf("fallback policy = %q, want never", state.AutoFallback.Policy)
	}
	if state.AutoFallback.Reason != GpuFallbackReasonWaylandProtocolError {
		t.Errorf("fallback reason = %q, want %q", state.AutoFallback.Reason, GpuFallbackReasonWaylandProtocolError)
	}
	if state.AutoFallback.DetectedAt != "2026-10-19T19:36:49Z" {
		t.Errorf("detected_at = %q", state.AutoFallback.DetectedAt)
	}
	run := DeriveWebviewGpuRun("linux", agentDir, true, nil)
	if run.Policy != WebviewGpuPolicyNever || run.Source != WebviewGpuSourceAutoFallback {
		t.Errorf("launch resolution = %s/%s, want never/auto-fallback", run.Policy, run.Source)
	}
}

func TestApplyWebviewGpuCrashFallbackRespectsGate(t *testing.T) {
	cases := []struct {
		name           string
		goos           string
		envRaw         string
		setting        string
		stderrObserved bool
	}{
		{"non-linux", "darwin", "", "", true},
		{"explicit env override", "linux", "on-demand", "", true},
		{"explicit setting", "linux", "", WebviewGpuPolicyOnDemand, true},
		{"unobserved stderr", "linux", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agentDir, logDir := t.TempDir(), t.TempDir()
			writeCrashedRunLog(t, logDir)
			if tc.setting != "" {
				if err := SaveWebviewGpuState(agentDir, WebviewGpuState{GPUPolicy: tc.setting}); err != nil {
					t.Fatalf("save state: %v", err)
				}
			}

			applyWebviewGpuCrashFallback(tc.goos, tc.envRaw, agentDir, logDir, tc.stderrObserved, nil, time.Now)

			if LoadWebviewGpuState(agentDir, nil).FallbackActive() {
				t.Fatal("fallback must not be persisted when detection does not apply")
			}
		})
	}
}

func TestApplyWebviewGpuCrashFallbackInvalidEnvStaysArmed(t *testing.T) {
	agentDir, logDir := t.TempDir(), t.TempDir()
	writeCrashedRunLog(t, logDir)

	applyWebviewGpuCrashFallback("linux", "bogus", agentDir, logDir, true, nil, time.Now)

	if !LoadWebviewGpuState(agentDir, nil).FallbackActive() {
		t.Fatal("an invalid env value is not an explicit choice: the fallback must be persisted")
	}
}
