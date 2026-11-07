package providerauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"
)

// ErrDeviceExpired reports an expired device authorization that must be restarted.
var ErrDeviceExpired = errors.New("providerauth: device authorization expired; start sign-in again")

var (
	errAuthorizationPending = errors.New("authorization_pending")
	errSlowDown             = errors.New("slow_down")
)

// DeviceAuthorization is the issuer's device-flow start response.
type DeviceAuthorization struct {
	UserCode        string `json:"user_code"`
	DeviceCode      string `json:"device_code"`
	VerificationURI string `json:"verification_uri_complete"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

func (f *Flow) signInDevice(ctx context.Context) (*SignInResult, error) {
	auth, err := f.requestDeviceAuthorization(ctx)
	if err != nil {
		return nil, err
	}
	if err := f.opener(auth.VerificationURI); err != nil {
		return nil, err
	}

	interval := time.Duration(auth.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	expiresAt := f.now().Add(DefaultCallbackWait)
	if auth.ExpiresIn > 0 {
		expiresAt = f.now().Add(time.Duration(auth.ExpiresIn) * time.Second)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !f.now().Before(expiresAt) {
			return nil, ErrDeviceExpired
		}
		resp, err := f.pollDeviceToken(ctx, auth.DeviceCode)
		switch {
		case err == nil:
			tokens := resp.tokens(f.now())
			var identity *Identity
			if resp.IDToken != "" {
				identity, err = ParseIDTokenClaims(resp.IDToken)
				if err != nil {
					return nil, err
				}
			}
			return &SignInResult{Tokens: tokens, Identity: identity}, nil
		case errors.Is(err, errAuthorizationPending):
		case errors.Is(err, errSlowDown):
			interval *= 2
			if interval > 30*time.Second {
				interval = 30 * time.Second
			}
		case errors.Is(err, ErrDeviceExpired), errors.Is(err, ErrAccessDenied):
			return nil, err
		default:
			return nil, err
		}
		if err := f.sleep(ctx, interval); err != nil {
			return nil, err
		}
	}
}

func (f *Flow) requestDeviceAuthorization(ctx context.Context) (DeviceAuthorization, error) {
	resp, err := postFormURL(ctx, f.client, f.profile, url.Values{"client_id": {f.profile.ClientID}}, f.profile.DeviceAuthorizationURL())
	if err != nil {
		return DeviceAuthorization{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return DeviceAuthorization{}, decodeTokenEndpointError(resp)
	}
	var auth DeviceAuthorization
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxTokenResponseBody)).Decode(&auth); err != nil {
		return DeviceAuthorization{}, fmt.Errorf("providerauth: decoding device authorization: %w", err)
	}
	if auth.UserCode == "" || auth.DeviceCode == "" || auth.VerificationURI == "" {
		return DeviceAuthorization{}, errors.New("providerauth: device authorization is missing user_code, device_code or verification_uri_complete")
	}
	return auth, nil
}

func (f *Flow) pollDeviceToken(ctx context.Context, deviceCode string) (*tokenResponse, error) {
	form := url.Values{
		"client_id":   {f.profile.ClientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	resp, err := postForm(ctx, f.client, f.profile, form)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var tErr *TokenEndpointError
		if !errors.As(decodeTokenEndpointError(resp), &tErr) {
			return nil, errors.New("providerauth: decoding device token error")
		}
		switch tErr.Code {
		case "authorization_pending":
			return nil, errAuthorizationPending
		case "slow_down":
			return nil, errSlowDown
		case "expired_token":
			return nil, ErrDeviceExpired
		case "access_denied":
			return nil, ErrAccessDenied
		default:
			return nil, tErr
		}
	}
	var out tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxTokenResponseBody)).Decode(&out); err != nil {
		return nil, fmt.Errorf("providerauth: decoding device token response: %w", err)
	}
	if out.AccessToken == "" {
		return nil, errors.New("providerauth: device token response carried no access_token")
	}
	return &out, nil
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
