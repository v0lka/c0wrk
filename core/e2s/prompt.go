package e2s

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/v0lka/sp4rk/security"
	"github.com/v0lka/sp4rk/strutil"
	sdktools "github.com/v0lka/sp4rk/tools"

	"github.com/v0lka/c0wrk/core/prompts"
)

// SkillSection is one activated skill body injected into the E2S system
// prompt; the host owns skill resolution, the loop treats the section as a
// directive (the orchestrator's "Active Skills" analog).
type SkillSection struct {
	Name        string
	Description string
	Body        string
}

// BuildSystemPrompt assembles the E2S system prompt: the core directive
// (cfg.SystemPrompt overrides — the Model Profiles Lite swap), the security
// directives, the workspace sections, the Available Tools catalog, and the
// active-skill sections. Deterministic per config + descriptors, so
// provider-side prefix caching holds across the loop's fresh one-shot
// dialogs (specs/domains/e2s.md).
func BuildSystemPrompt(cfg Config, descriptors []sdktools.ToolDescriptor) string {
	core := cfg.SystemPrompt
	if core == "" {
		core = prompts.E2SSystem
	}
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(core))
	sb.WriteString("\n\n" + strings.TrimSpace(prompts.VerificationMandate))
	if cfg.InjectionDefense {
		sb.WriteString("\n\n" + strings.TrimSpace(prompts.InjectionDefense))
	}
	sb.WriteString(workspaceSection(cfg))
	sb.WriteString(availableToolsSection(descriptors))
	sb.WriteString(skillsSection(cfg.Skills))
	return sb.String()
}

// workspaceSection renders the workspace/temp containment sections plus the
// scratchpad block: when cfg.NotesPath is set it names the file and states
// the overflow discipline — raw evidence to the file, distilled facts and
// pointers in Σ (specs/domains/e2s.md).
func workspaceSection(cfg Config) string {
	if cfg.WorkspacePath == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n## Workspace\n")
	sb.WriteString("Your session workspace is: " + cfg.WorkspacePath + "\n")
	sb.WriteString("All artifacts you create (files, directories, temporary files) MUST be placed strictly inside this workspace directory, unless the task explicitly requires creating artifacts at a specific external location.")
	if cfg.TempDir != "" {
		sb.WriteString("\nYour session temp directory is: " + cfg.TempDir + "\n")
		sb.WriteString("Use this directory for ANY intermediate files — drafts, partial results, scratch data, inter-step artifacts. These files are NOT part of the final deliverable and will be cleaned up when the session ends.")
	}
	if cfg.NotesPath != "" {
		sb.WriteString("\nYour scratchpad file is: " + cfg.NotesPath + "\n")
		sb.WriteString("The scratchpad is your overflow memory for RAW evidence — tool output, dumps, long excerpts, bulky intermediate results. Raw content longer than ~5 lines does not belong in the state: write it to the scratchpad and keep in Σ only the distilled fact plus a pointer (scratchpad path + line range); re-read the range with `read_file` when you need the raw data back. The final answer is assembled from Σ and the scratchpad together — check both before you finish.")
	}
	return sb.String()
}

