package backend

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// This file covers the embedded OPERATION GATE and the budgets around it.
//
// The gate is the single-run invariant of the whole subsystem, not of the
// install alone: the core Installer is a plain struct with no internal
// synchronization, and Install and Remove write and delete the SAME
// staging/runtime/model trees. Before the gate was shared, a Remove claimed
// nothing, so an Install clicked during a multi-gigabyte removal raced the
// deletion — a corrupted half-staged tree, or an install completing after
// ApplyRemoved and silently re-adding what the user had just removed.
//
// The gate is also the seam the REQUEST paths are refused at
// (embeddedLoaderRef.Load): the service-LLM gate, the router's Loader default
// and the ensure-loaded transport all converge there, so one refusal closes all
// three. Without it a chat message arriving during a repair/reinstall cold-loads
// the OLD install, and the install's promote step then renames and deletes the
// tree the child is executing from.

// installerOf returns the constructed installer, building the subsystem if
// needed. Test-only accessor (the sibling of supervisorOf); it exists so a test
// can substitute the Installer's injectable fields, which is how a removal can
// be made to block at a known point.
func installerOf(t *testing.T, f *FrontendAPI) *embeddedllm.Installer {
	t.Helper()
	_, installer, err := f.embeddedBuild()
	if err != nil {
		t.Fatalf("embeddedBuild: %v", err)
	}
	return installer
}

// setStopBudget tightens the stop budget the stop-shaped paths hand core, so the
// deadline path is observable without waiting the production 30s.
func setStopBudget(t *testing.T, f *FrontendAPI, d time.Duration) {
	t.Helper()
	f.embedded.mu.Lock()
	f.embedded.stopBudget = d
	f.embedded.mu.Unlock()
}

// waitForIdleGate polls until no embedded operation holds the gate.
func waitForIdleGate(t *testing.T, f *FrontendAPI, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.embeddedBusyOperation() == embeddedOpIdle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; the operation gate is still held by %q",
		what, f.embeddedBusyOperation())
}

// TestEmbeddedStopBudgetResolvesToTheDocumentedCeiling pins the budget every
// stop-shaped path hands core: the documented 30s in production, the override in
// a test. The application context Wails gives the backend is never cancelled, so
// this resolution is the only thing standing between a quit and a wait measured
// in cold-load minutes.
func TestEmbeddedStopBudgetResolvesToTheDocumentedCeiling(t *testing.T) {
	if embeddedStopTimeout != 30*time.Second {
		t.Errorf("embeddedStopTimeout = %v, want the documented 30s", embeddedStopTimeout)
	}
	f, _, _ := newEmbeddedTestAPI(t)
	if got := f.embeddedStopBudget(); got != embeddedStopTimeout {
		t.Errorf("embeddedStopBudget() = %v, want the production %v", got, embeddedStopTimeout)
	}
	setStopBudget(t, f, 5*time.Second)
	if got := f.embeddedStopBudget(); got != 5*time.Second {
		t.Errorf("embeddedStopBudget() = %v, want the 5s override", got)
	}
}

