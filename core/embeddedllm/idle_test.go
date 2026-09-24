package embeddedllm

import (
	"context"
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
	last, ok := fx.events.last()
	if !ok || last.State != StateInstalled {
		t.Errorf("last event = %+v, want the unloaded (installed) state", last)
	}
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

	// A single request that runs for two hours of clock time. Nothing stamps
	// activity while it is in flight, so the budget expires — but the stamp the
	// transport makes when the response completes must restore a full budget
	// rather than leaving the model marked as long-idle.
	clock.advance(2 * time.Hour)
	fx.srv.MarkActivity()

	if left, armed := fx.srv.IdleRemaining(); !armed || left != 60*time.Minute {
		t.Errorf("budget left = %s (armed %v), want a full hour after the response completed",
			left, armed)
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
func TestSetAutoUnloadTakesEffectImmediately(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{
		autoUnload: AutoUnload{Enabled: false, Idle: time.Hour},
	})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, armed := fx.srv.IdleRemaining(); armed {
		t.Fatal("the timer is armed although the policy started disabled")
	}

	// Enabling arms it against the existing activity stamp.
	fx.srv.SetAutoUnload(AutoUnload{Enabled: true, Idle: time.Hour})
	left, armed := fx.srv.IdleRemaining()
	if !armed || left > time.Hour || left < 59*time.Minute {
		t.Errorf("enabling the policy left %s (armed %v), want about an hour", left, armed)
	}
	if got := fx.srv.AutoUnloadPolicy(); !got.Enabled || got.Idle != time.Hour {
		t.Errorf("AutoUnloadPolicy() = %+v", got)
	}

	// Disabling disarms it again.
	fx.srv.SetAutoUnload(AutoUnload{Enabled: false, Idle: time.Hour})
	if left, armed := fx.srv.IdleRemaining(); armed || left != 0 {
		t.Errorf("disabling the policy left a budget: %v armed=%v", left, armed)
	}

	// Shortening below the time already spent unloads the model, rather than
	// granting a fresh budget.
	fx.srv.SetAutoUnload(AutoUnload{Enabled: true, Idle: 50 * time.Millisecond})
	waitForState(t, fx.srv, StateInstalled)
	if proc := fx.spawner.lastProcess(); proc.alive() {
		t.Error("shortening the budget did not stop the process")
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

	// A budget already spent fires at once rather than never.
	fired.Store(0)
	tracker.mark(start, true, onExpire)
	tracker.setPolicy(AutoUnload{Enabled: true, Idle: 20 * time.Millisecond},
		start.Add(time.Hour), true, onExpire)
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
}
