package core

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// TestToolRegistry_Clone_Independent verifies that the per-session clone
// produced by ToolRegistry.Clone() does not share mutable policy state with
// the parent registry. Regression guard for C-3 (policy leak across sessions).
func TestToolRegistry_Clone_Independent(t *testing.T) {
	parent := tools.NewToolRegistry()
	parent.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupExecute: sdktools.PolicyAlwaysDeny,
	})

	child := parent.Clone()
	if child == nil {
		t.Fatal("Clone returned nil")
	}
	// The clone starts with a COPY of the parent's map.
	if got := child.GroupPolicies()[sdktools.GroupExecute]; got != sdktools.PolicyAlwaysDeny {
		t.Fatalf("clone lost the parent's execute policy: got %v, want always_deny", got)
	}

	// Replacing the child's map must NOT propagate to the parent.
	child.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupLocalWrite: sdktools.PolicyAlwaysAllow,
	})
	if _, ok := parent.GroupPolicies()[sdktools.GroupLocalWrite]; ok {
		t.Error("parent inherited the child's group-policy mutation")
	}
	if got := parent.GroupPolicies()[sdktools.GroupExecute]; got != sdktools.PolicyAlwaysDeny {
		t.Errorf("parent execute policy = %v, want always_deny", got)
	}

	// Replacing the parent's map afterwards must not show up on the child
	// either: the child keeps its own map untouched.
	parent.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupExecute: sdktools.PolicyAlwaysAllow,
	})
	if got := child.GroupPolicies()[sdktools.GroupLocalWrite]; got != sdktools.PolicyAlwaysAllow {
		t.Errorf("child's own local_write policy = %v, want always_allow", got)
	}
	if _, ok := child.GroupPolicies()[sdktools.GroupExecute]; ok {
		t.Error("child saw the parent's later mutation — the maps must be independent")
	}
}

// TestApplySecurityPolicies covers the BuilderConfig → registry group-policy
// mapping. We exercise the helper directly via a fake builder so we don't
// need a full async-init builder instance.
func TestApplySecurityPolicies(t *testing.T) {
	cfg := &BuilderConfig{
		Security: BuilderSecurityConfig{
			Groups: map[string]BuilderGroupPolicy{
				"local_read":   {Policy: "allow"},
				"execute":      {Policy: "deny", Blocklist: []string{"rm\\s+-rf"}},
				"local_write":  {Policy: "user_confirm"},
				"system":       {Policy: "allow"},    // must be skipped: not configurable
				"bogus_group":  {Policy: "allow"},    // must be skipped: unknown group
				"typo_allowed": {Policy: "nonsense"}, // fail-safe → user_confirm
			},
		},
		ExpandEnvVars: func(s string) string { return s },
	}

	b := &OrchestratorBuilder{registry: tools.NewToolRegistry()}
	b.applySecurityPolicies(cfg)

	got := b.registry.GroupPolicies()
	want := map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupLocalRead:  sdktools.PolicyAlwaysAllow,
		sdktools.GroupExecute:    sdktools.PolicyAlwaysDeny,
		sdktools.GroupLocalWrite: sdktools.PolicyUserConfirm,
	}
	for group, policy := range want {
		if got[group] != policy {
			t.Errorf("group policy %q = %v, want %v", group, got[group], policy)
		}
	}
	for _, skipped := range []sdktools.ToolGroup{sdktools.GroupSystem, sdktools.ToolGroup("bogus_group"), sdktools.ToolGroup("typo_allowed")} {
		if _, ok := got[skipped]; ok {
			t.Errorf("group %q must be skipped by applySecurityPolicies", skipped)
		}
	}

	// Re-applying must not panic.
	b.applySecurityPolicies(cfg)
}

// TestUpdateSecurityPolicies_ReachesLiveSessionRegistries verifies that a
// runtime security-policy push reaches ALREADY-OPEN sessions: Build hands
// each session its own registry clone, so applying the new state only to the
// shared builder registry would leave live sessions executing on stale state
// — a deny set in the security settings UI would fail open on every session
// created before the save. The clone registered by registerSessionRegistry
// must receive the group-policy half of every subsequent
// applySecurityPolicies push until the orchestrator's cleanup hook
// unregisters it (the autonomy half is pinned per task instead — see
// TestApplySecurityPolicies_PinsAutonomyToTaskLaunch).
func TestUpdateSecurityPolicies_ReachesLiveSessionRegistries(t *testing.T) {
	cfgOf := func(execPolicy, autonomyMode string, autoApprove bool) *BuilderConfig {
		return &BuilderConfig{
			Security: BuilderSecurityConfig{
				Groups:                     map[string]BuilderGroupPolicy{"execute": {Policy: execPolicy}},
				AutoApproveWorkspaceWrites: autoApprove,
				AutonomyMode:               autonomyMode,
			},
			ExpandEnvVars: func(s string) string { return s },
		}
	}

	b := &OrchestratorBuilder{registry: tools.NewToolRegistry()}
	b.applySecurityPolicies(cfgOf("user_confirm", AutonomyModeStandard, false))

	// The "already-open session": a registered clone created under the old
	// security state (what Build does for every session).
	session := b.registerSessionRegistry()

	// The runtime push (the security settings UI save path) must reach the
	// live session clone, not just the shared registry.
	b.UpdateSecurityPolicies(cfgOf("deny", AutonomyModeAssisted, true))
	if got := b.registry.GroupPolicies()[sdktools.GroupExecute]; got != sdktools.PolicyAlwaysDeny {
		t.Fatalf("shared registry execute policy = %v, want always_deny", got)
	}
	if got := session.GroupPolicies()[sdktools.GroupExecute]; got != sdktools.PolicyAlwaysDeny {
		t.Fatalf("live session registry execute policy = %v, want always_deny — a runtime deny must reach already-open sessions", got)
	}

	// Unregistering (the orchestrator cleanup hook) stops future pushes.
	b.unregisterSessionRegistry(session)
	b.UpdateSecurityPolicies(cfgOf("allow", AutonomyModeStandard, false))
	if got := session.GroupPolicies()[sdktools.GroupExecute]; got != sdktools.PolicyAlwaysDeny {
		t.Fatalf("unregistered session execute policy = %v, want always_deny (no pushes after cleanup)", got)
	}
	if got := b.registry.GroupPolicies()[sdktools.GroupExecute]; got != sdktools.PolicyAlwaysAllow {
		t.Fatalf("shared registry execute policy = %v, want always_allow", got)
	}
}

