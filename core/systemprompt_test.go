package core

import (
	"context"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/prompt"
	"github.com/v0lka/sp4rk/skills"
	"github.com/v0lka/sp4rk/tools"
)

func TestFormatVectorSearchHints_Empty(t *testing.T) {
	if got := formatVectorSearchHints(context.Background(), ""); got != "" {
		t.Errorf("expected empty for missing hints, got %q", got)
	}

	ctx := WithVectorSearchHints(context.Background(), &VectorSearchHints{})
	if got := formatVectorSearchHints(ctx, ""); got != "" {
		t.Errorf("expected empty for hints with no files, got %q", got)
	}
}

func TestFormatVectorSearchHints_WithFiles(t *testing.T) {
	ctx := WithVectorSearchHints(context.Background(), &VectorSearchHints{
		Files: []VectorSearchHint{
			{FilePath: "main.go", Summary: "entry point"},
			{FilePath: "lib/util.go"},
		},
	})

	got := formatVectorSearchHints(ctx, "")
	if !strings.Contains(got, "Relevant Project Files") {
		t.Error("missing section header")
	}
	if !strings.Contains(got, "main.go: entry point") {
		t.Error("missing first file entry")
	}
	if !strings.Contains(got, "lib/util.go") {
		t.Error("missing second file entry")
	}
	if strings.Contains(got, "footer:") {
		t.Error("should not include footer when none provided")
	}
}

func TestFormatVectorSearchHints_Footer(t *testing.T) {
	ctx := WithVectorSearchHints(context.Background(), &VectorSearchHints{
		Files: []VectorSearchHint{{FilePath: "main.go"}},
	})

	got := formatVectorSearchHints(ctx, "\nUse semantic_search for more.")
	if !strings.Contains(got, "semantic_search") {
		t.Error("expected footer text in output")
	}
}

func TestFormatActiveSkills_Empty(t *testing.T) {
	if got := formatActiveSkills(context.Background(), "preamble"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}

	ctx := WithActiveSkills(context.Background(), &ActiveSkills{})
	if got := formatActiveSkills(ctx, "preamble"); got != "" {
		t.Errorf("expected empty for ActiveSkills with no skills, got %q", got)
	}
}

func TestFormatActiveSkills_WithSkills(t *testing.T) {
	ctx := WithActiveSkills(context.Background(), &ActiveSkills{
		Skills: []*skills.Skill{
			{
				Metadata: skills.SkillMetadata{Name: "go-testing", Description: "Idiomatic Go tests."},
				Body:     "Step 1: write a table.\nStep 2: subtests.",
			},
		},
	})

	got := formatActiveSkills(ctx, "Test preamble.")
	if !strings.Contains(got, "Active Skills") {
		t.Error("missing Active Skills heading")
	}
	if !strings.Contains(got, "Test preamble.") {
		t.Error("missing preamble")
	}
	if !strings.Contains(got, "go-testing") {
		t.Error("missing skill name")
	}
	if !strings.Contains(got, "Idiomatic Go tests.") {
		t.Error("missing skill description")
	}
	if !strings.Contains(got, "Step 1") {
		t.Error("missing skill body content")
	}
}

func TestFormatAgentsMD_Untrusted(t *testing.T) {
	if got := formatAgentsMD(context.Background()); got != "" {
		t.Errorf("expected empty for missing AgentsMD, got %q", got)
	}

	if got := formatAgentsMD(WithAgentsMD(context.Background(), &AgentsMD{})); got != "" {
		t.Errorf("expected empty for blank AgentsMD content, got %q", got)
	}

	ctx := WithAgentsMD(context.Background(), &AgentsMD{Content: "Use Go 1.26."})
	got := formatAgentsMD(ctx)
	if !strings.Contains(got, "advisory") {
		t.Error("expected advisory framing in AGENTS.md prompt section")
	}
	if !strings.Contains(got, `<untrusted-content source="AGENTS.md">`) {
		t.Error("expected untrusted-content tag wrapping AGENTS.md content")
	}
	if !strings.Contains(got, "Use Go 1.26.") {
		t.Error("expected verbatim AGENTS.md content")
	}
	// Regression guard: must NOT use the old "MUST strictly follow" wording.
	if strings.Contains(got, "MUST strictly follow") {
		t.Error("AGENTS.md prompt section reverted to authoritative wording")
	}
}

