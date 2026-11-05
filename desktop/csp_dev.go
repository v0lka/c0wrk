//go:build dev

package desktop

import (
	"net/http"

	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// CSPMiddleware is a no-op under the `dev` build tag: the dev asset server proxies
// the Vite/HMR pipeline the production CSP (csp.go) would break (specs/domains/theming.md).
func CSPMiddleware() assetserver.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
		})
	}
}
