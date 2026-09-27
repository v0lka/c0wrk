package embeddedllm

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
)

// This file covers the loopback port scan: the persisted port is a PREFERENCE
// that is re-checked before every spawn, and a taken port walks upward until a
// bindable one is found. Spawning onto a taken port is not a plain failure — the
// readiness probe would be answered by the foreign listener, so the supervisor
// would report "loaded" for a model it never started.

// recordingProber stages collisions deterministically: taken lists the ports that
// must be reported busy, and every probe is recorded so a test can assert the
// scan order and that it stopped at the first free port.
type recordingProber struct {
	taken         map[int]bool
	probed        []int
	freeByDefault bool
}

func newRecordingProber(taken ...int) *recordingProber {
	set := make(map[int]bool, len(taken))
	for _, port := range taken {
		set[port] = true
	}
	return &recordingProber{taken: set, freeByDefault: true}
}

func (p *recordingProber) probe(_ context.Context, port int) bool {
	p.probed = append(p.probed, port)
	if p.taken[port] {
		return false
	}
	return p.freeByDefault
}

func TestSearchFreePortReturnsThePreferredPortWhenItIsFree(t *testing.T) {
	t.Parallel()

	prober := newRecordingProber()
	got, err := SearchFreePort(context.Background(), 52341, prober.probe)
	if err != nil {
		t.Fatalf("SearchFreePort: %v", err)
	}
	if got != 52341 {
		t.Errorf("port = %d, want the preferred 52341", got)
	}
	if len(prober.probed) != 1 {
		t.Errorf("probed %v, want exactly one probe of the preferred port", prober.probed)
	}
}

// The requirement in one test: a taken port must not fail the load, it must walk
// upward — one port at a time — until a bindable one is found.
func TestSearchFreePortIncrementsPastTakenPorts(t *testing.T) {
	t.Parallel()

	prober := newRecordingProber(52341, 52342, 52343)
	got, err := SearchFreePort(context.Background(), 52341, prober.probe)
	if err != nil {
		t.Fatalf("SearchFreePort: %v", err)
	}
	if got != 52344 {
		t.Errorf("port = %d, want the first free one above the collisions (52344)", got)
	}
	want := []int{52341, 52342, 52343, 52344}
	if len(prober.probed) != len(want) {
		t.Fatalf("probed %v, want %v", prober.probed, want)
	}
	for i, port := range want {
		if got := prober.probed[i]; got != port {
			t.Errorf("probe %d = %d, want %d (the scan must be contiguous and upward)", i, got, port)
		}
	}
}

// The 0 sentinel means "not allocated yet" and a hand-edited config can carry a
// privileged port; both must start the scan at the lowest bindable port rather
// than fail, because the caller wants a usable port and the preference is a hint.
func TestSearchFreePortClampsAnUnusablePreference(t *testing.T) {
	t.Parallel()

	for _, preferred := range []int{0, -1, 80, MinLoopbackPort - 1} {
		prober := newRecordingProber()
		got, err := SearchFreePort(context.Background(), preferred, prober.probe)
		if err != nil {
			t.Fatalf("SearchFreePort(preferred=%d): %v", preferred, err)
		}
		if got != MinLoopbackPort {
			t.Errorf("preferred %d → port %d, want %d", preferred, got, MinLoopbackPort)
		}
		if prober.probed[0] != MinLoopbackPort {
			t.Errorf("preferred %d probed %d first, want %d", preferred, prober.probed[0], MinLoopbackPort)
		}
	}
}

// An above-range preference — the shape only a hand-edited or corrupted
// manifest.json reaches, since ReadManifest performs no range validation — must
// clamp DOWN to MaxLoopbackPort instead of leaving the scan with an empty range.
// An empty range produced zero probes and an inverted exhaustion message ("every
// port from 99999 to 65535 is taken"), which misdiagnoses a bad preference as
// port exhaustion.
func TestSearchFreePortClampsAnAboveRangePreference(t *testing.T) {
	t.Parallel()

	for _, preferred := range []int{MaxLoopbackPort + 1, 99999, 1 << 20} {
		prober := newRecordingProber()
		got, err := SearchFreePort(context.Background(), preferred, prober.probe)
		if err != nil {
			t.Fatalf("SearchFreePort(preferred=%d): %v", preferred, err)
		}
		if got != MaxLoopbackPort {
			t.Errorf("preferred %d → port %d, want %d", preferred, got, MaxLoopbackPort)
		}
		if len(prober.probed) != 1 || prober.probed[0] != MaxLoopbackPort {
			t.Errorf("preferred %d probed %v, want exactly one probe of %d",
				preferred, prober.probed, MaxLoopbackPort)
		}
	}
}

