// Package session provides typed event payloads for session lifecycle events.
package session

import (
	"time"

	"github.com/v0lka/c0wrk/core"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/orchestration"
)

// Event type constants for backend-to-frontend communication.
const (
	EventStepLimit         = "step_limit"
	EventStepLimitResponse = "step_limit_response"
	// EventTaskStarted fires once per task ACTIVATION — fresh, goal, E2S and
	// continue-interrupted dispatches all funnel through SendMessage — and the
	// desktop layer mirrors it into the global sessions:changed push. The
	// resume path uses task_resumed + session_resumed instead, so one resume
	// never pushes the list change twice. No payload; transient. See
	// specs/contracts/event-catalog.md.
	EventTaskStarted = "task_started"
	// EventAutonomyDecision carries an automatic (no-human) security decision —
	// the non-blocking, persisted audit record of a gate a human would
	// otherwise have answered (OWASP ASI10). Payload contract (sub-policy
	// naming, the shell-exec effect Signature doubling as the judge's
	// memoization key): specs/contracts/event-catalog.md.
	EventAutonomyDecision = "autonomy_decision"
)

// AutonomyDecisionData is an alias of coretools.AutonomyDecision — the value
// the registry hands the host observer — so the registry and the wire contract
// cannot drift.
type AutonomyDecisionData = coretools.AutonomyDecision

// --- Session lifecycle event data ---

// SessionCreatedData is the payload for "session_created" events.
type SessionCreatedData struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// SessionDeletedData is the payload for "session_deleted" events.
type SessionDeletedData struct {
	ID string `json:"id"`
}

// SessionRenamedData is the payload for "session_renamed" events.
type SessionRenamedData struct {
	ID      string `json:"id"`
	OldName string `json:"old_name"`
	NewName string `json:"new_name"`
}

// SessionArchivedData is the payload for "session_archived" / "session_unarchived" events.
type SessionArchivedData struct {
	ID       string `json:"id"`
	Archived bool   `json:"archived"`
}

// SessionPinnedData is the payload for "session_pinned" / "session_unpinned" events.
type SessionPinnedData struct {
	ID     string `json:"id"`
	Pinned bool   `json:"pinned"`
}

// MessageReceivedData is the payload for "message_received" events.
type MessageReceivedData struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
	// ClientSendID echoes the sending surface's optimistic-card id
	// (WithClientMessageID) so the sender dedups by id, not
	// content+time-window. Absent without an optimistic card.
	ClientSendID string `json:"client_send_id,omitempty"`
	// IsNudge mirrors the persisted row's is_nudge stamp so surfaces without
	// an optimistic copy show the Nudge badge immediately; the flag and the
	// DB stamp come from the same session-locked send classification and must
	// agree.
	IsNudge bool `json:"is_nudge,omitempty"`
}

// TaskCompleteData is the payload for "task_complete" events.
type TaskCompleteData struct {
	SessionID       string                     `json:"session_id"`
	Output          string                     `json:"output"`
	RoutingDecision *router.RoutingDecision    `json:"routing_decision"`
	Plan            *orchestration.Plan        `json:"plan,omitempty"`
	Reflections     []orchestration.Reflection `json:"reflections,omitempty"`
	// Success is false for partial/failed/aborted executions still delivered
	// as task_complete so the best-effort output reaches the user.
	Success    bool   `json:"success"`
	Completion string `json:"completion,omitempty"` // "full" | "partial" | "failed" | "aborted"
}

// TaskCancelledData is the payload for "task_cancelled" events.
type TaskCancelledData struct {
	SessionID string `json:"session_id"`
}

// PromptSuggestionsData carries the session's FULL active suggestion set
// (both kinds, ADR-088); the frontend replaces its set outright.
type PromptSuggestionsData struct {
	SessionID   string                  `json:"session_id"`
	Suggestions []core.PromptSuggestion `json:"suggestions"`
}

