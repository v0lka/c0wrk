package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core"
)

// TestEventEmitterEmitSessionTokensWithStats pins the observability-augmented
// session-token seam: the typed payload carries the median tok/s, the latency
// aggregates and the cache totals alongside the existing totals/fill fields,
// the persistence callback still receives the plain totals (the sessions-table
// schema is unchanged), the cached totals (TokenSnapshot) stay in sync, and
// the aggregates are mirrored into the next agent_metrics payload.
func TestEventEmitterEmitSessionTokensWithStats(t *testing.T) {
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
	// on the stats path.
	emitter.ContextFill(75.5, 75500, 100000, "ok", "")

	stats := core.SessionTokenCallStats{
		MedianOutputTokPerSec:  42.5,
		TokSSamples:            7,
		MedianCallMillis:       910.5,
		MeanCallMillis:         980.25,
		MaxCallMillis:          1200,
		CallCount:              9,
		CachedInputTokensTotal: 5400,
		CacheWriteTokensTotal:  300,
		CacheHitPercent:        62.5,
	}
	emitter.EmitSessionTokensWithStats(1200, 300, "test-model", "test-family", stats)

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
	if data.MedianCallMillis != 910.5 || data.MeanCallMillis != 980.25 || data.MaxCallMillis != 1200 {
		t.Errorf("latency aggregates = %v/%v/%v, want 910.5/980.25/1200", data.MedianCallMillis, data.MeanCallMillis, data.MaxCallMillis)
	}
	if data.CallCount != 9 {
		t.Errorf("CallCount = %d, want 9", data.CallCount)
	}
	if data.CachedInputTokensTotal != 5400 || data.CacheWriteTokensTotal != 300 || data.CacheHitPercent != 62.5 {
		t.Errorf("cache aggregates = %d/%d/%v, want 5400/300/62.5", data.CachedInputTokensTotal, data.CacheWriteTokensTotal, data.CacheHitPercent)
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

	// The aggregates are mirrored into the next agent_metrics payload
	// (session-scoped values, not per-run deltas).
	got := emitter.EmitAgentMetrics("full")
	if got.LLMCallCount != 9 || got.CachedInputTokensTotal != 5400 || got.CacheWriteTokensTotal != 300 {
		t.Errorf("agent_metrics cache mirror = %d/%d/%d, want 9/5400/300", got.LLMCallCount, got.CachedInputTokensTotal, got.CacheWriteTokensTotal)
	}
	if got.CacheHitPercent != 62.5 {
		t.Errorf("agent_metrics cache_hit_percent = %v, want 62.5", got.CacheHitPercent)
	}
	if got.MedianCallMillis != 910.5 || got.MeanCallMillis != 980.25 || got.MaxCallMillis != 1200 {
		t.Errorf("agent_metrics latency mirror = %v/%v/%v, want 910.5/980.25/1200", got.MedianCallMillis, got.MeanCallMillis, got.MaxCallMillis)
	}
}

// TestSessionTokensEventData_OmitEmptyWireShape verifies the wire contract:
// until the windows warm up and providers report cache detail, the new fields
// are absent from the JSON payload — legacy consumers never see them.
func TestSessionTokensEventData_OmitEmptyWireShape(t *testing.T) {
	warm := SessionTokensEventData{
		SessionInputTokens:     10,
		MedianOutputTokS:       12.5,
		TokSSamples:            3,
		MedianCallMillis:       812.5,
		MeanCallMillis:         900,
		MaxCallMillis:          1500,
		CallCount:              5,
		CachedInputTokensTotal: 4200,
		CacheWriteTokensTotal:  600,
		CacheHitPercent:        87.5,
	}
	b, err := json.Marshal(warm)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"median_output_tok_s":12.5`, `"tok_s_samples":3`,
		`"median_call_ms":812.5`, `"mean_call_ms":900`, `"max_call_ms":1500`, `"call_count":5`,
		`"cached_input_tokens_total":4200`, `"cache_write_tokens_total":600`, `"cache_hit_percent":87.5`,
	} {
		if !strings.Contains(string(b), key) {
			t.Errorf("warm payload missing %s: %s", key, b)
		}
	}

	cold := SessionTokensEventData{SessionInputTokens: 10}
	b, err = json.Marshal(cold)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		"median_output_tok_s", "tok_s_samples",
		"median_call_ms", "mean_call_ms", "max_call_ms", "call_count",
		"cached_input_tokens_total", "cache_write_tokens_total", "cache_hit_percent",
	} {
		if strings.Contains(string(b), key) {
			t.Errorf("cold payload must omit %s: %s", key, b)
		}
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
	if data.MedianCallMillis != 0 || data.MeanCallMillis != 0 || data.MaxCallMillis != 0 || data.CallCount != 0 {
		t.Errorf("latency fields must stay zero for legacy payloads, got %v/%v/%v/%d", data.MedianCallMillis, data.MeanCallMillis, data.MaxCallMillis, data.CallCount)
	}
	if data.CachedInputTokensTotal != 0 || data.CacheWriteTokensTotal != 0 || data.CacheHitPercent != 0 {
		t.Errorf("cache fields must stay zero for legacy payloads, got %d/%d/%v", data.CachedInputTokensTotal, data.CacheWriteTokensTotal, data.CacheHitPercent)
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
	if data.CallCount != 0 || data.CachedInputTokensTotal != 0 || data.MedianCallMillis != 0 {
		t.Errorf("latency/cache fields must stay zero for legacy payloads, got %d/%d/%v", data.CallCount, data.CachedInputTokensTotal, data.MedianCallMillis)
	}
}

// TestEventPersister_SkipsSessionTokensWithStats pins that the enriched
// session_tokens event remains transient: the persister must not write message
// rows for it, regardless of the payload's new fields.
func TestEventPersister_SkipsSessionTokensWithStats(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	p.Persist(Event{
		SessionID: "s1",
		Type:      "session_tokens",
		Data: SessionTokensEventData{
			SessionInputTokens: 100, SessionOutputTokens: 50,
			Model: "m", Family: "f",
			MedianOutputTokS: 42.5, TokSSamples: 9,
			MedianCallMillis: 812.5, MeanCallMillis: 900, MaxCallMillis: 1500, CallCount: 9,
			CachedInputTokensTotal: 4200, CacheWriteTokensTotal: 600, CacheHitPercent: 87.5,
		},
	})
	// Close drains the single-writer queue, so any wrongly-enqueued write
	// would be visible in store.messages here.
	p.Close()

	if len(store.messages) != 0 {
		t.Errorf("session_tokens must stay transient; persister wrote %d rows: %+v", len(store.messages), store.messages)
	}
}
