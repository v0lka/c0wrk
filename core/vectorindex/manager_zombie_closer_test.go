package vectorindex

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/v0lka/c0wrk/core"
)

// Grace-expiry log messages the zombie-closer tests pin (message identity is
// the log's contract with log-based triage).
const (
	zcEvidenceMsg = "vector shutdown grace expired; clean close skipped and deferred to the zombie closer"
	zcDoneMsg     = "zombie closer: hung goroutine returned; performing the clean close Shutdown skipped"
)

// zcHandler is a test slog.Handler recording whole records so the grace-expiry
// evidence (severity, message, dump and skipped-close attrs) is asserted at
// its source, never silently dropped.
type zcHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *zcHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *zcHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *zcHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *zcHandler) WithGroup(string) slog.Handler      { return h }

func (h *zcHandler) find(msg string) (slog.Record, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Message == msg {
			return r, true
		}
	}
	return slog.Record{}, false
}

func (h *zcHandler) messages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.records))
	for _, r := range h.records {
		out = append(out, r.Level.String()+" "+r.Message)
	}
	return out
}

func (h *zcHandler) hasMsg(substr string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if strings.Contains(r.Message, substr) {
			return true
		}
	}
	return false
}

// zcAttrs extracts the evidence attrs of the grace-expiry warn.
func zcAttrs(rec slog.Record) (stacks string, truncated bool, skipped []string) {
	rec.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "stacks":
			stacks = a.Value.String()
		case "dump_truncated":
			truncated = a.Value.Bool()
		case "skipped_close":
			if s, ok := a.Value.Any().([]string); ok {
				skipped = s
			}
		}
		return true
	})
	return stacks, truncated, skipped
}

// TestShutdownGraceExpiryZombieClosesAfterPassReturns drives the whole
// grace-expiry contract under virtual time: a hung indexing pass is the seam;
// Shutdown must return via the grace (zero wall time), log the evidence
// (capped goroutine dump + the skipped closes by name), perform NO close
// while the pass is stuck, refuse a reopen of the sealed manager without
// deadlocking on the live zombie, and perform the skipped close only after
// the pass returns — in that order.
func TestShutdownGraceExpiryZombieClosesAfterPassReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		h := &zcHandler{}
		var (
			mu     sync.Mutex
			events []string
			closes atomic.Int32
		)
		record := func(e string) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, e)
		}
		mgr := &Manager{
			service:       svc,
			logger:        slog.New(h),
			shutdownGrace: 1 * time.Millisecond, // the virtual clock fires it instantly
			closeFn: func() error {
				closes.Add(1)
				record("closed")
				return nil
			},
		}

		release := make(chan struct{})
		mgr.indexingWG.Add(1)
		go func() {
			defer mgr.indexingWG.Done()
			<-release // wedged pass: ignores ctx like a stuck ONNX call
			record("pass_returned")
		}()

		mgr.Shutdown() // returns via the grace timer — virtual, no wall time

		rec, ok := h.find(zcEvidenceMsg)
		if !ok {
			t.Fatalf("missing grace-expiry evidence warn; records: %v", h.messages())
		}
		if rec.Level != slog.LevelWarn {
			t.Errorf("evidence level = %v, want WARN", rec.Level)
		}
		stacks, truncated, skipped := zcAttrs(rec)
		if !strings.Contains(stacks, "goroutine") {
			t.Error("evidence warn carries no goroutine dump")
		}
		if truncated {
			t.Error("evidence dump reported truncated in a tiny bubble")
		}
		if len(skipped) != 2 || skipped[0] != "service.Close" || skipped[1] != "closeFn" {
			t.Errorf("skipped_close = %v, want [service.Close closeFn]", skipped)
		}

		// Nothing may close while the pass is stuck; the zombie is durably
		// blocked on the drain (WaitGroup-gated, so this is structural).
		synctest.Wait()
		if got := closes.Load(); got != 0 {
			t.Errorf("close performed before the stuck pass returned: %d", got)
		}

		// A reopen while the zombie lives is refused, not deadlocked.
		if err := mgr.SwitchProject("reopened", "ws", "vi", ProjectCallbacks{}); !errors.Is(err, ErrManagerShutdown) {
			t.Errorf("SwitchProject on sealed manager = %v, want ErrManagerShutdown", err)
		}

		close(release)
		synctest.Wait() // pass returns → zombie performs the skipped close

		if got := closes.Load(); got != 1 {
			t.Fatalf("zombie closer close calls = %d, want 1", got)
		}
		if _, ok := h.find(zcDoneMsg); !ok {
			t.Error("missing zombie closer completion log")
		}
		mu.Lock()
		defer mu.Unlock()
		if len(events) != 2 || events[0] != "pass_returned" || events[1] != "closed" {
			t.Errorf("close order = %v, want [pass_returned closed]", events)
		}
	})
}

