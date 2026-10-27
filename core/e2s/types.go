// Package e2s implements the E2S (explicit-state) execution mode: the
// externalized state Σ, its fixed core schema, the per-step action contract,
// the merge operator Σₜ⊕ΔΣₜ (ApplyPatch, merge.go), and the driver loop
// (loop.go). The domain layer (types.go, merge.go) is pure data and logic;
// the driver files (loop.go, prompt.go, steptool.go) import sp4rk by design.
// See specs/domains/e2s.md.
package e2s

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"time"
)

// StateStatus is the lifecycle status of an E2S run; the source of truth is
// the core Σ "status" key — E2SState.Status is a derived copy (ApplyPatch).
type StateStatus string

// The closed set of legal lifecycle statuses.
const (
	StateStatusActive    StateStatus = "active"    // run is executing turns
	StateStatusPaused    StateStatus = "paused"    // run is suspended (resumable)
	StateStatusMet       StateStatus = "met"       // objective met, run finished
	StateStatusFailed    StateStatus = "failed"    // run failed (budget, spin, error)
	StateStatusCancelled StateStatus = "cancelled" // run cancelled by the user/runtime
)

// validStatuses is the closed membership set behind ValidStateStatus.
var validStatuses = map[StateStatus]struct{}{
	StateStatusActive:    {},
	StateStatusPaused:    {},
	StateStatusMet:       {},
	StateStatusFailed:    {},
	StateStatusCancelled: {},
}

// ValidStateStatus reports whether s belongs to the closed status set.
func ValidStateStatus(s StateStatus) bool {
	_, ok := validStatuses[s]
	return ok
}

// StateStatuses returns the legal status values in a stable, documented order.
func StateStatuses() []StateStatus {
	return []StateStatus{StateStatusActive, StateStatusPaused, StateStatusMet, StateStatusFailed, StateStatusCancelled}
}

// The fixed core Σ keys. The set and each key's JSON type are frozen:
// update-only, same-type, never deleted or re-typed; every other key is a
// mutable extension (see StatePatch). See specs/domains/e2s.md.
const (
	CoreKeyObjective    = "objective"     // string — the task objective
	CoreKeyChecklist    = "checklist"     // array — mutable working checklist
	CoreKeyFilesTouched = "files_touched" // array — paths read or written
	CoreKeyFindings     = "findings"      // array — discovered facts
	CoreKeyDecisions    = "decisions"     // array — decision log
	CoreKeyNextSteps    = "next_steps"    // array — planned next actions
	CoreKeyDoneCriteria = "done_criteria" // array — completion criteria
	CoreKeyStatus       = "status"        // Status string — lifecycle status
)

// NotesFileName is the E2S scratchpad file name inside the session temp
// directory — the overflow channel for raw data beyond the byte-capped Σ
// (distilled facts + pointers in Σ, evidence in the file); withDefaults
// derives <TempDir>/NotesFileName and the model uses the ordinary gates.
// See specs/domains/e2s.md.
const NotesFileName = "e2s-notes.md"

// coreTypeKind is the internal JSON-type marker used by coreTypes.
type coreTypeKind string

const (
	coreTypeString coreTypeKind = "string"
	coreTypeArray  coreTypeKind = "array"
	coreTypeStatus coreTypeKind = "status"
)

// coreTypes maps every core key to its required JSON type; the model emits
// JSON, so accepted Go shapes are the post-encoding/json shapes (string,
// []any).
var coreTypes = map[string]coreTypeKind{
	CoreKeyObjective:    coreTypeString,
	CoreKeyChecklist:    coreTypeArray,
	CoreKeyFilesTouched: coreTypeArray,
	CoreKeyFindings:     coreTypeArray,
	CoreKeyDecisions:    coreTypeArray,
	CoreKeyNextSteps:    coreTypeArray,
	CoreKeyDoneCriteria: coreTypeArray,
	CoreKeyStatus:       coreTypeStatus,
}

// CoreKeys returns the fixed core-key set in sorted order.
func CoreKeys() []string {
	return slices.Sorted(maps.Keys(coreTypes))
}

// IsCoreKey reports whether key belongs to the fixed core schema.
func IsCoreKey(key string) bool {
	_, ok := coreTypes[key]
	return ok
}

// SchemaFingerprint returns a stable content hash of the core schema (sorted
// key→type pairs), stamped on every E2SState: a mismatch means the state
// predates a schema change and must not be silently patched (ApplyPatch
// rejects it).
func SchemaFingerprint() string {
	h := sha256.New()
	for _, k := range CoreKeys() {
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(coreTypes[k]))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// E2SState is the externalized state Σ of an E2S run plus its bookkeeping
// metadata; round-trips through encoding/json for persistence.
// See specs/domains/e2s.md.
type E2SState struct {
	// Sigma is the state map Σ: core keys per the fixed schema above, every
	// other key a mutable extension (null tombstone deletes).
	Sigma map[string]any `json:"sigma"`
	// Schema is the SchemaFingerprint stamped at creation; empty skips the
	// check (hand-built states), a mismatch is rejected by ApplyPatch.
	Schema string `json:"schema"`
	// TurnCount is the number of applied patches (one per turn).
	TurnCount int `json:"turn_count"`
	// Status is the typed copy of the core "status" key, re-synced by
	// ApplyPatch.
	Status StateStatus `json:"status"`
	// CreatedAt timestamps run creation; UpdatedAt the last applied patch.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewE2SState builds the canonical initial state for a run: objective set,
// status active, list-valued core keys as empty arrays, schema fingerprint
// stamped. Zero now means time.Now().UTC().
func NewE2SState(objective string, now time.Time) E2SState {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return E2SState{
		Sigma: map[string]any{
			CoreKeyObjective:    objective,
			CoreKeyChecklist:    []any{},
			CoreKeyFilesTouched: []any{},
			CoreKeyFindings:     []any{},
			CoreKeyDecisions:    []any{},
			CoreKeyNextSteps:    []any{},
			CoreKeyDoneCriteria: []any{},
			CoreKeyStatus:       string(StateStatusActive),
		},
		Schema:    SchemaFingerprint(),
		TurnCount: 0,
		Status:    StateStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// StepAction contract violations (the struct lives in steptool.go; the
// domain contract is enforced by StepAction.Validate).
var (
	// ErrActionEmpty: the action names no tool (neither a target tool nor
	// "finish").
	ErrActionEmpty = errors.New("e2s: step action names no tool")
	// ErrActionNoAnswer: a finish action without an answer.
	ErrActionNoAnswer = errors.New("e2s: finish action requires an answer")
)

// Validate enforces the exactly-one-of action contract: a tool call or a
// finish with a non-empty answer — never an empty action.
func (a StepAction) Validate() error {
	if a.Tool == "" {
		return ErrActionEmpty
	}
	if a.IsFinish() && a.Answer == "" {
		return ErrActionNoAnswer
	}
	return nil
}

// StatePatch is the per-turn state delta ΔΣₜ. ApplyPatch folds it into Σ:
// null deletes (tombstone), core keys update-only with the key's fixed JSON
// type, extensions add/update in place. Values must be JSON-shaped —
// []any / map[string]any. See specs/domains/e2s.md.
type StatePatch map[string]any
