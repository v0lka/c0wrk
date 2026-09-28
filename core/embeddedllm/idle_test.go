package embeddedllm

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The idle budget has one rule that is easy to get wrong and expensive when it is
// wrong: loading 6.7 GiB of weights takes minutes, and those minutes belong to
// the load, not to the user's inactivity. Every test here pins that from a
// different angle — a fake clock for the arithmetic, real timers for the
// behaviour.

// ── acceptance: the load is not idle time ──

// TestLoadTimeIsNotChargedToTheIdleBudget is the acceptance test for the
// requirement. A "load" that takes ten minutes of clock time must leave the whole
// sixty-minute budget intact, because the budget starts when the model becomes
// usable — not when the user clicked Load.
func TestLoadTimeIsNotChargedToTheIdleBudget(t *testing.T) {
	t.Parallel()

	const (
		minutePerProbe = time.Minute
		notReadyProbes = 9 // the tenth probe is the one that answers
		budget         = 60 * time.Minute
	)
	loadStart := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	clock := newFakeClock(loadStart)

	fx := newFixture(t, fixtureOptions{
		now:        clock.Now,
		autoUnload: AutoUnload{Enabled: true, Idle: budget},
		notReady:   notReadyProbes,
		// Every probe is one minute of weight loading.
		onProbe: func(int) { clock.advance(minutePerProbe) },
	})

	loadDuration := time.Duration(notReadyProbes+1) * minutePerProbe
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := fx.srv.State(); got != StateLoaded {
		t.Fatalf("state = %s, want %s", got, StateLoaded)
	}
	if hits := fx.endpoint.hits("/v1/models"); hits != notReadyProbes+1 {
		t.Fatalf("the load was probed %d times, want %d", hits, notReadyProbes+1)
	}

	// The clock really did move: this is a ten-minute load compressed into
	// milliseconds of wall time.
	if got, want := clock.Now(), loadStart.Add(loadDuration); !got.Equal(want) {
		t.Fatalf("the fixture clock is at %s, want %s", got, want)
	}

	// The whole budget is left. Had the stamp been taken when the load STARTED,
	// ten of the sixty minutes would already be gone.
	left, armed := fx.srv.IdleRemaining()
	if !armed {
		t.Fatal("the idle timer is not armed for a loaded model")
	}
	if left != budget {
		t.Errorf("idle budget left after a %s load = %s, want the full %s",
			loadDuration, left, budget)
	}
	if got, want := fx.srv.LastActivity(), loadStart.Add(loadDuration); !got.Equal(want) {
		t.Errorf("lastActivity = %s, want the moment the load completed (%s)", got, want)
	}

	// And the countdown runs from the completion: fifteen more minutes of
	// inactivity leave forty-five, not thirty-five.
	clock.advance(15 * time.Minute)
	if left, _ := fx.srv.IdleRemaining(); left != budget-15*time.Minute {
		t.Errorf("idle budget left = %s, want %s", left, budget-15*time.Minute)
	}
	if got := fx.srv.State(); got != StateLoaded {
		t.Errorf("state = %s; ten minutes of loading must not have eaten the budget", got)
	}
}

// TestIdleBudgetSurvivesASlowLoadInRealTime repeats the same property with the
// real clock and a real timer, so it cannot be satisfied by fake-clock arithmetic
// alone: the process must still be alive well past the point at which a
// load-start stamp would have expired the budget.
func TestIdleBudgetSurvivesASlowLoadInRealTime(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{
		autoUnload: AutoUnload{Enabled: true, Idle: 300 * time.Millisecond},
		notReady:   25,
		readyDelay: 10 * time.Millisecond, // the ticker floors the load at ~250 ms
	})

	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	proc := fx.spawner.lastProcess()

	// Half the budget after the load COMPLETED. A budget stamped at the spawn
	// would already have consumed the ~250 ms of loading against the 300 ms
	// idle, and the model would be gone by now.
	time.Sleep(150 * time.Millisecond)
	if got := fx.srv.State(); got != StateLoaded {
		t.Fatalf("state = %s, want %s while the budget still has time left", got, StateLoaded)
	}
	if !proc.alive() {
		t.Fatal("the process was stopped while the user was still inside the idle budget")
	}
	waitFor(t, 3*time.Second, func() bool { return !proc.alive() },
		"the idle unload to stop the process once the budget really ran out")
}

// ── acceptance: the timer fires ──