func TestRenderGoalModeSection_NilOrEmpty(t *testing.T) {
	if got := renderGoalModeSection(nil); got != "" {
		t.Errorf("expected empty for nil GoalState, got %q", got)
	}
	if got := renderGoalModeSection(&goal.GoalState{}); got != "" {
		t.Errorf("expected empty for GoalState with blank condition, got %q", got)
	}
}

func TestRenderGoalModeSection_ContainsGoalFields(t *testing.T) {
	gs := &goal.GoalState{
		Condition:    "All tests in the auth package pass.",
		VerifyClause: "go test ./auth/... exits 0",
		Budget: goal.GoalBudget{
			MaxTurns: 8,
		},
		TurnCount: 3,
	}

	got := renderGoalModeSection(gs)
	if got == "" {
		t.Fatal("expected non-empty goal section for active goal")
	}
	// Condition is rendered verbatim.
	if !strings.Contains(got, "All tests in the auth package pass.") {
		t.Error("missing goal condition in rendered section")
	}
	// Verify clause is rendered verbatim.
	if !strings.Contains(got, "go test ./auth/... exits 0") {
		t.Error("missing verify clause in rendered section")
	}
	// Evidence mandate text from goal_mode.md.
	if !strings.Contains(got, "Evidence Mandate") {
		t.Error("missing Evidence Mandate heading")
	}
	if !strings.Contains(got, "declare_goal_status") {
		t.Error("missing declare_goal_status reference in evidence mandate")
	}
	// Evidence mandate must demand executed verification, not mere belief.
	if !strings.Contains(got, "VERIFIED") {
		t.Error("evidence mandate should demand executed verification (VERIFIED), not mere belief")
	}
	// A runnable, command-type verify clause must be executed with its real
	// exit code / output cited as evidence.
	if !strings.Contains(got, "exit code") {
		t.Error("evidence mandate should require citing a command's real exit code")
	}
	// Budget line reflects the turn count and cap.
	if !strings.Contains(got, "turn 3/8") {
		t.Error("missing turn budget segment")
	}
}

func TestRenderGoalModeSection_UnlimitedBudget(t *testing.T) {
	gs := &goal.GoalState{
		Condition: "Ship it.",
		// Zero budget => unlimited, zero verify clause => placeholder text.
	}
	got := renderGoalModeSection(gs)
	if !strings.Contains(got, "turn 0/unlimited") {
		t.Errorf("expected unlimited turn rendering, got %q", got)
	}
	// Empty verify clause falls back to an explicit placeholder, not a bare blank.
	if !strings.Contains(got, "verify the condition") {
		t.Error("expected verify-clause fallback text for blank clause")
	}
}

func TestBuildSystemPrompt_GoalSection_PresentWhenActive(t *testing.T) {
	gs := &goal.GoalState{
		Condition:    "Refactor returns no allocations.",
		VerifyClause: "go test -bench=. -benchmem shows 0 allocs/op",
	}
	ctx := WithGoalState(context.Background(), gs)

	sysprompt := buildSystemPrompt(ctx, "do the thing", llmModelMetaForTests())
	if !strings.Contains(sysprompt, "Goal Mode") {
		t.Error("buildSystemPrompt omitted Goal Mode section when goal is active")
	}
	if !strings.Contains(sysprompt, "Refactor returns no allocations.") {
		t.Error("buildSystemPrompt omitted the condition text")
	}
	if !strings.Contains(sysprompt, "Evidence Mandate") {
		t.Error("buildSystemPrompt omitted the evidence mandate")
	}
}

func TestBuildSystemPrompt_GoalSection_AbsentWhenInactive(t *testing.T) {
	ctx := context.Background()

	sysprompt := buildSystemPrompt(ctx, "do the thing", llmModelMetaForTests())
	if strings.Contains(sysprompt, "Goal Mode") {
		t.Error("buildSystemPrompt included Goal Mode section when no goal is active")
	}
	if strings.Contains(sysprompt, "Evidence Mandate") {
		t.Error("buildSystemPrompt included evidence mandate when no goal is active")
	}
}

