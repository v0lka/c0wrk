package planner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/skills"
	"github.com/v0lka/sp4rk/tools"
)

// ---------------------------------------------------------------------------
// Mock LLMCaller
// ---------------------------------------------------------------------------

type mockLLMCaller struct {
	resp  *llm.ChatResponse
	err   error
	calls int
}

func (m *mockLLMCaller) Call(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}

func newPlanResponse(content string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{Role: "assistant", Content: content},
	}
}

// ---------------------------------------------------------------------------
// Mock ToolRegistry
// ---------------------------------------------------------------------------

type mockToolRegistry struct {
	tools []tools.ToolDescriptor
}

func (m *mockToolRegistry) List() []tools.ToolDescriptor { return m.tools }
func (m *mockToolRegistry) Execute(_ context.Context, _ string, _ json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{}, nil
}
func (m *mockToolRegistry) GetToolSource(_ string) string { return "" }
func (m *mockToolRegistry) IsToolUntrusted(_ string) bool { return false }

// ---------------------------------------------------------------------------
// Helper to create a Config with minimal wired-in functions
// ---------------------------------------------------------------------------

func makeTestConfig(basePrompt string) Config {
	return Config{
		Prompts: PromptSet{
			BasePrompt:          basePrompt,
			PlanPreamble:        "preamble",
			MultiStepToT:        "tot",
			MultiStepGuidance:   "guidance",
			ExtraSections:       "extras",
			DomainAssignment:    "domain",
			AgentProfiles:       "profiles",
			VerificationMandate: "verify",
			FamilyPrompt:        nil,
		},
		DomainFromContext:     func(context.Context) string { return "" },
		ComplexityFromContext: func(context.Context) int { return 0 },
		FormatSkillList:       func(context.Context, []skills.SkillDescriptor) string { return "" },
		FormatWorkspacePath:   func(context.Context) string { return "" },
		AppendContextSections: func(_ context.Context, base string) string { return base },
	}
}

func makeTestMode() planPromptMode {
	return planPromptMode{
		preamble:      "multi",
		tot:           "tot",
		guidance:      "guide",
		extraSections: "extras",
		tail:          "TAIL",
		jsonExample:   `{"steps":[]}`,
		maxSteps:      "10",
	}
}

// ---------------------------------------------------------------------------
// NewPlanner
// ---------------------------------------------------------------------------

func TestNewPlanner_NilCaller(t *testing.T) {
	p, err := NewPlanner(nil, Config{})
	if err == nil {
		t.Error("expected error when caller is nil")
	}
	if p != nil {
		t.Error("expected nil Planner when caller is nil")
	}
}

func TestNewPlanner_ValidCaller(t *testing.T) {
	caller := &mockLLMCaller{}
	cfg := Config{
		DomainFromContext:     func(context.Context) string { return "code" },
		ComplexityFromContext: func(context.Context) int { return 0 },
		FormatSkillList:       func(context.Context, []skills.SkillDescriptor) string { return "None" },
		FormatWorkspacePath:   func(context.Context) string { return "" },
		AppendContextSections: func(_ context.Context, base string) string { return base },
	}
	p, err := NewPlanner(caller, cfg)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil Planner")
	}
	if p.llm == nil {
		t.Error("expected llm to be set")
	}
	if p.Cfg.MaxExploreSteps != 7 {
		t.Errorf("expected default MaxExploreSteps=7, got %d", p.Cfg.MaxExploreSteps)
	}
}

func TestNewPlanner_CustomMaxExploreSteps(t *testing.T) {
	caller := &mockLLMCaller{}
	cfg := Config{MaxExploreSteps: 10}
	p, err := NewPlanner(caller, cfg)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil Planner")
	}
	if p.Cfg.MaxExploreSteps != 10 {
		t.Errorf("expected MaxExploreSteps=10, got %d", p.Cfg.MaxExploreSteps)
	}
}

func TestNewPlanner_NegativeMaxExploreStepsDefaulted(t *testing.T) {
	caller := &mockLLMCaller{}
	cfg := Config{MaxExploreSteps: -5}
	p, err := NewPlanner(caller, cfg)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil Planner")
	}
	if p.Cfg.MaxExploreSteps != 7 {
		t.Errorf("expected negative MaxExploreSteps to default to 7, got %d", p.Cfg.MaxExploreSteps)
	}
}

// ---------------------------------------------------------------------------
// findTerminalSteps
// ---------------------------------------------------------------------------

func TestFindTerminalSteps_NilPlan(t *testing.T) {
	got := findTerminalSteps(nil)
	if got != nil {
		t.Errorf("expected nil for nil plan, got %v", got)
	}
}

func TestFindTerminalSteps_EmptyPlan(t *testing.T) {
	plan := &orchestration.Plan{}
	got := findTerminalSteps(plan)
	if len(got) != 0 {
		t.Errorf("expected empty slice for empty plan, got %v", got)
	}
}

func TestFindTerminalSteps_DAGTerminal(t *testing.T) {
	plan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", DependsOn: []string{}},
			{ID: "step_2", DependsOn: []string{"step_1"}},
			{ID: "step_3", DependsOn: []string{"step_1"}},
			{ID: "step_4", DependsOn: []string{"step_2", "step_3"}},
		},
	}
	got := findTerminalSteps(plan)
	if len(got) != 1 {
		t.Fatalf("expected 1 terminal step, got %d", len(got))
	}
	if got[0] != "step_4" {
		t.Errorf("expected 'step_4' as terminal, got %q", got[0])
	}
}

func TestFindTerminalSteps_MultipleTerminal(t *testing.T) {
	plan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", DependsOn: []string{}},
			{ID: "step_2", DependsOn: []string{}},
			{ID: "step_3", DependsOn: []string{"step_1"}},
		},
	}
	got := findTerminalSteps(plan)
	if len(got) != 2 {
		t.Fatalf("expected 2 terminal steps, got %d", len(got))
	}
	terminalSet := map[string]bool{}
	for _, id := range got {
		terminalSet[id] = true
	}
	if !terminalSet["step_2"] {
		t.Error("expected 'step_2' to be terminal")
	}
	if !terminalSet["step_3"] {
		t.Error("expected 'step_3' to be terminal")
	}
}

func TestFindTerminalSteps_SingleStep(t *testing.T) {
	plan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "only", DependsOn: []string{}},
		},
	}
	got := findTerminalSteps(plan)
	if len(got) != 1 {
		t.Fatalf("expected 1 terminal step, got %d", len(got))
	}
	if got[0] != "only" {
		t.Errorf("expected 'only', got %q", got[0])
	}
}

// ---------------------------------------------------------------------------
// DefaultConfig
// ---------------------------------------------------------------------------

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxExploreSteps != 7 {
		t.Errorf("expected MaxExploreSteps=7, got %d", cfg.MaxExploreSteps)
	}
	ctx := context.Background()
	if d := cfg.DomainFromContext(ctx); d != "" {
		t.Errorf("expected empty domain from default, got %q", d)
	}
	if c := cfg.ComplexityFromContext(ctx); c != 0 {
		t.Errorf("expected complexity=0, got %d", c)
	}
	skillStr := cfg.FormatSkillList(ctx, nil)
	if skillStr != "None" {
		t.Errorf("expected FormatSkillList to return 'None', got %q", skillStr)
	}
	wsPath := cfg.FormatWorkspacePath(ctx)
	if wsPath != "" {
		t.Errorf("expected empty workspace path, got %q", wsPath)
	}
	base := "test"
	appended := cfg.AppendContextSections(ctx, base)
	if appended != base {
		t.Errorf("expected AppendContextSections to return base unchanged, got %q", appended)
	}
}

