package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/v0lka/sp4rk/orchestration"
)

// RandomSuffix generates a 6-character random hex suffix for plan filenames.
func RandomSuffix() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())[:6]
	}
	return hex.EncodeToString(b)
}

// SerializePlan converts a Plan to markdown for user review.
// Each step renders as:
//
//	# Step N (id): summary
//
//	Depends on: step_1, step_2
//
//	<description>
//
// The step header carries the step ID (`# Step N (id): summary`) so the human
// reviewer sees the identifiers that DependsOn references target and empty or
// malformed IDs stand out. Immediately below the header, the "Depends on" line
// prints that step's dependencies (comma-separated), or exactly
// "Depends on: (none)" when it has none — this makes the flattened plan's edges
// explicit even when the DAG is rendered as a linear list. Only ID, Summary,
// DependsOn, and Description are included; other fields (Profile, Agent, etc.)
// are hidden.
func SerializePlan(plan *orchestration.Plan) string {
	if plan == nil || len(plan.Steps) == 0 {
		return ""
	}
	var b strings.Builder
	for i, step := range plan.Steps {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("# Step ")
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(" (")
		b.WriteString(step.ID)
		b.WriteString("): ")
		b.WriteString(step.Summary)
		b.WriteString("\n\n")
		b.WriteString("Depends on: ")
		deps := make([]string, 0, len(step.DependsOn))
		for _, d := range step.DependsOn {
			if trimmed := strings.TrimSpace(d); trimmed != "" {
				deps = append(deps, trimmed)
			}
		}
		if len(deps) == 0 {
			b.WriteString("(none)")
		} else {
			b.WriteString(strings.Join(deps, ", "))
		}
		b.WriteString("\n\n")
		b.WriteString(step.Description)
		b.WriteString("\n")
	}
	return b.String()
}
