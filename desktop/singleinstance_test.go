package desktop

import (
	"context"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/options"
)

// TestSingleInstanceOptions_Wiring: the Wails option carries the bundle-id
// identity and relays to the app handler.
func TestSingleInstanceOptions_Wiring(t *testing.T) {
	app := NewApp()
	opts := SingleInstanceOptions(app)
	if opts == nil {
		t.Fatal("SingleInstanceOptions must return a non-nil option")
	}
	if opts.UniqueId != SingleInstanceLockID {
		t.Fatalf("UniqueId = %q, want %q", opts.UniqueId, SingleInstanceLockID)
	}
	if opts.OnSecondInstanceLaunch == nil {
		t.Fatal("OnSecondInstanceLaunch must be wired")
	}
}

// TestHandleSecondInstanceLaunch_FocusesWindow: a relay on a live app focuses
// the existing window through the showWindow funnel (the same reveal-AND-raise
// the notification-click path uses) and logs the relay for diagnostics.
func TestHandleSecondInstanceLaunch_FocusesWindow(t *testing.T) {
	app := NewApp()
	app.ctx = context.Background()
	showCtx := make(chan context.Context, 1)
	app.windowShowFn = func(ctx context.Context) { showCtx <- ctx }

	log, buf := captureLogger()
	app.logger = log

	app.handleSecondInstanceLaunch(options.SecondInstanceData{
		Args:             []string{"--late-arg"},
		WorkingDirectory: "/tmp/second",
	})

	select {
	case ctx := <-showCtx:
		if ctx != app.ctx {
			t.Fatal("showWindow must receive the app context")
		}
	default:
		t.Fatal("showWindow was not called for the second-instance relay")
	}
	if out := buf.String(); !strings.Contains(out, "second instance launch relayed") {
		t.Fatalf("relay must be logged, got: %s", out)
	}
}

// TestHandleSecondInstanceLaunch_BeforeStartupNoop: Wails starts the
// second-instance processor before OnStartup binds the context, so a relay
// can arrive with ctx still nil — it must be a logged no-op, never a panic.
func TestHandleSecondInstanceLaunch_BeforeStartupNoop(t *testing.T) {
	app := NewApp() // ctx deliberately nil
	called := false
	app.windowShowFn = func(context.Context) { called = true }

	log, buf := captureLogger()
	app.logger = log

	app.handleSecondInstanceLaunch(options.SecondInstanceData{})

	if called {
		t.Fatal("showWindow must not run before Startup binds the context")
	}
	if out := buf.String(); !strings.Contains(out, "before Startup") {
		t.Fatalf("the skipped focus must be logged, got: %s", out)
	}
}
