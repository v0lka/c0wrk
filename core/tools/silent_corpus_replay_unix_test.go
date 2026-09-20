//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"

	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// ── Silent-mode corpus replay (deny-precision / allow-recall baseline) ────
//
// This is the measurable baseline the deny-accuracy tracks (A/B/C/D in
// silent-mode-deny-accuracy-recommendations.md §2–§3) are held against:
// "без замера любое улучшение нефальсифицируемо" (§6.1). It replays the 194
// audited tool_confirm decisions through the REAL deterministic pipeline —
// AnalyzeShellCommandForJudge (flowsh), the real bash_exec/read_file Judges,
// the real registry policy gates and the real silent terminal — with the LLM
// strict judge replaced by a DETERMINISTIC stub whose policy models the
// strict-judge doctrine the recommendations fix:
//
//	ALLOW ⟺ workspace-scoped-verification marker (Track B) ∨ no hard criteria
//	otherwise DENY (fail-closed: the judge cannot positively establish)
//
// Nothing here executes a corpus command: the shell/read tools are wrapped
// with inert Execute overrides, so a stub ALLOW resolves the decision without
// running anything.
//
// Modes:
//
//   - Baseline snapshot (now): replay counts must equal
//     testdata/silent_corpus/baseline_snapshot.json EXACTLY. Every landed
//     track PR consciously updates the snapshot (SILENT_CORPUS_REGEN_SNAPSHOT=1
//     go test ./core/tools -run TestSilentCorpus_Replay) — the diff IS the
//     review artifact for "which FDs did this track retire".
//   - Final thresholds (when snapshot.final_thresholds_enforced flips true):
//     deny-precision ≥ 0.80 and allow-recall = 1.0 become hard gates.
//     step_11 acceptance finding (2026-09-19, owner decision accept_gap):
//     these thresholds are NOT reachable with THIS deterministic stub after
//     the A+B+C+D package — the remaining FD/TA are code-exec sinks
//     (awk/python3/jq) and out-of-root reads the marker excludes by design,
//     and §4 of the recommendations forbids blanket non-canonical-C6→allow
//     (3 of 8 TD are exactly that shape). FD 967229 (python3 repo script)
//     vs TD 967493/969396/969419 (python3 temp scripts) is separable only
//     by script CONTENT — flip the flag only after Track E (content
//     attachment) or an equivalent positive-evidence doctrine lands.
//
// Independently of the mode, the eight TRUE_DENY events are ALWAYS asserted
// denied (must-stay-denied; loosening the fail-closed path must keep them —
// audit: 3 of 8 TD are exactly "judge CONFIRM → fail-closed deny" on C6).

const corpusSnapshotPath = "testdata/silent_corpus/baseline_snapshot.json"

// corpusSessionTempRe extracts the historical session temp directory a
// replayed command embeds — the audit DB's
// ~/.c0wrk/projects/<project>/<session-uuid>/temp layout, handed to the model
// by the system prompt and used verbatim in its commands (sometimes behind an
// in-command D=/OUT=/TMP= assignment, which the leading /Users/ anchor
// skips). It is attached as a session ROOT only: production attaches no shell
// variable binding (AttachShellAnalysis seeds none), so a $D form resolves
// only through its own in-command assignment — exactly as the executed shell
// would resolve it.
var corpusSessionTempRe = regexp.MustCompile(`/[^\s"';&|]*\.c0wrk/projects/[^/\s"';&|]+/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/temp`)

// corpusSessionTemp returns the embedded session temp directory, or "" when
// the command references none (its session's temp dir is then not recoverable
// from the fixture, and no target resolves there).
func corpusSessionTemp(command string) string {
	return corpusSessionTempRe.FindString(command)
}

// corpusPathWithinAnyRoot reports whether dir is already inside one of the
// ctx's session roots (workspace/temp/allowed) — the production cases that
// need no workdir mirroring.
func corpusPathWithinAnyRoot(ctx context.Context, dir string) bool {
	for _, root := range sdktools.SessionRoots(ctx) {
		if sdktools.IsWithinRoot(ctx, root, dir) {
			return true
		}
	}
	return false
}

// corpusWithinHostTemp reports whether dir is under the host temp directory
// — the validateWorkDir escape hatch that lets a command RUN from there
// without making the directory a containment root.
func corpusWithinHostTemp(dir string) bool {
	return sdktools.IsWithinRoot(context.Background(), os.TempDir(), dir)
}

