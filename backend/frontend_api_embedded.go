package backend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/embeddedllm"
	"runtime"
)

// The RPC surface of the embedded local model. This file owns the
// FrontendAPI-side wiring — supervisor, installer, config sink, the two global
// events — and NO policy; behavior is specified in
// specs/domains/embedded-llm.md, the wire contracts in
// specs/contracts/desktop-frontend.md. Beside it: wire shapes/mappers in
// frontend_api_embedded_dto.go, the tuning/device-probe RPCs in
// frontend_api_embedded_tuning.go.

// Budgets of the synchronous parts of this surface; the install itself is an
// unbounded background download. See specs/domains/embedded-llm.md.
const (
	// embeddedProbeTimeout bounds the install RPC's hardware probe — the outer
	// belt over core's per-command 2s bounds, so wedged probe helpers still
	// return an actionable error instead of hanging the settings dialog.
	embeddedProbeTimeout = 30 * time.Second
	// embeddedStopTimeout bounds every supervisor stop this surface waits for —
	// the shutdown stop, UnloadEmbeddedLLM, the stop RemoveEmbeddedLLM runs
	// first, and the step-0 stop a background install performs before its first
	// numbered step (traveling to core as Installer.StopTimeout rather than a
	// wrapped context, because the install's own context stays deadline-free for
	// the download) — covering the GATE ACQUISITION only: an in-flight load
	// holds the supervisor's single-instance gate for minutes and the app
	// context carries no deadline. Scoping and core's force path:
	// specs/domains/embedded-llm.md.
	embeddedStopTimeout = 30 * time.Second
)

// runtime_error codes of this subsystem (the payload's error_code field).
const (
	embeddedErrCodeInstall = "embedded_llm_install_failed"
	embeddedErrCodeRemove  = "embedded_llm_remove_failed"
)

// embeddedOpKind names the embedded-LLM operation holding the busy gate; its
// spelling is also the operator-facing noun the refusals use, so a refusal
// names the operation actually in flight.
type embeddedOpKind string

const (
	// embeddedOpIdle is the zero value: no embedded operation is in flight and
	// the gate is free.
	embeddedOpIdle embeddedOpKind = ""
	// embeddedOpInstall is the background download/verify/provision run.
	embeddedOpInstall embeddedOpKind = "install"
	// embeddedOpRemove is the blocking stop + tree removal + config save.
	embeddedOpRemove embeddedOpKind = "removal"
)

// ---------------------------------------------------------------------------
// Subsystem state
// ---------------------------------------------------------------------------

// embeddedLLMState is the FrontendAPI-owned bookkeeping of the embedded local
// model — storage layout, supervisor, installer, cached install record and the
// operation-busy gate — living on FrontendAPI as the value field `embedded`
// (zero value usable; no construction call before the first RPC). Two mutexes,
// deliberately separate: mu guards construction and gate bookkeeping, infoMu
// the cached manifest/restore/failure fields. Core calls OnState synchronously
// from the transitioning goroutine, so NEITHER is ever held across a
// supervisor call, and the lock order stays one-directional:
// (st.mu | st.infoMu) → configMu.
type embeddedLLMState struct {
	mu        sync.Mutex
	built     bool
	layout    embeddedllm.Layout
	server    *embeddedllm.Server
	installer *embeddedllm.Installer
	// loader is a LOCK-FREE snapshot of server for the router build, which
	// runs under configMu and must never take st.mu; a concurrent build
	// publishes the settled winner's instance. Only embeddedBuild writes it.
	loader atomic.Pointer[embeddedllm.Server]
	// busyOp is the single-run gate for the WHOLE subsystem, not just
	// installs: the installer has no internal synchronization and Install and
	// Remove write/delete the same staging/runtime/model trees, so an install
	// starting mid-removal would race the deletion for a half-staged tree.
	// Both directions claim this one gate.
	busyOp embeddedOpKind
	// installCancel is the CURRENT run's cancel, published at gate-claim time
	// — before the preflight, before the goroutine exists — so a cancel click
	// is honored at every point of the run; withdrawn (and CALLED, releasing
	// the context) by embeddedEndInstallRun. Guarded by mu.
	installCancel context.CancelFunc
	// installCancelRequested records that the OPERATOR asked to cancel, so the
	// failure branch can tell a requested cancellation from an unrelated fault
	// or an app-shutdown cancel of the parent. Neither signal alone silences.
	installCancelRequested bool

	infoMu sync.Mutex
	// manifest is the cached manifest.json snapshot (the durable install
	// record); hasManifest says it describes a real installation.
	manifest    embeddedllm.Manifest
	hasManifest bool
	// muted suppresses the transition event when the caller is about to emit
	// ONE explicit snapshot instead (startup restore, install completion,
	// removal) — one operation, one event. Set/cleared only by the goroutine
	// performing the operation (core calls OnState synchronously).
	muted bool
	// lastError carries the operator-friendly cause of the last failed
	// INSTALL run, surfaced as install_error — a Settings-only surface.
	// Removal failures ride their own RPC's rejected promise instead.
	// Cleared when the next install starts.
	lastError string
	// launchedTuning is the fingerprint of the overrides the CURRENTLY
	// RESIDENT process was launched with (recorded on `loading`, cleared on
	// every non-resident transition). Deliberately runtime state, NOT part of
	// manifest.json: tuning is an operator setting the config sink carries
	// verbatim, and a second persisted copy would go stale on the first edit.
	launchedTuning    string
	hasLaunchedTuning bool

	// Test seams, nil in production where the core defaults run: this
	// package's tests drive a full install/load/stop cycle without the
	// network, the pinned multi-gigabyte artifacts, or a real llama-server.
	probeFn    func(ctx context.Context, logger *slog.Logger) (embeddedllm.Hardware, error)
	spawnFn    embeddedllm.SpawnFunc
	httpClient *http.Client
	installFn  func(ctx context.Context, in *embeddedllm.Installer, opts embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error)
	// portProbeFn substitutes the loopback port prober, so a test can stage a
	// collision without occupying a real port.
	portProbeFn embeddedllm.PortProber
	// deviceProbeFn substitutes the accelerator probe behind
	// ProbeEmbeddedLLMDevices; production wires the SAME embeddedllm.ProbeDevices
	// the supervisor's load-time hook uses, so an explicit re-probe and a
	// load-time re-plan can never disagree about the machine.
	deviceProbeFn func(ctx context.Context, binaryPath string, logger *slog.Logger) (embeddedllm.MemoryTopology, bool)
	// stopBudget overrides embeddedStopTimeout on the stop-shaped paths (zero
	// in production), so a test observes the deadline path without waiting
	// the real 30s.
	stopBudget time.Duration
}

// embeddedStopBudget resolves the budget every stop-shaped path hands core: the
// test override when set, embeddedStopTimeout otherwise. Three of those paths
// wrap a context with it; the fourth — an install's step-0 stop — passes it to
// core as Installer.StopTimeout, the install's own context staying
// deadline-free for the download.
func (f *FrontendAPI) embeddedStopBudget() time.Duration {
	st := &f.embedded
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.stopBudget > 0 {
		return st.stopBudget
	}
	return embeddedStopTimeout
}

// installRecord returns the cached manifest snapshot.
func (s *embeddedLLMState) installRecord() (embeddedllm.Manifest, bool) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	return s.manifest, s.hasManifest
}

// setInstallRecord replaces the cached manifest snapshot.
func (s *embeddedLLMState) setInstallRecord(m embeddedllm.Manifest, ok bool) {
	s.infoMu.Lock()
	s.manifest, s.hasManifest = m, ok
	s.infoMu.Unlock()
}

// muteStateEvent arms/clears the suppression of the next transition's event.
func (s *embeddedLLMState) muteStateEvent(v bool) {
	s.infoMu.Lock()
	s.muted = v
	s.infoMu.Unlock()
}

// stateEventMuted reports whether transition events are suppressed.
func (s *embeddedLLMState) stateEventMuted() bool {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	return s.muted
}

// setError records the operator-friendly cause of a failed install run (see
// lastError for why removals never land here).
func (s *embeddedLLMState) setError(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	s.infoMu.Lock()
	s.lastError = msg
	s.infoMu.Unlock()
}

// lastFailure returns the recorded install failure ("" when none).
func (s *embeddedLLMState) lastFailure() string {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	return s.lastError
}

// setLaunchedTuning records the fingerprint of the overrides the process now
// starting was launched with. An empty fingerprint with ok=false clears the
// record, which is what every non-resident transition does.
func (s *embeddedLLMState) setLaunchedTuning(fingerprint string, ok bool) {
	s.infoMu.Lock()
	s.launchedTuning, s.hasLaunchedTuning = fingerprint, ok
	s.infoMu.Unlock()
}

// launchedTuningFingerprint returns the recorded launch fingerprint. ok is
// false when nothing was recorded — no resident process, or one this app
// instance did not start — and a caller MUST treat that as "unknown" rather
// than as "unchanged".
func (s *embeddedLLMState) launchedTuningFingerprint() (string, bool) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	return s.launchedTuning, s.hasLaunchedTuning
}

// deviceProber returns the accelerator-memory prober for an explicit re-probe:
// the test seam when one is installed, the core function otherwise. Read at
// call time, so a test may install it after construction.
func (s *embeddedLLMState) deviceProber() func(ctx context.Context, binaryPath string, logger *slog.Logger) (embeddedllm.MemoryTopology, bool) {
	if s.deviceProbeFn != nil {
		return s.deviceProbeFn
	}
	return embeddedllm.ProbeDevices
}

// ---------------------------------------------------------------------------
// Construction, restore and teardown
// ---------------------------------------------------------------------------