func TestBuildSystemPrompt_ReviewSection_PresentWhenActive(t *testing.T) {
	ctx := WithReviewMode(context.Background())

	sysprompt := buildSystemPrompt(ctx, "address these comments", llmModelMetaForTests())
	if !strings.Contains(sysprompt, "Code Review Feedback") {
		t.Error("buildSystemPrompt omitted Code Review section when ReviewModeKey is set")
	}
	if !strings.Contains(sysprompt, "actionable change requests") {
		t.Error("buildSystemPrompt omitted the actionable-feedback directive")
	}
}

func TestBuildSystemPrompt_ReviewSection_AbsentWhenInactive(t *testing.T) {
	ctx := context.Background()

	sysprompt := buildSystemPrompt(ctx, "do the thing", llmModelMetaForTests())
	if strings.Contains(sysprompt, "Code Review Feedback") {
		t.Error("buildSystemPrompt included Code Review section when no review is active")
	}
}

// llmModelMetaForTests returns a minimal ModelMetadata sufficient for
// buildSystemPrompt to assemble a prompt without panicking on family lookup.
func llmModelMetaForTests() llm.ModelMetadata {
	return llm.ModelMetadata{Family: "default"}
}

// TestRenderGoalModeStatic_ExcludesBudgetLine verifies the static (cacheable)
// goal section contains the condition and evidence mandate but NOT the
// per-turn budget numbers — which would bust the cacheable prefix across goal
// turns. The static budget note placeholder is emitted instead.
func TestRenderGoalModeStatic_ExcludesBudgetLine(t *testing.T) {
	gs := &goal.GoalState{
		Condition:    "All tests pass.",
		VerifyClause: "go test ./...",
		Budget:       goal.GoalBudget{MaxTurns: 8},
		TurnCount:    3,
	}
	got := renderGoalModeStatic(gs)
	if !strings.Contains(got, "All tests pass.") {
		t.Error("static section missing the condition")
	}
	if !strings.Contains(got, "Evidence Mandate") {
		t.Error("static section missing the evidence mandate")
	}
	// The volatile per-turn budget line must NOT appear in the static section.
	if strings.Contains(got, "turn 3/8") {
		t.Error("static section leaked the volatile turn budget — would bust prompt cache")
	}
	if strings.Contains(got, goalStaticBudgetNote) {
		// the static budget note SHOULD be present (it is session-invariant).
	} else {
		t.Error("static section missing the static budget note placeholder")
	}
}

// TestRenderGoalModeVolatile_OnlyBudgetLine verifies the volatile goal section
// contains ONLY the per-turn budget line (turn count + cap), which is the data
// that legitimately changes every turn.
func TestRenderGoalModeVolatile_OnlyBudgetLine(t *testing.T) {
	gs := &goal.GoalState{
		Condition: "Ship it.",
		Budget:    goal.GoalBudget{MaxTurns: 5},
		TurnCount: 2,
	}
	got := renderGoalModeVolatile(gs)
	if !strings.Contains(got, "turn 2/5") {
		t.Errorf("volatile section missing the turn budget, got %q", got)
	}
	// The condition (which belongs in the static section) must NOT appear.
	if strings.Contains(got, "Ship it.") {
		t.Error("volatile section leaked the condition — belongs in the static prefix")
	}
}

// TestRenderGoalModeStatic_NilOrEmpty mirrors the nil/empty contract of the
// original renderGoalModeSection.
func TestRenderGoalModeStatic_NilOrEmpty(t *testing.T) {
	if got := renderGoalModeStatic(nil); got != "" {
		t.Errorf("expected empty for nil GoalState, got %q", got)
	}
	if got := renderGoalModeStatic(&goal.GoalState{}); got != "" {
		t.Errorf("expected empty for blank-condition GoalState, got %q", got)
	}
}

