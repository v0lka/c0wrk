//go:build linux

package desktop

/*
#cgo pkg-config: x11

#include <X11/Xlib.h>
#include <X11/Xatom.h>
#include <stdlib.h>
#include <string.h>

// x11_swallow_display is the private activation connection whose protocol
// errors are swallowed — a best-effort activation must never abort the app
// through Xlib's default handler, which exits the process. Any other display
// is forwarded to the handler installed before ours (GTK's).
//
// The handler is installed exactly ONCE, for the lifetime of the process.
// Re-installing it per call was a latent self-reference bug: the second
// XSetErrorHandler returned x11_swallow_handler itself, so x11_prev_handler
// pointed at the swallower and any error on a display other than the private
// one recursed until the thread's stack was gone.
static Display *x11_swallow_display = NULL;
static int (*x11_prev_handler)(Display *, XErrorEvent *) = NULL;
static int x11_handler_installed = 0;

static int x11_swallow_handler(Display *d, XErrorEvent *ev) {
	(void)ev;
	if (x11_swallow_display != NULL && d == x11_swallow_display) return 0;
	if (x11_prev_handler != NULL) return x11_prev_handler(d, ev);
	return 0;
}

static void x11_install_swallow(Display *d) {
	x11_swallow_display = d;
	if (!x11_handler_installed) {
		int (*prev)(Display *, XErrorEvent *) = XSetErrorHandler(x11_swallow_handler);
		if (prev != x11_swallow_handler) x11_prev_handler = prev;
		x11_handler_installed = 1;
	}
}

// x11_property_time extracts xproperty.time from the XEvent union (cgo
// cannot address union members directly).
static unsigned long x11_property_time(XEvent *ev) {
	return (unsigned long)ev->xproperty.time;
}

// x11_cardinal_list reads a format-32 property; the caller XFree()s the
// result. Returns NULL for absent/mismatched properties.
static unsigned long *x11_cardinal_list(Display *d, Window w, Atom prop, unsigned long *nitems) {
	Atom actual = None;
	int fmt = 0;
	unsigned long after = 0;
	unsigned char *data = NULL;
	*nitems = 0;
	if (XGetWindowProperty(d, w, prop, 0, 16384, False, AnyPropertyType,
	                       &actual, &fmt, nitems, &after, &data) != Success)
		return NULL;
	if (actual == None || fmt != 32 || data == NULL || *nitems == 0) {
		if (data != NULL) XFree(data);
		*nitems = 0;
		return NULL;
	}
	return (unsigned long *)data;
}

// x11_is_normal_window reports whether w is an EWMH _NET_WM_WINDOW_TYPE_NORMAL
// window. A Wails/GTK process may own several X windows carrying its
// _NET_WM_PID (e.g. a leftover splash/utility window); only the NORMAL one
// is the user-visible main window and the correct activation target.
// window_type is the pre-interned _NET_WM_WINDOW_TYPE atom.
static int x11_is_normal_window(Display *d, Window w, Atom window_type) {
	Atom actual = None;
	int fmt = 0;
	unsigned long nitems = 0, after = 0;
	unsigned char *data = NULL;
	Atom want = XInternAtom(d, "_NET_WM_WINDOW_TYPE_NORMAL", False);
	if (XGetWindowProperty(d, w, window_type,
	                       0, 64, False, XA_ATOM, &actual, &fmt,
	                       &nitems, &after, &data) != Success)
		return 0;
	if (actual == None || fmt != 32 || data == NULL) {
		if (data != NULL) XFree(data);
		return 0;
	}
	Atom *types = (Atom *)data;
	int normal = 0;
	for (unsigned long i = 0; i < nitems; i++) {
		if (types[i] == want) { normal = 1; break; }
	}
	XFree(data);
	return normal;
}


// x11_activate maps/raises the window and asks the WM (EWMH
// _NET_ACTIVE_WINDOW, source indication 2 = pager) to make it the active
// window: raise, deiconify, focus. A pager-sourced request carries
// taskbar-click semantics and is honored by EWMH window managers (KWin,
// mutter, xfwm4, …) even against focus stealing prevention — exactly the
// case of a notification click arriving at an unfocused application.
// The direct XSetInputFocus afterwards reproduces the empirically verified
// xdotool windowactivate sequence: even a WM that still denies the EWMH
// request loses the X input focus to the window.
// ts is a fresh server timestamp when one was obtainable, else 0.
static void x11_activate(Display *d, Window root, Window w, unsigned long ts) {
	XEvent e;
	Atom net_active = XInternAtom(d, "_NET_ACTIVE_WINDOW", False);

	XMapRaised(d, w);

	memset(&e, 0, sizeof(e));
	e.xclient.type = ClientMessage;
	e.xclient.display = d;
	e.xclient.window = w;
	e.xclient.message_type = net_active;
	e.xclient.format = 32;
	// data.l[0] = source indication: 2 (pager)
	// data.l[1] = activation timestamp
	// data.l[2] = requestor's active window: none
	e.xclient.data.l[0] = 2;
	e.xclient.data.l[1] = (long)ts;
	e.xclient.data.l[2] = 0;
	e.xclient.data.l[3] = 0;
	e.xclient.data.l[4] = 0;
	XSendEvent(d, root, False,
	           SubstructureRedirectMask | SubstructureNotifyMask, &e);
	XSync(d, False);

	// Direct focus takeover (xdotool-equivalent). BadMatch (window not
	// viewable yet) is swallowed by the installed handler.
	XSetInputFocus(d, w, RevertToPointerRoot, ts);
	XRaiseWindow(d, w);
	XFlush(d);
}

*/
import "C" //nolint:gocritic // dupImport false positive: the C pseudo-package import alongside the Go imports below

