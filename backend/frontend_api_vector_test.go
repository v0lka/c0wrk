package backend

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/vectorindex"
)

func TestSearchVectorStore_NilManager(t *testing.T) {
	f := &FrontendAPI{
		appCtx: context.Background,
	}

	_, err := f.SearchVectorStore(SearchRequest{Query: "query", TopK: 10})
	if err == nil {
		t.Fatal("expected error when vectorManager is nil")
	}
	if err.Error() != "vector search not available" {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestSearchVectorStore_NoProjectReturnsEmpty verifies that vector search is
// disabled for the No Project pseudo-project: it returns empty results (not an
// error) without touching the vector manager.
func TestSearchVectorStore_NoProjectReturnsEmpty(t *testing.T) {
	f := &FrontendAPI{
		appCtx:          context.Background,
		activeProjectID: project.NoProjectID,
	}

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

	st := f.GetVectorIndexStatus()
	if st.State != "unavailable" {
		t.Fatalf("expected state %q for No Project, got %q", "unavailable", st.State)
	}
}

// TestGetVectorIndexStatus_EmbedderInfoSurfaced pins the ADR-042
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

// TestGetVectorIndexStatus_NoEmbedderInfoKeepsFieldsEmpty pins the
// pre-init/broken-init shape: without recorded embedder info the provider
// fields stay empty (omitted from JSON), matching the legacy payload shape.
func TestGetVectorIndexStatus_NoEmbedderInfoKeepsFieldsEmpty(t *testing.T) {
	f := &FrontendAPI{
		appCtx:          context.Background,
		activeProjectID: project.NoProjectID,
	}

	st := f.GetVectorIndexStatus()
	if st.ExecutionProvider != "" || st.RequestedExecutionProvider != "" ||
		st.ProviderFallbackReason != "" || st.CUDAVerified != nil {
		t.Errorf("provider fields not empty without embedder info: %+v", st)
	}
}

// newStuckVectorAPI returns a FrontendAPI whose vector manager has a
// never-ready service (no SetProject/SetReady), simulating a stuck full
// index, with the given search wait timeout knob.
func newStuckVectorAPI(t *testing.T, waitTimeout time.Duration) *FrontendAPI {
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

	return &FrontendAPI{
		appCtx:        context.Background,
		vectorManager: mgr,
	}
}

// TestSearchVectorStore_BoundedWhenIndexStuck pins the defense-in-depth RPC
// bound: the readiness wait inside HybridSearch is wrapped with the manager's
// search_wait_timeout, so a stuck full index surfaces an actionable error
// within the bound instead of blocking the RPC indefinitely.
func TestSearchVectorStore_BoundedWhenIndexStuck(t *testing.T) {
	f := newStuckVectorAPI(t, 60*time.Millisecond)

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
	f := newStuckVectorAPI(t, 0)

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
	f := newStuckVectorAPI(t, 0)
	f.vectorManager.Service().SetReady(true)

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

	err := f.ReindexVectorIndex()
	if err == nil {
		t.Fatal("expected error for No Project")
	}
	if !strings.Contains(err.Error(), "No Project") {
		t.Errorf("expected a No Project error, got %v", err)
	}
}

// TestReindexVectorIndex_NilManager verifies that a forced reindex fails
// cleanly before the vector manager is wired (background init not done yet).
func TestReindexVectorIndex_NilManager(t *testing.T) {
	f := &FrontendAPI{
		appCtx: context.Background,
	}

	err := f.ReindexVectorIndex()
	if err == nil {
		t.Fatal("expected error when vectorManager is nil")
	}
	if err.Error() != "vector index not available" {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestReindexVectorIndex_NoActiveIndexer verifies that a forced reindex
// surfaces the manager's "no indexer configured" error when no project has
// been activated yet (the manager is wired but idle).
func TestReindexVectorIndex_NoActiveIndexer(t *testing.T) {
	f := newStuckVectorAPI(t, 0)

	err := f.ReindexVectorIndex()
	if err == nil {
		t.Fatal("expected error when no indexer is configured")
	}
	if !strings.Contains(err.Error(), "no indexer configured") {
		t.Errorf("expected a no-indexer error, got %v", err)
	}
}
