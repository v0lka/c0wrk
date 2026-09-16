package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdktools "github.com/v0lka/sp4rk/tools"
)

const toolDeclarePlanDescription = `Purpose: publish the task roadmap — ordered steps with acceptance criteria — for user sign-off.
Use when: the user asked to plan first or the task is multi-step; call once before acting. Declare depends_on whenever a step consumes another step's output, artifacts, or decisions; a step with no depends_on runs CONCURRENTLY with its siblings, so an omitted link is a correctness bug, not a harmless omission. When unsure, declare the dependency: a spurious edge only serializes, a missing edge runs the steps in parallel. Approved plans are append-only: never edit or delete.
Inputs: mode ("present" | "await_approval"); tasks: array of {id (e.g. step_1), summary (label), description (What/How/Where/AC), depends_on (prerequisite ids), agent (Subagent name)}.
Outputs: "present" displays the plan and continues; "await_approval" blocks for approval.
Example: step 1 "write failing tests", step 2 "implement" with depends_on ["step_1"].
Anti-example: a flat plan whose step 1 "write failing tests" and step 2 "implement" both omit depends_on — step 2 consumes step 1's tests yet runs CONCURRENTLY and fails. Also: never implement before approval; single-step tasks need no plan.`

// PlanPublisher serializes a plan, persists it to the session plans directory,
// emits the PlanGenerated event, and sets the plan on the blackboard.
// The implementation lives in the core layer.
type PlanPublisher interface {
	Publish(ctx context.Context, tasks []PlanTaskInput) (planPath string, err error)
	// LastPlanMarkdown returns the markdown content from the most recent
	// Publish call. Used by declare_plan to pass the content to the approval
	// callback without re-reading from disk.
	LastPlanMarkdown() string
}

// PlanTaskInput is the user-facing shape of a single roadmap task.
// The publisher converts these into the internal Plan/PlanStep types.
type PlanTaskInput struct {
	ID          string   `json:"id"`
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	DependsOn   []string `json:"depends_on,omitempty"`
	// Agent optionally names a Subagent Profile to execute this step with.
	// The publisher copies it onto the resulting PlanStep so the execution
	// layer (Conductor) can resolve and apply the profile. See declare_plan's
	// schema for the user-facing description.
	Agent string `json:"agent,omitempty"`
}

// PlanContinuation is an OPTIONAL capability of the PlanChecker: it reports
// whether the CURRENT Conductor run resumed a task whose already-declared plan
// still has unreached (not successfully completed) steps. Implemented by the
// core conductorLauncher and detected by declare_plan via a type assertion, so
// this package stays decoupled from core: when the capability reports true,
// declare_plan returns a soft "plan already approved — continue with
// execute_plan" hint instead of publishing a replacement plan.
type PlanContinuation interface {
	PlanContinuation() bool
}

// planAbandoner is an OPTIONAL capability of the PlanPublisher: it lets
// declare_plan react to the user abandoning a plan at the approval prompt.
// Publishing a plan for review marks the run's plan workflow active (see
// conductorPublisher.Publish), which disables delegate, rejects a standalone
// (empty step_id) checklist, and lets execute_plan run the plan's steps — all
// correct while the plan awaits approval, but wrong once the user abandons it.
// The implementation therefore does two things: it releases the workflow so
// the model can act plan-less again, and it settles the abandoned plan's steps
// so the plan panel does not leave them pending forever (the run is no longer
// plan-active, so the finish-fallback sweep no longer reaches them).
//
// It is deliberately a parameterless, one-directional capability: the only
// transition declare_plan ever needs to trigger is abandonment. Re-activation
// is owned by Publish (every publish marks the workflow active), so no
// SetPlanWorkflowActive(true) path exists to drift out of use.
//
// Implemented by the core conductorPublisher and detected by declare_plan via
// a type assertion, so this package stays decoupled from core: a publisher
// that does not implement it (or a nil publisher) is simply left as-is.
type planAbandoner interface {
	AbandonPlan()
}

// DeclarePlanTool publishes a roadmap and optionally blocks for user approval.
type DeclarePlanTool struct {
	*sdktools.BaseTool
	approvalFunc ApprovalFunc
}

// ApprovalFunc is called when declare_plan runs in await_approval mode.
// Returns the user's decision: "approve", "request_changes" (with feedback),
// or "abandon". If nil, await_approval mode is unavailable.
type ApprovalFunc func(ctx context.Context, planPath, planMarkdown string) (decision string, feedback string, err error)

