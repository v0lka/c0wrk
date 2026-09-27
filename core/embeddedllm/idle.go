package embeddedllm

import (
	"context"
	"sync"
	"time"
)

// This file owns the auto-unload budget: an idle model is a multi-gigabyte
// resident process that the user is not actively using, so it is stopped after
// a configurable period of inactivity (ADR-066 D5 — unload means terminating
// the process, which is what actually returns the RAM/VRAM).
//
// The one rule that is easy to get wrong, and the reason this bookkeeping lives
// in its own file:
//
//	Weight-load time is NOT idle time.
//
// Loading 6.7 GiB of weights can take minutes. If lastActivity were stamped when
// a Load STARTS, that load would arrive with most of the operator's budget
// already spent, and a model the user has not yet sent a single request to would
// be unloaded almost immediately. So the budget is stamped when a load COMPLETES
// (server.go, Load step 7) and again when a request finishes (MarkActivity,
// called by the ensure-loaded transport). The timer never runs against a model
// that is not loaded.
//
// The sibling rule, and the reason the in-flight count lives beside the stamp:
//
//	A request in flight is not idle either.
//
// Stamping on completion alone cannot express it. The budget is an operator knob
// validated down to one minute, and a single streamed generation can plausibly
// outlive an aggressive one — so the transport also counts the requests it has
// open (BeginRequest/EndRequest) and an expiry that lands mid-request defers
// itself by a bounded grace instead of stopping the server out from under its own
// answer. The stamp on completion is what restarts the FULL budget afterwards.
//
// The deferral is bounded too, and the bound is WALL TIME rather than a count of
// deferrals: one deferral episode may not outlast a full idle budget, after which
// the unload proceeds anyway and says so at Warn. A count that is never released
// — a response body nobody closed — therefore costs at most one extra full idle
// budget instead of pinning gigabytes resident for the lifetime of the process.

// AutoUnload is the operator's idle policy — the in-memory shape of
// embedded_llm.auto_unload.
type AutoUnload struct {
	// Enabled false disables the timer entirely: the model stays resident until
	// an explicit Unload, a Remove, or app shutdown.
	Enabled bool
	// Idle is the inactivity budget after which the process is stopped. It is
	// clamped into [a minute-scale default, MaxAutoUnloadMinutes] by normalized:
	// a value below one minute becomes the default, which mirrors the config rule
	// that rejects a non-positive minutes value even while the timer is disabled
	// — re-enabling it must never activate a dead budget — and the ceiling keeps
	// an "effectively never" figure from wrapping (see autoUnloadBudget).
	Idle time.Duration
}

// DefaultAutoUnload is the policy an install establishes when the operator has
// not chosen one: enabled, with the documented 60-minute budget.
func DefaultAutoUnload() AutoUnload {
	return AutoUnload{
		Enabled: DefaultAutoUnloadEnabled,
		Idle:    DefaultAutoUnloadMinutes * time.Minute,
	}
}

// maxAutoUnloadBudget is the ceiling normalized() clamps to: MaxAutoUnloadMinutes
// expressed as a duration. It is a constant expression, so the minutes→
// nanoseconds multiply that overflows an int64 above that bound is performed at
// compile time here rather than at runtime on an operator's value.
const maxAutoUnloadBudget = time.Duration(MaxAutoUnloadMinutes) * time.Minute

// NewAutoUnload builds a policy from the persisted config shape
// (auto_unload.enabled + auto_unload.minutes). The minutes are converted
// without ever wrapping (autoUnloadBudget) and normalized() then owns the clamp.
func NewAutoUnload(enabled bool, minutes int) AutoUnload {
	return AutoUnload{Enabled: enabled, Idle: autoUnloadBudget(minutes)}.normalized()
}

// autoUnloadBudget converts persisted minutes into a budget. It is TOTAL: the
// `time.Duration(minutes) * time.Minute` multiply wraps above
// MaxAutoUnloadMinutes, and a wrapped result can be a positive budget of tens of
// seconds — or, at the residues of the 2^11 divisor of the minutes→nanoseconds
// factor, single-digit microseconds — so an operator's "effectively never"
// would silently invert into "unload immediately", every message paying a
// multi-minute cold load. Values above the ceiling are therefore converted as
// the ceiling, and a non-positive count is left at zero for normalized() to
// replace with the default.
func autoUnloadBudget(minutes int) time.Duration {
	switch {
	case minutes <= 0:
		return 0
	case minutes > MaxAutoUnloadMinutes:
		return maxAutoUnloadBudget
	default:
		return time.Duration(minutes) * time.Minute
	}
}