// TestNewOrchestratorBuilder_NilExpandEnvVars verifies the W-14 nil-guard:
// constructing a builder without an ExpandEnvVars hook should not panic.
func TestNewOrchestratorBuilder_NilExpandEnvVars(t *testing.T) {
	cfg := &BuilderConfig{
		Security: BuilderSecurityConfig{},
		// ExpandEnvVars intentionally omitted.
	}

	b, err := NewOrchestratorBuilder(cfg, nil, nil, expectedCoreLogger(t, expectedStartupDiagnostic(), expectedCoreDiagnostic{"WARN", "rebuildJudge: failed to build LLM router for judge", map[string]string{"error": "no providers configured"}}, expectedCoreDiagnostic{"WARN", "tool judge rebuild failed: judge will not be available for on-demand evaluation", nil}))
	if err != nil {
		t.Fatalf("NewOrchestratorBuilder failed: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := b.waitReady(ctx); err != nil {
			t.Errorf("waitReady(empty providers) = %v, want completed init", err)
		}
		if err := b.waitMCPReady(ctx); err != nil {
			t.Errorf("waitMCPReady(empty config) = %v, want completed init", err)
		}
	})
	if b == nil {
		t.Fatal("expected non-nil builder")
	}

	// ExpandEnvVars must be a callable no-op now.
	if got := cfg.ExpandEnvVars("hello"); got != "hello" {
		t.Errorf("ExpandEnvVars no-op returned %q, want hello", got)
	}

	// Wait for async init with a short context to avoid hanging.
	waitCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so waitReady returns ctx.Err()
	_ = b.WaitReady(waitCtx)
}

// TestBuilder_Build_FullPipeline verifies that Build() succeeds with a valid
// config and returns an Orchestrator with all expected dependencies wired.
// Uses a real LLM config so the router, planner, and reflector are actually
// constructed. Async init completes before Build() is called.
func TestBuilder_Build_FullPipeline(t *testing.T) {
	cfg := &BuilderConfig{
		LLM: BuilderLLMConfig{
			DefaultModel: "local-model",
			ProviderConfigs: map[string]BuilderProviderConfig{
				"openai": {ProviderType: "openai", Models: []string{"local-model"}},
			},
			Retry: BuilderRetryConfig{MaxRetries: 1, InitialBackoff: "1s", MaxBackoff: "10s"},
		},
		Security: BuilderSecurityConfig{},
		Orchestration: BuilderOrchestrationConfig{
			MaxDependencyContextChars: 8000,
			MaxJudgeCacheSize:         1000,
		},
		Executor: BuilderExecutorConfig{
			MaxRetries:         2,
			OutputTokenReserve: 1024,
			Compaction: BuilderCompactionConfig{
				SlidingWindow: BuilderSlidingWindow{KeepFirst: 3, KeepLast: 10},
				Thresholds:    BuilderCompactionThresholds{PredictivePercent: 80, WarningPercent: 90, EmergencyPercent: 95},
			},
			ToolResultBudget:  BuilderToolResultBudget{HardCapTokens: 32000, MaxFillFraction: 0.4},
			ToolOutputPruning: BuilderToolOutputPruning{KeepLastN: 5},
			CircuitBreaker: BuilderCircuitBreaker{
				RepeatNudgeThreshold:         3,
				RepeatAbortThreshold:         5,
				TruncationAbortThreshold:     10,
				ParseErrorAbortThreshold:     5,
				FruitlessNudgeThreshold:      3,
				FruitlessAbortThreshold:      5,
				SameToolRepeatNudgeThreshold: 4,
				SameToolRepeatAbortThreshold: 6,
			},
		},
		Timeouts: BuilderTimeoutsConfig{
			BashMaxTimeout:       120,
			BashWaitDelay:        2,
			RipgrepTimeout:       30,
			WebFetchTimeout:      30,
			WebFetchProxyTimeout: 30,
			WebFetchRetries:      2,
			WebSearchTimeout:     30,
		},
		ToolLimits: BuilderToolLimitsConfig{
			ReadDefaultLines:    100,
			WebSearchMaxResults: 5,
		},
		ExpandEnvVars: func(s string) string { return s },
	}

	b, err := NewOrchestratorBuilder(cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewOrchestratorBuilder failed: %v", err)
	}
	if b == nil {
		t.Fatal("expected non-nil builder")
	}

	// Wait for async init to complete before calling Build().
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.WaitReady(waitCtx); err != nil {
		t.Logf("async init returned error (expected when no real LLM is running): %v", err)
	}

	// Build should fail gracefully with an informative error when no LLM
	// provider is actually reachable, not panic or return nil.
	orch, buildErr := b.Build(cfg, nil, nil, "", nil, nil, nil, nil)
	if buildErr == nil && orch != nil {
		// If the local LM Studio happens to be running, verify basic wiring.
		if orch.router == nil {
			t.Error("expected non-nil router in orchestrator")
		}
		t.Log("Build succeeded (LM Studio was reachable)")
	} else {
		// Expected path: Build fails because no real LLM is available.
		t.Logf("Build failed as expected (no LLM provider): %v", buildErr)
	}

	// Verify registry is properly set up regardless of Build outcome.
	if b.ToolRegistry() == nil {
		t.Error("expected non-nil tool registry")
	}
}

// TestListAnthropicModels_Success verifies that listAnthropicModels parses a
// standard {"data":[{"id":...}]} response from an Anthropic-compatible
// endpoint and returns sorted, non-empty model IDs.
func TestListAnthropicModels_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %q, want /v1/models", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "proxy-key" {
			t.Errorf("x-api-key header = %q, want 'proxy-key'", r.Header.Get("x-api-key"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-20250514"},{"id":"claude-opus-4-20250514"}]}`))
	}))
	defer srv.Close()

	names, err := listAnthropicModels(context.Background(), srv.URL, "proxy-key", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"claude-opus-4-20250514", "claude-sonnet-4-20250514"}
	if len(names) != 2 || names[0] != want[0] || names[1] != want[1] {
		t.Errorf("names = %v, want %v", names, want)
	}
}