// ---------------------------------------------------------------------------
// parsePlanResponse
// ---------------------------------------------------------------------------

func TestParsePlanResponse_ValidJSON(t *testing.T) {
	p := &Planner{}
	input := `{"steps": [{"id": "step_1", "summary": "Do the thing", "description": "Desc", "depends_on": [], "parallelizable": true}]}`
	plan, err := p.parsePlanResponse(input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].ID != "step_1" {
		t.Errorf("expected step_1, got %q", plan.Steps[0].ID)
	}
}

func TestParsePlanResponse_InvalidJSON(t *testing.T) {
	p := &Planner{}
	_, err := p.parsePlanResponse(`{invalid json}`, nil)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParsePlanResponse_MarkdownWrappedJSON(t *testing.T) {
	p := &Planner{}
	input := "```json\n{\"steps\":[{\"id\":\"step_1\",\"description\":\"do thing\"}]}\n```"
	plan, err := p.parsePlanResponse(input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}
}

func TestParsePlanResponse_PlainMarkdownCodeBlock(t *testing.T) {
	p := &Planner{}
	input := "```\n{\"steps\":[{\"id\":\"step_1\",\"description\":\"do thing\"}]}\n```"
	plan, err := p.parsePlanResponse(input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}
}

func TestParsePlanResponse_NoJSON(t *testing.T) {
	p := &Planner{}
	_, err := p.parsePlanResponse("just plain text, no json at all", nil)
	if err == nil {
		t.Error("expected error for non-JSON content")
	}
}

func TestParsePlanResponse_EmptyString(t *testing.T) {
	p := &Planner{}
	_, err := p.parsePlanResponse("", nil)
	if err == nil {
		t.Error("expected error for empty string")
	}
}

func TestParsePlanResponse_SurroundingText(t *testing.T) {
	p := &Planner{}
	input := `Some explanation text. {"steps":[{"id":"step_1","description":"do it"}]} More text after.`
	plan, err := p.parsePlanResponse(input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}
}

func TestParsePlanResponse_WithProfile(t *testing.T) {
	p := &Planner{}
	input := `{"steps":[{"id":"step_1","description":"task","profile":{"role":"coder","domain":"code","skills":["go-testing"]}}]}`
	plan, err := p.parsePlanResponse(input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Steps[0].Profile == nil {
		t.Fatal("expected profile to be non-nil")
	}
	profile, ok := plan.Steps[0].Profile.(*AgentProfile)
	if !ok {
		t.Fatalf("expected *AgentProfile, got %T", plan.Steps[0].Profile)
	}
	if profile.Role != "coder" {
		t.Errorf("expected role 'coder', got %q", profile.Role)
	}
}

func TestParsePlanResponse_WithSkillsFiltering(t *testing.T) {
	p := &Planner{}
	availSkills := []skills.SkillDescriptor{
		{Name: "go-testing", Description: "Go testing skill"},
	}
	input := `{"steps":[{"id":"step_1","description":"task","profile":{"role":"coder","skills":["go-testing","unknown-skill"]}}]}`
	plan, err := p.parsePlanResponse(input, availSkills)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	profile, ok := plan.Steps[0].Profile.(*AgentProfile)
	if !ok {
		t.Fatalf("expected *AgentProfile, got %T", plan.Steps[0].Profile)
	}
	if len(profile.Skills) != 1 {
		t.Fatalf("expected 1 skill after filtering, got %d", len(profile.Skills))
	}
	if profile.Skills[0] != "go-testing" {
		t.Errorf("expected 'go-testing', got %q", profile.Skills[0])
	}
}

func TestParsePlanResponse_EmptyProfileMap(t *testing.T) {
	p := &Planner{}
	input := `{"steps":[{"id":"step_1","description":"task","profile":{}}]}`
	plan, err := p.parsePlanResponse(input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Steps[0].Profile == nil {
		t.Fatal("expected non-nil profile for empty map")
	}
}

func TestParsePlanResponse_DuplicateStepIDs(t *testing.T) {
	p := &Planner{}
	input := `{"steps":[{"id":"step_1","description":"first"},{"id":"step_1","description":"dup"}]}`
	_, err := p.parsePlanResponse(input, nil)
	if err == nil {
		t.Fatal("expected error for duplicate step IDs")
	}
	if !strings.Contains(err.Error(), "duplicate step ID") {
		t.Errorf("expected duplicate-ID error, got: %v", err)
	}
}

func TestParsePlanResponse_UnknownDependency(t *testing.T) {
	p := &Planner{}
	input := `{"steps":[{"id":"step_1","description":"first","depends_on":["missing_step"]}]}`
	_, err := p.parsePlanResponse(input, nil)
	if err == nil {
		t.Fatal("expected error for unknown depends_on reference")
	}
	if !strings.Contains(err.Error(), "unknown step ID") {
		t.Errorf("expected unknown-dependency error, got: %v", err)
	}
}

func TestParsePlanResponse_DependencyCycle(t *testing.T) {
	p := &Planner{}
	input := `{"steps":[` +
		`{"id":"step_1","description":"a","depends_on":["step_2"]},` +
		`{"id":"step_2","description":"b","depends_on":["step_3"]},` +
		`{"id":"step_3","description":"c","depends_on":["step_1"]}]}`
	_, err := p.parsePlanResponse(input, nil)
	if err == nil {
		t.Fatal("expected error for dependency cycle")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected cycle error, got: %v", err)
	}
}

func TestParsePlanResponse_SelfDependencyCycle(t *testing.T) {
	p := &Planner{}
	input := `{"steps":[{"id":"step_1","description":"a","depends_on":["step_1"]}]}`
	_, err := p.parsePlanResponse(input, nil)
	if err == nil {
		t.Fatal("expected error for self-dependency")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected cycle error, got: %v", err)
	}
}

func TestParsePlanResponse_MultipleSteps(t *testing.T) {
	p := &Planner{}
	input := `{"steps":[{"id":"step_1","description":"first"},{"id":"step_2","description":"second","depends_on":["step_1"]}]}`
	plan, err := p.parsePlanResponse(input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(plan.Steps))
	}
	if plan.Steps[0].ID != "step_1" {
		t.Errorf("expected step_1, got %q", plan.Steps[0].ID)
	}
	if plan.Steps[1].ID != "step_2" {
		t.Errorf("expected step_2, got %q", plan.Steps[1].ID)
	}
}

// ---------------------------------------------------------------------------
// summarizeExplorationSteps
// ---------------------------------------------------------------------------

func TestSummarizeExplorationSteps_WithSteps(t *testing.T) {
	steps := []agent.Step{
		{Thought: "Need to check the file structure", Action: llm.ToolCall{Name: "list_directory"}},
		{Thought: "Interesting, let's read the main file", Action: llm.ToolCall{Name: "read_file"}},
		{Thought: "Final analysis done", Action: llm.ToolCall{}},
	}
	result := summarizeExplorationSteps(steps)
	if result == "" {
		t.Fatal("expected non-empty summary")
	}
	if !strings.Contains(result, "list_directory") {
		t.Error("expected summary to mention list_directory")
	}
	if !strings.Contains(result, "read_file") {
		t.Error("expected summary to mention read_file")
	}
	if !strings.Contains(result, "Final analysis done") {
		t.Error("expected summary to contain final thought")
	}
}

func TestSummarizeExplorationSteps_EmptySteps(t *testing.T) {
	result := summarizeExplorationSteps(nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
	result = summarizeExplorationSteps([]agent.Step{})
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestSummarizeExplorationSteps_SkipsEmptyThought(t *testing.T) {
	steps := []agent.Step{
		{Thought: "", Action: llm.ToolCall{Name: "glob"}},
		{Thought: "Valid thought", Action: llm.ToolCall{Name: "ripgrep"}},
	}
	result := summarizeExplorationSteps(steps)
	if strings.Contains(result, "glob") {
		t.Error("steps with empty thought should be skipped")
	}
	if !strings.Contains(result, "Valid thought") {
		t.Error("expected valid thought in output")
	}
}

func TestSummarizeExplorationSteps_Truncation(t *testing.T) {
	longThought := strings.Repeat("x", 100)
	steps := make([]agent.Step, 50)
	for i := range steps {
		steps[i] = agent.Step{Thought: longThought, Action: llm.ToolCall{Name: "tool"}}
	}
	result := summarizeExplorationSteps(steps)
	if len(result) > 4000 {
		t.Errorf("result should be truncated to <=4000 chars, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// familyPrompt
// ---------------------------------------------------------------------------

func TestFamilyPrompt_NilFunction(t *testing.T) {
	p := &Planner{
		Cfg: Config{Prompts: PromptSet{FamilyPrompt: nil}},
	}
	result := p.familyPrompt(context.Background())
	if result != "" {
		t.Errorf("expected empty string when FamilyPrompt is nil, got %q", result)
	}
}

func TestFamilyPrompt_ReturnsPrompt(t *testing.T) {
	p := &Planner{
		Cfg: Config{
			Prompts: PromptSet{
				FamilyPrompt: func(agent, family string) string {
					return "family:" + agent + ":" + family
				},
			},
			ModelRegistry:         nil,
			DomainFromContext:     func(context.Context) string { return "" },
			ComplexityFromContext: func(context.Context) int { return 0 },
		},
	}
	result := p.familyPrompt(context.Background())
	if result != "family:planner:default" {
		t.Errorf("expected 'family:planner:default', got %q", result)
	}
}

// ---------------------------------------------------------------------------
// getFamily
// ---------------------------------------------------------------------------

func TestGetFamily_NilModelRegistry(t *testing.T) {
	p := &Planner{Cfg: Config{ModelRegistry: nil}}
	result := p.getFamily(context.Background())
	if result != "default" {
		t.Errorf("expected 'default' with nil registry, got %q", result)
	}
}

func TestGetFamily_EmptyModel(t *testing.T) {
	reg := llm.NewModelRegistry(nil)
	p := &Planner{
		Cfg: Config{ModelRegistry: reg, Model: ""},
	}
	result := p.getFamily(context.Background())
	if result != "default" {
		t.Errorf("expected 'default' for empty model, got %q", result)
	}
}

func TestGetFamily_KnownModel(t *testing.T) {
	reg := llm.NewModelRegistry(nil)
	p := &Planner{
		Cfg: Config{ModelRegistry: reg, Model: "claude-sonnet-4-20250514"},
	}
	result := p.getFamily(context.Background())
	if result == "" {
		t.Error("expected non-empty family for known model")
	}
}

// ---------------------------------------------------------------------------
// buildSystemPromptFromMode
// ---------------------------------------------------------------------------

func TestBuildSystemPromptFromMode_NilFunctionPointers(t *testing.T) {
	cfg := makeTestConfig("PLAN: {MODE-PREAMBLE} {MAX-STEPS} {WORKSPACE-PATH}")
	p := &Planner{Cfg: cfg}
	mode := makeTestMode()
	result := p.buildSystemPromptFromMode(context.Background(), mode, cfg.Prompts.BasePrompt, nil, nil, nil, nil)
	if result == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(result, "PLAN:") {
		t.Error("expected base template in output")
	}
}

func TestBuildSystemPromptFromMode_WithReflections(t *testing.T) {
	cfg := makeTestConfig("TAIL: {MODE-TAIL}")
	p := &Planner{Cfg: cfg}
	reflections := []orchestration.Reflection{
		{FailureAnalysis: "bad approach", RootCause: "wrong assumption", ActionPlan: "try different"},
	}
	mode := makeTestMode()
	mode.tail = "REFLECTIONS\n"
	result := p.buildSystemPromptFromMode(context.Background(), mode, cfg.Prompts.BasePrompt, nil, reflections, nil, nil)
	if !strings.Contains(result, "bad approach") {
		t.Error("expected reflection failure analysis in output")
	}
	if !strings.Contains(result, "wrong assumption") {
		t.Error("expected root cause in output")
	}
	if !strings.Contains(result, "try different") {
		t.Error("expected action plan in output")
	}
}

func TestBuildSystemPromptFromMode_WithExtraSubstitutions(t *testing.T) {
	cfg := makeTestConfig("CUSTOM: {MY-KEY}")
	p := &Planner{Cfg: cfg}
	mode := makeTestMode()
	result := p.buildSystemPromptFromMode(context.Background(), mode, cfg.Prompts.BasePrompt, nil, nil, nil,
		map[string]string{"MY-KEY": "my-value"})
	if !strings.Contains(result, "my-value") {
		t.Errorf("expected extra substitution, got: %s", result)
	}
}

// ---------------------------------------------------------------------------
// formatSkillList via buildSystemPromptFromMode
// ---------------------------------------------------------------------------

func TestFormatSkillList_Empty(t *testing.T) {
	cfg := makeTestConfig("SKILLS: {AVAILABLE-SKILLS}")
	cfg.FormatSkillList = func(context.Context, []skills.SkillDescriptor) string { return "None" }
	p := &Planner{Cfg: cfg}
	mode := makeTestMode()
	result := p.buildSystemPromptFromMode(context.Background(), mode, cfg.Prompts.BasePrompt, nil, nil, nil, nil)
	if !strings.Contains(result, "None") {
		t.Errorf("expected 'None' for empty skills, got: %s", result)
	}
}

func TestFormatSkillList_WithSkills(t *testing.T) {
	cfg := makeTestConfig("SKILLS: {AVAILABLE-SKILLS}")
	cfg.FormatSkillList = func(ctx context.Context, availableSkills []skills.SkillDescriptor) string {
		return "Available: Go Testing, Linting"
	}
	p := &Planner{Cfg: cfg}
	mode := makeTestMode()
	skillsList := []skills.SkillDescriptor{{Name: "go-testing", Description: "Go testing"}}
	result := p.buildSystemPromptFromMode(context.Background(), mode, cfg.Prompts.BasePrompt, nil, nil, skillsList, nil)
	if !strings.Contains(result, "Go Testing") {
		t.Errorf("expected skill list content, got: %s", result)
	}
}

// ---------------------------------------------------------------------------
// buildContinuationSystemPrompt
// ---------------------------------------------------------------------------

func TestBuildContinuationSystemPrompt(t *testing.T) {
	cfg := makeTestConfig("CONT: {ORIGINAL-REQUEST} | {COMPLETED-PLAN-SUMMARY} | {TERMINAL-STEPS} | {MAX-STEPS}")
	cfg.Prompts.ContinuationPreamble = "cont-preamble"
	p := &Planner{Cfg: cfg}

	existingPlan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Description: "first step"},
			{ID: "step_2", Description: "second step", DependsOn: []string{"step_1"}},
		},
	}
	completedSteps := []orchestration.CompletedStep{
		{StepID: "step_1", Output: "completed output"},
	}

	mode := p.continuationMultiMode()
	result := p.buildContinuationSystemPrompt(
		context.Background(), mode, "original request text",
		existingPlan, completedSteps, nil, nil, nil,
	)

	if result == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(result, "original request text") {
		t.Error("expected original request in prompt")
	}
	if !strings.Contains(result, "first step") {
		t.Error("expected step description in prompt")
	}
	if !strings.Contains(result, "completed output") {
		t.Error("expected completed output in prompt")
	}
	if !strings.Contains(result, "step_2") {
		t.Error("expected terminal step in prompt")
	}
}

func TestBuildContinuationSystemPrompt_SingleStep(t *testing.T) {
	cfg := makeTestConfig("CONT: {ORIGINAL-REQUEST} | {COMPLETED-PLAN-SUMMARY} | {TERMINAL-STEPS} | {MAX-STEPS}")
	cfg.Prompts.ContinuationSingleStep = "cont-single-preamble"
	cfg.Prompts.SingleStepToT = "tot"
	cfg.Prompts.SingleStepGuidance = "guide"
	p := &Planner{Cfg: cfg}

	existingPlan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Description: "the only step"},
		},
	}

	mode := p.continuationSingleMode()
	result := p.buildContinuationSystemPrompt(
		context.Background(), mode, "original",
		existingPlan, nil, nil, nil, nil,
	)

	if !strings.Contains(result, "original") {
		t.Error("expected original request")
	}
	if !strings.Contains(result, "the only step") {
		t.Error("expected step description")
	}
}

// ---------------------------------------------------------------------------
// planDirect
// ---------------------------------------------------------------------------

func TestPlanDirect_Success(t *testing.T) {
	caller := &mockLLMCaller{
		resp: newPlanResponse(`{"steps":[{"id":"step_1","summary":"Test","description":"desc"}]}`),
	}
	cfg := makeTestConfig("Plan: {MODE-PREAMBLE} | {MAX-STEPS} | {WORKSPACE-PATH}")
	p := &Planner{llm: caller, Cfg: cfg}

	mode := p.multiStepMode()
	plan, err := p.planDirect(context.Background(), "do task", mode, nil, nil, nil, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}
}

func TestPlanDirect_LLMError(t *testing.T) {
	caller := &mockLLMCaller{err: errors.New("llm unavailable")}
	cfg := makeTestConfig("Plan: {MODE-PREAMBLE}")
	p := &Planner{llm: caller, Cfg: cfg}

	mode := p.multiStepMode()
	_, err := p.planDirect(context.Background(), "task", mode, nil, nil, nil, false, nil)
	if err == nil {
		t.Error("expected error from LLM call failure")
	}
}

func TestPlanDirect_SingleStepTruncation(t *testing.T) {
	caller := &mockLLMCaller{
		resp: newPlanResponse(`{"steps":[{"id":"step_1","description":"first"},{"id":"step_2","description":"second"}]}`),
	}
	cfg := makeTestConfig("Plan")
	cfg.Prompts.SingleStepPreamble = "single"
	cfg.Prompts.SingleStepToT = ""
	cfg.Prompts.SingleStepGuidance = ""
	p := &Planner{llm: caller, Cfg: cfg}

	mode := p.singleStepMode()
	plan, err := p.planDirect(context.Background(), "task", mode, nil, nil, nil, true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step after truncation, got %d", len(plan.Steps))
	}
}

// ---------------------------------------------------------------------------
// Replan
// ---------------------------------------------------------------------------

func TestReplan_Success(t *testing.T) {
	caller := &mockLLMCaller{
		resp: newPlanResponse(`{"steps":[{"id":"step_new","summary":"retry","description":"different approach"}]}`),
	}
	cfg := makeTestConfig("REPLAN")
	cfg.Prompts.ReplanPrompt = "REPLAN: {ORIGINAL-PLAN} | {COMPLETED-STEPS} | {FAILED-STEP} | {PREVIOUS-SESSION-REFLECTIONS} | {CURRENT-REFLECTION} | {AVAILABLE-SKILLS} | {WORKSPACE-PATH}"
	p := &Planner{llm: caller, Cfg: cfg}

	originalPlan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{{ID: "step_1", Description: "original step"}},
	}
	failedStep := orchestration.CompletedStep{
		StepID: "step_1", Output: "some output", Error: errors.New("something broke"),
	}
	reflection := &orchestration.Reflection{
		FailureAnalysis: "bad assumption", RootCause: "wrong tool", ActionPlan: "use different tool",
	}

	plan, err := p.Replan(context.Background(), originalPlan, nil, failedStep, reflection, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].ID != "step_new" {
		t.Errorf("expected step_new, got %q", plan.Steps[0].ID)
	}
}

