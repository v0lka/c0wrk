package core

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/orchestration"
)

// ErrMCPAuthorizationState identifies a failed durable authorization-state preparation.
// Callers must preserve the task and retry, never recover by starting a fresh task.
var ErrMCPAuthorizationState = errors.New("MCP authorization state unavailable; retry this task")

// prepareTaskMCP resolves durable task intent and snapshots current modes before
// any execution. The returned context is shared by the resume wave and main loop.
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
		// Explicitly nonpersistent callers retain task-local ephemeral intent.
		o.recordMCPMentions(taskID, extra)
		names = o.mcpMentionsFor(taskID)
		if taskID == "" {
			names = extra
		}
	}
	names = slices.Clone(names)
	modes := o.config.MCPServerModes
	if o.config.MCPServerModesResolver != nil {
		modes = o.config.MCPServerModesResolver()
	}
	ctx = WithUserMCPServers(ctx, names)
	return tools.WithGatedMCPServers(ctx, gatedMCPServerSet(modes, names)), nil
}
