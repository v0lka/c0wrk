package core

import "github.com/user/agent/sdk/orchestration"

// Re-export blackboard types from sdk/orchestration.
type StepResult = orchestration.StepResult
type BlackboardEntry = orchestration.BlackboardEntry
type Blackboard = orchestration.Blackboard
type MapBlackboard = orchestration.MapBlackboard
type MapBlackboardOption = orchestration.MapBlackboardOption

var (
	NewMapBlackboard     = orchestration.NewMapBlackboard
	WithMaxSummaryTokens = orchestration.WithMaxSummaryTokens
	WithMaxSummaryLen    = orchestration.WithMaxSummaryLen
)