func TestReplan_LLMError(t *testing.T) {
	caller := &mockLLMCaller{err: errors.New("llm down")}
	cfg := makeTestConfig("REPLAN")
	cfg.Prompts.ReplanPrompt = "REPLAN"
	p := &Planner{llm: caller, Cfg: cfg}

	_, err := p.Replan(context.Background(), &orchestration.Plan{}, nil, orchestration.CompletedStep{}, nil, nil, nil)
	if err == nil {
		t.Error("expected error from LLM failure")
	}
}

// ---------------------------------------------------------------------------
// PlanContinuation
// ---------------------------------------------------------------------------

func TestPlanContinuation_Success(t *testing.T) {
	caller := &mockLLMCaller{
		resp: newPlanResponse(`{"steps":[{"id":"continuation_1","summary":"continue","description":"next step"}]}`),
	}
	cfg := makeTestConfig("CONT: {ORIGINAL-REQUEST} | {COMPLETED-PLAN-SUMMARY} | {TERMINAL-STEPS} | {MAX-STEPS}")
	cfg.Prompts.ContinuationPreamble = "cont-preamble"
	p := &Planner{llm: caller, Cfg: cfg}

	existingPlan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{{ID: "step_1", Description: "done step"}},
	}

	plan, err := p.PlanContinuation(context.Background(), "original", existingPlan, nil, "continue work", nil, nil, false, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
}

