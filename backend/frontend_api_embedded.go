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
)

// The RPC surface of the embedded local model (see
// specs/domains/embedded-llm.md and specs/contracts/desktop-frontend.md).
//
// Ownership: this file owns the FrontendAPI-side wiring of
// core/embeddedllm — the supervisor, the installer, the config sink and the
// two global events. It owns NO policy: the command line, the resolution, the
// state machine and the idle budget all live in core.
//
// The conventions the surface follows:
//
//   - GetEmbeddedLLMStatus is a read-only getter and therefore returns no
//     error (desktop-frontend.md "RPC Surface"): an unavailable subsystem
//     reports the not-installed state instead of failing.
//   - every mutating method returns an error, and an error it returns is
//     actionable — it names the refused operation and the reason.
//   - the multi-gigabyte install runs in the BACKGROUND: InstallEmbeddedLLM
//     performs only the synchronous gates (single-run, hardware probe, the
//     16 GiB RAM refusal) and then returns, so the RPC never blocks on a
//     download. Progress arrives through embedded_llm:install_progress and a
//     background failure through the runtime_error toast.
//   - startup performs no network I/O and never loads the model
//     (initEmbeddedLLM); it only restores the state from manifest.json.

// Budgets of the synchronous parts of this surface. The install itself is
// unbounded (it is a multi-gigabyte resumable download on a background
// goroutine); these bound only what an RPC waits for.
const (
	// embeddedProbeTimeout bounds the hardware probe the install RPC runs
	// before it commits to a background download. Every external probe command
	// is already bounded inside core (2s each), so this is the outer belt: a
	// machine with every probe helper installed and wedged must still return
	// an actionable error instead of hanging the settings dialog.
	embeddedProbeTimeout = 30 * time.Second
	// embeddedStopTimeout bounds the shutdown stop. The supervisor's own
	// graceful window is DefaultStopTimeout (10s) followed by a kill and a
	// bounded post-kill wait, so this only covers a wedged terminate path —
	// quitting must not hang on a model that refuses to die.
	embeddedStopTimeout = 30 * time.Second
)

// runtime_error codes of this subsystem (the payload's error_code field).
const (
	embeddedErrCodeInstall = "embedded_llm_install_failed"
	embeddedErrCodeRemove  = "embedded_llm_remove_failed"
)

// ---------------------------------------------------------------------------
// DTOs
// ---------------------------------------------------------------------------

// EmbeddedLLMStatus is the payload of GetEmbeddedLLMStatus: the supervision
// state plus the install record the Settings page and the status bar render.
// Every field is always present (no omitempty) so the frontend never has to
// distinguish "absent" from "zero".
type EmbeddedLLMStatus struct {
	// State is the raw supervision state: not_installed | installed | loading
	// | loaded | unloading | error.
	State string `json:"state"`
	// Installed reports that the runtime and the weights are on disk and
	// verified (manifest.json restored). It does NOT imply resident.
	Installed bool `json:"installed"`
	// Installing reports a background install run in flight.
	Installing bool `json:"installing"`
	// Loading reports a weight load in progress (the process is up, /v1/models
	// has not answered yet).
	Loading bool `json:"loading"`
	// Loaded reports a serving model: /v1/models answered with a non-empty
	// model list.
	Loaded bool `json:"loaded"`
	// Packing is the ternary quantization on disk ("PQ2_0" | "PTQ1_0").
	Packing string `json:"packing"`
	// Backend is the accelerator the runtime was provisioned for.
	Backend string `json:"backend"`
	// Port is the persisted loopback port (0 when nothing is installed).
	Port int `json:"port"`
	// ContextSize is the RAM-tiered context frozen in the manifest (0 when
	// nothing is installed).
	ContextSize int `json:"context_size"`
	// AutoUnloadEnabled is the resolved idle-timer master switch.
	AutoUnloadEnabled bool `json:"auto_unload_enabled"`
	// AutoUnloadMinutes is the resolved idle budget in minutes.
	AutoUnloadMinutes int `json:"auto_unload_minutes"`
	// IdleRemainingSeconds is the idle budget left before the process is
	// stopped, 0 when no timer is armed (the model is not loaded or the
	// auto-unload is off).
	IdleRemainingSeconds int64 `json:"idle_remaining_seconds"`
	// BaseURL is the OpenAI-compatible endpoint derived from the port, empty
	// when nothing is installed.
	BaseURL string `json:"base_url"`
	// ModelID is the composite provider/model id the router exposes
	// ("embedded/Bonsai 2 27B"), empty when nothing is installed.
	ModelID string `json:"model_id"`
	// ModelName is the bare model name of the generated provider record.
	ModelName string `json:"model_name"`
	// RuntimeVersion is the pinned fork release the runtime came from.
	RuntimeVersion string `json:"runtime_version"`
	// InstalledAt is the RFC 3339 install timestamp.
	InstalledAt string `json:"installed_at"`
	// ModelFile is the absolute path of the GGUF weights.
	ModelFile string `json:"model_file"`
	// Pid is the OS process id of the supervised server, 0 when no process is
	// running.
	Pid int `json:"pid"`
	// Error is a human-readable cause: the supervisor's message while State is
	// "error", otherwise the last failed install or removal. Empty when nothing
	// failed since the last successful operation.
	Error string `json:"error"`
	// Available reports whether the subsystem could be constructed at all
	// (false only when the agent directory is unset, i.e. before startup).
	Available bool `json:"available"`
}

