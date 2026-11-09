package desktop

import (
	"github.com/v0lka/c0wrk/backend"
)

// --- App integration ------------------------------------------------------

// sessionEventBatcher returns the app's lazily-created event batcher, creating
// and starting it on first use. Lazy construction keeps zero-value App
// fixtures (tests) working without an explicit startup hook.
func (a *App) sessionEventBatcher() *backend.EventBatcher {
	a.batcherOnce.Do(func() {
		a.batcherPtr.Store(backend.NewEventBatcher(a.log(), a.emit))
	})
	return a.batcherPtr.Load()
}

// emitBatchedEvent enqueues one frontend event through the batcher (see Enqueue).
func (a *App) emitBatchedEvent(name string, args []any, coalesceKey string, barrier bool, sizeHint int) {
	a.sessionEventBatcher().Enqueue(name, args, coalesceKey, barrier, sizeHint)
}

// stopEventBatcher flushes pending events and terminates the batcher on
// shutdown; events emitted afterwards are delivered synchronously.
func (a *App) stopEventBatcher() {
	if b := a.batcherPtr.Load(); b != nil {
		b.Stop()
	}
}
