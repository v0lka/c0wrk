package backend

import (
	"context"
	"sync"
	"testing"
)

// seedPublishedAPI marks a directly-constructed FrontendAPI as seed-published
// for tests that build an instance by hand and return it alongside other
// values (assignments mark themselves with f.seedPublished.Store(true)
// inline). It performs exactly Init's last publication step on an instance
// whose fields the test populated directly; see Init for the invariant.
func seedPublishedAPI(f *FrontendAPI) *FrontendAPI {
	f.seedPublished.Store(true)
	return f
}

// TestInitSeedPublicationOrdersPlainReads exercises the exact ordering the
// Init seed publication relies on (see the comment in Init): Wails-RPC-style
// readers call seedAcquire (guardless entries) and appCell (nil-guarded
// entries) while Init publishes the seed on another goroutine, then read the
// seed fields PLAINLY. The reader goroutines are pinned inside their loop
// (spinning channel barrier) before Init starts and released only after it
// returns, so the acquire attempts provably overlap the publication window
// and the plain reads follow it. Run under the race detector:
//
//	go test -race -count=1 ./backend -run TestInitSeedPublication
//
// The plain reads are only race-free because seedAcquire's atomic
// seedPublished Load (and appCell's post-publication RLock) orders them after
// Init's writes: remove that acquire and the detector fails this test.
func TestInitSeedPublicationOrdersPlainReads(t *testing.T) {
	f := &FrontendAPI{}

	const readers = 4
	spinning := make(chan struct{}, readers)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			spinning <- struct{}{}
			for {
				select {
				case <-stop:
					return
				default:
				}
				// The guardless-entry pattern: acquire, then plain reads.
				f.seedAcquire()
				_ = f.store
				_ = f.agentDir
				_ = f.emitEvent
				_ = f.logLevel
				_ = f.watcher
				// The nil-guarded-entry pattern: appCell, then a plain read.
				if f.appCell() != nil {
					_ = f.app
				}
			}
		}()
	}

	// Deterministic overlap: wait until every reader is inside its read
	// loop, run Init while they spin, and only then release them. No sleeps.
	for i := 0; i < readers; i++ {
		<-spinning
	}
	cfg := FrontendAPIConfig{
		App:      &Application{},
		LogLevel: "INFO",
		AppCtx:   context.Background,
	}
	_ = f.Lifecycle().Init(cfg)
	close(stop)
	wg.Wait()

	// Publication is observable on the init goroutine (program order).
	if f.logLevel != "INFO" {
		t.Fatalf("Init did not publish logLevel: got %q", f.logLevel)
	}
	if f.appCtx == nil {
		t.Fatal("Init did not publish appCtx")
	}
	if f.appCell() == nil {
		t.Fatal("appCell returned nil after Init published a non-nil app")
	}
	if ctx := f.ctx(); ctx == nil {
		t.Fatal("ctx() returned nil after Init")
	}
}