// EmbeddedLLMStateData is the payload of the global embedded_llm:state event.
// It is deliberately narrower than EmbeddedLLMStatus: it carries exactly what a
// status indicator needs, so a transition event stays cheap. Mirrors
// EmbeddedLLMStateData in frontend/src/types/events.ts.
type EmbeddedLLMStateData struct {
	Installed         bool   `json:"installed"`
	Loading           bool   `json:"loading"`
	Loaded            bool   `json:"loaded"`
	Packing           string `json:"packing"`
	Backend           string `json:"backend"`
	Port              int    `json:"port"`
	ContextSize       int    `json:"context_size"`
	AutoUnloadMinutes int    `json:"auto_unload_minutes"`
	Error             string `json:"error"`
}

// EmbeddedLLMProgressData is the payload of the global
// embedded_llm:install_progress event: one component's one stage. It is the
// frontend-facing shape of core/embeddedllm.Progress (snake_case JSON keys,
// mirroring tool_manager:progress). Mirrors EmbeddedLLMInstallProgressData in
// frontend/src/types/events.ts.
type EmbeddedLLMProgressData struct {
	// Component is the artifact being worked on: runtime | cudart | model |
	// mmproj.
	Component string `json:"component"`
	// Stage is downloading | verifying | extracting | signing | done.
	Stage string `json:"stage"`
	// BytesDone and BytesTotal describe the component's own transfer. Both are
	// 0 for the non-transfer stages (verifying, extracting, signing, done),
	// where a byte count would be a lie.
	BytesDone  int64 `json:"bytes_done"`
	BytesTotal int64 `json:"bytes_total"`
}

// ---------------------------------------------------------------------------
// Subsystem state
// ---------------------------------------------------------------------------