// corpusStubJudgeProvider is a deterministic llm.Provider: the replay sets
// the scripted verdict before each Execute, precomputed from the same
// analysis the registry will attach (flowsh is deterministic: same input →
// same digest → same stub decision; no LLM anywhere in the loop).
type corpusStubJudgeProvider struct {
	mu       sync.Mutex
	response string
	calls    int
}

func (p *corpusStubJudgeProvider) ChatCompletion(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return &llm.ChatResponse{Message: llm.Message{Content: p.response}}, nil
}

func (p *corpusStubJudgeProvider) Name() string { return "corpus-stub-judge" }

func (p *corpusStubJudgeProvider) setVerdict(allow bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if allow {
		p.response = "VERDICT: ALLOW\nREASON: stub judge: no hard criteria fired (or the workspace-scoped verification marker positively established safety)"
		return
	}
	p.response = "VERDICT: DENY\nREASON: stub judge: hard criteria fired and no positive evidence (workspace-scoped verification marker) establishes safety — fail-closed"
}

// corpusWorkspaceVerificationMarker reads the Track-B marker from the
// analysis digest (the workspaceScopedVerification field, landed in
// sp4rk-shell-analysis/v2 and carried unchanged into v3, sp4rk
// tools/shellanalysis.go): the stub
// judge ALLOWs marked verification drivers exactly as the real strict-judge
// prompt now teaches ("sufficient grounds to ALLOW unless the command text
// contradicts it"). Unmarked commands keep the fail-closed DENY — a judge
// that cannot positively establish still denies (3 of the 8 TD are exactly
// this shape and must stay denied).
func corpusWorkspaceVerificationMarker(a *sdktools.ShellAnalysis) bool {
	return a != nil && a.Digest.WorkspaceScopedVerification
}

// inertBashTool embeds the REAL bash_exec tool (real Judge — blocklist +
// flowsh criteria through the ctx-attached analysis — real group/policy
// surface) and neutralizes only Execute: a replayed ALLOW must resolve the
// audited decision without ever running the corpus command.
type inertBashTool struct {
	*builtins.BashExecTool
}

func (inertBashTool) Execute(context.Context, json.RawMessage) (sdktools.ToolResult, error) {
	return sdktools.ToolResult{Content: "corpus replay: inert execution (decision resolved, command not run)"}, nil
}

// inertReadFileTool is the read_file counterpart of inertBashTool: the real
// Judge (path containment), inert Execute.
type inertReadFileTool struct {
	*builtins.ReadFileTool
}

func (inertReadFileTool) Execute(context.Context, json.RawMessage) (sdktools.ToolResult, error) {
	return sdktools.ToolResult{Content: "corpus replay: inert execution (decision resolved, file not read)"}, nil
}

// corpusSnapshot is the golden baseline. The replay cross-tab must reproduce
// it exactly; each track PR updates it consciously. tracks counts the
// FALSE_DENY fixtures STILL denied, per fix tag (a fixture tagged B+D counts
// under both — retiring either track's mechanism should flip it).
type corpusSnapshot struct {
	TrueDenyDenied  int            `json:"true_deny_denied"`
	FalseDenyDenied int            `json:"false_deny_denied"`
	TrueAllowDenied int            `json:"true_allow_denied"`
	FalseAllowCount int            `json:"false_allow_count"`
	TracksStillDeny map[string]int `json:"fd_still_denied_per_track"`
	FinalThresholds bool           `json:"final_thresholds_enforced"`
	SnapshotComment string         `json:"comment"`
}

// corpusReplayOutcome is the measured cross-tab of one replay run.
type corpusReplayOutcome struct {
	trueDenyDenied  int
	falseDenyDenied int
	trueAllowDenied int
	falseAllow      int
	tracksStillDeny map[string]int
	// deniedByEvent records the terminal deny/allow verdict of every replayed
	// event, keyed by the audit event id. It carries the per-event facts the
	// pinned incident assertions in TestSilentCorpus_Replay check directly
	// (the aggregate cross-tab cannot name a single event).
	deniedByEvent map[int]bool
}

