package backend

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// Tests of the ensure-loaded transport WIRING — the half of the feature that
// makes "the model loads itself before the first request" true in production
// rather than only in core's own unit tests.
//
// The gap these pin: core attaches the transport only when
// BuilderConfig.EmbeddedLLM carries BOTH a provider name and a Loader, and
// ToBuilderConfig is a pure function of *config.Config that cannot reach the
// supervisor. So the seam has to be injected on the backend side, at every
// call site, without taking st.mu (which the lock order forbids under configMu)
// and without requiring the supervisor to exist yet (the router is first built
// inside NewApplication, before this subsystem is constructed).

// builderConfigUnderLock runs toBuilderConfigLocked the way every production
// call site does — with configMu held.
func builderConfigUnderLock(f *FrontendAPI) *core.BuilderConfig {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	return f.toBuilderConfigLocked()
}

// TestToBuilderConfigLockedInjectsTheEmbeddedLoaderOnlyWhenInstalled pins the
// gate: the seam follows the persisted install state, so it is present from the
// config load onwards and absent — leaving any unrelated provider that happens
// to be named "embedded" alone — while the local model is not installed.
func TestToBuilderConfigLockedInjectsTheEmbeddedLoaderOnlyWhenInstalled(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)

	bc := builderConfigUnderLock(f)
	if bc.EmbeddedLLM.Loader != nil || bc.EmbeddedLLM.ProviderName != "" {
		t.Errorf("EmbeddedLLM = %+v with nothing installed; the seam must stay inert "+
			"(a user's own provider named %q must not be hijacked)",
			bc.EmbeddedLLM, config.EmbeddedLLMProviderName)
	}

	installEmbeddedLLM(t, f, 4321)

	bc = builderConfigUnderLock(f)
	if bc.EmbeddedLLM.Loader == nil {
		t.Fatal("EmbeddedLLM.Loader = nil after an install; the ensure-loaded transport " +
			"would never be attached and a cold request would fail to connect")
	}
	if bc.EmbeddedLLM.ProviderName != config.EmbeddedLLMProviderName {
		t.Errorf("ProviderName = %q, want %q", bc.EmbeddedLLM.ProviderName, config.EmbeddedLLMProviderName)
	}
	// The guard is name-scoped, so it can only fire if a router entry with that
	// name actually exists. Assert the entry is there, or the loader guards
	// nothing.
	if _, ok := bc.LLM.ProviderConfigs[config.EmbeddedLLMProviderName]; !ok {
		t.Errorf("the builder config carries no %q provider entry, so the loader "+
			"would guard nothing: %+v", config.EmbeddedLLMProviderName, bc.LLM.ProviderConfigs)
	}
	// LoadWaitTimeout stays zero so core applies its own default; deriving it
	// from the config here would need embeddedAutoUnloadPolicy, which takes
	// configMu.RLock and would self-deadlock under the caller's lock.
	if bc.EmbeddedLLM.LoadWaitTimeout != 0 {
		t.Errorf("LoadWaitTimeout = %v, want 0 (core's DefaultLoadWaitTimeout)",
			bc.EmbeddedLLM.LoadWaitTimeout)
	}

	// Removing the install withdraws the seam again.
	f.configMu.Lock()
	f.config.EmbeddedLLM.Installed = false
	f.config.SyncEmbeddedLLMProvider(0)
	f.configMu.Unlock()

	if bc = builderConfigUnderLock(f); bc.EmbeddedLLM.Loader != nil {
		t.Errorf("EmbeddedLLM.Loader survived the uninstall: %+v", bc.EmbeddedLLM)
	}
}

// TestEmbeddedLoaderRefResolvesBeforeTheSupervisorExists is the ordering
// property the indirection exists for: the seam is attached while no supervisor
// has been constructed yet, and a later Load still reaches the real one. A
// direct *Server reference could not do both.
func TestEmbeddedLoaderRefResolvesBeforeTheSupervisorExists(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	_, port := modelsEndpoint(t)
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(port, 32768))
	// The installer writes BOTH halves — the manifest on disk and the
	// authoritative embedded_llm config section the provider record is generated
	// from. A tree without the config is not an install: with no generated
	// provider entry there is no router entry for the seam to guard.
	installEmbeddedLLM(t, f, port)
	// The spawn is faked, so nothing but this test's own /v1/models responder
	// holds the manifest port; the pre-spawn scan has to be told the port is
	// bindable or it would move the load off the endpoint that answers
	// readiness.
	stubFreePortProbe(t, f)

	proc := newFakeEmbeddedProcess(4242)
	spawns := make(chan embeddedllm.LaunchCommand, 1)
	f.embedded.spawnFn = func(_ context.Context, cmd embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		spawns <- cmd
		return proc, nil
	}

	// Capture the seam BEFORE anything constructs the subsystem.
	if got := f.embedded.loader.Load(); got != nil {
		t.Fatalf("the supervisor already exists (%p); this test must capture the seam first", got)
	}
	bc := builderConfigUnderLock(f)
	if bc.EmbeddedLLM.Loader == nil {
		t.Fatal("EmbeddedLLM.Loader = nil before the supervisor exists; the first router " +
			"build (inside NewApplication) would permanently lose the transport")
	}

	// Now construct it, the way the startup restore does, and shorten the
	// supervision budgets so the load cannot hang the test.
	tightenEmbeddedBudgets(t, f)

	if err := bc.EmbeddedLLM.Loader.Load(context.Background()); err != nil {
		t.Fatalf("Load through the injected seam: %v", err)
	}
	select {
	case <-spawns:
	default:
		t.Error("the cold load did not spawn the server")
	}
	if status := f.GetEmbeddedLLMStatus(); !status.Loaded {
		t.Errorf("status = %+v, want loaded after a cold load through the seam", status)
	}

	// A completed response restarts the idle budget; it must not panic or
	// resurrect a supervisor that was never built.
	bc.EmbeddedLLM.Loader.MarkActivity()
}

