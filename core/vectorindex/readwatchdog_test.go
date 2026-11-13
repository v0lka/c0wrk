package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// wdHandler is a test slog.Handler recording whole records so the read-
// watchdog tests can assert their expected diagnostics at the source — the
// skip warns with their "read timeout" errors — and that no unexpected
// warn/error leaked alongside them (test-output quality rule).
type wdHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *wdHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *wdHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *wdHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *wdHandler) WithGroup(string) slog.Handler      { return h }

// wdAudit asserts the pass's whole diagnostic surface: exactly want warns,
// every one of them the skip label carrying a "read timeout" error attr, and
// no error-level records at all.
func (h *wdHandler) wdAudit(t *testing.T, skipMsg string, want int) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	warns, errs := 0, 0
	for _, r := range h.records {
		switch r.Level {
		case slog.LevelWarn:
			if r.Message != skipMsg {
				t.Errorf("unexpected WARN diagnostic %q", r.Message)
				continue
			}
			if got := wdErrorAttr(r); !strings.Contains(got, "read timeout") {
				t.Errorf("skip warn error = %q, want it to name the read timeout", got)
			}
			warns++
		case slog.LevelError:
			errs++
		}
	}
	if warns != want {
		t.Errorf("skip warns = %d, want %d", warns, want)
	}
	if errs != 0 {
		t.Errorf("unexpected ERROR diagnostics = %d", errs)
	}
}

// wdErrorAttr returns the record's "error" attribute as a string.
func wdErrorAttr(rec slog.Record) string {
	s := ""
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key == "error" {
			s = a.Value.String()
		}
		return true
	})
	return s
}

// wdWatchdogIndexer builds an Indexer over a bare in-memory service with the
// binary-header seam replaced by a synthetic non-blocking check: the
// watchdog tests drive the read seams directly, so no OS open happens inside
// the synctest bubble. The only in-bubble filesystem touch is processFile's
// never-blocking os.Stat of files created outside the bubble.
func wdWatchdogIndexer(t *testing.T, h *wdHandler) *Indexer {
	t.Helper()
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	idx := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		Logger:  slog.New(h),
	})
	idx.binaryHeaderFn = func(string) (bool, error) { return false, nil }
	return idx
}

