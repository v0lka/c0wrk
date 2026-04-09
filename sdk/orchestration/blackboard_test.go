package orchestration

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/user/agent/sdk/agent"
)

func TestBlackboard_OriginalRequest(t *testing.T) {
	bb := NewMapBlackboard()

	if got := bb.GetOriginalRequest(); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}

	bb.SetOriginalRequest("build a CLI tool")
	if got := bb.GetOriginalRequest(); got != "build a CLI tool" {
		t.Fatalf("expected 'build a CLI tool', got %q", got)
	}
}

func TestBlackboard_Criteria_DefensiveCopy(t *testing.T) {
	bb := NewMapBlackboard()

	if got := bb.GetCriteria(); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}

	criteria := []Criterion{
		{ID: "ac_1", Description: "must compile"},
		{ID: "ac_2", Description: "must pass tests"},
	}
	bb.SetCriteria(criteria)

	// Mutate original — should not affect blackboard.
	criteria[0].Description = "MUTATED"

	got := bb.GetCriteria()
	if len(got) != 2 {
		t.Fatalf("expected 2 criteria, got %d", len(got))
	}
	if got[0].Description != "must compile" {
		t.Fatalf("defensive copy broken on set: got %q", got[0].Description)
	}

	// Mutate returned slice — should not affect blackboard.
	got[1].Description = "MUTATED"
	got2 := bb.GetCriteria()
	if got2[1].Description != "must pass tests" {
		t.Fatalf("defensive copy broken on get: got %q", got2[1].Description)
	}
}

func TestBlackboard_Plan_And_GetStepsByAC(t *testing.T) {
	bb := NewMapBlackboard()

	if got := bb.GetPlan(); got != nil {
		t.Fatalf("expected nil plan, got %v", got)
	}

	plan := &Plan{
		Steps: []PlanStep{
			{ID: "step_1", Description: "write code", RelevantAC: []string{"ac_1", "ac_2"}},
			{ID: "step_2", Description: "run tests", RelevantAC: []string{"ac_2"}},
			{ID: "step_3", Description: "deploy", RelevantAC: []string{"ac_3"}},
		},
	}
	bb.SetPlan(plan)

	// Mutate original — should not affect blackboard.
	plan.Steps[0].Description = "MUTATED"
	got := bb.GetPlan()
	if got.Steps[0].Description != "write code" {
		t.Fatalf("plan defensive copy broken: got %q", got.Steps[0].Description)
	}

	// Set step results.
	bb.SetStepResult("step_1", "code written", nil, nil)
	bb.SetStepResult("step_2", "tests passed", nil, nil)

	// GetStepsByAC for ac_2 should return step_1 and step_2.
	results := bb.GetStepsByAC("ac_2")
	if len(results) != 2 {
		t.Fatalf("expected 2 results for ac_2, got %d", len(results))
	}
	ids := map[string]bool{}
	for _, r := range results {
		ids[r.StepID] = true
	}
	if !ids["step_1"] || !ids["step_2"] {
		t.Fatalf("unexpected step IDs: %v", ids)
	}

	// ac_3 has no completed step result.
	if results := bb.GetStepsByAC("ac_3"); len(results) != 0 {
		t.Fatalf("expected 0 results for ac_3, got %d", len(results))
	}

	// No plan → nil results.
	bb2 := NewMapBlackboard()
	if results := bb2.GetStepsByAC("ac_1"); results != nil {
		t.Fatalf("expected nil, got %v", results)
	}
}

func TestBlackboard_StepResult_SummaryAutoGen(t *testing.T) {
	bb := NewMapBlackboard()

	output := "First paragraph here.\n\nSecond paragraph that should be excluded."
	bb.SetStepResult("s1", output, nil, nil)

	r, ok := bb.GetStepResult("s1")
	if !ok {
		t.Fatal("expected to find step result")
	}
	if r.Summary != "First paragraph here." {
		t.Fatalf("expected first paragraph summary, got %q", r.Summary)
	}
	if r.FullOutput != output {
		t.Fatalf("full output mismatch")
	}
}

func TestBlackboard_StepResult_SummaryTruncation500(t *testing.T) {
	bb := NewMapBlackboard()

	// Output longer than 500 chars with no paragraph break.
	longOutput := strings.Repeat("x", 600)
	bb.SetStepResult("s1", longOutput, nil, nil)

	r, _ := bb.GetStepResult("s1")
	if len(r.Summary) != 503 { // 500 + "..."
		t.Fatalf("expected summary length 503, got %d", len(r.Summary))
	}
	if !strings.HasSuffix(r.Summary, "...") {
		t.Fatalf("expected summary to end with '...', got %q", r.Summary[len(r.Summary)-5:])
	}
}

