//go:build !dev

package desktop

import (
	"net/http"

	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// productionCSP is the Content-Security-Policy applied to every response in
// production builds — the webview-level backstop for theme sanitization
// (specs/domains/theming.md): even a future sanitization bypass cannot turn
// an injected stylesheet into a network request. img-src is deliberately the
// one broad directive: the markdown and paper renderers pass remote image
// URLs through verbatim, and img-src grants no script or connect rights, so
// the exfiltration channel this CSP closes (connect-src 'self') stays closed.
// See specs/domains/web-access.md.
const productionCSP = "default-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: https: http:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"worker-src 'none'; " +
	"frame-src 'none'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'none'; " +
	"frame-ancestors 'none'"

// CSPMiddleware returns the assetserver middleware that stamps productionCSP
// onto every response; see specs/domains/web-access.md.
func CSPMiddleware() assetserver.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", productionCSP)
			next.ServeHTTP(w, r)
		})
	}
}
