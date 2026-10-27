package e2s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/strutil"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ErrPaused — a cooperative pause tripped at a step boundary: a recoverable
// checkpoint, not a failure (Result carries the snapshot + trajectory so far;
// the host resumes via Config.ResumeState). See specs/domains/e2s.md.
var ErrPaused = errors.New("e2s loop paused at step boundary")

// RunStatus is the loop's terminal disposition (why Run returned) — distinct
// from the domain StateStatus, the model-managed core Σ key. See specs/domains/e2s.md.
type RunStatus string

const (
	// RunStatusFinished — the model called the finish action; Answer is set.
	RunStatusFinished RunStatus = "finished"
	// RunStatusStepLimit — the step budget ran out before finish.
	RunStatusStepLimit RunStatus = "step_limit"
	// RunStatusSpinStop — the anti-spin detector aborted after identical
	// actions past the abort threshold.
	RunStatusSpinStop RunStatus = "spin_stop"
	// RunStatusPaused — a cooperative pause checkpoint at a step boundary.
	RunStatusPaused RunStatus = "paused"
	// RunStatusCanceled — the context was canceled mid-run.
	RunStatusCanceled RunStatus = "canceled"
	// RunStatusFailed — a fatal error ended the run; err is non-nil.
	RunStatusFailed RunStatus = "failed"
)

// Result is the outcome of an E2S run.
type Result struct {
	// Answer is the finish payload (empty for non-finished terminations).
	Answer string `json:"answer"`
	// Status is the terminal disposition.
	Status   RunStatus `json:"status"`
	Finished bool      `json:"finished"`
	// State is the final working state Σ (the Snapshot's Sigma).
	State map[string]any `json:"state"`
	// Snapshot is the full domain state — the resumable checkpoint.
	Snapshot E2SState `json:"snapshot"`
	// Turns is the number of completed turns (LLM steps).
	Turns int `json:"turns"`
	// Steps is the synthesized Thought/Action/Observation trajectory.
	Steps []agent.Step `json:"steps"`
}

// Emitter is the event surface the E2S loop requires: a structural subset of
// sp4rk's agent.Events, declared locally to avoid the core import cycle —
// every host emitter satisfies it. A nil emitter is a no-op.
type Emitter interface {
	StepStart(stepNum int)
	Thought(stepNum int, content, reasoning string)
	ToolCall(stepNum, callIdx int, toolName, argsPreview, source string)
	ToolResult(stepNum, callIdx int, resultLen int, preview string, isError bool)
	StepComplete(stepNum int, duration time.Duration)
	AssistantChunk(content string)
	AssistantDone(content string, inputTokens, outputTokens int)
	ContextFill(fillPercent float64, usedTokens, maxTokens int, status string, stepID string)
	Finishing(stepNum int, summary string)
	ExecutorDiagnostic(stepNum int, event string, details map[string]any)
}

// StateEmitter is an optional Emitter capability: when implemented, the loop
// emits the full Σ + turn snapshot after every applied patch (the e2s_state
// event); otherwise snapshots are skipped.
type StateEmitter interface {
	E2SState(data map[string]any)
}

// Registry is the tool surface the E2S loop requires: the dispatch target
// for actions (inheriting every security gate) and the descriptor catalog
// for the Available Tools section. ToolSource mirrors the executor's
// GetToolSource ("core" for built-ins, the mcp:<server> tag otherwise).
type Registry interface {
	List() []sdktools.ToolDescriptor
	Execute(ctx context.Context, name string, input json.RawMessage) (sdktools.ToolResult, error)
	IsToolUntrusted(name string) bool
	ToolSource(name string) string
}

