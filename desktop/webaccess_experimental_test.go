// SPDX-License-Identifier: Apache-2.0
//
// webaccess_experimental_test.go — the experimental master gate over the
// browser web-access sidecar (ADR-101), at the DESKTOP surfaces: the startup
// mount requires BOTH the section's own switch and experimental.enabled; a
// mid-session gate change live-follows through the backend hook
// (UpdateExperimentalFeatures → the OnExperimentalChanged FrontendAPIConfig
// seam → WebAccessService.ApplyExperimental) instead of waiting for the next
// launch. The service-level live-follow tests moved to the backend package
// with their code (P-11).

package desktop

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/config"
)

// TestInitWebAccess_ExperimentalGateClosedSkipsMount pins the gate the task
// fixes: web_access.enabled: true alone must NOT mount the sidecar while the
// experimental master gate is closed — including its absent-section default
// (false), which is exactly every pre-existing config.yaml at the moment the
// feature shipped. The skip must be QUIET (Debug): an intentionally-off
// feature is not a failure, so no ERROR record may land in the log.
func TestInitWebAccess_ExperimentalGateClosedSkipsMount(t *testing.T) {
	for name, experimental := range map[string]string{
		"section absent":   "",
		"explicitly false": "experimental:\n  enabled: false\n",
	} {
		t.Run(name, func(t *testing.T) {
			port := reserveLoopbackPort(t)
			cfg, path := writeWebAccessConfigGate(t, experimental, "web_access:\n  enabled: true\n  port: "+strconv.Itoa(port)+"\n")

			// A Debug-level buffer (unlike newWebAccessTestApp's Error-only
			// logger) so the skip record itself is observable.
			var buf bytes.Buffer
			a := NewApp()
			a.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			a.setConfigRef(cfg, path)
			t.Cleanup(func() { a.stopWebAccess(context.Background()) })

			a.initWebAccess(cfg, a.log())

			if a.webAccessService.Running() {
				t.Fatal("closed experimental gate: sidecar handle is set, want nil")
			}
			if a.eventTap.Load() != nil {
				t.Error("closed experimental gate: event tap is set — events would fan out to nowhere")
			}
			if dialLoopback(port) {
				t.Errorf("something listens on reserved port %d behind a closed experimental gate", port)
			}

			logs := buf.String()
			if strings.Contains(logs, "level=ERROR") {
				t.Errorf("the closed gate must skip quietly, got an ERROR record:\n%s", logs)
			}
			if !strings.Contains(logs, "level=DEBUG") || !strings.Contains(logs, "web access sidecar not mounted") {
				t.Errorf("the closed gate must leave a Debug skip record, got:\n%s", logs)
			}
		})
	}
}

// TestInitWebAccess_ExperimentalGateOpenMounts pins the positive combination:
// both gates open → the startup mount proceeds exactly as before the gate
// existed.
func TestInitWebAccess_ExperimentalGateOpenMounts(t *testing.T) {
	cfg, path := writeWebAccessConfigGate(t, "experimental:\n  enabled: true\n", "web_access:\n  enabled: true\n")
	a := newWebAccessTestApp(t, cfg, path)

	a.initWebAccess(cfg, a.log())

	if !a.webAccessService.Running() {
		t.Fatal("both gates open: sidecar handle is nil — the sidecar did not mount")
	}
	if a.webAccessService.Snapshot().URL == "" {
		t.Fatal("both gates open: sidecar has no resolved listener address")
	}
	if a.eventTap.Load() == nil {
		t.Error("both gates open: event tap is nil after a successful mount")
	}
}

// TestUpdateExperimentalFeatures_HookStartsSidecarMidSession is the full
// cross-seam contract (ADR-101): a REAL FrontendAPI (Init'd, like production)
// carries the desktop hook (the service's ApplyExperimental, wired in
// buildFrontendAPI), so UpdateExperimentalFeatures(true) persists the gate
// AND mounts the skipped sidecar in the same call, and
// UpdateExperimentalFeatures(false) stops it — no restart anywhere.
func TestUpdateExperimentalFeatures_HookStartsSidecarMidSession(t *testing.T) {
	port := reserveLoopbackPort(t)
	cfg, path := writeWebAccessConfigGate(t, "experimental:\n  enabled: false\n", "web_access:\n  enabled: true\n  port: "+strconv.Itoa(port)+"\n")

	// The startup_embedded_test pattern: a real FrontendAPI wired into the
	// App, over a temp agent dir, with an event recorder in place of the
	// Wails runtime. applyDefaults-free config: no skill/agent watchers
	// against the real user directories.
	agentDir := t.TempDir()
	recorder := &emitRecorder{}
	a := NewApp()
	a.logger = testLoggerForPhases()
	a.wailsEmit = recorder.emit
	a.FrontendAPI = backend.NewFrontendAPI(backend.FrontendAPIConfig{
		Config:                cfg,
		ConfigPath:            path,
		AgentDir:              agentDir,
		Logger:                a.logger,
		EmitEvent:             recorder.emit,
		AppCtx:                context.Background,
		OnExperimentalChanged: a.webAccessService.ApplyExperimental,
	})
	t.Cleanup(func() {
		a.stopWebAccess(context.Background())
		a.Lifecycle().Cleanup()
	})
	// Mirror buildFrontendAPI's wiring: the hook rides the FrontendAPIConfig
	// seam above (production hands it to Init inside buildFrontendAPI); the
	// config ref is captured the same way.
	a.setConfigRef(cfg, path)

	// Startup with the gate closed: the sidecar stays unmounted.
	a.initWebAccess(cfg, a.log())
	if a.webAccessService.Running() {
		t.Fatal("precondition: the closed-gate startup mount should have skipped")
	}

	// Enable mid-session: persist + hook in one call → the sidecar mounts.
	if err := a.frontendAPI().UpdateExperimentalFeatures(true); err != nil {
		t.Fatalf("UpdateExperimentalFeatures(true): %v", err)
	}
	if !a.webAccessService.Running() {
		t.Fatal("gate enabled mid-session: sidecar handle is nil — the hook did not mount")
	}
	if !dialLoopback(port) {
		t.Errorf("nothing accepts on the pinned port %d after the mid-session enable", port)
	}
	persisted, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load after enable: %v", err)
	}
	if !persisted.Experimental.Enabled {
		t.Error("persisted experimental.enabled = false after the mid-session enable, want true")
	}

	// Disable mid-session: the running sidecar stops in the same call.
	if err := a.frontendAPI().UpdateExperimentalFeatures(false); err != nil {
		t.Fatalf("UpdateExperimentalFeatures(false): %v", err)
	}
	if a.webAccessService.Running() {
		t.Fatal("gate disabled mid-session: sidecar handle is set, want nil")
	}
	if dialLoopback(port) {
		t.Errorf("something still listens on port %d after the mid-session disable", port)
	}
	persisted, err = config.Load(path)
	if err != nil {
		t.Fatalf("config.Load after disable: %v", err)
	}
	if persisted.Experimental.Enabled {
		t.Error("persisted experimental.enabled = true after the mid-session disable, want false")
	}
}
