// Package prompts provides embedded prompt templates used by LLM agents.
package prompts

import _ "embed"

// Planner prompts

//go:embed planner_base.md
var PlannerBase string

//go:embed planner_replan.md
var PlannerReplan string

//go:embed planner_large.md
var PlannerLarge string

//go:embed planner_small.md
var PlannerSmall string

//go:embed planner_informed.md
var PlannerInformed string

// Orchestrator prompt

//go:embed orchestrator_system.md
var OrchestratorSystem string

//go:embed orchestrator_plan_context.md
var OrchestratorPlanContext string

//go:embed orchestrator_large.md
var OrchestratorLarge string

//go:embed orchestrator_small.md
var OrchestratorSmall string

// Reflector prompt

//go:embed reflector_system.md
var ReflectorSystem string

//go:embed reflector_large.md
var ReflectorLarge string

//go:embed reflector_small.md
var ReflectorSmall string

// Router prompt

//go:embed router_system.md
var RouterSystem string

//go:embed router_large.md
var RouterLarge string

//go:embed router_small.md
var RouterSmall string

// Compaction summarize prompt

//go:embed compaction_summarize.md
var CompactionSummarize string
