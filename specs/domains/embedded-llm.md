# Embedded LLM

## Purpose

The embedded-LLM subsystem makes "run fully local" a single Settings action: c0wrk downloads a pinned inference runtime (the PrismML-Eng/llama.cpp fork) and the pinned **Ternary-Bonsai-2-27B** weights, provisions both for the machine's actual hardware, supervises `llama-server` as a loopback OpenAI-compatible endpoint, and registers it as the ordinary provider `embedded` with the model `Bonsai 2 27B`. It owns four things c0wrk had no spec for before: supervision of a long-lived local inference server, multi-gigabyte resumable downloads, localhost port allocation for an app-managed server, and a hardware probe for inference.

## Key Files

The subsystem (`core/embeddedllm/`) plus every existing file it touches. Port handling is split in two: `install.go` owns the ALLOCATION (`ephemeralLoopbackPort`, reached through the `Installer.AllocatePort` seam), `port.go` owns the pre-spawn SCAN (`SearchFreePort`), and `Server.EnsurePort` in `server.go` is the hook that runs it. Production wires `EnsurePort`; `AllocatePort` stays nil so the OS picks the initial port — see [Port allocation](#port-allocation).

**Subsystem (`core/embeddedllm/`)**

- `core/embeddedllm/registry.go` — compile-time artifact registry: `RuntimeTag` (the pinned fork release `prism-b10709-9a9394a`), `ModelRevision` (the pinned Hugging Face commit), per-packing model assets with SHA256 (HF LFS OID) and exact byte sizes, the vision projector, and per-platform × per-backend runtime archives with SHA256 taken from the GitHub REST per-asset `digest` field (including the paired Windows `cudart` archive). Pure lookups (`RuntimeAsset`, `CudartAsset`, `ModelAsset`, `MMProjAsset`, `ArtifactSet`, `TotalBytes`) plus `ValidateRegistry`, which self-checks the pin tables. `registry_test.go` pins the digests/sizes verbatim and proves the validator is not vacuous — against a local fixture, never by mutating the shared tables
- `core/embeddedllm/download.go` — the resumable downloader (`Downloader`, `Download`, `VerifyFile`, `Result`, `ProgressFunc`, `RequiredFreeBytes`): HTTP `Range` resume from a `<destination>.part` file, the two explicit Range fallbacks, throttled progress callbacks, context cancellation, fail-closed SHA256 verification before promotion, and an artifact-sized disk guard. Deliberately **not** `toolmanager.Download` (see Invariants). `download_test.go` drives it against an HTTPS `httptest` server in Range/ignore-Range/416 modes with an injectable `FreeSpace`
- `core/embeddedllm/hardware.go` — RAM probe (`sysctl hw.memsize` / `/proc/meminfo` / `GlobalMemoryStatusEx`) and accelerator probe (`nvidia-smi` → `nvcc` → `rocminfo`/`rocm-smi`/`hipcc` → `vulkaninfo` → Metal on darwin/arm64 → CPU), producing a `Hardware` value. Also owns the `Backend` vocabulary the registry tables are keyed by. The OS-specific RAM read lives in the two build-tagged companions `hardware_unix.go` (`!windows`: darwin `sysctl` + linux `/proc/meminfo`) and `hardware_windows.go` (`GlobalMemoryStatusEx` via `syscall`); every parser is in the untagged file so it is testable on all three platforms
- `core/embeddedllm/resolve.go` — pure function `(platform, backend, ramGiB) → Resolution`: runtime asset(s), packing, `-ngl`, context size, image-max-tokens, cudart requirement. No I/O, fully table-tested. It consults the registry through the `AssetTable` seam and never invents a URL or checksum; a probed backend the pin cannot serve is degraded (never refused) to the best build the platform does have
- `core/embeddedllm/layout.go` — the on-disk layout. `Layout{RuntimesRoot, ModelRoot}` is built by `NewLayout` from roots the centralized path API resolved (`config.RuntimesDir` / `config.EmbeddedModelDir`), so the subsystem never re-derives `<agentDir>/runtimes` itself; every derived path (`RuntimeDir`, `RuntimeStagingDir`, `RuntimeRetiredDir`, `DownloadsDir`, `ManifestPath`, `Destination`, `ModelFile`) goes through one containment-checked join over `pathutil.IsWithinPath`. `Layout.Owns` is the ownership predicate every deletion is gated on, `RuntimeDirName` is the `llama-<tag>-<backend>` shape, and `ServerBinaryPath` locates `llama-server` in an extracted tree by a deterministic walk (the archives nest it, at a depth that differs per platform)
- `core/embeddedllm/install.go` — Install/Remove orchestration: `Installer` (with injectable `Downloader`, `Probe`, `RunCommand`, `AllocatePort`, `Stop`, `Now`, `HostOS` seams), `InstallOptions`, `InstallReport`, `Manifest` + `ReadManifest`/atomic write, `Progress` + `InstallProgressFunc`, the `ConfigSink`/`InstallState` boundary to the config layer, per-component progress, the independent verification gate, archive extraction with traversal/symlink/size guards, the staging → retire → rename runtime swap, and the macOS quarantine-clear + ad-hoc codesign + `--version` smoke test. It also owns the production port allocator `ephemeralLoopbackPort` (bind `127.0.0.1:0`, read the assignment back, close), which `Installer.allocatePort` reaches whenever the `AllocatePort` seam is `nil`
- `core/embeddedllm/port.go` — the pre-spawn free-port scan: the `MinLoopbackPort`/`MaxLoopbackPort` bounds (mirroring `backend/config`'s `EmbeddedLLMMinPort`/`EmbeddedLLMMaxPort`, which core cannot import), `PortProber` (the injection seam), `SearchFreePort` (walk upward from the preferred port until one is bindable; a preference below the floor starts at it, an exhausted range is `ErrNoFreePort`, ctx is checked per candidate) and the production prober `loopbackPortFree` (bind and release — the same test `llama-server` itself applies, so a lingering `TIME_WAIT` socket is judged the way the server would judge it). `port_test.go` covers the scan against a staged prober and against a really occupied loopback port
- `core/embeddedllm/server.go` — the supervisor: `Server` (with injectable `Spawn`, `EnsurePort`, `OnState`, `HTTPClient`, `Now`, `HostOS`, `Platform` seams and four budget fields), the `State` machine plus `StateEvent`/`Status`, `Load`/`Unload`/`Stop`/`SetInstalled`, the single-instance gate, `LaunchSpec` with `Args`/`Validate` (the only place the command line exists), the `Process`/`SpawnFunc`/`LaunchCommand` seams and the production `spawnOSServer`, `/v1/models` readiness polling, output pumping into `slog` with a bounded tail that ends up in every failure message, crash detection, and graceful-then-forced termination. `EnsurePort` is nil only for a caller with no config to keep in sync; production always wires it
- `core/embeddedllm/idle.go` — the auto-unload budget: `AutoUnload` (plus `DefaultAutoUnload`/`NewAutoUnload`), the unexported `idleTimer` tracker, and the `Server` surface around it (`SetAutoUnload`, `AutoUnloadPolicy`, `MarkActivity`, `IdleRemaining`, `LastActivity`). Its defining rule is that weight-load time is never charged to the idle budget
- `core/embeddedllm/transport.go` — the ensure-loaded request path: `Loader` (the supervisor seam, satisfied by `*Server` as-is), the optional `PortSource` capability (`*Server.Port`, read to redirect a request whose URL was built from a base_url that predates a port move), `EnsureLoadedTransport`/`NewEnsureLoadedTransport` (the `http.RoundTripper` that makes the model resident before the request, aims it at the live port, arms the request budget only once the model can answer, and stamps activity when the response completes), `EnsureLoadedClient` (derives the provider entry's client, moving the shared LLM timeout off `http.Client.Timeout` and into the transport so the cold-load wait is not charged to it) and `ErrLoadWaitTimeout`/`DefaultLoadWaitTimeout`

**Existing touch points**

- `backend/config/config.go` — `EmbeddedLLMConfig` (+ `AutoUnloadConfig`) with yaml tags and the identity constants (`EmbeddedLLMProviderName`, `EmbeddedLLMModelName`, `EmbeddedLLMHost`, port bounds, the 60-minute default); `Config.SyncEmbeddedLLMProvider` / `LLMConfig.SyncEmbeddedProvider` generate or remove the backend-owned provider record and write the `llm.models` context-window override; `validateEmbeddedLLM` is wired into `validate()` and `LoadWithResult` syncs on every load
- `backend/config/defaults.go` — the `auto_unload` pointer defaults (`enabled: true`, `minutes: 60`); every other field's zero value already IS the documented not-installed default
- `backend/config/paths.go` — path constructors; owns `ModelsDir` (`<agentDir>/models`, the flat embedding-model files), `ToolsDir`/`ToolsBinDir`, and the two embedded roots `RuntimesDir` (`<agentDir>/runtimes`) and `EmbeddedModelDir` (`<agentDir>/models/bonsai-2-27b`). `TestEmbeddedLLMDirs` pins both outside the tools tree, which is what makes the agent-PATH isolation a checked property rather than a convention
- `backend/frontend_api_embedded.go` — the RPC surface and the wiring of core into the app: `GetEmbeddedLLMStatus` (the read-only getter, no error — an unconstructable subsystem reports `available: false`), `InstallEmbeddedLLM` (the synchronous gates only — single-run, bounded probe, the 16 GiB refusal — then a BACKGROUND run), `RemoveEmbeddedLLM`, `LoadEmbeddedLLM`, `UnloadEmbeddedLLM`, `SetEmbeddedLLMAutoUnload`; the DTOs (`EmbeddedLLMStatus`, `EmbeddedLLMStateData`, `EmbeddedLLMProgressData`); the production `ConfigSink` (`embeddedConfigSink`: the reference state mutation plus the atomic save-or-rollback, `config:updated` and the judge/router rebuild); the event emitters (`emitEmbeddedLLMState`, `emitEmbeddedInstallProgress`, `emitEmbeddedRuntimeError`); the lazily built, two-mutex subsystem state (`embeddedLLMState` on `FrontendAPI.embedded`, which also owns the manifest snapshot and the state-event mute that keeps one operation to one event); and the lifecycle hooks `FrontendAPILifecycle.InitEmbeddedLLM` / `StopEmbeddedLLM` (never Wails-bound); the pre-spawn port check `embeddedEnsurePort` (wired onto `Server.EnsurePort` by `embeddedBuild`) with `persistEmbeddedPort`, which writes a moved port to `embedded_llm.port` and regenerates the provider record — best-effort, since the transport redirect keeps the model usable even when the config write fails; and the router-side seam that makes a cold request load the model — `toBuilderConfigLocked` (the single wrapper every production `ToBuilderConfig` call site goes through), `applyEmbeddedLoader`, `syncEmbeddedBuilderSeam` (the builder-level default that also reaches per-session routers), `embeddedLoaderRef`, `rebuildRouterForEmbeddedTransport`, and the `embeddedLLMState.loader` atomic snapshot the injection reads without taking `st.mu`; plus the pre-dispatch gate for short-budget callers — `ensureEmbeddedReadyForLLMRequest` and its `activeModelIsEmbedded` predicate
- `backend/frontend_api_config.go` — **protects** the generated `llm.openai_compatible.embedded` entry: `UpdateLLMConfig` re-injects it from the authoritative state after building the candidate, because a non-nil `openai_compatible` request replaces the whole map; also maps the model to the `qwen3.8-27b` profile hint in `suggestModelProfileID`
- `backend/events.go` — the two global event-name constants `EventEmbeddedLLMInstallProgress` (`embedded_llm:install_progress`) and `EventEmbeddedLLMState` (`embedded_llm:state`); failures of a BACKGROUND run additionally reuse the existing `EventRuntimeError` toast with `error_code: "embedded_llm_install_failed"` / `"embedded_llm_remove_failed"`
- `core/builder.go` — `providerEntryFromConfig` attaches the ensure-loaded client through the existing `llm.ProviderEntry.HTTPClient` hook (the same mechanism `core/llmtls.RouterEntryClient` uses): the pin resolver runs first and `embeddedllm.EnsureLoadedClient` only decorates its answer, for the one entry the seam's `ProviderName` names. The same guard is the only place that sets `llm.ProviderEntry.ReasoningWire` (to `ReasoningWireChatTemplateKwargs`, the spelling the pinned fork parses — see [Reasoning effort and the family resolution](#reasoning-effort-and-the-family-resolution)); every other `openai_compatible` entry keeps the vendor default. `SetEmbeddedLLM` holds the builder-level default seam and `embeddedSeam` resolves it inside `buildRouter` — the one place every router is constructed, which is what covers the per-session router the session factory builds from a plain `ToBuilderConfig`. The seam is injected by the backend, not by `ToBuilderConfig` — see [Request path](#invariants)
- `core/builderconfig.go` — `BuilderEmbeddedLLMConfig` (`ProviderName` + `Loader` + `LoadWaitTimeout`), the injection seam for the supervisor instance core does not own; the zero value guards nothing
- `backend/frontend_api_prompt.go`, `backend/frontend_api_git.go`, `backend/session/manager_execution.go` — the three short-budget LLM callers, each of which gates BEFORE arming its own timeout: `OptimizePrompt` and `GenerateCommitMessage` call `ensureEmbeddedReadyForLLMRequest` ahead of `serviceLLMTimeout`, and the session manager's title generation runs `serviceLLMGate` inside `maybeSpawnTitleGeneration` ahead of its own `context.WithTimeout`. The gate reaches the manager through `Manager.SetServiceLLMGate` (`backend/session/manager.go`), wired once by `installServiceLLMGate` (`backend/frontend_api.go`) because the manager is built inside `NewApplication`, before any `FrontendAPI` exists; the builder-level seam travels the same route, as `SetEmbeddedLLM` on `appBuilder` (`backend/builder_iface.go`)
- `core/pathsegments.go` — cross-layer path segment constants, including `EmbeddedRuntimesRelativePath` (`runtimes`) and `EmbeddedModelRelativePath` (`models/bonsai-2-27b`)
- `desktop/startup_phases.go` — `(*App).initEmbeddedLLM` (Phase 5, right after `buildFrontendAPI`: one manifest read plus one snapshot event, so it stays far below the 50ms critical-phase budget) and `(*App).stopEmbeddedLLM` (called early in `Shutdown`, before the judge drain, so the gigabytes are released while the rest of the teardown still runs; a failure is logged, never fatal). Startup restores state from the manifest only — no download, no probe, no auto-load
- `frontend/src/api/embedded.ts` — the subsystem's ONLY path to the generated bindings: the validating RPC wrappers (`getEmbeddedLLMStatus` + `isEmbeddedLLMStatus`, `installEmbeddedLLM`, `removeEmbeddedLLM`, `loadEmbeddedLLM`, `unloadEmbeddedLLM`, `setEmbeddedLLMAutoUnload` with its local `MIN_AUTO_UNLOAD_MINUTES` refusal), the typed event subscriptions (`onEmbeddedLLMState`, `onEmbeddedLLMInstallProgress` — a malformed payload is reported, never silently dropped), the `EmbeddedLLMStatus` mirror of the backend DTO and `DEFAULT_AUTO_UNLOAD_MINUTES`
- `frontend/src/stores/embeddedLLMStore.ts` — the UI state both surfaces share (see [frontend/stores.md](frontend/stores.md)): the authoritative `status` snapshot (one writer, `setStatus`), `installing`, the per-component `progress` map, the mutating-RPC `busy` action and the last failed action's `error`; backend sync lives in the module-level `refreshEmbeddedLLMStatus` (never throws) and the refcounted `subscribeEmbeddedLLMEvents`. `refreshEmbeddedLLMStatus` carries the store's one deliberate cross-store side effect: when `installed` FLIPS it calls `invalidateConfigCache()` on `frontend/src/hooks/useConfigData.ts`, because that hook's module-level cache is the chat toolbar picker's model list and no other embedded path invalidates it
- `frontend/src/components/settings/EmbeddedLLMSettings.tsx` — the Settings block (mounted in `frontend/src/components/settings/LLMSettings.tsx` as the FIRST block of the provider section — directly under the Default Model field and above "+ Add compatible provider"): the state-derived action surface (Install / progress / record + Load-Unload + Remove / auto-unload) and its RPC handlers, with the presentation split into `frontend/src/components/settings/embedded/` — `EmbeddedLLMProgress.tsx` (the per-component rows, worded with the shared `frontend/src/lib/embeddedLLMLabels.ts` order/label/stage vocabulary), `EmbeddedLLMInstallRecord.tsx` (the informational label), `EmbeddedLLMAutoUnload.tsx` (the toggle + minutes draft), `EmbeddedLLMRemoveDialog.tsx` (the removal gate). See [Settings block](#settings-block-ui-states)
- `frontend/src/components/layout/EmbeddedModelStatus.tsx` — the status-bar block (mounted in `frontend/src/components/layout/StatusBar.tsx` between the vector-index block and the process-memory indicator): the always-visible counterpart of the Settings block. Like `ProcessMemoryStatus` it owns its LEADING separator and renders nothing at all — separator included — unless it has a state to report; when it does, it renders exactly one compact surface (the active artifact's download bar, an indeterminate weight-load bar, the residency indicator, or an explicit error hint). Its component/stage words come from `frontend/src/lib/embeddedLLMLabels.ts`, shared with `EmbeddedLLMProgress.tsx` so the two surfaces describing the same payload cannot drift. See [Status-bar indicator](#status-bar-indicator-ui-states)
- `frontend/src/components/ui/ModelPickerMenu.tsx` — the single model-picker implementation both model lists render (the Settings → LLM default-model picker through `allEnabledModels`, and the chat toolbar's `ModelCombobox` through `useConfigData`). Its `providerLabel` humanizes the backend-owned `embedded` config key to **Embedded**, while the value sent to the backend stays the composite `embedded/Bonsai 2 27B`. Its `groupByProvider` additionally hoists the `embedded` group to the TOP of the group list, because neither feed order puts it there: the settings list iterates a provider map whose JSON keys Go alphabetizes, and `all_models` arrives as (anthropic, chatgpt, sorted `openai_compatible`, sorted `anthropic_compatible`). Normalizing in the shared component is what keeps the two surfaces from drifting apart; every other provider keeps its input order
- `frontend/src/types/events.ts` (`EmbeddedLLMInstallProgressData` / `EmbeddedLLMStateData` plus the `EmbeddedLLMComponent`/`EmbeddedLLMStage` unions, their `GlobalEventMap` entries and the `isEmbeddedLLMInstallProgressData`/`isEmbeddedLLMStateData` guards — the state guard deliberately does NOT enumerate `packing`/`backend`, so a newly pinned backend cannot make the event fail validation)

## Core Types

```go
// Backend is the accelerator the runtime archive was built for, and the one
// the probe selected. Resolution order is fixed (see Flow).
type Backend string

const (
	BackendMetal   Backend = "metal"     // darwin/arm64
	BackendCUDA124 Backend = "cuda-12.4" // x64 only
	BackendCUDA128 Backend = "cuda-12.8" // x64 only
	BackendCUDA133 Backend = "cuda-13.3" // x64 only
	BackendROCm    Backend = "rocm"      // x64 only
	BackendVulkan  Backend = "vulkan"
	BackendCPU     Backend = "cpu"       // always-available fallback
)

// Packing is the ternary quantization of the weights actually on disk.
type Packing string

const (
	PackingPQ2_0  Packing = "PQ2_0"  // 7,206,168,928 B — default
	PackingPTQ1_0 Packing = "PTQ1_0" // 5,946,648,928 B — Vulkan only (no PQ2_0 kernels)
)

// Hardware is the probe result: everything resolution may depend on.
type Hardware struct {
	Platform string  // toolmanager.Platform() shape: "<goos>-<goarch>"
	Arch     string  // "amd64" | "arm64"
	RAMGiB   float64 // total system RAM, in GiB (bytes / 2^30) — not GB
	Backend  Backend // probed accelerator (BackendCPU when nothing else is found)
	CUDATag  string  // driver-derived CUDA asset tag ("" when not CUDA)
}

// Asset is one downloadable component with its integrity pin.
type Asset struct {
	Component   Component // "runtime" | "cudart" | "model" | "mmproj"
	URL         string
	SHA256      string // fail-closed: empty means REFUSE, never "skip verification"
	SizeBytes   int64  // exact expected size; drives the disk guard and progress totals
	ArchiveName string // on-disk cache file name
}

type Component string

const (
	ComponentRuntime Component = "runtime"
	ComponentCudart  Component = "cudart"  // Windows CUDA only: the paired DLL archive
	ComponentModel   Component = "model"
	ComponentMMProj  Component = "mmproj"
)

// Resolution is the pure output of (platform, backend, ramGiB).
type Resolution struct {
	Assets         []Asset // 1 runtime, or 2 on Windows CUDA (runtime + cudart)
	Backend        Backend // what the assets were ACTUALLY resolved for, post-degradation
	Packing        Packing
	Layers         int // -ngl: 0 on Intel Mac / CPU, 99 on GPU backends
	ContextSize    int // -c: RAM-tiered, NEVER 0 and never 262144
	ImageMaxTokens int // --image-max-tokens: 1024 on metal/vulkan/cpu, 0 = uncapped on CUDA/ROCm
	NeedsCudart    bool
}

// AssetTable is the registry seam Resolve consults. The production
// implementation delegates to the compile-time pins in registry.go; tests
// substitute a stub, so no test ever has to touch the pins or package state.
type AssetTable interface {
	RuntimeAsset(platform string, backend Backend) (Asset, bool)
	ArtifactSet(platform string, backend Backend, packing Packing) ([]Asset, error)
}

// The pins themselves (registry.go). Both are immutable refs — a release tag
// and a Hugging Face commit SHA, never a branch — so a rebuild cannot silently
// re-point at newer upstream bytes.
const (
	RuntimeTag    = "prism-b10709-9a9394a"                     // pinned fork release
	ModelRevision = "6ed5e12bf84b7a63069882c91dd9e9218647d17b"  // pinned HF commit of the weights repo
)

// Platform keys use the toolmanager.Platform() shape. windows-arm64 is
// deliberately absent (D9), so every lookup for it fails closed.
const (
	PlatformDarwinAMD64  = "darwin-amd64"
	PlatformDarwinARM64  = "darwin-arm64"
	PlatformLinuxAMD64   = "linux-amd64"
	PlatformLinuxARM64   = "linux-arm64"
	PlatformWindowsAMD64 = "windows-amd64"
)

// ErrArtifactNotPinned is the registry's fail-closed refusal for a
// (platform, backend, packing) combination this pin does not cover.
var ErrArtifactNotPinned = errors.New("no pinned embedded-LLM artifact")

// Registry accessors. Every one is a pure table lookup — no I/O, no upstream
// query. The boolean form IS the fail-closed contract: ok == false means "not
// pinned", never "unverified but downloadable anyway".
func SupportedPlatforms() []string
func IsSupportedPlatform(platform string) bool
func RuntimeAsset(platform string, backend Backend) (Asset, bool)
func CudartAsset(platform string, backend Backend) (Asset, bool)
func ModelAsset(packing Packing) (Asset, bool)
func MMProjAsset() Asset // unconditional: every install gets the projector
func SupportedPackings() []Packing
func ArtifactSet(platform string, backend Backend, packing Packing) ([]Asset, error)
func TotalBytes(assets []Asset) int64
func ValidateRegistry() error // self-check over the pin tables

// Downloader (download.go) fetches ONE pinned artifact to disk with resume,
// throttled progress and fail-closed verification. The zero value is usable;
// NewDownloader applies the documented defaults.
type Downloader struct {
	Client           *http.Client                     // nil → a client with NO overall timeout
	Logger           *slog.Logger                     // nil → slog.Default()
	ProgressInterval time.Duration                    // <= 0 → DefaultProgressInterval
	DiskHeadroom     int64                            // <= 0 → DefaultDiskHeadroom
	FreeSpace        func(path string) (int64, error) // nil → the platform probe; injectable for tests
}

// ProgressFunc reports (bytesDone, bytesTotal). It is called immediately at the
// resume offset, at throttled intervals during the transfer, and always once
// more at completion with done == total.
type ProgressFunc func(done, total int64)

// Result reports a verified download. Verified is always true when Download
// returns a nil error: there is no code path that hands back unverified bytes.
type Result struct {
	Path          string // the verified destination file, never the .part
	SizeBytes     int64
	BytesWritten  int64  // 0 on a cache hit; < SizeBytes when the transfer resumed
	SHA256        string // the verified digest
	Resumed       bool
	Cached        bool
	Restarted     bool   // a Range fallback discarded the resume offset
	RestartReason string // the explicit, user-facing reason for the restart
	Verified      bool
}

func NewDownloader(client *http.Client, logger *slog.Logger) *Downloader
func (d *Downloader) Download(ctx context.Context, asset Asset, dstPath string, progress ProgressFunc) (*Result, error)
func VerifyFile(path string, asset Asset) error
func RequiredFreeBytes(totalBytes int64) int64 // totalBytes + DefaultDiskHeadroom

const (
	PartialSuffix           = ".part"                // in-flight transfer marker
	DefaultProgressInterval = 100 * time.Millisecond // progress throttle
	DefaultDiskHeadroom     = 2 << 30                // 2 GiB, on top of the artifact
)

// Downloader refusals. All are sentinels so callers can branch with errors.Is
// and surface an actionable message instead of a generic failure.
var (
	ErrMissingChecksum    = errors.New("no pinned sha256 for this artifact")
	ErrChecksumMismatch   = errors.New("sha256 mismatch")
	ErrInsufficientDisk   = errors.New("insufficient free disk space")
	ErrArtifactTooLarge   = errors.New("transfer exceeded the pinned artifact size")
	ErrIncompleteTransfer = errors.New("transfer stopped before the pinned artifact size")
)

// State is the supervision state machine.
type State string

const (
	StateNotInstalled State = "not_installed"
	StateInstalled    State = "installed" // on disk, server not running (== unloaded)
	StateLoading      State = "loading"   // weights being loaded
	StateLoaded       State = "loaded"    // /v1/models answered with a non-empty list
	StateUnloading    State = "unloading"
	StateError        State = "error"
)

// Running reports whether the state implies a live llama-server process
// (loading, loaded or unloading).
func (st State) Running() bool

// Manifest is <models>/bonsai-2-27b/manifest.json — the durable record of what
// was installed. Startup trusts THIS, not the network and not a probe.
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

// Progress is one per-component download/verify/extract update.
type Progress struct {
	Component  Component
	Stage      string // "downloading" | "verifying" | "extracting" | "signing" | "done"
	BytesDone  int64
	BytesTotal int64
}

// ErrInsufficientRAM is the typed refusal for D6 (< 16 GiB).
var ErrInsufficientRAM = errors.New("embedded LLM requires at least 16 GiB of system RAM")

// ErrRAMUnknown is returned when total system RAM cannot be read. The 16 GiB
// gate is a safety gate, so an unreadable size refuses the install instead of
// assuming the machine is big enough.
var ErrRAMUnknown = errors.New("cannot determine total system RAM")

// Layout is the on-disk footprint, built from roots the centralized path API
// resolved. The zero value is invalid; ErrLayoutInvalid reports empty,
// relative, identical or nested roots.
type Layout struct {
	RuntimesRoot string // <agentDir>/runtimes      — config.RuntimesDir
	ModelRoot    string // <agentDir>/models/bonsai-2-27b — config.EmbeddedModelDir
}

func NewLayout(runtimesRoot, modelRoot string) (Layout, error)
func RuntimeDirName(backend Backend) string // "llama-<RuntimeTag>-<backend>"
func ServerBinaryPath(runtimeDir, goos string) (string, error) // walk, not a join

func (l Layout) RuntimeDir(backend Backend) (string, error)        // installed tree
func (l Layout) RuntimeStagingDir(backend Backend) (string, error) // ".staging"
func (l Layout) RuntimeRetiredDir(backend Backend) (string, error) // ".old"
func (l Layout) DownloadsDir() (string, error)                     // archive staging
func (l Layout) ManifestPath() (string, error)
func (l Layout) Destination(asset Asset) (string, error) // by component
func (l Layout) ModelFile(packing Packing) (string, error)
func (l Layout) Owns(path string) bool                   // the deletion gate
func (l Layout) EnsureRoots() error

// Progress stages. One component walks downloading → verifying →
// (extracting → signing for runtime archives) → done, with exactly one done.
const (
	StageDownloading = "downloading"
	StageVerifying   = "verifying"
	StageExtracting  = "extracting"
	StageSigning     = "signing"
	StageDone        = "done"
)

type InstallProgressFunc func(Progress) // named apart from download.go's
// ProgressFunc, which reports raw (done, total) for a single artifact

// Installer orchestrates Install/Remove. Every external effect is a field, so
// the whole flow is testable without a network, without multi-gigabyte
// artifacts and without macOS binaries.
type Installer struct {
	Layout       Layout
	Sink         ConfigSink  // required by Install AND Remove
	Logger       *slog.Logger
	Downloader   *Downloader
	Probe        func(ctx context.Context, logger *slog.Logger) (Hardware, error)
	RunCommand   CommandRunner
	AllocatePort func(ctx context.Context) (int, error)
	Stop         func(ctx context.Context) error // nil = no supervisor wired yet
	Now          func() time.Time
	HostOS       string // "" → runtime.GOOS; tests force the darwin branch
}

type CommandRunner func(ctx context.Context, name string, args ...string) (string, error)

type InstallOptions struct {
	Port     int          // 0 → AllocatePort
	Platform string       // "" → probed
	Backend  Backend      // "" → probed (still degraded by resolution)
	Progress InstallProgressFunc
}

type InstallReport struct {
	Manifest     Manifest
	Resolution   Resolution
	Hardware     Hardware
	RuntimeDir   string
	ServerBinary string
	ModelFile    string
}

// InstallState is the durable record handed to the config layer, and the ONLY
// channel through which this subsystem reaches config.yaml (core cannot import
// backend/config). AutoUnload* are DEFAULTS: the sink must apply them only
// where the operator has not chosen explicitly (both config knobs are
// pointers, so nil is distinguishable from an explicit false).
type InstallState struct {
	Packing           Packing
	Backend           Backend
	Port              int
	ModelFile         string
	RuntimeVersion    string
	InstalledAt       string // RFC 3339
	ContextSize       int
	AutoUnloadEnabled bool
	AutoUnloadMinutes int
}

const (
	DefaultAutoUnloadEnabled = true
	DefaultAutoUnloadMinutes = 60 // mirrors config.EmbeddedLLMDefaultAutoUnloadMinutes
)

// ConfigSink is the config-layer half of the boundary; the reference
// implementation and its contract tests live in
// backend/config/embedded_llm_sink_test.go.
type ConfigSink interface {
	ApplyInstalled(ctx context.Context, state InstallState) error
	ApplyRemoved(ctx context.Context) error
}

func NewInstaller(layout Layout, logger *slog.Logger) *Installer
func (in *Installer) Install(ctx context.Context, opts InstallOptions) (*InstallReport, error)
func (in *Installer) Remove(ctx context.Context) error
func ReadManifest(path string) (Manifest, error)

var ErrLayoutInvalid = errors.New("invalid embedded-LLM storage layout")
var ErrNotInstalled = errors.New("embedded LLM is not installed") // no manifest
var ErrSmokeTestFailed = errors.New("the provisioned llama-server did not run")

// LoopbackHost is the only address the server is ever bound to.
const LoopbackHost = "127.0.0.1"

// LaunchSpec is everything one llama-server invocation is derived from: the
// install record plus the pure launch policy in resolve.go. Args renders the
// command line and Validate is the last gate before exec.
type LaunchSpec struct {
	ServerBinary   string
	ModelFile      string // -m
	MMProjFile     string // --mmproj; empty omits the flag (text-only serving)
	Host           string // --host, always LoopbackHost
	Port           int    // --port, the persisted loopback port
	Layers         int    // -ngl, layersFor(platform, manifest.Backend)
	ContextSize    int    // -c, the RAM tier frozen in manifest.json
	ImageMaxTokens int    // --image-max-tokens; ImageMaxTokensUncapped omits it
}

func (spec LaunchSpec) Args() []string
func (spec LaunchSpec) Validate() error
func (spec LaunchSpec) ModelsURL() string // http://127.0.0.1:<port>/v1/models

// LaunchCommand is the resolved invocation handed to SpawnFunc, so a substituted
// spawner still exercises — and can assert — the real Args and Env.
type LaunchCommand struct {
	Binary string
	Args   []string
	Env    []string
	Dir    string
}

// Process is the supervisor's handle on one spawned llama-server, and the seam
// every supervision test drives. Stdout/Stderr are drained before Wait returns,
// which is the ordering os/exec requires for piped output.
type Process interface {
	Wait() error
	Pid() int
	Signal(sig os.Signal) error
	Kill() error
	Stdout() io.Reader
	Stderr() io.Reader
}

// SpawnFunc starts the process. ctx bounds the START only: the returned Process
// outlives it, because the production spawn detaches the context on purpose.
type SpawnFunc func(ctx context.Context, cmd LaunchCommand) (Process, error)

// StateEvent is one transition, in the shape the backend forwards as the global
// `embedded_llm:state` event. Message carries a cause for StateError only.
type StateEvent struct {
	State   State
	Port    int
	Message string
}

// Status is a UI-safe snapshot; Pid is 0 when no process is running.
type Status struct {
	State   State
	Port    int
	Pid     int
	Since   time.Time
	Message string
}

// Server supervises one llama-server for one installation. As with Installer,
// every external effect is an injectable field, so the whole state machine —
// including a multi-minute load, a crash and the idle unload — is testable
// without a runtime, without weights and without waiting.
type Server struct {
	Layout     Layout
	Logger     *slog.Logger
	AutoUnload AutoUnload // construction-time policy; SetAutoUnload owns it afterwards
	Spawn      SpawnFunc  // nil → spawnOSServer, the real llama-server
	// EnsurePort re-checks the persisted port immediately before the spawn and
	// may return (and persist) a replacement. PRODUCTION ALWAYS WIRES IT
	// (backend.embeddedEnsurePort → SearchFreePort); nil trusts the persisted
	// port and is only correct for a caller with no config to keep in sync.
	EnsurePort func(ctx context.Context, port int) (int, error)
	OnState    func(StateEvent)
	HTTPClient *http.Client // nil → a client whose probes carry ProbeTimeout
	Now        func() time.Time
	HostOS     string // "" → runtime.GOOS: binary suffix + library-path policy
	Platform   string // "" → the host "<goos>-<goarch>", for -ngl

	ReadyTimeout      time.Duration // <= 0 → DefaultReadyTimeout
	ReadyPollInterval time.Duration // <= 0 → DefaultReadyPollInterval
	ProbeTimeout      time.Duration // <= 0 → DefaultProbeTimeout
	StopTimeout       time.Duration // <= 0 → DefaultStopTimeout
}

func NewServer(layout Layout, logger *slog.Logger) *Server // starts at not_installed
func (s *Server) Load(ctx context.Context) error
func (s *Server) Unload(ctx context.Context) error
func (s *Server) Stop(ctx context.Context) error // == Unload; matches Installer.Stop
func (s *Server) SetInstalled(installed bool) error
func (s *Server) State() State
func (s *Server) Status() Status
func (s *Server) Port() int
func (s *Server) BaseURL() string // http://127.0.0.1:<port>/v1

// The loopback port bounds. They mirror backend/config's EmbeddedLLMMinPort /
// EmbeddedLLMMaxPort (the layering forbids importing it), and usablePort is the
// last gate before exec.
const (
	MinLoopbackPort = 1024
	MaxLoopbackPort = 65535
)

// PortProber reports whether a loopback port can be bound right now — the
// injection seam for the scan. nil selects loopbackPortFree (bind and release).
type PortProber func(ctx context.Context, port int) bool

// SearchFreePort returns the first bindable port at or above preferred, scanning
// upward. A preferred below MinLoopbackPort (including the 0 "not allocated
// yet" sentinel) starts the scan at the floor; exhaustion reports ErrNoFreePort;
// ctx is checked per candidate. The result is a port that WAS free, not a
// reservation — there is no fd-passing path to llama-server.
func SearchFreePort(ctx context.Context, preferred int, probe PortProber) (int, error)

var ErrNoFreePort = errors.New("no free loopback port for the embedded LLM")

// AutoUnload is the in-memory shape of embedded_llm.auto_unload.
type AutoUnload struct {
	Enabled bool
	Idle    time.Duration // non-positive → clamped to the 60-minute default
}

func DefaultAutoUnload() AutoUnload                     // enabled, 60 min
func NewAutoUnload(enabled bool, minutes int) AutoUnload // from the persisted shape
func (s *Server) SetAutoUnload(policy AutoUnload)
func (s *Server) AutoUnloadPolicy() AutoUnload
func (s *Server) MarkActivity()
func (s *Server) IdleRemaining() (time.Duration, bool) // ok == false: no timer armed
func (s *Server) LastActivity() time.Time

// Supervisor refusals. All are sentinels, so a caller can branch with errors.Is
// and surface an actionable message; ErrServerDied and ErrLoadTimeout carry the
// last lines of the server's own log in the wrapped detail.
var (
	ErrServerDied        = errors.New("the embedded LLM server process exited")
	ErrLoadTimeout       = errors.New("the embedded LLM server did not become ready")
	ErrLaunchSpecInvalid = errors.New("invalid embedded-LLM launch specification")
	ErrServerBusy        = errors.New("the embedded LLM server is running")
)

// Loader is what the ensure-loaded transport needs from the supervisor. *Server
// satisfies it unchanged, pinned by a compile-time assertion.
type Loader interface {
	Load(ctx context.Context) error // idempotent, single-instance
	MarkActivity()                  // restarts the idle budget
}

// PortSource is an OPTIONAL Loader capability: the loopback port the supervisor
// actually bound. *Server satisfies it as-is (Port), pinned by an assertion.
//
// It exists because the request URL is not authoritative — the router builds it
// from the provider base_url, which is derived from the PERSISTED port, and the
// load re-checks that port and may move it. Declaring it optional (rather than
// growing Loader) keeps every other Loader valid: one that reports no port simply
// gets no redirect.
type PortSource interface {
	Port() int // 0 = no server has been started, which is not a port to aim at
}

// EnsureLoadedTransport is the http.RoundTripper on the embedded provider entry:
// the wrapped base, the Loader, the wait budget, the request budget it arms only
// once the model is resident, and the ONE in-flight load its concurrent requests
// coalesce onto.
type EnsureLoadedTransport struct{ /* base, loader, waitTimeout, requestTimeout, logger, inflight, waiters */ }

// NewEnsureLoadedTransport arms no request budget of its own (requestTimeout 0),
// so the request runs on whatever deadline its caller gave it — the historical
// contract. Production goes through EnsureLoadedClient, which does arm one.
func NewEnsureLoadedTransport(base http.RoundTripper, loader Loader, waitTimeout time.Duration, logger *slog.Logger) *EnsureLoadedTransport
func (t *EnsureLoadedTransport) RoundTrip(req *http.Request) (*http.Response, error)
func (t *EnsureLoadedTransport) CloseIdleConnections() // forwarded, so the stdlib still reaches the real pool

// EnsureLoadedClient derives the entry's client: a CLONE of base (the pin
// resolver's answer — nil when no pin applies) or of shared (the router-level LLM
// client), carrying the transport. Cloning is what keeps
// timeouts.llmRequestTimeout — as the transport's requestTimeout, with the
// clone's own Timeout zeroed, because a client-level timeout would also cover
// the cold-load wait. loader nil returns base unchanged, so the pin/proxy
// resolution keeps its "nil = use the router-level client" answer.
func EnsureLoadedClient(base, shared *http.Client, loader Loader, waitTimeout time.Duration, logger *slog.Logger) *http.Client

// DefaultLoadWaitTimeout bounds one request's wait for a cold model. It exceeds
// DefaultReadyTimeout on purpose: the supervisor's ready budget is the authority
// on "this load is wedged".
const DefaultLoadWaitTimeout = DefaultReadyTimeout + 2*time.Minute // 17 min

// Distinct from ErrLoadTimeout (the supervisor's ready budget): the transport's
// own budget expired while the load was still legitimately running, and the load
// was NOT cancelled.
var ErrLoadWaitTimeout = errors.New("the embedded LLM did not become resident within the load wait budget")
```

Persisted config (see [Configuration](#configuration)):

```go
type EmbeddedLLMConfig struct {
	Installed      bool             `yaml:"installed"`
	Packing        string           `yaml:"packing"`         // informational: what was resolved
	Backend        string           `yaml:"backend"`         // informational: what was probed
	Port           int              `yaml:"port"`            // 0 = allocate at install time
	ModelFile      string           `yaml:"model_file"`
	RuntimeVersion string           `yaml:"runtime_version"` // pinned fork release tag
	InstalledAt    string           `yaml:"installed_at"`
	AutoUnload     AutoUnloadConfig `yaml:"auto_unload"`
}

type AutoUnloadConfig struct {
	Enabled *bool `yaml:"enabled"` // default true
	Minutes *int  `yaml:"minutes"` // default 60
}
```

## Flow

### Hardware probe → resolution

```
probeHardware()
├─ RAM:  darwin  sysctl hw.memsize
│        linux   /proc/meminfo (MemTotal)
│        windows GlobalMemoryStatusEx
└─ accelerator, first hit wins (fixed order):
   1. nvidia-smi         → CUDA driver version → asset tag
   2. nvcc --version     → CUDA toolkit fallback when nvidia-smi is absent
   3. rocminfo / rocm-smi / hipcc → rocm
   4. vulkaninfo         → vulkan
   5. darwin/arm64       → metal
   6. otherwise          → cpu

   CUDA asset tag map:  driver ≥ 13.3 → "13.3"
                        13.x, or ≥ 12.8 → "12.8"
                        12.x            → "12.4"
                        < 12.x          → NOT a hit; the ladder keeps looking,
                                          since no pinned archive would load
   CUDA and ROCm are x64-only: a non-x64 platform with CUDA detected
   resolves to the CPU build (linux-arm64 + CUDA → cpu).
   RAM is a hard input, not a best-effort one: an unreadable size yields
   ErrRAMUnknown rather than letting the 16 GiB gate pass on a guess.

resolve(platform, backend, ramGiB)  →  Resolution        [pure, no I/O]
├─ ramGiB < 16                    → ErrInsufficientRAM (the FIRST check)
├─ effective backend (see degradation rules below): metal only on
│           darwin-arm64; CUDA/ROCm only on amd64; a CUDA tag the platform has
│           no archive for clamps DOWN to the nearest older pinned tag; any
│           other unpinned pair → cpu
├─ packing: PTQ1_0 on vulkan (the one backend with no PQ2_0 kernels);
│           PQ2_0 everywhere else. There is NO RAM-based downgrade.
│           mmproj-Q8_0 is ALWAYS part of the set.
├─ assets:  runtime[platform][effective]  (+ cudart on windows CUDA)
│           + model[packing] + mmproj      — ArtifactSet order = download order
├─ -ngl:    0 on Intel Mac and on the CPU build; 99 on CUDA/ROCm/Vulkan and on
│           Apple Silicon (there the arm64 archive IS the Metal build)
├─ -c:      RAM tier (see table below) — never 0, never 262144
└─ --image-max-tokens: 1024 on metal/vulkan/cpu; uncapped on CUDA/ROCm
                       (uncapped = the flag is omitted entirely)

  RAM (GiB)   context    reachable through Resolve?
  ≤ 11        8192       no — below the 16 GiB gate
  ≤ 23        16384      yes
  ≤ 35        32768      yes
  ≤ 71        65536      yes
  > 71        131072     yes          (KV cache ≈ 64 KiB/token)

  ramGiB is FLOORED to a whole GiB before the tier comparison, matching the
  integer arithmetic of the demo script the tiers come from. It matters on
  Linux, where MemTotal is reported below the nominal size: a 24 GB machine
  reads ~23.9 GiB and belongs to the 16384 tier, not the next one up.
```

**Backend → asset → packing.** What each *effective* backend resolves to. The
archive stems are the pinned `prism-b10709-9a9394a` release names; checksums and
exact sizes live in `registry.go`.

| Effective backend | Runtime archive stem(s) | Packing | `-ngl` | `--image-max-tokens` | Second runtime component |
| --- | --- | --- | --- | --- | --- |
| `metal` (darwin-arm64 only) | `bin-macos-arm64` | `PQ2_0` | 99 | 1024 | — |
| `cuda-12.4` | linux `bin-linux-cuda-12.4-x64`, win `bin-win-cuda-12.4-x64` | `PQ2_0` | 99 | uncapped | `cudart-…-win-cuda-12.4-x64` (Windows only) |
| `cuda-12.8` | linux `bin-linux-cuda-12.8-x64` (no Windows archive in this pin) | `PQ2_0` | 99 | uncapped | — |
| `cuda-13.3` | linux `bin-linux-cuda-13.3-x64`, win `bin-win-cuda-13.3-x64` | `PQ2_0` | 99 | uncapped | `cudart-…-win-cuda-13.3-x64` (Windows only) |
| `rocm` | linux `bin-ubuntu-rocm-7.2-x64`, win `bin-win-hip-radeon-x64` | `PQ2_0` | 99 | uncapped | — |
| `vulkan` | `bin-ubuntu-vulkan-x64`, `bin-ubuntu-vulkan-arm64`, `bin-win-vulkan-x64` | **`PTQ1_0`** | 99 | 1024 | — |
| `cpu` | `bin-macos-x64`, `bin-macos-arm64`, `bin-ubuntu-x64`, `bin-ubuntu-arm64`, `bin-win-cpu-x64` | `PQ2_0` | 0 (99 on darwin-arm64) | 1024 | — |

Model assets, by packing — plus the projector, which is unconditional because
Bonsai 2 27B is multimodal:

| Component | Asset | Selected when |
| --- | --- | --- |
| `model` | `Ternary-Bonsai-2-27B-PQ2_0.gguf` | every backend except Vulkan |
| `model` | `Ternary-Bonsai-2-27B-PTQ1_0.gguf` | Vulkan (no `PQ2_0` kernels) |
| `mmproj` | `Ternary-Bonsai-2-27B-mmproj-Q8_0.gguf` | always |

**Degradation rules.** Resolution never refuses because of an unsupported
*backend* — only because of RAM or a platform with no pins at all. A working CPU
install beats an error on a machine that could have run the model.

| Probed | Platform | Effective | Why |
| --- | --- | --- | --- |
| `metal` | anything but darwin-arm64 | `cpu` | Metal exists only on Apple Silicon |
| `cuda-*`, `rocm` | any non-amd64 | `cpu` | CUDA/ROCm archives are x64-only |
| `cuda-12.8` | windows-amd64 | `cuda-12.4` | this pin has no Windows 12.8 archive; clamp **down** (a 12.4 build runs on a newer driver, never the reverse) |
| any accelerator | a platform that does not pin it | `cpu` | `cpu` is pinned for every supported platform |
| any | a platform with no pins at all | — | `ErrArtifactNotPinned` |

`Resolution.Backend` records the *effective* backend, and it — never the probed
value — is what the manifest and the informational Settings label must carry:
recording the probed backend would describe an install that is not on disk.

### Install (explicit user click only)

```
Installer.Install(ctx, InstallOptions)   [runs in background; the RPC returns immediately]
├─ hardware probe      → RAM is a hard input: ErrRAMUnknown refuses instead of guessing
├─ resolve             → Resolve(platform, backend, ramGiB). Its 16 GiB RAM gate is
│                        the FIRST check, so an undersized machine plans no assets,
│                        creates no directory and downloads nothing (ErrInsufficientRAM)
├─ ensure roots        → <agentDir>/runtimes, <agentDir>/models/bonsai-2-27b,
│                        <agentDir>/runtimes/downloads
├─ disk guard          → RequiredFreeBytes(TotalBytes(set)) measured at the model root,
│                        before the first byte (~7.3 GiB PQ2_0 / ~6.1 GiB PTQ1_0
│                        + runtime 150–650 MB + 391 MB cudart on Windows CUDA). The
│                        Downloader re-checks each artifact at its own destination, so
│                        a second volume is covered too
├─ port allocation     → InstallOptions.Port, else Installer.AllocatePort; persisted in
│                        manifest.json AND embedded_llm.port
├─ RUNTIME phase       → for runtime (+ cudart on Windows CUDA), in that order:
│    ├─ download with resume + per-component progress   → embedded_llm:install_progress
│    ├─ SHA256 verify (fail-closed; mismatch deletes the partial and errors)
│    └─ extract into <runtimes>/llama-<tag>-<backend>.staging
├─ macOS only          → xattr -cr <staging>, ad-hoc codesign each Mach-O image
│                        (llama* and *.dylib), then `llama-server --version`.
│                        Failure → ErrSmokeTestFailed carrying the Gatekeeper fix.
│                        This runs BEFORE the weights on purpose: an unrunnable
│                        runtime fails in seconds, not after a multi-gigabyte download
├─ promote             → retire the previous tree to .old, rename staging into place,
│                        delete .old (rollback on a failed rename). The old tree dies
│                        only after the new one is secured AND proven to run
├─ WEIGHTS phase       → for model, mmproj: download straight to their final path (the
│                        artifact IS the file, so there is nothing to extract)
│                        → SHA256 verify
├─ write manifest.json                                  [atomically, and only now]
└─ ConfigSink.ApplyInstalled(InstallState)
     → embedded_llm.* + the auto-unload defaults (unset knobs only)
     → Config.SyncEmbeddedLLMProvider(state.ContextSize) generates
        llm.openai_compatible.embedded + the llm.models context_window override
        (the only caller that passes a non-zero tier, because it is the only one
         that has resolved it) → persist → rebuild the router

A failure at any step leaves no manifest and no provider entry: a half-installed
model is never registered. Bytes that DID verify are deliberately kept — they are
cache hits for the next attempt, which is what makes a multi-gigabyte install
resumable rather than restartable. Install refuses outright when no ConfigSink is
wired: bytes that cannot be registered are bytes nobody can use. A sink failure
after the manifest was written keeps the manifest, because it is accurate — the
retry re-downloads nothing and rewrites the same record.
```

### Remove (explicit user click only)

```
Installer.Remove(ctx)
├─ Installer.Stop(ctx) → the supervisor terminates llama-server FIRST. A stop
│                        failure aborts the removal: a live process holding the
│                        files cannot be deleted on Windows, and on Unix it would
│                        keep serving from unlinked inodes
├─ delete <agentDir>/models/bonsai-2-27b/   [the manifest goes with it, so the
│                                             on-disk state is "not installed"
│                                             before anything else happens]
├─ delete every <agentDir>/runtimes/llama-* tree — the installed one plus any
│  .staging / .old leftover of an interrupted install, and any other backend's
│  tree — then <agentDir>/runtimes/downloads/
├─ ConfigSink.ApplyRemoved()
│    → migrate llm.default_model off embedded/Bonsai 2 27B to the first other
│      enabled model. NOT to "": validate() rejects an empty default_model, so
│      clearing it would break the next load and the next settings save. Empty
│      is correct only when the embedded model was the only one enabled
│    → reset embedded_llm.* to zero while PRESERVING the auto_unload pointers
│      (an idle budget is an operator setting, not install state)
│    → Config.SyncEmbeddedLLMProvider(0) drops llm.openai_compatible.embedded,
│      so the composite id stops resolving → persist
└─ deletion errors are joined and returned AFTER the config was cleared

Every deletion goes through one gated primitive that refuses any path the Layout
does not own, so Remove can never touch the flat embedding-model files sharing
<agentDir>/models, nor anything under <toolsDir>/bin. The config is cleared even
when a deletion failed: a leftover directory wastes disk and a retry reclaims it,
while a config that still claims an install whose files are gone points the
router at a dead provider. Remove is idempotent — removing something that was
never installed is a no-op that still clears the config.
```

### Load / serve / unload

```
             ┌──────────────┐  Install   ┌────────────┐
             │ not_installed│───────────▶│ installed  │◀────────────┐
             └──────────────┘            └─────┬──────┘             │
                     ▲                         │ Load / EnsureLoaded│
              Remove │                         ▼                    │
                     │                   ┌───────────┐   /v1/models │
                     │                   │  loading  │──────────────┤
                     │                   └─────┬─────┘   answered   │
                     │        spawn failure    │                    │
                     │             ┌───────────┴───┐                │
                     │             ▼               ▼                │
                     │       ┌─────────┐     ┌──────────┐  Unload / │
                     └───────│  error  │     │  loaded  │───idle ───┘
                             └─────────┘     └────┬─────┘  (via unloading)
                                                  │ process died
                                                  ▼
                                            ┌─────────┐
                                            │  error  │ + embedded_llm:state
                                            └─────────┘

Load:  single-instance gate (ctx-aware; a Load against a loaded model is a no-op
       that only marks activity)
       → a process left over from a stop that did not take is discarded first, so
         a second llama-server can never be stacked on the first
       → read manifest.json (no manifest → not_installed; nothing is spawned)
       → the persisted port is re-checked and walked upward to the first
         bindable one (EnsurePort → SearchFreePort); a move is written back to
         embedded_llm.port and the provider record is regenerated from it
       → LaunchSpec from the manifest + the pure policy in resolve.go, then
         Validate: loopback host only, usable port, -c inside the resolved tiers
       → State loading, then spawn
            llama-server -m <gguf> --host 127.0.0.1 --port <effective> -ngl N
                         -fa on -c CTX --temp 1.0 --top-p 0.95 --top-k 20 --jinja
                         --no-webui [--mmproj <file>] [--image-max-tokens N]
         with the platform library path (LD_LIBRARY_PATH / DYLD_LIBRARY_PATH /
         PATH) headed by the binary's own directory, that directory as cwd, and
         the context DETACHED from the caller's — the server outlives the Load
         that started it
       → stdout/stderr drained into slog (debug) and into a 24-line tail
       → readiness = GET /v1/models answering 200 with a NON-EMPTY model list,
         polled every ReadyPollInterval and bounded by ReadyTimeout, by ctx, and
         by the death of the process. Never /health, which answers before the
         weights are in memory
       → on ready: State loaded AND lastActivity := now — the load's own duration
         never eats the idle budget
       → on any failure: the half-started process is discarded as abandoned (so
         its exit is not reported a second time as a crash) and the state becomes
         error, carrying the tail

Serve: the ensure-loaded RoundTripper on the entry (installed on EVERY router —
       the cached one because every production conversion goes through
       toBuilderConfigLocked, which injects the BuilderConfig.EmbeddedLLM seam,
       and a per-session one because buildRouter falls back to the builder-level
       default SetEmbeddedLLM installed — see Request path under Invariants):
       → join the ONE in-flight load or start it — N parallel cold requests are
         one weight load, not N — and wait for it, bounded by
         DefaultLoadWaitTimeout; exceeding it is ErrLoadWaitTimeout, never a hang
       → the load runs on a context derived from nothing the caller owns, so a
         request that runs out of patience (timeouts.llmRequestTimeout, 10 min,
         is SHORTER than the 15 min ready budget) stops waiting without
         cancelling the load: the next request joins it instead of starting over
       → only then is the request sent; a failed load is reported as itself,
         instead of a "connection refused" from a socket nothing listens on
       → the request's own budget is armed HERE, not by http.Client.Timeout:
         EnsureLoadedClient zeroes the clone's Timeout and hands it to the
         transport, which applies it once the model is resident and releases it
         when the body closes — so a cold load never shortens the generation it
         precedes
       → MarkActivity when the response COMPLETES (body read to EOF or closed),
         so a long streamed generation counts as activity, not as idle time

Serve, short-budget callers: the transport gates inside http.Client.Do, which is
       too late for a caller that armed its deadline first. The three one-shot
       service requests (prompt optimization, commit message, session title)
       therefore run ensureEmbeddedReadyForLLMRequest — or the manager's
       serviceLLMGate — BEFORE creating their serviceLLMRequestTimeout context
       (default 120 s, far shorter than a cold load), and skip the request
       entirely when the gate fails. Every other provider returns from the gate
       at once.

Unload: State unloading → SIGTERM (straight to Kill on a platform with no
        graceful child signal) → wait StopTimeout → Kill → wait the kill bound →
        State installed (on disk, not resident). A stop that does not take is
        reported as error and KEEPS the process handle, so the next attempt
        targets the same process instead of spawning a second one.
Idle:   auto_unload.minutes (default 60) without activity → the same stop path.
        auto_unload.enabled = false leaves the timer unarmed entirely.

`unloading` spans the graceful window between the termination signal and the
observed exit. A stop that does not take ends in `error` — never in `installed` —
and `error` is not terminal: the next Load discards the leftover process and
tries again.
Crash:  the supervise goroutine drains the output, waits for the exit and reports
        it — an expected exit → installed, an unexpected one (a clean status 0
        included) → error + embedded_llm:state carrying the tail.
```

### Reasoning effort and the family resolution

The subsystem sets no reasoning effort — no such flag exists in `LaunchSpec.Args` — so the value travels the ordinary path. Three things outside `core/embeddedllm/` make that path work for this checkpoint:

```
picker / HandleOptions.ReasoningEffort / profile sampling.reasoning_effort
        |
        v
ChatRequest.ReasoningEffort --> Router.prepareRequest
        |                         |- req.ModelFamily = meta.Family   (resolved once)
        |                         \- applyDefaultSampling(req, meta) <- authoritative caps
        v
OpenAIProvider.buildChatParams
        |  switch req.ModelFamily {
        |    case "qwen": applyQwenReasoning(params, model, effort, p.reasoningWire)
        |  }                       |
        |                          |- ReasoningWireVendorDefault      -> top-level
        |                          |                                    enable_thinking /
        |                          |                                    reasoning_effort
        |                          \- ReasoningWireChatTemplateKwargs -> chat_template_kwargs:
        |                               (the embedded entry)             {enable_thinking: <bool>,
        |                                                                 reasoning_effort: medium|low}
        v
pinned llama-server (reads enable_thinking ONLY from chat_template_kwargs)
```

- **Family.** `DetectFamily("Bonsai 2 27B")` finds no family token in the name, so the family comes from the sp4rk catalog entry for the checkpoint (keyed `bonsai 2 27b` and `prism-ml/ternary-bonsai-2-27b`, `Family "qwen"`). Family keys BOTH ends of the path: `llm.ModelReasoningOptions(family, model)` decides whether `collectAllModels` reports a `Reasoning` block at all (`[xhigh, medium, low, Off]`, default `xhigh` — no block, no combobox in either picker), and the provider's per-family switch decides whether an effort is encoded at all. Under family `default` both ends were dead: no options rendered, and an effort that was set silently dropped.
- **Family inheritance.** The override `SyncEmbeddedProvider` writes pins `context_window` only, and a partial override inherits its unset fields from the tiers below (`enrichPartialWith`) — `Family` among them. An override that *does* name a family stays authoritative, so an operator's `family:` tweak in `llm.models` outranks the catalog and survives every sync.
- **Wire.** `providerEntryFromConfig` sets `llm.ReasoningWireChatTemplateKwargs` on this entry alone, inside the same `embedded.guards(name)` branch as the ensure-loaded transport. The fork's `oaicompat_chat_params_parse` never reads a top-level `enable_thinking`, so the vendor spelling made `Off` a silent no-op; a top-level `reasoning_effort` *is* read, which is why only the native levels could ever have arrived. `enable_thinking` is emitted as a JSON **boolean** — the server dumps each kwarg and compares it against the strings `true`/`false`, and a JSON *string* dumps with quotes and makes it throw. `"Off"`/`"On"` are never forwarded as `reasoning_effort` (the Bonsai template raises on anything outside `xhigh`/`medium`/`low`).

The catalog entry also declares `Temperature: true` authoritatively, which lifts the model out of `applyDefaultSampling`'s "the registry cannot vouch for this" early return: deterministic calls (routing, compaction, summarization, session title) now get the qwen floor `0.6` instead of the server's own `--temp 1.0`, and an enabled Model Profile's sampling preset now applies through the router's `SamplingFunc` — which is what makes the suggested `qwen3.8-27b` profile (it pins `reasoning_effort: "medium"`) take effect. With the profile off, the injected preset is the qwen vendor matrix (`temperature 1.0`, `top_p 0.95`, `top_k 20`) — the same values the server is launched with. The catalog's `ContextWindow 262144` never wins over the RAM tier: precedence stays config override > probe > static catalog.

**Known ordering hazard.** `SetRuntimeMetadata` pre-fills `Family` from the model name at write time, and the observed-runtime tier (1.5) sits above the catalog *and* is the enrichment baseline for a tier-1 partial override — so a runtime entry for this model would resolve the family back to `default` and take the effort control away again. No entry is expected: the LM Studio-native endpoint the lazy probe tries first is absent from the fork's route table, and none of `max_model_len` / `max_context_length` / `context_length` (the three fields `probeOpenAIModels` reads) is present in the pinned runtime's server library. The durable fix is sp4rk-side (do not pre-fill `Family` in the runtime/cached setters); c0wrk must not paper over it by writing a family it does not own.

### Port allocation

```
install:  ask the OS for a free loopback port (ephemeralLoopbackPort: listen on
          127.0.0.1:0, read the assignment back, close) → persist in
          manifest.json AND embedded_llm.port. An explicit InstallOptions.Port
          bypasses allocation entirely.
load:     the persisted port is a PREFERENCE that is re-checked immediately
          before every spawn. Production wires Server.EnsurePort to
          backend.embeddedEnsurePort → embeddedllm.SearchFreePort, which
          bind-probes the preferred port and walks UPWARD one port at a time
          until it finds a bindable one (port.go; a preference below
          MinLoopbackPort starts the scan there, exhaustion of the range is
          ErrNoFreePort). A move is written back to embedded_llm.port and the
          backend-owned provider record is regenerated from it, so base_url keeps
          matching the socket the server bound; manifest.json keeps the port the
          install ALLOCATED, which is the preferred one the next load re-scans
          from — a temporarily taken port is reclaimed instead of drifting
          upward forever.
request:  the router builds each request URL from the provider base_url, so a
          move would otherwise aim the in-flight request at a socket this process
          does not own. The ensure-loaded transport redirects to the supervisor's
          live port (the optional PortSource capability, transport.go).
config:   the provider base URL is derived from the persisted port
          (http://127.0.0.1:<port>/v1) and regenerated whenever it changes
```

The pre-spawn re-check is what makes the FIRST readiness probe safe. That probe
runs immediately after the spawn, so a foreign listener on the persisted port —
including a `llama-server` a crashed run left behind — would answer it, and the
supervisor would report `loaded` for a model it never started while every request
carried the prompt to that unrelated process.

The probe releases the port before the server binds it (there is no fd-passing
path to `llama-server`), so a racing process can still take it in between; the
readiness probe is the backstop that turns a lost race into a reported failure
rather than a false `loaded`. A config write that fails is logged and does NOT
fail the load: the server binds the free port either way and the transport still
redirects, so a stale `base_url` on disk is an administrative problem, not an
unusable model.

### Startup / shutdown

```
startup:  desktop/startup_phases.go initEmbeddedLLM → Lifecycle().InitEmbeddedLLM
          → build the layout/supervisor/installer (inert) → read manifest.json →
          cache the install record → SetInstalled(true) with its transition event
          muted → apply the auto-unload policy from config → emit exactly ONE
          embedded_llm:state snapshot.
          NO download, NO network, NO probe, NO auto-load — startup never depends
          on the network and never blocks on the model. The restore is also the
          lazy path of the first GetEmbeddedLLMStatus, so an RPC that arrives
          before the phase still reports the truth.
shutdown: desktop/startup_phases.go stopEmbeddedLLM → Lifecycle().StopEmbeddedLLM
          → Server.Stop (bounded) — the server if it is running, a no-op when the
          subsystem was never built. A failure is logged, never fatal.
```

### Settings block (UI states)

```
mount:      EmbeddedLLMSettings → subscribeEmbeddedLLMEvents (ONE shared,
            refcounted Wails subscription) → refreshEmbeddedLLMStatus
            (GetEmbeddedLLMStatus). Mounting is read-only: it NEVER installs,
            NEVER loads and NEVER probes — all three are explicit clicks.

not installed → ONE Install button and nothing else: no Remove, no Load, no
            auto-unload control, no install record.
            click → InstallEmbeddedLLM settles with the GATES only.
            Rejected → the actionable refusal is rendered inline (role="alert")
            and NOT as a toast, because a synchronous refusal still has a
            caller to report to; nothing was downloaded.
            Resolved → beginInstall() flips the block to the installing
            surface before the first progress event arrives.

installing → one row PER COMPONENT, in install order (runtime, cudart when this
            machine gets one, model, mmproj), each with its own icon / stage /
            bytes / bar / percent — the artifacts are never aggregated into one
            bar. A byte-less stage (verifying, extracting, signing) renders an
            indeterminate pulse instead of a percentage nobody knows; `done`
            renders Done. A component that never reported gets NO row, so a
            platform that fetches no cudart shows none. Closing the settings
            dialog does not interrupt the run (it lives on the app context).

installed  → the INFORMATIONAL install record: the packing the resolver
            actually chose (PQ2_0 | PTQ1_0), the EFFECTIVE backend, the
            RAM-tiered context, the persisted port and the derived base URL +
            composite model id. Below it Load (or Unload while resident, with
            the blocking load's own spinner), Remove behind a confirmation
            dialog, and the auto-unload toggle with its minutes field
            (default 60; committed on blur/Enter; an out-of-range value reverts
            locally instead of paying a round trip for a guaranteed refusal).
            While loaded with the idle timer armed, the remaining budget is
            shown.

transitions → every embedded_llm:state event re-reads the authoritative
            snapshot instead of patching it (the payload carries no
            `installing`, no `auto_unload_enabled` and no provider identity),
            so the block can never render a half-merged state. A snapshot that
            reports no live install retires the progress rows with it, and a
            failed READ keeps the previous snapshot and paints no action error.
            A read whose `installed` differs from the previous snapshot also
            drops the shared model cache (`invalidateConfigCache`) — that is how
            an install which finished in the background reaches the chat
            toolbar's picker, whose list is served from that cache. Load/unload
            transitions do NOT invalidate: an unloaded model stays selectable
            (the first request loads it), so the list is unchanged.
```

The block is the model's ONLY settings surface: the generated `openai_compatible.embedded` record renders no accordion of its own and travels in no settings draft, so nothing in the LLM tab can edit a value the backend regenerates. Its model still appears in the Default Model picker, and `embedded` is a reserved name in the add-provider form even while the model is not installed. See [llm-providers.md](llm-providers.md#backend-owned-embedded-provider).

**Placement.** The block LEADS the LLM tab's provider section: Default Model field → **Embedded LLM** → "+ Add compatible provider" → the fixed / OpenAI-compatible / Anthropic-compatible accordions. Running fully local is the primary offering, so the remote-endpoint escape hatch sits below it, immediately above the accordions an added provider turns into.

**In both pickers.** While installed, the model is listed in the Settings default-model picker AND the chat toolbar's session-model picker, as provider **Embedded** / model **Bonsai 2 27B**, and its provider group is always the FIRST one — see the `ModelPickerMenu.groupByProvider` hoist in [Key Files](#key-files) and the invariants below.

### Status-bar indicator (UI states)

The `StatusBar` block `EmbeddedModelStatus` is the same store's second surface:
a compact, always-available readout of a run that outlives the Settings dialog
and of a residency that occupies gigabytes of RAM. It is mounted between the
vector-index block and the process-memory indicator and is NOT gated on
No Project mode — the local model is process-wide, not per-project.

```
mount:      subscribeEmbeddedLLMEvents (the ONE shared, refcounted Wails
            subscription, so mounting next to the Settings block never applies
            an event twice) → refreshEmbeddedLLMStatus, retried on
            `backend:ready` because the startup snapshot event is emitted in
            desktop startup phase 5 and can precede the first paint. Mounting
            installs nothing, loads nothing, unloads nothing and probes nothing.

nothing to say → the block renders NOTHING, its own leading separator included,
            so a hidden indicator never leaves a stray separator in the bar.
            "Nothing to say" is exactly: no snapshot yet, a snapshot whose
            subsystem is not available, not installed with no pending failure,
            and installed-but-stopped (an idle install is not an event — the
            Settings block is where it is acted on).

installing → the ACTIVE artifact only, in install order: the first reported
            component that has not reached `done`. Its own label, its own
            bytes and its own percent — NEVER an aggregate, because the
            components differ by two orders of magnitude (a ~100 MB runtime
            against 6.7 GiB of weights), so a summed fraction would jump
            backwards when the weights start. A byte-less stage (verifying,
            extracting, signing) and the window before the first progress event
            render an indeterminate pulse instead of a percentage nobody knows.
            A live run outranks every snapshot field: the store raises
            `installing` from the progress event itself, which can precede a
            stale snapshot, and the backend refuses a load during an install.

loading    → an indeterminate bar plus "Loading model…": `LoadEmbeddedLLM`
            reports a state, never a fraction, so there is nothing to compute.

loaded     → the residency indicator: the bare model name (`model_name`, falling
            back to the composite's bare part) beside a success-colored icon,
            with the whole install identity in the tooltip (packing/effective
            backend, RAM-tiered context, base URL, pid, and the idle policy —
            "auto-unloads after N min idle" or "stays resident until unloaded").
            It stays across snapshot refreshes, INCLUDING one whose
            `idle_remaining_seconds` reached 0: `loaded` is the authority, not a
            countdown this block does not own. It disappears on the unload
            transition, whether the user clicked Unload or the idle timer fired.
            The remaining-idle number is deliberately not rendered: the snapshot
            only refreshes on a transition, so a ticking readout would be wrong
            within a second.

error      → an explicit hint carrying the backend's message (truncated in the
            bar, whole in the tooltip, which also points at Settings → LLM).
            Reached both from `state: "error"` and from a not-installed snapshot
            whose `error` is non-empty — a failed install is otherwise invisible
            outside the Settings dialog.
```

## Invariants

**Hardware probe and resolution:**

- Total RAM is a hard input: when it cannot be read the probe returns `ErrRAMUnknown` rather than guessing, because the 16 GiB gate depends on it.
- The install is refused below 16 GiB with the typed `ErrInsufficientRAM`, and that refusal is the FIRST check in `Resolve` — an undersized machine plans no assets and downloads nothing. There is deliberately no reduced-experience band between 8 and 16 GiB.
- `Resolve` is a pure function of `(platform, backend, ramGiB)`: no I/O, no probing, no clock. That is what keeps the whole platform × backend × RAM matrix table-testable.
- The accelerator ladder is fixed (`nvidia-smi` → `nvcc` → ROCm tools → `vulkaninfo` → Metal on darwin/arm64 → CPU) and first hit wins. Every external probe is bounded by a 2 s budget, so a wedged driver cannot stall installation; an absent probe helper is a normal outcome, not an error.
- A detected CUDA version older than the oldest pinned asset tag is not a hit: the ladder keeps looking instead of provisioning an archive the driver cannot load.
- Resolution degrades an unsupported backend rather than refusing it, and a CUDA tag with no archive for the platform clamps DOWN to the nearest older pinned tag — never up, because a binary built against a newer toolkit will not load on an older driver.
- `PQ2_0` is the default packing; `PTQ1_0` is selected for Vulkan alone, the one backend without `PQ2_0` kernels. Packing is independent of RAM.
- The context size is always an explicit positive value drawn from the five RAM tiers. `Resolve` never emits `0` ("let the server choose") and never the model's full `262144`-token training context: both are memory-unaware and OOM a constrained machine once `-ngl` offloads the KV cache.
- The tier comparison floors RAM to a whole GiB, matching the integer arithmetic the tiers were derived with, so a Linux machine reporting slightly under its nominal size lands in the tier its real capacity belongs to.
- The vision projector is part of every resolved set, for every backend and packing.
- The manifest and the informational Settings label record `Resolution.Backend` (the effective backend), never the raw probed value.

**Supply chain (ASI04):**

- Every artifact version is a compile-time pin in `core/embeddedllm/registry.go`; the subsystem performs no upstream version queries and no automatic updates. A version advances only when a developer raises the pin after a CVE review.
- Both pins are **immutable refs**: `RuntimeTag` is the fork release tag and `ModelRevision` is a Hugging Face commit SHA, never a branch — so a rebuild cannot silently re-point at whatever upstream published last. Runtime checksums are the GitHub REST per-asset `digest`; model checksums are the HF LFS OID, which *is* the SHA256.
- `ValidateRegistry()` self-checks the pin tables and is the guard that turns a hand-edited table into a failure instead of a supply-chain hole: every asset must carry an HTTPS URL under its pinned ref, a 64-character lowercase hex digest, a positive exact size and a plain file name; the component must match its table; a `cudart` may never be pinned without its runtime; and every supported platform must pin a CPU fallback.
- A `(platform, backend, packing)` combination the pin does not cover fails closed with `ErrArtifactNotPinned` — never a neighbouring platform's or backend's bytes. The pinned release has **no** `windows-amd64` + `cuda-12.8` archive (and no matching `cudart`) although linux does ship `bin-linux-cuda-12.8-x64`; the registry refuses that pair and `Resolve` clamps the CUDA tag down to a pinned one. `windows-arm64` is out of scope entirely (D9), so it is not even a supported platform key.
- Every downloaded byte is SHA256-verified before use, **fail-closed**: an empty or missing checksum for the platform refuses the install rather than skipping verification, and a mismatch deletes the partial file and returns an error. Verified bytes are never accepted from an unverified path.
- `manifest.json` is written only after every component has been verified; a failed verification leaves no manifest and registers no provider.
- The runtime is a pinned fork release (`prism-b10709-9a9394a`), never upstream llama.cpp: stock rejects `PQ2_0`/`PTQ1_0` and silently produces garbage on `Q2_0`.

**Download:**

- Both pinned hosts honour HTTP `Range` — **verified experimentally**, so resume is the normal path and the restart fallback is a defence rather than the design. Hugging Face `resolve/<revision>/<file>` answers `302` → `206` with `accept-ranges: bytes` and an exact `content-range` (the `302` also carries `x-linked-size` and `x-linked-etag`, the latter being the SHA256 LFS OID); GitHub `releases/download/<tag>/<asset>` answers `302` → `206` the same way. Both answer `416` for a range beyond EOF. `net/http` copies the `Range` header across the redirect (only `Authorization`, `Cookie`, `Cookie2` and `WWW-Authenticate` are domain-gated), so a single ranged request resumes.
- A transfer is never silently partial. Two explicit fallbacks exist, both logged and reported on `Result.Restarted` + `Result.RestartReason`: a host that answers `200` to a ranged request ignored `Range`, so the partial is truncated and the full body is written from byte 0 — appending a complete object onto a partial would silently corrupt the artifact; a host that answers `416` cannot be reconciled with the partial, so the partial is discarded and the download restarts from byte 0. Restarts are bounded by `maxTransferAttempts` (2): a host that keeps rejecting ranges is an error, not a retry loop.
- A partial transfer lives at `<destination>.part`. The destination path only ever holds bytes that passed SHA256 verification, because verification happens *before* promotion (`os.Rename`) — and the partial file is not even created until the server has answered, so a failed request leaves no litter. A complete-but-unpromoted `.part` (a crash between the last byte and the rename) is verified and promoted with no request at all.
- Fail-closed at every gate: an artifact whose `SHA256` is empty, malformed or not 64 lowercase hex characters is refused before any I/O (`ErrMissingChecksum`), a digest mismatch deletes the partial and returns `ErrChecksumMismatch`, and a non-HTTPS URL is refused outright. The resumed prefix is fed into the same hasher as the streamed bytes, so one digest covers the whole artifact without a second multi-gigabyte read.
- A transport failure or cancellation **keeps** the partial so the next call resumes (`ErrIncompleteTransfer`, also wrapping `context.Canceled` when the caller cancelled); only a checksum mismatch, an oversized partial or an unusable one deletes it.
- The pin's exact `SizeBytes` is the transfer ceiling — there is deliberately no fixed byte cap. A server offering more than the pin yields `ErrArtifactTooLarge` and the partial is deleted, because it cannot be resumed.
- Progress is reported per component (`runtime`, `cudart`, `model`, `mmproj`) as `(done, total)` through `ProgressFunc`: one immediate unthrottled callback at the resume offset, throttled callbacks during the transfer (`DefaultProgressInterval`, ~100 ms), and one final unthrottled callback that always reports `done == total` even when the throttle swallowed every intermediate update.
- Cancellation is honoured through the request context at every stage. The default transfer client has **no** overall `Client.Timeout` — a 6.7 GiB download may legitimately take hours — so liveness is bounded by the dial and response-header timeouts plus `ctx`.
- The disk guard is sized to the actual artifact, not a fixed constant: `SizeBytes + DefaultDiskHeadroom` (2 GiB) per artifact, and `RequiredFreeBytes(TotalBytes(set))` for the whole set before the first byte is fetched. Guard measurement failure is best-effort and non-fatal, matching `toolmanager.checkDiskSpace`: a genuinely full disk still fails safely, since ENOSPC leaves a resumable partial and the digest gate still refuses unverified bytes.
- The embedded downloader is separate from the tool-manager's: `maxDownloadBytes` (1 GiB), `maxExtractEntryBytes` (512 MiB) and the 5-minute HTTP client timeout in `core/toolmanager` bound startup-critical archives and are incompatible with a 6.7 GiB user-initiated download. The tool-manager's *pattern* (pins, fail-closed SHA256, secure-bytes-before-destroy) is reused; its code and registry are not.

**Storage and agent isolation (ASI05):**

- Artifacts live in `~/.c0wrk/runtimes/llama-<tag>-<backend>/` and `~/.c0wrk/models/bonsai-2-27b/`. No embedded-LLM file is ever written under `<toolsDir>/bin`, which `Manager.PrependToPATH()` exposes to the agent's `bash_exec`.
- The weights occupy a dedicated subdirectory of `<agentDir>/models`, which already holds the flat embedding-model files resolved by `desktop/startup.go` `resolveModelPath`; Remove deletes only the `bonsai-2-27b/` subtree and never the embedding model.
- Every path is constructed and containment-checked through the centralized path API (`backend/config/paths.go`, `sp4rk/pathutil`, `core/pathsegments.go`); inline `strings.HasPrefix`/`filepath.Rel` containment is not used. The two roots are injected into `Layout` by the caller from `config.RuntimesDir`/`config.EmbeddedModelDir`, so the subsystem never re-derives `<agentDir>/...`; `NewLayout` refuses empty, relative, identical or nested roots, which is what keeps one tree's cleanup from reaching the other.
- Every derived path goes through one containment-checked join and every deletion through one gated primitive (`removeOwned`, guarded by `Layout.Owns` over `pathutil.IsWithinPath`). `os.RemoveAll` appears exactly once in the subsystem, inside that primitive, so the isolation invariant is enforced in a single place instead of being repeated per call site.
- `Remove` deletes every `llama-*` tree under the runtime root — not only the manifest's backend — plus the archive staging area, so an interrupted install's `.staging`/`.old` leftovers and a previous backend's tree are reclaimed by the user-visible action.
- Extraction is guarded independently of the download: each entry is containment-checked (a traversal entry is skipped, an escaping symlink is refused outright), and per-entry / per-archive decompressed caps (2 GiB / 8 GiB) bound a checksum-valid-but-malicious archive. Those caps are deliberately larger than `core/toolmanager`'s 512 MiB, which sits below a single CUDA runtime library in these archives.
- Stale artifacts are destroyed only after the replacement bytes are secured **and proven**: the runtime is extracted into `.staging`, signed and smoke-tested there, and only then promoted by retiring the previous tree to `.old`, renaming, and deleting `.old` — with a rollback rename when the promotion itself fails.

**Install and remove orchestration:**

- Install's gates run in a fixed order and each one is a gate: probe → resolve (RAM first) → ensure roots → whole-set disk guard → port → runtime phase → macOS provisioning → promote → weights phase → manifest → config sink. Nothing is downloaded before the RAM and disk gates have passed, and no directory is created before the RAM gate has.
- The runtime is proven **before** the weights are fetched: a runtime that cannot execute (Gatekeeper, a missing GPU library) fails the install in seconds instead of after a multi-gigabyte download.
- Verification is enforced twice, and neither pass re-hashes a multi-gigabyte file: `Downloader.Download` never promotes unverified bytes, and `Install` independently refuses a result that is not marked verified or whose digest differs from the pin. Fail-closed is the only acceptable reading — "probably fine" is how an unverified binary ends up executed.
- `manifest.json` is written once, atomically (sibling temp + rename), and only after every component verified and the runtime ran. A crash mid-write leaves the previous manifest intact rather than a truncated one, and a failed install leaves no manifest at all.
- The manifest records the *effective* backend, the pinned `RuntimeTag`, the resolved context tier and the verified digest of every downloaded component keyed by component name — enough for startup to restore state from disk alone.
- Install and Remove both refuse without a `ConfigSink`: bytes that cannot be registered are bytes nobody can use, and a deletion whose provider entry survives points the router at nothing. A sink failure *after* the manifest was written keeps the manifest, because it is accurate — the retry re-downloads nothing and rewrites the same record.
- A failed install keeps the bytes that DID verify (they are cache hits for the next attempt) and writes no manifest, which is what makes a multi-gigabyte install resumable rather than restartable.
- Progress is per component and per stage. Each component reports `downloading` first and exactly one `done`, always against its own pinned byte total; the runtime group's `done` is deferred until the tree is provisioned, so a component's stream ends when it is usable rather than when its bytes landed.
- macOS provisioning is the only platform-specific branch, keyed off `Installer.HostOS` (the probe's goos in production). A missing `xattr`/`codesign` helper is a warning, not a failure — the smoke test is the authority on whether the runtime runs — while a failing smoke test is fatal and its error names the exact Gatekeeper fix (`xattr -dr com.apple.quarantine <dir>`, or System Settings → Privacy & Security → "Allow Anyway").
- `Remove` stops the server before deleting anything (a live process cannot be deleted on Windows and would keep serving unlinked inodes on Unix) and clears the config last, even when a deletion failed. It is idempotent: removing an install that was never there is a no-op that still clears the config.
- Every external effect is an injectable field on `Installer` (`Downloader`, `Probe`, `RunCommand`, `AllocatePort`, `Stop`, `Now`, `HostOS`), so the whole flow — including the darwin-only branch — is tested on every CI platform against kilobyte artifacts served by an in-test HTTPS server, exercising the production download, verification, extraction and provisioning code rather than a mock of it.

**Server supervision:**

- The server binds strictly `127.0.0.1` — `LaunchSpec.Validate` refuses any other host — and its built-in Web UI is disabled with `--no-webui` on every command line. The server's own agent surface (`--agent`/`-ag`, `--tools`, `--cors-origins`, the MCP proxy) is never enabled.
- Readiness is a `200` from `/v1/models` **with a non-empty model list**, never `/health`: a `200` whose list is empty keeps polling, because the fork's server opens its socket and answers both routes long before the weights are in memory.
- The ready wait watches the process as well as the clock, so a server that dies during the weight load is reported in milliseconds instead of after the whole ready budget.
- Load is idempotent and single-instance: a ctx-aware gate guarantees at most one `llama-server` per install, a Load against a serving model is a no-op that only marks activity, and a process left over from a stop that did not take is discarded before another one is spawned.
- The spawned process is deliberately NOT bound to the caller's context (`context.WithoutCancel`): a Load's context is usually one RPC, and the server must outlive it. Its lifetime is owned by `Unload`/`Stop`, the idle timer and app shutdown.
- The child's environment heads the platform's dynamic-library search path (`LD_LIBRARY_PATH` / `DYLD_LIBRARY_PATH` / `PATH`) with the runtime's own binary directory, and the separator and variable are derived from the target `goos` rather than from the host, so the policy stays a pure, table-testable function. Nothing is installed into a system location.
- The child's stdout and stderr are drained into `slog` (debug, tagged with the stream and the pid) and only then is `Wait` called — the ordering `os/exec` requires for piped output. A bounded 24-line tail of that output is attached to every load failure and every crash report, because "the process exited" is not a diagnosis and "ggml_cuda_init: found 0 devices" is.
- Unload means terminating the process, which deterministically returns RAM/VRAM. It is graceful first (SIGTERM, then Kill after `StopTimeout`); a platform with no graceful child signal falls straight through to the kill.
- A stop that did not take is an error, keeps the process handle and never reports `installed`: claiming "not resident" while a process holds gigabytes would misreport the machine, and dropping the handle would let the next Load start a second server.
- An unexpected exit — a clean status 0 included — transitions the state to `error` and emits `embedded_llm:state`; only an exit the supervisor asked for reports `installed`. A dead server is never reported as `loaded`.
- A failed Load marks its own process *abandoned* before discarding it, so one death is never reported twice (once as a load failure and once as a crash); state transitions that change nothing emit nothing.
- The state of a live process belongs to the supervisor: `SetInstalled` is refused with `ErrServerBusy` while one is running, so neither startup nor Remove can silently misreport a running model.
- The launch specification is validated at the last gate before exec: a missing binary or model, a non-loopback host, an unusable port, negative `-ngl`/`--image-max-tokens`, or a context outside `1..contextTierTop` is refused with `ErrLaunchSpecInvalid` rather than spawned.
- No hardware probe runs at load time: `-ngl` and `--image-max-tokens` are re-derived from the manifest's **effective** backend through the same pure policy `Resolve` uses, and `-c` is the tier frozen in the manifest, so a load cannot fail because a driver query wedged.
- Startup flags always come from the resolution: the context size is a RAM tier, never unspecified and never the model's own full training context.
- A missing vision projector degrades to text-only serving (a warning, and no `--mmproj`) instead of refusing to load; a missing model file does refuse, wrapped in `ErrNotInstalled` with a reinstall hint.
- Reasoning effort is not set by the subsystem: no reasoning flag exists in `LaunchSpec.Args`, and the effort arrives through the ordinary mechanism (`HandleOptions.ReasoningEffort` / the picker / a Model Profile's `sampling.reasoning_effort`). That mechanism only works for this model because of three things outside the subsystem — the sp4rk catalog entry for the checkpoint (`Family "qwen"` + authoritative capabilities), the registry's Family inheritance for a partial override, and the `chat_template_kwargs` reasoning wire this entry alone carries. See [Reasoning effort and the family resolution](#reasoning-effort-and-the-family-resolution) and [ADR-066](../decisions/066-embedded-llm-runtime.md) D7.

**Idle budget:**

- `lastActivity` is set when a request completes and re-set immediately after a load completes, so weight-load time never consumes the idle budget. The load-completion stamp is what ARMS the timer: nothing arms it earlier, which is what makes a ten-minute load arrive with the whole budget intact.
- `auto_unload.minutes` (default 60) without activity stops the process through the same path as an explicit Unload; `auto_unload.enabled = false` leaves the timer unarmed entirely and the model stays resident until an explicit Unload, a Remove or shutdown.
- The timer only ever runs against a `loaded` model: every other transition disarms it, and a stamp made while the model is not resident records the time but arms nothing — so the next load starts a budget of its own.
- The stamp is taken when a response COMPLETES, not when a request is sent, so a generation longer than the remaining budget cannot unload the model mid-answer.
- A policy change takes effect immediately and re-arms against the EXISTING stamp: shortening the budget grants no fresh one (a budget already spent unloads at once) and disabling disarms the timer without discarding the stamp.
- The live policy is owned by the idle tracker, not by the `Server.AutoUnload` field, which is a construction-time input seeded on first use — so a runtime `SetAutoUnload` and a concurrent `IdleRemaining` read cannot race.
- A non-positive budget is clamped to the default rather than meaning "unload immediately", so re-enabling the timer can never activate a dead budget.
- The expiry callback re-checks the remaining budget under the lock, so an activity stamp that raced the timer wins and the stale callback does nothing.

**Request path (ensure-loaded transport):**

- The embedded provider entry is the ONLY dial path the transport is installed on. It is attached through `llm.ProviderEntry.HTTPClient` in `core/builder.go` `providerEntryFromConfig`, guarded by `BuilderConfig.EmbeddedLLM.ProviderName`; the Fetch Models listing and the lazy context-window probe are not wrapped, and neither is any other provider.
- The same guard is the ONLY selector of the reasoning wire: that entry carries `llm.ReasoningWireChatTemplateKwargs` and every other one keeps the vendor-default top-level spelling. The wire follows the supervisor, never the name or the URL shape — a user provider pointed at the same loopback is not opted in, because c0wrk does not know which binary answers there — and no loader wired means no wire selected either. See [Reasoning effort and the family resolution](#reasoning-effort-and-the-family-resolution).
- The pin resolver runs FIRST and the transport only decorates its answer, because `llmtls` needs a concrete `*http.Transport` to hold a `tls.Config`: wrapping first would make the pin resolver discard the wrapper and replace it with a default-transport clone. The generated embedded record carries no pin (plain HTTP on loopback), so in practice the transport wraps the shared LLM client — but the order holds if that ever changes.
- The client handed to the entry is always a CLONE of the shared LLM client (or of the pin resolver's clone of it), so `timeouts.llmRequestTimeout` survives. Attaching a client without it would shadow `RouterConfig.HTTPClient` and cap inference at the 30 s web-fetch proxy budget — the invariant [llm-providers.md](llm-providers.md) states for the pin path, and the reason `EnsureLoadedClient` takes the shared client at all.
- That budget is MOVED, not dropped. `http.Client.Timeout` covers the whole exchange, gate included, so a client that both waits for a cold load and carries the request budget would hand the generation whatever is left after the weights land — and with the default 600 s budget against a 15 min ready allowance a legitimately slow load would consume it entirely. `EnsureLoadedClient` therefore zeroes the clone's `Timeout` and passes it to the transport, which arms it on the request only AFTER the model is resident. The deadline is released when the response body is closed (never when `RoundTrip` returns), because a streamed generation is read long after the headers arrive.
- Concurrent requests coalesce into ONE load: the transport keeps a single in-flight call and every waiter joins it, so a burst of cold requests is one weight load rather than N queue entries on the supervisor's gate.
- The wait is bounded, and the bound is longer than the supervisor's own ready budget, so a wedged load is always diagnosed by `Load` (with the server's log tail) rather than reported as a transport timeout. An expired transport budget is `ErrLoadWaitTimeout`.
- The load is NOT bound to a request context. `timeouts.llmRequestTimeout` (default 600 s) is shorter than `DefaultReadyTimeout` (15 min), so a caller-side deadline during a cold load would otherwise cancel it and discard the half-loaded weights — and the next request would start from zero, so the model would never become resident. A cancelled request stops waiting (with its own context error) while the detached load runs to completion for whoever asks next.
- A load failure reaches the caller as itself and is not cached: the next request loads again. The request is never sent to a server that is not listening.
- Activity is stamped when the response COMPLETES, not when its headers arrive, and at most once per response — whichever of a full read or a close happens first. Stamping at header time would let the idle timer fire during a generation longer than the remaining budget.
- The wrapper forwards `CloseIdleConnections` to the transport it wraps, so installing it does not strand the provider's connection pool (`http.Client` only recognises the method on the RoundTripper it was handed).
- No loader wired means no transport: `EnsureLoadedClient` returns the pin resolver's answer unchanged, `nil` included, so the entry keeps falling back to the router-level client exactly as before.
- **Every** router carries it, cached and per-session alike. A per-session orchestrator is built from a `BuilderConfig` converted deep inside the session factory, which has no path to the supervisor, so `OrchestratorBuilder` also holds a builder-level default seam (`SetEmbeddedLLM`) that `buildRouter` falls back to when the config carries no `Loader`. An explicit per-config `Loader` wins; a half-populated one (a name with no supervisor — what `applyEmbeddedLoader` leaves when nothing is installed) does not shadow the default.

**Pre-dispatch gate (short-budget callers):**

- The transport gates on the wire, inside `http.Client.Do`, which is too late for a caller that has ALREADY armed a short deadline: the one-shot service requests create a `timeouts.serviceLLMRequestTimeout` context (default 120 s) first and issue the request second, so a cold load measured in minutes would be charged to it and the call would fail instead of waiting. Those paths call `FrontendAPI.ensureEmbeddedReadyForLLMRequest` BEFORE the context exists — `OptimizePrompt`, `GenerateCommitMessage` and the session manager's title generation (through `Manager.SetServiceLLMGate`, wired by `installServiceLLMGate`).
- The gate is keyed on `activeModelIsEmbedded`: the persisted install state AND the default model resolving to the backend-owned provider. Service calls run on the cached router, whose active model is `llm.default_model` (`buildRouter` applies it via `SetModel`), so resolving the default answers the same question the router will. For any other provider the gate returns at once — one config read.
- The load runs under the app context bounded by `DefaultLoadWaitTimeout`, not the caller's: an expired service budget must not abort a load that is legitimately in progress (the transport detaches for the same reason), and concurrent callers coalesce on the supervisor's single-instance gate.
- A gate failure skips the request rather than issuing it. For the title that means the session keeps its generated placeholder name and the failure is logged — best-effort by design; for the two RPCs the error is returned to the caller, who is the one that asked for a generation.
- `GenerateCommitMessage` gates AFTER its staged-diff check, so a nothing-to-generate call does not load gigabytes for no reason.

> **Injection is on the backend side, lock-free, and precedes the supervisor.** `BuilderConfig.EmbeddedLLM` is filled by `FrontendAPI.applyEmbeddedLoader`, reached through `FrontendAPI.toBuilderConfigLocked` — the single wrapper (`ToBuilderConfig` + the seam) that every production conversion call site uses, so `backend/configadapter.go` `ToBuilderConfig` stays a pure function of `*config.Config` and never learns about the supervisor. Two facts force the shape. Every call site runs with `configMu` held while the backend's lock order is one-directional (`(st.mu | st.infoMu) → configMu`), so the injection must not take `st.mu`: it reads `embeddedLLMState.loader`, an `atomic.Pointer[embeddedllm.Server]` that `embeddedBuild` publishes once, settling any concurrent build on the winner. And the router is first built inside `NewApplication` — which has no `FrontendAPI` — before this subsystem exists, so the injection must not require a supervisor either: the loader is `embeddedLoaderRef`, a value type that resolves the supervisor at CALL time and constructs it on the spot should a request somehow arrive before any lifecycle hook did (pure local work, and that path holds no `configMu`, so neither lock rule is bent). The gate is the persisted `embedded_llm.installed`, true from the config load onwards, which is also what leaves a user's own unrelated provider that happens to be named `embedded` untouched while the local model is not installed. `LoadWaitTimeout` stays zero so core applies `DefaultLoadWaitTimeout`; deriving it from config would need `embeddedAutoUnloadPolicy`, which takes `configMu.RLock` and would self-deadlock under the caller's lock. Because the first router predates the subsystem, `initEmbeddedLLM` re-attaches the seam to the LIVE router during the startup restore (`rebuildRouterForEmbeddedTransport`, under `saveMu`) — before `backend:ready`, so before any session can issue a request — and only when an install is present, so a machine without the model pays no rebuild. The same call path (`rebuildAfterEmbeddedConfigChange`, which the startup restore, a completed install and a removal all funnel through) also refreshes the builder-level default via `syncEmbeddedBuilderSeam`, which is what reaches the routers the per-config injection cannot: the session factory in `backend/application.go` converts the live config directly, having been closed over inside `NewApplication` before any `FrontendAPI` existed. Both are driven by the same `embedded_llm.installed` gate, so a removal withdraws both and a user's own provider reclaimed under the name `embedded` is never hijacked. See [llm-providers.md](llm-providers.md#backend-owned-embedded-provider).

**Port:**

- The port is allocated once at install (OS-assigned ephemeral) and persisted in both the manifest and `embedded_llm.port`. The persisted value is a PREFERENCE, not a reservation: `Server.EnsurePort` re-checks it immediately before every spawn and walks upward one port at a time until one is bindable, so a taken port moves the server instead of failing the load. Production always wires this hook.
- A move is written back to `embedded_llm.port` and the generated provider record is regenerated from it, so `base_url` and the socket the server bound always agree. `manifest.json` keeps the port the install allocated — the preferred one the next load re-scans from — so a temporarily taken port is reclaimed instead of drifting upward.
- The request path never trusts `base_url` for the port: the ensure-loaded transport redirects each request to the supervisor's LIVE port (`PortSource`), so the request that triggered a move reaches the model rather than the unrelated local process that took the old port. Without this the prompt would be handed to a stranger.
- A config write that fails is logged and does not fail the load: the server binds the free port either way and the redirect still applies, so a stale `base_url` on disk stays an administrative problem instead of an unusable model. The save-or-rollback path leaves the in-memory value matching the disk.
- Exhaustion of the range (`ErrNoFreePort`) transitions the supervisor to `error` with the diagnosis, and `LaunchSpec.Validate` remains the last gate against an out-of-range port reaching `exec`.
- The provider `base_url` is always derived from the persisted port.

**Provider and config:**

- `embedded_llm:` is the authoritative install/runtime state; the backend generates `llm.openai_compatible.embedded` (`base_url http://127.0.0.1:<port>/v1`, empty API key, `models: ["Bonsai 2 27B"]`) from it while `installed` is true, and removes it on Remove.
- Because `UpdateLLMConfig` replaces the whole `openai_compatible` map when the request carries one, the backend re-injects `embedded` from the authoritative state after building the candidate: saving LLM settings never deletes the embedded provider, and a draft that claims the key cannot redirect its `base_url` or swap its model list.
- The same reconciliation runs on every config load, so a hand-deleted entry self-heals and a record with no install behind it is dropped — a dangling loopback provider cannot survive a restart.
- The sync is copy-on-write: a candidate that is later REJECTED (e.g. a dangling `default_model`) leaves the live config byte-identical, even though the candidate shares its map headers with it.
- Remove must migrate `llm.default_model` off the embedded composite in the same operation that clears `installed`; otherwise the next load fails validation and the next settings save is rejected as dangling.
- The resolved context tier is written as an `llm.models` `context_window` override deterministically (no probe), preserving the documented precedence config override > probe > static catalog.
- `GetConfig` stays network-free; reading config never starts, probes or waits for the server.
- The composite model id `embedded/Bonsai 2 27B` resolves through the normal `ResolveModelID`/`ResolveDefaultModelProvider` path, so the router, the lazy probe and both pickers need no special case. Humanizing is DISPLAY-only and lives in the one picker implementation both model lists share (`ModelPickerMenu.providerLabel` maps the internal key `embedded` → **Embedded**; the bare name `Bonsai 2 27B` comes from the generated provider record): the value sent to the backend and persisted as `llm.default_model` stays the composite `embedded/Bonsai 2 27B`, so the Settings default-model picker and the chat toolbar picker cannot disagree and no resolution path sees the label.
- The **group order** is normalized in that same shared component, not per caller: `ModelPickerMenu.groupByProvider` hoists the `embedded` group to the front of every picker. Neither feed order would do it — the settings list iterates a provider map whose JSON keys Go alphabetizes, and `all_models` is emitted as (anthropic, chatgpt, sorted `openai_compatible`, sorted `anthropic_compatible`) — so without the hoist the local model lands mid-list on both surfaces and the two can drift apart again. The hoist moves the group only: every other provider keeps its input order and the group keeps its models' order. No group is invented when the model is not installed.
- The chat toolbar's picker reads its list from `useConfigData`'s module-level cache, which the embedded lifecycle must invalidate itself: `refreshEmbeddedLLMStatus` calls `invalidateConfigCache()` exactly when `installed` FLIPS between two applied snapshots. The first read is skipped (at startup the cache is fetched after the backend has already synced the provider, so there is no stale entry) and a failed read invalidates nothing (the previous snapshot stays). Load/unload and auto-unload changes must NOT invalidate — an unloaded model remains selectable, since the first request to it loads it — so a load never costs a config refetch. The settings dialog needs no invalidation: it re-reads config on every open.
- Installing the model never flips `model_profiles.enabled` and never changes `active_profile`; the `qwen3.8-27b` mapping is a hint only.

**Startup:**

- Startup performs local disk work only (manifest restore). It performs no download, no network call and no automatic load; the install download runs only on an explicit user action, in the background, without blocking the RPC.
- Shutdown stops a running server.

**Settings UI:**

- The block renders exactly one action surface per state: Install while not installed, one progress row per reporting component while installing, and the install record + Load/Unload + Remove + auto-unload once installed. Remove is reachable only through its confirmation dialog, so the first click never issues the RPC.
- The block reads its state from `embeddedLLMStore`, whose snapshot has one writer (`setStatus` from `GetEmbeddedLLMStatus`); an `embedded_llm:state` event re-reads that snapshot instead of patching it, so no half-merged state is ever rendered.
- Mounting the block subscribes to the two events and reads the status once. It installs nothing, loads nothing and probes nothing.
- The install record shows the EFFECTIVE backend and the resolved packing from the status snapshot — never a locally remembered or probed value.
- A synchronous install/remove/load/unload refusal is rendered inline by the block; only a BACKGROUND install failure additionally reaches the `runtime_error` toast.
- Every size in the block is scale-agnostic: no viewport unit, no `*-screen` utility and no pointer-anchored placement (see [frontend/ui-scale.md](frontend/ui-scale.md)); its colors are design tokens only.

**Status-bar UI:**

- The indicator renders nothing — its own leading separator included — unless it has a state to report, so a hidden block never leaves a stray separator in the bar (the same contract `ProcessMemoryStatus` holds). "Nothing to report" includes installed-but-stopped: an idle install is not status-bar news.
- Exactly one surface is rendered at a time, in a fixed precedence: a live install run outranks the snapshot (the backend refuses a load while one is in flight, and the progress event is itself proof of the run), then `loading`, then `loaded`, then an error.
- Install progress in the bar is the ACTIVE artifact's own fraction — the first reported component that has not reached `done` — never an aggregate across artifacts (a ~100 MB runtime against 6.7 GiB of weights would make a summed percent jump backwards) and never an invented percentage for a byte-less stage or for the window before the first progress event.
- The component and stage words are the shared `frontend/src/lib/embeddedLLMLabels.ts` vocabulary, so the status bar and the Settings rows describing the same `embedded_llm:install_progress` payload cannot name it differently; an artifact or stage this frontend does not know yet degrades to its raw id, never to a blank.
- Residency is reported from the snapshot's `loaded` flag alone: it survives any number of refreshes including one whose `idle_remaining_seconds` is 0, and it disappears on the unload transition whoever triggered it (a manual Unload or the idle timer). No remaining-idle countdown is rendered, because the snapshot refreshes only on transitions and a ticking readout derived from it would be wrong within a second.
- The block is strictly read-only: it issues no RPC other than the status read, exposes no action, and never installs, loads, unloads, removes or probes. The Settings block stays the only surface that mutates.
- It is not gated on project mode (the local model is process-wide, like RSS), and every size is scale-agnostic: fixed layout-px widths plus percentages for the bar, no viewport unit, no `*-screen` utility, no pointer-anchored placement; its colors are design tokens only.

## Configuration

Authoritative reference: `config.example.yaml` (section `embedded_llm:`). Every value is written by the app — the section is state, not tuning, and hand-editing it does not install or remove anything.

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `embedded_llm.installed` | bool | `false` | Weights + runtime are present and verified. Gates the provider entry. |
| `embedded_llm.packing` | string | `""` | Resolved packing, `PQ2_0` \| `PTQ1_0`. Informational — shown in Settings. |
| `embedded_llm.backend` | string | `""` | Probed backend (`metal`, `cuda-12.4`, `cuda-12.8`, `cuda-13.3`, `rocm`, `vulkan`, `cpu`). Informational. |
| `embedded_llm.port` | int | `0` | Persisted loopback port. `0` = allocate at install time. Valid range 1024–65535; `installed: true` with `port: 0` is rejected (a completed install always has one). |
| `embedded_llm.model_file` | string | `""` | Absolute path of the installed GGUF. |
| `embedded_llm.runtime_version` | string | `""` | The pinned fork release the runtime came from. |
| `embedded_llm.installed_at` | string | `""` | RFC 3339 install timestamp. |
| `embedded_llm.auto_unload.enabled` | bool | `true` | Idle timer on/off. |
| `embedded_llm.auto_unload.minutes` | int | `60` | Idle minutes before the process is stopped. Must be ≥ 1 — validated even while the timer is disabled, so re-enabling it can never activate a dead budget. |

Derived config the backend writes (never authored by hand), reconciled by `Config.SyncEmbeddedLLMProvider` → `LLMConfig.SyncEmbeddedProvider` at two points — the config load path (`LoadWithResult`, after `ApplyDefaults` and before `validate`) and `UpdateLLMConfig` (after the candidate is built, before it is validated):

- `llm.openai_compatible.embedded` — `base_url: http://127.0.0.1:<port>/v1` (always derived from the persisted port, never stored independently), `api_key: ""`, `models: ["Bonsai 2 27B"]`, no `tls_fingerprint` (plain HTTP on loopback, so the ADR-054 pin does not apply); present exactly while `installed` is true. `output_token_reserve` is the one operator field preserved across regeneration.
- `llm.models["Bonsai 2 27B"].context_window` — the resolved RAM tier, so token budgets are correct while the server is stopped. Written only by a caller that knows the tier (`contextWindow > 0`); a `0` argument leaves an existing override untouched, which is what both sync points pass because neither performs a hardware probe or any network I/O. Other override fields (family, tokenizer, output limit) are preserved, and an operator-authored `family:` stays authoritative across every sync. The sync must never WRITE a family: an absent one is exactly what lets the sp4rk catalog's `qwen` — which the reasoning-effort picker and the request encoding both key off — reach the model through partial-override inheritance. See [Reasoning effort and the family resolution](#reasoning-effort-and-the-family-resolution).

The sync copies the provider and override maps before mutating them: `UpdateLLMConfig` builds its candidate as a struct copy that shares map headers with the live config, so a candidate that is later rejected must not leak the injection — or the removal — into the observable state.

The override is backend-written but operator-owned afterwards: the Configure dialog (`SetModelConfig`) rewrites the whole `llm.models` entry, and saving it with every field at the built-in default DROPS the entry — including the tier. That is the documented precedence (an explicit user choice wins), and it self-heals: with no config override the lazy probe (tier 1.5) rediscovers the window from `/v1/models` the first time the server runs, and the next install/load sync rewrites the tier. A runtime entry written that way also pre-fills `Family` from the model name, which outranks the catalog — see the ordering hazard in [Reasoning effort and the family resolution](#reasoning-effort-and-the-family-resolution).

`validate()` rejects an out-of-range port, an install without an allocated port, and a non-positive `auto_unload.minutes`, each with an actionable message naming the key and the fix. Because the record disappears with `installed: false`, the Remove flow must also migrate `llm.default_model` off `embedded/Bonsai 2 27B` — otherwise the next load fails validation and the next settings save is rejected as a dangling default. Migration means moving it to the first *other* enabled model id, not clearing it: `validate()` also rejects an empty `llm.default_model`, so an empty result is correct only when the embedded model was the only one enabled (a config with no provider at all is invalid independently of this subsystem, and `ApplyDefaults` fills the first available model on the next load). `Remove` preserves the `auto_unload` pointers while resetting every other `embedded_llm.*` field — an idle budget is an operator setting, not install state. Fixed operational constants (not configurable, by design — tunability would undermine the pins and the hardware safety margins):

| Parameter | Value |
| --- | --- |
| Minimum system RAM | 16 GiB (typed refusal below it) |
| Runtime pin | `prism-b10709-9a9394a` |
| Sampling flags | `--temp 1.0 --top-p 0.95 --top-k 20 --jinja -fa on` |
| Progress throttle | ~100 ms |
| Disk headroom on top of the artifact set | 2 GiB (`DefaultDiskHeadroom`) |
| Max decompressed size per archive entry | 2 GiB (`maxExtractEntryBytes`) |
| Max decompressed size per archive | 8 GiB (`maxExtractTotalBytes`) |
| macOS `xattr`/`codesign` command budget | 2 min per command |
| macOS `--version` smoke-test budget | 60 s |
| Auto-unload defaults an install establishes | `enabled: true`, `minutes: 60` (unset knobs only) |
| Ready budget for one Load (spawn → first `/v1/models` answer) | 15 min (`DefaultReadyTimeout`) |
| Request-path budget for waiting on a cold load | 17 min (`DefaultLoadWaitTimeout` = `DefaultReadyTimeout` + 2 min) |
| Readiness poll interval | 500 ms (`DefaultReadyPollInterval`) |
| Budget for a single readiness probe | 10 s (`DefaultProbeTimeout`) |
| Graceful stop window before the kill | 10 s (`DefaultStopTimeout`) |
| Bounded wait after the kill | 5 s |
| Server output retained for failure messages | last 24 lines |
| Level the server's own stdout/stderr is logged at | debug |
| Bind address | `127.0.0.1` (`LoopbackHost`, enforced by `Validate`) |
| Web UI | disabled unconditionally (`--no-webui`) |

The four supervision budgets and the stop timeout are per-`Server` fields rather than config keys: they are recovery timeouts with no operator meaning, and the idle budget — the one knob a user actually tunes — is `embedded_llm.auto_unload`.

## Extension Points

**Add a packing or a second model.** Append the asset (component, URL, SHA256 from the HF LFS OID, exact size) to `core/embeddedllm/registry.go`, extend `Packing`, and teach `resolve.go` when to select it. Resolution stays a pure function so the whole matrix remains table-testable; the manifest records what was actually installed, so mixed installs are distinguishable.

**Add a backend.** Extend `Backend`, add the probe step to `hardware.go` in the fixed precedence order, add the per-platform runtime assets (with checksums from the GitHub REST `digest` field) to the registry, and extend `resolve.go` for `-ngl`/image-token policy. Backends that need a companion archive follow the Windows `cudart` shape: a second `Asset` with its own component, progress and checksum.

**Raise the runtime pin (security/maintenance bump).** Update the release tag, every affected runtime URL and checksum, and run a CVE review of the old→new range before shipping — the same discipline the tool-manager requires. Existing installs reconcile against the manifest's `runtime_version`; a pin change forces a runtime re-download while weights are kept.

**Change port policy.** Three seams own the port and nothing else chooses one: `Installer.AllocatePort` (`func(ctx) (int, error)`, called once during install, defaulting to the unexported `ephemeralLoopbackPort`) picks the initial one; `Server.EnsurePort` (`func(ctx, port) (int, error)`, called immediately before every spawn) may substitute and persist a replacement — production wires it to `backend.embeddedEnsurePort`, which scans with `embeddedllm.SearchFreePort` and writes a move back to config; and `PortProber` (the third argument of `SearchFreePort`, defaulting to `loopbackPortFree`) decides what "free" means, injected in tests through `embeddedLLMState.portProbeFn` and in core by passing the prober directly. Leaving `EnsurePort` nil trusts the persisted port, which is correct only for a caller with no config to keep in sync. An explicit `InstallOptions.Port` bypasses allocation entirely. The provider base URL is always derived from the persisted value and the request path always aims at the live one, so a different allocation strategy is a change to these seams and nothing else.

**Wire or change the supervision.** `Server` owns the whole lifecycle and every external effect is an injectable field: `Spawn` (`SpawnFunc`) replaces the real `exec` with a double, `EnsurePort` the port policy, `OnState` the event transport (the backend forwards `StateEvent` as the global `embedded_llm:state` payload), `HTTPClient` the readiness client, and `Now`/`HostOS`/`Platform` the clock and the platform keys. `Stop` has exactly the `Installer.Stop` signature, so removal and shutdown wire with no adapter (`installer.Stop = server.Stop`). The command line exists in one place — `LaunchSpec.Args` — and its inputs come from the manifest plus the pure policy in `resolve.go`, so a new flag is a `LaunchSpec` field, an `Args` entry and a `Validate` rule, and nothing else. `Process` is deliberately an interface with `Stdout`/`Stderr` readers so the production output pumping and log tail are exercised by tests that never start a real server. The request path is a seam of the same shape: `Loader` (`Load` + `MarkActivity`) is all `EnsureLoadedTransport` needs, so a different residency policy — pre-warming on project open, keeping the model pinned while a session is live — replaces the loader or wraps it, and the transport, its coalescing and its activity stamping stay put.

**Wire or change the install orchestration.** `Installer` owns the whole flow; the config layer is reached only through `ConfigSink` (`ApplyInstalled(InstallState)` / `ApplyRemoved()`), implemented by `backend/frontend_api_embedded.go`. The reference implementation and its executable contract — what an install must produce in `embedded_llm.*`/`llm.*`, which auto-unload knobs survive, and what a removal must leave behind — is `backend/config/embedded_llm_sink_test.go`. A different progress transport only has to adapt `InstallOptions.Progress` (the `embedded_llm:install_progress` payload *is* the `Progress` struct), and every external effect (`Downloader`, `Probe`, `RunCommand`, `AllocatePort`, `Stop`, `Now`, `HostOS`) is an injectable field, so the flow itself is testable without a network, without multi-gigabyte artifacts and without macOS binaries.

**Add a config knob.** Add the field to `EmbeddedLLMConfig` with a yaml tag, a default in `backend/config/defaults.go`, a rule in `validateEmbeddedLLM` (`backend/config/config.go`, reached from `validate()`), an entry in `config.example.yaml`, and — if the UI exposes it — an RPC in `backend/frontend_api_embedded.go` plus a field on the `embedded_llm:state` payload and its type in `frontend/src/types/events.ts`.

**Add lifecycle events.** Both events are global (bare names, not session-scoped): `embedded_llm:install_progress` and `embedded_llm:state`. Extend the payload structs, `backend/events.go`, `frontend/src/types/events.ts` and the event catalog together.

## Related Specs

- [ADR-066: Embedded LLM Runtime (Bonsai 2 27B)](../decisions/066-embedded-llm-runtime.md) — the decision record; D1–D12 are cited by number throughout this spec
- [Tool Manager](tool-manager.md) — the subsystem deliberately **not** reused, with the limits (1 GiB / 512 MiB / 5 min / 200 MiB) and the offline-first startup invariant that rule it out
- [ADR-032: Offline-First Tool Reconciliation](../decisions/032-offline-first-tool-reconciliation.md) — the startup invariant an on-click multi-GiB download must respect
- [LLM Providers](llm-providers.md) — provider config, the `ProviderEntry.HTTPClient` hook, the lazy context probe and the context-window precedence this subsystem writes into
- [ADR-054: Per-Provider TLS Pinning](../decisions/054-per-provider-tls-pinning.md) — the existing consumer of the same per-provider client hook
- [Model Profiles](model-profiles.md) — the hint-only `suggested_profile_id` semantics preserved by the `qwen3.8-27b` mapping
- [ADR-045: GPU Embedding Execution Provider](../decisions/045-gpu-embedding-provider.md) — the prior (embedding-only) hardware-detection decision
- [Workspace](workspace.md) — the embedding-model artifact that shares `<agentDir>/models`
- [Contract: Desktop ↔ Frontend](../contracts/desktop-frontend.md) — RPC conventions (error-free getters, mutating methods returning `error`) and the binding regeneration rule
- [Event Catalog](../contracts/event-catalog.md) — where the two global events are catalogued
- [Frontend Stores](frontend/stores.md) — `embeddedLLMStore` and the stable-selector rule
- [Security Model](../architecture/security-model.md) and `SECURITY.md` — ASI04 (supply chain) and ASI05 (agent-invokable binaries) behind the storage and pinning invariants
