// SPDX-License-Identifier: Apache-2.0
//
// ws.go — the /ws event channel: RFC6455 handshake and a minimal frame
// codec, hand-rolled on the stdlib (the sidecar's dependency contract bans
// a websocket library). Admission is layered: requireAuth (session cookie)
// runs before this handler; the same-origin Origin check runs here —
// browsers always send Origin on a WebSocket handshake, and a mismatched or
// missing one is a cross-site hijack attempt (cookie auth alone would ride
// along on any page's behalf). Scope: text frames in and out,
// ping/pong/close handled, control frames interleaved with fragmented
// messages, fragment total capped, no compression. Dead-peer reclaim
// (keepalive ping + read-pump liveness deadline — IdleTimeout does not
// cover hijacked conns) is documented at wsPingLoop/wsReadPump.

package webaccess

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// wsGUID is the RFC6455 handshake GUID.
	wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	// wsMaxMessageSize caps a single (possibly fragmented) inbound message.
	wsMaxMessageSize = maxBodyBytes
	// wsMaxControlPayload is the RFC6455 §5.5 ceiling for one control
	// frame's payload; a larger control frame is a protocol violation.
	wsMaxControlPayload = 125
	// wsWriteTimeout bounds a single frame write so one stuck client cannot
	// stall broadcasts to every other client.
	wsWriteTimeout = 10 * time.Second
	// wsHandshakeTimeout bounds reading the (already-arrived) handshake bytes.
	wsHandshakeTimeout = 10 * time.Second
	// wsPingInterval is the server keepalive cadence: every connected client
	// is pinged this often, and its pong (or any other frame) re-arms the
	// read pump's liveness deadline.
	wsPingInterval = 30 * time.Second
	// wsReadIdleTimeout is that liveness window: how long the read pump
	// waits for ANY frame before declaring the peer dead. Deliberately more
	// than two ping intervals — one lost ping/pong exchange must not tear
	// down a live-but-idle tab — while a dead peer is reclaimed in bounded
	// time.
	wsReadIdleTimeout = 75 * time.Second
	// maxWSConns caps simultaneous websocket connections: each carries three
	// goroutines and an outbox, so a leaked tab fan-out must stay bounded.
	// The cap is a slot RESERVATION taken before the hijack (handleWS): an
	// HTTP error can no longer be written once hijacked, so an over-cap
	// handshake is refused while the writer is alive. Authenticated tabs
	// beyond the cap get 503 and reconnect with backoff.
	maxWSConns = 16
	// hostResolveBudget bounds one DNS lookup on the wildcard-bind Host gate
	// (hostAllowed → nameServes): a wedged resolver must not stall the
	// handshake beyond this.
	hostResolveBudget = 1500 * time.Millisecond
	// hostCachePositiveTTL / hostCacheNegativeTTL are the cache windows for
	// resolved hostnames: positives briefly (interface addresses change),
	// negatives very briefly (the rebinding window must not be held open by a
	// stale "not served" answer either — the fail-closed default re-queries).
	hostCachePositiveTTL = 30 * time.Second
	hostCacheNegativeTTL = 5 * time.Second
	// hostCacheMax bounds the hostname cache: on overflow it is dropped
	// wholesale — it is a performance shim, never authority.
	hostCacheMax = 256
)

// WebSocket opcodes (RFC6455 §5.2).
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// Close codes used by this server.
const (
	closeNormal      = 1000
	closeGoingAway   = 1001
	closeProtocolErr = 1002
	closeTooBig      = 1009
)

// ws errors (connection-fatal by definition; the read loop closes on them).
var (
	errWSProtocol = errors.New("websocket protocol violation")
	errWSTooBig   = errors.New("websocket message too large")
)

// WSConn is one accepted websocket connection. Safe for concurrent use.
type WSConn struct {
	srv  *Server
	conn net.Conn
	br   *bufio.Reader
	bw   *bufio.Writer

	// outbox is the per-client bounded outbound queue (broadcast.go): the
	// fan-out never blocks on a slow client — overflow aborts the client
	// instead (it reconnects with backoff).
	outbox chan []byte
	// closing is closed exactly once when the connection is torn down; it
	// wakes the write pump so it never drains a dead queue.
	closing chan struct{}

	writeMu sync.Mutex
	closed  sync.Once
}