// normalized clamps an unusable budget into the modelled range instead of
// leaving it at zero. Zero would mean "unload immediately" once the timer is
// armed, which is the opposite of what an operator who wrote nothing expects;
// the ceiling exists for the same reason at the other end — see
// autoUnloadBudget. It is the single place the clamp is applied, so a policy
// assembled by hand (a struct literal, a runtime SetAutoUnload) is bounded the
// same way as one built from config.
//
// A budget inside the range is left alone: the config layer validates the
// persisted minutes, and a caller that wants a sub-minute budget (a test) means
// it.
func (p AutoUnload) normalized() AutoUnload {
	switch {
	case p.Idle <= 0:
		p.Idle = DefaultAutoUnloadMinutes * time.Minute
	case p.Idle > maxAutoUnloadBudget:
		p.Idle = maxAutoUnloadBudget
	}
	return p
}

// idleTimer is the auto-unload bookkeeping: the last activity stamp, the live
// policy and the single timer that acts on them.
//
// It has its own mutex and never reaches back into the Server, so the lock order
// is always Server.mu → idleTimer.mu. The expiry callback is supplied by the
// caller on every arm and runs on the timer goroutine.
type idleTimer struct {
	mu     sync.Mutex
	seeded bool
	pol    AutoUnload
	last   time.Time
	timer  *time.Timer
	// gen counts activity stamps. The idle path decides to unload outside the
	// lock (Unload re-acquires both the gate and Server.mu), so it carries the
	// generation it decided on and the unload is abandoned if activity moved in
	// between — a request that finished exactly at the deadline must not have
	// the model pulled out from under the next one.
	gen uint64
	// graceUntil is non-zero while an expiry has been DEFERRED because a request
	// was in flight: the budget ran out, but stopping the server would have
	// killed a generation mid-answer, so the timer was re-armed for this long
	// instead. remaining reports it, so a status surface shows the deferral
	// rather than a stale zero.
	graceUntil time.Time
	// deferSince stamps the START of the current deferral episode — the first
	// expiry that was deferred while a request was in flight. It is the
	// wall-time bound on deferring: an episode may not outlast one full idle
	// budget, because the thing it is waiting for may be an in-flight count that
	// is never released (a response body nobody closed), and an unbounded
	// deferral would pin the model resident for the lifetime of the process.
	// Zero means no episode is running; stopLocked clears it, so real activity
	// (mark), a disarm and a policy change all end the episode, while armGrace
	// preserves it across the re-arms that continue one.
	deferSince time.Time
}

// seed returns the live policy, adopting the Server's construction-time field
// the first time it is asked. All tracker state stays behind its own mutex.
func (t *idleTimer) seed(policy AutoUnload) AutoUnload {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.seeded {
		t.pol = policy.normalized()
		t.seeded = true
	}
	return t.pol
}

// setPolicy replaces the live policy and re-arms from the EXISTING activity
// stamp, so changing the budget does not grant a fresh one: shortening it while
// a model has been idle for longer than the new value unloads that model.
func (t *idleTimer) setPolicy(policy AutoUnload, now time.Time, armed bool, onExpire func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pol = policy.normalized()
	t.seeded = true
	t.stopLocked()
	// A policy change supersedes any deferral: the timer is re-armed from the
	// existing activity stamp below, on the operator's new budget.
	t.graceUntil = time.Time{}
	if !armed {
		return
	}
	if t.last.IsZero() {
		t.last = now
	}
	t.armLocked(now, onExpire)
}

// mark stamps activity and, when armed, (re)starts the budget from that stamp.
// It also ends any deferred grace — and with it the deferral episode's
// wall-time bound, which stopLocked clears: a completed request is exactly the
// event the grace was waiting for, and the full budget restarts from this stamp.
func (t *idleTimer) mark(now time.Time, armed bool, onExpire func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.last = now
	t.graceUntil = time.Time{}
	t.gen++
	t.stopLocked()
	if armed {
		t.armLocked(now, onExpire)
	}
}

// armGrace re-arms the timer for a bounded grace period WITHOUT touching the
// activity stamp: the budget has run out, but a request is in flight and the
// unload is deferred rather than cancelled. The deferral is re-checked when the
// grace expires, so a long generation keeps deferring it while the model is
// genuinely being used.
//
// The re-checks are bounded by wall time, not by their number: the FIRST
// deferral of an episode stamps deferSince and the re-arms that continue it keep
// that stamp, so a leaked in-flight count — one that is never released — stops
// being deferred once the episode has lasted a full idle budget (see
// deferElapsed and onIdleExpired) and ends in an unload then, rather than one
// grace later, and rather than never.
func (t *idleTimer) armGrace(now time.Time, grace time.Duration, armed bool, onExpire func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// stopTimerLocked, NOT stopLocked: the episode this call continues must keep
	// its start stamp, or every re-arm would grant itself a fresh wall-time
	// budget and the bound would never be reached.
	t.stopTimerLocked()
	if !armed || !t.pol.Enabled || grace <= 0 {
		t.graceUntil = time.Time{}
		t.deferSince = time.Time{}
		return
	}
	if t.deferSince.IsZero() {
		t.deferSince = now
	}
	t.graceUntil = now.Add(grace)
	t.timer = time.AfterFunc(grace, onExpire)
}