// TestBuildSystemPrompt_GoalSection_BudgetLineAfterCacheBreak is the key
// regression test for the prompt-caching fix: the per-turn budget line MUST
// live after the CacheBreak boundary so the stable prefix stays session-
// invariant and benefits from provider-side prompt caching across goal turns.
func TestBuildSystemPrompt_GoalSection_BudgetLineAfterCacheBreak(t *testing.T) {
	gs := &goal.GoalState{
		Condition:    "Refactor returns no allocations.",
		VerifyClause: "go test -bench=. -benchmem shows 0 allocs/op",
		Budget:       goal.GoalBudget{MaxTurns: 4},
		TurnCount:    1,
	}
	ctx := WithGoalState(context.Background(), gs)

	full := buildSystemPrompt(ctx, "do the thing", llmModelMetaForTests())
	parts := prompt.SplitCacheBreak(full)
	if len(parts) < 2 {
		t.Fatalf("expected at least 2 prompt parts (stable + dynamic), got %d — CacheBreak not present", len(parts))
	}
	stable, dynamic := parts[0], strings.Join(parts[1:], "")

	// The stable (cacheable) prefix MUST contain the goal condition and
	// evidence mandate so the agent has its success criteria cached.
	if !strings.Contains(stable, "Refactor returns no allocations.") {
		t.Error("stable prefix missing the goal condition")
	}
	if !strings.Contains(stable, "Evidence Mandate") {
		t.Error("stable prefix missing the evidence mandate")
	}
	// The stable prefix MUST NOT contain the volatile per-turn budget line —
	// that changes every turn and would invalidate the cache.
	if strings.Contains(stable, "turn 1/4") {
		t.Error("stable prefix contains the volatile turn budget — would bust prompt cache across goal turns")
	}

	// The dynamic (post-CacheBreak) tail MUST contain the volatile budget line.
	if !strings.Contains(dynamic, "turn 1/4") {
		t.Error("dynamic tail missing the turn budget line")
	}
}

// TestBuildSpecializedSystemPrompt_IncludesProjectContext is the key regression
// test for the derivation-context fix: a specialized run (goal derivation)
// MUST see the same project context a normal run does — AGENTS.md, workspace,
// environment, work directories — alongside its specialized core directive,
// not just the bare directive. Before the fix, systemPromptOverride returned
// only the bare GoalDerivation prompt, dropping all project context.
func TestBuildSpecializedSystemPrompt_IncludesProjectContext(t *testing.T) {
	ctx := tools.WithWorkspacePath(context.Background(), "/test/workspace")
	ctx = tools.WithTempDir(ctx, "/test/workspace/.tmp")
	ctx = tools.WithEnvInfo(ctx, &tools.EnvInfo{
		OS:   "darwin",
		Arch: "arm64",
	})
	ctx = WithAgentsMD(ctx, &AgentsMD{Content: "# Project Rules\nUse Go 1.26 and make test."})
	ctx = WithWorkDirectories(ctx, []WorkDirectory{
		{Path: "/aux/sdk", Description: "local SDK checkout"},
	})

	got := buildSpecializedSystemPrompt(ctx, "derive a goal", llmModelMetaForTests(), prompts.GoalDerivation)

	// The specialized core directive is present.
	if !strings.Contains(got, "Goal Derivation Agent") {
		t.Error("specialized prompt missing the GoalDerivation core directive")
	}
	// Shared project context is present — the whole point of the fix.
	if !strings.Contains(got, "/test/workspace") {
		t.Error("specialized prompt missing the workspace path (shared prefix dropped)")
	}
	if !strings.Contains(got, "Use Go 1.26 and make test.") {
		t.Error("specialized prompt missing AGENTS.md content (shared prefix dropped)")
	}
	if !strings.Contains(got, "OS: darwin") {
		t.Error("specialized prompt missing the environment block (shared prefix dropped)")
	}
	if !strings.Contains(got, "Additional Work Directories") {
		t.Error("specialized prompt missing work directories section (shared prefix dropped)")
	}
	if !strings.Contains(got, "/aux/sdk") {
		t.Error("specialized prompt missing the work-directory entry")
	}
}

