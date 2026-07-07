// Package judge_prompts provides embedded prompt templates for the tool safety judge.
package judge_prompts

import _ "embed"

// JudgeSystem is the embedded system prompt for the tool safety judge.
//
//go:embed judge_system.md
var JudgeSystem string