// newCorpusReplayRegistries builds the two silent-mode registries the corpus
// needs: the default judge terminal and the permissive allow terminal (the
// historical tool_confirm sub-policies recorded per event). Both share the
// stub judge provider; both record their autonomy decisions.
func newCorpusReplayRegistries(t *testing.T, provider *corpusStubJudgeProvider, bash *builtins.BashExecTool, read *builtins.ReadFileTool) (judge, allow *ToolRegistry, judgeRec, allowRec *autonomyDecisionRecorder) {
	t.Helper()

	build := func(mode string) (*ToolRegistry, *autonomyDecisionRecorder) {
		registry := NewToolRegistry()
		setDefaultGroupPolicies(registry)
		registry.ApplySecurityState(registry.GroupPolicies(), false, AutonomyModeSilent, SilentModeState{ToolConfirm: mode})
		registry.SetJudge(sdktools.NewToolJudge(provider, "corpus-stub", 1, nil))
		rec := &autonomyDecisionRecorder{}
		registry.SetAutonomyDecisionObserver(rec.observe)
		registry.Register(inertBashTool{bash})
		registry.Register(inertReadFileTool{read})
		return registry, rec
	}
	judge, judgeRec = build(SilentToolConfirmJudge)
	allow, allowRec = build(SilentToolConfirmAllow)
	return judge, allow, judgeRec, allowRec
}

// replayCorpus runs every fixture through the real pipeline and returns the
// cross-tab. The per-case flow mirrors production exactly once per call:
// precompute the flowsh analysis (what AttachShellAnalysis will recompute
// deterministically inside Execute), derive the stub verdict from it, then
// let the registry decide through its own gates.
func replayCorpus(t *testing.T, cases []silentCorpusCase) corpusReplayOutcome {
	t.Helper()

	bash, err := builtins.NewBashExecTool(nil)
	if err != nil {
		t.Fatalf("NewBashExecTool: %v", err)
	}
	read := builtins.NewReadFileTool()
	provider := &corpusStubJudgeProvider{}
	judgeReg, allowReg, judgeRec, allowRec := newCorpusReplayRegistries(t, provider, bash, read)

	out := corpusReplayOutcome{tracksStillDeny: map[string]int{}, deniedByEvent: map[int]bool{}}
	for _, c := range cases {
		ctx := sdktools.WithWorkspacePathNoProbe(context.Background(), c.Workspace)
		// Production fidelity: the executor always attaches the session temp
		// directory as a session root (sdktools.WithTempDir). Mirror it for
		// fixtures whose command embeds the historical temp path; without it
		// every temp-writing fixture would look out-of-root exactly where
		// production sees an in-root write. No shell-variable binding is
		// mirrored: AttachShellAnalysis seeds none (an unassigned $D stays
		// unset, fail-closed), and every $D fixture here also assigns D in
		// the command itself, so the analysis is unaffected either way.
		if temp := corpusSessionTemp(c.Command); temp != "" {
			ctx = sdktools.WithTempDir(ctx, temp)
		}
		// Production fidelity: bash_exec validates working_directory against
		// the session roots ∪ os.TempDir() BEFORE execution (sp4rk builtins
		// validateWorkDir), so a recorded call's workdir was either already
		// inside a session root (workspace/temp — nothing to mirror), an
		// os.TempDir() path (allowed for execution but NOT a containment
		// root in production — skipped here for the same reason), or a
		// registered additional working directory (sdktools.WithAllowedRoots
		// in production — e.g. the cross-repo sp4rk/flowsh partners). Mirror
		// the last case only.
		if c.Workdir != "" && !corpusPathWithinAnyRoot(ctx, c.Workdir) && !corpusWithinHostTemp(c.Workdir) {
			ctx = sdktools.WithAllowedRoots(ctx, []string{c.Workdir})
		}

		var input json.RawMessage
		var analysis *sdktools.ShellAnalysis
		hard := false
		switch c.Tool {
		case sdktools.ToolBashExec:
			input, err = json.Marshal(map[string]string{"command": c.Command, "working_directory": c.Workdir})
			if err != nil {
				t.Fatalf("event %d: marshal input: %v", c.EventID, err)
			}
			// Assign, don't shadow: the stub policy below reads this exact
			// analysis (a := here would leave the outer analysis nil and the
			// marker predicate would never see the digest).
			var aErr error
			analysis, aErr = sdktools.AnalyzeShellCommandForJudge(ctx, sdktools.ToolBashExec, input)
			if aErr != nil {
				t.Fatalf("event %d: AnalyzeShellCommandForJudge: %v", c.EventID, aErr)
			}
			// The same attachment the registry performs inside Execute; the
			// real bash Judge folds the winning criterion into the outcome.
			outcome := bash.Judge(sdktools.WithShellAnalysis(ctx, analysis, nil), input)
			hard = !outcome.Allow && outcome.Severity == sdktools.JudgeSeverityHard
		case "read_file":
			input, err = json.Marshal(map[string]string{"path": c.Path})
			if err != nil {
				t.Fatalf("event %d: marshal input: %v", c.EventID, err)
			}
			outcome := read.Judge(ctx, input)
			hard = !outcome.Allow && outcome.Severity == sdktools.JudgeSeverityHard
		default:
			t.Fatalf("event %d: unsupported corpus tool %q", c.EventID, c.Tool)
		}

		// Deterministic stub policy: ALLOW ⟺ marker B ∨ no hard criteria.
		provider.setVerdict(corpusWorkspaceVerificationMarker(analysis) || !hard)

		// Replay under the historically recorded tool_confirm sub-policy
		// ("silent" events predate the policy field — the judge default).
		registry, rec := judgeReg, judgeRec
		if c.GateMode == SilentToolConfirmAllow {
			registry, rec = allowReg, allowRec
		}
		before := len(rec.decisions)
		res, execErr := registry.Execute(ctx, c.Tool, input)
		if execErr != nil {
			t.Fatalf("event %d: Execute: %v", c.EventID, execErr)
		}
		if got := len(rec.decisions) - before; got != 1 {
			t.Fatalf("event %d: %d autonomy decisions recorded, want exactly 1", c.EventID, got)
		}
		decision := rec.decisions[len(rec.decisions)-1]
		denied := decision.Verdict == autonomyDecisionVerdictDeny
		if denied != res.IsError {
			t.Fatalf("event %d: decision verdict %q disagrees with result IsError=%v", c.EventID, decision.Verdict, res.IsError)
		}
		out.deniedByEvent[c.EventID] = denied

		switch c.AuditClass {
		case corpusClassTrueDeny:
			if denied {
				out.trueDenyDenied++
			}
		case corpusClassFalseDeny:
			if denied {
				out.falseDenyDenied++
				for _, tag := range c.TrackTags {
					out.tracksStillDeny[tag]++
				}
			}
		case corpusClassTrueAllow:
			if denied {
				out.trueAllowDenied++
				t.Logf("TRUE_ALLOW denied by replay: event %d criterion=%s command=%.120s", c.EventID, c.Criterion, c.Command)
			}
		case corpusClassFalseAllow:
			if !denied {
				out.falseAllow++
			}
		}
	}
	return out
}

