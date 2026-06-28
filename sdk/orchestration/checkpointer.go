package orchestration

import (
	"context"
)

// Checkpointer provides persistence for blackboard state.
// Implementations are responsible for serialization format and backend storage.
// All methods must be safe for concurrent use.
type Checkpointer interface {
	// SaveCheckpoint persists the full blackboard state under the given ID.
	SaveCheckpoint(ctx context.Context, id string, bb Blackboard) error
	// LoadCheckpoint restores a blackboard from persistence.
	// Returns nil, nil if the checkpoint does not exist.
	LoadCheckpoint(ctx context.Context, id string) (Blackboard, error)
	// DeleteCheckpoint removes a checkpoint from persistence.
	DeleteCheckpoint(ctx context.Context, id string) error
}
