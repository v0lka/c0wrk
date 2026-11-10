package backend

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/vectorindex"
)

// TestSearchVectorStore_NoFocusRootErrors verifies the pre-focus surface: no
// project switch has pointed the Git panel anywhere yet, so the focus-routed
// search fails with the legacy "not available" message.
func TestSearchVectorStore_NoFocusRootErrors(t *testing.T) {
	f := &FrontendAPI{
		appCtx: context.Background,
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)

	_, err := f.SearchVectorStore(SearchRequest{Query: "query", TopK: 10})
	if err == nil {
		t.Fatal("expected error when no focus root is set")
	}
	if err.Error() != "vector search not available" {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestSearchVectorStore_NoProjectReturnsEmpty verifies that vector search is
// disabled for the No Project pseudo-project: it returns empty results (not an
// error) without touching the vector registry.
func TestSearchVectorStore_NoProjectReturnsEmpty(t *testing.T) {
	f := &FrontendAPI{
		appCtx:          context.Background,
		activeProjectID: project.NoProjectID,
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)

	got, err := f.SearchVectorStore(SearchRequest{Query: "query", TopK: 10})
	if err != nil {
		t.Fatalf("expected no error for No Project, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty results for No Project, got %d entries", len(got))
	}
}

// TestGetVectorIndexStatus_NoProjectUnavailable verifies that the index status
// RPC reports a disabled ("unavailable") state for No Project — neither
// "building" nor "ready".
func TestGetVectorIndexStatus_NoProjectUnavailable(t *testing.T) {
	f := &FrontendAPI{
		appCtx:          context.Background,
		activeProjectID: project.NoProjectID,
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)

	st := f.GetVectorIndexStatus()
	if st.State != "unavailable" {
		t.Fatalf("expected state %q for No Project, got %q", "unavailable", st.State)
	}
}

// TestGetVectorIndexStatus_EmbedderInfoSurfaced pins the ADR-045
// observability contract on the RPC path: once the desktop background init
// has recorded the embedder's execution-provider facts, every status —
// including the unavailable/No-Project variants — carries the effective and
// requested provider (and the CUDA verification verdict when probed), so a
// CUDA→CPU fallback is detectable from the status alone.
func TestGetVectorIndexStatus_EmbedderInfoSurfaced(t *testing.T) {
	verified := true
	f := &FrontendAPI{
		appCtx:          context.Background,
		activeProjectID: project.NoProjectID,
		vectorEmbedderInfo: VectorEmbedderInfo{
			EffectiveProvider: "cuda",
			RequestedProvider: "cuda",
			FallbackReason:    "",
			CUDAVerified:      &verified,
		},
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)

	st := f.GetVectorIndexStatus()
	if st.State != "unavailable" {
		t.Fatalf("expected state %q, got %q", "unavailable", st.State)
	}
	if st.ExecutionProvider != "cuda" {
		t.Errorf("ExecutionProvider = %q, want cuda", st.ExecutionProvider)
	}
	if st.RequestedExecutionProvider != "cuda" {
		t.Errorf("RequestedExecutionProvider = %q, want cuda", st.RequestedExecutionProvider)
	}
	if st.CUDAVerified == nil || !*st.CUDAVerified {
		t.Errorf("CUDAVerified = %v, want pointer to true", st.CUDAVerified)
	}
	if st.ProviderFallbackReason != "" {
		t.Errorf("ProviderFallbackReason = %q, want empty for honored request", st.ProviderFallbackReason)
	}
}

// TestGetVectorIndexStatus_FallbackFieldsSurfaced pins the fallback variant:
// requested cuda, effective cpu (explicit-cuda loud fallback), with the cause
// recorded — the payload a future UI renders as "running on CPU, GPU setup
// broken".
func TestGetVectorIndexStatus_FallbackFieldsSurfaced(t *testing.T) {
	f := &FrontendAPI{
		appCtx:          context.Background,
		activeProjectID: project.NoProjectID,
		vectorEmbedderInfo: VectorEmbedderInfo{
			EffectiveProvider: "cpu",
			RequestedProvider: "cuda",
			FallbackReason:    "AppendExecutionProvider_CUDA: provider library missing",
		},
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)

	st := f.GetVectorIndexStatus()
	if st.ExecutionProvider != "cpu" {
		t.Errorf("ExecutionProvider = %q, want cpu (fallback)", st.ExecutionProvider)
	}
	if st.RequestedExecutionProvider != "cuda" {
		t.Errorf("RequestedExecutionProvider = %q, want cuda", st.RequestedExecutionProvider)
	}
	if st.ProviderFallbackReason == "" {
		t.Error("ProviderFallbackReason empty, want the CUDA failure cause")
	}
	if st.CUDAVerified != nil {
		t.Errorf("CUDAVerified = %v, want nil (no probe on CPU embedder)", st.CUDAVerified)
	}
}

// TestGetVectorIndexStatus_OpenVINOEmbedderInfoSurfaced mirrors the cuda
// embedder-info test for the OpenVINO provider (same contract, different
// name): every status carries requested/effective plus the xpu-smi ov_device
// device-class verdict when probed, so the plugin's own GPU-vs-CPU device
// choice is observable from the status alone.
func TestGetVectorIndexStatus_OpenVINOEmbedderInfoSurfaced(t *testing.T) {
	f := &FrontendAPI{
		appCtx:          context.Background,
		activeProjectID: project.NoProjectID,
		vectorEmbedderInfo: VectorEmbedderInfo{
			EffectiveProvider: "openvino",
			RequestedProvider: "openvino",
			OVDevice:          "gpu",
		},
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)

	st := f.GetVectorIndexStatus()
	if st.State != "unavailable" {
		t.Fatalf("expected state %q, got %q", "unavailable", st.State)
	}
	if st.ExecutionProvider != "openvino" {
		t.Errorf("ExecutionProvider = %q, want openvino", st.ExecutionProvider)
	}
	if st.RequestedExecutionProvider != "openvino" {
		t.Errorf("RequestedExecutionProvider = %q, want openvino", st.RequestedExecutionProvider)
	}
	if st.OVDevice != "gpu" {
		t.Errorf("OVDevice = %q, want gpu", st.OVDevice)
	}
	if st.CUDAVerified != nil {
		t.Errorf("CUDAVerified = %v, want nil (no nvidia-smi probe on an OpenVINO embedder)", st.CUDAVerified)
	}
	if st.ProviderFallbackReason != "" {
		t.Errorf("ProviderFallbackReason = %q, want empty for honored request", st.ProviderFallbackReason)
	}
}

// TestGetVectorIndexStatus_OpenVINOFallbackFieldsSurfaced mirrors the cuda
// fallback variant for OpenVINO: requested openvino, effective cpu (explicit
// openvino loud fallback), with the cause recorded — the payload a UI renders
// as "running on CPU, Intel setup broken".
func TestGetVectorIndexStatus_OpenVINOFallbackFieldsSurfaced(t *testing.T) {
	f := &FrontendAPI{
		appCtx:          context.Background,
		activeProjectID: project.NoProjectID,
		vectorEmbedderInfo: VectorEmbedderInfo{
			EffectiveProvider: "cpu",
			RequestedProvider: "openvino",
			FallbackReason:    "OpenVINO execution provider plugin not found at /opt/app/libonnxruntime_providers_openvino_plugin.so",
		},
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)

	st := f.GetVectorIndexStatus()
	if st.ExecutionProvider != "cpu" {
		t.Errorf("ExecutionProvider = %q, want cpu (fallback)", st.ExecutionProvider)
	}
	if st.RequestedExecutionProvider != "openvino" {
		t.Errorf("RequestedExecutionProvider = %q, want openvino", st.RequestedExecutionProvider)
	}
	if st.ProviderFallbackReason == "" {
		t.Error("ProviderFallbackReason empty, want the OpenVINO failure cause")
	}
	if st.OVDevice != "" {
		t.Errorf("OVDevice = %q, want empty (no probe on a CPU embedder)", st.OVDevice)
	}
	if st.CUDAVerified != nil {
		t.Errorf("CUDAVerified = %v, want nil (no probe on CPU embedder)", st.CUDAVerified)
	}
}

// TestGetVectorIndexStatus_NoEmbedderInfoKeepsFieldsEmpty pins the
// pre-init/broken-init shape: without recorded embedder info the provider
// fields stay empty (omitted from JSON), matching the legacy payload shape.
func TestGetVectorIndexStatus_NoEmbedderInfoKeepsFieldsEmpty(t *testing.T) {
	f := &FrontendAPI{
		appCtx:          context.Background,
		activeProjectID: project.NoProjectID,
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)

	st := f.GetVectorIndexStatus()
	if st.ExecutionProvider != "" || st.RequestedExecutionProvider != "" ||
		st.ProviderFallbackReason != "" || st.CUDAVerified != nil || st.OVDevice != "" {
		t.Errorf("provider fields not empty without embedder info: %+v", st)
	}
}

// newFocusedStuckVectorAPI returns a FrontendAPI whose per-root registry
// already holds a manager with a never-ready service (no SetProject/SetReady,
// simulating a stuck full index) for a synthetic focus root, with that root
// focused — the minimal wiring for the user-facing RPC paths, which route
// through the registry's focus (ADR-080). Injecting the entry directly keeps
// the test off resolveRootPlan (no project manager, no real worktrees).
func newFocusedStuckVectorAPI(t *testing.T, waitTimeout time.Duration) (*FrontendAPI, *vectorindex.Manager) {
	t.Helper()
	mgr, err := vectorindex.NewManager(vectorindex.ManagerConfig{
		EmbeddingFunc: func(ctx context.Context, text string) ([]float32, error) {
			return make([]float32, 4), nil
		},
		SearchWaitTimeout: waitTimeout,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { mgr.Shutdown() })

	f := &FrontendAPI{appCtx: context.Background}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	root := canonicalRoot(t.TempDir())
	vr := f.vectorRootsRegistry()
	vr.mu.Lock()
	vr.roots[root] = &vectorRootEntry{mgr: mgr, projectID: "p-test", lastUsed: time.Now()}
	vr.focus = root
	vr.mu.Unlock()
	return f, mgr
}

// TestSearchVectorStore_BoundedWhenIndexStuck pins the defense-in-depth RPC
// bound: the readiness wait inside HybridSearch is wrapped with the manager's
// search_wait_timeout, so a stuck full index surfaces an actionable error
// within the bound instead of blocking the RPC indefinitely.
func TestSearchVectorStore_BoundedWhenIndexStuck(t *testing.T) {
	f, _ := newFocusedStuckVectorAPI(t, 60*time.Millisecond)

	start := time.Now()
	_, err := f.SearchVectorStore(SearchRequest{Query: "query", TopK: 10})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when index never becomes ready")
	}
	if elapsed > 60*time.Millisecond+500*time.Millisecond {
		t.Errorf("SearchVectorStore blocked for %v; want bounded by 60ms", elapsed)
	}
	if !strings.Contains(err.Error(), "index not yet ready") || !strings.Contains(err.Error(), "retry") {
		t.Errorf("expected actionable not-ready error with retry suggestion, got %v", err)
	}
}

// TestSearchVectorStore_FailFastZeroTimeout pins the 0 sentinel on the RPC
// path: zero waiting — a not-ready index errors immediately with the
// actionable message.
func TestSearchVectorStore_FailFastZeroTimeout(t *testing.T) {
	f, _ := newFocusedStuckVectorAPI(t, 0)

	start := time.Now()
	_, err := f.SearchVectorStore(SearchRequest{Query: "query", TopK: 10})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when index is not ready and timeout is 0")
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("fail-fast SearchVectorStore took %v; want ~0", elapsed)
	}
	if !strings.Contains(err.Error(), "retry") {
		t.Errorf("expected retry suggestion in error, got %v", err)
	}
}

// TestSearchVectorStore_FailFastReadyIndexProceeds pins that the fail-fast
// (0) sentinel does not break the ready path: with the index ready, the RPC
// dispatches through the NoWait variant, passes the readiness gate, and
// surfaces the ordinary downstream error (no collection on this manager) —
// not a not-ready error.
func TestSearchVectorStore_FailFastReadyIndexProceeds(t *testing.T) {
	f, mgr := newFocusedStuckVectorAPI(t, 0)
	mgr.Service().SetReady(true)

	start := time.Now()
	_, err := f.SearchVectorStore(SearchRequest{Query: "query", TopK: 10})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the ordinary no-collection error, got nil")
	}
	if strings.Contains(err.Error(), "retry") || strings.Contains(err.Error(), "not ready") {
		t.Errorf("ready index must not produce a not-ready error, got %v", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("fail-fast RPC on a ready index took %v; want immediate", elapsed)
	}
}

// TestReindexVectorIndex_NoProject verifies that a forced reindex is rejected
// for the No Project pseudo-project, where vector indexing is disabled.
func TestReindexVectorIndex_NoProject(t *testing.T) {
	f := &FrontendAPI{
		appCtx:          context.Background,
		activeProjectID: project.NoProjectID,
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)

	err := f.ReindexVectorIndex()
	if err == nil {
		t.Fatal("expected error for No Project")
	}
	if !strings.Contains(err.Error(), "No Project") {
		t.Errorf("expected a No Project error, got %v", err)
	}
}

// TestReindexVectorIndex_NoFocusRoot verifies that a forced reindex fails
// cleanly before any focus root has been established (no project switch has
// pointed the Git panel anywhere yet).
func TestReindexVectorIndex_NoFocusRoot(t *testing.T) {
	f := &FrontendAPI{
		appCtx: context.Background,
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)

	err := f.ReindexVectorIndex()
	if err == nil {
		t.Fatal("expected error when no focus root is set")
	}
	// The refusal carries the routing cause (the frontend surfaces it as a
	// runtime_error toast), instead of a bare "not available" string.
	if !strings.HasPrefix(err.Error(), "vector index not available: ") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestReindexVectorIndex_NoActiveIndexer verifies that a forced reindex
// surfaces the manager's "no indexer configured" error when no project has
// been activated yet (the manager is wired but idle).
func TestReindexVectorIndex_NoActiveIndexer(t *testing.T) {
	f, _ := newFocusedStuckVectorAPI(t, 0)

	err := f.ReindexVectorIndex()
	if err == nil {
		t.Fatal("expected error when no indexer is configured")
	}
	if !strings.Contains(err.Error(), "no indexer configured") {
		t.Errorf("expected a no-indexer error, got %v", err)
	}
}

// TestListVectorIndexGPUs_DevicesCarryProvider verifies the device-picker
// RPC surface: the merged multi-vendor enumeration must tag every entry
// with its execution provider ("cuda" for nvidia-smi devices, "openvino"
// for Intel vulkaninfo devices), and Intel entries always carry index 0 —
// the only device_id the plugin-based OpenVINO surface accepts. On a
// machine without any GPU tooling the list is simply empty (nil error) —
// the assertions hold trivially there; on Intel hardware (the reference
// Arrow Lake iGPU machine) the Arc entry must appear with the openvino
// provider.
func TestListVectorIndexGPUs_DevicesCarryProvider(t *testing.T) {
	f := &FrontendAPI{
		appCtx: context.Background,
	}
	f.seedPublished.Store(true)

	devices, err := f.ListVectorIndexGPUs()
	if err != nil {
		t.Fatalf("ListVectorIndexGPUs() error = %v, want nil on a healthy machine or one without GPU tooling", err)
	}
	sawOpenVINO := false
	for _, d := range devices {
		switch d.Provider {
		case "cuda":
			// NVIDIA entry: the driver ordinal is the device id.
		case "openvino":
			sawOpenVINO = true
			if d.Index != 0 {
				t.Errorf("openvino device %q carries index %d, want 0 (the OpenVINO surface has no per-device selection)", d.Name, d.Index)
			}
			if d.Name == "" {
				t.Error("openvino device carries an empty name")
			}
		default:
			t.Errorf("device %q carries unknown provider %q", d.Name, d.Provider)
		}
	}
	t.Logf("ListVectorIndexGPUs() = %d devices (openvino present: %v)", len(devices), sawOpenVINO)
}
