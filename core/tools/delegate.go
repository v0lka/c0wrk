package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdktools "github.com/v0lka/sp4rk/tools"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agents"
)

const toolDelegateDescription = `Purpose: launch subagents that each run a task in their own context window — parallelize independent work, keep the main context lean.
Use when: tasks are independent and parallelizable, or the main context risks overflow. The subagent sees the blackboard and its brief, not full chat history.
Inputs: tasks — array of {id (unique, e.g. del_1), summary (5-7 word UI label), task (What/How/Where/Acceptance criteria), acceptance_criteria (optional explicit checks), tools (optional: "all" | "read-only" bare strings, or group-token array — never ["read-only"]), depends_on (blocking delegation ids that must finish first; async ids rejected), mode ("blocking" | "async"), max_steps (0 = config default), allow_redelegate (default false), agent (optional; must name an existing Subagent Profile)}.
Outputs: progress events per subagent; blocking tasks return output here, async ones return a delegation id.
Example: two independent refactors as two tasks without depends_on — they run in parallel.
Anti-example: no re-delegation from inside a subagent unless allow_redelegate is set; not for sequential steps (chain via depends_on or run inline); delegating does not replace reading key files.`

// maxDelegationBatchSize bounds one delegate call: the concurrency cap limits
// how many subagents RUN at once, not how many a single call may queue, so an
// unbounded batch would still register hundreds of goroutines and units. The
// schema's maxItems mirrors this constant.
const maxDelegationBatchSize = 16

// delegateSchemaTemplate is the delegate tool's JSON schema; the tasks[].tools
// group enum is injected at construction so it cannot drift from the resolver.
const delegateSchemaTemplate = `{
	"type": "object",
	"properties": {
		"tasks": {
			"type": "array",
			"minItems": 1,
			"maxItems": __MAX_TASKS__,
			"items": {
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Unique delegation identifier (e.g. del_1)"},
					"summary": {"type": "string", "description": "5-7 word label for UI display"},
					"task": {"type": "string", "description": "Full task description with What/How/Where/Acceptance Criteria"},
					"acceptance_criteria": {"type": "array", "items": {"type": "string"}, "description": "Optional explicit criteria the subagent verifies before finishing"},
					"tools": {
						"description": "Capability groups granted to the subagent, on top of the always-included system group (finish, facts, checklist, meta tools — but NOT the goal-loop tools, which never reach a subagent): \"all\" (default — full toolset minus Conductor-only tools), \"read-only\" (read-only exploration: local-read + remote-read, no MCP), or a non-empty array of group names to grant selectively (e.g. [\"local-read\",\"execute\"]); [\"system\"] alone grants only the system group. Kebab-case (\"local-read\") is the canonical spelling; the underscore spelling of the same tokens (\"local_read\") is also accepted, mirroring Subagent Profile AGENT.md tool lists. \"all\" and \"read-only\" are bare-string presets only — never valid inside the array ([\"read-only\"] is rejected); the array form carries group tokens only.",
						"anyOf": [
							{"type": "string", "enum": ["all", "read-only"]},
							{"type": "array", "minItems": 1, "items": {"type": "string", "enum": ["__GROUP_TOKENS__"]}}
						]
					},
					"depends_on": {"type": "array", "items": {"type": "string"}, "description": "IDs of BLOCKING delegations that must complete before this one starts — from this batch or an earlier delegate call in the same Conductor run. Async delegations cannot be depended upon (rejected at validation); read their results via read_step_output instead. A task cannot depend on itself, and the combined dependency graph must be acyclic."},
					"mode": {"type": "string", "enum": ["blocking", "async"], "description": "blocking (default) returns output in result; async returns delegation_id immediately"},
					"max_steps": {"type": "integer", "description": "Per-subagent ReAct iteration cap; 0 = config default"},
					"allow_redelegate": {"type": "boolean", "description": "Grant the subagent the delegate tool (capped by config maxRedelegationDepth); default false"},
					"agent": {"type": "string", "description": "Must name an existing Subagent Profile (.agents/agents/<name>/AGENT.md) — an unknown name fails the whole delegate call before any subagent launches. When set, the profile's system prompt, tool preference, max-steps, model, and allow-redelegate override the task fields."}
				},
				"required": ["id", "summary", "task"]
			}
		}
	},
	"required": ["tasks"]
}`

