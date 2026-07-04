package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/agent/reflector"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/orchestration"
	sdktools "github.com/v0lka/c0wrk/sdk/tools"
)

// trajectoryHolder is a thread-safe TrajectoryStore for the Conductor.
// The executor syncs its step history here at each loop iteration; the
// reflect tool reads the current trajectory for scope="trajectory".
type trajectoryHolder struct {
	mu    sync.Mutex
	steps []agent.Step
}

func (h *trajectoryHolder) Sync(steps []agent.Step) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.steps = make([]agent.Step, len(steps))
	copy(h.steps, steps)
}

func (h *trajectoryHolder) Steps() []agent.Step {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.steps
}

// conductorDeps bundles the runtime dependencies needed by the Conductor's
// launcher, publisher, and reflection runner. It mirrors the subset of
// OrchestratorDeps required to build executors and publish plans.
type conductorDeps struct {
	contextFactory   ContextManagerFactory
	toolExec         agent.ToolExecutor
	toolRegistry     *sdktools.ToolRegistry
	llm              agent.LLMCaller
	modelRegistry    *llm.ModelRegistry
	model            string
	tokenCounter     llm.TokenCounter
	emitter          Emitter
	logger           *slog.Logger
	trackingCaller   *llm.TrackingCaller
	providerName     string
	stepDumpTracker  *orchestration.StepDumpTracker
	toolCache        *agent.ToolResultCache
	perToolTrunc     map[string]agent.ToolTruncationConfig
	toolResultBudget agent.ToolResultBudget
	circuitBreaker   agent.CircuitBreakerConfig
	hitlHandler      agent.HITLHandler
	reflector        *reflector.Reflector
	maxSteps         int
	subagentMaxSteps int
	maxRedelegDepth  int
	maxDepCtxChars   int
	reasoningEffort  string
	preWarningPct    int
}

// conductorLauncher implements tools.DelegationLauncher by building a fresh
// Executor + ContextManager per task and dispatching via RunSubAgent.
type conductorLauncher struct {
	deps   conductorDeps
	bb     orchestration.Blackboard
}

func (l *conductorLauncher) Launch(ctx context.Context, tasks []tools.DelegationTask, registry *tools.DelegationRegistry) []tools.DelegationResult {
	results := make([]tools.DelegationResult, 0, len(tasks))
	pending := make(map[string]tools.DelegationTask, len(tasks))
	for _, t := range tasks {
		pending[t.ID] = t
	}

	for len(pending) > 0 {
		if ctx.Err() != nil {
			for id := range pending {
				registry.Complete(id, "", ctx.Err(), nil)
				results = append(results, tools.DelegationResult{ID: id, Status: tools.DelegationStatusCancelled, Error: ctx.Err()})
			}
			return results
		}

		var ready []tools.DelegationTask
		for _, t := range pending {
			if l.depsReady(t, registry) {
				ready = append(ready, t)
			}
		}
		if len(ready) == 0 {
			for id := range pending {
				err := fmt.Errorf("delegation %q: dependencies could not be satisfied", id)
				registry.Complete(id, "", err, nil)
				results = append(results, tools.DelegationResult{ID: id, Status: tools.DelegationStatusFailed, Error: err})
			}
			return results
		}

		waveResults := l.runWave(ctx, ready, registry)
		for _, r := range waveResults {
			results = append(results, r)
			delete(pending, r.ID)
		}
	}
	return results
}

func (l *conductorLauncher) depsReady(t tools.DelegationTask, registry *tools.DelegationRegistry) bool {
	for _, dep := range t.DependsOn {
		if !registry.IsCompleted(dep) {
			return false
		}
		d := registry.Get(dep)
		if d != nil && d.Status == tools.DelegationStatusFailed {
			return false
		}
	}
	return true
}

