package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/user/agent/internal/session"
	"github.com/user/agent/internal/tools"
)

// mockSemanticStore is a mock implementation of SemanticStore.
type mockSemanticStore struct {
	storeCalled  bool
	storeKey     string
	storeContent string
	storeMeta    map[string]string
	storeErr     error

	searchCalled  bool
	searchQuery   string
	searchTopK    int
	searchResults []SemanticSearchResult
	searchErr     error
}

func (m *mockSemanticStore) Store(ctx context.Context, key, content string, metadata map[string]string) error {
	m.storeCalled = true
	m.storeKey = key
	m.storeContent = content
	m.storeMeta = metadata
	return m.storeErr
}

func (m *mockSemanticStore) Search(ctx context.Context, query string, topK int) ([]SemanticSearchResult, error) {
	m.searchCalled = true
	m.searchQuery = query
	m.searchTopK = topK
	return m.searchResults, m.searchErr
}

// mockCompactionSwitcher is a mock implementation of CompactionSwitcher.
type mockCompactionSwitcher struct {
	setCalled   bool
	setStrategy string
	setErr      error
}

func (m *mockCompactionSwitcher) SetStrategy(strategy string) error {
	m.setCalled = true
	m.setStrategy = strategy
	return m.setErr
}

func TestContextManagerTool_Descriptor(t *testing.T) {
	tool := NewContextManagerTool(nil, nil, nil, nil)

	// Test Name
	if got := tool.Name(); got != "context_manager" {
		t.Errorf("Name() = %q, want %q", got, "context_manager")
	}

	// Test Description
	desc := tool.Description()
	if desc == "" {
		t.Error("Description() should not be empty")
	}
	if !strings.Contains(desc, "semantic memory") {
		t.Error("Description() should mention semantic memory")
	}

	// Test InputSchema
	schema := tool.InputSchema()
	if len(schema) == 0 {
		t.Error("InputSchema() should not be empty")
	}

	var schemaMap map[string]interface{}
	if err := json.Unmarshal(schema, &schemaMap); err != nil {
		t.Errorf("InputSchema() is not valid JSON: %v", err)
	}

	// Verify schema has required structure
	if schemaMap["type"] != "object" {
		t.Error("InputSchema() should have type 'object'")
	}

	props, ok := schemaMap["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("InputSchema() should have 'properties'")
	}

	// Verify action property exists
	action, ok := props["action"].(map[string]interface{})
	if !ok {
		t.Fatal("InputSchema() should have 'action' property")
	}

	// Verify action enum values
	enum, ok := action["enum"].([]interface{})
	if !ok {
		t.Fatal("action property should have 'enum'")
	}

	expectedActions := map[string]bool{
		"memory_store":      false,
		"memory_search":     false,
		"switch_compaction": false,
	}
	for _, v := range enum {
		if s, ok := v.(string); ok {
			expectedActions[s] = true
		}
	}
	for action, found := range expectedActions {
		if !found {
			t.Errorf("InputSchema() action enum should include %q", action)
		}
	}
}