// Config configures an E2S loop run. Zero values fall back to defaults
// (see withDefaults).
type Config struct {
	// Model is the LLM model id passed through to every ChatRequest.
	Model string
	// MaxTokens is the per-call output limit.
	MaxTokens int
	// MaxSteps caps the number of turns (LLM steps); e2s.max_steps seeds it,
	// withDefaults falls back to 16.
	MaxSteps int
	// MaxObservationChars is the per-observation cap in characters — the
	// FALLBACK when no window is resolved (default 8000).
	MaxObservationChars int
	// ObservationBudgetTokens is the per-turn observation budget in TOKENS
	// (ReAct-scale; the executor's tool-result budget is 30K). Engages only
	// with a resolved window: effective cap = min(budget, fill × window) × 4
	// chars. Default 8192.
	ObservationBudgetTokens int
	// ObservationFillFraction caps that budget as a fraction of the context
	// window, so a small-window model gets proportionally smaller
	// observations; ignored without a window. Default 0.4.
	ObservationFillFraction float64
	// StateByteLimit caps the JSON-encoded Σ (ApplyPatch's byte limit;
	// default DefaultStateByteLimit). An over-cap merge is rejected —
	// bounded retry, then error observation.
	StateByteLimit int
	// SpinNudgeThreshold — consecutive identical actions before a nudge
	// observation is injected (the redundant action is not re-dispatched).
	// Default 3.
	SpinNudgeThreshold int
	// SpinAbortThreshold — consecutive identical actions before the loop
	// aborts with RunStatusSpinStop. Default 5.
	SpinAbortThreshold int
	// PatchRetries bounds the corrective re-requests for an invalid turn
	// before the failure becomes the next observation. Default 1.
	PatchRetries int
	// ReasoningEffort, when non-empty, rides every ChatRequest (the
	// host-resolved override / Model Profiles sampling value), mirroring the
	// executor's SetReasoningEffort. Empty = provider default.
	ReasoningEffort string
	// SystemPrompt, when non-empty, replaces the compiled-in
	// prompts.E2SSystem (the Model Profiles Lite swap: the host passes
	// prompts.E2SSystemLite). Empty = prompts.E2SSystem.
	SystemPrompt string
	// InjectionDefense appends the config-gated prompts.InjectionDefense
	// directive (the unconditional VerificationMandate is always added),
	// mirroring the Conductor's security prefix — SECURITY.md mandates it
	// for every model-facing loop.
	InjectionDefense bool
	// EditVerify, when non-nil, is the config-authored verify-on-edit runner:
	// after any turn whose action (or batch sub-call) contains a successful
	// write_file/edit_file it runs once and its note is appended to the
	// observation (the executor's SetVerifyOnEdit hook).
	EditVerify agent.EditVerifyRunner
	// EditVerifyMaxChars caps the injected verification output;
	// <= 0 selects agent.DefaultVerifyOnEditCap.
	EditVerifyMaxChars int
	// ContextWindowTokens, when > 0, enables the flat per-request
	// ContextFill emission (input tokens vs the window); zero disables it
	// and falls the observation cap back to MaxObservationChars.
	ContextWindowTokens int
	// Task is the run objective: it seeds the core "objective" Σ key (Σ is
	// the model's only memory, so the objective must live there).
	Task string
	// InitialState seeds extension keys of the initial Σ (copied; never
	// mutated); non-core keys only — the core schema is built canonically
	// by NewE2SState.
	InitialState map[string]any
	// ResumeState, when non-nil, seeds the ENTIRE run state instead of a
	// fresh NewE2SState: a previous Result.Snapshot continues with its Σ,
	// turn count, and bookkeeping; takes precedence over Task + InitialState.
	ResumeState *E2SState
	// ResumeNote, when non-empty, replaces the generic turn-1 observation on
	// a resumed run: the turn has no message channel (the model sees only
	// Σ + O), so a user's follow-up rides the first observation or is lost.
	ResumeNote string
	// WorkspacePath and TempDir render the workspace containment sections
	// of the system prompt.
	WorkspacePath string
	TempDir       string
	// NotesPath is the session scratchpad — the sanctioned overflow channel
	// for raw data beyond the byte-capped Σ; the prompt renders it with its
	// usage rules and the model reaches it via ordinary write_file/read_file
	// gates. Empty disables; withDefaults derives <TempDir>/NotesFileName.
	NotesPath string
	// Skills are the active skill sections injected into the system prompt.
	Skills []SkillSection
	// ContentBlocks, when non-empty, are attached to EVERY turn's user
	// message (image attachments): each turn is a fresh dialog, so the
	// blocks must be re-sent or they vanish after turn 1. The provider
	// prepends Message.Content as a text block when the blocks carry no text.
	ContentBlocks []llm.ContentBlock
	// TrajectoryStore, when non-nil, is synced with the growing step
	// trajectory after every completed step.
	Trajectory agent.TrajectoryStore
	// ToolCache is the shared tool-result cache (the Conductor executor's
	// instance): it rides the dispatch context (tool_result_read resolves)
	// and a truncated observation is cached in full with the hash nudge
	// appended; nil degrades to plain truncation.
	ToolCache *agent.ToolResultCache
	// PauseChecker is a cooperative pause signal checked once per step
	// boundary; a true return stops the loop with ErrPaused.
	PauseChecker func(ctx context.Context) bool
	// ToolCallTimeout, when positive, bounds a SINGLE action's tool call:
	// the watchdog abandons the wait and the run fails with ErrToolTimeout
	// (naming the tool); pause/cancellation stay observable in flight. Zero
	// disables. Mirrors the host's timeouts.toolCallTimeout.
	ToolCallTimeout time.Duration
	// Logger; nil → slog.Default().
	Logger *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.MaxSteps <= 0 {
		c.MaxSteps = 16
	}
	if c.MaxObservationChars <= 0 {
		c.MaxObservationChars = 8000
	}
	if c.ObservationBudgetTokens <= 0 {
		c.ObservationBudgetTokens = 8192
	}
	if c.ObservationFillFraction <= 0 {
		c.ObservationFillFraction = 0.4
	}
	if c.StateByteLimit <= 0 {
		c.StateByteLimit = DefaultStateByteLimit
	}
	if c.SpinNudgeThreshold <= 0 {
		c.SpinNudgeThreshold = 3
	}
	if c.SpinAbortThreshold <= 0 {
		c.SpinAbortThreshold = 5
	}
	if c.PatchRetries <= 0 {
		c.PatchRetries = 1
	}
	if c.NotesPath == "" && c.TempDir != "" {
		c.NotesPath = filepath.Join(c.TempDir, NotesFileName)
	}
	if c.SpinAbortThreshold <= c.SpinNudgeThreshold {
		// Fail-safe ordering: the model gets at least one corrective nudge
		// before the loop stops.
		c.SpinAbortThreshold = c.SpinNudgeThreshold + 1
	}
	return c
}