// embeddedLLMState is the FrontendAPI-owned bookkeeping of the embedded local
// model: the storage layout, the supervisor, the installer, the cached install
// record and the in-flight-install flag. It lives on FrontendAPI as the value
// field `embedded`, so the zero value is usable and no construction call is
// needed before the first RPC.
//
// TWO mutexes, deliberately separate:
//
//   - mu guards construction and the install-run bookkeeping. It is NEVER held
//     across a call into the supervisor: core calls OnState synchronously from
//     the transitioning goroutine and that handler reads the cached install
//     record, so holding mu there would deadlock on the first transition.
//   - infoMu guards the cached manifest snapshot, the restore-in-progress flag
//     and the last background failure. It is likewise never held across a
//     supervisor call.
//
// Neither mutex is ever taken while configMu is held, which keeps the lock
// order one-directional: (st.mu | st.infoMu) → configMu.
type embeddedLLMState struct {
	mu        sync.Mutex
	built     bool
	layout    embeddedllm.Layout
	server    *embeddedllm.Server
	installer *embeddedllm.Installer
	// loader is a LOCK-FREE snapshot of server, published the moment the
	// supervisor is constructed. It exists so the router build can reach the
	// supervisor without taking mu: ToBuilderConfig runs while configMu is held
	// at every call site, and mu must never be taken under configMu (the lock
	// order is one-directional, (st.mu | st.infoMu) → configMu). Readers get the
	// current supervisor or nil. Only embeddedBuild writes it, and it writes the
	// winner's instance, so a concurrent build publishes one supervisor.
	loader atomic.Pointer[embeddedllm.Server]
	// installing is true while a background install run is in flight. It is
	// the single-run gate: a second InstallEmbeddedLLM is refused instead of
	// racing the first one for the same staging tree.
	installing bool

	infoMu sync.Mutex
	// manifest is the cached manifest.json snapshot (the durable install
	// record). hasManifest reports whether it describes a real installation;
	// without it the cached value is the zero Manifest.
	manifest    embeddedllm.Manifest
	hasManifest bool
	// muted suppresses the state event of a supervisor transition the caller
	// is about to provoke, because the caller emits ONE explicit snapshot
	// right afterwards (the startup restore, an install completion, a
	// removal). Without it those operations would emit the same state twice.
	// It is only ever set and cleared by the goroutine performing the
	// operation — core calls OnState synchronously — so the window is
	// microseconds wide and never spans another caller's transition.
	muted bool
	// lastError carries the cause of the last failed background operation
	// (install, removal). Cleared when the next one starts.
	lastError string

	// Test seams. All are nil in production, where the core defaults run
	// (ProbeHardware, spawnOSServer, the supervisor's own readiness client and
	// the real Installer.Install). They exist so this package's tests can drive
	// a full install/load/stop cycle without a network, without the pinned
	// multi-gigabyte artifacts (whose SHA256 pins no fake download can
	// satisfy) and without a real llama-server. Mirrors fetchPaperOriginalFn
	// and gitStatusFn.
	probeFn    func(ctx context.Context, logger *slog.Logger) (embeddedllm.Hardware, error)
	spawnFn    embeddedllm.SpawnFunc
	httpClient *http.Client
	installFn  func(ctx context.Context, in *embeddedllm.Installer, opts embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error)
	// portProbeFn substitutes the loopback port prober used by the pre-spawn
	// port scan, so a test can stage a collision without occupying a real port.
	portProbeFn embeddedllm.PortProber
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

// setError records the cause of a failed background operation.
func (s *embeddedLLMState) setError(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	s.infoMu.Lock()
	s.lastError = msg
	s.infoMu.Unlock()
}

// lastFailure returns the recorded background failure ("" when none).
func (s *embeddedLLMState) lastFailure() string {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	return s.lastError
}

// ---------------------------------------------------------------------------
// Construction, restore and teardown
// ---------------------------------------------------------------------------

// embeddedBuild returns the supervisor and the installer, constructing them on
// first use and restoring the on-disk state. Construction and restore are pure
// local work: no network, no hardware probe, no process spawn — which is what
// lets startup call it on the critical path and lets an early RPC call it
// safely.
func (f *FrontendAPI) embeddedBuild() (*embeddedllm.Server, *embeddedllm.Installer, error) {
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
	layout, err := embeddedllm.NewLayout(config.RuntimesDir(f.agentDir), config.EmbeddedModelDir(f.agentDir))
	if err != nil {
		return nil, nil, fmt.Errorf("embedded LLM storage layout: %w", err)
	}

	logger := f.log().With("subsystem", "embedded_llm")
	server := embeddedllm.NewServer(layout, logger)
	server.AutoUnload = f.embeddedAutoUnloadPolicy()
	server.OnState = f.onEmbeddedLLMState
	// The persisted port is a preference, not a reservation: it is re-checked
	// before every spawn and walks upward to the next free one, and a move is
	// written back to config so the generated provider base_url keeps matching
	// the socket the server bound.
	server.EnsurePort = f.embeddedEnsurePort
	installer := embeddedllm.NewInstaller(layout, logger)
	installer.Sink = embeddedConfigSink{f: f}
	// The supervisor owns the process, so the removal stop and the shutdown
	// stop are the same call — the signatures match by design.
	installer.Stop = server.Stop

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
	// A concurrent builder wins; both build the same thing from the same
	// agent dir, so discarding the loser is correct (and it never started a
	// process — construction is inert).
	if st.built {
		server, installer = st.server, st.installer
	} else {
		st.layout, st.server, st.installer, st.built = layout, server, installer, true
	}
	st.mu.Unlock()
	// Publish the winner's supervisor lock-free for the router build, which
	// runs under configMu and therefore must never take st.mu. Storing outside
	// the lock is deliberate: the value is already the settled winner, and a
	// concurrent identical store is idempotent.
	st.loader.Store(server)
	return server, installer, nil
}

// embeddedRestore reads manifest.json into memory and records whether the model
// is installed. This is the WHOLE of the startup work for this subsystem: it
// performs no download, no probe and no load, so startup neither depends on the
// network nor blocks on a multi-gigabyte weight load.
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
			// The bytes are gone but config.yaml still claims an install. The
			// generated provider entry is reconciled away on the next config
			// load; report the mismatch instead of silently serving a model
			// that cannot start.
			logger.Warn("embedded_llm.installed is true but no manifest was found; the model needs a reinstall",
				"path", path)
		}
		return
	}

	st.setInstallRecord(manifest, true)
	// ONE event for the restore: suppress the SetInstalled transition and emit
	// an explicit snapshot in initEmbeddedLLM, so a startup neither double-
	// emits nor stays silent when nothing is installed.
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
// embedded_llm:state snapshot. Called from desktop/startup_phases.go on the
// startup path (InitEmbeddedLLM). It never downloads, probes hardware or loads
// the model.
func (f *FrontendAPI) initEmbeddedLLM() {
	server, _, err := f.embeddedBuild()
	if err != nil {
		// Report the unusable subsystem rather than leaving the UI without an
		// initial snapshot: not_installed is the truth when there is no
		// supervisor to ask.
		f.log().Warn("embedded LLM subsystem unavailable", "error", err)
		f.emitEmbeddedLLMState(embeddedllm.StateNotInstalled, 0, err.Error())
		return
	}
	// The policy is re-applied from the live config: construction read it too,
	// but a config reload between construction and startup must still win.
	server.SetAutoUnload(f.embeddedAutoUnloadPolicy())

	snapshot := server.Status()
	// The router was first built inside NewApplication, which has no
	// FrontendAPI and therefore no loader seam. Re-attaching it here — during
	// the startup restore, before backend:ready and so before any session can
	// issue a request — is what makes a cold request to an idle-unloaded model
	// load it transparently instead of failing to connect. Skipped when nothing
	// is installed: there is no embedded provider entry to guard then, and a
	// router rebuild must not become a cost every machine pays at startup.
	if snapshot.State != embeddedllm.StateNotInstalled {
		f.rebuildRouterForEmbeddedTransport()
	}
	f.emitEmbeddedLLMState(snapshot.State, snapshot.Port, snapshot.Message)
}