// TestListAnthropicModels_BaseURLWithV1 verifies URL normalization when the
// base URL already ends with "/v1".
func TestListAnthropicModels_BaseURLWithV1(t *testing.T) {
	var hitPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-20250514"}]}`))
	}))
	defer srv.Close()

	_, err := listAnthropicModels(context.Background(), srv.URL+"/v1", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hitPath != "/v1/models" {
		t.Errorf("request path = %q, want /v1/models", hitPath)
	}
}

// TestListAnthropicModels_OversizedBody verifies the bounded read (#23): a
// response larger than maxModelsListBodyLen is refused with an actionable
// error instead of being buffered unbounded from a configurable endpoint.
func TestListAnthropicModels_OversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat("a", maxModelsListBodyLen+1024)))
	}))
	defer srv.Close()

	_, err := listAnthropicModels(context.Background(), srv.URL, "", nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized models response: err = %v, want a byte-cap error", err)
	}
}

// TestListOpenAIModels_Success guards the bounded-client wiring (#69): a
// normal listing still parses through the SDK with the body-capping transport
// attached to the client.
func TestListOpenAIModels_Success(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("request path = %q, want /models", r.URL.Path)
		}
		hadAuth = r.Header.Get("Authorization") == "Bearer test-key"
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-b"},{"id":"model-a"}]}`))
	}))
	defer srv.Close()

	names, err := listOpenAIModels(context.Background(), srv.URL, "test-key", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hadAuth {
		t.Error("Authorization header with the API key was not sent")
	}
	want := []string{"model-a", "model-b"}
	if len(names) != 2 || names[0] != want[0] || names[1] != want[1] {
		t.Errorf("names = %v, want %v", names, want)
	}
}

// TestListOpenAIModels_OversizedBody verifies the transport-level body cap
// (#69): the openai-go SDK buffers the /v1/models response with a bare
// io.ReadAll and offers no maximum-body option, so the capped transport must
// turn an oversized body into an actionable error instead of an unbounded
// allocation from a UI-configurable endpoint.
func TestListOpenAIModels_OversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat("a", maxModelsListBodyLen+1024)))
	}))
	defer srv.Close()

	_, err := listOpenAIModels(context.Background(), srv.URL, "", nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized models response: err = %v, want a byte-cap error", err)
	}
}

// TestWaitReadyInitErrSynchronizedWithRebuild is the #16 race witness: it
// drives WaitReady's locked read of initErr concurrently with the
// RebuildRouter-style locked write, so `go test -race` (make test-stress)
// flags any unsynchronized access. Without the lock the two goroutines read
// and write b.initErr with no shared synchronization.
func TestWaitReadyInitErrSynchronizedWithRebuild(t *testing.T) {
	b := &OrchestratorBuilder{
		logger:   slog.New(slog.DiscardHandler),
		initDone: make(chan struct{}),
	}
	close(b.initDone)
	b.mu.Lock()
	b.initErr = errors.New("init failed")
	b.mu.Unlock()

	ctx := context.Background()
	const iterations = 2000
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = b.WaitReady(ctx)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			b.mu.Lock()
			b.initErr = nil
			b.mu.Unlock()
			b.mu.Lock()
			b.initErr = errors.New("init failed")
			b.mu.Unlock()
		}
	}()
	wg.Wait()
}

// TestBuildModelRegistryOwnershipMatchesOutcome is the #17 regression guard:
// the per-session model registry must be registered exactly when Build hands
// the orchestrator back — never left registered on a failed Build (it would
// otherwise keep receiving every UpdateModelOverrides push forever), and
// never dropped early on a successful one — and released by the
// orchestrator's cleanup hook.
func TestBuildModelRegistryOwnershipMatchesOutcome(t *testing.T) {
	cfg := &BuilderConfig{
		LLM: BuilderLLMConfig{
			DefaultModel: "local-model",
			ProviderConfigs: map[string]BuilderProviderConfig{
				"openai": {ProviderType: "openai", Models: []string{"local-model"}},
			},
			Retry: BuilderRetryConfig{MaxRetries: 1, InitialBackoff: "1s", MaxBackoff: "10s"},
		},
		Security: BuilderSecurityConfig{},
		Orchestration: BuilderOrchestrationConfig{
			MaxDependencyContextChars: 8000,
			MaxJudgeCacheSize:         1000,
		},
		Executor: BuilderExecutorConfig{
			MaxRetries:         2,
			OutputTokenReserve: 1024,
			Compaction: BuilderCompactionConfig{
				SlidingWindow: BuilderSlidingWindow{KeepFirst: 3, KeepLast: 10},
				Thresholds:    BuilderCompactionThresholds{PredictivePercent: 80, WarningPercent: 90, EmergencyPercent: 95},
			},
			ToolResultBudget:  BuilderToolResultBudget{HardCapTokens: 32000, MaxFillFraction: 0.4},
			ToolOutputPruning: BuilderToolOutputPruning{KeepLastN: 5},
		},
		Timeouts:      BuilderTimeoutsConfig{BashMaxTimeout: 120, WebFetchProxyTimeout: 30},
		ToolLimits:    BuilderToolLimitsConfig{ReadDefaultLines: 100},
		ExpandEnvVars: func(s string) string { return s },
	}

	b, err := NewOrchestratorBuilder(cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewOrchestratorBuilder failed: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := b.waitReady(ctx); err != nil {
			t.Errorf("waitReady = %v, want completed init", err)
		}
		if err := b.waitMCPReady(ctx); err != nil {
			t.Errorf("waitMCPReady = %v, want completed init", err)
		}
	})

	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.WaitReady(waitCtx); err != nil {
		t.Fatalf("WaitReady = %v, want offline init to complete", err)
	}

	orch, buildErr := b.Build(cfg, nil, slog.New(slog.DiscardHandler), "", nil, nil, nil, nil)
	b.mu.RLock()
	live := len(b.sessionModelRegistries)
	b.mu.RUnlock()

	if buildErr != nil {
		// Failed Build: the registry must have been released again.
		if live != 0 {
			t.Fatalf("failed Build (%v) left %d live session model registries, want 0", buildErr, live)
		}
		return
	}
	if orch == nil {
		t.Fatal("Build returned a nil orchestrator without an error")
	}
	if live != 1 {
		t.Fatalf("successful Build: live session model registries = %d, want 1 (ownership must survive until cleanup)", live)
	}

	orch.Cleanup()
	b.mu.RLock()
	live = len(b.sessionModelRegistries)
	b.mu.RUnlock()
	if live != 0 {
		t.Fatalf("after orchestrator Cleanup: live session model registries = %d, want 0", live)
	}
}

