//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// ── Silent-mode strict-judge verdict memoization (audit Track D) ───────────
//
// silentJudgeDecide keys verdicts by the shell EFFECT signature
// (ShellAnalysisDigest.Signature) within the registry's task: the first
// verdict for an effect is fixed and replayed verbatim on re-escalation. The
// tests here pin the audit's acceptance criteria:
//
//   - identical signature ⇒ NO second judge call, verdict + justification
//     reproduced byte-identically (the 961130/961162 class: the same command
//     re-spelled so only transport arguments differ);
//   - a different signature ⇒ the judge IS consulted again;
//   - the first verdict wins even when a later judge response would flip;
//   - an infrastructure failure (provider error, unparseable response) is
//     NOT memoized — the next occurrence retries the judge;
//   - non-shell tools never memoize (no effect identity to key on);
//   - a Clone starts with an empty memo (task scoping — no leak across
//     tasks/sessions);
//   - every emitted decision carries policy + signature (audit §6.3).

// sequenceJudgeProvider is a deterministic llm.Provider whose responses are
// scripted by 1-based call index: each ChatCompletion bumps the counter and
// forwards to script, so tests can model a judge whose verdict FLIPS between
// occurrences — exactly the historical non-determinism the memo must remove.
type sequenceJudgeProvider struct {
	mu     sync.Mutex
	calls  int
	script func(call int) (string, error)
}

func (p *sequenceJudgeProvider) ChatCompletion(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	content, err := p.script(p.calls)
	if err != nil {
		return nil, err
	}
	return &llm.ChatResponse{Message: llm.Message{Content: content}}, nil
}

func (p *sequenceJudgeProvider) Name() string { return "sequence-judge" }

func (p *sequenceJudgeProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// newSilentMemoRegistry builds a silent-mode registry under the given
// tool_confirm sub-policy with a sequence-scripted strict judge and the
// autonomy-decision recorder. The mock bash_exec tool registered by the
// caller carries no local Judge, so every call escalates softly and — being
// name-keyed as a shell tool — still gets the real flowsh analysis attached,
// which is where the effect signature comes from.
func newSilentMemoRegistry(mode string, provider *sequenceJudgeProvider) (*ToolRegistry, *autonomyDecisionRecorder) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	registry.ApplySecurityState(registry.GroupPolicies(), false, AutonomyModeSilent, SilentModeState{ToolConfirm: mode})
	registry.SetJudge(sdktools.NewToolJudge(provider, "sequence-judge", 1, nil))
	rec := &autonomyDecisionRecorder{}
	registry.SetAutonomyDecisionObserver(rec.observe)
	return registry, rec
}

// executeMockShell runs one mock bash_exec call and returns the result plus
// the recorded autonomy decision.
func executeMockShell(t *testing.T, registry *ToolRegistry, rec *autonomyDecisionRecorder, command string) (sdktools.ToolResult, AutonomyDecision) {
	t.Helper()
	ws := t.TempDir()
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	before := len(rec.decisions)
	res, err := registry.Execute(ctx, sdktools.ToolBashExec, marshalShellInput(t, command, ws))
	if err != nil {
		t.Fatalf("Execute(%q): %v", command, err)
	}
	if got := len(rec.decisions) - before; got != 1 {
		t.Fatalf("Execute(%q): %d autonomy decisions recorded, want exactly 1", command, got)
	}
	return res, rec.decisions[len(rec.decisions)-1]
}