// EffectiveMaxSteps reports the turn budget a run with this config would use
// (withDefaults applied). Hosts that render the budget must use this — a
// zero MaxSteps means "compiled-in default", not "zero turns".
func EffectiveMaxSteps(cfg Config) int {
	return cfg.withDefaults().MaxSteps
}

// EffectiveObservationCapChars reports the per-observation cap in characters
// (withDefaults applied): token-proportional with a resolved window —
// min(ObservationBudgetTokens, ObservationFillFraction × window) × 4 —
// else the plain MaxObservationChars fallback. Hosts that render or reason
// about the cap must use this, not the raw config fields.
func EffectiveObservationCapChars(cfg Config) int {
	c := cfg.withDefaults()
	if c.ContextWindowTokens <= 0 {
		return c.MaxObservationChars
	}
	budgetTokens := float64(c.ObservationBudgetTokens)
	fillTokens := c.ObservationFillFraction * float64(c.ContextWindowTokens)
	return int(math.Min(budgetTokens, fillTokens) * 4)
}

// stepResult is the outcome of one turn's LLM interaction: a valid e2s_step
// call with its applied state, an invalid turn after the bounded retry (the
// error becomes the next observation), or a fatal error.
type stepResult struct {
	// call is the validated step call (zero when invalid or fatal).
	call StepCall
	// state is Σ with the turn's patch applied (zero when invalid/fatal).
	state   E2SState
	applied bool
	// thought is the response reasoning text (Rₜ).
	thought string
	// usage is the token usage of the successful LLM call.
	usage llm.TokenUsage
	// invalid is non-nil when both the turn and its corrective retry failed
	// validation: no patch applied, nothing dispatched.
	invalid error
	// fatal is a terminal LLM error for the run.
	fatal error
}

// Loop runs the E2S cycle on raw sp4rk primitives (llm.Caller + Registry +
// Emitter) — deliberately without agent.Executor or the Conductor: every
// turn is a fresh one-shot dialog (O(1) in turns) and Σ evolves through the
// domain merge operator (ApplyRawPatch). See specs/domains/e2s.md.
type Loop struct {
	cfg      Config
	caller   llm.Caller
	registry Registry
	emitter  Emitter
	system   string
	toolDefs []llm.ToolDefinition
	// schemas maps tool name → raw input schema from the registry catalog,
	// powering the pre-dispatch validation of action.args
	// (sdktools.ValidateToolInput); built in New from the rendered catalog.
	schemas map[string]json.RawMessage
	// watchdogInterval is the pause-poll cadence while a tool call is in
	// flight (see executeToolCall). It lives on the instance — never in
	// package state — so a test's shortened cadence cannot leak package-wide.
	watchdogInterval time.Duration
}

// New creates an E2S loop. caller must be non-nil; a nil registry allows
// only finish-only runs; a nil emitter is a no-op.
func New(caller llm.Caller, registry Registry, emitter Emitter, cfg Config) *Loop {
	cfg = cfg.withDefaults()
	stepTool := NewStepTool()
	l := &Loop{
		cfg:              cfg,
		caller:           caller,
		registry:         registry,
		emitter:          emitter,
		watchdogInterval: defaultToolWatchdogInterval,
		toolDefs: []llm.ToolDefinition{{
			Name:        stepTool.Name(),
			Description: stepTool.Description(),
			InputSchema: stepTool.InputSchema(),
		}},
	}
	if descs := l.descriptors(); descs != nil {
		l.schemas = make(map[string]json.RawMessage, len(descs))
		for _, d := range descs {
			l.schemas[d.Name] = d.InputSchema
		}
	}
	l.system = BuildSystemPrompt(cfg, l.descriptors())
	return l
}

// descriptors returns the registry's tool catalog, or nil without a registry.
func (l *Loop) descriptors() []sdktools.ToolDescriptor {
	if l.registry == nil {
		return nil
	}
	return l.registry.List()
}

// log returns the configured logger or the default.
func (l *Loop) log() *slog.Logger {
	if l.cfg.Logger != nil {
		return l.cfg.Logger
	}
	return slog.Default()
}

// emit safely invokes f when an emitter is configured.
func (l *Loop) emit(f func(e Emitter)) {
	if l.emitter != nil {
		f(l.emitter)
	}
}

