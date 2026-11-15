package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/v0lka/c0wrk/core/llmbudget"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
)

// This file owns the delegation launcher's single auto-retry for transient
// provider-class failures (specs/domains/orchestration/delegation.md): a
// blocking delegation whose execution error classifies transient is
// re-launched ONCE after a fixed backoff, BEFORE the outcome settles the unit
// failed — the durable ledger never observes failed→running (ADR-048's
// one-directional lifecycle). Non-transient errors settle failed immediately.
//
// The transient class is the ADR-065/ADR-126 taxonomy: rate_limit/overloaded
// (HTTP 429/529 or the transport-independent ErrKind) plus connection-level
// failures in sp4rk's dns/connect/reset/timeout set (StatusCode 0) — with the
// llmbudget transport's own-budget-expiry wrapper carved out (ADR-071 owns
// that recovery; the chained context.DeadlineExceeded would otherwise satisfy
// a naive timeout predicate). tls/eof/other and every budget death never
// qualify.

// delegationRetryBackoff is the fixed pause between a blocking delegation's
// transient failure and its single re-launch. Deliberately not configurable:
// one retry, one cadence, no knob to keep operational (the class taxonomy is
// already the price ADR-065/ADR-126 accepted). 30s matches the ADR-065
// rate-limit cadence — the fastest-healing class in the set — and sits far
// under any delegation's own runtime; the delegate tool is on the executor's
// per-tool-call exempt set, so the pause never competes with a watchdog
// ceiling. Package-level so tests override it (or let a synctest bubble
// consume it virtually).
var delegationRetryBackoff = 30 * time.Second

// IsTransientProviderError reports whether the error chain carries the
// transient provider class (ADR-065/ADR-126): a terminal *llm.Error with
// HTTP 429/529, the rate_limit/overloaded ErrKind, or — at StatusCode 0 — a
// wrapped chain classifying dns/connect/reset/timeout under llm.TransportKind.
// The llmbudget own-budget-expiry signature is excluded in BOTH directions
// (ADR-126): a budget death escalates in-flight (ADR-071) and never
// qualifies, while a provider-side timeout still does. tls/eof/other and
// every non-provider error are false. The class is defined ONCE, here: the
// task-level auto-resend classifier (backend/session/manager_auto_retry.go)
// consumes this predicate and the network-subclass split below, adding only
// the interval resolution.
func IsTransientProviderError(err error) bool {
	if err == nil {
		return false
	}
	if llmbudget.IsOwnBudgetExpiry(err) {
		return false
	}
	var llmErr *llm.Error
	if !errors.As(err, &llmErr) {
		return false
	}
	if llmErr.StatusCode == 429 || llmErr.StatusCode == 529 {
		return true
	}
	if llmErr.ErrKind == llm.ErrKindRateLimit || llmErr.ErrKind == llm.ErrKindOverloaded {
		return true
	}
	return IsTransientNetworkClassError(err)
}

// IsTransientNetworkClassError reports whether the error chain carries the
// connection-level subclass of the transient provider class (ADR-126): a
// terminal *llm.Error at StatusCode 0 — no HTTP response at all — whose
// wrapped chain classifies dns/connect/reset/timeout under llm.TransportKind.
// tls/eof/other stay out, and budget deaths are excluded like in the parent
// class (the same llmbudget.IsOwnBudgetExpiry carve-out). The task-level
// auto-resend seam (backend/session/manager_auto_retry.go) consumes this
// predicate to pick the GLOBAL network interval over the per-provider
// rate-limit knob.
func IsTransientNetworkClassError(err error) bool {
	if err == nil {
		return false
	}
	if llmbudget.IsOwnBudgetExpiry(err) {
		return false
	}
	var llmErr *llm.Error
	if !errors.As(err, &llmErr) {
		return false
	}
	if llmErr.StatusCode != 0 {
		return false
	}
	switch llm.TransportKind(llmErr.Err) {
	case "dns", "connect", "reset", "timeout":
		return true
	}
	return false
}

// retriedLeg couples a blocking leg's post-retry outcome with the first
// attempt's failure: retried is true only when a re-launch actually ran (the
// caller then annotates the result and must settle the RETRY's outcome — the
// first attempt never reached the ledger).
type retriedLeg struct {
	sr       agent.SubAgentResult
	firstErr error
	retried  bool
}