// TestListAnthropicModels_FallbackOnError verifies that an error response is
// returned (the caller — ListProviderModels — maps it to the built-in list).
func TestListAnthropicModels_FallbackOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := listAnthropicModels(context.Background(), srv.URL, "proxy-key", nil)
	if err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
}

// TestListAnthropicModels_MalformedBody verifies a non-JSON body errors out.
func TestListAnthropicModels_MalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	_, err := listAnthropicModels(context.Background(), srv.URL, "", nil)
	if err == nil {
		t.Fatal("expected error for malformed response, got nil")
	}
}

// TestDedupeModelNames verifies the dedup helper directly: duplicates are
// removed, first-occurrence order is preserved, and short lists pass through.
func TestDedupeModelNames(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty", []string{}, []string{}},
		{"single", []string{"m"}, []string{"m"}},
		{"no dups", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"adjacent dups", []string{"a", "a", "b"}, []string{"a", "b"}},
		{"scattered dups keep first order", []string{"b", "a", "b", "c", "a"}, []string{"b", "a", "c"}},
		{"all same", []string{"x", "x", "x"}, []string{"x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedupeModelNames(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("dedupeModelNames(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestListProviderModels_DeduplicatesOpenAI verifies that ListProviderModels
// collapses duplicate model IDs reported by an OpenAI-compatible endpoint
// (LM Studio and multi-tenant gateways legitimately repeat entries) before
// the list reaches the settings UI.
func TestListProviderModels_DeduplicatesOpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m-b"},{"id":"m-a"},{"id":"m-b"},{"id":"m-a"}]}`))
	}))
	defer srv.Close()

	cfg := &BuilderConfig{
		LLM: BuilderLLMConfig{
			ProviderConfigs: map[string]BuilderProviderConfig{
				"local": {ProviderType: "openai", BaseURL: srv.URL},
			},
		},
	}
	b, err := NewOrchestratorBuilder(cfg, nil, nil, expectedCoreLogger(t, expectedStartupDiagnostic(), expectedCoreDiagnostic{"WARN", "rebuildJudge: failed to build LLM router for judge", map[string]string{"error": "no providers configured"}}, expectedCoreDiagnostic{"WARN", "tool judge rebuild failed: judge will not be available for on-demand evaluation", nil}))
	if err != nil {
		t.Fatalf("NewOrchestratorBuilder failed: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := b.waitReady(ctx); err != nil {
			t.Errorf("waitReady(empty providers) = %v, want completed init", err)
		}
		if err := b.waitMCPReady(ctx); err != nil {
			t.Errorf("waitMCPReady(empty config) = %v, want completed init", err)
		}
	})

	names, err := b.ListProviderModels(context.Background(), "local", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"m-a", "m-b"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v (duplicates must be collapsed)", names, want)
	}
}

// TestListProviderModels_DeduplicatesAnthropicCompatible verifies the same
// guarantee for anthropic-compatible endpoints: the /v1/models response may
// repeat an ID, the UI list must not.
func TestListProviderModels_DeduplicatesAnthropicCompatible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-x"},{"id":"claude-x"},{"id":"claude-y"}]}`))
	}))
	defer srv.Close()

	cfg := &BuilderConfig{
		LLM: BuilderLLMConfig{
			ProviderConfigs: map[string]BuilderProviderConfig{
				"proxy": {ProviderType: "anthropic", BaseURL: srv.URL},
			},
		},
	}
	b, err := NewOrchestratorBuilder(cfg, nil, nil, expectedCoreLogger(t, expectedStartupDiagnostic(), expectedCoreDiagnostic{"WARN", "rebuildJudge: failed to build LLM router for judge", map[string]string{"error": "no providers configured"}}, expectedCoreDiagnostic{"WARN", "tool judge rebuild failed: judge will not be available for on-demand evaluation", nil}))
	if err != nil {
		t.Fatalf("NewOrchestratorBuilder failed: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := b.waitReady(ctx); err != nil {
			t.Errorf("waitReady(empty providers) = %v, want completed init", err)
		}
		if err := b.waitMCPReady(ctx); err != nil {
			t.Errorf("waitMCPReady(empty config) = %v, want completed init", err)
		}
	})

	names, err := b.ListProviderModels(context.Background(), "proxy", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"claude-x", "claude-y"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v (duplicates must be collapsed)", names, want)
	}
}