func TestPlanContinuation_SingleStepTruncation(t *testing.T) {
	caller := &mockLLMCaller{
		resp: newPlanResponse(`{"steps":[{"id":"c1","description":"one"},{"id":"c2","description":"two"}]}`),
	}
	cfg := makeTestConfig("CONT: {ORIGINAL-REQUEST} | {COMPLETED-PLAN-SUMMARY} | {TERMINAL-STEPS} | {MAX-STEPS}")
	cfg.Prompts.ContinuationSingleStep = "csp"
	cfg.Prompts.SingleStepToT = ""
	cfg.Prompts.SingleStepGuidance = ""
	p := &Planner{llm: caller, Cfg: cfg}

	plan, err := p.PlanContinuation(context.Background(), "original", &orchestration.Plan{}, nil, "continue", nil, nil, true, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step after truncation in single-step continuation, got %d", len(plan.Steps))
	}
}

// ---------------------------------------------------------------------------
// formatPlanReflections
// ---------------------------------------------------------------------------

func TestFormatPlanReflections_Empty(t *testing.T) {
	result := formatPlanReflections(nil)
	if result != "" {
		t.Errorf("expected empty for nil, got %q", result)
	}
	result = formatPlanReflections([]orchestration.Reflection{})
	if result != "" {
		t.Errorf("expected empty for empty slice, got %q", result)
	}
}

func TestFormatPlanReflections_WithReflections(t *testing.T) {
	reflections := []orchestration.Reflection{
		{FailureAnalysis: "bad", RootCause: "wrong", ActionPlan: "fix"},
		{FailureAnalysis: "fail", RootCause: "bug", ActionPlan: "patch"},
	}
	result := formatPlanReflections(reflections)
	if !strings.Contains(result, "bad") {
		t.Error("expected first failure analysis")
	}
	if !strings.Contains(result, "wrong") {
		t.Error("expected first root cause")
	}
	if !strings.Contains(result, "fix") {
		t.Error("expected first action plan")
	}
	if !strings.Contains(result, "fail") {
		t.Error("expected second failure analysis")
	}
	if !strings.Contains(result, "bug") {
		t.Error("expected second root cause")
	}
	if !strings.Contains(result, "patch") {
		t.Error("expected second action plan")
	}
}

