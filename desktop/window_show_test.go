package desktop

import (
	"context"
	"testing"

	"github.com/v0lka/c0wrk/backend"
)

// windowShowFixture wires an App with the window-reveal test hooks: a counting
// stand-in for wailsRuntime.WindowShow (a.windowShowFn) and a captured event
// sink (a.wailsEmit). Both take precedence over the production wailsRuntime
// calls, which panic on a context that no live Wails runtime owns.
type windowShowFixture struct {
	app       *App
	showCalls int
	showCtxs  []context.Context
	emitted   []string
}

func newWindowShowFixture() *windowShowFixture {
	f := &windowShowFixture{}
	f.app = NewApp()
	f.app.ctx = context.Background()
	f.app.windowShowFn = func(ctx context.Context) {
		f.showCalls++
		f.showCtxs = append(f.showCtxs, ctx)
	}
	f.app.wailsEmit = func(eventName string, _ ...any) {
		f.emitted = append(f.emitted, eventName)
	}
	return f
}

// TestShowWindow_UsesSeamAndForwardsContext verifies showWindow prefers the
// injected hook over wailsRuntime and hands it the caller's context unchanged.
// Every reveal path depends on this delegation to stay testable.
func TestShowWindow_UsesSeamAndForwardsContext(t *testing.T) {
	f := newWindowShowFixture()
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")

	f.app.showWindow(ctx)

	if f.showCalls != 1 {
		t.Fatalf("expected exactly one window show, got %d", f.showCalls)
	}
	if got := f.showCtxs[0].Value(ctxKey{}); got != "marker" {
		t.Errorf("expected the caller's context to be forwarded, got value %v", got)
	}
}

// TestDomReady_ShowsWindow is the core regression guard for the hidden-window
// race. OnDomReady is the one reveal that fires after Wails has finished
// setting the window up, so it can never be overtaken by that setup — unlike
// the reveals issued from the OnStartup goroutine. Losing this call would
// remove the guarantee that a window hidden by window setup comes back.
func TestDomReady_ShowsWindow(t *testing.T) {
	f := newWindowShowFixture()

	f.app.DomReady(context.Background())

	if f.showCalls != 1 {
		t.Fatalf("expected DomReady to request exactly one window show, got %d", f.showCalls)
	}
	if len(f.emitted) != 0 {
		t.Errorf("expected DomReady to emit no events, got %v", f.emitted)
	}
}

// TestDomReady_RepeatIsHarmless covers the reload path: reloadFrontend makes
// the webview re-fire OnDomReady on an already-visible window. The repeat must
// stay a plain idempotent reveal — no panic, no events, no extra state.
func TestDomReady_RepeatIsHarmless(t *testing.T) {
	f := newWindowShowFixture()

	f.app.DomReady(context.Background())
	f.app.DomReady(context.Background())
	f.app.DomReady(context.Background())

	if f.showCalls != 3 {
		t.Fatalf("expected one window show per DomReady, got %d", f.showCalls)
	}
	if len(f.emitted) != 0 {
		t.Errorf("expected no events from repeated DomReady, got %v", f.emitted)
	}
}

// TestEmitBackendReady_ShowsWindow verifies the last startup-path safety net
// still asks for the window before telling the frontend the backend is up.
func TestEmitBackendReady_ShowsWindow(t *testing.T) {
	f := newWindowShowFixture()

	f.app.emitBackendReady(nil, nil, false, testLoggerForPhases())

	if f.showCalls != 1 {
		t.Fatalf("expected emitBackendReady to request one window show, got %d", f.showCalls)
	}
	if len(f.emitted) != 1 || f.emitted[0] != backend.EventBackendReady {
		t.Errorf("expected one %s event, got %v", backend.EventBackendReady, f.emitted)
	}
}

// TestEmitBackendReady_NilCtxSkipsShow guards the nil-ctx path used by tests
// that run startup phases without a Wails lifecycle: no window call, but the
// backend:ready signal must still reach the frontend.
func TestEmitBackendReady_NilCtxSkipsShow(t *testing.T) {
	f := newWindowShowFixture()
	f.app.ctx = nil

	f.app.emitBackendReady(nil, nil, false, testLoggerForPhases())

	if f.showCalls != 0 {
		t.Errorf("expected no window show with a nil ctx, got %d", f.showCalls)
	}
	if len(f.emitted) != 1 || f.emitted[0] != backend.EventBackendReady {
		t.Errorf("expected one %s event, got %v", backend.EventBackendReady, f.emitted)
	}
}