// The exhaustion message must never name an inverted range: an above-range
// preference that finds its one clamped candidate taken reports a range whose
// start is at most its end.
func TestSearchFreePortExhaustionMessageIsNeverAnInvertedRange(t *testing.T) {
	t.Parallel()

	prober := newRecordingProber(MaxLoopbackPort)
	_, err := SearchFreePort(context.Background(), 99999, prober.probe)
	if !errors.Is(err, ErrNoFreePort) {
		t.Fatalf("error = %v, want ErrNoFreePort", err)
	}
	message := err.Error()
	inverted := "from " + strconv.Itoa(99999) + " to " + strconv.Itoa(MaxLoopbackPort)
	if strings.Contains(message, inverted) {
		t.Errorf("error = %q, must not report the inverted range %q", message, inverted)
	}
	want := "from " + strconv.Itoa(MaxLoopbackPort) + " to " + strconv.Itoa(MaxLoopbackPort)
	if !strings.Contains(message, want) {
		t.Errorf("error = %q, want it to name the clamped range %q", message, want)
	}
}

// Exhaustion is a machine-state diagnosis, not a silent hang: the scan is bounded
// by MaxLoopbackPort and reports ErrNoFreePort.
func TestSearchFreePortReportsExhaustion(t *testing.T) {
	t.Parallel()

	prober := newRecordingProber(MaxLoopbackPort-1, MaxLoopbackPort)
	_, err := SearchFreePort(context.Background(), MaxLoopbackPort-1, prober.probe)
	if !errors.Is(err, ErrNoFreePort) {
		t.Fatalf("error = %v, want ErrNoFreePort", err)
	}
	if len(prober.probed) != 2 {
		t.Errorf("probed %v, want the scan to stop at MaxLoopbackPort", prober.probed)
	}

	// A single exhausted candidate reports the same diagnosis.
	single := newRecordingProber(MaxLoopbackPort)
	if _, err := SearchFreePort(context.Background(), MaxLoopbackPort, single.probe); !errors.Is(err, ErrNoFreePort) {
		t.Errorf("error = %v, want ErrNoFreePort", err)
	}
}

// A cancelled load must stop probing instead of walking the remaining range.
func TestSearchFreePortHonoursContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	prober := newRecordingProber()
	if _, err := SearchFreePort(ctx, MinLoopbackPort, prober.probe); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(prober.probed) != 0 {
		t.Errorf("probed %v after the context was cancelled, want no probes", prober.probed)
	}
}

// A nil prober selects the real bind probe, so the production default is what
// this test exercises — against an actually occupied loopback port.
func TestSearchFreePortSkipsAReallyOccupiedPort(t *testing.T) {
	// Deliberately not parallel: it occupies a real loopback port.
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp",
		net.JoinHostPort(LoopbackHost, "0"))
	if err != nil {
		t.Skipf("cannot bind a loopback port in this environment: %v", err)
	}
	defer func() { _ = listener.Close() }()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address = %T, want *net.TCPAddr", listener.Addr())
	}
	taken := addr.Port

	if loopbackPortFree(context.Background(), taken) {
		t.Errorf("loopbackPortFree(%d) = true while a listener holds it", taken)
	}

	got, err := SearchFreePort(context.Background(), taken, nil)
	if err != nil {
		t.Fatalf("SearchFreePort: %v", err)
	}
	if got == taken {
		t.Errorf("port = %d, the occupied port itself", got)
	}
	if got <= taken {
		t.Errorf("port = %d, want a port above the occupied %d (the scan moves upward)", got, taken)
	}
	if !loopbackPortFree(context.Background(), got) {
		t.Errorf("the port the scan returned (%d) is not bindable", got)
	}
}

// The bounds must agree with what backend/config validates against the persisted
// value, or a config that loads could describe a port the supervisor refuses.
func TestLoopbackPortBoundsMatchTheConfigValidation(t *testing.T) {
	t.Parallel()

	if MinLoopbackPort != 1024 || MaxLoopbackPort != 65535 {
		t.Errorf("bounds = %d..%d, want 1024..65535 (backend/config EmbeddedLLMMinPort/MaxPort)",
			MinLoopbackPort, MaxLoopbackPort)
	}
	for _, tc := range []struct {
		port int
		want bool
	}{
		{MinLoopbackPort - 1, false},
		{MinLoopbackPort, true},
		{52341, true},
		{MaxLoopbackPort, true},
		{MaxLoopbackPort + 1, false},
		{0, false},
	} {
		if got := usablePort(tc.port); got != tc.want {
			t.Errorf("usablePort(%s) = %t, want %t", strconv.Itoa(tc.port), got, tc.want)
		}
	}
}
