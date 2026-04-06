package agent

import (
	"sync"
	"time"
)

// Artifact represents a named output produced by an agent step.
type Artifact struct {
	Key        string    `json:"key"`
	Content    string    `json:"content"`
	ProducedBy string    `json:"produced_by"` // step ID that created it
	CreatedAt  time.Time `json:"created_at"`
}

// SharedWorkspace provides inter-agent communication via named artifacts.
// It is safe for concurrent use.
type SharedWorkspace struct {
	mu        sync.RWMutex
	artifacts map[string]Artifact
}

// NewSharedWorkspace creates a new empty SharedWorkspace.
func NewSharedWorkspace() *SharedWorkspace {
	return &SharedWorkspace{
		artifacts: make(map[string]Artifact),
	}
}

// Store saves an artifact in the workspace.
func (sw *SharedWorkspace) Store(key, content, producedBy string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.artifacts[key] = Artifact{
		Key:        key,
		Content:    content,
		ProducedBy: producedBy,
		CreatedAt:  time.Now(),
	}
}

// Get retrieves an artifact by key.
func (sw *SharedWorkspace) Get(key string) (Artifact, bool) {
	sw.mu.RLock()
	defer sw.mu.RUnlock()
	a, ok := sw.artifacts[key]
	return a, ok
}

// List returns all artifacts.
func (sw *SharedWorkspace) List() []Artifact {
	sw.mu.RLock()
	defer sw.mu.RUnlock()
	result := make([]Artifact, 0, len(sw.artifacts))
	for _, a := range sw.artifacts {
		result = append(result, a)
	}
	return result
}

// GetByProducer returns all artifacts produced by a specific step.
func (sw *SharedWorkspace) GetByProducer(stepID string) []Artifact {
	sw.mu.RLock()
	defer sw.mu.RUnlock()
	var result []Artifact
	for _, a := range sw.artifacts {
		if a.ProducedBy == stepID {
			result = append(result, a)
		}
	}
	return result
}

// Clear removes all artifacts from the workspace.
func (sw *SharedWorkspace) Clear() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.artifacts = make(map[string]Artifact)
}