// corpusMetrics renders the headline metrics: deny-precision = TD/(TD+FD)
// over denied events, allow-recall over the audited allow classes.
func (o corpusReplayOutcome) corpusMetrics(cases []silentCorpusCase) (denyPrecision, allowRecall float64) {
	denied := o.trueDenyDenied + o.falseDenyDenied
	if denied > 0 {
		denyPrecision = float64(o.trueDenyDenied) / float64(denied)
	}
	allowTotal := 0
	for _, c := range cases {
		if c.AuditClass == corpusClassTrueAllow || c.AuditClass == corpusClassFalseAllow {
			allowTotal++
		}
	}
	allowAllowed := allowTotal - o.trueAllowDenied - o.falseAllow
	if allowTotal > 0 {
		allowRecall = float64(allowAllowed) / float64(allowTotal)
	}
	return denyPrecision, allowRecall
}

// corpusOutcomeEqual reports whether two replay cross-tabs are identical
// (determinism evidence — no LLM, no hidden state).
func corpusOutcomeEqual(a, b corpusReplayOutcome) bool {
	if a.trueDenyDenied != b.trueDenyDenied || a.falseDenyDenied != b.falseDenyDenied ||
		a.trueAllowDenied != b.trueAllowDenied || a.falseAllow != b.falseAllow {
		return false
	}
	if len(a.tracksStillDeny) != len(b.tracksStillDeny) {
		return false
	}
	for tag, n := range a.tracksStillDeny {
		if b.tracksStillDeny[tag] != n {
			return false
		}
	}
	if len(a.deniedByEvent) != len(b.deniedByEvent) {
		return false
	}
	for id, denied := range a.deniedByEvent {
		if b.deniedByEvent[id] != denied {
			return false
		}
	}
	return true
}

