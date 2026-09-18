package desktop

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCSPMiddleware_StampsHeader guards the production contract: every
// response carries the strict Content-Security-Policy that blocks any fetch
// an injected stylesheet might attempt. The test compiles the !dev variant
// (the package's default build) — csp_dev.go is excluded here.
func TestCSPMiddleware_StampsHeader(t *testing.T) {
	handler := CSPMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	handler.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("expected Content-Security-Policy header on the response")
	}
	// The directives that close the theme-CSS exfiltration channel.
	for _, want := range []string{
		"default-src 'none'",
		"img-src 'self' data: https: http:",
		"connect-src 'self'",
		"script-src 'self'",
	} {
		if !containsDirective(csp, want) {
			t.Errorf("CSP missing %q: %s", want, csp)
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
