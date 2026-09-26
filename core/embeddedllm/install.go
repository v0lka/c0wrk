package embeddedllm

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/v0lka/sp4rk/pathutil"
)

// Progress stages reported through InstallProgressFunc. One component walks
// downloading → verifying → (extracting → signing for runtime archives) → done.
const (
	StageDownloading = "downloading"
	StageVerifying   = "verifying"
	StageExtracting  = "extracting"
	StageSigning     = "signing"
	StageDone        = "done"
)

// Progress is one per-component install update. Every component of the
// resolved set reports its own stream, so the UI can show a separate bar for
// the runtime, the Windows CUDA runtime DLLs, the weights and the projector
// (ADR-066 D9).
type Progress struct {
	Component  Component `json:"component"`
	Stage      string    `json:"stage"`
	BytesDone  int64     `json:"bytes_done"`
	BytesTotal int64     `json:"bytes_total"`
}

// InstallProgressFunc receives one Progress update per component and stage.
// It may be nil. The type is named apart from download.go's ProgressFunc —
// that one reports raw (done, total) byte counts for a single artifact, this
// one reports the component-and-stage stream the install UI renders.
type InstallProgressFunc func(Progress)

// Manifest is <models>/bonsai-2-27b/manifest.json — the durable record of what
// was installed. Startup trusts THIS, not the network and not a probe: reading
// it is the only disk work the boot path performs for this subsystem.
//
// It is written once, atomically, and only after every component has been
// downloaded, SHA256-verified and (for the runtime) extracted, signed and
// smoke-tested. A failed install therefore never leaves a manifest describing
// bytes that are not on disk.
type Manifest struct {
	Packing        Packing           `json:"packing"`
	Backend        Backend           `json:"backend"`
	RuntimeVersion string            `json:"runtime_version"`
	Checksums      map[string]string `json:"checksums"` // component -> verified sha256
	Port           int               `json:"port"`

	// ContextSize is the LAST KNOWN EFFECTIVE context of this installation —
	// the number the `llm.models."Bonsai 2 27B".context_window` override is
	// generated from, and the value a launch falls back to when no plan was
	// recorded.
	//
	// It is a record that is *corrected*, not a decision that is *frozen*:
	// install writes the planner's figure, and every successful load overwrites
	// it with the context the server itself reports through `/props` (see
	// `Server.recordEffectiveContext`). That readback is what keeps the tier-1
	// config override honest — `contracts`/`llm-providers.md` gives a tier-1
	// override precedence over the tier-1.5 lazy probe, so a stale one could
	// never be corrected by the probe and would silently misreport the model's
	// real window forever.
	//
	// A FIT-SIZED plan has no concrete context to record (the runtime's own fit
	// pass chooses it at launch, and `MemoryPlan.ContextSize` is 0 there), so
	// the install records `FitMinContext` — the floor fit is held to — as the
	// estimate the override needs, and the first successful load replaces it
	// with the measured value. It never records 0: a zero override means "leave
	// the existing one alone" to `SyncEmbeddedLLMProvider`, and a zero context
	// in a manifest would read as a lost tier.
	ContextSize int `json:"context_size"`

	ModelFile   string `json:"model_file"`
	InstalledAt string `json:"installed_at"` // RFC 3339

	// Topology is the device-memory snapshot the recorded plan was made from —
	// the accelerators the provisioned runtime could see, whether their memory
	// aliases host RAM, and the two budgets the memory gate was allowed to
	// spend. It is persisted for two reasons. A load re-probes only
	// opportunistically and must fail SOFT, so the snapshot is what a wedged or
	// absent driver query falls back to; and a support bundle carries the
	// machine's real shape as measured at provision time rather than as guessed
	// from a backend name.
	//
	// Nil means "no probe ever answered" — a first install whose runtime would
	// not enumerate its devices, or a manifest written before this field
	// existed. A reader must treat nil as UNKNOWN, never as "no accelerator":
	// `MemoryTopology`'s own contract is that its zero value means unknown, and
	// a zero budget read as a fact is the refusal the combined gate exists to
	// avoid.
	Topology *MemoryTopology `json:"topology,omitempty"`

	// Plan is the launch shape LAST APPLIED to this installation: every
	// flag-bearing value `LaunchSpec` renders, the two footprints it expected,
	// the budgets it was gated against, and the human-readable `Notes` saying
	// why each non-default decision was made. `Server.launchSpec` builds the
	// shape half of a launch from it, so a load reproduces the decision the
	// install made instead of re-deriving a different one from a coarser policy.
	//
	// Nil means "no plan was recorded" — a manifest written before this field
	// existed — and a load then falls back to the pure policy in resolve.go
	// (explicit `-ngl`, `-fit off`, the manifest's `ContextSize`). Persisting
	// the plan rather than re-deriving it at load is deliberate: re-deriving
	// would need the operator's `Tuning`, and `tuning` is an operator SETTING
	// the sink carries verbatim, not install state a record may copy — a second
	// copy would be a second source of truth that goes stale on the first edit.
	Plan *MemoryPlan `json:"plan,omitempty"`

	// PackingReason says WHY this packing was chosen. It is recorded because
	// every value other than "default" is a degradation of some kind — a backend
	// with no PQ2_0 kernels, a pin that predates an upstream fix, a GPU
	// generation that decodes the smaller packing faster, or a measured budget
	// PQ2_0 did not fit — and a degraded install must state its reason instead
	// of leaving the user to infer it from a file size. Empty on a manifest
	// written before this field existed, which readers must treat as "unknown",
	// never as "default".
	PackingReason PackingReason `json:"packing_reason,omitempty"`

	// GPUFamily is the accelerator generation the plan was classified as, empty
	// when no device probe answered. Recorded so a support bundle carries the
	// silicon the guards and the packing rule were reasoning about.
	GPUFamily GPUFamily `json:"gpu_family,omitempty"`

	// Guards are the backend compatibility decisions that were in force when
	// this install was planned — each with its typed reason, its severity, its
	// upstream issue citation, its guidance, and an Applied flag saying whether
	// the plan actually changed because of it. They are persisted, not just
	// returned, because the whole point is that a degraded install stays
	// visible: a guard that fired is a fact about THIS install and must still be
	// readable after the process that made it exited.
	Guards []GuardDecision `json:"guards,omitempty"`
}

// ErrNotInstalled reports that no manifest exists, i.e. the model is not
// installed. It is a normal state, not a fault.
var ErrNotInstalled = errors.New("embedded LLM is not installed")

// ErrSmokeTestFailed reports that the freshly provisioned llama-server did not
// run. On macOS the cause is almost always Gatekeeper still blocking an
// unsigned binary; the wrapped message carries the fix.
var ErrSmokeTestFailed = errors.New("the provisioned llama-server did not run")

// ReadManifest loads the manifest at path. A missing file is reported as
// ErrNotInstalled rather than a generic OS error, because "not installed" is
// the state it encodes.
func ReadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Manifest{}, fmt.Errorf("%w (no manifest at %s)", ErrNotInstalled, path)
		}
		return Manifest{}, fmt.Errorf("embeddedllm: reading manifest %q: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("embeddedllm: parsing manifest %q: %w", path, err)
	}
	return m, nil
}

// manifestTempName is the sibling temporary file the atomic write renames from.
const manifestTempName = ManifestFileName + ".tmp"