// handleWS upgrades an authenticated, same-origin request to a websocket and
// hands the connection to the fan-out registry. The HTTP handler returns
// after the pump goroutine takes over (the hijacked conn outlives it).
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeJSONError(w, http.StatusForbidden, "cross-origin websocket connections are rejected")
		return
	}
	key := wsHandshakeKey(r)
	if key == "" {
		writeJSONError(w, http.StatusBadRequest, "websocket upgrade required")
		return
	}
	// RFC6455 §4.2.2: only version 13 is spoken here; a mismatched request is
	// refused with the supported version named back. Checked after the
	// upgrade-header gate so a non-upgrade request keeps its 400.
	if v := r.Header.Get("Sec-WebSocket-Version"); v != "13" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		writeJSONError(w, http.StatusUpgradeRequired, "unsupported websocket version")
		return
	}

	// Capacity must be reserved while the ResponseWriter is still alive:
	// after Hijack an HTTP status can no longer be written. The reservation
	// converts into the registration below; every failure path in between
	// releases it.
	if !s.tryReserveWSSlot() {
		writeJSONError(w, http.StatusServiceUnavailable, "too many websocket connections")
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		s.releaseWSSlot()
		writeJSONError(w, http.StatusInternalServerError, "connection cannot be upgraded")
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		s.releaseWSSlot()
		writeJSONError(w, http.StatusInternalServerError, "connection cannot be upgraded")
		return
	}
	// From here on the raw conn is ours; every exit path closes it.
	c := &WSConn{
		srv:     s,
		conn:    conn,
		br:      brw.Reader,
		bw:      brw.Writer,
		outbox:  make(chan []byte, wsEventQueueCap),
		closing: make(chan struct{}),
	}
	// Register BEFORE the 101 goes on the wire. A client that has observed
	// the 101 may broadcast immediately (the shim subscribes on socket
	// open), and a hub snapshot taken before registration would legally drop
	// that frame under at-most-once semantics — a delivery hole with no
	// client-side remedy. Registration strictly precedes the handshake
	// write, so any broadcast triggered by the 101 snapshots the connection;
	// frames landing before pump start queue in the outbox and the write
	// pump drains them right after, keeping 101-first wire ordering intact.
	s.registerWS(c)
	if err := wsWriteHandshake(conn, key); err != nil {
		c.Close() //nolint:errcheck // nothing left to report to a dead peer
		return
	}
	// The handshake deadline must not leak into the live connection.
	conn.SetDeadline(time.Time{}) //nolint:errcheck // best-effort bound
	go s.wsWritePump(c)
	go s.wsReadPump(c)
	go s.wsPingLoop(c)
}

// wsHandshakeKey validates the upgrade headers and returns the client key,
// or "" when this is not a websocket upgrade.
func wsHandshakeKey(r *http.Request) string {
	if !tokenListContains(r.Header, "Connection", "upgrade") ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return ""
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return ""
	}
	return key
}

// wsWriteHandshake writes the 101 response with the derived accept value.
func wsWriteHandshake(conn net.Conn, key string) error {
	sum := sha1.Sum([]byte(key + wsGUID)) //nolint:gosec // SHA-1 is mandated by RFC6455 for the handshake
	accept := base64.StdEncoding.EncodeToString(sum[:])
	conn.SetDeadline(time.Now().Add(wsHandshakeTimeout)) //nolint:errcheck // best-effort bound
	_, err := fmt.Fprintf(conn,
		"HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n\r\n", accept)
	return err
}

// tokenListContains reports whether the named header's comma-separated token
// list contains want (case-insensitive), per RFC 7230 list-of-tokens.
func tokenListContains(h http.Header, name, want string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), want) {
				return true
			}
		}
	}
	return false
}

// sameOrigin reports whether the request's Origin header (mandatory on
// browser websocket handshakes) matches the request host exactly. Missing
// Origin is a non-browser client; rejected because cookie credentials make
// every origin potentially hostile. The Host is additionally checked
// against the configured bind (hostAllowed): an Origin==Host pair on a
// rebound DNS name would otherwise pass the equality — defense-in-depth on
// top of the host-scoped SameSite=Strict cookie (a rebinding attacker
// without the cookie is already rejected by requireAuth).
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	if !s.hostAllowed(r.Host) {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return canonicalOrigin(origin) == canonicalOrigin(scheme+"://"+r.Host)
}