// Run executes the E2S cycle until finish, a stop condition, or a fatal
// error. The returned Result is non-nil even on error paths (checkpoint
// snapshot + trajectory); errors.Is(err, ErrPaused) distinguishes a
// cooperative pause, context.Canceled/DeadlineExceeded a cancellation.
func (l *Loop) Run(ctx context.Context) (*Result, error) {
	if l.cfg.ToolCache != nil {
		ctx = agent.WithToolResultCache(ctx, l.cfg.ToolCache)
	}
	state := l.seedState()
	// Seed-limit guard: an oversized initial Σ (huge objective, or a
	// checkpoint saved under a larger limit) would wedge every turn on
	// ErrStateTooLarge — fail fast with an actionable error
	// (specs/domains/e2s.md).
	if seedBytes := SigmaBytes(state.Sigma); seedBytes > l.cfg.StateByteLimit {
		err := fmt.Errorf("%w: the initial state Σ is %d bytes, over the %d-byte limit — shorten the task/objective (or attach the oversized content as a file and reference it) before re-running",
			ErrStateTooLarge, seedBytes, l.cfg.StateByteLimit)
		return l.checkpoint(RunStatusFailed, state, 0, nil), err
	}
	observation := initialObservation
	if l.cfg.ResumeNote != "" {
		observation = l.cfg.ResumeNote
	}
	steps := make([]agent.Step, 0, l.cfg.MaxSteps)
	spinCount := 0
	spinFingerprint := ""

	for turn := 1; turn <= l.cfg.MaxSteps; turn++ {
		// Step boundary: cancellation first, then the cooperative pause —
		// checked before any turn work, so a paused run consumes nothing.
		if err := ctx.Err(); err != nil {
			return l.checkpoint(RunStatusCanceled, state, turn-1, steps), err
		}
		if l.cfg.PauseChecker != nil && l.cfg.PauseChecker(ctx) {
			return l.checkpoint(RunStatusPaused, state, turn-1, steps), ErrPaused
		}

		started := time.Now()
		l.emit(func(e Emitter) { e.StepStart(turn) })

		res := l.turnCall(ctx, state, observation, turn)
		if res.fatal != nil {
			// A cancellation surfacing INSIDE the LLM call arrives as fatal —
			// the boundary checks already ran. Re-check ctx so a shutdown
			// checkpoints as a resumable cancellation instead of a failure
			// that drops the Σ (mirrors runGoalTurns's post-turn re-check).
			if err := ctx.Err(); err != nil {
				return l.checkpoint(RunStatusCanceled, state, turn-1, steps), err
			}
			return l.checkpoint(RunStatusFailed, state, turn-1, steps), res.fatal
		}
		l.reportResponse(turn, res, true)

		// Invalid turn (both attempts failed): the error becomes the next
		// observation; Σ untouched, nothing dispatched.
		if res.invalid != nil {
			obs := invalidTurnObservation(res.invalid)
			steps = append(steps, agent.Step{
				Thought:     res.thought,
				Action:      llm.ToolCall{Name: StepToolName},
				Observation: obs,
				IsError:     true,
			})
			l.syncTrajectory(steps)
			observation = obs
			l.emit(func(e Emitter) { e.StepComplete(turn, time.Since(started)) })
			continue
		}

		// The patch was applied inside turnCall; adopt the merged state.
		state = res.state
		if res.applied {
			l.emitState(turn, state)
		}

		// Finish: intercepted by the loop, never dispatched.
		if res.call.Action.IsFinish() {
			steps = append(steps, agent.Step{
				Thought: res.thought,
				Action: llm.ToolCall{
					ID:    res.call.ID,
					Name:  FinishActionName,
					Input: json.RawMessage(`{"answer":` + mustMarshalString(res.call.Action.Answer) + `}`),
				},
			})
			l.syncTrajectory(steps)
			l.emit(func(e Emitter) { e.Finishing(turn, res.call.Action.Answer) })
			out := l.checkpoint(RunStatusFinished, state, turn, steps)
			out.Answer = res.call.Action.Answer
			out.Finished = true
			return out, nil
		}

		// Anti-spin: semantic action fingerprint; identical consecutive
		// actions are nudged, then abort the loop.
		fp := ActionFingerprint(res.call.Action.Tool, res.call.Action.Args)
		if fp == spinFingerprint {
			spinCount++
		} else {
			spinCount = 1
			spinFingerprint = fp
		}
		if spinCount >= l.cfg.SpinAbortThreshold {
			l.emit(func(e Emitter) {
				e.ExecutorDiagnostic(turn, "spin_stop", map[string]any{
					"tool":            res.call.Action.Tool,
					"repeat_count":    spinCount,
					"abort_threshold": l.cfg.SpinAbortThreshold,
				})
			})
			l.log().Warn("e2s: anti-spin abort", "tool", res.call.Action.Tool, "repeat_count", spinCount)
			return l.checkpoint(RunStatusSpinStop, state, turn, steps), nil
		}
		if spinCount >= l.cfg.SpinNudgeThreshold {
			nudge := fmt.Sprintf(
				"You repeated the identical action (%s) %d times in a row. Do NOT repeat it again. Reassess the state, change the approach or the arguments, or call finish with your best answer.",
				res.call.Action.Tool, spinCount)
			l.emit(func(e Emitter) {
				e.ExecutorDiagnostic(turn, "spin_nudge", map[string]any{
					"tool":         res.call.Action.Tool,
					"repeat_count": spinCount,
				})
			})
			steps = append(steps, agent.Step{
				Thought:     res.thought,
				Action:      llm.ToolCall{ID: res.call.ID, Name: res.call.Action.Tool, Input: res.call.Action.Args},
				Observation: nudge,
				UserNudge:   nudge,
			})
			l.syncTrajectory(steps)
			observation = nudge
			l.emit(func(e Emitter) { e.StepComplete(turn, time.Since(started)) })
			continue
		}

		// Dispatch through the registry (all security gates). A watchdog
		// signal — ErrPaused in flight, ErrToolTimeout, ctx cancellation —
		// unwinds to the matching checkpoint; the incomplete action is NOT
		// appended to the trajectory, because a paused/timed-out tool keeps
		// running detached and its side effects are unknown. No verify-on-edit
		// flush here either: the trajectory is never replayed on resume, so
		// E2S verify-on-edit is per-turn only.
		step, dispatchErr := l.dispatch(ctx, turn, res.thought, res.call)
		if dispatchErr != nil {
			// A pause tripped while the tool was in flight: the same
			// recoverable checkpoint (the turn's patch already rides in
			// `state`).
			if errors.Is(dispatchErr, ErrPaused) {
				return l.checkpoint(RunStatusPaused, state, turn-1, steps), ErrPaused
			}
			// Cancellation inside the tool call: checkpoint as canceled
			// (resumable) rather than a Σ-dropping failure.
			if err := ctx.Err(); err != nil {
				return l.checkpoint(RunStatusCanceled, state, turn-1, steps), err
			}
			// A tool past its per-call ceiling (errors.Is-matchable against
			// ErrToolTimeout): terminal failure, not a hang.
			return l.checkpoint(RunStatusFailed, state, turn-1, steps), dispatchErr
		}
		steps = append(steps, step)
		l.syncTrajectory(steps)
		observation = step.Observation

		if err := ctx.Err(); err != nil {
			return l.checkpoint(RunStatusCanceled, state, turn, steps), err
		}
		l.emit(func(e Emitter) { e.StepComplete(turn, time.Since(started)) })
	}

	// Step budget exhausted.
	l.log().Info("e2s: step budget exhausted", "max_steps", l.cfg.MaxSteps)
	return l.checkpoint(RunStatusStepLimit, state, l.cfg.MaxSteps, steps), nil
}

