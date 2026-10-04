package providerauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrMalformedIDToken reports an ID token that is not a well-formed JWT.
var ErrMalformedIDToken = errors.New("providerauth: malformed ID token")

// Identity is the account identity extracted from the provider's ID token.
type Identity struct {
	// Subject is the "sub" claim (the issuer's user id).
	Subject string `json:"subject,omitempty"`
	// Email is the "email" claim, when present.
	Email string `json:"email,omitempty"`
	// ChatGPTAccountID is the chatgpt_account_id claim (namespaced form
	// preferred), identifying the ChatGPT account backing the subscription.
	ChatGPTAccountID string `json:"chatgpt_account_id,omitempty"`
}

// jwtClaims is the subset of standard + OpenAI claims c0wrk consumes.
type jwtClaims struct {
	Subject          string         `json:"sub"`
	Email            string         `json:"email"`
	ChatGPTAccountID string         `json:"chatgpt_account_id"`
	Auth             *authNamespace `json:"https://api.openai.com/auth"`
}

// authNamespace is the OpenAI namespaced claim object.
type authNamespace struct {
	ChatGPTAccountID string `json:"chatgpt_account_id"`
	UserID           string `json:"user_id"`
}

// ParseIDTokenClaims extracts Identity from a JWT ID token WITHOUT verifying
// its signature. This is deliberate: the token is received directly from the
// issuer's token endpoint over TLS during the code exchange or a refresh, so
// its origin is already trusted; nothing here ever accepts a token from a
// less-trusted channel. Callers must not use this function on tokens from
// untrusted input.
func ParseIDTokenClaims(idToken string) (*Identity, error) {
	claims, err := decodeJWTClaims(idToken)
	if err != nil {
		return nil, err
	}
	id := &Identity{
		Subject: claims.Subject,
		Email:   claims.Email,
	}
	// The namespaced claim is the authoritative location; the flat claim is
	// accepted for issuers that inline it.
	id.ChatGPTAccountID = claims.ChatGPTAccountID
	if claims.Auth != nil && claims.Auth.ChatGPTAccountID != "" {
		id.ChatGPTAccountID = claims.Auth.ChatGPTAccountID
	}
	return id, nil
}

// decodeJWTClaims base64-decodes the JWT payload segment and unmarshals it.
// It ignores the header and signature segments entirely.
func decodeJWTClaims(idToken string) (*jwtClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 || parts[1] == "" {
		return nil, fmt.Errorf("%w: expected 3 dot-separated segments", ErrMalformedIDToken)
	}
	payload, err := decodeBase64URL(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: payload segment is not base64url: %w", ErrMalformedIDToken, err)
	}
	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("%w: payload is not a JSON object: %w", ErrMalformedIDToken, err)
	}
	return &claims, nil
}

// decodeBase64URL decodes unpadded or padded base64url input.
func decodeBase64URL(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}
