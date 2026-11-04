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

// GitStatusEntry describes the git status of a single file as reported by git
// status --porcelain. When both index and work tree are modified the legacy
// Status/Staged fields reflect the index side; read IndexStatus/WorkTreeStatus for both.
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

// Branch represents a git branch: Kind "local" (refs/heads/) or "remote"
// (refs/remotes/); IsCurrent only for the checked-out local branch; Upstream
// the short upstream name, empty when none configured.
type Branch struct {
	Name      string `json:"name"`
	IsCurrent bool   `json:"is_current"`
	Kind      string `json:"kind"`
	Upstream  string `json:"upstream"`
}

// BranchInfo describes the checked-out branch and its upstream tracking state:
// Ahead = local commits missing upstream, Behind = upstream not yet integrated;
// Upstream empty when unconfigured (incl. detached HEAD).
type BranchInfo struct {
	Name     string `json:"name"`
	Upstream string `json:"upstream"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
}

// BranchBase is a ref usable as a `git checkout -b` start-point; Type
// groups it for the UI, Detail carries the commit subject (commits only).
type BranchBase struct {
	Ref    string `json:"ref"`    // git ref: "main", "origin/main", "v1.0", "a3f5c1d"
	Label  string `json:"label"`  // display: same as Ref for branches/tags; short SHA for commits
	Type   string `json:"type"`   // "local" | "remote" | "tag" | "commit"
	Detail string `json:"detail"` // commit subject for commits; "" for branches/tags
}

// CommitFile is a single file changed by a commit: Status the git
// name-status letter, Path the post-commit path (rename/copy destination).
type CommitFile struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

// StashEntry describes a single entry in the stash list.
type StashEntry struct {
	Index   int    `json:"index"`
	Message string `json:"message"`
}

// GitHistoryCommit is one commit of the unified history+graph view: log
// fields and graph topology (parents/refs) from a single source.
type GitHistoryCommit struct {
	SHA     string   `json:"sha"`
	Parents []string `json:"parents"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
	Date    string   `json:"date"`
	Message string   `json:"message"`
	Refs    []string `json:"refs"`
}

// GitHistoryPage is one page of the unified commit history, loaded incrementally
// (Skip = NextSkip while HasMore); HasMore is page saturation (len == limit).
type GitHistoryPage struct {
	Commits  []GitHistoryCommit `json:"commits"`
	NextSkip int                `json:"next_skip"`
	HasMore  bool               `json:"has_more"`
}

// HunkDiffInfo is one diff hunk with its staging status and raw text, for the
// frontend's per-hunk stage/unstage/discard controls. The Start/Count fields
// mirror the @@ header (they include context — the first context line, not the
// first changed line); OldChangeStart/NewChangeStart walk the body to the first
// '+'/'-' line in each file's coordinates. Staged = from git diff --cached; Diff is header + body.
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

// MergeRebaseState reports an in-progress merge or rebase (frontend abort
// controls); both flags false in a clean state.
type MergeRebaseState struct {
	IsMerging  bool `json:"is_merging"`
	IsRebasing bool `json:"is_rebasing"`
}

// ReviewHunk is one unified-diff hunk for the code-review page: @@ header
// numbers plus the raw block; a display-oriented snapshot, untied to staging.
type ReviewHunk struct {
	Raw      string `json:"raw"`       // full hunk block: "@@ ..." header + body lines
	OldStart int    `json:"old_start"` // first old-file line (1-based, includes context)
	OldCount int    `json:"old_count"` // number of old-file lines in the hunk
	NewStart int    `json:"new_start"` // first new-file line (1-based, includes context)
	NewCount int    `json:"new_count"` // number of new-file lines in the hunk
}

// ReviewFileDiff groups the uncommitted hunks of one file for the code-review
// page: Path post-rename, OldPath only when a/ and b/ differ, Hunks empty on pure rename.
type ReviewFileDiff struct {
	Path    string       `json:"path"`
	OldPath string       `json:"old_path,omitempty"`
	Hunks   []ReviewHunk `json:"hunks"`
}
