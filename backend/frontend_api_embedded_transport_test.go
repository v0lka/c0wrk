package backend

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	ref := embeddedLoaderRef{f: f}

	err := ref.Load(context.Background())
	if err == nil {
		t.Fatal("Load succeeded with no agent directory")
	}
	if !strings.Contains(err.Error(), "agent directory") {
		t.Errorf("the error %q does not name the missing agent directory", err)
	}
	ref.MarkActivity() // must not panic
	// The two optional capabilities resolve the same cached supervisor, so with
	// none cached they must be inert rather than an error path: Port reports 0,
	// which is what redirectToLivePort reads as "do not redirect".
	ref.BeginRequest()
	ref.EndRequest()
	if got := ref.Port(); got != 0 {
		t.Errorf("Port() = %d with no supervisor cached, want 0 (the transport's "+
			"no-redirect answer)", got)
	}
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

// ── the production seam's optional capabilities ────────────────────────────
//
// The transport discovers BOTH of its optional Loader capabilities —
// embeddedllm.RequestTracker (the in-flight counter that lets an idle expiry
// defer instead of killing a generation) and embeddedllm.PortSource (the
// live-port redirect) — through an interface assertion on whatever Loader it was
// handed. Production never hands it a *Server: applyEmbeddedLoader and
// syncEmbeddedBuilderSeam both inject embeddedLoaderRef, so a ref that
// implements only Load and MarkActivity fails those assertions SILENTLY and
// both controls become dead code while every fake-loader test still passes.
// The tests below therefore drive the value applyEmbeddedLoader actually
// produces, wrapped by the same EnsureLoadedClient call core.buildRouter makes.