// availableToolsSection renders the dispatchable tool catalog, sorted by
// name (cache-friendly); each entry carries its purpose line and the compact
// description-free input schema — without it the model guesses parameter
// names that json.Unmarshal silently ignores (specs/domains/e2s.md).
func availableToolsSection(descriptors []sdktools.ToolDescriptor) string {
	if len(descriptors) == 0 {
		return "\n\n## Available Tools\nNone — call " + FinishActionName + " with your best answer."
	}
	sorted := make([]sdktools.ToolDescriptor, len(descriptors))
	copy(sorted, descriptors)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var sb strings.Builder
	sb.WriteString("\n\n## Available Tools\n")
	fmt.Fprintf(&sb, "Actions dispatch to these tools via `%s.action.tool`. The special target %q ends the task. Each tool's `args` MUST use exactly the parameter names declared in its schema.\n", StepToolName, FinishActionName)
	for _, d := range sorted {
		sb.WriteString("- `" + d.Name + "`")
		if desc := firstLine(d.Description); desc != "" {
			sb.WriteString(": " + desc)
		}
		if d.SourceCategory == sdktools.SourceCategoryMCP {
			sb.WriteString(" [MCP]")
		}
		if schema := compactSchema(d.InputSchema); schema != "" && schema != "{}" {
			sb.WriteString("\n  args schema: " + schema)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// compactSchema renders a tool's input schema as COMPACT, description-free
// JSON: only "description" keys are stripped; property names, required,
// enums, types, and constraints survive VERBATIM — the model dispatches
// action.args by exact parameter name (ADR-040). Parsing + re-marshaling
// sorts keys and UseNumber carries numbers as literals, so the render — and
// the provider prefix cache over it — stays reproducible. Empty/unparseable
// schema renders as "".
func compactSchema(schema json.RawMessage) string {
	if strings.TrimSpace(string(schema)) == "" {
		return ""
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(schema))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return ""
	}
	data, err := json.Marshal(stripSchemaDescriptions(v))
	if err != nil {
		return ""
	}
	return string(data)
}

// stripSchemaDescriptions copies the parsed schema tree minus every
// "description" key at any depth; everything else passes through untouched.
func stripSchemaDescriptions(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if k == "description" {
				continue
			}
			out[k] = stripSchemaDescriptions(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = stripSchemaDescriptions(val)
		}
		return out
	default:
		return v
	}
}

// skillsSection renders the active-skill bodies. Skill bodies are emitted
// verbatim — truncating guidance silently degrades execution fidelity.
func skillsSection(skills []SkillSection) string {
	if len(skills) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n## Active Skills\n")
	sb.WriteString("The following skills have been activated for this task. Follow their instructions carefully.\n\n")
	for _, s := range skills {
		sb.WriteString("### Skill: " + s.Name + "\n")
		if s.Description != "" {
			sb.WriteString("Description: " + s.Description + "\n")
		}
		sb.WriteString("\n")
		sb.WriteString(s.Body)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// BuildUserMessage renders the per-turn user message — turn header, full Σₜ
// as compact JSON, latest Oₜ. It (plus the system prompt) is the ENTIRE
// request: previous turns never leak, context stays O(1). maxTurns > 0 adds
// the "[turn N of M]" header and the wrap-up directive inside the warning
// window; maxTurns <= 0 renders the legacy bare "[turn N]".
// See specs/domains/e2s.md.
func BuildUserMessage(state map[string]any, observation string, turn, maxTurns int) string {
	var sb strings.Builder
	sb.WriteString(turnHeader(turn, maxTurns))
	sb.WriteString("\n<state>\n")
	sb.WriteString(renderStateJSON(state))
	sb.WriteString("\n</state>\n")
	sb.WriteString("<observation>\n")
	sb.WriteString(observation)
	sb.WriteString("\n</observation>\n")
	return sb.String()
}

// budgetWarnFrom returns the first turn number at which the wrap-up warning
// renders: the last max(3, ceil(20% of maxTurns)) turns of the run. For
// maxTurns <= 0 (unbudgeted) the warning never renders (0 sentinel).
func budgetWarnFrom(maxTurns int) int {
	if maxTurns <= 0 {
		return 0
	}
	warnRemaining := (maxTurns + 4) / 5 // ceil(20%)
	if warnRemaining < 3 {
		warnRemaining = 3
	}
	if warnRemaining > maxTurns {
		warnRemaining = maxTurns
	}
	return maxTurns - warnRemaining + 1
}

// turnHeader renders the turn/budget line: "[turn N of M]" plus the wrap-up
// directive inside the warning window.
func turnHeader(turn, maxTurns int) string {
	if maxTurns <= 0 {
		return fmt.Sprintf("[turn %d]", turn)
	}
	remaining := maxTurns - turn
	if remaining < 0 {
		remaining = 0
	}
	if turn >= budgetWarnFrom(maxTurns) {
		return fmt.Sprintf(
			"[turn %d of %d — %d turns remain: distill what matters into your state NOW and call finish with your best answer before the budget runs out]",
			turn, maxTurns, remaining)
	}
	return fmt.Sprintf("[turn %d of %d]", turn, maxTurns)
}

// BuildCorrectionSuffix renders the corrective tail appended for the bounded
// retry after an invalid e2s_step call: the error names the violation, the
// envelope literals re-show the exact shapes so the retry copies the form.
// See specs/domains/e2s.md.
func BuildCorrectionSuffix(validationErr error) string {
	return "\n<correction>\nYour previous turn was INVALID and was not applied. Error:\n" +
		validationErr.Error() +
		"\nA valid `" + StepToolName + "` call has the exact shape " + stepEnvelope +
		"; to finish, the action is " + stepFinishEnvelope + "." +
		"\nRespond now with one corrected `" + StepToolName + "` call following the E2S protocol exactly.\n</correction>\n"
}

// untrustedWrap wraps an untrusted-source observation in untrusted-content
// boundary tags via the canonical SDK helper, so the mandatory tag-breakout
// sanitization (StripUntrustedTags) applies exactly as on the orchestrator
// path — a literal closing tag must not end the boundary early.
// See specs/architecture/security-model.md.
func untrustedWrap(source, content string) string {
	return security.WrapUntrustedContent(content, source, nil)
}

// renderStateJSON marshals the state compactly. A state that fails to
// marshal (should never happen — values come from JSON patches) is rendered
// as an error marker instead of failing the turn.
func renderStateJSON(state map[string]any) string {
	if len(state) == 0 {
		return "{}"
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Sprintf("<state marshal error: %v>", err)
	}
	return string(data)
}

// firstLine extracts the first non-empty line of a tool description.
func firstLine(desc string) string {
	for _, line := range strings.Split(desc, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return strutil.TruncateUTF8(trimmed, 160)
		}
	}
	return ""
}
