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

// AC Extractor prompt

//go:embed ac_extractor_system.md
var ACExtractorSystem string

// Evaluator prompt

//go:embed evaluator_judge.md
var EvaluatorJudge string

// Constitution prompt

//go:embed constitution_meta_reflection.md
var ConstitutionMetaReflection string
