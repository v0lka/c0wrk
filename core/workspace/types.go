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

// CommitInfo describes a single commit in the repository history.
type CommitInfo struct {
	SHA     string `json:"sha"`
	Author  string `json:"author"`
	Email   string `json:"email"`
	Date    string `json:"date"`
	Message string `json:"message"`
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

// GraphCommit describes a single commit for graph visualization. Parents
// holds the full SHAs of the commit's parents (empty for the root
// commit); the frontend computes lane layout from these. Refs lists the
// decorated ref names git attaches to the commit (e.g. "HEAD -> main",
// "tag: v1.0"), empty when none.
type GraphCommit struct {
	SHA     string   `json:"sha"`
	Parents []string `json:"parents"`
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

// MergeRebaseState reports whether a merge or rebase is currently in
// progress in the repository. The frontend uses it to reveal abort
// controls in the git panel toolbar. Both flags are false when the
// repository is in a clean (non in-progress) state.
type MergeRebaseState struct {
	IsMerging  bool `json:"is_merging"`
	IsRebasing bool `json:"is_rebasing"`
}
