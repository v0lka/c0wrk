package smallllm

import (
	sdktools "github.com/v0lka/sp4rk/tools"
)

// compactDescriptions maps builtin tool names to one-line descriptions used
// when small_llm.essential_tools.compact_descriptions is enabled. Full rubric
// descriptions (purpose/when-to-use/inputs/outputs/example/anti-example,
// 480-1100 chars) are ideal for large models but cost real prompt budget on
// small local ones; these one-liners keep the disambiguation (the "vs
// neighbor" hint) while dropping the long form. Unknown tools (e.g. MCP) keep
// their original description.
var compactDescriptions = map[string]string{
	// sp4rk builtins — files
	"read_file":        "Read a file by path (paginated; pdf/docx/html convert to markdown). Use once the exact path is known — find paths with glob/list_directory first.",
	"write_file":       "Create a file or replace its full content (parents auto-created). For partial changes to an existing file use edit_file.",
	"edit_file":        "Replace exactly one occurrence of a substring in an existing file. Fails on zero/multiple matches. For whole-file rewrites use write_file.",
	"list_directory":   "List one directory's immediate entries (name, type, size), non-recursive. For recursive name patterns use glob; for content use ripgrep.",
	"create_directory": "Create a directory with parents (mkdir -p), idempotent. Usually unnecessary before write_file, which auto-creates parents.",
	"delete_file":      "Delete a single regular file (fails on directories). Destructive — verify the path first.",
	"delete_directory": "Delete a directory; recursive=true removes all contents (rm -rf). Default requires empty — escalate to recursive only after verifying contents.",

	// sp4rk builtins — search
	"glob":            "Find files/dirs by name pattern ('*' one level, '**' recursive, e.g. **/*.go). For content search use ripgrep; for one dir's entries use list_directory.",
	"ripgrep":         "Regex/literal content search across files (file:line: matches, respects .gitignore). For meaning-based search use semantic_search; for names use glob.",
	"semantic_search": "Hybrid (vector+BM25) code search by meaning ('retry with backoff'). For exact identifiers/strings use ripgrep; for file names use glob.",

	// sp4rk builtins — execution & shell
	"bash_exec": "Run a shell command via bash -c — fallback for builds/tests/git when no dedicated tool fits. Prefer read_file/edit_file/glob/ripgrep over cat/sed/find/grep.",
	"posh_exec": "Run a PowerShell command — Windows fallback for builds/tests/git when no dedicated tool fits. Prefer dedicated file/search tools over shell equivalents.",
	"batch":     "Run several independent tool calls in one round-trip (calls: [{tool, input}]). All run in order even if some fail; dependent calls must NOT be batched.",

	// sp4rk builtins — web
	"web_fetch":  "Fetch one known HTTP(S) URL as markdown (paginated). Only user-given or search-result URLs; to discover URLs use web_search.",
	"web_search": "Web search returning {title, URL, snippet} results. For reading a known URL use web_fetch; never paste secrets into a query.",

	// sp4rk builtins — memory & workspace
	"store_fact":        "Persist a durable fact (3-5 keywords) for later retrieval across steps. Store early — before context grows; retrieve via search_facts.",
	"search_facts":      "Recall previously stored facts by keywords (ranked by relevance). For new information search the codebase instead; never reconstruct from memory.",
	"read_step_output":  "Read the full raw output of one completed plan step by ID, when its summary is insufficient.",
	"list_step_outputs": "List completed plan steps with short previews — discover step IDs before read_step_output.",
	"read_final_result": "Read the previous task's final answer from the blackboard (e.g. after a restart). For plan steps use read_step_output.",
	"update_checklist":  "Maintain the step's sub-task checklist: initialize at step start, then mark exactly one item [x] per completed sub-task. ASCII '- [ ]'/'- [x]' lines only.",
	"tool_result_read":  "Re-read fragments of a truncated tool result via its cache hash — never re-run the original tool for truncated output.",
	"read_attachment":   "Read a user-attached file's markdown by attachment_id (from the message). Not for workspace files — those go through read_file.",

	// sp4rk agent-loop tool (protected, always present)
	"finish": "Signal task completion and deliver the final answer — call exactly once, only after verifying every acceptance criterion; if any is unmet, keep working.",

	// c0wrk orchestration tools
	"ask_user":              "Ask the user questions with selectable options (single/multi-select; free text allowed) — the only channel for user-directed questions; batch related questions.",
	"execute_plan":          "Execute the approved roadmap in dependency order with per-task verification; call ONCE per plan. For a single delegated task use delegate.",
	"reflect":               "Re-evaluate and replan the strategy when attempts keep failing or the goal is already met — runtime-triggered; optional scope/delegation_id target a subagent's run.",
	"delegate":              "Launch a subagent on an independent task in its own context (parallelizable). No re-delegation unless allow-redelegate; sequential work stays inline.",
	"cancel_delegation":     "Cancel a running subagent by its delegation id — only when it clearly went the wrong way; progress is discarded.",
	"declare_plan":          "Publish the multi-step roadmap for user approval before implementation; call once; append-only after approval. Single-step tasks act directly.",
	"declare_step_complete": "Mark one inline plan step completed/failed (exactly once per step); delegated steps are tracked automatically.",
	"declare_verification":  "Record the verification-pass verdict: confirmed and reason, with concrete evidence REQUIRED when confirming. Not for mid-task checks.",
	"propose_goal":          "Propose the session's verifiable goal (condition + verify) before substantial work; skip for trivial or exploratory asks.",
	"declare_goal_status":   "Declare the terminal goal verdict (met/not_met/blocked) exactly once after verification; evidence REQUIRED for met; no tool calls afterwards.",
}

// maxCompactDescriptionLength bounds every compact one-liner so the compact
// set cannot silently regress to full-length prose.
const MaxCompactDescriptionLength = 220

// CompactDescription returns the compact one-liner for a known builtin tool,
// or "" when the tool should keep its full description.
func CompactDescription(name string) string {
	return compactDescriptions[name]
}

// ApplyCompactDescriptions returns a copy of the descriptor list with every
// known builtin's Description replaced by its compact one-liner. Descriptors
// of unknown tools (e.g. MCP-sourced) are passed through untouched. The input
// slice and its descriptors are never mutated.
func ApplyCompactDescriptions(in []sdktools.ToolDescriptor) []sdktools.ToolDescriptor {
	out := make([]sdktools.ToolDescriptor, len(in))
	copy(out, in)
	for i := range out {
		if compact, ok := compactDescriptions[out[i].Name]; ok {
			out[i].Description = compact
		}
	}
	return out
}

// MaybeCompactDescriptions applies ApplyCompactDescriptions only when enabled;
// otherwise the input slice is returned as-is (byte-identical descriptions).
func MaybeCompactDescriptions(in []sdktools.ToolDescriptor, enabled bool) []sdktools.ToolDescriptor {
	if !enabled {
		return in
	}
	return ApplyCompactDescriptions(in)
}
