package core

import (
	"strings"
	"testing"
)

// TestPreprocessMessageText verifies the user-message preprocessor. It focuses
// on the @file → fileref:// conversion, including GitHub-style line/line-range
// anchors (#L20, #L20-L36, #L5-10), legacy bare-number anchors (#42, #5-10),
// the single-quoted (@'my file.go') and legacy escaped-space (@my\ file.go)
// path forms, plain paths, /-mention stripping (plain and collision-qualified
// forms for both skills and agents, ADR-076), the "/goal" command
// interaction, and multi-space collapsing.
func TestPreprocessMessageText(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		activeSkills []string
		activeAgents []string
		activeMCP    []string
		workspace    string
		want         string
	}{
		// GitHub-style L-anchored refs (the core regression this test guards).
		{
			name: "L-range anchor with nested path",
			text: "@desktop/x.go#L20-L36",
			want: "fileref://desktop/x.go#L20-L36",
		},
		{
			name: "L-prefixed start, bare-number end (#L5-10)",
			text: "@x.go#L5-10",
			want: "fileref://x.go#L5-10",
		},
		{
			name: "L-prefixed start and end (#L5-L10)",
			text: "@x.go#L5-L10",
			want: "fileref://x.go#L5-L10",
		},
		{
			name: "single L-anchored line (#L42)",
			text: "@x.go#L42",
			want: "fileref://x.go#L42",
		},

		// Legacy bare-number anchors must keep working unchanged.
		{
			name: "single bare-number line (#42)",
			text: "@x.go#42",
			want: "fileref://x.go#42",
		},
		{
			name: "bare-number range (#5-10)",
			text: "@x.go#5-10",
			want: "fileref://x.go#5-10",
		},

		// Plain path with no anchor.
		{
			name: "plain path no anchor",
			text: "@x.go",
			want: "fileref://x.go",
		},

		// Single-quoted form (@'file name') — the canonical form for paths
		// with spaces. The anchor may follow the closing quote or sit inside
		// the quotes; both positions must work.
		{
			name: "quoted path with spaces",
			text: "@'my file.go'",
			want: "fileref://my file.go",
		},
		{
			name: "quoted path inside prose",
			text: "see @'my file.go' here",
			want: "see fileref://my file.go here",
		},
		{
			name: "quoted path with anchor after closing quote",
			text: "@'my file.go'#L20-L36",
			want: "fileref://my file.go#L20-L36",
		},
		{
			name: "quoted path with anchor inside quotes",
			text: "@'my file.go#L20'",
			want: "fileref://my file.go#L20",
		},
		{
			name:      "quoted relative path resolved against workspace",
			text:      "see @'docs/my file.md' here",
			workspace: "/ws",
			want:      "see fileref:///ws/docs/my file.md here",
		},
		{
			name:      "quoted absolute path left unchanged",
			text:      "@'/abs/path/with spaces/x.go'",
			workspace: "/ws",
			want:      "fileref:///abs/path/with spaces/x.go",
		},

		// Legacy backslash-escaped form keeps working unchanged.
		{
			name: "legacy escaped-space path",
			text: "@my\\ file.go",
			want: "fileref://my file.go",
		},

		// Ref embedded in surrounding prose preserves the prose and surrounding spaces.
		{
			name: "anchored ref inside prose",
			text: "see @x.go#L20-L36 here",
			want: "see fileref://x.go#L20-L36 here",
		},

		// /skill stripping still behaves: the skill ref and its surrounding spaces
		// collapse to a single space.
		{
			name:         "skill strip collapses surrounding spaces",
			text:         "hello /explore world",
			activeSkills: []string{"explore"},
			want:         "hello world",
		},
		{
			name:         "skill strip at start leaves no leading space",
			text:         "/explore do stuff",
			activeSkills: []string{"explore"},
			want:         "do stuff",
		},

		// Plain /agent-name stripping: agents share the "/" trigger with
		// skills (ADR-076), matched case-sensitively against the
		// activeAgents catalog and preserving whitespace boundaries.
		{
			name:         "plain agent strip collapses surrounding spaces",
			text:         "hello /code-reviewer world",
			activeAgents: []string{"code-reviewer"},
			want:         "hello world",
		},
		{
			name:         "plain agent strip at start leaves no leading space",
			text:         "/code-reviewer do stuff",
			activeAgents: []string{"code-reviewer"},
			want:         "do stuff",
		},
		{
			name:         "multiple plain agent mentions stripped",
			text:         "/code-reviewer and /test-writer please",
			activeAgents: []string{"code-reviewer", "test-writer"},
			want:         "and please",
		},
		{
			name:         "plain agent strip with newline boundary",
			text:         "/code-reviewer\ndo stuff",
			activeAgents: []string{"code-reviewer"},
			want:         "do stuff",
		},
		{
			name:         "plain mention is case-sensitive",
			text:         "/Code-Reviewer stays",
			activeAgents: []string{"code-reviewer"},
			want:         "/Code-Reviewer stays",
		},
		{
			name:         "longer token not partially stripped",
			text:         "/code-reviewer-extra stays",
			activeAgents: []string{"code-reviewer"},
			want:         "/code-reviewer-extra stays",
		},
		{
			name:         "mention followed by punctuation stays",
			text:         "see /explore, then rest",
			activeSkills: []string{"explore"},
			want:         "see /explore, then rest",
		},
		{
			name:         "slash inside a word is not a mention",
			text:         "path/like stays",
			activeSkills: []string{"like"},
			want:         "path/like stays",
		},
		// A name present in BOTH catalogs is stripped exactly once — the
		// strip is kind-agnostic; partitioning is the send path's job.
		{
			name:         "plain name in both catalogs strips once",
			text:         "/shared start fresh",
			activeSkills: []string{"shared"},
			activeAgents: []string{"shared"},
			want:         "start fresh",
		},

		// Collision-qualified forms (ADR-076): "/agent: id" and "/skill: id",
		// with or without the space after the colon, strip the whole ref
		// when the id is in the marker's own catalog.
		{
			name:         "qualified agent spaced strip",
			text:         "/agent: code-reviewer do stuff",
			activeAgents: []string{"code-reviewer"},
			want:         "do stuff",
		},
		{
			name:         "qualified agent unspaced strip",
			text:         "/agent:code-reviewer do stuff",
			activeAgents: []string{"code-reviewer"},
			want:         "do stuff",
		},
		{
			name:         "qualified skill spaced strip",
			text:         "hello /skill: deep-dive world",
			activeSkills: []string{"deep-dive"},
			want:         "hello world",
		},
		{
			name:         "qualified skill unspaced strip",
			text:         "hello /skill:deep-dive world",
			activeSkills: []string{"deep-dive"},
			want:         "hello world",
		},
		{
			name:         "qualified refs of both kinds in one message",
			text:         "please /agent: code-reviewer and /skill: deep-dive go",
			activeAgents: []string{"code-reviewer"},
			activeSkills: []string{"deep-dive"},
			want:         "please and go",
		},
		// Fail-closed qualified forms: only the marker's own catalog counts.
		// A cross-catalog, unknown, or non-boundary id is preserved verbatim.
		{
			name:         "qualified agent id in skill catalog stays",
			text:         "/agent: shared do stuff",
			activeSkills: []string{"shared"},
			want:         "/agent: shared do stuff",
		},
		{
			name:         "qualified skill id in agent catalog stays",
			text:         "/skill: shared do stuff",
			activeAgents: []string{"shared"},
			want:         "/skill: shared do stuff",
		},
		{
			name:         "qualified unknown id stays",
			text:         "/agent: nobody knows",
			activeAgents: []string{"code-reviewer"},
			want:         "/agent: nobody knows",
		},
		{
			name:         "qualified id without trailing boundary stays",
			text:         "/agent: reviewer, please",
			activeAgents: []string{"reviewer"},
			want:         "/agent: reviewer, please",
		},
		// A marker word without the colon is an ordinary plain mention: the
		// qualified path requires ":" right after the marker token.
		{
			name:         "marker word as plain skill name strips",
			text:         "/skill activates things",
			activeSkills: []string{"skill"},
			want:         "activates things",
		},
		{
			name:         "marker word as plain agent name strips",
			text:         "/agent handles it",
			activeAgents: []string{"agent"},
			want:         "handles it",
		},
		// A qualified ref whose marker names a real profile must not be
		// partially consumed as a plain "/agent" fragment either.
		{
			name:         "marker profile qualified ref not partially stripped",
			text:         "/agent: nobody /agent does work",
			activeAgents: []string{"agent"},
			want:         "/agent: nobody does work",
		},

		// "#" is no longer a mention trigger (ADR-076): "#name" is plain
		// text even when the name is a threaded agent, and the "#" of a
		// GitHub-style @file#L20 line anchor belongs to the file ref.
		{
			name:         "hash agent mention not stripped",
			text:         "hello #code-reviewer world",
			activeAgents: []string{"code-reviewer"},
			want:         "hello #code-reviewer world",
		},
		{
			name:         "@file#L20 line anchor untouched",
			text:         "see @x.go#L20 here",
			activeAgents: []string{"L20"},
			want:         "see fileref://x.go#L20 here",
		},
		// Collision guard: "#review", "/review" and "@review" are three
		// distinct token shapes; only the "/" one is a mention now.
		{
			name:         "collision review: slash ref stripped, hash and file kept",
			text:         "#review /review @review",
			activeAgents: []string{"review"},
			activeSkills: []string{"review"},
			want:         "#review fileref://review",
		},

		// "/goal" interaction: "goal" is a command, not a catalog name, so
		// the prefix survives preprocessing (DetectAndStripGoalMode runs on
		// the processed text) — including when refs ahead of it are
		// stripped, which exposes the command at the start of the text.
		{
			name: "goal prefix preserved without catalogs",
			text: "/goal refactor the auth module",
			want: "/goal refactor the auth module",
		},
		{
			name:         "goal prefix preserved with non-empty catalogs",
			text:         "/goal refactor the auth module",
			activeAgents: []string{"code-reviewer"},
			activeSkills: []string{"explore"},
			want:         "/goal refactor the auth module",
		},
		{
			name:         "stripped plain skill ref exposes goal prefix",
			text:         "/explore /goal refactor the auth module",
			activeSkills: []string{"explore"},
			want:         "/goal refactor the auth module",
		},
		{
			name:         "stripped qualified agent ref exposes goal prefix",
			text:         "/agent: reviewer /goal ship it",
			activeAgents: []string{"reviewer"},
			want:         "/goal ship it",
		},
		{
			name:         "goal command is not a qualified marker ref",
			text:         "/goal: reviewer ship it",
			activeAgents: []string{"reviewer"},
			want:         "/goal: reviewer ship it",
		},

		// Multiple spaces collapse into one after all transforms.
		{
			name: "multi-space collapse",
			text: "hello   world",
			want: "hello world",
		},
		{
			name: "leading and trailing whitespace trimmed",
			text: "   @x.go#L5-L10   ",
			want: "fileref://x.go#L5-L10",
		},

		// Relative @file paths are resolved against the workspace so the LLM
		// prompt carries unambiguous absolute paths. The line anchor is split
		// off during resolution and re-attached verbatim.
		{
			name:      "relative path resolved against workspace",
			text:      "see @main.go here",
			workspace: "/ws",
			want:      "see fileref:///ws/main.go here",
		},
		{
			name:      "nested relative path with L-range anchor",
			text:      "@desktop/x.go#L20-L36",
			workspace: "/ws",
			want:      "fileref:///ws/desktop/x.go#L20-L36",
		},
		{
			name:      "relative path with dot segment cleaned",
			text:      "@./src/a.go#L5",
			workspace: "/ws",
			want:      "fileref:///ws/src/a.go#L5",
		},
		{
			name:      "absolute path left unchanged",
			text:      "@/abs/path/x.go",
			workspace: "/ws",
			want:      "fileref:///abs/path/x.go",
		},
		{
			name:      "home-relative path left unchanged",
			text:      "@~/notes/x.go",
			workspace: "/ws",
			want:      "fileref://~/notes/x.go",
		},
		{
			name:      "no workspace leaves relative path untouched",
			text:      "@x.go#L10",
			workspace: "",
			want:      "fileref://x.go#L10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PreprocessMessageText(tt.text, tt.activeSkills, tt.activeAgents, tt.activeMCP, tt.workspace)
			if got != tt.want {
				t.Errorf("PreprocessMessageText(%q, %v, %v, %q) =\n  got:  %q\n  want: %q",
					tt.text, tt.activeSkills, tt.activeAgents, tt.workspace, got, tt.want)
			}
		})
	}
}

