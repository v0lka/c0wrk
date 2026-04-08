package core

import (
	"strconv"
	"strings"
	"sync"
)

// maxSummaryLen is the maximum length for auto-generated step summaries.
const maxSummaryLen = 500

// ---------------------------------------------------------------------------
// Blackboard types
// ---------------------------------------------------------------------------

// StepResult holds both a summary and the full output of a completed step.
type StepResult struct {
	StepID     string
	Summary    string // auto-generated summary (first 500 chars or first paragraph, whichever shorter)
	FullOutput string
	Error      error
	Steps      []Step // executor ReAct steps (tool calls + observations)
}

// BlackboardEntry represents a search result from the blackboard.
type BlackboardEntry struct {
	Type    string // "step_result", "criterion", "reflection", etc.
	Key     string // identifier
	Summary string // brief content
}

// ---------------------------------------------------------------------------
// Blackboard interface
// ---------------------------------------------------------------------------

// Blackboard provides structured access to shared task state.
// All methods are safe for concurrent use.
type Blackboard interface {
	// Read methods
	GetOriginalRequest() string
	GetCriteria() []AcceptanceCriterion
	GetPlan() *Plan
	GetStepResult(stepID string) (StepResult, bool)
	GetStepSummary(stepID string) string
	GetStepsByAC(criterionID string) []StepResult
	GetAllStepResults() map[string]StepResult
	GetReflections() []Reflection
	GetFinalResult() string

	// Write methods
	SetOriginalRequest(req string)
	SetCriteria(criteria []AcceptanceCriterion)
	SetPlan(plan *Plan)
	SetStepResult(stepID string, output string, err error, steps []Step)
	AddReflection(r Reflection)
	SetFinalResult(result string)

	// Search (keyword match for now)
	Search(query string) []BlackboardEntry
}

// ---------------------------------------------------------------------------
// MapBlackboard — in-memory Blackboard implementation
// ---------------------------------------------------------------------------

// compile-time check
var _ Blackboard = (*MapBlackboard)(nil)

// MapBlackboard is a thread-safe, map-backed implementation of Blackboard.
type MapBlackboard struct {
	mu               sync.RWMutex
	request          string
	criteria         []AcceptanceCriterion
	plan             *Plan
	stepResults      map[string]StepResult
	reflections      []Reflection
	finalResult      string
	maxSummaryTokens int // token-based limit for summaries (0 = use char-based default)
}

// MapBlackboardOption configures a MapBlackboard.
type MapBlackboardOption func(*MapBlackboard)

// WithMaxSummaryTokens sets a token-based cap on auto-generated step summaries.
// The summary produced by generateSummary is further truncated to approximately
// n*4 characters (1 token ≈ 4 chars). A value of 0 disables the token budget.
func WithMaxSummaryTokens(n int) MapBlackboardOption {
	return func(b *MapBlackboard) {
		b.maxSummaryTokens = n
	}
}