// TestAutoUnloadStopsTheProcessAfterTheIdleBudget is the acceptance test for the
// timer itself: the budget runs out, the process is gone, and the state reports
// the model as unloaded (installed on disk, not resident).
func TestAutoUnloadStopsTheProcessAfterTheIdleBudget(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{
		autoUnload: AutoUnload{Enabled: true, Idle: 60 * time.Millisecond},
	})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	proc := fx.spawner.lastProcess()
	if got := fx.srv.State(); got != StateLoaded {
		t.Fatalf("state = %s, want %s", got, StateLoaded)
	}

	waitForState(t, fx.srv, StateInstalled)
	if proc.alive() {
		t.Error("the process is still running after the idle budget expired")
	}
	if len(proc.signalLog()) == 0 && proc.killCount() == 0 {
		t.Error("the process was never asked to stop")
	}
	states := fx.events.states()
	if !containsState(states, StateUnloading) {
		t.Errorf("no unloading transition was emitted: %v", states)
	}
	// Synchronised on the RECORDER, not only on the state above: transition
	// publishes the state under s.mu and emits after unlocking, so a state wait can
	// return before the event is recorded and last() would still name the previous
	// transition.
	waitForEventState(t, fx.events, StateInstalled)
	if left, armed := fx.srv.IdleRemaining(); armed || left != 0 {
		t.Errorf("the timer is still armed after an idle unload: %v armed=%v", left, armed)
	}
	if got := fx.srv.Status().Pid; got != 0 {
		t.Errorf("Status().Pid = %d after the unload, want 0", got)
	}

	// The bytes are untouched — an idle unload is not a removal.
	requireFile(t, fx.manifest.ModelFile, "the weights")
	path, err := fx.layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	requireFile(t, path, "the manifest")
}

// TestAutoUnloadDisabledKeepsTheModelResident is the acceptance test for the
// operator's off switch: with enabled = false the model stays loaded no matter
// how long it is idle.
func TestAutoUnloadDisabledKeepsTheModelResident(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{
		autoUnload: AutoUnload{Enabled: false, Idle: 40 * time.Millisecond},
	})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	proc := fx.spawner.lastProcess()

	// Several times the (ignored) budget.
	time.Sleep(250 * time.Millisecond)

	if got := fx.srv.State(); got != StateLoaded {
		t.Errorf("state = %s, want %s with the timer disabled", got, StateLoaded)
	}
	if !proc.alive() {
		t.Error("the model was unloaded although auto_unload.enabled is false")
	}
	if left, armed := fx.srv.IdleRemaining(); armed || left != 0 {
		t.Errorf("a disabled policy reports a budget: %v armed=%v", left, armed)
	}
	if states := fx.events.states(); containsState(states, StateUnloading) {
		t.Errorf("an unloading transition was emitted with the timer disabled: %v", states)
	}
}

// ── activity ──

// TestMarkActivityRestartsTheIdleBudget pins the transport's half of the
// contract: a completed request restarts the budget from zero.
func TestMarkActivityRestartsTheIdleBudget(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	clock := newFakeClock(start)
	fx := newFixture(t, fixtureOptions{
		now:        clock.Now,
		autoUnload: AutoUnload{Enabled: true, Idle: 60 * time.Minute},
	})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	clock.advance(45 * time.Minute)
	if left, _ := fx.srv.IdleRemaining(); left != 15*time.Minute {
		t.Fatalf("budget left = %s, want 15m", left)
	}

	fx.srv.MarkActivity()
	if left, armed := fx.srv.IdleRemaining(); !armed || left != 60*time.Minute {
		t.Errorf("budget left after a request = %s (armed %v), want the full hour", left, armed)
	}
	if got, want := fx.srv.LastActivity(), clock.Now(); !got.Equal(want) {
		t.Errorf("lastActivity = %s, want %s", got, want)
	}
	if got := fx.srv.State(); got != StateLoaded {
		t.Errorf("state = %s, want %s", got, StateLoaded)
	}
}

// TestMarkActivityKeepsALongGenerationResident covers the case the "stamp on
// completion" rule exists for: a request that takes longer than the remaining
// budget must not unload the model mid-answer.
//
// The completion stamp alone cannot guarantee that — it fires only AFTER the
// answer, so a generation longer than the WHOLE budget would have its expiry tick
// land mid-stream. The in-flight count is what the expiry checks first, so the
// tick is driven directly here instead of being left to a real timer.
func TestMarkActivityKeepsALongGenerationResident(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	clock := newFakeClock(start)
	fx := newFixture(t, fixtureOptions{
		now:        clock.Now,
		autoUnload: AutoUnload{Enabled: true, Idle: 60 * time.Minute},
	})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	proc := fx.spawner.lastProcess()

	// A single request that runs for two hours of clock time. Nothing stamps
	// activity while it is in flight, so the budget expires MID-GENERATION and
	// the timer callback runs with the request still open.
	fx.srv.BeginRequest()
	if got := fx.srv.InFlightRequests(); got != 1 {
		t.Fatalf("in-flight requests = %d, want 1", got)
	}
	clock.advance(2 * time.Hour)
	fx.srv.onIdleExpired()

	if got := fx.srv.State(); got != StateLoaded {
		t.Errorf("state = %s, want %s: an expiry during a request must defer the unload",
			got, StateLoaded)
	}
	if !proc.alive() {
		t.Error("the process was stopped mid-generation, killing the answer it was serving")
	}
	if left, armed := fx.srv.IdleRemaining(); !armed || left != idleGracePeriod {
		t.Errorf("budget left = %s (armed %v), want the %s deferral to be visible",
			left, armed, idleGracePeriod)
	}

	// A second expiry while the request is still open defers again: the grace is
	// a re-check interval, not a one-shot extension, so a generation longer than
	// the idle budget is not cut short. The re-checks are bounded by WALL TIME,
	// though — one episode may not outlast a full idle budget — which is what
	// TestALeakedInFlightCountStillUnloadsAfterOneExtraIdleBudget pins from the
	// other side.
	clock.advance(idleGracePeriod)
	fx.srv.onIdleExpired()
	if !proc.alive() || fx.srv.State() != StateLoaded {
		t.Fatal("a repeated expiry during the same request unloaded the model")
	}

	// The response completes: the count is released and the stamp restores a FULL
	// budget, so the expiry that follows is the stale one and must not unload.
	fx.srv.EndRequest()
	fx.srv.MarkActivity()
	if got := fx.srv.InFlightRequests(); got != 0 {
		t.Errorf("in-flight requests after completion = %d, want 0", got)
	}
	if left, armed := fx.srv.IdleRemaining(); !armed || left != 60*time.Minute {
		t.Errorf("budget left = %s (armed %v), want a full hour after the response completed",
			left, armed)
	}
	fx.srv.onIdleExpired()
	if got := fx.srv.State(); got != StateLoaded {
		t.Errorf("state = %s after a stale expiry, want %s", got, StateLoaded)
	}
	if !proc.alive() {
		t.Error("a stale expiry killed a model that had just been used")
	}
}

