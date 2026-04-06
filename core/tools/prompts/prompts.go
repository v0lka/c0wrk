// Package prompts provides embedded prompt templates used by tool subsystems.
package prompts

import _ "embed"

//go:embed judge_system.md
var JudgeSystem string
