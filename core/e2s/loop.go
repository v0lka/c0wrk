package e2s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/strutil"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ErrPaused is returned by Loop.Run when a cooperative pause signal trips at
// a step boundary. It is a recoverable checkpoint, not a failure: the result
// carries the state snapshot, the trajectory collected so far, and
// RunStatusPaused, and the host may resume later by re-running with the
// returned snapshot's Sigma seeded via Config.ResumeState (the same
// checkpoint Run returns in Result.Snapshot).
var ErrPaused = errors.New("e2s loop paused at step boundary")

// RunStatus is the terminal disposition of an E2S loop run. It is the LOOP's
// status (why Run returned), distinct from the domain StateStatus — the
// model-managed core Σ key that the run's lifecycle mirrors.
type RunStatus string

const (
	// RunStatusFinished — the model called the finish action; Answer is set.
	RunStatusFinished RunStatus = "finished"
	// RunStatusStepLimit — the step budget was exhausted before finish.
	RunStatusStepLimit RunStatus = "step_limit"
	// RunStatusSpinStop — the anti-spin detector aborted the loop after the
	// model repeated the identical action past the abort threshold.
	RunStatusSpinStop RunStatus = "spin_stop"
	// RunStatusPaused — a cooperative pause checkpoint at a step boundary.
	RunStatusPaused RunStatus = "paused"
	// RunStatusCanceled — the context was canceled mid-run.
	RunStatusCanceled RunStatus = "canceled"
	// RunStatusFailed — a fatal error (e.g. LLM call failure) ended the run;
	// the accompanying error is non-nil.
	RunStatusFailed RunStatus = "failed"
)

// Result is the outcome of an E2S run.
type Result struct {
	// Answer is the final answer delivered by the finish action (empty for
	// non-finished terminations).
	Answer string `json:"answer"`
	// Status is the terminal disposition.
	Status RunStatus `json:"status"`
	// Finished is true only for RunStatusFinished.
	Finished bool `json:"finished"`
	// State is the final working state Σ (the Snapshot's Sigma).
	State map[string]any `json:"state"`
	// Snapshot is the full domain state (Σ + schema fingerprint + turn
	// count + lifecycle status) — the resumable checkpoint for a paused run.
	Snapshot E2SState `json:"snapshot"`
	// Turns is the number of completed turns (LLM steps).
	Turns int `json:"turns"`
	// Steps is the synthesized agent.Step trajectory (Thought, Action,
	// Observation per step) for the trajectory store.
	Steps []agent.Step `json:"steps"`
}

// Emitter is the event surface the E2S loop requires. It is a structural
// subset of github.com/v0lka/sp4rk/agent.Events, so every agent.Events /
// core.Emitter implementation satisfies it without adaptation — the loop
// deliberately declares its own minimal interface instead of importing the
// host interface to stay decoupled from the core package (import cycle).
//
// A nil emitter is treated as a no-op.
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

// StateEmitter is an optional Emitter capability for the loop's live state
// snapshots. When the underlying emitter implements it, the loop emits the
// full Σ + turn snapshot after every applied state patch (the e2s_state
// event); otherwise snapshots are skipped silently (graceful degradation).
type StateEmitter interface {
	E2SState(data map[string]any)
}

