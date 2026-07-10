package sp4rk

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/reflector"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/planner"
	"github.com/v0lka/sp4rk/skills"
	"github.com/v0lka/sp4rk/tools"
)

// errTaskNoSystem is returned by [TaskBuilder.Execute] when no system prompt
// was configured.
var errTaskNoSystem = errors.New("TaskF: system prompt is required — use .System(...) or .SystemFactory(...)")

// trajectoryStore is a minimal [agent.TrajectoryStore] capturing the last
// synced step sequence, used to feed the reflector after a failed attempt.
type trajectoryStore struct {
	mu    sync.Mutex
	steps []agent.Step
}

func (s *trajectoryStore) Sync(steps []agent.Step) {
	s.mu.Lock()
	s.steps = steps
	s.mu.Unlock()
}

func (s *trajectoryStore) Steps() []agent.Step {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.steps
}

// TaskBuilder is a fluent builder for orchestrated task execution (the
// Plan→Execute→Reflect loop). Create one with [Framework.TaskF]; terminate the
// chain with [TaskBuilder.Execute].
//
// Without [TaskBuilder.Plan], Execute runs a single ReAct loop (like [Run] but
// returning an orchestrated result). With [TaskBuilder.Plan], Execute builds a
// DAG and runs it with retry and (optionally) reflection.
type TaskBuilder struct {
	ctx  context.Context
	fw   *Framework
	task string

	system   string
	systemFn orchestration.SystemPromptFactory

	events orchestration.Events

	usePlanner      bool
	customPlanner   *planner.Planner
	useReflector    bool
	customReflector *reflector.Reflector

	maxRetries int

	planModel string
	execModel string

	workspace  string
	skills     []skills.SkillDescriptor
	compaction string

	err error // set by pipeline transition when the framework build failed
}

// TaskF starts a fluent orchestrated execution over fw for the given task. The
// chain is terminated by [TaskBuilder.Execute], which returns the original
// [*orchestration.ExecutionResult].
//
// TaskF is the fluent counterpart of a hand-rolled Plan→Execute→Reflect loop:
// without [TaskBuilder.Plan] it runs a single ReAct loop, with [TaskBuilder.Plan]
// it builds a DAG with retry and (optionally) reflection.
func (fw *Framework) TaskF(ctx context.Context, task string) *TaskBuilder {
	return &TaskBuilder{
		ctx:        ctx,
		fw:         fw,
		task:       task,
		maxRetries: 2,
		compaction: "sliding_window",
	}
}

// Task is a pipeline transition on [FrameworkBuilder]: it builds the framework
// from the accumulated configuration and starts a [TaskBuilder] in one step.
//
// Use this for single-use scripts that don't need to retain the [*Framework]
// handle (there is no defer Shutdown). If the build fails, the error is surfaced
// by [TaskBuilder.Execute] instead of panicking.
//
//	sp4rk.NewF().Anthropic(key, model).
//	    FileTools().Task(ctx, task).
//	    System("...").Plan().Reflect().Execute()
func (b *FrameworkBuilder) Task(ctx context.Context, task string) *TaskBuilder {
	fw, err := b.build()
	if err != nil {
		return &TaskBuilder{
			ctx:        ctx,
			task:       task,
			maxRetries: 2,
			compaction: "sliding_window",
			err:        err,
		}
	}
	return fw.TaskF(ctx, task)
}

// System sets a static system prompt for step execution.
func (b *TaskBuilder) System(prompt string) *TaskBuilder {
	b.system = prompt
	return b
}

// SystemFactory sets a [orchestration.SystemPromptFactory] for dynamic prompts.
func (b *TaskBuilder) SystemFactory(fn orchestration.SystemPromptFactory) *TaskBuilder {
	b.systemFn = fn
	return b
}

// Events sets the orchestration lifecycle event sink. Defaults to
// [orchestration.NoopEvents].
func (b *TaskBuilder) Events(e orchestration.Events) *TaskBuilder {
	b.events = e
	return b
}

