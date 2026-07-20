//go:build darwin

package desktop

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation -framework AppKit -framework WebKit

#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>

// c0wrkOnPowerWake is implemented in Go. It is invoked from the Objective-C
// observer block below whenever the system or the displays wake from sleep.
extern void c0wrkOnPowerWake(void);

// c0wrkLogWebviewRecovery is implemented in Go. It is invoked from the IMP
// below each time the WKWebView's web-content process terminates and is
// recovered, so the previously-silent death surfaces in the slog log.
extern void c0wrkLogWebviewRecovery(char *reason);

// NOTE ON API: the WKNavigationDelegate protocol exposes exactly ONE
// web-content-process-termination hook:
//   - (void)webViewWebContentProcessDidTerminate:(WKWebView *)webView;
// (macOS 10.11+). There is no public -webView:webContentProcessDidTerminateWithReason:
// selector in WebKit's headers. Wails v2.12.0 does not implement even this
// one hook, so a killed renderer leaves the window blank. This file fills
// that gap by attaching the selector to the real WailsContext class at
// runtime (see c0wrkInstallWebviewRecovery).

// Minimal WailsContext surface for this translation unit. The full class is
// implemented in the Wails module (github.com/wailsapp/wails/v2) and linked
// into the binary ONLY by `wails build` (via the generated main) — never by
// plain `go build`/`go test`. Only the accessors are referenced here, and
// Objective-C property access compiles to dynamic message sends
// (objc_msgSend(self, "shuttingDown") / "webview"): that emits NO link-time
// reference to _OBJC_CLASS_$_WailsContext. That matters — a compile-time
// @implementation WailsContext (Category) WOULD emit such a symbol, leaving
// it unresolved under `go build ./...` / `go test ./...` on darwin and
// breaking macOS CI. Resolving the class by name via objc_getClass and
// attaching the method via class_addMethod keeps the standard Go toolchain
// link-clean while the real WailsContext (loaded by Wails at app start) still
// receives the hook. MRC-friendly property attributes (assign/retain) match
// Wails's own non-ARC darwin sources.
@interface WailsContext : NSObject
@property (nonatomic, assign) BOOL shuttingDown;
@property (nonatomic, retain) WKWebView *webview;
@end

// c0wrkLastRecoveryTime timestamps the most recent web-content-process
// recovery. It debounces reload storms: WebKit may post the delegate callback
// several times in quick succession (repeated wake-related kills), and the
// recovery itself can transiently re-trigger. Main-thread-only access — the
// WKNavigationDelegate callback and the dispatch_async below both target the
// main queue, so no lock is required.
static NSTimeInterval c0wrkLastRecoveryTime = 0.0;

// c0wrkRecoveryDebounceSeconds is the minimum interval between recovery
// reloads. ~3s collapses a burst of terminations into a single reload.
static const NSTimeInterval c0wrkRecoveryDebounceSeconds = 3.0;

// c0wrkWebContentProcessDidTerminateIMP is the IMP attached to WailsContext as
// -webViewWebContentProcessDidTerminate:. WebKit invokes it when the
// web-content process dies (crash, memory ceiling, or an OS-initiated kill
// such as the post-sleep suspension on macOS 26). The real API carries no
// termination reason, so a descriptive cause is reported to the log.
//
// It (a) bails when shuttingDown is set, (b) debounces via
// c0wrkLastRecoveryTime, and (c) dispatches -[WKWebView reload] to the main
// queue so the reload never runs synchronously inside the WebKit callback that
// reported the death. reload is used instead of Wails's WindowReloadApp (which
// runs evaluateJavaScript and cannot revive a dead process — a dead process
// has no JS runtime): reload relaunches the web-content process and re-fetches
// the wails:// start URL. shuttingDown is re-checked inside the block in case
// teardown began between the gate and the dispatch.
static void c0wrkWebContentProcessDidTerminateIMP(id self, SEL _cmd, WKWebView *terminatedWebView) {
    (void)_cmd;
    (void)terminatedWebView;
    WailsContext *ctx = (WailsContext *)self;
    if (ctx.shuttingDown) {
        return;
    }
    NSTimeInterval now = [NSDate timeIntervalSinceReferenceDate];
    if (now - c0wrkLastRecoveryTime < c0wrkRecoveryDebounceSeconds) {
        return;
    }
    c0wrkLastRecoveryTime = now;

    // Surface the death — the silent-exit symptom was partly because nothing
    // logged the process termination.
    c0wrkLogWebviewRecovery((char *)"content-process-terminated");

    dispatch_async(dispatch_get_main_queue(), ^{
        if (ctx.shuttingDown) return;
        WKWebView *wv = ctx.webview;
        if (wv != nil) {
            [wv reload];
        }
    });
}

