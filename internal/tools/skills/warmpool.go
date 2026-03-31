package skills

import (
	"context"
	"sync"
	"time"
)

// WarmPool maintains a pool of pre-built SkillContainers for faster skill execution.
type WarmPool struct {
	mu          sync.RWMutex
	containers  map[string]*poolEntry
	builder     *DockerBuilder
	maxSize     int
	idleTimeout time.Duration
	stopCh      chan struct{}
	wg          sync.WaitGroup
}

// poolEntry represents a cached container with metadata.
type poolEntry struct {
	container *SkillContainer
	skillDir  string
	lastUsed  time.Time
}

// NewWarmPool creates a new WarmPool with the given configuration.
func NewWarmPool(builder *DockerBuilder, maxSize int, idleTimeout time.Duration) *WarmPool {
	return &WarmPool{
		containers:  make(map[string]*poolEntry),
		builder:     builder,
		maxSize:     maxSize,
		idleTimeout: idleTimeout,
		stopCh:      make(chan struct{}),
	}
}

// Get returns a cached SkillContainer or creates a new one.
func (wp *WarmPool) Get(ctx context.Context, manifest *SkillManifest, skillDir string) (*SkillContainer, error) {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	// Check if container already exists in pool
	if entry, ok := wp.containers[manifest.Name]; ok {
		entry.lastUsed = time.Now()
		return entry.container, nil
	}

	// Check if we need to evict to make room
	if len(wp.containers) >= wp.maxSize {
		wp.evictLRU()
	}

	// Create new container
	container := NewSkillContainer(manifest, wp.builder)
	if err := container.Build(ctx, skillDir); err != nil {
		return nil, err
	}

	// Add to pool
	wp.containers[manifest.Name] = &poolEntry{
		container: container,
		skillDir:  skillDir,
		lastUsed:  time.Now(),
	}

	return container, nil
}

// Return marks a container as available (updates lastUsed).
func (wp *WarmPool) Return(name string) {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	if entry, ok := wp.containers[name]; ok {
		entry.lastUsed = time.Now()
	}
}

// Start begins the background cleanup goroutine.
func (wp *WarmPool) Start() {
	wp.wg.Add(1)
	go wp.cleanupLoop()
}

// Stop shuts down the pool and all containers.
func (wp *WarmPool) Stop() {
	close(wp.stopCh)
	wp.wg.Wait()

	wp.mu.Lock()
	defer wp.mu.Unlock()

	// Stop all containers
	for name, entry := range wp.containers {
		_ = entry.container.Stop()
		delete(wp.containers, name)
	}
}

// Size returns current pool size.
func (wp *WarmPool) Size() int {
	wp.mu.RLock()
	defer wp.mu.RUnlock()
	return len(wp.containers)
}

// cleanupLoop periodically removes idle containers.
func (wp *WarmPool) cleanupLoop() {
	defer wp.wg.Done()

	ticker := time.NewTicker(wp.idleTimeout / 2)
	defer ticker.Stop()

	for {
		select {
		case <-wp.stopCh:
			return
		case <-ticker.C:
			wp.cleanupIdle()
		}
	}
}

// cleanupIdle removes containers that have been idle for too long.
func (wp *WarmPool) cleanupIdle() {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	now := time.Now()
	for name, entry := range wp.containers {
		if now.Sub(entry.lastUsed) > wp.idleTimeout {
			_ = entry.container.Stop()
			delete(wp.containers, name)
		}
	}
}

// evictLRU removes the least recently used entry from the pool.
// Must be called with lock held.
func (wp *WarmPool) evictLRU() {
	var lruName string
	var lruTime time.Time

	for name, entry := range wp.containers {
		if lruName == "" || entry.lastUsed.Before(lruTime) {
			lruName = name
			lruTime = entry.lastUsed
		}
	}

	if lruName != "" {
		if entry, ok := wp.containers[lruName]; ok {
			_ = entry.container.Stop()
			delete(wp.containers, lruName)
		}
	}
}
