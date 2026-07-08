package orchestration

import (
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/strutil"
)

// compile-time check
var _ Blackboard = (*MapBlackboard)(nil)

// MapBlackboard is a thread-safe, map-backed implementation of Blackboard.
type MapBlackboard struct {
	mu            sync.RWMutex
	request       string
	plan          *Plan
	stepResults   map[string]StepResult
	reflections   []Reflection
	finalResult   string
	maxSummaryLen int    // char-based limit for summaries (0 = use default 500)
	facts         []Fact // keyword-tagged facts for inter-step communication
}

// MapBlackboardOption configures a MapBlackboard.
type MapBlackboardOption func(*MapBlackboard)

// WithMaxSummaryLen sets a character-based cap on auto-generated step summaries.
// A value of 0 uses the default (500).
func WithMaxSummaryLen(n int) MapBlackboardOption {
	return func(b *MapBlackboard) {
		b.maxSummaryLen = n
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
func (b *MapBlackboard) SetStepResult(stepID, output string, err error, steps []agent.Step) {
	maxLen := b.maxSummaryLen
	if maxLen == 0 {
		maxLen = 500
	}
	summary := GenerateSummary(output, maxLen)

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

// MaxSummaryLen returns the configured character limit for summaries.
func (b *MapBlackboard) MaxSummaryLen() int {
	return b.maxSummaryLen
}

// SetStepResultRaw stores a pre-built StepResult without regenerating the summary.
// Used by persistence restoration to hydrate the blackboard with stored data.
func (b *MapBlackboard) SetStepResultRaw(stepID string, sr StepResult) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stepResults[stepID] = sr
}

// ---------------------------------------------------------------------------
// Fact memory
// ---------------------------------------------------------------------------

// StoreFact appends a fact to the facts slice.
func (b *MapBlackboard) StoreFact(f Fact) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.facts = append(b.facts, f)
}

// SearchFacts returns facts where at least one keyword matches (case-insensitive),
// sorted by number of matching keywords descending (most relevant first).
// Returns defensive copies of matching facts.
func (b *MapBlackboard) SearchFacts(keywords []string) []Fact {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if len(keywords) == 0 || len(b.facts) == 0 {
		return nil
	}

	// Normalize search keywords to lowercase.
	lowerKeywords := make([]string, len(keywords))
	for i, k := range keywords {
		lowerKeywords[i] = strings.ToLower(k)
	}

	type scored struct {
		fact  Fact
		score int
	}
	var results []scored

	for _, f := range b.facts {
		matchCount := 0
		for _, fk := range f.Keywords {
			fkLower := strings.ToLower(fk)
			for _, qk := range lowerKeywords {
				if fkLower == qk {
					matchCount++
					break
				}
			}
		}
		if matchCount > 0 {
			// Defensive copy of keywords slice.
			kwCopy := make([]string, len(f.Keywords))
			copy(kwCopy, f.Keywords)
			results = append(results, scored{
				fact:  Fact{Keywords: kwCopy, Content: f.Content, Author: f.Author},
				score: matchCount,
			})
		}
	}

	// Sort by match count descending.
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	out := make([]Fact, len(results))
	for i, r := range results {
		out[i] = r.fact
	}
	return out
}

// GetFacts returns a defensive copy of all stored facts.
func (b *MapBlackboard) GetFacts() []Fact {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.facts) == 0 {
		return nil
	}
	out := make([]Fact, len(b.facts))
	for i, f := range b.facts {
		kwCopy := make([]string, len(f.Keywords))
		copy(kwCopy, f.Keywords)
		out[i] = Fact{Keywords: kwCopy, Content: f.Content, Author: f.Author}
	}
	return out
}

// SetFacts replaces the facts slice. Used by persistence restoration.
//
// Defensively deep-copies the input slice (and each Fact's Keywords slice) so
// the caller can mutate its own slice afterwards without racing with reads
// against the blackboard. Mirrors the read-side defensive copy in GetFacts.
func (b *MapBlackboard) SetFacts(facts []Fact) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if facts == nil {
		b.facts = nil
		return
	}
	copied := make([]Fact, len(facts))
	for i, f := range facts {
		var kwCopy []string
		if f.Keywords != nil {
			kwCopy = make([]string, len(f.Keywords))
			copy(kwCopy, f.Keywords)
		}
		copied[i] = Fact{Keywords: kwCopy, Content: f.Content, Author: f.Author}
	}
	b.facts = copied
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
// double-newline) or first maxLen chars, whichever is shorter. Appends "..." if truncated.
// If maxLen is 0, uses default of 500.
func GenerateSummary(output string, maxLen int) string {
	if output == "" {
		return ""
	}

	if maxLen == 0 {
		maxLen = 500
	}

	// Find first double-newline (paragraph break).
	paragraph := output
	if idx := strings.Index(output, "\n\n"); idx >= 0 {
		paragraph = output[:idx]
	}

	// Take whichever is shorter: paragraph or maxLen chars.
	// TruncateUTF8 cuts at a rune boundary so multi-byte characters
	// are never split mid-sequence.
	result := paragraph
	truncated := false
	if len(result) > maxLen {
		result = strutil.TruncateUTF8(result, maxLen)
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
		Steps:              make([]PlanStep, len(p.Steps)),
		ExplorationContext: p.ExplorationContext,
	}
	for i, s := range p.Steps {
		out.Steps[i] = PlanStep{
			ID:             s.ID,
			Summary:        s.Summary,
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

// copyStepResult returns a copy of a StepResult with copied Steps slice.
func copyStepResult(r StepResult) StepResult {
	out := r
	if r.Steps != nil {
		out.Steps = make([]agent.Step, len(r.Steps))
		copy(out.Steps, r.Steps)
	}
	return out
}
