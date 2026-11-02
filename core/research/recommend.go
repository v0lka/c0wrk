package research

import (
	"strings"
)

// ActionKind identifies the recommended next action; its value is also the
// name of the research-* skill implementing it, so the dashboard displays and
// routes "/<skill>" with the same token.
type ActionKind string

const (
	// ActionInit: initialize the first R-NNN project.
	ActionInit ActionKind = "research-init"

	// ActionHypothesize: formulate the first hypothesis.
	ActionHypothesize ActionKind = "research-hypothesis"

	// ActionExperiment: experiment on the leading front hypothesis.
	ActionExperiment ActionKind = "research-experiment"

	// ActionDecision: next direction once all decided and the report exists.
	ActionDecision ActionKind = "research-decision"

	// ActionSynthesize: final report once all decided and none written.
	ActionSynthesize ActionKind = "research-synthesis"
)

// Recommendation is the single recommended next action: kind, target
// hypothesis ("" when not hypothesis-scoped), rationale, implementing skill.
type Recommendation struct {
	Action ActionKind `json:"action"`

	Target string `json:"target,omitempty"`

	Reason string `json:"reason"`

	// Skill equals Action (kept separate so callers can route the skill
	// without interpreting the kind).
	Skill string `json:"skill"`
}

// RecommendNextStep derives the project-wide recommendation (pure; nil-safe).
// Phase precedence:
//
//  1. no project    → research-init
//  2. no hypotheses → research-hypothesis
//  3. active front  → research-experiment on the front's first open/in-progress
//  4. all terminal  → research-synthesis (no report) / research-decision (reported)
func RecommendNextStep(project *ResearchProject) Recommendation {
	if project == nil {
		return Recommendation{
			Action: ActionInit,
			Reason: "no research project exists yet — initialize the first R-NNN",
			Skill:  string(ActionInit),
		}
	}

	m := project.Metrics
	if m.Total == 0 {
		return Recommendation{
			Action: ActionHypothesize,
			Reason: "the project brief exists but no hypotheses have been formulated yet",
			Skill:  string(ActionHypothesize),
		}
	}

	if len(m.ActiveFront) > 0 {
		// Sorted by ID; [0] is the front's leading hypothesis.
		target := m.ActiveFront[0]
		return Recommendation{
			Action: ActionExperiment,
			Target: target,
			Reason: "an active front exists — run an experiment against the leading open/in-progress hypothesis " + target,
			Skill:  string(ActionExperiment),
		}
	}

	// All hypotheses are terminal.
	if !project.HasReport {
		return Recommendation{
			Action: ActionSynthesize,
			Reason: "all hypotheses are decided and no report exists yet — synthesize the final report",
			Skill:  string(ActionSynthesize),
		}
	}
	return Recommendation{
		Action: ActionDecision,
		Reason: "all hypotheses are decided and the report exists — decide the next direction (continue/pivot/kill/fork)",
		Skill:  string(ActionDecision),
	}
}

// RecommendNextStepForHypothesis scopes the recommendation to one hypothesis
// (the dashboard's selected card): open/in-progress → research-experiment on
// it; terminal without a recorded Decision → research-decision on it;
// anything else (nil project, unknown ID, decision already recorded) falls
// back to the project-wide result. Contract: specs/domains/research.md.
func RecommendNextStepForHypothesis(project *ResearchProject, hypothesisID string) Recommendation {
	if project == nil {
		return RecommendNextStep(nil)
	}
	id := NormalizeID(hypothesisID)
	if id == "" {
		return RecommendNextStep(project)
	}
	node := project.Graph.Node(id)
	if node == nil {
		return RecommendNextStep(project)
	}
	switch {
	case node.Status.IsActive():
		return Recommendation{
			Action: ActionExperiment,
			Target: node.ID,
			Reason: "hypothesis " + node.ID + " is " + string(node.Status) + " — run an experiment against it",
			Skill:  string(ActionExperiment),
		}
	case node.Status.IsTerminal() && strings.TrimSpace(node.Decision) == "":
		return Recommendation{
			Action: ActionDecision,
			Target: node.ID,
			Reason: "hypothesis " + node.ID + " is " + string(node.Status) + " with no recorded decision — review its results and decide the next direction (continue/pivot/kill/fork)",
			Skill:  string(ActionDecision),
		}
	default:
		return RecommendNextStep(project)
	}
}