// stopEmbeddedLLM stops the supervised server if one is running (Shutdown). It
// is a no-op when the subsystem was never constructed — nothing can be running
// then — and idempotent, so calling it twice (or after Cleanup) is safe.
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
	stopCtx, cancel := context.WithTimeout(ctx, embeddedStopTimeout)
	defer cancel()
	if err := server.Stop(stopCtx); err != nil {
		f.log().Error("failed to stop the embedded LLM server", "error", err)
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
// (Server.EnsurePort): it returns the first free loopback port at or above the
// persisted one, scanning upward one port at a time, and persists a move so the
// generated provider base_url keeps matching the socket the server is about to
// bind.
//
// A persistence failure is deliberately NOT fatal. The server binds the free
// port either way and the ensure-loaded transport redirects every request to
// the live port, so the model stays usable while config.yaml simply keeps the
// previous value until the next successful write; failing here would make a
// multi-gigabyte weight load unusable over an administrative write.
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
// regenerates the backend-owned provider record from it, so the base_url every
// router build derives stays in agreement with the socket the server bound.
//
// manifest.json keeps the port the install allocated: that is the PREFERRED
// port and the next load re-scans from it, so a port that was only temporarily
// taken is reclaimed instead of drifting upward forever.
//
// It does NOT rebuild the router. This runs inside Server.Load — on the request
// path, while the supervisor's single-instance gate is held — and a rebuild
// there would swap the router underneath an in-flight request. None is needed:
// the entry's transport redirects to the live port, and the persisted value is
// what the next rebuild (a settings save, a profile change, a restart) picks up.
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
		// No generated provider record exists to keep in sync, and writing a
		// port into a section that reports "not installed" would read as
		// corrupt state to an operator. The load still proceeds on the free
		// port; only the config write is skipped.
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

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// onEmbeddedLLMState is the core supervisor's StateEvent transport: it forwards
// every observable transition as the global embedded_llm:state event. It runs
// on the goroutine that made the transition and must never call back into the
// supervisor (core documents OnState as non-reentrant), which is why it only
// reads the cached install record and the config.
func (f *FrontendAPI) onEmbeddedLLMState(ev embeddedllm.StateEvent) {
	if f.embedded.stateEventMuted() {
		return
	}
	f.emitEmbeddedLLMState(ev.State, ev.Port, ev.Message)
}

// emitEmbeddedLLMState builds and emits the global embedded_llm:state payload.
// state/port/message come from the transition (or the startup snapshot); the
// install record and the auto-unload budget come from the cached manifest and
// the persisted config. Nil-guarded like every other emitter: most tests do not
// wire emitEvent.
func (f *FrontendAPI) emitEmbeddedLLMState(state embeddedllm.State, port int, message string) {
	if f.emitEvent == nil {
		return
	}
	f.emitEvent(EventEmbeddedLLMState, f.embeddedStatePayload(state, port, message))
}

// embeddedStatePayload merges a supervision state with the install record and
// the persisted config. Precedence is explicit: the live supervisor port wins
// over the persisted one, the manifest (what is actually on disk) wins over the
// informational config copy of packing/backend, and the error field carries the
// supervisor's cause only for the error state — otherwise the last background
// failure, so a failed install is visible in the same field the UI reads.
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
		errText = f.embedded.lastFailure()
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
	}
}

// emitEmbeddedInstallProgress forwards one core Progress update as the global
// embedded_llm:install_progress event. It is the InstallOptions.Progress
// callback of a background install run, so it is called from that goroutine and
// must not block: the emit is a synchronous Wails dispatch, and the core
// downloader already throttles the byte-level updates.
func (f *FrontendAPI) emitEmbeddedInstallProgress(p embeddedllm.Progress) {
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

// GetEmbeddedLLMStatus returns the embedded local-model status: the supervision
// state, the install record and the resolved auto-unload policy.
//
// Read-only getter, so it returns no error (desktop-frontend.md convention):
// when the subsystem cannot be constructed (no agent directory — only possible
// before startup) it reports Available=false with the not-installed state
// instead of failing. It performs no network I/O and no hardware probe; the only
// disk access is the manifest restore on the first call.
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
		Error:             f.embedded.lastFailure(),
	}
	if manifest, ok := f.embedded.installRecord(); ok {
		// The manifest is what is actually on disk; the config copy of
		// packing/backend is informational. The port falls back to it as well,
		// so a status read still reports the endpoint the supervisor would
		// launch even before the config was synced.
		status.Packing = string(manifest.Packing)
		status.Backend = string(manifest.Backend)
		status.ContextSize = manifest.ContextSize
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

	server, _, err := f.embeddedBuild()
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
	return status
}