// TestADeferralDoesNotReplaceAFreshBudgetWithTheGrace pins the re-read inside
// deferIdleUnload. The transport stamps activity BEFORE it releases its in-flight
// count (activityBody.finish() runs before end()), so there is a window where a
// completing request has already armed a FULL budget while the count is still
// positive. An expiry whose in-flight read landed in that window reaches the
// deferral with a fresh budget already armed — and armGrace begins with
// stopTimerLocked, which stops whatever is armed, so deferring unconditionally
// replaces the operator's budget with a 30 s grace and unloads the model 30 s
// after genuine activity. The next request then pays a multi-minute cold load.
//
// The window is staged directly rather than raced. onIdleExpired's own `left > 0`
// guard returns before it ever defers once activity has landed, so the only way
// to reach deferIdleUnload with a fresh budget armed is the interleaving that
// guard cannot see: an expiry that DECIDED before the stamp and deferred after it.
func TestADeferralDoesNotReplaceAFreshBudgetWithTheGrace(t *testing.T) {
	t.Parallel()

	const budget = 60 * time.Minute
	start := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	clock := newFakeClock(start)
	fx := newFixture(t, fixtureOptions{
		now:        clock.Now,
		autoUnload: AutoUnload{Enabled: true, Idle: budget},
	})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	proc := fx.spawner.lastProcess()

	// The budget expires mid-generation, and the expiry defers as it should.
	fx.srv.BeginRequest()
	clock.advance(budget + time.Minute)
	fx.srv.onIdleExpired()
	if left, armed := fx.srv.IdleRemaining(); !armed || left != idleGracePeriod {
		t.Fatalf("the deferral left %s (armed %v), want the %s grace",
			left, armed, idleGracePeriod)
	}

	// The response completes: activity arms a FULL budget while the count is
	// still positive — the window itself.
	fx.srv.MarkActivity()
	if got := fx.srv.InFlightRequests(); got != 1 {
		t.Fatalf("in-flight requests = %d, want the positive count the window needs", got)
	}

	// The stale expiry's deferral lands now.
	fx.srv.deferIdleUnload(1, time.Minute)

	if left, armed := fx.srv.IdleRemaining(); !armed || left != budget {
		t.Errorf("budget left = %s (armed %v), want the full %s the activity armed: "+
			"the deferral replaced it with the grace", left, armed, budget)
	}
	if !proc.alive() || fx.srv.State() != StateLoaded {
		t.Error("the deferral path unloaded a model that had just been used")
	}

	// The budget that survived is a working one, not a stale report: once the
	// count is released and it expires, the model goes.
	fx.srv.EndRequest()
	clock.advance(budget + time.Minute)
	fx.srv.onIdleExpired()
	waitFor(t, 5*time.Second, func() bool { return !proc.alive() },
		"the budget activity armed to expire once nothing is in flight any more")
	waitForState(t, fx.srv, StateInstalled)
}

