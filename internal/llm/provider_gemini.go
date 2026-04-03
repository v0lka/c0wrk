package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/genai"
)

// geminiStopReasonMap maps Gemini finish reasons to our standard format.
var geminiStopReasonMap = map[string]string{
	string(genai.FinishReasonStop):       "end_turn",
	string(genai.FinishReasonMaxTokens):  "max_tokens",
	string(genai.FinishReasonSafety):     "safety",
	string(genai.FinishReasonRecitation): "recitation",
	string(genai.FinishReasonOther):      "other",
}

// GeminiProviderConfig holds configuration for the Gemini provider.
type GeminiProviderConfig struct {
	APIKey    string
	ProjectID string // optional, for Vertex AI
	Location  string // optional, for Vertex AI
}

// GeminiProvider implements LLMProvider using Google's Gen AI SDK.
type GeminiProvider struct {
	client *genai.Client
	name   string
}

// NewGeminiProvider creates a new GeminiProvider with the given configuration.
func NewGeminiProvider(ctx context.Context, cfg GeminiProviderConfig) (*GeminiProvider, error) {
	clientCfg := &genai.ClientConfig{}

	if cfg.ProjectID != "" && cfg.Location != "" {
		// Use Vertex AI backend
		clientCfg.Project = cfg.ProjectID
		clientCfg.Location = cfg.Location
		clientCfg.Backend = genai.BackendVertexAI
	} else {
		// Use Gemini API backend
		clientCfg.APIKey = cfg.APIKey
		clientCfg.Backend = genai.BackendGeminiAPI
	}

	client, err := genai.NewClient(ctx, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create genai client: %w", err)
	}

	return &GeminiProvider{
		client: client,
		name:   "gemini",
	}, nil
}

// Name returns the provider name.
func (p *GeminiProvider) Name() string {
	return p.name
}

// ChatCompletion sends a request and returns the full response.
func (p *GeminiProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	contents, systemInstruction := p.convertMessages(req.Messages)
	config := p.buildConfig(req, systemInstruction)

	result, err := p.client.Models.GenerateContent(ctx, req.Model, contents, config)
	if err != nil {
		return nil, p.wrapError(fmt.Errorf("gemini GenerateContent error: %w", err))
	}

	return p.convertResponse(result)
}

