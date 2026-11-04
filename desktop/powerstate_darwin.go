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

// NOTE ON API: WKNavigationDelegate exposes exactly ONE
// web-content-process-termination hook, -webViewWebContentProcessDidTerminate:
// (macOS 10.11+); there is no public -webView:webContentProcessDidTerminateWithReason:
// selector, and Wails v2.12.0 implements not even this one — a killed renderer
// leaves the window blank. This file attaches the selector to the real
// WailsContext class at runtime (see c0wrkInstallWebviewRecovery).

// Minimal WailsContext surface for this translation unit. The full class is
// implemented in the Wails module and linked into the binary ONLY by
// `wails build` (via the generated main) — never by plain `go build`/`go test`.
// Only the accessors are referenced here, and Obj-C property access compiles
// to dynamic message sends (objc_msgSend(self, "shuttingDown") / "webview"):
// that emits NO link-time reference to _OBJC_CLASS_$_WailsContext, which a
// compile-time @implementation WailsContext (Category) WOULD emit — leaving
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
// recovery, debouncing reload storms (WebKit may post the delegate callback
// several times in quick succession, and a recovery can transiently
// re-trigger). Main-thread-only access — the WKNavigationDelegate callback
// and the dispatch_async below both target the main queue, so no lock.
static NSTimeInterval c0wrkLastRecoveryTime = 0.0;

// c0wrkRecoveryDebounceSeconds is the minimum interval between recovery
// reloads. ~3s collapses a burst of terminations into a single reload.
static const NSTimeInterval c0wrkRecoveryDebounceSeconds = 3.0;

// c0wrkWebContentProcessDidTerminateIMP is the IMP attached to WailsContext as
// -webViewWebContentProcessDidTerminate:. WebKit invokes it when the
// web-content process dies (crash, memory ceiling, or an OS-initiated kill
// such as the post-sleep suspension on macOS 26); the real API carries no
// termination reason. It (a) bails when shuttingDown is set, (b) debounces
// via c0wrkLastRecoveryTime, and (c) dispatches -[WKWebView reload] to the
// main queue so the reload never runs synchronously inside the WebKit
// callback that reported the death. reload is used instead of Wails's
// WindowReloadApp — an evaluateJavaScript call cannot revive a dead process
// (no JS runtime) — and shuttingDown is re-checked inside the block in case
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
// first WKWebView (the app is single-window with one webview). The IMP can
// read ctx.webview directly, but the Go wake callback has no such handle — it
// locates the webview through the window hierarchy at wake time.
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

// c0wrkReloadWebview forces a native -[WKWebView reload] on the main thread —
// the wake-path counterpart of the termination-hook recovery. Wails's
// WindowReloadApp (evaluateJavaScript) is the wrong primitive: it cannot run
// until the suspended web-content process resumes, and re-assigning the same
// URL is a no-op in WebKit. This machine's sleep suspends rather than kills
// the process, so the termination hook does NOT fire on wake and this native
// reload is the only reliable recovery; the outcome is logged from the
// dispatched block, never silent. Declared static (like the other helpers
// here): cgo's //export path compiles the preamble into multiple object
// files, and a non-static definition would collide.
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
// hook to the real WailsContext class at runtime. Returns 0 = installed (the
// normal case under Wails v2.12.0), 1 = class not found (a future Wails
// rename; the wake path remains as best-effort fallback), 2 = WailsContext
// already implements the selector (its implementation is left in place).
//
// Called from registerPowerWakeObserver during Startup, by which point Wails
// has loaded WailsContext and installed it as the webview's navigation
// delegate. class_addMethod mutates the method table consulted at every
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

// c0wrkRegisterWakeObserver registers NSWorkspace observers for system wake
// (NSWorkspaceDidWakeNotification) and display wake
// (NSWorkspaceScreensDidWakeNotification, macOS 10.15+). The block runs on
// the posting thread — the main thread for NSWorkspace power notifications —
// and forwards to Go via c0wrkOnPowerWake; the observer is retained by the
// notification center for the app's lifetime.
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

// powerWakeHandler holds the Go-side callback invoked on a power-state wake:
// set in Startup before the observer registers, read from the cgo-invoked
// c0wrkOnPowerWake on the main thread — hence the atomic.
var powerWakeHandler atomic.Pointer[func()]

// recoveryLogger is the logger behind c0wrkLogWebviewRecovery, set by
// registerPowerWakeObserver: the Obj-C IMP is a cgo callback that cannot
// receive an injected logger. Atomic — the callback fires on the main thread.
var recoveryLogger atomic.Pointer[slog.Logger]

// registerPowerWakeObserver arms the macOS wake observers (invoking
// onPowerWake per wake), the recovery logger, and the
// web-content-process-termination hook injected into WailsContext. No-op on
// non-darwin builds — see powerstate_other.go.
func registerPowerWakeObserver(log *slog.Logger, onPowerWake func()) {
	handler := onPowerWake
	powerWakeHandler.Store(&handler)
	recoveryLogger.Store(log)

	// Runtime method injection rather than a compile-time Obj-C category, so
	// the package links under the standard Go toolchain (a category would
	// emit an unresolved _OBJC_CLASS_$_WailsContext). See
	// specs/decisions/018-macos-webview-recovery.md.
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

// reloadWebviewNative issues a NATIVE -[WKWebView reload] (dispatched on the
// main queue inside the C helper, which returns immediately; the result is
// logged from the block) — the only primitive that revives a suspended/blank
// webview, see specs/decisions/018-macos-webview-recovery.md. Returns true so
// reloadFrontend skips the JS fallback; the non-darwin stub returns false.
func reloadWebviewNative() bool {
	C.c0wrkReloadWebview()
	return true
}