// TestALeakedInFlightCountStillUnloadsAfterOneExtraIdleBudget pins the bound on
// the deferral. EndRequest runs only from activityBody.finish(), so a response
// body nobody ever closed leaves the count positive for the rest of the process's
// life — and an unbounded deferral would then keep the multi-gigabyte model
// resident forever while every idle budget expired. The bound is WALL TIME: one
// deferral episode may not outlast a full idle budget, after which the unload
// proceeds and names the leaked count at Warn.
func TestALeakedInFlightCountStillUnloadsAfterOneExtraIdleBudget(t *testing.T) {
	t.Parallel()

	// Three re-checks per episode, so the walk below distinguishes "still inside
	// the extra budget" from "the extra budget is spent".
	const idle = 3 * idleGracePeriod
	start := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	clock := newFakeClock(start)
	fx := newFixture(t, fixtureOptions{
		now:        clock.Now,
		autoUnload: AutoUnload{Enabled: true, Idle: idle},
	})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	proc := fx.spawner.lastProcess()

	// The leak: a request whose response is never finished, so the count never
	// returns to zero and nothing stamps activity again.
	fx.srv.BeginRequest()

	clock.advance(idle + time.Second)
	fx.srv.onIdleExpired()
	if !proc.alive() || fx.srv.State() != StateLoaded {
		t.Fatal("the first expiry unloaded a model with a request in flight instead of deferring")
	}

	steps := int(idle / idleGracePeriod)
	for step := 1; step <= steps; step++ {
		clock.advance(idleGracePeriod)
		fx.srv.onIdleExpired()
		if step < steps {
			// Inside the extra budget a leak is indistinguishable from a long
			// generation, so the deferral must hold.
			if !proc.alive() || fx.srv.State() != StateLoaded {
				t.Fatalf("re-check %d of %d unloaded a model inside the extra idle budget", step, steps)
			}
			continue
		}
		// The episode has now lasted a full idle budget since it started, so the
		// count is a leak rather than a generation and the unload proceeds.
		waitFor(t, 5*time.Second, func() bool { return !proc.alive() },
			"a leaked in-flight count to stop deferring the idle unload")
		waitForState(t, fx.srv, StateInstalled)
	}

	if got := fx.srv.InFlightRequests(); got != 1 {
		t.Errorf("in-flight requests = %d, want the staged leak to still be counted", got)
	}
	logs := fx.logs.String()
	if !strings.Contains(logs, "level=WARN") {
		t.Errorf("the unload of a leaked count was not reported at Warn: %q", logs)
	}
	if !strings.Contains(logs, "in-flight count is leaked") || !strings.Contains(logs, "in_flight=1") {
		t.Errorf("the Warn does not name the leak and its count: %q", logs)
	}
	// The episode ended with the unload: no timer, and no half-spent wall-time
	// bound left behind for the next load to inherit.
	if left, armed := fx.srv.IdleRemaining(); armed || left != 0 {
		t.Errorf("the timer survived the idle unload: %v armed=%v", left, armed)
	}
}

// TestIdleExpiryWithoutARequestStillUnloads pins the other half of the deferral:
// a request in flight is the ONLY thing that stops an expiry. With no request
// open, the tick must still stop the process — a deferral that never ended would
// leave gigabytes resident forever.
func TestIdleExpiryWithoutARequestStillUnloads(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	clock := newFakeClock(start)
	fx := newFixture(t, fixtureOptions{
		now:        clock.Now,
		autoUnload: AutoUnload{Enabled: true, Idle: 60 * time.Minute},
	})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	proc := fx.spawner.lastProcess()

	clock.advance(61 * time.Minute)
	fx.srv.onIdleExpired()

	waitFor(t, 5*time.Second, func() bool { return !proc.alive() },
		"the idle expiry to stop a model nothing is using")
	if got := fx.srv.State(); got != StateInstalled {
		t.Errorf("state = %s, want %s after the idle unload", got, StateInstalled)
	}
}

// TestIdleExpiryIsSupersededByActivity closes the TOCTOU between the expiry's
// decision and the unload it performs: Unload has to re-acquire both the gate and
// Server.mu, and a request that completes in that window re-arms a fresh budget —
// so the unload must be abandoned rather than pulling the model out from under
// the request that just arrived.
func TestIdleExpiryIsSupersededByActivity(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	clock := newFakeClock(start)
	fx := newFixture(t, fixtureOptions{
		now:        clock.Now,
		autoUnload: AutoUnload{Enabled: true, Idle: 60 * time.Minute},
	})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	proc := fx.spawner.lastProcess()

	clock.advance(61 * time.Minute)
	// The generation the expiry decided on, captured the way onIdleExpired does.
	fx.srv.mu.Lock()
	generation := fx.srv.idle.generation()
	fx.srv.mu.Unlock()

	// Activity lands before the unload can take the gate.
	fx.srv.MarkActivity()

	if err := fx.srv.unloadIfIdle(context.Background(), generation); err != nil {
		t.Fatalf("unloadIfIdle: %v", err)
	}
	if !proc.alive() {
		t.Error("the stale idle unload stopped a model that had just been used")
	}
	if got := fx.srv.State(); got != StateLoaded {
		t.Errorf("state = %s, want %s", got, StateLoaded)
	}
	if left, armed := fx.srv.IdleRemaining(); !armed || left != 60*time.Minute {
		t.Errorf("budget left = %s (armed %v), want the fresh hour the activity stamped",
			left, armed)
	}
}

