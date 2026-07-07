package planner

// PromptSet holds all parameterizable prompt templates for the Planner.
// The host application injects its own prompts via this struct.
type PromptSet struct {
	// Base templates
	BasePrompt     string
	InformedPrompt string
	ReplanPrompt   string

	// Mode sections (multi-step, single-step, continuation variants)
	PlanPreamble                   string
	SingleStepPreamble             string
	MultiStepToT                   string
	SingleStepToT                  string
	MultiStepGuidance              string
	SingleStepGuidance             string
	ContinuationPreamble           string
	ContinuationIncompletePreamble string
	ContinuationSingleStep         string

	// Shared sections
	DomainAssignment string
	AgentProfiles    string
	ExtraSections    string

	// FamilyPrompt returns the prompt adapter for the given agent role and model family.
	// Returns "" when no family-specific adaptation exists.
	FamilyPrompt func(agent, family string) string

	// VerificationMandate is appended to all planner prompts.
	VerificationMandate string
}