// TestEmbeddedOperationGateIsSharedByInstallAndRemove pins the generalized gate:
// a REMOVAL in flight refuses every other gated entry point, and each refusal
// names the operation that is actually running.
//
// The narrow `embeddedInstalling()` predicate must stay false throughout: it is
// what EmbeddedLLMStatus.Installing reports, and the frontend renders that field
// as the per-component INSTALL progress bar — a removal must not light one.
func TestEmbeddedOperationGateIsSharedByInstallAndRemove(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(4321, 32768))
	f.Lifecycle().InitEmbeddedLLM()
	f.embedded.spawnFn = func(context.Context, embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		t.Error("a server was spawned while a removal held the gate")
		return nil, errors.New("spawn forbidden")
	}
	f.embedded.deviceProbeFn = func(context.Context, string, *slog.Logger) (embeddedllm.MemoryTopology, bool) {
		t.Error("the device probe ran while a removal held the gate")
		return embeddedllm.MemoryTopology{}, false
	}

	claimed, holder := f.beginEmbeddedOperation(embeddedOpRemove)
	if !claimed {
		t.Fatalf("the gate refused a removal on an idle subsystem (held by %q)", holder)
	}
	defer f.endEmbeddedOperation()

	if f.embeddedInstalling() {
		t.Error("embeddedInstalling() = true during a REMOVAL: the frontend would render " +
			"an install progress bar with no install progress to show")
	}
	if f.GetEmbeddedLLMStatus().Installing {
		t.Error("status.Installing = true during a removal")
	}
	if got := f.embeddedBusyOperation(); got != embeddedOpRemove {
		t.Errorf("embeddedBusyOperation() = %q, want %q", got, embeddedOpRemove)
	}

	// A second removal, an install, a load, an unload, a probe and the
	// request-path loader are all refused.
	if claimed, _ := f.beginEmbeddedOperation(embeddedOpRemove); claimed {
		t.Error("a second removal claimed the gate while one was in flight")
	}
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"install", f.InstallEmbeddedLLM},
		{"load", f.LoadEmbeddedLLM},
		{"unload", f.UnloadEmbeddedLLM},
		{"probe", func() error { _, err := f.ProbeEmbeddedLLMDevices(); return err }},
		{"request-path load", func() error {
			return (embeddedLoaderRef{f: f}).Load(t.Context())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatalf("%s succeeded while a removal held the gate", tc.name)
			}
			// The refusal names the operation that is ACTUALLY in flight — not a
			// hardcoded "install" — and says what to do about it.
			if !strings.Contains(err.Error(), string(embeddedOpRemove)) {
				t.Errorf("%s refusal = %q, want it to name the in-flight removal", tc.name, err)
			}
			if !strings.Contains(err.Error(), "wait for it to finish") {
				t.Errorf("%s refusal = %q, want it to say what to do instead", tc.name, err)
			}
		})
	}
}

// TestInstallPreflightPanicDoesNotLeakTheGate pins the pre-start panic guard:
// a panic inside the SYNCHRONOUS gates (the hardware probe, the memory budget)
// must release the operation gate through refuseEmbeddedInstall. The
// background run's release defer does not exist until its goroutine starts, so
// without the guard a panicking probe would leave the gate claimed for the
// process lifetime and every embedded RPC refusing until a restart.
func TestInstallPreflightPanicDoesNotLeakTheGate(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(4321, 32768))
	f.Lifecycle().InitEmbeddedLLM()
	f.embedded.probeFn = func(context.Context, *slog.Logger) (embeddedllm.Hardware, error) {
		panic("the probe exploded")
	}

	err := f.InstallEmbeddedLLM()
	if err == nil {
		t.Fatal("InstallEmbeddedLLM succeeded although its probe panicked")
	}
	if !strings.Contains(err.Error(), "panicked before it started") {
		t.Errorf("refusal = %q, want it to name the pre-start panic", err)
	}
	if op := f.embeddedBusyOperation(); op != embeddedOpIdle {
		t.Errorf("the gate is still held by %q after the preflight panic", op)
	}
	// The very next install must be able to claim the gate again.
	if claimed, holder := f.beginEmbeddedOperation(embeddedOpInstall); !claimed {
		t.Fatalf("a fresh install could not claim the gate after the panic (held by %q)", holder)
	}
	f.endEmbeddedOperation()
}

