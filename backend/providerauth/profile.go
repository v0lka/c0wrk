package providerauth

import (
	"fmt"
	"strings"
)

// Profile describes one subscription provider's OAuth and API endpoints.
// Profiles are pure data; ChatGPT() returns the production ChatGPT profile
// and tests build throwaway profiles against httptest servers.
type Profile struct {
	// ProviderID is the stable provider identifier ("chatgpt"); it keys the
	// persisted secret and appears in diagnostics.
	ProviderID string
	// Issuer is the OAuth issuer base URL, without a trailing slash
	// (e.g. "https://auth.openai.com").
	Issuer string
	// ClientID is the public OAuth client identifier; subscription clients
	// use PKCE instead of a client secret.
	ClientID string
	// Scopes is the OAuth scope list requested at authorization.
	Scopes []string
	// Originator is the value of the "originator" header sent to the issuer's
	// token endpoint, identifying the calling application ("c0wrk").
	Originator string
	// APIBaseURL is the provider's model-API base URL (the path /responses is
	// appended by the transport layer, not here).
	APIBaseURL string
	// RedirectPort is the loopback listener port for the browser redirect.
	// The issuer validates redirect_uri against the OAuth client's
	// allow-list exactly, so the port is not negotiable: when it is busy the
	// sign-in fails with an actionable error instead of silently moving to
	// an unregistered port.
	RedirectPort int
	// RedirectHost is the host named in the redirect URI ("localhost"). It
	// is the host the client registered, not necessarily a numeric loopback
	// address: browsers may resolve the name to either loopback family, so
	// the listener binds every family it can.
	RedirectHost string
	// RedirectPath is the exact redirect path registered for the OAuth
	// client ("/auth/callback" for the Codex CLI client) and the path the
	// loopback listener serves.
	RedirectPath string
	// AuthorizeParams carries provider-specific extra query parameters for
	// the authorize request (e.g. the Codex simplified-flow switches and the
	// originator). Core OAuth parameters (response_type, client_id, …) are
	// always set by the flow itself.
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
// The host and path come from the profile because the issuer matches
// redirect_uri against the client's allow-list byte-for-byte.
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
