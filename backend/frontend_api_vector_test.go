package backend

import (
	"context"
	"testing"
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