// wdWriteFile writes one regular file for os.Stat to clear, outside the
// synctest bubble.
func wdWriteFile(t *testing.T, root, name string) string {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestReadWatchdogHungReadSkipsCountedPassCompletes drives the core
// watchdog contract under virtual time: one wedged regular-file read is the
// blocking-reader seam; the pass must skip the file at the watchdog
// deadline, COUNT it toward pass progress (incremental countSkipped policy),
// keep indexing the other files, and complete — while the abandoned reader
// goroutine stays stuck until released after the pass.
func TestReadWatchdogHungReadSkipsCountedPassCompletes(t *testing.T) {
	root := t.TempDir()
	hung := wdWriteFile(t, root, "hung.go")
	others := []string{wdWriteFile(t, root, "a.go"), wdWriteFile(t, root, "b.go")}
	synctest.Test(t, func(t *testing.T) {
		h := &wdHandler{}
		idx := wdWatchdogIndexer(t, h)
		release := make(chan struct{})
		// Release the abandoned reader before the bubble drains (registered
		// before any fatal assertion so cleanup always runs).
		defer close(release)
		idx.readFileFn = func(path string) ([]byte, error) {
			if filepath.Base(path) == "hung.go" {
				<-release // wedged read: never returns while the pass runs
				return []byte("never indexed"), nil
			}
			return []byte(""), nil // empty content: zero docs, no service writes
		}

		pass := idx.beginIndexPass()
		defer pass.release()
		paths := append([]string{hung}, others...)
		started := time.Now()
		progress, err := idx.indexPrepared(context.Background(), paths, indexPipelineOpts{
			state:        IndexStateReindexing,
			total:        len(paths),
			countSkipped: true,
			cancelMsg:    "incremental indexing cancelled",
			skipLogLabel: "skipping file during incremental index",
		}, pass)
		elapsed := time.Since(started)

		if err != nil {
			t.Fatalf("pass with one hung read must complete: %v", err)
		}
		if progress != len(paths) {
			t.Errorf("progress = %d, want %d: the timed-out file counts toward pass progress", progress, len(paths))
		}
		if elapsed < fileReadWatchdogTimeout || elapsed >= 2*fileReadWatchdogTimeout {
			t.Errorf("pass elapsed %v; want the skip to land at the %v watchdog deadline",
				elapsed, fileReadWatchdogTimeout)
		}
		h.wdAudit(t, "skipping file during incremental index", 1)
	})
}

// TestReadWatchdogFullPassCompletesWithUncountedSkip pins the full-pass side
// of the accounting policy: a timed-out read is skipped and the pass
// completes, but a full pass does not count skipped files toward progress.
func TestReadWatchdogFullPassCompletesWithUncountedSkip(t *testing.T) {
	root := t.TempDir()
	hung := wdWriteFile(t, root, "hung.go")
	others := []string{wdWriteFile(t, root, "a.go"), wdWriteFile(t, root, "b.go")}
	synctest.Test(t, func(t *testing.T) {
		h := &wdHandler{}
		idx := wdWatchdogIndexer(t, h)
		release := make(chan struct{})
		defer close(release)
		idx.readFileFn = func(path string) ([]byte, error) {
			if filepath.Base(path) == "hung.go" {
				<-release
				return []byte("never indexed"), nil
			}
			return []byte(""), nil
		}

		pass := idx.beginIndexPass()
		defer pass.release()
		progress, err := idx.indexPrepared(context.Background(), append([]string{hung}, others...), indexPipelineOpts{
			state:        IndexStateIndexing,
			total:        len(others) + 1,
			cancelMsg:    "indexing cancelled",
			skipLogLabel: "skipping file",
		}, pass)

		if err != nil {
			t.Fatalf("full pass with one hung read must complete: %v", err)
		}
		if progress != len(others) {
			t.Errorf("progress = %d, want %d: a full pass does not count skipped files", progress, len(others))
		}
		h.wdAudit(t, "skipping file", 1)
	})
}

// TestReadWatchdogBudgetExhaustionRefusesPass pins the refusal: with
// fileReadTimeoutSkipBudget+1 wedged files, the pass skips exactly
// fileReadTimeoutSkipBudget reads (each stranding one reader goroutine) and
// then refuses explicitly with ErrReadSkipBudgetExhausted. The pre-read
// check is per-file on a fanned-out walk (streamPreparedFiles runs
// prepWorkers workers), so a worker whose check precedes the last in-flight
// timeout lawfully starts one extra read: the deterministic bound is
// budget..budget+1 attempted reads, the tolerated skips stay exactly at the
// budget, and the refusal — pre-check or spent-allowance timeout alike —
// ends the pass fatally instead of stranding more readers.
func TestReadWatchdogBudgetExhaustionRefusesPass(t *testing.T) {
	const victims = fileReadTimeoutSkipBudget + 1
	root := t.TempDir()
	paths := make([]string, 0, victims)
	for i := range victims {
		paths = append(paths, wdWriteFile(t, root, fmt.Sprintf("hung-%02d.go", i)))
	}
	synctest.Test(t, func(t *testing.T) {
		h := &wdHandler{}
		idx := wdWatchdogIndexer(t, h)
		release := make(chan struct{})
		defer close(release)
		var entered atomic.Int64
		idx.readFileFn = func(string) ([]byte, error) {
			entered.Add(1)
			<-release // every read in this test wedges
			return nil, nil
		}

		pass := idx.beginIndexPass()
		defer pass.release()
		_, err := idx.indexPrepared(context.Background(), paths, indexPipelineOpts{
			state:        IndexStateIndexing,
			total:        victims,
			cancelMsg:    "indexing cancelled",
			skipLogLabel: "skipping file",
		}, pass)

		if !errors.Is(err, ErrReadSkipBudgetExhausted) {
			t.Fatalf("budget exhaustion = %v, want ErrReadSkipBudgetExhausted", err)
		}
		if got := entered.Load(); got < fileReadTimeoutSkipBudget || got > fileReadTimeoutSkipBudget+1 {
			t.Errorf("reads attempted = %d, want %d..%d: the budget bounds the attempted reads and the refusal stops further ones",
				got, fileReadTimeoutSkipBudget, fileReadTimeoutSkipBudget+1)
		}
		h.wdAudit(t, "skipping file", fileReadTimeoutSkipBudget)
	})
}

// TestProcessFileReadTimeout pins the per-file unit: a read wedged past the
// deadline returns a "read timeout" skip error with no documents, at virtual
// deadline cost; a read that completes indexes normally.
func TestProcessFileReadTimeout(t *testing.T) {
	p := wdWriteFile(t, t.TempDir(), "wedged.go")
	synctest.Test(t, func(t *testing.T) {
		idx := wdWatchdogIndexer(t, &wdHandler{})
		release := make(chan struct{})
		defer close(release)
		idx.readFileFn = func(string) ([]byte, error) {
			<-release
			return nil, nil
		}
		started := time.Now()
		vecDocs, lexDocs, err := idx.processFile(p, "", nil)
		elapsed := time.Since(started)
		if !errors.Is(err, errFileReadTimeout) {
			t.Fatalf("wedged read = %v, want errFileReadTimeout", err)
		}
		if !strings.Contains(err.Error(), "read timeout") {
			t.Errorf("error %q must carry the skip label", err)
		}
		if len(vecDocs) != 0 || len(lexDocs) != 0 {
			t.Errorf("timed-out file must produce no documents, got %d vec / %d lex", len(vecDocs), len(lexDocs))
		}
		if elapsed < fileReadWatchdogTimeout || elapsed >= 2*fileReadWatchdogTimeout {
			t.Errorf("elapsed %v; want the skip at the %v deadline", elapsed, fileReadWatchdogTimeout)
		}

		// Control: a read completing within the deadline indexes normally.
		idx.readFileFn = func(string) ([]byte, error) { return []byte("package main\n"), nil }
		vecDocs, lexDocs, err = idx.processFile(p, "", nil)
		if err != nil {
			t.Fatalf("completing read: %v", err)
		}
		if len(vecDocs) != 1 || len(lexDocs) != 1 {
			t.Errorf("completing read produced %d vec / %d lex docs, want 1/1", len(vecDocs), len(lexDocs))
		}
	})
}

// TestProcessFileBinaryHeaderTimeout pins that the binary-header pre-read is
// bounded too: a wedged header read skips the file at the deadline and the
// full read is never attempted.
func TestProcessFileBinaryHeaderTimeout(t *testing.T) {
	p := wdWriteFile(t, t.TempDir(), "wedged-header.go")
	synctest.Test(t, func(t *testing.T) {
		idx := wdWatchdogIndexer(t, &wdHandler{})
		release := make(chan struct{})
		defer close(release)
		idx.binaryHeaderFn = func(string) (bool, error) {
			<-release
			return false, nil
		}
		var fullReads atomic.Int64
		idx.readFileFn = func(string) ([]byte, error) {
			fullReads.Add(1)
			return []byte("x"), nil
		}
		_, _, err := idx.processFile(p, "", nil)
		if !errors.Is(err, errFileReadTimeout) {
			t.Fatalf("wedged header read = %v, want errFileReadTimeout", err)
		}
		if got := fullReads.Load(); got != 0 {
			t.Errorf("full reads after a header timeout = %d, want 0", got)
		}
	})
}

// TestProcessFileSpentBudgetRefusesBeforeRead pins the pre-read refusal: a
// budget with no allowances turns the file away before any read is
// attempted, so a spent budget starts no further reader goroutines.
func TestProcessFileSpentBudgetRefusesBeforeRead(t *testing.T) {
	p := wdWriteFile(t, t.TempDir(), "never-read.go")
	synctest.Test(t, func(t *testing.T) {
		idx := wdWatchdogIndexer(t, &wdHandler{})
		var reads atomic.Int64
		idx.readFileFn = func(string) ([]byte, error) {
			reads.Add(1)
			return []byte("x"), nil
		}
		_, _, err := idx.processFile(p, "", newReadSkipBudget(0))
		if !errors.Is(err, ErrReadSkipBudgetExhausted) {
			t.Fatalf("spent budget = %v, want ErrReadSkipBudgetExhausted", err)
		}
		if got := reads.Load(); got != 0 {
			t.Errorf("spent budget attempted %d read(s); it must refuse before any read", got)
		}
	})
}

// TestReadTimeoutSkipErrorBudgetTransitions pins the allowance arithmetic:
// the budget's last allowance still tolerates a skip, the next timeout is
// the explicit refusal, and a nil budget (direct callers) always tolerates.
func TestReadTimeoutSkipErrorBudgetTransitions(t *testing.T) {
	b := newReadSkipBudget(1)
	first := readTimeoutSkipError("/f", b)
	if !errors.Is(first, errFileReadTimeout) || errors.Is(first, ErrReadSkipBudgetExhausted) {
		t.Fatalf("first timeout = %v, want a tolerated read timeout", first)
	}
	if second := readTimeoutSkipError("/g", b); !errors.Is(second, ErrReadSkipBudgetExhausted) {
		t.Fatalf("second timeout = %v, want ErrReadSkipBudgetExhausted", second)
	}
	for range 3 {
		err := readTimeoutSkipError("/f", nil)
		if !errors.Is(err, errFileReadTimeout) || errors.Is(err, ErrReadSkipBudgetExhausted) {
			t.Fatalf("nil-budget timeout = %v, want a tolerated read timeout", err)
		}
	}
}
