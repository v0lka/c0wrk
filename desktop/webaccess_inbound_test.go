package desktop

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/session"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// webInboundFrame builds the {name, args} envelope a browser tab sends over
// the sidecar websocket (the symmetric twin of the outbound tap envelope).
func webInboundFrame(t *testing.T, name string, args ...any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"name": name, "args": args})
	if err != nil {
		t.Fatalf("marshal inbound frame: %v", err)
	}
	return body
}

// dispatch waits for the judge goroutine when the frame can spawn one and
// otherwise returns immediately.
func dispatchInbound(a *App, data []byte, uiEmit func(session.Event), log *slog.Logger) {
	a.dispatchWebInboundFrame(data, uiEmit, log)
	a.judgeWG.Wait()
}

// --- resolution: every whitelisted event reaches its pending channel ---

func TestWebInboundDispatch_ToolConfirmResolves(t *testing.T) {
	a := &App{}
	ch := make(chan sdktools.ConfirmationResponse, 1)
	a.pendingConfirmations.Store("rid-1", &pendingConfirmData{ch: ch, toolName: "bash_exec", sessionID: "sess-1"})

	dispatchInbound(a, webInboundFrame(t, "tool_confirm_response",
		map[string]any{"confirm_id": "rid-1", "decision": "deny_and_stop"}), silentUIEmit(), silentLogger())

	select {
	case got := <-ch:
		if got != sdktools.ConfirmDenyAndStop {
			t.Errorf("got %v, want ConfirmDenyAndStop", got)
		}
	default:
		t.Fatal("no decision delivered — the frame did not reach the handler")
	}
	if _, ok := a.pendingConfirmations.Load("rid-1"); ok {
		t.Error("pending confirmation entry not cleaned up")
	}
}

func TestWebInboundDispatch_ToolConfirmSecondFrameFirstResponderWins(t *testing.T) {
	a := &App{}
	ch := make(chan sdktools.ConfirmationResponse, 1)
	a.pendingConfirmations.Store("rid-1", &pendingConfirmData{ch: ch, toolName: "bash_exec", sessionID: "sess-1"})
	frame := webInboundFrame(t, "tool_confirm_response",
		map[string]any{"confirm_id": "rid-1", "decision": "allow_once"})

	dispatchInbound(a, frame, silentUIEmit(), silentLogger())
	// The losing second responder must not deliver a second decision.
	dispatchInbound(a, frame, silentUIEmit(), silentLogger())

	select {
	case got := <-ch:
		if got != sdktools.ConfirmAllowOnce {
			t.Errorf("got %v, want exactly one ConfirmAllowOnce", got)
		}
	default:
		t.Fatal("no decision delivered")
	}
	select {
	case got := <-ch:
		t.Errorf("second frame delivered a duplicate decision: %v", got)
	default:
	}
}

func TestWebInboundDispatch_AskUserResolves(t *testing.T) {
	a := &App{}
	ch := make(chan coretools.AskUserResponse, 1)
	a.pendingAskUser.Store("req-1", &pendingAskUserEntry{ch: ch, sessionID: "sess-1"})

	dispatchInbound(a, webInboundFrame(t, "ask_user_response",
		map[string]any{"request_id": "req-1", "answers": []any{
			map[string]any{"id": "q1", "selected": []any{"opt-a"}, "custom_text": "note"},
		}}), silentUIEmit(), silentLogger())

	select {
	case got := <-ch:
		if len(got.Answers) != 1 || got.Answers[0].ID != "q1" {
			t.Fatalf("got %+v, want one answer for q1", got)
		}
		if len(got.Answers[0].Selected) != 1 || got.Answers[0].Selected[0] != "opt-a" {
			t.Errorf("selected = %v, want [opt-a]", got.Answers[0].Selected)
		}
		if got.Answers[0].CustomText != "note" {
			t.Errorf("custom_text = %q, want note", got.Answers[0].CustomText)
		}
	default:
		t.Fatal("no answers delivered")
	}
	if _, ok := a.pendingAskUser.Load("req-1"); ok {
		t.Error("pending ask_user entry not cleaned up")
	}
}