func TestContextManagerTool_MemoryStore(t *testing.T) {
	store := &mockSemanticStore{}
	tool := NewContextManagerTool(store, nil, nil, nil)

	input := map[string]interface{}{
		"action":  "memory_store",
		"key":     "test-key",
		"content": "test content",
		"metadata": map[string]string{
			"source": "test",
		},
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.IsError {
		t.Errorf("Execute() returned error result: %s", result.Content)
	}

	if !store.storeCalled {
		t.Error("Store() was not called")
	}

	if store.storeKey != "test-key" {
		t.Errorf("Store() key = %q, want %q", store.storeKey, "test-key")
	}

	if store.storeContent != "test content" {
		t.Errorf("Store() content = %q, want %q", store.storeContent, "test content")
	}

	if !strings.Contains(result.Content, "successfully stored") {
		t.Errorf("Result should indicate success, got: %s", result.Content)
	}
}

func TestContextManagerTool_MemorySearch(t *testing.T) {
	store := &mockSemanticStore{
		searchResults: []SemanticSearchResult{
			{Key: "key1", Content: "content1", Score: 0.95},
			{Key: "key2", Content: "content2", Score: 0.85},
		},
	}
	tool := NewContextManagerTool(store, nil, nil, nil)

	input := map[string]interface{}{
		"action": "memory_search",
		"query":  "test query",
		"top_k":  3,
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.IsError {
		t.Errorf("Execute() returned error result: %s", result.Content)
	}

	if !store.searchCalled {
		t.Error("Search() was not called")
	}

	if store.searchQuery != "test query" {
		t.Errorf("Search() query = %q, want %q", store.searchQuery, "test query")
	}

	if store.searchTopK != 3 {
		t.Errorf("Search() topK = %d, want %d", store.searchTopK, 3)
	}

	// Verify formatted output
	if !strings.Contains(result.Content, "Found 2 results") {
		t.Errorf("Result should indicate found results, got: %s", result.Content)
	}

	if !strings.Contains(result.Content, "key1") {
		t.Errorf("Result should contain key1, got: %s", result.Content)
	}

	if !strings.Contains(result.Content, "0.95") {
		t.Errorf("Result should contain score, got: %s", result.Content)
	}
}

func TestContextManagerTool_MemorySearchDefaultTopK(t *testing.T) {
	store := &mockSemanticStore{}
	tool := NewContextManagerTool(store, nil, nil, nil)

	input := map[string]interface{}{
		"action": "memory_search",
		"query":  "test query",
		// no top_k specified
	}
	inputJSON, _ := json.Marshal(input)

	_, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if store.searchTopK != 5 {
		t.Errorf("Search() should use default topK = 5, got %d", store.searchTopK)
	}
}

func TestContextManagerTool_MemorySearchNoResults(t *testing.T) {
	store := &mockSemanticStore{
		searchResults: []SemanticSearchResult{},
	}
	tool := NewContextManagerTool(store, nil, nil, nil)

	input := map[string]interface{}{
		"action": "memory_search",
		"query":  "nothing matches",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.IsError {
		t.Errorf("Execute() returned error result for no results: %s", result.Content)
	}

	if !strings.Contains(result.Content, "no results found") {
		t.Errorf("Result should indicate no results, got: %s", result.Content)
	}
}

func TestContextManagerTool_SwitchCompaction(t *testing.T) {
	switcher := &mockCompactionSwitcher{}
	tool := NewContextManagerTool(nil, switcher, nil, nil)

	strategies := []string{"sliding_window", "summarization", "hierarchical"}

	for _, strategy := range strategies {
		switcher.setCalled = false
		switcher.setStrategy = ""

		input := map[string]interface{}{
			"action":   "switch_compaction",
			"strategy": strategy,
		}
		inputJSON, _ := json.Marshal(input)

		result, err := tool.Execute(context.Background(), inputJSON)
		if err != nil {
			t.Fatalf("Execute() error = %v for strategy %s", err, strategy)
		}

		if result.IsError {
			t.Errorf("Execute() returned error result for strategy %s: %s", strategy, result.Content)
		}

		if !switcher.setCalled {
			t.Errorf("SetStrategy() was not called for %s", strategy)
		}

		if switcher.setStrategy != strategy {
			t.Errorf("SetStrategy() = %q, want %q", switcher.setStrategy, strategy)
		}

		if !strings.Contains(result.Content, "successfully switched") {
			t.Errorf("Result should indicate success for %s, got: %s", strategy, result.Content)
		}
	}
}

func TestContextManagerTool_SwitchCompactionInvalidStrategy(t *testing.T) {
	switcher := &mockCompactionSwitcher{}
	tool := NewContextManagerTool(nil, switcher, nil, nil)

	input := map[string]interface{}{
		"action":   "switch_compaction",
		"strategy": "invalid_strategy",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return error for invalid strategy")
	}

	if !strings.Contains(result.Content, "invalid strategy") {
		t.Errorf("Result should indicate invalid strategy, got: %s", result.Content)
	}

	if switcher.setCalled {
		t.Error("SetStrategy() should not be called for invalid strategy")
	}
}

func TestContextManagerTool_NilSemanticStore(t *testing.T) {
	tool := NewContextManagerTool(nil, nil, nil, nil)

	tests := []struct {
		name   string
		action string
	}{
		{"memory_store", "memory_store"},
		{"memory_search", "memory_search"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := map[string]interface{}{
				"action":  tt.action,
				"key":     "test",
				"content": "test",
				"query":   "test",
			}
			inputJSON, _ := json.Marshal(input)

			result, err := tool.Execute(context.Background(), inputJSON)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}

			if !result.IsError {
				t.Error("Execute() should return error when semantic store is nil")
			}

			if !strings.Contains(result.Content, "semantic memory is not available") {
				t.Errorf("Result should indicate semantic memory unavailable, got: %s", result.Content)
			}
		})
	}
}

func TestContextManagerTool_NilCompactionSwitcher(t *testing.T) {
	tool := NewContextManagerTool(nil, nil, nil, nil)

	input := map[string]interface{}{
		"action":   "switch_compaction",
		"strategy": "sliding_window",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return error when compaction switcher is nil")
	}

	if !strings.Contains(result.Content, "compaction switcher is not available") {
		t.Errorf("Result should indicate compaction switcher unavailable, got: %s", result.Content)
	}
}

func TestContextManagerTool_UnknownAction(t *testing.T) {
	tool := NewContextManagerTool(nil, nil, nil, nil)

	input := map[string]interface{}{
		"action": "unknown_action",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return error for unknown action")
	}

	if !strings.Contains(result.Content, "unknown action") {
		t.Errorf("Result should indicate unknown action, got: %s", result.Content)
	}
}

func TestContextManagerTool_MissingParams(t *testing.T) {
	store := &mockSemanticStore{}
	switcher := &mockCompactionSwitcher{}
	tool := NewContextManagerTool(store, switcher, nil, nil)

	tests := []struct {
		name          string
		input         map[string]interface{}
		expectedError string
	}{
		{
			name: "memory_store missing key",
			input: map[string]interface{}{
				"action":  "memory_store",
				"content": "test content",
			},
			expectedError: "missing required parameter: key",
		},
		{
			name: "memory_store missing content",
			input: map[string]interface{}{
				"action": "memory_store",
				"key":    "test-key",
			},
			expectedError: "missing required parameter: content",
		},
		{
			name: "memory_search missing query",
			input: map[string]interface{}{
				"action": "memory_search",
			},
			expectedError: "missing required parameter: query",
		},
		{
			name: "switch_compaction missing strategy",
			input: map[string]interface{}{
				"action": "switch_compaction",
			},
			expectedError: "missing required parameter: strategy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON, _ := json.Marshal(tt.input)

			result, err := tool.Execute(context.Background(), inputJSON)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}

			if !result.IsError {
				t.Error("Execute() should return error for missing params")
			}

			if !strings.Contains(result.Content, tt.expectedError) {
				t.Errorf("Result should contain %q, got: %s", tt.expectedError, result.Content)
			}
		})
	}
}