// writeManifest persists m atomically: bytes land in a sibling temporary file
// and are renamed over the target, so a crash mid-write leaves the previous
// manifest intact instead of a truncated one.
func writeManifest(path string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("embeddedllm: encoding manifest: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := mkdirAll(dir); err != nil {
		return err
	}
	tmp := filepath.Join(dir, manifestTempName)
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("embeddedllm: writing %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("embeddedllm: promoting manifest into place: %w", err)
	}
	return nil
}

// recordableContext is the context a MANIFEST may carry for a plan.
//
// A planner-computed shape has one: `MemoryPlan.ContextSize` is always positive
// when `Fit` is false, because with fit off nothing else would size it. A
// FIT-SIZED shape does not — `ContextSize` is 0 there by definition, since the
// runtime's own fit pass chooses the value at launch — but a manifest still owes
// its readers a number: `ContextSize` is what the `llm.models` `context_window`
// override is generated from, and a 0 there means "leave the existing override
// alone" rather than "the window is zero", so the honest figure would be lost.
//
// `FitMinContext` is that figure. It is the floor fit is held to (`-fitc`), so
// it is the smallest context the launch can legitimately end up with, and the
// first successful load replaces it with the value the server actually reports
// through `/props` — see `Manifest.ContextSize` and
// `Server.recordEffectiveContext`. Recording the floor instead of 0 is the
// conservative direction in both uses: an override that under-reports the window
// costs compaction headroom, while one that claims 0 costs the model its place
// in the context accounting entirely.
//
// fallback is the value a plan-less resolution already carries (`Resolution.
// ContextSize`), used when the plan is absent and for the RAM-tiered path.
func recordableContext(plan MemoryPlan, fallback int) int {
	if plan.Fit {
		if plan.FitMinContext > 0 {
			return plan.FitMinContext
		}
		return fallback
	}
	if plan.ContextSize > 0 {
		return plan.ContextSize
	}
	return fallback
}

// InstallState is the durable install record handed to the config layer. It
// maps one-to-one onto config.EmbeddedLLMConfig plus the resolved context
// tier, and it is the ONLY channel through which this subsystem reaches
// config.yaml — core never imports backend/config (backend/config sits above
// core and already imports core packages, so an import back would cycle).
type InstallState struct {
	Packing        Packing
	Backend        Backend
	Port           int
	ModelFile      string
	RuntimeVersion string
	InstalledAt    string // RFC 3339, matching Manifest.InstalledAt
	ContextSize    int

	// AutoUnloadEnabled and AutoUnloadMinutes are the DEFAULTS the install
	// establishes. The sink must apply them only where the operator has not
	// chosen explicitly (config.AutoUnloadConfig keeps both as pointers, so
	// "unset" is distinguishable from "explicitly false"), and must never
	// overwrite an existing choice: installing the model does not reset a
	// tuned idle budget.
	AutoUnloadEnabled bool
	AutoUnloadMinutes int

	// There are deliberately NO memory-tuning defaults here. `Tuning`'s zero
	// value IS the all-Auto plan, and config.TuningConfig keeps every knob a
	// pointer precisely so "the operator never wrote this" stays
	// distinguishable from "the operator wrote auto" — so an install that
	// shipped defaults for the sink to apply would collapse that distinction
	// on the first provision. What the sink owes the tuning section is
	// narrower and stronger: carry it through verbatim (see ConfigSink).
}

// ConfigSink persists install state into config.yaml. The backend layer
// supplies the implementation (backend/frontend_api_embedded.go):
//
//   - ApplyInstalled writes embedded_llm.* from state, (re)applies the
//     auto-unload defaults without overwriting explicit operator values, calls
//     Config.SyncEmbeddedLLMProvider(state.ContextSize) so the backend-owned
//     llm.openai_compatible.embedded entry and the llm.models context_window
//     override are generated from the authoritative state, persists, and
//     rebuilds the router.
//   - ApplyRemoved clears embedded_llm.installed and the informational fields,
//     migrates llm.default_model off "embedded/Bonsai 2 27B" (otherwise the
//     next load fails validation and the next settings save is rejected as
//     dangling), calls SyncEmbeddedLLMProvider(0) so the provider entry is
//     dropped, and persists.
//
// BOTH rewrite embedded_llm.* wholesale, so both must carry the two
// operator-owned sub-sections through unchanged: `auto_unload` (applying
// state.AutoUnload* to unset knobs only) and `tuning` (verbatim — it is a
// setting, not a record, so neither provisioning nor uninstalling the model
// may reset the memory plan the operator chose).
//
// Both must be idempotent: Install and Remove may be retried after a partial
// failure.
type ConfigSink interface {
	ApplyInstalled(ctx context.Context, state InstallState) error
	ApplyRemoved(ctx context.Context) error
}

// CommandRunner executes one external command and returns its combined output.
// It exists so the macOS provisioning steps (xattr, codesign, the --version
// smoke test) are testable on every platform without those binaries present.
type CommandRunner func(ctx context.Context, name string, args ...string) (string, error)

