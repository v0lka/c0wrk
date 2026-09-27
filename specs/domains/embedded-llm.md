# Embedded LLM

## Purpose

The embedded-LLM subsystem makes "run fully local" a single Settings action: c0wrk downloads a pinned inference runtime (the PrismML-Eng/llama.cpp fork) and the pinned **Ternary-Bonsai-2-27B** weights, provisions both for the machine's actual hardware, supervises `llama-server` as a loopback OpenAI-compatible endpoint, and registers it as the ordinary provider `embedded` with the model `Bonsai 2 27B`. It owns four things c0wrk had no spec for before: supervision of a long-lived local inference server, multi-gigabyte resumable downloads, localhost port allocation for an app-managed server, and a hardware probe for inference.

## Key Files

The subsystem (`core/embeddedllm/`) plus every existing file it touches. Port handling is split in two: `install.go` owns the ALLOCATION (`ephemeralLoopbackPort`, reached through the `Installer.AllocatePort` seam), `port.go` owns the pre-spawn SCAN (`SearchFreePort`), and `Server.EnsurePort` in `server.go` is the hook that runs it. Production wires `EnsurePort`; `AllocatePort` stays nil so the OS picks the initial port — see [Port allocation](#port-allocation).

**Subsystem (`core/embeddedllm/`)**

- `core/embeddedllm/registry.go` — compile-time artifact registry: `RuntimeTag` (the pinned fork release `prism-b10735-842b188`), `ModelRevision` (the pinned Hugging Face commit), per-packing model assets with SHA256 (HF LFS OID) and exact byte sizes, the vision projector, and per-platform × per-backend runtime archives with SHA256 taken from the GitHub REST per-asset `digest` field (including the paired Windows `cudart` archive). Pure lookups (`RuntimeAsset`, `CudartAsset`, `ModelAsset`, `MMProjAsset`, `ArtifactSet`, `TotalBytes`) plus `ValidateRegistry`, which self-checks the pin tables. `registry_test.go` pins the digests/sizes verbatim (`TestRegistryModelPinsMatchUpstreamLFS` for the HF LFS OIDs, `TestRegistryRuntimePinsMatchUpstreamRelease` for the GitHub release `digest` field) and proves the validator is not vacuous — against a local fixture, never by mutating the shared tables. The pin is currently `prism-b10735-842b188`; see the **Pin bump 2026-09-25** note under **Invariants** for the CVE-reviewed advance from `prism-b10709-9a9394a` — ADR-067 D11's literal tag is historical, because accepted ADRs are immutable
- `core/embeddedllm/download.go` — the resumable downloader (`Downloader`, `Download`, `VerifyFile`, `Result`, `ProgressFunc`, `RequiredFreeBytes`): HTTP `Range` resume from a `<destination>.part` file, the two explicit Range fallbacks, throttled progress callbacks, context cancellation, fail-closed SHA256 verification before promotion, and an artifact-sized disk guard. Deliberately **not** `toolmanager.Download` (see Invariants). `download_test.go` drives it against an HTTPS `httptest` server in Range/ignore-Range/416 modes with an injectable `FreeSpace`
- `core/embeddedllm/hardware.go` — RAM probe (`sysctl hw.memsize` / `/proc/meminfo` / `GlobalMemoryStatusEx`) and accelerator probe (`nvidia-smi` → `nvcc` → `rocminfo`/`rocm-smi`/`hipcc` → `vulkaninfo` → Metal on darwin/arm64 → CPU), producing a `Hardware` value. Also owns the `Backend` vocabulary the registry tables are keyed by. The OS-specific RAM read lives in the two build-tagged companions `hardware_unix.go` (`!windows`: darwin `sysctl` + linux `/proc/meminfo`) and `hardware_windows.go` (`GlobalMemoryStatusEx` via `syscall`); every parser is in the untagged file so it is testable on all three platforms. Every probe in the package spawns through one hardened helper (`runProbeCommand`: `exec.LookPath` resolution, the 2 s `probeCommandTimeout` bound, `probeWaitDelay` on the output pipes, `sysproc.HideConsole`), which `topology.go` reuses rather than re-implementing
- `core/embeddedllm/topology.go` — the SECOND, post-install hardware probe: `ProbeDevices(ctx, binaryPath, logger)` asks the installed runtime what accelerator memory it can see (`llama-server --list-devices`) and returns a `MemoryTopology` — every device's total/free MiB, whether those bytes alias host RAM (`Unified`), and the two budgets a fit decision may spend (`DeviceBudgetBytes` / `HostBudgetBytes`). It exists because `Hardware` has no VRAM field and cannot grow one: at probe time the binary is not on disk yet, so there is nothing to ask. Fail-soft by contract (a missing, hung, nonzero-exit or unrecognized answer yields `ok=false` and a Debug log, never an error), and split like the rest of the probe layer into an I/O half and a pure half (`parseDeviceListing`, `classifyUnified`, `buildTopology`) that is table-tested against verbatim captured runtime output. `topology_test.go` carries the fixtures plus the never-sum-a-unified-pool guard
- `core/embeddedllm/resolve.go` — pure resolution: `Resolve(ResolveInput)` over the full machine picture and the `ResolveMachine(platform, backend, ramGiB)` / `ResolveProfile(MachineProfile)` convenience forms, producing a `Resolution` (runtime asset(s), packing + its `PackingReason`, GPU family, the guard decisions, `-ngl`, context size, image-max-tokens, cudart requirement). No I/O, fully table-tested. It consults the registry through the `AssetTable` seam and never invents a URL or checksum; a probed backend the pin cannot serve is degraded (never refused) to the best build the platform does have, and `applyCompatGuards` then folds in the compatibility table's substitutions. `decidePacking` is the packing table itself; `packingFor(backend, gpuFamily, fitsPQ2_0, hostCaps)` is the four-input form the resolver calls
- `core/embeddedllm/compat.go` — the machine-class knowledge derived from the pinned model's own documents: the `GPUFamily` vocabulary and its description classifier (`ClassifyGPU` / `ClassifyGPUs` / `ClassifyTopology`), the tri-state input vocabularies (`HostCaps`/`CPUFeature`, `BudgetFit`), the pure guard table `CompatibilityGuards(backend, gpuFamily, platform) → []GuardDecision` with its typed reasons, derived severities and upstream issue citations, `mergeGuardDecisions` for the two-pass record, and the pin awareness behind the AVX-512 rule (`parseRuntimeBuild`, `minBuildWithAVX512PQ2_0Fix`). `compat_test.go` table-tests the packing matrix, the classifier, every guard and the pin parsing, and scans this file's own source to prove each guard constant still cites an issue number. See [Backend compatibility guards](#backend-compatibility-guards)
- `core/embeddedllm/memory.go` — the measured memory model of the ONE pinned model: `KVType` (the closed set `f16` / `q8_0` / `q4_0`, with `ParseKVType` refusing everything else), `ModelMemoryProfile` (every term of the footprint — on-disk sizes derived from the registry, device/host weight residency, the context-independent recurrent state, the compute reserves, the exact per-token KV cost and the divisor table — each carrying the measurement behind it) and the two pure projections `ProjectDeviceMiB` / `ProjectHostMiB`. `PinnedMemoryProfile` builds it; nothing here decides a context size or refuses an install. See [Memory model](#memory-model)
- `core/embeddedllm/plan.go` — the **pure planner** `Plan(MemoryTopology, ModelMemoryProfile, Tuning, Backend, GPUFamily) → (MemoryPlan, error)`: the decision layer that turns the two measured halves (capacity from `topology.go`, requirement from `memory.go`) into a launch shape. Owns the `Tuning` override vocabulary in which *unset* is representable, the `--fit` **exclusivity** rule, adaptive `f16 → q8_0 → q4_0` KV escalation (long contexts — above the 65536 fit floor — start the ladder at q8_0: the KV cache dominates the footprint there, halving it costs the fork-measured ~1% throughput and frees device capacity), the two measurement-forced defaults (`-fitc` 65536, `-np` 1), the `splitAllowanceMiB` policy margin that prices `memory.go`'s Metal-only measurement, and the operator-facing `Notes` trail. It is also the **combined memory gate** that replaced ADR-067 D6's flat RAM floor: `memoryGate` (the first check in `Resolve`), the tri-state `deviceAxisState`, `gateBudgetsFor` (measured budgets, or budgets derived from the RAM probe when nothing was probed), `backendHasIndependentVRAM`, `normalizeTopology`, the typed `InsufficientMemoryError` / `ErrInsufficientMemory`, the exported `CheckMemoryBudget` / `DefaultHostReserveGiB` the desktop RPC calls, and `Plan`'s **degradation ladder** (`relaxations`) that shrinks a shape before refusing it. No I/O, no clock, no probing — asserted structurally (`TestPlanImportsNoIOPackage` bans every I/O-capable import from the file) and behaviourally (`TestPlanIsPure`). `plan_test.go` tables the exclusivity rule in both directions, the KV ladder at three budget levels and the pinned-offload refusal; `resolve_test.go` tables the gate's verdicts and pins the derived unified floor. See [Memory plan](#memory-plan) and [The combined memory gate](#the-combined-memory-gate)
- `core/embeddedllm/layout.go` — the on-disk layout. `Layout{RuntimesRoot, ModelRoot}` is built by `NewLayout` from roots the centralized path API resolved (`config.RuntimesDir` / `config.EmbeddedModelDir`), so the subsystem never re-derives `<agentDir>/runtimes` itself; every derived path (`RuntimeDir`, `RuntimeStagingDir`, `RuntimeRetiredDir`, `DownloadsDir`, `ManifestPath`, `Destination`, `ModelFile`) goes through one containment-checked join over `pathutil.IsWithinPath`. `Layout.Owns` is the ownership predicate every deletion is gated on, `RuntimeDirName` is the `llama-<tag>-<backend>` shape, and `ServerBinaryPath` locates `llama-server` in an extracted tree by a deterministic walk (the archives nest it, at a depth that differs per platform)
- `core/embeddedllm/install.go` — Install/Remove orchestration: `Installer` (with injectable `Downloader`, `Probe`, `ProbeDevices`, `RunCommand`, `AllocatePort`, `Stop`, `Now`, `HostOS` seams plus the exported `StopTimeout` bound — `Stop` runs FIRST in both `Install` and `Remove`, so a resident server never executes out of a tree the call is about to retire or delete, and never holds the memory the install's probe and gate are about to price; `StopTimeout` bounds that step-0 call alone, gate wait included, through `stopBeforeInstall` (`0` = inherit the caller's ctx), and is NOT `Server.StopTimeout`, which is the graceful signal→kill window inside `terminate`), `InstallOptions`, `InstallReport` (which carries the compatibility record: `Guards` + `PackingReason`), `Manifest` + `ReadManifest`/atomic write (persisting `packing_reason`, `gpu_family`, `guards` and the `topology` + `plan` pair a load launches from; the write lands in a uniquely named `manifest.json.*.tmp` sibling and renames over the target, and it begins by SWEEPING leftovers of that shape older than `manifestTempStaleAfter` — one minute, age-gated so a concurrent writer's live temporary survives — because a crash between `CreateTemp` and the rename leaks a name nothing else knows), `recordableContext`, `Progress` + `InstallProgressFunc`, the `ConfigSink`/`InstallState` boundary to the config layer, per-component progress, the independent verification gate, archive extraction with traversal/symlink/size guards, the staging → retire → rename runtime swap, and the macOS quarantine-clear + ad-hoc codesign + `--version` smoke test. It also owns the production port allocator `ephemeralLoopbackPort` (bind `127.0.0.1:0`, read the assignment back, close), which `Installer.allocatePort` reaches whenever the `AllocatePort` seam is `nil`
- `core/embeddedllm/port.go` — the pre-spawn free-port scan: the `MinLoopbackPort`/`MaxLoopbackPort` bounds (mirroring `backend/config`'s `EmbeddedLLMMinPort`/`EmbeddedLLMMaxPort`, which core cannot import), `PortProber` (the injection seam), `SearchFreePort` (walk upward from the preferred port until one is bindable; a preference OUTSIDE `[MinLoopbackPort, MaxLoopbackPort]` is CLAMPED into it rather than failing — the low clamp covers the 0 "not allocated yet" sentinel, and the high clamp covers the hand-edited or corrupted `manifest.json` that `ReadManifest` performs no range validation on, which would otherwise leave the loop never executing and report an inverted range like "every port from 99999 to 65535 is taken", misdiagnosing a bad preference as port exhaustion; an exhausted range is `ErrNoFreePort`, ctx is checked per candidate) and the production prober `loopbackPortFree` (bind and release — the same test `llama-server` itself applies, so a lingering `TIME_WAIT` socket is judged the way the server would judge it). `port_test.go` covers the scan against a staged prober and against a really occupied loopback port
- `core/embeddedllm/server.go` — the supervisor: `Server` (with injectable `Spawn`, `EnsurePort`, `OnState`, `HTTPClient`, `ProbeDevices`, `Tuning`, `PersistContext`, `Now`, `HostOS`, `Platform` seams and five budget fields), the `State` machine plus `StateEvent`/`Status`, `Load`/`Unload`/`Stop`/`SetInstalled`, the single-instance gate, `LaunchSpec` with `Args`/`Validate` (the only place the command line exists), the launch derivation (`launchSpec` / `launchIdentity` / `effectivePlan` / `replan` and the `resolvedLaunch` they return — `launchIdentity` containment-checks both of its path halves against the layout, so a tampered install record cannot aim a launch at bytes outside it, falling back to the layout-derived model path with a Warn rather than refusing), the post-ready `/props` context readback (`propsURL`, `readPropsContext` — overflow-checked and capped at the model's training context, failing soft, because it is the one context figure that arrives from an external process over HTTP and it lands in two durable stores — `recordEffectiveContext`, which MERGES onto the current on-disk manifest and CLAMPS the reported window to the launched `-c` in both stores, because a server can never legitimately report more than this launch ordered and an over-window readback would scale compaction and the output budget off a window nobody holds), the `Process`/`SpawnFunc`/`LaunchCommand` seams and the production `spawnOSServer`, `/v1/models` readiness polling, output pumping into `slog` with a bounded tail that ends up in every failure message, crash detection, the fit-contract tail scanner (`fitFailureMarker` / `scanFitFailure` → `Status.FitWarning`), and graceful-then-forced termination, plus the distinct NO-GATE force path (`forceUnload`) that stops a live child when the caller's budget expired while the single-instance gate was held by an in-flight load — the only way a quit during a cold load avoids leaving `llama-server` running AND UNTRACKED. It arms its OWN stop budget under `context.WithoutCancel` (`stopTimeout + killWait + 1s`), because the caller's has already expired, and its `takeRun` detach is PROVISIONAL: a force stop that does not take re-attaches the run handle before it reports the failure, so a wedged child is never left live and untracked and the next attempt targets the same process instead of stacking a second server beside it. It leaves the terminal transition to the interrupted `Load`: when a `Load` is still in flight and the state is still `loading`, it SKIPS its own success transition, while a force stop with no load in flight ends at `installed`. WHICH transition the interrupted `Load` then writes is decided on the `Load` side, because the snapshot is taken before `terminate` signals the child and `waitReady` can therefore still return nil — `Load` re-validates under `s.mu` that the run it published is still the published one before claiming residency, so a dead child is NEVER reported as `loaded` and the two outcomes are `error` carrying the load's own diagnosis or `installed` with the unexported `errStoppedDuringLoad` returned to the caller (see the Unload flow). It also owns the launch vocabulary the memory planner renders into: `LayerMode` (the four `-ngl` answers, one of which is *omit the flag*), the typed `LaunchSpec` fields for `-fit`/`-fitt`/`-fitc`/`-ctk`/`-ctv`/`-nkvo`/`--no-mmproj-offload`/`-np`/`--cache-ram`/`-dev`/`-sm`, the `ApplyMemoryPlan` bridge, and the `Validate` rules that make the fit-exclusivity invariant a spec error rather than a spawn-time abort. `EnsurePort` is nil only for a caller with no config to keep in sync; production always wires it
- `core/embeddedllm/idle.go` — the auto-unload budget: `AutoUnload` (plus `DefaultAutoUnload`/`NewAutoUnload`), the unexported `idleTimer` tracker (which carries the deferred-expiry `graceUntil` and the deferral episode's wall-time start stamp `deferSince` beside the activity stamp), the in-flight request count (`BeginRequest`/`EndRequest`/`InFlightRequests`), `idleGracePeriod` and the `Server` surface around it (`SetAutoUnload`, `AutoUnloadPolicy`, `MarkActivity`, `IdleRemaining`, `LastActivity`). Its defining rule is that weight-load time is never charged to the idle budget, and its sibling rule is that an expiry never stops a server that is mid-generation: an idle expiry with a request open defers the unload by a bounded grace rather than killing it — unless activity landed in the window the expiry read the count outside the lock and already armed a fresh FULL budget, which `deferIdleUnload` detects by re-reading the budget under `Server.mu` and then keeps. `armLocked` always leaves a real timer behind (`time.AfterFunc`, at zero delay for a spent budget) instead of firing the callback inline, because the expiry re-validates against `armed` and would reject a fire that left no timer. The deferral is itself bounded, and the bound is WALL TIME rather than a count of deferrals (`deferSince` stamped on the first deferral of an episode, kept across the grace re-arms that continue it, cleared by `mark`/`disarm`/`setPolicy`): one episode may not outlast a full idle budget, after which the unload proceeds anyway and names the leaked count at Warn
- `core/embeddedllm/transport.go` — the ensure-loaded request path: `Loader` (the supervisor seam, satisfied by `*Server` as-is) and its two OPTIONAL capabilities — `PortSource` (`*Server.Port`, read to redirect a request whose URL was built from a base_url that predates a port move) and `RequestTracker` (`BeginRequest`/`EndRequest`, bracketing the exchange so an idle expiry defers rather than stopping the server mid-answer). Optional is a property of the INTERFACE, not of production: `backend.embeddedLoaderRef` implements all three, so the live-port redirect and the mid-generation deferral are live controls rather than hypotheticals, and the package pins `var _ Loader/PortSource/RequestTracker = (*Server)(nil)` while the backend pins the same three on `embeddedLoaderRef` — the two halves of one contract, because a capability discovered through a type assertion on the `Loader` value is invisible to the compiler at the wiring site. A test double or alternative loader that does not track requests simply gets the completion stamp alone, `EnsureLoadedTransport`/`NewEnsureLoadedTransport` (the `http.RoundTripper` that makes the model resident before the request, aims it at the live port, arms the request budget only once the model can answer, brackets the exchange through the OPTIONAL `RequestTracker` capability so an idle expiry defers instead of cutting a generation short, and stamps activity when the response completes), `EnsureLoadedClient` (derives the provider entry's client, moving the shared LLM timeout off `http.Client.Timeout` and into the transport so the cold-load wait is not charged to it) and `ErrLoadWaitTimeout`/`DefaultLoadWaitTimeout`
- `core/embeddedllm/limits.go` — the numeric CEILINGS shared across the layer boundary: `MaxAutoUnloadMinutes` (525600), `MaxTuningMiB` (1<<31), `MaxTuningLayers` (1<<20), `MaxTuningParallel` (64) and `MaxTuningHostReserveGiB` (1<<20). They are exported because `backend/config` validates the persisted operator surface against the very same figures — a bound that exists at only one of the two layers is a bound an operator can walk around by hand-editing `config.yaml`. Every ceiling is an OVERFLOW or absurdity guard, not a tuning opinion: each is set far above any real machine so rejecting it can never refuse a legitimate configuration, while still making the arithmetic downstream total (the minutes→nanoseconds multiply, the MiB→bytes shift, the per-layer and per-slot multiplications, and the float→int conversion that is implementation-defined in Go when the result type cannot represent the value). What a real machine can actually hold stays the memory gate's and the planner's call. The frontend's TS mirror of these ceilings is machine-checked against this file: `frontend/src/lib/embeddedTuningLimits.test.ts` reads the declarations and EVALUATES their shift literals, so a bump here fails that suite instead of drifting silently (see [Key Files](#key-files)). The package's own bounds live next to their subject instead (`maxTrainingContext` in memory.go, `DefaultReadyTimeout` in server.go)

**Existing touch points**

- `backend/config/config.go` — `EmbeddedLLMConfig` (+ `AutoUnloadConfig`, and the operator-owned memory-plan override surface `TuningConfig` / `EmbeddedLLMContextConfig` / `EmbeddedLLMOffloadConfig` with `TuningConfig.ToTuning` as the single translation into `embeddedllm.Tuning`, `validateEmbeddedLLMTuning` delegating to it, and `EmbeddedLLMMaxContextTokens` reading the context ceiling from the pinned profile rather than transcribing it) with yaml tags and the identity constants (`EmbeddedLLMProviderName`, `EmbeddedLLMModelName`, `EmbeddedLLMHost`, port bounds, the 60-minute default); `Config.SyncEmbeddedLLMProvider` / `LLMConfig.SyncEmbeddedProvider` generate or remove the backend-owned provider record and write the `llm.models` context-window override; `validateEmbeddedLLM` is wired into `validate()` and `LoadWithResult` syncs on every load
- `backend/config/defaults.go` — the `auto_unload` pointer defaults (`enabled: true`, `minutes: 60`); every other field's zero value already IS the documented not-installed default. `tuning` is deliberately **not** seeded at all: its all-nil zero value IS the all-Auto default, and materializing the pointers would collapse *unset* into *explicit auto* on the first save
- `backend/config/paths.go` — path constructors; owns `ModelsDir` (`<agentDir>/models`, the flat embedding-model files), `ToolsDir`/`ToolsBinDir`, and the two embedded roots `RuntimesDir` (`<agentDir>/runtimes`) and `EmbeddedModelDir` (`<agentDir>/models/bonsai-2-27b`). `TestEmbeddedLLMDirs` pins both outside the tools tree, which is what makes the agent-PATH isolation a checked property rather than a convention
- `backend/frontend_api_embedded.go` — the RPC surface and the wiring of core into the app: `GetEmbeddedLLMStatus` (the read-only getter, no error — an unconstructable subsystem reports `available: false`), `InstallEmbeddedLLM` (the synchronous gates only — the subsystem-wide OPERATION gate, bounded probe, the combined memory refusal via `embeddedllm.CheckMemoryBudget` — then a BACKGROUND run), `CancelEmbeddedLLMInstall` (the idempotent stop request for that background run — a READER of the install-run bookkeeping that never claims the operation gate, because claiming would make a cancel click refuse against the very install it is meant to stop; the run releases the gate itself through its defer), `RemoveEmbeddedLLM`, `LoadEmbeddedLLM`, `UnloadEmbeddedLLM`, `SetEmbeddedLLMAutoUnload` and the single-run operation gate plus the stop budget every stop-shaped path hands core (`beginEmbeddedOperation`/`endEmbeddedOperation`/`embeddedBusyRefusal`, the install-run bookkeeping `embeddedBeginInstallRun`/`embeddedEndInstallRun` that publishes the run's cancellable context and its operator-request flag, `embeddedStopTimeout`/`embeddedStopBudget`). The tuning and probe RPCs, and every wire shape, moved out of this file into the two siblings below
- `backend/frontend_api_embedded_dto.go` — the wire shapes of this RPC surface and the pure mappers that build them: boundary data ONLY (every DTO that crosses the Wails binding, its request counterpart, and the functions that map a core or config value onto it — no policy, no state, no I/O), ADDITIVE at this boundary so a payload from a newer backend still validates against a renderer that has not caught up: the DTOs (`EmbeddedLLMStatus` — including the `packing_reason` / `gpu_family` / `guards` degradation record, the recorded device topology (`devices`/`unified`/`host_ram_gib`/both budgets/`topology_probed_at`), the effective launch plan (`EmbeddedLLMPlan` with `Recorded` and `Notes`) and `reload_required` (plus the additive `fit_warning`, the surfaced fit-contract complaint of the last failed launch) —, `EmbeddedLLMGuard`, `EmbeddedLLMDevice`, `EmbeddedLLMDevicesDTO`, the nullable tuning mirrors `EmbeddedLLMTuningDTO`/`EmbeddedLLMTuningRequest` (with their context/offload sub-DTOs), `EmbeddedLLMStateData`, `EmbeddedLLMProgressData`), plus the pointer-copy helpers (`copyStringPtr`/`copyIntPtr`/`copyBoolPtr`/`copyFloat64Ptr`) that keep a nullable knob nullable — "unset, the planner decides" and "explicitly set to the planner's own answer" are different operator statements and must not collapse into one
- `backend/frontend_api_embedded_tuning.go` — the operator tuning surface: it owns the OVERRIDE, not the outcome (the resolution that turns these knobs into a launch shape, and the guards that can refuse one, all live in `core/embeddedllm`): `GetEmbeddedLLMTuning` (the tuning getter — returns an error only before startup, where the all-nil fail-soft answer would be indistinguishable from "the operator overrode nothing" and an editor would wipe the real tuning on Save), `SetEmbeddedLLMTuning` (the PARTIAL tuning patch: nil keeps, present replaces, `reset` clears; validated through the same `ToTuning` translation a config load runs, refused without a write, rolled back on a failed persist, no-op writes nothing, and never restarts a resident model — it sets `reload_required` instead), `ProbeEmbeddedLLMDevices` (the on-demand re-measurement; the one embedded-LLM read allowed to be slow and to fail, converting core's fail-soft probe silence into an actionable error — and reachable from Go-side diagnostics only, since no UI affordance consumes it and the frontend `@/api/embeddedTuning` wrapper was deliberately not written), and the tuning bookkeeping those RPCs lean on (`applyEmbeddedTuningRequest` — the pure fold, `embeddedTuningFingerprint` — compared over the translated planner vocabulary so equivalent respellings do not raise a spurious reload flag, `noteEmbeddedLaunchTuning` — records on `loading` what the argv was actually built from, cleared on every non-resident transition, and `embeddedTuningReloadRequired` — whether a resident model was launched with overrides the operator has since changed, i.e. whether the persisted tuning only takes effect on the NEXT load, answering false on a missing fingerprint so "unknown" never surfaces as a demand to reload)
- `backend/frontend_api_embedded.go` — the rest of the wiring, in the same file as the RPCs above: the production `ConfigSink` (`embeddedConfigSink`: the reference state mutation plus the atomic save-or-rollback, `config:updated` and the judge/router rebuild); the event emitters (`emitEmbeddedLLMState`, `emitEmbeddedInstallProgress`, `emitEmbeddedRuntimeError`); the lazily built, two-mutex subsystem state (`embeddedLLMState` on `FrontendAPI.embedded`, which also owns the manifest snapshot and the state-event mute that keeps one operation to one event); and the lifecycle hooks `FrontendAPILifecycle.InitEmbeddedLLM` / `StopEmbeddedLLM` (never Wails-bound); the pre-spawn port check `embeddedEnsurePort` (wired onto `Server.EnsurePort` by `embeddedBuild`) with `persistEmbeddedPort`, which writes a moved port to `embedded_llm.port` and regenerates the provider record — best-effort, since the transport redirect keeps the model usable even when the config write fails; the post-ready context readback's config half `persistEmbeddedContext` (wired onto `Server.PersistContext`), which writes the value the server reported to the tier-1 `llm.models` `context_window` override, mirrors it into the cached install record, and pushes the corrected window into the live sessions' emitters (`pushDisplayContextWindow` → `session.Manager.SetDisplayContextWindowForModel` — the display basis of the status bar and the compaction cards) — likewise best-effort, and likewise without a router rebuild, because it runs inside `Load`; and `embeddedTuning` (wired onto `Server.Tuning`), which translates the live `embedded_llm.tuning` for a launch and fails soft to the all-Auto plan; and the router-side seam that makes a cold request load the model — `toBuilderConfigLocked` (the single wrapper every production `ToBuilderConfig` call site goes through), `applyEmbeddedLoader`, `syncEmbeddedBuilderSeam` (the builder-level default that also reaches per-session routers), `embeddedLoaderRef`, `rebuildRouterForEmbeddedTransport`, and the `embeddedLLMState.loader` atomic snapshot the injection reads without taking `st.mu`; plus the pre-dispatch gate for short-budget callers — `serviceEmbeddedGate` (agent-idle deference first — while an agent task runs, ANY service request would evict that task's prompt cache and cost its next step a fresh multi-minute prefill, so the gate waits within a caller-shaped budget: `embeddedServiceIdleWaitInteractive` 20 s for the RPC callers `serviceEmbeddedGateInteractive`, ending in an actionable refusal, `embeddedServiceIdleWaitBackground` 10 min for the manager's title generation via `serviceEmbeddedGateBackground`, which already skips on a gate failure — then `ensureEmbeddedReadyForLLMRequest` and its `activeModelIsEmbedded` predicate; the idle wait reads the `FrontendAPI.activeSessionCount` seam wired in `installServiceLLMGate`)
- `backend/frontend_api_config.go` — **protects** the generated `llm.openai_compatible.embedded` entry: `UpdateLLMConfig` re-injects it from the authoritative state after building the candidate, because a non-nil `openai_compatible` request replaces the whole map; also maps the model to the `qwen3.8-27b` profile hint in `suggestModelProfileID`
- `backend/events.go` — the two global event-name constants `EventEmbeddedLLMInstallProgress` (`embedded_llm:install_progress`) and `EventEmbeddedLLMState` (`embedded_llm:state`); a FATAL background install failure additionally reuses the existing `EventRuntimeError` toast with `error_code: "embedded_llm_install_failed"` and an operator-friendly text (resumable download failures never toast — core retries them silently), and a removal failure keeps `error_code: "embedded_llm_remove_failed"`
- `core/builder.go` — `providerEntryFromConfig` attaches the ensure-loaded client through the existing `llm.ProviderEntry.HTTPClient` hook (the same mechanism `core/llmtls.RouterEntryClient` uses): the pin resolver runs first and `embeddedllm.EnsureLoadedClient` only decorates its answer, for the one entry the seam's `ProviderName` names. The same guard is the only place that sets `llm.ProviderEntry.ReasoningWire` (to `ReasoningWireChatTemplateKwargs`, the spelling the pinned fork parses — see [Reasoning effort and the family resolution](#reasoning-effort-and-the-family-resolution)); every other `openai_compatible` entry keeps the vendor default. `SetEmbeddedLLM` holds the builder-level default seam and `embeddedSeam` resolves it inside `buildRouter` — the one place every router is constructed, which is what covers the per-session router the session factory builds from a plain `ToBuilderConfig`. The seam is injected by the backend, not by `ToBuilderConfig` — see [Request path](#invariants)
- `core/builderconfig.go` — `BuilderEmbeddedLLMConfig` (`ProviderName` + `Loader` + `LoadWaitTimeout`), the injection seam for the supervisor instance core does not own; the zero value guards nothing
- `backend/frontend_api_prompt.go`, `backend/frontend_api_git.go`, `backend/session/manager_execution.go` — the three short-budget LLM callers, each of which gates BEFORE arming its own timeout: `OptimizePrompt` and `GenerateCommitMessage` call `ensureEmbeddedReadyForLLMRequest` ahead of `serviceLLMTimeout`, and the session manager's title generation runs `serviceLLMGate` inside `maybeSpawnTitleGeneration` ahead of its own `context.WithTimeout`. The gate reaches the manager through `Manager.SetServiceLLMGate` (`backend/session/manager.go`), wired once by `installServiceLLMGate` (`backend/frontend_api.go`) because the manager is built inside `NewApplication`, before any `FrontendAPI` exists; the builder-level seam travels the same route, as `SetEmbeddedLLM` on `appBuilder` (`backend/builder_iface.go`)
- `core/pathsegments.go` — cross-layer path segment constants, including `EmbeddedRuntimesRelativePath` (`runtimes`) and `EmbeddedModelRelativePath` (`models/bonsai-2-27b`)
- `desktop/startup_phases.go` — `(*App).initEmbeddedLLM` (Phase 5, right after `buildFrontendAPI`: one manifest read plus one snapshot event, so it stays far below the 50ms critical-phase budget) and `(*App).stopEmbeddedLLM` (called early in `Shutdown`, before the judge drain, so the gigabytes are released while the rest of the teardown still runs; a failure is logged, never fatal). Startup restores state from the manifest only — no download, no probe, no auto-load
- `frontend/src/api/embedded.ts` — the subsystem's ONLY path to the generated bindings: the validating RPC wrappers (`getEmbeddedLLMStatus` + `isEmbeddedLLMStatus`, `installEmbeddedLLM`, `cancelEmbeddedLLMInstall` (whose busy window in the store covers only the request + read-back — the cancelled run's real, quiet end arrives later through `embedded_llm:state`), `removeEmbeddedLLM`, `loadEmbeddedLLM`, `unloadEmbeddedLLM`, `setEmbeddedLLMAutoUnload` with its local `MIN_AUTO_UNLOAD_MINUTES` refusal), the typed event subscriptions (`onEmbeddedLLMState`, `onEmbeddedLLMInstallProgress` — a malformed payload is reported, never silently dropped), the `EmbeddedLLMStatus` mirror of the backend DTO and `DEFAULT_AUTO_UNLOAD_MINUTES`
- `frontend/src/stores/embeddedLLMStore.ts` — the UI state both surfaces share (see [frontend/stores.md](frontend/stores.md)), pure state + reducers + selectors: the authoritative `status` snapshot (one writer, `setStatus`), `installing`, the per-component `progress` map, the mutating-RPC `busy` action (named by the FIRST action of an overlapping set and cleared only when the last one has read back — `runEmbeddedLLMAction` is the sole owner; the action union includes `cancel`, whose window covers only the request + read-back because the cancelled install run's real end arrives later through `embedded_llm:state`) and the last failed action's `error`
- `frontend/src/stores/embeddedLLMSync.ts` — the module-level backend sync, split out of the store so that file stays free of I/O: `refreshEmbeddedLLMStatus` (never throws), `refreshEmbeddedLLMTuning` (with its counted tuning-read window), `runEmbeddedLLMAction` (with its counted busy window) and the refcounted `subscribeEmbeddedLLMEvents` (with its refcount state). BOTH disable windows are COUNTED, not boolean, because `disabled` is a render-time property and cannot cover a control that is already on screen: a Radix menu opened before the first action keeps its portaled items clickable, so one physical click on such an item can fire a SECOND action while the first read-back is still in flight. Each of the two runners therefore keeps a module-level in-flight tally — the busy window is named by the FIRST action of an overlapping set and clears only when that tally reaches zero, and `tuningLoading` closes in a `finally` at zero pending reads, re-arming the flag `setTuning` implicitly cleared while a sibling read is still out — so an overlapping sibling can no longer close the first action's window early and re-enable every control mid-write. Both tallies RESYNC FROM THE STORE FLAG ON ENTRY, which is what keeps an abandoned read or action from wedging a window shut. All four are RE-EXPORTED from `embeddedLLMStore.ts`, so every historical import site keeps working unchanged. `refreshEmbeddedLLMStatus` carries the store's one deliberate cross-store side effect: when `installed` FLIPS it calls `invalidateConfigCache()` on `frontend/src/hooks/useConfigData.ts`, because that hook's module-level cache is the chat toolbar picker's model list and no other embedded path invalidates it
- `frontend/src/components/settings/EmbeddedLLMSettings.tsx` — the Settings block (mounted in `frontend/src/components/settings/LLMSettings.tsx` as the FIRST block of the provider section — directly under the Default Model field and above "+ Add compatible provider"): the state-derived action surface (Install / progress / record + Load-Unload + Remove / auto-unload / tuning), with BOTH the presentation and the flows split out so the block file keeps its baseline length. Presentation leaves live in `frontend/src/components/settings/embedded/` — `EmbeddedLLMActions.tsx` (the Load/Unload + Remove row of an installed model, plus `EmbeddedLLMInstallAction`, the single Install action of a machine with no install yet and its refusal-policy line; both fully controlled — the busy window, the RPCs, the error line and the dialog state all stay in the parent), `EmbeddedLLMProgress.tsx` (the header with its fully controlled Cancel action — a button that only DELIVERS the stop request (`CancelEmbeddedLLMInstall`, disabled while any mutating action's busy window is open), since the run's asynchronous, quiet end arrives through `embedded_llm:state` — plus the per-component rows, worded with the shared `frontend/src/lib/embeddedLLMLabels.ts` order/label/stage vocabulary), `EmbeddedLLMInstallRecord.tsx` (the informational label: the install record plus the measured topology and the effective plan with its notes), `EmbeddedLLMAutoUnload.tsx` (the toggle + minutes draft), `EmbeddedLLMTuning.tsx` (the three primary memory-plan controls), `EmbeddedLLMAdvancedTuning.tsx` (the collapsed Advanced tuning VariantSection — a fully controlled leaf with ZERO local state, holding only the layout), the two per-knob leaves it renders, `TuningNumber.tsx` (ONE numeric knob: the shared `NumberField` plus that knob's Auto affordance — a static "Auto" label while the override is unset, an Auto BUTTON sending `{reset:[knob]}` once it is set) and `TuningTriState.tsx` (the Auto/On/Off Combobox ALL THREE boolean knobs share — Fit, KV-cache offload and the vision projector — sending `{reset:[knob]}` for Auto and the explicit boolean for On/Off, and rendering the caller's quoted-plan clause beside the hint), and `EmbeddedLLMRemoveDialog.tsx` (the removal gate). The flows live in `frontend/src/hooks/`: `useEmbeddedLLMLifecycle.ts` (the mutating install/cancel-install/load/unload/remove actions plus the store subscription — every flow runs through the store's `runEmbeddedLLMAction`, which holds the busy window open across the post-action read-back so a fast second click cannot fire a duplicate RPC, and COUNTS the overlapping actions so a click landing on an already-open menu cannot close that window early; the Remove CONFIRMATION stays in the component as pure UI state), `useEmbeddedLLMAutoUnload.ts` and `useEmbeddedLLMTuning.ts` (the two commit flows: drafts, local refusal, RPC, re-read), with `useEmbeddedLLMAdvancedTuning.ts` split out of the last so each half of the tuning surface stays small — it owns the collapsed-section flag and the value every advanced knob renders — including all three boolean knobs' tri-state spellings and the recorded-plan clauses quoted beside them, while the parent owns the primary controls' drafts and the ONE commit seam both halves write through (`useEmbeddedLLMTuning` still returns `{ primary, advanced }`, so the block and the leaf tests are unaffected by the split). The two count-bearing MODE switches (Context → Exact, layer offload → N layers) keep their drafts in `frontend/src/hooks/useTuningModeDrafts.ts`, extracted from `useEmbeddedLLMTuning` so that hook stays at the derivation and the ONE commit seam: a mode switch persists NOTHING and only raises a draft flag, because the sole count available at that instant is a fallback and committing it would write an override the operator never chose (`-ngl 0` is an all-CPU launch shape). The "what does the control show" arithmetic is pure and React-free in `frontend/src/lib/embeddedTuningDisplay.ts` (the same split as `lib/gitGraphRender` and `lib/embeddedLLMLabels`), so every display rule is unit-testable without a renderer — `boolModeOf` gives every nullable boolean knob its tri-state spelling (`auto` when unset, so no surface paints a fallback-derived ON), `plannedBoolArg(plan, knob)` quotes the recorded plan's own resolution of an unset offload ("on device", or the host-RAM flag the plan actually launched with), and `plannedLayers` answers null for the plan's `-1` omitted-flag sentinel instead of passing it through, so no surface can paint "-1 layers" for a fit-sized launch. The numeric BOUNDS those rules paint against live one level down in `frontend/src/lib/embeddedTuningLimits.ts` — the TS mirror of the Go figures: the floors from `backend/config`'s `ToTuning` validation, the four ceilings from `core/embeddedllm/limits.go` (each carrying the overflow/absurdity rationale its Go constant names) and the documented defaults an unset knob displays. Its own suite, `frontend/src/lib/embeddedTuningLimits.test.ts`, MACHINE-CHECKS that mirror against the Go declarations in BOTH directions: it reads `core/embeddedllm/limits.go`, `memory.go`, `plan.go` and `backend/config/config.go` with `readFileSync` and anchored regexes, EVALUATES the shift literals (`1 << 31`) instead of string-comparing them, and THROWS when an extraction stops matching, so a bump on either side fails the suite rather than drifting silently — following the project's other cross-boundary invariant guards. `MaxAutoUnloadMinutes` is pinned the same way with BOTH sides read from source, because its TS mirror lives in `@/api/embedded`, a browser-facing module this node-env suite must not import. `DEFAULT_FIT_TARGET_MIB` stays a literal: it has no Go constant, 1024 MiB being the upstream runtime's own `-fitt` default that `plan.go`'s `FitTargetMiB` prose documents (zero omits the flag and keeps it) rather than any figure c0wrk sets. They were split OUT of `frontend/src/api/embeddedTuning.ts`, which now holds only the DTOs, the guards and the RPC wrappers, because THREE consumers share them — the wrapper's local range check (TWO predicates: `wholeInRange` for the six knobs whose wire type is an `int` — `context.tokens`, `offload.layers`, `fit_target_mib`, `fit_min_context`, `parallel`, `cache_ram_mib` — and `withinRange` for the one `*float64` knob, `host_reserve_gib`, because a decimal passes every relational comparison and would only be refused by Go's unmarshaller, as a raw driver string in the Settings error line), these display ranges and the inputs' own `min`/`max` attributes — and one copy is what keeps a control from offering a value the guard would refuse. A pin bump changes the Go figure, not this mirror: the wrapper's refusal is only a fast path and the backend stays authoritative. See [Settings block](#settings-block-ui-states)
- `frontend/src/components/layout/EmbeddedModelStatus.tsx` — the status-bar block (mounted in `frontend/src/components/layout/StatusBar.tsx` between the vector-index block and the process-memory indicator): the always-visible counterpart of the Settings block. Like `ProcessMemoryStatus` it owns its LEADING separator and renders nothing at all — separator included — unless it has a state to report; when it does, it renders exactly one compact surface (the active artifact's download bar, an indeterminate weight-load bar, the residency indicator, or an explicit error hint). The surface SELECTION and the exact tooltip words are pure and React-free in `frontend/src/lib/embeddedModelView.ts`, which also owns the block's contract (it returns null when there is nothing to say, so a hidden indicator never leaves a stray divider); the component keeps only the subscription, the memo and the markup (its subscriptions include the ACTIVE session's cached token info — chatStore `sessionTokens` read through sessionStore's active id with stable selectors returning direct store references — the input of the gated throughput metric; see [Status-bar indicator](#status-bar-indicator-ui-states)). The compact bar itself is `frontend/src/components/layout/EmbeddedModelProgressBar.tsx` — 64 layout-px wide so the block's width stays stable while the percent changes, with `percent === null` rendering an indeterminate pulse because a byte-less stage (verifying / extracting / signing) and a weight load both report no fraction and a percentage there would be a lie. Its component/stage words come from `frontend/src/lib/embeddedLLMLabels.ts`, shared with `EmbeddedLLMProgress.tsx` so the two surfaces describing the same payload cannot drift. See [Status-bar indicator](#status-bar-indicator-ui-states)
- `frontend/src/components/ui/ModelPickerMenu.tsx` — the single model-picker implementation both model lists render (the Settings → LLM default-model picker through `allEnabledModels`, and the chat toolbar's `ModelCombobox` through `useConfigData`). Its `providerLabel` humanizes the backend-owned `embedded` config key to **Embedded**, while the value sent to the backend stays the composite `embedded/Bonsai 2 27B`. Its `groupByProvider` additionally hoists the `embedded` group to the TOP of the group list, because neither feed order puts it there: the settings list iterates a provider map whose JSON keys Go alphabetizes, and `all_models` arrives as (anthropic, chatgpt, sorted `openai_compatible`, sorted `anthropic_compatible`). Normalizing in the shared component is what keeps the two surfaces from drifting apart; every other provider keeps its input order
- `frontend/src/types/events.ts` (`EmbeddedLLMInstallProgressData` / `EmbeddedLLMStateData` plus the `EmbeddedLLMComponent`/`EmbeddedLLMStage` unions, their `GlobalEventMap` entries and the `isEmbeddedLLMInstallProgressData`/`isEmbeddedLLMStateData` guards — the state guard deliberately does NOT enumerate `packing`/`backend`, so a newly pinned backend cannot make the event fail validation)
- `frontend/src/lib/embeddedColdLoadHint.ts` + `frontend/src/hooks/useServiceCallTitle.ts` — the ONE wording and the ONE gate for the long-wait clause of the pending service-call tooltips (`Optimize prompt` in `frontend/src/components/chat/ChatInputToolbar.tsx`, `Generate commit message` in `frontend/src/components/GitPanel/CommitSection.tsx`). The clause "a cold embedded local model loads first, which can take minutes" is CONDITIONAL: `isColdEmbeddedTarget` answers true only when the effective model IS the embedded one AND it is not resident, because the backend's `ensureEmbeddedReadyForLLMRequest` gate is a no-op for every other provider — telling a user on a remote provider that a multi-gigabyte local model is loading is the wrong diagnosis for an ordinary sub-second call. The gate is React-free and pure (the same split as `lib/embeddedModelView`); the hook's parameter is the EFFECTIVE model id — the model the service RPC actually runs on — and BOTH call sites pass `null`, meaning "the configured default", which the hook resolves from the shared config cache (`llm.default_model`). That is the honest argument, not a lazy one: `OptimizePrompt(prompt string)` and `GenerateCommitMessage` take no model parameter and the backend's `activeModelIsEmbedded` resolves the default, so gating the hint on a chat surface's per-message `selectedModel` override mis-warned (a cold embedded pick beside a remote default) and mis-silenced (the reverse) — a pass a real id is correct ONLY if a service RPC ever grows a model parameter
- `frontend/src/test/embeddedStatusFixture.ts`, `frontend/src/test/embeddedTuningFixture.ts`, `frontend/src/test/tuningTestHarness.tsx` and `frontend/src/test/TuningHarnessSection.tsx` — the shared test support, all four under `frontend/src/test/`: a complete `EmbeddedLLMStatus` snapshot (so a DTO addition is fixed in one place rather than in six suites), the all-unset `EmbeddedLLMTuning` override surface plus the fold the backend performs on a patch (so a committed patch round-trips through the mocked re-read exactly as the backend would), and the harness both tuning section suites drive through the REAL `useEmbeddedLLMTuning` hook and the REAL store with the RPC boundary mocked at `@/api/embeddedTuning` — its `touchField`/`retypeField`/`emptyField`/`enterField`/`blurField` helpers are the only executable specification of what counts as a real user interaction for the shared `NumberField`, and they pin its edit-dirty-vs-value-comparison contract, its empty-draft-means-no-value rule and its one-gesture-one-persist Enter semantics. The last two helpers are the pair that expresses the REAL Enter-then-click-away gesture: `blurField` dispatches ONLY `focusout`, with no preceding `focusin`, because `touchField` is focus+blur and its `focusin` runs `NumberField.onFocus`, which re-seeds the draft and clears the dirty flag — a test built on `touchField` would have the flag consumed by the re-focus instead of by the commit, and would keep passing with the commit's flag reset deleted. `TuningHarnessSection.tsx` is the section that harness mounts — the real `EmbeddedLLMTuning` / `EmbeddedLLMAdvancedTuning` behind the real hook — and lives in its own file because eslint-plugin-react-refresh requires a module that declares a component to export nothing else, while the harness exports its helpers

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
	PackingPTQ1_0 Packing = "PTQ1_0" // 5,946,648,928 B — the downgrade packing
)

// PackingReason is WHY that packing was chosen. Recorded in the Resolution, the
// Manifest and the status DTO: a downgrade that does not say why is invisible.
type PackingReason string

const (
	PackingReasonDefault              PackingReason = "default"
	PackingReasonNoPQ2_0Kernels       PackingReason = "no_pq2_0_kernels"      // Vulkan
	PackingReasonAVX512PQ2_0Segfault  PackingReason = "avx512_pq2_0_segfault" // pin predates #245
	PackingReasonPQ2_0DoesNotFit      PackingReason = "pq2_0_does_not_fit"    // measured budget
	PackingReasonGPUGenerationDecode  PackingReason = "gpu_generation_decode" // Ada / L4
	PackingReasonOperatorOverride     PackingReason = "operator_override"     // Tuning.Packing
)

// GPUFamily is the accelerator GENERATION a device description names, classified
// from the runtime's own `--list-devices` output (never from the backend: the
// backend says which archive was downloaded, the description says which silicon
// answered). Two consumers read it — the packing rule (which generation decodes
// which ternary packing faster) and the compatibility guards (which parts
// upstream documents as broken) — plus the memory planner, which prices the
// uncertainty of a device/host split nobody measured.
type GPUFamily string

// "" (unknown) | apple-silicon | nvidia-ada | nvidia-blackwell | nvidia-hopper
// | nvidia-ampere | amd-rdna2 | amd-rdna3 | amd-gfx1151 | intel-arc
// Unknown means "nothing to decide", never "no GPU": it fires no GPU-specific
// guard, keeps the default packing and pays the largest memory allowance.

// HostCaps is the CPU-side capability set the packing decision reads. Tri-state
// per feature, because "we did not look" must not read as "the CPU lacks it".
type HostCaps struct {
	AVX512 CPUFeature // CPUFeatureUnknown (zero) | CPUFeatureAbsent | CPUFeaturePresent
}

// BudgetFit is the fit gate's verdict on one packing. Tri-state for the same
// reason: a bool's zero value would assert "does not fit" for every caller that
// measured nothing, downgrading the packing on machines that were never probed.
type BudgetFit int // FitUnknown (zero) | FitSufficient | FitInsufficient

// GuardDecision is one backend-compatibility verdict: what fired, what it asks
// for, why (typed), how bad, which upstream report says so, whether c0wrk acted
// on it, and the sentence a user reads. JSON-serializable, because it is
// persisted in the Manifest and replayed by the status DTO.
type GuardDecision struct {
	Guard    GuardID         // stable id, e.g. "cuda-13.3-crash"
	Action   GuardAction     // prefer_backend | prefer_packing | advisory
	Reason   GuardReason     // crash_on_load | process_abort | garbled_output | hang | fails_to_start
	Severity GuardSeverity   // critical | warning — DERIVED from Reason, never hand-set
	Issue    string          // "<repo>#<number>", e.g. "PrismML-Eng/llama.cpp#222"
	Applied  bool            // set by the consumer: did the plan actually change?
	Backend  Backend         // prefer_backend only
	Packing  Packing         // prefer_packing only
	Guidance string          // user-facing: what is documented, what c0wrk did
}

// Hardware is the probe result: everything resolution may depend on.
// It carries NO device-memory field — accelerator memory is a second, later
// probe (MemoryTopology) because the runtime binary does not exist yet here.
type Hardware struct {
	Platform string  // toolmanager.Platform() shape: "<goos>-<goarch>"
	Arch     string  // "amd64" | "arm64"
	RAMGiB   float64 // total system RAM, in GiB (bytes / 2^30) — not GB
	Backend  Backend // probed accelerator (BackendCPU when nothing else is found)
	CUDATag  string  // driver-derived CUDA asset tag ("" when not CUDA)
}

// DeviceMemory is one accelerator as the RUNTIME reports it (not as the OS
// does), from `llama-server --list-devices`.
type DeviceMemory struct {
	Name        string `json:"name"`        // "MTL0" | "CUDA0" | "HIP0" | "Vulkan0"
	Description string `json:"description"` // "Apple M4 Max", "NVIDIA GeForce RTX 4090"
	TotalMiB    int64  `json:"total_mib"`
	FreeMiB     int64  `json:"free_mib"` // a snapshot; informational, never a budget input
}

// MemoryTopology is the post-install probe result, and the only description of
// accelerator memory the subsystem has. JSON-serializable so an install can
// record it beside the Manifest and a support bundle can carry it.
type MemoryTopology struct {
	Devices           []DeviceMemory `json:"devices"` // printed order; 0/0 entries dropped; deduped by name
	HostRAMGiB        float64        `json:"host_ram_gib"`
	Unified           bool           `json:"unified"` // device memory IS host RAM — never add the two
	DeviceBudgetBytes int64          `json:"device_budget_bytes"`
	HostBudgetBytes   int64          `json:"host_budget_bytes"`
	ProbedAt          string         `json:"probed_at"` // RFC 3339 UTC
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

// Resolution is the pure output of a ResolveInput.
type Resolution struct {
	Assets         []Asset // 1 runtime, or 2 on Windows CUDA (runtime + cudart)
	Backend        Backend // what the assets were ACTUALLY resolved for, post-degradation
	Packing        Packing
	Layers         int // -ngl: mirrors Memory.Layers; 0 under a fit-sized plan
	ContextSize    int // -c: mirrors Memory.ContextSize; 0 under a fit-sized plan
	ImageMaxTokens int // --image-max-tokens: 1024 on metal/vulkan/cpu, 0 = uncapped on CUDA/ROCm
	NeedsCudart    bool
	Memory         MemoryPlan // the resolved launch shape; Layers/ContextSize/Packing above are projections of it
}

// ResolveInput is the whole resolution input. MachineProfile alone is the
// DERIVED-budget view: the host budget comes from the RAM probe and the
// accelerator axis is classified statically (see deviceAxisState). Topology is
// what turns both into measured ones.
type ResolveInput struct {
	MachineProfile          // Platform, Backend, RAMGiB, GPU, Host, FitsPQ2_0
	Topology       *MemoryTopology // nil = "not probed" (NOT the zero MemoryTopology, which means "unknown")
	Tuning         Tuning          // zero value = the all-Auto plan
}

func Resolve(in ResolveInput) (Resolution, error)          // the whole plan
func ResolveProfile(p MachineProfile) (Resolution, error)  // no topology, no tuning
func ResolveMachine(platform string, b Backend, ramGiB float64) (Resolution, error) // three-input form

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
	RuntimeTag    = "prism-b10735-842b188"                     // pinned fork release
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
	// ContextSize is the LAST KNOWN EFFECTIVE context, not a tier frozen at
	// install: install writes the planner's figure (a fit-sized plan records its
	// FitMinContext floor, never 0) and every successful load overwrites it with
	// the value the server itself reported through /props. It is what the tier-1
	// llm.models context_window override is generated from.
	ContextSize int    `json:"context_size"`
	ModelFile   string `json:"model_file"`
	InstalledAt string `json:"installed_at"` // RFC 3339

	PackingReason PackingReason   `json:"packing_reason,omitempty"`
	GPUFamily     GPUFamily       `json:"gpu_family,omitempty"`
	Guards        []GuardDecision `json:"guards,omitempty"`

	// Topology is the device-memory snapshot the recorded plan was made from.
	// NIL means "no probe ever answered" and a reader must treat it as UNKNOWN,
	// never as "no accelerator" — a zero budget read as a fact is the refusal the
	// combined gate exists to avoid. A load's fail-soft path falls back to it.
	Topology *MemoryTopology `json:"topology,omitempty"`
	// Plan is the launch shape LAST APPLIED. NIL means a pre-plan manifest, and
	// a load then derives its shape from the pure policy in resolve.go instead.
	// Persisted rather than re-derived because re-deriving would need the
	// operator's Tuning, which is a SETTING the sink carries verbatim — a copy in
	// a record would be a second source of truth, stale on the first edit.
	Plan *MemoryPlan `json:"plan,omitempty"`
}

// recordableContext is the context a manifest may carry for a plan: the
// planner's own value for a computed shape, and FitMinContext — the floor fit is
// held to — for a fit-sized one, which by definition has no concrete context.
// Never 0: a 0 override means "leave the existing one alone" to
// SyncEmbeddedLLMProvider, and a 0 in a manifest reads as a lost tier.
func recordableContext(plan MemoryPlan, fallback int) int

// Progress is one per-component download/verify/extract update.
type Progress struct {
	Component  Component
	Stage      string // "downloading" | "verifying" | "extracting" | "signing" | "done"
	BytesDone  int64
	BytesTotal int64
}

// ErrInsufficientMemory is the typed refusal of the combined gate: no shape
// memory.go has measured — neither residency extreme, nor any packing, nor any
// modelled KV precision — fits this machine's memory.
var ErrInsufficientMemory = errors.New("the embedded LLM does not fit this machine's memory")

// InsufficientMemoryError is that refusal WITH the arithmetic in it. Its Error()
// names BOTH pools and BOTH numbers, in GiB, plus the reserve the host figure
// was derived with, the installed total and the smallest shape that was priced.
// It unwraps to a CHAIN: ErrInsufficientMemory always, ErrMemoryPlanInfeasible
// always (the two name one fact at two layers), and the deprecated
// ErrInsufficientRAM only when the HOST pool overflowed — so a refusal caused by
// a small accelerator never reports itself as insufficient system RAM.
type InsufficientMemoryError struct {
	Packing, KVType, Context, Offloaded // the shape the numbers belong to
	DeviceNeedMiB, DeviceHaveMiB int64
	Device                       deviceAxisState
	HostNeedMiB, HostHaveMiB, HostReserveMiB int64
	HostRAMGiB                               float64
	Unified                                  bool
}

// deviceAxisState is the TRI-STATE accelerator axis: absent (nothing to offload
// to — a refusal), known (a budget this gate may spend), unreadable (an
// accelerator with independent memory nobody measured — NOT a refusal; the gate
// degrades to the host pool and says so in Notes). Tri-state for the same reason
// BudgetFit and CPUFeature are.
type deviceAxisState int // deviceAbsent (zero) | deviceKnown | deviceUnreadable

// ErrInsufficientRAM is DEPRECATED: ADR-067 D6's flat floor is gone. Kept so a
// caller that only cares about the host pool can still match it — every
// *InsufficientMemoryError whose host budget overflowed unwraps to it.
var ErrInsufficientRAM = errors.New("insufficient system RAM for the embedded LLM")

// ErrRAMUnknown is returned when total system RAM cannot be read. The gate is a
// safety gate and derives its HOST budget from this figure on every path, so an
// unreadable size refuses the install instead of assuming the machine is big
// enough. An unreadable DEVICE budget is the opposite case — see deviceAxisState.
var ErrRAMUnknown = errors.New("cannot determine total system RAM")

// DefaultHostReserveGiB is the reserve derivation exposed so a caller with no
// topology and one with a topology cannot disagree about it: max(4 GiB, 1/8 of
// RAM), covering the OS and window server, c0wrk itself, and the vector index.
func DefaultHostReserveGiB(ramGiB float64) float64

// CheckMemoryBudget runs the gate on its own — no assets, no launch shape. It is
// what the desktop RPC calls so a click and the background install answer with
// one voice.
func CheckMemoryBudget(in ResolveInput) error

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
	ProbeDevices func(ctx context.Context, binaryPath string, logger *slog.Logger) (MemoryTopology, bool)
	RunCommand   CommandRunner
	AllocatePort func(ctx context.Context) (int, error)
	Stop         func(ctx context.Context) error // nil = no supervisor wired yet
	// StopTimeout bounds Install's STEP-0 Stop only, gate wait included
	// (Remove's stop rides on the ctx its caller passes). 0 → inherit the
	// caller's ctx. NOT Server.StopTimeout, which is the graceful
	// signal→kill window inside terminate.
	StopTimeout  time.Duration
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
// install record plus the pure launch policy in resolve.go, or — for a
// memory-aware launch — a MemoryPlan through ApplyMemoryPlan. Args renders the
// command line and Validate is the last gate before exec.
//
// There is no free-form flag or argument string here. Every argv element comes
// from a typed field, so nothing an operator or a config file says can become
// part of the command line (SECURITY.md, ASI05); the four string-typed fields
// that do reach argv are each pinned by Validate — Host to LoopbackHost, KVType
// and SplitMode to closed enums, Devices token by token — and the three path
// fields come from the install record and the layout, never from config.
type LaunchSpec struct {
	ServerBinary string        // the exec target; NOT an argv element
	ModelFile    string        // -m
	MMProjFile   string        // --mmproj; empty omits the flag (text-only serving)
	Host         string        // --host, always LoopbackHost
	Port         int           // --port, the persisted loopback port
	Layers       LayerMode     // -ngl; LayerAuto OMITS the element entirely
	ContextSize  int           // -c; 0 = "fit sizes it", valid only with Fit
	ImageMaxTokens int         // --image-max-tokens; ImageMaxTokensUncapped omits it

	Fit           bool         // -fit on|off, rendered UNCONDITIONALLY (see below)
	FitTargetMiB  int          // -fitt; 0 omits it; rendered only under Fit
	FitMinContext int          // -fitc; required > 0 under Fit, omitted otherwise
	KVType        KVType       // -ctk/-ctv (one precision for both); "" omits both
	KVOffload     *bool        // nil omits; an explicit false renders -nkvo
	MMProjOffload *bool        // nil omits; an explicit false renders --no-mmproj-offload
	Parallel      int          // -np, rendered UNCONDITIONALLY; Validate requires 1..MaxTuningParallel
	CacheRAMMiB   *int         // --cache-ram; nil omits, 0 disables, -1 is "no limit"
	Devices       []string     // -dev, comma-joined into ONE element; empty omits it
	SplitMode     SplitMode    // -sm; SplitModeAuto omits it
}

// LayerMode is the `-ngl` half of a spec, and it has four answers rather than
// three because one of them is the ABSENCE of an answer. The discriminator and
// the count are private, so only these four are constructible.
func LayerAuto() LayerMode     // OMIT the flag — the only shape a fit-sized launch may carry
func LayerAll() LayerMode      // -ngl all
func LayerCPU() LayerMode      // -ngl 0
func LayerCount(n int) LayerMode // -ngl n (negative is representable and refused by Validate)
func (m LayerMode) EmitsFlag() bool // the LayerMode spelling of MemoryPlan.EmitsLayers
func (m LayerMode) String() string  // diagnostics only; never an argv element

// ApplyMemoryPlan is the one bridge between the pure planner and the command
// line. It copies rather than mutates, and it is total: every MemoryPlan field
// that names a runtime flag is carried across, so a plan cannot be half-applied
// and a new planner knob cannot be silently dropped on the way to argv
// (TestApplyMemoryPlanRendersEveryFlagBearingField ratchets this by reflection).
// It does not call Validate.
func (spec LaunchSpec) ApplyMemoryPlan(plan MemoryPlan) LaunchSpec

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
func (s *Server) IdleRemaining() (time.Duration, bool) // ok == false: no timer armed.
                                                       // While an expiry is DEFERRED for an
                                                       // in-flight request the answer is the
                                                       // grace left, not the budget left —
                                                       // the budget is already spent.
func (s *Server) LastActivity() time.Time

// In-flight request tracking: the transport brackets each request it carries so
// an idle expiry can defer itself instead of stopping a server mid-generation.
func (s *Server) BeginRequest()
func (s *Server) EndRequest()      // idempotent at zero
func (s *Server) InFlightRequests() int64

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
// actually bound. *Server satisfies it as-is (Port), and so does production's
// embeddedLoaderRef — both pinned by assertions, because a capability discovered
// by type assertion on the Loader value is invisible at the wiring site. The
// redirect is therefore a live control, not a hypothetical.
//
// It exists because the request URL is not authoritative — the router builds it
// from the provider base_url, which is derived from the PERSISTED port, and the
// load re-checks that port and may move it. Declaring it optional (rather than
// growing Loader) keeps every other Loader valid: one that reports no port simply
// gets no redirect.
type PortSource interface {
	Port() int // 0 = no server has been started, which is not a port to aim at
}

// RequestTracker is an OPTIONAL Loader capability: the count of requests the
// transport currently has open against the model. *Server satisfies it as-is,
// and so does production's embeddedLoaderRef — both pinned by assertions, so the
// mid-generation deferral is a live control rather than a hypothetical.
//
// It exists because activity is stamped on COMPLETION (Loader.MarkActivity),
// which cannot protect a single generation that outlives the whole idle budget:
// the timer would fire mid-answer and stop the server out from under the request
// that is using it. Bracketing the exchange with a count lets the idle path DEFER
// that unload instead. Declaring it optional — rather than growing Loader — keeps
// every existing double and test loader valid: one that does not track requests
// simply gets the completion stamp alone, which is the pre-existing behaviour.
type RequestTracker interface {
	BeginRequest() // records that a request is now in flight
	EndRequest()   // releases one in-flight request; idempotent at zero
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

// KVType is the precision of the K and V KV caches — the value passed to BOTH
// --cache-type-k and --cache-type-v. One type covers both on purpose: a MIXED
// K/V cache silently runs flash attention on the CPU (PrismML-Eng/llama.cpp#267),
// so the mixed shape is unrepresentable.
//
// The set is CLOSED at three. q5_0 is excluded on performance
// (PrismML-Eng/llama.cpp#191: pp 11.4 / tg 4.0 t/s against f16's pp 343.5 /
// tg 31.8 on the same hardware, model and prompt, reproduced after a reboot, and
// it buys no capacity — a 73728 context ceiling against q4_0's 72960);
// q4_1 / iq4_nl / q5_1 are excluded as unverified on this model.
type KVType string

const (
	KVTypeF16  KVType = "f16"  // lossless baseline
	KVTypeQ8_0 KVType = "q8_0" // same throughput as f16 in #191
	KVTypeQ4_0 KVType = "q4_0" // the fork's own long-context option (BONSAI_KV4)
)

func KVTypes() []KVType                                   // the three, in order; a copy
func ParseKVType(s string) (KVType, error)                // ErrKVTypeUnsupported, never a coercion
func (t KVType) Valid() bool

// ModelMemoryProfile is the measured footprint of Ternary-Bonsai-2-27B, term by
// term, in MiB. It is a DESCRIPTION, not a policy: it decides nothing. Every
// field's doc comment in memory.go names the measurement and its date.
type ModelMemoryProfile struct {
	WeightsMiB           map[Packing]int64   // on-disk; DERIVED from registry Asset.SizeBytes, never re-typed
	MMProjMiB            int64               // on-disk projector; derived the same way
	DeviceWeightsMiB     map[Packing]int64   // accelerator residency at full offload
	HostWeightsMiB       map[Packing]int64   // system-RAM residency at -ngl 0
	HostWeightSpillMiB   map[Packing]int64   // the un-repackable spill that stays in RAM even at full offload
	RecurrentStateMiB    int64               // the SSM/GDN state: context- AND cache-type-independent
	ComputeDeviceMiB     int64               // accelerator compute reserve, f16 reference shape
	KVQuantComputeExtraMiB int64             // extra device compute a quantised cache needs
	ComputeHostMiB       int64               // system-RAM compute reserve at full offload
	ComputeHostCPUOnlyMiB int64              // the same at -ngl 0, where the whole graph is on the CPU
	MMProjReserveDeviceMiB int64             // the projector's worst-case device reserve (not in the projections)
	MMProjReserveHostMiB int64               // the projector's worst-case host reserve (not in the projections)
	KVBytesPerTokenF16   int64               // exactly 64 KiB on this model
	KVValuesPerLayerToken int64              // n_embd_k_gqa + n_embd_v_gqa
	KVDivisor            map[KVType]float64  // f16 1, q8_0 32/17, q4_0 32/9
	MaxContext           int                 // the model's training context (a ceiling, never a -c value)
	LayerCount           int
	OffloadableLayers    int                 // LayerCount + 1
	FullAttentionLayers  int                 // 16 of 64: the rest are recurrent
}

func PinnedMemoryProfile() (ModelMemoryProfile, error) // fails closed on an unpinned packing
func (p ModelMemoryProfile) KVCacheMiB(ctx int, kv KVType) (int64, error)
func (p ModelMemoryProfile) ProjectDeviceMiB(packing Packing, ctx int, kv KVType, offloaded bool) (int64, error)
func (p ModelMemoryProfile) ProjectHostMiB(packing Packing, ctx int, kv KVType, offloaded bool) (int64, error)

var ErrKVTypeUnsupported = errors.New("unsupported embedded-LLM KV cache type")
var ErrMemoryNotMeasured = errors.New("no measured memory residency for this packing")
var ErrContextOutOfRange = errors.New("context size outside the modelled range")

// ── plan.go: the decision layer between the two measured halves above ──

// Tuning is the operator override vocabulary. EVERY field distinguishes "unset"
// from "set to the default value", because the difference is load-bearing: an
// unset -ngl lets the runtime's fit pass size the launch, while an -ngl that
// happens to equal the value fit would have chosen turns fit OFF (see the
// exclusivity rule in Flow). Fields with a natural zero sentinel use it; the
// rest are pointers.
type Tuning struct {
	Context        ContextTuning // ContextAuto (zero) | ContextExact + Tokens
	KVType         KVType        // zero = Auto: escalate f16 → q8_0 → q4_0
	Offload        Offload       // OffloadAuto (zero) | OffloadAll | OffloadCPU | OffloadLayers + Layers
	FitEnabled     *bool         // -fit; nil lets the exclusivity rule decide
	FitTargetMiB   *int          // -fitt; nil/0 omits the flag (runtime default 1024)
	FitMinContext  *int          // -fitc; nil = DefaultFitMinContext (65536, NOT the runtime's 4096)
	KVOffload      *bool         // -kvo/-nkvo; false keeps the KV cache in system RAM
	MMProjOffload  *bool         // --mmproj-offload/--no-mmproj-offload
	Packing        Packing       // zero = the backend/GPU-derived packing
	Parallel       *int          // -np; nil = DefaultParallel (1, NOT the runtime's -1 auto)
	CacheRAMMiB    *int          // -cram; nil omits the flag, 0 disables the prompt cache
	HostReserveGiB *float64      // planner-side budget knob; nil keeps the topology's derivation
	Devices        []string      // -dev; any entry forces fit off
	SplitMode      SplitMode     // -sm; anything but SplitModeAuto forces fit off
}

type ContextMode int // ContextAuto (zero) | ContextExact
type ContextTuning struct {
	Mode   ContextMode
	Tokens int
}

type OffloadMode int // OffloadAuto (zero) | OffloadAll | OffloadCPU | OffloadLayers
type Offload struct {
	Mode   OffloadMode
	Layers int // read by OffloadLayers only
}

type SplitMode string // "" (Auto, omit -sm) | none | layer | row | tensor — the runtime's own spellings

// MemoryPlan is the resolved launch shape. It is a DESCRIPTION, not a command
// line: LaunchSpec.Args remains the only place an argv is assembled, and
// omission is expressed as a nil pointer or a zero rather than as a rendered
// flag.
type MemoryPlan struct {
	Fit           bool   // the runtime's --fit pass sizes layers AND context
	FitTargetMiB  int    // -fitt; 0 omits
	FitMinContext int    // -fitc; the floor fit is held to (only meaningful with Fit)
	Layers        *int   // -ngl; NIL MEANS OMIT THE FLAG — a pointer to 0 means "-ngl 0"
	ContextSize   int    // -c; 0 under fit, computed here otherwise
	KVType        KVType // never empty: Auto is resolved to a concrete precision
	Packing       Packing
	KVOffload     bool // false emits -nkvo
	MMProjOffload bool // false emits --no-mmproj-offload
	Parallel      int  // -np; DefaultParallel unless overridden
	CacheRAMMiB   *int // -cram; nil omits
	Devices       []string
	SplitMode     SplitMode
	GPUFamily     GPUFamily // echoed: it is what priced the split allowance below

	DeviceBudgetMiB int64 // topology's budget, minus the family's split allowance
	HostBudgetMiB   int64 // topology's budget, or re-derived from a HostReserveGiB override

	// The projected footprints INCLUDING the vision projector's reserve
	// (memory.go's two projections are text-only). Under a PARTIAL layer count
	// these are conservative bounds (full-offload device, CPU-only host), and
	// Notes says so.
	ExpectedDeviceMiB int64
	ExpectedHostMiB   int64

	Notes []string // the human-readable "why", surfaced in the UI verbatim
}

func (p MemoryPlan) FitArg() string          // the literal -fit value: "on" | "off"
func (p MemoryPlan) EmitsLayers() bool       // false = -ngl must be OMITTED
func (p MemoryPlan) OffloadsToDevice() bool  // the boolean every projection needs

// Plan is PURE: no I/O, no probing, no clock, no package state. The zero
// MemoryTopology means "unknown" and must not be passed — a caller with no
// topology takes the derived-budget path in Resolve instead.
func Plan(topology MemoryTopology, profile ModelMemoryProfile, tuning Tuning,
	backend Backend, family GPUFamily) (MemoryPlan, error)

const DefaultFitMinContext = 65536 // -fitc; the runtime's own default is 4096
const DefaultParallel = 1          // -np; the runtime's own default is -1 (auto)

var ErrMemoryPlanInfeasible = errors.New("the embedded LLM does not fit this machine's measured memory budgets")
var ErrTuningInvalid = errors.New("invalid embedded-LLM memory tuning")
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
	AutoUnload     AutoUnloadConfig `yaml:"auto_unload"`      // operator setting
	Tuning         TuningConfig     `yaml:"tuning,omitempty"` // operator setting
}

type AutoUnloadConfig struct {
	Enabled *bool `yaml:"enabled"` // default true
	Minutes *int  `yaml:"minutes"` // default 60
}

// TuningConfig is the persisted form of the planner's `Tuning` override
// vocabulary (plan.go). EVERY knob is a pointer, or a struct of pointers, so
// *unset* stays distinguishable from an explicit `auto` / `0` / `false` —
// which for `Fit` and `CacheRAMMiB` is a different PLAN, not a different
// spelling. Nothing is seeded by ApplyDefaults: the all-nil zero value IS the
// all-Auto default.
type TuningConfig struct {
	Context       EmbeddedLLMContextConfig `yaml:"context,omitempty"`       // mode auto|exact + tokens
	KVCacheType   *string                  `yaml:"kv_cache_type,omitempty"` // auto|f16|q8_0|q4_0
	Offload       EmbeddedLLMOffloadConfig `yaml:"offload,omitempty"`       // mode auto|all|cpu|layers + layers
	Fit           *bool                    `yaml:"fit,omitempty"`
	FitTargetMiB  *int                     `yaml:"fit_target_mib,omitempty"`
	FitMinContext *int                     `yaml:"fit_min_context,omitempty"`
	KVOffload     *bool                    `yaml:"kv_offload,omitempty"`
	MMProjOffload *bool                    `yaml:"mmproj_offload,omitempty"`
	Packing       *string                  `yaml:"packing,omitempty"` // auto|PQ2_0|PTQ1_0
	Parallel      *int                     `yaml:"parallel,omitempty"`
	CacheRAMMiB   *int                     `yaml:"cache_ram_mib,omitempty"`
	HostReserveGiB *float64                `yaml:"host_reserve_gib,omitempty"`
}

// ToTuning is the ONE translation into the planner's vocabulary, and
// validateEmbeddedLLMTuning delegates to it — so "validate() accepts it" and
// "the planner can honour it" are the same statement by construction.
func (t TuningConfig) ToTuning() (embeddedllm.Tuning, error)
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
   ErrRAMUnknown rather than letting the memory gate derive a host budget
   from nothing.

resolve(ResolveInput)  →  Resolution                      [pure, no I/O]
├─ ramGiB unreadable              → ErrRAMUnknown (fail-closed)
├─ effective backend (see degradation rules below): metal only on
│           darwin-arm64; CUDA/ROCm only on amd64; a CUDA tag the platform has
│           no archive for clamps DOWN to the nearest older pinned tag; any
│           other unpinned pair → cpu
├─ guards:  the compatibility table for (effective backend, GPU family,
│           platform) runs BEFORE the packing decision, so a backend it
│           substitutes is the one the packing is chosen for — see
│           [Backend compatibility guards](#backend-compatibility-guards)
├─ packing: PQ2_0 unless one of four triggers fires, highest precedence first
│           (the reason is recorded, never inferred from a file size):
│             1. vulkan — the one backend with no PQ2_0 kernels at all
│             2. an AVX-512 host AND a pin predating #245 — PQ2_0 segfaults at
│                load there, even fully offloaded (inert on the current pin)
│             3. the MEASURED device budget does not fit PQ2_0
│             4. an Ada/L4-class GPU — PTQ1_0 decodes faster there
│           Installed RAM is NOT a trigger: the constraint is the accelerator
│           budget, and it counts only when it was actually measured.
│           mmproj-Q8_0 is ALWAYS part of the set.
├─ GATE:    memoryGate prices every modelled shape (packing × KV precision ×
│           residency extreme) at the smallest context c0wrk serves, against
│           BOTH pools, and refuses with ErrInsufficientMemory when none fits.
│           Runs BEFORE any asset is planned — see
│           [The combined memory gate](#the-combined-memory-gate)
├─ assets:  runtime[platform][effective]  (+ cudart on windows CUDA)
│           + model[packing] + mmproj      — ArtifactSet order = download order
├─ -ngl:    the PLATFORM TIER — 0 on Intel Mac and on the CPU build; 99 on
│           CUDA/ROCm/Vulkan and on Apple Silicon (there the arm64 archive IS
│           the Metal build). This is the default the memory plan starts from
│           and what the RAM-only path emits; a probed topology under an
│           all-Auto tuning OMITS the flag entirely and lets --fit size it
│           (see [Memory plan](#memory-plan))
├─ -c:      the RAM tier (see table below) — what the RAM-only path emits,
│           never 0 and never 262144 there; a fit-sized plan renders the SAME
│           tier as an explicit -c (held to the 65536 fit floor), because
│           --fit sizes only the offload: an omitted -c would hand the context
│           to the runtime's fit pass, which sizes it up to the model's full
│           training context regardless of available memory; an explicit
│           Context override replaces the tier outright
└─ --image-max-tokens: 1024 on metal/vulkan/cpu; uncapped on CUDA/ROCm
                       (uncapped = the flag is omitted entirely)

  RAM (GiB)   context    reachable through Resolve?
  ≤ 11        8192       only with a device-resident plan; a host-resident
                         one at this tier does not clear the gate
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
archive stems are the pinned `prism-b10735-842b188` release names; checksums and
exact sizes live in `registry.go`.

| Effective backend | Runtime archive stem(s) | Packing | `-ngl` | `--image-max-tokens` | Second runtime component |
| --- | --- | --- | --- | --- | --- |
| `metal` (darwin-arm64 only) | `bin-macos-arm64` | `PQ2_0` | 99 | 1024 | — |
| `cuda-12.4` | linux `bin-linux-cuda-12.4-x64`, win `bin-win-cuda-12.4-x64` | `PQ2_0` | 99 | uncapped | `cudart-…-win-cuda-12.4-x64` (Windows only) |
| `cuda-12.8` | linux `bin-linux-cuda-12.8-x64` (no Windows archive in this pin) | `PQ2_0` | 99 | uncapped | — |
| `cuda-13.3` | linux `bin-linux-cuda-13.3-x64`, win `bin-win-cuda-13.3-x64` | `PQ2_0` | 99 | uncapped | `cudart-…-win-cuda-13.3-x64` (Windows only) |

> The `cuda-13.3` row is pinned but **not reachable**: the `cuda-13.3-crash`
> guard substitutes `cuda-12.8` on Linux and `cuda-12.4` on Windows before the
> artifact set is built (KNOWN_ISSUES #222 — the 13.3 builds segfault on Linux
> and print their banner and exit on Windows). A 13.3 driver still runs an
> older-toolkit build, which is the same backwards compatibility the
> missing-archive clamp below relies on. The archives stay pinned so the guard
> can be lifted the moment upstream closes #222.
| `rocm` | linux `bin-ubuntu-rocm-7.2-x64`, win `bin-win-hip-radeon-x64` | `PQ2_0` | 99 | uncapped | — |
| `vulkan` | `bin-ubuntu-vulkan-x64`, `bin-ubuntu-vulkan-arm64`, `bin-win-vulkan-x64` | **`PTQ1_0`** | 99 | 1024 | — |
| `cpu` | `bin-macos-x64`, `bin-macos-arm64`, `bin-ubuntu-x64`, `bin-ubuntu-arm64`, `bin-win-cpu-x64` | `PQ2_0` | 0 (99 on darwin-arm64) | 1024 | — |

Model assets, by packing — plus the projector, which is unconditional because
Bonsai 2 27B is multimodal:

| Component | Asset | Selected when |
| --- | --- | --- |
| `model` | `Ternary-Bonsai-2-27B-PQ2_0.gguf` | no packing trigger fired |
| `model` | `Ternary-Bonsai-2-27B-PTQ1_0.gguf` | any of the four triggers: Vulkan, an AVX-512 host on a pre-#245 pin, a measured budget PQ2_0 does not fit, or an Ada/L4-class GPU |
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
| `cuda-13.3` | linux-amd64 | `cuda-12.8` | guard `cuda-13.3-crash` (#222) — the 13.3 build segfaults on some systems |
| `cuda-13.3` | windows-amd64 | `cuda-12.4` | guard `cuda-13.3-crash` (#222) — the 13.3 build exits after its banner |
| `rocm` + RDNA2 | linux-amd64, windows-amd64 | `vulkan` (⇒ `PTQ1_0`) | guard `rocm-rdna2-abort` (Bonsai-demo #197) — HIP aborts on gfx1030-class parts |
| `rocm` + gfx1151 | windows-amd64 | `vulkan` (⇒ `PTQ1_0`) | guard `windows-hip-gfx1151-garbled` (#223) — `PQ2_0` output is garbage there |
| any | a platform with no pins at all | — | `ErrArtifactNotPinned` |

`Resolution.Backend` records the *effective* backend, and it — never the probed
value — is what the manifest and the informational Settings label must carry:
recording the probed backend would describe an install that is not on disk.

### Backend compatibility guards

The pinned runtime documents its own machine-class failures. `compat.go` turns
each one into a decision c0wrk makes **before** it commits a multi-gigabyte
download to a plan that is known to crash, hang, abort — or quietly produce
garbage. None of this is inferred from first principles: every guard cites the
upstream report that justifies it, and a guard without a citation is a bug.

Sources of record, both read against the current pin (`prism-b10735-842b188`) on
2026-09-25: `KNOWN_ISSUES.md` in the pinned weights repository
(`prism-ml/Ternary-Bonsai-2-27B-gguf`, "Last checked: 2026-09-23"), and the same
repository's model card ("Choosing a Packing" plus the "Cross-Platform
Throughput" table).

```
CompatibilityGuards(backend, gpuFamily, platform)  →  []GuardDecision
                                                     [pure table, no I/O]
```

| Guard id | Fires when | Action | Reason → severity | Upstream |
| --- | --- | --- | --- | --- |
| `cuda-13.3-crash` | effective backend `cuda-13.3` | prefer `cuda-12.8` on linux-amd64, `cuda-12.4` on windows-amd64 | `crash_on_load` → critical | `llama.cpp#222` — Linux segfault; Windows prints the banner and exits |
| `rocm-rdna2-abort` | `rocm` + RDNA2 (gfx1030-class) | prefer `vulkan` | `process_abort` → critical | `Bonsai-demo#197` — HIP aborts on consumer RDNA2; "try the Vulkan build" |
| `windows-hip-gfx1151-garbled` | windows-amd64 + `rocm` (HIP) + gfx1151 | prefer `vulkan`, which resolves to `PTQ1_0` | `garbled_output` → critical | `llama.cpp#223` — `PQ2_0` output is garbage; `-ngl 0` or Vulkan+`PTQ1_0` are correct |
| `vulkan-intel-arc-hang` | `vulkan` + Intel Arc | advisory | `hang` → warning | `llama.cpp#192` — hangs after ~1,900 generated tokens; "run on CPU for now" |
| `windows-cuda-no-start` | windows-amd64 + any CUDA backend | advisory | `fails_to_start` → warning | `llama.cpp#241` — some Windows CUDA builds do not start; the CPU-only build does |

Severity is **derived** from the reason (`severityFor`), never set per guard, so
the two cannot drift apart — and an unranked reason maps to critical, because a
failure nobody classified must never render as the mildest thing on the list.

**Why two guards are advisory only.** `windows-cuda-no-start` names no CPUID, no
driver version and no card, so nothing can predict it statically; what the guard
can do is put the documented fallback in the install record *before* the failure
instead of after it. `vulkan-intel-arc-hang` has a workaround that gives up GPU
acceleration entirely, and it degrades only after ~1,900 tokens of *output* — a
trade c0wrk discloses rather than makes silently on the user's behalf.

**Where the decisions are made.** A guard is data, so the same table is read
twice: by the resolver, to apply the substitutions it still can, and by the
installer, to disclose the rest.

```
Install
├─ plan()                        static half, BEFORE any download
│  ├─ ResolveMachine(...)        #222 and #241 need no device probe
│  ├─ a runtime already on disk? (a repair / reinstall)
│  │     └─ ProbeDevices → ClassifyGPUs → ResolveProfile(GPU: family)
│  │        so #197 / #223 / #192 apply to the artifacts about to be fetched
│  └─ Resolution{Backend, Packing, PackingReason, GPU, Guards}
├─ runtime archives → stage → sign → smoke test
├─ refineWithStagedDevices()     device half, AFTER staging, BEFORE the weights
│  ├─ the staged runtime answers --list-devices → ClassifyGPUs
│  ├─ backend unchanged → adopt the refined plan (the packing rule can still
│  │   flip the weights that have NOT been downloaded yet: an Ada card that was
│  │   unknown at plan time gets PTQ1_0)
│  └─ backend changed   → too late to substitute; record it unapplied, with the
│                         reason in its guidance ("reinstall to apply it")
├─ weights → verify → manifest{packing_reason, gpu_family, guards} → config sink
└─ InstallReport{Guards, PackingReason, Manifest, Resolution}
       └─ backend: EmbeddedLLMStatus{packing_reason, gpu_family, guards}
```

**A substitution must be pinned.** `applyCompatGuards` only swaps in a backend
the registry actually publishes for that platform. A guard that swapped in an
unpinned build would convert an upstream failure into c0wrk's own
`ErrArtifactNotPinned` — a strictly worse outcome — so the decision is recorded
unapplied and its guidance says why.

**The pin is part of the guard set.** The AVX-512 rule is pin-aware by
construction: `PQ2_0` segfaults at load on an AVX-512 host only while the pin
predates PR #245, so `decidePacking` compares `parseRuntimeBuild(RuntimeTag)`
against `minBuildWithAVX512PQ2_0Fix` (10735 — the first build c0wrk can *prove*
contains the fix, since b10709 demonstrably predates it and the CVE-reviewed
range `9a9394a..842b188` demonstrably contains it). An unproven or unparseable
build number compares below the threshold, i.e. it is treated as unfixed: the
error in that direction is a smaller, slower-decoding packing, while the error in
the other direction is a segfault. **Every pin bump must re-read both upstream
documents** and re-check this table — a fixed issue means a guard that should
stop firing, and a stale guard is a needless degradation.

**Known coverage limit.** Classification reads the device *description* the
runtime prints. `gfx1151` is matched by its target name and by the Strix Halo /
Ryzen AI Max product names, but Windows HIP commonly reports that iGPU as a
generic "AMD Radeon(TM) Graphics" with no target in the string — such a machine
classifies as unknown and `windows-hip-gfx1151-garbled` cannot fire. The honest
alternatives were both worse: matching all of RDNA 3 would degrade working
RX 7000 cards to protect a broken iGPU, and c0wrk ships no `rocminfo` probe to
read the target from. A `rocminfo`-derived gfx target is the extension point that
would close this.

### Device memory topology probe

`Hardware` answers *which archive to download* and *how big a context this
machine can hold*. It says nothing about accelerator memory, and it cannot: the
probe runs **before** anything is on disk, so there is no runtime to ask.
`ProbeDevices` is that second probe — it runs against the **installed** binary
and reports the real memory topology.

```
ProbeDevices(ctx, binaryPath, logger)  →  (MemoryTopology, ok bool)   [I/O half]
├─ binaryPath == ""                     → (zero, false)
├─ platformTotalRAMBytes(ctx)           → unreadable ⇒ (zero, false)
│        (the same read Hardware.RAMGiB uses, so the two can never disagree)
├─ runProbeCommand(ctx, binaryPath, "--list-devices")
│        the EXISTING hardened spawn: exec.LookPath, probeCommandTimeout (2 s),
│        probeWaitDelay (500 ms) on the output pipes, sysproc.HideConsole
│        ⇒ absent / hung / nonzero exit / cancelled ⇒ (zero, false)
├─ parseDeviceListing(stdout)           → unrecognized output ⇒ (zero, false)
├─ buildTopology(platform, hostRAMBytes, listing, probedAt)   [pure half]
│   ├─ classifyUnified(platform, devices)
│   ├─ hostBudget   = hostRAM − reserve,  reserve = max(4 GiB, 12.5% of RAM)
│   ├─ devicePool   = unified ? LARGEST device total : SUM of device totals
│   ├─ deviceBudget = devicePool − margin (1536 MiB)
│   │                 unified ⇒ additionally min(deviceBudget, hostBudget)
│   └─ both clamped at 0, never negative
└─ crossCheckHostRAM(logger, topology, listing)   [Debug only, never fatal]

ok == false means UNKNOWN, never "no memory": the caller treats the accelerator
budget as `deviceUnreadable`, which degrades the memory gate to the host pool and
records it in `Notes` rather than refusing. A topology probe refines a decision;
it must never be the reason a load fails.
```

**Why the pool question is the whole point.** Measured on this project's
reference machine (Apple M4 Max, 128 GiB, pinned fork runtime, stdout verbatim):

```
Available devices:
  MTL0: Apple M4 Max (110100 MiB, 110100 MiB free)
  BLAS: Accelerate (0 MiB, 0 MiB free)
```

Those 110100 MiB are **not extra memory** — the OS carved that working set out
of the same 131072 MiB of RAM, so a naive "RAM + VRAM" sum reports 235 GiB on a
128 GiB machine, a **1.84×** overcount that would wave through a launch the
machine cannot serve. Unified pools are not a macOS specialty either (AMD APUs /
Strix Halo through GTT and stolen memory, Intel iGPUs the same way, Jetson and
Grace-Hopper class CUDA SoCs), so unification is **detected**, never inferred
from `GOOS`.

**The two spellings of the inventory.** The parser accepts both; they never
appear on the same line.

| Form | Stream | Reaches c0wrk via | Lists `CPU`? |
| --- | --- | --- | --- |
| `llama-server --list-devices` | **stdout**, exit 0, stderr empty | `ProbeDevices` | no |
| `llama-server -lv 4 -m … ` parameter dump (`common_param: device_info:`) | **stderr**, printed before the load | `Server.pumpOutput`'s bounded `lineTail`, a support bundle | **yes** |

The richer form's `CPU` row is host RAM wearing a device line (measured
`CPU : Apple M4 Max (131072 MiB, 131072 MiB free)` against a
`sysctl hw.memsize` of exactly 128 GiB). It is kept **out** of the accelerator
inventory and used only as the cross-check. `-lv 4 --list-devices` is *not* a
way to get it: the list-devices path exits before the parameter dump, so it
prints the stdout spelling and nothing else (measured with `-v`, `-lv 4` and
`--verbosity 4`).

**Parser rules.** Both format strings were read out of the pinned build's
`libllama-common` (`  %s: %s (%zu MiB, %zu MiB free)` — the two leading spaces
are part of the format — and `cmn  %12.*s:   - %-8s: %s (%zu MiB, %zu MiB free)`)
and re-verified against the shipped pin. Matches are strictly line-anchored and
applied per line.

| Rule | Why |
| --- | --- |
| `^\s{2}(\S+): (.*) \((\d+) MiB, (\d+) MiB free\)$` for the stdout form | a log line, a warning or a stack trace simply does not match |
| `:\s+-\s+(\S+)\s*: (.*) \((\d+) MiB, (\d+) MiB free\)$` for the dump form | the name is left-aligned in a fixed width of 8 and the line carries a timestamp/level prefix |
| entries with `Total == 0 && Free == 0` are **dropped** (and counted, so `ProbeDevices` logs the drop) | measured `BLAS: Accelerate (0 MiB, 0 MiB free)`, and the pinned fork says so at `common/fit.cpp:117`: *"Some non-GPU accelerator backends, such as BLAS, report 0/0 and rely on the host-memory fallback."* The same code **keeps** 0/0 for a GPU/IGPU-typed device and then refuses to place anything on it (*"--fit will not use it"*) — a dropped entry amounts to the same thing here, and keeping the row would add a zero to a discrete sum or win the unified `max()` on a machine whose real accelerator was misparsed |
| the `CPU` row is kept out of the inventory | it is host RAM, not an accelerator |
| duplicate names collapse, first wins | a log tail can hold both spellings; a duplicated pool would be summed twice |
| `Available devices:` alone, or `  (none)`, is an **answered** probe with an empty inventory | a CPU-only build can print the header and nothing after it |
| nothing recognized ⇒ `ok = false`, **not** an empty inventory | "no accelerator" is a verdict about the machine; unrecognized output is a probe that did not answer, and reporting it as the former would turn a broken binary into a "no GPU" verdict |

**Unified classification** (`classifyUnified`, first rule that fires wins):

| # | Condition | Verdict | Why |
| --- | --- | --- | --- |
| 1 | no devices | unified | nothing to sum, and it is the fallback anyway |
| 2 | `darwin-arm64` | unified | Apple Silicon's Metal working set is carved out of system RAM. A discrete eGPU is misclassified here — the **safe** direction, since the `min()` can only shrink its budget |
| 3 | a device named `MTL<n>` | unified | Metal exists only on Apple's unified parts |
| 4 | a `CUDA<n>`/`HIP<n>` device on a **non-amd64** platform | unified | that shape is a Jetson / Grace-Hopper class SoC; a discrete card is only ever provisioned on amd64 |
| 5 | any device matching an **integrated** marker (Apple `M<n>`, Ryzen, Strix Halo, `Radeon(TM) Graphics`, `Radeon 890M`, Iris, UHD Graphics, `Arc(TM) Graphics`, Adreno, Mali) | unified | one device aliases host RAM, so the pools cannot be added. Discrete markers (NVIDIA/GeForce/RTX/GTX/Quadro/Tesla/Instinct, `Radeon RX`/`Radeon Pro`, `Arc A770`-style model numbers) are checked **first per device**, so a hybrid laptop is decided by its iGPU |
| 6 | anything else | **unified** | unsure means unified: misclassifying a discrete card only clamps its budget to a host pool that is bigger than it, while misclassifying a unified pool invents memory |

**Budget derivation.**

| | unified | discrete |
| --- | --- | --- |
| device pool | **largest** device total (one physical pool; a second entry is another view of the same bytes) | **sum** of device totals (independent VRAM the runtime tensor-splits across) |
| `DeviceBudgetBytes` | `min(pool − margin, HostBudgetBytes)` | `pool − margin` |
| `HostBudgetBytes` | `hostRAM − reserve` | `hostRAM − reserve` (independent of the device term) |

- `reserve` = `max(4 GiB, 12.5% of RAM)` — exposed as
  `DefaultHostReserveGiB(ramGiB)` and overridable per plan by
  `Tuning.HostReserveGiB` (which REPLACES the derivation, never stacks on it).
  It is not slack: it has three named tenants, and a launch that eats into it
  does not merely slow down. (1) the OS and the window server, which is what the
  4 GiB floor is sized for; (2) c0wrk itself — the Wails webview, the Go heap,
  the PTYs and the session store all live in this process while the model is
  resident; (3) the **vector index**, the tenant that scales with the project —
  the ONNX Runtime session, the embedding model and a large workspace's
  in-memory index are all host-resident whether or not the embedded LLM is. The
  1/8 ratio above the floor is what keeps all three covered on a big machine,
  where "everything else" is bigger too. On a 16 GiB machine that is 4 GiB,
  leaving 12 GiB; on the 128 GiB reference machine 16 GiB, leaving 112 GiB.
- `margin` = 1536 MiB of accelerator slack: the fork's demo documents
  "~1.2 GiB overhead" for the pinned family, rounded up to the next half-GiB
  because it is a whole-family approximation rather than a measurement of this
  build. It deliberately excludes the vision projector's 849 MiB device reserve
  — a topology is model-agnostic, and the consumer that projects a footprint
  already adds `ModelMemoryProfile.MMProjReserveDeviceMiB`, so folding it in
  here would count the projector twice.
- Budgets come from `Total`, not `Free`: `Free` is a snapshot of the probe
  instant (on the reference machine it equals `Total`, nothing else was using
  the GPU), and a capacity plan that moved with whatever else happened to be
  running would not be reproducible. A consumer wanting a live check reads
  `FreeMiB` explicitly.
- Both budgets floor at 0 and are exposed in MiB too (`DeviceBudgetMiB`,
  `HostBudgetMiB`, floored) because that is the unit `memory.go`'s projections
  speak.

**What this section does *not* do.** `MemoryTopology` is a description, not a
policy: it refuses nothing, picks no context size and changes no launch flag.
`Resolve` still takes `(platform, backend, ramGiB)` and stays pure — threading a
post-install probe through it would mean passing a zero value at the only place
`Resolve` is called. Capacity lives here; the requirement side lives in
[Memory model](#memory-model); a gate that compares them is a separate concern.

### Memory model

`core/embeddedllm/memory.go` is the measured answer to "does this launch fit?".
It replaces a single transcribed *≈ 64 KiB per token* comment — a figure
`Bonsai-demo/README.md` states for a whole model **family**, where the
full-attention 8B costs roughly 140 KiB per token — with a per-term model of the
ONE model c0wrk pins. It decides nothing: the RAM-tiered `-c` ladder above lives
in `resolve.go` and is a separate concern. It is the **requirement** side of a
fit decision; the **capacity** side — how much device and host memory this
machine actually has, and whether the two are the same bytes — is
[Device memory topology probe](#device-memory-topology-probe).

**Provenance.** Every constant was measured on **2026-09-25** with the pinned
fork release `prism-b10735-842b188` (`llama-server --version` →
`0.2.0-dev (build 10735, commit 842b18804)`) on an Apple M4 Max (128 GiB
unified; the Metal device `MTL0` reports 110100 MiB), against the pinned
`Ternary-Bonsai-2-27B-PQ2_0.gguf` and `-PTQ1_0.gguf` (both re-verified against
their registry SHA256 first). The runtime archive was re-downloaded from the
registry URL, so the figures belong to the pin this package ships. Reference
command line — c0wrk's own launch shape minus the flags the model does not
depend on:

```
llama-server -v -m <weights>.gguf --host 127.0.0.1 -ngl <99|0> -fa on \
  -c 262144 -np 1 --no-webui [-ctk T -ctv T]
```

**Two totals per run, and they are not interchangeable.**

| Pass | What it prints | Used for |
| --- | --- | --- |
| dry-run *fit* | `common_params_fit_impl: projected to use N MiB of device memory` + the `common_memory_breakdown_print` MTL0 row | `ProjectDeviceMiB` — it is what the fork's own `--fit` decides on, it is available **before** the load, and it splits device from host cleanly |
| loaded (printed at shutdown) | the `| - Host | … |` row, asserted by `~llama_context: … matches expectation` | `ProjectHostMiB` — it is what the running process actually holds |

The dry-run **Host** row is not the real host footprint: the reserve pass runs
twice, so the breakdown sums two identical CPU compute buffers
(`874 = 322 + 0 + 552`, where 552 = 2 × 276.02). The loaded pass reports the
single buffer the process keeps (276), which is why the host projection is
modelled on it.

**Measured run matrix** — `-c 262144 -np 1 -fa on`, text-only. Device totals are
the fork's own projection; host totals are the loaded pass.

| Packing | `-ngl` | KV | `llama_kv_cache: size` | device total | host total |
| --- | --- | --- | --- | --- | --- |
| PQ2_0 | 99 | `f16` | 16384.00 MiB (K 8192.00 + V 8192.00) | **24450** = 6539 + 16533 + 1377 | **598** = 322 + 0 + 276 |
| PQ2_0 | 99 | `q8_0` | 8704.00 MiB (K 4352.00 + V 4352.00) | **16782** = 6539 + 8853 + 1389 | **598** |
| PQ2_0 | 99 | `q4_0` | 4608.00 MiB (K 2304.00 + V 2304.00) | **12686** = 6539 + 4757 + 1389 | **598** |
| PQ2_0 | 0 | `f16` | 16384.00 MiB | **0** = 0 + 0 + 0 | **23789** = 6865 + 16533 + 390 |
| PTQ1_0 | 99 | `f16` | 16384.00 MiB | **23306** = 5395 + 16533 + 1377 | **541** = 265 + 276 |
| PTQ1_0 | 0 | `f16` | 16384.00 MiB | **0** | **22588** = 5664 + 16533 + 390 |

`TestProjectionMatchesMeasuredMatrix` asserts every cell; the projections
reproduce all six rows **exactly**, in whole MiB.

**The terms.**

| Term | Value (MiB) | Measurement |
| --- | --- | --- |
| KV per token, f16 | 65536 B | `size = 16384.00 MiB (262144 cells, 16 layers)` — exact, no remainder. Derivable: `FullAttentionLayers × KVValuesPerLayerToken × 2 B` = 16 × 2048 × 2 |
| KV divisor | f16 1, q8_0 32/17, q4_0 32/9 | a quantised block is 32 values **plus one fp16 scale**, so a value costs 8.5 / 4.5 bits, not 8 / 4. The three divisors reproduce 16384.00 / 8704.00 / 4608.00 MiB exactly |
| Recurrent state | 149.62 → 150 | `llama_memory_recurrent: size = 149.62 MiB (1 cells, 64 layers), R (f32): 5.62, S (f32): 144.00` — byte-identical under all three cache types and at `-c` 262144 / 131072 / 65536, so it is **context-independent**. An undocumented term: no upstream table carries it |
| Device weights | PQ2_0 6539, PTQ1_0 5395 | the dry-run breakdown's `model` column |
| Host weight spill | PQ2_0 322, PTQ1_0 265 | `load_tensors: CPU_Mapped model buffer size = 322.07 MiB`; the tensors the runtime cannot repack (`token_embd.weight … cannot be used with preferred buffer type CPU_REPACK`). Per-packing because it is the same tensors at 2.13 vs 1.75 bpw |
| Host weights (`-ngl 0`) | PQ2_0 6865, PTQ1_0 5664 | the loaded Host row's `model` column; 4 MiB above device + spill, being the small host buffers (`CPU output buffer size = 0.95 MiB` and friends) |
| Device compute | 1377.52 → 1377 (+12 quantised) | `sched_reserve: MTL0 compute buffer size = 1377.52 MiB`; 1389.03 under **both** quantised types, the dequantisation scratch |
| Host compute | 276.02 → 276 (390 at `-ngl 0`) | `sched_reserve: CPU compute buffer size = 276.02 MiB`, confirmed by the shutdown assert |
| Projector reserve | 849 device + 25 host | `[mtmd] estimated worst-case memory usage of mmproj is 873.10 MiB`, split `adding 848.18 MiB … for device MTL0` / `24.93 MiB … for device CPU` |
| Geometry | 262144 / 64 / 65 / 16 | `n_ctx_train = 262144`; `n_layer = 64`; `-ngl 65` and `-ngl 99` project identically (and #191 calls it "all 65/65 layers"); `16 layers` in the KV line, corroborated by the recurrent-layer trace keeping 0,1,2 / skipping 3 / keeping 4,5,6 / skipping 7 — one full-attention layer in four, which is why a 27B model's cache costs what a 4B full-attention model's would |

`TestEveryConstantMatchesItsMeasurement` is the table form of this: one row per
constant, its measured value, and the log line it came from.

**KV-cache precision is a quality/memory trade-off.** Previously undocumented,
now explicit: the cache type is a first-class knob, not a detail.

| | KV at 262144 | Throughput (#191, same hardware/model/prompt) | Notes |
| --- | --- | --- | --- |
| `f16` | 16384.00 MiB | pp 343.5 / tg 31.8 t/s | lossless baseline |
| `q8_0` | 8704.00 MiB | pp 342.0 / tg 31.5 t/s | indistinguishable from f16 in speed; costs 11.5 MiB of extra device compute |
| `q4_0` | 4608.00 MiB | pp 342.6 / tg 31.3 t/s | the fork's own `BONSAI_KV4=1` option; ~3.5× smaller. Upstream recommends a per-model mean-centering bias (`--kv-mean-center`, `llama-kv-mean-center`) for K-cache quality, and #85 records that no bias file ships for this model |
| `q5_0` | — | **pp 11.4 / tg 4.0 t/s** | **excluded**: ~8× slower on long-context decode (#191, reproduced after a reboot; suspected CUDA-graph cache-key churn) and no capacity gain over q4_0 |
| `q4_1`, `iq4_nl`, `q5_1` | — | unmeasured | **excluded** as unverified on this model |

**Limitations.** The figures are TEXT-ONLY — c0wrk always passes `--mmproj`, so
a gate must add the projector reserve (849 + 25 MiB) to the projections. And
they were all measured on **Metal**: the weight/cache/state/compute terms are
properties of the model and runtime, but the device/host *split* is a property
of the backend's repack support, so PTQ1_0 — which `packingFor` selects only for
Vulkan — was measured on a backend c0wrk does not pair it with. CUDA, ROCm,
Vulkan and CPU residency is unmeasured.

**Load verification per backend (`-ctk`/`-ctv` quantized with `-fa on`).**
KNOWN_ISSUES warns that CUDA builds may need `-DGGML_CUDA_FA_ALL_QUANTS=ON` for
quantized KV beside flash attention; whether each pinned archive actually loads
that shape is therefore recorded as evidence, not assumed:

| Backend (pinned archive) | Quantized KV + `-fa on` loads? | Evidence |
| --- | --- | --- |
| `metal` (`bin-macos-arm64`) | **yes** — q4_0 verified end to end | 2026-09-26: SHA256-verified archive of pin `prism-b10735-842b188`, full load with `-ngl 99 -fa on -ctk q4_0 -ctv q4_0`, all 65/65 layers offloaded, `llama_kv_cache: size = 144.00 MiB (8192 cells, 16 layers …), K (q4_0): 72.00 MiB, V (q4_0): 72.00 MiB`, `/v1/models` answered ready |
| `cpu` (`bin-ubuntu-x64`, `bin-win-cpu-x64`, `bin-macos-x64`) | **unverified — no host** | the archives were downloaded and SHA256-verified against the pins on 2026-09-26, but the capture machine (darwin/arm64) had no container runtime, VM or Windows host to execute them on; a load failure on such a backend surfaces through the bounded-tail failure message, and the fit contract's own abort through `Status.FitWarning` |
| `cuda-12.4`/`cuda-12.8`/`cuda-13.3`, `rocm`, `vulkan` (Linux/Windows archives) | **unverified — no host, no GPU** | same as above, plus the CUDA/Vulkan inventories themselves require the matching GPUs; `bin-linux-cuda-12.8-x64` was SHA256-verified byte-exact (168052249 bytes) against both the pin table and the GitHub REST asset `digest` |

No backend is pinned to `f16` on this table's say-so: `unverified` is not
`failed`, and the planner escalates precision on fit arithmetic alone (ADR-066
D3). What the record buys instead is honesty about which claim rests on a
measurement and which on the fork's documentation — and a failure on an
unverified backend is *visible* (state `error` + the server's own complaint in
the tail) rather than silent.

`WeightsMiB` / `MMProjMiB` (the on-disk sizes) are **derived from `registry.go`
at call time** and never re-typed in `memory.go`;
`TestNoRegistryByteSizeIsRetypedInMemoryGo` scans the source for every registry
byte count to prove it, so a pin bump cannot leave a stale figure behind. Note
that the resident weights are ~11–12 MiB *below* the file size — GGUF metadata
and the tensor-info table are never loaded into a compute buffer — which is why
the projections use the measured residency and not the file size.

### Memory plan

`core/embeddedllm/plan.go` is the decision layer between the two measured halves
above. [Device memory topology probe](#device-memory-topology-probe) answers
*how much memory does this machine have*, [Memory model](#memory-model) answers
*how much does this launch need*, and the planner turns the pair into a launch
shape — the `-ngl` / `-c` / `-ctk` / `-np` / `-fit` values, the two expected
footprints and a `Notes` trail. Before it existed those two values came out of
`resolve.go` as a **constant** `-ngl` per platform/backend and a five-step RAM
ladder for `-c`, neither of which was a memory decision. See
[ADR-067](../decisions/067-memory-aware-embedded-llm-provisioning.md).

```
Plan(topology, profile, tuning, backend, family)  →  (MemoryPlan, error)   [PURE]
│
├─ 1. packing      tuning.Packing, else packingFor(backend, family, fit, host).
│                  Refused with ErrMemoryNotMeasured if the profile has no
│                  measured residency for it — never projected from a file size.
│
├─ 2. budgets      device = topology.DeviceBudgetMiB() − splitAllowance(family)
│                  host   = topology.HostBudgetMiB(), or RAM − HostReserveGiB
│                           when that override is set (it REPLACES the derived
│                           reserve, it does not stack on it)
│
│     splitAllowance is POLICY, not measurement: memory.go's device/host split
│     was measured on Metal alone, so an unmeasured family pays 1024 MiB and an
│     unrecognized one 2048 MiB. Apple Silicon — the measured family — pays 0.
│
├─ 3. FIT EXCLUSIVITY  (see the rule below)  →  fit bool, layers *int
│
├─ 4. target ctx   tuning.Context exact  → that (validated 1..MaxContext)
│                  fit                    → FitMinContext (the floor fit must
│                                            reach; a fit run that cannot is
│                                            aborted by the runtime)
│                  otherwise              → contextSizeFor(topology.HostRAMGiB)
│
├─ 5. ADAPTIVE KV  Auto: try f16; if the target context does not fit the device
│                  budget, escalate q8_0, then q4_0. Every escalation is noted.
│                  Pinned: honoured as given, never escalated; a precision
│                  outside the closed set is refused (ErrKVTypeUnsupported
│                  wrapped in ErrTuningInvalid — both are in the chain).
│                  None fits → this PASS is infeasible, and Plan's degradation
│                  ladder (below) tries the next shape before anyone refuses.
│
├─ 6. footprints   profile.ProjectDeviceMiB / ProjectHostMiB
│                  + MMProjReserveDeviceMiB / MMProjReserveHostMiB (the
│                  projections are text-only; c0wrk always passes --mmproj)
│                  −nkvo moves the whole KV cache device → host
│                  a PARTIAL layer count yields BOUNDS, not measurements
│
└─ 7. Notes        one entry per non-default decision, in operator-facing prose
```

**Fit exclusivity — exactly one decider.** The pinned fork ships its own sizing
pass, `--fit`, which adjusts *unset* arguments so the launch fits the device
memory it can see. It is ON by default and it sizes both `-ngl` and `-c`, which
makes it a direct competitor with this planner. The fork settles the competition
by **refusing** the ambiguous shape: `common/fit.cpp` throws at `:183` and
`:462`, `:466`, `:472`, `:477`, `:480`, `:483` whenever `--fit` is asked to size
an argument the caller already pinned. Confirmed empirically against the pinned
runtime: **`-ngl 99` beside `--fit on` aborts the launch** rather than degrading
it. The healthy fit pass was re-captured end to end on 2026-09-26 (pin
`prism-b10735-842b188`, darwin-arm64, `-fit on -fitc 65536 -np 1 -fa on`, no
`-ngl`, no `-c`): `common_init_: fitting params to device memory …` →
`common_params_fit_impl: projected to use 24450 MiB of device memory vs. 109950
MiB of free device memory` → `will leave 85499 >= 1872 MiB of free device
memory, no changes needed` → `common_fit_params: successfully fit params to free
device memory` — the 24450 MiB projection is the same figure ADR-066 cites, and
on a pool with headroom fit settles at the model's training maximum
(`n_ctx = 262144`), which `-fitc` bounds only from *below*. The abort spelling
of that last line is what the supervision tail scanner looks for (see
Invariants). So which of the two decides is a pure function of the override
vocabulary:

| `Offload` | `Devices` | `SplitMode` | `-fit` | `-ngl` | `-c` | who sizes |
| --- | --- | --- | --- | --- | --- | --- |
| Auto | none | Auto | `on` | **omitted** | `0` | the runtime, held to `-fitc` |
| any explicit | — | — | `off` | passed | **computed here** | the planner, from `ModelMemoryProfile` |
| Auto | any | — | `off` | passed | **computed here** | naming the devices *is* pinning the offload |
| Auto | none | any | `off` | passed | **computed here** | the same, for the split |

`MemoryPlan.Layers` is a `*int` because `nil` (*omit the flag*) is materially
different from a pointer to `0` (`-ngl 0`, a real choice). `FitArg()` renders the
literal `-fit` value and `EmitsLayers()` the omission, so the rule is testable
exactly as it is stated. A `FitEnabled` override never breaks exclusivity:
`false` forces the second row, and `true` beside an explicit offload **loses** to
the rule and is recorded in `Notes` rather than silently dropped or spawned into
a `fit.cpp` throw. An exact `Context` does *not* force fit off — it is not an
offload override, and `--fit` adjusts only *unset* arguments, so a pinned `-c`
survives beside `--fit on`.

**Two defaults that diverge from the runtime's own, both measurement-forced.**

| | c0wrk | runtime default | why |
| --- | --- | --- | --- |
| `-fitc` | **65536** | 4096 | the pinned model's issue tracker reports empty/truncated answers as *the most common complaint*, and the documented workaround is `-n 16384` with `-c 65536`. A 4096 floor reproduces that failure on every machine tight enough for fit to shrink — a server that loads, answers and returns nothing useful, which is worse than a refusal because nothing reports it. 65536 also equals the tier `contextSizeFor` grants a ≤71 GiB machine, so the two paths stop disagreeing about "usable" |
| `-np` | **1** | `-1` (auto) | measured: `-np 4` inflates fit's own projection from **24450 to 77297 MiB** (`n_streams` becomes 4 when `kv_unified` is false), and `-np` **splits** `-c` across slots (`-c 8192 -np 4` → `n_ctx_slot 2048`). Auto would therefore both triple the number fit decides against and quietly quarter the context the plan verified. c0wrk serves one agent loop over one loopback socket, one request at a time, so a single slot is not a restriction — and it is the shape every `memory.go` figure was measured in |

**A shape that does not fit is DEGRADED before it is refused.** `planShape` is
the single pass; `Plan` wraps it in a bounded ladder of relaxations, least-lossy
first, and every rung re-runs the KV precision ladder rather than replacing it:

| # | rung | what it trades | flag(s) |
| --- | --- | --- | --- |
| 1 | spill the KV cache and the projector's reserve to host RAM | attention bandwidth; the weights stay on the accelerator | `-nkvo`, `--no-mmproj-offload` |
| 2 | reduce the context to `DefaultFitMinContext` | reach — never below 65536, see that constant | `-fitc`, or an exact `-c` off the fit path |
| 3 | both | — | — |
| 4 | host residency | the accelerator, entirely | `-ngl 0` (which also turns fit off) |
| 5 | host residency with the reduced context | — | — |

A **partial** offload is deliberately NOT a rung: `memory.go` has no measurement
for one (`footprint` can only bound it), and an unmeasured device/host split is a
guess a capacity gate must not make. A slow launch that loads beats a fast plan
nobody could verify. An operator who pinned the offload (`explicitOffloadShape`)
gets no ladder at all — honouring the pin and reporting the refusal beats quietly
launching a different shape than the one asked for — but the refusal is still the
typed both-pools one, and it prices only the residency they pinned. Every rung
that fires is recorded in `Notes`.

The RAM-only path (`Topology == nil`) still does **not** delegate to fit — a
fit-sized plan has no concrete context, and the resolved context is what the
manifest records and what becomes the `llm.models` `context_window` override
deterministically at config-read time, when the server is not running and cannot
be asked. It reports the DERIVED budgets it was gated on rather than two zeroes,
so a reader can tell a plan that was priced from one that was not.

**The packing loop closes here too.** `MachineProfile.FitsPQ2_0` exists so that
ADR-067 D2's *"PTQ1_0 whenever memory is short"* can be a measurement rather than
a heuristic, and this planner is what produces it: `planMemory` plans the derived
packing, and if the measured budgets refuse it, it re-decides with
`FitInsufficient` and plans **once** more. A second refusal is final — PTQ1_0 is
the smallest packing this model ships. An explicit `Tuning.Packing`
short-circuits the loop and is recorded as `PackingReasonOperatorOverride`.

### The combined memory gate

The gate that replaced ADR-067 D6's flat `MinRAMGiB = 16.0` refusal. D6 measured
ONE pool and was wrong in both directions at once, which is why the replacement
is not a different threshold but a different *measurement*:

```
memoryGate(ResolveInput, effectiveBackend, gpuFamily, ModelMemoryProfile)   [PURE]
│
├─ 1. budgets   gateBudgetsFor — WITH a topology: its own two budgets (reserve
│               and unified clamp already applied) minus splitAllowanceMiB(family).
│               WITHOUT one: host = RAM − DefaultHostReserveGiB(RAM), and the
│               accelerator axis is classified STATICALLY:
│                 backend not gpuAccelerated          → deviceAbsent
│                 backendHasIndependentVRAM            → deviceUnreadable
│                 otherwise (metal / vulkan / unsure)  → deviceKnown, unified:
│                                                        the pool IS the host budget
│
├─ 2. context   the operator's exact -c, else the -fitc override, else
│               DefaultFitMinContext — the smallest context c0wrk will serve, so
│               the question is "can this machine serve the model AT ALL"
│
├─ 3. space     packings = the machine's own decision (+ PTQ1_0 when that is not
│               already it), mirroring planMemory's single bounded downgrade so
│               the gate never admits a machine on a packing the planner cannot
│               reach. Shapes = both residency extremes, minus the offloaded one
│               when there is nothing to offload to.
│
└─ 4. search    closestAttempt walks (packing × residency × precision) least-lossy
                first and returns the first shape whose overflow is 0, or the one
                that overflowed by the least. Any fit → ADMIT; none → refuse with
                an *InsufficientMemoryError built from the closest attempt.
```

`overflow` is where the unified awareness lives: on a unified machine the two
footprints are **additive** against one pool (and the device term is *also*
checked, because the topology's margin and the family's split allowance live in
the device budget alone); on a discrete machine each is checked against its own.

**The three device-axis states have three different consequences**, which is why
the axis is an enum and not a bool:

| state | when | consequence |
| --- | --- | --- |
| `deviceAbsent` | the runtime reported no devices, or the backend is not accelerated (CPU build, Intel Mac) | only the host-resident shape is priced; a host pool too small for it is a **refusal** |
| `deviceKnown` | a topology was probed, or the accelerator's pool IS host RAM (Metal, and Vulkan where `classifyUnified` settles "unsure" as unified) | both pools are priced and both are checked |
| `deviceUnreadable` | a backend with memory **independent** of host RAM (discrete CUDA/ROCm on amd64) and nothing to ask yet | the device axis is **not** gated; the host axis is priced against the device-resident host need (the measured 566–623 MiB of spill + one CPU compute buffer + the projector), and the degradation is recorded in `Notes` |

`deviceUnreadable` is the state that fixes the original bug: it is what a FIRST
install on an 8 GiB laptop with a big card looks like, because `ProbeDevices`
needs a provisioned runtime and there is none yet. Refusing there reproduces D6
exactly. It is safe because the host figure it gates against is a measurement of
the device-resident shape, not a guess — and because the install re-plans against
the staged runtime's real topology before the multi-gigabyte weights are fetched.
`backendHasIndependentVRAM` deliberately excludes Vulkan: it serves discrete cards
and iGPUs alike, and `classifyUnified`'s rule 6 already settles "unsure" as
unified, because misclassifying a discrete card only shrinks its budget while
misclassifying a unified pool invents memory.

**Ordering.** The gate is the first thing `resolveWith` does after resolving the
effective backend, the GPU family and the compatibility guards, and before
`ArtifactSet`. `Install` calls `plan` before `Layout.EnsureRoots`, so a refused
machine plans no assets, creates no directory, fetches no byte and registers no
provider — the invariant `TestInstallMemoryGateRefusesBeforeAnyDownload` asserts.

**Two callers, one gate.** `Resolve` runs it internally; `CheckMemoryBudget`
exposes the same function so `InstallEmbeddedLLM` can refuse *synchronously* — a
rejection that arrives as the answer to the user's click rather than as a toast
ten minutes into a download. Exposing it rather than re-deriving a threshold in
the backend is what keeps the two from drifting apart.

### Install (explicit user click only)

```
Installer.Install(ctx, InstallOptions)   [runs in background; the RPC returns immediately]
├─ Installer.Stop(ctx) → STEP 0, and it precedes the probe: a resident server executes
│                        out of the runtime tree the promote step below retires (on
│                        Windows renaming a live process's own tree and working
│                        directory is refused outright), and the gigabytes it holds
│                        would otherwise be priced as unavailable by the probe and the
│                        memory gate. A no-op when nothing is resident, so this is what
│                        makes a repair click UNLOAD a model the user had running; a
│                        stop failure aborts the install before anything is fetched.
│                        BOUNDED by Installer.StopTimeout — the backend's 30 s stop
│                        budget, handed over as a FIELD because this install's own ctx
│                        must stay deadline-free for the download. An expiry is core's
│                        no-gate force path, so a repair clicked during a cold load
│                        takes the resident child down and PROCEEDS instead of sitting
│                        here — before plan, therefore before the first
│                        install_progress event — for the length of that load
├─ hardware probe      → RAM is a hard input: ErrRAMUnknown refuses instead of guessing
├─ resolve             → Resolve(ResolveInput). The combined memory gate is its
│                        FIRST check, so a machine that cannot hold the model plans
│                        no assets, creates no directory and downloads nothing
│                        (ErrInsufficientMemory, naming both pools). A repair or a
│                        reinstall still has a provisioned runtime to ask, so its
│                        gate runs on the MEASURED topology; a first install has
│                        none and runs on the derived budgets
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
├─ DEVICE PROBE        → ProbeDevices on the staged, signed, smoke-tested runtime — the
│                        last moment a refinement is still cheap, because the
│                        multi-gigabyte weights have NOT been fetched yet. The
│                        authoritative topology drives the generation-aware packing rule
│                        (so the weights about to be downloaded are the right ones), the
│                        device-dependent guards and the memory gate. Fail-soft: no
│                        answer leaves the plan on its statically decidable guards. A
│                        guard that wants a DIFFERENT RUNTIME is past its moment — the
│                        archive is staged and signed — so it is recorded as guidance
│                        (Applied=false) rather than silently dropped
├─ WEIGHTS phase       → for model, mmproj: download straight to their final path (the
│                        artifact IS the file, so there is nothing to extract)
│                        → SHA256 verify
├─ write manifest.json                                  [atomically, and only now]
│                        records packing/backend/checksums/port/model_file, the
│                        degradation record (packing_reason, gpu_family, guards) AND the
│                        topology + plan pair a load launches from — one coherent pair,
│                        so a topology is recorded only when it actually informed the
│                        plan beside it
└─ ConfigSink.ApplyInstalled(InstallState)
     → embedded_llm.* + the auto-unload defaults (unset knobs only)
     → Config.SyncEmbeddedLLMProvider(state.ContextSize) generates
        llm.openai_compatible.embedded + the llm.models context_window override
        (the only caller that passes a non-zero tier, because it is the only one
         that has resolved it) → persist → rebuild the router

A failure at any step leaves no manifest and no provider entry: a half-installed
model is never registered. Bytes that DID verify are deliberately kept — they are
cache hits for the next attempt, which is what makes a multi-gigabyte install
resumable rather than restartable. An operator cancellation rides the same rule:
`CancelEmbeddedLLMInstall` cancels the run's context, core surfaces it as
`context.Canceled`, the run leaves no manifest and no provider entry but KEEPS
the verified bytes, and reports the quiet outcome (no toast — the click is the
report), so the retry resumes rather than restarts. Install refuses outright when no ConfigSink is
wired: bytes that cannot be registered are bytes nobody can use. A sink failure
after the manifest was written keeps the manifest, because it is accurate — the
retry re-downloads nothing and rewrites the same record.
```

#### Download failures: silent auto-resume, fatal-only reporting

`Downloader.Download` owns the retry policy, and the policy is shaped by what
the operator can act on:

- **Resumable failures are retried SILENTLY, inside the one `Download` call.**
  A transfer that drops mid-body (`ErrIncompleteTransfer` — the "unexpected
  EOF" of a flaky link), a server that could not be reached at all
  (`ErrUnreachable`: DNS/dial/TLS/response-header failures and the retryable
  HTTP statuses 408/429/5xx) is retried after a backoff that starts at
  `DefaultRetryBackoff` (1 s), doubles per consecutive failure and caps at
  30 s. Each retry resumes the kept partial through a ranged request, so the
  progress bar simply continues — no error field, no toast, no event: the
  operator has nothing to do about a dropped connection, so nothing is shown.
- **The fatal bound is zero PROGRESS, not failure count.** A streak counter
  advances only on attempts that grew the partial by nothing at all; any
  attempt that advanced it resets both the streak and the backoff. After
  `DefaultMaxFailedAttempts` (3) consecutive zero-progress attempts — the "no
  network" shape the operator named — the loop stops and returns
  `ErrAttemptsExhausted` wrapping the last cause (the partial is kept). A
  flaky link that keeps moving bytes is therefore retried without bound, while
  a dead one fails in seconds.
- **Cancellation is never retried.** `context.Canceled` from the operator's
  cancel click or an app shutdown aborts the loop at once — the quiet outcome
  and the reported interruption respectively.
- **Everything else is fatal on first sight**: a checksum mismatch (ASI04),
  the disk guard, an oversized object, a verdict HTTP status (403/404/410…),
  a server that keeps rejecting resume offsets (the bounded 416 restart).

The backend routes the fatal half by surface: `failEmbeddedInstall` translates
it through `embeddedInstallFailureMessage` into an operator-friendly sentence
(naming the action — check the network, retry, bytes are kept — instead of
transport diagnostics), records it as `install_error` (the Settings error
line) and raises the `runtime_error` toast with the same text, so the failure
reaches the operator even with the Settings dialog closed. The status-bar
indicator shows none of it — its `error` surface is the supervisor's own error
state, and `install_error` is deliberately not read there.

### Remove (explicit user click only)

```
Installer.Remove(ctx)
├─ Installer.Stop(ctx) → the supervisor terminates llama-server FIRST. A stop
│                        failure aborts the removal: a live process holding the
│                        files cannot be deleted on Windows, and on Unix it would
│                        keep serving from unlinked inodes. This stop needs no
│                        Installer.StopTimeout of its own: it is bounded by the
│                        ctx its caller passes, and RemoveEmbeddedLLM wraps the
│                        WHOLE removal in the 30 s stop budget
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
│    → reset embedded_llm.* to zero while PRESERVING the auto_unload and
│      tuning sub-sections (both are operator settings, not install state)
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
       → LaunchSpec: the IDENTITY half (binary, weights, projector, loopback
         socket, --image-max-tokens) always from the manifest + the layout, and
         the SHAPE half from Manifest.Plan through ApplyMemoryPlan — or, for a
         pre-plan manifest, from the pure policy in resolve.go. A recorded plan
         is then OPTIONALLY refined by a bounded load-time device probe
         (Server.ProbeDevices, LoadProbeTimeout), which FAILS SOFT: an unwired
         probe, a wedged or absent driver query, an unreadable memory profile
         and a re-plan that now refuses all launch the recorded plan unchanged
         and log at Debug. A re-plan pins the packing to the manifest's, since
         the bytes on disk are the only ones it may price.
         Then Validate: loopback host only, usable port, the fit-exclusivity
         rule, a context inside 0..MaxContext (0 only under fit), the KV
         allow-list, and EVERY argv-bound numeric field bounded on both ends —
         the floors are launch semantics, and the ceilings come in two kinds:
         limits.go's overflow and absurdity guards for the OPERATOR TUNABLES
         (-np 1..MaxTuningParallel, -ngl 0..MaxTuningLayers,
         -fitt/--cache-ram 0..MaxTuningMiB), and the model's own training
         context (memory.go's maxTrainingContext) for the three context-derived
         fields -c, -fitc and --image-max-tokens. The tunables' ceilings are
         enforced here as well as in backend/config because a bound at only one
         layer is one an operator can walk around, and this gate sees the value
         that becomes a command line whichever path it arrived by
       → State loading, then spawn
            llama-server -m <gguf> --host 127.0.0.1 --port <effective>
                         -fit <on|off> [-fitt N] [-fitc N]
                         [-ngl <all|0|N>] [-sm <mode>] [-dev <a,b>]
                         -fa on -c CTX [-ctk T -ctv T] [-nkvo] -np N
                         [--cache-ram N]
                         --temp 1.0 --top-p 0.95 --top-k 20 --jinja
                         --no-ui [--mmproj <file>] [--no-mmproj-offload]
                         [--image-max-tokens N]
         (bracketed flags are omitted. A manifest that recorded a PLAN renders
         every value that plan carries — `-fit on` with `-ngl` omitted, or
         `-fit off` with an explicit `-ngl`, plus the resolved KV precision, the
         cache knobs, `-dev` and `-sm` where the plan named them. A PRE-PLAN
         manifest renders `-fit off` with an explicit `-ngl`, `-np 1` and the
         recorded `-c`, and leaves the KV precision, the cache knobs, `-dev` and
         `-sm` to the runtime's defaults. `-np` is rendered on every command
         line either way — see DefaultParallel)
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
       → CONTEXT READBACK: GET /props → default_generation_settings.n_ctx (the
         PER-SLOT figure) × total_slots = the effective total. Persisted to the
         manifest and, through Server.PersistContext, to the tier-1
         llm.models."Bonsai 2 27B".context_window override — the one correction
         that override can ever receive, because a tier-1 value shadows the
         tier-1.5 lazy probe. Fail-soft, and BOUNDED: readPropsContext refuses a
         non-200, an unreadable or unparseable body, a missing or non-positive
         n_ctx, a product that would OVERFLOW, and a product above the model's
         own training context — so no answer keeps the recorded value, and a
         value that did not move writes nothing. Never fails a load that is
         already serving. The ceiling is the point of the fail-softness, not a
         refinement of it: this is the only context figure in the subsystem that
         arrives from an external process over HTTP, and it lands in two durable
         stores, so an absurd value would poison the router's context accounting
         (a huge positive figure reports "fits" forever and compaction never
         triggers) with no way to repair it — the readback re-runs on every load
         and clobbers a hand-edit. The manifest write MERGES onto the current
         on-disk manifest rather than clobbering it
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
       → the wait budget bounds the WAITING and never the load. It is armed PER
         WAITER, so its expiry stops that one request from waiting and leaves the
         in-flight load running DETACHED, bounded by the supervisor's own
         ReadyTimeout — which is the budget that decides a load is wedged and
         carries the diagnosis (the server's log tail). The half-loaded weights
         are therefore never discarded by a request that ran out of patience, and
         a following request joins the same load or finds the model already
         resident instead of paying for a second cold start. This is what makes
         ADR-067 D13's guarantee structural rather than a default-tuning
         coincidence: the same holds for timeouts.llmRequestTimeout (10 min,
         SHORTER than the 15 min ready budget), which the transport keeps off the
         load's context entirely
       → an ErrLoadWaitTimeout means exactly one of: the load was still
         legitimately in progress, or the supervisor's ready budget is
         misconfigured above the transport's wait budget. Since the load survives,
         the honest recovery is to RETRY — not to report the model as broken
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
        Unload and Stop carry a FORCE path: a caller whose ctx expires while the
        single-instance gate is held by an in-flight Load does NOT get a timeout
        error back — forceUnload terminates the live child WITHOUT the gate,
        because the alternative is an orphan the app can never identify again.
        Its detach is provisional: a force stop that does not take RE-ATTACHES the
        run handle before it reports the failure, so the rule above holds on this
        path too — a wedged child is never left live and untracked, and a retry
        targets the same process instead of stacking a second server.
        The caller's budget bounds only the GATE ACQUISITION, on BOTH paths: once
        the gate is held the stop derives its own budget (stopTimeout + killWait
        = 15 s) and the force path derives its own (+1s = 16 s), each under
        context.WithoutCancel, so a budget nearly spent behind a cold load no
        longer truncates the graceful window and SIGKILLs the child early. The
        force path force-terminates and returns SUCCESS when the stop takes, so
        a stop-shaped RPC's STOP costs at most 30 s + 16 s. The backend's
        TRANSLATED "a load is
        still in progress" error — the one `embeddedBoundedStopErr` produces,
        and it fires only on `errors.Is(err, context.DeadlineExceeded)` — comes
        back only when no child had spawned yet, the one case where there is
        nothing to kill and the gate timeout is the whole truth. That scoping is
        a claim about THIS translation, not about every error a stop-shaped call
        can return; two other paths are actionable with a child very much
        involved or with none at all. `terminate`'s "did not exit within … of
        being killed" — the uninterruptible-syscall case `defaultKillWait`
        exists for — is returned on BOTH the gated and the force path, and
        `RemoveEmbeddedLLM`'s post-stop work (`errors.Join` over the three tree
        deletions plus `Sink.ApplyRemoved`) returns its own errors when the tree
        is read-only or busy, with no child ever part of it. The 30 s + 16 s
        figure bounds the STOP alone: in RemoveEmbeddedLLM the tree deletions
        and the config save that follow it take no context — finite, but
        unbudgeted.
        The terminal state is NOT the force path's alone to decide: with a Load
        still in flight and the state still `loading` it SKIPS its own success
        transition and leaves the terminal transition to the interrupted Load,
        while a force stop with no load in flight ends at `installed`. Nor does
        that snapshot decide WHICH transition the Load writes, because it is
        taken BEFORE `terminate` signals the child — an `s.emit` (the backend's
        synchronous `OnState` handler) and a Warn log sit in between — so a
        readiness poll landing in that window is answered by a server already on
        its way out and `waitReady` can still return nil. `Load`'s success path
        therefore re-validates, under `s.mu`, that the run it published is still
        the published one BEFORE it claims residency. The invariant that
        survives every interleaving is that a dead child is NEVER reported as
        `loaded`, and the interrupted Load leaves exactly one of two terminals:
        `error` carrying the load's own diagnosis (the child died before/at
        readiness — `supervise`'s crash report stands and the Load adds no
        transition of its own, returning `ErrServerDied`), or `installed` with
        an EMPTY message plus the unexported `errStoppedDuringLoad` returned to
        the caller (readiness won the race against a requested stop). Neither
        refused-residency branch stamps the idle budget or runs the `/props`
        context readback — both happen only once residency is claimed. One
        residual case deliberately still claims residency: a force stop whose
        `terminate` did NOT take RE-ATTACHES the run handle, so the
        re-validation passes and the Load reports `loaded` for a child that is
        alive and just answered the probe, replacing the `error` the force path
        wrote — reporting `installed` there would misreport the RAM a live
        process still holds.
        Shutdown is exactly the caller it exists for: it budgets 30 s against a
        Load that can hold the gate for DefaultReadyTimeout (15 min), and a Stop
        that returned "context deadline exceeded" there would leave llama-server
        running — detached from every caller context, unrecorded in the manifest,
        and routed around rather than adopted by the next launch's port scan —
        holding its gigabytes of RAM and VRAM until a manual kill or a reboot.
Idle:   auto_unload.minutes (default 60) without activity → the same stop path,
        minus the force: an idle unload that cannot get the gate is a load in
        progress, and killing that would trade a deferrable unload for a failed
        cold start. It also carries the activity generation the expiry decided on
        and is ABANDONED if activity moved in the meantime.
        An expiry never cuts a generation short: while a request is in flight the
        unload DEFERS itself by a bounded 30 s grace (idleGracePeriod), re-armed
        on every expiry for as long as requests stay open. The grace is a
        re-check interval, not a second budget — it deliberately does not stamp
        activity, so the unload lands one grace after the last request completes.
        One exception: a deferral is SKIPPED when activity landed in the window
        the expiry read the in-flight count outside the lock and already armed a
        fresh FULL budget. deferIdleUnload RE-READS the budget under Server.mu and
        keeps it when any is left, because armGrace would otherwise stop that
        budget and replace it with a 30 s grace — unloading a model 30 s after
        genuine activity and making the next request pay a cold load.
        The re-checks are bounded by WALL TIME, not by their number: the first
        deferral of an episode stamps idleTimer.deferSince and the re-arms that
        continue it keep that stamp (mark/disarm/setPolicy clear it), so a leaked
        in-flight count (a response body nobody closed) costs AT MOST ONE EXTRA
        FULL IDLE BUDGET and then ends in an unload that names the leaked count
        at Warn, rather than pinning gigabytes resident forever.
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
          → Server.Stop under a 30 s budget (embeddedStopBudget) — the server if
          it is running, a no-op when the subsystem was never built. A failure is
          logged, never fatal. The budget is why Stop carries its NO-GATE force
          path: core's Stop must first acquire the single-instance gate, which an
          in-flight Load can hold for the 15 min ready budget, so a quit during a
          cold load would otherwise exhaust the 30 s and leave llama-server
          running — detached from every caller context, unrecorded in the
          manifest, and routed around rather than adopted by the next launch's
          port scan — holding its gigabytes until a manual kill or a reboot.
          The 30 s bounds only the GATE WAIT: whichever path runs then derives its
          OWN stop budget under context.WithoutCancel (stopTimeout + killWait
          = 15 s once the gate is held, +1s = 16 s on the no-gate force path an
          expired budget takes), so the graceful window is never cut down to what
          a cold load left of the caller's 30 s. A quit therefore costs at most
          30 s + 16 s = 46 s, and what the bound guarantees is TRACKING, not
          termination: a stop that does not take — the wedged native child in an
          uninterruptible syscall that survives BOTH the graceful signal and the
          kill, precisely what defaultKillWait exists for — makes terminate
          exhaust its own budget and return an error, and both stop paths then
          RE-ATTACH the live run handle and record StateError, which this
          shutdown logs as non-fatal before the app exits with the child alive.
          That is the ONE quit outcome that can still leave llama-server
          running; what no quit outcome can leave behind is a live child the
          supervisor has lost sight of.
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
            The header carries a Cancel button (disabled while any mutating
            action's busy window is open, rendering `Cancelling…` with a
            spinner while its own request + read-back is in flight): the click
            calls `CancelEmbeddedLLMInstall` — idempotent, and never gated on
            the operation gate — and only DELIVERS the stop request; the run
            then ends asynchronously and QUIETLY (no toast, no error line: the
            click is the report), and the `embedded_llm:state` event retires
            the progress rows and flips the block back to the Install button.
            Partial verified bytes are kept as the resume point, so clicking
            Install again RESUMES the download instead of restarting it.

the error line (role="alert", top of the block) renders with this precedence:
            the failed action the operator just took (its rejected promise IS
            the report — a synchronous gate refusal, a failed removal) → the
            last FATAL install failure (`install_error`, the backend's
            operator-friendly translation) → the supervisor's own error state
            (`status.error`). Resumable download failures NEVER appear here —
            core retries them silently — so the line only ever names something
            the operator must act on; a new install run clears it, and the
            retry affordance is the Install button beside it.

installed  → the INFORMATIONAL install record: the packing the resolver
            actually chose (PQ2_0 | PTQ1_0), the EFFECTIVE backend, the
            RAM-tiered context, the persisted port and the derived base URL +
            composite model id, plus the MEASURED topology (the probed devices
            with their free/total memory and the unified-memory flag; an
            unprobed machine says so instead of implying CPU-only) and the
            EFFECTIVE plan as one compact line (packing · KV · context ·
            offload · fit · slots · expected device/host footprint) with the
            planner's `Notes` trail verbatim — '—' wherever nothing was
            measured or recorded. When the last failed launch tripped the fit
            contract, its `fit_warning` sentence renders in the warning colour
            inside the record (absent on a healthy status; a ready
            launch clears it). Below it Load (or Unload while resident,
            with the blocking load's own spinner), Remove behind a
            confirmation dialog, and the auto-unload toggle with its minutes
            field (default 60, bounded 1-525600 by a `max` attribute mirroring
            `embeddedllm.MaxAutoUnloadMinutes`; committed on blur/Enter, and
            only once a keystroke has landed — this field keeps its OWN draft
            rather than using the shared `NumberField`, and a null draft makes
            the commit a no-op, so a bare focus loss fires no RPC; an
            out-of-range value reverts locally instead of paying a round trip
            for a guaranteed refusal). While loaded with the idle timer armed,
            the remaining budget is shown — and while an expiry is DEFERRED for
            an in-flight request the answer is the grace left rather than the
            budget left, because the budget is already spent. Below those sits
            the memory-plan TUNING block (the `useEmbeddedLLMTuning` hook behind
            two fully controlled leaves): three PRIMARY controls — Context
            (Auto | Exact + a tokens field), KV-cache precision
            (Auto | f16 | q8_0 | q4_0) and layer offload (Auto | All | CPU |
            N layers + a layers field) — plus a COLLAPSED "Advanced tuning"
            VariantSection carrying the rest (fit, fit target MiB, fit floor
            tokens, KV-on-device, projector-on-device, packing, parallel slots,
            prompt-cache MiB, host reserve GiB; every numeric knob shows "Auto"
            while unset and gets an Auto button that clears the override back to
            unset once set, and every numeric input carries a `max` attribute
            mirroring the Go ceiling in `core/embeddedllm/limits.go` so the
            browser refuses what the backend would). ALL THREE boolean knobs —
            Fit, KV-cache-on-device and projector-on-device — are **TRI-STATE**
            (Auto | On | Off) rendering the ONE shared `TuningTriState` leaf,
            NOT two-position switches: an unset knob means "the planner
            decides", and the planner CAN decide OFF (the offload exclusivity
            rule forces `-fit off`, and the memory gate spills the KV cache and
            the projector reserve to host RAM with `-nkvo` /
            `--no-mmproj-offload` when the accelerator cannot hold them), so
            rendering one as a fallback-derived ON would let "flipping the
            switch to what it already shows" silently change the launch shape.
            Auto sends `{reset:[knob]}` — the only spelling that clears an
            override — so there is now a UI path back to "the planner decides"
            for all three booleans; before that, an offload knob once set could
            only be unset by hand-editing `config.yaml`. The recorded plan's own
            outcome is quoted beside each knob so Auto is never a guess:
            `plannedFitArg` for `-fit`'s literal value, and the pure
            `plannedBoolArg(plan, knob)` for the two offloads, which answers
            "on device" or names the host-RAM flag the plan actually launched
            with ("in system RAM (-nkvo)" / "in system RAM
            (--no-mmproj-offload)").
            The two COUNT-BEARING mode switches (Context → Exact, offload →
            N layers) are **drafted locally and persist nothing** until a count
            is committed: picking the mode flips a local draft flag and seeds
            the count field from the recorded plan, and a persisted override
            retires the matching draft, so a half-filled mode can never write a
            knob whose required companion is still empty. The same guarantee
            covers the COMMIT path: the shared `NumberField` primitive commits on
            blur OR Enter, and only AFTER AN EDIT — an edit-dirty flag, not a
            `parsed !== value` comparison — so focusing and blurring a count
            field whose rendered value is a FALLBACK (an unset knob showing the
            planner's own answer) never pins that fallback as an override nobody
            chose, which would be a real launch change (`cache_ram_mib: 0`
            DISABLES the prompt cache, `-ngl 0` is an all-CPU shape) while still
            committing a value the operator deliberately typed that happens to
            equal the fallback. An EMPTY or whitespace-only draft is "no value",
            never zero — and the field does not trim to reach that branch: the
            number input's own value sanitization already replaced any
            `e.target.value` that is not a valid floating-point number (which
            cannot carry surrounding ASCII whitespace) with `''`, so a
            whitespace-only draft arrives empty. It reverts to the rendered
            value and persists nothing, so
            an emptied field can never commit `0` to a knob whose floor IS 0
            (Layers, Fit target, Prompt cache, Host reserve) — and it is reachable
            without a select-all+Backspace, because `<input type="number">`
            reports `''` for any not-yet-valid content too (a half-typed `1e` on
            the way to `1e3`). Enter commits exactly like blur — the gesture the
            auto-unload minutes field has always used — but it does NOT clear the
            focus flag, because `type="number"` outside a `<form>` gives Enter no
            default action of its own: the DOM node never blurs, so a `focused`
            of false would disagree with it and the field would render the
            authoritative value while every further keystroke still landed in
            the draft, unseen, to be persisted by the eventual real blur. Both
            commit paths consume the dirty flag and re-seed the draft from the
            commit's OWN outcome (the accepted number, or the value a rejected
            entry reverted to), so rendered text and draft cannot diverge and
            one gesture persists once — a following bare `focusout` is a no-op
            unless the user genuinely typed something new after the Enter. Every
            control is a Combobox / NumberField (a native <select> is
            banned in settings and there is no slider primitive); the drafts
            and the commit flow live in the hook, local validation refuses an
            out-of-range value WITHOUT a round trip — and, on every knob whose
            wire type is an `int`, a non-INTEGER one the same way (`NumberField`'s
            `integer` prop, backed by `wholeInRange` in `@/api/embeddedTuning`,
            whose refusal reads "must be a whole number within <min>-<max>"):
            `<input type="number">`'s default `step=1` constrains only the
            SPINNER buttons, so a typed `2.5` would otherwise travel all the way
            to Go and come back as a raw `json: cannot unmarshal number 2.5 into
            Go struct field … of type int` painted in the error line. The one
            `*float64` knob (`host_reserve_gib`) keeps the plain `withinRange`
            check. A value the backend
            reports as 0 / absent / NaN / out-of-range falls back to the knob's
            documented default instead of painting an uncommittable entry (and
            an UNSET knob keeps its own `auto` spelling rather than a
            fallback-derived boolean — which is what all three boolean
            tri-states depend on), and a commit
            runs `SetEmbeddedLLMTuning` (a PARTIAL patch naming only the
            changed knob — absent keeps, present replaces, Auto resets to
            unset) → busy/error → a re-read of BOTH the tuning and the status
            in success AND failure. A write NEVER restarts a resident model:
            the status's `reload_required` renders a pending-changes hint, and
            Unload + Load is the explicit apply path.

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
            "Nothing to say" is exactly: no snapshot yet, not installed with no
            pending failure, and installed-but-stopped (an idle install is not an
            event — the Settings block is where it is acted on).
            `status.available` is deliberately NOT part of that contract: it is
            false only before startup (the agent directory is unset), in which
            case the same snapshot also reports nothing installed, so the flag
            carries no surface information of its own — and folding it in would
            let an "unconstructable subsystem" hide a real error the snapshot
            reports. The surface follows the install/supervision fields only, so
            an erroring snapshot still paints.

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
            The loaded surface MAY additionally append the ACTIVE session's
            median output-token throughput — visible "name · N tok/s" (at most
            one decimal, trailing ".0" dropped), tooltip part "end-to-end per
            request · median of last N samples". The metric comes from the
            session's cached token info (chatStore `sessionTokens`, fed by
            `session_tokens` events), and is GATED in the pure derivation
            (`lib/embeddedModelView.ts` `sessionThroughput`), never in the
            component: it renders only when the session's tracked model IS the
            resident embedded model (normalized trim + case-insensitive
            comparison of `tokens.model` against the resident name, composite
            "embedded/…" selectors reduced via `bareModel`, with the bare
            `model_id` fallback on the snapshot side) AND the median rests on
            at least three per-call samples (below that a median is noise —
            the backend omits the field anyway). Gated off — no active session,
            a foreign-model session, a legacy payload, or too few samples —
            the surface renders exactly as it did before the metric existed.

error      → an explicit hint carrying the backend's message (truncated in the
            bar, whole in the tooltip, which also points at Settings → LLM).
            SUPERVISION errors only: `state: "error"` (a launch that failed, a
            resident process that died). Install/download failures NEVER paint
            the bar — the backend retries resumable transfer failures silently
            (the download bar simply resumes from the kept partial), and a
            fatal one is carried by `install_error`, a field this block does
            not read, because the Settings block — where the retry button
            lives — is the single surface for it.
```

## Invariants

**Hardware probe and resolution:**

- Total RAM is a hard input: when it cannot be read the probe returns `ErrRAMUnknown` rather than guessing, and `resolveWith` refuses a non-positive total itself — because the gate derives its HOST budget from that figure on *every* path, measured topology or not. An unreadable **device** budget is the opposite case and is deliberately not fatal; see the memory-gate invariants below.
- **There is no RAM threshold.** ADR-067 D6's flat 16 GiB floor is gone, and nothing replaced it with another round number: viability is wherever the smallest shape `memory.go` has measured stops fitting, which is a fact about the model and both of the machine's memory pools rather than a constant anyone has to remember to revisit. `MinRAMGiB` no longer exists.
- `Resolve` is a pure function of its `ResolveInput`: no I/O, no probing, no clock. That is what keeps the whole platform × backend × RAM × topology × tuning matrix table-testable.
- The accelerator ladder is fixed (`nvidia-smi` → `nvcc` → ROCm tools → `vulkaninfo` → Metal on darwin/arm64 → CPU) and first hit wins. Every external probe is bounded by a 2 s budget, so a wedged driver cannot stall installation; an absent probe helper is a normal outcome, not an error.
- A detected CUDA version older than the oldest pinned asset tag is not a hit: the ladder keeps looking instead of provisioning an archive the driver cannot load.
- Resolution degrades an unsupported backend rather than refusing it, and a CUDA tag with no archive for the platform clamps DOWN to the nearest older pinned tag — never up, because a binary built against a newer toolkit will not load on an older driver.
- `PQ2_0` is the default packing, and `PTQ1_0` is selected only when one of four documented triggers fires — Vulkan (the one backend with no `PQ2_0` kernels), an AVX-512 host on a pin that predates `#245`, a **measured** device budget `PQ2_0` does not fit, or an Ada/L4-class GPU (the generations the model card measures as `PTQ1_0`-faster for decode). The decision is one pure table (`decidePacking`) with a documented precedence, and every outcome carries a typed `PackingReason`.
- **Installed RAM is not a packing input.** The old claim that "packing is independent of RAM" was half right and has been replaced: RAM *alone* still never downgrades the packing — the constraint that `PQ2_0` can exceed is the accelerator budget, not system memory, and it downgrades only when that budget was actually measured (`FitInsufficient`) — the verdict ADR-067's gate produces, which is what replaced ADR-067 D2's unmeasured *"whenever memory is short"* clause rather than reinstating it. An unmeasured budget (`FitUnknown`, the zero value) never downgrades anything, which is why `BudgetFit` and `CPUFeature` are tri-state: a bool's zero value would assert a fact nobody measured.
- The packing decision is **pin-aware**: the AVX-512 trigger compares the pinned fork build against the first build proven to contain `#245`, and an unproven or unparseable build number is treated as unfixed — the conservative direction, because the failure it guards is a segfault at load.

**Backend compatibility guards:**

- Every guard cites the upstream report that justifies it (`GuardDecision.Issue`, `<repo>#<number>`) and carries a typed `GuardReason`. A guard invented from a hunch is a bug: the table is a transcription of the pinned model's `KNOWN_ISSUES.md`, not a theory about hardware.
- `CompatibilityGuards(backend, gpuFamily, platform)` is a **pure** table lookup: no I/O, no clock, no failure, and a machine nothing is documented against gets no decisions at all. That is what makes the whole backend × family × platform matrix table-testable.
- Severity is **derived** from the reason, never hand-set per guard, and an unranked reason maps to `critical` — an unclassified failure must never render as the mildest thing on the list.
- A guard substitutes a backend only when the registry **pins** that backend for the platform. Otherwise the decision is recorded unapplied with the reason in its guidance: converting an upstream failure into c0wrk's own `ErrArtifactNotPinned` would be strictly worse than the failure.
- **No guard changes behavior without recording a reason, and no decision is dropped because it could not be applied.** Every decision — applied or not — travels `Resolution.Guards` → `InstallReport.Guards` → `Manifest.guards` → `EmbeddedLLMStatus.guards`, so a degraded install is visible in Settings after the installing process has exited. `Applied` is set by the consumer, never by the table: the table states what *should* happen, `Applied` records what *did*.
- Guards are evaluated twice, because one install learns about the machine twice: the statically decidable half at plan time (before any byte is fetched), and the device-dependent half from a device probe — against a previously installed runtime when there is one (so a repair applies substitutions *before* downloading), otherwise against the freshly staged runtime, which is still early enough to change the weights but too late to change the runtime. A substitution discovered too late is recorded as guidance, never silently discarded.
- The device probe that feeds a guard is **fail-soft**: no answer leaves the plan on its static guards. A compatibility guard refines a plan; it is never a precondition of one.
- `GPUFamilyUnknown` fires no GPU-specific guard and pays the largest memory allowance. It means "nothing recognized", never "no accelerator" — so a classification miss degrades to the plan c0wrk would have made before the guards existed.
- **Every pin bump re-reads `KNOWN_ISSUES.md` and the model card** and re-checks this table. A fixed upstream issue means a guard that should stop firing, and a stale guard is a needless degradation of a working machine.
- The context size is always an explicit positive value drawn from the five RAM tiers. `Resolve` never emits `0` ("let the server choose") and never the model's full `262144`-token training context: both are memory-unaware and OOM a constrained machine once `-ngl` offloads the KV cache.
- The tier comparison floors RAM to a whole GiB, matching the integer arithmetic the tiers were derived with, so a Linux machine reporting slightly under its nominal size lands in the tier its real capacity belongs to.
- The vision projector is part of every resolved set, for every backend and packing.
- The manifest and the informational Settings label record `Resolution.Backend` (the effective backend), never the raw probed value.

**Device memory topology:**

- Accelerator memory is always **detected**, never assumed from `GOOS`: unified pools exist on Apple Silicon, AMD APUs / Strix Halo, Intel iGPUs and Jetson-class CUDA SoCs alike, and `MemoryTopology.Unified` is the only authority on whether device memory and host RAM are the same bytes.
- A unified pool is **never summed** with host RAM. The pool is the *largest* device (not the sum), and the device budget is `min(pool − margin, hostBudget)`, so on a unified machine the device budget can never exceed the machine's physical RAM. On the reference machine a naive `RAM + VRAM` sum claims 235 GiB where 128 GiB exist — a 1.84× overcount that a fit decision would act on.
- An inconclusive classification resolves to **unified**: misclassifying a discrete card can only shrink its budget through the `min()`, while misclassifying a unified pool invents memory that does not exist.
- The topology probe is **fail-soft in every direction**: a missing, hung, nonzero-exit, cancelled or unrecognized answer yields `ok = false` with a Debug log — never an error, and never a failed load. `ok = false` means *unknown*, not "no memory", and the caller treats the accelerator budget as `deviceUnreadable` — degrading the memory gate to the host pool and recording it in `Notes`, never refusing the machine for it.
- The probe spawns only through the shared hardened path (`runProbeCommand`), so it inherits the `exec.LookPath` resolution, the 2 s `probeCommandTimeout` bound, the `probeWaitDelay` pipe deadline and `sysproc.HideConsole`: a wedged runtime cannot stall a load, even when its child leaves a grandchild holding stdout.
- Every device the parser keeps is a strictly line-anchored match of one of the runtime's two own format strings. An output with nothing recognizable is a **failed probe**, never an empty inventory — while `Available devices:` with no entries, or `  (none)`, is an answered probe reporting no accelerator.
- Entries that report no memory of their own (`Total == 0 && Free == 0`, measured as `BLAS: Accelerate`) are dropped — and the drop is counted and logged, never silent, so a machine whose only accelerator failed to report a size is distinguishable from one with no accelerator. Duplicate names collapse to the first, and the `CPU` row of the `-lv 4` parameter dump never enters the accelerator inventory — it is host RAM, used only as a Debug cross-check against the OS RAM probe.
- Both budgets are non-negative, and the host budget is never reduced by a device footprint: on a unified machine the two numbers describe the same bytes from two sides and the clamp lives in the device budget alone, so adding them is meaningless by construction rather than by convention.
- The topology is **model-agnostic** — it carries no weight, KV-cache or projector figure. The requirement side belongs to `memory.go` (`ProjectDeviceMiB` / `ProjectHostMiB` / `MMProjReserve*MiB`), and the 1536 MiB device margin deliberately excludes the projector reserve so a consumer that adds it does not count it twice.
- Every topology carries a `ProbedAt` RFC 3339 stamp, because free memory and even the device list change with the driver and the build: a recorded topology is always attributable to an instant.

**Memory planning:**

- `Plan` is **pure**: no I/O, no probing, no clock, no package state. Every input arrives as a value, so the whole (topology × profile × tuning × backend × GPU family) matrix is table-testable with no runtime, no GPU and no filesystem. The guarantee is structural as well as behavioural — `plan.go` imports nothing that can touch a file, a socket, a subprocess or the clock, and a test fails the build if it ever does.
- **Exactly one decider sizes the launch.** `Offload == Auto` ∧ no `Devices` ∧ `SplitMode == Auto` → `-fit on`, `-ngl` **omitted**, `-c` zero, and the runtime sizes both. Any one of those three pinned → `-fit off`, the values passed explicitly, and the context **computed from `ModelMemoryProfile`** because nothing else will. This is not a preference: the pinned fork's `common/fit.cpp` throws when `--fit` is asked to size an argument the caller already pinned (`-ngl 99` beside `--fit on` aborts the launch, confirmed empirically), so a plan that emitted both would not degrade — it would crash.
- `MemoryPlan.Layers` is a `*int` and `nil` means **omit `-ngl`**, which is materially different from a pointer to `0` (`-ngl 0`). The distinction is what the exclusivity rule turns on, and it is why `EmitsLayers()` exists rather than a layer count that has to be reinterpreted.
- A `FitEnabled` override never breaks exclusivity: `true` beside an explicit offload **loses** and is recorded in `Notes`. An exact `Context` does *not* force fit off — `--fit` adjusts only *unset* arguments, so a pinned `-c` survives beside `--fit on`.
- `-fitc` is **never** the runtime's own 4096 default. `DefaultFitMinContext` is 65536, and a test fails if the two are ever equalized: a 4096 floor reproduces the pinned model's most-reported failure (empty/truncated answers, whose documented workaround is `-c 65536`) on every machine tight enough for fit to shrink.
- `-np` is **1** unless overridden, never the runtime's `-1` (auto): auto was measured to inflate fit's projection 24450 → 77297 MiB and to split `-c` across slots, so a plan that verified a 65536-token budget under `-np 4` would be provisioning four 16384-token slots.
- The KV ladder only ever descends in precision (`f16 → q8_0 → q4_0`), never below `q4_0`, and every escalation is recorded in `Notes`. A pinned precision is honoured as given and **never** escalated; a precision outside `memory.go`'s closed set is refused, never coerced to `f16`.
- The ladder's fit test is **unified-memory aware**: it is `budgets.fits`, not two independent comparisons. On a machine where the accelerator bytes alias host RAM, a shape whose device and host footprints EACH fit a pool can still need twice the machine's memory in sum, so `fits` prices them against the single pool (`deviceMiB+hostMiB <= hostMiB`) — the same rule `gateBudgets.overflow` applies on the gate's side of the arithmetic — while still checking the device term on its own, because the topology's margin and the family's split allowance live in the device budget alone. Per-pool gating here is what turned a narrow unified-memory configuration into a failed launch: the escalation settled on a precision the runtime's own `--fit` pass could not fit even at the `-fitc` floor, so the load aborted with the `FitWarning` marker instead of escalating to `q8_0` as the gate's verdict would have.
- **The gate is combined and unified-aware, and it is the FIRST check** — before any asset is planned, and so before `Install` creates a directory or fetches a byte. It prices both pools the way the machine actually spends them: ONE shared pool when accelerator memory aliases host RAM (the two footprints are then *additive* against it, and checking each against its own budget would pass a launch needing twice the machine's memory), two independent budgets when it does not.
- **An unreadable device budget degrades; it never refuses.** The accelerator axis is tri-state (`deviceAbsent` / `deviceKnown` / `deviceUnreadable`) for the same reason `BudgetFit` is: "no accelerator" and "an accelerator nobody measured" have OPPOSITE consequences, and a bool's zero value would assert one of them about every caller that measured nothing. Only a backend whose memory is *independent* of host RAM (a discrete CUDA or ROCm card on amd64 — `backendHasIndependentVRAM`) can be `deviceUnreadable`; a unified or absent accelerator is priced from the RAM probe, which measured both. That is why 8 GiB of RAM beside a CUDA card degrades while 8 GiB of Apple Silicon refuses.
- **Whatever `Resolve` returns fits the budgets it reports** — on every path, measured or derived. Admitting a machine because *some* modelled shape fits is only half the job: the RAM-only planner's defaults are platform-derived (`-ngl 99` on Apple Silicon, an f16 cache), not memory-derived, so a 13 GiB unified machine was once admitted on a host-resident PTQ1_0 shape and then handed a device-resident PQ2_0 plan 145 MiB too large. Both defaults therefore yield to the gate's verdict wherever the operator pinned neither, and the gate's packing verdict is fed back as `FitsPQ2_0 = FitInsufficient` so `decidePacking`'s rule 3 selects the smaller weights *and* records why. `TestResolveNeverEmitsAPlanThatOverflowsItsOwnBudget` ratchets the property over the machine matrix, re-projecting each plan's footprint from `memory.go`'s own API so the assertion is not circular with the planner.
- The gate's device axis follows **`layersFor`, not `backend.gpuAccelerated()`**, because the gate must price the shape the plan will actually emit. On Apple Silicon the arm64 archive IS the Metal build even when the effective backend came out as `cpu`, so a gate keyed on the backend label would see "no accelerator" there and price a host-resident shape nobody launches.
- **A shape that does not fit is degraded before it is refused**, and the ladder is ordered least-lossy first: KV/projector spill to host → context down to `DefaultFitMinContext` → both → host residency → host residency with the reduced context. A **partial offload is never a rung**: `memory.go` has no measurement for one, and an unmeasured split is a guess a capacity gate must not make. Every rung that fires is recorded in `Notes`, so a degraded install states its degradation instead of leaving the user to infer it from a slower generation.
- An operator-pinned offload shape gets **no ladder**: honouring the pin and reporting the refusal beats quietly launching a different shape than the one asked for. The refusal still prices only the residency that was pinned, so its numbers describe a launch somebody actually requested.
- A refusal is an `*InsufficientMemoryError` and it **names both pools with both numbers** — need, available, the reserve the host figure was derived with, the installed total and the smallest shape priced — so a UI can show why instead of a bare "not enough memory". It unwraps to a chain (`ErrInsufficientMemory`, `ErrMemoryPlanInfeasible`, and the deprecated `ErrInsufficientRAM` *only* when the host pool overflowed), so a caller may match any of the three without parsing a message and a device-side refusal never reports itself as insufficient system RAM.
- A shortfall under 1 GiB is rendered in **MiB**, because a refusal that reads "needs 8.0 GiB, 8.0 GiB available" tells the user nothing: the two figures differ by an amount one decimal place cannot show, and the difference is the whole reason for the refusal.
- `normalizeTopology` derives the budgets of a topology that carries an inventory but none — the same reserve policy, device margin and unified clamp `buildTopology` uses — because `MemoryTopology`'s zero value means *unknown*, never "no memory", and reading a zero `DeviceBudgetBytes` beside a 24 GiB inventory literally would degrade a machine that has plenty. It never re-classifies `Unified` (topology.go names that field the only authority, and re-deriving it would need a platform key `Plan` does not take) and never derives a budget for a pool that fits inside the margin (that is a measurement, not an omission).
- A tuning typo is `ErrTuningInvalid` and is checked *before* any feasibility arithmetic, so an operator is never told their machine is too small when what they typed was out of range.
- `splitAllowanceMiB` is **policy, not measurement**, and is labelled as such in the source: it is the only pair of numbers in `plan.go` not derived from a cited figure. It exists because `memory.go`'s device/host split was measured on Metal alone. Apple Silicon pays 0, a recognized-but-unmeasured family 1024 MiB, an unrecognized part 2048 MiB — unknown means unknown in both directions.
- A **partial** layer count is the one shape the measured profile does not cover, so its expected figures are conservative **bounds** (full-offload device, CPU-only host — both err towards refusal) and the plan says so in `Notes` instead of presenting an estimate as a measurement.
- `Expected*MiB` always **include** the vision projector's reserve (849 device + 25 host measured), because `memory.go`'s two projections are text-only and c0wrk always passes `--mmproj`. A consumer that adds the reserve again counts it twice.
- `Notes` explains **every** non-default decision and is surfaced in the UI verbatim, so each entry is a sentence an operator can act on and none is a debug dump.
- `Tuning`'s zero value is the **all-Auto** plan and every field distinguishes *unset* from *set to the default value*. A field whose zero already meant "chosen" would make `-ngl 0` indistinguishable from "no opinion about `-ngl`", which is the distinction the exclusivity rule reads.

**Supply chain (ASI04):**

- Every artifact version is a compile-time pin in `core/embeddedllm/registry.go`; the subsystem performs no upstream version queries and no automatic updates. A version advances only when a developer raises the pin after a CVE review.
- Both pins are **immutable refs**: `RuntimeTag` is the fork release tag and `ModelRevision` is a Hugging Face commit SHA, never a branch — so a rebuild cannot silently re-point at whatever upstream published last. Runtime checksums are the GitHub REST per-asset `digest`; model checksums are the HF LFS OID, which *is* the SHA256.
- `ValidateRegistry()` self-checks the pin tables and is the guard that turns a hand-edited table into a failure instead of a supply-chain hole: every asset must carry an HTTPS URL under its pinned ref, a 64-character lowercase hex digest, a positive exact size and a plain file name; the component must match its table; a `cudart` may never be pinned without its runtime; and every supported platform must pin a CPU fallback.
- A `(platform, backend, packing)` combination the pin does not cover fails closed with `ErrArtifactNotPinned` — never a neighbouring platform's or backend's bytes. The pinned release has **no** `windows-amd64` + `cuda-12.8` archive (and no matching `cudart`) although linux does ship `bin-linux-cuda-12.8-x64`; the registry refuses that pair and `Resolve` clamps the CUDA tag down to a pinned one. `windows-arm64` is out of scope entirely (D9), so it is not even a supported platform key.
- Every downloaded byte is SHA256-verified before use, **fail-closed**: an empty or missing checksum for the platform refuses the install rather than skipping verification, and a mismatch deletes the partial file and returns an error. Verified bytes are never accepted from an unverified path.
- `manifest.json` is written only after every component has been verified; a failed verification leaves no manifest and registers no provider.
- The runtime is a pinned fork release (`prism-b10735-842b188`), never upstream llama.cpp: stock rejects `PQ2_0`/`PTQ1_0` and silently produces garbage on `Q2_0`.

> **Pin bump 2026-09-25 — `prism-b10709-9a9394a` → `prism-b10735-842b188`.**
>
> [ADR-067](../decisions/066-embedded-llm-runtime.md) D11 records the pin as `prism-b10709-9a9394a`. Accepted ADRs are immutable (`META.md`, "How to update" rule 5), so the **current** value lives here and in `registry.go` — D11's literal tag is now historical and must not be read as the pin. Read D11 for the *policy* (no upstream version queries, no auto-update, hand bump after a CVE review, digests from the GitHub REST per-asset `digest` field) and this note for the *value*.
>
> **Why it moved.** Five correctness fixes landed 2026-09-21/23, *after* `b10709` was published (2026-09-18), so the previous pin was known-broken on whole machine classes: `#245` `PQ2_0` segfaulted on AVX-512 hosts (Zen 4/5, Strix Halo — even with every layer on the GPU), `#238` `PQ2_0` on Vulkan silently ran on the CPU at <2 tok/s and `PTQ1_0` was slow on Intel Xe2, `#206` had no fast x86 `PQ2_0` kernels, `#196` left the Metal tensor API disabled on the newest Apple chips, and `#205` refused MTP. A resolver that computes a perfect plan for a runtime that segfaults or silently runs on the CPU is worthless, so the pin is a correctness surface, not only a supply-chain one.
>
> **CVE review of the 26-commit range (`9a9394a..842b188`) — outcome: no security-relevant change, bump approved.** The range is linear and fork-only (`ahead_by 26`, `behind_by 0`) with **no upstream `llama.cpp` rebase**, so upstream CVE exposure is unchanged from the already-reviewed pin. All 44 changed files sit under `ggml/` (cpu/cuda/metal/sycl/vulkan backends), `src/llama-*`, `tests/`, `docs/` and `conversion/base.py`: **nothing under `tools/server/`**, no CI workflow, no root `CMakeLists.txt`, no download or build script. `common/arg.cpp` is **byte-identical** between the two tags, so the loopback HTTP/OpenAI surface c0wrk exposes did not move. The fork publishes **no** GitHub Security Advisories. An added-line scan for process/network/filesystem/dynamic-loading primitives (`system`, `popen`, `exec*`, `fork`, `socket`, `connect`, `curl`, `dlopen`, `LoadLibrary`, `fopen`/`fwrite`, `eval`, `subprocess`, URL and credential literals) found one benign hit: a `getenv("PQ2_SGEMM")` kill-switch that can only *disable* a fast kernel path when set to `0` and never enables anything (c0wrk does not set it). The one suspicious-looking commit (`ea50aba8c` `#233`, subject "# Pull request — testo pronto da incollare su GitHub") was inspected individually: a legitimate AVX2/AVX-VNNI `Q1_0` 4x8-repack GEMV/GEMM performance contribution touching only `ggml-cpu` `repack.cpp`/`repack.h`, whose subject is an Italian PR-template placeholder pasted verbatim. The range is net **memory-safety-positive** — `#245` keeps Hadamard rotation tensors out of `CPU_REPACK` buffers and adds a fail-loud `throw` where such a table was previously misused silently. The new SYCL backend (`#235`) is unreachable: c0wrk ships no SYCL artifact.
>
> **What the re-pin preserved.** All 16 runtime entries were re-pinned from the GitHub REST per-asset `digest` field and are now locked verbatim by `TestRegistryRuntimePinsMatchUpstreamRelease` (previously only the *model* digests were pinned verbatim; runtime digests were checked structurally). The release is shape-identical — still 23 assets, same names modulo the tag — and the two Windows `cudart` digests are **byte-identical** across the tags (`8c79a9b226de…`, `1462a050eb4c…`), so only runtime digests changed. `windows-amd64 + cuda-12.8` is still absent, so the clamp-down invariant and `TestRegistryWindowsCUDA128Gap` hold unchanged, and `windows-arm64` stays out of scope (D9).
>
> **Launch-flag migration in the same change.** `LaunchSpec.Args()` now emits the canonical `--no-ui` instead of the legacy `--no-webui`. The pinned fork registers the switch as the alias pair `{"--ui", "--webui"}` / `{"--no-ui", "--no-webui"}` in one `common_arg`, and `--ui`-first is the vocabulary every related flag uses (`--ui-config`, `--ui-mcp-proxy`), so `--no-ui` is canonical. Both spellings are accepted at this pin — verified in the pinned tree, where **no** deprecation warning for `--no-webui` exists — so this is a spelling migration, not a behavior change, and the D12 guarantee (Web UI off, loopback only) is untouched. Because the runtime is a compile-time pin whose `arg.cpp` was verified to accept `--no-ui`, no rejected-spelling fallback was added: it would be dead code guarding a binary this build cannot spawn. `TestLaunchSpecAlwaysLoopbackAndNeverAgentFacing` now additionally **forbids** `--no-webui`, so a stale legacy flag cannot survive silently.

**Download:**

- Both pinned hosts honour HTTP `Range` — **verified experimentally**, so resume is the normal path and the restart fallback is a defence rather than the design. Hugging Face `resolve/<revision>/<file>` answers `302` → `206` with `accept-ranges: bytes` and an exact `content-range` (the `302` also carries `x-linked-size` and `x-linked-etag`, the latter being the SHA256 LFS OID); GitHub `releases/download/<tag>/<asset>` answers `302` → `206` the same way. Both answer `416` for a range beyond EOF. `net/http` copies the `Range` header across the redirect (only `Authorization`, `Cookie`, `Cookie2` and `WWW-Authenticate` are domain-gated), so a single ranged request resumes.
- A transfer is never silently partial. Two explicit fallbacks exist, both logged and reported on `Result.Restarted` + `Result.RestartReason`: a host that answers `200` to a ranged request ignored `Range`, so the partial is truncated and the full body is written from byte 0 — appending a complete object onto a partial would silently corrupt the artifact; a host that answers `416` cannot be reconciled with the partial, so the partial is discarded and the download restarts from byte 0. Restarts are bounded by `maxTransferAttempts` (2): a host that keeps rejecting ranges is an error, not a retry loop.
- A transfer that ends short of the pin — dropped, cancelled, **or a body that closed cleanly early** — keeps the partial and returns `ErrIncompleteTransfer`, so the next call resumes rather than restarts. A clean EOF from the server is not a completed download: the pin's exact byte size is the authority, not the transport's idea of success.
- A partial transfer lives at `<destination>.part`. The destination path only ever holds bytes that passed SHA256 verification, because verification happens *before* promotion (`os.Rename`) — and the partial file is not even created until the server has answered, so a failed request leaves no litter. A complete-but-unpromoted `.part` (a crash between the last byte and the rename) is verified and promoted with no request at all.
- The resumable partial is never opened through a symlink. `discardForeignPartial` `os.Lstat`s the path and discards anything that is not a regular file this package wrote before the `O_RDWR|O_CREATE` open, which would otherwise FOLLOW a pre-planted `<dst>.part` symlink and land the truncate plus every appended byte on the link's TARGET — pinned artifact bytes written at an attacker-chosen offset into an arbitrary same-user path, a side effect no verification can undo even though the digest gate still fires and deletes the link rather than promoting anything. `os.Lstat` is the portable check because `O_NOFOLLOW` is not available on every platform this runs on, and nothing legitimate can put a non-regular file there. A residual TOCTOU window remains between that check and the open (an actor with same-user write access to the downloads directory could swap the path in between); closing it portably would need an `O_EXCL` create-and-rename per chunk, which a resumable multi-gigabyte transfer cannot do.
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

- Install's gates run in a fixed order and each one is a gate: **stop a resident server (step 0)** → probe → resolve (RAM first) → ensure roots → whole-set disk guard → port → runtime phase → macOS provisioning → promote → weights phase → manifest → config sink. The step-0 `Installer.Stop` mirrors Remove's: it is a no-op when nothing is resident, and a stop failure aborts the install before anything is fetched. It is also the ONE bounded stop whose budget cannot ride on a context — the install runs on the deadline-free APPLICATION context because a multi-gigabyte download must not inherit a timeout — so the backend's 30 s reaches core as the exported `Installer.StopTimeout` field (`0` = inherit the caller's ctx) and `stopBeforeInstall` wraps that one call in `context.WithTimeout`. The bound is the difference between a repair and a stall: an expired budget is not a worse outcome than the wait, it is what makes core's no-gate force path reachable, so a repair clicked during a cold load either takes the resident child down through it and PROCEEDS, or fails with the translated actionable error — `embeddedBoundedStopErr("the pre-install stop", …)` inside core's `stopping the inference server before installation:` wrapper. The stop precedes the probe because a resident server executes out of the runtime tree `promote` retires (renaming a live process's own tree and working directory is refused outright on Windows) and holds the gigabytes the probe and the memory gate would otherwise price as unavailable — so clicking Install or repair on a machine with a loaded model UNLOADS it. Nothing is downloaded before the RAM and disk gates have passed, and no directory is created before the RAM gate has.
- The backend guards the whole subsystem with ONE **operation gate**, not an install-only slot — CLAIMED by the two mutating RPCs and only READ, with a refusal, by everything else: `beginEmbeddedOperation`/`endEmbeddedOperation` track which kind (`embeddedOpInstall` `"install"` | `embeddedOpRemove` `"removal"`, with the zero value `embeddedOpIdle` meaning the gate is free) is in flight, and **Remove holds it too**. Install, Load, `UnloadEmbeddedLLM` and `ProbeEmbeddedLLMDevices` all refuse while any embedded operation owns it, and so does the **request-path loader** (`embeddedLoaderRef` refuses "loading the model for a request"), so a cold LLM request cannot start a load against a tree a removal is deleting. `CancelEmbeddedLLMInstall` is the one deliberate exception on both sides: it neither claims nor reads the gate, because a cancel click must not be refused by the very install it is meant to stop — it reads the install run's own bookkeeping (`installCancel`/`installCancelRequested` on the subsystem state, the flag recorded before the cancel fires) and the run it addresses releases the gate through its own defer. The gate is a Remove↔Install TOCTOU fix: `RemoveEmbeddedLLM` is a blocking RPC deleting multi-gigabyte trees, and an Install launched during it would write partial downloads and promotions into the same staging/runtime/model trees, producing a half-staged tree — or an install completing after `ApplyRemoved` and silently re-adding what the user just removed. The release is a `defer` registered FIRST in `runEmbeddedInstall` so it runs LAST: an observer reads the gate as "the run is over" only once every effect (the config save, the supervisor state, the completion event, a panic's failure report) is already visible, and a leaked gate would be unrecoverable for the process lifetime.
- The stop-shaped calls are BOUNDED — all FOUR of them — and the bound covers the GATE ACQUISITION rather than the whole stop. Three (the shutdown stop, `RemoveEmbeddedLLM`, `UnloadEmbeddedLLM`) hand core a 30 s budget (`embeddedStopTimeout`, overridable per call by `stopBudget`) as a wrapped context rather than the never-deadlined app context; the fourth — an install's step-0 stop — cannot be bounded that way, because the install deliberately runs on the deadline-free APPLICATION context (a multi-gigabyte download must not inherit a timeout), so the same figure travels to core as the exported `Installer.StopTimeout` field instead: `embeddedBuild` wires `installer.Stop = server.Stop`, `runEmbeddedInstall` re-resolves the budget onto its per-run installer copy, and core's `stopBeforeInstall` wraps only that one call in `context.WithTimeout`. All four exist for the same reason: core's `Stop` must first acquire the single-instance gate — which an in-flight `Load` can hold for the 15 min ready budget — so an unbounded stop would hang a blocking RPC, and a quit, for minutes, and would park a repair in step 0 (before `plan`, therefore before the FIRST `install_progress` event) with the progress bar at 0% while the operation gate made every other embedded call refuse. The stop that follows never runs on the caller's deadline: `unload` derives its OWN `stopTimeout + killWait` = 15 s once the gate is held, and `forceUnload` its own `stopTimeout + killWait + 1s` = 10 s + 5 s + 1 s = 16 s on the no-gate path an expired budget takes, both under `context.WithoutCancel` — so a budget nearly spent behind a cold load still gets the full graceful window rather than falling straight through to Kill. When the budget expires with a child still live, `forceUnload` force-terminates the child and returns SUCCESS once that stop takes, so the STOP's real ceiling is 30 s + 16 s = **at most 46 s** and not 30 s — a figure that is no longer force-path-specific, since both paths now budget themselves. The actionable error naming the likely cause (a load still in progress) is returned ONLY when no child had spawned yet — `forceUnload` found no run handle to kill, so the caller's timeout is the whole truth. What the bound guarantees is TRACKING, not termination: a stop that does NOT take — the wedged native child in an uninterruptible syscall that survives BOTH the graceful signal and the kill, which is what `defaultKillWait` exists for — makes `terminate` exhaust its own budget and return an error, and both stop paths then re-attach the live run handle and record `StateError` instead of dropping it, so a retry targets the same process. That is the one stop outcome that can still leave `llama-server` running; what no outcome leaves behind is a live child the supervisor has lost sight of. That 46 s bounds the STOP ALONE: in `RemoveEmbeddedLLM` the deletions (`removeOwned` on the model root, `removeRuntimeTrees`, `removeOwned` on the downloads dir) and the `ApplyRemoved` config save that follow it take no context — finite, but unbudgeted — so removing an install whose `downloads` dir still holds cached multi-gigabyte archives on a slow, encrypted or network volume can carry the RPC past it.
- The runtime is proven **before** the weights are fetched: a runtime that cannot execute (Gatekeeper, a missing GPU library) fails the install in seconds instead of after a multi-gigabyte download.
- Verification is enforced twice, and neither pass re-hashes a multi-gigabyte file: `Downloader.Download` never promotes unverified bytes, and `Install` independently refuses a result that is not marked verified or whose digest differs from the pin. Fail-closed is the only acceptable reading — "probably fine" is how an unverified binary ends up executed.
- `manifest.json` is written atomically (sibling temp + rename), and the install writes it once — only after every component verified and the runtime ran. A crash mid-write leaves the previous manifest intact rather than a truncated one, and a failed install leaves no manifest at all. The temporary name is **unique per write** (`os.CreateTemp` over `manifest.json.*.tmp`), not a fixed sibling: `Installer.Install` holds no supervisor gate while `Server.recordEffectiveContext` writes under one, so two writers can overlap, and a shared fixed name would let one rename a file the other is still writing — promoting a torn manifest the next `ReadManifest` then fails on, reporting the model as not installed until a reinstall. The post-ready context readback **merges onto the current on-disk manifest** (`ReadManifest` first) rather than clobbering it, so a load's refinement cannot drop a field the install recorded.
- The manifest records the *effective* backend, the pinned `RuntimeTag`, the verified digest of every downloaded component keyed by component name, the degradation record (`packing_reason`, `gpu_family`, `guards`) and the **`topology` + `plan` pair** a load launches from — enough for startup to restore state from disk alone.
- `Topology` and `Plan` are recorded as ONE coherent fact: a topology is persisted only when it actually informed the plan beside it, so a load's fail-soft path can read the pair as "the shape this machine was provisioned for, and the measurement it was priced against". A measurement the re-plan rejected is dropped rather than recorded beside a plan that ignores it. Both are nil-able, and nil means **unknown** — never "no accelerator" and never "no plan to launch".
- `ContextSize` is the **last known effective** context, not a tier frozen at install. Install writes `recordableContext(plan, resolution.ContextSize)` — the planner's own value for a computed shape, and the `FitMinContext` floor for a fit-sized one, because a fit plan has no concrete context and `0` would mean "leave the existing override alone" to `SyncEmbeddedLLMProvider` while reading as a lost tier in a manifest. Every successful load then overwrites it with the value the server reported through `/props`.
- Install and Remove both refuse without a `ConfigSink`: bytes that cannot be registered are bytes nobody can use, and a deletion whose provider entry survives points the router at nothing. A sink failure *after* the manifest was written keeps the manifest, because it is accurate — the retry re-downloads nothing and rewrites the same record.
- A failed install keeps the bytes that DID verify (they are cache hits for the next attempt) and writes no manifest, which is what makes a multi-gigabyte install resumable rather than restartable.
- Progress is per component and per stage. Each component reports `downloading` first and exactly one `done`, always against its own pinned byte total; the runtime group's `done` is deferred until the tree is provisioned, so a component's stream ends when it is usable rather than when its bytes landed.
- macOS provisioning is the only platform-specific branch, keyed off `Installer.HostOS` (the probe's goos in production). Both helpers are invoked by **ABSOLUTE path** (`/usr/bin/xattr`, `/usr/bin/codesign`), never resolved through `PATH`: they are part of the base system, and a PATH element an attacker controls (a hijacked shell profile plus an app relaunch — the app loads the login shell's environment at startup) would otherwise substitute a `codesign` that runs arbitrary code during a later install click. A missing helper is still a warning, not a failure — the smoke test is the authority on whether the runtime runs, and refusing to install on a machine without `/usr/bin/codesign` would be a worse outcome than trying — while a failing smoke test is fatal and its error names the exact Gatekeeper fix (`xattr -dr com.apple.quarantine <dir>`, or System Settings → Privacy & Security → "Allow Anyway").
- `Remove` stops the server before deleting anything (a live process cannot be deleted on Windows and would keep serving unlinked inodes on Unix) and clears the config last, even when a deletion failed. It is idempotent: removing an install that was never there is a no-op that still clears the config.
- Every external effect is an injectable field on `Installer` (`Downloader`, `Probe`, `RunCommand`, `AllocatePort`, `Stop`, `Now`, `HostOS`), so the whole flow — including the darwin-only branch — is tested on every CI platform against kilobyte artifacts served by an in-test HTTPS server, exercising the production download, verification, extraction and provisioning code rather than a mock of it.

**Server supervision:**

- The server binds strictly `127.0.0.1` — `LaunchSpec.Validate` refuses any other host — and its built-in Web UI is disabled with `--no-ui` on every command line. The server's own agent surface (`--agent`/`-ag`, `--tools`, `--cors-origins`, the MCP proxy) is never enabled.
- **The tuning surface is typed-only and cannot inject argv.** `LaunchSpec.Args` is the only place a command line exists, and every element it renders comes from a typed field, a fixed literal in that function, or an integer rendering of one — there is no free-form flag, argument or command-line string in the struct, and no value read from `config.yaml`. The four string-typed fields that do reach argv are each pinned by `Validate` first: `Host` to `LoopbackHost`, `KVType` to `memory.go`'s closed three-precision set, `SplitMode` to the runtime's closed `{none,layer,row,tensor}`, and every `Devices` entry to a single separator-free, dash-free token (so a name cannot smuggle a second device into the joined `-dev` element, or present the parser with something that reads as a flag). The three path fields come from the install record and the layout. `LayerMode`'s discriminator and count are private, so only its four constructors can produce an `-ngl` value. This is enforced structurally, not by review: `TestLaunchSpecArgsRenderTypedValuesOnly` parses `server.go`'s own syntax tree and requires that `Args` concatenate no strings, call nothing outside a declared rendering vocabulary, and read exactly an allow-listed set of `LaunchSpec` fields — and that every field of the struct is either on that list or is `ServerBinary`, the exec target. `TestLaunchSpecStringFieldsCannotSplitArgv` is the behavioural half: a hostile payload in a path field replaces exactly one element, in the same value slot, and the flag-position sequence is unchanged.
- `-fit` is rendered on **every** command line and never inherited from the binary. The pinned fork's own default is `on`, and `--fit` is an undocumented fork contract that stock llama.cpp does not have, so a pin bump could flip or drop that default; rendering the switch explicitly makes the launch shape a property of `LaunchSpec` rather than of whichever runtime happens to be pinned. `-np` is rendered unconditionally for the same reason, and because the fork's `-1` (auto) both inflates fit's compute reserve per stream and splits `-c` across slots — see `DefaultParallel`.
- Readiness is a `200` from `/v1/models` **with a non-empty model list**, never `/health`: a `200` whose list is empty keeps polling, because the fork's server opens its socket and answers both routes long before the weights are in memory.
- The ready wait watches the process as well as the clock, so a server that dies during the weight load is reported in milliseconds instead of after the whole ready budget.
- Load is idempotent and single-instance: a ctx-aware gate guarantees at most one `llama-server` per install, a Load against a serving model is a no-op that only marks activity, and a process left over from a stop that did not take is discarded before another one is spawned.
- The spawned process is deliberately NOT bound to the caller's context (`context.WithoutCancel`): a Load's context is usually one RPC, and the server must outlive it. Its lifetime is owned by `Unload`/`Stop`, the idle timer and app shutdown.
- The child's environment heads the platform's dynamic-library search path (`LD_LIBRARY_PATH` / `DYLD_LIBRARY_PATH` / `PATH`) with the runtime's own binary directory, and the separator and variable are derived from the target `goos` rather than from the host, so the policy stays a pure, table-testable function. Nothing is installed into a system location.
- The child's stdout and stderr are drained into `slog` (debug, tagged with the stream and the pid) and only then is `Wait` called — the ordering `os/exec` requires for piped output. A bounded 24-line tail of that output is attached to every load failure and every crash report, because "the process exited" is not a diagnosis and "ggml_cuda_init: found 0 devices" is. The same tail is **scanned for the fit contract's failure signature** — `failed to fit params to free device memory`, the abort spelling of the success line (`common_fit_params: successfully fit params to free device memory`) that ends every captured fit trace — and a hit is surfaced as `Status.FitWarning` (the `fit_warning` status field the Settings install record renders verbatim) instead of dying with the discarded run: a pin bump that breaks the fit contract must be REPORTED, not silently retried away. The warning describes the *last failed* launch and is cleared by the next launch that becomes ready (`TestLoadTimeoutSurfacesTheFitContractWarning`, `TestLoadDeathWithoutTheFitComplaintStaysQuiet`, `TestLoadReadyClearsTheFitWarning`; the scanner's spelling discrimination is `TestScanFitFailureMatchesTheFailureSpellingOnly`).
- Unload means terminating the process, which deterministically returns RAM/VRAM. It is graceful first (SIGTERM, then Kill after `StopTimeout`); a platform with no graceful child signal falls straight through to the kill.
- A stop that did not take is an error, keeps the process handle and never reports `installed`: claiming "not resident" while a process holds gigabytes would misreport the machine, and dropping the handle would let the next Load start a second server. The one consequence of keeping the handle is deliberate: a Load interrupted by that stop finds the run still published, so its ownership re-check passes and it claims the residency that is REAL — a child alive and answering its probe — replacing the force path's `error`, because reporting `installed` there would misreport the RAM still held. The failed stop is reported to the caller that asked for it.
- An unexpected exit — a clean status 0 included — transitions the state to `error` and emits `embedded_llm:state`; only an exit the supervisor asked for reports `installed`. A dead server is never reported as `loaded`.
- A failed Load marks its own process *abandoned* before discarding it, so one death is never reported twice (once as a load failure and once as a crash); state transitions that change nothing emit nothing.
- The state of a live process belongs to the supervisor: `SetInstalled` is refused with `ErrServerBusy` while one is running, so neither startup nor Remove can silently misreport a running model.
- The launch specification is validated at the last gate before exec, and the refusals are `ErrLaunchSpecInvalid` rather than a spawn: a missing binary or model; a non-loopback host; an unusable port; EVERY argv-bound numeric field outside its range on EITHER end — `-ngl` below 0 or above `MaxTuningLayers`, `-fitt` below 0 or above `MaxTuningMiB`, `-np` below 1 or above `MaxTuningParallel`, `--cache-ram` below `-1` (the runtime's own spelling of no limit) or above `MaxTuningMiB`, and `-c`, `-fitc` and `--image-max-tokens` below 0 or above the model's own training context (`maxTrainingContext`, the figure `ModelMemoryProfile.MaxContext` reports); a **zero** context with `-fit off`, since with fit off nothing else would size it; `-fit on` beside an explicit `-ngl`, `-dev` or `-sm`, and `-fit off` with no `-ngl` at all — the two halves of the exclusivity rule; `-fit on` with a non-positive `-fitc` floor, which would let the fork's own 4096 default reproduce the pinned model's most-reported failure; a `KVType` outside the closed set; and an unknown `-sm` or malformed `-dev` name. The floors are launch semantics; the ceilings come in the two kinds the Validate step of the [Flow](#flow) names — `limits.go`'s overflow and absurdity guards for the operator tunables, and `maxTrainingContext` for the three context-derived fields.
- The context **ceiling** at that gate is the model's own training context, not the resolver's top RAM tier. `contextTierTop` (131072) is still the largest context the RAM ladder in `resolve.go` ever *emits* — the ladder is unchanged — but a memory-aware plan can legitimately be held to a floor at the top of the modelled range, so the gate no longer refuses it. What the gate still refuses is anything memory-unaware: a negative context, a context above the training maximum, and a zero context nobody is going to size.
- **A load must not fail because a driver query wedged.** That guarantee used to be bought by never probing at load time; it is now bought by making the probe FAIL-SOFT, which keeps the guarantee and adds freshness. `--image-max-tokens` is still re-derived from the manifest's **effective** backend through the same pure policy `Resolve` uses, and the shape half still comes from a record rather than from a measurement that must succeed: `Manifest.Plan` when the install recorded one (applied through `ApplyMemoryPlan`), otherwise the pure policy's explicit `-ngl` / `-fit off` / recorded `-c`. On top of that record, `Server.ProbeDevices` MAY re-measure the accelerator, bounded by `LoadProbeTimeout` (`DefaultLoadProbeTimeout`, 10 s — an order of magnitude below the ready budget, because every cold start pays it, including one a first request waits on through the ensure-loaded transport).
- Every way that refinement can fail leaves the recorded decision in place and logs at Debug: the hook is unwired (the pre-existing contract, and what a caller that does not opt in gets), the probe wedges or answers nothing, `PinnedMemoryProfile` is unreadable, or the planner now refuses the machine it was handed. A load-time refusal is deliberately NOT a new failure mode — the memory gate that decides whether this machine may hold the model ran at **install**, when the bytes were chosen and the user accepted them, and a measurement at load refines that decision rather than re-litigating it.
- The probe is **opt-in**: `Server.ProbeDevices` nil means no probe runs at all, rather than defaulting to the package function the way `Installer.ProbeDevices` does. Production always wires it; a caller that does not gets exactly the older "no hardware probe runs at load time" behaviour and no exec it did not ask for.
- A load-time re-plan **pins the packing to the manifest's**. It is the one input the refinement must not re-decide: `Plan` derives a packing from the backend and GPU family whenever the tuning leaves it unset, and a different packing would price a footprint the installed GGUF does not have — so the `-c` and `-ngl` it returned would be computed for bytes that are not on disk. A packing change is an install decision, because it changes which multi-gigabyte file gets downloaded (`TestLoadReplanPinsTheInstalledPacking`).
- The operator's `Tuning` reaches a load through `Server.Tuning`, a **function** rather than a value: the supervisor is built once and cached, while a settings save can change `embedded_llm.tuning` at any point in between, so a load must plan with the tuning in force when it runs. It is read on the load path only, where taking `configMu.RLock` is already established as safe (`EnsurePort` → `persistEmbeddedPort` does the same), and an untranslatable section yields the all-Auto zero rather than failing the load.
- A refinement that changed nothing writes nothing: `planChanged`/`topologyChanged` gate the manifest rewrite, so a steady machine pays no disk write, no new mtime and no `config:updated` on every cold start (`TestLoadDoesNotRewriteTheRecordWhenNothingChanged`).
- **The context readback is the only correction the tier-1 override ever gets.** After readiness the load asks `GET /props` for `default_generation_settings.n_ctx` — the PER-SLOT figure — and multiplies it by `total_slots`, because reading the per-slot value alone would under-report a multi-slot server by a factor of `-np`. `DefaultParallel` is 1, so today the two figures agree — but `embedded_llm.tuning.parallel` is an operator knob, and the multiplication is what keeps the recorded window honest the moment somebody raises it. The product is persisted to the manifest and, through `Server.PersistContext`, to `llm.models."Bonsai 2 27B".context_window`. This is inside the existing contract, not an extension of it: [llm-providers.md](llm-providers.md) has always assigned that override to "the path that knows it", naming **install and load**, and the load path already spawned the process and waited for it — so the read costs no extra startup and `GetConfig` stays network-free.
- The readback is fail-soft in both layers. A `/props` that does not answer (a non-200, an unparseable body, a missing or non-positive `n_ctx`, an unreachable socket) keeps the recorded value and logs at Debug; an absent `total_slots` is read as one slot, not as zero. A manifest or config write that fails is logged and otherwise ignored, because the model is resident and serving and that is the fact the Load was asked to establish — the manifest is written first, so the next load retries only the config mirror (`TestReadPropsContextIsFailSoft`, `TestRecordEffectiveContextSurvivesAFailingConfigWrite`).
- The readback runs AFTER the `loaded` transition is emitted, not before. A `/props` read is normally sub-millisecond on loopback, but a wedged server could hold it for the whole `ProbeTimeout`, and delaying the "the model is ready" signal by that would be a worse regression than the one-event staleness it buys: the `embedded_llm:state` payload for that transition can carry the pre-readback `context_size`, while the cached install record, `GetEmbeddedLLMStatus` and the `config:updated` the write emits all carry the corrected one immediately after.
- `persistEmbeddedContext` does not rebuild the router SYNCHRONOUSLY — it runs inside `Load`, usually on behalf of an in-flight request the ensure-loaded transport is waiting on, which holds the supervisor's single-instance gate and the `saveMu` a rebuild needs. But unlike a port change (the transport redirects every request to the live port, so no rebuild is ever needed there), the previous context is NOT "still valid": it is the install-time `DefaultFitMinContext` estimate, which under-represents the server and makes the router's pre-call guard refuse every prompt above roughly (window − reserve) × 95% until an unrelated rebuild. A successful persist therefore SCHEDULES `rebuildAfterEmbeddedConfigChange` through `scheduleEmbeddedRouterRefresh` — a single-flight async loop that runs once the persist has released its locks and reruns once more when a second change lands while it is open. The refresh also PUSHES the fresh metadata into every live per-session model registry (`OrchestratorBuilder.UpdateModelOverrides`, the model-registry counterpart of the security-policy push, wired through the `sessionModelRegistries` set and the shared cleanup hook), because `RebuildRouter` alone only fixes sessions built afterwards; the two writers of the override map — registry construction and the push — derive it through the one `modelOverridesFromConfig` helper so they cannot drift. The request that triggered the load is the one request that still sees the stale window: its context validation ran before the transport dispatched; everything after is corrected without a restart. It also mirrors the corrected value into the cached install record, which is what `GetEmbeddedLLMStatus` and the `embedded_llm:state` payload answer from. The persist ALSO PUSHES the corrected window into the live sessions' emitters (`pushDisplayContextWindow` → `session.Manager.SetDisplayContextWindowForModel`): an emitter caches its display basis — the context window the status bar's "N of M" and the "Compacted from X% to Y%" cards are presented against — at `HandleMessage` start, and the only mid-flight corrector besides this push is the lazy local-server probe, which never fires for the embedded provider (its `/v1/models` carries none of the three fields the probe parses). Without the push, an idle session — or the tail of a task that started before the correction — keeps displaying against the stale window: observed as the status bar showing the model's old, larger window and compaction cards whose percentages are scaled by the stale-to-real ratio. The emitter method is model-scoped (a session that has since switched models drops the correction) and re-broadcasts a corrected `context_fill` for an idle session, so the status bar heals immediately; a running task's executor budget is baked per task and self-heals on the next one.
- Startup flags always come from the resolution: the context size is a RAM tier or a planner-computed value, never unspecified, and the offload is either an explicit `-ngl` (with `-fit off`) or no `-ngl` element at all (with `-fit on`) — never the runtime's own `auto` default, which is decided by the model file rather than by any memory measurement.
- A missing vision projector degrades to text-only serving (a warning, and no `--mmproj`) instead of refusing to load; a missing model file does refuse, wrapped in `ErrNotInstalled` with a reinstall hint.
- Reasoning effort is not set by the subsystem: no reasoning flag exists in `LaunchSpec.Args`, and the effort arrives through the ordinary mechanism (`HandleOptions.ReasoningEffort` / the picker / a Model Profile's `sampling.reasoning_effort`). That mechanism only works for this model because of three things outside the subsystem — the sp4rk catalog entry for the checkpoint (`Family "qwen"` + authoritative capabilities), the registry's Family inheritance for a partial override, and the `chat_template_kwargs` reasoning wire this entry alone carries. See [Reasoning effort and the family resolution](#reasoning-effort-and-the-family-resolution) and [ADR-066](../decisions/066-embedded-llm-runtime.md) D7.

**Memory-plan tuning (`embedded_llm.tuning`):**

- Every knob is a pointer, or a struct of pointers, so *unset* is a distinct value from an explicit `auto` / `0` / `false`. That distinction is load-bearing, not cosmetic: an unset `fit` lets the exclusivity rule decide while `fit: false` forces `-fit off`, and an unset `cache_ram_mib` keeps the runtime's default while `cache_ram_mib: 0` DISABLES the prompt cache.
- `ApplyDefaults` seeds **nothing** here. The all-nil zero `TuningConfig` IS the documented all-Auto default, and materializing the pointers would collapse *unset* into *explicit auto* on the first save (`TestEmbeddedLLMTuningDefaultsAreNotSeeded`, `TestEmbeddedLLMTuningZeroValueIsAllUnset`).
- An unauthored section writes **nothing**: `yaml:"tuning,omitempty"` plus yaml.v3's deep-zero struct elision mean a config.yaml that never authored a tuning knob does not grow a block of nulls (`TestEmbeddedLLMTuningUnsetIsDistinguishableFromExplicitAuto`).
- Validation and translation are ONE function. `validateEmbeddedLLMTuning` delegates to `TuningConfig.ToTuning`, so "validate() accepts it" and "the planner can honour it" are the same statement by construction and there is no second allow-list to drift.
- The closed sets are read from core, never transcribed: `kv_cache_type` from `embeddedllm.KVTypes()` (through `ParseKVType`, so `q5_0` stays excluded on its measured ~8x long-context slowdown) and `packing` from `embeddedllm.SupportedPackings()`. The context ceiling is `PinnedMemoryProfile().MaxContext`, resolved once through `sync.OnceValues` and failing closed — a profile nobody could read is a ceiling nobody could enforce, so it is an error rather than a permissive default.
- Rejection is fail-closed and never a coercion: an out-of-set spelling or an out-of-range number is an error naming the key and stating the fix, not a silent fall-back to Auto. Silently planning a different memory plan than the one the operator wrote is the exact failure this surface exists to avoid.
- Every knob is validated even while it is **inert** — while the model is not installed, while fit is off, while another knob makes it moot — for the reason `auto_unload.minutes` already is: arming a value must never activate a dead one.
- `ToTuning` clones every pointer it copies, so a translated `Tuning` never aliases the live config and no caller can reach back into it (`TestEmbeddedLLMTuningToTuningClonesPointers`).
- Install and Remove both rewrite `embedded_llm.*` wholesale and must carry `tuning` through **verbatim** — the same rule `auto_unload` already follows. Install establishes no tuning defaults, because `Tuning`'s zero value already IS the all-Auto plan.
- A tuning edit never rewrites the app-written record: `installed`, `packing`, `backend`, `port`, `model_file`, `runtime_version` and `installed_at` survive a load → edit-tuning → save → load cycle byte-identical, and the generated provider record keeps following the untouched port (`TestEmbeddedLLMTuningEditDoesNotRewriteInstallState`).
- The mapping direction is one-way: `backend/config` imports `core/embeddedllm` (backend sits above core), and core never imports back — which is why the translation lives on the backend side rather than in the planner. `InstallState` carries no tuning for the same reason `ConfigSink` exists at all (`TestEmbeddedLLMTuningKeepsTheImportDirection`).
- `-dev` and `-sm` are **not** reachable from config.yaml. Both pin the offload, which forces fit off, and a wrong device name yields an unlaunchable server rather than a slower one.
- `tuning:` reaches the UI through its own RPC pair, not through the config view: `GetEmbeddedLLMTuning`/`SetEmbeddedLLMTuning` in `backend/frontend_api_embedded_tuning.go`. It appears in neither `ConfigResponse` nor `EmbeddedLLMStatus` — the status carries the resolved **outcome** (`plan`), never the override. The setter is a PARTIAL patch (nil keeps the stored value) with a `reset` list for the third state, because the pointers exist to keep *unset* and *explicit auto* distinct and a whole-section write would collapse them. A tuning write never restarts a resident model — every knob is a launch flag, so it takes effect on the next load and `reload_required` says so.

**Idle budget:**

- `lastActivity` is set when a request completes and re-set immediately after a load completes, so weight-load time never consumes the idle budget. The load-completion stamp is what ARMS the timer: nothing arms it earlier, which is what makes a ten-minute load arrive with the whole budget intact.
- An idle expiry never terminates a server that is mid-generation. In-flight requests are counted, and an expiry that finds one open DEFERS the unload by a bounded 30 s grace (`idleGracePeriod`) instead of stopping the process out from under its own answer. The grace is a re-check interval rather than a second budget: it re-arms on every expiry for as long as requests stay open (so a long generation keeps deferring it while the model is genuinely in use) and it does NOT stamp activity, so the unload lands one grace after the last request completes. ONE deferral is skipped: when activity landed in the window `onIdleExpired` read the in-flight count outside the lock and already armed a fresh FULL budget, `deferIdleUnload` re-reads the budget under `Server.mu` and keeps it rather than trading it for a 30 s grace — a request that completed exactly at the deadline must not have the model unloaded 30 s later and make the next one pay a cold load. The re-checks are bounded by WALL TIME rather than by their number: `idleTimer.deferSince` stamps the FIRST deferral of an episode, the grace re-arms that continue it keep that stamp, and `mark`/`disarm`/`setPolicy` clear it, so `onIdleExpired` defers only while `deferElapsed(now)` is still under one full idle budget. A leaked count — a response body nobody ever closed — therefore costs AT MOST ONE EXTRA FULL IDLE BUDGET, after which the unload proceeds anyway and names the leaked count at Warn, rather than pinning gigabytes resident forever.
- The idle path never takes the FORCE stop path, and `Unload`/`Stop` always do. An idle unload that cannot acquire the single-instance gate is a load in progress, and killing it would trade a deferrable unload for a failed cold start; a caller whose budget expired while the gate was held gets the live child terminated without the gate, because the alternative is an orphan the app can never identify again.
- `auto_unload.minutes` (default 60) without activity stops the process through the same path as an explicit Unload; `auto_unload.enabled = false` leaves the timer unarmed entirely and the model stays resident until an explicit Unload, a Remove or shutdown.
- The timer only ever runs against a `loaded` model: every other transition disarms it, and a stamp made while the model is not resident records the time but arms nothing — so the next load starts a budget of its own.
- The stamp is taken when a response COMPLETES, not when a request is sent, so a generation longer than the remaining budget cannot unload the model mid-answer.
- A policy change takes effect immediately and re-arms against the EXISTING stamp: shortening the budget grants no fresh one, and neither does ENABLING it. A budget already spent leaves a real timer armed at zero delay — `armLocked` always calls `time.AfterFunc` and never fires the callback inline, because the expiry re-validates against `armed` and a spent-budget fire that left no timer behind would be rejected by its own guard — so enabling auto-unload on a model that has been idle longer than the new budget, or shortening the budget below the time already idle, ACTUALLY unloads it. Disabling disarms the timer without discarding the stamp.
- The live policy is owned by the idle tracker, not by the `Server.AutoUnload` field, which is a construction-time input seeded on first use — so a runtime `SetAutoUnload` and a concurrent `IdleRemaining` read cannot race.
- A non-positive budget is clamped to the default rather than meaning "unload immediately", so re-enabling the timer can never activate a dead budget.
- The expiry callback re-checks the remaining budget under the lock, and so does the deferral it hands off to, so an activity stamp that raced the timer wins and neither the stale callback nor a stale deferral acts on it.

**Request path (ensure-loaded transport):**

- The embedded provider entry is the ONLY dial path the transport is installed on. It is attached through `llm.ProviderEntry.HTTPClient` in `core/builder.go` `providerEntryFromConfig`, guarded by `BuilderConfig.EmbeddedLLM.ProviderName`; the Fetch Models listing and the lazy context-window probe are not wrapped, and neither is any other provider.
- The same guard is the ONLY selector of the reasoning wire: that entry carries `llm.ReasoningWireChatTemplateKwargs` and every other one keeps the vendor-default top-level spelling. The wire follows the supervisor, never the name or the URL shape — a user provider pointed at the same loopback is not opted in, because c0wrk does not know which binary answers there — and no loader wired means no wire selected either. See [Reasoning effort and the family resolution](#reasoning-effort-and-the-family-resolution).
- The pin resolver runs FIRST and the transport only decorates its answer, because `llmtls` needs a concrete `*http.Transport` to hold a `tls.Config`: wrapping first would make the pin resolver discard the wrapper and replace it with a default-transport clone. The generated embedded record carries no pin (plain HTTP on loopback), so in practice the transport wraps the shared LLM client — but the order holds if that ever changes.
- The client handed to the entry is always a CLONE of the shared LLM client (or of the pin resolver's clone of it), so `timeouts.llmRequestTimeout` survives. Attaching a client without it would shadow `RouterConfig.HTTPClient` and cap inference at the 30 s web-fetch proxy budget — the invariant [llm-providers.md](llm-providers.md) states for the pin path, and the reason `EnsureLoadedClient` takes the shared client at all.
- That budget is MOVED, not dropped. `http.Client.Timeout` covers the whole exchange, gate included, so a client that both waits for a cold load and carries the request budget would hand the generation whatever is left after the weights land — and with the default 600 s budget against a 15 min ready allowance a legitimately slow load would consume it entirely. `EnsureLoadedClient` therefore zeroes the clone's `Timeout` and passes it to the transport, which arms it on the request only AFTER the model is resident. The deadline is released when the response body is closed (never when `RoundTrip` returns), because a streamed generation is read long after the headers arrive.
- Concurrent requests coalesce into ONE load: the transport keeps a single in-flight call and every waiter joins it, so a burst of cold requests is one weight load rather than N queue entries on the supervisor's gate.
- The wait is bounded, and the bound is longer than the supervisor's own ready budget, so a wedged load is always diagnosed by `Load` (with the server's log tail) rather than reported as a transport timeout. An expired transport budget is `ErrLoadWaitTimeout` — and it is the WAIT that expired, not the load: the budget is armed per waiter, so its expiry leaves the in-flight load running detached under the supervisor's own `ReadyTimeout`. `ErrLoadWaitTimeout` therefore means exactly one of "the load was still legitimately in progress" or "the ready budget is misconfigured above the wait budget", and the honest recovery is to retry rather than to report the model broken.
- The load is NOT bound to a request context, NOR to the wait budget. `timeouts.llmRequestTimeout` (default 600 s) is shorter than `DefaultReadyTimeout` (15 min), so a caller-side deadline during a cold load would otherwise cancel it and discard the half-loaded weights — and the next request would start from zero, so the model would never become resident. A cancelled request stops waiting (with its own context error) while the detached load runs to completion for whoever asks next. This is what makes ADR-067 D13's "the load is never charged to the request's own timeout" a structural property rather than a consequence of the default tuning: an expiry of EITHER budget stops a waiter without touching the load.
- A load failure reaches the caller as itself and is not cached: the next request loads again. The request is never sent to a server that is not listening.
- Activity is stamped when the response COMPLETES, not when its headers arrive, and at most once per response — whichever of a full read or a close happens first. Stamping at header time would let the idle timer fire during a generation longer than the remaining budget.
- The wrapper forwards `CloseIdleConnections` to the transport it wraps, so installing it does not strand the provider's connection pool (`http.Client` only recognises the method on the RoundTripper it was handed).
- No loader wired means no transport: `EnsureLoadedClient` returns the pin resolver's answer unchanged, `nil` included, so the entry keeps falling back to the router-level client exactly as before.
- **Every** router carries it, cached and per-session alike. A per-session orchestrator is built from a `BuilderConfig` converted deep inside the session factory, which has no path to the supervisor, so `OrchestratorBuilder` also holds a builder-level default seam (`SetEmbeddedLLM`) that `buildRouter` falls back to when the config carries no `Loader`. An explicit per-config `Loader` wins; a half-populated one (a name with no supervisor — what `applyEmbeddedLoader` leaves when nothing is installed) does not shadow the default.

**Pre-dispatch gate (short-budget callers):**

- The transport gates on the wire, inside `http.Client.Do`, which is too late for a caller that has ALREADY armed a short deadline: the one-shot service requests create a `timeouts.serviceLLMRequestTimeout` context (default 120 s) first and issue the request second, so a cold load measured in minutes would be charged to it and the call would fail instead of waiting. Those paths call `FrontendAPI.ensureEmbeddedReadyForLLMRequest` BEFORE the context exists — `OptimizePrompt`, `GenerateCommitMessage` and the session manager's title generation (through `Manager.SetServiceLLMGate`, wired by `installServiceLLMGate`).
- The gate is keyed on `activeModelIsEmbedded`: the persisted install state AND the default model resolving to the backend-owned provider. Service calls run on the cached router, whose active model is `llm.default_model` (`buildRouter` applies it via `SetModel`), so resolving the default answers the same question the router will. For any other provider the gate returns at once — one config read.
- The load runs under the app context, bounded by the supervisor's own `ReadyTimeout` — not by the caller's deadline and not by `DefaultLoadWaitTimeout`, which bounds only how long the gate WAITS: an expired service budget must not abort a load that is legitimately in progress (the transport detaches for the same reason), and concurrent callers coalesce on the supervisor's single-instance gate.
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
- Shutdown stops a running server, under the same bounded stop as every other stop-shaped call (30 s for the gate, then the stop's own derived budget), and logs a failure as non-fatal. The guarantee a quit makes is TRACKING: a stop that does not take re-attaches the run handle and records `error` instead of dropping it, so the child is never left live and unaccounted for — and that one outcome is also the only way a quit can still leave `llama-server` running.

**Settings UI:**

- The block renders exactly one action surface per state: Install while not installed, one progress row per reporting component while installing, and the install record + Load/Unload + Remove + auto-unload once installed. Remove is reachable only through its confirmation dialog, so the first click never issues the RPC.
- The block reads its state from `embeddedLLMStore`, whose snapshot has one writer (`setStatus` from `GetEmbeddedLLMStatus`); an `embedded_llm:state` event re-reads that snapshot instead of patching it, so no half-merged state is ever rendered.
- Mounting the block subscribes to the two events and reads the status once. It installs nothing, loads nothing and probes nothing.
- The install record shows the EFFECTIVE backend and the resolved packing from the status snapshot — never a locally remembered or probed value.
- A synchronous install/remove/load/unload refusal is rendered inline by the block; only a BACKGROUND install failure additionally reaches the `runtime_error` toast.
- Every size in the block is scale-agnostic: no viewport unit, no `*-screen` utility and no pointer-anchored placement (see [frontend/ui-scale.md](frontend/ui-scale.md)); its colors are design tokens only.

**Status-bar UI:**

- The indicator renders nothing — its own leading separator included — unless it has a state to report, so a hidden block never leaves a stray separator in the bar (the same contract `ProcessMemoryStatus` holds). "Nothing to report" includes installed-but-stopped: an idle install is not status-bar news. `status.available` is deliberately NOT part of that contract: the surface follows the install/supervision fields only, so an erroring snapshot still paints and an unconstructable subsystem cannot hide the error it reports.
- Exactly one surface is rendered at a time, in a fixed precedence: a live install run outranks the snapshot (the backend refuses a load while one is in flight, and the progress event is itself proof of the run), then `loading`, then `loaded`, then an error.
- Install progress in the bar is the ACTIVE artifact's own fraction — the first reported component that has not reached `done` — never an aggregate across artifacts (a ~100 MB runtime against 6.7 GiB of weights would make a summed percent jump backwards) and never an invented percentage for a byte-less stage or for the window before the first progress event.
- The component and stage words are the shared `frontend/src/lib/embeddedLLMLabels.ts` vocabulary, so the status bar and the Settings rows describing the same `embedded_llm:install_progress` payload cannot name it differently; an artifact or stage this frontend does not know yet degrades to its raw id, never to a blank.
- Residency is reported from the snapshot's `loaded` flag alone: it survives any number of refreshes including one whose `idle_remaining_seconds` is 0, and it disappears on the unload transition whoever triggered it (a manual Unload or the idle timer). No remaining-idle countdown is rendered, because the snapshot refreshes only on transitions and a ticking readout derived from it would be wrong within a second.
- The block is strictly read-only: it issues no RPC other than the status read, exposes no action, and never installs, loads, unloads, removes or probes. The Settings block stays the only surface that mutates.
- It is not gated on project mode (the local model is process-wide, like RSS), and every size is scale-agnostic: fixed layout-px widths plus percentages for the bar, no viewport unit, no `*-screen` utility, no pointer-anchored placement; its colors are design tokens only.

## Configuration

Authoritative reference: `config.example.yaml` (section `embedded_llm:`). Most of the section is written by the app — it is state, not tuning, and hand-editing it does not install or remove anything. **Two sub-sections are the exception and are operator settings**: `auto_unload:` (the idle budget) and `tuning:` (the memory-plan overrides). Install and Remove carry both through verbatim, so provisioning or uninstalling the model never resets either. Everything else — `installed`, `packing`, `backend`, `port`, `model_file`, `runtime_version`, `installed_at` — is owned by the install flow alone, and a tuning edit never rewrites it (pinned by `TestEmbeddedLLMTuningEditDoesNotRewriteInstallState`).

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

The memory-plan override surface. Every key is **optional**, defaults to *absent*, and maps one-to-one onto a field of `embeddedllm.Tuning` (see [Memory plan](#memory-plan)). Absent is not spelled `auto`: both build the same plan, but only an absent key means "the operator chose nothing". Spellings are matched case-insensitively and trimmed; the two closed sets (`kv_cache_type`, `packing`) are READ from core (`embeddedllm.KVTypes()` / `SupportedPackings()`) rather than transcribed, so they cannot drift.

| Key | Type | Default (unset means) | Valid values |
| --- | --- | --- | --- |
| `embedded_llm.tuning.context.mode` | string | planner sizes `-c`: from `fit_min_context` under fit, otherwise from the RAM ladder, capped at the training context | `auto` \| `exact` (which makes `tokens` **required**) |
| `embedded_llm.tuning.context.tokens` | int | — | `1`–`262144` (the pinned model's training context, read from `PinnedMemoryProfile().MaxContext`). Validated even while `mode` is `auto` |
| `embedded_llm.tuning.kv_cache_type` | string | adaptive `-ctk`/`-ctv`: f16 → q8_0 → q4_0 until the target context fits | `auto` \| `f16` \| `q8_0` \| `q4_0`. Anything else is refused, never coerced |
| `embedded_llm.tuning.offload.mode` | string | nothing pinned — under fit the RUNTIME sizes the layer count | `auto` \| `all` (`-ngl 99`) \| `cpu` (`-ngl 0`) \| `layers` (which makes `layers` **required**). Any explicit mode turns fit **off** |
| `embedded_llm.tuning.offload.layers` | int | — | ≥ `0`. Validated even while `mode` is not `layers` |
| `embedded_llm.tuning.fit` | bool | the exclusivity rule decides | `true` \| `false`. An explicit `true` next to an explicit offload LOSES to the rule and is recorded in the plan's notes |
| `embedded_llm.tuning.fit_target_mib` | int | the runtime's own 1024 MiB `-fitt` | ≥ `0`; an explicit `0` also omits the flag. Only emitted under fit |
| `embedded_llm.tuning.fit_min_context` | int | `65536` (`DefaultFitMinContext`, NOT the runtime's 4096) | `1`–`262144`. Only emitted under fit |
| `embedded_llm.tuning.kv_offload` | bool | the KV cache stays on the device with the layers | `true` \| `false` (`false` passes `-nkvo`) |
| `embedded_llm.tuning.mmproj_offload` | bool | the projector reserve stays on the device | `true` \| `false` (`false` passes `--no-mmproj-offload`) |
| `embedded_llm.tuning.packing` | string | the packing the hardware probe resolved, which `embedded_llm.packing` RECORDS | `auto` \| `PQ2_0` \| `PTQ1_0`. Overrides what the PLAN assumes, not what is on disk — change it and reinstall. Recorded as `PackingReasonOperatorOverride` |
| `embedded_llm.tuning.parallel` | int | `1` (`DefaultParallel`) | ≥ `1` |
| `embedded_llm.tuning.cache_ram_mib` | int | the runtime's own `-cram` | ≥ `0`; an explicit `0` DISABLES the prompt cache and is passed through verbatim, not read as unset |
| `embedded_llm.tuning.host_reserve_gib` | float | the topology's own derivation (the larger of a 4 GiB floor and 1/8 of RAM) | ≥ `0`. A **planner-side** budget knob, not a runtime flag (the fork has no `--host-reserve`), and it REPLACES the derived reserve |

Two `embeddedllm.Tuning` fields are deliberately **not** exposed: `Devices` (`-dev`) and `SplitMode` (`-sm`). Both pin the offload — which forces fit off — and naming a device this machine does not have yields an unlaunchable server rather than a slower one. They stay reachable only through the planner's own vocabulary, and `TestEmbeddedLLMTuningToTuning` asserts a translated plan leaves both at their zero sentinels.

Derived config the backend writes (never authored by hand), reconciled by `Config.SyncEmbeddedLLMProvider` → `LLMConfig.SyncEmbeddedProvider` at two points — the config load path (`LoadWithResult`, after `ApplyDefaults` and before `validate`) and `UpdateLLMConfig` (after the candidate is built, before it is validated):

- `llm.openai_compatible.embedded` — `base_url: http://127.0.0.1:<port>/v1` (always derived from the persisted port, never stored independently), `api_key: ""`, `models: ["Bonsai 2 27B"]`, no `tls_fingerprint` (plain HTTP on loopback, so the ADR-054 pin does not apply); present exactly while `installed` is true. `output_token_reserve` is the one operator field preserved across regeneration.
- `llm.models["Bonsai 2 27B"].context_window` — the resolved RAM tier, so token budgets are correct while the server is stopped. Written only by a caller that knows the tier (`contextWindow > 0`); a `0` argument leaves an existing override untouched, which is what both sync points pass because neither performs a hardware probe or any network I/O. Other override fields (family, tokenizer, output limit) are preserved, and an operator-authored `family:` stays authoritative across every sync. The sync must never WRITE a family: an absent one is exactly what lets the sp4rk catalog's `qwen` — which the reasoning-effort picker and the request encoding both key off — reach the model through partial-override inheritance. See [Reasoning effort and the family resolution](#reasoning-effort-and-the-family-resolution).

The sync copies the provider and override maps before mutating them: `UpdateLLMConfig` builds its candidate as a struct copy that shares map headers with the live config, so a candidate that is later rejected must not leak the injection — or the removal — into the observable state.

The override is backend-written but operator-owned afterwards: the Configure dialog (`SetModelConfig`) rewrites the whole `llm.models` entry, and saving it with every field at the built-in default DROPS the entry — including the tier. That is the documented precedence (an explicit user choice wins), and it self-heals: with no config override the lazy probe (tier 1.5) rediscovers the window from `/v1/models` the first time the server runs, and the next install/load sync rewrites the tier. A runtime entry written that way also pre-fills `Family` from the model name, which outranks the catalog — see the ordering hazard in [Reasoning effort and the family resolution](#reasoning-effort-and-the-family-resolution).

`validate()` rejects an out-of-range port, an install without an allocated port, an `auto_unload.minutes` outside **1..`embeddedllm.MaxAutoUnloadMinutes`** (bounded on BOTH sides — the ceiling is the minutes→nanoseconds overflow guard, and it is checked even while the timer is disabled), a `llm.models.<name>.context_window` override outside **0..`config.MaxModelContextWindow`**, and every illegal `tuning.*` value, each with an actionable message naming the key and the fix. The tuning rules live in `validateEmbeddedLLMTuning`, which is a one-line delegation to `TuningConfig.ToTuning` — there is deliberately no second list of legal values to drift, so a config that loads is by construction a config the planner can honour. The numeric knobs are bounded by `core/embeddedllm/limits.go`'s exported ceilings as well as their floors, and `host_reserve_gib` is additionally required to be FINITE: NaN compares false against every bound, so a floor-only check let a typo through for the planner to silently ignore, and a float→int conversion the result type cannot represent is implementation-defined in Go — it saturates on arm64 and yields the negative "indefinite value" on amd64, which would make `host = ram − reserve` enormous and fail the memory gate OPEN on the one knob meant to shrink the budget. They run **unconditionally**, including while the model is not installed and while the knob in question is inert (`fit_min_context` beside `fit: false`, `offload.layers` beside `offload.mode: all`, `context.tokens` beside `mode: auto`), for the reason `auto_unload.minutes` already is validated while the timer is off: arming a value must never activate a dead one (`TestEmbeddedLLMTuningValidatedWhileInert`). Because the record disappears with `installed: false`, the Remove flow must also migrate `llm.default_model` off `embedded/Bonsai 2 27B` — otherwise the next load fails validation and the next settings save is rejected as a dangling default. Migration means moving it to the first *other* enabled model id, not clearing it: `validate()` also rejects an empty `llm.default_model`, so an empty result is correct only when the embedded model was the only one enabled — and it is DELIBERATELY left invalid then, exactly as for the no-provider rule. `ApplyDefaults` does **not** seed `DefaultModel`, so nothing self-heals on the next load; the user must pick or add a provider (`TestEmbeddedLLMRemoveWithNoOtherModelLeavesAnEmptyDefault` pins the post-Remove config as failing validation). `Remove` preserves the `auto_unload` and `tuning` sub-sections while resetting every other `embedded_llm.*` field — an idle budget and a memory plan are operator settings, not install state, so a reinstall starts from the plan the operator chose rather than from a reset one. `Install` follows the same rule and additionally fills the auto-unload defaults into unset knobs only; it establishes **no** tuning defaults, because `Tuning`'s zero value already IS the all-Auto plan and shipping defaults for the sink to apply would collapse the unset/explicit distinction on the first provision. Fixed operational constants (not configurable, by design — tunability would undermine the pins and the hardware safety margins):

| Parameter | Value |
| --- | --- |
| Minimum memory | **no fixed threshold** — the gate refuses when no modelled shape fits *both* pools (`ErrInsufficientMemory`). On a UNIFIED machine the floor is *derived*: it lands at **12.03 GiB** of RAM (`TestMemoryGateUnifiedFloorIsDerived` pins it), which is where the 8230 MiB host-resident PTQ1_0 shape at the 65536-token floor clears `RAM − max(4 GiB, RAM/8)`. Beside an accelerator with its own memory the RAM total is not the binding constraint at all — 8 GiB of RAM with a 32 GiB card is admitted |
| Runtime pin | `prism-b10735-842b188` |
| Sampling flags | `--temp 1.0 --top-p 0.95 --top-k 20 --jinja -fa on` |
| Progress throttle | ~100 ms |
| Disk headroom on top of the artifact set | 2 GiB (`DefaultDiskHeadroom`) |
| Max decompressed size per archive entry | 2 GiB (`maxExtractEntryBytes`) |
| Max decompressed size per archive | 8 GiB (`maxExtractTotalBytes`) |
| macOS `xattr`/`codesign` command budget | 2 min per command |
| macOS `--version` smoke-test budget | 60 s |
| Output one provisioning command may contribute | 1 MiB (`maxCommandOutputBytes`), with `cmd.WaitDelay` set so killing the child also ends the pipe read |
| Idle-unload deferral while a request is in flight | 30 s per re-check (`idleGracePeriod`), re-armed on every expiry — but SKIPPED when activity landed in the expiry's unlocked window and already armed a fresh full budget, which `deferIdleUnload` re-reads under `Server.mu` and keeps — with each deferral EPISODE bounded to one extra full idle budget of wall time (`idleTimer.deferSince`) |
| Backend budget for every stop-shaped call — FOUR of them: the shutdown stop, `UnloadEmbeddedLLM`, `RemoveEmbeddedLLM`'s stop, and an install's step-0 stop | 30 s for the GATE WAIT (`embeddedStopTimeout`, overridable per call by `stopBudget`). The first three wrap a context with it; the fourth hands the same figure to core as the exported `Installer.StopTimeout` field, because the install's own context must stay deadline-free for the download. The stop then derives its own budget under `context.WithoutCancel` on either path — `stopTimeout + killWait` = 15 s once the gate is held, 16 s (`+ 1s`) on core's no-gate force path — so the STOP's ceiling is ≤ 46 s and it returns success with the child terminated, UNLESS the stop did not take: a child that survives both the signal and the kill is reported as an error with its run handle re-attached and `StateError` recorded, never dropped. `RemoveEmbeddedLLM`'s tree deletions and config save follow that stop and are unbudgeted |
| Auto-unload defaults an install establishes | `enabled: true`, `minutes: 60` (unset knobs only) |
| Memory-tuning defaults an install establishes | **none** — `Tuning`'s zero value is the all-Auto plan, and seeding it would collapse *unset* into *explicit auto* |
| Context ceiling for an explicit override | `262144` — `PinnedMemoryProfile().MaxContext`, read not transcribed (`EmbeddedLLMMaxContextTokens`) |
| Numeric ceilings shared with `backend/config` (`core/embeddedllm/limits.go`) | `MaxAutoUnloadMinutes` 525600, `MaxTuningMiB` 1<<31, `MaxTuningLayers` 1<<20, `MaxTuningParallel` 64, `MaxTuningHostReserveGiB` 1<<20 — overflow/absurdity guards, not tuning opinions |
| Ceiling for a `llm.models.<name>.context_window` override | `16777216` (`config.MaxModelContextWindow` = 1<<24), bounding BOTH writers of that tier-1 key |
| Ready budget for one Load (spawn → first `/v1/models` answer) | 15 min (`DefaultReadyTimeout`) |
| Request-path budget for WAITING on a cold load | 17 min (`DefaultLoadWaitTimeout` = `DefaultReadyTimeout` + 2 min) — bounds the wait only, never the load, which stays detached under the ready budget |
| Readiness poll interval | 500 ms (`DefaultReadyPollInterval`) |
| Budget for a single readiness probe | 10 s (`DefaultProbeTimeout`) |
| Graceful stop window before the kill | 10 s (`DefaultStopTimeout`) |
| Bounded wait after the kill | 5 s |
| Server output retained for failure messages | last 24 lines |
| Level the server's own stdout/stderr is logged at | debug |
| Bind address | `127.0.0.1` (`LoopbackHost`, enforced by `Validate`) |
| Web UI | disabled unconditionally (`--no-ui`) |

The four supervision budgets and the stop timeout are per-`Server` fields rather than config keys: they are recovery timeouts with no operator meaning. The two knobs a user actually tunes are `embedded_llm.auto_unload` (the idle budget) and `embedded_llm.tuning` (the memory plan), and both are reachable from the UI: the former through `SetEmbeddedLLMAutoUnload`, the latter through the `GetEmbeddedLLMTuning`/`SetEmbeddedLLMTuning` pair (see [desktop-frontend.md](../contracts/desktop-frontend.md#embedded-llm-backendfrontend_api_embeddedgo)).

The **Runtime pin** row is the current value of `embeddedllm.RuntimeTag`. ADR-067 D11 still reads `prism-b10709-9a9394a`, because accepted ADRs are immutable — see the **Pin bump 2026-09-25** note under **Invariants** for the CVE review that advanced it to `prism-b10735-842b188`. Advancing the pin is a code change in `registry.go` plus this table, never a `config.yaml` edit: `embedded_llm.runtime_version` only *records* which pin an install came from, and a pin change forces a runtime re-download while keeping the weights.

## Extension Points

**Add a packing or a second model.** Append the asset (component, URL, SHA256 from the HF LFS OID, exact size) to `core/embeddedllm/registry.go`, extend `Packing`, and teach `resolve.go` when to select it. Resolution stays a pure function so the whole matrix remains table-testable; the manifest records what was actually installed, so mixed installs are distinguishable.

**Add a backend.** Extend `Backend`, add the probe step to `hardware.go` in the fixed precedence order, add the per-platform runtime assets (with checksums from the GitHub REST `digest` field) to the registry, and extend `resolve.go` for `-ngl`/image-token policy. Backends that need a companion archive follow the Windows `cudart` shape: a second `Asset` with its own component, progress and checksum.

**Raise the runtime pin (security/maintenance bump).** Update the release tag, every affected runtime URL and checksum, and run a CVE review of the old→new range before shipping — the same discipline the tool-manager requires. Existing installs reconcile against the manifest's `runtime_version`; a pin change forces a runtime re-download while weights are kept.

**Change port policy.** Three seams own the port and nothing else chooses one: `Installer.AllocatePort` (`func(ctx) (int, error)`, called once during install, defaulting to the unexported `ephemeralLoopbackPort`) picks the initial one; `Server.EnsurePort` (`func(ctx, port) (int, error)`, called immediately before every spawn) may substitute and persist a replacement — production wires it to `backend.embeddedEnsurePort`, which scans with `embeddedllm.SearchFreePort` and writes a move back to config; and `PortProber` (the third argument of `SearchFreePort`, defaulting to `loopbackPortFree`) decides what "free" means, injected in tests through `embeddedLLMState.portProbeFn` and in core by passing the prober directly. Leaving `EnsurePort` nil trusts the persisted port, which is correct only for a caller with no config to keep in sync. An explicit `InstallOptions.Port` bypasses allocation entirely. The provider base URL is always derived from the persisted value and the request path always aims at the live one, so a different allocation strategy is a change to these seams and nothing else.

**Wire or change the supervision.** `Server` owns the whole lifecycle and every external effect is an injectable field: `Spawn` (`SpawnFunc`) replaces the real `exec` with a double, `EnsurePort` the port policy, `OnState` the event transport (the backend forwards `StateEvent` as the global `embedded_llm:state` payload), `HTTPClient` the readiness client, and `Now`/`HostOS`/`Platform` the clock and the platform keys. Three more carry the memory-aware load, and production wires all three (`embeddedBuild`): `ProbeDevices` (the OPTIONAL, fail-soft, `LoadProbeTimeout`-bounded re-measurement a launch refines its plan with — nil means no probe at all, which is the pre-existing contract), `Tuning` (a **function** resolving `embedded_llm.tuning` at launch time, because the supervisor is cached while a settings save is not; nil means the all-Auto zero), and `PersistContext` (where the `/props` readback writes the tier-1 `context_window` override; nil means the manifest is still corrected but config.yaml keeps the install's estimate). A different residency or freshness policy — never probe, probe on a schedule, refuse a load whose machine changed — is a change to these three and nothing else. `Stop` has exactly the `Installer.Stop` signature, so install, removal and shutdown all wire it with no adapter (`installer.Stop = server.Stop` in `embeddedBuild`) — THREE wirers, since `Install` now stops a resident server as its step 0 before `Remove` and the shutdown path do. They do not all BOUND it the same way: removal and shutdown wrap a context with `embeddedStopBudget()`, while the install's step 0 hands the same figure over as `Installer.StopTimeout` (re-resolved per run in `runEmbeddedInstall`), because the install's own context must stay deadline-free for the download. The command line exists in one place — `LaunchSpec.Args` — and its inputs come from the manifest, from `Manifest.Plan` through `ApplyMemoryPlan`, or (for a pre-plan manifest) from the pure policy in `resolve.go`, so a new flag is a `LaunchSpec` field, an `Args` entry and a `Validate` rule — plus a `MemoryPlan` field if the planner decides it, which `TestApplyMemoryPlanRendersEveryFlagBearingField` then ratchets. `Process` is deliberately an interface with `Stdout`/`Stderr` readers so the production output pumping and log tail are exercised by tests that never start a real server. The request path is a seam of the same shape: `Loader` (`Load` + `MarkActivity`) is all `EnsureLoadedTransport` REQUIRES, so a different residency policy — pre-warming on project open, keeping the model pinned while a session is live — replaces the loader or wraps it, and the transport, its coalescing and its activity stamping stay put. A replacement loader should also carry the two OPTIONAL capabilities production's `backend.embeddedLoaderRef` does (`PortSource`, `RequestTracker`): they are discovered by type assertion on the `Loader` value, so one that omits them silently loses the live-port redirect and the mid-generation idle deferral while every test double keeps passing.

**Wire or change the install orchestration.** `Installer` owns the whole flow; the config layer is reached only through `ConfigSink` (`ApplyInstalled(InstallState)` / `ApplyRemoved()`), implemented by `backend/frontend_api_embedded.go`. The reference implementation and its executable contract — what an install must produce in `embedded_llm.*`/`llm.*`, which auto-unload knobs survive, and what a removal must leave behind — is `backend/config/embedded_llm_sink_test.go`. A different progress transport only has to adapt `InstallOptions.Progress` (the `embedded_llm:install_progress` payload *is* the `Progress` struct), and every external effect (`Downloader`, `Probe`, `RunCommand`, `AllocatePort`, `Stop`, `Now`, `HostOS`) is an injectable field, so the flow itself is testable without a network, without multi-gigabyte artifacts and without macOS binaries. The one non-function knob on the same struct is the exported `StopTimeout`: it bounds the step-0 `Stop` call alone (`stopBeforeInstall` wraps it in `context.WithTimeout`; `0` inherits the caller's ctx), and a caller that wires `Stop` to a supervisor SHOULD set it — the install's own context stays deadline-free for the download, so this field is the only bound between a repair click and a wait for the length of an in-flight cold load. It is not `Server.StopTimeout`, which is the graceful signal→kill window inside `terminate`.

**Recognize a new accelerator, or act on the topology.** The classifier's vocabulary is two marker lists in `core/embeddedllm/topology.go` (`discreteDeviceMarkers`, `unifiedDeviceMarkers`) matched against a device's name *and* description, plus the ordered rules in `classifyUnified`; a new part is a marker and a row in `TestClassifyUnified`, and an unrecognized part already lands on the safe side (unified). Everything downstream of the parse is a pure function of `(platform, hostRAMBytes, listing)`, so a different budget policy is a change to `buildTopology` and its table alone. A **consumer** — the memory gate, a status surface — reads `DeviceBudgetMiB()` / `HostBudgetMiB()` against `memory.go`'s `ProjectDeviceMiB` / `ProjectHostMiB` plus the projector reserve, and must treat `ok == false` from `ProbeDevices` as `deviceUnreadable` — a host-only gate with the degradation recorded in `Notes` — never as a refusal. A consumer that assembles a `MemoryTopology` by hand rather than receiving one from `ProbeDevices` should expect `normalizeTopology` to derive the budgets it left out; what it must still supply itself is `Unified`, which is never re-classified for it.

**Add or retire a compatibility guard.** The table is `CompatibilityGuards` in `core/embeddedllm/compat.go`: one `GuardID` constant whose doc comment cites the upstream report, one branch in the table body carrying a typed `GuardReason` (severity is derived from it), and a row in `TestCompatibilityGuardsTable` plus a shape assertion in `TestGuardDecisionShape`. Nothing else has to change — the resolver applies a `prefer_backend` decision automatically when the target is pinned, the packing rule reads the same `GPUFamily`, and the record reaches the manifest and the status DTO on its own. A **new accelerator generation** is a `GPUFamily` constant, a rule in `gpuRules` (order matters: specific tokens before broad ones) and rows in `TestClassifyGPU`; if the model card measures a different packing as faster there, that is one line in `prefersPTQ1_0Decode`. Retiring a guard — because upstream fixed the issue — is deleting its branch and its table rows, and it is what a pin bump's re-read of `KNOWN_ISSUES.md` is for. Two inputs are still unprobed and therefore inert in production, each with a documented safe default: `HostCaps.AVX512` (no CPU-feature probe exists; `Unknown` is treated as present, which only matters on a pre-#245 pin) and `MachineProfile.FitsPQ2_0` (the fit gate supplies it; `FitUnknown` never downgrades). A `rocminfo`-derived gfx target would close the `gfx1151` coverage gap documented under [Backend compatibility guards](#backend-compatibility-guards).

**Change the memory gate.** The gate is three pure pieces in `core/embeddedllm/plan.go` and nothing else decides viability: `gateBudgetsFor` (which pools exist and how much of each may be spent), `memoryGate`'s shape space (`gatePackings` / `gateKVTypes` / `gateContext` / the residency extremes), and `gateBudgets.overflow` (how the two footprints are charged — additively on a unified machine, independently on a discrete one). A new memory model is a change to those three plus a row in `TestResolveMemoryGate`. Two contracts a change must respect: the gate stays the FIRST check in `resolveWith`, before `ArtifactSet`, so a refused machine plans no assets and creates no directory (`TestInstallMemoryGateRefusesBeforeAnyDownload` ratchets it); and a new *device-axis* state must be added to `deviceAxisState` rather than folded into `deviceKnown` or `deviceUnreadable`, because the three states have three different consequences and the distinction between "no accelerator" and "an accelerator nobody measured" is the whole reason the gate is not a RAM threshold. The derived unified floor is pinned by `TestMemoryGateUnifiedFloorIsDerived`, which recomputes it from `memory.go`'s own projections — a pin bump that changes the weights moves the floor, and that test tells you the Configuration table's number is stale.

**Add a degradation rung.** `relaxations` in `core/embeddedllm/plan.go` is an ordered list of `(Tuning → Tuning, note)` pairs, least-lossy first, and `Plan` walks it only when the base pass came back infeasible and the operator pinned no offload. A new rung is one entry plus a `plan_test.go` case at a budget where it is the one that wins. Two rules: it must compose with `planKVType`'s precision ladder rather than replacing it (every rung re-runs it), and it must only touch a knob the operator left *unset* — a rung that overrides a pin silently launches a different shape than the one asked for. A rung that needs a measurement `memory.go` does not have (a partial offload is the standing example) is not a rung: an unmeasured split is a guess a capacity gate must not make.

**Change the memory plan, or add a tuning knob.** The whole decision is one pure function — `Plan` in `core/embeddedllm/plan.go` — over values, so a policy change is a change to that function and its table, with nothing to stub and no machine to test on. A new knob is a `Tuning` field (a pointer, or a type with an explicit Auto sentinel, so *unset* stays representable), a branch in `Plan`, a `MemoryPlan` field if the launcher must see it, a note in `shapeNotes`, and a row in `TestPlanNotesEveryNonDefaultDecision`. Two contracts a new field must respect: it may not make `Plan` impure (`TestPlanImportsNoIOPackage` bans the imports that would), and if it pins the offload it **must** be added to `Tuning.explicitOffloadShape()` — otherwise the plan emits `-fit on` beside a pinned `-ngl`, which is the shape the fork's `fit.cpp` aborts on. A new KV precision belongs in `memory.go`'s closed `KVType` set first (with a measurement); the ladder in `planKVType` then picks it up automatically.

**Consume a memory plan at launch.** The rendering half is built: `LaunchSpec` carries a typed field for every flag a `MemoryPlan` names, `ApplyMemoryPlan` is the total bridge between them, and `Validate` enforces the exclusivity rule on the argv side. So mapping a plan onto a launch is one call, and `TestApplyMemoryPlanRendersEveryFlagBearingField` ratchets it — it reflects over `MemoryPlan` and fails if a new field is neither a documented non-flag nor visible in argv, so a planner knob cannot be added and then silently dropped.

The decision to use one is wired too, and it took the persisting branch. `Manifest.Plan` carries the shape an install resolved, `Server.launchSpec` splits a launch into an **identity half** (`launchIdentity`: the binary, the weights, the projector, the socket, `--image-max-tokens` — all describing bytes that are on disk) and a **shape half** (`ApplyMemoryPlan` over the recorded plan), and `resolvedLaunch` carries the applied plan, the topology it was priced against and a `Refreshed` flag back to `Load` so a refinement can be written down. Both identity path halves are **containment-checked against the layout**, so a tampered install record cannot aim the launch at bytes outside it: the binary is derived from the runtime tree and walked through `ServerBinaryPath`, and the recorded `model_file` must lie inside the model root (`Layout.OwnsModel`). A record that fails that check falls back to the **layout-derived** path for the recorded packing rather than refusing — the derivation is what an install writes, so it is also the honest recovery — and the divergence is logged at Warn, because a record that does not describe the layout is a fact an operator should see. Persisting beat re-deriving for the reason the extension point predicted: re-deriving at load would need the operator's `Tuning`, and `tuning` is a setting the sink carries verbatim, so a copy in a record would be a second source of truth that goes stale on the first edit. A **pre-plan manifest** still launches, from the pure policy (explicit `-ngl`, `-fit off`, `-np 1`, the recorded `-c`, and the runtime's own defaults for the KV precision, the cache knobs, `-dev` and `-sm`) — and gains no plan it never had, even when a probe answers.

The context half is `recordableContext`, and it is the rule the extension point asked for: anything that persists a context reads `MemoryPlan.Fit` and treats a fit-sized plan as *"no concrete context to record"* rather than *"context 0"*, recording the `FitMinContext` floor instead. `Validate` accepts the zero on the argv side because there fit sizes it; a record may not carry one, because `0` means "leave the existing override alone" to `SyncEmbeddedLLMProvider` and would read as a lost tier.

A plan that outlives a single launch as a user-visible artifact is wired too: `MemoryPlan.Notes` — the human-readable "why" for every non-default decision — reaches the manifest, the install log **and** `EmbeddedLLMStatus.Plan`, alongside every flag-bearing value, both expected footprints and the budgets, with a `Recorded` flag disambiguating a pre-plan manifest's zeros from a real decision. A degraded *launch* is therefore as visible in Settings as a degraded *install*, and `ProbeEmbeddedLLMDevices` exists for the follow-up question — "what does the machine look like NOW?" — against the recorded snapshot the plan was made from; it is reachable from the Go side and diagnostics only for now, since no UI affordance invokes it and `frontend/src/api/embeddedTuning.ts` deliberately wraps no such call.

**Add a config knob.** Add the field to `EmbeddedLLMConfig` with a yaml tag, a default in `backend/config/defaults.go`, a rule in `validateEmbeddedLLM` (`backend/config/config.go`, reached from `validate()`), an entry in `config.example.yaml`, and — if the UI exposes it — an RPC in `backend/frontend_api_embedded.go` plus a field on the `embedded_llm:state` payload and its type in `frontend/src/types/events.ts`.

**Add a memory-plan tuning knob.** The route is different, because the vocabulary already exists on core's side. Add a **pointer** field (or a struct of pointers) to `TuningConfig` with a `yaml:"…,omitempty"` tag, translate it in `TuningConfig.ToTuning` — which is where its range check belongs too, since `validateEmbeddedLLMTuning` is a delegation to that one function and a rule written anywhere else would be a second list to drift — and document the key, its unset meaning and its valid values in both the Configuration table above and `config.example.yaml`. Do **not** seed it in `defaults.go`: the all-nil zero value is the all-Auto default, and materializing a pointer converts *unset* into *explicit*, which for `Fit` and `CacheRAMMiB` changes the plan. Add a row to `TestEmbeddedLLMTuningYAMLRoundTrip`'s key list and to `fullySpecifiedTuning`, plus rejection and acceptance rows. Two contracts hold it in place: the sink carries `Tuning` through Install and Remove verbatim, so a new knob is preserved with no sink change; and the knob must correspond to a real `embeddedllm.Tuning` field, because anything the planner does not read is a setting that silently does nothing. If the new knob pins the offload, `Tuning.explicitOffloadShape()` in `plan.go` must learn about it too — see [Memory plan](#memory-plan).

**Add lifecycle events.** Both events are global (bare names, not session-scoped): `embedded_llm:install_progress` and `embedded_llm:state`. Extend the payload structs, `backend/events.go`, `frontend/src/types/events.ts` and the event catalog together.

## Related Specs

- [ADR-066: Embedded LLM Runtime (Bonsai 2 27B)](../decisions/066-embedded-llm-runtime.md) — the decision record; D1–D12 are cited by number throughout this spec. Its **D6** (the hard 16 GiB RAM floor) and **D10** (the RAM-tiered context, "never `-c 0`") and the *"whenever memory is short"* clause of its **D2** are superseded by ADR-067; ADR-066 itself stays `Accepted` and the rest of it stands.
  **Note on ADR-067's own D6.** ADR-067 D6 recorded that the measured budget replaces the RAM floor *"only when a topology exists"*, leaving `MinRAMGiB` in force for an unprobed caller. That residual is gone too: the gate is now combined on every path, deriving its host budget from the RAM probe and classifying the accelerator axis statically when nothing measured it (see [The combined memory gate](#the-combined-memory-gate)). ADR-067 is `Accepted` and therefore immutable per [META.md](../META.md), so this spec — not that ADR — is the authority on the gate, and the supersession is recorded here rather than by editing it
- [ADR-067: Memory-Aware Embedded LLM Provisioning](../decisions/067-memory-aware-embedded-llm-provisioning.md) — the pure planner, the `--fit` exclusivity rule, adaptive KV escalation, the two measurement-forced defaults (`-fitc` 65536, `-np` 1), the measured-budget gate that replaces the RAM floor only when a topology was probed, the `Tuning` vocabulary and the `Notes` contract; see [Memory plan](#memory-plan)
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
