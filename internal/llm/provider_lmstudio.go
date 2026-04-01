package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/user/agent/internal/logger"
)

// LMStudioProviderConfig contains configuration for LM Studio provider.
type LMStudioProviderConfig struct {
	Name    string // logical name for logging
	BaseURL string // default: http://localhost:1234
	APIKey  string // optional bearer token
}

// LMStudioProvider implements LLMProvider using LM Studio's native REST API v1.
type LMStudioProvider struct {
	client  *http.Client
	baseURL string
	apiKey  string
	name    string
	logger  *logger.SessionLogger
}

// LMStudioModel represents a model in LM Studio.
type LMStudioModel struct {
	ID           string `json:"id"`
	Type         string `json:"type"`           // "llm", "vlm", "embeddings"
	State        string `json:"state"`          // "loaded", "not_loaded"
	Architecture string `json:"architecture"`
	Quantization string `json:"quantization"`
	MaxContext   int    `json:"max_context_length"`
}

// NewLMStudioProvider creates a new LM Studio provider.
// If BaseURL is empty, defaults to http://localhost:1234.
func NewLMStudioProvider(cfg LMStudioProviderConfig) (*LMStudioProvider, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:1234"
	}

	// Remove trailing slash from base URL
	baseURL = strings.TrimSuffix(baseURL, "/")

	return &LMStudioProvider{
		client:  &http.Client{},
		baseURL: baseURL,
		apiKey:  cfg.APIKey,
		name:    cfg.Name,
		logger:  nil, // Will be set if needed
	}, nil
}

// SetLogger sets the session logger for the provider.
func (p *LMStudioProvider) SetLogger(l *logger.SessionLogger) {
	p.logger = l
}

// Name returns the provider name for logging.
func (p *LMStudioProvider) Name() string {
	return p.name
}

// Internal types for LM Studio v1 API

type lmStudioRequest struct {
	Model          string               `json:"model"`
	Input          []lmStudioInputMsg   `json:"input"`
	SystemPrompt   string               `json:"system_prompt,omitempty"`
	Temperature    *float64             `json:"temperature,omitempty"`
	MaxOutputTokens int                 `json:"max_output_tokens,omitempty"`
	Stream         bool                 `json:"stream"`
	Tools          []lmStudioTool       `json:"tools,omitempty"`
	TopK           *int                 `json:"top_k,omitempty"`
	MinP           *float64             `json:"min_p,omitempty"`
	RepeatPenalty  *float64             `json:"repeat_penalty,omitempty"`
}

type lmStudioInputMsg struct {
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []lmStudioToolCall `json:"tool_calls,omitempty"`
}

type lmStudioToolCall struct {
	ID        string          `json:"id,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type lmStudioTool struct {
	Type     string          `json:"type"`
	Function lmStudioToolFunc `json:"function"`
}

type lmStudioToolFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type lmStudioResponse struct {
	Output     []lmStudioOutputItem `json:"output"`
	Stats      lmStudioStats        `json:"stats"`
	ResponseID string               `json:"response_id"`
}

type lmStudioOutputItem struct {
	Type      string          `json:"type"` // "message", "reasoning", "tool_call"
	Content   string          `json:"content,omitempty"`
	ID        string          `json:"id,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type lmStudioStats struct {
	InputTokens            int     `json:"input_tokens"`
	TotalOutputTokens      int     `json:"total_output_tokens"`
	TokensPerSecond        float64 `json:"tokens_per_second"`
	TimeToFirstTokenSeconds float64 `json:"time_to_first_token_seconds"`
}

type lmStudioContentDelta struct {
	Content string `json:"content"`
}

type lmStudioToolCallStart struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type lmStudioToolCallArguments struct {
	Arguments string `json:"arguments"`
}

type lmStudioError struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

type lmStudioModelLoadProgress struct {
	Progress float64 `json:"progress"`
	Model    string  `json:"model"`
}

