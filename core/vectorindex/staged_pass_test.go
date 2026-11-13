package vectorindex

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// stagedPassWatchdog bounds the barrier-driven assertions below. These are
// hang detectors, not elapsed-time assertions: every wait under it is woken
// by a deterministic signal (a seam barrier or a completion channel), and the
// watchdog only converts a wedged run into a diagnosable failure. See
// specs/domains/testing.md (timer and concurrency tests).
const stagedPassWatchdog = 5 * time.Second

// blockingChunkSeam builds a chunker seam that parks the pass inside
// preparation — the one stage staged acquisition (ADR-123) keeps lock-free.
// entered closes on the first chunk call; releaseAll unwinds every parked
// call and must be registered as cleanup BEFORE any fatal assertion (the
// idempotent-release rule), because the parked pass goroutine must always be
// joinable.
type blockingChunkSeam struct {
	entered     chan struct{}
	enteredOnce sync.Once
	release     chan struct{}
	releaseOnce sync.Once
	entries     func() // invoked on every chunk call, before parking (may be nil)
}

func newBlockingChunkSeam() *blockingChunkSeam {
	return &blockingChunkSeam{entered: make(chan struct{}), release: make(chan struct{})}
}

func (s *blockingChunkSeam) releaseAll() {
	s.releaseOnce.Do(func() { close(s.release) })
}

func (s *blockingChunkSeam) chunkFn(filePath string, content []byte, maxChunkSize, overlap int) ([]ChunkResult, error) {
	if s.entries != nil {
		s.entries()
	}
	s.enteredOnce.Do(func() { close(s.entered) })
	<-s.release
	return fakeChunkFunc(filePath, content, maxChunkSize, overlap)
}

// TestSearchRespondsDuringFullPass pins the ADR-123 contract the
// semantic_search tool depends on: a pass that has entered with the service
// already ready over a queryable collection keeps readiness up and holds the
// service write lock only in short windows, so a search must ANSWER while the
// pass is parked inside its lock-free preparation stage. The pass parks on a
// barrier, the search must complete before the barrier is released.
func TestSearchRespondsDuringFullPass(t *testing.T) {
	svc := setupTestService(t)
	ws := createTestWorkspace(t)

	// The first pass builds a queryable collection and re-arms readiness, so
	// the second pass enters the already-ready branch of the ADR-123 gate.
	indexer := NewIndexer(IndexerConfig{Service: svc, HashFn: fakeHashFunc, ChunkFn: fakeChunkFunc})
	if err := indexer.IndexFull(context.Background(), ws); err != nil {
		t.Fatalf("initial full index: %v", err)
	}
	if !svc.IsReady() {
		t.Fatal("service must be ready after the initial full index")
	}

	seam := newBlockingChunkSeam()
	t.Cleanup(seam.releaseAll)
	slowIndexer := NewIndexer(IndexerConfig{Service: svc, HashFn: fakeHashFunc, ChunkFn: seam.chunkFn})

	passDone := make(chan error, 1)
	go func() { passDone <- slowIndexer.IndexFull(context.Background(), ws) }()

	select {
	case <-seam.entered:
	case <-time.After(stagedPassWatchdog):
		t.Fatal("slowed pass never reached preparation")
	}

	// The pass is parked lock-free in preparation. The search rides the same
	// path as the semantic_search tool (WaitReady → RLock → query) and must
	// answer while the pass is parked.
	searchDone := make(chan error, 1)
	results := make(chan []SearchResult, 1)
	go func() {
		res, err := svc.HybridSearch(context.Background(), SearchOptions{Query: "package main", TopK: 5, Mode: ModeHybrid})
		searchDone <- err
		results <- res
	}()
	select {
	case err := <-searchDone:
		if err != nil {
			t.Fatalf("search during slowed pass: %v", err)
		}
		if got := <-results; len(got) == 0 {
			t.Fatal("search during slowed pass returned no results")
		}
	case <-time.After(stagedPassWatchdog):
		t.Fatal("search blocked for the whole slowed pass — staged acquisition broken")
	}

	seam.releaseAll()
	select {
	case err := <-passDone:
		if err != nil {
			t.Fatalf("slowed pass: %v", err)
		}
	case <-time.After(stagedPassWatchdog):
		t.Fatal("slowed pass never finished after release")
	}
}

