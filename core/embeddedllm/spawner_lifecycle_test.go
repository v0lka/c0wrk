package embeddedllm

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestFakeSpawnerPublishesOnlyConfiguredProcess(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var spawnErr error
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	sp := &fakeSpawner{configure: func(p *fakeProcess, _ LaunchCommand) {
		close(entered)
		<-release
		p.ignoreSignals = true
	}}
	go func() {
		defer close(done)
		_, spawnErr = sp.spawn(context.Background(), LaunchCommand{})
	}()
	// Release before joining, including on a fatal assertion. The watchdog
	// diagnoses a broken configure lifecycle; it is not an ordering delay.
	await := func(ch <-chan struct{}, phase string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Fatalf("fakeSpawner timed out waiting for %s", phase)
		}
	}
	t.Cleanup(func() { unblock(); await(done, "cleanup completion") })
	await(entered, "configure entry")
	if got := sp.spawnCount(); got != 0 {
		t.Errorf("spawnCount during configure = %d, want 0", got)
	}
	if got := sp.lastProcess(); got != nil {
		t.Error("lastProcess during configure is non-nil, want nil")
	}
	unblock()
	await(done, "spawn completion")
	if spawnErr != nil {
		t.Fatalf("spawn() = %v, want nil", spawnErr)
	}
	if got := sp.spawnCount(); got != 1 {
		t.Errorf("spawnCount after configure = %d, want 1", got)
	}
	p := sp.lastProcess()
	if p == nil || !p.ignoreSignals {
		t.Error("lastProcess after configure is not the configured child")
	}
}
