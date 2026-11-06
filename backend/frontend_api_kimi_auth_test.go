package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/providerauth"
	"github.com/v0lka/c0wrk/core/version"
	"github.com/v0lka/sp4rk/llm"
)

type kimiAuthEventRecorder struct {
	mu     sync.Mutex
	events []KimiAuthEventData
	notify chan struct{}
}

func newKimiAuthEventRecorder() *kimiAuthEventRecorder {
	return &kimiAuthEventRecorder{notify: make(chan struct{}, 1)}
}

func (r *kimiAuthEventRecorder) emit(name string, args ...any) {
	if name != EventKimiAuthState || len(args) == 0 {
		return
	}
	data, ok := args[0].(KimiAuthEventData)
	if !ok {
		return
	}
	r.mu.Lock()
	r.events = append(r.events, data)
	r.mu.Unlock()
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func (r *kimiAuthEventRecorder) waitForState(t *testing.T, state string) KimiAuthEventData {
	t.Helper()
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	for {
		r.mu.Lock()
		for _, event := range r.events {
			if event.State == state {
				r.mu.Unlock()
				return event
			}
		}
		r.mu.Unlock()
		select {
		case <-r.notify:
		case <-timeout.C:
			t.Fatalf("timed out waiting for Kimi auth state %q", state)
		}
	}
}

func newKimiAuthTestAPI(t *testing.T) (*FrontendAPI, *mockBuilder, *kimiAuthEventRecorder) {
	t.Helper()
	f, mock, _ := newChatGPTAuthTestAPI(t)
	f.config.LLM.DefaultModel = "kimi/k3"
	f.config.LLM.Kimi.Models = []string{"k3"}
	f.config.LLM.Kimi.Auth.Mode = config.KimiAuthModeOAuth
	rec := newKimiAuthEventRecorder()
	f.emitEvent = rec.emit
	return f, mock, rec
}

func installKimiManager(t *testing.T, f *FrontendAPI, m *fakeChatGPTManager) {
	t.Helper()
	f.kimiAuth.newManagerFn = func() (chatGPTAuthManager, error) { return m, nil }
	f.Lifecycle().InitKimiAuth()
}

func successfulKimiSignIn(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error) {
	if err := opener("https://auth.kimi.com/device?user_code=test"); err != nil {
		return nil, err
	}
	return &providerauth.SignInResult{
		Tokens:   &providerauth.Tokens{AccessToken: "secret-access", RefreshToken: "secret-refresh", Expiry: time.Now().Add(time.Hour)},
		Identity: &providerauth.Identity{Email: "kimi@example.com"},
	}, nil
}

func TestKimiSignInStatusAndSignOut(t *testing.T) {
	f, mock, rec := newKimiAuthTestAPI(t)
	m := &fakeChatGPTManager{}
	// The real manager commits these fields after SignIn; the shared fake's
	// default helper is ChatGPT-specific, so commit Kimi's result in its hook.
	m.signInHook = func(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error) {
		result, err := successfulKimiSignIn(ctx, opener)
		if err == nil {
			m.mu.Lock()
			m.signedIn = true
			m.identity = result.Identity
			m.expiresAt = result.Tokens.Expiry
			m.mu.Unlock()
		}
		return result, err
	}
	installKimiManager(t, f, m)

	resp, err := f.StartKimiSignIn()
	if err != nil {
		t.Fatalf("StartKimiSignIn: %v", err)
	}
	if !strings.HasPrefix(resp.AuthURL, "https://auth.kimi.com/") {
		t.Errorf("AuthURL = %q", resp.AuthURL)
	}
	rec.waitForState(t, chatgptAuthEventPending)
	success := rec.waitForState(t, chatgptAuthEventSuccess)
	if success.Email != "kimi@example.com" || strings.Contains(success.Email, "secret") {
		t.Errorf("success identity = %+v", success)
	}
	st := f.GetKimiAuthStatus()
	if !st.SignedIn || st.Email != "kimi@example.com" || st.Mode != config.KimiAuthModeOAuth || st.InFlight {
		t.Errorf("status = %+v", st)
	}
	seam, _ := mock.lastSubscriptionSeam()
	if seam.ProviderName != "kimi" || seam.TokenSource == nil {
		t.Errorf("builder seam = %+v, want live Kimi source", seam)
	}

	if err := f.SignOutKimi(); err != nil {
		t.Fatalf("SignOutKimi: %v", err)
	}
	if f.GetKimiAuthStatus().SignedIn {
		t.Error("status remains signed in after sign-out")
	}
	seam, _ = mock.lastSubscriptionSeam()
	if seam.ProviderName != "kimi" || seam.TokenSource != nil {
		t.Errorf("seam after Kimi sign-out = %+v", seam)
	}
	rec.waitForState(t, providerAuthEventCancelled)
}

func TestKimiCancelSignIn(t *testing.T) {
	f, _, rec := newKimiAuthTestAPI(t)
	m := &fakeChatGPTManager{signInHook: func(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error) {
		if err := opener("https://auth.kimi.com/device?user_code=test"); err != nil {
			return nil, err
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	installKimiManager(t, f, m)
	if _, err := f.StartKimiSignIn(); err != nil {
		t.Fatal(err)
	}
	if err := f.CancelKimiSignIn(); err != nil {
		t.Fatal(err)
	}
	rec.waitForState(t, chatgptAuthEventCancelled)
	if st := f.GetKimiAuthStatus(); st.InFlight || st.LastError != "" {
		t.Errorf("status after cancellation = %+v", st)
	}
}

func TestCleanupCancelsAnInFlightKimiSignIn(t *testing.T) {
	f, _, rec := newKimiAuthTestAPI(t)
	var logBuf bytes.Buffer
	f.logger = slog.New(slog.NewJSONHandler(&logBuf, nil))
	m := &fakeChatGPTManager{signInHook: func(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error) {
		if err := opener("https://auth.kimi.com/device?user_code=test"); err != nil {
			return nil, err
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	installKimiManager(t, f, m)
	if _, err := f.StartKimiSignIn(); err != nil {
		t.Fatal(err)
	}

	f.Lifecycle().Cleanup()
	rec.waitForState(t, chatgptAuthEventCancelled)
	if st := f.GetKimiAuthStatus(); st.InFlight || st.LastError != "" {
		t.Errorf("status after shutdown cancellation = %+v", st)
	}
	logLines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	if len(logLines) != 6 {
		t.Fatalf("cleanup emitted %d diagnostics, want six lifecycle records: %s", len(logLines), logBuf.String())
	}
	for _, line := range logLines {
		var record struct {
			Level   string `json:"level"`
			Message string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.Level != slog.LevelInfo.String() || record.Message != "cleanup step complete" {
			t.Errorf("unexpected cleanup diagnostic: %s", line)
		}
	}
}

func TestKimiRestoreAndModelPresets(t *testing.T) {
	f, mock, rec := newKimiAuthTestAPI(t)
	m := &fakeChatGPTManager{signedIn: true, identity: &providerauth.Identity{Email: "restored@example.com"}}
	installKimiManager(t, f, m)
	if got := rec.waitForState(t, chatgptAuthEventSuccess).Email; got != "restored@example.com" {
		t.Errorf("restored email = %q", got)
	}
	if mock.rebuildRouterCallsSnapshot() == 0 {
		t.Error("signed-in oauth restore did not rebuild the router")
	}
	seam, _ := mock.lastSubscriptionSeam()
	if seam.ProviderName != "kimi" || seam.TokenSource == nil {
		t.Errorf("restored seam = %+v", seam)
	}

	oauthModels := f.GetKimiModelPreset().Models
	gotOAuth := make([]string, 0, len(oauthModels))
	for _, model := range oauthModels {
		gotOAuth = append(gotOAuth, model.Name)
	}
	if !slices.Equal(gotOAuth, []string{"k3.1-pro", "k3.1-pro-highspeed", "k3", "k3-256k", "kimi-for-coding", "kimi-for-coding-highspeed"}) {
		t.Errorf("oauth preset = %v", gotOAuth)
	}
}

func TestKimiAuthModeConfigRoundTrip(t *testing.T) {
	f, _, _ := newKimiAuthTestAPI(t)
	mode := config.KimiAuthModeAPIKey
	if err := f.UpdateLLMConfig(LLMFullConfigRequest{Kimi: &ProviderConfigRequest{AuthMode: &mode}}); err != nil {
		t.Fatalf("UpdateLLMConfig: %v", err)
	}
	if got := f.GetConfig().LLM.Kimi.AuthMode; got != config.KimiAuthModeAPIKey {
		t.Errorf("wire mode = %q, want api_key", got)
	}
	persisted, err := config.Load(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.LLM.Kimi.Auth.Mode != config.KimiAuthModeAPIKey {
		t.Errorf("persisted mode = %q", persisted.LLM.Kimi.Auth.Mode)
	}
	bad := "OAuth"
	if err := f.UpdateLLMConfig(LLMFullConfigRequest{Kimi: &ProviderConfigRequest{AuthMode: &bad}}); err == nil || !strings.Contains(err.Error(), "kimi auth_mode") {
		t.Errorf("invalid mode error = %v", err)
	}
	if got := f.GetConfig().LLM.Kimi.AuthMode; got != config.KimiAuthModeAPIKey {
		t.Errorf("rejected update changed mode to %q", got)
	}
}

func TestKimiRegionConfigRoundTrip(t *testing.T) {
	f, _, _ := newKimiAuthTestAPI(t)
	region := config.KimiRegionMainlandCN
	if err := f.UpdateLLMConfig(LLMFullConfigRequest{Kimi: &ProviderConfigRequest{AuthRegion: &region}}); err != nil {
		t.Fatalf("UpdateLLMConfig region: %v", err)
	}
	if got := f.GetConfig().LLM.Kimi.AuthRegion; got != config.KimiRegionMainlandCN {
		t.Errorf("wire region = %q, want mainland_cn", got)
	}
	persisted, err := config.Load(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.LLM.Kimi.Auth.Region != config.KimiRegionMainlandCN {
		t.Errorf("persisted region = %q", persisted.LLM.Kimi.Auth.Region)
	}
	invalid := "europe"
	if err := f.UpdateLLMConfig(LLMFullConfigRequest{Kimi: &ProviderConfigRequest{AuthRegion: &invalid}}); err == nil || !strings.Contains(err.Error(), "kimi auth_region") {
		t.Errorf("invalid region error = %v", err)
	}
	if got := f.GetConfig().LLM.Kimi.AuthRegion; got != config.KimiRegionMainlandCN {
		t.Errorf("rejected update changed region to %q", got)
	}
}

func TestKimiRegionChangeRebindsManagerWithoutRestart(t *testing.T) {
	f, _, rec := newKimiAuthTestAPI(t)
	oldManager := &fakeChatGPTManager{signedIn: true}
	installKimiManager(t, f, oldManager)
	var reboundProfile providerauth.Profile
	f.kimiAuth.newManagerFn = func() (chatGPTAuthManager, error) {
		reboundProfile = f.kimiAuthDescriptor().profile()
		return &fakeChatGPTManager{signInHook: successfulKimiSignIn}, nil
	}
	region := config.KimiRegionMainlandCN
	if err := f.UpdateLLMConfig(LLMFullConfigRequest{Kimi: &ProviderConfigRequest{AuthRegion: &region}}); err != nil {
		t.Fatalf("UpdateLLMConfig region: %v", err)
	}
	if oldManager.signOutCalls != 1 {
		t.Errorf("old manager sign-out calls = %d, want 1 before region rebinding", oldManager.signOutCalls)
	}
	if reboundProfile.Issuer != "https://auth.kimi.com" {
		t.Errorf("rebound issuer = %q, want mainland", reboundProfile.Issuer)
	}
	if f.kimiAuth.manager == nil {
		t.Fatal("region update did not construct a new manager")
	}
	if f.GetKimiAuthStatus().SignedIn {
		t.Error("credentials survived region change instead of requiring reauthorization")
	}
	rec.waitForState(t, providerAuthEventCancelled)
	if _, err := f.StartKimiSignIn(); err != nil {
		t.Fatalf("fresh sign-in after region change: %v", err)
	}
	rec.waitForState(t, providerAuthEventPending)
}

func TestKimiRegionChangeWaitsForStartupRestore(t *testing.T) {
	f, _, _ := newKimiAuthTestAPI(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	f.kimiAuth.newManagerFn = func() (chatGPTAuthManager, error) {
		close(entered)
		<-release
		return &fakeChatGPTManager{signedIn: true}, nil
	}
	initDone := make(chan struct{})
	go func() {
		defer close(initDone)
		f.Lifecycle().InitKimiAuth()
	}()
	<-entered
	region := config.KimiRegionMainlandCN
	err := f.UpdateLLMConfig(LLMFullConfigRequest{Kimi: &ProviderConfigRequest{AuthRegion: &region}})
	if err == nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("region update during startup restore = %v, want an actionable restore-in-progress error", err)
	}
	if got := f.GetConfig().LLM.Kimi.AuthRegion; got != config.KimiRegionGlobal {
		t.Errorf("region after rejected update = %q, want global", got)
	}
	releaseOnce.Do(func() { close(release) })
	<-initDone
}

func TestKimiRegionChangeDuringSignIn(t *testing.T) {
	f, _, _ := newKimiAuthTestAPI(t)
	started := make(chan struct{})
	release := make(chan struct{})
	manager := &fakeChatGPTManager{signInHook: func(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error) {
		if err := opener("https://auth.kimi.com/device?user_code=test"); err != nil {
			return nil, err
		}
		close(started)
		<-release
		return successfulKimiSignIn(ctx, opener)
	}}
	installKimiManager(t, f, manager)
	if _, err := f.StartKimiSignIn(); err != nil {
		t.Fatal(err)
	}
	<-started
	region := config.KimiRegionMainlandCN
	updateDone := make(chan error, 1)
	go func() {
		updateDone <- f.UpdateLLMConfig(LLMFullConfigRequest{Kimi: &ProviderConfigRequest{AuthRegion: &region}})
	}()
	close(release)
	if err := <-updateDone; err != nil {
		t.Fatalf("region change during sign-in: %v", err)
	}
	if f.GetKimiAuthStatus().SignedIn {
		t.Error("completed sign-in survived region change instead of requiring reauthorization")
	}
}

func TestKimiRegionChangeSignOutFailureKeepsPreviousConfig(t *testing.T) {
	f, _, _ := newKimiAuthTestAPI(t)
	oldManager := &fakeChatGPTManager{signedIn: true, signOutErr: errors.New("keychain delete failed")}
	installKimiManager(t, f, oldManager)
	region := config.KimiRegionMainlandCN
	if err := f.UpdateLLMConfig(LLMFullConfigRequest{Kimi: &ProviderConfigRequest{AuthRegion: &region}}); err == nil {
		t.Fatal("region change succeeded despite keychain delete failure")
	}
	if got := f.GetConfig().LLM.Kimi.AuthRegion; got != config.KimiRegionGlobal {
		t.Errorf("persisted region = %q after failed rebind, want global", got)
	}
	if f.kimiAuth.manager != oldManager {
		t.Error("failed sign-out replaced the old manager")
	}
}

func TestKimiRegionChangeConstructionFailureKeepsPreviousManager(t *testing.T) {
	f, _, _ := newKimiAuthTestAPI(t)
	oldManager := &fakeChatGPTManager{signedIn: true}
	installKimiManager(t, f, oldManager)
	f.kimiAuth.newManagerFn = func() (chatGPTAuthManager, error) {
		return nil, errors.New("keychain initialization failed")
	}
	region := config.KimiRegionMainlandCN
	if err := f.UpdateLLMConfig(LLMFullConfigRequest{Kimi: &ProviderConfigRequest{AuthRegion: &region}}); err == nil {
		t.Fatal("region change succeeded despite manager initialization failure")
	}
	if got := f.GetConfig().LLM.Kimi.AuthRegion; got != config.KimiRegionGlobal {
		t.Errorf("region = %q after failed initialization, want global", got)
	}
	if f.kimiAuth.manager != oldManager {
		t.Error("failed initialization discarded the previous manager")
	}
	if f.GetKimiAuthStatus().SignedIn {
		t.Error("old-region credential survived the attempted region change")
	}
}

func TestKimiRegionChangeRejectsSignInUntilRebindCompletes(t *testing.T) {
	f, _, _ := newKimiAuthTestAPI(t)
	installKimiManager(t, f, &fakeChatGPTManager{signedIn: true})
	constructing := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	f.kimiAuth.newManagerFn = func() (chatGPTAuthManager, error) {
		close(constructing)
		<-release
		return &fakeChatGPTManager{}, nil
	}
	region := config.KimiRegionMainlandCN
	updateDone := make(chan error, 1)
	go func() {
		updateDone <- f.UpdateLLMConfig(LLMFullConfigRequest{Kimi: &ProviderConfigRequest{AuthRegion: &region}})
	}()
	<-constructing
	if _, err := f.StartKimiSignIn(); err == nil || !strings.Contains(err.Error(), "region is changing") {
		t.Errorf("sign-in during rebind error = %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-updateDone; err != nil {
		t.Fatalf("region change: %v", err)
	}
}

func TestKimiRegionChangeSaveFailureRestoresPreviousManagerProfile(t *testing.T) {
	f, _, _ := newKimiAuthTestAPI(t)
	installKimiManager(t, f, &fakeChatGPTManager{signedIn: true})
	var issuers []string
	f.kimiAuth.newManagerFn = func() (chatGPTAuthManager, error) {
		issuers = append(issuers, f.kimiAuthDescriptor().profile().Issuer)
		return &fakeChatGPTManager{}, nil
	}
	f.configPath = t.TempDir()
	region := config.KimiRegionMainlandCN
	if err := f.UpdateLLMConfig(LLMFullConfigRequest{Kimi: &ProviderConfigRequest{AuthRegion: &region}}); err == nil || !strings.Contains(err.Error(), "failed to persist") {
		t.Fatalf("save failure error = %v", err)
	}
	if got := f.GetConfig().LLM.Kimi.AuthRegion; got != config.KimiRegionGlobal {
		t.Errorf("region = %q after failed save, want global", got)
	}
	wantIssuers := []string{"https://auth.kimi.com", "https://auth.kimi.ai"}
	if !slices.Equal(issuers, wantIssuers) {
		t.Errorf("manager profile issuers = %v, want %v", issuers, wantIssuers)
	}
}

func TestKimiRegionChangeCancellationTimeoutKeepsPreviousConfig(t *testing.T) {
	f, _, rec := newKimiAuthTestAPI(t)
	f.kimiAuth.rebindJoinTimeoutOverride = time.Millisecond
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	manager := &fakeChatGPTManager{signInHook: func(ctx context.Context, opener providerauth.BrowserOpener) (*providerauth.SignInResult, error) {
		if err := opener("https://auth.kimi.com/device?user_code=test"); err != nil {
			return nil, err
		}
		<-release
		return nil, ctx.Err()
	}}
	installKimiManager(t, f, manager)
	if _, err := f.StartKimiSignIn(); err != nil {
		t.Fatal(err)
	}
	region := config.KimiRegionMainlandCN
	if err := f.UpdateLLMConfig(LLMFullConfigRequest{Kimi: &ProviderConfigRequest{AuthRegion: &region}}); err == nil {
		t.Fatal("region change succeeded despite cancellation timeout")
	}
	if got := f.GetConfig().LLM.Kimi.AuthRegion; got != config.KimiRegionGlobal {
		t.Errorf("persisted region = %q after cancellation timeout, want global", got)
	}
	releaseOnce.Do(func() { close(release) })
	rec.waitForState(t, chatgptAuthEventCancelled)
}

func TestKimiAuthUnavailableAndRPCShapes(t *testing.T) {
	f, _, _ := newKimiAuthTestAPI(t)
	var logBuf bytes.Buffer
	f.logger = slog.New(slog.NewJSONHandler(&logBuf, nil))
	f.kimiAuth.newManagerFn = func() (chatGPTAuthManager, error) { return nil, errors.New("keychain unavailable") }
	f.Lifecycle().InitKimiAuth()
	if st := f.GetKimiAuthStatus(); st.SignedIn || !strings.Contains(st.LastError, "keychain unavailable") {
		t.Errorf("unavailable status = %+v", st)
	}
	if _, err := f.StartKimiSignIn(); err == nil || !strings.Contains(err.Error(), "keychain unavailable") {
		t.Errorf("start error = %v", err)
	}
	logLines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	if len(logLines) != 1 {
		t.Fatalf("auth init emitted %d diagnostics, want exactly one: %s", len(logLines), logBuf.String())
	}
	var record struct {
		Level   string `json:"level"`
		Message string `json:"msg"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal([]byte(logLines[0]), &record); err != nil {
		t.Fatalf("decode captured auth diagnostic: %v", err)
	}
	if record.Level != slog.LevelWarn.String() || record.Message != "Kimi subscription auth unavailable" || !strings.Contains(record.Error, "keychain unavailable") {
		t.Errorf("captured diagnostic = %+v", record)
	}

	errType := reflect.TypeOf((*error)(nil)).Elem()
	apiType := reflect.TypeOf(f)
	for name, outputs := range map[string][]reflect.Type{
		"StartKimiSignIn":    {reflect.TypeOf((*KimiSignInResponse)(nil)), errType},
		"CancelKimiSignIn":   {errType},
		"GetKimiAuthStatus":  {reflect.TypeOf(KimiAuthStatusResponse{})},
		"SignOutKimi":        {errType},
		"GetKimiModelPreset": {reflect.TypeOf(KimiModelPresetResponse{})},
		"FetchKimiModels":    {reflect.TypeOf(KimiModelPresetResponse{}), errType},
	} {
		method, ok := apiType.MethodByName(name)
		if !ok {
			t.Errorf("RPC %s is missing", name)
			continue
		}
		if method.Type.NumIn() != 1 || method.Type.NumOut() != len(outputs) {
			t.Errorf("RPC %s signature = %s", name, method.Type)
			continue
		}
		for i, want := range outputs {
			if got := method.Type.Out(i); got != want {
				t.Errorf("RPC %s output %d = %v, want %v", name, i, got, want)
			}
		}
	}
}

func TestFetchKimiModels_LiveCatalog(t *testing.T) {
	catalog := `{"object":"list","data":[
		{"id":"k3.1-pro","context_length":1048576,"supports_reasoning":true},
		{"id":"k3","context_length":1,"supports_reasoning":false},
		{"id":""},
		{"id":"k3.1-pro","context_length":1},
		{"id":"c0wrk-kimi-catalog-unknown","context_length":262144,"supports_reasoning":true}
	]}`
	var gotAuth, gotUA, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(catalog))
	}))
	t.Cleanup(srv.Close)

	f, mock, _ := newKimiAuthTestAPI(t)
	m := &fakeChatGPTManager{signedIn: true, tokenExtraHeaders: map[string]string{"User-Agent": "c0wrk/" + version.Version}}
	installKimiManager(t, f, m)
	mock.registry = llm.NewModelRegistry(nil)
	f.kimiAuth.modelsBaseURLOverride = srv.URL

	resp, err := f.FetchKimiModels()
	if err != nil {
		t.Fatalf("FetchKimiModels: %v", err)
	}
	if gotPath != "/models" {
		t.Errorf("catalog path = %q, want /models", gotPath)
	}
	if gotAuth != "Bearer fake-access" || gotUA != "c0wrk/"+version.Version {
		t.Errorf("catalog auth = %q user-agent = %q", gotAuth, gotUA)
	}
	wantOrder := []string{"k3.1-pro", "k3", "c0wrk-kimi-catalog-unknown"}
	if len(resp.Models) != len(wantOrder) {
		t.Fatalf("models = %+v, want %v", resp.Models, wantOrder)
	}
	for i, want := range wantOrder {
		if resp.Models[i].Name != want {
			t.Errorf("model %d = %q, want %q", i, resp.Models[i].Name, want)
		}
	}
	k3 := resp.Models[1]
	if meta, ok := mock.registry.ResolveLocal("k3"); !ok || k3.ContextWindow != meta.ContextWindow || !k3.Reasoning {
		t.Errorf("registry model = %+v, want registry window %d and reasoning", k3, meta.ContextWindow)
	}
	unknown := resp.Models[2]
	if unknown.ContextWindow != 262144 || !unknown.Reasoning || unknown.OutputLimit != 0 {
		t.Errorf("unknown model = %+v, want the endpoint context_length and reasoning flag", unknown)
	}
	if _, known := mock.registry.ResolveLocal("k3.1-pro"); known {
		t.Fatalf("k3.1-pro is already in the registry; the endpoint-window assertion would no longer prove the fallback")
	}
	if resp.Models[0].ContextWindow != 1048576 || !resp.Models[0].Reasoning {
		t.Errorf("k3.1-pro = %+v, want the endpoint window and reasoning", resp.Models[0])
	}
}

func TestFetchKimiModels_RequiresSignIn(t *testing.T) {
	f, _, _ := newKimiAuthTestAPI(t)
	installKimiManager(t, f, &fakeChatGPTManager{})
	if _, err := f.FetchKimiModels(); err == nil || !strings.Contains(err.Error(), "sign in with Kimi") {
		t.Fatalf("signed-out error = %v", err)
	}
}

func TestFetchKimiModels_DoesNotFollowRedirectsOrEchoTheToken(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, "http://127.0.0.1:1/steal", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	f, _, _ := newKimiAuthTestAPI(t)
	installKimiManager(t, f, &fakeChatGPTManager{signedIn: true})
	f.kimiAuth.modelsBaseURLOverride = srv.URL
	_, err := f.FetchKimiModels()
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("redirect error = %v", err)
	}
	if strings.Contains(err.Error(), "fake-access") {
		t.Errorf("error echoed the access token: %v", err)
	}
	if hits != 1 {
		t.Errorf("catalog requests = %d, want 1 (redirect must not be followed)", hits)
	}
}

func TestFetchKimiModelsKeepsManagerAndRegionalEndpointSnapshot(t *testing.T) {
	oldHits, newHits := 0, 0
	var oldAuth string
	oldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oldHits++
		oldAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(oldServer.Close)
	newServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		newHits++
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(newServer.Close)

	f, _, _ := newKimiAuthTestAPI(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	oldManager := &fakeChatGPTManager{
		signedIn: true,
		tokenHook: func(context.Context) (llm.BearerToken, error) {
			close(entered)
			<-release
			return llm.BearerToken{AccessToken: "old-access", TokenType: "Bearer"}, nil
		},
	}
	installKimiManager(t, f, oldManager)
	f.kimiAuth.modelsBaseURL = oldServer.URL
	fetchDone := make(chan error, 1)
	go func() {
		_, err := f.FetchKimiModels()
		fetchDone <- err
	}()
	<-entered
	f.kimiAuth.modelsBaseURLOverride = newServer.URL
	f.kimiAuth.newManagerFn = func() (chatGPTAuthManager, error) {
		return &fakeChatGPTManager{}, nil
	}
	region := config.KimiRegionMainlandCN
	if err := f.UpdateLLMConfig(LLMFullConfigRequest{Kimi: &ProviderConfigRequest{AuthRegion: &region}}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-fetchDone; err != nil {
		t.Fatalf("FetchKimiModels during region rebind: %v", err)
	}
	if oldHits != 1 || newHits != 0 {
		t.Errorf("catalog requests old/new = %d/%d, want 1/0", oldHits, newHits)
	}
	if oldAuth != "Bearer old-access" {
		t.Errorf("old endpoint authorization = %q, want the old manager's token", oldAuth)
	}
}