func TestContextManagerTool_InvalidJSON(t *testing.T) {
	tool := NewContextManagerTool(nil, nil, nil, nil)

	result, err := tool.Execute(context.Background(), []byte("invalid json"))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return error for invalid JSON")
	}

	if !strings.Contains(result.Content, "invalid input") {
		t.Errorf("Result should indicate invalid input, got: %s", result.Content)
	}
}

func TestContextManagerTool_DefaultPolicy(t *testing.T) {
	tool := NewContextManagerTool(nil, nil, nil, nil)
	if tool.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected DefaultPolicy() to return PolicyAlwaysAllow, got %v", tool.DefaultPolicy())
	}
}

func TestContextManagerTool_StoreError(t *testing.T) {
	store := &mockSemanticStore{
		storeErr: errors.New("database error"),
	}
	tool := NewContextManagerTool(store, nil, nil, nil)

	input := map[string]interface{}{
		"action":  "memory_store",
		"key":     "test-key",
		"content": "test content",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return error when store fails")
	}

	if !strings.Contains(result.Content, "failed to store") {
		t.Errorf("Result should indicate store failure, got: %s", result.Content)
	}
}

func TestContextManagerTool_SearchError(t *testing.T) {
	store := &mockSemanticStore{
		searchErr: errors.New("search error"),
	}
	tool := NewContextManagerTool(store, nil, nil, nil)

	input := map[string]interface{}{
		"action": "memory_search",
		"query":  "test query",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return error when search fails")
	}

	if !strings.Contains(result.Content, "failed to search") {
		t.Errorf("Result should indicate search failure, got: %s", result.Content)
	}
}