func TestBlackboard_StepResult_ParagraphShorterThan500(t *testing.T) {
	bb := NewMapBlackboard()

	output := "Short paragraph.\n\n" + strings.Repeat("x", 600)
	bb.SetStepResult("s1", output, nil, nil)

	r, _ := bb.GetStepResult("s1")
	if r.Summary != "Short paragraph." {
		t.Fatalf("expected 'Short paragraph.', got %q", r.Summary)
	}
}

func TestBlackboard_StepResult_WithError(t *testing.T) {
	bb := NewMapBlackboard()

	testErr := errors.New("step failed")
	bb.SetStepResult("s1", "partial output", testErr, nil)

	r, ok := bb.GetStepResult("s1")
	if !ok {
		t.Fatal("expected to find step result")
	}
	if r.Error == nil || r.Error.Error() != "step failed" {
		t.Fatalf("error mismatch: %v", r.Error)
	}
}

func TestBlackboard_GetStepSummary(t *testing.T) {
	bb := NewMapBlackboard()

	if got := bb.GetStepSummary("nonexistent"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}

	bb.SetStepResult("s1", "hello world", nil, nil)
	if got := bb.GetStepSummary("s1"); got != "hello world" {
		t.Fatalf("expected 'hello world', got %q", got)
	}
}

func TestBlackboard_Reflections_Ordering(t *testing.T) {
	bb := NewMapBlackboard()

	if got := bb.GetReflections(); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}

	r1 := Reflection{Summary: "first reflection"}
	r2 := Reflection{Summary: "second reflection"}
	r3 := Reflection{Summary: "third reflection"}

	bb.AddReflection(r1)
	bb.AddReflection(r2)
	bb.AddReflection(r3)

	got := bb.GetReflections()
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	if got[0].Summary != "first reflection" || got[1].Summary != "second reflection" || got[2].Summary != "third reflection" {
		t.Fatalf("ordering broken: %v", got)
	}

	// Defensive copy: mutate returned slice.
	got[0].Summary = "MUTATED"
	got2 := bb.GetReflections()
	if got2[0].Summary != "first reflection" {
		t.Fatalf("defensive copy broken: %q", got2[0].Summary)
	}
}

func TestBlackboard_FinalResult(t *testing.T) {
	bb := NewMapBlackboard()

	if got := bb.GetFinalResult(); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}

	bb.SetFinalResult("task completed successfully")
	if got := bb.GetFinalResult(); got != "task completed successfully" {
		t.Fatalf("expected final result, got %q", got)
	}
}

func TestBlackboard_GetAllStepResults_DefensiveCopy(t *testing.T) {
	bb := NewMapBlackboard()

	bb.SetStepResult("s1", "output one", nil, nil)
	bb.SetStepResult("s2", "output two", nil, nil)

	all := bb.GetAllStepResults()
	if len(all) != 2 {
		t.Fatalf("expected 2 results, got %d", len(all))
	}

	// Mutate returned map — should not affect blackboard.
	delete(all, "s1")
	all2 := bb.GetAllStepResults()
	if len(all2) != 2 {
		t.Fatalf("defensive copy broken: expected 2, got %d", len(all2))
	}
}

func TestBlackboard_Search_CaseInsensitive(t *testing.T) {
	bb := NewMapBlackboard()

	bb.SetCriteria([]Criterion{
		{ID: "ac_1", Description: "Must compile without errors"},
	})
	bb.SetStepResult("s1", "Compiled the project successfully", nil, nil)
	bb.AddReflection(Reflection{Summary: "The compilation step went well"})

	// Case-insensitive search for "compil" should match all three.
	results := bb.Search("COMPIL")
	if len(results) != 3 {
		t.Fatalf("expected 3 matches, got %d: %v", len(results), results)
	}

	typeCount := map[string]int{}
	for _, e := range results {
		typeCount[e.Type]++
	}
	if typeCount["step_result"] != 1 || typeCount["criterion"] != 1 || typeCount["reflection"] != 1 {
		t.Fatalf("unexpected type distribution: %v", typeCount)
	}
}

