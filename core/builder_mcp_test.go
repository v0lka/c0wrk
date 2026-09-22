package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/tools/mcp"
)

// TestConfigToGatewayConfig_MCPTimeouts verifies the per-server timeout /
// call_timeout durations are copied from BuilderMCPServer into the sp4rk
// mcp.ServerEntry the gateway consumes.
func TestConfigToGatewayConfig_MCPTimeouts(t *testing.T) {
	cfg := &BuilderConfig{
		MCP: BuilderMCPConfig{
			Servers: map[string]BuilderMCPServer{
				"srv": {
					Transport:   "http",
					URL:         "https://example.com/mcp",
					Timeout:     30 * time.Second,
					CallTimeout: 2 * time.Minute,
				},
			},
		},
	}

	gw := configToGatewayConfig(cfg)
	entry, ok := gw.Servers["srv"]
	if !ok {
		t.Fatal(`server "srv" missing from gateway config`)
	}
	if entry.Timeout != 30*time.Second {
		t.Errorf("ServerEntry.Timeout = %v, want 30s", entry.Timeout)
	}
	if entry.CallTimeout != 2*time.Minute {
		t.Errorf("ServerEntry.CallTimeout = %v, want 2m", entry.CallTimeout)
	}
}

// newFailingGateway returns a non-nil *mcp.Gateway backed by a stdio server
// whose command does not exist. StartGateway returns the gateway even when the
// underlying server fails to connect, so the returned gateway is usable for
// testing field writes (e.g. SetDefaultWorkDir) without performing any real I/O.
func newFailingGateway(t *testing.T) *mcp.Gateway {
	t.Helper()
	registry := tools.NewToolRegistry()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	gw, err := mcp.StartGateway(ctx, mcp.GatewayConfig{
		Servers: map[string]mcp.ServerEntry{
			"failing": {Transport: "stdio", Command: "/nonexistent/binary/that/does/not/exist"},
		},
	}, registry.ToolRegistry, func(s string) string { return s }, nil)
	if err != nil {
		t.Fatalf("StartGateway returned unexpected error: %v", err)
	}
	if gw == nil {
		t.Fatal("expected non-nil gateway even when server fails to start")
	}
	t.Cleanup(func() {
		if err := gw.Stop(); err != nil {
			t.Logf("gateway stop error (ignored): %v", err)
		}
	})
	return gw
}

// gatewayDefaultWorkDir reads the unexported defaultWorkDir field of an
// *mcp.Gateway via reflection. The field is private to the mcp package, so the
// core test package cannot access it directly; reflection lets us assert that
// the record-and-apply path propagated the work directory into the gateway.
func gatewayDefaultWorkDir(t *testing.T, gw *mcp.Gateway) string {
	t.Helper()
	v := reflect.ValueOf(gw).Elem()
	field := v.FieldByName("defaultWorkDir")
	if !field.IsValid() {
		t.Fatal("reflect: defaultWorkDir field not found on mcp.Gateway")
	}
	return field.String()
}

// TestSetMCPWorkDir_DoesNotBlock proves that SetMCPWorkDir returns immediately
// even when the MCP startup goroutine has not finished (mcpDone is still open)
// and no gateway has been assigned yet. The previous implementation called
// waitMCPReady, which would block up to 30s behind MCP server discovery.
func TestSetMCPWorkDir_DoesNotBlock(t *testing.T) {
	b := &OrchestratorBuilder{
		mcpDone: make(chan struct{}), // open — startup "in flight"
		// gateway == nil
	}

	done := make(chan struct{})
	start := time.Now()
	go func() {
		b.SetMCPWorkDir("/some/workdir")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SetMCPWorkDir blocked for >2s with open mcpDone and nil gateway")
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("SetMCPWorkDir took %v; expected to be near-instant (non-blocking)", elapsed)
	}

	b.mu.RLock()
	recorded := b.mcpWorkDir
	b.mu.RUnlock()
	if recorded != "/some/workdir" {
		t.Errorf("b.mcpWorkDir = %q, want %q", recorded, "/some/workdir")
	}
}