func TestWebInboundDispatch_StepLimitResolves(t *testing.T) {
	a := &App{}
	ch := make(chan agent.StepLimitResponse, 1)
	a.pendingStepLimit.Store("req-1", &pendingStepLimitEntry{ch: ch, sessionID: "sess-1"})

	dispatchInbound(a, webInboundFrame(t, "step_limit_response",
		map[string]any{"request_id": "req-1", "response": "allow_more"}), silentUIEmit(), silentLogger())

	select {
	case got := <-ch:
		if got != agent.StepLimitAllowMore {
			t.Errorf("got %q, want StepLimitAllowMore", got)
		}
	default:
		t.Fatal("no step-limit decision delivered")
	}
	if _, ok := a.pendingStepLimit.Load("req-1"); ok {
		t.Error("pending step_limit entry not cleaned up")
	}
}

func TestWebInboundDispatch_PlanApprovalResolves(t *testing.T) {
	a := &App{}
	ch := make(chan planApprovalResponse, 1)
	a.pendingPlanApprovals.Store("req-1", &pendingPlanApprovalEntry{ch: ch, sessionID: "sess-1"})

	dispatchInbound(a, webInboundFrame(t, "plan_approval_response",
		map[string]any{"request_id": "req-1", "decision": "approve", "feedback": "ship it"}), silentUIEmit(), silentLogger())

	select {
	case got := <-ch:
		if got.Decision != "approve" || got.Feedback != "ship it" {
			t.Errorf("got %+v, want {approve ship it}", got)
		}
	default:
		t.Fatal("no plan-approval decision delivered")
	}
	if _, ok := a.pendingPlanApprovals.Load("req-1"); ok {
		t.Error("pending plan approval entry not cleaned up")
	}
}

func TestWebInboundDispatch_PlanApprovalRejectsUnknownDecision(t *testing.T) {
	a := &App{}
	ch := make(chan planApprovalResponse, 1)
	a.pendingPlanApprovals.Store("req-1", &pendingPlanApprovalEntry{ch: ch, sessionID: "sess-1"})

	// "approved" is not one of the catalog enum values (approve /
	// request_changes / abandon) — the malformed response must be dropped.
	dispatchInbound(a, webInboundFrame(t, "plan_approval_response",
		map[string]any{"request_id": "req-1", "decision": "approved", "feedback": "ship it"}), silentUIEmit(), silentLogger())

	select {
	case got := <-ch:
		t.Fatalf("malformed decision was delivered: %+v", got)
	default:
	}
	// Fail-closed: the malformed response does not consume the pending
	// approval — a valid retry can still resolve it.
	if _, ok := a.pendingPlanApprovals.Load("req-1"); !ok {
		t.Error("pending plan approval entry was consumed by a malformed decision")
	}
}

func TestWebInboundDispatch_GoalProposalResolves(t *testing.T) {
	a := &App{}
	ch := make(chan goalProposalResponse, 1)
	a.pendingGoalProposals.Store("req-1", &pendingGoalProposalEntry{ch: ch, sessionID: "sess-1"})

	// "approve" is the enum value the frontend sends (GoalProposalPanel's
	// Decision vocabulary, same as the RPC path).
	dispatchInbound(a, webInboundFrame(t, "goal_proposal_response",
		map[string]any{"request_id": "req-1", "decision": "approve",
			"condition": "tests pass", "verify": "go test ./...", "verification_mode": "command"}), silentUIEmit(), silentLogger())

	select {
	case got := <-ch:
		if got.Decision != "approve" || got.Condition != "tests pass" || got.Verify != "go test ./..." {
			t.Errorf("got %+v, want the approved proposal", got)
		}
	default:
		t.Fatal("no goal-proposal decision delivered")
	}
	if _, ok := a.pendingGoalProposals.Load("req-1"); ok {
		t.Error("pending goal proposal entry not cleaned up")
	}
}

