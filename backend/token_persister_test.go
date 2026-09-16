package backend

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"
)

type recordingTokenStore struct {
	mu      sync.Mutex
	updates []tokenUpdateRecord
}

type tokenUpdateRecord struct {
	id, model, family string
	input, output     int
	fill              float64
}

func (s *recordingTokenStore) UpdateSessionTokens(_ context.Context, id string, inputTokens, outputTokens int, model, family string, fillPercent float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, tokenUpdateRecord{
		id: id, model: model, family: family,
		input: inputTokens, output: outputTokens, fill: fillPercent,
	})
	return nil
}

// inlineSubmit runs the operation synchronously so the tests can observe writes
// deterministically without waiting on a background writer.
func inlineSubmit(op func()) { op() }

func TestTokenPersister_CoalescesLatestPerSession(t *testing.T) {
	store := &recordingTokenStore{}
	// A long interval means the ticker never fires during the test; only the
	// explicit Flush produces writes.
	tp := newTokenPersister(store, inlineSubmit, time.Hour, time.Second, nil)
	defer tp.Close()

	tp.Record("s1", 1, 1, "m1", "f1", 10)
	tp.Record("s1", 2, 2, "m2", "f2", 20) // latest wins
	tp.Record("s2", 5, 9, "m", "f", 50)
	tp.Flush()

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.updates) != 2 {
		t.Fatalf("expected 2 coalesced updates (one per session), got %d: %+v", len(store.updates), store.updates)
	}
	sort.Slice(store.updates, func(i, j int) bool { return store.updates[i].id < store.updates[j].id })

	s1 := store.updates[0]
	if s1.id != "s1" || s1.input != 2 || s1.output != 2 || s1.fill != 20 || s1.model != "m2" {
		t.Errorf("s1 was not coalesced to the latest report: %+v", s1)
	}
	s2 := store.updates[1]
	if s2.id != "s2" || s2.input != 5 || s2.output != 9 {
		t.Errorf("s2 update wrong: %+v", s2)
	}
}

func TestTokenPersister_CloseFlushesPending(t *testing.T) {
	store := &recordingTokenStore{}
	tp := newTokenPersister(store, inlineSubmit, time.Hour, time.Second, nil)

	tp.Record("s1", 7, 8, "m", "f", 70)
	tp.Close() // must flush the pending update

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.updates) != 1 {
		t.Fatalf("expected Close to flush the pending update, got %d", len(store.updates))
	}
	if u := store.updates[0]; u.input != 7 || u.output != 8 || u.fill != 70 {
		t.Errorf("wrong flushed values: %+v", u)
	}
}

// TestTokenPersister_CloseIsIdempotent guards against a double-close panic on
// shutdown paths that may both flush and close.
func TestTokenPersister_CloseIsIdempotent(t *testing.T) {
	store := &recordingTokenStore{}
	tp := newTokenPersister(store, inlineSubmit, time.Hour, time.Second, nil)
	tp.Record("s1", 1, 1, "m", "f", 1)
	tp.Close()
	tp.Close()
}
