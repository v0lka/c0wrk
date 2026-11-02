package core

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/orchestration"
)

// ErrMCPAuthorizationState marks a failed durable authorization-state
// preparation: preserve the task for retry, never fresh-start it
// (specs/contracts/backend-core.md).
var ErrMCPAuthorizationState = errors.New("MCP authorization state unavailable; retry this task")

// prepareTaskMCP resolves durable task intent and snapshots current modes
// before execution; the context is shared by the resume wave and main loop.
func (o *Orchestrator) prepareTaskMCP(ctx context.Context, bb orchestration.Blackboard, extra []string) (context.Context, error) {
	taskID := ""
	if pbb, ok := bb.(PersistableBlackboard); ok {
		taskID = pbb.TaskID()
	}
	var names []string
	if o.taskStore != nil {
		if taskID == "" {
			return ctx, fmt.Errorf("%w: missing persistent task id", ErrMCPAuthorizationState)
		}
		var err error
		names, err = o.taskStore.LoadMCPMentions(ctx, taskID)
		if err != nil {
			return ctx, fmt.Errorf("%w: load task mentions: %w", ErrMCPAuthorizationState, err)
		}
		if len(extra) > 0 {
			if err := o.taskStore.PersistMCPMentions(ctx, taskID, extra); err != nil {
				return ctx, fmt.Errorf("%w: persist task mentions: %w", ErrMCPAuthorizationState, err)
			}
			// Reload the committed union, including concurrent accepted additions.
			names, err = o.taskStore.LoadMCPMentions(ctx, taskID)
			if err != nil {
				return ctx, fmt.Errorf("%w: reload task mentions: %w", ErrMCPAuthorizationState, err)
			}
		}
	} else {
		// Nonpersistent callers: task-local ephemeral intent only.
		o.recordMCPMentions(taskID, extra)
		names = o.mcpMentionsFor(taskID)
		if taskID == "" {
			names = extra
		}
	}
	names = slices.Clone(names)
	// The mode snapshot is taken once so reconfiguration cannot change a
	// running task's authorization; the session context picks chat vs code
	// per server (specs/contracts/backend-core.md).
	modes := capabilityModesForSession(o.currentMCPCapabilityModes(), o.sessionCapabilityContext())
	ctx = WithUserMCPServers(ctx, names)
	return tools.WithGatedMCPServers(ctx, gatedMCPServerSet(modes, names)), nil
}
