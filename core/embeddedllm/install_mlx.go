package embeddedllm

// The mtplx engine's provisioning flow (ADR-092): the llama install's gate
// order with the engine's three deviations — relocatable plain-file tree,
// import smoke test, atomically swapped pack. Nothing runs `mtplx pull`:
// every byte is Downloader-verified against mlxpins.go.
// See specs/decisions/092-embedded-llm-mlx-mtplx.md and
// specs/domains/embedded-llm.md (Install).

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
)

const (
	// mlxPipInstallTimeout bounds the offline wheel install: local files and a
	// closed dependency set, so it bounds only disk work.
	mlxPipInstallTimeout = 10 * time.Minute
	// mlxSmokeTimeout bounds the import smoke test.
	mlxSmokeTimeout = 2 * time.Minute
	// mlxProvisionHeadroomBytes is the disk beyond the archives: the extracted
	// CPython plus the wheels unpacked into site-packages, rounded to 2 GiB.
	mlxProvisionHeadroomBytes = int64(2) << 30
	// mlxRequirementsName is the pin-generated requirements file, written into
	// the staging tree and removed before promotion.
	mlxRequirementsName = "mlx-requirements.txt"
)

// mlxProvisionAssets is the EngineArtifactSet seam; a test swaps in a tiny
// fake set (no network, no pins), like the llama path's assetTable seam.
var mlxProvisionAssets = EngineArtifactSet

// splitMLXAssets orders the engine's artifact set into the engine stack (the
// managed CPython archive, then the closed wheel set) and the weight pack.
func splitMLXAssets(assets []Asset) (engineAssets, packAssets []Asset) {
	engineAssets = make([]Asset, 0, 1+len(mtplxWheelPins))
	packAssets = make([]Asset, 0, len(mlxPackPins))
	for _, asset := range assets {
		switch asset.Component {
		case ComponentPython, ComponentEngineWheels:
			engineAssets = append(engineAssets, asset)
		case ComponentMLXPack:
			packAssets = append(packAssets, asset)
		}
	}
	return engineAssets, packAssets
}