func TestWebInboundDispatch_GoalProposalRejectsUnknownDecision(t *testing.T) {
	a := &App{}
	ch := make(chan goalProposalResponse, 1)
	a.pendingGoalProposals.Store("req-1", &pendingGoalProposalEntry{ch: ch, sessionID: "sess-1"})

	// "accepted" is not one of the catalog enum values (approve / cancel) —
	// the malformed response must be dropped, mirroring the plan-approval
	// discipline above: an unrecognized decision is a malformed response,
	// not an implicit cancel.
	dispatchInbound(a, webInboundFrame(t, "goal_proposal_response",
		map[string]any{"request_id": "req-1", "decision": "accepted",
			"condition": "tests pass", "verify": "go test ./...", "verification_mode": "command"}), silentUIEmit(), silentLogger())

	select {
	case got := <-ch:
		t.Fatalf("malformed decision was delivered: %+v", got)
	default:
	}
	// Fail-closed: the malformed response does not consume the pending
	// proposal — a valid retry can still resolve it.
	if _, ok := a.pendingGoalProposals.Load("req-1"); !ok {
		t.Error("pending goal proposal entry was consumed by a malformed decision")
	}
}

func TestWebInboundDispatch_GoalSuggestionResolves(t *testing.T) {
	a := &App{}
	ch := make(chan goalSuggestionResponse, 1)
	a.pendingGoalSuggestions.Store("req-1", &pendingGoalSuggestionEntry{ch: ch, sessionID: "sess-1"})

	dispatchInbound(a, webInboundFrame(t, "goal_suggestion_response",
		map[string]any{"request_id": "req-1", "decision": "decline"}), silentUIEmit(), silentLogger())

	select {
	case got := <-ch:
		if got.Decision != "decline" {
			t.Errorf("got %+v, want decline", got)
		}
	default:
		t.Fatal("no goal-suggestion decision delivered")
	}
	if _, ok := a.pendingGoalSuggestions.Load("req-1"); ok {
		t.Error("pending goal suggestion entry not cleaned up")
	}
}

