package backend

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// This file covers the pre-dispatch half of the embedded readiness guarantee.
//
// The ensure-loaded transport gates on the wire, inside http.Client.Do — which
// protects every request but cannot protect a caller that has ALREADY armed a
// short budget. The one-shot service calls do exactly that: they create a
// serviceLLMTimeout context first and issue the request second, so a cold weight
// load (minutes) would be charged to a budget measured in minutes at best and
// the call would fail instead of waiting. These paths therefore gate BEFORE the
// context exists, which is what "no LLM request starts until the model is loaded
// and ready" means for them.

// installEmbeddedAsDefault puts the API into the state the gate is meant for:
// the local model installed AND selected as the default, so a service call
// resolves to it. It returns the loopback port the fake server listens on.
func installEmbeddedAsDefault(t *testing.T, f *FrontendAPI) int {
	t.Helper()
	_, port := modelsEndpoint(t)
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(port, 32768))
	installEmbeddedLLM(t, f, port)
	stubFreePortProbe(t, f)

	f.configMu.Lock()
	f.config.LLM.DefaultModel = embeddedCompositeID()
	f.configMu.Unlock()
	return port
}

// slowEmbeddedSpawn fakes the llama-server spawn and makes the load take d, so a
// test can compare the load against a request budget of a similar size.
//
// It MUST run before the supervisor is first built (tightenEmbeddedBudgets does
// that): embeddedBuild copies the spawn seam onto the Server once, so a spawnFn
// installed afterwards would never be seen.
func slowEmbeddedSpawn(t *testing.T, f *FrontendAPI, d time.Duration) {
	t.Helper()
	proc := newFakeEmbeddedProcess(4242)
	f.embedded.spawnFn = func(_ context.Context, _ embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		time.Sleep(d)
		return proc, nil
	}
}

// failingEmbeddedSpawn makes every load fail at the spawn. Same ordering rule as
// slowEmbeddedSpawn.
func failingEmbeddedSpawn(t *testing.T, f *FrontendAPI, err error) {
	t.Helper()
	f.embedded.spawnFn = func(context.Context, embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		return nil, err
	}
}

// TestActiveModelIsEmbedded pins the predicate the gate is keyed on. It must fire
// only for the backend-owned entry: a user's own provider that happens to be
// named "embedded" is not the local model, and gating on it would block requests
// behind a supervisor that has nothing to load.
func TestActiveModelIsEmbedded(t *testing.T) {
	t.Run("nothing installed", func(t *testing.T) {
		f, _, _ := newEmbeddedTestAPI(t)
		if f.activeModelIsEmbedded() {
			t.Error("activeModelIsEmbedded() = true with no install")
		}
	})

	t.Run("installed but another model is the default", func(t *testing.T) {
		f, _, _ := newEmbeddedTestAPI(t)
		installEmbeddedLLM(t, f, 4321)
		if f.activeModelIsEmbedded() {
			t.Error("activeModelIsEmbedded() = true while claude-3-opus is the default")
		}
	})

	t.Run("installed and selected", func(t *testing.T) {
		f, _, _ := newEmbeddedTestAPI(t)
		installEmbeddedAsDefault(t, f)
		if !f.activeModelIsEmbedded() {
			t.Error("activeModelIsEmbedded() = false with the embedded composite as the default")
		}
	})

	t.Run("installed, selected by bare name", func(t *testing.T) {
		f, _, _ := newEmbeddedTestAPI(t)
		installEmbeddedLLM(t, f, 4321)
		f.configMu.Lock()
		f.config.LLM.DefaultModel = config.EmbeddedLLMModelName
		f.configMu.Unlock()
		if !f.activeModelIsEmbedded() {
			t.Error("a bare model name must resolve to the same provider as the composite")
		}
	})

	t.Run("uninstalled", func(t *testing.T) {
		f, _, _ := newEmbeddedTestAPI(t)
		installEmbeddedAsDefault(t, f)
		f.configMu.Lock()
		f.config.EmbeddedLLM.Installed = false
		f.configMu.Unlock()
		if f.activeModelIsEmbedded() {
			t.Error("activeModelIsEmbedded() = true after the install was withdrawn")
		}
	})
}