// NewMapBlackboard creates a new empty MapBlackboard.
func NewMapBlackboard(opts ...MapBlackboardOption) *MapBlackboard {
	b := &MapBlackboard{
		stepResults: make(map[string]StepResult),
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// ---------------------------------------------------------------------------
// Read methods
// ---------------------------------------------------------------------------

// GetOriginalRequest returns the original user request.
func (b *MapBlackboard) GetOriginalRequest() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.request
}

// GetCriteria returns a defensive copy of the acceptance criteria.
func (b *MapBlackboard) GetCriteria() []AcceptanceCriterion {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.criteria == nil {
		return nil
	}
	out := make([]AcceptanceCriterion, len(b.criteria))
	copy(out, b.criteria)
	return out
}

// GetPlan returns a deep copy of the plan, or nil if not set.
func (b *MapBlackboard) GetPlan() *Plan {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.plan == nil {
		return nil
	}
	return copyPlan(b.plan)
}

// GetStepResult returns a copy of the StepResult for the given step ID.
func (b *MapBlackboard) GetStepResult(stepID string) (StepResult, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	r, ok := b.stepResults[stepID]
	if !ok {
		return StepResult{}, false
	}
	return copyStepResult(r), true
}

// GetStepSummary returns the summary for a step, or empty string if not found.
func (b *MapBlackboard) GetStepSummary(stepID string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	r, ok := b.stepResults[stepID]
	if !ok {
		return ""
	}
	return r.Summary
}

// GetStepsByAC returns step results related to the given criterion ID,
// as determined by the plan's RelevantAC mapping.
func (b *MapBlackboard) GetStepsByAC(criterionID string) []StepResult {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.plan == nil {
		return nil
	}
	var results []StepResult
	for _, ps := range b.plan.Steps {
		for _, ac := range ps.RelevantAC {
			if ac == criterionID {
				if r, ok := b.stepResults[ps.ID]; ok {
					results = append(results, copyStepResult(r))
				}
				break
			}
		}
	}
	return results
}

// GetAllStepResults returns a defensive copy of all step results.
func (b *MapBlackboard) GetAllStepResults() map[string]StepResult {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[string]StepResult, len(b.stepResults))
	for k, v := range b.stepResults {
		out[k] = copyStepResult(v)
	}
	return out
}

// GetReflections returns a defensive copy of all reflections, in order.
func (b *MapBlackboard) GetReflections() []Reflection {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.reflections == nil {
		return nil
	}
	out := make([]Reflection, len(b.reflections))
	copy(out, b.reflections)
	return out
}

// GetFinalResult returns the final result string.
func (b *MapBlackboard) GetFinalResult() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.finalResult
}

// ---------------------------------------------------------------------------
// Write methods
// ---------------------------------------------------------------------------

// SetOriginalRequest sets the original user request.
func (b *MapBlackboard) SetOriginalRequest(req string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.request = req
}

// SetCriteria stores a defensive copy of the criteria.
func (b *MapBlackboard) SetCriteria(criteria []AcceptanceCriterion) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if criteria == nil {
		b.criteria = nil
		return
	}
	b.criteria = make([]AcceptanceCriterion, len(criteria))
	copy(b.criteria, criteria)
}

// SetPlan stores a deep copy of the plan.
func (b *MapBlackboard) SetPlan(plan *Plan) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if plan == nil {
		b.plan = nil
		return
	}
	b.plan = copyPlan(plan)
}

// SetStepResult records the result of a completed step, auto-generating a summary.
// If maxSummaryTokens is configured, the summary is additionally capped to
// approximately maxSummaryTokens*4 characters.
func (b *MapBlackboard) SetStepResult(stepID, output string, err error, steps []Step) {
	summary := generateSummary(output)

	// Apply token-budget cap as a secondary limit.
	if b.maxSummaryTokens > 0 {
		maxChars := b.maxSummaryTokens * 4
		if len(summary) > maxChars {
			summary = summary[:maxChars] + "..."
		}
	}

	stepsCopy := make([]Step, len(steps))
	copy(stepsCopy, steps)

	b.mu.Lock()
	defer b.mu.Unlock()
	b.stepResults[stepID] = StepResult{
		StepID:     stepID,
		Summary:    summary,
		FullOutput: output,
		Error:      err,
		Steps:      stepsCopy,
	}
}

// GetStepResultBudgeted returns a copy of the step result with FullOutput
// truncated to approximately maxOutputTokens tokens (maxOutputTokens * 4 chars).
// If maxOutputTokens is 0, the full output is returned unmodified.
func (b *MapBlackboard) GetStepResultBudgeted(stepID string, maxOutputTokens int) (StepResult, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	r, ok := b.stepResults[stepID]
	if !ok {
		return StepResult{}, false
	}
	out := copyStepResult(r)
	if maxOutputTokens > 0 {
		maxChars := maxOutputTokens * 4
		if len(out.FullOutput) > maxChars {
			out.FullOutput = out.FullOutput[:maxChars] + "..."
		}
	}
	return out, true
}

