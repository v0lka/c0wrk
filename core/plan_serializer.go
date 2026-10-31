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

// SerializePlan renders the plan-approval markdown: per step a "# Step N (id):
// summary" header, its "Depends on:" line, then the description — the
// reviewer-facing edges of ADR-047/069 (specs/decisions/047-plan-step-dependencies.md).
// Only ID, Summary, DependsOn, and Description are rendered; other fields hidden.
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
