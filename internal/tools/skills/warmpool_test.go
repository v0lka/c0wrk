package skills

import (
	"context"
	"sync"
	"testing"
	"time"
)

// createTestManifest creates a test manifest with the given name.
func createTestManifest(name string) *SkillManifest {
	return &SkillManifest{
		Name:        name,
		Description: "Test skill " + name,
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "main.py",
	}
}

// TestWarmPool_NewWarmPool tests pool creation.
func TestWarmPool_NewWarmPool(t *testing.T) {
	builder := NewDockerBuilder()
	pool := NewWarmPool(builder, 5, time.Minute)

	if pool == nil {
		t.Fatal("NewWarmPool returned nil")
	}
	if pool.maxSize != 5 {
		t.Errorf("maxSize = %d, want 5", pool.maxSize)
	}
	if pool.idleTimeout != time.Minute {
		t.Errorf("idleTimeout = %v, want %v", pool.idleTimeout, time.Minute)
	}
	if pool.Size() != 0 {
		t.Errorf("Size() = %d, want 0", pool.Size())
	}
}

// TestWarmPool_Return tests updating lastUsed time.
func TestWarmPool_Return(t *testing.T) {
	builder := NewDockerBuilder()
	pool := NewWarmPool(builder, 5, time.Minute)

	// Return on non-existent entry should not panic
	pool.Return("nonexistent")

	// Size should still be 0
	if pool.Size() != 0 {
		t.Errorf("Size() = %d, want 0", pool.Size())
	}
}

// TestWarmPool_Size tests size tracking.
func TestWarmPool_Size(t *testing.T) {
	builder := NewDockerBuilder()
	pool := NewWarmPool(builder, 5, time.Minute)

	if pool.Size() != 0 {
		t.Errorf("initial Size() = %d, want 0", pool.Size())
	}

	// Manually add an entry to test Size
	pool.mu.Lock()
	pool.containers["test"] = &poolEntry{
		container: &SkillContainer{},
		skillDir:  "/tmp",
		lastUsed:  time.Now(),
	}
	pool.mu.Unlock()

	if pool.Size() != 1 {
		t.Errorf("Size() after add = %d, want 1", pool.Size())
	}
}

// TestWarmPool_StartStop tests the lifecycle methods.
func TestWarmPool_StartStop(t *testing.T) {
	builder := NewDockerBuilder()
	pool := NewWarmPool(builder, 5, 100*time.Millisecond)

	// Start should not panic
	pool.Start()

	// Give cleanup goroutine time to start
	time.Sleep(10 * time.Millisecond)

	// Stop should not panic and should clean up
	pool.Stop()

	// Pool should be empty after stop
	if pool.Size() != 0 {
		t.Errorf("Size() after Stop = %d, want 0", pool.Size())
	}
}

// TestWarmPool_StopCleansUp tests that Stop cleans up all containers.
func TestWarmPool_StopCleansUp(t *testing.T) {
	builder := NewDockerBuilder()
	pool := NewWarmPool(builder, 5, time.Minute)

	// Manually add entries to simulate cached containers
	pool.mu.Lock()
	for i := 0; i < 3; i++ {
		manifest := createTestManifest("skill" + string(rune('a'+i)))
		pool.containers[manifest.Name] = &poolEntry{
			container: NewSkillContainer(manifest, builder),
			skillDir:  "/tmp",
			lastUsed:  time.Now(),
		}
	}
	pool.mu.Unlock()

	if pool.Size() != 3 {
		t.Errorf("Size() before Stop = %d, want 3", pool.Size())
	}

	pool.Start()
	pool.Stop()

	if pool.Size() != 0 {
		t.Errorf("Size() after Stop = %d, want 0", pool.Size())
	}
}

// TestWarmPool_IdleCleanup tests that idle containers are removed.
func TestWarmPool_IdleCleanup(t *testing.T) {
	builder := NewDockerBuilder()
	idleTimeout := 50 * time.Millisecond
	pool := NewWarmPool(builder, 5, idleTimeout)

	// Add an entry with old lastUsed time
	pool.mu.Lock()
	manifest := createTestManifest("idle-skill")
	pool.containers[manifest.Name] = &poolEntry{
		container: NewSkillContainer(manifest, builder),
		skillDir:  "/tmp",
		lastUsed:  time.Now().Add(-2 * idleTimeout), // Already idle
	}
	pool.mu.Unlock()

	if pool.Size() != 1 {
		t.Errorf("Size() before cleanup = %d, want 1", pool.Size())
	}

	pool.Start()

	// Wait for cleanup to run (idleTimeout/2 interval)
	time.Sleep(idleTimeout)

	if pool.Size() != 0 {
		t.Errorf("Size() after cleanup = %d, want 0", pool.Size())
	}

	pool.Stop()
}