// canonicalOrigin reduces an origin to scheme://host, carrying the port
// only when it is not the scheme's default. Browsers serialize Origin
// WITHOUT the default port (:80/:443) while a typed Host header can retain
// it; without this the explicit-default-port spelling of the SAME origin
// would 403 the operator's own browser on login and the WS handshake.
// Malformed input is returned unchanged and simply never matches
// (fail-closed).
func canonicalOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	host := u.Hostname()
	if host == "" {
		return raw
	}
	port := u.Port()
	if port == "" || (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		return u.Scheme + "://" + host
	}
	return u.Scheme + "://" + net.JoinHostPort(host, port)
}

// hostAllowed reports whether a Host header value (host or host:port) names
// an address this server actually serves — the DNS-rebinding backstop
// behind sameOrigin. A NON-wildcard bind keeps the exact-match rule: the
// configured bind, "localhost", or a loopback IP literal — no lookups. A
// WILDCARD bind ("0.0.0.0"/"::") names every interface and once accepted
// ANY Host — an Origin==Host pair on a rebound DNS name sailed through the
// equality; now the Host must name a genuinely served address: a loopback
// IP literal or "localhost", an IP among the serving addresses snapshotted
// at Start, or a hostname that RESOLVES (bounded, cached) into one of them
// — the legitimate mDNS way in ("my-mac.local"); a rebound name resolves
// elsewhere and fails. The cookie's host scope stays the primary gate this
// check cannot cover; this closes the spoofable-equality hole.
func (s *Server) hostAllowed(hostHeader string) bool {
	if s.cfg.Bind != "0.0.0.0" && s.cfg.Bind != "::" {
		host := hostOnly(hostHeader)
		if strings.EqualFold(host, s.cfg.Bind) || strings.EqualFold(host, "localhost") {
			return true
		}
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	}
	host := hostOnly(hostHeader)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || s.ipServed(ip)
	}
	return s.nameServes(host)
}

// hostOnly strips the port from a Host header value, returning the host
// part (or the whole value when no port split applies).
func hostOnly(hostHeader string) string {
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		return h
	}
	return hostHeader
}

// ipServed reports whether ip is among the serving addresses snapshotted at
// Start. An empty snapshot (handler used without Start) serves nothing —
// fail-closed: only the loopback/localhost structural cases pass.
func (s *Server) ipServed(ip net.IP) bool {
	s.mu.Lock()
	hosts := s.servingHosts
	s.mu.Unlock()
	for _, h := range hosts {
		if h.Equal(ip) {
			return true
		}
	}
	return false
}

// nameServes reports whether hostname resolves into a serving address, with
// a bounded TTL cache: the resolve is a network round trip on the
// same-origin gate, so its result is remembered — positives briefly (an
// interface address can change), negatives very briefly (the rebinding
// window must not be propped open). The cache is a performance shim, never
// authority: dropped wholesale at hostCacheMax, no eviction ordering.
func (s *Server) nameServes(hostname string) bool {
	key := strings.ToLower(hostname)
	now := time.Now()
	s.dnsMu.Lock()
	if e, ok := s.dnsCache[key]; ok && now.Before(e.expires) {
		s.dnsMu.Unlock()
		return e.allowed
	}
	s.dnsMu.Unlock()

	allowed := s.resolveNameServes(hostname)

	ttl := hostCacheNegativeTTL
	if allowed {
		ttl = hostCachePositiveTTL
	}
	s.dnsMu.Lock()
	if len(s.dnsCache) >= hostCacheMax {
		s.dnsCache = make(map[string]dnsCacheEntry, hostCacheMax/2)
	}
	s.dnsCache[key] = dnsCacheEntry{allowed: allowed, expires: now.Add(ttl)}
	s.dnsMu.Unlock()
	return allowed
}

// resolveNameServes performs the bounded DNS lookup behind nameServes. A
// lookup error, a timeout, or an empty answer all read as "not served"
// (fail-closed) — a resolver hiccup must not open the wildcard gate.
func (s *Server) resolveNameServes(hostname string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), hostResolveBudget)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, hostname)
	if err != nil {
		return false
	}
	for _, ia := range ips {
		if s.ipServed(ia.IP) {
			return true
		}
	}
	return false
}

// servingHostSnapshot collects the unicast addresses a wildcard listener
// answers on: every interface address minus multicast (a wildcard TCP bind
// serves all of them — loopback included, which hostAllowed also accepts
// structurally, so its presence here is harmless redundancy). An error
// yields nil, and ipServed treats nil as "serves nothing" — fail-closed.
func servingHostSnapshot() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var ips []net.IP
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsMulticast() {
			continue
		}
		ips = append(ips, ipnet.IP)
	}
	return ips
}

