package lexical

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"path/filepath"
	"sync"
	"testing"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/index/scorch"
)

// scorchOptionsContract mirrors the two fields the fix depends on in scorch's
// persisterOptions: bleve JSON-round-trips scorchPersisterOptions into that
// struct, so a key-name drift here would silently fall back to the 1000-file
// default and resurrect the deadlock the options exist to prevent.
func TestScorchOptions_PersisterContract(t *testing.T) {
	opts := scorchOptions()

	name, ok := opts["asyncErrorCallbackName"].(string)
	if !ok || name == "" {
		t.Fatalf("asyncErrorCallbackName = %q, want a registered callback name", name)
	}
	if name != scorchAsyncErrorCallbackName {
		t.Fatalf("asyncErrorCallbackName = %q, want %q", name, scorchAsyncErrorCallbackName)
	}

	raw, err := json.Marshal(opts)
	if err != nil {
		t.Fatalf("the options map must stay JSON-marshalable (bleve round-trips it): %v", err)
	}
	var po struct {
		PersisterNapTimeMSec      int `json:"PersisterNapTimeMSec"`
		PersisterNapUnderNumFiles int `json:"PersisterNapUnderNumFiles"`
	}
	if err := json.Unmarshal(extractPersisterOptions(t, raw), &po); err != nil {
		t.Fatalf("unmarshaling scorchPersisterOptions: %v", err)
	}
	if po.PersisterNapUnderNumFiles != math.MaxInt {
		t.Fatalf("PersisterNapUnderNumFiles = %d, want MaxInt — the merger-catch-up pause must never trigger", po.PersisterNapUnderNumFiles)
	}
	if po.PersisterNapTimeMSec != 0 {
		t.Fatalf("PersisterNapTimeMSec = %d, want 0 (the nap branch needs it > 0 and buys nothing here)", po.PersisterNapTimeMSec)
	}
}

// extractPersisterOptions pulls the nested scorchPersisterOptions value out of
// the marshaled options map.
func extractPersisterOptions(t *testing.T, raw []byte) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshaling options map: %v", err)
	}
	po, ok := m["scorchPersisterOptions"]
	if !ok {
		t.Fatal("options map carries no scorchPersisterOptions key")
	}
	return po
}

// TestScorchAsyncErrorCallbackRegistered pins the registry wiring: the
// callback must be registered under the package's name before any open
// (scorch looks it up at construction), and it must route into the package
// sink.
func TestScorchAsyncErrorCallbackRegistered(t *testing.T) {
	cb, ok := scorch.RegistryAsyncErrorCallbacks[scorchAsyncErrorCallbackName]
	if !ok || cb == nil {
		t.Fatalf("no async-error callback registered under %q", scorchAsyncErrorCallbackName)
	}

	type report struct {
		err  error
		path string
	}
	got := make(chan report, 1)
	installAsyncErrSink(t, func(err error, path string) {
		got <- report{err: err, path: path}
	})

	fired := scorch.NewScorchError("merger", "panic: boom", scorch.ErrAsyncPanic)
	cb(fired, "/tmp/some-lexical-index")

	select {
	case r := <-got:
		if !errors.Is(r.err, scorch.ErrAsyncPanic) {
			t.Fatalf("sink received error that lost its ErrAsyncPanic identity: %v", r.err)
		}
		if r.path != "/tmp/some-lexical-index" {
			t.Fatalf("sink path = %q, want the index path", r.path)
		}
	default:
		t.Fatal("the registered callback did not reach the package sink")
	}
}

// TestLogScorchAsyncError_LevelByType pins the default reporting levels with a
// test-local injected logger: a background-loop panic (the goroutine is gone)
// logs at Error, other background errors at Warn.
func TestLogScorchAsyncError_LevelByType(t *testing.T) {
	rec := &recordingHandler{}
	log := slog.New(rec)

	logScorchAsyncError(log, scorch.NewScorchError("merger", "panic: x", scorch.ErrAsyncPanic), "/idx")
	logScorchAsyncError(log, scorch.NewScorchError("persister", "got err persisting snapshot: disk full", scorch.ErrPersist), "/idx")

	if got := len(rec.records); got != 2 {
		t.Fatalf("records = %d, want 2", got)
	}
	if rec.records[0].level != slog.LevelError || rec.records[0].msg != "scorch background error" {
		t.Fatalf("panic record = (%v, %q), want (Error, scorch background error)", rec.records[0].level, rec.records[0].msg)
	}
	if rec.records[1].level != slog.LevelWarn {
		t.Fatalf("persist record level = %v, want Warn", rec.records[1].level)
	}
}

// recordingHandler is a minimal slog handler capturing level and message —
// enough to assert the classification without asserting prose details.
type recordingHandler struct {
	mu      sync.Mutex
	records []struct {
		level slog.Level
		msg   string
	}
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, struct {
		level slog.Level
		msg   string
	}{level: r.Level, msg: r.Message})
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *recordingHandler) WithGroup(string) slog.Handler { return h }

// installAsyncErrSink swaps the package sink and restores it on cleanup; the
// mutex mirrors the one reportScorchAsyncError takes against scorch's own
// reporting goroutines.
func installAsyncErrSink(t *testing.T, sink func(err error, path string)) {
	t.Helper()
	asyncErrMu.Lock()
	prev := asyncErrSink
	asyncErrSink = sink
	asyncErrMu.Unlock()
	t.Cleanup(func() {
		asyncErrMu.Lock()
		asyncErrSink = prev
		asyncErrMu.Unlock()
	})
}

// TestIndex_OpenLegacyIndexAppliesOptions is the back-compat rung: indexes on
// disk created before the options existed (a plain bleve.New meta, the shape
// every existing c0wrk store carries) must reopen and serve through the
// runtime-options path.
func TestIndex_OpenLegacyIndexAppliesOptions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lex")
	ctx := context.Background()

	mapping, err := buildMapping()
	if err != nil {
		t.Fatalf("buildMapping: %v", err)
	}
	legacy, err := bleve.New(dir, mapping)
	if err != nil {
		t.Fatalf("bleve.New: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("closing the legacy index: %v", err)
	}

	idx, err := Open(dir)
	if err != nil {
		t.Fatalf("Open over a legacy meta: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	if err := idx.Upsert(ctx, []Doc{{ID: "a:0", Content: "legacy shape", FilePath: "/p/a.go", Language: "go"}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	hits, err := idx.Query(ctx, "legacy", 5)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "a:0" {
		t.Fatalf("hits = %+v, want top a:0", hits)
	}
}