// Plan enables the planner, configured with [DefaultPromptSet] and the
// framework's active model. For a custom planner, use [TaskBuilder.Planner].
func (b *TaskBuilder) Plan() *TaskBuilder {
	b.usePlanner = true
	return b
}

// Planner injects a custom [planner.Planner] (implies [TaskBuilder.Plan]).
func (b *TaskBuilder) Planner(p *planner.Planner) *TaskBuilder {
	b.usePlanner = true
	b.customPlanner = p
	return b
}

// Reflect enables the reflector, configured with [DefaultReflectorPrompt]. For
// a custom reflector, use [TaskBuilder.Reflector].
func (b *TaskBuilder) Reflect() *TaskBuilder {
	b.useReflector = true
	return b
}

// Reflector injects a custom [reflector.Reflector] (implies [TaskBuilder.Reflect]).
func (b *TaskBuilder) Reflector(r *reflector.Reflector) *TaskBuilder {
	b.useReflector = true
	b.customReflector = r
	return b
}

// MaxRetries sets the per-step retry budget (default 2). 0 disables retries.
func (b *TaskBuilder) MaxRetries(n int) *TaskBuilder {
	b.maxRetries = n
	return b
}

// Models configures runtime model switching: planModel for planning/reflection,
// execModel for step execution. The router is restored to planModel after
// execution. Use when you want a strong-reasoning model for planning and a
// faster/cheaper one for execution.
func (b *TaskBuilder) Models(planModel, execModel string) *TaskBuilder {
	b.planModel = planModel
	b.execModel = execModel
	return b
}

// Workspace attaches the workspace path to the execution context (via
// [tools.WithWorkspacePath]) so tools resolve relative paths correctly.
func (b *TaskBuilder) Workspace(dir string) *TaskBuilder {
	b.workspace = dir
	return b
}

// Skills sets the skill descriptors available to the planner.
func (b *TaskBuilder) Skills(s []skills.SkillDescriptor) *TaskBuilder {
	b.skills = s
	return b
}

// Compaction sets the context compaction strategy passed to the conductor
// (default "sliding_window").
func (b *TaskBuilder) Compaction(strategy string) *TaskBuilder {
	b.compaction = strategy
	return b
}

// Execute runs the task and returns the original [*orchestration.ExecutionResult].
//
// Without [TaskBuilder.Plan]: a single ReAct loop.
// With [TaskBuilder.Plan]: a DAG executed with retry and (if enabled)
// reflection, exactly as the full Plan→Execute→Reflect loop in the classic API.
func (b *TaskBuilder) Execute() (*orchestration.ExecutionResult, error) {
	if b.err != nil {
		return nil, b.err
	}
	if b.system == "" && b.systemFn == nil {
		return nil, errTaskNoSystem
	}

	if b.events == nil {
		b.events = &orchestration.NoopEvents{}
	}

	ctx := b.ctx
	if b.workspace != "" {
		ctx = tools.WithWorkspacePath(ctx, b.workspace)
	}

	systemFn := b.systemFn
	if systemFn == nil {
		prompt := b.system
		systemFn = func(_ context.Context, _ string, _ llm.ModelMetadata) string { return prompt }
	}

	conductor, err := b.fw.NewConductor(systemFn)
	if err != nil {
		return nil, fmt.Errorf("TaskF: %w", err)
	}
	defer conductor.Cleanup()

	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest(b.task)
	availableTools := b.fw.ToolRegistry().List()

	if !b.usePlanner {
		// Single ReAct loop — no DAG.
		res, runErr := conductor.Run(ctx, b.task, bb, availableTools, b.events, b.compaction)
		if runErr != nil {
			return nil, fmt.Errorf("TaskF: %w", runErr)
		}
		return res, nil
	}

	return b.runPlanned(ctx, conductor, bb, availableTools)
}