// TestWarmPool_MaxSize tests LRU eviction when pool is full.
func TestWarmPool_MaxSize(t *testing.T) {
	builder := NewDockerBuilder()
	pool := NewWarmPool(builder, 2, time.Minute)

	// Add entries manually (simulating successful builds)
	now := time.Now()
	pool.mu.Lock()

	// First entry (oldest)
	pool.containers["skill-a"] = &poolEntry{
		container: NewSkillContainer(createTestManifest("skill-a"), builder),
		skillDir:  "/tmp/a",
		lastUsed:  now.Add(-2 * time.Minute),
	}

	// Second entry (newer)
	pool.containers["skill-b"] = &poolEntry{
		container: NewSkillContainer(createTestManifest("skill-b"), builder),
		skillDir:  "/tmp/b",
		lastUsed:  now.Add(-1 * time.Minute),
	}
	pool.mu.Unlock()

	if pool.Size() != 2 {
		t.Errorf("Size() = %d, want 2", pool.Size())
	}

	// Trigger eviction by adding beyond maxSize
	pool.mu.Lock()
	if len(pool.containers) >= pool.maxSize {
		pool.evictLRU()
	}
	pool.containers["skill-c"] = &poolEntry{
		container: NewSkillContainer(createTestManifest("skill-c"), builder),
		skillDir:  "/tmp/c",
		lastUsed:  now,
	}
	pool.mu.Unlock()

	if pool.Size() != 2 {
		t.Errorf("Size() after eviction = %d, want 2", pool.Size())
	}

	// Verify oldest (skill-a) was evicted
	pool.mu.RLock()
	_, hasA := pool.containers["skill-a"]
	_, hasB := pool.containers["skill-b"]
	_, hasC := pool.containers["skill-c"]
	pool.mu.RUnlock()

	if hasA {
		t.Error("skill-a should have been evicted (LRU)")
	}
	if !hasB {
		t.Error("skill-b should still be in pool")
	}
	if !hasC {
		t.Error("skill-c should be in pool")
	}
}

// TestWarmPool_ConcurrentAccess tests thread safety.
func TestWarmPool_ConcurrentAccess(t *testing.T) {
	builder := NewDockerBuilder()
	pool := NewWarmPool(builder, 10, time.Minute)
	pool.Start()
	defer pool.Stop()

	var wg sync.WaitGroup
	numGoroutines := 10
	iterations := 100

	// Concurrent Size() calls
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = pool.Size()
			}
		}()
	}

	// Concurrent Return() calls
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				pool.Return("skill-" + string(rune('a'+id)))
			}
		}(i)
	}

	// Concurrent manual entry manipulation (simulating Get/Return)
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			name := "concurrent-skill-" + string(rune('a'+id))
			for j := 0; j < iterations/10; j++ {
				pool.mu.Lock()
				if _, exists := pool.containers[name]; !exists && len(pool.containers) < pool.maxSize {
					pool.containers[name] = &poolEntry{
						container: NewSkillContainer(createTestManifest(name), builder),
						skillDir:  "/tmp",
						lastUsed:  time.Now(),
					}
				}
				pool.mu.Unlock()
				pool.Return(name)
			}
		}(i)
	}

	// Wait with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent access test timed out - possible deadlock")
	}
}

// TestWarmPool_GetAndReturn_Cached tests cache hit scenario.
func TestWarmPool_GetAndReturn_Cached(t *testing.T) {
	builder := NewDockerBuilder()
	pool := NewWarmPool(builder, 5, time.Minute)

	manifest := createTestManifest("cached-skill")

	// Manually add a pre-built container to simulate cached state
	container := NewSkillContainer(manifest, builder)
	pool.mu.Lock()
	pool.containers[manifest.Name] = &poolEntry{
		container: container,
		skillDir:  "/tmp/skill",
		lastUsed:  time.Now().Add(-time.Second),
	}
	initialTime := pool.containers[manifest.Name].lastUsed
	pool.mu.Unlock()

	// Get should return cached container
	ctx := context.Background()
	got, err := pool.Get(ctx, manifest, "/tmp/skill")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got != container {
		t.Error("Get() should return cached container")
	}

	// lastUsed should be updated
	pool.mu.RLock()
	newTime := pool.containers[manifest.Name].lastUsed
	pool.mu.RUnlock()

	if !newTime.After(initialTime) {
		t.Error("Get() should update lastUsed time")
	}

	// Return should also update lastUsed
	pool.mu.Lock()
	pool.containers[manifest.Name].lastUsed = time.Now().Add(-time.Hour)
	pool.mu.Unlock()

	pool.Return(manifest.Name)

	pool.mu.RLock()
	returnTime := pool.containers[manifest.Name].lastUsed
	pool.mu.RUnlock()

	if time.Since(returnTime) > time.Second {
		t.Error("Return() should update lastUsed to now")
	}
}

// TestWarmPool_EvictLRU_EmptyPool tests eviction on empty pool.
func TestWarmPool_EvictLRU_EmptyPool(t *testing.T) {
	builder := NewDockerBuilder()
	pool := NewWarmPool(builder, 2, time.Minute)

	// evictLRU on empty pool should not panic
	pool.mu.Lock()
	pool.evictLRU()
	pool.mu.Unlock()

	if pool.Size() != 0 {
		t.Errorf("Size() = %d, want 0", pool.Size())
	}
}
