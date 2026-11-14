package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/sp4rk/embedding"
)

// errSimulatedGPUFailure is the CUDA-shaped inference error the test double
// returns. Injecting a real GPU inference failure is impossible without the
// hardware, and this file pins the LAUNCHER's reaction to the degradation
// contract (sp4rk ADR-011: once per embedder, after consecutive failures,
// WARN + callback) — not the driver's failure modes — so a double is the
// sanctioned simulation here.
var errSimulatedGPUFailure = errors.New("CUDA failure: ONNXRuntimeError: an error occurred in the CUDA provider (simulated GPU death)")

// failingGPUEmbedder is the call counter for the embedder seam this test
// observes. The manager consumes embedding through ManagerConfig.EmbeddingFunc
// (the production seam wraps BatchEmbedder.EmbedDocuments), and the test
// below wires that seam as a closure that bumps the counter and fails with
// errSimulatedGPUFailure, so the count answers whether a debounce-armed pass
// reached the (broken) embedder at all.
type failingGPUEmbedder struct {
	calls atomic.Int64
}

// cudaEmbedderFacts returns the pre-slide facts a launch records for an
// "auto" request that WON the hardware ladder (auto→cuda is a success, not a
// fallback), with the nvidia-smi verdict present.
func cudaEmbedderFacts() VectorEmbedderInfo {
	verified := true
	return VectorEmbedderInfo{
		RequestedProvider: config.VectorIndexProviderAuto,
		EffectiveProvider: embedding.ExecutionProviderCUDA,
		CUDAVerified:      &verified,
	}
}

// currentEpoch assigns the launcher a fresh epoch the way launch() does,
// without running the init body (no model files in tests).
func currentEpoch(l *vectorIndexLauncher) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.epoch.Add(1)
}

// runtimeErrorNotices filters the recorder's stream down to the
// runtime_error notices.
func runtimeErrorNotices(recorder *launcherEmitRecorder) []map[string]string {
	var notices []map[string]string
	for _, ev := range recorder.snapshot() {
		if ev.Name != EventRuntimeError || len(ev.Data) == 0 {
			continue
		}
		if m, ok := ev.Data[0].(map[string]string); ok {
			notices = append(notices, m)
		}
	}
	return notices
}

// TestVectorLauncher_ApplyVectorProviderDegrade pins the recorded-facts
// mutation behind the runtime slide: the effective provider becomes the
// slide target, the cause replaces FallbackReason (the slide is the last,
// decisive divergence), the hardware-path verification verdicts are dropped
// (a stale "cuda_verified: true" beside an effective "cpu" would lie), and
// the requested provider + device survive untouched. The pre-record window
// (the launch has not recorded facts of its own yet) is refused.
func TestVectorLauncher_ApplyVectorProviderDegrade(t *testing.T) {
	recorder := &launcherEmitRecorder{}
	api, _ := newLauncherAPI(t, newEnabledConfig(false), recorder)

	if _, ok := api.Lifecycle().ApplyVectorProviderDegrade(embedding.ExecutionProviderCPU, errSimulatedGPUFailure.Error()); ok {
		t.Fatal("a degrade with no recorded embedder facts must be refused (the init path owns that window)")
	}

	verified := false
	api.Lifecycle().SetVectorEmbedderInfo(VectorEmbedderInfo{
		RequestedProvider: config.VectorIndexProviderCUDA,
		EffectiveProvider: embedding.ExecutionProviderCUDA,
		CUDAVerified:      &verified,
		OVDevice:          "gpu",
		DeviceID:          3,
	})

	info, ok := api.Lifecycle().ApplyVectorProviderDegrade(embedding.ExecutionProviderCPU, errSimulatedGPUFailure.Error())
	if !ok {
		t.Fatal("a degrade over recorded facts must apply")
	}
	if info.EffectiveProvider != embedding.ExecutionProviderCPU {
		t.Errorf("EffectiveProvider after the slide = %q, want %q", info.EffectiveProvider, embedding.ExecutionProviderCPU)
	}
	if info.FallbackReason != errSimulatedGPUFailure.Error() {
		t.Errorf("FallbackReason after the slide = %q, want the runtime cause", info.FallbackReason)
	}
	if info.CUDAVerified != nil {
		t.Error("the nvidia-smi verdict must be dropped by the slide (it described the dead CUDA path)")
	}
	if info.OVDevice != "" {
		t.Errorf("the xpu-smi verdict must be dropped by the slide, got %q", info.OVDevice)
	}
	if info.RequestedProvider != config.VectorIndexProviderCUDA {
		t.Errorf("RequestedProvider must survive the slide untouched, got %q", info.RequestedProvider)
	}
	if info.DeviceID != 3 {
		t.Errorf("DeviceID must survive the slide untouched, got %d", info.DeviceID)
	}

	// Every status producer reads these facts: the honest pair must be
	// visible through the production read path alone.
	var st VectorIndexStatus
	api.applyEmbedderInfo(&st)
	if st.ExecutionProvider != embedding.ExecutionProviderCPU ||
		st.RequestedExecutionProvider != config.VectorIndexProviderCUDA ||
		st.ProviderFallbackReason != errSimulatedGPUFailure.Error() ||
		st.CUDAVerified != nil {
		t.Errorf("applyEmbedderInfo after the slide = %+v, want the slid pair with the cause and no verdict", st)
	}
}

