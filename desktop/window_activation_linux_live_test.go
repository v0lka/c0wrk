//go:build linux

package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestX11ActivateOwnWindowLive is an opt-in live-systems test: it exercises
// the REAL x11ActivateOwnWindow — discovery over _NET_CLIENT_LIST, the
// _NET_WM_WINDOW_TYPE_NORMAL filter, the pager-source activation — against
// the running window manager. A scratch xclock window is adopted by the WM
// and its _NET_WM_PID repointed at this test process (the production
// discovery then finds it); activation must make it the active window.
//
// Scratch-window plumbing uses xdotool/xprop subprocesses because Go does
// not support `import "C"` inside _test.go files.
//
// Opt-in via C0WRK_X11_ACTIVATE_LIVE=1 because it (briefly) steals focus:
// plain `go test` runs and CI (xvfb without a WM → no _NET_CLIENT_LIST →
// fail-soft false) never run it. It restores the previously active window
// afterwards.
func TestX11ActivateOwnWindowLive(t *testing.T) {
	if os.Getenv("C0WRK_X11_ACTIVATE_LIVE") != "1" {
		t.Skip("live X11 activation test; set C0WRK_X11_ACTIVATE_LIVE=1 on a desktop session")
	}
	for _, tool := range []string{"xdotool", "xprop"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed (scratch window provider)")
	}

	activeBefore := strings.TrimSpace(mustOut(t, "xdotool", "getactivewindow"))

	// Wire the production debug hook to the test log to pinpoint any failing
	// discovery step, keeping the lines so the cache assertion below can read
	// which path the activation took.
	var steps []string
	origDebug := x11ActivateDebug
	x11ActivateDebug = func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		steps = append(steps, line)
		t.Log("x11: " + line)
	}
	defer func() { x11ActivateDebug = origDebug }()

	// The cache is process-global; a previous test in this binary may have
	// filled it with a window that no longer exists.
	x11ActivationMu.Lock()
	x11TargetWindow = 0
	x11ActivationMu.Unlock()

	// Scratch window via PyGObject (GTK sets _NET_WM_PID and
	// _NET_WM_WINDOW_TYPE=NORMAL exactly like the real app does; the PID is
	// then repointed at this test process so the production discovery
	// matches). cgo is not allowed in _test.go, hence the subprocess.
	scratchPy := `
import gi, sys
gi.require_version('Gtk', '3.0')
from gi.repository import Gtk, GLib
w = Gtk.Window(title="c0wrk-live-activation-scratch")
w.set_default_size(120, 120)
w.connect("destroy", Gtk.main_quit)
w.show_all()
GLib.timeout_add(60000, Gtk.main_quit)
print(w.get_window().get_xid(), flush=True)
Gtk.main()
`
	scratchProc := exec.CommandContext(t.Context(), "python3", "-c", scratchPy)
	outPipe, err := scratchProc.StdoutPipe()
	if err != nil {
		t.Skipf("cannot pipe scratch window: %v", err)
	}
	if err := scratchProc.Start(); err != nil {
		t.Skipf("cannot start scratch window: %v", err)
	}
	defer func() {
		_ = scratchProc.Process.Kill()
		_, _ = scratchProc.Process.Wait()
	}()

	// Read the scratch XID from the subprocess.
	xidCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := outPipe.Read(buf)
		xidCh <- strings.TrimSpace(string(buf[:n]))
	}()
	var scratchXID string
	select {
	case scratchXID = <-xidCh:
	case <-time.After(5 * time.Second):
		t.Skip("scratch window did not report its XID")
	}
	if scratchXID == "" {
		t.Skip("scratch window XID empty")
	}

	// Wait for the WM to ADOPT the scratch window: it must appear in the
	// root's _NET_CLIENT_LIST (the production discovery reads exactly that
	// list). Finding it via xdotool by name is NOT enough — unmanaged
	// windows are searchable too.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list := mustOut(t, "xprop", "-root", "_NET_CLIENT_LIST")
		hex := "0x" + strconv.FormatUint(parseXID(t, scratchXID), 16)
		if strings.Contains(list, hex) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Repoint _NET_WM_PID at THIS test process so the production PID match
	// finds the scratch window (the XID stays owned by the subprocess; only
	// the discovery property is overridden for the test).
	if err := exec.CommandContext(t.Context(), "xprop", "-id", scratchXID,
		"-f", "_NET_WM_PID", "32c", "-set", "_NET_WM_PID",
		strconv.Itoa(os.Getpid())).Run(); err != nil {
		t.Skipf("cannot set _NET_WM_PID on scratch window: %v", err)
	}

	if !x11ActivateOwnWindow() {
		t.Fatal("x11ActivateOwnWindow returned false on a live X session with an owned window")
	}

	deadline = time.Now().Add(2 * time.Second)
	active := ""
	for time.Now().Before(deadline) {
		active = strings.TrimSpace(mustOut(t, "xdotool", "getactivewindow"))
		if active == scratchXID {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if active != scratchXID {
		t.Fatalf("activation did not make the owned window active: %s, want %s", active, scratchXID)
	}

	// A second activation must take the cached XID: rediscovery reads two
	// properties per managed window, and a notification click would pay that
	// on every single click.
	steps = nil
	if !x11ActivateOwnWindow() {
		t.Fatal("second x11ActivateOwnWindow returned false")
	}
	cached := false
	for _, step := range steps {
		if strings.HasPrefix(step, "activating cached window") {
			cached = true
		}
	}
	if !cached {
		t.Errorf("second activation rediscovered instead of using the cache; steps: %v", steps)
	}

	// Restore the previously focused window (courtesy).
	if activeBefore != "" && activeBefore != scratchXID {
		_ = exec.CommandContext(t.Context(), "xdotool", "windowactivate", activeBefore).Run()
	}
}

func mustOut(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), name, args...).Output()
	if err != nil {
		t.Skipf("%s %v failed: %v", name, args, err)
	}
	return string(out)
}

// parseXID parses the scratch window XID as printed by the PyGObject
// subprocess (decimal) into a uint64.
func parseXID(t *testing.T, s string) uint64 {
	t.Helper()
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		t.Skipf("bad scratch XID %q: %v", s, err)
	}
	return v
}