// TestSilentJudgeMemo_IdenticalEffectSkipsJudge pins the core Track-D
// property on the audit's canonical non-determinism pair (961130 allow vs
// 961162 deny): two commands that differ ONLY in a transport argument
// (tail -20 vs tail -15) carry the same effect signature, so the second
// occurrence reuses the first verdict without consulting the judge — even
// though the scripted judge would flip to DENY — and the terminal
// (verdict + justification) is reproduced byte-identically.
func TestSilentJudgeMemo_IdenticalEffectSkipsJudge(t *testing.T) {
	provider := &sequenceJudgeProvider{script: func(call int) (string, error) {
		if call == 1 {
			return "VERDICT: ALLOW\nREASON: in-repo verification pipeline; no egress, no irreversible writes", nil
		}
		return "VERDICT: DENY\nREASON: the historical flip the memo must eliminate", nil
	}}
	registry, rec := newSilentMemoRegistry(SilentToolConfirmJudge, provider)
	registry.Register(newMockTool(sdktools.ToolBashExec, "mock shell exec"))

	res1, d1 := executeMockShell(t, registry, rec, "cat notes.txt 2>&1 | tail -20")
	res2, d2 := executeMockShell(t, registry, rec, "cat notes.txt 2>&1 | tail -15")

	if got := provider.callCount(); got != 1 {
		t.Fatalf("strict judge calls = %d, want 1: an identical effect signature must reuse the memoized verdict", got)
	}
	if res1.IsError || res2.IsError {
		t.Fatalf("both occurrences must execute on the first ALLOW: IsError = %v / %v", res1.IsError, res2.IsError)
	}
	if d1.Verdict != autonomyDecisionVerdictAllow || d2.Verdict != autonomyDecisionVerdictAllow {
		t.Fatalf("verdicts = %q/%q, want allow/allow — the memo must reproduce the first verdict", d1.Verdict, d2.Verdict)
	}
	if d1.Justification != d2.Justification {
		t.Errorf("justifications must be byte-identical on a memo hit:\nfirst:  %q\nsecond: %q", d1.Justification, d2.Justification)
	}
	// Audit completeness (§6.3): both decisions name the deciding sub-policy
	// and the adjudicated effect.
	if d1.Policy != SilentToolConfirmJudge || d2.Policy != SilentToolConfirmJudge {
		t.Errorf("policies = %q/%q, want %q on every silent judge decision", d1.Policy, d2.Policy, SilentToolConfirmJudge)
	}
	if d1.Signature == "" || d1.Signature != d2.Signature {
		t.Errorf("signatures = %q/%q, want equal and non-empty (the audit pair must share one effect identity)", d1.Signature, d2.Signature)
	}
}

// TestSilentJudgeMemo_FirstVerdictWins proves the memo is write-once per
// effect: a judge whose response flips from DENY to ALLOW between two
// occurrences of the same effect cannot change the task's terminal — the
// second occurrence replays the first DENY verbatim.
func TestSilentJudgeMemo_FirstVerdictWins(t *testing.T) {
	provider := &sequenceJudgeProvider{script: func(call int) (string, error) {
		if call == 1 {
			return "VERDICT: DENY\nREASON: egress to an untrusted host in the argument", nil
		}
		return "VERDICT: ALLOW\nREASON: changed my mind — exactly what must not matter", nil
	}}
	registry, rec := newSilentMemoRegistry(SilentToolConfirmJudge, provider)
	registry.Register(newMockTool(sdktools.ToolBashExec, "mock shell exec"))

	res1, d1 := executeMockShell(t, registry, rec, "cat notes.txt 2>&1 | tail -20")
	res2, d2 := executeMockShell(t, registry, rec, "cat notes.txt 2>&1 | tail -20")

	if got := provider.callCount(); got != 1 {
		t.Fatalf("strict judge calls = %d, want 1: the second occurrence must hit the memo", got)
	}
	if !res1.IsError || !res2.IsError {
		t.Fatalf("both occurrences must stay denied: IsError = %v / %v", res1.IsError, res2.IsError)
	}
	if d1.Verdict != autonomyDecisionVerdictDeny || d2.Verdict != autonomyDecisionVerdictDeny {
		t.Fatalf("verdicts = %q/%q, want deny/deny — the first verdict is fixed for the task", d1.Verdict, d2.Verdict)
	}
	if d1.Justification != d2.Justification {
		t.Errorf("justifications must be byte-identical on a memo hit:\nfirst:  %q\nsecond: %q", d1.Justification, d2.Justification)
	}
}

