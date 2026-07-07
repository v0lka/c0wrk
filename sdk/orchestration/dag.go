package orchestration

import (
	"fmt"
	"strings"

	"github.com/v0lka/sp4rk/agent"
)

// FindReadySteps returns plan steps whose dependencies are all completed successfully.
// Steps with failed dependencies or already-completed steps are not included.
func FindReadySteps(plan *Plan, completed map[string]CompletedStep) []PlanStep {
	if plan == nil {
		return nil
	}
	ready := []PlanStep{}
	for _, step := range plan.Steps {
		// Skip if already completed
		if _, done := completed[step.ID]; done {
			continue
		}

		// Check if all dependencies are completed successfully
		allDepsComplete := true
		for _, depID := range step.DependsOn {
			cs, done := completed[depID]
			if !done || cs.Error != nil {
				allDepsComplete = false
				break
			}
		}

		if allDepsComplete {
			ready = append(ready, step)
		}
	}
	return ready
}

// BuildCarryForward maps previously completed step outputs to a new plan.
// Steps whose IDs appear in the new plan AND completed without error are
// candidates for carry-forward. Steps whose dependencies (in the new plan)
// include a step that is NOT carried forward are transitively excluded,
// ensuring that a replanned step invalidates all its downstream dependents.
// Returns nil if no steps can be preserved (triggering full re-execution).
func BuildCarryForward(completed []CompletedStep, newPlan *Plan) map[string]CompletedStep {
	if newPlan == nil {
		return nil
	}
	newStepIDs := make(map[string]bool, len(newPlan.Steps))
	for _, s := range newPlan.Steps {
		newStepIDs[s.ID] = true
	}

	// Phase 1: collect candidates — old steps that match new plan IDs and had no error.
	carried := make(map[string]CompletedStep)
	for _, cs := range completed {
		if newStepIDs[cs.StepID] && cs.Error == nil {
			carried[cs.StepID] = cs
		}
	}

	// Phase 2: transitively remove steps whose dependencies won't be carried forward.
	// If a step depends on a new-plan step that is NOT in `carried`, the dependency
	// will be re-executed, so this step's prior output is stale and must be re-run.
	changed := true
	for changed {
		changed = false
		for _, s := range newPlan.Steps {
			if _, ok := carried[s.ID]; !ok {
				continue // not a candidate
			}
			for _, depID := range s.DependsOn {
				if newStepIDs[depID] {
					if _, depCarried := carried[depID]; !depCarried {
						// Dependency exists in new plan but is not carried forward.
						delete(carried, s.ID)
						changed = true
						break
					}
				}
			}
		}
	}

	if len(carried) == 0 {
		return nil
	}
	return carried
}

// BuildPlanExecutionSteps converts completed plan steps into an execution
// trajectory ([]agent.Step) for reflectors and evaluators. When a CompletedStep
// carries the actual executor steps (tool calls + observations), those are
// used directly so the evaluator sees real evidence. Otherwise, a fallback
// summary step is created from the completion output.
func BuildPlanExecutionSteps(completedList []CompletedStep, plan *Plan) []agent.Step {
	if plan == nil {
		return nil
	}
	// Build a map from step ID to plan step description
	stepDescriptions := make(map[string]string)
	for _, ps := range plan.Steps {
		stepDescriptions[ps.ID] = ps.Description
	}

	var steps []agent.Step
	for _, cs := range completedList {
		if len(cs.Steps) > 0 {
			// Use actual executor steps (preserves tool calls + observations)
			steps = append(steps, cs.Steps...)
		} else {
			// Fallback: constructed summary when executor steps are unavailable
			desc := stepDescriptions[cs.StepID]
			step := agent.Step{
				Thought: fmt.Sprintf("Executing plan step %s: %s", cs.StepID, desc),
			}
			if cs.Error != nil {
				step.Observation = fmt.Sprintf("STEP FAILED: %s\nOutput: %s", cs.Error.Error(), cs.Output)
			} else {
				step.Observation = cs.Output
			}
			steps = append(steps, step)
		}
	}
	return steps
}

// AggregateOutput combines outputs from terminal steps (steps that no other step
// depends on). If no terminal outputs exist, all step outputs are collected instead.
// preCompletedIDs, when non-nil, lists step IDs that were pre-completed from a
// previous turn's blackboard; these are excluded from output aggregation so that
// continuation messages only return newly produced output.
func AggregateOutput(completedSteps map[string]CompletedStep, plan *Plan, preCompletedIDs map[string]bool) string {
	if plan == nil {
		return ""
	}
	// Find terminal steps (steps that no other step depends on)
	dependedUpon := make(map[string]bool)
	for _, step := range plan.Steps {
		for _, depID := range step.DependsOn {
			dependedUpon[depID] = true
		}
	}

	// Collect outputs from terminal steps, skipping pre-completed ones
	var outputs []string
	for _, step := range plan.Steps {
		if preCompletedIDs[step.ID] {
			continue
		}
		if !dependedUpon[step.ID] {
			if completed, ok := completedSteps[step.ID]; ok && completed.Error == nil {
				outputs = append(outputs, completed.Output)
			}
		}
	}

	// If no terminal outputs, collect all outputs (still excluding pre-completed)
	if len(outputs) == 0 {
		for _, step := range plan.Steps {
			if preCompletedIDs[step.ID] {
				continue
			}
			if completed, ok := completedSteps[step.ID]; ok && completed.Error == nil {
				outputs = append(outputs, completed.Output)
			}
		}
	}

	return strings.Join(outputs, "\n\n")
}