// defaultCommandRunner is the production runner. Every call site bounds it with
// its own context timeout, so a wedged codesign cannot stall the install.
func defaultCommandRunner(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// Auto-unload defaults established by an install. The minutes value mirrors
// config.EmbeddedLLMDefaultAutoUnloadMinutes; core cannot import that constant
// (see InstallState), so it is declared here and pinned equal by
// TestEmbeddedAutoUnloadDefaultsMatchCore in backend/config.
const (
	DefaultAutoUnloadEnabled = true
	DefaultAutoUnloadMinutes = 60
)

// macOS provisioning budgets. Bounded like the hardware probe: a stuck
// codesign must not hang an install forever.
const (
	macOSCommandTimeout = 2 * time.Minute
	smokeTestTimeout    = 60 * time.Second
)

// Extraction guards. Defence-in-depth against a checksum-valid-but-malicious
// archive, mirroring core/toolmanager's posture but sized for this subsystem:
// the tool-manager's 512 MiB per-entry cap is below a single CUDA runtime
// library in these archives (ADR-066, "Not a tool-manager extension"), so the
// caps are per-entry 2 GiB and per-archive 8 GiB — far above the ~1.5 GiB a
// fully extracted runtime actually occupies.
const (
	maxExtractEntryBytes int64 = 2 << 30
	maxExtractTotalBytes int64 = 8 << 30
)

// Installer orchestrates Install and Remove over a Layout.
//
// The zero value is not usable; build one with NewInstaller. Every external
// effect is reachable through a field, so the whole flow is testable without
// a network, without multi-gigabyte artifacts and without macOS binaries:
// Downloader (HTTP client, free-space probe), Probe (hardware), RunCommand
// (xattr/codesign/smoke test), AllocatePort and Now.
type Installer struct {
	// Layout is the on-disk footprint. Required.
	Layout Layout
	// Sink persists install state into config.yaml. Required by Install and
	// Remove: an install that cannot be registered would leave bytes on disk
	// with no provider, and a removal that cannot clear the config would leave
	// a provider pointing at nothing.
	Sink ConfigSink
	// Logger receives the subsystem's diagnostics. nil → slog.Default().
	Logger *slog.Logger
	// Downloader fetches and verifies artifacts. nil → NewDownloader(nil, Logger).
	Downloader *Downloader
	// Probe reads the machine's RAM and accelerator. nil → ProbeHardware.
	Probe func(ctx context.Context, logger *slog.Logger) (Hardware, error)
	// ProbeDevices reads the accelerator INVENTORY of a provisioned runtime, so
	// an install can learn the GPU generation — which the packing rule and the
	// compatibility guards both need — instead of guessing it from the backend.
	// nil → ProbeDevices (the package function). Fail-soft like every other
	// probe here: an answer of false leaves the plan on its statically
	// decidable guards, it never fails an install.
	ProbeDevices func(ctx context.Context, binaryPath string, logger *slog.Logger) (MemoryTopology, bool)
	// RunCommand executes xattr/codesign/--version. nil → defaultCommandRunner.
	RunCommand CommandRunner
	// AllocatePort reserves a free loopback port. nil → ephemeralLoopbackPort.
	// The supervisor (server.go) owns port persistence and the pre-load
	// collision re-check; it may substitute a richer allocator.
	AllocatePort func(ctx context.Context) (int, error)
	// Stop terminates a running server before Remove deletes its files. nil is
	// legal and means "no supervisor is wired yet" — Remove then proceeds, and
	// the OS releases the files when the process exits.
	Stop func(ctx context.Context) error
	// Now stamps InstalledAt. nil → time.Now.
	Now func() time.Time
	// HostOS overrides the OS the macOS provisioning branch keys off. Empty →
	// runtime.GOOS. Tests set it to "darwin" to exercise quarantine-clear,
	// ad-hoc signing and the smoke test on any platform.
	HostOS string
}

// NewInstaller returns an Installer with production defaults for the given
// layout. Wire Sink before calling Install or Remove.
func NewInstaller(layout Layout, logger *slog.Logger) *Installer {
	return &Installer{Layout: layout, Logger: logger}
}

// InstallOptions parameterises one install run.
type InstallOptions struct {
	// Port is the loopback port to persist. Zero means "allocate a free one".
	Port int
	// Platform overrides the probed "<goos>-<goarch>" key. Empty → probed.
	Platform string
	// Backend overrides the probed accelerator. Empty → probed. Resolution
	// still degrades an override it cannot serve.
	Backend Backend
	// Progress receives one update per component and stage. May be nil.
	Progress InstallProgressFunc
}

// InstallReport is the outcome of a successful install.
type InstallReport struct {
	Manifest     Manifest
	Resolution   Resolution
	Hardware     Hardware
	RuntimeDir   string
	ServerBinary string
	ModelFile    string

	// Guards is the complete compatibility record of this install: every
	// decision the guard table made for this machine, whether c0wrk acted on it
	// (Applied=true, e.g. a CUDA 13.3 plan swapped to 12.8 before anything was
	// downloaded) or could only disclose it (Applied=false, e.g. a substitution
	// discovered after the runtime archive was already staged, or an advisory
	// whose workaround trades away something the user may want to keep).
	//
	// It is the same list the Manifest persists, lifted to the top level because
	// a degraded install is the first thing a caller should be able to see
	// without walking the report's nested records.
	Guards []GuardDecision

	// PackingReason is why the installed packing is what it is, lifted from the
	// resolution for the same reason.
	PackingReason PackingReason
}

// Install provisions the pinned runtime and weights for this machine.
//
// The order is fixed and each step is a gate:
//
//  1. hardware probe — RAM is a hard input (ErrRAMUnknown refuses);
//  2. Resolve — the combined memory gate is its FIRST check, so a machine
//     whose memory no modelled shape fits plans no assets, creates no
//     directory and downloads nothing (ErrInsufficientMemory, naming both
//     pools with both numbers);
//  3. a whole-set disk guard, before the first byte is fetched;
//  4. port allocation, persisted in both the manifest and the config;
//  5. the runtime archives (and the Windows CUDA companion): download with
//     resume and per-component progress → SHA256 verification gate → extract
//     into a staging tree;
//  6. macOS only: quarantine-clear, ad-hoc codesign and a --version smoke test
//     of the staged runtime — before the weights, so an unrunnable runtime
//     fails in seconds instead of after a multi-gigabyte download;
//  7. the staged runtime replaces the previous one — the old tree is retired
//     only after every new byte is secured and proven to run;
//  8. the weights (model, mmproj): download → SHA256 verification gate,
//     straight to their final paths;
//  9. manifest.json, written atomically and only now;
//
// 10. the config sink, which registers the provider entry.
//
// A failure at any step leaves no manifest and registers no provider: a
// half-installed model is never visible to the router. Bytes that DID verify
// are deliberately kept — they are cache hits for the next attempt, which is
// what makes a multi-gigabyte install resumable rather than restartable.
func (in *Installer) Install(ctx context.Context, opts InstallOptions) (*InstallReport, error) {
	if in == nil {
		return nil, errors.New("embeddedllm: nil Installer")
	}
	if in.Sink == nil {
		return nil, errors.New("embeddedllm: no ConfigSink wired — refusing to install " +
			"bytes that could not be registered as a provider")
	}
	if _, err := in.Layout.ManifestPath(); err != nil {
		return nil, err
	}

	hw, platform, res, topology, err := in.plan(ctx, opts)
	if err != nil {
		return nil, err
	}

	if err := in.Layout.EnsureRoots(); err != nil {
		return nil, err
	}
	if err := in.checkWholeSetDiskSpace(res.Assets); err != nil {
		return nil, err
	}

	port := opts.Port
	if port == 0 {
		port, err = in.allocatePort(ctx)
		if err != nil {
			return nil, fmt.Errorf("embeddedllm: allocating a loopback port: %w", err)
		}
	}

	// Phase order is deliberate: the runtime is downloaded, extracted, signed
	// and smoke-tested BEFORE the multi-gigabyte weights are fetched, so a
	// runtime that cannot execute (Gatekeeper, a missing GPU library) fails the
	// install in seconds instead of after an hour of downloading.
	runtimeAssets, weightAssets := splitRuntimeAssets(res.Assets)
	checksums := make(map[string]string, len(res.Assets))

	staging, err := in.Layout.RuntimeStagingDir(res.Backend)
	if err != nil {
		return nil, err
	}
	if err := in.prepareStaging(staging); err != nil {
		return nil, err
	}
	if err := in.fetchComponents(ctx, opts, runtimeAssets, staging, checksums); err != nil {
		return nil, err
	}

	serverBinary, err := in.provisionRuntime(ctx, opts, res, runtimeAssets)
	if err != nil {
		return nil, err
	}

	// The staged runtime can now answer the device probe, which is the last
	// moment a refinement is still cheap: the multi-gigabyte weights have not
	// been fetched yet. A first install learns its GPU generation here (a
	// repair already learned it in plan), so the generation-aware packing rule
	// and the device-dependent guards apply to the weights that are about to be
	// downloaded. A guard that wants a DIFFERENT RUNTIME is past its moment —
	// the archive is on disk and signed — so it is recorded as guidance rather
	// than silently dropped.
	res, weightAssets, refinedTopology := in.refineWithStagedDevices(ctx, platform, hw, res, serverBinary, weightAssets)
	if refinedTopology != nil {
		// The staged runtime answered, and the resolution that goes with it was
		// refined by that answer, so the pair the manifest records is the pair
		// the weights about to be downloaded were chosen for. A repair keeps the
		// topology plan() measured off the pre-existing tree; a first install
		// learns it here.
		topology = refinedTopology
	}

	if err := in.fetchComponents(ctx, opts, weightAssets, "", checksums); err != nil {
		return nil, err
	}

	modelFile, err := in.Layout.ModelFile(res.Packing)
	if err != nil {
		return nil, err
	}
	installedAt := in.now().UTC().Format(time.RFC3339)
	// The plan is recorded because a load must reproduce the shape this
	// machine was provisioned for, and it can only do that from a record: the
	// operator's `tuning` is a setting the sink carries verbatim, not install
	// state a manifest may copy, so re-deriving the plan at load would need a
	// second source of truth that goes stale on the first edit.
	plan := res.Memory
	manifest := Manifest{
		Packing:        res.Packing,
		Backend:        res.Backend,
		RuntimeVersion: RuntimeTag,
		Checksums:      checksums,
		Port:           port,
		ContextSize:    recordableContext(plan, res.ContextSize),
		ModelFile:      modelFile,
		InstalledAt:    installedAt,
		PackingReason:  res.PackingReason,
		GPUFamily:      res.GPU,
		Guards:         res.Guards,
		Topology:       topology,
		Plan:           &plan,
	}

	manifestPath, err := in.Layout.ManifestPath()
	if err != nil {
		return nil, err
	}
	if err := writeManifest(manifestPath, manifest); err != nil {
		return nil, err
	}
	in.logger().Info("embedded LLM installed",
		"backend", manifest.Backend, "packing", manifest.Packing,
		"packing_reason", manifest.PackingReason, "gpu_family", manifest.GPUFamily,
		"guards", guardIDsForLog(manifest.Guards),
		"port", manifest.Port, "context_size", manifest.ContextSize,
		"fit", plan.FitArg(), "kv_type", plan.KVType,
		"topology_probed", topology != nil,
		"model_file", manifest.ModelFile)

	state := InstallState{
		Packing:           manifest.Packing,
		Backend:           manifest.Backend,
		Port:              manifest.Port,
		ModelFile:         manifest.ModelFile,
		RuntimeVersion:    manifest.RuntimeVersion,
		InstalledAt:       manifest.InstalledAt,
		ContextSize:       manifest.ContextSize,
		AutoUnloadEnabled: DefaultAutoUnloadEnabled,
		AutoUnloadMinutes: DefaultAutoUnloadMinutes,
	}
	if err := in.Sink.ApplyInstalled(ctx, state); err != nil {
		// The bytes are fine and the manifest describes them accurately; only
		// the registration failed. Removing the manifest again would throw away
		// a correct record, so the error is returned as-is and a retry is
		// idempotent (the downloads are cache hits, the manifest is rewritten).
		return nil, fmt.Errorf("embeddedllm: registering the installed model in config: %w", err)
	}

	runtimeDir, err := in.Layout.RuntimeDir(res.Backend)
	if err != nil {
		return nil, err
	}
	return &InstallReport{
		Manifest:      manifest,
		Resolution:    res,
		Hardware:      hw,
		RuntimeDir:    runtimeDir,
		ServerBinary:  serverBinary,
		ModelFile:     modelFile,
		Guards:        manifest.Guards,
		PackingReason: manifest.PackingReason,
	}, nil
}

// plan runs the pure gates — the hardware probe, the memory gate and
// resolution — and returns both results together with the platform key the plan
// was made for. The memory refusal lives inside Resolve as its first check, so
// nothing is planned and nothing is downloaded on a machine that cannot hold
// the model.
//
// The DEVICE PROBE is an optional refinement, and plan tries to obtain it
// without spending anything: a repair or a reinstall still has the previously
// provisioned runtime on disk, which can answer `--list-devices` before a
// single byte is downloaded. When it answers, the plan is refined with BOTH
// halves of the answer — the classified GPU generation, so the
// backend-substituting compatibility guards (KNOWN_ISSUES #197, #223) and the
// generation-aware packing rule apply to the artifacts about to be fetched
// rather than to a record written after the fact, AND the measured memory
// budgets, so the gate and the launch shape are priced against the card that is
// actually there. A first install has no binary to ask and keeps
// GPUFamilyUnknown here; Install probes the staged runtime later and refines
// what is still refinable.
//
// Without a topology the gate does NOT fall back to a RAM floor. It derives the
// host budget from the RAM probe with the same reserve policy a topology
// carries, and classifies the accelerator axis statically: a backend whose
// memory is independent of host RAM (a discrete CUDA or ROCm card on amd64)
// leaves the device side UNREADABLE, which degrades the gate to the host pool
// and says so in `MemoryPlan.Notes` rather than refusing — that refusal is the
// bug this gate replaced. A unified or absent accelerator is priced from the RAM
// probe alone, which measures both. FitsPQ2_0 is still left Unknown, so no
// capacity downgrade is ever inferred from an unmeasured budget.
func (in *Installer) plan(ctx context.Context, opts InstallOptions) (Hardware, string, Resolution, *MemoryTopology, error) {
	hw, err := in.probe()(ctx, in.logger())
	if err != nil {
		return Hardware{}, "", Resolution{}, nil, fmt.Errorf("embeddedllm: hardware probe: %w", err)
	}
	platform := hw.Platform
	if opts.Platform != "" {
		platform = opts.Platform
	}
	backend := hw.Backend
	if opts.Backend != "" {
		backend = opts.Backend
	}

	profile := MachineProfile{Platform: platform, Backend: backend, RAMGiB: hw.RAMGiB}
	res, err := ResolveProfile(profile)
	if err != nil {
		return Hardware{}, "", Resolution{}, nil, fmt.Errorf("embeddedllm: %w", err)
	}

	// probed is returned only when it INFORMED the resolution that goes with
	// it. A topology the re-plan rejected is still a true measurement, but
	// recording it beside a plan that was not made from it would break the pair
	// the manifest promises — and a load's fail-soft path reads them as one
	// fact. Losing the measurement costs a support bundle one line; a
	// mismatched pair costs a launch shape nobody reasoned about.
	var probed *MemoryTopology
	if topology, ok := in.memoryTopologyFromBinary(ctx, in.existingServerBinary(res.Backend)); ok {
		gpu := ClassifyGPUs(topology.Devices)
		profile.GPU = gpu
		refined, refineErr := Resolve(ResolveInput{MachineProfile: profile, Topology: &topology})
		if refineErr != nil {
			// A MEMORY refusal is not "lost information" — it is the measured
			// budget saying this machine cannot hold the model, discovered
			// before a single byte was fetched. It must stop the install
			// rather than fall back to a plan that would OOM at load.
			if errors.Is(refineErr, ErrInsufficientMemory) {
				return Hardware{}, "", Resolution{}, nil, fmt.Errorf("embeddedllm: %w", refineErr)
			}
			// Anything else: the first plan is valid and the refinement only
			// adds information. Losing it degrades to the guards that are
			// statically decidable.
			in.logger().Debug("embedded LLM device-aware re-plan skipped",
				"gpu_family", gpu, "error", refineErr)
			res.GPU = gpu
			return hw, platform, res, nil, nil
		}
		if refined.Backend != res.Backend {
			in.logger().Info("embedded LLM plan changed after the device probe",
				"gpu_family", gpu, "probed_backend", backend,
				"from", res.Backend, "to", refined.Backend,
				"guards", guardIDsForLog(refined.Guards))
		}
		res = refined
		measured := topology
		probed = &measured
	}
	return hw, platform, res, probed, nil
}

// existingServerBinary returns the llama-server of an already-provisioned
// runtime for a backend, or "" when there is none. It exists so a repair or a
// reinstall can answer the device probe before downloading anything, and it
// never creates anything: a missing tree is the normal first-install case.
func (in *Installer) existingServerBinary(backend Backend) string {
	runtimeDir, err := in.Layout.RuntimeDir(backend)
	if err != nil {
		return ""
	}
	binary, err := ServerBinaryPath(runtimeDir, in.hostOS())
	if err != nil {
		return ""
	}
	if _, err := os.Stat(binary); err != nil {
		return ""
	}
	return binary
}

// memoryTopologyFromBinary asks a provisioned runtime what accelerator memory
// it can see. It is FAIL-SOFT in every direction, like the probe it wraps: no
// binary, a hung one or an unrecognized inventory all yield (zero, false), and
// the caller keeps the plan it already had. A memory measurement refines a
// plan; it is never a precondition of one.
//
// It returns the whole topology rather than only the GPU generation it used to,
// because the two consumers of the probe need different halves of it and the
// probe is expensive enough to run once: the compatibility guards and the
// packing rule need the FAMILY, and the memory gate needs the BUDGETS. A first
// install has no binary to ask, so its gate runs on the derived host budget and
// treats the device side as unreadable — see gateBudgetsFor.
func (in *Installer) memoryTopologyFromBinary(ctx context.Context, binaryPath string) (MemoryTopology, bool) {
	if binaryPath == "" {
		return MemoryTopology{}, false
	}
	topology, ok := in.probeDevices()(ctx, binaryPath, in.logger())
	if !ok {
		return MemoryTopology{}, false
	}
	if ClassifyGPUs(topology.Devices) == GPUFamilyUnknown {
		in.logger().Debug("embedded LLM device probe recognized no accelerator family",
			"devices", len(topology.Devices))
	}
	return topology, true
}

// guardIDsForLog renders the guards that a plan change acted on, for one log
// line. Only the applied ones are interesting there: the record itself carries
// every decision, applied or not.
func guardIDsForLog(decisions []GuardDecision) string {
	ids := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		if decision.Applied {
			ids = append(ids, string(decision.Guard))
		}
	}
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ",")
}