// retryTransientBlockingLeg re-launches ONE transiently-failed blocking leg
// after the fixed backoff. leg re-runs the leg FRESH (a rebuilt subagent task;
// for a redelegation also a fresh child registry + per-task context) so a
// retry never inherits the failed attempt's partial state. A cooperative
// pause and every non-transient error return the original result unretried;
// a cancellation landing during the backoff does the same (the wave is being
// torn down — no point re-launching into a dead context).
func (l *conductorLauncher) retryTransientBlockingLeg(ctx context.Context, sr agent.SubAgentResult, leg func() agent.SubAgentResult) retriedLeg {
	firstErr := sr.Error
	if firstErr == nil || isPaused(firstErr) || !IsTransientProviderError(firstErr) {
		return retriedLeg{sr: sr}
	}
	if !l.waitDelegationRetryBackoff(ctx) {
		// The wave is being torn down while the backoff ran out: never
		// re-launch into a dying context — and never let the first attempt's
		// transient error settle the unit failed. Wrapping the settled error
		// with the chain-visible context.Canceled routes the settlement
		// through unitStatusFor's abandonment branch: the durable ledger
		// records `interrupted` (delegation.md — a cancellation is never a
		// failure, so the resume funnel relaunches the abandoned unit fresh)
		// while the wave result stays failed (non-nil error) for the caller.
		return retriedLeg{sr: agent.SubAgentResult{
			StepID: sr.StepID,
			Output: sr.Output,
			Error:  fmt.Errorf("%w (wave canceled during auto-retry backoff: %w)", sr.Error, context.Canceled),
			Steps:  sr.Steps,
		}}
	}
	return retriedLeg{sr: leg(), firstErr: firstErr, retried: true}
}

// waitDelegationRetryBackoff sleeps the fixed delegation auto-retry backoff,
// returning false when the context was canceled first (no retry).
func (l *conductorLauncher) waitDelegationRetryBackoff(ctx context.Context) bool {
	timer := time.NewTimer(delegationRetryBackoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// abandonAttemptChildren tears down the child delegations an ABANDONED
// blocking attempt (re-delegating subagent) left behind, before the attempt's
// successor may register a fresh generation: pending/running entries are
// cancelled and — for started async children, the only kind with a settling
// goroutine — the caller waits (bounded by ctx) for their settlement, so the
// durable ledger folds the abandoned generation terminal BEFORE the retry
// registers its own children (delegation.md). A child registered but never
// started (the RegisterTask→Start window) has no goroutine to settle it —
// the launcher settles its ledger record here, as interrupted. Only
// re-delegating legs can have children at all: regular legs cannot delegate.
func (l *conductorLauncher) abandonAttemptChildren(ctx context.Context, reg *tools.DelegationRegistry) {
	if reg == nil {
		return
	}
	ids := reg.ListPending()
	for _, id := range ids {
		reg.Cancel(id)
	}
	for _, id := range ids {
		done := reg.Done(id)
		if done == nil {
			// Done() is nil for a never-started entry AND for an
			// already-settled one (the channel is reaped when the settling
			// goroutine closes it) — discriminated by StartedAt, which Start
			// stamps exactly once: a nil-Done entry with a zero StartedAt is
			// the Register→Start window (no goroutine will ever settle its
			// Begin'd record — fold it here, as interrupted); a nil-Done
			// entry with a stamped StartedAt already settled through its own
			// exit path (completed/paused/failed) and its durable record must
			// stay untouched.
			if d := reg.Get(id); d != nil && d.StartedAt.IsZero() {
				l.settleUnit(id, context.Canceled)
			}
			continue
		}
		select {
		case <-done:
		case <-ctx.Done():
			// Teardown while waiting: the cancellations are already issued
			// and the goroutines settle on their own exit paths.
			return
		}
	}
}

// delegationRetriedSuffix annotates a FINAL failure that followed a retried
// leg — the delegate tool's failed branch renders only the error, so the
// narrative must ride the error text for the result markdown to note the
// auto-retried leg there too. The %w wrap preserves every errors.Is/As chain
// (pause sentinels never reach this path — a retried leg that paused settles
// as a pause, unwrapped).
const delegationRetriedSuffix = "auto-retry: this delegation was re-launched once after a transient first-attempt failure; the re-launched leg failed again"

// noteAutoRetriedLeg prepends the auto-retry notice to a delegation output
// that completed after a re-launched leg. The notice rides sr.Output, so it
// reaches every consumer at once: the blackboard step result, the registry,
// and the delegate tool's result markdown.
func noteAutoRetriedLeg(output string, firstErr error) string {
	note := fmt.Sprintf("> [auto-retry] the first attempt failed with a transient provider error (%s); this delegation was automatically re-launched once after a fixed backoff and completed.\n\n", firstErr.Error())
	return note + output
}
