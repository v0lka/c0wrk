package config

import (
	"testing"

	"github.com/v0lka/c0wrk/core/vectorindex"
)

// TestDerefParkBudgetBytes pins the nil→default contract: an unset
// park_budget_mb (nil pointer) must resolve to the documented 1024 MiB default
// rather than 0 (which the service reads as "byte budget disabled"), while a
// non-nil value is scaled MiB→bytes verbatim — preserving the negative -1
// disable sentinel.
func TestDerefParkBudgetBytes(t *testing.T) {
	if got := derefParkBudgetBytes(nil); got != vectorindex.DefaultParkBudgetBytes {
		t.Errorf("derefParkBudgetBytes(nil) = %d, want %d (the 1024 MiB default)",
			got, vectorindex.DefaultParkBudgetBytes)
	}
	if vectorindex.DefaultParkBudgetBytes != 1024<<20 {
		t.Errorf("vectorindex.DefaultParkBudgetBytes = %d, want %d (1024 MiB)",
			vectorindex.DefaultParkBudgetBytes, int64(1024)<<20)
	}

	explicit := int64(2048)
	if got := derefParkBudgetBytes(&explicit); got != 2048<<20 {
		t.Errorf("derefParkBudgetBytes(&2048) = %d, want %d", got, int64(2048)<<20)
	}

	disabled := int64(-1)
	if got := derefParkBudgetBytes(&disabled); got != -1<<20 {
		t.Errorf("derefParkBudgetBytes(&-1) = %d, want %d (the disable sentinel stays negative)",
			got, int64(-1)<<20)
	}
}