// TestVectorLauncher_OnProviderDegrade_SingleNoticeAndHonestPair pins the
// handler's contract: the embedder facts slide once, exactly ONE
// runtime_error toast with the dedicated error code fires even if the
// callback were invoked twice, and the message carries the runtime cause.
func TestVectorLauncher_OnProviderDegrade_SingleNoticeAndHonestPair(t *testing.T) {
	recorder := &launcherEmitRecorder{}
	api, log := newLauncherAPI(t, newEnabledConfig(false), recorder)
	l := NewVectorIndexLauncher(api, t.TempDir(), log, VectorIndexLauncherDeps{Emit: recorder.emit})
	epoch := currentEpoch(l)
	l.recordEmbedderInfo(epoch, cudaEmbedderFacts())

	handler := l.onProviderDegrade(epoch)
	handler(embedding.ExecutionProviderCUDA, embedding.ExecutionProviderCPU, errSimulatedGPUFailure.Error())
	handler(embedding.ExecutionProviderCUDA, embedding.ExecutionProviderCPU, "a second invocation must not re-notify")

	var st VectorIndexStatus
	api.applyEmbedderInfo(&st)
	if st.ExecutionProvider != embedding.ExecutionProviderCPU {
		t.Errorf("effective provider after the slide = %q, want cpu", st.ExecutionProvider)
	}
	if st.RequestedExecutionProvider != config.VectorIndexProviderAuto {
		t.Errorf("requested provider must stay the config value, got %q", st.RequestedExecutionProvider)
	}
	if st.ProviderFallbackReason != errSimulatedGPUFailure.Error() {
		t.Errorf("fallback reason after the slide = %q, want the runtime cause", st.ProviderFallbackReason)
	}
	if st.CUDAVerified != nil {
		t.Error("the cuda_verified verdict must be gone after the slide")
	}

	notices := runtimeErrorNotices(recorder)
	if len(notices) != 1 {
		t.Fatalf("runtime_error notices = %d, want exactly 1 (the degradation is once per embedder)", len(notices))
	}
	if notices[0]["error_code"] != vectorRuntimeDegradeErrorCode {
		t.Errorf("error_code = %q, want %q", notices[0]["error_code"], vectorRuntimeDegradeErrorCode)
	}
	if notices[0]["id"] == "" {
		t.Error("the notice must carry a fresh id")
	}
	if !strings.Contains(notices[0]["message"], errSimulatedGPUFailure.Error()) {
		t.Errorf("the notice message must carry the runtime cause, got %q", notices[0]["message"])
	}
}

// TestVectorLauncher_OnProviderDegrade_StaleLaunchDoesNotOwnTheStatus pins
// the ownership mirror of wireIfCurrent: a launch superseded by a newer
// launch (or a disable) must not overwrite the newer state's embedder facts
// or fire the toast — its embedder is being closed anyway.
func TestVectorLauncher_OnProviderDegrade_StaleLaunchDoesNotOwnTheStatus(t *testing.T) {
	recorder := &launcherEmitRecorder{}
	api, log := newLauncherAPI(t, newEnabledConfig(false), recorder)
	l := NewVectorIndexLauncher(api, t.TempDir(), log, VectorIndexLauncherDeps{Emit: recorder.emit})
	epoch := currentEpoch(l)
	l.recordEmbedderInfo(epoch, cudaEmbedderFacts())

	handler := l.onProviderDegrade(epoch)
	l.mu.Lock()
	l.epoch.Add(1) // a newer launch superseded this one
	l.mu.Unlock()

	handler(embedding.ExecutionProviderCUDA, embedding.ExecutionProviderCPU, errSimulatedGPUFailure.Error())

	var st VectorIndexStatus
	api.applyEmbedderInfo(&st)
	if st.ExecutionProvider != embedding.ExecutionProviderCUDA {
		t.Errorf("a stale launch's slide must not touch the current facts, effective provider = %q", st.ExecutionProvider)
	}
	if n := len(runtimeErrorNotices(recorder)); n != 0 {
		t.Errorf("a stale launch must not fire the toast, got %d notices", n)
	}
}