func (l *conductorLauncher) runWave(ctx context.Context, ready []tools.DelegationTask, registry *tools.DelegationRegistry) []tools.DelegationResult {
	var blocking []tools.DelegationTask
	var asyncTasks []tools.DelegationTask
	for _, t := range ready {
		mode := t.Mode
		if mode == "" {
			mode = "blocking"
		}
		if mode == "async" {
			asyncTasks = append(asyncTasks, t)
		} else {
			blocking = append(blocking, t)
		}
	}

	var results []tools.DelegationResult

	for _, t := range asyncTasks {
		r := l.launchAsync(ctx, t, registry)
		results = append(results, r)
	}

	if len(blocking) > 0 {
		blockResults := l.runBlocking(ctx, blocking, registry)
		results = append(results, blockResults...)
	}

	return results
}

func (l *conductorLauncher) runBlocking(ctx context.Context, tasks []tools.DelegationTask, registry *tools.DelegationRegistry) []tools.DelegationResult {
	// Split tasks into regular and redelegating. Redelegating tasks need
	// per-task contexts (child registry + launcher), so they can't share
	// the same ctx path through RunSubAgentsParallel.
	var regularTasks []tools.DelegationTask
	var redelegTasks []tools.DelegationTask
	for _, t := range tasks {
		if t.AllowRedelegate {
			redelegTasks = append(redelegTasks, t)
		} else {
			regularTasks = append(regularTasks, t)
		}
	}

	var results []tools.DelegationResult

	// Launch regular tasks in parallel.
	if len(regularTasks) > 0 {
		results = append(results, l.runRegularBlocking(ctx, regularTasks, registry)...)
	}

	// Launch redelegating tasks individually (per-task context).
	for _, t := range redelegTasks {
		results = append(results, l.runRedelegBlocking(ctx, t, registry))
	}

	return results
}

func (l *conductorLauncher) runRegularBlocking(ctx context.Context, tasks []tools.DelegationTask, registry *tools.DelegationRegistry) []tools.DelegationResult {
	subTasks := make([]agent.SubAgentTask, 0, len(tasks))
	for _, t := range tasks {
		st, err := l.buildSubAgentTask(ctx, t, registry)
		if err != nil {
			registry.Complete(t.ID, "", err, nil)
			continue
		}
		registry.Start(t.ID, nil)
		subTasks = append(subTasks, st)
	}

	if len(subTasks) == 0 {
		out := make([]tools.DelegationResult, 0, len(tasks))
		for _, t := range tasks {
			out = append(out, tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusFailed, Error: errors.New("subagent task construction failed")})
		}
		return out
	}

	subResults := agent.RunSubAgentsParallel(ctx, subTasks)
	out := make([]tools.DelegationResult, 0, len(subResults))
	for _, sr := range subResults {
		var execErr error
		if sr.Error != nil {
			execErr = sr.Error
		}
		registry.Complete(sr.StepID, sr.Output, execErr, sr.Steps)
		l.bb.SetStepResult(sr.StepID, sr.Output, execErr, sr.Steps)
		status := tools.DelegationStatusCompleted
		if execErr != nil {
			status = tools.DelegationStatusFailed
		}
		out = append(out, tools.DelegationResult{ID: sr.StepID, Status: status, Output: sr.Output, Error: execErr})
	}
	return out
}