import (
	"os"
	goruntime "runtime"
	"sync"
	"time"
	"unsafe" //nolint:gocritic // dupImport false positive (cgo import mangling)
)

// x11ActivationMu serializes activation attempts so the shared private X
// connection (x11Conn) is single-flight. Acquired with TryLock — see
// x11ActivateOwnWindow for why an activation must never queue.
var x11ActivationMu sync.Mutex

// x11ActivateDebug, when non-nil, receives step diagnostics from
// x11ActivateOwnWindow. Nil in production; the live test wires it to t.Logf
// to pinpoint the failing discovery step. Best-effort: never called on hot
// paths.
var x11ActivateDebug func(format string, args ...any)

// x11Debug forwards to x11ActivateDebug when wired. MUST be used for every
// diagnostics call inside the activation path: the variable is nil in
// production, and a bare x11ActivateDebug(...) call would panic the app
// (the very failure that crashed startup before this guard existed).
func x11Debug(format string, args ...any) {
	if x11ActivateDebug == nil {
		return
	}
	x11ActivateDebug(format, args...)
}

// x11ActivateOwnWindow raises and focuses this process's own top-level
// window over a private X11 connection using EWMH pager-source activation
// (see x11_activate in the preamble).
//
// It exists because the Wails runtime's Linux WindowUnminimise maps to
// gtk_window_present, which carries the timestamp of the application's LAST
// user interaction (0 when there was none since the window was created) —
// a request from a non-focused application with a stale/zero timestamp is
// exactly what focus stealing prevention (KWin's default policy) rejects,
// leaving the taskbar entry merely flashing ("demands attention").
//
// The window is located via _NET_WM_PID == os.Getpid() over the root's
// _NET_CLIENT_LIST because Wails v2 exposes no window-handle accessor.
//
// Fail-soft by design: any failure (no DISPLAY, a Wayland session, no
// matching window) returns false and showWindow falls back to the Wails
// present() path.
func x11ActivateOwnWindow() bool {
	// TryLock, never Lock: an activation already in flight may be blocked
	// inside Xlib (the window manager can hold a server grab during the very
	// un-minimize animation this activation triggers). Queueing behind it
	// would block this goroutine too — and the caller is the D-Bus signal
	// pump, whose goroutine must keep reading or every later notification
	// click is silently dropped. A skipped activation simply falls back to
	// the Wails present() path.
	if !x11ActivationMu.TryLock() {
		x11Debug("activation already in flight; skipping")
		return false
	}
	defer x11ActivationMu.Unlock()

	// Pin to one OS thread for the duration of the Xlib calls.
	goruntime.LockOSThread()
	defer goruntime.UnlockOSThread()

	d := x11Display()
	if d == nil {
		x11Debug("XOpenDisplay failed")
		return false
	}

	root := C.XDefaultRootWindow(d)

	clientListName := C.CString("_NET_CLIENT_LIST")
	defer C.free(unsafe.Pointer(clientListName))
	pidName := C.CString("_NET_WM_PID")
	defer C.free(unsafe.Pointer(pidName))

	var nitems C.ulong
	raw := C.x11_cardinal_list(d, root, C.XInternAtom(d, clientListName, C.False), &nitems)
	if raw == nil {
		x11Debug("no _NET_CLIENT_LIST on root")
		return false
	}
	// Copy before freeing: the slice must not alias the X property memory.
	list := make([]C.ulong, int(nitems))
	copy(list, unsafe.Slice((*C.ulong)(unsafe.Pointer(raw)), int(nitems)))
	C.XFree(unsafe.Pointer(raw))
	x11Debug("client list: %d windows, own pid %d", len(list), os.Getpid())

	pid := C.ulong(os.Getpid())
	windowTypeName := C.CString("_NET_WM_WINDOW_TYPE")
	defer C.free(unsafe.Pointer(windowTypeName))
	windowTypeAtom := C.XInternAtom(d, windowTypeName, C.False)

	// Two passes: prefer the EWMH NORMAL window (the user-visible main
	// window), fall back to any PID match (a WM/daemon that does not set
	// _NET_WM_WINDOW_TYPE must not lose activation entirely).
	var target, anyPID C.ulong
	for _, w := range list {
		var pn C.ulong
		praw := C.x11_cardinal_list(d, w, C.XInternAtom(d, pidName, C.False), &pn)
		if praw == nil {
			continue
		}
		wp := *praw
		C.XFree(unsafe.Pointer(praw))
		if wp != pid {
			continue
		}
		if anyPID == 0 {
			anyPID = w
		}
		if C.x11_is_normal_window(d, w, windowTypeAtom) == 1 {
			target = w
			break
		}
	}
	if target == 0 {
		target = anyPID
	}
	if target == 0 {
		x11Debug("no window with _NET_WM_PID == %d", os.Getpid())
		return false
	}
	x11Debug("activating window %d", uint64(target))

	ts := x11ServerTimestamp(d, root)
	C.x11_activate(d, root, target, ts)
	return true
}

