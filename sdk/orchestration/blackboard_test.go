package orchestration

import (
	"errors"
	"strings"
	"sync"
	"testing"
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

func TestBlackboard_Plan_DefensiveCopy(t *testing.T) {
	bb := NewMapBlackboard()

	if got := bb.GetPlan(); got != nil {
		t.Fatalf("expected nil plan, got %v", got)
	}

	plan := &Plan{
		Steps: []PlanStep{
			{ID: "step_1", Description: "write code"},
			{ID: "step_2", Description: "run tests"},
			{ID: "step_3", Description: "deploy"},
		},
	}
	bb.SetPlan(plan)

	// Mutate original — should not affect blackboard.
	plan.Steps[0].Description = "MUTATED"
	got := bb.GetPlan()
	if got.Steps[0].Description != "write code" {
		t.Fatalf("plan defensive copy broken: got %q", got.Steps[0].Description)
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

	bb.SetStepResult("s1", "Compiled the project successfully", nil, nil)
	bb.AddReflection(Reflection{Summary: "The compilation step went well"})

	// Case-insensitive search for "compil" should match both.
	results := bb.Search("COMPIL")
	if len(results) != 2 {
		t.Fatalf("expected 2 matches, got %d: %v", len(results), results)
	}

	typeCount := map[string]int{}
	for _, e := range results {
		typeCount[e.Type]++
	}
	if typeCount["step_result"] != 1 || typeCount["reflection"] != 1 {
		t.Fatalf("unexpected type distribution: %v", typeCount)
	}
}

func TestBlackboard_Search_NoResults(t *testing.T) {
	bb := NewMapBlackboard()

	bb.SetStepResult("s1", "hello world", nil, nil)

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

func TestCopyPlanPreservesExplorationContext(t *testing.T) {
	original := &Plan{
		Steps: []PlanStep{
			{ID: "step_1", Description: "Do something"},
		},
		ExplorationContext: "- Found file X (via read_file)\n- Project uses Go modules (via list_directory)\n",
	}

	copied := copyPlan(original)

	if copied.ExplorationContext != original.ExplorationContext {
		t.Errorf("ExplorationContext not preserved: got %q, want %q", copied.ExplorationContext, original.ExplorationContext)
	}

	// Verify it's a true copy (modifying copy doesn't affect original)
	copied.ExplorationContext = "modified"
	if original.ExplorationContext == "modified" {
		t.Error("copyPlan did not create independent copy of ExplorationContext")
	}
}

func TestCopyPlanEmptyExplorationContext(t *testing.T) {
	original := &Plan{
		Steps: []PlanStep{
			{ID: "s1", Description: "test"},
		},
	}
	copied := copyPlan(original)
	if copied.ExplorationContext != "" {
		t.Errorf("expected empty ExplorationContext, got %q", copied.ExplorationContext)
	}
}

// ---------------------------------------------------------------------------
// StoreFact / SearchFacts
// ---------------------------------------------------------------------------

func TestBlackboard_StoreFact_Retrievable(t *testing.T) {
	bb := NewMapBlackboard()

	bb.StoreFact(Fact{Keywords: []string{"auth", "login"}, Content: "Users authenticate via OAuth2", Author: "step_1"})

	facts := bb.GetFacts()
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if facts[0].Content != "Users authenticate via OAuth2" {
		t.Fatalf("unexpected content: %q", facts[0].Content)
	}
	if facts[0].Author != "step_1" {
		t.Fatalf("unexpected author: %q", facts[0].Author)
	}
}

func TestBlackboard_SearchFacts_ByKeywords(t *testing.T) {
	bb := NewMapBlackboard()

	bb.StoreFact(Fact{Keywords: []string{"auth", "login", "oauth"}, Content: "OAuth2 used for auth", Author: "s1"})
	bb.StoreFact(Fact{Keywords: []string{"database", "postgres"}, Content: "PostgreSQL is the main DB", Author: "s2"})
	bb.StoreFact(Fact{Keywords: []string{"auth", "roles", "permissions"}, Content: "RBAC for authorization", Author: "s3"})

	// Search for "auth" — should match fact 1 and 3.
	results := bb.SearchFacts([]string{"auth"})
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Search for "auth", "login" — fact 1 matches 2 keywords, fact 3 matches 1.
	results = bb.SearchFacts([]string{"auth", "login"})
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// Fact with 2 matches should come first (sorted by relevance).
	if results[0].Content != "OAuth2 used for auth" {
		t.Fatalf("expected highest relevance first, got %q", results[0].Content)
	}
}

func TestBlackboard_SearchFacts_CaseInsensitive(t *testing.T) {
	bb := NewMapBlackboard()

	bb.StoreFact(Fact{Keywords: []string{"Auth", "LOGIN"}, Content: "case test", Author: "s1"})

	results := bb.SearchFacts([]string{"auth"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result for case-insensitive search, got %d", len(results))
	}

	results = bb.SearchFacts([]string{"LOGIN"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result for uppercase search, got %d", len(results))
	}
}

func TestBlackboard_SearchFacts_NoMatches(t *testing.T) {
	bb := NewMapBlackboard()

	bb.StoreFact(Fact{Keywords: []string{"auth"}, Content: "something", Author: "s1"})

	results := bb.SearchFacts([]string{"nonexistent"})
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestBlackboard_SearchFacts_EmptyKeywords(t *testing.T) {
	bb := NewMapBlackboard()

	bb.StoreFact(Fact{Keywords: []string{"auth"}, Content: "something", Author: "s1"})

	results := bb.SearchFacts(nil)
	if results != nil {
		t.Fatalf("expected nil for nil keywords, got %v", results)
	}
}

func TestBlackboard_SearchFacts_EmptyStore(t *testing.T) {
	bb := NewMapBlackboard()

	results := bb.SearchFacts([]string{"auth"})
	if results != nil {
		t.Fatalf("expected nil for empty store, got %v", results)
	}
}
