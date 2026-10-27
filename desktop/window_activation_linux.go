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
// through Xlib's default handler, which exits the process; other displays are
// forwarded to the previously installed handler (GTK's). Installed exactly
// ONCE per process — re-installing per call made x11_prev_handler point at
// the swallower itself and recurse on any other display. See
// specs/domains/frontend/system-notifications.md (X connection lifetime).
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

// x11_cardinal_list reads a format-32 property; caller XFree()s the result.
// NULL for absent/mismatched properties.
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
// window — the user-visible main window and correct activation target (a
// Wails/GTK process owns several X windows carrying its _NET_WM_PID, e.g. the
// splash). window_type is the pre-interned _NET_WM_WINDOW_TYPE atom.
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
// _NET_ACTIVE_WINDOW, source indication 2 = pager — taskbar-click semantics,
// honored even against focus stealing prevention) to make it the active
// window, then takes the X input focus directly (the empirically verified
// xdotool windowactivate sequence). ts is a fresh server timestamp when one
// was obtainable, else 0. See
// specs/domains/frontend/system-notifications.md (Click → window activation).
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
	"slices"
	"sync"
	"time"
	"unsafe" //nolint:gocritic // dupImport false positive (cgo import mangling)
)

// x11ActivationMu serializes activation attempts so the shared private X
// connection (x11Conn) is single-flight; taken with TryLock, never Lock —
// queueing behind an in-flight activation would stall the caller (the D-Bus
// signal pump). See specs/domains/frontend/system-notifications.md.
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

// x11ActivateOwnWindow raises and focuses this process's own top-level window
// over a private X11 connection using EWMH pager-source activation. The Wails
// runtime's Linux WindowUnminimise (gtk_window_present) carries the
// application's LAST-interaction timestamp, which focus stealing prevention
// rejects from a non-focused app — the taskbar entry merely flashes. The own
// window is located via _NET_WM_PID over the root's _NET_CLIENT_LIST (Wails
// exposes no window-handle accessor). Fail-soft: any failure returns false
// and showWindow falls back to the Wails present() path.
// See specs/domains/frontend/system-notifications.md (Click → window activation).
func x11ActivateOwnWindow() bool {
	// TryLock, never Lock: an activation already in flight may be blocked
	// inside Xlib (the WM's server grab), and the caller is the D-Bus signal
	// pump — a skipped activation simply falls back to the Wails present()
	// path.
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
	pidAtom := C.XInternAtom(d, pidName, C.False)

	// Fast path: the window activated last time, still managed and still
	// ours — one property read vs the per-window walk on every activation.
	if cached := x11CachedTarget(d, list, pidAtom, pid); cached != 0 {
		x11Debug("activating cached window %d", uint64(cached))
		C.x11_activate(d, root, cached, x11ServerTimestamp(d, root))
		return true
	}

	windowTypeName := C.CString("_NET_WM_WINDOW_TYPE")
	defer C.free(unsafe.Pointer(windowTypeName))
	windowTypeAtom := C.XInternAtom(d, windowTypeName, C.False)

	// Two passes: prefer the EWMH NORMAL window (the user-visible main
	// window), fall back to any PID match (a WM/daemon that does not set
	// _NET_WM_WINDOW_TYPE must not lose activation entirely).
	var target, anyPID C.ulong
	for _, w := range list {
		if x11WindowPID(d, w, pidAtom) != pid {
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
	x11TargetWindow = target
	x11Debug("activating window %d", uint64(target))

	ts := x11ServerTimestamp(d, root)
	C.x11_activate(d, root, target, ts)
	return true
}

// x11TargetWindow caches the own-window XID discovered by the last successful
// activation; guarded by x11ActivationMu together with x11Conn.
var x11TargetWindow C.ulong

// x11CachedTarget returns the cached own-window XID when still valid — still
// in the CURRENT client list and still carrying this process's _NET_WM_PID
// (an XID-recycle guard) — else 0, clearing the cache.
func x11CachedTarget(d *C.Display, list []C.ulong, pidAtom C.Atom, pid C.ulong) C.ulong {
	if x11TargetWindow == 0 {
		return 0
	}
	if !slices.Contains(list, x11TargetWindow) || x11WindowPID(d, x11TargetWindow, pidAtom) != pid {
		x11Debug("cached window %d is stale; rediscovering", uint64(x11TargetWindow))
		x11TargetWindow = 0
		return 0
	}
	return x11TargetWindow
}

// x11WindowPID reads a window's _NET_WM_PID (0 when the property is absent or
// malformed — an unmanaged or non-EWMH window).
func x11WindowPID(d *C.Display, w C.ulong, pidAtom C.Atom) C.ulong {
	var n C.ulong
	raw := C.x11_cardinal_list(d, w, pidAtom, &n)
	if raw == nil {
		return 0
	}
	got := *raw
	C.XFree(unsafe.Pointer(raw))
	return got
}

// x11Conn is the process-wide private X11 activation connection, opened
// lazily on first use and deliberately NEVER closed: XCloseDisplay performs a
// synchronous round trip that a WM holding a server grab (KWin's un-minimize
// animation — triggered by the activation itself) blocks forever, which once
// stalled the godbus signal pump and killed notification clicks for the rest
// of the run. A server restart would leave the connection stale, but GDK's
// own connection dies first, so the process is gone either way. Guarded by
// x11ActivationMu. See specs/domains/frontend/system-notifications.md
// (X connection lifetime).
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
// roundtrip on a scratch window (the gdk_x11_get_server_time technique), so
// the activation request compares as newer than the user's latest
// interaction; 0 (CurrentTime) on failure, which EWMH still permits for
// pager requests.
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