func (l *conductorLauncher) runRedelegBlocking(ctx context.Context, t tools.DelegationTask, registry *tools.DelegationRegistry) tools.DelegationResult {
	if registry.Depth() >= l.deps.maxRedelegDepth {
		err := fmt.Errorf("delegation %q: allow_redelegate requested at depth %d but cap is %d", t.ID, registry.Depth(), l.deps.maxRedelegDepth)
		registry.Complete(t.ID, "", err, nil)
		return tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusFailed, Error: err}
	}

	// Create a child registry one level deeper and inject it + the launcher
	// into a per-task context so the subagent can use delegate/cancel_delegation.
	childReg := tools.NewDelegationRegistryWithDepth(registry.Depth() + 1)
	taskCtx := tools.WithDelegationRegistry(ctx, childReg)
	taskCtx = tools.WithDelegationLauncher(taskCtx, l)
	// Also inject into the SDK-level context key so finishJoinExecutor
	// (in sdk/orchestration/conductor.go) can find the child registry.
	taskCtx = orchestration.WithDelegationRegistry(taskCtx, childReg)

	// Build the subagent task. The finishJoinExecutor in the SDK Conductor
	// will guard against finish with pending async sub-delegations
	// automatically, since the child registry is in the context.
	st, err := l.buildSubAgentTask(taskCtx, t, registry)
	if err != nil {
		registry.Complete(t.ID, "", err, nil)
		return tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusFailed, Error: err}
	}

	// Add delegate + cancel_delegation to the subagent's tool set.
	redelegTools := make([]sdktools.ToolDescriptor, 0, len(st.TaskTools)+2)
	redelegTools = append(redelegTools, st.TaskTools...)
	redelegTools = append(redelegTools,
		sdktools.ToolDescriptor{Name: "delegate", Description: "Delegate subtasks to further subagents"},
		sdktools.ToolDescriptor{Name: "cancel_delegation", Description: "Cancel a pending async delegation"},
	)

	registry.Start(t.ID, nil)
	ch := agent.RunSubAgent(taskCtx, t.ID, st.Executor, st.CM, redelegTools, st.TaskDesc, st.Emitter, st.TodoUpdateFunc)
	sr := <-ch

	var execErr error
	if sr.Error != nil {
		execErr = sr.Error
	}
	registry.Complete(t.ID, sr.Output, execErr, sr.Steps)
	l.bb.SetStepResult(t.ID, sr.Output, execErr, sr.Steps)
	status := tools.DelegationStatusCompleted
	if execErr != nil {
		status = tools.DelegationStatusFailed
	}
	return tools.DelegationResult{ID: t.ID, Status: status, Output: sr.Output, Error: execErr}
}

func (l *conductorLauncher) launchAsync(ctx context.Context, t tools.DelegationTask, registry *tools.DelegationRegistry) tools.DelegationResult {
	subTask, err := l.buildSubAgentTask(ctx, t, registry)
	if err != nil {
		registry.Complete(t.ID, "", err, nil)
		return tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusFailed, Error: err}
	}

	asyncCtx, cancel := context.WithCancel(ctx)
	registry.Start(t.ID, cancel)

	go func() {
		defer cancel()
		ch := agent.RunSubAgent(asyncCtx, t.ID, subTask.Executor, subTask.CM, subTask.TaskTools, subTask.TaskDesc, subTask.Emitter, subTask.TodoUpdateFunc)
		select {
		case sr := <-ch:
			var execErr error
			if sr.Error != nil {
				execErr = sr.Error
			}
			registry.Complete(t.ID, sr.Output, execErr, sr.Steps)
			l.bb.SetStepResult(t.ID, sr.Output, execErr, sr.Steps)
		case <-asyncCtx.Done():
			registry.Complete(t.ID, "", asyncCtx.Err(), nil)
		}
	}()

	return tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusRunning}
}

func (l *conductorLauncher) buildSubAgentTask(ctx context.Context, t tools.DelegationTask, registry *tools.DelegationRegistry) (agent.SubAgentTask, error) {
	maxSteps := t.MaxSteps
	if maxSteps <= 0 {
		maxSteps = l.deps.subagentMaxSteps
	}

	taskTools := l.resolveTaskTools(t)
	taskDesc := l.buildTaskDescription(t, registry)

	// Derive compaction strategy from the Conductor's routing domain +
	// complexity (inherited via context). Subagents inherit the same
	// domain/complexity as the Conductor unless the task overrides it.
	domain := DomainFromContext(ctx)
	complexity := ComplexityFromContext(ctx)
	compactionStrategy := compactionStrategyForDomain(domain, complexity)

	modelMeta := l.resolveModelMeta(ctx)
	systemPrompt := buildSystemPrompt(ctx, t.Task, modelMeta)

	var cm agent.ContextManager
	if l.deps.contextFactory != nil {
		cm = l.deps.contextFactory(systemPrompt, modelMeta, compactionStrategy)
	} else {
		return agent.SubAgentTask{}, fmt.Errorf("delegation %q: context factory not configured", t.ID)
	}
	if ccm, ok := cm.(interface{ SetTask(string) }); ok {
		ccm.SetTask(taskDesc)
	}

	caller := l.callerForStep(cm, t.ID)
	executor := agent.NewExecutor(caller, l.deps.toolExec, l.deps.tokenCounter, maxSteps, l.scopeEvents(t.ID), false, l.deps.toolResultBudget, l.deps.circuitBreaker, l.deps.hitlHandler)
	executor.SetPlanContext(t.ID, 0, 0)
	l.configureExecutor(executor)

	return agent.SubAgentTask{
		StepID:    t.ID,
		Executor:  executor,
		CM:        cm,
		TaskTools: taskTools,
		TaskDesc:  taskDesc,
		Emitter:   l.scopeEvents(t.ID),
	}, nil
}

