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
				return New(TierLarge).Core("system prompt").Build()
			},
			expected: "system prompt",
		},
		{
			name: "multiple core sections",
			build: func() string {
				return New(TierSmall).
					Core("section one").
					Core("section two").
					Build()
			},
			expected: "section one\n\nsection two",
		},
		{
			name: "large tier includes large-only sections",
			build: func() string {
				return New(TierLarge).
					Core("core content").
					ForLarge("large only content").
					Build()
			},
			expected: "core content\n\nlarge only content",
		},
		{
			name: "large tier excludes small-only sections",
			build: func() string {
				return New(TierLarge).
					Core("core content").
					ForSmall("small only content").
					Build()
			},
			expected: "core content",
		},
		{
			name: "small tier includes small-only sections",
			build: func() string {
				return New(TierSmall).
					Core("core content").
					ForSmall("small only content").
					Build()
			},
			expected: "core content\n\nsmall only content",
		},
		{
			name: "small tier excludes large-only sections",
			build: func() string {
				return New(TierSmall).
					Core("core content").
					ForLarge("large only content").
					Build()
			},
			expected: "core content",
		},
		{
			name: "adaptive picks large content for large tier",
			build: func() string {
				return New(TierLarge).
					Core("core").
					Adaptive("detailed instructions", "brief instructions").
					Build()
			},
			expected: "core\n\ndetailed instructions",
		},
		{
			name: "adaptive picks small content for small tier",
			build: func() string {
				return New(TierSmall).
					Core("core").
					Adaptive("detailed instructions", "brief instructions").
					Build()
			},
			expected: "core\n\nbrief instructions",
		},
		{
			name: "single substitution",
			build: func() string {
				return New(TierLarge).
					Core("Hello {{NAME}}!").
					Replace("{{NAME}}", "World").
					Build()
			},
			expected: "Hello World!",
		},
		{
			name: "multiple substitutions",
			build: func() string {
				return New(TierLarge).
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
				return New(TierLarge).
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
			name: "combined core tier specific and substitutions",
			build: func() string {
				return New(TierLarge).
					Core("You are {{ROLE}}.").
					ForLarge("Use detailed reasoning.").
					Replace("{{ROLE}}", "an AI assistant").
					Build()
			},
			expected: "You are an AI assistant.\n\nUse detailed reasoning.",
		},
		{
			name: "empty content sections are skipped",
			build: func() string {
				return New(TierLarge).
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
				return New(TierLarge).Build()
			},
			expected: "",
		},
		{
			name: "all sections empty produces empty string",
			build: func() string {
				return New(TierLarge).
					Core("").
					ForLarge("").
					Build()
			},
			expected: "",
		},
		{
			name: "substitution on empty result",
			build: func() string {
				return New(TierLarge).
					Replace("{{PLACEHOLDER}}", "value").
					Build()
			},
			expected: "",
		},
		{
			name: "sections joined with double newline",
			build: func() string {
				return New(TierLarge).
					Core("a").
					Core("b").
					Core("c").
					Build()
			},
			expected: "a\n\nb\n\nc",
		},
		{
			name: "mixed sections with correct tier selection",
			build: func() string {
				return New(TierSmall).
					Core("core").
					ForLarge("large section").
					ForSmall("small section").
					Adaptive("large adaptive", "small adaptive").
					Build()
			},
			expected: "core\n\nsmall section\n\nsmall adaptive",
		},
		{
			name: "substitution replaces all occurrences",
			build: func() string {
				return New(TierLarge).
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
	b := New(TierLarge)

	if b.Core("a") != b {
		t.Error("Core() should return the same builder")
	}
	if b.ForLarge("b") != b {
		t.Error("ForLarge() should return the same builder")
	}
	if b.ForSmall("c") != b {
		t.Error("ForSmall() should return the same builder")
	}
	if b.Adaptive("d", "e") != b {
		t.Error("Adaptive() should return the same builder")
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
	b := New(TierLarge).Core("{{A}}").ReplaceAll(subs)
	subs["{{A}}"] = "C" // Modify original map

	result := b.Build()
	if strings.Contains(result, "C") {
		t.Error("Builder should not be affected by modifications to input map after ReplaceAll")
	}
}

func TestModelTierValues(t *testing.T) {
	t.Parallel()

	if TierLarge != "large" {
		t.Errorf("TierLarge = %q, want %q", TierLarge, "large")
	}
	if TierSmall != "small" {
		t.Errorf("TierSmall = %q, want %q", TierSmall, "small")
	}
}