// TestBuildSpecializedSystemPrompt_OmitsModeBlock verifies that a specialized
// run does NOT inherit the orchestrator's plan/completion mode block — the
// specialized directive defines its own completion semantics. prepareRequestContext
// sets PlanModeKey for every message (including the goal path), so the
// specialized prompt must ignore it rather than emit a "Plan Context" block.
func TestBuildSpecializedSystemPrompt_OmitsModeBlock(t *testing.T) {
	ctx := context.WithValue(tools.WithWorkspacePath(context.Background(), "/ws"), PlanModeKey, true)

	got := buildSpecializedSystemPrompt(ctx, "derive", llmModelMetaForTests(), prompts.GoalDerivation)

	if strings.Contains(got, "single-step mode") {
		t.Error("specialized prompt leaked the orchestrator Completion mode block")
	}
	if strings.Contains(got, "Plan Context") {
		t.Error("specialized prompt leaked the orchestrator Plan Context mode block (PlanModeKey was ignored)")
	}
}

// TestBuildSpecializedSystemPrompt_OmitsGoalSections verifies that even when a
// goal state is present in the context, a specialized run does not render the
// goal-mode sections — derivation runs before a goal exists, and the
// specialized directive owns the task framing.
func TestBuildSpecializedSystemPrompt_OmitsGoalSections(t *testing.T) {
	gs := &goal.GoalState{
		Condition:    "Should not appear in a specialized run.",
		VerifyClause: "go test ./...",
	}
	ctx := WithGoalState(tools.WithWorkspacePath(context.Background(), "/ws"), gs)

	got := buildSpecializedSystemPrompt(ctx, "derive", llmModelMetaForTests(), prompts.GoalDerivation)

	if strings.Contains(got, "Goal Mode") {
		t.Error("specialized prompt rendered the goal-mode section")
	}
	if strings.Contains(got, "Should not appear in a specialized run.") {
		t.Error("specialized prompt rendered the goal condition")
	}
}

// TestBuildSpecializedSystemPrompt_PreservesCacheBreak verifies the
// CacheBreak boundary survives the refactor: the shared project context
// (AGENTS.md) and the specialized core directive land in the stable prefix so
// they benefit from prompt caching. Vector search hints are emitted after the
// CacheBreak boundary, so injecting one forces a dynamic tail and exercises
// the stable/dynamic split.
func TestBuildSpecializedSystemPrompt_PreservesCacheBreak(t *testing.T) {
	ctx := tools.WithWorkspacePath(context.Background(), "/ws")
	ctx = WithAgentsMD(ctx, &AgentsMD{Content: "cached conventions"})
	ctx = WithVectorSearchHints(ctx, &VectorSearchHints{
		Files: []VectorSearchHint{{FilePath: "main.go", Summary: "entry point"}},
	})

	full := buildSpecializedSystemPrompt(ctx, "derive", llmModelMetaForTests(), prompts.GoalDerivation)
	parts := prompt.SplitCacheBreak(full)
	if len(parts) < 2 {
		t.Fatalf("expected a CacheBreak boundary (vector hints should create a dynamic tail), got %d part(s)", len(parts))
	}
	if !strings.Contains(parts[0], "cached conventions") {
		t.Error("AGENTS.md content not in the stable (cacheable) prefix")
	}
	if !strings.Contains(parts[0], "Goal Derivation Agent") {
		t.Error("specialized core directive not in the stable (cacheable) prefix")
	}
}

// TestBuildSpecializedAndNormalShareProjectContext cross-checks that a
// specialized run and a normal run built from the same context both carry the
// shared project context (AGENTS.md, workspace), proving the refactor did not
// regress the normal path and that the two run types share the prefix.
func TestBuildSpecializedAndNormalShareProjectContext(t *testing.T) {
	ctx := tools.WithWorkspacePath(context.Background(), "/shared/ws")
	ctx = WithAgentsMD(ctx, &AgentsMD{Content: "shared AGENTS.md content"})

	normal := buildSystemPrompt(ctx, "do work", llmModelMetaForTests())
	specialized := buildSpecializedSystemPrompt(ctx, "derive", llmModelMetaForTests(), prompts.GoalDerivation)

	for _, p := range []struct{ name, prompt string }{
		{"normal", normal},
		{"specialized", specialized},
	} {
		if !strings.Contains(p.prompt, "/shared/ws") {
			t.Errorf("%s prompt missing the shared workspace path", p.name)
		}
		if !strings.Contains(p.prompt, "shared AGENTS.md content") {
			t.Errorf("%s prompt missing the shared AGENTS.md content", p.name)
		}
	}
}