type lmStudioPromptProcessing struct {
	Progress float64 `json:"progress"`
}

type lmStudioChatStart struct {
	Model string `json:"model"`
}

// ChatCompletion sends a request and returns the full response.
func (p *LMStudioProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	lmReq, err := p.buildRequest(req, false)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: failed to build request: %w", err)
	}

	httpReq, err := p.newHTTPRequest(ctx, "POST", "/api/v1/chat", lmReq)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: failed to create request: %w", err)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, p.parseErrorResponse(resp)
	}

	var lmResp lmStudioResponse
	if err := json.NewDecoder(resp.Body).Decode(&lmResp); err != nil {
		return nil, fmt.Errorf("lmstudio: failed to decode response: %w", err)
	}

	return p.parseResponse(&lmResp)
}

// StreamChatCompletion sends a request and returns a channel of streaming chunks.
func (p *LMStudioProvider) StreamChatCompletion(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error) {
	lmReq, err := p.buildRequest(req, true)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: failed to build request: %w", err)
	}

	httpReq, err := p.newHTTPRequest(ctx, "POST", "/api/v1/chat", lmReq)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: failed to create request: %w", err)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: request failed: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, p.parseErrorResponse(resp)
	}

	chunks := make(chan ChatChunk)

	go func() {
		defer close(chunks)
		defer func() { _ = resp.Body.Close() }()

		p.processSSEStream(resp.Body, chunks)
	}()

	return chunks, nil
}

// processSSEStream reads SSE events from the response body and emits ChatChunks.
func (p *LMStudioProvider) processSSEStream(body io.Reader, chunks chan<- ChatChunk) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024) // 1MB max line size

	var currentToolCall *ToolCall

	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines
		if line == "" {
			continue
		}

		// Parse SSE format: "event: <type>" or "data: <JSON>"
		if strings.HasPrefix(line, "event: ") {
			eventType := strings.TrimPrefix(line, "event: ")
			
			// Read the next line which should be "data: <JSON>"
			if !scanner.Scan() {
				break
			}
			dataLine := scanner.Text()
			
			if !strings.HasPrefix(dataLine, "data: ") {
				continue
			}
			
			dataStr := strings.TrimPrefix(dataLine, "data: ")
			if dataStr == "" {
				continue
			}

			p.handleSSEEvent(eventType, dataStr, chunks, &currentToolCall)
		} else if strings.HasPrefix(line, "data: ") {
			// Some SSE implementations skip the event line
			dataStr := strings.TrimPrefix(line, "data: ")
			if dataStr == "" {
				continue
			}

			p.handleSSEEvent("", dataStr, chunks, &currentToolCall)
		}
	}

	if err := scanner.Err(); err != nil {
		chunks <- ChatChunk{StopReason: "error"}
	}
}