// TestPreprocessMessageText_MCPMentions covers the third mention catalog:
// MCP server names strip in the plain form and the "/mcp: id" qualified form,
// with the same catalog-gated, fail-closed semantics as skills and agents.
func TestPreprocessMessageText_MCPMentions(t *testing.T) {
	mcp := []string{"server-one", "server-two"}
	cases := []struct{ name, text, want string }{
		{"plain mention stripped", "use /server-one for queries", "use for queries"},
		{"qualified spaced", "use /mcp: server-one for queries", "use for queries"},
		{"qualified unspaced", "use /mcp:server-one for queries", "use for queries"},
		{"unknown server preserved verbatim", "use /mcp: unknown-srv for queries", "use /mcp: unknown-srv for queries"},
		{"untethered path preserved", "read /etc/hosts now", "read /etc/hosts now"},
		{"both servers strip", "/server-one /server-two go", "go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PreprocessMessageText(tc.text, nil, nil, mcp, ""); got != tc.want {
				t.Errorf("PreprocessMessageText(%q, mcp=%v) =\n  got:  %q\n  want: %q", tc.text, mcp, got, tc.want)
			}
		})
	}
	// The /mcp: qualified marker resolves ONLY in the MCP catalog — an agent
	// name after it is not stripped (the send path partitioned it elsewhere).
	got := PreprocessMessageText("use /mcp: code-reviewer now", nil, []string{"code-reviewer"}, nil, "")
	if want := "use /mcp: code-reviewer now"; got != want {
		t.Errorf("qualified /mcp id must resolve only in the MCP catalog: got %q, want %q", got, want)
	}
}