// runPlanned builds a plan and executes the DAG with retry + reflection.
func (b *TaskBuilder) runPlanned(
	ctx context.Context,
	conductor *orchestration.Conductor,
	bb orchestration.Blackboard,
	availableTools []tools.ToolDescriptor,
) (*orchestration.ExecutionResult, error) {
	router := b.fw.LLMRouter()

	// Resolve the planner.
	pl, err := b.resolvePlanner(ctx)
	if err != nil {
		return nil, err
	}

	// Resolve the reflector (may be nil).
	rf := b.resolveReflector()

	// Plan on the planning model.
	if b.planModel != "" {
		if err := router.SetModel(ctx, b.planModel); err != nil {
			return nil, fmt.Errorf("TaskF: switch to plan model %q: %w", b.planModel, err)
		}
	}

	plan, err := pl.Plan(ctx, b.task, availableTools, nil, b.skills, false, nil)
	if err != nil {
		return nil, fmt.Errorf("TaskF: plan: %w", err)
	}
	b.events.OnPlanGenerated(len(plan.Steps), planStepsToEvents(plan))
	bb.SetPlan(plan)

	// Execute on the execution model.
	if b.execModel != "" {
		if err := router.SetModel(ctx, b.execModel); err != nil {
			return nil, fmt.Errorf("TaskF: switch to exec model %q: %w", b.execModel, err)
		}
	}

	completed := make(map[string]orchestration.CompletedStep)
	var reflections []orchestration.Reflection
	aborted := false

	for {
		ready := orchestration.FindReadySteps(plan, completed)
		if len(ready) == 0 {
			break
		}
		var replanPlan *orchestration.Plan
		for _, step := range ready {
			b.events.OnStepStarted(step.ID, step.Description, step.Summary)
			b.runStep(ctx, conductor, bb, availableTools, pl, step, completed, rf, &reflections, &aborted, &replanPlan)
			if aborted || replanPlan != nil {
				break
			}
		}
		if aborted {
			break
		}
		if replanPlan != nil {
			// Reflection flagged a plan-level flaw: adopt the re-derived plan,
			// carrying forward prior successful work, and restart the DAG over
			// the new steps.
			plan = replanPlan
			completed = orchestration.BuildCarryForward(completedInOrder(completed, bb.GetPlan()), plan)
			bb.SetPlan(plan)
			b.events.OnPlanGenerated(len(plan.Steps), planStepsToEvents(plan))
			continue
		}
	}

	// Restore the planning model for any subsequent calls.
	if b.execModel != "" && b.planModel != "" {
		b.switchModelMidLoop(ctx, b.planModel)
	}

	// Derive terminal status from the completed set. See planCompletionStatus
	// for the precedence rules (aborted > failed > partial > success) and the
	// partial/cycle detection.
	status, failed, execErr := planCompletionStatus(completed, plan, aborted)

	return &orchestration.ExecutionResult{
		Output:      orchestration.AggregateOutput(completed, plan, nil),
		Plan:        plan,
		Blackboard:  bb,
		Reflections: reflections,
		Status:      status,
		FailedSteps: failed,
	}, execErr
}

// planCompletionStatus derives the terminal ExecutionStatus — and, when the
// plan is incomplete, a wrapped ErrExecutionIncomplete — from the set of
// completed steps. It also returns the count of failed steps for the
// ExecutionResult.FailedSteps field.
//
// Ordering matters: aborted takes precedence (the user/reflector halted
// execution), then failed (at least one step was attempted and exhausted its
// retry budget — cascaded dependents are a consequence of the failure), then
// partial. The partial case fires only when no step failed and nothing was
// aborted, yet some steps never ran — which, because FindReadySteps blocks a
// step solely on a failed or missing dependency, can only mean a cyclic or
// dangling DependsOn graph. Without this check such a plan would be falsely
// reported as success.
func planCompletionStatus(
	completed map[string]orchestration.CompletedStep,
	plan *orchestration.Plan,
	aborted bool,
) (status orchestration.ExecutionStatus, failed int, execErr error) {
	for _, c := range completed {
		if c.Error != nil {
			failed++
		}
	}
	switch {
	case aborted:
		status = orchestration.ExecutionStatusAborted
	case failed > 0:
		status = orchestration.ExecutionStatusFailed
	case len(completed) < len(plan.Steps):
		status = orchestration.ExecutionStatusPartial
		execErr = fmt.Errorf("%w: %d/%d steps completed",
			orchestration.ErrExecutionIncomplete, len(completed), len(plan.Steps))
	default:
		status = orchestration.ExecutionStatusSuccess
	}
	return status, failed, execErr
}