// Registry is the tool surface the E2S loop requires: the dispatch target
// for actions (inheriting every security gate the registry applies — group
// policies, judge, HITL confirmation, verify-on-edit) and the descriptor
// catalog for the Available Tools section. ToolSource mirrors
// agent.ToolExecutor.GetToolSource: "core" for built-ins, the MCP source
// tag (mcp:<server>) otherwise — the loop emits it as the tool-call event
// source so the UI renders real MCP tools as MCP and everything else as
// built-in.
// Satisfied by *core/tools.ToolRegistry (which embeds sp4rk's ToolRegistry)
// and by test doubles.
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
	// MaxSteps caps the number of turns (LLM steps). Default 50 from
	// e2s.max_steps; 16 when the config supplies no value (withDefaults
	// fallback).
	MaxSteps int
	// MaxObservationChars caps an observation fed back to the model. Default
	// 4000 runes when the config supplies no value (withDefaults fallback);
	// the shipped e2s.observation_truncate default is 2000.
	MaxObservationChars int
	// StateByteLimit caps the JSON-encoded Σ (ApplyPatch's byte limit).
	// Default DefaultStateByteLimit. A patch whose merged Σ exceeds the cap
	// is rejected (validation error path: bounded retry, then error
	// observation).
	StateByteLimit int
	// SpinNudgeThreshold is the count of consecutive identical actions at
	// which a nudge observation is injected (the redundant action is not
	// dispatched again). Default 3.
	SpinNudgeThreshold int
	// SpinAbortThreshold is the count of consecutive identical actions at
	// which the loop aborts with RunStatusSpinStop. Default 5.
	SpinAbortThreshold int
	// PatchRetries bounds the corrective re-requests for an invalid turn
	// (bad envelope or rejected patch) before the failure becomes the next
	// observation. Default 1 (a single corrective retry); values <= 0 fall
	// back to the default.
	PatchRetries int
	// ReasoningEffort, when non-empty, is passed through to every
	// ChatRequest (the per-message override / Model Profiles sampling profile
	// value the host resolved) — mirroring the executor's per-run
	// SetReasoningEffort. Empty = the provider default.
	ReasoningEffort string
	// SystemPrompt, when non-empty, replaces the compiled-in
	// prompts.E2SSystem core directive (the Model Profiles Lite swap: the host
	// passes prompts.E2SSystemLite when the profile's prompt variant is
	// active). Empty = prompts.E2SSystem.
	SystemPrompt string
	// InjectionDefense appends the config-gated prompts.InjectionDefense
	// directive (plus the unconditional prompts.VerificationMandate) to the
	// system prompt, mirroring the Conductor's security prefix — SECURITY.md
	// mandates the directive for every model-facing loop, not just the
	// Conductor.
	InjectionDefense bool
	// AgentSections carries pre-rendered "## Available Subagents" /
	// "## Requested Subagents" prompt sections (the host renders them from
	// the discovered profile catalog and any explicit #agent mentions).
	// Appended after the delegation directive so explicit user requests keep
	// their mandatory-delegation force in E2S mode.
	AgentSections string
	// FinishGuard, when non-nil, is consulted before a finish action is
	// accepted. A non-nil error vetoes the finish for that turn: the error
	// becomes the next observation and the run continues (the model must
	// resolve the blocker — e.g. pending async delegations — or finish
	// later). Mirrors the executor's finish-join guard.
	FinishGuard func(ctx context.Context) error
	// EditVerify, when non-nil, is the config-authored verify-on-edit
	// runner: after any turn whose action (or batch sub-call) contains a
	// successful write_file/edit_file, the runner executes once and its
	// formatted note is appended to the observation — the same hook the
	// executor installs via SetVerifyOnEdit.
	EditVerify agent.EditVerifyRunner
	// EditVerifyMaxChars caps the injected verification output;
	// <= 0 selects agent.DefaultVerifyOnEditCap.
	EditVerifyMaxChars int
	// ContextWindowTokens, when > 0, enables the flat per-request
	// ContextFill emission (last request input tokens vs the window). Zero
	// disables the emission.
	ContextWindowTokens int
	// Task is the run objective: it seeds the core "objective" Σ key (Σ is
	// the model's only memory, so the objective must live there).
	Task string
	// InitialState seeds extension keys of the initial Σ (copied; never
	// mutated). Non-core keys are added as extensions; the core schema is
	// built canonically by NewE2SState.
	InitialState map[string]any
	// ResumeState, when non-nil, seeds the ENTIRE run state instead of a
	// fresh NewE2SState: the checkpoint (a previous Run's Result.Snapshot)
	// continues with its Σ — core keys, extensions, turn count, and
	// bookkeeping — exactly where it stopped. Takes precedence over Task +
	// InitialState; a resumed run therefore inherits the accumulated working
	// state (findings, decisions, files_touched …), not just extension keys.
	ResumeState *E2SState
	// ResumeNote, when non-empty, replaces the generic turn-1 observation on a
	// resumed run. The E2S turn exposes no separate message channel (the model
	// sees only Σ + O), so a user's follow-up sent into a paused/interrupted
	// task — a nudge-resume — has nowhere else to go; delivering it as the
	// first observation is what keeps it from being silently dropped.
	ResumeNote string
	// WorkspacePath and TempDir render the workspace containment sections
	// of the system prompt.
	WorkspacePath string
	TempDir       string
	// DelegateDirective is an optional "## Delegation" section body for the
	// system prompt (e.g. how/when to use the delegate tool).
	DelegateDirective string
	// Skills are the active skill sections injected into the system prompt.
	Skills []SkillSection
	// ContentBlocks, when non-empty, are attached to every turn's user message
	// (image attachments staged for the run). Each turn is a fresh one-shot
	// [system, user] request, so the blocks are re-sent every turn — otherwise
	// the model would lose sight of them after turn 1. The per-turn text (Σ +
	// observation) stays in Message.Content; the provider prepends it as a
	// text block when the blocks carry no text, so it always reaches the model.
	ContentBlocks []llm.ContentBlock
	// TrajectoryStore, when non-nil, is synced with the growing step
	// trajectory after every completed step.
	Trajectory agent.TrajectoryStore
	// ToolCache is the shared tool-result cache (the same instance the
	// Conductor's executor uses). When non-nil: (a) the dispatch context
	// carries it so tool_result_read actions resolve, and (b) an
	// observation truncated by MaxObservationChars is cached in full and the
	// truncation nudge with the cache hash is appended — the model recovers
	// the dropped content via tool_result_read instead of re-running the
	// tool. Nil degrades to the legacy plain truncation (test doubles).
	ToolCache *agent.ToolResultCache
	// PauseChecker is a cooperative pause signal checked once per step
	// boundary; a true return stops the loop with ErrPaused.
	PauseChecker func(ctx context.Context) bool
	// Logger; nil → slog.Default().
	Logger *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.MaxSteps <= 0 {
		c.MaxSteps = 16
	}
	if c.MaxObservationChars <= 0 {
		c.MaxObservationChars = 4000
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
	if c.SpinAbortThreshold <= c.SpinNudgeThreshold {
		// Fail-safe ordering: abort must strictly exceed nudge so the model
		// gets at least one corrective nudge before the loop stops.
		c.SpinAbortThreshold = c.SpinNudgeThreshold + 1
	}
	return c
}

