package e2s

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"
)

// Merge and validation errors. Every ApplyPatch failure wraps one of these
// sentinels, so callers can classify policy violations with errors.Is.
var (
	// ErrDeleteCoreKey: the patch tried to delete a fixed core key.
	ErrDeleteCoreKey = errors.New("e2s: core key cannot be deleted")
	// ErrCoreKeyType: the patch set a core key to a value of the wrong JSON
	// type (re-typing is forbidden).
	ErrCoreKeyType = errors.New("e2s: core key type mismatch")
	// ErrCoreKeyElement: a core array key held a wrong-shaped element
	// (checklist: {text, checked} objects; other list keys: strings).
	ErrCoreKeyElement = errors.New("e2s: core key element mismatch")
	// ErrInvalidStatus: the core status key was set to a value outside the
	// closed status set.
	ErrInvalidStatus = errors.New("e2s: invalid status value")
	// ErrStateTooLarge: the merged Σ exceeds the configured byte limit.
	ErrStateTooLarge = errors.New("e2s: state exceeds byte limit")
	// ErrSchemaMismatch: the state carries a schema fingerprint from a
	// different (older or newer) core schema than the compiled-in one.
	ErrSchemaMismatch = errors.New("e2s: state schema fingerprint mismatch")
	// ErrEmptyKey: the patch contains an empty ("") key.
	ErrEmptyKey = errors.New("e2s: empty state key")
)

// DefaultStateByteLimit is the default cap on the JSON-encoded size of Σ, in
// bytes. backend/config seeds e2s.state_byte_limit with this value so the
// domain default and the config default cannot drift apart.
const DefaultStateByteLimit = 16 * 1024

// MergeOptions parameterizes ApplyPatch.
type MergeOptions struct {
	// StateByteLimit caps the JSON-encoded size of Σ in bytes; zero disables
	// the check (offline tooling only — the runtime always sets it from
	// e2s.state_byte_limit).
	StateByteLimit int
	// Now timestamps the merge (E2SState.UpdatedAt); zero = time.Now().UTC().
	Now time.Time
}

// ApplyPatch computes Σₜ₊₁ = Σₜ ⊕ ΔΣₜ under full validation; the receiver
// is never mutated (rollback by construction). Success bumps TurnCount, sets
// UpdatedAt from opts, re-syncs Status; a stale schema fingerprint is
// rejected (ErrSchemaMismatch). See specs/domains/e2s.md.
func ApplyPatch(state E2SState, patch StatePatch, opts MergeOptions) (E2SState, error) {
	if state.Schema != "" && state.Schema != SchemaFingerprint() {
		return E2SState{}, fmt.Errorf("%w: state was created under a different core schema", ErrSchemaMismatch)
	}

	// Shallow copy suffices: patch semantics replace whole values, nested
	// structures are never mutated in place.
	merged := make(map[string]any, len(state.Sigma)+len(patch))
	maps.Copy(merged, state.Sigma)

	// Deterministic key order keeps error messages stable regardless of map
	// iteration order.
	for _, key := range slices.Sorted(maps.Keys(patch)) {
		value := patch[key]
		if key == "" {
			return E2SState{}, fmt.Errorf("%w: patch keys must not be empty", ErrEmptyKey)
		}
		if value == nil {
			// Tombstone: null deletes the key.
			if IsCoreKey(key) {
				return E2SState{}, fmt.Errorf("%w: %q is a fixed core key", ErrDeleteCoreKey, key)
			}
			delete(merged, key) // absent keys are a no-op
			continue
		}
		if IsCoreKey(key) {
			if err := validateCoreValue(key, value); err != nil {
				return E2SState{}, err
			}
			merged[key] = value
			continue
		}
		// Extension keys are mutable: a write replaces the previous value in
		// place (ADR-040 — the add-only policy forced tombstone+re-add churn
		// for every mutable fact, starving the model's usable memory).
		// Overwrite protection stays where it is typed: core-key typing, the
		// byte cap, and the null tombstone.
		merged[key] = value
	}

	if opts.StateByteLimit > 0 {
		if size := SigmaBytes(merged); size > opts.StateByteLimit {
			return E2SState{}, fmt.Errorf("%w: merged Σ is %d bytes, limit is %d",
				ErrStateTooLarge, size, opts.StateByteLimit)
		}
	}

	next := E2SState{
		Sigma:     merged,
		Schema:    state.Schema,
		TurnCount: state.TurnCount + 1,
		Status:    state.Status,
		CreatedAt: state.CreatedAt,
		UpdatedAt: opts.Now,
	}
	if next.UpdatedAt.IsZero() {
		next.UpdatedAt = time.Now().UTC()
	}
	// Re-sync the typed status copy from the (validated) core key —
	// validateCoreValue guarantees a string whenever it exists.
	if s, ok := merged[CoreKeyStatus].(string); ok {
		next.Status = StateStatus(s)
	}
	return next, nil
}

