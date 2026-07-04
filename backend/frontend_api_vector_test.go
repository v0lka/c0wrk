package backend

import (
	"context"
	"testing"

	"github.com/v0lka/c0wrk/backend/project"
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