// TestBuildSessionAgentManager_Discovery verifies that buildSessionAgentManager
// (the per-session wiring called from Build()) discovers project-local
// Subagent Profiles from `<workspace>/.agents/agents/<name>/AGENT.md`. This
// locks in the runtime wiring: without it, Build() would leave
// OrchestratorDeps.AgentManager nil and the feature dead at runtime even
// though unit tests pass.
func TestBuildSessionAgentManager_Discovery(t *testing.T) {
	ws := t.TempDir()
	agentDir := filepath.Join(ws, AgentsRelativePath, "builder-test-agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	agentMD := "---\nname: builder-test-agent\ndescription: A test agent for builder wiring.\n---\nTest body.\n"
	if err := os.WriteFile(filepath.Join(agentDir, "AGENT.md"), []byte(agentMD), 0o644); err != nil {
		t.Fatalf("write AGENT.md: %v", err)
	}

	b := &OrchestratorBuilder{}
	mgr := b.buildSessionAgentManager(ws, nil)
	if mgr == nil {
		t.Fatal("buildSessionAgentManager returned nil for a workspace with .agents/agents")
	}
	discovered, ok := mgr.Get("builder-test-agent")
	if !ok {
		t.Fatal("project-local agent not discovered by buildSessionAgentManager")
	}
	if discovered.Metadata.Description != "A test agent for builder wiring." {
		t.Errorf("unexpected description: %q", discovered.Metadata.Description)
	}
}

// TestBuildSessionAgentManager_NoDirsReturnsNil verifies the nil-safe
// contract: with no project workspace and no base dirs, the manager is nil
// (no spurious empty AgentManager, no crash downstream).
func TestBuildSessionAgentManager_NoDirsReturnsNil(t *testing.T) {
	b := &OrchestratorBuilder{} // no baseAgentDirs, no workspace
	if mgr := b.buildSessionAgentManager("", nil); mgr != nil {
		t.Fatalf("expected nil manager with no dirs, got %v", mgr)
	}
}

// --- extractCommitMessage tests ---

func TestExtractCommitMessage_FromContent(t *testing.T) {
	resp := &llm.ChatResponse{
		Message: llm.Message{Content: "feat(api): add rate limiting"},
	}
	got := extractCommitMessage(resp)
	if got != "feat(api): add rate limiting" {
		t.Errorf("extractCommitMessage() = %q, want %q", got, "feat(api): add rate limiting")
	}
}

func TestExtractCommitMessage_FallbackToReasoningContent(t *testing.T) {
	resp := &llm.ChatResponse{
		Message: llm.Message{
			Content:          "",
			ReasoningContent: "fix(db): resolve connection leak",
		},
	}
	got := extractCommitMessage(resp)
	if got != "fix(db): resolve connection leak" {
		t.Errorf("extractCommitMessage() = %q, want %q", got, "fix(db): resolve connection leak")
	}
}

func TestExtractCommitMessage_FallbackToReasoning(t *testing.T) {
	resp := &llm.ChatResponse{
		Message:   llm.Message{Content: ""},
		Reasoning: "docs(readme): update install instructions",
	}
	got := extractCommitMessage(resp)
	if got != "docs(readme): update install instructions" {
		t.Errorf("extractCommitMessage() = %q, want %q", got, "docs(readme): update install instructions")
	}
}

func TestExtractCommitMessage_StripsReasoningPrefix(t *testing.T) {
	resp := &llm.ChatResponse{
		Message: llm.Message{
			Content: "Based on my analysis: feat(auth): add token refresh",
		},
	}
	got := extractCommitMessage(resp)
	if got != "feat(auth): add token refresh" {
		t.Errorf("extractCommitMessage() = %q, want %q", got, "feat(auth): add token refresh")
	}
}

func TestExtractCommitMessage_StripsMarkdownFence(t *testing.T) {
	resp := &llm.ChatResponse{
		Message: llm.Message{
			Content: "```\nfeat(api): add rate limiting\n```",
		},
	}
	got := extractCommitMessage(resp)
	if got != "feat(api): add rate limiting" {
		t.Errorf("extractCommitMessage() = %q, want %q", got, "feat(api): add rate limiting")
	}
}

func TestExtractCommitMessage_AllEmpty(t *testing.T) {
	resp := &llm.ChatResponse{
		Message: llm.Message{Content: ""},
	}
	got := extractCommitMessage(resp)
	if got != "" {
		t.Errorf("extractCommitMessage() = %q, want empty", got)
	}
}

func TestExtractCommitMessage_NilResponse(t *testing.T) {
	got := extractCommitMessage(nil)
	if got != "" {
		t.Errorf("extractCommitMessage(nil) = %q, want empty", got)
	}
}

func TestExtractCommitMessage_PrefersContentOverReasoning(t *testing.T) {
	resp := &llm.ChatResponse{
		Message: llm.Message{
			Content:          "feat(ui): update button",
			ReasoningContent: "fix(db): resolve leak",
		},
	}
	got := extractCommitMessage(resp)
	if got != "feat(ui): update button" {
		t.Errorf("extractCommitMessage() = %q, want %q", got, "feat(ui): update button")
	}
}

// --- isValidConventionalCommit tests ---

func TestIsValidConventionalCommit(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{"feat without scope", "feat: add new feature", true},
		{"fix with scope", "fix(auth): resolve token leak", true},
		{"docs", "docs(readme): update installation", true},
		{"refactor", "refactor: extract validation logic", true},
		{"perf", "perf(db): optimize query", true},
		{"test", "test: add unit tests for auth", true},
		{"build", "build: update dependencies", true},
		{"ci", "ci: add GitHub Actions workflow", true},
		{"chore", "chore: clean up unused imports", true},
		{"revert", "revert: undo previous commit", true},
		{"style", "style: fix formatting", true},
		{"missing colon", "feat add feature", false},
		{"missing description", "feat:", false},
		{"invalid type", "invalid: something", false},
		{"empty string", "", false},
		{"just type prefix", "feat", false},
		{"with body", "feat: add feature\n\nThis is the body.", true},
		{"uppercase type", "Feat: add feature", false},
		{"uppercase description", "feat: Add New Feature", false},
		{"reasoning prefix", "Based on my analysis: feat: add feature", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isValidConventionalCommit(tt.msg); got != tt.want {
				t.Errorf("isValidConventionalCommit(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

// --- GenerateCommitMessage request tests ---

func TestBuildCommitMessageRequest_OneshotPolicy(t *testing.T) {
	const (
		diff            = "diff --git a/file.go b/file.go"
		reasoningEffort = "Off" // resolved per active model by the caller
	)

	req := buildCommitMessageRequest(diff, reasoningEffort)

	if req.Temperature == nil || *req.Temperature != oneshotTempCommit {
		t.Fatalf("Temperature = %v, want the oneshot service policy pin %v", req.Temperature, oneshotTempCommit)
	}
	if req.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %d, want 2048", req.MaxTokens)
	}
	if req.ReasoningEffort != reasoningEffort {
		t.Errorf("ReasoningEffort = %q, want %q", req.ReasoningEffort, reasoningEffort)
	}
	if req.CallPurpose != llm.CallPurposeSummarization {
		t.Errorf("CallPurpose = %q, want %q", req.CallPurpose, llm.CallPurposeSummarization)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(req.Messages))
	}
	if req.Messages[0].Role != "system" || req.Messages[0].Content == "" {
		t.Errorf("system message = %+v, want non-empty system prompt", req.Messages[0])
	}
	if req.Messages[1].Role != "user" || req.Messages[1].Content != "## Staged Diff\n\n"+diff {
		t.Errorf("user message = %+v, want role=user content=%q", req.Messages[1], "## Staged Diff\n\n"+diff)
	}
}

// --- GenerateCommitMessage retry loop test ---

// TestGenerateCommitMessage_RetryLoop verifies the client-owned oneshot retry
// loop: when a response is not a valid Conventional Commits message (or
// carries no usable text), the oneshot client nudges with the assistant echo
// plus a "[System]" format restatement and gives up after exactly 3 attempts;
// the refusal error keeps the operator-facing advice.
func TestGenerateCommitMessage_RetryLoop(t *testing.T) {
	tests := []struct {
		name           string
		responses      []*llm.ChatResponse
		wantErr        bool
		wantErrContain string
	}{
		{
			name: "valid message on first attempt",
			responses: []*llm.ChatResponse{
				{Message: llm.Message{Content: "feat(api): add rate limiting"}},
			},
			wantErr: false,
		},
		{
			name: "invalid then valid on retry",
			responses: []*llm.ChatResponse{
				{Message: llm.Message{Content: "Here is the commit message:\nfeat(api): add rate limiting"}},
				{Message: llm.Message{Content: "feat(api): add rate limiting"}},
			},
			wantErr: false,
		},
		{
			name: "invalid then valid on second retry",
			responses: []*llm.ChatResponse{
				{Message: llm.Message{Content: "feat: Add New Feature"}},         // uppercase desc
				{Message: llm.Message{Content: "fix(auth): Resolve token leak"}}, // uppercase desc
				{Message: llm.Message{Content: "fix(auth): resolve token leak"}},
			},
			wantErr: false,
		},
		{
			name: "all attempts invalid",
			responses: []*llm.ChatResponse{
				{Message: llm.Message{Content: "feat: Add New Feature"}},
				{Message: llm.Message{Content: "feat: Another Uppercase Message"}},
				{Message: llm.Message{Content: "feat: Yet Another One"}},
			},
			wantErr:        true,
			wantErrContain: "invalid commit message after multiple attempts",
		},
		{
			name: "empty content rides the nudge loop then refuses",
			responses: []*llm.ChatResponse{
				{Message: llm.Message{Content: ""}},
			},
			wantErr:        true,
			wantErrContain: "empty commit message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockLLMCaller{responses: tt.responses}
			var diagnostics []expectedCoreDiagnostic
			if tt.wantErr {
				if tt.wantErrContain == "empty commit message" {
					for i := 0; i < 3; i++ {
						stop := "end_turn"
						if i == 0 {
							stop = ""
						}
						diagnostics = append(diagnostics, expectedCoreDiagnostic{"WARN", "commit message generation produced no usable output", map[string]string{"diff_bytes": "0", "provider": "test-provider", "stop_reason": stop, "has_reasoning": "false"}})
					}
				}
				diagnostics = append(diagnostics, expectedServiceDiagnostic("commit_message", "", "error", 3), expectedCoreDiagnostic{"WARN", "commit message generation failed validation after all retries", map[string]string{"diff_bytes": "0", "provider": "test-provider"}})
			}

			b := &OrchestratorBuilder{
				logger: expectedCoreLogger(t, diagnostics...),
				mu:     sync.RWMutex{},
			}

			msg, err := b.generateCommitMessageWithCaller(context.Background(), mock, "test-provider", "", "")

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrContain)
				}
				if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantErrContain)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErrContain != "" && !strings.Contains(msg, tt.wantErrContain) {
				t.Errorf("message = %q, want to contain %q", msg, tt.wantErrContain)
			}
		})
	}
}