// handleSSEEvent handles individual SSE events.
func (p *LMStudioProvider) handleSSEEvent(eventType, dataStr string, chunks chan<- ChatChunk, currentToolCall **ToolCall) {
	switch eventType {
	case "content.delta":
		var delta lmStudioContentDelta
		if err := json.Unmarshal([]byte(dataStr), &delta); err == nil && delta.Content != "" {
			chunks <- ChatChunk{Delta: delta.Content}
		}

	case "reasoning.delta":
		// Reasoning is visible content, emit as regular delta
		var delta lmStudioContentDelta
		if err := json.Unmarshal([]byte(dataStr), &delta); err == nil && delta.Content != "" {
			chunks <- ChatChunk{Delta: delta.Content}
		}

	case "tool_call.start":
		var start lmStudioToolCallStart
		if err := json.Unmarshal([]byte(dataStr), &start); err == nil {
			*currentToolCall = &ToolCall{
				ID:   start.ID,
				Name: start.Name,
				Input: json.RawMessage(""),
			}
		}

	case "tool_call.arguments":
		if *currentToolCall != nil {
			var args lmStudioToolCallArguments
			if err := json.Unmarshal([]byte(dataStr), &args); err == nil {
				existing := string((*currentToolCall).Input)
				(*currentToolCall).Input = json.RawMessage(existing + args.Arguments)
			}
		}

	case "tool_call.success", "tool_call.failure":
		if *currentToolCall != nil {
			chunks <- ChatChunk{ToolCall: *currentToolCall}
			*currentToolCall = nil
		}

	case "chat.end":
		chunks <- ChatChunk{StopReason: "end_turn"}

	case "error":
		var lmErr lmStudioError
		if err := json.Unmarshal([]byte(dataStr), &lmErr); err == nil {
			p.logDebug("stream error", "message", lmErr.Message, "code", lmErr.Code)
		}
		chunks <- ChatChunk{StopReason: "error"}

	case "model_load.start", "model_load.progress", "model_load.complete":
		var progress lmStudioModelLoadProgress
		if err := json.Unmarshal([]byte(dataStr), &progress); err == nil {
			p.logDebug("model load", "event", eventType, "progress", progress.Progress, "model", progress.Model)
		}

	case "prompt_processing.start", "prompt_processing.progress", "prompt_processing.complete":
		var proc lmStudioPromptProcessing
		if err := json.Unmarshal([]byte(dataStr), &proc); err == nil {
			p.logDebug("prompt processing", "event", eventType, "progress", proc.Progress)
		}

	case "chat.start":
		var start lmStudioChatStart
		if err := json.Unmarshal([]byte(dataStr), &start); err == nil {
			p.logDebug("chat started", "model", start.Model)
		}

	default:
		// Unknown event type, try to parse as generic and log
		p.logDebug("unknown SSE event", "type", eventType, "data", dataStr)
	}
}

// buildRequest converts ChatRequest to LM Studio v1 request format.
func (p *LMStudioProvider) buildRequest(req ChatRequest, stream bool) (*lmStudioRequest, error) {
	// Extract system messages into system_prompt
	var systemPrompt string
	var input []lmStudioInputMsg

	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			if systemPrompt != "" {
				systemPrompt += "\n"
			}
			systemPrompt += msg.Content
		case "user", "assistant":
			inputMsg := lmStudioInputMsg{
				Role:    msg.Role,
				Content: msg.Content,
			}

			// Handle tool calls in assistant messages
			if len(msg.ToolCalls) > 0 {
				inputMsg.ToolCalls = make([]lmStudioToolCall, len(msg.ToolCalls))
				for i, tc := range msg.ToolCalls {
					inputMsg.ToolCalls[i] = lmStudioToolCall{
						ID:        tc.ID,
						Tool:      tc.Name,
						Arguments: tc.Input,
					}
				}
			}

			input = append(input, inputMsg)

		case "tool":
			// Tool result messages
			input = append(input, lmStudioInputMsg{
				Role:       "tool",
				Content:    msg.Content,
				ToolCallID: msg.ToolCallID,
			})
		}
	}

	lmReq := &lmStudioRequest{
		Model:           req.Model,
		Input:           input,
		SystemPrompt:    systemPrompt,
		MaxOutputTokens: req.MaxTokens,
		Stream:          stream,
		Temperature:     req.Temperature,
	}

	// Convert tools
	if len(req.Tools) > 0 {
		lmReq.Tools = make([]lmStudioTool, len(req.Tools))
		for i, tool := range req.Tools {
			lmReq.Tools[i] = lmStudioTool{
				Type: "function",
				Function: lmStudioToolFunc{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.InputSchema,
				},
			}
		}
	}

	return lmReq, nil
}

