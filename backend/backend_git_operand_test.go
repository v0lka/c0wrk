package backend

import (
	"strings"
	"testing"
)

// TestValidateGitOperand pins the leading-dash rejection every branch/ref/
// remote/tag RPC applies to its git operands (review [26]/[33]/[53]/[54]/
// [105]): option-like names (creatable via git update-ref or a crafted
// packed-refs, and listed verbatim by GetBranches) must be refused at the
// boundary instead of being parsed as git options.
func TestValidateGitOperand(t *testing.T) {
	cases := []struct {
		name    string
		operand string
		wantErr bool
	}{
		{"plain branch", "main", false},
		{"slash form", "origin/feature-x", false},
		{"short option", "-f", true},
		{"long option", "--mirror", true},
		{"double dash alone", "--", true},
		{"dash in the middle is fine", "feature/-x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGitOperand("branch name", tc.operand)
			if tc.wantErr && err == nil {
				t.Fatalf("validateGitOperand(%q) = nil, want an error", tc.operand)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateGitOperand(%q) = %v, want nil", tc.operand, err)
			}
			if err != nil && !strings.Contains(err.Error(), "must not start with") {
				t.Errorf("error %v should name the leading-dash rule", err)
			}
		})
	}
}
