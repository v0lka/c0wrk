package core

// This file re-exports types and functions from sdk/agent for backward compatibility.
// These will be removed in a future cleanup.

import (
	"context"

	"github.com/user/agent/sdk/agent"
)

// Executor runs the ReAct loop: Thought → Action → Observation.
type Executor = agent.Executor

// SubAgent wraps an Executor to run as a goroutine for parallel plan execution.
type SubAgent = agent.SubAgent

// SubAgentTask bundles an agent with its task tools, context manager, and events.
type SubAgentTask = agent.SubAgentTask

// FinishTool is a special tool that signals task completion.
type FinishTool = agent.FinishTool

// NewExecutor creates a new Executor.
var NewExecutor = agent.NewExecutor

// NewSubAgent creates a SubAgent for a specific plan step.
var NewSubAgent = agent.NewSubAgent

// RunSubAgentsParallel runs multiple SubAgents concurrently and collects results.
var RunSubAgentsParallel = agent.RunSubAgentsParallel

// NewFinishTool creates a new FinishTool.
var NewFinishTool = agent.NewFinishTool

// BuildGroupedToolList formats tool descriptors into a tiered text block.
var BuildGroupedToolList = agent.BuildGroupedToolList

// buildGroupedToolList is kept as an unexported alias for backward compatibility with tests.
var buildGroupedToolList = agent.BuildGroupedToolList

// RunSubAgent is a backward-compatible wrapper around agent.RunSubAgent.
// It accepts a TaskDefinition (c0wrk-specific) and extracts tools/description for the SDK call.
func RunSubAgent(ctx context.Context, stepID string, executor *Executor, cm ContextManager, task TaskDefinition, emitter Emitter) <-chan SubAgentResult {
	return agent.RunSubAgent(ctx, stepID, executor, cm, task.Tools, task.Task, emitter)
}