// refineWithStagedDevices is the second, device-aware pass over a plan: it
// probes the freshly staged runtime, classifies the accelerator, and folds what
// that reveals into the resolution BEFORE the weights are fetched.
//
// It never fails and never downloads. Three outcomes, in increasing order of
// what could still be changed:
//
//   - no answer (no probe, an unrecognized inventory, or a plan that already
//     knew its GPU family): the resolution is returned untouched;
//   - a guard or the packing rule wants something different and the BACKEND is
//     unchanged: the refined resolution and its weight assets are returned, so
//     the weights that are about to be downloaded are the right ones. This is
//     the case the refinement exists for — an Ada card that should get PTQ1_0,
//     or #223's garbled-output guard on a machine whose iGPU only a device
//     probe could name;
//   - a guard wants a DIFFERENT BACKEND: the runtime archive for the planned
//     backend is already staged, signed and smoke-tested, so the substitution
//     is recorded as guidance instead of applied. The install completes and the
//     record says why it may not work, which is the difference between a
//     documented upstream failure and a mystery.
//
// Guards are merged rather than replaced, so a decision that WAS applied in
// plan (a static one, like #222) keeps its Applied flag and its place in the
// record.
//
// The third result is the measured `MemoryTopology` — non-nil ONLY in the second
// and third outcomes' successful form, i.e. only when the returned resolution
// was actually refined by it, so the caller can persist a topology and a plan
// that describe one another. A branch that kept the resolution it came in with
// returns nil and the caller keeps whatever `plan` measured.
func (in *Installer) refineWithStagedDevices(ctx context.Context, platform string, hw Hardware,
	res Resolution, serverBinary string, weightAssets []Asset,
) (Resolution, []Asset, *MemoryTopology) {
	if res.GPU != GPUFamilyUnknown {
		return res, weightAssets, nil
	}
	topology, ok := in.memoryTopologyFromBinary(ctx, serverBinary)
	if !ok {
		return res, weightAssets, nil
	}
	gpu := ClassifyGPUs(topology.Devices)

	refined, err := Resolve(ResolveInput{MachineProfile: MachineProfile{
		Platform: platform,
		Backend:  res.Backend,
		RAMGiB:   hw.RAMGiB,
		GPU:      gpu,
	}, Topology: &topology})
	if err != nil {
		// The unrefined plan is valid; only the extra information is lost.
		in.logger().Debug("embedded LLM device-aware refinement skipped",
			"gpu_family", gpu, "error", err)
		res.GPU = gpu
		return res, weightAssets, nil
	}

	if refined.Backend != res.Backend {
		in.logger().Warn("a compatibility guard wants a different runtime than the one already staged",
			"gpu_family", gpu, "staged_backend", res.Backend, "preferred_backend", refined.Backend,
			"guards", guardIDsForLog(refined.Guards))
		res.GPU = gpu
		res.Guards = mergeGuardDecisions(res.Guards, markGuardsUnappliable(refined.Guards))
		return res, weightAssets, nil
	}

	res = refined
	_, weights := splitRuntimeAssets(refined.Assets)
	if refined.PackingReason != PackingReasonDefault {
		in.logger().Info("embedded LLM packing refined by the device probe",
			"gpu_family", gpu, "packing", refined.Packing, "reason", refined.PackingReason)
	}
	// The measurement is returned only here, where the resolution that goes
	// with it was actually refined by it. Every earlier branch keeps the plan
	// it came in with, so it keeps the topology (if any) that plan was made
	// from — see Installer.plan for why the pair must not be split.
	measured := topology
	return res, weights, &measured
}

