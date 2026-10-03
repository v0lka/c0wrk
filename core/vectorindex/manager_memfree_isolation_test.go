package vectorindex

import (
	"sync/atomic"
	"testing"
)

// TestFreeOSMemory_DependencyIsInstanceLocal holds one service's scavenge
// across construction of another. The late call must still reach its owner.
func TestFreeOSMemory_DependencyIsInstanceLocal(t *testing.T) {
	var callsA, callsB atomic.Int32
	mgrA, _ := newMemFreeManager(t, &callsA)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		close(entered)
		<-release
		mgrA.freeOSMemoryAfterFullIndex()
	}()
	// Registered before any fatal assertion; release precedes join/Shutdown.
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		awaitVectorSignal(t, done, "instance-local scavenge cleanup")
	})
	awaitVectorSignal(t, entered, "first service's scavenge entry")
	mgrB, _ := newMemFreeManager(t, &callsB)
	close(release)
	awaitVectorSignal(t, done, "first service's scavenge completion")
	if got := callsA.Load(); got != 1 {
		t.Errorf("service A scavenge count = %d, want 1", got)
	}
	if got := callsB.Load(); got != 0 {
		t.Errorf("service B scavenge count before its own pass = %d, want 0", got)
	}
	mgrB.freeOSMemoryAfterFullIndex()
	if got := callsA.Load(); got != 1 {
		t.Errorf("service A scavenge count after B's pass = %d, want 1", got)
	}
	if got := callsB.Load(); got != 1 {
		t.Errorf("service B scavenge count after its own pass = %d, want 1", got)
	}
}
