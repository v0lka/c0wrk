package session

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/orchestration"
)

// forecastCtxStore records the contexts its app_state methods were called
// with. It embeds the ProjectStore interface so only the two methods under
// test need real implementations; nothing else is reached by the forecast
// persistence paths.
type forecastCtxStore struct {
	project.ProjectStore
	loadCtx context.Context
	saveCtx context.Context
}

func (s *forecastCtxStore) LoadAppState(ctx context.Context, key string) (string, error) {
	s.loadCtx = ctx
	return "", nil
}

func (s *forecastCtxStore) SaveAppState(ctx context.Context, key, value string) error {
	s.saveCtx = ctx
	return nil
}

// TestCompactionForecastStoreCallsAreBounded pins review [C3-4]: the forecast
// persistence paths must pass the store a deadline-bounded context
// (restoreDBReadTimeout), never a bare context.Background(). The calls share
// the app's single SQLite connection with every active session's writes, and
// the restore head-reads are bounded for exactly that reason — an unbounded
// forecast load parks the lazy restore (and its restoreInFlight waiters), an
// unbounded save stalls the compaction-finish path.
func TestCompactionForecastStoreCallsAreBounded(t *testing.T) {
	factory := func(core.Emitter, *slog.Logger, string, core.BlackboardFactory, io.Writer, *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		return nil, nil
	}
	m := NewManager(factory, func(Event) {}, runtimeTempDir(t))
	t.Cleanup(m.Shutdown)
	store := &forecastCtxStore{}
	m.SetProjectStore(store)

	// The zero-value orchestrator is sufficient: both paths only read
	// CurrentModel and (for persist) CompactionForecast — field accessors
	// guarded by zero-value-usable mutexes.
	orch := &core.Orchestrator{}

	m.persistCompactionForecast(orch)
	m.loadCompactionForecast(orch)

	cases := []struct {
		name string
		ctx  context.Context
	}{
		{"SaveAppState (persist)", store.saveCtx},
		{"LoadAppState (load)", store.loadCtx},
	}
	for _, tc := range cases {
		if tc.ctx == nil {
			t.Fatalf("%s was not called", tc.name)
		}
		dl, ok := tc.ctx.Deadline()
		if !ok {
			t.Fatalf("%s received a context without a deadline, want the %s bound", tc.name, restoreDBReadTimeout)
		}
		if remain := time.Until(dl); remain <= 0 || remain > restoreDBReadTimeout {
			t.Fatalf("%s deadline has %v remaining, want it bounded by %s", tc.name, remain, restoreDBReadTimeout)
		}
	}
}