// TestSilentCorpus_Replay replays the audited corpus and holds it against the
// baseline snapshot. See the file comment for the mode semantics; the
// regeneration path (SILENT_CORPUS_REGEN_SNAPSHOT=1) exists so each track PR
// can update the golden numbers with a single conscious command instead of
// hand-editing JSON.
func TestSilentCorpus_Replay(t *testing.T) {
	cases := loadSilentCorpus(t)
	out := replayCorpus(t, cases)

	// Determinism evidence (the AC of the baseline: reproducible without an
	// LLM): a second, independent replay must yield the identical cross-tab —
	// same flowsh digests, same stub verdicts, no hidden state.
	out2 := replayCorpus(t, cases)
	if !corpusOutcomeEqual(out, out2) {
		t.Fatalf("replay is not deterministic: run1 %+v, run2 %+v", out, out2)
	}

	denyPrecision, allowRecall := out.corpusMetrics(cases)
	t.Logf("corpus replay: TD denied %d/8, FD denied %d (audit: 43, of which 3 retired by sp4rk 950a865), TA denied %d/143, FA %d/0",
		out.trueDenyDenied, out.falseDenyDenied, out.trueAllowDenied, out.falseAllow)
	t.Logf("corpus replay: deny-precision %.4f (audit real-judge baseline 8/51 = 15.7%%), allow-recall %.4f (audit real-judge baseline 1.0; the deterministic stub cannot positively clear, which is the gap Track B's marker closes)",
		denyPrecision, allowRecall)
	t.Logf("corpus replay: FD still denied per track: A=%d B=%d C=%d D=%d",
		out.tracksStillDeny["A"], out.tracksStillDeny["B"], out.tracksStillDeny["C"], out.tracksStillDeny["D"])

	// Security invariant, independent of any snapshot: every audited TRUE_DENY
	// must stay denied. 3 of the 8 are "judge CONFIRM → fail-closed deny" on
	// the non-canonical C6 — any loosening of the fail-closed path that loses
	// them is a regression, not an improvement.
	if out.trueDenyDenied != len(corpusTrueDenyEventIDs) {
		tdDenied := corpusDeniedEventIDs(t, cases)
		t.Errorf("TRUE_DENY denied = %d, want all %d (must-stay-denied); replay-allowed TD events: %v",
			out.trueDenyDenied, len(corpusTrueDenyEventIDs), tdDenied)
	}

	// Pinned per-event incident assertions — the flow-based criteria's
	// falsifiable acceptance, stated on the events themselves rather than
	// inferred from the aggregate cross-tab:
	//
	//   - 966284 (track A, "network-ingress-only"): the read-only stdout
	//     fetch (`curl … | head`) the OLD ⊤∧NetEgress∧host-evidence C5 rule
	//     manufactured a canonical cradle deny for. A fetch that neither
	//     executes nor persists establishes NO flow, so neither C5 nor C7 is
	//     in force and the deterministic stub must ALLOW it — the incident is
	//     fixed on the deterministic layer.
	//   - 964976/965136 (`curl -o … <file>`): the NEW C7
	//     external-content-ingest flow (hard, non-canonical) keeps them
	//     denied — the fail-closed-on-arbitrary-host replacement for the
	//     removed host-reputation trigger (ADR-056 D2).
	assertEventDenied := func(id int, wantDenied bool, why string) {
		got, ok := out.deniedByEvent[id]
		if !ok {
			t.Errorf("event %d missing from the replay outcome (corpus/linkage drift)", id)
			return
		}
		if got != wantDenied {
			t.Errorf("event %d denied = %v, want %v: %s", id, got, wantDenied, why)
		}
	}
	assertEventDenied(966284, false,
		"read-only stdout fetch: no cradle/ingest flow fires, the deterministic stub must ALLOW it (phantom-C5 incident fixed)")
	assertEventDenied(964976, true,
		"curl -o persisted fetch: the C7 external-content-ingest flow is in force (hard, non-canonical)")
	assertEventDenied(965136, true,
		"curl -o persisted fetch: the C7 external-content-ingest flow is in force (hard, non-canonical)")
	// 966665 (toolchain over an unpacked EXTERNAL module graph: GOFLAGS=-mod=mod
	// + a `> go.work` write): the ⊤/C6 escalation must survive. This is the
	// regression the flow-based rewrite caught — a flowsh KB that BOUNDS a
	// verification driver fires no criterion on this shape, silently clearing
	// the marker's env/manifest screens (the driver stays unresolved by
	// design; see flowsh kb/data/linting.yaml).
	assertEventDenied(966665, true,
		"toolchain over an external module graph: the ⊤ escalation must stay (no cradle/ingest flow, marker off)")

	if os.Getenv("SILENT_CORPUS_REGEN_SNAPSHOT") == "1" {
		snap := corpusSnapshot{
			TrueDenyDenied:  out.trueDenyDenied,
			FalseDenyDenied: out.falseDenyDenied,
			TrueAllowDenied: out.trueAllowDenied,
			FalseAllowCount: out.falseAllow,
			TracksStillDeny: out.tracksStillDeny,
			FinalThresholds: false,
			SnapshotComment: "regenerated; review the diff as the track-PR review artifact",
		}
		raw, err := json.MarshalIndent(snap, "", "  ")
		if err != nil {
			t.Fatalf("marshal snapshot: %v", err)
		}
		if err := os.WriteFile(filepath.FromSlash(corpusSnapshotPath), append(raw, '\n'), 0o644); err != nil {
			t.Fatalf("write snapshot: %v", err)
		}
		t.Logf("SILENT_CORPUS_REGEN_SNAPSHOT=1: rewrote %s — review the diff", corpusSnapshotPath)
		return
	}

	raw, err := os.ReadFile(filepath.FromSlash(corpusSnapshotPath))
	if err != nil {
		t.Fatalf("read snapshot (%v); regenerate with SILENT_CORPUS_REGEN_SNAPSHOT=1 go test ./core/tools -run TestSilentCorpus_Replay", err)
	}
	var snap corpusSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}

	if out.trueDenyDenied != snap.TrueDenyDenied ||
		out.falseDenyDenied != snap.FalseDenyDenied ||
		out.trueAllowDenied != snap.TrueAllowDenied ||
		out.falseAllow != snap.FalseAllowCount {
		t.Errorf("replay cross-tab drifted from the baseline snapshot:\n got TD/FD/TA-denied/FA = %d/%d/%d/%d\nwant TD/FD/TA-denied/FA = %d/%d/%d/%d\n"+
			"If this run lands a deny-accuracy track (A/B/C/D), regenerate the snapshot and review the FD diff:\n"+
			"  SILENT_CORPUS_REGEN_SNAPSHOT=1 go test ./core/tools -run TestSilentCorpus_Replay",
			out.trueDenyDenied, out.falseDenyDenied, out.trueAllowDenied, out.falseAllow,
			snap.TrueDenyDenied, snap.FalseDenyDenied, snap.TrueAllowDenied, snap.FalseAllowCount)
	}
	for _, tag := range []string{"A", "B", "C", "D"} {
		if got, want := out.tracksStillDeny[tag], snap.TracksStillDeny[tag]; got != want {
			t.Errorf("track %s FD still denied = %d, snapshot says %d (regenerate + review after a track lands)", tag, got, want)
		}
	}

	// Final thresholds (§6.2): deny-precision ≥ 0.80 AND allow-recall = 1.0.
	// Enabled by flipping final_thresholds_enforced in the snapshot once the
	// A+B+C package lands; until then the snapshot equality above is the
	// regression gate.
	if snap.FinalThresholds {
		if denyPrecision < 0.80 {
			t.Errorf("final threshold: deny-precision = %.4f, want ≥ 0.80 (TD %d, FD %d)", denyPrecision, out.trueDenyDenied, out.falseDenyDenied)
		}
		if allowRecall < 1.0 {
			t.Errorf("final threshold: allow-recall = %.4f, want 1.0 (TA denied: %d, FA: %d)", allowRecall, out.trueAllowDenied, out.falseAllow)
		}
	}
}

// corpusDeniedEventIDs re-replays (diagnostic helper for the TD assertion
// failure path) and returns the TRUE_DENY events the replay allowed.
func corpusDeniedEventIDs(t *testing.T, cases []silentCorpusCase) []int {
	t.Helper()
	// The cheap variant: rerun only the TD fixtures through a fresh replay
	// driven by the same stub policy.
	tdCases := make([]silentCorpusCase, 0, len(corpusTrueDenyEventIDs))
	for _, c := range cases {
		if c.AuditClass == corpusClassTrueDeny {
			tdCases = append(tdCases, c)
		}
	}
	out := replayCorpus(t, tdCases)
	var allowed []int
	if out.trueDenyDenied != len(tdCases) {
		// Identify which: rerun individually.
		for _, c := range tdCases {
			single := replayCorpus(t, []silentCorpusCase{c})
			if single.trueDenyDenied == 0 {
				allowed = append(allowed, c.EventID)
			}
		}
	}
	return allowed
}
