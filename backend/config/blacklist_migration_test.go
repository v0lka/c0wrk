package config

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
)

// legacyDefaultExecuteGroupBlacklistSHA256 is the SHA-256 of the frozen
// pre-rename execute-group default, taken over the patterns joined by "\n".
//
// It was captured from the RELEASED tree (`git show <pre-ADR-052>:backend/config/defaults.go`
// → DefaultExecuteGroupBlacklist(), the 77-pattern dedup union of the bash
// and POSIX-in-PowerShell sets) and verified byte-identical to the inlined
// copy. Pinning the digest keeps the frozen list from drifting silently: the
// migration's whole job is to recognize a legacy list that is exactly the old
// default, so a transcription slip here would make a default-equal list get
// carried into `blocklist:` — re-activating patterns the user never chose.
// Update this constant ONLY when intentionally re-freezing against a new
// released baseline, never to make a failing comparison pass.
const legacyDefaultExecuteGroupBlacklistSHA256 = "a9ccfb6cf37f9ff6b2a699898841c276697b94c76be35a87ff9aa66016b6d149"

// TestLegacyDefaultExecuteGroupBlacklist_Frozen pins the frozen legacy default
// the load migration compares against. The list is dead history — nothing
// else may consult it — so it must be provably unchanged.
func TestLegacyDefaultExecuteGroupBlacklist_Frozen(t *testing.T) {
	patterns := legacyDefaultExecuteGroupBlacklist()
	if len(patterns) == 0 {
		t.Fatal("legacyDefaultExecuteGroupBlacklist() is empty; the frozen migration fixture was emptied")
	}
	sum := sha256.Sum256([]byte(strings.Join(patterns, "\n")))
	got := hex.EncodeToString(sum[:])
	if got != legacyDefaultExecuteGroupBlacklistSHA256 {
		t.Fatalf("legacy default digest = %s, want %s — the frozen pre-rename default changed; the migration's default-equality check is now comparing against a different history",
			got, legacyDefaultExecuteGroupBlacklistSHA256)
	}
}

// TestSamePatternSet covers the order-insensitive legacy comparison: reordering
// the shipped default must still read as "the default is in effect" (dropped),
// while any added, removed or duplicated pattern is a customization (carried
// over).
func TestSamePatternSet(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{"identical", []string{"a", "b"}, []string{"a", "b"}, true},
		{"reordered", []string{"b", "a"}, []string{"a", "b"}, true},
		{"added", []string{"a", "b", "c"}, []string{"a", "b"}, false},
		{"removed", []string{"a"}, []string{"a", "b"}, false},
		{"duplicated", []string{"a", "a", "b"}, []string{"a", "b"}, false},
		{"both nil", nil, nil, true},
		{"nil vs non-empty", nil, []string{"a"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			aCopy := slices.Clone(tt.a)
			bCopy := slices.Clone(tt.b)
			if got := samePatternSet(tt.a, tt.b); got != tt.want {
				t.Errorf("samePatternSet(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
			// The comparison must not mutate its inputs (it sorts copies).
			if !slices.Equal(tt.a, aCopy) || !slices.Equal(tt.b, bCopy) {
				t.Errorf("samePatternSet mutated its inputs: a=%v (was %v), b=%v (was %v)", tt.a, aCopy, tt.b, bCopy)
			}
		})
	}
}