// deferElapsed reports how long the CURRENT deferral episode has been running:
// zero when no expiry has been deferred since the last real activity, a disarm
// or a policy change. onIdleExpired compares it against the idle budget — an
// episode that has already lasted that long is not waiting for a generation any
// more, it is waiting for an in-flight count that will never be released.
func (t *idleTimer) deferElapsed(now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.deferSince.IsZero() {
		return 0
	}
	if elapsed := now.Sub(t.deferSince); elapsed > 0 {
		return elapsed
	}
	return 0
}

// armLocked creates the timer for whatever budget is left. t.mu must be held.
//
// It ALWAYS leaves a timer behind when the policy can fire at all, including the
// spent-budget case (armed at zero, so the callback runs at once): `armed` is
// reported from t.timer, and the callback trusts that report.
func (t *idleTimer) armLocked(now time.Time, onExpire func()) {
	if !t.pol.Enabled || t.pol.Idle <= 0 {
		return
	}
	wait := t.pol.Idle
	if !t.last.IsZero() {
		wait = t.pol.Idle - now.Sub(t.last)
	}
	if wait < 0 {
		wait = 0
	}
	// A spent budget is ARMED AT ZERO rather than fired inline. The callback
	// re-validates under this same mutex and only acts on a timer it finds armed
	// — remaining reports `armed` from t.timer, and onIdleExpired bails on !armed
	// — so a bare `go onExpire()` here is rejected by its own guard: enabling the
	// policy (or shortening it below the time already spent) on a long-idle model
	// would neither unload it nor leave a timer armed behind, and nothing re-arms
	// afterwards. AfterFunc always runs the callback on its own goroutine, which
	// is what the zero-delay arm needs as well: the caller holds Server.mu and the
	// unload has to acquire it.
	t.timer = time.AfterFunc(wait, onExpire)
}

// disarm stops the timer without touching the activity stamp or the policy.
func (t *idleTimer) disarm() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopLocked()
}

// stopLocked stops the timer AND ends the deferral episode. Every caller — mark,
// disarm and setPolicy — is a path that supersedes a deferral rather than
// continuing one, so the wall-time bound must not survive them: real activity
// restarts the full budget, a disarm means nothing will expire, and a policy
// change re-arms on the operator's new budget. armGrace is the one path that
// continues an episode, and it calls stopTimerLocked instead.
func (t *idleTimer) stopLocked() {
	t.stopTimerLocked()
	t.deferSince = time.Time{}
}

// stopTimerLocked stops the timer and leaves the deferral episode alone.
func (t *idleTimer) stopTimerLocked() {
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
}

// remaining reports the budget left before an auto-unload. ok is false when no
// timer is armed — the policy is disabled, or the model is not loaded — which is
// what lets a caller distinguish "60 minutes left" from "this never unloads".
//
// While an expiry is deferred for an in-flight request (armGrace), the answer is
// the grace left rather than the budget left: the budget is already spent, and
// reporting 0 there would hide the deferral from every reader.
func (t *idleTimer) remaining(now time.Time) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.timer == nil || !t.pol.Enabled || t.pol.Idle <= 0 || t.last.IsZero() {
		return 0, false
	}
	left := t.pol.Idle - now.Sub(t.last)
	if !t.graceUntil.IsZero() {
		left = t.graceUntil.Sub(now)
	}
	if left < 0 {
		left = 0
	}
	return left, true
}

// generation is the activity counter the idle path carries into its unload, so
// the unload can be abandoned when activity landed after the decision.
func (t *idleTimer) generation() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.gen
}

func (t *idleTimer) lastActivity() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last
}

func (t *idleTimer) policy() AutoUnload {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pol
}

// ── Server surface ──