// TestSilentJudgeMemo_DifferentEffectConsultsJudgeAgain proves the memo does
// not over-match: two commands whose canonical effects differ (a redirect to
// a different target) have different signatures, so the judge IS consulted
// for the second effect.
func TestSilentJudgeMemo_DifferentEffectConsultsJudgeAgain(t *testing.T) {
	provider := &sequenceJudgeProvider{script: func(int) (string, error) {
		return "VERDICT: ALLOW\nREASON: in-workspace write", nil
	}}
	registry, rec := newSilentMemoRegistry(SilentToolConfirmJudge, provider)
	registry.Register(newMockTool(sdktools.ToolBashExec, "mock shell exec"))

	_, d1 := executeMockShell(t, registry, rec, "printf hi > a.txt")
	_, d2 := executeMockShell(t, registry, rec, "printf hi > b.txt")

	if got := provider.callCount(); got != 2 {
		t.Fatalf("strict judge calls = %d, want 2: a different effect signature must be adjudicated afresh", got)
	}
	if d1.Signature == "" || d2.Signature == "" {
		t.Fatalf("signatures = %q/%q, want non-empty", d1.Signature, d2.Signature)
	}
	if d1.Signature == d2.Signature {
		t.Fatalf("signatures must differ for different write targets: %q", d1.Signature)
	}
}

// TestSilentJudgeMemo_JudgeFailureNotMemoized proves an infrastructure
// failure is not a verdict: a provider outage fail-closed denies the first
// occurrence WITHOUT recording a memo entry, so the second occurrence — once
// the provider recovered — is adjudicated afresh and may ALLOW. Memoizing the
// failure would cement a transient outage into a task-lifetime denial.
func TestSilentJudgeMemo_JudgeFailureNotMemoized(t *testing.T) {
	provider := &sequenceJudgeProvider{script: func(call int) (string, error) {
		if call == 1 {
			return "", errors.New("provider outage")
		}
		return "VERDICT: ALLOW\nREASON: provider recovered; the effect is a benign verification run", nil
	}}
	registry, rec := newSilentMemoRegistry(SilentToolConfirmJudge, provider)
	registry.Register(newMockTool(sdktools.ToolBashExec, "mock shell exec"))

	res1, d1 := executeMockShell(t, registry, rec, "cat notes.txt 2>&1 | tail -20")
	if !res1.IsError {
		t.Fatal("first occurrence (provider outage) must fail closed to a denial")
	}
	if d1.Verdict != autonomyDecisionVerdictDeny {
		t.Fatalf("first verdict = %q, want deny (fail-closed)", d1.Verdict)
	}

	res2, d2 := executeMockShell(t, registry, rec, "cat notes.txt 2>&1 | tail -20")
	if got := provider.callCount(); got != 2 {
		t.Fatalf("strict judge calls = %d, want 2: an infrastructure failure must not be memoized", got)
	}
	if res2.IsError {
		t.Fatalf("second occurrence must execute after the judge recovered: %s", res2.Content)
	}
	if d2.Verdict != autonomyDecisionVerdictAllow {
		t.Fatalf("second verdict = %q, want allow (fresh adjudication after the failure)", d2.Verdict)
	}
}

// TestSilentJudgeMemo_NonShellToolNeverMemoizes proves the memo is keyed on
// the shell effect identity: a non-shell tool has no signature, so every
// escalation consults the judge and the decisions carry an empty Signature.
func TestSilentJudgeMemo_NonShellToolNeverMemoizes(t *testing.T) {
	provider := &sequenceJudgeProvider{script: func(int) (string, error) {
		return "VERDICT: ALLOW\nREASON: benign", nil
	}}
	registry, rec := newSilentMemoRegistry(SilentToolConfirmJudge, provider)
	registry.Register(newMockTool("mutating", "a non-shell mutating tool"))

	execute := func() AutonomyDecision {
		t.Helper()
		before := len(rec.decisions)
		res, err := registry.Execute(context.Background(), "mutating", json.RawMessage(`{"input":"x"}`))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if res.IsError {
			t.Fatalf("scripted ALLOW must execute: %s", res.Content)
		}
		if got := len(rec.decisions) - before; got != 1 {
			t.Fatalf("%d autonomy decisions recorded, want exactly 1", got)
		}
		return rec.decisions[len(rec.decisions)-1]
	}

	d1, d2 := execute(), execute()
	if got := provider.callCount(); got != 2 {
		t.Fatalf("strict judge calls = %d, want 2: without an effect signature every escalation goes to the judge", got)
	}
	if d1.Signature != "" || d2.Signature != "" {
		t.Errorf("signatures = %q/%q, want empty for a non-shell tool", d1.Signature, d2.Signature)
	}
}

