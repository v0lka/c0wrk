package backend

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/sp4rk/embedding"
)

// VectorIndexLauncherDeps carries the transport-neutral seams of the vector
// index launcher (stage P-12 of the desktop layering audit, §9.1): the event
// sink and the shutdown probe are injected so the launcher owns no Wails
// types. Emit mirrors the FrontendAPIConfig.EmitEvent seam; ShuttingDown
// reports "the GUI host is going away" (the desktop adapter derives it from
// the Wails context being cancelled). Nil deps default to a no-op sink and
// "never shutting down".
type VectorIndexLauncherDeps struct {
	Emit         func(name string, data ...any)
	ShuttingDown func() bool
}

// vectorIndexLauncher drives the restartable background ONNX init behind
// vector_index.enabled. The startup launch and every UpdateIndexingSettings
// enabled=true restart go through launch(); an epochal guard under mu makes
// every state change — a newer launch or the disable-side invalidation —
// invalidate all older launches, whose late wiring attempt then fails
// atomically and closes its own embedder instead of resurrecting a factory
// after VectorRoots.Disable unwound the registry. The live enable/disable
// lifecycle is specified in specs/domains/workspace.md (the
// "live-applied by UpdateIndexingSettings" invariant) and ADR-103.
type vectorIndexLauncher struct {
	api          *FrontendAPI
	agentDir     string
	log          *slog.Logger
	emit         func(name string, data ...any)
	shuttingDown func() bool

	mu    sync.Mutex
	epoch uint64
}

// NewVectorIndexLauncher assembles the launcher around the FrontendAPI whose
// Lifecycle owns the per-root registry; the deps carry the transport seams.
func NewVectorIndexLauncher(api *FrontendAPI, agentDir string, log *slog.Logger, deps VectorIndexLauncherDeps) *vectorIndexLauncher {
	if deps.Emit == nil {
		deps.Emit = func(string, ...any) {}
	}
	if deps.ShuttingDown == nil {
		deps.ShuttingDown = func() bool { return false }
	}
	return &vectorIndexLauncher{
		api:          api,
		agentDir:     agentDir,
		log:          log,
		emit:         deps.Emit,
		shuttingDown: deps.ShuttingDown,
	}
}

// ApplyEnabledChange is the exported entry point behind the
// SetVectorIndexEnabledHook seam (UpdateIndexingSettings's enabled
// transitions).
func (l *vectorIndexLauncher) ApplyEnabledChange(enabled bool) {
	l.applyEnabledChange(enabled)
}

// Launch starts one background init (the startup-phase entry point).
func (l *vectorIndexLauncher) Launch() {
	l.launch()
}

// applyEnabledChange is the backend hook (SetVectorIndexEnabledHook) behind
// UpdateIndexingSettings's enabled transitions. enabled=false only
// invalidates in-flight launches — the RPC parks the registry itself
// (VectorRoots.Disable) immediately after the hook returns, so the ordering
// contract is hook-first-then-disable; enabled=true starts a fresh
// background init.
func (l *vectorIndexLauncher) applyEnabledChange(enabled bool) {
	if !enabled {
		l.mu.Lock()
		l.epoch++
		l.mu.Unlock()
		return
	}
	l.launch()
}

// launch starts one background init under a fresh epoch and a fresh ready
// channel (closed exactly once when that init settles, success or
// known-unavailable) — the per-launch replacement of the old process-wide
// vectorOnce/vectorReady pair, which could fire only once per app run.
func (l *vectorIndexLauncher) launch() {
	l.mu.Lock()
	l.epoch++
	epoch := l.epoch
	l.mu.Unlock()
	ready := make(chan struct{})
	var once sync.Once
	go func() {
		defer once.Do(func() { close(ready) })
		l.runVectorIndexInit(epoch, ready)
	}()
}

// wireIfCurrent hands the factory + readiness to the per-root registry only
// while epoch is still current. The epoch comparison and the wiring are one
// atomic step under mu: a disable (or a newer launch) that raced in between
// has already bumped the epoch, so the stale init loses here, closes its own
// embedder, and leaves the registry untouched.
func (l *vectorIndexLauncher) wireIfCurrent(epoch uint64, factory func() (*vectorindex.Manager, error), ready <-chan struct{}, closeEmbedder func() error) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.epoch != epoch {
		return false
	}
	l.api.Lifecycle().SetVectorRootsFactory(factory, ready, closeEmbedder)
	return true
}

