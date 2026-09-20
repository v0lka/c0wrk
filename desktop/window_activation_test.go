package desktop

import (
	"context"
	"testing"
)

// windowActivationFixture records the Wails window calls showWindow's
// platform branches make. The per-call seams (windowRaiseFn /
// windowUnminimiseFn / windowIsMinimisedFn) stand in for wailsRuntime so the
// activation matrix is exercisable on any platform; the windowShowFn seam
// stays unset so the branches themselves run.
type windowActivationFixture struct {
	app       *App
	calls     []string // ordered log of the runtime calls made
	minimised bool     // what windowIsMinimisedFn reports
}

func newWindowActivationFixture() *windowActivationFixture {
	f := &windowActivationFixture{}
	f.app = NewApp()
	f.app.ctx = context.Background()
	f.app.windowRaiseFn = func(context.Context) {
		f.calls = append(f.calls, "Show")
	}
	f.app.windowUnminimiseFn = func(context.Context) {
		f.calls = append(f.calls, "Unminimise")
	}
	f.app.windowIsMinimisedFn = func(context.Context) bool {
		f.calls = append(f.calls, "IsMinimised")
		return f.minimised
	}
	return f
}

// withShowWindowPlatform swaps showWindowPlatform for a test and restores it
// afterwards.
func withShowWindowPlatform(t *testing.T, platform string) {
	t.Helper()
	orig := showWindowPlatform
	showWindowPlatform = platform
	t.Cleanup(func() { showWindowPlatform = orig })
}

// TestShowWindow_LinuxUsesPresentAlone verifies the Linux fallback path:
// when the pager-source activation is unavailable (Wayland, headless, no
// matching window), the activation is a single WindowUnminimise
// (= gtk_window_present — raise + deiconify + focus). The plain WindowShow
// (gtk_widget_show) is deliberately absent: for an already-mapped window it
// is a no-op that cannot raise a covered or minimized window, which was
// exactly the reported bug.
func TestShowWindow_LinuxUsesPresentAlone(t *testing.T) {
	f := newWindowActivationFixture()
	withShowWindowPlatform(t, "linux")
	f.app.x11PagerActivateFn = func() bool { return false }

	f.app.showWindow(context.Background())

	if len(f.calls) != 1 || f.calls[0] != "Unminimise" {
		t.Fatalf("linux fallback must be exactly one Unminimise (gtk_window_present), got %v", f.calls)
	}
}

// TestShowWindow_LinuxPagerActivationSkipsFallback verifies the primary
// Linux path: a successful EWMH pager-source activation (the x11PagerActivateFn
// seam standing in for x11ActivateOwnWindow) fully replaces the Wails calls —
// no Unminimise, no Show. The pager activation already mapped+raised+focused
// the window; stacking a stale-timestamp gtk_window_present on top would only
// re-trigger the KWin focus-stealing rejection this fix exists to avoid.
func TestShowWindow_LinuxPagerActivationSkipsFallback(t *testing.T) {
	f := newWindowActivationFixture()
	withShowWindowPlatform(t, "linux")
	f.app.x11PagerActivateFn = func() bool { return true }

	f.app.showWindow(context.Background())

	if len(f.calls) != 0 {
		t.Fatalf("successful pager activation must skip the Wails fallback entirely, got %v", f.calls)
	}
}

// TestShowWindow_DarwinUsesShow verifies the darwin branch keeps the
// full-activation WindowShow (makeKeyAndOrderFront +
// activateIgnoringOtherApps) and does not touch Unminimise — deminiaturize
// alone would not raise a covered window, and the Show already covers it.
func TestShowWindow_DarwinUsesShow(t *testing.T) {
	f := newWindowActivationFixture()
	withShowWindowPlatform(t, "darwin")

	f.app.showWindow(context.Background())

	if len(f.calls) != 1 || f.calls[0] != "Show" {
		t.Fatalf("darwin activation must be exactly one Show, got %v", f.calls)
	}
}

// TestShowWindow_WindowsShowThenRestoreWhenMinimised verifies the Windows
// branch asks IsMinimised after Show and restores only a really-minimized
// window: WPF Form.Restore() would UNMAXIMIZE a maximized window, so the
// guard is load-bearing.
func TestShowWindow_WindowsShowThenRestoreWhenMinimised(t *testing.T) {
	f := newWindowActivationFixture()
	withShowWindowPlatform(t, "windows")
	f.minimised = true

	f.app.showWindow(context.Background())

	want := []string{"Show", "IsMinimised", "Unminimise"}
	if len(f.calls) != len(want) {
		t.Fatalf("windows (minimized) expected %v, got %v", want, f.calls)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Fatalf("windows (minimized) expected %v, got %v", want, f.calls)
		}
	}
}

// TestShowWindow_WindowsSkipsRestoreWhenNotMinimised is the maximized-window
// guard: IsMinimised false → no Restore, so a maximized c0wrk window is not
// unmaximized by a notification click.
func TestShowWindow_WindowsSkipsRestoreWhenNotMinimised(t *testing.T) {
	f := newWindowActivationFixture()
	withShowWindowPlatform(t, "windows")
	f.minimised = false

	f.app.showWindow(context.Background())

	want := []string{"Show", "IsMinimised"}
	if len(f.calls) != len(want) {
		t.Fatalf("windows (not minimized) expected %v, got %v", want, f.calls)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Fatalf("windows (not minimized) expected %v, got %v", want, f.calls)
		}
	}
}

// TestShowWindow_UnknownPlatformKeepsShow guards the default branch: an
// unrecognized platform keeps the historical plain-Show behavior.
func TestShowWindow_UnknownPlatformKeepsShow(t *testing.T) {
	f := newWindowActivationFixture()
	withShowWindowPlatform(t, "plan9")

	f.app.showWindow(context.Background())

	if len(f.calls) != 1 || f.calls[0] != "Show" {
		t.Fatalf("unknown platform must keep the plain Show, got %v", f.calls)
	}
}

// TestShowWindow_SeamsStillShortCircuit verifies windowShowFn still takes
// precedence over the platform branches — the existing tests
// (window_show_test.go, notifications_test.go) drive every reveal path
// through that seam and must not observe the per-call seams.
func TestShowWindow_SeamsStillShortCircuit(t *testing.T) {
	f := newWindowActivationFixture()
	withShowWindowPlatform(t, "windows")
	var showCalls int
	f.app.windowShowFn = func(context.Context) { showCalls++ }

	f.app.showWindow(context.Background())

	if showCalls != 1 {
		t.Fatalf("windowShowFn must take precedence, got %d calls", showCalls)
	}
	if len(f.calls) != 0 {
		t.Fatalf("platform seams must stay untouched behind windowShowFn, got %v", f.calls)
	}
}