// SetAutoUnload replaces the idle policy at runtime — the Settings toggle and
// the minutes field both land here. The change takes effect immediately: the
// timer is re-armed against the existing activity stamp while the model is
// loaded, and disarmed when the policy is off.
func (s *Server) SetAutoUnload(policy AutoUnload) {
	if s == nil {
		return
	}
	policy = policy.normalized()
	s.mu.Lock()
	armed := s.stateLocked() == StateLoaded
	s.idle.setPolicy(policy, s.now(), armed, s.onIdleExpired)
	s.mu.Unlock()
	s.logger().Debug("embedded LLM auto-unload policy updated",
		"enabled", policy.Enabled, "idle", policy.Idle)
}

// AutoUnloadPolicy is the live policy. It reflects SetAutoUnload, and the
// Server.AutoUnload field until the first change.
func (s *Server) AutoUnloadPolicy() AutoUnload {
	if s == nil {
		return AutoUnload{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idlePolicyLocked()
}

// MarkActivity records that the model was just used, restarting the idle budget.
//
// The ensure-loaded transport calls it when a response COMPLETES, not when a
// request is sent: a long generation is activity, and charging it to the budget
// would unload a model mid-conversation. Marking while the model is not loaded
// only updates the stamp — the timer runs against a loaded model, so a stamp
// made before a load cannot arm anything.
//
// Completion is only half of that guarantee: a generation LONGER than the whole
// budget would still expire before it completes, so the in-flight count below is
// what the expiry checks first. The stamp is what restarts the full budget once
// the request is over.
func (s *Server) MarkActivity() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.idlePolicyLocked()
	s.recordActivityLocked()
	s.mu.Unlock()
}

// BeginRequest records that a request against the embedded model is now in
// flight. The ensure-loaded transport calls it immediately before it sends one
// and EndRequest when that request's response is finished, so the pair brackets
// the whole exchange — headers, streamed body and all.
//
// It is the second half of "a long generation is activity": MarkActivity stamps
// on completion, which cannot help a request that outlives the entire budget, so
// the idle expiry defers itself while this count is positive (onIdleExpired). The
// deferral is capped at ONE EXTRA FULL IDLE BUDGET of wall time, so a count this
// pair never releases — a response body nobody closed — delays the unload by at
// most that much instead of pinning the model resident for the process lifetime.
func (s *Server) BeginRequest() {
	if s == nil {
		return
	}
	s.inFlight.Add(1)
}

// EndRequest releases one in-flight request. It is idempotent at zero: a count
// that has already been released cannot go negative, which would under-count the
// requests that follow it and leave a genuine long generation with no deferral
// protecting it.
func (s *Server) EndRequest() {
	if s == nil {
		return
	}
	for {
		current := s.inFlight.Load()
		if current <= 0 {
			return
		}
		if s.inFlight.CompareAndSwap(current, current-1) {
			return
		}
	}
}

// InFlightRequests is how many requests the transport currently has open against
// this server. It is what the idle path checks before it unloads.
func (s *Server) InFlightRequests() int64 {
	if s == nil {
		return 0
	}
	return s.inFlight.Load()
}

// IdleRemaining reports the auto-unload budget left, and whether the timer is
// armed at all. It is what a status surface should show: the full budget
// immediately after a load, counting down from the last completed request.
func (s *Server) IdleRemaining() (time.Duration, bool) {
	if s == nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idlePolicyLocked()
	return s.idle.remaining(s.now())
}

// LastActivity is the stamp the idle budget counts from: the completion of a
// load, or of the most recent request, whichever is later. It is the zero time
// until one of them happens.
func (s *Server) LastActivity() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idlePolicyLocked()
	return s.idle.lastActivity()
}

// idlePolicyLocked returns the live idle policy, seeding it from the
// Server.AutoUnload field on first use. The field is a construction-time input;
// the tracker is the single source of truth afterwards, so a runtime
// SetAutoUnload and a concurrent read can never race on it. s.mu must be held.
func (s *Server) idlePolicyLocked() AutoUnload {
	return s.idle.seed(s.AutoUnload)
}

// recordActivityLocked stamps activity and arms the timer only while the model
// is loaded. It seeds the live policy first: a load is the FIRST thing that ever
// arms the timer, so without this the construction-time AutoUnload field would
// never reach the tracker. s.mu must be held.
func (s *Server) recordActivityLocked() {
	s.idlePolicyLocked()
	s.idle.mark(s.now(), s.stateLocked() == StateLoaded, s.onIdleExpired)
}

// idleGracePeriod is how far an idle expiry defers itself while a request is in
// flight. It is a re-check interval rather than a second budget: the deferral is
// re-armed on every expiry for as long as requests are open, so a long generation
// is never cut short. The re-checks are bounded by wall time (deferSince), so a
// leaked in-flight count — a response body nobody ever closed — ends in an unload
// after AT MOST ONE EXTRA FULL IDLE BUDGET instead of pinning gigabytes resident
// for the lifetime of the process.
const idleGracePeriod = 30 * time.Second

