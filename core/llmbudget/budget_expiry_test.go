package llmbudget

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestOwnBudgetExpiryWrapperMessageUnchanged pins the wrapper's message at
// byte-identity with the pre-marker format: the message IS the classification
// seam (ADR-126 — transient-provider predicates couple to its shape, there is
// deliberately no sentinel), so ownBudgetExpiryMarker and the format string
// above must not drift apart.
func TestOwnBudgetExpiryWrapperMessageUnchanged(t *testing.T) {
	budget := 45 * time.Second
	got := fmt.Errorf("adaptive request budget exhausted: %s armed for %q (%s): %w", budget, "gpt", ownBudgetExpiryMarker, context.DeadlineExceeded).Error()
	want := fmt.Errorf("adaptive request budget exhausted: %s armed for %q (own budget expiry, not a provider abort; the next budget escalates): %w", budget, "gpt", context.DeadlineExceeded).Error()
	if got != want {
		t.Fatalf("wrapper message drifted:\n got: %q\nwant: %q", got, want)
	}
}

// TestIsOwnBudgetExpiry pins the exported predicate: the wrapper's signature
// is detected anywhere in the chain; a plain provider error and nil are not.
func TestIsOwnBudgetExpiry(t *testing.T) {
	wrapped := fmt.Errorf("llmbudget: request failed: %w",
		fmt.Errorf("adaptive request budget exhausted: %s armed for %q (%s): %w", 45*time.Second, "m", ownBudgetExpiryMarker, context.DeadlineExceeded))
	if !IsOwnBudgetExpiry(wrapped) {
		t.Fatal("a wrapped own-budget-expiry chain must be detected")
	}
	if IsOwnBudgetExpiry(nil) {
		t.Fatal("nil must not be detected")
	}
	if IsOwnBudgetExpiry(errors.New("connection refused")) {
		t.Fatal("a plain provider error must not be detected")
	}
}