// TestCloseCompletesDuringSlowedFullPass pins the Close half of ADR-123:
// Service.Close takes the service write lock and must therefore complete
// while a pass is parked in its lock-free preparation stage; once the pass
// reaches its first write window it must abort with ErrWorkingStoreDisplaced
// instead of committing into a closed store.
func TestCloseCompletesDuringSlowedFullPass(t *testing.T) {
	svc := setupTestService(t)
	ws := createTestWorkspace(t)

	indexer := NewIndexer(IndexerConfig{Service: svc, HashFn: fakeHashFunc, ChunkFn: fakeChunkFunc})
	if err := indexer.IndexFull(context.Background(), ws); err != nil {
		t.Fatalf("initial full index: %v", err)
	}

	seam := newBlockingChunkSeam()
	t.Cleanup(seam.releaseAll)
	slowIndexer := NewIndexer(IndexerConfig{Service: svc, HashFn: fakeHashFunc, ChunkFn: seam.chunkFn})

	passDone := make(chan error, 1)
	go func() { passDone <- slowIndexer.IndexFull(context.Background(), ws) }()

	select {
	case <-seam.entered:
	case <-time.After(stagedPassWatchdog):
		t.Fatal("slowed pass never reached preparation")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- svc.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close during slowed pass: %v", err)
		}
	case <-time.After(stagedPassWatchdog):
		t.Fatal("Close frozen by an in-flight pass — staged acquisition broken")
	}

	seam.releaseAll()
	select {
	case err := <-passDone:
		if !errors.Is(err, ErrWorkingStoreDisplaced) {
			t.Fatalf("slowed pass after Close = %v; want ErrWorkingStoreDisplaced", err)
		}
	case <-time.After(stagedPassWatchdog):
		t.Fatal("slowed pass never aborted after Close")
	}
}

// TestPassLeaseSerializesPasses pins the pass-lease half of ADR-123: a full
// pass holds the lease for its whole duration, so a second pass must run its
// entire preparation strictly AFTER the first one — the pass-vs-pass
// exclusion the whole-pass service write hold used to provide. The assertion
// is an ordering check over a recorded entry log, never a timed absence.
func TestPassLeaseSerializesPasses(t *testing.T) {
	svc := setupTestService(t)
	ws := createTestWorkspace(t)

	var orderMu sync.Mutex
	var order []string
	record := func(tag string) {
		orderMu.Lock()
		order = append(order, tag)
		orderMu.Unlock()
	}

	// The first pass parks EVERY file in preparation until released, so its
	// lease is provably held while the second pass is launched.
	first := newBlockingChunkSeam()
	t.Cleanup(first.releaseAll)
	first.entries = func() { record("first") }
	firstIndexer := NewIndexer(IndexerConfig{Service: svc, HashFn: fakeHashFunc, ChunkFn: first.chunkFn})

	secondEntries := make(chan struct{}, 64)
	secondIndexer := NewIndexer(IndexerConfig{Service: svc, HashFn: fakeHashFunc, ChunkFn: func(filePath string, content []byte, maxChunkSize, overlap int) ([]ChunkResult, error) {
		record("second")
		secondEntries <- struct{}{}
		return fakeChunkFunc(filePath, content, maxChunkSize, overlap)
	}})

	firstDone := make(chan error, 1)
	go func() { firstDone <- firstIndexer.IndexFull(context.Background(), ws) }()

	select {
	case <-first.entered:
	case <-time.After(stagedPassWatchdog):
		t.Fatal("first pass never reached preparation")
	}

	secondDone := make(chan error, 1)
	go func() { secondDone <- secondIndexer.IndexFull(context.Background(), ws) }()

	// Release the first pass; only then may the second one enter preparation.
	first.releaseAll()
	for range 4 {
		select {
		case <-secondEntries:
		case <-time.After(stagedPassWatchdog):
			t.Fatal("second pass never reached preparation after the first released the lease")
		}
	}
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first pass: %v", err)
		}
	case <-time.After(stagedPassWatchdog):
		t.Fatal("first pass never finished after release")
	}
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second pass: %v", err)
		}
	case <-time.After(stagedPassWatchdog):
		t.Fatal("second pass never finished")
	}

	// Ordering proof: while the first pass held the lease, NO second-pass
	// preparation may have interleaved.
	orderMu.Lock()
	defer orderMu.Unlock()
	sawSecond := false
	for _, tag := range order {
		if tag == "second" {
			sawSecond = true
			continue
		}
		if sawSecond {
			t.Fatalf("second pass preparation interleaved the first pass's lease hold: %v", order)
		}
	}
}