// onIdleExpired is the timer callback: the budget ran out, so the process is
// stopped.
//
// It re-checks everything under the lock first, because the callback acts on a
// decision the timer made earlier and three things can have changed since:
//
//   - the model is no longer loaded (an Unload or a crash got there first);
//   - the timer is no longer ARMED: the operator disabled the policy — or
//     disarmed it another way — in the window between the tick and this
//     callback, and a model just exempted must not be unloaded by the stale tick;
//   - activity landed and already re-armed a fresh budget (left > 0), which
//     makes this callback the stale one.
//
// A request in flight defers the unload by a bounded grace instead: stopping the
// server now would kill a generation mid-answer, which is the exact outcome
// MarkActivity's completion stamp exists to prevent. The deferral is bounded by
// WALL TIME — one episode may not outlast a full idle budget — so an in-flight
// count that is never released delays the unload by at most that much and then
// loses it, with the leaked count named at Warn.
//
// The unload itself carries the activity generation this callback decided on and
// is abandoned if activity moved in the meantime, because Unload has to
// re-acquire both the gate and Server.mu — a window a request finishing exactly
// at the deadline would otherwise lose its model in.
func (s *Server) onIdleExpired() {
	s.mu.Lock()
	now := s.now()
	loaded := s.stateLocked() == StateLoaded
	s.idlePolicyLocked()
	left, armed := s.idle.remaining(now)
	idle := s.idle.policy().Idle
	generation := s.idle.generation()
	deferred := s.idle.deferElapsed(now)
	s.mu.Unlock()
	inFlight := s.InFlightRequests()

	if !loaded || !armed || left > 0 {
		return
	}
	if inFlight > 0 {
		if deferred < idle {
			s.deferIdleUnload(inFlight, deferred)
			return
		}
		// The wall-time bound is spent: this episode has already deferred for a
		// full idle budget, so the positive count is no longer evidence of a
		// generation being served — it is a leak, and honouring it any longer
		// would pin the model resident for the lifetime of the process. The
		// count is named so the leak is visible in the log.
		s.logger().Warn("unloading the embedded LLM with requests still counted in flight: the idle deferral has already lasted a full idle budget, so the in-flight count is leaked",
			"in_flight", inFlight, "idle", idle, "deferred", deferred)
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.stopTimeout()+s.killWait()+time.Second)
	defer cancel()
	s.logger().Info("unloading the embedded LLM after an idle period", "idle", idle)
	if err := s.unloadIfIdle(ctx, generation); err != nil {
		s.logger().Warn("the idle unload of the embedded LLM failed", "error", err)
	}
}

// deferIdleUnload re-arms the idle timer for one grace period because a request
// is still being served. It deliberately does NOT stamp activity: the budget
// stays spent, so the unload lands one grace after the last request completes
// rather than a full budget after it. MarkActivity — which the transport calls on
// completion — is what grants the fresh budget.
//
// deferred is how long the current episode has already been deferring, logged so
// an operator watching a long generation can see how much of the wall-time bound
// (one full idle budget) it has used.
func (s *Server) deferIdleUnload(inFlight int64, deferred time.Duration) {
	s.mu.Lock()
	loaded := s.stateLocked() == StateLoaded
	s.idlePolicyLocked()
	// The budget is RE-READ under the lock, because the decision to defer was
	// made outside it: onIdleExpired releases s.mu before it reads the in-flight
	// count, and a request that completes in that window stamps activity BEFORE
	// it releases the count (activityBody.finish() runs before end()) — so the
	// fresh FULL budget mark() armed can already be in place while this call
	// still sees a positive count. armGrace begins with stopTimerLocked, which
	// stops whatever is armed, so deferring unconditionally here would throw that
	// budget away for a 30 s grace and unload the model 30 s after genuine
	// activity: the next request would pay a multi-minute cold load. This is the
	// same staleness the `left > 0` guard in onIdleExpired rejects, re-checked at
	// the only place that can still act on the stale read.
	if left, _ := s.idle.remaining(s.now()); left > 0 {
		s.mu.Unlock()
		s.logger().Debug("an idle deferral was superseded by the fresh budget activity armed; keeping that budget",
			"in_flight", inFlight, "left", left)
		return
	}
	s.idle.armGrace(s.now(), idleGracePeriod, loaded, s.onIdleExpired)
	s.mu.Unlock()
	s.logger().Info("the embedded LLM idle budget expired while a request was in flight; deferring the unload",
		"in_flight", inFlight, "grace", idleGracePeriod, "deferred", deferred)
}
