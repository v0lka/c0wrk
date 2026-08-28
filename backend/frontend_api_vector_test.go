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
