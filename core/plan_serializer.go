package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/sdk/orchestration"
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
// Only Summary and Description are included; DependsOn, Profile, etc. are hidden.
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
		b.WriteString(": ")
		b.WriteString(step.Summary)
		b.WriteString("\n\n")
		b.WriteString(step.Description)
		b.WriteString("\n")
	}
	return b.String()
}
