package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
		// A bare model name can no longer be a configured default (config
		// load rejects it, ADR-122 mandatory-composite rule); a hand-built
		// in-memory state with one must NOT resolve to the embedded provider.
		f.config.LLM.DefaultModel = config.EmbeddedLLMModelName
		f.configMu.Unlock()
		if f.activeModelIsEmbedded() {
			t.Error("a bare model name must not resolve to the embedded provider (mandatory-composite rule)")
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

// TestServiceEmbeddedGateWaitsForAgentIdle pins the cache half of the service
// gate. The local server runs one slot, so ANY service request issued while an
// agent task is running evicts the prompt cache that task's next step would
// have reused — on a 40-50K-token agent prompt that eviction costs a fresh
// multi-minute prefill. The gate therefore defers service calls until no
// session carries live background work, within the caller's budget: an expired
// interactive budget is an actionable refusal, not a cache-evicting request.
func TestServiceEmbeddedGateWaitsForAgentIdle(t *testing.T) {
	t.Run("no agent running: the gate proceeds to readiness", func(t *testing.T) {
		f, _, _ := newEmbeddedTestAPI(t)
		installEmbeddedAsDefault(t, f)
		stubFreePortProbe(t, f)
		f.embedded.spawnFn = func(context.Context, embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
			return newFakeEmbeddedProcess(4242), nil
		}
		tightenEmbeddedBudgets(t, f)

		if err := f.serviceEmbeddedGate(t.Context(), 50*time.Millisecond); err != nil {
			t.Fatalf("serviceEmbeddedGate with no active task: %v", err)
		}
	})

	t.Run("an active task defers the call; an expired budget is an actionable refusal", func(t *testing.T) {
		f, _, _ := newEmbeddedTestAPI(t)
		installEmbeddedAsDefault(t, f)
		forbidEmbeddedSideEffects(t, f)
		f.activeSessionCount = func() int { return 1 }

		err := f.serviceEmbeddedGate(t.Context(), 50*time.Millisecond)
		if err == nil {
			t.Fatal("the gate let a service request through while an agent task is running: " +
				"it would evict that task's prompt cache")
		}
		if !strings.Contains(err.Error(), "busy with an active task") {
			t.Errorf("the refusal = %q, want it to name the busy embedded model", err)
		}
	})

	t.Run("another model as the default: the wait never engages", func(t *testing.T) {
		f, _, _ := newEmbeddedTestAPI(t)
		installEmbeddedLLM(t, f, 4321)
		forbidEmbeddedSideEffects(t, f)
		f.activeSessionCount = func() int { return 1 }

		if err := f.serviceEmbeddedGate(t.Context(), 50*time.Millisecond); err != nil {
			t.Fatalf("serviceEmbeddedGate must be inert while another model is the default: %v", err)
		}
	})
}

// --- Effective-target advisory (llm.service_models pins) --------------------
//
// A service pin re-routes ONE one-shot call to a dedicated provider/model
// (core applies it per call, before the request is built). The gates above are
// therefore advisory to the EFFECTIVE target of the imminent requests: when
// every service-model key a caller names is pinned away from the embedded
// provider, no idle wait defers the call and no weights load — the pre-pin
// behavior warmed a multi-gigabyte local model for a request that never
// reaches it.

// mockServicePin builds a one-key mock-builder pin resolution for the gate
// tests (the same shape the real builder's ServiceModelPin returns).
func mockServicePin(kind, provider, model string) map[string]core.ServiceModelOverride {
	return map[string]core.ServiceModelOverride{
		kind: {Provider: provider, Model: model, Composite: provider + "/" + model},
	}
}

// fakeEmbeddedSpawn replaces the spawn seam with an instant fake server, so an
// ENGAGING gate can be observed by the model actually becoming resident.
func fakeEmbeddedSpawn(t *testing.T, f *FrontendAPI) {
	t.Helper()
	stubFreePortProbe(t, f)
	f.embedded.spawnFn = func(context.Context, embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		return newFakeEmbeddedProcess(4242), nil
	}
	tightenEmbeddedBudgets(t, f)
}

// TestServiceTargetIsEmbedded pins the effective-target predicate: a resolved
// pin answers for itself, a dropped/unconfigured pin falls back to the
// default-model answer, and without an install nothing resolves to the local
// model — not even a pin naming "embedded".
func TestServiceTargetIsEmbedded(t *testing.T) {
	t.Run("a pin to another provider is false even while it is the default", func(t *testing.T) {
		f, _, mock := newEmbeddedTestAPI(t)
		installEmbeddedAsDefault(t, f)
		mock.mu.Lock()
		mock.serviceModelPins = mockServicePin(config.ServiceModelCommitMessage, "openai", "gpt-4o")
		mock.mu.Unlock()

		if f.serviceTargetIsEmbedded(config.ServiceModelCommitMessage) {
			t.Error("commit_message pinned to openai must not resolve to the embedded provider")
		}
	})

	t.Run("an explicit pin to the embedded provider is true", func(t *testing.T) {
		f, _, mock := newEmbeddedTestAPI(t)
		installEmbeddedAsDefault(t, f)
		mock.mu.Lock()
		mock.serviceModelPins = mockServicePin(config.ServiceModelTitle,
			config.EmbeddedLLMProviderName, config.EmbeddedLLMModelName)
		mock.mu.Unlock()

		if !f.serviceTargetIsEmbedded(config.ServiceModelTitle) {
			t.Error("a pin naming the embedded provider must resolve to it")
		}
	})

	t.Run("no resolved pin falls back to the default-model answer", func(t *testing.T) {
		f, _, _ := newEmbeddedTestAPI(t)
		installEmbeddedAsDefault(t, f)
		if !f.serviceTargetIsEmbedded(config.ServiceModelCommitMessage) {
			t.Error("without a pin the effective target is the default — embedded here")
		}

		f.configMu.Lock()
		f.config.LLM.DefaultModel = "anthropic/claude-3-opus"
		f.configMu.Unlock()
		if f.serviceTargetIsEmbedded(config.ServiceModelCommitMessage) {
			t.Error("without a pin the effective target is the default — not embedded here")
		}
	})

	t.Run("nothing installed is false for every target", func(t *testing.T) {
		f, _, mock := newEmbeddedTestAPI(t)
		mock.mu.Lock()
		mock.serviceModelPins = mockServicePin(config.ServiceModelCommitMessage,
			config.EmbeddedLLMProviderName, config.EmbeddedLLMModelName)
		mock.mu.Unlock()

		if f.serviceTargetIsEmbedded(config.ServiceModelCommitMessage) {
			t.Error("a pin naming 'embedded' must not resolve without an install")
		}
	})
}

// TestServiceEmbeddedGateAdvisesTheEffectiveTarget is the gate-level proof:
// pinned-away calls stay fully inert (idle wait included), a call whose
// effective target is embedded still warms, and a batch warms when ANY of its
// kinds resolves to the embedded provider.
func TestServiceEmbeddedGateAdvisesTheEffectiveTarget(t *testing.T) {
	t.Run("a pin away keeps the gate fully inert", func(t *testing.T) {
		f, _, mock := newEmbeddedTestAPI(t)
		installEmbeddedAsDefault(t, f)
		forbidEmbeddedSideEffects(t, f)
		// A live task would normally defer the call — the pinned-away gate
		// must not even reach the idle wait.
		f.activeSessionCount = func() int { return 1 }
		mock.mu.Lock()
		mock.serviceModelPins = mockServicePin(config.ServiceModelCommitMessage, "openai", "gpt-4o")
		mock.mu.Unlock()

		if err := f.serviceEmbeddedGate(t.Context(), 20*time.Millisecond, config.ServiceModelCommitMessage); err != nil {
			t.Fatalf("the gate must be inert for a pinned-away call: %v", err)
		}
	})

	t.Run("the effective target embedded: the gate warms the model", func(t *testing.T) {
		f, _, mock := newEmbeddedTestAPI(t)
		installEmbeddedAsDefault(t, f)
		fakeEmbeddedSpawn(t, f)
		mock.mu.Lock()
		mock.serviceModelPins = mockServicePin(config.ServiceModelCommitMessage,
			config.EmbeddedLLMProviderName, config.EmbeddedLLMModelName)
		mock.mu.Unlock()

		if err := f.serviceEmbeddedGate(t.Context(), 50*time.Millisecond, config.ServiceModelCommitMessage); err != nil {
			t.Fatalf("the gate: %v", err)
		}
		if status := f.GetEmbeddedLLMStatus(); !status.Loaded {
			t.Errorf("status = %+v, want loaded — an embedded-target call must warm the model", status)
		}
	})

	t.Run("union: one kind pinned away, another riding the default still warms", func(t *testing.T) {
		f, _, mock := newEmbeddedTestAPI(t)
		installEmbeddedAsDefault(t, f)
		fakeEmbeddedSpawn(t, f)
		mock.mu.Lock()
		mock.serviceModelPins = mockServicePin(config.ServiceModelOptimizeExtract, "openai", "gpt-4o")
		mock.mu.Unlock()

		if err := f.serviceEmbeddedGateInteractive(t.Context(),
			config.ServiceModelOptimizeExtract, config.ServiceModelOptimizeRewrite); err != nil {
			t.Fatalf("the gate: %v", err)
		}
		if status := f.GetEmbeddedLLMStatus(); !status.Loaded {
			t.Errorf("status = %+v, want loaded — the unpinned rewrite call rides the embedded default", status)
		}
	})

	t.Run("the background gate covers its two callers' union", func(t *testing.T) {
		f, _, mock := newEmbeddedTestAPI(t)
		installEmbeddedAsDefault(t, f)
		forbidEmbeddedSideEffects(t, f)
		mock.mu.Lock()
		mock.serviceModelPins = map[string]core.ServiceModelOverride{
			config.ServiceModelTitle:         {Provider: "openai", Model: "gpt-4o", Composite: "openai/gpt-4o"},
			config.ServiceModelPromptSuggest: {Provider: "openai", Model: "gpt-4o", Composite: "openai/gpt-4o"},
		}
		mock.mu.Unlock()

		if err := f.serviceEmbeddedGateBackground(t.Context()); err != nil {
			t.Fatalf("both background services pinned away: the gate must be inert: %v", err)
		}
	})
}

// TestOptimizePromptGateFollowsThePins threads the kinds end to end: the RPC
// must name optimize_extract + optimize_rewrite at the gate, so pins routing
// both calls away keep the RPC load-free even though the local model is the
// default (forbidden side effects fail the test on any spawn attempt).
func TestOptimizePromptGateFollowsThePins(t *testing.T) {
	f, _, mock := newEmbeddedTestAPI(t)
	installEmbeddedAsDefault(t, f)
	forbidEmbeddedSideEffects(t, f)
	mock.optimizePromptRes = &core.OptimizePromptResult{OptimizedPrompt: "better"}
	mock.mu.Lock()
	mock.serviceModelPins = map[string]core.ServiceModelOverride{
		config.ServiceModelOptimizeExtract: {Provider: "openai", Model: "gpt-4o", Composite: "openai/gpt-4o"},
		config.ServiceModelOptimizeRewrite: {Provider: "openai", Model: "gpt-4o", Composite: "openai/gpt-4o"},
	}
	mock.mu.Unlock()

	if _, err := f.OptimizePrompt("make this better"); err != nil {
		t.Fatalf("OptimizePrompt: %v", err)
	}
	if mock.optimizePromptCalls != 1 {
		t.Errorf("builder calls = %d, want 1", mock.optimizePromptCalls)
	}
}

// TestGenerateCommitMessageGateFollowsThePin is the commit-message twin: with
// the local model as the default, a commit_message pin to another provider
// must reach the builder without spawning anything.
func TestGenerateCommitMessageGateFollowsThePin(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Stage a change so generation reaches the builder (same shape as
		// TestGenerateCommitMessage_Delegates).
		if err := os.WriteFile(filepath.Join(dir, "newfile.txt"), []byte("hello\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		runGit(t, dir, "add", "newfile.txt")

		f.configMu.Lock()
		if f.config == nil {
			f.config = &config.Config{}
			config.ApplyDefaults(f.config)
		}
		f.configMu.Unlock()
		installEmbeddedAsDefault(t, f)
		forbidEmbeddedSideEffects(t, f)

		mock := &mockBuilder{
			generateCommitMsgRes: "feat: add new thing",
			serviceModelPins:     mockServicePin(config.ServiceModelCommitMessage, "openai", "gpt-4o"),
		}
		f.builderOverride = mock

		if _, err := f.GenerateCommitMessage(); err != nil {
			t.Fatalf("GenerateCommitMessage: %v", err)
		}
		if mock.generateCommitMsgCalls != 1 {
			t.Errorf("builder calls = %d, want 1", mock.generateCommitMsgCalls)
		}
	})
}