// initialObservation is the turn-1 observation when no action ran yet.
const initialObservation = "(no observation yet — this is turn 1; act according to the task in your state)"

// turnCall performs the turn's LLM interaction: the one-shot call plus up to
// cfg.PatchRetries corrective retries (see specs/domains/e2s.md).
func (l *Loop) turnCall(ctx context.Context, state E2SState, observation string, turn int) stepResult {
	user := BuildUserMessage(state.Sigma, observation, turn, l.cfg.MaxSteps)

	var (
		lastResp    *llm.ChatResponse
		lastInvalid error
	)
	for attempt := 0; attempt <= l.cfg.PatchRetries; attempt++ {
		prompt := user
		if attempt > 0 {
			prompt = user + BuildCorrectionSuffix(lastInvalid)
		}
		resp, err := l.callLLM(ctx, prompt)
		if err != nil {
			return stepResult{fatal: err}
		}
		res, invalidErr := l.validateTurn(resp, state)
		if invalidErr == nil {
			return res
		}
		lastResp, lastInvalid = resp, invalidErr

		// A further retry is available: re-ask with the correction tail.
		if attempt < l.cfg.PatchRetries {
			l.log().Warn("e2s: invalid step call, retrying", "turn", turn, "attempt", attempt+1, "error", invalidErr)
			l.emit(func(e Emitter) {
				e.ExecutorDiagnostic(turn, "step_retry", map[string]any{"error": invalidErr.Error()})
			})
		}
	}

	// Retries exhausted: the error becomes the observation; Σ untouched.
	return stepResult{
		thought: thoughtOf(lastResp),
		usage:   lastResp.Usage,
		invalid: lastInvalid,
	}
}

// validateTurn parses the response and applies the patch through the domain
// merge operator. A non-nil error marks the turn invalid; the caller drives
// the bounded retry / error-as-observation path.
func (l *Loop) validateTurn(resp *llm.ChatResponse, state E2SState) (stepResult, error) {
	call, err := ParseStepCall(resp.Message.ToolCalls)
	if err != nil {
		return stepResult{}, err
	}
	merged, err := ApplyRawPatch(state, call.StatePatch, MergeOptions{
		StateByteLimit: l.cfg.StateByteLimit,
	})
	if err != nil {
		return stepResult{}, err
	}
	return stepResult{
		call:    call,
		state:   merged,
		applied: true,
		thought: thoughtOf(resp),
		usage:   resp.Usage,
	}, nil
}

// invalidTurnObservation renders the error-as-observation text used after a
// failed retry: the state is unchanged and the model sees exactly why.
func invalidTurnObservation(err error) string {
	return "Your previous " + StepToolName + " call was invalid and was NOT applied; the state is unchanged. Error: " + err.Error()
}