// TaskFailedResumableData is the payload for "task_failed_resumable" events;
// TaskID lets the persisted banner message be resolved on resume/cancel.
type TaskFailedResumableData struct {
	Message string `json:"message"`
	TaskID  string `json:"task_id,omitempty"`
	// Reason explains WHY the task is resumable rather than a generic message.
	Reason string `json:"reason,omitempty"`
	// AutoRetryAt, when non-zero, schedules the UI's auto-resend countdown —
	// stamped only for a classified rate-limit/overload llm.Error from a
	// provider with auto_retry_seconds > 0. There is no backend timer
	// (ADR-065, see specs/decisions/065-auto-resend-retryable-errors.md): the
	// deadline is advisory; 0 means the manual decision is final.
	AutoRetryAt int64 `json:"auto_retry_at,omitempty"`
}

// ErrorData is the payload for "error" events.
type ErrorData struct {
	SessionID string `json:"session_id"`
	Error     string `json:"error"`
}

// WorkUnitSettledData is the payload for "work_unit_settled" events — a unit
// settled because the resume funnel will not relaunch it, so a live view can
// align the chat block instead of leaving it "running". Transient: the durable
// source is the unit ledger (GetSessionRuntimeStatus.work_units).
type WorkUnitSettledData struct {
	StepID string `json:"step_id"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// --- Tool confirmation payloads ---

// ToolConfirmPayload is sent when a tool needs user confirmation; ToolCallID
// anchors the card to the triggering tool_call, DisableJudge hides the
// advisory "Ask Agent" button when the strict judge already evaluated the
// call. See specs/contracts/event-catalog.md.
type ToolConfirmPayload struct {
	ConfirmID    string `json:"confirm_id"`
	Tool         string `json:"tool"`
	Args         string `json:"args"`
	Reasoning    string `json:"reasoning"`
	ToolCallID   string `json:"tool_call_id,omitempty"`
	DisableJudge bool   `json:"disable_judge,omitempty"`
}

// JudgeRequestPayload is received from the frontend when the user requests an on-demand judge verdict.
type JudgeRequestPayload struct {
	ConfirmID string `json:"confirm_id"`
}

// JudgeResponsePayload is sent to the frontend with the judge's verdict.
type JudgeResponsePayload struct {
	ConfirmID string `json:"confirm_id"`
	Reasoning string `json:"reasoning"`
	Error     string `json:"error,omitempty"`
}

// --- Ask-user payloads ---

// AskUserPayload is sent to the frontend when the agent asks the user questions.
type AskUserPayload struct {
	RequestID string                      `json:"request_id"`
	Questions []coretools.AskUserQuestion `json:"questions"`
}

// --- Step limit payloads ---

// StepLimitPayload is emitted when an agent reaches its tool call step limit
// or a circuit breaker abort threshold, prompting the user for a decision on whether to continue.
type StepLimitPayload struct {
	RequestID   string `json:"request_id"`
	CurrentStep int    `json:"current_step"`
	MaxSteps    int    `json:"max_steps"`
	Reason      string `json:"reason,omitempty"` // empty for normal step limit; describes circuit breaker trigger
}

// StepLimitResponsePayload carries the user's decision about continuing
// past the step limit.
type StepLimitResponsePayload struct {
	RequestID string `json:"request_id"`
	Response  string `json:"response"` // "allow_once", "allow_more", "allow_always", or "deny"
}

// --- Plan approval payloads ---

// PlanApprovalPayload is sent to the frontend when the Conductor calls
// declare_plan with mode=await_approval and the plan is ready for review.
type PlanApprovalPayload struct {
	RequestID   string `json:"request_id"`
	PlanPath    string `json:"plan_path"`
	PlanContent string `json:"plan_content"`
}

// PlanApprovalResponsePayload is received from the frontend when the user
// decides on a plan awaiting approval.
type PlanApprovalResponsePayload struct {
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"` // "approve", "request_changes", or "abandon"
	Feedback  string `json:"feedback"` // non-empty when decision="request_changes"
}

// --- Goal proposal payloads ---

// GoalProposalPayload is sent when the derivation agent calls propose_goal —
// a pending action that blocks the agent until the user responds. See
// specs/contracts/event-catalog.md.
type GoalProposalPayload struct {
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	Condition string `json:"condition"`
	Verify    string `json:"verify"`
	// VerificationMode echoes the derivation agent's per-goal verification mode
	// (see goal.VerificationMode* constants) so the frontend can round-trip a
	// user edit; empty means the default (executable).
	VerificationMode string `json:"verification_mode,omitempty"`
}