// TestRemoveEmbeddedLLMHoldsTheGateForItsWholeDuration is the TOCTOU itself: the
// gate must be claimed for the DURATION of installer.Remove — not merely checked
// once at entry — so an Install that arrives mid-removal is refused instead of
// racing the deletion.
func TestRemoveEmbeddedLLMHoldsTheGateForItsWholeDuration(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(4321, 32768))
	f.Lifecycle().InitEmbeddedLLM()

	release := make(chan struct{})
	entered := make(chan struct{})
	installer := installerOf(t, f)
	// Remove calls Stop first, unconditionally when it is wired — which
	// embeddedBuild always does. Blocking here stands in for the real cost of a
	// removal: a supervisor stop plus deleting several gigabytes.
	installer.Stop = func(context.Context) error {
		close(entered)
		<-release
		return nil
	}

	done := make(chan error, 1)
	go func() { done <- f.RemoveEmbeddedLLM() }()
	<-entered

	if got := f.embeddedBusyOperation(); got != embeddedOpRemove {
		t.Errorf("the gate is held by %q mid-removal, want %q", got, embeddedOpRemove)
	}
	err := f.InstallEmbeddedLLM()
	if err == nil {
		t.Fatal("InstallEmbeddedLLM was accepted while a removal was deleting the trees")
	}
	if !strings.Contains(err.Error(), "removal is running") {
		t.Errorf("the install refusal = %q, want it to name the in-flight removal", err)
	}
	// "already running" is reserved for the operation the caller actually asked
	// for. An install refused by a REMOVAL must not claim another install is
	// under way — there is none — and the string is rendered verbatim in the
	// Settings error line, where a wrong noun reads as a bug.
	if strings.Contains(err.Error(), "already") {
		t.Errorf("the install refusal = %q says something is ALREADY running; the "+
			"gate is held by a removal, not by the install that was refused", err)
	}
	if err := f.LoadEmbeddedLLM(); err == nil || !strings.Contains(err.Error(), "removal is running") {
		t.Errorf("LoadEmbeddedLLM mid-removal = %v, want a refusal naming the removal", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("RemoveEmbeddedLLM: %v", err)
	}
	waitForIdleGate(t, f, "the removal to release the gate")
	if f.GetEmbeddedLLMStatus().Installing {
		t.Error("status.Installing = true after the removal finished")
	}
}

// TestRunEmbeddedInstallReleasesTheGateOnEveryPath pins the single deferred
// release. Before it was a defer the release sat on three separate explicit
// paths — the panic one only inside `if r := recover() != nil` — so any new
// return leaked the gate for the whole process lifetime: Install, Remove, Load,
// Unload, Probe and the request-path loader would all refuse until a restart.
func TestRunEmbeddedInstallReleasesTheGateOnEveryPath(t *testing.T) {
	hw := embeddedllm.Hardware{
		Platform: "darwin-arm64", Arch: "arm64", RAMGiB: 32,
		Backend: embeddedllm.BackendMetal,
	}
	report := &embeddedllm.InstallReport{Manifest: embeddedTestManifest(4321, 32768)}
	installRun := func(context.Context, *embeddedllm.Installer,
		embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error) {
		return report, nil
	}

	for _, tc := range []struct {
		name string
		run  func(context.Context, *embeddedllm.Installer,
			embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error)
	}{
		{"success", installRun},
		{"failure", func(context.Context, *embeddedllm.Installer,
			embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error) {
			return nil, errors.New("the download failed")
		}},
		{"a nil report", func(context.Context, *embeddedllm.Installer,
			embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error) {
			return nil, nil
		}},
		{"panic", func(context.Context, *embeddedllm.Installer,
			embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error) {
			panic("the install exploded")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, _ := newEmbeddedTestAPI(t)
			writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(4321, 32768))
			f.embedded.probeFn = func(context.Context, *slog.Logger) (embeddedllm.Hardware, error) {
				return hw, nil
			}
			f.embedded.installFn = tc.run

			server := supervisorOf(t, f)
			if claimed, holder := f.beginEmbeddedOperation(embeddedOpInstall); !claimed {
				t.Fatalf("the gate refused the install it was about to run (held by %q)", holder)
			}
			// Synchronous: runEmbeddedInstall is exactly what
			// InstallEmbeddedLLM's goroutine body runs, so calling it directly
			// observes the release without a poll.
			f.runEmbeddedInstall(t.Context(), server, installerOf(t, f), hw)

			if got := f.embeddedBusyOperation(); got != embeddedOpIdle {
				t.Errorf("the gate is still held by %q after the run finished", got)
			}
			if f.GetEmbeddedLLMStatus().Installing {
				t.Error("status.Installing = true after the run finished")
			}
		})
	}
}