func (l *conductorLauncher) resolveTaskTools(t tools.DelegationTask) []sdktools.ToolDescriptor {
	all := l.allToolDescriptors()
	if t.Tools == nil {
		return all
	}
	switch v := t.Tools.(type) {
	case string:
		switch v {
		case "all", "":
			return all
		case "read-only":
			return filterReadOnlyTools(all)
		default:
			return all
		}
	default:
		if names, ok := parseToolNames(t.Tools); ok {
			return filterToolsByName(all, names)
		}
		return all
	}
}

func (l *conductorLauncher) allToolDescriptors() []sdktools.ToolDescriptor {
	if l.deps.toolRegistry != nil {
		return l.deps.toolRegistry.List()
	}
	return nil
}

func (l *conductorLauncher) buildTaskDescription(t tools.DelegationTask, registry *tools.DelegationRegistry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Delegation %s] %s\n\n", t.ID, t.Task)
	fmt.Fprintf(&b, "Tool call budget: %d iterations. Plan your approach to finish within this budget.\n\n", l.effectiveMaxSteps(t))
	b.WriteString("IMPORTANT: You are executing ONE delegated task. Complete ONLY this task's objective. Do NOT perform work outside the task scope.\n\n")

	if len(t.AcceptanceCriteria) > 0 {
		b.WriteString("## Acceptance Criteria\n")
		for _, c := range t.AcceptanceCriteria {
			fmt.Fprintf(&b, "- %s\n", c)
		}
		b.WriteString("\n## Pre-Finish Verification\nBefore calling the finish tool, verify each acceptance criterion is satisfied. Use tool calls to confirm, not assumptions.\n\n")
	} else {
		b.WriteString("## Pre-Finish Verification\nBefore calling the finish tool, verify the task objective is satisfied. Use tool calls to confirm, not assumptions.\n\n")
	}

	if len(t.DependsOn) > 0 {
		var depBuf strings.Builder
		for _, depID := range t.DependsOn {
			d := registry.Get(depID)
			if d != nil && d.Output != "" {
				fmt.Fprintf(&depBuf, "\n### [%s]: %s\n%s\n", depID, d.Summary, d.Output)
			}
		}
		depContext := depBuf.String()
		maxDepChars := l.deps.maxDepCtxChars
		if maxDepChars <= 0 {
			maxDepChars = 8000
		}
		if len(depContext) > maxDepChars {
			depContext = depContext[len(depContext)-maxDepChars:]
		}
		if depContext != "" {
			b.WriteString("\n## Context from previous delegations\n")
			b.WriteString(depContext)
			b.WriteString("\n\nIf insufficient, use read_step_output with the delegation ID to access the full output.\n")
		}
	}

	b.WriteString("Pass your result through the finish tool.\n")
	return b.String()
}

func (l *conductorLauncher) effectiveMaxSteps(t tools.DelegationTask) int {
	if t.MaxSteps > 0 {
		return t.MaxSteps
	}
	return l.deps.subagentMaxSteps
}

func (l *conductorLauncher) resolveModelMeta(ctx context.Context) llm.ModelMetadata {
	if l.deps.modelRegistry != nil && l.deps.model != "" {
		if meta, ok := l.deps.modelRegistry.Resolve(ctx, l.deps.model); ok {
			return meta
		}
	}
	if l.deps.modelRegistry != nil {
		if meta, ok := l.deps.modelRegistry.Resolve(ctx, ""); ok {
			return meta
		}
	}
	return llm.ModelMetadata{}
}

