package embeddedllm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
)

// Loopback port allocation for the embedded server. The persisted port is a
// PREFERENCE, not a reservation — nothing holds it between install and load,
// and loopback is shared with every other local process. Spawning onto a
// taken port is worse than a plain failure: the FOREIGN listener would answer
// the readiness probe, the supervisor would report "loaded" for a model it
// never started. So the port is re-checked immediately before each spawn and
// walks upward until one can actually be bound.
// See specs/domains/embedded-llm.md (Port allocation).

// Loopback port bounds. They mirror backend/config's EmbeddedLLMMinPort and
// EmbeddedLLMMaxPort, which validate() enforces on the persisted value; core
// cannot import that package (layering), so the range is repeated here.
const (
	// MinLoopbackPort is the lowest bindable port: below it needs root, which
	// the app never has.
	MinLoopbackPort = 1024
	// MaxLoopbackPort is the highest valid TCP port.
	MaxLoopbackPort = 65535
)

// ErrNoFreePort reports that the upward scan reached MaxLoopbackPort without
// finding a bindable port — a pathological machine state, not a transient
// collision.
var ErrNoFreePort = errors.New("no free loopback port for the embedded LLM")

// PortProber reports whether a loopback port can be bound right now — the
// injection seam tests substitute to stage a collision deterministically
// instead of occupying real ports.
type PortProber func(ctx context.Context, port int) bool

// SearchFreePort returns the first bindable loopback port at or above
// preferred, scanning upward. A preferred OUTSIDE [MinLoopbackPort,
// MaxLoopbackPort] is CLAMPED in, not failed — the persisted preference is
// only a hint: the low clamp covers the 0 "not allocated yet" sentinel, the
// high clamp covers the hand-edited manifest.json ReadManifest does not
// range-validate (without it the loop never runs and exhaustion names an
// inverted range). Bounded, so it terminates with ErrNoFreePort; nil probe
// selects the production prober; ctx is checked per candidate.
//
// The result was free when probed, not reserved — it is released before the
// server binds, so a racing process can take it; probing as late as possible
// keeps that window small, and the readiness probe is the backstop against a
// false "loaded".
func SearchFreePort(ctx context.Context, preferred int, probe PortProber) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if probe == nil {
		probe = loopbackPortFree
	}

	start := min(max(preferred, MinLoopbackPort), MaxLoopbackPort)
	for port := start; port <= MaxLoopbackPort; port++ {
		if err := ctx.Err(); err != nil {
			return 0, fmt.Errorf("embeddedllm: searching for a free loopback port: %w", err)
		}
		if probe(ctx, port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("%w: every port from %d to %d is taken",
		ErrNoFreePort, start, MaxLoopbackPort)
}

// loopbackPortFree is the production prober: bind and release — the same test
// llama-server applies, so a lingering TIME_WAIT socket is judged as the
// server (SO_REUSEADDR) would judge it, where a dial cannot tell "free" from
// "filtered". A listening socket that never accepted leaves no TIME_WAIT, so
// long scans stay cheap; the release (no fd-passing to the child) is
// deliberate.
func loopbackPortFree(ctx context.Context, port int) bool {
	address := net.JoinHostPort(LoopbackHost, strconv.Itoa(port))
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}
