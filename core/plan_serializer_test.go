package core

import (
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/sdk/orchestration"
)

const samplePlanMD = `# Step 1: Analyze project structure

**What**: Understand the codebase layout.

**Where**: src/

**How**: Use ls and grep.

**Acceptance Criteria**: Tree produced.

# Step 2: Identify issues

**What**: Find problems.

**Where**: config/

**How**: Inspect config files.

**Acceptance Criteria**: Issue list created.
`

func TestSerializePlan(t *testing.T) {
	plan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{
				ID:      "step_1",
				Summary: "Analyze project structure",
				Description: `### What:
Understand the codebase layout.

### Where:
src/

### How:
Use ls and grep.

### Acceptance Criteria:
Tree produced.`,
			},
			{
				ID:      "step_2",
				Summary: "Identify issues",
				Description: `### What:
Find problems.

### Where:
config/

### How:
Inspect config files.

### Acceptance Criteria:
Issue list created.`,
			},
		},
	}

	result := SerializePlan(plan)
	if !strings.Contains(result, "# Step 1: Analyze project structure") {
		t.Error("missing step 1 header")
	}
	if !strings.Contains(result, "# Step 2: Identify issues") {
		t.Error("missing step 2 header")
	}
	if !strings.Contains(result, "**What**") {
		t.Error("missing What field")
	}
	if !strings.Contains(result, "**Where**") {
		t.Error("missing Where field")
	}
	if !strings.Contains(result, "**How**") {
		t.Error("missing How field")
	}
	if !strings.Contains(result, "**Acceptance Criteria**") {
		t.Error("missing Acceptance Criteria field")
	}
}

func TestSerializePlanNil(t *testing.T) {
	if result := SerializePlan(nil); result != "" {
		t.Errorf("expected empty string for nil plan, got %q", result)
	}
}

func TestSerializePlanEmpty(t *testing.T) {
	plan := &orchestration.Plan{Steps: nil}
	if result := SerializePlan(plan); result != "" {
		t.Errorf("expected empty string for empty plan, got %q", result)
	}
}

func TestSerializePlanEmptyFields(t *testing.T) {
	plan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{
				ID:          "step_1",
				Summary:     "Test",
				Description: "",
			},
		},
	}
	result := SerializePlan(plan)
	if !strings.Contains(result, "# Step 1: Test") {
		t.Error("missing step header")
	}
	// Empty fields should get placeholder "..."
	if !strings.Contains(result, "...") {
		t.Error("empty fields should have placeholder")
	}
}

func TestParsePlanMarkdown(t *testing.T) {
	parsed, errors := ParsePlanMarkdown(samplePlanMD)
	if len(errors) > 0 {
		for _, e := range errors {
			t.Logf("parse error: %v", e)
		}
		t.Fatalf("expected no errors, got %d", len(errors))
	}
	if parsed == nil {
		t.Fatal("expected non-nil result")
	}
	if len(parsed.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(parsed.Steps))
	}
	if parsed.Steps[0].Title != "Step 1: Analyze project structure" {
		t.Errorf("wrong title: %q", parsed.Steps[0].Title)
	}
	if parsed.Steps[0].What != "Understand the codebase layout." {
		t.Errorf("wrong what: %q", parsed.Steps[0].What)
	}
	if parsed.Steps[0].Where != "src/" {
		t.Errorf("wrong where: %q", parsed.Steps[0].Where)
	}
	if parsed.Steps[0].How != "Use ls and grep." {
		t.Errorf("wrong how: %q", parsed.Steps[0].How)
	}
	if parsed.Steps[0].AcceptanceCriteria != "Tree produced." {
		t.Errorf("wrong AC: %q", parsed.Steps[0].AcceptanceCriteria)
	}
}

func TestParsePlanMarkdownEmpty(t *testing.T) {
	_, errors := ParsePlanMarkdown("")
	if len(errors) == 0 {
		t.Error("expected errors for empty input")
	}
}

func TestParsePlanMarkdownNoHeaders(t *testing.T) {
	_, errors := ParsePlanMarkdown("Just some text, no step headers.")
	if len(errors) == 0 {
		t.Error("expected errors for missing headers")
	}
}

func TestParsePlanMarkdownMissingFields(t *testing.T) {
	md := `# Step 1: Test

**What**: Only what, nothing else.
`
	_, errors := ParsePlanMarkdown(md)
	if len(errors) == 0 {
		t.Error("expected errors for missing fields")
	}
}

func TestParsePlanMarkdownNonSequential(t *testing.T) {
	md := `# Step 1: First

**What**: a
**Where**: b
**How**: c
**Acceptance Criteria**: d

# Step 3: Third

**What**: a
**Where**: b
**How**: c
**Acceptance Criteria**: d
`
	_, errors := ParsePlanMarkdown(md)
	if len(errors) == 0 {
		t.Error("expected errors for non-sequential steps")
	}
}