func (l *conductorLauncher) callerForStep(cm agent.ContextManager, stepID string) agent.LLMCaller {
	if l.deps.trackingCaller == nil {
		return l.deps.llm
	}
	var caller agent.LLMCaller = l.deps.trackingCaller
	if ctm, ok := cm.(interface {
		ContextTracker() *llm.ContextTokenTracker
	}); ok {
		caller = l.deps.trackingCaller.WithContextTracker(ctm.ContextTracker())
	}
	if l.deps.stepDumpTracker != nil && l.deps.providerName != "" {
		caller = agent.NewLoggingLLMCaller(caller, l.deps.providerName, l.deps.logger)
	}
	if l.deps.stepDumpTracker != nil {
		if w := l.deps.stepDumpTracker.OpenStepDump(stepID); w != nil {
			caller = agent.NewDumpCaller(caller, w, l.deps.logger)
		}
	}
	return caller
}

func (l *conductorLauncher) scopeEvents(stepID string) agent.AgentEvents {
	if sc, ok := l.deps.emitter.(interface {
		WithPlanStepID(string) Emitter
	}); ok {
		return sc.WithPlanStepID(stepID)
	}
	return &agent.NoopEvents{}
}

func (l *conductorLauncher) configureExecutor(executor *agent.Executor) {
	if l.deps.hitlHandler != nil {
		executor.SetHITLHandler(l.deps.hitlHandler)
	}
	if l.deps.preWarningPct > 0 {
		executor.SetPreWarningPercent(l.deps.preWarningPct)
	}
	if l.deps.toolCache != nil {
		executor.SetToolCache(l.deps.toolCache)
	}
	if l.deps.perToolTrunc != nil {
		executor.SetPerToolTruncation(l.deps.perToolTrunc)
	}
	if l.deps.reasoningEffort != "" {
		executor.SetReasoningEffort(l.deps.reasoningEffort)
	}
}

func parseToolNames(v any) ([]string, bool) {
	if arr, ok := v.([]any); ok {
		names := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok {
				names = append(names, s)
			}
		}
		return names, true
	}
	return nil, false
}

func filterToolsByName(all []sdktools.ToolDescriptor, names []string) []sdktools.ToolDescriptor {
	nameSet := make(map[string]struct{}, len(names))
	for _, n := range names {
		nameSet[n] = struct{}{}
	}
	out := make([]sdktools.ToolDescriptor, 0, len(names))
	for _, d := range all {
		if _, ok := nameSet[d.Name]; ok {
			out = append(out, d)
		}
	}
	internal := []string{"finish", "store_fact", "search_facts", "read_step_output", "set_step_status", "semantic_search", "ask_user", "tool_result_read", "list_step_outputs"}
	for _, name := range internal {
		for _, d := range all {
			if d.Name == name {
				out = append(out, d)
				break
			}
		}
	}
	return out
}

func filterReadOnlyTools(all []sdktools.ToolDescriptor) []sdktools.ToolDescriptor {
	readOnly := map[string]struct{}{
		"read_file": {}, "list_directory": {}, "glob": {}, "ripgrep": {},
		"semantic_search": {}, "search_facts": {}, "read_step_output": {},
		"list_step_outputs": {}, "web_fetch": {}, "web_search": {},
		"finish": {}, "store_fact": {}, "set_step_status": {}, "ask_user": {},
		"tool_result_read": {}, "read_skill_resource": {},
	}
	out := make([]sdktools.ToolDescriptor, 0, len(all))
	for _, d := range all {
		if _, ok := readOnly[d.Name]; ok {
			out = append(out, d)
		}
	}
	return out
}

// conductorPublisher implements tools.PlanPublisher.
type conductorPublisher struct {
	emitter  Emitter
	bb       orchestration.Blackboard
	plansDir string
	logger   *slog.Logger
	lastMD   string // markdown from the most recent Publish call
}