// embeddedBuild returns the supervisor and the installer, constructing them on
// first use and restoring the on-disk state. Both are pure local work — no
// network, no hardware probe, no process spawn — which is what lets startup
// call it on the critical path.
func (f *FrontendAPI) embeddedBuild() (*embeddedllm.Server, *embeddedllm.Installer, error) {
	f.seedAcquire()
	st := &f.embedded

	st.mu.Lock()
	if st.built {
		server, installer := st.server, st.installer
		st.mu.Unlock()
		return server, installer, nil
	}
	st.mu.Unlock()

	if f.agentDir == "" {
		return nil, nil, errors.New("the embedded LLM is unavailable: no agent directory")
	}
	// Roots come from the centralized path API; core never re-derives them.
	layout, err := embeddedllm.NewLayout(config.RuntimesDir(f.agentDir), config.EmbeddedModelDir(f.agentDir), config.EmbeddedMLXModelDir(f.agentDir))
	if err != nil {
		return nil, nil, fmt.Errorf("embedded LLM storage layout: %w", err)
	}

	logger := f.log().With("subsystem", "embedded_llm")
	server := embeddedllm.NewServer(layout, logger)
	server.AutoUnload = f.embeddedAutoUnloadPolicy()
	server.OnState = f.onEmbeddedLLMState
	// The persisted port is a preference re-checked before every spawn; a move
	// is written back so the provider base_url keeps matching (see
	// specs/domains/embedded-llm.md, Port allocation).
	server.EnsurePort = f.embeddedEnsurePort
	// A load re-measures the accelerator before committing to a launch shape;
	// the hook is fail-soft in core, so wiring it can never fail a load.
	server.ProbeDevices = embeddedllm.ProbeDevices
	// The supervisor is built once and cached, so the launch tuning is a
	// function: it reads the live config when the shape is planned.
	server.Tuning = f.embeddedTuning
	// The effective context a ready server reports is written back to the
	// tier-1 llm.models override, which nothing else can correct.
	server.PersistContext = f.persistEmbeddedContext
	installer := embeddedllm.NewInstaller(layout, logger)
	installer.Sink = embeddedConfigSink{f: f}
	// The supervisor owns the process, so install, removal and shutdown stops
	// are the same call; a repair also retires the tree a resident server
	// executes from.
	installer.Stop = server.Stop
	// The install's step-0 stop is the one stop nobody else bounds: the install
	// runs on the never-deadlined application context, so the budget travels
	// with the seam; step 0 precedes plan, so its expiry is what makes core's
	// force path reachable. runEmbeddedInstall re-resolves it onto the per-run
	// copy, honouring a tightened test seam after the first build.
	installer.StopTimeout = f.embeddedStopBudget()

	// Test seams override the core defaults; production leaves them nil.
	if st.probeFn != nil {
		installer.Probe = st.probeFn
	}
	if st.spawnFn != nil {
		server.Spawn = st.spawnFn
	}
	if st.httpClient != nil {
		server.HTTPClient = st.httpClient
	}

	f.embeddedRestore(server, layout, logger)

	st.mu.Lock()
	// A concurrent builder wins: both build the same thing from the same
	// agent dir, and the loser never started a process (construction is
	// inert).
	if st.built {
		server, installer = st.server, st.installer
	} else {
		st.layout, st.server, st.installer, st.built = layout, server, installer, true
	}
	st.mu.Unlock()
	// Published lock-free for the router build, which runs under configMu and
	// must never take st.mu; a concurrent identical store is idempotent.
	st.loader.Store(server)
	return server, installer, nil
}

// embeddedRestore reads manifest.json into memory — the WHOLE of the startup
// work for this subsystem: no download, no probe, no load.
func (f *FrontendAPI) embeddedRestore(server *embeddedllm.Server, layout embeddedllm.Layout, logger *slog.Logger) {
	st := &f.embedded
	path, err := layout.ManifestPath()
	if err != nil {
		logger.Warn("embedded LLM state not restored: the manifest path is unusable", "error", err)
		st.setInstallRecord(embeddedllm.Manifest{}, false)
		return
	}
	manifest, err := embeddedllm.ReadManifest(path)
	if err != nil {
		if !errors.Is(err, embeddedllm.ErrNotInstalled) {
			logger.Warn("embedded LLM manifest is unreadable; treating the model as not installed",
				"path", path, "error", err)
		}
		st.setInstallRecord(embeddedllm.Manifest{}, false)
		if f.embeddedConfig().Installed {
			// The generated `embedded` provider record is regenerated on every
			// config load from embedded_llm.installed alone, so nothing
			// reconciles the mismatch away: report it instead of silently
			// serving a model that cannot start.
			logger.Warn("embedded_llm.installed is true but no manifest was found; the model needs a reinstall",
				"path", path)
		}
		return
	}

	st.setInstallRecord(manifest, true)
	// ONE event for the restore: mute the SetInstalled transition, let
	// initEmbeddedLLM emit the explicit snapshot.
	st.muteStateEvent(true)
	if err := server.SetInstalled(true); err != nil {
		logger.Warn("embedded LLM state restore refused", "error", err)
	}
	st.muteStateEvent(false)
	logger.Info("embedded LLM state restored from the manifest",
		"backend", manifest.Backend, "packing", manifest.Packing,
		"port", manifest.Port, "context_size", manifest.ContextSize,
		"runtime_version", manifest.RuntimeVersion)
}

// initEmbeddedLLM restores the embedded local-model state and emits the initial
// embedded_llm:state snapshot (desktop/startup_phases.go, startup path). It
// never downloads, probes hardware or loads the model.
// See specs/domains/embedded-llm.md.
func (f *FrontendAPI) initEmbeddedLLM() {
	server, _, err := f.embeddedBuild()
	if err != nil {
		// Report the unusable subsystem rather than leaving the UI without an
		// initial snapshot: not_installed is the truth with no supervisor.
		f.log().Warn("embedded LLM subsystem unavailable", "error", err)
		f.emitEmbeddedLLMState(embeddedllm.StateNotInstalled, 0, err.Error())
		return
	}
	// The policy is re-applied from the live config: construction read it too,
	// but a config reload between construction and startup must still win.
	server.SetAutoUnload(f.embeddedAutoUnloadPolicy())

	snapshot := server.Status()
	// The router was first built inside NewApplication, before any loader seam
	// existed; re-attaching it here — during the startup restore, before any
	// session can issue a request — is what makes a cold request load the
	// model instead of failing to connect. Skipped when nothing is installed.
	if snapshot.State != embeddedllm.StateNotInstalled {
		f.rebuildRouterForEmbeddedTransport()
	}
	f.emitEmbeddedLLMState(snapshot.State, snapshot.Port, snapshot.Message)
}

// stopEmbeddedLLM stops the supervised server if one is running (Shutdown);
// a no-op when never constructed. Bounded by embeddedStopBudget: the shutdown
// context carries no deadline, an in-flight load holds the supervisor's gate
// for minutes, and the child is DETACHED — the OS will not reclaim it when
// this process exits (see desktop.App.stopEmbeddedLLM).
func (f *FrontendAPI) stopEmbeddedLLM(ctx context.Context) error {
	st := &f.embedded
	st.mu.Lock()
	server := st.server
	st.mu.Unlock()
	if server == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	budget := f.embeddedStopBudget()
	stopCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if err := server.Stop(stopCtx); err != nil {
		// A deadline most likely means the gate was still held by an in-flight
		// load. Unlike the RPC paths there is no "try again" to advise — an
		// unstopped llama-server outlives this process, and this is a quit.
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf(
				"the embedded LLM server did not stop within %s, most likely because a load is still in progress: %w",
				budget, err)
		}
		f.log().Error("failed to stop the embedded LLM server",
			"error", err, "budget", budget)
		return err
	}
	return nil
}

// embeddedAutoUnloadPolicy resolves the operator's idle policy from config.
func (f *FrontendAPI) embeddedAutoUnloadPolicy() embeddedllm.AutoUnload {
	cfg := f.embeddedConfig()
	return embeddedllm.NewAutoUnload(cfg.AutoUnload.IsEnabled(), cfg.AutoUnload.IdleMinutes())
}

// embeddedConfig returns a copy of the persisted embedded_llm section. A copy
// (not a pointer) so the caller never touches config state outside configMu.
func (f *FrontendAPI) embeddedConfig() config.EmbeddedLLMConfig {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	if f.config == nil {
		return config.EmbeddedLLMConfig{}
	}
	return f.config.EmbeddedLLM
}

// embeddedEnsurePort is the supervisor's pre-spawn port check
// (Server.EnsurePort): the first free loopback port at or above the persisted
// one, with a move persisted so the provider base_url keeps matching. A
// persistence failure is deliberately not fatal — the transport redirects to
// the live port either way. See specs/domains/embedded-llm.md.
func (f *FrontendAPI) embeddedEnsurePort(ctx context.Context, port int) (int, error) {
	free, err := embeddedllm.SearchFreePort(ctx, port, f.embeddedPortProber())
	if err != nil {
		return 0, err
	}
	if free == port {
		return free, nil
	}
	f.log().Warn("the persisted embedded LLM port is taken; moving to the next free one",
		"persisted", port, "port", free)
	if perr := f.persistEmbeddedPort(free); perr != nil {
		f.log().Warn("failed to persist the moved embedded LLM port",
			"port", free, "error", perr)
	}
	return free, nil
}

// embeddedPortProber returns the loopback prober for the port scan: the test
// seam when one is installed, nil otherwise (core resolves nil to the real bind
// probe). Read at call time, so a test may install it after construction.
func (f *FrontendAPI) embeddedPortProber() embeddedllm.PortProber {
	return f.embedded.portProbeFn
}

// persistEmbeddedPort writes a moved loopback port to embedded_llm.port and
// regenerates the backend-owned provider record from it; manifest.json keeps
// the port the install ALLOCATED — the preference the next load re-scans from,
// so a taken port is reclaimed instead of drifting upward. No router rebuild:
// this runs inside Server.Load under the supervisor gate, and the entry's
// transport redirects to the live port anyway.
func (f *FrontendAPI) persistEmbeddedPort(port int) error {
	sink := embeddedConfigSink{f: f}

	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}
	if !f.config.EmbeddedLLM.Installed {
		// No generated provider record exists to keep in sync; the load still
		// proceeds on the free port, only the config write is skipped.
		f.configMu.Unlock()
		return nil
	}
	previousLLM := f.config.LLM
	previousEmbedded := f.config.EmbeddedLLM

	f.config.EmbeddedLLM.Port = port
	// contextWindow 0: a port move knows nothing about the resolved RAM tier, so
	// the existing llm.models override is left exactly as the install wrote it.
	f.config.SyncEmbeddedLLMProvider(0)

	return sink.saveOrRollback(previousLLM, previousEmbedded)
}