// ---------------------------------------------------------------------------
// formatSessionReflections
// ---------------------------------------------------------------------------

func TestFormatSessionReflections_Empty(t *testing.T) {
	result := formatSessionReflections(nil)
	if result != "" {
		t.Errorf("expected empty for nil, got %q", result)
	}
	result = formatSessionReflections([]orchestration.Reflection{})
	if result != "" {
		t.Errorf("expected empty for empty slice, got %q", result)
	}
}

func TestFormatSessionReflections_WithReflections(t *testing.T) {
	reflections := []orchestration.Reflection{
		{Summary: "issue", RootCause: "cause", ActionPlan: "plan", SuggestedAction: "retry"},
	}
	result := formatSessionReflections(reflections)
	if !strings.Contains(result, "issue") {
		t.Error("expected summary")
	}
	if !strings.Contains(result, "cause") {
		t.Error("expected root cause")
	}
	if !strings.Contains(result, "plan") {
		t.Error("expected action plan")
	}
	if !strings.Contains(result, "retry") {
		t.Error("expected suggested action")
	}
}

// ---------------------------------------------------------------------------
// systemMessagesFromPrompt
// ---------------------------------------------------------------------------

func TestSystemMessagesFromPrompt_NoCacheBreak(t *testing.T) {
	msgs := systemMessagesFromPrompt("hello world")
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("expected role system, got %s", msgs[0].Role)
	}
	if msgs[0].Content != "hello world" {
		t.Errorf("expected 'hello world', got %q", msgs[0].Content)
	}
}

func TestSystemMessagesFromPrompt_WithCacheBreak(t *testing.T) {
	msgs := systemMessagesFromPrompt("part1\x00CACHE_BREAK\x00part2")
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	for _, m := range msgs {
		if m.Role != "system" {
			t.Errorf("expected role system, got %s", m.Role)
		}
	}
	if msgs[0].Content != "part1" {
		t.Errorf("expected 'part1', got %q", msgs[0].Content)
	}
	if msgs[1].Content != "part2" {
		t.Errorf("expected 'part2', got %q", msgs[1].Content)
	}
}

// ---------------------------------------------------------------------------
// Plan (integration via planDirect path)
// ---------------------------------------------------------------------------

func TestPlan_DirectPath_NoPlannerTools(t *testing.T) {
	caller := &mockLLMCaller{
		resp: newPlanResponse(`{"steps":[{"id":"step_1","description":"task"}]}`),
	}
	cfg := makeTestConfig("Plan: {MODE-PREAMBLE} | {MAX-STEPS}")
	cfg.DomainFromContext = func(context.Context) string { return "general" }
	cfg.ComplexityFromContext = func(context.Context) int { return 0 }
	cfg.PlannerToolNames = map[string]bool{}
	p := &Planner{llm: caller, Cfg: cfg}

	plan, err := p.Plan(context.Background(), "do task", nil, nil, nil, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
}

func TestPlan_DirectPath_DomainGeneralLowComplexity(t *testing.T) {
	caller := &mockLLMCaller{
		resp: newPlanResponse(`{"steps":[{"id":"step_1","description":"task"}]}`),
	}
	cfg := makeTestConfig("Plan: {MODE-PREAMBLE} | {MAX-STEPS}")
	cfg.DomainFromContext = func(context.Context) string { return "general" }
	cfg.ComplexityFromContext = func(context.Context) int { return 2 }
	cfg.ToolRegistry = &mockToolRegistry{
		tools: []tools.ToolDescriptor{{Name: "read_file"}},
	}
	cfg.PlannerToolNames = map[string]bool{"read_file": true}
	p := &Planner{llm: caller, Cfg: cfg}

	plan, err := p.Plan(context.Background(), "task", nil, nil, nil, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
}

// ---------------------------------------------------------------------------
// Mode helpers
// ---------------------------------------------------------------------------

func TestMultiStepMode_Fields(t *testing.T) {
	cfg := makeTestConfig("")
	cfg.Prompts.PlanPreamble = "preamble_text"
	cfg.Prompts.MultiStepToT = "tot_text"
	cfg.Prompts.MultiStepGuidance = "guidance_text"
	cfg.Prompts.ExtraSections = "extra_text"
	p := &Planner{Cfg: cfg}

	mode := p.multiStepMode()
	if mode.preamble != "preamble_text" {
		t.Errorf("expected preamble_text, got %q", mode.preamble)
	}
	if mode.tot != "tot_text" {
		t.Errorf("expected tot_text, got %q", mode.tot)
	}
	if mode.guidance != "guidance_text" {
		t.Errorf("expected guidance_text, got %q", mode.guidance)
	}
	if mode.extraSections != "extra_text" {
		t.Errorf("expected extra_text, got %q", mode.extraSections)
	}
	if mode.tail != planModeTail {
		t.Errorf("expected planModeTail, got %q", mode.tail)
	}
	if mode.maxSteps != "10" {
		t.Errorf("expected maxSteps=10, got %q", mode.maxSteps)
	}
}

func TestSingleStepMode_Fields(t *testing.T) {
	cfg := makeTestConfig("")
	cfg.Prompts.SingleStepPreamble = "single_preamble"
	cfg.Prompts.SingleStepToT = "single_tot"
	cfg.Prompts.SingleStepGuidance = "single_guidance"
	cfg.Prompts.ExtraSections = "extra"
	p := &Planner{Cfg: cfg}

	mode := p.singleStepMode()
	if mode.preamble != "single_preamble" {
		t.Errorf("expected single_preamble, got %q", mode.preamble)
	}
	if mode.tot != "single_tot" {
		t.Errorf("expected single_tot, got %q", mode.tot)
	}
	if mode.guidance != "single_guidance" {
		t.Errorf("expected single_guidance, got %q", mode.guidance)
	}
	if mode.maxSteps != "1" {
		t.Errorf("expected maxSteps=1, got %q", mode.maxSteps)
	}
}

func TestContinuationMultiMode_Fields(t *testing.T) {
	cfg := makeTestConfig("")
	cfg.Prompts.ContinuationPreamble = "cont_pre"
	cfg.Prompts.MultiStepToT = "cont_tot"
	cfg.Prompts.MultiStepGuidance = "cont_guidance"
	cfg.Prompts.ExtraSections = "cont_extra"
	p := &Planner{Cfg: cfg}

	mode := p.continuationMultiMode()
	if mode.preamble != "cont_pre" {
		t.Errorf("expected cont_pre, got %q", mode.preamble)
	}
	if mode.tail != "" {
		t.Errorf("expected empty tail, got %q", mode.tail)
	}
	if mode.jsonExample != continuationModeJSONExample {
		t.Error("expected continuationModeJSONExample")
	}
	if mode.maxSteps != "10" {
		t.Errorf("expected maxSteps=10, got %q", mode.maxSteps)
	}
}

func TestContinuationSingleMode_Fields(t *testing.T) {
	cfg := makeTestConfig("")
	cfg.Prompts.ContinuationSingleStep = "cont_single_pre"
	cfg.Prompts.SingleStepToT = "single_tot"
	cfg.Prompts.SingleStepGuidance = "single_g"
	cfg.Prompts.ExtraSections = "extra"
	p := &Planner{Cfg: cfg}

	mode := p.continuationSingleMode()
	if mode.preamble != "cont_single_pre" {
		t.Errorf("expected cont_single_pre, got %q", mode.preamble)
	}
	if mode.tail != "" {
		t.Errorf("expected empty tail, got %q", mode.tail)
	}
	if mode.jsonExample != continuationSingleStepJSONExample {
		t.Error("expected continuationSingleStepJSONExample")
	}
	if mode.maxSteps != "1" {
		t.Errorf("expected maxSteps=1, got %q", mode.maxSteps)
	}
}

// ---------------------------------------------------------------------------
// getPlannerTools
// ---------------------------------------------------------------------------

func TestGetPlannerTools_WithTools(t *testing.T) {
	reg := &mockToolRegistry{
		tools: []tools.ToolDescriptor{
			{Name: "read_file"},
			{Name: "glob"},
			{Name: "ripgrep"},
			{Name: "write_file"},
		},
	}
	p := &Planner{Cfg: Config{
		ToolRegistry:     reg,
		PlannerToolNames: map[string]bool{"read_file": true, "glob": true},
	}}
	result := p.getPlannerTools()
	if len(result) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result))
	}
	names := map[string]bool{}
	for _, td := range result {
		names[td.Name] = true
	}
	if !names["read_file"] || !names["glob"] {
		t.Errorf("expected read_file and glob, got %v", names)
	}
}

