//go:build darwin

package desktop

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// withRecoveryLogger swaps the package recoveryLogger for a capture handler
// for the duration of the test (restored via t.Cleanup).
func withRecoveryLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prev := recoveryLogger.Load()
	recoveryLogger.Store(logger)
	t.Cleanup(func() { recoveryLogger.Store(prev) })
	return &buf
}

// TestRecoveryHookInstalledLogsPerStatus pins the severity mapping of the
// main-queued install result: 0 and 2 are informational, 1 (WailsContext not
// found) is a warning because only the best-effort wake path remains.
func TestRecoveryHookInstalledLogsPerStatus(t *testing.T) {
	buf := withRecoveryLogger(t)

	for _, tc := range []struct {
		status int
		level  string
		frag   string
	}{
		{0, "INFO", "installed WKWebView web-content-process recovery hook"},
		{1, "WARN", "WailsContext class not found"},
		{2, "INFO", "already implements webContentProcessDidTerminate"},
	} {
		buf.Reset()
		logRecoveryHookInstalled(tc.status)
		out := buf.String()
		if !strings.Contains(out, "level="+tc.level) {
			t.Fatalf("status %d: log level mismatch, want level=%s in %q", tc.status, tc.level, out)
		}
		if !strings.Contains(out, tc.frag) {
			t.Fatalf("status %d: missing message fragment %q in %q", tc.status, tc.frag, out)
		}
	}
}

// TestRecoveryHookInstalledNilLoggerFallsBackToDefault pins the never-silent
// fallback: when the install block fires before Startup wired the logger,
// the outcome still surfaces through slog.Default. The nil store happens
// after withRecoveryLogger captured the original pointer, so its cleanup
// restores the original logger, not nil.
func TestRecoveryHookInstalledNilLoggerFallsBackToDefault(t *testing.T) {
	buf := withRecoveryLogger(t)
	prevDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prevDefault) })

	recoveryLogger.Store(nil)
	logRecoveryHookInstalled(0)

	if !strings.Contains(buf.String(), "installed WKWebView web-content-process recovery hook") {
		t.Fatalf("nil-logger fallback did not log through slog.Default: %q", buf.String())
	}
}
