package providerauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// makeJWT assembles an unsigned JWT with the given claims. The signature
// segment is opaque — parsing deliberately ignores it.
func makeJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	sig := base64.RawURLEncoding.EncodeToString([]byte("opaque-signature"))
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + sig
}

func TestParseIDTokenClaims_NamespacedAccountID(t *testing.T) {
	tok := makeJWT(map[string]any{
		"sub":   "user-abc",
		"email": "dev@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-789",
			"user_id":            "user-abc",
		},
	})
	id, err := ParseIDTokenClaims(tok)
	if err != nil {
		t.Fatalf("ParseIDTokenClaims: %v", err)
	}
	if id.Subject != "user-abc" {
		t.Errorf("Subject = %q, want user-abc", id.Subject)
	}
	if id.Email != "dev@example.com" {
		t.Errorf("Email = %q, want dev@example.com", id.Email)
	}
	if id.ChatGPTAccountID != "acct-789" {
		t.Errorf("ChatGPTAccountID = %q, want acct-789 (namespaced claim)", id.ChatGPTAccountID)
	}
}

func TestParseIDTokenClaims_FlatAccountIDFallback(t *testing.T) {
	tok := makeJWT(map[string]any{
		"sub":                "user-flat",
		"chatgpt_account_id": "acct-flat",
	})
	id, err := ParseIDTokenClaims(tok)
	if err != nil {
		t.Fatalf("ParseIDTokenClaims: %v", err)
	}
	if id.ChatGPTAccountID != "acct-flat" {
		t.Errorf("ChatGPTAccountID = %q, want acct-flat (flat claim fallback)", id.ChatGPTAccountID)
	}
	if id.Email != "" {
		t.Errorf("Email = %q, want empty", id.Email)
	}
}

// Namespaced and flat claims both present: the namespaced one wins.
func TestParseIDTokenClaims_NamespacedWins(t *testing.T) {
	tok := makeJWT(map[string]any{
		"chatgpt_account_id": "flat-loses",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "ns-wins",
		},
	})
	id, err := ParseIDTokenClaims(tok)
	if err != nil {
		t.Fatalf("ParseIDTokenClaims: %v", err)
	}
	if id.ChatGPTAccountID != "ns-wins" {
		t.Errorf("ChatGPTAccountID = %q, want ns-wins", id.ChatGPTAccountID)
	}
}

// Padded base64url payloads (some issuers emit them) must decode too.
func TestParseIDTokenClaims_PaddedBase64(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"email": "padded@example.com"})
	padded := base64.URLEncoding.EncodeToString(payload) // with padding
	tok := "h." + padded + ".s"
	id, err := ParseIDTokenClaims(tok)
	if err != nil {
		t.Fatalf("ParseIDTokenClaims: %v", err)
	}
	if id.Email != "padded@example.com" {
		t.Errorf("Email = %q, want padded@example.com", id.Email)
	}
}

func TestParseIDTokenClaims_Malformed(t *testing.T) {
	cases := map[string]string{
		"not a jwt":     "nosegments",
		"two segments":  "a.b",
		"empty payload": "a..c",
		"bad base64":    "a.!!!.c",
		"payload not json": func() string {
			return "h." + base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".s"
		}(),
	}
	for name, tok := range cases {
		_, err := ParseIDTokenClaims(tok)
		if err == nil {
			t.Errorf("%s: expected error, got nil", name)
			continue
		}
		if !errors.Is(err, ErrMalformedIDToken) {
			t.Errorf("%s: err = %v, want ErrMalformedIDToken", name, err)
		}
		if strings.Contains(err.Error(), "opaque") {
			t.Errorf("%s: error should not leak token material", name)
		}
	}
}
