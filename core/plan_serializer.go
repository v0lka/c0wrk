package core

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/v0lka/c0wrk/sdk/orchestration"
)

// ParsedPlan holds the user-visible parts of a plan extracted from markdown.
type ParsedPlan struct {
	Steps []ParsedStep
}

// ParsedStep holds the user-editable fields of a single plan step.
type ParsedStep struct {
	Title              string // "Step N: <summary>"
	What               string
	Where              string
	How                string
	AcceptanceCriteria string
}

// PlanParseError describes a structural parse failure.
type PlanParseError struct {
	StepNum int    // 1-based step number, 0 if not step-specific
	Field   string // "what" | "where" | "how" | "acceptance_criteria" | "title" | "structure"
	Detail  string
}

// Error implements the error interface.
func (e PlanParseError) Error() string {
	return fmt.Sprintf("step %d, field %q: %s", e.StepNum, e.Field, e.Detail)
}

// stepHeaderRe matches "# Step N: <title>" headings.
var stepHeaderRe = regexp.MustCompile(`(?m)^# Step (\d+): (.+)$`)

// whitespaceRe collapses multiple whitespace characters, used by detectFieldHeader.
var whitespaceRe = regexp.MustCompile(`\s+`)

// summaryTitleRe strips the "Step N: " prefix from a step title.
var summaryTitleRe = regexp.MustCompile(`^Step \d+: `)

// SerializePlan converts a Plan to markdown for user review.
// Only Summary and Description are included; DependsOn, Profile, etc. are hidden.
func SerializePlan(plan *orchestration.Plan) string {
	if plan == nil || len(plan.Steps) == 0 {
		return ""
	}
	var b strings.Builder
	for i, step := range plan.Steps {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("# Step %d: %s\n\n", i+1, step.Summary))
		fields := parseDescriptionFields(step.Description)
		b.WriteString(fmt.Sprintf("**What**: %s\n\n", fieldOrPlaceholder(fields["what"], "...")))
		b.WriteString(fmt.Sprintf("**Where**: %s\n\n", fieldOrPlaceholder(fields["where"], "...")))
		b.WriteString(fmt.Sprintf("**How**: %s\n\n", fieldOrPlaceholder(fields["how"], "...")))
		b.WriteString(fmt.Sprintf("**Acceptance Criteria**: %s\n", fieldOrPlaceholder(fields["acceptance_criteria"], "...")))
	}
	return b.String()
}

// fieldOrPlaceholder returns the field value or a placeholder if empty.
func fieldOrPlaceholder(value, placeholder string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return placeholder
	}
	return v
}

