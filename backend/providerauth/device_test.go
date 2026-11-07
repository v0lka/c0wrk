package providerauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type deviceTokenReply struct {
	status int
	body   string
}

type deviceCallCount struct {
	start int
	token int
}

func newDeviceIssuer(t *testing.T, startBody string, replies []deviceTokenReply, intervals *[]int) (*httptest.Server, *deviceCallCount) {
	t.Helper()
	calls := &deviceCallCount{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Header.Get("X-Msh-Test") != "present" && r.Header.Get("X-Msh-Device-Id") == "" {
			t.Error("device headers are missing")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/oauth/device_authorization":
			calls.start++
			if r.Form.Get("client_id") != "17e5f671-d194-4dfb-9706-5516cb48c098" {
				t.Errorf("client_id = %q", r.Form.Get("client_id"))
			}
			_, _ = w.Write([]byte(startBody))
		case "/api/oauth/token":
			calls.token++
			if got := r.Form.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Errorf("grant_type = %q", got)
			}
			if r.Form.Get("device_code") != "device-1" {
				t.Errorf("device_code = %q", r.Form.Get("device_code"))
			}
			index := calls.token - 1
			if index >= len(replies) {
				t.Fatalf("unexpected extra token poll %d", calls.token)
			}
			reply := replies[index]
			w.WriteHeader(reply.status)
			_, _ = w.Write([]byte(reply.body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

func deviceTestProfile(issuer string) Profile {
	profile := Kimi()
	profile.Issuer = issuer
	profile.TokenHeaders = map[string]string{"X-Msh-Test": "present"}
	return profile
}

func TestKimiDeviceFlowSignIn(t *testing.T) {
	idToken := makeJWT(map[string]any{"sub": "user-1", "email": "dev@example.com"})
	srv, calls := newDeviceIssuer(t,
		`{"user_code":"user-1","device_code":"device-1","verification_uri":"https://kimi.test/authorize","verification_uri_complete":"https://kimi.test/authorize?user_code=user-1","expires_in":600,"interval":5}`,
		[]deviceTokenReply{
			{status: http.StatusBadRequest, body: `{"error":"authorization_pending"}`},
			{status: http.StatusOK, body: `{"access_token":"at-1","refresh_token":"rt-1","id_token":"` + idToken + `","token_type":"Bearer","expires_in":3600}`},
		}, nil)
	var browserURL string
	flow := NewFlow(deviceTestProfile(srv.URL), srv.Client(), func(authURL string) error {
		browserURL = authURL
		return nil
	})
	flow.sleep = func(context.Context, time.Duration) error { return nil }

	result, err := flow.SignIn(context.Background())
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if browserURL != "https://kimi.test/authorize?user_code=user-1" {
		t.Errorf("browser URL = %q", browserURL)
	}
	if calls.start != 1 || calls.token != 2 {
		t.Fatalf("start calls = %d, token calls = %d, want 1 and 2", calls.start, calls.token)
	}
	if result.Tokens.AccessToken != "at-1" || result.Tokens.RefreshToken != "rt-1" {
		t.Errorf("tokens = %+v", result.Tokens)
	}
	if result.Identity == nil || result.Identity.Email != "dev@example.com" {
		t.Errorf("identity = %+v", result.Identity)
	}
}

func TestKimiDeviceFlowErrors(t *testing.T) {
	start := `{"user_code":"user-1","device_code":"device-1","verification_uri_complete":"https://kimi.test/authorize?user_code=user-1","expires_in":600,"interval":5}`
	t.Run("slow down increases the next interval", func(t *testing.T) {
		var intervals []int
		srv, _ := newDeviceIssuer(t, start, []deviceTokenReply{
			{status: http.StatusBadRequest, body: `{"error":"slow_down"}`},
			{status: http.StatusOK, body: `{"access_token":"at-1","refresh_token":"rt-1","token_type":"Bearer","expires_in":3600}`},
		}, &intervals)
		flow := NewFlow(deviceTestProfile(srv.URL), srv.Client(), func(string) error { return nil })
		flow.sleep = func(_ context.Context, d time.Duration) error {
			intervals = append(intervals, int(d/time.Second))
			return nil
		}
		if _, err := flow.SignIn(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(intervals) != 1 || intervals[0] != 10 {
			t.Fatalf("poll intervals = %v, want [10]", intervals)
		}
	})

	t.Run("expired device", func(t *testing.T) {
		srv, _ := newDeviceIssuer(t, start, []deviceTokenReply{{status: http.StatusBadRequest, body: `{"error":"expired_token"}`}}, nil)
		flow := NewFlow(deviceTestProfile(srv.URL), srv.Client(), func(string) error { return nil })
		flow.sleep = func(context.Context, time.Duration) error { return nil }
		if _, err := flow.SignIn(context.Background()); !errors.Is(err, ErrDeviceExpired) {
			t.Fatalf("error = %v, want ErrDeviceExpired", err)
		}
	})

	t.Run("denied", func(t *testing.T) {
		srv, _ := newDeviceIssuer(t, start, []deviceTokenReply{{status: http.StatusBadRequest, body: `{"error":"access_denied"}`}}, nil)
		flow := NewFlow(deviceTestProfile(srv.URL), srv.Client(), func(string) error { return nil })
		flow.sleep = func(context.Context, time.Duration) error { return nil }
		if _, err := flow.SignIn(context.Background()); !errors.Is(err, ErrAccessDenied) {
			t.Fatalf("error = %v, want ErrAccessDenied", err)
		}
	})

	t.Run("malformed start", func(t *testing.T) {
		srv, _ := newDeviceIssuer(t, `{}`, nil, nil)
		flow := NewFlow(deviceTestProfile(srv.URL), srv.Client(), func(string) error { return nil })
		if _, err := flow.SignIn(context.Background()); err == nil || !strings.Contains(err.Error(), "verification_uri_complete") {
			t.Fatalf("error = %v, want missing verification_uri_complete", err)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		srv, _ := newDeviceIssuer(t, start, []deviceTokenReply{{status: http.StatusBadRequest, body: `{"error":"authorization_pending"}`}}, nil)
		flow := NewFlow(deviceTestProfile(srv.URL), srv.Client(), func(string) error { return nil })
		flow.sleep = func(context.Context, time.Duration) error { return context.Canceled }
		if _, err := flow.SignIn(context.Background()); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	})
}

func TestTokenManagerDeviceFlowSignInPending(t *testing.T) {
	srv, _ := newDeviceIssuer(t,
		`{"user_code":"user-1","device_code":"device-1","verification_uri_complete":"https://kimi.test/authorize?user_code=user-1","expires_in":600,"interval":5}`,
		[]deviceTokenReply{
			{status: http.StatusBadRequest, body: `{"error":"authorization_pending"}`},
			{status: http.StatusOK, body: `{"access_token":"at-1","refresh_token":"rt-1","token_type":"Bearer","expires_in":3600}`},
		}, nil)
	var logBuf bytes.Buffer
	manager, err := NewTokenManager(ManagerConfig{
		Profile: deviceTestProfile(srv.URL), Store: NewMemorySecretStore(), HTTPClient: srv.Client(), AgentDir: t.TempDir(),
		Logger: slog.New(slog.NewJSONHandler(&logBuf, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.sleep = func(context.Context, time.Duration) error { return nil }
	result, err := manager.SignIn(context.Background(), func(string) error { return nil })
	if err != nil {
		t.Fatalf("TokenManager.SignIn: %v", err)
	}
	if result.Tokens.AccessToken != "at-1" || !manager.Status().SignedIn {
		t.Fatalf("sign-in result = %+v, status = %+v", result, manager.Status())
	}
	logLines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	if len(logLines) != 1 {
		t.Fatalf("sign-in emitted %d diagnostics, want one: %s", len(logLines), logBuf.String())
	}
	var diagnostic struct {
		Level    string `json:"level"`
		Message  string `json:"msg"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal([]byte(logLines[0]), &diagnostic); err != nil {
		t.Fatal(err)
	}
	if diagnostic.Level != slog.LevelInfo.String() || diagnostic.Message != "provider sign-in complete" || diagnostic.Provider != "kimi" {
		t.Errorf("sign-in diagnostic = %+v", diagnostic)
	}
}

func TestKimiDeviceFlowErrorsNonJSONPoll(t *testing.T) {
	start := `{"user_code":"user-1","device_code":"device-1","verification_uri_complete":"https://kimi.test/authorize?user_code=user-1","expires_in":600,"interval":5}`
	srv, _ := newDeviceIssuer(t, start, []deviceTokenReply{{status: http.StatusBadGateway, body: `bad gateway`}}, nil)
	flow := NewFlow(deviceTestProfile(srv.URL), srv.Client(), func(string) error { return nil })
	flow.sleep = func(context.Context, time.Duration) error { return nil }
	if _, err := flow.SignIn(context.Background()); err == nil || !strings.Contains(err.Error(), "http_502") {
		t.Fatalf("error = %v, want HTTP status code", err)
	}
}
