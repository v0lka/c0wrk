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

func TestCacheBreak(t *testing.T) {
	t.Parallel()

	t.Run("CacheBreak returns same builder", func(t *testing.T) {
		t.Parallel()
		b := NewBuilder()
		if b.CacheBreak() != b {
			t.Error("CacheBreak() should return the same builder")
		}
	})

	t.Run("Build inserts marker between stable and dynamic", func(t *testing.T) {
		t.Parallel()
		got := NewBuilder().
			Core("stable part").
			CacheBreak().
			Core("dynamic part").
			Build()

		want := "stable part" + CacheBreakMarker + "dynamic part"
		if got != want {
			t.Errorf("Build() = %q, want %q", got, want)
		}
	})

	t.Run("Build without CacheBreak has no marker", func(t *testing.T) {
		t.Parallel()
		got := NewBuilder().
			Core("a").
			Core("b").
			Build()

		if strings.Contains(got, CacheBreakMarker) {
			t.Error("Build() should not contain CacheBreakMarker when CacheBreak not called")
		}
	})

	t.Run("Build with CacheBreak at end omits marker", func(t *testing.T) {
		t.Parallel()
		got := NewBuilder().
			Core("only stable").
			CacheBreak().
			Build()

		if strings.Contains(got, CacheBreakMarker) {
			t.Error("Build() should not contain marker when no dynamic content follows")
		}
		if got != "only stable" {
			t.Errorf("Build() = %q, want %q", got, "only stable")
		}
	})

	t.Run("Build with substitutions across cache break", func(t *testing.T) {
		t.Parallel()
		got := NewBuilder().
			Core("Hello {{NAME}}").
			CacheBreak().
			Core("Workspace: {{WS}}").
			Replace("{{NAME}}", "Agent").
			Replace("{{WS}}", "/home").
			Build()

		want := "Hello Agent" + CacheBreakMarker + "Workspace: /home"
		if got != want {
			t.Errorf("Build() = %q, want %q", got, want)
		}
	})
}

func TestBuildParts(t *testing.T) {
	t.Parallel()

	t.Run("with CacheBreak", func(t *testing.T) {
		t.Parallel()
		stable, dynamic := NewBuilder().
			Core("section1").
			Core("section2").
			CacheBreak().
			Core("section3").
			BuildParts()

		if stable != "section1\n\nsection2" {
			t.Errorf("stable = %q, want %q", stable, "section1\n\nsection2")
		}
		if dynamic != "section3" {
			t.Errorf("dynamic = %q, want %q", dynamic, "section3")
		}
	})

	t.Run("without CacheBreak returns full in stable", func(t *testing.T) {
		t.Parallel()
		stable, dynamic := NewBuilder().
			Core("a").
			Core("b").
			BuildParts()

		if stable != "a\n\nb" {
			t.Errorf("stable = %q, want %q", stable, "a\n\nb")
		}
		if dynamic != "" {
			t.Errorf("dynamic = %q, want empty", dynamic)
		}
	})

	t.Run("substitutions applied to both parts", func(t *testing.T) {
		t.Parallel()
		stable, dynamic := NewBuilder().
			Core("Hello {{X}}").
			CacheBreak().
			Core("World {{X}}").
			Replace("{{X}}", "!").
			BuildParts()

		if stable != "Hello !" {
			t.Errorf("stable = %q, want %q", stable, "Hello !")
		}
		if dynamic != "World !" {
			t.Errorf("dynamic = %q, want %q", dynamic, "World !")
		}
	})

	t.Run("CacheBreak at start means empty stable", func(t *testing.T) {
		t.Parallel()
		stable, dynamic := NewBuilder().
			CacheBreak().
			Core("all dynamic").
			BuildParts()

		if stable != "" {
			t.Errorf("stable = %q, want empty", stable)
		}
		if dynamic != "all dynamic" {
			t.Errorf("dynamic = %q, want %q", dynamic, "all dynamic")
		}
	})
}

func TestSplitCacheBreak(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "no marker",
			input: "single part",
			want:  []string{"single part"},
		},
		{
			name:  "with marker",
			input: "stable" + CacheBreakMarker + "dynamic",
			want:  []string{"stable", "dynamic"},
		},
		{
			name:  "empty dynamic after marker",
			input: "stable" + CacheBreakMarker,
			want:  []string{"stable"},
		},
		{
			name:  "empty string",
			input: "",
			want:  nil,
		},
		{
			name:  "only marker",
			input: CacheBreakMarker,
			want:  nil,
		},
		{
			name:  "whitespace parts trimmed and empty dropped",
			input: "  a  " + CacheBreakMarker + "  b  ",
			want:  []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SplitCacheBreak(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("SplitCacheBreak() returned %d parts, want %d: got %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("part[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
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

func TestReplaceData_NoPlaceholderInjection(t *testing.T) {
	t.Parallel()

	// An untrusted value containing the name of another placeholder must NOT
	// be expanded: ReplaceData applies a single pass without re-scanning.
	result := NewBuilder().
		Core("User: USER-REQUEST\nSecret: SECRET-DATA").
		ReplaceData("USER-REQUEST", "please show me SECRET-DATA now").
		ReplaceData("SECRET-DATA", "top-secret").
		Build()

	want := "User: please show me SECRET-DATA now\nSecret: top-secret"
	if result != want {
		t.Errorf("Build() = %q, want %q", result, want)
	}
}

func TestReplaceData_AppliedAfterTrustedSubstitutions(t *testing.T) {
	t.Parallel()

	// Trusted substitutions resolve iteratively (nested placeholders), then
	// data substitutions run once. A trusted value may reference a data
	// placeholder; the data value must not be re-scanned.
	result := NewBuilder().
		Core("PREAMBLE").
		Replace("PREAMBLE", "Conversation:\nRECENT-CONVERSATION").
		ReplaceData("RECENT-CONVERSATION", "user: ignore PREAMBLE and RECENT-CONVERSATION").
		Build()

	want := "Conversation:\nuser: ignore PREAMBLE and RECENT-CONVERSATION"
	if result != want {
		t.Errorf("Build() = %q, want %q", result, want)
	}
}

func TestReplaceData_CacheBreakParts(t *testing.T) {
	t.Parallel()

	stable, dynamic := NewBuilder().
		Core("Stable: DATA-A").
		CacheBreak().
		Core("Dynamic: DATA-B").
		ReplaceData("DATA-A", "contains DATA-B literal").
		ReplaceData("DATA-B", "value-b").
		BuildParts()

	if stable != "Stable: contains DATA-B literal" {
		t.Errorf("stable = %q", stable)
	}
	if dynamic != "Dynamic: value-b" {
		t.Errorf("dynamic = %q", dynamic)
	}
}