func TestContextManagerTool_SwitchCompactionError(t *testing.T) {
	switcher := &mockCompactionSwitcher{
		setErr: errors.New("switch error"),
	}
	tool := NewContextManagerTool(nil, switcher, nil, nil)

	input := map[string]interface{}{
		"action":   "switch_compaction",
		"strategy": "sliding_window",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return error when switch fails")
	}

	if !strings.Contains(result.Content, "failed to switch") {
		t.Errorf("Result should indicate switch failure, got: %s", result.Content)
	}
}

// mockEpisodicStore is a mock implementation of EpisodicStore.
type mockEpisodicStore struct {
	storeEntryCalled bool
	storedEntry      EpisodicEntry
	storeEntryErr    error

	retrieveCalled bool
	retrieveSessionID string
	retrieveLimit  int
	retrieveResults []EpisodicEntry
	retrieveErr    error
}

func (m *mockEpisodicStore) StoreEntry(ctx context.Context, entry EpisodicEntry) error {
	m.storeEntryCalled = true
	m.storedEntry = entry
	return m.storeEntryErr
}

func (m *mockEpisodicStore) RetrieveEntries(ctx context.Context, sessionID string, limit int) ([]EpisodicEntry, error) {
	m.retrieveCalled = true
	m.retrieveSessionID = sessionID
	m.retrieveLimit = limit
	return m.retrieveResults, m.retrieveErr
}

// mockReflexionStore is a mock implementation of ReflexionStore.
type mockReflexionStore struct {
	storeCalled    bool
	storedReflection StoredReflexion
	storeErr       error

	searchCalled bool
	searchQuery  string
	searchLimit  int
	searchResults []StoredReflexion
	searchErr    error
}

func (m *mockReflexionStore) Store(ctx context.Context, reflection StoredReflexion) error {
	m.storeCalled = true
	m.storedReflection = reflection
	return m.storeErr
}

func (m *mockReflexionStore) Search(ctx context.Context, query string, limit int) ([]StoredReflexion, error) {
	m.searchCalled = true
	m.searchQuery = query
	m.searchLimit = limit
	return m.searchResults, m.searchErr
}