// embeddedTuning resolves the operator's memory-plan overrides
// (embedded_llm.tuning) for a launch — wired onto Server.Tuning, so it reads
// the live config. A translation failure is fail-soft to the zero Tuning (the
// documented all-Auto plan): it should be unreachable, since validate() runs
// the same ToTuning, and the all-Auto plan is the safe answer, not a refusal.
func (f *FrontendAPI) embeddedTuning() embeddedllm.Tuning {
	tuning, err := f.embeddedConfig().Tuning.ToTuning()
	if err != nil {
		f.log().Debug("the embedded LLM tuning could not be translated; launching the all-Auto plan",
			"error", err)
		return embeddedllm.Tuning{}
	}
	return tuning
}

// persistEmbeddedContext writes the effective context a READY server reported
// into the tier-1 llm.models context_window override — a tier-1 value shadows
// the lazy probe, so this is the only correction that override can ever get.
// Runs inside Server.Load under the single-instance gate; no synchronous
// rebuild, a success SCHEDULES one (the stale install-time estimate makes the
// pre-call guard refuse prompts the real window accepts). Persistence failure
// is not fatal. See specs/domains/embedded-llm.md.
func (f *FrontendAPI) persistEmbeddedContext(_ context.Context, contextSize int) error {
	if contextSize <= 0 {
		// The override treats <= 0 as "leave the existing one alone"; a
		// non-positive window would fail validate() on the next config load.
		return nil
	}
	if contextSize > config.MaxModelContextWindow {
		// The CEILING side of the same guard: this is the one context figure
		// arriving from an external process over HTTP, and it lands in TWO
		// durable stores — a poisoned readback (an absurd n_ctx, a wrapped
		// product) must never be persisted, or compaction silently stops
		// triggering. Fail-soft: skip the write, the next load retries.
		f.log().Warn("ignoring an out-of-range embedded LLM context readback",
			"context_size", contextSize, "max", config.MaxModelContextWindow)
		return nil
	}

	st := &f.embedded
	// Mirror the corrected value into the cached install record: core already
	// rewrote manifest.json, and GetEmbeddedLLMStatus answers from this cache.
	if record, ok := st.installRecord(); ok && record.ContextSize != contextSize {
		record.ContextSize = contextSize
		st.setInstallRecord(record, true)
	}

	sink := embeddedConfigSink{f: f}

	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}
	if !f.config.EmbeddedLLM.Installed {
		// No generated provider record exists to keep in sync. The load still
		// proceeds; only the config write is skipped.
		f.configMu.Unlock()
		return nil
	}
	previousLLM := f.config.LLM
	previousEmbedded := f.config.EmbeddedLLM

	// The port is already authoritative in the section, so this regenerates the
	// provider record (a no-op when it agrees) and records the context window;
	// a false return means nothing moved — no write, no config:updated event.
	if !f.config.SyncEmbeddedLLMProvider(contextSize) {
		f.configMu.Unlock()
		return nil
	}

	if err := sink.saveOrRollback(previousLLM, previousEmbedded); err != nil {
		return err
	}

	// Emitters cache their display basis at HandleMessage start, so an idle
	// session would keep stale fill percentages; this model-scoped push
	// re-broadcasts a corrected context_fill immediately.
	f.pushDisplayContextWindow(contextSize)

	// The corrected window is durable and config:updated is on its way.
	// Schedule the async refresh so the live router stops guarding prompts
	// with the stale estimate — see scheduleEmbeddedRouterRefresh for why it
	// must not run synchronously on this path.
	f.scheduleEmbeddedRouterRefresh()
	return nil
}

