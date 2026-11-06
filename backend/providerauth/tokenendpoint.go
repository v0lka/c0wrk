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

// maxTokenErrorBody bounds the token-endpoint error body read for
// diagnostics (error bodies carry no issued secrets, but stay bounded).
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
	// ExpiresIn is the issuer's reported access-token lifetime in seconds.
	// Memory-only — used to size the device-flow refresh threshold.
	ExpiresIn int `json:"-"`
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
// When the access token is a JWT, its exp claim is preferred if earlier than
// expires_in (Kimi access tokens are short-lived ES256 JWTs; see
// specs/domains/llm-providers.md).
func (r *tokenResponse) tokens(now time.Time) *Tokens {
	t := &Tokens{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		IDToken:      r.IDToken,
		TokenType:    r.TokenType,
		ExpiresIn:    r.ExpiresIn,
	}
	if r.ExpiresIn > 0 {
		t.Expiry = now.Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	if exp, ok := jwtExpiryUnix(r.AccessToken); ok {
		jwtExpiry := time.Unix(exp, 0)
		if t.Expiry.IsZero() || jwtExpiry.Before(t.Expiry) {
			t.Expiry = jwtExpiry
			if remaining := int(jwtExpiry.Sub(now).Seconds()); remaining > 0 {
				t.ExpiresIn = remaining
			}
		}
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

// maxRefreshAttempts bounds transient refresh retries (429 / 5xx), matching
// the official Kimi Code CLI. Non-retryable OAuth errors fail immediately.
const maxRefreshAttempts = 3

// refreshTokens redeems a refresh token for a fresh token set. When the
// endpoint omits a new refresh token the caller must keep the previous one.
// Transient HTTP failures (429, 500, 502, 503, 504) are retried with
// exponential backoff; invalid_grant / invalid_client are not.
func refreshTokens(ctx context.Context, client *http.Client, profile Profile, refreshToken string) (*tokenResponse, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {profile.ClientID},
		"refresh_token": {refreshToken},
	}
	var lastErr error
	for attempt := 0; attempt < maxRefreshAttempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt-1)) * time.Second
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		resp, err := postTokenForm(ctx, client, profile, form)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		var tErr *TokenEndpointError
		if errors.As(err, &tErr) && !retryableTokenHTTPStatus(tErr.HTTPStatus) {
			return nil, err
		}
	}
	return nil, lastErr
}

func retryableTokenHTTPStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// postForm submits one OAuth form without logging its secret values.
func postForm(ctx context.Context, client *http.Client, profile Profile, form url.Values) (*http.Response, error) {
	return postFormURL(ctx, client, profile, form, profile.TokenURL())
}

func postFormURL(ctx context.Context, client *http.Client, profile Profile, form url.Values, endpoint string) (*http.Response, error) {
	if client == nil {
		client = defaultHTTPClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("providerauth: building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if profile.Originator != "" {
		req.Header.Set("originator", profile.Originator)
	}
	for name, value := range profile.TokenHeaders {
		if value != "" {
			req.Header.Set(name, value)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("providerauth: token request to %s failed: %w", endpoint, err)
	}
	return resp, nil
}

// postTokenForm submits a token-endpoint form and decodes the reply.
// Failures return a *TokenEndpointError or a transport error and never echo
// the submitted form values (they include secrets).
func postTokenForm(ctx context.Context, client *http.Client, profile Profile, form url.Values) (*tokenResponse, error) {
	resp, err := postForm(ctx, client, profile, form)
	if err != nil {
		return nil, err
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
