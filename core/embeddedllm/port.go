package embeddedllm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
)

// Loopback port allocation for the embedded server.
//
// The persisted port (embedded_llm.port, mirrored into manifest.json) is a
// PREFERENCE, not a reservation: nothing holds it between an install and the
// next load, and loopback is shared with every other local process — another
// app, a dev server, or a llama-server left behind by a crashed run. Spawning
// onto a taken port is worse than a plain failure: the readiness probe would
// be answered by the FOREIGN listener and the supervisor would report "loaded"
// for a model it never started, and every request would then carry the prompt
// to that unrelated process.
//
// So the port is re-checked immediately before each spawn and walks upward
// until it finds one that can actually be bound.

// Loopback port bounds. They mirror backend/config's EmbeddedLLMMinPort and
// EmbeddedLLMMaxPort, which validate() enforces on the persisted value; core
// cannot import that package (layering), so the range is repeated here as the
// authority on what the supervisor may bind.
const (
	// MinLoopbackPort is the lowest bindable port. The privileged range below
	// it is excluded: binding there would need root, which the app never has.
	MinLoopbackPort = 1024
	// MaxLoopbackPort is the highest valid TCP port.
	MaxLoopbackPort = 65535
)

// ErrNoFreePort reports that the upward scan reached MaxLoopbackPort without
// finding a bindable port. It means the loopback range is exhausted — a
// pathological machine state, not a transient collision.
var ErrNoFreePort = errors.New("no free loopback port for the embedded LLM")

// PortProber reports whether a loopback port can be bound right now. It is the
// injection seam for the scan: tests substitute it to stage a collision
// deterministically instead of occupying real ports.
type PortProber func(ctx context.Context, port int) bool

// SearchFreePort returns the first bindable loopback port at or above
// preferred, scanning upward one port at a time.
//
// A preferred OUTSIDE [MinLoopbackPort, MaxLoopbackPort] is CLAMPED into it
// rather than failing: the caller wants a usable port, and the persisted
// preference is only a hint. Clamping the low end (including the 0 "not
// allocated yet" sentinel) starts the scan at MinLoopbackPort; clamping the
// high end — the case a hand-edited or corrupted manifest.json produces, since
// ReadManifest performs no range validation — starts it at MaxLoopbackPort.
// Without the high clamp the loop below would never execute and the exhaustion
// message would name an inverted range ("every port from 99999 to 65535 is
// taken"), misdiagnosing a bad preference as port exhaustion.
//
// The scan is bounded by MaxLoopbackPort, so it always terminates — exhaustion
// is reported as ErrNoFreePort.
//
// probe nil selects the production prober. ctx bounds the whole scan and is
// checked per candidate, so a cancelled load stops probing instead of walking
// the remaining range.
//
// The result is a port that was free when it was probed, not a reservation: it
// is released before the server binds it, so a racing process can still take it
// in between. Probing as late as possible (immediately before the spawn) is
// what keeps that window small; the readiness probe is the backstop that turns
// a lost race into a reported failure instead of a false "loaded".
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

// loopbackPortFree is the production prober: bind the port on loopback and
// release it again. A successful bind is the same test llama-server itself
// applies, so it accounts for the socket options the server uses (notably
// SO_REUSEADDR, which makes a lingering TIME_WAIT socket bindable) instead of
// guessing from a dial — a dial cannot distinguish "free" from "filtered", and
// would happily connect to the very foreign listener the scan exists to avoid.
//
// A listening socket that never accepted a connection leaves no TIME_WAIT
// entry behind, so a long scan stays cheap.
func loopbackPortFree(ctx context.Context, port int) bool {
	address := net.JoinHostPort(LoopbackHost, strconv.Itoa(port))
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return false
	}
	// The port is deliberately released here rather than handed to the child:
	// llama-server binds its own socket, and there is no fd-passing path to it.
	_ = listener.Close()
	return true
}
