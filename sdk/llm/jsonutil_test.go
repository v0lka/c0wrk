package llm

import (
	"encoding/json"
	"testing"
)

func TestExtractJSON_MultipleBlocks(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantOK   bool
		wantJSON string // exact expected JSON when applicable
	}{
		{
			name:     "plain JSON",
			content:  `{"mode":"react","domain":"code"}`,
			wantOK:   true,
			wantJSON: `{"mode":"react","domain":"code"}`,
		},
		{
			name:     "JSON in code block with language",
			content:  "```json\n{\"mode\":\"react\"}\n```",
			wantOK:   true,
			wantJSON: `{"mode":"react"}`,
		},
		{
			name:     "JSON in code block without language",
			content:  "```\n{\"mode\":\"react\"}\n```",
			wantOK:   true,
			wantJSON: `{"mode":"react"}`,
		},
		{
			name:    "two separate JSON blocks returns last valid",
			content: `Some text {"a": 1} more text {"b": 2}`,
			wantOK:  true,
			// last valid block is {"b": 2}
			wantJSON: `{"b": 2}`,
		},
		{
			name:     "nested braces",
			content:  `Result: {"outer": {"inner": "value"}} end`,
			wantOK:   true,
			wantJSON: `{"outer": {"inner": "value"}}`,
		},
		{
			name:     "balanced braces with multiple objects returns last",
			content:  `{"x": 1} {"y": 2}`,
			wantOK:   true,
			wantJSON: `{"y": 2}`,
		},
		{
			name:     "markdown block with nested JSON",
			content:  "```json\n{\"result\": {\"data\": [1,2,3]}}\n```",
			wantOK:   true,
			wantJSON: `{"result": {"data": [1,2,3]}}`,
		},
		{
			name:     "brace inside string value",
			content:  `{"key": "value with } inside"}`,
			wantOK:   true,
			wantJSON: `{"key": "value with } inside"}`,
		},
		{
			name:     "escaped quote inside string",
			content:  `{"key": "escaped \" quote"}`,
			wantOK:   true,
			wantJSON: `{"key": "escaped \" quote"}`,
		},
		{
			name:    "no braces returns original",
			content: `just plain text`,
			wantOK:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractJSON(tt.content)
			if tt.wantOK {
				if !json.Valid([]byte(result)) {
					t.Errorf("ExtractJSON(%q) = %q, want valid JSON", tt.content, result)
				}
				if tt.wantJSON != "" && result != tt.wantJSON {
					t.Errorf("ExtractJSON(%q) = %q, want %q", tt.content, result, tt.wantJSON)
				}
			}
		})
	}
}

func TestExtractJSON_Empty(t *testing.T) {
	result := ExtractJSON("")
	if result != "" {
		t.Errorf("ExtractJSON(\"\") = %q, want \"\"", result)
	}
}

func TestDetectFamily_Unknown(t *testing.T) {
	if f := DetectFamily("unknown-model-v1"); f != FamilyDefault {
		t.Errorf("DetectFamily(unknown) = %s, want %s", f, FamilyDefault)
	}
}

func TestDetectFamily_Empty(t *testing.T) {
	if f := DetectFamily(""); f != FamilyDefault {
		t.Errorf("DetectFamily(\"\") = %s, want %s", f, FamilyDefault)
	}
}

func TestSanitizeSchema_NestedRef(t *testing.T) {
	// Schema with $ref pointing to a definition that itself has nested properties
	schema := json.RawMessage(`{
		"$ref": "#/$defs/Address",
		"$defs": {
			"Address": {
				"type": "object",
				"properties": {
					"street": {"type": "string"},
					"city": {"$ref": "#/$defs/City"}
				}
			},
			"City": {
				"type": "object",
				"properties": {
					"name": {"type": "string"}
				}
			}
		}
	}`)
	result := SanitizeSchemaForOpenAI(schema)
	if !json.Valid(result) {
		t.Errorf("SanitizeSchemaForOpenAI produced invalid JSON: %s", result)
	}
	var m map[string]any
	_ = json.Unmarshal(result, &m)
	if m["type"] != "object" {
		t.Errorf("Expected type object, got %v", m["type"])
	}
}