// TestSilentJudgeMemo_CloneGetsFreshMemo proves the memo is task-scoped: a
// clone (what OrchestratorBuilder.registerSessionRegistry cuts per task
// launch) starts with an empty memo, so a verdict from the parent's task
// never leaks into a new task's adjudication.
func TestSilentJudgeMemo_CloneGetsFreshMemo(t *testing.T) {
	provider := &sequenceJudgeProvider{script: func(int) (string, error) {
		return "VERDICT: DENY\nREASON: out-of-root write", nil
	}}
	registry, rec := newSilentMemoRegistry(SilentToolConfirmJudge, provider)
	registry.Register(newMockTool(sdktools.ToolBashExec, "mock shell exec"))

	res1, d1 := executeMockShell(t, registry, rec, "cat notes.txt 2>&1 | tail -20")
	if !res1.IsError || d1.Verdict != autonomyDecisionVerdictDeny {
		t.Fatalf("parent task: IsError=%v verdict=%q, want denied", res1.IsError, d1.Verdict)
	}

	child := registry.Clone()
	childRec := &autonomyDecisionRecorder{}
	child.SetAutonomyDecisionObserver(childRec.observe)
	res2, d2 := executeMockShell(t, child, childRec, "cat notes.txt 2>&1 | tail -20")

	if got := provider.callCount(); got != 2 {
		t.Fatalf("strict judge calls = %d, want 2: a cloned registry must re-adjudicate (fresh task, fresh memo)", got)
	}
	if !res2.IsError || d2.Verdict != autonomyDecisionVerdictDeny {
		t.Fatalf("child task: IsError=%v verdict=%q, want denied (same judge script)", res2.IsError, d2.Verdict)
	}
}

// TestSilentJudgeMemo_OutrightSubPoliciesCarrySignature covers the two
// silent sub-policies that decide WITHOUT the judge (deny / allow-with-no-
// hard-reason): their decisions must still name the sub-policy and carry the
// effect signature (audit §6.3 — every decision names its mechanism and its
// effect).
func TestSilentJudgeMemo_OutrightSubPoliciesCarrySignature(t *testing.T) {
	for _, mode := range []string{SilentToolConfirmDeny, SilentToolConfirmAllow} {
		t.Run(mode, func(t *testing.T) {
			provider := &sequenceJudgeProvider{script: func(int) (string, error) {
				return "VERDICT: DENY\nREASON: must never be consulted on these paths", nil
			}}
			registry, rec := newSilentMemoRegistry(mode, provider)
			registry.Register(newMockTool(sdktools.ToolBashExec, "mock shell exec"))

			_, decision := executeMockShell(t, registry, rec, "cat notes.txt 2>&1 | tail -20")

			if decision.Policy != mode {
				t.Errorf("policy = %q, want %q", decision.Policy, mode)
			}
			if decision.Signature == "" {
				t.Errorf("an outright %q decision on a shell call must carry the effect signature", mode)
			}
			if got := provider.callCount(); got != 0 {
				t.Errorf("strict judge calls = %d, want 0: the %q sub-policy decides without the judge", got, mode)
			}
		})
	}
}