// TestSetMCPWorkDir_AppliesWhenGatewayPresent verifies that when the gateway is
// already assigned, SetMCPWorkDir applies the directory immediately to the
// gateway (not just records it for later).
func TestSetMCPWorkDir_AppliesWhenGatewayPresent(t *testing.T) {
	gw := newFailingGateway(t)

	b := &OrchestratorBuilder{
		mcpDone: make(chan struct{}),
		gateway: gw,
	}

	b.SetMCPWorkDir("/applied/workdir")

	if got := gatewayDefaultWorkDir(t, gw); got != "/applied/workdir" {
		t.Errorf("gateway defaultWorkDir = %q, want %q", got, "/applied/workdir")
	}

	// The recorded value must also be persisted.
	b.mu.RLock()
	recorded := b.mcpWorkDir
	b.mu.RUnlock()
	if recorded != "/applied/workdir" {
		t.Errorf("b.mcpWorkDir = %q, want %q", recorded, "/applied/workdir")
	}
}

// TestSetMCPWorkDir_RecordedBeforeGateway verifies the deferred-apply path: a
// SetMCPWorkDir call that arrives before the gateway exists records the value,
// and when runMCPInit later assigns the gateway it applies the recorded value.
func TestSetMCPWorkDir_RecordedBeforeGateway(t *testing.T) {
	b := &OrchestratorBuilder{
		mcpDone: make(chan struct{}), // open — gateway not yet assigned
	}

	// Recorded, but gateway == nil so it is NOT applied yet.
	b.SetMCPWorkDir("/deferred/workdir")

	// Sanity: recorded but no gateway to apply to.
	b.mu.RLock()
	recorded := b.mcpWorkDir
	b.mu.RUnlock()
	if recorded != "/deferred/workdir" {
		t.Fatalf("b.mcpWorkDir = %q, want %q before gateway assignment", recorded, "/deferred/workdir")
	}

	// Simulate runMCPInit finishing: create a real gateway and apply the
	// recorded work dir under b.mu, exactly as runMCPInit does.
	gw := newFailingGateway(t)

	b.mu.Lock()
	b.gateway = gw
	if gw != nil && b.mcpWorkDir != "" {
		gw.SetDefaultWorkDir(b.mcpWorkDir)
	}
	b.mu.Unlock()

	if got := gatewayDefaultWorkDir(t, gw); got != "/deferred/workdir" {
		t.Errorf("gateway defaultWorkDir = %q, want %q (deferred apply)", got, "/deferred/workdir")
	}
}

// TestMCPStartupDone verifies the non-blocking startup-completion signal.
func TestMCPStartupDone(t *testing.T) {
	// Open mcpDone → startup still in flight → false.
	b := &OrchestratorBuilder{mcpDone: make(chan struct{})}
	if b.MCPStartupDone() {
		t.Error("MCPStartupDone() = true, want false when mcpDone is open")
	}

	// Closed mcpDone → startup finished → true.
	close(b.mcpDone)
	if !b.MCPStartupDone() {
		t.Error("MCPStartupDone() = false, want true when mcpDone is closed")
	}
}

// TestWaitMCPStartup verifies the blocking counterpart: it returns nil once
// mcpDone is closed, and ctx.Err() when the context expires first.
func TestWaitMCPStartup(t *testing.T) {
	// 1. Already-closed mcpDone → returns immediately with nil.
	b := &OrchestratorBuilder{mcpDone: make(chan struct{})}
	close(b.mcpDone)
	if err := b.WaitMCPStartup(context.Background()); err != nil {
		t.Errorf("WaitMCPStartup = %v, want nil when mcpDone is already closed", err)
	}

	// 2. Open mcpDone + expiring context → ctx.Err().
	b2 := &OrchestratorBuilder{mcpDone: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := b2.WaitMCPStartup(ctx); err == nil {
		t.Error("WaitMCPStartup = nil, want ctx.Err() when context expires before mcpDone closes")
	}

	// 3. Open mcpDone that closes mid-wait → unblocks with nil.
	b3 := &OrchestratorBuilder{mcpDone: make(chan struct{})}
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(b3.mcpDone)
	}()
	if err := b3.WaitMCPStartup(context.Background()); err != nil {
		t.Errorf("WaitMCPStartup = %v, want nil when mcpDone closes mid-wait", err)
	}
}