// NewDeclarePlanTool creates the declare_plan tool. approvalFunc may be nil;
// in that case await_approval mode returns an error if invoked.
func NewDeclarePlanTool(approvalFunc ApprovalFunc) *DeclarePlanTool {
	return &DeclarePlanTool{
		BaseTool: &sdktools.BaseTool{
			ToolGroup:       sdktools.GroupSystem,
			ToolName:        "declare_plan",
			ToolDescription: toolDeclarePlanDescription,
			Schema: json.RawMessage(`{
	"type": "object",
	"properties": {
		"mode": {"type": "string", "enum": ["present", "await_approval"], "description": "present (default) displays the plan; await_approval blocks for user approval before returning"},
		"tasks": {
			"type": "array",
			"minItems": 1,
			"items": {
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Unique task identifier (e.g. step_1)"},
					"summary": {"type": "string", "description": "5-7 word label for UI display"},
					"description": {"type": "string", "description": "Full task description with What/How/Where/Acceptance Criteria"},
					"depends_on": {"type": "array", "items": {"type": "string"}, "description": "IDs of tasks that must complete before this one. A step with no depends_on runs CONCURRENTLY with its siblings — declare the dependency whenever this step consumes another step's output, artifacts, or decisions; an omitted link is a correctness bug, not a harmless omission (a spurious edge only serializes; a missing edge runs the steps in parallel)."},
					"agent": {"type": "string", "description": "Optional Subagent Profile name to execute this step with (e.g. \"code-reviewer\"). When set, the step runs with that profile's system prompt, tools, max-steps, and model instead of the orchestrator defaults. Omit for a generic step."}
				},
				"required": ["id", "summary", "description"]
			}
		}
	},
	"required": ["tasks"]
}`),
			Policy: sdktools.PolicyAlwaysAllow,
		},
		approvalFunc: approvalFunc,
	}
}

type declarePlanInput struct {
	Mode  string          `json:"mode"`
	Tasks []PlanTaskInput `json:"tasks"`
}

// validatePlanTasks checks that every task has a non-empty id, summary, and
// description, that task ids are unique within the plan, and that every
// depends_on entry (direct or transitive) references an id declared in the
// same plan. All violations are collected and reported together so the caller
// can fix the whole plan in one revision. Returns nil when the plan is valid.
func validatePlanTasks(tasks []PlanTaskInput) error {
	var problems []string
	// ids maps each declared non-empty id to the 1-based number of its first
	// occurrence, both for duplicate detection and reference resolution.
	ids := make(map[string]int, len(tasks))
	for i, task := range tasks {
		num := i + 1
		if strings.TrimSpace(task.ID) == "" {
			problems = append(problems, fmt.Sprintf("task %d: missing required field %q", num, "id"))
		}
		if strings.TrimSpace(task.Summary) == "" {
			problems = append(problems, fmt.Sprintf("task %d: missing required field %q", num, "summary"))
		}
		if strings.TrimSpace(task.Description) == "" {
			problems = append(problems, fmt.Sprintf("task %d: missing required field %q", num, "description"))
		}
		if id := strings.TrimSpace(task.ID); id != "" {
			if first, dup := ids[id]; dup {
				problems = append(problems, fmt.Sprintf("duplicate task id %q (tasks %d and %d)", id, first, num))
			} else {
				ids[id] = num
			}
		}
	}
	for i, task := range tasks {
		num := i + 1
		for _, dep := range task.DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				problems = append(problems, fmt.Sprintf("task %d: depends_on contains an empty task id", num))
				continue
			}
			if _, ok := ids[dep]; !ok {
				problems = append(problems, fmt.Sprintf("task %d: depends_on references unknown task id %q", num, dep))
			}
		}
	}
	if len(problems) > 0 {
		return errors.New("validation error: invalid plan tasks. Fix these issues and call declare_plan again:\n- " + strings.Join(problems, "\n- "))
	}
	// Acyclicity: depends_on must form a DAG. A cycle or self-dependency
	// passes reference resolution but can never be satisfied — execute_plan
	// would report it as an "upstream failure" instead of a malformed plan,
	// hiding the real fix (re-declare without the cycle).
	if cycle := planDependencyCycle(tasks); cycle != "" {
		return errors.New("validation error: invalid plan tasks. Fix these issues and call declare_plan again:\n- " + cycle)
	}
	return nil
}