// pushDisplayContextWindow forwards the measured embedded context window to
// the live session emitters (session.Manager.SetDisplayContextWindowForModel),
// model-scoped. Best-effort: a nil app/manager (tests, early startup) skips
// the push, exactly like a skipped router rebuild.
func (f *FrontendAPI) pushDisplayContextWindow(contextSize int) {
	push := f.displayWindowPush
	if push == nil {
		if f.appCell() == nil || f.app.Manager() == nil {
			return
		}
		push = f.app.Manager().SetDisplayContextWindowForModel
	}
	push(config.EmbeddedLLMModelName, contextSize)
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// onEmbeddedLLMState is the core supervisor's StateEvent transport: it forwards
// every observable transition as the global embedded_llm:state event. It runs
// on the transitioning goroutine and must never call back into the supervisor
// (core documents OnState as non-reentrant).
func (f *FrontendAPI) onEmbeddedLLMState(ev embeddedllm.StateEvent) {
	// Recorded BEFORE the mute check: muting suppresses an EVENT, not the
	// bookkeeping behind ReloadRequired.
	f.noteEmbeddedLaunchTuning(ev.State)

	if f.embedded.stateEventMuted() {
		return
	}
	f.emitEmbeddedLLMState(ev.State, ev.Port, ev.Message)
}

// emitEmbeddedLLMState emits the global embedded_llm:state event; the payload
// merges the transition with the cached manifest and the persisted config.
func (f *FrontendAPI) emitEmbeddedLLMState(state embeddedllm.State, port int, message string) {
	f.seedAcquire()
	if f.emitEvent == nil {
		return
	}
	f.emitEvent(EventEmbeddedLLMState, f.embeddedStatePayload(state, port, message))
}

// embeddedStatePayload merges a supervision state with the install record and
// the persisted config: the live supervisor port wins over the persisted one,
// the manifest (what is on disk) over the informational config copy. The two
// error fields are deliberately split — Error is the supervisor's own cause and
// only while the state IS error (the status-bar surface); InstallError is the
// last failed install run (Settings-only).
func (f *FrontendAPI) embeddedStatePayload(state embeddedllm.State, port int, message string) EmbeddedLLMStateData {
	manifest, hasManifest := f.embedded.installRecord()
	cfg := f.embeddedConfig()

	packing, backend := cfg.Packing, cfg.Backend
	contextSize := 0
	if hasManifest {
		packing = string(manifest.Packing)
		backend = string(manifest.Backend)
		contextSize = manifest.ContextSize
	}
	if port <= 0 {
		port = cfg.Port
	}
	if port <= 0 && hasManifest {
		port = manifest.Port
	}

	errText := message
	if state != embeddedllm.StateError {
		errText = ""
	}

	return EmbeddedLLMStateData{
		Installed:         state != embeddedllm.StateNotInstalled,
		Loading:           state == embeddedllm.StateLoading,
		Loaded:            state == embeddedllm.StateLoaded,
		Packing:           packing,
		Backend:           backend,
		Port:              port,
		ContextSize:       contextSize,
		AutoUnloadMinutes: cfg.AutoUnload.IdleMinutes(),
		Error:             errText,
		InstallError:      f.embedded.lastFailure(),
	}
}

// emitEmbeddedInstallProgress forwards one core Progress update as the global
// embedded_llm:install_progress event; it runs on the install goroutine and
// must not block (core already throttles the byte-level updates).
func (f *FrontendAPI) emitEmbeddedInstallProgress(p embeddedllm.Progress) {
	f.seedAcquire()
	if f.emitEvent == nil {
		return
	}
	f.emitEvent(EventEmbeddedLLMInstallProgress, EmbeddedLLMProgressData{
		Component:  string(p.Component),
		Stage:      p.Stage,
		BytesDone:  p.BytesDone,
		BytesTotal: p.BytesTotal,
	})
}

// emitEmbeddedRuntimeError raises the user-visible toast for a background
// failure — the case where no RPC is left to carry the error. Mirrors the
// existing runtime_error emitters (id, message, error_code).
func (f *FrontendAPI) emitEmbeddedRuntimeError(code, message string) {
	f.seedAcquire()
	if f.emitEvent == nil {
		return
	}
	f.emitEvent(EventRuntimeError, map[string]string{
		"id":         uuid.New().String(),
		"message":    message,
		"error_code": code,
	})
}

// ---------------------------------------------------------------------------
// RPC surface
// ---------------------------------------------------------------------------

// GetEmbeddedLLMStatus returns the embedded local-model status: supervision
// state, install record, resolved auto-unload policy, and whether a tuning
// change waits for the next load. Read-only getter, no error
// (specs/contracts/desktop-frontend.md): an unavailable subsystem reports
// Available=false with the not-installed state. No network, no probe.
func (f *FrontendAPI) GetEmbeddedLLMStatus() EmbeddedLLMStatus {
	cfg := f.embeddedConfig()
	status := EmbeddedLLMStatus{
		State:             string(embeddedllm.StateNotInstalled),
		Installed:         cfg.Installed,
		Packing:           cfg.Packing,
		Backend:           cfg.Backend,
		Port:              cfg.Port,
		AutoUnloadEnabled: cfg.AutoUnload.IsEnabled(),
		AutoUnloadMinutes: cfg.AutoUnload.IdleMinutes(),
		RuntimeVersion:    cfg.RuntimeVersion,
		InstalledAt:       cfg.InstalledAt,
		ModelFile:         cfg.ModelFile,
		ModelName:         config.EmbeddedLLMModelName,
		// Error is set from the live snapshot below; an install failure is the
		// Settings-only InstallError surface.
		Error:        "",
		InstallError: f.embedded.lastFailure(),
		Guards:       []EmbeddedLLMGuard{},
		Devices:      []EmbeddedLLMDevice{},
		Plan:         embeddedPlanDTO(nil),
	}
	if manifest, ok := f.embedded.installRecord(); ok {
		// The manifest is what is on disk; the config copy of packing/backend
		// is informational. The port falls back to it as well.
		status.Engine = string(manifest.Engine)
		status.PythonVersion = manifest.PythonVersion
		status.PackRevision = manifest.PackRevision
		status.Packing = string(manifest.Packing)
		status.Backend = string(manifest.Backend)
		status.ContextSize = manifest.ContextSize
		// The degradation record travels with the manifest: it describes the
		// install that produced these bytes.
		status.PackingReason = string(manifest.PackingReason)
		status.GPUFamily = string(manifest.GPUFamily)
		status.Guards = embeddedGuardsDTO(manifest.Guards)
		// The measured topology and the launch shape it informed are facts
		// about THIS install, recorded beside the bytes. A nil Topology (no
		// probe ever answered) leaves the zero values — check
		// TopologyProbedAt, not Devices, for "unknown".
		if manifest.Topology != nil {
			topology := embeddedDevicesDTO(*manifest.Topology)
			status.Devices = topology.Devices
			status.Unified = topology.Unified
			status.HostRAMGiB = topology.HostRAMGiB
			status.DeviceBudgetMiB = topology.DeviceBudgetMiB
			status.HostBudgetMiB = topology.HostBudgetMiB
			status.TopologyProbedAt = topology.ProbedAt
		}
		status.Plan = embeddedPlanDTO(manifest.Plan)
		if status.Port <= 0 {
			status.Port = manifest.Port
		}
		if status.ModelFile == "" {
			status.ModelFile = manifest.ModelFile
		}
		if status.RuntimeVersion == "" {
			status.RuntimeVersion = embeddedllm.RuntimeTag
		}
		if status.InstalledAt == "" {
			status.InstalledAt = manifest.InstalledAt
		}
	}

	server, installer, err := f.embeddedBuild()
	if err != nil {
		f.log().Debug("embedded LLM status without a supervisor", "error", err)
		if status.Installed && status.Port > 0 {
			status.BaseURL = embeddedBaseURL(status.Port)
			status.ModelID = embeddedCompositeModelID()
		}
		return status
	}
	status.Available = true

	snapshot := server.Status()
	status.State = string(snapshot.State)
	status.Installed = snapshot.State != embeddedllm.StateNotInstalled
	status.Loading = snapshot.State == embeddedllm.StateLoading
	status.Loaded = snapshot.State == embeddedllm.StateLoaded
	status.Pid = snapshot.Pid
	status.FitWarning = snapshot.FitWarning
	if snapshot.State == embeddedllm.StateError {
		status.Error = snapshot.Message
	}
	if snapshot.Port > 0 {
		status.Port = snapshot.Port
	}
	status.Installing = f.embeddedInstalling()
	if status.Installed && status.Port > 0 {
		status.BaseURL = embeddedBaseURL(status.Port)
	}
	if status.Installed {
		status.ModelID = embeddedCompositeModelID()
	} else {
		status.ModelID = ""
		status.BaseURL = ""
	}
	if remaining, armed := server.IdleRemaining(); armed && remaining > 0 {
		status.IdleRemainingSeconds = int64(remaining / time.Second)
	}
	// Computed LAST, from the live supervision state: the question is whether
	// the resident process was started with the overrides now on disk.
	status.ReloadRequired = f.embeddedTuningReloadRequired(status.Loading, status.Loaded)

	// The leftover scan is LAST of all and fail-soft: a read error leaves the
	// flags false — an honest "unknown — do not offer cleanup" — and never
	// degrades the status read. The flags' real audience is a not-installed
	// state with residue.
	if installer != nil {
		if lo, err := installer.DetectLeftovers(); err == nil {
			status.LeftoverRuntime = lo.Runtime
			status.LeftoverWeights = lo.Weights
			status.LeftoverProjection = lo.Projection
		} else {
			f.log().Debug("embedded LLM leftover scan failed", "error", err)
		}
	}
	return status
}

// InstallEmbeddedLLM provisions the pinned runtime and weights: synchronous
// gates only (single-run gate, bounded probe, combined memory refusal — a
// refusal downloads nothing); everything heavy runs in the BACKGROUND with
// embedded_llm:install_progress / embedded_llm:state events. Resumable and
// cancellable (a requested cancel is the quiet outcome); the step-0 stop is
// bounded so a repair during a cold load never parks the bar at 0%.
// See specs/domains/embedded-llm.md.
func (f *FrontendAPI) InstallEmbeddedLLM() error {
	server, installer, err := f.embeddedBuild()
	if err != nil {
		return err
	}
	if claimed, holder := f.beginEmbeddedOperation(embeddedOpInstall); !claimed {
		return embeddedBusyRefusal(holder, embeddedOpInstall, "starting an install")
	}

	// The run's cancellable context is published at claim time — before the
	// preflight and the goroutine — so a cancel click is honored at every
	// point of the run; refuseEmbeddedInstall withdraws it on refusals.
	installCtx, cancelInstall := context.WithCancel(f.ctx())
	f.embeddedBeginInstallRun(cancelInstall)
	// A new run retires the previous run's failure line: a stale install error
	// beside a live progress bar is a contradiction.
	f.embedded.setError(nil)

	// The MEMORY GATE runs HERE, synchronously: a refusal ten minutes into a
	// download is not actionable, and the rejected promise is the report — no
	// toast on top of it. This is the SAME gate Resolve runs first, exposed
	// rather than re-derived, so a click and the background install answer
	// with one voice; it prices both memory pools (see
	// specs/domains/embedded-llm.md, The combined memory gate). The engine
	// resolves FIRST — the gate is engine-shaped and `auto` is a platform
	// decision made here, once; a refusal exits through refuseEmbeddedInstall
	// like every other synchronous refusal, releasing the claimed gate.
	engine, err := f.embeddedResolveEngine()
	if err != nil {
		f.refuseEmbeddedInstall(err)
		return err
	}
	hw, err := f.embeddedInstallPreflight(engine)
	if err != nil {
		return err
	}

	go f.runEmbeddedInstall(installCtx, server, installer, hw, engine)
	return nil
}

// embeddedResolveEngine resolves the operator's `embedded_llm.engine` setting
// against THIS machine (see embeddedResolveEngineOn).
func (f *FrontendAPI) embeddedResolveEngine() (embeddedllm.Engine, error) {
	return f.embeddedResolveEngineOn(runtime.GOOS + "-" + runtime.GOARCH)
}

// embeddedResolveEngineOn resolves the operator's `embedded_llm.engine` setting
// against an explicit platform ("goos-goarch"): empty means auto, a supported
// explicit value is honored, an unsupported one is refused with guidance, and
// an unknown spelling is a typed refusal — never a silent substitution. The
// platform parameter is the seam the resolution table test drives; production
// passes only the running platform.
func (f *FrontendAPI) embeddedResolveEngineOn(platform string) (embeddedllm.Engine, error) {
	requested := embeddedllm.Engine(f.embeddedConfig().Engine)
	resolved, err := embeddedllm.ResolveEngine(requested, platform)
	if err != nil {
		return "", fmt.Errorf("the embedded LLM engine cannot run on this machine: %w", err)
	}
	f.log().Info("embedded LLM engine resolved", "requested", string(requested), "resolved", string(resolved))
	return resolved, nil
}

// CancelEmbeddedLLMInstall asks the in-flight background install to stop.
// IDEMPOTENT (no install in flight: a success no-op); it never CLAIMS the
// operation gate — it reads the run's bookkeeping, the run releases the gate
// itself. COOPERATIVE: the RPC returns once the request is delivered, not
// once the run has unwound. Core keeps the partial bytes as the resume point
// and the run reports the quiet outcome (the click is the report).
// See specs/domains/embedded-llm.md.
func (f *FrontendAPI) CancelEmbeddedLLMInstall() error {
	st := &f.embedded
	st.mu.Lock()
	cancel := st.installCancel
	if cancel != nil {
		// Recorded BEFORE the cancel fires: the run's failure branch, which
		// may race this call, needs the flag that makes the outcome quiet.
		st.installCancelRequested = true
	}
	st.mu.Unlock()
	if cancel == nil {
		return nil
	}
	f.log().Info("cancelling the embedded LLM install at the operator's request")
	cancel()
	return nil
}

// embeddedInstallPreflight runs the two synchronous install gates — the bounded
// hardware probe and the combined memory refusal — for a caller that already
// holds the operation gate. Every failure path, a PANIC included (the run's
// release defer does not exist until its goroutine starts), releases the gate
// through refuseEmbeddedInstall.
func (f *FrontendAPI) embeddedInstallPreflight(engine embeddedllm.Engine) (hw embeddedllm.Hardware, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicErr := fmt.Errorf("the embedded LLM install panicked before it started: %v", r)
			f.refuseEmbeddedInstall(panicErr)
			hw, err = embeddedllm.Hardware{}, panicErr
		}
	}()
	probeCtx, cancel := context.WithTimeout(f.ctx(), embeddedProbeTimeout)
	defer cancel()
	hw, err = f.embeddedProbe(probeCtx)
	if err != nil {
		refusal := fmt.Errorf("the embedded LLM install was refused: %w", err)
		f.refuseEmbeddedInstall(refusal)
		return hw, refusal
	}
	if err := embeddedllm.CheckMemoryBudget(embeddedllm.ResolveInput{
		MachineProfile: embeddedllm.MachineProfile{
			Platform:       hw.Platform,
			Backend:        hw.Backend,
			RAMGiB:         hw.RAMGiB,
			CUDA12Userland: hw.CUDA12Userland,
		},
		Engine: engine,
		// The operator's overrides are part of the shape this gate prices, and
		// the install below plans from this same tuning — the refusal here is
		// the one the install itself would produce, before a byte is fetched.
		Tuning: f.embeddedTuning(),
	}); err != nil {
		refusal := fmt.Errorf("the embedded LLM install was refused: %w", err)
		f.refuseEmbeddedInstall(refusal)
		return hw, refusal
	}
	return hw, nil
}

