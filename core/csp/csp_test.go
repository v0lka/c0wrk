package csp

import (
	"strings"
	"testing"
)

// TestPolicy_Directives guards the production policy contract: the directives
// that close the theme-CSS exfiltration channel must stay present.
func TestPolicy_Directives(t *testing.T) {
	policy := Policy()
	if policy == "" {
		t.Fatal("expected a non-empty production CSP")
	}
	for _, want := range []string{
		"default-src 'none'",
		"img-src 'self' data: https: http:",
		"connect-src 'self'",
		"script-src 'self'",
	} {
		if !containsDirective(policy, want) {
			t.Errorf("CSP missing %q: %s", want, policy)
		}
	}
}

// containsDirective checks that a full directive (including its source list)
// appears in the policy.
func containsDirective(policy, directive string) bool {
	for _, part := range strings.Split(policy, ";") {
		if strings.TrimSpace(part) == directive {
			return true
		}
	}
	return false
}
