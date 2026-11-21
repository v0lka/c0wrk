package desktop

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// logRecord is one captured slog record with its typed attributes.
type logRecord struct {
	level slog.Level
	msg   string
	attrs []slog.Attr
}

// recordingHandler captures every record it is handed, so tests can assert
// on severity, payload and call count instead of parsing formatted text.
type recordingHandler struct {
	mu      sync.Mutex
	records []logRecord
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	rec := logRecord{level: r.Level, msg: r.Message}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs = append(rec.attrs, a)
		return true
	})
	h.records = append(h.records, rec)
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) all() []logRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]logRecord(nil), h.records...)
}

func (h *recordingHandler) find(msg string) []logRecord {
	var out []logRecord
	for _, r := range h.all() {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

// TestDomReady_LogsWebviewReadyElapsed verifies the webview-boot diagnostic:
// DomReady logs exactly one Info "webview ready" record carrying a
// non-negative elapsed_ms measured from the Startup-entry anchor, into the
// instance logger — without changing the reveal behavior.
func TestDomReady_LogsWebviewReadyElapsed(t *testing.T) {
	f := newWindowShowFixture()
	h := &recordingHandler{}
	f.app.logger = slog.New(h)
	f.app.setBootStartedAt(time.Now())

	f.app.DomReady(context.Background())

	found := h.find("webview ready")
	if len(found) != 1 {
		t.Fatalf("expected exactly one 'webview ready' record, got %d (all: %+v)", len(found), h.all())
	}
	rec := found[0]
	if rec.level != slog.LevelInfo {
		t.Errorf("expected Info level, got %v", rec.level)
	}
	if len(rec.attrs) != 1 || rec.attrs[0].Key != "elapsed_ms" {
		t.Fatalf("expected a single elapsed_ms attribute, got %+v", rec.attrs)
	}
	ms, ok := rec.attrs[0].Value.Any().(int64)
	if !ok {
		t.Fatalf("expected elapsed_ms to be an int64, got %T", rec.attrs[0].Value.Any())
	}
	if ms < 0 {
		t.Errorf("expected a non-negative elapsed_ms, got %d", ms)
	}
	if f.showCalls != 1 {
		t.Errorf("expected the window reveal to be unaffected (1 call), got %d", f.showCalls)
	}
}

// TestDomReady_WebviewReadyLogRepeatIsHarmless covers the reload path for the
// diagnostic: every re-fired DomReady re-logs 'webview ready' with a
// non-decreasing elapsed_ms — a plain harmless log, no panic, no extra state.
func TestDomReady_WebviewReadyLogRepeatIsHarmless(t *testing.T) {
	f := newWindowShowFixture()
	h := &recordingHandler{}
	f.app.logger = slog.New(h)
	f.app.setBootStartedAt(time.Now())

	f.app.DomReady(context.Background())
	f.app.DomReady(context.Background())
	f.app.DomReady(context.Background())

	found := h.find("webview ready")
	if len(found) != 3 {
		t.Fatalf("expected three 'webview ready' records (one per DomReady), got %d", len(found))
	}
	prev := int64(-1)
	for i, rec := range found {
		if rec.level != slog.LevelInfo {
			t.Errorf("record %d: expected Info level, got %v", i, rec.level)
		}
		ms := rec.attrs[0].Value.Int64()
		if ms < prev {
			t.Errorf("record %d: elapsed_ms went backwards (%d < %d)", i, ms, prev)
		}
		prev = ms
	}
	if f.showCalls != 3 {
		t.Errorf("expected one window show per DomReady (3), got %d", f.showCalls)
	}
}

// TestDomReady_NoStartupAnchorStaysSilent guards the zero-anchor path: DomReady
// without a prior Startup (only reachable in tests) must not log an absurd
// zero-time elapsed — the diagnostic stays silent, the Debug line remains.
func TestDomReady_NoStartupAnchorStaysSilent(t *testing.T) {
	f := newWindowShowFixture()
	h := &recordingHandler{}
	f.app.logger = slog.New(h)

	f.app.DomReady(context.Background())

	if found := h.find("webview ready"); len(found) != 0 {
		t.Fatalf("expected no 'webview ready' record without a Startup anchor, got %d", len(found))
	}
	if dbg := h.find("frontend DOM ready"); len(dbg) != 1 {
		t.Errorf("expected the existing Debug 'frontend DOM ready' record to remain, got %d", len(dbg))
	}
}