// StreamChatCompletion sends a request and returns a channel of streaming chunks.
func (p *GeminiProvider) StreamChatCompletion(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error) {
	contents, systemInstruction := p.convertMessages(req.Messages)
	config := p.buildConfig(req, systemInstruction)

	ch := make(chan ChatChunk)

	go func() {
		defer close(ch)

		for result, err := range p.client.Models.GenerateContentStream(ctx, req.Model, contents, config) {
			if err != nil {
				ch <- ChatChunk{StopReason: "error"}
				return
			}

			chunks := p.convertStreamResponse(result)
			for _, chunk := range chunks {
				select {
				case ch <- chunk:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch, nil
}

// convertMessages converts our Message format to Gemini Content format.
// Returns contents and system instruction separately.
func (p *GeminiProvider) convertMessages(messages []Message) (contents []*genai.Content, systemInstruction *genai.Content) {
	systemPromptStr, filteredMsgs := ExtractSystemPrompt(messages)
	if systemPromptStr != "" {
		systemInstruction = &genai.Content{
			Parts: []*genai.Part{{Text: systemPromptStr}},
		}
	}

	for _, msg := range filteredMsgs {
		switch msg.Role {
		case "user":
			contents = append(contents, &genai.Content{
				Role:  "user",
				Parts: []*genai.Part{{Text: msg.Content}},
			})
		case "assistant":
			content := &genai.Content{
				Role: "model", // Gemini uses "model" instead of "assistant"
			}
			if msg.Content != "" {
				content.Parts = append(content.Parts, &genai.Part{Text: msg.Content})
			}
			// Add function calls from assistant
			for _, tc := range msg.ToolCalls {
				var args map[string]any
				if len(tc.Input) > 0 {
					_ = json.Unmarshal(tc.Input, &args)
				}
				content.Parts = append(content.Parts, &genai.Part{
					FunctionCall: &genai.FunctionCall{
						ID:   tc.ID,
						Name: tc.Name,
						Args: args,
					},
				})
			}
			contents = append(contents, content)
		case "tool":
			// Tool response
			var responseData map[string]any
			if msg.Content != "" {
				_ = json.Unmarshal([]byte(msg.Content), &responseData)
				if responseData == nil {
					responseData = map[string]any{"result": msg.Content}
				}
			}
			contents = append(contents, &genai.Content{
				Role: "user", // Function responses are sent as user role in Gemini
				Parts: []*genai.Part{{
					FunctionResponse: &genai.FunctionResponse{
						ID:       msg.ToolCallID,
						Name:     "", // Will be matched by ID
						Response: responseData,
					},
				}},
			})
		}
	}

	return contents, systemInstruction
}

// buildConfig creates the GenerateContentConfig from our ChatRequest.
func (p *GeminiProvider) buildConfig(req ChatRequest, systemInstruction *genai.Content) *genai.GenerateContentConfig {
	config := &genai.GenerateContentConfig{}

	if req.MaxTokens > 0 {
		config.MaxOutputTokens = int32(req.MaxTokens)
	}

	if req.Temperature != nil {
		temp := float32(*req.Temperature)
		config.Temperature = &temp
	}

	if systemInstruction != nil {
		config.SystemInstruction = systemInstruction
	}

	// Convert tools
	if len(req.Tools) > 0 {
		functionDeclarations := make([]*genai.FunctionDeclaration, 0, len(req.Tools))
		for _, tool := range req.Tools {
			fd := &genai.FunctionDeclaration{
				Name:        tool.Name,
				Description: tool.Description,
			}
			if len(tool.InputSchema) > 0 {
				var schema *genai.Schema
				if err := json.Unmarshal(tool.InputSchema, &schema); err == nil {
					fd.Parameters = schema
				}
			}
			functionDeclarations = append(functionDeclarations, fd)
		}
		config.Tools = []*genai.Tool{{
			FunctionDeclarations: functionDeclarations,
		}}
	}

	return config
}

// convertResponse converts Gemini response to our ChatResponse format.
func (p *GeminiProvider) convertResponse(result *genai.GenerateContentResponse) (*ChatResponse, error) {
	response := &ChatResponse{
		Message: Message{
			Role: "assistant",
		},
	}

	// Extract content from candidates
	if len(result.Candidates) > 0 {
		candidate := result.Candidates[0]

		// Map finish reason
		response.StopReason = MapStopReason(string(candidate.FinishReason), geminiStopReasonMap)

		if candidate.Content != nil {
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					response.Message.Content += part.Text
				}
				if part.FunctionCall != nil {
					argsJSON, _ := json.Marshal(part.FunctionCall.Args)
					toolCall := ToolCall{
						ID:    part.FunctionCall.ID,
						Name:  part.FunctionCall.Name,
						Input: argsJSON,
					}
					// Generate ID if not provided
					if toolCall.ID == "" {
						toolCall.ID = "call_" + part.FunctionCall.Name
					}
					response.Message.ToolCalls = append(response.Message.ToolCalls, toolCall)
				}
			}
		}
	}

	// Map token usage
	if result.UsageMetadata != nil {
		response.Usage = TokenUsage{
			InputTokens:  int(result.UsageMetadata.PromptTokenCount),
			OutputTokens: int(result.UsageMetadata.CandidatesTokenCount),
		}
	}

	return response, nil
}

// convertStreamResponse converts a streaming response to ChatChunks.
func (p *GeminiProvider) convertStreamResponse(result *genai.GenerateContentResponse) []ChatChunk {
	var chunks []ChatChunk

	if len(result.Candidates) > 0 {
		candidate := result.Candidates[0]

		if candidate.Content != nil {
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					chunks = append(chunks, ChatChunk{Delta: part.Text})
				}
				if part.FunctionCall != nil {
					argsJSON, _ := json.Marshal(part.FunctionCall.Args)
					toolCall := ToolCall{
						ID:    part.FunctionCall.ID,
						Name:  part.FunctionCall.Name,
						Input: argsJSON,
					}
					if toolCall.ID == "" {
						toolCall.ID = "call_" + part.FunctionCall.Name
					}
					chunks = append(chunks, ChatChunk{ToolCall: &toolCall})
				}
			}
		}

		// Check for finish reason
		if candidate.FinishReason != "" {
			chunks = append(chunks, ChatChunk{
				StopReason: MapStopReason(string(candidate.FinishReason), geminiStopReasonMap),
			})
		}
	}

	return chunks
}

// wrapError maps Gemini SDK error types to *LLMError.
func (p *GeminiProvider) wrapError(err error) error {
	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		return WrapProviderError(p.name, apiErr.Code, err)
	}
	return WrapProviderError(p.name, 0, err)
}
