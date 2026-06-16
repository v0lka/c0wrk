// Package judge_prompts provides embedded prompt templates for the tool safety judge.
package judge_prompts

import _ "embed"

//go:embed judge_system.md
var JudgeSystem string