// TestVectorLauncher_OnProviderDegrade_PreRecordWindowLeavesInitInCharge
// pins the init-window guard in BOTH of its shapes — the guard is
// launch-scoped (infoEpoch == epoch), not mere facts-existence:
//
//  1. fresh install: no facts at all — a slide during the init-time warmup
//     probes lands before the launch records anything;
//  2. toggle restart: the lifecycle still holds the SUPERSEDED launch's
//     non-zero facts, and the fresh launch has not stamped its own yet —
//     a mere facts-existence guard would mutate those stale facts and fire
//     the runtime toast, misreporting a probe-window slide as a runtime
//     one. In both shapes the handler must refuse: no toast, no facts
//     write — the init path re-reads the provider after the probes
//     (reconcileProbeSlide) and records the slid provider itself.
func TestVectorLauncher_OnProviderDegrade_PreRecordWindowLeavesInitInCharge(t *testing.T) {
	recorder := &launcherEmitRecorder{}
	api, log := newLauncherAPI(t, newEnabledConfig(false), recorder)
	l := NewVectorIndexLauncher(api, t.TempDir(), log, VectorIndexLauncherDeps{Emit: recorder.emit})

	// 1. Fresh install: nothing recorded at all.
	epoch := currentEpoch(l)
	l.onProviderDegrade(epoch)(embedding.ExecutionProviderCUDA, embedding.ExecutionProviderCPU, errSimulatedGPUFailure.Error())

	var st VectorIndexStatus
	api.applyEmbedderInfo(&st)
	if st.ExecutionProvider != "" {
		t.Errorf("no facts may be written during the init window, got effective provider %q", st.ExecutionProvider)
	}
	if n := len(runtimeErrorNotices(recorder)); n != 0 {
		t.Errorf("no toast may fire during the init window, got %d notices", n)
	}

	// 2. Toggle restart: stale facts of a superseded launch are on file,
	//    the fresh launch has recorded nothing of its own yet. The stale
	//    facts must come through untouched (the reconcile owns this window).
	staleEpoch := currentEpoch(l)
	l.recordEmbedderInfo(staleEpoch, cudaEmbedderFacts())
	freshEpoch := currentEpoch(l)
	l.onProviderDegrade(freshEpoch)(embedding.ExecutionProviderCUDA, embedding.ExecutionProviderCPU, errSimulatedGPUFailure.Error())

	st = VectorIndexStatus{}
	api.applyEmbedderInfo(&st)
	if st.ExecutionProvider != embedding.ExecutionProviderCUDA {
		t.Errorf("a probe-window slide over stale facts must not mutate them, effective provider = %q", st.ExecutionProvider)
	}
	if st.ProviderFallbackReason != "" {
		t.Errorf("a probe-window slide over stale facts must not write a cause, got %q", st.ProviderFallbackReason)
	}
	if n := len(runtimeErrorNotices(recorder)); n != 0 {
		t.Errorf("a probe-window slide over stale facts must not fire the toast, got %d notices", n)
	}
}