func TestMergePlanSteps(t *testing.T) {
	original := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Summary: "Old 1", Description: "old desc 1", DependsOn: []string{}, Parallelizable: true, EstimatedTools: []string{"ls"}},
			{ID: "step_2", Summary: "Old 2", Description: "old desc 2", DependsOn: []string{"step_1"}},
		},
	}

	parsed := &ParsedPlan{
		Steps: []ParsedStep{
			{Title: "Step 1: New Title 1", What: "new what 1", Where: "new where 1", How: "new how 1", AcceptanceCriteria: "new ac 1"},
			{Title: "Step 2: New Title 2", What: "new what 2", Where: "new where 2", How: "new how 2", AcceptanceCriteria: "new ac 2"},
		},
	}

	merged := MergePlanSteps(parsed, original)
	if len(merged) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(merged))
	}
	if merged[0].Summary != "New Title 1" {
		t.Errorf("expected summary 'New Title 1', got %q", merged[0].Summary)
	}
	if merged[0].Description != "**What**: new what 1\n\n**Where**: new where 1\n\n**How**: new how 1\n\n**Acceptance Criteria**: new ac 1" {
		t.Errorf("wrong description: %q", merged[0].Description)
	}
	// Hidden fields should be preserved
	if !merged[0].Parallelizable {
		t.Error("Parallelizable should be preserved from original")
	}
	if len(merged[0].EstimatedTools) != 1 || merged[0].EstimatedTools[0] != "ls" {
		t.Error("EstimatedTools should be preserved from original")
	}
	if len(merged[1].DependsOn) != 1 || merged[1].DependsOn[0] != "step_1" {
		t.Error("DependsOn should be preserved from original")
	}
}

func TestMergePlanStepsNilParsed(t *testing.T) {
	original := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Summary: "Test"},
		},
	}
	merged := MergePlanSteps(nil, original)
	if len(merged) != 1 {
		t.Fatalf("expected 1 step, got %d", len(merged))
	}
}

func TestMergePlanStepsLongerParsed(t *testing.T) {
	// Parsed has more steps than original
	original := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Summary: "Old", DependsOn: []string{}},
		},
	}
	parsed := &ParsedPlan{
		Steps: []ParsedStep{
			{Title: "Step 1: A", What: "w1", Where: "w1", How: "h1", AcceptanceCriteria: "ac1"},
			{Title: "Step 2: B", What: "w2", Where: "w2", How: "h2", AcceptanceCriteria: "ac2"},
		},
	}
	merged := MergePlanSteps(parsed, original)
	if len(merged) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(merged))
	}
	// Step 2 has no hidden field defaults
	if merged[1].ID != "step_2" {
		t.Errorf("expected step_2 ID, got %q", merged[1].ID)
	}
}

func TestRoundTrip(t *testing.T) {
	original := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{
				ID:      "step_1",
				Summary: "Test step",
				Description: `### What:
Do something.

### Where:
some/file.go

### How:
Use tools.

### Acceptance Criteria:
It works.`,
				DependsOn:      []string{},
				Parallelizable: true,
				EstimatedTools: []string{"read_file", "grep"},
			},
		},
	}

	// Serialize
	md := SerializePlan(original)

	// Parse back
	parsed, errors := ParsePlanMarkdown(md)
	if len(errors) > 0 {
		for _, e := range errors {
			t.Logf("parse error: %v", e)
		}
		t.Fatal("round-trip parse should not produce errors")
	}

	// Merge to recover hidden fields
	merged := MergePlanSteps(parsed, original)
	if len(merged) != 1 {
		t.Fatalf("expected 1 step, got %d", len(merged))
	}
	if merged[0].Summary != "Test step" {
		t.Errorf("summary lost in round-trip: %q", merged[0].Summary)
	}
	if !merged[0].Parallelizable {
		t.Error("Parallelizable lost in round-trip")
	}
	if len(merged[0].EstimatedTools) != 2 {
		t.Error("EstimatedTools lost in round-trip")
	}
}

func TestExtractSummaryFromTitle(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{"Step 1: Hello world", "Hello world"},
		{"Step 10: Complex task", "Complex task"},
		{"No prefix", "No prefix"},
		{"", ""},
	}
	for _, tt := range tests {
		result := extractSummaryFromTitle(tt.title)
		if result != tt.expected {
			t.Errorf("extractSummaryFromTitle(%q) = %q, want %q", tt.title, result, tt.expected)
		}
	}
}
