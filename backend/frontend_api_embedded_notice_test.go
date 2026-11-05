// The embedded_llm:notice event transport: the core supervisor's non-terminal
// launch findings (the load-time free-memory pre-flight) forwarded to the UI
// as a global event beside — never instead of — the state machine's own
// embedded_llm:state verdicts.
package backend

import (
	"testing"

	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// TestOnEmbeddedLLMNoticeForwardsAsGlobalEvent pins the payload contract: the
// notice maps field for field onto EmbeddedLLMNoticeData, under the event
// name the frontend subscribes to.
func TestOnEmbeddedLLMNoticeForwardsAsGlobalEvent(t *testing.T) {
	t.Parallel()

	f, rec, _ := newEmbeddedTestAPI(t)

	f.onEmbeddedLLMNotice(embeddedllm.ServerNotice{
		Kind:              embeddedllm.NoticeLowFreeDeviceMemory,
		Message:           "the accelerator has 8000 MiB free, below the 20000 MiB this launch plans to place on it",
		FreeDeviceMiB:     8000,
		ExpectedDeviceMiB: 20000,
	})

	payloads := rec.of(EventEmbeddedLLMNotice)
	if len(payloads) != 1 {
		t.Fatalf("got %d %q events, want 1", len(payloads), EventEmbeddedLLMNotice)
	}
	data, ok := payloads[0].(EmbeddedLLMNoticeData)
	if !ok {
		t.Fatalf("payload is %T, want EmbeddedLLMNoticeData", payloads[0])
	}
	if data.Kind != "low_free_device_memory" {
		t.Errorf("kind = %q, want low_free_device_memory", data.Kind)
	}
	if data.Message == "" {
		t.Error("message was dropped")
	}
	if data.FreeDeviceMiB != 8000 || data.ExpectedDeviceMiB != 20000 {
		t.Errorf("evidence fields = (%d, %d), want (8000, 20000)",
			data.FreeDeviceMiB, data.ExpectedDeviceMiB)
	}

	// A notice is NOT a state event: the two transports stay separate, so a
	// renderer keying on embedded_llm:state sees no spurious transition.
	if rec.count(EventEmbeddedLLMState) != 0 {
		t.Errorf("a notice produced %d state events, want 0", rec.count(EventEmbeddedLLMState))
	}
}

// TestEmbeddedBuildWiresOnNotice pins the production wiring: a built
// supervisor delivers notices, exactly as it delivers state events.
func TestEmbeddedBuildWiresOnNotice(t *testing.T) {
	t.Parallel()

	f, _, _ := newEmbeddedTestAPI(t)
	server, _, err := f.embeddedBuild()
	if err != nil {
		t.Fatalf("embeddedBuild: %v", err)
	}
	if server.OnNotice == nil {
		t.Fatal("the supervisor's OnNotice seam is unwired: pre-flight findings would never reach the UI")
	}
	if server.OnState == nil {
		t.Fatal("the supervisor's OnState seam is unwired")
	}
}

// TestEmitEmbeddedLLMNoticeWithoutEmitterIsQuiet: no emitEvent wired (most
// unit tests) — the transport must be a no-op, not a panic.
func TestEmitEmbeddedLLMNoticeWithoutEmitterIsQuiet(t *testing.T) {
	t.Parallel()

	f, _, _ := newEmbeddedTestAPI(t)
	f.emitEvent = nil

	f.onEmbeddedLLMNotice(embeddedllm.ServerNotice{
		Kind:              embeddedllm.NoticeLowFreeDeviceMemory,
		Message:           "shortfall",
		FreeDeviceMiB:     1,
		ExpectedDeviceMiB: 2,
	})
}