// TestVectorLauncher_GPUErrorChain_HonestStatusThroughDegradeAndBackoff is
// the integration walk of the whole chain the launcher now wires, on a real
// manager with a failing embedder double:
//
//	GPU inference errors (init pass + debounced file-change pass both fail
//	  at the embedder)
//	→ (simulated sp4rk ADR-011 slide) → effective=cpu in the recorded
//	  facts + exactly one toast, so every status payload carries the honest
//	  requested/effective pair
//	→ the next file-change arm is backed off to the 5 s failure window
//	  (never debounce speed)
//	→ the production status payload (what a GetVectorIndexStatus poll
//	  serves) carries the manager's honest state — "ready" with a live
//	  collection, search still answering — plus the slid provider facts.
//
// With a live collection a failed pass deliberately keeps reporting "ready"
// (search still answers from the existing index); the unavailable/suspended
// verdicts belong to the trip/no-collection paths and are pinned on virtual
// time in core/vectorindex (ADR-122). This test proves the backend seam —
// manager + recorded embedder facts + toast — tell one coherent story.
func TestVectorLauncher_GPUErrorChain_HonestStatusThroughDegradeAndBackoff(t *testing.T) {
	recorder := &launcherEmitRecorder{}
	api, log := newLauncherAPI(t, newEnabledConfig(false), recorder)
	l := NewVectorIndexLauncher(api, t.TempDir(), log, VectorIndexLauncherDeps{Emit: recorder.emit})
	epoch := currentEpoch(l)
	l.recordEmbedderInfo(epoch, cudaEmbedderFacts())

	double := &failingGPUEmbedder{}
	mgr, err := vectorindex.NewManager(vectorindex.ManagerConfig{
		EmbeddingFunc: func(ctx context.Context, text string) ([]float32, error) {
			double.calls.Add(1)
			return nil, errSimulatedGPUFailure
		},
		Debounce: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(mgr.Shutdown)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\n// a body long enough to produce one chunk\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write corpus file: %v", err)
	}

	// Set the project up exactly like the registry does (the heavy init runs
	// in the background; a failing embedder cannot fail the call itself).
	if err := mgr.SwitchProject("proj-degrade-chain", root, filepath.Join(t.TempDir(), "vi"), vectorindex.ProjectCallbacks{}, filepath.Join(t.TempDir(), "cache")); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}

	// Leg 1 — GPU errors reach the embedder: the init pass walked the corpus
	// and failed at the first inference call (readiness flips either way, so
	// WaitReady is the race-free quiescence signal; a follow-up no-op pass
	// may repaint the status ready — the honest verdict is pinned on the
	// debounce leg below, whose failed report is the terminal one).
	readyCtx, cancelReady := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelReady()
	if err := mgr.Service().WaitReady(readyCtx); err != nil {
		t.Fatalf("WaitReady after the failed init pass: %v", err)
	}
	if double.calls.Load() == 0 {
		t.Fatal("the init pass never reached the failing embedder")
	}

	// Leg 2 — the debounced file-change pass fails on the GPU error. With a
	// live collection the pass's terminal contract reports "ready" (search
	// still answers from the existing index — workspace.md's ready-vs-
	// unavailable-by-collection-presence rule), so the honest verdict does
	// NOT ride this pass; what it does do is start the failure backoff. (A
	// fresh file is required: an unchanged workspace no-ops without
	// embedding anything.)
	before := double.calls.Load()
	if err := os.WriteFile(filepath.Join(root, "changed.go"), []byte("package main\n\nfunc changed() {}\n"), 0o644); err != nil {
		t.Fatalf("write change file: %v", err)
	}
	mgr.NotifyFileChange()
	pollUntil(t, 10*time.Second, func() bool { return double.calls.Load() > before },
		"the debounced file-change pass never reached the failing embedder")
	if mgr.GetIndexStatus().ErrorKind == vectorindex.IndexErrorKindSuspended {
		t.Error("one failed pass must not trip the circuit breaker")
	}

	// Leg 3 — the degradation (what sp4rk reports after its consecutive-
	// failure threshold): the recorded facts slide and exactly one toast
	// fires, so the next status payload carries the slid requested/effective
	// pair (ADR-045 stays honest after the runtime slide).
	l.onProviderDegrade(epoch)(embedding.ExecutionProviderCUDA, embedding.ExecutionProviderCPU, errSimulatedGPUFailure.Error())

	st := api.vectorIndexStatusFromManager(mgr)
	api.applyEmbedderInfo(&st)
	if st.ExecutionProvider != embedding.ExecutionProviderCPU ||
		st.RequestedExecutionProvider != config.VectorIndexProviderAuto ||
		st.ProviderFallbackReason != errSimulatedGPUFailure.Error() ||
		st.CUDAVerified != nil {
		t.Errorf("combined status provider facts = %+v, want the slid requested/effective pair with the cause", st)
	}
	if n := len(runtimeErrorNotices(recorder)); n != 1 {
		t.Fatalf("runtime_error notices after the slide = %d, want exactly 1", n)
	}

	// Leg 4 — the failure backoff engages: the arm after one failed pass
	// carries the 5 s backoff instead of the 25 ms debounce. Observed as
	// data: the next pass reaches the embedder only after the backed-off
	// window (floor-checked below), never at debounce speed. The exact
	// 5s→10s→…→cap schedule and the breaker's suspension run on virtual
	// time in core/vectorindex's ADR-122 tests.
	quietBefore := double.calls.Load()
	if err := os.WriteFile(filepath.Join(root, "backoff.go"), []byte("package main\n\nfunc backoff() {}\n"), 0o644); err != nil {
		t.Fatalf("write backoff file: %v", err)
	}
	armedAt := time.Now()
	mgr.NotifyFileChange()
	pollUntil(t, 10*time.Second, func() bool { return double.calls.Load() > quietBefore },
		"the backed-off arm never ran its pass")
	if elapsed := time.Since(armedAt); elapsed < 4500*time.Millisecond {
		t.Errorf("the arm after a failed pass ran after %s — the 5 s failure backoff did not engage (a plain debounce fires at ~25 ms)", elapsed)
	}

	// Leg 5 — the honest status on the production read path: the payload a
	// GetVectorIndexStatus poll would serve. With a live collection the
	// manager's own truth IS "ready" (search still answers from the
	// existing index — a failed pass never claims otherwise), and the
	// unavailable/suspended verdicts belong to the trip/no-collection paths
	// pinned on virtual time in core. What this seam must guarantee is that
	// the poll payload carries the manager's real state AND the slid
	// requested/effective pair — one coherent story.
	st = api.vectorIndexStatusFromManager(mgr)
	api.applyEmbedderInfo(&st)
	if st.State != string(vectorindex.IndexStateReady) {
		t.Errorf("combined status state after the quiesced passes = %q, want the manager's honest ready (live collection, search answering)", st.State)
	}
	if st.ExecutionProvider != embedding.ExecutionProviderCPU ||
		st.RequestedExecutionProvider != config.VectorIndexProviderAuto ||
		st.ProviderFallbackReason != errSimulatedGPUFailure.Error() ||
		st.CUDAVerified != nil {
		t.Errorf("combined status provider facts = %+v, want the slid requested/effective pair with the cause", st)
	}
}

