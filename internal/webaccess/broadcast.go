// SPDX-License-Identifier: Apache-2.0
//
// broadcast.go — the event fan-out: a per-client bounded outbox with a
// writer goroutine each, and the web-agnostic event tap the desktop mount
// registers on (*desktop.App).emit's eventTap seam.
//
// Backpressure contract: the fan-out NEVER blocks its caller and NEVER
// writes on it — a client that cannot keep up fills its own bounded queue
// and is dropped on overflow (Warn + abrupt close; the browser reconnects
// with backoff and re-syncs), strictly better than stalling the app's event
// path or growing memory without bound. Per-client FIFO order and payload
// bytes are preserved end-to-end; the frame is marshaled once before
// fan-out. specs/domains/web-access.md (Events).

package webaccess

import (
	"encoding/json"
)

// wsEventQueueCap bounds one client's outbound queue (frames): large enough
// to absorb a burst of coalesced events (GC pause, tab throttling), small
// enough that a dead client is dropped in bounded time.
const wsEventQueueCap = 1024

// enqueue queues one frame for the client's writer; never blocks — a full
// queue means the client cannot keep up, so it is dropped (Warn + abort).
// Returns whether the frame was queued.
func (c *WSConn) enqueue(frame []byte) bool {
	select {
	case c.outbox <- frame:
		return true
	default:
		c.srv.log.Warn("webaccess: ws client outbox overflow — dropping client (it will reconnect)",
			"queued", len(c.outbox), "cap", cap(c.outbox))
		c.abort()
		return false
	}
}

// wsWritePump drains the client's outbox, one text frame at a time. One
// pump per client: writes are serialized by construction, and a slow or
// dead peer only delays its own queue. The pump carries its own recover and
// closes the connection on any exit, reclaiming clients whose writes failed.
func (s *Server) wsWritePump(c *WSConn) {
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("webaccess: ws write pump panic", "panic", p)
		}
		c.Close()
	}()
	for {
		select {
		case frame := <-c.outbox:
			if err := c.SendText(frame); err != nil {
				return // the deferred Close reclaims the dead peer
			}
		case <-c.closing:
			return
		}
	}
}

// eventEnvelope is the wire shape of one backend event: exactly what the
// desktop webview receives from Wails EventsEmit, as JSON. args is null
// when the event carried no data; the server never batches or reshapes —
// one envelope per event, in order.
type eventEnvelope struct {
	Name string        `json:"name"`
	Args []interface{} `json:"args"`
}

// EventTap returns the tap for the desktop event seam: a func receiving
// (name, args) exactly as the app's emit sees them; the mount point assigns
// it to the app's eventTap field. The signature is deliberately generic —
// this package knows nothing about its consumers, and consumers know
// nothing about this package. Safe for concurrent use, bounded enqueue per
// live client, never panics into its caller (the desktop event path must
// be untouchable from the web side).
func (s *Server) EventTap() func(name string, args []interface{}) {
	return s.tapEvent
}

// tapEvent marshals the envelope once and fans the frame out to every live
// client. Marshal failures (an unmarshalable payload) are logged and
// dropped for the web side only — the Wails dispatch is untouched by
// construction.
func (s *Server) tapEvent(name string, args []interface{}) {
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("webaccess: event tap panic — desktop dispatch unaffected", "panic", p)
		}
	}()
	frame, err := json.Marshal(eventEnvelope{Name: name, Args: args})
	if err != nil {
		s.log.Warn("webaccess: event not deliverable to web clients",
			"event", name, "err", err)
		return
	}
	s.BroadcastText(frame)
}