func (p *conductorPublisher) Publish(ctx context.Context, tasks []tools.PlanTaskInput) (string, error) {
	plan := &orchestration.Plan{}
	for _, t := range tasks {
		plan.Steps = append(plan.Steps, orchestration.PlanStep{
			ID:          t.ID,
			Summary:     t.Summary,
			Description: t.Description,
			DependsOn:   append([]string(nil), t.DependsOn...),
		})
	}

	if p.plansDir == "" {
		return "", errors.New("declare_plan: session plans directory not configured")
	}
	if err := os.MkdirAll(p.plansDir, 0o755); err != nil {
		return "", fmt.Errorf("declare_plan: failed to create plans directory: %w", err)
	}
	md := SerializePlan(plan)
	p.lastMD = md
	suffix := RandomSuffix()
	path := filepath.Join(p.plansDir, fmt.Sprintf("plan_%s.md", suffix))
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		return "", fmt.Errorf("declare_plan: failed to write plan file: %w", err)
	}

	// Emit plan events and set plan on blackboard only after the file is
	// persisted — if the write fails, the UI should not show a plan that
	// can't be reviewed or approved.
	planEvents := make([]orchestration.PlanStepEvent, len(plan.Steps))
	for i, s := range plan.Steps {
		planEvents[i] = orchestration.PlanStepEvent{ID: s.ID, Summary: s.Summary, Description: s.Description, Status: "pending", DependsOn: s.DependsOn}
	}
	p.emitter.PlanGenerated(len(plan.Steps), planEvents)
	p.bb.SetPlan(plan)

	if p.logger != nil {
		p.logger.Debug("declare_plan: plan published", "path", path, "steps", len(plan.Steps))
	}
	return path, nil
}

// LastPlanMarkdown returns the markdown content from the most recent Publish call.
func (p *conductorPublisher) LastPlanMarkdown() string {
	return p.lastMD
}

// conductorReflectionRunner implements tools.ReflectionRunner.
type conductorReflectionRunner struct {
	reflector *reflector.Reflector
	bb        orchestration.Blackboard
	emitter   Emitter
	logger    *slog.Logger
	traj      *trajectoryHolder
}

func (r *conductorReflectionRunner) Reflect(ctx context.Context, scope, delegationID string) (tools.ReflectionResult, error) {
	if r.reflector == nil {
		return tools.ReflectionResult{}, errors.New("reflect: no reflector configured")
	}

	var trajectory []agent.Step
	if scope == "delegation" {
		reg := tools.DelegationRegistryFrom(ctx)
		if reg == nil {
			return tools.ReflectionResult{}, errors.New("reflect: no delegation registry in context")
		}
		d := reg.Get(delegationID)
		if d == nil {
			return tools.ReflectionResult{}, fmt.Errorf("reflect: unknown delegation id %q", delegationID)
		}
		trajectory = d.Steps
	} else if r.traj != nil {
		trajectory = r.traj.Steps()
	}

	plan := r.bb.GetPlan()
	prevReflections := r.bb.GetReflections()

	reflection, err := r.reflector.Reflect(ctx, trajectory, plan, prevReflections)
	if err != nil {
		return tools.ReflectionResult{}, fmt.Errorf("reflect failed: %w", err)
	}
	if reflection == nil {
		return tools.ReflectionResult{Summary: "no reflection produced"}, nil
	}

	r.bb.AddReflection(*reflection)
	r.emitter.Reflection(reflection, 0, 0)
	if r.logger != nil {
		r.logger.Debug("reflect: reflection produced", "summary", reflection.Summary, "suggested_action", reflection.SuggestedAction)
	}

	return tools.ReflectionResult{
		Summary:         reflection.Summary,
		SuggestedAction: reflection.SuggestedAction,
		RootCause:       reflection.RootCause,
		ActionPlan:      reflection.ActionPlan,
	}, nil
}

// compactionStrategyForDomain maps a routing domain + complexity to the
// compaction strategy per ADR-012 / specs/domains/orchestration/router.md:
//
//	code     → sliding_window (keep recent edits visible)
//	research → summarization (condense findings)
//	general  → sliding_window, or hierarchical if complexity >= 4
//	mixed    → sliding_window
func compactionStrategyForDomain(domain string, complexity int) string {
	switch domain {
	case "research":
		return "summarization"
	case "general":
		if complexity >= 4 {
			return "hierarchical"
		}
		return "sliding_window"
	default:
		return "sliding_window"
	}
}

