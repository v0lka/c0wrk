package core

import "github.com/user/agent/sdk/orchestration"

// NewTokenTrackingCaller wraps an LLMCaller to report token usage after every call.
//
// Deprecated: Use orchestration.NewTokenTrackingCaller directly.
var NewTokenTrackingCaller = orchestration.NewTokenTrackingCaller