// thoughtOf extracts the response's reasoning text (Rₜ).
func thoughtOf(resp *llm.ChatResponse) string {
	if resp == nil {
		return ""
	}
	if resp.Reasoning != "" {
		return resp.Reasoning
	}
	return resp.Message.ReasoningContent
}

// callLLM performs the fresh one-shot request for a turn.
func (l *Loop) callLLM(ctx context.Context, user string) (*llm.ChatResponse, error) {
	userMsg := llm.Message{Role: "user", Content: user}
	if len(l.cfg.ContentBlocks) > 0 {
		userMsg.ContentBlocks = l.cfg.ContentBlocks
	}
	req := llm.ChatRequest{
		Model:           l.cfg.Model,
		Messages:        []llm.Message{{Role: "system", Content: l.system}, userMsg},
		Tools:           l.toolDefs,
		MaxTokens:       l.cfg.MaxTokens,
		ReasoningEffort: l.cfg.ReasoningEffort,
		CallPurpose:     llm.CallPurposeExecutor,
	}
	resp, err := l.caller.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("e2s llm call failed: %w", err)
	}
	if resp == nil {
		return nil, errors.New("e2s llm call returned nil response")
	}
	return resp, nil
}

// mustMarshalString JSON-encodes s for inline embedding; it cannot fail for
// a string.
func mustMarshalString(s string) string {
	data, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(data)
}

// reportResponse emits the per-response events: the finish answer as
// assistant chunk/done (emitAnswer is the accepted-finish gate; finishes are
// never vetoed, hosts pass true), the turn thought, and the context fill.
func (l *Loop) reportResponse(turn int, res stepResult, emitAnswer bool) {
	if emitAnswer && res.call.Action.IsFinish() && res.call.Action.Answer != "" {
		answer := res.call.Action.Answer
		l.emit(func(e Emitter) { e.AssistantChunk(answer) })
		l.emit(func(e Emitter) { e.AssistantDone(answer, res.usage.InputTokens, res.usage.OutputTokens) })
	}
	l.emit(func(e Emitter) { e.Thought(turn, res.thought, "") })
	if l.cfg.ContextWindowTokens > 0 {
		pct := float64(res.usage.InputTokens) / float64(l.cfg.ContextWindowTokens) * 100
		l.emit(func(e Emitter) { e.ContextFill(pct, res.usage.InputTokens, l.cfg.ContextWindowTokens, "ok", "") })
	}
}

// dispatch executes the validated action via the registry and synthesizes
// the trajectory step (truncation, cache-on-truncate, untrusted wrapping by
// tool class, the verify-on-edit note).
//
// A non-nil error is NOT a tool failure (those become error observations) —
// it is a watchdog signal (ErrPaused, ErrToolTimeout) or ctx cancellation;
// Run unwinds it into the matching checkpoint, the step is zero.
func (l *Loop) dispatch(ctx context.Context, turn int, thought string, call StepCall) (agent.Step, error) {
	argsPreview := strutil.TruncateUTF8(compactJSON(call.Action.Args), 200)
	l.emit(func(e Emitter) { e.ToolCall(turn, 0, call.Action.Tool, argsPreview, l.toolSource(call.Action.Tool)) })

	observation := ""
	isError := false
	// rawPreview mirrors the pre-wrap result for the UI ToolResult event.
	rawPreview := ""
	// editSucceeded arms the verify-on-edit run below: any successful
	// content-changing edit in this action (single or batch sub-call).
	editSucceeded := false
	if l.registry == nil {
		observation = "no tool registry configured for the E2S loop"
		isError = true
	} else {
		// Pre-dispatch structural validation: the provider never schema-checks
		// the envelope's free-form action.args, so a wrong parameter name would
		// be silently ignored by the target tool's json.Unmarshal. A violation
		// is an ACTION error observation — the patch stays applied and no
		// patch-retry budget is consumed.
		if schema, known := l.schemas[call.Action.Tool]; known {
			if verr := sdktools.ValidateToolInput(call.Action.Tool, schema, call.Action.Args); verr != nil {
				observation = "action arguments rejected: " + verr.Error()
				isError = true
			}
		}
		if !isError {
			if call.Action.Tool == sdktools.ToolBatch {
				// The batch meta-tool is intercepted by the loop (its registry
				// form always errors); each sub-call takes the same gated path.
				var rawJoined string
				var dispatchErr error
				observation, isError, rawJoined, editSucceeded, dispatchErr = l.dispatchBatch(ctx, call.Action.Args)
				if dispatchErr != nil {
					return agent.Step{}, dispatchErr
				}
				rawPreview = rawJoined
			} else {
				var dispatchErr error
				observation, isError, dispatchErr = l.executeSingle(ctx, call.Action.Tool, call.Action.Args)
				if dispatchErr != nil {
					return agent.Step{}, dispatchErr
				}
				rawPreview = observation
				editSucceeded = !isError && agent.IsFileEditTool(call.Action.Tool)
			}
		}
	}

	// Verify-on-edit, debounced per action (the executor's per-response-group
	// debounce): run once after this action's successful edit.
	if editSucceeded && l.cfg.EditVerify != nil {
		if note := agent.FormatVerifyNote(l.cfg.EditVerify(ctx), l.cfg.EditVerifyMaxChars); note != "" {
			observation += "\n\n" + note
			if rawPreview != "" {
				rawPreview += "\n\n" + note
			} else {
				rawPreview = note
			}
		}
	}

	capChars := EffectiveObservationCapChars(l.cfg)
	truncated := strutil.TruncateUTF8(observation, capChars)
	// Cache-on-truncate: cache the full result and append the standard
	// fragmentation nudge so the model recovers it via tool_result_read;
	// without a cache, plain truncation applies.
	if l.cfg.ToolCache != nil && utf8.RuneCountInString(observation) > capChars {
		meta := agent.ToolCacheMeta{Input: string(call.Action.Args)}
		hash := l.cfg.ToolCache.Store(call.Action.Tool, observation, meta)
		truncated += agent.FormatFragmentationNudge(hash, call.Action.Tool, 0)
	}
	// Wrapping is decided by TOOL CLASS (IsUntrusted), not result type: an
	// untrusted tool's error diagnostic is attacker-influenceable too
	// (specs/architecture/security-model.md, "Error-Recovery Carve-Out").
	if l.registry != nil && l.registry.IsToolUntrusted(call.Action.Tool) {
		truncated = untrustedWrap(call.Action.Tool, truncated)
	}

	// The event preview mirrors the raw pre-wrap result IN FULL (matching the
	// Conductor): untrusted-content boundaries are model-context markers, not
	// UI text, and the frontend renders its "N chars" badge from result_len —
	// a capped preview would contradict the badge and leave "Show more"
	// revealing nothing. Model-context truncation happened above, untouched.
	// See specs/contracts/event-catalog.md (tool_result).
	if rawPreview == "" {
		rawPreview = observation
	}
	l.emit(func(e Emitter) { e.ToolResult(turn, 0, len(observation), rawPreview, isError) })

	return agent.Step{
		Thought:     thought,
		Action:      llm.ToolCall{ID: call.ID, Name: call.Action.Tool, Input: call.Action.Args},
		Observation: truncated,
		IsError:     isError,
	}, nil
}