// AddReflection appends a reflection to the list.
func (b *MapBlackboard) AddReflection(r Reflection) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reflections = append(b.reflections, r)
}

// SetFinalResult sets the final result string.
func (b *MapBlackboard) SetFinalResult(result string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.finalResult = result
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// Search performs a case-insensitive substring match across step summaries,
// step full outputs, criterion descriptions, and reflection summaries.
func (b *MapBlackboard) Search(query string) []BlackboardEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()

	q := strings.ToLower(query)
	var entries []BlackboardEntry

	// Search step results
	for id, sr := range b.stepResults {
		if strings.Contains(strings.ToLower(sr.Summary), q) ||
			strings.Contains(strings.ToLower(sr.FullOutput), q) {
			entries = append(entries, BlackboardEntry{
				Type:    "step_result",
				Key:     id,
				Summary: sr.Summary,
			})
		}
	}

	// Search criteria
	for _, c := range b.criteria {
		if strings.Contains(strings.ToLower(c.Description), q) {
			entries = append(entries, BlackboardEntry{
				Type:    "criterion",
				Key:     c.ID,
				Summary: c.Description,
			})
		}
	}

	// Search reflections
	for i, r := range b.reflections {
		if strings.Contains(strings.ToLower(r.Summary), q) {
			entries = append(entries, BlackboardEntry{
				Type:    "reflection",
				Key:     "reflection_" + strconv.Itoa(i),
				Summary: r.Summary,
			})
		}
	}

	return entries
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// generateSummary creates a summary from output: first paragraph (up to first
// double-newline) or first 500 chars, whichever is shorter. Appends "..." if truncated.
func generateSummary(output string) string {
	if output == "" {
		return ""
	}

	// Find first double-newline (paragraph break).
	paragraph := output
	if idx := strings.Index(output, "\n\n"); idx >= 0 {
		paragraph = output[:idx]
	}

	// Take whichever is shorter: paragraph or maxSummaryLen chars.
	result := paragraph
	truncated := false
	if len(result) > maxSummaryLen {
		result = result[:maxSummaryLen]
		truncated = true
	}

	// Mark as truncated if we used a shorter version than original.
	if truncated {
		result += "..."
	}

	return result
}

// copyPlan returns a deep copy of a Plan.
func copyPlan(p *Plan) *Plan {
	out := &Plan{
		Steps: make([]PlanStep, len(p.Steps)),
	}
	for i, s := range p.Steps {
		out.Steps[i] = PlanStep{
			ID:             s.ID,
			Description:    s.Description,
			Parallelizable: s.Parallelizable,
		}
		if s.DependsOn != nil {
			out.Steps[i].DependsOn = make([]string, len(s.DependsOn))
			copy(out.Steps[i].DependsOn, s.DependsOn)
		}
		if s.EstimatedTools != nil {
			out.Steps[i].EstimatedTools = make([]string, len(s.EstimatedTools))
			copy(out.Steps[i].EstimatedTools, s.EstimatedTools)
		}
		if s.RelevantAC != nil {
			out.Steps[i].RelevantAC = make([]string, len(s.RelevantAC))
			copy(out.Steps[i].RelevantAC, s.RelevantAC)
		}
		if s.AgentProfile != nil {
			profile := *s.AgentProfile
			if profile.AllowedTools != nil {
				profile.AllowedTools = make([]string, len(s.AgentProfile.AllowedTools))
				copy(profile.AllowedTools, s.AgentProfile.AllowedTools)
			}
			out.Steps[i].AgentProfile = &profile
		}
	}
	return out
}

// copyStepResult returns a copy of a StepResult with copied Steps slice.
func copyStepResult(r StepResult) StepResult {
	out := r
	if r.Steps != nil {
		out.Steps = make([]Step, len(r.Steps))
		copy(out.Steps, r.Steps)
	}
	return out
}