// TestPreprocessMessageText_MentionBoundaries mirrors extraction/display
// regressions: a path or longer token never becomes a registered prefix ref.
func TestPreprocessMessageText_MentionBoundaries(t *testing.T) {
	skills := []string{"github", "skill"}
	agents := []string{"agentName", "agent"}
	mcp := []string{"github", "mcp"}
	preserved := []string{
		"/github/cache/config.json", "/agentName/path",
		"/github.json", "/agentName,", "/githubé",
		"/mcp:github/cache", "/mcp: github/cache",
		"/agent:agentName/path", "/agent: agentName/path",
		"/skill:github/path", "/skill: github/path",
		"/mcp:github.json", "/agent: agentName,",
		"/mcp:", "/agent: ", "/skill:  github",
		"/github\v", "\v/github", "/github\u00a0", "\u00a0/github",
		"/github\u2028", "\u2028/github", "/github\u2029", "\u2029/github",
		"/github-extra", "/github_extra", "/github2",
		"/agentName-extra", "/agentName_extra", "/agentName2",
		"/mcp: github-extra", "/agent:agentName-extra", "/skill:github-extra",
	}
	for _, token := range preserved {
		t.Run(token, func(t *testing.T) {
			// Surround with prose so final TrimSpace does not obscure whether
			// a non-ASCII/non-boundary whitespace character was preserved.
			text := "read " + token + " now"
			want := strings.ReplaceAll(text, "  ", " ")
			if got := PreprocessMessageText(text, skills, agents, mcp, ""); got != want {
				t.Errorf("PreprocessMessageText(%q, skills=%v, agents=%v, mcp=%v) = %q, want %q",
					text, skills, agents, mcp, got, want)
			}
		})
	}

	for _, space := range []string{" ", "\t", "\n", "\r", "\f"} {
		t.Run("boundary "+space, func(t *testing.T) {
			text := "before" + space + "/github" + space + "/agent:agentName" + space + "after"
			want := "before" + strings.Repeat(space, 3) + "after"
			if space == " " {
				want = "before after"
			}
			if got := PreprocessMessageText(text, nil, agents, mcp, ""); got != want {
				t.Errorf("PreprocessMessageText(%q, agents=%v, mcp=%v) = %q, want %q", text, agents, mcp, got, want)
			}
		})
	}

	for _, kind := range []string{"agent", "skill", "mcp"} {
		for _, separator := range []string{"", " ", "\t"} {
			text := "/" + kind + ":" + separator + "github-extra"
			t.Run(text, func(t *testing.T) {
				// An exact longer name is valid; it must strip as one token
				// when explicitly active, not leave an "-extra" suffix.
				catalog := []string{"github", "github-extra"}
				if got := PreprocessMessageText(text, catalog, catalog, catalog, ""); got != "" {
					t.Errorf("PreprocessMessageText(%q, all catalogs=%v) = %q, want empty", text, catalog, got)
				}
			})
		}
	}

	text := "/mcp: github/cache /agent:agentName/path /github /agentName"
	want := "/mcp: github/cache /agent:agentName/path"
	if got := PreprocessMessageText(text, nil, agents, mcp, ""); got != want {
		t.Errorf("PreprocessMessageText(%q, agents=%v, mcp=%v) = %q, want %q", text, agents, mcp, got, want)
	}
}