// installMLX provisions the pinned MTPLX engine stack and the pinned MLX
// weight pack. Dispatched from Install after the step-0 stop.
func (in *Installer) installMLX(ctx context.Context, opts InstallOptions) (*InstallReport, error) {
	// 1. Hardware probe: RAM is this engine's only machine input.
	hw, err := in.probe()(ctx, in.logger())
	if err != nil {
		return nil, fmt.Errorf("embeddedllm: hardware probe: %w", err)
	}
	platform := hw.Platform
	if opts.Platform != "" {
		platform = opts.Platform
	}
	if platform != PlatformDarwinARM64 {
		return nil, fmt.Errorf("%w: engine %q needs %q, got %q (use engine %q)",
			ErrEngineUnsupportedPlatform, EngineMTPLX, PlatformDarwinARM64, platform, EngineLlama)
	}

	// 2. Resolve: the gate is the FIRST check — no fitting window, no assets,
	// no downloads. The operator's tuning (context.mode exact) is the plan.
	terms, err := PinnedMLXMemoryTerms()
	if err != nil {
		return nil, err
	}
	plan, err := PlanMLX(hw.RAMGiB, opts.Tuning, terms)
	if err != nil {
		return nil, fmt.Errorf("embeddedllm: %w", err)
	}
	assets, err := mlxProvisionAssets(platform)
	if err != nil {
		return nil, err
	}

	if err := in.Layout.EnsureRoots(); err != nil {
		return nil, err
	}
	if err := in.checkWholeSetDiskSpaceMLX(assets); err != nil {
		return nil, err
	}

	port := opts.Port
	if port == 0 {
		port, err = in.allocatePort(ctx)
		if err != nil {
			return nil, fmt.Errorf("embeddedllm: allocating a loopback port: %w", err)
		}
	}

	// 3. The engine stack. Nothing is resident after the step-0 stop: staging
	// rebuilds from scratch (a partial rebuild is a verified cache); the old
	// tree is untouched until promotion (layout.go's staging invariant).
	runtimeDir, err := in.Layout.MLXRuntimeDir()
	if err != nil {
		return nil, err
	}
	workDir, err := in.Layout.MLXRuntimeWorkDir()
	if err != nil {
		return nil, err
	}
	if err := in.prepareStaging(workDir); err != nil {
		return nil, err
	}
	// Sweep the downloads cache of non-current-pin files first, so the pip
	// --find-links source carries only what this build's pins own.
	if err := in.mlxSweepDownloads(); err != nil {
		return nil, err
	}

	engineAssets, packAssets := splitMLXAssets(assets)
	checksums := make(map[string]string, len(assets))
	// The Python archive is staged-extracted (the tree the wheels go into);
	// fetchComponents emits the extracting stage.
	if err := in.fetchComponents(ctx, opts, engineAssets[:1], workDir, checksums); err != nil {
		return nil, fmt.Errorf("embeddedllm: provisioning the managed Python: %w", err)
	}
	if err := in.fetchComponents(ctx, opts, engineAssets[1:], "", checksums); err != nil {
		return nil, fmt.Errorf("embeddedllm: fetching the engine wheel set: %w", err)
	}

	stagedPython, err := MLXServerPythonPath(workDir)
	if err != nil {
		return nil, err
	}
	if !pathExists(stagedPython) {
		return nil, fmt.Errorf("embeddedllm: the managed Python archive did not extract the expected tree (missing %q)", stagedPython)
	}

	// 4. The wheels, offline — mlxPipInstall. The engine tree and its cache
	// are already verified, so pip has nothing to fetch and nowhere to phone
	// home.
	if err := in.mlxPipInstall(ctx, stagedPython, workDir); err != nil {
		return nil, err
	}

	// 5. Smoke test: the pinned engine must import in THIS interpreter before
	// the multi-gigabyte pack is fetched.
	if err := in.mlxSmoke(ctx, stagedPython, workDir); err != nil {
		return nil, err
	}

	// 6. Promote the finished tree — the FIRST touch of the previous tree, so
	// every failure above leaves the machine with a working engine.
	if err := in.removeOwned(runtimeDir); err != nil {
		return nil, err
	}
	if err := os.Rename(workDir, runtimeDir); err != nil {
		return nil, fmt.Errorf("embeddedllm: promoting the provisioned engine tree: %w", err)
	}
	finalPython, err := MLXServerPythonPath(runtimeDir)
	if err != nil {
		return nil, err
	}
	for _, asset := range engineAssets {
		in.emit(opts, Progress{Component: asset.Component, Stage: StageDone,
			BytesDone: asset.SizeBytes, BytesTotal: asset.SizeBytes})
	}

	// 7. The weight pack, staged and swapped WHOLE: its 16 files only make
	// sense together (one revision's weights, MTP head, tokenizer stack and
	// serving contract), so every file is fetched and hash-verified into a
	// staging sibling before the two-rename swap below.
	packDir, err := in.Layout.MLXPackDir()
	if err != nil {
		return nil, err
	}
	packStaging, err := in.Layout.MLXPackStagingDir()
	if err != nil {
		return nil, err
	}
	if err := in.prepareStaging(packStaging); err != nil {
		return nil, err
	}
	for _, asset := range packAssets {
		dst, derr := in.Layout.safeJoin(packStaging, asset.ArchiveName)
		if derr != nil {
			return nil, derr
		}
		digest, ferr := in.fetchOneTo(ctx, opts, asset, dst)
		if ferr != nil {
			return nil, fmt.Errorf("embeddedllm: fetching the MLX weight pack: %w", ferr)
		}
		checksums[checksumKey(asset)] = digest
		in.emit(opts, Progress{Component: asset.Component, Stage: StageDone,
			BytesDone: asset.SizeBytes, BytesTotal: asset.SizeBytes})
	}
	// A failure before the swap leaves the previous pack and manifest fully
	// consistent; a crash BETWEEN the swap and the manifest write leaves the
	// new (complete) revision under the OLD manifest — stale, never a mixture.
	packOld, err := in.Layout.MLXPackRetiredDir()
	if err != nil {
		return nil, err
	}
	if pathExists(packDir) {
		if err := in.removeOwned(packOld); err != nil {
			return nil, err
		}
		if err := os.Rename(packDir, packOld); err != nil {
			return nil, fmt.Errorf("embeddedllm: retiring the previous MLX weight pack: %w", err)
		}
	}
	if err := os.Rename(packStaging, packDir); err != nil {
		return nil, fmt.Errorf("embeddedllm: promoting the staged MLX weight pack: %w", err)
	}
	if err := in.removeOwned(packOld); err != nil {
		return nil, err
	}

	installedAt := in.now().UTC().Format(time.RFC3339)
	manifest := Manifest{
		Packing:        "", // the pack ships one quantization; the reason says so
		Backend:        BackendMetal,
		RuntimeVersion: MTPLXVersion,
		Engine:         EngineMTPLX,
		PythonVersion:  pythonInstallVersion,
		PackRevision:   MLXPackRevision,
		Checksums:      checksums,
		Port:           port,
		ContextSize:    recordableContext(plan, plan.ContextSize),
		ModelFile:      packDir,
		InstalledAt:    installedAt,
		PackingReason:  PackingReasonMLXPack,
		GPUFamily:      GPUFamilyAppleSilicon,
		Plan:           &plan,
		// The machine shape the plan was priced from — this engine's only
		// evidence (no device inventory, one unified pool). The launch's live
		// re-plan takes the RAM from here.
		Topology: &MemoryTopology{
			HostRAMGiB:        hw.RAMGiB,
			Unified:           true,
			DeviceBudgetBytes: mlxEngineBudgetBytes(hw.RAMGiB),
			HostBudgetBytes:   mlxEngineBudgetBytes(hw.RAMGiB),
			ProbedAt:          installedAt,
		},
	}
	manifestPath, err := in.Layout.ManifestPath()
	if err != nil {
		return nil, err
	}
	if err := writeManifest(manifestPath, manifest, in.logger()); err != nil {
		return nil, err
	}
	in.logger().Info("embedded LLM installed",
		"engine", string(EngineMTPLX), "runtime_version", manifest.RuntimeVersion,
		"python", manifest.PythonVersion, "pack_revision", manifest.PackRevision,
		"backend", manifest.Backend, "packing_reason", manifest.PackingReason,
		"port", manifest.Port, "context_size", manifest.ContextSize,
		"model_dir", manifest.ModelFile)

	state := InstallState{
		Engine:            EngineMTPLX,
		Packing:           manifest.Packing,
		Backend:           manifest.Backend,
		Port:              manifest.Port,
		ModelFile:         manifest.ModelFile,
		RuntimeVersion:    manifest.RuntimeVersion,
		InstalledAt:       manifest.InstalledAt,
		ContextSize:       manifest.ContextSize,
		PythonVersion:     manifest.PythonVersion,
		PackRevision:      manifest.PackRevision,
		AutoUnloadEnabled: DefaultAutoUnloadEnabled,
		AutoUnloadMinutes: DefaultAutoUnloadMinutes,
	}
	if err := in.Sink.ApplyInstalled(ctx, state); err != nil {
		// The bytes and manifest are accurate; the retry re-downloads nothing.
		return nil, fmt.Errorf("embeddedllm: registering the installed model in config: %w", err)
	}

	return &InstallReport{
		Manifest: manifest,
		Resolution: Resolution{
			Engine:        EngineMTPLX,
			Assets:        assets,
			Backend:       BackendMetal,
			PackingReason: PackingReasonMLXPack,
			GPU:           GPUFamilyAppleSilicon,
			ContextSize:   plan.ContextSize,
			Memory:        plan,
		},
		Hardware:      hw,
		RuntimeDir:    runtimeDir,
		ServerBinary:  finalPython,
		ModelFile:     packDir,
		PackingReason: PackingReasonMLXPack,
	}, nil
}

