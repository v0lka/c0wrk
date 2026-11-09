//go:build !dev

package desktop

import (
	"net/http"

	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/v0lka/c0wrk/core/csp"
)

// CSPMiddleware returns the assetserver middleware that stamps the production
// CSP (core/csp) onto every response; see specs/domains/web-access.md.
func CSPMiddleware() assetserver.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", csp.Policy())
			next.ServeHTTP(w, r)
		})
	}
}