// switchModelMidLoop switches the active model during a phase where a switch
// failure is non-fatal — the reflection/replan block inside runStep and the
// final model restore in runPlanned. Unlike the top-level plan/exec switches in
// runPlanned (which abort on failure), these sites must keep going, so the
// error is surfaced via OnServiceMeta for observability instead of being
// silently discarded with "_ = router.SetModel(...)".
func (b *TaskBuilder) switchModelMidLoop(ctx context.Context, model string) {
	if err := b.fw.LLMRouter().SetModel(ctx, model); err != nil {
		b.events.OnServiceMeta(
			fmt.Sprintf("failed to switch model to %q: %s (continuing on active model)", model, err),
			map[string]any{"model": model},
		)
	}
}

// completedInOrder returns the completed steps as a slice ordered by the plan
// (rather than non-deterministic map iteration order), so replanning and
// output aggregation observe a stable sequence. Steps not present in the plan
// (if any) are appended in map order.
func completedInOrder(completed map[string]orchestration.CompletedStep, plan *orchestration.Plan) []orchestration.CompletedStep {
	if plan == nil {
		out := make([]orchestration.CompletedStep, 0, len(completed))
		for _, c := range completed {
			out = append(out, c)
		}
		return out
	}
	out := make([]orchestration.CompletedStep, 0, len(completed))
	for _, s := range plan.Steps {
		if c, ok := completed[s.ID]; ok {
			out = append(out, c)
		}
	}
	return out
}