// RemoveEmbeddedLLM stops the server and removes the parts of the
// installation the scope names; under EVERY scope the manifest and the config
// registration are cleared and llm.default_model migrates off the embedded
// composite — a partial removal leaves a cache, never a half-registered
// install. Blocking and bounded (the translated "a load is still in progress"
// error fires only when no child had spawned); holds the operation gate; an
// unknown scope is refused BEFORE the stop. See specs/domains/embedded-llm.md.
func (f *FrontendAPI) RemoveEmbeddedLLM(scope string) error {
	// Parse the scope BEFORE anything else: an unknown value must not stop a
	// resident server or delete anything.
	removeScope, err := embeddedllm.ParseRemoveScope(scope)
	if err != nil {
		return err
	}
	server, installer, err := f.embeddedBuild()
	if err != nil {
		return err
	}
	claimed, holder := f.beginEmbeddedOperation(embeddedOpRemove)
	if !claimed {
		return embeddedBusyRefusal(holder, embeddedOpRemove, "removing the model")
	}
	defer f.endEmbeddedOperation()
	f.embedded.setError(nil)

	// Refuse BEFORE any byte is deleted when the removal would orphan the
	// config (embedded model the default, no other model enabled): ApplyRemoved
	// would refuse only after the weights and the manifest were already gone.
	// See embeddedConfigSink.ApplyRemoved for the data-loss rationale.
	f.configMu.RLock()
	orphanedConfig := f.config != nil &&
		f.config.LLM.DefaultModel == embeddedCompositeModelID() &&
		firstNonEmbeddedModelID(f.config, embeddedCompositeModelID()) == ""
	f.configMu.RUnlock()
	if orphanedConfig {
		err := errors.New("cannot remove the embedded model: it is the default model and no other provider model is enabled — add or enable another model first")
		f.log().Warn("embedded LLM removal refused", "reason", err.Error())
		return err
	}

	budget := f.embeddedStopBudget()
	removeCtx, cancel := context.WithTimeout(f.ctx(), budget)
	defer cancel()
	if err := installer.RemoveWithScope(removeCtx, removeScope); err != nil {
		err = embeddedBoundedStopErr("the local model removal", err, budget)
		// Carried by THIS call's rejected promise and the toast below —
		// deliberately not recorded as the install failure it is not (see
		// embeddedLLMState.lastError).
		f.log().Error("embedded LLM removal failed", "error", err)
		f.emitEmbeddedRuntimeError(embeddedErrCodeRemove,
			"The local model could not be removed: "+err.Error())
		f.emitEmbeddedStateFrom(server)
		return err
	}

	// The bytes and the manifest are gone; the supervisor must not keep
	// reporting an installation that no longer exists.
	f.embedded.setInstallRecord(embeddedllm.Manifest{}, false)
	f.embedded.muteStateEvent(true)
	err = server.SetInstalled(false)
	f.embedded.muteStateEvent(false)
	f.emitEmbeddedStateFrom(server)
	if err != nil {
		// Only reachable with a process still live, which Remove's own stop
		// should have made impossible — report it honestly.
		f.embedded.setError(err)
		return fmt.Errorf("the local model was removed from disk but its server is still running: %w", err)
	}
	return nil
}

// LoadEmbeddedLLM starts the local server and blocks until the model can
// actually answer — readiness is a non-empty /v1/models response, never a
// health check. Idempotent (a loaded model is a no-op) and single-instance;
// blocking on purpose, with the embedded_llm:state events carrying progress.
// See specs/domains/embedded-llm.md.
func (f *FrontendAPI) LoadEmbeddedLLM() error {
	server, _, err := f.embeddedBuild()
	if err != nil {
		return err
	}
	if op := f.embeddedBusyOperation(); op != embeddedOpIdle {
		return embeddedBusyRefusal(op, embeddedOpIdle, "loading the model")
	}
	f.embedded.setError(nil)

	if err := server.Load(f.ctx()); err != nil {
		if errors.Is(err, embeddedllm.ErrNotInstalled) {
			return errors.New("the embedded LLM is not installed — install it before loading it")
		}
		// The state event already carries the supervisor's cause; return the
		// same detail to the caller.
		return err
	}
	return nil
}

// UnloadEmbeddedLLM stops the server process, deterministically returning its
// RAM/VRAM; bytes stay on disk, so the state afterwards is "installed" and a
// later Load restarts without a download. Blocking, idempotent, bounded: a
// stop that did not take stays an error, and the TRANSLATED "a load is still
// in progress" error comes back only when no child had spawned yet. Refused
// while an install/removal holds the operation gate — one refusal rule for
// every reader. See specs/domains/embedded-llm.md.
func (f *FrontendAPI) UnloadEmbeddedLLM() error {
	server, _, err := f.embeddedBuild()
	if err != nil {
		return err
	}
	if op := f.embeddedBusyOperation(); op != embeddedOpIdle {
		return embeddedBusyRefusal(op, embeddedOpIdle, "unloading the model")
	}
	budget := f.embeddedStopBudget()
	unloadCtx, cancel := context.WithTimeout(f.ctx(), budget)
	defer cancel()
	if err := server.Unload(unloadCtx); err != nil {
		return embeddedBoundedStopErr("the local model unload", err, budget)
	}
	return nil
}

// embeddedBoundedStopErr translates a bounded stop's DEADLINE failure into an
// actionable one: the single-instance gate was still held by a load that
// started before this call. Every other failure is returned as core reported
// it, wrapped, so errors.Is(err, context.DeadlineExceeded) keeps working.
// See specs/domains/embedded-llm.md.
func embeddedBoundedStopErr(op string, err error, budget time.Duration) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf(
			"%s did not finish within %s, most likely because a load is still in progress: "+
				"wait for the model to report loaded or error, then try again: %w",
			op, budget, err)
	}
	return err
}