// TestGenerateCommitMessage_OneshotNudgeShape pins the client-owned nudge
// convention on the commit-message path: the retried request carries the
// model's failed output as an assistant echo followed by a "[System]" user
// message restating the Conventional Commits contract, and the loop stops
// after exactly three attempts.
func TestGenerateCommitMessage_OneshotNudgeShape(t *testing.T) {
	mock := &mockLLMCaller{responses: []*llm.ChatResponse{
		{Message: llm.Message{Content: "feat: Add New Feature"}},
		{Message: llm.Message{Content: "feat: Another Uppercase Message"}},
		{Message: llm.Message{Content: "fix(auth): resolve token leak"}},
	}}

	b := &OrchestratorBuilder{logger: slog.Default(), mu: sync.RWMutex{}}
	msg, err := b.generateCommitMessageWithCaller(context.Background(), mock, "test-provider", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != "fix(auth): resolve token leak" {
		t.Errorf("message = %q, want %q", msg, "fix(auth): resolve token leak")
	}
	if len(mock.calls) != 3 {
		t.Fatalf("call count = %d, want 3 (first attempt + two nudges)", len(mock.calls))
	}

	retry := mock.calls[1]
	if len(retry.Messages) != 4 {
		t.Fatalf("retry request has %d messages, want 4 ([system, user, assistant echo, user nudge])", len(retry.Messages))
	}
	echo, nudge := retry.Messages[2], retry.Messages[3]
	if echo.Role != "assistant" || echo.Content != "feat: Add New Feature" {
		t.Errorf("echo = (%q, %q), want assistant role carrying the model's own failed output", echo.Role, echo.Content)
	}
	if nudge.Role != "user" || !strings.HasPrefix(nudge.Content, "[System]") {
		t.Errorf("nudge = (%q, %q), want a user message starting with %q", nudge.Role, nudge.Content, "[System]")
	}
	if !strings.Contains(nudge.Content, "MUST start with a valid type prefix") {
		t.Errorf("nudge %q must restate the Conventional Commits contract", nudge.Content)
	}
	// The retried request must keep the oneshot service policy intact.
	if retry.Temperature == nil || *retry.Temperature != oneshotTempCommit {
		t.Errorf("retry Temperature = %v, want the policy pin %v", retry.Temperature, oneshotTempCommit)
	}
}

// TestGenerateCommitMessage_TransportErrorNotRetried pins the oneshot
// contract that transport failures are never retried by the client — the
// Router owns provider retry — so exactly one call is made.
func TestGenerateCommitMessage_TransportErrorNotRetried(t *testing.T) {
	boom := errors.New("provider unavailable")
	mock := &mockLLMCaller{err: boom}

	b := &OrchestratorBuilder{logger: expectedCoreLogger(t, expectedServiceDiagnostic("commit_message", "", "transport_error", 1), expectedCoreDiagnostic{"ERROR", "commit message generation failed: LLM call error", map[string]string{"err": "provider unavailable", "diff_bytes": "0", "provider": "test-provider"}}), mu: sync.RWMutex{}}
	if _, err := b.generateCommitMessageWithCaller(context.Background(), mock, "test-provider", "", ""); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the transport error passed through as-is", err)
	}
	if len(mock.calls) != 1 {
		t.Errorf("call count = %d, want 1 (transport errors are never retried by the client)", len(mock.calls))
	}
}