func TestContextManagerTool_EpisodicStore(t *testing.T) {
	episodicStore := &mockEpisodicStore{}
	tool := NewContextManagerTool(nil, nil, episodicStore, nil)

	// Create context with session ID
	ctx := context.WithValue(context.Background(), session.SessionIDKey, "test-session-123")

	input := map[string]interface{}{
		"action":  "episodic_store",
		"content": "some observation",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(ctx, inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.IsError {
		t.Errorf("Execute() returned error result: %s", result.Content)
	}

	if !episodicStore.storeEntryCalled {
		t.Error("StoreEntry() was not called")
	}

	if episodicStore.storedEntry.SessionID != "test-session-123" {
		t.Errorf("SessionID = %q, want %q", episodicStore.storedEntry.SessionID, "test-session-123")
	}

	if episodicStore.storedEntry.UserMessage != "some observation" {
		t.Errorf("UserMessage = %q, want %q", episodicStore.storedEntry.UserMessage, "some observation")
	}

	if !strings.Contains(result.Content, "successfully stored") {
		t.Errorf("Result should indicate success, got: %s", result.Content)
	}
}

func TestContextManagerTool_EpisodicSearch(t *testing.T) {
	episodicStore := &mockEpisodicStore{
		retrieveResults: []EpisodicEntry{
			{SessionID: "test-session-123", UserMessage: "First observation", Mode: "direct", Success: true},
			{SessionID: "test-session-123", UserMessage: "Second observation", Mode: "react", Success: false},
		},
	}
	tool := NewContextManagerTool(nil, nil, episodicStore, nil)

	// Create context with session ID
	ctx := context.WithValue(context.Background(), session.SessionIDKey, "test-session-123")

	input := map[string]interface{}{
		"action": "episodic_search",
		"limit":  5,
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(ctx, inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.IsError {
		t.Errorf("Execute() returned error result: %s", result.Content)
	}

	if !episodicStore.retrieveCalled {
		t.Error("RetrieveEntries() was not called")
	}

	if episodicStore.retrieveSessionID != "test-session-123" {
		t.Errorf("SessionID = %q, want %q", episodicStore.retrieveSessionID, "test-session-123")
	}

	if episodicStore.retrieveLimit != 5 {
		t.Errorf("Limit = %d, want %d", episodicStore.retrieveLimit, 5)
	}

	if !strings.Contains(result.Content, "First observation") {
		t.Errorf("Result should contain first observation, got: %s", result.Content)
	}
}

func TestContextManagerTool_EpisodicStoreNoSessionID(t *testing.T) {
	episodicStore := &mockEpisodicStore{}
	tool := NewContextManagerTool(nil, nil, episodicStore, nil)

	// Context without session ID
	ctx := context.Background()

	input := map[string]interface{}{
		"action":  "episodic_store",
		"content": "some observation",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(ctx, inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return error when session ID is missing")
	}

	if !strings.Contains(result.Content, "session ID not found") {
		t.Errorf("Result should indicate missing session ID, got: %s", result.Content)
	}
}

func TestContextManagerTool_EpisodicStoreNilStore(t *testing.T) {
	tool := NewContextManagerTool(nil, nil, nil, nil)

	ctx := context.WithValue(context.Background(), session.SessionIDKey, "test-session-123")

	input := map[string]interface{}{
		"action":  "episodic_store",
		"content": "some observation",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(ctx, inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return error when episodic store is nil")
	}

	if !strings.Contains(result.Content, "episodic memory is not available") {
		t.Errorf("Result should indicate episodic memory unavailable, got: %s", result.Content)
	}
}

func TestContextManagerTool_ReflexionStore(t *testing.T) {
	reflexionStore := &mockReflexionStore{}
	tool := NewContextManagerTool(nil, nil, nil, reflexionStore)

	input := map[string]interface{}{
		"action":           "reflexion_store",
		"summary":          "Test failure summary",
		"hypotheses":       []string{"hypothesis 1", "hypothesis 2"},
		"suggested_action": "retry with different approach",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.IsError {
		t.Errorf("Execute() returned error result: %s", result.Content)
	}

	if !reflexionStore.storeCalled {
		t.Error("Store() was not called")
	}

	if reflexionStore.storedReflection.Summary != "Test failure summary" {
		t.Errorf("Summary = %q, want %q", reflexionStore.storedReflection.Summary, "Test failure summary")
	}

	if len(reflexionStore.storedReflection.Hypotheses) != 2 {
		t.Errorf("Hypotheses length = %d, want 2", len(reflexionStore.storedReflection.Hypotheses))
	}

	if reflexionStore.storedReflection.SuggestedAction != "retry with different approach" {
		t.Errorf("SuggestedAction = %q, want %q", reflexionStore.storedReflection.SuggestedAction, "retry with different approach")
	}

	if !strings.Contains(result.Content, "successfully stored") {
		t.Errorf("Result should indicate success, got: %s", result.Content)
	}
}

func TestContextManagerTool_ReflexionSearch(t *testing.T) {
	reflexionStore := &mockReflexionStore{
		searchResults: []StoredReflexion{
			{Summary: "Previous failure 1", Hypotheses: []string{"bug"}, SuggestedAction: "fix"},
			{Summary: "Previous failure 2", Hypotheses: []string{"timeout"}, SuggestedAction: "retry"},
		},
	}
	tool := NewContextManagerTool(nil, nil, nil, reflexionStore)

	input := map[string]interface{}{
		"action": "reflexion_search",
		"query":  "test query",
		"limit":  5,
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.IsError {
		t.Errorf("Execute() returned error result: %s", result.Content)
	}

	if !reflexionStore.searchCalled {
		t.Error("Search() was not called")
	}

	if reflexionStore.searchQuery != "test query" {
		t.Errorf("Query = %q, want %q", reflexionStore.searchQuery, "test query")
	}

	if reflexionStore.searchLimit != 5 {
		t.Errorf("Limit = %d, want %d", reflexionStore.searchLimit, 5)
	}

	if !strings.Contains(result.Content, "Previous failure 1") {
		t.Errorf("Result should contain first reflection, got: %s", result.Content)
	}
}

func TestContextManagerTool_ReflexionStoreNilStore(t *testing.T) {
	tool := NewContextManagerTool(nil, nil, nil, nil)

	input := map[string]interface{}{
		"action":           "reflexion_store",
		"summary":          "Test failure summary",
		"hypotheses":       []string{"hypothesis 1"},
		"suggested_action": "retry",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return error when reflexion store is nil")
	}

	if !strings.Contains(result.Content, "reflexion memory is not available") {
		t.Errorf("Result should indicate reflexion memory unavailable, got: %s", result.Content)
	}
}

func TestContextManagerTool_ReflexionSearchNilStore(t *testing.T) {
	tool := NewContextManagerTool(nil, nil, nil, nil)

	input := map[string]interface{}{
		"action": "reflexion_search",
		"query":  "test query",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return error when reflexion store is nil")
	}

	if !strings.Contains(result.Content, "reflexion memory is not available") {
		t.Errorf("Result should indicate reflexion memory unavailable, got: %s", result.Content)
	}
}

func TestContextManagerTool_ReflexionStoreMissingParams(t *testing.T) {
	reflexionStore := &mockReflexionStore{}
	tool := NewContextManagerTool(nil, nil, nil, reflexionStore)

	tests := []struct {
		name   string
		input  map[string]interface{}
		wantErr string
	}{
		{
			name: "missing summary",
			input: map[string]interface{}{
				"action":           "reflexion_store",
				"hypotheses":       []string{"hypothesis 1"},
				"suggested_action": "retry",
			},
			wantErr: "missing required parameter: summary",
		},
		{
			name: "missing hypotheses",
			input: map[string]interface{}{
				"action":           "reflexion_store",
				"summary":          "Test failure",
				"suggested_action": "retry",
			},
			wantErr: "missing required parameter: hypotheses",
		},
		{
			name: "missing suggested_action",
			input: map[string]interface{}{
				"action":     "reflexion_store",
				"summary":    "Test failure",
				"hypotheses": []string{"hypothesis 1"},
			},
			wantErr: "missing required parameter: suggested_action",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON, _ := json.Marshal(tt.input)

			result, err := tool.Execute(context.Background(), inputJSON)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}

			if !result.IsError {
				t.Error("Execute() should return error for missing params")
			}

			if !strings.Contains(result.Content, tt.wantErr) {
				t.Errorf("Result should contain %q, got: %s", tt.wantErr, result.Content)
			}
		})
	}
}

func TestContextManagerTool_EpisodicSearchNilStore(t *testing.T) {
	tool := NewContextManagerTool(nil, nil, nil, nil)

	ctx := context.WithValue(context.Background(), session.SessionIDKey, "test-session-123")

	input := map[string]interface{}{
		"action": "episodic_search",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(ctx, inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.IsError {
		t.Error("Execute() should return error when episodic store is nil")
	}

	if !strings.Contains(result.Content, "episodic memory is not available") {
		t.Errorf("Result should indicate episodic memory unavailable, got: %s", result.Content)
	}
}