// TestEnsureEmbeddedReadyForLLMRequestLoadsOnlyWhenSelected is the gate itself:
// a no-op for every other provider, a blocking load for the embedded one, and an
// error — not a request — when the load fails.
func TestEnsureEmbeddedReadyForLLMRequestLoadsOnlyWhenSelected(t *testing.T) {
	t.Run("another provider: no load, no block", func(t *testing.T) {
		f, _, _ := newEmbeddedTestAPI(t)
		installEmbeddedLLM(t, f, 4321)
		forbidEmbeddedSideEffects(t, f)

		if err := f.ensureEmbeddedReadyForLLMRequest(t.Context()); err != nil {
			t.Fatalf("the gate must be inert while another model is the default: %v", err)
		}
	})

	t.Run("embedded selected: the model is resident when the gate returns", func(t *testing.T) {
		f, _, _ := newEmbeddedTestAPI(t)
		installEmbeddedAsDefault(t, f)
		slowEmbeddedSpawn(t, f, 50*time.Millisecond)
		tightenEmbeddedBudgets(t, f)

		if err := f.ensureEmbeddedReadyForLLMRequest(t.Context()); err != nil {
			t.Fatalf("the gate: %v", err)
		}
		if status := f.GetEmbeddedLLMStatus(); !status.Loaded {
			t.Errorf("status = %+v, want loaded — the gate returned before the model could answer", status)
		}
	})

	t.Run("a failed load is reported instead of dispatching", func(t *testing.T) {
		f, _, _ := newEmbeddedTestAPI(t)
		installEmbeddedAsDefault(t, f)
		wantErr := errors.New("weights are gone")
		failingEmbeddedSpawn(t, f, wantErr)
		tightenEmbeddedBudgets(t, f)

		err := f.ensureEmbeddedReadyForLLMRequest(t.Context())
		if err == nil {
			t.Fatal("the gate succeeded although the load failed; the request would be " +
				"dispatched to a socket nothing is listening on")
		}
		if !errors.Is(err, wantErr) {
			t.Errorf("err = %v, want it to carry the load's cause %v", err, wantErr)
		}
	})
}

// TestOptimizePromptGatesBeforeTheServiceBudget is the end-to-end proof for the
// short-budget paths. The load takes LONGER than the service timeout, so the
// ordering is observable: arm the budget first and the call is dead on arrival,
// gate first and the call gets its whole budget.
func TestOptimizePromptGatesBeforeTheServiceBudget(t *testing.T) {
	const (
		loadTime      = 1200 * time.Millisecond
		serviceBudget = 1 * time.Second
	)
	f, _, mock := newEmbeddedTestAPI(t)
	installEmbeddedAsDefault(t, f)
	slowEmbeddedSpawn(t, f, loadTime)
	tightenEmbeddedBudgets(t, f)
	mock.optimizePromptRes = &core.OptimizePromptResult{OptimizedPrompt: "improved"}

	f.configMu.Lock()
	f.config.Timeouts.ServiceLLMRequestTimeout = int(serviceBudget / time.Second)
	f.configMu.Unlock()

	start := time.Now()
	if _, err := f.OptimizePrompt("make this prompt better"); err != nil {
		t.Fatalf("OptimizePrompt: %v", err)
	}
	elapsed := time.Since(start)

	if mock.optimizePromptCalls != 1 {
		t.Fatalf("builder calls = %d, want 1", mock.optimizePromptCalls)
	}
	if elapsed < loadTime {
		t.Errorf("the call returned in %v, before the %v load could have completed: "+
			"the request was dispatched without waiting", elapsed, loadTime)
	}
	if status := f.GetEmbeddedLLMStatus(); !status.Loaded {
		t.Errorf("status = %+v, want loaded before the request went out", status)
	}

	// The budget the request carries must be whole. Had it been armed before the
	// gate, the %v load would have spent it and the call would have started
	// already expired — which is the failure this prevents. Measured at call
	// time: the RPC cancels the context the moment it returns.
	budget := mock.lastOptimizePromptBudget()
	if !budget.live {
		t.Fatalf("the service context was already dead when the request was issued: "+
			"the %v load was charged to a %v budget", loadTime, serviceBudget)
	}
	if !budget.hasDeadline {
		t.Fatal("the service context carries no deadline — serviceLLMTimeout was not applied")
	}
	if budget.remaining < serviceBudget/2 {
		t.Errorf("remaining budget = %v, want ≈%v: the load ate into it", budget.remaining, serviceBudget)
	}
}

// TestSyncEmbeddedBuilderSeamFollowsTheInstallState pins the builder-level
// default — the one every PER-SESSION router falls back to. The session factory
// converts the live config directly and has no path to the supervisor, so this
// default is the only thing that can give a session router its ensure-loaded
// transport; losing it silently returns requests to a dead loopback socket.
func TestSyncEmbeddedBuilderSeamFollowsTheInstallState(t *testing.T) {
	f, _, mock := newEmbeddedTestAPI(t)

	f.syncEmbeddedBuilderSeam()
	if seam, sets := mock.lastEmbeddedSeam(); sets != 1 || seam.Loader != nil || seam.ProviderName != "" {
		t.Errorf("seam = %+v (sets=%d) with nothing installed; it must stay inert so a "+
			"user's own provider named %q is not hijacked", seam, sets, config.EmbeddedLLMProviderName)
	}

	installEmbeddedLLM(t, f, 4321)
	f.syncEmbeddedBuilderSeam()
	seam, sets := mock.lastEmbeddedSeam()
	if sets != 2 {
		t.Fatalf("the seam was pushed %d time(s), want 2", sets)
	}
	if seam.Loader == nil {
		t.Error("Loader = nil after an install: per-session routers would carry no transport")
	}
	if seam.ProviderName != config.EmbeddedLLMProviderName {
		t.Errorf("ProviderName = %q, want %q", seam.ProviderName, config.EmbeddedLLMProviderName)
	}

	// A removal must withdraw it, or a session router would keep guarding the
	// name after the user reclaims it for their own provider.
	f.configMu.Lock()
	f.config.EmbeddedLLM.Installed = false
	f.configMu.Unlock()
	f.syncEmbeddedBuilderSeam()
	if seam, _ := mock.lastEmbeddedSeam(); seam.Loader != nil {
		t.Errorf("seam = %+v survived the uninstall", seam)
	}
}