// parseDescriptionFields extracts What/How/Where/Acceptance Criteria from a
// markdown description. Handles both "### What:" and "**What**:" formats.
func parseDescriptionFields(desc string) map[string]string {
	result := map[string]string{
		"what":                "",
		"where":               "",
		"how":                 "",
		"acceptance_criteria": "",
	}

	lines := strings.Split(desc, "\n")
	var currentField string
	var contentLines []string

	flushField := func() {
		if currentField != "" {
			result[currentField] = strings.TrimSpace(strings.Join(contentLines, "\n"))
			contentLines = nil
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		fieldName, fieldContent, isHeader := detectFieldHeader(trimmed)
		if isHeader {
			flushField()
			currentField = fieldName
			if fieldContent != "" {
				contentLines = append(contentLines, fieldContent)
			}
			continue
		}

		if currentField != "" {
			contentLines = append(contentLines, trimmed)
		}
	}
	flushField()

	return result
}

// detectFieldHeader checks if a line is a field header (e.g., "### What:" or "**What**: content").
// Returns the normalized field name, any inline content, and whether it was a header.
func detectFieldHeader(line string) (fieldName, content string, isHeader bool) {
	// Normalize whitespace: collapse multiple spaces to single underscore (matching
	// frontend planParser.ts behavior).
	normalize := func(s string) string {
		s = strings.TrimSpace(s)
		s = whitespaceRe.ReplaceAllString(s, "_")
		return strings.ToLower(s)
	}
	// Pattern: "### What:" or "### What"
	if strings.HasPrefix(line, "###") {
		after := strings.TrimLeft(line[3:], " ")
		after = strings.TrimRight(after, ":")
		name := normalize(after)
		if name == "what" || name == "where" || name == "how" || name == "acceptance_criteria" {
			return name, "", true
		}
		return "", "", false
	}

	// Pattern: "**What**: content" or "**What**:"
	if strings.HasPrefix(line, "**") {
		after := line[2:]
		// Find the separator: "**" or ":" — which comes after the field name.
		// The format is "**FieldName**: rest" or "**FieldName** rest"
		idxStar := strings.Index(after, "**")
		idxColon := strings.Index(after, ":")
	
		var sepIdx int
		if idxStar >= 0 && idxColon >= 0 {
			// Take the earlier separator
			if idxStar < idxColon {
				sepIdx = idxStar
			} else {
				sepIdx = idxColon
			}
		} else if idxStar >= 0 {
			sepIdx = idxStar
		} else if idxColon >= 0 {
			sepIdx = idxColon
		} else {
			return "", "", false
		}
	
		name := normalize(after[:sepIdx])
		if name == "what" || name == "where" || name == "how" || name == "acceptance_criteria" {
			rest := strings.TrimLeft(after[sepIdx:], ":* ")
			return name, rest, true
		}
	}

	return "", "", false
}

// ParsePlanMarkdown parses a markdown plan document into structured steps.
// Returns parsed steps and any structural errors found.
func ParsePlanMarkdown(md string) (*ParsedPlan, []PlanParseError) {
	var errors []PlanParseError
	md = strings.TrimSpace(md)
	if md == "" {
		errors = append(errors, PlanParseError{Field: "structure", Detail: "plan markdown is empty"})
		return nil, errors
	}

	// Find all step headers
	headerLocs := stepHeaderRe.FindAllStringSubmatchIndex(md, -1)
	if len(headerLocs) == 0 {
		errors = append(errors, PlanParseError{Field: "structure", Detail: "no step headers found (expected '# Step N: <title>')"})
		return nil, errors
	}

	var parsed ParsedPlan
	prevNum := 0
	for idx, loc := range headerLocs {
		// loc[0] = start of full match, loc[1] = end of full match
		// loc[2] = start of group 1 (step number), loc[3] = end
		// loc[4] = start of group 2 (title), loc[5] = end
		stepNum := 0
		fmt.Sscanf(md[loc[2]:loc[3]], "%d", &stepNum)
		title := strings.TrimSpace(md[loc[4]:loc[5]])

		if stepNum != prevNum+1 {
			errors = append(errors, PlanParseError{
				StepNum: stepNum,
				Field:   "structure",
				Detail:  fmt.Sprintf("expected step %d, got step %d — steps must be sequential", prevNum+1, stepNum),
			})
		}
		prevNum = stepNum

		// Extract the block content for this step (from end of header to next header or end)
		blockStart := loc[1]
		blockEnd := len(md)
		if idx+1 < len(headerLocs) {
			blockEnd = headerLocs[idx+1][0]
		}
		block := md[blockStart:blockEnd]

		fields := parseDescriptionFields(block)

		step := ParsedStep{
			Title:              fmt.Sprintf("Step %d: %s", stepNum, title),
			What:               fields["what"],
			Where:              fields["where"],
			How:                fields["how"],
			AcceptanceCriteria: fields["acceptance_criteria"],
		}

		// Validate required fields
		if strings.TrimSpace(step.What) == "" {
			errors = append(errors, PlanParseError{StepNum: stepNum, Field: "what", Detail: "What field is empty"})
		}
		if strings.TrimSpace(step.Where) == "" {
			errors = append(errors, PlanParseError{StepNum: stepNum, Field: "where", Detail: "Where field is empty"})
		}
		if strings.TrimSpace(step.How) == "" {
			errors = append(errors, PlanParseError{StepNum: stepNum, Field: "how", Detail: "How field is empty"})
		}
		if strings.TrimSpace(step.AcceptanceCriteria) == "" {
			errors = append(errors, PlanParseError{StepNum: stepNum, Field: "acceptance_criteria", Detail: "Acceptance Criteria field is empty"})
		}

		parsed.Steps = append(parsed.Steps, step)
	}

	if len(parsed.Steps) == 0 {
		errors = append(errors, PlanParseError{Field: "structure", Detail: "plan has no steps"})
	}

	return &parsed, errors
}

// MergePlanSteps merges user-reviewed markdown content with hidden fields from
// the original plan. Steps are matched by position (first parsed step → first
// original step). Hidden fields (DependsOn, Profile, EstimatedTools, Parallelizable)
// are copied from the original plan.
func MergePlanSteps(parsed *ParsedPlan, original *orchestration.Plan) []orchestration.PlanStep {
	if parsed == nil {
		if original != nil {
			return original.Steps
		}
		return nil
	}

	merged := make([]orchestration.PlanStep, len(parsed.Steps))
	for i, ps := range parsed.Steps {
		// Build Description from the 4 structured fields
		var desc strings.Builder
		desc.WriteString("**What**: " + ps.What + "\n\n")
		desc.WriteString("**Where**: " + ps.Where + "\n\n")
		desc.WriteString("**How**: " + ps.How + "\n\n")
		desc.WriteString("**Acceptance Criteria**: " + ps.AcceptanceCriteria)

		stepID := fmt.Sprintf("step_%d", i+1)

		merged[i] = orchestration.PlanStep{
			ID:          stepID,
			Summary:     extractSummaryFromTitle(ps.Title),
			Description: desc.String(),
		}

		// Copy hidden fields from original if available
		if original != nil && i < len(original.Steps) {
			merged[i].DependsOn = original.Steps[i].DependsOn
			merged[i].Parallelizable = original.Steps[i].Parallelizable
			merged[i].EstimatedTools = original.Steps[i].EstimatedTools
			merged[i].Profile = original.Steps[i].Profile
		}
	}
	return merged
}

// extractSummaryFromTitle strips the "Step N: " prefix from a title.
func extractSummaryFromTitle(title string) string {
	return summaryTitleRe.ReplaceAllString(strings.TrimSpace(title), "")
}
