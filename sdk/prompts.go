package sp4rk

import "github.com/v0lka/sp4rk/planner"

// defaultBasePrompt is a general-purpose planner base prompt. It uses bare
// placeholders resolved by the planner's substitution engine:
//   - Trusted (template-on-template): MODE-PREAMBLE, MAX-STEPS
//   - Untrusted (single-pass, injection-safe): AVAILABLE-TOOLS, AVAILABLE-SKILLS
//
// MODE-JSON-EXAMPLE and MODE-TAIL are injected by the planner from package
// constants, so they need no PromptSet field.
const defaultBasePrompt = `You are a task planning agent. Break the task into concrete, verifiable steps.

Available tools:
AVAILABLE-TOOLS

Available skills:
AVAILABLE-SKILLS

MODE-PREAMBLE

Create at most MAX-STEPS steps. Each step needs a clear summary, description, and verifiable acceptance criteria. Use depends_on to express ordering between steps; mark parallelizable steps when they have no dependencies on each other.

MODE-JSON-EXAMPLE`

// DefaultPromptSet returns a general-purpose [planner.PromptSet] suitable for a
// zero-config planner. The prompts are domain-agnostic (they do not assume a
// specific workspace, language, or framework) so they work for any sp4rk
// consumer. Override individual fields after calling it to customize.
//
//	cfg := planner.DefaultConfig()
//	cfg.Prompts = sp4rk.DefaultPromptSet()
//	cfg.Model = "claude-sonnet-4-5"
func DefaultPromptSet() planner.PromptSet {
	return planner.PromptSet{
		BasePrompt: defaultBasePrompt,

		// Multi-step mode (the common case).
		PlanPreamble:      "Break the task into sequential steps with clear deliverables. Order steps so each builds on the previous one's output.",
		MultiStepGuidance: "Each step should produce a verifiable artifact (a file, a test result, a confirmed state). Prefer fewer, well-scoped steps over many tiny ones.",

		// Single-step mode (when the task is simple enough for one step).
		SingleStepPreamble: "This task is simple enough to complete in a single step. Produce one step that fully addresses the request.",
		SingleStepGuidance: "The single step should be self-contained and produce a complete, verifiable result.",

		// Replan mode (after a failure + reflection).
		ReplanPrompt: `You are a task planning agent revising a plan after a step failed.

Available tools:
AVAILABLE-TOOLS

MODE-PREAMBLE

The previous plan and the reflection analysis are provided. Produce a revised plan that avoids the failure. Keep completed steps as-is; only change steps that have not yet succeeded or that depended on the failed step.

MODE-JSON-EXAMPLE`,

		// Continuation mode (follow-up message within an existing plan).
		ContinuationPreamble:           "Continue the existing plan, incorporating the new message. Preserve completed steps and adjust remaining steps as needed.",
		ContinuationIncompletePreamble: "The previous plan was incomplete. Revise the remaining steps to finish the task, incorporating the new message.",
		ContinuationSingleStep:         "Handle the new message as a single continuation step that completes the request.",

		// Appended to every planner prompt before the cache-break boundary.
		VerificationMandate: "Before finalizing the plan, verify each step's acceptance criteria are concrete and independently checkable.",
	}
}

// DefaultReflectorPrompt returns a general-purpose system prompt for a
// [reflector.Reflector]. It instructs the model to analyze a failed execution
// trajectory and return structured self-correction guidance (retry, replan, or
// abort).
func DefaultReflectorPrompt() string {
	return `You are a reflection agent. Analyze the failed execution trajectory and return JSON with these fields:
- summary: a short description of what went wrong
- root_cause: the underlying reason for the failure
- suggested_action: one of "retry", "replan", or "abort"
- action_plan: concrete corrective steps to take

Choose "retry" when the failure was transient or the approach was correct but execution stumbled. Choose "replan" when the plan itself was flawed. Choose "abort" only when the task is infeasible as stated.`
}
