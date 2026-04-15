package prompt

import (
	"strings"
	"testing"
)

func TestBuilder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		build    func() string
		expected string
	}{
		{
			name: "single core section",
			build: func() string {
				return NewBuilder().Core("system prompt").Build()
			},
			expected: "system prompt",
		},
		{
			name: "multiple core sections",
			build: func() string {
				return NewBuilder().
					Core("section one").
					Core("section two").
					Build()
			},
			expected: "section one\n\nsection two",
		},
		{
			name: "single substitution",
			build: func() string {
				return NewBuilder().
					Core("Hello {{NAME}}!").
					Replace("{{NAME}}", "World").
					Build()
			},
			expected: "Hello World!",
		},
		{
			name: "multiple substitutions",
			build: func() string {
				return NewBuilder().
					Core("Hello {{NAME}}, you are {{AGE}} years old.").
					Replace("{{NAME}}", "Alice").
					Replace("{{AGE}}", "30").
					Build()
			},
			expected: "Hello Alice, you are 30 years old.",
		},
		{
			name: "ReplaceAll substitutions",
			build: func() string {
				return NewBuilder().
					Core("Hello {{NAME}}, welcome to {{PLACE}}!").
					ReplaceAll(map[string]string{
						"{{NAME}}":  "Bob",
						"{{PLACE}}": "Wonderland",
					}).
					Build()
			},
			expected: "Hello Bob, welcome to Wonderland!",
		},
		{
			name: "combined core and substitutions",
			build: func() string {
				return NewBuilder().
					Core("You are {{ROLE}}.").
					Core("Use detailed reasoning.").
					Replace("{{ROLE}}", "an AI assistant").
					Build()
			},
			expected: "You are an AI assistant.\n\nUse detailed reasoning.",
		},
		{
			name: "empty content sections are skipped",
			build: func() string {
				return NewBuilder().
					Core("first").
					Core("").
					Core("second").
					Build()
			},
			expected: "first\n\nsecond",
		},
		{
			name: "empty builder produces empty string",
			build: func() string {
				return NewBuilder().Build()
			},
			expected: "",
		},
		{
			name: "all sections empty produces empty string",
			build: func() string {
				return NewBuilder().
					Core("").
					Build()
			},
			expected: "",
		},
		{
			name: "substitution on empty result",
			build: func() string {
				return NewBuilder().
					Replace("{{PLACEHOLDER}}", "value").
					Build()
			},
			expected: "",
		},
		{
			name: "sections joined with double newline",
			build: func() string {
				return NewBuilder().
					Core("a").
					Core("b").
					Core("c").
					Build()
			},
			expected: "a\n\nb\n\nc",
		},
		{
			name: "substitution replaces all occurrences",
			build: func() string {
				return NewBuilder().
					Core("{{X}} and {{X}} and {{X}}").
					Replace("{{X}}", "Y").
					Build()
			},
			expected: "Y and Y and Y",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.build()
			if got != tt.expected {
				t.Errorf("Build() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestBuilderChaining(t *testing.T) {
	t.Parallel()

	// Verify that all builder methods return the same builder instance for chaining
	b := NewBuilder()

	if b.Core("a") != b {
		t.Error("Core() should return the same builder")
	}
	if b.Replace("{{X}}", "Y") != b {
		t.Error("Replace() should return the same builder")
	}
	if b.ReplaceAll(map[string]string{"{{Z}}": "W"}) != b {
		t.Error("ReplaceAll() should return the same builder")
	}
}

func TestBuilderImmutabilityOfSlices(t *testing.T) {
	t.Parallel()

	// Verify that modifying the input map after ReplaceAll doesn't affect the builder
	subs := map[string]string{"{{A}}": "B"}
	b := NewBuilder().Core("{{A}}").ReplaceAll(subs)
	subs["{{A}}"] = "C" // Modify original map

	result := b.Build()
	if strings.Contains(result, "C") {
		t.Error("Builder should not be affected by modifications to input map after ReplaceAll")
	}
}