// markGuardsUnappliable rewrites a set of decisions that were computed for a
// substitution c0wrk is no longer able to make: the runtime archive is already
// on disk. Every backend substitution loses its Applied flag and gains the
// reason it could not be applied, so the record explains itself instead of
// looking like an oversight.
func markGuardsUnappliable(decisions []GuardDecision) []GuardDecision {
	marked := make([]GuardDecision, 0, len(decisions))
	for _, decision := range decisions {
		if decision.Action == GuardActionPreferBackend {
			decision.Applied = false
			decision.Guidance += " (this was only discovered after a runtime had already been " +
				"staged, so the recommended " + string(decision.Backend) + " build was NOT provisioned: " +
				"reinstall to apply it)"
		}
		marked = append(marked, decision)
	}
	return marked
}

// splitRuntimeAssets separates the runtime archives (the server build plus the
// Windows CUDA companion) from the weights (model, mmproj). The two groups are
// fetched in different phases — see Install.
func splitRuntimeAssets(assets []Asset) (runtimeAssets, weightAssets []Asset) {
	runtimeAssets = make([]Asset, 0, 2)
	weightAssets = make([]Asset, 0, 2)
	for _, asset := range assets {
		if asset.Component == ComponentRuntime || asset.Component == ComponentCudart {
			runtimeAssets = append(runtimeAssets, asset)
			continue
		}
		weightAssets = append(weightAssets, asset)
	}
	return runtimeAssets, weightAssets
}

// prepareStaging clears and recreates the tree a runtime is extracted into. A
// staging tree left behind by a previous failed attempt would mix old and new
// bytes, so extraction always starts from an empty directory.
func (in *Installer) prepareStaging(staging string) error {
	if err := in.removeOwned(staging); err != nil {
		return err
	}
	return mkdirAll(staging)
}

