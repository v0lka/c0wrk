package providerauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxTokenErrorBody bounds how much of a token-endpoint error body is read
// for diagnostics (error bodies never contain issued secrets, but stay
// bounded regardless).
const maxTokenErrorBody = 4 << 10

// maxTokenResponseBody bounds successful token responses.
const maxTokenResponseBody = 64 << 10

// ErrReauthRequired reports that the stored refresh token was rejected and
// the user must sign in again in the browser.
var ErrReauthRequired = errors.New("providerauth: re-authorization required (refresh token rejected)")

// TokenEndpointError is a structured OAuth error response from the issuer's
// token endpoint.
type TokenEndpointError struct {
	// Code is the OAuth error code ("invalid_grant", "invalid_request", …).
	Code string
	// Description is the human-readable error_description, when present.
	Description string
	// HTTPStatus is the response status code.
	HTTPStatus int
}

// Error implements error. It never includes token material — only the OAuth
// error code and description the endpoint chose to expose.
func (e *TokenEndpointError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("providerauth: token endpoint error %q: %s", e.Code, e.Description)
	}
	return fmt.Sprintf("providerauth: token endpoint error %q", e.Code)
}

// Tokens is the persisted OAuth token set for one provider.
type Tokens struct {
	// AccessToken is the bearer credential for API calls.
	AccessToken string `json:"access_token"`
	// RefreshToken renews the access token; empty when the provider issued none.
	RefreshToken string `json:"refresh_token,omitempty"`
	// IDToken is the most recent OIDC ID token (claims source).
	IDToken string `json:"id_token,omitempty"`
	// TokenType is the reported token type (typically "Bearer").
	TokenType string `json:"token_type,omitempty"`
	// Expiry is when AccessToken stops being valid (zero when unknown).
	Expiry time.Time `json:"expiry"`
}

// tokenResponse is the wire shape of a successful token-endpoint reply.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// tokens converts a wire response into Tokens, stamping the absolute expiry.
func (r *tokenResponse) tokens(now time.Time) *Tokens {
	t := &Tokens{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		IDToken:      r.IDToken,
		TokenType:    r.TokenType,
	}
	if r.ExpiresIn > 0 {
		t.Expiry = now.Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	return t
}

// exchange trades an authorization code (plus PKCE verifier) for tokens.
func (f *Flow) exchange(ctx context.Context, code, verifier, redirectURI string) (*tokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {f.profile.ClientID},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	return postTokenForm(ctx, f.client, f.profile, form)
}

// refreshTokens redeems a refresh token for a fresh token set at the
// profile's token endpoint. When the endpoint omits a new refresh token the
// caller must keep the previous one.
func refreshTokens(ctx context.Context, client *http.Client, profile Profile, refreshToken string) (*tokenResponse, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {profile.ClientID},
		"refresh_token": {refreshToken},
	}
	return postTokenForm(ctx, client, profile, form)
}

// postTokenForm submits a token-endpoint form and decodes the reply. All
// requests carry the originator header; failures return either a
// *TokenEndpointError (structured OAuth error) or a transport error, and
// never echo submitted form values (which include secrets).
func postTokenForm(ctx context.Context, client *http.Client, profile Profile, form url.Values) (*tokenResponse, error) {
	if client == nil {
		client = defaultHTTPClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, profile.TokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("providerauth: building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if profile.Originator != "" {
		req.Header.Set("originator", profile.Originator)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("providerauth: token request to %s failed: %w", profile.TokenURL(), err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, decodeTokenEndpointError(resp)
	}

	var out tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxTokenResponseBody)).Decode(&out); err != nil {
		return nil, fmt.Errorf("providerauth: decoding token response: %w", err)
	}
	if out.AccessToken == "" {
		return nil, errors.New("providerauth: token response carried no access_token")
	}
	return &out, nil
}

// decodeTokenEndpointError converts a non-2xx token response into a
// *TokenEndpointError, tolerating non-JSON bodies.
func decodeTokenEndpointError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxTokenErrorBody))
	tErr := &TokenEndpointError{HTTPStatus: resp.StatusCode}
	var wire struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if json.Unmarshal(body, &wire) == nil && wire.Error != "" {
		tErr.Code = wire.Error
		tErr.Description = wire.ErrorDescription
	} else {
		tErr.Code = fmt.Sprintf("http_%d", resp.StatusCode)
	}
	return tErr
}