// dnsCacheEntry is one cached hostname→served verdict (nameServes).
type dnsCacheEntry struct {
	allowed bool
	expires time.Time
}

// registry ---------------------------------------------------------------------

func (s *Server) registerWS(c *WSConn) {
	s.wsMu.Lock()
	s.wsReserved--
	s.wsConns[c] = struct{}{}
	s.wsMu.Unlock()
}

func (s *Server) unregisterWS(c *WSConn) {
	s.wsMu.Lock()
	delete(s.wsConns, c)
	s.wsMu.Unlock()
}

// tryReserveWSSlot claims one of the maxWSConns capacity slots for an
// in-flight handshake. The count includes reservations, so a burst of
// parallel upgrades cannot each observe "one slot left" and all proceed.
func (s *Server) tryReserveWSSlot() bool {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	if len(s.wsConns)+s.wsReserved >= maxWSConns {
		return false
	}
	s.wsReserved++
	return true
}

// releaseWSSlot returns a reservation that will not become a registration
// (a pre-hijack failure path). Registering connections release their slot
// through registerWS→unregisterWS instead.
func (s *Server) releaseWSSlot() {
	s.wsMu.Lock()
	s.wsReserved--
	s.wsMu.Unlock()
}

func (s *Server) closeAllWS() {
	s.wsMu.Lock()
	conns := make([]*WSConn, 0, len(s.wsConns))
	for c := range s.wsConns {
		conns = append(conns, c)
	}
	s.wsConns = make(map[*WSConn]struct{})
	s.wsMu.Unlock()
	for _, c := range conns {
		// Stop-path teardown aborts instead of Close: the close frame write
		// carries a 10s deadline per client, so a wedged peer would stall
		// shutdown tab-by-tab after the stop budget already expired. The
		// browser reconnects with backoff.
		c.abort()
	}
}

// BroadcastText fans one text frame out to every connected browser via the
// per-client bounded outboxes — never written on the caller's goroutine
// (the backpressure contract lives in broadcast.go).
func (s *Server) BroadcastText(data []byte) {
	for _, c := range s.liveConns() {
		c.enqueue(data)
	}
}

// BroadcastJSON marshals v and broadcasts it as one text frame.
func (s *Server) BroadcastJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		s.log.Error("webaccess: ws broadcast marshal failed", "err", err)
		return
	}
	s.BroadcastText(data)
}

// WSConn methods -----------------------------------------------------------------

// SendText writes one unmasked server text frame.
func (c *WSConn) SendText(data []byte) error { return c.writeFrame(opText, data) }

// liveConns snapshots the currently registered connections.
func (s *Server) liveConns() []*WSConn {
	s.wsMu.Lock()
	conns := make([]*WSConn, 0, len(s.wsConns))
	for c := range s.wsConns {
		conns = append(conns, c)
	}
	s.wsMu.Unlock()
	return conns
}

// Close tears the connection down (idempotent) and unregisters it; the
// best-effort close frame keeps graceful per-client paths clean (bounded by
// wsWriteTimeout). The server stop path uses abort — shutdown never waits
// on a peer-bound write.
func (c *WSConn) Close() {
	c.closed.Do(func() {
		c.srv.unregisterWS(c)
		close(c.closing) // wake the write pump before the socket goes away
		_ = c.writeFrame(opClose, wsClosePayload(closeNormal))
		_ = c.conn.Close()
	})
}

// abort tears the connection down WITHOUT the close frame — the abrupt
// variant for a backpressure drop. It never writes (the write path may be
// wedged), so it never blocks the producer goroutine; the client sees the
// socket die and reconnects.
func (c *WSConn) abort() {
	c.closed.Do(func() {
		c.srv.unregisterWS(c)
		close(c.closing)
		_ = c.conn.Close()
	})
}

// wsClosePayload builds a close-frame body from a code.
func wsClosePayload(code int) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, uint16(code))
	return b
}