// planDependencyCycle detects dependency cycles (including
// self-dependencies) via Kahn's algorithm and renders the offending ids; ""
// when the graph is a DAG. Ids are matched trimmed, exactly as the reference
// resolution in validatePlanTasks matches them.
func planDependencyCycle(tasks []PlanTaskInput) string {
	const selfDep = "task %q depends on itself — remove the self-referencing depends_on entry"
	ids := make([]string, len(tasks))
	degree := make(map[string]int, len(tasks))
	adj := make(map[string][]string, len(tasks))
	for i, t := range tasks {
		id := strings.TrimSpace(t.ID)
		ids[i] = id
		for _, dep := range t.DependsOn {
			d := strings.TrimSpace(dep)
			if d == "" {
				continue
			}
			if d == id {
				return fmt.Sprintf(selfDep, id)
			}
			adj[d] = append(adj[d], id)
			degree[id]++
		}
	}
	queue := make([]string, 0, len(tasks))
	for _, id := range ids {
		if degree[id] == 0 {
			queue = append(queue, id)
		}
	}
	processed := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		processed++
		for _, next := range adj[cur] {
			degree[next]--
			if degree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if processed == len(ids) {
		return ""
	}
	// Every id with residual in-degree sits on (or depends on) a cycle.
	stuck := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, id := range ids {
		if degree[id] > 0 {
			if _, dup := seen[id]; !dup {
				seen[id] = struct{}{}
				stuck = append(stuck, id)
			}
		}
	}
	return fmt.Sprintf("depends_on contains a dependency cycle involving: %s — a plan must be a DAG (reorder or remove the cyclic depends_on entries)", strings.Join(stuck, ", "))
}

// planExecutionWaves partitions a valid plan's steps into execution waves:
// wave 1 holds every dependency-free step, wave N+1 holds the steps whose
// prerequisites all live in waves <= N (Kahn's algorithm by layers). Each wave
// is therefore the maximal set of steps that may run concurrently, and the
// steps inside a wave keep their declaration order. Acyclicity is guaranteed
// upstream by validatePlanTasks, which rejects cyclic plans before this runs.
func planExecutionWaves(tasks []PlanTaskInput) [][]string {
	n := len(tasks)
	index := make(map[string]int, n)
	for i, task := range tasks {
		index[strings.TrimSpace(task.ID)] = i
	}
	// pending[i] counts task i's still-unmet prerequisites; dependents[j]
	// lists the tasks that name j in their depends_on.
	pending := make([]int, n)
	dependents := make([][]int, n)
	for i, task := range tasks {
		seen := make(map[string]struct{}, len(task.DependsOn))
		for _, dep := range task.DependsOn {
			id := strings.TrimSpace(dep)
			j, ok := index[id]
			if !ok || j == i {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			pending[i]++
			dependents[j] = append(dependents[j], i)
		}
	}
	placed := make([]bool, n)
	waves := make([][]string, 0, n)
	for done := 0; done < n; {
		var wave []string
		var ready []int
		for i, task := range tasks {
			if !placed[i] && pending[i] == 0 {
				wave = append(wave, strings.TrimSpace(task.ID))
				ready = append(ready, i)
			}
		}
		if len(ready) == 0 {
			// Unreachable for a validated plan (cycles are rejected before
			// this runs); emit the leftovers rather than spin forever.
			for i, task := range tasks {
				if !placed[i] {
					wave = append(wave, strings.TrimSpace(task.ID))
					ready = append(ready, i)
				}
			}
		}
		for _, i := range ready {
			placed[i] = true
			done++
		}
		for _, i := range ready {
			for _, dep := range dependents[i] {
				pending[dep]--
			}
		}
		waves = append(waves, wave)
	}
	return waves
}

// formatExecutionWaves renders planExecutionWaves as a compact echo, e.g.
// "Execution waves: 1=[step_1, step_3] · 2=[step_2] · 3=[step_4]". Each wave
// lists its step ids in declaration order.
func formatExecutionWaves(waves [][]string) string {
	rendered := make([]string, len(waves))
	for i, wave := range waves {
		rendered[i] = fmt.Sprintf("%d=[%s]", i+1, strings.Join(wave, ", "))
	}
	return "Execution waves: " + strings.Join(rendered, " · ")
}

// singleWaveHint warns that a multi-step plan collapsed into one concurrent
// wave, so every step will run in parallel unless dependencies are declared.
func singleWaveHint(steps int) string {
	return fmt.Sprintf("All %d steps are in a single parallel wave — every step will run concurrently. If any step consumes another's output, re-declare with depends_on before executing.", steps)
}

func (t *DeclarePlanTool) Execute(ctx context.Context, input json.RawMessage) (sdktools.ToolResult, error) {
	var params declarePlanInput
	if err := json.Unmarshal(input, &params); err != nil {
		return sdktools.ParseInputError(err)
	}
	if len(params.Tasks) == 0 {
		return sdktools.ErrorResult("validation error: tasks array must not be empty"), nil
	}
	mode := params.Mode
	if mode == "" {
		mode = "present"
	}
	if mode != "present" && mode != "await_approval" {
		return sdktools.ErrorResult("validation error: mode must be \"present\" or \"await_approval\", got %q", mode), nil
	}
	// Schema-level validation runs before the continuation guard and before
	// Publish, so a malformed plan never reaches the filesystem, the
	// blackboard, or the approval flow — even on a resumed task.
	if err := validatePlanTasks(params.Tasks); err != nil {
		return sdktools.ErrorResult("%s", err), nil
	}

	publisher := PlanPublisherFrom(ctx)
	if publisher == nil {
		return sdktools.ErrorResult("declare_plan: no plan publisher in context (not running inside a Conductor)"), nil
	}

	// Continuation guard: this Conductor run resumed a task whose approved
	// plan still has unreached steps. The plan on the blackboard is still
	// authoritative — re-declaring would replace the approved roadmap and
	// reset step progress. Return a soft (non-error) hint pointing back to
	// execute_plan, which resumes the remaining steps and skips the ones that
	// already succeeded. Detected via the optional PlanContinuation capability
	// on the context's PlanChecker (implemented by the core conductorLauncher),
	// so plain Conductor runs are unaffected.
	if pc := PlanCheckerFrom(ctx); pc != nil {
		if cont, ok := pc.(PlanContinuation); ok && cont.PlanContinuation() {
			return sdktools.ToolResult{Content: "A plan has already been declared and approved for this resumed task — do not re-declare it. Call execute_plan to continue the remaining steps (already-completed steps are skipped automatically)."}, nil
		}
	}

	planPath, err := publisher.Publish(ctx, params.Tasks)
	if err != nil {
		return sdktools.ErrorResult("declare_plan: failed to publish plan: %v", err), nil
	}

	// Echo the plan's execution waves back to the model so a mis-declared
	// dependency graph is visible before execute_plan runs. Steps were already
	// validated (validatePlanTasks guarantees a DAG), so the layering is total.
	waves := planExecutionWaves(params.Tasks)
	waveEcho := formatExecutionWaves(waves)

	if mode == "present" {
		// A plan whose steps collapse into one wave runs fully concurrently,
		// so a multi-step single-wave plan also carries a non-blocking hint.
		content := fmt.Sprintf("Plan published to %s and displayed in the plan panel. Execution continues.\n\n%s", planPath, waveEcho)
		if len(waves) == 1 && len(params.Tasks) > 1 {
			content += "\n\n" + singleWaveHint(len(params.Tasks))
		}
		return sdktools.ToolResult{Content: content}, nil
	}

	if t.approvalFunc == nil {
		return sdktools.ErrorResult("declare_plan: await_approval mode is not available (no approval callback configured)"), nil
	}

	decision, feedback, err := t.approvalFunc(ctx, planPath, publisher.LastPlanMarkdown())
	if err != nil {
		return sdktools.ErrorResult("declare_plan: approval callback failed: %v", err), nil
	}

	switch decision {
	case "approve":
		return sdktools.ToolResult{Content: "Plan approved by user. Proceeding with implementation.\n\n" + waveEcho}, nil
	case "request_changes":
		msg := "User requested changes to the plan."
		if feedback != "" {
			msg += " Feedback: " + feedback
		}
		msg += "\n\nRevise the plan and call declare_plan again with the updated tasks."
		return sdktools.ToolResult{Content: msg}, nil
	case "abandon":
		// Release the plan workflow this run acquired when the plan was
		// published for review (conductorPublisher.Publish marks it declared)
		// AND settle the abandoned plan's steps as terminal. An abandoned plan
		// must not leave the run locked: delegate stays disabled, a standalone
		// (empty step_id) checklist is rejected, execute_plan is willing to run
		// the abandoned draft, and — because the run is no longer plan-active —
		// the finish-fallback sweep would never reach the abandoned plan's
		// steps, so they would otherwise stay "pending" in the plan panel
		// forever. The plan itself stays on the blackboard as a historical
		// artifact (not cleared here). approve/present (Publish already marked)
		// and request_changes (the model re-declares, so delegate correctly
		// stays disabled) leave the workflow active. Detected via the optional
		// planAbandoner capability, so a publisher without it is unaffected.
		if ab, ok := publisher.(planAbandoner); ok {
			ab.AbandonPlan()
		}
		return sdktools.ToolResult{Content: "User abandoned the plan. Do not proceed with implementation unless the user gives new instructions.", IsError: true}, nil
	default:
		return sdktools.ToolResult{Content: fmt.Sprintf("Approval callback returned unknown decision %q; treating as request_changes.", decision)}, nil
	}
}

// --- Context plumbing ---

type planPublisherKey struct{}

// WithPlanPublisher injects the publisher into the context.
func WithPlanPublisher(ctx context.Context, publisher PlanPublisher) context.Context {
	return context.WithValue(ctx, planPublisherKey{}, publisher)
}

// PlanPublisherFrom extracts the publisher from the context, or returns nil.
func PlanPublisherFrom(ctx context.Context) PlanPublisher {
	if v, ok := ctx.Value(planPublisherKey{}).(PlanPublisher); ok {
		return v
	}
	return nil
}
