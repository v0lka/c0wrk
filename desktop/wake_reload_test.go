package desktop

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// TestDeferredWakeReload_NilCtx_NoPanic verifies the nil-context guard: when
// Startup has not yet bound a.ctx, deferredWakeReload returns without blocking
// or panicking. This is the same guard the other callbacks rely on.
func TestDeferredWakeReload_NilCtx_NoPanic(t *testing.T) {
	a := &App{} // a.ctx == nil

	// Must not block and must not dereference a nil ctx.
	done := make(chan struct{})
	go func() {
		a.deferredWakeReload()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("deferredWakeReload blocked with nil ctx")
	}
}

// TestDeferredWakeReload_ContextCanceled_NoReload verifies the key safety
// property: a reload scheduled by the wake observer is cancelled when the app
// is shutting down (ctx.Done fires before the wakeReloadDelay elapses). This
// is what prevents a torn-down context from being reloaded during teardown,
// and — because the reload is deferred off the synchronous wake callback — is
// also what stopped the silent-exit race.
func TestDeferredWakeReload_ContextCanceled_NoReload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var called atomic.Bool
		a := &App{
			reloadAppFn: func(context.Context) { called.Store(true) },
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a.ctx = ctx
		cancel()

		a.deferredWakeReload()
		// Drain cancellation, then cross the full virtual reload deadline:
		// Wait alone could also observe a worker parked on its timer.
		synctest.Wait()
		<-time.After(wakeReloadDelay)
		synctest.Wait()
		if called.Load() {
			t.Fatal("reloadFrontend was called despite context being canceled before the delay")
		}
	})
}
