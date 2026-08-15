package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdktools "github.com/v0lka/sp4rk/tools"

	"github.com/v0lka/sp4rk/agents"
)

const toolDelegateDescription = `Launch one or more subagents to execute units of work in isolated ReAct loops. Each task runs in its own context with its own tool set; only the summary output returns. Use this to break large tasks into parallel or sequential pieces, or to isolate context-heavy investigation. Tasks with depends_on wait for their dependencies to complete first; tasks without dependencies run in parallel. blocking mode returns the output in the tool result; async mode returns immediately with a delegation_id (read results later via read_step_output). depends_on can only reference blocking tasks — async tasks run in the background and cannot be depended upon. By default subagents cannot delegate further; set allow_redelegate=true to permit nesting (capped by config).`

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
	// Agent names a Subagent Profile (an .agents/agents/<name>/AGENT.md file).
	// When set, the launcher resolves the profile and applies it: the agent's
	// body replaces the orchestrator system prompt (the shared project-context
	// prefix is preserved), and the profile's tool preference / max-steps /
	// model / allow-redelegate override the task fields. Empty = no profile
	// (the legacy behavior). An unknown name fails fast (delegate validation
	// rejects it before any subagent launches).
	Agent string `json:"agent,omitempty"`
}

// DelegationResult is the outcome of a single delegation as returned by the launcher.
type DelegationResult struct {
	ID     string
	Status DelegationStatus
	Output string
	Error  error
}

// DelegationLauncher builds and runs subagents for the delegate tool.
// The implementation lives in the core layer and knows how to construct
// an Executor, ContextManager, scoped emitter, and tool set for each task.
type DelegationLauncher interface {
	Launch(ctx context.Context, tasks []DelegationTask, registry *DelegationRegistry) []DelegationResult
}

// AgentResolver looks up a Subagent Profile by name. It is injected into the
// Conductor context (and inherited by subagent contexts) so the launcher can
// resolve the `agent` field of a DelegationTask to a full *agents.Agent profile.
// Returns (nil, false) when the name is unknown — validateDelegationTasks turns
// that into a fail-fast error before any subagent launches. Nil (no resolver in
// context) means profiles are unavailable: a non-empty `agent` field is then
// rejected, so a delegate call never silently ignores a requested profile.
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
			Schema: json.RawMessage(`{
	"type": "object",
	"properties": {
		"tasks": {
			"type": "array",
			"minItems": 1,
			"items": {
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Unique delegation identifier (e.g. del_1)"},
					"summary": {"type": "string", "description": "5-7 word label for UI display"},
					"task": {"type": "string", "description": "Full task description with What/How/Where/Acceptance Criteria"},
					"acceptance_criteria": {"type": "array", "items": {"type": "string"}, "description": "Optional explicit criteria the subagent verifies before finishing"},
					"tools": {
						"description": "Capability groups granted to the subagent, on top of the always-included system group (finish, facts, checklist, meta tools): \"all\" (default — full toolset minus Conductor-only tools), \"read-only\" (read-only exploration: local-read + remote-read, no MCP), or an array of group names to grant selectively (e.g. [\"local-read\",\"execute\"]).",
						"anyOf": [
							{"type": "string", "enum": ["all", "read-only"]},
							{"type": "array", "items": {"type": "string", "enum": ["execute", "local-read", "local-write", "remote-read", "remote-write", "local-mcp", "remote-mcp", "system"]}}
						]
					},
					"depends_on": {"type": "array", "items": {"type": "string"}, "description": "IDs of delegations that must complete before this one starts"},
					"mode": {"type": "string", "enum": ["blocking", "async"], "description": "blocking (default) returns output in result; async returns delegation_id immediately"},
					"max_steps": {"type": "integer", "description": "Per-subagent ReAct iteration cap; 0 = config default"},
					"allow_redelegate": {"type": "boolean", "description": "Grant the subagent the delegate tool (capped by config maxRedelegationDepth); default false"},
					"agent": {"type": "string", "description": "Name of a Subagent Profile (.agents/agents/<name>/AGENT.md) to apply. When set, the profile's system prompt, tool preference, max-steps, model, and allow-redelegate override the task fields. Unknown name fails fast."}
				},
				"required": ["id", "summary", "task"]
			}
		}
	},
	"required": ["tasks"]
}`),
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

	// Orthogonality guard: when a plan is declared on the blackboard, delegate
	// is disabled — execute_plan is the only execution path for plan steps.
	// delegate is for plan-less task optimization only. The PlanChecker is nil
	// in subagent contexts, so this guard is inert for redelegation.
	if pc := PlanCheckerFrom(ctx); pc != nil && pc.HasDeclaredPlan() {
		return sdktools.ErrorResult("delegate is disabled while a plan is declared — use execute_plan to execute plan steps. delegate is for plan-less task optimization only."), nil
	}

	// Resolve the agent-profile resolver (if any) before validation so a
	// non-empty `agent` field can be checked for existence up front. Absent
	// resolver → any requested profile is rejected (fail fast rather than
	// silently ignoring it).
	agentResolver := AgentResolverFrom(ctx)
	if err := validateDelegationTasks(params.Tasks, registry, agentResolver); err != nil {
		return sdktools.ErrorResult("delegate validation failed: %v", err), nil
	}

	for _, task := range params.Tasks {
		mode := task.Mode
		if mode == "" {
			mode = "blocking"
		}
		if err := registry.Register(task.ID, task.Summary, task.DependsOn, mode); err != nil {
			return sdktools.ErrorResult("delegate registration failed: %v", err), nil
		}
	}

	results := launcher.Launch(ctx, params.Tasks, registry)
	return buildDelegateToolResult(results), nil
}

