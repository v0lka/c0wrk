package vectorindex

import (
	"strings"
	"testing"
	"time"
)

// TestNotReadyMessage_ProgressAndRetry pins the actionable timeout message:
// it must carry the index state, progress percentage, current file, and a
// retry suggestion with a ripgrep/glob fallback — the contract the
// semantic_search tool surfaces when its bounded wait expires.
func TestNotReadyMessage_ProgressAndRetry(t *testing.T) {
	msg := NotReadyMessage(IndexStatus{
		State:        IndexStateIndexing,
		FilesIndexed: 45,
		TotalFiles:   100,
		CurrentFile:  "core/orchestrator.go",
	})

	for _, want := range []string{
		"index not yet ready",
		"indexing",
		"45%",
		"core/orchestrator.go",
		"retry semantic_search later",
		"ripgrep",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("NotReadyMessage %q missing %q", msg, want)
		}
	}
}

// TestNotReadyMessage_NoTotalsOmitsPercentage verifies the zero-status shape
// (before any indexing progress is known): a placeholder state, no bogus 0%,
// and the retry suggestion are still present.
func TestNotReadyMessage_NoTotalsOmitsPercentage(t *testing.T) {
	msg := NotReadyMessage(IndexStatus{})

	if strings.Contains(msg, "%") {
		t.Errorf("NotReadyMessage must omit the percentage when TotalFiles is unknown, got %q", msg)
	}
	if !strings.Contains(msg, "initializing") {
		t.Errorf("empty state must render as initializing, got %q", msg)
	}
	if !strings.Contains(msg, "retry") {
		t.Errorf("NotReadyMessage must always suggest retry, got %q", msg)
	}
}

// TestManagerNotReadyError_FromCurrentStatus verifies the Manager-level
// helper reads the live status snapshot.
func TestManagerNotReadyError_FromCurrentStatus(t *testing.T) {
	mgr, err := NewManager(ManagerConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { mgr.Shutdown() })

	err = mgr.NotReadyError()
	if err == nil {
		t.Fatal("expected not-ready error for a fresh manager")
	}
	if !strings.Contains(err.Error(), "retry") {
		t.Errorf("expected retry suggestion, got %v", err)
	}
}

// TestManagerSearchWaitTimeout_RawAccessor pins that the accessor returns
// the raw configured value, including the 0 fail-fast sentinel — never a
// silently defaulted one (the config layer owns the default).
func TestManagerSearchWaitTimeout_RawAccessor(t *testing.T) {
	mgr, err := NewManager(ManagerConfig{
		EmbeddingFunc:     fakeEmbeddingFunc(),
		SearchWaitTimeout: 4 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { mgr.Shutdown() })

	if got := mgr.SearchWaitTimeout(); got != 4*time.Second {
		t.Errorf("SearchWaitTimeout() = %v, want 4s (raw configured value)", got)
	}

	mgrZero, err := NewManager(ManagerConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { mgrZero.Shutdown() })

	if got := mgrZero.SearchWaitTimeout(); got != 0 {
		t.Errorf("unset SearchWaitTimeout must stay 0 (fail-fast sentinel), got %v", got)
	}
}