// toolSource resolves the tool-call event source: "core" for built-ins, the
// MCP source tag (mcp:<server>) otherwise; an empty lookup falls back to
// "core".
func (l *Loop) toolSource(name string) string {
	if l.registry == nil {
		return "core"
	}
	if src := l.registry.ToolSource(name); src != "" {
		return src
	}
	return "core"
}

// executeSingle runs one tool call through the bounded dispatch watchdog and
// returns the raw observation plus its error flag (registry errors and
// IsError results both surface as error observations); untrusted wrapping is
// the caller's decision. A non-nil third return is a watchdog/context signal
// (ErrPaused, ErrToolTimeout, ctx cancellation), not a tool failure — the
// observation/flag are meaningless on that path.
func (l *Loop) executeSingle(ctx context.Context, tool string, args json.RawMessage) (observation string, isError bool, dispatchErr error) {
	result, err := l.executeToolCall(ctx, tool, args)
	switch {
	case errors.Is(err, ErrPaused):
		return "", true, ErrPaused
	case errors.Is(err, ErrToolTimeout):
		return "", true, fmt.Errorf("tool %q: %w", tool, ErrToolTimeout)
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		// Propagate as dispatchErr so the run checkpoints as canceled instead
		// of appending a fabricated failure step for a merely interrupted call.
		return "", true, err
	case err != nil:
		return fmt.Sprintf("tool execution error: %v", err), true, nil
	case result.IsError:
		return result.Content, true, nil
	default:
		return result.Content, false, nil
	}
}