// mlxPipInstall installs the closed wheel set into the staged interpreter,
// offline: pip --require-hashes over the pin-generated requirements file, so
// a substitute byte fails here (ADR-092 D1). Child: mlxChildEnv, -I.
func (in *Installer) mlxPipInstall(ctx context.Context, stagedPython, workDir string) error {
	downloads, err := in.Layout.DownloadsDir()
	if err != nil {
		return err
	}
	wheels := EngineWheels()
	reqPath, err := in.Layout.safeJoin(workDir, mlxRequirementsName)
	if err != nil {
		return err
	}
	var req strings.Builder
	for _, w := range wheels {
		fmt.Fprintf(&req, "%s==%s --hash=sha256:%s\n", w.Name, w.Version, w.Asset.SHA256)
	}
	if err := os.WriteFile(reqPath, []byte(req.String()), 0o640); err != nil {
		return fmt.Errorf("embeddedllm: writing the pinned wheel requirements: %w", err)
	}
	defer func() { _ = os.Remove(reqPath) }() // never promoted with the tree

	pipCtx, cancel := context.WithTimeout(ctx, mlxPipInstallTimeout)
	defer cancel()
	env := mlxChildEnv(os.Environ(), in.hostOS(), stagedPython)
	args := []string{
		"-I", "-m", "pip", "install",
		"--no-index", "--no-deps", "--only-binary=:all:", "--require-hashes",
		"--no-cache-dir", "--disable-pip-version-check", "--no-input",
		"--find-links", downloads,
		"-r", reqPath,
	}
	if _, err := in.runCommand()(pipCtx, stagedPython, &RunOptions{Env: env}, args...); err != nil {
		return fmt.Errorf("embeddedllm: installing the pinned engine wheels (offline, %d distributions): %w", len(wheels), err)
	}
	in.logger().Info("embedded LLM engine wheels installed",
		"distributions", len(wheels), "source", downloads)
	return nil
}

