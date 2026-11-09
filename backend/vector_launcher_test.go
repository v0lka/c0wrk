package backend

import (
	"bytes"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/vectorindex"
)

// launcherTestLogger drops everything below Error level: the launcher's
// disabled/unavailable arms log Info/Warn diagnostics that are part of the
// behavior under test's happy path here, and a test-local handler (not a
// global redirect) keeps the package's real logging untouched.
func launcherTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

// launcherEmitRecorder is a thread-safe stand-in for the injected Emit seam
// that records every emitted (eventName, data) pair so tests can assert on
// event flow.
type launcherEmitRecorder struct {
	mu     sync.Mutex
	events []launcherEmittedEvent
}

type launcherEmittedEvent struct {
	Name string
	Data []any
}

func (r *launcherEmitRecorder) emit(eventName string, optionalData ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, launcherEmittedEvent{Name: eventName, Data: optionalData})
}

func (r *launcherEmitRecorder) snapshot() []launcherEmittedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]launcherEmittedEvent, len(r.events))
	copy(out, r.events)
	return out
}

// newLauncherAPI builds a FrontendAPI carrying cfg (the launcher reads its
// knobs through the configMu-guarded snapshot accessor); newLauncher wires
// the launcher's Emit seam to the same recorder.
func newLauncherAPI(t *testing.T, cfg *config.Config, recorder *launcherEmitRecorder) (*FrontendAPI, *slog.Logger) {
	t.Helper()
	log := launcherTestLogger()
	api := NewFrontendAPI(FrontendAPIConfig{
		Config:     cfg,
		ConfigPath: filepath.Join(t.TempDir(), "config.yaml"),
		Logger:     log,
		EmitEvent:  recorder.emit,
	})
	return api, log
}

// newEnabledConfig returns an ApplyDefaults-resolved config; when disabled is
// set, vector_index.enabled is explicitly false.
func newEnabledConfig(disabled bool) *config.Config {
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	faux := !disabled
	cfg.VectorIndex.Enabled = &faux
	return cfg
}

// TestVectorIndexLauncher_DisabledConfigParksRegistry pins the boot-time arm
// of the restartable init: a config with vector_index.enabled: false never
// loads the ONNX runtime — the per-root registry is parked (every path fails
// fast with the disabled error, FactoryReady reports false) and the off
// state is emitted on the vector_index:status stream.
func TestVectorIndexLauncher_DisabledConfigParksRegistry(t *testing.T) {
	recorder := &launcherEmitRecorder{}
	api, log := newLauncherAPI(t, newEnabledConfig(true), recorder)
	l := NewVectorIndexLauncher(api, t.TempDir(), log, VectorIndexLauncherDeps{Emit: recorder.emit})

	// Synchronous call — the disabled arm returns before any model-file or
	// ONNX work, so no goroutine timing is involved.
	l.runVectorIndexInit(1, make(chan struct{}))

	vr := api.Lifecycle().VectorRoots()
	if !vr.Disabled() {
		t.Fatal("registry must be parked when the config boots with indexing off")
	}
	if vr.FactoryReady() {
		t.Error("no factory may be wired while disabled")
	}

	emittedDisabled := false
	for _, ev := range recorder.snapshot() {
		if ev.Name != EventVectorIndexStatus || len(ev.Data) == 0 {
			continue
		}
		if st, ok := ev.Data[0].(VectorIndexStatus); ok && st.State == "disabled" {
			emittedDisabled = true
		}
	}
	if !emittedDisabled {
		t.Error("the disabled arm must emit a vector_index:status snapshot with state \"disabled\"")
	}
}

// TestVectorIndexLauncher_MissingModelsKeepsRegistryIdle pins that a
// launched-but-unavailable init (model files absent) leaves the registry
// un-parked and un-wired: "unavailable" is a backend failure, not the
// user's off switch, and a later factory wiring (or restart) still applies.
func TestVectorIndexLauncher_MissingModelsKeepsRegistryIdle(t *testing.T) {
	recorder := &launcherEmitRecorder{}
	api, log := newLauncherAPI(t, newEnabledConfig(false), recorder)
	// Empty agent dir: resolveModelPath finds no model files next to the
	// test binary (there is no app bundle) nor under the agent dir.
	l := NewVectorIndexLauncher(api, t.TempDir(), log, VectorIndexLauncherDeps{Emit: recorder.emit})

	l.runVectorIndexInit(1, make(chan struct{}))

	vr := api.Lifecycle().VectorRoots()
	if vr.Disabled() {
		t.Error("missing model files must NOT park the registry (that is the enabled=false state)")
	}
	if vr.FactoryReady() {
		t.Error("no factory may be wired when the model files are missing")
	}

	foundUnavailable := false
	for _, ev := range recorder.snapshot() {
		if ev.Name != "vector_index:status" || len(ev.Data) == 0 {
			continue
		}
		if m, ok := ev.Data[0].(map[string]any); ok {
			if avail, isBool := m["available"].(bool); isBool && !avail {
				foundUnavailable = true
			}
		}
	}
	if !foundUnavailable {
		t.Error("the missing-models arm must emit an available:false status")
	}
}

// TestVectorIndexLauncher_WireIfCurrentSuperseded pins the epochal guard:
// only the CURRENT epoch may wire its factory into the registry. A disable
// (applyEnabledChange(false)) or a newer launch bumps the epoch, and the
// stale launch's wiring attempt is refused atomically — the registry keeps
// whatever the newer state installed instead of being resurrected.
func TestVectorIndexLauncher_WireIfCurrentSuperseded(t *testing.T) {
	recorder := &launcherEmitRecorder{}
	api, log := newLauncherAPI(t, newEnabledConfig(false), recorder)
	l := NewVectorIndexLauncher(api, t.TempDir(), log, VectorIndexLauncherDeps{Emit: recorder.emit})

	// Mimic launch()'s epoch assignment without running the init body.
	l.mu.Lock()
	l.epoch++
	epoch := l.epoch
	l.mu.Unlock()

	// errLauncherProbe marks the probe factory so a test that accidentally
	// builds a manager through it fails loudly instead of constructing a real
	// vectorindex.Manager.
	errLauncherProbe := errors.New("launcher probe factory must never be invoked")
	noopFactory := func() (*vectorindex.Manager, error) { return nil, errLauncherProbe }
	ready := make(chan struct{})
	closeEmbedder := func() error { return nil }

	// Current epoch: wiring succeeds and the registry can build managers.
	if !l.wireIfCurrent(epoch, noopFactory, ready, closeEmbedder) {
		t.Fatal("the current epoch must wire successfully")
	}
	vr := api.Lifecycle().VectorRoots()
	if !vr.FactoryReady() {
		t.Fatal("precondition: registry wired by the current epoch")
	}

	// Disable-side invalidation: the hook bumps the epoch; the STALE launch
	// must lose its wiring race and never reach the registry again.
	l.applyEnabledChange(false)
	if l.wireIfCurrent(epoch, noopFactory, ready, closeEmbedder) {
		t.Error("a stale epoch must not wire (disable or a newer launch superseded it)")
	}

	// A NEW launch (fresh epoch) wires again — the restart path.
	l.mu.Lock()
	l.epoch++
	nextEpoch := l.epoch
	l.mu.Unlock()
	if !l.wireIfCurrent(nextEpoch, noopFactory, ready, closeEmbedder) {
		t.Error("the fresh epoch must wire successfully")
	}
}