// EffectiveMaxSteps reports the turn budget a run with this config would
// use (withDefaults applied). Hosts that render the budget to the user
// (e.g. the step-limit wrap-up message) must use this, not the raw config
// value — a zero MaxSteps means "compiled-in default", not "zero turns".
func EffectiveMaxSteps(cfg Config) int {
	return cfg.withDefaults().MaxSteps
}

// stepResult is the outcome of one turn's LLM interaction: either a valid
// e2s_step call with its applied state, or a marker that the turn was
// invalid after the bounded retry (error becomes the observation), or a
// fatal error.
type stepResult struct {
	// call is the validated step call (zero when invalid or fatal).
	call StepCall
	// state is Σ with the turn's patch applied (ApplyRawPatch result; the
	// domain TurnCount is bumped). Zero value only when invalid/fatal.
	state E2SState
	// applied reports whether the patch merge succeeded (always true when
	// call is valid — kept explicit for readability).
	applied bool
	// thought is the response reasoning text (Rₜ).
	thought string
	// usage is the token usage of the successful LLM call.
	usage llm.TokenUsage
	// invalid is non-nil when both the turn and its corrective retry failed
	// validation: no patch is applied, no action dispatched; the error
	// becomes the next observation.
	invalid error
	// fatal is a terminal LLM error for the run.
	fatal error
}