// fetchComponents downloads and verifies each asset, recording its digest in
// checksums. When staging is non-empty the assets are runtime archives: each is
// extracted into the staging tree, and its StageDone is deferred to
// provisionRuntime so the component's stream ends when the runtime is actually
// usable rather than when its bytes landed.
func (in *Installer) fetchComponents(ctx context.Context, opts InstallOptions, assets []Asset,
	staging string, checksums map[string]string,
) error {
	deferDone := staging != ""
	for _, asset := range assets {
		digest, err := in.fetchOne(ctx, opts, asset)
		if err != nil {
			return err
		}
		checksums[string(asset.Component)] = digest

		if deferDone {
			in.emit(opts, Progress{Component: asset.Component, Stage: StageExtracting,
				BytesDone: asset.SizeBytes, BytesTotal: asset.SizeBytes})
			path, derr := in.Layout.Destination(asset)
			if derr != nil {
				return derr
			}
			if xerr := extractArchive(path, staging); xerr != nil {
				return fmt.Errorf("embeddedllm: extracting %s archive: %w", asset.Component, xerr)
			}
			continue
		}
		in.emit(opts, Progress{Component: asset.Component, Stage: StageDone,
			BytesDone: asset.SizeBytes, BytesTotal: asset.SizeBytes})
	}
	return nil
}

// fetchOne downloads one artifact and enforces the verification gate.
//
// Downloader.Download already refuses to promote unverified bytes, so this is
// a second, independent gate rather than a re-hash of a multi-gigabyte file:
// a result that is not marked verified, or whose digest differs from the pin,
// aborts the install. Fail-closed is the only acceptable behaviour here
// (ASI04) — "probably fine" is how an unverified binary ends up executed.
func (in *Installer) fetchOne(ctx context.Context, opts InstallOptions, asset Asset) (string, error) {
	in.emit(opts, Progress{Component: asset.Component, Stage: StageDownloading,
		BytesDone: 0, BytesTotal: asset.SizeBytes})

	dst, err := in.Layout.Destination(asset)
	if err != nil {
		return "", err
	}
	result, err := in.downloader().Download(ctx, asset, dst, func(done, total int64) {
		in.emit(opts, Progress{Component: asset.Component, Stage: StageDownloading,
			BytesDone: done, BytesTotal: total})
	})
	if err != nil {
		return "", fmt.Errorf("embeddedllm: downloading %s: %w", asset.Component, err)
	}

	in.emit(opts, Progress{Component: asset.Component, Stage: StageVerifying,
		BytesDone: asset.SizeBytes, BytesTotal: asset.SizeBytes})

	pinned := strings.ToLower(asset.SHA256)
	if !result.Verified || result.SHA256 != pinned {
		return "", fmt.Errorf("embeddedllm: %s failed the verification gate (verified=%t, "+
			"digest=%q, pinned=%q): %w", asset.Component, result.Verified, result.SHA256, pinned,
			ErrChecksumMismatch)
	}
	in.logger().Info("embedded LLM component verified",
		"component", asset.Component, "sha256", result.SHA256,
		"bytes", result.SizeBytes, "cached", result.Cached, "resumed", result.Resumed)
	return result.SHA256, nil
}

// provisionRuntime signs and smoke-tests the staged runtime on macOS, swaps it
// into place, locates the server binary and closes out the runtime components'
// progress streams.
func (in *Installer) provisionRuntime(ctx context.Context, opts InstallOptions, res Resolution,
	runtimeAssets []Asset,
) (string, error) {
	staging, err := in.Layout.RuntimeStagingDir(res.Backend)
	if err != nil {
		return "", err
	}
	if in.hostOS() == "darwin" {
		total := TotalBytes(runtimeAssets)
		in.emit(opts, Progress{Component: ComponentRuntime, Stage: StageSigning,
			BytesDone: total, BytesTotal: total})
		if err := in.provisionDarwin(ctx, staging); err != nil {
			return "", err
		}
	}

	runtimeDir, err := in.promoteRuntime(res.Backend)
	if err != nil {
		return "", err
	}
	serverBinary, err := ServerBinaryPath(runtimeDir, in.hostOS())
	if err != nil {
		return "", err
	}
	for _, asset := range runtimeAssets {
		in.emit(opts, Progress{Component: asset.Component, Stage: StageDone,
			BytesDone: asset.SizeBytes, BytesTotal: asset.SizeBytes})
	}
	return serverBinary, nil
}

// provisionDarwin clears the download quarantine from the whole runtime tree,
// applies an ad-hoc signature to every Mach-O image in it, and proves the
// result actually executes.
//
// Why: artifacts fetched outside a browser still carry
// com.apple.quarantine, and arm64 macOS refuses to execute an image whose
// signature is missing or invalid. The pinned fork archives are unsigned CI
// builds, so both steps are required before llama-server will run at all.
//
// A missing xattr/codesign helper is a warning, not a failure: the smoke test
// is the authority on whether the runtime runs, and refusing to install on a
// machine without /usr/bin/codesign would be a worse outcome than trying.
func (in *Installer) provisionDarwin(ctx context.Context, runtimeDir string) error {
	if err := in.runBounded(ctx, macOSCommandTimeout, "xattr", "-cr", runtimeDir); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			in.logger().Warn("xattr is unavailable; quarantine attributes were not cleared",
				"path", runtimeDir, "error", err)
		} else {
			return fmt.Errorf("embeddedllm: clearing the download quarantine on %q: %w", runtimeDir, err)
		}
	}

	images, err := machOImages(runtimeDir)
	if err != nil {
		return err
	}
	for _, image := range images {
		err := in.runBounded(ctx, macOSCommandTimeout, "codesign", "--force", "--sign", "-", image)
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				in.logger().Warn("codesign is unavailable; the runtime keeps its upstream signatures",
					"path", runtimeDir)
				break
			}
			return fmt.Errorf("embeddedllm: ad-hoc signing %q: %w", image, err)
		}
	}
	in.logger().Info("embedded LLM runtime provisioned for macOS",
		"path", runtimeDir, "signed_images", len(images))

	return in.smokeTest(ctx, runtimeDir)
}

// smokeTest runs "llama-server --version" against the staged tree and turns a
// failure into an actionable message: on macOS the overwhelmingly likely cause
// is Gatekeeper, and "the install failed" with no next step is not an
// acceptable outcome for a user who just downloaded 7 GiB.
func (in *Installer) smokeTest(ctx context.Context, runtimeDir string) error {
	binary, err := ServerBinaryPath(runtimeDir, in.hostOS())
	if err != nil {
		return err
	}
	smokeCtx, cancel := context.WithTimeout(ctx, smokeTestTimeout)
	defer cancel()

	out, err := in.runner()(smokeCtx, binary, "--version")
	if err != nil {
		return fmt.Errorf("%w: %s --version failed: %v\n"+
			"macOS Gatekeeper is most likely still blocking the unsigned runtime. To fix it, run:\n"+
			"  xattr -dr com.apple.quarantine %s\n"+
			"or open System Settings → Privacy & Security, find the blocked llama-server "+
			"entry and click \"Allow Anyway\", then install again.\n"+
			"Command output: %s",
			ErrSmokeTestFailed, binary, err, runtimeDir, strings.TrimSpace(out))
	}
	in.logger().Debug("embedded LLM runtime smoke test passed", "binary", binary,
		"output", strings.TrimSpace(out))
	return nil
}