func TestGetPlannerTools_NilRegistry(t *testing.T) {
	p := &Planner{}
	result := p.getPlannerTools()
	if result != nil {
		t.Errorf("expected nil for nil registry, got %v", result)
	}
}

func TestGetPlannerTools_EmptyPlannerNames(t *testing.T) {
	reg := &mockToolRegistry{
		tools: []tools.ToolDescriptor{{Name: "read_file"}},
	}
	p := &Planner{Cfg: Config{
		ToolRegistry:     reg,
		PlannerToolNames: map[string]bool{},
	}}
	result := p.getPlannerTools()
	if result != nil {
		t.Errorf("expected nil for empty planner names, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// Plan → exploration path (code domain + high complexity + planner tools)
// With nil ContextFactory, falls back to planDirect.
// Covers: Plan exploration branch, buildInformedPlanSystemPrompt,
//         planWithExploration nil-ContextFactory fallback, and metadata path.
// ---------------------------------------------------------------------------

func TestPlan_ExplorationPath_NilContextFactory(t *testing.T) {
	caller := &mockLLMCaller{
		resp: newPlanResponse(`{"steps":[{"id":"step_1","description":"informed task"}]}`),
	}
	cfg := makeTestConfig("Plan: {MODE-PREAMBLE} | {MAX-STEPS} | {WORKSPACE-PATH}")
	cfg.DomainFromContext = func(context.Context) string { return "code" }
	cfg.ComplexityFromContext = func(context.Context) int { return 5 }
	cfg.ToolRegistry = &mockToolRegistry{
		tools: []tools.ToolDescriptor{{Name: "read_file"}, {Name: "glob"}},
	}
	cfg.PlannerToolNames = map[string]bool{"read_file": true, "glob": true}
	// ContextFactory is nil => fallback to planDirect inside planWithExploration
	p := &Planner{llm: caller, Cfg: cfg}

	plan, err := p.Plan(context.Background(), "explore this task", nil, nil, nil, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}
}

// ---------------------------------------------------------------------------
// emitService with non-nil emitter (the 50% path)
// ---------------------------------------------------------------------------

type mockEmitter struct {
	serviceCalls []string
}

func (m *mockEmitter) StepStart(_ int)                                      {}
func (m *mockEmitter) Thought(_ int, _, _ string)                           {}
func (m *mockEmitter) ToolCall(_, _ int, _, _, _ string)                    {}
func (m *mockEmitter) ToolResult(_, _, _ int, _ string, _ bool)             {}
func (m *mockEmitter) StepComplete(_ int, _ time.Duration)                  {}
func (m *mockEmitter) SubAgentLaunch(_, _ string)                           {}
func (m *mockEmitter) SubAgentComplete(_ string, _ bool, _ time.Duration)   {}
func (m *mockEmitter) AssistantChunk(_ string)                              {}
func (m *mockEmitter) AssistantDone(_ string, _, _ int)                     {}
func (m *mockEmitter) ContextFill(_ float64, _, _ int, _, _ string)         {}
func (m *mockEmitter) ContextCompaction(_, _ float64, _ string)             {}
func (m *mockEmitter) Finishing(_ int, _ string)                            {}
func (m *mockEmitter) ExecutorDiagnostic(_ int, _ string, _ map[string]any) {}
func (m *mockEmitter) ServiceWithMeta(content string, _ map[string]any) {
	m.serviceCalls = append(m.serviceCalls, content)
}

func TestEmitService_WithEmitter(t *testing.T) {
	em := &mockEmitter{}
	p := &Planner{Cfg: Config{Emitter: em}}
	p.emitService("hello world", map[string]any{"phase": "test"})
	if len(em.serviceCalls) != 1 {
		t.Fatalf("expected 1 service call, got %d", len(em.serviceCalls))
	}
	if em.serviceCalls[0] != "hello world" {
		t.Errorf("expected 'hello world', got %q", em.serviceCalls[0])
	}
}

// ---------------------------------------------------------------------------
// log with non-nil Logger (the 66.7% path)
// ---------------------------------------------------------------------------

func TestLog_WithLogger(t *testing.T) {
	logger := slog.Default()
	p := &Planner{Cfg: Config{Logger: logger}}
	got := p.log()
	if got != logger {
		t.Error("expected logger to be the configured logger")
	}
}

// ---------------------------------------------------------------------------
// getFamily: empty family from model registry
// ---------------------------------------------------------------------------

func TestGetFamily_EmptyFamilyFromRegistry(t *testing.T) {
	// A model registry that resolves to empty family
	reg := llm.NewModelRegistry(nil)
	// Use an unknown model that resolves to empty metadata
	p := &Planner{
		Cfg: Config{ModelRegistry: reg, Model: "unknown-model-xyz"},
	}
	result := p.getFamily(context.Background())
	if result != "default" {
		t.Errorf("expected 'default' when resolved family is empty, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// PlanContinuation: LLM error path
// ---------------------------------------------------------------------------

func TestPlanContinuation_LLMError(t *testing.T) {
	caller := &mockLLMCaller{err: errors.New("llm down")}
	cfg := makeTestConfig("CONT: {ORIGINAL-REQUEST} | {COMPLETED-PLAN-SUMMARY}")
	cfg.Prompts.ContinuationPreamble = "cont-preamble"
	p := &Planner{llm: caller, Cfg: cfg}

	_, err := p.PlanContinuation(context.Background(), "original", &orchestration.Plan{}, nil, "continue", nil, nil, false, nil, true)
	if err == nil {
		t.Error("expected error from LLM failure")
	}
}

// ---------------------------------------------------------------------------
// PlanContinuation: parse error path
// ---------------------------------------------------------------------------

func TestPlanContinuation_ParseError(t *testing.T) {
	caller := &mockLLMCaller{
		resp: newPlanResponse(`not valid json at all`),
	}
	cfg := makeTestConfig("CONT: {ORIGINAL-REQUEST} | {COMPLETED-PLAN-SUMMARY}")
	cfg.Prompts.ContinuationPreamble = "cont-preamble"
	p := &Planner{llm: caller, Cfg: cfg}

	_, err := p.PlanContinuation(context.Background(), "original", &orchestration.Plan{}, nil, "continue", nil, nil, false, nil, true)
	if err == nil {
		t.Error("expected error from parse failure")
	}
}

// ---------------------------------------------------------------------------
// planDirect: parse error path
// ---------------------------------------------------------------------------

func TestPlanDirect_ParseError(t *testing.T) {
	caller := &mockLLMCaller{
		resp: newPlanResponse(`garbage response`),
	}
	cfg := makeTestConfig("Plan: {MODE-PREAMBLE}")
	p := &Planner{llm: caller, Cfg: cfg}

	mode := p.multiStepMode()
	_, err := p.planDirect(context.Background(), "task", mode, nil, nil, nil, false, nil)
	if err == nil {
		t.Error("expected error from parse failure")
	}
}

// ---------------------------------------------------------------------------
// Replan: parse error path
// ---------------------------------------------------------------------------

func TestReplan_ParseError(t *testing.T) {
	caller := &mockLLMCaller{
		resp: newPlanResponse(`not valid`),
	}
	cfg := makeTestConfig("REPLAN")
	cfg.Prompts.ReplanPrompt = "REPLAN: {ORIGINAL-PLAN} | {COMPLETED-STEPS} | {FAILED-STEP} | {PREVIOUS-SESSION-REFLECTIONS} | {CURRENT-REFLECTION} | {AVAILABLE-SKILLS} | {WORKSPACE-PATH}"
	p := &Planner{llm: caller, Cfg: cfg}

	_, err := p.Replan(context.Background(), &orchestration.Plan{}, nil, orchestration.CompletedStep{}, nil, nil, nil)
	if err == nil {
		t.Error("expected error from parse failure")
	}
}

// ---------------------------------------------------------------------------
// Replan: with completed steps and session reflections (covers more branches)
// ---------------------------------------------------------------------------

func TestReplan_WithCompletedStepsAndSessionReflections(t *testing.T) {
	caller := &mockLLMCaller{
		resp: newPlanResponse(`{"steps":[{"id":"step_retry","description":"retry with context"}]}`),
	}
	cfg := makeTestConfig("REPLAN")
	cfg.Prompts.ReplanPrompt = "REPLAN: {ORIGINAL-PLAN} | {COMPLETED-STEPS} | {FAILED-STEP} | {PREVIOUS-SESSION-REFLECTIONS} | {CURRENT-REFLECTION} | {AVAILABLE-SKILLS} | {WORKSPACE-PATH}"
	p := &Planner{llm: caller, Cfg: cfg}

	originalPlan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{{ID: "step_1", Description: "first"}},
	}
	completedSteps := []orchestration.CompletedStep{
		{StepID: "step_0", Output: "done earlier"},
	}
	failedStep := orchestration.CompletedStep{
		StepID: "step_1", Output: "partial output", Error: errors.New("failed"),
	}
	reflection := &orchestration.Reflection{
		FailureAnalysis: "bad approach", RootCause: "wrong tool", ActionPlan: "switch tools",
	}
	sessionReflections := []orchestration.Reflection{
		{Summary: "pattern", RootCause: "systemic", ActionPlan: "avoid", SuggestedAction: "refactor"},
	}

	plan, err := p.Replan(context.Background(), originalPlan, completedSteps, failedStep, reflection, sessionReflections, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}
}

// ---------------------------------------------------------------------------
// parsePlanResponse: profile is not a map (e.g., a string)
// ---------------------------------------------------------------------------

func TestParsePlanResponse_NonMapProfile(t *testing.T) {
	p := &Planner{}
	input := `{"steps":[{"id":"step_1","description":"task","profile":"not-a-map"}]}`
	plan, err := p.parsePlanResponse(input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Profile should be nil or the raw string (since !isMap, it's left as-is)
	if plan.Steps[0].Profile != nil {
		t.Logf("profile (not a map, left as raw): %v", plan.Steps[0].Profile)
	}
}

// ---------------------------------------------------------------------------
// buildInformedPlanSystemPrompt direct coverage
// ---------------------------------------------------------------------------

func TestBuildInformedPlanSystemPrompt(t *testing.T) {
	cfg := makeTestConfig("INFORMED: {MODE-PREAMBLE} | {MAX-STEPS} | {WORKSPACE-PATH}")
	cfg.Prompts.InformedPrompt = "INFORMED: {MODE-PREAMBLE} | {MAX-STEPS}"
	p := &Planner{Cfg: cfg}
	mode := makeTestMode()
	result := p.buildInformedPlanSystemPrompt(context.Background(), mode, nil, nil, nil, nil)
	if result == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(result, "INFORMED:") {
		t.Error("expected InformedPrompt content in output")
	}
}

// ---------------------------------------------------------------------------
// formatPlanReflections: nil reflections (edge on the nil-return path)
// ---------------------------------------------------------------------------

func TestFormatPlanReflections_NilSlice(t *testing.T) {
	result := formatPlanReflections(nil)
	if result != "" {
		t.Errorf("expected empty string for nil, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// parsePlanResponse: markdown code block with json prefix, multi-step
// also tests the firstStepSummary debug log path
// ---------------------------------------------------------------------------

func TestParsePlanResponse_MarkdownJSONNoClosingBackticks(t *testing.T) {
	p := &Planner{}
	// json code block with no closing ```
	input := "```json\n{\"steps\":[{\"id\":\"step_1\",\"description\":\"task\"}]}"
	plan, err := p.parsePlanResponse(input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}
}

// ---------------------------------------------------------------------------
// buildContinuationSystemPrompt: step status labels
// ---------------------------------------------------------------------------

func TestBuildContinuationSystemPrompt_AllCompleted(t *testing.T) {
	cfg := makeTestConfig("CONT: {ORIGINAL-REQUEST} | {COMPLETED-PLAN-SUMMARY} | {TERMINAL-STEPS} | {RECENT-CONVERSATION}")
	cfg.Prompts.ContinuationPreamble = "cont-preamble"
	p := &Planner{Cfg: cfg}

	existingPlan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Description: "first task"},
			{ID: "step_2", Description: "second task"},
		},
	}
	completedSteps := []orchestration.CompletedStep{
		{StepID: "step_1", Output: "done 1", Error: nil},
		{StepID: "step_2", Output: "done 2", Error: nil},
	}

	mode := p.continuationMultiMode()
	result := p.buildContinuationSystemPrompt(context.Background(), mode, "original request", existingPlan, completedSteps, nil, nil, nil)

	if !strings.Contains(result, "[COMPLETED] step_1: first task") {
		t.Error("expected step_1 to be marked [COMPLETED]")
	}
	if !strings.Contains(result, "[COMPLETED] step_2: second task") {
		t.Error("expected step_2 to be marked [COMPLETED]")
	}
	if strings.Contains(result, "[FAILED]") {
		t.Error("expected no [FAILED] labels when all steps succeeded")
	}
	if strings.Contains(result, "[PENDING]") {
		t.Error("expected no [PENDING] labels when all steps completed")
	}
}

func TestBuildContinuationSystemPrompt_MixedStatus(t *testing.T) {
	cfg := makeTestConfig("CONT: {ORIGINAL-REQUEST} | {COMPLETED-PLAN-SUMMARY} | {TERMINAL-STEPS} | {RECENT-CONVERSATION}")
	cfg.Prompts.ContinuationPreamble = "cont-preamble"
	p := &Planner{Cfg: cfg}

	existingPlan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Description: "first task"},
			{ID: "step_2", Description: "second task"},
			{ID: "step_3", Description: "third task"},
		},
	}
	completedSteps := []orchestration.CompletedStep{
		{StepID: "step_1", Output: "done", Error: nil},                      // COMPLETED
		{StepID: "step_2", Output: "partial", Error: errors.New("timeout")}, // FAILED
		// step_3: not present → PENDING
	}

	mode := p.continuationMultiMode()
	result := p.buildContinuationSystemPrompt(context.Background(), mode, "original request", existingPlan, completedSteps, nil, nil, nil)

	if !strings.Contains(result, "[COMPLETED] step_1: first task") {
		t.Error("expected step_1 to be marked [COMPLETED]")
	}
	if !strings.Contains(result, "[FAILED] step_2: second task") {
		t.Error("expected step_2 to be marked [FAILED]")
	}
	if !strings.Contains(result, "[PENDING] step_3: third task") {
		t.Error("expected step_3 to be marked [PENDING]")
	}
}

func TestBuildContinuationSystemPrompt_AllFailed(t *testing.T) {
	cfg := makeTestConfig("CONT: {ORIGINAL-REQUEST} | {COMPLETED-PLAN-SUMMARY} | {TERMINAL-STEPS} | {RECENT-CONVERSATION}")
	cfg.Prompts.ContinuationPreamble = "cont-preamble"
	p := &Planner{Cfg: cfg}

	existingPlan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Description: "first task"},
			{ID: "step_2", Description: "second task"},
		},
	}
	completedSteps := []orchestration.CompletedStep{
		{StepID: "step_1", Output: "oops", Error: errors.New("crash")},
		{StepID: "step_2", Output: "", Error: errors.New("panic")},
	}

	mode := p.continuationMultiMode()
	result := p.buildContinuationSystemPrompt(context.Background(), mode, "original request", existingPlan, completedSteps, nil, nil, nil)

	if strings.Contains(result, "[COMPLETED]") {
		t.Error("expected no [COMPLETED] labels when all steps failed")
	}
	if !strings.Contains(result, "[FAILED] step_1: first task") {
		t.Error("expected step_1 to be marked [FAILED]")
	}
	if !strings.Contains(result, "[FAILED] step_2: second task") {
		t.Error("expected step_2 to be marked [FAILED]")
	}
	if strings.Contains(result, "[PENDING]") {
		t.Error("expected no [PENDING] labels when all steps have results")
	}
}

func TestBuildContinuationSystemPrompt_AllPending(t *testing.T) {
	cfg := makeTestConfig("CONT: {ORIGINAL-REQUEST} | {COMPLETED-PLAN-SUMMARY} | {TERMINAL-STEPS} | {RECENT-CONVERSATION}")
	cfg.Prompts.ContinuationPreamble = "cont-preamble"
	p := &Planner{Cfg: cfg}

	existingPlan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Description: "first task"},
			{ID: "step_2", Description: "second task"},
		},
	}
	// No completed steps at all — all pending.

	mode := p.continuationMultiMode()
	result := p.buildContinuationSystemPrompt(context.Background(), mode, "original request", existingPlan, nil, nil, nil, nil)

	if strings.Contains(result, "[COMPLETED]") {
		t.Error("expected no [COMPLETED] labels when no steps have results")
	}
	if strings.Contains(result, "[FAILED]") {
		t.Error("expected no [FAILED] labels when no steps have results")
	}
	if !strings.Contains(result, "[PENDING] step_1: first task") {
		t.Error("expected step_1 to be marked [PENDING]")
	}
	if !strings.Contains(result, "[PENDING] step_2: second task") {
		t.Error("expected step_2 to be marked [PENDING]")
	}
}