// c0wrkFindWKWebViewInView recursively searches an NSView hierarchy for the
// first WKWebView. The c0wrk app is single-window with one webview; this is
// how the wake-reload path reaches it. The -webViewWebContentProcessDidTerminate:
// IMP receives self and can read ctx.webview directly, but the Go wake callback
// has no such handle — so it locates the webview through the window hierarchy
// at wake time (long after startup, the webview always exists).
static WKWebView *c0wrkFindWKWebViewInView(NSView *view) {
    if (view == nil) {
        return nil;
    }
    if ([view isKindOfClass:[WKWebView class]]) {
        return (WKWebView *)view;
    }
    for (NSView *sub in [view subviews]) {
        WKWebView *found = c0wrkFindWKWebViewInView(sub);
        if (found != nil) {
            return found;
        }
    }
    return nil;
}

// c0wrkReloadWebview forces a native -[WKWebView reload] on the main thread.
// It is the wake-path counterpart to the -webViewWebContentProcessDidTerminate:
// recovery: both reload the webview NATIVELY. Wails's WindowReloadApp is an
// evaluateJavaScript("window.location.href = startURL") call (Wails v2.12.0
// darwin frontend.go:214, WailsContext.m:451), which is the WRONG primitive
// for post-wake recovery:
//   - It cannot run until the SUSPENDED web-content process fully resumes; a
//     call during the resume window may be queued then dropped.
//   - When the webview is already at the start URL, re-assigning
//     window.location.href to the same value is a no-op in WebKit — no fresh
//     load, so the volatile-marked render surface is never rebuilt.
// Because this machine's sleep SUSPENDS (does not kill) the web-content
// process, -webViewWebContentProcessDidTerminate: does NOT fire on wake, so
// this native reload is the only reliable recovery primitive. reload always
// re-fetches the current request and rebuilds the render surface at the native
// level, independent of JS-runtime and URL state. The outcome (webview found
// vs not found) is logged from the dispatched block so the result is never
// silent. Declared static — like c0wrkInstallWebviewRecovery and
// c0wrkRegisterWakeObserver — because cgo's //export path compiles the
// preamble into multiple object files; a non-static definition would collide
// (duplicate symbol). cgo can call static C functions directly.
static void c0wrkReloadWebview(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        WKWebView *wv = nil;
        for (NSWindow *win in [NSApp windows]) {
            wv = c0wrkFindWKWebViewInView([win contentView]);
            if (wv != nil) {
                break;
            }
        }
        if (wv == nil) {
            c0wrkLogWebviewRecovery((char *)"wake-reload: webview not found in window hierarchy");
            return;
        }
        c0wrkLogWebviewRecovery((char *)"wake-reload");
        [wv reload];
    });
}

// c0wrkInstallWebviewRecovery attaches the web-content-process-termination
// hook to the real WailsContext class at runtime. Returns:
//   0 — hook installed (the normal case under Wails v2.12.0).
//   1 — WailsContext class not found (e.g. a future Wails rename); the hook
//       is skipped and the wake path (desktop/startup.go) remains as a
//       best-effort fallback for the suspended-render case.
//   2 — WailsContext already implements the selector (a future Wails that
//       forwards the hook natively); its implementation is left in place.
//
// Called from registerPowerWakeObserver during Startup, by which point Wails
// has loaded WailsContext and installed it as the webview's navigation
// delegate (WailsContext.m: setNavigationDelegate:self). class_addMethod
// mutates the class's method table, which the runtime consults at every
// message dispatch, so the hook takes effect on the already-created instance
// immediately.
static int c0wrkInstallWebviewRecovery(void) {
    Class cls = objc_getClass("WailsContext");
    if (cls == NULL) {
        return 1;
    }
    SEL sel = sel_registerName("webViewWebContentProcessDidTerminate:");
    if (class_getInstanceMethod(cls, sel) != NULL) {
        return 2;
    }
    // Type encoding "v@:@": void return, id self, SEL _cmd, id (WKWebView*).
    class_addMethod(cls, sel,
                    (IMP)c0wrkWebContentProcessDidTerminateIMP, "v@:@");
    return 0;
}