// TestIdleExpiryAfterThePolicyIsDisabled pins the stale-callback guard for the
// operator's off switch: a tick that was already scheduled when the policy was
// disabled reports armed=false, and must not unload a model the operator just
// exempted.
func TestIdleExpiryAfterThePolicyIsDisabled(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	clock := newFakeClock(start)
	fx := newFixture(t, fixtureOptions{
		now:        clock.Now,
		autoUnload: AutoUnload{Enabled: true, Idle: 60 * time.Minute},
	})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	proc := fx.spawner.lastProcess()

	clock.advance(61 * time.Minute)
	fx.srv.SetAutoUnload(AutoUnload{Enabled: false, Idle: 60 * time.Minute})
	fx.srv.onIdleExpired()

	if !proc.alive() {
		t.Error("a stale tick unloaded a model whose policy had just been disabled")
	}
	if got := fx.srv.State(); got != StateLoaded {
		t.Errorf("state = %s, want %s", got, StateLoaded)
	}
}

// TestMarkActivityWithoutALoadedModelArmsNothing checks that a stamp made while
// the model is not resident cannot arm a timer: there would be nothing to
// unload, and the next load must start with a budget of its own.
func TestMarkActivityWithoutALoadedModelArmsNothing(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	clock := newFakeClock(start)
	fx := newFixture(t, fixtureOptions{
		now:        clock.Now,
		autoUnload: AutoUnload{Enabled: true, Idle: 60 * time.Minute},
	})

	fx.srv.MarkActivity()
	if left, armed := fx.srv.IdleRemaining(); armed || left != 0 {
		t.Errorf("a stamp without a loaded model armed the timer: %v armed=%v", left, armed)
	}
	if got := fx.srv.LastActivity(); !got.Equal(clock.Now()) {
		t.Errorf("lastActivity = %s, want the stamp to be recorded anyway (%s)", got, clock.Now())
	}

	// The load that follows starts its own budget from its own completion.
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	clock.advance(30 * time.Minute)
	if left, armed := fx.srv.IdleRemaining(); !armed || left != 30*time.Minute {
		t.Errorf("budget left after the load = %s (armed %v), want 30m", left, armed)
	}
}

// TestIdleTimerIsDisarmedByAnExplicitUnload checks the manual path: an operator
// unload leaves no timer behind that could fire against the next load.
func TestIdleTimerIsDisarmedByAnExplicitUnload(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{
		autoUnload: AutoUnload{Enabled: true, Idle: 60 * time.Millisecond},
	})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := fx.srv.Unload(context.Background()); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	if left, armed := fx.srv.IdleRemaining(); armed || left != 0 {
		t.Errorf("the timer survived an explicit unload: %v armed=%v", left, armed)
	}

	time.Sleep(200 * time.Millisecond)
	if got := fx.srv.State(); got != StateInstalled {
		t.Errorf("state = %s, want %s (a stale timer fired)", got, StateInstalled)
	}
	if n := fx.spawner.spawnCount(); n != 1 {
		t.Errorf("%d processes were spawned, want 1", n)
	}
}

// ── runtime policy changes ──

// TestSetAutoUnloadTakesEffectImmediately covers the Settings surface: turning
// the timer on, off, and shortening it while a model is loaded.
//
// The clock is FAKE and the shortening step advances past the new budget first,
// so the spent-budget arm is reached deterministically. On the wall clock only a
// few milliseconds elapse between the load and the policy change, so `wait` stays
// positive and the test silently exercises the ORDINARY arm instead — and on a
// machine slow enough to burn the new budget first it flips to the other one and
// fails. What the Server does with a spent budget is pinned by
// TestEnablingAutoUnloadUnloadsAModelIdleSinceItWasDisabled and
// TestShorteningTheBudgetBelowTheTimeAlreadyIdleUnloads.
func TestSetAutoUnloadTakesEffectImmediately(t *testing.T) {
	t.Parallel()

	const (
		budget    = time.Hour
		shortened = 5 * time.Minute
	)
	start := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	clock := newFakeClock(start)
	fx := newFixture(t, fixtureOptions{
		now:        clock.Now,
		autoUnload: AutoUnload{Enabled: false, Idle: budget},
	})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, armed := fx.srv.IdleRemaining(); armed {
		t.Fatal("the timer is armed although the policy started disabled")
	}

	// Enabling arms it against the existing activity stamp.
	fx.srv.SetAutoUnload(AutoUnload{Enabled: true, Idle: budget})
	if left, armed := fx.srv.IdleRemaining(); !armed || left != budget {
		t.Errorf("enabling the policy left %s (armed %v), want the full %s", left, armed, budget)
	}
	if got := fx.srv.AutoUnloadPolicy(); !got.Enabled || got.Idle != budget {
		t.Errorf("AutoUnloadPolicy() = %+v", got)
	}

	// Disabling disarms it again.
	fx.srv.SetAutoUnload(AutoUnload{Enabled: false, Idle: budget})
	if left, armed := fx.srv.IdleRemaining(); armed || left != 0 {
		t.Errorf("disabling the policy left a budget: %v armed=%v", left, armed)
	}

	// Shortening below the time already spent unloads the model, rather than
	// granting a fresh budget. Forty-five of the sixty minutes are gone and the
	// new budget is five, so the arm sees a budget that is already spent.
	clock.advance(45 * time.Minute)
	fx.srv.SetAutoUnload(AutoUnload{Enabled: true, Idle: shortened})
	waitForState(t, fx.srv, StateInstalled)
	if proc := fx.spawner.lastProcess(); proc.alive() {
		t.Error("shortening the budget did not stop the process")
	}

	// The durable half of the contract: the immediate fire is a ONE-SHOT arm, not
	// a dead policy, so the load that follows comes up with a timer of its own.
	requireReloadArmsTheTimer(t, fx, shortened)
}

