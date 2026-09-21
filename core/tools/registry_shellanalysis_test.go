package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// capturingJudgeProvider snapshots every LLM request the judge issues, so
// tests can assert what the strict/advisory judge actually SAW in its prompt
// (the strict envelope is a JSON user message; the advisory user prompt is
// the templated markdown). Scripted with one response string.
type capturingJudgeProvider struct {
	mu       sync.Mutex
	response string
	err      error
	requests []llm.ChatRequest
}

func (p *capturingJudgeProvider) ChatCompletion(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	if p.err != nil {
		return nil, p.err
	}
	return &llm.ChatResponse{Message: llm.Message{Content: p.response}}, nil
}

func (p *capturingJudgeProvider) Name() string { return "capturing-judge" }

func (p *capturingJudgeProvider) snapshot() []llm.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]llm.ChatRequest, len(p.requests))
	copy(out, p.requests)
	return out
}

func (p *capturingJudgeProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

// strictEnvelopeField extracts a string field from the strict-judge envelope
// captured by the provider: the envelope is a JSON-object user message (see
// marshalStrictEnvelope in sp4rk); other messages (system prompt, prose) do
// not decode as flat objects and are skipped. The returned value is the
// UNQUOTED string (boundary wrappers stay verbatim). Returns ok=false when
// the field is absent in every message (omitempty contract).
func strictEnvelopeField(t *testing.T, reqs []llm.ChatRequest, field string) (string, bool) {
	t.Helper()
	for _, req := range reqs {
		for _, msg := range req.Messages {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(msg.Content), &fields); err != nil {
				continue
			}
			raw, ok := fields[field]
			if !ok {
				continue
			}
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatalf("strict envelope field %q is not a string: %s", field, raw)
			}
			return value, true
		}
	}
	return "", false
}

// newShellAnalysisRegistry builds a registry with smart approve ON, a strict
// judge backed by a capturing provider scripted with judgeResponse, and a
// confirm func that records whether the manual-confirmation path was reached
// (and denies, so no real command ever runs).
func newShellAnalysisRegistry(t *testing.T, judgeResponse string) (*ToolRegistry, *capturingJudgeProvider, *bool) {
	t.Helper()
	registry := NewToolRegistry()
	registry.SetAutonomyMode(AutonomyModeAssisted)
	provider := &capturingJudgeProvider{response: judgeResponse}
	registry.SetJudge(sdktools.NewToolJudge(provider, "test-model", 10, nil))

	confirmCalled := false
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmDeny, nil
	})
	return registry, provider, &confirmCalled
}

// marshalShellInput marshals a shell-exec tool input as JSON instead of
// hand-splicing host paths into a raw string literal: on Windows,
// t.TempDir() yields backslash-separated paths whose raw splice produces
// invalid JSON escape sequences ("\U", "\R", …), so the input fails object
// validation before any analysis runs. The command itself must carry the
// path QUOTED — an unquoted backslash path is a chain of bash escape
// characters, and the glued relative word the analyzer recovers resolves
// inside the workspace, so the outside-roots criterion never fires.
func marshalShellInput(t *testing.T, command, workDir string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"command": command, "working_directory": workDir})
	if err != nil {
		t.Fatalf("marshal shell input: %v", err)
	}
	return raw
}