func TestWebInboundDispatch_JudgeRoutesThroughUIEmit(t *testing.T) {
	a := &App{}
	a.pendingConfirmations.Store("cid-1", &pendingConfirmData{
		ch:        make(chan sdktools.ConfirmationResponse, 1),
		toolName:  "bash_exec",
		input:     json.RawMessage(`{"command":"ls"}`),
		sessionID: "sess-1",
	})
	emitted := make(chan session.Event, 2)
	uiEmit := func(e session.Event) {
		select {
		case emitted <- e:
		default:
		}
	}

	dispatchInbound(a, webInboundFrame(t, "tool_judge_request",
		map[string]any{"confirm_id": "cid-1"}), uiEmit, silentLogger())

	// dispatchInbound joins the judge goroutine, so the emitted event is
	// already in the buffer — a watchdog select only guards a regression
	// where the goroutine finishes without emitting.
	select {
	case e := <-emitted:
		if e.Type != "tool_judge_response" {
			t.Errorf("event type = %q, want tool_judge_response", e.Type)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no judge response event — the injected uiEmit is not the response path")
	}
	if _, ok := a.pendingConfirmations.Load("cid-1"); ok {
		t.Error("pending confirmation not cleaned up after the judge run")
	}
}

// --- drops: malformed, unnamed, and non-whitelisted frames never dispatch ---

func TestWebInboundDispatch_Drops(t *testing.T) {
	t.Run("malformed JSON", func(t *testing.T) {
		a := &App{}
		log, buf := captureLogger()
		a.dispatchWebInboundFrame([]byte("{not json"), silentUIEmit(), log)
		if !strings.Contains(buf.String(), "malformed frame dropped") {
			t.Errorf("expected the malformed-frame debug record, got %q", buf.String())
		}
	})
	t.Run("empty frame", func(t *testing.T) {
		a := &App{}
		log, buf := captureLogger()
		a.dispatchWebInboundFrame([]byte("{}"), silentUIEmit(), log)
		if !strings.Contains(buf.String(), "not allowed from the web") {
			t.Errorf("expected the not-allowed debug record, got %q", buf.String())
		}
	})
	t.Run("non-whitelisted backend event", func(t *testing.T) {
		a := &App{}
		log, buf := captureLogger()
		a.dispatchWebInboundFrame(webInboundFrame(t, "work_unit_settled",
			map[string]any{"step_id": "s1"}), silentUIEmit(), log)
		if !strings.Contains(buf.String(), "not allowed from the web") {
			t.Errorf("expected the not-allowed debug record, got %q", buf.String())
		}
		if strings.Contains(buf.String(), "work_unit") {
			// The event name may appear in the drop record; the handler must
			// not have produced any other diagnostics.
			if n := strings.Count(buf.String(), "\n"); n > 1 {
				t.Errorf("dropped frame produced %d log lines, want exactly the drop record", n)
			}
		}
	})
	t.Run("whitelisted event with empty args drops at Debug", func(t *testing.T) {
		a := &App{}
		log, buf := captureLogger()
		a.dispatchWebInboundFrame(webInboundFrame(t, "tool_confirm_response"), silentUIEmit(), log)
		if !strings.Contains(buf.String(), "missing payload") {
			t.Errorf("expected the missing-payload drop record, got %q", buf.String())
		}
		// The file's security invariant: network input must not write unbounded
		// Warn lines — the payload-shape drop stays Debug-level.
		if strings.Contains(buf.String(), "level=WARN") {
			t.Errorf("payload drop must never log at Warn, got %q", buf.String())
		}
		if !strings.Contains(buf.String(), "level=DEBUG") {
			t.Errorf("expected a Debug drop record, got %q", buf.String())
		}
	})
	t.Run("whitelisted event with non-map payload drops at Debug", func(t *testing.T) {
		a := &App{}
		log, buf := captureLogger()
		a.dispatchWebInboundFrame(webInboundFrame(t, "tool_confirm_response", "not a map"), silentUIEmit(), log)
		if !strings.Contains(buf.String(), "unexpected payload type") {
			t.Errorf("expected the unexpected-payload-type drop record, got %q", buf.String())
		}
		if strings.Contains(buf.String(), "level=WARN") {
			t.Errorf("payload drop must never log at Warn, got %q", buf.String())
		}
		if !strings.Contains(buf.String(), "level=DEBUG") {
			t.Errorf("expected a Debug drop record, got %q", buf.String())
		}
	})
	t.Run("nil frame never panics", func(t *testing.T) {
		a := &App{}
		a.dispatchWebInboundFrame(nil, silentUIEmit(), silentLogger())
	})
	t.Run("drops keep nothing pending", func(t *testing.T) {
		a := &App{}
		ch := make(chan sdktools.ConfirmationResponse, 1)
		a.pendingConfirmations.Store("rid-1", &pendingConfirmData{ch: ch, toolName: "bash_exec", sessionID: "sess-1"})
		a.dispatchWebInboundFrame(webInboundFrame(t, "ask_user_response",
			map[string]any{"request_id": "rid-1", "answers": []any{}}), silentUIEmit(), silentLogger())
		select {
		case got := <-ch:
			t.Errorf("a cross-kind frame resolved a tool confirmation: %v", got)
		default:
		}
		if _, ok := a.pendingConfirmations.Load("rid-1"); !ok {
			t.Error("a dropped frame must leave the pending entry untouched")
		}
	})
}

// silentUIEmit discards emitted session events (behavior is asserted on the
// pending channels, not on the emitter).
func silentUIEmit() func(session.Event) {
	return func(session.Event) {}
}

// --- over-the-wire: an authenticated browser tab resolves a HITL action ---

// dialWebAccessWS performs the raw upgrade handshake against the sidecar
// handler with the session cookie and a same-origin Origin header, mirroring
// what the browser's WebSocket API does.
func dialWebAccessWS(t *testing.T, hostURL, cookie string) net.Conn {
	t.Helper()
	var dialer net.Dialer
	conn, err := dialer.DialContext(context.Background(), "tcp", strings.TrimPrefix(hostURL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	req := "GET /ws HTTP/1.1\r\n" +
		"Host: " + strings.TrimPrefix(hostURL, "http://") + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key) + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Origin: " + hostURL + "\r\n" +
		"Cookie: " + cookie + "\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read handshake response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status = %d, want 101", resp.StatusCode)
	}
	return conn
}