// TestStrictJudgeFailureReasonMarkers_Pin behaviorally pins the mirrored
// fail-safe reason markers: JudgeStrict NEVER returns a non-nil error, so the
// memo's judgeSpoke predicate must recognize infrastructure failures by these
// exact reason strings. If sp4rk ever rewords them, this pin fails instead of
// the memo silently caching failures as verdicts.
func TestStrictJudgeFailureReasonMarkers_Pin(t *testing.T) {
	// Provider outage → fail-safe CONFIRM with the failure marker, no error.
	judge, _ := newStrictJudge("", errors.New("outage"))
	verdict, reasoning, err := judge.JudgeStrict(context.Background(), sdktools.StrictJudgeRequest{ToolName: sdktools.ToolBashExec})
	if err != nil {
		t.Fatalf("JudgeStrict must fail safe in-band, got err = %v", err)
	}
	if verdict != sdktools.VerdictConfirm || reasoning != strictJudgeFailureReason {
		t.Fatalf("outage: verdict/reasoning = %d/%q, want CONFIRM/%q", verdict, reasoning, strictJudgeFailureReason)
	}

	// Twice-unparseable response → retry once, then fail-safe CONFIRM with
	// the unparsed marker.
	judge2, provider2 := newStrictJudge("probably fine", nil)
	verdict2, reasoning2, err2 := judge2.JudgeStrict(context.Background(), sdktools.StrictJudgeRequest{ToolName: sdktools.ToolBashExec})
	if err2 != nil {
		t.Fatalf("JudgeStrict must fail safe in-band, got err = %v", err2)
	}
	if verdict2 != sdktools.VerdictConfirm || reasoning2 != judgeUnparsedReason {
		t.Fatalf("unparseable: verdict/reasoning = %d/%q, want CONFIRM/%q", verdict2, reasoning2, judgeUnparsedReason)
	}
	if got := provider2.callCount(); got != 2 {
		t.Errorf("unparseable response must retry exactly once: calls = %d, want 2", got)
	}

	// judgeSpoke: parsed verdicts always spoke; only the two fail-safe
	// markers mark a CONFIRM as "the judge did not speak".
	if !judgeSpoke(sdktools.VerdictAllow, "benign") || !judgeSpoke(sdktools.VerdictDeny, "dangerous") {
		t.Error("parsed ALLOW/DENY must count as spoken verdicts")
	}
	if !judgeSpoke(sdktools.VerdictConfirm, "borderline: manual confirmation") {
		t.Error("a parsed CONFIRM with a substantive reasoning must count as spoken")
	}
	if judgeSpoke(sdktools.VerdictConfirm, strictJudgeFailureReason) || judgeSpoke(sdktools.VerdictConfirm, judgeUnparsedReason) {
		t.Error("the fail-safe markers must NOT count as spoken verdicts")
	}
}

// TestAutonomyDecisionAssistedDenyCarriesPolicyAndSignature pins audit §6.3
// on the assisted posture: a strict-judge DENY that terminates a call before
// any card opened names its deciding sub-policy ("judge") and — for a shell
// call — the adjudicated effect signature.
func TestAutonomyDecisionAssistedDenyCarriesPolicyAndSignature(t *testing.T) {
	provider := &sequenceJudgeProvider{script: func(int) (string, error) {
		return "VERDICT: DENY\nREASON: destructive write to a system path", nil
	}}
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	registry.SetAutonomyMode(AutonomyModeAssisted)
	registry.SetJudge(sdktools.NewToolJudge(provider, "sequence-judge", 1, nil))
	rec := &autonomyDecisionRecorder{}
	registry.SetAutonomyDecisionObserver(rec.observe)
	registry.Register(newMockTool(sdktools.ToolBashExec, "mock shell exec"))

	ws := t.TempDir()
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	res, err := registry.Execute(ctx, sdktools.ToolBashExec, marshalShellInput(t, "cat notes.txt 2>&1 | tail -20", ws))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatal("an assisted strict DENY must terminate the call")
	}
	if len(rec.decisions) != 1 {
		t.Fatalf("%d autonomy decisions recorded, want exactly 1", len(rec.decisions))
	}
	decision := rec.decisions[0]
	if decision.Kind != autonomyDecisionKindAssistedDeny {
		t.Errorf("kind = %q, want %q", decision.Kind, autonomyDecisionKindAssistedDeny)
	}
	if decision.Policy != SilentToolConfirmJudge {
		t.Errorf("policy = %q, want %q — the strict judge is the decider and the audit must name it", decision.Policy, SilentToolConfirmJudge)
	}
	if decision.Signature == "" {
		t.Error("an assisted_deny on a shell call must carry the effect signature")
	}
}