// TestVectorLauncher_ReconcileProbeSlide pins the init-path reconcile for a
// runtime slide that landed during the verification probes: the OpenVINO
// device probe drives sustained inference, so sp4rk's runtime-degradation
// threshold (two consecutive failures) can fire BEFORE SetVectorEmbedderInfo
// — the slide callback lands in the pre-record window, and the recorded pair
// must be corrected from the live provider instead. Without it the OpenVINO
// probe window is exactly where the recorded facts would go stale for the
// embedder's whole life: effective "openvino" (plus a "cpu" device verdict)
// beside inference that actually runs on the CPU.
func TestVectorLauncher_ReconcileProbeSlide(t *testing.T) {
	verified := true
	info := VectorEmbedderInfo{
		RequestedProvider: config.VectorIndexProviderAuto,
		EffectiveProvider: embedding.ExecutionProviderOpenVINO,
		FallbackReason:    "",
		CUDAVerified:      &verified,
		OVDevice:          "gpu",
		DeviceID:          0,
	}

	corrected, slid := reconcileProbeSlide(info, embedding.ExecutionProviderCPU)
	if !slid {
		t.Fatal("a live provider diverging from the recorded one must reconcile")
	}
	if corrected.EffectiveProvider != embedding.ExecutionProviderCPU {
		t.Errorf("EffectiveProvider after the reconcile = %q, want %q", corrected.EffectiveProvider, embedding.ExecutionProviderCPU)
	}
	if corrected.CUDAVerified != nil {
		t.Error("the nvidia-smi verdict must be dropped by the reconcile (it described the dead hardware path)")
	}
	if corrected.OVDevice != "" {
		t.Errorf("the xpu-smi verdict must be dropped by the reconcile, got %q", corrected.OVDevice)
	}
	if corrected.RequestedProvider != config.VectorIndexProviderAuto {
		t.Errorf("RequestedProvider must survive the reconcile untouched, got %q", corrected.RequestedProvider)
	}
	if corrected.DeviceID != 0 {
		t.Errorf("DeviceID must survive the reconcile untouched, got %d", corrected.DeviceID)
	}
	// The runtime-slide marker: a non-empty reason on an auto→cpu pair is
	// what lets the status surface tell this slide from the init-time
	// ladder loss (which carries none).
	if corrected.FallbackReason != probeSlideFallbackReason {
		t.Errorf("FallbackReason after the reconcile = %q, want probeSlideFallbackReason", corrected.FallbackReason)
	}
	// Value semantics: the caller's original snapshot stays untouched
	// (including the empty pre-slide reason).
	if info.EffectiveProvider != embedding.ExecutionProviderOpenVINO || info.OVDevice != "gpu" || info.FallbackReason != "" {
		t.Errorf("the original snapshot was mutated in place: %+v", info)
	}

	// The honest-window case: the live provider matches the recorded one —
	// nothing changes, byte-identical.
	same, slid := reconcileProbeSlide(info, embedding.ExecutionProviderOpenVINO)
	if slid {
		t.Error("a matching live provider must not reconcile")
	}
	if same != info {
		t.Errorf("a matching live provider must pass the facts through unchanged: %+v vs %+v", same, info)
	}
}