// delegateGroupEnumJSON renders every group token the delegation resolver
// accepts, derived from sdktools.AllToolGroups in both spellings — a literal
// here would be a third copy of the vocabulary able to drift.
func delegateGroupEnumJSON() string {
	seen := make(map[string]struct{}, 16)
	var tokens []string
	for _, g := range sdktools.AllToolGroups() {
		for _, tok := range []string{string(g), strings.ReplaceAll(string(g), "_", "-")} {
			if _, dup := seen[tok]; dup {
				continue
			}
			seen[tok] = struct{}{}
			tokens = append(tokens, strconv.Quote(tok))
		}
	}
	return strings.Join(tokens, ",")
}

// DelegationTask describes a single subagent invocation.
type DelegationTask struct {
	ID                 string   `json:"id"`
	Summary            string   `json:"summary"`
	Task               string   `json:"task"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
	Tools              any      `json:"tools,omitempty"`
	DependsOn          []string `json:"depends_on,omitempty"`
	Mode               string   `json:"mode,omitempty"`
	MaxSteps           int      `json:"max_steps,omitempty"`
	AllowRedelegate    bool     `json:"allow_redelegate,omitempty"`
	// Agent names a Subagent Profile applied by the launcher (prompt, tools,
	// max-steps, model, allow-redelegate); unknown names fail validation
	// before any launch. See specs/domains/orchestration/delegation.md.
	Agent string `json:"agent,omitempty"`
}

// DelegationResult is the outcome of a single delegation as returned by the launcher.
type DelegationResult struct {
	ID     string
	Status DelegationStatus
	Output string
	Error  error
}

// DelegationLauncher builds and runs subagents for the delegate tool; the
// implementation lives in the core layer.
type DelegationLauncher interface {
	Launch(ctx context.Context, tasks []DelegationTask, registry *DelegationRegistry) []DelegationResult
	// CompletedStep feeds the duplicate guard: a delegation id that already
	// settled must be refused and replayed, never re-run.
	// See specs/contracts/conductor-tools.md (delegate).
	CompletedStep(id string) (DelegationCompletedStep, bool)
}

// DelegationCompletedStep is the settled outcome (auto-resume wave or an
// earlier leg) replayed into the registry so co-launched dependents resolve.
type DelegationCompletedStep struct {
	Output string
	Steps  []agent.Step
}

// AgentResolver looks up a Subagent Profile by name (see
// specs/domains/orchestration/delegation.md). Unknown names fail validation
// before any launch; with no resolver in context a requested profile is
// rejected rather than silently ignored.
type AgentResolver func(name string) (*agents.Agent, bool)

// DelegateTool launches subagents via a DelegationLauncher injected through context.
type DelegateTool struct {
	*sdktools.BaseTool
}

// NewDelegateTool creates the delegate tool.
func NewDelegateTool() *DelegateTool {
	return &DelegateTool{
		BaseTool: &sdktools.BaseTool{
			ToolGroup:       sdktools.GroupSystem,
			ToolName:        "delegate",
			ToolDescription: toolDelegateDescription,
			Schema: json.RawMessage(strings.Replace(
				strings.Replace(
					delegateSchemaTemplate,
					`"__GROUP_TOKENS__"`,
					delegateGroupEnumJSON(),
					1,
				),
				"__MAX_TASKS__",
				strconv.Itoa(maxDelegationBatchSize),
				1,
			)),
			Policy: sdktools.PolicyAlwaysAllow,
			// ASI07: subagent output may carry content a subagent read from
			// external sources (indirect-injection vector). Treat as untrusted.
			Untrusted: true,
		},
	}
}

type delegateInput struct {
	Tasks []DelegationTask `json:"tasks"`
}

func (t *DelegateTool) Execute(ctx context.Context, input json.RawMessage) (sdktools.ToolResult, error) {
	var params delegateInput
	if err := json.Unmarshal(input, &params); err != nil {
		return sdktools.ParseInputError(err)
	}
	if len(params.Tasks) == 0 {
		return sdktools.ErrorResult("validation error: tasks array must not be empty"), nil
	}

	registry := DelegationRegistryFrom(ctx)
	if registry == nil {
		return sdktools.ErrorResult("delegate tool: no delegation registry in context (not running inside a Conductor)"), nil
	}
	launcher := DelegationLauncherFrom(ctx)
	if launcher == nil {
		return sdktools.ErrorResult("delegate tool: no delegation launcher in context (not running inside a Conductor)"), nil
	}

	// Orthogonality: once a plan is declared, execute_plan is the only
	// execution path (specs/contracts/conductor-tools.md). The checker is nil
	// in subagent contexts, so redelegation is unaffected.
	if pc := PlanCheckerFrom(ctx); pc != nil && pc.HasDeclaredPlan() {
		return sdktools.ErrorResult("delegate is disabled while a plan is declared — use execute_plan to execute plan steps. delegate is for plan-less task optimization only."), nil
	}

	// Resolve the profile resolver first so existence is checked up front.
	agentResolver := AgentResolverFrom(ctx)
	if err := validateDelegationTasks(params.Tasks, registry, agentResolver); err != nil {
		return sdktools.ErrorResult("delegate validation failed: %v", err), nil
	}

	// Duplicate guard, BEFORE registration so a refusal never re-fires the
	// spec sink: an id already settled on the blackboard (auto-resume wave or
	// a prior leg) is replayed via Register (no sink) so dependents resolve,
	// and refused as fresh work.
	results := make([]DelegationResult, 0, len(params.Tasks))
	runnable := make([]DelegationTask, 0, len(params.Tasks))
	for _, task := range params.Tasks {
		if done, ok := launcher.CompletedStep(task.ID); ok {
			mode := task.Mode
			if mode == "" {
				mode = "blocking"
			}
			if err := registry.Register(task.ID, task.Summary, task.DependsOn, mode); err == nil {
				registry.Complete(task.ID, done.Output, nil, done.Steps)
			}
			results = append(results, DelegationResult{
				ID:     task.ID,
				Status: DelegationStatusFailed,
				Error:  fmt.Errorf("delegation %q already completed earlier in this task — its output is on the blackboard (read_step_output); delegate fresh work under a new id", task.ID),
			})
			continue
		}
		runnable = append(runnable, task)
	}

	for _, task := range runnable {
		if err := registry.RegisterTask(task); err != nil {
			return sdktools.ErrorResult("delegate registration failed: %v", err), nil
		}
	}

	if len(runnable) > 0 {
		results = append(results, launcher.Launch(ctx, runnable, registry)...)
	}
	return buildDelegateToolResult(results), nil
}

func buildDelegateToolResult(results []DelegationResult) sdktools.ToolResult {
	var blocking []DelegationResult
	var asyncIDs []string
	var failed []string
	var paused []string
	for _, r := range results {
		switch r.Status {
		case DelegationStatusCompleted, DelegationStatusFailed:
			blocking = append(blocking, r)
		case DelegationStatusPaused:
			paused = append(paused, r.ID)
		case DelegationStatusRunning, DelegationStatusPending:
			asyncIDs = append(asyncIDs, r.ID)
		}
		if r.Status == DelegationStatusFailed {
			failed = append(failed, r.ID)
		}
	}

	var sb strings.Builder
	if len(blocking) > 0 {
		sb.WriteString("## Delegation results\n\n")
		for _, r := range blocking {
			fmt.Fprintf(&sb, "### %s\n", r.ID)
			if r.Error != nil {
				fmt.Fprintf(&sb, "Status: failed\nError: %s\n\n", r.Error.Error())
			} else {
				fmt.Fprintf(&sb, "Status: completed\nOutput:\n%s\n\n", r.Output)
			}
		}
	}
	if len(asyncIDs) > 0 {
		sb.WriteString("## Async delegations launched\n\n")
		for _, id := range asyncIDs {
			fmt.Fprintf(&sb, "- %s (running in background; read results via read_step_output(id=%q))\n", id, id)
		}
		sb.WriteString("\n")
	}
	if len(paused) > 0 {
		sb.WriteString("## Delegations paused\n\n")
		for _, id := range paused {
			fmt.Fprintf(&sb, "- %s (paused by the user at a step boundary; its partial trajectory is checkpointed and the system resumes it automatically when the task resumes)\n", id)
		}
		sb.WriteString("\n")
	}
	if len(failed) > 0 && len(blocking) > 0 {
		fmt.Fprintf(&sb, "Note: %d delegation(s) failed: %s. Consider calling reflect to analyze, or revise and re-delegate.\n", len(failed), strings.Join(failed, ", "))
	}
	if sb.Len() == 0 {
		return sdktools.ToolResult{Content: "No delegations were launched."}
	}
	return sdktools.ToolResult{Content: sb.String()}
}

// validateDelegationTools validates the `tools` field: nil, ""/"all"/"read-only",
// or a non-empty group-token array. An empty array is rejected, not silently
// resolved to a system-only grant (["system"] says that); any other shape
// fails loudly — a silently degraded toolset would hide a broken request.
func validateDelegationTools(v any) error {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		switch t {
		case "", "all", "read-only":
			return nil
		}
		return fmt.Errorf("tools: invalid value %q (valid strings: \"all\", \"read-only\"; or an array of group names: %s)",
			t, strings.Join(agents.ToolGroupTokens(), ", "))
	case []any:
		if len(t) == 0 {
			return errors.New("tools: empty array — omit the field (or pass \"all\") for the full toolset; to grant only the always-included system group, pass [\"system\"]")
		}
		for _, item := range t {
			s, isString := item.(string)
			if !isString {
				return fmt.Errorf("tools: group names must be strings, got %T", item)
			}
			if err := validateDelegationGroupToken(s); err != nil {
				return err
			}
		}
		return nil
	case []string:
		if len(t) == 0 {
			return errors.New("tools: empty array — omit the field (or pass \"all\") for the full toolset; to grant only the always-included system group, pass [\"system\"]")
		}
		for _, s := range t {
			if err := validateDelegationGroupToken(s); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("tools: unexpected type %T (valid: \"all\", \"read-only\", or an array of group names)", v)
	}
}

// validateDelegationGroupToken checks a single tools-array element: it must be
// a declared capability-group token. "all"/"read-only" are string-only values
// and are rejected inside arrays with a targeted hint.
func validateDelegationGroupToken(s string) error {
	switch s {
	case "all", "read-only":
		return fmt.Errorf("tools: %q must be passed as a plain string, not inside an array", s)
	}
	if _, ok := agents.NormalizeToolGroupToken(s); !ok {
		return fmt.Errorf("tools: unknown tool group %q (valid groups: %s)", s, strings.Join(agents.ToolGroupTokens(), ", "))
	}
	return nil
}

// validateDelegationTasks enforces the delegate contract — unique ids,
// resolvable depends_on, acyclic combined graph, valid modes, resolvable
// agent profiles. See specs/domains/orchestration/delegation.md.
func validateDelegationTasks(tasks []DelegationTask, registry *DelegationRegistry, agentResolver AgentResolver) error {
	if len(tasks) > maxDelegationBatchSize {
		return fmt.Errorf("too many tasks: %d (max %d per delegate call) — split the work across multiple delegate calls", len(tasks), maxDelegationBatchSize)
	}
	ids := make(map[string]int, len(tasks))
	for i, task := range tasks {
		if task.ID == "" {
			return fmt.Errorf("task %d: id is required", i+1)
		}
		if task.Summary == "" {
			return fmt.Errorf("task %q: summary is required", task.ID)
		}
		if task.Task == "" {
			return fmt.Errorf("task %q: task description is required", task.ID)
		}
		if _, exists := ids[task.ID]; exists {
			return fmt.Errorf("duplicate task id %q within delegate call", task.ID)
		}
		if registry.Has(task.ID) {
			return fmt.Errorf("task id %q already exists in this Conductor run", task.ID)
		}
		ids[task.ID] = i
		mode := task.Mode
		if mode == "" {
			mode = "blocking"
		}
		if mode != "blocking" && mode != "async" {
			return fmt.Errorf("task %q: mode must be \"blocking\" or \"async\", got %q", task.ID, mode)
		}
		// Unknown tools/groups fail the whole call loudly — never a silently
		// collapsed toolset.
		if err := validateDelegationTools(task.Tools); err != nil {
			return fmt.Errorf("task %q: %w", task.ID, err)
		}
		if task.Agent != "" {
			if agentResolver == nil {
				return fmt.Errorf("task %q: agent %q requested but no agent resolver is configured (Subagent Profiles unavailable)", task.ID, task.Agent)
			}
			if _, ok := agentResolver(task.Agent); !ok {
				return fmt.Errorf("task %q: unknown agent %q — no Subagent Profile with that name was found", task.ID, task.Agent)
			}
		}
	}

	for _, task := range tasks {
		for _, dep := range task.DependsOn {
			if dep == task.ID {
				return fmt.Errorf("task %q: cannot depend on itself", task.ID)
			}
			if _, inBatch := ids[dep]; !inBatch && !registry.Has(dep) {
				return fmt.Errorf("task %q: depends_on references unknown delegation %q", task.ID, dep)
			}
			// An async dependency can never be observed "completed" by the
			// wave loop (delegation.md); check batch and registry alike.
			if depTask, ok := findTaskByID(tasks, dep); ok {
				depMode := depTask.Mode
				if depMode == "" {
					depMode = "blocking"
				}
				if depMode == "async" {
					return fmt.Errorf("task %q: depends_on references async task %q — async delegations cannot be depended upon; read their results via read_step_output instead", task.ID, dep)
				}
			} else if regDep := registry.Get(dep); regDep != nil {
				depMode := regDep.Mode
				if depMode == "" {
					depMode = "blocking"
				}
				if depMode == "async" {
					return fmt.Errorf("task %q: depends_on references async task %q — async delegations cannot be depended upon; read their results via read_step_output instead", task.ID, dep)
				}
			}
		}
	}

	if cycle := detectDelegationCycle(tasks, registry); cycle != "" {
		return fmt.Errorf("dependency cycle detected: %s", cycle)
	}

	return nil
}

// detectDelegationCycle DFS-checks the combined graph; registry delegations
// are already-validated roots, so only new-task edges are walked.
func detectDelegationCycle(tasks []DelegationTask, registry *DelegationRegistry) string {
	adj := make(map[string][]string)
	for _, task := range tasks {
		adj[task.ID] = append(adj[task.ID], task.DependsOn...)
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int)

	var dfs func(node string, stack []string) []string
	dfs = func(node string, stack []string) []string {
		if color[node] == gray {
			for i, n := range stack {
				if n == node {
					return append(stack[i:], node)
				}
			}
			return []string{node, node}
		}
		if color[node] == black {
			return nil
		}
		color[node] = gray
		for _, dep := range adj[node] {
			if registry.Has(dep) {
				continue
			}
			nextStack := append([]string(nil), stack...)
			nextStack = append(nextStack, node)
			if cycle := dfs(dep, nextStack); cycle != nil {
				return cycle
			}
		}
		color[node] = black
		return nil
	}

	for _, task := range tasks {
		if color[task.ID] == white {
			if cycle := dfs(task.ID, nil); cycle != nil {
				return strings.Join(cycle, " -> ")
			}
		}
	}
	return ""
}

// --- Context plumbing ---

// findTaskByID returns the task with the given ID from the batch, or false.
func findTaskByID(tasks []DelegationTask, id string) (DelegationTask, bool) {
	for _, t := range tasks {
		if t.ID == id {
			return t, true
		}
	}
	return DelegationTask{}, false
}

type delegationLauncherKey struct{}

// WithDelegationLauncher injects the launcher into the context so the delegate
// tool can call it without importing core.
func WithDelegationLauncher(ctx context.Context, launcher DelegationLauncher) context.Context {
	return context.WithValue(ctx, delegationLauncherKey{}, launcher)
}

// DelegationLauncherFrom extracts the launcher from the context, or returns nil.
func DelegationLauncherFrom(ctx context.Context) DelegationLauncher {
	if v, ok := ctx.Value(delegationLauncherKey{}).(DelegationLauncher); ok {
		return v
	}
	return nil
}

// PlanChecker enforces the plan/delegate orthogonality
// (specs/contracts/conductor-tools.md); subagent contexts strip it, so
// redelegation is unaffected.
type PlanChecker interface {
	HasDeclaredPlan() bool
}

type planCheckerKey struct{}

// WithPlanChecker injects a PlanChecker into the context.
func WithPlanChecker(ctx context.Context, pc PlanChecker) context.Context {
	return context.WithValue(ctx, planCheckerKey{}, pc)
}

// PlanCheckerFrom extracts the PlanChecker from the context, or returns nil.
func PlanCheckerFrom(ctx context.Context) PlanChecker {
	if v, ok := ctx.Value(planCheckerKey{}).(PlanChecker); ok {
		return v
	}
	return nil
}

// --- Agent profile resolver plumbing ---

type agentResolverKey struct{}

// WithAgentResolver injects the profile resolver; unlike the Conductor-only
// handles it is NOT stripped by subagentCtx, so redelegation can name profiles.
func WithAgentResolver(ctx context.Context, resolver AgentResolver) context.Context {
	return context.WithValue(ctx, agentResolverKey{}, resolver)
}

// AgentResolverFrom extracts the AgentResolver from the context, or returns nil.
func AgentResolverFrom(ctx context.Context) AgentResolver {
	if v, ok := ctx.Value(agentResolverKey{}).(AgentResolver); ok {
		return v
	}
	return nil
}