// Loop runs the E2S cycle on raw sp4rk primitives (llm.Caller + Registry +
// Emitter). It deliberately does NOT use agent.Executor or the Conductor:
// every turn is a fresh one-shot [system, user] dialog, so the request size
// is O(1) in the number of turns. State evolves through the domain merge
// operator (ApplyRawPatch), which owns patch validation and rollback.
type Loop struct {
	cfg      Config
	caller   llm.Caller
	registry Registry
	emitter  Emitter
	system   string
	toolDefs []llm.ToolDefinition
	// schemas maps tool name → raw input schema from the registry catalog,
	// powering the pre-dispatch structural validation of action.args (see
	// sdktools.ValidateToolInput). Built once in New from the same descriptor
	// list the system prompt renders.
	schemas map[string]json.RawMessage
}

// New creates an E2S loop. caller must be non-nil. A nil registry allows
// only finish-only runs (any dispatch attempt surfaces an error
// observation); a nil emitter is treated as a no-op.
func New(caller llm.Caller, registry Registry, emitter Emitter, cfg Config) *Loop {
	cfg = cfg.withDefaults()
	stepTool := NewStepTool()
	l := &Loop{
		cfg:      cfg,
		caller:   caller,
		registry: registry,
		emitter:  emitter,
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
// error. The returned Result is non-nil even on error paths (carrying the
// checkpoint snapshot and trajectory); errors.Is(err, ErrPaused)
// distinguishes a cooperative pause, context.Canceled/DeadlineExceeded a
// cancellation.
func (l *Loop) Run(ctx context.Context) (*Result, error) {
	// The shared tool-result cache rides on the dispatch context so every
	// action (notably tool_result_read) resolves it, mirroring the
	// executor's WithToolResultCache injection.
	if l.cfg.ToolCache != nil {
		ctx = agent.WithToolResultCache(ctx, l.cfg.ToolCache)
	}
	state := l.seedState()
	// Seed-limit guard: an oversized initial Σ (a huge objective, or a
	// resume checkpoint created under a larger byte limit) can never accept
	// even an empty patch — every merge exceeds the cap, every turn fails
	// validation, and the run burns its whole budget wedging on
	// ErrStateTooLarge. Fail fast instead, with an actionable error.
	if seedBytes := SigmaBytes(state.Sigma); seedBytes > l.cfg.StateByteLimit {
		err := fmt.Errorf("%w: the initial state Σ is %d bytes, over the %d-byte limit — shorten the task/objective (or attach the oversized content as a file and reference it) before re-running",
			ErrStateTooLarge, seedBytes, l.cfg.StateByteLimit)
		return l.checkpoint(RunStatusFailed, state, 0, nil), err
	}
	observation := initialObservation
	if l.cfg.ResumeNote != "" {
		// A resumed run carrying a user follow-up delivers it as the turn-1
		// observation so the model reacts to it instead of silently continuing
		// the old objective (Σ precedence never lets the follow-up in otherwise).
		observation = l.cfg.ResumeNote
	}
	steps := make([]agent.Step, 0, l.cfg.MaxSteps)
	spinCount := 0
	spinFingerprint := ""

	for turn := 1; turn <= l.cfg.MaxSteps; turn++ {
		// Step boundary: cancellation first, then the cooperative pause —
		// the checks run before any work of the turn so a paused run
		// consumes nothing.
		if err := ctx.Err(); err != nil {
			return l.checkpoint(RunStatusCanceled, state, turn-1, steps), err
		}
		if l.cfg.PauseChecker != nil && l.cfg.PauseChecker(ctx) {
			return l.checkpoint(RunStatusPaused, state, turn-1, steps), ErrPaused
		}

		started := time.Now()
		l.emit(func(e Emitter) { e.StepStart(turn) })

		// One LLM call per step (plus at most one bounded corrective retry):
		// a fresh [system, user] dialog — no history from previous turns.
		res := l.turnCall(ctx, state, observation, turn)
		if res.fatal != nil {
			// A cancellation that surfaced INSIDE the LLM call (the task
			// context was canceled while the request was in flight) arrives
			// here as a fatal error — the step-boundary checks above already
			// ran. Re-check the context so a shutdown is checkpointed as a
			// cancellation (non-terminal, resumable) instead of a failure
			// that would drop the Σ on restart. Mirrors runGoalTurns's
			// post-turn ctx.Err() re-check.
			if err := ctx.Err(); err != nil {
				return l.checkpoint(RunStatusCanceled, state, turn-1, steps), err
			}
			return l.checkpoint(RunStatusFailed, state, turn-1, steps), res.fatal
		}
		// Finish-guard veto is computed BEFORE reportResponse so a rejected
		// finish never emits its answer as an assistant message (the UI must
		// not show a delivered answer for a run that continues).
		var finishVeto error
		if res.invalid == nil && res.call.Action.IsFinish() && l.cfg.FinishGuard != nil {
			finishVeto = l.cfg.FinishGuard(ctx)
		}
		l.reportResponse(turn, res, finishVeto == nil)

		// Invalid turn (both attempts failed): the error becomes the next
		// observation; Σ is untouched (ApplyPatch never mutated it), nothing
		// is dispatched.
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

		// The patch was validated and applied inside turnCall; adopt the
		// merged state and publish the full Σ + turn snapshot.
		state = res.state
		if res.applied {
			l.emitState(turn, state)
		}

		// Finish: intercepted by the loop, never dispatched. The finish
		// guard (when configured) vets the finish first — e.g. pending
		// async delegations must not be silently abandoned. A veto is NOT a
		// validation error (no retry budget consumed): the reason becomes
		// the next observation and the run continues.
		if res.call.Action.IsFinish() {
			if finishVeto != nil {
				steps = append(steps, agent.Step{
					Thought:     res.thought,
					Action:      llm.ToolCall{ID: res.call.ID, Name: FinishActionName},
					Observation: "finish rejected: " + finishVeto.Error(),
					IsError:     true,
				})
				l.syncTrajectory(steps)
				observation = "finish rejected: " + finishVeto.Error()
				l.emit(func(e Emitter) {
					e.ExecutorDiagnostic(turn, "finish_vetoed", map[string]any{"error": finishVeto.Error()})
				})
				l.emit(func(e Emitter) { e.StepComplete(turn, time.Since(started)) })
				continue
			}
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

		// Anti-spin: fingerprint on tool + canonical args; identical
		// consecutive actions are nudged, then abort the loop.
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

		// Dispatch the action through the registry (all security gates).
		step := l.dispatch(ctx, turn, res.thought, res.call)
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

// turnCall performs the turn's LLM interaction: the one-shot call, and — when
// the response fails validation — up to cfg.PatchRetries corrective retries
// (default 1) with the same Σ + Oₜ plus a correction tail describing the
// failure. After the retries are exhausted the failure becomes the next
// observation (Σ untouched).
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

		// A further retry is available: log/emit it and re-ask with the tail.
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

// validateTurn parses and validates a response against the current state,
// applying the patch through the domain merge operator. A non-nil error
// means the turn is invalid (bad envelope or rejected patch — ApplyPatch
// guarantees Σ was not mutated) — the caller drives the bounded retry /
// error-as-observation path with it.
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
	// Image attachments ride on every turn's user message: each turn is a
	// fresh dialog, so the blocks are re-attached or the model would lose
	// sight of them after turn 1. The provider prepends Content as a text
	// block when the blocks carry no text, so Σ + observation still reach it.
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
// assistant chunk/done (only when the finish is actually accepted — a
// guard-vetoed finish must not render as a delivered answer), the turn
// thought, and the flat context fill.
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
// the trajectory step. The observation is the truncated result, wrapped in
// untrusted-content tags when the source tool is untrusted. When the action
// (or a batch sub-call) contains a successful file edit and the host
// configured a verify-on-edit runner, the verification note is appended to
// the observation exactly as the executor appends it to the group's last
// observation.
func (l *Loop) dispatch(ctx context.Context, turn int, thought string, call StepCall) agent.Step {
	argsPreview := strutil.TruncateUTF8(compactJSON(call.Action.Args), 200)
	l.emit(func(e Emitter) { e.ToolCall(turn, 0, call.Action.Tool, argsPreview, l.toolSource(call.Action.Tool)) })

	observation := ""
	isError := false
	// rawPreview mirrors the pre-wrap result for the UI ToolResult event —
	// untrusted-content tags belong to the model context, not the preview.
	rawPreview := ""
	// editSucceeded reports whether a content-changing file edit succeeded
	// in this action (single, or any batch sub-call), arming the debounced
	// verify-on-edit run below.
	editSucceeded := false
	if l.registry == nil {
		observation = "no tool registry configured for the E2S loop"
		isError = true
	} else {
		// Pre-dispatch structural validation: the e2s_step envelope's
		// action.args is a free-form object the provider never schema-checks,
		// so wrong parameter names would be silently ignored by the target
		// tool's json.Unmarshal (defaults applied, bewildering result). A
		// violation is an ACTION error observation — the turn's patch stays
		// applied and no patch-retry budget is consumed; the model corrects
		// the arguments next turn using the valid-parameter list.
		if schema, known := l.schemas[call.Action.Tool]; known {
			if verr := sdktools.ValidateToolInput(call.Action.Tool, schema, call.Action.Args); verr != nil {
				observation = "action arguments rejected: " + verr.Error()
				isError = true
			}
		}
		if !isError {
			if call.Action.Tool == sdktools.ToolBatch {
				// The batch meta-tool is intercepted by the loop (its registry
				// form always errors): each sub-call dispatches through the
				// same registry path, inheriting every security gate.
				var rawJoined string
				observation, isError, rawJoined, editSucceeded = l.dispatchBatch(ctx, call.Action.Args)
				rawPreview = rawJoined
			} else {
				observation, isError = l.executeSingle(ctx, call.Action.Tool, call.Action.Args)
				rawPreview = observation
				editSucceeded = !isError && agent.IsFileEditTool(call.Action.Tool)
			}
		}
	}

	// Verify-on-edit (debounced per action, mirroring the executor's
	// per-response-group debounce): run once after any successful edit in
	// this action and append the formatted note to the observation.
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

	truncated := strutil.TruncateUTF8(observation, l.cfg.MaxObservationChars)
	// Cache-on-truncate: the full raw result goes into the shared cache and
	// the standard fragmentation nudge (identical format to the Conductor's
	// executor) tells the model how to recover the dropped content via
	// tool_result_read. Without a cache the legacy plain truncation applies.
	if l.cfg.ToolCache != nil && utf8.RuneCountInString(observation) > l.cfg.MaxObservationChars {
		meta := agent.ToolCacheMeta{Input: string(call.Action.Args)}
		hash := l.cfg.ToolCache.Store(call.Action.Tool, observation, meta)
		truncated += agent.FormatFragmentationNudge(hash, call.Action.Tool, 0)
	}
	// Wrapping is decided by TOOL CLASS (IsUntrusted), not by result type: an
	// untrusted tool's error diagnostic is attacker-influenceable too and must
	// be delivered inside the boundary exactly like successful output (see
	// specs/architecture/security-model.md, "Error-Recovery Carve-Out").
	if l.registry != nil && l.registry.IsToolUntrusted(call.Action.Tool) {
		truncated = untrustedWrap(call.Action.Tool, truncated)
	}

	// The event preview mirrors the raw (pre-wrap) result IN FULL, matching the
	// Conductor (executor_run passes the whole observation as the preview): the
	// <untrusted-content> boundary belongs to the model context, not the UI
	// preview — including the batch path, whose per-sub-call wrappers are
	// model-context boundaries, not UI text. The preview must NOT be capped
	// here: the frontend renders the "N chars" badge from result_len (the full
	// observation's length) beside the preview body, so a capped preview makes
	// the card self-contradictory and leaves the tail of the result unreachable
	// (a "Show more" that reveals nothing). Truncation for the MODEL context
	// (truncated, MaxObservationChars + cache-on-truncate) happens above and is
	// untouched.
	if rawPreview == "" {
		rawPreview = observation
	}
	l.emit(func(e Emitter) { e.ToolResult(turn, 0, len(observation), rawPreview, isError) })

	return agent.Step{
		Thought:     thought,
		Action:      llm.ToolCall{ID: call.ID, Name: call.Action.Tool, Input: call.Action.Args},
		Observation: truncated,
		IsError:     isError,
	}
}

// toolSource resolves the tool-call event source: "core" for built-ins, the
// MCP source tag (mcp:<server>) otherwise (an empty lookup falls back to
// "core" so the UI renders builtin tools as builtins — the previous
// hardcoded literal made every E2S tool render as an MCP card with an MCP
// badge).
func (l *Loop) toolSource(name string) string {
	if l.registry == nil {
		return "core"
	}
	if src := l.registry.ToolSource(name); src != "" {
		return src
	}
	return "core"
}

// executeSingle runs one tool call through the registry and returns the raw
// observation plus its error flag (registry errors and IsError results both
// surface as error observations). Untrusted wrapping is NOT applied here —
// the caller decides (single dispatch wraps the whole observation; batch
// wraps each sub-result individually).
func (l *Loop) executeSingle(ctx context.Context, tool string, args json.RawMessage) (string, bool) {
	result, err := l.registry.Execute(ctx, tool, args)
	switch {
	case err != nil:
		return fmt.Sprintf("tool execution error: %v", err), true
	case result.IsError:
		return result.Content, true
	default:
		return result.Content, false
	}
}

// dispatchBatch executes the batch meta-tool's sub-calls sequentially. Each
// sub-call runs the full single-dispatch path — schema validation (against
// the sub-tool's own schema), registry execution with every security gate,
// and per-sub-call untrusted wrapping by tool class — and the numbered
// results are joined into one observation. Per-call errors never abort the
// batch (mirroring the executor's batch semantics); nested batch and the
// E2S envelope targets (e2s_step, finish) are rejected fail-closed: finish
// must stay a top-level action or the loop's finish interception could be
// bypassed. Returns the wrapped observation, its error flag, the raw
// pre-wrap join (UI preview), and whether any file edit succeeded.
func (l *Loop) dispatchBatch(ctx context.Context, args json.RawMessage) (observation string, anyError bool, rawJoined string, editSucceeded bool) {
	var input struct {
		Calls []struct {
			Tool  string          `json:"tool"`
			Input json.RawMessage `json:"input"`
		} `json:"calls"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "batch parse error: " + err.Error(), true, "", false
	}
	if len(input.Calls) == 0 {
		return "batch: no calls provided (empty calls array)", true, "", false
	}

	// sb carries the model-facing join (each untrusted sub-result wrapped);
	// raw carries the same join pre-wrap for the UI preview — the
	// untrusted-content boundaries are model-context markers, not UI text.
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
		content, subErr := l.executeSingle(ctx, sub.Tool, sub.Input)
		if subErr {
			anyError = true
		} else if agent.IsFileEditTool(sub.Tool) {
			editSucceeded = true
		}
		rawContent := content
		if l.registry.IsToolUntrusted(sub.Tool) {
			content = untrustedWrap(sub.Tool, content)
		}
		sb.WriteString(content)
		sb.WriteString("\n\n")
		raw.WriteString(rawContent)
		raw.WriteString("\n\n")
	}
	return strings.TrimRight(sb.String(), "\n"), anyError, strings.TrimRight(raw.String(), "\n"), editSucceeded
}

// emitState publishes the full Σ + turn snapshot via the optional
// StateEmitter capability (skipped silently when unsupported). `turn` is the
// RUN-LOCAL turn (the loop's own counter, coherent with max_turns — a
// resumed run restarts at 1 against its fresh budget); `total_turns` carries
// the domain TurnCount (cumulative applied patches across all runs of the
// task — the persistence continuation point). max_turns carries the run's
// turn budget (a zero value means unbudgeted) and status the current domain
// lifecycle status, so the UI can render a turn/budget counter and a status
// badge without inferring them from Σ.
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
// (ResumeState — core keys, extensions, and turn count all continue), or the
// canonical core schema with the task as the objective plus extension seeds
// from InitialState.
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
