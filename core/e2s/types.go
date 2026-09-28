// Package e2s implements the E2S (explicit-state) execution mode: the
// externalized state Σ ("sigma"), its fixed core schema, the per-step action
// contract, and the merge operator Σₜ⊕ΔΣₜ (ApplyPatch, see merge.go) that
// folds a model-emitted state patch into the state under strict validation —
// plus the driver loop (loop.go) that runs the protocol on raw sp4rk
// primitives.
//
// The domain layer in this file and merge.go is deliberately free of LLM,
// tool, and transport dependencies: everything there is pure data and pure
// logic, so it can be unit-tested, persisted (encoding/json round-trips),
// and reused by any driver loop. The loop/driver files (loop.go, prompt.go,
// steptool.go) do import sp4rk's llm/tools/agent packages by design.
package e2s

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"time"
)

// Status is the lifecycle status of an E2S run. The single source of truth is
// the core Σ key "status"; the E2SState.Status field is a typed, derived copy
// kept in sync by ApplyPatch.
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

// The fixed core Σ keys — the schema every E2S state is built around. The key
// set and the JSON type of each key are frozen: a patch may update a core key
// only with a value of the same JSON type, and may never delete or re-type
// it. Keys outside this set are extensions (mutable; see StatePatch).
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

// NotesFileName is the file name of the E2S scratchpad inside the session
// temp directory: the overflow channel for raw data that does not fit the
// byte-capped Σ (long tool output, dumps, excerpts). Σ stores the distilled
// facts plus pointers into the file; the file stores the evidence. The full
// path is derived by Config.withDefaults (<TempDir>/NotesFileName) and
// rendered into the Workspace section of the system prompt with its usage
// rules; no dedicated tool exists — the model reaches it through the
// ordinary write_file/read_file security gates.
const NotesFileName = "e2s-notes.md"

// coreTypeKind is the internal JSON-type marker used by coreTypes.
type coreTypeKind string

const (
	coreTypeString coreTypeKind = "string"
	coreTypeArray  coreTypeKind = "array"
	coreTypeStatus coreTypeKind = "status"
)

// coreTypes maps every core key to its required JSON type. The reference
// decoding is JSON (the model emits JSON), so accepted Go shapes are the
// post-encoding/json shapes: string for strings, []any for arrays.
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
// key→type pairs). It is stamped on every E2SState so persisted states can be
// checked against the schema they were created with: a fingerprint mismatch
// means the state predates a schema change and must not be silently patched
// (ApplyPatch rejects it, see ErrSchemaMismatch).
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
// metadata. Sigma is the state the model reads and patches each turn; the
// remaining fields are runtime bookkeeping. The type round-trips through
// encoding/json for persistence.
type E2SState struct {
	// Sigma is the state map Σ. Core keys follow the fixed schema above;
	// every other key is an extension (mutable, null tombstone deletes).
	Sigma map[string]any `json:"sigma"`
	// Schema is the SchemaFingerprint stamped at creation. An empty value
	// skips the fingerprint check (hand-built states); ApplyPatch rejects a
	// non-empty mismatch.
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

// NewE2SState builds the canonical initial state for a run: the objective is
// set, the status is active, every list-valued core key exists as an empty
// array, and the schema fingerprint is stamped. now is recorded as both
// CreatedAt and UpdatedAt; a zero now means time.Now().UTC().
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

// StepAction is the validated action half of an e2s_step call: a tool call
// (tool+args) or a finish (finish+answer). The struct itself is declared in
// steptool.go alongside ParseStepCall (the runtime envelope that produces
// it); the domain contract it must satisfy is enforced by Validate below.
//
// StepAction contract violations.
var (
	// ErrActionEmpty: the action names no tool (neither a target tool nor
	// "finish").
	ErrActionEmpty = errors.New("e2s: step action names no tool")
	// ErrActionNoAnswer: a finish action without an answer to deliver.
	ErrActionNoAnswer = errors.New("e2s: finish action requires an answer")
)

// Validate enforces the exactly-one-of action contract: a step is either a
// tool call (a non-empty tool name other than "finish") or a finish (tool ==
// FinishActionName with a non-empty answer) — never an empty action.
func (a StepAction) Validate() error {
	if a.Tool == "" {
		return ErrActionEmpty
	}
	if a.IsFinish() && a.Answer == "" {
		return ErrActionNoAnswer
	}
	return nil
}

// StatePatch is the state delta ΔΣₜ emitted by the model for one turn. The
// merge operator Σₜ⊕ΔΣₜ (ApplyPatch) folds it into Σ with these semantics:
//
//   - a nil (JSON null) value deletes the key from Σ (no-op for absent keys);
//   - a core-key value must match the key's fixed JSON type (updates only —
//     deletion and re-typing of core keys are rejected);
//   - a non-core key is added or updated in place (extensions are mutable;
//     deletion via null remains the universal tombstone for extension keys).
//
// Patches are JSON-shaped: values must be encodable, and array/object values
// must use []any / map[string]any (the shapes encoding/json produces).
type StatePatch map[string]any