// TestEmbeddedBoundedStopErrIsActionable pins the translation that turns a
// bounded stop's deadline into something an operator can act on. A bare
// "context deadline exceeded" from a Remove or Unload click says nothing about
// the load that is holding the supervisor's gate.
func TestEmbeddedBoundedStopErrIsActionable(t *testing.T) {
	const budget = 30 * time.Second

	t.Run("a deadline names the wait and keeps the cause", func(t *testing.T) {
		err := embeddedBoundedStopErr("the local model unload", context.DeadlineExceeded, budget)
		msg := err.Error()
		for _, want := range []string{
			"the local model unload", "30s", "load is still in progress", "try again",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("the message %q does not mention %q", msg, want)
			}
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want it to keep wrapping context.DeadlineExceeded", err)
		}
	})

	t.Run("a cancellation is left alone", func(t *testing.T) {
		if got := embeddedBoundedStopErr("the local model removal", context.Canceled, budget); !errors.Is(got, context.Canceled) {
			t.Errorf("err = %v, want the untranslated context.Canceled", got)
		}
	})

	t.Run("any other failure is passed through verbatim", func(t *testing.T) {
		want := errors.New("the tree is read-only")
		got := embeddedBoundedStopErr("the local model removal", want, budget)
		if !errors.Is(got, want) {
			t.Errorf("err = %v, want it to carry the untouched cause %v", got, want)
		}
		if strings.Contains(got.Error(), "load is still in progress") {
			t.Errorf("err = %q, want a non-deadline failure left untranslated", got)
		}
	})

	t.Run("a joined deadline is recognised too", func(t *testing.T) {
		joined := errors.Join(errors.New("stopping the inference server before removal"),
			context.DeadlineExceeded)
		got := embeddedBoundedStopErr("the local model removal", joined, budget)
		if !strings.Contains(got.Error(), "load is still in progress") {
			t.Errorf("err = %v, want a joined deadline to be translated as well", got)
		}
	})
}

// blockTheSupervisorGate starts a load whose spawn blocks, so the supervisor's
// single-instance gate stays held until release is closed. That is the exact
// shape of the production hazard: Stop, Unload and Remove all acquire the gate
// first, and a cold load holds it for up to DefaultReadyTimeout (15 min).
//
// It returns a func that ends the load. The spawnFn MUST be installed before the
// supervisor is first built (embeddedBuild copies the seam onto the Server once),
// which tightenEmbeddedBudgets does — hence its position here.
func blockTheSupervisorGate(t *testing.T, f *FrontendAPI) func() {
	t.Helper()
	release := make(chan struct{})
	entered := make(chan struct{})
	f.embedded.spawnFn = func(context.Context, embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		close(entered)
		<-release
		return nil, errors.New("the test load was released")
	}
	tightenEmbeddedBudgets(t, f)
	stubFreePortProbe(t, f)

	loadDone := make(chan error, 1)
	go func() { loadDone <- f.LoadEmbeddedLLM() }()
	<-entered

	return func() {
		close(release)
		<-loadDone
	}
}