// seamProbeTransport is the inner RoundTripper the seam wraps in tests. It
// records the request the transport actually sent — which is the only way to
// see a redirect — and returns a response whose body stays open until the test
// closes it, because that open body is the window the in-flight counter has to
// cover.
type seamProbeTransport struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (p *seamProbeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req.Clone(req.Context()))
	p.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"choices":[]}`)),
		Request:    req,
	}, nil
}

// lastHost is the URL host of the most recent request that reached the wire.
func (p *seamProbeTransport) lastHost() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reqs) == 0 {
		return ""
	}
	return p.reqs[len(p.reqs)-1].URL.Host
}

func (p *seamProbeTransport) requests() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

// productionSeamClient builds the ensure-loaded client the way core.buildRouter
// does from the BuilderConfig applyEmbeddedLoader produced: same Loader value,
// same ProviderName guard, same LoadWaitTimeout (left zero so core applies
// embeddedllm.DefaultLoadWaitTimeout), same EnsureLoadedClient call. inner is
// what the seam wraps, so a test can inspect the request that goes out.
func productionSeamClient(t *testing.T, f *FrontendAPI, inner http.RoundTripper) (*http.Client, embeddedllm.Loader) {
	t.Helper()
	bc := builderConfigUnderLock(f)
	if bc.EmbeddedLLM.Loader == nil {
		t.Fatal("BuilderConfig.EmbeddedLLM.Loader = nil: applyEmbeddedLoader did not " +
			"inject the production seam, so there is nothing to test")
	}
	if bc.EmbeddedLLM.ProviderName != config.EmbeddedLLMProviderName {
		t.Fatalf("ProviderName = %q, want %q", bc.EmbeddedLLM.ProviderName, config.EmbeddedLLMProviderName)
	}
	base := &http.Client{Transport: inner}
	client := embeddedllm.EnsureLoadedClient(base, base, bc.EmbeddedLLM.Loader,
		bc.EmbeddedLLM.LoadWaitTimeout, slog.New(slog.DiscardHandler))
	return client, bc.EmbeddedLLM.Loader
}

// stageSeamSpawn fakes the spawn and tightens the supervision budgets on an API
// whose install tree and config are already written, so a load through the
// production seam becomes resident against the caller's own /v1/models
// responder. A nil portProbe keeps the persisted port (nothing else is
// listening on it in a test); pass takenBelow to stage the collision the
// live-port redirect exists for.
func stageSeamSpawn(t *testing.T, f *FrontendAPI, portProbe embeddedllm.PortProber) {
	t.Helper()
	if portProbe == nil {
		stubFreePortProbe(t, f)
	} else {
		f.embedded.portProbeFn = portProbe
	}
	f.embedded.spawnFn = func(context.Context, embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		return newFakeEmbeddedProcess(4242), nil
	}
	tightenEmbeddedBudgets(t, f)
}

// TestProductionLoaderSeamCountsARequestInFlightOnTheSupervisor is the
// regression for the dead-code half of "never unload mid-generation". With a
// loader that cannot reach the counter, BeginRequest/EndRequest are never
// called, Server.inFlight stays 0 for the whole life of a generation, the idle
// path sees nothing to defer for, and an expiry stops llama-server mid-stream —
// with auto_unload.minutes validated down to 1, any generation longer than the
// budget is cut off.
func TestProductionLoaderSeamCountsARequestInFlightOnTheSupervisor(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	_, port := modelsEndpoint(t)
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(port, 32768))
	installEmbeddedLLM(t, f, port)
	stageSeamSpawn(t, f, nil)

	inner := &seamProbeTransport{}
	client, loader := productionSeamClient(t, f, inner)

	if err := loader.Load(t.Context()); err != nil {
		t.Fatalf("Load through the production seam: %v", err)
	}
	server := supervisorOf(t, f)
	if got := server.InFlightRequests(); got != 0 {
		t.Fatalf("in-flight = %d before any request was sent", got)
	}

	resp := seamGet(t, client, embeddedBaseURL(port)+"/chat/completions")
	// The body is still open. This is the window an idle expiry must be
	// deferred out of, and it is the assertion a fake loader cannot make.
	if got := server.InFlightRequests(); got != 1 {
		t.Errorf("in-flight = %d while the response body is open, want 1 — the "+
			"production loader does not reach the supervisor's counter, so an idle "+
			"expiry during a generation still stops llama-server mid-stream", got)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing the response body: %v", err)
	}
	if got := server.InFlightRequests(); got != 0 {
		t.Errorf("in-flight = %d after the body was closed, want 0 — the count "+
			"leaked, so every later idle expiry would be deferred forever", got)
	}
	if inner.requests() != 1 {
		t.Errorf("the seam sent %d request(s), want 1", inner.requests())
	}
}

// TestProductionLoaderSeamRedirectsToTheLivePort is the regression for the
// other silently-dead control. persistEmbeddedPort deliberately does NOT rebuild
// the router, because the entry's transport is supposed to redirect to the port
// the supervisor actually bound; a loader that cannot report a port leaves that
// promise unkept, and the request — full prompt included — is POSTed to
// whatever unrelated local process squats the old port, whose answer is then
// reported as the assistant's reply.
func TestProductionLoaderSeamRedirectsToTheLivePort(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	_, livePort := modelsEndpoint(t)
	// The persisted port is the one below the responder, so the pre-spawn scan
	// has exactly one collision to walk past and the load lands on livePort.
	stale := livePort - 1
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(stale, 32768))
	installEmbeddedLLM(t, f, stale)
	stageSeamSpawn(t, f, takenBelow(livePort))

	inner := &seamProbeTransport{}
	client, loader := productionSeamClient(t, f, inner)

	// Before any load the supervisor has bound nothing, so the ref must report
	// the transport's no-redirect answer rather than the stale preference.
	if got := loaderPort(t, loader); got != 0 {
		t.Fatalf("Port() = %d before a load, want 0", got)
	}

	if err := loader.Load(t.Context()); err != nil {
		t.Fatalf("Load through the production seam: %v", err)
	}
	server := supervisorOf(t, f)
	if server.Port() != livePort {
		t.Fatalf("the fixture did not move the port: supervisor = %d, want %d",
			server.Port(), livePort)
	}
	if got := loaderPort(t, loader); got != livePort {
		t.Errorf("Port() = %d through the production seam, want the live %d", got, livePort)
	}

	// The router entry this request came from was built BEFORE the move and was
	// deliberately not rebuilt, so it still names the stale port.
	staleURL := embeddedBaseURL(stale) + "/chat/completions"
	resp := seamGet(t, client, staleURL)
	defer func() { _ = resp.Body.Close() }()

	wantHost := net.JoinHostPort(embeddedllm.LoopbackHost, strconv.Itoa(livePort))
	if got := inner.lastHost(); got != wantHost {
		t.Errorf("the request went to %q, want %q — the prompt would be handed to "+
			"whatever holds the stale port %d", got, wantHost, stale)
	}

	// A request already aimed at the live port must be left alone.
	resp2 := seamGet(t, client, embeddedBaseURL(livePort)+"/chat/completions")
	defer func() { _ = resp2.Body.Close() }()
	if got := inner.lastHost(); got != wantHost {
		t.Errorf("an already-correct request was rewritten to %q, want %q", got, wantHost)
	}
}

// seamGet issues one GET through the production seam and fails the test on a
// transport error. It builds the request explicitly rather than calling
// client.Get because the repo's noctx linter bans the context-free form
// everywhere, tests included — and a request with no context would also be
// immune to the test's own cancellation.
func seamGet(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatalf("building a request for %s: %v", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("the request through the production seam (%s): %v", url, err)
	}
	return resp
}

// loaderPort reads the optional PortSource capability the way the transport
// does, so a test fails on the assertion rather than silently skipping.
func loaderPort(t *testing.T, loader embeddedllm.Loader) int {
	t.Helper()
	source, ok := loader.(embeddedllm.PortSource)
	if !ok {
		t.Fatalf("%T does not implement embeddedllm.PortSource, so the transport's "+
			"live-port redirect can never fire in production", loader)
	}
	return source.Port()
}