// TestBuildSystemPrompt_AvailableAgents verifies the Conductor system prompt
// gains a "## Available Subagents" section when the discovered subagent catalog
// is attached via WithAvailableAgents. Each non-hidden agent's name and
// description must appear.
func TestBuildSystemPrompt_AvailableAgents(t *testing.T) {
	ctx := tools.WithWorkspacePath(context.Background(), "/test/workspace")
	ctx = WithAvailableAgents(ctx, []agents.AgentDescriptor{
		{Name: "code-reviewer", Description: "Reviews Go code for style and correctness."},
		{Name: "test-writer", Description: "Generates table-driven tests."},
	})

	result := buildSystemPrompt(ctx, "refactor this", llmModelMetaForTests())

	if !strings.Contains(result, "## Available Subagents") {
		t.Error("prompt should contain Available Subagents section when agents are available")
	}
	if !strings.Contains(result, "code-reviewer") {
		t.Error("prompt should list the code-reviewer agent name")
	}
	if !strings.Contains(result, "Reviews Go code for style and correctness.") {
		t.Error("prompt should carry the code-reviewer description")
	}
	if !strings.Contains(result, "test-writer") {
		t.Error("prompt should list the test-writer agent name")
	}
}

// TestBuildSystemPrompt_AvailableAgents_HiddenExcluded verifies that hidden
// agents are NOT advertised in the public "Available Subagents" roster.
func TestBuildSystemPrompt_AvailableAgents_HiddenExcluded(t *testing.T) {
	ctx := tools.WithWorkspacePath(context.Background(), "/test/workspace")
	ctx = WithAvailableAgents(ctx, []agents.AgentDescriptor{
		{Name: "public-agent", Description: "visible"},
		{Name: "secret-agent", Description: "should not show", Hidden: true},
	})

	result := buildSystemPrompt(ctx, "do work", llmModelMetaForTests())

	if !strings.Contains(result, "## Available Subagents") {
		t.Fatal("expected Available Subagents section")
	}
	if strings.Contains(result, "secret-agent") {
		t.Error("hidden agent must NOT appear in the public Available Subagents roster")
	}
	if !strings.Contains(result, "public-agent") {
		t.Error("non-hidden agent should appear in the roster")
	}
}

// TestBuildSystemPrompt_NoAvailableAgents verifies that when no agents are in
// the context, the prompt does NOT contain the Available Subagents section
// (no regression for projects without subagents).
func TestBuildSystemPrompt_NoAvailableAgents(t *testing.T) {
	ctx := tools.WithWorkspacePath(context.Background(), "/test/workspace")

	result := buildSystemPrompt(ctx, "do work", llmModelMetaForTests())

	if strings.Contains(result, "Available Subagents") {
		t.Error("prompt should NOT contain Available Subagents section when no agents are available")
	}
}

// TestBuildSystemPrompt_RequestedAgents verifies that explicit #mentions
// (UserAgents) produce a "## Requested Subagents" directive section, and that
// the directive resolves each named agent's description from the catalog.
func TestBuildSystemPrompt_RequestedAgents(t *testing.T) {
	ctx := tools.WithWorkspacePath(context.Background(), "/test/workspace")
	ctx = WithAvailableAgents(ctx, []agents.AgentDescriptor{
		{Name: "code-reviewer", Description: "Reviews Go code for style and correctness."},
	})
	ctx = WithUserAgents(ctx, []string{"code-reviewer"})

	result := buildSystemPrompt(ctx, "review my code #code-reviewer", llmModelMetaForTests())

	if !strings.Contains(result, "## Requested Subagents") {
		t.Fatal("prompt should contain Requested Subagents section when agents are #mentioned")
	}
	if !strings.Contains(result, "code-reviewer") {
		t.Error("prompt should name the requested agent")
	}
	if !strings.Contains(result, "Reviews Go code for style and correctness.") {
		t.Error("prompt should resolve the requested agent's description from the catalog")
	}
	if !strings.Contains(result, "delegate(agent:") {
		t.Error("requested section should direct the agent to use delegate(agent: \"name\")")
	}
}