// TestStopShapedPathsAreBoundedByTheStopBudget is the hang the budget exists
// for. The application context Wails hands the backend is never cancelled, so an
// unbounded stop waits for the whole cold load: a blocking RPC (and its UI
// spinner) for minutes, and a QUIT that does not complete. Each stop-shaped path
// must instead return at its own budget.
//
// The assertion is the BOUND, not the error kind: core's Stop takes a force path
// when the gate cannot be acquired in time, so the call may also come back
// quickly and successfully. Both outcomes satisfy the contract — a bounded ctx
// terminates the child instead of erroring out; what must never happen is a wait
// measured in minutes. The bound itself is DERIVED from the budgets this fixture
// tightens rather than hardcoded — see the composition note at its definition —
// so it moves with them and cannot sit loose enough to accept an untightened
// path.
//
// The table holds the three RPC- and shutdown-shaped paths. The FOURTH bounded
// stop — a background install's step 0 — cannot join it: it runs on a goroutine,
// and its budget reaches core as Installer.StopTimeout rather than as a wrapped
// context, because the install's own context must stay deadline-free for the
// download. It is pinned by TestInstallStepZeroStopCarriesTheStopBudget below and,
// behaviourally, by core's TestInstallStopsTheServerFirst.
func TestStopShapedPathsAreBoundedByTheStopBudget(t *testing.T) {
	const testBudget = 250 * time.Millisecond
	// forceHeadRoom is the term core's forceUnload adds on top of
	// stopTimeout + killWait when it arms its own detached budget
	// (`s.stopTimeout()+s.killWait()+time.Second`).
	const forceHeadRoom = time.Second
	// slack is a documented multiple for scheduling on a loaded CI machine, not
	// cover for a slow path: the composition note at the bound below records what
	// the elapsed time actually is (~testBudget) and why nothing else is
	// reachable, which leaves the derived bound ~28× of margin.
	const slack = 4

	for _, tc := range []struct {
		name string
		call func(*FrontendAPI) error
	}{
		{"UnloadEmbeddedLLM", func(f *FrontendAPI) error { return f.UnloadEmbeddedLLM() }},
		{"RemoveEmbeddedLLM", func(f *FrontendAPI) error { return f.RemoveEmbeddedLLM() }},
		{"the shutdown stop", func(f *FrontendAPI) error {
			return f.Lifecycle().StopEmbeddedLLM(context.Background())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, _ := newEmbeddedTestAPI(t)
			writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(4321, 32768))
			endLoad := blockTheSupervisorGate(t, f)
			defer endLoad()
			setStopBudget(t, f, testBudget)

			// The threshold is DERIVED from this fixture's own budgets rather than
			// hardcoded, so it cannot drift away from them and a future change that
			// lets a stop-shaped path run at production defaults — a 30s gate budget
			// (embeddedStopTimeout) or a 16s force budget (DefaultStopTimeout +
			// defaultKillWait + 1s) — fails here instead of hiding under a literal
			// (the previous 10s was 40× testBudget and would have accepted a 9.9s
			// stall). It is composed of:
			//
			//   slack × (the gate budget this call hands core
			//            + the supervisor's tightened graceful window
			//            + core's force-budget head-room term)
			//   = 4 × (250ms + 500ms + 1s) = 7s
			//
			// Both budgets are READ from the fixture (embeddedStopBudget and the
			// supervisor's StopTimeout, which tightenEmbeddedBudgets sets) so a
			// change to either moves the bound with it.
			//
			// The gate wait is the ONLY wait these paths can perform in this
			// scenario: the load's spawn is still blocked, so forceUnload finds no
			// run handle and returns at once without arming its own budget, and on
			// the gated path the fake child exits on the first signal, so terminate
			// costs at most one StopTimeout and core's post-kill wait
			// (defaultKillWait, unexported and therefore not readable here) is
			// unreachable. A fixture that staged an UNKILLABLE child would have to
			// add that wait to this bound.
			gateBudget := f.embeddedStopBudget()
			stopWindow := supervisorOf(t, f).StopTimeout
			bound := slack * (gateBudget + stopWindow + forceHeadRoom)

			start := time.Now()
			err := tc.call(f)
			elapsed := time.Since(start)

			if elapsed > bound {
				t.Errorf("%s took %v, over the %v bound derived from this fixture's budgets "+
					"(%d × (gate %v + supervisor stop window %v + force head-room %v)): the stop was "+
					"not bounded, so a quit or a blocking RPC would hang for the length of a cold "+
					"weight load",
					tc.name, elapsed, bound, slack, gateBudget, stopWindow, forceHeadRoom)
			}
			if err != nil && errors.Is(err, context.DeadlineExceeded) &&
				!strings.Contains(err.Error(), "a load is still in progress") {
				t.Errorf("%s = %v: a deadline must reach the caller as an actionable error "+
					"naming the load that holds the gate, not as a bare context.DeadlineExceeded",
					tc.name, err)
			}
		})
	}
}

