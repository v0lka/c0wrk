package providerauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestRefreshTokensDoesNotReplayCredentialsOnRedirect(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{name: "temporary", status: http.StatusTemporaryRedirect},
		{name: "permanent", status: http.StatusPermanentRedirect},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var redirectedCalls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				redirectedCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"at-redirected","token_type":"Bearer","expires_in":60}`))
			}))
			t.Cleanup(target.Close)
			issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL, tt.status)
			}))
			t.Cleanup(issuer.Close)
			profile := Kimi()
			profile.Issuer = issuer.URL

			_, err := refreshTokens(context.Background(), issuer.Client(), profile, "secret")
			var tokenErr *TokenEndpointError
			if !errors.As(err, &tokenErr) || tokenErr.HTTPStatus != tt.status {
				t.Fatalf("refreshTokens error = %v, want token endpoint HTTP %d", err, tt.status)
			}
			if got := redirectedCalls.Load(); got != 0 {
				t.Fatalf("redirect target received %d requests, want 0", got)
			}
		})
	}
}