func buildDelegateToolResult(results []DelegationResult) sdktools.ToolResult {
	var blocking []DelegationResult
	var asyncIDs []string
	var failed []string
	for _, r := range results {
		switch r.Status {
		case DelegationStatusCompleted, DelegationStatusFailed:
			blocking = append(blocking, r)
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
	if len(failed) > 0 && len(blocking) > 0 {
		fmt.Fprintf(&sb, "Note: %d delegation(s) failed: %s. Consider calling reflect to analyze, or revise and re-delegate.\n", len(failed), strings.Join(failed, ", "))
	}
	if sb.Len() == 0 {
		return sdktools.ToolResult{Content: "No delegations were launched."}
	}
	return sdktools.ToolResult{Content: sb.String()}
}

// validateDelegationTools validates a delegation task's `tools` field: nil
// (omit), the strings ""/"all"/"read-only", or an array of capability-group
// tokens (kebab or underscore spelling; canonicalized by the resolver). Any
// other shape fails with a message listing the valid values — the alternative
// (silently degrading to the safe minimum at launch time) hides a broken
// toolset request from the Conductor.
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

// validateDelegationTasks checks IDs are unique within the batch, depends_on
// references exist (in the batch or the registry), the combined graph is
// acyclic, modes are valid, and — when an AgentResolver is present — every
// non-empty `agent` field names a known Subagent Profile. A non-empty agent
// with no resolver in context is rejected so a requested profile is never
// silently ignored.
func validateDelegationTasks(tasks []DelegationTask, registry *DelegationRegistry, agentResolver AgentResolver) error {
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
		// Validate the tools request up front: unknown strings/groups fail the
		// whole delegate call with a message listing the valid values, instead
		// of launching a subagent whose toolset silently collapsed to the
		// always-granted system group (fail-closed, but loud).
		if err := validateDelegationTools(task.Tools); err != nil {
			return fmt.Errorf("task %q: %w", task.ID, err)
		}
		// Validate the agent profile name up front so an unknown name fails
		// fast (no subagent launches) with a clear message. When no resolver
		// is present (profiles unavailable), a requested profile is rejected
		// rather than silently ignored.
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
			// An async task cannot be a dependency — it returns immediately
			// and runs in the background, so dependents would never see it
			// "completed" within the Launch wave loop. Check both the current
			// batch and the registry (cross-batch: the async task may have
			// been registered in a previous delegate call).
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

// detectDelegationCycle checks the combined graph (new tasks + registry's
// existing delegations) for cycles via DFS. Returns a cycle description or "".
// Registry delegations are treated as already-validated roots; only edges
// from the new tasks are walked.
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

// PlanChecker reports whether a plan has been declared on the blackboard.
// Used by delegate to enforce orthogonality: once a plan is declared, delegate
// is disabled and execute_plan is the only execution path for plan steps.
// The PlanChecker is injected only into the Conductor's context — subagent
// contexts do not carry it (subagentCtx strips Conductor-only values), so the
// guard is inert for subagent redelegation.
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

// WithAgentResolver injects the resolver into the context so buildSubAgentTask
// can resolve the `agent` field of a DelegationTask. The resolver is inherited
// by subagent contexts (it is NOT stripped by subagentCtx), so a redelegating
// subagent can itself delegate to a named agent profile.
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
