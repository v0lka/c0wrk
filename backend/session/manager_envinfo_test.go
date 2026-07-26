package session

import (
	"context"
	"testing"
	"time"
)

// TestManager_StartEnvInfoCollection_SetsEnvInfo verifies the core contract
// introduced when NewApplication stopped blocking on CollectEnvInfo:
//
//	StartEnvInfoCollection launches the background probe goroutine, and once
//	WaitEnvInfo returns nil the manager's envInfo field is populated and
//	observable (read under m.mu so the race detector stays happy).
//
// Arch is set synchronously from runtime.GOARCH inside CollectEnvInfo, so it
// is never empty regardless of which external-process probes succeed — making
// it the most robust field to assert on.
func TestManager_StartEnvInfoCollection_SetsEnvInfo(t *testing.T) {
	m, _, _ := testManager(t)

	m.StartEnvInfoCollection()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.WaitEnvInfo(ctx); err != nil {
		t.Fatalf("WaitEnvInfo: %v", err)
	}

	// After collection, envInfo must be populated and observable. Read it
	// under the manager's mutex — the field is written from the collection
	// goroutine, so an unsynchronized read here would trip the race detector.
	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo == nil {
		t.Fatal("envInfo is nil after StartEnvInfoCollection")
	}

	// Arch is populated synchronously from runtime.GOARCH and does not depend
	// on any external process, so it must always be set.
	if envInfo.Arch == "" {
		t.Error("envInfo.Arch is empty")
	}
}

// TestManager_StartEnvInfoCollection_Idempotent ensures the sync.Once guard
// works: a second call must be a safe no-op and never panic on a double-close
// of envInfoDone. WaitEnvInfo is drained afterwards so the background goroutine
// is observed as finished before the test tears down.
func TestManager_StartEnvInfoCollection_Idempotent(t *testing.T) {
	m, _, _ := testManager(t)

	// First launch.
	m.StartEnvInfoCollection()
	// Second call must not panic (sync.Once makes it a no-op and prevents a
	// double-close of envInfoDone).
	m.StartEnvInfoCollection()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.WaitEnvInfo(ctx); err != nil {
		t.Fatalf("WaitEnvInfo: %v", err)
	}
}

// TestManager_WaitEnvInfo_CancelledContext confirms the select branch: when the
// caller's context expires before collection finishes, WaitEnvInfo returns the
// context's error rather than blocking indefinitely. We force this by never
// starting collection, so envInfoDone is never closed.
func TestManager_WaitEnvInfo_CancelledContext(t *testing.T) {
	m, _, _ := testManager(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := m.WaitEnvInfo(ctx); err == nil {
		t.Fatal("WaitEnvInfo returned nil for an already-cancelled context; want ctx.Err()")
	}
}
