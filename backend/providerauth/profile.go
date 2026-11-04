package providerauth

import (
	"fmt"
	"strings"
)

// Profile describes one subscription provider's OAuth and API endpoints as
// pure data; ChatGPT() is the production profile, tests build throwaway ones.
type Profile struct {
	// ProviderID keys the persisted secret and appears in diagnostics.
	ProviderID string
	// Issuer is the OAuth issuer base URL, without a trailing slash
	// (e.g. "https://auth.openai.com").
	Issuer string
	// ClientID is the public OAuth client identifier (PKCE, no client secret).
	ClientID string
	// Scopes is the OAuth scope list requested at authorization.
	Scopes []string
	// Originator is the value of the "originator" header sent to the issuer's
	// token endpoint, identifying the calling application ("c0wrk").
	Originator string
	// APIBaseURL is the provider's model-API base URL (the path /responses is
	// appended by the transport layer, not here).
	APIBaseURL string
	// RedirectPort is the loopback redirect port — not negotiable: the issuer
	// allow-lists the exact redirect URI, so a busy port fails the sign-in
	// with an actionable error, never a port fallback
	// (specs/domains/llm-providers.md).
	RedirectPort int
	// RedirectHost is the host named in the registered redirect URI
	// ("localhost") — a name browsers may resolve to either loopback family.
	RedirectHost string
	// RedirectPath is the exact redirect path registered for the OAuth client
	// ("/auth/callback" for the Codex CLI client), served by the listener.
	RedirectPath string
	// AuthorizeParams carries provider-specific extra authorize-query
	// parameters (Codex simplified-flow switches, originator, …); the flow
	// always sets the core OAuth parameters itself.
	AuthorizeParams map[string]string
}

// AuthorizeURL returns the issuer's authorization endpoint.
func (p Profile) AuthorizeURL() string {
	return p.Issuer + "/oauth/authorize"
}

// TokenURL returns the issuer's token endpoint (authorization-code exchange
// and refresh both go here).
func (p Profile) TokenURL() string {
	return p.Issuer + "/oauth/token"
}

// RedirectURI returns the loopback redirect URI for the given local port.
func (p Profile) RedirectURI(port int) string {
	return fmt.Sprintf("http://%s:%d%s", p.RedirectHost, port, p.RedirectPath)
}

// ScopeString returns the space-joined scope list for the authorize request.
func (p Profile) ScopeString() string {
	return strings.Join(p.Scopes, " ")
}

// secretKey returns the SecretStore key under which this provider's tokens
// are persisted.
func (p Profile) secretKey() string {
	return "providerauth:" + p.ProviderID
}