// TestInitEmbeddedLLMAttachesTheTransportToTheLiveRouter closes the startup
// ordering gap end-to-end: the router is first built inside NewApplication
// (which has no FrontendAPI and so no seam), so the startup restore must
// re-attach it before backend:ready — and must not impose a router rebuild on
// machines that never installed the model.
func TestInitEmbeddedLLMAttachesTheTransportToTheLiveRouter(t *testing.T) {
	t.Run("installed: the live router gets the seam", func(t *testing.T) {
		f, _, mock := newEmbeddedTestAPI(t)
		_, port := modelsEndpoint(t)
		writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(port, 32768))
		// Both halves of an install, as the installer's sink writes them: the
		// manifest the restore reads AND the config section the backend-owned
		// provider record (and therefore the router entry the seam guards) is
		// generated from.
		installEmbeddedLLM(t, f, port)
		forbidEmbeddedSideEffects(t, f)

		var captured *core.BuilderConfig
		mock.rebuildRouterHook = func(cfg *core.BuilderConfig) { captured = cfg }

		f.Lifecycle().InitEmbeddedLLM()

		if captured == nil {
			t.Fatal("RebuildRouter was not called during the startup restore; the router " +
				"built by NewApplication keeps carrying no ensure-loaded transport")
		}
		if captured.EmbeddedLLM.Loader == nil {
			t.Errorf("the rebuilt router got no Loader: %+v", captured.EmbeddedLLM)
		}
		if captured.EmbeddedLLM.ProviderName != config.EmbeddedLLMProviderName {
			t.Errorf("ProviderName = %q, want %q",
				captured.EmbeddedLLM.ProviderName, config.EmbeddedLLMProviderName)
		}
		// The restore must stay a restore: attaching the transport is not
		// loading the model.
		if status := f.GetEmbeddedLLMStatus(); status.Loaded || status.Loading {
			t.Errorf("status = %+v, want the model NOT resident after startup", status)
		}
	})

	t.Run("not installed: no rebuild is imposed on every machine", func(t *testing.T) {
		f, _, mock := newEmbeddedTestAPI(t)
		forbidEmbeddedSideEffects(t, f)

		f.Lifecycle().InitEmbeddedLLM()

		if mock.rebuildRouterCalls != 0 {
			t.Errorf("RebuildRouter was called %d time(s) with nothing installed; the "+
				"startup rebuild must be gated on an install", mock.rebuildRouterCalls)
		}
	})
}

// TestToBuilderConfigLockedNeverTakesTheSubsystemMutex is the lock-order
// regression. The order is one-directional, (st.mu | st.infoMu) → configMu, so
// reading the seam under configMu while another goroutine holds st.mu must not
// block. If the injection ever reached for st.mu (or for embeddedBuild, which
// takes both st.mu and configMu.RLock) this would deadlock instead of failing.
func TestToBuilderConfigLockedNeverTakesTheSubsystemMutex(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	installEmbeddedLLM(t, f, 4321)

	f.embedded.mu.Lock()
	defer f.embedded.mu.Unlock()

	type result struct {
		bc  *core.BuilderConfig
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- result{err: &panicError{r}}
			}
		}()
		done <- result{bc: builderConfigUnderLock(f)}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("toBuilderConfigLocked panicked while st.mu was held: %v", res.err)
		}
		if res.bc.EmbeddedLLM.Loader == nil {
			t.Error("Loader = nil; the seam must be readable without st.mu")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("toBuilderConfigLocked blocked while st.mu was held: the injection " +
			"inverted the (st.mu → configMu) lock order")
	}
}

// TestEmbeddedLoaderRefWithoutAnAgentDirIsAnErrorNotAPanic covers the
// pre-startup zero-value FrontendAPI (desktop.NewApp constructs one so early
// RPC calls return errors instead of panicking): a request that reaches the
// seam before the app is wired must fail with an actionable error.
func TestEmbeddedLoaderRefWithoutAnAgentDirIsAnErrorNotAPanic(t *testing.T) {
	f := &FrontendAPI{logger: slog.New(slog.DiscardHandler)}
	ref := embeddedLoaderRef{f: f}

	err := ref.Load(context.Background())
	if err == nil {
		t.Fatal("Load succeeded with no agent directory")
	}
	if !strings.Contains(err.Error(), "agent directory") {
		t.Errorf("the error %q does not name the missing agent directory", err)
	}
	ref.MarkActivity() // must not panic
}

// panicError carries a recovered panic value out of a test goroutine.
type panicError struct{ value any }

func (e *panicError) Error() string { return "panic: " + strings.TrimSpace(errString(e.value)) }

func errString(v any) string {
	if err, ok := v.(error); ok {
		return err.Error()
	}
	if s, ok := v.(string); ok {
		return s
	}
	return "unknown"
}
