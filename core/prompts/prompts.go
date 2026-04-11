// Package prompts provides embedded prompt templates used by LLM agents.
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

// Compaction summarize prompt

//go:embed compaction_summarize.md
var CompactionSummarize string