// runVectorIndexInit is the background ONNX-backed vector-index init, run in
// a goroutine after EventBackendReady so it never blocks the critical path.
// Every config knob is read through the LIVE snapshot accessor
// (configMu-guarded), never through the startup config pointer: a restart
// triggered by UpdateIndexingSettings must see the just-persisted values.
func (l *vectorIndexLauncher) runVectorIndexInit(epoch uint64, ready chan struct{}) {
	log := l.log
	startTime := time.Now()
	vi := l.api.Lifecycle().VectorIndexConfigSnapshot()

	// Recover from a CGO/ONNX panic (version mismatch, malformed model
	// file, …) so the app survives with vector search disabled rather than
	// crashing after EventBackendReady has already been emitted.
	defer func() {
		if r := recover(); r != nil {
			log.Error("vector index background init panicked", "panic", r)
			l.emit("vector_index:status", map[string]any{"available": false, "reason": fmt.Sprint(r)})
		}
	}()

	// vector_index.enabled gate: with indexing off (the config booted that
	// way — the disable RPC invalidates in-flight launches through the hook
	// instead of reaching this arm) the ONNX runtime is never loaded. Park
	// the registry so every routing path fails fast with the disabled error
	// (not the still-loading one) and surface the off state to the UI.
	if !vi.IndexingEnabled() {
		log.Info("vector search disabled by config (vector_index.enabled: false); skipping ONNX init")
		l.api.Lifecycle().VectorRoots().Disable()
		l.emit(EventVectorIndexStatus, VectorIndexStatus{State: "disabled", Indices: []string{}})
		return
	}

	modelPath := resolveModelPath("jina-v2-small.onnx", l.agentDir)
	tokenizerPath := resolveModelPath("jina-v2-small-tokenizer.json", l.agentDir)
	libraryPath := resolveONNXLibPath()

	// Create embedder directly via github.com/v0lka/sp4rk/embedding.
	if modelPath == "" || tokenizerPath == "" || libraryPath == "" {
		log.Info("vector search disabled (model files not found)")
		l.emit("vector_index:status", map[string]any{"available": false, "reason": "model files not found"})
		return
	}
	const (
		maxSeqLength = embedding.DefaultMaxSeqLength
		hiddenDim    = embedding.DefaultHiddenDim
	)
	cacheFingerprint, fingerprintErr := vectorindex.EmbeddingFingerprint(modelPath, tokenizerPath, vectorindex.EmbeddingFingerprintParams{
		MaxSeqLength:           maxSeqLength,
		Dimension:              hiddenDim,
		NormalizationAlgorithm: vectorindex.EmbeddingNormalizationAlgorithm,
	})
	if fingerprintErr != nil {
		// Cache is an optimization: fingerprint I/O failure must not disable
		// vector search or indexing.
		log.Warn("embedding cache disabled: artifact fingerprint failed", "error", fingerprintErr)
		cacheFingerprint = ""
	}

	// ONNX execution provider, driven by config
	// (vector_index.execution_provider / device_id) — the former
	// environment-only proof-of-concept knobs are gone. The provider is
	// resolved exactly ONCE per launch here and the embedder (plus its ONNX
	// session) is created once per launch (startup or an enabled=true
	// restart) and never re-created mid-launch: changing either knob
	// therefore requires an app restart. All four values pass through to
	// sp4rk verbatim — "auto" walks the hardware ladder (CUDA, then
	// OpenVINO) and falls back to CPU with a WARN inside NewEmbedder, "cpu"
	// forces the CPU provider, "cuda" demands the NVIDIA GPU, "openvino"
	// demands Intel hardware.
	requestedProvider := vi.ExecutionProvider
	if requestedProvider == "" {
		requestedProvider = config.VectorIndexProviderAuto
	}
	onnxDevice := vi.DeviceID
	embCfg := embedding.EmbedderConfig{
		ModelPath:         modelPath,
		TokenizerPath:     tokenizerPath,
		LibraryPath:       libraryPath,
		MaxSeqLength:      maxSeqLength,
		HiddenDim:         hiddenDim,
		BatchSize:         vi.EmbeddingBatchSize,
		IntraOpThreads:    vi.EmbeddingThreads,
		ExecutionProvider: requestedProvider,
		DeviceID:          onnxDevice,
		Logger:            log,
	}
	emb, embErr := embedding.NewEmbedder(embCfg)
	// embedderInfo feeds the execution-provider facts into every
	// vector-index status payload; the explicit-cuda/openvino→cpu
	// fallback (the only true fallback — an "auto" request always
	// resolves to a winner, so auto→cuda and auto→openvino are
	// successes and auto→cpu is Auto's expected degradation) is what
	// the settings UI renders from the requested/effective pair
	// (ADR-045 observability contract).
	embedderInfo := VectorEmbedderInfo{RequestedProvider: requestedProvider, DeviceID: onnxDevice}
	if embErr != nil && (requestedProvider == config.VectorIndexProviderCUDA || requestedProvider == config.VectorIndexProviderOpenVINO) {
		// Explicit "cuda"/"openvino" that cannot come up must not
		// silently kill vector search: continue on the CPU provider so
		// search stays available — but the user explicitly asked for a
		// hardware provider, so the fallback is made unmissable: WARN
		// log + runtime_error toast + the requested/effective mismatch
		// recorded in every status.
		providerLabel, errorCode, fixHint := "CUDA", "vector_cuda_fallback",
			"Fix the GPU setup (make fetch-onnx-gpu + CUDA driver) or set vector_index.execution_provider: auto|cpu."
		if requestedProvider == config.VectorIndexProviderOpenVINO {
			providerLabel, errorCode, fixHint = "OpenVINO", "vector_openvino_fallback",
				"Fix the Intel setup (make fetch-onnx-openvino installs the OpenVINO execution-provider plugin next to the ONNX Runtime library, Linux x64 only) or set vector_index.execution_provider: auto|cpu."
		}
		log.Warn(providerLabel+" execution provider unavailable, falling back to CPU",
			"error", embErr,
			"deviceID", onnxDevice,
			"library", libraryPath)
		l.emit(EventRuntimeError, map[string]string{
			"id":         uuid.New().String(),
			"message":    "Vector index: " + providerLabel + " execution provider is unavailable — running on the CPU, embeddings will be slower. Cause: " + embErr.Error() + " " + fixHint,
			"error_code": errorCode,
		})
		embedderInfo.FallbackReason = embErr.Error()
		embCfg.ExecutionProvider = embedding.ExecutionProviderCPU
		emb, embErr = embedding.NewEmbedder(embCfg)
	}
	if embErr != nil {
		log.Warn("vector search unavailable",
			"error", embErr,
			"executionProvider", requestedProvider,
			"deviceID", onnxDevice,
			"library", libraryPath)
		embedderInfo.FallbackReason = embErr.Error()
		l.api.Lifecycle().SetVectorEmbedderInfo(embedderInfo)
		l.emit("vector_index:status", map[string]any{
			"available":                    false,
			"reason":                       embErr.Error(),
			"requested_execution_provider": requestedProvider,
		})
		return
	}
	embedderInfo.EffectiveProvider = emb.ExecutionProvider()
	if embedderInfo.EffectiveProvider == requestedProvider && embedderInfo.FallbackReason != "" {
		// Unreachable combination today (reason set ⇒ divergence); guard
		// keeps the invariant explicit for future editors.
		embedderInfo.FallbackReason = ""
	}
	if embedderInfo.EffectiveProvider == embedding.ExecutionProviderCPU &&
		requestedProvider == config.VectorIndexProviderAuto {
		// "auto" degraded to the CPU provider inside sp4rk (the WARN with
		// the concrete cause is logged there); keep one app-level line
		// tying the divergence to vector search. The status payload
		// carries requested vs effective for the same reason. An "auto"
		// request that WON with CUDA (effective=cuda) is not a fallback —
		// only effective=cpu under an auto request is.
		log.Warn("vector index embedder running on CPU provider",
			"requestedExecutionProvider", requestedProvider,
			"effectiveExecutionProvider", embedderInfo.EffectiveProvider,
			"hint", "no usable hardware execution provider; see the WARN above for the per-provider causes")
	}
	if embedderInfo.EffectiveProvider == embedding.ExecutionProviderCUDA {
		// External verification that the process really runs on the GPU —
		// diagnostics only, never blocks or fails startup.
		verified := verifyEmbedderGPU(emb, log)
		embedderInfo.CUDAVerified = &verified
	}
	if embedderInfo.EffectiveProvider == embedding.ExecutionProviderOpenVINO {
		// External verification of the device class the OpenVINO
		// provider actually drives ("gpu" | "cpu" | "" unverified) —
		// sp4rk resolves the concrete device inside the rung (the plugin
		// uses only the first appended device, so a device it cannot
		// compile — an Intel NPU — is skipped for the next one), which
		// means only an external probe can tell GPU from CPU. Diagnostics
		// only, never blocks or fails startup.
		embedderInfo.OVDevice = vectorindex.VerifyEmbedderOvDevice(context.Background(), emb, log)
	}
	l.api.Lifecycle().SetVectorEmbedderInfo(embedderInfo)

	// The per-root registry (ADR-080) builds managers on demand through
	// this factory. CloseFn stays nil on every per-root manager: the
	// ONNX runtime is process-global and shared, so an LRU eviction must
	// never close it — the registry's ShutdownAll closes the embedder
	// exactly once after every manager is down.
	//
	// Every manager build re-snapshots the LIVE config (configMu-guarded
	// accessors): an UpdateIndexingSettings save persists new values
	// BEFORE any post-save manager build runs, so managers created after
	// the save carry the new indexing policy (the RPC's reload path
	// releases the pre-save managers and rebuilds the focus root through
	// this same closure).
	factory := func() (*vectorindex.Manager, error) {
		viNow := l.api.Lifecycle().VectorIndexConfigSnapshot()
		pol := ResolveVectorIndexingPolicy(viNow)
		contentFilterNow := viNow.ContentFilter.ResolveContentFilter()
		// Corpus residency (ADR-104 §5): apply the live count/byte policy
		// to the process-level corpus owner BEFORE building — idempotent,
		// immediately enforced against what is already retained, and
		// re-applied on every build so a saved settings change lands with
		// the next manager build (UpdateIndexingSettings rebuilds the focus
		// root through this closure) instead of needing a restart.
		if reg := l.api.Lifecycle().VectorCorpusRegistry(); reg != nil {
			reg.SetResidency(
				config.DerefInt(viNow.CorpusResidencyCapacity),
				config.DerefCorpusResidencyBudgetBytes(viNow.CorpusResidencyBudgetMb),
			)
			// §5 disk ledger budget (store_gc.go): the store-tree byte cap,
			// applied beside the residency bounds on every manager build so
			// a saved settings change lands with the next build.
			reg.SetDiskBudget(config.DerefStoreDiskBudgetBytes(viNow.StoreDiskBudgetMb))
		}
		return vectorindex.NewManager(vectorindex.ManagerConfig{
			EmbeddingFunc: emb.EmbeddingFunc(),
			// BatchEmbedder enables the batched document-embedding path in
			// Service.AddDocuments: chunk contents are embedded via the
			// embedder's batch ONNX session BEFORE the chromem commit, so
			// chromem skips its one-inference-per-chunk calls entirely.
			// *embedding.Embedder satisfies the interface directly.
			BatchEmbedder: emb,
			HybridConfig: vectorindex.HybridConfig{
				RRFK:              viNow.HybridRRFK,
				FanoutMultiplier:  viNow.HybridFanoutMultiplier,
				FanoutMin:         viNow.HybridFanoutMin,
				VectorScoreFloor:  config.DerefFloat(viNow.HybridVectorScoreFloor),
				VectorScoreRatio:  config.DerefFloat(viNow.HybridVectorScoreRatio),
				LexicalScoreRatio: config.DerefFloat(viNow.HybridLexicalScoreRatio),
			},
			MaxFileSize:      viNow.MaxFileSize,
			MaxChunkSize:     viNow.MaxChunkSize,
			MaxChunksPerFile: viNow.MaxChunksPerFile,
			ContentFilter:    &contentFilterNow,
			// Indexing policy (vector_index.respect_gitignore /
			// respect_aiignore / exclude_patterns / index_submodules):
			// resolved from the same live snapshot, so a policy save is
			// picked up by the next manager build without a restart.
			IgnoreFiles:       pol.IgnoreFiles,
			ExcludePatterns:   pol.ExcludePatterns,
			IncludeSubmodules: pol.IncludeSubmodules,
			// Indexing/search tuning knobs (vector_index.*). The config is
			// resolved (ApplyDefaults ran), so every value carries an
			// explicit default here; EmbeddingBatchSize must match the
			// EmbedderConfig value above — the Manager stores it for the
			// batched-embedding path, the embedder uses it as its ONNX
			// batch session capacity.
			EmbeddingBatchSize:        viNow.EmbeddingBatchSize,
			EmbeddingCacheFingerprint: cacheFingerprint,
			EmbeddingDimension:        hiddenDim,
			EmbeddingCacheMaxBytes:    viNow.EmbeddingCacheMaxBytes,
			PrepWorkers:               viNow.PrepWorkers,
			Debounce:                  time.Duration(viNow.DebounceMs) * time.Millisecond,
			ChunkOverlap:              viNow.ChunkOverlap,
			// SearchWaitTimeout: 0 = "fail fast" (explicit sentinel from
			// config, never defaulted); stored on the Manager for the
			// search-path wiring.
			SearchWaitTimeout: time.Duration(config.DerefInt(viNow.SearchWaitTimeoutMs)) * time.Millisecond,
			// Corpus (ADR-104 stage 2 owner/view): the process-level
			// shared-corpus registry — every root manager of a project
			// shares one snapshot-keyed corpus per commit (read-only) plus
			// its own per-root overlay as the working store.
			Corpus: l.api.Lifecycle().VectorCorpusRegistry(),
			// ParkCapacity: how many recently-closed projects keep their
			// vector-index state resident (vector_index.park_capacity;
			// resolved default 3, explicit 0 disables parking).
			ParkCapacity: config.DerefInt(viNow.ParkCapacity),
			// ParkBudgetBytes: cumulative resident-memory budget for the
			// park LRU (vector_index.park_budget_mb → bytes; resolved
			// default 1024 MiB, explicit -1 disables the byte budget
			// — park_capacity alone). A nil pointer (should not happen:
			// ApplyDefaults always resolves it) still yields the 1024 MiB
			// default rather than a fail-open zero budget.
			ParkBudgetBytes: config.DerefParkBudgetBytes(viNow.ParkBudgetMb),
			Logger:          log,
		})
	}

	// Abort registration if Shutdown has already run Cleanup — the
	// registry is closed and will never build a manager, so nobody would
	// release the embedder; close it here instead (quitting during init
	// must not leak the ONNX runtime).
	if l.shuttingDown() {
		log.Info("vector search init aborted: app shutting down")
		_ = emb.Close()
		return
	}
	// Hand the factory + readiness to the backend registry, gated on
	// this launch's epoch: a disable (or a newer launch) that raced the
	// init has bumped the epoch, and the stale launch then closes its
	// own embedder and leaves the parked registry untouched instead of
	// resurrecting a factory after VectorRoots.Disable unwound it.
	if !l.wireIfCurrent(epoch, factory, ready, emb.Close) {
		log.Info("vector search init superseded (disabled or restarted); closing the stale embedder")
		_ = emb.Close()
		return
	}
	// From this point managers are built per workspace root on demand.
	// The frontend's first SwitchProject (fired on backend:ready) almost
	// certainly ran before the wiring above and skipped vector setup
	// because the factory was not wired yet — and while indexing was
	// disabled, every switch deferred the same way. Apply the deferred
	// setup now, so the active project is indexed without a manual
	// project switch.
	l.api.Lifecycle().InitVectorIndexForActiveProject()
	log.Info("background init complete", "phase", "vector_index", "elapsed_ms", time.Since(startTime).Milliseconds())
}

