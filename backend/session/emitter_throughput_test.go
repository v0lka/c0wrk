package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestEventEmitterEmitSessionTokensWithThroughput pins the throughput-augmented
// session-token seam: the typed payload carries the median tok/s and sample
// count alongside the existing totals/fill fields, the persistence callback
// still receives the plain totals (the sessions-table schema is unchanged),
// and the cached totals (TokenSnapshot) stay in sync.
func TestEventEmitterEmitSessionTokensWithThroughput(t *testing.T) {
	var received Event
	emit := func(e Event) { received = e }

	var persisted []string
	var persistIn, persistOut int
	emitter := NewEventEmitter("test-session", emit)
	emitter.SetTokenPersist(func(inputTokens, outputTokens int, _, _ string, _ float64) {
		persistIn, persistOut = inputTokens, outputTokens
		persisted = append(persisted, "called")
	})

	// Populate the session-root fill cache to verify forwarding still works
	// on the throughput path.
	emitter.ContextFill(75.5, 75500, 100000, "ok", "")

	emitter.EmitSessionTokensWithThroughput(1200, 300, "test-model", "test-family", 42.5, 7)

	if received.Type != "session_tokens" {
		t.Fatalf("expected type 'session_tokens', got %q", received.Type)
	}
	data, ok := received.Data.(SessionTokensEventData)
	if !ok {
		t.Fatalf("expected SessionTokensEventData, got %T", received.Data)
	}
	if data.SessionInputTokens != 1200 || data.SessionOutputTokens != 300 {
		t.Errorf("totals = %d/%d, want 1200/300", data.SessionInputTokens, data.SessionOutputTokens)
	}
	if data.Model != "test-model" || data.Family != "test-family" {
		t.Errorf("model/family = %q/%q", data.Model, data.Family)
	}
	if data.MedianOutputTokS != 42.5 {
		t.Errorf("MedianOutputTokS = %v, want 42.5", data.MedianOutputTokS)
	}
	if data.TokSSamples != 7 {
		t.Errorf("TokSSamples = %d, want 7", data.TokSSamples)
	}
	if data.FillPercent != 75.5 || data.UsedTokens != 75500 || data.MaxTokens != 100000 {
		t.Errorf("cached fill forwarding broken: %+v", data)
	}

	if len(persisted) != 1 || persistIn != 1200 || persistOut != 300 {
		t.Errorf("persist callback = %v (%d/%d), want one call with 1200/300", persisted, persistIn, persistOut)
	}

	snap := emitter.TokenSnapshot()
	if snap.OutputTokens != 300 || snap.InputTokens != 1200 {
		t.Errorf("cached totals = %d/%d, want 1200/300", snap.InputTokens, snap.OutputTokens)
	}
}

// TestSessionTokensEventData_OmitEmptyWireShape verifies the wire contract:
// until the throughput window warms up (median 0, samples 0), the new fields
// are absent from the JSON payload — legacy consumers never see them.
func TestSessionTokensEventData_OmitEmptyWireShape(t *testing.T) {
	warm := SessionTokensEventData{SessionInputTokens: 10, MedianOutputTokS: 12.5, TokSSamples: 3}
	b, err := json.Marshal(warm)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"median_output_tok_s":12.5`) || !strings.Contains(string(b), `"tok_s_samples":3`) {
		t.Errorf("warm payload missing throughput fields: %s", b)
	}

	cold := SessionTokensEventData{SessionInputTokens: 10}
	b, err = json.Marshal(cold)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "median_output_tok_s") || strings.Contains(string(b), "tok_s_samples") {
		t.Errorf("cold payload must omit throughput fields: %s", b)
	}
}

// legacySessionTokensJSON is the exact payload shape session_tokens events
// carried before the throughput fields existed (see specs/contracts/event-catalog.md).
const legacySessionTokensJSON = `{
	"session_input_tokens": 1200,
	"session_output_tokens": 300,
	"model": "test-model",
	"family": "test-family",
	"fill_percent": 42.5,
	"used_tokens": 75500,
	"max_tokens": 100000
}`

// TestSessionTokensEventData_LegacyJSONLoads verifies tolerance for legacy
// payloads: unmarshalling a pre-throughput session_tokens JSON blob into the
// extended struct succeeds and leaves the new fields at their zero values.
func TestSessionTokensEventData_LegacyJSONLoads(t *testing.T) {
	var data SessionTokensEventData
	if err := json.Unmarshal([]byte(legacySessionTokensJSON), &data); err != nil {
		t.Fatalf("legacy session_tokens payload must load without error: %v", err)
	}
	if data.SessionInputTokens != 1200 || data.SessionOutputTokens != 300 {
		t.Errorf("totals = %d/%d, want 1200/300", data.SessionInputTokens, data.SessionOutputTokens)
	}
	if data.MedianOutputTokS != 0 || data.TokSSamples != 0 {
		t.Errorf("throughput fields must stay zero for legacy payloads, got %v/%d", data.MedianOutputTokS, data.TokSSamples)
	}
}

// TestLegacySessionTokensRow_LoadsWithoutError pins the persistence tolerance
// at the store level: message rows written by older builds (metadata carrying
// a legacy session_tokens-shaped payload, no throughput fields) load through
// the normal LoadMessages path without error.
func TestLegacySessionTokensRow_LoadsWithoutError(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	if err := store.SaveSession(ctx, SessionInfo{ID: "legacy-tokens-session", ProjectID: testProjectID, CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	// A row as an older build could have written it: generic role, metadata
	// = legacy session_tokens payload without the new fields.
	if err := store.SaveMessage(ctx, ChatMessage{
		SessionID: "legacy-tokens-session",
		Role:      "event_unknown",
		Content:   legacySessionTokensJSON,
		Metadata:  json.RawMessage(legacySessionTokensJSON),
		CreatedAt: "2026-01-01T00:00:01Z",
	}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}

	msgs, err := store.LoadMessages(ctx, "legacy-tokens-session")
	if err != nil {
		t.Fatalf("legacy session_tokens row must load without error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	// The payload itself still decodes into the extended struct.
	var data SessionTokensEventData
	if err := json.Unmarshal(msgs[0].Metadata, &data); err != nil {
		t.Fatalf("legacy metadata must decode into SessionTokensEventData: %v", err)
	}
	if data.MedianOutputTokS != 0 || data.TokSSamples != 0 {
		t.Errorf("throughput fields must stay zero, got %v/%d", data.MedianOutputTokS, data.TokSSamples)
	}
}

// TestEventPersister_SkipsSessionTokensWithThroughput pins that the enriched
// session_tokens event remains transient: the persister must not write message
// rows for it, regardless of the payload's new fields.
func TestEventPersister_SkipsSessionTokensWithThroughput(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	p.Persist(Event{
		SessionID: "s1",
		Type:      "session_tokens",
		Data: SessionTokensEventData{
			SessionInputTokens: 100, SessionOutputTokens: 50,
			Model: "m", Family: "f",
			MedianOutputTokS: 42.5, TokSSamples: 9,
		},
	})
	// Close drains the single-writer queue, so any wrongly-enqueued write
	// would be visible in store.messages here.
	p.Close()

	if len(store.messages) != 0 {
		t.Errorf("session_tokens must stay transient; persister wrote %d rows: %+v", len(store.messages), store.messages)
	}
}
