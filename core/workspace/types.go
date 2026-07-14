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

// Branch represents a local git branch.
type Branch struct {
	Name      string `json:"name"`
	IsCurrent bool   `json:"is_current"`
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

// HunkRange identifies a contiguous slice of a file in old-file (pre-diff)
// line coordinates. StartLine and EndLine are 1-based and inclusive;
// EndLine is StartLine + hunkLineCount - 1 (so a pure-addition hunk with
// zero old lines has EndLine == StartLine - 1).
type HunkRange struct {
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
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