// dispatchBatch executes the batch meta-tool's sub-calls sequentially, each
// through the full single-dispatch path (schema validation, gated execution,
// per-sub-result cache-on-truncate, untrusted wrapping); results join into
// one numbered observation. Per-call tool FAILURES never abort the batch;
// nested batch and the envelope targets (e2s_step, finish) are rejected
// fail-closed — finish must stay top-level or the loop's interception could
// be bypassed. The watchdog error unwinds the whole action (the batch is one
// E2S action). See specs/domains/e2s.md.
func (l *Loop) dispatchBatch(ctx context.Context, args json.RawMessage) (observation string, anyError bool, rawJoined string, editSucceeded bool, dispatchErr error) {
	var input struct {
		Calls []struct {
			Tool  string          `json:"tool"`
			Input json.RawMessage `json:"input"`
		} `json:"calls"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "batch parse error: " + err.Error(), true, "", false, nil //nolint:nilerr // a malformed batch envelope becomes an error observation, not a run-terminating error
	}
	if len(input.Calls) == 0 {
		return "batch: no calls provided (empty calls array)", true, "", false, nil
	}

	// The same effective observation cap a single action sees: each
	// oversized sub-result truncates against it individually; the join goes
	// through it once more as the last barrier.
	capChars := EffectiveObservationCapChars(l.cfg)

	// sb carries the model-facing join (each untrusted sub-result wrapped);
	// raw carries the pre-wrap join for the UI preview.
	var sb strings.Builder
	var raw strings.Builder
	anyError = false
	for i, sub := range input.Calls {
		fmt.Fprintf(&sb, "[batch result %d/%d — %s]\n", i+1, len(input.Calls), sub.Tool)
		fmt.Fprintf(&raw, "[batch result %d/%d — %s]\n", i+1, len(input.Calls), sub.Tool)
		switch sub.Tool {
		case sdktools.ToolBatch:
			sb.WriteString("error: batch cannot be nested inside another batch call\n\n")
			raw.WriteString("error: batch cannot be nested inside another batch call\n\n")
			anyError = true
			continue
		case StepToolName, FinishActionName:
			msg := fmt.Sprintf("error: %q cannot be used inside a batch; it must be the top-level action\n\n", sub.Tool)
			sb.WriteString(msg)
			raw.WriteString(msg)
			anyError = true
			continue
		}
		if schema, known := l.schemas[sub.Tool]; known {
			if verr := sdktools.ValidateToolInput(sub.Tool, schema, sub.Input); verr != nil {
				msg := "action arguments rejected: " + verr.Error() + "\n\n"
				sb.WriteString(msg)
				raw.WriteString(msg)
				anyError = true
				continue
			}
		}
		content, subErr, execErr := l.executeSingle(ctx, sub.Tool, sub.Input)
		if execErr != nil {
			// Watchdog signal (pause/timeout) mid-batch: unwind the whole
			// action. A per-call tool FAILURE (subErr) does not abort the batch.
			return "", true, "", false, execErr
		}
		if subErr {
			anyError = true
		} else if agent.IsFileEditTool(sub.Tool) {
			editSucceeded = true
		}
		// Per-sub-result cache-on-truncate (ADR-083): an oversized sub-result
		// is cached IN FULL under its OWN hash, so three big reads yield three
		// independently recoverable fragments instead of one join-level entry;
		// the join-level cap in dispatch stays the last barrier.
		modelContent := content
		if utf8.RuneCountInString(content) > capChars {
			modelContent = strutil.TruncateUTF8(content, capChars)
			if l.cfg.ToolCache != nil {
				meta := agent.ToolCacheMeta{Input: string(sub.Input)}
				hash := l.cfg.ToolCache.Store(sub.Tool, content, meta)
				modelContent += agent.FormatFragmentationNudge(hash, sub.Tool, 0)
			}
		}
		if l.registry.IsToolUntrusted(sub.Tool) {
			modelContent = untrustedWrap(sub.Tool, modelContent)
		}
		sb.WriteString(modelContent)
		sb.WriteString("\n\n")
		raw.WriteString(content)
		raw.WriteString("\n\n")
	}
	return strings.TrimRight(sb.String(), "\n"), anyError, strings.TrimRight(raw.String(), "\n"), editSucceeded, nil
}

// emitState publishes the full Σ + turn snapshot via the optional
// StateEmitter capability (skipped silently when unsupported). `turn` is
// RUN-LOCAL (a resumed run restarts at 1); `total_turns` is the cumulative
// domain TurnCount (the persistence continuation point); max_turns/status
// feed the UI's turn/budget counter and status badge.
func (l *Loop) emitState(turn int, state E2SState) {
	if l.emitter == nil {
		return
	}
	if se, ok := l.emitter.(StateEmitter); ok {
		se.E2SState(map[string]any{
			"state":       state.Sigma,
			"turn":        turn,
			"total_turns": state.TurnCount,
			"max_turns":   l.cfg.MaxSteps,
			"status":      string(state.Status),
		})
	}
}

// syncTrajectory pushes the trajectory to the configured store.
func (l *Loop) syncTrajectory(steps []agent.Step) {
	if l.cfg.Trajectory != nil {
		l.cfg.Trajectory.Sync(steps)
	}
}

// seedState builds the initial domain state: a resumed checkpoint verbatim
// (ResumeState), or the canonical core schema with the task objective plus
// InitialState extension seeds.
func (l *Loop) seedState() E2SState {
	if l.cfg.ResumeState != nil {
		resumed := *l.cfg.ResumeState
		resumed.Sigma = maps.Clone(l.cfg.ResumeState.Sigma)
		if resumed.Sigma == nil {
			resumed.Sigma = map[string]any{}
		}
		return resumed
	}
	state := NewE2SState(l.cfg.Task, time.Time{})
	for k, v := range l.cfg.InitialState {
		if !IsCoreKey(k) {
			state.Sigma[k] = v
		}
	}
	return state
}

// checkpoint assembles a terminal Result snapshot.
func (l *Loop) checkpoint(status RunStatus, state E2SState, turns int, steps []agent.Step) *Result {
	return &Result{
		Status:   status,
		State:    state.Sigma,
		Snapshot: state,
		Turns:    turns,
		Steps:    steps,
	}
}
