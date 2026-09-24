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

// AutoUnload is the operator's idle policy — the in-memory shape of
// embedded_llm.auto_unload.
type AutoUnload struct {
	// Enabled false disables the timer entirely: the model stays resident until
	// an explicit Unload, a Remove, or app shutdown.
	Enabled bool
	// Idle is the inactivity budget after which the process is stopped. A value
	// below one minute is clamped to the default (see normalized), which mirrors
	// the config rule that rejects a non-positive minutes value even while the
	// timer is disabled — re-enabling it must never activate a dead budget.
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

// NewAutoUnload builds a policy from the persisted config shape
// (auto_unload.enabled + auto_unload.minutes).
func NewAutoUnload(enabled bool, minutes int) AutoUnload {
	return AutoUnload{Enabled: enabled, Idle: time.Duration(minutes) * time.Minute}.normalized()
}

// normalized clamps an unusable budget to the default instead of leaving it at
// zero. Zero would mean "unload immediately" once the timer is armed, which is
// the opposite of what an operator who wrote nothing expects. A positive budget
// is left alone: the config layer validates the persisted minutes, and a caller
// that wants a sub-minute budget (a test) means it.
func (p AutoUnload) normalized() AutoUnload {
	if p.Idle <= 0 {
		p.Idle = DefaultAutoUnloadMinutes * time.Minute
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
	if !armed {
		return
	}
	if t.last.IsZero() {
		t.last = now
	}
	t.armLocked(now, onExpire)
}

// mark stamps activity and, when armed, (re)starts the budget from that stamp.
func (t *idleTimer) mark(now time.Time, armed bool, onExpire func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.last = now
	t.stopLocked()
	if armed {
		t.armLocked(now, onExpire)
	}
}

// armLocked creates the timer for whatever budget is left. t.mu must be held.
func (t *idleTimer) armLocked(now time.Time, onExpire func()) {
	if !t.pol.Enabled || t.pol.Idle <= 0 {
		return
	}
	wait := t.pol.Idle
	if !t.last.IsZero() {
		wait = t.pol.Idle - now.Sub(t.last)
	}
	if wait <= 0 {
		// The budget is already spent. Fire on its own goroutine: the caller
		// holds Server.mu, and the unload needs it.
		go onExpire()
		return
	}
	t.timer = time.AfterFunc(wait, onExpire)
}

// disarm stops the timer without touching the activity stamp or the policy.
func (t *idleTimer) disarm() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopLocked()
}

func (t *idleTimer) stopLocked() {
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
}

// remaining reports the budget left before an auto-unload. ok is false when no
// timer is armed — the policy is disabled, or the model is not loaded — which is
// what lets a caller distinguish "60 minutes left" from "this never unloads".
func (t *idleTimer) remaining(now time.Time) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.timer == nil || !t.pol.Enabled || t.pol.Idle <= 0 || t.last.IsZero() {
		return 0, false
	}
	left := t.pol.Idle - now.Sub(t.last)
	if left < 0 {
		left = 0
	}
	return left, true
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
func (s *Server) MarkActivity() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.idlePolicyLocked()
	s.recordActivityLocked()
	s.mu.Unlock()
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

// onIdleExpired is the timer callback: the budget ran out, so the process is
// stopped. It re-checks under the lock, because an activity stamp that raced the
// timer already re-armed a fresh budget and this callback is the stale one.
func (s *Server) onIdleExpired() {
	s.mu.Lock()
	loaded := s.stateLocked() == StateLoaded
	s.idlePolicyLocked()
	left, armed := s.idle.remaining(s.now())
	idle := s.idle.policy().Idle
	s.mu.Unlock()

	if !loaded || (armed && left > 0) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.stopTimeout()+s.killWait()+time.Second)
	defer cancel()
	s.logger().Info("unloading the embedded LLM after an idle period", "idle", idle)
	if err := s.Unload(ctx); err != nil {
		s.logger().Warn("the idle unload of the embedded LLM failed", "error", err)
	}
}