// SetEmbeddedLLMAutoUnload persists the idle-unload policy
// (embedded_llm.auto_unload) and applies it to the supervisor immediately: the
// timer is re-armed against the existing activity stamp, disarmed when off.
// minutes must be within 1..embeddedllm.MaxAutoUnloadMinutes — the same
// ceiling config validation enforces, an overflow guard, not a tuning opinion.
// The setting survives a removal. See specs/domains/embedded-llm.md.
func (f *FrontendAPI) SetEmbeddedLLMAutoUnload(enabled bool, minutes int) error {
	if minutes < 1 || minutes > embeddedllm.MaxAutoUnloadMinutes {
		return fmt.Errorf(
			"embedded_llm.auto_unload.minutes %d is not valid; must be within 1-%d",
			minutes, embeddedllm.MaxAutoUnloadMinutes)
	}

	server, _, err := f.embeddedBuild()
	if err != nil {
		// The subsystem being unavailable is not a reason to lose the
		// operator's setting: persist it, and report the supervisor problem.
		f.log().Warn("embedded LLM subsystem unavailable; persisting the auto-unload setting only", "error", err)
		server = nil
	}

	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}
	// Snapshot the whole section: saveOrRollback restores the pair it is
	// given, and the mutation below REPLACES the AutoUnload struct with fresh
	// pointers, leaving the snapshot's own pointers untouched.
	previousLLM := f.config.LLM
	previousEmbedded := f.config.EmbeddedLLM
	enabledValue, minutesValue := enabled, minutes
	f.config.EmbeddedLLM.AutoUnload = config.AutoUnloadConfig{
		Enabled: &enabledValue,
		Minutes: &minutesValue,
	}
	// The shared tail of every embedded config write: persist under configMu
	// (a failed write restores the exact prior value), clear configLoadErrors,
	// release configMu and emit config:updated.
	if err := (embeddedConfigSink{f: f}).saveOrRollback(previousLLM, previousEmbedded); err != nil {
		return err
	}

	if server != nil {
		server.SetAutoUnload(embeddedllm.NewAutoUnload(enabled, minutes))
		f.emitEmbeddedStateFrom(server)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Background install run
// ---------------------------------------------------------------------------

// beginEmbeddedOperation claims the single embedded-operation gate for kind.
// claimed=false means another embedded operation (install OR removal) is
// already in flight; holder names it, read under the same lock acquisition so
// a refusal can never race a release. See embeddedLLMState.busyOp.
func (f *FrontendAPI) beginEmbeddedOperation(kind embeddedOpKind) (claimed bool, holder embeddedOpKind) {
	st := &f.embedded
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.busyOp != embeddedOpIdle {
		return false, st.busyOp
	}
	st.busyOp = kind
	return true, embeddedOpIdle
}

// embeddedBusyOperation returns the kind of the embedded operation holding the
// gate, or embeddedOpIdle when it is free.
func (f *FrontendAPI) embeddedBusyOperation() embeddedOpKind {
	st := &f.embedded
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.busyOp
}

// embeddedInstalling reports whether a background INSTALL run is in flight —
// the narrow predicate EmbeddedLLMStatus.Installing renders as the
// per-component progress bar; a removal holds the same gate but must not
// light an install bar.
func (f *FrontendAPI) embeddedInstalling() bool {
	return f.embeddedBusyOperation() == embeddedOpInstall
}

// endEmbeddedOperation releases the embedded-operation gate.
func (f *FrontendAPI) endEmbeddedOperation() {
	st := &f.embedded
	st.mu.Lock()
	st.busyOp = embeddedOpIdle
	st.mu.Unlock()
}

// embeddedBeginInstallRun publishes the cancel of the run that just claimed
// the gate — before the preflight, so a cancel clicked during the synchronous
// gates is delivered the moment the run begins.
func (f *FrontendAPI) embeddedBeginInstallRun(cancel context.CancelFunc) {
	st := &f.embedded
	st.mu.Lock()
	st.installCancel = cancel
	st.installCancelRequested = false
	st.mu.Unlock()
}

// embeddedEndInstallRun withdraws the published cancel and CALLS it, so the
// cancellable context is released on every exit instead of living until
// shutdown. Both the run's bookkeeping defer and the preflight-refusal path
// go through here.
func (f *FrontendAPI) embeddedEndInstallRun() {
	st := &f.embedded
	st.mu.Lock()
	cancel := st.installCancel
	st.installCancel = nil
	st.installCancelRequested = false
	st.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// embeddedInstallCancelled reports whether err is the run ending because the
// OPERATOR cancelled it — both signals must agree: the flag alone would
// silence an unrelated later failure, the cause alone would treat an
// app-shutdown cancel as a cancellation nobody asked for.
func (f *FrontendAPI) embeddedInstallCancelled(err error) bool {
	st := &f.embedded
	st.mu.Lock()
	requested := st.installCancelRequested
	st.mu.Unlock()
	return requested && errors.Is(err, context.Canceled)
}

// embeddedBusyRefusal builds the actionable refusal every gated entry returns
// while an embedded operation holds the single-run gate. wanted (the CALLER's
// operation) decides the wording: "already running" is true only when the
// holder IS that operation, and the text renders verbatim in the Settings
// error line, so the nouns must stay true in both cases. Callers that are not
// themselves gate-holding operations pass embeddedOpIdle and always get the
// plain wording.
func embeddedBusyRefusal(holder, wanted embeddedOpKind, refused string) error {
	if holder == wanted {
		return fmt.Errorf("an embedded LLM %s is already running; wait for it to finish before %s",
			string(holder), refused)
	}
	return fmt.Errorf("an embedded LLM %s is running; wait for it to finish before %s",
		string(holder), refused)
}

// embeddedProbe runs the hardware probe through the seam (production:
// core/embeddedllm.ProbeHardware).
func (f *FrontendAPI) embeddedProbe(ctx context.Context) (embeddedllm.Hardware, error) {
	if fn := f.embedded.probeFn; fn != nil {
		return fn(ctx, f.log())
	}
	return embeddedllm.ProbeHardware(ctx, f.log())
}

// runEmbeddedInstall performs the download/verify/extract/provision run on a
// background goroutine; ctx is the cancellable child of the application
// context — the run stops only when the app quits or the operator cancels.
// The step-0 stop is the exception: it runs under embeddedStopBudget, handed
// to core as Installer.StopTimeout.
func (f *FrontendAPI) runEmbeddedInstall(ctx context.Context, server *embeddedllm.Server, installer *embeddedllm.Installer, hw embeddedllm.Hardware, engine embeddedllm.Engine) {
	// The gate release is a DEFER registered FIRST so it runs LAST: an observer
	// reads the gate as "the run is over", so every effect of the run must
	// already be visible when it flips; a leaked gate would refuse every
	// embedded RPC until a restart.
	defer f.endEmbeddedOperation()
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("the embedded LLM install panicked: %v", r)
			f.log().Error("panic during the embedded LLM install", "panic", r)
			f.failEmbeddedInstall(err)
			f.emitEmbeddedStateFrom(server)
		}
	}()
	// The bookkeeping withdrawal runs BEFORE the gate release (LIFO); its
	// cancel() call releases the context on every exit, success included.
	defer f.embeddedEndInstallRun()

	// A per-run copy pins the probe result the RPC already paid for; copying
	// is safe — Installer is a plain struct of injectable fields.
	run := *installer
	run.Probe = func(context.Context, *slog.Logger) (embeddedllm.Hardware, error) { return hw, nil }
	// Resolved PER RUN, like every other stop-shaped path resolves per call:
	// its expiry is what makes core's force path reachable for a repair
	// clicked during an in-flight cold load.
	stopBudget := f.embeddedStopBudget()
	run.StopTimeout = stopBudget
	if stop := run.Stop; stop != nil {
		// Translated on the run's own copy, NOT the shared installer, so
		// RemoveEmbeddedLLM's stop keeps its own single translation. A step-0
		// deadline means the gate was still held by a load — the one case with
		// no child for the force path to kill.
		run.Stop = func(stopCtx context.Context) error {
			return embeddedBoundedStopErr("the pre-install stop", stop(stopCtx), stopBudget)
		}
	}

	opts := embeddedllm.InstallOptions{
		Platform: hw.Platform,
		Backend:  hw.Backend,
		Engine:   engine,
		// The same tuning the preflight priced, through the same fail-soft
		// accessor, so plan, persist and launch see one operator setting.
		Tuning:   f.embeddedTuning(),
		Progress: f.emitEmbeddedInstallProgress,
	}
	// A repair/reinstall keeps the persisted port, so the generated provider
	// base_url (always derived from it) does not churn.
	if cfg := f.embeddedConfig(); cfg.Port >= config.EmbeddedLLMMinPort && cfg.Port <= config.EmbeddedLLMMaxPort {
		opts.Port = cfg.Port
	}

	var report *embeddedllm.InstallReport
	var err error
	if fn := f.embedded.installFn; fn != nil {
		report, err = fn(ctx, &run, opts)
	} else {
		report, err = run.Install(ctx, opts)
	}
	if err == nil && report == nil {
		// Unreachable for the real Installer; named rather than nil-derefd.
		err = errors.New("the embedded LLM install finished without reporting its result")
	}
	if err != nil {
		if f.embeddedInstallCancelled(err) {
			// QUIET outcome: the click is the report — no toast, no recorded
			// failure; core kept the partial bytes as the resume point.
			f.embedded.setError(nil)
			f.log().Info("embedded LLM install cancelled at the operator's request", "error", err)
			f.emitEmbeddedStateFrom(server)
			return
		}
		f.failEmbeddedInstall(err)
		f.emitEmbeddedStateFrom(server)
		return
	}

	f.embedded.setError(nil)
	f.embedded.setInstallRecord(report.Manifest, true)
	// ONE event for the whole operation: the transition is muted and the
	// snapshot below is emitted unconditionally, so a repair install (whose
	// state does not change) still reports that it finished.
	f.embedded.muteStateEvent(true)
	if err := server.SetInstalled(true); err != nil {
		f.log().Warn("the embedded LLM supervisor refused the installed state", "error", err)
	}
	f.embedded.muteStateEvent(false)
	server.SetAutoUnload(f.embeddedAutoUnloadPolicy())
	f.log().Info("embedded LLM install complete",
		"backend", report.Manifest.Backend, "packing", report.Manifest.Packing,
		"packing_reason", report.PackingReason, "gpu_family", report.Manifest.GPUFamily,
		"guards", embeddedGuardIDs(report.Guards),
		"port", report.Manifest.Port, "context_size", report.Manifest.ContextSize)
	f.emitEmbeddedStateFrom(server)
}

// refuseEmbeddedInstall releases the gate and records a SYNCHRONOUS refusal,
// raising no toast — the rejected promise is the report. It also withdraws the
// run's published cancel: the only exit between embeddedBeginInstallRun and
// the goroutine's own defers.
func (f *FrontendAPI) refuseEmbeddedInstall(err error) {
	f.embeddedEndInstallRun()
	f.endEmbeddedOperation()
	f.embedded.setError(err)
	f.log().Warn("embedded LLM install refused", "error", err)
}

// failEmbeddedInstall records a BACKGROUND FATAL failure: the cause lands in
// install_error (the Settings line), the log, and a runtime_error toast — no
// RPC is left to carry it. Resumable download failures never reach this
// function (core retries them silently).
func (f *FrontendAPI) failEmbeddedInstall(err error) {
	message := embeddedInstallFailureMessage(err)
	f.embedded.setError(errors.New(message))
	f.log().Error("embedded LLM install failed", "error", err)
	f.emitEmbeddedRuntimeError(embeddedErrCodeInstall,
		"The local model could not be installed: "+message)
}

// embeddedInstallFailureMessage translates a fatal install error into the
// sentence the Settings line and the toast render: download sentinels get
// dedicated wording naming the action (check the network / retry — bytes are
// kept); everything else is operator-facing at its source and passes through.
func embeddedInstallFailureMessage(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// An app-shutdown death mid-run, without an operator's cancel click;
		// the raw cause stays diagnosable in parentheses.
		return fmt.Sprintf(
			"the installation was interrupted before it finished — the bytes already downloaded are kept, and starting it again resumes from them (%v)",
			err)
	case errors.Is(err, embeddedllm.ErrAttemptsExhausted):
		if errors.Is(err, embeddedllm.ErrUnreachable) {
			return fmt.Sprintf(
				"the download server could not be reached after %d attempts — check the network connection and try again; the bytes already downloaded are kept, and the install resumes from them",
				embeddedllm.DefaultMaxFailedAttempts)
		}
		return "the download stopped making progress — the server kept dropping the transfer before any bytes arrived; the bytes already downloaded are kept, and starting the install again resumes from them"
	case errors.Is(err, embeddedllm.ErrUnreachable):
		return "the download server could not be reached — check the network connection and try again"
	case errors.Is(err, embeddedllm.ErrIncompleteTransfer):
		return "the download was interrupted before it finished — the bytes already downloaded are kept, and starting the install again resumes from them"
	default:
		return err.Error()
	}
}

// embeddedBaseURL derives the OpenAI-compatible endpoint from a loopback port
// — derived, never stored, so a reallocation cannot leave a stale endpoint.
func embeddedBaseURL(port int) string {
	if port <= 0 {
		return ""
	}
	return fmt.Sprintf("http://%s:%d/v1", config.EmbeddedLLMHost, port)
}

// embeddedCompositeModelID is the provider/model id the router exposes for the
// local model.
func embeddedCompositeModelID() string {
	return config.EmbeddedLLMProviderName + "/" + config.EmbeddedLLMModelName
}

// emitEmbeddedStateFrom emits a state snapshot taken from the supervisor.
func (f *FrontendAPI) emitEmbeddedStateFrom(server *embeddedllm.Server) {
	if server == nil {
		return
	}
	snapshot := server.Status()
	f.emitEmbeddedLLMState(snapshot.State, snapshot.Port, snapshot.Message)
}

// ---------------------------------------------------------------------------
// Config sink (core/embeddedllm.ConfigSink)
// ---------------------------------------------------------------------------

// embeddedConfigSink is the production core → config boundary (core cannot
// import backend/config): the reference state mutation pinned by
// backend/config/embedded_llm_sink_test.go, plus persistence, config:updated
// and the judge/router rebuild. See specs/domains/embedded-llm.md.
type embeddedConfigSink struct{ f *FrontendAPI }

var _ embeddedllm.ConfigSink = embeddedConfigSink{}