// conductorGuidanceForComplexity returns a system-prompt section that guides
// the Conductor on when to delegate, declare_plan, or handle inline, based on
// the routing complexity.
func conductorGuidanceForComplexity(complexity int) string {
	switch {
	case complexity <= 1:
		return "## Conductor Guidance\nYou are the Conductor: you own this task end-to-end. This is a simple task — handle it inline (read files, search, answer, call finish). Do not call delegate or declare_plan for trivial tasks.\n"
	case complexity <= 2:
		return "## Conductor Guidance\nYou are the Conductor: you own this task end-to-end. Handle this task inline unless it clearly involves multiple files or subsystems — in that case, call delegate with one task. Call finish when the task is complete.\n"
	case complexity <= 3:
		return "## Conductor Guidance\nYou are the Conductor: you own this task end-to-end. For tasks involving multiple actions or some exploration, consider calling delegate to break coherent units of work into isolated subagents. Call finish when the task is complete.\n"
	case complexity <= 4:
		return "## Conductor Guidance\nYou are the Conductor: you own this task end-to-end. This is a complex task — call delegate to break it into subtasks, and consider calling declare_plan to surface a roadmap before implementing. When an active skill prescribes an approval gate, call declare_plan with mode=await_approval before implementing. Call finish when the task is complete.\n"
	default:
		return "## Conductor Guidance\nYou are the Conductor: you own this task end-to-end. This is a large task — call declare_plan with mode=await_approval to present a roadmap for user approval before implementing, then call delegate to break the approved plan into subtasks. When the trajectory looks wrong, call reflect. Call finish when the task is complete.\n"
	}
}

// RunConductor builds and launches the Conductor: a single Executor.Run that
// owns the task end-to-end. The Conductor's tool set includes the standard
// file/search/internal tools plus the Conductor tools (delegate, declare_plan,
// reflect, cancel_delegation), wired via context-injected implementations.
//
// The core layer is responsible for:
//   - Injecting Conductor tools (DelegationRegistry, DelegationLauncher,
//     PlanPublisher, ReflectionRunner, TrajectoryStore) into the context
//   - Appending the Conductor Guidance section to the system prompt
//
// The SDK-level orchestration.Conductor handles:
//   - Model metadata resolution and system prompt construction
//   - ContextManager creation
//   - Executor construction and configuration
//   - finishJoinExecutor wrapping (prevents finish with pending async delegations)
//   - StepOutputStore and FactStore injection
//   - ExecutionResult assembly
func RunConductor(
	ctx context.Context,
	message string,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
	deps conductorDeps,
	plansDir string,
) (*orchestration.ExecutionResult, error) {
	registry := tools.NewDelegationRegistry()
	launcher := &conductorLauncher{deps: deps, bb: bb}
	publisher := &conductorPublisher{emitter: deps.emitter, bb: bb, plansDir: plansDir, logger: deps.logger}
	trajHolder := &trajectoryHolder{}
	runner := &conductorReflectionRunner{reflector: deps.reflector, bb: bb, emitter: deps.emitter, logger: deps.logger, traj: trajHolder}

	// Derive compaction strategy from routing domain + complexity.
	domain := DomainFromContext(ctx)
	complexity := ComplexityFromContext(ctx)
	compactionStrategy := compactionStrategyForDomain(domain, complexity)
	guidance := conductorGuidanceForComplexity(complexity)

	// Inject Conductor-specific context values so the tools (delegate,
	// declare_plan, reflect, cancel_delegation) can find their dependencies.
	ctx = tools.WithDelegationRegistry(ctx, registry)
	ctx = tools.WithDelegationLauncher(ctx, launcher)
	ctx = tools.WithPlanPublisher(ctx, publisher)
	ctx = tools.WithReflectionRunner(ctx, runner)
	ctx = agent.WithTrajectoryStore(ctx, trajHolder)
	ctx = orchestration.WithDelegationRegistry(ctx, registry)

	// Build the system prompt with complexity-based Conductor Guidance.
	systemPromptFactory := func(ctx context.Context, msg string, modelMeta llm.ModelMetadata) string {
		prompt := buildSystemPrompt(ctx, msg, modelMeta)
		prompt += "\n\n" + guidance
		return prompt
	}

	cfg := orchestration.ConductorConfig{
		LLM:               callerForConductor(deps),
		Tools:             deps.toolExec,
		ToolRegistry:      deps.toolRegistry,
		TokenCounter:      deps.tokenCounter,
		Model:             deps.model,
		ModelRegistry:     deps.modelRegistry,
		ContextFactory:    adaptContextFactory(deps.contextFactory),
		SystemPrompt:      systemPromptFactory,
		MaxSteps:          deps.maxSteps,
		ToolResultBudget:  deps.toolResultBudget,
		CircuitBreaker:    deps.circuitBreaker,
		HITLHandler:       deps.hitlHandler,
		ToolCache:         deps.toolCache,
		PerToolTruncation: deps.perToolTrunc,
		ReasoningEffort:   deps.reasoningEffort,
		PreWarningPercent: deps.preWarningPct,
	}

	var events agent.AgentEvents = &agent.NoopEvents{}
	if deps.emitter != nil {
		events = deps.emitter
	}

	conductor := orchestration.NewConductor(cfg)
	return conductor.Run(ctx, message, bb, availableTools, events, compactionStrategy)
}