// GoalSuggestionPayload is sent when the post-routing suggestion gate judged a
// fresh task's message goal-suited — a pending action blocking the task until
// the user answers (accept enters the goal loop; decline continues the plain
// Conductor path and suppresses further suggestions for the session).
type GoalSuggestionPayload struct {
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	// Reason is the classifier's short user-facing explanation (in the
	// user's language) of WHY the request fits goal mode.
	Reason string `json:"reason"`
}

// --- HITL resolution payloads ---

// HitlResolvedKind identifies which kind of HITL prompt a HitlResolvedPayload
// settles; values mirror the frontend message types so the UI can retire the
// exact card.
type HitlResolvedKind string

const (
	HitlResolvedToolConfirm    HitlResolvedKind = "tool_confirm"
	HitlResolvedAskUser        HitlResolvedKind = "ask_user"
	HitlResolvedStepLimit      HitlResolvedKind = "step_limit"
	HitlResolvedPlanReview     HitlResolvedKind = "plan_review"
	HitlResolvedGoalProposal   HitlResolvedKind = "goal_proposal"
	HitlResolvedGoalSuggestion HitlResolvedKind = "goal_suggestion"
)

// HitlResolvedData is the typed Data payload for "hitl_resolved" events —
// broadcast to EVERY surface when a pending HITL prompt is consumed by the
// first responder, so the other surfaces retire their cards immediately;
// Decision carries the EFFECTIVE answer a racer converges on. Transient: the
// durable resolution lives in the resolved pending message row. See
// specs/contracts/event-catalog.md.
type HitlResolvedData struct {
	Kind     HitlResolvedKind `json:"kind"`
	ID       string           `json:"id"`
	Decision string           `json:"decision,omitempty"`
}

// --- Emitter event data types (typed Data field payloads) ---
// These mirror the event data produced by the EventEmitter methods,
// enabling type-safe assertions in the emitFunc / persistence layer.

// ThoughtEventData is the typed Data payload for "thought" events.
type ThoughtEventData struct {
	StepNum    int    `json:"step_num"`
	Content    string `json:"content"`
	Reasoning  string `json:"reasoning"`
	PlanStepID string `json:"plan_step_id,omitempty"`
}