// TestInstallStepZeroStopCarriesTheStopBudget pins the FOURTH bounded stop path:
// the step-0 stop a background install performs before its first numbered step.
// It is the one stop nobody else bounds — the install runs on the never-deadlined
// application context, so the budget has to travel to core as
// Installer.StopTimeout instead of as a wrapped context. Without it a Load that
// claimed the supervisor's single-instance gate first parks the install in step 0
// for the length of that load (up to DefaultReadyTimeout, 15 min), and step 0
// precedes plan — so it precedes the first install_progress event — while the
// operation gate makes every other embedded call refuse. The operator's view is a
// bar at 0% that never moves.
//
// The behavioural half (that a bounded step 0 reaches core's force path and takes
// a resident child down instead of waiting the load out) is pinned in core by
// TestInstallStopsTheServerFirst; this pins the wiring and the translation.
func TestInstallStepZeroStopCarriesTheStopBudget(t *testing.T) {
	const testBudget = 250 * time.Millisecond

	f, _, _ := newEmbeddedTestAPI(t)
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(4321, 32768))
	// Tightened BEFORE the first build: embeddedBuild resolves the budget onto the
	// shared installer, and runEmbeddedInstall re-resolves it onto its per-run copy.
	setStopBudget(t, f, testBudget)

	installer := installerOf(t, f)
	if got := installer.StopTimeout; got != testBudget {
		t.Errorf("installer.StopTimeout = %v, want the stop budget %v: the install's step-0 "+
			"stop would run on the never-deadlined application context and wait out an "+
			"in-flight cold load", got, testBudget)
	}
	installer.Stop = func(context.Context) error { return context.DeadlineExceeded }

	hw := embeddedllm.Hardware{
		Platform: "darwin-arm64", Arch: "arm64", RAMGiB: 32,
		Backend: embeddedllm.BackendMetal,
	}
	var (
		runBudget time.Duration
		stepZero  error
	)
	f.embedded.installFn = func(_ context.Context, in *embeddedllm.Installer,
		_ embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error) {
		runBudget = in.StopTimeout
		// core's Install performs step 0 through this seam; the stub makes the same
		// call, so what is exercised is the wrapper this run wired onto its copy.
		stepZero = in.Stop(context.Background())
		return nil, errors.New("the test stopped at step 0")
	}

	server := supervisorOf(t, f)
	if claimed, holder := f.beginEmbeddedOperation(embeddedOpInstall); !claimed {
		t.Fatalf("the gate refused the install it was about to run (held by %q)", holder)
	}
	f.runEmbeddedInstall(t.Context(), server, installer, hw)

	if runBudget != testBudget {
		t.Errorf("the per-run installer carries StopTimeout = %v, want %v", runBudget, testBudget)
	}
	if !errors.Is(stepZero, context.DeadlineExceeded) {
		t.Fatalf("step 0 = %v, want it to keep wrapping context.DeadlineExceeded", stepZero)
	}
	if !strings.Contains(stepZero.Error(), "a load is still in progress") {
		t.Errorf("step 0 = %v, want the translated stop error: a step-0 deadline has one "+
			"likely cause the operator can act on, and a bare context.DeadlineExceeded in an "+
			"install-failure toast does not name it", stepZero)
	}
	// The SHARED seam stays untranslated. RemoveEmbeddedLLM wraps the error its own
	// installer.Remove returns, so translating here too would name one removal twice.
	if err := installer.Stop(context.Background()); err == nil ||
		strings.Contains(err.Error(), "a load is still in progress") {
		t.Errorf("the shared installer's Stop = %v, want the untranslated core error", err)
	}
	if got := f.embeddedBusyOperation(); got != embeddedOpIdle {
		t.Errorf("the gate is still held by %q after the run finished", got)
	}
}