// TestEnablingAutoUnloadUnloadsAModelIdleSinceItWasDisabled is operator sequence
// (a) for the spent-budget arm: the policy started DISABLED, the model has been
// resident for hours, and the operator turns auto-unload on — the exact moment
// they want the gigabytes back. The budget is spent the instant it is armed, so
// the arm fires at once; a callback that re-validates against a tracker reporting
// "not armed" rejects its own fire, and nothing re-arms afterwards, which would
// leave the model resident for the rest of the process lifetime while every
// status surface says the policy is enabled.
func TestEnablingAutoUnloadUnloadsAModelIdleSinceItWasDisabled(t *testing.T) {
	t.Parallel()

	const budget = 5 * time.Minute
	start := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	clock := newFakeClock(start)
	fx := newFixture(t, fixtureOptions{
		now:        clock.Now,
		autoUnload: AutoUnload{Enabled: false, Idle: budget},
	})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	resident := fx.spawner.lastProcess()

	// Hours of residency with the timer off: nothing is armed, so nothing expires.
	clock.advance(3 * time.Hour)
	if left, armed := fx.srv.IdleRemaining(); armed || left != 0 {
		t.Fatalf("a disabled policy reported %s (armed %v)", left, armed)
	}
	if !resident.alive() {
		t.Fatal("the model was stopped while the policy was disabled")
	}

	fx.srv.SetAutoUnload(AutoUnload{Enabled: true, Idle: budget})

	waitFor(t, 5*time.Second, func() bool { return !resident.alive() },
		"enabling the policy to stop a model that has been idle longer than the budget")
	waitForState(t, fx.srv, StateInstalled)
	if got := fx.spawner.spawnCount(); got != 1 {
		t.Errorf("%d processes were spawned, want 1", got)
	}
	requireReloadArmsTheTimer(t, fx, budget)
}

// TestShorteningTheBudgetBelowTheTimeAlreadyIdleUnloads is operator sequence (b):
// the model has been idle for half of an hour-long budget and the operator
// shortens the budget to five minutes. setPolicy's own doc promises the outcome —
// "shortening it while a model has been idle for longer than the new value
// unloads that model" — and it lands on the same spent-budget arm as enabling
// from off.
func TestShorteningTheBudgetBelowTheTimeAlreadyIdleUnloads(t *testing.T) {
	t.Parallel()

	const (
		budget    = time.Hour
		shortened = 5 * time.Minute
	)
	start := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	clock := newFakeClock(start)
	fx := newFixture(t, fixtureOptions{
		now:        clock.Now,
		autoUnload: AutoUnload{Enabled: true, Idle: budget},
	})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	resident := fx.spawner.lastProcess()

	clock.advance(budget / 2)
	if left, armed := fx.srv.IdleRemaining(); !armed || left != budget/2 {
		t.Fatalf("budget left = %s (armed %v), want %s", left, armed, budget/2)
	}

	fx.srv.SetAutoUnload(AutoUnload{Enabled: true, Idle: shortened})

	waitFor(t, 5*time.Second, func() bool { return !resident.alive() },
		"a budget shortened below the time already idle to stop the process")
	waitForState(t, fx.srv, StateInstalled)
	if got := fx.spawner.spawnCount(); got != 1 {
		t.Errorf("%d processes were spawned, want 1", got)
	}
	requireReloadArmsTheTimer(t, fx, shortened)
}

// requireReloadArmsTheTimer is the durable half of both spent-budget sequences:
// after the immediate fire has unloaded the model, the NEXT load must come up
// with a timer of its own on the live budget. Without it the policy reads as
// enabled while the model the operator just reloaded can never be unloaded — and
// IdleRemaining would report armed=false for the rest of the process lifetime.
func requireReloadArmsTheTimer(t *testing.T, fx *fixture, budget time.Duration) {
	t.Helper()

	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("the reload after the idle unload: %v", err)
	}
	if left, armed := fx.srv.IdleRemaining(); !armed || left != budget {
		t.Errorf("budget left after the reload = %s (armed %v), want the full %s",
			left, armed, budget)
	}
	if got := fx.srv.State(); got != StateLoaded {
		t.Errorf("state = %s, want %s", got, StateLoaded)
	}
}

