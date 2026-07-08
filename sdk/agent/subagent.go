package agent

import (
	"context"
	"errors"
	"time"

	"github.com/v0lka/sp4rk/tools"
)

// SubAgentTask bundles an agent with its task tools, context manager, and events.
type SubAgentTask struct {
	StepID         string
	Executor       *Executor
	CM             ContextManager
	TaskTools      []tools.ToolDescriptor
	TaskDesc       string             // task description (for SubAgentLaunch event)
	Emitter        Events             // event emitter (nil-safe)
	TodoUpdateFunc StepTodoUpdateFunc // optional callback for update_checklist tool
}

// RunSubAgent starts the executor in a goroutine and returns a channel for the result.
// The goroutine respects context cancellation — when ctx is cancelled,
// executor.Run will return because its LLM calls and tool executions use the same context.
// emitter is optional (nil-safe) for console output.
func RunSubAgent(ctx context.Context, stepID string, executor *Executor, cm ContextManager, taskTools []tools.ToolDescriptor, taskDesc string, emitter Events, todoUpdateFunc StepTodoUpdateFunc) (resultCh <-chan SubAgentResult) {
	// Use NoopEvents if nil to avoid nil checks
	if emitter == nil {
		emitter = &NoopEvents{}
	}
	ch := make(chan SubAgentResult, 1)

	go func() {
		defer close(ch)

		// Emit subagent launch
		emitter.SubAgentLaunch(stepID, taskDesc)
		startTime := time.Now()

		// Set task context for tool execution
		ctx = tools.WithTaskContext(ctx, taskDesc)

		// Set step ID so file tracker and other context-aware tools know the current step
		ctx = WithStepID(ctx, stepID)

		// Set checklist update callback for update_checklist tool
		if todoUpdateFunc != nil {
			ctx = WithStepTodoUpdateFunc(ctx, todoUpdateFunc)
		}

		result, err := executor.Run(ctx, taskTools, cm)

		duration := time.Since(startTime)
		success := err == nil && result.Finished

		// Defense-in-depth: even if the executor returned Finished=true, a
		// failure-mode where the model printed tool-call syntax as text
		// (instead of emitting a tool_use block) is NOT a success. The
		// handleImplicitFinish detector should have aborted such cases with
		// Finished=false, but this guard catches any escape.
		if success && DetectToolCallSyntaxInContent(result.Output) {
			success = false
			if err == nil {
				err = errors.New("model printed tool-call syntax as text instead of using tool_use blocks")
			}
		}

		// Emit subagent complete
		emitter.SubAgentComplete(stepID, success, duration)

		if err != nil {
			var steps []Step
			if result != nil {
				steps = result.Steps
			}
			ch <- SubAgentResult{StepID: stepID, Steps: steps, Error: err}
			return
		}

		// Treat incomplete execution (no proper finish) as a step failure.
		// Use the executor's output as the error message when available — it contains
		// the specific abort reason (e.g. circuit breaker, fruitless abort, max steps).
		if !result.Finished {
			errMsg := "step execution did not complete within max steps"
			if result.Output != "" {
				errMsg = result.Output
			}
			ch <- SubAgentResult{StepID: stepID, Output: result.Output, Steps: result.Steps, Error: errors.New(errMsg)}
			return
		}

		ch <- SubAgentResult{
			StepID: stepID,
			Output: result.Output,
			Steps:  result.Steps,
		}
	}()

	return ch
}

// RunSubAgentsParallel runs multiple SubAgents concurrently and collects results.
// Returns results in input order (not completion order); a slow agent blocks
// all subsequent results from being returned.
func RunSubAgentsParallel(ctx context.Context, agents []SubAgentTask) (results []SubAgentResult) {
	if len(agents) == 0 {
		return nil
	}

	// Launch all agents and collect their channels
	channels := make([]<-chan SubAgentResult, len(agents))
	for i, ag := range agents {
		channels[i] = RunSubAgent(ctx, ag.StepID, ag.Executor, ag.CM, ag.TaskTools, ag.TaskDesc, ag.Emitter, ag.TodoUpdateFunc)
	}

	// Collect all results
	results = make([]SubAgentResult, 0, len(agents))
	for _, ch := range channels {
		result := <-ch
		results = append(results, result)
	}

	return results
}