// ApplyInstalled writes embedded_llm.* from the install record, fills the
// auto-unload defaults into UNSET knobs only (nil stays distinguishable from
// an explicit choice), and regenerates the provider entry plus the
// context-window override. The engine and tuning sections are operator-owned
// and carried through verbatim.
func (s embeddedConfigSink) ApplyInstalled(_ context.Context, state embeddedllm.InstallState) error {
	f := s.f
	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}
	previousLLM := f.config.LLM
	previousEmbedded := f.config.EmbeddedLLM

	autoUnload := f.config.EmbeddedLLM.AutoUnload
	tuning := f.config.EmbeddedLLM.Tuning
	engineSetting := f.config.EmbeddedLLM.Engine // a choice, not a record
	f.config.EmbeddedLLM = config.EmbeddedLLMConfig{
		Engine:         engineSetting,
		Installed:      true,
		Packing:        string(state.Packing),
		Backend:        string(state.Backend),
		Port:           state.Port,
		ModelFile:      state.ModelFile,
		RuntimeVersion: state.RuntimeVersion,
		InstalledAt:    state.InstalledAt,
		PythonVersion:  state.PythonVersion,
		PackRevision:   state.PackRevision,
		AutoUnload:     autoUnload,
		Tuning:         tuning,
	}
	if f.config.EmbeddedLLM.AutoUnload.Enabled == nil {
		enabled := state.AutoUnloadEnabled
		f.config.EmbeddedLLM.AutoUnload.Enabled = &enabled
	}
	if f.config.EmbeddedLLM.AutoUnload.Minutes == nil {
		minutes := state.AutoUnloadMinutes
		f.config.EmbeddedLLM.AutoUnload.Minutes = &minutes
	}
	// The install is the only caller that has resolved a context tier, so it is
	// the only one that writes the llm.models override.
	f.config.SyncEmbeddedLLMProvider(state.ContextSize)

	if err := s.saveOrRollback(previousLLM, previousEmbedded); err != nil {
		return err
	}
	f.rebuildAfterEmbeddedConfigChange()
	return nil
}

// ApplyRemoved clears the install state, migrates llm.default_model off the
// embedded composite (an empty default fails validation — it must MOVE to
// another enabled model), and drops the provider record. The engine setting,
// auto-unload and tuning survive: operator settings, not install state.
func (s embeddedConfigSink) ApplyRemoved(_ context.Context) error {
	f := s.f
	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}
	previousLLM := f.config.LLM
	previousEmbedded := f.config.EmbeddedLLM

	composite := embeddedCompositeModelID()
	migrated := f.config.LLM.DefaultModel
	if migrated == composite {
		migrated = firstNonEmbeddedModelID(f.config, composite)
	}
	if migrated == "" {
		// No migration target: persisting would leave llm.default_model empty —
		// a config that fails validation at the next load. Refuse with the
		// config untouched; RemoveEmbeddedLLM gates the same predicate before
		// any byte is deleted, this is the convergent backstop.
		f.configMu.Unlock()
		return errors.New("cannot remove the embedded model: it is the default model and no other provider model is enabled — add or enable another model first")
	}
	autoUnload := f.config.EmbeddedLLM.AutoUnload
	tuning := f.config.EmbeddedLLM.Tuning
	engine := f.config.EmbeddedLLM.Engine // a choice, not a record
	f.config.EmbeddedLLM = config.EmbeddedLLMConfig{
		Engine:     engine,
		AutoUnload: autoUnload,
		Tuning:     tuning,
	}
	f.config.LLM.DefaultModel = migrated
	// contextWindow 0: the override of a removed model is dropped with its
	// provider record, and no other model's override is touched.
	f.config.SyncEmbeddedLLMProvider(0)

	if err := s.saveOrRollback(previousLLM, previousEmbedded); err != nil {
		return err
	}
	f.rebuildAfterEmbeddedConfigChange()
	return nil
}

// saveOrRollback persists the mutated config and restores the exact prior
// state when the write fails, so a failed save is indistinguishable from a
// rejected one. Called with configMu HELD; releases the lock before returning
// and emits config:updated only after a successful write.
func (s embeddedConfigSink) saveOrRollback(previousLLM config.LLMConfig, previousEmbedded config.EmbeddedLLMConfig) error {
	f := s.f
	if err := config.Save(f.config, f.configPath); err != nil {
		f.config.LLM = previousLLM
		f.config.EmbeddedLLM = previousEmbedded
		f.configMu.Unlock()
		return fmt.Errorf("failed to persist the embedded LLM state: %w", err)
	}
	f.configLoadErrors = nil
	f.configMu.Unlock()
	f.emitConfigUpdated()
	return nil
}

// embeddedLoaderRef is the lock-free Loader seam the router entry drives. The
// router is first built inside NewApplication — before this subsystem exists —
// and every ToBuilderConfig call site runs under configMu, where st.mu must
// never be taken; resolving the supervisor at CALL time (one atomic load, or
// one short st.mu acquisition) keeps that lock order unchanged.
type embeddedLoaderRef struct {
	f *FrontendAPI
}

// Load makes the model resident, constructing the subsystem first when a
// request arrives before any lifecycle hook built it. It REFUSES while the
// embedded-operation gate is held — the seam all three ungated request paths
// converge on: a request during a repair/reinstall would otherwise cold-load
// the OLD install, whose tree the promote step then deletes under the running
// child. The refusal is an error, not a wait — waiting would charge a
// multi-gigabyte download to a request budget.
func (r embeddedLoaderRef) Load(ctx context.Context) error {
	if op := r.f.embeddedBusyOperation(); op != embeddedOpIdle {
		return embeddedBusyRefusal(op, embeddedOpIdle, "loading the model for a request")
	}
	server := r.f.embedded.loader.Load()
	if server == nil {
		built, _, err := r.f.embeddedBuild()
		if err != nil {
			return err
		}
		server = built
	}
	if server == nil {
		return errors.New("the embedded LLM supervisor is unavailable")
	}
	return server.Load(ctx)
}

// MarkActivity restarts the auto-unload budget. A response that completes
// before the subsystem exists has no budget to restart, so a nil snapshot is a
// no-op rather than an error path.
func (r embeddedLoaderRef) MarkActivity() {
	if server := r.f.embedded.loader.Load(); server != nil {
		server.MarkActivity()
	}
}

// BeginRequest and EndRequest bracket one in-flight request on the supervisor's
// counter — what lets the idle path DEFER an expiry landing mid-generation
// instead of stopping llama-server under its own response. They resolve the
// supervisor from the cache, never by building: Load owns the gate check, and
// this runs on a request goroutine inside http.Client.Do; a no-op when there
// is none (the request that would create it goes through Load first).
func (r embeddedLoaderRef) BeginRequest() {
	if server := r.f.embedded.loader.Load(); server != nil {
		server.BeginRequest()
	}
}

// EndRequest releases one in-flight request. The supervisor's counter is
// idempotent at zero, so a release that finds no supervisor cannot drive it
// negative.
func (r embeddedLoaderRef) EndRequest() {
	if server := r.f.embedded.loader.Load(); server != nil {
		server.EndRequest()
	}
}

// Port is the loopback port the supervisor actually bound, or 0 when none is
// cached — which the transport reads as "do not redirect". The request URL is
// NOT authoritative: the router derives it from the PERSISTED port, a
// preference the load re-checks; without this seam a squatted port would get
// the full prompt POSTed to an unrelated local process. See
// specs/domains/embedded-llm.md.
func (r embeddedLoaderRef) Port() int {
	if server := r.f.embedded.loader.Load(); server != nil {
		return server.Port()
	}
	return 0
}

// The ref satisfies the core seam AND both optional capabilities; the
// assertions are pinned on the REF — the value production actually injects —
// not on *embeddedllm.Server, whose capabilities going unwired is how the
// in-flight bracketing and the live-port redirect once went dead in
// production while every test still passed.
var (
	_ embeddedllm.Loader         = embeddedLoaderRef{}
	_ embeddedllm.RequestTracker = embeddedLoaderRef{}
	_ embeddedllm.PortSource     = embeddedLoaderRef{}
)

// applyEmbeddedLoader attaches the ensure-loaded transport seam to a freshly
// converted BuilderConfig. MUST be called with configMu held (reads
// f.config) and MUST NOT take st.mu or call embeddedBuild — see
// embeddedLoaderRef. The gate is the persisted install state, so the FIRST
// router build already carries the seam; LoadWaitTimeout stays zero so core
// applies its default (deriving it here would take configMu.RLock and
// self-deadlock under the caller's lock).
func (f *FrontendAPI) applyEmbeddedLoader(bc *core.BuilderConfig) {
	if bc == nil || f.config == nil || !f.config.EmbeddedLLM.Installed {
		return
	}
	bc.EmbeddedLLM = core.BuilderEmbeddedLLMConfig{
		ProviderName: config.EmbeddedLLMProviderName,
		Loader:       embeddedLoaderRef{f: f},
	}
}

// syncEmbeddedBuilderSeam mirrors the persisted install state into the
// BUILDER's default seam — the fallback every router build with no Loader of
// its own uses. The per-session orchestrator is built from a plain
// ToBuilderConfig closed over inside NewApplication, so without this default
// its router carries no ensure-loaded transport at all. MUST NOT be called
// with configMu held (embeddedConfig takes it to read).
func (f *FrontendAPI) syncEmbeddedBuilderSeam() {
	b := f.builder()
	if b == nil {
		return
	}
	var seam core.BuilderEmbeddedLLMConfig
	if f.embeddedConfig().Installed {
		seam = core.BuilderEmbeddedLLMConfig{
			ProviderName: config.EmbeddedLLMProviderName,
			Loader:       embeddedLoaderRef{f: f},
		}
	}
	b.SetEmbeddedLLM(seam)
}

