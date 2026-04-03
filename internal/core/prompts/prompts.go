package prompts

import _ "embed"

// Planner prompts (merged templates)

//go:embed planner_plan.md
var PlannerPlan string

//go:embed planner_replan.md
var PlannerReplan string

// Orchestrator prompt

//go:embed orchestrator_system.md
var OrchestratorSystem string

// Reflector prompt

//go:embed reflector_system.md
var ReflectorSystem string

// Router prompt

//go:embed router_system.md
var RouterSystem string

// Raw AC Extractor prompt (Phase 1 — domain-agnostic)

//go:embed raw_ac_extractor_system.md
var RawACExtractorSystem string

// AC Enricher prompt (Phase 2 — domain-specific)

//go:embed ac_enricher_system.md
var ACEnricherSystem string

// Evaluator prompts

//go:embed evaluator_judge.md
var EvaluatorJudge string

//go:embed evaluator_reconsider.md
var EvaluatorReconsider string

// Constitution prompt

//go:embed constitution_meta_reflection.md
var ConstitutionMetaReflection string
