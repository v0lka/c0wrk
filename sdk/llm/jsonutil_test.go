package llm

import "testing"

func TestExtractJSON_PlainJSON(t *testing.T) {
	input := `{"mode":"react","domain":"code"}`
	result := ExtractJSON(input)
	if result != input {
		t.Errorf("expected '%s', got '%s'", input, result)
	}
}

func TestExtractJSON_JSONInCodeBlock(t *testing.T) {
	input := "```json\n{\"mode\":\"react\"}\n```"
	expected := `{"mode":"react"}`
	result := ExtractJSON(input)
	if result != expected {
		t.Errorf("expected '%s', got '%s'", expected, result)
	}
}

func TestExtractJSON_JSONInCodeBlockWithoutLanguage(t *testing.T) {
	input := "```\n{\"mode\":\"react\"}\n```"
	expected := `{"mode":"react"}`
	result := ExtractJSON(input)
	if result != expected {
		t.Errorf("expected '%s', got '%s'", expected, result)
	}
}