// TestSilentJudgeMemo_CorpusPairSameTerminal replays the audit's documented
// non-determinism pair — 961130 (allowed) vs 961162 (denied: the same vitest
// run, tail -20 vs tail -15) — through the REAL corpus pipeline (real bash
// tool, real flowsh analysis, judge-mode silent registry) with a judge whose
// verdict FLIPS between the two occurrences. The memo must give the pair one
// terminal: the second event reuses the first verdict without a second judge
// call, so the historical allow/deny split cannot reproduce.
func TestSilentJudgeMemo_CorpusPairSameTerminal(t *testing.T) {
	cases := loadSilentCorpus(t)
	pair := make([]silentCorpusCase, 0, 2)
	for _, c := range cases {
		if c.EventID == 961130 || c.EventID == 961162 {
			pair = append(pair, c)
		}
	}
	if len(pair) != 2 {
		t.Fatalf("corpus fixtures must contain the non-determinism pair 961130/961162, found %d", len(pair))
	}
	sort.Slice(pair, func(i, j int) bool { return pair[i].EventID < pair[j].EventID })

	bash, err := builtins.NewBashExecTool(nil)
	if err != nil {
		t.Fatalf("NewBashExecTool: %v", err)
	}
	provider := &sequenceJudgeProvider{script: func(call int) (string, error) {
		if call == 1 {
			return "VERDICT: ALLOW\nREASON: workspace-scoped vitest verification run", nil
		}
		return "VERDICT: DENY\nREASON: the historical flip (audit: 961162 denied what 961130 allowed)", nil
	}}
	registry, rec := newSilentMemoRegistry(SilentToolConfirmJudge, provider)
	registry.Register(inertBashTool{bash})

	results := make([]sdktools.ToolResult, 0, len(pair))
	for _, c := range pair {
		ctx := sdktools.WithWorkspacePathNoProbe(context.Background(), c.Workspace)
		if temp := corpusSessionTemp(c.Command); temp != "" {
			ctx = sdktools.WithTempDir(ctx, temp)
			ctx = sdktools.WithShellVarBindings(ctx, map[string]string{"D": temp})
		}
		if c.Workdir != "" && !corpusPathWithinAnyRoot(ctx, c.Workdir) && !corpusWithinHostTemp(c.Workdir) {
			ctx = sdktools.WithAllowedRoots(ctx, []string{c.Workdir})
		}
		input, mErr := json.Marshal(map[string]string{"command": c.Command, "working_directory": c.Workdir})
		if mErr != nil {
			t.Fatalf("event %d: marshal input: %v", c.EventID, mErr)
		}
		res, execErr := registry.Execute(ctx, c.Tool, input)
		if execErr != nil {
			t.Fatalf("event %d: Execute: %v", c.EventID, execErr)
		}
		results = append(results, res)
	}

	if got := provider.callCount(); got != 1 {
		t.Fatalf("strict judge calls = %d, want 1: the pair shares one effect signature, so the second occurrence must reuse the first verdict", got)
	}
	if len(results) != 2 || results[0].IsError || results[1].IsError {
		t.Fatalf("the pair must share one terminal (both allowed): IsError = %v / %v", results[0].IsError, results[1].IsError)
	}
	if len(rec.decisions) != 2 {
		t.Fatalf("%d autonomy decisions recorded, want exactly 2", len(rec.decisions))
	}
	d1, d2 := rec.decisions[0], rec.decisions[1]
	if d1.Verdict != autonomyDecisionVerdictAllow || d2.Verdict != autonomyDecisionVerdictAllow {
		t.Fatalf("verdicts = %q/%q, want allow/allow — the memo reproduces the first terminal", d1.Verdict, d2.Verdict)
	}
	if d1.Signature == "" || d1.Signature != d2.Signature {
		t.Errorf("signatures = %q/%q, want equal and non-empty (the audit pair is one effect)", d1.Signature, d2.Signature)
	}
	if d1.Policy != SilentToolConfirmJudge || d2.Policy != SilentToolConfirmJudge {
		t.Errorf("policies = %q/%q, want %q", d1.Policy, d2.Policy, SilentToolConfirmJudge)
	}
}