// ensureEmbeddedReadyForLLMRequest blocks until the embedded model can answer,
// returning immediately when the request will not be served by it. It exists
// because the transport gates inside http.Client.Do — too late for a caller
// that armed a short budget first: gate BEFORE arming, and the load never
// shortens the request it precedes (ADR-066 D13). The load runs on the app
// context, bounded by the supervisor's wait budget (a walking-away caller must
// not cancel it); concurrent callers coalesce on the single-instance gate.
func (f *FrontendAPI) ensureEmbeddedReadyForLLMRequest(ctx context.Context) error {
	if !f.activeModelIsEmbedded() {
		return nil
	}
	if ctx == nil {
		ctx = f.ctx()
	}
	loadCtx, cancel := context.WithTimeout(ctx, embeddedllm.DefaultLoadWaitTimeout)
	defer cancel()
	if err := (embeddedLoaderRef{f: f}).Load(loadCtx); err != nil {
		return fmt.Errorf("the embedded model could not be loaded: %w", err)
	}
	return nil
}

// Budgets of the agent-idle wait in serviceEmbeddedGate.
const (
	// embeddedServiceIdleWaitInteractive bounds the INTERACTIVE wait (commit
	// message, prompt optimization): short — the user is watching the RPC.
	embeddedServiceIdleWaitInteractive = 20 * time.Second
	// embeddedServiceIdleWaitBackground bounds the BACKGROUND wait (session
	// title generation): tracked, invisible to the user, so it can wait out a
	// task minutes from finishing.
	embeddedServiceIdleWaitBackground = 10 * time.Minute
	// embeddedServiceIdlePoll is the active-session recheck gap (an RLock'd
	// snapshot, cheap to poll).
	embeddedServiceIdlePoll = 2 * time.Second
)

// serviceEmbeddedGate is the pre-dispatch gate for one-shot service LLM calls:
// wait for agent idle, THEN model ready. The idle wait protects the prompt
// cache — the server runs -np 1, so a different prompt would evict the running
// task's next step and cost it a multi-minute prefill; the request DEFERS
// within the caller's budget instead. Readiness is
// ensureEmbeddedReadyForLLMRequest (gate before arming, ADR-066 D13).
func (f *FrontendAPI) serviceEmbeddedGate(ctx context.Context, waitBudget time.Duration) error {
	if !f.activeModelIsEmbedded() {
		// Not the default model: the service request never reaches the local
		// server, so no cache can be disturbed.
		return nil
	}
	if err := f.waitForEmbeddedAgentIdle(ctx, waitBudget); err != nil {
		return err
	}
	return f.ensureEmbeddedReadyForLLMRequest(ctx)
}

// waitForEmbeddedAgentIdle blocks until no session carries live background
// work, the budget expires, or ctx is done. "Busy" is deliberately coarse —
// ANY active session while the embedded model is the default: the precise
// per-session predicate is not observable here, and the false positive is
// cheap (the call waits the task out, as a post-eviction call would have to).
func (f *FrontendAPI) waitForEmbeddedAgentIdle(ctx context.Context, waitBudget time.Duration) error {
	if ctx == nil {
		ctx = f.ctx()
	}
	if f.activeSessionCount == nil {
		// No manager wired (tests, headless embedding): no agent can be
		// running; blocking would be wrong by construction.
		return nil
	}
	if f.activeSessionCount() == 0 {
		return nil
	}
	f.log().Info("deferring an embedded service LLM request until the active task finishes",
		"active_sessions", f.activeSessionCount(), "wait_budget", waitBudget.String())
	deadline := time.NewTimer(waitBudget)
	defer deadline.Stop()
	ticker := time.NewTicker(embeddedServiceIdlePoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf(
				"the embedded model is busy with an active task; the request was deferred for %s to keep that task's prompt cache — retry when it finishes",
				waitBudget)
		case <-ticker.C:
			if f.activeSessionCount() == 0 {
				return nil
			}
		}
	}
}

// serviceEmbeddedGateInteractive is serviceEmbeddedGate for the RPC-backed
// callers (GenerateCommitMessage, OptimizePrompt): a short, visible wait, then
// an actionable refusal.
func (f *FrontendAPI) serviceEmbeddedGateInteractive(ctx context.Context) error {
	return f.serviceEmbeddedGate(ctx, embeddedServiceIdleWaitInteractive)
}

// serviceEmbeddedGateBackground is serviceEmbeddedGate for the session
// manager's title generation: a long invisible wait; the caller skips the
// rename when the gate fails.
func (f *FrontendAPI) serviceEmbeddedGateBackground(ctx context.Context) error {
	return f.serviceEmbeddedGate(ctx, embeddedServiceIdleWaitBackground)
}

// activeModelIsEmbedded reports whether the model a one-shot service request
// resolves to is the backend-owned embedded entry. Service calls run on the
// cached router, whose active model is the configured default — resolving the
// default here answers the same question the router will. The install gate
// comes first: a user's own provider named "embedded" is not the local model.
func (f *FrontendAPI) activeModelIsEmbedded() bool {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	if f.config == nil || !f.config.EmbeddedLLM.Installed {
		return false
	}
	provider, _, err := f.config.LLM.ResolveDefaultModelProvider()
	if err != nil {
		return false
	}
	return provider.Name == config.EmbeddedLLMProviderName
}

// toBuilderConfigLocked converts the live config and attaches the embedded
// loader seam. Callers MUST hold configMu (Lock or RLock); use this instead of
// calling ToBuilderConfig directly on a FrontendAPI path, or the embedded
// entry silently loses its transport.
func (f *FrontendAPI) toBuilderConfigLocked() *core.BuilderConfig {
	bc := ToBuilderConfig(f.config, f.modelProfilesCatalog())
	f.applyEmbeddedLoader(bc)
	return bc
}

// rebuildRouterForEmbeddedTransport re-attaches the ensure-loaded transport to
// the live router — the one place that closes the gap left by NewApplication's
// first build — during the startup restore, before any session can issue a
// request. Runs under saveMu, which rebuildAfterEmbeddedConfigChange requires
// (the config writers it serializes against hold it across their rebuilds).
func (f *FrontendAPI) rebuildRouterForEmbeddedTransport() {
	f.saveMu.Lock()
	defer f.saveMu.Unlock()
	f.rebuildAfterEmbeddedConfigChange()
}

// rebuildAfterEmbeddedConfigChange rebuilds the judge and the LLM router so
// the provider-set change takes effect without a restart. Runs outside the
// config write lock but under saveMu, taking configMu.RLock across snapshot +
// rebuild exactly like UpdateLLMConfig. A rebuild failure is logged, not
// returned: the install succeeded and the config on disk is authoritative.
// The builder-level seam is refreshed FIRST, here — where all three lifecycle
// points (startup restore, completed install, removal) already meet.
func (f *FrontendAPI) rebuildAfterEmbeddedConfigChange() {
	f.syncEmbeddedBuilderSeam()
	b := f.builder()
	if b == nil {
		return
	}
	f.configMu.RLock()
	fresh := f.toBuilderConfigLocked()
	b.RebuildJudge(fresh)
	// Push the fresh model metadata into every LIVE per-session registry:
	// RebuildRouter replaces only what sessions built AFTER this point see,
	// and the push is an idempotent upsert run before the rebuild, so the
	// correction lands even when the rebuild fails.
	b.UpdateModelOverrides(fresh)
	err := b.RebuildRouter(fresh)
	f.configMu.RUnlock()
	if err != nil {
		f.log().Warn("failed to rebuild the LLM router after an embedded LLM config change", "error", err)
	}
}

// scheduleEmbeddedRouterRefresh runs rebuildAfterEmbeddedConfigChange outside
// the caller's critical section. The async half of the context read-back: no
// synchronous rebuild inside Server.Load, but a skipped rebuild is not
// acceptable either — the stale window is the install-time estimate that
// makes the pre-call guard refuse prompts the real window accepts.
// Single-flight: a second schedule sets the dirty flag and the running loop
// reruns once more before closing, so no correction is lost. Tests inject
// embeddedRefreshDispatch to run the work synchronously.
func (f *FrontendAPI) scheduleEmbeddedRouterRefresh() {
	f.embeddedRefreshMu.Lock()
	if f.embeddedRefreshScheduled {
		f.embeddedRefreshDirty = true
		f.embeddedRefreshMu.Unlock()
		return
	}
	f.embeddedRefreshScheduled = true
	f.embeddedRefreshMu.Unlock()

	dispatch := f.embeddedRefreshDispatch
	if dispatch == nil {
		dispatch = func(fn func()) { go fn() }
	}
	dispatch(f.runEmbeddedRouterRefresh)
}

// runEmbeddedRouterRefresh is the scheduled refresh body: one rebuild, plus
// exactly one more when a change landed while this window was open.
func (f *FrontendAPI) runEmbeddedRouterRefresh() {
	for {
		f.rebuildAfterEmbeddedConfigChange()

		f.embeddedRefreshMu.Lock()
		if !f.embeddedRefreshDirty {
			f.embeddedRefreshScheduled = false
			f.embeddedRefreshMu.Unlock()
			return
		}
		f.embeddedRefreshDirty = false
		f.embeddedRefreshMu.Unlock()
	}
}

// firstNonEmbeddedModelID picks the llm.default_model migration target when
// the embedded model is removed; empty means ApplyRemoved REFUSES instead of
// persisting an invalid empty default (never guessing a model the operator
// did not choose). Pinned by
// config.TestEmbeddedLLMRemoveWithNoOtherModelIsRefused.
func firstNonEmbeddedModelID(cfg *config.Config, composite string) string {
	for _, id := range cfg.LLM.AllModelIDs() {
		if id != composite {
			return id
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Lifecycle surface (NOT part of the Wails RPC surface: methods on
// FrontendAPILifecycle are never bound — see frontend_api.go)
// ---------------------------------------------------------------------------

// InitEmbeddedLLM restores the embedded local-model state from manifest.json
// and emits the initial embedded_llm:state snapshot (desktop startup path).
// NO network I/O, NO hardware probe, no model load — the whole call is a
// manifest read plus one event. See specs/domains/embedded-llm.md.
func (l *FrontendAPILifecycle) InitEmbeddedLLM() {
	l.f.initEmbeddedLLM()
}

// StopEmbeddedLLM stops the supervised llama-server if one is running,
// releasing its RAM/VRAM (desktop Shutdown).
func (l *FrontendAPILifecycle) StopEmbeddedLLM(ctx context.Context) error {
	return l.f.stopEmbeddedLLM(ctx)
}
