package memory

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestReflexionMemory_StoreAndSearch(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	rm, err := NewReflexionMemory(db)
	if err != nil {
		t.Fatalf("failed to create reflexion memory: %v", err)
	}

	ctx := context.Background()

	reflection := StoredReflexion{
		TaskDescription: "Fix failing unit tests in user service",
		Summary:         "Test failed due to missing import",
		Hypotheses:      []string{"Missing dependency", "Wrong package name"},
		SuggestedAction: "Add the missing import statement",
		Timestamp:       time.Now(),
	}

	err = rm.Store(ctx, reflection)
	if err != nil {
		t.Fatalf("failed to store reflection: %v", err)
	}

	// Search by similar task description
	results, err := rm.Search(ctx, "unit tests user service", 10)
	if err != nil {
		t.Fatalf("failed to search reflections: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.Summary != reflection.Summary {
		t.Errorf("summary mismatch: got %q, want %q", r.Summary, reflection.Summary)
	}
	if len(r.Hypotheses) != 2 {
		t.Errorf("hypotheses count mismatch: got %d, want 2", len(r.Hypotheses))
	}
	if r.SuggestedAction != reflection.SuggestedAction {
		t.Errorf("suggested action mismatch: got %q, want %q", r.SuggestedAction, reflection.SuggestedAction)
	}
	if r.TaskDescription != reflection.TaskDescription {
		t.Errorf("task description mismatch: got %q, want %q", r.TaskDescription, reflection.TaskDescription)
	}
}

func TestReflexionMemory_SearchCrossSession(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	rm, err := NewReflexionMemory(db)
	if err != nil {
		t.Fatalf("failed to create reflexion memory: %v", err)
	}

	ctx := context.Background()

	// Store reflections that would conceptually come from different sessions
	// Since ReflexionMemory is cross-session, it doesn't have session_id
	// We store multiple reflections with similar keywords to test cross-session retrieval
	reflections := []StoredReflexion{
		{
			TaskDescription: "Fix database connection timeout in production",
			Summary:         "Connection pool was exhausted",
			Hypotheses:      []string{"Pool size too small", "Leaked connections"},
			SuggestedAction: "Increase pool size and fix connection leaks",
			Timestamp:       time.Now().Add(-2 * time.Hour),
		},
		{
			TaskDescription: "Optimize database queries for dashboard",
			Summary:         "N+1 query problem detected",
			Hypotheses:      []string{"Missing eager loading", "Inefficient joins"},
			SuggestedAction: "Add eager loading for related entities",
			Timestamp:       time.Now().Add(-1 * time.Hour),
		},
		{
			TaskDescription: "Fix authentication flow bug",
			Summary:         "Token validation was failing",
			Hypotheses:      []string{"Clock skew", "Wrong secret key"},
			SuggestedAction: "Sync server times and verify secret",
			Timestamp:       time.Now(),
		},
	}

	for i, reflection := range reflections {
		if err := rm.Store(ctx, reflection); err != nil {
			t.Fatalf("failed to store reflection %d: %v", i, err)
		}
	}

	// Search for "database" - should match 2 reflections (cross-session)
	results, err := rm.Search(ctx, "database issues", 10)
	if err != nil {
		t.Fatalf("failed to search reflections: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("expected 2 results matching 'database', got %d", len(results))
	}

	// Verify results are ordered by recency (most recent first)
	// The most recent database reflection should be first
	if len(results) > 0 && results[0].Summary != "N+1 query problem detected" {
		t.Errorf("expected most recent database reflection first, got %q", results[0].Summary)
	}
}

func TestReflexionMemory_Count(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	rm, err := NewReflexionMemory(db)
	if err != nil {
		t.Fatalf("failed to create reflexion memory: %v", err)
	}

	ctx := context.Background()

	// Test Count on empty DB
	count, err := rm.Count(ctx)
	if err != nil {
		t.Fatalf("failed to count on empty DB: %v", err)
	}
	if count != 0 {
		t.Errorf("expected count 0 on empty DB, got %d", count)
	}

	// Store a few reflections
	for i := 0; i < 3; i++ {
		reflection := StoredReflexion{
			TaskDescription: "fix database connection issue",
			Summary:         "Test summary",
			Hypotheses:      []string{"hypothesis"},
			Timestamp:       time.Now(),
		}
		if err := rm.Store(ctx, reflection); err != nil {
			t.Fatalf("failed to store reflection %d: %v", i, err)
		}
	}

	// Test Count returns correct number
	count, err = rm.Count(ctx)
	if err != nil {
		t.Fatalf("failed to count after storing: %v", err)
	}
	if count != 3 {
		t.Errorf("expected count 3, got %d", count)
	}
}

func TestReflexionMemory_EmptySearch(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	rm, err := NewReflexionMemory(db)
	if err != nil {
		t.Fatalf("failed to create reflexion memory: %v", err)
	}

	ctx := context.Background()

	// Search from empty DB
	results, err := rm.Search(ctx, "any query", 10)
	if err != nil {
		t.Fatalf("failed to search from empty DB: %v", err)
	}

	if results == nil {
		t.Error("expected empty slice, got nil")
	}

	if len(results) != 0 {
		t.Errorf("expected 0 results from empty DB, got %d", len(results))
	}
}

func TestReflexionMemory_SearchLimit(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	rm, err := NewReflexionMemory(db)
	if err != nil {
		t.Fatalf("failed to create reflexion memory: %v", err)
	}

	ctx := context.Background()

	// Store multiple reflections with similar task descriptions
	for i := 0; i < 5; i++ {
		reflection := StoredReflexion{
			TaskDescription: "fix database connection issue",
			Summary:         "Test summary",
			Hypotheses:      []string{"hypothesis"},
			Timestamp:       time.Now().Add(time.Duration(i) * time.Second),
		}
		if err := rm.Store(ctx, reflection); err != nil {
			t.Fatalf("failed to store reflection %d: %v", i, err)
		}
	}

	// Search with limit of 3
	results, err := rm.Search(ctx, "database connection", 3)
	if err != nil {
		t.Fatalf("failed to search reflections: %v", err)
	}

	if len(results) != 3 {
		t.Errorf("expected 3 results (limit), got %d", len(results))
	}

	// Should be ordered by created_at DESC (most recent first)
	// Timestamps are increasing, so last stored is most recent
	if len(results) > 0 && results[0].Timestamp.Before(results[len(results)-1].Timestamp) {
		t.Error("expected results ordered by recency (most recent first)")
	}
}

func TestReflexionMemory_SearchByKeyword(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	rm, err := NewReflexionMemory(db)
	if err != nil {
		t.Fatalf("failed to create reflexion memory: %v", err)
	}

	ctx := context.Background()

	// Store reflections with different task descriptions
	tasks := []string{
		"implement user authentication",
		"fix payment processing bug",
		"add user profile feature",
	}

	for i, task := range tasks {
		reflection := StoredReflexion{
			TaskDescription: task,
			Summary:         "Summary for " + task,
			Timestamp:       time.Now(),
		}
		if err := rm.Store(ctx, reflection); err != nil {
			t.Fatalf("failed to store reflection %d: %v", i, err)
		}
	}

	// Search for "user" - should match 2 tasks
	results, err := rm.Search(ctx, "user related tasks", 10)
	if err != nil {
		t.Fatalf("failed to search reflections: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("expected 2 results matching 'user', got %d", len(results))
	}

	// Search for "payment" - should match 1 task
	results, err = rm.Search(ctx, "payment issues", 10)
	if err != nil {
		t.Fatalf("failed to search reflections: %v", err)
	}

	if len(results) != 1 {
		t.Errorf("expected 1 result matching 'payment', got %d", len(results))
	}

	// Search for "nonexistent" - should match 0 tasks
	results, err = rm.Search(ctx, "xyznonexistent foobar", 10)
	if err != nil {
		t.Fatalf("failed to search reflections: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("expected 0 results for nonexistent keyword, got %d", len(results))
	}
}

func TestReflexionMemory_NullHypotheses(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	rm, err := NewReflexionMemory(db)
	if err != nil {
		t.Fatalf("failed to create reflexion memory: %v", err)
	}

	ctx := context.Background()

	// Store reflection with nil hypotheses
	reflection := StoredReflexion{
		TaskDescription: "nil hypotheses test",
		Summary:         "Test with nil hypotheses",
		Hypotheses:      nil,
		Timestamp:       time.Now(),
	}

	if err := rm.Store(ctx, reflection); err != nil {
		t.Fatalf("failed to store reflection: %v", err)
	}

	results, err := rm.Search(ctx, "hypotheses test", 10)
	if err != nil {
		t.Fatalf("failed to search reflections: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	// Hypotheses should be nil or empty, not cause an error
	if len(results[0].Hypotheses) != 0 {
		t.Errorf("expected nil or empty hypotheses, got %v", results[0].Hypotheses)
	}
}

func TestReflexionMemory_AutoCreateTables(t *testing.T) {
	// Open fresh DB - tables should be auto-created
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	rm, err := NewReflexionMemory(db)
	if err != nil {
		t.Fatalf("failed to create reflexion memory: %v", err)
	}

	ctx := context.Background()

	// Operations should work immediately without manual table creation
	reflection := StoredReflexion{
		TaskDescription: "auto table test task",
		Summary:         "Auto table test",
		Hypotheses:      []string{"h1", "h2"},
		SuggestedAction: "action",
		Timestamp:       time.Now(),
	}

	if err := rm.Store(ctx, reflection); err != nil {
		t.Fatalf("store failed on fresh DB: %v", err)
	}

	results, err := rm.Search(ctx, "auto table test", 10)
	if err != nil {
		t.Fatalf("search failed on fresh DB: %v", err)
	}

	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
}

func TestReflexionMemory_EmptyQuery(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	rm, err := NewReflexionMemory(db)
	if err != nil {
		t.Fatalf("failed to create reflexion memory: %v", err)
	}

	ctx := context.Background()

	// Store a reflection
	reflection := StoredReflexion{
		TaskDescription: "test task",
		Summary:         "Test summary",
		Timestamp:       time.Now(),
	}
	if err := rm.Store(ctx, reflection); err != nil {
		t.Fatalf("failed to store reflection: %v", err)
	}

	// Search with empty query (all stop words)
	results, err := rm.Search(ctx, "a the is and or", 10)
	if err != nil {
		t.Fatalf("failed to search with empty query: %v", err)
	}

	// Should return empty slice (no keywords to match)
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty query, got %d", len(results))
	}
}

func TestReflexionMemory_DefaultTimestamp(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	rm, err := NewReflexionMemory(db)
	if err != nil {
		t.Fatalf("failed to create reflexion memory: %v", err)
	}

	ctx := context.Background()

	// Store reflection with zero timestamp (should default to now)
	reflection := StoredReflexion{
		TaskDescription: "default timestamp test",
		Summary:         "Test summary",
		Timestamp:       time.Time{}, // Zero value
	}

	if err := rm.Store(ctx, reflection); err != nil {
		t.Fatalf("failed to store reflection: %v", err)
	}

	results, err := rm.Search(ctx, "timestamp test", 10)
	if err != nil {
		t.Fatalf("failed to search reflections: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	// Timestamp should not be zero
	if results[0].Timestamp.IsZero() {
		t.Error("expected timestamp to be set, got zero value")
	}
}