func TestBlackboard_Search_NoResults(t *testing.T) {
	bb := NewMapBlackboard()

	bb.SetStepResult("s1", "hello world", nil, nil)
	bb.SetCriteria([]Criterion{{ID: "ac_1", Description: "must pass"}})

	results := bb.Search("zzzznonexistentzzzz")
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestBlackboard_ConcurrentReadWrite(t *testing.T) {
	bb := NewMapBlackboard()
	const goroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines * 2) // writers + readers

	// Writers
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				stepID := "step"
				bb.SetStepResult(stepID, "output", nil, nil)
				bb.SetOriginalRequest("request")
				bb.SetCriteria([]Criterion{{ID: "ac_1"}})
				bb.SetPlan(&Plan{Steps: []PlanStep{{ID: "s1"}}})
				bb.AddReflection(Reflection{Summary: "r"})
				bb.SetFinalResult("done")
			}
		}(g)
	}

	// Readers
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = bb.GetOriginalRequest()
				_ = bb.GetCriteria()
				_ = bb.GetPlan()
				_, _ = bb.GetStepResult("step")
				_ = bb.GetStepSummary("step")
				_ = bb.GetAllStepResults()
				_ = bb.GetReflections()
				_ = bb.GetFinalResult()
				_ = bb.Search("output")
			}
		}()
	}

	wg.Wait()
}

func TestBlackboard_StepResult_EmptyOutput(t *testing.T) {
	bb := NewMapBlackboard()
	bb.SetStepResult("s1", "", nil, nil)

	r, ok := bb.GetStepResult("s1")
	if !ok {
		t.Fatal("expected to find step result")
	}
	if r.Summary != "" {
		t.Fatalf("expected empty summary, got %q", r.Summary)
	}
}

func TestMapBlackboard_WithMaxSummaryTokens(t *testing.T) {
	// Token budget = 10 tokens → 40 chars max for summary.
	bb := NewMapBlackboard(WithMaxSummaryTokens(10))

	// Output with a single paragraph longer than 40 chars but shorter than 500 chars.
	output := strings.Repeat("a", 100)
	bb.SetStepResult("s1", output, nil, nil)

	r, ok := bb.GetStepResult("s1")
	if !ok {
		t.Fatal("expected to find step result")
	}
	// generateSummary produces 100-char string (no paragraph break, under 500).
	// Token cap should truncate to 40 chars + "...".
	if len(r.Summary) != 43 { // 40 + "..."
		t.Fatalf("expected summary length 43, got %d", len(r.Summary))
	}
	if !strings.HasSuffix(r.Summary, "...") {
		t.Fatalf("expected summary to end with '...', got %q", r.Summary)
	}

	// Verify full output is untouched.
	if r.FullOutput != output {
		t.Fatalf("full output should not be modified")
	}
}

func TestMapBlackboard_GetStepResultBudgeted(t *testing.T) {
	bb := NewMapBlackboard()

	output := strings.Repeat("x", 1000)
	bb.SetStepResult("s1", output, nil, nil)

	// Budget = 50 tokens → 200 chars max.
	r, ok := bb.GetStepResultBudgeted("s1", 50)
	if !ok {
		t.Fatal("expected to find step result")
	}
	if len(r.FullOutput) != 203 { // 200 + "..."
		t.Fatalf("expected full output length 203, got %d", len(r.FullOutput))
	}
	if !strings.HasSuffix(r.FullOutput, "...") {
		t.Fatalf("expected truncation suffix")
	}
}

func TestMapBlackboard_GetStepResultBudgeted_ZeroMeansUnlimited(t *testing.T) {
	bb := NewMapBlackboard()

	output := strings.Repeat("y", 5000)
	bb.SetStepResult("s1", output, nil, nil)

	// maxOutputTokens = 0 → full output returned.
	r, ok := bb.GetStepResultBudgeted("s1", 0)
	if !ok {
		t.Fatal("expected to find step result")
	}
	if r.FullOutput != output {
		t.Fatalf("expected full output when maxOutputTokens is 0, got len %d", len(r.FullOutput))
	}
}

func TestMapBlackboard_GetStepResultBudgeted_NotFound(t *testing.T) {
	bb := NewMapBlackboard()
	_, ok := bb.GetStepResultBudgeted("nonexistent", 100)
	if ok {
		t.Fatal("expected not found")
	}
}

func TestBlackboard_StepResult_StepsCopy(t *testing.T) {
	bb := NewMapBlackboard()

	steps := []agent.Step{
		{Thought: "thinking"},
	}
	bb.SetStepResult("s1", "output", nil, steps)

	// Mutate original steps — should not affect blackboard.
	steps[0].Thought = "MUTATED"

	r, _ := bb.GetStepResult("s1")
	if r.Steps[0].Thought != "thinking" {
		t.Fatalf("step copy broken: got %q", r.Steps[0].Thought)
	}
}