// TestRebuildAfterEmbeddedConfigChangePushesTheSeam covers the real call path:
// the startup restore, a completed install and a removal all funnel through the
// rebuild, which is where the builder default is refreshed.
func TestRebuildAfterEmbeddedConfigChangePushesTheSeam(t *testing.T) {
	f, _, mock := newEmbeddedTestAPI(t)
	installEmbeddedLLM(t, f, 4321)

	f.rebuildAfterEmbeddedConfigChange()

	seam, sets := mock.lastEmbeddedSeam()
	if sets == 0 {
		t.Fatal("the rebuild never pushed the builder-level seam")
	}
	if seam.Loader == nil {
		t.Errorf("seam = %+v, want a Loader", seam)
	}
}

// TestInstallServiceLLMGateIsNilSafe covers the zero-value API (desktop.NewApp
// constructs one so early RPC calls return errors instead of panicking) and a
// FrontendAPI with no Application at all — which is how every backend test
// builds one.
func TestInstallServiceLLMGateIsNilSafe(t *testing.T) {
	(&FrontendAPI{}).installServiceLLMGate()

	f, _, _ := newEmbeddedTestAPI(t)
	f.installServiceLLMGate()
}

// TestNewFrontendAPIInstallsTheServiceLLMGate is the wiring guard. The manager is
// built inside NewApplication, before any FrontendAPI exists, so the gate has
// exactly one place it can be installed — and a missing call is invisible at
// runtime: title generation simply keeps failing against a cold model. Source
// scans are already how this repo guards wiring that has no other observable
// (see core/embeddedllm's TestPackageSourcesNeverEmitBannedContext).
func TestNewFrontendAPIInstallsTheServiceLLMGate(t *testing.T) {
	source, err := os.ReadFile("frontend_api.go")
	if err != nil {
		t.Fatalf("reading frontend_api.go: %v", err)
	}
	if !strings.Contains(string(source), "f.installServiceLLMGate()") {
		t.Error("NewFrontendAPI no longer installs the service LLM gate: session " +
			"title generation would be issued against a cold embedded model and " +
			"spend its whole budget on the weight load")
	}
}

// TestEmbeddedLoaderRefusesWhileAnEmbeddedOperationRuns is the request-path half
// of the install gate — the invariant TestLoadEmbeddedLLMRefusesWhileAnInstallRuns
// documents for the RPC, pinned here for the transport.
//
// The gate RPCs (Remove, Load, Probe) were never the whole surface: three paths
// reached Server.Load without consulting it — this file's own service-LLM gate,
// the router's Loader seam and the ensure-loaded transport. All three end at
// embeddedLoaderRef.Load, so refusing there closes them at the seam. Without it a
// chat message arriving during a repair/reinstall cold-loads the OLD install, and
// the install's promote step then renames and deletes the tree the child is
// executing from (its working directory IS that tree, which on Windows makes the
// rename of a live process's executable ERROR_ACCESS_DENIED unconditionally).
//
// A removal is covered too: it holds the same gate, and loading weights that are
// being deleted is the same hazard with a wider window.
func TestEmbeddedLoaderRefusesWhileAnEmbeddedOperationRuns(t *testing.T) {
	for _, op := range []embeddedOpKind{embeddedOpInstall, embeddedOpRemove} {
		t.Run(string(op), func(t *testing.T) {
			f, _, _ := newEmbeddedTestAPI(t)
			installEmbeddedAsDefault(t, f)
			f.embedded.spawnFn = func(context.Context, embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
				t.Error("the request-path loader spawned a server while an operation held the gate")
				return nil, errors.New("spawn forbidden")
			}

			if claimed, holder := f.beginEmbeddedOperation(op); !claimed {
				t.Fatalf("the gate refused %q on an idle subsystem (held by %q)", op, holder)
			}
			defer f.endEmbeddedOperation()

			err := (embeddedLoaderRef{f: f}).Load(t.Context())
			if err == nil {
				t.Fatal("the request-path loader started a load while an operation held the gate: " +
					"it would cold-load the install being replaced")
			}
			if !strings.Contains(err.Error(), string(op)+" is running") {
				t.Errorf("the refusal = %q, want it to name the in-flight %s", err, op)
			}

			// The service-LLM gate surfaces the same refusal to its caller
			// rather than dispatching a request at a model that must not load.
			gateErr := f.ensureEmbeddedReadyForLLMRequest(t.Context())
			if gateErr == nil {
				t.Fatal("ensureEmbeddedReadyForLLMRequest succeeded while an operation held the gate")
			}
			if !strings.Contains(gateErr.Error(), string(op)+" is running") {
				t.Errorf("the service gate = %q, want it to carry the refusal", gateErr)
			}
		})
	}
}