// writeFrame serializes one frame (server frames are never masked).
func (c *WSConn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout)) //nolint:errcheck // deadline is best-effort
	defer c.conn.SetWriteDeadline(time.Time{})              //nolint:errcheck // same

	var header [10]byte
	header[0] = 0x80 | opcode // FIN set: our payloads are always single-frame
	pos := 2
	switch plen := len(payload); {
	case plen < 126:
		header[1] = byte(plen)
	case plen <= 0xFFFF:
		header[1] = 126
		binary.BigEndian.PutUint16(header[2:], uint16(plen))
		pos = 4
	default:
		header[1] = 127
		binary.BigEndian.PutUint64(header[2:], uint64(plen))
		pos = 10
	}
	if _, err := c.bw.Write(header[:pos]); err != nil {
		return err
	}
	if _, err := c.bw.Write(payload); err != nil {
		return err
	}
	return c.bw.Flush()
}

// wsReadPump drains frames until the peer goes away, delivering every
// message to the inbound callback (if wired); the goroutine carries its own
// recover. A liveness deadline is armed around every frame read and
// re-armed by every successfully read frame (any traffic counts), so a peer
// silent past wsReadIdleTimeout is reclaimed.
func (s *Server) wsReadPump(c *WSConn) {
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("webaccess: ws read pump panic", "panic", p)
		}
		c.Close()
	}()
	for {
		// Liveness bound, not a rate limit: an idle-but-alive tab answers
		// the keepalive ping (wsPingLoop) well inside the window and stays.
		if err := c.conn.SetReadDeadline(time.Now().Add(wsReadIdleTimeout)); err != nil {
			return
		}
		opcode, payload, err := c.readMessage()
		if err != nil {
			return
		}
		// readMessage answers interleaved control frames (ping/pong/close)
		// internally, so only data messages reach this switch.
		switch opcode {
		case opText, opBinary:
			if s.wsInbound != nil {
				s.wsInbound(c, payload)
			}
		}
	}
}

// wsPingLoop sends a protocol-level ping every wsPingInterval — the
// server-side half of the keepalive pair (the peer's pong re-arms the read
// pump's liveness deadline). A failed ping write means the peer is gone:
// abort (no graceful close to a wedged socket) and exit.
func (s *Server) wsPingLoop(c *WSConn) {
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.closing:
			return
		case <-ticker.C:
			if err := c.writeFrame(opPing, nil); err != nil {
				c.abort()
				return
			}
		}
	}
}

// readMessage assembles one complete message, answering interleaved control
// frames as they arrive.
func (c *WSConn) readMessage() (opcode byte, payload []byte, err error) {
	var (
		assembled  []byte
		firstOp    byte
		inFragment bool
	)
	for {
		var fin bool
		var op byte
		var data []byte
		fin, op, data, err = c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case opPing:
			if err := c.writeFrame(opPong, data); err != nil {
				return 0, nil, err
			}
		case opPong:
			// keepalive replies are absorbed silently
		case opClose:
			_ = c.writeFrame(opClose, data)
			return 0, nil, io.EOF
		case opText, opBinary:
			if inFragment {
				return 0, nil, errWSProtocol
			}
			if fin {
				return op, data, nil // the common case: single unfragmented frame
			}
			firstOp, inFragment = op, true
			assembled = append(assembled, data...)
			if len(assembled) > wsMaxMessageSize {
				return 0, nil, errWSTooBig
			}
		case opContinuation:
			if !inFragment {
				return 0, nil, errWSProtocol
			}
			assembled = append(assembled, data...)
			if len(assembled) > wsMaxMessageSize {
				return 0, nil, errWSTooBig
			}
			if fin {
				return firstOp, assembled, nil
			}
		default:
			return 0, nil, errWSProtocol
		}
	}
}

// readFrame reads exactly one frame, unmasks it, and enforces the protocol
// rules this server cares about: no reserved bits (no extensions), client
// frames must be masked, lengths within cap.
func (c *WSConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(c.br, hdr[:]); err != nil {
		return false, 0, nil, err
	}
	fin = hdr[0]&0x80 != 0
	if hdr[0]&0x70 != 0 {
		return false, 0, nil, errWSProtocol // RSV bits: no extensions negotiated
	}
	opcode = hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
	if !masked {
		// Clients MUST mask (RFC6455 §5.1); an unmasked client frame is a
		// smuggling attempt.
		return false, 0, nil, errWSProtocol
	}
	length := uint64(hdr[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if opcode&0x8 != 0 && length > wsMaxControlPayload {
		return false, 0, nil, errWSProtocol
	}
	if length > wsMaxMessageSize {
		return false, 0, nil, errWSTooBig
	}
	var mask [4]byte
	if _, err = io.ReadFull(c.br, mask[:]); err != nil {
		return false, 0, nil, err
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return fin, opcode, payload, nil
}
