package orchestration

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/user/agent/sdk/agent"
)

// maxSummaryLen is the maximum length for auto-generated step summaries.
const maxSummaryLen = 500

// compile-time check
var _ Blackboard = (*MapBlackboard)(nil)

// MapBlackboard is a thread-safe, map-backed implementation of Blackboard.
type MapBlackboard struct {
	mu               sync.RWMutex
	request          string
	plan             *Plan
	stepResults      map[string]StepResult
	reflections      []Reflection
	finalResult      string
	maxSummaryTokens int                     // token-based limit for summaries (0 = use char-based default)
	fileChanges      map[string][]FileChange // keyed by stepID
}

// MapBlackboardOption configures a MapBlackboard.
type MapBlackboardOption func(*MapBlackboard)

// WithMaxSummaryTokens sets a token-based cap on auto-generated step summaries.
// The summary produced by GenerateSummary is further truncated to approximately
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
		fileChanges: make(map[string][]FileChange),
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
func (b *MapBlackboard) SetStepResult(stepID, output string, err error, steps []agent.Step) {
	summary := GenerateSummary(output)

	// Apply token-budget cap as a secondary limit.
	if b.maxSummaryTokens > 0 {
		maxChars := b.maxSummaryTokens * 4
		if len(summary) > maxChars {
			summary = summary[:maxChars] + "..."
		}
	}

	stepsCopy := make([]agent.Step, len(steps))
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

// MaxSummaryTokens returns the configured token budget for summaries.
func (b *MapBlackboard) MaxSummaryTokens() int {
	return b.maxSummaryTokens
}

// ---------------------------------------------------------------------------
// File change tracking
// ---------------------------------------------------------------------------

// SetStepFileChanges stores a defensive copy of file changes for a step.
// If a StepResult already exists for the step, its FileChanges field is also updated.
func (b *MapBlackboard) SetStepFileChanges(stepID string, changes []FileChange) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := make([]FileChange, len(changes))
	copy(c, changes)
	b.fileChanges[stepID] = c
	// Also update the StepResult if it exists.
	if sr, ok := b.stepResults[stepID]; ok {
		sr.FileChanges = c
		b.stepResults[stepID] = sr
	}
}

// GetStepFileChanges returns a defensive copy of file changes for a step.
func (b *MapBlackboard) GetStepFileChanges(stepID string) []FileChange {
	b.mu.RLock()
	defer b.mu.RUnlock()
	changes := b.fileChanges[stepID]
	if changes == nil {
		return nil
	}
	out := make([]FileChange, len(changes))
	copy(out, changes)
	return out
}

// GetAllFileChanges returns a defensive copy of all file changes keyed by step ID.
func (b *MapBlackboard) GetAllFileChanges() map[string][]FileChange {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make(map[string][]FileChange, len(b.fileChanges))
	for stepID, changes := range b.fileChanges {
		c := make([]FileChange, len(changes))
		copy(c, changes)
		result[stepID] = c
	}
	return result
}

// GetSessionFileChanges returns aggregated file changes across all steps.
// For each unique path, it determines the final state by processing steps
// in sorted order. If a file was created and then deleted, it is omitted.
// The result is sorted by path for deterministic output.
func (b *MapBlackboard) GetSessionFileChanges() []FileChange {
	b.mu.RLock()
	defer b.mu.RUnlock()

	type pathState struct {
		firstOp  string
		lastOp   string
		lastDiff string
		lastSize int64
	}

	paths := make(map[string]*pathState)

	// Process steps in deterministic order.
	stepIDs := make([]string, 0, len(b.fileChanges))
	for id := range b.fileChanges {
		stepIDs = append(stepIDs, id)
	}
	sort.Strings(stepIDs)

	for _, stepID := range stepIDs {
		for _, fc := range b.fileChanges[stepID] {
			state, exists := paths[fc.Path]
			if !exists {
				state = &pathState{firstOp: fc.Operation}
				paths[fc.Path] = state
			}
			state.lastOp = fc.Operation
			state.lastDiff = fc.Diff
			state.lastSize = fc.SizeBytes
		}
	}

	var result []FileChange
	for path, state := range paths {
		// Created then deleted → net zero change, omit.
		if state.firstOp == "CREATE" && state.lastOp == "DELETE" {
			continue
		}
		result = append(result, FileChange{
			Path:      path,
			Operation: state.lastOp,
			Diff:      state.lastDiff,
			SizeBytes: state.lastSize,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Path < result[j].Path
	})

	return result
}

// SetStepResultRaw stores a pre-built StepResult without regenerating the summary.
// Used by persistence restoration to hydrate the blackboard with stored data.
func (b *MapBlackboard) SetStepResultRaw(stepID string, sr StepResult) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stepResults[stepID] = sr
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// Search performs a case-insensitive substring match across step summaries,
// step full outputs, and reflection summaries.
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

	// Search file changes
	for stepID, changes := range b.fileChanges {
		for _, fc := range changes {
			if strings.Contains(strings.ToLower(fc.Path), q) {
				entries = append(entries, BlackboardEntry{
					Type:    "file_change",
					Key:     stepID + "/" + fc.Path,
					Summary: fmt.Sprintf("%s: %s", fc.Operation, fc.Path),
				})
			}
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

// GenerateSummary creates a summary from output: first paragraph (up to first
// double-newline) or first 500 chars, whichever is shorter. Appends "..." if truncated.
func GenerateSummary(output string) string {
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
			Profile:        s.Profile, // opaque value; simple assignment
		}
		if s.DependsOn != nil {
			out.Steps[i].DependsOn = make([]string, len(s.DependsOn))
			copy(out.Steps[i].DependsOn, s.DependsOn)
		}
		if s.EstimatedTools != nil {
			out.Steps[i].EstimatedTools = make([]string, len(s.EstimatedTools))
			copy(out.Steps[i].EstimatedTools, s.EstimatedTools)
		}
	}
	return out
}

// copyStepResult returns a copy of a StepResult with copied Steps and FileChanges slices.
func copyStepResult(r StepResult) StepResult {
	out := r
	if r.Steps != nil {
		out.Steps = make([]agent.Step, len(r.Steps))
		copy(out.Steps, r.Steps)
	}
	if r.FileChanges != nil {
		out.FileChanges = make([]FileChange, len(r.FileChanges))
		copy(out.FileChanges, r.FileChanges)
	}
	return out
}