// promoteRuntime swaps the staged tree into place. The previous tree is
// retired rather than deleted first, so a failed rename rolls back instead of
// leaving the install with no runtime at all — the subsystem's version of
// "secure the new bytes before destroying the old".
func (in *Installer) promoteRuntime(backend Backend) (string, error) {
	final, err := in.Layout.RuntimeDir(backend)
	if err != nil {
		return "", err
	}
	staging, err := in.Layout.RuntimeStagingDir(backend)
	if err != nil {
		return "", err
	}
	retired, err := in.Layout.RuntimeRetiredDir(backend)
	if err != nil {
		return "", err
	}

	// Clear a retire dir left behind by a crashed previous run.
	if err := in.removeOwned(retired); err != nil {
		return "", err
	}
	hadPrevious := pathExists(final)
	if hadPrevious {
		if err := os.Rename(final, retired); err != nil {
			return "", fmt.Errorf("embeddedllm: retiring the previous runtime %q: %w", final, err)
		}
	}
	if err := os.Rename(staging, final); err != nil {
		if hadPrevious {
			if rbErr := os.Rename(retired, final); rbErr != nil {
				in.logger().Error("failed to roll back to the previous runtime",
					"path", final, "error", rbErr, "original", err)
			}
		}
		return "", fmt.Errorf("embeddedllm: moving the provisioned runtime into place: %w", err)
	}
	if hadPrevious {
		if err := in.removeOwned(retired); err != nil {
			in.logger().Warn("the previous runtime could not be deleted",
				"path", retired, "error", err)
		}
	}
	return final, nil
}

// Remove deletes an installation: it stops a running server, removes the model
// tree (manifest and weights), removes every runtime tree and the archive
// staging area, and finally clears the config.
//
// The config is cleared even when a deletion failed, and the deletion error is
// returned afterwards: a leftover directory wastes disk and a retry reclaims
// it, while a config that still claims an install whose files are gone points
// the router at a dead provider. Deletions are gated on Layout.Owns, so no
// path outside the two embedded-LLM trees can be removed — in particular never
// the flat embedding-model files that share <agentDir>/models and never
// anything under <toolsDir>/bin.
func (in *Installer) Remove(ctx context.Context) error {
	if in == nil {
		return errors.New("embeddedllm: nil Installer")
	}
	if in.Sink == nil {
		return errors.New("embeddedllm: no ConfigSink wired — refusing to delete an install " +
			"whose provider entry could not be cleared")
	}
	if in.Stop != nil {
		if err := in.Stop(ctx); err != nil {
			return fmt.Errorf("embeddedllm: stopping the inference server before removal: %w", err)
		}
	}

	var errs []error
	if err := in.removeOwned(in.Layout.ModelRoot); err != nil {
		errs = append(errs, err)
	}
	removed, err := in.removeRuntimeTrees()
	if err != nil {
		errs = append(errs, err)
	}
	if downloads, derr := in.Layout.DownloadsDir(); derr != nil {
		errs = append(errs, derr)
	} else if err := in.removeOwned(downloads); err != nil {
		errs = append(errs, err)
	}

	if serr := in.Sink.ApplyRemoved(ctx); serr != nil {
		errs = append(errs, fmt.Errorf("clearing the config: %w", serr))
	} else {
		in.logger().Info("embedded LLM removed", "runtime_trees_deleted", removed)
	}
	return errors.Join(errs...)
}

// removeRuntimeTrees deletes every "llama-*" tree under the runtime root —
// the installed one, plus any .staging or .old leftover from an interrupted
// install — and reports how many were removed.
func (in *Installer) removeRuntimeTrees() (int, error) {
	entries, err := os.ReadDir(in.Layout.RuntimesRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("embeddedllm: listing %q: %w", in.Layout.RuntimesRoot, err)
	}
	removed := 0
	var errs []error
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), runtimeDirPrefix) {
			continue
		}
		// Built through the layout's containment-checked join, then deleted
		// through the single gated primitive: neither step can reach outside
		// the runtime root.
		target, jerr := in.Layout.safeJoin(in.Layout.RuntimesRoot, entry.Name())
		if jerr != nil {
			errs = append(errs, jerr)
			continue
		}
		if err := in.removeOwned(target); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// removeOwned deletes path only when the layout owns it. It is the single
// destructive primitive in this subsystem, so the agent-isolation invariant
// (nothing under <toolsDir>/bin, nothing outside the two trees) is enforced in
// exactly one place.
func (in *Installer) removeOwned(path string) error {
	if path == "" {
		return errors.New("embeddedllm: refusing to delete an empty path")
	}
	if !pathExists(path) {
		return nil
	}
	if !in.Layout.Owns(path) {
		return fmt.Errorf("embeddedllm: refusing to delete %q — it is outside the "+
			"embedded-LLM storage layout", path)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("embeddedllm: deleting %q: %w", path, err)
	}
	return nil
}

// checkWholeSetDiskSpace refuses up front when the volume cannot hold the
// entire resolved set plus headroom, so a doomed 7 GiB download fails in
// milliseconds instead of after an hour. Measurement failure is best-effort
// and non-fatal, matching the Downloader's per-artifact guard: a genuinely
// full disk still fails safely on ENOSPC, which leaves a resumable partial.
func (in *Installer) checkWholeSetDiskSpace(assets []Asset) error {
	required := RequiredFreeBytes(TotalBytes(assets))
	free, err := in.freeSpace()(in.Layout.ModelRoot)
	if err != nil {
		in.logger().Warn("disk space check unavailable, proceeding without the whole-set guard",
			"path", in.Layout.ModelRoot, "required", required, "error", err)
		return nil //nolint:nilerr // best-effort guard; the write fails safely on ENOSPC
	}
	if free < required {
		return fmt.Errorf("%w for the full embedded-LLM set: %s available at %s, %s required "+
			"(artifacts %s + %s headroom)",
			ErrInsufficientDisk, formatBytes(free), in.Layout.ModelRoot,
			formatBytes(required), formatBytes(required-DefaultDiskHeadroom),
			formatBytes(DefaultDiskHeadroom))
	}
	return nil
}

// ── extraction ──

// extractArchive unpacks a .tar.gz/.tgz or .zip archive into destDir. Entries
// are containment-checked through pathutil, so a traversal entry is skipped
// rather than written outside the tree, and both a per-entry and a total
// decompressed-size cap bound a checksum-valid-but-malicious archive.
func extractArchive(archivePath, destDir string) error {
	switch {
	case strings.HasSuffix(archivePath, ".tar.gz"), strings.HasSuffix(archivePath, ".tgz"):
		return extractTarGz(archivePath, destDir)
	case strings.HasSuffix(archivePath, ".zip"):
		return extractZip(archivePath, destDir)
	default:
		return fmt.Errorf("unsupported archive format %q", filepath.Ext(archivePath))
	}
}

// extractQuota tracks the total decompressed bytes of one archive.
type extractQuota struct {
	written int64
}

func (q *extractQuota) add(n int64) error {
	q.written += n
	if q.written > maxExtractTotalBytes {
		return fmt.Errorf("archive extracts to more than %d bytes (possible archive bomb)",
			maxExtractTotalBytes)
	}
	return nil
}

func extractTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("opening archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("creating gzip reader: %w", err)
	}
	defer func() { _ = gzr.Close() }()

	var quota extractQuota
	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading tar entry: %w", err)
		}
		target, ok, err := extractTarget(destDir, hdr.Name)
		if err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := mkdirAll(target); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeExtractEntry(target, io.LimitReader(tr, maxExtractEntryBytes+1),
				os.FileMode(hdr.Mode), &quota); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := createExtractSymlink(destDir, target, hdr.Linkname); err != nil {
				return err
			}
		}
	}
}

func extractZip(archivePath, destDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("opening zip: %w", err)
	}
	defer func() { _ = r.Close() }()

	var quota extractQuota
	for _, f := range r.File {
		target, ok, err := extractTarget(destDir, f.Name)
		if err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		if f.FileInfo().IsDir() {
			if err := mkdirAll(target); err != nil {
				return err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("opening zip entry %q: %w", f.Name, err)
		}
		writeErr := writeExtractEntry(target, io.LimitReader(rc, maxExtractEntryBytes+1),
			f.Mode(), &quota)
		_ = rc.Close()
		if writeErr != nil {
			return writeErr
		}
	}
	return nil
}

// extractTarget resolves one archive entry name against destDir and reports
// whether it may be written. Containment is decided by pathutil, never by an
// inline prefix comparison; an escaping entry is skipped, matching the
// tool-manager's behaviour.
func extractTarget(destDir, name string) (target string, writable bool, err error) {
	if name == "" {
		return "", false, nil
	}
	target = filepath.Join(destDir, name)
	within, werr := pathutil.IsWithinPath(destDir, target)
	if werr != nil {
		return "", false, fmt.Errorf("checking containment of archive entry %q: %w", name, werr)
	}
	if !within {
		return "", false, nil
	}
	return target, true, nil
}

func writeExtractEntry(target string, src io.Reader, mode fs.FileMode, quota *extractQuota) error {
	if err := mkdirAll(filepath.Dir(target)); err != nil {
		return err
	}
	out, err := os.Create(target)
	if err != nil {
		return fmt.Errorf("creating %q: %w", target, err)
	}
	n, copyErr := io.Copy(out, src)
	if cerr := out.Close(); cerr != nil && copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		_ = os.Remove(target)
		return fmt.Errorf("writing %q: %w", target, copyErr)
	}
	if n > maxExtractEntryBytes {
		_ = os.Remove(target)
		return fmt.Errorf("archive entry %q exceeds %d bytes (possible archive bomb)",
			filepath.Base(target), maxExtractEntryBytes)
	}
	if err := quota.add(n); err != nil {
		_ = os.Remove(target)
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	if err := os.Chmod(target, mode.Perm()); err != nil {
		return fmt.Errorf("setting permissions on %q: %w", target, err)
	}
	return nil
}

// createExtractSymlink reproduces an in-archive symlink, refusing a target that
// would escape destDir: a symlink pointing outside the tree would let the
// runtime load (or let a later deletion follow) an arbitrary path.
func createExtractSymlink(destDir, target, linkname string) error {
	if linkname == "" {
		return nil
	}
	resolved := linkname
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(target), linkname)
	}
	within, err := pathutil.IsWithinPath(destDir, resolved)
	if err != nil {
		return fmt.Errorf("checking symlink target %q: %w", linkname, err)
	}
	if !within {
		return fmt.Errorf("archive symlink %q points outside the runtime tree", linkname)
	}
	_ = os.Remove(target)
	if err := os.Symlink(linkname, target); err != nil {
		return fmt.Errorf("creating symlink %q: %w", target, err)
	}
	return nil
}

// machOImages lists the files in a macOS runtime tree that need a signature:
// the llama-* executables and the dylibs they load. arm64 macOS refuses to
// execute an image without a valid signature, so signing only the server would
// leave it unable to load its own libraries.
func machOImages(runtimeDir string) ([]string, error) {
	var images []string
	walkErr := filepath.WalkDir(runtimeDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !needsAdHocSignature(d.Name()) {
			return nil
		}
		// An entry whose containment cannot be resolved is skipped instead of
		// signed: signing is a mutation, and fail-closed is the only acceptable
		// default for a path this subsystem did not itself write.
		within, werr := pathutil.IsWithinPath(runtimeDir, path)
		if werr != nil || !within {
			return nil //nolint:nilerr // skipping an unverifiable entry is the safe outcome
		}
		images = append(images, path)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("embeddedllm: scanning %q for Mach-O images: %w", runtimeDir, walkErr)
	}
	return images, nil
}

// needsAdHocSignature reports whether a file name is one of the runtime's
// Mach-O images: the llama-* executables or a .dylib dependency.
func needsAdHocSignature(name string) bool {
	return strings.HasPrefix(name, "llama") || strings.HasSuffix(name, ".dylib")
}

// ── seams ──

// emit reports one progress update, tolerating a nil callback.
func (in *Installer) emit(opts InstallOptions, p Progress) {
	if opts.Progress != nil {
		opts.Progress(p)
	}
}

func (in *Installer) logger() *slog.Logger {
	if in == nil || in.Logger == nil {
		return slog.Default()
	}
	return in.Logger
}

func (in *Installer) downloader() *Downloader {
	if in.Downloader != nil {
		return in.Downloader
	}
	return NewDownloader(nil, in.logger())
}

func (in *Installer) probe() func(context.Context, *slog.Logger) (Hardware, error) {
	if in.Probe != nil {
		return in.Probe
	}
	return ProbeHardware
}

func (in *Installer) probeDevices() func(context.Context, string, *slog.Logger) (MemoryTopology, bool) {
	if in.ProbeDevices != nil {
		return in.ProbeDevices
	}
	return ProbeDevices
}

func (in *Installer) runner() CommandRunner {
	if in.RunCommand != nil {
		return in.RunCommand
	}
	return defaultCommandRunner
}

func (in *Installer) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

func (in *Installer) hostOS() string {
	if in.HostOS != "" {
		return in.HostOS
	}
	return runtime.GOOS
}

func (in *Installer) freeSpace() func(string) (int64, error) {
	if in.Downloader != nil && in.Downloader.FreeSpace != nil {
		return in.Downloader.FreeSpace
	}
	return platformFreeSpace
}

// allocatePort reserves a loopback port for the server.
func (in *Installer) allocatePort(ctx context.Context) (int, error) {
	if in.AllocatePort != nil {
		return in.AllocatePort(ctx)
	}
	return ephemeralLoopbackPort(ctx)
}

// ephemeralLoopbackPort asks the OS for a free loopback port by binding :0 and
// reading the assignment back. It is the production allocator, used whenever
// Installer.AllocatePort is nil; the install flow persists the port it returns,
// and server.go's EnsurePort seam may substitute a different one before a spawn.
func ephemeralLoopbackPort(ctx context.Context) (int, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = listener.Close() }()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || addr.Port <= 0 {
		return 0, errors.New("the OS did not report a usable loopback port")
	}
	return addr.Port, nil
}

// runBounded runs one external command under its own timeout so a wedged
// helper cannot stall the install indefinitely.
func (in *Installer) runBounded(ctx context.Context, timeout time.Duration,
	name string, args ...string,
) error {
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := in.runner()(bounded, name, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %w (output: %s)", name, strings.Join(args, " "),
			err, strings.TrimSpace(out))
	}
	return nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