// parseResponse converts LM Studio response to ChatResponse.
func (p *LMStudioProvider) parseResponse(lmResp *lmStudioResponse) (*ChatResponse, error) {
	message := Message{
		Role: "assistant",
	}

	var hasToolCalls bool

	// Process output items
	for _, item := range lmResp.Output {
		switch item.Type {
		case "message", "reasoning":
			if message.Content != "" {
				message.Content += "\n"
			}
			message.Content += item.Content

		case "tool_call":
			hasToolCalls = true
			message.ToolCalls = append(message.ToolCalls, ToolCall{
				ID:    item.ID,
				Name:  item.Tool,
				Input: item.Arguments,
			})
		}
	}

	// Determine stop reason
	stopReason := "end_turn"
	if hasToolCalls {
		stopReason = "tool_use"
	}

	return &ChatResponse{
		Message:    message,
		StopReason: stopReason,
		Usage: TokenUsage{
			InputTokens:  lmResp.Stats.InputTokens,
			OutputTokens: lmResp.Stats.TotalOutputTokens,
		},
	}, nil
}

// newHTTPRequest creates a new HTTP request with proper headers.
func (p *LMStudioProvider) newHTTPRequest(ctx context.Context, method, path string, body interface{}) (*http.Request, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	url := p.baseURL + path
	httpReq, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	return httpReq, nil
}

// parseErrorResponse extracts error information from HTTP error responses.
func (p *LMStudioProvider) parseErrorResponse(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("lmstudio: HTTP %d (failed to read error body)", resp.StatusCode)
	}

	// Try to parse as JSON error
	var lmErr struct {
		Error lmStudioError `json:"error"`
	}
	if err := json.Unmarshal(body, &lmErr); err == nil && lmErr.Error.Message != "" {
		return fmt.Errorf("lmstudio: %s (HTTP %d)", lmErr.Error.Message, resp.StatusCode)
	}

	return fmt.Errorf("lmstudio: HTTP %d: %s", resp.StatusCode, string(body))
}

// logDebug logs a debug message if logger is available.
func (p *LMStudioProvider) logDebug(msg string, args ...interface{}) {
	if p.logger != nil && p.logger.Logger() != nil {
		p.logger.Logger().Debug(msg, args...)
	}
}

// ListModels returns all available models from LM Studio.
func (p *LMStudioProvider) ListModels(ctx context.Context) ([]LMStudioModel, error) {
	httpReq, err := p.newHTTPRequest(ctx, "GET", "/api/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: failed to create request: %w", err)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, p.parseErrorResponse(resp)
	}

	var result struct {
		Data []LMStudioModel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("lmstudio: failed to decode response: %w", err)
	}

	return result.Data, nil
}

// LoadModel loads a model in LM Studio.
func (p *LMStudioProvider) LoadModel(ctx context.Context, model string) error {
	reqBody := map[string]string{"model": model}
	httpReq, err := p.newHTTPRequest(ctx, "POST", "/api/v1/models/load", reqBody)
	if err != nil {
		return fmt.Errorf("lmstudio: failed to create request: %w", err)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("lmstudio: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return p.parseErrorResponse(resp)
	}

	return nil
}

// UnloadModel unloads a model from LM Studio.
func (p *LMStudioProvider) UnloadModel(ctx context.Context, model string) error {
	reqBody := map[string]string{"model": model}
	httpReq, err := p.newHTTPRequest(ctx, "POST", "/api/v1/models/unload", reqBody)
	if err != nil {
		return fmt.Errorf("lmstudio: failed to create request: %w", err)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("lmstudio: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return p.parseErrorResponse(resp)
	}

	return nil
}

// MetadataSource returns a ModelMetadataSource that resolves model metadata
// by querying the LM Studio server's model listing endpoint.
func (p *LMStudioProvider) MetadataSource() ModelMetadataSource {
	return func(model string) (ModelMetadata, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		models, err := p.ListModels(ctx)
		if err != nil {
			return ModelMetadata{}, false
		}

		for _, m := range models {
			if m.ID == model {
				meta := ModelMetadata{
					ContextWindow: m.MaxContext,
					OutputLimit:   4096, // reasonable default
					TokenizerType: "approximate",
				}
				if meta.ContextWindow == 0 {
					return ModelMetadata{}, false
				}
				return meta, true
			}
		}
		return ModelMetadata{}, false
	}
}