// TestMCPGatewayNoWait verifies the three non-blocking return cases of
// MCPGatewayNoWait: nil while startup is in flight, nil when startup finished
// but no gateway was assigned, and the assigned gateway otherwise.
func TestMCPGatewayNoWait(t *testing.T) {
	// 1. mcpDone open → nil (startup in flight, do not block).
	b := &OrchestratorBuilder{mcpDone: make(chan struct{})}
	if gw := b.MCPGatewayNoWait(); gw != nil {
		t.Error("MCPGatewayNoWait() = non-nil, want nil while mcpDone is open")
	}

	// 2. mcpDone closed, gateway == nil → nil (started but no gateway / failed).
	close(b.mcpDone)
	if gw := b.MCPGatewayNoWait(); gw != nil {
		t.Error("MCPGatewayNoWait() = non-nil, want nil when gateway is nil and mcpDone is closed")
	}

	// 3. mcpDone closed, gateway assigned → that gateway.
	gw := newFailingGateway(t)
	b.mu.Lock()
	b.gateway = gw
	b.mu.Unlock()
	if got := b.MCPGatewayNoWait(); got != gw {
		t.Error("MCPGatewayNoWait() did not return the assigned gateway")
	}
}

// --- b.mu must not be held across MCP gateway network work ---

// hangingMCPServer returns an HTTPS-free test server that blocks every request
// until release is closed, plus a channel closed when the first request lands
// and a counter of requests received. It stands in for an MCP endpoint that
// went unreachable (the real-world case: a server that failed to connect while
// a proxy was on is retried on the next Reconfigure, silently and with no
// deadline of its own).
func hangingMCPServer(t *testing.T) (url string, entered <-chan struct{}, hits *atomic.Int32, release func()) {
	t.Helper()
	enteredCh := make(chan struct{})
	releaseCh := make(chan struct{})
	hits = &atomic.Int32{}
	var once sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		once.Do(func() { close(enteredCh) })
		select {
		case <-releaseCh:
		case <-r.Context().Done():
		}
		// Abort the connection rather than answering: the client must fail
		// fast once released, not sit waiting for a valid MCP handshake that
		// this stub cannot produce.
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(srv.Close)

	// Idempotent, so a test can release on a failure path and again at the
	// end. Callers MUST also `defer release()` in the test body: t.Cleanup
	// callbacks run LIFO, so a gateway registered after this helper would have
	// its Stop() run first — and Stop blocks on the gateway mutex that the
	// parked Reconfigure still holds. A body-level defer runs ahead of every
	// cleanup, including on the t.Fatal path (Goexit runs defers first).
	release = sync.OnceFunc(func() { close(releaseCh) })
	t.Cleanup(release)
	return srv.URL, enteredCh, hits, release
}

func hangingServerConfig(url string) *BuilderConfig {
	return &BuilderConfig{
		MCP: BuilderMCPConfig{
			Servers: map[string]BuilderMCPServer{
				"hang": {Transport: "http", URL: url},
			},
		},
		ExpandEnvVars: func(s string) string { return s },
	}
}

// closedChan returns an already-closed channel, standing in for "MCP startup
// finished" (mcpDone).
func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// TestReconfigureMCP_DoesNotHoldBuilderLockDuringGatewayWork is the regression
// test for the settings-dialog freeze: ReconfigureMCP used to hold b.mu
// (WRITE) across gateway.Reconfigure, which connects to every changed and
// every previously-failed server. b.mu sits on the GetConfig path
// (configMu.RLock → b.mu.RLock via ModelRegistry), so an unreachable MCP
// endpoint froze the whole settings dialog for as long as the connect took —
// observed at 169 seconds in the field.
func TestReconfigureMCP_DoesNotHoldBuilderLockDuringGatewayWork(t *testing.T) {
	url, entered, _, release := hangingMCPServer(t)
	defer release()

	b := &OrchestratorBuilder{
		registry: tools.NewToolRegistry(),
		gateway:  newFailingGateway(t),
		mcpDone:  closedChan(),
	}

	reconfigureDone := make(chan struct{})
	go func() {
		defer close(reconfigureDone)
		_ = b.ReconfigureMCP(context.Background(), hangingServerConfig(url))
	}()

	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("the gateway reconfigure never reached the MCP endpoint")
	}

	// The reconfigure is now parked inside the network call. A config reader
	// must not be convoyed behind it: ModelRegistry is exactly what GetConfig
	// calls while holding configMu.RLock.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		_ = b.ModelRegistry()
	}()
	select {
	case <-readerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("ModelRegistry blocked while the MCP gateway was reconfiguring — b.mu is held across the network call")
	}

	// A writer must get in too: a queued writer is what stops every
	// subsequent reader under Go's RWMutex, so this is the stricter check.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		b.mu.Lock()
		b.mcpWorkDir = "/tmp/probe"
		b.mu.Unlock()
	}()
	select {
	case <-writerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("a builder writer blocked while the MCP gateway was reconfiguring")
	}

	release()
	select {
	case <-reconfigureDone:
	case <-time.After(20 * time.Second):
		t.Fatal("ReconfigureMCP did not return after the endpoint was released")
	}
}

