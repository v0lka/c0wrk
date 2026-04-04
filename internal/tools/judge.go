package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/user/agent/internal/llm"
	"github.com/user/agent/internal/tools/prompts"
)

// pathRegex matches absolute path-like substrings in command strings.
var pathRegex = regexp.MustCompile(`/[a-zA-Z0-9/_.\-~]+`)

// JudgeVerdict represents the safety assessment of a tool call.
type JudgeVerdict int

const (
	// VerdictAllow indicates the tool call is safe to auto-approve.
	VerdictAllow JudgeVerdict = iota
	// VerdictConfirm indicates the tool call needs user confirmation.
	VerdictConfirm
)

// judgeResult holds both verdict and reasoning for caching.
type judgeResult struct {
	verdict   JudgeVerdict
	reasoning string
}

// ToolJudge evaluates whether a mutating tool call is safe to auto-approve.
type ToolJudge struct {
	provider llm.LLMProvider
	model    string
	cache    map[string]judgeResult
	mu       sync.RWMutex
}

// NewToolJudge creates a new ToolJudge with the given LLM provider and model.
func NewToolJudge(provider llm.LLMProvider, model string) *ToolJudge {
	return &ToolJudge{
		provider: provider,
		model:    model,
		cache:    make(map[string]judgeResult),
	}
}

// judgeCacheKey generates a cache key from tool name and input.
func judgeCacheKey(toolName string, input json.RawMessage) string {
	h := sha256.Sum256(input)
	return toolName + ":" + hex.EncodeToString(h[:])
}

// Judge evaluates whether a tool call is safe to auto-approve.
// It uses the LLM to assess the tool call and caches the result.
// On any LLM error, it defaults to VerdictConfirm (fail-safe) with a reasoning explaining the failure.
// Returns (verdict, reasoning, error).
func (j *ToolJudge) Judge(ctx context.Context, toolName string, input json.RawMessage, taskContext string) (JudgeVerdict, string, error) {
	// Use context-based task context as fallback
	if taskContext == "" {
		taskContext = TaskContextFrom(ctx)
	}

	// Short-circuit for workspace-internal operations
	if allPathsInWorkspace(ctx, input) {
		return VerdictAllow, "all paths are within the session workspace", nil
	}

	// Compute cache key
	key := judgeCacheKey(toolName, input)

	// Check cache under RLock
	j.mu.RLock()
	if result, ok := j.cache[key]; ok {
		j.mu.RUnlock()
		return result.verdict, result.reasoning, nil
	}
	j.mu.RUnlock()

	// Build LLM request
	inputStr := string(input)
	if len(inputStr) > 2000 {
		inputStr = inputStr[:2000] + "... (truncated)"
	}

	systemPrompt := prompts.JudgeSystem

	userPrompt := "Task: " + taskContext + "\n\nTool: " + toolName + "\n\nInput: " + inputStr

	req := llm.ChatRequest{
		Model: j.model,
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens: 100, // Need more tokens for verdict + reason
	}

	// Create a dedicated context for the judge LLM call with its own timeout.
	// The passed-in context may have a tight deadline from the executor, which would
	// cause the judge to fail-safe to VerdictConfirm on timeout.
	judgeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Call LLM
	resp, err := j.provider.ChatCompletion(judgeCtx, req)
	if err != nil {
		// Fail-safe: default to CONFIRM on error with explanatory reasoning
		return VerdictConfirm, "Judge evaluation failed; requiring manual confirmation for safety", nil
	}

	// Parse response - extract verdict and reason
	content := strings.TrimSpace(resp.Message.Content)
	verdict, reasoning := parseJudgeResponse(content)

	// Cache the result under Lock
	j.mu.Lock()
	j.cache[key] = judgeResult{verdict: verdict, reasoning: reasoning}
	j.mu.Unlock()

	return verdict, reasoning, nil
}

// isPathInWorkspace checks if the given absolute path is within the workspace directory.
func isPathInWorkspace(absPath, workspacePath string) bool {
	workspaceAbs := filepath.Clean(workspacePath)
	if !strings.HasSuffix(workspaceAbs, string(filepath.Separator)) {
		workspaceAbs += string(filepath.Separator)
	}
	absPathClean := filepath.Clean(absPath)
	return strings.HasPrefix(absPathClean+string(filepath.Separator), workspaceAbs) || absPathClean == filepath.Clean(workspacePath)
}

// extractJSONStrings recursively extracts all string values from a JSON structure.
func extractJSONStrings(data interface{}) []string {
	var results []string
	switch v := data.(type) {
	case string:
		results = append(results, v)
	case map[string]interface{}:
		for _, val := range v {
			results = append(results, extractJSONStrings(val)...)
		}
	case []interface{}:
		for _, val := range v {
			results = append(results, extractJSONStrings(val)...)
		}
	}
	return results
}

// extractPaths extracts absolute path-like substrings from a string value.
func extractPaths(s string) []string {
	return pathRegex.FindAllString(s, -1)
}

// allPathsInWorkspace returns true if the JSON input contains at least one absolute
// path and every such path is within the workspace directory.
func allPathsInWorkspace(ctx context.Context, input json.RawMessage) bool {
	workspacePath := WorkspacePathFrom(ctx)
	if workspacePath == "" {
		return false
	}

	var parsed interface{}
	if err := json.Unmarshal(input, &parsed); err != nil {
		return false
	}

	strValues := extractJSONStrings(parsed)
	var allPaths []string
	for _, s := range strValues {
		allPaths = append(allPaths, extractPaths(s)...)
	}

	if len(allPaths) == 0 {
		return false
	}

	for _, p := range allPaths {
		cleaned := filepath.Clean(p)
		if !isPathInWorkspace(cleaned, workspacePath) {
			return false
		}
	}
	return true
}

// ResetCache clears all cached verdicts.
func (j *ToolJudge) ResetCache() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cache = make(map[string]judgeResult)
}

// parseJudgeResponse extracts verdict and reasoning from LLM response.
// Expected format:
//
//	VERDICT: ALLOW or CONFIRM
//	REASON: <explanation>
//
// Falls back to reasonable defaults if parsing fails.
func parseJudgeResponse(content string) (verdict JudgeVerdict, reasoning string) {
	lines := strings.Split(content, "\n")
	verdict = VerdictConfirm // default to safe
	reasoning = "Unable to parse judge response; requiring manual confirmation for safety"

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "VERDICT:") {
			verdictStr := strings.TrimSpace(strings.TrimPrefix(line, "VERDICT:"))
			if strings.EqualFold(verdictStr, "ALLOW") {
				verdict = VerdictAllow
			} else {
				verdict = VerdictConfirm
			}
		} else if strings.HasPrefix(line, "REASON:") {
			reasoning = strings.TrimSpace(strings.TrimPrefix(line, "REASON:"))
		}
	}

	// If we couldn't parse a reason but have a verdict, provide a default
	if reasoning == "Unable to parse judge response; requiring manual confirmation for safety" && verdict == VerdictAllow {
		reasoning = "Tool call appears safe and relevant to the task"
	}

	return verdict, reasoning
}