// TestStopBudgetDocsScopeTheNoChildClaim keeps the stop-budget doc sites —
// embeddedStopTimeout, RemoveEmbeddedLLM, UnloadEmbeddedLLM and the install's
// step-0 seam (InstallEmbeddedLLM, runEmbeddedInstall and embeddedBuild's
// Installer.StopTimeout wiring) — honest about WHICH error is conditional on no
// child having spawned. These are load-bearing
// specs of the RPC contract, and the absolute form ("an actionable error comes
// back only when no child had spawned yet") is false, contradicted by three
// reachable paths:
//
//   - a stop that did NOT take is reported by core's terminate on BOTH the gated
//     and the force path ("did not exit within … of being killed") — precisely the
//     uninterruptible-syscall case defaultKillWait exists for, pinned by
//     TestUnloadReportsAStopThatDidNotTake and
//     TestForceUnloadReportsAStopThatDidNotTake in core;
//   - UnloadEmbeddedLLM's doc contradicted ITSELF with that absolute claim two
//     sentences after stating that such a stop is reported as an error;
//   - RemoveEmbeddedLLM's post-stop work returns errors.Join over the three tree
//     deletions plus Sink.ApplyRemoved, so a read-only or busy tree yields an
//     actionable error with no child involved at all.
//
// Only the TRANSLATED "a load is still in progress" message — the one
// embeddedBoundedStopErr produces, and it fires solely on
// errors.Is(err, context.DeadlineExceeded) — is conditional on the no-child case,
// which is how AGENTS.md scopes it. Both patterns are constructed rather than
// written out, so this guard does not plant the literals it scans for.
//
// It also pins the ENUMERATION in embeddedStopTimeout's own doc. There are four
// bounded stop paths, and the fourth — a background install's step-0 stop — is the
// easy one to lose: it hands core the budget as a field (Installer.StopTimeout)
// rather than as a wrapped context, so it does not show up in a grep for
// context.WithTimeout, and it runs on a goroutine whose context is deliberately
// deadline-free for the download. An enumeration that names only three reads as a
// claim that the install stop is unbounded, which is the hang this budget exists
// to prevent.
func TestStopBudgetDocsScopeTheNoChildClaim(t *testing.T) {
	t.Parallel()

	claim := "only when no child had " + "spawned yet"
	absolute := "an " + "actionable error comes back " + claim
	// The marker the three scoped sites carry: the claim is explicitly about the
	// translated message and not about every error the call can return. A reworded
	// doc must keep an equivalent explicit scoping marker.
	scoping := "TRANSLATED"

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	scanned := 0
	// Captured from the scan for the enumeration guard below, so the file is read
	// once and the guard cannot drift onto a different copy of it.
	var apiDocs string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		scanned++
		content := normalizedDocText(string(source))
		if name == "frontend_api_embedded.go" {
			apiDocs = content
		}

		if strings.Contains(content, absolute) {
			t.Errorf("%s states %q absolutely; that is true only of the translated "+
				"%q error embeddedBoundedStopErr produces, not of a stop that did not "+
				"take or of RemoveEmbeddedLLM's tree deletions and config save",
				name, absolute, "a load is still in progress")
		}
		claims := strings.Count(content, claim)
		if claims == 0 {
			continue
		}
		if markers := strings.Count(content, scoping); markers < claims {
			t.Errorf("%s makes the %q claim %d time(s) but scopes it to the %s message "+
				"only %d time(s): every occurrence must say it is the translated error that "+
				"is conditional, or it reads as an absolute promise the other error paths break",
				name, claim, claims, scoping, markers)
		}
	}

	if scanned == 0 {
		t.Fatal("no package sources were scanned; the guard is vacuous")
	}

	// The enumeration guard the doc above describes: embeddedStopTimeout's own doc
	// must name EVERY stop path the budget bounds. The fourth is the one a grep for
	// context.WithTimeout does not find, because it reaches core as a field.
	const (
		docStart = "embeddedStopTimeout bounds every supervisor stop"
		docEnd   = "runtime_error codes of this subsystem"
	)
	start := strings.Index(apiDocs, docStart)
	if start < 0 {
		t.Fatalf("frontend_api_embedded.go no longer opens embeddedStopTimeout's doc with %q; "+
			"move this guard with the rewording", docStart)
	}
	budgetDoc := apiDocs[start:]
	if end := strings.Index(budgetDoc, docEnd); end >= 0 {
		budgetDoc = budgetDoc[:end]
	}
	for _, want := range []string{
		"the shutdown stop",
		"UnloadEmbeddedLLM",
		"RemoveEmbeddedLLM",
		"step-0 stop",
		"Installer.StopTimeout",
	} {
		if !strings.Contains(budgetDoc, want) {
			t.Errorf("embeddedStopTimeout's doc does not enumerate %q: it must name every "+
				"stop path this budget bounds, and an enumeration of three reads as a claim "+
				"that a background install's step-0 stop is unbounded — the hang the budget "+
				"exists to prevent", want)
		}
	}
}