// The start path (no gateway yet, i.e. startup failed earlier) also dials, so
// it must not hold b.mu either.
func TestReconfigureMCP_StartPathDoesNotHoldBuilderLock(t *testing.T) {
	url, entered, _, release := hangingMCPServer(t)
	defer release()

	b := &OrchestratorBuilder{
		registry: tools.NewToolRegistry(),
		mcpDone:  closedChan(), // gateway == nil: startup failed earlier
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = b.ReconfigureMCP(context.Background(), hangingServerConfig(url))
	}()

	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("StartGateway never reached the MCP endpoint")
	}

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		_ = b.ModelRegistry()
	}()
	select {
	case <-readerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("ModelRegistry blocked while StartGateway was dialing — b.mu is held across the network call")
	}

	release()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("ReconfigureMCP did not return")
	}
}

// gatewayInitMu serializes the start path so two concurrent callers cannot
// each dial a gateway of their own and leave one orphaned (an orphan keeps its
// stdio subprocesses running). While the first caller is parked inside
// StartGateway, the second must not have reached the endpoint at all.
func TestReconfigureMCP_ConcurrentStartsSerialized(t *testing.T) {
	url, entered, hits, release := hangingMCPServer(t)
	defer release()

	b := &OrchestratorBuilder{
		registry: tools.NewToolRegistry(),
		mcpDone:  closedChan(),
	}

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.ReconfigureMCP(context.Background(), hangingServerConfig(url))
		}()
	}

	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		release()
		wg.Wait()
		t.Fatal("no caller reached the MCP endpoint")
	}
	// Give the second caller a chance to (incorrectly) dial in parallel.
	time.Sleep(500 * time.Millisecond)
	if got := hits.Load(); got != 1 {
		release()
		wg.Wait()
		t.Fatalf("endpoint received %d requests while the first start was in flight, want 1 (the start path must be serialized)", got)
	}

	release()
	wg.Wait()

	b.mu.RLock()
	gw := b.gateway
	b.mu.RUnlock()
	if gw == nil {
		t.Error("no gateway was published after the start path completed")
	}
}

// The proxy client must be read under b.mu: RebuildProxy writes it
// concurrently. Run under -race to catch a regression.
func TestReconfigureMCP_ProxyClientReadIsSynchronized(t *testing.T) {
	url, entered, _, release := hangingMCPServer(t)
	defer release()

	b := &OrchestratorBuilder{
		registry: tools.NewToolRegistry(),
		gateway:  newFailingGateway(t),
		mcpDone:  closedChan(),
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = b.ReconfigureMCP(context.Background(), hangingServerConfig(url))
	}()

	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("the gateway reconfigure never reached the MCP endpoint")
	}

	// Hammer the field the way RebuildProxy does.
	stop := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			b.mu.Lock()
			b.proxyClient = &http.Client{}
			b.mu.Unlock()
			b.mu.Lock()
			b.proxyClient = nil
			b.mu.Unlock()
		}
	}()

	time.Sleep(200 * time.Millisecond)
	close(stop)
	// Bounded: if b.mu were held across the network call again, the writer
	// would be parked on b.mu.Lock forever. Fail with a diagnosis instead of
	// hanging until the package timeout.
	select {
	case <-writerDone:
	case <-time.After(5 * time.Second):
		release()
		<-writerDone
		t.Fatal("the writer never acquired b.mu — it is held across the gateway reconfigure")
	}
	release()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("ReconfigureMCP did not return")
	}
}
