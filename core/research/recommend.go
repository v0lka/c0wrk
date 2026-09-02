package research

// RecommendNextStep derives the single recommended next research action for a
// research project from its current phase — encoded entirely in the project's
// hypothesis-graph metrics and report flag. It is a pure function (no I/O)
// mirroring the lifecycle vocabulary of the research-* skills, and it is safe
// to call on a nil project (a research root with no active R-NNN yet), which
// yields the research-init setup recommendation.

// ActionKind identifies the recommended next research action. Its value is
// also the name of the research-* skill that implements the action, so the
// dashboard can both display the kind and route a "/<skill>" invocation with
// the same token.
type ActionKind string

const (
	// ActionInit initializes the first research project (no active R-NNN yet).
	ActionInit ActionKind = "research-init"

	// ActionHypothesize formulates the first hypothesis (no hypotheses yet).
	ActionHypothesize ActionKind = "research-hypothesis"

	// ActionExperiment runs an experiment against the leading open/in-progress
	// hypothesis on the active front.
	ActionExperiment ActionKind = "research-experiment"

	// ActionDecision reviews terminal results to choose the next direction
	// (continue / pivot / kill / fork) — reached once all hypotheses are
	// decided and the synthesis report already exists.
	ActionDecision ActionKind = "research-decision"

	// ActionSynthesize concludes the project by writing the final report —
	// reached when all hypotheses are decided and no report exists yet.
	ActionSynthesize ActionKind = "research-synthesis"
)

// Recommendation is the single recommended next research action: the action
// kind, the target hypothesis (or "" when the action is not hypothesis-scoped),
// a human-readable rationale, and the research-* skill that implements it.
type Recommendation struct {
	// Action is the recommended action kind (one of the Action* constants).
	Action ActionKind `json:"action"`

	// Target is the hypothesis ID the action operates on, or "" when the
	// action is not scoped to a single hypothesis (init/hypothesize/decide/
	// synthesize).
	Target string `json:"target,omitempty"`

	// Reason is a short human-readable rationale for the recommendation.
	Reason string `json:"reason"`

	// Skill is the research-* skill that implements the action. It equals
	// Action but is kept as a separate field so the dashboard can route the
	// skill without interpreting the action kind.
	Skill string `json:"skill"`
}

// RecommendNextStep derives the recommendation for a project. A nil project
// means "no active research project yet" and recommends research-init.
//
// The phase precedence is:
//
//  1. no project        → research-init
//  2. no hypotheses     → research-hypothesis
//  3. active front      → research-experiment on the front's first
//     open/in-progress hypothesis
//  4. all terminal      → research-synthesis (no report yet) or
//     research-decision (report already written)
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
		// ActiveFront is sorted by ID; the first entry is the front's leading
		// open/in-progress hypothesis.
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