func TestAutoUnloadPolicyConstruction(t *testing.T) {
	t.Parallel()

	def := DefaultAutoUnload()
	if !def.Enabled || def.Idle != DefaultAutoUnloadMinutes*time.Minute {
		t.Errorf("DefaultAutoUnload() = %+v, want enabled/%dm", def, DefaultAutoUnloadMinutes)
	}
	if got := NewAutoUnload(true, 15); !got.Enabled || got.Idle != 15*time.Minute {
		t.Errorf("NewAutoUnload(true, 15) = %+v", got)
	}
	if got := NewAutoUnload(false, 5); got.Enabled || got.Idle != 5*time.Minute {
		t.Errorf("NewAutoUnload(false, 5) = %+v", got)
	}
	// An unusable budget falls back to the default instead of meaning "unload
	// immediately" — re-enabling the timer must never activate a dead budget.
	for minutes, want := range map[int]time.Duration{
		0:   DefaultAutoUnloadMinutes * time.Minute,
		-30: DefaultAutoUnloadMinutes * time.Minute,
		1:   time.Minute,
	} {
		if got := NewAutoUnload(true, minutes); got.Idle != want {
			t.Errorf("NewAutoUnload(true, %d).Idle = %s, want %s", minutes, got.Idle, want)
		}
	}
}

// ── the tracker in isolation ──

func TestIdleTimerTracker(t *testing.T) {
	t.Parallel()

	var fired atomic.Int32
	onExpire := func() { fired.Add(1) }
	start := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	tracker := &idleTimer{}

	// Unseeded: no policy, no stamp, nothing armed.
	if left, armed := tracker.remaining(start); armed || left != 0 {
		t.Errorf("a fresh tracker reports %v armed=%v", left, armed)
	}
	if got := tracker.lastActivity(); !got.IsZero() {
		t.Errorf("a fresh tracker has an activity stamp: %s", got)
	}

	// A stamp without arming records the time and nothing else.
	tracker.setPolicy(AutoUnload{Enabled: true, Idle: 40 * time.Millisecond}, start, false, onExpire)
	tracker.mark(start, false, onExpire)
	if left, armed := tracker.remaining(start); armed || left != 0 {
		t.Errorf("an unarmed tracker reports %v armed=%v", left, armed)
	}
	if got := tracker.lastActivity(); !got.Equal(start) {
		t.Errorf("lastActivity = %s, want %s", got, start)
	}

	// Arming re-uses the existing stamp: the budget is what is LEFT of it, not a
	// fresh one. Ten milliseconds later, a forty-millisecond budget has thirty
	// left.
	tracker.setPolicy(AutoUnload{Enabled: true, Idle: 40 * time.Millisecond},
		start.Add(10*time.Millisecond), true, onExpire)
	left, armed := tracker.remaining(start.Add(10 * time.Millisecond))
	if !armed || left != 30*time.Millisecond {
		t.Errorf("re-arming left %s (armed %v), want 30ms", left, armed)
	}
	waitFor(t, 2*time.Second, func() bool { return fired.Load() == 1 }, "the timer to fire once")

	// Disarming stops it and clears the armed flag, keeping the stamp.
	tracker.disarm()
	if left, armed := tracker.remaining(start.Add(50 * time.Millisecond)); armed || left != 0 {
		t.Errorf("a disarmed tracker reports %v armed=%v", left, armed)
	}
	if got := tracker.lastActivity(); got.IsZero() {
		t.Error("disarming dropped the activity stamp")
	}

	// A budget already spent fires at once rather than never — AND reports itself
	// as armed while it does, because the callback re-validates against exactly
	// that report: remaining derives `armed` from t.timer and onIdleExpired bails
	// on !armed, so an arm that fired without leaving a timer behind is an arm
	// whose own callback refuses to act. A bare counter cannot see the Server's
	// half of this; TestEnablingAutoUnloadUnloadsAModelIdleSinceItWasDisabled and
	// TestShorteningTheBudgetBelowTheTimeAlreadyIdleUnloads pin the consequence.
	fired.Store(0)
	tracker.mark(start, true, onExpire)
	tracker.setPolicy(AutoUnload{Enabled: true, Idle: 20 * time.Millisecond},
		start.Add(time.Hour), true, onExpire)
	if left, armed := tracker.remaining(start.Add(time.Hour)); !armed || left != 0 {
		t.Errorf("a spent budget reports %s (armed %v), want 0 with armed=true so the "+
			"immediate callback's own re-validation accepts it", left, armed)
	}
	waitFor(t, 2*time.Second, func() bool { return fired.Load() >= 1 }, "an expired budget to fire")

	// A disabled policy never arms, however it is set.
	tracker.disarm()
	tracker.setPolicy(AutoUnload{Enabled: false, Idle: time.Millisecond},
		start.Add(2*time.Hour), true, onExpire)
	if left, armed := tracker.remaining(start.Add(3 * time.Hour)); armed || left != 0 {
		t.Errorf("a disabled policy armed the timer: %v armed=%v", left, armed)
	}
	if got := tracker.policy(); got.Enabled {
		t.Errorf("policy() = %+v, want disabled", got)
	}
}