// TestSmartApproveShellAnalysis_CleanCallReachesStrictJudge proves the
// registry runs the flowsh analysis ONCE per shell-tool call (Execute →
// AttachShellAnalysis) and forwards the digest to the strict judge even for
// a CLEAN call — one with no escalation reason at all (the tool's mock Judge
// reports nothing; the digest itself is the only static-analysis evidence).
// The digest must reflect the actual command (its criteria name the fired
// soft criterion), reach the envelope behind the untrusted-content boundary,
// and a strict ALLOW must execute without UI.
func TestSmartApproveShellAnalysis_CleanCallReachesStrictJudge(t *testing.T) {
	registry, provider, confirmCalled := newShellAnalysisRegistry(t, "VERDICT: ALLOW\nREASON: benign read")

	// Mock tool NAMED bash_exec (group execute) without a Judge: the call is
	// escalation-free under the effective user_confirm policy, so the ONLY
	// thing carrying analysis to the strict judge is the ctx attachment.
	bashMock := newMockTool("bash_exec", "mock shell")
	bashMock.group = sdktools.GroupExecute
	registry.Register(bashMock)

	ws := t.TempDir()
	outside := t.TempDir() // a second root: outside the workspace
	outsideNotes := outside + "/notes.md"
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	// Read of a non-system path outside the session roots: fires the SOFT
	// outside-roots criterion in the digest, but the mock tool consumes
	// nothing — the call itself stays escalation-free (clean).
	input := marshalShellInput(t, `cat "`+outsideNotes+`"`, ws)

	result, err := registry.Execute(ctx, "bash_exec", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected execution (strict ALLOW), got error result: %s", result.Content)
	}
	if *confirmCalled {
		t.Error("strict ALLOW must not reach the manual confirmation path")
	}
	if got := provider.callCount(); got != 1 {
		t.Fatalf("strict judge calls = %d, want 1", got)
	}

	analysis, ok := strictEnvelopeField(t, provider.snapshot(), "analysis")
	if !ok {
		t.Fatal("strict envelope lacks the analysis field: the flowsh digest must reach the strict judge for clean shell calls too")
	}
	if !strings.Contains(analysis, `"schemaVersion":"sp4rk-shell-analysis/v4"`) {
		t.Errorf("analysis field is not a shell-analysis digest: %s", analysis)
	}
	if !strings.Contains(analysis, "outside_session_roots") {
		t.Errorf("digest does not reflect the analyzed command (want the outside_session_roots criterion): %s", analysis)
	}
	if !strings.Contains(analysis, "shell_analysis") {
		t.Errorf("digest must sit behind the shell_analysis untrusted-content boundary: %s", analysis)
	}
}

// TestSmartApproveShellAnalysis_EscalatedShellCallReachesStrictJudge proves
// an ESCALATED shell call (a fired judge reason) still carries the digest to
// the strict judge — alongside the escalation reasoning, not instead of it.
func TestSmartApproveShellAnalysis_EscalatedShellCallReachesStrictJudge(t *testing.T) {
	registry, provider, confirmCalled := newShellAnalysisRegistry(t, "VERDICT: ALLOW\nREASON: scope is fine")

	registry.Register(newMockExecuteJudgerTool("bash_exec", sdktools.JudgeOutcome{
		Reason:     "Shell analysis: filesystem effect outside the session roots",
		Severity:   sdktools.JudgeSeveritySoft,
		ReasonCode: sdktools.ReasonCodeOutsideSessionRoots,
	}))

	ws := t.TempDir()
	outside := t.TempDir() // a second root: outside the workspace
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	input := marshalShellInput(t, `cat "`+outside+`/notes.md"`, ws)

	result, err := registry.Execute(ctx, "bash_exec", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected execution (strict ALLOW on a soft escalation), got error result: %s", result.Content)
	}
	if *confirmCalled {
		t.Error("strict ALLOW on a soft escalation must not reach the manual confirmation path")
	}

	analysis, ok := strictEnvelopeField(t, provider.snapshot(), "analysis")
	if !ok {
		t.Fatal("strict envelope lacks the analysis field for an escalated shell call")
	}
	if !strings.Contains(analysis, `"schemaVersion":"sp4rk-shell-analysis/v4"`) {
		t.Errorf("analysis field is not a shell-analysis digest: %s", analysis)
	}
	// The escalation reasoning must survive alongside the digest.
	reasoning, ok := strictEnvelopeField(t, provider.snapshot(), "judge_reasoning")
	if !ok || !strings.Contains(reasoning, "session roots") {
		t.Errorf("judge_reasoning = %q (ok=%v), want the escalation reason mentioning session roots", reasoning, ok)
	}
}