// runStep executes a single plan step with retry and optional reflection. On
// the final failed attempt it records the step as failed, carrying the last
// error message. When reflection is enabled, a failing attempt may instead
// abort (sets *aborted) or replan (produces a new plan via *replanPlan); in
// both cases the step is intentionally left out of completed so it — and
// anything depending on it — is re-evaluated under the new plan.
func (b *TaskBuilder) runStep(
	ctx context.Context,
	conductor *orchestration.Conductor,
	bb orchestration.Blackboard,
	availableTools []tools.ToolDescriptor,
	pl *planner.Planner,
	step orchestration.PlanStep,
	completed map[string]orchestration.CompletedStep,
	rf *reflector.Reflector,
	reflections *[]orchestration.Reflection,
	aborted *bool,
	replanPlan **orchestration.Plan,
) {
	maxAttempts := b.maxRetries + 1
	var trajectory []agent.Step
	lastErrMsg := "max retries exceeded"

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		store := &trajectoryStore{}
		stepCtx := agent.WithTrajectoryStore(ctx, store)
		result, runErr := conductor.Run(stepCtx, step.Description, bb, availableTools, b.events, b.compaction)
		trajectory = store.Steps()

		if runErr == nil && result != nil && result.Status == orchestration.ExecutionStatusSuccess {
			completed[step.ID] = orchestration.CompletedStep{StepID: step.ID, Output: result.Output, Steps: trajectory}
			bb.SetStepResult(step.ID, result.Output, nil, trajectory)
			b.events.OnStepCompleted(step.ID, true, 0, "")
			return
		}

		if runErr != nil {
			lastErrMsg = runErr.Error()
		} else if result != nil {
			lastErrMsg = result.Output
		}

		if attempt <= b.maxRetries && rf != nil {
			// Reflect on the planning model (stronger reasoning), then restore.
			if b.planModel != "" && b.execModel != "" {
				b.switchModelMidLoop(stepCtx, b.planModel)
			}
			if r, rErr := rf.Reflect(stepCtx, trajectory, bb.GetPlan(), *reflections); rErr == nil && r != nil {
				bb.AddReflection(*r)
				*reflections = append(*reflections, *r)
				b.events.OnReflected(r, attempt, b.maxRetries)
				switch r.SuggestedAction {
				case "abort":
					if b.planModel != "" && b.execModel != "" {
						b.switchModelMidLoop(stepCtx, b.execModel)
					}
					*aborted = true
					completed[step.ID] = orchestration.CompletedStep{StepID: step.ID, Error: errors.New("aborted by reflection")}
					b.events.OnStepCompleted(step.ID, false, 0, "aborted by reflection")
					return
				case "replan":
					// The reflector judged the plan itself flawed (not just this
					// attempt). Re-derive the remaining plan, preserving prior
					// successful work; runPlanned applies BuildCarryForward and
					// restarts the DAG. The failed step is left out of completed
					// so it and its dependents are re-executed under the new plan.
					newPlan, rpErr := pl.Replan(
						stepCtx,
						bb.GetPlan(),
						completedInOrder(completed, bb.GetPlan()),
						orchestration.CompletedStep{StepID: step.ID, Output: lastErrMsg, Steps: trajectory},
						r,
						*reflections,
						b.skills,
					)
					if rpErr != nil {
						b.events.OnReplanFailed(rpErr)
						break // replanning failed — restore exec model below, then retry
					}
					if b.planModel != "" && b.execModel != "" {
						b.switchModelMidLoop(stepCtx, b.execModel)
					}
					*replanPlan = newPlan
					b.events.OnStepCompleted(step.ID, false, 0, "replanning after reflection")
					return
				}
			}
			if b.planModel != "" && b.execModel != "" {
				b.switchModelMidLoop(stepCtx, b.execModel)
			}
		}
	}

	completed[step.ID] = orchestration.CompletedStep{
		StepID: step.ID,
		Error:  fmt.Errorf("failed after retries: %s", lastErrMsg),
	}
	b.events.OnStepCompleted(step.ID, false, 0, lastErrMsg)
}

// resolvePlanner returns the injected planner or builds a default one.
func (b *TaskBuilder) resolvePlanner(ctx context.Context) (*planner.Planner, error) {
	if b.customPlanner != nil {
		return b.customPlanner, nil
	}
	cfg := planner.DefaultConfig()
	cfg.Prompts = DefaultPromptSet()
	// Resolve family from the planning model if set, else the active model.
	model := b.planModel
	if model == "" {
		model = b.fw.LLMRouter().ActiveModel()
	}
	cfg.Model = model
	pl, err := planner.NewPlanner(b.fw.LLMRouter(), cfg)
	if err != nil {
		return nil, fmt.Errorf("TaskF: planner: %w", err)
	}
	return pl, nil
}

// resolveReflector returns the injected reflector or builds a default one. May
// return nil when reflection is disabled.
func (b *TaskBuilder) resolveReflector() *reflector.Reflector {
	if !b.useReflector {
		return nil
	}
	if b.customReflector != nil {
		return b.customReflector
	}
	return reflector.New(b.fw.LLMRouter(), reflector.Config{
		SystemPrompt: DefaultReflectorPrompt(),
	})
}

// planStepsToEvents converts plan steps to the event representation.
func planStepsToEvents(plan *orchestration.Plan) []orchestration.PlanStepEvent {
	events := make([]orchestration.PlanStepEvent, len(plan.Steps))
	for i, s := range plan.Steps {
		events[i] = orchestration.PlanStepEvent{
			ID:          s.ID,
			Summary:     s.Summary,
			Description: s.Description,
			DependsOn:   s.DependsOn,
		}
	}
	return events
}