// --- extractOptimizedPrompt tests ---

func TestExtractOptimizedPrompt_PrefersMarkersOverContent(t *testing.T) {
	// Even when Content has dirty text, markers in ReasoningContent should win.
	resp := &llm.ChatResponse{
		Message: llm.Message{
			Content:          "Some preamble text that should be ignored",
			ReasoningContent: "### OPTIMIZED_PROMPT_START\nExtracted from reasoning\n### OPTIMIZED_PROMPT_END",
		},
	}
	got := extractOptimizedPrompt(resp)
	if got != "Extracted from reasoning" {
		t.Errorf("extractOptimizedPrompt() = %q, want %q", got, "Extracted from reasoning")
	}
}

func TestExtractOptimizedPrompt_MarkersInContent(t *testing.T) {
	resp := &llm.ChatResponse{
		Message: llm.Message{
			Content: "### OPTIMIZED_PROMPT_START\nClean prompt from content\n### OPTIMIZED_PROMPT_END",
		},
	}
	got := extractOptimizedPrompt(resp)
	if got != "Clean prompt from content" {
		t.Errorf("extractOptimizedPrompt() = %q, want %q", got, "Clean prompt from content")
	}
}

func TestExtractOptimizedPrompt_MarkersInReasoning(t *testing.T) {
	// Content is empty, markers are in ReasoningContent.
	resp := &llm.ChatResponse{
		Message: llm.Message{
			Content:          "",
			ReasoningContent: "### OPTIMIZED_PROMPT_START\nPrompt from reasoning\n### OPTIMIZED_PROMPT_END",
		},
	}
	got := extractOptimizedPrompt(resp)
	if got != "Prompt from reasoning" {
		t.Errorf("extractOptimizedPrompt() = %q, want %q", got, "Prompt from reasoning")
	}
}

func TestExtractOptimizedPrompt_MarkersInReasoningField(t *testing.T) {
	// Content and ReasoningContent empty, markers in Reasoning field.
	resp := &llm.ChatResponse{
		Message:   llm.Message{Content: "", ReasoningContent: ""},
		Reasoning: "### OPTIMIZED_PROMPT_START\nPrompt from Reasoning field\n### OPTIMIZED_PROMPT_END",
	}
	got := extractOptimizedPrompt(resp)
	if got != "Prompt from Reasoning field" {
		t.Errorf("extractOptimizedPrompt() = %q, want %q", got, "Prompt from Reasoning field")
	}
}

func TestExtractOptimizedPrompt_FallbackToHeuristic(t *testing.T) {
	// No markers anywhere — should fall back to heuristic (strip prefix).
	// The regex matches "the optimized prompt:" (colon directly after prompt).
	resp := &llm.ChatResponse{
		Message: llm.Message{
			Content: "the optimized prompt: Fix the login bug",
		},
	}
	got := extractOptimizedPrompt(resp)
	if got != "Fix the login bug" {
		t.Errorf("extractOptimizedPrompt() = %q, want %q", got, "Fix the login bug")
	}
}

func TestExtractOptimizedPrompt_FallbackToHeuristic_NoMarkersInReasoning(t *testing.T) {
	// Content is empty, ReasoningContent has a pattern the regex matches.
	// `ok,? ` matches "ok, " at the start.
	resp := &llm.ChatResponse{
		Message: llm.Message{
			Content:          "",
			ReasoningContent: "ok, fix the auth module",
		},
	}
	got := extractOptimizedPrompt(resp)
	if got != "fix the auth module" {
		t.Errorf("extractOptimizedPrompt() = %q, want %q", got, "fix the auth module")
	}
}

func TestExtractOptimizedPrompt_MarkersEmptyContent(t *testing.T) {
	// Markers present but content between them is empty — should return empty.
	resp := &llm.ChatResponse{
		Message: llm.Message{
			Content: "### OPTIMIZED_PROMPT_START\n\n### OPTIMIZED_PROMPT_END",
		},
	}
	got := extractOptimizedPrompt(resp)
	if got != "" {
		t.Errorf("extractOptimizedPrompt() = %q, want empty", got)
	}
}

func TestExtractOptimizedPrompt_MultiLinePrompt(t *testing.T) {
	resp := &llm.ChatResponse{
		Message: llm.Message{
			Content: "### OPTIMIZED_PROMPT_START\nFix the login bug\n\nSteps:\n1. Check auth middleware\n2. Fix token validation\n\nBe specific about file paths.\n### OPTIMIZED_PROMPT_END",
		},
	}
	got := extractOptimizedPrompt(resp)
	want := "Fix the login bug\n\nSteps:\n1. Check auth middleware\n2. Fix token validation\n\nBe specific about file paths."
	if got != want {
		t.Errorf("extractOptimizedPrompt() = %q, want %q", got, want)
	}
}

func TestExtractOptimizedPrompt_PrefersContentMarkersOverReasoning(t *testing.T) {
	// Markers in both Content and ReasoningContent — Content should win.
	resp := &llm.ChatResponse{
		Message: llm.Message{
			Content:          "### OPTIMIZED_PROMPT_START\nFrom content\n### OPTIMIZED_PROMPT_END",
			ReasoningContent: "### OPTIMIZED_PROMPT_START\nFrom reasoning\n### OPTIMIZED_PROMPT_END",
		},
	}
	got := extractOptimizedPrompt(resp)
	if got != "From content" {
		t.Errorf("extractOptimizedPrompt() = %q, want %q", got, "From content")
	}
}

func TestExtractOptimizedPrompt_NilResponse(t *testing.T) {
	got := extractOptimizedPrompt(nil)
	if got != "" {
		t.Errorf("extractOptimizedPrompt(nil) = %q, want empty", got)
	}
}