func TestAutoUnloadNormalizedKeepsAUsableBudget(t *testing.T) {
	t.Parallel()

	if got := (AutoUnload{Enabled: true, Idle: 90 * time.Second}).normalized(); got.Idle != 90*time.Second {
		t.Errorf("normalized() changed a usable budget: %+v", got)
	}
	if got := (AutoUnload{Enabled: true}).normalized(); got.Idle != DefaultAutoUnloadMinutes*time.Minute {
		t.Errorf("normalized() left a zero budget: %+v", got)
	}
	if got := (AutoUnload{Enabled: true, Idle: -time.Minute}).normalized(); got.Idle <= 0 {
		t.Errorf("normalized() kept a negative budget: %+v", got)
	}
	// The ceiling is clamped, not rejected and not wrapped: an "effectively
	// never" figure must not become a short budget (see autoUnloadBudget).
	if got := (AutoUnload{Enabled: true, Idle: maxAutoUnloadBudget}).normalized(); got.Idle != maxAutoUnloadBudget {
		t.Errorf("normalized() changed a budget at the ceiling: %+v", got)
	}
	if got := (AutoUnload{Enabled: true, Idle: maxAutoUnloadBudget + time.Hour}).normalized(); got.Idle != maxAutoUnloadBudget {
		t.Errorf("normalized() kept an above-ceiling budget: %+v", got)
	}
}

// TestNewAutoUnloadCannotOverflowTheBudget pins the arithmetic the persisted
// minutes go through. `time.Duration(minutes) * time.Minute` wraps above
// 153,722,867 minutes, and the wrapped product can be a small POSITIVE budget —
// 26 s at 307,445,735 minutes, and 2.048 µs at 3,749,353,613,647,811, the minimum
// positive residue of the 2^11 divisor of the minutes→nanoseconds factor (16
// digits, below 2^53, so a JS double carries it exactly and it survives the whole
// settings → config → core path) — or a negative one that normalized() would
// silently replace with the default. Either way the UI echoes the operator's
// number while the armed timer says something else, and at the microsecond
// residue the model would be unloaded after every single load.
func TestNewAutoUnloadCannotOverflowTheBudget(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		minutes int
		want    time.Duration
	}{
		// Below the ceiling the minutes→nanoseconds multiply does not wrap, so
		// the budget is the unwrapped product. This is the branch every clamped
		// case below is indistinguishable from without it.
		{"42 minutes, below the ceiling", 42, 42 * time.Minute},
		{"one year, the documented ceiling", MaxAutoUnloadMinutes, maxAutoUnloadBudget},
		// Everything above the ceiling clamps to it — never wrapped, never
		// silently defaulted. The residues are what makes the clamp
		// load-bearing: the raw multiply wraps to 26 s at 307,445,735 minutes
		// and to 2.048 µs at 3,749,353,613,647,811, the minimum positive
		// residue of the 2^11 divisor of the minutes→nanoseconds factor.
		{"one minute above the ceiling", MaxAutoUnloadMinutes + 1, maxAutoUnloadBudget},
		{"the largest non-wrapping minutes value", 153722867, maxAutoUnloadBudget},
		{"the first wrapping minutes value", 153722868, maxAutoUnloadBudget},
		{"a wrapped positive residue (26 s)", 307445735, maxAutoUnloadBudget},
		{"int32's maximum", 2147483647, maxAutoUnloadBudget},
		{"the minimum positive residue (2.048 µs)", 3749353613647811, maxAutoUnloadBudget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := NewAutoUnload(true, tc.minutes); got.Idle != tc.want {
				t.Errorf("NewAutoUnload(true, %d).Idle = %s, want %s", tc.minutes, got.Idle, tc.want)
			}
		})
	}

	// The contrast the case labels promise, asserted directly rather than left
	// to the reader: at the first wrapping value the raw multiply is NEGATIVE —
	// which normalized() would replace with the 60-minute default, so the UI
	// would echo 153,722,868 minutes while the armed timer said one hour — while
	// the policy the operator gets is the ceiling. The minutes go through a
	// variable so the multiply happens at runtime and wraps the way an
	// operator's value would; written as constants it is a compile-time error.
	const firstWrappingMinutes = 153722868
	minutes := firstWrappingMinutes
	if wrapped := time.Duration(minutes) * time.Minute; wrapped >= 0 {
		t.Fatalf("time.Duration(%d) * time.Minute = %s, want a negative wrapped product; "+
			"without the wrap the ceiling clamp guards nothing", minutes, wrapped)
	}
	if got := NewAutoUnload(true, firstWrappingMinutes); got.Idle != maxAutoUnloadBudget {
		t.Errorf("NewAutoUnload(true, %d).Idle = %s, want the %s ceiling — "+
			"neither the wrapped product nor the default normalized() would substitute for it",
			firstWrappingMinutes, got.Idle, maxAutoUnloadBudget)
	}

	// The non-positive end still defaults, and the disabled policy is unaffected.
	if got := NewAutoUnload(true, 0); got.Idle != DefaultAutoUnloadMinutes*time.Minute {
		t.Errorf("NewAutoUnload(true, 0).Idle = %s, want the default", got.Idle)
	}
	if got := NewAutoUnload(false, MaxAutoUnloadMinutes+1); got.Idle != maxAutoUnloadBudget || got.Enabled {
		t.Errorf("NewAutoUnload(false, above the ceiling) = %+v, want a clamped, disabled policy", got)
	}
}