// InstallEmbeddedLLM provisions the pinned runtime and weights for this machine.
//
// The synchronous part is only the gates: one install at a time, a bounded
// hardware probe, and the 16 GiB RAM refusal — so an undersized machine gets an
// actionable error from THIS call and not one byte is downloaded. Everything
// heavy (the multi-gigabyte resumable download, verification, extraction, the
// macOS provisioning and smoke test) then runs on a background goroutine: the
// RPC returns as soon as the run is started.
//
// Progress arrives as embedded_llm:install_progress (one event per component
// and stage) and the outcome as embedded_llm:state; a background failure
// additionally raises a runtime_error toast, because no RPC is left to carry
// it. The install is resumable: verified artifacts are kept as cache hits, so a
// retry continues where the previous attempt stopped.
func (f *FrontendAPI) InstallEmbeddedLLM() error {
	server, installer, err := f.embeddedBuild()
	if err != nil {
		return err
	}
	if !f.beginEmbeddedInstall() {
		return errors.New("an embedded LLM install is already running")
	}

	// The RAM gate runs HERE, synchronously: D6 refuses a machine below 16 GiB,
	// and a refusal the caller only learns about from a toast ten minutes into
	// a download is not actionable. The probe is local and bounded (each
	// external helper carries its own 2s budget inside core). A synchronous
	// refusal returns the error to the caller and raises NO toast: the rejected
	// promise is the report, and a toast on top of it would say it twice.
	probeCtx, cancel := context.WithTimeout(f.ctx(), embeddedProbeTimeout)
	defer cancel()
	hw, err := f.embeddedProbe(probeCtx)
	if err != nil {
		refusal := fmt.Errorf("the embedded LLM install was refused: %w", err)
		f.refuseEmbeddedInstall(refusal)
		return refusal
	}
	if hw.RAMGiB < embeddedllm.MinRAMGiB {
		refusal := fmt.Errorf("%w: this machine reports %.1f GiB of system RAM",
			embeddedllm.ErrInsufficientRAM, hw.RAMGiB)
		f.refuseEmbeddedInstall(refusal)
		return refusal
	}

	go f.runEmbeddedInstall(server, installer, hw)
	return nil
}

// RemoveEmbeddedLLM stops a running server and removes the runtime, the weights
// and the manifest, then clears the backend-owned provider record (migrating
// llm.default_model off the embedded composite in the same operation).
//
// Blocking: the work is local file removal plus a config save. Verified
// artifacts of an unrelated component are never touched, and the flat
// embedding-model files sharing <agentDir>/models survive.
func (f *FrontendAPI) RemoveEmbeddedLLM() error {
	server, installer, err := f.embeddedBuild()
	if err != nil {
		return err
	}
	if f.embeddedInstalling() {
		return errors.New("an embedded LLM install is running; wait for it to finish before removing the model")
	}
	f.embedded.setError(nil)

	if err := installer.Remove(f.ctx()); err != nil {
		f.embedded.setError(err)
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
		// Only reachable when a process is still live, which Remove's own stop
		// should have made impossible. Report it honestly rather than claiming
		// the model is gone while it holds gigabytes.
		f.embedded.setError(err)
		return fmt.Errorf("the local model was removed from disk but its server is still running: %w", err)
	}
	return nil
}

// LoadEmbeddedLLM starts the local server and blocks until the model can
// actually answer — readiness is a non-empty /v1/models response, never a
// health check. It is idempotent (a loaded model is a no-op) and single-
// instance: concurrent calls cannot start a second process.
//
// Blocking on purpose: a load is an explicit user action whose outcome the
// caller needs, and the weight load it waits for is bounded by the supervisor's
// ready budget. The embedded_llm:state events (loading → loaded | error) let
// the UI show progress while the promise is pending, and the idle budget starts
// only once the load completes, so a slow load never eats it.
func (f *FrontendAPI) LoadEmbeddedLLM() error {
	server, _, err := f.embeddedBuild()
	if err != nil {
		return err
	}
	if f.embeddedInstalling() {
		return errors.New("an embedded LLM install is running; wait for it to finish before loading the model")
	}
	f.embedded.setError(nil)

	if err := server.Load(f.ctx()); err != nil {
		if errors.Is(err, embeddedllm.ErrNotInstalled) {
			return errors.New("the embedded LLM is not installed — install it before loading it")
		}
		// The state event already carries the supervisor's cause (including the
		// tail of the server's own log); return the same detail to the caller.
		return err
	}
	return nil
}

// UnloadEmbeddedLLM stops the server process, deterministically returning its
// RAM/VRAM. The bytes stay on disk, so the state afterwards is "installed"
// (unloaded) and a later Load restarts it without a download.
//
// Blocking and idempotent: unloading a model that is not resident succeeds. A
// stop that did not take is reported as an error and keeps the state honest
// (error) instead of claiming the memory was released.
func (f *FrontendAPI) UnloadEmbeddedLLM() error {
	server, _, err := f.embeddedBuild()
	if err != nil {
		return err
	}
	return server.Unload(f.ctx())
}

