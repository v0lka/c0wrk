package core

import (
	"context"
	"errors"
	"time"
)

// SubAgent wraps an Executor to run as a goroutine for parallel plan execution.
type SubAgent struct {
	id       string
	executor *Executor
}

// NewSubAgent creates a SubAgent for a specific plan step.
func NewSubAgent(id string, executor *Executor) *SubAgent {
	return &SubAgent{
		id:       id,
		executor: executor,
	}
}

// SubAgentTask bundles an agent with its task and context manager.
type SubAgentTask struct {
	StepID   string
	Executor *Executor
	CM       ContextManager
	Task     TaskDefinition
	Emitter  Emitter // event emitter (nil-safe)
}

// RunSubAgent starts the executor in a goroutine and returns a channel for the result.
// The goroutine respects context cancellation — when ctx is cancelled,
// executor.Run will return because its LLM calls and tool executions use the same context.
// emitter is optional (nil-safe) for console output.
func RunSubAgent(ctx context.Context, stepID string, executor *Executor, cm ContextManager, task TaskDefinition, emitter Emitter) <-chan SubAgentResult {
	// Use noopEmitter if nil to avoid nil checks
	if emitter == nil {
		emitter = &noopEmitter{}
	}
	ch := make(chan SubAgentResult, 1)

	go func() {
		defer close(ch)

		// Emit subagent launch
		emitter.SubAgentLaunch(stepID, task.Task)
		startTime := time.Now()

		result, err := executor.Run(ctx, task, cm)

		duration := time.Since(startTime)
		success := err == nil && result.Finished

		// Emit subagent complete
		emitter.SubAgentComplete(stepID, success, duration)

		if err != nil {
			ch <- SubAgentResult{StepID: stepID, Error: err}
			return
		}

		// Treat max steps exhaustion (no proper finish) as a step failure
		if !result.Finished {
			ch <- SubAgentResult{StepID: stepID, Output: result.Output, Error: errors.New("step execution did not complete within max steps")}
			return
		}

		ch <- SubAgentResult{
			StepID: stepID,
			Output: result.Output,
		}
	}()

	return ch
}

// RunSubAgentsParallel runs multiple SubAgents concurrently and collects results.
// Returns results in the order they complete (not necessarily input order).
func RunSubAgentsParallel(ctx context.Context, agents []SubAgentTask) []SubAgentResult {
	if len(agents) == 0 {
		return nil
	}

	// Launch all agents and collect their channels
	channels := make([]<-chan SubAgentResult, len(agents))
	for i, agent := range agents {
		channels[i] = RunSubAgent(ctx, agent.StepID, agent.Executor, agent.CM, agent.Task, agent.Emitter)
	}

	// Collect all results
	results := make([]SubAgentResult, 0, len(agents))
	for _, ch := range channels {
		result := <-ch
		results = append(results, result)
	}

	return results
}