// x11Conn is the process-wide private X11 activation connection, opened
// lazily on first use and deliberately NEVER closed.
//
// It used to be opened and closed per call. XCloseDisplay performs a
// synchronous round trip, and under KWin it was observed to block forever:
// the window manager grabs the X server for the un-minimize animation that
// our own activation request just triggered, and the close never completes.
// The blocked call held x11ActivationMu and — because the notification click
// callback runs on the godbus signal-pump goroutine — stopped the pump, after
// which godbus silently discarded every subsequent ActionInvoked signal and
// notification clicks stopped working entirely for the rest of the run.
//
// Keeping one connection removes the close from the hot path altogether.
// Cost: one X connection for the process lifetime, and a server restart would
// leave it stale — but GDK's own connection dies with the server first, so
// the process is gone either way.
//
// Guarded by x11ActivationMu; only x11ActivateOwnWindow touches it.
var x11Conn *C.Display

// x11Display returns the private activation connection, opening it on first
// use. Returns nil when no X display is reachable (Wayland, headless), which
// makes x11ActivateOwnWindow fail soft into the Wails fallback.
func x11Display() *C.Display {
	if x11Conn != nil {
		return x11Conn
	}
	d := C.XOpenDisplay(nil)
	if d == nil {
		return nil
	}
	C.x11_install_swallow(d)
	x11Conn = d
	return d
}

// x11ServerTimestamp returns a fresh server timestamp via a PropertyNotify
// roundtrip on a scratch window — the same technique as
// gdk_x11_get_server_time. Used as the activation timestamp so the request
// compares as newer than the user's latest interaction; returns 0
// (CurrentTime) on failure, which EWMH still permits for pager requests.
func x11ServerTimestamp(d *C.Display, root C.ulong) C.ulong {
	sw := C.XCreateSimpleWindow(d, root, 0, 0, 1, 1, 0, 0, 0)
	if sw == 0 {
		return 0
	}
	defer C.XDestroyWindow(d, sw)

	C.XSelectInput(d, sw, C.PropertyChangeMask)

	syncName := C.CString("_C0WRK_ACTIVATE_TS")
	defer C.free(unsafe.Pointer(syncName))
	atom := C.XInternAtom(d, syncName, C.False)

	var one C.uchar = 1
	C.XChangeProperty(d, sw, atom, atom, 8, C.PropModeAppend, &one, 1)
	C.XSync(d, C.False)

	var ev C.XEvent
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		if C.XCheckWindowEvent(d, sw, C.PropertyChangeMask, &ev) != 0 { //nolint:gocritic // dupSubExpr false positive on cgo-mangled XEvent access
			return C.x11_property_time(&ev) //nolint:gocritic // dupSubExpr false positive on cgo-mangled XEvent access
		}
		time.Sleep(time.Millisecond)
	}
	return 0
}