// TestSmartApproveShellAnalysis_NonShellToolOmitsAnalysis guards the
// omitempty contract from the other side: a non-shell tool's strict envelope
// must carry NO analysis field (there is no static analysis for it).
func TestSmartApproveShellAnalysis_NonShellToolOmitsAnalysis(t *testing.T) {
	registry, provider, _ := newShellAnalysisRegistry(t, "VERDICT: ALLOW\nREASON: benign")
	registry.Register(newMockTool("mutating", "a plain mutating tool"))

	if _, err := registry.Execute(context.Background(), "mutating", json.RawMessage(`{"input":"x"}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := strictEnvelopeField(t, provider.snapshot(), "analysis"); ok {
		t.Error("analysis field must be absent for a non-shell tool (no static analysis exists)")
	}
}

// TestSmartApproveCanonicalFlowshCodes_BackstopUnderAllowPolicy is the
// behavioral proof for the extended canonical set: a fired HARD flowsh
// control (exfiltration flow, privilege escalation, system write,
// destructive write outside the session roots, download cradle) must NOT
// pass Smart Approve even when the strict judge returns ALLOW — the
// deterministic backstop forces manual confirmation. The analyzer's ⊤
// limitation (command_unbounded_analysis) is the deliberate counter-example:
// hard but NON-canonical, so a strict ALLOW clears it.
func TestSmartApproveCanonicalFlowshCodes_BackstopUnderAllowPolicy(t *testing.T) {
	tests := []struct {
		name      string
		code      sdktools.JudgeReasonCode
		wantBlock bool
	}{
		{name: "exfiltration flow", code: sdktools.ReasonCodeCommandExfilFlow, wantBlock: true},
		{name: "privilege escalation", code: sdktools.ReasonCodeCommandPrivilegeEscalation, wantBlock: true},
		{name: "system write", code: sdktools.ReasonCodeCommandSystemWrite, wantBlock: true},
		{name: "destructive outside roots", code: sdktools.ReasonCodeCommandDestructiveOutsideRoots, wantBlock: true},
		{name: "download cradle", code: sdktools.ReasonCodeCommandDownloadCradle, wantBlock: true},
		{name: "unbounded analysis stays clearable", code: sdktools.ReasonCodeCommandUnboundedAnalysis, wantBlock: false},
		{name: "external-content ingest stays clearable", code: sdktools.ReasonCodeCommandExternalContentIngest, wantBlock: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, provider, confirmCalled := newShellAnalysisRegistry(t, "VERDICT: ALLOW\nREASON: looks safe to me")
			hardMock := newMockHardJudgerTool("bash_exec", "command fired a hard flowsh control", tt.code)
			hardMock.group = sdktools.GroupExecute
			registry.Register(hardMock)
			registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
				sdktools.GroupExecute: sdktools.PolicyAlwaysAllow,
			})

			result, err := registry.Execute(
				sdktools.WithWorkspacePath(context.Background(), t.TempDir()),
				"bash_exec", json.RawMessage(`{"command":"anything"}`))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// The strict judge must actually have run and returned ALLOW —
			// the block, if any, is the deterministic backstop's doing.
			if got := provider.callCount(); got != 1 {
				t.Fatalf("strict judge calls = %d, want 1", got)
			}

			if tt.wantBlock {
				if !*confirmCalled {
					t.Error("canonical hard flowsh control must NOT be auto-approved by a strict ALLOW; expected manual confirmation")
				}
				if !result.IsError {
					t.Error("denied confirmation must yield an error result (command must not execute)")
				}
				if !strings.Contains(result.Content, "cannot be waived") {
					t.Errorf("result must explain the backstop override, got: %s", result.Content)
				}
			} else {
				if *confirmCalled {
					t.Error("non-canonical hard reason (⊤ unbounded analysis) may be cleared by a strict ALLOW; no confirmation expected")
				}
				if result.IsError {
					t.Errorf("expected execution on strict ALLOW, got: %s", result.Content)
				}
			}
		})
	}
}

// TestAttachShellAnalysis_DoesNotSeedDSessionVarBinding pins the corrected,
// fail-closed contract for an UNASSIGNED $D
// (silent-mode-deny-accuracy-recommendations.md §2C): AttachShellAnalysis
// attaches NO host-known variable binding, because the executed shell is a
// fresh stateless `bash -c` where $D is genuinely unset — seeding `D` → the
// session temp dir would let the analyzer resolve an unassigned `$D` to an
// in-root target the command never actually writes (a fail-open), downgrading
// the fail-closed ⊤ escalation (the command_unbounded_analysis criterion) to
// a criterion-free in-root allow.
//
// The command below re-uses $D without a visible assignment; even with the
// session temp dir present in ctx, the digest must keep the ⊤ shape (the
// CommandUnboundedAnalysis criterion fires) and must NOT carry a concrete
// in-root temp target for the redirect — i.e. no fabricated `D` binding.
func TestAttachShellAnalysis_DoesNotSeedDSessionVarBinding(t *testing.T) {
	const command = `git diff main...HEAD -- core > $D/registry.diff`

	registry, provider, confirmCalled := newShellAnalysisRegistry(t, "VERDICT: ALLOW\nREASON: benign diff dump")
	// Mock tool NAMED bash_exec without a Judge: the call is escalation-free,
	// so the digest reaches the strict judge purely through the
	// AttachShellAnalysis ctx attachment.
	bashMock := newMockTool("bash_exec", "mock shell")
	bashMock.group = sdktools.GroupExecute
	registry.Register(bashMock)

	ws := t.TempDir()
	temp := t.TempDir()
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	// The session temp dir IS present in ctx, but AttachShellAnalysis must not
	// turn it into a `D` binding — an unassigned $D stays unset.
	ctx = sdktools.WithTempDir(ctx, temp)
	result, err := registry.Execute(ctx, "bash_exec", marshalShellInput(t, command, ws))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected execution (strict ALLOW), got error result: %s", result.Content)
	}
	if *confirmCalled {
		t.Error("strict ALLOW must not reach the manual confirmation path")
	}
	analysis, ok := strictEnvelopeField(t, provider.snapshot(), "analysis")
	if !ok {
		t.Fatal("strict envelope lacks the analysis field")
	}
	// The digest sits inside the untrusted-content boundary wrapper
	// (see strictEnvelopeField); peel it to the JSON object.
	start := strings.Index(analysis, "{")
	end := strings.LastIndex(analysis, "}")
	if start < 0 || end < start {
		t.Fatalf("analysis field carries no JSON digest: %s", analysis)
	}
	var digest sdktools.ShellAnalysisDigest
	if err := json.Unmarshal([]byte(analysis[start:end+1]), &digest); err != nil {
		t.Fatalf("analysis field is not a shell-analysis digest: %v (%s)", err, analysis)
	}

	// Fail-closed: the ⊤ (unbounded-analysis) criterion must fire — the
	// analysis must NOT have been handed a concrete target to bound it.
	fired := false
	for _, c := range digest.Criteria {
		if c.Fired == sdktools.ReasonCodeCommandUnboundedAnalysis {
			fired = true
		}
	}
	if !fired {
		t.Errorf("unassigned $D redirect must keep the ⊤ shape (unbounded-analysis criterion); criteria: %+v", digest.Criteria)
	}

	// And no concrete in-root temp target may appear: the removed seed must
	// not be resurrected by AttachShellAnalysis.
	wantTarget := filepath.Join(temp, "registry.diff")
	for _, e := range digest.Effects {
		if e.Kind != "FSWrite" || e.Mode != "Direct" {
			continue
		}
		for _, target := range e.Targets {
			if filepath.Clean(target) == filepath.Clean(wantTarget) {
				t.Errorf("AttachShellAnalysis fabricated the removed D → session-temp binding: redirect resolved to %q", target)
			}
		}
	}
}