// verifyEmbedderGPU performs the one-shot external GPU verification after a
// successful CUDA embedder init (lazy CUDA context → warmup, then an
// nvidia-smi probe of THIS process) — the cuda_verified verdict of the ADR-045
// status contract. Diagnostics only: errors are treated as "unverified",
// never as failure.
func verifyEmbedderGPU(emb *embedding.Embedder, log *slog.Logger) bool {
	// Warmup inference materializes the CUDA context. A failure here is a
	// strong signal the GPU path is broken end-to-end (later real searches
	// would fail too): WARN loudly and let the probe answer.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := emb.EmbedQuery(ctx, "warmup"); err != nil {
		log.Warn("CUDA embedder warmup inference failed",
			"error", err,
			"hint", "embedder stays enabled; real searches may fail if this persists")
	}
	inUse, err := embedding.GPUInUse(ctx)
	if err != nil {
		// Probe failure ≠ "not on GPU": treated as unverified, but always
		// visible — a missing verdict is suspicious.
		log.Warn("CUDA GPU verification probe failed",
			"error", err,
			"hint", "treated as unverified, not as CPU fallback")
	}
	if inUse {
		log.Info("CUDA verified via nvidia-smi",
			"pid", os.Getpid())
		return true
	}
	log.Warn("process absent from GPU compute apps — possible silent CPU fallback",
		"pid", os.Getpid(),
		"hint", "nvidia-smi did not list this PID; the CUDA-capable build may be sliding to the CPU provider")
	return false
}