// ApplyRawPatch is the JSON-level entry point for patches produced by the
// E2S loop (StepCall.StatePatch): each key's raw JSON is decoded into its
// post-encoding/json shape and merged via ApplyPatch — a raw `null` decodes
// to nil any, the deletion tombstone. Invalid JSON in any value fails the
// whole patch atomically.
func ApplyRawPatch(state E2SState, patch map[string]json.RawMessage, opts MergeOptions) (E2SState, error) {
	if len(patch) == 0 {
		return ApplyPatch(state, nil, opts)
	}
	decoded := make(StatePatch, len(patch))
	for k, raw := range patch {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return E2SState{}, fmt.Errorf("e2s: patch key %q is not valid JSON: %w", k, err)
		}
		decoded[k] = value
	}
	return ApplyPatch(state, decoded, opts)
}

// validateCoreValue checks a patch value against the fixed JSON type of its
// core key (post-encoding/json shapes: string, []any). Update-only — never
// re-typed.
func validateCoreValue(key string, value any) error {
	switch coreTypes[key] {
	case coreTypeString:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%w: %q requires a string, got %s", ErrCoreKeyType, key, jsonTypeName(value))
		}
	case coreTypeStatus:
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("%w: %q requires a status string, got %s", ErrCoreKeyType, key, jsonTypeName(value))
		}
		if !ValidStateStatus(StateStatus(s)) {
			return fmt.Errorf("%w: %q is not one of: %s", ErrInvalidStatus, s, strings.Join(statusNames(), ", "))
		}
	case coreTypeArray:
		arr, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%w: %q requires an array, got %s", ErrCoreKeyType, key, jsonTypeName(value))
		}
		// Element shapes are part of the documented core schema — checklist
		// holds {text, checked} objects, other list keys strings — enforced
		// here so a wrong-shaped element is rejected at merge, not persisted
		// and dropped by the UI's Σ guard (specs/domains/e2s.md).
		for i, el := range arr {
			if err := validateCoreArrayElement(key, i, el); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateCoreArrayElement enforces the element shape of a core array key.
func validateCoreArrayElement(key string, idx int, el any) error {
	if key == CoreKeyChecklist {
		obj, ok := el.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: %q[%d] must be an object with text and checked, got %s",
				ErrCoreKeyElement, key, idx, jsonTypeName(el))
		}
		if _, ok := obj["text"].(string); !ok {
			return fmt.Errorf("%w: %q[%d].text must be a string", ErrCoreKeyElement, key, idx)
		}
		if _, ok := obj["checked"].(bool); !ok {
			return fmt.Errorf("%w: %q[%d].checked must be a boolean", ErrCoreKeyElement, key, idx)
		}
		return nil
	}
	if _, ok := el.(string); !ok {
		return fmt.Errorf("%w: %q[%d] must be a string, got %s", ErrCoreKeyElement, key, idx, jsonTypeName(el))
	}
	return nil
}

// statusNames renders the closed status set for error messages.
func statusNames() []string {
	names := make([]string, 0, len(validStatuses))
	for _, s := range StateStatuses() {
		names = append(names, string(s))
	}
	return names
}

// jsonTypeName reports the JSON type name of a decoded value for error
// messages: null, string, number, boolean, array, or object.
func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case float64, int, int64, json.Number:
		return "number"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// SigmaBytes returns the JSON-encoded size of Σ in bytes — the measure the
// StateByteLimit caps. A Σ that cannot be marshaled is reported as
// math.MaxInt so the limit check always rejects it (only reachable for values
// that did not come from JSON decoding).
func SigmaBytes(sigma map[string]any) int {
	data, err := json.Marshal(sigma)
	if err != nil {
		return math.MaxInt
	}
	return len(data)
}