func TestBuildContinuationSystemPrompt_IncludesErrorDetails(t *testing.T) {
	cfg := makeTestConfig("CONT: {ORIGINAL-REQUEST} | {COMPLETED-PLAN-SUMMARY} | {TERMINAL-STEPS} | {RECENT-CONVERSATION}")
	cfg.Prompts.ContinuationPreamble = "cont-preamble"
	p := &Planner{Cfg: cfg}

	existingPlan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Description: "compile"},
		},
	}
	completedSteps := []orchestration.CompletedStep{
		{StepID: "step_1", Output: "build output", Error: errors.New("syntax error at line 42")},
	}

	mode := p.continuationMultiMode()
	result := p.buildContinuationSystemPrompt(context.Background(), mode, "original request", existingPlan, completedSteps, nil, nil, nil)

	if !strings.Contains(result, "syntax error at line 42") {
		t.Error("expected error message to appear in the prompt summary")
	}
}

func TestBuildContinuationSystemPrompt_TerminalStepsIncluded(t *testing.T) {
	cfg := makeTestConfig("CONT: {ORIGINAL-REQUEST} | {COMPLETED-PLAN-SUMMARY} | {TERMINAL-STEPS} | {RECENT-CONVERSATION}")
	cfg.Prompts.ContinuationPreamble = "cont-preamble"
	p := &Planner{Cfg: cfg}

	existingPlan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Description: "first"},
			{ID: "step_2", Description: "second", DependsOn: []string{"step_1"}},
		},
	}

	mode := p.continuationMultiMode()
	result := p.buildContinuationSystemPrompt(context.Background(), mode, "original request", existingPlan, nil, nil, nil, nil)

	if !strings.Contains(result, "step_2") {
		t.Error("expected terminal step ID in prompt")
	}
	if !strings.Contains(result, "original request") {
		t.Error("expected original request in prompt")
	}
}