func callerForConductor(deps conductorDeps) agent.LLMCaller {
	if deps.trackingCaller == nil {
		return deps.llm
	}
	var caller agent.LLMCaller = deps.trackingCaller
	if deps.providerName != "" && deps.logger != nil {
		caller = agent.NewLoggingLLMCaller(caller, deps.providerName, deps.logger)
	}
	return caller
}

// adaptContextFactory converts a core ContextManagerFactory (which returns
// core.ContextManager and takes variadic PruningOverride) to the SDK
// ContextManagerFactory signature (which returns agent.ContextManager and
// takes variadic PruningOverride). The core ContextManager embeds
// agent.ContextManager, so the return type is compatible.
func adaptContextFactory(cf ContextManagerFactory) orchestration.ContextManagerFactory {
	if cf == nil {
		return nil
	}
	return func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string, pruningOverrides ...orchestration.PruningOverride) agent.ContextManager {
		coreOverrides := make([]orchestration.PruningOverride, len(pruningOverrides))
		copy(coreOverrides, pruningOverrides)
		return cf(systemPrompt, modelMeta, compactionStrategy, coreOverrides...)
	}
}

// (o *Orchestrator) runConductor assembles a conductorDeps from the
// orchestrator's stored fields and delegates to RunConductor. Used by
// HandleMessage and Resume.
func (o *Orchestrator) runConductor(ctx context.Context, message string, bb orchestration.Blackboard, availableTools []sdktools.ToolDescriptor, plansDir string) (*orchestration.ExecutionResult, error) {
	deps := conductorDeps{
		contextFactory:   o.contextFactory,
		toolExec:         o.toolExec,
		toolRegistry:     o.toolRegistry,
		llm:              o.llm,
		modelRegistry:    o.modelRegistry,
		model:            o.config.Model,
		tokenCounter:     o.tokenCounter,
		emitter:          o.emitter,
		logger:           o.logger,
		trackingCaller:   o.trackingCaller,
		providerName:     o.providerName,
		stepDumpTracker:  o.stepDumpTracker,
		toolCache:        o.toolCache,
		perToolTrunc:     o.perToolTrunc,
		toolResultBudget: o.toolResultBudget,
		circuitBreaker:   o.circuitBreaker,
		hitlHandler:      o.config.HITLHandler,
		reflector:        o.reflector,
		maxSteps:         o.config.MaxSteps,
		subagentMaxSteps: o.config.SubagentMaxSteps,
		maxRedelegDepth:  o.config.MaxRedelegationDepth,
		maxDepCtxChars:   o.config.MaxDependencyContextChars,
		reasoningEffort:  o.config.ReasoningEffort,
		preWarningPct:    o.config.PreWarningPercent,
	}
	return RunConductor(ctx, message, bb, availableTools, deps, plansDir)
}