func TestExtractOptimizedPrompt_AllEmpty(t *testing.T) {
	resp := &llm.ChatResponse{
		Message: llm.Message{Content: ""},
	}
	got := extractOptimizedPrompt(resp)
	if got != "" {
		t.Errorf("extractOptimizedPrompt() = %q, want empty", got)
	}
}

// TestApplyProviderOutputReserves covers the per-provider output-token reserve
// seeding (D4): per-model overrides win, per-provider values apply to every
// listed model, unset/zero/negative provider values are ignored, and existing
// partial overrides are enriched rather than clobbered.
func TestApplyProviderOutputReserves(t *testing.T) {
	overrides := map[string]llm.ModelMetadata{
		// Explicit per-model override — must never be clobbered.
		"model-with-explicit": {ContextWindow: 100000, OutputLimit: 2048},
		// Partial override without OutputLimit — reserve must be added,
		// other fields preserved.
		"model-partial": {ContextWindow: 32000},
	}
	providerConfigs := map[string]BuilderProviderConfig{
		"gateway": {
			ProviderType:       "openai",
			Models:             []string{"model-a", "model-with-explicit", "model-partial", ""},
			OutputTokenReserve: 8192,
		},
		"unset-provider": {
			ProviderType: "openai",
			Models:       []string{"model-b"},
		},
		"negative-provider": {
			ProviderType:       "openai",
			Models:             []string{"model-c"},
			OutputTokenReserve: -1,
		},
	}

	applyProviderOutputReserves(overrides, providerConfigs)

	if got := overrides["model-a"].OutputLimit; got != 8192 {
		t.Errorf("model-a OutputLimit = %d, want 8192 (seeded from provider)", got)
	}
	if got := overrides["model-with-explicit"].OutputLimit; got != 2048 {
		t.Errorf("model-with-explicit OutputLimit = %d, want 2048 (per-model wins)", got)
	}
	partial := overrides["model-partial"]
	if partial.OutputLimit != 8192 {
		t.Errorf("model-partial OutputLimit = %d, want 8192 (seeded)", partial.OutputLimit)
	}
	if partial.ContextWindow != 32000 {
		t.Errorf("model-partial ContextWindow = %d, want 32000 (preserved)", partial.ContextWindow)
	}
	if _, ok := overrides["model-b"]; ok {
		t.Error("model-b should not be seeded when provider reserve is unset")
	}
	if _, ok := overrides["model-c"]; ok {
		t.Error("model-c should not be seeded when provider reserve is negative")
	}
}

// TestApplyProviderOutputReserves_ConflictingProvidersDeterministic: when two
// providers list the same bare model name with different reserves, the
// lexicographically first provider name must win — regardless of Go's
// randomized map iteration order (verified over repeated runs).
func TestApplyProviderOutputReserves_ConflictingProvidersDeterministic(t *testing.T) {
	providerConfigs := map[string]BuilderProviderConfig{
		"zzz-gateway": {
			ProviderType:       "openai",
			Models:             []string{"shared-model"},
			OutputTokenReserve: 4096,
		},
		"aaa-gateway": {
			ProviderType:       "openai",
			Models:             []string{"shared-model"},
			OutputTokenReserve: 8192,
		},
	}

	for i := 0; i < 50; i++ {
		overrides := map[string]llm.ModelMetadata{}
		applyProviderOutputReserves(overrides, providerConfigs)
		if got := overrides["shared-model"].OutputLimit; got != 8192 {
			t.Fatalf("run %d: shared-model OutputLimit = %d, want 8192 (aaa-gateway wins, lexicographic order)", i, got)
		}
	}
}

// TestRegisterVectorSearch_StoresWaitTimeout verifies the RAG-hint bound
// travels with the vector-search registration: the waitTimeout argument is
// stored for per-session OrchestratorDeps.VectorSearchWaitTimeout, and a nil
// searchFunc remains a no-op.
func TestRegisterVectorSearch_StoresWaitTimeout(t *testing.T) {
	cfg := &BuilderConfig{
		Security:      BuilderSecurityConfig{},
		ExpandEnvVars: func(string) string { return "" },
	}
	b, err := NewOrchestratorBuilder(cfg, nil, nil, expectedCoreLogger(t, expectedStartupDiagnostic(), expectedCoreDiagnostic{"WARN", "rebuildJudge: failed to build LLM router for judge", map[string]string{"error": "no providers configured"}}, expectedCoreDiagnostic{"WARN", "tool judge rebuild failed: judge will not be available for on-demand evaluation", nil}))
	if err != nil {
		t.Fatalf("NewOrchestratorBuilder: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := b.waitReady(ctx); err != nil {
			t.Errorf("waitReady(empty providers) = %v, want completed init", err)
		}
		if err := b.waitMCPReady(ctx); err != nil {
			t.Errorf("waitMCPReady(empty config) = %v, want completed init", err)
		}
	})

	search := builtins.VectorSearchFunc(func(ctx context.Context, opts builtins.VectorSearchOptions) ([]builtins.VectorSearchResult, error) {
		return nil, nil
	})
	wait := builtins.VectorSearchWaitFunc(func(context.Context) error { return nil })
	b.RegisterVectorSearch(search, wait, 2*time.Second, false)

	b.mu.RLock()
	gotFunc := b.vectorSearchFunc
	gotWait := b.vectorSearchWaitFunc
	gotTimeout := b.vectorSearchWaitTimeout
	b.mu.RUnlock()

	if gotFunc == nil {
		t.Fatal("expected vectorSearchFunc to be stored")
	}
	if gotWait == nil {
		t.Fatal("expected vectorSearchWaitFunc to be stored (the RAG-hint path calls it under the shared deadline)")
	}
	if gotTimeout != 2*time.Second {
		t.Errorf("vectorSearchWaitTimeout = %v, want 2s", gotTimeout)
	}

	// nil searchFunc must stay a no-op (no panic, no clearing).
	b.RegisterVectorSearch(nil, nil, time.Minute, false)
	b.mu.RLock()
	gotFunc = b.vectorSearchFunc
	b.mu.RUnlock()
	if gotFunc == nil {
		t.Error("nil searchFunc must not clear a previously registered search func")
	}
}
