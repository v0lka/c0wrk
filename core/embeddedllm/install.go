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
	ContextSize    int               `json:"context_size"`
	ModelFile      string            `json:"model_file"`
	InstalledAt    string            `json:"installed_at"` // RFC 3339
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
}

// Install provisions the pinned runtime and weights for this machine.
//
// The order is fixed and each step is a gate:
//
//  1. hardware probe — RAM is a hard input (ErrRAMUnknown refuses);
//  2. Resolve — the 16 GiB gate is its FIRST check, so an undersized machine
//     plans no assets and downloads nothing;
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

	hw, res, err := in.plan(ctx, opts)
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

	if err := in.fetchComponents(ctx, opts, weightAssets, "", checksums); err != nil {
		return nil, err
	}

	modelFile, err := in.Layout.ModelFile(res.Packing)
	if err != nil {
		return nil, err
	}
	installedAt := in.now().UTC().Format(time.RFC3339)
	manifest := Manifest{
		Packing:        res.Packing,
		Backend:        res.Backend,
		RuntimeVersion: RuntimeTag,
		Checksums:      checksums,
		Port:           port,
		ContextSize:    res.ContextSize,
		ModelFile:      modelFile,
		InstalledAt:    installedAt,
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
		"port", manifest.Port, "context_size", manifest.ContextSize,
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
		Manifest:     manifest,
		Resolution:   res,
		Hardware:     hw,
		RuntimeDir:   runtimeDir,
		ServerBinary: serverBinary,
		ModelFile:    modelFile,
	}, nil
}

// plan runs the two pure gates — the hardware probe and resolution — and
// returns both results. The RAM refusal lives inside Resolve as its first
// check, so nothing is planned and nothing is downloaded below 16 GiB.
func (in *Installer) plan(ctx context.Context, opts InstallOptions) (Hardware, Resolution, error) {
	hw, err := in.probe()(ctx, in.logger())
	if err != nil {
		return Hardware{}, Resolution{}, fmt.Errorf("embeddedllm: hardware probe: %w", err)
	}
	platform := hw.Platform
	if opts.Platform != "" {
		platform = opts.Platform
	}
	backend := hw.Backend
	if opts.Backend != "" {
		backend = opts.Backend
	}
	res, err := Resolve(platform, backend, hw.RAMGiB)
	if err != nil {
		return Hardware{}, Resolution{}, fmt.Errorf("embeddedllm: %w", err)
	}
	return hw, res, nil
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