// normalizedDocText collapses a source file's line structure so a doc claim that
// wraps across commented lines can be matched as one sentence: comment markers
// become spaces and every whitespace run collapses to a single space.
func normalizedDocText(source string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(source, "//", " ")), " ")
}

// TestStopEmbeddedLLMIsANoOpWithoutASupervisor keeps the documented degenerate
// shapes: nothing was ever constructed, so nothing can be running, and a nil
// context is tolerated because Shutdown may be handed a torn-down one.
func TestStopEmbeddedLLMIsANoOpWithoutASupervisor(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.stopEmbeddedLLM(context.Background()); err != nil {
		t.Errorf("stopEmbeddedLLM on a zero API = %v, want nil", err)
	}
	if err := f.stopEmbeddedLLM(nil); err != nil { //nolint:staticcheck // exercising the nil-context guard
		t.Errorf("stopEmbeddedLLM(nil) = %v, want nil", err)
	}
}

// TestEmbeddedBusyRefusalWordingNamesTheOperationThatWasAskedFor pins the
// wording rule every gated entry point depends on. The gate is shared by two
// different operations, so the holder is not necessarily what the caller asked
// for: "already running" is true only when it is. An install refused by an
// in-flight removal that claimed another install were already running would
// name an operation that does not exist — verbatim, in the Settings error line.
func TestEmbeddedBusyRefusalWordingNamesTheOperationThatWasAskedFor(t *testing.T) {
	for _, tc := range []struct {
		name       string
		holder     embeddedOpKind
		wanted     embeddedOpKind
		refused    string
		want       string
		wantAbsent string
	}{
		{
			name:    "a second install",
			holder:  embeddedOpInstall,
			wanted:  embeddedOpInstall,
			refused: "starting an install",
			want:    "an embedded LLM install is already running; wait for it to finish before starting an install",
		},
		{
			name:       "an install refused by a removal",
			holder:     embeddedOpRemove,
			wanted:     embeddedOpInstall,
			refused:    "starting an install",
			want:       "an embedded LLM removal is running; wait for it to finish before starting an install",
			wantAbsent: "already",
		},
		{
			name:    "a second removal",
			holder:  embeddedOpRemove,
			wanted:  embeddedOpRemove,
			refused: "removing the model",
			want:    "an embedded LLM removal is already running; wait for it to finish before removing the model",
		},
		{
			name:       "a removal refused by an install",
			holder:     embeddedOpInstall,
			wanted:     embeddedOpRemove,
			refused:    "removing the model",
			want:       "an embedded LLM install is running; wait for it to finish before removing the model",
			wantAbsent: "already",
		},
		{
			// A load and a probe are not gate-holding operations, so they pass
			// embeddedOpIdle and can never read as "already running".
			name:       "a load refused by an install",
			holder:     embeddedOpInstall,
			wanted:     embeddedOpIdle,
			refused:    "loading the model",
			want:       "an embedded LLM install is running; wait for it to finish before loading the model",
			wantAbsent: "already",
		},
		{
			name:       "a probe refused by a removal",
			holder:     embeddedOpRemove,
			wanted:     embeddedOpIdle,
			refused:    "probing the devices",
			want:       "an embedded LLM removal is running; wait for it to finish before probing the devices",
			wantAbsent: "already",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := embeddedBusyRefusal(tc.holder, tc.wanted, tc.refused).Error()
			if got != tc.want {
				t.Errorf("embeddedBusyRefusal(%q, %q, %q)\n got = %q\nwant = %q",
					tc.holder, tc.wanted, tc.refused, got, tc.want)
			}
			if tc.wantAbsent != "" && strings.Contains(got, tc.wantAbsent) {
				t.Errorf("the refusal %q must not say %q", got, tc.wantAbsent)
			}
		})
	}
}