// writeWSTextFrame writes one MASKED client text frame (clients must mask).
func writeWSTextFrame(t *testing.T, conn net.Conn, payload []byte) {
	t.Helper()
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	hdr := make([]byte, 0, 2+len(mask))
	hdr = append(hdr, 0x81, 0x80|byte(len(payload)))
	hdr = append(hdr, mask[:]...)
	masked := make([]byte, len(payload))
	for i, b := range payload {
		masked[i] = b ^ mask[i%4]
	}
	if _, err := conn.Write(append(hdr, masked...)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func TestWebInbound_AuthenticatedWSFrameResolvesToolConfirm(t *testing.T) {
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n")
	a := NewApp()
	buf := &strings.Builder{}
	log := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	a.logger = log
	a.setConfigRef(cfg, path)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer func() { _ = cancel }()
		a.stopWebAccess(ctx)
	})
	a.initWebAccess(cfg, log)
	baseURL, token := webAccessLiveEndpoint(t, a)

	client := loginWebAccessClient(t, baseURL, token)
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse baseURL: %v", err)
	}
	cookies := client.Jar.Cookies(u)
	cookieParts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		cookieParts = append(cookieParts, c.Name+"="+c.Value)
	}
	if len(cookieParts) == 0 {
		t.Fatal("login produced no session cookie")
	}

	conn := dialWebAccessWS(t, baseURL, strings.Join(cookieParts, "; "))

	// A malformed frame first: the drop must not poison the connection.
	writeWSTextFrame(t, conn, []byte("{not json"))

	ch := make(chan sdktools.ConfirmationResponse, 1)
	a.pendingConfirmations.Store("wire-1", &pendingConfirmData{ch: ch, toolName: "bash_exec", sessionID: "sess-1"})
	writeWSTextFrame(t, conn, webInboundFrame(t, "tool_confirm_response",
		map[string]any{"confirm_id": "wire-1", "decision": "allow_once"}))

	select {
	case got := <-ch:
		if got != sdktools.ConfirmAllowOnce {
			t.Errorf("got %v, want ConfirmAllowOnce", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no decision arrived over the wire within the watchdog")
	}
	if _, ok := a.pendingConfirmations.Load("wire-1"); ok {
		t.Error("pending confirmation entry not cleaned up after the wire delivery")
	}
	if !strings.Contains(buf.String(), "malformed frame dropped") {
		t.Errorf("expected the malformed-frame drop to be logged at Debug, log: %q", buf.String())
	}
}

func TestWebInbound_HotRestartRewiresInbound(t *testing.T) {
	port := reserveLoopbackPort(t)
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n  port: "+strconv.Itoa(port)+"\n")
	a := NewApp()
	log := slog.New(slog.NewTextHandler(&strings.Builder{}, &slog.HandlerOptions{Level: slog.LevelError}))
	a.logger = log
	a.setConfigRef(cfg, path)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer func() { _ = cancel }()
		a.stopWebAccess(ctx)
	})
	a.initWebAccess(cfg, log)

	resolve := func(tag string) {
		t.Helper()
		if !a.webAccessService.Running() {
			t.Fatalf("%s: sidecar not mounted", tag)
		}
		baseURL, token := webAccessLiveEndpoint(t, a)
		client := loginWebAccessClient(t, baseURL, token)
		u, err := url.Parse(baseURL)
		if err != nil {
			t.Fatalf("%s: parse baseURL: %v", tag, err)
		}
		cookies := client.Jar.Cookies(u)
		cookieParts := make([]string, 0, len(cookies))
		for _, c := range cookies {
			cookieParts = append(cookieParts, c.Name+"="+c.Value)
		}
		conn := dialWebAccessWS(t, baseURL, strings.Join(cookieParts, "; "))
		ch := make(chan sdktools.ConfirmationResponse, 1)
		a.pendingConfirmations.Store(tag, &pendingConfirmData{ch: ch, toolName: "bash_exec", sessionID: "sess-1"})
		writeWSTextFrame(t, conn, webInboundFrame(t, "tool_confirm_response",
			map[string]any{"confirm_id": tag, "decision": "deny"}))
		select {
		case got := <-ch:
			if got != sdktools.ConfirmDeny {
				t.Errorf("%s: got %v, want ConfirmDeny", tag, got)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no decision over the wire — the restart did not re-wire the inbound bridge", tag)
		}
	}

	resolve("wire-first")
	if err := a.SetWebAccessEnabled(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := a.SetWebAccessEnabled(true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	resolve("wire-second")
}
