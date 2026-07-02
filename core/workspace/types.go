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
