package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/user/agent/core/tools/prompts"
	"github.com/user/agent/sdk/llm"
	tools "github.com/user/agent/sdk/tools"
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
	provider     llm.Provider
	model        string
	cache        map[string]judgeResult
	mu           sync.RWMutex
	maxCacheSize int // max cached results before cache is cleared (default: 1000)
	logger       *slog.Logger
}

// NewToolJudge creates a new ToolJudge with the given LLM provider and model.
// If maxCacheSize is 0, defaults to 1000. Logger may be nil.
func NewToolJudge(provider llm.Provider, model string, maxCacheSize int, logger *slog.Logger) *ToolJudge {
	if maxCacheSize == 0 {
		maxCacheSize = 1000
	}
	return &ToolJudge{
		provider:     provider,
		model:        model,
		cache:        make(map[string]judgeResult),
		maxCacheSize: maxCacheSize,
		logger:       logger,
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
	log := j.logger

	if log != nil {
		log.Debug("judge: evaluating tool", "tool", toolName)
	}

	// Internal tools are always allowed (defense-in-depth)
	if IsInternalTool(toolName) {
		if log != nil {
			log.Debug("judge: fast-path internal tool", "tool", toolName, "verdict", "ALLOW")
		}
		return VerdictAllow, "internal tool, always allowed", nil
	}

	// Use context-based task context as fallback
	if taskContext == "" {
		taskContext = TaskContextFrom(ctx)
	}

	// Unconditionally allow operations inside session temp directory
	if tempDir := tools.TempDirFrom(ctx); tempDir != "" && allPathsInDir(input, tempDir) {
		if log != nil {
			log.Debug("judge: fast-path temp dir", "tool", toolName, "verdict", "ALLOW")
		}
		return VerdictAllow, "all paths are within the session temp directory", nil
	}

	// Short-circuit for workspace-internal operations
	if allPathsInWorkspace(ctx, input) {
		if log != nil {
			log.Debug("judge: fast-path workspace", "tool", toolName, "verdict", "ALLOW")
		}
		return VerdictAllow, "all paths are within the session workspace", nil
	}

	// Compute cache key
	key := judgeCacheKey(toolName, input)

	// Check cache under RLock
	j.mu.RLock()
	if result, ok := j.cache[key]; ok {
		j.mu.RUnlock()
		if log != nil {
			log.Debug("judge: cache hit", "tool", toolName, "verdict", verdictString(result.verdict))
		}
		return result.verdict, result.reasoning, nil
	}
	j.mu.RUnlock()

	// Build LLM request
	inputStr := string(input)

	systemPrompt := prompts.JudgeSystem

	userPrompt := "Task: " + taskContext + "\n\nTool: " + toolName + "\n\nInput: " + inputStr

	// Append compact environment context for safety reasoning.
	if envBlock := FormatCompactEnvBlock(EnvInfoFrom(ctx)); envBlock != "" {
		userPrompt += "\n\n" + envBlock
	}

	req := llm.ChatRequest{
		Model: j.model,
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens:       100, // Need more tokens for verdict + reason
		ReasoningEffort: llm.ReasoningOff, // Judge is a simple classification task — no extended thinking needed
	}

	// Create a dedicated context for the judge LLM call with its own timeout.
	// Uses the parent context so that application shutdown is respected.
	// On timeout, the judge fail-safes to VerdictConfirm below.
	judgeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	if log != nil {
		log.Debug("judge: LLM evaluation starting", "tool", toolName, "model", j.model)
	}

	// Call LLM
	resp, err := j.provider.ChatCompletion(judgeCtx, req)
	if err != nil {
		if log != nil {
			log.Warn("judge: LLM call failed, fail-safe to CONFIRM", "tool", toolName, "error", err)
		}
		// Fail-safe: default to CONFIRM on error with explanatory reasoning
		return VerdictConfirm, "Judge evaluation failed; requiring manual confirmation for safety", nil
	}

	// Parse response - extract verdict and reason
	content := strings.TrimSpace(resp.Message.Content)
	verdict, reasoning := parseJudgeResponse(content)

	if log != nil {
		abbrevReasoning := reasoning
		if len(abbrevReasoning) > 120 {
			abbrevReasoning = abbrevReasoning[:120] + "..."
		}
		log.Debug("judge: LLM verdict", "tool", toolName, "verdict", verdictString(verdict), "reasoning", abbrevReasoning)
	}

	// Cache the result under Lock (evict if cache is too large)
	j.mu.Lock()
	// Aggressive full-clear when cache is full. Acceptable because judge results
	// are cheap to recompute and the cache is a best-effort optimization.
	if len(j.cache) >= j.maxCacheSize {
		j.cache = make(map[string]judgeResult)
	}
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
func extractJSONStrings(data any) []string {
	var results []string
	switch v := data.(type) {
	case string:
		results = append(results, v)
	case map[string]any:
		for _, val := range v {
			results = append(results, extractJSONStrings(val)...)
		}
	case []any:
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

// allPathsInDir returns true if the JSON input contains at least one absolute
// path and every such path is within the specified directory.
func allPathsInDir(input json.RawMessage, dir string) bool {
	if dir == "" {
		return false
	}

	var parsed any
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
		if !isPathInWorkspace(cleaned, dir) {
			return false
		}
	}
	return true
}

// allPathsInWorkspace returns true if the JSON input contains at least one absolute
// path and every such path is within the workspace directory.
func allPathsInWorkspace(ctx context.Context, input json.RawMessage) bool {
	workspacePath := WorkspacePathFrom(ctx)
	if workspacePath == "" {
		return false
	}
	return allPathsInDir(input, workspacePath)
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

// JudgeConfig holds the settings needed to create a ToolJudge.
type JudgeConfig struct {
	Model        string // specific model for judge; if empty, uses DefaultModel
	DefaultModel string // fallback model from active provider
	Provider     llm.Provider
	MaxCacheSize int // max cached results before cache is cleared (default: 1000)
}

// NewToolJudgeFromConfig creates a ToolJudge if properly configured.
// Returns nil if misconfigured. Logs warnings via the provided logger.
func NewToolJudgeFromConfig(cfg JudgeConfig, logger *slog.Logger) *ToolJudge {
	if cfg.Provider == nil {
		return nil
	}

	model := cfg.Model
	if model == "" {
		model = cfg.DefaultModel
	}

	if model == "" {
		if logger != nil {
			logger.Warn("tool judge disabled: no model configured")
		}
		return nil
	}

	judge := NewToolJudge(cfg.Provider, model, cfg.MaxCacheSize, logger)
	if logger != nil {
		logger.Info("tool judge initialized", "model", model)
	}
	return judge
}

// verdictString returns a human-readable string for a JudgeVerdict.
func verdictString(v JudgeVerdict) string {
	switch v {
	case VerdictAllow:
		return "ALLOW"
	case VerdictConfirm:
		return "CONFIRM"
	default:
		return "UNKNOWN"
	}
}