// SetEmbeddedLLMAutoUnload persists the idle-unload policy
// (embedded_llm.auto_unload) and applies it to the supervisor immediately: the
// timer is re-armed against the existing activity stamp while the model is
// loaded, and disarmed when the policy is off.
//
// minutes must be at least 1 — the same rule config validation enforces, so a
// rejected value never reaches config.yaml. The setting is an operator
// preference, not install state: it survives a removal and applies to the next
// install.
func (f *FrontendAPI) SetEmbeddedLLMAutoUnload(enabled bool, minutes int) error {
	if minutes < 1 {
		return fmt.Errorf("the auto-unload budget must be at least 1 minute (got %d)", minutes)
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
	previous := f.config.EmbeddedLLM.AutoUnload
	enabledValue, minutesValue := enabled, minutes
	f.config.EmbeddedLLM.AutoUnload = config.AutoUnloadConfig{
		Enabled: &enabledValue,
		Minutes: &minutesValue,
	}
	// Persist under configMu so a failed write restores the exact prior value
	// before any reader observes the new one (the UpdateLLMConfig pattern).
	if err := config.Save(f.config, f.configPath); err != nil {
		f.config.EmbeddedLLM.AutoUnload = previous
		f.configMu.Unlock()
		return fmt.Errorf("failed to persist the auto-unload setting: %w", err)
	}
	f.configMu.Unlock()
	f.emitConfigUpdated()

	if server != nil {
		server.SetAutoUnload(embeddedllm.NewAutoUnload(enabled, minutes))
		f.emitEmbeddedStateFrom(server)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Background install run
// ---------------------------------------------------------------------------

// beginEmbeddedInstall claims the single install slot. False means a run is
// already in flight.
func (f *FrontendAPI) beginEmbeddedInstall() bool {
	st := &f.embedded
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.installing {
		return false
	}
	st.installing = true
	return true
}

// embeddedInstalling reports whether a background install run is in flight.
func (f *FrontendAPI) embeddedInstalling() bool {
	st := &f.embedded
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.installing
}

// endEmbeddedInstall releases the install slot.
func (f *FrontendAPI) endEmbeddedInstall() {
	st := &f.embedded
	st.mu.Lock()
	st.installing = false
	st.mu.Unlock()
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
// background goroutine. ctx is the APPLICATION context, not the RPC's: the run
// must outlive the call that started it and stop only when the app quits.
func (f *FrontendAPI) runEmbeddedInstall(server *embeddedllm.Server, installer *embeddedllm.Installer, hw embeddedllm.Hardware) {
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("the embedded LLM install panicked: %v", r)
			f.log().Error("panic during the embedded LLM install", "panic", r)
			f.failEmbeddedInstall(err)
			f.emitEmbeddedStateFrom(server)
			f.endEmbeddedInstall()
		}
	}()

	ctx := f.ctx()
	if ctx == nil {
		ctx = context.Background()
	}

	// A per-run copy of the installer pins the probe result the RPC already
	// paid for, so the background run neither re-probes nor races a second
	// install over the shared field. Copying is safe: Installer is a plain
	// struct of injectable fields with no internal synchronization.
	run := *installer
	run.Probe = func(context.Context, *slog.Logger) (embeddedllm.Hardware, error) { return hw, nil }

	opts := embeddedllm.InstallOptions{
		Platform: hw.Platform,
		Backend:  hw.Backend,
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
		// Unreachable for the real Installer (a nil error always carries a
		// report); reported explicitly rather than recovered from a nil deref.
		err = errors.New("the embedded LLM install finished without reporting its result")
	}
	if err != nil {
		f.failEmbeddedInstall(err)
		f.emitEmbeddedStateFrom(server)
		f.endEmbeddedInstall()
		return
	}

	f.embedded.setError(nil)
	f.embedded.setInstallRecord(report.Manifest, true)
	// The sink already persisted the config and rebuilt the router; what is
	// left is the supervisor's view of the disk. ONE event for the whole
	// operation: the transition is muted and the snapshot below is emitted
	// unconditionally, so a repair install (whose state does not change) still
	// reports that it finished.
	f.embedded.muteStateEvent(true)
	if err := server.SetInstalled(true); err != nil {
		f.log().Warn("the embedded LLM supervisor refused the installed state", "error", err)
	}
	f.embedded.muteStateEvent(false)
	server.SetAutoUnload(f.embeddedAutoUnloadPolicy())
	f.log().Info("embedded LLM install complete",
		"backend", report.Manifest.Backend, "packing", report.Manifest.Packing,
		"port", report.Manifest.Port, "context_size", report.Manifest.ContextSize)
	f.emitEmbeddedStateFrom(server)
	// The slot is released LAST: it is what an observer (the status RPC, the
	// UI) reads as "the run is over", so every effect of the run — the config
	// save, the supervisor state and the completion event — is already visible
	// through this mutex by the time it flips.
	f.endEmbeddedInstall()
}

// refuseEmbeddedInstall releases the install slot and records a SYNCHRONOUS
// refusal (the single-run gate, an unreadable hardware probe, the 16 GiB RAM
// gate). It raises no toast: the RPC returns the error to its caller, which is
// the report, and a toast on top of it would say the same thing twice.
func (f *FrontendAPI) refuseEmbeddedInstall(err error) {
	f.endEmbeddedInstall()
	f.embedded.setError(err)
	f.log().Warn("embedded LLM install refused", "error", err)
}

// failEmbeddedInstall records a BACKGROUND failure: the cause is kept for the
// status/event error field, and a runtime_error toast is raised because no RPC
// is left to carry it. The caller releases the install slot afterwards, so the
// failure is fully reported before the run looks finished.
func (f *FrontendAPI) failEmbeddedInstall(err error) {
	f.embedded.setError(err)
	f.log().Error("embedded LLM install failed", "error", err)
	f.emitEmbeddedRuntimeError(embeddedErrCodeInstall,
		"The local model could not be installed: "+err.Error())
}

// embeddedBaseURL derives the OpenAI-compatible endpoint from a loopback port.
// Deriving (never storing) it is what keeps a port reallocation from leaving a
// stale endpoint behind — the same rule backend/config applies to the generated
// provider record.
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

// embeddedConfigSink is the production half of the core → config boundary: the
// only channel through which the subsystem reaches config.yaml (core cannot
// import backend/config). Its state mutation is exactly the reference
// implementation pinned by backend/config/embedded_llm_sink_test.go; on top of
// it, this one persists the file and rebuilds the LLM router so a freshly
// installed model is usable without a restart.
type embeddedConfigSink struct{ f *FrontendAPI }

var _ embeddedllm.ConfigSink = embeddedConfigSink{}

// ApplyInstalled writes embedded_llm.* from the install record, fills the
// auto-unload defaults WITHOUT overwriting an explicit operator choice (both
// knobs are pointers, so nil is distinguishable from an explicit false), and
// regenerates the backend-owned provider entry plus the context-window override
// from the authoritative state.
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
	f.config.EmbeddedLLM = config.EmbeddedLLMConfig{
		Installed:      true,
		Packing:        string(state.Packing),
		Backend:        string(state.Backend),
		Port:           state.Port,
		ModelFile:      state.ModelFile,
		RuntimeVersion: state.RuntimeVersion,
		InstalledAt:    state.InstalledAt,
		AutoUnload:     autoUnload,
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
// embedded composite (an empty default fails validation, so the composite must
// MOVE to another enabled model rather than simply be cleared) and drops the
// provider record. The auto-unload knobs are operator settings, not install
// state, so they survive.
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
	autoUnload := f.config.EmbeddedLLM.AutoUnload
	f.config.EmbeddedLLM = config.EmbeddedLLMConfig{AutoUnload: autoUnload}
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

// saveOrRollback persists the mutated config and restores the exact prior state
// when the write fails, so a failed save is indistinguishable from a rejected
// one. Called with configMu HELD; it releases the lock before returning, and
// emits config:updated only after a successful write.
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

// embeddedLoaderRef is the lock-free Loader seam the router entry drives.
//
// It exists because of an ordering fact, not for convenience: the router is
// first built inside NewApplication — before this subsystem has been
// constructed — and every ToBuilderConfig call site runs while configMu is
// held, where st.mu must never be taken (the lock order is one-directional,
// (st.mu | st.infoMu) → configMu). A direct *Server reference would therefore
// either be nil at the first build, or require inverting the lock order and
// risk a deadlock.
//
// The indirection resolves the supervisor at CALL time instead of at build
// time, so the transport can be attached before the supervisor exists and still
// reach the real one later. Reads are a single atomic load: no mutex, no
// configMu re-entry, no change to the lock order.
type embeddedLoaderRef struct {
	f *FrontendAPI
}

// Load makes the model resident, constructing the subsystem first when a
// request arrives before any lifecycle hook built it. That construction is pure
// local work and takes configMu.RLock internally, which is safe here: a request
// goroutine holds no configMu, so neither lock-order rule nor self-deadlock
// applies.
func (r embeddedLoaderRef) Load(ctx context.Context) error {
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

// The ref satisfies the core seam; the assertion keeps the wiring honest if
// either side drifts.
var _ embeddedllm.Loader = embeddedLoaderRef{}

// applyEmbeddedLoader attaches the ensure-loaded transport seam to a freshly
// converted BuilderConfig, so the embedded provider's router entry starts a
// cold model before its first request goes out and restarts the idle budget on
// every completed response.
//
// MUST be called with configMu held (it reads f.config) and MUST NOT take st.mu
// or call embeddedBuild — see embeddedLoaderRef for why the loader resolves
// lazily instead.
//
// The gate is the persisted install state, not the presence of a supervisor: it
// is true from the config load onwards, so the very first router build already
// carries the seam. It is also what leaves a user's own unrelated provider that
// happens to be named "embedded" untouched while the local model is not
// installed.
//
// LoadWaitTimeout deliberately stays zero so core applies
// embeddedllm.DefaultLoadWaitTimeout. Deriving it here would need
// embeddedAutoUnloadPolicy, which takes configMu.RLock and would self-deadlock
// under the caller's lock.
func (f *FrontendAPI) applyEmbeddedLoader(bc *core.BuilderConfig) {
	if bc == nil || f.config == nil || !f.config.EmbeddedLLM.Installed {
		return
	}
	bc.EmbeddedLLM = core.BuilderEmbeddedLLMConfig{
		ProviderName: config.EmbeddedLLMProviderName,
		Loader:       embeddedLoaderRef{f: f},
	}
}

// syncEmbeddedBuilderSeam mirrors the persisted install state into the BUILDER's
// default seam, the one every router build falls back to when its own
// BuilderConfig carries no Loader (core.OrchestratorBuilder.SetEmbeddedLLM).
//
// applyEmbeddedLoader decorates one BuilderConfig and therefore only reaches the
// routers built from it. The per-session orchestrator is not one of them: the
// session factory in backend/application.go converts the live config directly,
// because it was closed over inside NewApplication — before any FrontendAPI, and
// so before any supervisor, existed. Without this default the session router
// carries no ensure-loaded transport, and a chat request to a cold model is
// dispatched straight to a loopback socket nothing is listening on: the exact
// failure the transport exists to prevent, on the one path that matters most.
//
// MUST NOT be called with configMu held (embeddedConfig takes it for reading).
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
// and returns immediately when the request will not be served by it.
//
// It exists for the one thing the ensure-loaded transport cannot do. The
// transport gates inside http.Client.Do, so it protects the wire — but a caller
// that has ALREADY armed a short request budget gets that budget eaten by the
// wait: time the supervisor legitimately needs to map gigabytes of weights
// counts against a call configured for 2 minutes. So the short-budget paths call
// this FIRST, and create their timeout context only afterwards, which is what
// makes the requirement hold: no LLM request starts before the model is loaded
// and ready, and the load never shortens the request it precedes.
//
// The load runs under the app context, not the caller's, bounded by the
// supervisor's own wait budget: an expired service budget must not abort a load
// that is legitimately in progress (the transport detaches for the same
// reason), and a caller that walks away must not be able to cancel it either.
// Concurrent callers coalesce on the supervisor's single-instance gate.
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

// activeModelIsEmbedded reports whether the model a one-shot service request
// resolves to is the backend-owned embedded entry.
//
// Service calls run on the cached router, whose active model is the configured
// default (buildRouter applies llm.default_model via SetModel), so resolving the
// default here answers the same question the router will. The install gate comes
// first: with nothing installed there is no embedded entry to resolve to, and a
// user's own provider that happens to be named "embedded" must not be treated as
// the local model.
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
// loader seam. Callers MUST hold configMu (Lock or RLock) — exactly the
// precondition every ToBuilderConfig call site already satisfies. Use this
// instead of calling ToBuilderConfig directly on a FrontendAPI path, or the
// embedded entry silently loses its transport.
func (f *FrontendAPI) toBuilderConfigLocked() *core.BuilderConfig {
	bc := ToBuilderConfig(f.config, f.modelProfilesCatalog())
	f.applyEmbeddedLoader(bc)
	return bc
}

// rebuildRouterForEmbeddedTransport re-attaches the ensure-loaded transport to
// the live router. The router is first built inside NewApplication, which has no
// FrontendAPI and therefore no loader seam; this is the one place that closes
// the gap, and it runs during the startup restore — before backend:ready, so
// before any session can issue a request. It takes saveMu because
// rebuildAfterEmbeddedConfigChange is contracted to run under it (the config
// writers it serializes against hold saveMu across their own rebuild).
func (f *FrontendAPI) rebuildRouterForEmbeddedTransport() {
	f.saveMu.Lock()
	defer f.saveMu.Unlock()
	f.rebuildAfterEmbeddedConfigChange()
}

// rebuildAfterEmbeddedConfigChange rebuilds the judge and the LLM router so the
// provider set change takes effect for new sessions without a restart. Runs
// OUTSIDE the config write lock (readers stay responsive) but still under
// saveMu, and takes configMu.RLock across snapshot + rebuild exactly like
// UpdateLLMConfig, so a concurrent profile/model writer cannot be rolled back.
// A rebuild failure is logged, not returned: the install itself succeeded and
// the config on disk is authoritative — the next load picks it up.
//
// The builder-level seam is refreshed FIRST, and it is refreshed here rather
// than at each of the three lifecycle points because this is the one place all
// three already meet (startup restore, completed install, removal): it keeps the
// default every FUTURE per-session router is built with in step with the
// persisted install state, so a removal cannot leave a session router guarding a
// provider the user then creates themselves under the same name.
func (f *FrontendAPI) rebuildAfterEmbeddedConfigChange() {
	f.syncEmbeddedBuilderSeam()
	b := f.builder()
	if b == nil {
		return
	}
	f.configMu.RLock()
	fresh := f.toBuilderConfigLocked()
	b.RebuildJudge(fresh)
	err := b.RebuildRouter(fresh)
	f.configMu.RUnlock()
	if err != nil {
		f.log().Warn("failed to rebuild the LLM router after an embedded LLM config change", "error", err)
	}
}

// firstNonEmbeddedModelID picks the migration target for llm.default_model when
// the embedded model is removed. Empty when it was the only enabled model:
// ApplyDefaults fills the first available model on the next load, and a config
// with no provider at all is already invalid independently of this subsystem.
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
// and emits the initial embedded_llm:state snapshot. Called from
// desktop/startup_phases.go on the startup path.
//
// It performs NO network I/O, NO hardware probe and never loads the model:
// startup must neither depend on the network nor block on a multi-gigabyte
// weight load. The whole call is a manifest read plus one event.
func (l *FrontendAPILifecycle) InitEmbeddedLLM() {
	l.f.initEmbeddedLLM()
}

// StopEmbeddedLLM stops the supervised llama-server if one is running,
// releasing its RAM/VRAM. Called from desktop Shutdown; idempotent and a no-op
// when the subsystem was never constructed.
func (l *FrontendAPILifecycle) StopEmbeddedLLM(ctx context.Context) error {
	return l.f.stopEmbeddedLLM(ctx)
}