// c0wrkRegisterWakeObserver registers NSWorkspace notification-center
// observers for both system wake (NSWorkspaceDidWakeNotification) and display
// wake (NSWorkspaceScreensDidWakeNotification, macOS 10.15+). The block runs
// on the thread that posts the notification — the main thread for NSWorkspace
// power notifications — and forwards to Go via c0wrkOnPowerWake. The observer
// is retained by the notification center for the lifetime of the app.
static void c0wrkRegisterWakeObserver(void) {
    @autoreleasepool {
        NSNotificationCenter *nc = [[NSWorkspace sharedWorkspace] notificationCenter];
        void (^wakeBlock)(NSNotification *) = ^(NSNotification *note) {
            (void)note;
            c0wrkOnPowerWake();
        };
        [nc addObserverForName:NSWorkspaceDidWakeNotification
                         object:nil
                          queue:nil
                     usingBlock:wakeBlock];
        [nc addObserverForName:NSWorkspaceScreensDidWakeNotification
                         object:nil
                          queue:nil
                     usingBlock:wakeBlock];
    }
}
*/
import "C"

import (
	"log/slog"
	"sync/atomic"
)

// powerWakeHandler holds the Go-side recovery callback invoked when macOS
// reports a power-state wake. It is set in Startup before the native observer
// is registered and read from the cgo-invoked c0wrkOnPowerWake on wake, hence
// the atomic for safe cross-thread access (the observer block runs on the main
// thread, which is not synchronized with Startup's writer goroutine).
var powerWakeHandler atomic.Pointer[func()]

// recoveryLogger holds the slog logger used by c0wrkLogWebviewRecovery to
// report WKWebView web-content-process terminations. It is set in
// registerPowerWakeObserver (which receives the app logger) so the Obj-C
// IMP — a cgo callback that cannot receive an injected logger — can log
// without resorting to a package global. Read atomically because the callback
// fires on the main thread, unsynchronized with the Startup writer.
var recoveryLogger atomic.Pointer[slog.Logger]

// registerPowerWakeObserver registers native macOS observers that fire when
// the system or the displays wake from sleep (invoking onPowerWake on each
// wake), arms the logger used by the web-content-process recovery path, and
// installs the WKNavigationDelegate web-content-process-termination hook on
// the real WailsContext class via runtime method injection. No-op on
// non-darwin builds — see powerstate_other.go.
func registerPowerWakeObserver(log *slog.Logger, onPowerWake func()) {
	handler := onPowerWake
	powerWakeHandler.Store(&handler)
	recoveryLogger.Store(log)

	// Attach -webViewWebContentProcessDidTerminate: to WailsContext. Done by
	// runtime method injection (objc_getClass + class_addMethod) rather than a
	// compile-time Obj-C category so the desktop package links cleanly under
	// the standard Go toolchain — a category would emit an unresolved
	// _OBJC_CLASS_$_WailsContext symbol (that class is linked only by
	// `wails build`). See specs/decisions/018-macos-webview-recovery.md.
	switch int(C.c0wrkInstallWebviewRecovery()) {
	case 0:
		log.Info("installed WKWebView web-content-process recovery hook on WailsContext")
	case 1:
		log.Warn("WailsContext class not found; web-content-process recovery hook not installed (wake path remains as fallback)")
	case 2:
		log.Info("WailsContext already implements webContentProcessDidTerminate; recovery hook not installed")
	}

	C.c0wrkRegisterWakeObserver()
	log.Info("registered macOS power-state wake observer")
}

//export c0wrkOnPowerWake
func c0wrkOnPowerWake() {
	if h := powerWakeHandler.Load(); h != nil && *h != nil {
		(*h)()
	}
}

//export c0wrkLogWebviewRecovery
func c0wrkLogWebviewRecovery(reason *C.char) {
	log := recoveryLogger.Load()
	if log == nil {
		// Top-level-boundary fallback: the callback fired before Startup wired
		// the logger. Still log so the death is never silent.
		log = slog.Default()
	}
	if reason != nil {
		log.Info("WKWebView web-content process terminated; reloading to recover", "reason", C.GoString(reason))
	} else {
		log.Info("WKWebView web-content process terminated; reloading to recover")
	}
}

// reloadWebviewNative forces a NATIVE -[WKWebView reload] on the main thread.
// It is the wake-path recovery primitive: Wails's WindowReloadApp is an
// evaluateJavaScript("window.location.href = startURL") call that cannot
// restore a post-wake blank webview (no-op when URL unchanged; cannot run
// until the suspended process resumes). See powerstate_darwin.go and ADR-018.
// The reload is dispatched to the main queue inside the C helper and returns
// immediately to the caller; the result is logged from the block. Returns
// true so reloadFrontend knows a native reload was issued and skips the
// JS-based fallback. On non-darwin builds the stub returns false (the bug is
// macOS/WKWebView-specific).
func reloadWebviewNative() bool {
	C.c0wrkReloadWebview()
	return true
}