// TestBuildSystemPrompt_RequestedAgents_UnknownKeptWithoutDescription verifies
// that a requested agent not present in the catalog is still listed (the user
// explicitly asked for it) but without a description, surfacing the mismatch.
func TestBuildSystemPrompt_RequestedAgents_UnknownKeptWithoutDescription(t *testing.T) {
	ctx := tools.WithWorkspacePath(context.Background(), "/test/workspace")
	ctx = WithUserAgents(ctx, []string{"does-not-exist"})

	result := buildSystemPrompt(ctx, "delegate #does-not-exist", llmModelMetaForTests())

	if !strings.Contains(result, "## Requested Subagents") {
		t.Fatal("requested section should render even for unknown agents")
	}
	if !strings.Contains(result, "does-not-exist") {
		t.Error("unknown requested agent name must still be listed")
	}
}

// TestBuildSystemPrompt_NoRequestedAgents verifies that without #mentions there
// is no Requested Subagents section (no regression).
func TestBuildSystemPrompt_NoRequestedAgents(t *testing.T) {
	ctx := tools.WithWorkspacePath(context.Background(), "/test/workspace")
	// Available catalog present but no explicit mentions.
	ctx = WithAvailableAgents(ctx, []agents.AgentDescriptor{
		{Name: "code-reviewer", Description: "Reviews Go code."},
	})

	result := buildSystemPrompt(ctx, "do work", llmModelMetaForTests())

	if strings.Contains(result, "Requested Subagents") {
		t.Error("prompt should NOT contain Requested Subagents section when no agents are #mentioned")
	}
}

// TestBuildSpecializedSystemPrompt_OmitsAgentSections verifies that the
// specialized prompt (goal derivation) does NOT contain the Available or
// Requested Subagents sections, even when the context carries agents. The
// sections are Conductor-only — specialized runs define their own delegation
// semantics.
func TestBuildSpecializedSystemPrompt_OmitsAgentSections(t *testing.T) {
	ctx := tools.WithWorkspacePath(context.Background(), "/test/workspace")
	ctx = WithAvailableAgents(ctx, []agents.AgentDescriptor{
		{Name: "code-reviewer", Description: "Reviews Go code."},
	})
	ctx = WithUserAgents(ctx, []string{"code-reviewer"})

	specialized := buildSpecializedSystemPrompt(ctx, "derive a goal", llmModelMetaForTests(), prompts.GoalDerivation)

	if strings.Contains(specialized, "Available Subagents") {
		t.Error("specialized prompt must NOT contain the Available Subagents section")
	}
	if strings.Contains(specialized, "Requested Subagents") {
		t.Error("specialized prompt must NOT contain the Requested Subagents section")
	}
}

// TestWithAvailableAgents_RoundTrip and TestWithUserAgents_RoundTrip verify the
// context helpers round-trip and default to nil/empty on a bare context.
func TestWithAvailableAgents_RoundTrip(t *testing.T) {
	if got := AvailableAgentsFromContext(context.Background()); got != nil {
		t.Errorf("empty ctx: got %v, want nil", got)
	}

	want := []agents.AgentDescriptor{
		{Name: "a", Description: "alpha"},
		{Name: "b", Description: "beta", Hidden: true},
	}
	ctx := WithAvailableAgents(context.Background(), want)
	got := AvailableAgentsFromContext(ctx)
	if len(got) != len(want) {
		t.Fatalf("got %d descriptors, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestWithUserAgents_RoundTrip(t *testing.T) {
	if got := UserAgentsFromContext(context.Background()); got != nil {
		t.Errorf("empty ctx: got %v, want nil", got)
	}

	want := []string{"code-reviewer", "test-writer"}
	ctx := WithUserAgents(context.Background(), want)
	got := UserAgentsFromContext(ctx)
	if len(got) != len(want) {
		t.Fatalf("got %d agents, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i], w)
		}
	}
}
