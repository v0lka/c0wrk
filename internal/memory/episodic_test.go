package memory

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestEpisodicMemory_Cleanup(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	em, err := NewEpisodicMemory(db)
	if err != nil {
		t.Fatalf("failed to create episodic memory: %v", err)
	}

	ctx := context.Background()

	// Store an "old" entry (30 days ago)
	oldEntry := EpisodicEntry{
		SessionID:   "session-old",
		UserMessage: "Old task",
		Summary:     "Old entry summary",
		Timestamp:   time.Now().Add(-30 * 24 * time.Hour),
	}
	if err := em.StoreEntry(ctx, oldEntry); err != nil {
		t.Fatalf("failed to store old entry: %v", err)
	}

	// Store a "new" entry (now)
	newEntry := EpisodicEntry{
		SessionID:   "session-new",
		UserMessage: "New task",
		Summary:     "New entry summary",
		Timestamp:   time.Now(),
	}
	if err := em.StoreEntry(ctx, newEntry); err != nil {
		t.Fatalf("failed to store new entry: %v", err)
	}

	// Cleanup entries older than 7 days
	if err := em.Cleanup(ctx, 7*24*time.Hour); err != nil {
		t.Fatalf("failed to cleanup: %v", err)
	}

	// Only the new entry should remain
	results, err := em.RetrieveEntries(ctx, "session-new", 10)
	if err != nil {
		t.Fatalf("failed to retrieve entries: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result after cleanup, got %d", len(results))
	}

	if results[0].Summary != "New entry summary" {
		t.Errorf("expected new entry to remain, got %q", results[0].Summary)
	}

	// Old entries should be gone
	oldResults, err := em.RetrieveEntries(ctx, "session-old", 10)
	if err != nil {
		t.Fatalf("failed to retrieve old entries: %v", err)
	}
	if len(oldResults) != 0 {
		t.Errorf("expected 0 old entries after cleanup, got %d", len(oldResults))
	}
}

func TestEpisodicMemory_AutoCreateTables(t *testing.T) {
	// Open fresh DB - tables should be auto-created
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	em, err := NewEpisodicMemory(db)
	if err != nil {
		t.Fatalf("failed to create episodic memory: %v", err)
	}

	ctx := context.Background()

	// Operations should work immediately without manual table creation
	entry := EpisodicEntry{
		SessionID:   "session-test",
		UserMessage: "Auto table test task",
		Summary:     "Auto table test",
		Timestamp:   time.Now(),
	}

	if err := em.StoreEntry(ctx, entry); err != nil {
		t.Fatalf("store failed on fresh DB: %v", err)
	}

	results, err := em.RetrieveEntries(ctx, "session-test", 10)
	if err != nil {
		t.Fatalf("retrieve failed on fresh DB: %v", err)
	}

	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}

	// Cleanup should also work
	if err := em.Cleanup(ctx, time.Hour); err != nil {
		t.Fatalf("cleanup failed on fresh DB: %v", err)
	}
}

func TestEpisodicMemory_Count(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	em, err := NewEpisodicMemory(db)
	if err != nil {
		t.Fatalf("failed to create episodic memory: %v", err)
	}

	ctx := context.Background()

	// Test Count on empty DB
	count, err := em.Count(ctx)
	if err != nil {
		t.Fatalf("failed to count on empty DB: %v", err)
	}
	if count != 0 {
		t.Errorf("expected count 0 on empty DB, got %d", count)
	}

	// Store a few entries
	for i := 0; i < 3; i++ {
		entry := EpisodicEntry{
			SessionID:     "session-test",
			UserMessage:   "Test message",
			Summary:       "Test summary",
			EvalPassCount: i,
			EvalTotalCount: 3,
			Timestamp:     time.Now(),
		}
		if err := em.StoreEntry(ctx, entry); err != nil {
			t.Fatalf("failed to store entry %d: %v", i, err)
		}
	}

	// Test Count returns correct number
	count, err = em.Count(ctx)
	if err != nil {
		t.Fatalf("failed to count after storing: %v", err)
	}
	if count != 3 {
		t.Errorf("expected count 3, got %d", count)
	}
}

