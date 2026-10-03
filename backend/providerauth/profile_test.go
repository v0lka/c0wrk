package providerauth

import (
	"maps"
	"slices"
	"testing"
)

// TestChatGPTProfile pins the production ChatGPT constants: issuer endpoints,
// the public Codex CLI client id, scopes, originator, API base URL, and the
// loopback redirect port.
func TestChatGPTProfile(t *testing.T) {
	p := ChatGPT()

	if p.ProviderID != "chatgpt" {
		t.Errorf("ProviderID = %q, want chatgpt", p.ProviderID)
	}
	if p.Issuer != "https://auth.openai.com" {
		t.Errorf("Issuer = %q, want https://auth.openai.com", p.Issuer)
	}
	if p.ClientID != "app_EMoamEEZ73f0CkXaXp7hrann" {
		t.Errorf("ClientID = %q, want the public Codex CLI client id", p.ClientID)
	}
	wantScopes := []string{"openid", "profile", "email", "offline_access"}
	if !slices.Equal(p.Scopes, wantScopes) {
		t.Errorf("Scopes = %v, want %v", p.Scopes, wantScopes)
	}
	if p.Originator != "c0wrk" {
		t.Errorf("Originator = %q, want c0wrk", p.Originator)
	}
	if p.APIBaseURL != "https://chatgpt.com/backend-api/codex" {
		t.Errorf("APIBaseURL = %q, want https://chatgpt.com/backend-api/codex", p.APIBaseURL)
	}
	if p.RedirectPort != 1455 {
		t.Errorf("RedirectPort = %d, want 1455", p.RedirectPort)
	}
	if p.RedirectHost != "localhost" {
		t.Errorf("RedirectHost = %q, want localhost", p.RedirectHost)
	}
	if p.RedirectPath != "/auth/callback" {
		t.Errorf("RedirectPath = %q, want /auth/callback", p.RedirectPath)
	}
	// The authorize extras the Codex CLI client expects.
	wantParams := map[string]string{
		"id_token_add_organizations": "true",
		"codex_cli_simplified_flow":  "true",
		"originator":                 "c0wrk",
	}
	if !maps.Equal(p.AuthorizeParams, wantParams) {
		t.Errorf("AuthorizeParams = %v, want %v", p.AuthorizeParams, wantParams)
	}
}

func TestProfileEndpoints(t *testing.T) {
	p := ChatGPT()
	if got, want := p.AuthorizeURL(), "https://auth.openai.com/oauth/authorize"; got != want {
		t.Errorf("AuthorizeURL = %q, want %q", got, want)
	}
	if got, want := p.TokenURL(), "https://auth.openai.com/oauth/token"; got != want {
		t.Errorf("TokenURL = %q, want %q", got, want)
	}
	if got, want := p.RedirectURI(1455), "http://localhost:1455/auth/callback"; got != want {
		t.Errorf("RedirectURI = %q, want %q", got, want)
	}
	if got, want := p.ScopeString(), "openid profile email offline_access"; got != want {
		t.Errorf("ScopeString = %q, want %q", got, want)
	}
	if got, want := p.secretKey(), "providerauth:chatgpt"; got != want {
		t.Errorf("secretKey = %q, want %q", got, want)
	}
}