// mlxSmoke proves the pinned engine imports in the provisioned interpreter
// before the multi-gigabyte pack is fetched — deliberately NOT MLX/Metal:
// the readiness poll is the GPU proof (ADR-092 D2), and a Metal context here
// would hold memory the gate just priced (ADR-092 D4). Child: mlxChildEnv, -I.
func (in *Installer) mlxSmoke(ctx context.Context, stagedPython, workDir string) error {
	smokeCtx, cancel := context.WithTimeout(ctx, mlxSmokeTimeout)
	defer cancel()
	env := mlxChildEnv(os.Environ(), in.hostOS(), stagedPython)
	out, err := in.runCommand()(smokeCtx, stagedPython, &RunOptions{Env: env},
		"-I", "-c", "import mtplx, mtplx.cli; v = getattr(mtplx, '__version__', 'unknown'); print(v)")
	if err != nil {
		return fmt.Errorf("%w: the pinned MTPLX engine did not import in the provisioned Python (%s): %s",
			ErrSmokeTestFailed, strings.TrimSpace(out), mlxSmokeHint())
	}
	if !strings.Contains(out, MTPLXVersion) {
		in.logger().Warn("embedded LLM engine smoke test reported an unexpected version",
			"want", MTPLXVersion, "got", strings.TrimSpace(out))
	}
	return nil
}

// mlxSweepDownloads prunes the downloads cache of everything the CURRENT pins
// do not name — a stale wheel can satisfy the same distribution with other
// bytes. --require-hashes is the primary gate; this keeps --find-links clean.
func (in *Installer) mlxSweepDownloads() error {
	downloads, err := in.Layout.DownloadsDir()
	if err != nil {
		return err
	}
	pinned := make(map[string]bool, 1+len(mtplxWheelPins))
	pinned[pythonArchiveAsset.ArchiveName] = true
	for _, w := range mtplxWheelPins {
		pinned[w.Asset.ArchiveName] = true
	}
	entries, err := os.ReadDir(downloads)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // nothing downloaded yet: nothing to sweep
		}
		return fmt.Errorf("embeddedllm: reading the downloads area: %w", err)
	}
	for _, entry := range entries {
		if pinned[entry.Name()] {
			continue
		}
		stale, serr := in.Layout.safeJoin(downloads, entry.Name())
		if serr != nil {
			return serr
		}
		if err := in.removeOwned(stale); err != nil {
			return err
		}
		in.logger().Info("removed a download the current pin table does not carry",
			"name", entry.Name())
	}
	return nil
}

func mlxSmokeHint() string {
	return "the wheel set in the pinned table and the interpreter it was installed into have to agree; " +
		"re-run the install, and if it persists the pin bump that changed mlxpins.go is suspect"
}

// checkWholeSetDiskSpaceMLX is the whole-set disk guard with the engine's
// extra headroom: the archives are not the footprint, because the CPython
// archive is extracted and the wheels are unpacked into it.
func (in *Installer) checkWholeSetDiskSpaceMLX(assets []Asset) error {
	required := RequiredFreeBytes(TotalBytes(assets)) + mlxProvisionHeadroomBytes
	free, err := in.freeSpace()(in.Layout.ModelRoot)
	if err != nil {
		in.logger().Warn("disk space check unavailable, proceeding without the whole-set guard",
			"path", in.Layout.ModelRoot, "required", required, "error", err)
		return nil //nolint:nilerr // best-effort guard; the write fails safely on ENOSPC
	}
	if free < required {
		return fmt.Errorf("%w for the full embedded-LLM set: %s available at %s, %s required "+
			"(artifacts %s + provisioning headroom %s)",
			ErrInsufficientDisk, formatBytes(free), in.Layout.ModelRoot,
			formatBytes(required), formatBytes(required-DefaultDiskHeadroom-mlxProvisionHeadroomBytes),
			formatBytes(mlxProvisionHeadroomBytes))
	}
	return nil
}

// runCommand is the Installer.RunCommand seam with its production default.
func (in *Installer) runCommand() CommandRunner {
	if in.RunCommand != nil {
		return in.RunCommand
	}
	return defaultCommandRunner
}