func TestStoreEntry(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	em, err := NewEpisodicMemory(db)
	if err != nil {
		t.Fatalf("failed to create episodic memory: %v", err)
	}

	ctx := context.Background()

	// Store an entry with all fields populated
	entry := EpisodicEntry{
		SessionID:      "session-123",
		UserMessage:    "Fix the failing unit tests",
		Summary:        "Added missing import statement",
		Mode:           "react",
		ToolsUsed:      []string{"bash_exec", "file_ops"},
		Success:        true,
		EvalPassCount:  3,
		EvalTotalCount: 3,
		Timestamp:      time.Now(),
	}

	err = em.StoreEntry(ctx, entry)
	if err != nil {
		t.Fatalf("failed to store entry: %v", err)
	}

	// Retrieve it via RetrieveEntries
	results, err := em.RetrieveEntries(ctx, "session-123", 10)
	if err != nil {
		t.Fatalf("failed to retrieve entries: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	// Verify all fields roundtrip correctly
	r := results[0]
	if r.SessionID != entry.SessionID {
		t.Errorf("SessionID mismatch: got %q, want %q", r.SessionID, entry.SessionID)
	}
	if r.UserMessage != entry.UserMessage {
		t.Errorf("UserMessage mismatch: got %q, want %q", r.UserMessage, entry.UserMessage)
	}
	if r.Summary != entry.Summary {
		t.Errorf("Summary mismatch: got %q, want %q", r.Summary, entry.Summary)
	}
	if r.Mode != entry.Mode {
		t.Errorf("Mode mismatch: got %q, want %q", r.Mode, entry.Mode)
	}
	if len(r.ToolsUsed) != len(entry.ToolsUsed) {
		t.Errorf("ToolsUsed count mismatch: got %d, want %d", len(r.ToolsUsed), len(entry.ToolsUsed))
	} else {
		for i, tool := range entry.ToolsUsed {
			if r.ToolsUsed[i] != tool {
				t.Errorf("ToolsUsed[%d] mismatch: got %q, want %q", i, r.ToolsUsed[i], tool)
			}
		}
	}
	if r.Success != entry.Success {
		t.Errorf("Success mismatch: got %v, want %v", r.Success, entry.Success)
	}
	if r.EvalPassCount != entry.EvalPassCount {
		t.Errorf("EvalPassCount mismatch: got %d, want %d", r.EvalPassCount, entry.EvalPassCount)
	}
	if r.EvalTotalCount != entry.EvalTotalCount {
		t.Errorf("EvalTotalCount mismatch: got %d, want %d", r.EvalTotalCount, entry.EvalTotalCount)
	}
}

func TestRetrieveEntries_BySession(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	em, err := NewEpisodicMemory(db)
	if err != nil {
		t.Fatalf("failed to create episodic memory: %v", err)
	}

	ctx := context.Background()

	// Store entries for two different session IDs
	entryA1 := EpisodicEntry{
		SessionID:   "session-A",
		UserMessage: "Task A1",
		Summary:     "Summary A1",
		Timestamp:   time.Now(),
	}
	entryA2 := EpisodicEntry{
		SessionID:   "session-A",
		UserMessage: "Task A2",
		Summary:     "Summary A2",
		Timestamp:   time.Now().Add(time.Second),
	}
	entryB1 := EpisodicEntry{
		SessionID:   "session-B",
		UserMessage: "Task B1",
		Summary:     "Summary B1",
		Timestamp:   time.Now(),
	}

	if err := em.StoreEntry(ctx, entryA1); err != nil {
		t.Fatalf("failed to store entryA1: %v", err)
	}
	if err := em.StoreEntry(ctx, entryA2); err != nil {
		t.Fatalf("failed to store entryA2: %v", err)
	}
	if err := em.StoreEntry(ctx, entryB1); err != nil {
		t.Fatalf("failed to store entryB1: %v", err)
	}

	// Retrieve for session A — verify only session A entries returned
	resultsA, err := em.RetrieveEntries(ctx, "session-A", 10)
	if err != nil {
		t.Fatalf("failed to retrieve entries for session A: %v", err)
	}
	if len(resultsA) != 2 {
		t.Errorf("expected 2 results for session A, got %d", len(resultsA))
	}
	for _, r := range resultsA {
		if r.SessionID != "session-A" {
			t.Errorf("expected session-A, got %s", r.SessionID)
		}
	}

	// Retrieve for session B — verify only session B entries returned
	resultsB, err := em.RetrieveEntries(ctx, "session-B", 10)
	if err != nil {
		t.Fatalf("failed to retrieve entries for session B: %v", err)
	}
	if len(resultsB) != 1 {
		t.Errorf("expected 1 result for session B, got %d", len(resultsB))
	}
	if resultsB[0].SessionID != "session-B" {
		t.Errorf("expected session-B, got %s", resultsB[0].SessionID)
	}

	// Retrieve for unknown session — verify empty list (not nil)
	resultsUnknown, err := em.RetrieveEntries(ctx, "session-unknown", 10)
	if err != nil {
		t.Fatalf("failed to retrieve entries for unknown session: %v", err)
	}
	if resultsUnknown == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(resultsUnknown) != 0 {
		t.Errorf("expected 0 results for unknown session, got %d", len(resultsUnknown))
	}
}

func TestRetrieveEntries_Limit(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	em, err := NewEpisodicMemory(db)
	if err != nil {
		t.Fatalf("failed to create episodic memory: %v", err)
	}

	ctx := context.Background()

	// Store 5 entries for a session
	for i := 0; i < 5; i++ {
		entry := EpisodicEntry{
			SessionID:     "session-test",
			UserMessage:   "Task",
			Summary:       "Summary",
			EvalPassCount: i, // Use this to identify order
			Timestamp:     time.Now().Add(time.Duration(i) * time.Second),
		}
		if err := em.StoreEntry(ctx, entry); err != nil {
			t.Fatalf("failed to store entry %d: %v", i, err)
		}
	}

	// Retrieve with limit=3
	results, err := em.RetrieveEntries(ctx, "session-test", 3)
	if err != nil {
		t.Fatalf("failed to retrieve entries: %v", err)
	}

	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}

	// Verify they are the most recent (ordered by created_at DESC)
	// The most recent has EvalPassCount=4, then 3, then 2
	if results[0].EvalPassCount != 4 {
		t.Errorf("expected most recent entry first (EvalPassCount=4), got %d", results[0].EvalPassCount)
	}
	if results[1].EvalPassCount != 3 {
		t.Errorf("expected second most recent (EvalPassCount=3), got %d", results[1].EvalPassCount)
	}
	if results[2].EvalPassCount != 2 {
		t.Errorf("expected third most recent (EvalPassCount=2), got %d", results[2].EvalPassCount)
	}
}

func TestCount_EntriesOnly(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	em, err := NewEpisodicMemory(db)
	if err != nil {
		t.Fatalf("failed to create episodic memory: %v", err)
	}

	ctx := context.Background()

	// Store 3 entries
	for i := 0; i < 3; i++ {
		entry := EpisodicEntry{
			SessionID:   "session-1",
			UserMessage: "Task",
			Summary:     "Entry summary",
			Timestamp:   time.Now(),
		}
		if err := em.StoreEntry(ctx, entry); err != nil {
			t.Fatalf("failed to store entry %d: %v", i, err)
		}
	}

	// Call Count() — verify returns 3
	count, err := em.Count(ctx)
	if err != nil {
		t.Fatalf("failed to count: %v", err)
	}
	if count != 3 {
		t.Errorf("expected count 3 (entries only), got %d", count)
	}
}

func TestCountBySession(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	em, err := NewEpisodicMemory(db)
	if err != nil {
		t.Fatalf("failed to create episodic memory: %v", err)
	}

	ctx := context.Background()

	// Store entries for two different sessions
	// Session A: 2 entries
	for i := 0; i < 2; i++ {
		entry := EpisodicEntry{
			SessionID:   "session-A",
			UserMessage: "Task A",
			Summary:     "Entry A",
			Timestamp:   time.Now(),
		}
		if err := em.StoreEntry(ctx, entry); err != nil {
			t.Fatalf("failed to store entryA %d: %v", i, err)
		}
	}

	// Session B: 1 entry
	entryB := EpisodicEntry{
		SessionID:   "session-B",
		UserMessage: "Task B",
		Summary:     "Entry B",
		Timestamp:   time.Now(),
	}
	if err := em.StoreEntry(ctx, entryB); err != nil {
		t.Fatalf("failed to store entryB: %v", err)
	}

	// Call CountBySession for each — verify correct counts
	countA, err := em.CountBySession(ctx, "session-A")
	if err != nil {
		t.Fatalf("failed to count session-A: %v", err)
	}
	if countA != 2 {
		t.Errorf("expected count 2 for session-A, got %d", countA)
	}

	countB, err := em.CountBySession(ctx, "session-B")
	if err != nil {
		t.Fatalf("failed to count session-B: %v", err)
	}
	if countB != 1 {
		t.Errorf("expected count 1 for session-B, got %d", countB)
	}

	// Call CountBySession for unknown session — verify returns 0
	countUnknown, err := em.CountBySession(ctx, "session-unknown")
	if err != nil {
		t.Fatalf("failed to count unknown session: %v", err)
	}
	if countUnknown != 0 {
		t.Errorf("expected count 0 for unknown session, got %d", countUnknown)
	}
}



func TestCleanup_IncludesEntries(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	em, err := NewEpisodicMemory(db)
	if err != nil {
		t.Fatalf("failed to create episodic memory: %v", err)
	}

	ctx := context.Background()

	// Store entries with old timestamps (30 days ago)
	oldEntry := EpisodicEntry{
		SessionID:   "session-old",
		UserMessage: "Old task",
		Summary:     "Old entry summary",
		Timestamp:   time.Now().Add(-30 * 24 * time.Hour),
	}
	if err := em.StoreEntry(ctx, oldEntry); err != nil {
		t.Fatalf("failed to store old entry: %v", err)
	}

	// Store entries with new timestamps (now)
	newEntry := EpisodicEntry{
		SessionID:   "session-new",
		UserMessage: "New task",
		Summary:     "New entry summary",
		Timestamp:   time.Now(),
	}
	if err := em.StoreEntry(ctx, newEntry); err != nil {
		t.Fatalf("failed to store new entry: %v", err)
	}

	// Cleanup items older than 7 days
	if err := em.Cleanup(ctx, 7*24*time.Hour); err != nil {
		t.Fatalf("failed to cleanup: %v", err)
	}

	// Verify old entries are gone
	oldEntries, err := em.RetrieveEntries(ctx, "session-old", 10)
	if err != nil {
		t.Fatalf("failed to retrieve old entries: %v", err)
	}
	if len(oldEntries) != 0 {
		t.Errorf("expected 0 old entries after cleanup, got %d", len(oldEntries))
	}

	// Verify new entries remain
	newEntries, err := em.RetrieveEntries(ctx, "session-new", 10)
	if err != nil {
		t.Fatalf("failed to retrieve new entries: %v", err)
	}
	if len(newEntries) != 1 {
		t.Errorf("expected 1 new entry after cleanup, got %d", len(newEntries))
	}
}
