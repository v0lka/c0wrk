package tools

import (
	"context"
	"encoding/json"
)

// ParamManager handles auto-injected tool parameters.
// SanitizeSchema strips auto-injected params from tool schemas so the LLM never sees them.
// InjectParams adds their values at execution time.
//
// A single ParamManager instance should be shared between the MCP gateway
// (which calls SanitizeSchema) and the tool registry (which calls InjectParams)
// to ensure both sides agree on the set of auto-injected parameters.
type ParamManager interface {
	SanitizeSchema(source string, schema json.RawMessage) json.RawMessage
	InjectParams(ctx context.Context, toolName, source string, input json.RawMessage) json.RawMessage
}

// DefaultParamManager returns a ParamManager that handles all known
// auto-injected parameters (currently: "project").
func DefaultParamManager() ParamManager {
	return &defaultParamManager{
		autoInjected: map[string]bool{
			AutoInjectedParamProject: true,
		},
	}
}

type defaultParamManager struct {
	autoInjected map[string]bool
}

func (m *defaultParamManager) SanitizeSchema(_ string, schema json.RawMessage) json.RawMessage {
	result, err := stripParamsFromSchema(schema, m.autoInjected)
	if err != nil {
		return schema // re-marshaling error is near-impossible; fall back to original
	}
	return result
}

func (m *defaultParamManager) InjectParams(ctx context.Context, _, _ string, input json.RawMessage) json.RawMessage {
	for param := range m.autoInjected {
		if param == AutoInjectedParamProject {
			if wsPath := WorkspacePathFrom(ctx); wsPath != "" {
				input = injectJSONParam(input, param, wsPath)
			}
		}
	}
	return input
}

// injectJSONParam adds a key-value pair to a JSON object if the key is not already present.
// If the input is not a valid JSON object (including null), it is returned unchanged.
func injectJSONParam(input json.RawMessage, key, value string) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return input
	}
	if m == nil {
		return input // null or empty object-like — cannot inject
	}
	if _, exists := m[key]; exists {
		return input
	}
	m[key] = value
	out, err := json.Marshal(m)
	if err != nil {
		return input
	}
	return out
}
