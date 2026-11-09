// Package csp holds the production Content-Security-Policy applied to the
// webview's asset server — the webview-level backstop for theme sanitization
// (specs/domains/theming.md): even a future sanitization bypass cannot turn
// an injected stylesheet into a network request. The Wails assetserver
// middleware (desktop CSPMiddleware) stamps the policy onto every response;
// this package keeps the policy itself Wails-free so it stays testable and
// single-sourced. See specs/domains/web-access.md.
package csp

// productionCSP is the policy applied to every response in production
// builds. img-src is deliberately the one broad directive: the markdown and
// paper renderers pass remote image URLs through verbatim, and img-src grants
// no script or connect rights, so the exfiltration channel this CSP closes
// (connect-src 'self') stays closed.
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

// Policy returns the production Content-Security-Policy value for the
// asset-server middleware's Content-Security-Policy header.
func Policy() string {
	return productionCSP
}
