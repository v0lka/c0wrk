// Package workspace manages the workspace directory tree, file system
// watching, and workspace-level state for the desktop UI.
package workspace

// FileNode represents a file or directory entry in the workspace tree.
type FileNode struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	IsDir      bool   `json:"is_dir"`
	Icon       string `json:"icon"`
	IconColor  string `json:"icon_color"`
	Hidden     bool   `json:"hidden"`
	GitIgnored bool   `json:"gitignored"`
}

// GitStatusEntry describes the git status of a single file.
// It captures both the index (staged) and work-tree (unstaged) status
// as reported by git status --porcelain.  When both the index and the
// work tree have modifications (e.g. "MM") the legacy Status/Staged
// fields reflect the index side for backward compatibility with
// existing consumers; callers that need both should read IndexStatus
// and WorkTreeStatus directly.
type GitStatusEntry struct {
	Status         string `json:"status"`          // legacy primary status char: "M", "A", "R", "C", or "U"
	Staged         bool   `json:"staged"`          // legacy: true=index, false=worktree
	IndexStatus    string `json:"index_status"`    // status in the index (staged): "M", "A", "R", "C", "U", "?" or ""
	WorkTreeStatus string `json:"worktree_status"` // status in the work tree (unstaged): "M", "A", "R", "C", "U", "?" or ""
}

// DiffStat reports the number of added and deleted lines in a diff.
type DiffStat struct {
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
}

// Branch represents a git branch, local or remote. Kind is "local" for
// refs under refs/heads/ and "remote" for refs under refs/remotes/.
// IsCurrent is true only for the currently checked-out local branch.
// Upstream holds the short name of the branch's upstream (e.g. "origin/main")
// and is empty when none is configured (including remote branches).
type Branch struct {
	Name      string `json:"name"`
	IsCurrent bool   `json:"is_current"`
	Kind      string `json:"kind"`
	Upstream  string `json:"upstream"`
}

// BranchInfo describes the currently checked-out branch together with its
// upstream tracking state (ahead/behind counts).  Ahead is the number of
// local commits not present on the upstream; Behind is the number of
// upstream commits not yet integrated locally.  Upstream is empty when no
// upstream is configured (including detached HEAD).
type BranchInfo struct {
	Name     string `json:"name"`
	Upstream string `json:"upstream"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
}

// BranchBase represents a ref that can serve as a start-point for
// `git checkout -b`. Type distinguishes the ref category so the UI can
// group and label items. Detail holds the commit subject for commits
// (empty for branches and tags).
type BranchBase struct {
	Ref    string `json:"ref"`    // git ref: "main", "origin/main", "v1.0", "a3f5c1d"
	Label  string `json:"label"`  // display: same as Ref for branches/tags; short SHA for commits
	Type   string `json:"type"`   // "local" | "remote" | "tag" | "commit"
	Detail string `json:"detail"` // commit subject for commits; "" for branches/tags
}

// CommitFile describes a single file changed by a commit.  Status is the
// git name-status letter ("A" added, "M" modified, "D" deleted, "R"
// renamed, "C" copied).  Path is the post-commit path (the destination
// for renames/copies).
type CommitFile struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

// StashEntry describes a single entry in the stash list.
type StashEntry struct {
	Index   int    `json:"index"`
	Message string `json:"message"`
}

// GitHistoryCommit describes a single commit for the unified history+graph
// view. It carries both the human-readable log fields (author/email/date)
// and the graph topology fields (parents/refs) so the frontend can render
// lane topology and expandable commit details from a single source.
type GitHistoryCommit struct {
	SHA     string   `json:"sha"`
	Parents []string `json:"parents"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
	Date    string   `json:"date"`
	Message string   `json:"message"`
	Refs    []string `json:"refs"`
}

// HunkDiffInfo describes a single diff hunk with its staging status and
// raw unified-diff text. The frontend uses it to render per-hunk controls
// (stage / unstage / discard) and hover tooltips with syntax-highlighted
// diff content. OldStart/OldCount and NewStart/NewCount mirror the
// unified-diff hunk header fields — these include context lines and
// therefore point to the first context line, not the first changed line.
// OldChangeStart/NewChangeStart are computed by walking the hunk body and
// identify the first actually-changed line (first '+' or '-' line) in
// old-file and new-file coordinates respectively. Staged is true for hunks
// that appear in git diff --cached (HEAD vs index) and false for hunks in
// git diff (index vs worktree). Diff is the hunk's unified-diff block
// (header + body) suitable for re-parsing or syntax highlighting.
type HunkDiffInfo struct {
	OldStart       int    `json:"old_start"`
	OldCount       int    `json:"old_count"`
	NewStart       int    `json:"new_start"`
	NewCount       int    `json:"new_count"`
	OldChangeStart int    `json:"old_change_start"`
	NewChangeStart int    `json:"new_change_start"`
	Staged         bool   `json:"staged"`
	Diff           string `json:"diff"`
}

// MergeRebaseState reports whether a merge or rebase is currently in
// progress in the repository. The frontend uses it to reveal abort
// controls in the git panel toolbar. Both flags are false when the
// repository is in a clean (non in-progress) state.
type MergeRebaseState struct {
	IsMerging  bool `json:"is_merging"`
	IsRebasing bool `json:"is_rebasing"`
}

// ReviewHunk describes a single unified-diff hunk for the code-review
// page. It mirrors the "@@" header line numbers and carries the raw hunk
// block (header + body) for rendering. Unlike HunkDiffInfo (which is tied
// to the git panel's per-hunk staging controls), ReviewHunk is a minimal,
// display-oriented snapshot of an uncommitted change region.
type ReviewHunk struct {
	Raw      string `json:"raw"`       // full hunk block: "@@ ..." header + body lines
	OldStart int    `json:"old_start"` // first old-file line (1-based, includes context)
	OldCount int    `json:"old_count"` // number of old-file lines in the hunk
	NewStart int    `json:"new_start"` // first new-file line (1-based, includes context)
	NewCount int    `json:"new_count"` // number of new-file lines in the hunk
}

// ReviewFileDiff groups the uncommitted hunks of a single file for the
// code-review page. Path is the current (post-rename) path; OldPath is
// populated only for renames/copies where the a/ and b/ sides of the diff
// header differ. Hunks is empty for a pure rename with no content change.
type ReviewFileDiff struct {
	Path    string       `json:"path"`
	OldPath string       `json:"old_path,omitempty"`
	Hunks   []ReviewHunk `json:"hunks"`
}
