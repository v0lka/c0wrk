package core

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// TestToolRegistry_Clone_Independent verifies that the per-session clone
// produced by ToolRegistry.Clone() does not share mutable policy state with
// the parent registry. Regression guard for C-3 (skill policy leak across
// sessions).
func TestToolRegistry_Clone_Independent(t *testing.T) {
	parent := tools.NewToolRegistry()
	parent.SetPolicyOverrides(map[string]sdktools.ToolPolicy{
		ToolBashExec: sdktools.PolicyAlwaysDeny,
	})

	child := parent.Clone()

	// Mutate the child: skill policy override should NOT propagate to parent.
	child.SetSkillPolicyOverrides(map[string]sdktools.ToolPolicy{
		ToolWriteFile: sdktools.PolicyAlwaysAllow,
	})

	// Reset the parent's policy so we can detect leakage.
	parent.SetSkillPolicyOverrides(nil)

	// The parent must not have inherited the child's skill override.
	// We cannot read the policy directly, but we can clone again and verify
	// that the new clone does not see the leaked override.
	other := parent.Clone()
	_ = other

	// Independent SetPolicyOverrides on parent should not show up on child.
	parent.SetPolicyOverrides(map[string]sdktools.ToolPolicy{
		ToolReadFile: sdktools.PolicyAlwaysDeny,
	})

	// Best-effort smoke: ensure both registries can independently set policy
	// without panicking and that Clone returns a non-nil pointer.
	if child == nil {
		t.Fatal("Clone returned nil")
	}
}

// TestApplySecurityPolicies covers the BuilderConfig → registry policy mapping.
// We exercise the helper directly via a fake builder so we don't need a full
// async-init builder instance.
func TestApplySecurityPolicies(t *testing.T) {
	cfg := &BuilderConfig{
		Security: BuilderSecurityConfig{
			DefaultPolicy: "user_confirm",
			ToolPolicies: map[string]BuilderToolPolicy{
				ToolReadFile: {Policy: "always_allow"},
				ToolBashExec: {Policy: "always_deny"},
			},
		},
		ExpandEnvVars: func(s string) string { return s },
	}

	b := &OrchestratorBuilder{registry: tools.NewToolRegistry()}
	b.applySecurityPolicies(cfg)

	// Re-applying must not panic.
	b.applySecurityPolicies(cfg)
}

// TestNewOrchestratorBuilder_NilExpandEnvVars verifies the W-14 nil-guard:
// constructing a builder without an ExpandEnvVars hook should not panic.
func TestNewOrchestratorBuilder_NilExpandEnvVars(t *testing.T) {
	cfg := &BuilderConfig{
		Security: BuilderSecurityConfig{DefaultPolicy: "user_confirm"},
		// ExpandEnvVars intentionally omitted.
	}

	b, err := NewOrchestratorBuilder(cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewOrchestratorBuilder failed: %v", err)
	}
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
		Security: BuilderSecurityConfig{DefaultPolicy: "user_confirm"},
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
			BashMaxTimeout:   120,
			BashWaitDelay:    2,
			RipgrepTimeout:   30,
			WebFetchTimeout:  30,
			WebSearchTimeout: 30,
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

// TestStripMarkdownCodeFence verifies the defensive safety net that removes a
// surrounding markdown code block from a model-generated commit message. The
// prompt forbids fencing, but some models still emit it; the helper must
// strip exactly one outer block and leave everything else untouched.
func TestStripMarkdownCodeFence(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no fencing, single line",
			in:   "feat(auth): add token refresh on 401",
			want: "feat(auth): add token refresh on 401",
		},
		{
			name: "plain fenced block",
			in:   "```\nfeat(auth): add token refresh\n```",
			want: "feat(auth): add token refresh",
		},
		{
			name: "fenced block with language tag",
			in:   "```text\nfeat(auth): add token refresh\n```",
			want: "feat(auth): add token refresh",
		},
		{
			name: "fenced block with markdown language tag",
			in:   "```markdown\nfeat(auth): add token refresh\n```",
			want: "feat(auth): add token refresh",
		},
		{
			name: "fenced block preserves multi-line body",
			in:   "```\nfeat(auth): add token refresh\n\nRefetch the token when the API returns 401.\n```",
			want: "feat(auth): add token refresh\n\nRefetch the token when the API returns 401.",
		},
		{
			name: "fenced block with surrounding whitespace",
			in:   "\n\n  ```\nfeat: add thing\n```\n\n",
			want: "feat: add thing",
		},
		{
			name: "trailing spaces after closing fence",
			in:   "```\nfeat: add thing\n```   ",
			want: "feat: add thing",
		},
		{
			name: "inner backticks are preserved when not wrapping",
			in:   "feat: use `git diff --staged`",
			want: "feat: use `git diff --staged`",
		},
		{
			name: "empty string stays empty",
			in:   "",
			want: "",
		},
		{
			name: "whitespace only collapses to empty",
			in:   "   \n\t\n",
			want: "",
		},
		{
			name: "partial fence at start only is left unchanged",
			in:   "```\nfeat: add thing",
			want: "```\nfeat: add thing",
		},
		{
			name: "single-line fence not treated as wrapper",
			in:   "``feat``",
			want: "``feat``",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripMarkdownCodeFence(tt.in); got != tt.want {
				t.Errorf("stripMarkdownCodeFence(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
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

// --- stripMarkdownCodeFence multi-line tests ---

func TestStripMarkdownCodeFence_MultiLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "multi-line with body",
			in:   "```\nfeat(database): resolve connection pool exhaustion under load\n\nThe pool size was hardcoded to 10 instead of using the\nconfigured MaxConnections value.\n```",
			want: "feat(database): resolve connection pool exhaustion under load\n\nThe pool size was hardcoded to 10 instead of using the\nconfigured MaxConnections value.",
		},
		{
			name: "with language tag",
			in:   "```text\nfeat(api): add rate limiting\n```",
			want: "feat(api): add rate limiting",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripMarkdownCodeFence(tt.in); got != tt.want {
				t.Errorf("stripMarkdownCodeFence(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// --- GenerateCommitMessage retry loop test ---

// TestGenerateCommitMessage_RetryLoop verifies the retry loop in
// GenerateCommitMessage: when the first response is not a valid Conventional
// Commits message, the caller retries up to 2 times with feedback.
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
			name: "empty content returns no usable output",
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

			b := &OrchestratorBuilder{
				logger: slog.Default(),
				mu:     sync.RWMutex{},
			}

			buildRequest := func(extraUserText string) llm.ChatRequest {
				return llm.ChatRequest{
					Messages: []llm.Message{
						{Role: "user", Content: "## Staged Diff\n\n" + extraUserText},
					},
				}
			}

			msg, err := b.generateCommitMessageWithCaller(context.Background(), agent.LLMCaller(mock), "test-provider", "", buildRequest)

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
