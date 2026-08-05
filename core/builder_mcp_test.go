package core

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/tools/mcp"
)

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
