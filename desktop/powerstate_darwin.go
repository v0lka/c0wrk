//go:build darwin

package desktop

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation -framework AppKit

#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>

// c0wrkOnPowerWake is implemented in Go. It is invoked from the Objective-C
// observer block below whenever the system or the displays wake from sleep.
extern void c0wrkOnPowerWake(void);

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

// registerPowerWakeObserver registers native macOS observers that fire when
// the system or the displays wake from sleep, invoking onPowerWake on each
// wake. No-op on non-darwin builds — see powerstate_other.go.
func registerPowerWakeObserver(log *slog.Logger, onPowerWake func()) {
	handler := onPowerWake
	powerWakeHandler.Store(&handler)
	C.c0wrkRegisterWakeObserver()
	log.Info("registered macOS power-state wake observer")
}

//export c0wrkOnPowerWake
func c0wrkOnPowerWake() {
	if h := powerWakeHandler.Load(); h != nil && *h != nil {
		(*h)()
	}
}