// TestShutdownGraceExpiryZombieWaitsForStuckInit pins the init-hang variant:
// the zombie waits for the stuck init goroutine (then the indexing drain),
// both SwitchProject paths refuse the sealed manager promptly while the
// zombie lives — closing the initWG Add-vs-Wait panic window a post-shutdown
// switch used to race — and the skipped close lands only after init returns.
func TestShutdownGraceExpiryZombieWaitsForStuckInit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		h := &zcHandler{}
		var (
			mu     sync.Mutex
			events []string
			closes atomic.Int32
		)
		record := func(e string) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, e)
		}
		mgr := &Manager{
			service:       svc,
			logger:        slog.New(h),
			shutdownGrace: 1 * time.Millisecond,
			closeFn: func() error {
				closes.Add(1)
				record("closed")
				return nil
			},
		}

		release := make(chan struct{})
		mgr.initWG.Add(1)
		go func() {
			defer mgr.initWG.Done()
			<-release // wedged init: the non-interruptible gob-decode stand-in
			record("init_returned")
		}()

		mgr.Shutdown()

		if _, ok := h.find(zcEvidenceMsg); !ok {
			t.Fatalf("missing grace-expiry evidence warn; records: %v", h.messages())
		}
		synctest.Wait()
		if got := closes.Load(); got != 0 {
			t.Errorf("close performed before the stuck init returned: %d", got)
		}
		if err := mgr.SwitchProject("reopened", "ws", "vi", ProjectCallbacks{}); !errors.Is(err, ErrManagerShutdown) {
			t.Errorf("CODE-mode SwitchProject on sealed manager = %v, want ErrManagerShutdown", err)
		}
		if err := mgr.SwitchProject(core.NoProjectID, "", "", ProjectCallbacks{}); !errors.Is(err, ErrManagerShutdown) {
			t.Errorf("No Project SwitchProject on sealed manager = %v, want ErrManagerShutdown", err)
		}

		close(release)
		synctest.Wait()

		if got := closes.Load(); got != 1 {
			t.Fatalf("zombie closer close calls = %d, want 1", got)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(events) != 2 || events[0] != "init_returned" || events[1] != "closed" {
			t.Errorf("close order = %v, want [init_returned closed]", events)
		}
	})
}

// TestShutdownCleanPathStaysSilent guards the skip path against false
// positives: a shutdown whose goroutine groups drained cleanly closes inline
// and logs neither the grace-expiry evidence nor any zombie-closer line.
func TestShutdownCleanPathStaysSilent(t *testing.T) {
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := &zcHandler{}
	var closes atomic.Int32
	mgr := &Manager{
		service:       svc,
		logger:        slog.New(h),
		shutdownGrace: 10 * time.Millisecond,
		closeFn:       func() error { closes.Add(1); return nil },
	}

	mgr.Shutdown()

	if got := closes.Load(); got != 1 {
		t.Fatalf("clean shutdown close calls = %d, want 1 (inline, not deferred)", got)
	}
	if h.hasMsg("grace expired") {
		t.Error("clean shutdown logged the grace-expiry evidence")
	}
	if h.hasMsg("zombie closer") {
		t.Error("clean shutdown logged a zombie-closer line")
	}
}