// AssistantDoneEventData is the typed Data payload for "assistant_done" events.
type AssistantDoneEventData struct {
	Content      string `json:"content"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	PlanStepID   string `json:"plan_step_id,omitempty"`
}

// ContextFillEventData is the typed Data payload for "context_fill" events.
type ContextFillEventData struct {
	FillPercent float64 `json:"fill_percent"`
	UsedTokens  int     `json:"used_tokens"`
	MaxTokens   int     `json:"max_tokens"`
	Status      string  `json:"status"`
	PlanStepID  string  `json:"plan_step_id,omitempty"`
	// SessionRootMirror marks a step-scoped event from the ROOT conductor
	// emitter: the frontend may mirror the fill to the session-level status
	// bar immediately (the session_tokens re-broadcast lags one iteration).
	// Never set on subagent copies — their fill must not clobber the
	// conductor's session-level fill.
	// See specs/contracts/event-catalog.md (context_fill).
	SessionRootMirror   bool   `json:"session_root_mirror,omitempty"`
	SessionInputTokens  int    `json:"session_input_tokens"`
	SessionOutputTokens int    `json:"session_output_tokens"`
	Model               string `json:"model"`
	Family              string `json:"family"`
}

// SessionTokensEventData is the typed Data payload for "session_tokens".
// UsedTokens/MaxTokens mirror the session-root context-window fill cached by
// ContextFill (drives the status bar's "N of M" tooltip); the throughput
// fields stay omitted until the sliding window reaches its minimum samples.
type SessionTokensEventData struct {
	SessionInputTokens  int     `json:"session_input_tokens"`
	SessionOutputTokens int     `json:"session_output_tokens"`
	Model               string  `json:"model"`
	Family              string  `json:"family"`
	FillPercent         float64 `json:"fill_percent"`
	UsedTokens          int     `json:"used_tokens"`
	MaxTokens           int     `json:"max_tokens"`
	MedianOutputTokS    float64 `json:"median_output_tok_s,omitempty"`
	TokSSamples         int     `json:"tok_s_samples,omitempty"`
}

// ContextCompactionEventData is the typed Data payload for "context_compaction" events.
type ContextCompactionEventData struct {
	BeforePercent float64 `json:"before_percent"`
	AfterPercent  float64 `json:"after_percent"`
	PlanStepID    string  `json:"plan_step_id,omitempty"`
}

// CompactionStartedEventData is the typed Data payload for "compaction_started"
// — the manual compaction flow is running (UI locks the input). See
// specs/contracts/event-catalog.md.
type CompactionStartedEventData struct {
	Strategy string `json:"strategy"`
}

// CompactionFinishedEventData is the typed Data payload for
// "compaction_finished" — the manual compaction flow's terminal state.
// Field semantics (exactly-one-of Success/Cancelled/Error, NothingCompacted,
// DeferredToResume, PausedWithoutResume, CompactionAvailability) are specified
// in specs/contracts/event-catalog.md and specs/domains/memory/compaction.md.
type CompactionFinishedEventData struct {
	Strategy               string                        `json:"strategy"`
	Success                bool                          `json:"success"`
	Cancelled              bool                          `json:"cancelled,omitempty"`
	Error                  string                        `json:"error,omitempty"`
	BeforePercent          float64                       `json:"before_percent"`
	AfterPercent           float64                       `json:"after_percent"`
	Resumed                bool                          `json:"resumed,omitempty"`
	PausedWithoutResume    bool                          `json:"paused_without_resume,omitempty"`
	NothingCompacted       bool                          `json:"nothing_compacted,omitempty"`
	DeferredToResume       bool                          `json:"deferred_to_resume,omitempty"`
	CompactionAvailability []core.CompactionAvailability `json:"compaction_availability"`
}

// SkillsActivatedData is the typed Data payload for "skills_activated" events.
type SkillsActivatedData struct {
	Skills []string `json:"skills"`
}

// AgentMetricsData is the typed Data payload for "agent_metrics", emitted once
// per task finish/abort — executor quality counters so profile effects can be
// measured against data. See specs/domains/model-profiles.md (and the
// specs/contracts/event-catalog.md row).
type AgentMetricsData struct {
	// Finish: "full" | "partial" | "failed" | "aborted" | "cancelled".
	Finish string `json:"finish"`
	// ParseErrors counts malformed model outputs that triggered a corrective
	// nudge or an abort.
	ParseErrors int `json:"parse_errors"`
	// InvalidToolCalls counts results the executor classified as an invalid
	// tool call — distinct from runtime errors and policy refusals.
	InvalidToolCalls int `json:"invalid_tool_calls"`
	// Nudges counts corrective nudges from the executor loop detectors,
	// broken down by detector kind.
	Nudges AgentMetricsCounters `json:"nudges"`
	// Aborts counts hard loop-breaker aborts, broken down by detector kind.
	Aborts AgentMetricsCounters `json:"aborts"`
	// Steps counts executor steps (conductor plus sharing subagents).
	Steps int `json:"steps"`
	// OutputTokens is the session-wide accumulated output usage.
	OutputTokens  int                   `json:"output_tokens"`
	ModelProfiles ModelProfilesMetaInfo `json:"model_profiles"`
}

// AgentMetricsCounters breaks nudges/aborts down by executor loop detector:
// repeat — identical tool calls, same_tool — same tool with similar results,
// fruitless — no-progress detector, parse — malformed output detectors,
// truncation — output-truncation circuit-breaker abort.
type AgentMetricsCounters struct {
	Repeat     int `json:"repeat"`
	SameTool   int `json:"same_tool"`
	Fruitless  int `json:"fruitless"`
	Parse      int `json:"parse"`
	Truncation int `json:"truncation"`
}

// ModelProfilesMetaInfo snapshots the Model Profiles profile state the session ran
// under, so metrics can be grouped by active optimization variants.
type ModelProfilesMetaInfo struct {
	Enabled bool `json:"enabled"`
	// Profile is the active profile's id slug (same id
	// `model_profiles.active_profile` persists), reported even when the master
	// toggle is off; empty when never recorded.
	Profile string `json:"profile,omitempty"`
	// ProfileKind is "predefined" or "custom" (config.ModelProfileKind);
	// empty exactly when Profile is empty.
	ProfileKind string   `json:"profile_kind,omitempty"`
	Variants    []string `json:"variants"`
}