// ---------------------------------------------------------------------------
// Conversation history in plan prompts
// ---------------------------------------------------------------------------

// capturingLLMCaller records requests and replies with a fixed response.
type capturingLLMCaller struct {
	resp *llm.ChatResponse
	reqs []llm.ChatRequest
}

func (c *capturingLLMCaller) Call(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	c.reqs = append(c.reqs, req)
	return c.resp, nil
}

// TestPlanDirect_ConversationHistoryInPrompt verifies that Plan substitutes
// the conversation history into the RECENT-CONVERSATION placeholder of the
// plan-mode preamble.
func TestPlanDirect_ConversationHistoryInPrompt(t *testing.T) {
	caller := &capturingLLMCaller{
		resp: newPlanResponse(`{"steps":[{"id":"step_1","description":"task"}]}`),
	}
	cfg := makeTestConfig("Plan: MODE-PREAMBLE")
	cfg.Prompts.PlanPreamble = "History:\nRECENT-CONVERSATION"
	p := &Planner{llm: caller, Cfg: cfg}

	history := []llm.Message{
		{Role: "user", Content: "add auth to the API"},
		{Role: "assistant", Content: "auth added"},
	}
	if _, err := p.Plan(context.Background(), "now add tests", nil, nil, nil, false, history); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(caller.reqs) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(caller.reqs))
	}
	var systemPrompt strings.Builder
	for _, msg := range caller.reqs[0].Messages {
		if msg.Role == "system" {
			systemPrompt.WriteString(msg.Content)
		}
	}
	got := systemPrompt.String()
	if !strings.Contains(got, "user: add auth to the API") {
		t.Errorf("system prompt should contain the prior user message, got:\n%s", got)
	}
	if !strings.Contains(got, "assistant: auth added") {
		t.Errorf("system prompt should contain the prior assistant message, got:\n%s", got)
	}
}

// TestPlanDirect_EmptyConversationHistoryPlaceholder verifies that an empty
// history substitutes the "(no previous conversation)" marker instead of
// leaving the placeholder in the prompt.
func TestPlanDirect_EmptyConversationHistoryPlaceholder(t *testing.T) {
	caller := &capturingLLMCaller{
		resp: newPlanResponse(`{"steps":[{"id":"step_1","description":"task"}]}`),
	}
	cfg := makeTestConfig("Plan: MODE-PREAMBLE")
	cfg.Prompts.PlanPreamble = "History:\nRECENT-CONVERSATION"
	p := &Planner{llm: caller, Cfg: cfg}

	if _, err := p.Plan(context.Background(), "first task", nil, nil, nil, false, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var systemPrompt strings.Builder
	for _, msg := range caller.reqs[0].Messages {
		if msg.Role == "system" {
			systemPrompt.WriteString(msg.Content)
		}
	}
	got := systemPrompt.String()
	if !strings.Contains(got, "(no previous conversation)") {
		t.Errorf("system prompt should contain the empty-history marker, got:\n%s", got)
	}
	if strings.Contains(got, "RECENT-CONVERSATION") {
		t.Errorf("RECENT-CONVERSATION placeholder should be substituted, got:\n%s", got)
	}
}
